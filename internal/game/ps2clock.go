package game

import "time"

// ps2clock.go — the PS2's clock, and VISyncTime.
//
// VITimer (SUPPORT vitimer.cpp, 0x003E8400) counts BUSCLK/16 = 9.216 MHz up
// to 18,750 and adds 2 to SystemTime on every compare interrupt: 983.04
// units a real second, not 1,000. Every CLIENT time is one of these — the
// frame clock at VIClient+0x7568, VIScene::GetTime, the 200 of an entity's
// blend, the 3,600,000 of a game day — so a game day lasts 61.04 real
// minutes, and claude-notes/npc-extrapolation-limiter-20260816.md measured
// the same tick off a live frame (16-18 to a 60 fps frame).

// ps2TicksPerSecond is VITimer::GetTime's rate.
const ps2TicksPerSecond = 983.04

// ps2Epoch is the Go client's VITimer::Init: tick 0.
var ps2Epoch = time.Now()

// ps2Ticks converts a time since ps2Epoch into VITimer ticks.
func ps2Ticks(t time.Time) int64 {
	return int64(t.Sub(ps2Epoch).Seconds() * ps2TicksPerSecond)
}

// ps2Now is VITimer::GetTime: the real clock unless a test set PS2Clock.
func (gs *GameState) ps2Now() int64 {
	if gs.PS2Clock != nil {
		return gs.PS2Clock()
	}
	return ps2Ticks(time.Now())
}

// syncBlendTicks is VISyncTime's BLEND_INTERVAL: a correction is blended in
// over 60,000 ticks, 61 real seconds.
const syncBlendTicks = 60000

// syncTime is VISyncTime (VISyncTime.cpp; Set 0x00CBCBD8, GetTime 0x00CBCC80,
// read off the disassembly [A]): a server time and the local tick it arrived
// on. The first Set takes the time as it is; a later one keeps the time where
// it was and blends the difference in over syncBlendTicks, so a resync never
// makes the hour jump.
type syncTime struct {
	ready bool  // +0x00 m_bReady
	blend int64 // +0x08 m_Blend: the correction being blended in
	base  int64 // +0x10: the local tick of the last Set
	t     int64 // +0x18: the synced time at base
}

// Set is VISyncTime::Set(t, now).
func (s *syncTime) Set(t, now int64) {
	if !s.ready {
		s.ready = true
		s.base = now
		s.t = t
		s.blend = 0
		return
	}
	cur := s.Get(now)
	s.blend = t - cur
	s.base = now
	s.t = cur
}

// Get is VISyncTime::GetTime(now): the synced time plus the local ticks since
// it, plus as much of the correction as min(elapsed, 60,000)/60,000 of it —
// __muldi3 then __divdi3, a truncating 64-bit division.
func (s *syncTime) Get(now int64) int64 {
	e := now - s.base
	k := e
	if syncBlendTicks < e {
		k = syncBlendTicks
	}
	return s.t + e + s.blend*k/syncBlendTicks
}
