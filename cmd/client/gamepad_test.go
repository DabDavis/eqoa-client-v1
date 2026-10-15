package main

import "testing"

// VIPad::ScaleStick: a dead zone of 88..167 and a ramp from its edge.
func TestScaleStick(t *testing.T) {
	for _, c := range []struct {
		in   uint8
		want float32
	}{
		{0, -1}, {87, -0.0113636364}, {88, 0}, {128, 0}, {167, 0},
		{168, 0.0113636364}, {255, 1},
	} {
		if got := scaleStick(c.in); got != c.want {
			t.Errorf("scaleStick(%d) = %v, want %v", c.in, got, c.want)
		}
	}
}
