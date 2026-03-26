package main

import (
	"fmt"
	"math"

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
// =============================================================================

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
	bg := rl.Color{R: 30, G: 30, B: 50, A: 255}
	if hover {
		bg = rl.Color{R: 40, G: 40, B: 70, A: 255}
	}
	rl.DrawRectangle(x, y, w, h, bg)
	rl.DrawRectangleLines(x, y, w, h, rl.Color{R: 60, G: 60, B: 80, A: 255})
	tw := c.measureText(label, fontSize)
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
	rl.DrawRectangle(x, y, w, h, rl.Color{R: 25, G: 25, B: 40, A: 255})
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
	rl.DrawRectangle(x, y, w, h, rl.Color{R: 20, G: 20, B: 32, A: 255})
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
