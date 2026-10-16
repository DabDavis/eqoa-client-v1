package game

import (
	"bytes"
	"encoding/binary"
	"math"
	"net"
	"testing"
	"time"

	"github.com/eqoa/client/internal/network"
)

// The 0x40 against what real PS2s sent: every captured buffer is decoded with
// func_00BC2F30's layout and boxes (each value at the middle of its bucket)
// and encoded again by SelfState.Encode. A bucket's middle comes back to the
// same bucket only under TRUNCATION, so this pins the rounding rule as well
// as the layout, the boxes and the byte order.

// dequant is the middle of bucket q of WriteQuantized's n-byte scale.
func dequant(b []byte, n int, lo, hi float32) float32 {
	var q uint32
	for i := 0; i < n; i++ {
		q = q<<8 | uint32(b[i])
	}
	scale := float64(uint32(1) << (8 * uint(n)))
	return float32(float64(lo) + (float64(q)+0.5)*(float64(hi)-float64(lo))/scale)
}

// decodeSelfState reads a 41-byte 0x40 back into the state that wrote it,
// each value at the middle of its bucket. The floor comes from byte 29-30's
// height above it.
func decodeSelfState(d []byte) SelfState {
	var s SelfState
	s.World = d[0]
	o := 1
	for i := 0; i < 3; i++ {
		s.Position[i] = dequant(d[o:], 3, selfPosLo[i], selfPosHi[i])
		o += 3
	}
	for i := 0; i < 3; i++ {
		s.Velocity[i] = dequant(d[o:], 2, selfVelLo[i], selfVelHi[i])
		o += 2
	}
	for i := 0; i < 3; i++ {
		s.Force[i] = dequant(d[o:], 2, selfForceLo[i], selfForceHi[i])
		o += 2
	}
	for i := 0; i < 2; i++ {
		s.Angles[i] = dequant(d[o:], 1, selfAngLo[i], selfAngHi[i])
		o++
	}
	for i := 0; i < 2; i++ {
		s.AngularVel[i] = dequant(d[o:], 1, selfAngVelLo[i], selfAngVelHi[i])
		o++
	}
	for i := 0; i < 2; i++ {
		s.Torque[i] = dequant(d[o:], 1, selfTorqueLo[i], selfTorqueHi[i])
		o++
	}
	s.TargetHeading = dequant(d[o:], 1, -math.Pi, math.Pi)
	o++
	dy := dequant(d[o:], 2, -2000, 2000)
	o += 2
	s.Floor = s.Position[1] - dy
	s.Flags = binary.LittleEndian.Uint32(d[o:])
	o += 4
	s.LocoState = d[o]
	s.AutoAttack = d[o+1]
	s.Target = binary.LittleEndian.Uint32(d[o+2:])
	return s
}

func TestSelfStateReencodesEveryCapturedBuffer(t *testing.T) {
	n, nudged := 0, 0
	for _, m := range egressAll(t) {
		if m.Type != 0x40 {
			continue
		}
		if len(m.Data) != 41 {
			t.Fatalf("%s: a 0x40 of %d bytes", m.Capture, len(m.Data))
		}
		n++
		s := decodeSelfState(m.Data)
		// A position's bucket is narrower than the float32 grid of v - lo
		// (0.0021 against 0.0020 for x and z past 12,384; 0.00012 against
		// 0.00012 for y past 24): the middle of the bucket can chop into
		// the next one. The PS2's own position was SOME float32 that landed
		// in the captured bucket, so look for one near the middle -- and
		// fail if there is none, which is what a wrong rounding rule gives.
		for i := 0; i < 3; i++ {
			want := m.Data[1+3*i : 4+3*i]
			if !bytes.Equal(writeQuantized(nil, 3, s.Position[i], selfPosLo[i], selfPosHi[i]), want) {
				v, ok := nudgeIntoBucket(s.Position[i], want, selfPosLo[i], selfPosHi[i])
				if !ok {
					t.Fatalf("%s t=%.3f: no float32 position reaches % X on axis %d", m.Capture, m.Time, want, i)
				}
				if i == 1 {
					s.Floor += v - s.Position[1] // keep the height above the floor
				}
				s.Position[i] = v
				nudged++
			}
		}
		if got := s.Encode(); !bytes.Equal(got, m.Data) {
			t.Fatalf("%s t=%.3f\n got % X\nwant % X", m.Capture, m.Time, got, m.Data)
		}
	}
	if n < 3000 {
		t.Fatalf("only %d captured 0x40 buffers decoded", n)
	}
	t.Logf("%d captured 0x40 buffers re-encoded byte for byte (%d position axes nudged off the bucket middle)", n, nudged)
}

// nudgeIntoBucket walks float32 steps out from v until WriteQuantized lands
// on want.
func nudgeIntoBucket(v float32, want []byte, lo, hi float32) (float32, bool) {
	up, down := v, v
	for k := 0; k < 64; k++ {
		up = math.Nextafter32(up, float32(math.Inf(1)))
		down = math.Nextafter32(down, float32(math.Inf(-1)))
		for _, c := range []float32{up, down} {
			if bytes.Equal(writeQuantized(nil, 3, c, lo, hi), want) {
				return c, true
			}
		}
	}
	return v, false
}

// TestSelfStateFixtures pins the encoder on hand-built states.
func TestSelfStateFixtures(t *testing.T) {
	// A walker standing still where SetLocation put it, facing 1.031 rad:
	// ps2_sandstorm.pcap's first 0x40 (onFloor | TurnTrack, no landscape yet).
	s := SelfState{
		Position:      [3]float32{16789.42, 187.81, 15330.53},
		Angles:        [3]float32{1.04, 0, 0},
		TargetHeading: 1.04,
		Floor:         187.81,
		Flags:         WalkOnFloor | WalkTurnTrack,
		Target:        0xFFFFFFFF,
	}
	want := []byte{0x00, 0x93, 0xD5, 0xFB, 0x98, 0x0A, 0x38, 0x89, 0x76, 0x26,
		0x80, 0x00, 0xF7, 0x39, 0x80, 0x00, // velocity 0: vy 0 is 0xF739 over [-84.5, 3]
		0x80, 0x00, 0x80, 0x00, 0x80, 0x00, // force 0
		0xAA, 0x80, 0x80, 0x80, 0x80, 0x80, // heading, pitch, rates, torques
		0xAA, 0x80, 0x00, // target heading, height above the floor 0
		0x01, 0x01, 0x00, 0x00, // flags 0x101
		0x00, 0x00, 0xFF, 0xFF, 0xFF, 0xFF}
	got := s.Encode()
	// The position bytes are the capture's, which our mid-bucket value lands
	// in; check the rest exactly and the position by decode.
	if len(got) != 41 {
		t.Fatalf("len %d", len(got))
	}
	if !bytes.Equal(got[10:], want[10:]) || got[0] != 0 {
		t.Errorf("\n got % X\nwant % X", got, want)
	}

	// Saturation: everything past its box clamps to the last bucket, or the
	// first, and a heading of exactly pi wraps to -pi (bucket 0).
	s = SelfState{
		Position:   [3]float32{1e9, -1e9, 32000},
		Velocity:   [3]float32{100, -100, -15.3},
		Angles:     [3]float32{math.Pi, 9, 0},
		AngularVel: [3]float32{-6.4, 0.8, 0},
		Torque:     [3]float32{40, -40, 0},
		Floor:      -5000,
	}
	got = s.Encode()
	check := func(name string, off int, want ...byte) {
		t.Helper()
		if !bytes.Equal(got[off:off+len(want)], want) {
			t.Errorf("%s: % X, want % X", name, got[off:off+len(want)], want)
		}
	}
	check("pos x clamps high", 1, 0xFF, 0xFF, 0xFF)
	check("pos y clamps low", 4, 0x00, 0x00, 0x00)
	check("pos z at max", 7, 0xFF, 0xFF, 0xFF)
	check("vel x clamps high", 10, 0xFF, 0xFF)
	check("vel y clamps low", 12, 0x00, 0x00)
	check("vel z at min", 14, 0x00, 0x00)
	check("heading pi wraps to -pi", 22, 0x00)
	check("pitch wraps (9 - 2pi = 2.72) then clamps", 23, 0xFF)
	check("heading rate at min, pitch rate at max", 24, 0x00, 0xFF)
	check("torques clamp", 26, 0xFF, 0x00)
	check("height above floor clamps (y far below it)", 29, 0x00, 0x00)
}

func TestSelfStateWrapPi(t *testing.T) {
	for _, c := range []struct{ in, want float32 }{
		{0, 0},
		{math.Pi, -math.Pi},
		{-math.Pi, -math.Pi},
		{3 * math.Pi / 2, -math.Pi / 2},
		{-3 * math.Pi / 2, math.Pi / 2},
	} {
		if got := wrapPi(c.in); math.Abs(float64(got-c.want)) > 1e-6 {
			t.Errorf("wrapPi(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestSelfStateDue(t *testing.T) {
	t0 := time.Unix(1000, 0)
	if !selfStateDue(time.Time{}, t0, 100) {
		t.Error("the first write after a connect is due at once")
	}
	// 100 VITimer ticks at 983.04 a second are 101.7 ms.
	if selfStateDue(t0, t0.Add(101*time.Millisecond), 100) {
		t.Error("due before the interval")
	}
	if !selfStateDue(t0, t0.Add(102*time.Millisecond), 100) {
		t.Error("not due at the interval")
	}
	if !selfStateDue(t0, t0, 0) {
		t.Error("an interval of 0 writes every frame")
	}
}

func TestDriveAccelMatchesTheCapturedRunForce(t *testing.T) {
	// ps2_sandstorm.pcap: a run at 8.83333 shows |force| 36.81.
	if a := DriveAccel(8.83333); math.Abs(float64(a)-36.81) > 0.05 {
		t.Errorf("run accel %.3f, want 36.81", a)
	}
	if a := DriveAccel(3); math.Abs(float64(a)-12.50) > 0.05 {
		t.Errorf("walk accel %.3f, want 12.50", a)
	}
}

// TestEngineSelfStateMapping pins what the Engine hands the writer.
func TestEngineSelfStateMapping(t *testing.T) {
	e := NewEngine("127.0.0.1", 9, 9, 9, "test", "")
	p := &e.State.Player
	p.World = 3
	p.X, p.Y, p.Z = 100, 50, 200
	p.Facing = 0.5
	e.VelX, e.VelY, e.VelZ = 1, -2, 3
	e.ForceX, e.ForceZ = 4, 5
	e.AnimByte = 3
	e.CombatEngaged = true
	e.TargetObjectID = 0x1234

	buf := e.buildPositionBuffer()
	got := decodeSelfState(buf)
	near := func(name string, a, b float32) {
		t.Helper()
		if math.Abs(float64(a-b)) > 0.03 {
			t.Errorf("%s = %v, want %v", name, a, b)
		}
	}
	if got.World != 3 || got.LocoState != 3 || got.AutoAttack != 1 || got.Target != 0x1234 {
		t.Errorf("world %d loco %d auto %d target %X",
			got.World, got.LocoState, got.AutoAttack, got.Target)
	}
	near("x", got.Position[0], 100)
	near("y", got.Position[1], 50)
	near("vel y", got.Velocity[1], -2)
	near("force z", got.Force[2], 5)
	near("heading", got.Angles[0], 0.5)
	near("target heading", got.TargetHeading, 0.5)
	if buf[29] != 0x80 || buf[30] != 0x00 {
		t.Errorf("height above the floor (none set: standing) % X, want 80 00", buf[29:31])
	}
	if got.Flags != WalkOnFloor|WalkTurnTrack|WalkLandscape {
		t.Errorf("a walker that never set its state sends %X, want 0x501", got.Flags)
	}

	// A walker in the air, first person, 7 above the floor it left.
	e.SetWalkState(0, 43)
	got = decodeSelfState(e.buildPositionBuffer())
	if got.Flags != 0 {
		t.Errorf("flags %X, want 0", got.Flags)
	}
	if dy := got.Position[1] - got.Floor; math.Abs(float64(dy-7)) > 0.07 {
		t.Errorf("height above the floor %v, want 7", dy)
	}

	// World entry is SetLocation's state: still, on a floor, the 0x100 bit kept.
	e.SetWalkState(WalkOnFloor|WalkTurnTrack|WalkLandscape, 50)
	s := e.selfState(true)
	b := s.Encode()
	got = decodeSelfState(b)
	if got.Flags != WalkOnFloor|WalkTurnTrack || got.LocoState != 0 {
		t.Errorf("entry flags %X loco %d, want 0x101 and 0", got.Flags, got.LocoState)
	}
	if !bytes.Equal(b[10:22], []byte{0x80, 0x00, 0xF7, 0x39, 0x80, 0x00, 0x80, 0x00, 0x80, 0x00, 0x80, 0x00}) {
		t.Errorf("entry velocity/force not zero: % X", b[10:22])
	}

	e.TargetObjectID = 0
	if got := decodeSelfState(e.buildPositionBuffer()); got.Target != 0xFFFFFFFF {
		t.Errorf("no target sends %X, want FFFFFFFF", got.Target)
	}
}

// TestSelfStateOnTheWire sends the Engine's 0x40 through a real Connection
// to a local socket and reads it back as a server would.
func TestSelfStateOnTheWire(t *testing.T) {
	l, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Skip(err)
	}
	defer l.Close()
	c, err := network.NewConnection("127.0.0.1", l.LocalAddr().(*net.UDPAddr).Port, 0x1111)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	server, _ := network.NewConnection("127.0.0.1", 9, 0xFFFE)
	defer server.Close()

	e := NewEngine("127.0.0.1", 9, 9, 9, "test", "")
	e.State.Conn = c
	e.State.Phase = PhaseInWorld
	p := &e.State.Player
	p.X, p.Y, p.Z = 1000, 10, 2000

	recv := func() [][]byte {
		var got [][]byte
		buf := make([]byte, 2048)
		for {
			l.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
			n, err := l.Read(buf)
			if err != nil {
				return got
			}
			pkt, err := network.ParsePacket(append([]byte(nil), buf[:n]...), nil)
			if err != nil || pkt.Body == nil {
				continue
			}
			for _, m := range server.ProcessIncoming(pkt) {
				if m.Type == 0x40 {
					got = append(got, append([]byte(nil), m.Data()...))
				}
			}
		}
	}

	e.sendWorldEntryTrigger()
	got := recv()
	if len(got) != 1 || binary.LittleEndian.Uint32(got[0][31:]) != WalkOnFloor|WalkTurnTrack {
		t.Fatalf("world entry: %d 0x40s, want one with flags 0x101", len(got))
	}
	e.sendPositionUpdate() // standing: the landscape bit comes in, as in every capture
	if got = recv(); len(got) != 1 || binary.LittleEndian.Uint32(got[0][31:]) != 0x501 {
		t.Fatalf("second: %d 0x40s, want one with flags 0x501", len(got))
	}
	e.sendPositionUpdate() // nothing changed: nothing on the wire
	if got = recv(); len(got) != 0 {
		t.Fatalf("an unchanged state went out again (%d)", len(got))
	}
	p.X += 1
	e.sendPositionUpdate()
	if got = recv(); len(got) != 1 {
		t.Fatalf("a moved state: %d 0x40s, want 1", len(got))
	}
	if d := decodeSelfState(got[0]); math.Abs(float64(d.Position[0]-1001)) > 0.01 {
		t.Fatalf("x %v, want 1001", d.Position[0])
	}
}
