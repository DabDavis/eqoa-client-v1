package main

import (
	"log"
	"math"
	"sort"

	rl "github.com/gen2brain/raylib-go/raylib"

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

	if bestID != 0 {
		c.targetObjectID = bestID
		c.engine.TargetObjectID = bestID
		c.engine.SendTarget(bestID)
		c.engine.ForcePositionFlush()
		c.animMgr.CombatEngaged = false // allow re-engage on new target
		c.engine.CombatEngaged = false
		c.engine.AttackSent = false
		log.Printf("TARGET: mousePick → 0x%08X engine.TargetObjectID=0x%08X", bestID, c.engine.TargetObjectID)
	} else {
		c.targetObjectID = 0
		c.engine.TargetObjectID = 0
	}
}

// tabTarget cycles through nearby entities sorted by 3D distance from player.
func (c *Client) tabTarget() {
	gs := c.engine.State
	if len(gs.Entities) == 0 {
		return
	}

	p := gs.Player

	camera := c.currentCamera(p)
	sw := float32(rl.GetScreenWidth())
	sh := float32(rl.GetScreenHeight())

	// Build sorted list of visible entities by 3D distance (closest first)
	type entDist struct {
		id   uint32
		dist float32
	}
	var sorted []entDist
	for _, ent := range gs.Entities {
		// Screen visibility check
		height := c.modelHeight(ent.ModelID, 1.8) * ent.Scale
		// Check entity is in front of camera (dot product with view direction > 0)
		entPos := rl.Vector3{X: ent.X, Y: ent.Y + height*0.5, Z: ent.Z}
		viewDir := rl.Vector3{
			X: camera.Target.X - camera.Position.X,
			Y: camera.Target.Y - camera.Position.Y,
			Z: camera.Target.Z - camera.Position.Z,
		}
		toEnt := rl.Vector3{
			X: entPos.X - camera.Position.X,
			Y: entPos.Y - camera.Position.Y,
			Z: entPos.Z - camera.Position.Z,
		}
		dot := viewDir.X*toEnt.X + viewDir.Y*toEnt.Y + viewDir.Z*toEnt.Z
		if dot <= 0 {
			continue // behind camera
		}
		screenPos := rl.GetWorldToScreen(entPos, camera)
		if screenPos.X < 0 || screenPos.X > sw || screenPos.Y < 0 || screenPos.Y > sh {
			continue
		}

		d := mathutil.Dist3D(ent.X, ent.Y, ent.Z, camera.Position.X, camera.Position.Y, camera.Position.Z)
		sorted = append(sorted, entDist{id: ent.ObjectID, dist: d})
	}
	if len(sorted) == 0 {
		return
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].dist < sorted[j].dist
	})

	// Find current target in list and pick the next one (closest -> farthest -> wrap)
	nextIdx := 0
	if c.targetObjectID != 0 {
		for i, ed := range sorted {
			if ed.id == c.targetObjectID {
				nextIdx = (i + 1) % len(sorted)
				break
			}
		}
	}

	c.targetObjectID = sorted[nextIdx].id
	c.engine.TargetObjectID = c.targetObjectID
	c.engine.SendTarget(c.targetObjectID)
	c.engine.ForcePositionFlush()
	c.animMgr.CombatEngaged = false // allow re-engage on new target
	c.engine.CombatEngaged = false
	c.engine.AttackSent = false
	log.Printf("TARGET: tab → 0x%08X engine.TargetObjectID=0x%08X", c.targetObjectID, c.engine.TargetObjectID)
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

// camTrig returns the four camera trig values used for camera positioning and movement.
func (c *Client) camTrig() (cosP, sinP, cosA, sinA float32) {
	cosP = float32(math.Cos(float64(c.camPitch)))
	sinP = float32(math.Sin(float64(c.camPitch)))
	cosA = float32(math.Cos(float64(c.camAngle)))
	sinA = float32(math.Sin(float64(c.camAngle)))
	return
}

// currentCamera is the frame's camera: VIWalkCamera::SetCamera
// (walkcamera.go), DeathCamera while dead.
func (c *Client) currentCamera(p game.Entity) rl.Camera3D {
	return c.walkCameraView(p)
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
