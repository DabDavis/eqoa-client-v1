// Package protocol contains EQOA DRDP protocol types and functions.
// This is a client-side copy of the essential types from the server's
// internal/protocol package, since Go's internal package convention
// prevents cross-module imports.
package protocol

import (
	"errors"
	"fmt"

	"github.com/DabDavis/eqoa-esf-tools/pkg/buffer"
)

// ========================================================================
// Opcodes
// ========================================================================

// GameOpcode represents a game protocol opcode.
type GameOpcode uint16

// PacketHeader special values.
const (
	PacketHeaderNewConnection uint16 = 0xFFFE
)

const (
	GameVersionEQOAFrontiers = 0x25
	GameVersionEQOAVanilla   = 0x12
)

// =============================================================================
// 0x00XX - Core Game Opcodes
// =============================================================================

const (
	// 0x000X - Basic operations
	OpcodeDiscVersion      GameOpcode = 0x0000 // Game Version check
	OpcodeAuthenticate2    GameOpcode = 0x0001 // Username/Auth (game server)
	OpcodeInteract         GameOpcode = 0x0004 // NPC/Object interaction
	OpcodeSendReport       GameOpcode = 0x0009 // Send Report (client)
	OpcodePrepareItem      GameOpcode = 0x000A // Prepare Item on Hotbar
	OpcodeClientSayChat    GameOpcode = 0x000B // Chat Input from Client
	OpcodeMemoryDump       GameOpcode = 0x000D // Memory Dump (world enter)
	OpcodeChangeChatMode   GameOpcode = 0x000E // Change chat mode
	OpcodeAttack           GameOpcode = 0x000F // Auto Attack

	// 0x001X - Targeting, Time, Loot
	OpcodeClassicTargetInfo    GameOpcode = 0x0010 // Target Info
	OpcodeRandomName           GameOpcode = 0x0012 // Random name
	OpcodeTime                 GameOpcode = 0x0013 // Epoch Time
	OpcodeCharacterInWorld     GameOpcode = 0x0014 // Char in world
	OpcodeLootCorpse           GameOpcode = 0x0015 // Loot Corpse (client)
	OpcodeClientCloseLoot      GameOpcode = 0x0016 // Close loot (client)
	OpcodeLoot                 GameOpcode = 0x0018 // Loot Window (server)
	OpcodeClientLoot           GameOpcode = 0x0019 // Loot item (client)
	OpcodeLootResult           GameOpcode = 0x001A // Loot outcome (server)
	OpcodeNPCInteraction       GameOpcode = 0x001C // NPC interaction response (server)
	OpcodeUpdateTrainingPoints GameOpcode = 0x001D // Training Points

	// 0x002X - Character/XP
	OpcodeGrantXP         GameOpcode = 0x0020 // Receive XP
	OpcodePrepareWeapon   GameOpcode = 0x0021 // Prepare Weapon
	OpcodeSelectedChar    GameOpcode = 0x002A // Char Select (client)
	OpcodeCreateCharacter GameOpcode = 0x002B // Char Create (client)
	OpcodeCharacterSelect GameOpcode = 0x002C // Char List (server)
	OpcodeDelCharacter    GameOpcode = 0x002D // Char Delete
	OpcodeWorldEntry      GameOpcode = 0x002E // Char select → in-world (server)
	OpcodeNameTaken       GameOpcode = 0x002F // Name taken

	// 0x003X - Skills, Quests, Inventory
	OpcodeUseSkill          GameOpcode = 0x0032 // Use Skill (client)
	OpcodeAddSkillToBar     GameOpcode = 0x0033 // Add Skill to Bar (client)
	OpcodeQuestPopupMulti    GameOpcode = 0x0034 // Quest Popup Multi (server)
	OpcodeDialogueBoxOption  GameOpcode = 0x0035 // Popup selection (client)
	OpcodeQuestStageComplete GameOpcode = 0x0036 // Quest reward (server)
	OpcodeAddInvItem        GameOpcode = 0x003B // Add Inventory Item (server)
	OpcodeRemoveInvItem     GameOpcode = 0x003C // Remove Inv Item (client)
	OpcodeArrangeItem       GameOpcode = 0x003D // Move Inv Item (client)
	OpcodeInteractItem      GameOpcode = 0x003E // Equip/Use Item (client)
	OpcodeEquipItem         GameOpcode = 0x003F // Equip confirm (server)

	// 0x004X - Pets, Quests, Merchants
	OpcodeUnequipItem  GameOpcode = 0x0040 // Unequip item
	OpcodePetCommands  GameOpcode = 0x0041 // Pet Commands
	OpcodeQuestDialogueResp GameOpcode = 0x0045 // Quest dialogue response (server)
	OpcodeDialogueBox       GameOpcode = 0x0046 // Quest Popup (server)
	OpcodeEnableChannel GameOpcode = 0x0049 // Enable channel
	OpcodeMerchantBuy  GameOpcode = 0x004A // Buy from Merchant
	OpcodeMerchantSell GameOpcode = 0x004B // Sell to Merchant
	OpcodeMerchantDiag GameOpcode = 0x004C // Merchant Interact

	// 0x005X - Trade, Tunar, Death
	OpcodePlayerTunar  GameOpcode = 0x0052 // Update Tunar (server)
	OpcodeTradeUI      GameOpcode = 0x0056 // Trade UI
	OpcodeTradeRequest GameOpcode = 0x0057 // Trade Request (client)
	OpcodeCharacterDied GameOpcode = 0x005E // Character died

	// 0x006X - Items, Durability, Blacksmith, Hotkeys
	OpcodeAdjustItemHP        GameOpcode = 0x0060 // Item Durability
	OpcodeBlackSmithMenu      GameOpcode = 0x0061 // Blacksmith Window
	OpcodeRequestRepair       GameOpcode = 0x0062 // Repair Item
	OpcodeCloseBlacksmithMenu GameOpcode = 0x0063 // Close Blacksmith
	OpcodeItemListUpdate      GameOpcode = 0x0065 // Full item definition (server)
	OpcodeHotkeyMenu          GameOpcode = 0x006C // Hotkey Menu (server)
	OpcodeStubNOP             GameOpcode = 0x006E // Deprecated stub (NOP)

	// 0x007X - Quests, Pets
	OpcodeUpdateQuestProgress GameOpcode = 0x007B // Quest progress update (server)
	OpcodeAddQuestLog         GameOpcode = 0x007C // Add Quest (server)
	OpcodeDeleteQuest         GameOpcode = 0x007D // Remove Quest
	OpcodeAddPetToGroup       GameOpcode = 0x007E // Add Pet to Group
	OpcodeClearQuestTracker   GameOpcode = 0x007F // Clear quest tracker (server)

	// 0x008X
	OpcodeLootOptions GameOpcode = 0x0085 // Loot options

	// 0x00BX - Combat, Skills, Merchants, Entity
	OpcodeCastingCombat    GameOpcode = 0x00B1 // Casting Combat
	OpcodeTeleport         GameOpcode = 0x00B2 // Teleport
	OpcodeFullEntityCreate GameOpcode = 0x00B3 // Master entity creation (server)
	OpcodeSkillBarAck      GameOpcode = 0x00B4 // Skill Bar Ack (server)
	OpcodeEntityUpdate     GameOpcode = 0x00B5 // Entity property update (server)
	OpcodeMerchantBox      GameOpcode = 0x00B7 // Merchant Window
	OpcodeEntityDetailUpdate GameOpcode = 0x00B8 // Entity detail refresh (server)

	// 0x00CX - Faction, Weather, Cooldowns, Errors
	OpcodeWeatherControl  GameOpcode = 0x00C5 // Weather
	OpcodeSkyboxWeather   GameOpcode = 0x00C7 // Skybox Weather
	OpcodeRollRequest     GameOpcode = 0x00C8 // Roll Request
	OpcodeFactionMessages GameOpcode = 0x00C9 // Faction messages
	OpcodeLootMessages    GameOpcode = 0x00CA // Loot messages
	OpcodeCooldownTimer   GameOpcode = 0x00CD // Cooldown Timer
	OpcodeErrorMessage    GameOpcode = 0x00CE // Error Message

	// 0x00DX/EX/FX - Damage, NPC, Entity, Misc
	OpcodeNPCMessage          GameOpcode = 0x00D3 // NPC/server say message (server)
	OpcodeInventoryFull       GameOpcode = 0x00D7 // Inventory full
	OpcodeHPResourceUpdate    GameOpcode = 0x00D9 // HP/resource update (server)
	OpcodeDamageIndication    GameOpcode = 0x00DB // Damage/Heal
	OpcodeForceDisconnect     GameOpcode = 0x00EC // Force disconnect (server)
	OpcodePlayerSpeed         GameOpcode = 0x00F8 // Player speed
	OpcodeEntityStateRefresh  GameOpcode = 0x00FC // Lightweight entity refresh (server)
	OpcodeEntityDataUpdate    GameOpcode = 0x00FD // Entity data with validation (server)
	OpcodeClearSpellBar       GameOpcode = 0x00FE // Clear all spells from hotbar (client, 4 bytes)
	OpcodeFullEntityRefresh   GameOpcode = 0x00FF // Complete entity property reset (server)
)

// =============================================================================
// 0x03XX - Buddy List
// =============================================================================

const (
	OpcodeBuddyRemove       GameOpcode = 0x0369 // Buddy remove (client)
	OpcodeBuddyListEntry    GameOpcode = 0x03D5 // Buddy entry (server)
	OpcodeCollectionRemove  GameOpcode = 0x03D6 // PS2: zigzag key → tree remove (inventory/quest/buddy)
	OpcodeBuddyListFull     GameOpcode = 0x03D7 // Buddy list full
	OpcodeAddBuddyFailed    GameOpcode = 0x03D8 // Add buddy failed
)

// =============================================================================
// 0x05XX - Guilds
// =============================================================================

const (
	OpcodeGuildInfo        GameOpcode = 0x0468 // Guild info + member list (server→client)
	OpcodeGuildCreate      GameOpcode = 0x0469 // Create guild (client→server)
	OpcodeGuildInvite      GameOpcode = 0x046A // Invite player (client→server)
	OpcodeGuildLeave       GameOpcode = 0x046B // Leave guild (client→server)
	OpcodeGuildKick        GameOpcode = 0x046C // Kick member (client→server)
	OpcodeGuildPromote     GameOpcode = 0x046D // Promote member (client→server)
	OpcodeGuildDemote      GameOpcode = 0x046E // Demote member (client→server)
	OpcodeGuildSetMOTD     GameOpcode = 0x046F // Set MOTD (client→server)
	OpcodeGuildWorldEntry  GameOpcode = 0x0579 // Full world entry with guild init (server)
	OpcodeGuildInviteStuff GameOpcode = 0x057A // Guild invite stuff
)

// =============================================================================
// 0x06XX - Groups
// =============================================================================

const (
	OpcodeAcceptGroupInvite  GameOpcode = 0x0622 // Accept Group
	OpcodeLeaveGroup         GameOpcode = 0x0623 // Leave Group
	OpcodeDeclineGroupInvite GameOpcode = 0x0624 // Decline Group
	OpcodeGroupInvite        GameOpcode = 0x0625 // Group Invite (client)
	OpcodeBootGroupMember    GameOpcode = 0x0626 // Boot Member
	OpcodeDisbandGroup       GameOpcode = 0x0627 // Disband Group
	OpcodeServerDisbandGroup GameOpcode = 0x062F // Server Disband
	OpcodeRemoveGroupMember  GameOpcode = 0x069A // Remove Member
	OpcodeGroupStuff         GameOpcode = 0x069C // Group Stuff
	OpcodeGroupChat          GameOpcode = 0x069E // Group Chat
)

// =============================================================================
// 0x07XX - Server Messages, Camera, Errors
// =============================================================================

const (
	OpcodeAdminCode                     GameOpcode = 0x0700 // Admin Code
	OpcodeServerLoginResponse           GameOpcode = 0x0728 // Post-auth world entry (server)
	OpcodeNPCInteractResult             GameOpcode = 0x0729 // PS2: NPC interaction result (entityID+actionType+name)
	OpcodeNotSubscribed                 GameOpcode = 0x072B // Not Subscribed
	OpcodeReconnectWorldEntry           GameOpcode = 0x0758 // Reconnect world entry (server)
	OpcodeTarget                        GameOpcode = 0x0760 // Target
	OpcodeTargetInformation             GameOpcode = 0x0761 // Target Info
	OpcodeBuffNotification              GameOpcode = 0x076F // PS2: Buff/debuff applied (buffID+name, sends ack 0x0368)
	OpcodeGroupInviteAcceptedMessage    GameOpcode = 0x0770 // Group Accept Msg
	OpcodeBuddyListStuff                GameOpcode = 0x0771 // Buddy List Stuff
	OpcodeCreateGroup                   GameOpcode = 0x0772 // Display Group
	OpcodeNPCInteractResult2            GameOpcode = 0x0773 // PS2: NPC interaction result (same handler as 0x0729)
	OpcodeResourceUpdate                GameOpcode = 0x0775 // Resource Update
	OpcodeCombatEnd                     GameOpcode = 0x0777 // PS2: Combat end / target clear (no data, resets combat)
	OpcodeOutMatched                    GameOpcode = 0x077A // Outmatched
	OpcodeStatsUpdate                   GameOpcode = 0x0790 // PS2: XP/stats update (int64+int32+int64, triggers UI refresh)
	OpcodeEntityUpdateAudio             GameOpcode = 0x07A1 // Entity update + stop audio (server)
	OpcodeEntityUpdateMinimal           GameOpcode = 0x07A2 // Entity update minimal (server)
	OpcodeChatMessage                   GameOpcode = 0x07A3 // Chat message (server)
	OpcodeLoggedInFromAnotherLocationDC GameOpcode = 0x07A4 // Dup Login DC
	OpcodeZoneTransferInit              GameOpcode = 0x07B0 // Zone transfer + loading (server)
	OpcodeZoneTransferConfirm           GameOpcode = 0x07B2 // Zone transfer complete (server)
	OpcodeGameServers                   GameOpcode = 0x07B3 // Server Listing
	OpcodeEntityUpdateDirect            GameOpcode = 0x07C0 // Direct entity update (server)
	OpcodeAdministratorShutdown         GameOpcode = 0x07C1 // Admin Shutdown
	OpcodeBestEffortPing                GameOpcode = 0x07D0 // Ping
	OpcodeCamera1                       GameOpcode = 0x07D1 // Camera 1
	OpcodeCharacterModifiedDisconnect   GameOpcode = 0x07D2 // Char Modified DC
	OpcodeServerReady                   GameOpcode = 0x07E0 // Server ready/keepalive (no payload)
	OpcodeFactionPage                   GameOpcode = 0x07E2 // Faction Page
	OpcodeEntityUpdateMapPanel          GameOpcode = 0x07F0 // Entity update + map panel (server)
	OpcodeEntityUpdateMapPanel2         GameOpcode = 0x07F1 // Same as 0x07F0
	OpcodeWorldUnavailable              GameOpcode = 0x07F3 // World Unavailable
	OpcodeEntityUpdateFullUI            GameOpcode = 0x07F4 // Entity update + full UI (server)
	OpcodeCamera2                       GameOpcode = 0x07F5 // Camera 2
)

// =============================================================================
// 0x08XX+ - LFG, Auth, Chat, Ignore, Bank, Mastery, Duel
// =============================================================================

const (
	OpcodeLFGShowHide   GameOpcode = 0x0801 // LFG Show/Hide
	OpcodeLFGUnknown    GameOpcode = 0x0840 // LFG Unknown
	OpcodeAuthenticate  GameOpcode = 0x0904 // Auth (login server)
	OpcodeDisconnectClient GameOpcode = 0x09B0 // Disconnect
	OpcodeClientMessage GameOpcode = 0x0A7A // Client Message
	OpcodeColoredChat   GameOpcode = 0x0A7B // Colored Chat
	OpcodeClientFaction GameOpcode = 0x0AC4 // View Faction (client)
	OpcodeClientShout   GameOpcode = 0x0C01 // Client Shout
	OpcodeMailDelivery    GameOpcode = 0x0D41 // Mail system (server)
	OpcodeWhoList         GameOpcode = 0x0E01 // Who List (client)
	OpcodeWhoListResponse GameOpcode = 0x0E02 // Who List (server)
	OpcodeWhoListFiltered GameOpcode = 0x0F02 // Filtered who results (server)
	OpcodeIgnoreList      GameOpcode = 0x1005 // Ignore List
	OpcodeIgnoreListResult GameOpcode = 0x1102 // Ignore list display (server)
	OpcodeAuctionRequest GameOpcode = 0x1205 // Auction Request (client)
	OpcodeAuctionStuff   GameOpcode = 0x1206 // Auction Item (server)
	OpcodeBankUI        GameOpcode = 0x124D // Bank UI
	OpcodeRemoveBankItem GameOpcode = 0x1250 // Withdraw Bank
	OpcodeAddBankItem   GameOpcode = 0x1251 // Add Bank Item
	OpcodeConfirmBankTunar GameOpcode = 0x1253 // Bank Tunar
	OpcodeBankItem      GameOpcode = 0x1254 // Bank Item (client)
	OpcodeDepositBankTunar GameOpcode = 0x1255 // Deposit Tunar (client)
	OpcodeClassMastery  GameOpcode = 0x1401 // Class Mastery (client)
	OpcodeClassMasteryServer GameOpcode = 0x1402 // Class Mastery (server)
	OpcodeDuelRequest   GameOpcode = 0x1501 // Duel Request
)

// opcodeNames maps opcode values to their human-readable names.
var opcodeNames = map[GameOpcode]string{
	0x0000: "DiscVersion", 0x0001: "Authenticate2", 0x0004: "Interact",
	0x0009: "SendReport", 0x000A: "PrepareItem", 0x000B: "ClientSayChat",
	0x000D: "MemoryDump", 0x000E: "ChangeChatMode", 0x000F: "Attack",
	0x0010: "ClassicTargetInfo", 0x0012: "RandomName", 0x0013: "Time",
	0x0014: "CharacterInWorld", 0x0015: "LootCorpse", 0x0016: "ClientCloseLoot",
	0x0018: "Loot", 0x0019: "ClientLoot", 0x001A: "LootResult",
	0x001C: "NPCInteraction", 0x001D: "UpdateTrainingPoints",
	0x0020: "GrantXP", 0x0021: "PrepareWeapon",
	0x002A: "SelectedChar", 0x002B: "CreateCharacter", 0x002C: "CharacterSelect",
	0x002D: "DelCharacter", 0x002E: "WorldEntry", 0x002F: "NameTaken",
	0x0032: "UseSkill", 0x0033: "AddSkillToBar",
	0x0034: "QuestPopupMulti", 0x0035: "DialogueBoxOption", 0x0036: "QuestStageComplete",
	0x003B: "AddInvItem", 0x003C: "RemoveInvItem", 0x003D: "ArrangeItem",
	0x003E: "InteractItem", 0x003F: "EquipItem",
	0x0040: "UnequipItem", 0x0041: "PetCommands", 0x0045: "QuestDialogueResp",
	0x0046: "DialogueBox",
	0x0049: "EnableChannel", 0x004A: "MerchantBuy", 0x004B: "MerchantSell",
	0x004C: "MerchantDiag",
	0x0052: "PlayerTunar", 0x0056: "TradeUI", 0x0057: "TradeRequest",
	0x005E: "CharacterDied",
	0x0060: "AdjustItemHP", 0x0061: "BlackSmithMenu", 0x0062: "RequestRepair",
	0x0063: "CloseBlacksmith", 0x0065: "ItemListUpdate",
	0x006C: "HotkeyMenu", 0x006E: "StubNOP",
	0x007B: "UpdateQuestProgress", 0x007C: "AddQuestLog", 0x007D: "DeleteQuest",
	0x007E: "AddPetToGroup", 0x007F: "ClearQuestTracker",
	0x0085: "LootOptions",
	0x00B1: "CastingCombat", 0x00B2: "Teleport", 0x00B3: "FullEntityCreate",
	0x00B4: "SkillBarAck", 0x00B5: "EntityUpdate", 0x00B7: "MerchantBox",
	0x00B8: "EntityDetailUpdate",
	0x00C5: "WeatherControl", 0x00C7: "SkyboxWeather", 0x00C8: "RollRequest",
	0x00C9: "FactionMessages", 0x00CA: "LootMessages", 0x00CD: "CooldownTimer",
	0x00CE: "ErrorMessage",
	0x00D3: "NPCMessage", 0x00D7: "InventoryFull", 0x00D9: "HPResourceUpdate",
	0x00DB: "DamageIndication", 0x00EC: "ForceDisconnect",
	0x00F8: "PlayerSpeed", 0x00FC: "EntityStateRefresh",
	0x00FD: "EntityDataUpdate", 0x00FE: "ClearSpellBar", 0x00FF: "FullEntityRefresh",
	0x0369: "BuddyRemove", 0x03D5: "BuddyListEntry", 0x03D6: "CollectionRemove",
	0x03D7: "BuddyListFull", 0x03D8: "AddBuddyFailed",
	0x0468: "GuildInfo", 0x0579: "GuildWorldEntry",
	0x057A: "GuildInviteStuff",
	0x0622: "AcceptGroupInvite", 0x0623: "LeaveGroup", 0x0624: "DeclineGroupInvite",
	0x0625: "GroupInvite", 0x0626: "BootGroupMember", 0x0627: "DisbandGroup",
	0x062F: "ServerDisbandGroup", 0x069A: "RemoveGroupMember",
	0x069C: "GroupStuff", 0x069E: "GroupChat",
	0x0700: "AdminCode", 0x0728: "ServerLoginResponse",
	0x0729: "NPCInteractResult", 0x072B: "NotSubscribed",
	0x0758: "ReconnectWorldEntry",
	0x0760: "Target", 0x0761: "TargetInformation",
	0x076F: "BuffNotification", 0x0770: "GroupInviteAccepted",
	0x0771: "BuddyListStuff", 0x0772: "CreateGroup", 0x0773: "NPCInteractResult2",
	0x0775: "ResourceUpdate", 0x0777: "CombatEnd", 0x077A: "OutMatched",
	0x0790: "StatsUpdate",
	0x07A1: "EntityUpdateAudio", 0x07A2: "EntityUpdateMinimal",
	0x07A3: "ChatMessage", 0x07A4: "LoggedInElsewhereDC",
	0x07B0: "ZoneTransferInit", 0x07B2: "ZoneTransferConfirm",
	0x07B3: "GameServers", 0x07C0: "EntityUpdateDirect",
	0x07C1: "AdminShutdown", 0x07D0: "BestEffortPing",
	0x07D1: "Camera1", 0x07D2: "CharModifiedDC", 0x07E0: "ServerReady",
	0x07E2: "FactionPage",
	0x07F0: "EntityUpdateMapPanel", 0x07F1: "EntityUpdateMapPanel2",
	0x07F3: "WorldUnavailable", 0x07F4: "EntityUpdateFullUI", 0x07F5: "Camera2",
	0x0801: "LFGShowHide", 0x0840: "LFGUnknown",
	0x0904: "Authenticate", 0x09B0: "DisconnectClient",
	0x0A7A: "ClientMessage", 0x0A7B: "ColoredChat", 0x0AC4: "ClientFaction",
	0x0C01: "ClientShout", 0x0D41: "MailDelivery",
	0x0E01: "WhoList", 0x0E02: "WhoListResponse",
	0x0F02: "WhoListFiltered", 0x1005: "IgnoreList", 0x1102: "IgnoreListResult",
	0x1205: "AuctionRequest", 0x1206: "AuctionStuff", 0x124D: "BankUI", 0x1250: "RemoveBankItem",
	0x1251: "AddBankItem", 0x1253: "ConfirmBankTunar",
	0x1254: "BankItem", 0x1255: "DepositBankTunar",
	0x1401: "ClassMastery", 0x1402: "ClassMasteryServer",
	0x1501: "DuelRequest",
}

// String returns the string representation of an opcode.
func (op GameOpcode) String() string {
	if name, ok := opcodeNames[op]; ok {
		return name
	}
	return "Unknown"
}

// ========================================================================
// Message Types
// ========================================================================

// MessageType represents the type of a network message.
type MessageType byte

const (
	MessageTypeClientUpdate    MessageType = 0x40
	MessageTypeStatUpdate      MessageType = 0x42
	MessageTypeBuffUpdate      MessageType = 0x43
	MessageTypePing            MessageType = 0xF9
	MessageTypeSegmentReliable MessageType = 0xFA
	MessageTypeReliable        MessageType = 0xFB
	MessageTypeUnreliable      MessageType = 0xFC
)

// IsReliable returns true if the message type requires reliable delivery.
func (mt MessageType) IsReliable() bool {
	return mt == MessageTypeReliable || mt == MessageTypeSegmentReliable || mt == MessageTypePing
}

// IsStateChannel returns true if the message type is a state channel (0x00-0xF7).
func (mt MessageType) IsStateChannel() bool {
	return mt <= 0xF7
}

// ========================================================================
// Message
// ========================================================================

const (
	MaxMessageSize = 0x07FE
)

var (
	ErrMessageTooLarge    = errors.New("message size exceeds maximum")
	ErrInvalidMessageType = errors.New("invalid message type")
	ErrInvalidChannel     = errors.New("invalid state channel")
)

// Message represents a network message.
type Message struct {
	Type     MessageType
	Opcode   GameOpcode
	Sequence uint16
	XORByte  byte
	data     []byte
	size     int
}

// Data returns the message data.
func (m *Message) Data() []byte {
	return m.data[:m.size]
}

// SetData copies data into the message.
func (m *Message) SetData(data []byte) {
	if len(data) > cap(m.data) {
		m.data = make([]byte, len(data))
	}
	m.size = copy(m.data, data)
}

// Size returns the data size.
func (m *Message) Size() int {
	return m.size
}

// ========================================================================
// Segment Header
// ========================================================================

// SegmentHeaderFlags contains flags for the segment header.
type SegmentHeaderFlags uint32

const (
	SegmentFlagIsRemote          SegmentHeaderFlags = 0x00000800 // remote_endpoint varint inline
	SegmentFlagClientInitiated   SegmentHeaderFlags = 0x00001000 // local_endpoint = src_addr (no varint)
	SegmentFlagHasInstance       SegmentHeaderFlags = 0x00002000
	SegmentFlagDidServerInitiate SegmentHeaderFlags = 0x00004000
	SegmentFlagSrcAddrInline     SegmentHeaderFlags = 0x00008000 // src_addr is 8-byte long inline
	SegmentFlagResetConnection   SegmentHeaderFlags = 0x00010000
	SegmentFlagData0x40000       SegmentHeaderFlags = 0x00040000 // extra uint32 in header
	SegmentFlagNewInstance       SegmentHeaderFlags = 0x00080000

	SegmentHeaderFlagsMask SegmentHeaderFlags = 0xFFFFF800
	SegmentHeaderSizeMask  uint64             = 0x7FF
)

// SegmentHeader represents the header portion of a network segment.
type SegmentHeader struct {
	Flags         SegmentHeaderFlags
	Size          int
	Instance      uint32
	LocalEndpoint uint64 // Server's local_endpoint (when NOT CLIENT_INITIATED)
}

// Read parses a segment header from the buffer reader.
// Field order matches Java SegmentHeader.read(ByteBuf, long, long):
//  1. varint(flags|size)
//  2. instance      — if 0x2000 (HasInstance)
//  3. data_0x40000  — if 0x40000 (extra uint32)
//  4. src_addr      — if 0x8000 (8-byte long inline)
//  5. local_endpoint — if NOT 0x1000 (ClientInitiated) — varint64
//  6. remote_endpoint — if 0x0800 (IsRemote) — varint64
func (h *SegmentHeader) Read(reader *buffer.Reader) error {
	flagsLen, err := reader.Read7BitEncodedUInt64()
	if err != nil {
		return err
	}

	h.Flags = SegmentHeaderFlags(flagsLen & uint64(SegmentHeaderFlagsMask))
	h.Size = int(flagsLen & SegmentHeaderSizeMask)

	// 2. Instance ID (flag 0x2000)
	if h.Flags&SegmentFlagHasInstance != 0 {
		h.Instance, err = reader.ReadUint32()
		if err != nil {
			return err
		}
	}

	// 3. Data 0x40000 — extra uint32 (used in reset packets)
	if h.Flags&SegmentFlagData0x40000 != 0 {
		_, err = reader.ReadUint32()
		if err != nil {
			return err
		}
	}

	// 4. Src addr inline (flag 0x8000) — 8-byte long
	if h.Flags&SegmentFlagSrcAddrInline != 0 {
		_, err = reader.ReadUint32() // low 4 bytes
		if err != nil {
			return err
		}
		_, err = reader.ReadUint32() // high 4 bytes
		if err != nil {
			return err
		}
	}

	// 5. Local endpoint (NOT CLIENT_INITIATED) — varint64
	if h.Flags&SegmentFlagClientInitiated == 0 {
		h.LocalEndpoint, err = reader.Read7BitEncodedUInt64()
		if err != nil {
			return err
		}
	}

	// 6. Remote endpoint (flag 0x0800) — varint64
	if h.Flags&SegmentFlagIsRemote != 0 {
		_, err = reader.Read7BitEncodedUInt64()
		if err != nil {
			return err
		}
	}

	return nil
}

// ========================================================================
// Segment Body
// ========================================================================

// SegmentBodyFlags contains flags for the segment body.
type SegmentBodyFlags byte

const (
	BodyFlagFlushAck         SegmentBodyFlags = 0x01
	BodyFlagGuaranteedAck    SegmentBodyFlags = 0x02
	BodyFlagUnknownPacked    SegmentBodyFlags = 0x04
	BodyFlagGuaranteedAckMask SegmentBodyFlags = 0x08
	BodyFlagClientUpdateAck  SegmentBodyFlags = 0x10
	BodyFlagRdpMessage       SegmentBodyFlags = 0x20
	BodyFlagSessionAck       SegmentBodyFlags = 0x40
)

// StateAck represents an acknowledgment for a state channel.
type StateAck struct {
	Channel byte
	Ack     uint16
}

// SegmentBody represents the body portion of a network segment.
type SegmentBody struct {
	Flags             SegmentBodyFlags
	InstanceLocal     int32
	ArrivalNumber     uint16
	FlushAck          uint16
	GuaranteedAck     uint16
	GuaranteedAckMask int64
	StateAcks         []StateAck
	Messages          []*Message
}

// NewSegmentBody creates a new empty segment body.
func NewSegmentBody() *SegmentBody {
	return &SegmentBody{
		StateAcks: make([]StateAck, 0, 24),
		Messages:  make([]*Message, 0, 8),
	}
}

// Read parses the segment body from the buffer reader.
func (b *SegmentBody) Read(reader *buffer.Reader, header *SegmentHeader) error {
	bodyStart := reader.Position()

	// Read body flags
	flags, err := reader.ReadByte()
	if err != nil {
		return err
	}
	b.Flags = SegmentBodyFlags(flags)

	// Read session ack if present
	if b.Flags&BodyFlagSessionAck != 0 {
		val, err := reader.ReadInt32()
		if err != nil {
			return err
		}
		b.InstanceLocal = val
	}

	// Read arrival number
	b.ArrivalNumber, err = reader.ReadUint16()
	if err != nil {
		return err
	}

	// Read flush ack
	if b.Flags&BodyFlagFlushAck != 0 {
		b.FlushAck, err = reader.ReadUint16()
		if err != nil {
			return err
		}

		if b.Flags&BodyFlagUnknownPacked != 0 {
			_, err = reader.Read7BitEncodedInt64()
			if err != nil {
				return err
			}
		}
	}

	// Read guaranteed ack
	if b.Flags&BodyFlagGuaranteedAck != 0 {
		b.GuaranteedAck, err = reader.ReadUint16()
		if err != nil {
			return err
		}

		if b.Flags&BodyFlagGuaranteedAckMask != 0 {
			b.GuaranteedAckMask, err = reader.Read7BitEncodedInt64()
			if err != nil {
				return err
			}
		}
	}

	// Read state channel acks
	// PS2 validates state ACKs must arrive in ascending channel order (#24).
	if b.Flags&BodyFlagClientUpdateAck != 0 {
		var lastChannel byte
		firstAck := true
		for {
			channel, err := reader.ReadByte()
			if err != nil {
				return err
			}
			if channel == 0xF8 {
				break
			}
			if channel > 0xF8 {
				return ErrInvalidChannel
			}
			// #24: PS2 validates ascending channel order
			if !firstAck && channel <= lastChannel {
				return fmt.Errorf("state acks not in ascending order: %d <= %d", channel, lastChannel)
			}
			ack, err := reader.ReadUint16()
			if err != nil {
				return err
			}
			b.StateAcks = append(b.StateAcks, StateAck{Channel: channel, Ack: ack})
			lastChannel = channel
			firstAck = false
		}
	}

	// Read messages
	for {
		if reader.Position()-bodyStart >= header.Size {
			break
		}

		msgType, err := reader.ReadByte()
		if err != nil {
			return err
		}

		size, err := reader.ReadSize()
		if err != nil {
			return err
		}

		if size > MaxMessageSize {
			return ErrMessageTooLarge
		}

		mt := MessageType(msgType)

		// Reliable messages (0xFA, 0xFB, 0xF9 all have same wire format)
		if mt == MessageTypeReliable || mt == MessageTypePing || mt == MessageTypeSegmentReliable {
			seqNum, err := reader.ReadUint16()
			if err != nil {
				return err
			}

			var opcode GameOpcode
			if mt != MessageTypePing && size >= 2 {
				op, err := reader.ReadUint16()
				if err != nil {
					return err
				}
				opcode = GameOpcode(op)
				size -= 2
			}

			if size < 0 {
				size = 0
			}
			msgData, err := reader.ReadBytes(size)
			if err != nil {
				return err
			}

			msg := &Message{data: make([]byte, len(msgData)+2)}
			msg.Type = mt
			msg.Opcode = opcode
			msg.Sequence = seqNum
			// Reconstruct data with opcode prefix for consistency
			if mt != MessageTypePing {
				fullData := make([]byte, 2+len(msgData))
				fullData[0] = byte(opcode)
				fullData[1] = byte(opcode >> 8)
				copy(fullData[2:], msgData)
				msg.SetData(fullData)
			} else {
				msg.SetData(msgData)
			}
			b.Messages = append(b.Messages, msg)
			continue
		}

		// Unreliable messages
		if mt == MessageTypeUnreliable {
			op, err := reader.ReadUint16()
			if err != nil {
				return err
			}
			size -= 2

			msgData, err := reader.ReadBytes(size)
			if err != nil {
				return err
			}

			msg := &Message{data: make([]byte, len(msgData)+2)}
			msg.Type = mt
			msg.Opcode = GameOpcode(op)
			// Reconstruct data with opcode prefix
			fullData := make([]byte, 2+len(msgData))
			fullData[0] = byte(op)
			fullData[1] = byte(op >> 8)
			copy(fullData[2:], msgData)
			msg.SetData(fullData)
			b.Messages = append(b.Messages, msg)
			continue
		}

		// State channel messages (0x00-0xF7)
		if msgType <= 0xF7 {
			if reader.Remaining() < 3 {
				break
			}

			// PS2 connection_parse_segment_body validates refnum_offset < 0x21 (33).
			// Size is already validated against 0x7FE for all message types above.
			seqNum, _ := reader.ReadUint16()
			refNum, _ := reader.ReadByte()

			if refNum >= 0x21 {
				return fmt.Errorf("state channel %d: refnum %d exceeds PS2 limit 0x21", msgType, refNum)
			}

			// RLE decode
			var decoded []byte
			for reader.Remaining() > 0 {
				rb, err := reader.ReadByte()
				if err != nil || rb == 0x00 {
					break
				}

				var copyCount, zeroCount int
				if rb&0x80 != 0 {
					copyCount = int(rb & 0x7F)
					z, _ := reader.ReadByte()
					zeroCount = int(z)
				} else {
					copyCount = int(rb >> 4)
					zeroCount = int(rb & 0x0F)
				}

				for j := 0; j < zeroCount; j++ {
					decoded = append(decoded, 0)
				}
				if copyCount > 0 && reader.Remaining() >= copyCount {
					data, _ := reader.ReadBytes(copyCount)
					decoded = append(decoded, data...)
				}
			}

			msg := &Message{data: make([]byte, len(decoded))}
			msg.Type = mt
			msg.Sequence = seqNum
			msg.XORByte = refNum
			msg.SetData(decoded)
			b.Messages = append(b.Messages, msg)
			continue
		}

		return ErrInvalidMessageType
	}

	return nil
}

// ========================================================================
// CRC
// ========================================================================

const CRCXorConstant uint32 = 0x11f19ed3

var crcTable = [256]uint32{
	0x00000000, 0x77073096, 0xee0e612c, 0x990951ba, 0x076dc419,
	0x706af48f, 0xe963a535, 0x9e6495a3, 0x0edb8832, 0x79dcb8a4,
	0xe0d5e91e, 0x97d2d988, 0x09b64c2b, 0x7eb17cbd, 0xe7b82d07,
	0x90bf1d91, 0x1db71064, 0x6ab020f2, 0xf3b97148, 0x84be41de,
	0x1adad47d, 0x6ddde4eb, 0xf4d4b551, 0x83d385c7, 0x136c9856,
	0x646ba8c0, 0xfd62f97a, 0x8a65c9ec, 0x14015c4f, 0x63066cd9,
	0xfa0f3d63, 0x8d080df5, 0x3b6e20c8, 0x4c69105e, 0xd56041e4,
	0xa2677172, 0x3c03e4d1, 0x4b04d447, 0xd20d85fd, 0xa50ab56b,
	0x35b5a8fa, 0x42b2986c, 0xdbbbc9d6, 0xacbcf940, 0x32d86ce3,
	0x45df5c75, 0xdcd60dcf, 0xabd13d59, 0x26d930ac, 0x51de003a,
	0xc8d75180, 0xbfd06116, 0x21b4f4b5, 0x56b3c423, 0xcfba9599,
	0xb8bda50f, 0x2802b89e, 0x5f058808, 0xc60cd9b2, 0xb10be924,
	0x2f6f7c87, 0x58684c11, 0xc1611dab, 0xb6662d3d, 0x76dc4190,
	0x01db7106, 0x98d220bc, 0xefd5102a, 0x71b18589, 0x06b6b51f,
	0x9fbfe4a5, 0xe8b8d433, 0x7807c9a2, 0x0f00f934, 0x9609a88e,
	0xe10e9818, 0x7f6a0dbb, 0x086d3d2d, 0x91646c97, 0xe6635c01,
	0x6b6b51f4, 0x1c6c6162, 0x856530d8, 0xf262004e, 0x6c0695ed,
	0x1b01a57b, 0x8208f4c1, 0xf50fc457, 0x65b0d9c6, 0x12b7e950,
	0x8bbeb8ea, 0xfcb9887c, 0x62dd1ddf, 0x15da2d49, 0x8cd37cf3,
	0xfbd44c65, 0x4db26158, 0x3ab551ce, 0xa3bc0074, 0xd4bb30e2,
	0x4adfa541, 0x3dd895d7, 0xa4d1c46d, 0xd3d6f4fb, 0x4369e96a,
	0x346ed9fc, 0xad678846, 0xda60b8d0, 0x44042d73, 0x33031de5,
	0xaa0a4c5f, 0xdd0d7cc9, 0x5005713c, 0x270241aa, 0xbe0b1010,
	0xc90c2086, 0x5768b525, 0x206f85b3, 0xb966d409, 0xce61e49f,
	0x5edef90e, 0x29d9c998, 0xb0d09822, 0xc7d7a8b4, 0x59b33d17,
	0x2eb40d81, 0xb7bd5c3b, 0xc0ba6cad, 0xedb88320, 0x9abfb3b6,
	0x03b6e20c, 0x74b1d29a, 0xead54739, 0x9dd277af, 0x04db2615,
	0x73dc1683, 0xe3630b12, 0x94643b84, 0x0d6d6a3e, 0x7a6a5aa8,
	0xe40ecf0b, 0x9309ff9d, 0x0a00ae27, 0x7d079eb1, 0xf00f9344,
	0x8708a3d2, 0x1e01f268, 0x6906c2fe, 0xf762575d, 0x806567cb,
	0x196c3671, 0x6e6b06e7, 0xfed41b76, 0x89d32be0, 0x10da7a5a,
	0x67dd4acc, 0xf9b9df6f, 0x8ebeeff9, 0x17b7be43, 0x60b08ed5,
	0xd6d6a3e8, 0xa1d1937e, 0x38d8c2c4, 0x4fdff252, 0xd1bb67f1,
	0xa6bc5767, 0x3fb506dd, 0x48b2364b, 0xd80d2bda, 0xaf0a1b4c,
	0x36034af6, 0x41047a60, 0xdf60efc3, 0xa867df55, 0x316e8eef,
	0x4669be79, 0xcb61b38c, 0xbc66831a, 0x256fd2a0, 0x5268e236,
	0xcc0c7795, 0xbb0b4703, 0x220216b9, 0x5505262f, 0xc5ba3bbe,
	0xb2bd0b28, 0x2bb45a92, 0x5cb36a04, 0xc2d7ffa7, 0xb5d0cf31,
	0x2cd99e8b, 0x5bdeae1d, 0x9b64c2b0, 0xec63f226, 0x756aa39c,
	0x026d930a, 0x9c0906a9, 0xeb0e363f, 0x72076785, 0x05005713,
	0x95bf4a82, 0xe2b87a14, 0x7bb12bae, 0x0cb61b38, 0x92d28e9b,
	0xe5d5be0d, 0x7cdcefb7, 0x0bdbdf21, 0x86d3d2d4, 0xf1d4e242,
	0x68ddb3f8, 0x1fda836e, 0x81be16cd, 0xf6b9265b, 0x6fb077e1,
	0x18b74777, 0x88085ae6, 0xff0f6a70, 0x66063bca, 0x11010b5c,
	0x8f659eff, 0xf862ae69, 0x616bffd3, 0x166ccf45, 0xa00ae278,
	0xd70dd2ee, 0x4e048354, 0x3903b3c2, 0xa7672661, 0xd06016f7,
	0x4969474d, 0x3e6e77db, 0xaed16a4a, 0xd9d65adc, 0x40df0b66,
	0x37d83bf0, 0xa9bcae53, 0xdebb9ec5, 0x47b2cf7f, 0x30b5ffe9,
	0xbdbdf21c, 0xcabac28a, 0x53b39330, 0x24b4a3a6, 0xbad03605,
	0xcdd70693, 0x54de5729, 0x23d967bf, 0xb3667a2e, 0xc4614ab8,
	0x5d681b02, 0x2a6f2b94, 0xb40bbe37, 0xc30c8ea1, 0x5a05df1b,
	0x2d02ef8d,
}

// CalculateCRC computes the CRC32 checksum with EQOA XOR constant.
func CalculateCRC(data []byte) uint32 {
	crc := uint32(0xFFFFFFFF)
	for _, b := range data {
		crc = (crc >> 8) ^ crcTable[(crc&0xFF)^uint32(b)]
	}
	return crc ^ CRCXorConstant
}

// ValidateCRC checks if the packet CRC is valid.
func ValidateCRC(packet []byte) bool {
	if len(packet) < 4 {
		return false
	}
	dataLen := len(packet) - 4
	expectedCRC := CalculateCRC(packet[:dataLen])
	actualCRC := uint32(packet[dataLen]) |
		uint32(packet[dataLen+1])<<8 |
		uint32(packet[dataLen+2])<<16 |
		uint32(packet[dataLen+3])<<24
	return expectedCRC == actualCRC
}

// AppendCRC appends the CRC32 checksum to a packet.
func AppendCRC(data []byte) []byte {
	crc := CalculateCRC(data)
	return append(data,
		byte(crc),
		byte(crc>>8),
		byte(crc>>16),
		byte(crc>>24),
	)
}

// ========================================================================
// RLE Encoding (for outgoing state channels)
// ========================================================================

// RunLengthEncode is run_length_encode (rle.cpp): pairs of (zero run, literal
// run). A zero run stops at 255 and a literal run at the first zero or at 127.
// The pair is one byte (literals<<4 | zeros) when there are fewer than 8
// literals and 16 zeros, else two: (literals | 0x80), zeros. The stream ends
// with a 0 byte, which is the one-byte form of an empty pair.
//
// The short form used to take up to 15 literals. 8 or more set bit 7, which
// the decoder reads as the long form — so a run of 8..15 non-zero bytes
// corrupted the rest of the state (each 0x40 whose facing byte is 0 has one).
func RunLengthEncode(data []byte) []byte {
	result := make([]byte, 0, len(data)+10)
	for {
		zeros := 0
		for zeros < len(data) && data[zeros] == 0 && zeros != 255 {
			zeros++
		}
		data = data[zeros:]
		literals := 0
		for literals < len(data) && data[literals] != 0 && literals != 127 {
			literals++
		}
		if literals >= 8 || zeros >= 16 {
			result = append(result, byte(literals|0x80), byte(zeros))
		} else {
			c := byte(literals<<4 | zeros)
			result = append(result, c)
			if c == 0 {
				return result
			}
		}
		result = append(result, data[:literals]...)
		data = data[literals:]
	}
}
