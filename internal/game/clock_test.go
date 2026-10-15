package game

import (
	"math"
	"testing"
	"time"
)

// The hour is the SECOND timestamp modulo one real hour, scaled to 24 — CLIENT
// func_00BC7B70 — and the first timestamp plays no part.
func TestGameHourIsEpoch2ModOneHour(t *testing.T) {
	cases := []struct {
		ts1, ts2 uint64
		want     float32
	}{
		{0, 0, 0},
		{0, 1_800_000, 12},
		{987_654_321, 5*GameDayMs + 900_000, 6},
		{42, 7*GameDayMs + 2_700_000, 18},
		// Sandstorm's two timestamps sit 1,350,000 ms apart; the old code took
		// that gap as the length of the day.
		{1_700_000_000_000 - 1_350_000, 1_700_000_000_000, float32(1_700_000_000_000%GameDayMs) / GameDayMs * 24},
	}
	for _, c := range cases {
		gs := NewGameState("", 0, 0, 0, "")
		gs.TimeEpoch1, gs.TimeEpoch2 = c.ts1, c.ts2
		gs.TimeReceived = time.Now()
		gs.TimeValid = true
		if got := gs.GameHour(); math.Abs(float64(got-c.want)) > 0.01 {
			t.Errorf("ts1=%d ts2=%d: hour %.4f, want %.4f", c.ts1, c.ts2, got, c.want)
		}
	}
}

// The clock runs at real time: a game day is 60 real minutes.
func TestGameHourAdvancesOneHourPerTwoAndAHalfMinutes(t *testing.T) {
	gs := NewGameState("", 0, 0, 0, "")
	gs.TimeEpoch2 = 0
	gs.TimeReceived = time.Now().Add(-150 * time.Second) // 2.5 real minutes ago
	gs.TimeValid = true
	if got := gs.GameHour(); math.Abs(float64(got-1)) > 0.01 {
		t.Errorf("after 150 s: hour %.4f, want 1", got)
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
