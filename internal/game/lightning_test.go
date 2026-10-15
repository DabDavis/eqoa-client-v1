package game

import (
	"testing"
)

// The bolt keeps its source fixed, stays near the line between its ends, and
// its charge falls at 5 a second once released.
//
// "Near": each harmonic moves a point by at most its amplitude on each of the
// two axes across the bolt (a bend sets a segment's ends to a sample and
// interpolates between them), so a point is at most 0.57 + 0.15 + 0.08 = 0.8
// off on each axis, and offset² is at most 2 × 0.8². A ceiling of 1 failed
// about one run in 40 on a legitimate bolt.
func TestBoltShapeAndDecay(t *testing.T) {
	const amp = 0.57 + 0.15 + 0.08
	const maxOffSq = 2 * amp * amp
	lb := NewLightningBolt(0, 0, 0, 10, 0, 0, [3]float32{1, 1, 1})
	lb.SetMaxBranchCount(3)
	maxOff := float32(0)
	branches := 0
	for f := 0; f < 300; f++ {
		lb.Flash()
		lb.Update(1.0 / 30)
		if lb.bolts[0].points[0] != (lvec{0, 0, 0}) {
			t.Fatalf("source moved: %v", lb.bolts[0].points[0])
		}
		for _, p := range lb.bolts[0].points {
			off := p[1]*p[1] + p[2]*p[2]
			maxOff = max(maxOff, off)
		}
		branches = max(branches, lb.boltCount-1)
	}
	t.Logf("max offset² %.3f, most branches %d", maxOff, branches)
	if maxOff > maxOffSq || branches != 3 {
		t.Fatalf("offset² %.3f (want at most %.2f), branches %d (want 3)", maxOff, maxOffSq, branches)
	}
	frames := 0
	for lb.Alive() && frames < 100 {
		lb.Update(1.0 / 30)
		frames++
	}
	if frames < 5 || frames > 8 {
		t.Fatalf("released at charge 1, it lived %d frames of 1/30 s; want about 0.2 s", frames)
	}
}
