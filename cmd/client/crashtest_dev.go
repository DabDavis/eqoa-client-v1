//go:build dev

package main

import (
	"log"
	"os"
	"time"

	"github.com/eqoa/client/internal/game"
)

// EQOA_CRASH_TEST crashes a GM build on purpose, ten seconds into the world,
// so both of crash.go's paths can be checked end to end: "main" panics in the
// game loop (recovered, crash screen), "goroutine" panics on a goroutine of
// its own (the runtime's crash output, offered by the next run). Release
// builds have no such switch.
var crashTestAt time.Time

func (c *Client) crashTestTick() {
	mode := os.Getenv("EQOA_CRASH_TEST")
	if mode == "" || c.engine == nil || c.engine.State.Phase != game.PhaseInWorld {
		return
	}
	if crashTestAt.IsZero() {
		crashTestAt = time.Now().Add(10 * time.Second)
		log.Printf("crash test: %s in 10 s", mode)
	}
	if time.Now().Before(crashTestAt) {
		return
	}
	switch mode {
	case "main":
		var none []int
		_ = none[len(c.engine.State.Entities)+3] // an index out of range, as a real bug would be
	case "goroutine":
		crashTestAt = time.Now().Add(time.Hour)
		go func() { panic("crash test: a goroutine") }()
	}
}
