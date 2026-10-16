package main

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// clientImage is CLIENT's decrypted, decompressed module image, indexed by
// VA − clientImageBase (decomp/eqdecomp.py's module table). It is not in the
// repository; the tests that need it skip without it.
const clientImageBase = 0x00BB4980

func readClientImage(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join("..", "..", "..", "claude-eqoa-iso", "decrypted_modules", "CLIENT_decompressed_SNR1.bin")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("no CLIENT image: %v", err)
	}
	return b
}

// TestRaceCameraTablesMatchImage pins both VIRaceCamera tables, every byte of
// all 21 entries, against the image.
func TestRaceCameraTablesMatchImage(t *testing.T) {
	img := readClientImage(t)
	for _, tc := range []struct {
		name  string
		va    int
		table *[21]raceCamera
	}{
		{"base", 0x00CDD8A0, &raceCameraBase},
		{"expansion", 0x00CDDD38, &raceCameraExp},
	} {
		for i, e := range tc.table {
			off := tc.va - clientImageBase + i*56
			raw := img[off : off+56]
			if got := int32(binary.LittleEndian.Uint32(raw)); got != e.Race {
				t.Errorf("%s[%d] race: image %d, table %d", tc.name, i, got, e.Race)
			}
			want := []float32{
				e.Login.TargetHeight, e.Login.LookAtHeightAbove, e.Login.Pitch, e.Login.Distance,
				e.Close.TargetHeight, e.Close.LookAtHeightAbove, e.Close.Pitch, e.Close.Distance,
				e.World.TargetHeight, e.World.LookAtHeightAbove, e.World.Pitch, e.World.Distance,
				e.EyeHeight,
			}
			for k, w := range want {
				bits := binary.LittleEndian.Uint32(raw[4+4*k:])
				if bits != math.Float32bits(w) {
					t.Errorf("%s[%d] float %d (+0x%02X): image %v (0x%08X), table %v",
						tc.name, i, k, 4+4*k, math.Float32frombits(bits), bits, w)
				}
			}
		}
	}
}

// TestGetRaceCamera is GetRaceCamera's selection: bit 1 of the expansion
// flags picks the second table, a race past 20 is the human, and the empty
// entries find nothing.
func TestGetRaceCamera(t *testing.T) {
	if rc := getRaceCamera(5, 0); rc != &raceCameraBase[5] {
		t.Errorf("troll, flags 0: not the base table's entry")
	}
	if rc := getRaceCamera(5, 2); rc != &raceCameraExp[5] {
		t.Errorf("troll, flags 2: not the expansion table's entry")
	}
	if rc := getRaceCamera(5, 1); rc != &raceCameraBase[5] {
		t.Errorf("troll, flags 1: bit 0 must not pick the expansion table")
	}
	if rc := getRaceCamera(21, 3); rc != &raceCameraExp[0] {
		t.Errorf("race 21: want the human (key 0)")
	}
	for _, r := range []int{10, 15, 20} {
		if rc := getRaceCamera(r, 3); rc != nil {
			t.Errorf("race %d: want nil (no entry), got race %d", r, rc.Race)
		}
	}
	// The tables differ only in the window cameras; the world camera and the
	// eye height, all the in-world camera reads, are the same in both.
	for i := 0; i < 10; i++ {
		if raceCameraBase[i].World != raceCameraExp[i].World || raceCameraBase[i].EyeHeight != raceCameraExp[i].EyeHeight {
			t.Errorf("race %d: the world camera differs between the tables", i)
		}
	}
}

// TestRaceCameraNoEntryFallsBack: the custom race 10 (Sandstorm's froglok)
// has no entry; the player's camera falls back to the human's instead of
// CLIENT's NULL.
func TestRaceCameraNoEntryFallsBack(t *testing.T) {
	c := newCameraTestClient(10)
	if rc := c.playerRaceCamera(); rc == nil || rc.Race != 0 {
		t.Fatalf("race 10: want the human's camera, got %+v", rc)
	}
}
