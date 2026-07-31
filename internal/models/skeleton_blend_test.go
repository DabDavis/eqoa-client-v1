package models

import (
	"math"
	"testing"

	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

// lerpMat4 blends two bone local transforms for the 250 ms cross-fade. It used
// to lerp the sixteen matrix elements and repair the result with Gram-Schmidt,
// which is orthogonal and correctly scaled but lands on the wrong rotation: over
// 227,040 blends from real CHAR.ESF slot transitions it left the shortest arc by
// up to 45 degrees, and sat up to 77 degrees from a quaternion blend
// (esfviewer/posetest.html).
//
// These tests pin what the fix depends on: the blend stays ON the arc between
// the two rotations, at the position NLERP puts it.

const degTol = 1.0 // degrees

func quatMat(axis [3]float32, deg float32, scale [3]float32, pos [3]float32) mat4 {
	l := float32(math.Sqrt(float64(axis[0]*axis[0] + axis[1]*axis[1] + axis[2]*axis[2])))
	ax, ay, az := axis[0]/l, axis[1]/l, axis[2]/l
	h := float64(deg) * math.Pi / 360
	s := float32(math.Sin(h))
	m := composeTransform(ax*s, ay*s, az*s, float32(math.Cos(h)), pos[0], pos[1], pos[2], 1)
	applyFixScale(&m, scale)
	return m
}

// angleBetween returns the rotation angle between two transforms, in degrees.
func angleBetween(t *testing.T, a, b mat4) float32 {
	t.Helper()
	qa, _, _, oka := decomposeMat4(a)
	qb, _, _, okb := decomposeMat4(b)
	if !oka || !okb {
		t.Fatalf("decompose failed: %v %v", oka, okb)
	}
	d := qa[0]*qb[0] + qa[1]*qb[1] + qa[2]*qb[2] + qa[3]*qb[3]
	if d < 0 {
		d = -d
	}
	if d > 1 {
		d = 1
	}
	return float32(2 * math.Acos(float64(d)) * 180 / math.Pi)
}

// nlerpAngle is where NLERP lands, exactly: with the two rotations `sep` apart,
// the blended one sits this far from the first.
//
//	q(t) = normalize((1-t)*q0 + t*q1)
//	angle(q0, q(t)) = 2*atan2(t*sin(sep/2), (1-t) + t*cos(sep/2))
//
// This is NOT t*sep. NLERP follows the shortest arc but eases along it — slower
// near the ends, faster through the middle — deviating from a constant rate by
// up to ~7 degrees at 170 degrees of separation. That is the accepted trade the
// rest of the file already makes: lerpFrame blends keyframes the same way and
// says so ("SLERP approximated with NLERP for speed"). Being on the right arc is
// what matters; the old element-lerp was not.
func nlerpAngle(sep, t float64) float64 {
	h := sep * math.Pi / 360
	return 2 * math.Atan2(t*math.Sin(h), (1-t)+t*math.Cos(h)) * 180 / math.Pi
}

func TestLerpMat4TravelsTheShortestArc(t *testing.T) {
	axes := [][3]float32{{0, 1, 0}, {1, 0, 0}, {0, 0, 1}, {0.3, -0.8, 0.5}}
	// The old implementation was accurate below ~15 degrees and drifted badly
	// past 90, so the separations that matter are the wide ones.
	for _, sep := range []float32{10, 45, 90, 135, 170} {
		for _, axis := range axes {
			a := quatMat(axis, 0, [3]float32{1, 1, 1}, [3]float32{0, 0, 0})
			b := quatMat(axis, sep, [3]float32{1, 1, 1}, [3]float32{0, 0, 0})
			for _, frac := range []float32{0.25, 0.5, 0.75} {
				got := lerpMat4(a, b, frac)
				fromA := angleBetween(t, a, got)
				toB := angleBetween(t, got, b)
				// On the arc: the two legs must sum to the whole. This is the
				// assertion the old implementation failed — it left the arc
				// entirely, by a mean of 26 degrees past 135 degrees apart.
				if d := float64(fromA + toB - sep); math.Abs(d) > degTol {
					t.Errorf("axis %v sep %.0f t %.2f: off-arc, legs %.2f+%.2f=%.2f want %.0f",
						axis, sep, frac, fromA, toB, fromA+toB, sep)
				}
				// And at the position NLERP puts it.
				want := nlerpAngle(float64(sep), float64(frac))
				if d := float64(fromA) - want; math.Abs(d) > degTol {
					t.Errorf("axis %v sep %.0f t %.2f: travelled %.2f, want %.2f",
						axis, sep, frac, fromA, want)
				}
			}
		}
	}
}

func TestLerpMat4EndpointsAreExact(t *testing.T) {
	a := quatMat([3]float32{0, 1, 0}, 20, [3]float32{1, 1, 1}, [3]float32{1, 2, 3})
	b := quatMat([3]float32{1, 0, 0}, 100, [3]float32{1, 1, 1}, [3]float32{-4, 5, 6})
	for _, c := range []struct {
		t    float32
		want mat4
	}{{0, a}, {1, b}} {
		got := lerpMat4(a, b, c.t)
		for i := range got {
			if math.Abs(float64(got[i]-c.want[i])) > 1e-5 {
				t.Fatalf("t=%v element %d: got %v want %v", c.t, i, got[i], c.want[i])
			}
		}
	}
}

// FixScale gives DarkElfM (1.03, 1.10, 1.02) and GnomeM (1.06, 1, 1.10), so a
// blend that dropped per-axis scale would visibly resize those races mid-fade.
func TestLerpMat4PreservesPerAxisScaleAndTranslation(t *testing.T) {
	sa := [3]float32{1.03, 1.10, 1.02}
	sb := [3]float32{1.06, 1.00, 1.10}
	a := quatMat([3]float32{0, 1, 0}, 30, sa, [3]float32{1, 2, 3})
	b := quatMat([3]float32{0, 1, 0}, 120, sb, [3]float32{7, -1, 0.5})
	const frac = 0.4
	got := lerpMat4(a, b, frac)

	_, gs, gt, ok := decomposeMat4(got)
	if !ok {
		t.Fatal("blended transform did not decompose")
	}
	for i := 0; i < 3; i++ {
		want := (1-frac)*sa[i] + frac*sb[i]
		if math.Abs(float64(gs[i]-want)) > 1e-5 {
			t.Errorf("scale axis %d: got %v want %v", i, gs[i], want)
		}
	}
	wantT := [3]float32{
		(1-frac)*1 + frac*7,
		(1-frac)*2 + frac*-1,
		(1-frac)*3 + frac*0.5,
	}
	for i := 0; i < 3; i++ {
		if math.Abs(float64(gt[i]-wantT[i])) > 1e-5 {
			t.Errorf("translation axis %d: got %v want %v", i, gt[i], wantT[i])
		}
	}
}

// The result has to stay a similarity — orthogonal axes — or the mesh shears.
func TestLerpMat4StaysOrthogonal(t *testing.T) {
	a := quatMat([3]float32{0.2, 1, -0.4}, 15, [3]float32{1.03, 1.10, 1.02}, [3]float32{})
	b := quatMat([3]float32{-1, 0.3, 0.7}, 165, [3]float32{1, 1, 1}, [3]float32{})
	for f := float32(0); f <= 1.0001; f += 0.05 {
		m := lerpMat4(a, b, f)
		col := func(c int) [3]float32 { return [3]float32{m[c], m[4+c], m[8+c]} }
		norm := func(v [3]float32) float32 {
			return float32(math.Sqrt(float64(v[0]*v[0] + v[1]*v[1] + v[2]*v[2])))
		}
		dot := func(u, v [3]float32) float32 { return u[0]*v[0] + u[1]*v[1] + u[2]*v[2] }
		c0, c1, c2 := col(0), col(1), col(2)
		for _, p := range [][2][3]float32{{c0, c1}, {c0, c2}, {c1, c2}} {
			if cos := float32(math.Abs(float64(dot(p[0], p[1]) / (norm(p[0]) * norm(p[1]))))); cos > 1e-5 {
				t.Errorf("t=%.2f: axes not orthogonal, |cos| = %v", f, cos)
			}
		}
	}
}

// A mirrored bone must come back mirrored. Nothing on the disc is mirrored
// today, but the old cross-product step would have silently unmirrored one, and
// that is the kind of thing that only shows up on new art.
func TestLerpMat4KeepsHandedness(t *testing.T) {
	a := quatMat([3]float32{0, 1, 0}, 30, [3]float32{1, 1, -1}, [3]float32{})
	b := quatMat([3]float32{0, 1, 0}, 90, [3]float32{1, 1, -1}, [3]float32{})
	det := func(m mat4) float32 {
		return m[0]*(m[5]*m[10]-m[6]*m[9]) -
			m[1]*(m[4]*m[10]-m[6]*m[8]) +
			m[2]*(m[4]*m[9]-m[5]*m[8])
	}
	if det(a) >= 0 {
		t.Fatal("test setup: source should be mirrored")
	}
	if d := det(lerpMat4(a, b, 0.5)); d >= 0 {
		t.Errorf("blend unmirrored the frame: det = %v", d)
	}
}

// A Skeleton is shared by every entity using its ModelID, so the idle ground
// baseline must come from the idle animation and not from whichever animation
// the first such entity happened to be playing. On HumanM the difference is
// real: idle sits at −0.0209 from bind, walk at −0.1205 and death at −0.1557,
// so a corpse spawning first lifted every human male ~0.13 units — about 7% of
// body height — off the ground for the rest of the session.
func TestCaptureIdleBounceBeatsTheFirstFramePlayed(t *testing.T) {
	// Root at bind Y = 1.06, one animated track putting it at 1.0391.
	hier := &esf.HSpriteHierarchy{Nodes: []esf.BoneNode{{
		ParentID: -1, Quat: [4]float32{0, 0, 0, 1}, Scale: 1,
		Pos: esf.Point{X: 0, Y: 1.06, Z: 0}, FixScale: [3]float32{1, 1, 1},
	}}}
	mkAnim := func(y float32) *esf.HSpriteAnim {
		return &esf.HSpriteAnim{
			NumNodes: 1, NumFrames: 1, FPS: 10,
			Nodes: []esf.AnimNode{{RefID: 0, Frames: []esf.AnimPackFrame{{
				Quat: [4]int16{0, 0, 0, 32767}, Scale: 512,
				Pos: [3]int16{0, int16(y * 512), 0},
			}}}},
		}
	}
	const bind = 1.06
	idle, walk := mkAnim(1.0391), mkAnim(0.9395)

	s := NewSkeleton(hier, nil)
	s.CaptureIdleBounce(idle, nil)
	want := float32(1.0391 - bind)
	if math.Abs(float64(s.IdleBounce-want)) > 1e-3 {
		t.Fatalf("idle baseline: got %v want %v", s.IdleBounce, want)
	}

	// Posing something else afterwards must not move the baseline.
	s.ComputeAnimPoseLerp(walk, 0)
	if math.Abs(float64(s.IdleBounce-want)) > 1e-3 {
		t.Errorf("baseline drifted after posing walk: got %v want %v", s.IdleBounce, want)
	}
	// StepBounce, which is per-frame, must still track the pose being played.
	if wantStep := float32(0.9395 - bind); math.Abs(float64(s.StepBounce-wantStep)) > 1e-3 {
		t.Errorf("StepBounce: got %v want %v", s.StepBounce, wantStep)
	}

	// A model with no idle slot keeps the old first-frame fallback rather than
	// silently defaulting to zero, which would drop it through the floor.
	s2 := NewSkeleton(hier, nil)
	s2.CaptureIdleBounce(nil, nil)
	s2.ComputeAnimPoseLerp(walk, 0)
	if wantFallback := float32(0.9395 - bind); math.Abs(float64(s2.IdleBounce-wantFallback)) > 1e-3 {
		t.Errorf("fallback baseline: got %v want %v", s2.IdleBounce, wantFallback)
	}
}

// ComputeAnimPose and ComputeAnimPoseLerp used to skip the LOD gate that
// applyAnimNodes calls critical, so the entry points disagreed about whether a
// LODChannel < 0 bone may be animated. They now share one rule.
func TestAnimatesBoneGate(t *testing.T) {
	s := &Skeleton{BoneLOD: []int32{-1, 0, 1, 2, 3}}
	for _, c := range []struct {
		filter int32
		want   []bool
	}{
		{0, []bool{false, true, true, true, true}},
		{1, []bool{false, false, false, true, true}},
		{2, []bool{false, false, false, false, true}},
	} {
		s.ChannelFilter = c.filter
		for i, want := range c.want {
			if got := s.animatesBone(int32(i)); got != want {
				t.Errorf("filter %d bone %d (lod %d): got %v want %v",
					c.filter, i, s.BoneLOD[i], got, want)
			}
		}
	}
	// A bone past the end of the table is animated rather than dropped.
	if !s.animatesBone(99) {
		t.Error("bone outside BoneLOD should default to animated")
	}
}
