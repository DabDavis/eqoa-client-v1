package terrain

import (
	"math"
	"testing"
)

// stripZone builds a zone from triangle strips exactly as the zone loader
// does from a CollBuffer (terrain.go): even triangles (v[i+1], v[i], v[i+2]),
// odd ones (v[i], v[i+1], v[i+2]).
func stripZone(strips ...[]vec3) *ZoneTerrain {
	zt := &ZoneTerrain{CollTiles: map[TileKey]*CollTile{}}
	for _, v := range strips {
		for i := 0; i+2 < len(v); i++ {
			a, b, c := v[i+1], v[i], v[i+2]
			if i%2 == 1 {
				a, b = v[i], v[i+1]
			}
			addCollTriToZone(zt, CollTriangle{
				X0: a[0], Y0: a[1], Z0: a[2],
				X1: b[0], Y1: b[1], Z1: b[2],
				X2: c[0], Y2: c[1], Z2: c[2],
				FloraType: -1,
			})
		}
	}
	return zt
}

// A wall in the plane z = 10, x in [-5, 5], facing -Z under the PS2's strip
// order (v0, v1, v2), (v2, v1, v3).
var wallZ10 = []vec3{{5, 0, 10}, {5, 5, 10}, {-5, 0, 10}, {-5, 5, 10}}

// A second wall in the plane x = 3, facing -X.
var wallX3 = []vec3{{3, 0, 0}, {3, 5, 0}, {3, 0, 20}, {3, 5, 20}}

func near(a, b float32) bool { return math.Abs(float64(a-b)) < 1e-3 }

// The face the PS2 blocks on must be the one the old sweep blocked on: the
// loader's vertex order is the PS2's with two vertices swapped.
func TestWallFacesTheSameWay(t *testing.T) {
	zt := stripZone(wallZ10)
	if _, _, _, _, hit := zt.SweepSphereVsWalls(0, 1.2, 8, 0, 1.2, 9.8, charRadius); !hit {
		t.Fatal("the old sweep does not block from -Z: the test wall is the wrong way round")
	}
	r := zt.CalcFit(0, 0, 8, 0, 9.8, true, false)
	if !r.Hit || r.NZ > -0.99 {
		t.Fatalf("CalcFit from -Z: hit=%v normal=(%.2f,%.2f,%.2f), want a hit facing -Z", r.Hit, r.NX, r.NY, r.NZ)
	}
}

// Walking into a wall stops the sphere against it — not a radius short of
// that, as the old unconditional push-out did.
func TestWallHeadOnStops(t *testing.T) {
	zt := stripZone(wallZ10)
	r := zt.CalcFit(0, 0, 8, 0, 9.8, true, false)
	if !near(r.Z, 10-charRadius) || !near(r.X, 0) || r.PushedOut {
		t.Fatalf("head-on: ended at (%.3f, %.3f) pushed=%v, want (0, %.3f)", r.X, r.Z, r.PushedOut, 10-charRadius)
	}
	// Standing against it and pushing on: no movement, no tunnelling.
	r = zt.CalcFit(0, 0, 10-charRadius, 0, 10.5, true, false)
	if r.Z > 10-charRadius+1e-3 {
		t.Fatalf("pressed against the wall: moved to z=%.3f", r.Z)
	}
}

// At 45° the walker slides along the wall, staying in front of it.
func TestWallSlides(t *testing.T) {
	zt := stripZone(wallZ10)
	r := zt.CalcFit(0, 0, 9, 1, 10, true, false)
	if r.Z > 10-charRadius+1e-3 || r.X < 0.5 {
		t.Fatalf("45° into the wall: ended at (%.3f, %.3f), want sliding along x in front of z=%.1f", r.X, r.Z, 10-charRadius)
	}
}

// The far side does not block: a triangle is one-sided.
func TestWallOneSided(t *testing.T) {
	zt := stripZone(wallZ10)
	r := zt.CalcFit(0, 0, 12, 0, 8, true, false)
	if r.Hit || !near(r.Z, 8) {
		t.Fatalf("from behind: hit=%v z=%.3f, want straight through to 8", r.Hit, r.Z)
	}
}

// A corner holds: driving into it diagonally ends inside both walls' reach.
func TestCornerHolds(t *testing.T) {
	zt := stripZone(wallZ10, wallX3)
	x, z := float32(1), float32(8)
	for i := 0; i < 40; i++ {
		r := zt.CalcFit(x, 0, z, x+0.3, z+0.3, true, false)
		x, z = r.X, r.Z
	}
	if x > 3-charRadius+1e-3 || z > 10-charRadius+1e-3 {
		t.Fatalf("after 40 steps into the corner: (%.3f, %.3f), past (%.2f, %.2f)", x, z, 3-charRadius, 10-charRadius)
	}
	if x < 3-charRadius-0.05 || z < 10-charRadius-0.05 {
		t.Fatalf("after 40 steps into the corner: (%.3f, %.3f), short of it", x, z)
	}
}
