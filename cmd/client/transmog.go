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
}

// setName returns the display name of the overridden set.
func (t *transmogState) setName() string {
	if int(t.Set) >= len(models.ArmorSetNames) {
		return "Unknown"
	}
	return models.ArmorSetNames[t.Set]
}
