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
