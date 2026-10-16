package game

// intake_routing.go — the DRDP receive loop's own cases and the state channels,
// as CLIENT runs them: VIClient::PumpMessages (0x00BD4C10), ResetWorld
// (0x00BD0560), ProcessMessageFC (0x00BD3AF8), ProcessStateChannel
// (0x00BD3C18), Channel40/43/44 and ChannelEntity (0x00BD41B8). The decomp is
// decomp/modules/client/src/viclient.cpp; ChannelEntity and the entity readers
// it calls (VIEntity.cpp, VICreatureEntity.cpp) are read off the disassembly.

import (
	"encoding/binary"
	"fmt"
	"log"
	"unicode/utf16"

	"github.com/eqoa/client/internal/protocol"
)

// RoutingState is what the pump and the state channels keep.
type RoutingState struct {
	// selfKnown is CLIENT's m_selfId != -1. Msg000D (the MemoryDump) sets
	// m_selfId to the message's src and ResetWorld (a new connection) to -1;
	// ChannelEntity drops every entity message while it is -1.
	selfKnown bool

	// entitySlots are CLIENT's m_entities[24]: state channels 0x00..0x17,
	// one entity each, whose type and id decide what the next message does.
	entitySlots [24]entitySlot

	// GroupStatus is state channel 0x40 (Channel40) by group member id: each
	// message first marks every member inactive and then updates the ones it
	// lists, so a member absent from the last message is inactive.
	GroupStatus map[uint32]GroupMemberStatus

	// ChannelRecs are state channels 0x44..0x6B (Channel44), a record each.
	ChannelRecs [40]ChannelRec

	// PlayerStats, PlayerBaseStats and PlayerStatTail are the stat channel
	// (0x42) as ReadStats reads it: VIPlayer's m_stat[29], m_baseStat[29] and
	// the byte m_p30. Index 7 is HP (GetHP), 8 max HP, 9 power, 10 max power;
	// a base and a total share the index (GetStatTotal adds the equipped
	// items' stat id N to m_baseStat[N]).
	PlayerStats     [29]int32
	PlayerBaseStats [29]int32
	PlayerStatTail  byte

	// The active effects, channel 0x43 (Channel43): m_effectCount (at most 8),
	// m_effectIcon[8] and m_effectName[8]. Buffs is built from them.
	effectCount int32
	effectIcon  [8]uint32
	effectName  [8]string

	// DisconnectReason is CLIENT's m_p4F0E8 after the connection ended
	// (0xFD); DRDPReason is the terminate byte it came from (m_p4F0EC).
	DisconnectReason int
	DRDPReason       int
}

// entitySlot is one of m_entities[24].
type entitySlot struct {
	used bool
	id   uint32
	kind byte
}

// The two entity types VIEntity.cpp's factory (0x00CC5098) makes. Any other
// type byte makes it assert "pEntity != NULL" and then write through the
// NULL it returned.
const (
	entityTypePlain    = 0x81 // VIEntity, 88 bytes: the base read only
	entityTypeCreature = 0x82 // VICreatureEntity, 704 bytes
	// entityBaseSize is what VIEntity's reader takes, the 6-byte header
	// included: position, heading, pitch, world, a u64, two bytes, the
	// model, a u32 and the scale. The creature's reader goes on to 162.
	entityBaseSize = 40
)

// GroupMemberStatus is one record of state channel 0x40.
type GroupMemberStatus struct {
	HPKnown byte    // VIPartyMember +0x09
	HP      byte    // +0x08
	P0C     int32   // +0x0C: a signed byte on the wire, meaning unknown
	X, Y, Z float32 // +0x10: three int16s, unscaled
}

// ChannelRec is one record of state channels 0x44..0x6B (VIChannelRec,
// 160 bytes; game.h). Size 0 is an empty one. What CLIENT shows from them is
// not decompiled yet.
type ChannelRec struct {
	Size                    int
	ID                      uint64
	P08, P0C, P10, P14, P18 int32
	P1E                     byte
	Name                    string // 64 UTF-16 code units, raw
}

// noteSelf is Msg000D's m_selfId = (int)src: the MemoryDump says whose
// entity is the player's own.
func (gs *GameState) noteSelf(msg *protocol.Message) {
	if msg.Src != 0 {
		gs.PlayerObjectID = msg.Src
	}
	gs.selfKnown = true
}

// resetWorld is ResetWorld, which the pump runs on a connection the server
// opened (0xFF): every entity goes, the 0x44..0x6B records are zeroed and the
// self id is forgotten until the next MemoryDump. CLIENT also resets the
// guild, the buddy list and two sub-objects (+0x382E8, +0x38300) there; those
// belong to their own handlers here.
func (gs *GameState) resetWorld() {
	for ch := range gs.entitySlots {
		gs.entitySlots[ch] = entitySlot{}
	}
	for id := range gs.Entities {
		gs.removeEntity(id)
	}
	gs.ChannelEntity = [29]uint32{}
	gs.ChannelRecs = [40]ChannelRec{}
	gs.selfKnown = false
	log.Printf("Connect: a session the server opened (0xFF), world reset")
}

// handleConnectionless is ProcessMessageF8 (0x00BD0D00): only 0x0993, the
// echo of a 0x0992 the client sent to the address 0x0790 named, which it
// answers with 0x0791 if the token is the one 0x0790 gave. The Go connection
// receives no connectionless packets and sends no 0x0992, so nothing reaches
// here; the case keeps the pump whole.
func (gs *GameState) handleConnectionless(msg *protocol.Message) {
	if d := msg.Data(); len(d) >= 2 {
		log.Printf("Connectionless 0x%04X (%d bytes): not answered", binary.LittleEndian.Uint16(d), len(d))
	}
}

// handleBestEffort is ProcessMessageFC: a best-effort (0xFC) message is read
// only if it is the shard list, 0x07B3. Everything else sent that way is
// dropped.
func (gs *GameState) handleBestEffort(msg *protocol.Message) {
	d := msg.Data()
	if len(d) < 2 {
		return
	}
	if op := protocol.GameOpcode(binary.LittleEndian.Uint16(d)); op == protocol.OpcodeGameServers {
		gs.handleGameServers(msg)
	} else {
		log.Printf("Best-effort 0x%04X (%d bytes): CLIENT reads only 0x07B3 this way, dropped", uint16(op), len(d))
	}
}

// handleDRDPDisconnect is the pump's 0xFD: the connection has ended, and the
// reason byte the DRDP library terminated it with is mapped to the client's
// own reason (1 -> 1, 14 -> 16, 15 -> 15, anything else -> 10) for
// Disconnect. Byte 1 is an ICMP unreachable, 14 a peer on another protocol
// version, 15 an empty segment (the route closed), 2/3 the warn and fail
// timeouts, 6 a reset from the far end.
func (gs *GameState) handleDRDPDisconnect(msg *protocol.Message) {
	var b byte
	if d := msg.Data(); len(d) > 0 {
		b = d[0]
	}
	gs.DRDPReason = int(b)
	reason := 10
	switch b {
	case 1:
		reason = 1
	case 14:
		reason = 16
	case 15:
		reason = 15
	}
	log.Printf("Disconnect-11: connection terminated (DRDP reason %d), disconnect reason %d", b, reason)
	gs.DisconnectReason = reason
	// Disconnect (0x00BC1720): the entities go and the game window gives
	// way to the disconnect window, which reads the reason.
	for ch := range gs.entitySlots {
		gs.entitySlots[ch] = entitySlot{}
	}
	for id := range gs.Entities {
		gs.removeEntity(id)
	}
	gs.ChannelEntity = [29]uint32{}
	gs.Phase = PhaseDisconnected
	gs.StatusMsg = fmt.Sprintf("Disconnected from the server (reason %d)", reason)
}

// channelEntity is ChannelEntity: state channels 0x00..0x17.
//
// The header is 6 bytes: a flag, the entity id and its type. A message under
// 5 bytes empties the slot. Otherwise the slot keeps its entity only when the
// type and id match and the flag is set; when they do not, the old entity is
// deleted, and a new one is made only if the flag is set. An empty slot makes
// one whatever the flag. Sandstorm sends the flag 0 in a channel's first
// message and 1 after it, and 0 again for the entity a world change takes
// away. The player's own entity is the one whose id is the self id.
func (gs *GameState) channelEntity(ch byte, data []byte) {
	if !gs.selfKnown {
		return
	}
	slot := &gs.entitySlots[ch]
	if len(data) < 5 {
		gs.releaseEntitySlot(ch)
		return
	}
	keep := data[0]
	id := binary.LittleEndian.Uint32(data[1:5])
	var kind byte // a 5-byte message's type read fails; CLIENT then uses whatever byte its stack held
	if len(data) >= 6 {
		kind = data[5]
	}
	created := false
	if slot.used && slot.kind == kind && slot.id == id && keep != 0 {
		// the same entity: read into it
	} else {
		if slot.used {
			gs.releaseEntitySlot(ch)
			if keep == 0 {
				return
			}
		}
		if kind != entityTypePlain && kind != entityTypeCreature {
			log.Printf("Entity ch=%d id=0x%08X: type 0x%02X is neither 0x81 nor 0x82 (the PS2 asserts here)", ch, id, kind)
			return
		}
		*slot = entitySlot{used: true, id: id, kind: kind}
		created = true
	}
	if kind == entityTypePlain && len(data) > entityBaseSize {
		data = data[:entityBaseSize]
	}
	if id == gs.PlayerObjectID {
		// The PS2 then puts its own walk control's position and heading
		// back over the ones just read; decodePlayerState keeps ours.
		gs.decodePlayerState(data)
		return
	}
	gs.decodeEntityState(ch, data, created)
}

// releaseEntitySlot deletes slot ch's entity.
func (gs *GameState) releaseEntitySlot(ch byte) {
	slot := &gs.entitySlots[ch]
	if !slot.used {
		return
	}
	if slot.id != gs.PlayerObjectID {
		if ent, ok := gs.Entities[slot.id]; ok && ent.Channel == int(ch) {
			log.Printf("Entity removed: ch=%d %q objID=0x%08X", ch, ent.Name, slot.id)
			gs.removeEntity(slot.id)
		}
	}
	if int(ch) < len(gs.ChannelEntity) {
		gs.ChannelEntity[ch] = 0
	}
	*slot = entitySlot{}
}

// decodeGroupPositions is Channel40 (0x00BD3FA0): 13-byte records of a
// member id, an HP-known byte, the HP byte, a signed byte and three int16
// coordinates, read until the message ends.
func (gs *GameState) decodeGroupPositions(data []byte) {
	status := make(map[uint32]GroupMemberStatus)
	for p := 0; p+13 <= len(data); p += 13 {
		r := data[p:]
		status[binary.LittleEndian.Uint32(r)] = GroupMemberStatus{
			HPKnown: r[4],
			HP:      r[5],
			P0C:     int32(int8(r[6])),
			X:       float32(int16(binary.LittleEndian.Uint16(r[7:]))),
			Y:       float32(int16(binary.LittleEndian.Uint16(r[9:]))),
			Z:       float32(int16(binary.LittleEndian.Uint16(r[11:]))),
		}
	}
	gs.GroupStatus = status
}

// decodeChannelRec is Channel44 (0x00BD3E40): the size is kept whatever it
// is, and a record that has bytes is read raw, field by field, in the order
// they sit; a short one stops where it runs out, leaving the rest as it was.
func (gs *GameState) decodeChannelRec(ch byte, data []byte) {
	rec := &gs.ChannelRecs[ch-0x44]
	rec.Size = len(data)
	if len(data) == 0 {
		return
	}
	p := 0
	take := func(n int) []byte {
		if p+n > len(data) {
			p = len(data) + 1
			return nil
		}
		b := data[p : p+n]
		p += n
		return b
	}
	if b := take(8); b != nil {
		rec.ID = binary.LittleEndian.Uint64(b)
	}
	for _, f := range []*int32{&rec.P08, &rec.P0C, &rec.P10, &rec.P14, &rec.P18} {
		if b := take(4); b != nil {
			*f = int32(binary.LittleEndian.Uint32(b))
		}
	}
	if b := take(1); b != nil {
		rec.P1E = b[0]
	}
	if b := take(128); b != nil {
		u := make([]uint16, 0, 64)
		for i := 0; i < 64; i++ {
			c := binary.LittleEndian.Uint16(b[2*i:])
			if c == 0 {
				break
			}
			u = append(u, c)
		}
		rec.Name = string(utf16.Decode(u))
	}
}
