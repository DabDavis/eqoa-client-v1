package audio

import (
	"math"
	"testing"
)

// The PS2's pan against the camera. Looking down +Z, this world has +X on the
// left (models face +Z with +X their left), so a source at +X pans -1.
func TestPanFollowsTheCamera(t *testing.T) {
	cam := [3]float32{0, 0, 0}
	ahead := [3]float32{0, 0, 1}
	cases := []struct {
		dir, src [3]float32
		want     float32
	}{
		{ahead, [3]float32{10, 0, 0}, -1},                // left
		{ahead, [3]float32{-10, 0, 0}, 1},                // right
		{ahead, [3]float32{0, 0, 10}, 0},                 // in front
		{ahead, [3]float32{0, 5, -10}, 0},                // behind, height ignored
		{[3]float32{1, 0, 0}, [3]float32{0, 0, -10}, -1}, // turned to +X: -Z is its left
		{ahead, [3]float32{10, 0, 10}, -float32(math.Sqrt2) / 2},
	}
	for _, c := range cases {
		if got := panFor(cam, c.dir, c.src); math.Abs(float64(got-c.want)) > 1e-5 {
			t.Errorf("looking %v, source at %v: pan %.3f, want %.3f", c.dir, c.src, got, c.want)
		}
	}
}
