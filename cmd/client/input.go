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
		// Respawn's camera half runs once the arrival sets the walker down
		// (arrival.go: resetCameraBehind).
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
	// Only logout (Ctrl+X) remains active.
	// PS2 VICSprite::SetDeath (0x00421588) sets IsDead flag, plays death anim (slot 14),
	// holds last frame. CLIENT game loop checks IsDead and blocks all input processing.
	// The camera is VIClient::DeathCamera's meanwhile (processWalkCamera).
	if c.engine.State.Player.Dead {
		c.currentSpeed = 0
		c.autoRun = false
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
	// PS2 VIWalkControl speed constants from InitParms (0x00487910).
	// Walk base = 3.0 (hardcoded, never changed by server).
	// Run base = server PlayerSpeed value (handler at 0x00BDD540, default 8.833).
	// Walk/run ratio = 3.0 / 8.833 = 0.3397.
	const (
		ps2WalkBase     = float32(3.0)    // PS2 InitParms walk base (fixed)
		ps2DefaultRunBase = float32(8.833) // PS2 InitParms default run base
		// PS2 UpdatePhysics (0x486198): exponential velocity damping.
		// powf(dampingBase, dt) applied per tick. dampingBase = InitParms+0x40 = 0.02.
		// At 30fps (dt=0.033): decay = 0.02^0.033 ≈ 0.876. At 60fps: 0.02^0.016 ≈ 0.936.
		dampingBase = float64(0.02)
		// Minimum speed before snapping to zero (avoids infinite deceleration tail).
		speedEpsilon = float32(0.05)

		// PS2 VIWalkControl::UpdatePhysics gravity constants.
		// Extracted from EE memory dump: _13VIWalkControl$Gravity at 0x004E31F8.
		gravityAccel    = float32(19.6) // $Gravity.Y magnitude (approx 2g, PS2 uses -19.6)
		waterGravity    = float32(1.0)  // PS2 $WGravity.Y = -1.0 (slow sink when submerged)
		airDamping      = float64(0.8)  // PS2 InitParms m_parm44: UpdatePhysics's damping in the air
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

	// The walk camera's axes for this frame: A/D and the right stick's x orbit
	// or turn, W/S and its y zoom or look, by camera mode (walkcamera.go).
	ctrlDown := rl.IsKeyDown(rl.KeyLeftControl) || rl.IsKeyDown(rl.KeyRightControl)
	c.gatherCameraInput(gpCamH, gpCamV, ctrlDown)

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
	// Until a 0x00F8 the walk control runs at its InitParms speeds; after
	// one, a run of 0 is a stop (ps2_sandstorm.pcap sends 0 after a respawn).
	serverSpeed := c.engine.State.Player.Speed
	if !c.engine.State.Session.SpeedSet {
		serverSpeed = ps2DefaultRunBase
	}
	// VIClient::SetSpeed (0x00F8): run is the server's value and walk a flat
	// 3.0 (0 when the run is 0); walk used to scale with the run.
	walkSpeedCap := ps2WalkBase
	if serverSpeed == 0 {
		walkSpeedCap = 0
	}

	// PS2 UpdatePhysics speed ramping: exponential damping + force accumulation.
	// velocity = velocity * powf(dampingBase, dt) + targetSpeed * (1 - powf(dampingBase, dt))
	// dampingBase = 0.02 from InitParms+0x40.
	decay := float32(math.Pow(dampingBase, float64(dt)))

	// In the air VIWalkControl::ViewRelativeMove adds no force and sets no
	// heading (it returns unless on a floor or in a liquid), and UpdatePhysics
	// damps by m_parm44 = 0.8 a second instead of 0.02: no air control, and
	// the run carries on.
	if zt != nil && !c.onGround && c.waterState == terrain.WaterNone && !c.gmNoClip {
		moveX, moveZ = 0, 0
		decay = float32(math.Pow(airDamping, float64(dt)))
	}

	steer := false // this frame sets a target heading (mode 3's SetTargetHeading)
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
			steer = true
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

		// PS2 VIWalkControl::Move's floor half — step up, step down, headroom.
		c.rampClimbing = false
		if zt != nil && c.onGround && !c.gmNoClip && c.waterState == terrain.WaterNone {
			c.walkFloor(zt, oldX, p.Y, oldZ)
		}

		c.engine.Walking = true
		// PS2 UpdateLocomotion: classify speed into animation state.
		c.engine.AnimByte = locoAnim(speed, walkSpeedCap, serverSpeed)
		c.engine.VelX = moveX * speed
		c.engine.VelZ = moveZ * speed
		if c.onGround {
			c.engine.VelY = 0
		}
		c.engine.ForceX, c.engine.ForceZ = c.driveForce(moveX, moveZ, targetSpeed, walkSpeedCap)
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
			c.engine.ForceX, c.engine.ForceZ = 0, 0
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
			if zt != nil && c.onGround && !c.gmNoClip && c.waterState == terrain.WaterNone {
				c.walkFloor(zt, oldX, p.Y, oldZ)
			}

			c.engine.Walking = true
			c.engine.AnimByte = locoAnim(speed, walkSpeedCap, serverSpeed)
			c.engine.VelX = c.lastMoveX * speed
			c.engine.VelZ = c.lastMoveZ * speed
			if c.onGround {
				c.engine.VelY = 0
			}
			c.engine.ForceX, c.engine.ForceZ = 0, 0
		}
	}

	// === Facing rotation ===
	// VIWalkControl's angular body at the camera style's turn rate: in third
	// person a bang-bang turn toward the move's heading (2π rad/s, style 3),
	// in first person and the fixed follow the turn keys (π/2 rad/s), which
	// the camera follows (walkcamera.go).
	c.steerWalker(dt, steer, c.targetFacing)

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

		// UpdatePhysics (viwalkcontrol.cpp:788) with the force at Gravity and
		// the air damping:
		//   pos += f·dt²·damp + v·dt·damp;  v = (v + f·dt)·damp
		// There is no terminal velocity. The 25 that stood here is the
		// camera's orbit limit, and its "6.0 deceleration" is the rate the
		// render height offset relaxes after a step (+0x18C → +0x104).
		damp := float32(math.Pow(airDamping, float64(dt)))
		f := -gravityAccel
		p.Y += f*dt*dt*damp + c.velocityY*dt*damp
		c.velocityY = (c.velocityY + f*dt) * damp

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
		if c.onGround {
			// Standing: Move runs every tick, so the floor follows here too.
			c.walkFloor(zt, p.X, p.Y, p.Z)
		} else if groundY, ok := zt.SampleHeight(p.X, p.Z, p.Y); ok {
			if p.Y <= groundY {
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
				c.floorY = groundY
			}
			// If airborne and above ground, keep falling (physics handles it).
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
	c.engine.SetWalkState(c.walkFlags(), c.floorY)

afterPhysics:

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
				c.floorY = groundY
			}
		} else {
			// No ground below — keep falling (gravity handles death).
			// Water detection in ProbeWater will catch underground water (e.g. FP).
		}
	}

	// U: first/third person (VIClient::InitCameraStyle, the PS2's Select).
	if rl.IsKeyPressed(rl.KeyU) {
		c.toggleFirstPerson()
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

// walkFloor is the floor half of VIWalkControl::Move (viwalkcontrol.cpp:1137)
// for a walker on land, once the sphere sweep has moved it on from (fromX,
// fromY, fromZ). CalcFit (terrain.Fit) finds the floor and the head; then
//   - with under 1.8 between them the move is refused (m_parm30);
//   - feet below the floor stand on it: any step up the probe can see, which
//     starts at the centre, so under 1.2;
//   - a floor less than 0.6 below the last one stood on is stepped down to
//     (m_parm28);
//   - anything else is a fall. The probe reaches only 0.6 below the feet, so
//     a drop it cannot see is a fall too.
//
// The old rule stepped up 1.8 and down 1.5, checked no headroom, and stood on
// ceilings.
func (c *Client) walkFloor(zt *terrain.ZoneTerrain, fromX, fromY, fromZ float32) {
	p := &c.engine.State.Player
	floor, head := zt.Fit(p.X, p.Y, p.Z)
	if head-floor < 1.8 {
		p.X, p.Y, p.Z = fromX, fromY, fromZ
		if terrain.CollDebug {
			fmt.Printf("CalcFit: headroom %.2f < 1.8 -> move refused\n", head-floor)
		}
		return
	}
	if p.Y < floor || c.floorY-floor < 0.6 {
		if terrain.CollDebug && p.Y != floor {
			fmt.Printf("CalcFit: Y %.2f -> %.2f\n", p.Y, floor)
		}
		p.Y = floor
		c.floorY = floor
		c.onGround = true
		c.velocityY = 0
		return
	}
	if terrain.CollDebug {
		fmt.Printf("CalcFit: floor %.2f is %.2f below the last -> falling\n", floor, c.floorY-floor)
	}
	c.onGround = false
}

// locoAnim is VIWalkControl::UpdateLocomotion's forward classification:
// a run past walk + (run − walk)·0.1 (3.58 at the default speeds), a walk past
// m_speedThreshold 0.5, and otherwise standing.
func locoAnim(speed, walk, run float32) byte {
	switch {
	case walk+(run-walk)*0.1 < speed:
		return animRun
	case 0.5 < speed:
		return 1 // walk
	}
	return 0
}
