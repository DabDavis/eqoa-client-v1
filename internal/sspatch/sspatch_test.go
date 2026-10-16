package sspatch

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The S2 builds this reads, none of them in the repository (they are
// Sandstorm's code, and our own derivative): Sandstorm's live ss-patch-60
// (2026-10-15), its July build, and what go-eqoa-server serves.
const root = "../../.."

var builds = map[string]string{
	"live": "extracted-assets/sandstorm-patch-20261015/S2",
	"july": "go-eqoa-server/patches/S2-stock",
	"ours": "go-eqoa-server/patches/serve/S2",
}

func load(t *testing.T, which string) *Features {
	data, err := os.ReadFile(filepath.Join(root, builds[which]))
	if err != nil {
		t.Skipf("no %s S2 here: %v", which, err)
	}
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("%s: %v", which, err)
	}
	return Read(p)
}

// Every feature the Go client takes from S2, read off Sandstorm's live
// build, is what the July and live notes say it is.
func TestLiveS2(t *testing.T) {
	f := load(t, "live")
	if len(f.Tints) != 30 || !f.TintClampRemoved {
		t.Errorf("tints %d, clamp removed %v", len(f.Tints), f.TintClampRemoved)
	}
	want := []RGBA{{0xFF, 0xFF, 0xFF, 0xFF}, {0x5A, 0x5A, 0x5A, 0xFF}, {0x00, 0xB4, 0xFF, 0xFF}}
	if !reflect.DeepEqual(f.Tints[:3], want) || f.Tints[15] != (RGBA{}) || f.Tints[29] != (RGBA{0xFF, 0xD7, 0x00, 0xFF}) {
		t.Errorf("tint table %v", f.Tints)
	}
	if f.ArmorSetLimit != 10 {
		t.Errorf("armour set limit %d", f.ArmorSetLimit)
	}
	if !reflect.DeepEqual(f.HelmRemap, map[int]int{9: 4}) {
		t.Errorf("helm remap %v", f.HelmRemap)
	}
	if f.RobeLimit != 6 || !reflect.DeepEqual(f.RobeArt, map[int][2]uint32{
		4: {0xDEADBEEF, 0xCAFEBABE}, 5: {0xDEADBEFE, 0xCAFEBAEB},
	}) {
		t.Errorf("robes %d %v", f.RobeLimit, f.RobeArt)
	}
	if f.ArtFile != "ss-art.csf" {
		t.Errorf("art file %q", f.ArtFile)
	}
	if !f.PlayerSearch || !f.DamageRGB || !f.CommandsToServer {
		t.Errorf("search %v, rgb %v, commands %v", f.PlayerSearch, f.DamageRGB, f.CommandsToServer)
	}
	if f.AuctionMaxLevel != 60 {
		t.Errorf("auction level %d", f.AuctionMaxLevel)
	}
	names := []string{"TUNARIA.ESF", "RATHE.ESF", "ODUS.ESF", "LAVASTM.ESF", "PLANESKY.ESF", "SECRETS.ESF", "UNDERFOOT.ESF", "SECRETS.ESF"}
	if len(f.Worlds) != len(names) {
		t.Fatalf("%d worlds", len(f.Worlds))
	}
	for i, w := range f.Worlds {
		if w.ESFName() != names[i] {
			t.Errorf("world %d: %s", i, w.ESFName())
		}
	}
	if w := f.Worlds[0]; w.Name != "tunaria" || w.NameID != 0x413 || w.MaxCol != 12 || w.MaxRow != 15 {
		t.Errorf("world 0: %+v", w)
	}
}

// July's build is the live one without the auction change; ours raises the
// armour set clamp for set 10.
func TestOtherBuilds(t *testing.T) {
	if f := load(t, "july"); f.AuctionMaxLevel != 0 || f.ArmorSetLimit != 10 || len(f.Tints) != 30 || len(f.Worlds) != 8 {
		t.Errorf("july: auction %d, sets %d, tints %d, worlds %d", f.AuctionMaxLevel, f.ArmorSetLimit, len(f.Tints), len(f.Worlds))
	}
	if f := load(t, "ours"); f.ArmorSetLimit != 11 || f.RobeLimit != 6 || !f.PlayerSearch {
		t.Errorf("ours: sets %d, robes %d, search %v", f.ArmorSetLimit, f.RobeLimit, f.PlayerSearch)
	}
}

// The stub's checks, on a patch built here (so CI has one).
func TestParseRules(t *testing.T) {
	rec := func(addr uint32, data ...byte) []byte {
		b := binary.LittleEndian.AppendUint32(nil, addr)
		b = binary.LittleEndian.AppendUint32(b, uint32(len(data)))
		b = append(b, data...)
		for len(b)%4 != 0 {
			b = append(b, 0)
		}
		return b
	}
	tail := func(relocs ...uint32) []byte {
		b := make([]byte, 8)
		b = binary.LittleEndian.AppendUint32(b, rlc1)
		b = binary.LittleEndian.AppendUint32(b, uint32(len(relocs)))
		for _, r := range relocs {
			b = binary.LittleEndian.AppendUint32(b, r)
		}
		return b
	}
	// A cave table, a hook pointing at it, and the relocation of its lui.
	good := append(rec(0x800FCD00, 1, 2, 3, 4, 5, 6, 7, 8),
		rec(0x804294E8, 0x0F, 0x00, 0x08, 0x3C, 0x00, 0xCD, 0x08, 0x35, 0x80, 0x20, 0x04, 0x00)...)
	good = append(good, tail(0x804294EA)...)
	p, err := Parse(good)
	if err != nil {
		t.Fatal(err)
	}
	f := Read(p)
	if len(f.Tints) != 2 || f.Tints[1] != (RGBA{5, 6, 7, 8}) {
		t.Fatalf("tints %v", f.Tints)
	}
	// Moved by a delta, the cave and the lui move together.
	img := p.Apply(0x660000)
	if w, _ := img.Word(getTintColor); w != 0x3C080075 {
		t.Fatalf("relocated lui %08X", w)
	}
	if _, ok := img.Word(0x000FCD00 + 0x660000); !ok {
		t.Fatal("the cave record did not move")
	}

	for name, bad := range map[string][]byte{
		"short":          {1, 2, 3},
		"no RLC1":        append(rec(0x800FCD00, 1, 2, 3, 4), make([]byte, 16)...),
		"count mismatch": append(append(rec(0x800FCD00, 1, 2, 3, 4), tail()...), 0, 0, 0, 0),
		"out of cave":    append(rec(0x800FFFFC, 1, 2, 3, 4, 5, 6, 7, 8), tail()...),
		"past the end":   rec(0x800FCD00, 1, 2, 3, 4)[:10],
		"buffer full":    make([]byte, maxFile),
	} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
