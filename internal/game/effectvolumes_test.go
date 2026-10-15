package game

import (
	"math"
	"testing"
)

// The hour gain over the windows Tunaria actually uses: [0,0] (always),
// [18, 7.75] (110 night fog volumes) and [5.75, 20] (50 daytime shafts).
func TestVolumeDayNight(t *testing.T) {
	cases := []struct {
		t0, t1, h, want float32
	}{
		{0, 0, 3, 1},
		{0, 0, 12, 1},
		{18, 7.75, 20, 1},
		{18, 7.75, 3, 1},
		{18, 7.75, 7.75, 1},
		{18, 7.75, 12, 0},
		{18, 7.75, 18 - 2.5/60, 0.5},   // half way up the five-minute ramp
		{18, 7.75, 7.75 + 2.5/60, 0.5}, // half way down
		{18, 7.75, 7.75 + 6.0/60, 0},
		{5.75, 20, 12, 1},
		{5.75, 20, 22, 0},
		{5.75, 20, 3, 0},
		{5.75, 20, 5.75 - 1.0/60, 0.8},
		{0, 24, 3, 1}, // 23.75 h or wider: always
		{24, 0, 3, 0},
	}
	for _, c := range cases {
		got := volumeDayNight(c.t0, c.t1, c.h)
		if math.Abs(float64(got-c.want)) > 1e-3 {
			t.Errorf("window [%.2f, %.2f] at %.4f h: %.3f, want %.3f", c.t0, c.t1, c.h, got, c.want)
		}
	}
}

// The distance gain: full within 0.75 of the range, gone past it.
func TestVolumeFade(t *testing.T) {
	o := [3]float32{100, 0, 100}
	for _, c := range []struct{ d, want float32 }{{0, 1}, {50, 1}, {75, 1}, {87.5, 0.5}, {100, 0}, {150, 0}} {
		if got := volumeFade(o, [3]float32{100 + c.d, 0, 100}, 100); math.Abs(float64(got-c.want)) > 1e-4 {
			t.Errorf("at %.1f of 100: %.3f, want %.3f", c.d, got, c.want)
		}
	}
}
