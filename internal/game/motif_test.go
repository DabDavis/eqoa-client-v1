package game

import (
	"math"
	"testing"
	"time"

	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

const testPD = 0x5D000001

// A driver on the caster with a particle system that knows one definition.
func motifDriver(t *testing.T, scene fakeScene) (*SpellEventDriver, *ParticleSystem) {
	t.Helper()
	db := &SpellFXDB{particles: map[int32]*esf.ParticleDefinition{testPD: motifDef()}}
	ps := NewParticleSystem(db)
	eff := newSpellEffectDriver(&esf.SpellEffect{}, tCaster, tTarget, ps, scene.pos, nil, nil, nil)
	ed := newSpellEventDriver(eff, nil, tCaster, 0)
	return ed, ps
}

func createEmitterEvent(id int32) *esf.SpellEvent {
	return &esf.SpellEvent{EventType: 3, Fields: [4]int32{id, testPD, 0xFFFF, 0}}
}

// A SetEmitterMotif ramps its factor over Fields[2] ms, then HOLDS it, and the
// event lives exactly as long as its emitter (ProcessSetEmitterMotif retires it
// once the blender's motif is unlinked). The port expired it after Fields[2]
// ms, or never when that was 0 — which held the effect for the 60 s timeout.
func TestMotifEventLivesWithItsEmitter(t *testing.T) {
	for _, dur := range []int32{100, 0} {
		ed, ps := motifDriver(t, fakeScene{tCaster: {1, 2, 3}})
		t0 := time.Unix(1000, 0)
		ed.newCreateEmitter(createEmitterEvent(1), t0)
		ev := &esf.SpellEvent{EventType: 6,
			Fields: [4]int32{1, viHashResourceID("Spell Motif 1"), dur, 0},
			Floats: [6]float32{1}}
		ed.newSetEmitterMotif(ev, t0)
		motif := ed.activeEvents[len(ed.activeEvents)-1]
		if motif.eventType != 6 || !motif.blender.Linked() {
			t.Fatalf("dur %d: no linked motif event", dur)
		}
		em := ps.emitters[0]

		ed.processActive(t0.Add(50*time.Millisecond), 1.0/30)
		if want := map[int32]float32{100: 0.5, 0: 1}[dur]; motif.blender.Factor() != want {
			t.Fatalf("dur %d at 50 ms: factor %v, want %v", dur, motif.blender.Factor(), want)
		}
		ed.processActive(t0.Add(10*time.Second), 1.0/30)
		if motif.blender.Factor() != 1 || len(ed.activeEvents) != 2 {
			t.Fatalf("dur %d at 10 s: factor %v, %d active events; the motif should hold",
				dur, motif.blender.Factor(), len(ed.activeEvents))
		}
		em.blendMotifs()
		if em.getAttr().Birthrate != 30 {
			t.Fatalf("dur %d: emitter birthrate %v, want the motif's 30", dur, em.getAttr().Birthrate)
		}

		// DestroyEmitter: it drains, goes, and the motif event goes with it.
		ed.newDestroyByType(&esf.SpellEvent{EventType: 4, Fields: [4]int32{1}}, 3)
		ps.Update(1.0/30, 0, 0, 0)
		ps.Update(1.0/30, 0, 0, 0)
		if len(ps.emitters) != 0 {
			t.Fatalf("dur %d: the drained emitter is still there", dur)
		}
		ed.processActive(t0.Add(11*time.Second), 1.0/30)
		if len(ed.activeEvents) != 0 {
			t.Fatalf("dur %d: %d active events left after the emitter went", dur, len(ed.activeEvents))
		}
	}
}

// A second motif event on the same emitter and motif takes the blender over
// and ramps from where the first had got to.
func TestMotifEventTakesOverTheBlender(t *testing.T) {
	ed, _ := motifDriver(t, fakeScene{})
	t0 := time.Unix(1000, 0)
	ed.newCreateEmitter(createEmitterEvent(1), t0)
	hash := viHashResourceID("Spell Motif 1")
	ed.newSetEmitterMotif(&esf.SpellEvent{EventType: 6, Fields: [4]int32{1, hash, 100}, Floats: [6]float32{1}}, t0)
	ed.processActive(t0.Add(40*time.Millisecond), 1.0/30) // factor 0.4
	first := ed.activeEvents[1].blender

	t1 := t0.Add(40 * time.Millisecond)
	ed.newSetEmitterMotif(&esf.SpellEvent{EventType: 6, Fields: [4]int32{1, hash, 100}, Floats: [6]float32{0}}, t1)
	if len(ed.activeEvents) != 2 || ed.activeEvents[1].blender != first {
		t.Fatalf("the second event did not take the first one's blender over")
	}
	ed.processActive(t1.Add(50*time.Millisecond), 1.0/30)
	if f := first.Factor(); math.Abs(float64(f-0.2)) > 1e-6 {
		t.Fatalf("factor %v, want 0.2 (from 0.4 halfway to 0)", f)
	}
	// No emitter with that id: no event at all.
	n := len(ed.activeEvents)
	ed.newSetEmitterMotif(&esf.SpellEvent{EventType: 6, Fields: [4]int32{9, hash, 100}}, t1)
	if len(ed.activeEvents) != n {
		t.Fatal("a motif for an emitter that does not exist made an event")
	}
}

type fakeAudio struct {
	played  []fakePlay
	stopped []int32
}

type fakePlay struct {
	dictID, id      int32
	pos             [3]float32
	volume, maxDist float32
}

func (a *fakeAudio) Play(dictID, id int32, pos [3]float32, volume, maxDist float32) {
	a.played = append(a.played, fakePlay{dictID, id, pos, volume, maxDist})
}
func (a *fakeAudio) Stop(id int32)                         { a.stopped = append(a.stopped, id) }
func (a *fakeAudio) HasSound(int32) bool                   { return true }
func (a *fakeAudio) LoadSpellSounds(file *esf.ObjFile) int { return 0 }

// Destroy* finds its event by type AND the list's own id, so an emitter and a
// sound (or sprite) that are both "1" no longer shadow one another.
func TestDestroyFindsByTypeAndID(t *testing.T) {
	ed, _ := motifDriver(t, fakeScene{tCaster: {1, 2, 3}})
	audio := &fakeAudio{}
	ed.effDriver.system = &SpellEffectSystem{Audio: audio}
	t0 := time.Unix(1000, 0)
	ed.newCreateEmitter(createEmitterEvent(1), t0)
	ed.newCreateSound(&esf.SpellEvent{EventType: 11, Fields: [4]int32{1, 0x777, 0xFFFF, 5000}, Floats: [6]float32{10}}, t0)
	ed.newCreateSprite(&esf.SpellEvent{EventType: 9, Fields: [4]int32{1, 0, 0xFFFF, 0}}, t0)

	ed.newDestroyByType(&esf.SpellEvent{EventType: 12, Fields: [4]int32{1}}, 11)
	ed.newDestroyByType(&esf.SpellEvent{EventType: 10, Fields: [4]int32{1}}, 9)
	if len(ed.activeEvents) != 1 || ed.activeEvents[0].eventType != 3 {
		t.Fatalf("after DestroySound 1 and DestroySprite 1: %d active, want only the emitter", len(ed.activeEvents))
	}
	if len(audio.stopped) != 1 || audio.stopped[0] != audio.played[0].id {
		t.Fatalf("stopped %v, want the sound's own audio ID %d", audio.stopped, audio.played[0].id)
	}
	if audio.played[0].id == 1 {
		t.Fatal("the audio ID is the list's local id, which other effects share")
	}
}

// CreateSound: the actor and node come from Fields[2], and the reach is
// Floats[0] (the actor scale) × the sprite's radius — 1 in a version-0 header,
// so 10 units, not 50. It stops when its lifetime is up.
func TestCreateSoundReachAndLifetime(t *testing.T) {
	ed, _ := motifDriver(t, fakeScene{tCaster: {1, 2, 3}, tTarget: {7, 8, 9}})
	audio := &fakeAudio{}
	ed.effDriver.system = &SpellEffectSystem{Audio: audio}
	t0 := time.Unix(1000, 0)
	ed.newCreateSound(&esf.SpellEvent{EventType: 11, Fields: [4]int32{2, 0x777, 0x6FFFF, 300}, Floats: [6]float32{10}}, t0)
	if len(audio.played) != 1 {
		t.Fatal("nothing played")
	}
	p := audio.played[0]
	if p.maxDist != 10 || p.volume != 1 {
		t.Fatalf("reach %v volume %v, want 10 and 1", p.maxDist, p.volume)
	}
	if p.pos != [3]float32{7, 8, 9} {
		t.Fatalf("played at %v, want the target (bit 17) at %v", p.pos, [3]float32{7, 8, 9})
	}
	ed.processActive(t0.Add(200*time.Millisecond), 1.0/30)
	if len(audio.stopped) != 0 {
		t.Fatal("stopped before its lifetime")
	}
	ed.processActive(t0.Add(301*time.Millisecond), 1.0/30)
	if len(audio.stopped) != 1 || len(ed.activeEvents) != 0 {
		t.Fatalf("after its 300 ms: stopped %v, %d active", audio.stopped, len(ed.activeEvents))
	}
}

// SetEmitterAttractor's Fields[1] selects an actor and node like a flags word;
// it is not an object ID. -1 clears the attractor.
func TestAttractorSelector(t *testing.T) {
	ed, ps := motifDriver(t, fakeScene{tCaster: {1, 2, 3}, tTarget: {7, 8, 9}})
	t0 := time.Unix(1000, 0)
	ed.newCreateEmitter(createEmitterEvent(1), t0)
	em := ps.emitters[0]

	ed.newSetEmitterAttractor(&esf.SpellEvent{EventType: 5, Fields: [4]int32{1, 0x2FFFF}, Floats: [6]float32{4}})
	ed.updateEmitterPositions(1.0 / 30)
	if !em.attractorActive || em.attractorPos != [3]float32{7, 8, 9} || em.attractorStrength != 4 {
		t.Fatalf("attractor %v at %v k=%v, want the target at (7,8,9) k=4",
			em.attractorActive, em.attractorPos, em.attractorStrength)
	}
	ed.newSetEmitterAttractor(&esf.SpellEvent{EventType: 5, Fields: [4]int32{1, -1}})
	ed.updateEmitterPositions(1.0 / 30)
	if em.attractorActive {
		t.Fatal("a selector of -1 should clear the attractor")
	}
}

// The emit-shape offsets are v · RotateHPR(angle) with row vectors: written
// here straight from VIMatrix44::RotateHPR and VIMatrix44::Mul.
func TestEmitShapeOffsetIsRowVectorRotateHPR(t *testing.T) {
	rotateHPRMul := func(hpr, v [3]float32) [3]float32 {
		si, ci := math.Sincos(float64(hpr[0]))
		sj, cj := math.Sincos(float64(hpr[1]))
		sh, ch := math.Sincos(float64(hpr[2]))
		m := [3][3]float64{
			{ch*ci + sh*sj*si, sh * cj, -ch*si + sh*sj*ci},
			{-sh*ci + ch*sj*si, ch * cj, sh*si + ch*sj*ci},
			{cj * si, -sj, cj * ci},
		}
		var out [3]float32
		for c := 0; c < 3; c++ {
			out[c] = float32(float64(v[0])*m[0][c] + float64(v[1])*m[1][c] + float64(v[2])*m[2][c])
		}
		return out
	}
	ev := &esf.SpellEvent{Floats: [6]float32{0.6, 1.4, 0, 0, 2.5}}
	for shape := int32(1); shape <= 3; shape++ {
		for _, a := range []float32{0.3, math.Pi / 2, 2.1, -1.2} {
			ae := &ActiveEvent{event: ev, emitShape: shape, emitAngle: a, emitRadParam: 0.7}
			got := emitShapeOffset(ae)
			tt := float32(math.Sin(0.7))*0.5 + 0.5
			r := 0.6 + 0.8*tt
			var hpr, v [3]float32
			switch shape {
			case 1:
				hpr, v = [3]float32{0, a, 0}, [3]float32{r, 2.5, 0}
			case 2:
				hpr, v = [3]float32{a, 0, 0}, [3]float32{0, r, 2.5}
			case 3:
				hpr, v = [3]float32{0, 0, a}, [3]float32{2.5, 0, r}
			}
			want := rotateHPRMul(hpr, v)
			for i := range got {
				if math.Abs(float64(got[i]-want[i])) > 1e-5 {
					t.Fatalf("shape %d angle %v: %v, want %v", shape, a, got, want)
				}
			}
		}
	}
	// Control: the old shape-2 offset, (−e·sin a, r, e·cos a), runs the other way.
	a := float32(math.Pi / 2)
	old := [3]float32{-2.5 * float32(math.Sin(float64(a))), 1, 2.5 * float32(math.Cos(float64(a)))}
	if want := rotateHPRMul([3]float32{a, 0, 0}, [3]float32{0, 1, 2.5}); math.Abs(float64(old[0]-want[0])) < 1 {
		t.Fatal("control: the old shape-2 offset should disagree")
	}
}

// ShakeCamera acts only for the local player, and VICameraEffect bobs the eye
// vertically through its speed's sample cycle; ShakeTerp ramps the amplitude.
func TestShakeCamera(t *testing.T) {
	sys := &SpellEffectSystem{IsPlayer: func(id uint32) bool { return id == tCaster }}
	eff := newSpellEffectDriver(&esf.SpellEffect{}, tCaster, tTarget, nil, fakeScene{}.pos, nil, nil, sys)
	shake := &esf.SpellEvent{EventType: 17, Fields: [4]int32{0x10000, 1}, Floats: [6]float32{4}}

	newSpellEventDriver(eff, nil, tTarget, 1).newShakeCamera(shake)
	if sys.Camera.speed != 0 {
		t.Fatal("a shake on another actor's list moved the player's camera")
	}
	newSpellEventDriver(eff, nil, tCaster, 0).newShakeCamera(shake)
	want := []float32{1, 0.7, -0.7, -1, -0.7, 0.7, 1}
	for i, s := range want {
		pos, dir := [3]float32{0, 10, 0}, [3]float32{0, 0, 2}
		sys.Camera.Apply(&pos, &dir)
		if d := pos[1] - 10 - 4*0.025*s; math.Abs(float64(d)) > 1e-6 || math.Abs(float64(dir[1]-4*0.009*s)) > 1e-6 {
			t.Fatalf("apply %d: eye y %+v dir %+v, want sample %v", i, pos, dir, s)
		}
	}
	// Ramp to 0 over 2 s: half way at 1 s, silent (still running) at 2 s.
	sys.Camera.ShakeTerp(0, 2)
	sys.Camera.Update(1)
	if sys.Camera.amplitude != 2 {
		t.Fatalf("amplitude %v at 1 s, want 2", sys.Camera.amplitude)
	}
	sys.Camera.Update(5)
	if sys.Camera.amplitude != 0 || sys.Camera.amplitudeRate != 0 {
		t.Fatalf("amplitude %v rate %v after the ramp, want 0 and 0", sys.Camera.amplitude, sys.Camera.amplitudeRate)
	}
}
