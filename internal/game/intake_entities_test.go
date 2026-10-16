package game

import (
	"encoding/binary"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/DabDavis/eqoa-esf-tools/pkg/buffer"
	"github.com/eqoa/client/internal/network"
	"github.com/eqoa/client/internal/protocol"
)

func entReader(b []byte) *buffer.Reader { return buffer.NewReader(b) }

// The ability, combat and player-record messages against the CLIENT decomp
// (decomp/modules/client/src/viclient.cpp). Every fixture is built the way
// CLIENT reads it.

// vims builds a VIMemStream payload.
type vims []byte

func (b vims) op(op uint16) vims  { return append(b, byte(op), byte(op>>8)) }
func (b vims) u8(v byte) vims     { return append(b, v) }
func (b vims) u32(v uint32) vims  { return binary.LittleEndian.AppendUint32(b, v) }
func (b vims) f32(v float32) vims { return b.u32(math.Float32bits(v)) }
func (b vims) signed(v int32) vims { // VIMemStream::WriteSigned
	u := uint32(v) << 1
	if v < 0 {
		u = uint32(^v)<<1 | 1
	}
	for {
		c := byte(u & 0x7F)
		u >>= 7
		if u != 0 {
			c |= 0x80
		}
		b = append(b, c)
		if u == 0 {
			return b
		}
	}
}
func (b vims) wstr(s string) vims { // WriteString(const unsigned short *)
	u := utf16.Encode([]rune(s))
	b = b.u32(uint32(len(u)))
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return b
}

// ability writes one VIAbility as Read takes it: id, display index, valid,
// slot, lockout, +0x14, level, range, cast time ... and no sub-records.
func (b vims) ability(id, display, valid, slot int32, rng float32, name string) vims {
	b = b.signed(id).signed(display).signed(valid).signed(slot).signed(0).signed(-1).signed(5)
	b = b.f32(rng)
	for _, v := range []int32{2, 30, 486870879, 1589074753, 1, 3, -1} {
		b = b.signed(v)
	}
	return b.wstr(name).wstr(name + " desc").signed(0)
}

func entMsg(b vims) *protocol.Message {
	m := &protocol.Message{Type: protocol.MessageTypeReliable}
	m.SetData(b)
	if len(b) >= 2 {
		m.Opcode = protocol.GameOpcode(binary.LittleEndian.Uint16(b))
	}
	return m
}

func entState() *GameState {
	gs := NewGameState("test", 10070, 9735, 7000, "test")
	gs.PlayerObjectID = 0x1F62C628
	return gs
}

// abilitiesState holds four abilities: two memorized (slots 0 and 1), with
// ids out of display order so the id-order walk matters.
func abilitiesState() *GameState {
	gs := entState()
	gs.ParsedSpells = []ParsedSpell{
		{SpellID: 300, AddedOrder: 0, OnHotBar: 1, WhereOnHotBar: 0, Name: "A"},
		{SpellID: 100, AddedOrder: 1, OnHotBar: 1, WhereOnHotBar: 1, Name: "B"},
		{SpellID: 200, AddedOrder: 2, OnHotBar: 0, WhereOnHotBar: -1, Name: "C"},
		{SpellID: 400, AddedOrder: 252, OnHotBar: 0, WhereOnHotBar: -1, Name: "item"},
	}
	gs.BuildHotbar()
	return gs
}

func TestReadAbilityIsVIAbilityRead(t *testing.T) {
	b := vims{}.signed(1817).signed(39).signed(1).signed(2).signed(303300).signed(-1).signed(50)
	b = b.f32(4.5)
	for _, v := range []int32{3, 0, -1809115846, -568605204, 0, 300, -1} {
		b = b.signed(v)
	}
	b = b.wstr("Mouse Form").wstr(strings.Repeat("x", 128)) // 128 + terminator > 128: skipped
	b = b.signed(1).signed(7).signed(-8).signed(9).signed(10).wstr("rec")
	b = append(b, 0xEE) // the next record's first byte
	r := entReader(b)
	sp, err := readAbility(r)
	if err != nil {
		t.Fatal(err)
	}
	if sp.SpellID != 1817 || sp.AddedOrder != 39 || sp.OnHotBar != 1 || sp.WhereOnHotBar != 2 ||
		sp.Lockout != 303300 || sp.ShowHide != -1 || sp.AbilityLevel != 50 {
		t.Fatalf("head %+v", sp)
	}
	if sp.RangeF != 4.5 || sp.Range != 4 || sp.CastTime != 3 || sp.IconColor != -1809115846 ||
		sp.Recast != 300 || sp.EqpRequire != -1 || sp.Name != "Mouse Form" || sp.Desc != "" {
		t.Fatalf("tail %+v", sp)
	}
	if len(sp.Records) != 1 || sp.Records[0] != (AbilityRecord{7, -8, 9, 10, "rec"}) {
		t.Fatalf("records %+v", sp.Records)
	}
	if r.Remaining() != 1 {
		t.Fatalf("%d bytes left, want 1", r.Remaining())
	}
}

func TestVimsSignedIs32Bit(t *testing.T) {
	// Five continuation bytes: the 33rd bit and up fall off, as in the EE's
	// 32-bit ReadUnsigned.
	v, err := vimsSigned(entReader([]byte{0xFE, 0xFF, 0xFF, 0xFF, 0x7F}))
	if err != nil || v != 0x7FFFFFFF {
		t.Fatalf("%d %v", v, err)
	}
	if v, _ := vimsSigned(entReader([]byte{0x01})); v != -1 {
		t.Fatalf("zigzag 1 = %d", v)
	}
}

func TestAbilityAddIsInsertOnly(t *testing.T) {
	gs := abilitiesState()
	gs.HandleMessage(entMsg(vims{}.op(0x00B3).u32(7).u32(1).ability(500, 3, 1, 5, 30, "Fresh")))
	if len(gs.ParsedSpells) != 5 || gs.AbilityStamp != 7 {
		t.Fatalf("%d abilities, stamp %d", len(gs.ParsedSpells), gs.AbilityStamp)
	}
	if gs.Hotbar[5] == nil || gs.Hotbar[5].Name != "Fresh" || gs.Hotbar[0].Name != "A" {
		t.Fatal("hotbar not rebuilt over the new list")
	}
	if n := len(gs.ChatLog); n != 1 || gs.ChatLog[0].Text != "You have gained a new ability." {
		t.Fatalf("chat %+v", gs.ChatLog)
	}
	// An id already in the map keeps its record; flag 0 says nothing.
	gs.HandleMessage(entMsg(vims{}.op(0x00B3).u32(7).u32(0).ability(100, 9, 0, -1, 1, "Other")))
	if len(gs.ParsedSpells) != 5 || gs.ParsedSpells[1].Name != "B" || len(gs.ChatLog) != 1 {
		t.Fatal("Insert replaced a record")
	}
}

func TestAbilityMemorizeSwapsAndEvicts(t *testing.T) {
	gs := abilitiesState()
	// C to slot 0: A leaves the slot, C is memorized there.
	gs.HandleMessage(entMsg(vims{}.op(0x00B4).u32(0).u8(2).u8(0)))
	a, c := gs.ParsedSpells[0], gs.ParsedSpells[2]
	if a.OnHotBar != 0 || a.WhereOnHotBar != -1 || c.OnHotBar != 1 || c.WhereOnHotBar != 0 {
		t.Fatalf("A %+v C %+v", a, c)
	}
	if gs.Hotbar[0] == nil || gs.Hotbar[0].Name != "C" {
		t.Fatal("hotbar slot 0")
	}
	// The same echo again forgets it.
	gs.HandleMessage(entMsg(vims{}.op(0x00B4).u32(0).u8(2).u8(0)))
	if gs.ParsedSpells[2].OnHotBar != 0 || gs.Hotbar[0] != nil {
		t.Fatal("second echo did not forget")
	}
	want := []string{"You have memorized C.", "You have forgotten C."}
	if len(gs.ChatLog) != 2 || gs.ChatLog[0].Text != want[0] || gs.ChatLog[1].Text != want[1] {
		t.Fatalf("chat %+v", gs.ChatLog)
	}
	// An unknown display index changes nothing.
	gs.HandleMessage(entMsg(vims{}.op(0x00B4).u32(0).u8(99).u8(1)))
	if gs.ParsedSpells[1].OnHotBar != 1 || len(gs.ChatLog) != 2 {
		t.Fatal("unknown display index acted")
	}
}

func TestAbilityInvalidateAllSparesItems(t *testing.T) {
	gs := abilitiesState()
	gs.ParsedSpells[3].OnHotBar, gs.ParsedSpells[3].WhereOnHotBar = 1, 9
	gs.HandleMessage(entMsg(vims{}.op(0x00FF).u32(3)))
	for i, sp := range gs.ParsedSpells[:3] {
		if sp.OnHotBar != 0 || sp.WhereOnHotBar != -1 {
			t.Fatalf("ability %d still memorized", i)
		}
	}
	if gs.ParsedSpells[3].OnHotBar != 1 || gs.Hotbar[9] == nil {
		t.Fatal("an item entry (display >= 0xFC) was cleared")
	}
}

func TestAbilitySlotMoveShifts(t *testing.T) {
	gs := entState()
	for i := 0; i < 4; i++ {
		gs.ParsedSpells = append(gs.ParsedSpells, ParsedSpell{SpellID: int64(10 + i), AddedOrder: int64(i),
			OnHotBar: 1, WhereOnHotBar: int64(i), Name: string(rune('A' + i))})
	}
	gs.BuildHotbar()
	// Slot 0 to slot 3 (Msg00B5): B C D close up to 0 1 2, `to` drops to 2,
	// slots >= 2 open up, and A lands on 2 — A B C D becomes B C A D.
	gs.HandleMessage(entMsg(vims{}.op(0x00B5).u32(0).u8(0).u8(3)))
	got := ""
	for _, sp := range gs.Hotbar[:4] {
		if sp == nil {
			got += "-"
		} else {
			got += sp.Name
		}
	}
	if got != "BCAD" {
		t.Fatalf("bar %q, want BCAD", got)
	}
}

func TestAbilityDisplayMoveShifts(t *testing.T) {
	gs := entState()
	for i := 0; i < 4; i++ {
		gs.ParsedSpells = append(gs.ParsedSpells, ParsedSpell{SpellID: int64(10 + i), AddedOrder: int64(i), Name: string(rune('A' + i))})
	}
	gs.ParsedSpells = append(gs.ParsedSpells, ParsedSpell{SpellID: 99, AddedOrder: 253, Name: "item"})
	gs.handleAbilityDisplayMove(entMsg(vims{}.op(0x00B6).u32(0).u8(3).u8(0)))
	var got []int64
	for _, sp := range gs.ParsedSpells {
		got = append(got, sp.AddedOrder)
	}
	// D moves to the front: A B C shift up, the item entry stays.
	want := []int64{1, 2, 3, 0, 253}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("display %v, want %v", got, want)
		}
	}
}

func TestAbilitySetP14(t *testing.T) {
	gs := abilitiesState()
	gs.HandleMessage(entMsg(vims{}.op(0x00FD).u32(0).u32(100).u32(5)))
	if b := gs.ParsedSpells[1]; b.ShowHide != 5 || b.OnHotBar != 1 {
		t.Fatalf("B %+v", b)
	}
	gs.HandleMessage(entMsg(vims{}.op(0x00FD).u32(0).u32(100).u32(0)))
	if b := gs.ParsedSpells[1]; b.ShowHide != 0 || b.OnHotBar != 0 || gs.Hotbar[1] != nil {
		t.Fatalf("0 did not invalidate: %+v", b)
	}
}

func TestAbilityRemoveRebuildsTheBar(t *testing.T) {
	gs := abilitiesState()
	gs.HandleMessage(entMsg(vims{}.op(0x00B8).u32(0).signed(0))) // A, display 0
	if len(gs.ParsedSpells) != 3 || gs.ParsedSpells[0].Name != "B" {
		t.Fatalf("list %+v", gs.ParsedSpells)
	}
	// The bar points into the list, which just moved under it.
	if gs.Hotbar[0] != nil || gs.Hotbar[1] == nil || gs.Hotbar[1].Name != "B" || gs.Hotbar[1] != &gs.ParsedSpells[0] {
		t.Fatal("hotbar left pointing at the old list")
	}
}

func TestCooldownFirstInIDOrder(t *testing.T) {
	gs := abilitiesState()
	gs.ParsedSpells[2].AddedOrder = 1 // C (id 200) shares B's (id 100) display index
	// Captured: cd00 13 00000000 c833 — display 19, lockout 3300 ms.
	gs.HandleMessage(entMsg(vims{}.op(0x00CD).u8(1).u32(4).signed(3300)))
	if gs.ParsedSpells[1].Lockout != 3300 || gs.ParsedSpells[2].Lockout != 0 || gs.AbilityStamp != 4 {
		t.Fatalf("B %d C %d", gs.ParsedSpells[1].Lockout, gs.ParsedSpells[2].Lockout)
	}
	if r := gs.CooldownRemaining(1); r < 3 || r > 3.3 {
		t.Fatalf("slot 1 cooldown %.2f", r)
	}
	gs.HandleMessage(entMsg(vims{}.op(0x00CD).u8(1).u32(4).signed(0)))
	if gs.ParsedSpells[1].Lockout != 0 || gs.CooldownRemaining(1) != 0 {
		t.Fatal("a 0 lockout did not clear it")
	}
}

func TestDamageIndicationPlacementAndColour(t *testing.T) {
	gs := entState()
	gs.Player.X = 1
	npc := uint32(0x013CC7F2)
	gs.Entities[npc] = &Entity{ObjectID: npc, X: 50}
	// Captured shapes: self hits the NPC (28c6621f b004 f2c73c01, +280), the
	// NPC hits self (f2c73c01 43 28c6621f, -34), self heals self.
	gs.HandleMessage(entMsg(vims{}.op(0x00DB).u32(gs.PlayerObjectID).signed(-280).u32(npc)))
	gs.HandleMessage(entMsg(vims{}.op(0x00DB).u32(npc).signed(-34).u32(gs.PlayerObjectID)))
	gs.HandleMessage(entMsg(vims{}.op(0x00DB).u32(gs.PlayerObjectID).signed(285).u32(gs.PlayerObjectID)))
	// Someone else's hit floats over ME, whoever the defender was.
	gs.HandleMessage(entMsg(vims{}.op(0x00DB).u32(npc).signed(-5).u32(0x12345678)))
	// My hit on something not in the scene draws nothing.
	gs.HandleMessage(entMsg(vims{}.op(0x00DB).u32(gs.PlayerObjectID).signed(-9).u32(0x12345678)))
	want := []struct {
		text  string
		x     float32
		color [3]uint8
	}{
		{"280", 50, [3]uint8{255, 255, 255}},
		{"34", 1, [3]uint8{204, 204, 25}},
		{"285", 1, [3]uint8{0, 255, 0}},
		{"5", 1, [3]uint8{204, 204, 25}},
	}
	if len(gs.FloatingTexts) != len(want) {
		t.Fatalf("%d numbers: %+v", len(gs.FloatingTexts), gs.FloatingTexts)
	}
	for i, w := range want {
		ft := gs.FloatingTexts[i]
		if ft.Text != w.text || ft.X != w.x || ft.Color != w.color || ft.HasColor {
			t.Fatalf("number %d: %+v, want %+v", i, ft, w)
		}
	}
}

func TestResourceUpdateIsSetAction(t *testing.T) {
	gs := entState()
	npc := uint32(0x4000)
	gs.Entities[npc] = &Entity{ObjectID: npc}
	sys := NewSpellEffectSystem(nil)
	var calls [][2]uint32
	sys.OnSetAction = func(id uint32, a int32) int { calls = append(calls, [2]uint32{id, uint32(a)}); return 0 }
	sys.IsCSprite = func(uint32) bool { return true }
	gs.SpellFXSys = sys
	gs.HandleMessage(entMsg(vims{}.op(0x0775).u32(npc).u8(7)))
	gs.HandleMessage(entMsg(vims{}.op(0x0775).u32(gs.PlayerObjectID).u8(7)))  // mine: only 34
	gs.HandleMessage(entMsg(vims{}.op(0x0775).u32(gs.PlayerObjectID).u8(34))) // breath_capture's
	gs.HandleMessage(entMsg(vims{}.op(0x0775).u32(0x9999).u8(7)))             // not found
	if len(calls) != 2 || calls[0] != [2]uint32{npc, 7} || calls[1] != [2]uint32{gs.PlayerObjectID, 34} {
		t.Fatalf("SetAction calls %v", calls)
	}
}

func TestTargetInfoStringsAndToken(t *testing.T) {
	gs := entState()
	b := vims{}.op(0x0010).u8(4).u8(5).u8(1)
	title := make([]byte, 128)
	copy(title, []byte{'T', 0, 'i', 0})
	guild := make([]byte, 128)
	copy(guild, []byte{'G', 0})
	b = append(append(b, title...), guild...)
	gs.HandleMessage(entMsg(b)) // Sandstorm's 261 bytes, no token
	if gs.TargetFaceColor != 4 || gs.TargetConColor != 5 || gs.TargetTitle != "Ti" || gs.TargetGuildName != "G" {
		t.Fatalf("face %d con %d title %q guild %q", gs.TargetFaceColor, gs.TargetConColor, gs.TargetTitle, gs.TargetGuildName)
	}
	// A reply carrying another request's token stores the strings only.
	b[2], b[3], b[4] = 0, 2, 0
	gs.HandleMessage(entMsg(b.u32(9)))
	if gs.TargetFaceColor != 4 || gs.TargetConColor != 5 {
		t.Fatal("a foreign reply set the colours")
	}
}

func TestPlayerRecordMessages(t *testing.T) {
	gs := entState()
	gs.HandleMessage(entMsg(vims{}.op(0x0020).signed(51).signed(12345)))
	if gs.Player.Level != 51 || gs.Player.XPRaw != 12345 {
		t.Fatalf("level %d xp %d", gs.Player.Level, gs.Player.XPRaw)
	}
	gs.HandleMessage(entMsg(vims{}.op(0x00D9).u32(0xDEADBEEF)))
	if gs.ExpDebt != 0xDEADBEEF || gs.Player.HPInt != 0 {
		t.Fatalf("0x00D9: %#x, HP %d", gs.ExpDebt, gs.Player.HPInt)
	}
	// Captured: 5200 9808 is 524 tunar.
	gs.HandleMessage(entMsg(vims{0x52, 0x00, 0x98, 0x08}))
	if gs.Tunar != 524 {
		t.Fatalf("tunar %d", gs.Tunar)
	}
	gs.TrainingPoints = 2
	gs.HandleMessage(entMsg(vims{}.op(0x001D).signed(3)))
	if gs.TrainingPoints != -1 {
		t.Fatalf("training points %d (CLIENT subtracts, no clamp)", gs.TrainingPoints)
	}
	gs.HandleMessage(entMsg(vims{}.op(0x00FC).u8(1)))
	if gs.MoveLock != 1 {
		t.Fatal("0x00FC")
	}
}

func TestTeleportKeepsTheToken(t *testing.T) {
	gs := entState()
	// local-warp.pcap: world 8, (1200, 0, 1000), HPR 0, token 1; the PS2's
	// 0x0049 answer is 02.
	b := vims{}.op(0x00B2).u8(8).f32(1200).f32(0).f32(1000).f32(0).f32(0).f32(0).u32(1)
	gs.handleTeleport(entMsg(b))
	if gs.TeleportToken != 1 || gs.TeleportWorld != 8 || gs.Player.X != 1200 || !gs.TeleportPending {
		t.Fatalf("token %d world %d x %.0f", gs.TeleportToken, gs.TeleportWorld, gs.Player.X)
	}
}

func TestOutMatched(t *testing.T) {
	gs := entState()
	gs.HandleMessage(entMsg(vims{}.op(0x077A)))
	if len(gs.ChatLog) != 1 || !strings.HasPrefix(gs.ChatLog[0].Text, "You are outmatched.") ||
		gs.ChatLog[0].ColorR != 255 || gs.ChatLog[0].ColorG != 64 || !gs.CombatEnded {
		t.Fatalf("%+v ended=%v", gs.ChatLog, gs.CombatEnded)
	}
}

func TestDeathReadsNoPayload(t *testing.T) {
	gs := entState()
	gs.CastDurationMs = 2000
	gs.HandleMessage(entMsg(vims{}.op(0x005E)))
	if !gs.Player.Dead || gs.CastDurationMs != 0 {
		t.Fatal("death")
	}
}

// The PS2's own session on Sandstorm (ps2_main_capture.pcap): three 0x0033
// echoes and thirteen 0x00CD, against the MemoryDump's 42 abilities.
func TestCaptureAbilitiesThroughIntake(t *testing.T) {
	d := replayReadUDP(t, filepath.Join("..", "..", "..", "pcaps", "ps2_main_capture.pcap"), 10070)
	if len(d) == 0 {
		t.Skip("no capture")
	}
	gs := entitiesReplay(t, d)
	if len(gs.ParsedSpells) != 42 {
		t.Fatalf("%d abilities", len(gs.ParsedSpells))
	}
	name := func(slot int) string {
		if gs.Hotbar[slot] == nil {
			return ""
		}
		return gs.Hotbar[slot].Name
	}
	// (0, 0) put Chant of Battle on slot 0 over Entrancing Song, (31, 0) put
	// Entrancing Song back over it, (39, 2) Mouse Form over Remove Illusion.
	if name(0) != "Entrancing Song" || name(2) != "Mouse Form" {
		t.Fatalf("slot 0 %q, slot 2 %q", name(0), name(2))
	}
	for _, sp := range gs.ParsedSpells {
		switch sp.Name {
		case "Chant of Battle", "Remove Illusion":
			if sp.OnHotBar != 0 || sp.WhereOnHotBar != -1 {
				t.Fatalf("%s still memorized", sp.Name)
			}
		case "Mouse Form":
			if sp.Lockout != 303300 { // the last 0x00CD: 27 00000000 888325
				t.Fatalf("Mouse Form lockout %d", sp.Lockout)
			}
		default:
			if sp.Lockout != 0 {
				t.Fatalf("%s lockout %d, every other one was cleared", sp.Name, sp.Lockout)
			}
		}
		if sp.RangeF != 4.5 && sp.RangeF != 20 && sp.RangeF != 30 && sp.RangeF != 35 {
			t.Fatalf("%s range %g", sp.Name, sp.RangeF)
		}
	}
	if gs.Tunar != 562 {
		t.Fatalf("tunar %d", gs.Tunar)
	}
}

// entitiesReplay is the replay test's intake run, keeping the state.
func entitiesReplay(t *testing.T, datagrams [][]byte) *GameState {
	t.Helper()
	var pkts []*network.ReceivedPacket
	count := map[uint16]int{}
	for _, d := range datagrams {
		pkt, err := network.ParsePacket(d, nil)
		if err != nil || pkt.Body == nil {
			continue
		}
		pkts = append(pkts, pkt)
		count[pkt.LocalEndpoint]++
	}
	var ep uint16
	for e, n := range count {
		if n > count[ep] || (n == count[ep] && e < ep) {
			ep = e
		}
	}
	c, err := network.NewConnection("127.0.0.1", 9, 0xFFFE)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	gs := NewGameState("test", 10070, 9735, 7000, "test")
	gs.Conn = c
	for _, pkt := range pkts {
		if pkt.LocalEndpoint != ep {
			continue
		}
		for _, m := range c.ProcessIncoming(pkt) {
			gs.dispatchIncoming(m)
		}
	}
	return gs
}
