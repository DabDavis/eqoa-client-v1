package models

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/eqoa/client/internal/sspatch"
)

// Rules is the PS2 client this session plays as: the retail disc, as the
// server's patch makes it. Sandstorm's players run retail with Sandstorm's
// S2 applied over the patch server, and S2 changes things retail fixes
// (claude-notes/sandstorm-patch-s2-20260730.md); a server that sends no S2
// talks to retail. So the rules come from what the patcher put in the save
// folder — RulesFor reads its S2 the way the SS stub applies it
// (internal/sspatch) — and each rule below is what that S2 hooks, read off
// the hook. Whatever a patch does not hook stays retail's.
type Rules struct {
	// Source says where the rules came from: "retail", "S2" (the save
	// folder's), or "sandstorm (built in)" (the config's choice).
	Source string

	tints            []TintColor // GetTintColor's table; nil is retail's 15 (Tints[:15])
	tintClampRemoved bool        // SetArmorSlot+0x58: a tint past 14 is kept
	armorSetLimit    byte        // SetArmorSlot+0x44: a set at or past it is 0
	helmRemap        map[byte]byte
	robeLimit        byte              // GetRobeTexture: robes 0 .. robeLimit-1
	robeArt          map[byte][2]int32 // a patch robe's (chest, legs) DictIDs
	worlds           []sspatch.World   // the world table; nil is retail's six

	// ArtFile is the card file CLIENT parses its added art from: retail's
	// addart.csf (16 KB, SOE's own patch-art path), or the patch's own.
	ArtFile string
	// PlayerSearch: Sandstorm's Search for Player window and its opcodes
	// 0x1500-0x1504 (S2 hooks the opcode switch for them).
	PlayerSearch bool
	// DamageRGB: DamageIndication's three trailing bytes colour the number.
	DamageRGB bool
	// CommandsToServer: an unknown /command goes to the server as say
	// (0x000B), slash and all; retail prints string 826 and sends nothing.
	CommandsToServer bool
	// AuctionMaxLevel is the Max. Level the Auctions window opens on.
	AuctionMaxLevel int
}

// ActiveRules is the rule set of this session. The client sets it from the
// save folder at startup, and again once the patcher has run, before any
// entity is drawn.
var ActiveRules = RetailRules()

// RetailRules are the retail disc's: the client a server that sends no S2
// talks to.
func RetailRules() Rules {
	return Rules{Source: "retail", armorSetLimit: 9, robeLimit: 4, ArtFile: "addart.csf", AuctionMaxLevel: 50}
}

// SandstormRules are what Sandstorm's live S2 (ss-patch-60) reads as, for
// the config's "rules": "sandstorm" — a client with no patch in its save
// folder, offline. TestSandstormRulesAreTheLiveS2s pins them to the patch.
func SandstormRules() Rules {
	r := RetailRules()
	r.Source = "sandstorm (built in)"
	r.tints = append([]TintColor(nil), Tints[:]...)
	r.tintClampRemoved = true
	r.armorSetLimit = 10
	r.helmRemap = map[byte]byte{9: 4}
	r.robeLimit = 6
	r.robeArt = map[byte][2]int32{
		4: {int32(-559038737), int32(-889275714)}, // 0xDEADBEEF, 0xCAFEBABE (ss-art.csf)
		5: {int32(-559038722), int32(-889275669)}, // 0xDEADBEFE, 0xCAFEBAEB
	}
	r.ArtFile = "ss-art.csf"
	r.PlayerSearch, r.DamageRGB, r.CommandsToServer = true, true, true
	r.AuctionMaxLevel = 60
	for i, name := range []string{"TUNARIA.ESF", "RATHE.ESF", "ODUS.ESF", "LAVASTM.ESF", "PLANESKY.ESF", "SECRETS.ESF", "UNDERFOOT.ESF", "SECRETS.ESF"} {
		r.worlds = append(r.worlds, sspatch.World{ESF: name, NameID: sandstormWorldNameIDs[i]})
	}
	return r
}

var sandstormWorldNameIDs = [8]int{0x413, 0x414, 0x5A0, 0x5B6, 0x5B7, 0x6F8, 0x6F8, 0x6F8}

// RulesFromPatch builds the rules from what an S2's hooks say.
func RulesFromPatch(f *sspatch.Features) Rules {
	r := RetailRules()
	r.Source = "S2"
	if f.Tints != nil {
		r.tints = make([]TintColor, len(f.Tints))
		for i, t := range f.Tints {
			r.tints[i] = TintColor{t[0], t[1], t[2], t[3]}
		}
	}
	r.tintClampRemoved = f.TintClampRemoved
	if f.ArmorSetLimit > 0 && f.ArmorSetLimit < 256 {
		r.armorSetLimit = byte(f.ArmorSetLimit)
	}
	for from, to := range f.HelmRemap {
		if r.helmRemap == nil {
			r.helmRemap = map[byte]byte{}
		}
		r.helmRemap[byte(from)] = byte(to)
	}
	if f.RobeLimit > 4 && f.RobeLimit < 256 {
		r.robeLimit = byte(f.RobeLimit)
	}
	for robe, art := range f.RobeArt {
		if r.robeArt == nil {
			r.robeArt = map[byte][2]int32{}
		}
		r.robeArt[byte(robe)] = [2]int32{int32(art[0]), int32(art[1])}
	}
	if f.ArtFile != "" {
		r.ArtFile = f.ArtFile
	}
	r.PlayerSearch, r.DamageRGB, r.CommandsToServer = f.PlayerSearch, f.DamageRGB, f.CommandsToServer
	if f.AuctionMaxLevel > 0 {
		r.AuctionMaxLevel = f.AuctionMaxLevel
	}
	r.worlds = f.Worlds
	return r
}

// RulesFor picks the rules for a session: the config's "retail" or
// "sandstorm" if it names one, otherwise the S2 in the save folder dir, as
// the patcher left it, otherwise retail's. An S2 its stub would refuse
// applies nothing on a console, and nothing here.
func RulesFor(dir, setting string) Rules {
	switch setting {
	case "retail":
		return RetailRules()
	case "sandstorm":
		return SandstormRules()
	}
	data, err := os.ReadFile(filepath.Join(dir, "S2"))
	if err != nil {
		return RetailRules()
	}
	p, err := sspatch.Parse(data)
	if err != nil {
		log.Printf("rules: %s: %v; playing as retail", filepath.Join(dir, "S2"), err)
		return RetailRules()
	}
	return RulesFromPatch(sspatch.Read(p))
}

func (r Rules) String() string {
	return fmt.Sprintf("%s (tints %d, sets < %d, robes %d, worlds %d, art %s, search %v, rgb %v, commands %v, auction %d)",
		r.Source, r.TintCount(), r.armorSetLimit, r.robeLimit, r.WorldCount(), r.ArtFile,
		r.PlayerSearch, r.DamageRGB, r.CommandsToServer, r.AuctionMaxLevel)
}

// TintCount is how many tints an index may use. Retail's SetArmorSlot turns
// 15 or more into 0; a patch that removes that clamp (S2 nops
// SetArmorSlot+0x58) lets an index reach the end of its own table.
func (r Rules) TintCount() int {
	if r.tints != nil && r.tintClampRemoved {
		return len(r.tints)
	}
	return 15
}

// Tint is the colour of a tint index; past TintCount, white (index 0).
func (r Rules) Tint(i byte) TintColor {
	if int(i) >= r.TintCount() {
		i = 0
	}
	if r.tints != nil {
		return r.tints[i]
	}
	return Tints[i]
}

// ClampArmorSet is SetArmorSlot's set clamp: retail turns 9 or more into 0;
// Sandstorm's S2 raises it to 10 for its set 9 — CHAR.ESF's shadowed plate
// surfaces, the same five ArmorSetOrnatePlate draws — and our server's to
// 11 for set 10, ArmorSetTemplarPlate.
func (r Rules) ClampArmorSet(set byte) byte {
	if set >= r.armorSetLimit { // 0xFF included: it is set 0 on the PS2 too
		return 0
	}
	return set
}

// Helm maps a (clamped) helm set to the helm model drawn. Sandstorm's GetHelm
// hook sends set 9 to set 4's plate helm (0x03B8E453) — its "36 → 16" is byte
// offsets into $Helm — so its set-9 armour wears a helm.
func (r Rules) Helm(set byte) byte {
	if to, ok := r.helmRemap[set]; ok {
		return to
	}
	return set
}

// RobeCount is how many robes a robe byte can name: retail's $Robe holds 4;
// a patch's GetRobeTexture may take more (Sandstorm's 4 and 5). A byte past
// it is no robe.
func (r Rules) RobeCount() int { return int(r.robeLimit) }

// Robe maps a robe byte to the robe drawn. A patch robe's art comes from the
// patch's art file in the save folder; until both its surfaces are there,
// GetRobeTexture's table entries stay -1 and it falls back to robe 0.
func (r Rules) Robe(robe byte) byte {
	if robe >= 4 && robe < r.robeLimit && !saveRobeArt[robe] {
		return 0
	}
	return robe
}

// RobeTexture is a robe's (chest, legs) DictIDs: retail's $Robe for 0-3, the
// patch's FindTyped ids past them.
func (r Rules) RobeTexture(robe byte) [2]int32 {
	if robe < 4 {
		return RobeTextures[robe]
	}
	return r.robeArt[robe]
}

// WorldCount is how many worlds the world table holds.
func (r Rules) WorldCount() int {
	if r.worlds != nil {
		return len(r.worlds)
	}
	return len(retailWorldESF)
}

// WorldESF is the ESF a world id loads: the patch's world table, or
// retail's six. Sandstorm's 6 is data3\Underfoot.esf, which is on no disc.
func (r Rules) WorldESF(id byte) (string, bool) {
	if r.worlds != nil {
		if int(id) < len(r.worlds) {
			return r.worlds[id].ESFName(), true
		}
		return "", false
	}
	if int(id) < len(retailWorldESF) {
		return retailWorldESF[id], true
	}
	return "", false
}

// WorldNameIDs are the world table's name string ids (0x413 "Tunaria" ...).
func (r Rules) WorldNameIDs() []int {
	if r.worlds == nil {
		return append([]int(nil), sandstormWorldNameIDs[:len(retailWorldESF)]...)
	}
	ids := make([]int, len(r.worlds))
	for i, w := range r.worlds {
		ids[i] = w.NameID
	}
	return ids
}

// retailWorldESF is CLIENT's own world table, as ESF names on the disc.
var retailWorldESF = []string{"TUNARIA.ESF", "RATHE.ESF", "ODUS.ESF", "LAVASTM.ESF", "PLANESKY.ESF", "SECRETS.ESF"}

// saveRobeArt is which patch robes have both surfaces in the save folder's
// art (ModelManager.SetSaveArt).
var saveRobeArt = map[byte]bool{}
