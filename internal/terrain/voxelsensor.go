package terrain

import "math"

// VoxelSensor is a small 3D grid around the player that detects walkable
// paths including stairs, ramps, and slopes. Used for tactical movement
// decisions — the 2D grid A* provides strategic direction, the sensor
// picks the best immediate step.
type VoxelSensor struct {
	walkable []bool // 3D grid: walkable[y*sizeX*sizeZ + z*sizeX + x]
	sizeX    int
	sizeZ    int
	sizeY    int
	originX  float32 // world position of voxel (0,0,0)
	originY  float32
	originZ  float32
	cellSize float32
	cellH    float32 // vertical cell size
}

// BuildVoxelSensor creates a small 3D walkability grid around (cx, cy, cz).
// Scans collision triangles to determine which voxels have walkable ground
// and are not blocked by walls.
func BuildVoxelSensor(zt *ZoneTerrain, cx, cy, cz float32) *VoxelSensor {
	if zt == nil {
		return nil
	}

	const cellSize = float32(1.0)
	const cellH = float32(1.5) // vertical cell height (character is ~2u tall)
	const radiusXZ = float32(10.0)
	const radiusY = float32(8.0)

	sizeX := int(math.Ceil(float64(radiusXZ*2/cellSize))) + 1
	sizeZ := int(math.Ceil(float64(radiusXZ*2/cellSize))) + 1
	sizeY := int(math.Ceil(float64(radiusY*2/cellH))) + 1

	originX := cx - radiusXZ
	originY := cy - radiusY
	originZ := cz - radiusXZ

	n := sizeX * sizeZ * sizeY
	hasFloor := make([]bool, n) // voxel has walkable triangle in its Y range
	hasWall := make([]bool, n)  // voxel is blocked by a wall triangle

	// Scan nearby CollTiles
	tMinTX := int(math.Floor(float64(originX / TileSize)))
	tMaxTX := int(math.Floor(float64((originX + float32(sizeX)*cellSize) / TileSize)))
	tMinTZ := int(math.Floor(float64(originZ / TileSize)))
	tMaxTZ := int(math.Floor(float64((originZ + float32(sizeZ)*cellSize) / TileSize)))

	for tz := tMinTZ; tz <= tMaxTZ; tz++ {
		for tx := tMinTX; tx <= tMaxTX; tx++ {
			tile := zt.CollTiles[TileKey{TX: tx, TZ: tz}]
			if tile == nil {
				continue
			}
			for i := range tile.Triangles {
				tri := &tile.Triangles[i]

				// Triangle Y range
				triMinY := tri.Y0
				if tri.Y1 < triMinY { triMinY = tri.Y1 }
				if tri.Y2 < triMinY { triMinY = tri.Y2 }
				triMaxY := tri.Y0
				if tri.Y1 > triMaxY { triMaxY = tri.Y1 }
				if tri.Y2 > triMaxY { triMaxY = tri.Y2 }

				// Skip if entirely outside sensor Y range
				if triMinY > originY+float32(sizeY)*cellH || triMaxY < originY {
					continue
				}

				// XZ range in grid coords
				gx0 := int((tri.MinX - originX) / cellSize)
				gx1 := int((tri.MaxX - originX) / cellSize)
				gz0 := int((tri.MinZ - originZ) / cellSize)
				gz1 := int((tri.MaxZ - originZ) / cellSize)
				if gx0 < 0 { gx0 = 0 }
				if gz0 < 0 { gz0 = 0 }
				if gx1 >= sizeX { gx1 = sizeX - 1 }
				if gz1 >= sizeZ { gz1 = sizeZ - 1 }

				// Y range in grid coords
				gy0 := int((triMinY - originY) / cellH)
				gy1 := int((triMaxY - originY) / cellH)
				if gy0 < 0 { gy0 = 0 }
				if gy1 >= sizeY { gy1 = sizeY - 1 }

				if tri.Walkable {
					// Mark floor voxels — only cells where the triangle actually is
					for gy := gy0; gy <= gy1; gy++ {
						for gz := gz0; gz <= gz1; gz++ {
							for gx := gx0; gx <= gx1; gx++ {
								cellCX := originX + (float32(gx)+0.5)*cellSize
								cellCZ := originZ + (float32(gz)+0.5)*cellSize
								if pointNearTriXZ(cellCX, cellCZ, tri.X0, tri.Z0, tri.X1, tri.Z1, tri.X2, tri.Z2, cellSize*0.7) {
									hasFloor[gy*sizeX*sizeZ+gz*sizeX+gx] = true
								}
							}
						}
					}
				} else {
					// Mark wall voxels
					for gy := gy0; gy <= gy1; gy++ {
						for gz := gz0; gz <= gz1; gz++ {
							for gx := gx0; gx <= gx1; gx++ {
								cellCX := originX + (float32(gx)+0.5)*cellSize
								cellCZ := originZ + (float32(gz)+0.5)*cellSize
								if pointNearTriXZ(cellCX, cellCZ, tri.X0, tri.Z0, tri.X1, tri.Z1, tri.X2, tri.Z2, cellSize*0.5+0.2) {
									hasWall[gy*sizeX*sizeZ+gz*sizeX+gx] = true
								}
							}
						}
					}
				}
			}
		}
	}

	// A voxel is walkable if it has a floor AND is not walled AND the cell above is not walled
	walkable := make([]bool, n)
	for gy := 0; gy < sizeY-1; gy++ {
		for gz := 0; gz < sizeZ; gz++ {
			for gx := 0; gx < sizeX; gx++ {
				idx := gy*sizeX*sizeZ + gz*sizeX + gx
				aboveIdx := (gy+1)*sizeX*sizeZ + gz*sizeX + gx
				walkable[idx] = hasFloor[idx] && !hasWall[idx] && !hasWall[aboveIdx]
			}
		}
	}

	return &VoxelSensor{
		walkable: walkable,
		sizeX:    sizeX,
		sizeZ:    sizeZ,
		sizeY:    sizeY,
		originX:  originX,
		originY:  originY,
		originZ:  originZ,
		cellSize: cellSize,
		cellH:    cellH,
	}
}

// BestStep finds the best walkable voxel to move toward, given a desired
// XZ direction (from the 2D path). Returns the world position of the best
// next step, and whether one was found.
func (vs *VoxelSensor) BestStep(px, py, pz, dirX, dirZ float32) (float32, float32, float32, bool) {
	if vs == nil {
		return px, py, pz, false
	}

	// Player's current voxel
	pgx := int((px - vs.originX) / vs.cellSize)
	pgz := int((pz - vs.originZ) / vs.cellSize)
	pgy := int((py - vs.originY) / vs.cellH)

	// Clamp
	if pgx < 0 { pgx = 0 }
	if pgz < 0 { pgz = 0 }
	if pgy < 0 { pgy = 0 }
	if pgx >= vs.sizeX { pgx = vs.sizeX - 1 }
	if pgz >= vs.sizeZ { pgz = vs.sizeZ - 1 }
	if pgy >= vs.sizeY { pgy = vs.sizeY - 1 }

	// Search neighbors: 3x3x3 cube around player voxel (26 directions)
	bestScore := float32(-math.MaxFloat32)
	bestX, bestY, bestZ := px, py, pz
	found := false

	for dy := -1; dy <= 1; dy++ {
		for dz := -1; dz <= 1; dz++ {
			for dx := -1; dx <= 1; dx++ {
				if dx == 0 && dy == 0 && dz == 0 {
					continue
				}
				nx := pgx + dx
				ny := pgy + dy
				nz := pgz + dz
				if nx < 0 || ny < 0 || nz < 0 || nx >= vs.sizeX || ny >= vs.sizeY || nz >= vs.sizeZ {
					continue
				}
				if !vs.walkable[ny*vs.sizeX*vs.sizeZ+nz*vs.sizeX+nx] {
					continue
				}

				// Score: how much this step aligns with desired direction
				wx := vs.originX + (float32(nx)+0.5)*vs.cellSize
				wz := vs.originZ + (float32(nz)+0.5)*vs.cellSize
				wy := vs.originY + (float32(ny)+0.5)*vs.cellH

				stepDX := wx - px
				stepDZ := wz - pz
				stepLen := float32(math.Sqrt(float64(stepDX*stepDX + stepDZ*stepDZ)))
				if stepLen < 0.01 {
					continue
				}

				// Dot product with desired direction
				score := (stepDX*dirX + stepDZ*dirZ) / stepLen

				// Penalize large Y changes slightly (prefer flat, use stairs when needed)
				yDiff := float32(math.Abs(float64(wy - py)))
				score -= yDiff * 0.1

				if score > bestScore {
					bestScore = score
					bestX = wx
					bestY = wy
					bestZ = wz
					found = true
				}
			}
		}
	}

	return bestX, bestY, bestZ, found
}
