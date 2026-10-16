package game

import (
	"encoding/binary"
	"math"
	"testing"
	"unicode/utf16"

	"github.com/eqoa/client/internal/protocol"
)

// These pin the receive loop and the state channels to CLIENT's own:
// VIClient::PumpMessages, ProcessStateChannel, ChannelEntity and the entity
// readers it calls, Channel40/42/43/44 and the shard list. Each fixture is
// laid out the way CLIENT's reader takes it.

func routingState(t *testing.T) *GameState {
	t.Helper()
	gs := NewGameState("test", 10070, 9735, 7000, "test")
	gs.noteSelf(&protocol.Message{Src: 0x1D9F67A2})
	return gs
}

// entityState is a 162-byte creature message: flag, id, type, then the
// fields this test reads back.
func entityState(keep byte, id uint32, kind byte, name string) []byte {
	b := make([]byte, 162)
	b[0] = keep
	binary.LittleEndian.PutUint32(b[1:], id)
	b[5] = kind
	b[26] = 1                                         // alive
	b[27] = 200                                       // HP bar
	binary.LittleEndian.PutUint32(b[28:], 0x70D898C6) // model
	binary.LittleEndian.PutUint32(b[36:], math.Float32bits(1))
	copy(b[116:140], name)
	b[140] = 12 // level
	return b
}

func chMsg(ch byte, data []byte) *protocol.Message {
	m := &protocol.Message{Type: protocol.MessageType(ch)}
	m.SetData(data)
	return m
}

func feed(gs *GameState, ch byte, data []byte) { gs.HandleStateChannel(ch, chMsg(ch, data)) }

func TestEntitySlotFollowsTheFlag(t *testing.T) {
	gs := routingState(t)
	const a, b, c = 0x0000E4CD, 0x0000E802, 0x00101666

	// An empty slot makes the entity whatever the flag: Sandstorm's first
	// message on a channel carries 0.
	feed(gs, 3, entityState(0, a, entityTypeCreature, "Guard"))
	ent := gs.Entities[a]
	if ent == nil || ent.Name != "Guard" || ent.Channel != 3 {
		t.Fatalf("first message: %+v", ent)
	}
	ent.Dead = true // a field only this object carries

	// Flag set, same type and id: the same entity, read again.
	feed(gs, 3, entityState(1, a, entityTypeCreature, "Guard"))
	if gs.Entities[a] != ent || !ent.Dead {
		t.Fatal("a kept entity was remade")
	}

	// Flag 0 on the same entity deletes it, and nothing is made.
	feed(gs, 3, entityState(0, a, entityTypeCreature, "Guard"))
	if gs.Entities[a] != nil || gs.entitySlots[3].used {
		t.Fatal("flag 0 on the slot's own entity left it")
	}

	// The slot is empty again, so flag 0 makes one.
	feed(gs, 3, entityState(0, a, entityTypeCreature, "Guard"))
	if gs.Entities[a] == nil {
		t.Fatal("an empty slot did not make the entity")
	}

	// Another id with the flag set replaces it.
	feed(gs, 3, entityState(1, b, entityTypeCreature, "Merchant"))
	if gs.Entities[a] != nil || gs.Entities[b] == nil || gs.ChannelEntity[3] != b {
		t.Fatal("a new id did not replace the old entity")
	}

	// Another id with flag 0 only deletes.
	feed(gs, 3, entityState(0, c, entityTypeCreature, "Cat"))
	if gs.Entities[b] != nil || gs.Entities[c] != nil {
		t.Fatal("flag 0 with a new id must delete and make nothing")
	}

	// Under 5 bytes empties the slot.
	feed(gs, 3, entityState(1, c, entityTypeCreature, "Cat"))
	feed(gs, 3, []byte{1, 2, 3, 4})
	if gs.Entities[c] != nil || gs.entitySlots[3].used {
		t.Fatal("a 4-byte message did not empty the slot")
	}
}

func TestEntityTypesAndChannels(t *testing.T) {
	gs := routingState(t)

	// VIEntity (0x81) reads 40 bytes: the model, not the creature's name.
	feed(gs, 1, entityState(1, 0x10, entityTypePlain, "Chest"))
	if e := gs.Entities[0x10]; e == nil || e.ModelID != 0x70D898C6 || e.Name != "" || e.Level != 0 {
		t.Fatalf("plain entity: %+v", e)
	}

	// The factory makes only 0x81 and 0x82.
	feed(gs, 2, entityState(1, 0x11, 0x07, "Nothing"))
	if gs.Entities[0x11] != nil || gs.entitySlots[2].used {
		t.Fatal("type 0x07 made an entity")
	}

	// 24 entity slots: 0x18 and on are read by nothing.
	feed(gs, 0x18, entityState(1, 0x12, entityTypeCreature, "Ghost"))
	feed(gs, 0x1C, entityState(1, 0x13, entityTypeCreature, "Ghost"))
	if gs.Entities[0x12] != nil || gs.Entities[0x13] != nil {
		t.Fatal("a channel past 0x17 made an entity")
	}
}

func TestSelfIsTheMemoryDumpsSource(t *testing.T) {
	gs := NewGameState("test", 10070, 9735, 7000, "test")

	// Before the MemoryDump, m_selfId is -1 and every entity is dropped.
	feed(gs, 4, entityState(1, 0x20, entityTypeCreature, "Early"))
	if len(gs.Entities) != 0 {
		t.Fatal("an entity before the MemoryDump")
	}

	gs.noteSelf(&protocol.Message{Src: 0x1C6AFEAC})
	if !gs.selfKnown || gs.PlayerObjectID != 0x1C6AFEAC {
		t.Fatalf("self 0x%08X known %v", gs.PlayerObjectID, gs.selfKnown)
	}

	// Self is found by id on any channel, and is the player, not an entity.
	self := entityState(1, 0x1C6AFEAC, entityTypeCreature, "Mcgregor")
	binary.LittleEndian.PutUint32(self[28:], 0x8150E6AF) // a form change
	feed(gs, 9, self)
	if gs.Entities[0x1C6AFEAC] != nil || gs.Player.ModelID != 0x8150E6AF {
		t.Fatalf("self on channel 9: entity %v, model 0x%08X", gs.Entities[0x1C6AFEAC] != nil, gs.Player.ModelID)
	}
}

func TestCreatureFieldsAsCLIENTReadsThem(t *testing.T) {
	gs := routingState(t)
	m := entityState(1, 0x30, entityTypeCreature, "Merchant Nelly")
	m[40] = 30    // velocity x
	m[145] = 0    // +0x259
	m[146] = 0x81 // m_npcFlags low: merchant (1) and not attackable (0x80)
	feed(gs, 6, m)
	feed(gs, 6, m) // a first appearance starts at rest; the second reads the velocity
	e := gs.Entities[0x30]
	if want := float32(30) * 21 / 128; e.VelX != want {
		t.Fatalf("velocity %v, want %v (21/128 a step)", e.VelX, want)
	}
	if e.NPCFlags != 0x0081 || e.NPCType != 0x8100 || e.NPCFlags&1 == 0 {
		t.Fatalf("NPCFlags 0x%04X NPCType 0x%04X", e.NPCFlags, e.NPCType)
	}
}

func TestStatChannelIsTwoBlocks(t *testing.T) {
	gs := routingState(t)
	b := make([]byte, 233)
	for i := 0; i < 29; i++ {
		binary.LittleEndian.PutUint32(b[4*i:], uint32(i*10+1))
		binary.LittleEndian.PutUint32(b[116+4*i:], uint32(i*100+1))
	}
	b[232] = 0x7F
	feed(gs, 0x42, b)
	p := gs.Player
	if gs.PlayerStats[7] != 71 || p.HPInt != 71 || p.MaxHPInt != 81 || p.PowerInt != 91 || p.MaxPowerInt != 101 {
		t.Fatalf("HP %d/%d power %d/%d (want index 7..10)", p.HPInt, p.MaxHPInt, p.PowerInt, p.MaxPowerInt)
	}
	if p.Str != 1 || p.Cha != 61 || p.BaseStr != 1 || p.BaseCha != 601 || gs.PlayerBaseStats[8] != 801 {
		t.Fatalf("stats %d..%d base %d..%d", p.Str, p.Cha, p.BaseStr, p.BaseCha)
	}
	if gs.PlayerStatTail != 0x7F {
		t.Fatalf("tail %#x", gs.PlayerStatTail)
	}

	// A short message changes what it holds and nothing after.
	short := make([]byte, 12)
	binary.LittleEndian.PutUint32(short[0:], 5)
	feed(gs, 0x42, short)
	if gs.PlayerStats[0] != 5 || gs.PlayerStats[1] != 0 || gs.PlayerStats[7] != 71 || gs.PlayerBaseStats[0] != 1 {
		t.Fatalf("short message: %v / %v", gs.PlayerStats[:8], gs.PlayerBaseStats[:2])
	}
}

func effect(icon uint32, name string) []byte {
	r := make([]byte, 132)
	binary.LittleEndian.PutUint32(r, icon)
	u := utf16.Encode([]rune(name))
	binary.LittleEndian.PutUint32(r[4:], uint32(len(u)))
	for i, c := range u {
		if 8+2*i+2 <= len(r) {
			binary.LittleEndian.PutUint16(r[8+2*i:], c)
		}
	}
	return r
}

func TestEffectsAre132ByteRecords(t *testing.T) {
	gs := routingState(t)
	msg := func(count int32, recs ...[]byte) []byte {
		b := make([]byte, 4)
		binary.LittleEndian.PutUint32(b, uint32(count))
		for _, r := range recs {
			b = append(b, r...)
		}
		return b
	}

	// pcaps/ps2_sandstorm.pcap's one effect, and a second record after it.
	feed(gs, 0x43, msg(2, effect(0xDE1BC5EC, "Gypsy WereHunter: Rat"), effect(0x1234, "Haste")))
	if len(gs.Buffs) != 2 || gs.Buffs[0].Name != "Gypsy WereHunter: Rat" ||
		uint32(gs.Buffs[0].SpellHash) != 0xDE1BC5EC || gs.Buffs[1].Name != "Haste" {
		t.Fatalf("buffs %+v", gs.Buffs)
	}

	// A name of 32 characters or more is skipped and shown empty.
	long := "An effect name that is far too long"
	feed(gs, 0x43, msg(1, effect(7, long)))
	if len(gs.Buffs) != 1 || gs.Buffs[0].Name != "" || gs.Buffs[0].SpellHash != 7 {
		t.Fatalf("long name: %+v", gs.Buffs)
	}

	// A count over 8 is 8, and records the message does not have keep
	// their old values; a negative count shows none.
	feed(gs, 0x43, msg(9, effect(1, "One")))
	if len(gs.Buffs) != 8 || gs.Buffs[0].Name != "One" || gs.Buffs[1].Name != "Haste" {
		t.Fatalf("count 9: %+v", gs.Buffs)
	}
	feed(gs, 0x43, msg(-1))
	if len(gs.Buffs) != 0 {
		t.Fatalf("negative count: %+v", gs.Buffs)
	}
}

func TestGroupChannel(t *testing.T) {
	gs := routingState(t)
	rec := func(id uint32, known, hp byte, x int8, px, py, pz int16) []byte {
		r := make([]byte, 13)
		binary.LittleEndian.PutUint32(r, id)
		r[4], r[5], r[6] = known, hp, byte(x)
		binary.LittleEndian.PutUint16(r[7:], uint16(px))
		binary.LittleEndian.PutUint16(r[9:], uint16(py))
		binary.LittleEndian.PutUint16(r[11:], uint16(pz))
		return r
	}
	feed(gs, 0x40, append(rec(5, 1, 128, -2, 25294, 54, -15786), rec(6, 0, 0, 0, 1, 2, 3)...))
	if s := gs.GroupStatus[5]; s.HP != 128 || s.HPKnown != 1 || s.P0C != -2 || s.X != 25294 || s.Z != -15786 {
		t.Fatalf("member 5: %+v", s)
	}
	feed(gs, 0x40, rec(6, 1, 9, 0, 1, 2, 3))
	if _, ok := gs.GroupStatus[5]; ok || gs.GroupStatus[6].HP != 9 {
		t.Fatalf("a member the message leaves out is inactive: %+v", gs.GroupStatus)
	}
}

func TestChannelRecords(t *testing.T) {
	gs := routingState(t)
	b := make([]byte, 157)
	binary.LittleEndian.PutUint64(b, 0x0102030405060708)
	binary.LittleEndian.PutUint32(b[8:], 11)
	binary.LittleEndian.PutUint32(b[24:], 55)
	b[28] = 9
	for i, c := range utf16.Encode([]rune("Grimefod")) {
		binary.LittleEndian.PutUint16(b[29+2*i:], c)
	}
	feed(gs, 0x45, b)
	r := gs.ChannelRecs[1]
	if r.Size != 157 || r.ID != 0x0102030405060708 || r.P08 != 11 || r.P18 != 55 || r.P1E != 9 || r.Name != "Grimefod" {
		t.Fatalf("record %+v", r)
	}
	feed(gs, 0x45, nil)
	if gs.ChannelRecs[1].Size != 0 || gs.ChannelRecs[1].Name != "Grimefod" {
		t.Fatalf("an empty message sets the size alone: %+v", gs.ChannelRecs[1])
	}
}

// shardList is a 0x07B3 the way VIShardInfo::Read takes it: the opcode, a
// zigzag count, then per entry a wide name, recommended, the 8-byte id and the
// language.
func shardList(rawCount byte, entries ...[2]interface{}) []byte {
	b := []byte{0xB3, 0x07, rawCount}
	for _, e := range entries {
		u := utf16.Encode([]rune(e[0].(string)))
		b = binary.LittleEndian.AppendUint32(b, uint32(len(u)))
		for _, c := range u {
			b = binary.LittleEndian.AppendUint16(b, c)
		}
		b = append(b, 0)                // recommended
		b = append(b, 0x91, 0xB9)       // endpoint 0xB991
		b = append(b, 0x56, 0x27)       // port 10070
		b = append(b, 188, 217, 161, 5) // 5.161.217.188, reversed
		b = append(b, byte(e[1].(int)))
	}
	return b
}

func TestShardListAsVIShardInfoReadsIt(t *testing.T) {
	gs := NewGameState("test", 10070, 9735, 7000, "test")
	gs.Phase = PhaseAuth
	fc := func(data []byte) *protocol.Message {
		m := &protocol.Message{Type: protocol.MessageTypeUnreliable}
		m.SetData(data)
		return m
	}

	// Sandstorm's: raw count 4 is two entries, and of those only language
	// 0's is an English client's.
	gs.dispatchIncoming(fc(shardList(4,
		[2]interface{}{"Sandstorm", 0}, [2]interface{}{"Sandstorm", 1},
		[2]interface{}{"Sandstorm", 2}, [2]interface{}{"Sandstorm", 3})))
	if len(gs.Servers) != 1 || gs.Servers[0].Name != "Sandstorm" || gs.Servers[0].Port != 10070 ||
		gs.Servers[0].EndpointID != 0xB991 || gs.Servers[0].IP != "5.161.217.188" || gs.Phase != PhaseServerList {
		t.Fatalf("servers %+v phase %v", gs.Servers, gs.Phase)
	}

	// A name of 32 characters or more is skipped, and the entry kept.
	gs.dispatchIncoming(fc(shardList(2, [2]interface{}{"A server name of thirty-two chars", 0})))
	if len(gs.Servers) != 1 || gs.Servers[0].Name != "" || gs.Servers[0].Port != 10070 {
		t.Fatalf("long name: %+v", gs.Servers)
	}

	// Once a shard is being joined, a list is not read.
	gs.Phase = PhaseInWorld
	gs.dispatchIncoming(fc(shardList(4, [2]interface{}{"Other", 0}, [2]interface{}{"Other", 0})))
	if gs.Phase != PhaseInWorld || gs.Servers[0].Name != "" {
		t.Fatalf("a list in world was read: %+v phase %v", gs.Servers, gs.Phase)
	}

	// The guaranteed dispatcher has no 0x07B3 case.
	gs.Phase = PhaseAuth
	fb := fc(shardList(2, [2]interface{}{"Guaranteed", 0}))
	fb.Type = protocol.MessageTypeReliable
	gs.dispatchIncoming(fb)
	if gs.Servers[0].Name == "Guaranteed" {
		t.Fatal("0x07B3 read off a guaranteed message")
	}
}

func TestPumpRoutesByType(t *testing.T) {
	gs := routingState(t)
	feed(gs, 2, entityState(1, 0x40, entityTypeCreature, "Bat"))

	// Best effort carries only the shard list: a chat line sent that way
	// is dropped.
	chat := &protocol.Message{Type: protocol.MessageTypeUnreliable}
	chat.SetData([]byte{0x7A, 0x0A, 2, 0, 0, 0, 'h', 0, 'i', 0})
	gs.dispatchIncoming(chat)
	if len(gs.ChatLog) != 0 {
		t.Fatalf("a best-effort 0x0A7A was read: %+v", gs.ChatLog)
	}

	// The system type is read and kept nowhere.
	ka := &protocol.Message{Type: protocol.MessageTypePing}
	ka.SetData([]byte{0x12})
	gs.dispatchIncoming(ka)

	// A new connection the server opened resets the world.
	gs.dispatchIncoming(&protocol.Message{Type: protocol.MessageTypeConnect})
	if len(gs.Entities) != 0 || gs.selfKnown {
		t.Fatalf("0xFF left %d entities, self known %v", len(gs.Entities), gs.selfKnown)
	}

	// The terminate byte becomes the client's own reason.
	for _, c := range []struct{ b, reason int }{{1, 1}, {14, 16}, {15, 15}, {6, 10}, {3, 10}} {
		gs.Phase = PhaseInWorld
		gs.Session.Disconnect = DisconnectState{} // each case is a fresh session
		fd := &protocol.Message{Type: protocol.MessageTypeDisconnect}
		fd.SetData([]byte{byte(c.b)})
		gs.dispatchIncoming(fd)
		if gs.Phase != PhaseDisconnected || gs.DisconnectReason != c.reason || gs.DRDPReason != c.b {
			t.Fatalf("0xFD %d: phase %v reason %d", c.b, gs.Phase, gs.DisconnectReason)
		}
	}

	// After a disconnect the pump reads nothing more (m_drdp is gone).
	gs.Phase = PhaseInWorld
	again := &protocol.Message{Type: protocol.MessageTypeDisconnect}
	again.SetData([]byte{1})
	gs.dispatchIncoming(again)
	if gs.Phase != PhaseInWorld {
		t.Fatalf("a message after the disconnect was dispatched")
	}
}
