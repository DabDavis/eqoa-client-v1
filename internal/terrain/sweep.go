package terrain

import "math"

// The swept-sphere collision of VIWalkControl::Move, in plain float32: the
// sphere (VICollSphere), its per-triangle test (VICollide::Collide with its
// edge and vertex fallbacks) and VICollRay::Skitter. The bit-exact versions,
// on the EE's float model, are go-eqoa-vi/vicoll; this follows them rule for
// rule, which is what decides how a wall feels:
//
//   - a triangle is one-sided: a sphere moving away from its face, or whose
//     centre is behind its plane, never touches it, edges included;
//   - a sphere that starts embedded hits at t = 0, pushed back out along the
//     normal, so it cannot sink further in;
//   - a hit only counts if it is strictly earlier than the one already held,
//     so colliding an unchanged sweep again reports nothing.

type vec3 [3]float32

func (a vec3) add(b vec3) vec3      { return vec3{a[0] + b[0], a[1] + b[1], a[2] + b[2]} }
func (a vec3) sub(b vec3) vec3      { return vec3{a[0] - b[0], a[1] - b[1], a[2] - b[2]} }
func (a vec3) scale(k float32) vec3 { return vec3{a[0] * k, a[1] * k, a[2] * k} }
func (a vec3) dot(b vec3) float32   { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }
func (a vec3) cross(b vec3) vec3 {
	return vec3{a[1]*b[2] - a[2]*b[1], a[2]*b[0] - a[0]*b[2], a[0]*b[1] - a[1]*b[0]}
}
func (a vec3) length() float32 { return float32(math.Sqrt(float64(a.dot(a)))) }

// normalize is VIVect3::Normalize: false, and the vector untouched, at zero length.
func (a *vec3) normalize() bool {
	l := a.length()
	if l == 0 {
		return false
	}
	*a = a.scale(1 / l)
	return true
}

// reject removes v's component along axis.
func reject(v, axis vec3) vec3 { return v.add(axis.scale(-axis.dot(v))) }

// collSphere is VICollSphere: a sweep from start to end with a radius, and the
// earliest hit found so far as a parametric time.
type collSphere struct {
	start, end, delta vec3
	dir               vec3 // unit delta
	dir2              vec3 // the direction at Init; Skitter keeps projecting onto it
	length, invLength float32
	radius, invRadius float32
	hitTime           float32
	hitNormal         vec3
}

func (s *collSphere) init(start, end vec3, radius float32) {
	s.start, s.end, s.radius = start, end, radius
	s.precalc()
	s.hitTime = 1
	s.hitNormal = vec3{}
	s.dir2 = s.dir
}

func (s *collSphere) precalc() {
	s.delta = s.end.sub(s.start)
	s.dir = s.delta
	s.length = s.dir.length()
	s.invLength = 0
	if s.length != 0 {
		s.invLength = 1 / s.length
		s.dir = s.dir.scale(s.invLength)
	}
	s.invRadius = 0
	if s.radius != 0 {
		s.invRadius = 1 / s.radius
	}
}

func (s *collSphere) hitPoint() vec3 { return s.start.add(s.delta.scale(s.hitTime)) }

// skitter is VICollRay::Skitter: re-aim the sweep from the hit point along the
// surface. v is a constraint axis the slide is kept perpendicular to — up for a
// grounded walker, so it slides horizontally; zero in the air. Returns false,
// leaving the sweep as it was, when there is no slide.
func (s *collSphere) skitter(v vec3) bool {
	if !(s.hitTime < 1) {
		return false
	}
	t1 := reject(s.hitNormal, v)
	if !t1.normalize() {
		return false
	}
	t0 := reject(s.dir2, t1)
	if !t0.normalize() {
		return false
	}
	t0 = t0.add(t1.scale(0.01))
	if !t0.normalize() {
		return false
	}
	t0 = reject(t0, v) // not renormalised: the shortening is the effect
	dist := (1 - s.hitTime) * t0.dot(s.dir2) * s.length
	if !(0 < dist) {
		return false
	}
	hp := s.hitPoint()
	s.start = hp
	s.end = hp.add(t0.scale(dist))
	s.precalc()
	s.hitTime = 1
	s.hitNormal = vec3{}
	return true
}

// collideTri is VICollide::Collide(VICollSphere&, int, VIVect3&, VIVect3&,
// VIVect3&): the face, then the three edges (each of which falls through to
// its first vertex).
func (s *collSphere) collideTri(v0, v1, v2 vec3) bool {
	n := v2.sub(v1).cross(v1.sub(v0))
	if !n.normalize() {
		return false
	}
	d := -n.dot(v0)
	if !(n.dot(s.dir) <= 0) {
		return false
	}
	distStart := s.start.dot(n) + d
	if !(0 < distStart) {
		return false
	}
	distEnd := s.end.dot(n) + d - s.radius
	if !(distEnd < 0) {
		return false
	}
	distStart -= s.radius

	if distStart != distEnd {
		t := distStart / (distStart - distEnd)
		if !(t < s.hitTime) {
			return false
		}
		var hp vec3
		if 0 < t {
			hp = s.start.add(s.delta.scale(t)).sub(n.scale(s.radius))
		} else {
			hp = s.start.sub(n.scale(s.radius - distStart))
			t = 0
		}
		if pointInTri(hp, n, v0, v1, v2) {
			s.hitTime = t
			s.hitNormal = n
			return true
		}
	}

	hit := s.collideEdge(v0, v1)
	if s.collideEdge(v1, v2) {
		hit = true
	}
	if s.collideEdge(v2, v0) {
		hit = true
	}
	return hit
}

// pointInTri is VICollide::PointInTriangle: outside if, for any edge, the
// cross of the edge with (p - the edge's END vertex) points along the normal.
func pointInTri(p, n, v0, v1, v2 vec3) bool {
	out := func(from, to vec3) bool { return 0 < to.sub(from).cross(p.sub(to)).dot(n) }
	return !out(v0, v1) && !out(v1, v2) && !out(v2, v0)
}

// collideEdge is VICollide::Collide(VICollSphere&, VIVect3&, VIVect3&): the
// sweep against the cylinder round the edge, clipped to the segment, then
// always a test against v0 — except when the centre is a radius or more from
// the slab, which returns without one.
func (s *collSphere) collideEdge(v0, v1 vec3) bool {
	e := v1.sub(v0)
	c := s.dir.cross(e)
	cLenSq := c.dot(c)
	if !(0 < cLenSq) {
		return s.collideVertex(v0)
	}
	invC := 1 / float32(math.Sqrt(float64(cLenSq)))
	n := c.scale(invC)
	dist := s.start.dot(n) - n.dot(v0)
	if !(-s.radius < dist) || !(dist < s.radius) {
		return false
	}

	hit := false
	for {
		f := -s.start.sub(v0).cross(e).dot(n) * invC
		if !(0 < f) {
			break
		}
		if !e.normalize() {
			break
		}
		chord := float32(math.Sqrt(float64(s.radius*s.radius - dist*dist)))
		f += chord / s.dir.dot(n.cross(e))
		overlapping := false
		if f < 0 {
			overlapping = true
			f = 0
		}
		t := f * s.invLength
		if !(t < s.hitTime) {
			break
		}
		cp := s.start.add(s.delta.scale(t))
		along := e.dot(cp)
		if !(0 < along-e.dot(v0)) || !(along-e.dot(v1) < 0) {
			break
		}
		hn := cp.sub(v0)
		hn = hn.sub(e.scale(hn.dot(e)))
		if overlapping {
			hn.normalize()
		} else {
			hn = hn.scale(s.invRadius)
		}
		s.hitNormal = hn
		s.hitTime = t
		hit = true
		break
	}
	if s.collideVertex(v0) {
		hit = true
	}
	return hit
}

// collideVertex is VICollide::Collide(VICollSphere&, VIVect3&): a ray against
// the point's radius-sphere, only when moving toward it.
func (s *collSphere) collideVertex(p vec3) bool {
	l := s.start.sub(p)
	b := l.dot(s.dir)
	if !(b < 0) {
		return false
	}
	disc := b*b - (l.dot(l) - s.radius*s.radius)
	if !(0 < disc) {
		return false
	}
	tn := -(b + float32(math.Sqrt(float64(disc))))
	t := tn * s.invLength
	if !(t < s.hitTime) {
		return false
	}
	if 0 < t {
		s.hitTime = t
		s.hitNormal = s.dir.scale(tn).add(s.start).sub(p).scale(s.invRadius)
		return true
	}
	s.hitTime = 0
	s.hitNormal = l
	s.hitNormal.normalize()
	return true
}
