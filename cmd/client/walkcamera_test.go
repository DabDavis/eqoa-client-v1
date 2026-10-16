package main

import (
	"math"
	"testing"

	"github.com/eqoa/client/internal/game"
)

// newCameraTestClient is a client with a player and no scene: every camera
// sweep is clear.
func newCameraTestClient(race byte) *Client {
	c := &Client{
		engine:     game.NewEngine("", 0, 0, 0, "test", ""),
		camMaxDist: 8,
	}
	p := &c.engine.State.Player
	p.Race = race
	p.X, p.Y, p.Z = 100, 10, 200
	c.engine.State.Session.ExpansionFlags = 0xFFFFFFFF // every server sends -1
	return c
}

func near(a, b, eps float32) bool { return float32(math.Abs(float64(a-b))) <= eps }

func normAngle(a float32) float32 {
	for a < 0 {
		a += 2 * math.Pi
	}
	for a >= 2*math.Pi {
		a -= 2 * math.Pi
	}
	return a
}

// TestCameraWorldEntry: the first Msg000D gives ToggleCameraStyle's camera,
// style 3, 3.38 from the head at 15°, exactly behind the heading, looking at
// the race's eye height.
func TestCameraWorldEntry(t *testing.T) {
	c := newCameraTestClient(3) // gnome: eye height 1.35
	p := &c.engine.State.Player
	p.Facing = 0.7
	c.cameraWorldEntry()
	w := &c.wcam
	if w.mode != camModeThird || c.firstPerson {
		t.Fatalf("mode %d, want 3", w.mode)
	}
	if !near(c.camDist, 3.38, 1e-6) || !near(c.camPitch, 0.2617994, 1e-6) {
		t.Errorf("dist %v pitch %v, want 3.38 and 15°", c.camDist, c.camPitch)
	}
	if !near(normAngle(c.camAngle), normAngle(behindAngle(p.Facing)), 1e-5) {
		t.Errorf("angle %v, want behind the heading %v", c.camAngle, normAngle(behindAngle(p.Facing)))
	}
	if w.lookAtHeightAbove != 1.35 || w.targetHeight != 1.68 || w.maxOrbitDist != 8 || w.minCollisionDist != 3.5 {
		t.Errorf("style 3 parameters: %+v", w)
	}
	if w.turnRate != 6.2831855 || w.minPitch != -0.7853982 || w.maxPitch != 1.5607964 {
		t.Errorf("walker: turn %v pitch range %v..%v", w.turnRate, w.minPitch, w.maxPitch)
	}
	// The eye is behind the player: against the heading's forward vector.
	fx, fz := float32(math.Sin(float64(p.Facing))), float32(math.Cos(float64(p.Facing)))
	dx, dz := w.camPos[0]-p.X, w.camPos[2]-p.Z
	if dx*fx+dz*fz >= 0 {
		t.Errorf("eye (%v, %v) from the player is not behind the heading (%v, %v)", dx, dz, fx, fz)
	}
	if want := p.Y + 1.68 + 3.38*float32(math.Sin(0.2617994)); !near(w.camPos[1], want, 1e-4) {
		t.Errorf("eye height %v, want %v", w.camPos[1], want)
	}
	cam := c.walkCameraView(*p)
	if !near(cam.Target.Y, p.Y+1.35, 1e-5) {
		t.Errorf("look-at y %v, want the gnome's eye %v", cam.Target.Y, p.Y+1.35)
	}
	if c.wcam.saved.mode != camModeThird {
		t.Errorf("m_camSaved not saved")
	}

	// A later world entry restores the saved style, at 3.38 again.
	c.toggleFirstPerson()
	c.setOrbitDistance(7)
	c.toggleFirstPerson() // back to 3: SetOrbitDistance(3.38)
	c.setOrbitDistance(7)
	c.cameraWorldEntry()
	if w.mode != camModeThird || !near(c.camDist, 3.38, 1e-6) {
		t.Errorf("second entry: mode %d dist %v", w.mode, c.camDist)
	}
}

// TestToggleFirstPerson is InitCameraStyle: third ⇄ first, and the option
// picks the fixed follow for third.
func TestToggleFirstPerson(t *testing.T) {
	c := newCameraTestClient(5) // troll: eye 1.7
	c.cameraWorldEntry()
	c.toggleFirstPerson()
	w := &c.wcam
	if w.mode != camModeFirst || !c.firstPerson || w.lookAtHeight != 1.7 || w.turnRate != 1.5707964 {
		t.Fatalf("first person: mode %d eye %v turn %v", w.mode, w.lookAtHeight, w.turnRate)
	}
	if w.minPitch != -1.5607964 || w.maxPitch != 1.5607964 {
		t.Errorf("first person pitch range %v..%v, want ±1.5607964", w.minPitch, w.maxPitch)
	}
	c.camFixedFollow = true
	c.toggleFirstPerson()
	if w.mode != camModeFixed || !near(c.camDist, 3.38, 1e-6) || w.maxOrbitDist != 25 {
		t.Errorf("fixed follow: mode %d dist %v max %v", w.mode, c.camDist, w.maxOrbitDist)
	}
	// Changing the option back switches the style (ApplyOptions).
	c.camFixedFollow = false
	c.applyCameraOptions()
	if w.mode != camModeThird {
		t.Errorf("after the option: mode %d, want 3", w.mode)
	}
}

// TestWalkerTurnRate: under a constant full turn the walker's angular
// velocity settles at the style's turn rate, the terminal speed
// InitIntermediate's acceleration is built for.
func TestWalkerTurnRate(t *testing.T) {
	for _, tc := range []struct {
		style int
		rate  float32
	}{{0, 1.5707964}, {2, 1.5707964}, {3, 6.2831855}} {
		c := newCameraTestClient(0)
		c.cameraWorldEntry()
		c.setCameraStyle(tc.style)
		c.wcam.tracking = false // a free turn, no target to snap to
		for i := 0; i < 200; i++ {
			c.turnLeft(1)
			c.stepWalkerAngles(0.032)
		}
		if !near(c.wcam.turnVel, tc.rate, tc.rate*1e-3) {
			t.Errorf("style %d: angular velocity %v, want %v", tc.style, c.wcam.turnVel, tc.rate)
		}
	}
}

// TestThirdPersonTurnSnaps: style 3's bang-bang turn reaches a target behind
// the player and stops on it (the overshoot detector), in well under a second.
func TestThirdPersonTurnSnaps(t *testing.T) {
	c := newCameraTestClient(0)
	p := &c.engine.State.Player
	p.Facing = 0
	c.cameraWorldEntry()
	target := float32(math.Pi - 0.1)
	var elapsed float32
	for i := 0; i < 100 && p.Facing != target; i++ {
		c.steerWalker(1.0/30, true, target)
		elapsed += 1.0 / 30
	}
	if p.Facing != target || c.wcam.turnVel != 0 {
		t.Fatalf("facing %v vel %v after %.2fs, want snapped to %v", p.Facing, c.wcam.turnVel, elapsed, target)
	}
	if elapsed > 1 {
		t.Errorf("a 174° turn took %.2fs", elapsed)
	}
	t.Logf("a 174° turn at 2π rad/s: %.2f s", elapsed)
}

// TestZoomAndPitchSnap: the zoom settles at 11.75 u/s and stops at the
// ceiling; style 3's pitch snaps back to 15° at π rad/s when nothing blocks.
func TestZoomAndPitchSnap(t *testing.T) {
	c := newCameraTestClient(0)
	c.cameraWorldEntry()
	w := &c.wcam
	for i := 0; i < 60; i++ {
		w.inZoom = -1 // zoom out
		c.processWalkCameraStep(0.032)
	}
	if c.camDist != 8 {
		t.Errorf("zoomed out to %v, want the style 3 ceiling 8", c.camDist)
	}
	c.camPitch = 1.0
	c.processWalkCameraStep(0.032)
	if w.flags&8 == 0 {
		t.Fatalf("pitch 1.0 with a clear sweep: no snap set")
	}
	for i := 0; i < 20; i++ {
		c.processWalkCameraStep(0.032)
	}
	if c.camPitch != w.defOrbitPitch || w.flags&8 != 0 {
		t.Errorf("pitch %v flags %x, want snapped to %v", c.camPitch, w.flags, w.defOrbitPitch)
	}
	// Free pitch (a Go extra) leaves it.
	c.camFreePitch = true
	c.camPitch = 1.0
	for i := 0; i < 20; i++ {
		c.processWalkCameraStep(0.032)
	}
	if c.camPitch != 1.0 {
		t.Errorf("free pitch: %v, want 1.0 kept", c.camPitch)
	}
}

// TestDragCamera: in style 3 the orbit angle follows where the eye was, so a
// walker running sideways pulls the camera round behind it.
func TestDragCamera(t *testing.T) {
	c := newCameraTestClient(0)
	p := &c.engine.State.Player
	p.Facing = 0 // facing +Z; the eye is at −Z
	c.cameraWorldEntry()
	start := normAngle(c.camAngle)
	for i := 0; i < 90; i++ {
		p.X += 8.8 / 30 // running toward +X for 3 s
		c.processWalkCameraStep(1.0 / 30)
	}
	// The eye trails toward −X: its angle turns toward π (Go convention).
	dx, dz := c.wcam.camPos[0]-p.X, c.wcam.camPos[2]-p.Z
	if !(dx < -2) {
		t.Errorf("eye offset (%v, %v): the camera did not trail behind the run (angle %v -> %v)", dx, dz, start, c.camAngle)
	}
	if d := float32(math.Hypot(float64(dx), float64(dz))); !near(d, 3.38*float32(math.Cos(0.2617994)), 1e-3) {
		t.Errorf("flat distance %v, want the orbit's", d)
	}
}

// TestApplyRaceCamera: the panel camera is the race's World setting in mode
// 1, in front of the player, and ApplyDefaultCamera puts back what was saved.
func TestApplyRaceCamera(t *testing.T) {
	c := newCameraTestClient(5) // troll: World {0.65, 0.65, 10°, 5.9}
	p := &c.engine.State.Player
	p.Facing = 1.0
	c.cameraWorldEntry()
	before := c.wcam.saved
	c.saveCameraPreset(&before)
	c.applyRaceCamera()
	w := &c.wcam
	if w.mode != camModeOrbit || w.targetHeight != 0.65 || w.lookAtHeightAbove != 0.65 ||
		c.camPitch != 0.17453294 || c.camDist != 5.9 {
		t.Fatalf("panel camera: mode %d %+v dist %v pitch %v", w.mode, w, c.camDist, c.camPitch)
	}
	if !near(normAngle(c.camAngle), normAngle(math.Pi/2-p.Facing), 1e-5) {
		t.Errorf("angle %v, want in front %v", normAngle(c.camAngle), normAngle(math.Pi/2-p.Facing))
	}
	c.applyDefaultCamera()
	if w.mode != before.mode || c.camDist != before.dist || c.camPitch != before.pitch || w.targetHeight != before.targetHeight {
		t.Errorf("restored %+v (dist %v pitch %v), want %+v", w, c.camDist, c.camPitch, before)
	}
}

// TestDeathCamera: ResetCamera keeps the camera's place, a first-person one
// first becoming style 2; DeathCamera backs away and rises; the next world
// entry ends it.
func TestDeathCamera(t *testing.T) {
	c := newCameraTestClient(0)
	p := &c.engine.State.Player
	c.cameraWorldEntry()
	c.toggleFirstPerson()
	p.Dead = true
	c.resetCamera()
	w := &c.wcam
	if !w.dead || w.mode != camModeFixed {
		t.Fatalf("dead %v mode %d, want the fixed follow", w.dead, w.mode)
	}
	y0, d0 := w.deathPos[1], w.deathDist
	var cam = w.deathView
	for i := 0; i < 150; i++ {
		cam = c.deathCamera(1.0 / 30)
	}
	if !(w.deathPos[1] > y0) || !(w.deathT > 4.9) {
		t.Errorf("after 5 s: eye y %v (from %v), t %v", w.deathPos[1], y0, w.deathT)
	}
	dx, dz := cam.Position.X-p.X, cam.Position.Z-p.Z
	if d := float32(math.Hypot(float64(dx), float64(dz))); !(d > d0) {
		t.Errorf("the camera did not back away: %v from %v", d, d0)
	}
	p.Dead = false
	c.cameraWorldEntry()
	if w.dead {
		t.Errorf("world entry left the death camera on")
	}
}
