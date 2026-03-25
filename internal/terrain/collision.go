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
)

// CollTriangle holds three vertices (position only) for collision queries.
type CollTriangle struct {
	X0, Y0, Z0 float32
	X1, Y1, Z1 float32
	X2, Y2, Z2 float32
	MinX, MaxX float32 // precomputed XZ bounding box
	MinZ, MaxZ float32
	FloraType  int  // -1 = no flora, 0+ = flora set index
	Walkable   bool // precomputed slope check (normalY > ~0.7)
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

// sweepSphereVsWalls sweeps a sphere from start to end against all non-walkable
// triangles in nearby collision tiles. Returns the closest hit.
// Replaces castWallRay — covers the full sphere volume instead of approximating
// with discrete ray heights.
func (zt *ZoneTerrain) sweepSphereVsWalls(
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

// CalcFitResult mirrors PS2 VIWalkControl::CalcFit output parameters:
//
//	position      → X, Z          (final resolved position after all iterations)
//	hitPoint      → HitX, HitZ    (point of first wall collision)
//	surfaceNormal → NX, NY, NZ    (outward normal at first hit surface)
//	slideVector   → SlideX, SlideZ (Skitter output — remaining movement projected onto wall)
//
// PS2 signature: CalcFit(int mode, VIScene& scene, VIVect3& position,
//
//	VIVect3& hitPoint, VIVect3& surfaceNormal, VIVect3& slideVector)
type CalcFitResult struct {
	X, Z           float32 // final resolved position
	HitX, HitZ     float32 // point of first wall collision (from→hit at t)
	NX, NY, NZ     float32 // surface normal at first hit
	SlideX, SlideZ float32 // slide vector — Skitter output after last iteration
	Hit            bool    // true if any wall collision occurred
}

// CalcFit resolves horizontal wall collision using iterative collide-and-slide.
// Mirrors PS2 VIWalkControl::CalcFit + VICollSphere + VICollRay::Skitter.
//
// sweepSphereVsWalls only returns wall/steep hits (normalY ≤ 0.7). Walkable surfaces
// (ramps, gentle slopes) are invisible to CalcFit — they're handled by
// SampleHeight (CalcFloor), which adjusts Y after horizontal movement resolves.
//
// Pipeline: CalcFit (horizontal walls) → CalcFloor/SampleHeight (vertical ground)
//
// Returns CalcFitResult with PS2 output parameters: position, hitPoint,
// surfaceNormal, slideVector.
func (zt *ZoneTerrain) CalcFit(oldX, oldY, oldZ, newX, newZ float32) CalcFitResult {
	r := CalcFitResult{X: newX, Z: newZ}
	if len(zt.CollTiles) == 0 {
		return r
	}

	const (
		maxIter    = 2     // PS2 Move: $fp = 2 — max Skitter iterations
		perturbMul = 0.01  // PS2 Skitter: PerturbMult blend weight (0x3C23D70A)
		minMove    = 0.001 // PS2 Move: skip if movement negligible (0x38D1B717 ≈ 1e-4 squared)
	)

	// PS2 VIWalkControl::Move sweeps at constant Y = oldY + charCollY.
	// The PS2 VIEnvRay pre-sample only searches ±0.1 (walkControl[0x54]),
	// which is essentially no adjustment. Constant Y is correct here.
	sphereY := oldY + charCollY

	fromX, fromZ := oldX, oldZ
	remX, remZ := newX-oldX, newZ-oldZ

	// Accumulated hit normals for post-loop pushout (PS2 Move accumulates
	// normals across all Skitter iterations).
	var accNX, accNY, accNZ float32

	for iter := 0; iter < maxIter; iter++ {
		mag := float32(math.Sqrt(float64(remX*remX + remZ*remZ)))
		if mag < minMove {
			break
		}

		toX := fromX + remX
		toZ := fromZ + remZ

		hnx, hny, hnz, t, hit := zt.sweepSphereVsWalls(
			fromX, sphereY, fromZ,
			toX, sphereY, toZ,
			charRadius,
		)
		if !hit {
			r.X = toX
			r.Z = toZ
			break
		}

		// PS2 Move: check hit time > 0.001 (0x3A83126F) before processing.
		if t < 0.001 {
			break
		}

		// Record first-hit outputs (PS2 hitPoint + surfaceNormal).
		if !r.Hit {
			r.HitX = fromX + remX*t
			r.HitZ = fromZ + remZ*t
			r.NX = hnx
			r.NY = hny
			r.NZ = hnz
			r.Hit = true
		}

		// PS2 Move: accumulate hit normals across iterations.
		accNX += hnx
		accNY += hny
		accNZ += hnz

		if CollDebug {
			slopeAngle := math.Acos(float64(hny)) * 180 / math.Pi
			fmt.Printf("CalcFit[%d] HIT t=%.3f normal=(%.2f,%.2f,%.2f) slope=%.0f° rem=(%.3f,%.3f)\n",
				iter, t, hnx, hny, hnz, slopeAngle, remX, remZ)
		}

		// PS2 Move: position at hit point (no skin offset — PS2 uses t directly).
		r.X = fromX + remX*t
		r.Z = fromZ + remZ*t

		// PS2 VICollRay::Skitter: 3D normal projection.
		// slideVector = remainder - dot(remainder, N) * N
		// PS2 does double reflection with 0.01 blend, but the dominant term
		// is the simple projection. Using full 3D normal (not 2D XZ-only).
		slideX := remX * (1 - t)
		slideY := float32(0) // horizontal movement, Y component is 0
		slideZ := remZ * (1 - t)

		dot3d := slideX*hnx + slideY*hny + slideZ*hnz
		slideX -= dot3d * hnx
		slideZ -= dot3d * hnz

		// PS2 Skitter: blend with PerturbMult (0.01) to nudge off wall.
		// slide = normalize(slide + perturbMul * hitNormal)
		slideX += perturbMul * hnx
		slideZ += perturbMul * hnz
		sLen := float32(math.Sqrt(float64(slideX*slideX + slideZ*slideZ)))
		if sLen > 1e-6 {
			slideX /= sLen
			slideZ /= sLen
			// Scale to remaining distance projected onto slide direction.
			remMag := float32(math.Sqrt(float64(remX*remX+remZ*remZ))) * (1 - t)
			remX = slideX * remMag
			remZ = slideZ * remMag
		} else {
			remX = 0
			remZ = 0
		}

		if CollDebug {
			fmt.Printf("  Skitter: dot=%.3f slide=(%.3f,%.3f)\n", dot3d, remX, remZ)
		}

		// Store slide vector output (PS2 slideVector parameter).
		r.SlideX = remX
		r.SlideZ = remZ

		fromX = r.X
		fromZ = r.Z
	}

	// PS2 Move: post-loop pushout — push position outward along accumulated
	// normal by charRadius. This ensures the player stays fully outside walls.
	if r.Hit {
		aLen := float32(math.Sqrt(float64(accNX*accNX + accNY*accNY + accNZ*accNZ)))
		if aLen > 1e-6 {
			pushX := (accNX / aLen) * charRadius
			pushZ := (accNZ / aLen) * charRadius
			r.X += pushX
			r.Z += pushZ
		}
	}

	if CollDebug && r.Hit {
		fmt.Printf("CalcFit RESULT: pos=(%.2f,%.2f) hit=(%.2f,%.2f) normal=(%.2f,%.2f,%.2f) slide=(%.3f,%.3f)\n",
			r.X, r.Z, r.HitX, r.HitZ, r.NX, r.NY, r.NZ, r.SlideX, r.SlideZ)
	}

	return r
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
	tri.Walkable = ny*ny > slopeThresholdSq*lenSq

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
