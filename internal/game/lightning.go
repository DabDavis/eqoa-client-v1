package game

// The PS2's spell lightning, VILightning (decomp/src/vilightning.cpp): a bolt
// of 17 points between two ends, bent by three harmonics of interpolated
// noise — 4 segments of 0.57, 8 of 0.15, and a per-point 0.08, cross-fading
// to fresh noise at 0.64, 10.24 and 44 cycles a second — and up to three
// branches from the main bolt's points 3, 6 and 9 to the same end. A strike
// lasts 70..350 ms (a quarter of that with no branches) and then re-rolls
// its branch count. Every 30 ms the bolts are copied into a three-frame ring,
// and the older frames are drawn at 2/3 and 1/3 brightness. Each bolt is four
// ribbons 0.08 wide, two vertical and two across, both windings, additive,
// at colour · charge. The charge falls at 5 a second; the spell holds it at
// 1 for the event's duration.
//
// The material is lightning.tga, which no file on this disc carries, so the
// ribbons are drawn untextured.

import (
	"math"
	"math/rand"

	rl "github.com/gen2brain/raylib-go/raylib"
)

type lvec [3]float32

func (a lvec) add(b lvec) lvec      { return lvec{a[0] + b[0], a[1] + b[1], a[2] + b[2]} }
func (a lvec) sub(b lvec) lvec      { return lvec{a[0] - b[0], a[1] - b[1], a[2] - b[2]} }
func (a lvec) scale(k float32) lvec { return lvec{a[0] * k, a[1] * k, a[2] * k} }
func (a lvec) mul(m [3]lvec) lvec {
	return m[0].scale(a[0]).add(m[1].scale(a[1])).add(m[2].scale(a[2]))
}
func lcross(a, b lvec) lvec {
	return lvec{a[1]*b[2] - a[2]*b[1], -a[0]*b[2] + a[2]*b[0], a[0]*b[1] - a[1]*b[0]}
}
func (a lvec) normalized() lvec {
	l := float32(math.Sqrt(float64(a[0]*a[0] + a[1]*a[1] + a[2]*a[2])))
	if l == 0 {
		return a
	}
	return a.scale(1 / l)
}

// boltHarmonic is VIBoltHarmonic: 17 samples cross-fading to 17 new ones.
type boltHarmonic struct {
	phase, rate, amplitude float32
	samples                [2][17]lvec
}

func (h *boltHarmonic) create(amplitude, rate float32, rng *rand.Rand) {
	h.phase, h.rate, h.amplitude = 0, rate, amplitude
	h.generate(0, rng)
	h.generate(1, rng)
}

// generate is GenerateSamples: x and y within ±amplitude, z 0.
func (h *boltHarmonic) generate(which int, rng *rand.Rand) {
	for i := range h.samples[which] {
		h.samples[which][i] = lvec{
			float32(rng.Intn(256)-127) / 128 * h.amplitude,
			float32(rng.Intn(256)-127) / 128 * h.amplitude,
			0,
		}
	}
}

// sample is Sample: old + (new − old)·(1 − t), t = sin(phase·π + π/2)/2 + 1/2.
func (h *boltHarmonic) sample(i int) lvec {
	t := float32(math.Sin(float64(h.phase)*math.Pi+math.Pi/2))*0.5 + 0.5
	return h.samples[0][i].add(h.samples[1][i].sub(h.samples[0][i]).scale(1 - t))
}

func (h *boltHarmonic) update(dtMs int, rng *rand.Rand) {
	if 1 <= h.phase {
		h.phase = 0
		h.samples[0] = h.samples[1]
		h.generate(1, rng)
		return
	}
	h.phase = min(1, h.phase+float32(dtMs)*0.001*h.rate)
}

// bolt is VIBolt.
type bolt struct {
	harmonics [3]boltHarmonic
	points    [17]lvec
}

func (b *bolt) reset(rng *rand.Rand) {
	b.harmonics[0].create(0.57, 0.64, rng)
	b.harmonics[1].create(0.15, 10.24, rng)
	b.harmonics[2].create(0.08, 44.0, rng)
}

// update is VIBolt::Update: the straight line, then four segments bent by
// harmonic 0 (the last end held), eight by harmonic 1, and every inner point
// moved by harmonic 2 — each offset turned into the bolt's frame.
func (b *bolt) update(dtMs int, a, e lvec, m [3]lvec, rng *rand.Rand) {
	for i := range b.harmonics {
		b.harmonics[i].update(dtMs, rng)
	}
	d := e.sub(a)
	for i := range b.points {
		b.points[i] = a.add(d.scale(float32(i) * 0.0625))
	}
	bend := func(n, span int, h *boltHarmonic, last bool) {
		for i := 0; i < n; i++ {
			start, end := i*span, i*span+span
			t := h.sample(i).mul(m)
			d := b.points[end]
			if last || i < n-1 {
				d = d.add(t)
			}
			d = d.sub(b.points[start])
			for j := start; j <= end; j++ {
				b.points[j] = b.points[start].add(d.scale(float32(j-start) / float32(end-start)))
			}
		}
	}
	bend(4, 4, &b.harmonics[0], false)
	bend(8, 2, &b.harmonics[1], true)
	for i := 1; i < 16; i++ {
		b.points[i] = b.points[i].add(b.harmonics[2].sample(i).mul(m))
	}
}

// LightningBolt is VILightning.
type LightningBolt struct {
	Source, Target [3]float32
	Color          [3]float32
	Charge         float32

	lastCharge  float32
	matrix      [3]lvec // right, up, along
	strikeMs    int
	boltCount   int
	maxBranches int
	bolts       [4]bolt
	ring        [3]struct {
		count  int
		points [4][17]lvec
	}
	ringHead, ringCount, ringMs int
	rng                         *rand.Rand
}

// NewLightningBolt makes a bolt from (sx,sy,sz) to (tx,ty,tz) at charge 1.
func NewLightningBolt(sx, sy, sz, tx, ty, tz float32, color [3]float32) *LightningBolt {
	lb := &LightningBolt{Color: color, Charge: 1, ringHead: -1, rng: rand.New(rand.NewSource(rand.Int63()))}
	for i := range lb.bolts {
		lb.bolts[i].reset(lb.rng)
	}
	lb.SetEndpoints(sx, sy, sz, tx, ty, tz)
	return lb
}

// SetEndpoints is SetLocation: the ends and the bolt's frame — right across
// it and level, up perpendicular to both, and along it.
func (lb *LightningBolt) SetEndpoints(sx, sy, sz, tx, ty, tz float32) {
	lb.Source = [3]float32{sx, sy, sz}
	lb.Target = [3]float32{tx, ty, tz}
	d := lvec(lb.Target).sub(lvec(lb.Source))
	right := lcross(lvec{0, 1, 0}, d)
	up := lcross(d, right)
	lb.matrix = [3]lvec{right.normalized(), up.normalized(), d.normalized()}
}

// SetMaxBranchCount is VILightning::SetMaxBranchCount (the event's m_p10),
// at most 3.
func (lb *LightningBolt) SetMaxBranchCount(n int) {
	lb.maxBranches = min(n, 3)
}

// Flash is SetCharge(1), which the spell does every frame of its duration.
func (lb *LightningBolt) Flash() {
	lb.Charge = 1
}

// Update is VILightning::Update, with dt in seconds (the PS2 hands it whole
// milliseconds).
func (lb *LightningBolt) Update(dt float32) {
	ms := int(dt * 1000)
	lb.strikeMs -= ms
	lb.ringMs = max(lb.ringMs-ms, 0)
	if lb.strikeMs <= 0 {
		for i := 1; i < 4; i++ {
			if i < lb.boltCount {
				lb.bolts[i].reset(lb.rng)
			}
		}
		lb.strikeMs = max(lb.rng.Intn(351), 70)
		lb.boltCount = lb.rng.Intn(lb.maxBranches+1) + 1
		if lb.boltCount == 1 {
			lb.strikeMs >>= 2
		}
	}
	for i := 0; i < lb.boltCount; i++ {
		if i == 0 {
			lb.bolts[0].update(ms, lvec(lb.Source), lvec(lb.Target), lb.matrix, lb.rng)
		} else {
			lb.bolts[i].update(ms, lb.bolts[0].points[i*3], lvec(lb.Target), lb.matrix, lb.rng)
		}
	}
	lb.lastCharge = lb.Charge
	lb.Charge = max(0, lb.Charge-float32(ms*5)*0.001)
}

// Alive returns whether the bolt still has charge.
func (lb *LightningBolt) Alive() bool {
	return lb.Charge > 0 || lb.lastCharge > 0
}

// Render is VILightning::Render: the ring, then every frame in it.
func (lb *LightningBolt) Render() {
	if lb.ringMs == 0 {
		lb.ringHead = (lb.ringHead + 1) % 3
		lb.ringMs = 30
		lb.ringCount = min(3, lb.ringCount+1)
	}
	r := &lb.ring[lb.ringHead]
	r.count = lb.boltCount
	for i := 0; i < lb.boltCount; i++ {
		r.points[i] = lb.bolts[i].points
	}

	const glOne, glSrcAlpha, glAdd = 1, 0x0302, 0x8006
	rl.DrawRenderBatchActive()
	rl.DisableDepthMask()
	rl.SetBlendFactors(glSrcAlpha, glOne, glAdd)
	rl.BeginBlendMode(rl.BlendCustom)
	rl.Begin(rl.Triangles)
	for i := 0; i < lb.ringCount; i++ {
		f := &lb.ring[(lb.ringHead-i+3)%3]
		for j := 0; j < f.count; j++ {
			lb.renderBolt(&f.points[j], 3-i)
		}
	}
	rl.End()
	rl.EndBlendMode()
	rl.EnableDepthMask()
}

// renderBolt is RenderBoltVertices: four ribbons — offsets ±0.04 up, then
// ±0.04 along the bolt's right — each two strips of points 0..8 and 8..16,
// pinched at the ends, at colour · n/3 · the charge.
func (lb *LightningBolt) renderBolt(pts *[17]lvec, n int) {
	k := float32(n) / 3 * lb.lastCharge
	c := func(v float32) uint8 { return uint8(min(max(v*k, 0), 1) * 255) }
	rl.Color4ub(c(lb.Color[0]), c(lb.Color[1]), c(lb.Color[2]), 255)

	side := lvec{0.04, 0, 0}.mul(lb.matrix)
	for _, off := range [4]lvec{{0, 0.04, 0}, {0, -0.04, 0}, side, side.scale(-1)} {
		var strip []lvec
		strip = append(strip, pts[0], pts[0])
		for j := 1; j < 9; j++ {
			strip = append(strip, pts[j].sub(off), pts[j].add(off))
		}
		lightningStrip(strip)
		strip = append(strip[:0], pts[8].sub(off), pts[8].add(off))
		for j := 9; j < 17; j++ {
			if j == 16 {
				strip = append(strip, pts[16], pts[16])
			} else {
				strip = append(strip, pts[j].sub(off), pts[j].add(off))
			}
		}
		lightningStrip(strip)
	}
}

// lightningStrip emits a triangle strip as triangles, alternating winding
// as a strip does.
func lightningStrip(v []lvec) {
	for i := 0; i+2 < len(v); i++ {
		a, b, c := v[i], v[i+1], v[i+2]
		if i%2 == 1 {
			a, b = b, a
		}
		rl.Vertex3f(a[0], a[1], a[2])
		rl.Vertex3f(b[0], b[1], b[2])
		rl.Vertex3f(c[0], c[1], c[2])
	}
}

// LightningSystem manages active lightning bolts for spell effects.
type LightningSystem struct {
	bolts map[uint64]*LightningBolt // key = driverID<<32 | boltIndex
}

// NewLightningSystem creates a new lightning system.
func NewLightningSystem() *LightningSystem {
	return &LightningSystem{
		bolts: make(map[uint64]*LightningBolt),
	}
}

// GetOrCreate returns an existing bolt or creates a new one.
func (ls *LightningSystem) GetOrCreate(key uint64, sx, sy, sz, tx, ty, tz float32, color [3]float32) *LightningBolt {
	if lb, ok := ls.bolts[key]; ok {
		return lb
	}
	lb := NewLightningBolt(sx, sy, sz, tx, ty, tz, color)
	ls.bolts[key] = lb
	return lb
}

// Update advances all bolts and removes dead ones.
func (ls *LightningSystem) Update(dt float32) {
	for key, lb := range ls.bolts {
		lb.Update(dt)
		if !lb.Alive() {
			delete(ls.bolts, key)
		}
	}
}

// Render draws all active bolts.
func (ls *LightningSystem) Render() {
	for _, lb := range ls.bolts {
		lb.Render()
	}
}

// Remove deletes a specific bolt by key.
func (ls *LightningSystem) Remove(key uint64) {
	delete(ls.bolts, key)
}

// GetKeys returns all active bolt keys.
func (ls *LightningSystem) GetKeys() map[uint64]struct{} {
	keys := make(map[uint64]struct{}, len(ls.bolts))
	for k := range ls.bolts {
		keys[k] = struct{}{}
	}
	return keys
}

// Clear removes all bolts.
func (ls *LightningSystem) Clear() {
	ls.bolts = make(map[uint64]*LightningBolt)
}
