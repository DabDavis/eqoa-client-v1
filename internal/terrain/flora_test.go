package terrain

import (
	"math"
	"os"
	"testing"

	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

// newlib's rand(): srand(1) then rand() is the LCG's first high word.
func TestNewlibRand(t *testing.T) {
	var r newlibRand
	r.srand(1)
	if got := r.rand(); got != 1481765933 {
		t.Fatalf("srand(1); rand() = %d, want 1481765933", got)
	}
	// A negative seed (sub-cells run -25..24) is taken as unsigned.
	r.srand(-2525)
	a := r.rand()
	r.srand(-2525)
	if b := r.rand(); a != b || a < 0 {
		t.Fatalf("srand(-2525) is not repeatable or not positive: %d %d", a, b)
	}
}

// CalcRadialLoc truncates toward zero and then steps a negative offset down,
// so an exact negative multiple of 40 lands one sub-cell further out; the
// sub-cell clamps to [-25, 24].
func TestCalcRadialLoc(t *testing.T) {
	fr := NewFloraRenderer()
	g := floraGrid{rows: 10, cols: 10, cell: 2000, origin: [3]float32{1000, 0, 1000}}
	for _, c := range []struct {
		x    float32
		sub1 int
	}{
		{1000, 0}, {1039, 0}, {1040, 1}, {999, -1}, {960, -2}, {961, -1}, {1, -25},
	} {
		if loc := fr.calcRadialLoc(g, c.x, 1000); loc.sub[1] != c.sub1 || loc.cell != [2]int{0, 0} {
			t.Errorf("x=%v: cell %v sub %v, want sub[1] %d", c.x, loc.cell, loc.sub, c.sub1)
		}
	}
	// The column truncates at multiples of the cell size, wherever the origin
	// puts the centre: x = 2500 is column 1, 500 short of its centre.
	if loc := fr.calcRadialLoc(g, 2500, 1000); loc.cell[1] != 1 || loc.sub[1] != -13 {
		t.Errorf("x=2500: cell %v sub %v, want column 1 sub -13", loc.cell, loc.sub)
	}
	g.origin = [3]float32{}
	if loc := fr.calcRadialLoc(g, 1999, 0); loc.sub[1] != 24 {
		t.Errorf("x=1999 from a corner origin: sub %v, want the clamp 24", loc.sub)
	}
}

// floraQuads is a zone of level, up-facing quads (the PS2's strip order),
// each n×n units over [x0, x0+w) × [z0, z0+w), carrying flora set `set`, at
// height y(x, z).
func floraQuads(x0, z0, w, n float32, set int, y func(x, z float32) float32) *ZoneTerrain {
	zt := &ZoneTerrain{CollTiles: map[TileKey]*CollTile{}}
	for z := z0; z < z0+w; z += n {
		for x := x0; x < x0+w; x += n {
			// PS2 strip (x,z), (x+n,z), (x,z+n), (x+n,z+n); the loader stores
			// each triangle with its first two vertices swapped.
			v := [4][3]float32{{x, y(x, z), z}, {x + n, y(x+n, z), z}, {x, y(x, z+n), z + n}, {x + n, y(x+n, z+n), z + n}}
			for i := 0; i < 2; i++ {
				a, b, c := v[i+1], v[i], v[i+2]
				if i == 1 {
					a, b = v[i], v[i+1]
				}
				addCollTriToZone(zt, CollTriangle{
					X0: a[0], Y0: a[1], Z0: a[2], X1: b[0], Y1: b[1], Z1: b[2], X2: c[0], Y2: c[1], Z2: c[2],
					FloraType: set, Room: true,
				})
			}
		}
	}
	return zt
}

func flat(x, z float32) float32 { return 0 }

func oneSet(weight, base, variance float32, flags uint32) *esf.ZoneRadialFlora {
	rf := &esf.ZoneRadialFlora{
		Models: []esf.RadialFloraModel{{Mesh: &esf.FloraMesh{}, SphereR: 1}},
		Dists:  []esf.RadialFloraDist{{Flags: flags, Model: 0, Weight: weight, BaseScale: base, ScaleVar: variance}},
		Sets:   []esf.RadialFloraSet{{First: 0, Count: 1}},
	}
	rf.Precalc()
	return rf
}

// The swatch over [0, 40)²: one cell, so the sub-cell is (0, 0).
func swatchAt0(fr *FloraRenderer) *floraSwatch {
	return fr.createSwatch(floraGrid{rows: 1, cols: 1, cell: 1, minY: -100, maxY: 100}, floraLoc{})
}

// (int)(area × summed weight) plants a triangle: two 800-unit halves of the
// swatch at 0.05 grow 40 each.
func TestFloraCountIsAreaTimesWeight(t *testing.T) {
	fr := NewFloraRenderer()
	fr.zt = floraQuads(0, 0, 40, 40, 0, flat)
	fr.zt.RadialFlora = oneSet(0.05, 1, 0.2, 0)
	s := swatchAt0(fr)
	if len(s.insts) != 80 {
		t.Fatalf("%d plants, want 80", len(s.insts))
	}
	for _, in := range s.insts {
		sc := float32(math.Sqrt(float64(in.world.M0*in.world.M0 + in.world.M1*in.world.M1 + in.world.M2*in.world.M2)))
		if sc < 0.8-1e-4 || sc > 1.2+1e-4 {
			t.Fatalf("scale %v outside base·(1 ± var) = [0.8, 1.2]", sc)
		}
		if in.pos[0] < 0 || in.pos[0] > 40 || in.pos[2] < 0 || in.pos[2] > 40 || in.pos[1] != 0 {
			t.Fatalf("plant at %v, outside the swatch's ground", in.pos)
		}
	}
	// The same sub-cell grows the same plants.
	again := swatchAt0(fr)
	if len(again.insts) != len(s.insts) || again.insts[7].pos != s.insts[7].pos {
		t.Fatal("a swatch is not stable: the sub-cell seed does not fix its plants")
	}
}

// Ground the swatch does not cover is clipped away; the 500-plant cap holds.
func TestFloraClipAndInstanceCap(t *testing.T) {
	fr := NewFloraRenderer()
	fr.zt = floraQuads(-40, -40, 120, 120, 0, flat) // one quad over 3×3 swatches
	fr.zt.RadialFlora = oneSet(0.05, 1, 0, 0)
	if s := swatchAt0(fr); len(s.insts) < 75 || len(s.insts) > 80 {
		t.Fatalf("%d plants on the clipped 40² piece, want about 80", len(s.insts))
	}
	fr.zt.RadialFlora = oneSet(1, 1, 0, 0)
	if s := swatchAt0(fr); len(s.insts) != floraInstCap {
		t.Fatalf("%d plants at weight 1, want the cap %d", len(s.insts), floraInstCap)
	}
}

// The query keeps 100 triangles: a swatch of 800 small ones grows plants on
// the first 100 only.
func TestFloraTriangleCap(t *testing.T) {
	fr := NewFloraRenderer()
	fr.zt = floraQuads(0, 0, 40, 2, 0, flat) // 400 quads, 800 triangles of area 2
	fr.zt.RadialFlora = oneSet(1, 1, 0, 0)   // 2 plants each
	s := swatchAt0(fr)
	if fr.lastTris != floraTriCap || len(s.insts) != 2*floraTriCap {
		t.Fatalf("%d query triangles, %d plants; want %d and %d", fr.lastTris, len(s.insts), floraTriCap, 2*floraTriCap)
	}
}

// Within 30° of up grows; steeper does not; flags bit 0 stands the plant on
// the slope.
func TestFloraSlope(t *testing.T) {
	slope := func(deg float64) func(x, z float32) float32 {
		k := float32(math.Tan(deg * math.Pi / 180))
		return func(x, z float32) float32 { return x * k }
	}
	fr := NewFloraRenderer()
	fr.zt = floraQuads(0, 0, 40, 40, 0, slope(40))
	fr.zt.RadialFlora = oneSet(0.05, 1, 0, 1)
	if s := swatchAt0(fr); len(s.insts) != 0 {
		t.Fatalf("a 40° slope grew %d plants", len(s.insts))
	}
	fr.zt = floraQuads(0, 0, 40, 40, 0, slope(20))
	fr.zt.RadialFlora = oneSet(0.05, 1, 0, 1)
	s := swatchAt0(fr)
	if len(s.insts) == 0 {
		t.Fatal("a 20° slope grew nothing")
	}
	n := [3]float32{-float32(math.Sin(20 * math.Pi / 180)), float32(math.Cos(20 * math.Pi / 180)), 0}
	for _, in := range s.insts {
		up := [3]float32{in.world.M4, in.world.M5, in.world.M6} // the plant's +Y
		if d := dot3(up, n) / length3(up); d < 0.999 {
			t.Fatalf("a tilted plant's up is %v, %.4f off the slope normal %v", up, d, n)
		}
	}
	fr.zt.RadialFlora = oneSet(0.05, 1, 0, 0)
	for _, in := range swatchAt0(fr).insts {
		if in.world.M5 != 1 {
			t.Fatalf("an untilted plant leans: up %v", [3]float32{in.world.M4, in.world.M5, in.world.M6})
		}
	}
}

// Moving one sub-cell keeps the six swatches still in the grid, in their new
// slots, and grows three.
func TestFloraSwatchesSlide(t *testing.T) {
	fr := NewFloraRenderer()
	zt := floraQuads(-200, -200, 400, 40, 0, flat)
	zt.RadialFlora = oneSet(0.01, 1, 0, 0)
	fr.Update(20, 20, zt)
	before := fr.swatches
	fr.Update(60, 20, zt) // +1 sub-column
	kept := 0
	for i, s := range fr.swatches {
		if s == nil {
			t.Fatalf("slot %d empty after the move", i)
		}
		for j, b := range before {
			if s == b {
				kept++
				if j != i+1 {
					t.Fatalf("swatch moved from slot %d to %d, want %d", j, i, j-1)
				}
			}
		}
	}
	if kept != 6 {
		t.Fatalf("%d swatches kept, want 6", kept)
	}
}

// The shipped data: plants per swatch at Freeport and on open grass, against
// the old scatter's expectation over the same 40² (×0.07 density, sets keyed
// by the dist's flags word).
func TestFloraISOCounts(t *testing.T) {
	iso := "/home/sdg/claude-eqoa/EverQuest - Online Adventures - Frontiers (USA).iso"
	if _, err := os.Stat(iso); err != nil || os.Getenv("FLORA_ISO") == "" {
		t.Skip("set FLORA_ISO=1 with the ISO present")
	}
	f, err := esf.OpenISOFile(iso, "TUNARIA.ESF")
	if err != nil {
		t.Fatal(err)
	}
	l, err := NewLoaderFromFile(f)
	if err != nil {
		t.Fatal(err)
	}
	h := l.World.Header
	t.Logf("world grid: %d×%d cells of %.0f, origin (%.0f, %.0f), %d zones", h.NumRows, h.NumCols, h.CellSize, h.Origin.X, h.Origin.Z, h.NumZones)
	l.loaded = map[int]*loadedZone{}
	load := func(x, z float32) int {
		idx := l.FindZone(x, z)
		if idx < 0 {
			return idx
		}
		if _, ok := l.loaded[idx]; !ok {
			zt, err := l.loadZone(idx)
			if err != nil {
				t.Fatal(err)
			}
			l.loaded[idx] = &loadedZone{proxyIdx: idx, terrain: zt, ready: true}
		}
		return idx
	}
	fr := NewFloraRenderer()
	fr.loader = l
	g := fr.grid()

	for _, spot := range []struct {
		name string
		x, z float32
	}{{"Freeport", 25305.6, 15748.4}, {"east of Freeport", 26500, 14500}, {"west of Freeport", 23000, 15000}, {"Qeynos hills", 4500, 4000}} {
		idx := load(spot.x, spot.z)
		zt := l.GetZoneTerrain(idx)
		if zt == nil {
			t.Logf("%s: no zone", spot.name)
			continue
		}
		if zt.RadialFlora == nil {
			t.Logf("%s: zone %d has no flora", spot.name, idx)
			continue
		}
		// Every swatch of the zone's bbox; report the spot's and the zone's best.
		bb := l.proxies.Zones[idx].BBox
		var n, newSum, oldSum, best int
		var bestAt [2]float32
		for z := bb.MinZ; z < bb.MaxZ; z += floraSubCell {
			for x := bb.MinX; x < bb.MaxX; x += floraSubCell {
				loc := fr.calcRadialLoc(g, x, z)
				if loc.zone != idx {
					continue
				}
				s := fr.createSwatch(g, loc)
				n++
				newSum += len(s.insts)
				oldSum += int(oldFloraExpected(zt, s.min, s.max))
				if len(s.insts) > best {
					best, bestAt = len(s.insts), [2]float32{x + 20, z + 20}
				}
			}
		}
		here := fr.createSwatch(g, fr.calcRadialLoc(g, spot.x, spot.z))
		gy, _ := zt.SampleHeight(spot.x, spot.z, 1e4)
		by, _ := zt.SampleHeight(bestAt[0], bestAt[1], 1e4)
		t.Logf("%s: ground %.1f here, %.1f at the densest", spot.name, gy, by)
		t.Logf("%s (zone %d): here %d plants (old %.1f); zone mean %.1f per swatch (old %.1f) over %d swatches; densest %d at (%.0f, %.0f)",
			spot.name, idx, len(here.insts), oldFloraExpected(zt, here.min, here.max),
			float64(newSum)/float64(max(n, 1)), float64(oldSum)/float64(max(n, 1)), n, best, bestAt[0], bestAt[1])
	}
}

// oldFloraExpected is the old BuildZoneFlora's expected plant count over a
// box: render overlay triangles with a flora set under their centroid, ny ≥
// 0.7, density·area·0.07 per dist, the dists found by their FLAGS word.
func oldFloraExpected(zt *ZoneTerrain, lo, hi [3]float32) float64 {
	rf := zt.RadialFlora
	grid := map[[2]int]int{}
	for _, ct := range zt.CollTiles {
		for i := range ct.Triangles {
			tri := &ct.Triangles[i]
			if tri.FloraType < 0 || tri.FloraType == 255 {
				continue
			}
			cx := (tri.X0 + tri.X1 + tri.X2) / 3
			cz := (tri.Z0 + tri.Z1 + tri.Z2) / 3
			grid[[2]int{int(math.Floor(float64(cx / 4))), int(math.Floor(float64(cz / 4)))}] = tri.FloraType
		}
	}
	setDists := map[int][]int{}
	for di, d := range rf.Dists {
		setDists[int(d.Flags)] = append(setDists[int(d.Flags)], di)
	}
	var sum float64
	for _, tile := range zt.Tiles {
		for ti := range tile.Triangles {
			tri := &tile.Triangles[ti]
			if tri.Layer == 0 {
				continue
			}
			cx := (tri.X0 + tri.X1 + tri.X2) / 3
			cz := (tri.Z0 + tri.Z1 + tri.Z2) / 3
			if cx < lo[0] || cx >= hi[0] || cz < lo[2] || cz >= hi[2] {
				continue
			}
			ft, ok := grid[[2]int{int(math.Floor(float64(cx / 4))), int(math.Floor(float64(cz / 4)))}]
			if !ok {
				continue
			}
			e1 := [3]float32{tri.X1 - tri.X0, tri.Y1 - tri.Y0, tri.Z1 - tri.Z0}
			e2 := [3]float32{tri.X2 - tri.X0, tri.Y2 - tri.Y0, tri.Z2 - tri.Z0}
			n := [3]float32{e1[1]*e2[2] - e1[2]*e2[1], e1[2]*e2[0] - e1[0]*e2[2], e1[0]*e2[1] - e1[1]*e2[0]}
			nl := length3(n)
			if nl < 0.001 || n[1]/nl < 0.7 {
				continue
			}
			area := nl * 0.5
			for _, di := range setDists[ft] {
				sum += float64(rf.Dists[di].Weight * area * 0.07)
			}
		}
	}
	return sum
}
