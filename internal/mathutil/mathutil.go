package mathutil

import "math"

// ClampF clamps a float32 value to [lo, hi].
func ClampF(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// FloatToUint8 converts a [0,1] float32 to a [0,255] uint8 with rounding.
func FloatToUint8(v float32) uint8 {
	n := int(math.Round(float64(v) * 255))
	if n < 0 {
		return 0
	}
	if n > 255 {
		return 255
	}
	return uint8(n)
}

// Dist3D returns the Euclidean distance between two 3D points.
func Dist3D(ax, ay, az, bx, by, bz float32) float32 {
	dx := ax - bx
	dy := ay - by
	dz := az - bz
	return float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))
}

// Dist2D returns the Euclidean distance between two 2D points.
func Dist2D(ax, ay, bx, by float32) float32 {
	dx := ax - bx
	dy := ay - by
	return float32(math.Sqrt(float64(dx*dx + dy*dy)))
}

// RectContains returns true if point (px, py) is inside the rectangle.
func RectContains(px, py, rx, ry, rw, rh int32) bool {
	return px >= rx && px <= rx+rw && py >= ry && py <= ry+rh
}
