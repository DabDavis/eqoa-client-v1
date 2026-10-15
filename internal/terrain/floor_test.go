package terrain

import "testing"

// A level quad at height y over x, z in [x0, x1] × [z0, z1], facing up under
// the PS2's strip order, or down when flipped.
func levelQuad(y, x0, x1, z0, z1 float32, down bool) []vec3 {
	if down {
		return []vec3{{x0, y, z0}, {x0, y, z1}, {x1, y, z0}, {x1, y, z1}}
	}
	return []vec3{{x0, y, z0}, {x1, y, z0}, {x0, y, z1}, {x1, y, z1}}
}

func TestLevelQuadFacing(t *testing.T) {
	up := stripZone(levelQuad(0, -5, 5, -5, 5, false))
	down := stripZone(levelQuad(0, -5, 5, -5, 5, true))
	for _, tile := range up.CollTiles {
		for _, tri := range tile.Triangles {
			if !tri.Walkable {
				t.Fatal("an up-facing floor is not walkable")
			}
		}
	}
	for _, tile := range down.CollTiles {
		for _, tri := range tile.Triangles {
			if tri.Walkable {
				t.Fatal("a ceiling counts as walkable ground")
			}
		}
	}
}

// CalcFit's drop finds a floor anywhere from the centre (feet + 1.2) to 0.6
// below the feet, and nothing outside that.
func TestFitFloorReach(t *testing.T) {
	for _, c := range []struct {
		floorY, want float32
	}{
		{0, 0},        // standing on it
		{1.0, 1.0},    // a step up under 1.2
		{1.3, -0.6},   // above the centre: not seen
		{-0.5, -0.5},  // a step down the probe reaches
		{-0.7, -0.6},  // out of reach: the probe's end
	} {
		zt := stripZone(levelQuad(c.floorY, -5, 5, -5, 5, false))
		floor, _ := zt.Fit(0, 0, 0)
		if !near(floor, c.want+probeRadius) && !near(floor, c.want) {
			t.Errorf("floor at %.2f: Fit found %.3f, want %.3f", c.floorY, floor, c.want)
		}
	}
}

// The rise meets a ceiling, and a ceiling is never stood on.
func TestFitHeadroom(t *testing.T) {
	zt := stripZone(levelQuad(0, -5, 5, -5, 5, false), levelQuad(1.7, -5, 5, -5, 5, true))
	floor, head := zt.Fit(0, 0, 0)
	if head-floor >= floorDrop {
		t.Fatalf("a ceiling 1.7 up: headroom %.3f, want under %.1f", head-floor, floorDrop)
	}
	zt = stripZone(levelQuad(0, -5, 5, -5, 5, false), levelQuad(2.5, -5, 5, -5, 5, true))
	floor, head = zt.Fit(0, 0, 0)
	if head-floor < floorDrop {
		t.Fatalf("a ceiling 2.5 up: headroom %.3f, want at least %.1f", head-floor, floorDrop)
	}
	// Feet at 1.5 under a ceiling at 1.0: the drop passes through its back.
	zt = stripZone(levelQuad(0, -5, 5, -5, 5, false), levelQuad(1.0, -5, 5, -5, 5, true))
	if floor, _ = zt.Fit(0, 0.5, 0); !near(floor, probeRadius) && !near(floor, 0) {
		t.Fatalf("over a ceiling's back: floor %.3f, want the ground at 0", floor)
	}
	if y, ok := zt.SampleHeight(0, 0, 0.5); !ok || y != 0 {
		t.Fatalf("SampleHeight over a ceiling's back: %.3f %v, want the ground at 0", y, ok)
	}
}
