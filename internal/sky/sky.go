package sky

// Sky — a port of the sky half of the PS2's VIAtmosphere
// (decomp/src/viatmosphere.cpp), as SetResources finds it in SKY.ESF:
//
//   - RenderSky draws the "sky" dome at the camera, UNROTATED, with its own
//     material: layer 0 the sky texture scrolling at 0.009 a second, layer 1
//     the same texture tiled ×3, alpha-blended at 64 and scrolling at 0.05
//     (VIMaterial::Process). Every layer's colour is the sky colour
//     VIScene::SetupTimeOfDaySky computes. "EQOA_Skydome_360" is drawn only
//     on the Plane of Sky (SetPlaneOfSky), never alongside it.
//   - Then the Sun (8 units), the Corona (24, scaled by m_skyScale) and the
//     Moon (4), 35 units out on a circle in the XY plane, turning 15° an hour
//     (VIAtmosphere::Update), all additive (their materials are blend 1), and
//     only while the sun's alpha is not zero: an overcast sky hides them.
//   - RenderStars: 128 fixed stars out of the module's noise table, twinkling
//     by a 32-step table, scaled by the night factor m_p61C; a shooting star
//     every 10–25 s; and, while a flash lasts, the 14 lightning patches.
//   - During a weather transition ProcessTransitions rebuilds the dome
//     material: a third layer cross-fades the new sky texture in (Sunny, the
//     dome's own, or DarkClouds) while the cloud layer fades out, and then the
//     cloud layer comes back on the new texture.

import (
	"log"
	"math"
	"math/rand"
	"runtime"
	"unsafe"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

// The sky textures VIAtmosphere::SetResources keeps as m_skyTex[0..2]:
// PS2EQSky_Sunny, the dome's own layer-0 texture, PS2EQSky_DarkClouds.
const (
	SkyTexSunny   = 0
	SkyTexDefault = 1
	SkyTexDark    = 2
)

// Atmosphere is what the sky reads from VIAtmosphere and VIScene each frame.
type Atmosphere struct {
	// SkyColor is VIScene::SetupTimeOfDaySky's colour (0..1): the hour's sun
	// colour blended toward the overcast grey and tinted by the terrain
	// profiles. SetSkyColor gives it to every layer of the dome. Zero means
	// "not supplied" and leaves the texture as authored.
	SkyColor [3]float32
	// NightFactor is m_p61C: 1 at midnight, 0 from 6:00 to 18:00, faded with
	// the overcast. Stars show while it is above zero.
	NightFactor float32
	// SkyScale is m_skyScale, the corona's scale.
	SkyScale float32
	// SunAlpha is the alpha ProcessTransitions gives the sun's and moon's
	// materials, CoronaAlpha the corona's. Zero hides all three (m_overcast).
	SunAlpha, CoronaAlpha uint8
	// Transition is m_p608 as ProcessTransitions saw it this frame — 1 on
	// the frame a transition completes — and NextSky m_p5F8 (SkyTex*).
	Transition float32
	NextSky    int
	// Flash is GetLightning's m_pF48 and Patches the 14 lightning patches
	// (RecomputeLightningPatches): a direction ×100 and a half-size.
	Flash   float32
	Patches [14][4]float32
}

// DefaultAtmosphere is a clear sky at the given hour, for when no weather
// system drives the sky.
func DefaultAtmosphere(hour float32) Atmosphere {
	n := float32(0)
	switch {
	case hour < 6:
		n = 1 - hour/6
	case hour > 18:
		n = (hour - 18) / 6
	}
	return Atmosphere{NightFactor: n, SkyScale: 1, SunAlpha: 255, CoronaAlpha: 255, NextSky: SkyTexDefault}
}

// skyLayer is one VIMaterial layer of a dome.
type skyLayer struct {
	surf   *esf.Surface
	blend  int32      // VIAlphaBlendMode
	alpha  float32    // the layer colour's alpha, 0..255
	uvMul  [2]float32 // UVTransform's scale
	rate   [2]float32 // m_uvRate, per second
	offset [2]float32 // m_uvOffset (VIMaterial::Process)
}

// dome is one sky SimpleSprite: its triangles and its material's layers.
type dome struct {
	verts  []esf.Vertex
	layers []skyLayer
	mesh   rl.Mesh
	built  bool
}

// celestial is the Sun, the Corona or the Moon: a quad in the XZ plane.
type celestial struct {
	verts []esf.Vertex
	surf  *esf.Surface
	blend int32
	color [4]byte
	mesh  rl.Mesh
	ok    bool
}

// star is one of RenderStars' 128: a position 100 out (Init's RotateHPR of
// (0, 100, 0)), a half-size, and a twinkle phase (m_pE20).
type star struct {
	pos   rl.Vector3
	size  float32
	phase int
}

// Sky loads and renders the PS2 sky from SKY.ESF.
type Sky struct {
	domes      [2]*dome // m_p4E4: "sky", "EQOA_Skydome_360"
	planeOfSky bool     // m_p618
	skyTex     [3]*esf.Surface
	sun        celestial
	corona     celestial
	moon       celestial
	starSurf   *esf.Surface
	patchSurf  *esf.Surface

	stars   [128]star
	twinkle [32]float32 // m_pEA0
	starT   float32     // m_pF24
	shootIn float32     // m_pF28: seconds to the next shooting star
	shootT  float32     // m_pF2C: 0 none, else its progress
	shootA  rl.Vector3  // m_pF30
	shootB  rl.Vector3  // m_pF3C
	rng     *rand.Rand

	atm    Atmosphere
	atmSet bool
	hour   float32
	dt     float32
	lastT  float32 // the transition value last applied to the dome material
	texGPU map[*esf.Surface]rl.Texture2D
	white  rl.Texture2D
	shader rl.Shader
	locUV  int32
	locOff int32
	locCol int32
	mat    rl.Material // the dome's, on the sky shader
	celMat rl.Material // the sun, corona and moon's, on the default shader
	built  bool
}

// NewSky opens SKY.ESF. Returns nil if it cannot be loaded.
func NewSky(esfPath string) *Sky {
	file, err := esf.Open(esfPath)
	if err != nil {
		log.Printf("sky: failed to open %s: %v", esfPath, err)
		return nil
	}
	return NewSkyFromFile(file)
}

// NewSkyFromFile finds what VIAtmosphere::SetResources finds, by name.
func NewSkyFromFile(file *esf.ObjFile) *Sky {
	if err := file.BuildDictionary(); err != nil {
		log.Printf("sky: BuildDictionary: %v", err)
		return nil
	}
	find := func(name string) any {
		o, err := file.FindObject(esf.HashResourceID(name))
		if err != nil {
			return nil
		}
		return o
	}
	s := &Sky{texGPU: map[*esf.Surface]rl.Texture2D{}, rng: rand.New(rand.NewSource(1))}

	for i, name := range []string{"sky", "EQOA_Skydome_360"} {
		sp, _ := find(name).(*esf.SimpleSprite)
		if sp == nil {
			continue
		}
		s.domes[i] = loadDome(file, sp)
	}
	if s.domes[0] == nil {
		log.Printf("sky: no \"sky\" sprite")
		return nil
	}
	// SetResources: m_skyTex[1] is the dome's own layer-0 texture.
	if len(s.domes[0].layers) > 0 {
		s.skyTex[SkyTexDefault] = s.domes[0].layers[0].surf
	}
	s.skyTex[SkyTexSunny], _ = find("PS2EQSky_Sunny.tga").(*esf.Surface)
	s.skyTex[SkyTexDark], _ = find("PS2EQSky_DarkClouds.tga").(*esf.Surface)
	s.starSurf, _ = find("star.tga").(*esf.Surface)
	s.patchSurf, _ = find("lightning_patch.tga").(*esf.Surface)

	for _, c := range []struct {
		name string
		dst  *celestial
	}{{"Sun", &s.sun}, {"Corona", &s.corona}, {"Moon", &s.moon}} {
		if sp, _ := find(c.name).(*esf.SimpleSprite); sp != nil {
			*c.dst = loadCelestial(file, sp)
		}
	}

	s.initStars()
	s.atm = DefaultAtmosphere(10)
	log.Printf("sky: dome %d tris (%d layers), sun=%v corona=%v moon=%v, plane-of-sky dome=%v",
		len(s.domes[0].verts)/3, len(s.domes[0].layers), s.sun.ok, s.corona.ok, s.moon.ok, s.domes[1] != nil)
	return s
}

// loadDome reads a dome's triangles and its first material's layers.
func loadDome(file *esf.ObjFile, sp *esf.SimpleSprite) *dome {
	pb, err := sp.GetPrimBuffer(file)
	if err != nil || pb == nil {
		return nil
	}
	mp := pb.MatPal
	if mp == nil {
		mp, _ = sp.GetMatPal(file)
	}
	d := &dome{}
	for _, vl := range pb.VertexLists {
		d.verts = append(d.verts, destripVertices(vl.Vertices)...)
	}
	if mp != nil && len(mp.Materials) > 0 {
		for _, l := range mp.Materials[0].Layers {
			d.layers = append(d.layers, skyLayer{
				surf:  l.Surface,
				blend: l.BlendMode,
				alpha: float32(l.Color[3]),
				uvMul: [2]float32{nonZero(l.UVTransform[0]), nonZero(l.UVTransform[4])},
				rate:  [2]float32{l.URate, l.VRate},
			})
		}
	}
	return d
}

func nonZero(f float32) float32 {
	if f == 0 {
		return 1
	}
	return f
}

// loadCelestial reads one of the sun sprites: its quad and its material's
// first layer.
func loadCelestial(file *esf.ObjFile, sp *esf.SimpleSprite) celestial {
	pb, err := sp.GetPrimBuffer(file)
	if err != nil || pb == nil {
		return celestial{}
	}
	mp := pb.MatPal
	if mp == nil {
		mp, _ = sp.GetMatPal(file)
	}
	var c celestial
	for _, vl := range pb.VertexLists {
		c.verts = append(c.verts, destripVertices(vl.Vertices)...)
	}
	if mp != nil && len(mp.Materials) > 0 && len(mp.Materials[0].Layers) > 0 {
		l := mp.Materials[0].Layers[0]
		c.surf, c.blend, c.color = l.Surface, l.BlendMode, l.Color
	}
	c.ok = len(c.verts) > 0 && c.surf != nil
	return c
}

// rotateHPRRow1 is row 1 of VIMatrix44::RotateHPR (row vectors): the image
// of (0, 1, 0), which is where Init and RecomputeLightningPatches put a point
// 100 out.
func rotateHPRRow1(h, p, r float64) rl.Vector3 {
	sh, ch := math.Sincos(h)
	sp, cp := math.Sincos(p)
	sr, cr := math.Sincos(r)
	return rl.Vector3{
		X: float32(-sr*ch + cr*sp*sh),
		Y: float32(cr * cp),
		Z: float32(sr*sh + cr*sp*ch),
	}
}

// initStars is the star half of VIAtmosphere::Init. The positions come out
// of the module's noise table; the sizes and phases out of rand(), whose
// state at Init is not known, so a seeded generator stands in.
func (s *Sky) initStars() {
	for i := range s.stars {
		h := float64(128-int(starNoise[i*3+0])) / 128 * math.Pi * 2.3
		p := float64(128-int(starNoise[i*3+1])) / 128 * 0.49 * math.Pi
		r := float64(128-int(starNoise[i*3+2])) / 128 * 0.49 * math.Pi
		d := rotateHPRRow1(h, p, r)
		s.stars[i] = star{
			pos:   rl.Vector3{X: d.X * 100, Y: d.Y * 100, Z: d.Z * 100},
			size:  float32(s.rng.Intn(256))/255*0.5 + 0.3,
			phase: s.rng.Intn(32),
		}
	}
	for i := range s.twinkle {
		s.twinkle[i] = (float32(math.Sin(float64(i)/16*math.Pi))*0.5+0.5)*0.4 + 0.5
	}
}

// SetPlaneOfSky is VIAtmosphere::SetPlaneOfSky: the 360° dome instead of the
// sky, with no sun, moon or stars.
func (s *Sky) SetPlaneOfSky(on bool) {
	if s == nil || s.domes[1] == nil {
		return
	}
	s.planeOfSky = on
}

// SetOvercast is kept for callers that do not supply an Atmosphere; the
// overcast reaches the sky through SetAtmosphere's colour and alphas.
func (s *Sky) SetOvercast(interp float32) {}

// SetAtmosphere supplies this frame's VIAtmosphere and sky colour state.
func (s *Sky) SetAtmosphere(a Atmosphere) {
	if s == nil {
		return
	}
	s.atm = a
	s.atmSet = true
}

// Update advances the material scroll, the weather's dome cross-fade, the
// twinkle and the shooting star. gameHour = 0..24.
func (s *Sky) Update(dt, gameHour float32) {
	if s == nil {
		return
	}
	s.hour = gameHour
	s.dt = dt
	if !s.atmSet {
		s.atm = DefaultAtmosphere(gameHour)
	}
	d := s.activeDome()
	s.applyTransition(d)
	// VIMaterial::Process: offset += rate·dt, wrapped back into (-1, 1).
	for i := range d.layers {
		l := &d.layers[i]
		for k := 0; k < 2; k++ {
			l.offset[k] += l.rate[k] * dt
			if l.offset[k] > 1 || l.offset[k] < -1 {
				l.offset[k] -= float32(int(l.offset[k]))
			}
		}
	}
}

func (s *Sky) activeDome() *dome {
	if s.planeOfSky {
		return s.domes[1]
	}
	return s.domes[0]
}

// applyTransition is the dome-material half of ProcessTransitions. While a
// transition runs, the first half grows the material to three layers — the
// old base (now blend 3), a copy of it carrying the new texture fading in to
// 255, and the old cloud layer fading out from 64 — and the second half
// shrinks it back to two, the new base and the cloud layer on the new
// texture, fading in to 64. On the frame it completes, both layers take the
// new texture at 255 and 64.
func (s *Sky) applyTransition(d *dome) {
	t := s.atm.Transition
	if t == 0 || len(d.layers) < 2 {
		s.lastT = t
		return
	}
	next := s.skyTex[min(max(s.atm.NextSky, 0), 2)]
	if next == nil {
		next = s.skyTex[SkyTexDefault]
	}
	switch {
	case t < 0.5:
		d.layers[0].blend = 3
		if len(d.layers) != 3 {
			clouds := d.layers[1]
			base := d.layers[0]
			base.surf = next
			d.layers = []skyLayer{d.layers[0], base, clouds}
		}
		d.layers[1].alpha = float32(int(t * 2 * 255))
		d.layers[2].alpha = float32(int((0.5 - t) * 2 * 64))
	case t < 1:
		if len(d.layers) != 2 {
			d.layers = []skyLayer{d.layers[1], d.layers[2]}
			d.layers[1].surf = next
		}
		d.layers[1].alpha = float32(int((t - 0.5) * 2 * 64))
	}
	if t == 1 {
		if len(d.layers) == 3 {
			d.layers = []skyLayer{d.layers[1], d.layers[2]}
		}
		d.layers[0].surf = next
		d.layers[1].surf = next
		d.layers[0].alpha = 255
		d.layers[1].alpha = 64
	}
	s.lastT = t
}

// build uploads to the GPU; called on the first Draw.
func (s *Sky) build() {
	if s.built {
		return
	}
	s.built = true
	img := rl.GenImageColor(4, 4, rl.White)
	s.white = rl.LoadTextureFromImage(img)
	rl.UnloadImage(img)

	s.shader = rl.LoadShaderFromMemory(skyVS, skyFS)
	s.locUV = rl.GetShaderLocation(s.shader, "uvScale")
	s.locOff = rl.GetShaderLocation(s.shader, "uvOffset")
	s.locCol = rl.GetShaderLocation(s.shader, "layerColor")
	s.mat = rl.LoadMaterialDefault()
	s.mat.Shader = s.shader
	s.celMat = rl.LoadMaterialDefault()

	for _, d := range s.domes {
		if d != nil && len(d.verts) > 0 {
			d.mesh = uploadMesh(d.verts)
			d.built = true
		}
	}
	for _, c := range []*celestial{&s.sun, &s.corona, &s.moon} {
		if c.ok {
			c.mesh = uploadMesh(c.verts)
		}
	}
}

// texture uploads a surface once.
func (s *Sky) texture(surf *esf.Surface) rl.Texture2D {
	if surf == nil || surf.Image == nil {
		return s.white
	}
	if t, ok := s.texGPU[surf]; ok {
		return t
	}
	img := rl.NewImageFromImage(surf.Image)
	t := rl.LoadTextureFromImage(img)
	rl.UnloadImage(img)
	rl.GenTextureMipmaps(&t)
	rl.SetTextureFilter(t, rl.FilterTrilinear)
	rl.SetTextureWrap(t, rl.WrapRepeat)
	s.texGPU[surf] = t
	return t
}

// beginBlend sets the GS ALPHA a VIMaterial blend mode selects
// (VIMaterial::ConstructPass): 0 opaque (Cs), 1 Cs·As + Cd, 2 Cd − Cs·As,
// 3 the alpha blend, 4 Cd·As.
func beginBlend(mode int32) {
	const (
		glZero, glOne              = 0, 1
		glSrcAlpha, glOneMinusSrcA = 0x0302, 0x0303
		glAdd, glRevSub            = 0x8006, 0x800B
	)
	switch mode {
	case 1:
		rl.SetBlendFactors(glSrcAlpha, glOne, glAdd)
	case 2:
		rl.SetBlendFactors(glSrcAlpha, glOne, glRevSub)
	case 3:
		rl.SetBlendFactors(glSrcAlpha, glOneMinusSrcA, glAdd)
	case 4:
		rl.SetBlendFactors(glZero, glSrcAlpha, glAdd)
	default:
		rl.SetBlendFactors(glOne, glZero, glAdd)
	}
	rl.BeginBlendMode(rl.BlendCustom)
}

// Draw renders RenderSky and RenderStars around the camera. Call between
// BeginMode3D/EndMode3D, before the terrain: nothing here tests or writes
// depth, so everything drawn after covers it.
func (s *Sky) Draw(camera rl.Camera3D) {
	if s == nil {
		return
	}
	s.build()
	cam := camera.Position

	rl.DrawRenderBatchActive()
	rl.DisableDepthMask()
	rl.DisableDepthTest()
	rl.DisableBackfaceCulling()

	// The dome: the camera's view with its translation zeroed, i.e. centred
	// on the camera and not rotated.
	d := s.activeDome()
	if d != nil && d.built {
		col := s.atm.SkyColor
		if col == ([3]float32{}) {
			col = [3]float32{1, 1, 1}
		}
		at := rl.MatrixTranslate(cam.X, cam.Y, cam.Z)
		for _, l := range d.layers {
			setMap(s.mat, s.texture(l.surf), rl.White)
			uv := l.uvMul
			off := l.offset
			c := [4]float32{clamp01(col[0]), clamp01(col[1]), clamp01(col[2]), l.alpha / 255}
			rl.SetShaderValue(s.shader, s.locUV, uv[:], rl.ShaderUniformVec2)
			rl.SetShaderValue(s.shader, s.locOff, off[:], rl.ShaderUniformVec2)
			rl.SetShaderValue(s.shader, s.locCol, c[:], rl.ShaderUniformVec4)
			beginBlend(l.blend)
			rl.DrawMesh(d.mesh, s.mat, at)
			rl.EndBlendMode()
		}
	}

	if !s.planeOfSky {
		if s.atm.SunAlpha != 0 {
			s.drawCelestials(cam)
		}
		s.drawStars(camera)
	}

	rl.EnableBackfaceCulling()
	rl.EnableDepthTest()
	rl.EnableDepthMask()
}

func clamp01(f float32) float32 { return min(max(f, 0), 1) }

// drawCelestials draws the Sun on m_skyRot, the Corona on m_coronaRot
// (m_skyRot after a scale by m_skyScale) and the Moon on m_moonRot
// (VIAtmosphere::Update): a roll of hour·15° about z, and 35 units out along
// (sin w, -cos w, 0) — below the horizon at midnight, overhead at noon, at +X
// at 6:00, where the terrain's sun comes from.
func (s *Sky) drawCelestials(cam rl.Vector3) {
	w := float64(s.hour) * 0.2617993
	sw, cw := math.Sincos(w)
	sun := rowRoll(sw, cw, 1, float32(sw)*35, float32(-cw)*35, cam)
	corona := rowRoll(sw, cw, s.atm.SkyScale, float32(sw)*35, float32(-cw)*35, cam)
	// m_moonRot: the roll plus pi, at minus the sun's translation.
	moon := rowRoll(-sw, -cw, 1, float32(-sw)*35, float32(cw)*35, cam)

	for _, c := range []struct {
		cel   *celestial
		m     rl.Matrix
		alpha uint8
	}{
		{&s.sun, sun, s.atm.SunAlpha},
		{&s.corona, corona, s.atm.CoronaAlpha},
		{&s.moon, moon, s.atm.SunAlpha},
	} {
		if !c.cel.ok || c.alpha == 0 {
			continue
		}
		setMap(s.celMat, s.texture(c.cel.surf), rl.Color{R: c.cel.color[0], G: c.cel.color[1], B: c.cel.color[2], A: c.alpha})
		beginBlend(c.cel.blend)
		rl.DrawMesh(c.cel.mesh, s.celMat, c.m)
		rl.EndBlendMode()
	}
}

// rowRoll is the raylib (column-vector) form of a PS2 row-vector matrix
// Scale(k) × RotateHPR(0, 0, w) with translation (tx, ty, 0), moved to the
// camera: x' = k(x·cos w − y·sin w), y' = k(x·sin w + y·cos w), z' = k·z.
func rowRoll(sw, cw float64, k, tx, ty float32, cam rl.Vector3) rl.Matrix {
	s, c := float32(sw)*k, float32(cw)*k
	return rl.Matrix{
		M0: c, M4: -s, M8: 0, M12: cam.X + tx,
		M1: s, M5: c, M9: 0, M13: cam.Y + ty,
		M2: 0, M6: 0, M10: k, M14: cam.Z,
		M15: 1,
	}
}

// drawStars is VIAtmosphere::RenderStars.
func (s *Sky) drawStars(camera rl.Camera3D) {
	cam := camera.Position
	right, up := billboardVectors(camera)
	dt := s.dt

	if night := s.atm.NightFactor; night > 0 && s.starSurf != nil {
		tex := s.texture(s.starSurf)
		beginBlend(1)
		rl.SetTexture(tex.ID)
		rl.Begin(rl.Quads)

		// The shooting star: every 10..25 s, a 0.5 billboard falling from the
		// top of the sky to the horizon over five seconds.
		if s.shootIn <= 0 {
			if s.shootT == 0 {
				a := float64(s.rand255()) * math.Pi * 3.9
				s.shootA = rl.Vector3{X: float32(math.Sin(a)) * 100, Y: 100, Z: float32(math.Cos(a)) * 100}
				a = float64(s.rand255()) * math.Pi * 3.9
				s.shootB = rl.Vector3{X: float32(math.Sin(a)) * 100, Y: 0, Z: float32(math.Cos(a)) * 100}
				s.shootT = 1e-5
			}
			if s.shootT > 0 {
				s.shootT += dt * 0.2
				b := (s.rng.Float32()-s.rng.Float32())*0.3 + 0.7
				if s.shootT < 0.2 {
					b *= s.shootT * 5
				} else {
					b *= 1 - (s.shootT-0.2)*1.25
				}
				b = clamp01(b)
				p := rl.Vector3Lerp(s.shootA, s.shootB, s.shootT)
				starQuad(cam, p, right, up, 0.5, b)
				if s.shootT >= 1 {
					s.shootT = 0
					s.shootIn = float32(s.rng.Intn(16) + 10)
				}
			}
		} else {
			s.shootIn -= dt
		}

		idx := int(int8(int(s.starT)))
		s.starT += dt * 15
		if s.starT > 32 {
			s.starT -= 32
		}
		for _, st := range s.stars {
			b := s.twinkle[(idx+st.phase)&31] * night
			starQuad(cam, st.pos, right, up, st.size, b)
		}
		rl.End()
		rl.SetTexture(0)
		rl.EndBlendMode()
	}

	// The lightning patches, while a flash lasts: min(flash·0.45, 0.15).
	if s.atm.Flash > 0 && s.patchSurf != nil {
		b := min(s.atm.Flash*0.45, 0.15)
		beginBlend(1)
		rl.SetTexture(s.texture(s.patchSurf).ID)
		rl.Begin(rl.Quads)
		for _, p := range s.atm.Patches {
			starQuad(cam, rl.Vector3{X: p[0], Y: p[1], Z: p[2]}, right, up, p[3], b)
		}
		rl.End()
		rl.SetTexture(0)
		rl.EndBlendMode()
	}
}

func (s *Sky) rand255() float32 { return float32(s.rng.Intn(256)) / 255 }

// starQuad emits one camera-facing billboard of half-size h at cam + p,
// coloured (b, b, b, 1).
func starQuad(cam, p, right, up rl.Vector3, h, b float32) {
	x, y, z := cam.X+p.X, cam.Y+p.Y, cam.Z+p.Z
	rx, ry, rz := right.X*h, right.Y*h, right.Z*h
	ux, uy, uz := up.X*h, up.Y*h, up.Z*h
	c := uint8(clamp01(b) * 255)
	rl.Color4ub(c, c, c, 255)
	rl.TexCoord2f(0, 1)
	rl.Vertex3f(x-rx-ux, y-ry-uy, z-rz-uz)
	rl.TexCoord2f(1, 1)
	rl.Vertex3f(x+rx-ux, y+ry-uy, z+rz-uz)
	rl.TexCoord2f(1, 0)
	rl.Vertex3f(x+rx+ux, y+ry+uy, z+rz+uz)
	rl.TexCoord2f(0, 0)
	rl.Vertex3f(x-rx+ux, y-ry+uy, z-rz+uz)
}

// billboardVectors returns the camera's right and up.
func billboardVectors(cam rl.Camera3D) (right, up rl.Vector3) {
	fwd := rl.Vector3Normalize(rl.Vector3Subtract(cam.Target, cam.Position))
	right = rl.Vector3Normalize(rl.Vector3CrossProduct(fwd, cam.Up))
	up = rl.Vector3CrossProduct(right, fwd)
	return right, up
}

// setMap sets a material's diffuse texture and colour.
func setMap(mat rl.Material, tex rl.Texture2D, col rl.Color) {
	maps := (*[32]rl.MaterialMap)(unsafe.Pointer(mat.Maps))
	maps[rl.MapDiffuse].Texture = tex
	maps[rl.MapDiffuse].Color = col
}

// uploadMesh uploads a triangle list with its texcoords and colours.
func uploadMesh(tris []esf.Vertex) rl.Mesh {
	n := len(tris)
	positions := make([]float32, n*3)
	uvs := make([]float32, n*2)
	colors := make([]uint8, n*4)
	for i, v := range tris {
		positions[i*3+0], positions[i*3+1], positions[i*3+2] = v.X, v.Y, v.Z
		uvs[i*2+0], uvs[i*2+1] = v.U, v.V
		colors[i*4+0], colors[i*4+1], colors[i*4+2], colors[i*4+3] = 255, 255, 255, 255
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
	return mesh
}

// Cleanup unloads all GPU resources.
func (s *Sky) Cleanup() {
	if s == nil || !s.built {
		return
	}
	for _, d := range s.domes {
		if d != nil && d.built {
			rl.UnloadMesh(&d.mesh)
			d.built = false
		}
	}
	for _, c := range []*celestial{&s.sun, &s.corona, &s.moon} {
		if c.ok {
			rl.UnloadMesh(&c.mesh)
		}
	}
	for _, t := range s.texGPU {
		rl.UnloadTexture(t)
	}
	s.texGPU = nil
	rl.UnloadTexture(s.white)
	rl.UnloadShader(s.shader)
	s.built = false
	log.Printf("sky: cleanup complete")
}

// destripVertices converts a triangle strip to a triangle list, skipping
// degenerate triangles. The sky draws with culling off, so winding does not
// matter.
func destripVertices(strip []esf.Vertex) []esf.Vertex {
	if len(strip) < 3 {
		return nil
	}
	out := make([]esf.Vertex, 0, (len(strip)-2)*3)
	for t := 0; t+2 < len(strip); t++ {
		v0, v1, v2 := strip[t], strip[t+1], strip[t+2]
		if t%2 == 0 {
			v1, v2 = v2, v1
		}
		if same(v0, v1) || same(v0, v2) || same(v1, v2) {
			continue
		}
		out = append(out, v0, v1, v2)
	}
	return out
}

func same(a, b esf.Vertex) bool { return a.X == b.X && a.Y == b.Y && a.Z == b.Z }

// The dome shader: the texture × the layer colour, its UVs scaled by the
// layer's UVTransform and offset by its scroll.
const skyVS = `#version 330
in vec3 vertexPosition;
in vec2 vertexTexCoord;
uniform mat4 mvp;
uniform vec2 uvScale;
uniform vec2 uvOffset;
out vec2 fragTexCoord;
void main() {
    fragTexCoord = vertexTexCoord * uvScale + uvOffset;
    gl_Position = mvp * vec4(vertexPosition, 1.0);
}
`

const skyFS = `#version 330
in vec2 fragTexCoord;
uniform sampler2D texture0;
uniform vec4 layerColor;
out vec4 finalColor;
void main() {
    finalColor = texture(texture0, fragTexCoord) * layerColor;
}
`

// starNoise is VIAtmosphere's sStarNoise: three bytes per star, the
// heading, pitch and roll Init turns (0, 100, 0) by.
var starNoise = [384]byte{
	0xF9, 0xF6, 0xA3, 0x48, 0xC7, 0xDB, 0x76, 0x15, 0x91, 0x35, 0x67, 0xE7,
	0xDA, 0xA9, 0x1A, 0x55, 0x02, 0x62, 0x81, 0x22, 0x71, 0x3E, 0x62, 0x4E,
	0x58, 0x46, 0x7B, 0xBF, 0x5D, 0x8F, 0x75, 0xA3, 0x9B, 0xC8, 0xF1, 0x51,
	0x79, 0xDA, 0x39, 0x60, 0xAF, 0x22, 0x78, 0xAB, 0x1B, 0xFE, 0xA8, 0x94,
	0x2D, 0xFD, 0x18, 0x48, 0xC6, 0xC3, 0x0D, 0x88, 0x5F, 0x19, 0x08, 0x53,
	0x15, 0x9C, 0x9C, 0x9B, 0x69, 0x41, 0xFA, 0x45, 0xD7, 0xFE, 0x21, 0xF0,
	0xD6, 0xFE, 0x0C, 0x07, 0x30, 0x41, 0x59, 0x09, 0xA4, 0x0A, 0xDB, 0xF4,
	0x01, 0x70, 0xC2, 0x16, 0x49, 0x3B, 0x1B, 0x6B, 0x08, 0xD1, 0x2A, 0xC1,
	0x15, 0x16, 0xD3, 0xEA, 0x74, 0x92, 0xAC, 0x10, 0xDC, 0x98, 0x20, 0xB5,
	0x12, 0xC3, 0x78, 0xC0, 0x86, 0x67, 0x92, 0x13, 0x9B, 0x71, 0x28, 0x9B,
	0xED, 0x9D, 0x06, 0x84, 0x32, 0x98, 0x77, 0x5E, 0xFB, 0x19, 0x84, 0xD7,
	0xED, 0x73, 0xD9, 0x18, 0x40, 0x78, 0x20, 0x73, 0xC1, 0xE1, 0xAA, 0xD3,
	0x6A, 0x70, 0x71, 0xA6, 0xF9, 0xE6, 0x89, 0xD3, 0xA9, 0xBB, 0xDB, 0xD1,
	0x1C, 0x40, 0x52, 0x2F, 0xBE, 0x93, 0xEC, 0xB0, 0xD0, 0x15, 0x93, 0xAD,
	0x7A, 0x0A, 0x28, 0xFA, 0xA1, 0xA3, 0xD6, 0x42, 0x58, 0x6B, 0x06, 0xE6,
	0xB7, 0x1D, 0x75, 0x69, 0x8E, 0xFB, 0xB9, 0x43, 0x19, 0xD6, 0x37, 0xDC,
	0x20, 0x34, 0x53, 0xDD, 0xFC, 0x66, 0x4C, 0x30, 0xD3, 0x37, 0xAF, 0xF2,
	0xF7, 0x90, 0xF1, 0x71, 0x52, 0x78, 0xB7, 0x2D, 0xF8, 0x69, 0x1A, 0xD5,
	0xE2, 0x05, 0x03, 0xEF, 0xB7, 0xE1, 0x24, 0xE3, 0x41, 0x08, 0xEC, 0xF9,
	0x4F, 0x82, 0x63, 0xB5, 0x4F, 0x53, 0x03, 0xEB, 0x28, 0xE8, 0xAD, 0x4F,
	0xC9, 0x84, 0x5F, 0xA0, 0xD8, 0xDE, 0xB8, 0x25, 0x20, 0x51, 0x54, 0x45,
	0x3D, 0xD8, 0x25, 0x6A, 0x94, 0x38, 0x27, 0x3F, 0x4C, 0x6F, 0x28, 0xB2,
	0xED, 0x0C, 0x23, 0xA0, 0xF7, 0x67, 0x1B, 0xC7, 0x69, 0x3E, 0x49, 0xC2,
	0x48, 0x05, 0x6A, 0xCB, 0xEA, 0x8C, 0xBD, 0xC9, 0x07, 0x54, 0x6A, 0xDD,
	0xB3, 0x05, 0xCF, 0x8F, 0x35, 0xC3, 0x82, 0xE8, 0xD7, 0x51, 0x5B, 0x63,
	0x55, 0xD7, 0xFD, 0x7A, 0x94, 0xBC, 0xF0, 0xC9, 0x59, 0x6C, 0x09, 0x63,
	0xD9, 0xB7, 0x1D, 0x70, 0xF9, 0x22, 0xCA, 0x5D, 0xFF, 0x53, 0x78, 0xC5,
	0x74, 0xCC, 0x68, 0xA5, 0xC2, 0x24, 0x17, 0xC8, 0x7F, 0x27, 0x8B, 0x43,
	0x22, 0x7F, 0x0F, 0xE2, 0x58, 0x4E, 0x01, 0xF2, 0xE4, 0xB8, 0x18, 0x60,
	0xF5, 0x33, 0x32, 0xFE, 0x1C, 0xBB, 0xAB, 0x5C, 0x3D, 0xE8, 0x2E, 0x87,
	0xCF, 0xD3, 0x75, 0x0A, 0x60, 0xBC, 0xDC, 0xED, 0xB6, 0x07, 0xD7, 0x44,
	0x35, 0x08, 0x52, 0xBE, 0xD2, 0x69, 0xC6, 0xB2, 0x48, 0x9F, 0xF3, 0x0F,
}
