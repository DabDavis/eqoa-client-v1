package main

import (
	"log"
	"math"
	"strings"
	"time"

	rl "github.com/gen2brain/raylib-go/raylib"
)

// PS2 DualShock 2 → Raylib gamepad button mapping for EQOA.
//
// PS2 EQOA Controls:
//   Left Stick:  Character movement (camera-relative)
//   Right Stick: Camera orbit (X) and zoom (Y)
//   D-Pad L/R:   Cycle targets (tab-target)
//   Cross (A):   Toggle auto-attack
//   Circle (B):  Cancel / Clear target / Close menu
//   Square (X):  Interact with target / Confirm dialogue
//   Triangle (Y): Escape (close dialogue / close menu / clear target)
//   L1:          Target self / cycle group members
//   R1:          Cycle target forward (tab-target)
//   L2:          Walk/Run toggle
//   R2:          (reserved)
//   Start:       Open/close main menu
//   Select:      Toggle first/third person
//   L3:          Toggle auto-run
//   R3:          Reset camera to default position
const (
	gpID = int32(0) // first gamepad
)

// gpAvailable returns true if a gamepad is connected.
func gpAvailable() bool {
	return rl.IsGamepadAvailable(gpID)
}

// gpStick reads a stick axis as the PS2 does, returns -1..1. The axis is
// taken back to the DualShock's byte (0..255, centre 127.5) and put through
// VIPad::ScaleStick.
func gpStick(axis int32) float32 {
	v := rl.GetGamepadAxisMovement(gpID, axis)
	b := math.Round(float64(v+1) * 127.5)
	return scaleStick(uint8(max(0, min(255, b))))
}

// scaleStick is VIPad::ScaleStick (vipad.cpp:136): bytes 88..167 are the dead
// zone, and the output ramps from 0 at its edge to ±1 at the stop, in steps
// of 1/88.
func scaleStick(v uint8) float32 {
	if v < 88 {
		return float32(88-int(v)) * -0.0113636364
	}
	if v >= 168 {
		return float32(int(v)-167) * 0.0113636364
	}
	return 0
}

// gpPressed returns true on the frame a gamepad button is first pressed.
func gpPressed(button int32) bool {
	return rl.IsGamepadButtonPressed(gpID, button)
}

// gpDown returns true while a gamepad button is held.
func gpDown(button int32) bool {
	return rl.IsGamepadButtonDown(gpID, button)
}

// handleGamepadInWorld processes gamepad input for in-world gameplay.
// Called from handleInWorldInput alongside keyboard input.
func (c *Client) handleGamepadInWorld() (inputFwd, inputRight, camDH, camDV float32) {
	if !gpAvailable() {
		return
	}

	// Auto-show spell bar on first gamepad frame
	if !c.spellBarVisible {
		c.spellBarVisible = true
		c.spellBarFade = 3.0
	}

	// --- Left Stick: Movement ---
	lx := gpStick(rl.GamepadAxisLeftX)
	ly := gpStick(rl.GamepadAxisLeftY)
	inputRight = lx
	inputFwd = -ly // stick Y is inverted (up = -1)

	// --- Right Stick: Camera orbit (X) + Zoom/Swim (Y) ---
	rx := gpStick(rl.GamepadAxisRightX)
	ry := gpStick(rl.GamepadAxisRightY)
	camRotSpeed := float32(10.0) * c.camSensitivity // Camera orbit force (rad/s²), scaled by sensitivity
	camDH = rx * camRotSpeed // returns force magnitude, integrated by caller like keyboard
	if c.waterState != 0 {
		// In water: right stick Y = ascend/descend (up = ascend, down = descend)
		const gpSwimSpeed = float32(3.0) // PS2 InitParms+0x20 = 3.0
		c.swimVelY = -ry * gpSwimSpeed // stick Y inverted: up=-1
	} else if c.camMode >= 1 {
		// Follow cam / first-person: right stick Y = pitch (look up/down).
		if c.invertPitch {
			camDV = -ry * camRotSpeed
		} else {
			camDV = ry * camRotSpeed
		}
	} else {
		// Third-person: right stick Y = zoom via velocity (PS2 OrbitIn damped).
		zoomSpeed := float32(5) * c.camSensitivity
		c.camDistVel += ry * zoomSpeed
	}

	// --- Select button: cycle camera mode ---
	if gpPressed(rl.GamepadButtonMiddleLeft) {
		c.cycleCameraMode()
	}

	// --- R3 (right stick press): reset camera to default tether ---
	if gpPressed(rl.GamepadButtonRightThumb) {
		c.camMode = 0
		c.firstPerson = false
		c.camDist = 11.75 // PS2 default orbit distance
		c.camPitch = 0.5236 // PS2 default pitch (30 degrees)
		c.camDistVel = 0
		c.camAngleVel = 0
		c.camPitchVel = 0
		log.Printf("Camera: reset to default tether")
	}

	// --- D-Pad: Dialogue navigation / Spell bar / target cycling ---
	if !c.hotkeyActive {
		gs := c.engine.State
		if gs.DialogueActive && len(gs.DialogueOptions) > 0 {
			// D-pad up/down navigates dialogue options
			if gpPressed(rl.GamepadButtonLeftFaceUp) {
				c.dialogueCursor--
				if c.dialogueCursor < 0 {
					c.dialogueCursor = len(gs.DialogueOptions) - 1
				}
			}
			if gpPressed(rl.GamepadButtonLeftFaceDown) {
				c.dialogueCursor++
				if c.dialogueCursor >= len(gs.DialogueOptions) {
					c.dialogueCursor = 0
				}
			}
		} else {
			if gpPressed(rl.GamepadButtonLeftFaceUp) {
				c.spellBarCursor--
				if c.spellBarCursor < 0 {
					c.spellBarCursor = 9
				}
				c.spellBarVisible = true
				c.spellBarFade = 3.0 // 3s visibility
			}
			if gpPressed(rl.GamepadButtonLeftFaceDown) {
				c.spellBarCursor++
				if c.spellBarCursor > 9 {
					c.spellBarCursor = 0
				}
				c.spellBarVisible = true
				c.spellBarFade = 3.0
			}
			if gpPressed(rl.GamepadButtonLeftFaceRight) {
				c.tabTarget()
			}
			if gpPressed(rl.GamepadButtonLeftFaceLeft) {
				c.tabTargetReverse()
			}
		}
	}

	// --- Circle (B): Cast selected spell (when spell bar cursor visible) ---
	// Note: Circle also handles dialogue/menu/target — check spell bar first when visible
	// This is handled in the Circle block below by priority.

	// --- Cross (A): Confirm dialogue selection / Toggle auto-attack ---
	if gpPressed(rl.GamepadButtonRightFaceDown) && c.engine.State.DialogueActive && len(c.engine.State.DialogueOptions) > 0 {
		gs := c.engine.State
		if c.dialogueCursor >= 0 && c.dialogueCursor < len(gs.DialogueOptions) {
			c.engine.SendDialogueOption(gs.DialogueCounter, byte(c.dialogueCursor))
		}
		gs.DialogueActive = false
		gs.DialogueOptions = nil
		c.dialogueCursor = 0
	} else if gpPressed(rl.GamepadButtonRightFaceDown) && c.targetObjectID != 0 {
		if c.animMgr.CombatEngaged {
			c.animMgr.CombatEngaged = false
			c.engine.CombatEngaged = false
			c.engine.AttackSent = false
			c.animMgr.InCombat = false
			c.engine.InCombat = false
			c.animMgr.playerCombat.inCombat = false
			c.animMgr.playerCombat.combatAnim = 0
			log.Printf("COMBAT: Gamepad Cross — disengaged")
		} else {
			// PS2 auto-approach: player runs to target first, Attack sent when in melee range.
			c.animMgr.CombatEngaged = true
			c.engine.CombatEngaged = true
			c.engine.AttackSent = false
			c.animMgr.playerCombat.attackCycleIdx = 0
			c.animMgr.playerCombat.lastActivity = time.Now()
			log.Printf("COMBAT: Gamepad Cross — engaged, will auto-approach")
		}
	}

	// --- Circle (B): Cast spell (if spell bar cursor visible) / Cancel dialogue/menu ---
	// PS2: Circle does NOT clear target — use Escape/Triangle for that.
	if gpPressed(rl.GamepadButtonRightFaceRight) {
		gs := c.engine.State
		if gs.DialogueActive {
			if len(gs.DialogueOptions) > 0 {
				c.engine.SendDialogueOption(gs.DialogueCounter, 255)
			}
			gs.DialogueActive = false
			gs.DialogueOptions = nil
			c.dialogueCursor = 0
		} else if c.spellBarVisible {
			c.castHotbarSlot(c.spellBarCursor)
			c.spellBarFade = 3.0 // reset fade timer, keep visible for repeat casts
		} else if c.menuOpen {
			c.menuOpen = false
		}
	}

	// --- Square (X): Confirm dialogue selection / Interact ---
	if gpPressed(rl.GamepadButtonRightFaceLeft) {
		if c.engine.State.DialogueActive {
			gs := c.engine.State
			if len(gs.DialogueOptions) > 0 && c.dialogueCursor >= 0 && c.dialogueCursor < len(gs.DialogueOptions) {
				c.engine.SendDialogueOption(gs.DialogueCounter, byte(c.dialogueCursor))
			}
			gs.DialogueActive = false
			gs.DialogueOptions = nil
			c.dialogueCursor = 0
		} else if c.targetObjectID != 0 {
			if ent, ok := c.engine.State.Entities[c.targetObjectID]; ok && ent.IsNPC {
				// NPC target: interact
				c.engine.SendInteract(c.targetObjectID)
				if ent.NPCType&0x0100 != 0 || strings.HasPrefix(ent.Name, "Merchant") {
					c.engine.State.MerchantName = ent.Name
					c.engine.SendMerchantDiag(c.targetObjectID)
				}
			}
		}
	}

	// --- Triangle (Y): Escape (close dialogue / close menu / clear target) ---
	if gpPressed(rl.GamepadButtonRightFaceUp) {
		gs := c.engine.State
		if gs.DialogueActive {
			if len(gs.DialogueOptions) > 0 {
				c.engine.SendDialogueOption(gs.DialogueCounter, 255)
			}
			gs.DialogueActive = false
			gs.DialogueOptions = nil
			c.dialogueCursor = 0
		} else if c.menuOpen {
			c.menuOpen = false
		} else if c.targetObjectID != 0 {
			c.targetObjectID = 0
			c.engine.TargetObjectID = 0
			c.engine.SendTarget(0xFFFFFFFF)
			c.engine.ForcePositionFlush()
		}
	}

	// --- Start: Menu toggle ---
	if gpPressed(rl.GamepadButtonMiddleRight) {
		c.menuOpen = !c.menuOpen
		c.menuTab = 0
		c.menuRow = 0
		c.menuScroll = 0
	}

	// --- R1: Tab target forward (not when menu open — R1 is sub-tab there) ---
	if gpPressed(rl.GamepadButtonRightTrigger1) && !c.menuOpen {
		c.tabTarget()
	}

	// --- L1: Target self / cycle group members (not when menu open) ---
	if gpPressed(rl.GamepadButtonLeftTrigger1) && !c.menuOpen {
		c.targetGroupCycle()
	}

	// L2 is now used for the PS2 Hotkey Menu (handleHotkeyMenu in input.go).
	// Walk/run toggle remains on Z key.

	// --- L3 (Left stick click): Toggle auto-run ---
	if gpPressed(rl.GamepadButtonLeftThumb) {
		c.autoRun = !c.autoRun
	}

	// --- Select (Back): Toggle first/third person ---
	if gpPressed(rl.GamepadButtonMiddleLeft) {
		if c.camDist < 0.1 {
			if c.camSavedDist < 1 {
				c.camSavedDist = 11.75 // PS2 default orbit distance
			}
			c.camDist = c.camSavedDist
		} else {
			c.camSavedDist = c.camDist
			c.camDist = 0
		}
	}

	// --- R3 (Right stick click): Reset camera to PS2 default ---
	if gpPressed(rl.GamepadButtonRightThumb) {
		c.camPitch = 0.5236 // PS2 default: PI/6
		c.camDist = 11.75   // PS2 VIWalkCamera default: InitParms+0x254
	}

	return
}

// tabTargetReverse cycles to the previous nearest entity (reverse tab-target).
func (c *Client) tabTargetReverse() {
	gs := c.engine.State
	p := &gs.Player
	if len(gs.Entities) == 0 {
		return
	}

	// Build sorted list by distance, pick the one before current target
	type entDist struct {
		id   uint32
		dist float32
	}
	var sorted []entDist
	for _, ent := range gs.Entities {
		if ent.Name == "" {
			continue
		}
		dx, dz := ent.X-p.X, ent.Z-p.Z
		d := float32(math.Sqrt(float64(dx*dx + dz*dz)))
		sorted = append(sorted, entDist{ent.ObjectID, d})
	}
	if len(sorted) == 0 {
		return
	}

	// Sort by distance
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].dist < sorted[i].dist {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	// Find current target, go to previous
	idx := -1
	for i, e := range sorted {
		if e.id == c.targetObjectID {
			idx = i
			break
		}
	}
	idx--
	if idx < 0 {
		idx = len(sorted) - 1
	}

	newTarget := sorted[idx].id
	c.targetObjectID = newTarget
	c.engine.TargetObjectID = newTarget
	c.engine.SendTarget(newTarget)
	c.engine.ForcePositionFlush()
}

// targetGroupCycle cycles through self and group members (L1).
// Order: self → group member 1 → group member 2 → ... → wrap to self.
func (c *Client) targetGroupCycle() {
	gs := c.engine.State
	selfID := gs.PlayerObjectID
	if selfID == 0 {
		return
	}

	// Build ordered list: self first, then group members
	ids := []uint32{selfID}
	for _, gm := range gs.GroupMembers {
		if gm.ObjectID != 0 && gm.ObjectID != selfID {
			ids = append(ids, gm.ObjectID)
		}
	}

	// Find current target in list and pick next
	nextIdx := 0
	for i, id := range ids {
		if id == c.targetObjectID {
			nextIdx = (i + 1) % len(ids)
			break
		}
	}

	c.targetObjectID = ids[nextIdx]
	c.engine.TargetObjectID = ids[nextIdx]
	c.engine.SendTarget(ids[nextIdx])
	c.engine.ForcePositionFlush()
}
