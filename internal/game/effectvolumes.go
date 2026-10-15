package game

// The PS2's VIEffectVolume (decomp/src/vieffectvolume.cpp): a box of weather
// placed by a zone actor — ground fog, a dust storm, falling leaves, light
// shafts or a flock of birds.
//
// Everything lives in the volume's own space: the actor's position is the
// origin (every local box in Tunaria is centred on it), and the actor's
// rotation and scale carry it into the world, as the raster's model-view
// does. The billboard cards (fog, dust) face the camera through
// PrepareBillboardTransform, which hands them the camera's rotation
// expressed in the volume's space, so they face it and take the actor's
// scale; a shaft only turns about world Y.
//
// Blend modes are AttachSurface's: fog, dust and leaves alpha-blend (mode
// 3), shafts are additive (mode 1). The colour fog and shafts are drawn with
// is (1,1,1,a); leaves and dust are (0,0,0,a) with a normal of (0,1,0), so
// what colours them is the scene's light, which BeginTriFan uploads.

import (
	"log"
	"math"
	"math/rand"
	"unsafe"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
	"github.com/eqoa/client/internal/mathutil"
)

// The cards GlobalInit bakes once: the fog's eight rolls, the dust's sixty,
// and a falling leaf's two swings of 30 poses. Four corners each.
var (
	evFogCard    [8][4][3]float32
	evDustCard   [60][4][3]float32
	evFallEffect [2][30][4][3]float32
)

func init() {
	unit := [4][3]float32{{-0.5, 0.5, 0}, {0.5, 0.5, 0}, {0.5, -0.5, 0}, {-0.5, -0.5, 0}}
	for i := range evFogCard {
		m := evRotateZ(float32(i) / 8 * 2 * math.Pi)
		for k := range unit {
			evFogCard[i][k] = evMul(unit[k], m)
		}
	}
	for i := range evDustCard {
		m := evRotateZ(float32(-i) / 60 * 2 * math.Pi)
		for k := range unit {
			evDustCard[i][k] = evMul(unit[k], m)
		}
	}
	for i := 0; i < 30; i++ {
		a := float32(i) / 30 * 2 * math.Pi
		sa := float32(math.Sin(float64(a)))
		ca := float32(math.Cos(float64(a)))

		// Swing 0: hanging 0.5 below the pivot, turned about Y, X, then Z.
		h := float32(0.5)
		v := [4][3]float32{{-0.1, -h, -0.1}, {0.1, -h, -0.1}, {0.1, -h, 0.1}, {-0.1, -h, 0.1}}
		for _, m := range [][3][3]float32{evRotateY(a), evRotateX(sa * 0.4), evRotateZ(-ca * 0.4)} {
			for k := range v {
				v[k] = evMul(v[k], m)
			}
		}
		for k := range v {
			v[k][1] += h
		}
		evFallEffect[0][i] = v

		// Swing 1: 0.4 below, turned about X, then a little about Y.
		h = 0.4
		v = [4][3]float32{{-0.1, -h, -0.1}, {0.1, -h, -0.1}, {0.1, -h, 0.1}, {-0.1, -h, 0.1}}
		for _, m := range [][3][3]float32{evRotateX(sa * h), evRotateY(sa * 0.23)} {
			for k := range v {
				v[k] = evMul(v[k], m)
			}
		}
		for k := range v {
			v[k][1] += h
		}
		evFallEffect[1][i] = v
	}
}

// VIMatrix44::RotateX/Y/Z (row vectors; each one SETS the matrix).
func evRotateX(a float32) [3][3]float32 {
	s, c := evSinCos(a)
	return [3][3]float32{{1, 0, 0}, {0, c, s}, {0, -s, c}}
}

func evRotateY(a float32) [3][3]float32 {
	s, c := evSinCos(a)
	return [3][3]float32{{c, 0, -s}, {0, 1, 0}, {s, 0, c}}
}

func evRotateZ(a float32) [3][3]float32 {
	s, c := evSinCos(a)
	return [3][3]float32{{c, s, 0}, {-s, c, 0}, {0, 0, 1}}
}

// evRotateHPR is VIMatrix44::RotateHPR: x heading, y pitch, z roll.
func evRotateHPR(h, p, r float32) [3][3]float32 {
	sh, ch := evSinCos(h)
	sp, cp := evSinCos(p)
	sr, cr := evSinCos(r)
	return [3][3]float32{
		{cr*ch + sr*sp*sh, sr * cp, -cr*sh + sr*sp*ch},
		{-sr*ch + cr*sp*sh, cr * cp, sr*sh + cr*sp*ch},
		{cp * sh, -sp, cp * ch},
	}
}

func evSinCos(a float32) (float32, float32) {
	s, c := math.Sincos(float64(a))
	return float32(s), float32(c)
}

// evMul is v × m for a row vector.
func evMul(v [3]float32, m [3][3]float32) [3]float32 {
	return [3]float32{
		v[0]*m[0][0] + v[1]*m[1][0] + v[2]*m[2][0],
		v[0]*m[0][1] + v[1]*m[1][1] + v[2]*m[2][1],
		v[0]*m[0][2] + v[1]*m[1][2] + v[2]*m[2][2],
	}
}

// EffectVolumeSystem holds the current zone's volumes.
type EffectVolumeSystem struct {
	volumes      []*EffectVolume
	volumesReady bool
	rng          *rand.Rand

	// The scene's light for the leaves and the dust (SetLighting).
	lightSet bool
	ambient  [3]float32
	sun      [3]float32
	sunDir   [3]float32
}

// EffectVolume is one VIEffectVolume.
type EffectVolume struct {
	EffectType esf.EffectVolumeType
	Density    float32 // m_density
	Intensity  float32 // m_p28
	HourStart  float32 // m_p34
	HourEnd    float32 // m_p38
	FadeRange  float32 // m_p30
	Flags      uint32  // m_flags

	// Origin is the actor's position, the volume's local origin; rot and
	// scale are the actor's (local → world: v × scale·rot + Origin), and size
	// is the local box's (m_bbox.Size()).
	Origin [3]float32
	rot    [3][3]float32
	scale  float32
	size   [3]float32

	fade     float32 // Render's distance gain
	dayNight float32 // Render's hour gain
	lean     float32 // the shafts' lean along world X (flags & 2)
	culled   bool
	reseed   bool // m_reseed

	fog    []evFogPuff
	dust   []evDustMote
	leaves []evLeaf
	shafts []evShaft
	boids  []evBoid
	windIn float32    // m_p6C: seconds to the flock's next waypoint
	wind   [3]float32 // m_wind: the waypoint

	tex         rl.Texture2D
	hasTex      bool
	birdMesh    rl.Mesh
	birdMat     rl.Material
	hasBirdMesh bool
}

// VIFogPuff.
type evFogPuff struct {
	state                   int
	pos                     [3]float32
	card                    int
	size, height, age, life float32
}

// VIDustMote.
type evDustMote struct {
	state                    int
	pos, end                 [3]float32
	frame, base              int
	sizeX, sizeY             float32
	age, rate, life, invLife float32
}

// VIFallingLeaf.
type evLeaf struct {
	state        int
	side         int
	wobble       float32
	pos          [3]float32
	floor, scale float32
	life         float32
}

// VILightShaft.
type evShaft struct {
	state                    int
	pos                      [3]float32
	width, height, age, life float32
}

// VIBoid.
type evBoid struct {
	hpr                   [3]float32
	turn                  float32 // m_p10
	dir, pos              [3]float32
	sep, align, coh, goal [3]float32
	count                 int
	turnGain, climbGain   float32 // m_pA4, m_pA8
	xform                 [3][3]float32
}

// NewEffectVolumeSystem creates an empty system.
func NewEffectVolumeSystem() *EffectVolumeSystem {
	return &EffectVolumeSystem{rng: rand.New(rand.NewSource(1))}
}

// VolumeCount returns the number of loaded effect volumes.
func (s *EffectVolumeSystem) VolumeCount() int {
	return len(s.volumes)
}

// SetLighting gives the leaves and the dust the scene's light: the ambient,
// the sun's colour, and the direction its light travels (m_tod[h].m_b).
// Without it they are drawn as if fully lit.
func (s *EffectVolumeSystem) SetLighting(ambient, sun, sunDir [3]float32) {
	s.lightSet = true
	s.ambient, s.sun, s.sunDir = ambient, sun, sunDir
}

// upLight is the light on a face whose normal is (0, 1, 0) with a black
// vertex colour: min(1, ambient + sun·max(0, −N·D)).
func (s *EffectVolumeSystem) upLight() [3]float32 {
	if !s.lightSet {
		return [3]float32{1, 1, 1}
	}
	k := max(0, -s.sunDir[1])
	var c [3]float32
	for i := range c {
		c[i] = min(1, s.ambient[i]+s.sun[i]*k)
	}
	return c
}

// LoadZoneVolumes loads the zone's volumes, replacing any loaded before.
//
// The placement carries the actor's position and a world box that is the
// local box moved by it; the actor's rotation and scale are not carried
// (esf GetEffectVolumes drops them), so they stay identity until it does.
func (s *EffectVolumeSystem) LoadZoneVolumes(placements []esf.EffectVolumePlacement) {
	s.Clear()
	for i := range placements {
		p := &placements[i]
		vol := &EffectVolume{
			EffectType: p.EffectType,
			Density:    p.Density,
			Intensity:  p.Intensity,
			HourStart:  p.HourStart,
			HourEnd:    p.HourEnd,
			FadeRange:  p.FadeRange,
			Flags:      p.Flags,
			Origin:     [3]float32{p.Pos.X, p.Pos.Y, p.Pos.Z},
			rot:        evRotateHPR(0, 0, 0),
			scale:      1,
			size:       [3]float32{p.BBox.MaxX - p.BBox.MinX, p.BBox.MaxY - p.BBox.MinY, p.BBox.MaxZ - p.BBox.MinZ},
			reseed:     true,
		}
		// The actor's rotation and scale (VIEffectVolumeSprite::Raster hands
		// Render the actor's matrix): 485 Tunaria fog volumes are rotated and
		// 1,551 scaled.
		vol.SetActorTransform([3]float32{p.Rot.X, p.Rot.Y, p.Rot.Z}, p.Scale)
		if p.Texture != nil {
			img := rl.NewImageFromImage(p.Texture)
			vol.tex = rl.LoadTextureFromImage(img)
			rl.UnloadImage(img)
			rl.SetTextureFilter(vol.tex, rl.FilterBilinear)
			rl.SetTextureWrap(vol.tex, rl.WrapClamp)
			vol.hasTex = vol.tex.ID > 0
		}
		if vol.EffectType == esf.EffectTypeFlock && len(p.MeshVertices) > 0 {
			if mesh, mat, ok := buildBirdMesh(p.MeshVertices); ok {
				vol.birdMesh, vol.birdMat, vol.hasBirdMesh = mesh, mat, true
			}
		}
		s.allocParticles(vol)
		s.volumes = append(s.volumes, vol)
	}
	log.Printf("effectvol: loaded %d volumes", len(s.volumes))
	s.volumesReady = true
}

// SetActorTransform gives a volume its actor's rotation (HPR: heading,
// pitch, roll) and scale.
func (vol *EffectVolume) SetActorTransform(rot [3]float32, scale float32) {
	vol.rot = evRotateHPR(rot[0], rot[1], rot[2])
	vol.scale = scale
}

// Clear removes all volumes and unloads their GPU resources.
func (s *EffectVolumeSystem) Clear() {
	for _, vol := range s.volumes {
		if vol.hasTex {
			rl.UnloadTexture(vol.tex)
		}
		if vol.hasBirdMesh {
			rl.UnloadMesh(&vol.birdMesh)
			rl.UnloadMaterial(vol.birdMat)
		}
	}
	s.volumes = s.volumes[:0]
	s.volumesReady = false
}

// IsReady returns true if volumes have been loaded for the current zone.
func (s *EffectVolumeSystem) IsReady() bool {
	return s.volumesReady
}

// virand is VIBOIDRAND: (rand() - rand()) / 2^31.
func (s *EffectVolumeSystem) virand() float32 { return s.rng.Float32() - s.rng.Float32() }

// rand255 is ((rand() * 131) & 0xFF) / 255.
func (s *EffectVolumeSystem) rand255() float32 { return float32(s.rng.Intn(256)) / 255 }

// allocParticles is VIEffectVolume::AllocParticles: the count each type
// takes from the box and the density.
func (s *EffectVolumeSystem) allocParticles(vol *EffectVolume) {
	sz := vol.size
	count := func(c float32) int { return max(int(c), 1) }
	switch vol.EffectType {
	case esf.EffectTypeFallingLeaves:
		vol.leaves = make([]evLeaf, count(sz[0]*sz[1]*0.4*vol.Density))
	case esf.EffectTypeLightShafts:
		vol.shafts = make([]evShaft, count(sz[0]*0.5*vol.Density))
	case esf.EffectTypeGroundFog:
		vol.fog = make([]evFogPuff, count(sz[0]*20*vol.Density))
	case esf.EffectTypeDustStorm:
		vol.dust = make([]evDustMote, count(sz[0]*20*vol.Density))
	case esf.EffectTypeFlock:
		vol.boids = make([]evBoid, int(vol.Density*8)+1)
		for i := range vol.boids {
			b := &vol.boids[i]
			b.hpr = [3]float32{s.virand() * 0.2, s.virand() * 0.2, s.virand() * 0.2}
			b.dir = [3]float32{0, 0, 1}
			b.pos = [3]float32{s.virand() * 3, s.virand() * 3, s.virand() * 3}
			b.turnGain = s.virand()*0.3 + 1
			b.climbGain = s.virand()*0.3 + 1
			b.xform = evRotateHPR(0, 0, 0)
		}
	}
	vol.reseed = true
}

// Update takes Render's two gains for every volume and then runs its
// Process, with the step capped at 0.1 s.
func (s *EffectVolumeSystem) Update(dt, camX, camY, camZ, hour float32) {
	dt = min(dt, 0.1)
	for _, vol := range s.volumes {
		vol.fade = volumeFade(vol.Origin, [3]float32{camX, camY, camZ}, vol.FadeRange)
		vol.dayNight = volumeDayNight(vol.HourStart, vol.HourEnd, hour)
		vol.culled = vol.fade <= 0
		// RenderLightShafts: behind flags & 2 the sun's schedule replaces the
		// hour gain — once the window has let the volume through at all.
		if vol.EffectType == esf.EffectTypeLightShafts && vol.Flags&2 != 0 && vol.dayNight != 0 {
			vol.dayNight, vol.lean = shaftDayNight(hour)
		}
		if vol.culled || vol.dayNight == 0 {
			continue
		}
		switch vol.EffectType {
		case esf.EffectTypeFallingLeaves:
			s.processFallingLeaves(vol, dt)
		case esf.EffectTypeLightShafts:
			s.processLightShafts(vol, dt)
		case esf.EffectTypeGroundFog:
			s.processGroundFog(vol, dt)
		case esf.EffectTypeDustStorm:
			s.processDustStorm(vol, dt)
		case esf.EffectTypeFlock:
			s.processFlock(vol, dt)
		}
	}
}

// volumeFade is VIEffectVolume::Render's distance gain: 1 within three
// quarters of the range from the volume's origin, falling to 0 at the range.
func volumeFade(origin, cam [3]float32, rng float32) float32 {
	dx, dy, dz := cam[0]-origin[0], cam[1]-origin[1], cam[2]-origin[2]
	dist := float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))
	near := rng * 0.75
	if dist < near {
		return 1
	}
	if rng < dist {
		return 0
	}
	return mathutil.ClampF(1-(dist-near)/(rng*0.25), 0, 1)
}

// volumeDayNight is VIEffectVolume::Render's hour gain over [t0, t1]: a
// window under a quarter-hour wide is never on (or always, if t0 >= t1), one
// 23.75 h or wider always on (never, if t0 >= t1), and otherwise on inside the
// window — which may wrap midnight — with a five-minute ramp either side.
func volumeDayNight(t0, t1, h float32) float32 {
	const ramp = 0.083333336
	span := float32(math.Abs(float64(t0 - t1)))
	switch {
	case span <= 0.25:
		if t0 < t1 {
			return 0
		}
		return 1
	case 23.75 <= span:
		if t0 < t1 {
			return 1
		}
		return 0
	}
	e := t1
	if e < t0 {
		if h < e {
			h += 24
		}
		e += 24
	}
	if t0 <= h && h <= e {
		return 1
	}
	if e+ramp < h {
		h -= 24
	} else if h < t0-ramp {
		h += 24
	}
	var g float32
	switch {
	case t0-ramp < h && h <= t0:
		g = (h - (t0 - ramp)) / ramp
	case e <= h && h < e+ramp:
		g = (e + ramp - h) / ramp
	}
	return mathutil.ClampF(g, 0, 1)
}

// processFallingLeaves is VIEffectVolume::ProcessFallingLeaves. A leaf is
// born at the top of the box (anywhere in it on the first pass), falls at
// its own scale per second through a 30-step swing, and below the floor
// finishes the swing it is in before fading at 0.6 a second.
func (s *EffectVolumeSystem) processFallingLeaves(vol *EffectVolume, dt float32) {
	sz := vol.size
	for i := range vol.leaves {
		p := &vol.leaves[i]
		switch p.state {
		case 0:
			p.state = 1
			p.pos[0] = sz[0] * 0.5 * s.virand()
			if vol.reseed {
				p.pos[1] = sz[1] * 0.5 * s.virand()
			} else {
				p.pos[1] = sz[1] * 0.5
			}
			p.pos[2] = sz[2] * 0.5 * s.virand()
			p.scale = s.rand255()*0.8 + 0.5
			p.floor = -sz[1] * 0.5
			p.life = 1
			p.wobble = float32(s.rng.Intn(30))
			p.side = s.rng.Intn(2)
		case 1:
			p.pos[1] -= p.scale * dt
			p.wobble += dt * 30
			if 30 <= p.wobble {
				p.wobble = 0
			}
			if p.pos[1] < p.floor {
				p.state = 2
			}
		case 2:
			if 1 <= p.wobble {
				p.wobble += dt * 30
				if 30 <= p.wobble {
					p.wobble = 0
				}
			} else {
				p.life -= dt * 0.6
			}
			if p.life < 0 {
				p.state = 0
			}
		}
	}
	vol.reseed = false
}

// processLightShafts is VIEffectVolume::ProcessLightShafts: a shaft hangs
// from the top of the box, 0.75..0.95 wide and 0.8..1 of the box high, and
// lives (1 − p28) minutes and change. Every age starts at 0.
func (s *EffectVolumeSystem) processLightShafts(vol *EffectVolume, dt float32) {
	sz := vol.size
	for i := range vol.shafts {
		p := &vol.shafts[i]
		switch p.state {
		case 0:
			p.state = 1
			p.pos = [3]float32{sz[0] * 0.5 * s.virand(), sz[1] * 0.5, sz[2] * 0.5 * s.virand()}
			p.width = (s.rand255()*0.4 + 1.5) * 0.5
			p.height = sz[1] * (s.rand255()*0.2 + 0.8)
			p.age = 0
			k := 1 - vol.Intensity
			p.life = k*60 + 4 + s.rand255()*(k*10+2)
		case 1:
			p.age += dt
			if p.life <= p.age {
				p.state = 0
			}
		}
	}
	vol.reseed = false
}

// processGroundFog is VIEffectVolume::ProcessGroundFog: a puff sits at the
// volume's centre height, 4..6 wide and 0.85..1 of the box high, wears one
// of the eight rolled cards, and lives 10..40 s; on the first pass the ages
// are scattered.
func (s *EffectVolumeSystem) processGroundFog(vol *EffectVolume, dt float32) {
	sz := vol.size
	for i := range vol.fog {
		p := &vol.fog[i]
		switch p.state {
		case 0:
			p.state = 1
			p.size = (s.rand255() + 2) * 2
			p.height = sz[1] * (s.rand255()*0.15 + 0.85)
			p.pos = [3]float32{sz[0] * 0.5 * s.virand(), 0, sz[2] * 0.5 * s.virand()}
			p.card = s.rng.Intn(8)
			p.life = s.rand255()*30 + 10
			if vol.reseed {
				p.age = p.life * s.rand255()
			} else {
				p.age = 0
			}
		case 1:
			p.age += dt
			if p.life <= p.age {
				p.state = 0
			}
		}
	}
	vol.reseed = false
}

// processDustStorm is VIEffectVolume::ProcessDustStorm: a mote is born
// upwind of the box (−x) and blown across it to beyond the far side along
// local +X over its life, turning through the 60 dust cards.
func (s *EffectVolumeSystem) processDustStorm(vol *EffectVolume, dt float32) {
	sz := vol.size
	k := vol.Intensity*0.5 + 0.5
	for i := range vol.dust {
		p := &vol.dust[i]
		switch p.state {
		case 0:
			p.state = 1
			p.sizeX = (s.rand255() + 2) * 2 * k
			p.sizeY = (s.rand255() + 2) * 2 * k
			p.pos[0] = -sz[0]*0.5 - sz[0]*0.25*s.rand255()
			p.pos[1] = sz[1] * 0.5 * s.virand()
			p.pos[2] = sz[2] * 0.5 * s.virand()
			p.end = [3]float32{sz[0]*0.5 + sz[0]*0.25*s.rand255(), p.pos[1], p.pos[2]}
			p.base = s.rng.Intn(1<<16) & 0x3C
			p.frame = p.base
			p.rate = s.rand255()*0.5 + 0.5
			p.life = (s.rand255()*0.7 + 1) * (1 - vol.Intensity + 1)
			p.invLife = 1 / p.life
			if vol.reseed {
				p.age = p.life * s.rand255()
			} else {
				p.age = 0
			}
		case 1:
			p.age += dt
			p.frame = (int(p.age*p.invLife*60*p.rate) + p.base) % 60
			if p.life <= p.age {
				p.state = 0
			}
		}
	}
	vol.reseed = false
}

// processFlock is VIEffectVolume::ProcessFlock: Reynolds' boids within 2
// units of each other separate (by the inverse square), align and cohere,
// and all head for a waypoint that moves every 3..11 s. The sum is
// flattened and steered toward by banking, the climb follows its sign, and
// the speed follows the pitch (2.3..3.7).
func (s *EffectVolumeSystem) processFlock(vol *EffectVolume, dt float32) {
	b := vol.boids
	if vol.windIn <= 0 {
		vol.windIn = float32(s.rng.Intn(9)) + 3
		sz := vol.size
		vol.wind = [3]float32{sz[0] * 0.5 * s.virand(), sz[1] * 0.5 * s.virand(), sz[2] * 0.5 * s.virand()}
	}
	add := func(a, c [3]float32, k float32) [3]float32 {
		return [3]float32{a[0] + c[0]*k, a[1] + c[1]*k, a[2] + c[2]*k}
	}
	mag := func(a [3]float32) float32 {
		return float32(math.Sqrt(float64(a[0]*a[0] + a[1]*a[1] + a[2]*a[2])))
	}
	for i := range b {
		b[i].sep, b[i].align, b[i].coh, b[i].goal, b[i].count = [3]float32{}, [3]float32{}, [3]float32{}, [3]float32{}, 0
	}
	for i := range b {
		for j := i + 1; j < len(b); j++ {
			d := add(b[i].pos, b[j].pos, -1)
			dist := mag(d)
			if dist < 2 && 0 < dist {
				inv := 1 / dist
				t := d
				for k := range t {
					t[k] *= 1 / (dist * dist)
				}
				b[i].sep = add(b[i].sep, t, 1)
				b[j].sep = add(b[j].sep, t, -1)
				b[i].align = add(b[i].align, b[j].dir, inv)
				b[j].align = add(b[j].align, b[i].dir, inv)
				b[i].coh = add(b[i].coh, b[j].pos, 1)
				b[j].coh = add(b[j].coh, b[i].pos, 1)
				b[i].count++
				b[j].count++
			}
		}
		if b[i].count != 0 {
			n := 1 / float32(b[i].count)
			c := add([3]float32{}, b[i].coh, n)
			d := add(c, b[i].pos, -1)
			if m := mag(d); 0 < m {
				b[i].coh = add([3]float32{}, d, 1/m)
			} else {
				b[i].coh = [3]float32{}
			}
			b[i].align = add([3]float32{}, b[i].align, n)
			if m := mag(b[i].align); 0 < m {
				inv := 1 / m
				al := add([3]float32{}, b[i].align, inv)
				b[i].align = add([3]float32{}, add(al, b[i].dir, -1), inv)
			}
		}
		b[i].goal = add(vol.wind, b[i].pos, -1)
	}
	for i := range b {
		p := &b[i]
		a := add(add(add(p.sep, p.align, 1), p.coh, 1), p.goal, 1)
		h := [3]float32{a[0], 0, a[2]}
		c := p.dir
		if m := mag(h); 0 < m {
			h = add([3]float32{}, h, 1/m)
		}
		dot := h[0]*c[0] + h[1]*c[1] + h[2]*c[2]
		cy := h[2]*c[0] - h[0]*c[2] // (h × c).y
		dot = (1 - dot) * 0.5 * 0.0067
		if 0.05 < cy {
			p.turn -= dot
		} else if cy < -0.05 {
			p.turn += dot
		}
		p.turn *= 0.94
		p.hpr[2] = -p.turn * 9
		p.hpr[0] += (p.turn + p.hpr[1]*0.01) * p.turnGain * dt * 30
		if 0 < a[1] {
			p.hpr[1] -= dt * 0.25 * p.climbGain
		} else {
			p.hpr[1] += dt * 0.25 * p.climbGain
		}
		p.hpr[1] = mathutil.ClampF(p.hpr[1], -math.Pi*0.15, math.Pi*0.15)
		p.pos = add(p.pos, p.dir, dt*(p.hpr[1]*2.4+5)*0.6)
		// VIBoid::Apply: the transform, and the heading it flies along is its
		// third row.
		p.xform = evRotateHPR(p.hpr[0], p.hpr[1], p.hpr[2])
		p.dir = p.xform[2]
	}
	vol.windIn -= dt
}

// toWorld carries a local point into the world.
func (vol *EffectVolume) toWorld(p [3]float32) [3]float32 {
	w := evMul(p, vol.rot)
	return [3]float32{vol.Origin[0] + w[0]*vol.scale, vol.Origin[1] + w[1]*vol.scale, vol.Origin[2] + w[2]*vol.scale}
}

// evBeginBlend sets a VIMaterial blend mode's GS ALPHA: 1 Cs·As + Cd, 3 the
// alpha blend.
func evBeginBlend(mode int) {
	const (
		glOne, glSrcAlpha, glOneMinusSrcAlpha, glAdd = 1, 0x0302, 0x0303, 0x8006
	)
	if mode == 1 {
		rl.SetBlendFactors(glSrcAlpha, glOne, glAdd)
	} else {
		rl.SetBlendFactors(glSrcAlpha, glOneMinusSrcAlpha, glAdd)
	}
	rl.BeginBlendMode(rl.BlendCustom)
}

// Render draws every volume in range and inside its hours, with depth
// tested and not written.
func (s *EffectVolumeSystem) Render(camera rl.Camera3D) {
	if len(s.volumes) == 0 {
		return
	}
	right, up := billboardVectors(camera)
	fwd := rl.Vector3Normalize(rl.Vector3Subtract(camera.Target, camera.Position))

	rl.DrawRenderBatchActive()
	rl.DisableDepthMask()
	rl.DisableBackfaceCulling()
	for _, vol := range s.volumes {
		if vol.culled || vol.dayNight == 0 {
			continue
		}
		switch vol.EffectType {
		case esf.EffectTypeGroundFog:
			s.renderGroundFog(vol, right, up)
		case esf.EffectTypeDustStorm:
			s.renderDustStorm(vol, right, up)
		case esf.EffectTypeFallingLeaves:
			s.renderFallingLeaves(vol)
		case esf.EffectTypeLightShafts:
			s.renderLightShafts(vol, fwd)
		case esf.EffectTypeFlock:
			s.renderFlock(vol)
		}
	}
	rl.EnableBackfaceCulling()
	rl.EnableDepthMask()
}

// evQuad draws v0..v3 as the PS2's six-vertex fan 0,1,2,3,2,1 — both
// windings, so either face shows.
func evQuad(v [4][3]float32, col [4]uint8) {
	uv := [4][2]float32{{0, 0}, {1, 0}, {1, 1}, {0, 1}}
	rl.Color4ub(col[0], col[1], col[2], col[3])
	for _, k := range [6]int{0, 1, 2, 0, 2, 3} {
		rl.TexCoord2f(uv[k][0], uv[k][1])
		rl.Vertex3f(v[k][0], v[k][1], v[k][2])
	}
}

func evByte(f float32) uint8 { return uint8(mathutil.ClampF(f, 0, 1) * 255) }

func (vol *EffectVolume) begin(mode int) bool {
	if !vol.hasTex {
		return false
	}
	evBeginBlend(mode)
	rl.SetTexture(vol.tex.ID)
	rl.Begin(rl.Triangles)
	return true
}

func evEnd() {
	rl.End()
	rl.SetTexture(0)
	rl.EndBlendMode()
}

// billboard places card corner c (in camera axes, already sized) at the
// local point p: the card faces the camera and takes the actor's scale.
func (vol *EffectVolume) billboard(c [3]float32, p [3]float32, right, up rl.Vector3) [3]float32 {
	w := vol.toWorld(p)
	k := vol.scale
	return [3]float32{
		w[0] + k*(c[0]*right.X+c[1]*up.X),
		w[1] + k*(c[0]*right.Y+c[1]*up.Y),
		w[2] + k*(c[0]*right.Z+c[1]*up.Z),
	}
}

// renderGroundFog is VIEffectVolume::RenderGroundFog: each puff is its
// rolled card stretched to its size, lifted by up to 1.5 as it swells, white
// at p28 · sin(age/life · π) · k · fade · dayNight, k = (1 − density)/2 + 1/2.
func (s *EffectVolumeSystem) renderGroundFog(vol *EffectVolume, right, up rl.Vector3) {
	if !vol.begin(3) {
		return
	}
	k := (1-vol.Density)*0.5 + 0.5
	for i := range vol.fog {
		p := &vol.fog[i]
		if p.state == 0 {
			continue
		}
		f := float32(math.Sin(float64(p.age/p.life*math.Pi))) * k
		at := [3]float32{p.pos[0], p.pos[1] + f*1.5, p.pos[2]}
		var v [4][3]float32
		for c := range v {
			card := evFogCard[p.card][c]
			v[c] = vol.billboard([3]float32{card[0] * p.size, card[1] * p.height, 0}, at, right, up)
		}
		evQuad(v, [4]uint8{255, 255, 255, evByte(vol.Intensity * f * vol.fade * vol.dayNight)})
	}
	evEnd()
}

// dustCard is the card a mote wears. rand() & 0x3C reaches 60, so on a
// mote's first frame the PS2 reads one card past the end of sDustCard; this
// wraps instead.
func dustCard(frame int) *[4][3]float32 {
	return &evDustCard[frame%60]
}

// renderDustStorm is VIEffectVolume::RenderDustStorm: each mote is its dust
// card, sized, carried from its start to its end over its life, black and
// lit from above, at p28 · sin · k · fade · dayNight.
func (s *EffectVolumeSystem) renderDustStorm(vol *EffectVolume, right, up rl.Vector3) {
	if !vol.begin(3) {
		return
	}
	light := s.upLight()
	k := (1-vol.Density)*0.5 + 0.5
	for i := range vol.dust {
		p := &vol.dust[i]
		if p.state == 0 {
			continue
		}
		t := p.age * p.invLife
		f := float32(math.Sin(float64(t*math.Pi))) * k
		var at [3]float32
		for c := range at {
			at[c] = p.pos[c] + (p.end[c]-p.pos[c])*t
		}
		var v [4][3]float32
		for c := range v {
			card := dustCard(p.frame)[c]
			v[c] = vol.billboard([3]float32{card[0] * p.sizeX, card[1] * p.sizeY, 0}, at, right, up)
		}
		evQuad(v, [4]uint8{evByte(light[0]), evByte(light[1]), evByte(light[2]), evByte(vol.Intensity * f * vol.fade * vol.dayNight)})
	}
	evEnd()
}

// renderFallingLeaves is VIEffectVolume::RenderFallingLeaves: a leaf is its
// swing pose moved to it, in the volume's space; only dayNight of the
// leaves are drawn, black and lit from above, at life · fade.
func (s *EffectVolumeSystem) renderFallingLeaves(vol *EffectVolume) {
	if !vol.begin(3) {
		return
	}
	light := s.upLight()
	n := int(vol.dayNight * float32(len(vol.leaves)))
	for i := 0; i < n; i++ {
		p := &vol.leaves[i]
		if p.state == 0 {
			continue
		}
		pose := evFallEffect[p.side][int(p.wobble)]
		var v [4][3]float32
		for c := range v {
			v[c] = vol.toWorld([3]float32{pose[c][0] + p.pos[0], pose[c][1] + p.pos[1], pose[c][2] + p.pos[2]})
		}
		evQuad(v, [4]uint8{evByte(light[0]), evByte(light[1]), evByte(light[2]), evByte(p.life * vol.fade)})
	}
	evEnd()
}

// shaftDayNight is RenderLightShafts' sun schedule (flags & 2): shafts shine
// 9:00 to 15:00, fading in and out over the first and last hour, and lean
// −1 .. +1 along world X across the day.
func shaftDayNight(h float32) (dayNight, lean float32) {
	switch {
	case h < 9:
		lean = -1
	case h < 15:
		lean = (h-9)/3 - 1
	default:
		lean = 1
	}
	switch {
	case h < 9:
		dayNight = 0
	case h < 10:
		dayNight = h - 9
	case h < 14:
		dayNight = 1
	case h < 15:
		dayNight = 1 - (h - 14)
	}
	return dayNight, lean
}

// renderLightShafts is VIEffectVolume::RenderLightShafts: a shaft is a quad
// hanging from its point, turned about world Y to face the camera's heading,
// additive, at sin(age/life · π) · fade · dayNight · ((1 − density)/2 + 1/2).
func (s *EffectVolumeSystem) renderLightShafts(vol *EffectVolume, dir rl.Vector3) {
	dayNight, lean := vol.dayNight, vol.lean
	if !vol.begin(1) {
		return
	}
	// b.RotateY(atan(dir.z / −dir.x) ∓ π/2): the quad's x axis across the
	// camera's horizontal heading.
	a := float32(math.Atan(float64(dir.Z / -dir.X)))
	if 0 < dir.X {
		a -= math.Pi / 2
	} else {
		a += math.Pi / 2
	}
	b := evRotateY(a)
	alpha := vol.fade * dayNight * ((1-vol.Density)*0.5 + 0.5)
	k := vol.scale
	for i := range vol.shafts {
		p := &vol.shafts[i]
		if p.state == 0 {
			continue
		}
		top := vol.toWorld(p.pos)
		corners := [4][3]float32{{-p.width, 0, 0}, {p.width, 0, 0}, {p.width, -p.height, 0}, {-p.width, -p.height, 0}}
		var v [4][3]float32
		for c := range v {
			o := evMul(corners[c], b)
			v[c] = [3]float32{top[0] + o[0]*k, top[1] + o[1]*k, top[2] + o[2]*k}
		}
		if vol.Flags&2 != 0 {
			v[2][0] += lean * p.height * k
			v[3][0] += lean * p.height * k
		}
		a := float32(math.Sin(float64(p.age/p.life*math.Pi))) * alpha
		evQuad(v, [4]uint8{255, 255, 255, evByte(a)})
	}
	evEnd()
}

// renderFlock draws each boid's bird through its transform, in the
// volume's space. The PS2 draws a copy of the volume's CSprite and switches
// it between gliding and flapping by the pitch; this mesh does not animate.
func (s *EffectVolumeSystem) renderFlock(vol *EffectVolume) {
	if !vol.hasBirdMesh {
		return
	}
	k := vol.scale
	for i := range vol.boids {
		p := &vol.boids[i]
		// local → world: (v × xform + pos) × k·rot + Origin.
		var m [3][3]float32
		for r := 0; r < 3; r++ {
			m[r] = evMul(p.xform[r], vol.rot)
			for c := range m[r] {
				m[r][c] *= k
			}
		}
		o := vol.toWorld(p.pos)
		transform := rl.Matrix{
			M0: m[0][0], M4: m[1][0], M8: m[2][0], M12: o[0],
			M1: m[0][1], M5: m[1][1], M9: m[2][1], M13: o[1],
			M2: m[0][2], M6: m[1][2], M10: m[2][2], M14: o[2],
			M15: 1,
		}
		rl.DrawMesh(vol.birdMesh, vol.birdMat, transform)
	}
}

// buildBirdMesh converts ESF triangle strips (VertexList) into an rl.Mesh.
// The mesh is centred at the origin; each bird is drawn through its own
// transform.
func buildBirdMesh(strips []esf.VertexList) (rl.Mesh, rl.Material, bool) {
	type tri struct {
		v0, v1, v2 esf.Vertex
	}
	var tris []tri
	for _, strip := range strips {
		numTris := len(strip.Vertices) - 2
		if numTris <= 0 {
			continue
		}
		odd := true
		for t := 0; t < numTris; t++ {
			var i0, i1, i2 int
			if odd {
				i0, i1, i2 = t, t+2, t+1
			} else {
				i0, i1, i2 = t, t+1, t+2
			}
			odd = !odd

			v0 := strip.Vertices[i0]
			v1 := strip.Vertices[i1]
			v2 := strip.Vertices[i2]

			if v0.X == v1.X && v0.Y == v1.Y && v0.Z == v1.Z {
				continue
			}
			if v0.X == v2.X && v0.Y == v2.Y && v0.Z == v2.Z {
				continue
			}
			if v1.X == v2.X && v1.Y == v2.Y && v1.Z == v2.Z {
				continue
			}
			tris = append(tris, tri{v0, v1, v2})
		}
	}

	if len(tris) == 0 {
		return rl.Mesh{}, rl.Material{}, false
	}

	vertCount := int32(len(tris) * 3)
	triCount := int32(len(tris))

	mesh := rl.Mesh{
		VertexCount:   vertCount,
		TriangleCount: triCount,
	}

	verts := make([]float32, vertCount*3)
	normals := make([]float32, vertCount*3)
	colors := make([]uint8, vertCount*4)

	for i, t := range tris {
		base := i * 9
		verts[base+0] = t.v0.X
		verts[base+1] = t.v0.Y
		verts[base+2] = t.v0.Z
		verts[base+3] = t.v1.X
		verts[base+4] = t.v1.Y
		verts[base+5] = t.v1.Z
		verts[base+6] = t.v2.X
		verts[base+7] = t.v2.Y
		verts[base+8] = t.v2.Z

		normals[base+0] = t.v0.NX
		normals[base+1] = t.v0.NY
		normals[base+2] = t.v0.NZ
		normals[base+3] = t.v1.NX
		normals[base+4] = t.v1.NY
		normals[base+5] = t.v1.NZ
		normals[base+6] = t.v2.NX
		normals[base+7] = t.v2.NY
		normals[base+8] = t.v2.NZ

		cbase := i * 12
		for j := 0; j < 3; j++ {
			// Dark bird silhouette colour (the CSprite's own textures are not drawn).
			colors[cbase+j*4+0] = 30
			colors[cbase+j*4+1] = 25
			colors[cbase+j*4+2] = 20
			colors[cbase+j*4+3] = 220
		}
	}

	mesh.Vertices = (*float32)(unsafe.Pointer(&verts[0]))
	mesh.Normals = (*float32)(unsafe.Pointer(&normals[0]))
	mesh.Colors = (*uint8)(unsafe.Pointer(&colors[0]))

	rl.UploadMesh(&mesh, false)

	mat := rl.LoadMaterialDefault()

	log.Printf("effectvol: built bird mesh: %d triangles, %d vertices from %d strips",
		triCount, vertCount, len(strips))
	return mesh, mat, true
}
