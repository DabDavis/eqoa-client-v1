package main

import (
	"log"
	"math"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/eqoa/client/internal/game"
	"github.com/eqoa/client/internal/terrain"
)

// The walk camera: SUPPORT's VIWalkCamera (decomp/src/viwalkcamera.cpp,
// 0x00482020..0x00483EB8, every method byte-exact) and the walker half of it
// the camera styles touch (VIWalkControl's turn and pitch), with CLIENT's camera
// styles and presets over it (VIClient::SetCameraStyle 0x00BD8B40,
// InitCameraStyle 0x00BD5E38, ToggleCameraStyle 0x00BD58C8,
// ApplyRaceCamera 0x00BC7A80, ApplyDefaultCamera 0x00BC7B38,
// Save/ApplyCameraPreset 0x00BD5940/0x00BD5978, ResetCamera 0x00BC5F78, all
// byte-exact; DeathCamera 0x00BC6118 written; the pad mapping off
// func_00BC3AE8's disassembly). claude-notes/go-client-egress-pass-20261016/camera.md
// has the table.
//
// Conventions. The orbit angle is kept in Go's form, c.camAngle: the eye sits
// dist·(cos p·cos A, sin p, cos p·sin A) from the orbit centre. The PS2's
// orbit angle a (the eye on VIVect3::HPR(a, −p)) is A = π/2 − a. The walker's
// heading h is Go's Player.Facing (forward = (sin h, 0, cos h), as HPR's), so
// the PS2's "behind", a = h + π, is A = −π/2 − h. A pitch is the PS2's in
// both: positive puts the orbit eye above the centre and turns a look down.
//
// What is not here: VICameraEffect's manual camera (BeginManualCamera, a
// look-around the game window starts 300 ms after a target cycle while it is
// modal; Go has no modal target window) and the height offset a step-up adds
// to the walker's render position (Go's floor snaps). The shake is the spell
// system's (render.go).

// The walk camera's modes, VIWalkCamera::m_cameraMode (+0x27C).
const (
	camModeFirst = 0 // first person: the eye at the race's height, along the walker's heading and pitch
	camModeOrbit = 1 // a free orbit with no correction (ApplyRaceCamera's, in front)
	camModeFixed = 2 // fixed follow: 3.38 behind the head, looking where the walker looks
	camModeThird = 3 // third person: the drag orbit, the pitch snap and the wall swing
)

// Constants VIWalkCamera::Init and VIWalkControl::InitParms set and no style
// changes.
const (
	wcTickMS      = 32        // VIWalkControl::m_tickMS: the physics step
	wcTickAltMS   = 200       // m_tickAltMS: a longer gap is skipped, not stepped
	wcDamping     = 0.01      // m_angleDamping, m_distDamping, m_turnDamping, m_pitchDamping
	wcOrbitRate   = 1.5707964 // m_defOrbitAngle: the orbit's full-stick speed, rad/s
	wcZoomRate    = 11.75     // m_defOrbitDistance: the zoom's full-stick speed, units/s
	wcPitchRate   = 0.7853982 // m_pitchRate: the walker's look speed, rad/s
	wcPitchClamp  = 1.5607964 // SetPitchRange's clamp, 89.43°
	wcAltPitch    = 1.0471976 // m_altOrbitPitch, 60°
	wcCollRadius  = 0.5       // m_collisionRadius
	wcSnapSpeed   = 3.1415927 // m_snapSpeed, rad/s
	wcDistSnap    = 23.5      // m_distSnapSpeed, units/s
	wcHideFixedAt = 0.8       // func_00BC6500: style 2 hides the player when the eye is this close to the head
)

// cameraPreset is VICameraPreset: the camera as SaveCameraPreset reads it.
type cameraPreset struct {
	mode              int
	targetHeight      float32
	lookAtHeightAbove float32
	pitch             float32
	dist              float32
	angle             float32 // Go convention
}

// walkCamera holds the VIWalkCamera state beside c.camAngle, c.camPitch and
// c.camDist (the orbit angle, pitch and distance).
type walkCamera struct {
	ready bool

	mode              int     // m_cameraMode
	targetHeight      float32 // m_targetHeight: the orbit centre above the feet
	lookAtHeightAbove float32 // m_lookAtHeightAbove: what modes 1 and 3 look at, above the feet
	lookAtHeight      float32 // m_lookAtHeight: first person's eye above the feet
	minOrbitDist      float32 // m_minOrbitDistance
	maxOrbitDist      float32 // m_maxOrbitDistance
	minCollisionDist  float32 // m_minCollisionDist
	defOrbitPitch     float32 // m_defOrbitPitch
	autoOrbit         bool    // m_autoOrbit: modes 0-2 pull the eye in to what the sweep hit

	// The orbit body, Go convention (the PS2's m_cur; m_last is the same
	// after every step).
	angleVel, angleForce float32
	distVel, distForce   float32
	camPos               [3]float32 // m_cameraPos
	snapAngle, snapPitch float32
	flags                uint32 // 1 snap the angle, 2 zoom to the minimum, 4 zoom in, 8 snap the pitch

	// The walker half: what SetCameraStyle sets on VIWalkControl.
	turnRate          float32 // m_turnRate: 2π in style 3, π/2 in the others
	minPitch          float32
	maxPitch          float32
	pitch             float32 // m_phys.m_angles.y: the walker's look pitch (modes 0 and 2)
	turnVel, turnTq   float32 // m_angularVel.x, m_torque.x
	pitchVel, pitchTq float32 // .y
	targetHeading     float32 // m_targetHeading
	tracking          bool    // flag 0x100: SetCameraMode(3) turns heading tracking on

	// This frame's inputs, as func_00BC3AE8 hands them over (−1..1).
	inOrbit, inZoom, inTurn, inPitch, inFreePitch float32

	// VIClient's.
	preset   cameraPreset // m_camPreset (+0x6D90): ApplyRaceCamera saves into it
	saved    cameraPreset // m_camSaved (+0x6DA8): the style last chosen
	entered  bool         // +0x2BD50: set by the first world entry
	panelCam bool         // Go: ApplyRaceCamera's camera is up

	// ResetCamera's record and DeathCamera's clock (m_pC1B0, +0xC1B4..+0xC1D4).
	dead      bool
	deathPos  [3]float32
	deathDir  [3]float32
	deathDist float32
	deathT    float32
	deathYaw  float32
	deathView rl.Camera3D
}

func pow32(b, e float32) float32  { return float32(math.Pow(float64(b), float64(e))) }
func sin32(x float32) float32     { return float32(math.Sin(float64(x))) }
func cos32(x float32) float32     { return float32(math.Cos(float64(x))) }
func fmod32(x, y float32) float32 { return float32(math.Mod(float64(x), float64(y))) }

func clamp32(v, lo, hi float32) float32 {
	return min(hi, max(lo, v))
}

// accelFor is the shape every InitIntermediate uses: the force under which a
// body damped by wcDamping per second settles at rate, at the 32 ms tick.
func accelFor(rate float32) float32 {
	dt := float32(wcTickMS) * 0.001
	d := pow32(wcDamping, dt)
	return (rate - rate*d) / (d * dt)
}

// orbitDir is VIVect3::HPR(a, −p) in Go convention: the eye's direction from
// the orbit centre.
func orbitDir(A, p float32) [3]float32 {
	return [3]float32{cos32(p) * cos32(A), sin32(p), cos32(p) * sin32(A)}
}

// lookDir is VIVect3::HPR(h, p): along the walker's heading and pitch.
func lookDir(h, p float32) [3]float32 {
	return [3]float32{sin32(h) * cos32(p), -sin32(p), cos32(h) * cos32(p)}
}

// behindAngle is the orbit angle behind heading h (SetOrbitAngleBehind's
// heading + π), Go convention.
func behindAngle(h float32) float32 { return -math.Pi/2 - h }

// initWalkCamera is VIWalkCamera::Init's constants (0x00482090) and
// InitParms' walker values, before any style.
func (c *Client) initWalkCamera() {
	w := &c.wcam
	w.ready = true
	w.lookAtHeight = 1.68
	w.autoOrbit = true
	w.minOrbitDist = 1.5
	w.mode = camModeFirst
	w.maxOrbitDist = 25.0
	w.minCollisionDist = 3.0
	w.defOrbitPitch = 0.5235988
	w.targetHeight = 1.1
	w.lookAtHeightAbove = 1.68
	w.turnRate = 1.5707964
	w.minPitch, w.maxPitch = -wcPitchClamp, wcPitchClamp
	c.camDist = 1.5 // m_last.m_dist
	c.camPitch = w.defOrbitPitch
}

func (c *Client) ensureWalkCamera() {
	if !c.wcam.ready {
		c.initWalkCamera()
	}
}

// player is the walker.
func (c *Client) walker() *game.Entity {
	if c.engine == nil || c.engine.State == nil {
		return nil
	}
	return &c.engine.State.Player
}

// --- the walk camera's own methods -----------------------------------------

// setPitchRange is VIWalkControl::SetPitchRange (0x00484C30 [V]).
func (c *Client) setPitchRange(lo, hi float32) {
	w := &c.wcam
	w.minPitch = clamp32(lo, -wcPitchClamp, wcPitchClamp)
	w.maxPitch = clamp32(hi, -wcPitchClamp, wcPitchClamp)
	w.pitch = clamp32(w.pitch, w.minPitch, w.maxPitch)
}

// setCameraMode is VIWalkCamera::SetCameraMode (0x00482368 [V]): mode 3 alone
// tracks a target heading.
func (c *Client) setCameraMode(mode int) {
	w := &c.wcam
	w.mode = mode
	w.tracking = mode == camModeThird
	if w.tracking {
		if p := c.walker(); p != nil {
			w.targetHeading = p.Facing
		}
	}
	c.firstPerson = mode == camModeFirst
}

// setOrbitDistance is VIWalkCamera::SetOrbitDistance (0x004830B8 [V]).
func (c *Client) setOrbitDistance(d float32) {
	c.camDist = clamp32(d, c.wcam.minOrbitDist, c.wcam.maxOrbitDist)
	c.wcam.distVel, c.wcam.distForce = 0, 0
}

// setOrbitPitch is SetOrbitPitchAngle (0x004830A8 [V]); setDefOrbitPitch is
// SetDefOrbitPitchAngle (0x00483090 [V]).
func (c *Client) setOrbitPitch(p float32) { c.camPitch = p }
func (c *Client) setDefOrbitPitch(p float32) {
	c.wcam.defOrbitPitch = p
	c.camPitch = p
}

// setOrbitAngle is VIWalkCamera::SetOrbitAngle (0x00483208 [V]): in mode 3
// the eye goes straight to where the angle puts it.
func (c *Client) setOrbitAngle(A float32) {
	c.camAngle = fmod32(A, 2*math.Pi)
	if c.wcam.mode == camModeThird {
		if p := c.walker(); p != nil {
			d := orbitDir(c.camAngle, c.camPitch)
			c.wcam.camPos = [3]float32{
				p.X + d[0]*c.camDist,
				p.Y + c.wcam.targetHeight + d[1]*c.camDist,
				p.Z + d[2]*c.camDist,
			}
		}
	}
}

// setOrbitAngleBehind is SetOrbitAngleBehind (0x00483330 [V]).
func (c *Client) setOrbitAngleBehind() {
	if p := c.walker(); p != nil {
		c.setOrbitAngle(behindAngle(p.Facing))
	}
}

// halt is VIWalkCamera::Halt (0x00483130 [V]): the walker and the orbit stop.
func (c *Client) halt() {
	w := &c.wcam
	w.turnVel, w.turnTq, w.pitchVel, w.pitchTq = 0, 0, 0, 0
	w.angleVel, w.angleForce, w.distVel, w.distForce = 0, 0, 0, 0
	c.currentSpeed = 0
}

// cameraTerrain is the scene the camera sweeps against, or nil when there is
// none (or the Camera Collision option is off).
func (c *Client) cameraTerrain() *terrain.ZoneTerrain {
	if !c.camCollision || c.terrainLoader == nil || c.terrainZoneIdx < 0 {
		return nil
	}
	return c.terrainLoader.GetZoneTerrain(c.terrainZoneIdx)
}

// sweep is VIScene::Collide(VICollSphere, VIWalkCameraFilter) from the orbit
// centre: the sphere's stopping point, its distance and whether it was
// blocked. With no scene nothing is ever hit.
func (c *Client) sweep(from, to [3]float32) (stop [3]float32, dist float32, hit bool) {
	zt := c.cameraTerrain()
	if zt == nil {
		dx, dy, dz := to[0]-from[0], to[1]-from[1], to[2]-from[2]
		return to, float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz))), false
	}
	x, y, z, d, h := zt.CameraSweep(from[0], from[1], from[2], to[0], to[1], to[2], wcCollRadius)
	return [3]float32{x, y, z}, d, h
}

// orbitCentre is the walker's position raised by the target height.
func (c *Client) orbitCentre() [3]float32 {
	p := c.walker()
	return [3]float32{p.X, p.Y + c.wcam.targetHeight, p.Z}
}

func along(base, dir [3]float32, k float32) [3]float32 {
	return [3]float32{base[0] + dir[0]*k, base[1] + dir[1]*k, base[2] + dir[2]*k}
}

// orbitCandidates are FindOrbitAngle's eight angles in its order: the
// caller's, then ±45, ±90, ±135 and 180 degrees off it (a + 45 first, which is
// A − 45 in Go convention).
func orbitCandidates(A float32) [8]float32 {
	const q = 0.7853982
	return [8]float32{
		A,
		fmod32(A-q, 2*math.Pi), fmod32(A+q, 2*math.Pi),
		fmod32(A-2*q, 2*math.Pi), fmod32(A+2*q, 2*math.Pi),
		fmod32(A-3*q, 2*math.Pi), fmod32(A+3*q, 2*math.Pi),
		fmod32(A-math.Pi, 2*math.Pi),
	}
}

// findOrbitAngle is VIWalkCamera::FindOrbitAngle (0x00483910 [V]): the first
// of the eight angles whose sweep is clear; with none, the caller's and false.
func (c *Client) findOrbitAngle(A float32) (float32, bool) {
	cand := orbitCandidates(A)
	base := c.orbitCentre()
	for _, a := range cand {
		if _, _, hit := c.sweep(base, along(base, orbitDir(a, c.camPitch), c.camDist)); !hit {
			return a, true
		}
	}
	return cand[0], false
}

// findBestOrbitAngle is FindBestOrbitAngle (0x00483BC0 [V]): the first clear
// angle, else the one whose sweep got furthest.
func (c *Client) findBestOrbitAngle(A float32) float32 {
	cand := orbitCandidates(A)
	base := c.orbitCentre()
	best, bestDist := -1, float32(0)
	for i, a := range cand {
		_, d, hit := c.sweep(base, along(base, orbitDir(a, c.camPitch), c.camDist))
		if !hit {
			return a
		}
		if best == -1 || bestDist < d {
			best, bestDist = i, d
		}
	}
	return cand[best]
}

// chooseOrbitAngle is ChooseOrbitAngle (0x00483360 [V]).
func (c *Client) chooseOrbitAngle(A float32) {
	c.setOrbitAngle(c.findBestOrbitAngle(fmod32(A, 2*math.Pi)))
}

// updateCameraPhysics is VIWalkCamera::UpdateCameraPhysics (0x00482758 [V]),
// one step of dt seconds.
func (c *Client) updateCameraPhysics(dt float32) {
	w := &c.wcam
	d := pow32(wcDamping, dt)
	c.camAngle += w.angleForce*dt*dt*d + w.angleVel*dt*d
	w.angleVel = (w.angleVel + w.angleForce*dt) * d

	if w.flags&1 != 0 {
		tgt := w.snapAngle
		if math.Pi < w.snapAngle-c.camAngle {
			tgt = w.snapAngle - 2*math.Pi
		} else if w.snapAngle-c.camAngle < -math.Pi {
			tgt = w.snapAngle + 2*math.Pi
		}
		if cur := c.camAngle; cur < tgt {
			c.camAngle = cur + wcSnapSpeed*dt
			if tgt < c.camAngle {
				c.camAngle = tgt
				w.flags &^= 1
			}
		} else {
			c.camAngle = cur - wcSnapSpeed*dt
			if c.camAngle <= tgt {
				c.camAngle = tgt
				w.flags &^= 1
			}
		}
	}
	c.camAngle = fmod32(c.camAngle, 2*math.Pi)

	if w.flags&8 != 0 {
		tgt, cur := w.snapPitch, c.camPitch
		if cur < tgt {
			c.camPitch = cur + wcSnapSpeed*dt
			if tgt < c.camPitch {
				c.camPitch = tgt
				w.flags &^= 8
			}
		} else {
			c.camPitch = cur - wcSnapSpeed*dt
			if c.camPitch <= tgt {
				c.camPitch = tgt
				w.flags &^= 8
			}
		}
	}

	d = pow32(wcDamping, dt)
	c.camDist += w.distForce*dt*dt*d + w.distVel*dt*d
	w.distVel = (w.distVel + w.distForce*dt) * d

	if w.flags&2 != 0 {
		c.camDist -= wcDistSnap * dt
		if c.camDist <= w.minOrbitDist {
			w.flags &^= 2
		}
	} else if w.flags&4 != 0 {
		c.camDist -= wcDistSnap * dt
		if c.camDist <= w.minCollisionDist {
			c.camDist = w.minCollisionDist
			w.flags &^= 4
		}
	}
	c.camDist = clamp32(c.camDist, w.minOrbitDist, w.maxOrbitDist)
}

// moveCamera is VIWalkCamera::MoveCamera (0x00482AF0 [V]): place the eye. In
// mode 3 the orbit is first swept at the DEFAULT pitch: clear, the pitch snaps
// back to it; blocked, the camera zooms in while it is past the collision
// distance, then tries 60° (only on ground steeper than y 0.9), then swings to
// the first clear angle, and zooms to the minimum when there is none. Modes
// 0-2 put the eye where the sweep from the head stops.
func (c *Client) moveCamera() {
	w := &c.wcam
	p := c.walker()
	base := c.orbitCentre()
	if w.mode == camModeThird {
		if w.flags&(1|4|8) == 0 {
			_, _, blocked := c.sweep(base, along(base, orbitDir(c.camAngle, w.defOrbitPitch), c.camDist))
			switch {
			case !blocked:
				if c.camPitch != w.defOrbitPitch && !c.camFreePitch {
					w.snapPitch = w.defOrbitPitch
					w.flags |= 8
				}
			case c.camDist > w.minCollisionDist:
				w.flags |= 4
			default:
				_, _, altBlocked := c.sweep(base, along(base, orbitDir(c.camAngle, wcAltPitch), c.camDist))
				steep := false
				if zt := c.cameraTerrain(); zt != nil {
					steep = zt.GroundNormalY(p.X, p.Y, p.Z) < 0.9
				}
				if !altBlocked && steep {
					if c.camPitch != wcAltPitch && !c.camFreePitch {
						w.snapPitch = wcAltPitch
						w.flags |= 8
					}
				} else if a, ok := c.findOrbitAngle(c.camAngle); !ok {
					w.flags |= 2
				} else if c.camAngle != a {
					w.snapAngle = a
					w.flags |= 1
				}
			}
		}
		w.camPos = along(base, orbitDir(c.camAngle, c.camPitch), c.camDist)
		return
	}
	var to [3]float32
	if w.mode == camModeFixed {
		d := lookDir(p.Facing, w.pitch)
		to = along(base, d, -c.camDist)
	} else {
		to = along(base, orbitDir(c.camAngle, c.camPitch), c.camDist)
	}
	if w.autoOrbit {
		w.camPos, _, _ = c.sweep(base, to)
	} else {
		w.camPos = to
	}
}

// --- the walker's angular half -----------------------------------------------

// setTargetHeading is VIWalkControl::SetTargetHeading (0x00485980 [V]): a
// full-strength turn toward h, the bang-bang half of the turn.
func (c *Client) setTargetHeading(h float32) {
	w := &c.wcam
	p := c.walker()
	w.targetHeading = h
	d := h - p.Facing
	if math.Pi < d {
		h -= 2 * math.Pi
	} else if d < -math.Pi {
		h += 2 * math.Pi
	}
	if p.Facing < h {
		c.turnLeft(1)
	}
	if h < p.Facing {
		c.turnLeft(-1)
	}
}

// turnLeft is VIWalkControl::TurnLeft (0x00485A68 [V]).
func (c *Client) turnLeft(t float32) {
	c.wcam.turnTq += accelFor(c.wcam.turnRate) * clamp32(t, -1, 1)
}

// pitchDown is VIWalkControl::PitchDown's dry branch (0x00485B50 [V]).
func (c *Client) pitchDown(t float32) {
	c.wcam.pitchTq += accelFor(wcPitchRate) * clamp32(t, -1, 1)
}

// stepWalkerAngles is the angular half of VIWalkControl::UpdatePhysics
// (0x00486198 [V]) over one frame of dt seconds, in its ≤ 32 ms steps, with
// the turn's overshoot detector when tracking. The torques are this frame's
// and are cleared after it, as Process's ClearForces does.
func (c *Client) stepWalkerAngles(dt float32) {
	w := &c.wcam
	p := c.walker()
	if p == nil {
		return
	}
	left := min(dt, wcTickAltMS*0.001)
	for left > 0 {
		step := min(left, wcTickMS*0.001)
		left -= step
		d := pow32(wcDamping, step)
		p.Facing += w.turnTq*step*step*d + w.turnVel*step*d
		w.turnVel = (w.turnVel + w.turnTq*step) * d
		prevPitch := w.pitch
		w.pitch += w.pitchTq*step*step*d + w.pitchVel*step*d
		w.pitchVel = (w.pitchVel + w.pitchTq*step) * d

		if w.tracking {
			wrapped := w.targetHeading
			if math.Pi < w.targetHeading-p.Facing {
				wrapped = w.targetHeading - 2*math.Pi
			} else if w.targetHeading-p.Facing < -math.Pi {
				wrapped = w.targetHeading + 2*math.Pi
			}
			if (0 < w.turnTq && wrapped < p.Facing) || (w.turnTq < 0 && p.Facing < wrapped) {
				p.Facing = w.targetHeading
				w.turnTq = 0
				w.turnVel = 0
			}
		}
		p.Facing = fmod32(p.Facing, 2*math.Pi)

		// The dry pitch limit: past a limit, the pitch may not go further.
		if y := w.pitch; y < w.minPitch && y < prevPitch {
			w.pitch = prevPitch
		} else if w.maxPitch < y && prevPitch < y {
			w.pitch = prevPitch
		}
	}
	if p.Facing < 0 {
		p.Facing += 2 * math.Pi
	}
	w.turnTq, w.pitchTq = 0, 0
}

// steerWalker hands the walker this frame's turn the way func_00BC3AE8 does
// for the camera mode: in mode 3 a target heading while there is a move, in
// modes 0-2 the turn and look axes. Then the angles step.
func (c *Client) steerWalker(dt float32, moving bool, heading float32) {
	c.ensureWalkCamera()
	w := &c.wcam
	switch w.mode {
	case camModeThird:
		if moving {
			c.setTargetHeading(heading)
		}
	case camModeFirst, camModeFixed:
		c.turnLeft(w.inTurn)
		c.pitchDown(w.inPitch)
	case camModeOrbit:
		c.turnLeft(w.inTurn)
	}
	c.stepWalkerAngles(dt)
}

// --- CLIENT's styles ----------------------------------------------------------

// setCameraStyle is VIClient::SetCameraStyle (0x00BD8B40 [V]). 0 first
// person at the race's eye height; 2 the fixed follow; 3 third person at the
// race's look-at height, swung behind; 1 the plain orbit, swung behind;
// anything else first person without the eye height.
func (c *Client) setCameraStyle(style int) {
	c.ensureWalkCamera()
	w := &c.wcam
	p := c.walker()
	rc := c.playerRaceCamera()
	switch style {
	case 0:
		c.setPitchRange(-1.5707964, 1.5707964)
		w.turnRate = 1.5707964
		c.setCameraMode(camModeFirst)
		w.lookAtHeight = rc.EyeHeight
	case 2:
		w.targetHeight = 1.68
		w.minOrbitDist = 1.5
		w.maxOrbitDist = 25.0
		w.minCollisionDist = 3.5
		c.setOrbitDistance(3.38)
		c.setPitchRange(-0.7853982, 1.5707964)
		w.turnRate = 1.5707964
		w.lookAtHeightAbove = 1.68
		c.setCameraMode(camModeFixed)
	case 3:
		w.targetHeight = 1.68
		w.minOrbitDist = 1.5
		w.maxOrbitDist = c.styleThreeCeiling()
		w.minCollisionDist = 3.5
		c.setOrbitDistance(3.38)
		c.setPitchRange(-0.7853982, 1.5707964)
		c.setDefOrbitPitch(0.2617994)
		w.turnRate = 6.2831855
		w.lookAtHeightAbove = rc.EyeHeight
		c.setCameraMode(camModeThird)
		if p != nil {
			c.chooseOrbitAngle(behindAngle(p.Facing))
		}
	case 1:
		w.targetHeight = 1.1
		w.minOrbitDist = 1.5
		w.maxOrbitDist = 25.0
		w.minCollisionDist = 3.5
		c.setPitchRange(-1.5707964, 1.5707964)
		c.setDefOrbitPitch(0.43633232)
		w.turnRate = 1.5707964
		c.setCameraMode(camModeOrbit)
		if p != nil {
			c.chooseOrbitAngle(behindAngle(p.Facing))
		}
	default:
		c.setPitchRange(-1.5707964, 1.5707964)
		w.turnRate = 1.5707964
		c.setCameraMode(camModeFirst)
	}
}

// styleThreeCeiling is style 3's zoom ceiling: CLIENT's 8.0, or the Max
// Camera Zoom setting (a Go extra in the spirit of EQOA-QOL.ELF's camera
// distance, which raises the same limit).
func (c *Client) styleThreeCeiling() float32 {
	if c.camMaxDist > 0 {
		return c.camMaxDist
	}
	return 8.0
}

// thirdPersonStyle is the option VIGameOptions::m_x06 chooses: 3, or 2 when
// set (the Fixed Follow Camera setting).
func (c *Client) thirdPersonStyle() int {
	if c.camFixedFollow {
		return 2
	}
	return 3
}

// toggleFirstPerson is VIClient::InitCameraStyle (0x00BD5E38 [V]), the
// first/third person switch: pad code 145, Select (VIClient::PadKeys sends
// 128 + the button's index, [rx, ry, lx, ly, triangle, square, cross, circle,
// left, right, up, down, L1, L2, R1, R2, start, select, L3, R3]), and 'P' on a
// USB keyboard (VIWndGame's action table, 0x00C53DF8). U in Go.
func (c *Client) toggleFirstPerson() {
	c.ensureWalkCamera()
	switch c.wcam.mode {
	case camModeFixed, camModeThird:
		c.setCameraStyle(0)
	case camModeFirst:
		c.setCameraStyle(c.thirdPersonStyle())
	default:
		c.setCameraStyle(0)
	}
	c.saveCameraPreset(&c.wcam.saved)
	log.Printf("Camera: style %d (%s)", c.wcam.mode, cameraModeName(c.wcam.mode))
}

// toggleCameraStyle is VIClient::ToggleCameraStyle (0x00BD58C8 [V]): the
// third-person style the option chooses, exactly behind. The first world
// entry calls it, and so does changing the option.
func (c *Client) toggleCameraStyle() {
	c.setCameraStyle(c.thirdPersonStyle())
	if p := c.walker(); p != nil {
		c.setOrbitAngle(behindAngle(p.Facing))
	}
	c.saveCameraPreset(&c.wcam.saved)
}

// applyCameraOptions is ApplyOptions' camera half (0x00BCE848 [V]): a
// third-person style that no longer matches the option is switched.
func (c *Client) applyCameraOptions() {
	m := c.wcam.mode
	if (m == camModeFixed && !c.camFixedFollow) || (m == camModeThird && c.camFixedFollow) {
		c.toggleCameraStyle()
	}
	if m == camModeThird && !c.camFixedFollow {
		c.wcam.maxOrbitDist = c.styleThreeCeiling()
		c.setOrbitDistance(c.camDist)
	}
}

// saveCameraPreset is VIClient::SaveCameraPreset (0x00BD5940 [V]).
func (c *Client) saveCameraPreset(p *cameraPreset) {
	w := &c.wcam
	*p = cameraPreset{
		mode:              w.mode,
		targetHeight:      w.targetHeight,
		lookAtHeightAbove: w.lookAtHeightAbove,
		pitch:             c.camPitch,
		dist:              c.camDist,
		angle:             c.camAngle,
	}
}

// applyCameraPreset is VIClient::ApplyCameraPreset (0x00BD5978 [V]).
func (c *Client) applyCameraPreset(p cameraPreset) {
	w := &c.wcam
	c.setCameraMode(p.mode)
	w.targetHeight = p.targetHeight
	w.lookAtHeightAbove = p.lookAtHeightAbove
	c.setOrbitPitch(p.pitch)
	c.setOrbitDistance(p.dist)
	c.setOrbitAngle(p.angle)
}

// applyRaceCamera is VIClient::ApplyRaceCamera (0x00BC7A80 [V]), which the
// character panel's opening runs (0x00C55A20, after StopAutoAttack): the
// camera is saved, then put IN FRONT of the player in mode 1 at the race's
// World setting, and the walker stops. The panel is Start / Esc on a PS2;
// in Go it is the main menu.
func (c *Client) applyRaceCamera() {
	c.ensureWalkCamera()
	w := &c.wcam
	p := c.walker()
	rc := c.playerRaceCamera()
	if p == nil || w.panelCam || w.dead {
		return
	}
	c.saveCameraPreset(&w.preset)
	w.targetHeight = rc.World.TargetHeight
	w.lookAtHeightAbove = rc.World.LookAtHeightAbove
	c.setOrbitPitch(rc.World.Pitch)
	c.setOrbitDistance(rc.World.Distance)
	c.setCameraMode(camModeOrbit)
	// ChooseOrbitAngle(heading): the heading itself is in FRONT.
	c.chooseOrbitAngle(math.Pi/2 - p.Facing)
	c.halt()
	w.panelCam = true
}

// applyDefaultCamera is VIClient::ApplyDefaultCamera (0x00BC7B38 [V]), run
// when the panel closes (0x00C55BC8).
func (c *Client) applyDefaultCamera() {
	w := &c.wcam
	if !w.panelCam {
		return
	}
	w.panelCam = false
	c.applyCameraPreset(w.preset)
	// A camera option changed while the menu was open applies now (in mode 1
	// ApplyOptions has nothing to switch).
	c.applyCameraOptions()
}

// cameraModeName names a camera mode for the HUD and the logs.
func cameraModeName(mode int) string {
	switch mode {
	case camModeFirst:
		return "First Person"
	case camModeOrbit:
		return "Orbit"
	case camModeFixed:
		return "Fixed Follow"
	}
	return "Third Person"
}

// --- placement: world entry, teleports, death -------------------------------

// setCameraLocation is VIWalkCamera::SetLocation (0x004821D8 [V]) at the
// walker's position and heading: the orbit is rebuilt at the default pitch,
// angle 0 (the PS2's, π/2 here), the eye at the walker's feet, and moved from
// there.
func (c *Client) setCameraLocation(pitch float32) {
	w := &c.wcam
	p := c.walker()
	w.pitch = pitch
	w.turnVel, w.turnTq, w.pitchVel, w.pitchTq = 0, 0, 0, 0
	c.camPitch = w.defOrbitPitch
	c.camAngle = math.Pi / 2
	w.flags = 0
	w.angleVel, w.angleForce, w.distVel, w.distForce = 0, 0, 0, 0
	w.snapAngle, w.snapPitch = 0, 0
	w.camPos = [3]float32{p.X, p.Y, p.Z}
	c.moveCamera()
}

// resetCameraBehind is the camera half of VIClient::MoveTo (0x00BC4C08) and
// Respawn, which Msg00B2 (0x00BD38C8 [V]) runs: SetLocation at the walker's
// new position and heading, then SetOrbitAngleBehind. The style is kept.
// Whoever places the walker on the ground calls it afterwards.
func (c *Client) resetCameraBehind() {
	c.ensureWalkCamera()
	if c.walker() == nil {
		return
	}
	c.setCameraLocation(0)
	c.setOrbitAngleBehind()
}

// cameraWorldEntry is the camera half of Msg000D (0x00BD0630, read off the
// disassembly): SetLocation at the dump's position and heading,
// SetOrbitAngleBehind, the death camera and any camera effect ended; then on
// the first world entry ToggleCameraStyle, on every later one
// SetCameraStyle(m_camSaved.mode). Every MemoryDump runs it, a respawn after
// death included, so a world entry always comes back to 3.38 behind.
func (c *Client) cameraWorldEntry() {
	c.ensureWalkCamera()
	w := &c.wcam
	if c.walker() == nil {
		return
	}
	w.dead = false
	w.panelCam = false
	c.setCameraLocation(0)
	c.setOrbitAngleBehind()
	if !w.entered {
		w.entered = true
		c.toggleCameraStyle()
	} else {
		c.setCameraStyle(w.saved.mode)
	}
	log.Printf("Camera: world entry, style %d (%s), dist %.2f, pitch %.3f",
		w.mode, cameraModeName(w.mode), c.camDist, c.camPitch)
}

// resetCamera is VIClient::ResetCamera (0x00BC5F78 [V]), Msg005E's (death):
// a first-person camera first becomes style 2, then where the camera looks
// from is kept for the death camera.
func (c *Client) resetCamera() {
	c.ensureWalkCamera()
	w := &c.wcam
	p := c.walker()
	if p == nil {
		return
	}
	w.dead = true
	if w.mode == camModeFirst {
		c.setCameraStyle(2)
		c.processWalkCameraStep(0)
	}
	cam := c.walkCameraView(*p)
	w.deathPos = [3]float32{cam.Position.X, cam.Position.Y, cam.Position.Z}
	dir := [3]float32{cam.Target.X - cam.Position.X, cam.Target.Y - cam.Position.Y, cam.Target.Z - cam.Position.Z}
	if n := float32(math.Sqrt(float64(dir[0]*dir[0] + dir[1]*dir[1] + dir[2]*dir[2]))); n > 0 {
		dir = [3]float32{dir[0] / n, dir[1] / n, dir[2] / n}
	}
	w.deathDir = dir
	dx, dz := p.X-w.deathPos[0], p.Z-w.deathPos[2]
	w.deathDist = float32(math.Sqrt(float64(dx*dx + dz*dz)))
	w.deathT = 0
	w.deathYaw = -float32(math.Atan2(float64(dir[2]), float64(dir[0])))
	w.deathView = cam
}

// deathCamera is VIClient::DeathCamera (0x00BC6118 [A]), once a frame while
// dead: it backs away from the body along the kept bearing, rising and
// swinging round a little more each frame, and for four seconds turns from the
// kept direction down onto the body. The rise and the swing are per FRAME on
// the PS2, which runs locked at 30 (claude-notes/qol-patches-20260816.md), so
// they are scaled by dt·30 here. Its fade at four seconds is not ported.
func (c *Client) deathCamera(dt float32) rl.Camera3D {
	w := &c.wcam
	p := c.walker()
	if dt > 0.1 {
		dt = 1.0 / 30.0
	}
	frames := dt * 30
	pos := [3]float32{p.X, p.Y, p.Z}
	reach := w.deathDist + w.deathT*0.15
	eye := [3]float32{pos[0] - cos32(w.deathYaw)*reach, w.deathPos[1], pos[2] + sin32(w.deathYaw)*reach}
	ramp := min(w.deathT*0.25, 1)
	w.deathPos[1] += ramp * 0.019 * frames
	w.deathYaw += ramp * 0.014 * frames
	dir := [3]float32{pos[0] - eye[0], pos[1] - eye[1], pos[2] - eye[2]}
	if n := float32(math.Sqrt(float64(dir[0]*dir[0] + dir[1]*dir[1] + dir[2]*dir[2]))); n > 0 {
		dir = [3]float32{dir[0] / n, dir[1] / n, dir[2] / n}
	}
	if w.deathT < 4 {
		k := cos32(w.deathT*0.25*math.Pi+math.Pi)*0.5 + 0.5
		dir[1] = w.deathDir[1] + (dir[1]-w.deathDir[1])*k
		x2, z2 := dir[0]*dir[0], dir[2]*dir[2]
		if x2+z2 > 0 {
			s := (1 - dir[1]*dir[1]) / (x2 + z2)
			sx := float32(math.Sqrt(float64(x2 * s)))
			sz := float32(math.Sqrt(float64(z2 * s)))
			if dir[0] < 0 {
				sx = -sx
			}
			if dir[2] < 0 {
				sz = -sz
			}
			dir[0], dir[2] = sx, sz
		}
	}
	w.deathT += dt
	return rl.Camera3D{
		Position:   rl.Vector3{X: eye[0], Y: eye[1], Z: eye[2]},
		Target:     rl.Vector3{X: eye[0] + dir[0], Y: eye[1] + dir[1], Z: eye[2] + dir[2]},
		Up:         rl.Vector3{Y: 1},
		Fovy:       60,
		Projection: rl.CameraPerspective,
	}
}

// --- the frame ------------------------------------------------------------------

// gatherCameraInput turns this frame's keys and sticks into the axes
// func_00BC3AE8 gives the walk camera for its mode (the pad mapping is read
// off 0x00BC4930..0x00BC4AA8):
//
//	modes 1, 3  OrbitRight(−rx) on the right stick's x, OrbitIn(−ry) on its y
//	            (SwimUp in a liquid, which Go's swim keys handle)
//	modes 0, 2  TurnLeft on the right stick's x, PitchDown(ry) on its y
//
// Go's keys stand in for the right stick: A/D its x, W/S its y, and the zoom
// keys and wheel zoom. With the Free Camera Pitch setting (a Go extra; the
// PS2 has no pitch control in third person, and its pitch snaps back), W/S
// pitch the orbit instead.
func (c *Client) gatherCameraInput(rx, ry float32, ctrlDown bool) {
	w := &c.wcam
	var keysX, keysY, keysZoom float32
	if !ctrlDown && rl.IsKeyDown(rl.KeyA) {
		keysX--
	}
	if !ctrlDown && rl.IsKeyDown(rl.KeyD) {
		keysX++
	}
	if !ctrlDown && rl.IsKeyDown(rl.KeyW) {
		keysY--
	}
	if !ctrlDown && rl.IsKeyDown(rl.KeyS) {
		keysY++
	}
	if rl.IsKeyDown(rl.KeyEqual) || rl.IsKeyDown(rl.KeyKpAdd) {
		keysZoom++
	}
	if rl.IsKeyDown(rl.KeyMinus) || rl.IsKeyDown(rl.KeyKpSubtract) {
		keysZoom--
	}
	w.inOrbit, w.inZoom, w.inTurn, w.inPitch, w.inFreePitch = 0, 0, 0, 0, 0
	switch w.mode {
	case camModeOrbit, camModeThird:
		w.inOrbit = keysX + rx
		w.inZoom = keysZoom - ry
		if c.camFreePitch {
			w.inFreePitch = -keysY
		} else {
			w.inZoom -= keysY
		}
	default:
		// D and the stick's right turn right: TurnLeft(−1).
		w.inTurn = -(keysX + rx)
		// W looks down, as Go's first person always has; the stick's down
		// too, unless inverted (VIGameOptions::m_x07 flips ry in modes 0 and
		// 2, and the orbit's direction in 1 and 3).
		if c.invertPitch {
			ry = -ry
		}
		w.inPitch = -keysY + ry
	}
	if wheel := rl.GetMouseWheelMove(); wheel != 0 && !c.isMouseOverChat() {
		// The wheel is a Go extra: an impulse on the zoom's velocity.
		if w.mode == camModeOrbit || w.mode == camModeThird {
			w.distVel -= wheel * 5 * c.camSensitivity
		}
	}
}

// processWalkCamera is VIWalkCamera::Process (0x004823D0 [V]) for the
// frame, run every in-world frame after the input and the walker: in mode 3
// the orbit angle is re-derived from where the eye was (the drag), then the
// orbit steps and the eye is placed. Dead, DeathCamera runs instead.
func (c *Client) processWalkCamera() {
	c.ensureWalkCamera()
	p := c.walker()
	if p == nil {
		return
	}
	// Msg005E's ResetCamera, on the death edge (the handler is internal/game's).
	if p.Dead && !c.wcam.dead {
		c.resetCamera()
	} else if !p.Dead && c.wcam.dead {
		c.wcam.dead = false
	}
	// Dead (and no other window up), func_00BC6500 runs DeathCamera in place
	// of the input and the walk camera.
	if c.wcam.dead {
		c.wcam.deathView = c.deathCamera(rl.GetFrameTime())
		return
	}
	// The character panel: ApplyRaceCamera as it opens, ApplyDefaultCamera as
	// it closes. Go's menu is a box over the middle of the screen, where the
	// player then stands, so the Character View Camera setting can turn it off.
	if c.menuOpen && c.camPanelView && !c.wcam.panelCam {
		c.applyRaceCamera()
	} else if (!c.menuOpen || !c.camPanelView) && c.wcam.panelCam {
		c.applyDefaultCamera()
	}
	c.processWalkCameraStep(rl.GetFrameTime())
}

func (c *Client) processWalkCameraStep(dt float32) {
	w := &c.wcam
	p := c.walker()
	if w.mode == camModeFirst || w.mode == camModeFixed {
		// The orbit angle plays no part in modes 0 and 2; keeping it behind
		// the heading keeps Go's camera-relative movement heading-relative.
		c.camAngle = behindAngle(p.Facing)
	}
	if w.mode == camModeThird {
		dx, dz := w.camPos[0]-p.X, w.camPos[2]-p.Z
		if dx != 0 || dz != 0 {
			c.camAngle = float32(math.Atan2(float64(dz), float64(dx)))
		} else {
			c.camAngle = math.Pi / 2 // atan2f(0, 0) is the PS2's 0
		}
	}
	sens := c.camSensitivity
	if sens <= 0 {
		sens = 1
	}
	if w.mode == camModeOrbit || w.mode == camModeThird {
		// OrbitRight and OrbitIn answer only in the orbiting modes.
		w.angleForce += accelFor(wcOrbitRate) * sens * clamp32(w.inOrbit, -1, 1)
		w.distForce += accelFor(wcZoomRate) * sens * -clamp32(w.inZoom, -1, 1)
		if w.inFreePitch != 0 {
			c.camPitch = clamp32(c.camPitch+w.inFreePitch*wcOrbitRate*sens*dt, -0.7853982, 1.5)
		}
	}

	left := min(dt, wcTickAltMS*0.001)
	if left <= 0 {
		c.moveCamera()
	}
	for left > 0 {
		step := min(left, wcTickMS*0.001)
		left -= step
		c.updateCameraPhysics(step)
		c.moveCamera()
	}
	w.angleForce, w.distForce = 0, 0 // ClearCameraForces
	// The axes are this frame's: a frame whose input is skipped (a menu, the
	// chat line) hands over nothing.
	w.inOrbit, w.inZoom, w.inTurn, w.inPitch, w.inFreePitch = 0, 0, 0, 0, 0

	for c.camAngle < 0 {
		c.camAngle += 2 * math.Pi
	}
	for c.camAngle >= 2*math.Pi {
		c.camAngle -= 2 * math.Pi
	}
}

// walkCameraView is VIWalkCamera::SetCamera (0x00483578 [V]): the eye and the
// look for the mode. Dead, it is DeathCamera's.
func (c *Client) walkCameraView(p game.Entity) rl.Camera3D {
	c.ensureWalkCamera()
	w := &c.wcam
	if w.dead && w.deathView.Fovy != 0 {
		return w.deathView
	}
	var eye, target [3]float32
	switch w.mode {
	case camModeFirst:
		eye = [3]float32{p.X, p.Y + w.lookAtHeight, p.Z}
		target = along(eye, lookDir(p.Facing, w.pitch), 1)
	case camModeFixed:
		eye = w.camPos
		target = along(eye, lookDir(p.Facing, w.pitch), 1)
	default:
		eye = w.camPos
		target = [3]float32{p.X, p.Y + w.lookAtHeightAbove, p.Z}
	}
	return rl.Camera3D{
		Position:   rl.Vector3{X: eye[0], Y: eye[1], Z: eye[2]},
		Target:     rl.Vector3{X: target[0], Y: target[1], Z: target[2]},
		Up:         rl.Vector3{Y: 1},
		Fovy:       60,
		Projection: rl.CameraPerspective,
	}
}

// cameraHidesPlayer is func_00BC6500's rule for the player's own actor: hidden
// in first person (unless dead), and in style 2 while the eye is within 0.8 of
// the head.
func (c *Client) cameraHidesPlayer(p game.Entity, cam rl.Camera3D) bool {
	w := &c.wcam
	switch w.mode {
	case camModeFirst:
		return !w.dead
	case camModeFixed:
		dx := p.X - cam.Position.X
		dy := p.Y + w.targetHeight - cam.Position.Y
		dz := p.Z - cam.Position.Z
		return dx*dx+dy*dy+dz*dz < wcHideFixedAt*wcHideFixedAt
	}
	return false
}
