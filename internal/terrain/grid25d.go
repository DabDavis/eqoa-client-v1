package terrain

import (
	"container/heap"
	"log"
	"math"
)

// FindGridPath25D builds a 2.5D local grid (multiple Y layers per cell)
// and runs A* on it. Handles ramps, stairs, bridges, and multi-story
// buildings by tracking separate walkable surfaces at each XZ position.
func FindGridPath25D(zt *ZoneTerrain, sx, sy, sz, gx, gy, gz float32) []GridWaypoint {
	path := findGrid25DInner(zt, sx, sy, sz, gx, gy, gz, 40.0)
	if path != nil {
		return path
	}
	return findGrid25DInner(zt, sx, sy, sz, gx, gy, gz, 80.0)
}

// cellLayer is one walkable surface at a cell position.
type cellLayer struct {
	y float32 // ground height
}

func findGrid25DInner(zt *ZoneTerrain, sx, sy, sz, gx, gy, gz, margin float32) []GridWaypoint {
	if zt == nil || len(zt.CollTiles) == 0 {
		return nil
	}

	const cellSize = float32(1.0)
	const charClearance = float32(0.2)
	const layerMerge = float32(1.0) // merge Y values within 1 unit into one layer
	const maxStepY = float32(2.5)   // max Y diff between connected layers

	// Bounding box
	minX := sx; if gx < minX { minX = gx }; minX -= margin
	minZ := sz; if gz < minZ { minZ = gz }; minZ -= margin
	maxX := sx; if gx > maxX { maxX = gx }; maxX += margin
	maxZ := sz; if gz > maxZ { maxZ = gz }; maxZ += margin

	width := int(math.Ceil(float64((maxX - minX) / cellSize)))
	height := int(math.Ceil(float64((maxZ - minZ) / cellSize)))
	if width <= 0 || height <= 0 || width*height > 500000 {
		return nil
	}

	// Per-cell: list of walkable Y layers + blocked flags
	type cellData struct {
		layers  []cellLayer
		blocked bool // wall at player's Y level (for 2D fallback compat)
	}
	cells := make([]cellData, width*height)

	// Scan CollTiles
	tMinTX := int(math.Floor(float64(minX / TileSize)))
	tMaxTX := int(math.Floor(float64(maxX / TileSize)))
	tMinTZ := int(math.Floor(float64(minZ / TileSize)))
	tMaxTZ := int(math.Floor(float64(maxZ / TileSize)))

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
					// Add Y layer at walkable triangle centroid
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
							// Merge into existing layer if Y is close, else add new
							merged := false
							for li := range cells[idx].layers {
								if float32(math.Abs(float64(cells[idx].layers[li].y-triCY))) < layerMerge {
									// Average the Y values
									cells[idx].layers[li].y = (cells[idx].layers[li].y + triCY) / 2
									merged = true
									break
								}
							}
							if !merged {
								cells[idx].layers = append(cells[idx].layers, cellLayer{y: triCY})
							}
						}
					}
				} else {
					// Wall — mark cells blocked (Y-filtered)
					if triMinY > sy+3 || triMaxY < sy-1 {
						continue
					}
					triMinX2 := tri.MinX - charClearance
					triMaxX2 := tri.MaxX + charClearance
					triMinZ2 := tri.MinZ - charClearance
					triMaxZ2 := tri.MaxZ + charClearance

					gx0 := int((triMinX2 - minX) / cellSize)
					gx1 := int((triMaxX2 - minX) / cellSize)
					gz0 := int((triMinZ2 - minZ) / cellSize)
					gz1 := int((triMaxZ2 - minZ) / cellSize)
					if gx0 < 0 { gx0 = 0 }
					if gz0 < 0 { gz0 = 0 }
					if gx1 >= width { gx1 = width - 1 }
					if gz1 >= height { gz1 = height - 1 }

					for gzi := gz0; gzi <= gz1; gzi++ {
						for gxi := gx0; gxi <= gx1; gxi++ {
							cx := minX + (float32(gxi)+0.5)*cellSize
							cz := minZ + (float32(gzi)+0.5)*cellSize
							if pointNearTriXZ(cx, cz, tri.X0, tri.Z0, tri.X1, tri.Z1, tri.X2, tri.Z2, cellSize*0.5+charClearance) {
								// Mark cell as blocked for LOS checks (same as 2D grid).
								// Don't remove layers — A* still needs them for Y transitions.
								// The blocked flag prevents LOS smoothing through walls.
								cells[gzi*width+gxi].blocked = true
							}
						}
					}
				}
			}
		}
	}

	// Build A* node index: each (cell, layer) pair is a node
	type nodeKey struct {
		cellIdx  int
		layerIdx int
	}

	// Assign node IDs
	var nodeKeys []nodeKey
	nodeAt := make([][]int, width*height) // cell → list of node IDs per layer

	for ci := 0; ci < width*height; ci++ {
		for li := range cells[ci].layers {
			nid := len(nodeKeys)
			nodeKeys = append(nodeKeys, nodeKey{ci, li})
			nodeAt[ci] = append(nodeAt[ci], nid)
		}
	}

	n := len(nodeKeys)
	if n == 0 {
		return nil
	}

	// Find start/goal nodes (closest layer Y to player/target Y)
	findNode := func(wx, wz, wy float32) int {
		gxi := int((wx - minX) / cellSize)
		gzi := int((wz - minZ) / cellSize)
		// Snap to bounds + search nearby if needed
		bestNode := -1
		bestDist := float32(math.MaxFloat32)
		for r := 0; r < 20; r++ {
			for dz := -r; dz <= r; dz++ {
				for dx := -r; dx <= r; dx++ {
					if r > 0 && dx != -r && dx != r && dz != -r && dz != r { continue }
					nx := gxi + dx
					nz := gzi + dz
					if nx < 0 || nz < 0 || nx >= width || nz >= height { continue }
					ci := nz*width + nx
					for _, nid := range nodeAt[ci] {
						ly := cells[ci].layers[nodeKeys[nid].layerIdx].y
						d := float32(math.Abs(float64(ly - wy)))
						if d < bestDist {
							bestDist = d
							bestNode = nid
						}
					}
				}
			}
			if bestNode >= 0 { break }
		}
		return bestNode
	}

	startNode := findNode(sx, sz, sy)
	goalNode := findNode(gx, gz, gy)
	if startNode < 0 || goalNode < 0 {
		return nil
	}
	if startNode == goalNode {
		nk := nodeKeys[goalNode]
		ci := nk.cellIdx
		gxi := ci % width; gzi := ci / width
		wx := minX + (float32(gxi)+0.5)*cellSize
		wz := minZ + (float32(gzi)+0.5)*cellSize
		return []GridWaypoint{{X: wx, Z: wz, Y: cells[ci].layers[nk.layerIdx].y}}
	}

	// A* on 2.5D graph
	gScore := make([]float32, n)
	parent := make([]int, n)
	closed := make([]bool, n)
	for i := range gScore {
		gScore[i] = math.MaxFloat32
		parent[i] = -1
	}
	gScore[startNode] = 0

	goalNK := nodeKeys[goalNode]
	goalCi := goalNK.cellIdx
	goalGX := goalCi % width; goalGZ := goalCi / width
	goalY := cells[goalCi].layers[goalNK.layerIdx].y

	heuristic := func(nid int) float32 {
		nk := nodeKeys[nid]
		ci := nk.cellIdx
		gxi := ci % width; gzi := ci / width
		dx := float32(gxi - goalGX)
		dz := float32(gzi - goalGZ)
		dy := cells[ci].layers[nk.layerIdx].y - goalY
		return float32(math.Sqrt(float64(dx*dx + dz*dz + dy*dy)))
	}

	pq := &grid25dHeap{}
	heap.Init(pq)
	heap.Push(pq, grid25dNode{idx: startNode, f: heuristic(startNode)})

	dirs := [8][2]int{
		{0, 1}, {1, 0}, {0, -1}, {-1, 0},
		{1, 1}, {1, -1}, {-1, 1}, {-1, -1},
	}
	baseCosts := [8]float32{1, 1, 1, 1, 1.414, 1.414, 1.414, 1.414}

	found := false
	maxIter := n * 2
	if maxIter > 100000 { maxIter = 100000 }
	iter := 0

	for pq.Len() > 0 && iter < maxIter {
		iter++
		cur := heap.Pop(pq).(grid25dNode)
		if cur.idx == goalNode { found = true; break }
		if closed[cur.idx] { continue }
		closed[cur.idx] = true

		curNK := nodeKeys[cur.idx]
		curCi := curNK.cellIdx
		curGX := curCi % width; curGZ := curCi / width
		curY := cells[curCi].layers[curNK.layerIdx].y

		for di, dir := range dirs {
			nx := curGX + dir[0]
			nz := curGZ + dir[1]
			if nx < 0 || nz < 0 || nx >= width || nz >= height { continue }

			// Diagonal corner-cut prevention
			if di >= 4 {
				c1 := (curGZ+dir[1])*width + curGX
				c2 := curGZ*width + (curGX + dir[0])
				if c1 < 0 || c1 >= width*height || c2 < 0 || c2 >= width*height { continue }
				if cells[c1].blocked || len(cells[c1].layers) == 0 ||
					cells[c2].blocked || len(cells[c2].layers) == 0 { continue }
			}

			nci := nz*width + nx
			// Skip blocked cells (walls) — same as 2D grid
			if cells[nci].blocked { continue }
			// Try all layers at neighbor cell
			for _, nnid := range nodeAt[nci] {
				if closed[nnid] { continue }
				nbrY := cells[nci].layers[nodeKeys[nnid].layerIdx].y
				yDiff := float32(math.Abs(float64(nbrY - curY)))
				if yDiff > maxStepY { continue } // too steep

				cost := baseCosts[di] + yDiff*0.3 // slight Y penalty
				newG := gScore[cur.idx] + cost
				if newG < gScore[nnid] {
					gScore[nnid] = newG
					parent[nnid] = cur.idx
					heap.Push(pq, grid25dNode{idx: nnid, f: newG + heuristic(nnid)})
				}
			}
		}
	}

	if !found { return nil }

	// Reconstruct path
	type rawPt struct{ gx, gz int; y float32 }
	var raw []rawPt
	idx := goalNode
	for idx != startNode && idx >= 0 {
		nk := nodeKeys[idx]
		ci := nk.cellIdx
		raw = append(raw, rawPt{ci % width, ci / width, cells[ci].layers[nk.layerIdx].y})
		idx = parent[idx]
	}
	for i, j := 0, len(raw)-1; i < j; i, j = i+1, j-1 {
		raw[i], raw[j] = raw[j], raw[i]
	}

	// Line-of-sight smoothing — use wall blocked flag (not just empty layers)
	isBlocked := func(gxi, gzi int) bool {
		if gxi < 0 || gzi < 0 || gxi >= width || gzi >= height { return true }
		ci := gzi*width + gxi
		return cells[ci].blocked || len(cells[ci].layers) == 0
	}
	los := func(x0, z0, x1, z1 int) bool {
		dx := x1 - x0; dz := z1 - z0
		if dx < 0 { dx = -dx }; if dz < 0 { dz = -dz }
		sx2 := 1; if x0 > x1 { sx2 = -1 }
		sz2 := 1; if z0 > z1 { sz2 = -1 }
		err := dx - dz
		for {
			if isBlocked(x0, z0) { return false }
			if x0 == x1 && z0 == z1 { return true }
			e2 := 2 * err
			if e2 > -dz { err -= dz; x0 += sx2 }
			if e2 < dx { err += dx; z0 += sz2 }
		}
	}

	var smoothed []GridWaypoint
	i := 0
	for i < len(raw) {
		wx := minX + (float32(raw[i].gx)+0.5)*cellSize
		wz := minZ + (float32(raw[i].gz)+0.5)*cellSize
		smoothed = append(smoothed, GridWaypoint{X: wx, Z: wz, Y: raw[i].y})
		farthest := i + 1
		for j := len(raw) - 1; j > i+1; j-- {
			// Only smooth if Y difference along the segment is small
			yRange := float32(math.Abs(float64(raw[j].y - raw[i].y)))
			if yRange > 3.0 { continue } // don't smooth across floors
			if los(raw[i].gx, raw[i].gz, raw[j].gx, raw[j].gz) {
				farthest = j
				break
			}
		}
		i = farthest
	}
	gw := raw[len(raw)-1]
	gwx := minX + (float32(gw.gx)+0.5)*cellSize
	gwz := minZ + (float32(gw.gz)+0.5)*cellSize
	if len(smoothed) == 0 || smoothed[len(smoothed)-1].X != gwx || smoothed[len(smoothed)-1].Z != gwz {
		smoothed = append(smoothed, GridWaypoint{X: gwx, Z: gwz, Y: gw.y})
	}

	// Insert ramp entrance waypoints at Y transitions.
	// When consecutive waypoints have Y diff > 1, insert an intermediate
	// waypoint at the LOWER Y, offset toward the higher waypoint.
	// This makes the character walk TO the ramp entrance at ground level,
	// then naturally walk UP the ramp via normal SampleHeight terrain-following.
	if len(smoothed) >= 2 {
		var withRamps []GridWaypoint
		withRamps = append(withRamps, smoothed[0])
		for i := 1; i < len(smoothed); i++ {
			prev := smoothed[i-1]
			cur := smoothed[i]
			yDiff := cur.Y - prev.Y
			if yDiff > 1.0 {
				// Ascending: insert ground-level waypoint near ramp base.
				// Offset 2 units from prev toward cur (approach the ramp).
				dx := cur.X - prev.X
				dz := cur.Z - prev.Z
				dist := float32(math.Sqrt(float64(dx*dx + dz*dz)))
				if dist > 0.1 {
					// Ramp entrance: same Y as prev (ground), XZ offset toward ramp
					t := float32(2.0) / dist
					if t > 0.8 { t = 0.8 }
					withRamps = append(withRamps, GridWaypoint{
						X: prev.X + dx*t,
						Z: prev.Z + dz*t,
						Y: prev.Y, // ground level
					})
				}
			} else if yDiff < -1.0 {
				// Descending: insert waypoint at top of ramp before going down.
				dx := cur.X - prev.X
				dz := cur.Z - prev.Z
				dist := float32(math.Sqrt(float64(dx*dx + dz*dz)))
				if dist > 0.1 {
					t := float32(2.0) / dist
					if t > 0.8 { t = 0.8 }
					withRamps = append(withRamps, GridWaypoint{
						X: prev.X + dx*t,
						Z: prev.Z + dz*t,
						Y: prev.Y, // upper level
					})
				}
			}
			withRamps = append(withRamps, cur)
		}
		smoothed = withRamps
	}

	totalLayers := 0
	for ci := range cells { totalLayers += len(cells[ci].layers) }
	log.Printf("grid25d: %dx%d grid, %d nodes (%d cells, avg %.1f layers), %d iter",
		width, height, n, width*height, float64(totalLayers)/float64(width*height), iter)

	return smoothed
}

// --- A* priority queue ---
type grid25dNode struct {
	idx int
	f   float32
}
type grid25dHeap []grid25dNode
func (h grid25dHeap) Len() int            { return len(h) }
func (h grid25dHeap) Less(i, j int) bool  { return h[i].f < h[j].f }
func (h grid25dHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *grid25dHeap) Push(x interface{}) { *h = append(*h, x.(grid25dNode)) }
func (h *grid25dHeap) Pop() interface{} {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}
