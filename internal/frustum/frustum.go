package frustum

import (
	"math"

	rl "github.com/gen2brain/raylib-go/raylib"
)

// Plane represents a half-space: Ax + By + Cz + D >= 0 is inside.
type Plane struct {
	A, B, C, D float32
}

// Frustum holds the 6 clipping planes of a view frustum.
// Order: left, right, bottom, top, near, far.
type Frustum struct {
	Planes [6]Plane
}

// CullStats tracks how many objects were tested and drawn.
type CullStats struct {
	TerrainTotal int
	TerrainDrawn int
	EntityTotal  int
	EntityDrawn  int
}

// FromCamera extracts frustum planes from a Camera3D using the
// Gribb-Hartmann method (extract planes directly from VP matrix rows).
func FromCamera(cam rl.Camera3D, aspect float32) Frustum {
	// Build view matrix (lookAt).
	view := lookAt(cam.Position, cam.Target, cam.Up)

	// Build perspective projection matrix.
	fovyRad := float64(cam.Fovy) * math.Pi / 180.0
	near := float32(1.0)
	far := float32(5000.0)
	proj := perspective(float32(fovyRad), aspect, near, far)

	// Combined view-projection matrix.
	vp := matMul(proj, view)

	var f Frustum

	// Left:   row3 + row0
	f.Planes[0] = Plane{
		A: vp[3] + vp[0],
		B: vp[7] + vp[4],
		C: vp[11] + vp[8],
		D: vp[15] + vp[12],
	}
	// Right:  row3 - row0
	f.Planes[1] = Plane{
		A: vp[3] - vp[0],
		B: vp[7] - vp[4],
		C: vp[11] - vp[8],
		D: vp[15] - vp[12],
	}
	// Bottom: row3 + row1
	f.Planes[2] = Plane{
		A: vp[3] + vp[1],
		B: vp[7] + vp[5],
		C: vp[11] + vp[9],
		D: vp[15] + vp[13],
	}
	// Top:    row3 - row1
	f.Planes[3] = Plane{
		A: vp[3] - vp[1],
		B: vp[7] - vp[5],
		C: vp[11] - vp[9],
		D: vp[15] - vp[13],
	}
	// Near:   row3 + row2
	f.Planes[4] = Plane{
		A: vp[3] + vp[2],
		B: vp[7] + vp[6],
		C: vp[11] + vp[10],
		D: vp[15] + vp[14],
	}
	// Far:    row3 - row2
	f.Planes[5] = Plane{
		A: vp[3] - vp[2],
		B: vp[7] - vp[6],
		C: vp[11] - vp[10],
		D: vp[15] - vp[14],
	}

	// Normalize each plane so distance tests are correct.
	for i := range f.Planes {
		normalizePlane(&f.Planes[i])
	}

	return f
}

// TestAABB returns true if the axis-aligned bounding box is at least
// partially inside the frustum (conservative — false positives possible,
// no false negatives).
func (f *Frustum) TestAABB(minX, minY, minZ, maxX, maxY, maxZ float32) bool {
	for _, p := range f.Planes {
		// Find the corner most aligned with the plane normal (p-vertex).
		px, py, pz := minX, minY, minZ
		if p.A >= 0 {
			px = maxX
		}
		if p.B >= 0 {
			py = maxY
		}
		if p.C >= 0 {
			pz = maxZ
		}
		// If the p-vertex is outside this plane, the whole AABB is outside.
		if p.A*px+p.B*py+p.C*pz+p.D < 0 {
			return false
		}
	}
	return true
}

// TestSphere returns true if the sphere intersects the frustum.
func (f *Frustum) TestSphere(cx, cy, cz, radius float32) bool {
	for _, p := range f.Planes {
		dist := p.A*cx + p.B*cy + p.C*cz + p.D
		if dist < -radius {
			return false
		}
	}
	return true
}

func normalizePlane(p *Plane) {
	length := float32(math.Sqrt(float64(p.A*p.A + p.B*p.B + p.C*p.C)))
	if length > 0 {
		invLen := 1.0 / length
		p.A *= invLen
		p.B *= invLen
		p.C *= invLen
		p.D *= invLen
	}
}

// lookAt builds a view matrix (column-major [16]float32).
func lookAt(eye, target, up rl.Vector3) [16]float32 {
	// Forward = normalize(eye - target)
	fx := eye.X - target.X
	fy := eye.Y - target.Y
	fz := eye.Z - target.Z
	fLen := float32(math.Sqrt(float64(fx*fx + fy*fy + fz*fz)))
	if fLen > 0 {
		fx /= fLen
		fy /= fLen
		fz /= fLen
	}

	// Right = normalize(cross(up, forward))
	rx := up.Y*fz - up.Z*fy
	ry := up.Z*fx - up.X*fz
	rz := up.X*fy - up.Y*fx
	rLen := float32(math.Sqrt(float64(rx*rx + ry*ry + rz*rz)))
	if rLen > 0 {
		rx /= rLen
		ry /= rLen
		rz /= rLen
	}

	// Up = cross(forward, right)
	ux := fy*rz - fz*ry
	uy := fz*rx - fx*rz
	uz := fx*ry - fy*rx

	// Column-major layout matching raylib's Matrix:
	// [0]=M0  [4]=M4  [8]=M8   [12]=M12
	// [1]=M1  [5]=M5  [9]=M9   [13]=M13
	// [2]=M2  [6]=M6  [10]=M10 [14]=M14
	// [3]=M3  [7]=M7  [11]=M11 [15]=M15
	return [16]float32{
		rx, ux, fx, 0,
		ry, uy, fy, 0,
		rz, uz, fz, 0,
		-(rx*eye.X + ry*eye.Y + rz*eye.Z),
		-(ux*eye.X + uy*eye.Y + uz*eye.Z),
		-(fx*eye.X + fy*eye.Y + fz*eye.Z),
		1,
	}
}

// perspective builds a perspective projection matrix (column-major [16]float32).
func perspective(fovyRad, aspect, near, far float32) [16]float32 {
	tanHalf := float32(math.Tan(float64(fovyRad) / 2.0))
	var m [16]float32
	m[0] = 1.0 / (aspect * tanHalf)
	m[5] = 1.0 / tanHalf
	m[10] = -(far + near) / (far - near)
	m[11] = -1.0
	m[14] = -(2.0 * far * near) / (far - near)
	return m
}

// matMul multiplies two column-major 4x4 matrices: result = a * b.
func matMul(a, b [16]float32) [16]float32 {
	var r [16]float32
	for col := 0; col < 4; col++ {
		for row := 0; row < 4; row++ {
			var sum float32
			for k := 0; k < 4; k++ {
				sum += a[k*4+row] * b[col*4+k]
			}
			r[col*4+row] = sum
		}
	}
	return r
}
