package game

// handlers.go — Server→Client opcode handlers for all opcodes identified from
// PS2 CLIENT.AUT decompilation (VIClient.cpp mega-function at 0x00BD0DC8).
// Covers entity lifecycle, quest system, world entry/zone transfer, NPC
// interaction, item streaming, chat, guild, and social.
//
// PS2 Disconnect Error Codes (from debug printf strings in CLIENT data section):
//   Disconnect-5:  Malformed entity/group data (opcodes 0x0012, 0x07A2)
//   Disconnect-6:  Bad stats update (opcode 0x0790)
//   Disconnect-7:  Invalid admin/world data (opcode 0x0700)
//   Disconnect-8:  Buddy list corruption (opcode 0x0771)
//   Disconnect-9:  Auction system error (opcode 0x07E2)
//   Disconnect-12: Combat state corruption (opcode 0x0777)
//   Disconnect-??: Unknown/catch-all

import (
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"time"

	"github.com/eqoa/client/internal/protocol"
	"github.com/DabDavis/eqoa-esf-tools/pkg/buffer"
)

// PS2 disconnect error codes from CLIENT debug strings.
// Logged when opcode handlers receive malformed data.
var ps2DisconnectCodes = map[uint16]int{
	0x0012: 5,  // GroupInvite
	0x0700: 7,  // AdminCode
	0x0771: 8,  // BuddyListStuff
	0x0777: 12, // CombatEnd
	0x0790: 6,  // StatsUpdate
	0x07A2: 5,  // EntityStringUpdate
	0x07E2: 9,  // Auction
}

func logPS2DisconnectCode(opcode uint16) {
	if code, ok := ps2DisconnectCodes[opcode]; ok {
		log.Printf("WARNING: malformed data for opcode 0x%04X (PS2 Disconnect-%d)", opcode, code)
	}
}

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
// Entity Update Cascade (0x07F4/F0/F1/A1/A2/C0)
// =============================================================================

// handleEntityUpdateFullUI handles opcode 0x07F4 — enables HUD+map panels, then entity update.
func (gs *GameState) handleEntityUpdateFullUI(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 4 {
		return
	}
	log.Printf("EntityUpdateFullUI: enabling HUD+map panels, dataLen=%d", len(data))
	reader := buffer.NewReader(data[2:])
	gs.parseEntityUpdateCascade(reader, "EntityUpdateFullUI")
}

// handleEntityUpdateMapPanel handles opcode 0x07F0/0x07F1 — enables map panel, then entity update.
func (gs *GameState) handleEntityUpdateMapPanel(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 4 {
		return
	}
	log.Printf("EntityUpdateMapPanel: enabling map panel, dataLen=%d", len(data))
	reader := buffer.NewReader(data[2:])
	// PS2: reads 8-byte timestamp, stops audio, then shared body
	gs.parseEntityUpdateCascade(reader, "EntityUpdateMapPanel")
}

// handleEntityUpdateAudio handles opcode 0x07A1 — stops audio, then entity update.
func (gs *GameState) handleEntityUpdateAudio(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 4 {
		return
	}
	reader := buffer.NewReader(data[2:])
	// PS2: reads 8-byte timestamp
	if reader.Remaining() >= 8 {
		ts, _ := reader.ReadBytes(8)
		log.Printf("EntityUpdateAudio: timestamp=%X, stopping audio", ts)
	}
	gs.parseEntityUpdateCascade(reader, "EntityUpdateAudio")
}

// handleEntityUpdateMinimal handles opcode 0x07A2 — minimal cleanup, then entity update.
func (gs *GameState) handleEntityUpdateMinimal(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 4 {
		return
	}
	log.Printf("EntityUpdateMinimal: minimal cleanup, dataLen=%d", len(data))
	reader := buffer.NewReader(data[2:])
	gs.parseEntityUpdateCascade(reader, "EntityUpdateMinimal")
}

// handleEntityUpdateDirect handles opcode 0x07C0 — no prefix, straight entity update.
func (gs *GameState) handleEntityUpdateDirect(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 4 {
		return
	}
	reader := buffer.NewReader(data[2:])
	gs.parseEntityUpdateCascade(reader, "EntityUpdateDirect")
}

// parseEntityUpdateCascade is the shared body for entity update cascade opcodes.
// PS2: reads zigzag entity ID, raw byte flag, zigzag zone delta, zigzag value,
// name(24), final 4-byte → state 19.
func (gs *GameState) parseEntityUpdateCascade(reader *buffer.Reader, source string) {
	if reader.Remaining() < 2 {
		return
	}

	// Read entity ID (zigzag)
	entityID, err := reader.Read7BitEncodedInt64()
	if err != nil {
		log.Printf("%s: failed to read entity ID: %v", source, err)
		return
	}

	// Read flag byte
	flag := byte(0)
	if reader.Remaining() >= 1 {
		flag, _ = reader.ReadByte()
	}

	// Read zone delta (zigzag)
	zoneDelta := int64(0)
	if reader.Remaining() >= 1 {
		zoneDelta, _ = reader.Read7BitEncodedInt64()
	}

	// Read value (zigzag)
	value := int64(0)
	if reader.Remaining() >= 1 {
		value, _ = reader.Read7BitEncodedInt64()
	}

	// Read name (24-byte fixed)
	name := ""
	if reader.Remaining() >= 24 {
		nameBytes, err := reader.ReadBytes(24)
		if err == nil {
			for i, b := range nameBytes {
				if b == 0 {
					name = string(nameBytes[:i])
					break
				}
			}
		}
	}

	log.Printf("%s: entityID=%d flag=%d zoneDelta=%d value=%d name=%q remaining=%d",
		source, entityID, flag, zoneDelta, value, name, reader.Remaining())

	// Update entity if it exists
	objID := uint32(entityID)
	if objID != 0 {
		ent, exists := gs.Entities[objID]
		if exists && name != "" {
			ent.Name = name
			ent.LastUpdate = time.Now()
		}
	}
}

// =============================================================================
// Quest System (0x0045/7B/7D/7F/0036)
// =============================================================================

// handleQuestDialogueResp handles opcode 0x0045 — quest dialogue response.
// PS2: zigzag option ID → func_BDB820 builds formatted string with msg 550/551.
func (gs *GameState) handleQuestDialogueResp(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 4 {
		return
	}
	reader := buffer.NewReader(data[2:])
	optionID, err := reader.Read7BitEncodedInt64()
	if err != nil {
		return
	}
	log.Printf("QuestDialogueResp: optionID=%d", optionID)

	// Read remaining quest text if present
	gs.parseQuestTail(reader, "QuestDialogueResp")
}

// handleUpdateQuestProgress handles opcode 0x007B — quest progress update.
// PS2: quest ID, 256-char quest text (ring buffer, 8 entries), progress zigzag, float values.
func (gs *GameState) handleUpdateQuestProgress(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 4 {
		return
	}
	reader := buffer.NewReader(data[2:])

	// Read quest ID
	questID, err := reader.Read7BitEncodedInt64()
	if err != nil {
		log.Printf("UpdateQuestProgress: failed to read quest ID: %v", err)
		return
	}

	// Read quest text (string)
	questText := ""
	if reader.Remaining() > 4 {
		text, err := readStringFromReader(reader)
		if err == nil {
			questText = text
		}
	}

	// Read progress value (zigzag)
	progress := int64(0)
	if reader.Remaining() >= 1 {
		progress, _ = reader.Read7BitEncodedInt64()
	}

	// Read float values
	var floats [4]float32
	for i := 0; i < 4 && reader.Remaining() >= 4; i++ {
		bits, err := reader.ReadUint32()
		if err != nil {
			break
		}
		floats[i] = math.Float32frombits(bits)
	}

	// Store in ring buffer
	gs.QuestTrackerID = uint32(questID)
	if questText != "" {
		gs.QuestTexts[gs.QuestTextIndex%8] = questText
		gs.QuestTextIndex++
	}
	gs.QuestProgress = progress
	gs.QuestProgressF = floats

	log.Printf("UpdateQuestProgress: questID=%d text=%q progress=%d floats=%v",
		questID, questText, progress, floats)

	gs.parseQuestTail(reader, "UpdateQuestProgress")
}

// handleDeleteQuest handles opcode 0x007D — remove quest from journal.
func (gs *GameState) handleDeleteQuest(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 4 {
		return
	}
	reader := buffer.NewReader(data[2:])
	questID, err := reader.Read7BitEncodedInt64()
	if err != nil {
		return
	}
	log.Printf("DeleteQuest: questID=%d", questID)
	if gs.QuestTrackerID == uint32(questID) {
		gs.QuestTrackerID = 0xFFFFFFFF
	}
}

// handleClearQuestTracker handles opcode 0x007F — clears active quest tracker.
// PS2: sets +0x2BE50=0xFFFFFFFF, zeroes +0x2BE54. Then shared string+message tail.
func (gs *GameState) handleClearQuestTracker(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 4 {
		return
	}
	gs.QuestTrackerID = 0xFFFFFFFF
	log.Printf("ClearQuestTracker: quest tracker cleared")

	reader := buffer.NewReader(data[2:])
	gs.parseQuestTail(reader, "ClearQuestTracker")
}

// handleQuestStageComplete handles opcode 0x0036 — quest stage reward.
// PS2 (0x00BD26CC): stageIndex(u8) + stageCounter(zigzag) + xpReward(zigzag)
//   + subStageA(u8) + subStageB(u8) + 3×uint32 + float3 + rewardA(u32) + rewardB(u32)
//   + stageType(u8) + additional(u32) → shared tail
func (gs *GameState) handleQuestStageComplete(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 4 {
		return
	}
	reader := buffer.NewReader(data[2:])

	// Stage index (uint8)
	stageIndex := byte(0)
	if reader.Remaining() >= 1 {
		stageIndex, _ = reader.ReadByte()
	}

	// Stage counter (zigzag)
	stageCounter := int64(0)
	if reader.Remaining() >= 1 {
		stageCounter, _ = reader.Read7BitEncodedInt64()
	}

	// XP reward (zigzag)
	xpDelta, err := reader.Read7BitEncodedInt64()
	if err != nil {
		return
	}

	// Sub-stages (2 bytes)
	subStageA, subStageB := byte(0), byte(0)
	if reader.Remaining() >= 2 {
		subStageA, _ = reader.ReadByte()
		subStageB, _ = reader.ReadByte()
	}

	// Position/reward data (3×uint32 = 12 bytes)
	if reader.Remaining() >= 12 {
		reader.ReadBytes(12) // position/reward data (not yet used)
	}

	// Camera position (3×float32 = 12 bytes)
	if reader.Remaining() >= 12 {
		reader.ReadBytes(12) // camera position (not yet used)
	}

	// Reward values (2×uint32 = 8 bytes)
	rewardA, rewardB := uint32(0), uint32(0)
	if reader.Remaining() >= 8 {
		rewardA, _ = reader.ReadUint32()
		rewardB, _ = reader.ReadUint32()
	}

	// Stage type (uint8, 22-way jump table in PS2)
	stageType := byte(0)
	if reader.Remaining() >= 1 {
		stageType, _ = reader.ReadByte()
	}

	// Additional value (uint32)
	if reader.Remaining() >= 4 {
		reader.ReadUint32()
	}

	log.Printf("QuestStageComplete: stage=%d counter=%d xp=%d subA=%d subB=%d rewardA=%d rewardB=%d type=%d",
		stageIndex, stageCounter, xpDelta, subStageA, subStageB, rewardA, rewardB, stageType)

	if xpDelta > 0 {
		gs.Player.XPRaw += xpDelta
		gs.Player.XPPct = xpLevelPct(int(gs.Player.Level), gs.Player.XPRaw)
		gs.addChatMessage(fmt.Sprintf("Quest stage %d complete! +%d XP", stageIndex, xpDelta), ChatSystem)
	}

	// Tunar reward
	if rewardA > 0 {
		gs.addChatMessage(fmt.Sprintf("Received %d tunar", rewardA), ChatSystem)
	}

	// Drain shared tail
	if reader.Remaining() > 0 {
		gs.parseQuestTail(reader, "QuestStageComplete")
	}
}

// parseQuestTail reads the shared tail of quest cascade opcodes:
// string(64) + byte + entity ref data.
func (gs *GameState) parseQuestTail(reader *buffer.Reader, source string) {
	// Try to read NPC name string (64 bytes)
	if reader.Remaining() >= 64 {
		nameBytes, err := reader.ReadBytes(64)
		if err == nil {
			name := ""
			for i, b := range nameBytes {
				if b == 0 {
					name = string(nameBytes[:i])
					break
				}
			}
			if name != "" {
				log.Printf("%s tail: npcName=%q", source, name)
			}
		}
	}

	// Read type byte
	if reader.Remaining() >= 1 {
		typeByte, _ := reader.ReadByte()
		if typeByte != 0 {
			log.Printf("%s tail: type=%d", source, typeByte)
		}
	}
}

// =============================================================================
// World Entry / Zone Transfer / Session (0x0728/0758/002E/0579/0790/07B0/07B2)
// =============================================================================

// handleServerLoginResponse handles opcode 0x0728 — post-auth world entry.
// PS2: debug prints, state reset 6/7, server list, char appearance bytes.
func (gs *GameState) handleServerLoginResponse(msg *protocol.Message) {
	data := msg.Data()
	log.Printf("ServerLoginResponse: dataLen=%d", len(data))
	// This opcode signals successful auth — transition toward character select
	// The actual char list comes via 0x002C, which we already handle
}

// handleReconnectWorldEntry handles opcode 0x0758 — reconnection world entry.
// PS2: reads server name string(512) + version byte first, then falls into login body.
func (gs *GameState) handleReconnectWorldEntry(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 4 {
		return
	}
	log.Printf("ReconnectWorldEntry: dataLen=%d", len(data))
	// Read server name (up to 512 bytes)
	reader := buffer.NewReader(data[2:])
	if reader.Remaining() >= 4 {
		nameLen, _ := reader.ReadInt32()
		if nameLen > 0 && nameLen <= 512 && reader.Remaining() >= int(nameLen) {
			nameBytes, _ := reader.ReadBytes(int(nameLen))
			serverName := string(nameBytes)
			log.Printf("ReconnectWorldEntry: serverName=%q", serverName)
		}
	}
	// Read version byte
	if reader.Remaining() >= 1 {
		version, _ := reader.ReadByte()
		log.Printf("ReconnectWorldEntry: version=%d", version)
		if version == 18 {
			log.Printf("ReconnectWorldEntry: early exit (version 18)")
			return
		}
	}
}

// handleWorldEntry handles opcode 0x002E — char select → in-world transition.
// PS2: sets state=3, reads appearance/equipment, full entity init.
func (gs *GameState) handleWorldEntry(msg *protocol.Message) {
	data := msg.Data()
	log.Printf("WorldEntry: dataLen=%d — transitioning to in-world", len(data))
	gs.Phase = PhaseEnteringWorld

	// Extract appearance/equipment if present (after opcode bytes)
	if len(data) > 2 {
		reader := buffer.NewReader(data[2:])
		// Try to read equipment block similar to char select
		gs.parseWorldEntryAppearance(reader)
	}
}

// handleGuildWorldEntry handles opcode 0x0579 — full world entry with guild init.
// PS2: inits 6 subsystems, enables UI panels 1662+676, audio reset, entity update.
func (gs *GameState) handleGuildWorldEntry(msg *protocol.Message) {
	data := msg.Data()
	log.Printf("GuildWorldEntry: full world entry with guild, dataLen=%d", len(data))
	gs.Phase = PhaseEnteringWorld

	if len(data) > 2 {
		reader := buffer.NewReader(data[2:])
		gs.parseWorldEntryAppearance(reader)
	}
}

// handleServerAssignment handles opcode 0x0790 — server/world assignment.
// PS2: name + flags + zone → state 19.
// handleStatsUpdate handles opcode 0x0790 — XP/stats update.
// PS2: reads int64+int32+int64 into entity+0x4F0F0..+0x4F107, sets sentinel -1,
// then calls UI refresh. Likely current XP, level/stat, max XP.
func (gs *GameState) handleStatsUpdate(msg *protocol.Message) {
	data := msg.Data()
	payload := data[2:] // skip opcode
	if len(payload) < 20 {
		logPS2DisconnectCode(0x0790)
		log.Printf("StatsUpdate: too short (%d bytes)", len(payload))
		return
	}
	val1 := binary.LittleEndian.Uint64(payload[0:8])
	val2 := binary.LittleEndian.Uint32(payload[8:12])
	val3 := binary.LittleEndian.Uint64(payload[12:20])
	log.Printf("StatsUpdate: val1=%d val2=%d val3=%d", val1, val2, val3)
	// Store for UI (XP bar, level display)
	gs.Player.Experience = int64(val1)
	gs.Player.ExperienceMax = int64(val3)
}

// handleZoneTransferInit handles opcode 0x07B0 — zone transfer init/loading.
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
			// PS2 stores pending zone index at entity+0x4F0E8 for the
			// zone about to be loaded. The result byte indicates the target zone.
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

// handleZoneTransferConfirm handles opcode 0x07B2 — zone transfer complete.
// PS2: same handler as 0x07B0 (FUN_00BE71B0) — shared sub-type dispatch.
func (gs *GameState) handleZoneTransferConfirm(msg *protocol.Message) {
	// Route through the same sub-type dispatcher
	gs.handleZoneTransferInit(msg)
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

// handleLootResult handles opcode 0x001A — loot outcome.
// PS2: status(1=ok/2=partial/3=fail → strings 673-675), item ID array, entity update.
func (gs *GameState) handleLootResult(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 4 {
		return
	}
	reader := buffer.NewReader(data[2:])
	status, err := reader.ReadByte()
	if err != nil {
		return
	}
	gs.LootResultStatus = status

	statusNames := map[byte]string{1: "success", 2: "partial", 3: "fail"}
	statusName := statusNames[status]
	if statusName == "" {
		statusName = fmt.Sprintf("unknown(%d)", status)
	}

	log.Printf("LootResult: status=%s remaining=%d", statusName, reader.Remaining())

	switch status {
	case 1:
		gs.LootResultMsg = "Loot acquired"
	case 2:
		gs.LootResultMsg = "Partial loot — inventory full"
		gs.addChatMessage("Your inventory is partially full.", ChatError)
	case 3:
		gs.LootResultMsg = "Loot failed"
		gs.addChatMessage("Loot failed.", ChatError)
	}
}

// handleNPCInteraction handles opcode 0x001C — NPC interaction response.
// PS2: skill/ability defs, UI window text, type-code dispatch
// (18=info, 16/3/4/6/7=training/merchant/blacksmith), then item list.
func (gs *GameState) handleNPCInteraction(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 6 {
		return
	}
	reader := buffer.NewReader(data[2:])

	// Read NPC interaction type
	npcType, err := reader.Read7BitEncodedInt64()
	if err != nil {
		return
	}
	gs.NPCInteractionType = int(npcType)

	typeNames := map[int]string{
		3: "merchant", 4: "blacksmith", 6: "quest", 7: "banker",
		16: "trainer", 18: "info",
	}
	typeName := typeNames[int(npcType)]
	if typeName == "" {
		typeName = fmt.Sprintf("type_%d", npcType)
	}

	log.Printf("NPCInteraction: type=%s(%d) remaining=%d", typeName, npcType, reader.Remaining())

	// Try to extract NPC name and text
	if reader.Remaining() > 4 {
		text, err := readStringFromReader(reader)
		if err == nil && text != "" {
			gs.NPCInteractionName = text
			log.Printf("NPCInteraction: text=%q", text)
		}
	}

	// For merchant/blacksmith, parse remaining data as items
	if reader.Remaining() > 10 {
		remaining := data[2+reader.Position()-reader.Remaining():]
		items := parseItemData(remaining)
		if len(items) > 0 && (npcType == 3 || npcType == 4) {
			gs.MerchantItems = items
			gs.MerchantName = gs.NPCInteractionName
			gs.MerchantOpen = true
			gs.MerchantSelected = -1
			log.Printf("NPCInteraction: opened merchant %q with %d items", gs.MerchantName, len(items))
		}
	}

	// Log string data for debugging
	if reader.Remaining() > 0 {
		strs := extractUTF16Strings(data)
		for _, s := range strs {
			writeItemLog("NPCInteraction", s)
		}
	}
}

// handleItemListUpdate handles opcode 0x0065 — full item definition with stats.
// PS2: reads appearance bytes, equipment slots, pricing.
func (gs *GameState) handleItemListUpdate(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 4 {
		return
	}
	log.Printf("ItemListUpdate: dataLen=%d", len(data))

	// Parse items using existing item parser
	if len(data) > 2 {
		gs.parseAndLogItems("ItemListUpdate", data)
	}
}

// handleNPCMessage handles opcode 0x00D3 — NPC/server say message.
// PS2 (0x00BD2C48): string(64) npcName + byte messageType + dialogue display
//   + byte optionA + byte optionB + string(24) speakerName → UTF-16 → shared tail
func (gs *GameState) handleNPCMessage(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 4 {
		return
	}
	reader := buffer.NewReader(data[2:])

	// Read NPC name (64-byte fixed buffer)
	npcName := ""
	if reader.Remaining() >= 64 {
		nameBytes, err := reader.ReadBytes(64)
		if err == nil {
			for i, b := range nameBytes {
				if b == 0 {
					npcName = string(nameBytes[:i])
					break
				}
			}
		}
	}

	// Read type byte
	msgType := byte(0)
	if reader.Remaining() >= 1 {
		msgType, _ = reader.ReadByte()
	}

	// Read message text (variable-length string from Sandstorm)
	msgText := ""
	if reader.Remaining() > 4 {
		text, err := readStringFromReader(reader)
		if err == nil {
			msgText = text
		}
	}

	// PS2: optionA and optionB bytes (dialogue choice buttons)
	optionA, optionB := byte(0), byte(0)
	if reader.Remaining() >= 2 {
		optionA, _ = reader.ReadByte()
		optionB, _ = reader.ReadByte()
	}

	// PS2: speakerName (24 bytes → UTF-16)
	speakerName := ""
	if reader.Remaining() >= 24 {
		spkBytes, err := reader.ReadBytes(24)
		if err == nil {
			for i, b := range spkBytes {
				if b == 0 {
					speakerName = string(spkBytes[:i])
					break
				}
			}
		}
	}

	log.Printf("NPCMessage: name=%q type=%d text=%q optA=%d optB=%d speaker=%q",
		npcName, msgType, msgText, optionA, optionB, speakerName)

	// Use speaker name if NPC name is empty
	displayName := npcName
	if displayName == "" && speakerName != "" {
		displayName = speakerName
	}

	if displayName != "" && msgText != "" {
		gs.addChatMessage(fmt.Sprintf("%s says, '%s'", displayName, msgText), ChatSay)
	} else if msgText != "" {
		gs.addChatMessage(msgText, ChatSystem)
	}

	// If options present, show as dialogue
	if optionA != 0 || optionB != 0 {
		log.Printf("NPCMessage: dialogue options A=%d B=%d", optionA, optionB)
	}
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

// handleChatMessage handles opcode 0x07A3 — chat message.
// PS2: sender name(24) UTF-16 + 4-byte metadata + zigzag msg ID.
func (gs *GameState) handleChatMessage(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 4 {
		return
	}
	reader := buffer.NewReader(data[2:])

	// Read sender name (24-byte fixed buffer)
	senderName := ""
	if reader.Remaining() >= 24 {
		nameBytes, err := reader.ReadBytes(24)
		if err == nil {
			for i, b := range nameBytes {
				if b == 0 {
					senderName = string(nameBytes[:i])
					break
				}
			}
		}
	}

	// Read 4-byte metadata — byte 0 is chat channel type
	chatChannel := ChatSay
	if reader.Remaining() >= 4 {
		metaBytes, _ := reader.ReadBytes(4)
		if len(metaBytes) >= 1 {
			switch metaBytes[0] {
			case 0:
				chatChannel = ChatSay
			case 1:
				chatChannel = ChatShout
			case 2:
				chatChannel = ChatGroup
			case 3:
				chatChannel = ChatGuild
			case 4:
				chatChannel = ChatTell
			}
		}
	}

	// Read message ID (zigzag)
	msgID := int64(0)
	if reader.Remaining() >= 1 {
		msgID, _ = reader.Read7BitEncodedInt64()
	}

	// Read message text
	msgText := ""
	if reader.Remaining() > 4 {
		text, err := readStringFromReader(reader)
		if err == nil {
			msgText = text
		}
	}

	channelNames := map[ChatChannel]string{
		ChatSay: "say", ChatShout: "shout", ChatGroup: "group",
		ChatGuild: "guild", ChatTell: "tell",
	}
	log.Printf("ChatMessage[%s]: sender=%q msgID=%d text=%q",
		channelNames[chatChannel], senderName, msgID, msgText)

	if senderName != "" && msgText != "" {
		prefix := ""
		switch chatChannel {
		case ChatGuild:
			prefix = "[Guild] "
		case ChatGroup:
			prefix = "[Group] "
		case ChatShout:
			prefix = "[Shout] "
		case ChatTell:
			prefix = "[Tell] "
		}
		gs.addChatMessage(fmt.Sprintf("%s%s: %s", prefix, senderName, msgText), chatChannel)
	} else if msgText != "" {
		gs.addChatMessage(msgText, chatChannel)
	}
}

// =============================================================================
// Social / Lists (0x0D41/0x0F02/0x1102)
// =============================================================================

// handleMailDelivery handles opcode 0x0D41 — mail system.
func (gs *GameState) handleMailDelivery(msg *protocol.Message) {
	data := msg.Data()
	log.Printf("MailDelivery: dataLen=%d", len(data))

	// Extract any strings for logging
	strs := extractUTF16Strings(data)
	for _, s := range strs {
		log.Printf("MailDelivery: string=%q", s)
	}

	if len(strs) >= 2 {
		mail := MailMessage{
			Sender:  strs[0],
			Subject: strs[1],
		}
		if len(strs) >= 3 {
			mail.Body = strs[2]
		}
		gs.MailMessages = append(gs.MailMessages, mail)
		gs.addChatMessage(fmt.Sprintf("Mail from %s: %s", mail.Sender, mail.Subject), ChatSystem)
	}
}

// handleWhoListFiltered handles opcode 0x0F02 — filtered who results.
func (gs *GameState) handleWhoListFiltered(msg *protocol.Message) {
	data := msg.Data()
	log.Printf("WhoListFiltered: dataLen=%d", len(data))
	// Same format as WhoListResponse — delegate to existing handler
	gs.handleWhoListResponse(msg)
}

// handleIgnoreListResult handles opcode 0x1102 — dual purpose:
// 1. Ignore list display (when no auction session active)
// 2. Auction init response (sub=0x0C open ack, sub=0x0F category ack)
// The first byte after opcode determines the sub-type.
func (gs *GameState) handleIgnoreListResult(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 3 {
		return
	}
	payload := data[2:] // skip opcode
	sub := payload[0]

	log.Printf("0x1102: sub=0x%02X payloadLen=%d hex=%X", sub, len(payload), payload)

	switch {
	case sub == 0x0C:
		// Auction open response — contains session/request ID
		if len(payload) >= 2 {
			gs.AuctionSessionID = payload[1]
			log.Printf("AuctionOpen: sessionID=%d (byte[1]=0x%02X)", gs.AuctionSessionID, gs.AuctionSessionID)
		}
	case sub == 0x0F:
		// Auction category ack
		log.Printf("AuctionCategoryAck: payload=%X", payload)
	default:
		// Ignore list
		strs := extractUTF16Strings(data)
		gs.IgnoreList = gs.IgnoreList[:0]
		for _, s := range strs {
			gs.IgnoreList = append(gs.IgnoreList, s)
		}
		log.Printf("IgnoreListResult: %d entries", len(gs.IgnoreList))
	}
}

// =============================================================================
// Simple / Stub Handlers
// =============================================================================

// handleForceDisconnect handles opcode 0x00EC — force disconnect.
// PS2: debug printf + state transition 8 (disconnect). Shares handler with 0x07A4.
func (gs *GameState) handleForceDisconnect(msg *protocol.Message) {
	data := msg.Data()
	log.Printf("*** FORCE DISCONNECT: opcode=0x00EC dataLen=%d ***", len(data))
	if len(data) > 2 {
		log.Printf("*** DISCONNECT DATA: %X ***", data)
	}
}

// handleStubNOP handles opcode 0x006E — deprecated stub (NOP).
// PS2: func_BDB8F0 = 2-instruction return, then falls through to quest chain.
func (gs *GameState) handleStubNOP(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 4 {
		return
	}
	// Falls through to quest progress chain — parse quest tail
	reader := buffer.NewReader(data[2:])
	gs.parseQuestTail(reader, "StubNOP")
}

// handleEquipItemConfirm handles opcode 0x003F — equip confirmation.
// PS2 wire format: 2 bytes = [invSlot, equipLoc].
// invSlot = array index in ParsedItems. equipLoc = body position assigned.
// For visible equipment, state channel bytes are the source of truth.
// For accessories (no state channel), we update EquipLocation here.
func (gs *GameState) handleEquipItemConfirm(msg *protocol.Message) {
	data := msg.Data()
	if len(data) >= 4 {
		invSlot := data[2]
		equipLoc := data[3]
		log.Printf("EquipItem: invSlot=%d equipLoc=%d", invSlot, equipLoc)
		// Only update accessories (slots without state channel tracking).
		// Visible equipment (armor, weapons, robe) is handled by state channel.
		if isAccessoryEquipLoc(int(equipLoc)) {
			if int(invSlot) < len(gs.ParsedItems) {
				gs.ParsedItems[invSlot].EquipLocation = int64(equipLoc)
				log.Printf("EquipItem: idx %d %q EquipLocation → %d", invSlot, gs.ParsedItems[invSlot].Name, equipLoc)
			}
		}
	} else {
		log.Printf("EquipItem: dataLen=%d", len(data))
	}
}

// handleUnequipItem handles opcode 0x0040 — unequip confirmation.
// PS2 wire format: 1 byte = equipLoc (body position that was unequipped).
func (gs *GameState) handleUnequipItem(msg *protocol.Message) {
	data := msg.Data()
	if len(data) >= 3 {
		equipLoc := data[2]
		log.Printf("UnequipItem: equipLoc=%d", equipLoc)
		// Find item with matching EquipLocation and clear it.
		// Only update accessories. Visible equipment uses state channel.
		if isAccessoryEquipLoc(int(equipLoc)) {
			for i := range gs.ParsedItems {
				if gs.ParsedItems[i].EquipLocation == int64(equipLoc) {
					log.Printf("UnequipItem: idx %d %q EquipLocation %d → -1", i, gs.ParsedItems[i].Name, equipLoc)
					gs.ParsedItems[i].EquipLocation = -1
					break
				}
			}
		}
	} else {
		log.Printf("UnequipItem: dataLen=%d", len(data))
	}
}

// isAccessoryEquipLoc returns true for equip locations NOT tracked by state channel.
func isAccessoryEquipLoc(loc int) bool {
	switch loc {
	case 2, 3: // Earrings
		return true
	case 4: // Neck
		return true
	case 6, 7: // Bracelet
		return true
	case 9, 10: // Rings
		return true
	case 11: // Belt
		return true
	}
	return false
}

// handleArrangeItem handles opcode 0x003D — move/rearrange inventory item.
func (gs *GameState) handleArrangeItem(msg *protocol.Message) {
	data := msg.Data()
	log.Printf("ArrangeItem: dataLen=%d", len(data))
}

// handleLootOptions handles opcode 0x0085 — loot option settings.
func (gs *GameState) handleLootOptions(msg *protocol.Message) {
	data := msg.Data()
	log.Printf("LootOptions: dataLen=%d", len(data))
}

// handleClientCloseLoot handles opcode 0x0016 — close loot window.
func (gs *GameState) handleClientCloseLoot(msg *protocol.Message) {
	log.Printf("ClientCloseLoot: loot window closed")
}

// handlePrepareItem handles opcode 0x000A — equipment hotbar swap confirmation.
// Server response format (from PS2 pcap): slot(uint8) + action(uint8) = 2 bytes
// Action: 0=unequip to hotbar, 2=equip from hotbar, 3=swap hotbar slot.
// Client sends 5 bytes (uint32 slot + action), server confirms with 2 bytes.
func (gs *GameState) handlePrepareItem(msg *protocol.Message) {
	data := msg.Data()
	payload := data[2:] // skip opcode
	if len(payload) < 2 {
		log.Printf("PrepareItem: dataLen=%d (too short)", len(payload))
		return
	}
	slot := payload[0]
	action := payload[1]
	actionName := "unknown"
	switch action {
	case 0:
		actionName = "unequip→hotbar"
	case 2:
		actionName = "equip←hotbar"
	case 3:
		actionName = "swap"
	}
	log.Printf("PrepareItem: slot=%d action=%d(%s)", slot, action, actionName)
}

// handleAdjustItemHP handles opcode 0x0060 — item durability change.
// PS2 pcap confirmed: invSlot(uint32 LE) + remainingHP(uint16 LE) = 6 bytes after opcode.
func (gs *GameState) handleAdjustItemHP(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 8 { // 2 opcode + 4 slot + 2 hp
		log.Printf("AdjustItemHP: dataLen=%d (too short)", len(data))
		return
	}
	invSlot := binary.LittleEndian.Uint32(data[2:6])
	remainingHP := binary.LittleEndian.Uint16(data[6:8])
	log.Printf("AdjustItemHP: invSlot=%d remainingHP=%d", invSlot, remainingHP)
	if int(invSlot) < len(gs.ParsedItems) {
		gs.ParsedItems[invSlot].RemainingHP = int64(remainingHP)
	}
}

// handleBlackSmithMenu handles opcode 0x0061 — blacksmith/repair menu.
// Server sends item list (fragmented) or 4-byte repair cost summary.
func (gs *GameState) handleBlackSmithMenu(msg *protocol.Message) {
	data := msg.Data()
	if len(data) <= 6 {
		// Short form: 4 bytes — total repair cost or blacksmith NPC info.
		if len(data) >= 6 {
			cost := binary.LittleEndian.Uint32(data[2:6])
			gs.BlacksmithRepairCost = int(cost)
			log.Printf("BlackSmithMenu: repairCost=%d", cost)
		}
		gs.BlacksmithOpen = true
		return
	}
	// Long form: item list (same DSP format as MemoryDump).
	log.Printf("BlackSmithMenu: dataLen=%d (item list)", len(data))
	gs.BlacksmithOpen = true
}

// handleCloseBlacksmith handles opcode 0x0063 — close blacksmith window.
func (gs *GameState) handleCloseBlacksmith(msg *protocol.Message) {
	log.Printf("CloseBlacksmith: window closed")
	gs.BlacksmithOpen = false
}

// handleInventoryFull handles opcode 0x00D7 — inventory full notification.
func (gs *GameState) handleInventoryFull(msg *protocol.Message) {
	log.Printf("InventoryFull: inventory is full")
	gs.addChatMessage("Your inventory is full.", ChatError)
}

// handleTradeUI handles opcode 0x0056 — trade window.
func (gs *GameState) handleTradeUI(msg *protocol.Message) {
	data := msg.Data()
	log.Printf("TradeUI: dataLen=%d", len(data))
}

// handleTradeRequest handles opcode 0x0057 — incoming trade request.
func (gs *GameState) handleTradeRequest(msg *protocol.Message) {
	data := msg.Data()
	log.Printf("TradeRequest: dataLen=%d", len(data))
	strs := extractUTF16Strings(data)
	if len(strs) > 0 {
		gs.addChatMessage(fmt.Sprintf("%s wants to trade.", strs[0]), ChatSystem)
	}
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

// handleGuildInviteStuff handles opcode 0x057A — guild invite.
func (gs *GameState) handleGuildInviteStuff(msg *protocol.Message) {
	data := msg.Data()
	log.Printf("GuildInviteStuff: dataLen=%d", len(data))
	strs := extractUTF16Strings(data)
	if len(strs) > 0 {
		gs.addChatMessage(fmt.Sprintf("Guild invite from %s", strs[0]), ChatSystem)
	}
}

// handleBuddyInvite handles opcode 0x076F — buddy invite received.
// handleBuffNotification handles opcode 0x076F — buff/debuff applied.
// PS2: reads buffID(int32) + buffName(24-byte string), shows notification,
// sends acknowledgment opcode 0x0368 back to server.
func (gs *GameState) handleBuffNotification(msg *protocol.Message) {
	data := msg.Data()
	payload := data[2:] // skip opcode
	if len(payload) < 4 {
		return
	}
	buffID := binary.LittleEndian.Uint32(payload[0:4])
	buffName := ""
	if len(payload) >= 28 {
		// 24-byte null-terminated string
		nameBytes := payload[4:28]
		for i, b := range nameBytes {
			if b == 0 {
				buffName = string(nameBytes[:i])
				break
			}
		}
		if buffName == "" {
			buffName = string(nameBytes)
		}
	}
	log.Printf("BuffNotification: id=%d name=%q", buffID, buffName)
	if buffName != "" {
		gs.addChatMessage(fmt.Sprintf("Buff: %s", buffName), ChatSystem)
	}
	// PS2 sends acknowledgment opcode 0x0368 back to server.
	// Build and queue the ack: opcode(2) + buffID(4) + flag(1) + fragment
	if gs.Conn != nil {
		ackData := make([]byte, 5)
		binary.LittleEndian.PutUint32(ackData[0:4], buffID)
		ackData[4] = 1 // ack flag
		gs.Conn.QueueReliable(0x0368, ackData)
		log.Printf("BuffNotification: sent ack 0x0368 for buff %d", buffID)
	}
}

// handleCombatEnd handles opcode 0x0777 — combat end / target clear.
// PS2: no data payload. Resets combat target to -1, clears connection state,
// transitions entity to idle mode. Server sends this when target dies,
// goes out of range, or combat is explicitly stopped.
func (gs *GameState) handleCombatEnd(msg *protocol.Message) {
	log.Printf("CombatEnd: server cleared combat state")
	gs.Player.AttackCount = 0
	gs.PlayerInCombat = false
	gs.CombatEnded = true // consumed by animation manager to clear combat state
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

// handleAddBuddyFailed handles opcode 0x03D8 — add buddy failed.
func (gs *GameState) handleAddBuddyFailed(msg *protocol.Message) {
	log.Printf("AddBuddyFailed: could not add buddy")
	gs.addChatMessage("Could not add buddy.", ChatError)
}

// handleBuddyListStuff handles opcode 0x0771 — buddy list data.
func (gs *GameState) handleBuddyListStuff(msg *protocol.Message) {
	data := msg.Data()
	log.Printf("BuddyListStuff: dataLen=%d", len(data))
}

// handleFactionPage handles opcode 0x07E2 — faction page response.
// Wire format: opcode(2) + byte(0x77) + zigzag(pageID) + byte(pageIndex) +
// byte(0x44) + zigzag(count) + [count × { int32(nameLen) + utf16(name) + zigzag(standing) }]
func (gs *GameState) handleFactionPage(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 8 {
		return
	}
	reader := buffer.NewReader(data[2:]) // skip opcode

	marker, _ := reader.ReadByte() // 0x77
	if marker != 0x77 {
		log.Printf("FactionPage: unexpected marker 0x%02X", marker)
		return
	}

	pageID, _ := reader.Read7BitEncodedInt64()
	pageIndex, _ := reader.ReadByte()
	flag, _ := reader.ReadByte() // 0x44
	_ = flag

	count, _ := reader.Read7BitEncodedInt64()
	if count <= 0 || count > 20 {
		return
	}

	for i := int64(0); i < count; i++ {
		nameLen, err := reader.ReadInt32()
		if err != nil || nameLen <= 0 || nameLen > 100 {
			break
		}
		name, err := reader.ReadStringUnicode(int(nameLen))
		if err != nil {
			break
		}
		standing, err := reader.Read7BitEncodedInt64()
		if err != nil {
			break
		}
		gs.Factions = append(gs.Factions, FactionEntry{
			Name:     name,
			Standing: int(standing),
		})
	}

	log.Printf("FactionPage: pageID=%d pageIdx=%d count=%d total=%d",
		pageID, pageIndex, count, len(gs.Factions))

	// Mark loaded when we've received all 34 pages (pageIndex 66 is last)
	if pageIndex >= 66 {
		gs.FactionLoaded = true
		log.Printf("FactionPage: all %d factions loaded", len(gs.Factions))
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

// handleClassMasteryServer handles opcode 0x1402 — class mastery response.
// Wire format from pcap: type byte determines response format.
func (gs *GameState) handleClassMasteryServer(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 4 {
		return
	}
	payload := data[2:] // skip opcode
	if len(payload) < 1 {
		return
	}

	respType := payload[0]

	switch respType {
	case 0x08:
		// Full CM tree (1154+ bytes)
		gs.parseCMTree(payload)

	case 0x09:
		// Category detail (138-404 bytes) — skill name + description
		gs.parseCMDetail(payload)

	case 0x01:
		// Select ack (9 bytes)
		if len(payload) >= 9 {
			skillID := int(binary.LittleEndian.Uint32(payload[5:9]))
			log.Printf("CM: select ack skillID=0x%04X", skillID)
		}

	case 0x03:
		// Spend confirmation (9 bytes)
		if len(payload) >= 9 {
			remaining := int(binary.LittleEndian.Uint32(payload[5:9]))
			gs.CMUnspent = remaining
			log.Printf("CM: spend confirmed, %d CMs remaining", remaining)
			gs.addChatMessage(fmt.Sprintf("CM spent (%d remaining)", remaining), ChatSystem)
		}

	default:
		log.Printf("CM: unknown response type 0x%02X (%d bytes)", respType, len(payload))
	}
}

// parseCMTree extracts skill entries from the full CM tree response.
func (gs *GameState) parseCMTree(payload []byte) {
	if len(payload) < 7 {
		return
	}
	gs.CMUnspent = int(binary.LittleEndian.Uint16(payload[5:7]))
	gs.CMSkills = nil

	// Scan for skill entries: look for UTF-16 name strings preceded by int32 nameLen
	pos := 7
	for pos < len(payload)-6 {
		// Try to find a name at nearby offsets
		found := false
		for scan := pos; scan < pos+20 && scan < len(payload)-4; scan++ {
			nameLen := int(binary.LittleEndian.Uint32(payload[scan : scan+4]))
			if nameLen >= 3 && nameLen <= 40 && scan+4+nameLen*2 <= len(payload) {
				nameBytes := payload[scan+4 : scan+4+nameLen*2]
				name := decodeUTF16LE(nameBytes)
				if len(name) >= 3 && isPrintableString(name) {
					// Extract header bytes before the name for ID/prereq/cost
					headerStart := pos
					header := payload[headerStart:scan]

					skill := CMSkill{Name: name, HasName: true}

					// Parse header: last 7 bytes before name are typically:
					// skillID(1-2) flags(1) unk(1) prereqID(1-2) cost(1-2)
					if len(header) >= 7 {
						// Per-skill entry: ID rank hasName ID prereq cost
						skill.ID = int(header[len(header)-7])
						skill.Rank = header[len(header)-6]
						skill.PrereqID = int(header[len(header)-3])<<8 | int(header[len(header)-4])
						skill.Cost = int(header[len(header)-1])
					} else if len(header) >= 2 {
						skill.ID = int(header[0])
					}

					gs.CMSkills = append(gs.CMSkills, skill)
					pos = scan + 4 + nameLen*2
					found = true
					break
				}
			}
		}
		if !found {
			pos++
		}
	}

	gs.CMTreeLoaded = true
	log.Printf("CM: tree loaded — %d skills, %d unspent CMs", len(gs.CMSkills), gs.CMUnspent)
}

// parseCMDetail extracts skill detail (name + description) from category response.
func (gs *GameState) parseCMDetail(payload []byte) {
	if len(payload) < 20 {
		return
	}

	skillID := int(binary.LittleEndian.Uint16(payload[5:7]))
	detail := &CMSkillDetail{ID: skillID}

	// Scan for UTF-16 strings: name, display name, description
	strings := extractUTF16Strings16(payload[10:])

	if len(strings) >= 1 {
		detail.Name = strings[0]
	}
	if len(strings) >= 2 {
		detail.DisplayName = strings[1]
	}
	if len(strings) >= 3 {
		detail.Description = strings[2]
	}

	// Rank info is in the last few bytes
	if len(payload) >= 3 {
		detail.Rank = payload[len(payload)-3]
		detail.MaxRank = payload[len(payload)-2]
	}

	gs.CMDetail = detail
	log.Printf("CM: detail skillID=0x%04X name=%q desc=%q rank=%d/%d",
		skillID, detail.Name, detail.Description, detail.Rank, detail.MaxRank)
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

// handleBankUI handles opcode 0x124D — open bank window.
func (gs *GameState) handleBankUI(msg *protocol.Message) {
	data := msg.Data()
	log.Printf("BankUI: dataLen=%d", len(data))
	if len(data) > 2 {
		gs.parseAndLogItems("BankUI", data)
	}
}

// handleConfirmBankTunar handles opcode 0x1253 — bank tunar update.
func (gs *GameState) handleConfirmBankTunar(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 6 {
		return
	}
	tunar := int64(0)
	reader := buffer.NewReader(data[2:])
	tunar, _ = reader.Read7BitEncodedInt64()
	log.Printf("ConfirmBankTunar: tunar=%d", tunar)
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

		// 23 zigzag item fields (func_00CBACE8)
		ai.Item.ItemID, _ = reader.Read7BitEncodedInt64()
		ai.Item.ItemCost, _ = reader.Read7BitEncodedInt64()
		ai.Item.Unk1, _ = reader.Read7BitEncodedInt64()
		ai.Item.ItemIcon, _ = reader.Read7BitEncodedInt64()
		ai.Item.Unk2, _ = reader.Read7BitEncodedInt64()
		ai.Item.Slot, _ = reader.Read7BitEncodedInt64()
		ai.Item.Unk3, _ = reader.Read7BitEncodedInt64()
		ai.Item.NoTrade, _ = reader.Read7BitEncodedInt64()
		ai.Item.NoRent, _ = reader.Read7BitEncodedInt64()
		ai.Item.Unk4, _ = reader.Read7BitEncodedInt64()
		ai.Item.AttackType, _ = reader.Read7BitEncodedInt64()
		ai.Item.WeaponDamage, _ = reader.Read7BitEncodedInt64()
		ai.Item.Unk5, _ = reader.Read7BitEncodedInt64()
		ai.Item.LevelReq, _ = reader.Read7BitEncodedInt64()
		ai.Item.MaxStack, _ = reader.Read7BitEncodedInt64()
		ai.Item.MaxHP, _ = reader.Read7BitEncodedInt64()
		ai.Item.Duration, _ = reader.Read7BitEncodedInt64()
		ai.Item.ClassUse, _ = reader.Read7BitEncodedInt64()
		ai.Item.RaceUse, _ = reader.Read7BitEncodedInt64()
		ai.Item.ProcAnim, _ = reader.Read7BitEncodedInt64()
		ai.Item.IsLore, _ = reader.Read7BitEncodedInt64()
		ai.Item.Unk6, _ = reader.Read7BitEncodedInt64()
		ai.Item.IsCraft, _ = reader.Read7BitEncodedInt64()

		// Name (uint32 charCount + UTF-16) — blob_read format
		nameLen, err := reader.ReadInt32()
		if err != nil {
			log.Printf("AuctionStuff[%d]: name read error at pos %d: %v", i, reader.Position(), err)
			break
		}
		if nameLen > 0 && nameLen < 200 {
			ai.Item.Name, _ = reader.ReadStringUnicode(int(nameLen))
		} else if nameLen != 0 {
			log.Printf("AuctionStuff[%d]: bad name len %d at pos %d seller=%q itemID=%d",
				i, nameLen, reader.Position(), ai.Seller, auctionItemID)
			break
		}

		// Description
		descLen, err := reader.ReadInt32()
		if err != nil {
			log.Printf("AuctionStuff[%d]: desc read error at pos %d", i, reader.Position())
			break
		}
		if descLen > 0 && descLen < 500 {
			ai.Item.Desc, _ = reader.ReadStringUnicode(int(descLen))
		} else if descLen != 0 {
			log.Printf("AuctionStuff[%d]: bad desc len %d at pos %d name=%q",
				i, descLen, reader.Position(), ai.Item.Name)
			break
		}

		// Stats (zigzag count + pairs)
		statSize, _ := reader.Read7BitEncodedInt64()
		if statSize > 0 && statSize < 50 {
			ai.Item.Stats = make(map[int]int64)
			for j := int64(0); j < statSize; j++ {
				idx, _ := reader.Read7BitEncodedInt64()
				val, e := reader.Read7BitEncodedInt64()
				if e != nil {
					break
				}
				ai.Item.Stats[int(idx)] = val
			}
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

// handleAdminShutdown handles opcode 0x07C1 — server shutdown announcement.
func (gs *GameState) handleAdminShutdown(msg *protocol.Message) {
	log.Printf("*** SERVER SHUTDOWN ANNOUNCED ***")
	gs.addChatMessage("Server is shutting down!", ChatSystem)
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
