package audio

import (
	"math"
	"testing"
)

// A clip shaped like the disc's loops: silent block 0, 06 on block 1, 02
// through the body, 03 to end the loop, a trailing 07.
func loopVAG(blocks int) []byte {
	v := make([]byte, blocks*16)
	for b := 1; b < blocks; b++ {
		v[b*16+1] = 2
	}
	v[1*16+1] = 6
	v[(blocks-2)*16+1] = 3
	v[(blocks-1)*16+1] = 7
	return v
}

func TestVAGLoopStart(t *testing.T) {
	v := loopVAG(10)
	if got := vagLoopStart(v); got != 28 {
		t.Fatalf("loop start %d, want 28 (block 1)", got)
	}
	if n := len(DecodeVAG(v)); n != 9*28 {
		t.Fatalf("decoded %d samples, want 9 blocks' worth (the 07 block is not played)", n)
	}
	oneShot := make([]byte, 5*16)
	oneShot[4*16+1] = 1
	if got := vagLoopStart(oneShot); got != 0 {
		t.Fatalf("one-shot loop start %d, want 0", got)
	}
}

// VICSprite::CalcVolumeAndPan: 1 − distance·0.02, in 3D, clamped.
func TestCalcVolumeAndPan(t *testing.T) {
	cam := [3]float32{0, 0, 0}
	dir := [3]float32{0, 0, 1}
	for _, c := range []struct {
		at   [3]float32
		want float32
	}{
		{[3]float32{0, 0, 0}, 1},
		{[3]float32{0, 0, 25}, 0.5},
		{[3]float32{0, 30, 40}, 0}, // 50 away, up and out: 3D, not ground distance
		{[3]float32{0, 0, 80}, 0},  // the old 80-unit range is silent here
	} {
		if v, _ := calcVolumeAndPan(cam, dir, c.at); math.Abs(float64(v-c.want)) > 1e-6 {
			t.Errorf("volume at %v = %v, want %v", c.at, v, c.want)
		}
	}
	if _, pan := calcVolumeAndPan(cam, dir, [3]float32{0, 0, 80}); pan != 0 {
		t.Errorf("pan out of earshot = %v, want 0", pan)
	}
}

func has(ids ...int32) func(int32) bool {
	return func(id int32) bool {
		for _, i := range ids {
			if i == id {
				return true
			}
		}
		return false
	}
}

// SetSoundTrack: the first track plays at once at full volume; a change waits
// out 15 s since the last; then the old fades out and the new in over 15 s.
func TestAmbientMix(t *testing.T) {
	var m ambientMix
	ready := has(10, 20)

	act, _ := m.Step(10, ready, 0, 0, 1, false)
	if !act[0].start || act[0].track != 10 || act[0].vol != 1 {
		t.Fatalf("first track: %+v, want 10 started at full volume", act[0])
	}
	// A change 5 s later is held off.
	act, moved := m.Step(20, ready, 5000, 0, 1, false)
	if moved || act[0].start || act[0].vol != 1 {
		t.Fatalf("change inside the hold-off: moved=%v %+v", moved, act[0])
	}
	// Past 15 s it moves: 10 becomes slot 1 and fades out, 20 fades in.
	act, moved = m.Step(20, ready, 15001, 0, 1, false)
	if !moved || !act[0].start || act[0].track != 20 || act[0].vol != 0 {
		t.Fatalf("change: moved=%v slot0 %+v, want 20 started at 0", moved, act[0])
	}
	if act[1].vol != 1 {
		t.Fatalf("change: slot1 %+v, want the old track still at 1", act[1])
	}
	act, _ = m.Step(20, ready, 15001+7500, 0, 1, false)
	if math.Abs(float64(act[0].vol-0.5)) > 1e-3 || math.Abs(float64(act[1].vol-0.5)) > 1e-3 {
		t.Fatalf("half way: in %v out %v, want 0.5 each", act[0].vol, act[1].vol)
	}
	act, _ = m.Step(20, ready, 15001+15000, 0, 1, false)
	if act[0].vol != 1 || !act[1].stop {
		t.Fatalf("faded: in %v, out retired=%v", act[0].vol, act[1].stop)
	}
	// Rain cubes in, outdoors scales, a liquid mutes.
	act, _ = m.Step(20, ready, 40000, 0.5, 0.8, false)
	if math.Abs(float64(act[0].vol-0.1)) > 1e-6 {
		t.Fatalf("rain .5, outdoors .8: %v, want 0.125·0.8 = 0.1", act[0].vol)
	}
	act, _ = m.Step(20, ready, 40000, 0, 1, true)
	if act[0].vol != 0 {
		t.Fatalf("in a liquid: %v, want 0", act[0].vol)
	}
}

// Silence (track 0) is a track too: a zone of silence fades the music out.
func TestAmbientMixToSilence(t *testing.T) {
	var m ambientMix
	ready := has(10)
	m.Step(10, ready, 0, 0, 1, false)
	act, moved := m.Step(0, ready, 20000, 0, 1, false)
	if !moved || act[0].start || act[1].vol != 1 {
		t.Fatalf("to silence: moved=%v %+v %+v", moved, act[0], act[1])
	}
	act, _ = m.Step(0, ready, 35000, 0, 1, false)
	if !act[1].stop {
		t.Fatalf("silence after the fade: %+v, want the old track retired", act[1])
	}
}

func zoneAt(pos [3]float32, heading, scale float32, pri int32, path string) BGMZone {
	return BGMZone{
		MinX: -10, MinY: -10, MinZ: -2, MaxX: 10, MaxY: 10, MaxZ: 2,
		Path: path, Priority: pri, Loop: true,
		Rows: actorRows(heading, 0, 0), Pos: pos, Scale: scale,
	}
}

// QueryEmitters tests the camera in the actor's own space: the box turns and
// scales with its actor.
func TestBGMZoneTransform(t *testing.T) {
	// A 20 × 4 box (local x by z), turned a quarter about Y and doubled: in
	// the world it runs 8 along x and 40 along z.
	z := []BGMZone{zoneAt([3]float32{100, 0, 100}, math.Pi/2, 2, 0, "A")}
	if got := CheckBGMZone(z, 100, 0, 115); got != "A" {
		t.Fatalf("15 along world z (inside once turned and scaled): %q", got)
	}
	if got := CheckBGMZone(z, 110, 0, 100); got != "" {
		t.Fatalf("10 along world x (outside once turned): %q", got)
	}
	// Negative control: the old world box (position added, no turn, no
	// scale) gets both wrong.
	old := func(x, z float32) bool {
		return x >= 100-10 && x <= 100+10 && z >= 100-2 && z <= 100+2
	}
	if old(100, 115) || !old(110, 100) {
		t.Fatal("negative control: the old box should miss the first and take the second")
	}
}

// Priority first, then the nearest box centre.
func TestBGMZonePriority(t *testing.T) {
	z := []BGMZone{
		zoneAt([3]float32{0, 0, 0}, 0, 1, 1, "high"),
		zoneAt([3]float32{1, 0, 0}, 0, 1, 0, "low-near"),
	}
	if got := CheckBGMZone(z, 1, 0, 0); got != "high" {
		t.Fatalf("priority: %q, want high", got)
	}
	z = []BGMZone{
		zoneAt([3]float32{0, 0, 0}, 0, 1, 0, "far"),
		zoneAt([3]float32{5, 0, 0}, 0, 1, 0, "near"),
		zoneAt([3]float32{-5, 0, 0}, 0, 1, 0, "other"),
	}
	if got := CheckBGMZone(z, 4, 0, 0); got != "near" {
		t.Fatalf("equal priority: %q, want the nearest centre", got)
	}
}
