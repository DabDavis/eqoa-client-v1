package game

// handlers.go — Server→Client opcode handlers for all opcodes identified from
// PS2 CLIENT.AUT decompilation (VIClient.cpp mega-function at 0x00BD0DC8).
// Covers entity lifecycle, quest system, world entry/zone transfer, NPC
// interaction, item streaming, chat, guild, and social.
//
// The "Disconnect-N" printfs in CLIENT are the server-ordered disconnects
// (intake_session.go, disconnectReasons): -5 0x0729, -6 0x072B, -7 0x0728,
// -8 0x072A, -9 0x00EC/0x07A4, -11 a DRDP 0xFD, -12 0x0777, -?? a 0x0000
// version other than 18. None of them is a malformed-data report.

import (
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"time"

	"github.com/eqoa/client/internal/protocol"
	"github.com/DabDavis/eqoa-esf-tools/pkg/buffer"
)

// =============================================================================
// The ability messages (0x00B3/B5/B8/FD/FF) and 0x00FC
// =============================================================================
//
// The old notes read these as entity messages. CLIENT's handlers for all five
// edit the ability map instead (abilities.go); the Go names stay until the
// dispatch is renamed.

// handleFullEntityCreate is 0x00B3, one ability added (handleAbilityAdd).
func (gs *GameState) handleFullEntityCreate(msg *protocol.Message) {
	gs.handleAbilityAdd(msg)
}

// handleEntityUpdate is 0x00B5, a bar entry moved (handleAbilitySlotMove).
func (gs *GameState) handleEntityUpdate(msg *protocol.Message) {
	gs.handleAbilitySlotMove(msg)
}

// handleEntityDetailUpdate is 0x00B8, an ability erased (handleAbilityRemove).
func (gs *GameState) handleEntityDetailUpdate(msg *protocol.Message) {
	gs.handleAbilityRemove(msg)
}

// handleEntityStateRefresh is 0x00FC: one byte, stored whole in
// VIClient+0x6DC8, which zeroes the walker's forward speed while it is set.
func (gs *GameState) handleEntityStateRefresh(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 3 {
		return
	}
	gs.MoveLock = int(data[2])
	log.Printf("MoveLock: %d", gs.MoveLock)
}

// handleEntityDataUpdate is 0x00FD, an ability's +0x14 by id (handleAbilitySetP14).
func (gs *GameState) handleEntityDataUpdate(msg *protocol.Message) {
	gs.handleAbilitySetP14(msg)
}

// handleFullEntityRefresh is 0x00FF, every ability off the bar
// (handleAbilityInvalidateAll).
func (gs *GameState) handleFullEntityRefresh(msg *protocol.Message) {
	gs.handleAbilityInvalidateAll(msg)
}

// =============================================================================
// Character-select messages and disconnects (0x07F0..0x07F4, 0x07A1/A2, 0x07C0)
// =============================================================================
//
// The old "entity update cascade" reading of these was Ghidra's fall-through
// misread (claude-notes/decomp-phase62-dispatcher-survey-20261015.md §2): none
// carries an entity. 0x07F0..0x07F4 are VIClient::CharSelectMsg, and 0x07A1,
// 0x07A2 and 0x07C0 are Disconnect(13), (14) and (17).

// handleEntityUpdateFullUI is 0x07F4 (and 0x07F2): CharSelectMsg(1662), the
// "you are not using a Frontiers disc" message.
func (gs *GameState) handleEntityUpdateFullUI(msg *protocol.Message) {
	gs.handleCharSelectMsg(msg)
}

// handleEntityUpdateMapPanel is 0x07F0 and 0x07F1 (and 0x07F3):
// CharSelectMsg(676), the "character is in an area that is unavailable" one.
func (gs *GameState) handleEntityUpdateMapPanel(msg *protocol.Message) {
	gs.handleCharSelectMsg(msg)
}

// handleEntityUpdateAudio is 0x07A1, VIClient::Msg07A1: eight bytes read and
// dropped, then Disconnect(13, 0).
func (gs *GameState) handleEntityUpdateAudio(msg *protocol.Message) {
	gs.handleServerDisconnect(msg)
}

// handleEntityUpdateMinimal is 0x07A2, VIClient::Msg07A2: Disconnect(14, 0).
func (gs *GameState) handleEntityUpdateMinimal(msg *protocol.Message) {
	gs.handleServerDisconnect(msg)
}

// handleEntityUpdateDirect is 0x07C0: Disconnect(17, 0).
func (gs *GameState) handleEntityUpdateDirect(msg *protocol.Message) {
	gs.handleServerDisconnect(msg)
}

// =============================================================================
// Quests, the pet, assist, the profanity filter, training points
// (0x0045/7B/7D/7F/0036). The old reading of these as one "quest chain" that
// fell through from case to case is refuted: each is its own short case.
// =============================================================================

// handleQuestDialogueResp is 0x0045 (Msg0045, 0x00BDB820): the profanity
// filter's state, a zigzag, shown as "Profanity Filter: On" / "Off".
func (gs *GameState) handleQuestDialogueResp(msg *protocol.Message) {
	s := newSocialStream(msg)
	on := s.zigzag()
	if s.err {
		return
	}
	gs.ProfanityFilter = on != 0
	text := gs.socialWord(550) + ": "
	if on != 0 {
		text += gs.socialWord(551)
	} else {
		text += gs.socialWord(552)
	}
	gs.socialShowText(text)
}

// handleUpdateQuestProgress is 0x007B (Msg007B): the target an assist found,
// a u32. 0xFFFFFFFF is "There is no target for assisting." (string 838, a
// notice box on the PS2). Any other id becomes the target and the PS2 sends
// 0x0010 for it; here it waits in AssistTarget for the client to take.
func (gs *GameState) handleUpdateQuestProgress(msg *protocol.Message) {
	s := newSocialStream(msg)
	id := s.u32()
	if s.err {
		return
	}
	if id == 0xFFFFFFFF {
		gs.socialShowWord(838)
		return
	}
	gs.AssistTarget = id
	gs.AssistTargetPending = true
	log.Printf("Assist: target 0x%08X", id)
}

// handleDeleteQuest is 0x007D (Msg007D): a u32 (VIClient+0x2BB0C), then a
// zigzag index into the quest log. Below 8, the titles above it move down
// one and the log is one shorter. (The PS2 checks the index against 8 only,
// not against the count; the count is kept from going negative here.)
func (gs *GameState) handleDeleteQuest(msg *protocol.Message) {
	s := newSocialStream(msg)
	stamp := s.u32()
	i := int(s.zigzag())
	if s.err {
		return
	}
	gs.QuestLogStamp = stamp
	if i >= 8 {
		return
	}
	if i >= 0 && i < len(gs.QuestTitles) {
		gs.QuestTitles = append(gs.QuestTitles[:i], gs.QuestTitles[i+1:]...)
	} else if len(gs.QuestTitles) > 0 {
		gs.QuestTitles = gs.QuestTitles[:len(gs.QuestTitles)-1]
	}
	log.Printf("DeleteQuest: index %d, %d in the log", i, len(gs.QuestTitles))
}

// handleClearQuestTracker is 0x007F: no pet (VIClient+0x2BE50 = 0xFFFFFFFF,
// its name cleared). Not a quest message; 0x007E sets the pet.
func (gs *GameState) handleClearQuestTracker(msg *protocol.Message) {
	gs.PetID = 0xFFFFFFFF
	log.Printf("Pet: none")
}

// handleQuestStageComplete is 0x0036: training points. A byte (a sound
// when set), then the new count of unspent training points (zigzag, the
// player record's +0x40, which 0x001D lowers when they are spent). When the
// count changes the PS2 shows "You have received more training points!" as
// a dialog. It grants no experience.
func (gs *GameState) handleQuestStageComplete(msg *protocol.Message) {
	s := newSocialStream(msg)
	flag := s.u8()
	tp := int64(s.zigzag())
	if s.err {
		return
	}
	old := gs.TrainingPoints
	gs.TrainingPoints = tp
	log.Printf("TrainingPoints: %d -> %d (flag %d)", old, tp, flag)
	if tp != old {
		gs.socialShowDialog(0, gs.socialWord(625))
	}
}

// =============================================================================
// World Entry / Zone Transfer / Session (0x0728/0758/002E/0579/0790/07B0/07B2)
// =============================================================================

// handleServerLoginResponse is 0x0728, which is printf "Disconnect-7" and
// Disconnect(6, 0): "Server not ready, please try again later. (nodbs)".
func (gs *GameState) handleServerLoginResponse(msg *protocol.Message) {
	gs.handleServerDisconnect(msg)
}

// handleReconnectWorldEntry is 0x0758: ReadString(buf, 512) — an 8-bit
// string — into the game window's text (VIWndGame::AddText(const char *)).
func (gs *GameState) handleReconnectWorldEntry(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 2 {
		return
	}
	text := psString(buffer.NewReader(data[2:]), 512)
	log.Printf("Text (0x0758): %q", text)
	if text != "" {
		gs.addChatMessage(text, ChatSystem)
	}
}

// handleWorldEntry is 0x002E, which is not a world entry: the login window
// shows word 269, "Character create failed", and m_charCount comes down.
func (gs *GameState) handleWorldEntry(msg *protocol.Message) {
	gs.createFailed(269)
}

// handleGuildWorldEntry is 0x0579 (VIClientPart2EF24::Recv0579, 0x00BEF788):
// why a guild command failed. Not a world entry. Two bytes, the command and
// the reason; a reason of 0 says nothing.
//
//	1 create:  563, then 565 / 566 / 567 (the Tunar it costs) / 568 for 1/2/6/7
//	2 disband: 564, then 546 / 570 for 5/8
//	3-7, 11, 12: 546 / 570 / 584 / 745 for 5/8/11/12
func (gs *GameState) handleGuildWorldEntry(msg *protocol.Message) {
	s := newSocialStream(msg)
	cmd := s.u8()
	why := s.u8()
	if s.err || why == 0 {
		return
	}
	log.Printf("GuildCommand: command %d failed, reason %d", cmd, why)
	switch cmd {
	case 1:
		gs.socialShowWord(563)
		switch why {
		case 1:
			gs.socialShowWord(565)
		case 2:
			gs.socialShowWord(566)
		case 6:
			gs.socialShowText(socialFormat(gs.socialWord(567), guildCreateCost))
		case 7:
			gs.socialShowWord(568)
		}
	case 2:
		gs.socialShowWord(564)
		switch why {
		case 5:
			gs.socialShowWord(546)
		case 8:
			gs.socialShowWord(570)
		}
	case 3, 4, 5, 6, 7, 11, 12:
		if w, ok := map[byte]int{5: 546, 8: 570, 11: 584, 12: 745}[why]; ok {
			gs.socialShowWord(w)
		}
	}
}

// handleStatsUpdate is 0x0790, VIClient::Msg0790, which is not experience: a
// u64 DRDP endpoint id (an address), a u32 token and a u64 value
// (SessionIntake.Probe), then Msg0790Done. CLIENT sends 0x0992 {token,
// value} to that address from then on (each net tick, at most once per
// min(connection timeout, 1,000 ms)); the Go client cannot send to a second
// address and does not.
func (gs *GameState) handleStatsUpdate(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 22 {
		log.Printf("Probe (0x0790): short (%d bytes)", len(data))
		return
	}
	p := data[2:]
	gs.Session.Probe = ProbeState{
		Set:   true,
		Addr:  binary.LittleEndian.Uint64(p[0:8]),
		Token: binary.LittleEndian.Uint32(p[8:12]),
		Value: binary.LittleEndian.Uint64(p[12:20]),
	}
	a := gs.Session.Probe.Addr
	log.Printf("Probe (0x0790): %d.%d.%d.%d:%d drdp %d, token 0x%08X, value 0x%X",
		byte(a>>56), byte(a>>48), byte(a>>40), byte(a>>32), uint16(a>>16), uint16(a),
		gs.Session.Probe.Token, gs.Session.Probe.Value)
}

// handleZoneTransferInit handles opcode 0x07B0, VIClientPart33950::Recv07B0
// (0x00BE71B0), whose sub-handlers are not decompiled: the labels below are
// the old Ghidra reading, unverified, and nothing reads the state they set.
// PS2 FUN_00BE71B0: reads 1-byte sub-type, dispatches to state machine:
//   0=init (guard state==3), 1=ready (guard state==5),
//   2=zone data (guard state==1), 3=zone resource (guard state==2),
//   4=complete (guard state==4), 5=abort, 6=extra handler
func (gs *GameState) handleZoneTransferInit(msg *protocol.Message) {
	data := msg.Data()
	payload := data[2:] // skip opcode
	if len(payload) < 1 {
		return
	}
	subType := payload[0]
	rest := payload[1:]

	switch subType {
	case 0: // Init — server initiates zone transfer
		log.Printf("ZoneTransfer: sub=0 INIT (dataLen=%d)", len(rest))
		gs.ZoneTransferActive = true
		gs.ZoneTransferState = 3 // ready for next phase
		if len(rest) >= 1 {
			result := rest[0]
			log.Printf("ZoneTransfer: init result=%d", result)
			// The result byte (what it selects is not decompiled).
			gs.PendingZoneIdx = int(result)
		}

	case 1: // Ready — server confirms zone is available
		log.Printf("ZoneTransfer: sub=1 READY (dataLen=%d)", len(rest))
		if len(rest) >= 1 {
			result := rest[0]
			log.Printf("ZoneTransfer: ready result=%d", result)
			if result == 0 {
				gs.ZoneTransferState = 0 // success → start loading
			}
		}

	case 2: // Zone data — server sends zone proxy list
		log.Printf("ZoneTransfer: sub=2 ZONE_DATA (dataLen=%d)", len(rest))
		if len(rest) >= 5 { // 1 result + 4 count
			result := rest[0]
			count := int(binary.LittleEndian.Uint32(rest[1:5]))
			log.Printf("ZoneTransfer: zone data result=%d count=%d", result, count)
			// Each entry is 24 bytes (zone proxy data)
			if count > 0 && count <= 100 {
				gs.ZoneTransferProxyCount = count
			}
		}

	case 3: // Zone resource — server sends zone ESF data blob
		log.Printf("ZoneTransfer: sub=3 ZONE_RESOURCE (dataLen=%d)", len(rest))
		if len(rest) >= 1 {
			result := rest[0]
			log.Printf("ZoneTransfer: resource result=%d blobLen=%d", result, len(rest)-1)
			// PS2 reads blob(0x4000) into zone resource buffer
			// The blob contains ESF zone data for streaming
		}

	case 4: // Complete — zone transfer finalized
		log.Printf("ZoneTransfer: sub=4 COMPLETE (dataLen=%d)", len(rest))
		gs.ZoneTransferActive = false
		gs.ZoneTransferState = 0
		// PS2 reads 1 byte result + zigzag varint, then calls finalize
		if len(rest) >= 1 {
			result := rest[0]
			log.Printf("ZoneTransfer: complete result=%d", result)
		}

	case 5: // Abort — cancel zone transfer
		log.Printf("ZoneTransfer: sub=5 ABORT")
		gs.ZoneTransferActive = false
		gs.ZoneTransferState = 0

	case 6: // Extra handler
		log.Printf("ZoneTransfer: sub=6 EXTRA (dataLen=%d)", len(rest))

	default:
		log.Printf("ZoneTransfer: unknown sub-type %d (dataLen=%d)", subType, len(rest))
	}
}

// handleZoneTransferConfirm is 0x07B2, which is not 0x07B0's: the dispatcher
// reads one u32 into VIClient+0x7578 (the constructor's 100) and does nothing
// else with it.
func (gs *GameState) handleZoneTransferConfirm(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 6 {
		return
	}
	gs.Session.P07578 = binary.LittleEndian.Uint32(data[2:6])
	log.Printf("0x07B2: +0x7578 = %d", gs.Session.P07578)
}

// parseWorldEntryAppearance tries to parse appearance/equipment from world entry data.
func (gs *GameState) parseWorldEntryAppearance(reader *buffer.Reader) {
	// World entry may contain appearance data similar to MemoryDump
	// Log for now — exact format depends on server implementation
	remaining := reader.Remaining()
	if remaining > 0 {
		log.Printf("WorldEntry: %d bytes of appearance/equipment data", remaining)
	}
}

// =============================================================================
// Item / Loot / NPC (0x001A/001C/0065/00D3/00D9)
// =============================================================================

// handleLootResult handles opcode 0x001A, Msg001A (CLIENT 0x00BD98B0): no
// payload. Everything on the corpse goes into the inventory, record by
// record; a record whose stack did not all fit stays, and the walk moves on.
func (gs *GameState) handleLootResult(msg *protocol.Message) {
	moved := 0
	for i := 0; i < len(gs.Corpse); {
		item := listGet(gs.Corpse, i)
		if item.StackLeft <= 0 {
			// A record holding none: Took(i, 0) takes it off.
			gs.Corpse = append(gs.Corpse[:i], gs.Corpse[i+1:]...)
			continue
		}
		item.EquipLocation = -1
		took := gs.addInventory(&item)
		gs.Corpse, _ = listTook(gs.Corpse, i, took)
		if took != item.StackLeft {
			i++
		}
		moved++
	}
	log.Printf("LootAll: %d records, %d left on the corpse", moved, len(gs.Corpse))
}

// handleNPCInteraction handles opcode 0x001C. CLIENT reads one raw u32 into
// the player record's +0x44 (VIPlayer, +0xC320), which the stat-training
// window adds the CM count to (GetP44Total). That is all it does.
func (gs *GameState) handleNPCInteraction(msg *protocol.Message) {
	d := msg.Data()
	if len(d) < 6 {
		return
	}
	gs.PlayerP44 = binary.LittleEndian.Uint32(d[2:6])
	log.Printf("0x001C: player +0x44 = %d", gs.PlayerP44)
}

// handleItemListUpdate is 0x0065, which carries no items: the login window
// shows word 828, "Failed: invalid name", and m_charCount comes down.
func (gs *GameState) handleItemListUpdate(msg *protocol.Message) {
	gs.createFailed(828)
}

// handleNPCMessage is 0x00D3 (Msg00D3, 0x00BDB0E8): someone asks to summon
// the player (byte 0) or to resurrect them (any other): a name of at most 63
// characters and the byte. The PS2 asks with a yes/no prompt, its text the
// name and string 610 or 611 run together (the separating space is copied
// into a one-character buffer and lost). Kept in Summon; the question is a
// chat line here, there being no prompt to answer it with.
func (gs *GameState) handleNPCMessage(msg *protocol.Message) {
	s := newSocialStream(msg)
	name := s.str8(64)
	c := s.u8()
	if s.err {
		return
	}
	word := 610
	if c != 0 {
		word = 611
	}
	gs.Summon = &SummonRequest{Name: name, Resurrect: c != 0}
	text := name + gs.socialWord(word)
	log.Printf("Summon: %s", text)
	gs.socialShowText(text)
}

// handleHPResourceUpdate is 0x00D9 (the dispatcher's case): one raw u32,
// stored whole in the player record's +0x2C (VIClient+0xC308, Go: ExpDebt).
// It is not HP — that is the stat channel's (0x42). The entity id, zigzag
// amount and buff type this handler used to read are not on the wire.
func (gs *GameState) handleHPResourceUpdate(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 6 {
		return
	}
	gs.ExpDebt = binary.LittleEndian.Uint32(data[2:6])
	log.Printf("PlayerP2C (0x00D9): %d", gs.ExpDebt)
}

// =============================================================================
// Chat (0x07A3)
// =============================================================================

// handleChatMessage is 0x07A3, which carries no chat: ReadString(name, 24)
// widened into VIClient+0x4F0B8, the name GetReplyName returns.
func (gs *GameState) handleChatMessage(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 2 {
		return
	}
	gs.Session.ReplyName = psString(buffer.NewReader(data[2:]), 24)
	log.Printf("ReplyName (0x07A3): %q", gs.Session.ReplyName)
}

// =============================================================================
// Mail, fishing and tradeskills (0x0D41/0x0F02/0x1102)
// =============================================================================

// handleMailDelivery is 0x0D41, VIClientPart2EFD0::Recv0D41 (0x00BF5670): a
// sub-type byte. 1-4 answer a request the PS2 made, and each is read only
// while that request is the one waiting (MailWait), a result byte first:
//
//	1 sent:    1 is "Your mail has been sent." (839); 9-12 fail -1..-4, else -5
//	2 list:    2, then a zigzag count of headers (100 at most); else -6
//	3 message: 3, then the message's id (zigzag) and, when it is the one
//	           asked for, its text (511 characters at most); else -7
//	4 deleted: 13 is "Message deleted." (840); else -8
//
// 5 says new mail is waiting. With no mail window nothing sets MailWait, so
// only 5 does anything here, as on a PS2 that never opened its mail.
func (gs *GameState) handleMailDelivery(msg *protocol.Message) {
	s := newSocialStream(msg)
	sub := s.u8()
	switch sub {
	case 1:
		if gs.MailWait != 3 {
			return
		}
		switch code := s.u8(); {
		case code == 1:
			gs.socialShowWord(839)
			gs.MailWait = 0
		case code >= 9 && code <= 12:
			gs.MailWait = 8 - int32(code)
		default:
			gs.MailWait = -5
		}
	case 2:
		if gs.MailWait != 1 {
			return
		}
		if s.u8() != 2 {
			gs.MailWait = -6
			return
		}
		gs.MailNew = false
		gs.MailWait = 0
		n := int(s.zigzag())
		gs.MailHeaders = gs.MailHeaders[:0]
		for i := 0; i < n && i < 100; i++ {
			var h MailHeader
			h.A, h.B, h.C = s.u32(), s.u32(), s.u32()
			h.Sender = s.str8(24)
			h.Subject = s.str16(128)
			h.Year = uint16(s.u8()) | uint16(s.u8())<<8
			h.Month, h.Day = s.u8(), s.u8()
			h.Hour, h.Minute, h.Second = s.u8(), s.u8(), s.u8()
			h.Read = s.u8() != 0
			if s.err {
				break
			}
			gs.MailHeaders = append(gs.MailHeaders, h)
		}
		log.Printf("Mail: %d headers", len(gs.MailHeaders))
	case 3:
		if gs.MailWait != 2 {
			return
		}
		if s.u8() != 3 {
			gs.MailWait = -7
			return
		}
		gs.MailWait = 0
		id := s.zigzag()
		if id != gs.MailWantID {
			return
		}
		for i := range gs.MailHeaders {
			if int32(gs.MailHeaders[i].A) == id {
				gs.MailHeaders[i].Read = true
			}
		}
		gs.MailBody = s.str16(1024)
	case 4:
		if gs.MailWait != 4 {
			return
		}
		if s.u8() == 13 {
			gs.socialShowWord(840)
			gs.MailWait = 0
		} else {
			gs.MailWait = -8
		}
	case 5:
		gs.MailNew = true
		log.Printf("Mail: new mail waiting")
	}
}

// handleWhoListFiltered is 0x0F02, VIClientPart3FC10::Recv0F02 (0x00BEB900):
// fishing, not a who list. A sub-type byte, 1-10: start ("Let's go fishing!",
// 1431), stop, the catch (an item list, then into the inventory), "You caught
// something!" (1434), "You fail to catch anything" (1435), "The fish got away
// with your bait" (1436), "You damaged your pole" (1437). All but the last go
// to the fishing window, which this client does not have; the last is also a
// chat line.
func (gs *GameState) handleWhoListFiltered(msg *protocol.Message) {
	s := newSocialStream(msg)
	sub := s.u8()
	log.Printf("Fishing: sub-type %d (%d bytes)", sub, len(msg.Data()))
	if sub == 10 {
		gs.socialShowWord(1437)
	}
}

// handleIgnoreListResult is 0x1102, VIClientPart3834C::Recv1102 (0x00BFE118):
// tradeskills, not an ignore list and not the auction. A sub-type byte, 1-16.
// Ported: 1, a recipe scribed (1544); 5, "You have not yet recovered." in
// (1, .25, .25); 12, a combine's result. Each carries the session's u32
// (+0x78C0). 12's results 2 and 3 ("The items you chose do not make anything."
// 1599, "Your tradeskill level is not high enough..." 1598) go to the
// tradeskill window, which this client does not have.
func (gs *GameState) handleIgnoreListResult(msg *protocol.Message) {
	s := newSocialStream(msg)
	sub := s.u8()
	switch sub {
	case 1:
		gs.TradeskillSerial = s.u32()
		gs.socialShowWord(1544)
	case 5:
		gs.TradeskillSerial = s.u32()
		gs.socialLine(gs.socialWord(1567), ChatError, socialRed)
	case 12:
		result := s.u8()
		gs.TradeskillSerial = s.u32()
		log.Printf("Tradeskill: combine result %d", result)
	default:
		log.Printf("Tradeskill: sub-type %d not ported (%d bytes)", sub, len(msg.Data()))
	}
}

// =============================================================================
// Simple / Stub Handlers
// =============================================================================

// handleForceDisconnect is 0x00EC (and 0x07A4, the same case): printf
// "Disconnect-9" and Disconnect(8, 0), "You have logged in from another
// location.", DisconnectKind 3.
func (gs *GameState) handleForceDisconnect(msg *protocol.Message) {
	gs.handleServerDisconnect(msg)
}

// handleStubNOP is 0x006E: Msg006E is an empty function. Nothing follows it.
func (gs *GameState) handleStubNOP(msg *protocol.Message) {
}

// handleEquipItemConfirm handles opcode 0x003F, Msg003F (CLIENT 0x00BCB2F0):
// an inventory slot byte and an equip slot byte. If the item's wear slot fits
// the equip slot (func_00CC61A8), whatever is worn there comes off, then what
// the item displaces (a two-hander both hands, a shield the off hand, ...),
// and the item goes on. The state channel draws the body; this is the list.
func (gs *GameState) handleEquipItemConfirm(msg *protocol.Message) {
	d := msg.Data()
	if len(d) < 4 {
		log.Printf("EquipItem: dataLen=%d", len(d))
		return
	}
	slot, equip := int(d[2]), int64(d[3])
	inv := gs.Inventory()
	if slot >= len(inv) {
		log.Printf("EquipItem: slot %d of %d", slot, len(inv))
		return
	}
	wear := inv[slot].Slot
	if !fitsEquip(wear, equip) {
		log.Printf("EquipItem: %q (wear slot %d) does not fit equip slot %d", inv[slot].Name, wear, equip)
		return
	}
	listUnequip(inv, equip)
	for _, e := range equipDisplaces(wear, equip) {
		listUnequip(inv, e)
	}
	inv[slot].EquipLocation = equip
	log.Printf("EquipItem: %q -> equip slot %d", inv[slot].Name, equip)
}

// handleUnequipItem handles opcode 0x0040, Msg0040 (CLIENT 0x00BCB8A0): an
// equip slot byte; whatever is worn there comes off (VIItemList::Unequip).
func (gs *GameState) handleUnequipItem(msg *protocol.Message) {
	d := msg.Data()
	if len(d) < 3 {
		log.Printf("UnequipItem: dataLen=%d", len(d))
		return
	}
	if !listUnequip(gs.Inventory(), int64(d[2])) {
		log.Printf("UnequipItem: nothing worn in equip slot %d", d[2])
	}
}

// handleArrangeItem handles opcode 0x003D, Msg003D (CLIENT 0x00BCB208): two
// inventory slot bytes. The record at the first moves to the second, the ones
// between shifting towards where it was (VIItemList::Op003D).
func (gs *GameState) handleArrangeItem(msg *protocol.Message) {
	d := msg.Data()
	if len(d) < 4 {
		log.Printf("ArrangeItem: dataLen=%d", len(d))
		return
	}
	inv := gs.invCopy()
	if !listMove(inv, int(d[2]), int(d[3])) {
		log.Printf("ArrangeItem: %d -> %d of %d", d[2], d[3], len(inv))
		return
	}
	gs.setLists(inv, gs.Bank())
}

// handleLootOptions handles opcode 0x0085 (CLIENT's dispatcher, 0x00BD1E88):
// one byte, why the corpse cannot be looted, shown in the text window as word
// 673, 674 or 675. Any other value shows nothing.
func (gs *GameState) handleLootOptions(msg *protocol.Message) {
	d := msg.Data()
	if len(d) < 3 {
		return
	}
	if id := map[byte]int{1: 673, 2: 674, 3: 675}[d[2]]; id != 0 {
		gs.addChatMessage(itemWords[id], ChatSystem)
	}
}

// handleClientCloseLoot handles opcode 0x0016 (CLIENT's dispatcher,
// 0x00BD1E50): the loot window closes and the corpse list empties.
func (gs *GameState) handleClientCloseLoot(msg *protocol.Message) {
	gs.HaveCorpse = false
	gs.Corpse = nil
	gs.corpseNext = 0
}

// handlePrepareItem handles opcode 0x000A, Msg000A (CLIENT 0x00BD9C18): an
// inventory slot byte and a quick slot byte. A quick slot under 4 takes the
// item if it fits there — P4C 2 in slots 0-1, P4C 7 in slot 2, a weapon's wear
// slot in slot 3 (FitsSlot3) — and the item that held it lets go. Anything
// else changes nothing: the PS2's test for 255 sits inside its test for < 4.
func (gs *GameState) handlePrepareItem(msg *protocol.Message) {
	d := msg.Data()
	if len(d) < 4 {
		log.Printf("PrepareItem: dataLen=%d (too short)", len(d))
		return
	}
	slot, pos := int(d[2]), int64(d[3])
	inv := gs.Inventory()
	if pos >= 4 || slot >= len(inv) {
		log.Printf("PrepareItem: slot %d of %d, quick slot %d", slot, len(inv), pos)
		return
	}
	prev := -1
	for i := range inv {
		if inv[i].QuickSlot == pos {
			prev = i
			break
		}
	}
	it := &inv[slot]
	if (it.P4C == 2 && pos < 2) || (it.P4C == 7 && pos == 2) || (fitsQuickSlot3(it.Slot) && pos == 3) {
		if prev != -1 {
			inv[prev].QuickSlot = -1
		}
		it.QuickSlot = pos
	}
	log.Printf("PrepareItem: %q quick slot %d -> %d", it.Name, pos, it.QuickSlot)
}

// handleAdjustItemHP handles opcode 0x0060, Msg0060 (CLIENT 0x00BD2A14,
// VIItemList::Op0060): an item KEY (u32) and a zigzag HP, the current HP of
// the inventory record with that key, floored at 0. (The PS2 writes it 12
// bytes before the list when no record has the key.)
func (gs *GameState) handleAdjustItemHP(msg *protocol.Message) {
	r := buffer.NewReader(msg.Data()[2:])
	key, err1 := r.ReadUint32()
	hp, err2 := readSigned(r)
	if err1 != nil || err2 != nil {
		log.Printf("AdjustItemHP: dataLen=%d (too short)", len(msg.Data()))
		return
	}
	inv := gs.Inventory()
	i := listFind(inv, key)
	if i < 0 {
		log.Printf("AdjustItemHP: no inventory record has key %d", key)
		return
	}
	if hp < 0 {
		hp = 0
	}
	inv[i].RemainingHP = int64(hp)
}

// handleBlackSmithMenu handles opcode 0x0061, Msg0061 (CLIENT): one u32, the
// blacksmith, kept to send back with every repair (Send0062), and the repair
// window opens. (The PS2 opens it only when no other panel is up.)
func (gs *GameState) handleBlackSmithMenu(msg *protocol.Message) {
	d := msg.Data()
	if len(d) < 6 {
		log.Printf("BlackSmithMenu: dataLen=%d (too short)", len(d))
		return
	}
	gs.BlacksmithID = binary.LittleEndian.Uint32(d[2:6])
	gs.BlacksmithOpen = true
	log.Printf("BlackSmithMenu: blacksmith 0x%08X", gs.BlacksmithID)
}

// handleCloseBlacksmith handles opcode 0x0063, Msg0063 (CLIENT): the
// blacksmith is forgotten and the window closes.
func (gs *GameState) handleCloseBlacksmith(msg *protocol.Message) {
	gs.BlacksmithID = 0xFFFFFFFF
	gs.BlacksmithOpen = false
}

// handleInventoryFull handles opcode 0x00D7, Msg00D7 (CLIENT): no payload. It
// clears the PS2's three request-pending flags (a merchant request at
// +0x1B304, the bank's at VIBankControl+0x78C8, an item use at +0x13B90) and
// shows nothing. The Go client keeps none of the three.
func (gs *GameState) handleInventoryFull(msg *protocol.Message) {
	log.Printf("0x00D7: request flags cleared")
}

// handleTradeUI handles opcode 0x0056 (CLIENT's dispatcher, 0x00BD28A8): a
// sub-type byte and the partner (u32). Only 1, 2 and 20 are taken from anyone
// but the partner. 1 and 16 end the trade; 20 ends it after "<name> is busy
// and cannot accept trades." (word 651) when this side asked for it; 17, 21
// and 22 are trade errors (words 733-735, in red). 2 opens the trade window,
// 6 and 7 are the window's, and the rest are its offers (TradeA/TradeB). The
// Go client has no trade window, and answers every offer as busy (0x0057).
func (gs *GameState) handleTradeUI(msg *protocol.Message) {
	r := buffer.NewReader(msg.Data()[2:])
	sub, err1 := r.ReadByte()
	id, err2 := r.ReadUint32()
	if err1 != nil || err2 != nil {
		log.Printf("TradeUI: dataLen=%d", len(msg.Data()))
		return
	}
	if id != gs.TradePartner && sub != 1 && sub != 2 && sub != 20 {
		return
	}
	switch sub {
	case 1, 16: // Trade01 is Trade16
		gs.tradeReset()
	case 20: // Trade20
		if gs.tradeAsked && gs.tradeEntity != 0xFFFFFFFF {
			gs.addChatMessage(fmt.Sprintf(itemWords[651], gs.tradeName(gs.tradeEntity, "Unknown")), ChatSystem)
		}
		gs.tradeReset()
	case 17:
		gs.addChatMessage(itemWords[733], ChatError)
	case 21:
		gs.addChatMessage(itemWords[734], ChatError)
	case 22:
		gs.addChatMessage(itemWords[735], ChatError)
	case 2: // Trade02
		gs.TradePartner = id
		log.Printf("TradeUI: a trade with 0x%08X opened, and there is no trade window", id)
	default:
		log.Printf("TradeUI: sub-type %d from 0x%08X (%d bytes)", sub, id, r.Remaining())
	}
}

// handleTradeRequest handles opcode 0x0057, Msg0057 (CLIENT 0x00BDA1A8): the
// partner and the partner's entity (two u32s). The PS2 puts up the offer when
// no panel is open; otherwise it says "<name> wants to trade with you, but you
// are busy." (word 626), declines (0x0056 sub-type 20, the partner) and
// resets. The Go client has no trade window, so it is always the second.
func (gs *GameState) handleTradeRequest(msg *protocol.Message) {
	d := msg.Data()
	if len(d) < 10 {
		log.Printf("TradeRequest: dataLen=%d", len(d))
		return
	}
	partner := binary.LittleEndian.Uint32(d[2:6])
	gs.TradePartner, gs.tradeEntity = partner, binary.LittleEndian.Uint32(d[6:10])
	gs.addChatMessage(fmt.Sprintf(itemWords[626], gs.tradeName(gs.tradeEntity, "Someone")), ChatSystem)
	if gs.Conn != nil {
		decline := make([]byte, 5) // SendOpByteInt(86, 20, partner)
		decline[0] = 20
		binary.LittleEndian.PutUint32(decline[1:], partner)
		gs.Conn.QueueReliable(protocol.OpcodeTradeUI, decline)
	}
	gs.tradeReset()
}

// handleOutMatched is 0x077A (the dispatcher's case), with no payload: word
// 1196 in red (1, 0.25, 0.25), auto-attack off, and 0x077B (1915) back.
func (gs *GameState) handleOutMatched(msg *protocol.Message) {
	log.Printf("OutMatched")
	gs.addWordLine(gs.wordText(1196), 255, 64, 64)
	gs.CombatEnded = true // StopAutoAttack: the animation layer turns auto-attack off
	if gs.Conn != nil {
		gs.Conn.QueueReliable(protocol.GameOpcode(0x077B), nil)
	}
}

// handleGuildInviteStuff is 0x057A, VIClientPart2EF24::Recv057A (0x00BEF6D8),
// guild invitations, by sub-type:
//
//	1  an invitation: a zigzag the PS2 does not keep, the inviter's id (u32)
//	   and name, the guild's name (wide). Busy, the PS2 says so (650) and
//	   answers 0x0578 {10, id, 2}; already in a guild it answers {10, id, 4};
//	   otherwise it asks. The question is a chat line here (535 is the
//	   window's title), the invitation kept in GuildInvite.
//	2  a zigzag and a name: "%s has accepted your invitation." (557)
//	3  a byte and a name: why an invitation failed (558 for 1 and anything
//	   unknown; 559, 560, 576, 587, 589, 642 for 2-7)
func (gs *GameState) handleGuildInviteStuff(msg *protocol.Message) {
	s := newSocialStream(msg)
	switch sub := s.u8(); sub {
	case 1:
		s.zigzag()
		id := s.u32()
		name := s.str8(24)
		guild := s.str16(128)
		if s.err {
			return
		}
		reply := func(why byte) {
			p := append([]byte{10}, binary.LittleEndian.AppendUint32(nil, id)...)
			gs.socialSend(protocol.GameOpcode(0x0578), append(p, why))
			gs.GuildInvite = nil
		}
		switch {
		case gs.socialBusy():
			gs.socialShowText(socialFormat(gs.socialWord(650), name))
			reply(2)
		case gs.InGuild:
			reply(4)
		default:
			gs.GuildInvite = &GuildInviteEntry{ID: id, Name: name, GuildName: guild}
			gs.socialShowText(fmt.Sprintf("%s %s (%s)", gs.socialWord(535), name, guild))
		}
		log.Printf("GuildInvite: from %s (0x%08X) to %q", name, id, guild)
	case 2:
		s.zigzag()
		name := s.str8(24)
		gs.socialShowText(socialFormat(gs.socialWord(557), name))
	case 3:
		code := s.u8()
		name := s.str8(24)
		word, ok := map[byte]int{2: 559, 3: 560, 4: 576, 5: 587, 6: 589, 7: 642}[code]
		if !ok {
			word = 558
		}
		gs.socialShowText(socialFormat(gs.socialWord(word), name))
	default:
		log.Printf("GuildInvite: sub-type %d ignored", sub)
	}
}

// handleBuffNotification is 0x076F (Msg076F, 0x00BDB3E8): a buddy-list
// invitation, not a buff. The requester's id (u32, VIClient+0x2BB10) and name.
// Busy, the PS2 says so (649) and declines with 0x0368 {id, 1}; with 32
// buddies it declines {id, 8}; otherwise it asks (519 is the prompt's title),
// and AnswerBuddyRequest is the answer.
func (gs *GameState) handleBuffNotification(msg *protocol.Message) {
	s := newSocialStream(msg)
	id := s.u32()
	name := s.str8(24)
	if s.err {
		log.Printf("BuddyInvite: short message (%d bytes)", len(msg.Data()))
		return
	}
	gs.BuddyRequestID = id
	gs.BuddyRequestName = name
	switch {
	case gs.socialBusy():
		gs.socialShowText(socialFormat(gs.socialWord(649), name))
		gs.socialSendMsg(protocol.GameOpcode(0x0368), id, 1)
		gs.BuddyRequestID = 0xFFFFFFFF
	case len(gs.Buddies) >= 32:
		gs.socialSendMsg(protocol.GameOpcode(0x0368), id, 8)
		gs.BuddyRequestID = 0xFFFFFFFF
	default:
		gs.socialShowText(gs.socialWord(519) + " " + name)
	}
	log.Printf("BuddyInvite: from %s (0x%08X)", name, id)
}

// AnswerBuddyRequest answers the open buddy-list invitation the way the
// PS2's prompt does: AnswerBuddyRequest sends 0x0367 {id},
// RejectBuddyRequest 0x0368 {id, 2}.
func (gs *GameState) AnswerBuddyRequest(accept bool) {
	id := gs.BuddyRequestID
	if id == 0 || id == 0xFFFFFFFF {
		return
	}
	if accept {
		gs.socialSend(protocol.GameOpcode(0x0367), binary.LittleEndian.AppendUint32(nil, id))
	} else {
		gs.socialSendMsg(protocol.GameOpcode(0x0368), id, 2)
	}
	gs.BuddyRequestID = 0xFFFFFFFF
}

// handleCombatEnd is 0x0777, VIClient::Msg0777, which does not end combat:
// it hides the login window, prints "Disconnect-12", sets +0x4F0EC to -1 and
// makes a QUIET Disconnect(0, 1), then Msg0777Done(2) reloads the world and
// shows the login window. The server's word for it is DisconnectNoError.
func (gs *GameState) handleCombatEnd(msg *protocol.Message) {
	if !guaranteed(msg) {
		return
	}
	gs.Session.Disconnect.HasCode = false
	gs.serverDisconnect(0, true)
}

// handleNPCInteractResult handles opcodes 0x0729 / 0x0773 — NPC interaction result.
// PS2: reads entityID(uint32) + actionType(uint32) + nameString(24 bytes).
// Server confirms which NPC was interacted with and the result type.
func (gs *GameState) handleNPCInteractResult(msg *protocol.Message) {
	data := msg.Data()
	payload := data[2:] // skip opcode
	if len(payload) < 8 {
		return
	}
	entityID := binary.LittleEndian.Uint32(payload[0:4])
	actionType := binary.LittleEndian.Uint32(payload[4:8])
	name := ""
	if len(payload) >= 32 {
		nameBytes := payload[8:32]
		for i, b := range nameBytes {
			if b == 0 {
				name = string(nameBytes[:i])
				break
			}
		}
	}
	log.Printf("NPCInteractResult: entity=0x%08X action=%d name=%q", entityID, actionType, name)

	// PS2: calls SetAction on the NPC actor to play interaction animation
	// (turn to face player, gesture, etc). actionType maps to a CPlayList slot.
	if gs.SpellFXSys != nil && gs.SpellFXSys.OnSetAction != nil && entityID != 0 {
		gs.SpellFXSys.OnSetAction(entityID, int32(actionType))
	}
}

// handleColoredChat — see state.go for existing implementation of 0x0A7A / 0x0A7B.

// handleAddBuddyFailed is 0x03D8: "Add buddy failed: Your buddy's buddy list
// is full." (591). 0x03D7 is the player's own list being full (590).
func (gs *GameState) handleAddBuddyFailed(msg *protocol.Message) {
	gs.socialShowWord(591)
}

// handleBuddyListStuff is 0x0771 (Msg0771, 0x00BDB6B8): why an invitation
// went nowhere, a byte and a name: 2 declined (558), 1 busy (559), 5 cannot
// accept (560), 6 expired (587), 7 too many open invitations (589), 8 their
// buddy list is full (586), 9 already a buddy (592), 10 ignoring you (642).
// (Any other byte formats a word id the PS2 never set; nothing is shown.)
func (gs *GameState) handleBuddyListStuff(msg *protocol.Message) {
	s := newSocialStream(msg)
	code := s.u8()
	name := s.str8(24)
	word, ok := map[byte]int{2: 558, 1: 559, 5: 560, 6: 587, 7: 589, 8: 586, 9: 592, 10: 642}[code]
	if !ok || s.err {
		log.Printf("InviteResult: code %d from %q not shown", code, name)
		return
	}
	gs.socialShowText(socialFormat(gs.socialWord(word), name))
}

// handleFactionPage is 0x07E2, VIClientPart4F078::Recv07E2 (0x00BEA820): a
// 0x77 byte, the request's token, the page's index and the number of pages
// (all zigzag), then a zigzag count of factions, each a wide name (63 at most)
// and a zigzag standing. The PS2 keeps one page, the one it asked for last;
// this client asks for every page at once and keeps them all.
func (gs *GameState) handleFactionPage(msg *protocol.Message) {
	s := newSocialStream(msg)
	if s.u8() != 0x77 {
		return
	}
	token := s.zigzag()
	page := s.zigzag()
	pages := s.zigzag()
	count := int(s.zigzag())
	if s.err || count < 0 || count > 1000 {
		log.Printf("FactionPage: bad header (count %d)", count)
		return
	}
	for i := 0; i < count; i++ {
		name := s.str16(128)
		standing := s.zigzag()
		if s.err {
			break
		}
		gs.Factions = append(gs.Factions, FactionEntry{Name: name, Standing: int(standing)})
	}
	log.Printf("FactionPage: token=%d page %d of %d, %d factions, %d held",
		token, page, pages, count, len(gs.Factions))
	if page+1 >= pages {
		gs.FactionLoaded = true
	}
}

// handleUpdateTrainingPoints handles opcode 0x001D — training point spend confirmation.
// Server sends: zigzag(totalSpent). Client updates remaining TP.
func (gs *GameState) handleUpdateTrainingPoints(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 3 {
		return
	}
	// Msg001D (0x00BDCAF0): the record's +0x40 -= z, then the window refreshes.
	reader := buffer.NewReader(data[2:])
	z, err := vimsSigned(reader)
	if err != nil {
		return
	}
	spent := int64(z)

	gs.TrainingPoints -= spent
	// Clear pending allocations after successful spend
	gs.TPAlloc = [7]int{}
	log.Printf("UpdateTrainingPoints: spent=%d remaining=%d", spent, gs.TrainingPoints)
	gs.addChatMessage(fmt.Sprintf("Spent %d training points (%d remaining)", spent, gs.TrainingPoints), ChatSystem)
}

// handleClassMasteryServer is 0x1402, VIClientPart4EC58::Recv1402
// (0x00BF7240), class mastery, by sub-type:
//
//	1  a u32 (+0x41C), a u32 (+0x08)
//	2  a u32 (+0x41C)
//	3  the points: a u32 (+0x0C) and a u32 (+0x10, the unspent count this
//	   client shows); a rise in the first is "You have earned %d mastery
//	   points." (1627; 1628 for one), a notice on the PS2
//	4  a u32 (+0x14)
//	5  a skill bought: u32 (+0x41C), its id, the two point counts, the run
//	   speed (f32, through SetSpeed), then stat blocks not ported; the skill
//	   is marked owned
//	6  a skill given back: u32 (+0x41C), its id, the speed, the stat blocks;
//	   the skill is marked not owned
//	7  a skill's id and whether it can be bought
//	8  the whole tree (func_00BF7748)
//	9  one skill's detail (func_00BF7810)
func (gs *GameState) handleClassMasteryServer(msg *protocol.Message) {
	s := newSocialStream(msg)
	sub := s.u8()
	switch sub {
	case 1:
		s.u32()
		id := s.u32()
		log.Printf("CM: select ack 0x%04X", id)
	case 2:
		s.u32()
	case 3:
		points := s.u32()
		unspent := s.u32()
		if s.err {
			return
		}
		if old := gs.CMPoints; points > old {
			if n := points - old; n >= 2 {
				gs.socialShowText(socialFormat(gs.socialWord(1627), n))
			} else {
				gs.socialShowWord(1628)
			}
		}
		gs.CMPoints = points
		gs.CMUnspent = int(unspent)
		log.Printf("CM: points %d, %d unspent", points, unspent)
	case 4:
		s.u32()
	case 5, 6:
		s.u32()
		id := s.u32()
		if sub == 5 {
			gs.CMPoints = s.u32()
			gs.CMUnspent = int(s.u32())
		}
		speed := s.u32()
		if s.err {
			return
		}
		gs.Player.Speed = math.Float32frombits(speed)
		gs.socialSetCMOwned(int32(id), sub == 5)
		log.Printf("CM: skill 0x%04X owned=%v, speed %.2f", id, sub == 5, gs.Player.Speed)
	case 7:
		id := s.u32()
		v := s.u32()
		if s.err {
			return
		}
		for i := range gs.CMTree {
			if gs.CMTree[i].ID == int32(id) {
				gs.CMTree[i].Available = byte(v)
			}
		}
		gs.socialCMSkills()
	case 8:
		gs.parseCMTree(s)
	case 9:
		gs.parseCMDetail(s)
	default:
		log.Printf("CM: sub-type %d ignored (%d bytes)", sub, len(msg.Data()))
	}
}

// parseCMTree is func_00BF7748: a u32 (+0x41C), a u32 count, then that many
// tree entries (func_00BF80A0).
func (gs *GameState) parseCMTree(s *socialStream) {
	s.u32()
	n := int(s.u32())
	if s.err || n < 0 || n > 4096 {
		log.Printf("CM: bad tree header (count %d)", n)
		return
	}
	tree := make([]CMTreeEntry, 0, n)
	for i := 0; i < n; i++ {
		e := readCMTreeEntry(s)
		if s.err {
			log.Printf("CM: tree cut short at entry %d of %d", i, n)
			break
		}
		tree = append(tree, e)
	}
	gs.CMTree = tree
	gs.socialCMSkills()
	gs.CMTreeLoaded = true
	log.Printf("CM: tree loaded — %d entries, %d skills", len(tree), len(gs.CMSkills))
}

// parseCMDetail is func_00BF7810: a u32 (+0x41C), then the record
// func_00BF81F0 reads: zigzag id, two bytes, two zigzags (the second the
// prerequisite), two bytes, nine zigzags, the name, a zigzag, a second name
// and the description (wide: 63, 63, 255), then three counted arrays (byte
// and zigzag pairs; a wide string and a byte; wide strings).
func (gs *GameState) parseCMDetail(s *socialStream) {
	s.u32()
	d := &CMSkillDetail{}
	d.ID = int(s.zigzag())
	d.Rank = s.u8()
	s.u8()
	s.zigzag()
	s.zigzag()
	s.u8()
	s.u8()
	for i := 0; i < 9; i++ {
		s.zigzag()
	}
	d.Name = s.str16(128)
	s.zigzag()
	d.DisplayName = s.str16(128)
	d.Description = s.str16(512)
	for i, n := 0, int(s.zigzag()); i < n && !s.err; i++ {
		s.u8()
		s.zigzag()
	}
	for i, n := 0, int(s.zigzag()); i < n && !s.err; i++ {
		s.str16(128)
		s.u8()
	}
	for i, n := 0, int(s.zigzag()); i < n && !s.err; i++ {
		s.str16(128)
	}
	if s.err {
		log.Printf("CM: short detail record")
		return
	}
	d.MaxRank = 1
	for _, e := range gs.CMTree {
		if int(e.ID) == d.ID {
			d.Cost = int(e.Cost)
		}
	}
	gs.CMDetail = d
	log.Printf("CM: detail id=%d name=%q desc=%q owned=%d", d.ID, d.Name, d.Description, d.Rank)
}

// socialSetCMOwned marks one tree entry owned or not (func_00BF78A8 finds it).
func (gs *GameState) socialSetCMOwned(id int32, owned bool) {
	for i := range gs.CMTree {
		if gs.CMTree[i].ID == id {
			if owned {
				gs.CMTree[i].Owned = 1
			} else {
				gs.CMTree[i].Owned = 0
			}
		}
	}
	gs.socialCMSkills()
}

// socialCMSkills rebuilds the list the client's mastery tab draws out of the
// tree: every entry that is owned or can be bought (the "x" placeholders are
// neither). Rank is owned (0 or 1); each skill is one purchase.
func (gs *GameState) socialCMSkills() {
	gs.CMSkills = gs.CMSkills[:0]
	for _, e := range gs.CMTree {
		if e.Owned == 0 && e.Available == 0 {
			continue
		}
		gs.CMSkills = append(gs.CMSkills, CMSkill{
			ID: int(e.ID), Name: e.Name, HasName: true,
			PrereqID: int(e.Prereq), Cost: int(e.Cost), Rank: e.Owned, MaxRank: 1,
		})
	}
}

// decodeUTF16LE decodes a UTF-16LE byte slice to a Go string.
func decodeUTF16LE(b []byte) string {
	var result []rune
	for i := 0; i+1 < len(b); i += 2 {
		c := binary.LittleEndian.Uint16(b[i : i+2])
		if c == 0 {
			break
		}
		result = append(result, rune(c))
	}
	return string(result)
}

// isPrintableString checks if all chars are printable ASCII + common symbols.
func isPrintableString(s string) bool {
	for _, c := range s {
		if c < 32 || c > 126 {
			if c != '\'' {
				return false
			}
		}
	}
	return true
}

// extractUTF16Strings16 extracts consecutive UTF-16LE strings with int32 length prefixes.
func extractUTF16Strings16(data []byte) []string {
	var result []string
	pos := 0
	for pos < len(data)-4 {
		nameLen := int(binary.LittleEndian.Uint32(data[pos : pos+4]))
		if nameLen <= 0 || nameLen > 200 || pos+4+nameLen*2 > len(data) {
			pos++
			continue
		}
		s := decodeUTF16LE(data[pos+4 : pos+4+nameLen*2])
		if len(s) >= 1 {
			result = append(result, s)
			pos += 4 + nameLen*2
		} else {
			pos++
		}
	}
	return result
}

// handleBankUI handles opcode 0x124D (CLIENT's dispatcher, 0x00BD2654): no
// payload. The bank window closes and its pending request clears
// (VIBankControl::Close). The bank's records come with the MemoryDump and
// change with 0x1250 and 0x1251.
func (gs *GameState) handleBankUI(msg *protocol.Message) {
	log.Printf("BankUI: closed")
}

// handleConfirmBankTunar handles opcode 0x1253 (VIBankControl::Recv, CLIENT
// 0x00BB9CF0): one zigzag, the tunar in the bank (VIPlayer+0x38).
func (gs *GameState) handleConfirmBankTunar(msg *protocol.Message) {
	v, err := readSigned(buffer.NewReader(msg.Data()[2:]))
	if err != nil {
		return
	}
	gs.BankTunar = int64(v)
	log.Printf("ConfirmBankTunar: bank tunar=%d", v)
}

// handleAuctionStuff handles opcode 0x1206 — auction data.
// Small (7 bytes): sub(0x81) + page + 5 zeros = status/ack.
// Large (5000+ bytes, reassembled from fragments): item listings.
//   Header: sub(0x81) + page(byte) + unk(byte) + itemCount(byte)
//   Per item: sellerNameLen(uint32) + sellerName(ASCII) + item fields (parseSingleItem)
//             + currentBid(zigzag) + buyoutPrice(zigzag)
func (gs *GameState) handleAuctionStuff(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 3 {
		return
	}
	payload := data[2:] // skip 2-byte opcode prefix

	// 0x1206 is VIBankControl::Recv1206 (CLIENT 0x00BB9D90,
	// VIBankControl.cpp:593): a sub-type byte, 0x80-0x89 into a ten-entry
	// jump table and anything else an assert. 0x81 is a page of search
	// results (func_00BBA2F8: "No auctions found.", "End of search
	// results.", "Start of search results."). The other nine are not ported.
	// They were once read as a quest journal, which was this same function
	// under the wrong name.
	if sub := payload[0]; sub != 0x81 {
		log.Printf("AuctionStuff: sub-type 0x%02X (%d bytes) not handled", sub, len(payload))
		return
	}

	// The page, all zigzag: the request's number echoed (the PS2 drops a
	// page whose number is not its latest request's), a field it keeps at
	// +0x7868, then the list (func_00BBD9E8: a count and the listings), then
	// three arrays of one value per listing (+0x7880/+0x7888/+0x7890, each a
	// count and the values).
	reader := buffer.NewReader(payload[1:])
	seq, _ := reader.Read7BitEncodedInt64()
	field, _ := reader.Read7BitEncodedInt64()
	itemCount, err := reader.Read7BitEncodedInt64()
	if err != nil || itemCount < 0 || itemCount > 1000 {
		log.Printf("AuctionStuff: bad page header (count %d, %v)", itemCount, err)
		return
	}
	items := make([]AuctionItem, 0, itemCount)

	for i := 0; i < int(itemCount); i++ {
		var ai AuctionItem
		startPos := reader.Position()

		// Auction header: 7 zigzag fields
		ai.ListingID, _ = reader.Read7BitEncodedInt64()
		auctionStack, _ := reader.Read7BitEncodedInt64()
		auctionMaxHP, _ := reader.Read7BitEncodedInt64()
		auctionUnk, _ := reader.Read7BitEncodedInt64()
		ai.CurrentBid, _ = reader.Read7BitEncodedInt64()
		ai.BuyoutPrice, _ = reader.Read7BitEncodedInt64()
		auctionItemID, _ := reader.Read7BitEncodedInt64()
		_, _, _ = auctionStack, auctionMaxHP, auctionUnk

		// Seller name: uint32 length + ASCII bytes
		sellerLen, err := reader.ReadUint32()
		if err != nil || sellerLen > 200 {
			log.Printf("AuctionStuff[%d]: bad seller len %d at pos %d (listingID=%d remaining=%d)",
				i, sellerLen, reader.Position(), ai.ListingID, reader.Remaining())
			break
		}
		if sellerLen > 0 {
			nameBytes, err := reader.ReadBytes(int(sellerLen))
			if err != nil {
				break
			}
			ai.Seller = string(nameBytes)
		}

		// The item proper (func_00CBACE8), as every item record carries it.
		if err := readItemBody(reader, &ai.Item); err != nil {
			log.Printf("AuctionStuff[%d]: item at pos %d seller=%q itemID=%d: %v",
				i, reader.Position(), ai.Seller, auctionItemID, err)
			break
		}

		bytesConsumed := reader.Position() - startPos

		items = append(items, ai)
		log.Printf("AuctionItem[%d]: seller=%q name=%q bid=%d buyout=%d lvl=%d bytes=%d",
			i, ai.Seller, ai.Item.Name, ai.CurrentBid, ai.BuyoutPrice, ai.Item.LevelReq, bytesConsumed)
	}

	for k := 0; k < 3; k++ {
		n, err := reader.Read7BitEncodedInt64()
		if err != nil || n < 0 || n > 1000 {
			break
		}
		for j := 0; j < int(n); j++ {
			v, _ := reader.Read7BitEncodedInt64()
			if j < len(items) {
				items[j].PageValues[k] = v
			}
		}
	}
	log.Printf("AuctionStuff: page seq=%d field=%d listings=%d/%d left=%d",
		seq, field, len(items), itemCount, reader.Remaining())

	// An empty page ends the search. The PS2 keeps the page it had and says
	// which; here the pages accumulate, and the paging stops.
	if len(items) == 0 {
		if len(gs.AuctionItems) == 0 {
			gs.addChatMessage("No auctions found.", ChatSystem)
		} else {
			gs.addChatMessage("End of search results.", ChatSystem)
		}
		gs.AuctionLastListing = 0
		return
	}
	gs.AuctionItems = append(gs.AuctionItems, items...)
	gs.AuctionTotalCount = len(gs.AuctionItems)
	gs.AuctionLastListing = items[len(items)-1].ListingID
	// The next request's number, as the one zigzag byte the sender writes.
	if seq >= 0 && seq < 63 {
		gs.AuctionSeq = byte(2 * (seq + 1))
	} else {
		gs.AuctionLastListing = 0
	}
	log.Printf("Auction: +%d items (total %d)", len(items), len(gs.AuctionItems))
}

// handleAdminShutdown is 0x07C1: Disconnect(18, 0), "We are updating the
// game, please try again later.", DisconnectKind 4.
func (gs *GameState) handleAdminShutdown(msg *protocol.Message) {
	gs.handleServerDisconnect(msg)
}

// =============================================================================
// Helper: add chat message to log
// =============================================================================

func (gs *GameState) addChatMessage(text string, channel ChatChannel) {
	gs.ChatLog = append(gs.ChatLog, ChatMessage{
		Text:    text,
		Channel: channel,
	})
	// Keep chat log bounded
	if len(gs.ChatLog) > 200 {
		gs.ChatLog = gs.ChatLog[len(gs.ChatLog)-200:]
	}
}

// Ensure imports are used
var _ = binary.LittleEndian
var _ = math.Float32frombits
var _ = time.Now
var _ = fmt.Sprintf
