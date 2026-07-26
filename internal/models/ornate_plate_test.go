package models

import (
	"image"
	"os"
	"testing"

	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

const testISOPath = "/home/sdg/claude-eqoa/EverQuest - Online Adventures - Frontiers (USA).iso"

// The ornate plate set must reuse the plate DictIDs exactly — it is the same
// five surfaces, read from a different file.
func TestOrnatePlateReusesPlateDictIDs(t *testing.T) {
	for slot := TextSlotChest; slot <= TextSlotBoots; slot++ {
		plate, plateFromChar := GetArmorTexture(4, slot)
		ornate, ornateFromChar := GetArmorTexture(ArmorSetOrnatePlate, slot)
		if plate != ornate {
			t.Errorf("slot %d: ornate DictID %d != plate DictID %d", slot, ornate, plate)
		}
		if plateFromChar {
			t.Errorf("slot %d: plate must use the default CHARCUST-first order", slot)
		}
		if !ornateFromChar {
			t.Errorf("slot %d: ornate plate must be read from CHAR.ESF", slot)
		}
	}
}

// Out-of-range sets must not panic now that the table has a client-side row.
func TestGetArmorTextureBounds(t *testing.T) {
	for _, set := range []byte{ArmorSetCount, ArmorSetCount + 1, 255} {
		if id, fromChar := GetArmorTexture(set, TextSlotChest); id != 0 || fromChar {
			t.Errorf("set %d: got (%d, %v), want (0, false)", set, id, fromChar)
		}
	}
	for _, slot := range []int{-1, 5, 99} {
		if id, _ := GetArmorTexture(4, slot); id != 0 {
			t.Errorf("slot %d: got %d, want 0", slot, id)
		}
	}
}

// The draw path must ask for CHAR.ESF when the ornate set is equipped —
// otherwise the lookup finds CHARCUST first and renders ordinary plate.
func TestResolveSlotTextureUsesCharESFForOrnate(t *testing.T) {
	m := &ModelManager{}

	for slot := TextSlotChest; slot <= TextSlotBoots; slot++ {
		want, _ := GetArmorTexture(ArmorSetOrnatePlate, slot)

		equip := &EquipmentState{Robe: 0xFF}
		setSlot(equip, slot, ArmorSetOrnatePlate)
		got, src := m.resolveSlotTexture(equip, slot)
		if got != want || src != texSrcCharESF {
			t.Errorf("ornate slot %d: got (%d, %v), want (%d, %v)",
				slot, got, src, want, texSrcCharESF)
		}

		// Ordinary plate must keep the default search order.
		equip = &EquipmentState{Robe: 0xFF}
		setSlot(equip, slot, 4)
		if _, src := m.resolveSlotTexture(equip, slot); src != texSrcAuto {
			t.Errorf("plate slot %d: src = %v, want %v", slot, src, texSrcAuto)
		}
	}
}

// A robe covers chest and legs, so it must win over the transmog unless the
// override also hides it — otherwise enabling the ornate set does nothing.
func TestRobeOverridesArmorUnlessHidden(t *testing.T) {
	m := &ModelManager{}
	ornate, _ := GetArmorTexture(ArmorSetOrnatePlate, TextSlotChest)
	robe := GetRobeTextureDictID(0, TextSlotChest)
	if robe == 0 {
		t.Fatal("robe 0 has no chest texture; test needs revisiting")
	}

	equip := &EquipmentState{Chest: ArmorSetOrnatePlate, Robe: 0}
	if got, src := m.resolveSlotTexture(equip, TextSlotChest); got != robe || src != texSrcAuto {
		t.Errorf("robe worn: got (%d, %v), want (%d, %v)", got, src, robe, texSrcAuto)
	}

	equip.HideRobe = true
	if got, src := m.resolveSlotTexture(equip, TextSlotChest); got != ornate || src != texSrcCharESF {
		t.Errorf("robe hidden: got (%d, %v), want (%d, %v)", got, src, ornate, texSrcCharESF)
	}
}

func setSlot(e *EquipmentState, slot int, set byte) {
	switch slot {
	case TextSlotChest:
		e.Chest = set
	case TextSlotBracer:
		e.Bracer = set
	case TextSlotGloves:
		e.Gloves = set
	case TextSlotLegs:
		e.Legs = set
	case TextSlotBoots:
		e.Boots = set
	}
}

// The whole feature rests on one claim: CHARCUST.CSF and CHAR.ESF both hold
// surfaces under the plate DictIDs, and the art differs. If that stops being
// true the ornate set silently renders as ordinary plate.
func TestPlateArtDiffersBetweenFiles(t *testing.T) {
	if _, err := os.Stat(testISOPath); err != nil {
		t.Skip("game ISO not present")
	}

	charFile, err := esf.OpenISOFile(testISOPath, "CHAR.ESF")
	if err != nil {
		t.Fatalf("opening CHAR.ESF: %v", err)
	}
	if err := charFile.BuildDictionary(); err != nil {
		t.Fatalf("CHAR.ESF dictionary: %v", err)
	}
	custFile, err := esf.OpenISOFile(testISOPath, "CHARCUST.CSF")
	if err != nil {
		t.Fatalf("opening CHARCUST.CSF: %v", err)
	}
	if err := custFile.BuildDictionary(); err != nil {
		t.Fatalf("CHARCUST.CSF dictionary: %v", err)
	}

	for slot := TextSlotChest; slot <= TextSlotBoots; slot++ {
		dictID, _ := GetArmorTexture(ArmorSetOrnatePlate, slot)

		charImg := loadSurface(t, charFile, dictID)
		custImg := loadSurface(t, custFile, dictID)
		if charImg == nil {
			t.Errorf("slot %d: DictID %d missing from CHAR.ESF", slot, dictID)
			continue
		}
		if custImg == nil {
			t.Errorf("slot %d: DictID %d missing from CHARCUST.CSF", slot, dictID)
			continue
		}
		if imagesEqual(charImg, custImg) {
			t.Errorf("slot %d: CHAR.ESF and CHARCUST.CSF art is identical for DictID %d — "+
				"the ornate set would be indistinguishable from plate", slot, dictID)
		}
	}
}

func loadSurface(t *testing.T, f *esf.ObjFile, dictID int32) image.Image {
	t.Helper()
	obj, err := f.FindObject(dictID)
	if err != nil || obj == nil {
		return nil
	}
	s, ok := obj.(*esf.Surface)
	if !ok || s.Image == nil {
		return nil
	}
	return s.Image
}

func imagesEqual(a, b image.Image) bool {
	ab, bb := a.Bounds(), b.Bounds()
	if ab.Dx() != bb.Dx() || ab.Dy() != bb.Dy() {
		return false
	}
	for y := 0; y < ab.Dy(); y++ {
		for x := 0; x < ab.Dx(); x++ {
			r1, g1, b1, a1 := a.At(ab.Min.X+x, ab.Min.Y+y).RGBA()
			r2, g2, b2, a2 := b.At(bb.Min.X+x, bb.Min.Y+y).RGBA()
			if r1 != r2 || g1 != g2 || b1 != b2 || a1 != a2 {
				return false
			}
		}
	}
	return true
}
