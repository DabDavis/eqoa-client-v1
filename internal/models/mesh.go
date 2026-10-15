package models

import (
	"fmt"
	"log"
	"runtime"
	"unsafe"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/eqoa/client/internal/mathutil"
	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

// triVert holds a single triangle vertex ready for GPU upload.
type triVert struct {
	X, Y, Z    float32
	U, V       float32
	NX, NY, NZ float32
	R, G, B, A float32
	BoneIdx    [4]byte    // bone indices (from esf.Vertex)
	BoneWeight [4]float32 // normalized weights
}

// matKey identifies a unique material + variant within a model.
// Variant fields ensure that base and variant meshes sharing the same
// material get separate GPU mesh groups so they can be filtered independently.
type matKey struct {
	matPalPtr uintptr       // pointer identity of MaterialPalette
	matIndex  int
	varSlot   esf.ArmorSlot // -1 for base
	varSet    byte
}

// loadModel loads a character model from the ESF file by dictionary ID.
// Returns nil if the model cannot be loaded.
func (m *ModelManager) loadModel(modelID int32) *CharModel {
	obj, err := m.file.FindObject(modelID)
	if err != nil {
		log.Printf("models: FindObject(%d): %v", modelID, err)
		return nil
	}
	if obj == nil {
		log.Printf("models: model %d not found in dictionary", modelID)
		return nil
	}

	// Resolve placements from the sprite type.
	placements := m.resolvePlacements(obj)
	if len(placements) == 0 {
		log.Printf("models: model %d has no placements", modelID)
		return nil
	}

	// Build material→TextureSlot map from the CSprite's TSlotList.
	// This is the canonical ESF-based mapping parsed from CSpriteTSlotList (0x2930).
	tslotMap := make(map[int]int) // matIndex → slotID
	if cs, ok := obj.(*esf.CSprite); ok && len(cs.TSlotList) > 0 {
		for _, entry := range cs.TSlotList {
			tslotMap[int(entry.MeshIndex)] = int(entry.SlotID)
		}
	}

	// Collect triangles grouped by material.
	groups := make(map[matKey][]triVert)
	matSurfaces := make(map[matKey]*esf.Surface) // material → layer 0 texture
	layer1DictIDs := make(map[matKey]int32)      // material → layer 1 DictID (clothing overlay)
	layer0Colors := make(map[matKey][4]byte)     // material → layer 0 colour (per-race skin tone)

	var totalTris int
	bbox := esf.NewBox()

	for _, sp := range placements {
		sprite := sp.Sprite
		if sprite == nil {
			continue
		}

		pb, err := sprite.GetPrimBuffer(m.file)
		if err != nil || pb == nil {
			continue
		}

		mp := pb.MatPal
		if mp == nil {
			mp, _ = sprite.GetMatPal(m.file)
		}

		var matPalPtr uintptr
		if mp != nil {
			matPalPtr = uintptr(unsafe.Pointer(mp))
		}

		// Include variant info in the key so base and variant meshes
		// sharing the same material get separate GPU mesh groups.
		var varSlot esf.ArmorSlot = esf.ArmorSlotBase
		var varSet byte
		if sp.Variant != nil {
			varSlot = sp.Variant.Slot
			varSet = sp.Variant.Set
		}

		for _, vl := range pb.VertexLists {
			if len(vl.Vertices) < 3 {
				continue
			}

			key := matKey{matPalPtr: matPalPtr, matIndex: vl.Material, varSlot: varSlot, varSet: varSet}

			// Resolve texture on first encounter.
			if _, seen := matSurfaces[key]; !seen {
				matSurfaces[key] = resolveSurface(mp, vl.Material)
				layer1DictIDs[key] = resolveLayer1DictID(mp, vl.Material)
				layer0Colors[key] = resolveLayer0Color(mp, vl.Material)
			}

			// Expand triangle strip to individual triangles.
			tris := destripVertices(vl.Vertices, sp)
			for i := range tris {
				bbox.Add(tris[i].X, tris[i].Y, tris[i].Z)
			}
			groups[key] = append(groups[key], tris...)
			totalTris += len(tris) / 3
		}
	}

	if totalTris == 0 {
		log.Printf("models: model %d produced 0 triangles", modelID)
		return nil
	}

	// PS2 CenterOff = (0, 1.2 * DefaultScale, 0) — hardcoded height from
	// VIWalkControl::InitParms. Only matters for pitch rotation (swimming);
	// for upright standing T(-c) and T(+c) cancel in centerTransform.
	// Using bbox center (old approach) caused race-dependent Z shifts when rotated.
	cx := float32(0)
	cz := float32(0)
	yBottom := bbox.MinY // still needed for bone height validation

	model := &CharModel{
		CenterOff:    [3]float32{0, 0, 0}, // set properly below after DefaultScale is known
		HeadIdx:      -1,
		RightHandIdx: -1,
		LeftHandIdx:  -1,
	}

	// Store DefaultScale, Race, Sex from CSprite (PS2 VIHSprite+0xD0, VICSprite GetRace/GetSex).
	// Applied as direct uniform scale in VIHSprite::Raster (0x00448BBC).
	model.DefaultScale = 1.0
	model.Race = 9 // PS2 default when header ver < 3
	if cs, ok := obj.(*esf.CSprite); ok {
		if cs.DefaultScale > 0 {
			model.DefaultScale = cs.DefaultScale
		}
		model.Race = cs.Race
		model.Sex = cs.Sex
		model.SkelType = cs.SkelType
		model.IsCSprite = true
	}
	// PS2 CenterOff = (0, 1.2 * DefaultScale, 0) from VIWalkControl::InitParms.
	// Height 1.2 is hardcoded; fixScale = DefaultScale.
	model.CenterOff = [3]float32{0, 1.2 * model.DefaultScale, 0}

	// Build skeleton and store animations if model has a bone hierarchy.
	var hasSkinning bool
	if cs, ok := obj.(*esf.CSprite); ok {
		hier := cs.Hierarchy
		if hier != nil && len(hier.Nodes) > 0 {
			model.Skeleton = NewSkeleton(hier, cs.BoneRefMap)
			model.Animations = cs.Animations
			hasSkinning = true
			log.Printf("  skeleton: %d bones, %d animations, refMap=%v",
				len(hier.Nodes), len(cs.Animations), cs.BoneRefMap != nil)
		}
		// Store bone-attached particle emitter definitions (0x2950/0x2960).
		if len(cs.PartEmitters) > 0 {
			model.PartEmitters = cs.PartEmitters
			model.PartDefs = cs.PartDefs
			log.Printf("  partEmitters: %d emitters, %d partDefs", len(cs.PartEmitters), len(cs.PartDefs))
		}
		// Store sound clip DictIDs and register with audio system.
		if len(cs.SoundClips) > 0 {
			model.SoundClipIDs = make([]int32, len(cs.SoundClips))
			for i, clip := range cs.SoundClips {
				model.SoundClipIDs[i] = clip.DictID
			}
			model.ContSoundID = cs.ContSoundID
			model.ContSoundVol = cs.ContSoundVol
			if m.soundLoader != nil {
				n := m.soundLoader.LoadModelSounds(cs.SoundClips)
				if n > 0 {
					log.Printf("  sounds: %d clips loaded", n)
				}
			}
		}
		// Build CPlayList slot → raw animation index mapping.
		// Each PlayList entry has a slot number (Index) and animation DictIDs.
		// We match the DictIDs to find their positions in the Animations array.
		if len(cs.PlayList) > 0 && len(cs.Animations) > 0 {
			dictToIdx := make(map[int32]int, len(cs.Animations))
			for i, a := range cs.Animations {
				if a != nil {
					dictToIdx[a.DictID] = i
				}
			}
			model.PLSlotToRaw = make(map[int]int, len(cs.PlayList))
			model.PLSlotToRawPair = make(map[int][2]int, len(cs.PlayList))
			model.PLSlotSpeed = make(map[int]float32, len(cs.PlayList))
			model.PLSlotPlayOnce = make(map[int]bool, len(cs.PlayList))
			for _, pl := range cs.PlayList {
				slot := int(pl.Index)
				upper, lower := -1, -1
				if idx, ok := dictToIdx[pl.AnimDictID[0]]; ok {
					model.PLSlotToRaw[slot] = idx
					upper = idx
				}
				if pl.AnimDictID[1] != 0 {
					if idx, ok := dictToIdx[pl.AnimDictID[1]]; ok {
						lower = idx
					}
				}
				model.PLSlotToRawPair[slot] = [2]int{upper, lower}
				model.PLSlotSpeed[slot] = pl.Speed
				model.PLSlotPlayOnce[slot] = pl.PlayOnce != 0
				// Store per-slot sound config (PS2: VICSprite+0x1C0).
				if pl.SoundDictID[0] != 0 || pl.SoundDictID[1] != 0 {
					if model.PLSlotSound == nil {
						model.PLSlotSound = make(map[int]PLSlotSoundEntry, len(cs.PlayList))
					}
					model.PLSlotSound[slot] = PLSlotSoundEntry{
						SoundDictID: pl.SoundDictID,
						Volume:      pl.SoundVolume,
						PitchRange:  pl.PitchRange,
					}
				}
			}
			// VICSprite::SetDefaultEntries: slots the ESF left empty are
			// filled from their neighbours (missing attacks from 22/23 ...).
			ApplyDefaultEntries(model)

			// Pin the idle ground baseline now, from slot 0's first frame,
			// rather than letting it latch onto whatever the first entity
			// using this model happened to be playing. Slot 0 is idle across
			// every humanoid (claude-notes/anim-slot-semantics.md); a model
			// without it keeps the old first-frame fallback.
			if model.Skeleton != nil {
				if pair, ok := model.PLSlotToRawPair[0]; ok {
					var idleUpper, idleLower *esf.HSpriteAnim
					if pair[0] >= 0 && pair[0] < len(cs.Animations) {
						idleUpper = cs.Animations[pair[0]]
					}
					if pair[1] >= 0 && pair[1] < len(cs.Animations) {
						idleLower = cs.Animations[pair[1]]
					}
					model.Skeleton.CaptureIdleBounce(idleUpper, idleLower)
				}
			}

			// Build per-animation keyframe trigger map (sound + spell FX triggers).
			// Store raw frame positions — PS2 ProcessPlayTiming compares frame positions directly.
			for rawIdx, anim := range cs.Animations {
				if anim == nil || len(anim.Keyframes) == 0 {
					continue
				}
				var kfs []AnimSoundKeyframe
				for _, kf := range anim.Keyframes {
					if IsSoundTrigger(kf.RefID) || IsSpellFXTrigger(kf.RefID) {
						kfs = append(kfs, AnimSoundKeyframe{
							Time:  kf.Time, // raw frame position (PS2 ProcessPlayTiming)
							RefID: kf.RefID,
						})
					}
				}
				if len(kfs) > 0 {
					if model.AnimKeyframes == nil {
						model.AnimKeyframes = make(map[int][]AnimSoundKeyframe, len(cs.Animations))
					}
					model.AnimKeyframes[rawIdx] = kfs
				}
			}
			if !model.IsHumanoid {
				log.Printf("  NH model 0x%08X: %d anims, PLSlotToRaw=%v", modelID, len(cs.Animations), model.PLSlotToRaw)
			} else {
				slots := make([]int, 0, len(model.PLSlotToRawPair))
				for s := range model.PLSlotToRawPair {
					slots = append(slots, s)
				}
				log.Printf("  Humanoid 0x%08X: %d anims, %d PL slots: %v", modelID, len(cs.Animations), len(slots), slots)
			}
		}
	}

	var totalTextures int

	for key, verts := range groups {
		n := len(verts)
		if n < 3 {
			continue
		}

		positions := make([]float32, n*3)
		uvs := make([]float32, n*2)
		normals := make([]float32, n*3)
		colors := make([]uint8, n*4)

		for i, v := range verts {
			p3 := i * 3
			p2 := i * 2
			p4 := i * 4

			positions[p3+0] = v.X
			positions[p3+1] = v.Y
			positions[p3+2] = v.Z

			uvs[p2+0] = v.U
			uvs[p2+1] = v.V

			normals[p3+0] = v.NX
			normals[p3+1] = v.NY
			normals[p3+2] = v.NZ

			colors[p4+0] = mathutil.FloatToUint8(v.R)
			colors[p4+1] = mathutil.FloatToUint8(v.G)
			colors[p4+2] = mathutil.FloatToUint8(v.B)
			colors[p4+3] = mathutil.FloatToUint8(v.A)
		}

		mesh := rl.Mesh{
			VertexCount:   int32(n),
			TriangleCount: int32(n / 3),
			Vertices:      (*float32)(unsafe.Pointer(&positions[0])),
			Texcoords:     (*float32)(unsafe.Pointer(&uvs[0])),
			Normals:       (*float32)(unsafe.Pointer(&normals[0])),
			Colors:        (*uint8)(unsafe.Pointer(&colors[0])),
		}

		// Check if any vertices have bone weights (for GPU skinning).
		hasBones := false
		if hasSkinning {
			for _, v := range verts {
				if v.BoneWeight[0] > 0 || v.BoneWeight[1] > 0 {
					hasBones = true
					break
				}
			}
		}

		// Populate bone vertex attributes for GPU skinning.
		var boneIds []byte
		var boneWeights []float32
		if hasBones && model.Skeleton != nil {
			boneIds = make([]byte, n*4)
			boneWeights = make([]float32, n*4)
			for i, v := range verts {
				boneIds[i*4+0] = v.BoneIdx[0]
				boneIds[i*4+1] = v.BoneIdx[1]
				boneIds[i*4+2] = v.BoneIdx[2]
				boneIds[i*4+3] = v.BoneIdx[3]
				boneWeights[i*4+0] = v.BoneWeight[0]
				boneWeights[i*4+1] = v.BoneWeight[1]
				boneWeights[i*4+2] = v.BoneWeight[2]
				boneWeights[i*4+3] = v.BoneWeight[3]
			}
			// BoneIds pointer type is *int32 in Go bindings, but C expects unsigned char*.
			mesh.BoneIds = (*int32)(unsafe.Pointer(&boneIds[0]))
			mesh.BoneWeights = (*float32)(unsafe.Pointer(&boneWeights[0]))
			mesh.BoneCount = int32(len(model.Skeleton.Nodes))
		}

		// GPU-skinned meshes upload as static — no per-frame buffer updates needed.
		rl.UploadMesh(&mesh, false)

		runtime.KeepAlive(positions)
		runtime.KeepAlive(uvs)
		runtime.KeepAlive(normals)
		runtime.KeepAlive(colors)
		runtime.KeepAlive(boneIds)
		runtime.KeepAlive(boneWeights)

		// Nil out CPU pointers so UnloadMesh won't free Go memory.
		mesh.Vertices = nil
		mesh.Texcoords = nil
		mesh.Normals = nil
		mesh.Colors = nil
		mesh.BoneIds = nil
		mesh.BoneWeights = nil

		// Auto-detect TextureSlot for equipment texture swapping.
		// Uses ESF TSlotList (canonical mapping from CSpriteTSlotList 0x2930).
		// Slots 0-4: armor, 7: helm, 8: robe (from TSlotList).
		// Slots 5 (hair) and 6 (face) are identified by DictID matching below.
		textureSlot := -1 // unknown
		rawSlotID := -1   // raw TSlotList slot ID (preserved for hair layer logic)
		if slotID, ok := tslotMap[key.matIndex]; ok && slotID >= 0 {
			textureSlot = slotID
			rawSlotID = slotID
		}

		mat := rl.LoadMaterialDefault()
		if hasBones {
			mat.Shader = m.skinShader // GPU-skinned alpha cutoff
		} else {
			mat.Shader = m.shader // static alpha cutoff
		}
		surf := matSurfaces[key]
		// Mark body-slot textures for dilation before GPU upload.
		// Body textures have alpha=0 UV atlas padding that causes black fringe
		// when bilinear filtering samples near seam edges.
		if textureSlot >= TextSlotChest && textureSlot <= TextSlotBoots && surf != nil {
			m.bodySlots[surf.ObjInfo().DictID] = true
		}
		tex := m.getTexture(surf)
		rl.SetMaterialTexture(&mat, rl.MapDiffuse, tex)
		// Layer-0 colour multiplies the texture (PS2 SetLayerColor) and carries
		// the per-race skin tone. Setting it here means the base pass gets it and
		// the armour/robe passes, which already save and restore Color, still work.
		if c, ok := layer0Colors[key]; ok && c != [4]byte{255, 255, 255, 255} {
			maps := (*[32]rl.MaterialMap)(unsafe.Pointer(mat.Maps))
			maps[rl.MapDiffuse].Color = rl.NewColor(c[0], c[1], c[2], 255)
		}
		if surf != nil {
			totalTextures++
		}

		// Attach variant tag if this mesh group came from a variant placement.
		var varTag *esf.VariantTag
		if key.varSlot != esf.ArmorSlotBase {
			varTag = &esf.VariantTag{Slot: key.varSlot, Set: key.varSet}
		}

		var surfDictID int32
		if surf != nil {
			surfDictID = surf.ObjInfo().DictID
		}

		mg := meshGroup{
			Mesh:         mesh,
			Material:     mat,
			Variant:      varTag,
			TextureSlot:  textureSlot,
			RawSlotID:    rawSlotID,
			SurfDictID:   surfDictID,
			Layer1DictID: layer1DictIDs[key],
			GPUSkinned:   hasBones,
		}
		model.Groups = append(model.Groups, mg)
	}

	// Identify hair mesh groups by matching their default Surface DictID
	// against known hair texture tables. PS2 uses slot IDs 8-18 for hair
	// layers (8-10 = facial, 11-13 = head, 14 = connecting, 15 = base,
	// 16-18 = extended facial). We remap TextureSlot to 5 for rendering
	// but preserve the raw slot ID in RawSlotID for visibility logic.
	// Face textures share TSlotList slot 7 (helm) — the same head mesh shows
	// face when no helm is equipped, helm texture when one is.
	for i := range model.Groups {
		mg := &model.Groups[i]
		if mg.SurfDictID == 0 || mg.TextureSlot <= TextSlotBoots {
			continue // skip unknown or already-identified armor slots
		}
		if mg.TextureSlot == 7 {
			continue // slot 7 = face/helm — already assigned by TSlotList
		}
		if isHairDictID(mg.SurfDictID) || mg.RawSlotID >= 8 {
			// Remap to TextureSlot=5 for hair rendering; RawSlotID preserved
			// for PS2-accurate layer visibility in hairSlotVisible(). Slot 8
			// is the first facial-hair layer on every model (SetHair covers
			// 8-18; SetRobe only ever retextures 0-4). Read as a robe mesh, it
			// hid the dwarf male's beard.
			mg.TextureSlot = 5
		}
	}

	dims := bbox.Dimensions()
	model.Height = dims.Y

	// Log DefaultScale for debugging.
	if cs, ok := obj.(*esf.CSprite); ok {
		log.Printf("  CSprite 0x%08X: DefaultScale=%.4f SkelType=%d Race=%d Sex=%d ver=%d",
			uint32(modelID), cs.DefaultScale, cs.SkelType, cs.Race, cs.Sex, cs.ObjInfo().Version)
	}

	// Extract bone positions from the skeleton hierarchy.
	// The hierarchy stores absolute bind-pose positions in model space.
	// We apply the same XZ centering and Y-bottom=0 adjustment as the mesh.
	// Humanoid detection: PS2 uses SkelType from ESF CSprite header (VICSprite+0x194).
	// SkelType 1 = dual-pair humanoid (upper/lower body split animation).
	// SkelType 0/2/3 = single-body animation (creatures, quadrupeds, flying).
	hasTSlotList := len(tslotMap) > 0
	isHumanoid := model.SkelType == 1
	model.IsHumanoid = isHumanoid
	model.HasTSlotList = hasTSlotList
	// Assign AnimProfile based on SkelType and anim count:
	//   SkelType 1 + >=60 anims  → humanoidProfile (player races — 72-80 anims)
	//   SkelType 1 + <60 anims   → humanoidArmedProfile (gnolls, etc. — 24-40 anims with equipment)
	//   SkelType 0/2/3           → nonHumanoidProfile (creatures — 4-18 anims, single body)
	nAnims := len(model.Animations)
	switch {
	case isHumanoid && nAnims >= 60:
		model.AnimProfile = humanoidProfile
	case isHumanoid:
		model.AnimProfile = humanoidArmedProfile
	default:
		model.AnimProfile = nonHumanoidProfile
	}
	// Per-model override as final fallback for edge cases.
	if override, ok := profileOverrides[modelID]; ok {
		model.AnimProfile = override
	}
	// Death is slot 14 for every model (SetDeath → SetLocomotion(14)); a
	// model whose 14 is idle plays idle once, as on the PS2. The swap to 15
	// that stood here played the special idle instead.
	if hier := getHierarchy(obj); hier != nil && len(hier.Nodes) > 0 {
		findBones(model, obj, hier, cx, yBottom, cz, dims.Y, isHumanoid)
		// Log root bone scale — PS2 uses this as the model's default scale.
		if hier.Nodes[0].Scale != 1.0 {
			log.Printf("  rootBoneScale=%.4f", hier.Nodes[0].Scale)
		}
		// Log FixScale for any non-identity bones (PS2 per-axis bone scaling).
		for bi, bn := range hier.Nodes {
			fs := bn.FixScale
			if fs[0] != 1.0 || fs[1] != 1.0 || fs[2] != 1.0 {
				log.Printf("  bone[%d] FixScale=(%.4f, %.4f, %.4f)", bi, fs[0], fs[1], fs[2])
			}
		}
	}

	dynGroups := 0
	for _, g := range model.Groups {
		if g.GPUSkinned {
			dynGroups++
		}
	}

	// Allocate shared bone matrix buffer for GPU skinning.
	// All skinned mesh groups in this model share the same buffer.
	if model.Skeleton != nil && dynGroups > 0 {
		boneCount := len(model.Skeleton.Nodes)
		model.boneMatBuf = make([]rl.Matrix, boneCount)
		// Point all skinned mesh groups' BoneMatrices at the shared buffer.
		for i := range model.Groups {
			if model.Groups[i].GPUSkinned {
				model.Groups[i].Mesh.BoneMatrices = &model.boneMatBuf[0]
				model.Groups[i].Mesh.BoneCount = int32(boneCount)
			}
		}
	}
	// Debug: dump TSlotList and mesh group texture slot assignments for humanoid models.
	if isHumanoid {
		log.Printf("  TSlotList (%d entries):", len(tslotMap))
		for matIdx, slotID := range tslotMap {
			log.Printf("    matIdx=%d → slotID=%d", matIdx, slotID)
		}
		for i, mg := range model.Groups {
			varStr := "base"
			if mg.Variant != nil {
				varStr = fmt.Sprintf("var(%d,%d)", mg.Variant.Slot, mg.Variant.Set)
			}
			log.Printf("    group[%d]: slot=%d raw=%d surfDictID=0x%08X %s isHair=%v",
				i, mg.TextureSlot, mg.RawSlotID, uint32(mg.SurfDictID), varStr, isHairDictID(mg.SurfDictID))
		}
	}
	log.Printf("models: loaded model %d: %d triangles, %d mesh groups (%d dynamic), %d textures, %d anims, humanoid=%v, size=(%.1f, %.1f, %.1f)",
		modelID, totalTris, len(model.Groups), dynGroups, totalTextures, len(model.Animations), isHumanoid, dims.X, dims.Y, dims.Z)
	return model
}

// getHierarchy extracts the bone hierarchy from a sprite object.
func getHierarchy(obj esf.Object) *esf.HSpriteHierarchy {
	switch s := obj.(type) {
	case *esf.CSprite:
		return s.Hierarchy
	case *esf.HSprite:
		return s.Hierarchy
	}
	return nil
}

// walkParents walks up n parent levels in the bone hierarchy, returning the ancestor
// bone index. Returns the original index if fewer than n parents exist.
func walkParents(nodes []esf.BoneNode, idx, levels int) int {
	for i := 0; i < levels; i++ {
		p := int(nodes[idx].ParentID)
		if p < 0 || p >= len(nodes) {
			break
		}
		idx = p
	}
	return idx
}

// findBones identifies key bone positions (head, hands) from the skeleton hierarchy.
// For CSprite models, uses PS2 canonical data: NodeIDList slot 8 for head bone
// (PS2 VICSprite::SetHelm reads NodeIDList[8] at +0x0F34), ASlotList for hand bones.
// Falls back to heuristic for HSprite or when canonical data is absent.
// Positions are adjusted to match mesh centering (XZ at origin, Y bottom=0).
func findBones(model *CharModel, obj esf.Object, hier *esf.HSpriteHierarchy, cx, yBottom, cz, meshHeight float32, isHumanoid bool) {
	// The NodeIDList, as VICSprite::m_node: named node -> bone.
	if cs, ok := obj.(*esf.CSprite); ok && len(cs.NodeIDList) > 0 {
		for n := range model.NodeBones {
			model.NodeBones[n] = -1
			if b := int(cs.NodeBone(int32(n))); b >= 0 && b < len(hier.Nodes) {
				model.NodeBones[n] = b
			}
		}
		model.HasNodeBones = true
	}

	// Find head bone — PS2 uses NodeIDList[8] (helm attachment bone).
	// Try canonical lookup first, fall back to heuristic (highest-Y near centerline).
	var headIdx int = -1
	if cs, ok := obj.(*esf.CSprite); ok {
		boneIdx := int(cs.NodeBone(8)) // PS2: NodeIDList[8] = helm bone
		if boneIdx >= 0 && boneIdx < len(hier.Nodes) {
			headIdx = boneIdx
		}
	}
	// Heuristic fallback: highest-Y bone near centerline.
	if headIdx < 0 {
		for i, n := range hier.Nodes {
			nearCenter := n.Pos.X > -0.1 && n.Pos.X < 0.1
			if nearCenter && (headIdx == -1 || n.Pos.Y > hier.Nodes[headIdx].Pos.Y) {
				headIdx = i
			}
		}
	}
	if headIdx >= 0 {
		hn := hier.Nodes[headIdx]
		model.HeadBone = [3]float32{hn.Pos.X, hn.Pos.Y, hn.Pos.Z}
		model.HeadIdx = headIdx
		model.HasHead = true
		model.HeadFixScale = hn.FixScale
		if hn.Scale != 1.0 {
			log.Printf("  headBone[%d] Scale=%.4f (non-unit)", headIdx, hn.Scale)
		}
	}

	// Hand bones: only for humanoid models with equipment slots.
	if !isHumanoid {
		return
	}

	// Try PS2 canonical ASlotList first (from CSpriteASlotList 0x2920).
	// Slot 0 = right hand (weapon), slot 1 = left hand (shield).
	if cs, ok := obj.(*esf.CSprite); ok && len(cs.ASlotList) >= 2 {
		rIdx := int(cs.AttachBone(0)) // right hand
		lIdx := int(cs.AttachBone(1)) // left hand
		if rIdx >= 0 && rIdx < len(hier.Nodes) && lIdx >= 0 && lIdx < len(hier.Nodes) {
			rn := hier.Nodes[rIdx]
			ln := hier.Nodes[lIdx]
			model.RightHand = [3]float32{rn.Pos.X, rn.Pos.Y, rn.Pos.Z}
			model.LeftHand = [3]float32{ln.Pos.X, ln.Pos.Y, ln.Pos.Z}
			model.RightHandIdx = rIdx
			model.LeftHandIdx = lIdx
			model.HasBones = true
			return
		}
	}

	// Fallback: heuristic for HSprite or models without ASlotList.
	var maxX, minX float32
	var rightIdx, leftIdx int
	rightIdx, leftIdx = -1, -1

	for i, n := range hier.Nodes {
		if n.Pos.X > maxX || rightIdx == -1 {
			maxX = n.Pos.X
			rightIdx = i
		}
		if n.Pos.X < minX || leftIdx == -1 {
			minX = n.Pos.X
			leftIdx = i
		}
	}

	// Validate: hands should have meaningful spread (arms, not just a spine).
	if rightIdx < 0 || leftIdx < 0 || maxX-minX < 0.3 {
		return
	}

	// Walk up 1 parent level from fingertip bones to reach the palm.
	rightIdx = walkParents(hier.Nodes, rightIdx, 1)
	leftIdx = walkParents(hier.Nodes, leftIdx, 1)

	// Validate at arm height (40-95% of mesh height) using the palm bone positions.
	rn := hier.Nodes[rightIdx]
	ln := hier.Nodes[leftIdx]
	rhY := rn.Pos.Y - yBottom
	lhY := ln.Pos.Y - yBottom
	if meshHeight > 0 {
		rhPct := rhY / meshHeight
		lhPct := lhY / meshHeight
		if rhPct < 0.4 || rhPct > 0.95 || lhPct < 0.4 || lhPct > 0.95 {
			return
		}
	}

	// EQOA models face +Z; in C-convention, +X is the character's left side.
	// So max-X bone = left hand, min-X bone = right hand.
	model.RightHand = [3]float32{ln.Pos.X, ln.Pos.Y, ln.Pos.Z}
	model.LeftHand = [3]float32{rn.Pos.X, rn.Pos.Y, rn.Pos.Z}
	model.RightHandIdx = leftIdx
	model.LeftHandIdx = rightIdx
	model.HasBones = true
}

// loadItemModel loads a weapon/item model from the ITEM.ESF file by dictionary ID.
// Returns nil if the model cannot be loaded.
// Unlike character models (Y bottom at 0), items are centered at geometric center
// for attachment positioning.
func (m *ModelManager) loadItemModel(modelID int32) *CharModel {
	obj, err := m.itemFile.FindObject(modelID)
	if err != nil {
		log.Printf("models: item FindObject(%d): %v", modelID, err)
		return nil
	}
	if obj == nil {
		log.Printf("models: item %d not found in dictionary", modelID)
		return nil
	}

	placements := m.resolveItemPlacements(obj)
	if len(placements) == 0 {
		log.Printf("models: item %d has no placements", modelID)
		return nil
	}

	groups := make(map[matKey][]triVert)
	matSurfaces := make(map[matKey]*esf.Surface)
	layer0Colors := make(map[matKey][4]byte) // material → layer 0 colour (per-race skin tone)

	var totalTris int
	bbox := esf.NewBox()

	var weaponParticles []WeaponParticleEntry

	for _, sp := range placements {
		sprite := sp.Sprite
		// GroupSprite placements store SpriteID references, not resolved sprites.
		// Resolve them from ITEM.ESF.
		if sprite == nil && sp.SpriteID != 0 {
			childObj, err := m.itemFile.FindObject(sp.SpriteID)
			if err != nil || childObj == nil {
				continue
			}
			// Check if this member is a ParticleSprite (weapon aura effect).
			if ps, ok := childObj.(*esf.ParticleSprite); ok {
				if ps.ParticleDef != nil {
					weaponParticles = append(weaponParticles, WeaponParticleEntry{
						PS:        ps,
						Placement: sp,
					})
				}
				continue
			}
			// Unwrap wrapper types to get to the SimpleSprite with PrimBuffer.
			childPlacements := m.resolveItemPlacements(childObj)
			for _, cp := range childPlacements {
				cs := cp.Sprite
				if cs == nil {
					continue
				}
				pb, err := cs.GetPrimBuffer(m.itemFile)
				if err != nil || pb == nil {
					continue
				}
				mp := pb.MatPal
				if mp == nil {
					mp, _ = cs.GetMatPal(m.itemFile)
				}
				var matPalPtr uintptr
				if mp != nil {
					matPalPtr = uintptr(unsafe.Pointer(mp))
				}
				for _, vl := range pb.VertexLists {
					if len(vl.Vertices) < 3 {
						continue
					}
					key := matKey{matPalPtr: matPalPtr, matIndex: vl.Material}
					if _, seen := matSurfaces[key]; !seen {
						matSurfaces[key] = resolveSurface(mp, vl.Material)
						layer0Colors[key] = resolveLayer0Color(mp, vl.Material)
					}
					// Apply both the child and parent placement transforms.
					tris := destripVertices(vl.Vertices, cp)
					for i := range tris {
						if sp != nil {
							p := esf.Point{X: tris[i].X, Y: tris[i].Y, Z: tris[i].Z}
							sp.Transform(&p)
							tris[i].X, tris[i].Y, tris[i].Z = p.X, p.Y, p.Z
						}
						bbox.Add(tris[i].X, tris[i].Y, tris[i].Z)
					}
					groups[key] = append(groups[key], tris...)
					totalTris += len(tris) / 3
				}
			}
			continue
		}
		if sprite == nil {
			continue
		}

		pb, err := sprite.GetPrimBuffer(m.itemFile)
		if err != nil || pb == nil {
			continue
		}

		mp := pb.MatPal
		if mp == nil {
			mp, _ = sprite.GetMatPal(m.itemFile)
		}

		var matPalPtr uintptr
		if mp != nil {
			matPalPtr = uintptr(unsafe.Pointer(mp))
		}

		for _, vl := range pb.VertexLists {
			if len(vl.Vertices) < 3 {
				continue
			}

			key := matKey{matPalPtr: matPalPtr, matIndex: vl.Material}

			if _, seen := matSurfaces[key]; !seen {
				matSurfaces[key] = resolveSurface(mp, vl.Material)
				layer0Colors[key] = resolveLayer0Color(mp, vl.Material)
			}

			tris := destripVertices(vl.Vertices, sp)
			for i := range tris {
				bbox.Add(tris[i].X, tris[i].Y, tris[i].Z)
			}
			groups[key] = append(groups[key], tris...)
			totalTris += len(tris) / 3
		}
	}

	if totalTris == 0 {
		log.Printf("models: item %d produced 0 triangles", modelID)
		return nil
	}

	// Keep native ESF coordinates (bone-local space).
	// PS2 weapon models are authored with grip at origin, blade extending along
	// the bone's local axis. VIHSprite::Attach applies the bone's world transform
	// directly. Centering would shift the origin away from the grip, breaking
	// bone rotation alignment.
	cx := float32(0)
	cy := float32(0)
	cz := float32(0)

	model := &CharModel{}
	var totalTextures int

	for key, verts := range groups {
		n := len(verts)
		if n < 3 {
			continue
		}

		positions := make([]float32, n*3)
		uvs := make([]float32, n*2)
		normals := make([]float32, n*3)
		colors := make([]uint8, n*4)

		for i, v := range verts {
			p3 := i * 3
			p2 := i * 2
			p4 := i * 4

			positions[p3+0] = v.X - cx
			positions[p3+1] = v.Y - cy
			positions[p3+2] = v.Z - cz

			uvs[p2+0] = v.U
			uvs[p2+1] = v.V

			normals[p3+0] = v.NX
			normals[p3+1] = v.NY
			normals[p3+2] = v.NZ

			colors[p4+0] = mathutil.FloatToUint8(v.R)
			colors[p4+1] = mathutil.FloatToUint8(v.G)
			colors[p4+2] = mathutil.FloatToUint8(v.B)
			colors[p4+3] = mathutil.FloatToUint8(v.A)
		}

		mesh := rl.Mesh{
			VertexCount:   int32(n),
			TriangleCount: int32(n / 3),
			Vertices:      (*float32)(unsafe.Pointer(&positions[0])),
			Texcoords:     (*float32)(unsafe.Pointer(&uvs[0])),
			Normals:       (*float32)(unsafe.Pointer(&normals[0])),
			Colors:        (*uint8)(unsafe.Pointer(&colors[0])),
		}

		rl.UploadMesh(&mesh, false)

		runtime.KeepAlive(positions)
		runtime.KeepAlive(uvs)
		runtime.KeepAlive(normals)
		runtime.KeepAlive(colors)

		mesh.Vertices = nil
		mesh.Texcoords = nil
		mesh.Normals = nil
		mesh.Colors = nil

		mat := rl.LoadMaterialDefault()
		mat.Shader = m.shader // alpha cutoff: discard transparent pixels
		surf := matSurfaces[key]
		tex := m.getTexture(surf)
		rl.SetMaterialTexture(&mat, rl.MapDiffuse, tex)
		// Layer-0 colour multiplies the texture (PS2 SetLayerColor) and carries
		// the per-race skin tone. Setting it here means the base pass gets it and
		// the armour/robe passes, which already save and restore Color, still work.
		if c, ok := layer0Colors[key]; ok && c != [4]byte{255, 255, 255, 255} {
			maps := (*[32]rl.MaterialMap)(unsafe.Pointer(mat.Maps))
			maps[rl.MapDiffuse].Color = rl.NewColor(c[0], c[1], c[2], 255)
		}
		if surf != nil {
			totalTextures++
		}

		model.Groups = append(model.Groups, meshGroup{
			Mesh:     mesh,
			Material: mat,
		})
	}

	// Store weapon particle effects (ParticleSprites from GroupSprite members).
	if len(weaponParticles) > 0 {
		model.WeaponParticles = weaponParticles
	}

	dims := bbox.Dimensions()
	partStr := ""
	if len(weaponParticles) > 0 {
		partStr = fmt.Sprintf(", %d particle effects", len(weaponParticles))
	}
	log.Printf("models: loaded item %d: %d triangles, %d mesh groups, %d textures%s, size=(%.1f, %.1f, %.1f)",
		modelID, totalTris, len(model.Groups), totalTextures, partStr, dims.X, dims.Y, dims.Z)

	return model
}

// resolveItemPlacements extracts SpritePlacements from ITEM.ESF objects.
// Uses the same logic as resolvePlacements but reads from itemFile.
func (m *ModelManager) resolveItemPlacements(obj esf.Object) []*esf.SpritePlacement {
	switch s := obj.(type) {
	case *esf.CSprite:
		return s.Placements
	case *esf.HSprite:
		placements, _ := s.LoadSprites(m.itemFile)
		return placements
	case *esf.GroupSprite:
		return s.Placements
	case *esf.LODSprite:
		inner, _, err := s.GetSprite(m.itemFile)
		if err != nil || inner == nil {
			return nil
		}
		return []*esf.SpritePlacement{{Sprite: inner}}
	case *esf.SimpleSprite:
		return []*esf.SpritePlacement{{Sprite: s}}
	}
	return nil
}

// resolvePlacements extracts SpritePlacements from various ESF sprite types.
// For CSprites with multiple base placements (LOD levels), only the first
// is returned. The PS2 renders one LOD at a time via AddPlayToLOD; rendering
// all LODs simultaneously causes z-fighting from duplicate overlapping geometry.
func (m *ModelManager) resolvePlacements(obj esf.Object) []*esf.SpritePlacement {
	switch s := obj.(type) {
	case *esf.CSprite:
		return filterLODPlacements(s.Placements)
	case *esf.HSprite:
		placements, _ := s.LoadSprites(m.file)
		return placements
	case *esf.GroupSprite:
		return s.Placements
	case *esf.LODSprite:
		inner, _, err := s.GetSprite(m.file)
		if err != nil || inner == nil {
			return nil
		}
		return []*esf.SpritePlacement{{Sprite: inner}}
	case *esf.SimpleSprite:
		return []*esf.SpritePlacement{{Sprite: s}}
	}
	return nil
}

// filterLODPlacements deduplicates CSprite placements that share the same
// variant tag. The PS2 uses AddPlayToLOD to render only one LOD level at a
// time based on camera distance. Multiple base placements (Slot=-1, Set=0)
// represent different LOD levels of the same body; we keep only the first.
func filterLODPlacements(placements []*esf.SpritePlacement) []*esf.SpritePlacement {
	type varKey struct {
		slot esf.ArmorSlot
		set  byte
	}
	seen := make(map[varKey]bool)
	var result []*esf.SpritePlacement
	for _, sp := range placements {
		if sp.Variant == nil {
			result = append(result, sp)
			continue
		}
		key := varKey{sp.Variant.Slot, sp.Variant.Set}
		if !seen[key] {
			seen[key] = true
			result = append(result, sp)
		}
	}
	return result
}

// destripVertices converts a triangle strip to individual triangle vertices,
// applying the SpritePlacement transform to each vertex position.
func destripVertices(strip []esf.Vertex, sp *esf.SpritePlacement) []triVert {
	numTris := len(strip) - 2
	if numTris <= 0 {
		return nil
	}

	out := make([]triVert, 0, numTris*3)
	odd := true // matches OBJ exporter: starts true, first tri swaps winding

	for t := 0; t < numTris; t++ {
		var i0, i1, i2 int
		if odd {
			i0, i1, i2 = t, t+2, t+1
		} else {
			i0, i1, i2 = t, t+1, t+2
		}
		odd = !odd

		// Skip degenerate triangles (shared vertices).
		v0, v1, v2 := strip[i0], strip[i1], strip[i2]
		if v0.X == v1.X && v0.Y == v1.Y && v0.Z == v1.Z {
			continue
		}
		if v0.X == v2.X && v0.Y == v2.Y && v0.Z == v2.Z {
			continue
		}
		if v1.X == v2.X && v1.Y == v2.Y && v1.Z == v2.Z {
			continue
		}

		out = append(out,
			transformVert(v0, sp),
			transformVert(v1, sp),
			transformVert(v2, sp),
		)
	}
	return out
}

// transformVert applies the SpritePlacement transform and converts to triVert.
func transformVert(v esf.Vertex, sp *esf.SpritePlacement) triVert {
	p := esf.Point{X: v.X, Y: v.Y, Z: v.Z}
	if sp != nil {
		sp.Transform(&p)
	}

	r, g, b := v.R, v.G, v.B
	// Default vertex color to white if all zero (common for skinned meshes).
	if r == 0 && g == 0 && b == 0 {
		r, g, b = 1, 1, 1
	}

	tv := triVert{
		X: p.X, Y: p.Y, Z: p.Z,
		U: v.U, V: v.V,
		NX: v.NX, NY: v.NY, NZ: v.NZ,
		R: r, G: g, B: b, A: 1,
	}
	tv.BoneIdx = v.BoneIdx

	// Normalize bone weights from 0-127 to 0.0-1.0 (PS2 uses signed bytes, sum ≈ 127).
	wSum := int(v.BoneWeight[0]) + int(v.BoneWeight[1]) + int(v.BoneWeight[2]) + int(v.BoneWeight[3])
	if wSum > 0 {
		invSum := 1.0 / float32(wSum)
		tv.BoneWeight[0] = float32(v.BoneWeight[0]) * invSum
		tv.BoneWeight[1] = float32(v.BoneWeight[1]) * invSum
		tv.BoneWeight[2] = float32(v.BoneWeight[2]) * invSum
		tv.BoneWeight[3] = float32(v.BoneWeight[3]) * invSum
	}

	return tv
}

// resolveSurface gets the texture Surface for a material index within a palette.
func resolveSurface(mp *esf.MaterialPalette, matIdx int) *esf.Surface {
	if mp == nil || matIdx < 0 || matIdx >= len(mp.Materials) {
		return nil
	}
	mat := mp.Materials[matIdx]
	if len(mat.Layers) == 0 {
		return nil
	}
	return mat.Layers[0].Surface
}

// resolveLayer0Color returns layer 0's material colour, which the PS2
// multiplies into the texture (VIMaterial::SetLayerColor). This is where the
// per-race skin tone lives, not in the texture: the dark-skinned race's body
// shares the same pale skin surface (-1338533307) as several others and is
// tinted (109, 83, 77), while its head has its own dark texture and no tint.
// Ignoring the colour therefore renders that race with a dark face and a pale
// body. White is returned when a material carries no layers.
func resolveLayer0Color(mp *esf.MaterialPalette, matIdx int) [4]byte {
	white := [4]byte{255, 255, 255, 255}
	if mp == nil || matIdx < 0 || matIdx >= len(mp.Materials) {
		return white
	}
	mat := mp.Materials[matIdx]
	if len(mat.Layers) == 0 {
		return white
	}
	return mat.Layers[0].Color
}

// resolveLayer1DictID returns the DictID of layer 1's Surface for a material.
// Returns 0 if the material has fewer than 2 layers or layer 1 has no surface.
func resolveLayer1DictID(mp *esf.MaterialPalette, matIdx int) int32 {
	if mp == nil || matIdx < 0 || matIdx >= len(mp.Materials) {
		return 0
	}
	mat := mp.Materials[matIdx]
	if len(mat.Layers) < 2 || mat.Layers[1].Surface == nil {
		return 0
	}
	return mat.Layers[1].Surface.ObjInfo().DictID
}
