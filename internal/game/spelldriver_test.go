package game

import (
	"math"
	"testing"
	"time"

	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

// A scene of actors at fixed points; no bone resolver, so every node is the
// actor's own location, as GetNodeTransform gives for a sprite without one.
type fakeScene map[uint32][3]float32

func (f fakeScene) pos(id uint32) (float32, float32, float32) {
	p := f[id]
	return p[0], p[1], p[2]
}

const (
	tCaster = 0x100
	tTarget = 0x200
)

func newTestEffect(scene fakeScene, lists [3][]esf.SpellEvent, secondary ...uint32) *SpellEffectDriver {
	d := newSpellEffectDriver(&esf.SpellEffect{EventLists: lists}, tCaster, tTarget, nil, scene.pos, nil, nil, nil)
	d.secondary = secondary
	d.Trigger(0, 10000, time.Time{})
	return d
}

func projectileEvent(flags, from, mode int32, speed float32) esf.SpellEvent {
	return esf.SpellEvent{EventType: 8, Fields: [4]int32{flags, -1, mode<<8 | from, 0}, Floats: [6]float32{speed}}
}

// A no-op event that only advances its driver.
var noopEvent = esf.SpellEvent{EventType: 7}

// driverFor finds the event driver playing on an actor. A caster list whose
// last event made the target drivers finishes in that same Process call and is
// gone, as on the PS2, so drivers are found by actor, not by position.
func driverFor(t *testing.T, d *SpellEffectDriver, actor uint32) *SpellEventDriver {
	t.Helper()
	for _, ed := range d.eventDrivers {
		if ed.actorID == actor && ed.listIndex != 0 {
			return ed
		}
	}
	t.Fatalf("no target driver on actor %#x among %d", actor, len(d.eventDrivers))
	return nil
}

// fly steps a driver's projectile at 30 Hz until it arrives; it returns the
// flight time.
func fly(t *testing.T, td *SpellEventDriver, limit time.Duration) time.Duration {
	t.Helper()
	start := td.projectileLastTick
	for k := 1; td.projectileActive; k++ {
		at := time.Duration(k) * time.Second / 30
		if at > limit {
			t.Fatalf("projectile still flying after %v at %v", limit, td.projectilePos)
		}
		td.processProjectile(start.Add(at))
	}
	return td.projectileLastTick.Sub(start)
}

// PlayTargetProjectile hands the projectile to a NEW target driver, so the
// caster's own list carries on while it flies and the target's waits for it.
func TestProjectileRidesTheTargetDriver(t *testing.T) {
	scene := fakeScene{tCaster: {0, 0, 0}, tTarget: {100, 0, 0}}
	d := newTestEffect(scene, [3][]esf.SpellEvent{
		{projectileEvent(1, 0, 0, 20), noopEvent, noopEvent},
		{noopEvent},
	})
	caster := d.eventDrivers[0]
	d.Process()
	if len(d.eventDrivers) != 2 {
		t.Fatalf("%d event drivers after the projectile event, want 2", len(d.eventDrivers))
	}
	td := d.eventDrivers[1]
	if td.actorID != tTarget || td.listIndex != 1 || !td.projectileActive || caster.projectileActive {
		t.Fatalf("projectile on the wrong driver: target actor=%#x list=%d active=%v, caster active=%v",
			td.actorID, td.listIndex, td.projectileActive, caster.projectileActive)
	}
	d.Process()
	d.Process()
	if caster.eventIndex != 3 {
		t.Fatalf("caster blocked behind its own projectile: eventIndex %d, want 3", caster.eventIndex)
	}
	if td.eventIndex != 0 {
		t.Fatalf("target list ran before the impact: eventIndex %d", td.eventIndex)
	}

	// It arrives on the step that stops bringing it closer — at the target,
	// not 3 units short (dist1 < 3 only bounds it): 100 units at 20/s is 5 s.
	got := fly(t, td, 10*time.Second)
	if got < 4950*time.Millisecond || got > 5100*time.Millisecond {
		t.Fatalf("flight took %v, want ~5 s at 20 units/s", got)
	}
	if td.Process(); td.eventIndex != 1 {
		t.Fatalf("target list did not run after the impact: eventIndex %d", td.eventIndex)
	}
}

// Bit 0 plays the Target list on the target, bit 1 the Area list on each
// secondary — one projectile each, and none on the caster.
func TestProjectilePerTargetAndSecondary(t *testing.T) {
	scene := fakeScene{tCaster: {0, 0, 0}, tTarget: {50, 0, 0}, 0x301: {0, 0, 50}, 0x302: {-50, 0, 0}}
	d := newTestEffect(scene, [3][]esf.SpellEvent{{projectileEvent(3, 0, 0, 20), noopEvent}}, 0x301, 0x302)
	d.Process()
	want := []struct {
		actor uint32
		list  int
	}{{tTarget, 1}, {0x301, 2}, {0x302, 2}}
	if len(d.eventDrivers) != 1+len(want) {
		t.Fatalf("%d event drivers, want %d", len(d.eventDrivers), 1+len(want))
	}
	for _, w := range want {
		td := driverFor(t, d, w.actor)
		if td.listIndex != w.list || !td.projectileActive || td.projectileSource != tCaster {
			t.Fatalf("actor %#x: list=%d projectile=%v source=%#x, want list %d with a projectile from the caster",
				w.actor, td.listIndex, td.projectileActive, td.projectileSource, w.list)
		}
	}
	if d.eventDrivers[0].projectileActive {
		t.Fatal("a projectile left on the caster driver")
	}
	// Bit 1 alone: the secondaries only.
	d = newTestEffect(scene, [3][]esf.SpellEvent{{projectileEvent(2, 0, 0, 20), noopEvent}}, 0x301)
	d.Process()
	if len(d.eventDrivers) != 2 || d.eventDrivers[1].actorID != 0x301 {
		t.Fatalf("bit 1 alone: %d drivers", len(d.eventDrivers))
	}
}

// The target leaving mid-flight retires the projectile (its actor is -1),
// where it used to head for the world origin a missing entity resolves to.
func TestProjectileTargetGoneRetires(t *testing.T) {
	scene := fakeScene{tCaster: {1000, 0, 1000}, tTarget: {1100, 0, 1000}}
	sys := &SpellEffectSystem{}
	d := newTestEffect(scene, [3][]esf.SpellEvent{{projectileEvent(1, 0, 0, 20)}})
	sys.drivers = append(sys.drivers, d)
	d.Process()
	td := driverFor(t, d, tTarget)
	td.processProjectile(td.projectileLastTick.Add(time.Second / 30))
	sys.NotifyActorWasDestroyed(tTarget)
	td.processProjectile(td.projectileLastTick.Add(time.Second / 30))
	if td.projectileActive {
		t.Fatalf("projectile still flying at a destroyed target, at %v", td.projectilePos)
	}
}

// Mode 3 lobs: the height at the middle of the flight is
// sin(pi/2) * (1/speed)^2 * total^2.
func TestProjectileLobHeight(t *testing.T) {
	scene := fakeScene{tCaster: {0, 0, 0}, tTarget: {100, 0, 0}}
	d := newTestEffect(scene, [3][]esf.SpellEvent{{projectileEvent(1, 0, 3, 20)}})
	d.Process()
	td := driverFor(t, d, tTarget)
	var peak float32
	start := td.projectileLastTick
	for k := 1; td.projectileActive; k++ {
		td.processProjectile(start.Add(time.Duration(k) * time.Second / 30))
		peak = max(peak, td.projectileDrawPos[1])
	}
	if want := float32(100 * 100 / (20 * 20)); math.Abs(float64(peak-want)) > 0.1 {
		t.Fatalf("lob peak %.3f, want %.3f", peak, want)
	}
}

// The bolt is held at full charge for Fields[2] ms, then decays (5/s) and the
// event ends once that charge is spent.
func TestLightningHeldThenDecays(t *testing.T) {
	scene := fakeScene{tCaster: {0, 0, 0}, tTarget: {10, 0, 0}}
	d := newTestEffect(scene, [3][]esf.SpellEvent{{{EventType: 13, Fields: [4]int32{1, 0, 1000, 0}}, noopEvent}})
	d.Process()
	td := driverFor(t, d, tTarget)
	if !td.lightningActive || td.lightningSource != tCaster || td.actorID != tTarget {
		t.Fatalf("bolt not on the target driver: active=%v source=%#x actor=%#x",
			td.lightningActive, td.lightningSource, td.actorID)
	}
	if d.eventDrivers[0].lightningActive {
		t.Fatal("bolt left on the caster driver too")
	}
	at := func(ms int) { td.processLightning(td.lightningStart.Add(time.Duration(ms) * time.Millisecond)) }
	at(900)
	if !td.lightningHeld || !td.lightningActive {
		t.Fatalf("at 900 ms of 1000: held=%v active=%v", td.lightningHeld, td.lightningActive)
	}
	at(1100)
	if td.lightningHeld || !td.lightningActive {
		t.Fatalf("at 1100 ms: held=%v active=%v, want decaying", td.lightningHeld, td.lightningActive)
	}
	at(1250)
	if td.lightningActive {
		t.Fatal("bolt still active 250 ms past its duration")
	}
}

// CLIENT stops an actor's effects the moment it dies.
func TestStopIfThisWasCaster(t *testing.T) {
	scene := fakeScene{tCaster: {0, 0, 0}, tTarget: {10, 0, 0}, 0x300: {5, 0, 0}}
	sys := &SpellEffectSystem{}
	a := newTestEffect(scene, [3][]esf.SpellEvent{{noopEvent, noopEvent}})
	b := newSpellEffectDriver(&esf.SpellEffect{EventLists: [3][]esf.SpellEvent{{noopEvent}}}, 0x300, tTarget, nil, scene.pos, nil, nil, nil)
	b.Trigger(0, 10000, time.Time{})
	sys.drivers = []*SpellEffectDriver{a, b}
	sys.StopIfThisWasCaster(tCaster)
	if len(sys.drivers) != 1 || sys.drivers[0] != b {
		t.Fatalf("%d drivers left, want only the other caster's", len(sys.drivers))
	}
}

// A scripted actor for the action and trigger callbacks.
type fakeActor struct {
	handles  int
	complete map[int]bool
	tripped  [4]bool
	resets   int
	csprite  bool
}

func (a *fakeActor) system() *SpellEffectSystem {
	return &SpellEffectSystem{
		OnSetAction: func(uint32, int32) int { a.handles++; return a.handles },
		OnPlayAttack: func(uint32, int32, [3]float32, int32) int {
			a.handles++
			return a.handles
		},
		IsActionComplete: func(_ uint32, h int) bool { return a.complete[h] },
		FindTrigger: func(_ uint32, name string) (int, bool) {
			for i, n := range []string{"Spell FX 1", "Spell FX 2", "Spell FX 3", "Spell FX 4"} {
				if n == name {
					return i, true
				}
			}
			return -1, false
		},
		ResetTrigger: func(_ uint32, i int) { a.tripped[i] = false; a.resets++ },
		HasTripped:   func(_ uint32, i int) bool { return a.tripped[i] },
		IsCSprite:    func(uint32) bool { return a.csprite },
	}
}

// NewSetAction keeps SetAction's handle for Wait 0 and arms "Spell FX 1-4"
// for Wait 3, as NewPlayAttack does: a cast's wait for its trigger releases on
// the keyframe, not on the timeout.
func TestSetActionArmsTriggers(t *testing.T) {
	a := &fakeActor{complete: map[int]bool{}, csprite: true}
	a.tripped = [4]bool{true, true, true, true} // stale from an earlier animation
	scene := fakeScene{tCaster: {0, 0, 0}, tTarget: {10, 0, 0}}
	d := newSpellEffectDriver(&esf.SpellEffect{EventLists: [3][]esf.SpellEvent{{
		{EventType: 2, Fields: [4]int32{39}},         // SetAction 39
		{EventType: 1, Fields: [4]int32{3, 1, 5000}}, // Wait 3: Spell FX 2, 5 s timeout
		{EventType: 1, Fields: [4]int32{0}},          // Wait 0: the action
		noopEvent,
	}}}, tCaster, tTarget, nil, scene.pos, nil, nil, a.system())
	d.Trigger(0, 10000, time.Time{})
	ed := d.eventDrivers[0]

	d.Process() // SetAction
	if ed.actionNodeID != 1 || a.resets != 4 || a.tripped != [4]bool{} {
		t.Fatalf("after SetAction: handle %d, %d resets, tripped %v — want handle 1 and all four reset",
			ed.actionNodeID, a.resets, a.tripped)
	}
	d.Process() // Wait 3
	d.Process()
	if ed.waitEvent == nil {
		t.Fatal("Wait 3 released before its trigger tripped")
	}
	a.tripped[1] = true
	d.Process() // releases Wait 3
	d.Process() // dispatches Wait 0
	d.Process()
	if ed.waitEvent == nil || ed.eventIndex != 3 {
		t.Fatalf("Wait 0 released while the action runs: wait=%v index=%d", ed.waitEvent != nil, ed.eventIndex)
	}
	a.complete[1] = true
	d.Process()
	d.Process()
	if ed.eventIndex != 4 {
		t.Fatalf("Wait 0 did not release when the action completed: index %d", ed.eventIndex)
	}

	// Not a VICSprite: no action and no triggers.
	b := &fakeActor{complete: map[int]bool{}}
	d = newSpellEffectDriver(&esf.SpellEffect{EventLists: [3][]esf.SpellEvent{{{EventType: 2, Fields: [4]int32{39}}, noopEvent}}},
		tCaster, tTarget, nil, scene.pos, nil, nil, b.system())
	d.Trigger(0, 10000, time.Time{})
	d.Process()
	if b.handles != 0 || d.eventDrivers[0].actionNodeID != -1 {
		t.Fatalf("SetAction on a non-CSprite: %d actions, handle %d", b.handles, d.eventDrivers[0].actionNodeID)
	}
}
