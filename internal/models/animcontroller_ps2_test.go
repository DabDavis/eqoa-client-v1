package models

import "testing"

// The rules below are VICSprite::SetAnimation's (decomp/src/vicsprite.cpp),
// one test per branch of its check / special / rank ladder.

// Stopping on the move: the lower layer's walk (6) loses to idle (2) only by
// SetAnimation's lower-layer exception — idle or combat idle over anything
// below death. Without it the legs walked on after the player stopped.
func TestStoppingIdlesTheLegs(t *testing.T) {
	c := NewSpriteAnimController(true)
	c.SetLocomotion(1)
	c.SetLocomotion(0)
	if upper, lower := c.Resolve(); upper != 0 || lower != 0 {
		t.Fatalf("stopped: %d/%d, want 0/0", upper, lower)
	}
	// The same from a run, and into the combat idle.
	c.SetLocomotion(3)
	c.SetAction(23)
	c.MarkActionFinished()
	c.SetLocomotion(3)
	c.SetLocomotion(0)
	if upper, lower := c.Resolve(); upper != 17 || lower != 17 {
		t.Fatalf("stopped after a swing: %d/%d, want 17/17", upper, lower)
	}
}

// The exception is the lower layer's alone: a creature's one layer keeps a
// looping higher animation against idle.
func TestUpperLayerHasNoIdleException(t *testing.T) {
	c := NewSpriteAnimController(false)
	c.BindModel(1, map[int]bool{}) // every slot loops
	c.SetAction(22)
	c.SetLocomotion(0)
	if upper, _ := c.Resolve(); upper != 22 {
		t.Fatalf("idle replaced a looping attack on layer 0: %d", upper)
	}
}

// The special idle (priority 0) replaces idle (2) by its own exception, idle
// cannot cut it short while it plays, walking can, and once it has stopped
// idle takes over again.
func TestSpecialIdle(t *testing.T) {
	c := NewSpriteAnimController(true)
	c.SetLocomotion(0)
	c.SetLocomotion(15)
	if upper, lower := c.Resolve(); upper != 15 || lower != 15 {
		t.Fatalf("special idle over idle: %d/%d, want 15/15", upper, lower)
	}
	c.SetLocomotion(0)
	if upper, lower := c.Resolve(); upper != 15 || lower != 15 {
		t.Fatalf("idle cut the special idle short: %d/%d", upper, lower)
	}
	c.MarkActionFinished()
	c.SetLocomotion(0)
	if upper, lower := c.Resolve(); upper != 0 || lower != 0 {
		t.Fatalf("after the special idle: %d/%d, want 0/0", upper, lower)
	}
	c.SetLocomotion(15)
	c.SetLocomotion(1)
	if upper, lower := c.Resolve(); upper != 1 || lower != 1 {
		t.Fatalf("walk did not take over from the special idle: %d/%d", upper, lower)
	}
}

// A slot SetAnimPriorities does not list has priority 0, not a default.
func TestUnlistedSlotPriorityIsZero(t *testing.T) {
	for _, slot := range []int{19, 20, 21, 47, 48, 49, 54, 55, 58, 62} {
		if p0, p1 := animPriority(slot, LayerUpper), animPriority(slot, LayerLower); p0 != 0 || p1 != 0 {
			t.Fatalf("slot %d: priority %d/%d, want 0/0", slot, p0, p1)
		}
	}
	c := NewSpriteAnimController(true)
	c.SetLocomotion(1)
	c.SetAction(19)
	if upper, lower := c.Resolve(); upper != 1 || lower != 1 {
		t.Fatalf("slot 19 took a walk: %d/%d", upper, lower)
	}
}

// Play-once comes from the model's play list once bound.
func TestBoundSyncDecidesPlayOnce(t *testing.T) {
	c := NewSpriteAnimController(false)
	c.BindModel(7, map[int]bool{22: true})
	c.SetLocomotion(0)
	c.SetAction(22)
	if !c.IsActionPlayOnce() {
		t.Fatal("slot 22 bound as once does not play once")
	}
	c.SetLocomotion(1) // walk loses to a playing once-attack
	if upper, _ := c.Resolve(); upper != 22 {
		t.Fatalf("walk replaced a playing attack: %d", upper)
	}
	c.MarkActionFinished()
	c.SetLocomotion(1)
	if upper, _ := c.Resolve(); upper != 1 {
		t.Fatalf("walk did not replace the stopped attack: %d", upper)
	}
	if !c.BoundTo(7) || c.BoundTo(8) {
		t.Fatal("BoundTo")
	}
}

// With a blend running into the layer's play, a higher request takes over
// (freezing the blend), while an idle request is taken without changing
// anything — SetAnimation leaves the layer alone while m_prev is in use.
func TestBlendInProgress(t *testing.T) {
	c := NewSpriteAnimController(false)
	c.SetLocomotion(1)
	c.SetBlendActive(LayerUpper, true)

	h := c.SetAction(0) // idle (2) is not below walk (2), but is idle
	if upper, _ := c.Resolve(); upper != 1 {
		t.Fatalf("idle changed the layer mid-blend: %d", upper)
	}
	if c.IsActionComplete(h) {
		t.Fatal("the mid-blend idle request was not taken (m_action did not move)")
	}

	c.SetAction(23) // higher: freeze, take over
	if upper, _ := c.Resolve(); upper != 23 {
		t.Fatalf("an attack did not take over mid-blend: %d", upper)
	}
}

// The lower layer's own once-play is visible to the caller, which ends it
// by length (applySkeletal reports only the upper layer's end).
func TestLowerLayerOnceOnItsOwn(t *testing.T) {
	c := NewSpriteAnimController(true)
	c.SetLocomotion(1)
	c.SetAction(43) // kick: 5 over walk's 2 on top, 7 over walk's 6 below
	c.SetAction(25) // the next swing takes the top only (5 < 7 below)
	if upper, lower := c.Resolve(); upper != 25 || lower != 43 {
		t.Fatalf("kick then swing: %d/%d, want 25/43", upper, lower)
	}
	c.MarkActionFinished() // the swing's end says nothing about the legs
	c.SetLocomotion(1)
	if _, lower := c.Resolve(); lower != 43 {
		t.Fatalf("walk took the legs from a playing kick: %d", lower)
	}
	c.Update(0.5)
	slot, playing, age := c.LowerLayer()
	if slot != 43 || !playing || age != 0.5 {
		t.Fatalf("LowerLayer = %d %v %v", slot, playing, age)
	}
	c.MarkLowerFinished()
	c.SetLocomotion(1)
	if upper, lower := c.Resolve(); upper != 1 || lower != 1 {
		t.Fatalf("after the kick: %d/%d, want 1/1", upper, lower)
	}
}

// The battle-stance timer starts at the attack's acceptance, and a repeat of
// the same attack refreshes it even though it changes nothing.
func TestRepeatedAttackRefreshesCombatIdle(t *testing.T) {
	c := NewSpriteAnimController(false)
	c.SetAction(23)
	c.Update(4)
	c.SetAction(23) // already playing: SetAnimation's `done`, after m_idleTime
	c.MarkActionFinished()
	c.Update(4)
	c.SetLocomotion(0)
	if upper, _ := c.Resolve(); upper != 17 {
		t.Fatalf("8 s after the first swing, 4 after its repeat: %d, want 17", upper)
	}
}

func TestApplyDefaultEntries(t *testing.T) {
	m := &CharModel{
		Race: 0,
		PLSlotToRawPair: map[int][2]int{
			0:  {0, 1},
			22: {2, 3},
			23: {4, 5},
			41: {6, 7},
			14: {-1, -1}, // listed, but its animation did not resolve
		},
		PLSlotToRaw:    map[int]int{0: 0, 22: 2, 23: 4, 41: 6},
		PLSlotSpeed:    map[int]float32{0: -0.8, 22: 1.1, 23: 1.2, 41: 2.0, 14: 1},
		PLSlotPlayOnce: map[int]bool{0: false, 22: true, 23: true, 41: true},
		PLSlotSound:    map[int]PLSlotSoundEntry{41: {SoundDictID: [2]int32{9, 9}}},
	}
	ApplyDefaultEntries(m)
	want := map[int][2]int{
		1: {0, 1}, 14: {0, 1}, 17: {0, 1}, 37: {0, 1}, // from idle
		7: {0, 1}, 34: {0, 1}, 56: {0, 1}, // from idle, once
		25: {4, 5}, 35: {4, 5}, 43: {4, 5}, 50: {4, 5}, // from 23
		24: {2, 3}, 32: {2, 3}, 36: {2, 3}, 40: {2, 3}, // from 22
		65: {6, 7}, // from 41
	}
	for slot, pair := range want {
		if got, ok := m.PLSlotToRawPair[slot]; !ok || got != pair {
			t.Errorf("slot %d: %v %v, want %v", slot, got, ok, pair)
		}
	}
	for _, slot := range []int{19, 47, 58, 62} {
		if _, ok := m.PLSlotToRawPair[slot]; ok {
			t.Errorf("slot %d was filled; SetDefaultEntries has no case for it", slot)
		}
	}
	if m.PLSlotPlayOnce[1] || !m.PLSlotPlayOnce[7] || !m.PLSlotPlayOnce[34] || !m.PLSlotPlayOnce[25] {
		t.Errorf("sync: 1=%v 7=%v 34=%v 25=%v, want false true true true",
			m.PLSlotPlayOnce[1], m.PLSlotPlayOnce[7], m.PLSlotPlayOnce[34], m.PLSlotPlayOnce[25])
	}
	if m.PLSlotSpeed[65] != 1.0 || m.PLSlotSpeed[1] != -0.8 || m.PLSlotSpeed[25] != 1.2 {
		t.Errorf("weights: 65=%v 1=%v 25=%v, want 1.0 -0.8 1.2", m.PLSlotSpeed[65], m.PLSlotSpeed[1], m.PLSlotSpeed[25])
	}
	if _, ok := m.PLSlotSound[65]; ok {
		t.Error("65 kept 41's sounds; SetDefaultEntries clears them")
	}
	if raw, ok := m.PLSlotToRaw[25]; !ok || raw != 4 {
		t.Errorf("PLSlotToRaw[25] = %d %v, want 4", raw, ok)
	}

	// Idempotent.
	before := len(m.PLSlotToRawPair)
	ApplyDefaultEntries(m)
	if len(m.PLSlotToRawPair) != before || m.PLSlotSpeed[65] != 1.0 {
		t.Error("a second call changed the model")
	}

	// An ogre's 65 is idle played once, not 41.
	o := &CharModel{
		Race:            9,
		PLSlotToRawPair: map[int][2]int{0: {0, 1}, 41: {6, 7}},
		PLSlotPlayOnce:  map[int]bool{},
	}
	ApplyDefaultEntries(o)
	if o.PLSlotToRawPair[65] != [2]int{0, 1} || !o.PLSlotPlayOnce[65] {
		t.Errorf("ogre 65: %v once=%v, want idle once", o.PLSlotToRawPair[65], o.PLSlotPlayOnce[65])
	}

	// No 22: the off-hand family falls to 23.
	n := &CharModel{PLSlotToRawPair: map[int][2]int{0: {0, 1}, 23: {4, 5}}}
	ApplyDefaultEntries(n)
	if n.PLSlotToRawPair[24] != [2]int{4, 5} || n.PLSlotToRawPair[22] != [2]int{4, 5} {
		t.Errorf("no 22: 24=%v 22=%v, want 23's", n.PLSlotToRawPair[24], n.PLSlotToRawPair[22])
	}
}
