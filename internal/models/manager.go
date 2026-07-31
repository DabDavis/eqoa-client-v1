package models

import (
	"image"
	"log"
	"math"
	"math/rand"
	"path/filepath"
	"runtime"
	"time"
	"unsafe"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/eqoa/client/internal/gfx"
	"github.com/eqoa/client/internal/mathutil"
	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

// EquipmentState holds the armor/robe bytes, tint indices, and appearance
// data for an entity. Armor set values come from state channel offsets 93-98, 109.
// Tint indices come from offsets 99-104, 110. Appearance from 105-108, 144.
type EquipmentState struct {
	Chest, Bracer, Gloves, Legs, Boots, Helm, Robe byte
	ChestTint, BracerTint, GlovesTint, LegsTint, BootsTint, HelmTint, RobeTint byte
	HairColor, HairLength, HairStyle, FaceOption, Race, Sex byte
	HideHelm bool // client-side toggle: hide helm without unequipping
	HideRobe bool // client-side toggle: hide robe without unequipping

	// TexOverride is a client-side per-armor-slot Surface DictID, indexed by
	// TextSlotChest..TextSlotBoots. Zero means no override. A non-zero entry
	// wins over the robe and armor-set lookups and is read from CHAR.ESF, which
	// is where the NPC art in NPCArmorTextures lives. Cosmetic and local only —
	// nothing here is sent to the server.
	TexOverride [5]int32
}

// slotOverride returns the per-slot texture override, or 0 when the slot is not
// an armor slot or nothing is overridden.
func (e *EquipmentState) slotOverride(slot int) int32 {
	if e == nil || slot < TextSlotChest || slot > TextSlotBoots {
		return 0
	}
	return e.TexOverride[slot]
}

// texSource identifies which ESF file a cached texture was read from. A DictID
// alone is not a unique key: CHAR.ESF and CHARCUST.CSF both store surfaces under
// the plate armor DictIDs with different art, so caching by DictID alone lets
// whichever loads first shadow the other.
type texSource uint8

const (
	// texSrcDirect is a surface handed to getTexture already loaded, from
	// whichever file its model came from.
	texSrcDirect texSource = iota
	// texSrcAuto is the default equipment search order: CHARCUST, CHARFACE, CHAR.
	texSrcAuto
	// texSrcCharESF forces the lookup to read from CHAR.ESF only.
	texSrcCharESF
)

// texKey identifies a cached GPU texture.
type texKey struct {
	dictID int32
	src    texSource
}

// meshGroup holds a GPU mesh and its material for one texture group.
type meshGroup struct {
	Mesh        rl.Mesh
	Material    rl.Material
	Variant     *esf.VariantTag // nil = base mesh (always drawn)
	TextureSlot int             // body part: 0-4=armor, 5=hair, 6=face, 7=helm, 8=robe; -1=unknown
	RawSlotID   int             // raw TSlotList slot ID (before remapping); -1=unknown
	SurfDictID   int32           // original Surface DictID for this mesh group's texture
	Layer1DictID int32           // layer 1 Surface DictID (clothing overlay), 0 if single-layer
	GPUSkinned   bool            // true if mesh uses GPU skinning (bone attributes uploaded)
}

// shouldDraw returns true if this mesh group should be rendered given the
// entity's equipment state. Base meshes (Variant==nil or Slot==ArmorSlotBase)
// are always drawn, UNLESS their TextureSlot indicates a conditional part
// (robe, helm) that requires specific equipment to be visible.
// Variant meshes are drawn only when the entity's equipment byte for that
// slot matches the variant's Set value.
func (mg *meshGroup) shouldDraw(equip *EquipmentState) bool {
	// Variant-tagged meshes: draw only when equipment matches.
	if mg.Variant != nil && mg.Variant.Slot != esf.ArmorSlotBase {
		if equip == nil {
			return false
		}
		var val byte
		switch mg.Variant.Slot {
		case esf.ArmorSlotChest:
			val = equip.Chest
		case esf.ArmorSlotBracer:
			val = equip.Bracer
		case esf.ArmorSlotGloves:
			val = equip.Gloves
		case esf.ArmorSlotLegs:
			val = equip.Legs
		case esf.ArmorSlotBoots:
			val = equip.Boots
		case esf.ArmorSlotHelm:
			val = equip.Helm
		case esf.ArmorSlotRobe:
			val = equip.Robe
		default:
			return true
		}
		return val == mg.Variant.Set
	}

	// Base meshes (no variant tag): conditionally hide based on TextureSlot.
	// The PS2 VICSpriteCust hides robe/helm meshes when no robe/helm is equipped.
	if equip != nil {
		switch mg.TextureSlot {
		case 5: // hair — PS2 SetHair uses progressive layer visibility per raw slot ID.
			return hairSlotVisible(mg.RawSlotID, equip)
		case 8: // robe mesh — only show when a valid robe is equipped (0-3)
			return equip.Robe < 4
		}
	}

	return true
}

// hairSlotVisible implements the PS2 VICSprite::SetHair visibility logic.
// Hair meshes use a progressive layer system where HairStyle/HairLength
// control how many layers are visible (not individual style selectors).
//
// PS2 SetHair(int hairStyle, int hairColor, int hairLength) at SUPPORT 0x004226A8:
//   PS2 arg1 "hairStyle"  = Go HairColor  (byte 105) — texture selector (Hair[0..7])
//   PS2 arg2 "hairColor"  = Go HairLength (byte 106) — head hair layer count
//   PS2 arg3 "hairLength" = Go HairStyle  (byte 107) — facial/extended layer count
//
// Per-slot visibility (using Go field names):
//   Slots 8-10:  visible when (slot-8) < HairStyle;   no helm override
//   Slots 11-13: visible when (slot-11) < HairLength;  hidden by helm
//   Slot 14:     visible when HairLength >= 2 AND HairStyle >= 2; hidden by helm
//   Slot 15:     always visible (base scalp/hair mesh)
//   Slots 16-18: visible when (slot-16) < HairStyle;   hidden by helm
//
// Gnomes: HairLength controls hat layers (slots 11-13). HairLength=2 = no hat (head hair only), 3 = hat.
// Gnome facial hair uses slots 8, 10, 17 (non-contiguous — no slot 9),
// so we map style values directly: 1=beard(8), 2=+glasses(10), 3=+mustache(17).
func hairSlotVisible(rawSlot int, equip *EquipmentState) bool {
	hasHelm := equip.Helm != 0 && equip.Helm != 255 && !equip.HideHelm
	style := int(equip.HairStyle)
	length := int(equip.HairLength)

	// Gnome: facial hair slots are non-contiguous (8, 10, 17), so
	// standard (slot-N) < style arithmetic doesn't work. Map directly.
	if equip.Race == 3 {
		switch {
		case rawSlot == 8:
			return style >= 1 // beard
		case rawSlot == 17:
			return style >= 2 // glasses
		case rawSlot == 10:
			return style >= 3 // fancy mustache
		case rawSlot >= 11 && rawSlot <= 13:
			// Hat layers — controlled by HairLength
			if hasHelm {
				return false
			}
			return (rawSlot - 11) < length
		case rawSlot == 14:
			if hasHelm {
				return false
			}
			return length >= 2 && style >= 2
		case rawSlot == 15:
			return true // base scalp/hat mesh
		default:
			return true
		}
	}

	switch {
	case rawSlot >= 8 && rawSlot <= 10:
		// Facial hair layers — NOT hidden by helm
		return (rawSlot - 8) < style
	case rawSlot >= 11 && rawSlot <= 13:
		// Head hair layers — hidden by helm
		if hasHelm {
			return false
		}
		return (rawSlot - 11) < length
	case rawSlot == 14:
		// Connecting mesh — visible when both length>=2 and style>=2, hidden by helm
		if hasHelm {
			return false
		}
		return length >= 2 && style >= 2
	case rawSlot == 15:
		// Base mesh — always visible
		return true
	case rawSlot >= 16 && rawSlot <= 18:
		// Extended facial hair layers — hidden by helm
		if hasHelm {
			return false
		}
		return (rawSlot - 16) < style
	default:
		// Unknown hair slot — show by default
		return true
	}
}

// WeaponParticleEntry pairs a weapon's ParticleSprite with its GroupSprite member
// placement (position/rotation relative to the weapon model origin).
type WeaponParticleEntry struct {
	PS        *esf.ParticleSprite
	Placement *esf.SpritePlacement // member pos/rot/scale in weapon local space
}

// PLSlotSoundEntry holds per-CPlayList-slot sound configuration.
// PS2: stored at VICSprite+0x1C0 per slot (52-byte stride).
type PLSlotSoundEntry struct {
	SoundDictID [2]int32   // [0]=Primary Sound, [1]=Secondary Sound DictID from 0xB070
	Volume      [2]float32 // volume multiplier (default 1.0)
	PitchRange  [2]float32 // random pitch variation range (default 0.0)
}

// AnimSoundKeyframe stores a trigger point within an animation.
// PS2: VIHSpriteAnimKeyframe — {float framePos, int32 refID}.
type AnimSoundKeyframe struct {
	Time  float32 // fractional frame position where trigger fires
	RefID int32   // trigger type (PrimSoundRefID, SecSoundRefID, etc.)
}

// PS2 VIHashRefID constants for sound trigger matching.
// Computed by _GLOBAL_$I$_9VICSprite$PrimSoundRefID (0x004283A8).
const (
	PrimSoundRefID    int32 = -0x73F15D57 // "Primary Sound"     = 0x8C0EA2A9
	PrimSoundBPURefID int32 = -0x6AD9246B // "Primary Sound_BPU" = 0x9526DB95
	PrimSoundBPLRefID int32 = -0x6AD92474 // "Primary Sound_BPL" = 0x9526DB8C
	SecSoundRefID     int32 = -0x2B2DC319 // "Secondary Sound"   = 0xD4D23CE7
	SecSoundBPURefID  int32 = -0x432742CD // "Secondary Sound_BPU" = 0xBCD8BD33
	SecSoundBPLRefID  int32 = -0x432742D6 // "Secondary Sound_BPL" = 0xBCD8BD2A

	// Spell FX trigger RefIDs (VIHashResourceID of "Spell FX 1-4").
	// Used by NewPlayAttack → FindTrigger to locate keyframe trigger points,
	// and by Wait(sub-type 3) → HasTripped to detect swing impact frames.
	SpellFX1RefID int32 = 0x53A2401B // "Spell FX 1"
	SpellFX2RefID int32 = 0x53A2401C // "Spell FX 2"
	SpellFX3RefID int32 = 0x53A2401D // "Spell FX 3"
	SpellFX4RefID int32 = 0x53A2401E // "Spell FX 4"
)

// IsSpellFXTrigger returns true if the RefID is a "Spell FX 1-4" trigger.
func IsSpellFXTrigger(refID int32) bool {
	return refID >= SpellFX1RefID && refID <= SpellFX4RefID
}

// SpellFXTriggerIndex returns the 0-3 index for a Spell FX trigger RefID (-1 if not).
func SpellFXTriggerIndex(refID int32) int {
	if refID >= SpellFX1RefID && refID <= SpellFX4RefID {
		return int(refID - SpellFX1RefID)
	}
	return -1
}

// IsSoundTrigger returns true if the RefID is a Primary or Secondary sound trigger.
func IsSoundTrigger(refID int32) bool {
	switch refID {
	case PrimSoundRefID, PrimSoundBPURefID, PrimSoundBPLRefID,
		SecSoundRefID, SecSoundBPURefID, SecSoundBPLRefID:
		return true
	}
	return false
}

// SoundChannel returns 0 for Primary Sound triggers, 1 for Secondary Sound triggers.
func SoundChannel(refID int32) int {
	switch refID {
	case SecSoundRefID, SecSoundBPURefID, SecSoundBPLRefID:
		return 1
	}
	return 0
}

// CharModel holds all GPU meshes for a character model.
type CharModel struct {
	Groups     []meshGroup
	RightHand  [3]float32 // bind-pose position of right hand bone (model space, Y-bottom=0)
	LeftHand   [3]float32 // bind-pose position of left hand bone (model space, Y-bottom=0)
	HeadBone   [3]float32 // bind-pose position of head bone (model space, Y-bottom=0)
	HeadIdx      int        // head bone index in skeleton hierarchy (-1 if not found)
	RightHandIdx int        // right hand bone index (-1 if not found)
	LeftHandIdx  int        // left hand bone index (-1 if not found)
	AnimHead     [3]float32 // animated head bone position (centered model space, updated per frame)
	AnimHeadMat  mat4       // animated head bone full world matrix (centered model space)
	AnimRHandMat mat4       // animated right hand bone full world matrix (centered model space)
	AnimLHandMat mat4       // animated left hand bone full world matrix (centered model space)
	HeadFixScale [3]float32 // PS2 FixScale for head bone (per-axis scale from ESF hierarchy)
	HasBones     bool       // true if hand positions were found from hierarchy
	HasHead    bool       // true if head bone was found
	// Per-frame skinning matrices (set by applySkeletal, used for bone resolver)
	lastSkinMatrices []mat4 // skinning matrices from last applySkeletal call
	IsHumanoid   bool     // true if SkelType == 1 (dual-pair upper/lower body animation)
	HasTSlotList bool     // true if model has TSlotList (player race with equipment slots)
	AnimProfile  AnimProfile // per-model animation role → pair index mapping
	Height       float32    // model height (bbox MaxY - MinY)
	DefaultScale float32    // ESF model scale (from CSprite.DefaultScale, PS2 VIHSprite+0xD0)
	Race         int32     // VICSpriteRace enum from ESF CSprite (ver >= 3, default 9)
	Sex          int32     // VICSpriteSex enum from ESF CSprite (ver >= 3, default 0)
	SkelType     int32     // VICSpriteSkelType enum (VICSprite+0x194): 0=simple, 1=humanoid dual-pair, 2=quadruped, 3=flying
	Skeleton     *Skeleton            // bone hierarchy with bind-pose matrices (nil if no hierarchy)
	Animations   []*esf.HSpriteAnim  // animations from CSprite (nil for items/weapons)
	CenterOff    [3]float32          // centering offset (cx, yBottom, cz) for skinning coord-space conversion
	boneMatBuf   []rl.Matrix         // shared GPU skinning bone matrix buffer (len = BoneCount)
	// Bone-attached particle emitters from CSprite PartEmitters (0x2960).
	// PS2: CreateParticleEmitters spawns VIParticleEmitter per entry;
	// UpdateParticleEmitters tracks bone position each frame.
	PartEmitters []esf.CSpritePartEmitter
	PartDefs     []*esf.ParticleDefinition
	// Weapon-embedded particle effects from GroupSprite ParticleSprite members.
	// Epic/special weapons in ITEM.ESF have ParticleSprite children with embedded
	// ParticleDefinitions. These produce the weapon's visual aura (glow, trail, etc).
	// Placement stores the member's position/rotation within the GroupSprite.
	WeaponParticles []WeaponParticleEntry
	// Sound clips from CSprite 0xB070 (footsteps, attack grunts, etc).
	SoundClipIDs []int32 // DictIDs of sound clips in order from ESF
	ContSoundID  int32   // continuous/ambient sound DictID (0=none)
	ContSoundVol float32 // continuous sound volume
	// PS2-accurate sound triggering: per-CPlayList-slot sound config + animation keyframes.
	// PLSlotSound maps CPlayList slot → sound info (DictID, volume, pitch range).
	PLSlotSound map[int]PLSlotSoundEntry
	// AnimKeyframes maps raw animation index → keyframe triggers.
	// Each keyframe specifies a frame position and trigger RefID (matches sound globals).
	AnimKeyframes map[int][]AnimSoundKeyframe
	// CPlayList slot → raw animation index mapping.
	// Built from ESF CPlayList (0x2910) during loadModel. Used by applySkeletal
	// to resolve animByte (= CPlayList slot number) → raw animation index.
	// For non-humanoid: Key = CPlayList slot, Value = raw index (single anim).
	// For humanoid paired: Key = CPlayList slot, Value = upper body raw index.
	PLSlotToRaw map[int]int
	// PLSlotToRawPair maps CPlayList slot → [upper, lower] raw animation indices.
	// Built for paired-animation models (humanoid, npcHumanoid). Used by applySkeletal
	// to resolve animByte (= ESF CPlayList slot) → raw pair indices.
	PLSlotToRawPair map[int][2]int
	// PLSlotSpeed maps CPlayList slot → playback speed (float32).
	// Negative = reverse playback. This is the authoritative speed per slot;
	// PS2 ProcessPlayList uses this instead of HSpriteAnim.PlaySpeed.
	PLSlotSpeed map[int]float32
	// PLSlotPlayOnce maps CPlayList slot → play-once flag (true = play once, false = loop).
	// Authoritative per-slot play behavior from ESF CPlayList.
	PLSlotPlayOnce map[int]bool
}

// resolveNHAnimIndex converts a non-humanoid F8 animByte to a raw animation
// index using the model's per-profile F8Slots and CPlayList mapping.
//
// Resolution order:
//  1. Profile F8Slots → CPlayList slot → PLSlotToRaw
//  2. Direct animByte as raw index (if within animation range)
//  3. Fallback to 0
func resolveNHAnimIndex(model *CharModel, animByte int) int {
	f8Slots := model.AnimProfile.F8Slots
	if model.PLSlotToRaw == nil || f8Slots == nil || animByte >= len(f8Slots) {
		return animByte
	}
	slot := f8Slots[animByte]
	if rawIdx, ok := model.PLSlotToRaw[slot]; ok {
		return rawIdx
	}
	// Fallback: direct index if within animation range.
	if animByte < len(model.Animations) {
		return animByte
	}
	return 0
}

// WeaponSlot identifies which hand/slot a weapon attaches to.
type WeaponSlot int

const (
	WeaponSlotPrimary   WeaponSlot = iota // right hand
	WeaponSlotSecondary                    // left hand
	WeaponSlotShield                       // left hand (shield)
)

// Alpha cutoff shader — discards fully transparent pixels so they don't write to
// the depth buffer. Fixes see-through artifacts on cylindrical body parts (legs, arms)
// caused by front-face transparent pixels blocking back-face geometry.
// Shader location constants missing from Go raylib bindings.
const (
	shaderLocVertexBoneIds     = 26
	shaderLocVertexBoneWeights = 27
	shaderLocBoneMatrices      = 28
)

const alphaCutoffVS = `#version 330
layout(location = 0) in vec3 vertexPosition;
layout(location = 1) in vec2 vertexTexCoord;
layout(location = 3) in vec4 vertexColor;
uniform mat4 mvp;
out vec2 fragTexCoord;
out vec4 fragColor;
void main() {
    fragTexCoord = vertexTexCoord;
    fragColor = vertexColor;
    gl_Position = mvp * vec4(vertexPosition, 1.0);
}
`

// GPU skinning vertex shader — transforms bind-pose vertices using bone matrices.
// Bone indices come as unsigned bytes (0-255) via vertexBoneIds attribute.
// Up to 4 bones per vertex, blended by vertexBoneWeights.
const skinAlphaCutoffVS = `#version 330
layout(location = 0) in vec3 vertexPosition;
layout(location = 1) in vec2 vertexTexCoord;
layout(location = 3) in vec4 vertexColor;
layout(location = 7) in vec4 vertexBoneIds;
layout(location = 8) in vec4 vertexBoneWeights;
uniform mat4 mvp;
uniform mat4 boneMatrices[128];
out vec2 fragTexCoord;
out vec4 fragColor;
void main() {
    int b0 = int(vertexBoneIds.x);
    int b1 = int(vertexBoneIds.y);
    int b2 = int(vertexBoneIds.z);
    int b3 = int(vertexBoneIds.w);
    mat4 skin = boneMatrices[b0] * vertexBoneWeights.x
              + boneMatrices[b1] * vertexBoneWeights.y
              + boneMatrices[b2] * vertexBoneWeights.z
              + boneMatrices[b3] * vertexBoneWeights.w;
    vec4 skinnedPos = skin * vec4(vertexPosition, 1.0);
    fragTexCoord = vertexTexCoord;
    fragColor = vertexColor;
    gl_Position = mvp * skinnedPos;
}
`

const alphaCutoffFS = `#version 330
in vec2 fragTexCoord;
in vec4 fragColor;
uniform sampler2D texture0;
uniform vec4 colDiffuse;
out vec4 finalColor;
void main() {
    vec4 texel = texture(texture0, fragTexCoord);
    vec4 color = texel * colDiffuse * fragColor;
    if (texel.a < 0.5) discard;
    finalColor = vec4(color.rgb, 1.0);
}
`

// Alpha blend shader — same as cutoff but without discard.
// Used for the armor overlay pass where transparent pixels should blend
// with the skin layer underneath (rendered in the previous pass).
const alphaBlendFS = `#version 330
in vec2 fragTexCoord;
in vec4 fragColor;
uniform sampler2D texture0;
uniform vec4 colDiffuse;
out vec4 finalColor;
void main() {
    vec4 texel = texture(texture0, fragTexCoord);
    finalColor = texel * colDiffuse * fragColor;
}
`

// Clothing composite shader — single-pass texture compositing for default clothing.
// Reads clothing atlas (texture0) and skin (texture1). Alpha-blends clothing over
// skin with bilinear-fringe recovery. Avoids 2-pass depth/blend issues.
//
// GPU bilinear filtering in straight-alpha space contaminates RGB at atlas edges
// (averages opaque tattoo/clothing color with black alpha=0 padding). Dividing
// by alpha recovers the pre-filter color, then mix() blends smoothly with skin.
// This eliminates black outlines on tattoos and clothing edges.
const clothingCompositeFS = `#version 330
in vec2 fragTexCoord;
in vec4 fragColor;
uniform sampler2D texture0;
uniform sampler2D texture1;
uniform vec4 colDiffuse;
out vec4 finalColor;
void main() {
    vec4 cloth = texture(texture0, fragTexCoord);
    vec4 skin = texture(texture1, fragTexCoord);
    vec3 tint = colDiffuse.rgb * fragColor.rgb;
    float alpha = max(skin.a, cloth.a);
    if (alpha < 0.5) discard;
    // Recover clothing color from bilinear-filtered boundary pixels.
    vec3 cleanCloth = cloth.a > 0.01 ? cloth.rgb / cloth.a : vec3(0.0);
    // Alpha-blend recovered clothing over skin — smooth transition at edges.
    vec3 blended = mix(skin.rgb, cleanCloth, cloth.a);
    finalColor = vec4(blended * tint, 1.0);
}
`

// dilateTexture fills transparent padding pixels with the nearest opaque pixel's
// color, preventing black fringe when bilinear filtering or mipmapping samples
// UV atlas padding. Uses alpha threshold of 26 (~10% opacity) to also catch
// near-transparent dark edge pixels left by PS2 alpha scaling (PS2 alpha 1-12
// scales to OpenGL 2-24, which are nearly invisible but have dark/black RGB).
func dilateTexture(img *image.NRGBA) {
	const alphaThreshold = 26 // treat pixels below this as transparent
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	// Mark which pixels are sufficiently opaque (valid content).
	opaque := make([]bool, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 4
			if img.Pix[i+3] >= alphaThreshold {
				opaque[y*w+x] = true
			}
		}
	}
	// Dilate: for each transparent pixel, find nearest opaque neighbor and copy RGB.
	// Run 4 passes to fill immediate padding for bilinear filtering.
	dx := []int{-1, 1, 0, 0, -1, -1, 1, 1}
	dy := []int{0, 0, -1, 1, -1, 1, -1, 1}
	for pass := 0; pass < 4; pass++ {
		changed := false
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				if opaque[y*w+x] {
					continue
				}
				// Find first opaque neighbor.
				for d := 0; d < 8; d++ {
					nx, ny := x+dx[d], y+dy[d]
					if nx < 0 || nx >= w || ny < 0 || ny >= h {
						continue
					}
					if opaque[ny*w+nx] {
						si := (ny*w + nx) * 4
						di := (y*w + x) * 4
						img.Pix[di] = img.Pix[si]
						img.Pix[di+1] = img.Pix[si+1]
						img.Pix[di+2] = img.Pix[si+2]
						// Zero alpha so the pixel is discarded by alpha cutoff,
						// but bilinear/mipmap at edges will sample valid colors.
						img.Pix[di+3] = 0
						changed = true
						break
					}
				}
			}
		}
		// Mark newly filled pixels as "opaque" for the next pass.
		if changed {
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					if !opaque[y*w+x] {
						i := (y*w + x) * 4
						if img.Pix[i] != 0 || img.Pix[i+1] != 0 || img.Pix[i+2] != 0 {
							opaque[y*w+x] = true
						}
					}
				}
			}
		} else {
			break
		}
	}
}

// ModelManager loads and caches character models from CHAR.ESF and ITEM.ESF.
// SoundLoader can register decoded sound clips for playback.
type SoundLoader interface {
	LoadModelSounds(clips []esf.SoundClip) int
}

type ModelManager struct {
	file         *esf.ObjFile           // CHAR.ESF
	custFile     *esf.ObjFile           // CHARCUST.CSF (decompressed) — equipment textures
	faceFile     *esf.ObjFile           // CHARFACE.CSF (decompressed) — face textures
	itemFile     *esf.ObjFile           // ITEM.ESF (nil if not loaded)
	soundLoader  SoundLoader            // audio system for loading model sounds
	cache        map[int32]*CharModel   // character ModelID → model
	itemCache    map[int32]*CharModel   // item/weapon ModelID → model
	helmCache    map[byte]*CharModel    // helm index → helm 3D model (loaded from CHARCUST.CSF)
	whiteTex          rl.Texture2D
	hairColorTexCache [8]rl.Texture2D    // solid-color textures for gnome mustache tinting
	texCache          map[texKey]rl.Texture2D // Surface DictID + source file → GPU texture
	shader             rl.Shader        // alpha cutoff shader for character models
	blendShader        rl.Shader        // alpha blend shader for armor overlay pass
	clothingShader     rl.Shader        // clothing overlay: discard transparent, force opaque
	skinShader         rl.Shader        // GPU-skinned alpha cutoff shader
	skinBlendShader    rl.Shader        // GPU-skinned alpha blend shader
	skinClothingShader rl.Shader        // GPU-skinned clothing composite shader
	bodySlots       map[int32]bool                    // DictIDs of body-slot textures (slots 0-4) needing dilation
	animStates      animStateMap                      // per-entity animation state
	boneLogCounter  int                               // throttle bone debug logging
	ShowBoneDebug   bool                              // F3 toggle: render bone position spheres
	// Per-entity bone matrix cache: stores a copy of skinning matrices after each
	// entity's applySkeletal, so bone lookups (spell FX, weapon emitters) remain
	// valid even after other entities with the same model ID overwrite the shared
	// CharModel.lastSkinMatrices.
	entityBones     map[uint32]entityBoneCache
	// Camera position for per-bone animation LOD. Set each frame via SetCameraPos.
	camX, camY, camZ float32
	// Callback fired when a play-once animation reaches its last frame (attack impact).
	// Parameters: entityID, animSlot (CPlayList slot number).
	OnPlayOnceFinish func(entityID uint32, animSlot byte)
}

// entityBoneCache stores per-entity bone data captured at draw time.
type entityBoneCache struct {
	modelID      int32
	skinMatrices []mat4
	rHandMat     mat4
	lHandMat     mat4
}

// NewManager opens a CHAR.ESF file and prepares for model loading.
// Also loads CHARCUST.CSF from the same directory for equipment textures.
func NewManager(charESFPath string) (*ModelManager, error) {
	file, err := esf.Open(charESFPath)
	if err != nil {
		return nil, err
	}
	if err := file.BuildDictionary(); err != nil {
		return nil, err
	}

	// Load CHARCUST.CSF (compressed) for armor/hair/robe equipment textures.
	var custFile *esf.ObjFile
	custCSFPath := filepath.Join(filepath.Dir(charESFPath), "CHARCUST.CSF")
	custData, err := esf.DecompressCSF(custCSFPath)
	if err != nil {
		log.Printf("models: CHARCUST.CSF not available: %v", err)
	} else {
		custFile, err = esf.OpenBytes(custData)
		if err != nil {
			log.Printf("models: CHARCUST parse error: %v", err)
		} else if err := custFile.BuildDictionary(); err != nil {
			log.Printf("models: CHARCUST dict error: %v", err)
			custFile = nil
		} else {
			log.Printf("models: CHARCUST.CSF loaded (%d dictionary entries)", custFile.DictLen())
		}
	}

	// Load CHARFACE.CSF (compressed) for face textures by race/sex/faceOption.
	var faceFile *esf.ObjFile
	faceCSFPath := filepath.Join(filepath.Dir(charESFPath), "CHARFACE.CSF")
	faceData, err := esf.DecompressCSF(faceCSFPath)
	if err != nil {
		log.Printf("models: CHARFACE.CSF not available: %v", err)
	} else {
		faceFile, err = esf.OpenBytes(faceData)
		if err != nil {
			log.Printf("models: CHARFACE parse error: %v", err)
		} else if err := faceFile.BuildDictionary(); err != nil {
			log.Printf("models: CHARFACE dict error: %v", err)
			faceFile = nil
		} else {
			log.Printf("models: CHARFACE.CSF loaded (%d dictionary entries)", faceFile.DictLen())
		}
	}

	return newManagerFromFiles(file, custFile, faceFile)
}

// NewManagerFromFiles creates a ModelManager from pre-opened ObjFiles.
// charFile is required; custFile and faceFile may be nil.
func NewManagerFromFiles(charFile, custFile, faceFile *esf.ObjFile) (*ModelManager, error) {
	if err := charFile.BuildDictionary(); err != nil {
		return nil, err
	}
	if custFile != nil {
		if err := custFile.BuildDictionary(); err != nil {
			log.Printf("models: CHARCUST dict error: %v", err)
			custFile = nil
		}
	}
	if faceFile != nil {
		if err := faceFile.BuildDictionary(); err != nil {
			log.Printf("models: CHARFACE dict error: %v", err)
			faceFile = nil
		}
	}
	return newManagerFromFiles(charFile, custFile, faceFile)
}

// SetSoundLoader sets the audio system used to register model sounds during loading.
func (m *ModelManager) SetSoundLoader(sl SoundLoader) {
	m.soundLoader = sl
}

func newManagerFromFiles(file, custFile, faceFile *esf.ObjFile) (*ModelManager, error) {

	// White fallback texture for untextured geometry.
	whiteImg := rl.GenImageColor(4, 4, rl.White)
	whiteTex := rl.LoadTextureFromImage(whiteImg)
	rl.UnloadImage(whiteImg)

	log.Printf("models: CHAR.ESF loaded (%d dictionary entries)", file.DictLen())

	shader := rl.LoadShaderFromMemory(alphaCutoffVS, alphaCutoffFS)
	blendShader := rl.LoadShaderFromMemory(alphaCutoffVS, alphaBlendFS)
	clothingShader := rl.LoadShaderFromMemory(alphaCutoffVS, clothingCompositeFS)
	// Tell the composite shader that texture1 sampler is bound to texture unit 1.
	tex1Loc := rl.GetShaderLocation(clothingShader, "texture1")
	tex1Unit := int32(1)
	tex1AsFloat := *(*float32)(unsafe.Pointer(&tex1Unit))
	rl.SetShaderValue(clothingShader, tex1Loc, []float32{tex1AsFloat}, rl.ShaderUniformInt)

	// GPU-skinned shader variants (same fragment shaders, skinning vertex shader).
	skinShader := rl.LoadShaderFromMemory(skinAlphaCutoffVS, alphaCutoffFS)
	skinBlendShader := rl.LoadShaderFromMemory(skinAlphaCutoffVS, alphaBlendFS)
	skinClothingShader := rl.LoadShaderFromMemory(skinAlphaCutoffVS, clothingCompositeFS)
	// Register bone attribute/uniform locations for all skinned shaders.
	for _, s := range []rl.Shader{skinShader, skinBlendShader, skinClothingShader} {
		s.UpdateLocation(shaderLocVertexBoneIds, rl.GetShaderLocationAttrib(s, "vertexBoneIds"))
		s.UpdateLocation(shaderLocVertexBoneWeights, rl.GetShaderLocationAttrib(s, "vertexBoneWeights"))
		s.UpdateLocation(shaderLocBoneMatrices, rl.GetShaderLocation(s, "boneMatrices"))
	}
	skinTex1Loc := rl.GetShaderLocation(skinClothingShader, "texture1")
	rl.SetShaderValue(skinClothingShader, skinTex1Loc, []float32{tex1AsFloat}, rl.ShaderUniformInt)
	log.Printf("models: shaders loaded (cutoff=%d, blend=%d, clothing=%d, skin=%d)", shader.ID, blendShader.ID, clothingShader.ID, skinShader.ID)

	return &ModelManager{
		file:        file,
		custFile:    custFile,
		faceFile:    faceFile,
		cache:       make(map[int32]*CharModel),
		itemCache:   make(map[int32]*CharModel),
		helmCache:   make(map[byte]*CharModel),
		whiteTex:    whiteTex,
		texCache:    make(map[texKey]rl.Texture2D),
		shader:             shader,
		blendShader:        blendShader,
		clothingShader:     clothingShader,
		skinShader:         skinShader,
		skinBlendShader:    skinBlendShader,
		skinClothingShader: skinClothingShader,
		bodySlots:           make(map[int32]bool),
		animStates:      make(animStateMap),
	}, nil
}

// DrawEntity renders a character model at the given world position and facing.
// equip controls which variant meshes are drawn (nil draws all base meshes).
// entityID uniquely identifies the entity for per-entity animation state.
// animByte selects the animation (0=idle, 1=walk, etc.).
// dt is the frame delta time in seconds for advancing animation playback.
// Returns false if the model is unavailable (caller should fall back to sphere).
// BoneWorldPos returns the animated world position of a bone for a given entity.
// modelID = entity's model, entityID = ObjectID (for animation state lookup),
// nodeID = spell attachment slot (0=right hand, 1=left hand, 8=head),
// x/y/z/facing = entity world position/rotation.
// Returns (bx, by, bz, true) or (0, 0, 0, false) if bone unavailable.
// PS2: GetNodeTransform at 0x0047A490 — CalcSlotWorldTransform for CSprite.
// PS2 maps nodeID through ASlotList (attachment slots) to skeleton bone indices.
func (m *ModelManager) BoneWorldPos(modelID int32, entityID uint32, nodeID uint16, x, y, z, facing, entityScale float32) (float32, float32, float32, bool) {
	if m == nil || modelID == 0 {
		return 0, 0, 0, false
	}
	model, ok := m.cache[modelID]
	if !ok || model == nil || model.Skeleton == nil {
		return 0, 0, 0, false
	}
	// Map spell attachment slot to skeleton bone index.
	// PS2 ASlotList: slot 0 = right hand, slot 1 = left hand.
	// NodeIDList: slot 8 = head bone.
	idx := -1
	switch nodeID {
	case 0:
		idx = model.RightHandIdx
	case 1:
		idx = model.LeftHandIdx
	case 8:
		idx = model.HeadIdx
	default:
		idx = int(nodeID) // raw fallback
	}
	// Use per-entity cached skin matrices (survives past draw call).
	// Falls back to shared model matrices if entity not in cache.
	skinMats := model.lastSkinMatrices
	if bc, ok := m.entityBones[entityID]; ok && bc.modelID == modelID {
		skinMats = bc.skinMatrices
	}
	if idx < 0 || idx >= len(skinMats) || idx >= len(model.Skeleton.BindWorld) {
		return 0, 0, 0, false
	}
	// Compute animated bone world position (raw model space).
	aw := mulMat4(skinMats[idx], model.Skeleton.BindWorld[idx])
	bx := aw[3]
	by := aw[7]
	bz := aw[11]

	// Apply centerTransform (same as DrawEntity) to convert to world space.
	cosF := float32(math.Cos(float64(facing)))
	sinF := float32(math.Sin(float64(facing)))
	scale := entityScale
	if scale <= 0 {
		scale = 1.0
	}
	scale *= model.DefaultScale
	cx, cy, cz := model.CenterOff[0], model.CenterOff[1], model.CenterOff[2]
	ct := centerTransformMat(sinF, cosF, scale, cx, cy, cz, x, y, z)
	wx, wy, wz := transformPoint(ct, bx, by, bz)
	return wx, wy, wz, true
}

// BoneWorldPosByIdx computes world-space position for a raw skeleton bone index.
// Unlike BoneWorldPos which maps through ASlotList, this uses the bone index directly —
// needed by CSprite PartEmitters which store raw BoneNodeIdx values.
// PS2: UpdateParticleEmitters reads bone matrix via (BoneNodeIdx << 8) offset.
func (m *ModelManager) BoneWorldPosByIdx(modelID int32, entityID uint32, boneIdx int, x, y, z, facing, entityScale float32) (float32, float32, float32, bool) {
	if m == nil || modelID == 0 {
		return 0, 0, 0, false
	}
	model, ok := m.cache[modelID]
	if !ok || model == nil || model.Skeleton == nil {
		return 0, 0, 0, false
	}
	// Use per-entity cached skin matrices (survives past draw call).
	skinMats := model.lastSkinMatrices
	if bc, ok := m.entityBones[entityID]; ok && bc.modelID == modelID {
		skinMats = bc.skinMatrices
	}
	if boneIdx < 0 || boneIdx >= len(skinMats) || boneIdx >= len(model.Skeleton.BindWorld) {
		return 0, 0, 0, false
	}
	aw := mulMat4(skinMats[boneIdx], model.Skeleton.BindWorld[boneIdx])
	bx := aw[3]
	by := aw[7]
	bz := aw[11]

	cosF := float32(math.Cos(float64(facing)))
	sinF := float32(math.Sin(float64(facing)))
	scale := entityScale
	if scale <= 0 {
		scale = 1.0
	}
	scale *= model.DefaultScale
	cx, cy, cz := model.CenterOff[0], model.CenterOff[1], model.CenterOff[2]
	ct := centerTransformMat(sinF, cosF, scale, cx, cy, cz, x, y, z)
	wx, wy, wz := transformPoint(ct, bx, by, bz)
	return wx, wy, wz, true
}

// WeaponPointWorldPos transforms a weapon-local point to world-space coordinates.
// Uses the same bone matrix + entity transform as DrawWeapon. The localPt is in
// weapon model space (e.g., from GroupSprite member Rot field which encodes offset).
func (m *ModelManager) WeaponPointWorldPos(charModelID int32, entityID uint32, localPt [3]float32, x, y, z, facing float32, slot WeaponSlot) (float32, float32, float32, bool) {
	if m == nil {
		return 0, 0, 0, false
	}
	model := m.cache[charModelID]
	if model == nil || model.Skeleton == nil || !model.HasBones {
		return 0, 0, 0, false
	}

	sinF := float32(math.Sin(float64(facing)))
	cosF := float32(math.Cos(float64(facing)))
	wScale := float32(1.0)
	if model.DefaultScale > 0 {
		wScale = model.DefaultScale
	}
	groundAdj := float32(0)
	if model.Skeleton != nil {
		groundAdj = model.Skeleton.IdleBounce * wScale
	}
	wpCx, wpCy, wpCz := model.CenterOff[0], model.CenterOff[1], model.CenterOff[2]
	entityMat := centerTransformMat(sinF, cosF, wScale, wpCx, wpCy, wpCz, x, y-groundAdj, z)

	// Use per-entity cached hand matrices if available.
	var boneMat mat4
	if bc, ok := m.entityBones[entityID]; ok && bc.modelID == charModelID {
		switch slot {
		case WeaponSlotPrimary:
			boneMat = bc.rHandMat
		case WeaponSlotSecondary, WeaponSlotShield:
			boneMat = bc.lHandMat
		}
	} else {
		switch slot {
		case WeaponSlotPrimary:
			boneMat = model.AnimRHandMat
		case WeaponSlotSecondary, WeaponSlotShield:
			boneMat = model.AnimLHandMat
		}
	}
	// Same offset as DrawWeapon for non-shield weapons
	if slot != WeaponSlotShield {
		boneMat = mulMat4(boneMat, mat4{
			1, 0, 0, 0,
			0, 1, 0, -0.01,
			0, 0, 1, 0,
			0, 0, 0, 1,
		})
	}

	final := mulMat4(entityMat, boneMat)
	// Transform localPt through the full matrix
	wx := final[0]*localPt[0] + final[1]*localPt[1] + final[2]*localPt[2] + final[3]
	wy := final[4]*localPt[0] + final[5]*localPt[1] + final[6]*localPt[2] + final[7]
	wz := final[8]*localPt[0] + final[9]*localPt[1] + final[10]*localPt[2] + final[11]
	return wx, wy, wz, true
}

// GetPartDefTexture extracts the texture image for a PartDef's TexDictID from CHAR.ESF.
// Returns nil if the texture is not found.
func (m *ModelManager) GetPartDefTexture(texDictID int32) *image.NRGBA {
	if m == nil || m.file == nil || texDictID == 0 {
		return nil
	}
	obj, err := m.file.FindObject(texDictID)
	if err != nil || obj == nil {
		return nil
	}
	surf, ok := obj.(*esf.Surface)
	if !ok || surf == nil || surf.Image == nil {
		return nil
	}
	return surf.Image
}

// GetItemPartDefTexture extracts a particle texture from ITEM.ESF.
func (m *ModelManager) GetItemPartDefTexture(texDictID int32) *image.NRGBA {
	if m == nil || m.itemFile == nil || texDictID == 0 {
		return nil
	}
	obj, err := m.itemFile.FindObject(texDictID)
	if err != nil || obj == nil {
		return nil
	}
	surf, ok := obj.(*esf.Surface)
	if !ok || surf == nil || surf.Image == nil {
		return nil
	}
	return surf.Image
}

// GetCachedItem returns a cached item model by ID (nil if not loaded).
func (m *ModelManager) GetCachedItem(modelID int32) (*CharModel, bool) {
	model, ok := m.itemCache[modelID]
	return model, ok
}

func (m *ModelManager) DrawEntity(modelID int32, x, y, z, facing, scale float32, equip *EquipmentState, entityID uint32, animByte, actionAnimByte byte, dt float32, isPlayer bool, callerSpeed float32) bool {
	if modelID == 0 {
		return false
	}

	model, cached := m.cache[modelID]
	if !cached {
		model = m.loadModel(modelID)
		m.cache[modelID] = model // nil stored for failed loads
	}
	if model == nil {
		return false
	}

	// Apply skeletal animation (CPU skinning) if model has a skeleton.
	// Each entity gets its own AnimState keyed by modelID+entityID.
	// Skinning overwrites the shared GPU mesh buffers, then we draw immediately.
	// This works because entities are drawn sequentially on the main thread.
	if model.Skeleton != nil && len(model.Animations) > 0 {
		m.applySkeletal(model, modelID, entityID, animByte, actionAnimByte, dt, isPlayer, callerSpeed, x, y, z)
	}

	// PS2 centerTransform: Scale(s) * T(-c) * R(rot, pos) * T(+c) [row-vector]
	// Applied as the model transform for body mesh rendering AND attachments.
	// Vertices are in raw model space; centerTransform handles centering at render time.
	if scale <= 0 {
		scale = 1.0
	}
	scale *= model.DefaultScale
	cosF := float32(math.Cos(float64(facing)))
	sinF := float32(math.Sin(float64(facing)))
	// Root bone animates freely (matching PS2 ProcessHierarchy). StepBounce
	// = root Y delta from bind pose. SUBTRACT it from entity Y to keep feet
	// on ground (PS2 VIWalkControl does this via entity position adjustment).
	groundAdj := float32(0)
	if model.Skeleton != nil {
		groundAdj = model.Skeleton.IdleBounce * scale
	}
	cx, cy, cz := model.CenterOff[0], model.CenterOff[1], model.CenterOff[2]
	ct := centerTransformMat(sinF, cosF, scale, cx, cy, cz, x, y-groundAdj, z)
	transform := rl.Matrix{
		M0: ct[0], M4: ct[1], M8: ct[2], M12: ct[3],
		M1: ct[4], M5: ct[5], M9: ct[6], M13: ct[7],
		M2: ct[8], M6: ct[9], M10: ct[10], M14: ct[11],
		M3: ct[12], M7: ct[13], M11: ct[14], M15: ct[15],
	}

	// PS2 meshes have inconsistent triangle winding — disable backface culling.
	// Alpha cutoff shader discards transparent pixels (no depth buffer artifacts).
	rl.DisableBackfaceCulling()
	for i := range model.Groups {
		if !model.Groups[i].shouldDraw(equip) {
			continue
		}
		mg := &model.Groups[i]

		// Equipment texture swap: resolve replacement texture based on TextureSlot.
		// Slots 0-4: armor (robe overrides chest/legs), 5: hair, 7: face.
		// skipSwap prevents hair color texture from replacing the baked texture:
		//   - Gnome hat top (slot 13): keeps baked red color
		//   - Human eyepatch (slot 10, style 3): keeps baked dark color
		//   - Null-texture accessories handled separately below
		skipSwap := equip != nil && mg.TextureSlot == 5 &&
			((equip.Race == 3 && mg.RawSlotID == 13) || (equip.Race == 0 && mg.RawSlotID == 10))



		// Gnome accessory meshes use a null texture (16x16 all-transparent).
		// Both have white vertex colors, so we must provide color via texture/colDiffuse.
		//   Glasses (slot 10): whiteTex + dark colDiffuse (dark frames).
		//   Mustache (slot 17): solid hair-color texture (matches beard).
		var nullTexSwapped bool
		var nullTexOrig rl.Texture2D
		var nullColorOrig rl.Color
		if skipSwap {
			maps := (*[32]rl.MaterialMap)(unsafe.Pointer(mg.Material.Maps))
			curTex := maps[rl.MapDiffuse].Texture
			if curTex.Width <= 16 && curTex.Height <= 16 {
				nullTexOrig = curTex
				nullColorOrig = maps[rl.MapDiffuse].Color
				if mg.RawSlotID == 10 && equip != nil && equip.Race != 0 {
					// Facial hair slot 10 (gnome mustache, barbarian beard, etc.):
					// solid hair-color texture (changes with hair color)
					maps[rl.MapDiffuse].Texture = m.getHairColorTex(equip.HairColor)
				} else {
					// Human eyepatch, gnome glasses, other null-texture accessories: dark fixed color
					maps[rl.MapDiffuse].Texture = m.whiteTex
					maps[rl.MapDiffuse].Color = rl.NewColor(40, 40, 40, 255)
				}
				nullTexSwapped = true
			}
		}

		var swapped bool
		var origTex rl.Texture2D
		if mg.TextureSlot >= 0 && equip != nil && !skipSwap {
			dictID, texSrc := m.resolveSlotTexture(equip, mg.TextureSlot)
			if dictID != 0 {
				replaceTex := m.getEquipmentTexture(dictID, texSrc)
				if replaceTex.ID != m.whiteTex.ID {
					maps := (*[32]rl.MaterialMap)(unsafe.Pointer(mg.Material.Maps))
					origTex = maps[rl.MapDiffuse].Texture

					// Armor slots (0-4): two-pass rendering for armor, single-pass for robes.
					// Armor textures have transparent areas where skin shows through
					// (e.g., exposed arms, neckline). Robes cover the full body opaquely.
					if mg.TextureSlot >= TextSlotChest && mg.TextureSlot <= TextSlotBoots {
						// Check if this slot's texture came from a robe (fully opaque coverage).
						// An override wins over the robe in resolveSlotTexture, so it must
						// also take the armor path here or it would render single-pass and
						// lose the skin underneath wherever the art is transparent.
						isRobe := equip.slotOverride(mg.TextureSlot) == 0 &&
							equip.Robe < 4 && GetRobeTextureDictID(equip.Robe, mg.TextureSlot) != 0
						if isRobe {
							// Robes: single-pass direct swap with robe tint.
							tint := GetTintColor(equip.RobeTint)
							tintColor := rl.NewColor(tint.R, tint.G, tint.B, tint.A)
							origColor := maps[rl.MapDiffuse].Color
							maps[rl.MapDiffuse].Texture = replaceTex
							maps[rl.MapDiffuse].Color = tintColor
							rl.DrawMesh(mg.Mesh, mg.Material, transform)
							maps[rl.MapDiffuse].Texture = origTex
							maps[rl.MapDiffuse].Color = origColor
							continue
						}
						// Armor: two-pass rendering (skin base + tinted armor overlay with alpha blend).
						// Pass 1: skin texture (base body, alpha cutoff shader).
						rl.DrawMesh(mg.Mesh, mg.Material, transform)
						// Pass 2: overlay on top (alpha blend, no depth write, tinted).
						// PolygonOffset pushes overlay slightly closer so it reliably
						// passes the GL_LESS depth test at same-depth as pass 1.
						_, tintIdx := armorSetForSlot(equip, mg.TextureSlot)
						tint := GetTintColor(tintIdx)
						tintColor := rl.NewColor(tint.R, tint.G, tint.B, tint.A)
						origColor := maps[rl.MapDiffuse].Color
						origShader := mg.Material.Shader
						maps[rl.MapDiffuse].Texture = replaceTex
						maps[rl.MapDiffuse].Color = tintColor
						if mg.GPUSkinned {
							mg.Material.Shader = m.skinBlendShader
						} else {
							mg.Material.Shader = m.blendShader
						}
						rl.DisableDepthMask()
						gfx.EnablePolygonOffset()
						rl.DrawMesh(mg.Mesh, mg.Material, transform)
						gfx.DisablePolygonOffset()
						rl.EnableDepthMask()
						mg.Material.Shader = origShader
						maps[rl.MapDiffuse].Texture = origTex
						maps[rl.MapDiffuse].Color = origColor
						continue
					}

					// Non-armor slots (hair, face): direct texture swap.
					maps[rl.MapDiffuse].Texture = replaceTex
					// Hair: PS2 SetHair uses hairColor as $Hair texture index (0-7).
					// The 8 hair textures encode different colors — no Tints multiply.
					swapped = true
				}
			} else if mg.TextureSlot >= TextSlotChest && mg.TextureSlot <= TextSlotBoots && mg.Layer1DictID != 0 {
				// No armor/robe equipped, but model has a layer 1 clothing texture.
				// Single-pass composite: layer1 (clothing) over layer0 (skin).
				layer1Tex := m.getEquipmentTexture(mg.Layer1DictID, texSrcAuto)
				if layer1Tex.ID != m.whiteTex.ID {
					maps := (*[32]rl.MaterialMap)(unsafe.Pointer(mg.Material.Maps))
					baseTex := maps[rl.MapDiffuse].Texture
					origShader := mg.Material.Shader
					maps[rl.MapDiffuse].Texture = layer1Tex  // texture0 = clothing overlay
					maps[rl.MapSpecular].Texture = baseTex   // texture1 = skin base
					if mg.GPUSkinned {
						mg.Material.Shader = m.skinClothingShader
					} else {
						mg.Material.Shader = m.clothingShader
					}
					rl.DrawMesh(mg.Mesh, mg.Material, transform)
					mg.Material.Shader = origShader
					maps[rl.MapDiffuse].Texture = baseTex
					maps[rl.MapSpecular].Texture = rl.Texture2D{}
					continue
				}
			}
		}

		// Null-texture mesh groups (SurfDictID=0) not handled by equipment swap
		// should render dark — e.g., hood interiors, inside-mouth meshes.
		var nullDarkSwapped bool
		var nullDarkOrig rl.Color
		if mg.SurfDictID == 0 && !nullTexSwapped && !swapped {
			maps := (*[32]rl.MaterialMap)(unsafe.Pointer(mg.Material.Maps))
			nullDarkOrig = maps[rl.MapDiffuse].Color
			maps[rl.MapDiffuse].Color = rl.NewColor(20, 20, 20, 255)
			nullDarkSwapped = true
		}

		rl.DrawMesh(mg.Mesh, mg.Material, transform)

		if swapped {
			maps := (*[32]rl.MaterialMap)(unsafe.Pointer(mg.Material.Maps))
			maps[rl.MapDiffuse].Texture = origTex
		}
		if nullTexSwapped {
			maps := (*[32]rl.MaterialMap)(unsafe.Pointer(mg.Material.Maps))
			maps[rl.MapDiffuse].Texture = nullTexOrig
			maps[rl.MapDiffuse].Color = nullColorOrig
		}
		if nullDarkSwapped {
			maps := (*[32]rl.MaterialMap)(unsafe.Pointer(mg.Material.Maps))
			maps[rl.MapDiffuse].Color = nullDarkOrig
		}
	}
	rl.EnableBackfaceCulling()

	// F3 debug: render bone position spheres
	if m.ShowBoneDebug && model.Skeleton != nil {
		boneColors := map[int]rl.Color{
			model.HeadIdx:      rl.Red,
			model.RightHandIdx: rl.Blue,
			model.LeftHandIdx:  rl.Green,
		}
		for bi := range model.Skeleton.Nodes {
			// Get bone world position via centerTransform
			bx, by, bz := model.AnimHead[0], model.AnimHead[1], model.AnimHead[2]
			if bi != model.HeadIdx {
				// Generic bone: extract from last skin matrices
				if bi < len(model.lastSkinMatrices) && bi < len(model.Skeleton.BindWorld) {
					aw := mulMat4(model.lastSkinMatrices[bi], model.Skeleton.BindWorld[bi])
					bx, by, bz = aw[3], aw[7], aw[11]
				} else {
					continue
				}
			}
			wx, wy, wz := transformPoint(ct, bx, by, bz)
			col := rl.Yellow
			radius := float32(0.02)
			if c, ok := boneColors[bi]; ok {
				col = c
				radius = 0.04
			}
			rl.DrawSphere(rl.Vector3{X: wx, Y: wy, Z: wz}, radius, col)
		}
	}

	// Draw helm as a separate 3D model attached to the character's head.
	// PS2 uses VIScene::CopySprite + VIHSprite::Attach to parent helm sprites
	// to the character hierarchy. We approximate by positioning at head height.
	if equip != nil && equip.Helm > 0 && equip.Helm != 255 && !equip.HideHelm && model.IsHumanoid {
		m.drawHelm(equip.Helm, model, x, y, z, facing, scale, equip.HelmTint)
	}

	return true
}

// DebugAnimUpper is the debug override for upper body animation index (-1 = auto).
// DebugAnimLower is the debug override for lower body animation index (-1 = auto).
// Cycle upper with PageUp/PageDown, lower with Home/End.
var DebugAnimUpper int = -1
var DebugAnimLower int = -1

// applySkeletal computes per-frame animation pose and updates dynamic mesh buffers.
// Called once per entity per frame, before drawing the model's mesh groups.
//
// PS2 VICSprite uses GetSkeletonType to determine animation mode:
//   Humanoid (has TSlotList): paired animations — even=upper body (~28 bones),
//     odd=lower body (~12 bones). SetLocomotion controls lower, SetAction controls upper.
//   Non-humanoid (creatures): single whole-body animations — each index drives all bones.
//     animByte maps directly to animation index (0=idle, 1=walk, 2=run).
//
// Debug: DebugAnimUpper/DebugAnimLower allow independent index control (player only).
func (m *ModelManager) applySkeletal(model *CharModel, modelID int32, entityID uint32, animByte, actionAnimByte byte, dt float32, isPlayer bool, callerSpeed float32, x, y, z float32) {
	skel := model.Skeleton
	anims := model.Animations

	var upperIdx, lowerIdx int

	// Get or create per-entity animation state.
	key := uint64(entityID)
	as := m.animStates.getOrCreate(key)

	// Periodic special idle (PS2 animTimerA: 20s + rand(20s) at VICSprite+0x11B0).
	specialIdle := model.AnimProfile.SpecialIdle
	if animByte == 0 {
		as.IdleTimer += dt
		if as.IdleTarget <= 0 {
			as.IdleTarget = 20.0 + float32(rand.Intn(20))
		}
		if as.IdleTimer >= as.IdleTarget {
			animByte = specialIdle
		}
	} else {
		as.IdleTimer = 0
		as.IdleTarget = 0
	}

	if model.AnimProfile.F8Slots == nil {
		// Paired animations (upper body + lower body).
		// animByte is an ESF CPlayList slot number; resolve via PLSlotToRawPair.
		// PS2 dual-pair: when actionAnimByte != 0, upper body uses the action
		// animation while lower body keeps the locomotion animation. This enables
		// attacking/emoting while running (pair 0 = loco legs, pair 1 = action upper).
		upperSlot := int(animByte)
		lowerSlot := int(animByte)
		if actionAnimByte != 0 {
			upperSlot = int(actionAnimByte)
			// lowerSlot stays as animByte (locomotion)
		}
if model.PLSlotToRawPair != nil {
			// Resolve upper body from action slot (or loco if no action).
			if pair, ok := model.PLSlotToRawPair[upperSlot]; ok {
				upperIdx = pair[0]
			} else if pair, ok := model.PLSlotToRawPair[0]; ok {
				upperIdx = pair[0]
			}
			// Resolve lower body from locomotion slot.
			if pair, ok := model.PLSlotToRawPair[lowerSlot]; ok {
				lowerIdx = pair[1]
			} else if pair, ok := model.PLSlotToRawPair[0]; ok {
				lowerIdx = pair[1]
			} else {
				lowerIdx = 1
			}
		} else {
			// Legacy fallback for models without CPlayList.
			upperIdx = upperSlot * 2
			lowerIdx = lowerSlot*2 + 1
		}
		if isPlayer && DebugAnimUpper >= 0 {
			upperIdx = DebugAnimUpper
		}
		if isPlayer && DebugAnimLower >= 0 {
			lowerIdx = DebugAnimLower
		}
		if upperIdx < 0 || upperIdx >= len(anims) {
			upperIdx = 0
		}
		if lowerIdx >= len(anims) {
			lowerIdx = -1 // no lower half available
		}
	} else {
		// Non-humanoid: animByte from server is a CPlayList slot (0=idle, 1=walk, 3=run).
		// Try direct slot lookup first; fall back to F8 for combat overrides.
		resolved := false
		if model.PLSlotToRaw != nil {
			if rawIdx, ok := model.PLSlotToRaw[int(animByte)]; ok {
				upperIdx = rawIdx
				resolved = true
			}
		}
		if !resolved {
			upperIdx = resolveNHAnimIndex(model, int(animByte))
		}
		// Some creatures (72/80-anim models) have paired upper+lower animations
		// just like humanoids. Use PLSlotToRawPair to get the lower body index.
		// True single-body creatures (9/13 anims) have pair[1]=-1, so lowerIdx stays -1.
		lowerIdx = -1
		if resolved && model.PLSlotToRawPair != nil {
			if pair, ok := model.PLSlotToRawPair[int(animByte)]; ok && pair[1] >= 0 {
				lowerIdx = pair[1]
			}
		}
		if isPlayer && DebugAnimUpper >= 0 {
			upperIdx = DebugAnimUpper
		}
		if isPlayer && DebugAnimLower >= 0 {
			lowerIdx = DebugAnimLower
		}
		if upperIdx >= len(anims) {
			upperIdx = 0
		}
		if lowerIdx >= len(anims) {
			lowerIdx = -1
		}
	}

	// Handle animation transitions — save previous state for cross-fade blend.
	// PS2: SetAnimation calls AddInterpolatedPlay which stores blend duration,
	// then ProcessPlayTiming computes blendFactor = elapsed / blendDuration.
	isDeath := animByte == model.AnimProfile.Death
	upperChanged := as.UpperIdx != upperIdx
	lowerChanged := as.LowerIdx != lowerIdx
	if upperChanged || lowerChanged {
		// PS2 SetDeath(freezeAtLastFrame=1): when an entity is already dead on
		// first render, skip to corpse pose immediately (no blend, no animation).
		if isDeath && as.PrevUpperIdx == -1 && as.UpperIdx == -1 {
			as.UpperIdx = upperIdx
			as.LowerIdx = lowerIdx
			as.BlendFactor = 1.0 // no blend
			as.FramePos = 1e6    // clamped to last frame by death clamp below
			as.PrevFrame = 0
		} else if model.AnimProfile.F8Slots != nil {
			// Creature: instant snap (PS2: AddPlay, no blend).
			as.UpperIdx = upperIdx
			as.LowerIdx = lowerIdx
			as.BlendFactor = 1.0
			as.FramePos = 0
			as.PrevFrame = 0
		} else if upperChanged {
			as.playOnceDone = false
			if as.BlendFactor < 1.0 && as.PrevUpperIdx >= 0 {
				// Blend already in progress — keep prev, replace target.
			} else {
				// PS2 paired blend (AddInterpolatedPlayWithSync at 0x00446CC0):
				// When locomotion is set to the same anim as the action pair,
				// blend from the action pair's frame position for smooth transition.
				if as.Controller != nil && as.Controller.PairedBlendFromAction && actionAnimByte != 0 {
					// Use action pair's animation as blend source
					actionSlot := int(actionAnimByte)
					if model.PLSlotToRawPair != nil {
						if pair, ok := model.PLSlotToRawPair[actionSlot]; ok {
							as.PrevUpperIdx = pair[0]
							if pair[1] >= 0 {
								as.PrevLowerIdx = pair[1]
							} else {
								as.PrevLowerIdx = as.LowerIdx
							}
						}
					}
					as.PrevFramePos = as.FramePos // action pair was driving upper
					as.PrevNumFrames = as.NumFrames
					as.PrevSlot = actionSlot
					as.Controller.PairedBlendFromAction = false
				} else {
					as.PrevUpperIdx = as.UpperIdx
					as.PrevLowerIdx = as.LowerIdx
					as.PrevFramePos = as.FramePos
					as.PrevNumFrames = as.NumFrames
					as.PrevSlot = int(as.CurSlot)
				}
				as.BlendFactor = 0
			}
			as.BlendDur = 0.25
			as.UpperIdx = upperIdx
			as.LowerIdx = lowerIdx
			as.FramePos = 0
			as.PrevFrame = 0
		} else {
			// Only lower body changed (e.g. idle→walk during attack).
			// Keep upper body frame position — don't reset the attack.
			as.LowerIdx = lowerIdx
		}
	}
	as.CurSlot = int(animByte)
	as.ActionSlot = int(actionAnimByte)

	// PS2-accurate frame position advance (ProcessPlayList port).
	// PS2: fracTime += deltaTimeMs; frameSteps = int(fracTime * invPlayRate)
	// Equivalent: FramePos += dt * FPS * |speed| * callerSpeed
	upper := anims[upperIdx]
	if upper.NumFrames <= 0 {
		return
	}
	numFrames := float32(upper.NumFrames)
	fps := upper.FPS
	if fps <= 0 {
		return
	}
	as.NumFrames = numFrames
	as.FPS = fps

	// Determine which slot drives the upper body frame advance.
	// When split (actionAnimByte != 0), upper uses action slot speed.
	upperSlotByte := animByte
	if actionAnimByte != 0 {
		upperSlotByte = actionAnimByte
	}

	// PS2 dual speed: slot[0x1C] = CPlayList.Speed, sprite[432] = callerSpeed.
	animSpeed := float32(1.0)
	if model.PLSlotSpeed != nil {
		if spd, ok := model.PLSlotSpeed[int(upperSlotByte)]; ok && spd != 0 {
			animSpeed = spd
		}
	}
	if callerSpeed <= 0 {
		callerSpeed = 1.0
	}
	as.Speed = callerSpeed

	// Store previous frame position before advancing (for keyframe crossing detection).
	as.PrevFrame = as.FramePos

	// Advance upper body frame position.
	absSpeed := animSpeed
	if absSpeed < 0 {
		absSpeed = -absSpeed
	}
	as.FramePos += dt * fps * absSpeed * callerSpeed

	// Check play-once: ESF CPlayList flag, OR controller says action is play-once.
	// Controller's getPlayOnce() covers all combat/spell slots consistently across
	// all race models, while ESF PLSlotPlayOnce can vary per model.
	isPlayOnce := false
	if model.PLSlotPlayOnce != nil {
		isPlayOnce = model.PLSlotPlayOnce[int(upperSlotByte)]
	}
	if !isPlayOnce && as.Controller != nil && actionAnimByte != 0 {
		isPlayOnce = as.Controller.IsActionPlayOnce()
	}
	as.IsPlayOnce = isPlayOnce || isDeath
	as.IsDeath = isDeath

	if animByte == specialIdle {
		// Special idle plays once then returns to normal idle.
		if as.FramePos >= numFrames {
			as.FramePos = 0
			as.PrevFrame = 0
			as.IdleTimer = 0
		}
	} else if isDeath {
		// Death holds at last frame forever (PS2: playbackState=3, STOPPED).
		lastFrame := numFrames - 1
		if as.FramePos >= lastFrame {
			as.FramePos = lastFrame
		}
		if as.FramePos < 0 {
			as.FramePos = 0
		}
	} else if isPlayOnce {
		// Play-once: hold at last frame (PS2: playbackState=3).
		lastFrame := numFrames - 1
		if as.FramePos >= lastFrame {
			if !as.playOnceDone {
				as.playOnceDone = true
			}
			as.FramePos = lastFrame
			// Notify controller that the play-once animation has finished.
			if as.Controller != nil {
				as.Controller.MarkActionFinished()
			}
		} else {
			as.playOnceDone = false
		}
	} else {
		// Looping animation: wrap around (PS2: currentFrame %= numFrames).
		for as.FramePos >= numFrames {
			as.FramePos -= numFrames
		}
		for as.FramePos < 0 {
			as.FramePos += numFrames
		}
	}

	// PS2 ProcessPlayTiming: check if frame crossed any keyframe triggers.
	// Trip the corresponding trigger when PrevFrame < keyframe.Time <= FramePos.
	if model.AnimKeyframes != nil {
		kfs := model.AnimKeyframes[upperIdx]
		for _, kf := range kfs {
			if as.PrevFrame < kf.Time && as.FramePos >= kf.Time {
				if IsSpellFXTrigger(kf.RefID) {
					idx := SpellFXTriggerIndex(kf.RefID)
					if idx >= 0 && idx < 4 {
						as.SpellFXTripped[idx] = true
					}
				} else if IsSoundTrigger(kf.RefID) {
					ch := SoundChannel(kf.RefID)
					if ch >= 0 && ch < 2 {
						as.SoundTripped[ch] = true
					}
				}
			}
		}
	}

	// PS2 SetAnimation: reset sound triggers when animation changes.
	// Track previous upper index; on change, clear sound tripped flags.
	if as.UpperIdx != upperIdx {
		as.SoundTripped = [2]bool{}
	}

	// Advance lower body frame independently when split (PS2 dual-pair).
	// Lower body (locomotion) always loops.
	if actionAnimByte != 0 && lowerIdx >= 0 && lowerIdx < len(anims) {
		lowerAnim := anims[lowerIdx]
		lowerNumFrames := float32(lowerAnim.NumFrames)
		lowerFPS := lowerAnim.FPS
		lowerSpeed := float32(1.0)
		if model.PLSlotSpeed != nil {
			if spd, ok := model.PLSlotSpeed[int(animByte)]; ok && spd != 0 {
				lowerSpeed = spd
			}
		}
		if lowerSpeed < 0 {
			lowerSpeed = -lowerSpeed
		}
		as.LowerFramePos += dt * lowerFPS * lowerSpeed * callerSpeed
		as.LowerNumFrames = lowerNumFrames
		for as.LowerFramePos >= lowerNumFrames {
			as.LowerFramePos -= lowerNumFrames
		}
		for as.LowerFramePos < 0 {
			as.LowerFramePos += lowerNumFrames
		}
	} else {
		// No split — lower body tracks upper body.
		as.LowerFramePos = as.FramePos
		as.LowerNumFrames = numFrames
	}

	// Advance blend factor and previous animation frame during cross-fade.
	// PS2 ProcessPlayTiming: blendFactor = elapsed / blendDuration
	if as.BlendFactor < 1.0 {
		as.BlendFactor += dt / as.BlendDur
		if as.BlendFactor >= 1.0 {
			as.BlendFactor = 1.0
		}
		// Advance previous animation (don't freeze mid-stride).
		if as.PrevUpperIdx >= 0 && as.PrevUpperIdx < len(anims) {
			prevAnim := anims[as.PrevUpperIdx]
			prevFPS := prevAnim.FPS
			prevNumFrames := float32(prevAnim.NumFrames)
			prevSpeed := float32(1.0)
			if model.PLSlotSpeed != nil {
				if spd, ok := model.PLSlotSpeed[as.PrevSlot]; ok {
					prevSpeed = spd
				}
			}
			if prevSpeed < 0 {
				prevSpeed = -prevSpeed
			}
			if prevSpeed <= 0 {
				prevSpeed = 1.0
			}
			as.PrevFramePos += dt * prevFPS * prevSpeed * callerSpeed
			as.PrevNumFrames = prevNumFrames
			for as.PrevFramePos >= prevNumFrames {
				as.PrevFramePos -= prevNumFrames
			}
		}
	}

	// Sync blend state to controller for priority gating during cross-fade.
	if as.Controller != nil {
		blendActive := as.BlendFactor < 1.0
		as.Controller.SetBlendActive(PairLocomotion, blendActive)
		as.Controller.SetBlendActive(PairAction, blendActive)
		// Advance controller timers.
		as.Controller.Update(dt)
	}

	// Compute normalized time for skeleton interpolation.
	t := as.FramePos / numFrames
	// Lower body uses independent time when split.
	lowerT := t
	if actionAnimByte != 0 && as.LowerNumFrames > 0 {
		lowerT = as.LowerFramePos / as.LowerNumFrames
	}

	// PS2 per-bone animation LOD (ProcessPlayList channelFilter).
	// Skip detail bones for distant entities to reduce CPU skinning cost.
	// Channel 3=spine (always), 2=limbs (medium), 1=hands (close), 0=fingertips (never).
	if isPlayer {
		skel.ChannelFilter = 0 // full detail for player
	} else {
		dx := x - m.camX
		dy := y - m.camY
		dz := z - m.camZ
		distSq := dx*dx + dy*dy + dz*dz
		switch {
		case distSq > 150*150: // >150 units: spine only
			skel.ChannelFilter = 2
		case distSq > 60*60: // >60 units: skip hands/fingers
			skel.ChannelFilter = 1
		default:
			skel.ChannelFilter = 0 // full detail
		}
	}

	// Compute per-bone skinning matrices, with cross-fade blend if active.
	var lower *esf.HSpriteAnim
	if lowerIdx >= 0 {
		lower = anims[lowerIdx]
	}

	var matrices []mat4
	isSplit := actionAnimByte != 0 && lowerT != t
	if as.BlendFactor < 1.0 && as.PrevUpperIdx >= 0 {
		var prevUpper, prevLower *esf.HSpriteAnim
		if as.PrevUpperIdx < len(anims) {
			prevUpper = anims[as.PrevUpperIdx]
		}
		if as.PrevLowerIdx >= 0 && as.PrevLowerIdx < len(anims) {
			prevLower = anims[as.PrevLowerIdx]
		}
		prevT := float32(0)
		if prevUpper != nil && as.PrevNumFrames > 0 {
			prevT = as.PrevFramePos / as.PrevNumFrames
		}
		if isSplit {
			matrices = skel.ComputeAnimPoseLerp2BlendSplit(upper, lower, t, lowerT, prevUpper, prevLower, prevT, as.BlendFactor)
		} else {
			matrices = skel.ComputeAnimPoseLerp2Blend(upper, lower, t, prevUpper, prevLower, prevT, as.BlendFactor)
		}
	} else {
		if isSplit {
			matrices = skel.ComputeAnimPoseLerp2Split(upper, lower, t, lowerT)
		} else {
			matrices = skel.ComputeAnimPoseLerp2(upper, lower, t)
		}
	}

	// Store skinning matrices for bone resolver (spell effect attachment).
	model.lastSkinMatrices = matrices

	// Cache per-entity bone data so bone lookups survive past the draw call.
	// Without this, multiple entities sharing the same CharModel overwrite
	// each other's bone matrices, causing spell/weapon emitters to detach.
	if m.entityBones == nil {
		m.entityBones = make(map[uint32]entityBoneCache)
	}
	// Reuse existing slice if same size, otherwise allocate once per entity.
	existing := m.entityBones[entityID]
	if len(existing.skinMatrices) != len(matrices) {
		existing.skinMatrices = make([]mat4, len(matrices))
	}
	copy(existing.skinMatrices, matrices)
	existing.modelID = modelID
	m.entityBones[entityID] = existing

	// Extract animated bone world transforms for attachments (helm, weapons).
	// Skinning matrix = animWorld * invBindWorld, so animWorld = skinMatrix * bindWorld.
	// Bone matrices are in RAW model space (no CenterOff subtraction) — matching
	// PS2's boneWorldMatrix at node+0x30 from ProcessHierarchy.
	extractAnimBonePos := func(boneIdx int, out *[3]float32) {
		if boneIdx >= 0 && boneIdx < len(matrices) {
			aw := mulMat4(matrices[boneIdx], skel.BindWorld[boneIdx])
			out[0] = aw[3]
			out[1] = aw[7]
			out[2] = aw[11]
		}
	}
	extractAnimBoneMat := func(boneIdx int, out *mat4) {
		if boneIdx >= 0 && boneIdx < len(matrices) {
			*out = mulMat4(matrices[boneIdx], skel.BindWorld[boneIdx])
		}
	}
	extractAnimBonePos(model.HeadIdx, &model.AnimHead)
	extractAnimBoneMat(model.HeadIdx, &model.AnimHeadMat)
	extractAnimBoneMat(model.RightHandIdx, &model.AnimRHandMat)
	extractAnimBoneMat(model.LeftHandIdx, &model.AnimLHandMat)

	// F3 bone debug: print PS2-comparable bone data
	if m.ShowBoneDebug {
		m.boneLogCounter++
		if m.boneLogCounter%180 == 1 {
			log.Printf("=== Go Bones: Model=0x%08X DefaultScale=%.4f Race=%d ===", modelID, model.DefaultScale, model.Race)
			log.Printf("  CenterOff=(%.4f, %.4f, %.4f)", model.CenterOff[0], model.CenterOff[1], model.CenterOff[2])
			log.Printf("  StepBounce=%.4f (entity groundAdj=%.4f)", model.Skeleton.StepBounce, model.Skeleton.StepBounce*model.DefaultScale)
			boneNames := map[int]string{0: "root", 1: "spine1", 2: "spine2", 3: "chest", 4: "neck", 5: "head"}
			for _, bi := range []int{0, 1, 2, 3, 4, 5} {
				if bi >= len(skel.Nodes) {
					break
				}
				bone := skel.Nodes[bi]
				// Recover animWorld = skinMatrix * BindWorld
				var aw mat4
				if bi < len(matrices) {
					aw = mulMat4(matrices[bi], skel.BindWorld[bi])
				}
				name := boneNames[bi]
				// Our column-vector: translation in column 3 (indices 3,7,11)
				// PS2 row-vector: translation in row 3 (indices 12,13,14)
				log.Printf("  bone[%d] %-6s parent=%d", bi, name, bone.ParentID)
				log.Printf("    worldPos=(%.4f, %.4f, %.4f)", aw[3], aw[7], aw[11])
				log.Printf("    fixScale=(%.4f, %.4f, %.4f)  bindPos=(%.3f, %.3f, %.3f)",
					bone.FixScale[0], bone.FixScale[1], bone.FixScale[2],
					bone.Pos.X, bone.Pos.Y, bone.Pos.Z)
				if bi == 3 || bi == 4 || bi == 5 {
				// Recover animLocal = inv(parentWorld) * thisWorld
				parentIdx := int(bone.ParentID)
				if parentIdx >= 0 && parentIdx < len(matrices) {
					pw := mulMat4(matrices[parentIdx], skel.BindWorld[parentIdx])
					al := mulMat4(invertMat4(pw), aw)
					log.Printf("    animLocal pos=(%.6f, %.6f, %.6f) diag=(%.6f, %.6f, %.6f)",
						al[3], al[7], al[11], al[0], al[5], al[10])
				}
			}
			if bi == 5 {
					log.Printf("    worldMatrix (column-vector row-major):")
					log.Printf("      [%.6f, %.6f, %.6f, %.6f]", aw[0], aw[1], aw[2], aw[3])
					log.Printf("      [%.6f, %.6f, %.6f, %.6f]", aw[4], aw[5], aw[6], aw[7])
					log.Printf("      [%.6f, %.6f, %.6f, %.6f]", aw[8], aw[9], aw[10], aw[11])
					log.Printf("      [%.6f, %.6f, %.6f, %.6f]", aw[12], aw[13], aw[14], aw[15])
				}
			}
		}
	}

	// Cache hand matrices per-entity for weapon emitter lookups.
	if bc, ok := m.entityBones[entityID]; ok {
		bc.rHandMat = model.AnimRHandMat
		bc.lHandMat = model.AnimLHandMat
		m.entityBones[entityID] = bc
	}

	// Upload bone matrices for GPU skinning.
	// PS2 SetBlendMatrices: blendMat[i] = invBindPose * worldMatrix
	// Our skinning matrices = animWorld * invBindWorld (column-vector equivalent).
	// Vertices are in raw model space (no centering). CenterOff is handled by
	// the centerTransform in DrawEntity/drawHelm/DrawWeapon.
	if len(model.boneMatBuf) > 0 {
		for i := range matrices {
			if i >= len(model.boneMatBuf) {
				break
			}
			model.boneMatBuf[i] = *(*rl.Matrix)(unsafe.Pointer(&matrices[i]))
		}
	}
}

// LoadItemESF opens an ITEM.ESF file for weapon/item model loading.
func (m *ModelManager) LoadItemESF(path string) error {
	file, err := esf.Open(path)
	if err != nil {
		return err
	}
	return m.LoadItemESFFromFile(file)
}

// LoadItemESFFromFile sets a pre-opened ObjFile for weapon/item model loading.
func (m *ModelManager) LoadItemESFFromFile(file *esf.ObjFile) error {
	if err := file.BuildDictionary(); err != nil {
		return err
	}
	m.itemFile = file
	log.Printf("models: item ESF loaded (%d dictionary entries)", file.DictLen())
	return nil
}

// DrawWeapon renders a weapon model attached to an entity's hand position.
// charModelID is the entity's character model ID (used to look up hand bone positions).
// Returns false if the weapon model is unavailable or weaponID is 0.
func (m *ModelManager) DrawWeapon(weaponID int32, charModelID int32, x, y, z, facing float32, slot WeaponSlot) bool {
	if weaponID == 0 || m.itemFile == nil {
		return false
	}

	weaponModel, cached := m.itemCache[weaponID]
	if !cached {
		weaponModel = m.loadItemModel(weaponID)
		m.itemCache[weaponID] = weaponModel
	}
	if weaponModel == nil {
		return false
	}

	// Get hand bone transform from the character model's skeleton.
	// PS2 uses VIHSprite::CalcNodeTransform → full bone world matrix (rotation +
	// position + scale). Weapon models are in bone-local space (native ESF coords,
	// not centered), so the bone's world transform correctly orients them.
	charModel := m.cache[charModelID]

	sinF := float32(math.Sin(float64(facing)))
	cosF := float32(math.Cos(float64(facing)))

	// Same centerTransform as DrawEntity (including groundAdj for StepBounce).
	wScale := float32(1.0)
	if charModel != nil && charModel.DefaultScale > 0 {
		wScale = charModel.DefaultScale
	}
	groundAdj := float32(0)
	if charModel != nil && charModel.Skeleton != nil {
		groundAdj = charModel.Skeleton.IdleBounce * wScale
	}
	wcx, wcy, wcz := float32(0), float32(0), float32(0)
	if charModel != nil {
		wcx, wcy, wcz = charModel.CenterOff[0], charModel.CenterOff[1], charModel.CenterOff[2]
	}
	entityMat := centerTransformMat(sinF, cosF, wScale, wcx, wcy, wcz, x, y-groundAdj, z)

	// PS2 VIHSprite::Attach parents weapon to hand bone with NO orientation
	// correction. VIHSprite::Raster applies: childWorld = boneMat * parentWorld.
	// Weapon vertices are in bone-local space; the bone's animated world matrix
	// provides the full orientation (accumulated through the skeleton chain).

	var transform rl.Matrix
	if charModel != nil && charModel.Skeleton != nil && charModel.HasBones {
		// Full bone world matrix — weapon inherits bone rotation + position.
		var boneMat mat4
		switch slot {
		case WeaponSlotPrimary:
			boneMat = charModel.AnimRHandMat
		case WeaponSlotSecondary, WeaponSlotShield:
			boneMat = charModel.AnimLHandMat
		}
		// PS2 Attach applies no flip (confirmed via decompilation). Weapons
		// render correctly with just the bone matrix. Shields need an X mirror
		// to appear on the outside of the arm with the face outward — this
		// compensates for an OpenGL vs PS2 GS rendering convention difference
		// that only affects asymmetric flat objects like shields.
		if slot == WeaponSlotShield {
			cz := float32(math.Cos(-0.25))
			sz := float32(math.Sin(-0.25))
			boneMat = mulMat4(boneMat, mat4{
				cz, -sz, 0, 0,
				sz, cz, 0, 0,
				0, 0, 1, 0,
				0, 0, 0, 1,
			})
			boneMat = mulMat4(boneMat, mat4{
				1, 0, 0, 0,
				0, -1, 0, -0.09,
				0, 0, 1, 0,
				0, 0, 0, 1,
			})
		} else {
			boneMat = mulMat4(boneMat, mat4{
				1, 0, 0, -0.01,
				0, 1, 0, 0,
				0, 0, 1, -0.01,
				0, 0, 0, 1,
			})
		}
		final := mulMat4(entityMat, boneMat)
		// Convert row-major mat4 → column-major rl.Matrix.
		transform = rl.Matrix{
			M0: final[0], M4: final[1], M8: final[2], M12: final[3],
			M1: final[4], M5: final[5], M9: final[6], M13: final[7],
			M2: final[8], M6: final[9], M10: final[10], M14: final[11],
			M3: final[12], M7: final[13], M11: final[14], M15: final[15],
		}
	} else {
		// Fallback: position-only with entity facing.
		var handX, handY, handZ float32
		if charModel != nil && charModel.HasBones {
			var hand [3]float32
			switch slot {
			case WeaponSlotPrimary:
				hand = charModel.RightHand
			case WeaponSlotSecondary, WeaponSlotShield:
				hand = charModel.LeftHand
			}
			handX, handY, handZ = hand[0], hand[1], hand[2]
		} else {
			h := float32(1.8)
			if charModel != nil && charModel.Height > 0 {
				h = charModel.Height
			}
			handY = h * 0.6
			switch slot {
			case WeaponSlotPrimary:
				handX = h * 0.15
			case WeaponSlotSecondary, WeaponSlotShield:
				handX = -h * 0.15
			}
		}
		wx, wy, wz := transformPoint(entityMat, handX, handY, handZ)
		transform = rl.Matrix{
			M0: cosF * wScale, M4: 0, M8: sinF * wScale, M12: wx,
			M1: 0, M5: wScale, M9: 0, M13: wy,
			M2: -sinF * wScale, M6: 0, M10: cosF * wScale, M14: wz,
			M3: 0, M7: 0, M11: 0, M15: 1,
		}
	}

	rl.DisableBackfaceCulling()
	for i := range weaponModel.Groups {
		rl.DrawMesh(weaponModel.Groups[i].Mesh, weaponModel.Groups[i].Material, transform)
	}
	rl.EnableBackfaceCulling()
	return true
}

// drawHelm renders a helm 3D model on the character's head.
// Helm models are SimpleSprites from CHARCUST.CSF, loaded and cached on demand.
// PS2 SetHelm applies VIMaterial::SetLayerColor on every material layer in the
// helm's palette. We replicate this by setting colDiffuse to the tint color.
// bodyScale is the exact scale used by DrawEntity for the body (DefaultScale * server scale).
func (m *ModelManager) drawHelm(helmIdx byte, charModel *CharModel, x, y, z, facing, bodyScale float32, tintIdx byte) {
	helmModel, cached := m.helmCache[helmIdx]
	if !cached {
		helmModel = m.loadHelmModel(helmIdx)
		m.helmCache[helmIdx] = helmModel
	}
	if helmModel == nil {
		return
	}

	sinF := float32(math.Sin(float64(facing)))
	cosF := float32(math.Cos(float64(facing)))
	s := bodyScale
	groundAdj := float32(0)
	if charModel != nil && charModel.Skeleton != nil {
		groundAdj = charModel.Skeleton.IdleBounce * s
	}
	cx, cy, cz := float32(0), float32(0), float32(0)
	if charModel != nil {
		cx, cy, cz = charModel.CenterOff[0], charModel.CenterOff[1], charModel.CenterOff[2]
	}
	entityMat := centerTransformMat(sinF, cosF, s, cx, cy, cz, x, y-groundAdj, z)

	var transform rl.Matrix
	if charModel != nil && charModel.HeadIdx >= 0 && charModel.Skeleton != nil {
		// Raw bone world matrix — matches PS2 boneWorldMatrix from ProcessHierarchy.
		// No offset needed since root bone is no longer locked to bind pose.
		headBone := charModel.AnimHeadMat
		final := mulMat4(entityMat, headBone)
		transform = rl.Matrix{
			M0: final[0], M4: final[1], M8: final[2], M12: final[3],
			M1: final[4], M5: final[5], M9: final[6], M13: final[7],
			M2: final[8], M6: final[9], M10: final[10], M14: final[11],
			M3: final[12], M7: final[13], M11: final[14], M15: final[15],
		}
	} else {
		// Fallback: position-only using centerTransform at head height.
		headY := float32(1.8)
		if charModel != nil && charModel.HasHead {
			headY = charModel.HeadBone[1]
		} else if charModel != nil && charModel.Height > 0 {
			headY = charModel.Height
		}
		// Apply centerTransform to (0, headY, 0)
		wx, wy, wz := transformPoint(entityMat, 0, headY, 0)
		transform = rl.Matrix{
			M0: cosF * s, M4: 0, M8: sinF * s, M12: wx,
			M1: 0, M5: s, M9: 0, M13: wy,
			M2: -sinF * s, M6: 0, M10: cosF * s, M14: wz,
			M3: 0, M7: 0, M11: 0, M15: 1,
		}
	}

	// Apply tint color to helm materials (PS2 SetLayerColor on all materials/layers).
	tint := GetTintColor(tintIdx)
	tintColor := rl.NewColor(tint.R, tint.G, tint.B, tint.A)

	rl.DisableBackfaceCulling()
	for i := range helmModel.Groups {
		maps := (*[32]rl.MaterialMap)(unsafe.Pointer(helmModel.Groups[i].Material.Maps))
		origColor := maps[rl.MapDiffuse].Color
		maps[rl.MapDiffuse].Color = tintColor
		rl.DrawMesh(helmModel.Groups[i].Mesh, helmModel.Groups[i].Material, transform)
		maps[rl.MapDiffuse].Color = origColor
	}
	rl.EnableBackfaceCulling()
}

// loadHelmModel loads a helm 3D model from CHARCUST.CSF by helm index.
func (m *ModelManager) loadHelmModel(helmIdx byte) *CharModel {
	if m.custFile == nil {
		return nil
	}
	dictID := GetHelmModelDictID(helmIdx)
	if dictID == 0 {
		return nil
	}

	obj, err := m.custFile.FindObject(dictID)
	if err != nil || obj == nil {
		log.Printf("models: helm model %d (DictID=%d) not found in CHARCUST.CSF", helmIdx, dictID)
		return nil
	}

	sprite, ok := obj.(*esf.SimpleSprite)
	if !ok {
		log.Printf("models: helm model %d is not a SimpleSprite", helmIdx)
		return nil
	}

	pb, err := sprite.GetPrimBuffer(m.custFile)
	if err != nil || pb == nil {
		log.Printf("models: helm model %d has no PrimBuffer", helmIdx)
		return nil
	}

	mp := pb.MatPal
	if mp == nil {
		mp, _ = sprite.GetMatPal(m.custFile)
	}

	// Collect triangles.
	groups := make(map[int][]triVert) // material index → triangles
	matSurfaces := make(map[int]*esf.Surface)
	var totalTris int
	bbox := esf.NewBox()

	for _, vl := range pb.VertexLists {
		if len(vl.Vertices) < 3 {
			continue
		}
		if _, seen := matSurfaces[vl.Material]; !seen {
			matSurfaces[vl.Material] = resolveSurface(mp, vl.Material)
		}
		tris := destripVertices(vl.Vertices, nil)
		for i := range tris {
			bbox.Add(tris[i].X, tris[i].Y, tris[i].Z)
		}
		groups[vl.Material] = append(groups[vl.Material], tris...)
		totalTris += len(tris) / 3
	}

	if totalTris == 0 {
		log.Printf("models: helm model %d produced 0 triangles", helmIdx)
		return nil
	}

	model := &CharModel{}
	for matIdx, verts := range groups {
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

			positions[p3+0] = v.X // native bone-local coordinates
			positions[p3+1] = v.Y // negative = hangs from attach point
			positions[p3+2] = v.Z // native Z offset from bone

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
		mat.Shader = m.shader
		surf := matSurfaces[matIdx]
		tex := m.getTexture(surf)
		rl.SetMaterialTexture(&mat, rl.MapDiffuse, tex)

		model.Groups = append(model.Groups, meshGroup{
			Mesh:     mesh,
			Material: mat,
		})
	}

	dims := bbox.Dimensions()
	log.Printf("models: loaded helm %d: %d tris, %d groups, size=(%.3f, %.3f, %.3f) min=(%.3f,%.3f,%.3f) max=(%.3f,%.3f,%.3f)",
		helmIdx, totalTris, len(model.Groups), dims.X, dims.Y, dims.Z,
		bbox.MinX, bbox.MinY, bbox.MinZ, bbox.MaxX, bbox.MaxY, bbox.MaxZ)

	return model
}

// ModelHeight returns the height to use for nameplate positioning.
// Prefers head bone Y (top of head) when available, falls back to bbox height.
// Returns 0, false if the model hasn't been loaded yet.
func (m *ModelManager) ModelHeight(modelID int32) (float32, bool) {
	model, ok := m.cache[modelID]
	if !ok || model == nil {
		return 0, false
	}
	s := model.DefaultScale
	if model.HasHead {
		h := model.HeadBone[1]
		// Clamp head bone height to mesh height — some models (e.g. training
		// dummy) have skeleton bones far above the visible mesh, causing the
		// reticle/nameplate to float in the air.
		if h > model.Height {
			h = model.Height
		}
		return h * s, true
	}
	return model.Height * s, true
}

// GetCachedModel returns a cached model by ID (nil if not loaded).
func (m *ModelManager) GetCachedModel(modelID int32) (*CharModel, bool) {
	model, ok := m.cache[modelID]
	return model, ok
}

// GetAnimState returns the AnimState for an entity. Used by the sound system
// to read frame position without maintaining a duplicate tracker.
// SetCameraPos stores the camera position for per-bone animation LOD.
// Call once per frame before drawing entities.
func (m *ModelManager) SetCameraPos(x, y, z float32) {
	m.camX, m.camY, m.camZ = x, y, z
}

// SetWeaponTrail activates a weapon swing trail on an entity.
// PS2 SetWeaponTrail at 0x00422C60: stores trail ID + color on CSprite.
// ProcessWeaponTrails (0x004235C0) renders geometry from bone positions.
// We store the state for the render loop to draw trail lines.
func (m *ModelManager) SetWeaponTrail(entityID uint32, trailID int32, color [3]float32) {
	as := m.GetAnimState(entityID)
	if as == nil {
		return
	}
	as.TrailActive = true
	as.TrailID = trailID
	as.TrailColor = color
	as.TrailStart = time.Now()
	as.TrailPoints = as.TrailPoints[:0] // reset trail points
}

// RecordTrailPoint appends the current weapon tip position to the trail buffer.
// Called per-frame from the render loop after weapon bone transforms are computed.
func (m *ModelManager) RecordTrailPoint(entityID uint32, wx, wy, wz float32) {
	as := m.GetAnimState(entityID)
	if as == nil || !as.TrailActive {
		return
	}
	// Auto-expire trail after 600ms (PS2 trail duration is ~0.5-0.7s per swing).
	if time.Since(as.TrailStart) > 600*time.Millisecond {
		as.TrailActive = false
		as.TrailPoints = as.TrailPoints[:0]
		return
	}
	const maxTrailPts = 12
	as.TrailPoints = append(as.TrailPoints, [3]float32{wx, wy, wz})
	if len(as.TrailPoints) > maxTrailPts {
		as.TrailPoints = as.TrailPoints[len(as.TrailPoints)-maxTrailPts:]
	}
}

// GetTrailState returns the active trail state for rendering.
// Returns (points, color, active). Points are world-space weapon tip positions.
func (m *ModelManager) GetTrailState(entityID uint32) ([][3]float32, [3]float32, bool) {
	as := m.GetAnimState(entityID)
	if as == nil || !as.TrailActive || len(as.TrailPoints) < 2 {
		return nil, [3]float32{}, false
	}
	return as.TrailPoints, as.TrailColor, true
}

func (m *ModelManager) GetAnimState(entityID uint32) *AnimState {
	key := uint64(entityID)
	if as, ok := m.animStates[key]; ok {
		return as
	}
	return nil
}

// FindSpellFXTrigger finds a "Spell FX N" trigger index for an entity by name.
// PS2 FindTrigger at 0x004464E8: searches the HSprite trigger array for a matching name hash.
// Returns (triggerSlot 0-3, true) if the name matches "Spell FX 1-4", (-1, false) otherwise.
func (m *ModelManager) FindSpellFXTrigger(entityID uint32, triggerName string) (int, bool) {
	// Map trigger names to slot indices.
	switch triggerName {
	case "Spell FX 1":
		return 0, true
	case "Spell FX 2":
		return 1, true
	case "Spell FX 3":
		return 2, true
	case "Spell FX 4":
		return 3, true
	}
	return -1, false
}

// ResetSpellFXTrigger clears the tripped flag for a trigger slot on an entity.
// PS2 ResetTrigger at 0x00446550: sets triggerArray[idx*16+4] = 0.
func (m *ModelManager) ResetSpellFXTrigger(entityID uint32, triggerIdx int) {
	as := m.GetAnimState(entityID)
	if as != nil && triggerIdx >= 0 && triggerIdx < 4 {
		as.SpellFXTripped[triggerIdx] = false
	}
}

// HasSpellFXTripped returns true if the trigger at the given slot has fired.
// PS2 HasTripped at 0x00446538: returns triggerArray[idx*16+4] != 0.
func (m *ModelManager) HasSpellFXTripped(entityID uint32, triggerIdx int) bool {
	as := m.GetAnimState(entityID)
	if as != nil && triggerIdx >= 0 && triggerIdx < 4 {
		return as.SpellFXTripped[triggerIdx]
	}
	return false
}

// SetEntityController associates a SpriteAnimController with an entity's AnimState.
// Called by the animation manager to wire controller feedback (play-once completion,
// blend state) from applySkeletal back to the controller.
func (m *ModelManager) SetEntityController(entityID uint32, ctrl *SpriteAnimController) {
	key := uint64(entityID)
	as := m.animStates.getOrCreate(key)
	as.Controller = ctrl
}

// GetModelDefaultScale returns the ESF DefaultScale for a loaded model.
// Returns 1.0 if the model is not loaded or has no scale.
func (m *ModelManager) GetModelDefaultScale(modelID int32) float32 {
	model, ok := m.cache[modelID]
	if !ok || model == nil {
		return 1.0
	}
	return model.DefaultScale
}

// ModelIsHumanoid returns true if the model's ESF SkelType == 1 (dual-pair humanoid
// animation with upper/lower body split). Used for animation dispatch, weapon rendering,
// and equipment attachment decisions.
func (m *ModelManager) ModelIsHumanoid(modelID int32) bool {
	model, ok := m.cache[modelID]
	return ok && model != nil && model.IsHumanoid
}

// ModelRace returns the ESF Race field for a loaded model (PS2 VICSprite::GetRace).
// Returns -1 if the model is not loaded.
func (m *ModelManager) ModelRace(modelID int32) int32 {
	if model, ok := m.cache[modelID]; ok && model != nil {
		return model.Race
	}
	return -1
}

// ModelSkelType returns the SkelType for a loaded model (PS2 VICSprite+0x194).
// 0=simple, 1=humanoid dual-pair, 2=quadruped, 3=flying. Returns 0 if not loaded.
func (m *ModelManager) ModelSkelType(modelID int32) int32 {
	if model, ok := m.cache[modelID]; ok && model != nil {
		return model.SkelType
	}
	return 0
}

// WeaponSkelType returns the SkelType of a weapon model by its DictID.
// Checks both character and item model caches.
func (m *ModelManager) WeaponSkelType(weaponDictID uint32) int32 {
	id := int32(weaponDictID)
	if model, ok := m.itemCache[id]; ok && model != nil {
		return model.SkelType
	}
	if model, ok := m.cache[id]; ok && model != nil {
		return model.SkelType
	}
	return 0
}

// ModelAnimProfile returns the AnimProfile for a loaded model, or nil if not loaded.
func (m *ModelManager) ModelAnimProfile(modelID int32) *AnimProfile {
	if model, ok := m.cache[modelID]; ok && model != nil {
		return &model.AnimProfile
	}
	return nil
}

// ModelHasTSlotList returns true if the model has a TSlotList (player race with equipment).
// NPC humanoids (skeletons, undead) are IsHumanoid but !HasTSlotList.
func (m *ModelManager) ModelHasTSlotList(modelID int32) bool {
	model, ok := m.cache[modelID]
	return ok && model != nil && model.HasTSlotList
}

// ModelAnimCount returns the number of animations for a model (0 if not loaded).
func (m *ModelManager) ModelAnimCount(modelID int32) int {
	model, ok := m.cache[modelID]
	if !ok || model == nil {
		return 0
	}
	return len(model.Animations)
}

// AnimDuration returns the duration in seconds for an animation on the given model.
// animByte is a CPlayList slot number for humanoid models, or an F8 index for non-humanoid.
// Returns 0 if the model or animation is unavailable.
func (m *ModelManager) AnimDuration(modelID int32, animByte byte) float32 {
	model, ok := m.cache[modelID]
	if !ok || model == nil || len(model.Animations) == 0 {
		return 0
	}
	var idx int
	if model.AnimProfile.F8Slots == nil {
		// Paired animations: resolve CPlayList slot → upper body raw index.
		if model.PLSlotToRawPair != nil {
			if pair, ok := model.PLSlotToRawPair[int(animByte)]; ok {
				idx = pair[0]
			} else {
				return 0
			}
		} else {
			idx = int(animByte) * 2
		}
	} else {
		// Match applySkeletal: try PLSlotToRaw first, fall back to F8.
		resolved := false
		if model.PLSlotToRaw != nil {
			if rawIdx, ok := model.PLSlotToRaw[int(animByte)]; ok {
				idx = rawIdx
				resolved = true
			}
		}
		if !resolved {
			idx = resolveNHAnimIndex(model, int(animByte))
		}
	}
	if idx < 0 || idx >= len(model.Animations) {
		return 0
	}
	anim := model.Animations[idx]
	if anim.FPS <= 0 || anim.NumFrames <= 0 {
		return 0
	}
	// Use (numFrames-1)/FPS so duration matches the play-once clamp point
	// (start of last frame). Prevents freeze gap between animation end and timer expiry.
	dur := float32(anim.NumFrames-1) / anim.FPS
	// Use CPlayList Speed for this slot (authoritative per PS2 ProcessPlayList).
	speed := float32(1.0)
	if model.PLSlotSpeed != nil {
		if spd, ok := model.PLSlotSpeed[int(animByte)]; ok && spd != 0 {
			speed = spd
		}
	} else if anim.PlaySpeed != 0 {
		speed = anim.PlaySpeed
	}
	dur /= absf(speed)
	return dur
}

// getHairColorTex returns a small solid-color texture matching the given hair
// color index. Used for gnome mustache meshes whose vertex colors are white.
func (m *ModelManager) getHairColorTex(hairColor byte) rl.Texture2D {
	idx := int(hairColor)
	if idx >= len(m.hairColorTexCache) {
		idx = 0
	}
	if m.hairColorTexCache[idx].ID != 0 {
		return m.hairColorTexCache[idx]
	}
	r, g, b := GetHairColor(byte(idx))
	img := rl.GenImageColor(4, 4, rl.NewColor(r, g, b, 255))
	tex := rl.LoadTextureFromImage(img)
	rl.UnloadImage(img)
	m.hairColorTexCache[idx] = tex
	return tex
}

// Cleanup unloads all GPU resources.
// NOTE: Do NOT call rl.UnloadMaterial — all materials share m.shader and m.blendShader,
// which are unloaded once below. UnloadMaterial would double-free the shared shader.
func (m *ModelManager) Cleanup() {
	for _, model := range m.cache {
		if model == nil {
			continue
		}
		for i := range model.Groups {
			model.Groups[i].Mesh.BoneMatrices = nil // Go memory, don't RL_FREE
			rl.UnloadMesh(&model.Groups[i].Mesh)
		}
	}
	m.cache = nil

	for _, model := range m.itemCache {
		if model == nil {
			continue
		}
		for i := range model.Groups {
			rl.UnloadMesh(&model.Groups[i].Mesh)
		}
	}
	m.itemCache = nil

	for _, model := range m.helmCache {
		if model == nil {
			continue
		}
		for i := range model.Groups {
			rl.UnloadMesh(&model.Groups[i].Mesh)
		}
	}
	m.helmCache = nil

	for _, tex := range m.texCache {
		rl.UnloadTexture(tex)
	}
	m.texCache = nil

	rl.UnloadTexture(m.whiteTex)
	for i, tex := range m.hairColorTexCache {
		if tex.ID != 0 {
			rl.UnloadTexture(tex)
			m.hairColorTexCache[i] = rl.Texture2D{}
		}
	}
	rl.UnloadShader(m.shader)
	rl.UnloadShader(m.blendShader)
	rl.UnloadShader(m.clothingShader)
	rl.UnloadShader(m.skinShader)
	rl.UnloadShader(m.skinBlendShader)
	rl.UnloadShader(m.skinClothingShader)
	log.Printf("models: cleanup complete")
}

// getTexture returns a GPU texture for the given surface, loading and caching it.
// Character textures are forced to full opacity since PS2 uses 0-128 alpha range.
func (m *ModelManager) getTexture(surf *esf.Surface) rl.Texture2D {
	if surf == nil || surf.Image == nil {
		return m.whiteTex
	}
	key := texKey{surf.ObjInfo().DictID, texSrcDirect}
	if tex, ok := m.texCache[key]; ok {
		return tex
	}
	// Scale PS2 alpha range (0-128 where 128=opaque) to OpenGL (0-255).
	img := scalePS2Alpha(surf.Image)
	// Dilate all textures: fill alpha=0 padding with nearest valid pixel color,
	// preventing black fringe from bilinear filtering at UV seam edges.
	dilateTexture(img)
	rlImg := rl.NewImageFromImage(img)
	tex := rl.LoadTextureFromImage(rlImg)
	rl.UnloadImage(rlImg)
	rl.GenTextureMipmaps(&tex)
	rl.SetTextureFilter(tex, rl.FilterTrilinear)
	rl.SetTextureWrap(tex, rl.WrapRepeat)
	m.texCache[key] = tex
	return tex
}

// scalePS2Alpha scales PS2 alpha values (0-128 = transparent-opaque) to
// OpenGL range (0-255). Preserves transparent regions (alpha=0) while
// making opaque regions (alpha>=128) fully opaque.
func scalePS2Alpha(img *image.NRGBA) *image.NRGBA {
	bounds := img.Bounds()
	out := image.NewNRGBA(bounds)
	copy(out.Pix, img.Pix)
	for i := 3; i < len(out.Pix); i += 4 {
		a := int(out.Pix[i])
		a *= 2
		if a > 255 {
			a = 255
		}
		out.Pix[i] = uint8(a)
	}
	return out
}


// getEquipmentTexture loads a GPU texture by ESF Surface DictID.
//
// src selects which file to read from: texSrcAuto searches CHARCUST.CSF first
// (armor/hair/robe), then CHARFACE.CSF (faces), then CHAR.ESF; texSrcCharESF
// reads CHAR.ESF only. The distinction matters because the plate DictIDs exist
// in both CHARCUST.CSF and CHAR.ESF with different art.
//
// Returns the white fallback texture if the DictID is invalid or not found.
func (m *ModelManager) getEquipmentTexture(dictID int32, src texSource) rl.Texture2D {
	if dictID == 0 {
		return m.whiteTex
	}
	key := texKey{dictID, src}
	if tex, ok := m.texCache[key]; ok {
		return tex
	}
	search := []*esf.ObjFile{m.custFile, m.faceFile, m.file}
	if src == texSrcCharESF {
		search = []*esf.ObjFile{m.file}
	}
	var surf *esf.Surface
	for _, f := range search {
		if f == nil {
			continue
		}
		obj, err := f.FindObject(dictID)
		if err != nil || obj == nil {
			continue
		}
		if s, ok := obj.(*esf.Surface); ok && s != nil && s.Image != nil {
			surf = s
			break
		}
	}
	if surf == nil {
		return m.whiteTex
	}
	img := scalePS2Alpha(surf.Image)
	dilateTexture(img)
	rlImg := rl.NewImageFromImage(img)
	tex := rl.LoadTextureFromImage(rlImg)
	rl.UnloadImage(rlImg)
	rl.GenTextureMipmaps(&tex)
	rl.SetTextureFilter(tex, rl.FilterTrilinear)
	rl.SetTextureWrap(tex, rl.WrapRepeat)
	m.texCache[key] = tex
	return tex
}

// resolveSlotTexture returns the replacement DictID for a given TextureSlot and
// the file it must be read from. Handles armor (0-4), hair (5), face (6), and
// helm (7).
func (m *ModelManager) resolveSlotTexture(equip *EquipmentState, slot int) (int32, texSource) {
	switch {
	case slot >= TextSlotChest && slot <= TextSlotBoots:
		// A per-slot cosmetic override outranks both the robe and the armor set,
		// so picking NPC art for a slot is not silently undone by an equipped robe.
		if dictID := equip.slotOverride(slot); dictID != 0 {
			return dictID, texSrcCharESF
		}
		// Armor slots 0-4: robe overrides chest (0) and legs (3) unless hidden.
		if equip.Robe < 4 && !equip.HideRobe {
			if dictID := GetRobeTextureDictID(equip.Robe, slot); dictID != 0 {
				return dictID, texSrcAuto
			}
		}
		armorSet, _ := armorSetForSlot(equip, slot)
		dictID, fromCharESF := GetArmorTexture(armorSet, slot)
		if fromCharESF {
			return dictID, texSrcCharESF
		}
		return dictID, texSrcAuto
	case slot == 5: // hair
		// PS2 SetHair uses hairStyle (arg1) as the $Hair texture index (0-7).
		// Go's HairColor field (byte 105) = PS2's hairStyle parameter.
		// Visibility is handled by shouldDraw/hairSlotVisible, so all slots that
		// reach here are visible and should get the hair texture.
		return GetHairTextureDictID(equip.HairColor), texSrcAuto
	case slot == 7: // face — always show character face texture
		// Helm is rendered as a separate 3D model (not a texture swap on slot 7).
		// The head mesh always displays the face texture from CHARFACE.CSF.
		return GetFaceTexture(equip.Race, equip.FaceOption, equip.Sex), texSrcAuto
	default:
		return 0, texSrcAuto
	}
}

// armorSetForSlot returns the armor set byte and tint byte for a given TextureSlot
// from the equipment state. Returns (0, 0) if equip is nil or slot is unknown.
func armorSetForSlot(equip *EquipmentState, slot int) (armorSet, tint byte) {
	if equip == nil {
		return 0, 0
	}
	switch slot {
	case TextSlotChest:
		return equip.Chest, equip.ChestTint
	case TextSlotBracer:
		return equip.Bracer, equip.BracerTint
	case TextSlotGloves:
		return equip.Gloves, equip.GlovesTint
	case TextSlotLegs:
		return equip.Legs, equip.LegsTint
	case TextSlotBoots:
		return equip.Boots, equip.BootsTint
	}
	return 0, 0
}


