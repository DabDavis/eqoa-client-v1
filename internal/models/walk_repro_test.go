package models

import "testing"

// Walk must survive the per-frame ClearAction (was the "walk shows idle /
// sliding" bug: SetLocomotion set both pairs to walk, then ClearAction wiped
// the locomotion pair).
func TestWalkNotWipedByClearAction(t *testing.T) {
	c := NewSpriteAnimController(true) // humanoid
	c.SetLocomotion(1)                 // walk
	c.ClearAction()
	if loco, _ := c.Resolve(); loco != 1 {
		t.Fatalf("walk wiped by ClearAction: loco=%d, want 1", loco)
	}
}

// Other locomotion slots likewise. (Slot 0/idle is excluded: setAnimation
// intentionally rewrites idle→17 combat-idle while combatTimer<5s.)
func TestLocomotionSurvivesClearAction(t *testing.T) {
	for _, slot := range []int{1, 2, 3, 10, 12} { // walk, walkBack, run, strafe, swim
		c := NewSpriteAnimController(true)
		c.SetLocomotion(slot)
		c.ClearAction()
		if loco, _ := c.Resolve(); loco != slot {
			t.Fatalf("loco slot %d wiped: got %d", slot, loco)
		}
	}
}

// Regression: ClearAction must STILL reclaim pair 0 when it was driven by an
// action animation (SetAction drives both pairs), so a finished attack doesn't
// leave the legs frozen at the attack pose.
func TestActionStillReclaimsPair0(t *testing.T) {
	c := NewSpriteAnimController(true)
	c.SetAction(25) // slash mainhand — an action anim on both pairs
	c.ClearAction()
	loco, action := c.Resolve()
	if action != -1 {
		t.Fatalf("action not cleared: action=%d", action)
	}
	if loco == 25 {
		t.Fatalf("pair0 kept the action anim 25 (should be reclaimed to idle)")
	}
}
