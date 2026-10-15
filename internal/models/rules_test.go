package models

import "testing"

func TestRulesFor(t *testing.T) {
	for _, c := range []struct {
		ip, setting string
		want        Rules
	}{
		{SandstormIP, "", RulesSandstorm},
		{"192.168.1.10", "", RulesRetail},
		{SandstormIP, "retail", RulesRetail},
		{"192.168.1.10", "sandstorm", RulesSandstorm},
	} {
		if got := RulesFor(c.ip, c.setting); got != c.want {
			t.Errorf("RulesFor(%q, %q) = %v, want %v", c.ip, c.setting, got, c.want)
		}
	}
}

// Retail clamps tints at 15 and sets at 9; Sandstorm's S2 at 30 and 10.
func TestRulesClamps(t *testing.T) {
	defer func(r Rules) { ActiveRules = r }(ActiveRules)

	ActiveRules = RulesRetail
	if GetTintColor(14) != Tints[14] || GetTintColor(15) != Tints[0] || GetTintColor(29) != Tints[0] {
		t.Error("retail: tint 14 is yellow, 15 and up are white")
	}
	if RulesRetail.ClampArmorSet(8) != 8 || RulesRetail.ClampArmorSet(9) != 0 || RulesRetail.ClampArmorSet(0xFF) != 0 {
		t.Error("retail: sets 9 and up are 0")
	}
	if RulesRetail.Robe(4) != 4 {
		t.Error("retail: robe bytes pass through")
	}

	ActiveRules = RulesSandstorm
	if GetTintColor(15) != Tints[15] || GetTintColor(29) != (TintColor{0xFF, 0xD7, 0x00, 0xFF}) || GetTintColor(30) != Tints[0] {
		t.Error("sandstorm: tints 15-29 are its own, 30 and up white")
	}
	if Tints[15].A != 0 {
		t.Error("sandstorm tint 15 is clear (alpha 0)")
	}
	if RulesSandstorm.ClampArmorSet(9) != 9 || RulesSandstorm.ClampArmorSet(10) != 0 {
		t.Error("sandstorm: set 9 is its own, 10 and up are 0")
	}
	if RulesSandstorm.Robe(5) != 0 || RulesSandstorm.Robe(3) != 3 {
		t.Error("sandstorm: robes 4 and 5 fall back to 0 without ss-art.csf")
	}
}
