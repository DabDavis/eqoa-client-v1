package game

import (
	"math"
	"testing"

	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

// GlobalInit's leaf poses: a 0.2 × 0.2 card swinging from its pivot. At
// step 0 of swing 0 it is turned −0.4 about Z (RotateZ(−cos 0 · 0.4)).
func TestFallEffectPoses(t *testing.T) {
	edge := func(a, b [3]float32) float64 {
		return math.Sqrt(float64((a[0]-b[0])*(a[0]-b[0]) + (a[1]-b[1])*(a[1]-b[1]) + (a[2]-b[2])*(a[2]-b[2])))
	}
	for _, sw := range evFallEffect {
		for _, c := range sw {
			if math.Abs(edge(c[0], c[1])-0.2) > 1e-4 || math.Abs(edge(c[1], c[2])-0.2) > 1e-4 {
				t.Fatalf("leaf card %v is not 0.2 square", c)
			}
		}
	}
	c := evFallEffect[0][0]
	if tilt := math.Atan2(float64(c[1][1]-c[0][1]), float64(c[1][0]-c[0][0])); math.Abs(tilt+0.4) > 1e-4 {
		t.Fatalf("step 0 of swing 0 tilts %.4f about Z, want -0.4", tilt)
	}
	for _, sw := range evFallEffect {
		for _, pose := range sw {
			for _, v := range pose {
				if math.Abs(float64(v[1])) > 0.25 || math.Abs(float64(v[0])) > 0.4 {
					t.Fatalf("pose vertex %v strays from its pivot", v)
				}
			}
		}
	}
	if evFogCard[2][0] != [3]float32{-0.5, -0.5, 0} && math.Abs(float64(evFogCard[2][0][0]+0.5)) > 1e-6 {
		t.Fatalf("fog card 2 (a quarter turn): %v", evFogCard[2])
	}
}

// AllocParticles' counts: no cap, and a flock of (int)(density·8)+1.
func TestVolumeCounts(t *testing.T) {
	s := NewEffectVolumeSystem()
	for _, c := range []struct {
		typ  esf.EffectVolumeType
		size [3]float32
		dens float32
		want int
	}{
		{esf.EffectTypeGroundFog, [3]float32{50, 3.7, 50}, 0.15, 150},
		{esf.EffectTypeDustStorm, [3]float32{50, 5.1, 50}, 0.45, 450},
		{esf.EffectTypeFallingLeaves, [3]float32{50, 30.7, 50}, 0.08, 49},
		{esf.EffectTypeLightShafts, [3]float32{3.4, 20, 3.4}, 0.06, 1},
		{esf.EffectTypeFlock, [3]float32{50, 10.2, 50}, 0.5, 5},
	} {
		v := &EffectVolume{EffectType: c.typ, size: c.size, Density: c.dens}
		s.allocParticles(v)
		got := len(v.fog) + len(v.dust) + len(v.leaves) + len(v.shafts) + len(v.boids)
		if got != c.want {
			t.Errorf("%v %v at %.2f: %d, want %d", c.typ, c.size, c.dens, got, c.want)
		}
	}
}

// RenderLightShafts' sun schedule (flags & 2): 9:00 to 15:00, an hour's
// ramp either end, leaning from −1 to +1.
func TestShaftSchedule(t *testing.T) {
	for _, c := range []struct{ h, dn, lean float32 }{
		{8, 0, -1}, {9.5, 0.5, -0.8333333}, {12, 1, 0}, {14.5, 0.5, 0.8333333}, {16, 0, 1},
	} {
		dn, lean := shaftDayNight(c.h)
		if math.Abs(float64(dn-c.dn)) > 1e-5 || math.Abs(float64(lean-c.lean)) > 1e-5 {
			t.Errorf("%.1f h: dayNight %.3f lean %.3f, want %.3f %.3f", c.h, dn, lean, c.dn, c.lean)
		}
	}
}

// A dust mote crosses the box along local +X at 17..44 units a second for a
// 50-wide box at p28 = 0.3 (Tunaria's dust storms).
func TestDustSpeed(t *testing.T) {
	s := NewEffectVolumeSystem()
	v := &EffectVolume{EffectType: esf.EffectTypeDustStorm, size: [3]float32{50, 5.1, 50}, Density: 0.45, Intensity: 0.3, fade: 1, dayNight: 1}
	s.allocParticles(v)
	v.reseed = false
	s.processDustStorm(v, 0.01)
	for _, p := range v.dust {
		speed := (p.end[0] - p.pos[0]) / p.life
		if speed < 17 || speed > 45 || p.end[1] != p.pos[1] || p.end[2] != p.pos[2] {
			t.Fatalf("mote %+v: %.1f u/s", p, speed)
		}
	}
}

// A mote born at base 60 (rand() & 0x3C) is drawn on its first frame
// without running off the 60-card table.
func TestDustBaseSixty(t *testing.T) {
	s := NewEffectVolumeSystem()
	v := &EffectVolume{EffectType: esf.EffectTypeDustStorm, size: [3]float32{50, 5.1, 50}, Density: 0.45, Intensity: 0.3, fade: 1, dayNight: 1, scale: 1, rot: evRotateHPR(0, 0, 0)}
	s.allocParticles(v)
	s.processDustStorm(v, 0.01)
	sixty := false
	for _, p := range v.dust {
		sixty = sixty || p.frame == 60
	}
	if !sixty {
		t.Skip("no mote drew base 60 with this seed")
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("drawing a base-60 mote: %v", r)
		}
	}()
	for i := range v.dust {
		_ = dustCard(v.dust[i].frame)
	}
}
