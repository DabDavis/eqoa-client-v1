package models

// NPCArmorTex is one CHAR.ESF texture that can stand in for player armor art in
// a single slot. The disc holds a lot of NPC clothing authored directly on the
// player body meshes; those surfaces are drop-in replacements for the equipment
// swap, unlike creature art, which is painted over bespoke UVs and tiles into
// garbage on a player.
//
// Compat is the constraint that makes this safe to expose. The UV layout for a
// slot is shared across all ten races but splits by sex, so a chest texture
// authored on a male body is wrong on a female one. Each entry therefore records
// exactly which bodies it was measured compatible with.
//
// The table itself is generated — see npcarmor.go.
type NPCArmorTex struct {
	Slot   int    // TextSlotChest..TextSlotBoots
	DictID int32  // ESF Surface DictID, always read from CHAR.ESF
	Source int32  // CSprite DictID of the body this art was authored on
	W, H   int    // base mip dimensions
	Alpha  bool   // has an alpha channel (skin shows through)
	Compat uint32 // bit (race*2+sex) set = measured compatible with that body
	Name   string // stable UI label, e.g. "Chest 7"
	From   string // NPC that wears it, when known; "" otherwise
}

// npcArmorCompatBit is the Compat bit for a body. CSprite.Race values observed
// on the player bodies run 0-8 and 10, and sex is 0 or 1, so every real body
// fits in the low 22 bits. Anything out of range returns 0 so a bogus race byte
// off the wire cannot match an entry.
func npcArmorCompatBit(race, sex byte) uint32 {
	if race > 10 || sex > 1 {
		return 0
	}
	return 1 << uint(race*2+sex)
}

// FitsBody reports whether this texture was measured compatible with a body.
func (t NPCArmorTex) FitsBody(race, sex byte) bool {
	bit := npcArmorCompatBit(race, sex)
	return bit != 0 && t.Compat&bit != 0
}

// NPCArmorForSlot returns the textures usable in one armor slot on one body, in
// table order. The result is freshly allocated, so callers may retain it.
func NPCArmorForSlot(slot int, race, sex byte) []NPCArmorTex {
	var out []NPCArmorTex
	for _, t := range NPCArmorTextures {
		if t.Slot == slot && t.FitsBody(race, sex) {
			out = append(out, t)
		}
	}
	return out
}

// NPCArmorLookup finds a table entry by slot and DictID. Callers that hold a
// stored DictID must go through this rather than trusting it: an override saved
// on one character can be loaded on another whose body the art does not fit.
func NPCArmorLookup(slot int, dictID int32) (NPCArmorTex, bool) {
	for _, t := range NPCArmorTextures {
		if t.Slot == slot && t.DictID == dictID {
			return t, true
		}
	}
	return NPCArmorTex{}, false
}
