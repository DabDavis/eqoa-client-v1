package game

import (
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/eqoa/client/internal/protocol"
)

// The session lane's opcodes, fed byte for byte the way CLIENT's dispatcher
// (VIClient::ProcessMessage, viclient.cpp) reads them.

type wire []byte

func (w wire) op(op uint16) wire { return append(w, byte(op), byte(op>>8)) }
func (w wire) u8(v byte) wire    { return append(w, v) }
func (w wire) u32(v uint32) wire { return binary.LittleEndian.AppendUint32(w, v) }
func (w wire) u64(v uint64) wire { return binary.LittleEndian.AppendUint64(w, v) }
func (w wire) zz(v int64) wire {
	u := uint64(v) << 1
	if v < 0 {
		u = uint64(^v)<<1 | 1
	}
	for {
		b := byte(u & 0x7F)
		u >>= 7
		if u != 0 {
			w = append(w, b|0x80)
			continue
		}
		return append(w, b)
	}
}
func (w wire) str(s string) wire { return append(w.u32(uint32(len(s))), s...) }
func (w wire) wstr(s string) wire {
	u := utf16.Encode([]rune(s))
	w = w.u32(uint32(len(u)))
	for _, c := range u {
		w = binary.LittleEndian.AppendUint16(w, c)
	}
	return w
}

func guaranteedMsg(data []byte) *protocol.Message {
	m := &protocol.Message{Type: protocol.MessageTypeReliable}
	m.SetData(data)
	return m
}

func newSessionState() *GameState {
	gs := NewGameState("test", 10070, 9735, 7000, "test")
	gs.Phase = PhaseInWorld
	return gs
}

// Every Disconnect(reason, 0) opcode, through HandleMessage where it routes
// today, and straight to handleServerDisconnect for the rest.
func TestServerDisconnectOpcodes(t *testing.T) {
	cases := []struct {
		op     uint16
		body   []byte
		reason int
		text   string
	}{
		{0x0729, nil, 3, "Bad Login/Password"},
		{0x072B, nil, 4, "Not Subscribed."},
		{0x0728, nil, 6, "Server not ready, please try again later. (nodbs)"},
		{0x072A, nil, 7, "Server not ready, please try again later. (loginto)"},
		{0x00EC, nil, 8, "You have logged in from another location."},
		{0x07A4, nil, 8, "You have logged in from another location."},
		{0x07A1, make([]byte, 8), 13, "Your character is in an area of the world that is temporarily unavailable."},
		{0x07A2, nil, 14, "You have been ejected by a server administrator."},
		{0x07C0, nil, 17, "We are updating the game, please try again later."},
		{0x07C1, nil, 18, "We are updating the game, please try again later."},
		{0x07D2, nil, 19, "This character has been modified by a customer service representative."},
	}
	routed := map[uint16]bool{0x0728: true, 0x00EC: true, 0x07A1: true, 0x07A2: true, 0x07C0: true, 0x07C1: true}
	for _, c := range cases {
		gs := newSessionState()
		m := guaranteedMsg(append(wire{}.op(c.op), c.body...))
		if routed[c.op] {
			gs.HandleMessage(m)
		} else {
			gs.handleServerDisconnect(m)
		}
		d := gs.Session.Disconnect
		if !d.Pending || d.Reason != c.reason || d.Quiet || d.Text != c.text || gs.Phase != PhaseDisconnected {
			t.Errorf("0x%04X: got %+v phase %v, want reason %d %q", c.op, d, gs.Phase, c.reason, c.text)
		}
	}
}

// A best-effort message is not the dispatcher's: one that starts with a
// disconnect opcode must not end the session.
func TestServerDisconnectNeedsGuaranteed(t *testing.T) {
	gs := newSessionState()
	m := &protocol.Message{Type: protocol.MessageTypeUnreliable}
	m.SetData(wire{}.op(0x07C1))
	gs.handleAdminShutdown(m)
	if gs.Session.Disconnect.Pending {
		t.Fatal("a 0xFC message disconnected")
	}
}

func TestDiscVersion(t *testing.T) {
	gs := newSessionState()
	gs.HandleMessage(guaranteedMsg(wire{}.op(0x0000).u32(18)))
	if gs.Session.Disconnect.Pending {
		t.Fatal("version 18 disconnected")
	}
	gs.HandleMessage(guaranteedMsg(wire{}.op(0x0000).u32(21)))
	d := gs.Session.Disconnect
	if !d.Pending || d.Reason != 16 || d.Text != "The server has been shutdown by a server administrator." {
		t.Fatalf("version 21: %+v", d)
	}
	if DisconnectKind(16) != 4 {
		t.Fatal("reason 16 is DisconnectKind 4")
	}
}

// 0x0777 is Msg0777: a quiet Disconnect(0, 1) and back to the login window.
func TestDisconnectNoError(t *testing.T) {
	gs := newSessionState()
	gs.HandleMessage(guaranteedMsg(wire{}.op(0x0777)))
	d := gs.Session.Disconnect
	if !d.Pending || !d.Quiet || d.Reason != 0 || d.Text != "" {
		t.Fatalf("0x0777: %+v", d)
	}
	if gs.CombatEnded {
		t.Fatal("0x0777 is not a combat end")
	}
}

func TestConnectStatusStringCodes(t *testing.T) {
	gs := newSessionState()
	if s := gs.ConnectStatusString(0, 7, true); s != "Disconnected (Code: 7)" {
		t.Errorf("reason 0, code 7: %q", s)
	}
	if s := gs.ConnectStatusString(0, 0, false); s != "Disconnected" {
		t.Errorf("reason 0, no code: %q", s)
	}
	if s := gs.ConnectStatusString(10, 3, true); s != "Connection problem or server currently unavailable (3)" {
		t.Errorf("reason 10, code 3: %q", s)
	}
	if s := gs.ConnectStatusString(22, 0, false); s != "" {
		t.Errorf("reason 22: %q", s)
	}
	// The receive loop's 0xFD: 14 → 16, 15 → 15, 1 → 1, else 10 with the code.
	for code, reason := range map[byte]int{1: 1, 14: 16, 15: 15, 9: 10} {
		gs := newSessionState()
		gs.DisconnectFromDRDP(code)
		if gs.Session.Disconnect.Reason != reason {
			t.Errorf("0xFD %d: reason %d, want %d", code, gs.Session.Disconnect.Reason, reason)
		}
	}
}

func TestCharSelectMsg(t *testing.T) {
	for op, want := range map[uint16]string{
		0x07F0: "That character is in an area of the world that is currently unavailable.",
		0x07F1: "That character is in an area of the world that is currently unavailable.",
		0x07F4: "Although you are registered to play EverQuest Online Adventures: Frontiers",
	} {
		gs := newSessionState()
		gs.Phase = PhaseEnteringWorld
		gs.HandleMessage(guaranteedMsg(wire{}.op(op).u64(0x1122334455667788)))
		if gs.Phase != PhaseCharSelect || !strings.HasPrefix(gs.Session.CharSelectMessage, want) {
			t.Errorf("0x%04X: phase %v, %q", op, gs.Phase, gs.Session.CharSelectMessage)
		}
		if len(gs.Entities) != 0 {
			t.Errorf("0x%04X made an entity", op)
		}
	}
}

func TestCreateFailed(t *testing.T) {
	for op, want := range map[uint16]string{
		0x002E: "Character create failed",
		0x0065: "Failed: invalid name",
	} {
		gs := newSessionState()
		gs.Phase = PhaseCharSelect
		gs.HandleMessage(guaranteedMsg(wire{}.op(op).u32(1).u32(2)))
		if gs.CreateError != want || gs.Phase != PhaseCharSelect || len(gs.ParsedItems) != 0 {
			t.Errorf("0x%04X: %q phase %v items %d", op, gs.CreateError, gs.Phase, len(gs.ParsedItems))
		}
	}
	gs := newSessionState()
	gs.handleNameTaken(guaranteedMsg(wire{}.op(0x002F)))
	if gs.CreateError != "Failed: name already taken" {
		t.Errorf("0x002F: %q", gs.CreateError)
	}
}

// The character list off breath_capture.pcap, and CharacterList's limits.
func TestCharacterList(t *testing.T) {
	char := func(w wire, name string, robe uint32) wire {
		w = w.str(name).zz(71500).zz(0x7EDD8F6A).zz(10).zz(2).zz(1)
		w = w.zz(3).zz(2).zz(0).zz(1).u32(robe).u8(0)
		w = w.u32(0).u32(0).u32(0).u8(0).u8(0).u8(0)
		return append(w, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0, 0, 0, 0, 0, 0)
	}

	gs := newSessionState()
	gs.Phase = PhaseConnectingGame
	w := wire{}.op(0x002C).zz(3)
	w = char(w, "Ltcomdata", 0xFFFFFFFF)
	w = char(w, strings.Repeat("x", 24), 4) // 24 bytes: no room for the NUL
	w = char(w, "Smallzzz", 4)
	gs.HandleMessage(guaranteedMsg(w))
	if gs.Phase != PhaseCharSelect || len(gs.Characters) != 3 {
		t.Fatalf("phase %v, %d characters", gs.Phase, len(gs.Characters))
	}
	if c := gs.Characters[0]; c.Name != "Ltcomdata" || c.Class != 10 || c.Race != 2 || c.Level != 1 || c.Robe != 0xFF {
		t.Errorf("char 0: %+v", c)
	}
	if c := gs.Characters[1]; c.Name != "" || c.Level != 1 {
		t.Errorf("a 24-byte name reads empty, the stream stays aligned: %+v", c)
	}
	if c := gs.Characters[2]; c.Name != "Smallzzz" || c.Robe != 4 {
		t.Errorf("robe 4 is kept for the rule set to judge: %+v", c)
	}

	// Clamped to 8.
	gs.Phase = PhaseCharSelect
	w = wire{}.op(0x002C).zz(9)
	for i := 0; i < 9; i++ {
		w = char(w, "c", 0xFFFFFFFF)
	}
	gs.HandleMessage(guaranteedMsg(w))
	if len(gs.Characters) != 8 {
		t.Errorf("count 9: %d characters", len(gs.Characters))
	}

	// Not taken once a character is chosen or in the world (state 4, 5).
	gs.Phase = PhaseEnteringWorld
	gs.HandleMessage(guaranteedMsg(char(wire{}.op(0x002C).zz(1), "late", 0)))
	if len(gs.Characters) != 8 || gs.Phase != PhaseEnteringWorld {
		t.Errorf("phase 4: %d characters, phase %v", len(gs.Characters), gs.Phase)
	}
}

func TestSmallSessionOpcodes(t *testing.T) {
	gs := newSessionState()

	gs.HandleMessage(guaranteedMsg(wire{}.op(0x07D1).u32(0xFFFFFFFF)))
	if gs.Session.ExpansionFlags != 0xFFFFFFFF {
		t.Errorf("0x07D1: 0x%X", gs.Session.ExpansionFlags)
	}

	gs.HandleMessage(guaranteedMsg(wire{}.op(0x07B2).u32(100)))
	if gs.Session.P07578 != 100 || gs.ZoneTransferActive || gs.ZoneTransferState != 0 {
		t.Errorf("0x07B2: %d active=%v state=%d", gs.Session.P07578, gs.ZoneTransferActive, gs.ZoneTransferState)
	}

	gs.Player.Experience = 1234
	gs.HandleMessage(guaranteedMsg(wire{}.op(0x0790).u64(0x05A1D9BC_2776_0001).u32(0xCAFE).u64(42)))
	p := gs.Session.Probe
	if !p.Set || p.Addr != 0x05A1D9BC27760001 || p.Token != 0xCAFE || p.Value != 42 || gs.Player.Experience != 1234 {
		t.Errorf("0x0790: %+v exp %d", p, gs.Player.Experience)
	}

	gs.HandleMessage(guaranteedMsg(wire{}.op(0x0758).str("Welcome back")))
	if n := len(gs.ChatLog); n == 0 || gs.ChatLog[n-1].Text != "Welcome back" {
		t.Errorf("0x0758: %+v", gs.ChatLog)
	}

	gs.HandleMessage(guaranteedMsg(wire{}.op(0x07A3).str("Mcgregor")))
	if gs.Session.ReplyName != "Mcgregor" {
		t.Errorf("0x07A3: %q", gs.Session.ReplyName)
	}

	gs.HandleMessage(guaranteedMsg(wire{}.op(0x0700).u8(35).zz(-3)))
	if gs.Session.AdminLevel != -3 {
		t.Errorf("0x0700/35: %d", gs.Session.AdminLevel)
	}
	gs.HandleMessage(guaranteedMsg(wire{}.op(0x0700).u8(50).wstr("Dab")))
	if n := len(gs.ChatLog); gs.ChatLog[n-1].Text != "Give item: Dab" {
		t.Errorf("0x0700/50: %q", gs.ChatLog[n-1].Text)
	}

	gs.HandleMessage(guaranteedMsg(wire{}.op(0x00F8).u32(0)))
	if !gs.Session.SpeedSet || gs.Player.Speed != 0 {
		t.Errorf("0x00F8 0: set=%v speed=%v", gs.Session.SpeedSet, gs.Player.Speed)
	}
}

// 0x1003..0x1005 off ps2_sandstorm.pcap: the pet "Elder Familiar".
func TestNoCollideList(t *testing.T) {
	gs := newSessionState()
	gs.handleNoCollide(guaranteedMsg(wire{}.op(0x1005).zz(0)))
	gs.handleNoCollide(guaranteedMsg([]byte{0x03, 0x10, 0x0E, 0, 0, 0,
		'E', 'l', 'd', 'e', 'r', ' ', 'F', 'a', 'm', 'i', 'l', 'i', 'a', 'r', 0, 0, 0, 0x60}))
	if !gs.IsNoCollide("Elder Familiar") || gs.Session.NoCollide[0].ID != 0x60000000 {
		t.Fatalf("0x1003: %+v", gs.Session.NoCollide)
	}
	gs.handleNoCollide(guaranteedMsg([]byte{0x04, 0x10, 0, 0, 0, 0x60}))
	if gs.IsNoCollide("Elder Familiar") {
		t.Fatalf("0x1004: %+v", gs.Session.NoCollide)
	}
	gs.handleNoCollide(guaranteedMsg(wire{}.op(0x1005).zz(2).str("a").u32(1).str("b").u32(2)))
	gs.handleNoCollide(guaranteedMsg(wire{}.op(0x1005).zz(1).str("c").u32(3)))
	if len(gs.Session.NoCollide) != 3 {
		t.Errorf("0x1005 appends without clearing: %+v", gs.Session.NoCollide)
	}
}

// SetWeather: a byte past 6 is clear, not ignored.
func TestWeatherOutOfRangeIsClear(t *testing.T) {
	gs := newSessionState()
	gs.Weather = NewWeatherSystem()
	gs.HandleMessage(guaranteedMsg(wire{}.op(0x00C7).u8(2)))
	gs.HandleMessage(guaranteedMsg(wire{}.op(0x00C7).u8(9)))
	if gs.Weather.pending != WeatherClear {
		t.Fatalf("pending %d", gs.Weather.pending)
	}
}

// A small MemoryDump, built the way Msg000D reads it.
func TestMemoryDumpSessionFields(t *testing.T) {
	w := wire{}.op(0x000D).u8(1).str(`data\tunaria.esf`)
	// VIPlayer: id, name, class, race, level, exp, +0x2C, +0x30 byte,
	// tunar, bank, +0x40, +0x44, world; position and rotation.
	w = w.zz(5).str("Dab").zz(6).zz(0).zz(10).zz(100).zz(0).u8(255)
	w = w.zz(50).zz(60).zz(3).zz(0).zz(0)
	for i := 0; i < 6; i++ {
		w = w.u32(0)
	}
	// One hotkey node: dir 1, four records; the first label is 128
	// characters (skipped, empty), the second slot's id is zigzag 2 = 1.
	w = w.zz(1).zz(1)
	w = w.zz(0).wstr(strings.Repeat("L", 128)).wstr("m0")
	w = w.zz(1).wstr("l1").wstr("m1")
	w = w.zz(2).wstr("l2").wstr("m2")
	w = w.zz(3).wstr("l3").wstr("m3")
	// +0x2BB60, +0x2BB0C, then nine quest titles.
	w = w.u32(0).u32(0).u32(9)
	for i := 0; i < 9; i++ {
		w = w.wstr(string(rune('A' + i)))
	}
	w = w.zz(0).u32(0) // inventory
	w = w.zz(0).u32(0) // bank items
	w = w.zz(0)        // bank listings
	w = w.zz(0)        // bank rows
	w = w.u8(1)        // bank flag
	w = w.u8(1)        // HaveCorpse
	w = w.zz(0).u32(0) // the corpse's (empty) list
	w = w.zz(0)        // abilities

	gs := newSessionState()
	gs.Player.Speed = 20
	gs.Session.SpeedSet = true
	gs.handleMemoryDump(guaranteedMsg(w))

	if gs.ZonePath != `data\tunaria.esf` || !gs.Session.WorldPathGate || gs.Player.Name != "Dab" {
		t.Errorf("header: %q gate=%v name=%q", gs.ZonePath, gs.Session.WorldPathGate, gs.Player.Name)
	}
	if gs.Player.Speed != 20 {
		t.Errorf("the dump must not change the walk speed: %v", gs.Player.Speed)
	}
	if len(gs.Hotkeys) != 1 {
		t.Fatalf("hotkeys: %+v", gs.Hotkeys)
	}
	h := gs.Hotkeys[0].Slots
	if h[0].Label != "" || h[0].Msg != "m0" || h[1].SlotID != 1 || h[3].Msg != "m3" {
		t.Errorf("hotkey slots: %+v", h)
	}
	if gs.QuestTextIndex != 8 || gs.QuestTexts[0] != "A" || gs.QuestTexts[7] != "H" {
		t.Errorf("quest titles: %d %q", gs.QuestTextIndex, gs.QuestTexts)
	}
	if !gs.HaveCorpse || gs.Phase != PhaseInWorld {
		t.Errorf("corpse %v phase %v", gs.HaveCorpse, gs.Phase)
	}
}
