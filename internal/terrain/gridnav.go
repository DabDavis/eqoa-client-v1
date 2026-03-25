package terrain

import (
	"container/heap"
	"log"
	"math"
)

// GridWaypoint is a world-space point on a grid-pathfound route.
type GridWaypoint struct {
	X, Z float32
	Y    float32 // ground Y from collision triangle (for SampleHeight refY hint)
}

// FindGridPath builds a local occupancy grid around the player→target area
// and runs A* on it. The grid covers the bounding box of start→goal + margin,
// using collision data from CollTiles. Returns smoothed waypoints or nil.
func FindGridPath(zt *ZoneTerrain, sx, sy, sz, gx, gy, gz float32) []GridWaypoint {
	// Try with normal margin first, then retry with larger margin if no path
	path := findGridPathInner(zt, sx, sy, sz, gx, gy, gz, 40.0)
	if path != nil {
		return path
	}
	return findGridPathInner(zt, sx, sy, sz, gx, gy, gz, 80.0)
}

func findGridPathInner(zt *ZoneTerrain, sx, sy, sz, gx, gy, gz, margin float32) []GridWaypoint {
	_ = gy // reserved for future Y-aware pathfinding
	if zt == nil || len(zt.CollTiles) == 0 {
		return nil
	}

	const cellSize = float32(1.0) // 1-unit cells for accurate wall/doorway detection
	const charClearance = float32(0.2)

	// Bounding box of start→goal + margin
	minX := sx
	if gx < minX { minX = gx }
	minX -= margin
	minZ := sz
	if gz < minZ { minZ = gz }
	minZ -= margin
	maxX := sx
	if gx > maxX { maxX = gx }
	maxX += margin
	maxZ := sz
	if gz > maxZ { maxZ = gz }
	maxZ += margin

	width := int(math.Ceil(float64((maxX - minX) / cellSize)))
	height := int(math.Ceil(float64((maxZ - minZ) / cellSize)))

	if width <= 0 || height <= 0 || width*height > 500000 {
		return nil // safety
	}

	// Build local blocked grid from wall collision triangles
	blocked := make([]bool, width*height)

	// Scan CollTiles that overlap our local area
	tMinTX := int(math.Floor(float64(minX / TileSize)))
	tMaxTX := int(math.Floor(float64(maxX / TileSize)))
	tMinTZ := int(math.Floor(float64(minZ / TileSize)))
	tMaxTZ := int(math.Floor(float64(maxZ / TileSize)))

	// Cell Y from walkable collision triangles (no SampleHeight needed).
	// For each walkable triangle covering a cell, record the Y centroid.
	// Pick the Y closest to playerY when multiple triangles overlap.
	cellY := make([]float32, width*height)
	for i := range cellY {
		cellY[i] = float32(math.NaN())
	}

	for tz := tMinTZ; tz <= tMaxTZ; tz++ {
		for tx := tMinTX; tx <= tMaxTX; tx++ {
			tile := zt.CollTiles[TileKey{TX: tx, TZ: tz}]
			if tile == nil {
				continue
			}
			for i := range tile.Triangles {
				tri := &tile.Triangles[i]

				triMinY := tri.Y0
				if tri.Y1 < triMinY { triMinY = tri.Y1 }
				if tri.Y2 < triMinY { triMinY = tri.Y2 }
				triMaxY := tri.Y0
				if tri.Y1 > triMaxY { triMaxY = tri.Y1 }
				if tri.Y2 > triMaxY { triMaxY = tri.Y2 }

				if tri.Walkable {
					// Record Y for walkable cells — use triangle centroid Y.
					// Bias toward target Y for cells near goal so ramps get
					// ramp height instead of ground height below.
					triCY := (tri.Y0 + tri.Y1 + tri.Y2) / 3

					gx0 := int((tri.MinX - minX) / cellSize)
					gx1 := int((tri.MaxX - minX) / cellSize)
					gz0 := int((tri.MinZ - minZ) / cellSize)
					gz1 := int((tri.MaxZ - minZ) / cellSize)
					if gx0 < 0 { gx0 = 0 }
					if gz0 < 0 { gz0 = 0 }
					if gx1 >= width { gx1 = width - 1 }
					if gz1 >= height { gz1 = height - 1 }

					for gzi := gz0; gzi <= gz1; gzi++ {
						for gxi := gx0; gxi <= gx1; gxi++ {
							idx := gzi*width + gxi
							existing := cellY[idx]

							// Pick Y closest to player Y
							if math.IsNaN(float64(existing)) ||
								float32(math.Abs(float64(triCY-sy))) < float32(math.Abs(float64(existing-sy))) {
								cellY[idx] = triCY
							}
						}
					}
				} else {
					// Wall triangle — block cells
					if triMinY > sy+3 || triMaxY < sy-1 {
						continue
					}
					triMinX := tri.MinX - charClearance
					triMaxX := tri.MaxX + charClearance
					triMinZ := tri.MinZ - charClearance
					triMaxZ := tri.MaxZ + charClearance

					gx0 := int((triMinX - minX) / cellSize)
					gx1 := int((triMaxX - minX) / cellSize)
					gz0 := int((triMinZ - minZ) / cellSize)
					gz1 := int((triMaxZ - minZ) / cellSize)
					if gx0 < 0 { gx0 = 0 }
					if gz0 < 0 { gz0 = 0 }
					if gx1 >= width { gx1 = width - 1 }
					if gz1 >= height { gz1 = height - 1 }

					for gzi := gz0; gzi <= gz1; gzi++ {
						for gxi := gx0; gxi <= gx1; gxi++ {
							cx := minX + (float32(gxi)+0.5)*cellSize
							cz := minZ + (float32(gzi)+0.5)*cellSize
							if pointNearTriXZ(cx, cz, tri.X0, tri.Z0, tri.X1, tri.Z1, tri.X2, tri.Z2, cellSize*0.5+charClearance) {
								blocked[gzi*width+gxi] = true
							}
						}
					}
				}
			}
		}
	}

	blockedCount := 0
	for _, b := range blocked {
		if b { blockedCount++ }
	}
	log.Printf("gridnav: %dx%d local grid (%d cells, %d blocked, %.0f%% walkable)",
		width, height, width*height, blockedCount,
		100*float64(width*height-blockedCount)/float64(width*height))

	// Convert start/goal to grid coords
	startGX := int((sx - minX) / cellSize)
	startGZ := int((sz - minZ) / cellSize)
	goalGX := int((gx - minX) / cellSize)
	goalGZ := int((gz - minZ) / cellSize)

	// Clamp
	clamp := func(v, lo, hi int) int {
		if v < lo { return lo }
		if v > hi { return hi }
		return v
	}
	startGX = clamp(startGX, 0, width-1)
	startGZ = clamp(startGZ, 0, height-1)
	goalGX = clamp(goalGX, 0, width-1)
	goalGZ = clamp(goalGZ, 0, height-1)

	startBlocked := blocked[startGZ*width+startGX]
	goalBlocked := blocked[goalGZ*width+goalGX]
	log.Printf("gridnav: start(%d,%d)=%v goal(%d,%d)=%v",
		startGX, startGZ, startBlocked, goalGX, goalGZ, goalBlocked)

	// Snap blocked start/goal to nearest open cell
	snapOpen := func(gxi, gzi int) (int, int) {
		if gxi >= 0 && gxi < width && gzi >= 0 && gzi < height && !blocked[gzi*width+gxi] {
			return gxi, gzi
		}
		for r := 1; r < 30; r++ {
			for dz := -r; dz <= r; dz++ {
				for dx := -r; dx <= r; dx++ {
					if dx != -r && dx != r && dz != -r && dz != r { continue }
					nx, nz := gxi+dx, gzi+dz
					if nx >= 0 && nx < width && nz >= 0 && nz < height && !blocked[nz*width+nx] {
						return nx, nz
					}
				}
			}
		}
		return gxi, gzi
	}
	startGX, startGZ = snapOpen(startGX, startGZ)
	goalGX, goalGZ = snapOpen(goalGX, goalGZ)

	if startGX == goalGX && startGZ == goalGZ {
		wx := minX + (float32(goalGX)+0.5)*cellSize
		wz := minZ + (float32(goalGZ)+0.5)*cellSize
		wy := cellY[goalGZ*width+goalGX]
		if math.IsNaN(float64(wy)) { wy = sy }
		return []GridWaypoint{{X: wx, Z: wz, Y: wy}}
	}

	// A* search
	n := width * height
	gScore := make([]float32, n)
	parent := make([]int, n)
	closed := make([]bool, n)
	for i := range gScore {
		gScore[i] = math.MaxFloat32
		parent[i] = -1
	}

	startIdx := startGZ*width + startGX
	goalIdx := goalGZ*width + goalGX
	gScore[startIdx] = 0

	heuristic := func(x, z int) float32 {
		dx := float32(x - goalGX)
		dz := float32(z - goalGZ)
		return float32(math.Sqrt(float64(dx*dx + dz*dz)))
	}

	isBlocked := func(x, z int) bool {
		if x < 0 || z < 0 || x >= width || z >= height { return true }
		return blocked[z*width+x]
	}

	pq := &gridHeap{}
	heap.Init(pq)
	heap.Push(pq, gridNode{idx: startIdx, x: startGX, z: startGZ, f: heuristic(startGX, startGZ)})

	dirs := [8][2]int{
		{0, 1}, {1, 0}, {0, -1}, {-1, 0},
		{1, 1}, {1, -1}, {-1, 1}, {-1, -1},
	}
	costs := [8]float32{1, 1, 1, 1, 1.414, 1.414, 1.414, 1.414}

	found := false
	maxIter := n
	if maxIter > 50000 { maxIter = 50000 }
	iter := 0

	for pq.Len() > 0 && iter < maxIter {
		iter++
		cur := heap.Pop(pq).(gridNode)
		if cur.idx == goalIdx { found = true; break }
		if closed[cur.idx] { continue }
		closed[cur.idx] = true

		for di, dir := range dirs {
			nx := cur.x + dir[0]
			nz := cur.z + dir[1]
			if isBlocked(nx, nz) { continue }
			// Diagonal corner-cut prevention
			if di >= 4 {
				if isBlocked(cur.x+dir[0], cur.z) || isBlocked(cur.x, cur.z+dir[1]) { continue }
			}
			nIdx := nz*width + nx
			if closed[nIdx] { continue }

			// Slope check using collision triangle Y (no SampleHeight)
			curCY := cellY[cur.idx]
			nbrCY := cellY[nIdx]
			if !math.IsNaN(float64(curCY)) && !math.IsNaN(float64(nbrCY)) {
				yDiff := float32(math.Abs(float64(nbrCY - curCY)))
				if yDiff > 2.5 { continue } // cliff — too steep
			}

			newG := gScore[cur.idx] + costs[di]
			// Slight cost for elevation change (prefer flat)
			if !math.IsNaN(float64(curCY)) && !math.IsNaN(float64(nbrCY)) {
				yDiff := float32(math.Abs(float64(nbrCY - curCY)))
				newG += yDiff * 0.3
			}
			if newG < gScore[nIdx] {
				gScore[nIdx] = newG
				parent[nIdx] = cur.idx
				heap.Push(pq, gridNode{idx: nIdx, x: nx, z: nz, f: newG + heuristic(nx, nz)})
			}
		}
	}

	if !found { return nil }

	// Reconstruct raw path
	type cell struct{ x, z int }
	var raw []cell
	idx := goalIdx
	for idx != startIdx && idx >= 0 {
		raw = append(raw, cell{idx % width, idx / width})
		idx = parent[idx]
	}
	for i, j := 0, len(raw)-1; i < j; i, j = i+1, j-1 {
		raw[i], raw[j] = raw[j], raw[i]
	}

	// Line-of-sight smoothing
	los := func(x0, z0, x1, z1 int) bool {
		dx := x1 - x0; dz := z1 - z0
		if dx < 0 { dx = -dx }; if dz < 0 { dz = -dz }
		sx := 1; if x0 > x1 { sx = -1 }
		sz := 1; if z0 > z1 { sz = -1 }
		err := dx - dz
		for {
			if isBlocked(x0, z0) { return false }
			if x0 == x1 && z0 == z1 { return true }
			e2 := 2 * err
			if e2 > -dz { err -= dz; x0 += sx }
			if e2 < dx { err += dx; z0 += sz }
		}
	}

	toWaypoint := func(gxi, gzi int) GridWaypoint {
		wx := minX + (float32(gxi)+0.5)*cellSize
		wz := minZ + (float32(gzi)+0.5)*cellSize
		wy := cellY[gzi*width+gxi]
		if math.IsNaN(float64(wy)) {
			wy = sy // fallback to player Y
		}
		return GridWaypoint{X: wx, Z: wz, Y: wy}
	}

	var smoothed []GridWaypoint
	i := 0
	for i < len(raw) {
		smoothed = append(smoothed, toWaypoint(raw[i].x, raw[i].z))
		farthest := i + 1
		for j := len(raw) - 1; j > i+1; j-- {
			if los(raw[i].x, raw[i].z, raw[j].x, raw[j].z) {
				farthest = j
				break
			}
		}
		i = farthest
	}
	// Always include goal
	gw := toWaypoint(goalGX, goalGZ)
	if len(smoothed) == 0 || smoothed[len(smoothed)-1].X != gw.X || smoothed[len(smoothed)-1].Z != gw.Z {
		smoothed = append(smoothed, gw)
	}

	return smoothed
}

// pointNearTriXZ tests if point (px,pz) is within dist of triangle edges in XZ.
func pointNearTriXZ(px, pz, ax, az, bx, bz, cx, cz, dist float32) bool {
	distSq := dist * dist
	// Check distance to each edge
	if ptSegDistSq(px, pz, ax, az, bx, bz) < distSq { return true }
	if ptSegDistSq(px, pz, bx, bz, cx, cz) < distSq { return true }
	if ptSegDistSq(px, pz, cx, cz, ax, az) < distSq { return true }
	return false
}

// ptSegDistSq returns squared distance from point to line segment in 2D.
func ptSegDistSq(px, pz, ax, az, bx, bz float32) float32 {
	dx := bx - ax
	dz := bz - az
	lenSq := dx*dx + dz*dz
	if lenSq < 0.0001 {
		ddx := px - ax
		ddz := pz - az
		return ddx*ddx + ddz*ddz
	}
	t := ((px-ax)*dx + (pz-az)*dz) / lenSq
	if t < 0 { t = 0 }
	if t > 1 { t = 1 }
	cx := ax + t*dx
	cz := az + t*dz
	ddx := px - cx
	ddz := pz - cz
	return ddx*ddx + ddz*ddz
}

// --- A* priority queue ---

type gridNode struct {
	idx  int
	x, z int
	f    float32
}

type gridHeap []gridNode

func (h gridHeap) Len() int            { return len(h) }
func (h gridHeap) Less(i, j int) bool  { return h[i].f < h[j].f }
func (h gridHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *gridHeap) Push(x interface{}) { *h = append(*h, x.(gridNode)) }
func (h *gridHeap) Pop() interface{} {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}
