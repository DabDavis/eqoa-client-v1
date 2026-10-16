package game

import (
	"encoding/binary"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/eqoa/client/internal/network"
	"github.com/eqoa/client/internal/protocol"
)

// These pin the world lane of the egress pass to CLIENT: the entity motion
// (ChannelEntity's error, UpdateCreatures' steps, Extrapolate's blend), the
// arrival, the battle-music byte and the fade. See entitymotion.go and
// claude-notes/go-client-egress-pass-20261016/world.md.

// putMedium writes v as VIEntity's s24 BE / 128.
func putMedium(b []byte, v float32) {
	q := int32(math.Round(float64(v * 128)))
	b[0], b[1], b[2] = byte(q>>16), byte(q>>8), byte(q)
}

// creatureAt is a creature message at (x, y, z) with heading byte h and
// velocity bytes v.
func creatureAt(id uint32, x, y, z float32, h int8, v [3]int8) []byte {
	b := entityState(1, id, entityTypeCreature, "Mover")
	putMedium(b[6:], x)
	putMedium(b[9:], y)
	putMedium(b[12:], z)
	b[15] = byte(h)
	for i := range v {
		b[40+i] = byte(v[i])
	}
	return b
}

func motionState(t *testing.T) (*GameState, *int64) {
	t.Helper()
	gs := routingState(t)
	now := int64(10_000)
	gs.PS2Clock = func() int64 { return now }
	return gs, &now
}

func nearf(a, b float32) bool { return math.Abs(float64(a-b)) < 1e-3 }

// A creature is drawn where it is at first; a later update with a new
// velocity keeps it where it was drawn and blends the difference out over
// 200 ticks, linearly; its own position steps on by the velocity meanwhile.
func TestExtrapolateBlendsAnUpdateOutOver200Ticks(t *testing.T) {
	gs, now := motionState(t)
	feed(gs, 3, creatureAt(0x40, 100, 10, 200, 0, [3]int8{}))
	e := gs.Entities[0x40]
	gs.UpdateEntityMotion()
	if !nearf(e.X, 100) || !nearf(e.Z, 200) {
		t.Fatalf("created at (%.2f, %.2f), want (100, 200)", e.X, e.Z)
	}

	// 5 units east, now moving east at 64 × 21/128 = 10.5 a tick-second.
	feed(gs, 3, creatureAt(0x40, 105, 10, 200, 0, [3]int8{64, 0, 0}))
	gs.UpdateEntityMotion()
	if !nearf(e.X, 100) {
		t.Fatalf("at the update: drawn at x=%.3f, want 100 (no snap)", e.X)
	}
	*now += 100 // half the blend: Pos stepped 1.05, half the -5 error left
	gs.UpdateEntityMotion()
	if want := float32(105+1.05) - 2.5; !nearf(e.X, want) {
		t.Fatalf("half way: x=%.3f, want %.3f", e.X, want)
	}
	*now += 100
	gs.UpdateEntityMotion()
	if want := float32(105 + 2.1); !nearf(e.X, want) {
		t.Fatalf("after 200 ticks: x=%.3f, want %.3f (the position, no error)", e.X, want)
	}
	*now += 300
	gs.UpdateEntityMotion()
	if want := float32(105 + 5.25); !nearf(e.X, want) {
		t.Fatalf("t stays 0 past the blend: x=%.3f, want %.3f", e.X, want)
	}
}

// With the velocity unchanged a creature keeps no position error: it is drawn
// at the new position at once. Its heading still blends.
func TestUnchangedVelocityKeepsNoError(t *testing.T) {
	gs, now := motionState(t)
	v := [3]int8{64, 0, 0}
	feed(gs, 3, creatureAt(0x41, 0, 0, 0, 0, v))
	*now += 1000
	gs.UpdateEntityMotion() // stepped 10.5 east
	e := gs.Entities[0x41]
	feed(gs, 3, creatureAt(0x41, 12, 0, 0, 64, v)) // a quarter turn, the same velocity
	gs.UpdateEntityMotion()
	if !nearf(e.X, 12) {
		t.Fatalf("drawn at x=%.3f, want 12 (no position error)", e.X)
	}
	if !nearf(e.Facing, 0) {
		t.Fatalf("facing %.3f at the update, want 0 (the heading blends)", e.Facing)
	}
	*now += 100
	gs.UpdateEntityMotion()
	if want := float32(math.Pi / 4); !nearf(e.Facing, want) {
		t.Fatalf("facing %.3f half way, want %.3f", e.Facing, want)
	}
}

// The heading error is wrapped to [−π, π): a turn across ±π goes the short
// way round.
func TestHeadingErrorTakesTheShortWay(t *testing.T) {
	gs, now := motionState(t)
	feed(gs, 3, creatureAt(0x42, 0, 0, 0, 120, [3]int8{})) // 120 × 2π/256: just short of π
	gs.UpdateEntityMotion()
	e := gs.Entities[0x42]
	feed(gs, 3, creatureAt(0x42, 0, 0, 0, -120, [3]int8{})) // just past −π: 16 steps away, not 240
	*now += 100
	gs.UpdateEntityMotion()
	want := facing0To2Pi(float32(math.Pi)) // half way is π itself
	if !nearf(e.Facing, want) && !nearf(e.Facing, want-2*math.Pi) {
		t.Fatalf("facing %.3f half way, want π", e.Facing)
	}
}

// Every frame a creature steps by its velocity over the ticks since its last
// step (dt × 0.001), and the floor probe sets it down, at the update and on
// every step; a probe that meets nothing leaves the height alone.
func TestCreatureStepsAndIsSetDown(t *testing.T) {
	gs, now := motionState(t)
	floorY := float32(2)
	probes := 0
	gs.CreatureFloorFunc = func(x, y, z float32) (float32, bool) {
		probes++
		if x > 50 { // no floor past x = 50
			return y, false
		}
		return floorY, true
	}
	feed(gs, 3, creatureAt(0x43, 40, 3, 0, 0, [3]int8{64, -32, 0}))
	e := gs.Entities[0x43]
	if probes != 1 || !nearf(e.Motion.Pos[1], 2) {
		t.Fatalf("at the update: %d probes, y %.3f, want 1 and 2", probes, e.Motion.Pos[1])
	}
	*now += 500
	gs.UpdateEntityMotion()
	if !nearf(e.X, 40+5.25) || !nearf(e.Y, 2) {
		t.Fatalf("after 500 ticks: (%.3f, %.3f), want (45.25, 2)", e.X, e.Y)
	}
	*now += 1000 // past x = 50: y falls with the velocity, unprobed
	gs.UpdateEntityMotion()
	if !nearf(e.X, 40+15.75) || !nearf(e.Y, 2-5.25) {
		t.Fatalf("off the floor: (%.3f, %.3f), want (55.75, -3.25)", e.X, e.Y)
	}
	// A plain VIEntity (0x81) is not stepped, and not probed.
	plain := entityState(1, 0x44, entityTypePlain, "")
	putMedium(plain[6:], 7)
	feed(gs, 5, plain[:40])
	before := probes
	*now += 1000
	gs.UpdateEntityMotion()
	if p := gs.Entities[0x44]; p == nil || !nearf(p.X, 7) || probes != before+1 {
		t.Fatalf("plain entity: %+v, probes %d (one, the creature's)", p, probes-before)
	}
}

// A 0x00B2 keeps the entities (Respawn → EnterWorld keeps m_entities) and
// leaves an arrival for the visual client: a cast of reach 3, the follow
// stopped. The MemoryDump's arrival has no cast, and fades in from black.
func TestTeleportKeepsEntitiesAndArrives(t *testing.T) {
	gs, _ := motionState(t)
	feed(gs, 3, creatureAt(0x45, 1, 2, 3, 0, [3]int8{}))
	b := []byte{0xB2, 0x00, 0}
	for _, f := range []float32{100, 20, 300, 1.5, 0, 0} {
		u := math.Float32bits(f)
		b = append(b, byte(u), byte(u>>8), byte(u>>16), byte(u>>24))
	}
	b = append(b, 7, 0, 0, 0)
	msg := &protocol.Message{Type: protocol.MessageTypeReliable}
	msg.SetData(b)
	gs.handleTeleport(msg)
	if gs.Entities[0x45] == nil {
		t.Fatal("0x00B2 dropped the entities")
	}
	a := gs.TakeArrival()
	if a == nil || a.Reach != 3 || !a.StopFollow || a.X != 100 || a.Y != 20 || a.Z != 300 {
		t.Fatalf("arrival %+v", a)
	}
	if gs.TakeArrival() != nil {
		t.Fatal("the arrival was handed over twice")
	}
}

// SetFade(1, 0) then SetFade(0, 1), as Msg000D does: black at once, clear a
// second later, linearly; a frame over a tenth of a second counts as 1/30.
func TestFadeInFromBlack(t *testing.T) {
	gs, _ := motionState(t)
	gs.SetFade(1, 0)
	gs.SetFade(0, 1)
	if a := gs.AdvanceFade(0); a != 1 {
		t.Fatalf("alpha %v at once, want 1", a)
	}
	var a float32
	for i := 0; i < 10; i++ {
		a = gs.AdvanceFade(0.05)
	}
	if !nearf(a, 0.5) {
		t.Fatalf("alpha %v after half a second, want 0.5", a)
	}
	if a := gs.AdvanceFade(5); !nearf(a, 0.5-1.0/30) {
		t.Fatalf("alpha %v after a long frame, want %v (1/30 s)", a, 0.5-1.0/30)
	}
	for i := 0; i < 20; i++ {
		a = gs.AdvanceFade(0.05)
	}
	if a != 0 {
		t.Fatalf("alpha %v at the end, want 0 (no overshoot)", a)
	}
}

// The battle music follows +0x263 (byte 155): the player's own out of a
// group, the leader's in one.
func TestBattleMusicFollowsTheCombatByte(t *testing.T) {
	gs, _ := motionState(t)
	self := entityState(1, gs.PlayerObjectID, entityTypeCreature, "Me")
	feed(gs, 2, self)
	if gs.LeaderByte263() {
		t.Fatal("battle music with the byte clear")
	}
	self[155] = 0x24 // ps2_main_capture.pcap's value in a fight
	feed(gs, 2, self)
	if !gs.LeaderByte263() {
		t.Fatal("no battle music with the byte set")
	}
	leader := entityState(1, 0x46, entityTypeCreature, "Leader")
	feed(gs, 4, leader)
	gs.GroupActive = true
	gs.GroupMembers = []GroupMember{{ObjectID: 0x46}, {ObjectID: gs.PlayerObjectID}}
	if gs.LeaderByte263() {
		t.Fatal("in a group the player's own byte decided")
	}
	leader[155] = 1
	feed(gs, 4, leader)
	if !gs.LeaderByte263() {
		t.Fatal("the leader's byte did not")
	}
}

// readS2CTimed is every datagram FROM port, with its capture time in seconds.
func readS2CTimed(t *testing.T, path string, port uint16) (dgs [][]byte, ts []float64) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) < 24 || binary.LittleEndian.Uint32(raw) != 0xa1b2c3d4 {
		t.Skipf("no capture %s", path)
	}
	off := map[uint32]int{1: 14, 113: 16, 276: 20}[binary.LittleEndian.Uint32(raw[20:])]
	for p := 24; p+16 <= len(raw); {
		sec := float64(binary.LittleEndian.Uint32(raw[p:])) + float64(binary.LittleEndian.Uint32(raw[p+4:]))/1e6
		n := int(binary.LittleEndian.Uint32(raw[p+8:]))
		if off == 0 || p+16+n > len(raw) {
			break
		}
		frame := raw[p+16 : p+16+n]
		p += 16 + n
		if len(frame) < off+28 {
			continue
		}
		ip := frame[off:]
		if ip[0]>>4 != 4 || ip[9] != 17 {
			continue
		}
		udp := ip[int(ip[0]&0x0F)*4:]
		ulen := int(binary.BigEndian.Uint16(udp[4:]))
		if binary.BigEndian.Uint16(udp) != port || ulen > len(udp) || ulen < 8 {
			continue
		}
		dgs = append(dgs, append([]byte(nil), udp[8:ulen]...))
		ts = append(ts, sec)
	}
	return dgs, ts
}

// Sandstorm's own creatures, replayed through the intake on the capture's
// clock with a 30 fps frame between datagrams: the velocity is a creature's
// motion in tick-seconds — over every pair of updates that carry the same
// velocity, the distance it really went is what vel × Δt predicts (1min10secs
// measured 0.984, the 983.04 ticks a second) — and what is drawn never runs
// away from the last position the server sent.
func TestReplayedCreaturesMoveAlongTheirVelocity(t *testing.T) {
	for _, name := range []string{"ps2_sandstorm_1min10secs.pcap", "ps2_main_capture.pcap"} {
		t.Run(name, func(t *testing.T) {
			dgs, ts := readS2CTimed(t, filepath.Join("..", "..", "..", "pcaps", name), 10070)
			c, err := network.NewConnection("127.0.0.1", 9, 0xFFFE)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			prevOut := log.Writer()
			log.SetOutput(io.Discard)
			defer log.SetOutput(prevOut)

			gs := NewGameState("test", 10070, 9735, 7000, "test")
			gs.Conn = c
			var now int64
			gs.PS2Clock = func() int64 { return now }
			type seen struct {
				pos [3]float32
				vel [3]float32
				at  float64
			}
			last := map[uint32]seen{}
			var pred, act float64
			pairs, frames := 0, 0
			worst := float32(0)
			t0 := ts[0]
			for i, d := range dgs {
				// The frames since the last datagram, then the datagram.
				target := int64((ts[i] - t0) * ps2TicksPerSecond)
				for now+33 < target {
					now += 33
					gs.UpdateEntityMotion()
					frames++
					for id, e := range gs.Entities {
						l, ok := last[id]
						if !ok {
							continue
						}
						if math.IsNaN(float64(e.X)) || math.IsNaN(float64(e.Z)) {
							t.Fatalf("%08X drawn at NaN", id)
						}
						// Its own position goes no farther than its velocity
						// takes it; what is drawn is that plus the blend.
						m := e.Motion
						dt := float32(ts[i] - l.at)
						reach := float32(math.Hypot(float64(l.vel[0]), float64(l.vel[2])))*dt + 1
						if off := float32(math.Hypot(float64(m.Pos[0]-l.pos[0]), float64(m.Pos[2]-l.pos[2]))); off > reach && off-reach > worst {
							worst = off - reach
						}
						bt := max(0, float32(m.Until-now)/entityBlendTicks)
						if d := float32(math.Hypot(float64(e.X-(m.Pos[0]+m.Err[0]*bt)), float64(e.Z-(m.Pos[2]+m.Err[2]*bt)))); d > 1e-3 {
							t.Fatalf("%08X drawn %.3f off its position plus the blend", id, d)
						}
					}
				}
				now = target
				pkt, err := network.ParsePacket(d, nil)
				if err != nil || pkt.Body == nil {
					continue
				}
				for _, m := range c.ProcessIncoming(pkt) {
					gs.dispatchIncoming(m)
				}
				for id, e := range gs.Entities {
					p := [3]float32{e.TargetX, e.TargetY, e.TargetZ}
					v := [3]float32{e.VelX, e.VelY, e.VelZ}
					l, ok := last[id]
					if ok && p == l.pos && v == l.vel {
						continue
					}
					if ok && p != l.pos && v == l.vel && (v[0] != 0 || v[2] != 0) {
						if dt := ts[i] - l.at; dt > 0.05 && dt < 2 {
							step := math.Hypot(float64(p[0]-l.pos[0]), float64(p[2]-l.pos[2]))
							if step < 50 {
								pred += math.Hypot(float64(v[0]), float64(v[2])) * dt * ps2TicksPerSecond / 1000
								act += step
								pairs++
							}
						}
					}
					last[id] = seen{p, v, ts[i]}
				}
			}
			if pairs < 100 || frames == 0 {
				t.Fatalf("%d same-velocity pairs, %d frames: not enough to judge", pairs, frames)
			}
			t.Logf("%d same-velocity pairs: distance gone / vel × Δt = %.3f; %d frames", pairs, act/pred, frames)
			if r := act / pred; r < 0.9 || r > 1.1 {
				t.Errorf("distance gone / vel × Δt = %.3f over %d pairs, want 1 ± 0.1", r, pairs)
			}
			if worst > 0 {
				t.Errorf("an entity went %.1f past where its velocity reaches from the last update", worst)
			}
		})
	}
}
