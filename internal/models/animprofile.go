package models

// AnimProfile maps logical animation roles to the correct anim pair indices
// for a specific model type. Stored on each CharModel during loadModel().
// Three default profiles cover the main model categories; individual models
// can be overridden later by inspecting their CPlayList.
type AnimProfile struct {
	SpecialIdle  byte  // fidget animation
	BattleStance byte  // combat idle
	Death        byte  // death pose
	HitStun      byte  // hit reaction
	CastDamage   byte  // hostile spell cast
	CastBuff     byte  // friendly spell cast
	Attack1      byte  // primary attack
	Attack2      byte  // secondary attack
	F8Slots      []int // CPlayList slots in F8 debug cycling order (nil = humanoid paired)

	// Weapon-specific attack pools (nil = use global weaponAttackAnims table).
	// For equipped non-player-race models (gnolls, etc.) that have different attack pairs.
	DualWieldAtks []byte // 1H dual wield attack cycle
	ShieldAtks    []byte // 1H + shield attack cycle
	TwoHandAtks   []byte // 2H weapon attack cycle
}

// humanoidProfile — all paired humanoid models (player races, NPC humanoids,
// skeletons, undead, etc.). Values are ESF CPlayList slot numbers, resolved
// via PLSlotToRawPair. All paired models share the same slot semantics
// (different animation data per model).
var humanoidProfile = AnimProfile{
	SpecialIdle:  15,
	BattleStance: 17,
	Death:        14,
	HitStun:      34,
	CastDamage:   37,
	CastBuff:     39,
	Attack1:      22,
	Attack2:      23,
}

// humanoidArmedProfile — medium paired creatures (gnolls, etc.) with weapon-
// specific attack pools. Same base slots as humanoidProfile but adds
// DualWield/Shield/TwoHand attack cycles for equipped NPC models.
var humanoidArmedProfile = AnimProfile{
	SpecialIdle:   15,
	BattleStance:  17,
	Death:         14,
	HitStun:       34,
	CastDamage:    37,
	CastBuff:      39,
	Attack1:       22,
	Attack2:       23,
	// Slot semantics verified via animviewer (HumanM). 24/25=slash off/main,
	// 40/41=slash quad 3rd/4th, 26=2H slash, 29=2H poke, 42=2H smash.
	DualWieldAtks: []byte{24, 25, 40, 41}, // slash quad: off, main, off-3rd, main-4th (was 22,23 unarmed + 24,25)
	ShieldAtks:    []byte{25, 30},         // mainhand slash + shield bash (was 23 unarmed + 25)
	TwoHandAtks:   []byte{26, 29, 42},     // 2H slash/poke/smash (was 26 + 28=1H poke!)
}

// nonHumanoidProfile — creatures/monsters with single-index animations.
// Covers SkelType 0 (simple), 2 (quadruped), 3 (flying). F8Slots uses
// the large variant's superset; small models gracefully skip missing slots.
// Verified CPlayList slots from ESF scan: 0,1,2,3,4,5,6,7,10,11,12,13,14,15,17,22,23,30,34,37-39,43,44
// Slots 8,9,16 do NOT exist — attacks at 22/23, battle stance at 17.
// Death and the special idle are the same slots for every skeleton:
// SetAnimation forces 14 when m_dead and substitutes 15 for idle when the
// fidget timer runs out (vicsprite.cpp). Many creatures have slot 14 as their
// only play-once animation and no slot 15 at all.
var nonHumanoidProfile = AnimProfile{
	SpecialIdle:  15, // CL slot 15: special idle (fidget)
	BattleStance: 17, // CL slot 17: combat idle (slot 16 doesn't exist for SkelType 0/3)
	Death:        14, // CL slot 14: death
	HitStun:      34, // CL slot 34: HitReaction (slot 10 only exists on SkelType 2)
	CastDamage:   37, // CL slot 37: SpellCast (slot 11 only exists on SkelType 2)
	CastBuff:     37, // CL slot 37: SpellCast
	Attack1:      22, // CL slot 22: Attack1 (verified from ESF — slot 8 doesn't exist)
	Attack2:      23, // CL slot 23: Attack2 (verified from ESF — slot 9 doesn't exist)
	F8Slots: []int{
		0,  // F8 0: idle       → slot 0
		1,  // F8 1: walk       → slot 1
		3,  // F8 2: run        → slot 3
		5,  // F8 3: strafe     → slot 5
		7,  // F8 4: jump/land  → slot 7
		14, // F8 5: death      → slot 14
		15, // F8 6: fidget     → slot 15
		17, // F8 7: battle stance → slot 17
		6,  // F8 8: turn       → slot 6
		10, // F8 9: hit        → slot 10
		11, // F8 10: cast      → slot 11
		22, // F8 11: attack1   → slot 22
		23, // F8 12: attack2   → slot 23
		12, // F8 13: swim      → slot 12
		34, // F8 14: extra     → slot 34
		37, // F8 15: cast      → slot 37
		43, // F8 16: emote3    → slot 43
		44, // F8 17: emote4    → slot 44
	},
}

// profileOverrides maps specific model DictIDs to non-default AnimProfiles.
// Final fallback for edge cases that don't fit SkelType-based assignment.
var profileOverrides = map[int32]AnimProfile{}

// ApplyDefaultEntries is VICSprite::SetDefaultEntries (decomp/src/vicsprite.cpp,
// run by ParseCSpriteObj right after the play list is parsed): every slot the
// ESF left empty — no entry, or an entry whose upper animation did not
// resolve — becomes a COPY of a related one, the whole entry (animations,
// weight, sync, sounds). Most come from idle (0), the attack families from 23
// or 22, and 65 from 41 at half weight. Slots 7, 34 and the emotes copied
// from idle play once. Slots with no case (19-21, 47-49, 54, 55, 58, 62) stay
// empty. It is idempotent: a second call finds every slot it fills full.
//
// It is what makes an attack slot a model lacks play something sensible:
// throwing (attack type 8) asks for 35/32, which no player body has, and gets
// 23/22; a creature's alt attack 25/24 gets its 23/22.
func ApplyDefaultEntries(m *CharModel) {
	if m == nil || m.PLSlotToRawPair == nil {
		return
	}
	has := func(slot int) bool {
		pair, ok := m.PLSlotToRawPair[slot]
		return ok && pair[0] >= 0
	}
	// copyEntry copies slot src into dst; once != nil forces the sync.
	copyEntry := func(dst, src int, once *bool) {
		pair, ok := m.PLSlotToRawPair[src]
		if !ok {
			// m_entries[src] is the constructor's: no animation, weight 1,
			// loop. Copying it leaves dst empty, as on the PS2.
			return
		}
		m.PLSlotToRawPair[dst] = pair
		if m.PLSlotToRaw != nil {
			if raw, ok := m.PLSlotToRaw[src]; ok {
				m.PLSlotToRaw[dst] = raw
			} else {
				delete(m.PLSlotToRaw, dst)
			}
		}
		if m.PLSlotSpeed != nil {
			if spd, ok := m.PLSlotSpeed[src]; ok {
				m.PLSlotSpeed[dst] = spd
			}
		}
		if m.PLSlotPlayOnce != nil {
			m.PLSlotPlayOnce[dst] = m.PLSlotPlayOnce[src]
			if once != nil {
				m.PLSlotPlayOnce[dst] = *once
			}
		}
		if m.PLSlotSound != nil {
			if snd, ok := m.PLSlotSound[src]; ok {
				m.PLSlotSound[dst] = snd
			} else {
				delete(m.PLSlotSound, dst)
			}
		}
	}
	once := true
	for i := 0; i < 66; i++ {
		if has(i) {
			continue
		}
		switch i {
		case 0, 1, 2, 3, 4, 5, 6, 8, 9, 10, 11, 12, 13, 14,
			15, 16, 17, 18, 37, 38, 39, 45, 46:
			copyEntry(i, 0, nil)

		case 65:
			switch {
			case m.Race == 9:
				copyEntry(i, 0, &once)
			case has(41):
				copyEntry(i, 41, nil)
				if m.PLSlotSpeed != nil {
					if _, ok := m.PLSlotToRawPair[i]; ok {
						spd, ok := m.PLSlotSpeed[i]
						if !ok {
							spd = 1 // m_weight's constructor value
						}
						m.PLSlotSpeed[i] = spd * 0.5
					}
				}
			default:
				copyEntry(i, 0, &once)
			}
			// m_sound[0] = m_sound[1] = -1.
			if m.PLSlotSound != nil {
				delete(m.PLSlotSound, i)
			}

		case 7, 34, 56, 57, 59, 60, 61, 63, 64:
			copyEntry(i, 0, &once)

		case 23, 25, 26, 28, 29, 31, 33, 35, 41, 42, 43, 44, 50, 51, 52, 53:
			if has(23) {
				copyEntry(i, 23, nil)
			} else {
				copyEntry(i, 0, &once)
			}

		case 22, 24, 27, 30, 32, 36, 40:
			switch {
			case has(22):
				copyEntry(i, 22, nil)
			case has(23):
				copyEntry(i, 23, nil)
			default:
				copyEntry(i, 0, &once)
			}
		}
	}
}
