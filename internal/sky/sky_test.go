package sky

import (
	"math"
	"math/rand"
	"testing"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

func testSky() *Sky {
	base := &esf.Surface{}
	s := &Sky{}
	s.skyTex = [3]*esf.Surface{{}, base, {}}
	s.domes[0] = &dome{layers: []skyLayer{
		{surf: base, blend: 0, alpha: 255, uvMul: [2]float32{1, 1}, rate: [2]float32{0.009, 0}},
		{surf: base, blend: 3, alpha: 64, uvMul: [2]float32{3, 3}, rate: [2]float32{0.05, 0}},
	}}
	return s
}

// ProcessTransitions' dome material: three layers in the first half (the new
// texture fading in over the old base, the clouds fading out), two in the
// second (the clouds back on the new texture), and the new texture on both
// at 255 and 64 when it completes.
func TestDomeTransition(t *testing.T) {
	s := testSky()
	d := s.domes[0]
	dark := s.skyTex[SkyTexDark]
	step := func(tr float32) {
		s.atm = Atmosphere{Transition: tr, NextSky: SkyTexDark}
		s.applyTransition(d)
	}
	step(0.25)
	if len(d.layers) != 3 || d.layers[1].surf != dark || d.layers[0].blend != 3 ||
		d.layers[1].alpha != 127 || d.layers[2].alpha != 32 || d.layers[2].uvMul[0] != 3 {
		t.Fatalf("t=0.25: %+v", d.layers)
	}
	step(0.75)
	if len(d.layers) != 2 || d.layers[0].surf != dark || d.layers[1].surf != dark ||
		d.layers[1].alpha != 32 || d.layers[1].uvMul[0] != 3 {
		t.Fatalf("t=0.75: %+v", d.layers)
	}
	step(1)
	if d.layers[0].alpha != 255 || d.layers[1].alpha != 64 {
		t.Fatalf("t=1: %+v", d.layers)
	}
	// An instant change (0xC7) goes straight to the end.
	s = testSky()
	d = s.domes[0]
	dark = s.skyTex[SkyTexDark]
	step(1)
	if len(d.layers) != 2 || d.layers[0].surf != dark || d.layers[1].surf != dark {
		t.Fatalf("instant: %+v", d.layers)
	}
}

// The 128 stars sit 100 out in the upper hemisphere (Init's pitch and roll
// stay within ±0.49π).
func TestStarField(t *testing.T) {
	s := &Sky{rng: rand.New(rand.NewSource(1))}
	s.initStars()
	for i, st := range s.stars {
		r := math.Sqrt(float64(st.pos.X*st.pos.X + st.pos.Y*st.pos.Y + st.pos.Z*st.pos.Z))
		if math.Abs(r-100) > 0.01 || st.pos.Y <= 0 || st.size < 0.3 || st.size > 0.8 {
			t.Fatalf("star %d: %+v (r %.2f)", i, st, r)
		}
	}
	if math.Abs(float64(s.twinkle[0])-0.7) > 1e-6 || math.Abs(float64(s.twinkle[8])-0.9) > 1e-6 {
		t.Fatalf("twinkle table %v", s.twinkle)
	}
}

// The sun turns 15° an hour 35 out: at +X on the horizon at 6:00, overhead
// at noon, under the ground at midnight — and the moon opposite.
func TestSunPath(t *testing.T) {
	for _, c := range []struct {
		h          float64
		sx, sy, my float32
	}{{6, 35, 0, 0}, {12, 0, 35, -35}, {0, 0, -35, 35}, {18, -35, 0, 0}} {
		w := c.h * 0.2617993
		sw, cw := math.Sincos(w)
		sun := rowRoll(sw, cw, 1, float32(sw)*35, float32(-cw)*35, rl.Vector3{})
		moon := rowRoll(-sw, -cw, 1, float32(-sw)*35, float32(cw)*35, rl.Vector3{})
		if math.Abs(float64(sun.M12-c.sx)) > 1e-3 || math.Abs(float64(sun.M13-c.sy)) > 1e-3 || math.Abs(float64(moon.M13-c.my)) > 1e-3 {
			t.Errorf("%02.0f:00: sun (%.2f, %.2f), moon y %.2f", c.h, sun.M12, sun.M13, moon.M13)
		}
	}
}
