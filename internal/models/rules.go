package models

// Rules is the PS2 client a server is built for. Sandstorm's players run the
// retail disc with Sandstorm's S2 patch applied over the patch server, and
// that patch changes rules the retail disc fixes
// (claude-notes/sandstorm-patch-s2-20260730.md). A server of our own talks to
// the retail disc, so it gets retail's rules.
type Rules int

const (
	RulesRetail Rules = iota
	RulesSandstorm
)

// ActiveRules is the rule set of the server this session talks to. The client
// sets it once, from the server it logs in to, before any entity is drawn.
var ActiveRules = RulesSandstorm

// SandstormIP is Sandstorm's address; any other server gets retail rules
// unless the config says otherwise.
const SandstormIP = "5.161.217.188"

// RulesFor picks the rules for a server: an explicit "retail" or "sandstorm"
// wins, otherwise Sandstorm's address means Sandstorm's rules.
func RulesFor(serverIP, setting string) Rules {
	switch setting {
	case "retail":
		return RulesRetail
	case "sandstorm":
		return RulesSandstorm
	}
	if serverIP == SandstormIP {
		return RulesSandstorm
	}
	return RulesRetail
}

func (r Rules) String() string {
	if r == RulesSandstorm {
		return "sandstorm"
	}
	return "retail"
}

// TintCount is how many entries of Tints a tint index may use. Retail's
// SetArmorSlot turns 15 or more into 0; Sandstorm's GetTintColor reads its own
// 30-entry table and the clamp is gone (S2 nops SetArmorSlot+0x58).
func (r Rules) TintCount() int {
	if r == RulesSandstorm {
		return 30
	}
	return 15
}

// ClampArmorSet is SetArmorSlot's set clamp: retail turns 9 or more into 0;
// Sandstorm raises it to 10 (slti 10), for its set 9 — CHAR.ESF's shadowed
// plate surfaces, the same five ArmorSetOrnatePlate draws.
func (r Rules) ClampArmorSet(set byte) byte {
	limit := byte(9)
	if r == RulesSandstorm {
		limit = 10
	}
	if set >= limit { // 0xFF included: it is set 0 on the PS2 too
		return 0
	}
	return set
}

// Helm maps a (clamped) helm set to the helm model drawn. Sandstorm's GetHelm
// hook (S2) sends set 9 to set 4's plate helm (0x03B8E453) — its "36 → 16" is
// byte offsets into $Helm — so its set-9 armour wears a helm; retail clamps
// set 9 away before SetHelm.
func (r Rules) Helm(set byte) byte {
	if r == RulesSandstorm && set == 9 {
		return 4
	}
	return set
}

// RobeCount is how many robes a robe byte can name: retail's $Robe holds 4;
// Sandstorm's GetRobeTexture adds 4 and 5 (a 2×2 table its SetResources hook
// fills from ss-art.csf). A byte past it is no robe.
func (r Rules) RobeCount() int {
	if r == RulesSandstorm {
		return 6
	}
	return 4
}

// Robe maps a robe byte to the robe drawn. Sandstorm's robes 4 and 5 are art
// from its ss-art.csf, which the patcher puts in the save folder; without it
// their table entries stay -1 and its GetRobeTexture falls back to robe 0.
func (r Rules) Robe(robe byte) byte {
	if r == RulesSandstorm && (robe == 4 || robe == 5) && !saveRobeArt {
		return 0
	}
	return robe
}

// saveRobeArt is set once the save folder's art holds all four of
// Sandstorm's robe surfaces (ModelManager.SetSaveArt).
var saveRobeArt bool
