package terrain

import "testing"

// liquidBelow is a zone whose tree holds env below y = 10 and air above.
func liquidBelow(env uint32) *ZoneTerrain {
	return &ZoneTerrain{Tree: &ZoneTree{
		Nodes:  []bspNode{{NY: 1, Dist: -10, FrontChild: bspLeafFlag | 0, BackChild: bspLeafFlag | 1}},
		Leaves: []bspLeaf{{EnvFlags: 0}, {EnvFlags: env}},
	}}
}

// Move probes the liquid at the sphere centre, feet + 1.2, and its surface
// within 0.1 of it: water only counts once it reaches the centre.
func TestProbeLiquidWater(t *testing.T) {
	zt := liquidBelow(EnvFlagWater)
	cases := []struct {
		feet  float32
		flags uint32
		state WaterState
	}{
		{9.5, 0, WaterNone},                                 // ankle deep: walking
		{8.95, 0, WaterNone},                                // centre 0.15 above: still dry
		{8.85, LiquidAtWater, WaterSurface},                 // centre just above the surface
		{8.75, LiquidInWater | LiquidAtWater, WaterSurface}, // centre just below it
		{8.0, LiquidInWater, WaterUnder},                    // submerged
	}
	for _, c := range cases {
		f := zt.ProbeLiquid(0, c.feet, 0)
		if f != c.flags || WaterStateOf(f) != c.state {
			t.Errorf("feet at %.2f: flags %#x state %d, want %#x state %d", c.feet, f, WaterStateOf(f), c.flags, c.state)
		}
	}
}

// Mud has its own bit at the feet, which is what slows a run on land.
func TestProbeLiquidMud(t *testing.T) {
	zt := liquidBelow(EnvFlagMud)
	if f := zt.ProbeLiquid(0, 9.5, 0); f != LiquidFeetInMud {
		t.Fatalf("feet in mud, centre dry: %#x, want %#x", f, LiquidFeetInMud)
	}
	if f := zt.ProbeLiquid(0, 8, 0); f&LiquidMud == 0 || f&LiquidFeetInMud == 0 {
		t.Fatalf("in mud to the centre: %#x", f)
	}
	if f := liquidBelow(EnvFlagLava).ProbeLiquid(0, 8, 0); f != LiquidInLava {
		t.Fatalf("in lava: %#x, want %#x", f, LiquidInLava)
	}
}
