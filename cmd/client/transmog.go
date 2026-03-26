package main

import (
	"fmt"
	"sort"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/eqoa/client/internal/models"
)

var armorSetNames = [9]string{
	"None", "Cloth", "Leather", "Chain", "Plate",
	"Padded", "Banded", "Scale", "Monk",
}

var tintNames = [14]string{
	"White", "DarkGray", "Cyan", "Brown", "Navy", "Green", "Gold",
	"Olive", "Red", "IceBlue", "Silver", "Tan", "Teal", "Purple",
}

// knownWeaponModels collects unique weapon DictIDs seen from entities.
// Populated lazily on first transmog panel open.
var knownWeaponModels []uint32
var knownShieldModels []uint32
var weaponModelsLoaded bool

func (c *Client) loadWeaponModels() {
	if weaponModelsLoaded {
		return
	}
	weaponModelsLoaded = true

	gs := c.engine.State
	seen := make(map[uint32]bool)
	seenShield := make(map[uint32]bool)

	// Collect from player's own weapons
	if gs.Player.PrimaryWeapon != 0 {
		seen[gs.Player.PrimaryWeapon] = true
	}
	if gs.Player.SecondaryWeapon != 0 {
		seen[gs.Player.SecondaryWeapon] = true
	}
	if gs.Player.Shield != 0 {
		seenShield[gs.Player.Shield] = true
	}

	// Collect from visible entities
	for _, ent := range gs.Entities {
		if ent.PrimaryWeapon != 0 {
			seen[ent.PrimaryWeapon] = true
		}
		if ent.SecondaryWeapon != 0 {
			seen[ent.SecondaryWeapon] = true
		}
		if ent.Shield != 0 {
			seenShield[ent.Shield] = true
		}
	}

	for id := range seen {
		knownWeaponModels = append(knownWeaponModels, id)
	}
	for id := range seenShield {
		knownShieldModels = append(knownShieldModels, id)
	}
	sort.Slice(knownWeaponModels, func(i, j int) bool { return knownWeaponModels[i] < knownWeaponModels[j] })
	sort.Slice(knownShieldModels, func(i, j int) bool { return knownShieldModels[i] < knownShieldModels[j] })
}

// applyTransmog applies client-side appearance overrides to an equipment state.
func (c *Client) applyTransmog(equip *models.EquipmentState) {
	if c.transmogChest >= 0 {
		equip.Chest = byte(c.transmogChest)
	}
	if c.transmogBracer >= 0 {
		equip.Bracer = byte(c.transmogBracer)
	}
	if c.transmogGloves >= 0 {
		equip.Gloves = byte(c.transmogGloves)
	}
	if c.transmogLegs >= 0 {
		equip.Legs = byte(c.transmogLegs)
	}
	if c.transmogBoots >= 0 {
		equip.Boots = byte(c.transmogBoots)
	}
	if c.transmogHelm >= 0 {
		if c.transmogHelm == 0 {
			equip.Helm = 255
			equip.HideHelm = true
		} else {
			equip.Helm = byte(c.transmogHelm)
		}
	}
	if c.hideHelm {
		equip.Helm = 255
		equip.HideHelm = true
	}
	if c.transmogRobe >= 0 {
		if c.transmogRobe == 0 {
			equip.Robe = 255
		} else {
			equip.Robe = byte(c.transmogRobe - 1)
		}
	}
	if c.hideRobe {
		equip.Robe = 255
	}
	// Tint overrides
	if c.transmogChestTint >= 0 {
		equip.ChestTint = byte(c.transmogChestTint)
	}
	if c.transmogBracerTint >= 0 {
		equip.BracerTint = byte(c.transmogBracerTint)
	}
	if c.transmogGlovesTint >= 0 {
		equip.GlovesTint = byte(c.transmogGlovesTint)
	}
	if c.transmogLegsTint >= 0 {
		equip.LegsTint = byte(c.transmogLegsTint)
	}
	if c.transmogBootsTint >= 0 {
		equip.BootsTint = byte(c.transmogBootsTint)
	}
	if c.transmogHelmTint >= 0 {
		equip.HelmTint = byte(c.transmogHelmTint)
	}
	if c.transmogRobeTint >= 0 {
		equip.RobeTint = byte(c.transmogRobeTint)
	}
}

// applyTransmogWeapons returns overridden weapon DictIDs.
func (c *Client) applyTransmogWeapons(primary, secondary, shield uint32) (uint32, uint32, uint32) {
	if c.transmogPrimary >= 0 && c.transmogPrimary < len(knownWeaponModels) {
		primary = knownWeaponModels[c.transmogPrimary]
	}
	if c.transmogSecondary >= 0 && c.transmogSecondary < len(knownWeaponModels) {
		secondary = knownWeaponModels[c.transmogSecondary]
	}
	if c.transmogShield >= 0 && c.transmogShield < len(knownShieldModels) {
		shield = knownShieldModels[c.transmogShield]
	}
	return primary, secondary, shield
}

type transmogSlotInfo struct {
	label string
	val   *int
	max   int
	names func(v int) string
}

func (c *Client) transmogSlots() []transmogSlotInfo {
	armorName := func(v int) string {
		if v < 0 {
			return "Actual"
		}
		if v < len(armorSetNames) {
			return armorSetNames[v]
		}
		return fmt.Sprintf("Set %d", v)
	}
	helmName := func(v int) string {
		if v < 0 {
			return "Actual"
		}
		if v == 0 {
			return "Hidden"
		}
		return fmt.Sprintf("Helm %d", v)
	}
	robeName := func(v int) string {
		if v < 0 {
			return "Actual"
		}
		if v == 0 {
			return "Hidden"
		}
		return fmt.Sprintf("Robe %d", v)
	}
	weaponName := func(v int) string {
		if v < 0 {
			return "Actual"
		}
		if v < len(knownWeaponModels) {
			return fmt.Sprintf("Wpn %d", v+1)
		}
		return "?"
	}
	shieldName := func(v int) string {
		if v < 0 {
			return "Actual"
		}
		if v < len(knownShieldModels) {
			return fmt.Sprintf("Shield %d", v+1)
		}
		return "?"
	}

	wpnMax := len(knownWeaponModels)
	if wpnMax == 0 {
		wpnMax = 1
	}
	shMax := len(knownShieldModels)
	if shMax == 0 {
		shMax = 1
	}

	tintName := func(v int) string {
		if v < 0 {
			return "Actual"
		}
		if v < len(tintNames) {
			return tintNames[v]
		}
		return fmt.Sprintf("%d", v)
	}

	return []transmogSlotInfo{
		{"Chest", &c.transmogChest, 9, armorName},
		{"  Tint", &c.transmogChestTint, 14, tintName},
		{"Bracer", &c.transmogBracer, 9, armorName},
		{"  Tint", &c.transmogBracerTint, 14, tintName},
		{"Gloves", &c.transmogGloves, 9, armorName},
		{"  Tint", &c.transmogGlovesTint, 14, tintName},
		{"Legs", &c.transmogLegs, 9, armorName},
		{"  Tint", &c.transmogLegsTint, 14, tintName},
		{"Boots", &c.transmogBoots, 9, armorName},
		{"  Tint", &c.transmogBootsTint, 14, tintName},
		{"Helm", &c.transmogHelm, 9, helmName},
		{"  Tint", &c.transmogHelmTint, 14, tintName},
		{"Robe", &c.transmogRobe, 5, robeName},
		{"  Tint", &c.transmogRobeTint, 14, tintName},
		{"Primary", &c.transmogPrimary, wpnMax, weaponName},
		{"Secondary", &c.transmogSecondary, wpnMax, weaponName},
		{"Shield", &c.transmogShield, shMax, shieldName},
	}
}

// drawTransmogPanel renders the full transmog controls in the right column.
func (c *Client) drawTransmogPanel(x, y, w, h int32, mx, my int32) {
	c.loadWeaponModels()

	slots := c.transmogSlots()
	rowH := int32(16)

	// Title
	titleColor := rl.Color{R: 220, G: 200, B: 120, A: 240}
	c.drawTextInRect("Transmog", x, y+2, w, 14, titleColor)

	listY := y + 22
	for i, slot := range slots {
		ry := listY + int32(i)*rowH
		if ry+rowH > y+h-20 {
			break
		}
		hover := mx >= x && mx < x+w && my >= ry && my < ry+rowH

		bg := rl.Color{R: 20, G: 20, B: 35, A: 255}
		if hover {
			bg = rl.Color{R: 40, G: 40, B: 60, A: 255}
		}
		rl.DrawRectangle(x, ry, w, rowH-1, bg)

		// Left arrow
		arrowColor := rl.Color{R: 100, G: 100, B: 130, A: 200}
		c.drawText("<", x+4, ry+2, 10, arrowColor)

		// Label
		labelColor := rl.Color{R: 170, G: 160, B: 130, A: 230}
		c.drawText(slot.label, x+18, ry+2, 10, labelColor)

		// Value
		name := slot.names(*slot.val)
		valColor := rl.Color{R: 140, G: 200, B: 140, A: 230}
		if *slot.val < 0 {
			valColor = rl.Color{R: 130, G: 130, B: 130, A: 180}
		}
		nameW := c.measureText(name, 12)
		c.drawText(name, x+w-nameW-18, ry+2, 10, valColor)

		// Right arrow
		c.drawText(">", x+w-14, ry+2, 10, arrowColor)

		// Click
		if hover && rl.IsMouseButtonPressed(rl.MouseLeftButton) {
			mid := x + w/2
			if mx < mid {
				*slot.val--
				if *slot.val < -1 {
					*slot.val = slot.max - 1
				}
			} else {
				*slot.val++
				if *slot.val >= slot.max {
					*slot.val = -1
				}
			}
		}
	}

	// Reset button at bottom
	resetY := y + h - 18
	resetHover := mx >= x && mx < x+w && my >= resetY && my < resetY+16
	resetBg := rl.Color{R: 40, G: 25, B: 25, A: 255}
	if resetHover {
		resetBg = rl.Color{R: 70, G: 40, B: 40, A: 255}
	}
	rl.DrawRectangle(x, resetY, w, 16, resetBg)
	resetW := c.measureText("Reset All", 11)
	c.drawText("Reset All", x+(w-resetW)/2, resetY+2, 11, rl.Color{R: 220, G: 140, B: 140, A: 230})
	if resetHover && rl.IsMouseButtonPressed(rl.MouseLeftButton) {
		c.transmogChest = -1
		c.transmogBracer = -1
		c.transmogGloves = -1
		c.transmogLegs = -1
		c.transmogBoots = -1
		c.transmogHelm = -1
		c.transmogRobe = -1
		c.transmogPrimary = -1
		c.transmogSecondary = -1
		c.transmogShield = -1
		c.transmogChestTint = -1
		c.transmogBracerTint = -1
		c.transmogGlovesTint = -1
		c.transmogLegsTint = -1
		c.transmogBootsTint = -1
		c.transmogHelmTint = -1
		c.transmogRobeTint = -1
		c.hideHelm = false
		c.hideRobe = false
	}
}
