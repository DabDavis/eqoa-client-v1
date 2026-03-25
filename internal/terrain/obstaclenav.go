package terrain

import "math"

// ObstacleWaypoint is a point to navigate to in order to avoid an obstacle.
type ObstacleWaypoint struct {
	X, Z float32
}

// pointInsideRect tests if (x,z) is inside the axis-aligned rectangle.
func pointInsideRect(x, z, minX, minZ, maxX, maxZ float32) bool {
	return x >= minX && x <= maxX && z >= minZ && z <= maxZ
}

// FindContainingObstacle returns the index of the smallest obstacle
// containing point (x,z). Returns -1 if the point is outdoors.
func FindContainingObstacle(obstacles []ActorObstacle, x, z float32) int {
	bestIdx := -1
	bestArea := float32(math.MaxFloat32)
	for i, obs := range obstacles {
		if x >= obs.MinX && x <= obs.MaxX && z >= obs.MinZ && z <= obs.MaxZ {
			area := (obs.MaxX - obs.MinX) * (obs.MaxZ - obs.MinZ)
			if area < bestArea {
				bestArea = area
				bestIdx = i
			}
		}
	}
	return bestIdx
}

// FindContainingObstacleVerified returns the index of the obstacle the player
// is truly inside (not just within the AABB). Uses CalcFit to verify there's
// no wall between the player and the obstacle center.
func FindContainingObstacleVerified(zt *ZoneTerrain, x, y, z float32) int {
	if zt == nil {
		return -1
	}
	idx := FindContainingObstacle(zt.Obstacles, x, z)
	if idx < 0 {
		return -1
	}
	// Verify: probe from player to obstacle center. If wall blocks it,
	// player is outside the building but inside the AABB.
	obs := &zt.Obstacles[idx]
	fit := zt.CalcFit(x, y, z, obs.CX, obs.CZ)
	if fit.Hit {
		return -1 // wall between player and center = outside the building
	}
	return idx
}

// FindObstaclePathWithProbe checks if the line from (sx,sz) to (gx,gz) crosses
// any actor obstacle footprints. Uses CalcFit probing at runtime to find doorways
// instead of precomputed data. Returns waypoints to navigate around/through obstacles.
func FindObstaclePathWithProbe(zt *ZoneTerrain, sx, sz, gx, gz, refY float32) []ObstacleWaypoint {
	if zt == nil || len(zt.Obstacles) == 0 {
		return nil
	}

	const pad = float32(1.5)

	// Check if player or target is truly inside an obstacle (verified with CalcFit)
	playerObs := FindContainingObstacleVerified(zt, sx, refY, sz)
	goalObs := FindContainingObstacleVerified(zt, gx, refY, gz)

	// Player inside an obstacle — probe for door to exit
	if playerObs >= 0 && playerObs != goalObs {
		obs := &zt.Obstacles[playerObs]
		doors := ProbeDoorways(zt, obs, refY)
		if len(doors) > 0 {
			// Pick door closest to goal
			best := nearestWaypoint(doors, gx, gz)
			return []ObstacleWaypoint{best, {X: gx, Z: gz}}
		}
	}

	// Target inside an obstacle — probe for door to enter
	if goalObs >= 0 && playerObs != goalObs {
		obs := &zt.Obstacles[goalObs]
		doors := ProbeDoorways(zt, obs, refY)
		if len(doors) > 0 {
			best := nearestWaypoint(doors, sx, sz)
			return []ObstacleWaypoint{best, {X: gx, Z: gz}}
		}
	}

	// Both outside — check if direct path crosses an obstacle
	var bestObs *ActorObstacle
	bestT := float32(math.MaxFloat32)

	for i := range zt.Obstacles {
		obs := &zt.Obstacles[i]
		t := lineSegRectIntersect(sx, sz, gx, gz, obs.MinX-pad, obs.MinZ-pad, obs.MaxX+pad, obs.MaxZ+pad)
		if t >= 0 && t < bestT {
			bestT = t
			bestObs = obs
		}
	}

	if bestObs == nil {
		return nil // direct path is clear
	}

	// Path crosses an obstacle — probe for door
	doors := ProbeDoorways(zt, bestObs, refY)
	if len(doors) > 0 {
		// Pick door closest to the intersection point
		hitX := sx + (gx-sx)*bestT
		hitZ := sz + (gz-sz)*bestT
		best := nearestWaypoint(doors, hitX, hitZ)
		return []ObstacleWaypoint{best, {X: gx, Z: gz}}
	}

	// No doors found — go around corners
	corners := [4][2]float32{
		{bestObs.MinX - pad, bestObs.MinZ - pad},
		{bestObs.MaxX + pad, bestObs.MinZ - pad},
		{bestObs.MaxX + pad, bestObs.MaxZ + pad},
		{bestObs.MinX - pad, bestObs.MaxZ + pad},
	}

	bestCorner := -1
	bestScore := float32(math.MaxFloat32)
	for ci, c := range corners {
		dx1 := c[0] - sx
		dz1 := c[1] - sz
		dx2 := gx - c[0]
		dz2 := gz - c[1]
		score := float32(math.Sqrt(float64(dx1*dx1+dz1*dz1))) + float32(math.Sqrt(float64(dx2*dx2+dz2*dz2)))
		t := lineSegRectIntersect(sx, sz, c[0], c[1], bestObs.MinX-pad*0.5, bestObs.MinZ-pad*0.5, bestObs.MaxX+pad*0.5, bestObs.MaxZ+pad*0.5)
		if t >= 0 && t < 0.95 {
			continue
		}
		if score < bestScore {
			bestScore = score
			bestCorner = ci
		}
	}

	if bestCorner < 0 {
		return nil
	}
	c := corners[bestCorner]
	return []ObstacleWaypoint{{X: c[0], Z: c[1]}, {X: gx, Z: gz}}
}

func nearestWaypoint(wps []ObstacleWaypoint, x, z float32) ObstacleWaypoint {
	best := wps[0]
	bestDist := float32(math.MaxFloat32)
	for _, wp := range wps {
		dx := wp.X - x
		dz := wp.Z - z
		d := dx*dx + dz*dz
		if d < bestDist {
			bestDist = d
			best = wp
		}
	}
	return best
}

// lineSegRectIntersect tests if line segment (x0,z0)→(x1,z1) intersects
// axis-aligned rectangle. Returns parametric t (0-1), or -1 if no intersection.
func lineSegRectIntersect(x0, z0, x1, z1, minX, minZ, maxX, maxZ float32) float32 {
	dx := x1 - x0
	dz := z1 - z0
	tmin := float32(0)
	tmax := float32(1)

	if dx != 0 {
		invD := 1.0 / dx
		t1 := (minX - x0) * invD
		t2 := (maxX - x0) * invD
		if t1 > t2 { t1, t2 = t2, t1 }
		if t1 > tmin { tmin = t1 }
		if t2 < tmax { tmax = t2 }
		if tmin > tmax { return -1 }
	} else if x0 < minX || x0 > maxX {
		return -1
	}

	if dz != 0 {
		invD := 1.0 / dz
		t1 := (minZ - z0) * invD
		t2 := (maxZ - z0) * invD
		if t1 > t2 { t1, t2 = t2, t1 }
		if t1 > tmin { tmin = t1 }
		if t2 < tmax { tmax = t2 }
		if tmin > tmax { return -1 }
	} else if z0 < minZ || z0 > maxZ {
		return -1
	}

	return tmin
}
