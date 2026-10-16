package game

import (
	"math"
	"testing"
	"time"
)

// pinClock makes gs's VITimer read *now.
func pinClock(gs *GameState, now *int64) {
	gs.PS2Clock = func() int64 { return *now }
}

// The hour is the SECOND timestamp modulo 3,600,000, scaled to 24 — CLIENT
// UpdateTimeOfDay (0x00BC7B70) — and the first timestamp plays no part.
func TestGameHourIsEpoch2ModOneHour(t *testing.T) {
	cases := []struct {
		ts1, ts2 int64
		want     float32
	}{
		{0, 0, 0},
		{0, 1_800_000, 12},
		{987_654_321, 5*GameDayMs + 900_000, 6},
		{42, 7*GameDayMs + 2_700_000, 18},
		// Sandstorm's two timestamps sit 1,350,000 apart; the old code took
		// that gap as the length of the day.
		{1_700_000_000_000 - 1_350_000, 1_700_000_000_000, float32(1_700_000_000_000%GameDayMs) / GameDayMs * 24},
	}
	for _, c := range cases {
		gs := NewGameState("", 0, 0, 0, "")
		now := int64(123_456)
		pinClock(gs, &now)
		gs.setTime(c.ts1, c.ts2)
		if got := gs.GameHour(); math.Abs(float64(got-c.want)) > 0.01 {
			t.Errorf("ts1=%d ts2=%d: hour %.4f, want %.4f", c.ts1, c.ts2, got, c.want)
		}
	}
}

// The clock runs in VITimer ticks: an hour of game time is 150,000 of them,
// 152.6 real seconds, and a day 61.04 real minutes.
func TestGameHourAdvancesOneHourPer150000Ticks(t *testing.T) {
	gs := NewGameState("", 0, 0, 0, "")
	now := int64(0)
	pinClock(gs, &now)
	gs.setTime(0, 0)
	now = 150_000
	if got := gs.GameHour(); math.Abs(float64(got-1)) > 0.0001 {
		t.Errorf("after 150,000 ticks: hour %.4f, want 1", got)
	}
	if d := time.Duration(GameDayMs / ps2TicksPerSecond * float64(time.Second)); d.Round(time.Second) != 3662*time.Second {
		t.Errorf("a game day lasts %v of real time, want 61m02s", d.Round(time.Second))
	}
}

// A second 0x0013 does not jump the clock: VISyncTime keeps the time where it
// was and blends the correction in over 60,000 ticks.
func TestGameHourResyncBlendsOverAMinute(t *testing.T) {
	gs := NewGameState("", 0, 0, 0, "")
	now := int64(1000)
	pinClock(gs, &now)
	gs.setTime(0, 0)
	now = 11_000 // 10,000 ticks on: the clock reads 10,000
	gs.setTime(0, 10_000+150_000)
	if got := gs.clockB.Get(now); got != 10_000 {
		t.Fatalf("at the resync: %d, want 10000 (no jump)", got)
	}
	now += 30_000
	if got, want := gs.clockB.Get(now), int64(10_000+30_000+75_000); got != want {
		t.Errorf("half way: %d, want %d", got, want)
	}
	now += 30_000
	if got, want := gs.clockB.Get(now), int64(10_000+60_000+150_000); got != want {
		t.Errorf("after 60,000 ticks: %d, want %d", got, want)
	}
	now += 100_000
	if got, want := gs.clockB.Get(now), int64(10_000+160_000+150_000); got != want {
		t.Errorf("long after: %d, want %d (the whole correction, no more)", got, want)
	}
}

func TestSetGameHour(t *testing.T) {
	gs := NewGameState("", 0, 0, 0, "")
	for _, h := range []float32{0, 6, 12.5, 23.75, 30, -1} {
		gs.SetGameHour(h)
		want := float32(math.Mod(float64(h)+24, 24))
		if got := gs.GameHour(); math.Abs(float64(got-want)) > 0.01 {
			t.Errorf("SetGameHour(%v): hour %.4f, want %.4f", h, got, want)
		}
	}
}
