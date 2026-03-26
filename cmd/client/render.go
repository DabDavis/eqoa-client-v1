package main

import (
	"fmt"
	"log"
	"time"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/eqoa/client/internal/audio"
	"github.com/eqoa/client/internal/frustum"
	"github.com/eqoa/client/internal/game"
	"github.com/eqoa/client/internal/mathutil"
	"github.com/eqoa/client/internal/models"
	"github.com/eqoa/client/internal/terrain"
	"github.com/eqoa/client/internal/ui"
	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

// boneEmitterEntry tracks a single bone-attached particle emitter for an entity.
// PS2: one VIParticleEmitter per CSpritePartEmitter entry.
type boneEmitterEntry struct {
	emitter   *game.ParticleEmitter // live emitter in ParticleSystem
	boneIdx   int                   // raw skeleton bone index to track
	modelID   int32                 // entity model ID (for BoneWorldPosByIdx)
	trackBone bool                  // PS2 entry+0x3FC: follow bone position each frame
}

// weaponEmitterEntry tracks a particle emitter attached to a weapon model.
type weaponEmitterEntry struct {
	emitter  *game.ParticleEmitter
	weaponID int32              // weapon model DictID
	slot     models.WeaponSlot  // which hand (for bone position tracking)
	localPt  [3]float32         // weapon-local offset (blade midpoint)
}

// Drawing functions

func (c *Client) drawStatus() {
	gs := c.engine.State
	c.drawText("EverQuest Online Adventures", 20, 20, 20, rl.White)
	c.drawText(fmt.Sprintf("Phase: %s", gs.Phase), 20, 50, 16, rl.LightGray)
	c.drawText(gs.StatusMsg, 20, 80, 16, rl.LightGray)
	c.drawText(fmt.Sprintf("Server: %s:%d", gs.ServerIP, gs.GamePort), 20, 110, 16, rl.LightGray)
	c.drawText(fmt.Sprintf("User: %s", gs.Username), 20, 130, 16, rl.LightGray)
}

func (c *Client) drawServerList() {
	gs := c.engine.State
	c.drawText("EQOA - Server List", 20, 20, 20, rl.White)
	c.drawText(gs.StatusMsg, 20, 50, 16, rl.LightGray)

	y := int32(90)
	for i, srv := range gs.Servers {
		c.drawText(fmt.Sprintf("[%d] %s (%s:%d)", i+1, srv.Name, srv.IP, srv.Port), 40, y, 16, rl.Yellow)
		y += 20
	}
	c.drawText("Auto-connecting to first server...", 20, y+20, 16, rl.LightGray)
}

func (c *Client) drawWorld() {
	if c.engine == nil {
		return
	}
	gs := c.engine.State
	p := gs.Player

	// PS2 radial zone streaming: load all zones within radius of player.
	const zoneStreamRadius float32 = 500
	if c.terrainLoader != nil && (p.X != 0 || p.Z != 0) {
		nearby := c.terrainLoader.FindNearbyZones(p.X, p.Z, zoneStreamRadius)
		// Track primary zone (for collision, flora, effects).
		primaryIdx := c.terrainLoader.FindZone(p.X, p.Z)
		if primaryIdx >= 0 && primaryIdx != c.terrainZoneIdx {
			if c.terrainStarted {
				c.safeValid = false
				if c.effectVolumes != nil {
					c.effectVolumes.Clear()
				}
				if c.floraRender != nil {
					c.floraRender.Reset()
				}
				// Clear active spell effects on zone change — entities from
				// the old zone will lose state channel references and emitters
				// would fall back to (0,0,0) or stale positions.
				if c.engine != nil && c.engine.SpellFX != nil {
					c.engine.SpellFX.StopAll()
				}
			}
			c.terrainZoneIdx = primaryIdx
			c.terrainStarted = true
			c.zoneParticlesSpawned = false
			// Build portal navigator for indoor pathfinding
			c.portalNav = nil
			c.navGridPath = nil
			if zt := c.terrainLoader.GetZoneTerrain(primaryIdx); zt != nil && len(zt.Rooms) > 0 {
				c.portalNav = terrain.NewPortalNav(zt.Rooms)
			}
		}
		// Start loading any new nearby zones.
		wantSet := make(map[int]bool, len(nearby))
		for _, idx := range nearby {
			wantSet[idx] = true
			if !c.loadedZoneIdxs[idx] {
				log.Printf("drawWorld: streaming in zone %d", idx)
				c.terrainLoader.EnsureZoneLoaded(idx)
				c.loadedZoneIdxs[idx] = true
			}
		}
		// Unload zones that are too far away.
		for idx := range c.loadedZoneIdxs {
			if !wantSet[idx] {
				log.Printf("drawWorld: streaming out zone %d", idx)
				c.terrainRender.ResetZone(idx)
				c.terrainLoader.UnloadZone(idx)
				delete(c.loadedZoneIdxs, idx)
			}
		}
	}

	// PS2 VIWalkCamera orbit -- distance=0 naturally becomes first-person.
	camera := c.currentCamera(p)
	firstPerson := c.camDist < 0.1

	// Compute view frustum once per frame for culling.
	aspect := float32(rl.GetScreenWidth()) / float32(rl.GetScreenHeight())
	frust := frustum.FromCamera(camera, aspect)

	// Update audio listener position for 3D sound
	if c.audioSys != nil {
		c.audioSys.SetListener(camera.Position.X, camera.Position.Y, camera.Position.Z)
		c.audioSys.Update()
		c.audioSys.UpdateBGM()
		c.audioSys.UpdateAmbient()

		// Clear music player override when menu is closed or not on Music tab
		// (unless the persistent HUD player is active)
		if c.musicPlayerOn && !c.musicHudOn && (!c.menuOpen || c.menuTab != 6) {
			c.musicPlayerOn = false
		}

		// Feed streaming BGM audio data each frame.
		c.audioSys.UpdateBGM()

		// Delayed BGM restart after display change (let audio device settle).
		if c.bgmRestartDelay > 0 {
			c.bgmRestartDelay--
			if c.bgmRestartDelay == 0 {
				log.Printf("Audio: BGM restart delay expired, clearing currentBGM=%q", c.currentBGM)
				c.currentBGM = "" // triggers restart below
			}
		}

		// PS2 VIBattleMusic: switch to battle BGM during combat, restore zone BGM after.
		// Play() selects a random battle track (battle01-08.bgm), Stop() fades out.
		if c.engine.InCombat && !c.battleBGMActive {
			c.battleBGMActive = true
			// Random battle track 1-8 (PS2: rand()%8+1 when trackIndex==9)
			track := c.battleTrackRand%8 + 1
			c.battleTrackRand++
			bgmName := fmt.Sprintf("BATTLE%02d.BGM", track)
			c.playBGMFromISO(bgmName)
			c.currentBGM = bgmName
		} else if !c.engine.InCombat && c.battleBGMActive {
			c.battleBGMActive = false
			c.currentBGM = "" // triggers zone BGM restore below
		}

		// Check if player is in a BGM zone and play/stop accordingly
		if len(c.bgmZones) > 0 && !c.bgmMuted && !c.musicPlayerOn && !c.musicHudOn && !c.battleBGMActive {
			bgmPath := audio.CheckBGMZone(c.bgmZones, p.X, p.Y, p.Z)
			if bgmPath != "" {
				bgmName := audio.ResolveBGMName(bgmPath)
				if bgmName != c.currentBGM {
					c.playBGMFromISO(bgmName)
					c.currentBGM = bgmName
				}
			} else {
				if c.currentBGM != "" {
					c.audioSys.StopBGM()
					c.currentBGM = ""
				}
			}
		}

	}

	rl.BeginMode3D(camera)

	// Set camera position for per-bone animation LOD (PS2 ProcessPlayList channelFilter).
	if c.modelMgr != nil {
		c.modelMgr.SetCameraPos(camera.Position.X, camera.Position.Y, camera.Position.Z)
	}

	// Update and draw sky dome (depth writes off -- always behind everything).
	if c.skyDome != nil {
		dt := rl.GetFrameTime()
		gameHour := c.engine.State.GameHour()
		c.skyDome.Update(dt, gameHour)
		c.skyDome.Draw(camera)
	}

	// Multi-zone terrain: incrementally upload + render all loaded zones.
	if c.terrainLoader != nil && c.terrainRender != nil {
		// Set per-frame lighting uniforms (sun direction, fog, ambient).
		gameHour := float32(12.0) // default noon
		if c.engine.State.TimeValid {
			gameHour = c.engine.State.GameHour()
		}
		// Point lights (torches + player lanterns) only active at dusk/night.
		c.pointLightBuf = c.pointLightBuf[:0]
		isDusk := gameHour >= 19.0 || gameHour < 6.0
		if c.engine.SpellFX != nil && c.engine.SpellFX.Particles != nil {
			c.engine.SpellFX.Particles.ZoneLightsOn = isDusk
		}
		// Collect point lights from all loaded zones.
		for idx := range c.loadedZoneIdxs {
			zt := c.terrainLoader.GetZoneTerrain(idx)
			if zt == nil {
				continue
			}
			for _, pl := range zt.PointLights {
				if pl.AlwaysOn || isDusk {
					c.pointLightBuf = append(c.pointLightBuf, pl)
				}
			}
		}
		// Player and entity lanterns at night — warm glow on all players.
		if isDusk {
			c.pointLightBuf = append(c.pointLightBuf, esf.PointLightPlacement{
				Pos:    esf.Point{X: p.X, Y: p.Y + 2.5, Z: p.Z},
				Radius: 25.0,
				Color:  esf.Color32F{R: 1.0, G: 0.69, B: 0.36, A: 1},
			})
			c.terrainRender.SetPlayerLight(true, p.X, p.Y+2.5, p.Z, 25.0, 1.0, 0.69, 0.36)
			// Other players get lanterns too
			for _, ent := range gs.Entities {
				if ent.IsPlayer && ent.ObjectID != 0 {
					c.pointLightBuf = append(c.pointLightBuf, esf.PointLightPlacement{
						Pos:    esf.Point{X: ent.X, Y: ent.Y + 2.5, Z: ent.Z},
						Radius: 25.0,
						Color:  esf.Color32F{R: 1.0, G: 0.69, B: 0.36, A: 1},
					})
				}
			}
		} else {
			c.terrainRender.SetPlayerLight(false, 0, 0, 0, 0, 0, 0, 0)
		}
		c.terrainRender.SetLightingUniforms(camera.Position.X, camera.Position.Y, camera.Position.Z, gameHour, c.pointLightBuf)

		// Play ambient track: region ID from WorldRegions → day/night track arrays
		if len(c.ambientTracks) > 0 {
			regionID := 0
			if c.terrainLoader != nil && c.terrainLoader.Regions != nil {
				if c.terrainZoneIdx < len(c.terrainLoader.Regions.RegionIDs) {
					regionID = int(c.terrainLoader.Regions.RegionIDs[c.terrainZoneIdx])
				}
			}
			ambID := audio.SelectAmbientTrack(regionID, isDusk)
			if ambID != c.lastAmbientID {
				if track, ok := c.ambientTracks[ambID]; ok {
					c.audioSys.PlayAmbientPCM(audio.AmbientKey(ambID), track.PCM, track.SampleRate, 0.15)
				}
				c.lastAmbientID = ambID
			}
		}

		// Upload + draw each loaded zone.
		// Primary zone first — claims depth at boundary edges.
		// Secondary zones drawn after — GL_LESS fails at equal depth,
		// so primary zone's colors always win at shared edges.
		// Compute visible rooms for portal culling (indoor zones).
		var visibleRooms map[int]bool
		if c.portalNav != nil {
			visibleRooms = c.portalNav.GetVisibleRooms(p.X, p.Y, p.Z)
		}

		if zt := c.terrainLoader.GetZoneTerrain(c.terrainZoneIdx); zt != nil {
			if !c.terrainRender.IsZoneBuilt(c.terrainZoneIdx) {
				c.terrainRender.UploadZoneBatch(c.terrainZoneIdx, zt, 10)
				// Assign room IDs to mesh groups after first build
				if len(zt.Rooms) > 0 {
					c.terrainRender.AssignRooms(zt.Rooms)
				}
			}
			c.terrainRender.DrawZone(c.terrainZoneIdx, &frust, true, visibleRooms)
		}
		for idx := range c.loadedZoneIdxs {
			if idx == c.terrainZoneIdx {
				continue
			}
			zt := c.terrainLoader.GetZoneTerrain(idx)
			if zt == nil {
				continue
			}
			if !c.terrainRender.IsZoneBuilt(idx) {
				c.terrainRender.UploadZoneBatch(idx, zt, 10)
			}
			c.terrainRender.DrawZone(idx, &frust, false, nil) // secondary zones: no portal culling
		}

		// Primary zone: flora, effect volumes, particles.
		zt := c.terrainLoader.GetZoneTerrain(c.terrainZoneIdx)
		if zt != nil {
			if c.floraRender != nil && c.floraRender.HasFlora(zt) {
				if !c.floraRender.IsBuilt() {
					c.floraRender.UploadBatch(zt)
				}
				c.floraRender.Update(p.X, p.Z, zt)
				c.floraRender.Draw(p.X, p.Y, p.Z, &frust)
			}
			if c.effectVolumes != nil && !c.effectVolumes.IsReady() {
				if len(zt.EffectVolumes) > 0 {
					c.effectVolumes.LoadZoneVolumes(zt.EffectVolumes)
				}
			}
			if !c.zoneParticlesSpawned && len(zt.ZoneParticles) > 0 {
				if c.engine.SpellFX != nil && c.engine.SpellFX.Particles != nil {
					ps := c.engine.SpellFX.Particles
					count := 0
					for _, zp := range zt.ZoneParticles {
						pos := [3]float32{zp.Pos.X, zp.Pos.Y, zp.Pos.Z}
						rot := [3]float32{zp.Rot.X, zp.Rot.Y, zp.Rot.Z}
						if ps.CreateZoneEmitter(zp.ParticleDef, pos, rot, zp.Texture, zp.AlwaysOn) != nil {
							count++
						}
					}
					log.Printf("zone particles: spawned %d/%d emitters", count, len(zt.ZoneParticles))
				}
				c.zoneParticlesSpawned = true
			}
		}
	}

	// Draw player model (fall back to green sphere). Player is never culled.
	// PS2 VIWalkControl::UpdateLocomotion -- animation driven by local movement state.
	// Pair 0 = idle, pair 1 = walk, pair 2 = run, pair 4 = fall.
	// In first-person mode, hide the player model (PS2 VIWalkCamera hides character at distance=0).
	dt := rl.GetFrameTime()
	now := time.Now()

	// Player animation — resolved by AnimationManager (combat, debug, landing).
	// Returns dual-pair: locoAnim (lower body) + actionAnim (upper body override).
	playerEquip := entityEquipment(&p, c.modelMgr)
	c.applyTransmog(playerEquip)
	playerLocoAnim, playerActionAnim := c.animMgr.ResolvePlayerAnim(gs, &p, c.engine, now)

	c.animMgr.ConsumePendingPlayerHitFX()

	// PS2 dual speed: callerSpeed = server movement speed / base walk speed.
	const baseWalkSpeed = float32(8.8333)
	playerCallerSpeed := float32(1.0)
	if p.Speed > 0 {
		playerCallerSpeed = p.Speed / baseWalkSpeed
	} else if gs.ParsedCM != nil && gs.ParsedCM.Speed > 0 {
		playerCallerSpeed = gs.ParsedCM.Speed / baseWalkSpeed
	}
	if !firstPerson {
		// Debug model override — visual only, not sent to server
		playerModelID := int32(p.ModelID)
		if c.debugModelID != 0 {
			playerModelID = c.debugModelID
		}
		if c.modelMgr == nil || !c.modelMgr.DrawEntity(playerModelID, p.X, p.Y, p.Z, p.Facing, p.Scale, playerEquip, p.ObjectID, playerLocoAnim, playerActionAnim, dt, true, playerCallerSpeed) {
			rl.DrawSphere(rl.Vector3{X: p.X, Y: p.Y + 5, Z: p.Z}, 5, rl.Green)
		}
		// Draw player weapons (hide in non-humanoid forms like mouse illusion).
		if c.modelMgr != nil && c.modelMgr.ModelIsHumanoid(playerModelID) {
			pri, sec, sh := c.applyTransmogWeapons(p.PrimaryWeapon, p.SecondaryWeapon, p.Shield)
			c.modelMgr.DrawWeapon(int32(pri), playerModelID, p.X, p.Y, p.Z, p.Facing, models.WeaponSlotPrimary)
			c.modelMgr.DrawWeapon(int32(sec), playerModelID, p.X, p.Y, p.Z, p.Facing, models.WeaponSlotSecondary)
			c.modelMgr.DrawWeapon(int32(sh), playerModelID, p.X, p.Y, p.Z, p.Facing, models.WeaponSlotShield)
		}
		// Update bone/weapon emitter positions while player's bone matrices are valid.
		c.updateEntityEmitterPositions(&p)
	}

	// Wire terrain height lookup for entity Y snapping (prevents clipping through slopes).
	if c.terrainLoader != nil && c.terrainZoneIdx >= 0 {
		if zt := c.terrainLoader.GetZoneTerrain(c.terrainZoneIdx); zt != nil {
			gs.SampleHeightFunc = func(x, z, refY float32) (float32, bool) {
				// Fast path: single tile. Fallback: 3×3 for tile boundaries.
				if y, ok := zt.SampleHeightRef(x, z, refY); ok {
					return y, true
				}
				return zt.SampleHeight(x, z, refY)
			}
		}
	}

	// Interpolate entity positions (dead reckoning + correction toward server target).
	gs.InterpolateEntities(dt)

	// Debug: periodic interpolation state dump (every 2 seconds)
	if gs.DebugEntityMovement {
		now := time.Now()
		if now.Sub(gs.LastInterpDebug) > 2*time.Second {
			gs.LastInterpDebug = now
			for _, ent := range gs.Entities {
				if !ent.Interpolating {
					continue
				}
				gap := mathutil.Dist3D(ent.TargetX, ent.TargetY, ent.TargetZ, ent.X, ent.Y, ent.Z)
				velMag := mathutil.Dist3D(ent.VelX, ent.VelY, ent.VelZ, 0, 0, 0)
				if gap > 0.5 || velMag > 0.5 {
					age := now.Sub(ent.LastUpdate).Milliseconds()
					log.Printf("INTERP %q gap=%.2f vel=%.2f age=%dms anim=%d ui=%.0fms",
						ent.Name, gap, velMag, age, ent.Animation, ent.UpdateInterval*1000)
				}
			}
		}
	}

	// Consolidated entity pass: ground-snap + frustum cull + draw + collect visible list.
	// Ground-snap entities to terrain so they don't clip through hills between
	// server updates. Skip when entity is in a water volume (BSP check) —
	// the server Y is at the water surface and ground snap would push them
	// to the lake/river bed. Also skip for swim animation byte (12).
	// Skip entity rendering until terrain is loaded — without ground data,
	// dead NPCs (Y offset by half height) visually fall through the ground plane.
	t0Ents := time.Now()
	entityDrawn := 0
	var groundSnapZT *terrain.ZoneTerrain
	if c.terrainLoader != nil && c.terrainZoneIdx >= 0 {
		groundSnapZT = c.terrainLoader.GetZoneTerrain(c.terrainZoneIdx)
	}
	terrainLoaded := groundSnapZT != nil
	c.visibleEnts = c.visibleEnts[:0]
	for _, ent := range gs.Entities {
		// Skip entities on a different continent (World byte from state channel offset 16).
		if ent.World != p.World {
			continue
		}

		// Server sends exact Y — no ground-snap needed for entities.
		// Only adjust swimming entities slightly for visual water surface.
		if groundSnapZT != nil && ent.Interpolating {
			if ent.Animation == 12 || groundSnapZT.GetEnvFlags(ent.X, ent.Y, ent.Z)&terrain.EnvFlagWater != 0 {
				ent.Y += 0.5
			}
		}

		if !terrainLoaded {
			continue
		}
		// Frustum cull: use bounding sphere around entity position.
		entRadius := c.modelHeight(ent.ModelID, 5.0) * ent.Scale
		if !frust.TestSphere(ent.X, ent.Y+entRadius*0.5, ent.Z, entRadius) {
			continue
		}
		entityDrawn++
		c.visibleEnts = append(c.visibleEnts, ent)

		em := int32(ent.ModelID)
		if em == 0 {
			continue // invisible entity (e.g. "a loose rock") — no model to render
		}
		entEquip := entityEquipment(ent, c.modelMgr)

		entLocoAnim, entActionAnim := c.animMgr.ResolveEntityAnim(ent, now)

		if c.modelMgr != nil && c.modelMgr.DrawEntity(em, ent.X, ent.Y, ent.Z, ent.Facing, ent.Scale, entEquip, ent.ObjectID, entLocoAnim, entActionAnim, dt, false, 1.0) {
			// Draw entity weapons (hide in non-humanoid forms).
			if c.modelMgr.ModelIsHumanoid(em) {
				c.modelMgr.DrawWeapon(int32(ent.PrimaryWeapon), em, ent.X, ent.Y, ent.Z, ent.Facing, models.WeaponSlotPrimary)
				c.modelMgr.DrawWeapon(int32(ent.SecondaryWeapon), em, ent.X, ent.Y, ent.Z, ent.Facing, models.WeaponSlotSecondary)
				c.modelMgr.DrawWeapon(int32(ent.Shield), em, ent.X, ent.Y, ent.Z, ent.Facing, models.WeaponSlotShield)
			}
			// Update bone/weapon emitter positions NOW while shared bone matrices
			// are still valid for this entity (before next entity overwrites them).
			c.updateEntityEmitterPositions(ent)
			continue
		}
		clr := rl.Yellow
		if ent.IsPlayer {
			clr = rl.Color{R: 0, G: 128, B: 255, A: 255}
		}
		rl.DrawSphere(rl.Vector3{X: ent.X, Y: ent.Y + 4, Z: ent.Z}, 4, clr)
	}
	if d := time.Since(t0Ents); d > 500*time.Millisecond {
		log.Printf("SLOW: entity draw took %v (%d entities)", d, entityDrawn)
	}

	// Animation-triggered entity sounds (footsteps, attack grunts).
	c.updateEntitySounds(dt)

	// Bone-attached particle emitters: spawn for new entities, update positions,
	// destroy for removed entities. PS2: CreateParticleEmitters + UpdateParticleEmitters.
	c.updateBoneEmitters(gs)

	// Target reticle — PS2 Selection_Rings HSprite (2 textures: ring + twinkle).
	// PS2 ConstructTextPass halves alpha when blend mode is active: alpha *= 0.5.
	if c.targetObjectID != 0 {
		targetEnt, ok := gs.Entities[c.targetObjectID]
		if !ok && c.targetObjectID == gs.PlayerObjectID {
			targetEnt = &gs.Player
			ok = true
		}
		if ok {
			entHeight := c.modelHeight(targetEnt.ModelID, 5.0) * targetEnt.Scale
			reticlePos := rl.Vector3{X: targetEnt.X, Y: targetEnt.Y + entHeight*0.5, Z: targetEnt.Z}
			conClr := conColorToRL(gs.TargetConColor)
			// PS2 alpha halving: alpha *= 0.5 when blend mode active.
			conClr.A = 128

			// Detect target switch → trigger twinkle flash.
			if c.targetObjectID != c.lastTargetID {
				c.twinkleAlpha = 1.0
				c.lastTargetID = c.targetObjectID
			}

			// Fade twinkle over 0.5 seconds.
			if c.twinkleAlpha > 0 {
				c.twinkleAlpha -= dt * 8.0 // fade out in ~0.12s
				if c.twinkleAlpha < 0 {
					c.twinkleAlpha = 0
				}
			}

			// Distance-independent size: scale with camera distance
			// so the reticle appears the same screen size at any zoom level.
			camDist := mathutil.Dist3D(camera.Position.X, camera.Position.Y, camera.Position.Z, reticlePos.X, reticlePos.Y, reticlePos.Z)
			if camDist < 1 {
				camDist = 1
			}
			reticleSize := camDist * 0.12

			// Slow spin (PS2 Selection_Rings rotates ~30 deg/sec).
			c.reticleSpin += 30.0 * dt
			if c.reticleSpin >= 360.0 {
				c.reticleSpin -= 360.0
			}

			rl.BeginBlendMode(rl.BlendAlpha)
			if c.reticleOK {
				src := rl.Rectangle{X: 0, Y: 0, Width: float32(c.reticleTex.Width), Height: float32(c.reticleTex.Height)}
				size := rl.Vector2{X: reticleSize, Y: reticleSize}
				origin := rl.Vector2{X: reticleSize * 0.5, Y: reticleSize * 0.5}
				rl.DrawBillboardPro(camera, c.reticleTex, src, reticlePos, rl.Vector3{X: 0, Y: 1, Z: 0}, size, origin, c.reticleSpin, conClr)

				// Twinkle overlay — flashes on target switch, fades out.
				if c.twinkleOK && c.twinkleAlpha > 0 {
					twinkleClr := rl.Color{R: 255, G: 255, B: 255, A: uint8(64 * c.twinkleAlpha)}
					twinkleSrc := rl.Rectangle{X: 0, Y: 0, Width: float32(c.twinkleTex.Width), Height: float32(c.twinkleTex.Height)}
					rl.DrawBillboardPro(camera, c.twinkleTex, twinkleSrc, reticlePos, rl.Vector3{X: 0, Y: 1, Z: 0}, size, origin, -c.reticleSpin, twinkleClr)
				}
			} else {
				rl.DrawCircle3D(reticlePos, reticleSize*0.5, rl.Vector3{X: 1, Y: 0, Z: 0}, 90, conClr)
			}
			rl.EndBlendMode()
		}
	} else {
		c.lastTargetID = 0
	}

	// Draw zone effect volumes (fog, dust, leaves, light shafts).
	if c.effectVolumes != nil {
		c.effectVolumes.Update(dt, camera.Position.X, camera.Position.Y, camera.Position.Z)
		c.effectVolumes.Render(camera)
	}

	// Draw weather particles (rain, snow, etc).
	if c.weather != nil {
		gh := c.engine.State.GameHour()
		c.weather.Update(dt, gh, camera.Position.X, camera.Position.Y, camera.Position.Z)
		c.weather.Render(camera)
	}

	// Draw spell visual effects (particles, lightning, sprites).
	c.drawSpellEffects(gs, camera)

	// NavMesh debug overlay (F7)
	if c.navDebug {
		c.drawNavDebug(gs)
	}

	rl.EndMode3D()

	// Skip all 2D HUD/nameplates while loading screen is active
	if c.loadScrFade > 0 {
		return
	}

	// Dead overlay: hide all HUD, deepening red tint over whole screen,
	// "You are Dead." text fades from red to white over same duration.
	if gs.Player.Dead {
		deadAge := float32(now.Sub(gs.Player.DeadSince).Seconds())
		// Progress 0→1 over 10 seconds
		t := deadAge / 10.0
		if t > 1 {
			t = 1
		}
		// Full-screen red tint deepens: alpha 0→150
		rl.DrawRectangle(0, 0, int32(screenWidth), int32(screenHeight),
			rl.Color{R: 100, G: 0, B: 0, A: uint8(t * 150)})

		// Text color: red(200,30,30) → white(255,255,255)
		tr := uint8(200 + t*55)
		tg := uint8(30 + t*225)
		tb := uint8(30 + t*225)

		deadText := "You are Dead."
		deadFontSize := int32(30)
		deadW := c.measureText(deadText, deadFontSize)
		deadX := (int32(screenWidth) - deadW) / 2
		deadY := int32(screenHeight)/3 - deadFontSize/2
		c.drawText(deadText, deadX+2, deadY+2, deadFontSize, rl.Color{R: 0, G: 0, B: 0, A: 200})
		c.drawText(deadText, deadX, deadY, deadFontSize, rl.Color{R: tr, G: tg, B: tb, A: 255})

		// Respawn countdown (10 seconds) — same red→white color transition
		remaining := 10.0 - deadAge
		if remaining < 0 {
			remaining = 0
		}
		countText := fmt.Sprintf("Respawn in %d...", int(remaining)+1)
		if remaining <= 0 {
			countText = "Respawning..."
		}
		countSize := int32(20)
		countW := c.measureText(countText, countSize)
		countX := (int32(screenWidth) - countW) / 2
		countY := deadY + deadFontSize + 16
		c.drawText(countText, countX+1, countY+1, countSize, rl.Color{R: 0, G: 0, B: 0, A: 180})
		c.drawText(countText, countX, countY, countSize, rl.Color{R: tr, G: tg, B: tb, A: 255})
		return
	}

	// --- VINameSprite nameplate rendering ---
	// PS2 pipeline: VICSprite::SetName -> NameColors[enum] -> VINameSprite billboard.
	// VINameSprite is a world-space billboard sprite that scales with camera distance.
	// We approximate this by scaling font size inversely with distance.
	//
	// VICSprite::NameColors static table at EE 0x004E0BF8 (3 entries, RGBA):
	//   [0] RGB(212, 192, 111) -- golden yellow (NPC)
	//   [1] RGB(100, 175, 220) -- steel blue (Player/Pet)
	//   [2] RGB(90,  255,  80) -- bright green (GM/special)
	//
	// State channel byte (offset 142) -> table index mapping (from CLIENT disasm):
	//   byte 0 -> NameColors[0]   (NPC)
	//   byte 2 -> NameColors[1]   (Player)
	//   other  -> NameColors[0]   (fallback)

	const nameplateRange float32 = 40 // max distance to show nameplates
	const nameplateRangeSq = nameplateRange * nameplateRange

	// Mouse hover detection for inspection tooltip (VIInspectionWND).
	mousePos := rl.GetMousePosition()
	var hoverEnt *game.Entity
	var hoverDistSq float32 = 40 * 40 // max 40px from entity center to trigger

	// Draw nameplates for visible entities (already frustum-culled).
	for _, ent := range c.visibleEnts {
		if ent.Name == "" {
			continue
		}
		// Always show nameplates for all entities with names.
		pdx, pdz := ent.X-p.X, ent.Z-p.Z
		if pdx*pdx+pdz*pdz > nameplateRangeSq {
			continue
		}
		height := c.modelHeight(ent.ModelID, 1.8)
		scale := ent.Scale
		if scale <= 0 {
			scale = 1.0
		}
		worldPos := rl.Vector3{X: ent.X, Y: ent.Y + height*scale + 0.3, Z: ent.Z}
		screenPos := rl.GetWorldToScreen(worldPos, camera)
		if screenPos.X < -100 || screenPos.X > float32(rl.GetScreenWidth())+100 ||
			screenPos.Y < -50 || screenPos.Y > float32(rl.GetScreenHeight())+50 {
			continue
		}
		if ent.ObjectID == c.targetObjectID {
			// Targeted entity: draw with brackets and brighter color
			c.drawNameplate(screenPos, "[ "+ent.Name+" ]", rl.White)
		} else {
			c.drawNameplate(screenPos, ent.Name, nameColorFromByte(ent.NameColor))
		}

		// Aggro indicator: NPC targeting the player
		if ent.Target == gs.PlayerObjectID && ent.IsNPC && gs.PlayerObjectID != 0 {
			aggroPos := rl.Vector2{X: screenPos.X, Y: screenPos.Y - 14}
			c.drawNameplate(aggroPos, "!", rl.Color{R: 255, G: 60, B: 60, A: 255})
		}

		// Check mouse hover — use body center (halfway between feet and nameplate)
		bodyScreen := rl.GetWorldToScreen(rl.Vector3{X: ent.X, Y: ent.Y + height*scale*0.5, Z: ent.Z}, camera)
		mdx := mousePos.X - bodyScreen.X
		mdy := mousePos.Y - bodyScreen.Y
		d2 := mdx*mdx + mdy*mdy
		if d2 < hoverDistSq {
			hoverDistSq = d2
			hoverEnt = ent
		}
	}

	// Draw nameplate for local player + include in hover/click detection.
	if p.Name != "" && !firstPerson {
		height := c.modelHeight(p.ModelID, 1.8)
		scale := p.Scale
		if scale <= 0 {
			scale = 1.0
		}
		worldPos := rl.Vector3{X: camera.Target.X, Y: p.Y + height*scale + 0.3, Z: camera.Target.Z}
		screenPos := rl.GetWorldToScreen(worldPos, camera)
		sw, sh := float32(rl.GetScreenWidth()), float32(rl.GetScreenHeight())
		if screenPos.X >= -100 && screenPos.X <= sw+100 &&
			screenPos.Y >= -50 && screenPos.Y <= sh+50 {
			c.drawNameplate(screenPos, p.Name, nameColorFromByte(p.NameColor))

			// Hover detection for self (same as other entities)
			bodyScreen := rl.GetWorldToScreen(rl.Vector3{X: p.X, Y: p.Y + height*scale*0.5, Z: p.Z}, camera)
			mdx := mousePos.X - bodyScreen.X
			mdy := mousePos.Y - bodyScreen.Y
			d2 := mdx*mdx + mdy*mdy
			if d2 < hoverDistSq {
				hoverDistSq = d2
				hoverEnt = &gs.Player
			}
		}
	}

	// --- VIInspectionWND: right-click inspect tooltip ---
	if hoverEnt != nil && rl.IsMouseButtonDown(rl.MouseButtonRight) {
		c.drawInspectionWnd(hoverEnt, mousePos)
	}

	// --- Target inspect tooltip (Square held on controller) ---
	squareHeld := gpAvailable() && rl.IsGamepadButtonDown(0, rl.GamepadButtonRightFaceLeft)
	if c.targetObjectID != 0 && squareHeld {
		if ent, ok := gs.Entities[c.targetObjectID]; ok {
			height := c.modelHeight(ent.ModelID, 1.8)
			scale := ent.Scale
			if scale <= 0 {
				scale = 1.0
			}
			worldPos := rl.Vector3{X: ent.X, Y: ent.Y + height*scale + 0.3, Z: ent.Z}
			sp := rl.GetWorldToScreen(worldPos, camera)
			c.drawInspectionWnd(ent, rl.Vector2{X: sp.X, Y: sp.Y + 10})
		}
	}

	// --- Floating damage/heal numbers ---
	// Expire old texts and render active ones
	const floatDuration = 2.0 // seconds to display
	const floatRise = 40.0    // pixels to rise over duration
	activeTexts := gs.FloatingTexts[:0]
	for i := range gs.FloatingTexts {
		ft := &gs.FloatingTexts[i]
		age := now.Sub(ft.SpawnTime).Seconds()
		if age >= floatDuration {
			continue // expired
		}
		activeTexts = append(activeTexts, *ft)
		// Get entity height for positioning above head
		progress := float32(age / floatDuration)
		yOffset := float32(1.8) + progress*1.5 // rise in world space
		worldPos := rl.Vector3{X: ft.X, Y: ft.Y + yOffset, Z: ft.Z}
		screenPos := rl.GetWorldToScreen(worldPos, camera)
		if screenPos.X < -100 || screenPos.X > float32(rl.GetScreenWidth())+100 ||
			screenPos.Y < -50 || screenPos.Y > float32(rl.GetScreenHeight())+50 {
			continue
		}
		// Fade out over last 40% of duration
		alpha := uint8(255)
		if progress > 0.6 {
			alpha = uint8(255 * (1.0 - (progress-0.6)/0.4))
		}
		clr := rl.Color{R: 255, G: 60, B: 60, A: alpha} // red for damage
		if ft.IsHeal {
			clr = rl.Color{R: 60, G: 255, B: 60, A: alpha} // green for heal
		}
		fontSize := int32(18)
		textW := c.measureText(ft.Text, fontSize)
		c.drawText(ft.Text, int32(screenPos.X)-textW/2, int32(screenPos.Y)-int32(floatRise*progress), fontSize, clr)
	}
	gs.FloatingTexts = activeTexts

	// HUD (screen space) -- PS2 VI UI rendering

	// Debug anim indicator (top center)
	if c.animMgr.DebugAnimActive {
		animStr := fmt.Sprintf("ANIM PAIR %d  (F8=next  Shift+F8=prev  `=off)", c.animMgr.DebugAnimOverride)
		animW := c.measureText(animStr, 16)
		c.drawText(animStr, (screenWidth-animW)/2, 10, 16, rl.Yellow)
	}
	// Debug spell effect indicator (below anim indicator)
	if c.debugFXActive && len(c.debugFXIDs) > 0 {
		dictID := c.debugFXIDs[c.debugFXIndex]
		fxStr := fmt.Sprintf("SPELL FX %d/%d  DictID=0x%08X  (F9=next  Shift+F9=prev)",
			c.debugFXIndex+1, len(c.debugFXIDs), uint32(dictID))
		c.drawCenteredText(fxStr, 28, 16, rl.Color{R: 100, G: 255, B: 200, A: 255})
	}
	// Water state indicator
	if c.waterState == terrain.WaterSurface {
		c.drawText("SWIMMING", screenWidth-100, 10, 16, rl.Color{R: 100, G: 175, B: 255, A: 255})
	} else if c.waterState == terrain.WaterUnder {
		c.drawText("UNDERWATER", screenWidth-120, 10, 16, rl.Color{R: 50, G: 100, B: 200, A: 255})
	}

	// Game clock (top-right, below water state)
	if c.engine.State.TimeValid {
		gh := c.engine.State.GameHour()
		hour := int(gh) % 24
		minute := int((gh - float32(int(gh))) * 60)
		clockStr := fmt.Sprintf("%02d:%02d", hour, minute)
		clockW := c.measureText(clockStr, 16)
		c.drawText(clockStr, screenWidth-clockW-10, 30, 16, rl.Color{R: 200, G: 200, B: 160, A: 255})
	}

	// === PS2 VI HUD -- positions scaled from 320x224 coordinate system ===
	// PS2->screen scale: X * 2.5, Y * 2.679 (320->800, 224->600)
	// const (
	// 	uiScaleX = float32(screenWidth) / 320.0
	// 	uiScaleY = float32(screenHeight) / 224.0
	// )

	// Compass (top-left, PS2 ~(2,2) 20x20)
	c.drawCompass()

	// HP / Power / XP bars (right of compass, vertically centered)
	const (
		compassX = float32(8)
		compassY = float32(14) // below position text
		compassW = float32(48)
		compassH = float32(56)
		barW     = float32(80)
		barH     = float32(8)
		barGap   = float32(3)
	)
	// 4 bars: HP, Power, XP, Bubble = 4*barH + 3*barGap = 41
	totalBarsH := 4*barH + 3*barGap
	barX := compassX + compassW + 4
	barStartY := compassY + (compassH-totalBarsH)/2
	hpY := barStartY
	powY := hpY + barH + barGap
	xpY := powY + barH + barGap
	if p.MaxHP > 0 {
		hpFrac := float32(p.HP) / float32(p.MaxHP)
		c.drawTexturedBar(barX, hpY, barW, barH, texBarHP, hpFrac, "")
	}
	if p.MaxPower > 0 {
		powFrac := float32(p.Power) / float32(p.MaxPower)
		c.drawTexturedBar(barX, powY, barW, barH, texBarPower, powFrac, "")
	}
	{
		xpFrac := float32(p.XPPct) / 255.0
		c.drawTexturedBar(barX, xpY, barW, barH, texBarXP, xpFrac, "")
		// Bubble bar (pink) -- shows progress within current 20% bubble
		bubbleSize := float32(0.2)
		bubbleFrac := float32(0)
		if xpFrac > 0 {
			rem := xpFrac - float32(int(xpFrac/bubbleSize))*bubbleSize
			bubbleFrac = rem / bubbleSize
			// If exactly on a 20% boundary and not 0, show full
			if rem == 0 && xpFrac > 0 {
				bubbleFrac = 1.0
			}
		}
		bubbleY := xpY + barH + barGap
		pink := rl.Color{R: 255, G: 100, B: 180, A: 255}
		c.drawTexturedBarTinted(barX, bubbleY, barW, barH, texBarXP, bubbleFrac, "", pink)
	}

	// Group member HUD (below compass/player bars)
	c.drawGroupHUD()

	// Spell bar -- vertical column of spell slots with scroll arrows (top-right)
	c.drawSpellBar()

	// Buff/debuff bar -- to the left of spell bar
	c.drawBuffBar()

	// Casting bar -- centered, shows spell cast progress
	if gs.CastDurationMs > 0 {
		castElapsed := float32(now.Sub(gs.CastStartTime).Seconds())
		castTotal := float32(gs.CastDurationMs) / 1000.0
		if castElapsed < castTotal {
			castFrac := castElapsed / castTotal
			castBarW := float32(200)
			castBarH := float32(14)
			castBarX := float32(screenWidth)/2 - castBarW/2
			castBarY := float32(screenHeight) - 120
			// Background
			rl.DrawRectangle(int32(castBarX)-1, int32(castBarY)-1, int32(castBarW)+2, int32(castBarH)+2, colorOverlay)
			// Fill (gold)
			fillW := int32(castBarW * castFrac)
			rl.DrawRectangle(int32(castBarX), int32(castBarY), fillW, int32(castBarH), rl.Color{R: 212, G: 192, B: 111, A: 220})
			// Label: spell name (fallback to target name or "Casting...")
			label := gs.CastSpellName
			if label == "" {
				label = gs.CastTargetName
			}
			if label == "" {
				label = "Casting..."
			}
			lw := c.measureText(label, 10)
			c.drawText(label, int32(castBarX)+(int32(castBarW)-lw)/2, int32(castBarY)+2, 10, rl.White)
		} else {
			gs.CastDurationMs = 0 // cast complete, clear
		}
	}

	// Position + World display (top-left, above compass)
	{
		worldNames := map[byte]string{0: "Tunaria", 1: "Rathe", 2: "Odus", 3: "Lavastorm", 4: "Plane of Sky", 5: "Secrets"}
		worldName := worldNames[gs.Player.World]
		posText := fmt.Sprintf("%.0f, %.0f, %.0f  %s", p.X, p.Y, p.Z, worldName)
		c.drawText(posText, 8, 0, 11, rl.Color{R: 200, G: 200, B: 200, A: 180})
	}

	// Camera mode HUD (bottom center, same style as POS display)
	{
		modeNames := [3]string{"Tether", "Follow", "First Person"}
		modeText := fmt.Sprintf("Y/R3 Reset/Default  U/Select Camera: %s", modeNames[c.camMode])
		cx := int32(rl.GetScreenWidth()) - c.measureText(modeText, 11) - 8
		cy := int32(rl.GetScreenHeight()) - 18
		c.drawText(modeText, cx, cy, 11, rl.Color{R: 200, G: 200, B: 200, A: 180})
	}

	// Target HUD panel (bottom-left with padding)
	// PS2 ~(2,170) -- bottom-left area
	if c.targetObjectID != 0 {
		if targetEnt, ok := gs.Entities[c.targetObjectID]; ok {
			c.drawTargetHUD(targetEnt)
		} else if c.targetObjectID == gs.PlayerObjectID {
			c.drawTargetHUD(&gs.Player)
		}
	}

	// Dialogue box (centered on screen)
	if gs.DialogueActive && gs.DialogueText != "" {
		c.drawDialogueBox(gs)
	}

	// Debug animation overlay (top center, below pair indicator)
	if c.modelMgr != nil && (models.DebugAnimUpper >= 0 || models.DebugAnimLower >= 0) {
		animStr2 := fmt.Sprintf("ANIM DEBUG: upper=%d lower=%d  PgUp/PgDn=upper Home/End=lower",
			models.DebugAnimUpper, models.DebugAnimLower)
		animW2 := c.measureText(animStr2, 14)
		c.drawText(animStr2, (screenWidth-animW2)/2, 30, 14, rl.Yellow)
	}

	// Chat log -- bottom-left, black box, opaque when active, fades to transparent
	const (
		chatFontSize  = 12
		chatLineH     = int32(14) // line height in pixels
		chatMaxLines  = 8         // max visible wrapped lines
		chatPadX      = int32(4)
		chatPadY      = int32(4)
	)
	chatW := int32(screenWidth) * 2 / 5 // 40% of screen width
	chatH := chatPadY*2 + chatLineH*chatMaxLines
	chatX := int32(screenWidth) - chatW - 4 // right edge with small margin
	chatY := int32(screenHeight) - chatH - 4

	// Detect new chat messages -> reset activity timer
	if len(gs.ChatLog) > c.chatLastCount {
		c.chatLastActivity = time.Now()
		c.chatLastCount = len(gs.ChatLog)
		// Auto-scroll to bottom on new message (unless user scrolled up)
		if c.chatScrollOffset == 0 {
			// already at bottom, stay there
		}
	}

	// Compute chat alpha: fully opaque for 5s after activity, then fade over 3s to 10% idle
	idleAlpha := uint8(25) // 10% opacity when idle
	chatAlpha := idleAlpha
	if c.chatTyping {
		chatAlpha = 200 // fully opaque when typing
	} else if c.chatScrollOffset > 0 {
		chatAlpha = 180 // opaque when scrolled up
	} else if !c.chatLastActivity.IsZero() {
		elapsed := time.Since(c.chatLastActivity).Seconds()
		if elapsed < 5.0 {
			chatAlpha = 180 // fully opaque
		} else if elapsed < 8.0 {
			// Fade from 180 -> idleAlpha over 3 seconds
			chatAlpha = uint8(float64(180-idleAlpha)*(1.0-(elapsed-5.0)/3.0)) + idleAlpha
		}
	}

	if c.viui != nil && c.viui.OK() {
		chatTint := rl.Color{R: chatAlpha, G: chatAlpha, B: chatAlpha, A: chatAlpha}
		c.viui.BlitFillFrame(float32(chatX), float32(chatY), float32(chatW), float32(chatH), ui.FrameLine, ui.FillBlack, chatTint)
	} else {
		rl.DrawRectangle(chatX, chatY, chatW, chatH, rl.Color{R: 0, G: 0, B: 0, A: chatAlpha})
	}

	// Chat mode button (top-right of chat panel)
	c.drawChatModeButton(chatX, chatY, chatW)

	// Word-wrap chat messages and collect visible lines (bottom-up)
	type chatLine struct {
		text    string
		r, g, b uint8
	}
	textW := chatW - chatPadX*2
	var wrappedLines []chatLine
	for _, msg := range gs.ChatLog {
		lines := c.wrapText(msg.Text, chatFontSize, textW)
		for _, ln := range lines {
			wrappedLines = append(wrappedLines, chatLine{ln, msg.ColorR, msg.ColorG, msg.ColorB})
		}
	}

	// Chat scroll input: mouse wheel over chat area, or R2+dpad
	mx := float32(rl.GetMouseX())
	my := float32(rl.GetMouseY())
	mouseInChat := mx >= float32(chatX) && mx < float32(chatX+chatW) &&
		my >= float32(chatY) && my < float32(chatY+chatH)
	if mouseInChat {
		wheel := int(rl.GetMouseWheelMove())
		if wheel != 0 {
			c.chatScrollOffset += wheel // wheel up = positive = scroll up
			c.chatLastActivity = time.Now()
		}
	}
	// Controller: R2 + dpad up/down (not when menu open — R2 is tab nav there)
	if gpAvailable() && !c.menuOpen && gpDown(rl.GamepadButtonRightTrigger2) {
		if gpPressed(rl.GamepadButtonLeftFaceUp) {
			c.chatScrollOffset++
			c.chatLastActivity = time.Now()
		}
		if gpPressed(rl.GamepadButtonLeftFaceDown) {
			c.chatScrollOffset--
			c.chatLastActivity = time.Now()
		}
	}

	// Clamp scroll offset
	maxScroll := len(wrappedLines) - chatMaxLines
	if maxScroll < 0 {
		maxScroll = 0
	}
	if c.chatScrollOffset > maxScroll {
		c.chatScrollOffset = maxScroll
	}
	if c.chatScrollOffset < 0 {
		c.chatScrollOffset = 0
	}

	// Show chatMaxLines starting from scroll position
	endIdx := len(wrappedLines) - c.chatScrollOffset
	startIdx := endIdx - chatMaxLines
	if startIdx < 0 {
		startIdx = 0
	}
	if endIdx < 0 {
		endIdx = 0
	}
	textAlpha := chatAlpha
	for i, ln := range wrappedLines[startIdx:endIdx] {
		clr := rl.Color{R: ln.r, G: ln.g, B: ln.b, A: textAlpha}
		c.drawText(ln.text, chatX+chatPadX, chatY+chatPadY+int32(i)*chatLineH, chatFontSize, clr)
	}

	// Scrollbar (right edge of chat panel, only when scrollable)
	if maxScroll > 0 {
		sbW := int32(6)
		sbX := chatX + chatW - sbW - 2
		sbAreaY := chatY + chatPadY
		sbAreaH := chatLineH * chatMaxLines
		// Thumb size proportional to visible/total
		thumbH := int32(float32(sbAreaH) * float32(chatMaxLines) / float32(len(wrappedLines)))
		if thumbH < 12 {
			thumbH = 12
		}
		// Thumb position: 0 scroll = bottom, maxScroll = top
		scrollFrac := float32(c.chatScrollOffset) / float32(maxScroll)
		thumbY := sbAreaY + int32(float32(sbAreaH-thumbH)*(1.0-scrollFrac))
		rl.DrawRectangle(sbX, sbAreaY, sbW, sbAreaH, rl.Color{R: 255, G: 255, B: 255, A: chatAlpha / 4})
		rl.DrawRectangle(sbX, thumbY, sbW, thumbH, rl.Color{R: 255, G: 255, B: 255, A: chatAlpha})

		// Scrollbar drag: start on click in scrollbar area, continue while held
		sbHitX := sbX - 6 // generous hit area
		if rl.IsMouseButtonPressed(rl.MouseButtonLeft) && mx >= float32(sbHitX) && mx <= float32(sbX+sbW+6) &&
			my >= float32(sbAreaY) && my <= float32(sbAreaY+sbAreaH) {
			c.chatDragging = true
		}
		if !rl.IsMouseButtonDown(rl.MouseButtonLeft) {
			c.chatDragging = false
		}
		if c.chatDragging {
			// Map mouse Y to scroll position (top=max, bottom=0)
			clickFrac := (my - float32(sbAreaY)) / float32(sbAreaH)
			if clickFrac < 0 {
				clickFrac = 0
			}
			if clickFrac > 1 {
				clickFrac = 1
			}
			c.chatScrollOffset = int(float32(maxScroll) * (1.0 - clickFrac))
			if c.chatScrollOffset < 0 {
				c.chatScrollOffset = 0
			}
			if c.chatScrollOffset > maxScroll {
				c.chatScrollOffset = maxScroll
			}
			c.chatLastActivity = time.Now()
		}
	} else {
		c.chatDragging = false
	}

	// Chat input line (above chat box when typing)
	if c.chatTyping {
		inputH := int32(18)
		inputY := chatY - inputH + 1
		rl.DrawRectangle(chatX, inputY, chatW, inputH, rl.Color{R: 0, G: 0, B: 0, A: 220})
		cursor := "_"
		if int(rl.GetTime()*2)%2 == 0 {
			cursor = ""
		}
		c.drawText("> "+c.chatBuffer+cursor, chatX+chatPadX, inputY+2, 14, rl.Yellow)
	}

	// GM persistent HUD overlays (always visible when enabled)
	c.drawGMDisplayPos()
	c.drawGMStats()

	// GM menu overlay (may set c.engine = nil on teleport disconnect)
	c.drawGMMenu()
	if c.engine == nil {
		return
	}

	// Merchant window overlay
	c.drawMerchantWindow()

	// Blacksmith/repair window overlay
	c.drawBlacksmithWindow()

	// Item inspect window overlay
	c.drawInspectWindow()

	// Main menu overlay
	if c.menuOpen {
		c.drawMenu()
	}

	// Minimap (bottom center, toggle with N key)
	if c.minimapOn && !c.worldMapOpen {
		c.drawMinimap()
	}

	// World map overlay
	c.drawWorldMap()

	// Music player HUD (always on top of menu)
	if c.musicHudOn {
		c.drawMusicHud()
	}

	// PS2 Hotkey Menu overlay (L2 + D-pad)
	c.drawHotkeyMenu()

	// Tester presence notifications (top-center, slide down)
	c.drawPresenceNotifs()

	// Logout countdown overlay.
	if c.logoutActive {
		remaining := time.Until(c.logoutTime)
		if remaining < 0 {
			remaining = 0
		}
		secs := int(remaining.Seconds()) + 1
		msg := fmt.Sprintf("Logging out in %d...", secs)
		msgW := c.measureText(msg, 22)
		boxW := msgW + 40
		boxH := int32(70)
		boxX := (screenWidth - boxW) / 2
		boxY := int32(160)
		drawPanel(boxX, boxY, boxW, boxH, rl.Color{R: 0, G: 0, B: 0, A: 200}, colorGold)
		c.drawCenteredText(msg, boxY+10, 22, colorGoldTitle)
		c.drawCenteredText("CTRL+X force quit  |  ESC cancel", boxY+42, 14, colorDim)
	}
}

// playBGMFromISO reads a BGM file from the game ISO and streams it via raylib Music.
func (c *Client) playBGMFromISO(bgmName string) {
	if c.audioSys == nil || c.isoPath == "" {
		return
	}
	data, err := esf.ReadISOFileRaw(c.isoPath, bgmName)
	if err != nil {
		return
	}
	c.audioSys.PlayBGMData(bgmName, data, c.bgmVolume)
}

// triggerSpellAnim forces an animation on an entity from a spell effect callback.
// Used by PlayAttack (type 15) and SetAction (type 2) to trigger attack/cast anims.
func (c *Client) triggerSpellAnim(actorObjID uint32, animByte byte) {
	if c.animMgr == nil {
		return
	}
	// For the local player, use the player combat state.
	if c.engine != nil && c.engine.State != nil && actorObjID == c.engine.State.Player.ObjectID {
		cs := &c.animMgr.playerCombat
		modelID := int32(c.engine.State.Player.ModelID)
		if c.animMgr.DebugModelID != 0 {
			modelID = c.animMgr.DebugModelID
		}
		cs.combatAnim = animByte
		cs.combatAnimEnd = time.Now().Add(c.animMgr.animDur(modelID, animByte))
		// Don't set inCombat — spell cast anims should not trigger combat stance.
		// Combat stance only comes from actual attacks/hits (attackCount changes).
		cs.lastActivity = time.Now()
		return
	}
	// For other entities, use the per-entity combat state.
	if c.animMgr.entityStates == nil {
		c.animMgr.entityStates = make(map[uint32]*combatState)
	}
	es := c.animMgr.entityStates[actorObjID]
	if es == nil {
		es = &combatState{}
		c.animMgr.entityStates[actorObjID] = es
	}
	// Look up model ID for correct animation duration.
	var modelID int32
	if c.engine != nil && c.engine.State != nil {
		for _, ent := range c.engine.State.Entities {
			if ent.ObjectID == actorObjID {
				modelID = int32(ent.ModelID)
				break
			}
		}
	}
	es.combatAnim = animByte
	es.combatAnimEnd = time.Now().Add(c.animMgr.animDur(modelID, animByte))
	// Don't set inCombat — spell cast anims should not trigger combat stance.
	es.lastActivity = time.Now()
}

// updateBoneEmitters manages CSprite bone-attached particle emitters for all entities.
// PS2: CreateParticleEmitters (0x00423A68) + UpdateParticleEmitters (0x00423B28).
//
// For each visible entity with PartEmitters:
//   - First time: create particle emitters, track per bone index
//   - Every frame: update emitter position from animated bone world position
//
// For entities that disappear: destroy their emitters.
func (c *Client) updateBoneEmitters(gs *game.GameState) {
	ps := c.getParticleSystem()
	if ps == nil || c.modelMgr == nil {
		return
	}

	// Initialize tracking map on first call
	if c.boneEmitters == nil {
		c.boneEmitters = make(map[uint32][]boneEmitterEntry)
	}

	// Track which entities are visible this frame
	visibleSet := make(map[uint32]bool, len(c.visibleEnts))
	for _, ent := range c.visibleEnts {
		visibleSet[ent.ObjectID] = true
	}

	// Spawn emitters for new entities, update positions for existing
	for _, ent := range c.visibleEnts {
		modelID := int32(ent.ModelID)
		model, ok := c.modelMgr.GetCachedModel(modelID)
		if !ok || model == nil || len(model.PartEmitters) == 0 {
			continue
		}

		entries, exists := c.boneEmitters[ent.ObjectID]
		if !exists {
			// First time seeing this entity — create emitters
			// PS2: CreateParticleEmitters iterates PartEmitters array,
			// resolves PD DictID, calls Create on embedded VIParticleEmitter.
			entries = c.createBoneEmitters(ps, model, ent)
			if len(entries) > 0 {
				c.boneEmitters[ent.ObjectID] = entries
				log.Printf("[BONE-FX] created %d emitters for entity %d (model 0x%08X)",
					len(entries), ent.ObjectID, uint32(modelID))
			}
		}
		// NOTE: Position updates moved to updateEntityEmitterPositions(),
		// called inline after each entity's DrawEntity+DrawWeapon while
		// shared bone matrices are still valid for that entity.
	}

	// Destroy emitters for entities that are no longer visible
	for objID, entries := range c.boneEmitters {
		if visibleSet[objID] {
			continue
		}
		for _, be := range entries {
			ps.DestroyEmitterByPtr(be.emitter)
		}
		delete(c.boneEmitters, objID)
	}

	// Weapon particle emitters: spawn for new weapon models, update positions.
	c.updateWeaponEmitters(gs, ps, visibleSet)
}

// updateEntityEmitterPositions updates bone and weapon emitter positions for a single entity.
// Must be called immediately after DrawEntity+DrawWeapon while the shared CharModel's bone
// matrices (AnimRHandMat, lastSkinMatrices) still contain this entity's data.
func (c *Client) updateEntityEmitterPositions(ent *game.Entity) {
	// Bone emitters
	if entries, ok := c.boneEmitters[ent.ObjectID]; ok {
		for i := range entries {
			be := &entries[i]
			if be.emitter == nil {
				continue
			}
			if be.trackBone {
				bx, by, bz, ok := c.modelMgr.BoneWorldPosByIdx(
					be.modelID, ent.ObjectID, be.boneIdx,
					ent.X, ent.Y, ent.Z, ent.Facing, ent.Scale,
				)
				if ok {
					be.emitter.SetPosition(bx, by, bz)
				}
			} else {
				be.emitter.SetPosition(ent.X, ent.Y, ent.Z)
			}
		}
	}

	// Weapon emitters
	if entries, ok := c.weaponEmitters[ent.ObjectID]; ok {
		for i := range entries {
			we := &entries[i]
			if we.emitter == nil {
				continue
			}
			wx, wy, wz, ok := c.modelMgr.WeaponPointWorldPos(
				int32(ent.ModelID), ent.ObjectID, we.localPt,
				ent.X, ent.Y, ent.Z, ent.Facing, we.slot,
			)
			if ok {
				we.emitter.SetPosition(wx, wy, wz)
			}
			we.emitter.SetFacing(ent.Facing)
		}
	}
}

// createBoneEmitters spawns particle emitters for all PartEmitter entries on a model.
// Returns the tracking entries for per-frame bone position updates.
func (c *Client) createBoneEmitters(ps *game.ParticleSystem, model *models.CharModel, ent *game.Entity) []boneEmitterEntry {
	var entries []boneEmitterEntry

	for i, pe := range model.PartEmitters {
		// PS2: skip if PDDictID == -1 (0xFFFFFFFF) — no particle definition assigned
		if pe.PDDictID == -1 {
			continue
		}

		// Find the ParticleDefinition by DictID
		var pd *esf.ParticleDefinition
		for _, d := range model.PartDefs {
			if d != nil && d.DictID == pe.PDDictID {
				pd = d
				break
			}
		}
		if pd == nil {
			log.Printf("[BONE-FX] emitter %d: PD DictID 0x%08X not found in model PartDefs", i, uint32(pe.PDDictID))
			continue
		}

		// Get texture from CHAR.ESF
		tex := c.modelMgr.GetPartDefTexture(pd.TexDictID)

		// Get initial bone world position
		pos := [3]float32{ent.X, ent.Y, ent.Z}
		bx, by, bz, ok := c.modelMgr.BoneWorldPosByIdx(
			int32(ent.ModelID), ent.ObjectID, int(pe.BoneNodeIdx),
			ent.X, ent.Y, ent.Z, ent.Facing, ent.Scale,
		)
		if ok {
			pos = [3]float32{bx, by, bz}
		}

		em := ps.CreateModelEmitter(pd, pos, tex)
		if em == nil {
			continue
		}

		entries = append(entries, boneEmitterEntry{
			emitter:   em,
			boneIdx:   int(pe.BoneNodeIdx),
			modelID:   int32(ent.ModelID),
			trackBone: pe.TrackBone != 0,
		})
	}
	return entries
}

// updateWeaponEmitters manages particle emitters from weapon GroupSprite ParticleSprite members.
// Weapons with visual auras (epic weapons) have embedded ParticleDefinitions in ITEM.ESF.
func (c *Client) updateWeaponEmitters(gs *game.GameState, ps *game.ParticleSystem, visibleSet map[uint32]bool) {
	if c.weaponEmitters == nil {
		c.weaponEmitters = make(map[uint32][]weaponEmitterEntry)
	}

	// Helper to check and create weapon emitters for an entity's weapon slot
	checkWeapon := func(ent *game.Entity, weaponID uint32, slot models.WeaponSlot) {
		if weaponID == 0 {
			return
		}
		itemModel, ok := c.modelMgr.GetCachedItem(int32(weaponID))
		if !ok || itemModel == nil || len(itemModel.WeaponParticles) == 0 {
			return
		}
		// Check if we already have emitters for this entity
		entries := c.weaponEmitters[ent.ObjectID]
		alreadyHas := false
		for _, we := range entries {
			if we.weaponID == int32(weaponID) && we.slot == slot {
				alreadyHas = true
				break
			}
		}
		if alreadyHas {
			return
		}
		// Create emitters for this weapon's particle effects
		for _, wpe := range itemModel.WeaponParticles {
			pd := wpe.PS.ParticleDef
			if pd == nil {
				continue
			}
			tex := c.modelMgr.GetItemPartDefTexture(pd.TexDictID)

			// Offset emitter along weapon local Z axis toward blade tip.
			// Weapon meshes extend along +Z: grip near Z=0, blade tip at Z≈2.2.
			bladeOffset := [3]float32{0, 0, 1.25}
			pos := [3]float32{ent.X, ent.Y, ent.Z}
			wx, wy, wz, ok := c.modelMgr.WeaponPointWorldPos(
				int32(ent.ModelID), ent.ObjectID, bladeOffset,
				ent.X, ent.Y, ent.Z, ent.Facing, slot,
			)
			if ok {
				pos = [3]float32{wx, wy, wz}
			}

			em := ps.CreateModelEmitter(pd, pos, tex)
			if em == nil {
				continue
			}
			em.SetFacing(ent.Facing)
			entries = append(entries, weaponEmitterEntry{
				emitter:  em,
				weaponID: int32(weaponID),
				slot:     slot,
				localPt:  bladeOffset,
			})
		}
		if len(entries) > 0 {
			c.weaponEmitters[ent.ObjectID] = entries
			log.Printf("[WEAPON-FX] created %d emitters for entity %d weapon 0x%08X",
				len(entries), ent.ObjectID, weaponID)
		}
	}

	// Check all visible entities for weapon particles
	for _, ent := range c.visibleEnts {
		checkWeapon(ent, ent.PrimaryWeapon, models.WeaponSlotPrimary)
		checkWeapon(ent, ent.SecondaryWeapon, models.WeaponSlotSecondary)
	}
	// Also check the player
	p := &gs.Player
	if p.ObjectID != 0 {
		checkWeapon(p, p.PrimaryWeapon, models.WeaponSlotPrimary)
		checkWeapon(p, p.SecondaryWeapon, models.WeaponSlotSecondary)
	}

	// NOTE: Position updates moved to updateEntityEmitterPositions(),
	// called inline after each entity's DrawEntity+DrawWeapon while
	// shared bone matrices are still valid for that entity.

	// Clean up emitters for entities that are no longer visible
	for objID, entries := range c.weaponEmitters {
		if visibleSet[objID] || objID == gs.PlayerObjectID {
			continue
		}
		for _, we := range entries {
			ps.DestroyEmitterByPtr(we.emitter)
		}
		delete(c.weaponEmitters, objID)
	}
}

// getParticleSystem returns the active ParticleSystem, or nil.
func (c *Client) getParticleSystem() *game.ParticleSystem {
	if c.engine == nil || c.engine.SpellFX == nil {
		return nil
	}
	return c.engine.SpellFX.Particles
}

func portalCentroidVec(p esf.ZoneRoomPortal) (float32, float32, float32) {
	if len(p.Vertices) == 0 {
		return 0, 0, 0
	}
	var sx, sy, sz float32
	for _, v := range p.Vertices {
		sx += v.X
		sy += v.Y
		sz += v.Z
	}
	n := float32(len(p.Vertices))
	return sx / n, sy / n, sz / n
}

// drawNavDebug renders navmesh triangles and current path as 3D wireframes.
func (c *Client) drawNavDebug(gs *game.GameState) {
	var zt *terrain.ZoneTerrain
	if c.terrainLoader != nil && c.terrainZoneIdx >= 0 {
		zt = c.terrainLoader.GetZoneTerrain(c.terrainZoneIdx)
	}
	if zt == nil || zt.Nav == nil {
		return
	}

	p := &gs.Player
	navClr := rl.Color{R: 0, G: 100, B: 255, A: 80}

	// Draw walkable navmesh triangles near player (within 100 units)
	const drawRange = float32(100)
	for _, t := range zt.Nav.Tris {
		dx := t.CX - p.X
		dz := t.CZ - p.Z
		if dx*dx+dz*dz > drawRange*drawRange {
			continue
		}
		// Offset Y slightly above ground to avoid z-fighting
		yo := float32(0.15)
		v0 := rl.Vector3{X: t.X0, Y: t.Y0 + yo, Z: t.Z0}
		v1 := rl.Vector3{X: t.X1, Y: t.Y1 + yo, Z: t.Z1}
		v2 := rl.Vector3{X: t.X2, Y: t.Y2 + yo, Z: t.Z2}
		rl.DrawLine3D(v0, v1, navClr)
		rl.DrawLine3D(v1, v2, navClr)
		rl.DrawLine3D(v2, v0, navClr)
	}

	// Draw actor obstacle footprints as red rectangles
	if zt != nil {
		obsClr := rl.Color{R: 255, G: 50, B: 50, A: 60}
		for _, obs := range zt.Obstacles {
			dx := obs.CX - p.X
			dz := obs.CZ - p.Z
			if dx*dx+dz*dz > 100*100 {
				continue
			}
			yo := float32(0.2)
			rl.DrawLine3D(
				rl.Vector3{X: obs.MinX, Y: p.Y + yo, Z: obs.MinZ},
				rl.Vector3{X: obs.MaxX, Y: p.Y + yo, Z: obs.MinZ}, obsClr)
			rl.DrawLine3D(
				rl.Vector3{X: obs.MaxX, Y: p.Y + yo, Z: obs.MinZ},
				rl.Vector3{X: obs.MaxX, Y: p.Y + yo, Z: obs.MaxZ}, obsClr)
			rl.DrawLine3D(
				rl.Vector3{X: obs.MaxX, Y: p.Y + yo, Z: obs.MaxZ},
				rl.Vector3{X: obs.MinX, Y: p.Y + yo, Z: obs.MaxZ}, obsClr)
			rl.DrawLine3D(
				rl.Vector3{X: obs.MinX, Y: p.Y + yo, Z: obs.MaxZ},
				rl.Vector3{X: obs.MinX, Y: p.Y + yo, Z: obs.MinZ}, obsClr)
			// Door probing removed from render — too expensive (36 CalcFit rays per frame)
		}
	}

	// Draw portal doorways as cyan wireframes
	if zt != nil && len(zt.Rooms) > 0 {
		portalClr := rl.Color{R: 0, G: 255, B: 255, A: 120}
		for _, room := range zt.Rooms {
			for _, portal := range room.Portals {
				if len(portal.Vertices) < 2 {
					continue
				}
				// Only draw portals near player
				cx, cy, cz := portalCentroidVec(portal)
				ddx := cx - p.X
				ddz := cz - p.Z
				if ddx*ddx+ddz*ddz > 150*150 {
					continue
				}
				// Draw portal polygon edges
				for vi := 0; vi < len(portal.Vertices); vi++ {
					v0 := portal.Vertices[vi]
					v1 := portal.Vertices[(vi+1)%len(portal.Vertices)]
					rl.DrawLine3D(
						rl.Vector3{X: v0.X, Y: v0.Y, Z: v0.Z},
						rl.Vector3{X: v1.X, Y: v1.Y, Z: v1.Z},
						portalClr,
					)
				}
				// Sphere at centroid
				rl.DrawSphere(rl.Vector3{X: cx, Y: cy, Z: cz}, 0.2, portalClr)
			}
		}
	}

	// Draw grid path debug
	if c.engine.CombatEngaged && c.targetObjectID != 0 {
		if ent, ok := gs.Entities[c.targetObjectID]; ok {
			playerPos := rl.Vector3{X: p.X, Y: p.Y + 1, Z: p.Z}
			targetPos := rl.Vector3{X: ent.X, Y: ent.Y + 1, Z: ent.Z}
			rl.DrawLine3D(playerPos, targetPos, rl.Color{R: 0, G: 255, B: 0, A: 120})

			if c.navGridPath != nil {
				prev := playerPos
				pathClr := rl.Color{R: 0, G: 255, B: 100, A: 255}
				for i, wp := range c.navGridPath {
					wpy := p.Y + 0.5
					if zt != nil {
						if h, ok := zt.SampleHeight(wp.X, wp.Z, p.Y); ok { wpy = h + 0.5 }
					}
					pos := rl.Vector3{X: wp.X, Y: wpy, Z: wp.Z}
					rl.DrawLine3D(prev, pos, pathClr)
					if i == c.navGridIdx {
						rl.DrawSphere(pos, 0.3, rl.Color{R: 255, G: 255, B: 0, A: 255})
					} else if i > c.navGridIdx {
						rl.DrawSphere(pos, 0.15, pathClr)
					}
					prev = pos
				}
			}
		}
	}
}

