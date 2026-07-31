package game

import (
	"testing"

	"github.com/eqoa/client/internal/protocol"
)

// A packet handler must not crash the client when there is no connection to
// answer on.
//
// Neither QueueReliable nor Flush is nil-receiver-safe — Flush locks the
// connection's mutex on its first line — so an unguarded send is a nil deref,
// which takes the process down rather than returning an error. CLAUDE.md records
// that packet handling is panic-guarded precisely because a malformed or
// ill-timed packet must not do that.
//
// gs.Conn is assigned once (engine.go) and never cleared, so in a real session
// these are all reachable only with a live connection. The guards are for the
// paths that are not a real session: tests that drive handlers directly, and any
// future teardown or reconnect that clears Conn while dispatch is still running.
func TestPacketHandlersSurviveNilConn(t *testing.T) {
	msgFor := func(opcode protocol.GameOpcode, payload []byte) *protocol.Message {
		m := &protocol.Message{}
		// Handlers read past the two opcode bytes, so the wire form carries them.
		data := append([]byte{byte(opcode), byte(opcode >> 8)}, payload...)
		m.SetData(data)
		m.Opcode = opcode
		return m
	}

	cases := []struct {
		name string
		run  func(gs *GameState)
	}{
		{"Camera1", func(gs *GameState) {
			gs.handleCamera1(msgFor(protocol.OpcodeCamera1, []byte{3, 0, 0, 0}))
		}},
		// Under six bytes the value parse is skipped and only the flush runs,
		// which is the path that actually crashed.
		{"Camera1 short", func(gs *GameState) {
			gs.handleCamera1(msgFor(protocol.OpcodeCamera1, nil))
		}},
		{"Camera2", func(gs *GameState) {
			gs.handleCamera2(msgFor(protocol.OpcodeCamera2, nil))
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gs := NewGameState("test", 10070, 9735, 7000, "test")
			if gs.Conn != nil {
				t.Fatal("test setup: expected a GameState with no connection")
			}
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("handler panicked with no connection: %v", r)
				}
			}()
			c.run(gs)
		})
	}
}
