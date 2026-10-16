package terrain

import "testing"

// A creature is set down on a floor within 1.5 above or below it, and a
// floor farther off, or a ceiling, leaves it where it was (CLIENT
// 0x00CBCE40).
func TestCreatureFloorReach(t *testing.T) {
	for _, c := range []struct {
		floorY, y float32
		hit       bool
	}{
		{0, 0, true},     // on it
		{0, 1.4, true},   // 1.4 above: dropped onto it
		{0, -1.4, true},  // sunk 1.4 into it: raised onto it
		{0, 1.7, false},  // 1.6 + the sphere: out of reach
		{0, -1.7, false}, // below it by more than the reach
		{10, 0, false},   // far overhead
	} {
		zt := stripZone(levelQuad(c.floorY, -5, 5, -5, 5, false))
		y, ok := zt.CreatureFloor(0, c.y, 0)
		if ok != c.hit {
			t.Errorf("floor %.1f, creature at %.1f: hit %v, want %v", c.floorY, c.y, ok, c.hit)
			continue
		}
		want := c.y
		if c.hit {
			want = c.floorY
		}
		if !near(y, want) {
			t.Errorf("floor %.1f, creature at %.1f: set at %.3f, want %.3f", c.floorY, c.y, y, want)
		}
	}
	// A ceiling 1 above is no floor: the sweep comes down onto its back.
	zt := stripZone(levelQuad(1, -5, 5, -5, 5, true))
	if _, ok := zt.CreatureFloor(0, 0, 0); ok {
		t.Error("a ceiling 1 above set the creature down on it")
	}
}
