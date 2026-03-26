package main

import (
	"fmt"
	"math"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/eqoa/client/internal/game"
	"github.com/eqoa/client/internal/gfx"
	"github.com/eqoa/client/internal/mathutil"
	"github.com/eqoa/client/internal/models"
	"github.com/eqoa/client/internal/protocol"
	"github.com/DabDavis/eqoa-esf-tools/pkg/buffer"
)

// Character select layout constants.
const (
	csListX      = int32(20)  // left panel X
	csListY      = int32(100) // first row Y
	csListRowH   = int32(28)  // row height
	csListW      = int32(210) // list panel width
	csPanelPad   = int32(15)  // padding inside panel

	// Right panel (3D model viewport)
	csModelPanelX   = int32(260) // left edge of model panel
	csModelPanelPad = int32(10)  // padding inside model panel

	// Server enforces 8 characters per account
	maxCharactersPerAccount = 8
)

func (c *Client) handleCharSelectInput() {
	// Appearance editor active — delegate input
	if c.appearanceEditActive {
		c.handleAppearanceEdit()
		return
	}

	// If creation wizard is active, delegate to it
	if c.charCreate.active {
		gs := c.engine.State
		// Check for CreateError from server (name taken)
		if gs.CreateError != "" {
			c.charCreate.error = gs.CreateError
			gs.CreateError = ""
			c.charCreate.submitted = false // allow retry
		}
		// Detect character list refresh after submission (creation success)
		if c.charCreate.submitted && len(gs.Characters) > c.charCreate.lastCharCnt {
			c.charCreate.active = false
			c.charSelectIdx = len(gs.Characters) - 1 // highlight new character
			return
		}
		c.handleCharCreateInput()
		return
	}

	// Escape or Triangle: dismiss delete confirm or disconnect
	gpTriangle := gpAvailable() && gpPressed(rl.GamepadButtonRightFaceUp)
	if rl.IsKeyPressed(rl.KeyEscape) || gpTriangle {
		if c.deleteConfirmActive {
			c.deleteConfirmActive = false
			return
		}
		c.disconnectAndReturnToLogin()
		return
	}

	// Delete confirmation dialog input
	if c.deleteConfirmActive {
		gpCross := gpAvailable() && gpPressed(rl.GamepadButtonRightFaceDown)
		gpCircle := gpAvailable() && gpPressed(rl.GamepadButtonRightFaceRight)
		if rl.IsKeyPressed(rl.KeyY) || gpCross {
			c.deleteConfirmActive = false
			c.sendDeleteCharacter()
			c.charSelectIdx = 0
		} else if rl.IsKeyPressed(rl.KeyN) || gpCircle {
			c.deleteConfirmActive = false
		}
		return
	}

	gs := c.engine.State
	n := len(gs.Characters)

	// Auto-highlight first character if none selected and chars exist
	if n > 0 && c.charSelectIdx < 0 {
		c.charSelectIdx = 0
		c.playCharSelectBGM(gs.Characters[0].Race)
	}

	// Arrow keys or D-pad change highlight
	prevIdx := c.charSelectIdx
	if n > 0 {
		if rl.IsKeyPressed(rl.KeyDown) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceDown)) {
			c.charSelectIdx = (c.charSelectIdx + 1) % n
		}
		if rl.IsKeyPressed(rl.KeyUp) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceUp)) {
			c.charSelectIdx = (c.charSelectIdx - 1 + n) % n
		}
	}

	// Play race-specific BGM and reset attack timer when character changes
	if n > 0 && c.charSelectIdx >= 0 && c.charSelectIdx != prevIdx {
		c.playCharSelectBGM(gs.Characters[c.charSelectIdx].Race)
		c.charSelectTime = 0
	}

	// Cross or Enter: enter world with highlighted character
	gpCross := gpAvailable() && gpPressed(rl.GamepadButtonRightFaceDown)
	if (rl.IsKeyPressed(rl.KeyEnter) || gpCross) && c.charSelectIdx >= 0 && c.charSelectIdx < n {
		gs.SelectCharacter(c.charSelectIdx)
		return
	}

	// Square: create new character
	if gpAvailable() && gpPressed(rl.GamepadButtonRightFaceLeft) && n < maxCharactersPerAccount {
		c.enterCharCreateWizard()
		return
	}

	// Mouse click on character row highlights it
	mx := int32(rl.GetMouseX())
	my := int32(rl.GetMouseY())
	panelX := csListX - csPanelPad
	panelRight := csListX + csListW + csPanelPad
	if rl.IsMouseButtonPressed(rl.MouseLeftButton) {
		// Check character list clicks
		if n > 0 && mx >= panelX && mx <= panelRight && my >= csListY {
			idx := int(my-csListY) / int(csListRowH+4)
			if idx >= 0 && idx < n && idx != c.charSelectIdx {
				c.charSelectIdx = idx
				c.charSelectTime = 0
				c.playCharSelectBGM(gs.Characters[idx].Race)
			}
		}
		// Check "Enter World" button click
		if c.charSelectIdx >= 0 && c.charSelectIdx < n && c.csEnterWorldHovered(mx, my) {
			gs.SelectCharacter(c.charSelectIdx)
			return
		}
		// Check "Create New" button click (only if under max)
		if n < maxCharactersPerAccount && c.csCreateNewHovered(mx, my) {
			c.enterCharCreateWizard()
			return
		}
		// Check "Appearance" button click
		if n > 0 && c.charSelectIdx >= 0 && c.csAppearanceHovered(mx, my) {
			c.enterAppearanceEdit()
			return
		}
		// Check "Delete" button click
		if n > 0 && c.charSelectIdx >= 0 && c.csDeleteHovered(mx, my) {
			c.deleteConfirmActive = true
			return
		}
	}

	// Number keys 1-9 highlight character
	for i := 0; i < n && i < 9; i++ {
		if rl.IsKeyPressed(int32(rl.KeyOne)+int32(i)) && i != c.charSelectIdx {
			c.charSelectIdx = i
			c.charSelectTime = 0
		}
	}
}

// enterCharCreateWizard initializes and activates the character creation wizard.
func (c *Client) enterCharCreateWizard() {
	cnt := 0
	if c.engine != nil {
		cnt = len(c.engine.State.Characters)
	}
	c.charCreate = charCreateState{
		active:      true,
		step:        0,
		humType:     1, // default East (Freeport)
		lastCharCnt: cnt,
	}
	// Play BGM for default Human East (Freeport)
	c.playCharSelectBGM(0)
}

// csCreateNewRect returns the position and size of the "Create New" button.
// Positioned below the character list in the left panel.
func (c *Client) csCreateNewRect() (x, y, w, h int32) {
	btnW := csListW - 10
	btnH := int32(30)
	btnX := csListX + 5
	btnY := csListY + maxCharactersPerAccount*(csListRowH+4) + 8
	return btnX, btnY, btnW, btnH
}

// csAppearanceRect returns the position and size of the "Change Appearance" button.
// Top-left of model viewport.
func (c *Client) csAppearanceRect() (x, y, w, h int32) {
	panelY := int32(50)
	return csModelPanelX + 8, panelY + 8, 130, 24
}

func (c *Client) csAppearanceHovered(mx, my int32) bool {
	bx, by, bw, bh := c.csAppearanceRect()
	return mx >= bx && mx <= bx+bw && my >= by && my <= by+bh
}

// csDeleteRect returns the position and size of the "Delete" button.
// Top-right of model viewport.
func (c *Client) csDeleteRect() (x, y, w, h int32) {
	panelY := int32(50)
	modelW := screenWidth - csModelPanelX - 15
	btnW := int32(70)
	btnH := int32(24)
	btnX := csModelPanelX + modelW - btnW - 8
	btnY := panelY + 8
	return btnX, btnY, btnW, btnH
}

// csDeleteHovered returns true if the mouse is over the "Delete" button.
func (c *Client) csDeleteHovered(mx, my int32) bool {
	btnX, btnY, btnW, btnH := c.csDeleteRect()
	return mathutil.RectContains(mx, my, btnX, btnY, btnW, btnH)
}

// sendDeleteCharacter sends opcode 0x002D with the selected character's ServerID.
func (c *Client) sendDeleteCharacter() {
	gs := c.engine.State
	if c.charSelectIdx < 0 || c.charSelectIdx >= len(gs.Characters) {
		return
	}
	ch := gs.Characters[c.charSelectIdx]
	// Encode ServerID as zigzag varint (matches server Read7BitEncodedInt64)
	var buf [8]byte
	w := buffer.NewWriter(buf[:])
	w.Write7BitEncodedInt64(int64(ch.ServerID))
	gs.Conn.QueueReliable(protocol.OpcodeDelCharacter, buf[:w.Position()])
}

// csCreateNewHovered returns true if the mouse is over the "Create New" button.
func (c *Client) csCreateNewHovered(mx, my int32) bool {
	btnX, btnY, btnW, btnH := c.csCreateNewRect()
	return mathutil.RectContains(mx, my, btnX, btnY, btnW, btnH)
}

// csEnterWorldHovered returns true if the mouse is over the "Enter World" button.
func (c *Client) csEnterWorldHovered(mx, my int32) bool {
	btnX, btnY, btnW, btnH := c.csEnterWorldRect()
	return mathutil.RectContains(mx, my, btnX, btnY, btnW, btnH)
}

// csEnterWorldRect returns the position and size of the "Enter World" button.
// Centered at the bottom of the model viewport.
func (c *Client) csEnterWorldRect() (x, y, w, h int32) {
	panelY := int32(50)
	panelH := screenHeight - panelY - 20
	modelW := screenWidth - csModelPanelX - 15
	btnW := modelW - 20
	btnH := int32(30)
	btnX := csModelPanelX + 10
	btnY := panelY + panelH - btnH - 8
	return btnX, btnY, btnW, btnH
}

// disconnectAndReturnToLogin sends the disconnect opcode, closes the server connection,
// and returns to the login screen.
func (c *Client) disconnectAndReturnToLogin() {
	if c.engine != nil {
		if c.engine.State.Phase == game.PhaseInWorld {
			c.saveChar()
		}
		if c.engine.State.Conn != nil {
			c.engine.SendDisconnect()
			c.engine.State.Conn.Close()
		}
	}
	c.engine = nil
	c.loginError = ""
	c.logoutActive = false
	c.menuOpen = false
	c.whoRequested = false
	c.auctionRequested = false
	c.auctionPage = 0
	c.charSelectIdx = -1
	// Reset to 640x480 for login screen
	screenWidth = 640
	screenHeight = 480
	rl.SetWindowSize(640, 480)
}

// drawCharacterSelect renders the character selection screen with animated
// background, character list on the left, and a 3D model preview on the right.
func (c *Client) drawCharacterSelect() {
	gs := c.engine.State
	gold := colorGoldWarm
	dimGold := rl.Color{R: 140, G: 120, B: 80, A: 255}

	// Animated title screen background (same as login)
	if c.titleScr != nil && c.titleScr.loaded {
		c.titleScr.drawTitleScene(c, false)
	}

	// Panel geometry (shared between left list and right model)
	panelY := int32(50)
	panelH := screenHeight - panelY - 20

	// Right model panel geometry
	modelPanelX := csModelPanelX
	modelPanelY := panelY
	modelPanelW := screenWidth - modelPanelX - 15
	modelPanelH := panelH

	// Draw dark panel behind model viewport (before 3D so it serves as background)
	rl.DrawRectangle(modelPanelX, modelPanelY, modelPanelW, modelPanelH, colorDarkPanel)

	// Draw 3D model inside the right panel (creation preview or char select preview)
	if c.charCreate.active {
		c.drawCharCreateModel(gs, modelPanelX, modelPanelY, modelPanelW, modelPanelH)
	} else {
		c.drawCharSelectModel(gs, modelPanelX, modelPanelY, modelPanelW, modelPanelH)
	}

	// Model viewport overlay buttons (only when a character is selected)
	if !c.charCreate.active && !c.appearanceEditActive &&
		c.charSelectIdx >= 0 && c.charSelectIdx < len(gs.Characters) {
		c.drawAppearanceButton()
		c.drawDeleteButton()
		c.drawEnterWorldButton()
	}

	// If creation wizard is active, draw it instead of char select list
	if c.charCreate.active {
		c.drawCharCreate()
		return
	}

	// If appearance editor is active, draw it instead of char select list
	if c.appearanceEditActive {
		panelX := csListX - csPanelPad
		panelW := csListW + csPanelPad*2
		rl.DrawRectangle(panelX, panelY, panelW, panelH, colorDarkPanel)
		c.drawAppearanceEdit()
		return
	}

	// Dark panel behind character list (drawn after 3D so it's on top)
	panelX := csListX - csPanelPad
	panelW := csListW + csPanelPad*2
	rl.DrawRectangle(panelX, panelY, panelW, panelH, colorDarkPanel)

	// Title (top of panel)
	sub := "Character Select"
	subW := c.measureText(sub, 14)
	c.drawText(sub, csListX+(csListW-subW)/2, panelY+8, 14, dimGold)

	if len(gs.Characters) == 0 {
		c.drawText("No characters found.", csListX, csListY, 14, rl.LightGray)
		c.drawCreateNewButton(gold)
		c.drawText("ESC - Back", 10, 10, 12, colorHintGray)
		return
	}

	raceNames := []string{"Human", "Elf", "Dark Elf", "Gnome", "Dwarf", "Troll", "Barbarian", "Halfling", "Erudite", "Ogre"}
	classNames := []string{"Warrior", "Ranger", "Paladin", "Shadow Knight", "Monk", "Bard", "Rogue", "Druid", "Shaman", "Cleric", "Magician", "Necromancer", "Enchanter", "Wizard", "Alchemist"}

	// Auto-highlight first if needed
	if c.charSelectIdx < 0 {
		c.charSelectIdx = 0
	}

	// Draw character rows
	y := csListY
	for i, ch := range gs.Characters {
		raceName := "Unknown"
		if ch.Race >= 0 && ch.Race < len(raceNames) {
			raceName = raceNames[ch.Race]
		}
		className := "Unknown"
		if ch.Class >= 0 && ch.Class < len(classNames) {
			className = classNames[ch.Class]
		}

		selected := i == c.charSelectIdx

		// Highlight row
		if selected {
			rl.DrawRectangle(csListX-4, y-2, csListW+8, csListRowH, colorSelectBg)
		}

		// Character name (prominent)
		prefix := "  "
		nameColor := rl.Color{R: 200, G: 190, B: 140, A: 255}
		if selected {
			prefix = "> "
			nameColor = rl.Color{R: 255, G: 245, B: 180, A: 255}
		}
		c.drawText(prefix+ch.Name, csListX, y+2, 16, nameColor)

		// Level / race / class on second line (smaller)
		detail := fmt.Sprintf("  Lv %d %s %s", ch.Level, raceName, className)
		c.drawText(detail, csListX, y+16, 10, dimGold)

		y += csListRowH + 4 // extra spacing between entries
	}

	// "Create New" button (only if under max)
	if len(gs.Characters) < maxCharactersPerAccount {
		c.drawCreateNewButton(gold)
	}

	// Delete button now drawn on model viewport (above)

	// Delete confirmation dialog
	if c.deleteConfirmActive && c.charSelectIdx >= 0 && c.charSelectIdx < len(gs.Characters) {
		c.drawDeleteConfirm(gs.Characters[c.charSelectIdx].Name)
	}

	// ESC hint top-left of screen
	c.drawText("ESC - Back", 10, 10, 12, colorHintGray)
}

// enterAppearanceEdit starts the appearance editor for the selected character.
func (c *Client) enterAppearanceEdit() {
	gs := c.engine.State
	idx := c.charSelectIdx
	if idx < 0 || idx >= len(gs.Characters) {
		return
	}
	ch := &gs.Characters[idx]
	c.appearanceEditActive = true
	c.appearanceEditIdx = idx
	// Load current appearance into charCreate fields for reuse of the UI
	c.charCreate.hairColor = int(ch.HairColor)
	c.charCreate.hairLength = int(ch.HairLength)
	c.charCreate.hairStyle = int(ch.HairStyle)
	c.charCreate.faceOption = int(ch.FaceOption)
	c.charCreate.selIdx = 0
	c.charCreate.race = ch.Race
}

// handleAppearanceEdit handles input for the appearance editor overlay.
func (c *Client) handleAppearanceEdit() {
	gs := c.engine.State
	idx := c.appearanceEditIdx
	if idx < 0 || idx >= len(gs.Characters) {
		c.appearanceEditActive = false
		return
	}
	ch := &gs.Characters[idx]

	// Escape: cancel and revert
	if rl.IsKeyPressed(rl.KeyEscape) || (gpAvailable() && gpPressed(rl.GamepadButtonRightFaceUp)) {
		c.appearanceEditActive = false
		return
	}

	// Enter: apply and save
	if rl.IsKeyPressed(rl.KeyEnter) || (gpAvailable() && gpPressed(rl.GamepadButtonRightFaceDown)) {
		ch.HairColor = byte(c.charCreate.hairColor)
		ch.HairLength = byte(c.charCreate.hairLength)
		ch.HairStyle = byte(c.charCreate.hairStyle)
		ch.FaceOption = byte(c.charCreate.faceOption)
		c.appearanceEditActive = false
		return
	}

	// Reuse appearance input handler (up/down to select, left/right to adjust)
	numOptions := 4
	if rl.IsKeyPressed(rl.KeyDown) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceDown)) {
		c.charCreate.selIdx = (c.charCreate.selIdx + 1) % numOptions
	}
	if rl.IsKeyPressed(rl.KeyUp) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceUp)) {
		c.charCreate.selIdx = (c.charCreate.selIdx - 1 + numOptions) % numOptions
	}

	adjustVal := func(val *int, delta, max int) {
		*val = (*val + delta + max) % max
	}
	leftPressed := rl.IsKeyPressed(rl.KeyLeft) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceLeft))
	rightPressed := rl.IsKeyPressed(rl.KeyRight) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceRight))
	if leftPressed || rightPressed {
		delta := 1
		if leftPressed {
			delta = -1
		}
		switch c.charCreate.selIdx {
		case 0:
			adjustVal(&c.charCreate.hairColor, delta, 8)
		case 1:
			adjustVal(&c.charCreate.hairLength, delta, 4)
		case 2:
			adjustVal(&c.charCreate.hairStyle, delta, 4)
		case 3:
			adjustVal(&c.charCreate.faceOption, delta, 4)
		}
	}

	// Mouse clicks
	if rl.IsMouseButtonPressed(rl.MouseLeftButton) {
		mx := int32(rl.GetMouseX())
		my := int32(rl.GetMouseY())
		startY := int32(100)
		rowH := int32(30)
		arrowLeftX := csListX
		arrowRightX := csListX + csListW - 24
		arrowW := int32(24)

		for i := 0; i < numOptions; i++ {
			by := startY + int32(i)*rowH
			if my >= by && my <= by+rowH {
				var val *int
				max := 4
				switch i {
				case 0:
					val = &c.charCreate.hairColor
					max = 8
				case 1:
					val = &c.charCreate.hairLength
				case 2:
					val = &c.charCreate.hairStyle
				case 3:
					val = &c.charCreate.faceOption
				}
				if val != nil {
					if mx >= arrowLeftX && mx <= arrowLeftX+arrowW {
						adjustVal(val, -1, max)
					} else if mx >= arrowRightX && mx <= arrowRightX+arrowW {
						adjustVal(val, 1, max)
					}
				}
			}
		}

		// Apply button
		applyX := csListX + 5
		applyY := startY + int32(numOptions)*rowH + 10
		applyW := csListW - 10
		applyH := int32(30)
		if mx >= applyX && mx <= applyX+applyW && my >= applyY && my <= applyY+applyH {
			ch.HairColor = byte(c.charCreate.hairColor)
			ch.HairLength = byte(c.charCreate.hairLength)
			ch.HairStyle = byte(c.charCreate.hairStyle)
			ch.FaceOption = byte(c.charCreate.faceOption)
			c.appearanceEditActive = false
		}
	}

	// Update the char preview with current edit values (temporary — not saved until Apply)
	ch.HairColor = byte(c.charCreate.hairColor)
	ch.HairLength = byte(c.charCreate.hairLength)
	ch.HairStyle = byte(c.charCreate.hairStyle)
	ch.FaceOption = byte(c.charCreate.faceOption)
}

// drawAppearanceEdit draws the appearance editor overlay.
func (c *Client) drawAppearanceEdit() {
	gold := colorGoldWarm
	startY := int32(100)
	rowH := int32(30)

	c.drawText("Change Appearance", csListX, int32(75), 14, gold)

	options := []struct{ label string; value int }{
		{"Hair Color", c.charCreate.hairColor},
		{"Hair Length", c.charCreate.hairLength},
		{"Hair Style", c.charCreate.hairStyle},
		{"Face", c.charCreate.faceOption},
	}

	for i, opt := range options {
		by := startY + int32(i)*rowH
		selected := i == c.charCreate.selIdx
		textColor := rl.Color{R: 200, G: 190, B: 140, A: 255}
		if selected {
			rl.DrawRectangle(csListX, by, csListW, rowH-2, colorSelectBg)
			textColor = rl.Color{R: 255, G: 245, B: 180, A: 255}
		}
		arrowColor := colorHintGray
		if selected {
			arrowColor = gold
		}
		c.drawText("<", csListX+4, by+7, 14, arrowColor)
		text := fmt.Sprintf("%s: %d", opt.label, opt.value)
		textW := c.measureText(text, 14)
		c.drawText(text, csListX+(csListW-textW)/2, by+7, 14, textColor)
		c.drawText(">", csListX+csListW-18, by+7, 14, arrowColor)
	}

	// Apply button
	applyY := startY + int32(len(options))*rowH + 10
	applyW := csListW - 10
	applyH := int32(30)
	applyX := csListX + 5
	mx := int32(rl.GetMouseX())
	my := int32(rl.GetMouseY())
	applyHover := mx >= applyX && mx <= applyX+applyW && my >= applyY && my <= applyY+applyH
	applyBg := rl.Color{R: 40, G: 60, B: 40, A: 220}
	if applyHover {
		applyBg = rl.Color{R: 60, G: 100, B: 60, A: 240}
	}
	rl.DrawRectangle(applyX, applyY, applyW, applyH, applyBg)
	rl.DrawRectangleLines(applyX, applyY, applyW, applyH, rl.Color{R: 100, G: 180, B: 100, A: 220})
	c.drawTextInRect("Apply", applyX, applyY+7, applyW, 16, rl.Color{R: 200, G: 255, B: 200, A: 255})

	c.drawText("Enter=Apply  ESC=Cancel", csListX, applyY+applyH+8, 10, colorHintGray)
	c.drawText("A/D or RMB drag = Rotate", csListX, applyY+applyH+22, 10, colorHintGray)
}

// charSelectAttackSequence returns the attack animation sequence for the char select
// screen based on level and dual-wield status. Uses PS2 SetAttackAction/SetAttackAltAction
// decompiled tables (SUPPORT 0x00421820 / 0x00421960).
//
// PS2 attack sequence:
//   Levels 1-19:  [Primary right]                             (single)
//   Levels 20-34: [Primary left, Primary right]               (double)
//   Levels 35+:   [Primary left, Primary right, Alt left, Alt right] (quad)
//
// Default to 1H Slash type (most common). Primary: right=23 left=22, Alt: right=41 left=40.
func charSelectAttackSequence(level int, dualWield, isMelee bool) []byte {
	// Default 1H Slash anims (PS2 SetAttackAction/SetAttackAltAction decompiled)
	priRight := byte(23)
	priLeft := byte(22)
	altRight := byte(41)
	altLeft := byte(40)

	// Casters/healers: single primary swing only, regardless of level or weapons
	if !isMelee {
		return []byte{priRight}
	}

	if !dualWield {
		// Single wield melee: alternate Primary/Alt on right hand
		if level >= 35 {
			return []byte{priRight, altRight, priRight, altRight}
		} else if level >= 20 {
			return []byte{priRight, altRight}
		}
		return []byte{priRight}
	}

	// Dual wield melee
	if level >= 35 {
		return []byte{priLeft, priRight, altLeft, altRight} // quad
	} else if level >= 20 {
		return []byte{priLeft, priRight} // double
	}
	return []byte{priRight} // single (right hand only at low level)
}

// drawAppearanceButton draws "Change Appearance" at top-left of model viewport.
func (c *Client) drawAppearanceButton() {
	apX, apY, apW, apH := c.csAppearanceRect()
	mx := int32(rl.GetMouseX())
	my := int32(rl.GetMouseY())
	hover := c.csAppearanceHovered(mx, my)

	bg := rl.Color{R: 20, G: 20, B: 35, A: 230}
	textColor := rl.Color{R: 200, G: 180, B: 100, A: 240}
	if hover {
		bg = colorSelectBg
		textColor = rl.Color{R: 255, G: 230, B: 130, A: 255}
	}
	rl.DrawRectangleRounded(rl.Rectangle{X: float32(apX), Y: float32(apY), Width: float32(apW), Height: float32(apH)}, 0.3, 4, bg)
	c.drawTextInRect("Change Appearance", apX, apY+5, apW, 12, textColor)
}

// drawCreateNewButton draws the "Create New" button on the char select screen.
func (c *Client) drawCreateNewButton(gold rl.Color) {
	cnX, cnY, cnW, cnH := c.csCreateNewRect()
	mx := int32(rl.GetMouseX())
	my := int32(rl.GetMouseY())
	cnHover := c.csCreateNewHovered(mx, my)

	cnBg := colorGmBtnBg
	cnBorder := rl.Color{R: 60, G: 60, B: 80, A: 255}
	cnTextColor := gold
	if cnHover {
		cnBg = colorBtnHoverDk
		cnBorder = gold
		cnTextColor = rl.Color{R: 255, G: 245, B: 180, A: 255}
	}
	rl.DrawRectangle(cnX-1, cnY-1, cnW+2, cnH+2, cnBorder)
	rl.DrawRectangle(cnX, cnY, cnW, cnH, cnBg)
	c.drawTextInRect("Create New", cnX, cnY+7, cnW, 16, cnTextColor)
}

// drawEnterWorldButton draws the "Enter World" button at bottom of model viewport.
func (c *Client) drawEnterWorldButton() {
	btnX, btnY, btnW, btnH := c.csEnterWorldRect()
	mx := int32(rl.GetMouseX())
	my := int32(rl.GetMouseY())
	hover := c.csEnterWorldHovered(mx, my)

	gold := colorGoldWarm
	bg := colorGmBtnBg
	border := rl.Color{R: 60, G: 60, B: 80, A: 255}
	textColor := gold
	if hover {
		bg = colorBtnHoverDk
		border = gold
		textColor = rl.Color{R: 255, G: 245, B: 180, A: 255}
	}
	rl.DrawRectangle(btnX-1, btnY-1, btnW+2, btnH+2, border)
	rl.DrawRectangle(btnX, btnY, btnW, btnH, bg)
	c.drawTextInRect("Enter World", btnX, btnY+7, btnW, 16, textColor)
}

// drawDeleteButton draws the "Delete" button on the char select screen.
func (c *Client) drawDeleteButton() {
	dX, dY, dW, dH := c.csDeleteRect()
	mx := int32(rl.GetMouseX())
	my := int32(rl.GetMouseY())
	hover := c.csDeleteHovered(mx, my)

	bg := rl.Color{R: 35, G: 15, B: 15, A: 230}
	textColor := rl.Color{R: 180, G: 80, B: 80, A: 240}
	if hover {
		bg = rl.Color{R: 60, G: 25, B: 25, A: 245}
		textColor = rl.Color{R: 255, G: 120, B: 120, A: 255}
	}
	rl.DrawRectangleRounded(rl.Rectangle{X: float32(dX), Y: float32(dY), Width: float32(dW), Height: float32(dH)}, 0.3, 4, bg)
	c.drawTextInRect("Delete", dX, dY+5, dW, 12, textColor)
}

// drawDeleteConfirm draws a confirmation dialog for character deletion.
func (c *Client) drawDeleteConfirm(charName string) {
	sw := screenWidth
	sh := screenHeight
	dlgW := int32(320)
	dlgH := int32(120)
	dlgX := (sw - dlgW) / 2
	dlgY := (sh - dlgH) / 2
	red := rl.Color{R: 200, G: 80, B: 80, A: 255}

	// Backdrop
	rl.DrawRectangle(0, 0, sw, sh, rl.Color{R: 0, G: 0, B: 0, A: 120})
	// Dialog box
	rl.DrawRectangle(dlgX-2, dlgY-2, dlgW+4, dlgH+4, red)
	rl.DrawRectangle(dlgX, dlgY, dlgW, dlgH, rl.Color{R: 20, G: 10, B: 10, A: 255})

	line1 := "Delete character?"
	line2 := charName
	line3 := "Y = Confirm    N / ESC = Cancel"

	c.drawTextInRect(line1, dlgX, dlgY+14, dlgW, 18, rl.White)
	c.drawTextInRect(line2, dlgX, dlgY+38, dlgW, 16, red)
	c.drawTextInRect(line3, dlgX, dlgY+72, dlgW, 13, rl.LightGray)
}

// drawCharSelectModel renders the 3D character model inside the given screen rect.
// Uses scissor + camera X offset (no glViewport) to avoid aspect ratio distortion.
func (c *Client) drawCharSelectModel(gs *game.GameState, px, py, pw, ph int32) {
	if c.modelMgr == nil || c.charSelectIdx < 0 || c.charSelectIdx >= len(gs.Characters) {
		return
	}

	ch := gs.Characters[c.charSelectIdx]
	modelID := ch.ModelID
	if modelID == 0 {
		return
	}

	dt := rl.GetFrameTime()
	c.charSelectTime += float64(dt)

	// Facing: auto-rotate normally, manual rotate during appearance edit
	var facing float32
	if c.appearanceEditActive {
		// L1/R1 or A/D to rotate manually
		rotSpeed := float32(2.0) * dt
		if rl.IsKeyDown(rl.KeyA) || (gpAvailable() && rl.IsGamepadButtonDown(0, rl.GamepadButtonLeftTrigger1)) {
			c.appearanceFacing -= rotSpeed
		}
		if rl.IsKeyDown(rl.KeyD) || (gpAvailable() && rl.IsGamepadButtonDown(0, rl.GamepadButtonRightTrigger1)) {
			c.appearanceFacing += rotSpeed
		}
		// Mouse drag to rotate
		if rl.IsMouseButtonDown(rl.MouseRightButton) {
			c.appearanceFacing += rl.GetMouseDelta().X * 0.01
		}
		facing = c.appearanceFacing
	} else {
		facing = float32(math.Mod(c.charSelectTime*0.5, 2*math.Pi))
	}

	// Build equipment from the CharInfo sent by the server in the char select packet.
	// Armor values of 0xFF mean "no equipment" — clamp to 0 for the renderer
	// (armor set 0 = bare skin). Robe is NOT clamped: the renderer treats
	// values 0-3 as valid robe styles and >= 4 as "no robe", so 0xFF works as-is.
	equip := &models.EquipmentState{
		Chest:      clampArmor(ch.Chest),
		Bracer:     clampArmor(ch.Bracer),
		Gloves:     clampArmor(ch.Gloves),
		Legs:       clampArmor(ch.Legs),
		Boots:      clampArmor(ch.Boots),
		Helm:       clampArmor(ch.Helm),
		Robe:       ch.Robe,
		ChestTint:  ch.ChestTint,
		BracerTint: ch.BracerTint,
		GlovesTint: ch.GlovesTint,
		LegsTint:   ch.LegsTint,
		BootsTint:  ch.BootsTint,
		HelmTint:   ch.HelmTint,
		RobeTint:   ch.RobeTint,
		HairColor:  ch.HairColor,
		HairLength: ch.HairLength,
		HairStyle:  ch.HairStyle,
		FaceOption: ch.FaceOption,
		Race:       byte(ch.Race),
		Sex:        game.SexFromModelID(uint32(ch.ModelID)),
	}
	primaryWpn := ch.PrimaryWeapon
	secondaryWpn := ch.SecondaryWeapon
	shield := ch.Shield

	// Compute camera X offset to center the model in the panel rect.
	// BeginMode3D uses the full window for projection, so we shift the camera
	// left so the model at origin projects onto the panel center.
	panelCenterX := float32(px) + float32(pw)/2
	screenCenterX := float32(screenWidth) / 2
	pixelOffset := panelCenterX - screenCenterX

	// Per-race camera: larger races need more distance.
	// Race: HUM=0, ELF=1, DELF=2, GNO=3, DWF=4, TRL=5, BAR=6, HLF=7, ERU=8, OGR=9
	type raceCam struct {
		dist, eyeY, targetY float32
	}
	raceCams := [10]raceCam{
		{3.5, 2.0, 1.1},  // HUM
		{3.5, 2.0, 1.1},  // ELF
		{3.5, 2.0, 1.1},  // DELF
		{3.0, 1.6, 0.8},  // GNO
		{3.0, 1.6, 0.8},  // DWF
		{4.5, 2.8, 1.5},  // TRL
		{4.0, 2.4, 1.3},  // BAR
		{3.0, 1.6, 0.8},  // HLF
		{3.5, 2.0, 1.1},  // ERU
		{4.5, 2.8, 1.5},  // OGR
	}
	rc := raceCams[0] // default human
	if ch.Race >= 0 && int(ch.Race) < len(raceCams) {
		rc = raceCams[ch.Race]
	}

	// Zoom to head during appearance editing
	if c.appearanceEditActive {
		rc.dist = 1.2
		headY := rc.eyeY        // head height from race table
		rc.eyeY = headY + 0.1   // camera slightly above head
		rc.targetY = headY - 0.3 // look down → pushes head to upper viewport
	}

	fovRad := float64(55) * math.Pi / 180
	windowAspect := float32(screenWidth) / float32(screenHeight)
	visibleWidth := 2 * rc.dist * float32(math.Tan(fovRad/2)) * windowAspect
	camOffsetX := pixelOffset * visibleWidth / float32(screenWidth)

	cam := rl.Camera3D{
		Position:   rl.Vector3{X: -camOffsetX, Y: rc.eyeY, Z: rc.dist},
		Target:     rl.Vector3{X: -camOffsetX, Y: rc.targetY, Z: 0},
		Up:         rl.Vector3{X: 0, Y: 1, Z: 0},
		Fovy:       55,
		Projection: rl.CameraPerspective,
	}

	// Scissor clips rendering to the panel rect (no aspect ratio distortion).
	rl.BeginScissorMode(px, py, pw, ph)
	gfx.ClearDepth()

	rl.BeginMode3D(cam)

	// Use a unique entity ID for animation state (won't collide with real entities)
	entityID := uint32(0xFFFF0000) + uint32(c.charSelectIdx)

	// Weapon AttackType from char select packet (bytes 17-19 of equipment block).
	atkType := int(ch.PrimaryAttackType)
	if atkType == 0 {
		atkType = 11
	}
	dw := ch.SecondaryAttackType > 0 && ch.SecondaryAttackType != 10 && shield == 0
	atkTable := weaponAttackAnims
	if dw {
		atkTable = weaponAttackAnimsDW
	}
	atkAnims := atkTable[atkType]
	if atkAnims == nil {
		atkAnims = atkTable[11]
	}

	// Auto-attack cycle: class/level swing count, back-to-back, then idle pause.
	numSwings := swingsPerAttack(byte(ch.Class), byte(ch.Level), dw)
	if numSwings > len(atkAnims) {
		numSwings = len(atkAnims)
	}
	swingDur := 0.6
	attackDur := float64(numSwings) * swingDur
	roundDur := attackDur + 3.0 // 3s combat idle between rounds (matches in-game auto-attack interval)

	var locoAnim, actionAnim byte
	firstDelay := 3.0 // combat idle before first salvo
	if c.charSelectTime < firstDelay {
		locoAnim = 17
		actionAnim = 0
	} else {
		timeInRound := math.Mod(c.charSelectTime-firstDelay, roundDur)
		if timeInRound < attackDur {
			swingIdx := int(timeInRound / swingDur)
			anim := atkAnims[swingIdx%len(atkAnims)]
			locoAnim = anim   // PS2 SetAction sets BOTH pairs
			actionAnim = anim
		} else {
			locoAnim = 17
			actionAnim = 0
		}
	}

	c.modelMgr.DrawEntity(modelID, 0, 0, 0, facing, 1.0, equip, entityID, locoAnim, actionAnim, dt, false, 1.0)

	// Draw weapons if humanoid model
	if c.modelMgr.ModelIsHumanoid(modelID) {
		c.modelMgr.DrawWeapon(int32(primaryWpn), modelID, 0, 0, 0, facing, models.WeaponSlotPrimary)
		c.modelMgr.DrawWeapon(int32(secondaryWpn), modelID, 0, 0, 0, facing, models.WeaponSlotSecondary)
		c.modelMgr.DrawWeapon(int32(shield), modelID, 0, 0, 0, facing, models.WeaponSlotShield)
	}

	rl.EndMode3D()
	rl.EndScissorMode()
}

// drawCharCreateModel renders a 3D preview of the character being created.
func (c *Client) drawCharCreateModel(gs *game.GameState, px, py, pw, ph int32) {
	if c.modelMgr == nil {
		return
	}

	// On the race step, preview the highlighted race (selIdx) instead of
	// the committed race, so the model updates as you browse.
	previewRace := c.charCreate.race
	if c.charCreate.step == 0 {
		previewRace = c.charCreate.selIdx
	}

	modelID := createModelID(previewRace, c.charCreate.gender)
	if modelID == 0 {
		return
	}

	dt := rl.GetFrameTime()
	c.charSelectTime += float64(dt)
	facing := float32(math.Mod(c.charSelectTime*0.5, 2*math.Pi))

	// New character: bare skin, no equipment
	sex := byte(c.charCreate.gender)
	hairLength := byte(c.charCreate.hairLength)
	// Gnome: always hairLength=3 (head hair + hat)
	if previewRace == 3 {
		hairLength = 3
	}
	equip := &models.EquipmentState{
		HairColor:  byte(c.charCreate.hairColor),
		HairLength: hairLength,
		HairStyle:  byte(c.charCreate.hairStyle),
		FaceOption: byte(c.charCreate.faceOption),
		Race:       byte(previewRace),
		Sex:        sex,
		Robe:       0xFF, // no robe
	}

	panelCenterX := float32(px) + float32(pw)/2
	screenCenterX := float32(screenWidth) / 2
	pixelOffset := panelCenterX - screenCenterX

	type raceCam struct {
		dist, eyeY, targetY float32
	}
	raceCams := [10]raceCam{
		{3.5, 2.0, 1.1}, {3.5, 2.0, 1.1}, {3.5, 2.0, 1.1},
		{3.0, 1.6, 0.8}, {3.0, 1.6, 0.8}, {4.5, 2.8, 1.5},
		{4.0, 2.4, 1.3}, {3.0, 1.6, 0.8}, {3.5, 2.0, 1.1},
		{4.5, 2.8, 1.5},
	}
	rc := raceCams[0]
	if previewRace >= 0 && previewRace < 10 {
		rc = raceCams[previewRace]
	}

	fovRad := float64(55) * math.Pi / 180
	windowAspect := float32(screenWidth) / float32(screenHeight)
	visibleWidth := 2 * rc.dist * float32(math.Tan(fovRad/2)) * windowAspect
	camOffsetX := pixelOffset * visibleWidth / float32(screenWidth)

	cam := rl.Camera3D{
		Position:   rl.Vector3{X: -camOffsetX, Y: rc.eyeY, Z: rc.dist},
		Target:     rl.Vector3{X: -camOffsetX, Y: rc.targetY, Z: 0},
		Up:         rl.Vector3{X: 0, Y: 1, Z: 0},
		Fovy:       55,
		Projection: rl.CameraPerspective,
	}

	rl.BeginScissorMode(px, py, pw, ph)
	gfx.ClearDepth()
	rl.BeginMode3D(cam)

	entityID := uint32(0xFFFE0000) // unique ID for creation preview
	c.modelMgr.DrawEntity(modelID, 0, 0, 0, facing, 1.0, equip, entityID, 0, 0, dt, false, 1.0)

	rl.EndMode3D()
	rl.EndScissorMode()
}

// Race → homeland BGM mapping from PS2 SUPPORT data table at 0x004ED448.
// PS2 uses bgm\qeynos.bgm for Human (not freeport — freeport.bgm only
// referenced in CLIENT VIWndCharCreateRace for east/west city selection).
var raceCharSelectBGM = [10]string{
	"QEYNOS.BGM",   // HUM (0) — Qeynos (PS2 table: bgm\qeynos.bgm)
	"ELVES.BGM",    // ELF (1) — Tethelin
	"DARKELVE.BGM", // DELF (2) — Neriak
	"GNOMES.BGM",   // GNO (3) — Klick'Anon
	"DWARVES.BGM",  // DWF (4) — Moradhim
	"TROLLS.BGM",   // TRL (5) — Grobb
	"BARBARIA.BGM", // BAR (6) — Halas
	"HALFLING.BGM", // HLF (7) — Rivervale
	"ERUDITES.BGM", // ERU (8) — Erudin
	"OGRE.BGM",     // OGR (9) — Oggok
}

// East-only Human classes (Freeport): SK=3, NEC=11
// West-only Human classes (Qeynos): RAN=1, PAL=2, MNK=4, DRD=7
// Shared classes default to Qeynos (PS2 race BGM table default)
func isHumanEast(class int) bool {
	return class == 3 || class == 11
}

func (c *Client) playCharSelectBGM(race int) {
	if race < 0 || race >= len(raceCharSelectBGM) {
		return
	}
	bgm := raceCharSelectBGM[race]
	// Human: determine East/West from humType (create) or class (char select)
	if race == 0 {
		isEast := false
		if c.charCreate.active {
			isEast = c.charCreate.humType == 1
		} else if c.charSelectIdx >= 0 && c.charSelectIdx < len(c.engine.State.Characters) {
			isEast = isHumanEast(c.engine.State.Characters[c.charSelectIdx].Class)
		}
		if isEast {
			bgm = "FREEPORT.BGM"
		}
	}
	if bgm != c.currentBGM {
		c.playBGMFromISO(bgm)
		c.currentBGM = bgm
	}
}
