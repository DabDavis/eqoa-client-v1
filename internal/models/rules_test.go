package models

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/eqoa/client/internal/sspatch"
)

// Sandstorm's live S2 (2026-10-15), kept outside the repository.
const liveS2 = "../../../extracted-assets/sandstorm-patch-20261015/S2"

func readLiveS2(t *testing.T) []byte {
	data, err := os.ReadFile(liveS2)
	if err != nil {
		t.Skip("no local copy of Sandstorm's S2")
	}
	return data
}

// The config's choice wins; otherwise the save folder's S2 decides, and a
// folder without one — or with one its stub would refuse — plays as retail.
func TestRulesFor(t *testing.T) {
	empty := t.TempDir()
	if r := RulesFor(empty, ""); r.Source != "retail" {
		t.Errorf("empty folder: %s", r.Source)
	}
	if r := RulesFor(empty, "sandstorm"); r.Source != "sandstorm (built in)" || !r.PlayerSearch {
		t.Errorf("config sandstorm: %s", r.Source)
	}
	bad := t.TempDir()
	os.WriteFile(filepath.Join(bad, "S2"), []byte("not a patch"), 0o644)
	if r := RulesFor(bad, ""); r.Source != "retail" {
		t.Errorf("refused S2: %s", r.Source)
	}

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "S2"), readLiveS2(t), 0o644)
	if r := RulesFor(dir, ""); r.Source != "S2" || !r.PlayerSearch || r.AuctionMaxLevel != 60 {
		t.Errorf("live S2: %v", r)
	}
	if r := RulesFor(dir, "retail"); r.Source != "retail" || r.PlayerSearch {
		t.Errorf("config retail over an S2: %v", r)
	}
}

// SandstormRules, the built-in copy, says what the live S2 says, rule by
// rule.
func TestSandstormRulesAreTheLiveS2s(t *testing.T) {
	p, err := sspatch.Parse(readLiveS2(t))
	if err != nil {
		t.Fatal(err)
	}
	got, want := RulesFromPatch(sspatch.Read(p)), SandstormRules()
	got.Source, want.Source = "", ""
	for i := 0; i < 32; i++ {
		if got.Tint(byte(i)) != want.Tint(byte(i)) {
			t.Errorf("tint %d: %v, built in %v", i, got.Tint(byte(i)), want.Tint(byte(i)))
		}
	}
	for set := byte(0); set < 12; set++ {
		if got.ClampArmorSet(set) != want.ClampArmorSet(set) || got.Helm(set) != want.Helm(set) {
			t.Errorf("set %d", set)
		}
	}
	for robe := byte(0); robe < 8; robe++ {
		if got.RobeTexture(robe) != want.RobeTexture(robe) {
			t.Errorf("robe %d: %v, built in %v", robe, got.RobeTexture(robe), want.RobeTexture(robe))
		}
	}
	if got.RobeCount() != want.RobeCount() || got.ArtFile != want.ArtFile || got.AuctionMaxLevel != want.AuctionMaxLevel ||
		got.PlayerSearch != want.PlayerSearch || got.DamageRGB != want.DamageRGB || got.CommandsToServer != want.CommandsToServer {
		t.Errorf("S2 %v\nbuilt in %v", got, want)
	}
	if !reflect.DeepEqual(got.WorldNameIDs(), want.WorldNameIDs()) {
		t.Errorf("world names %v, built in %v", got.WorldNameIDs(), want.WorldNameIDs())
	}
	for id := byte(0); id < 9; id++ {
		g, gok := got.WorldESF(id)
		w, wok := want.WorldESF(id)
		if g != w || gok != wok {
			t.Errorf("world %d: %q, built in %q", id, g, w)
		}
	}
}

// Retail clamps tints at 15 and sets at 9; Sandstorm's S2 at 30 and 10.
func TestRulesClamps(t *testing.T) {
	defer func(r Rules) { ActiveRules = r }(ActiveRules)
	retail, sandstorm := RetailRules(), SandstormRules()

	ActiveRules = retail
	if GetTintColor(14) != Tints[14] || GetTintColor(15) != Tints[0] || GetTintColor(29) != Tints[0] {
		t.Error("retail: tint 14 is yellow, 15 and up are white")
	}
	if retail.ClampArmorSet(8) != 8 || retail.ClampArmorSet(9) != 0 || retail.ClampArmorSet(0xFF) != 0 {
		t.Error("retail: sets 9 and up are 0")
	}
	if retail.Robe(4) != 4 || retail.RobeCount() != 4 {
		t.Error("retail: robe bytes pass through, and 4 is no robe")
	}
	if name, ok := retail.WorldESF(6); ok || name != "" {
		t.Error("retail: six worlds")
	}

	ActiveRules = sandstorm
	if GetTintColor(15) != Tints[15] || GetTintColor(29) != (TintColor{0xFF, 0xD7, 0x00, 0xFF}) || GetTintColor(30) != Tints[0] {
		t.Error("sandstorm: tints 15-29 are its own, 30 and up white")
	}
	if Tints[15].A != 0 {
		t.Error("sandstorm tint 15 is clear (alpha 0)")
	}
	if sandstorm.ClampArmorSet(9) != 9 || sandstorm.ClampArmorSet(10) != 0 {
		t.Error("sandstorm: set 9 is its own, 10 and up are 0")
	}
	if sandstorm.Helm(9) != 4 || sandstorm.Helm(3) != 3 || retail.Helm(9) != 9 {
		t.Error("sandstorm: set 9's helm is set 4's plate helm; retail never sees 9")
	}
	saveRobeArt = map[byte]bool{}
	if sandstorm.Robe(5) != 0 || sandstorm.Robe(3) != 3 {
		t.Error("sandstorm: robes 4 and 5 fall back to 0 without ss-art.csf")
	}
	if name, _ := sandstorm.WorldESF(6); name != "UNDERFOOT.ESF" {
		t.Errorf("sandstorm world 6: %q", name)
	}
}
