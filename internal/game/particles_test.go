package game

import (
	"math"
	"testing"

	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

// euler steps a particle the way VIParticleSystem::Render does, for t seconds
// at step dt: v = vel·dt; vel *= 1 − f·dt; pos += v; vel.y += g·dt.
func euler(p Particle, t, dt float32) [3]float32 {
	pos, vel := p.BirthPos, p.BirthVel
	for s := float32(0); s < t-dt/2; s += dt {
		v := vscale(vel, dt)
		vel = vscale(vel, 1-p.Friction*dt)
		pos = [3]float32{pos[0] + v[0], pos[1] + v[1], pos[2] + v[2]}
		vel[1] += p.Gravity * dt
	}
	return pos
}

// With friction on, gravity pulls DOWN toward the terminal speed g/f. The
// analytic position is the limit of the PS2's Euler step, so a fine Euler
// integration of Render's loop must land on it.
func TestFrictionGravityFalls(t *testing.T) {
	p := Particle{
		BirthPos: [3]float32{1, 10, -2},
		BirthVel: [3]float32{0.5, 2, 0},
		Gravity:  particleGravity,
		Friction: 1.5,
		Age:      1.2,
	}
	x, y, z := p.analyticPos()
	ref := euler(p, p.Age, 1.0/4000)
	for i, got := range [3]float32{x, y, z} {
		if math.Abs(float64(got-ref[i])) > 2e-3 {
			t.Errorf("axis %d: analytic %.5f, Render's Euler %.5f", i, got, ref[i])
		}
	}
	// Negative control: the old formula, (v₀ + g/f)·d − (g/f)·t, rises.
	f, g := p.Friction, p.Gravity
	d := (1 - float32(math.Exp(float64(-f*p.Age)))) / f
	old := p.BirthPos[1] + (p.BirthVel[1]+g/f)*d - g/f*p.Age
	if math.Abs(float64(old-ref[1])) < 0.5 {
		t.Fatalf("control: the old formula (%.3f) should miss the Euler height %.3f", old, ref[1])
	}
	if old <= p.BirthPos[1]+p.BirthVel[1]*d {
		t.Fatalf("control: the old formula should lift the particle, got %.3f", old)
	}
}

func testEmitter(pd *esf.ParticleDefinition, pos [3]float32) *ParticleEmitter {
	return &ParticleEmitter{def: pd, pos: pos, alive: true}
}

// DeltaSpawn births one particle per 1/DeltaSpawn units moved, spread from the
// LAST location along the path, even when the emitter was placed twice in the
// frame (the spell driver and the render loop both place it) and when the
// definition has no birthrate of its own.
func TestDeltaSpawnTrail(t *testing.T) {
	pd := &esf.ParticleDefinition{TexConfig: 1, Base: esf.ParticleAttributes{
		DeltaSpawn: 2, Lifespan: 1, NozzleAxis: [3]float32{0, 1, 0}, GradientRepeat: 1,
	}}
	em := testEmitter(pd, [3]float32{0, 0, 0})
	em.SetPosition(0, 0, 0)
	free := maxParticles
	em.update(1.0/30, &free)
	if len(em.particles) != 0 {
		t.Fatalf("a still emitter with no birthrate birthed %d", len(em.particles))
	}
	em.SetPosition(2, 0, 0)
	em.SetPosition(5, 0, 0) // the second placing this frame
	em.update(1.0/30, &free)
	if len(em.particles) != 10 {
		t.Fatalf("5 units at DeltaSpawn 2: %d births, want 10", len(em.particles))
	}
	for i, p := range em.particles {
		if want := float32(i) * 0.5; math.Abs(float64(p.BirthPos[0]-want)) > 1e-5 {
			t.Errorf("birth %d at x=%.3f, want %.3f (from the last location in steps of 0.5)", i, p.BirthPos[0], want)
		}
	}
	if free != maxParticles-10 {
		t.Errorf("pool: %d free, want %d", free, maxParticles-10)
	}
	// Control: the old SetPosition overwrote the last location with the new
	// one, which leaves no distance at the update.
	em.particles = nil
	em.SetPosition(9, 0, 0)
	em.prevPos = em.pos
	em.update(1.0/30, &free)
	if len(em.particles) != 0 {
		t.Fatalf("control: no distance should mean no births, got %d", len(em.particles))
	}
}

// The births stop when the 2,000-particle pool is used up.
func TestParticlePoolCap(t *testing.T) {
	pd := &esf.ParticleDefinition{TexConfig: 1, Base: esf.ParticleAttributes{
		Birthrate: 1e6, Lifespan: 10, NozzleAxis: [3]float32{0, 1, 0},
	}}
	ps := NewParticleSystem(nil)
	ps.emitters = []*ParticleEmitter{testEmitter(pd, [3]float32{}), testEmitter(pd, [3]float32{})}
	ps.Update(1.0/30, 0, 0, 0)
	ps.Update(1.0/30, 0, 0, 0)
	if n := ps.ActiveCount(); n != maxParticles {
		t.Fatalf("%d live particles, want the pool's %d", n, maxParticles)
	}
}

// A negative start size is kept: the PS2 draws it as the same billboard
// flipped. The port clamped it to 0 and then skipped it.
func TestNegativeSizeKept(t *testing.T) {
	pd := &esf.ParticleDefinition{TexConfig: 1, Base: esf.ParticleAttributes{
		Birthrate: 300, Lifespan: 1, StartSize: -0.4, EndSize: 0.2, NozzleAxis: [3]float32{0, 1, 0},
	}}
	em := testEmitter(pd, [3]float32{})
	free := maxParticles
	em.update(1.0/30, &free)
	if len(em.particles) == 0 {
		t.Fatal("no births")
	}
	p := em.particles[0]
	if p.StartSize != -0.2 {
		t.Fatalf("start size %v, want -0.2 (halved, not clamped)", p.StartSize)
	}
	if want := float32(0.3); math.Abs(float64(p.GrowthRate-want)) > 1e-6 {
		t.Fatalf("growth %v, want %v", p.GrowthRate, want)
	}
}

// TexConfig picks the sprite-sheet layout: 1 the whole texture, 2 a half
// (layouts 5-6), 3 and 4 one of the first three or four quadrants.
func TestTexFrame(t *testing.T) {
	cases := map[int32][2]int32{1: {0, 0}, 2: {5, 6}, 3: {1, 3}, 4: {1, 4}}
	for config, r := range cases {
		seen := map[int32]bool{}
		for i := 0; i < 400; i++ {
			f := texFrame(config)
			if f < r[0] || f > r[1] {
				t.Fatalf("config %d gave layout %d, want %d..%d", config, f, r[0], r[1])
			}
			seen[f] = true
		}
		if len(seen) != int(r[1]-r[0]+1) {
			t.Errorf("config %d used %d layouts, want %d", config, len(seen), r[1]-r[0]+1)
		}
	}
	// Layout 4 is the bottom-right quadrant, layout 6 the right half.
	if billboardFrameUV[4] != [4]float32{0.5, 0.5, 1, 1} || billboardFrameUV[6] != [4]float32{0.5, 0, 1, 1} {
		t.Fatal("UV table does not match BeginBillboards'")
	}
}

func motifDef() *esf.ParticleDefinition {
	pd := &esf.ParticleDefinition{TexConfig: 1, Base: esf.ParticleAttributes{
		Birthrate: 10, Lifespan: 1, NozzleAxis: [3]float32{0, 1, 0},
	}}
	pd.Base.Gradient[3] = esf.Color32F{R: 1, A: 1}
	a := esf.ParticleMotif{Name: "Spell Motif 1", Attributes: pd.Base}
	a.Attributes.Birthrate = 30
	a.Attributes.Gradient[3] = esf.Color32F{G: 1, A: 0.5}
	a.Attributes.GravityOn = true
	b := esf.ParticleMotif{Name: "Spell Motif 2", Attributes: pd.Base}
	b.Attributes.Birthrate = 50
	pd.Motifs = []esf.ParticleMotif{a, b}
	return pd
}

// Motifs STACK: the attributes are the definition plus, per linked motif,
// (motif − definition) × factor. Gravity blends as a float.
func TestMotifBlendStacks(t *testing.T) {
	ps := NewParticleSystem(nil)
	em := testEmitter(motifDef(), [3]float32{})
	em.emitterID = 7
	ps.emitters = []*ParticleEmitter{em}

	b1 := ps.LinkMotifBlender(7, viHashResourceID("Spell Motif 1"))
	b2 := ps.LinkMotifBlender(7, viHashResourceID("Spell Motif 2"))
	if !b1.Linked() || !b2.Linked() {
		t.Fatal("motifs not found by name hash")
	}
	if bad := ps.LinkMotifBlender(7, viHashResourceID("No Such Motif")); bad.Linked() {
		t.Fatal("an unknown motif linked")
	}
	b1.SetFactor(0.5)
	b2.SetFactor(0.25)
	em.blendMotifs()
	a := em.getAttr()
	if a.Birthrate != 10+20*0.5+40*0.25 {
		t.Fatalf("birthrate %v, want 30 (10 + 20·0.5 + 40·0.25)", a.Birthrate)
	}
	if g := a.Gradient[3]; g.R != 0.5 || g.G != 0.5 || g.A != 0.75 {
		t.Fatalf("gradient[3] %+v, want {0.5 0.5 0 0.75}", g)
	}
	if em.gravity != -2.5 || !a.GravityOn {
		t.Fatalf("gravity %v, want -2.5", em.gravity)
	}
	// Factor 0 is the definition again.
	b1.SetFactor(0)
	b2.SetFactor(0)
	em.blendMotifs()
	if a := em.getAttr(); a.Birthrate != 10 || em.gravity != 0 {
		t.Fatalf("at factor 0: birthrate %v gravity %v, want the definition's", a.Birthrate, em.gravity)
	}
	// Destroying the emitter unlinks its blenders.
	ps.DestroyEmitter(7)
	if b1.Linked() || b2.Linked() {
		t.Fatal("blenders still linked after the emitter went")
	}
}
