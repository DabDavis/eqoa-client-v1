package terrain

import (
	"container/heap"
	"log"
	"math"
)

// SparseNav is a sparse node graph for pathfinding. Nodes are placed at
// navigation-critical points (wall edges, doorframes, ramp transitions)
// with edges connecting visible pairs.
type SparseNav struct {
	Nodes []SparseNode
	Edges [][]SparseEdge // adjacency list
}

// SparseNode is a navigation waypoint with exact world position.
type SparseNode struct {
	X, Y, Z float32
}

// SparseEdge connects two nodes with a traversal cost.
type SparseEdge struct {
	To   int
	Cost float32
}

// SparseWaypoint is a world-space point on a sparse-pathfound route.
type SparseWaypoint struct {
	X, Y, Z float32
}

// BuildSparseNav creates a sparse navigation graph from collision data.
// Nodes placed at: wall-adjacent walkable positions + sparse open area fill.
// Edges connect visible node pairs (Bresenham line-of-sight on blocked grid).
func BuildSparseNav(zt *ZoneTerrain, sx, sy, sz, gx, gy, gz float32) (*SparseNav, []SparseWaypoint) {
	if zt == nil || len(zt.CollTiles) == 0 {
		return nil, nil
	}

	const margin = float32(40.0)
	const openSpacing = float32(8.0) // sparse nodes in open areas
	const edgeMaxDist = float32(15.0)
	const cellSize = float32(1.0) // for blocked grid (line-of-sight checks)

	// Bounding box
	minX := sx; if gx < minX { minX = gx }; minX -= margin
	minZ := sz; if gz < minZ { minZ = gz }; minZ -= margin
	maxX := sx; if gx > maxX { maxX = gx }; maxX += margin
	maxZ := sz; if gz > maxZ { maxZ = gz }; maxZ += margin

	gridW := int(math.Ceil(float64((maxX - minX) / cellSize)))
	gridH := int(math.Ceil(float64((maxZ - minZ) / cellSize)))
	if gridW <= 0 || gridH <= 0 || gridW*gridH > 500000 {
		return nil, nil
	}

	// Build blocked grid (same as FindGridPath) for line-of-sight checks
	blocked := make([]bool, gridW*gridH)

	tMinTX := int(math.Floor(float64(minX / TileSize)))
	tMaxTX := int(math.Floor(float64(maxX / TileSize)))
	tMinTZ := int(math.Floor(float64(minZ / TileSize)))
	tMaxTZ := int(math.Floor(float64(maxZ / TileSize)))

	// Collect nodes from wall-adjacent walkable triangles
	type nodeCandidate struct {
		x, y, z float32
	}

	// Spatial bucket for deduplication (avoid nodes too close together)
	type bucketKey struct{ bx, bz int }
	const bucketSize = 3.0
	placed := make(map[bucketKey]bool)

	var candidates []nodeCandidate

	addCandidate := func(x, y, z float32) {
		bk := bucketKey{int(x / bucketSize), int(z / bucketSize)}
		if placed[bk] {
			return
		}
		placed[bk] = true
		candidates = append(candidates, nodeCandidate{x, y, z})
	}

	for tz := tMinTZ; tz <= tMaxTZ; tz++ {
		for tx := tMinTX; tx <= tMaxTX; tx++ {
			tile := zt.CollTiles[TileKey{TX: tx, TZ: tz}]
			if tile == nil {
				continue
			}

			// First pass: mark blocked cells (walls)
			for i := range tile.Triangles {
				tri := &tile.Triangles[i]
				if tri.Walkable {
					continue
				}
				triMinY := tri.Y0
				if tri.Y1 < triMinY { triMinY = tri.Y1 }
				if tri.Y2 < triMinY { triMinY = tri.Y2 }
				triMaxY := tri.Y0
				if tri.Y1 > triMaxY { triMaxY = tri.Y1 }
				if tri.Y2 > triMaxY { triMaxY = tri.Y2 }
				if triMinY > sy+3 || triMaxY < sy-1 {
					continue
				}

				gx0 := int((tri.MinX - 0.2 - minX) / cellSize)
				gx1 := int((tri.MaxX + 0.2 - minX) / cellSize)
				gz0 := int((tri.MinZ - 0.2 - minZ) / cellSize)
				gz1 := int((tri.MaxZ + 0.2 - minZ) / cellSize)
				if gx0 < 0 { gx0 = 0 }
				if gz0 < 0 { gz0 = 0 }
				if gx1 >= gridW { gx1 = gridW - 1 }
				if gz1 >= gridH { gz1 = gridH - 1 }

				for gzi := gz0; gzi <= gz1; gzi++ {
					for gxi := gx0; gxi <= gx1; gxi++ {
						cx := minX + (float32(gxi)+0.5)*cellSize
						cz := minZ + (float32(gzi)+0.5)*cellSize
						if pointNearTriXZ(cx, cz, tri.X0, tri.Z0, tri.X1, tri.Z1, tri.X2, tri.Z2, cellSize*0.5+0.2) {
							blocked[gzi*gridW+gxi] = true
						}
					}
				}
			}

			// Second pass: find walkable triangles near walls → place nodes
			for i := range tile.Triangles {
				tri := &tile.Triangles[i]
				if !tri.Walkable {
					continue
				}
				cx := (tri.X0 + tri.X1 + tri.X2) / 3
				cy := (tri.Y0 + tri.Y1 + tri.Y2) / 3
				cz := (tri.Z0 + tri.Z1 + tri.Z2) / 3

				if cx < minX || cx > maxX || cz < minZ || cz > maxZ {
					continue
				}

				// Check if any vertex of this walkable triangle is near a wall
				// (grid cell at vertex position is blocked or adjacent to blocked)
				isNearWall := false
				for _, vx := range []float32{tri.X0, tri.X1, tri.X2} {
					for _, vz := range []float32{tri.Z0, tri.Z1, tri.Z2} {
						gxi := int((vx - minX) / cellSize)
						gzi := int((vz - minZ) / cellSize)
						// Check 3x3 around vertex
						for dz := -1; dz <= 1; dz++ {
							for dx := -1; dx <= 1; dx++ {
								nx := gxi + dx
								nz := gzi + dz
								if nx >= 0 && nx < gridW && nz >= 0 && nz < gridH {
									if blocked[nz*gridW+nx] {
										isNearWall = true
									}
								}
							}
						}
						if isNearWall { break }
					}
					if isNearWall { break }
				}

				if isNearWall {
					// Place node at centroid, offset slightly away from wall
					addCandidate(cx, cy, cz)
				}
			}
		}
	}

	// Add sparse open area nodes
	for oz := minZ + openSpacing; oz < maxZ; oz += openSpacing {
		for ox := minX + openSpacing; ox < maxX; ox += openSpacing {
			gxi := int((ox - minX) / cellSize)
			gzi := int((oz - minZ) / cellSize)
			if gxi >= 0 && gxi < gridW && gzi >= 0 && gzi < gridH && !blocked[gzi*gridW+gxi] {
				// Sample Y from nearest walkable collision triangle
				y := sy // fallback
				if h, ok := sampleYFromCollTiles(zt, ox, oz, sy); ok {
					y = h
				}
				addCandidate(ox, y, oz)
			}
		}
	}

	// Add start and goal as nodes
	addCandidate(sx, sy, sz)
	addCandidate(gx, gy, gz)

	if len(candidates) < 2 {
		return nil, nil
	}

	// Build node list
	nodes := make([]SparseNode, len(candidates))
	for i, c := range candidates {
		nodes[i] = SparseNode{X: c.x, Y: c.y, Z: c.z}
	}

	// Find start/goal node indices
	startIdx := len(nodes) - 2
	goalIdx := len(nodes) - 1

	// Build edges: connect visible node pairs within edgeMaxDist
	edges := make([][]SparseEdge, len(nodes))
	edgeMaxSq := edgeMaxDist * edgeMaxDist

	// Line-of-sight on blocked grid
	gridLOS := func(x0, z0, x1, z1 float32) bool {
		gx0 := int((x0 - minX) / cellSize)
		gz0 := int((z0 - minZ) / cellSize)
		gx1 := int((x1 - minX) / cellSize)
		gz1 := int((z1 - minZ) / cellSize)

		dx := gx1 - gx0; dz := gz1 - gz0
		if dx < 0 { dx = -dx }; if dz < 0 { dz = -dz }
		sx2 := 1; if gx0 > gx1 { sx2 = -1 }
		sz2 := 1; if gz0 > gz1 { sz2 = -1 }
		err := dx - dz
		for {
			if gx0 >= 0 && gx0 < gridW && gz0 >= 0 && gz0 < gridH {
				if blocked[gz0*gridW+gx0] { return false }
			}
			if gx0 == gx1 && gz0 == gz1 { return true }
			e2 := 2 * err
			if e2 > -dz { err -= dz; gx0 += sx2 }
			if e2 < dx { err += dx; gz0 += sz2 }
		}
	}

	edgeCount := 0
	for i := 0; i < len(nodes); i++ {
		for j := i + 1; j < len(nodes); j++ {
			dx := nodes[i].X - nodes[j].X
			dz := nodes[i].Z - nodes[j].Z
			distSq := dx*dx + dz*dz
			if distSq > edgeMaxSq {
				continue
			}

			// Y check: allow up to 3 units Y diff (stairs/ramps)
			dy := float32(math.Abs(float64(nodes[i].Y - nodes[j].Y)))
			if dy > 3.0 {
				continue
			}

			if gridLOS(nodes[i].X, nodes[i].Z, nodes[j].X, nodes[j].Z) {
				cost := float32(math.Sqrt(float64(distSq + dy*dy)))
				edges[i] = append(edges[i], SparseEdge{To: j, Cost: cost})
				edges[j] = append(edges[j], SparseEdge{To: i, Cost: cost})
				edgeCount++
			}
		}
	}

	log.Printf("sparsenav: %d nodes, %d edges (start=%d goal=%d)",
		len(nodes), edgeCount, startIdx, goalIdx)

	nav := &SparseNav{Nodes: nodes, Edges: edges}

	// A* on sparse graph
	path := nav.findPath(startIdx, goalIdx)
	return nav, path
}

// sampleYFromCollTiles gets Y from nearest walkable collision triangle centroid.
func sampleYFromCollTiles(zt *ZoneTerrain, x, z, refY float32) (float32, bool) {
	tx := int(math.Floor(float64(x / TileSize)))
	tz := int(math.Floor(float64(z / TileSize)))

	bestY := refY
	bestDist := float32(math.MaxFloat32)
	found := false

	for dtz := -1; dtz <= 1; dtz++ {
		for dtx := -1; dtx <= 1; dtx++ {
			tile := zt.CollTiles[TileKey{TX: tx + dtx, TZ: tz + dtz}]
			if tile == nil { continue }
			for i := range tile.Triangles {
				tri := &tile.Triangles[i]
				if !tri.Walkable { continue }
				cx := (tri.X0 + tri.X1 + tri.X2) / 3
				cz := (tri.Z0 + tri.Z1 + tri.Z2) / 3
				dx := x - cx; dz2 := z - cz
				d := dx*dx + dz2*dz2
				if d < bestDist {
					bestDist = d
					bestY = (tri.Y0 + tri.Y1 + tri.Y2) / 3
					found = true
				}
			}
		}
	}
	return bestY, found
}

func (nav *SparseNav) findPath(startIdx, goalIdx int) []SparseWaypoint {
	n := len(nav.Nodes)
	if startIdx < 0 || goalIdx < 0 || startIdx >= n || goalIdx >= n {
		return nil
	}
	if startIdx == goalIdx {
		g := nav.Nodes[goalIdx]
		return []SparseWaypoint{{g.X, g.Y, g.Z}}
	}

	gScore := make([]float32, n)
	parent := make([]int, n)
	closed := make([]bool, n)
	for i := range gScore {
		gScore[i] = math.MaxFloat32
		parent[i] = -1
	}
	gScore[startIdx] = 0

	goal := &nav.Nodes[goalIdx]
	heuristic := func(idx int) float32 {
		nd := &nav.Nodes[idx]
		dx := nd.X - goal.X
		dy := nd.Y - goal.Y
		dz := nd.Z - goal.Z
		return float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))
	}

	pq := &sparseHeap{}
	heap.Init(pq)
	heap.Push(pq, sparseNode{idx: startIdx, f: heuristic(startIdx)})

	found := false
	for pq.Len() > 0 {
		cur := heap.Pop(pq).(sparseNode)
		if cur.idx == goalIdx { found = true; break }
		if closed[cur.idx] { continue }
		closed[cur.idx] = true

		for _, edge := range nav.Edges[cur.idx] {
			if closed[edge.To] { continue }
			newG := gScore[cur.idx] + edge.Cost
			if newG < gScore[edge.To] {
				gScore[edge.To] = newG
				parent[edge.To] = cur.idx
				heap.Push(pq, sparseNode{idx: edge.To, f: newG + heuristic(edge.To)})
			}
		}
	}

	if !found {
		log.Printf("sparsenav: no path from node %d to %d", startIdx, goalIdx)
		return nil
	}

	// Reconstruct
	var path []SparseWaypoint
	idx := goalIdx
	for idx != startIdx && idx >= 0 {
		nd := nav.Nodes[idx]
		path = append(path, SparseWaypoint{nd.X, nd.Y, nd.Z})
		idx = parent[idx]
	}
	// Reverse
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}

	return path
}

// --- priority queue ---
type sparseNode struct {
	idx int
	f   float32
}
type sparseHeap []sparseNode
func (h sparseHeap) Len() int            { return len(h) }
func (h sparseHeap) Less(i, j int) bool  { return h[i].f < h[j].f }
func (h sparseHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *sparseHeap) Push(x interface{}) { *h = append(*h, x.(sparseNode)) }
func (h *sparseHeap) Pop() interface{} {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}
