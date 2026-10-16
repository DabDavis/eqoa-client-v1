package game

import (
	"math"
	"time"
)

// The player's own state channel, 0x40: CLIENT's func_00BC2F30, which
// VIClient::Process calls once a frame (0x00BC7F68). It writes the walk
// camera's COMMITTED physics -- m_walkCam.m_committed, the VIWalkPhysics at
// VIClient+0x6F00 (vi.h) -- plus the world, the auto-attack flag and the
// target, and hands the 41 bytes to connection_send on channel 0x40:
//
//	[0]      m_worldId                     (+0x6630, low byte)
//	[1-9]    m_position     3 bytes/axis   box (-4000,-1000,-4000)..(32000,1000,32000)
//	[10-15]  m_velocity     2 bytes/axis   box (-15.3,-84.5,-15.3)..(15.3,3,15.3)
//	[16-21]  m_force        2 bytes/axis   box (-62.6,-12.51,-62.52)..(62.6,12.51,62.52)
//	[22-23]  m_angles       1 byte/axis    heading +-pi, pitch +-1.6 (wrapped to [-pi,pi) first)
//	[24-25]  m_angularVel   1 byte/axis    +-6.4, +-0.8
//	[26-27]  m_torque       1 byte/axis    +-31.2, +-3.9
//	[28]     m_targetHeading               wrapped, +-pi
//	[29-30]  m_position.y - m_floor        +-2000
//	[31-34]  m_flags        u32 LE         VIWalkFlags (vi.h)
//	[35]     m_locoState    one byte       VIClient.cpp:2277 asserts 0..255
//	[36]     m_autoAttack                  (+0x71E4)
//	[37-40]  m_target       u32 LE         (+0xC140), -1 none
//
// A box axis with no extent writes NOTHING (WriteQuantized returns when hi ==
// lo), which is why the three angle-like vectors are two bytes and not three:
// roll never goes out. The boxes are six static VIBBoxes built by the
// initialiser func_00CC6558 (read by emulating it: the image holds zeros).
// The rest of the function keeps a running Union of every value it writes,
// one static box per vector, and compares it with memcmp to no effect: a
// debug statistic with its printf compiled out, nothing on the wire.

// The quantisation boxes, data_00CD7A58..data_00CD7AD0 as func_00CC6558
// builds them. Each is the nearest float32 of the literal, as Go's is.
var (
	selfPosLo, selfPosHi       = [3]float32{-4000, -1000, -4000}, [3]float32{32000, 1000, 32000}
	selfVelLo, selfVelHi       = [3]float32{-15.3, -84.5, -15.3}, [3]float32{15.3, 3, 15.3}
	selfForceLo, selfForceHi   = [3]float32{-62.6, -12.51, -62.52}, [3]float32{62.6, 12.51, 62.52}
	selfAngLo, selfAngHi       = [3]float32{-math.Pi, -1.6, 0}, [3]float32{math.Pi, 1.6, 0}
	selfAngVelLo, selfAngVelHi = [3]float32{-6.4, -0.8, 0}, [3]float32{6.4, 0.8, 0}
	selfTorqueLo, selfTorqueHi = [3]float32{-31.2, -3.9, 0}, [3]float32{31.2, 3.9, 0}
)

// VIWalkFlags, VIWalkPhysics::m_flags (decomp/src/vi.h).
const (
	WalkOnFloor   uint32 = 0x0001
	WalkHitWall   uint32 = 0x0002 // VIWalkControl::Move: the sweep hit something this tick
	WalkTurnTrack uint32 = 0x0100 // camera mode 3 tracks the target heading (VIWalkCamera::SetCameraMode)
	WalkNoTurn    uint32 = 0x0200
	WalkLandscape uint32 = 0x0400 // the floor found is landscape, not an actor
)

// SelfState is what func_00BC2F30 reads: the committed VIWalkPhysics and the
// three VIClient fields. Angles are radians, as the walker keeps them.
type SelfState struct {
	World         byte
	Position      [3]float32
	Velocity      [3]float32
	Force         [3]float32
	Angles        [3]float32 // heading, pitch, roll (roll is never written)
	AngularVel    [3]float32
	Torque        [3]float32
	TargetHeading float32
	Floor         float32
	Flags         uint32
	LocoState     byte
	AutoAttack    byte
	Target        uint32
}

// Encode is func_00BC2F30's stream, byte for byte.
func (s *SelfState) Encode() []byte {
	b := make([]byte, 0, 41)
	b = append(b, s.World)
	b = writeQuantizedVec(b, s.Position, 3, selfPosLo, selfPosHi)
	b = writeQuantizedVec(b, s.Velocity, 2, selfVelLo, selfVelHi)
	b = writeQuantizedVec(b, s.Force, 2, selfForceLo, selfForceHi)
	ang := s.Angles
	for i := range ang {
		ang[i] = wrapPi(ang[i]) // func_00CC69C8 on a copy
	}
	b = writeQuantizedVec(b, ang, 1, selfAngLo, selfAngHi)
	b = writeQuantizedVec(b, s.AngularVel, 1, selfAngVelLo, selfAngVelHi)
	b = writeQuantizedVec(b, s.Torque, 1, selfTorqueLo, selfTorqueHi)
	b = writeQuantized(b, 1, wrapPi(s.TargetHeading), -math.Pi, math.Pi)
	b = writeQuantized(b, 2, eeSub(s.Position[1], s.Floor), -2000, 2000)
	b = append(b, byte(s.Flags), byte(s.Flags>>8), byte(s.Flags>>16), byte(s.Flags>>24))
	b = append(b, s.LocoState, s.AutoAttack)
	b = append(b, byte(s.Target), byte(s.Target>>8), byte(s.Target>>16), byte(s.Target>>24))
	return b
}

// writeQuantizedVec is WriteQuantized(VIMemStream &, const VIVect3 &, int,
// const VIBBox &) (0x00BC2EB0): each axis over the box's extent.
func writeQuantizedVec(b []byte, v [3]float32, n int, lo, hi [3]float32) []byte {
	for i := 0; i < 3; i++ {
		b = writeQuantized(b, n, v[i], lo[i], hi[i])
	}
	return b
}

// writeQuantized is WriteQuantized(VIMemStream &, int, float, float, float)
// (0x00BC2D28) in the EE's arithmetic: v - lo clamped to [0, hi - lo], times
// 2^(8n), divided by the range, truncated, at most 2^(8n) - 1, high byte
// first. A range of zero writes nothing.
func writeQuantized(b []byte, n int, v, lo, hi float32) []byte {
	scale := uint32(1) << (8 * uint(n))
	rng := eeSub(hi, lo)
	if eeEq(rng, 0) {
		return b
	}
	t := eeSub(v, lo)
	// VIMIN(range, VIMAX(0.0f, t)), as the guest branches it.
	var c bool
	if eeLt(t, 0) {
		c = eeLt(rng, 0)
	} else {
		c = eeLt(rng, t)
	}
	x := rng
	if !c {
		if eeLt(t, 0) {
			x = 0
		} else {
			x = t
		}
	}
	f := eeDiv(eeMul(x, float32(scale)), rng)
	q := uint32(0)
	if f > 0 { // __fixunssfsi; x is never negative
		q = uint32(f)
	}
	if q > scale-1 {
		q = scale - 1
	}
	for i := 0; i < n; i++ {
		b = append(b, byte(q>>(8*uint(n-i-1))))
	}
	return b
}

// wrapPi is func_00CC6920: add 2pi while below -pi, subtract it while at or
// above pi, so the result is in [-pi, pi).
func wrapPi(a float32) float32 {
	const pi, twoPi = float32(math.Pi), float32(2 * math.Pi)
	for eeLt(a, -pi) {
		a = eeAdd(a, twoPi)
	}
	for eeLe(pi, a) {
		a = eeSub(a, twoPi)
	}
	return a
}

// selfStateDue is Process's test around the writer (0x00BC7F40): the first
// call after VIClient::Connect (which stores -1 in +0x7570), and then once
// the frame clock is interval ms past the last write. The interval is
// +0x7578: 100 from the constructor, whatever a 0x07B2 carries after.
func selfStateDue(last, now time.Time, interval uint32) bool {
	if last.IsZero() {
		return true
	}
	return now.Sub(last) >= time.Duration(interval)*time.Millisecond
}

// DriveAccel is the force VIWalkControl::Walk/Run adds for a walker heading
// for speed (units/s): InitIntermediate's accel = (s - s*d)/(d*dt) with
// d = MoveDamping^dt, MoveDamping 0.02 and the 32 ms tick (InitParms). It is
// the thrust whose damped steady state IS that speed, so a walker cruising
// at the run speed shows a force of 36.8 (8.83333 a second), as the PS2
// captures do.
func DriveAccel(speed float32) float32 {
	const dt = float32(0.032)
	d := float32(math.Pow(0.02, float64(dt)))
	return (speed - speed*d) / (d * dt)
}

// The EE FPU, as go-eqoa-vi/vimath has it (and measured there): add, sub
// and mul round toward zero, a denormal operand or result is a signed zero,
// and an exponent-255 operand is +-MaxFloat32. A comparison conditions its
// operands the same way. The divide is the exception; see eeDiv.

func eeChop(x float64) float32 {
	f := float32(x)
	if math.Abs(float64(f)) > math.Abs(x) {
		if bits := math.Float32bits(f); bits&0x7FFFFFFF != 0 {
			f = math.Float32frombits(bits - 1)
		}
	}
	return eeFTZ(f)
}

func eeFTZ(f float32) float32 {
	if bits := math.Float32bits(f); bits&0x7F800000 == 0 {
		return math.Float32frombits(bits & 0x80000000)
	}
	return f
}

func eeOperand(f float32) float32 {
	bits := math.Float32bits(f)
	if bits&0x7F800000 == 0x7F800000 {
		if bits>>31 != 0 {
			return -math.MaxFloat32
		}
		return math.MaxFloat32
	}
	return eeFTZ(f)
}

// eeSum rounds the exact x+y toward zero (twoSum carries the error a float64
// sum of far-apart operands would lose).
func eeSum(x, y float64) float32 {
	s := x + y
	bb := s - x
	e := (x - (s - bb)) + (y - bb)
	r := eeChop(s)
	if e == 0 || r == 0 {
		return r
	}
	if rem := (s - float64(r)) + e; (r > 0 && rem < 0) || (r < 0 && rem > 0) {
		return eeFTZ(math.Float32frombits(math.Float32bits(r) - 1))
	}
	return r
}

func eeAdd(a, b float32) float32 {
	return eeSum(float64(eeOperand(a)), float64(eeOperand(b)))
}

func eeSub(a, b float32) float32 {
	return eeSum(float64(eeOperand(a)), -float64(eeOperand(b)))
}

func eeMul(a, b float32) float32 {
	return eeChop(float64(eeOperand(a)) * float64(eeOperand(b)))
}

// eeDiv is div.s as the captures show it: rounded to NEAREST, not chopped.
// Under a chopping divide 23 of the 9,945 position axes in the PS2 captures
// cannot be produced by any float32 position (a 24-bit bucket over 2,000 is
// narrower than the float32 grid of y + 1000, and chopping skips some);
// rounding to nearest reaches all of them (TestSelfStateReencodes...). The
// captures were taken on PCSX2, whose divide rounding is set apart from the
// FPU's (default Nearest); go-eqoa-vi/vimath.Div chops.
func eeDiv(a, b float32) float32 {
	a, b = eeOperand(a), eeOperand(b)
	if b == 0 {
		if math.Signbit(float64(a)) != math.Signbit(float64(b)) {
			return -math.MaxFloat32
		}
		return math.MaxFloat32
	}
	return eeFTZ(float32(float64(a) / float64(b)))
}

func eeLt(a, b float32) bool { return eeOperand(a) < eeOperand(b) }
func eeLe(a, b float32) bool { return eeOperand(a) <= eeOperand(b) }
func eeEq(a, b float32) bool { return eeOperand(a) == eeOperand(b) }
