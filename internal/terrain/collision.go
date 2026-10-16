package terrain

import (
	"fmt"
	"math"
)

// CollDebug enables verbose collision logging when true.
// Toggle with F3 in the client.
var CollDebug bool

// slopeThresholdSq classifies collision triangles as walkable ground vs wall:
//   normalY² > slopeThresholdSq * lenSq  →  walkable ground (SampleHeight accepts)
//   normalY² ≤ slopeThresholdSq * lenSq  →  wall/cliff (sweepSphereVsWalls blocks)
//
// PS2 doesn't pre-classify — horizontal collision tests ALL triangles, and the
// collision response (Skitter + 3D pushout) naturally handles slope traversal.
// Since our CalcFit skips walkable triangles, we use cos²(60°) = 0.25 to allow
// traversal of moderate rock slopes (up to ~60°), matching observed PS2 behavior.
// PS2 walkControl[0x5C] = 0.7 is the VIWalkFloorFilter threshold, not a wall filter.
const slopeThresholdSq = 0.25 // 0.5 * 0.5 = cos²(60°)

// Swept sphere collision constants (PS2 VIWalkControl::InitParms).
const (
	charRadius float32 = 0.6 // PS2 walkControl[0x34] = 0.6 — horizontal collision sphere radius
	charCollY  float32 = 1.2 // PS2 walkControl[0x38] = 1.2 — sphere center height above feet
	// PS2 m_parm30 = 1.8: how far CalcFit drops its floor probe from the
	// centre, and the least headroom Move accepts.
	floorDrop float32 = 1.8
	// PS2 m_parm3C = 0.001: the radius of CalcFit's two probes.
	probeRadius float32 = 0.001
)

// CollTriangle holds three vertices (position only) for collision queries.
type CollTriangle struct {
	X0, Y0, Z0 float32
	X1, Y1, Z1 float32
	X2, Y2, Z2 float32
	MinX, MaxX float32 // precomputed XZ bounding box
	MinZ, MaxZ float32
	FloraType  int  // -1 = no flora, 0+ = flora set index
	Room       bool // a zone room's geometry; the radial flora query skips actors
	Walkable   bool // faces up (in the PS2's winding) at under 60° from level
}

// CollTile holds collision triangles for a tile grid cell.
type CollTile struct {
	Triangles []CollTriangle
}

// pointInTriangle3D tests whether point (px,py,pz) lies inside triangle (a,b,c)
// using 3D barycentric coordinates. Small epsilon tolerance for edge cases.
func pointInTriangle3D(px, py, pz, ax, ay, az, bx, by, bz, cx, cy, cz float32) bool {
	const e = 1e-4
	v0x, v0y, v0z := bx-ax, by-ay, bz-az
	v1x, v1y, v1z := cx-ax, cy-ay, cz-az
	v2x, v2y, v2z := px-ax, py-ay, pz-az

	d00 := v0x*v0x + v0y*v0y + v0z*v0z
	d01 := v0x*v1x + v0y*v1y + v0z*v1z
	d02 := v0x*v2x + v0y*v2y + v0z*v2z
	d11 := v1x*v1x + v1y*v1y + v1z*v1z
	d12 := v1x*v2x + v1y*v2y + v1z*v2z

	denom := d00*d11 - d01*d01
	if denom < 1e-8 {
		return false
	}
	inv := 1.0 / denom
	u := (d11*d02 - d01*d12) * inv
	v := (d00*d12 - d01*d02) * inv

	return u >= -e && v >= -e && (u+v) <= 1+e
}

// sweepSphereTriangle tests a moving sphere against a single triangle.
// Implements the 3-phase swept sphere test (Fauerby/Ericson algorithm),
// matching PS2 VICollide::Collide(VICollSphere&, VIVect3, VIVect3, VIVect3).
//
// Phase 1: Sphere vs triangle interior (plane test)
// Phase 2: Sphere vs triangle vertices (point-sphere test)
// Phase 3: Sphere vs triangle edges (cylinder test)
//
// Returns the earliest intersection t ∈ [0,1] and the collision normal
// pointing from the contact surface toward the sphere center.
func sweepSphereTriangle(
	sx, sy, sz float32, // sphere center start
	ex, ey, ez float32, // sphere center end
	radius float32,
	tri *CollTriangle,
) (hitT, hitNX, hitNY, hitNZ float32, hit bool) {
	const eps = 1e-7

	vx, vy, vz := ex-sx, ey-sy, ez-sz

	ax, ay, az := tri.X0, tri.Y0, tri.Z0
	bx, by, bz := tri.X1, tri.Y1, tri.Z1
	cx, cy, cz := tri.X2, tri.Y2, tri.Z2

	// Triangle edge vectors
	abx, aby, abz := bx-ax, by-ay, bz-az
	acx, acy, acz := cx-ax, cy-ay, cz-az

	// Triangle normal (normalized)
	nx := aby*acz - abz*acy
	ny := abz*acx - abx*acz
	nz := abx*acy - aby*acx
	nLenSq := nx*nx + ny*ny + nz*nz
	if nLenSq < eps {
		return 0, 0, 0, 0, false // degenerate triangle
	}
	invNLen := 1.0 / float32(math.Sqrt(float64(nLenSq)))
	nx *= invNLen
	ny *= invNLen
	nz *= invNLen

	bestT := float32(2.0)
	var bestNX, bestNY, bestNZ float32

	// --- Phase 1: Triangle interior (plane test) ---
	// Signed distance from sphere center to triangle plane: d(t) = d0 + t*dv
	d0 := (sx-ax)*nx + (sy-ay)*ny + (sz-az)*nz

	// One-way collision: if sphere center is behind the triangle plane (d0 < 0),
	// the player is already past this wall — don't block. Walls keep you out,
	// never trap you in.
	if d0 < 0 {
		return 0, 0, 0, 0, false
	}
	dv := vx*nx + vy*ny + vz*nz

	if math.Abs(float64(dv)) > eps {
		// Times when d(t) = ±radius
		ta := (radius - d0) / dv
		tb := (-radius - d0) / dv

		tLow := ta
		if tb < tLow {
			tLow = tb
		}

		if tLow >= 0 && tLow <= 1 && tLow < bestT {
			// Sphere center at tLow
			scx := sx + tLow*vx
			scy := sy + tLow*vy
			scz := sz + tLow*vz
			// Distance at tLow (±radius)
			dt := d0 + tLow*dv
			// Contact point on plane: center projected onto plane
			px := scx - dt*nx
			py := scy - dt*ny
			pz := scz - dt*nz

			if pointInTriangle3D(px, py, pz, ax, ay, az, bx, by, bz, cx, cy, cz) {
				bestT = tLow
				// Normal points from triangle toward sphere center
				if dt > 0 {
					bestNX, bestNY, bestNZ = nx, ny, nz
				} else {
					bestNX, bestNY, bestNZ = -nx, -ny, -nz
				}
			}
		}
	}

	// --- Phase 2: Vertex tests ---
	// |start + t*vel - vertex|² = radius²  →  quadratic in t
	velLenSq := vx*vx + vy*vy + vz*vz
	if velLenSq > eps {
		verts := [3][3]float32{{ax, ay, az}, {bx, by, bz}, {cx, cy, cz}}
		for _, v := range verts {
			dx := sx - v[0]
			dy := sy - v[1]
			dz := sz - v[2]

			qa := velLenSq
			qb := 2 * (dx*vx + dy*vy + dz*vz)
			qc := dx*dx + dy*dy + dz*dz - radius*radius

			disc := qb*qb - 4*qa*qc
			if disc < 0 {
				continue
			}

			sqrtDisc := float32(math.Sqrt(float64(disc)))
			t := (-qb - sqrtDisc) / (2 * qa)

			if t >= 0 && t <= 1 && t < bestT {
				bestT = t
				scx := sx + t*vx
				scy := sy + t*vy
				scz := sz + t*vz
				nnx := scx - v[0]
				nny := scy - v[1]
				nnz := scz - v[2]
				nlen := float32(math.Sqrt(float64(nnx*nnx + nny*nny + nnz*nnz)))
				if nlen > eps {
					bestNX = nnx / nlen
					bestNY = nny / nlen
					bestNZ = nnz / nlen
				}
			}
		}
	}

	// --- Phase 3: Edge tests ---
	// For each edge, solve |cross(S+t*vel, edgeDir)|² = radius² * |edgeDir|²
	// where S = start - edgeOrigin. This finds where the moving sphere
	// first touches the infinite cylinder around the edge.
	edges := [3][2][3]float32{
		{{ax, ay, az}, {bx, by, bz}},
		{{bx, by, bz}, {cx, cy, cz}},
		{{cx, cy, cz}, {ax, ay, az}},
	}

	for _, e := range edges {
		edx := e[1][0] - e[0][0]
		edy := e[1][1] - e[0][1]
		edz := e[1][2] - e[0][2]
		edLenSq := edx*edx + edy*edy + edz*edz
		if edLenSq < eps {
			continue
		}

		// S = start - edge origin
		spx := sx - e[0][0]
		spy := sy - e[0][1]
		spz := sz - e[0][2]

		// U = cross(S, edge), W = cross(vel, edge)
		ux := spy*edz - spz*edy
		uy := spz*edx - spx*edz
		uz := spx*edy - spy*edx

		wx := vy*edz - vz*edy
		wy := vz*edx - vx*edz
		wz := vx*edy - vy*edx

		qa := wx*wx + wy*wy + wz*wz
		qb := 2 * (ux*wx + uy*wy + uz*wz)
		qc := ux*ux + uy*uy + uz*uz - radius*radius*edLenSq

		disc := qb*qb - 4*qa*qc
		if disc < 0 || qa < eps {
			continue
		}

		sqrtDisc := float32(math.Sqrt(float64(disc)))
		t := (-qb - sqrtDisc) / (2 * qa)

		if t >= 0 && t <= 1 && t < bestT {
			// Verify contact is on the edge segment, not the infinite line.
			// s = dot(center(t) - edgeOrigin, edgeDir) / |edgeDir|²
			scx := sx + t*vx
			scy := sy + t*vy
			scz := sz + t*vz
			s := ((scx-e[0][0])*edx + (scy-e[0][1])*edy + (scz-e[0][2])*edz) / edLenSq

			if s >= 0 && s <= 1 {
				// Contact point on edge
				cpx := e[0][0] + s*edx
				cpy := e[0][1] + s*edy
				cpz := e[0][2] + s*edz

				bestT = t
				nnx := scx - cpx
				nny := scy - cpy
				nnz := scz - cpz
				nlen := float32(math.Sqrt(float64(nnx*nnx + nny*nny + nnz*nnz)))
				if nlen > eps {
					bestNX = nnx / nlen
					bestNY = nny / nlen
					bestNZ = nnz / nlen
				}
			}
		}
	}

	if bestT <= 1.0 {
		return bestT, bestNX, bestNY, bestNZ, true
	}
	return 0, 0, 0, 0, false
}

// SweepSphereVsWalls sweeps a sphere from start to end against all non-walkable
// triangles in nearby collision tiles. Returns the closest hit.
// Replaces castWallRay — covers the full sphere volume instead of approximating
// with discrete ray heights. Also used by camera collision.
func (zt *ZoneTerrain) SweepSphereVsWalls(
	fromX, fromY, fromZ,
	toX, toY, toZ,
	radius float32,
) (hitNX, hitNY, hitNZ, hitT float32, hit bool) {
	fx := int(math.Floor(float64(fromX / TileSize)))
	fz := int(math.Floor(float64(fromZ / TileSize)))
	tx := int(math.Floor(float64(toX / TileSize)))
	tz := int(math.Floor(float64(toZ / TileSize)))

	type tk = TileKey
	seen := make(map[tk]struct{}, 18)
	for dz := -1; dz <= 1; dz++ {
		for dx := -1; dx <= 1; dx++ {
			seen[tk{TX: fx + dx, TZ: fz + dz}] = struct{}{}
			seen[tk{TX: tx + dx, TZ: tz + dz}] = struct{}{}
		}
	}

	bestT := float32(2.0)
	var bestNX, bestNY, bestNZ float32

	for key := range seen {
		tile := zt.CollTiles[key]
		if tile == nil {
			continue
		}
		for i := range tile.Triangles {
			tri := &tile.Triangles[i]
			if tri.Walkable {
				continue
			}

			t, hnx, hny, hnz, ok := sweepSphereTriangle(
				fromX, fromY, fromZ,
				toX, toY, toZ,
				radius, tri,
			)
			if ok && t < bestT {
				bestT = t
				bestNX = hnx
				bestNY = hny
				bestNZ = hnz
			}
		}
	}

	if bestT <= 1.0 {
		return bestNX, bestNY, bestNZ, bestT, true
	}
	return 0, 0, 0, 0, false
}

// SampleHeight returns the ground Y at world position (x, z).
// Mirrors PS2 VIWalkControl::CalcFloor — finds the HIGHEST walkable surface
// at or below refY + stepUp. This is equivalent to a downward ray from above:
// it finds the ground the player would stand on, naturally preferring ramps
// and elevated surfaces over lower ground.
//
// stepUp allows the player to ascend ramps and step onto slightly higher
// surfaces (each frame of movement on a ramp only rises a fraction of a unit).
// Surfaces more than stepUp above refY are ignored (bridges, roofs).
//
// Returns (y, true) if ground found, (0, false) if no triangle contains the point.
// SampleLowestHeight returns the lowest walkable Y at (x,z) in the single tile.
// Used to detect elevated surfaces (roofs, canopies) — if a surface is far above
// the lowest walkable Y, it's not ground.
func (zt *ZoneTerrain) SampleLowestHeight(x, z float32) (float32, bool) {
	if len(zt.CollTiles) == 0 {
		return 0, false
	}
	cx := int(math.Floor(float64(x / TileSize)))
	cz := int(math.Floor(float64(z / TileSize)))
	tile := zt.CollTiles[TileKey{TX: cx, TZ: cz}]
	if tile == nil {
		return 0, false
	}
	found := false
	bestY := float32(0)
	for i := range tile.Triangles {
		tri := &tile.Triangles[i]
		if !tri.Walkable {
			continue
		}
		y, ok := collTriHeight(tri, x, z)
		if !ok {
			continue
		}
		if !found || y < bestY {
			bestY = y
			found = true
		}
	}
	return bestY, found
}

// SampleHeightFast returns the highest walkable Y at (x,z) checking only the single tile.
// Much faster than SampleHeight (no 3×3 neighbor scan, no refY filter).
// Suitable for flora placement where exact step-up logic is unnecessary.
func (zt *ZoneTerrain) SampleHeightFast(x, z float32) (float32, bool) {
	if len(zt.CollTiles) == 0 {
		return 0, false
	}
	cx := int(math.Floor(float64(x / TileSize)))
	cz := int(math.Floor(float64(z / TileSize)))
	tile := zt.CollTiles[TileKey{TX: cx, TZ: cz}]
	if tile == nil {
		return 0, false
	}
	found := false
	bestY := float32(0)
	for i := range tile.Triangles {
		tri := &tile.Triangles[i]
		if !tri.Walkable {
			continue
		}
		if x < tri.MinX || x > tri.MaxX || z < tri.MinZ || z > tri.MaxZ {
			continue
		}
		y, ok := collTriHeight(tri, x, z)
		if !ok {
			continue
		}
		if !found || y > bestY {
			bestY = y
			found = true
		}
	}
	return bestY, found
}

// SampleHeightRef returns the highest walkable Y at (x,z) that is at or below
// refY+stepUp, checking only the single tile containing the point.
// Use for entity ground-snap where refY filtering prevents roof snapping.
func (zt *ZoneTerrain) SampleHeightRef(x, z, refY float32) (float32, bool) {
	if len(zt.CollTiles) == 0 {
		return 0, false
	}
	const stepUp float32 = 1.8 // PS2 walkControl[0x30] = 1.8
	cx := int(math.Floor(float64(x / TileSize)))
	cz := int(math.Floor(float64(z / TileSize)))
	tile := zt.CollTiles[TileKey{TX: cx, TZ: cz}]
	if tile == nil {
		return 0, false
	}
	found := false
	bestY := float32(0)
	for i := range tile.Triangles {
		tri := &tile.Triangles[i]
		if !tri.Walkable {
			continue
		}
		if x < tri.MinX || x > tri.MaxX || z < tri.MinZ || z > tri.MaxZ {
			continue
		}
		y, ok := collTriHeight(tri, x, z)
		if !ok {
			continue
		}
		if y > refY+stepUp {
			continue
		}
		if !found || y > bestY {
			bestY = y
			found = true
		}
	}
	return bestY, found
}

func (zt *ZoneTerrain) SampleHeight(x, z, refY float32) (float32, bool) {
	if len(zt.CollTiles) == 0 {
		return 0, false
	}

	const stepUp float32 = 1.8 // PS2 walkControl[0x30] = 1.8 — max height above refY to step onto

	cx := int(math.Floor(float64(x / TileSize)))
	cz := int(math.Floor(float64(z / TileSize)))

	found := false
	bestY := float32(0)

	for dz := -1; dz <= 1; dz++ {
		for dx := -1; dx <= 1; dx++ {
			tile := zt.CollTiles[TileKey{TX: cx + dx, TZ: cz + dz}]
			if tile == nil {
				continue
			}
			for i := range tile.Triangles {
				tri := &tile.Triangles[i]
				if !tri.Walkable {
					continue
				}
				if x < tri.MinX || x > tri.MaxX || z < tri.MinZ || z > tri.MaxZ {
					continue
				}
				y, ok := collTriHeight(tri, x, z)
				if !ok {
					continue
				}
				// Reject surfaces too far above the player (bridges, roofs).
				if y > refY+stepUp {
					continue
				}
				if !found || y > bestY {
					bestY = y
					found = true
				}
			}
		}
	}
	return bestY, found
}

// collTriHeight tests whether (px, pz) lies inside the collision triangle's
// XZ projection and returns the interpolated Y if so.
func collTriHeight(tri *CollTriangle, px, pz float32) (float32, bool) {
	v0x := tri.X2 - tri.X0
	v0z := tri.Z2 - tri.Z0
	v1x := tri.X1 - tri.X0
	v1z := tri.Z1 - tri.Z0
	v2x := px - tri.X0
	v2z := pz - tri.Z0

	d00 := v0x*v0x + v0z*v0z
	d01 := v0x*v1x + v0z*v1z
	d02 := v0x*v2x + v0z*v2z
	d11 := v1x*v1x + v1z*v1z
	d12 := v1x*v2x + v1z*v2z

	denom := d00*d11 - d01*d01
	if denom == 0 {
		return 0, false
	}
	inv := 1.0 / denom
	u := (d11*d02 - d01*d12) * inv
	v := (d00*d12 - d01*d02) * inv
	w := 1.0 - u - v

	if u < 0 || v < 0 || w < 0 {
		return 0, false
	}

	y := w*tri.Y0 + v*tri.Y1 + u*tri.Y2
	return y, true
}

// CalcFitResult is what the sphere half of VIWalkControl::Move leaves behind.
type CalcFitResult struct {
	X, Z       float32 // final position
	HitX, HitZ float32 // where the first sweep stopped
	NX, NY, NZ float32 // the first hit's normal
	Hit        bool    // the first sweep hit something
	// PushedOut: wedged in the air, the walker was moved a radius out along the
	// stuck normals; Move zeroes its horizontal force and velocity then.
	PushedOut bool
}

// CalcFit is the sphere sweep of VIWalkControl::Move (viwalkcontrol.cpp), for
// a horizontal step at the sphere's height (feet + 1.2, radius 0.6):
//
//   - sweep from the old position to the new one; no hit, the walker arrives;
//   - on a hit, skitter twice (the player's Move(0)): re-aim along the surface,
//     kept horizontal when grounded, and sweep again. A sweep that comes free,
//     or that moves at all (t > 0.001), ends the wedge;
//   - only when every sweep stuck at once AND the walker is airborne and out
//     of liquid is it pushed a radius out along the stuck normals and swept
//     again — never on an ordinary wall contact;
//   - the walker ends where the last sweep stopped.
//
// It used to push out by the radius after every hit, which made walls shove
// the player away, and a first sweep starting in contact returned the
// unblocked target, which let corners be walked through.
//
// The floor is SampleHeight's (CalcFloor). Faces under 60° are left to it
// rather than swept: the PS2 steps the walker in 32 ms ticks, where those
// slopes only ever slow a climb, and this client steps once per frame.
func (zt *ZoneTerrain) CalcFit(oldX, oldY, oldZ, newX, newZ float32, grounded, inLiquid bool) CalcFitResult {
	r := CalcFitResult{X: newX, Z: newZ}
	if len(zt.CollTiles) == 0 {
		return r
	}
	y := oldY + charCollY
	from, to := vec3{oldX, y, oldZ}, vec3{newX, y, newZ}

	var s collSphere
	s.init(from, to, charRadius)
	if !zt.collideSphere(&s, false) {
		return r
	}
	hp := s.hitPoint()
	r.Hit = true
	r.HitX, r.HitZ = hp[0], hp[2]
	r.NX, r.NY, r.NZ = s.hitNormal[0], s.hitNormal[1], s.hitNormal[2]

	var skit vec3
	if grounded {
		skit = vec3{0, 1, 0}
	}
	wedged := true
	var normal vec3
	for i := 0; i < 2; i++ {
		s.skitter(skit)
		if !zt.collideSphere(&s, false) {
			wedged = false
			break
		}
		if 0.001 < s.hitTime {
			wedged = false
		} else {
			normal = normal.add(s.hitNormal)
		}
	}
	if wedged && !grounded && !inLiquid {
		normal.normalize()
		to = to.add(normal.scale(charRadius))
		r.PushedOut = true
		s.init(from, to, charRadius)
		zt.collideSphere(&s, false)
	}
	end := s.hitPoint()
	r.X, r.Z = end[0], end[2]

	if CollDebug {
		fmt.Printf("CalcFit: (%.2f,%.2f)->(%.2f,%.2f) hit (%.2f,%.2f) n=(%.2f,%.2f,%.2f) end (%.2f,%.2f) wedged=%v pushed=%v\n",
			oldX, oldZ, newX, newZ, r.HitX, r.HitZ, r.NX, r.NY, r.NZ, r.X, r.Z, wedged, r.PushedOut)
	}
	return r
}

// Fit is VIWalkControl::CalcFit (viwalkcontrol.cpp:1255) for a walker whose
// feet are at feetY: a sphere of radius 0.001 dropped 1.8 from the centre
// (feet + 1.2) finds the floor, and a second, swept from the centre up to
// floor + 3.6, finds the head. A probe that meets nothing ends at its end
// point, so with no floor in reach the floor is feet − 0.6. Faces are
// one-sided, as in the wall sweep, so the drop meets only faces that point
// up and the rise only faces that point down, whatever their slope.
func (zt *ZoneTerrain) Fit(x, feetY, z float32) (floor, head float32) {
	centre := vec3{x, feetY + charCollY, z}
	var s collSphere
	s.init(centre, centre.sub(vec3{0, floorDrop, 0}), probeRadius)
	zt.collideSphere(&s, true)
	floor = s.hitPoint()[1]

	s.init(centre, vec3{x, floor + floorDrop*2, z}, probeRadius)
	zt.collideSphere(&s, true)
	head = s.hitPoint()[1]
	return floor, head
}

// CreatureFloor is the floor half of CLIENT's 0x00CBCE40, which sets a
// creature that is not walking down on every update and every frame: a
// sphere of radius 0.1 swept from 1.5 above (x, y, z) to 1.5 below; a hit
// puts the creature on the floor, the sphere's centre less its radius. A
// sweep that meets nothing leaves it where it was (false). Characters are no
// part of it (its VIAIExtrapolateFilter rejects every VICSprite actor), and
// they are not in the collision tiles.
func (zt *ZoneTerrain) CreatureFloor(x, y, z float32) (float32, bool) {
	const reach, radius = 1.5, 0.1
	var s collSphere
	s.init(vec3{x, y + reach, z}, vec3{x, y - reach, z}, radius)
	if !zt.collideSphere(&s, true) {
		return y, false
	}
	return s.hitPoint()[1] - radius, true
}

// ArrivalFloor is the cast CLIENT's Respawn (0x00BD5628, 0x00B2's, reach 3)
// and MoveTo (0x00BD53A0, reach 300) set the arriving walker down with: a
// sphere of radius 0.1 from reach above (x, y, z) to reach below, and the
// walker's feet 0.2 below the sphere's centre where it stopped — 0.1 under a
// floor it met (the walker steps up onto it), or 0.2 under the sweep's end
// when it met none. (Both run the same sweep twice and throw the first
// answer away: the second Collide is not re-aimed.)
func (zt *ZoneTerrain) ArrivalFloor(x, y, z, reach float32) float32 {
	var s collSphere
	s.init(vec3{x, y + reach, z}, vec3{x, y - reach, z}, 0.1)
	zt.collideSphere(&s, true)
	return s.hitPoint()[1] - 0.2
}

// collideSphere sweeps s against the collision triangles around its path,
// keeping the earliest hit — the walker's VIScene::Collide. The wall sweep
// passes all = false and meets only the steep faces; see CalcFit.
func (zt *ZoneTerrain) collideSphere(s *collSphere, all bool) bool {
	// A zero-length sweep collides with nothing (VICollBuffer's walk).
	if s.length == 0 {
		return false
	}
	minX := min(s.start[0], s.end[0]) - s.radius
	maxX := max(s.start[0], s.end[0]) + s.radius
	minZ := min(s.start[2], s.end[2]) - s.radius
	maxZ := max(s.start[2], s.end[2]) + s.radius

	keys := make([]TileKey, 0, 18)
	add := func(x, z float32) {
		tx := int(math.Floor(float64(x / TileSize)))
		tz := int(math.Floor(float64(z / TileSize)))
		for dz := -1; dz <= 1; dz++ {
			for dx := -1; dx <= 1; dx++ {
				k := TileKey{TX: tx + dx, TZ: tz + dz}
				dup := false
				for _, have := range keys {
					dup = dup || have == k
				}
				if !dup {
					keys = append(keys, k)
				}
			}
		}
	}
	add(s.start[0], s.start[2])
	add(s.end[0], s.end[2])

	hit := false
	for _, key := range keys {
		tile := zt.CollTiles[key]
		if tile == nil {
			continue
		}
		for i := range tile.Triangles {
			tri := &tile.Triangles[i]
			if (tri.Walkable && !all) || tri.MaxX < minX || tri.MinX > maxX || tri.MaxZ < minZ || tri.MinZ > maxZ {
				continue
			}
			// The PS2's strip order: CollTriangle holds each strip triangle
			// with its first two vertices swapped, so swap them back — the
			// face normal, and so which side blocks, depends on it.
			if s.collideTri(vec3{tri.X1, tri.Y1, tri.Z1}, vec3{tri.X0, tri.Y0, tri.Z0}, vec3{tri.X2, tri.Y2, tri.Z2}) {
				hit = true
			}
		}
	}
	return hit
}

// segTriIntersect runs Möller-Trumbore segment-triangle intersection.
// Returns the outward face normal (nx,ny,nz), parameter t ∈ [0,1], and hit bool.
// Kept for future ray-based needs (targeting, line-of-sight).
func segTriIntersect(
	ox, oy, oz, nx, ny, nz float32,
	tri *CollTriangle,
) (wnx, wny, wnz, t float32, hit bool) {
	const eps = 1e-7

	// Segment direction
	dx := nx - ox
	dy := ny - oy
	dz := nz - oz

	// Triangle edges
	e1x := tri.X1 - tri.X0
	e1y := tri.Y1 - tri.Y0
	e1z := tri.Z1 - tri.Z0
	e2x := tri.X2 - tri.X0
	e2y := tri.Y2 - tri.Y0
	e2z := tri.Z2 - tri.Z0

	// h = cross(dir, edge2)
	hx := dy*e2z - dz*e2y
	hy := dz*e2x - dx*e2z
	hz := dx*e2y - dy*e2x

	a := e1x*hx + e1y*hy + e1z*hz
	if a > -eps && a < eps {
		return 0, 0, 0, 0, false // parallel
	}
	f := 1.0 / a

	// s = origin - v0
	sx := ox - tri.X0
	sy := oy - tri.Y0
	sz := oz - tri.Z0

	u := f * (sx*hx + sy*hy + sz*hz)
	if u < 0 || u > 1 {
		return 0, 0, 0, 0, false
	}

	// q = cross(s, edge1)
	qx := sy*e1z - sz*e1y
	qy := sz*e1x - sx*e1z
	qz := sx*e1y - sy*e1x

	v := f * (dx*qx + dy*qy + dz*qz)
	if v < 0 || u+v > 1 {
		return 0, 0, 0, 0, false
	}

	t = f * (e2x*qx + e2y*qy + e2z*qz)
	if t < 0 || t > 1 {
		return 0, 0, 0, 0, false
	}

	// Face normal = normalize(cross(edge1, edge2))
	fnx := e1y*e2z - e1z*e2y
	fny := e1z*e2x - e1x*e2z
	fnz := e1x*e2y - e1y*e2x
	flen := float32(math.Sqrt(float64(fnx*fnx + fny*fny + fnz*fnz)))
	if flen < eps {
		return 0, 0, 0, 0, false
	}
	fnx /= flen
	fny /= flen
	fnz /= flen

	// Ensure normal faces toward the segment origin (outward).
	// dot(normal, segDir) should be negative (we're moving toward the wall).
	if fnx*dx+fny*dy+fnz*dz > 0 {
		fnx = -fnx
		fny = -fny
		fnz = -fnz
	}

	return fnx, fny, fnz, t, true
}

// addCollTriToZone buckets a collision triangle into the appropriate tile.
func addCollTriToZone(zt *ZoneTerrain, tri CollTriangle) {
	// Precompute XZ bounding box.
	tri.MinX = tri.X0
	if tri.X1 < tri.MinX {
		tri.MinX = tri.X1
	}
	if tri.X2 < tri.MinX {
		tri.MinX = tri.X2
	}
	tri.MaxX = tri.X0
	if tri.X1 > tri.MaxX {
		tri.MaxX = tri.X1
	}
	if tri.X2 > tri.MaxX {
		tri.MaxX = tri.X2
	}
	tri.MinZ = tri.Z0
	if tri.Z1 < tri.MinZ {
		tri.MinZ = tri.Z1
	}
	if tri.Z2 < tri.MinZ {
		tri.MinZ = tri.Z2
	}
	tri.MaxZ = tri.Z0
	if tri.Z1 > tri.MaxZ {
		tri.MaxZ = tri.Z1
	}
	if tri.Z2 > tri.MaxZ {
		tri.MaxZ = tri.Z2
	}

	// Precompute walkable (slope check).
	e1x := tri.X1 - tri.X0
	e1y := tri.Y1 - tri.Y0
	e1z := tri.Z1 - tri.Z0
	e2x := tri.X2 - tri.X0
	e2y := tri.Y2 - tri.Y0
	e2z := tri.Z2 - tri.Z0
	nx := e1y*e2z - e1z*e2y
	ny := e1z*e2x - e1x*e2z
	nz := e1x*e2y - e1y*e2x
	lenSq := nx*nx + ny*ny + nz*nz
	// e1×e2 in this vertex order is the PS2's own face normal (the swap of
	// the first two vertices and the reversed cross cancel), so ny < 0 is a
	// ceiling. The square alone let a ceiling count as ground.
	tri.Walkable = ny > 0 && ny*ny > slopeThresholdSq*lenSq

	cx := (tri.X0 + tri.X1 + tri.X2) / 3
	cz := (tri.Z0 + tri.Z1 + tri.Z2) / 3
	key := TileKey{
		TX: int(math.Floor(float64(cx / TileSize))),
		TZ: int(math.Floor(float64(cz / TileSize))),
	}

	tile, ok := zt.CollTiles[key]
	if !ok {
		tile = &CollTile{}
		zt.CollTiles[key] = tile
	}
	tile.Triangles = append(tile.Triangles, tri)
}

// TopSurfaceY returns the height of the highest collision triangle over (x, z),
// whatever its slope — what a ray cast straight down from far above hits
// first. The weather's per-particle ray (VIAtmosphere::ProcessWeather) uses
// it, which is what stops rain at a roof.
func (zt *ZoneTerrain) TopSurfaceY(x, z float32) (float32, bool) {
	if len(zt.CollTiles) == 0 {
		return 0, false
	}
	cx := int(math.Floor(float64(x / TileSize)))
	cz := int(math.Floor(float64(z / TileSize)))
	found := false
	bestY := float32(0)
	for dz := -1; dz <= 1; dz++ {
		for dx := -1; dx <= 1; dx++ {
			tile := zt.CollTiles[TileKey{TX: cx + dx, TZ: cz + dz}]
			if tile == nil {
				continue
			}
			for i := range tile.Triangles {
				tri := &tile.Triangles[i]
				if x < tri.MinX || x > tri.MaxX || z < tri.MinZ || z > tri.MaxZ {
					continue
				}
				y, ok := collTriHeight(tri, x, z)
				if ok && (!found || y > bestY) {
					bestY = y
					found = true
				}
			}
		}
	}
	return bestY, found
}

// SunGateBelow returns the baked sun gate — the vertex alpha the lighting
// multiplies the sun by — of the highest base-layer triangle under (x, z)
// between y-depth and y. VIScene::CalcOutdoorsIntensity reads exactly that off
// the world 15 units below the camera to decide how far outdoors it is.
func (zt *ZoneTerrain) SunGateBelow(x, y, z, depth float32) (float32, bool) {
	cx := int(math.Floor(float64(x / TileSize)))
	cz := int(math.Floor(float64(z / TileSize)))
	found := false
	bestY, gate := float32(0), float32(0)
	for dz := -1; dz <= 1; dz++ {
		for dx := -1; dx <= 1; dx++ {
			tile := zt.Tiles[TileKey{TX: cx + dx, TZ: cz + dz}]
			if tile == nil {
				continue
			}
			for i := range tile.Triangles {
				t := &tile.Triangles[i]
				if t.Layer != 0 {
					continue
				}
				if x < min(t.X0, t.X1, t.X2) || x > max(t.X0, t.X1, t.X2) ||
					z < min(t.Z0, t.Z1, t.Z2) || z > max(t.Z0, t.Z1, t.Z2) {
					continue
				}
				w0, w1, w2, ok := baryXZ(t.X0, t.Z0, t.X1, t.Z1, t.X2, t.Z2, x, z)
				if !ok {
					continue
				}
				ty := w0*t.Y0 + w1*t.Y1 + w2*t.Y2
				if ty > y || ty < y-depth || (found && ty <= bestY) {
					continue
				}
				bestY, found = ty, true
				gate = w0*t.A0 + w1*t.A1 + w2*t.A2
			}
		}
	}
	return gate, found
}

// baryXZ returns the barycentric weights of (px, pz) in a triangle's XZ
// projection, and whether the point lies inside it.
func baryXZ(x0, z0, x1, z1, x2, z2, px, pz float32) (w0, w1, w2 float32, ok bool) {
	d := (z1-z2)*(x0-x2) + (x2-x1)*(z0-z2)
	if d == 0 {
		return 0, 0, 0, false
	}
	w0 = ((z1-z2)*(px-x2) + (x2-x1)*(pz-z2)) / d
	w1 = ((z2-z0)*(px-x2) + (x0-x2)*(pz-z2)) / d
	w2 = 1 - w0 - w1
	return w0, w1, w2, w0 >= 0 && w1 >= 0 && w2 >= 0
}
