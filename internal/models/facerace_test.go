package models

import (
	"image"
	"os"
	"testing"

	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

// CSprite.Race and the FaceTextures row index agree for 0-8 and diverge above:
// 9 is the ESF parser's "unset" default (every creature reports it) and the Ogre
// body reports 10.
func TestFaceRowForCSpriteRace(t *testing.T) {
	for r := byte(0); r <= 8; r++ {
		if got := FaceRowForCSpriteRace(r); got != int(r) {
			t.Errorf("race %d: row %d, want %d", r, got, r)
		}
	}
	if got := FaceRowForCSpriteRace(10); got != 9 {
		t.Errorf("race 10 (Ogre): row %d, want 9", got)
	}
	// 9 is the sentinel: creature models must not be handed the Ogre faces.
	if got := FaceRowForCSpriteRace(9); got != -1 {
		t.Errorf("race 9 (unset sentinel): row %d, want -1", got)
	}
	for _, bad := range []byte{11, 100, 255} {
		if got := FaceRowForCSpriteRace(bad); got != -1 {
			t.Errorf("race %d: row %d, want -1", bad, got)
		}
	}
}

// The Ogre body reports CSprite.Race 10; before the mapping existed that was out
// of range and Ogres rendered with no face texture at all.
func TestOgreRaceGetsFaces(t *testing.T) {
	for _, sex := range []byte{0, 1} {
		for face := byte(0); face < 4; face++ {
			if id := GetFaceTexture(10, face, sex); id == 0 {
				t.Errorf("Ogre (race 10) face %d sex %d: got 0, want a DictID", face, sex)
			}
		}
	}
	// The sentinel must yield nothing rather than borrowing Ogre art.
	if id := GetFaceTexture(9, 0, 0); id != 0 {
		t.Errorf("race 9 (sentinel): got %d, want 0", id)
	}
}

func TestFaceRowNamesCoverTable(t *testing.T) {
	for i, n := range FaceRowNames {
		if n == "" {
			t.Errorf("row %d has no name", i)
		}
	}
	// Names corrected from the art; row 5 is the green one.
	if FaceRowNames[5] != "Troll" {
		t.Errorf("row 5 = %q, want Troll (its face and body skin are both green)", FaceRowNames[5])
	}
	if FaceRowNames[8] != "Erudite" {
		t.Errorf("row 8 = %q, want Erudite (dark skin)", FaceRowNames[8])
	}
}

// The claim the row order rests on: each row's face art matches, in hue, the body
// skin the renderer puts beside it. Checked against the real ISO for the two rows
// whose colour is unmistakable — row 2 blue-grey and row 5 green — plus row 8 dark.
// If FaceTextures were reordered, or FaceRowForCSpriteRace changed, this breaks.
func TestFaceRowMatchesBodySkinHue(t *testing.T) {
	if _, err := os.Stat(testISOPath); err != nil {
		t.Skip("game ISO not present")
	}
	faceFile, err := esf.OpenISOFile(testISOPath, "CHARFACE.CSF")
	if err != nil {
		t.Fatalf("opening CHARFACE.CSF: %v", err)
	}
	if err := faceFile.BuildDictionary(); err != nil {
		t.Fatalf("CHARFACE dictionary: %v", err)
	}

	cases := []struct {
		race byte
		name string
		// Which channel must dominate the normalised mean, and by how much.
		want func(r, g, b float64) bool
		desc string
	}{
		{2, "DarkElf", func(r, g, b float64) bool { return b > r }, "blue-grey (B > R)"},
		{5, "Troll", func(r, g, b float64) bool { return g > r }, "green (G > R)"},
		{8, "Erudite", func(r, g, b float64) bool { return r+g+b < 240 }, "dark (sum < 240)"},
	}
	for _, c := range cases {
		row := FaceRowForCSpriteRace(c.race)
		if row < 0 {
			t.Errorf("%s: race %d has no row", c.name, c.race)
			continue
		}
		if FaceRowNames[row] != c.name {
			t.Errorf("row %d is named %q, want %q", row, FaceRowNames[row], c.name)
		}
		id := GetFaceTexture(c.race, 0, 0)
		img := loadSurface(t, faceFile, id)
		if img == nil {
			t.Errorf("%s: face DictID %d not in CHARFACE.CSF", c.name, id)
			continue
		}
		r, g, b := meanRGB(img)
		if !c.want(r, g, b) {
			t.Errorf("%s row %d face mean RGB (%.0f,%.0f,%.0f) is not %s",
				c.name, row, r, g, b, c.desc)
		}
		t.Logf("%s row %d: face mean RGB (%.0f,%.0f,%.0f) — %s", c.name, row, r, g, b, c.desc)
	}
}

// meanRGB averages the opaque pixels of the middle half of an image, which skips
// the transparent border padding these face atlases carry.
func meanRGB(img image.Image) (float64, float64, float64) {
	bnds := img.Bounds()
	x0, x1 := bnds.Min.X+bnds.Dx()/4, bnds.Max.X-bnds.Dx()/4
	y0, y1 := bnds.Min.Y+bnds.Dy()/4, bnds.Max.Y-bnds.Dy()/4
	var sr, sg, sb, n float64
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			if a < 0xC000 {
				continue
			}
			sr += float64(r >> 8)
			sg += float64(g >> 8)
			sb += float64(b >> 8)
			n++
		}
	}
	if n == 0 {
		return 0, 0, 0
	}
	return sr / n, sg / n, sb / n
}
