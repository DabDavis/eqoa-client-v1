package models

import (
	"crypto/sha256"
	"image"
	"os"
	"testing"

	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

// templarDonor is the CHAR.ESF model the set was adapted from: silver plate with
// a blue tabard and gold shoulder medallions.
const templarDonor int32 = -785380397

// The donor packs its armor into two atlases, so the four limb slots share one
// texture and chest has its own. If a future edit gives every slot a distinct
// DictID, it invented art the donor does not have.
func TestTemplarPlateSharesLimbAtlas(t *testing.T) {
	chest, _ := GetArmorTexture(ArmorSetTemplarPlate, TextSlotChest)
	limb, _ := GetArmorTexture(ArmorSetTemplarPlate, TextSlotBracer)

	if chest == limb {
		t.Errorf("chest and limb slots share DictID %d; the donor has two distinct atlases", chest)
	}
	for _, slot := range []int{TextSlotBracer, TextSlotGloves, TextSlotLegs, TextSlotBoots} {
		got, _ := GetArmorTexture(ArmorSetTemplarPlate, slot)
		if got != limb {
			t.Errorf("slot %d: DictID %d, want the shared limb atlas %d", slot, got, limb)
		}
	}
}

// Every slot must be read from CHAR.ESF, and must not collide with the ornate
// set — the two client-side sets have to stay visually distinct.
func TestTemplarPlateSourceAndDistinctness(t *testing.T) {
	for slot := TextSlotChest; slot <= TextSlotBoots; slot++ {
		id, fromChar := GetArmorTexture(ArmorSetTemplarPlate, slot)
		if id == 0 {
			t.Errorf("slot %d has no texture", slot)
		}
		if !fromChar {
			t.Errorf("slot %d: must be read from CHAR.ESF", slot)
		}
		if ornate, _ := GetArmorTexture(ArmorSetOrnatePlate, slot); id == ornate {
			t.Errorf("slot %d: same DictID %d as the ornate set", slot, id)
		}
	}
}

// The set is the five armor slots only. The donor also carries a face and hair
// texture; carrying either would replace the wearer's own head.
func TestTemplarPlateCarriesNoHeadArt(t *testing.T) {
	const (
		donorFace int32 = 1379008418
		donorHair int32 = 1842279120
	)
	for slot := TextSlotChest; slot <= TextSlotBoots; slot++ {
		id, _ := GetArmorTexture(ArmorSetTemplarPlate, slot)
		if id == donorFace {
			t.Errorf("slot %d carries the donor's FACE texture %d", slot, id)
		}
		if id == donorHair {
			t.Errorf("slot %d carries the donor's HAIR texture %d", slot, id)
		}
	}
}

// The draw path must ask for CHAR.ESF, or the lookup falls through the default
// search order and the set renders as whatever it finds first.
func TestResolveSlotTextureUsesCharESFForTemplar(t *testing.T) {
	m := &ModelManager{}
	for slot := TextSlotChest; slot <= TextSlotBoots; slot++ {
		want, _ := GetArmorTexture(ArmorSetTemplarPlate, slot)

		equip := &EquipmentState{Robe: 0xFF}
		setSlot(equip, slot, ArmorSetTemplarPlate)
		got, src := m.resolveSlotTexture(equip, slot)
		if got != want || src != texSrcCharESF {
			t.Errorf("templar slot %d: got (%d, %v), want (%d, %v)",
				slot, got, src, want, texSrcCharESF)
		}
	}
}

// The UI walks ArmorSetNames by index up to ArmorSetCount, so a short or empty
// row is a blank button rather than a compile error.
func TestArmorSetNamesCoverEverySet(t *testing.T) {
	for i := 0; i < ArmorSetCount; i++ {
		if ArmorSetNames[i] == "" {
			t.Errorf("set %d has no name", i)
		}
	}
	if ArmorSetNames[ArmorSetTemplarPlate] != "Templar Plate" {
		t.Errorf("set %d named %q", ArmorSetTemplarPlate, ArmorSetNames[ArmorSetTemplarPlate])
	}
}

// The claims this set rests on, checked against the disc:
//
//  1. Both DictIDs exist in CHAR.ESF and resolve to a Surface.
//  2. Neither exists in CHARCUST.CSF, so nothing shadows them and the CHAR.ESF
//     source is the only one — unlike the ornate set, which needs the forced
//     source precisely because CHARCUST holds different art under its IDs.
//  3. Each resolves UNAMBIGUOUSLY: CHAR.ESF keeps a private Surface copy beside
//     every model that uses a texture, and a handful of DictIDs carry genuinely
//     different art under one ID. If either were ambiguous, FindObject could
//     hand the client a different image than the one the set was measured on.
//  4. The two atlases differ from each other and from the donor's face art.
func TestTemplarPlateArtOnDisc(t *testing.T) {
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

	seen := map[int32]bool{}
	for slot := TextSlotChest; slot <= TextSlotBoots; slot++ {
		dictID, _ := GetArmorTexture(ArmorSetTemplarPlate, slot)
		if seen[dictID] {
			continue
		}
		seen[dictID] = true

		if img := loadSurface(t, charFile, dictID); img == nil {
			t.Errorf("slot %d: DictID %d missing from CHAR.ESF", slot, dictID)
		}
		if img := loadSurface(t, custFile, dictID); img != nil {
			t.Errorf("slot %d: DictID %d also in CHARCUST.CSF — it would be shadowed", slot, dictID)
		}

		// Ambiguity check: count distinct images stored under this DictID.
		distinct := map[string]bool{}
		for _, info := range charFile.AllObjects() {
			if info.Type != esf.TypeSurface || info.DictID != dictID {
				continue
			}
			obj, err := charFile.GetObject(info)
			if err != nil {
				continue
			}
			s, ok := obj.(*esf.Surface)
			if !ok {
				continue
			}
			if err := s.Load(charFile); err != nil || s.Image == nil {
				continue
			}
			distinct[imageKey(s.Image)] = true
		}
		if len(distinct) > 1 {
			t.Errorf("DictID %d has %d distinct images in CHAR.ESF — FindObject may not "+
				"resolve to the art this set was measured on", dictID, len(distinct))
		}
	}

	// The two atlases must differ, and neither may be the donor's face.
	chestID, _ := GetArmorTexture(ArmorSetTemplarPlate, TextSlotChest)
	limbID, _ := GetArmorTexture(ArmorSetTemplarPlate, TextSlotBracer)
	chestImg := loadSurface(t, charFile, chestID)
	limbImg := loadSurface(t, charFile, limbID)
	if chestImg == nil || limbImg == nil {
		t.Fatal("could not load both atlases")
	}
	if imagesEqual(chestImg, limbImg) {
		t.Error("chest and limb atlases hold identical art")
	}
	if face := loadSurface(t, charFile, 1379008418); face != nil {
		if imagesEqual(chestImg, face) || imagesEqual(limbImg, face) {
			t.Error("an armor slot holds the donor's face art")
		}
	}
}

// imageKey hashes an image's pixels so copies stored under one DictID can be
// grouped by content rather than by record.
func imageKey(img image.Image) string {
	b := img.Bounds()
	h := sha256.New()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			h.Write([]byte{byte(r >> 8), byte(g >> 8), byte(bl >> 8), byte(a >> 8)})
		}
	}
	return string(h.Sum(nil))
}
