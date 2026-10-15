package game

import (
	"testing"

	"github.com/eqoa/client/internal/protocol"
)

func s2msg(op protocol.GameOpcode, body []byte) *protocol.Message {
	m := &protocol.Message{Opcode: op}
	m.SetData(append([]byte{byte(op), byte(op >> 8)}, body...))
	return m
}

// A full 0x1500 entry, laid out as Sandstorm's S2 reads it.
func TestPlayerSearchEntry(t *testing.T) {
	gs := NewGameState("test", 10070, 9735, 7000, "test")
	body := []byte{6, 3, 50}                                   // BAR, SK, 50
	body = append(body, []byte("Mcgregor\x00\x00\x00\x00")...) // 12-byte name slot
	body = append(body, 0x78, 0x56, 0x34, 0x12)                // id
	body = append(body, 0, 11, 6)                              // Tunaria, col 11, row 6
	body = append(body, 2)                                     // group of 2
	body = append(body, []byte("LF healer")...)
	gs.HandleMessage(s2msg(protocol.OpcodePlayerSearchEntry, body))
	if len(gs.PlayerSearch) != 1 {
		t.Fatalf("%d entries", len(gs.PlayerSearch))
	}
	e := gs.PlayerSearch[0]
	if e.Race != 6 || e.Class != 3 || e.Level != 50 || e.Name != "Mcgregor" || e.ID != 0x12345678 ||
		!e.HasLocation || e.World != 0 || e.Col != 11 || e.Row != 6 || e.Group != 2 || e.Note != "LF healer" {
		t.Fatalf("parsed %+v", e)
	}
	words := make(Words, 1100)
	words[0x413-1], words[358-1] = "Tunaria", "Freeport"
	if got := LocationLine(words, e.World, e.Col, e.Row); got != "Location: Tunaria, Freeport" {
		t.Fatalf("location %q", got)
	}

	// A short entry: no id, no location, no group (0xFF: no group line).
	gs.HandleMessage(s2msg(protocol.OpcodePlayerSearchEntry, append([]byte{0, 0, 1}, "Bob"...)))
	if e := gs.PlayerSearch[1]; e.Name != "Bob" || e.ID != 0 || e.HasLocation || e.Group != 0xFF {
		t.Fatalf("short entry %+v", e)
	}
	// Under 3 bytes: ignored. 0x1501 clears.
	gs.HandleMessage(s2msg(protocol.OpcodePlayerSearchEntry, []byte{1, 2}))
	if len(gs.PlayerSearch) != 2 {
		t.Fatal("a 2-byte entry was taken")
	}
	gs.HandleMessage(s2msg(protocol.OpcodePlayerSearchClear, nil))
	if len(gs.PlayerSearch) != 0 {
		t.Fatal("0x1501 did not clear the results")
	}
}

// Worlds 6 and 7, and a cell off the grid, name the world only.
func TestLocationLineEdges(t *testing.T) {
	words := make(Words, 1800)
	words[0x6F8-1], words[0x5A0-1] = "Secret Zones", "Odus"
	if got := LocationLine(words, 7, 1, 1); got != "Location: Secret Zones" {
		t.Fatalf("world 7: %q", got)
	}
	if got := LocationLine(words, 2, 0, 0); got != "Location: Odus" {
		t.Fatalf("off Odus's land: %q", got)
	}
	if got := LocationLine(words, 8, 0, 0); got != "" {
		t.Fatalf("world 8: %q", got)
	}
}

// 0x00DB: three more bytes after the defender are its colour.
func TestDamageColor(t *testing.T) {
	gs := NewGameState("test", 10070, 9735, 7000, "test")
	plain := []byte{1, 0, 0, 0, 0x13, 2, 0, 0, 0} // attacker, zigzag -10, defender
	gs.HandleMessage(s2msg(protocol.OpcodeDamageIndication, plain))
	gs.HandleMessage(s2msg(protocol.OpcodeDamageIndication, append(append([]byte{}, plain...), 255, 128, 0)))
	if len(gs.FloatingTexts) != 2 {
		t.Fatalf("%d floating texts", len(gs.FloatingTexts))
	}
	if gs.FloatingTexts[0].HasColor {
		t.Fatal("a retail 0x00DB got a colour")
	}
	if ft := gs.FloatingTexts[1]; !ft.HasColor || ft.Color != [3]uint8{255, 128, 0} || ft.Text != "-10" {
		t.Fatalf("coloured number %+v", ft)
	}
}

func TestParseWords(t *testing.T) {
	w := ParseWords([]byte("\xEF\xBB\xBFEnglish\r\nJapanese\r\n"))
	if w.Get(1) != "English" || w.Get(2) != "Japanese" || w.Get(0) != "" || w.Get(99) != "" {
		t.Fatalf("%q", w)
	}
}
