package main

import (
	"fmt"
	"math"
	"sort"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/eqoa/client/internal/game"
	"github.com/eqoa/client/internal/gfx"
	"github.com/eqoa/client/internal/models"
)

// =============================================================================
// Reusable UI Widget Helpers
//
// Immediate-mode widgets that handle drawing + input in one call.
// Each returns the Y position after the widget for easy vertical stacking.
// All widgets use the Client's font via c.drawText / c.measureText.
//
// UI Style: toggle between "modern" (clean, rounded, gradients) and
// "classic" (PS2 UI.ESF sprite sheets). Set via c.uiStyleClassic.
// =============================================================================

// uiStyle controls visual appearance of all widgets.
type uiStyle int

const (
	uiStyleModern  uiStyle = 0 // clean dark panels, rounded corners, gradients
	uiStyleClassic uiStyle = 1 // PS2 UI.ESF sprite frames (future)
)

// --- Button ---

// uiButton draws a clickable text button. Returns true if clicked.
func (c *Client) uiButton(x, y int32, label string, fontSize int32) bool {
	w := c.measureText(label, fontSize)
	h := fontSize + 4
	mx, my := rl.GetMouseX(), rl.GetMouseY()
	hover := mx >= x && mx < x+w && my >= y && my < y+h
	clr := colorDim
	if hover {
		clr = rl.Color{R: 0, G: 200, B: 200, A: 255}
	}
	c.drawText(label, x, y+2, fontSize, clr)
	return hover && rl.IsMouseButtonPressed(rl.MouseButtonLeft)
}

// uiButtonStyled draws a button with background. Returns true if clicked.
func (c *Client) uiButtonStyled(x, y, w, h int32, label string, fontSize int32) bool {
	mx, my := rl.GetMouseX(), rl.GetMouseY()
	hover := mx >= x && mx < x+w && my >= y && my < y+h
	rect := rl.Rectangle{X: float32(x), Y: float32(y), Width: float32(w), Height: float32(h)}

	if c.uiTheme == uiStyleModern {
		// Gradient background + rounded corners
		bg := rl.Color{R: 30, G: 30, B: 55, A: 255}
		border := rl.Color{R: 60, G: 60, B: 90, A: 255}
		if hover {
			bg = rl.Color{R: 40, G: 40, B: 75, A: 255}
			border = rl.Color{R: 0, G: 180, B: 180, A: 255}
		}
		rl.DrawRectangleRounded(rect, 0.15, 4, bg)
		rl.DrawRectangleRoundedLines(rect, 0.15, 4, border)
	} else {
		bg := rl.Color{R: 30, G: 30, B: 50, A: 255}
		if hover {
			bg = rl.Color{R: 40, G: 40, B: 70, A: 255}
		}
		rl.DrawRectangle(x, y, w, h, bg)
		rl.DrawRectangleLines(x, y, w, h, rl.Color{R: 60, G: 60, B: 80, A: 255})
	}

	tw := c.measureText(label, fontSize)
	// Text shadow in modern mode
	if c.uiTheme == uiStyleModern {
		c.drawText(label, x+(w-tw)/2+1, y+(h-fontSize)/2+1, fontSize, rl.Color{R: 0, G: 0, B: 0, A: 120})
	}
	c.drawText(label, x+(w-tw)/2, y+(h-fontSize)/2, fontSize, colorGold)
	return hover && rl.IsMouseButtonPressed(rl.MouseButtonLeft)
}

// --- Text Input ---

// uiTextInput draws an editable text input box. Handles focus, typing, backspace.
func (c *Client) uiTextInput(x, y, w int32, text *string, focused *bool, maxLen int) {
	h := int32(18)
	mx, my := rl.GetMouseX(), rl.GetMouseY()

	// Click to focus/unfocus
	if rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
		*focused = mx >= x && mx < x+w && my >= y && my < y+h
	}

	// Draw box
	boxBg := rl.Color{R: 12, G: 12, B: 20, A: 255}
	border := rl.Color{R: 50, G: 50, B: 70, A: 255}
	if *focused {
		border = rl.Color{R: 0, G: 200, B: 200, A: 255}
	}
	rl.DrawRectangle(x, y, w, h, boxBg)
	rl.DrawRectangleLines(x, y, w, h, border)

	// Handle input when focused
	if *focused {
		key := rl.GetCharPressed()
		for key > 0 {
			if key >= 32 && key < 127 && len(*text) < maxLen {
				*text += string(rune(key))
			}
			key = rl.GetCharPressed()
		}
		if rl.IsKeyPressed(rl.KeyBackspace) && len(*text) > 0 {
			*text = (*text)[:len(*text)-1]
		}
		if rl.IsKeyPressed(rl.KeyEnter) || rl.IsKeyPressed(rl.KeyEscape) {
			*focused = false
		}
	}

	// Draw text
	display := *text
	if display == "" && !*focused {
		display = "(all)"
	}
	if *focused {
		display += "_"
	}
	c.drawText(display, x+3, y+3, 11, colorWhite)
}

// --- Label ---

// uiLabel draws a text label.
func (c *Client) uiLabel(x, y int32, text string, fontSize int32, color rl.Color) {
	c.drawText(text, x, y+2, fontSize, color)
}

// --- Checkbox ---

// uiCheckbox draws a checkbox with label. Returns new state.
func (c *Client) uiCheckbox(x, y int32, label string, checked *bool) bool {
	boxSize := int32(14)
	mx, my := rl.GetMouseX(), rl.GetMouseY()
	labelW := c.measureText(label, 11)
	totalW := boxSize + 4 + labelW
	hover := mx >= x && mx < x+totalW && my >= y && my < y+boxSize+2

	// Draw checkbox
	border := colorDim
	if hover {
		border = colorGold
	}
	rl.DrawRectangleLines(x, y+1, boxSize, boxSize, border)
	if *checked {
		rl.DrawRectangle(x+2, y+3, boxSize-4, boxSize-4, colorGold)
	}

	// Label
	c.drawText(label, x+boxSize+4, y+1, 11, colorWhite)

	// Click
	if hover && rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
		*checked = !*checked
		return true
	}
	return false
}

// --- Slider ---

// uiSliderInt draws a horizontal slider for an integer value. Returns true if changed.
func (c *Client) uiSliderInt(x, y, w int32, val *int, min, max int) bool {
	h := int32(16)
	mx, my := rl.GetMouseX(), rl.GetMouseY()
	changed := false

	// Track
	trackY := y + h/2 - 2
	rl.DrawRectangle(x, trackY, w, 4, rl.Color{R: 30, G: 30, B: 50, A: 255})

	// Thumb position
	frac := float32(*val-min) / float32(max-min)
	thumbX := x + int32(frac*float32(w-8))
	thumbW := int32(8)

	// Draw thumb
	thumbColor := colorDim
	hover := mx >= x && mx < x+w && my >= y && my < y+h
	if hover {
		thumbColor = colorGold
	}
	rl.DrawRectangle(thumbX, y, thumbW, h, thumbColor)

	// Drag
	if hover && rl.IsMouseButtonDown(rl.MouseButtonLeft) {
		newFrac := float32(mx-x) / float32(w)
		if newFrac < 0 {
			newFrac = 0
		}
		if newFrac > 1 {
			newFrac = 1
		}
		newVal := min + int(newFrac*float32(max-min))
		if newVal != *val {
			*val = newVal
			changed = true
		}
	}

	// Value label
	c.drawText(fmt.Sprintf("%d", *val), x+w+4, y+1, 11, colorWhite)

	return changed
}

// uiSliderFloat draws a horizontal slider for a float32 value. Returns true if changed.
// uiSliderFloat draws a labeled float slider. displayVal is shown to the right of the slider.
func (c *Client) uiSliderFloat(x, y, w int32, val *float32, min, max float32, label, displayVal string) bool {
	h := int32(16)
	mx, my := rl.GetMouseX(), rl.GetMouseY()
	changed := false

	// Label
	c.drawText(label, x, y-16, 14, colorGold)

	// Track
	trackY := y + h/2 - 2
	rl.DrawRectangle(x, trackY, w, 4, rl.Color{R: 30, G: 30, B: 50, A: 255})

	// Fill
	frac := (*val - min) / (max - min)
	if frac < 0 { frac = 0 }
	if frac > 1 { frac = 1 }
	rl.DrawRectangle(x, trackY, int32(frac*float32(w)), 4, colorGold)

	// Thumb
	thumbX := x + int32(frac*float32(w-8))
	thumbColor := colorDim
	hover := mx >= x && mx < x+w && my >= y && my < y+h
	if hover { thumbColor = colorGold }
	rl.DrawRectangle(thumbX, y, 8, h, thumbColor)

	// Drag
	if hover && rl.IsMouseButtonDown(rl.MouseButtonLeft) {
		newFrac := float32(mx-x) / float32(w)
		if newFrac < 0 { newFrac = 0 }
		if newFrac > 1 { newFrac = 1 }
		newVal := min + newFrac*(max-min)
		if newVal != *val {
			*val = newVal
			changed = true
		}
	}

	// Value label
	c.drawText(displayVal, x+w+4, y+1, 11, colorWhite)

	return changed
}

// --- Scroll List ---

// ScrollListRow is a callback to draw one row of a scroll list.
type ScrollListRow func(index int, x, y, w int32, hover bool)

// uiScrollList draws a scrollable list with alternating row backgrounds.
// Returns the index of clicked row (-1 if none).
func (c *Client) uiScrollList(x, y, w, h int32, itemCount int, scroll *int, lineH int32, drawRow ScrollListRow) int {
	mx, my := rl.GetMouseX(), rl.GetMouseY()
	maxVisible := int(h / lineH)
	clicked := -1

	// Clamp scroll
	if *scroll > itemCount-maxVisible {
		*scroll = itemCount - maxVisible
	}
	if *scroll < 0 {
		*scroll = 0
	}

	// Mouse wheel scroll (when hovering list area)
	if mx >= x && mx < x+w && my >= y && my < y+h {
		wheel := rl.GetMouseWheelMove()
		if wheel != 0 {
			*scroll -= int(wheel * 3)
			if *scroll < 0 {
				*scroll = 0
			}
			if *scroll > itemCount-maxVisible {
				*scroll = itemCount - maxVisible
			}
		}
	}

	// Row backgrounds and callbacks
	rowBg1 := rl.Color{R: 18, G: 18, B: 28, A: 255}
	rowBg2 := rl.Color{R: 22, G: 22, B: 35, A: 255}
	hoverBg := rl.Color{R: 30, G: 30, B: 50, A: 255}

	for i := *scroll; i < itemCount && int32(i-*scroll)*lineH < h; i++ {
		ey := y + int32(i-*scroll)*lineH

		bg := rowBg1
		if i%2 == 1 {
			bg = rowBg2
		}
		rowHover := mx >= x && mx < x+w && my >= ey && my < ey+lineH
		if rowHover {
			bg = hoverBg
		}
		rl.DrawRectangle(x, ey, w, lineH, bg)

		drawRow(i, x, ey, w, rowHover)

		if rowHover && rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
			clicked = i
		}
	}

	// Scrollbar
	if itemCount > maxVisible && maxVisible > 0 {
		scrollFrac := float32(*scroll) / float32(itemCount-maxVisible)
		barH := h * int32(maxVisible) / int32(itemCount)
		if barH < 10 {
			barH = 10
		}
		barY := y + int32(scrollFrac*float32(h-barH))
		rl.DrawRectangle(x+w-3, barY, 3, barH, rl.Color{R: 60, G: 60, B: 80, A: 200})
	}

	return clicked
}

// --- Section Header ---

// uiSectionHeader draws a colored header bar with text.
func (c *Client) uiSectionHeader(x, y, w int32, text string) {
	h := int32(18)
	if c.uiTheme == uiStyleModern {
		// Gradient header: darker at top, lighter at bottom
		rl.DrawRectangleGradientV(x, y, w, h,
			rl.Color{R: 30, G: 28, B: 50, A: 255},
			rl.Color{R: 22, G: 20, B: 38, A: 255})
		rl.DrawRectangle(x, y+h-1, w, 1, rl.Color{R: 60, G: 55, B: 80, A: 255})
	} else {
		rl.DrawRectangle(x, y, w, h, rl.Color{R: 25, G: 25, B: 40, A: 255})
	}
	c.drawText(text, x+4, y+2, 12, colorGold)
}

// --- Divider ---

// uiDividerV draws a vertical divider line.
func (c *Client) uiDividerV(x, y, h int32) {
	rl.DrawRectangle(x, y, 1, h, rl.Color{R: 40, G: 40, B: 60, A: 255})
}

// uiDividerH draws a horizontal divider line.
func (c *Client) uiDividerH(x, y, w int32) {
	rl.DrawRectangle(x, y, w, 1, rl.Color{R: 40, G: 40, B: 60, A: 255})
}

// --- Panel Background ---

// uiPanel draws a dark panel background.
func (c *Client) uiPanel(x, y, w, h int32) {
	if c.uiTheme == uiStyleModern {
		// Rounded corners + subtle drop shadow
		rl.DrawRectangleRounded(
			rl.Rectangle{X: float32(x + 2), Y: float32(y + 2), Width: float32(w), Height: float32(h)},
			0.02, 4, rl.Color{R: 0, G: 0, B: 0, A: 60})
		rl.DrawRectangleRounded(
			rl.Rectangle{X: float32(x), Y: float32(y), Width: float32(w), Height: float32(h)},
			0.02, 4, rl.Color{R: 20, G: 20, B: 32, A: 245})
	} else {
		rl.DrawRectangle(x, y, w, h, rl.Color{R: 20, G: 20, B: 32, A: 255})
	}
}

// --- Character Preview ---

// CharPreviewData holds the data needed to render a 3D character preview.
// Can be populated from Player, Entity, CharInfo, or inspect data.
type CharPreviewData struct {
	ModelID         uint32
	Race            byte
	Scale           float32
	PrimaryWeapon   uint32
	SecondaryWeapon uint32
	Shield          uint32
	// Equipment appearance
	Chest, Bracer, Gloves, Legs, Boots, Helm, Robe byte
	ChestTint, BracerTint, GlovesTint, LegsTint, BootsTint, HelmTint, RobeTint byte
	HairColor, HairLength, HairStyle, FaceOption byte
}

// CharPreviewFromPlayer creates preview data from the current player.
func CharPreviewFromPlayer(p *game.Entity) CharPreviewData {
	return CharPreviewData{
		ModelID: p.ModelID, Race: p.Race, Scale: 1.0,
		PrimaryWeapon: p.PrimaryWeapon, SecondaryWeapon: p.SecondaryWeapon, Shield: p.Shield,
		Chest: p.Chest, Bracer: p.Bracer, Gloves: p.Gloves,
		Legs: p.Legs, Boots: p.Boots, Helm: p.Helm, Robe: p.Robe,
		ChestTint: p.ChestTint, BracerTint: p.BracerTint, GlovesTint: p.GlovesTint,
		LegsTint: p.LegsTint, BootsTint: p.BootsTint, HelmTint: p.HelmTint, RobeTint: p.RobeTint,
		HairColor: p.HairColor, HairLength: p.HairLength, HairStyle: p.HairStyle, FaceOption: p.FaceOption,
	}
}

// CharPreviewFromEntity creates preview data from a nearby entity.
func CharPreviewFromEntity(e *game.Entity) CharPreviewData {
	return CharPreviewData{
		ModelID: e.ModelID, Race: e.Race, Scale: e.Scale,
		PrimaryWeapon: e.PrimaryWeapon, SecondaryWeapon: e.SecondaryWeapon, Shield: e.Shield,
		Chest: e.Chest, Bracer: e.Bracer, Gloves: e.Gloves,
		Legs: e.Legs, Boots: e.Boots, Helm: e.Helm, Robe: e.Robe,
		ChestTint: e.ChestTint, BracerTint: e.BracerTint, GlovesTint: e.GlovesTint,
		LegsTint: e.LegsTint, BootsTint: e.BootsTint, HelmTint: e.HelmTint, RobeTint: e.RobeTint,
		HairColor: e.HairColor, HairLength: e.HairLength, HairStyle: e.HairStyle, FaceOption: e.FaceOption,
	}
}

// ToEquipmentState converts CharPreviewData to EquipmentState for the model renderer.
func (d *CharPreviewData) ToEquipmentState() *models.EquipmentState {
	return &models.EquipmentState{
		Chest: d.Chest, Bracer: d.Bracer, Gloves: d.Gloves,
		Legs: d.Legs, Boots: d.Boots, Helm: d.Helm, Robe: d.Robe,
		ChestTint: d.ChestTint, BracerTint: d.BracerTint, GlovesTint: d.GlovesTint,
		LegsTint: d.LegsTint, BootsTint: d.BootsTint, HelmTint: d.HelmTint, RobeTint: d.RobeTint,
		HairColor: d.HairColor, HairLength: d.HairLength, HairStyle: d.HairStyle, FaceOption: d.FaceOption,
		Race: d.Race,
	}
}

// uiCharPreview renders a slowly-rotating 3D character model into a render texture
// and draws it at the given screen position. Works with any CharPreviewData source.
func (c *Client) uiCharPreview(x, y, w, h int32, data CharPreviewData, rotation *float32) {
	if c.modelMgr == nil || data.ModelID == 0 {
		return
	}

	// Allocate render texture once
	if !c.equipPreviewInit {
		c.equipPreviewTex = rl.LoadRenderTexture(equipPreviewW, equipPreviewH)
		c.equipPreviewInit = true
	}

	// Auto-rotate
	dt := rl.GetFrameTime()
	*rotation += dt * 0.5

	// Equipment state
	equip := data.ToEquipmentState()

	// Per-race camera
	type raceCam struct {
		dist, eyeY, targetY float32
	}
	raceCams := [10]raceCam{
		{4.5, 2.0, 1.1}, {4.5, 2.0, 1.1}, {4.5, 2.0, 1.1}, // HUM, ELF, DELF
		{4.0, 1.6, 0.8}, {4.0, 1.6, 0.8},                   // GNO, DWF
		{5.5, 2.8, 1.5}, {5.0, 2.4, 1.3},                   // TRL, BAR
		{4.0, 1.6, 0.8}, {4.5, 2.0, 1.1}, {5.5, 2.8, 1.5}, // HLF, ERU, OGR
	}
	rc := raceCams[0]
	if int(data.Race) < len(raceCams) {
		rc = raceCams[data.Race]
	}

	facing := float32(math.Mod(float64(*rotation), 2*math.Pi))
	cosF := float32(math.Cos(float64(facing)))
	sinF := float32(math.Sin(float64(facing)))
	yOffset := rc.targetY * -0.2

	cam := rl.Camera3D{
		Position:   rl.Vector3{X: sinF * rc.dist, Y: rc.eyeY + yOffset, Z: cosF * rc.dist},
		Target:     rl.Vector3{X: 0, Y: rc.targetY + yOffset, Z: 0},
		Up:         rl.Vector3{X: 0, Y: 1, Z: 0},
		Fovy:       30,
		Projection: rl.CameraPerspective,
	}

	rl.BeginTextureMode(c.equipPreviewTex)
	rl.ClearBackground(rl.Color{R: 0, G: 0, B: 0, A: 0})
	gfx.ClearDepth()
	rl.BeginMode3D(cam)

	entityID := uint32(0xFFFD0000)
	scale := data.Scale
	if scale == 0 {
		scale = 1.0
	}
	modelID := int32(data.ModelID)
	c.modelMgr.DrawEntity(modelID, 0, 0, 0, 0, scale, equip, entityID, 0, 0, dt, false, 1.0)

	if c.modelMgr.ModelIsHumanoid(modelID) {
		c.modelMgr.DrawWeapon(int32(data.PrimaryWeapon), modelID, 0, 0, 0, 0, models.WeaponSlotPrimary)
		c.modelMgr.DrawWeapon(int32(data.SecondaryWeapon), modelID, 0, 0, 0, 0, models.WeaponSlotSecondary)
		c.modelMgr.DrawWeapon(int32(data.Shield), modelID, 0, 0, 0, 0, models.WeaponSlotShield)
	}

	rl.EndMode3D()
	rl.EndTextureMode()

	src := rl.Rectangle{Width: float32(equipPreviewW), Height: -float32(equipPreviewH)}
	dst := rl.Rectangle{X: float32(x), Y: float32(y), Width: float32(w), Height: float32(h)}
	rl.DrawTexturePro(c.equipPreviewTex.Texture, src, dst, rl.Vector2{}, 0, rl.White)
}

// =============================================================================
// Tab Bar
// =============================================================================

// uiTabBar draws a row of clickable sub-tab labels with underline on active.
// Returns the new Y position after the tab bar.
func (c *Client) uiTabBar(x, y int32, labels []string, activeTab *int) int32 {
	tabH := int32(22)
	tx := x
	mx, my := rl.GetMouseX(), rl.GetMouseY()
	for i, label := range labels {
		tw := c.measureText(label, 13) + 16
		clr := colorGoldDim
		if i == *activeTab {
			clr = colorGold
			rl.DrawRectangle(tx, y+tabH-2, tw, 2, colorGold)
		}
		if rl.IsMouseButtonPressed(rl.MouseButtonLeft) && mx >= tx && mx < tx+tw && my >= y && my < y+tabH {
			*activeTab = i
		}
		c.drawText(label, tx+8, y+4, 13, clr)
		tx += tw + 4
	}
	return y + tabH + 4
}

// =============================================================================
// Checkbox Group
// =============================================================================

// uiCheckboxGroup draws a grid of checkboxes. cols = number of columns.
// Returns Y position after the group.
func (c *Client) uiCheckboxGroup(x, y int32, labels []string, checked []bool, cols int) int32 {
	if len(labels) != len(checked) {
		return y
	}
	colW := int32(80)
	rowH := int32(18)
	for i, label := range labels {
		col := i % cols
		row := i / cols
		cx := x + int32(col)*colW
		cy := y + int32(row)*rowH
		c.uiCheckbox(cx, cy, label, &checked[i])
	}
	rows := (len(labels) + cols - 1) / cols
	return y + int32(rows)*rowH + 4
}

// uiCheckboxGroupBitmask draws checkboxes backed by a uint64 bitmask.
// Each label index corresponds to a bit. Returns Y after group.
func (c *Client) uiCheckboxGroupBitmask(x, y int32, labels []string, mask *uint64, cols int) int32 {
	colW := int32(80)
	rowH := int32(18)
	for i, label := range labels {
		col := i % cols
		row := i / cols
		cx := x + int32(col)*colW
		cy := y + int32(row)*rowH

		checked := *mask&(1<<uint(i)) != 0
		if c.uiCheckbox(cx, cy, label, &checked) {
			if checked {
				*mask |= 1 << uint(i)
			} else {
				*mask &^= 1 << uint(i)
			}
		}
	}
	rows := (len(labels) + cols - 1) / cols
	return y + int32(rows)*rowH + 4
}

// =============================================================================
// Item Tooltip
// =============================================================================

// tooltipStatNames maps stat indices to display names for item tooltips.
// (statNames is already defined in charcreate.go for the stat panel)
var tooltipStatNames = map[int]string{
	0: "STR", 1: "STA", 2: "AGI", 3: "DEX", 4: "WIS", 5: "INT", 6: "CHA",
	7: "HP", 8: "HP Max", 9: "Power", 10: "Power Max",
	11: "PoT", 12: "HoT", 13: "AC",
	14: "AC Total", 23: "Poison Resist", 24: "Disease Resist",
	25: "Fire Resist", 26: "Cold Resist", 27: "Lightning Resist", 28: "Arcane Resist",
}

// Slot name lookup.
var slotNames = map[int64]string{
	0: "Helm", 1: "Gloves", 2: "Earring", 3: "Earring",
	4: "Necklace", 5: "Chest", 7: "Bracers", 8: "Ring",
	9: "Ring", 10: "Belt", 11: "Boots", 12: "Primary",
	13: "Shield", 14: "Secondary", 15: "Two-Hand",
	17: "Face", 19: "Gloves", 20: "Cloak", 21: "Robe",
}

// Attack type names.
var atkTypeNames = map[int64]string{
	1: "1H Slash", 2: "2H Slash", 3: "1H Blunt", 4: "2H Blunt",
	5: "Hand-to-Hand", 6: "Pierce", 7: "Bow", 8: "Throwing",
	9: "Crossbow", 10: "Focus", 11: "Dagger",
}

// uiItemTooltip draws a floating tooltip popup for an item at the given position.
// Typically called when hovering over an item row in inventory/auction.
func (c *Client) uiItemTooltip(item *game.ParsedItem, screenX, screenY int32) {
	if item == nil || item.Name == "" {
		return
	}

	// Check for item icon
	hasIcon := false
	var iconTex rl.Texture2D
	if item.ItemIcon != 0 {
		iconTex, hasIcon = c.getItemIcon(item.ItemIcon)
	}
	iconSize := int32(40)

	// Build tooltip lines
	lines := []struct {
		text  string
		color rl.Color
	}{
		{item.Name, colorGold},
	}

	// Slot + level
	slotStr := ""
	if sn, ok := slotNames[item.Slot]; ok {
		slotStr = sn
	}
	if item.LevelReq > 0 {
		if slotStr != "" {
			slotStr += fmt.Sprintf("  Lv %d", item.LevelReq)
		} else {
			slotStr = fmt.Sprintf("Level %d", item.LevelReq)
		}
	}
	if slotStr != "" {
		lines = append(lines, struct {
			text  string
			color rl.Color
		}{slotStr, colorDim})
	}

	// Weapon info
	if item.WeaponDamage > 0 {
		atkName := ""
		if an, ok := atkTypeNames[item.AttackType]; ok {
			atkName = an + "  "
		}
		lines = append(lines, struct {
			text  string
			color rl.Color
		}{fmt.Sprintf("%sDmg: %d", atkName, item.WeaponDamage), colorWhite})
	}

	// Stats
	if len(item.Stats) > 0 {
		lines = append(lines, struct {
			text  string
			color rl.Color
		}{"", colorDim}) // spacer
		// Sort stat keys for stable display order
		statKeys := make([]int, 0, len(item.Stats))
		for k := range item.Stats {
			statKeys = append(statKeys, k)
		}
		sort.Ints(statKeys)
		for _, idx := range statKeys {
			val := item.Stats[idx]
			name := fmt.Sprintf("Stat %d", idx)
			if sn, ok := tooltipStatNames[idx]; ok {
				name = sn
			}
			statColor := rl.Color{R: 100, G: 200, B: 100, A: 255}
			if val < 0 {
				statColor = rl.Color{R: 200, G: 100, B: 100, A: 255}
			}
			lines = append(lines, struct {
				text  string
				color rl.Color
			}{fmt.Sprintf("%s: %+d", name, val), statColor})
		}
	}

	// Description
	if item.Desc != "" {
		lines = append(lines, struct {
			text  string
			color rl.Color
		}{"", colorDim})
		// Word-wrap description at ~35 chars
		desc := item.Desc
		for len(desc) > 35 {
			cut := 35
			for cut > 20 && desc[cut] != ' ' {
				cut--
			}
			if cut <= 20 {
				cut = 35
			}
			lines = append(lines, struct {
				text  string
				color rl.Color
			}{desc[:cut], rl.Color{R: 170, G: 170, B: 140, A: 255}})
			desc = desc[cut:]
			if len(desc) > 0 && desc[0] == ' ' {
				desc = desc[1:]
			}
		}
		if desc != "" {
			lines = append(lines, struct {
				text  string
				color rl.Color
			}{desc, rl.Color{R: 170, G: 170, B: 140, A: 255}})
		}
	}

	// Cost
	if item.ItemCost > 0 {
		lines = append(lines, struct {
			text  string
			color rl.Color
		}{fmt.Sprintf("Value: %s", formatTunar(item.ItemCost)), colorDim})
	}

	// Compute tooltip dimensions
	lineH := int32(15)
	padX := int32(8)
	padY := int32(6)
	iconPad := int32(0)
	if hasIcon {
		iconPad = iconSize + 6
	}
	maxW := int32(0)
	for _, l := range lines {
		tw := c.measureText(l.text, 11)
		if tw > maxW {
			maxW = tw
		}
	}
	ttW := maxW + padX*2 + iconPad
	ttH := int32(len(lines))*lineH + padY*2
	if hasIcon && ttH < iconSize+padY*2 {
		ttH = iconSize + padY*2
	}

	// Position: prefer right of cursor, clamp to screen
	tx := screenX + 16
	ty := screenY
	if tx+ttW > screenWidth-10 {
		tx = screenX - ttW - 8
	}
	if ty+ttH > screenHeight-10 {
		ty = screenHeight - ttH - 10
	}

	// Draw tooltip background + border
	if c.uiTheme == uiStyleModern {
		// Drop shadow + rounded
		rect := rl.Rectangle{X: float32(tx), Y: float32(ty), Width: float32(ttW), Height: float32(ttH)}
		shadowRect := rl.Rectangle{X: float32(tx + 3), Y: float32(ty + 3), Width: float32(ttW), Height: float32(ttH)}
		rl.DrawRectangleRounded(shadowRect, 0.04, 4, rl.Color{R: 0, G: 0, B: 0, A: 80})
		rl.DrawRectangleRounded(rect, 0.04, 4, rl.Color{R: 15, G: 15, B: 22, A: 250})
		rl.DrawRectangleRoundedLines(rect, 0.04, 4, rl.Color{R: 80, G: 70, B: 50, A: 255})
	} else {
		rl.DrawRectangle(tx-1, ty-1, ttW+2, ttH+2, rl.Color{R: 60, G: 55, B: 40, A: 255})
		rl.DrawRectangle(tx, ty, ttW, ttH, rl.Color{R: 15, G: 15, B: 22, A: 245})
	}

	// Draw icon (top-left corner)
	textOffsetX := int32(0)
	if hasIcon {
		rl.DrawTexturePro(iconTex,
			rl.Rectangle{Width: float32(iconTex.Width), Height: float32(iconTex.Height)},
			rl.Rectangle{X: float32(tx + padX), Y: float32(ty + padY), Width: float32(iconSize), Height: float32(iconSize)},
			rl.Vector2{}, 0, rl.White)
		textOffsetX = iconPad
	}

	// Draw lines
	ly := ty + padY
	for _, l := range lines {
		if l.text != "" {
			c.drawText(l.text, tx+padX+textOffsetX, ly, 11, l.color)
		}
		ly += lineH
		// After first 3 lines (name + slot + weapon), text goes full width
		if ly > ty+padY+3*lineH {
			textOffsetX = 0
		}
	}
}

// --- Radio Group ---

// uiRadioGroup draws a horizontal row of labeled options. Returns true if selection changed.
// selected is a pointer to the current index (0-based). Labels are drawn side-by-side.
func (c *Client) uiRadioGroup(x, y int32, label string, labels []string, selected *int) bool {
	mx, my := rl.GetMouseX(), rl.GetMouseY()
	clicked := rl.IsMouseButtonPressed(rl.MouseButtonLeft)
	changed := false

	// Section label
	if label != "" {
		c.drawText(label, x, y, 14, colorGold)
		y += 18
	}

	btnH := int32(20)
	bx := x
	for i, lbl := range labels {
		btnW := c.measureText(lbl, 12) + 12
		isCurrent := *selected == i
		hover := mx >= bx && mx < bx+btnW && my >= y && my < y+btnH

		if isCurrent {
			rl.DrawRectangle(bx, y, btnW, btnH, colorRadioSelBg)
			c.drawText(lbl, bx+6, y+3, 12, colorGold)
		} else if hover {
			rl.DrawRectangle(bx, y, btnW, btnH, colorRowHover)
			c.drawText(lbl, bx+6, y+3, 12, colorWhite)
		} else {
			c.drawText(lbl, bx+6, y+3, 12, colorDim)
		}

		if hover && clicked && !isCurrent {
			*selected = i
			changed = true
		}
		bx += btnW + 4
	}

	return changed
}

// uiRadioGroupY is like uiRadioGroup but returns the Y position after the widget
// for easy vertical stacking. The changed bool is returned via pointer.
func (c *Client) uiRadioGroupY(x, y int32, label string, labels []string, selected *int) int32 {
	c.uiRadioGroup(x, y, label, labels, selected)
	h := int32(20)
	if label != "" {
		h += 18
	}
	return y + h + 8
}

// --- Hover Row ---

// uiHoverRow draws a clickable text row with hover highlight. Returns true if clicked.
func (c *Client) uiHoverRow(x, y, w, h int32, text string, fontSize int32) bool {
	mx, my := rl.GetMouseX(), rl.GetMouseY()
	hover := mx >= x && mx < x+w && my >= y && my < y+h
	clr := colorWhite
	if hover {
		rl.DrawRectangle(x, y, w, h, colorRowHover)
		clr = colorGoldHover
	}
	c.drawText(text, x+8, y+(h-fontSize)/2, fontSize, clr)
	return hover && rl.IsMouseButtonPressed(rl.MouseButtonLeft)
}

// uiHoverRowSelected is like uiHoverRow but highlights if selected.
func (c *Client) uiHoverRowSelected(x, y, w, h int32, text string, fontSize int32, selected bool) bool {
	mx, my := rl.GetMouseX(), rl.GetMouseY()
	hover := mx >= x && mx < x+w && my >= y && my < y+h
	clr := colorWhite
	if selected {
		clr = colorGold
	}
	if hover {
		rl.DrawRectangle(x, y, w, h, colorRowHover)
		if !selected {
			clr = colorGoldHover
		}
	}
	c.drawText(text, x+8, y+(h-fontSize)/2, fontSize, clr)
	return hover && rl.IsMouseButtonPressed(rl.MouseButtonLeft)
}

// --- Screen Utility Buttons ---

// uiMuteButton draws a small mute toggle button at the given position.
func (c *Client) uiMuteButton(x, y int32) {
	sz := int32(24)
	mx, my := rl.GetMouseX(), rl.GetMouseY()
	hover := mx >= x && mx < x+sz && my >= y && my < y+sz

	bg := rl.Color{R: 20, G: 20, B: 30, A: 180}
	if hover {
		bg = rl.Color{R: 40, G: 40, B: 60, A: 220}
	}
	rl.DrawRectangle(x, y, sz, sz, bg)

	muted := c.masterVolume == 0

	// Try to draw bard spell icon (anthem); fall back to text
	iconDrawn := false
	if tex, ok := c.getSpellIcon(202); ok {
		src := rl.Rectangle{Width: float32(tex.Width), Height: float32(tex.Height)}
		dst := rl.Rectangle{X: float32(x + 2), Y: float32(y + 2), Width: float32(sz - 4), Height: float32(sz - 4)}
		tint := rl.White
		if muted {
			tint = rl.Color{R: 255, G: 80, B: 80, A: 200}
		}
		rl.DrawTexturePro(tex, src, dst, rl.Vector2{}, 0, tint)
		iconDrawn = true
	}
	if !iconDrawn {
		if muted {
			c.drawText("M", x+6, y+4, 14, rl.Color{R: 200, G: 80, B: 80, A: 255})
		} else {
			clr := colorDim
			if hover {
				clr = colorWhite
			}
			c.drawText("S", x+6, y+4, 14, clr)
		}
	}
	if muted && iconDrawn {
		// Draw X over icon when muted
		rl.DrawLine(x+4, y+4, x+sz-4, y+sz-4, rl.Color{R: 255, G: 60, B: 60, A: 255})
		rl.DrawLine(x+4, y+sz-4, x+sz-4, y+4, rl.Color{R: 255, G: 60, B: 60, A: 255})
	}

	if hover && rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
		if c.masterVolume > 0 {
			c.prevMasterVolume = c.masterVolume
			c.masterVolume = 0
			rl.SetMasterVolume(0)
		} else {
			if c.prevMasterVolume <= 0 {
				c.prevMasterVolume = 1.0
			}
			c.masterVolume = c.prevMasterVolume
			rl.SetMasterVolume(c.masterVolume)
		}
		cfg := loadLoginConfig()
		cfg.MasterVolume = c.masterVolume
		saveLoginConfig(cfg)
	}
}

// uiBackArrow draws a back arrow button at the given position. Returns true if clicked.
func (c *Client) uiBackArrow(x, y int32) bool {
	sz := int32(28)
	mx, my := rl.GetMouseX(), rl.GetMouseY()
	hover := mx >= x && mx < x+sz && my >= y && my < y+sz

	clr := colorDim
	if hover {
		clr = colorGoldHover
	}
	c.drawText("<", x+8, y+5, 16, clr)

	return hover && rl.IsMouseButtonPressed(rl.MouseButtonLeft)
}
