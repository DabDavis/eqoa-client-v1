package models

import (
	"os"
	"testing"

	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

// The generated table must stay well-formed: the UI indexes it by slot and
// trusts Compat to keep incompatible art off a body.
func TestNPCArmorTableWellFormed(t *testing.T) {
	if len(NPCArmorTextures) == 0 {
		t.Fatal("table is empty; regenerate with npctex -gogen")
	}
	seen := map[[2]int32]bool{}
	for i, tex := range NPCArmorTextures {
		if tex.Slot < TextSlotChest || tex.Slot > TextSlotBoots {
			t.Errorf("[%d] slot %d out of armor range", i, tex.Slot)
		}
		if tex.DictID == 0 {
			t.Errorf("[%d] zero DictID would read as 'no override'", i)
		}
		if tex.Compat == 0 {
			t.Errorf("[%d] DictID %d fits no body, so it can never be selected", i, tex.DictID)
		}
		if tex.Name == "" {
			t.Errorf("[%d] DictID %d has no UI label", i, tex.DictID)
		}
		key := [2]int32{int32(tex.Slot), tex.DictID}
		if seen[key] {
			t.Errorf("[%d] duplicate (slot %d, DictID %d)", i, tex.Slot, tex.DictID)
		}
		seen[key] = true
	}
}

// Compat is a bitmask over (race, sex); an out-of-range body must never match,
// or a bogus race byte from the wire would unlock arbitrary art.
func TestNPCArmorFitsBodyBounds(t *testing.T) {
	all := NPCArmorTex{Compat: 0xFFFFFFFF}
	if !all.FitsBody(0, 0) {
		t.Error("all-bits mask should fit race 0 sex 0")
	}
	for _, bad := range [][2]byte{{11, 0}, {255, 0}, {0, 2}, {0, 255}} {
		if all.FitsBody(bad[0], bad[1]) {
			t.Errorf("race=%d sex=%d must not match any entry", bad[0], bad[1])
		}
	}
	none := NPCArmorTex{Compat: 0}
	if none.FitsBody(0, 0) {
		t.Error("empty mask must not fit anything")
	}
}

// NPCArmorForSlot is what the picker offers, so it must filter on both axes.
func TestNPCArmorForSlotFilters(t *testing.T) {
	for slot := TextSlotChest; slot <= TextSlotBoots; slot++ {
		for _, sex := range []byte{0, 1} {
			for _, tex := range NPCArmorForSlot(slot, 0, sex) {
				if tex.Slot != slot {
					t.Errorf("slot %d sex %d: got entry for slot %d", slot, sex, tex.Slot)
				}
				if !tex.FitsBody(0, sex) {
					t.Errorf("slot %d sex %d: offered incompatible DictID %d", slot, sex, tex.DictID)
				}
			}
		}
	}
}

// The sex split is the reason Compat exists. If male and female bodies were
// offered identical art the filter would be pointless — and wrong, since the
// measured UV footprints for chest and legs are nearly disjoint across sexes.
func TestNPCArmorSexSplitIsReal(t *testing.T) {
	male := map[int32]bool{}
	for _, tex := range NPCArmorForSlot(TextSlotChest, 0, 0) {
		male[tex.DictID] = true
	}
	female := NPCArmorForSlot(TextSlotChest, 0, 1)
	if len(male) == 0 || len(female) == 0 {
		t.Fatalf("chest art missing for a sex: male=%d female=%d", len(male), len(female))
	}
	for _, tex := range female {
		if male[tex.DictID] {
			t.Errorf("chest DictID %d offered to both sexes; the UV layouts differ", tex.DictID)
		}
	}
}

func TestNPCArmorLookup(t *testing.T) {
	want := NPCArmorTextures[0]
	got, ok := NPCArmorLookup(want.Slot, want.DictID)
	if !ok || got.DictID != want.DictID {
		t.Errorf("lookup(%d, %d) = (%+v, %v)", want.Slot, want.DictID, got, ok)
	}
	if _, ok := NPCArmorLookup(want.Slot, 12345678); ok {
		t.Error("unknown DictID must not resolve")
	}
	// A hit must always come back tagged with the slot that was asked for, so a
	// stored override cannot leak art into a slot it was not authored for.
	for slot := TextSlotChest; slot <= TextSlotBoots; slot++ {
		if got, ok := NPCArmorLookup(slot, want.DictID); ok && got.Slot != slot {
			t.Errorf("lookup(%d, %d) returned an entry for slot %d", slot, want.DictID, got.Slot)
		}
	}
}

// A per-slot override must beat both the armor set and an equipped robe, and
// must be read from CHAR.ESF — that is where the NPC art lives.
func TestResolveSlotTextureUsesOverride(t *testing.T) {
	m := &ModelManager{}
	for slot := TextSlotChest; slot <= TextSlotBoots; slot++ {
		avail := NPCArmorForSlot(slot, 0, 0)
		if len(avail) == 0 {
			continue
		}
		want := avail[0].DictID

		// Robe equipped (covers chest and legs) and a normal armor set on.
		equip := &EquipmentState{Robe: 0}
		setSlot(equip, slot, 4)
		equip.TexOverride[slot] = want

		got, src := m.resolveSlotTexture(equip, slot)
		if got != want || src != texSrcCharESF {
			t.Errorf("slot %d: got (%d, %v), want (%d, %v)", slot, got, src, want, texSrcCharESF)
		}
	}
}

// Clearing an override must fall back to the normal lookup, not to nothing.
func TestResolveSlotTextureZeroOverrideFallsBack(t *testing.T) {
	m := &ModelManager{}
	equip := &EquipmentState{Robe: 0xFF}
	setSlot(equip, TextSlotChest, 4)
	want, _ := GetArmorTexture(4, TextSlotChest)

	if got, src := m.resolveSlotTexture(equip, TextSlotChest); got != want || src != texSrcAuto {
		t.Errorf("got (%d, %v), want (%d, %v)", got, src, want, texSrcAuto)
	}
}

// slotOverride must ignore non-armor slots so hair, face and helm keep their
// own resolution paths.
func TestSlotOverrideIgnoresNonArmorSlots(t *testing.T) {
	equip := &EquipmentState{}
	equip.TexOverride[TextSlotChest] = 999
	for _, slot := range []int{-1, 5, 7, 8, 99} {
		if got := equip.slotOverride(slot); got != 0 {
			t.Errorf("slot %d: got %d, want 0", slot, got)
		}
	}
	if got := equip.slotOverride(TextSlotChest); got != 999 {
		t.Errorf("chest: got %d, want 999", got)
	}
	var nilState *EquipmentState
	if got := nilState.slotOverride(TextSlotChest); got != 0 {
		t.Errorf("nil state: got %d, want 0", got)
	}
}

// Every DictID in the table must actually exist as a Surface in CHAR.ESF at the
// recorded dimensions. This is the check that catches a stale generated table.
func TestNPCArmorTexturesResolveInCharESF(t *testing.T) {
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

	for _, tex := range NPCArmorTextures {
		img := loadSurface(t, charFile, tex.DictID)
		if img == nil {
			t.Errorf("%s: DictID %d missing from CHAR.ESF", tex.Name, tex.DictID)
			continue
		}
		if w, h := img.Bounds().Dx(), img.Bounds().Dy(); w != tex.W || h != tex.H {
			t.Errorf("%s: DictID %d is %dx%d, table says %dx%d",
				tex.Name, tex.DictID, w, h, tex.W, tex.H)
		}
	}
}
