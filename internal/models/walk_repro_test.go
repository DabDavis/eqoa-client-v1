package models

import "testing"

// Walk must survive the per-frame ClearAction (was the "walk shows idle /
// sliding" bug: clearing the action wiped the locomotion it shared a layer
// with).
func TestWalkNotWipedByClearAction(t *testing.T) {
	c := NewSpriteAnimController(true) // humanoid
	c.SetLocomotion(1)                 // walk
	c.ClearAction()
	if upper, lower := c.Resolve(); upper != 1 || lower != 1 {
		t.Fatalf("walk wiped by ClearAction: upper=%d lower=%d, want 1/1", upper, lower)
	}
}

// Other locomotion slots likewise.
func TestLocomotionSurvivesClearAction(t *testing.T) {
	for _, slot := range []int{0, 1, 2, 3, 10, 12} { // idle, walk, walkBack, run, strafe, swim
		c := NewSpriteAnimController(true)
		c.SetLocomotion(slot)
		c.ClearAction()
		if upper, lower := c.Resolve(); upper != slot || lower != slot {
			t.Fatalf("loco slot %d wiped: got %d/%d", slot, upper, lower)
		}
	}
}

// ClearAction must still end an action holding the layers, so a finished
// attack doesn't leave the body frozen at the attack pose.
func TestClearActionReclaimsActionLayers(t *testing.T) {
	c := NewSpriteAnimController(true)
	c.SetAction(25) // slash mainhand
	c.ClearAction()
	if c.ActionAnimID() != -1 {
		t.Fatalf("action not cleared: %d", c.ActionAnimID())
	}
	c.SetLocomotion(1)
	if upper, lower := c.Resolve(); upper != 1 || lower != 1 {
		t.Fatalf("after the action, walk did not take both layers: %d/%d", upper, lower)
	}
}

// An attack on the move takes the upper layer only (5 beats walk's 2 there,
// but not walk's 6 on the lower layer): the arms swing, the legs keep walking.
func TestAttackWhileWalkingSplits(t *testing.T) {
	c := NewSpriteAnimController(true)
	c.SetLocomotion(1)
	c.SetAction(25)
	if upper, lower := c.Resolve(); upper != 25 || lower != 1 {
		t.Fatalf("walk+attack: upper=%d lower=%d, want 25/1", upper, lower)
	}
	// The walk keeps being requested every frame and must not take the arms
	// back until the swing has finished.
	c.SetLocomotion(1)
	if upper, _ := c.Resolve(); upper != 25 {
		t.Fatalf("walk took the arms back mid-swing: upper=%d", upper)
	}
	c.MarkActionFinished()
	c.SetLocomotion(1)
	if upper, lower := c.Resolve(); upper != 1 || lower != 1 {
		t.Fatalf("after the swing: %d/%d, want 1/1", upper, lower)
	}
}

// Standing still, the attack takes both layers and plays full-body.
func TestAttackWhileIdleIsFullBody(t *testing.T) {
	c := NewSpriteAnimController(true)
	c.SetLocomotion(0)
	c.SetAction(25)
	if upper, lower := c.Resolve(); upper != 25 || lower != 25 {
		t.Fatalf("idle+attack: %d/%d, want 25/25", upper, lower)
	}
}

// A dead entity plays slot 14, whatever its skeleton and whatever is asked.
func TestDeathIsSlot14ForEverySkeleton(t *testing.T) {
	for _, humanoid := range []bool{true, false} {
		c := NewSpriteAnimController(humanoid)
		c.SetLocomotion(1)
		c.IsDead = true
		c.SetLocomotion(1)
		c.SetAction(22)
		if upper, lower := c.Resolve(); upper != 14 || lower != 14 {
			t.Fatalf("humanoid=%v dead: %d/%d, want 14/14", humanoid, upper, lower)
		}
	}
}

// A creature plays actions on its one layer, competing with locomotion.
func TestCreatureActionOnLayer0(t *testing.T) {
	c := NewSpriteAnimController(false)
	c.SetLocomotion(1)
	c.SetAction(22)
	if upper, lower := c.Resolve(); upper != 22 || lower != 22 {
		t.Fatalf("creature attack: %d/%d, want 22/22", upper, lower)
	}
}

// Idle only becomes the combat idle (17) within 5 s of an attack — never at
// spawn.
func TestCombatIdleOnlyAfterAnAttack(t *testing.T) {
	c := NewSpriteAnimController(true)
	c.SetLocomotion(0)
	if upper, _ := c.Resolve(); upper != 0 {
		t.Fatalf("fresh controller plays %d for idle, want 0", upper)
	}
	c.SetAction(22)
	c.MarkActionFinished()
	c.SetLocomotion(0)
	if upper, lower := c.Resolve(); upper != 17 || lower != 17 {
		t.Fatalf("idle right after an attack: %d/%d, want 17/17", upper, lower)
	}
	c.Update(5.1)
	c.SetLocomotion(0)
	if upper, _ := c.Resolve(); upper != 0 {
		t.Fatalf("idle 5 s after the attack: %d, want 0", upper)
	}
}

// SetAction hands back a handle and IsActionComplete(handle) follows
// VICSprite's m_action: running until layer 0 stops, complete at once when
// the request was rejected, and complete once anything later takes layer 0.
func TestActionHandles(t *testing.T) {
	c := NewSpriteAnimController(true)
	c.SetLocomotion(0)
	h := c.SetAction(25)
	if c.IsActionComplete(h) {
		t.Fatal("a swing that just started is complete")
	}
	again := c.SetAction(25) // same animation: rejected, its own handle
	if again == h || !c.IsActionComplete(again) {
		t.Fatalf("a rejected request: handle %d (first %d), complete=%v, want a new, complete handle",
			again, h, c.IsActionComplete(again))
	}
	if c.IsActionComplete(h) {
		t.Fatal("the rejected request ended the swing it lost to")
	}
	c.SetLocomotion(0) // idle cannot replace a swing still playing
	if c.IsActionComplete(h) {
		t.Fatal("idle took layer 0 from a playing swing")
	}
	c.MarkActionFinished()
	if !c.IsActionComplete(h) {
		t.Fatal("a stopped swing is not complete")
	}

	// A finished action replaced by locomotion is complete by sequence.
	h = c.SetAction(22)
	c.MarkActionFinished()
	c.SetLocomotion(1)
	if !c.IsActionComplete(h) || c.ActionAnimID() != -1 {
		t.Fatalf("after walk took layer 0: complete=%v action=%d", c.IsActionComplete(h), c.ActionAnimID())
	}

	// ClearAction empties layer 0, which counts as stopped.
	h = c.SetAction(25)
	c.ClearAction()
	if !c.IsActionComplete(h) {
		t.Fatal("a cleared action is not complete")
	}
}
