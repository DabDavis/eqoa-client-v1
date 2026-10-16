package main

import (
	"log"
	"math"

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

	// --- Right Stick: the walk camera's axes (gatherCameraInput) ---
	// x orbits (third person) or turns (first person, fixed follow); y zooms
	// or looks. In a liquid y swims (on the PS2 that is SwimUp in third person
	// and PitchDown's swim branch at the surface in first person).
	camDH = gpStick(rl.GamepadAxisRightX)
	camDV = gpStick(rl.GamepadAxisRightY)
	if c.waterState != 0 {
		// In water: right stick Y = ascend/descend (up = ascend, down = descend)
		const gpSwimSpeed = float32(3.0) // PS2 InitParms+0x20 = 3.0
		c.swimVelY = -camDV * gpSwimSpeed // stick Y inverted: up=-1
		camDV = 0
	}

	// --- Select: first/third person (VIClient::InitCameraStyle, pad code 145) ---
	if gpPressed(rl.GamepadButtonMiddleLeft) {
		c.toggleFirstPerson()
	}

	// --- R3: back to the third-person camera, behind (a Go extra: on the
	// PS2, R3 is code 147, VIWndGame's Enter) ---
	if gpPressed(rl.GamepadButtonRightThumb) {
		c.toggleCameraStyle()
		log.Printf("Camera: reset to %s", cameraModeName(c.wcam.mode))
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
	} else if gpPressed(rl.GamepadButtonRightFaceDown) {
		// The attack button (the PS2's code 134): ToggleAutoAttack on the press,
		// MaybeStopAutoAttack on the release (top of handleInWorldInput).
		c.attackPressed()
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
			c.engine.InteractWithTarget() // the PS2's code 133
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
			c.engine.ClearTarget() // the PS2's code 132: no message
			c.targetChanged()
		}
	}

	// --- Start: Menu toggle (opening it stops auto-attack, func_00C55A20) ---
	if gpPressed(rl.GamepadButtonMiddleRight) {
		if !c.menuOpen {
			c.engine.StopAutoAttack()
		}
		c.menuOpen = !c.menuOpen
		c.menuTab = 0
		c.menuRow = 0
		c.menuScroll = 0
	}

	// --- R1: Tab target forward (not when menu open — R1 is sub-tab there) ---
	if gpPressed(rl.GamepadButtonRightTrigger1) && !c.menuOpen {
		c.tabTarget()
	}

	// --- L1: cycle the party: me, the roster, the pet (not when menu open) ---
	if gpPressed(rl.GamepadButtonLeftTrigger1) && !c.menuOpen {
		c.partyTarget()
	}

	// L2 is now used for the PS2 Hotkey Menu (handleHotkeyMenu in input.go).
	// Walk/run toggle remains on Z key.

	// --- L3 (Left stick click): Toggle auto-run ---
	if gpPressed(rl.GamepadButtonLeftThumb) {
		c.autoRun = !c.autoRun
	}

	// Select and R3 are handled once, above (they were handled twice, so
	// Select switched the mode and then the distance on the same press).

	return
}

// tabTargetReverse is the D-pad's left: a PC addition with no CLIENT
// counterpart, so it is the scene cycle (CycleTarget) one entry back.
func (c *Client) tabTargetReverse() {
	gs := c.engine.State
	if len(gs.Entities) == 0 {
		return
	}
	// Cycling forward len-1 times lands one back; the 0x0010s go out as the
	// PS2's own would for each step, so take the list once instead.
	ids := gs.SceneTargetOrder(c.targetView())
	if len(ids) == 0 {
		return
	}
	at := 0
	for i, id := range ids {
		if id == c.targetObjectID {
			at = i
			break
		}
	}
	c.engine.SetTarget(ids[(at+len(ids)-1)%len(ids)])
	c.targetChanged()
}
