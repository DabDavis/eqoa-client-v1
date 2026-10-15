package main

import (
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/eqoa/client/internal/models"
	"github.com/eqoa/client/internal/terrain"
)

func (c *Client) handleInWorldInput() {
	// Check for server teleport physics reset flag.
	// PS2: after receiving Teleport opcode (0x00B2), VIWalkControl resets all physics.
	if c.engine.State.TeleportPhysicsReset {
		c.engine.State.TeleportPhysicsReset = false
		c.velocityY = 0
		c.onGround = false
		c.safeValid = false
		c.currentSpeed = 0
		c.flyVelX, c.flyVelY, c.flyVelZ = 0, 0, 0
	}

	// The F3 report dialog holds the keyboard, as the menu and chat do: no
	// movement, camera or hotkeys under it (report.go).
	if c.reportOpen() {
		return
	}

	// Shift+F3: toggle bone debug visualization. F3 alone is Report a Problem.
	shift := rl.IsKeyDown(rl.KeyLeftShift) || rl.IsKeyDown(rl.KeyRightShift)
	if shift && rl.IsKeyPressed(rl.KeyF3) && c.modelMgr != nil {
		c.modelMgr.ShowBoneDebug = !c.modelMgr.ShowBoneDebug
		log.Printf("Bone debug: %v", c.modelMgr.ShowBoneDebug)
	}

	// F7: toggle navmesh debug overlay
	if rl.IsKeyPressed(rl.KeyF7) {
		c.navDebug = !c.navDebug
		log.Printf("NavMesh debug: %v", c.navDebug)
	}

	// Ctrl+X: start 20s logout timer, or force-disconnect if already active
	if (rl.IsKeyDown(rl.KeyLeftControl) || rl.IsKeyDown(rl.KeyRightControl)) && rl.IsKeyPressed(rl.KeyX) {
		if c.logoutActive {
			// Force disconnect immediately
			c.logoutActive = false
			c.disconnectAndReturnToLogin()
			return
		}
		c.logoutActive = true
		c.logoutTime = time.Now().Add(20 * time.Second)
		return
	}

	// Escape cancels logout timer
	if c.logoutActive && rl.IsKeyPressed(rl.KeyEscape) {
		c.logoutActive = false
		return
	}

	// Check if logout timer expired
	if c.logoutActive && time.Now().After(c.logoutTime) {
		c.logoutActive = false
		c.disconnectAndReturnToLogin()
		return
	}

	// PS2 death: blocks ALL input (movement, camera, chat, hotkeys, spells).
	// Only logout (Ctrl+X) remains active. Camera auto-orbits slowly.
	// PS2 VICSprite::SetDeath (0x00421588) sets IsDead flag, plays death anim (slot 14),
	// holds last frame. CLIENT game loop checks IsDead and blocks all input processing.
	if c.engine.State.Player.Dead {
		c.currentSpeed = 0
		c.autoRun = false
		// Slow auto-orbit camera around corpse (PS2 VIWalkCamera::OrbitRight).
		// ~PI/12 rad/s = 15 deg/s = full rotation in 24 seconds.
		dt := rl.GetFrameTime()
		c.camAngle += float32(math.Pi/12.0) * dt
		return
	}

	// N: toggle minimap
	if rl.IsKeyPressed(rl.KeyN) && !c.chatTyping && !c.gmInputMode {
		c.minimapOn = !c.minimapOn
	}

	// M: toggle world map (only when not typing in chat/GM input)
	if rl.IsKeyPressed(rl.KeyM) && !c.chatTyping && !c.gmInputMode {
		c.toggleWorldMap()
		return
	}

	// World map open — Escape/Triangle closes, block other input
	if c.worldMapOpen {
		if rl.IsKeyPressed(rl.KeyEscape) || (gpAvailable() && gpPressed(rl.GamepadButtonRightFaceUp)) {
			c.worldMapOpen = false
		}
		return
	}

	// F11: toggle GM menu (dev builds only)
	if gmEnabled() && rl.IsKeyPressed(rl.KeyF11) {
		c.gmMenuOpen = !c.gmMenuOpen
		c.gmInputMode = false
		return
	}

	// GM menu open -- consume input
	if c.gmMenuOpen {
		c.handleGMMenuInput()
		return
	}

	// F12: toggle main menu
	if rl.IsKeyPressed(rl.KeyF12) {
		c.menuOpen = !c.menuOpen
		c.menuTab = 0
		c.menuScroll = 0
		return
	}

	// Ctrl+key shortcuts: toggle menu tabs directly
	if rl.IsKeyDown(rl.KeyLeftControl) || rl.IsKeyDown(rl.KeyRightControl) {
		type menuShortcut struct {
			key     int32
			tab     int
			subTab  int // -1 = no sub-tab
		}
		shortcuts := []menuShortcut{
			{rl.KeyI, 1, -1}, // Ctrl+I: Inventory
			{rl.KeyC, 2, 0},  // Ctrl+C: Character > Stats
			{rl.KeyS, 2, 1},  // Ctrl+S: Character > Spells
			{rl.KeyM, 2, 2},  // Ctrl+M: Character > Class Mastery
			{rl.KeyN, 3, -1}, // Ctrl+N: Nearby
			{rl.KeyW, 4, -1}, // Ctrl+W: Who List
			{rl.KeyG, 5, -1}, // Ctrl+G: Guild
			{rl.KeyP, 6, -1}, // Ctrl+P: Settings (was 5, now 6)
		}
		for _, sc := range shortcuts {
			if rl.IsKeyPressed(sc.key) {
				if c.menuOpen {
					c.closeMenu()
				} else {
					c.menuOpen = true
					c.menuTab = sc.tab
					c.menuRow = 0
					if sc.subTab >= 0 {
						c.charSubTab = sc.subTab
					}
					c.menuScroll = 0
					if sc.key == rl.KeyW {
						c.whoRequested = false
					}
				}
				c.spellCtxSpellID = -1
				c.spellSlotPicker = false
				c.spellInfoPopup = false
				return
			}
		}
	}

	// Item inspect window open -- consume input
	if c.inspectOpen {
		if rl.IsKeyPressed(rl.KeyEnter) || rl.IsKeyPressed(rl.KeyEscape) ||
			rl.IsKeyPressed(rl.KeyX) ||
			(gpAvailable() && (gpPressed(rl.GamepadButtonRightFaceDown) || gpPressed(rl.GamepadButtonRightFaceRight))) {
			c.inspectOpen = false
			c.inspectItem = nil
		}
		return
	}

	// Merchant window open -- consume input
	if c.engine.State.MerchantOpen {
		c.handleMerchantInput()
		return
	}

	// Blacksmith window open -- consume input
	if c.engine.State.BlacksmithOpen {
		c.handleBlacksmithInput()
		return
	}

	// Main menu open -- consume input
	if c.menuOpen {
		c.handleMenuInput()
		return
	}

	// Chat input mode -- when typing, consume all keyboard input
	if c.chatTyping {
		c.handleChatInput()
		return
	}

	// Enter key opens chat input
	if rl.IsKeyPressed(rl.KeyEnter) {
		c.chatTyping = true
		c.chatBuffer = ""
		return
	}

	dt := rl.GetFrameTime()
	rotSpeed := float32(10.0) * c.camSensitivity   // Camera orbit force (rad/s²), scaled by sensitivity
	zoomSpeed := float32(67) * c.camSensitivity   // Zoom force (units/s²), scaled by sensitivity
	// PS2 VIWalkControl speed constants from InitParms (0x00487910).
	// Walk base = 3.0 (hardcoded, never changed by server).
	// Run base = server PlayerSpeed value (handler at 0x00BDD540, default 8.833).
	// Walk/run ratio = 3.0 / 8.833 = 0.3397.
	const (
		ps2WalkBase     = float32(3.0)    // PS2 InitParms walk base (fixed)
		ps2DefaultRunBase = float32(8.833) // PS2 InitParms default run base
		walkRunRatio    = float32(0.3397)  // ps2WalkBase / ps2DefaultRunBase
		// PS2 UpdatePhysics (0x486198): exponential velocity damping.
		// powf(dampingBase, dt) applied per tick. dampingBase = InitParms+0x40 = 0.02.
		// At 30fps (dt=0.033): decay = 0.02^0.033 ≈ 0.876. At 60fps: 0.02^0.016 ≈ 0.936.
		dampingBase = float64(0.02)
		// Minimum speed before snapping to zero (avoids infinite deceleration tail).
		speedEpsilon = float32(0.05)
		// Follow-camera orbit tracking speed -- how fast camera orbits behind character.
		camFollowRate = float32(4.0) // radians/sec

		// PS2 VIWalkControl::UpdatePhysics gravity constants.
		// Extracted from EE memory dump: _13VIWalkControl$Gravity at 0x004E31F8.
		gravityAccel    = float32(19.6) // $Gravity.Y magnitude (approx 2g, PS2 uses -19.6)
		waterGravity    = float32(1.0)  // PS2 $WGravity.Y = -1.0 (slow sink when submerged)
		maxStepDown     = float32(1.5)  // max Y drop before falling (matches stepUp)
		terminalVelY    = float32(25.0) // PS2 InitParms+0x264 = 25.0
		// PS2 UpdatePhysics: deceleration rate when exceeding terminal velocity.
		// Constant 0x40C00000 = 6.0 u/s².
		terminalVelDecel = float32(6.0)
		jumpVelY        = float32(8.0)  // upward velocity impulse on jump (not in PS2 -- custom)
		swimSpeed       = float32(3.0)  // PS2 InitParms+0x20 = 3.0 (vertical swim speed)
		charHeight = float32(5.0) // approximate character height for water probing
	)

	// --- Targeting ---
	// Escape: clear target, or open menu if nothing else to dismiss
	if rl.IsKeyPressed(rl.KeyEscape) && !c.engine.State.DialogueActive {
		if c.targetObjectID != 0 {
			// Clear target — PS2 sends ClassicTargetInfo(0xFFFFFFFF) to deselect
			c.targetObjectID = 0
			c.engine.TargetObjectID = 0
			c.engine.SendTarget(0xFFFFFFFF)
			c.engine.ForcePositionFlush()
		} else {
			c.menuOpen = true
			c.menuTab = 0
			c.menuRow = 0
			c.menuScroll = 0
			return
		}
	}

	// F1: target self
	if rl.IsKeyPressed(rl.KeyF1) {
		selfID := c.engine.State.PlayerObjectID
		if selfID != 0 {
			c.targetObjectID = selfID
			c.engine.TargetObjectID = selfID
			c.engine.SendTarget(selfID)
		}
	}

	// Tab: cycle to next nearest entity by 3D distance
	if rl.IsKeyPressed(rl.KeyTab) {
		c.tabTarget()
	}

	// Left mouse button: screen-space proximity pick (skip if clicking UI)
	if rl.IsMouseButtonPressed(rl.MouseButtonLeft) && !c.isMouseOverSpellBar() && !c.isMouseOverChat() && !c.isMouseOverMusicHud() {
		c.mousePickTarget()
	}

	// Auto-clear stale target (entity left visibility, but not self-target)
	if c.targetObjectID != 0 && c.targetObjectID != c.engine.State.PlayerObjectID {
		if _, ok := c.engine.State.Entities[c.targetObjectID]; !ok {
			c.targetObjectID = 0
			c.engine.TargetObjectID = 0
		}
	}

	// --- Dialogue / Interact ---
	gs := c.engine.State
	if gs.DialogueActive {
		// Dialogue is showing -- handle option selection or dismiss
		if len(gs.DialogueOptions) > 0 {
			// Arrow keys / W/S navigate cursor
			if rl.IsKeyPressed(rl.KeyUp) || rl.IsKeyPressed(rl.KeyW) {
				c.dialogueCursor--
				if c.dialogueCursor < 0 {
					c.dialogueCursor = len(gs.DialogueOptions) - 1
				}
			}
			if rl.IsKeyPressed(rl.KeyDown) || rl.IsKeyPressed(rl.KeyS) {
				c.dialogueCursor++
				if c.dialogueCursor >= len(gs.DialogueOptions) {
					c.dialogueCursor = 0
				}
			}
			// Enter confirms cursor selection
			if rl.IsKeyPressed(rl.KeyEnter) && c.dialogueCursor >= 0 && c.dialogueCursor < len(gs.DialogueOptions) {
				c.engine.SendDialogueOption(gs.DialogueCounter, byte(c.dialogueCursor))
				gs.DialogueActive = false
				gs.DialogueOptions = nil
				c.dialogueCursor = 0
			}
			// Number keys 1-9 select options directly
			for i := 0; i < len(gs.DialogueOptions) && i < 9; i++ {
				if rl.IsKeyPressed(int32(rl.KeyOne + i)) {
					c.engine.SendDialogueOption(gs.DialogueCounter, byte(i))
					gs.DialogueActive = false
					gs.DialogueOptions = nil
					c.dialogueCursor = 0
				}
			}
		}
		// Escape or X dismisses dialogue (cancel = 255)
		if rl.IsKeyPressed(rl.KeyEscape) || rl.IsKeyPressed(rl.KeyX) {
			if len(gs.DialogueOptions) > 0 {
				c.engine.SendDialogueOption(gs.DialogueCounter, 255)
			}
			gs.DialogueActive = false
			gs.DialogueOptions = nil
			c.dialogueCursor = 0
		}
	} else if !c.chatTyping {
		// X or Right-click: interact with current target
		if (rl.IsKeyPressed(rl.KeyX) || rl.IsMouseButtonPressed(rl.MouseButtonRight)) && c.targetObjectID != 0 {
			c.engine.SendInteract(c.targetObjectID)
			// If target is a merchant NPC, also send MerchantDiag to open the shop.
			// PS2 flow: Interact → greeting → client sends 0x004C → server sends 0x00B7 items.
			if ent, ok := c.engine.State.Entities[c.targetObjectID]; ok && ent.IsNPC {
				if ent.NPCType&0x0100 != 0 || strings.HasPrefix(ent.Name, "Merchant") {
					c.engine.State.MerchantName = ent.Name
					c.engine.SendMerchantDiag(c.targetObjectID)
				}
			}
		}

		// 1-9, 0: cast spell from hotbar slot (keys 1-9 -> slots 0-8, key 0 -> slot 9)
		c.handleHotbarKeys()
	}

	// Space: toggle auto-attack on current target.
	// Manual edge-detect because IsKeyPressed fires across multiple frames.
	spaceDown := rl.IsKeyDown(rl.KeySpace)
	if spaceDown && !c.spaceWasDown && c.targetObjectID != 0 {
		if c.animMgr.CombatEngaged {
			// Disengage auto-attack
			c.animMgr.CombatEngaged = false
			c.engine.CombatEngaged = false
			c.engine.AttackSent = false
			c.animMgr.InCombat = false
			c.engine.InCombat = false
			c.animMgr.playerCombat.inCombat = false
			c.animMgr.playerCombat.combatAnim = 0
			log.Printf("COMBAT: Space pressed — disengaged auto-attack")
		} else {
			// Engage auto-attack — don't send Attack opcode yet.
			// PS2 auto-approach: player runs to target first, Attack sent when in melee range.
			log.Printf("COMBAT: Space pressed — targetObjID=0x%08X, will auto-approach",
				c.targetObjectID)
			c.animMgr.CombatEngaged = true
			c.engine.CombatEngaged = true
			c.engine.AttackSent = false
			c.animMgr.playerCombat.attackCycleIdx = 0
			c.animMgr.playerCombat.lastActivity = time.Now()
		}
	}
	c.spaceWasDown = spaceDown

	// Swimming -- Q = ascend, E = descend (keyboard)
	// Gamepad right stick Y sets swimVelY in handleGamepadInWorld.
	c.swimVelY = 0
	if c.waterState != terrain.WaterNone {
		if rl.IsKeyDown(rl.KeyQ) {
			c.swimVelY = swimSpeed
		} else if rl.IsKeyDown(rl.KeyE) {
			c.swimVelY = -swimSpeed
		}
	}

	// PageUp/PageDown: cycle upper body animation index
	if rl.IsKeyPressed(rl.KeyPageUp) {
		models.DebugAnimUpper++
		log.Printf("DEBUG ANIM: upper=%d lower=%d", models.DebugAnimUpper, models.DebugAnimLower)
	}
	if rl.IsKeyPressed(rl.KeyPageDown) {
		models.DebugAnimUpper--
		if models.DebugAnimUpper < -1 {
			models.DebugAnimUpper = -1
		}
		log.Printf("DEBUG ANIM: upper=%d lower=%d", models.DebugAnimUpper, models.DebugAnimLower)
	}
	// Home/End: cycle lower body animation index
	if rl.IsKeyPressed(rl.KeyHome) {
		models.DebugAnimLower++
		log.Printf("DEBUG ANIM: upper=%d lower=%d", models.DebugAnimUpper, models.DebugAnimLower)
	}
	if rl.IsKeyPressed(rl.KeyEnd) {
		models.DebugAnimLower--
		if models.DebugAnimLower < -1 {
			models.DebugAnimLower = -1
		}
		log.Printf("DEBUG ANIM: upper=%d lower=%d", models.DebugAnimUpper, models.DebugAnimLower)
	}

	// --- PS2 Hotkey Menu (L key or L2+D-pad) ---
	c.handleHotkeyMenu()

	// Spell bar cursor fade-out
	if c.spellBarVisible {
		if gpAvailable() {
			// Gamepad connected: keep spell bar always visible (no fade)
			c.spellBarFade = 3.0
		} else {
			c.spellBarFade -= rl.GetFrameTime()
		}
		if c.spellBarFade <= 0 {
			c.spellBarVisible = false
		}
	}

	// --- Gamepad input (merged with keyboard below) ---
	gpFwd, gpRight, gpCamH, gpCamV := c.handleGamepadInWorld()

	// PS2 VIWalkCamera: force-based orbit with exponential damping.
	// OrbitRight (0x004833C0) accumulates force, UpdateCameraPhysics (0x00482758)
	// integrates with powf(0.01, dt). Camera damping base = 0.01 (InitParms +0x258).
	const camDampingBase = float64(0.01)
	camDecay := float32(math.Pow(camDampingBase, float64(dt)))

	// Camera orbit / character turn: A/D + right stick X.
	// PS2: In first person, A/D rotates the character (camera follows facing).
	// In third person, A/D orbits the camera independently.
	ctrlDown := rl.IsKeyDown(rl.KeyLeftControl) || rl.IsKeyDown(rl.KeyRightControl)
	var camAngleForce float32
	if !ctrlDown && rl.IsKeyDown(rl.KeyA) {
		camAngleForce -= rotSpeed
	}
	if !ctrlDown && rl.IsKeyDown(rl.KeyD) {
		camAngleForce += rotSpeed
	}
	camAngleForce += gpCamH
	c.camAngleVel += camAngleForce * dt
	c.camAngle += c.camAngleVel * dt
	c.camAngleVel *= camDecay

	// Camera pitch: W/S accumulate pitch force (PS2 SetOrbitPitchAngle).
	rp := c.playerRaceParams()
	maxPitch := float32(1.5708)  // PI/2 up (same for all races)
	minPitch := -rp.PitchLimit   // per-race downward limit
	var camPitchForce float32
	if !ctrlDown && rl.IsKeyDown(rl.KeyW) {
		camPitchForce += rotSpeed
	}
	if !ctrlDown && rl.IsKeyDown(rl.KeyS) {
		camPitchForce -= rotSpeed
	}
	camPitchForce += gpCamV
	c.camPitchVel += camPitchForce * dt
	c.camPitch += c.camPitchVel * dt
	c.camPitchVel *= camDecay
	if c.camPitch > maxPitch {
		c.camPitch = maxPitch
		c.camPitchVel = 0
	}
	if c.camPitch < minPitch {
		c.camPitch = minPitch
		c.camPitchVel = 0
	}

	// Arrow keys / left stick: player movement.
	// PS2 ViewRelativeWalk (0x004856B8): converts camera-relative input to world heading.
	//
	// First person (PS2 mode 0): arrow left/right + left stick X = STRAFE.
	//   A/D + right stick X = turn character (camera follows).
	// Third person (PS2 mode 3): arrow left/right + left stick X = camera-relative move.
	//   A/D + right stick X = orbit camera.
	if c.hotkeyCloseTime > 0 {
		c.hotkeyCloseTime -= dt
	}
	var inputFwd, inputRight float32
	if !c.hotkeyActive && c.hotkeyCloseTime <= 0 {
		if rl.IsKeyDown(rl.KeyUp) {
			inputFwd += 1
		}
		if rl.IsKeyDown(rl.KeyDown) {
			inputFwd -= 1
		}
		if c.firstPerson {
			// First person: arrow left/right = strafe
			if rl.IsKeyDown(rl.KeyRight) {
				inputRight += 1
			}
			if rl.IsKeyDown(rl.KeyLeft) {
				inputRight -= 1
			}
		} else {
			// Third person: arrow left/right = camera-relative move
			if rl.IsKeyDown(rl.KeyRight) {
				inputRight += 1
			}
			if rl.IsKeyDown(rl.KeyLeft) {
				inputRight -= 1
			}
		}
	}

	if c.firstPerson {
		// First person: left stick X = strafe, not camera-relative right
		inputFwd += gpFwd
		inputRight += gpRight
	} else {
		// Third person: merge gamepad left stick as camera-relative
		inputFwd += gpFwd
		inputRight += gpRight
	}

	// Cancel auto-run on manual movement input
	if c.autoRun && (inputFwd != 0 || inputRight != 0) {
		c.autoRun = false
	}

	// Auto-run: inject forward movement
	if c.autoRun {
		inputFwd = 1
	}

	// Convert to world-space movement vector.
	_, _, cosA, sinA := c.camTrig()
	moveX := -cosA*inputFwd + sinA*inputRight
	moveZ := -sinA*inputFwd - cosA*inputRight

	// Grid runner autopilot: override movement direction with world-space target
	if grX, grZ := c.gridRunnerUpdate(); grX != 0 || grZ != 0 {
		moveX = grX
		moveZ = grZ
	}

	p := &c.engine.State.Player

	// Auto-Approach: when combat is engaged, run toward target.
	// 1. Portal pathfinding for indoor rooms (doorways)
	// 2. Bug Algorithm for wall avoidance (outdoor + within rooms)
	const meleeRange = float32(1.5)
	if c.engine.CombatEngaged && c.targetObjectID != 0 && (moveX == 0 && moveZ == 0) {
		if ent, ok := c.engine.State.Entities[c.targetObjectID]; ok {
			dx := ent.X - p.X
			dz := ent.Z - p.Z
			dist := float32(math.Sqrt(float64(dx*dx + dz*dz)))

			if !c.engine.AttackSent && dist > meleeRange {
				var zt *terrain.ZoneTerrain
				if c.terrainLoader != nil && c.terrainZoneIdx >= 0 {
					zt = c.terrainLoader.GetZoneTerrain(c.terrainZoneIdx)
				}

				// --- Local grid A*: compute path on target change or target moved ---
				needPath := c.navGridPath == nil || c.portalTarget != c.targetObjectID
				if !needPath {
					tdx := ent.X - c.navGridTX
					tdz := ent.Z - c.navGridTZ
					if tdx*tdx+tdz*tdz > 9 { needPath = true }
				}

				if needPath {
					c.portalTarget = c.targetObjectID
					c.navGridPath = nil
					c.navGridIdx = 0
					c.navGridTX = ent.X
					c.navGridTZ = ent.Z

				if zt != nil {
						// 2D grid A* — reliable for flat ground + doorways.
						// May also climb wide ramps via natural terrain following.
						// TODO: 2.5D grid for stairs/multi-level (grid25d.go preserved)
						c.navGridPath = terrain.FindGridPath(zt, p.X, p.Y, p.Z, ent.X, ent.Y, ent.Z)
						if c.navGridPath != nil {
							log.Printf("NAV: path %d waypoints (%.0f,%.0f)→(%.0f,%.0f)",
								len(c.navGridPath), p.X, p.Z, ent.X, ent.Z)
						} else {
							log.Printf("NAV: no path found")
						}
					}
					if c.navGridPath == nil {
						c.navGridPath = []terrain.GridWaypoint{{X: ent.X, Z: ent.Z, Y: ent.Y}}
					}
				}

				// --- Follow waypoints ---
				goalX, goalZ := ent.X, ent.Z
				if c.navGridPath != nil && c.navGridIdx < len(c.navGridPath) {
					wp := c.navGridPath[c.navGridIdx]
					wdx := wp.X - p.X
					wdz := wp.Z - p.Z
					wdist := float32(math.Sqrt(float64(wdx*wdx + wdz*wdz)))
					// Advance when close in XZ. The refY hint handles Y climbing
					// separately — don't block advancement based on Y.
					if wdist < 1.5 {
						c.navGridIdx++
					}
					if c.navGridIdx < len(c.navGridPath) {
						wp = c.navGridPath[c.navGridIdx]
						goalX, goalZ = wp.X, wp.Z
					}
				}

				gdx := goalX - p.X
				gdz := goalZ - p.Z
				gdist := float32(math.Sqrt(float64(gdx*gdx + gdz*gdz)))
				if gdist > 0.1 {
					dirX := gdx / gdist
					dirZ := gdz / gdist

					// TODO: 3D voxel sensor for stairs/ramps (disabled — causes vibration)
					// Needs integration with SampleHeight ground-snap instead of
					// overriding movement direction. Flat ground 2D grid works well.

					moveX = dirX
					moveZ = dirZ
				}
			}

			if dist <= meleeRange && !c.engine.AttackSent {
				c.engine.InCombat = true
				c.engine.SendAttack(c.targetObjectID)
				c.engine.ForcePositionFlush()
				c.engine.AttackSent = true
				c.navGridPath = nil
				log.Printf("COMBAT: Auto-approach complete (dist=%.1f), Attack sent", dist)
			}
		}
	}

	// Reset state when combat disengaged
	if !c.engine.CombatEngaged {
		c.navGridPath = nil
		c.portalTarget = 0
	}

	// Get terrain for ground tracking (primary zone for collision).
	var zt *terrain.ZoneTerrain
	if c.terrainLoader != nil && c.terrainZoneIdx >= 0 {
		zt = c.terrainLoader.GetZoneTerrain(c.terrainZoneIdx)
	}

	// PS2 VIWalkControl::Move -- the liquid bits, probed at the sphere centre.
	if zt != nil {
		c.liquidFlags = zt.ProbeLiquid(p.X, p.Y, p.Z)
		c.waterState = terrain.WaterStateOf(c.liquidFlags)
	}

	// Track safe position for void-fall recovery.
	if zt != nil {
		c.safeX, c.safeY, c.safeZ = p.X, p.Y, p.Z
		c.safeValid = true
	}

	// PS2 speed from server PlayerSpeed opcode (0x00F8).
	// Walk base = 3.0, run base = server value (default 8.833).
	serverSpeed := c.engine.State.Player.Speed
	if serverSpeed <= 0 {
		serverSpeed = ps2DefaultRunBase // fallback before server sends speed
	}
	walkSpeedCap := serverSpeed * walkRunRatio // PS2 Run() in liquid uses walk speed

	// PS2 UpdatePhysics speed ramping: exponential damping + force accumulation.
	// velocity = velocity * powf(dampingBase, dt) + targetSpeed * (1 - powf(dampingBase, dt))
	// dampingBase = 0.02 from InitParms+0x40.
	decay := float32(math.Pow(dampingBase, float64(dt)))

	if moveX != 0 || moveZ != 0 {
		// Cancel emote on movement
		if c.animMgr.EmoteAnim != 0 {
			c.animMgr.EmoteAnim = 0
			c.engine.EmoteAnimByte = 0
		}

		// Always normalize to unit direction.
		mag := float32(math.Sqrt(float64(moveX*moveX + moveZ*moveZ)))
		moveX /= mag
		moveZ /= mag

		// Store last movement direction for deceleration.
		c.lastMoveX = moveX
		c.lastMoveZ = moveZ

		// Analog stick magnitude drives walk/run selection.
		if mag > 1.0 {
			mag = 1.0
		}
		const runThreshold = float32(0.7) // PS2 InitParms+0x5C
		var targetSpeed float32
		// PS2: first person backward/strafe = walk speed only, forward = run allowed.
		fpWalkOnly := c.firstPerson && (inputFwd <= 0 || inputRight != 0)
		if c.runMode && mag > runThreshold && !fpWalkOnly {
			targetSpeed = serverSpeed
		} else {
			targetSpeed = walkSpeedCap
		}
		if c.gmFastRun {
			targetSpeed *= 4
		}

		// PS2 UpdatePhysics: exponential approach to target speed.
		// vel = vel * decay + target * (1 - decay)
		c.currentSpeed = c.currentSpeed*decay + targetSpeed*(1-decay)

		// PS2 VIWalkControl::SetTargetHeading — set target, TurnLeft rotates per frame.
		// First person: character faces camera, not movement direction.
		if !c.firstPerson {
			c.targetFacing = float32(math.Atan2(float64(moveX), float64(moveZ)))
		}

		// Apply movement using ramped speed. VIWalkControl::Run walks only in
		// mud: in a liquid when it is mud, out of one when the feet are in mud.
		speed := c.currentSpeed
		if c.inMud() && speed > walkSpeedCap {
			speed = walkSpeedCap
		}
		oldX, oldZ := p.X, p.Z
		p.X += moveX * speed * dt
		p.Z += moveZ * speed * dt

		// PS2 VIWalkControl::Move's sphere sweep -- collide and skitter.
		if zt != nil && !c.gmNoClip {
			fit := zt.CalcFit(oldX, p.Y, oldZ, p.X, p.Z, c.onGround, c.liquidFlags&terrain.LiquidIn != 0)
			p.X, p.Z = fit.X, fit.Z
			if fit.PushedOut {
				c.currentSpeed = 0 // Move zeroes horizontal force and velocity
			}
		}

		// PS2 VIWalkControl::CalcFloor — ground snap for horizontal movement.
		// No ramp hacks — the 2.5D path includes ground-level ramp entrance
		// waypoints so normal SampleHeight terrain-following handles ramps.
		c.rampClimbing = false
		if zt != nil && c.onGround && !c.gmNoClip {
			oldY := p.Y
			if newY, ok := zt.SampleHeight(p.X, p.Z, p.Y); ok {
				drop := oldY - newY
				if drop <= maxStepDown {
					p.Y = newY
					if terrain.CollDebug && newY != oldY {
						fmt.Printf("CalcFloor: Y %.2f -> %.2f (delta %.2f)\n", oldY, newY, newY-oldY)
					}
				} else {
					c.onGround = false
					if terrain.CollDebug {
						fmt.Printf("CalcFloor: ledge detected! drop=%.2f > maxStepDown=%.1f -> falling\n",
							drop, maxStepDown)
					}
				}
			} else {
				c.onGround = false
			}
		}

		c.engine.Walking = true
		// PS2 UpdateLocomotion: classify speed into animation state.
		if speed > walkSpeedCap*1.1 {
			c.engine.AnimByte = animRun
		} else {
			c.engine.AnimByte = 1 // walk
		}
		c.engine.VelX = moveX * speed
		c.engine.VelZ = moveZ * speed
		if c.onGround {
			c.engine.VelY = 0
		}
		c.engine.StickMagnitude = 254
		c.engine.AccelState = 0x80
	} else {
		// No input — decelerate using PS2 exponential damping.
		c.currentSpeed = c.currentSpeed * decay
		if c.currentSpeed < speedEpsilon {
			// Fully stopped.
			c.currentSpeed = 0
			c.engine.Walking = false
			c.engine.AnimByte = 0 // idle
			c.engine.VelX = 0
			c.engine.VelY = 0
			c.engine.VelZ = 0
			c.engine.StickMagnitude = 0x80
			c.engine.AccelState = 0x80
		} else {
			// Still decelerating — continue moving in last direction.
			speed := c.currentSpeed
			if c.inMud() && speed > walkSpeedCap {
				speed = walkSpeedCap
			}
			oldX, oldZ := p.X, p.Z
			p.X += c.lastMoveX * speed * dt
			p.Z += c.lastMoveZ * speed * dt

			if zt != nil && !c.gmNoClip {
				fit := zt.CalcFit(oldX, p.Y, oldZ, p.X, p.Z, c.onGround, c.liquidFlags&terrain.LiquidIn != 0)
				p.X, p.Z = fit.X, fit.Z
				if fit.PushedOut {
					c.currentSpeed = 0
				}
			}
			if zt != nil && c.onGround && !c.gmNoClip {
				oldY := p.Y
				if newY, ok := zt.SampleHeight(p.X, p.Z, p.Y); ok {
					drop := oldY - newY
					if drop <= maxStepDown {
						p.Y = newY
					} else {
						c.onGround = false
					}
				} else {
					c.onGround = false
				}
			}

			c.engine.Walking = true
			if speed > walkSpeedCap*1.1 {
				c.engine.AnimByte = animRun
			} else {
				c.engine.AnimByte = 1 // walk
			}
			c.engine.VelX = c.lastMoveX * speed
			c.engine.VelZ = c.lastMoveZ * speed
			if c.onGround {
				c.engine.VelY = 0
			}
			c.engine.StickMagnitude = 0x80
			c.engine.AccelState = 0x80
		}
	}

	// === Facing rotation ===
	// First person: character always faces camera direction (PS2 mode 0).
	// Third person: character faces movement direction (PS2 ViewRelativeWalk).
	if c.firstPerson {
		// PS2 mode 0: character faces camera look direction.
		// Camera look = (-cosA, -sinA) in XZ plane.
		// Facing = atan2(lookX, lookZ) = atan2(-cosA, -sinA).
		cosA := float32(math.Cos(float64(c.camAngle)))
		sinA := float32(math.Sin(float64(c.camAngle)))
		facing := float32(math.Atan2(float64(-cosA), float64(-sinA)))
		if facing < 0 {
			facing += 2 * math.Pi
		}
		p.Facing = facing
	} else {
		p.Facing = c.targetFacing
	}

	// === PS2 0x40 Smoothed Facing + Turn Rate ===
	// Byte 28: smoothed facing lags behind byte 22 during turns (PS2 pcap: 88% match).
	// Byte 23: turning input derived from heading change rate.
	{
		// Smoothed facing: exponential approach toward current facing.
		diff := p.Facing - c.engine.SmoothedFacing
		// Normalize to [-PI, PI]
		for diff > math.Pi {
			diff -= 2 * math.Pi
		}
		for diff < -math.Pi {
			diff += 2 * math.Pi
		}
		// Lag factor: 0.15 per frame at 30fps, frame-rate independent.
		lagFactor := float32(1.0 - math.Pow(1.0-0.15, float64(dt)*30.0))
		c.engine.SmoothedFacing += diff * lagFactor

		// Turn rate: heading change per second for byte 23 encoding.
		facingDiff := p.Facing - c.engine.PrevFacing
		for facingDiff > math.Pi {
			facingDiff -= 2 * math.Pi
		}
		for facingDiff < -math.Pi {
			facingDiff += 2 * math.Pi
		}
		if dt > 0.001 {
			c.engine.TurnRate = facingDiff / dt
		}
		c.engine.PrevFacing = p.Facing
	}

	// GM Noclip: PS2 VIFlyCamera — force-based movement with exponential damping.
	// Decompiled from SUPPORT VIFlyCamera::UpdateCameraPhysics at 0x00441788.
	// Physics: force accumulates into velocity, velocity decays exponentially
	// via powf(friction, dt) where friction = 0.01.
	if c.gmNoClip {
		// PS2 VIFlyCamera::Forward / Hover — accumulate forces along camera axes.
		// moveSpeed from PS2 InitCameraIntermediate, we use runSpeed as base.
		flyMoveSpeed := float32(45.0) // PS2 maxAngularSpeed default
		if c.gmFastRun {
			flyMoveSpeed *= 4
		}

		// Camera basis vectors for camera-relative movement
		cosP, sinP, cosA, sinA := c.camTrig()

		// Forward direction (camera look direction projected to movement)
		fwdX := -cosA * cosP
		fwdY := sinP
		fwdZ := -sinA * cosP

		// Right direction (perpendicular to forward in XZ plane)
		rightX := sinA
		rightZ := -cosA

		// Clear force accumulator each frame (PS2 ClearCameraForces at 0x00442168)
		c.flyForceX, c.flyForceY, c.flyForceZ = 0, 0, 0

		// Arrow keys + gamepad left stick: camera-relative forward/strafe (PS2 VIFlyCamera::Forward)
		flyFwd := gpFwd
		flyStrafe := gpRight
		if rl.IsKeyDown(rl.KeyUp) {
			flyFwd += 1
		}
		if rl.IsKeyDown(rl.KeyDown) {
			flyFwd -= 1
		}
		if rl.IsKeyDown(rl.KeyRight) {
			flyStrafe += 1
		}
		if rl.IsKeyDown(rl.KeyLeft) {
			flyStrafe -= 1
		}
		c.flyForceX += fwdX * flyMoveSpeed * flyFwd
		c.flyForceY += fwdY * flyMoveSpeed * flyFwd
		c.flyForceZ += fwdZ * flyMoveSpeed * flyFwd
		c.flyForceX += rightX * flyMoveSpeed * flyStrafe
		c.flyForceZ += rightZ * flyMoveSpeed * flyStrafe

		// Space/Shift: world-Y vertical (PS2 VIFlyCamera::Hover vertical axis)
		if rl.IsKeyDown(rl.KeySpace) {
			c.flyForceY += flyMoveSpeed
		}
		if rl.IsKeyDown(rl.KeyLeftShift) || rl.IsKeyDown(rl.KeyRightShift) {
			c.flyForceY -= flyMoveSpeed
		}

		// PS2 VIFlyCamera::UpdateCameraPhysics — exponential damping integration.
		// dampFactor = powf(linearFriction, dt) where linearFriction = 0.01
		const flyFriction = float64(0.01) // PS2 VIFlyCamera default
		dampFactor := float32(math.Pow(flyFriction, float64(dt)))

		// Semi-implicit Euler with damping (matches PS2 integration order):
		// position += force * dt^2 * damp + velocity * dt * damp
		dtSq := dt * dt
		posAccel := dtSq * dampFactor
		velAccel := dt * dampFactor
		p.X += c.flyForceX*posAccel + c.flyVelX*velAccel
		p.Y += c.flyForceY*posAccel + c.flyVelY*velAccel
		p.Z += c.flyForceZ*posAccel + c.flyVelZ*velAccel

		// velocity += force * dt; velocity *= dampFactor
		c.flyVelX = (c.flyVelX + c.flyForceX*dt) * dampFactor
		c.flyVelY = (c.flyVelY + c.flyForceY*dt) * dampFactor
		c.flyVelZ = (c.flyVelZ + c.flyForceZ*dt) * dampFactor

		c.velocityY = 0
		c.onGround = false
		// Skip all physics/gravity below
		goto afterPhysics
	}

	// === PS2 VIWalkControl::UpdatePhysics (0x486198) -- Vertical Physics ===
	// Ported from disassembly: gravity force -> velocity integration -> position update.
	// PS2 gravity selection from UpdatePhysics:
	//   if (flags & 0x18FC):        // in any liquid
	//     if (flags & 0x10A8):      // at surface -> gravity = (0, 0, 0)
	//     else:                     // submerged -> gravity = $WGravity = (0, -1.0, 0)
	//   else:                       // on land -> gravity = $Gravity = (0, -19.6, 0)
	if zt != nil && c.waterState != terrain.WaterNone {
		// Swimming -- PS2 VIWalkControl::UpdatePhysics + SwimUp.
		//
		// PS2 SwimUp ADDS swimSpeed*amount to velocity accumulator each frame.
		// UpdatePhysics then integrates with gravity:
		//   At surface: gravity = (0,0,0) — no pull
		//   Submerged:  gravity = (0,-1.0,0) — slow sink
		// The swim input and gravity combine through the accumulator.

		// Step 1: Set velocity from player swim input (PS2 SwimUp at 0x4858C0).
		// PS2 SwimUp adds swimSpeed*amount to velocity accumulator, but the
		// accumulator is consumed and damped each frame by UpdatePhysics.
		// Net effect: velocity = swimSpeed * stickAmount while input held.
		if c.swimVelY != 0 {
			c.velocityY = c.swimVelY
		}

		// Step 2: Apply water gravity (PS2 UpdatePhysics gravity selection).
		if c.waterState == terrain.WaterSurface {
			// At surface: zero gravity. Kill velocity when no input.
			if c.swimVelY == 0 {
				c.velocityY = 0
			}
		} else if c.swimVelY == 0 {
			// Submerged: $WGravity = (0, -1.0, 0), and UpdatePhysics damps a
			// body in liquid by m_swimDamping^dt (0.02 a second) — which is
			// what stops a plunge. Without it a fall carried on to the bottom.
			damp := float32(math.Pow(0.02, float64(dt)))
			c.velocityY = (c.velocityY - waterGravity*dt) * damp
		}

		// Step 3: Integrate position.
		p.Y += c.velocityY * dt

		// Step 4: Surface clamp — the centre (feet + 1.2) no higher than the
		// surface, inside the ±0.1 band Move calls "at the surface"; any
		// higher and the probe would see dry land and drop the swimmer.
		if surfY, ok := zt.FindLiquidSurfaceY(p.X, p.Y, p.Z, charHeight); ok {
			maxY := surfY - 1.2
			if p.Y > maxY {
				p.Y = maxY
				if c.velocityY > 0 {
					c.velocityY = 0
				}
			}
		}
		// Don't sink below ground.
		if groundY, ok := zt.SampleHeight(p.X, p.Z, p.Y); ok && p.Y < groundY {
			p.Y = groundY
			if c.velocityY < 0 {
				c.velocityY = 0
			}
		}

		// PS2 UpdatePhysics: terminal velocity deceleration applies in water too.
		if c.velocityY < -terminalVelY {
			c.velocityY += terminalVelDecel * dt
			if c.velocityY > -terminalVelY {
				c.velocityY = -terminalVelY
			}
		}

		c.onGround = false

		// Swim animation: CPlayList slot 12 for all swim states.
		c.engine.AnimByte = animSwim
		c.engine.VelY = c.velocityY

		if terrain.CollDebug {
			fmt.Printf("Swim: state=%d velY=%.2f Y=%.2f\n", c.waterState, c.velocityY, p.Y)
		}
	} else if !c.onGround && zt != nil {
		// Airborne on land -- PS2 VIWalkControl::UpdatePhysics (0x486198).
		// PS2 $Gravity at 0x004E31F8 = (0, -19.6, 0)

		// PS2 velocity Verlet integration (two-pass damped):
		//   y += v*dt + 0.5*a*dt²   (position from current velocity + acceleration)
		//   v += a*dt               (then update velocity)
		// More accurate than Euler for constant-acceleration free fall.
		accel := -gravityAccel
		p.Y += c.velocityY*dt + 0.5*accel*dt*dt
		c.velocityY += accel * dt

		// PS2 terminal velocity: smooth deceleration at 6.0 u/s² (0x40C00000)
		// when falling speed exceeds terminal velocity (per-instance at +0x18C).
		// Unlike a hard clamp, this gradually decelerates to terminal speed.
		if c.velocityY < -terminalVelY {
			c.velocityY += terminalVelDecel * dt
			if c.velocityY > -terminalVelY {
				c.velocityY = -terminalVelY
			}
		}

		// Fall animation: CPlayList slot 6 (64+ anim models; 40-anim falls back to idle).
		c.engine.AnimByte = animFall
		c.engine.VelY = c.velocityY

		if terrain.CollDebug {
			fmt.Printf("Physics: velY=%.2f Y=%.2f (airborne)\n", c.velocityY, p.Y)
		}
	}

	// === Ground collision -- landing detection ===
	// Skip ground snap when swimming -- water physics handles vertical position.
	// Skip during ramp climbing -- nudge handles Y.
	if zt != nil && c.waterState == terrain.WaterNone {
		if groundY, ok := zt.SampleHeight(p.X, p.Z, p.Y); ok {
			if c.onGround {
				// Already on ground -- snap to surface (handles small terrain changes).
				p.Y = groundY
			} else if p.Y <= groundY {
				// Falling and reached ground -- land.
				p.Y = groundY
				if terrain.CollDebug && c.velocityY != 0 {
					fmt.Printf("Physics: LANDED velY=%.2f groundY=%.2f\n", c.velocityY, groundY)
				}
				// Only trigger landing animation if we had meaningful fall velocity
				if c.velocityY < -2.0 {
					c.animMgr.LandingTime = time.Now()
				}
				c.velocityY = 0
				c.onGround = true
			}
			// If airborne and above ground, keep falling (physics handles it).
		} else if c.onGround {
			// Was on ground but no ground found -- start falling.
			c.onGround = false
		}
	} else if zt != nil && c.waterState != terrain.WaterNone {
		// In water: prevent sinking below ground.
		if groundY, ok := zt.SampleHeight(p.X, p.Z, p.Y); ok {
			if p.Y < groundY {
				p.Y = groundY
				c.velocityY = 0
			}
		}
	}

afterPhysics:

	// PS2 VIWalkCamera follow mode -- camera auto-orbits behind character.
	// SetOrbitAngleBehind (0x00513086) snaps camera directly behind.
	// We use smooth tracking at camFollowRate for less jarring motion.
	if c.camFollow && c.engine.Walking {
		// "Behind character" = character facing + PI (camera looks at character from behind).
		behindAngle := p.Facing + math.Pi
		for behindAngle >= 2*math.Pi {
			behindAngle -= 2 * math.Pi
		}
		// Smooth orbit toward behind angle.
		camDiff := behindAngle - c.camAngle
		for camDiff > math.Pi {
			camDiff -= 2 * math.Pi
		}
		for camDiff < -math.Pi {
			camDiff += 2 * math.Pi
		}
		maxCamTurn := camFollowRate * dt
		if camDiff > maxCamTurn {
			camDiff = maxCamTurn
		} else if camDiff < -maxCamTurn {
			camDiff = -maxCamTurn
		}
		c.camAngle += camDiff
	}
	// Keep camera angle in [0, 2*pi).
	for c.camAngle < 0 {
		c.camAngle += 2 * math.Pi
	}
	for c.camAngle >= 2*math.Pi {
		c.camAngle -= 2 * math.Pi
	}

	// Initialize onGround on first frame with terrain.
	// Also recovers from void fall: if falling below any findable ground, snap back.
	// Skip when swimming -- water physics handles vertical position.
	if zt != nil && !c.onGround && c.waterState == terrain.WaterNone {
		if groundY, ok := zt.SampleHeight(p.X, p.Z, p.Y); ok {
			// Ground exists -- if we're at or below it, land.
			if p.Y <= groundY {
				p.Y = groundY
				c.velocityY = 0
				c.onGround = true
			}
		} else {
			// No ground below — keep falling (gravity handles death).
			// Water detection in ProbeWater will catch underground water (e.g. FP).
		}
	}

	// U key: toggle first-person / third-person camera.
	if rl.IsKeyPressed(rl.KeyU) {
		c.cycleCameraMode()
	}

	// Zoom: +/- or scroll wheel applies distance force (PS2 OrbitIn at 0x004834D0).
	var camDistForce float32
	if rl.IsKeyDown(rl.KeyEqual) || rl.IsKeyDown(rl.KeyKpAdd) {
		camDistForce -= zoomSpeed
	}
	if rl.IsKeyDown(rl.KeyMinus) || rl.IsKeyDown(rl.KeyKpSubtract) {
		camDistForce += zoomSpeed
	}
	wheel := rl.GetMouseWheelMove()
	if wheel != 0 && !c.isMouseOverChat() {
		// Scroll wheel: impulse into velocity (instant response, then decay).
		c.camDistVel -= wheel * 5 * c.camSensitivity
	}
	c.camDistVel += camDistForce * dt
	c.camDist += c.camDistVel * dt
	c.camDistVel *= camDecay

	// Clamp zoom distance based on camera mode.
	switch c.camMode {
	case 0: // Tether: min 3 units
		if c.camDist < 3 {
			c.camDist = 3
			c.camDistVel = 0
		}
	case 1: // Follow: locked at current distance
		c.camDist = c.camFollowDist
		c.camDistVel = 0
	case 2: // First-person: locked at 0
		c.camDist = 0
		c.camDistVel = 0
	}
	if c.camDist > c.camMaxDist {
		c.camDist = c.camMaxDist
		c.camDistVel = 0
	}
}

// cycleCameraMode cycles through: tether → follow → first-person → tether.
func (c *Client) cycleCameraMode() {
	c.camMode = (c.camMode + 1) % 3
	wasFP := c.firstPerson
	switch c.camMode {
	case 0:
		// Tether: free zoom, restore saved distance.
		c.firstPerson = false
		if c.camSavedDist < 3 {
			c.camSavedDist = 11.75
		}
		c.camDist = c.camSavedDist
		log.Printf("Camera: tether (dist=%.1f)", c.camDist)
	case 1:
		// Follow: lock at current zoom level, no zoom allowed.
		c.firstPerson = false
		c.camFollowDist = c.camDist // lock at whatever tether was set to
		log.Printf("Camera: follow (locked dist=%.1f)", c.camFollowDist)
	case 2:
		// First-person: inside character.
		c.firstPerson = true
		c.camSavedDist = c.camFollowDist // save for return to tether
		c.camDist = 0
		log.Printf("Camera: first-person")
	}
	// Leaving first person: sync targetFacing to camera-derived facing
	// so character doesn't snap back to old movement direction.
	if wasFP && !c.firstPerson {
		p := &c.engine.State.Player
		c.targetFacing = p.Facing
	}
}

// navPointInTri tests if point (px,pz) is inside a triangle projected to XZ.
func navPointInTri(px, pz, ax, az, bx, bz, cx, cz float32) bool {
	d1 := (px-bx)*(az-bz) - (ax-bx)*(pz-bz)
	d2 := (px-cx)*(bz-cz) - (bx-cx)*(pz-cz)
	d3 := (px-ax)*(cz-az) - (cx-ax)*(pz-az)
	hasNeg := d1 < 0 || d2 < 0 || d3 < 0
	hasPos := d1 > 0 || d2 > 0 || d3 > 0
	return !(hasNeg && hasPos)
}

// inMud is VIWalkControl::Run's walk-speed test: in a liquid, IN_MUD; out of
// one, mud at the feet.
func (c *Client) inMud() bool {
	if c.liquidFlags&terrain.LiquidAny != 0 {
		return c.liquidFlags&terrain.LiquidMud != 0
	}
	return c.liquidFlags&terrain.LiquidFeetInMud != 0
}
