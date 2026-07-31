package models

import (
	"os"
	"testing"

	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

// The per-race skin tone lives in the material's layer-0 colour, not in the
// texture. The dark-skinned race's body shares a pale skin surface with several
// other races and is tinted dark; its head has its own dark texture and no tint.
// Dropping the colour renders that race with a dark face and a pale body.
//
// This walks the real CHAR.ESF and asserts the tint is present and that
// resolveLayer0Color surfaces it, so a refactor cannot silently discard it again.
func TestLayer0ColorCarriesSkinTone(t *testing.T) {
	if _, err := os.Stat(testISOPath); err != nil {
		t.Skip("game ISO not present")
	}
	f, err := esf.OpenISOFile(testISOPath, "CHAR.ESF")
	if err != nil {
		t.Fatalf("opening CHAR.ESF: %v", err)
	}
	if err := f.BuildDictionary(); err != nil {
		t.Fatalf("dictionary: %v", err)
	}

	// A body model of the dark-skinned race (CSprite Race 8).
	const darkBody = int32(-1213450447)
	obj, err := f.FindObject(darkBody)
	if err != nil || obj == nil {
		t.Fatalf("model %d not found: %v", darkBody, err)
	}
	cs, ok := obj.(*esf.CSprite)
	if !ok {
		t.Fatalf("model %d is a %T, want *esf.CSprite", darkBody, obj)
	}
	if cs.Race != 8 {
		t.Fatalf("model %d has Race %d, want 8 — pick a different fixture", darkBody, cs.Race)
	}

	placements := cs.GetSprites()
	if len(placements) == 0 {
		t.Fatal("no placements")
	}
	sprite, err := placements[0].GetSprite(f)
	if err != nil || sprite == nil {
		t.Fatalf("resolving sprite: %v", err)
	}
	// Same resolution order the renderer uses: the PrimBuffer carries the palette
	// for these sub-sprites, with the sprite's own as fallback.
	pb, err := sprite.GetPrimBuffer(f)
	if err != nil || pb == nil {
		t.Fatalf("prim buffer: %v", err)
	}
	mp := pb.MatPal
	if mp == nil {
		mp, _ = sprite.GetMatPal(f)
	}
	if mp == nil {
		t.Fatal("no material palette")
	}

	// The chest material, via the TSlotList mapping the renderer uses.
	chestMat := -1
	for _, e := range cs.TSlotList {
		if e.SlotID == TextSlotChest {
			chestMat = int(e.MeshIndex)
			break
		}
	}
	if chestMat < 0 {
		t.Fatal("no chest slot in TSlotList")
	}

	got := resolveLayer0Color(mp, chestMat)
	white := [4]byte{255, 255, 255, 255}
	if got == white {
		t.Errorf("chest layer-0 colour is white; the dark skin tint is gone (mat %d)", chestMat)
	}
	// A skin tone, not an arbitrary colour: clearly darker than white and not black.
	sum := int(got[0]) + int(got[1]) + int(got[2])
	if sum >= 600 || sum <= 60 {
		t.Errorf("chest tint %v (sum %d) does not look like a skin tone", got, sum)
	}
	t.Logf("dark-race chest tint = %v (sum %d)", got, sum)

	// The head is dark by texture and must NOT be tinted, or it would double up.
	headMat := -1
	for _, e := range cs.TSlotList {
		if e.SlotID == 7 {
			headMat = int(e.MeshIndex)
			break
		}
	}
	if headMat >= 0 {
		if c := resolveLayer0Color(mp, headMat); c != white {
			t.Errorf("head layer-0 colour = %v, want white (its texture is already dark)", c)
		}
	}
}

// Out-of-range and empty inputs must yield white rather than a zero colour,
// which would render the mesh black.
func TestResolveLayer0ColorDefaultsToWhite(t *testing.T) {
	white := [4]byte{255, 255, 255, 255}
	if got := resolveLayer0Color(nil, 0); got != white {
		t.Errorf("nil palette: got %v, want %v", got, white)
	}
	mp := &esf.MaterialPalette{}
	for _, idx := range []int{-1, 0, 99} {
		if got := resolveLayer0Color(mp, idx); got != white {
			t.Errorf("empty palette idx %d: got %v, want %v", idx, got, white)
		}
	}
}
