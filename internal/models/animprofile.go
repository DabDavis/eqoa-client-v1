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

// humanoidProfile — player race models with TSlotList (equipment slots).
// Values are ESF CPlayList slot numbers, resolved via PLSlotToRawPair.
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

// npcHumanoidProfile — NPC humanoids (skeletons, undead, etc.) that share
// the paired animation structure but lack TSlotList.
// Uses same ESF CPlayList slot numbers as humanoidProfile since all paired
// models share the same slot semantics (different animation data per model).
var npcHumanoidProfile = AnimProfile{
	SpecialIdle:  15,
	BattleStance: 17,
	Death:        14,
	HitStun:      34,
	CastDamage:   37,
	CastBuff:     39,
	Attack1:      22,
	Attack2:      23,
}

// humanoid40Profile — medium paired creatures (gnolls, etc.) with 40 anims.
// Paired indexing using ESF CPlayList slot numbers. Missing slots 6,7,10,11.
var humanoid40Profile = AnimProfile{
	SpecialIdle:   15,
	BattleStance:  17,
	Death:         14,
	HitStun:       34,
	CastDamage:    37,
	CastBuff:      39,
	Attack1:       22,
	Attack2:       23,
	DualWieldAtks: []byte{22, 23, 24, 25},
	ShieldAtks:    []byte{23, 25},
	TwoHandAtks:   []byte{26, 28},
}

// humanoid68Profile — large paired creatures (Howling Death, etc.) with 68 anims.
// Paired indexing using ESF CPlayList slot numbers.
var humanoid68Profile = AnimProfile{
	SpecialIdle:  15,
	BattleStance: 17,
	Death:        14,
	HitStun:      34,
	CastDamage:   37,
	CastBuff:     39,
	Attack1:      22,
	Attack2:      23,
}

// nonHumanoidLargeProfile — creatures/monsters with single-index animations (18-anim models).
// PLSlotToRaw keys (18-anim): 0,1,2,3,4,5,6,7,10,11,12,13,14,15,17,22,23,30,34,37-39,43,44
// Note: slots 8,9,16 don't exist — attacks are at 22/23, not 8/9.
var nonHumanoidLargeProfile = AnimProfile{
	SpecialIdle:  17, // CL slot 17: Special
	BattleStance: 16, // CL slot 16: BattleStance
	Death:        15, // CL slot 15: Death
	HitStun:      10, // CL slot 10: HitReaction
	CastDamage:   11, // CL slot 11: SpellCast
	CastBuff:     11, // CL slot 11: SpellCast
	Attack1:      8,  // CL slot 8: Attack1
	Attack2:      9,  // CL slot 9: Attack2
	F8Slots: []int{
		0,  // F8 0: idle       → slot 0
		1,  // F8 1: walk       → slot 1
		3,  // F8 2: run        → slot 3
		5,  // F8 3: strafe     → slot 5
		7,  // F8 4: jump/land  → slot 7
		14, // F8 5: emote      → slot 14
		15, // F8 6: death      → slot 15
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

// nonHumanoidSmallProfile — smaller creatures (mouse, etc.) with 13-anim layout.
// PLSlotToRaw keys (13-anim): 0,1,2,3,4,5,12,13,14,15,17,22,23,34,37-39,44
// Missing vs 18-anim: slots 6,7,10,11 (turn, jumpLand, hitReaction, spellCast).
var nonHumanoidSmallProfile = AnimProfile{
	SpecialIdle:  17, // CL slot 17: Special
	BattleStance: 16, // CL slot 16: BattleStance (may not exist on small models)
	Death:        15, // CL slot 15: Death
	HitStun:      10, // CL slot 10: HitReaction (may not exist on small models)
	CastDamage:   11, // CL slot 11: SpellCast (may not exist on small models)
	CastBuff:     11,
	Attack1:      8,  // CL slot 8: Attack1
	Attack2:      9,  // CL slot 9: Attack2
	F8Slots: []int{
		0,  // F8 0: idle       → slot 0
		1,  // F8 1: walk       → slot 1
		3,  // F8 2: run        → slot 3
		5,  // F8 3: strafe     → slot 5
		14, // F8 4: emote      → slot 14
		15, // F8 5: death      → slot 15
		17, // F8 6: special    → slot 17
		12, // F8 7: swim       → slot 12
		22, // F8 8: attack1    → slot 22
		23, // F8 9: attack2    → slot 23
		34, // F8 10: extra     → slot 34
		37, // F8 11: cast      → slot 37
		44, // F8 12: emote3    → slot 44
	},
}

// profileOverrides maps specific model DictIDs to non-default AnimProfiles.
// Final fallback for edge cases that don't fit anim-count-based assignment.
var profileOverrides = map[int32]AnimProfile{
	-0x27360FAF: humanoid68Profile, // Howling Death 0xD8C9F051 (68 anims)
}
