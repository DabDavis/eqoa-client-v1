package main

import "github.com/eqoa/client/internal/models"

// transmogState is a client-side cosmetic armor override. It changes only what
// this client renders for the local player: nothing is sent to the server, and
// other players still see the real equipment. The server's armor bytes are left
// untouched, so the override survives state channel updates without fighting them.
type transmogState struct {
	Enabled  bool
	Set      byte // armor set index into models.ArmorTextures
	TintSet  bool // apply Tint instead of keeping the server's per-slot tints
	Tint     byte // index into models.Tints
	HideRobe bool // hide an equipped robe, which would otherwise cover chest and legs

	// SlotTex holds a per-slot CHAR.ESF Surface DictID from
	// models.NPCArmorTextures, indexed by models.TextSlotChest..TextSlotBoots.
	// Zero means the slot falls back to Set. These are individual NPC materials
	// rather than coordinated sets, so they are picked one slot at a time.
	SlotTex [5]int32
}

// defaultTransmog returns the starting override: the ornate plate set, which is
// the reason this feature exists — CHAR.ESF's gold-and-steel plate art is
// unreachable through normal equipment because CHARCUST.CSF shadows it.
func defaultTransmog() transmogState {
	return transmogState{
		Set:      models.ArmorSetOrnatePlate,
		Tint:     0, // White — multiplicative, so it shows the art unmodified
		HideRobe: true,
	}
}

// apply rewrites an EquipmentState's five armor slots to the override. It is a
// no-op when disabled, so callers can apply it unconditionally.
//
// Per-slot NPC textures are re-validated against the body being rendered rather
// than trusted. A slot's UV layout is shared across races but splits by sex, so
// art picked on one character can be wrong on another; a selection that does not
// fit is dropped, leaving that slot on Set.
func (t *transmogState) apply(e *models.EquipmentState) {
	if t == nil || !t.Enabled || e == nil {
		return
	}
	e.Chest, e.Bracer, e.Gloves, e.Legs, e.Boots = t.Set, t.Set, t.Set, t.Set, t.Set
	if t.TintSet {
		e.ChestTint, e.BracerTint = t.Tint, t.Tint
		e.GlovesTint, e.LegsTint, e.BootsTint = t.Tint, t.Tint, t.Tint
	}
	if t.HideRobe {
		e.HideRobe = true
	}
	for slot, dictID := range t.SlotTex {
		e.TexOverride[slot] = 0
		if dictID == 0 {
			continue
		}
		if tex, ok := models.NPCArmorLookup(slot, dictID); ok && tex.FitsBody(e.Race, e.Sex) {
			e.TexOverride[slot] = dictID
		}
	}
}

// setName returns the display name of the overridden set.
func (t *transmogState) setName() string {
	if int(t.Set) >= len(models.ArmorSetNames) {
		return "Unknown"
	}
	return models.ArmorSetNames[t.Set]
}

// slotTexName labels a slot's selection for UI, falling back to the set name
// when the slot has no override.
func (t *transmogState) slotTexName(slot int) string {
	if slot < 0 || slot >= len(t.SlotTex) || t.SlotTex[slot] == 0 {
		return "Set: " + t.setName()
	}
	tex, ok := models.NPCArmorLookup(slot, t.SlotTex[slot])
	if !ok {
		return "Unknown"
	}
	if tex.From != "" {
		return tex.Name + " (" + tex.From + ")"
	}
	return tex.Name
}

// cycleSlotTex steps a slot's selection through the textures available for a
// body, treating "no override" as the entry before the first so the set is
// always reachable. Wraps in both directions.
func (t *transmogState) cycleSlotTex(slot int, race, sex byte, delta int) {
	if slot < 0 || slot >= len(t.SlotTex) || delta == 0 {
		return
	}
	avail := models.NPCArmorForSlot(slot, race, sex)
	if len(avail) == 0 {
		t.SlotTex[slot] = 0
		return
	}
	cur := 0 // 0 = no override; 1..len(avail) select a texture
	for i, tex := range avail {
		if tex.DictID == t.SlotTex[slot] {
			cur = i + 1
			break
		}
	}
	n := len(avail) + 1
	next := ((cur+delta)%n + n) % n
	if next == 0 {
		t.SlotTex[slot] = 0
		return
	}
	t.SlotTex[slot] = avail[next-1].DictID
}

// clearSlotTex returns every slot to Set.
func (t *transmogState) clearSlotTex() {
	for i := range t.SlotTex {
		t.SlotTex[i] = 0
	}
}
