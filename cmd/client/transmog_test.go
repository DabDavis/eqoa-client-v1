package main

import (
	"testing"

	"github.com/eqoa/client/internal/models"
)

func TestTransmogDisabledIsNoOp(t *testing.T) {
	tm := defaultTransmog() // Enabled defaults to false
	equip := &models.EquipmentState{Chest: 3, Legs: 3, ChestTint: 7, Robe: 1}
	before := *equip
	tm.apply(equip)
	if *equip != before {
		t.Errorf("disabled transmog mutated equipment: %+v -> %+v", before, *equip)
	}
}

func TestTransmogOverridesAllArmorSlots(t *testing.T) {
	tm := defaultTransmog()
	tm.Enabled = true

	equip := &models.EquipmentState{Chest: 1, Bracer: 2, Gloves: 3, Legs: 4, Boots: 5}
	tm.apply(equip)

	set := models.ArmorSetOrnatePlate
	if equip.Chest != set || equip.Bracer != set || equip.Gloves != set ||
		equip.Legs != set || equip.Boots != set {
		t.Errorf("not all slots overridden: %+v", *equip)
	}
	if !equip.HideRobe {
		t.Error("HideRobe not set; a robe would cover the transmogged chest and legs")
	}
}

// Tints are left alone unless explicitly overridden, so a character keeps the
// per-slot colours the server sent.
func TestTransmogKeepsServerTintsByDefault(t *testing.T) {
	tm := defaultTransmog()
	tm.Enabled = true

	equip := &models.EquipmentState{ChestTint: 8, LegsTint: 5}
	tm.apply(equip)
	if equip.ChestTint != 8 || equip.LegsTint != 5 {
		t.Errorf("tints changed without TintSet: chest=%d legs=%d", equip.ChestTint, equip.LegsTint)
	}

	tm.TintSet = true
	tm.Tint = 6
	tm.apply(equip)
	if equip.ChestTint != 6 || equip.LegsTint != 6 || equip.BootsTint != 6 {
		t.Errorf("TintSet did not apply: %+v", *equip)
	}
}

func TestTransmogSetNameInRange(t *testing.T) {
	tm := transmogState{Set: models.ArmorSetOrnatePlate}
	if got := tm.setName(); got != "Ornate Plate" {
		t.Errorf("setName() = %q, want %q", got, "Ornate Plate")
	}
	tm.Set = 200
	if got := tm.setName(); got != "Unknown" {
		t.Errorf("out-of-range setName() = %q, want %q", got, "Unknown")
	}
}

// A per-slot pick must reach the render path as a CHAR.ESF override.
func TestTransmogAppliesPerSlotTexture(t *testing.T) {
	avail := models.NPCArmorForSlot(models.TextSlotChest, 0, 0)
	if len(avail) == 0 {
		t.Skip("no chest art for race 0 sex 0")
	}
	tm := defaultTransmog()
	tm.Enabled = true
	tm.SlotTex[models.TextSlotChest] = avail[0].DictID

	equip := &models.EquipmentState{Race: 0, Sex: 0}
	tm.apply(equip)
	if equip.TexOverride[models.TextSlotChest] != avail[0].DictID {
		t.Errorf("chest override = %d, want %d",
			equip.TexOverride[models.TextSlotChest], avail[0].DictID)
	}
	// Slots without a pick stay on the set.
	if equip.TexOverride[models.TextSlotLegs] != 0 {
		t.Errorf("legs override = %d, want 0", equip.TexOverride[models.TextSlotLegs])
	}
}

// The sex split means art picked on one character is wrong on another. apply()
// must drop a selection the rendered body cannot wear rather than show garbage.
func TestTransmogRejectsArtForWrongBody(t *testing.T) {
	maleChest := models.NPCArmorForSlot(models.TextSlotChest, 0, 0)
	if len(maleChest) == 0 {
		t.Skip("no male chest art")
	}
	tm := defaultTransmog()
	tm.Enabled = true
	tm.SlotTex[models.TextSlotChest] = maleChest[0].DictID

	// Same selection, female body.
	equip := &models.EquipmentState{Race: 0, Sex: 1}
	tm.apply(equip)
	if equip.TexOverride[models.TextSlotChest] != 0 {
		t.Errorf("male chest art survived onto a female body: %d",
			equip.TexOverride[models.TextSlotChest])
	}
}

// An unknown DictID (stale saved state, hand-edited config) must not pass through.
func TestTransmogRejectsUnknownDictID(t *testing.T) {
	tm := defaultTransmog()
	tm.Enabled = true
	tm.SlotTex[models.TextSlotBoots] = 424242

	equip := &models.EquipmentState{}
	tm.apply(equip)
	if equip.TexOverride[models.TextSlotBoots] != 0 {
		t.Errorf("unknown DictID passed through: %d", equip.TexOverride[models.TextSlotBoots])
	}
}

// Clearing picks must clear the overrides on the next apply. The render path
// rebuilds EquipmentState each frame, but apply() zeroes unpicked slots anyway so
// a stale override can never survive a deselect.
func TestClearSlotTexRemovesOverride(t *testing.T) {
	avail := models.NPCArmorForSlot(models.TextSlotChest, 0, 0)
	if len(avail) == 0 {
		t.Skip("no chest art")
	}
	tm := defaultTransmog()
	tm.Enabled = true
	tm.SlotTex[models.TextSlotChest] = avail[0].DictID

	equip := &models.EquipmentState{Race: 0, Sex: 0}
	tm.apply(equip)
	if equip.TexOverride[models.TextSlotChest] == 0 {
		t.Fatal("override was not applied")
	}

	tm.clearSlotTex()
	tm.apply(equip)
	if equip.TexOverride[models.TextSlotChest] != 0 {
		t.Errorf("clearSlotTex left an override: %d", equip.TexOverride[models.TextSlotChest])
	}
}

// cycleSlotTex walks the available art with "no override" as the entry before
// the first, and wraps both ways so two buttons can drive it.
func TestCycleSlotTexWraps(t *testing.T) {
	avail := models.NPCArmorForSlot(models.TextSlotChest, 0, 0)
	if len(avail) < 2 {
		t.Skip("need at least two chest textures")
	}
	var tm transmogState

	tm.cycleSlotTex(models.TextSlotChest, 0, 0, 1)
	if tm.SlotTex[models.TextSlotChest] != avail[0].DictID {
		t.Errorf("first forward step = %d, want %d",
			tm.SlotTex[models.TextSlotChest], avail[0].DictID)
	}

	// Forward through every entry then once more lands back on "no override".
	for i := 1; i < len(avail); i++ {
		tm.cycleSlotTex(models.TextSlotChest, 0, 0, 1)
	}
	tm.cycleSlotTex(models.TextSlotChest, 0, 0, 1)
	if tm.SlotTex[models.TextSlotChest] != 0 {
		t.Errorf("wrap forward = %d, want 0", tm.SlotTex[models.TextSlotChest])
	}

	// Backward from "no override" lands on the last entry.
	tm.cycleSlotTex(models.TextSlotChest, 0, 0, -1)
	if want := avail[len(avail)-1].DictID; tm.SlotTex[models.TextSlotChest] != want {
		t.Errorf("wrap backward = %d, want %d", tm.SlotTex[models.TextSlotChest], want)
	}
}

// Cycling on a body with no compatible art must clear rather than select.
func TestCycleSlotTexNoAvailability(t *testing.T) {
	var tm transmogState
	tm.SlotTex[models.TextSlotChest] = 999
	tm.cycleSlotTex(models.TextSlotChest, 250, 0, 1) // no such body
	if tm.SlotTex[models.TextSlotChest] != 0 {
		t.Errorf("got %d, want 0", tm.SlotTex[models.TextSlotChest])
	}
}

func TestSlotTexNameFallsBackToSet(t *testing.T) {
	tm := defaultTransmog()
	if got := tm.slotTexName(models.TextSlotChest); got != "Set: Ornate Plate" {
		t.Errorf("slotTexName = %q, want %q", got, "Set: Ornate Plate")
	}
	avail := models.NPCArmorForSlot(models.TextSlotChest, 0, 0)
	if len(avail) == 0 {
		return
	}
	tm.SlotTex[models.TextSlotChest] = avail[0].DictID
	if got := tm.slotTexName(models.TextSlotChest); got == "" || got == "Unknown" {
		t.Errorf("slotTexName = %q, want a label", got)
	}
}
