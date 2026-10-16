package main

import (
	"log"
	"math"
	"time"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/eqoa/client/internal/frustum"
	"github.com/eqoa/client/internal/game"
	"github.com/eqoa/client/internal/mathutil"
)

// --- Targeting methods ---

// mousePickTarget picks the entity closest to the mouse click in screen space.
func (c *Client) mousePickTarget() {
	gs := c.engine.State
	if len(gs.Entities) == 0 {
		return
	}

	mousePos := rl.GetMousePosition()
	p := gs.Player

	camera := c.currentCamera(p)

	bestDist := float32(50.0) // max screen pixel distance threshold
	bestID := uint32(0)

	// Check all nearby entities + self
	checkEnt := func(ent *game.Entity) {
		scale := ent.Scale
		if scale <= 0 {
			scale = 1.0
		}
		height := c.modelHeight(ent.ModelID, 1.8) * scale
		worldPos := rl.Vector3{X: ent.X, Y: ent.Y + height*0.5, Z: ent.Z}
		screenPos := rl.GetWorldToScreen(worldPos, camera)
		if screenPos.X < 0 || screenPos.X > float32(rl.GetScreenWidth()) ||
			screenPos.Y < 0 || screenPos.Y > float32(rl.GetScreenHeight()) {
			return
		}
		dist := mathutil.Dist2D(mousePos.X, mousePos.Y, screenPos.X, screenPos.Y)
		if dist < bestDist {
			bestDist = dist
			bestID = ent.ObjectID
		}
	}
	for _, ent := range gs.Entities {
		checkEnt(ent)
	}
	// Include self
	if p.ObjectID != 0 && p.Name != "" {
		checkEnt(&gs.Player)
	}

	// A pick is a PC addition; it takes CLIENT's SelectTarget tail (SetTarget:
	// auto-attack off, 0x0010), and a miss its ClearTarget (no message).
	if bestID != 0 {
		c.engine.SetTarget(bestID)
		log.Printf("TARGET: mousePick → 0x%08X", bestID)
	} else {
		c.engine.ClearTarget()
	}
	c.targetChanged()
}

// tabTarget is the target key (Tab, R1): CLIENT's CycleTarget over the
// scene (combat.go), scored through this frame's camera.
func (c *Client) tabTarget() {
	c.engine.CycleTarget(false, c.targetView())
	c.targetChanged()
	log.Printf("TARGET: tab → 0x%08X", c.engine.TargetObjectID)
}

// partyTarget is the party key (`, L1): CycleTarget over me, the group
// roster and the pet.
func (c *Client) partyTarget() {
	c.engine.CycleTarget(true, nil)
	c.targetChanged()
	log.Printf("TARGET: party → 0x%08X", c.engine.TargetObjectID)
}

// targetView is m_camera as CycleTarget reads it: the camera this frame
// draws with, its frustum, and a point's clip-space x (GetWorldToScreen's
// pixel back to -1..1).
func (c *Client) targetView() *game.TargetView {
	camera := c.currentCamera(c.engine.State.Player)
	sw := float32(rl.GetScreenWidth())
	sh := float32(rl.GetScreenHeight())
	if sw <= 0 || sh <= 0 {
		return nil
	}
	frust := frustum.FromCamera(camera, sw/sh)
	look := rl.Vector3Normalize(rl.Vector3Subtract(camera.Target, camera.Position))
	return &game.TargetView{
		Pos: [3]float32{camera.Position.X, camera.Position.Y, camera.Position.Z},
		Dir: [3]float32{look.X, look.Y, look.Z},
		Visible: func(x, y, z float32) bool {
			return frust.TestSphere(x, y, z, 0)
		},
		ClipX: func(x, y, z float32) float32 {
			return rl.GetWorldToScreen(rl.Vector3{X: x, Y: y, Z: z}, camera).X/sw*2 - 1
		},
	}
}

// targetChanged follows the engine's m_target into the client's copy, and
// lets the animation layer re-engage on a new target.
func (c *Client) targetChanged() {
	if c.targetObjectID != c.engine.TargetObjectID {
		c.animMgr.CombatEngaged = false
	}
	c.targetObjectID = c.engine.TargetObjectID
}

// attackPressed is the attack button's press (Space, pad Cross; the PS2's
// keys 32 and 134): ToggleAutoAttack. Its release is MaybeStopAutoAttack, at
// the top of handleInWorldInput. When it engages, the animation layer starts
// its own combat state for the server's swings.
func (c *Client) attackPressed() {
	c.engine.ToggleAutoAttack()
	if c.engine.CombatEngaged && !c.animMgr.CombatEngaged {
		c.animMgr.CombatEngaged = true
		c.animMgr.playerCombat.attackCycleIdx = 0
		c.animMgr.playerCombat.lastActivity = time.Now()
	}
}

// attackHeld reports whether the attack button is down.
func attackHeld() bool {
	return rl.IsKeyDown(rl.KeySpace) || (gpAvailable() && gpDown(rl.GamepadButtonRightFaceDown))
}

// modelHeight returns the height of a model, or fallback if not loaded.
func (c *Client) modelHeight(modelID uint32, fallback float32) float32 {
	if c.modelMgr != nil {
		if h, ok := c.modelMgr.ModelHeight(int32(modelID)); ok && h > 0 {
			return h
		}
	}
	return fallback
}

// buildCamera constructs the camera matching drawWorld's logic.
// camTrig returns the four camera trig values used for camera positioning and movement.
func (c *Client) camTrig() (cosP, sinP, cosA, sinA float32) {
	cosP = float32(math.Cos(float64(c.camPitch)))
	sinP = float32(math.Sin(float64(c.camPitch)))
	cosA = float32(math.Cos(float64(c.camAngle)))
	sinA = float32(math.Sin(float64(c.camAngle)))
	return
}

// currentCamera computes camera trig values and builds the camera in one call.
func (c *Client) currentCamera(p game.Entity) rl.Camera3D {
	cosP, sinP, cosA, sinA := c.camTrig()
	return c.buildCamera(p, cosP, sinP, cosA, sinA)
}

func (c *Client) buildCamera(p game.Entity, cosP, sinP, cosA, sinA float32) rl.Camera3D {
	firstPerson := c.camDist < 0.1

	var camX, camY, camZ float32
	var targX, targY, targZ float32

	if firstPerson {
		eyeHeight := c.modelHeight(p.ModelID, 5.0) * 0.9
		camX = p.X
		camY = p.Y + eyeHeight
		camZ = p.Z
		targX = camX - cosP*cosA
		targY = camY - sinP
		targZ = camZ - cosP*sinA
	} else {
		camX = p.X + c.camDist*cosP*cosA
		camY = p.Y + c.camDist*sinP
		camZ = p.Z + c.camDist*cosP*sinA
		// PS2 VIWalkCamera targets a fixed attachment point on the character
		// (shoulder/chest height), NOT a distance-dependent offset.
		// Race HeightMult from CLIENT.AUT 0x00CDD8A0 scales the target height.
		rp := c.playerRaceParams()
		mh := c.modelHeight(p.ModelID, 5.0)
		targHeight := mh * (rp.HeightMax / rp.HeightMult)
		if targHeight < 0.5 {
			targHeight = mh * 0.5
		}
		targX = p.X
		targY = p.Y + targHeight
		targZ = p.Z

		// PS2 MoveCamera (0x00482AF0): camera-terrain collision.
		// PS2 uses VICollSphere (radius ~1.57) swept from player to camera,
		// with fallback to FindOrbitAngle if too close. We ray-march with
		// terrain + wall checks and use sweepSphereVsWalls for the full path.
		const camClearance = float32(1.2)  // PS2 collisionRadius ≈ PI/2
		const camSteps = 20                // dense sampling for indoor walls
		if c.camCollision && c.terrainLoader != nil && c.camDist > 0.5 {
			zt := c.terrainLoader.GetZoneTerrain(c.terrainZoneIdx)
			if zt != nil {
				startX, startY, startZ := targX, targY, targZ
				dx := camX - startX
				dy := camY - startY
				dz := camZ - startZ

				bestT := float32(1.0)
				step := float32(1.0) / float32(camSteps)

				for i := 1; i <= camSteps; i++ {
					t := float32(i) * step
					sx := startX + dx*t
					sy := startY + dy*t
					sz := startZ + dz*t

					clipped := false

					// Terrain floor check
					if groundY, ok := zt.SampleHeight(sx, sz, sy); ok {
						if sy < groundY+camClearance {
							clipped = true
						}
					}

					// Wall check via swept sphere every 4th step (perf: ~5 sphere sweeps)
					if !clipped && i > 1 && i%4 == 0 {
						prevT := float32(i-4) * step
						px := startX + dx*prevT
						py := startY + dy*prevT
						pz := startZ + dz*prevT
						_, _, _, hitT, hit := zt.SweepSphereVsWalls(
							px, py, pz, sx, sy, sz, 0.5)
						if hit && hitT < 0.9 {
							clipped = true
						}
					}

					if clipped {
						pullT := float32(i-1) * step
						if pullT < 0.05 {
							pullT = 0.05
						}
						if pullT < bestT {
							bestT = pullT
						}
						break
					}
				}

				// Smooth collision distance to prevent bouncing on beams/overhangs.
				// Pull IN is instant (safety), pull OUT ramps smoothly.
				if bestT < c.camCollDist {
					// Blocked closer — snap immediately (safety)
					c.camCollDist = bestT
				} else {
					// Clear further — ramp out slowly (prevents bounce)
					dt := rl.GetFrameTime()
					rampSpeed := float32(2.0) // recover to full distance in ~0.5s
					c.camCollDist += (bestT - c.camCollDist) * dt * rampSpeed
					if c.camCollDist > bestT {
						c.camCollDist = bestT
					}
				}

				useT := c.camCollDist
				camX = startX + dx*useT
				camY = startY + dy*useT
				camZ = startZ + dz*useT
				if groundY, ok := zt.SampleHeight(camX, camZ, camY); ok {
					if camY < groundY+camClearance {
						camY = groundY + camClearance
					}
				}
			}
		}
	}

	return rl.Camera3D{
		Position:   rl.Vector3{X: camX, Y: camY, Z: camZ},
		Target:     rl.Vector3{X: targX, Y: targY, Z: targZ},
		Up:         rl.Vector3{X: 0, Y: 1, Z: 0},
		Fovy:       60,
		Projection: rl.CameraPerspective,
	}
}

// conColorToRL converts a server con color byte to an rl.Color.
func conColorToRL(con byte) rl.Color {
	switch con {
	case 0:
		return rl.Color{R: 255, G: 50, B: 50, A: 255} // Red
	case 1:
		return rl.Color{R: 255, G: 255, B: 50, A: 255} // Yellow
	case 2:
		return rl.Color{R: 255, G: 255, B: 255, A: 255} // White
	case 3:
		return rl.Color{R: 100, G: 100, B: 255, A: 255} // Dark blue
	case 4:
		return rl.Color{R: 150, G: 200, B: 255, A: 255} // Light blue
	case 5:
		return rl.Color{R: 50, G: 255, B: 50, A: 255} // Green
	default:
		return rl.White
	}
}
