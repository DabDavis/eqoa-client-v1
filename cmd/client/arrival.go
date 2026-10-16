package main

import (
	"log"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/eqoa/client/internal/game"
	"github.com/eqoa/client/internal/terrain"
)

// arrival.go — the visual client's half of an arrival (game.Arrival): what
// CLIENT's Msg000D (0x00BD0630) and Respawn (0x00BD5628 [V]) do with the
// scene as the player lands somewhere. EnterWorld (0x00BD51C0 [V]) loads the
// zones around the point before either goes on; Go streams them, so the cast
// waits here until the zone under the point is in.

// updateArrival takes a new arrival and finishes a pending one. Every frame
// in world, before the input.
func (c *Client) updateArrival() {
	if a := c.engine.State.TakeArrival(); a != nil {
		c.arrival = a
		c.startArrival(a)
	}
	a := c.arrival
	if a == nil {
		return
	}
	if a.Reach == 0 {
		c.arrival = nil
		return
	}
	p := &c.engine.State.Player
	var zt *terrain.ZoneTerrain
	if c.terrainLoader != nil && c.currentWorld == a.World {
		zt = c.terrainLoader.GetZoneTerrain(c.terrainLoader.FindZone(a.X, a.Z))
	}
	if zt == nil {
		// Still loading: the walker waits at the point, as the PS2's waits
		// inside EnterWorld.
		p.X, p.Y, p.Z = a.X, a.Y, a.Z
		c.velocityY = 0
		return
	}
	p.X, p.Z = a.X, a.Z
	p.Y = zt.ArrivalFloor(a.X, a.Y, a.Z, a.Reach)
	c.velocityY = 0
	c.onGround = false
	c.arrival = nil
	log.Printf("Arrival: (%.1f, %.1f, %.1f) set down at y=%.2f (reach %.0f)", a.X, a.Y, a.Z, p.Y, a.Reach)
	// Respawn's and MoveTo's last word: VIWalkCamera::SetLocation at the
	// placed walker, then SetOrbitAngleBehind (walkcamera.go). A MemoryDump
	// (Reach 0) gets cameraWorldEntry from main.go instead, which also
	// restores the camera style.
	c.resetCameraBehind()
}

// startArrival is what every arrival does at once: the battle music and the
// zone's sound track stop (VIBattleMusic::StopNow, VIScene::StopSoundTrack),
// and a Respawn stops the follow and the auto-run (VIClient+0x70..+0x78).
func (c *Client) startArrival(a *game.Arrival) {
	if c.battleBGMActive {
		c.battleBGMActive = false
		c.currentBGM = "" // the zone's music comes back, or a new battle track
	}
	c.audioSys.StopSoundTrack()
	if a.StopFollow {
		c.followTarget = false
		c.autoRun = false
		c.engine.StopMovement()
	}
	// The camera goes behind the walker once it is set down (updateArrival,
	// resetCameraBehind); a.Heading is already the player's facing.
}

// drawFade is VIClient::DrawFade (0x00BC55D8 [V]), last in the frame and over
// everything: the fade moves on, then covers the screen in its colour at the
// colour's alpha × cur. The PS2's fade starts once EnterWorld has loaded the
// world, so it holds while Go's loading overlay is up; its time is the
// scene's, VITimer seconds.
func (c *Client) drawFade() {
	gs := c.engine.State
	var alpha float32
	if c.loadScrFade > 0 {
		alpha = gs.Fade.Color[3] * gs.Fade.Cur
	} else {
		alpha = gs.AdvanceFade(rl.GetFrameTime() * 0.98304)
	}
	if alpha <= 0 {
		return
	}
	col := gs.Fade.Color
	rl.DrawRectangle(-1, -1, int32(rl.GetScreenWidth())+2, int32(rl.GetScreenHeight())+2, rl.Color{
		R: uint8(col[0] * 255), G: uint8(col[1] * 255), B: uint8(col[2] * 255), A: uint8(min(1, alpha) * 255),
	})
}
