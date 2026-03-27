package sky

import (
	"image"
	"log"
	"math"
	"runtime"
	"unsafe"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/eqoa/client/internal/mathutil"
	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

// spriteData holds parsed ESF sprite data before GPU upload.
type spriteData struct {
	strips   []stripData
	surfaces map[int]*esf.Surface // material index → Layer 0 texture
	overlay  map[int]*esf.Surface // material index → Layer 1 texture (cloud detail)
	uvScale  map[int]float32      // material index → Layer 1 UV scale factor
}

// stripData holds one triangle strip from a PrimBuffer VertexList.
type stripData struct {
	vertices []esf.Vertex
	matIdx   int
}

// meshGroup holds a GPU mesh and its material.
type meshGroup struct {
	Mesh     rl.Mesh
	Material rl.Material
	HasAlpha bool
	Overlay  bool // true = Layer 1 overlay (cloud detail, rendered with additive blend)
}

// Sky loads and renders the sky dome from SKY.ESF.
type Sky struct {
	sprites  []spriteData
	groups   []meshGroup
	built    bool
	whiteTex rl.Texture2D
	texCache map[int32]rl.Texture2D
	scale    float32

	// Time-of-day modulation
	rotAngle    float32  // slow dome rotation for cloud drift (radians)
	baseTint    rl.Color // tint for base dome meshes
	overlayTint rl.Color // tint for cloud overlay

	// Celestial billboard textures (sun = Corona, moon, star)
	sunTex  rl.Texture2D
	moonTex rl.Texture2D
	starTex rl.Texture2D
	sunSurf  *esf.Surface // deferred GPU upload
	moonSurf *esf.Surface
	starSurf *esf.Surface
	hasSun  bool
	hasMoon bool
	hasStar bool

	// Procedural star field — pre-built GPU mesh (tiny quads on unit sphere)
	starPositions []rl.Vector3 // unit sphere positions (~200 points, upper hemisphere)
	starMesh      rl.Mesh      // single GPU mesh for all stars
	starMat       rl.Material  // white unlit material
	starBuilt     bool

	// Celestial positions (computed in Update)
	sunDir    rl.Vector3 // unit direction from center
	moonDir   rl.Vector3
	sunAlpha  uint8 // 0 at night, 255 during day
	moonAlpha uint8 // 255 at night, 0 during day
	starAlpha uint8 // 255 at night, fades at dawn/dusk

	// Weather-driven overcast (PS2 VIAtmosphere offset +0x61C).
	// Set by SetOvercast(). 0.0 = clear, 1.0 = fully overcast.
	// Darkens sky, reduces sun/moon/star alpha, boosts cloud overlay.
	overcastInterp float32
}

// NewSky opens SKY.ESF and loads all SimpleSprite geometry and textures.
// Returns nil if the file can't be loaded.
func NewSky(esfPath string) *Sky {
	file, err := esf.Open(esfPath)
	if err != nil {
		log.Printf("sky: failed to open %s: %v", esfPath, err)
		return nil
	}
	return NewSkyFromFile(file)
}

// NewSkyFromFile creates a Sky from a pre-opened ObjFile.
func NewSkyFromFile(file *esf.ObjFile) *Sky {
	if err := file.BuildDictionary(); err != nil {
		log.Printf("sky: BuildDictionary: %v", err)
		return nil
	}

	root, err := file.Root()
	if err != nil {
		log.Printf("sky: Root: %v", err)
		return nil
	}

	spriteInfos := root.ChildrenOfType(esf.TypeSimpleSprite)
	if len(spriteInfos) == 0 {
		log.Printf("sky: no SimpleSprites found")
		return nil
	}

	s := &Sky{
		scale:    5.0, // dome ~339 units → ~1700 radius
		texCache: make(map[int32]rl.Texture2D),
	}

	for _, info := range spriteInfos {
		obj, err := file.GetObject(info)
		if err != nil {
			continue
		}
		sprite, ok := obj.(*esf.SimpleSprite)
		if !ok {
			continue
		}

		pb, err := sprite.GetPrimBuffer(file)
		if err != nil || pb == nil {
			continue
		}

		// PS2 RenderSky: dome sprites have 400+ verts, billboards ≤16.
		// Capture billboard sprite textures for sun/moon rendering.
		totalVerts := 0
		for _, vl := range pb.VertexLists {
			totalVerts += len(vl.Vertices)
		}
		if totalVerts < 50 {
			// Billboard sprite — extract texture for sun/moon.
			mp := pb.MatPal
			if mp == nil {
				mp, _ = sprite.GetMatPal(file)
			}
			surf := resolveSurface(mp, 0) // Layer 0 texture
			if surf != nil && surf.Image != nil {
				if !s.hasSun && s.sunSurf == nil {
					s.sunSurf = surf
					log.Printf("sky: captured sun billboard dictID=0x%08x (%d verts)",
						info.DictID, totalVerts)
				} else if !s.hasMoon && s.moonSurf == nil {
					s.moonSurf = surf
					log.Printf("sky: captured moon billboard dictID=0x%08x (%d verts)",
						info.DictID, totalVerts)
				} else if !s.hasStar && s.starSurf == nil {
					s.starSurf = surf
					log.Printf("sky: captured star billboard dictID=0x%08x (%d verts)",
						info.DictID, totalVerts)
				} else {
					log.Printf("sky: skipping extra billboard sprite dictID=0x%08x (%d verts)",
						info.DictID, totalVerts)
				}
			}
			continue
		}

		mp := pb.MatPal
		if mp == nil {
			mp, _ = sprite.GetMatPal(file)
		}

		sd := spriteData{
			surfaces: make(map[int]*esf.Surface),
			overlay:  make(map[int]*esf.Surface),
			uvScale:  make(map[int]float32),
		}

		for _, vl := range pb.VertexLists {
			if len(vl.Vertices) < 3 {
				continue
			}
			sd.strips = append(sd.strips, stripData{
				vertices: vl.Vertices,
				matIdx:   vl.Material,
			})

			// Resolve Layer 0 texture.
			if _, seen := sd.surfaces[vl.Material]; !seen {
				sd.surfaces[vl.Material] = resolveSurface(mp, vl.Material)
			}

			// PS2 dome material: Layer 1 = cloud detail overlay with 3x UV tiling.
			// Resolve Layer 1 texture and UV scale if present.
			if _, seen := sd.overlay[vl.Material]; !seen {
				if surf, scale := resolveOverlay(mp, vl.Material); surf != nil {
					sd.overlay[vl.Material] = surf
					sd.uvScale[vl.Material] = scale
				}
			}
		}

		if len(sd.strips) > 0 {
			s.sprites = append(s.sprites, sd)
			log.Printf("sky: loaded dome sprite dictID=0x%08x with %d strips, %d verts",
				info.DictID, len(sd.strips), totalVerts)
		}
	}

	log.Printf("sky: loaded %d sprites", len(s.sprites))
	return s
}

// build uploads meshes to GPU. Called on first Draw after rl.InitWindow.
func (s *Sky) build() {
	if s.built {
		return
	}
	s.built = true

	// White fallback texture.
	whiteImg := rl.GenImageColor(4, 4, rl.White)
	s.whiteTex = rl.LoadTextureFromImage(whiteImg)
	rl.UnloadImage(whiteImg)

	// Upload all textures (Layer 0 + Layer 1 overlays).
	for _, sd := range s.sprites {
		for _, surf := range sd.surfaces {
			s.uploadSurface(surf)
		}
		for _, surf := range sd.overlay {
			s.uploadSurface(surf)
		}
	}

	// Build GPU meshes — one per strip (keeps material assignment simple).
	// Pass 1: Layer 0 (base sky texture).
	// Pass 2: Layer 1 overlay (cloud detail, 3x UV tiled, additive blend).
	for _, sd := range s.sprites {
		for _, strip := range sd.strips {
			tris := destripVertices(strip.vertices)
			if len(tris) == 0 {
				continue
			}

			// Layer 0: base mesh
			s.buildMeshGroup(&sd, strip.matIdx, tris, 1.0, false)

			// Layer 1: overlay mesh (if this material has a second layer)
			if overlaySurf := sd.overlay[strip.matIdx]; overlaySurf != nil {
				uvMul := sd.uvScale[strip.matIdx]
				if uvMul == 0 {
					uvMul = 1.0
				}
				s.buildOverlayGroup(overlaySurf, tris, uvMul)
			}
		}
	}

	// Upload celestial billboard textures directly (bypass cache to avoid DictID collisions).
	if s.sunSurf != nil && s.sunSurf.Image != nil {
		s.sunTex = s.uploadSurfaceDirect(s.sunSurf, "sun")
		s.hasSun = s.sunTex.ID > 0
	}
	if s.moonSurf != nil && s.moonSurf.Image != nil {
		s.moonTex = s.uploadSurfaceDirect(s.moonSurf, "moon")
		s.hasMoon = s.moonTex.ID > 0
	}
	if s.starSurf != nil && s.starSurf.Image != nil {
		s.starTex = s.uploadSurfaceDirect(s.starSurf, "star")
		s.hasStar = s.starTex.ID > 0
	}

	// Generate procedural star field and upload as a single GPU mesh.
	s.generateStars()
	s.buildStarMesh()

	log.Printf("sky: built %d GPU meshes, %d textures, sun=%v moon=%v stars=%d",
		len(s.groups), len(s.texCache), s.hasSun, s.hasMoon, len(s.starPositions))
}

// generateStars creates ~200 random points on the upper hemisphere using
// a golden spiral distribution for even coverage.
func (s *Sky) generateStars() {
	const numPoints = 200
	goldenRatio := (1.0 + math.Sqrt(5.0)) / 2.0
	s.starPositions = make([]rl.Vector3, 0, numPoints)
	for i := 0; i < numPoints; i++ {
		theta := 2.0 * math.Pi * float64(i) / goldenRatio
		phi := math.Acos(1.0 - 2.0*float64(i+1)/float64(numPoints+1))
		y := float32(math.Cos(phi))
		if y < 0.05 {
			continue // skip below horizon
		}
		s.starPositions = append(s.starPositions, rl.Vector3{
			X: float32(math.Sin(phi) * math.Cos(theta)),
			Y: y,
			Z: float32(math.Sin(phi) * math.Sin(theta)),
		})
	}
}

// SetOvercast sets the weather-driven overcast blend factor.
// PS2 VIAtmosphere offset +0x61C (GetOvercastInterp at 0x00418D48).
// 0.0 = clear sky, 1.0 = fully overcast (dark clouds, no sun/stars).
func (s *Sky) SetOvercast(interp float32) {
	if s == nil {
		return
	}
	if interp < 0 {
		interp = 0
	}
	if interp > 1 {
		interp = 1
	}
	s.overcastInterp = interp
}

// Update computes time-based dome rotation and time-of-day tints.
// dt is unused (kept for interface stability). gameHour = 0.0-24.0.
// PS2 VIAtmosphere::Update uses timeOfDay * π/12 for sun angle and a
// time-based offset for cloud drift — both are absolute, not accumulated.
func (s *Sky) Update(dt, gameHour float32) {
	if s == nil {
		return
	}
	// PS2: cloudAngle = timeOfDay * π/12 + π (180° offset from sun position).
	// One full rotation per 24 game hours = one per 45-minute real cycle.
	s.rotAngle = gameHour * (math.Pi / 12.0)
	s.baseTint = computeSkyTint(gameHour)
	s.overlayTint = computeOverlayTint(gameHour)

	// Sun position: rises east (6h), zenith south (12h), sets west (18h).
	// PS2 VIAtmosphere uses timeOfDay * π/12 for sun angle.
	sunAngle := float64(gameHour) * (math.Pi / 12.0)
	s.sunDir = rl.Vector3{
		X: float32(math.Sin(sunAngle)),  // east→west across sky
		Y: float32(-math.Cos(sunAngle)), // 0h=nadir(-1), 6h=horizon(0), 12h=zenith(+1)
		Z: 0.3,                          // slight offset so not directly overhead
	}
	// Normalize.
	mag := float32(math.Sqrt(float64(s.sunDir.X*s.sunDir.X + s.sunDir.Y*s.sunDir.Y + s.sunDir.Z*s.sunDir.Z)))
	s.sunDir.X /= mag
	s.sunDir.Y /= mag
	s.sunDir.Z /= mag

	// Moon: opposite side of sky from sun.
	s.moonDir = rl.Vector3{X: -s.sunDir.X, Y: -s.sunDir.Y, Z: -s.sunDir.Z}

	// Visibility alphas.
	s.sunAlpha = celestialAlpha(s.sunDir.Y)
	s.moonAlpha = celestialAlpha(s.moonDir.Y)
	s.starAlpha = starAlpha(gameHour)

	// PS2 ProcessTransitions: overcast reduces sun/moon/star visibility
	// and darkens the sky dome tint.
	if s.overcastInterp > 0 {
		oc := s.overcastInterp

		// PS2: sun alpha = 255 - overcastAlpha*2 (sprite 1 in palette animation)
		sunReduce := uint8(float32(s.sunAlpha) * oc)
		if sunReduce > s.sunAlpha {
			s.sunAlpha = 0
		} else {
			s.sunAlpha -= sunReduce
		}

		// Moon dims with overcast too
		moonReduce := uint8(float32(s.moonAlpha) * oc * 0.8)
		if moonReduce > s.moonAlpha {
			s.moonAlpha = 0
		} else {
			s.moonAlpha -= moonReduce
		}

		// PS2: stars invisible when overcastInterp <= 0 (RenderStars early exit)
		s.starAlpha = uint8(float32(s.starAlpha) * (1.0 - oc))

		// Darken base tint toward storm grey
		stormR, stormG, stormB := byte(60), byte(65), byte(80)
		s.baseTint.R = lerpByte(s.baseTint.R, stormR, oc)
		s.baseTint.G = lerpByte(s.baseTint.G, stormG, oc)
		s.baseTint.B = lerpByte(s.baseTint.B, stormB, oc)

		// Boost cloud overlay opacity during overcast
		boostA := uint8(float32(255-s.overlayTint.A) * oc * 0.6)
		if int(s.overlayTint.A)+int(boostA) > 255 {
			s.overlayTint.A = 255
		} else {
			s.overlayTint.A += boostA
		}
	}
}

// computeSkyTint returns the base dome color modulation for a game hour.
// Uses the PS2 24-keyframe sun color table (from VIScene+0x1FF4) to drive
// the sky dome tint, linearly interpolated between adjacent hour keyframes.
func computeSkyTint(h float32) rl.Color {
	if h < 0 {
		h = 0
	}
	if h >= 24 {
		h -= 24
	}

	idx := int(h)
	if idx >= 24 {
		idx = 23
	}
	next := (idx + 1) % 24
	t := h - float32(idx)

	a := &ps2SkyTable[idx]
	b := &ps2SkyTable[next]

	lerp := func(v0, v1 byte, f float32) byte {
		return byte(float32(v0) + float32(int(v1)-int(v0))*f)
	}

	return rl.Color{
		R: lerp(a.r, b.r, t),
		G: lerp(a.g, b.g, t),
		B: lerp(a.b, b.b, t),
		A: 255,
	}
}

// ps2SkyEntry holds PS2 sky dome tint colors per hour.
type ps2SkyEntry struct{ r, g, b byte }

// ps2SkyTable maps hour→sky dome color. Derived from PS2 sun color table:
// night=deep indigo, dawn/dusk=golden orange, day=warm white with slight blue.
// Values are the PS2 sun diffuse colors scaled for sky dome modulation.
var ps2SkyTable = [24]ps2SkyEntry{
	{30, 30, 60},    // 0: Midnight
	{30, 25, 65},    // 1: Night
	{30, 25, 70},    // 2: Night
	{35, 25, 75},    // 3: Night
	{45, 35, 70},    // 4: Pre-dawn purple
	{70, 50, 80},    // 5: Lavender dawn
	{200, 150, 90},  // 6: Golden sunrise
	{220, 190, 150}, // 7: Warm morning
	{200, 215, 235}, // 8: Warm white morning
	{190, 210, 240}, // 9: Day
	{185, 210, 245}, // 10: Day
	{180, 210, 245}, // 11: Midday
	{180, 210, 245}, // 12: Noon
	{180, 210, 245}, // 13: Afternoon
	{185, 210, 245}, // 14: Afternoon
	{190, 210, 240}, // 15: Afternoon
	{200, 215, 235}, // 16: Late afternoon
	{220, 180, 130}, // 17: Warm orange dusk
	{200, 150, 100}, // 18: Warm orange sunset
	{120, 100, 70},  // 19: Golden-green dusk
	{55, 40, 70},    // 20: Purple twilight
	{40, 35, 65},    // 21: Deep purple
	{35, 30, 65},    // 22: Dark blue-violet
	{30, 28, 62},    // 23: Night
}

// computeOverlayTint returns the cloud overlay tint. Kept dim since clouds use
// additive blend — high values wash out the sky to white.
func computeOverlayTint(h float32) rl.Color {
	var brightness byte
	switch {
	case h < 5:
		brightness = 15
	case h < 7:
		t := (h - 5) / 2
		brightness = lerpByte(15, 30, t)
	case h < 17:
		brightness = 30
	case h < 19:
		t := (h - 17) / 2
		brightness = lerpByte(30, 15, t)
	default:
		brightness = 15
	}
	return rl.Color{R: brightness, G: brightness, B: brightness, A: 255}
}

// lerpByte linearly interpolates between two byte values. t is clamped to [0,1].
func lerpByte(a, b byte, t float32) byte {
	if t <= 0 {
		return a
	}
	if t >= 1 {
		return b
	}
	v := float32(a) + (float32(b)-float32(a))*t
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return byte(v)
}

// celestialAlpha returns billboard alpha based on elevation above horizon.
// Fades in/out near horizon (Y ∈ [-0.1, 0.1]).
func celestialAlpha(elevY float32) uint8 {
	if elevY < -0.1 {
		return 0
	}
	if elevY > 0.1 {
		return 255
	}
	t := (elevY + 0.1) / 0.2 // 0..1
	return uint8(t * 255)
}

// starAlpha returns star visibility for a game hour.
// Full at night (20-4), fade out 4-6, fade in 18-20, zero during day.
func starAlpha(h float32) uint8 {
	switch {
	case h < 4:
		return 255
	case h < 6:
		return lerpByte(255, 0, (h-4)/2)
	case h < 18:
		return 0
	case h < 20:
		return lerpByte(0, 255, (h-18)/2)
	default:
		return 255
	}
}

// Draw renders the sky dome centered on the camera position.
// Must be called between BeginMode3D/EndMode3D, BEFORE terrain.
func (s *Sky) Draw(camera rl.Camera3D) {
	if s == nil || len(s.sprites) == 0 {
		return
	}
	s.build()

	camX := camera.Position.X
	camY := camera.Position.Y
	camZ := camera.Position.Z

	if len(s.groups) == 0 && !s.hasSun && !s.hasMoon && len(s.starPositions) == 0 {
		return
	}

	// Disable depth writes so sky never occludes terrain/models.
	rl.DisableDepthMask()

	// C-convention Y-rotation matrix for cloud drift (avoids MatrixRotateY bug).
	cosR := float32(math.Cos(float64(s.rotAngle)))
	sinR := float32(math.Sin(float64(s.rotAngle)))
	rotY := rl.Matrix{
		M0: cosR, M4: 0, M8: sinR, M12: 0,
		M1: 0, M5: 1, M9: 0, M13: 0,
		M2: -sinR, M6: 0, M10: cosR, M14: 0,
		M3: 0, M7: 0, M11: 0, M15: 1,
	}

	// Transform: rotate × scale × translate to camera position.
	transform := rl.MatrixMultiply(
		rl.MatrixMultiply(rotY, rl.MatrixScale(s.scale, s.scale, s.scale)),
		rl.MatrixTranslate(camX, camY, camZ),
	)

	// PS2 sky meshes have inconsistent winding — disable backface culling.
	rl.DisableBackfaceCulling()

	// Pass 1: Base dome meshes (Layer 0) with alpha blending + time-of-day tint.
	rl.BeginBlendMode(rl.BlendAlpha)
	for i := range s.groups {
		if !s.groups[i].Overlay {
			maps := (*[32]rl.MaterialMap)(unsafe.Pointer(s.groups[i].Material.Maps))
			origColor := maps[rl.MapDiffuse].Color
			maps[rl.MapDiffuse].Color = s.baseTint
			rl.DrawMesh(s.groups[i].Mesh, s.groups[i].Material, transform)
			maps[rl.MapDiffuse].Color = origColor
		}
	}
	rl.EndBlendMode()

	// Pass 2: Cloud overlay meshes (Layer 1) — additive blend on blue sky.
	rl.BeginBlendMode(rl.BlendAdditive)
	for i := range s.groups {
		if s.groups[i].Overlay {
			maps := (*[32]rl.MaterialMap)(unsafe.Pointer(s.groups[i].Material.Maps))
			origColor := maps[rl.MapDiffuse].Color
			maps[rl.MapDiffuse].Color = s.overlayTint
			rl.DrawMesh(s.groups[i].Mesh, s.groups[i].Material, transform)
			maps[rl.MapDiffuse].Color = origColor
		}
	}
	rl.EndBlendMode()

	// Pass 3: Stars + celestials on top of everything (additive glow).
	s.drawStars(camX, camY, camZ)
	s.drawCelestials(camera, camX, camY, camZ)

	rl.EnableBackfaceCulling()

	// Re-enable depth writes for terrain/models.
	rl.EnableDepthMask()
}

// buildStarMesh creates a single GPU mesh with all stars as tiny camera-facing quads
// on a unit sphere. Drawn with a single DrawMesh call + transform matrix.
func (s *Sky) buildStarMesh() {
	if s.starBuilt || len(s.starPositions) == 0 {
		return
	}
	s.starBuilt = true

	n := len(s.starPositions)
	// Each star = 2 triangles = 6 vertices (a tiny quad).
	vertCount := n * 6
	positions := make([]float32, vertCount*3)
	uvs := make([]float32, vertCount*2)
	colors := make([]uint8, vertCount*4)

	// Star size on unit sphere (will be scaled by transform).
	const sz = 0.0015

	// UV coords for a quad: 4 corners mapped to full texture
	quadUV := [4][2]float32{{0, 0}, {1, 0}, {1, 1}, {0, 1}}

	for i, sp := range s.starPositions {
		// Build a tiny quad centered on sp, oriented to face outward from origin.
		// tangent1 = normalize(cross(sp, up))  tangent2 = normalize(cross(sp, tangent1))
		var t1x, t1y, t1z float32
		if sp.Y > 0.99 || sp.Y < -0.99 {
			t1x, t1y, t1z = 0, -sp.Z, sp.Y
		} else {
			t1x, t1y, t1z = sp.Z, 0, -sp.X
		}
		mag := float32(math.Sqrt(float64(t1x*t1x + t1y*t1y + t1z*t1z)))
		if mag < 1e-6 {
			continue
		}
		t1x /= mag; t1y /= mag; t1z /= mag
		t2x := sp.Y*t1z - sp.Z*t1y
		t2y := sp.Z*t1x - sp.X*t1z
		t2z := sp.X*t1y - sp.Y*t1x

		cx, cy, cz := sp.X, sp.Y, sp.Z
		p := [4][3]float32{
			{cx - t1x*sz - t2x*sz, cy - t1y*sz - t2y*sz, cz - t1z*sz - t2z*sz},
			{cx + t1x*sz - t2x*sz, cy + t1y*sz - t2y*sz, cz + t1z*sz - t2z*sz},
			{cx + t1x*sz + t2x*sz, cy + t1y*sz + t2y*sz, cz + t1z*sz + t2z*sz},
			{cx - t1x*sz + t2x*sz, cy - t1y*sz + t2y*sz, cz - t1z*sz + t2z*sz},
		}

		// Two triangles: 0-1-2, 0-2-3
		base := i * 6
		for vi, idx := range [6]int{0, 1, 2, 0, 2, 3} {
			off3 := (base + vi) * 3
			off2 := (base + vi) * 2
			off4 := (base + vi) * 4
			positions[off3+0] = p[idx][0]
			positions[off3+1] = p[idx][1]
			positions[off3+2] = p[idx][2]
			uvs[off2+0] = quadUV[idx][0]
			uvs[off2+1] = quadUV[idx][1]
			colors[off4+0] = 255
			colors[off4+1] = 255
			colors[off4+2] = 240
			colors[off4+3] = 255
		}
	}

	mesh := rl.Mesh{
		VertexCount:   int32(vertCount),
		TriangleCount: int32(vertCount / 3),
		Vertices:      (*float32)(unsafe.Pointer(&positions[0])),
		Texcoords:     (*float32)(unsafe.Pointer(&uvs[0])),
		Colors:        (*uint8)(unsafe.Pointer(&colors[0])),
	}
	rl.UploadMesh(&mesh, false)
	runtime.KeepAlive(positions)
	runtime.KeepAlive(uvs)
	runtime.KeepAlive(colors)
	mesh.Vertices = nil
	mesh.Texcoords = nil
	mesh.Colors = nil
	s.starMesh = mesh

	// Use local variable for material — cgo rejects &s.starMat (Go heap pointer).
	mat := rl.LoadMaterialDefault()
	tex := s.whiteTex
	if s.hasStar {
		tex = s.starTex
	}
	rl.SetMaterialTexture(&mat, rl.MapDiffuse, tex)
	s.starMat = mat
}

// drawStars renders the pre-built star mesh with a single draw call.
func (s *Sky) drawStars(camX, camY, camZ float32) {
	if s.starAlpha == 0 || !s.starBuilt {
		return
	}
	radius := s.scale * 300.0
	maps := (*[32]rl.MaterialMap)(unsafe.Pointer(s.starMat.Maps))
	maps[rl.MapDiffuse].Color = rl.Color{R: 255, G: 255, B: 240, A: s.starAlpha}
	transform := rl.MatrixMultiply(
		rl.MatrixScale(radius, radius, radius),
		rl.MatrixTranslate(camX, camY, camZ),
	)
	rl.BeginBlendMode(rl.BlendAdditive)
	rl.DrawMesh(s.starMesh, s.starMat, transform)
	rl.EndBlendMode()
}

// drawCelestials renders sun and moon as camera-facing billboards.
func (s *Sky) drawCelestials(camera rl.Camera3D, camX, camY, camZ float32) {
	radius := s.scale * 280.0 // slightly inside star sphere

	// Sun billboard — alpha blend with corona texture.
	if s.hasSun && s.sunAlpha > 0 && s.sunDir.Y > -0.1 {
		sunPos := rl.Vector3{
			X: camX + s.sunDir.X*radius,
			Y: camY + s.sunDir.Y*radius,
			Z: camZ + s.sunDir.Z*radius,
		}
		sunSize := s.scale * 100.0
		src := rl.Rectangle{X: 0, Y: 0, Width: float32(s.sunTex.Width), Height: float32(s.sunTex.Height)}
		size := rl.Vector2{X: sunSize, Y: sunSize}
		origin := rl.Vector2{X: sunSize * 0.5, Y: sunSize * 0.5}

		// Additive glow pass (corona texture brightens sky).
		rl.BeginBlendMode(rl.BlendAdditive)
		sunClr := rl.Color{R: 255, G: 255, B: 100, A: s.sunAlpha}
		rl.DrawBillboardPro(camera, s.sunTex, src, sunPos, rl.Vector3{X: 0, Y: 1, Z: 0}, size, origin, 0, sunClr)
		rl.EndBlendMode()
	}

	// Moon billboard — alpha blend with moon texture.
	if s.hasMoon && s.moonAlpha > 0 && s.moonDir.Y > -0.1 {
		moonPos := rl.Vector3{
			X: camX + s.moonDir.X*radius,
			Y: camY + s.moonDir.Y*radius,
			Z: camZ + s.moonDir.Z*radius,
		}
		moonSize := s.scale * 50.0
		src := rl.Rectangle{X: 0, Y: 0, Width: float32(s.moonTex.Width), Height: float32(s.moonTex.Height)}
		size := rl.Vector2{X: moonSize, Y: moonSize}
		origin := rl.Vector2{X: moonSize * 0.5, Y: moonSize * 0.5}

		rl.BeginBlendMode(rl.BlendAlpha)
		moonClr := rl.Color{R: 220, G: 220, B: 255, A: s.moonAlpha}
		rl.DrawBillboardPro(camera, s.moonTex, src, moonPos, rl.Vector3{X: 0, Y: 1, Z: 0}, size, origin, 0, moonClr)
		rl.EndBlendMode()
	}
}

// Cleanup unloads all GPU resources.
func (s *Sky) Cleanup() {
	if s == nil {
		return
	}
	for i := range s.groups {
		rl.UnloadMesh(&s.groups[i].Mesh)
	}
	s.groups = nil

	if s.starBuilt {
		rl.UnloadMesh(&s.starMesh)
		s.starBuilt = false
	}

	for _, tex := range s.texCache {
		rl.UnloadTexture(tex)
	}
	s.texCache = nil

	rl.UnloadTexture(s.whiteTex)
	log.Printf("sky: cleanup complete")
}

// uploadSurfaceDirect uploads a surface texture to GPU unconditionally (no cache).
// Used for billboard textures that may share DictID with dome surfaces.
func (s *Sky) uploadSurfaceDirect(surf *esf.Surface, label string) rl.Texture2D {
	img := scalePS2Alpha(surf.Image)
	rlImg := rl.NewImageFromImage(img)
	tex := rl.LoadTextureFromImage(rlImg)
	rl.UnloadImage(rlImg)
	rl.GenTextureMipmaps(&tex)
	rl.SetTextureFilter(tex, rl.FilterTrilinear)
	rl.SetTextureWrap(tex, rl.WrapRepeat)
	log.Printf("sky: %s texture uploaded direct (%dx%d) glID=%d dictID=0x%08x",
		label, tex.Width, tex.Height, tex.ID, surf.ObjInfo().DictID)
	return tex
}

// uploadSurface loads a surface texture to GPU if not already cached.
func (s *Sky) uploadSurface(surf *esf.Surface) {
	if surf == nil || surf.Image == nil {
		return
	}
	texID := surf.ObjInfo().DictID
	if _, ok := s.texCache[texID]; ok {
		return
	}
	img := scalePS2Alpha(surf.Image)
	rlImg := rl.NewImageFromImage(img)
	tex := rl.LoadTextureFromImage(rlImg)
	rl.UnloadImage(rlImg)
	rl.GenTextureMipmaps(&tex)
	rl.SetTextureFilter(tex, rl.FilterTrilinear)
	rl.SetTextureWrap(tex, rl.WrapRepeat)
	s.texCache[texID] = tex
}

// buildMeshGroup creates a GPU mesh for Layer 0 (base sky texture).
func (s *Sky) buildMeshGroup(sd *spriteData, matIdx int, tris []esf.Vertex, uvMul float32, isOverlay bool) {
	n := len(tris)
	positions := make([]float32, n*3)
	uvs := make([]float32, n*2)
	colors := make([]uint8, n*4)

	for i, v := range tris {
		p3 := i * 3
		p2 := i * 2
		p4 := i * 4

		positions[p3+0] = v.X
		positions[p3+1] = v.Y
		positions[p3+2] = v.Z

		uvs[p2+0] = v.U * uvMul
		uvs[p2+1] = v.V * uvMul

		r, g, b := v.R, v.G, v.B
		if r == 0 && g == 0 && b == 0 {
			r, g, b = 1, 1, 1
		}
		colors[p4+0] = mathutil.FloatToUint8(r)
		colors[p4+1] = mathutil.FloatToUint8(g)
		colors[p4+2] = mathutil.FloatToUint8(b)
		colors[p4+3] = 255
	}

	mesh := rl.Mesh{
		VertexCount:   int32(n),
		TriangleCount: int32(n / 3),
		Vertices:      (*float32)(unsafe.Pointer(&positions[0])),
		Texcoords:     (*float32)(unsafe.Pointer(&uvs[0])),
		Colors:        (*uint8)(unsafe.Pointer(&colors[0])),
	}

	rl.UploadMesh(&mesh, false)
	runtime.KeepAlive(positions)
	runtime.KeepAlive(uvs)
	runtime.KeepAlive(colors)
	mesh.Vertices = nil
	mesh.Texcoords = nil
	mesh.Colors = nil

	mat := rl.LoadMaterialDefault()
	tex := s.whiteTex
	hasAlpha := false
	surf := sd.surfaces[matIdx]
	if surf != nil && surf.Image != nil {
		texID := surf.ObjInfo().DictID
		if t, ok := s.texCache[texID]; ok {
			tex = t
		}
		hasAlpha = surf.HasAlpha
	}
	rl.SetMaterialTexture(&mat, rl.MapDiffuse, tex)

	s.groups = append(s.groups, meshGroup{
		Mesh:     mesh,
		Material: mat,
		HasAlpha: hasAlpha,
	})
}

// buildOverlayGroup creates a GPU mesh for Layer 1 (cloud detail overlay).
// PS2: Layer 1 uses additive blend mode 3 with UV tiled by uvMul (typically 3x).
func (s *Sky) buildOverlayGroup(surf *esf.Surface, tris []esf.Vertex, uvMul float32) {
	n := len(tris)
	positions := make([]float32, n*3)
	uvs := make([]float32, n*2)
	colors := make([]uint8, n*4)

	for i, v := range tris {
		p3 := i * 3
		p2 := i * 2
		p4 := i * 4

		positions[p3+0] = v.X
		positions[p3+1] = v.Y
		positions[p3+2] = v.Z

		// Apply UV scale for tiled overlay (PS2 UVTransform[0] = 3.0)
		uvs[p2+0] = v.U * uvMul
		uvs[p2+1] = v.V * uvMul

		// Overlay uses white vertex colors
		colors[p4+0] = 255
		colors[p4+1] = 255
		colors[p4+2] = 255
		colors[p4+3] = 255
	}

	mesh := rl.Mesh{
		VertexCount:   int32(n),
		TriangleCount: int32(n / 3),
		Vertices:      (*float32)(unsafe.Pointer(&positions[0])),
		Texcoords:     (*float32)(unsafe.Pointer(&uvs[0])),
		Colors:        (*uint8)(unsafe.Pointer(&colors[0])),
	}

	rl.UploadMesh(&mesh, false)
	runtime.KeepAlive(positions)
	runtime.KeepAlive(uvs)
	runtime.KeepAlive(colors)
	mesh.Vertices = nil
	mesh.Texcoords = nil
	mesh.Colors = nil

	mat := rl.LoadMaterialDefault()
	tex := s.whiteTex
	if surf != nil && surf.Image != nil {
		texID := surf.ObjInfo().DictID
		if t, ok := s.texCache[texID]; ok {
			tex = t
		}
	}
	rl.SetMaterialTexture(&mat, rl.MapDiffuse, tex)

	s.groups = append(s.groups, meshGroup{
		Mesh:     mesh,
		Material: mat,
		HasAlpha: true, // overlay always uses alpha blending
		Overlay:  true, // mark for additive blend in Draw()
	})
}

// destripVertices converts a triangle strip to individual triangle vertices
// with swapped even/odd winding (PS2 CW → OpenGL CCW).
func destripVertices(strip []esf.Vertex) []esf.Vertex {
	numTris := len(strip) - 2
	if numTris <= 0 {
		return nil
	}

	out := make([]esf.Vertex, 0, numTris*3)
	odd := true // matches terrain/model pattern: starts true, first tri swaps

	for t := 0; t < numTris; t++ {
		var i0, i1, i2 int
		if odd {
			i0, i1, i2 = t, t+2, t+1
		} else {
			i0, i1, i2 = t, t+1, t+2
		}
		odd = !odd

		v0, v1, v2 := strip[i0], strip[i1], strip[i2]

		// Skip degenerate triangles.
		if v0.X == v1.X && v0.Y == v1.Y && v0.Z == v1.Z {
			continue
		}
		if v0.X == v2.X && v0.Y == v2.Y && v0.Z == v2.Z {
			continue
		}
		if v1.X == v2.X && v1.Y == v2.Y && v1.Z == v2.Z {
			continue
		}

		out = append(out, v0, v1, v2)
	}
	return out
}

// resolveSurface gets the Layer 0 texture Surface for a material index.
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

// resolveOverlay gets the Layer 1 texture Surface and UV scale for a material.
// PS2 dome material: Layer 1 = cloud detail overlay with UVTransform 3x scale
// and blend mode 3 (additive).
func resolveOverlay(mp *esf.MaterialPalette, matIdx int) (*esf.Surface, float32) {
	if mp == nil || matIdx < 0 || matIdx >= len(mp.Materials) {
		return nil, 0
	}
	mat := mp.Materials[matIdx]
	if len(mat.Layers) < 2 {
		return nil, 0
	}
	layer1 := mat.Layers[1]
	if layer1.Surface == nil {
		return nil, 0
	}
	// Extract UV scale from UVTransform[0] (the M00 element = U scale factor).
	scale := layer1.UVTransform[0]
	if scale == 0 {
		scale = 1.0
	}
	return layer1.Surface, scale
}

// scalePS2Alpha scales PS2 alpha values (0-128 = transparent-opaque) to
// OpenGL range (0-255).
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
