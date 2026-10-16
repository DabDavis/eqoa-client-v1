package terrain

// The walk camera's two scene queries (cmd/client/walkcamera.go).
//
// VIWalkCamera::MoveCamera, FindOrbitAngle and FindBestOrbitAngle
// (viwalkcamera.cpp, every one byte-exact) sweep a VICollSphere through
// VIScene::Collide with a VIWalkCameraFilter, which rejects only the
// character's own actor and every VICSprite (sprite type 5). Characters are
// not in the collision tiles, so the sweep here is the walker's own
// collideSphere over every face (all = true): the rooms and the placed
// actors' collision buffers.

// CameraSweep sweeps a sphere of the given radius from (fx,fy,fz) to
// (tx,ty,tz). It returns the sphere's centre where it stopped (the end point
// when nothing was hit: VICollRay::GetHitPoint with hit time 1), the distance
// it travelled (GetHitDistance) and whether anything was hit.
func (zt *ZoneTerrain) CameraSweep(fx, fy, fz, tx, ty, tz, radius float32) (hx, hy, hz, dist float32, hit bool) {
	var s collSphere
	s.init(vec3{fx, fy, fz}, vec3{tx, ty, tz}, radius)
	if len(zt.CollTiles) > 0 {
		hit = zt.collideSphere(&s, true)
	}
	p := s.hitPoint()
	return p[0], p[1], p[2], s.hitTime * s.length, hit
}

// GroundNormalY is the y of VIWalkPhysics::m_landscapeNormal for a walker
// whose feet are at feetY: the face normal under the floor probe CalcFit drops
// (Fit's first sweep), 1 where it meets nothing. MoveCamera takes the
// alternate pitch only on ground steeper than y 0.9.
func (zt *ZoneTerrain) GroundNormalY(x, feetY, z float32) float32 {
	if len(zt.CollTiles) == 0 {
		return 1
	}
	centre := vec3{x, feetY + charCollY, z}
	var s collSphere
	s.init(centre, centre.sub(vec3{0, floorDrop, 0}), probeRadius)
	if !zt.collideSphere(&s, true) {
		return 1
	}
	return s.hitNormal[1]
}
