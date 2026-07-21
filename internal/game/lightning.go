package game

// Port of VILightning from PS2 SUPPORT module.
// Renders a jagged lightning bolt between two 3D points with charge decay,
// branching, and per-frame jitter regeneration.
//
// PS2 VILightning is 8832 (0x2280) bytes:
//   +0x18C0  maxBranchCount (int)
//   +0x18C8  charge (float)
// The bolt is rendered as a chain of line segments with random perpendicular
// offsets.

import (
	"math"
	"math/rand"

	rl "github.com/gen2brain/raylib-go/raylib"
)

const (
	lightningSegments     = 16   // PS2: 17 control points, step=1/16
	lightningJitterScale  = 2.0  // max perpendicular displacement per segment
	lightningChargeDecay  = 5.0  // PS2: deltaMs*5*0.001 = 5.0 charge/sec
	lightningRegenMin     = 0.070 // PS2: min 70ms
	lightningRegenRange   = 0.280 // PS2: rand()%351 capped 70, range [70, 350] ms
)

// LightningBolt represents a single lightning bolt between two points.
// Port of VILightning from PS2 SUPPORT module (0x2280 bytes).
type LightningBolt struct {
	Source    [3]float32 // bolt start position
	Target    [3]float32 // bolt end position
	Color  [3]float32 // RGB (0-1)
	Charge float32    // current charge (1.0 = bright, decays to 0)

	// Pre-generated bolt shape (offsets perpendicular to bolt axis)
	mainOffsets [][2]float32 // [segment][perpAxis1, perpAxis2]

	// Perpendicular basis vectors for the bolt
	perp1 [3]float32 // perpendicular axis 1
	perp2 [3]float32 // perpendicular axis 2

	regenTimer float32 // time since last shape regeneration
	alive      bool
}

// NewLightningBolt creates a lightning bolt with the given parameters.
func NewLightningBolt(sx, sy, sz, tx, ty, tz float32, color [3]float32) *LightningBolt {
	lb := &LightningBolt{
		Source: [3]float32{sx, sy, sz},
		Target: [3]float32{tx, ty, tz},
		Color:  color,
		Charge: 1.0,
		alive:  true,
	}
	lb.computeBasis()
	lb.regenerate()
	return lb
}

// computeBasis builds two perpendicular vectors to the bolt axis.
func (lb *LightningBolt) computeBasis() {
	dx := lb.Target[0] - lb.Source[0]
	dy := lb.Target[1] - lb.Source[1]
	dz := lb.Target[2] - lb.Source[2]
	length := float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))
	if length < 0.001 {
		lb.perp1 = [3]float32{1, 0, 0}
		lb.perp2 = [3]float32{0, 1, 0}
		return
	}
	// Normalize bolt direction
	ax := dx / length
	ay := dy / length
	az := dz / length

	// Find a vector not parallel to axis
	var up [3]float32
	if math.Abs(float64(ay)) < 0.9 {
		up = [3]float32{0, 1, 0}
	} else {
		up = [3]float32{1, 0, 0}
	}

	// Cross product: perp1 = axis × up
	lb.perp1[0] = ay*up[2] - az*up[1]
	lb.perp1[1] = az*up[0] - ax*up[2]
	lb.perp1[2] = ax*up[1] - ay*up[0]
	p1len := float32(math.Sqrt(float64(lb.perp1[0]*lb.perp1[0] + lb.perp1[1]*lb.perp1[1] + lb.perp1[2]*lb.perp1[2])))
	if p1len > 0.001 {
		lb.perp1[0] /= p1len
		lb.perp1[1] /= p1len
		lb.perp1[2] /= p1len
	}

	// Cross product: perp2 = axis × perp1
	lb.perp2[0] = ay*lb.perp1[2] - az*lb.perp1[1]
	lb.perp2[1] = az*lb.perp1[0] - ax*lb.perp1[2]
	lb.perp2[2] = ax*lb.perp1[1] - ay*lb.perp1[0]
}

// regenerate creates new random bolt shape (jitter offsets).
func (lb *LightningBolt) regenerate() {
	n := lightningSegments
	lb.mainOffsets = make([][2]float32, n+1)
	// Endpoints have no jitter
	lb.mainOffsets[0] = [2]float32{0, 0}
	lb.mainOffsets[n] = [2]float32{0, 0}
	for i := 1; i < n; i++ {
		// Jitter scales with distance from endpoints (midpoint gets most)
		t := float32(i) / float32(n)
		envelope := 4.0 * t * (1.0 - t) // peaks at 1.0 at midpoint
		lb.mainOffsets[i] = [2]float32{
			(rand.Float32()*2 - 1) * lightningJitterScale * envelope,
			(rand.Float32()*2 - 1) * lightningJitterScale * envelope,
		}
	}

}

// SetEndpoints updates the bolt source and target positions.
func (lb *LightningBolt) SetEndpoints(sx, sy, sz, tx, ty, tz float32) {
	lb.Source = [3]float32{sx, sy, sz}
	lb.Target = [3]float32{tx, ty, tz}
	lb.computeBasis()
}

// Flash sets charge to 1.0 (called when PS2 timer expires for final bright flash).
func (lb *LightningBolt) Flash() {
	lb.Charge = 1.0
}

// Update advances the bolt simulation by dt seconds.
// PS2 VILightning::Update(deltaMs) decays charge and periodically regenerates bolt shape.
func (lb *LightningBolt) Update(dt float32) {
	if !lb.alive {
		return
	}

	// Decay charge over time
	lb.Charge -= lightningChargeDecay * dt
	if lb.Charge <= 0 {
		lb.Charge = 0
		lb.alive = false
		return
	}

	// Periodically regenerate bolt shape (jitter)
	// PS2 VILightning::Update (0x0044F0C8): timer = rand()%351, clamped min 70.
	// Range [70, 350] ms = [0.070, 0.350] seconds.
	lb.regenTimer += dt
	regenThreshold := lightningRegenMin + rand.Float32()*lightningRegenRange
	if lb.regenTimer >= regenThreshold {
		lb.regenTimer = 0
		lb.regenerate()
	}
}

// Alive returns whether the bolt still has charge remaining.
func (lb *LightningBolt) Alive() bool {
	return lb.alive && lb.Charge > 0
}

// Render draws the lightning bolt using immediate-mode lines.
func (lb *LightningBolt) Render() {
	if !lb.alive || lb.Charge <= 0 {
		return
	}

	// Compute alpha from charge
	alpha := lb.Charge
	if alpha > 1 {
		alpha = 1
	}

	// Main bolt color
	mainColor := rl.Color{
		R: uint8(lb.Color[0] * 255 * alpha),
		G: uint8(lb.Color[1] * 255 * alpha),
		B: uint8(lb.Color[2] * 255 * alpha),
		A: uint8(255 * alpha),
	}

	// Glow (brighter core)
	coreColor := rl.Color{
		R: uint8(math.Min(float64(lb.Color[0]*255*alpha+80*alpha), 255)),
		G: uint8(math.Min(float64(lb.Color[1]*255*alpha+80*alpha), 255)),
		B: uint8(math.Min(float64(lb.Color[2]*255*alpha+80*alpha), 255)),
		A: uint8(200 * alpha),
	}

	n := lightningSegments
	// Draw main bolt
	lb.drawChain(lb.Source, lb.Target, lb.mainOffsets, n, mainColor)
	// Draw core (slightly offset for glow effect)
	lb.drawChain(lb.Source, lb.Target, lb.mainOffsets, n, coreColor)
}

// drawChain draws a segmented line with perpendicular offsets.
func (lb *LightningBolt) drawChain(src, tgt [3]float32, offsets [][2]float32, segs int, color rl.Color) {
	prev := rl.Vector3{X: src[0], Y: src[1], Z: src[2]}
	for i := 1; i <= segs; i++ {
		t := float32(i) / float32(segs)
		// Linear interpolation along bolt axis
		px := src[0] + (tgt[0]-src[0])*t
		py := src[1] + (tgt[1]-src[1])*t
		pz := src[2] + (tgt[2]-src[2])*t
		// Add perpendicular offset
		if i < len(offsets) {
			px += lb.perp1[0]*offsets[i][0] + lb.perp2[0]*offsets[i][1]
			py += lb.perp1[1]*offsets[i][0] + lb.perp2[1]*offsets[i][1]
			pz += lb.perp1[2]*offsets[i][0] + lb.perp2[2]*offsets[i][1]
		}
		pt := rl.Vector3{X: px, Y: py, Z: pz}
		rl.DrawLine3D(prev, pt, color)
		prev = pt
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
