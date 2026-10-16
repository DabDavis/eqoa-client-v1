package game

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log"
	"math"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/eqoa/client/internal/mathutil"
	"github.com/eqoa/client/internal/protocol"
	"github.com/DabDavis/eqoa-esf-tools/pkg/buffer"

	"github.com/eqoa/client/internal/network"
)

// Phase represents the current game phase.
type Phase int

const (
	PhaseDisconnected Phase = iota
	PhaseConnecting
	PhaseAuth
	PhaseServerList
	PhaseConnectingGame
	PhaseCharSelect
	PhaseEnteringWorld
	PhaseInWorld
)

// String returns a human-readable name for the phase.
func (p Phase) String() string {
	switch p {
	case PhaseDisconnected:
		return "Disconnected"
	case PhaseConnecting:
		return "Connecting..."
	case PhaseAuth:
		return "Authenticating..."
	case PhaseServerList:
		return "Server List"
	case PhaseConnectingGame:
		return "Connecting to Game..."
	case PhaseCharSelect:
		return "Character Select"
	case PhaseEnteringWorld:
		return "Entering World..."
	case PhaseInWorld:
		return "In World"
	default:
		return "Unknown"
	}
}

// ServerInfo holds a game server entry from the server list.
type ServerInfo struct {
	Name       string
	EndpointID uint16
	Port       uint16
	IP         string
}

// GuildMember represents one member in the guild roster.
// PS2: 0x44 (68) bytes per member in a binary tree at entity+0x288.
type GuildMember struct {
	CharID int64
	Name   string
	Level  int
	Class  int
	Online bool
}

// CharInfo holds character data from the character select list.
type CharInfo struct {
	ServerID uint32
	Name     string
	ModelID  int32
	Class    int
	Race     int
	Level    int

	// Appearance (from char select packet)
	HairColor  byte
	HairLength byte
	HairStyle  byte
	FaceOption byte

	// Equipment (32-byte block from char select packet)
	Shield         uint32
	PrimaryWeapon  uint32
	SecondaryWeapon uint32
	PrimaryAttackType   byte // weapon AttackType for primary (PS2 VICSpriteAttackType)
	SecondaryAttackType byte // weapon AttackType for secondary
	ShieldAttackType    byte // shield type (usually 0)
	Chest, Bracer, Gloves, Legs, Boots, Helm, Robe byte
	ChestTint, BracerTint, GlovesTint, LegsTint, BootsTint, HelmTint, RobeTint byte

}

// Entity represents a game entity decoded from state channels.
type Entity struct {
	ObjectID  uint32
	Channel   int
	Name      string
	X, Y, Z   float32
	Facing    float32
	ModelID   uint32
	Level     byte
	HP, MaxHP       byte
	HPFlag          byte // state channel offset 26: 1=alive, 3=dead
	Power, MaxPower byte
	HPInt, MaxHPInt       int32 // raw int32 values from stat channel
	PowerInt, MaxPowerInt int32
	// Computed stats from stat channel 0x42 (offsets 0-27)
	Str, Sta, Agi, Dex, Wis, Int, Cha int32
	AC                                 int32
	OffMod, DefMod, HPFactor           int32 // stat channel offsets 48, 52, 56
	// Base stats (PS2 EE +0x74 to +0x8C, stat channel offsets 116-140)
	BaseStr, BaseSta, BaseAgi, BaseDex, BaseWis, BaseInt, BaseCha int32
	BaseAC                             int32 // stat channel offset 148 (armor AC)
	BaseOffMod, BaseDefMod             int32 // stat channel offsets 164, 168
	// Resists: additions from stat channel offsets 88-111, base from offsets 204-227
	PoisonR, DiseaseR, FireR, ColdR, LightningR, ArcaneR             int32 // total (base + addition)
	PoisonRAdd, DiseaseRAdd, FireRAdd, ColdRAdd, LightningRAdd, ArcaneRAdd int32 // additions only
	XPPct           byte  // XP progress within current level (0-255)
	XPRaw           int64 // raw total XP from server
	Breath          byte  // PS2: +0x30 breath (0=full, 255=empty/drowning)
	IsNPC           bool
	IsPlayer  bool
	NameColor byte // state channel offset 142: 0=NPC, 2=Player (indexes VICSprite::NameColors)
	Animation byte
	Scale     float32
	Dead      bool
	DeadSince time.Time // when death started (for HUD fade/tint)
	Speed     float32   // movement speed from PlayerSpeed opcode 0x00F8 (0 = not yet received)
	LastUpdate    time.Time // last time this entity's state channel was updated
	LastDamageTime time.Time // last time this entity took damage (for hit reaction anim)
	AttackCount       int       // attack counter (driven by damage indications)
	LastAttackAnimTime time.Time // throttle for entity attack animation triggers
	CastAnimEnd   time.Time // when NPC casting animation ends
	GroundSnapped bool      // true after idle Y snap applied (reset on move)

	// State channel fields from ObjectUpdate (confirmed by PS2 CLIENT decompilation)
	World     byte   // offset 16: continent (0=Tunaria)
	Movement  byte   // offset 140: 0=normal, 1=running
	NamePlate byte   // offset 142: 0=own player, 1=show
	Target    uint32 // offset 58-61: target ObjectID (0xFFFFFFFF = none)

	// Combat state (state channel offsets 63-64)
	CombatEngaged    bool // offset 63: auto-attack active
	CombatAttackType byte // offset 64: active weapon AttackType during combat

	// Equipment (state channel offsets 67-78, 91-98)
	PrimaryWeapon      uint32 // offset 67-70 LE
	SecondaryWeapon    uint32 // offset 71-74 LE
	Shield             uint32 // offset 75-78 LE
	PrimaryAttackType  byte   // offset 91: VICSpriteAttackType (1=1HS,2=2HS,3=1HB,etc)
	SecondaryAttackType byte  // offset 92: secondary weapon AttackType (0=none)
	Chest                  byte   // offset 93
	Bracer                 byte   // offset 94
	Gloves                 byte   // offset 95
	Legs                   byte   // offset 96
	Boots                  byte   // offset 97
	Helm                   byte   // offset 98
	Robe                   byte   // offset 109 (0xFF = no robe)
	RobeTint               byte   // offset 110 (0-13 tint index)

	// Tints (state channel offsets 99-104)
	ChestTint  byte // offset 99
	BracerTint byte // offset 100
	GlovesTint byte // offset 101
	LegsTint   byte // offset 102
	BootsTint  byte // offset 103
	HelmTint   byte // offset 104

	// Appearance (state channel offsets 105-108, 144)
	HairColor  byte // offset 105
	HairLength byte // offset 106
	HairStyle  byte // offset 107
	FaceOption byte // offset 108
	Sex        byte // from ModelID (0=male, 1=female)
	Race       byte // offset 144

	// Class from state channel offset 145 (player channel 0) or CharInfo.
	// 0=Warrior, 1=Ranger, 2=Paladin, 3=ShadowKnight, 4=Wizard, 5=Bard, etc.
	Class byte

	// ServerTargetID: server's view of who this player is targeting (channel 0, offset 59-62).
	// 0xFFFFFFFF = no target. Useful for confirming ForcePositionFlush communicated the target.
	ServerTargetID uint32

	// NPCType from state channel offsets 145-146 (uint16 LE).
	// 0x8000=NPC, 0x8100=merchant, 0x8006=quest giver, 0x8008=guild board, etc.
	NPCType uint16
	// NPCFlags is VICreatureEntity +0x25A, state channel bytes 146-147 (byte
	// 145 is a byte of its own, +0x259). InteractWithTarget: 1 merchant
	// (0x004C), 2 bank (0x124D), 8 blacksmith (0x0061); 0x80 cannot be
	// auto-attacked (ToggleAutoAttack). NPCType&0x0100 is NPCFlags&1.
	NPCFlags uint16

	// Experience from StatsUpdate opcode 0x0790.
	// PS2: stored at entity+0x4F0F0 (int64) and +0x4F100 (int64 max).
	Experience    int64
	ExperienceMax int64

	// PS2 VIWalkControl interpolation (InitParms exponential decay + velocity dead reckoning).
	// smoothRate values from InitParms disassembly (25 live instances confirmed).
	TargetX, TargetY, TargetZ float32 // latest server position
	TargetFacing              float32 // latest server facing
	VelX, VelY, VelZ          float32 // decoded velocity from state channel offsets 40-42
	PrevVelX, PrevVelZ        float32 // velocity from previous update (for direction change detection)
	Interpolating             bool    // false until first position received
	UpdateInterval            float32 // exponential moving average of seconds between state updates
}

// ChatChannel represents the type of chat message.
type ChatChannel int

const (
	ChatSay     ChatChannel = 0
	ChatShout   ChatChannel = 1
	ChatGroup   ChatChannel = 2
	ChatGuild   ChatChannel = 3
	ChatTell    ChatChannel = 4
	ChatSystem  ChatChannel = 5
	ChatError   ChatChannel = 6
	ChatAuction ChatChannel = 7
)

// ChatMessage represents a chat log entry.
type ChatMessage struct {
	Text    string      // Full message text (e.g. "PlayerName: hello")
	Channel ChatChannel // Channel type for color
	ColorR  uint8       // Custom color (for ColoredChat)
	ColorG  uint8
	ColorB  uint8
}

// BuffSlot represents an active buff/effect from state channel 0x43.
type BuffSlot struct {
	SpellHash int32  // Spell/effect identifier (also used as icon DictID)
	Name      string // Display name (e.g. "Illusion: Barbarian")
}

// GroupMember represents a member of the player's group.
type GroupMember struct {
	ObjectID uint32
	Name     string
}

// WhoEntry represents a player from the who list (opcode 0x0E02).
type WhoEntry struct {
	Name     string
	Race     byte
	Class    byte
	Level    byte
	ObjectID uint32
}

// PlayerSearchEntry is one 0x1500 result of Sandstorm's player search (its S2
// cave 0x000FE4B8). Each field is read only if the message reaches it.
type PlayerSearchEntry struct {
	Race, Class byte   // wire ids; only the low nibble indexes the name tables
	Level       byte   // printed as is, 0-255
	Name        string // a fixed 12-byte ASCII slot
	ID          uint32 // the player's handle (0 when the message stops short)
	// The location, when the message carries it (22 bytes or more): world,
	// grid column and row (LocationLine).
	HasLocation      bool
	World, Col, Row  int
	Group            byte   // group size; 0 not grouped, 5 and up (0xFF default) no line
	Note             string // the player's note, up to 71 ASCII
}

// LFGEntry represents a player from the group finder (opcode 0x03D5 LFG sub-type).
type LFGEntry struct {
	CharID    uint64
	Name      string
	Level     byte
	Class     byte // zigzag-decoded
	Zone      byte
	GuildName string // empty if no guild
}

// MailMessage represents a mail entry from the server.
type MailMessage struct {
	Sender  string
	Subject string
	Body    string
}

// FloatingText represents a rising/fading damage or heal number above an entity.
type FloatingText struct {
	Text     string    // |amount|, as CLIENT's "%d" draws it: "105"
	X, Y, Z  float32  // world position at spawn time
	SpawnTime time.Time
	IsHeal   bool      // the amount was positive
	// Color is what the number is drawn in: CLIENT's green (a positive
	// amount), white (my own hit) or yellow (a hit on me), or the server's
	// own when it sends one — Sandstorm's S2 reads three optional bytes
	// after the defender (R, G, B) and draws the text and its outline in it.
	Color    [3]uint8
	HasColor bool // Color came from the server
}

// GameState holds all client-side game state.
type GameState struct {
	Phase      Phase
	Conn       *network.Connection
	AccountID  uint32
	ServerIP   string
	Username   string
	GamePort   int
	AuthPort   int
	PatchPort  int

	// Server list
	Servers []ServerInfo

	// Character select
	Characters   []CharInfo
	SelectedChar int // index into Characters, -1 = none selected
	CreateError  string // set when server sends OpcodeNameTaken

	// In-world state
	PlayerObjectID uint32
	Player         Entity
	Entities       map[uint32]*Entity
	ChatLog        []ChatMessage

	// Zone file path from MemoryDump (e.g. "tunaria/freeprt")
	ZonePath string

	// MemoryDumpCount increments each time a MemoryDump is received
	// (world entry, coaching, death respawn). Visual client uses this
	// to trigger loading screen.
	MemoryDumpCount int

	// Status message for display
	StatusMsg string

	// Whether we got the server-initiated session promotion
	MasterSession bool

	// Whether the initial 0x40 world-entry handshake completed
	WorldEntryAcked bool

	// SampleHeightFunc is set by the visual client to provide terrain height lookup.
	// Returns (groundY, true) if terrain exists at (x,z) near refY, else (0, false).
	SampleHeightFunc func(x, z, refY float32) (float32, bool)

	// Target response from server (opcode 0x0010)
	TargetConColor  byte   // 0=red,1=yellow,2=white,3=dkblue,4=ltblue,5=green
	TargetFaceColor byte   // 0=hostile,2=neutral,4=friendly,5=ally
	TargetGuildName string // guild name of targeted player (UTF-16LE from ClassicTargetInfo)
	TargetTitle     string // title of targeted player

	// Parsed MemoryDump data (populated on world entry)
	ParsedItems          []ParsedItem
	ParsedSpells         []ParsedSpell
	ParsedCM             *ParsedCMData
	InventoryItemCount   int // items 0..N-1 are inventory, N..end are bank
	itemLists                // the lists' own state: keys, the corpse, the trade (items.go)

	// Hotbar: slots 0-9 mapped to spells. Populated from ParsedSpells (OnHotBar=1).
	// Keys 1-9,0 map to slots 0-8,9 respectively.
	Hotbar      [10]*ParsedSpell
	HotbarNames [10]string // short display names (first 8 chars)

	// Cooldown tracking: slot → expiry time and total duration
	Cooldowns    [10]time.Time
	CooldownDurs [10]float32 // total cooldown duration in seconds

	// AbilityStamp is VIClient+0x2BB60: the u32 every ability message but
	// 0x00CD carries, which the client sends back first in 0x0032/0x0033/0x00FE.
	AbilityStamp uint32

	// MoveLock is VIClient+0x6DC8, the byte 0x00FC sets: while it is non-zero
	// the walker's forward speed is zeroed (func_00BC3AE8, 0x00BC48D4).
	MoveLock int

	// ExpDebt is the player record's +0x2C (VIClient+0xC308), which 0x00D9
	// sets whole. FrontiersForge calls it exp debt; unconfirmed.
	ExpDebt uint32

	// Words is the string table (WORDS.TXT) handlers print from; the visual
	// client sets it once it has loaded one. Nil in tests and headless.
	Words Words

	// Dialogue state (populated by 0x0034 OptionBox / 0x0046 DialogueBox)
	DialogueText    string   // greeting or message text
	DialogueOptions []string // selectable options (empty for simple dialogue)
	DialogueCounter uint32   // server-assigned counter, echoed back in 0x0035
	DialogueActive  bool     // true when dialogue is displayed on screen

	// Spell effects database (loaded from SPELLFX.CSF/ESF)
	SpellFX    *SpellFXDB
	SpellFXSys *SpellEffectSystem // runtime spell effect driver (set by engine)

	// Weather (VIAtmosphere)
	Weather *WeatherSystem

	// Teleport pending — engine sends EnableChannel + 0x40 on next tick
	TeleportPending      bool
	TeleportWorld        byte
	TeleportToken        int32 // 0x00B2's last u32, sent back zigzag in 0x0049
	TeleportPhysicsReset bool // client should reset velocityY, onGround, etc.

	// World change — visual client must reload terrain ESF when world ID changes.
	// PS2 world IDs: 0=Tunaria, 1=Rathe, 2=Odus, 3=Lavastm, 4=Planesky, 5=Secrets
	WorldChanged bool // set by MemoryDump/handleTeleport when world changes
	PendingWorld byte // new world ID to load

	// Admin/GM state (from AdminCode opcode 0x0700)
	AdminMenuType byte  // menu type from server
	AdminMenuCode int64 // menu code from server

	// Time of day (from OpcodeTime 0x0013)
	TimeEpoch1   uint64    // server epoch1 (ms)
	TimeEpoch2   uint64    // server epoch2 (ms) — used for day/night cycle
	TimeReceived time.Time // wall clock when received
	TimeValid    bool      // true after first OpcodeTime

	// Buff state (from channel 0x43, variable slot count)
	Buffs []BuffSlot // active buffs/debuffs (server sends variable count)

	// Combat events (consumed by UI for animations)
	PlayerHitTime       time.Time // last time player took damage (for hit reaction anim)
	PlayerSwingTime     time.Time // last time server confirmed player melee swing (CastingCombat source==SELF)
	PlayerInCombat      bool      // one-shot: server confirmed player auto-attack (consumed by animation layer)
	CombatEnded         bool      // PS2 0x0777: server explicitly ended combat (consumed by animation layer)
	PendingPlayerHitFX  int       // queued hit FX count (consumed by applySkeletal at swing impact)
	HitFXTarget         uint32    // target ObjectID for pending hit FX
	LastAutoAttackEvent time.Time // last CastingCombat auto-attack involving player (combat-end detection)
	LastMeleeHitFX      time.Time // throttle: last time melee hit FX was played on a target
	LastProcFXTime      time.Time // throttle: PS2 proc cooldown ~3500ms (func_00BC76A0)
	CombatAttackFXHash  uint32    // PS2 func_00CBE0A8: resolved spell FX DictID for current weapon/class
	FloatingTexts   []FloatingText // rising/fading damage/heal numbers

	// Blacksmith/repair window (0x0061 opens it, 0x0063 closes it; BlacksmithID is in itemLists)
	BlacksmithOpen bool
	CastStartTime   time.Time      // when current spell cast began
	CastDurationMs  uint16         // cast duration in ms (0 = not casting)
	CastTargetName  string         // name of target being cast on
	CastSpellName   string         // name of spell being cast (set from hotbar)
	CastSelfTarget  bool           // true if casting on self (for buff vs damage anim)

	// Who list (from opcode 0x0E02)
	WhoList []WhoEntry

	// Auction house (from opcode 0x1206)
	AuctionItems       []AuctionItem
	AuctionSessionID   byte  // from 0x1102 sub=0x0C response
	AuctionSeq         byte  // increments by 2 per request (4, 6, 8...)
	AuctionLastListing int64 // last listing ID for cursor-based pagination
	AuctionTotalCount  int   // listings held, every page so far
	AuctionFilter      AuctionFilter // the current search's, which every page repeats

	// PS2 Hotkey Menu (parsed from MemoryDump)
	Hotkeys []HotkeyEntry // tree-structured hotkey entries (25 default)

	// Sandstorm's player search (0x1500 appends, 0x1501 clears).
	PlayerSearch []PlayerSearchEntry

	// LFG / Group Finder (from opcode 0x03D5 LFG entries)
	LFGList       []LFGEntry
	LFGRequested  bool      // true when we've sent an LFG request
	LFGRegistered bool      // true when player is registered as LFG

	// Group state
	GroupActive   bool
	GroupID       uint32
	GroupMembers  []GroupMember
	GroupInviteID uint32 // pending invite group ID (0 = none)
	GroupInviter  string // name of inviter

	// Reverse map: entity channel (0x00-0x17) → objectID currently on it.
	// Avoids O(n) entity scans in decodeEntityState.
	ChannelEntity [29]uint32
	RoutingState // the pump's and the state channels' own state (intake_routing.go)

	// Quest tracker (from opcodes 0x0045/0x007B/0x007F)
	QuestTrackerID    uint32   // active tracked quest ID (0xFFFFFFFF = none)
	QuestTexts        [8]string // ring buffer of quest text strings (256 chars each)
	QuestTextIndex    int       // current ring buffer write index
	QuestProgress     int64     // quest progress value (zigzag)
	QuestProgressF    [4]float32 // quest progress float values

	// Zone transfer state (from opcodes 0x07B0/0x07B2)
	ZoneTransferActive     bool   // true during zone loading
	ZoneTransferZone       string // target zone path
	ZoneTransferState      int    // PS2 state machine: 0=idle, 1-5=phases
	ZoneTransferProxyCount int    // number of zone proxies received in sub-type 2
	PendingZoneIdx         int    // PS2 entity+0x4F0E8: zone index about to be loaded

	// Guild state (PS2 opcode 0x0468 sub-types)
	GuildName    string        // from sub-type 1 (0x80 blob UTF-16)
	GuildMOTD    string        // from sub-type 1 (0x200 blob UTF-16)
	GuildMembers []GuildMember // from sub-type 3 (member list)
	GuildLeader  bool          // from sub-type 13/14 (leader status)

	// Player currency
	Tunar int64

	// Training points (from MemoryDump, updated by 0x001D)
	TrainingPoints int64

	// Pending training point allocations (UI staging before send)
	TPAlloc [7]int // STR, STA, AGI, DEX, WIS, INT, CHA

	// Faction standings (from opcode 0x07E2 FactionPage)
	Factions       []FactionEntry // all received faction entries
	FactionPageID  int            // session-specific counter for requests
	FactionLoaded  bool           // true after all pages received

	// Combat Mastery tree (from opcode 0x1402 ClassMasteryServer)
	CMSkills      []CMSkill // all skills from the full tree
	CMUnspent     int       // unspent CM points
	CMDetail      *CMSkillDetail // currently viewed skill detail
	CMTreeLoaded  bool

	// Merchant window (from opcode 0x00B7)
	MerchantOpen     bool
	MerchantName     string // the merchant entity's name (its greeting is MerchantText)
	MerchantItems    []ParsedItem
	MerchantSelected int // selected item index (-1 = none)
	MerchantBuyRate  int // +0x1B308: the percent the merchant pays of an item's value (SellPrice)
	MerchantSellRate int // +0x1B30C: the percent of its value a merchant item costs (MerchantPrice)

	// Chat message (from opcode 0x07A3)
	// Uses existing ChatLog

	// Mail system (from opcode 0x0D41)
	MailMessages []MailMessage

	// Ignore list (from opcode 0x1102)
	IgnoreList []string

	// Entity creation via reliable opcodes (from 0x00B3/0x00B5 etc.)
	// Uses existing Entities map — reliable opcodes populate same entities as state channels

	// Debug: state channel receive counts
	stateChannelCount map[byte]int

	// Debug: entity movement tracking (enabled by headless 'watch' command)
	DebugEntityMovement bool
	DebugStateDiffs     bool       // log byte-level diffs on self state channel
	prevChannelState    [29][]byte // previous state bytes per entity channel (0x00-0x17) for diff
	LastInterpDebug     time.Time  // last time interpolation debug was printed
}

// ResetStateDiffBaseline clears all previous state channel snapshots
// so the next updates capture fresh baselines.
func (gs *GameState) ResetStateDiffBaseline() {
	for i := range gs.prevChannelState {
		gs.prevChannelState[i] = nil
	}
}

// NewGameState creates a new game state.
func NewGameState(host string, gamePort, authPort, patchPort int, username string) *GameState {
	gs := &GameState{
		Phase:             PhaseDisconnected,
		ServerIP:          host,
		Username:          username,
		GamePort:          gamePort,
		AuthPort:          authPort,
		PatchPort:         patchPort,
		Entities:          make(map[uint32]*Entity),
		QuestTrackerID:    0xFFFFFFFF, // no active quest
		SelectedChar:      -1,         // no character selected yet
		itemLists:         newItemLists(),
		stateChannelCount: make(map[byte]int),
	}
	return gs
}

// HandleMessage is the guaranteed message (0xFB) dispatcher, CLIENT's
// VIClient::ProcessMessage (0x00BD0DC8): one switch on the u16 opcode.
func (gs *GameState) HandleMessage(msg *protocol.Message) {
	// Extract opcode from message data for reliable messages
	opcode := msg.Opcode
	if (msg.Type == protocol.MessageTypeReliable || msg.Type == protocol.MessageTypeSegmentReliable) && len(msg.Data()) >= 2 {
		opcode = protocol.GameOpcode(msg.Data()[0]) | protocol.GameOpcode(msg.Data()[1])<<8
	}
	if msg.Type == protocol.MessageTypeUnreliable && len(msg.Data()) >= 2 {
		opcode = protocol.GameOpcode(msg.Data()[0]) | protocol.GameOpcode(msg.Data()[1])<<8
	}

	// Log ALL opcodes to rolling log
	logOpcode(uint16(opcode), msg.Type, msg.Sequence, msg.Data())

	switch opcode {
	case protocol.OpcodeDiscVersion:
		gs.handleDiscVersion(msg)
	case protocol.OpcodeCamera1:
		gs.handleCamera1(msg)
	case protocol.OpcodeCamera2:
		gs.handleCamera2(msg)
	case protocol.OpcodeCharacterSelect:
		gs.handleCharacterSelect(msg)
	case protocol.OpcodeNameTaken:
		gs.CreateError = "Name is already taken"
		log.Printf("NameTaken: server rejected character name")
	case protocol.OpcodeTime:
		gs.handleTime(msg)
	case protocol.OpcodeMemoryDump:
		gs.noteSelf(msg)
		gs.handleMemoryDump(msg)
	case protocol.OpcodeTeleport:
		gs.handleTeleport(msg)
	case protocol.OpcodePlayerSpeed:
		gs.handlePlayerSpeed(msg)
	case protocol.OpcodeColoredChat:
		gs.handleColoredChat(msg)
	case protocol.OpcodeClientMessage:
		gs.handleClientMessage(msg)
	case protocol.OpcodeErrorMessage:
		gs.handleErrorMessage(msg)
	case protocol.OpcodeGroupChat:
		gs.handleGroupChat(msg)
	case protocol.OpcodeNPCInteractResult2:
		gs.handleNPCInteractResult(msg)
	case protocol.OpcodeCreateGroup:
		gs.handleCreateGroup(msg)
	case protocol.OpcodeRemoveGroupMember:
		gs.handleRemoveGroupMember(msg)
	case protocol.OpcodeServerDisbandGroup:
		gs.handleDisbandGroup(msg)
	case protocol.OpcodeClassicTargetInfo:
		gs.handleClassicTargetInfo(msg)

	// Entity lifecycle (reliable opcodes)
	case protocol.OpcodeFullEntityCreate:
		gs.handleFullEntityCreate(msg)
	case protocol.OpcodeEntityUpdate:
		gs.handleEntityUpdate(msg)
	case protocol.OpcodeEntityDetailUpdate:
		gs.handleEntityDetailUpdate(msg)
	case protocol.OpcodeEntityStateRefresh:
		gs.handleEntityStateRefresh(msg)
	case protocol.OpcodeEntityDataUpdate:
		gs.handleEntityDataUpdate(msg)
	case protocol.OpcodeFullEntityRefresh:
		gs.handleFullEntityRefresh(msg)
	case protocol.GameOpcode(0x00B6): // Msg00B6: an ability's display index moved
		gs.handleAbilityDisplayMove(msg)

	// Entity update cascade (world recovery opcodes)
	case protocol.OpcodeEntityUpdateFullUI:
		gs.handleEntityUpdateFullUI(msg)
	case protocol.OpcodeEntityUpdateMapPanel, protocol.OpcodeEntityUpdateMapPanel2:
		gs.handleEntityUpdateMapPanel(msg)
	case protocol.OpcodeEntityUpdateAudio:
		gs.handleEntityUpdateAudio(msg)
	case protocol.OpcodeEntityUpdateMinimal:
		gs.handleEntityUpdateMinimal(msg)
	case protocol.OpcodeEntityUpdateDirect:
		gs.handleEntityUpdateDirect(msg)

	// Item/Inventory opcodes — parse and log items for DB population
	case protocol.OpcodeGrantXP:
		gs.handleGrantXPOrItems(msg)
	case protocol.OpcodeAddInvItem:
		gs.handleInventoryItem(msg, "AddInvItem")
	case protocol.OpcodeLoot: // 0x0018: a corpse's contents (Msg0018)
		gs.handleLoot(msg)
	case protocol.OpcodeRemoveInvItem: // 0x003C
		gs.handleRemoveInvItem(msg)
	case protocol.OpcodeInteractItem: // 0x003E (also the request's opcode)
		gs.handleUseItemResult(msg)
	case protocol.OpcodeClientLoot: // 0x0019
		gs.handleLootOne(msg)
	case protocol.OpcodeRemoveBankItem: // 0x1250
		gs.handleBankWithdrawn(msg)
	case protocol.OpcodeAddBankItem: // 0x1251
		gs.handleBankDeposited(msg)
	case protocol.OpcodeEquipItem:
		gs.handleEquipItemConfirm(msg)
	case protocol.OpcodeUnequipItem:
		gs.handleUnequipItem(msg)
	case protocol.OpcodeArrangeItem:
		gs.handleArrangeItem(msg)
	case protocol.OpcodeItemListUpdate:
		gs.handleItemListUpdate(msg)
	case protocol.OpcodePrepareItem:
		gs.handlePrepareItem(msg)

	// Loot opcodes
	case protocol.OpcodeLootResult:
		gs.handleLootResult(msg)
	case protocol.OpcodeLootOptions:
		gs.handleLootOptions(msg)
	case protocol.OpcodeClientCloseLoot:
		gs.handleClientCloseLoot(msg)

	// NPC interaction
	case protocol.OpcodeNPCInteraction:
		gs.handleNPCInteraction(msg)
	case protocol.OpcodeNPCMessage:
		gs.handleNPCMessage(msg)

	// HP/Resource update
	case protocol.OpcodeHPResourceUpdate:
		gs.handleHPResourceUpdate(msg)

	// Quest opcodes
	case protocol.OpcodeDialogueBox:
		gs.handleDialogueBox(msg)
	case protocol.OpcodeQuestPopupMulti:
		gs.handleQuestPopup(msg)
	case protocol.OpcodeAddQuestLog:
		gs.handleAddQuestLog(msg)
	case protocol.OpcodeQuestDialogueResp:
		gs.handleQuestDialogueResp(msg)
	case protocol.OpcodeUpdateQuestProgress:
		gs.handleUpdateQuestProgress(msg)
	case protocol.OpcodeDeleteQuest:
		gs.handleDeleteQuest(msg)
	case protocol.OpcodeClearQuestTracker:
		gs.handleClearQuestTracker(msg)
	case protocol.OpcodeQuestStageComplete:
		gs.handleQuestStageComplete(msg)
	case protocol.OpcodeStubNOP:
		gs.handleStubNOP(msg)

	// Combat/skill opcodes
	case protocol.OpcodeDamageIndication:
		gs.handleDamageIndication(msg)
	case protocol.OpcodePlayerSearchEntry:
		gs.handlePlayerSearchEntry(msg)
	case protocol.OpcodePlayerSearchClear:
		gs.PlayerSearch = gs.PlayerSearch[:0]
	case protocol.OpcodeResourceUpdate:
		gs.handleResourceUpdate(msg)
	case protocol.OpcodeCharacterDied:
		gs.handleCharacterDied(msg)
	case protocol.OpcodeCooldownTimer:
		gs.handleCooldownTimer(msg)
	case protocol.OpcodeCastingCombat:
		gs.handleCastingCombat(msg)
	case protocol.GameOpcode(0x0776): // Msg0776: a spell effect between two others
		gs.handlePlayEffect(msg)
	case protocol.OpcodeSkillBarAck:
		gs.handleSkillBarAck(msg)
	case protocol.OpcodeNPCInteractResult:
		gs.handleNPCInteractResult(msg)

	// Item durability / blacksmith
	case protocol.OpcodeAdjustItemHP:
		gs.handleAdjustItemHP(msg)
	case protocol.OpcodeBlackSmithMenu:
		gs.handleBlackSmithMenu(msg)
	case protocol.OpcodeCloseBlacksmithMenu:
		gs.handleCloseBlacksmith(msg)
	case protocol.OpcodeInventoryFull:
		gs.handleInventoryFull(msg)

	// Economy opcodes
	case protocol.OpcodePlayerTunar:
		gs.handlePlayerTunar(msg)
	case protocol.OpcodeMerchantBox:
		gs.handleMerchantBox(msg)
	case protocol.OpcodeTradeUI:
		gs.handleTradeUI(msg)
	case protocol.OpcodeTradeRequest:
		gs.handleTradeRequest(msg)

	// Bank / Auction
	case protocol.OpcodeBankUI:
		gs.handleBankUI(msg)
	case protocol.OpcodeConfirmBankTunar:
		gs.handleConfirmBankTunar(msg)
	case protocol.OpcodeAuctionStuff:
		gs.handleAuctionStuff(msg)

	// Who list
	case protocol.OpcodeWhoListResponse:
		gs.handleWhoListResponse(msg)
	case protocol.OpcodeWhoListFiltered:
		gs.handleWhoListFiltered(msg)

	// Weather
	case protocol.OpcodeWeatherControl:
		gs.handleWeatherControl(msg)
	case protocol.OpcodeSkyboxWeather:
		gs.handleSkyboxWeather(msg)

	// Social / Guild
	case protocol.OpcodeBuddyListEntry:
		gs.handleBuddyListEntry(msg)
	case protocol.OpcodeCollectionRemove:
		gs.handleBuddyOnlineStatus(msg)
	case protocol.OpcodeBuffNotification:
		gs.handleBuffNotification(msg)
	case protocol.OpcodeAddBuddyFailed:
		gs.handleAddBuddyFailed(msg)
	case protocol.OpcodeBuddyListStuff:
		gs.handleBuddyListStuff(msg)
	case protocol.OpcodeGuildInfo:
		gs.handleGuildInfo(msg)
	case protocol.OpcodeGuildInviteStuff:
		gs.handleGuildInviteStuff(msg)
	case protocol.OpcodeGuildWorldEntry:
		gs.handleGuildWorldEntry(msg)
	case protocol.OpcodeOutMatched:
		gs.handleOutMatched(msg)

	// Chat message (0x07A3)
	case protocol.OpcodeChatMessage:
		gs.handleChatMessage(msg)

	// Mail / Ignore
	case protocol.OpcodeMailDelivery:
		gs.handleMailDelivery(msg)
	case protocol.OpcodeIgnoreListResult:
		gs.handleIgnoreListResult(msg)

	// World entry / session
	case protocol.OpcodeServerLoginResponse:
		gs.handleServerLoginResponse(msg)
	case protocol.OpcodeReconnectWorldEntry:
		gs.handleReconnectWorldEntry(msg)
	case protocol.OpcodeWorldEntry:
		gs.handleWorldEntry(msg)
	case protocol.OpcodeStatsUpdate:
		gs.handleStatsUpdate(msg)
	case protocol.OpcodeZoneTransferInit:
		gs.handleZoneTransferInit(msg)
	case protocol.OpcodeZoneTransferConfirm:
		gs.handleZoneTransferConfirm(msg)

	// Force disconnect
	case protocol.OpcodeForceDisconnect:
		gs.handleForceDisconnect(msg)

	// Training points / Class mastery / Faction
	case protocol.OpcodeUpdateTrainingPoints:
		gs.handleUpdateTrainingPoints(msg)
	case protocol.OpcodeClassMasteryServer:
		gs.handleClassMasteryServer(msg)
	case protocol.OpcodeFactionPage:
		gs.handleFactionPage(msg)

	// Admin/GM opcodes
	case protocol.OpcodeAdminCode:
		gs.handleAdminCode(msg)
	case protocol.OpcodeAdministratorShutdown:
		gs.handleAdminShutdown(msg)

	// Disconnect opcodes — log prominently for debugging
	case protocol.OpcodeCombatEnd:
		gs.handleCombatEnd(msg)

	// Disconnect opcodes — log prominently for debugging
	case protocol.OpcodeCharacterModifiedDisconnect,
		protocol.OpcodeDisconnectClient, protocol.OpcodeLoggedInFromAnotherLocationDC:
		log.Printf("*** SERVER DISCONNECT: opcode=0x%04X dataLen=%d ***", uint16(opcode), len(msg.Data()))
		if len(msg.Data()) > 2 {
			log.Printf("*** DISCONNECT DATA: %X ***", msg.Data())
		}

	// Suppress noise
	case protocol.OpcodeBestEffortPing:
		// Silent
	case protocol.OpcodeServerReady:
		// Silent — server keepalive/ready signal, no payload
	case protocol.OpcodeIgnoreList:
		// Silent
	case protocol.OpcodeWorldUnavailable:
		log.Printf("WorldUnavailable: server reports world unavailable")

	// Catch-all: everything else is already logged by logOpcode above
	default:
		gs.handleUnknownOpcode(opcode, msg)
	}
}

// HandleStateChannel is VIClient::ProcessStateChannel (0x00BD3C18): channels
// 0x00-0x17 are the 24 entities (the player's own among them), 0x40 the
// group, 0x42 the stats, 0x43 the active effects and 0x44-0x6B forty records.
// Every other channel is read by nothing. An empty message still counts: it
// empties an entity slot or a record.
func (gs *GameState) HandleStateChannel(channel byte, msg *protocol.Message) {
	data := msg.Data()

	// Debug: log first state channel delivery per channel
	gs.stateChannelCount[channel]++
	count := gs.stateChannelCount[channel]
	if count <= 3 || count%100 == 0 {
		hexLen := len(data)
		if hexLen > 200 && channel != 0 {
			hexLen = 64
		}
		log.Printf("StateChannel 0x%02X: seq=%d xor=%d len=%d count=%d hex=%X",
			channel, msg.Sequence, msg.XORByte, len(data), count, data[:hexLen])
	}

	// State channel byte-diff monitor: log changes on the entity channels.
	// Always active — critical for combat RE. Tracks previous bytes per channel.
	if channel < 24 && len(data) > 0 {
		chIdx := int(channel)
		if gs.prevChannelState[chIdx] == nil {
			gs.prevChannelState[chIdx] = make([]byte, len(data))
			copy(gs.prevChannelState[chIdx], data)
		} else if len(data) == len(gs.prevChannelState[chIdx]) {
			prev := gs.prevChannelState[chIdx]
			var diffs []string
			for i := 0; i < len(data); i++ {
				if data[i] != prev[i] {
					diffs = append(diffs, fmt.Sprintf("[%d]%02X→%02X", i, prev[i], data[i]))
				}
			}
			if len(diffs) > 0 {
				// For entity channels, include the entity name
				label := "self"
				var objID uint32
				if len(data) >= 5 {
					objID = binary.LittleEndian.Uint32(data[1:5])
				}
				if objID != gs.PlayerObjectID {
					if ent, ok := gs.Entities[objID]; ok {
						label = ent.Name
					} else {
						label = fmt.Sprintf("obj=0x%08X", objID)
					}
				}
				log.Printf("STATE_DIFF ch=0x%02X(%s): %s", channel, label, strings.Join(diffs, " "))
			}
			copy(gs.prevChannelState[chIdx], data)
		}
	}

	switch {
	case channel < 24:
		gs.channelEntity(channel, data)
	case channel == 0x40:
		gs.decodeGroupPositions(data)
	case channel == 0x42:
		gs.decodeStatUpdate(data)
	case channel == 0x43:
		gs.decodeBuffUpdate(data)
	case channel >= 0x44 && channel < 0x6C:
		gs.decodeChannelRec(channel, data)
	default:
		if count == 1 {
			log.Printf("State channel 0x%02X: %d bytes (CLIENT reads nothing here)", channel, len(data))
		}
	}
}

func (gs *GameState) handleDiscVersion(msg *protocol.Message) {
	data := msg.Data()
	if len(data) == 0 {
		log.Printf("DiscVersion: empty data")
		return
	}
	// Sandstorm sends 1 byte (0x12 keepalive), SPIRE sends 6 bytes (2-byte opcode + uint32)
	// Anything larger is a misidentified message (opcode 0x0000 collision) — ignore
	if len(data) > 10 {
		return
	}
	if len(data) >= 6 {
		reader := buffer.NewReader(data[2:])
		version, _ := reader.ReadUint32()
		log.Printf("DiscVersion: server version 0x%08X (%d bytes)", version, len(data))
	} else if len(data) == 1 {
		// Keepalive — suppress log to reduce noise
	} else {
		log.Printf("DiscVersion: %d bytes, raw=%X", len(data), data)
	}
}

// clientLanguage is the language the shard list is filtered on. VIShardInfo
// compares each entry's language byte with its own (+0x10); the Go client
// speaks English, 0.
const clientLanguage = 0

// handleGameServers is the shard list, 0x07B3, which CLIENT reads only off a
// best-effort (0xFC) message and only before a shard is chosen
// (ProcessMessageFC: client state 0 or 1). VIShardInfo::Read (0x00BFC208):
// the count is ZIGZAG (VIMemStreamArray.h's ReadSigned), then per entry an
// 88-byte record from func_00CBBB40 -- a wide name (u32 length; 32
// characters or more leaves it empty), a "recommended" byte, an 8-byte id
// (the endpoint, port and reversed IP Go splits it into) and a signed
// language byte. func_00BFC290 then keeps only the entries in the client's
// language. Sandstorm's raw count byte 4 is two entries of the six it sends,
// languages 0 and 1, of which an English client shows one.
func (gs *GameState) handleGameServers(msg *protocol.Message) {
	switch gs.Phase {
	case PhaseConnecting, PhaseAuth, PhaseServerList:
	default:
		log.Printf("GameServers: ignored in phase %v (CLIENT reads a list only before a shard is chosen)", gs.Phase)
		return
	}
	data := msg.Data()
	if len(data) < 3 {
		log.Printf("GameServers: short data (%d bytes)", len(data))
		return
	}

	// Skip opcode (2 bytes)
	reader := buffer.NewReader(data[2:])

	count, err := reader.Read7BitEncodedInt64()
	if err != nil || count < 0 {
		count = 0
	}
	log.Printf("GameServers: %d servers", count)

	gs.Servers = nil
	for i := 0; i < int(count); i++ {
		// Name: int32 char count + UTF-16LE
		nameLen, err := reader.ReadUint32()
		if err != nil {
			break
		}
		var name string
		if nameLen+1 <= 32 {
			if name, err = reader.ReadStringUnicode(int(nameLen)); err != nil {
				break
			}
		} else if err = reader.Advance(int(nameLen) * 2); err != nil {
			break
		}

		recommended, _ := reader.ReadByte()
		endpointID, _ := reader.ReadUint16()
		port, _ := reader.ReadUint16()

		// IP (4 bytes reversed)
		ip3, _ := reader.ReadByte()
		ip2, _ := reader.ReadByte()
		ip1, _ := reader.ReadByte()
		ip0, err := reader.ReadByte()
		language, err2 := reader.ReadInt8()
		if err != nil || err2 != nil {
			break
		}

		ipStr := fmt.Sprintf("%d.%d.%d.%d", ip0, ip1, ip2, ip3)

		log.Printf("Server[%d]: '%s' recommended=%d endpoint=0x%04X port=%d ip=%s lang=%d",
			i, name, recommended, endpointID, port, ipStr, language)
		if language != clientLanguage {
			continue
		}

		gs.Servers = append(gs.Servers, ServerInfo{
			Name:       name,
			EndpointID: endpointID,
			Port:       port,
			IP:         ipStr,
		})
	}

	gs.Phase = PhaseServerList
	gs.StatusMsg = "Received server list"
}

func (gs *GameState) handleCamera1(msg *protocol.Message) {
	data := msg.Data()
	if len(data) >= 6 {
		reader := buffer.NewReader(data[2:])
		val, _ := reader.ReadInt32()
		log.Printf("Camera1: value=%d", val)

		// Camera1 with value -1 means "no camera target" (pre-character-select).
		// Do NOT change Phase here — Sandstorm sends Camera1(-1) after CharacterSelect,
		// which would overwrite PhaseCharSelect and prevent auto-select from triggering.
	}
	// Always send ACK response. Flush locks the connection's mutex on its first
	// line, so a nil Conn panics rather than no-opping — same crash the
	// world-entry batch in handleMemoryDump had.
	if gs.Conn != nil {
		gs.Conn.Flush()
	}
}

func (gs *GameState) handleCamera2(msg *protocol.Message) {
	log.Printf("Camera2 received")
	if gs.Conn != nil {
		gs.Conn.Flush()
	}
}

func (gs *GameState) handleCharacterSelect(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 3 {
		log.Printf("CharacterSelect: short data (%d bytes)", len(data))
		return
	}

	// Log raw bytes for debugging wire format
	hexLen := len(data)
	if hexLen > 64 {
		hexLen = 64
	}
	log.Printf("CharacterSelect: raw data (%d bytes): %X...", len(data), data[:hexLen])

	// Skip opcode (2 bytes)
	reader := buffer.NewReader(data[2:])

	// Character count is zigzag encoded, like the per-character fields below.
	// Confirmed against pcaps/ps2_sandstorm.pcap: count byte 0x0E for 7
	// characters. CLAUDE.md's "count is UNSIGNED" note is incorrect.
	count, _ := reader.Read7BitEncodedInt64()
	log.Printf("CharacterSelect: %d characters", count)

	gs.Characters = nil
	for i := 0; i < int(count); i++ {
		// Name: int32 length + UTF-8 string
		nameLen, _ := reader.ReadInt32()
		name, _ := reader.ReadStringUTF8(int(nameLen))

		// Character fields (zigzag)
		serverID, _ := reader.Read7BitEncodedInt64()
		modelID, _ := reader.Read7BitEncodedInt64()
		class, _ := reader.Read7BitEncodedInt64()
		race, _ := reader.Read7BitEncodedInt64()
		level, _ := reader.Read7BitEncodedInt64()

		// Appearance (zigzag)
		hairColor, _ := reader.Read7BitEncodedInt64()
		hairLength, _ := reader.Read7BitEncodedInt64()
		hairStyle, _ := reader.Read7BitEncodedInt64()
		faceOption, _ := reader.Read7BitEncodedInt64()

		// Equipment section (32 bytes)
		// Layout: [0-3] shield (uint32 LE, 0xFFFFFFFF=none)
		//         [4]   unknown
		//         [5-8] primary weapon (uint32 LE)
		//         [9-12] secondary weapon (uint32 LE)
		//         [13-16] unknown uint32
		//         [17-19] unknown bytes
		//         [20-26] chest/bracer/gloves/legs/boots/helm/robe (0xFF=none)
		//         [27-31] tints: chest/bracer/gloves/legs/boots
		equipBytes, _ := reader.ReadBytes(32)

		ci := CharInfo{
			ServerID: uint32(serverID),
			Name:     name,
			ModelID:  int32(modelID),
			Class:    int(class),
			Race:     int(race),
			Level:    int(level),

			HairColor:  byte(hairColor),
			HairLength: byte(hairLength),
			HairStyle:  byte(hairStyle),
			FaceOption: byte(faceOption),
		}

		if len(equipBytes) >= 32 {
			// Equipment block layout (confirmed via C# reference + wire analysis):
			//   [0-3]   Robe (int32 LE: -1/0xFFFFFFFF = no robe, 0-3 = style)
			//   [4]     Robe tint index (0-13, index into $Tints table)
			//   [5-8]   Primary weapon (int32 LE)
			//   [9-12]  Secondary weapon (int32 LE)
			//   [13-16] Shield (int32 LE)
			//   [17-19] Unknown bytes
			//   [20-25] Armor: chest/bracer/gloves/legs/boots/helm (0xFF=none)
			//   [26-31] Tints: chest/bracer/gloves/legs/boots/helm
			robeRaw := int32(equipBytes[0]) | int32(equipBytes[1])<<8 | int32(equipBytes[2])<<16 | int32(equipBytes[3])<<24
			ci.RobeTint = equipBytes[4]
			ci.PrimaryWeapon = uint32(equipBytes[5]) | uint32(equipBytes[6])<<8 | uint32(equipBytes[7])<<16 | uint32(equipBytes[8])<<24
			ci.SecondaryWeapon = uint32(equipBytes[9]) | uint32(equipBytes[10])<<8 | uint32(equipBytes[11])<<16 | uint32(equipBytes[12])<<24
			ci.Shield = uint32(equipBytes[13]) | uint32(equipBytes[14])<<8 | uint32(equipBytes[15])<<16 | uint32(equipBytes[16])<<24
			// Bytes 17-19: weapon AttackTypes (PS2 VICSpriteAttackType per slot)
			ci.PrimaryAttackType = equipBytes[17]
			ci.SecondaryAttackType = equipBytes[18]
			ci.ShieldAttackType = equipBytes[19]

			ci.Chest = equipBytes[20]
			ci.Bracer = equipBytes[21]
			ci.Gloves = equipBytes[22]
			ci.Legs = equipBytes[23]
			ci.Boots = equipBytes[24]
			ci.Helm = equipBytes[25]
			ci.ChestTint = equipBytes[26]
			ci.BracerTint = equipBytes[27]
			ci.GlovesTint = equipBytes[28]
			ci.LegsTint = equipBytes[29]
			ci.BootsTint = equipBytes[30]
			ci.HelmTint = equipBytes[31]

			// Robe: -1 (0xFFFFFFFF) = no robe, 0-3 = valid robe style index.
			if robeRaw >= 0 && robeRaw <= 3 {
				ci.Robe = byte(robeRaw)
			} else {
				ci.Robe = 0xFF // no robe
			}

			// 0xFFFFFFFF sentinel means "no weapon/shield"
			if ci.Shield == 0xFFFFFFFF {
				ci.Shield = 0
			}
			if ci.PrimaryWeapon == 0xFFFFFFFF {
				ci.PrimaryWeapon = 0
			}
			if ci.SecondaryWeapon == 0xFFFFFFFF {
				ci.SecondaryWeapon = 0
			}
		}

		log.Printf("Character[%d]: '%s' serverID=%d model=0x%X class=%d race=%d level=%d sex=%d",
			i, name, serverID, int32(modelID), class, race, level, SexFromModelID(uint32(modelID)))
		log.Printf("  appearance: hairColor=%d hairLength=%d hairStyle=%d faceOption=%d",
			ci.HairColor, ci.HairLength, ci.HairStyle, ci.FaceOption)
		log.Printf("  robe=%d robeTint=%d armor=[%d/%d/%d/%d/%d/%d] tints=[%d/%d/%d/%d/%d/%d] wpn=[0x%X/0x%X] shield=0x%X",
			ci.Robe, ci.RobeTint, ci.Chest, ci.Bracer, ci.Gloves, ci.Legs, ci.Boots, ci.Helm,
			ci.ChestTint, ci.BracerTint, ci.GlovesTint, ci.LegsTint, ci.BootsTint, ci.HelmTint,
			ci.PrimaryWeapon, ci.SecondaryWeapon, ci.Shield)
		log.Printf("  equipBlock[0:32]: %02X", equipBytes[:32])

		gs.Characters = append(gs.Characters, ci)
	}

	gs.Phase = PhaseCharSelect
	gs.StatusMsg = "Select a character"
}

func (gs *GameState) handleTime(msg *protocol.Message) {
	data := msg.Data()
	if len(data) >= 18 {
		reader := buffer.NewReader(data[2:])
		epoch1, _ := reader.ReadUint64()
		epoch2, _ := reader.ReadUint64()
		gs.TimeEpoch1 = epoch1
		gs.TimeEpoch2 = epoch2
		gs.TimeReceived = time.Now()
		gs.TimeValid = true
		log.Printf("Time: epoch1=%d epoch2=%d (delta=%dms)", epoch1, epoch2, epoch2-epoch1)
	}
}

// GameDayMs is the length of a game day in real milliseconds: CLIENT
// func_00BC7B70 takes the clock modulo 3,600,000, so a day lasts 60 minutes.
const GameDayMs = 3_600_000

// GameHour returns the current game hour (0.0-24.0) based on server time.
// Returns 12.0 (noon) if no time has been received yet.
//
// CLIENT func_00BC7B70 computes it every frame as
// (VISyncTime::Get(+0x2EC50) mod 3,600,000) / 3,600,000 * 24 and hands it to
// VIScene::SetTimeOfDay. The Time handler (func_00BD0508) syncs the SECOND
// timestamp into +0x2EC50, and VISyncTime::Get advances it at 1:1 from the
// moment it was synced, so the phase comes from epoch2 alone. The first
// timestamp (+0x2EC30) plays no part in the hour.
func (gs *GameState) GameHour() float32 {
	if !gs.TimeValid {
		return 12.0
	}
	elapsedMs := time.Since(gs.TimeReceived).Milliseconds()
	if elapsedMs < 0 {
		elapsedMs = 0
	}
	ms := (gs.TimeEpoch2 + uint64(elapsedMs)) % GameDayMs
	return float32(ms) / GameDayMs * 24
}

// SetGameHour pins the clock so GameHour reads h now and advances from there,
// as if a Time opcode had just carried that hour. Offline mode uses it.
func (gs *GameState) SetGameHour(h float32) {
	h = float32(math.Mod(float64(h), 24))
	if h < 0 {
		h += 24
	}
	gs.TimeEpoch2 = uint64(float64(h) / 24 * GameDayMs)
	gs.TimeEpoch1 = gs.TimeEpoch2
	gs.TimeReceived = time.Now()
	gs.TimeValid = true
}

func (gs *GameState) handleMemoryDump(msg *protocol.Message) {
	data := msg.Data()
	log.Printf("MemoryDump: %d bytes", len(data))

	// MemoryDump on respawn (death → bind point) clears dead state.
	// Death respawn uses MemoryDump (0x000D), not Teleport (0x00B2).
	gs.Player.Dead = false

	if len(data) < 20 {
		return
	}

	// ─── Section 1: Character Core ──────────────────────────────────────
	// PS2: func_00CBA850 reads serverID, serverName(UTF-8,24), class, race,
	//   level, xp, debt, breath, tunar, bankTunar, TP, baseMaxStat, world,
	//   x, y, z, facing(+2 floats), 3 more floats
	// Sandstorm: adds opcode prefix, unknown byte, zone path, char name
	reader := buffer.NewReader(data[2:]) // skip opcode prefix (2 bytes)

	reader.ReadByte() // Sandstorm-specific: unknown byte (always 0)

	// Zone file path (Sandstorm-specific, not in PS2 format)
	pathLen, _ := reader.ReadInt32()
	zonePath, _ := reader.ReadStringUTF8(int(pathLen))
	gs.ZonePath = zonePath
	log.Printf("MemoryDump: zone='%s'", zonePath)

	// Character core fields (PS2: func_00CBA850 at +0xC2DC)
	charServerID, _ := reader.Read7BitEncodedInt64() // PS2: +0x00
	nameLen, _ := reader.ReadInt32()                  // Sandstorm: char name (PS2 uses serverName here)
	charName, _ := reader.ReadStringUTF8(int(nameLen))
	class, _ := reader.Read7BitEncodedInt64()         // PS2: +0x1C
	race, _ := reader.Read7BitEncodedInt64()          // PS2: +0x20
	level, _ := reader.Read7BitEncodedInt64()         // PS2: +0x24
	xp, _ := reader.Read7BitEncodedInt64()            // PS2: +0x28 XP in level
	reader.Read7BitEncodedInt64()                     // PS2: +0x2C Debt
	breath, _ := reader.ReadByte()                    // PS2: +0x30 Breath (0=full, 255=drowning)
	tunar, _ := reader.Read7BitEncodedInt64()         // PS2: +0x34 Inventory tunar
	gs.Tunar = tunar
	reader.Read7BitEncodedInt64()                     // PS2: +0x38 Bank tunar
	gs.TrainingPoints, _ = reader.Read7BitEncodedInt64() // PS2: +0x3C Training points
	reader.Read7BitEncodedInt64()                     // PS2: +0x40 Base max stat
	worldID, _ := reader.Read7BitEncodedInt64()       // PS2: +0x44 World ID
	newWorld := byte(worldID)
	// PS2 continent correction: worldID 2 (Odus) maps to 3 (shared with Lavastorm).
	// Mega-function: if (entity->worldID - 2U < 2) entity->worldID = 3;
	if newWorld == 2 {
		newWorld = 3
	}
	if newWorld != gs.Player.World {
		gs.WorldChanged = true
		gs.PendingWorld = newWorld
		log.Printf("MemoryDump: WORLD CHANGE %d → %d", gs.Player.World, newWorld)
	}
	gs.Player.World = newWorld

	// Position (PS2: +0x48/4C/50 = X/Y/Z, +0x54 = facing + 2 unknown floats)
	x, _ := reader.ReadFloat32()
	y, _ := reader.ReadFloat32()
	z, _ := reader.ReadFloat32()
	facing, _ := reader.ReadFloat32()

	log.Printf("MemoryDump: name='%s' serverID=%d class=%d race=%d level=%d xp=%d pos=(%.1f, %.1f, %.1f) facing=%.2f",
		charName, charServerID, class, race, level, xp, x, y, z, facing)

	// Get ModelID from selected character's CharInfo (channel 0 doesn't carry it).
	var playerModelID uint32
	if gs.SelectedChar >= 0 && gs.SelectedChar < len(gs.Characters) {
		playerModelID = uint32(gs.Characters[gs.SelectedChar].ModelID)
	}

	// Preserve World through Entity reset — set above from MemoryDump.
	// Without this, Player.World resets to 0 and 0x40 sends wrong world byte.
	prevWorld := gs.Player.World

	// Preserve stat channel and appearance values through MemoryDump reset —
	// stat channel 0x42 may not re-send after respawn, and the char-select
	// appearance re-apply only runs on first world entry.
	prevHP, prevMaxHP := gs.Player.HP, gs.Player.MaxHP
	prevPower, prevMaxPower := gs.Player.Power, gs.Player.MaxPower
	prevHPInt, prevMaxHPInt := gs.Player.HPInt, gs.Player.MaxHPInt
	prevPowerInt, prevMaxPowerInt := gs.Player.PowerInt, gs.Player.MaxPowerInt
	prevEquip := gs.Player // snapshot all equipment/appearance fields

	gs.Player = Entity{
		ObjectID:       gs.PlayerObjectID,
		Name:           charName,
		X:              x,
		Y:              y,
		Z:              z,
		Facing:         facing,
		World:          prevWorld,
		ModelID:        playerModelID,
		Level:          byte(level),
		Class:          byte(class),
		Race:           byte(race),
		Sex:            SexFromModelID(playerModelID),
		XPRaw:          xp,
		XPPct:          xpLevelPct(int(level), xp),
		Breath:         breath,
		ServerTargetID: 0xFFFFFFFF, // no target initially
		IsPlayer:       true,
		HP: prevHP, MaxHP: prevMaxHP,
		Power: prevPower, MaxPower: prevMaxPower,
		HPInt: prevHPInt, MaxHPInt: prevMaxHPInt,
		PowerInt: prevPowerInt, MaxPowerInt: prevMaxPowerInt,
		// Preserve equipment and appearance through death/respawn
		Chest: prevEquip.Chest, Bracer: prevEquip.Bracer, Gloves: prevEquip.Gloves,
		Legs: prevEquip.Legs, Boots: prevEquip.Boots, Helm: prevEquip.Helm,
		Robe: prevEquip.Robe, RobeTint: prevEquip.RobeTint,
		ChestTint: prevEquip.ChestTint, BracerTint: prevEquip.BracerTint,
		GlovesTint: prevEquip.GlovesTint, LegsTint: prevEquip.LegsTint,
		BootsTint: prevEquip.BootsTint, HelmTint: prevEquip.HelmTint,
		PrimaryWeapon: prevEquip.PrimaryWeapon, SecondaryWeapon: prevEquip.SecondaryWeapon,
		Shield: prevEquip.Shield,
		HairColor: prevEquip.HairColor, HairLength: prevEquip.HairLength,
		HairStyle: prevEquip.HairStyle, FaceOption: prevEquip.FaceOption,
	}

	// Continue parsing full MemoryDump: hotkeys, quests, inventory, spells, etc.
	gs.parseMemoryDumpRemainder(reader)

	gs.MemoryDumpCount++
	gs.Phase = PhaseInWorld
	gs.StatusMsg = "In world"

	// Everything above is parsing and local state, and is worth keeping whether
	// or not there is a connection to answer on. The batch below is not: a nil
	// Conn here would panic inside QueueReliable, and a packet handler that
	// crashes the client is exactly what the dispatch guard exists to prevent.
	// Every other sender in this file already checks.
	if gs.Conn == nil {
		log.Printf("MemoryDump: no connection — world entry batch not sent")
		return
	}

	// Send the same world-entry opcode batch the PS2 client sends (all in ONE packet):
	// CharacterInWorld + ChangeChatMode + FactionMessages + LootMessages + first 0x40.
	// PS2 pcap shows all five in the same packet at world entry (~10.48s).
	// These likely trigger enteredGame() / EntityInRange initialization
	// on Sandstorm. Without them, entity channels 1-28 never arrive.
	gs.Conn.QueueReliable(protocol.OpcodeCharacterInWorld, nil)
	gs.Conn.QueueReliable(protocol.OpcodeChangeChatMode, []byte{0x00})        // chat mode = 0 (Normal)
	gs.Conn.QueueReliable(protocol.OpcodeFactionMessages, []byte{0x00, 0x00}) // uint16 count = 0
	gs.Conn.QueueReliable(protocol.OpcodeLootMessages, []byte{0x00, 0x00})    // uint16 count = 0
	log.Printf("Sent world entry batch: CharacterInWorld + ChangeChatMode + FactionMessages + LootMessages")
}

// parseMemoryDumpRemainder parses everything after position in the MemoryDump.
//
// PS2 MemoryDump section order (decompiled from func_00BD0630):
//   1. Character core      (func_00CBA850) — serverID, name, class, race, etc.
//   2. Hotkeys              (zigzag count + entries)
//   3. Quest data           (4 raw bytes + zigzag count + quest rows)
//   4. Inventory items      (ReadItemTable: zigzag count + 4 raw bytes + N×ReadItemRow)
//   5. Weapon hotbars       (6 × 4 × raw4 = 96 bytes fixed)
//   6. Bank items           (ReadItemTable: same as inventory)
//   7. Flag byte            (1 byte)
//   8. Abilities/Spells     (ReadSpellTable: zigzag count + N×ReadSpellRow)
//   9. Active effects       (ReadEffectTable: zigzag count + N × 32-byte rows)
//  10. Ignore list          (zigzag count + entries)
//  11. Skills data          (func_00BF71A0)
//  12. Stats/CM block       (func_00BDCDC0)
//
// Sandstorm differs: no weapon hotbar section, simplified quest format,
// Sandstorm-specific spell format (not shared item pattern), effects/ignore
// sent as empty counts between bank and spells.
func (gs *GameState) parseMemoryDumpRemainder(reader *buffer.Reader) {
	// PS2: func_00CBA850 reads 3 float triples (facing + 2 unknown + 3 more)
	// Sandstorm sends 2 extra floats after x,y,z,facing
	reader.ReadFloat32()
	reader.ReadFloat32()

	// ─── Section 2: Hotkeys ─────────────────────────────────────────────
	// PS2: zigzag count + N × { zigzag(dirCode) + 4 × (byte + UTF-16 + UTF-16) }
	hotkeyCount, err := reader.Read7BitEncodedInt64()
	if err != nil {
		log.Printf("MemoryDump: hotkeys: read count failed (remaining=%d)", reader.Remaining())
		return
	}
	log.Printf("MemoryDump: pos=%d hotkeyCount=%d", reader.Position(), hotkeyCount)
	if hotkeyCount > 0 && hotkeyCount < 200 {
		gs.Hotkeys = parseHotkeys(reader, hotkeyCount)
		log.Printf("MemoryDump: parsed %d hotkey entries", len(gs.Hotkeys))
	}

	// ─── Section 3: Quest Data ──────────────────────────────────────────
	// PS2: 4 raw bytes (quest metadata) + zigzag(count) + complex quest rows
	// Sandstorm: 2×int32(0) + uint32(count) + count × WriteStringUnicode(text)
	reader.ReadInt32() // quest metadata (always 0 on Sandstorm)
	reader.ReadInt32() // quest metadata (always 0 on Sandstorm)
	questCount, err := reader.ReadUint32()
	if err != nil {
		log.Printf("MemoryDump: quests: read count failed (remaining=%d)", reader.Remaining())
		return
	}
	log.Printf("MemoryDump: pos=%d questCount=%d", reader.Position(), questCount)
	gs.QuestTextIndex = 0
	for i := uint32(0); i < questCount && i < 8; i++ {
		textLen, err := reader.ReadInt32()
		if err != nil {
			log.Printf("MemoryDump: quest %d read error: %v", i, err)
			return
		}
		if textLen > 0 && textLen < 5000 {
			text, _ := reader.ReadStringUnicode(int(textLen))
			gs.QuestTexts[i] = text
			gs.QuestTextIndex = int(i) + 1
		}
	}

	// ─── Section 4: Inventory Items (CHARACTER_ITEMS) ───────────────────
	// PS2: ReadItemTable (zigzag count + 4 raw bytes + N × ReadItemRow)
	// The "4 raw bytes" are the duplicate uint32 count (Sandstorm writes count twice)
	invCount, err := reader.Read7BitEncodedInt64()
	if err != nil {
		log.Printf("MemoryDump: inventory: read count failed (remaining=%d)", reader.Remaining())
		return
	}
	invCountDup, _ := reader.ReadUint32() // PS2: 4 raw bytes = duplicate count
	gs.SetInventoryNextKey(invCountDup)   // VIItemList::m_p04: the key the next new record gets
	if int64(invCountDup) != invCount {
		log.Printf("MemoryDump: inventory count mismatch zigzag=%d dup=%d", invCount, invCountDup)
	}
	log.Printf("MemoryDump: pos=%d inventoryCount=%d", reader.Position(), invCount)

	gs.ParsedItems = nil
	for i := int64(0); i < invCount; i++ {
		item, err := parseSingleItem(reader)
		if err != nil {
			log.Printf("MemoryDump: inventory item %d parse error: %v (remaining=%d)", i, err, reader.Remaining())
			return
		}
		item.InvSlot = int(i)
		gs.ParsedItems = append(gs.ParsedItems, item)
	}
	gs.InventoryItemCount = len(gs.ParsedItems)
	log.Printf("MemoryDump: parsed %d inventory items", gs.InventoryItemCount)

	// Log equipped items with all fields (investigating proc spell FX DictIDs)
	for _, item := range gs.Inventory() {
		if item.EquipLocation >= 0 {
			log.Printf("  EQUIPPED: %q id=%d equipLoc=%d atk=%d dmg=%d p4c=%d p08=%d icon2=%d p18=%d p24=%d range=%d p58=%d",
				item.Name, item.ItemID, item.EquipLocation, item.AttackType, item.WeaponDamage,
				item.P4C, item.P08, item.Icon2, item.P18, item.P24, item.Range, item.P58)
		}
	}

	// ─── Section 5: Weapon Hotbars ──────────────────────────────────────
	// PS2: 6 × (4 × ReadBytes(4)) = 96 bytes fixed via func_00BFD178
	// Sandstorm: SKIPPED (not present in wire format)

	// ─── Section 6: Bank Items (CHARACTER_BANK_ITEMS) ───────────────────
	// Same format as inventory: zigzag count + uint32 count + N × DumpItem
	bankCount, err := reader.Read7BitEncodedInt64()
	if err != nil {
		log.Printf("MemoryDump: bank: read count failed (remaining=%d)", reader.Remaining())
		return
	}
	bankCountDup, _ := reader.ReadUint32()
	gs.SetBankNextKey(bankCountDup)
	log.Printf("MemoryDump: pos=%d bankCount=%d", reader.Position(), bankCount)

	for i := int64(0); i < bankCount; i++ {
		prePos := reader.Position()
		item, err := parseSingleItem(reader)
		if err != nil {
			log.Printf("MemoryDump: bank item %d parse error: %v (remaining=%d pos=%d)", i, err, reader.Remaining(), reader.Position())
			return
		}
		postPos := reader.Position()
		if i >= bankCount-3 || i < 3 {
			log.Printf("MemoryDump: bank[%d] %q size=%d pos=%d→%d stats=%d",
				i, item.Name, postPos-prePos, prePos, postPos, len(item.Stats))
		}
		item.InvSlot = len(gs.ParsedItems)
		gs.ParsedItems = append(gs.ParsedItems, item)
	}
	log.Printf("MemoryDump: parsed %d bank items (total items=%d)", bankCount, len(gs.ParsedItems))

	// ─── Section 7-10: Effects / Flag / Ignore ──────────────────────────
	// PS2 order: flag byte → abilities → effects → ignore list
	// Sandstorm sends 4 bytes here for empty sections (all zero counts):
	//   byte 0: unknown (0x00)
	//   byte 1: unknown (0x00)
	//   byte 2: flag byte (0x01)
	//   byte 3: unknown (0x00)
	// ─── Section D: Buffs + Flag + Spells ──────────────────────────────
	// Sandstorm format (verified via Dolph [00 00 01 00] and McGregor [00 02 ...]):
	//   byte(unk0) + zigzag(buffCount) + [buff rows] + byte(flag) + byte(unk1)
	// Then spells follow.
	unk0, _ := reader.ReadByte() // always 0x00
	buffCount, _ := reader.Read7BitEncodedInt64()
	log.Printf("MemoryDump: pos=%d unk0=%02X buffCount=%d remaining=%d",
		reader.Position(), unk0, buffCount, reader.Remaining())
	if buffCount > 0 && buffCount < 200 {
		for i := int64(0); i < buffCount; i++ {
			// Each buff row: try reading zigzag fields until we figure out the format.
			// PS2 func_00CA6848: 2 key zigzag + 3 data zigzag + 1 flag byte = 6 reads per buff.
			reader.Read7BitEncodedInt64() // key1
			reader.Read7BitEncodedInt64() // key2
			reader.Read7BitEncodedInt64() // field1
			reader.Read7BitEncodedInt64() // field2
			reader.Read7BitEncodedInt64() // field3
			reader.ReadByte()             // flag
			log.Printf("MemoryDump: skipped buff[%d] pos=%d", i, reader.Position())
		}
	}
	flagByte, _ := reader.ReadByte()
	unk1, _ := reader.ReadByte()
	log.Printf("MemoryDump: pos=%d flagByte=%02X unk1=%02X before spells", reader.Position(), flagByte, unk1)

	// ─── Section 8: Abilities/Spells (CHARACTER_ABILITIES) ──────────────
	// PS2: ReadSpellTable (zigzag count + N × ReadSpellRow)
	//   ReadSpellRow: 7 zigzag + UTF-8 name + shared item pattern (23 zigzag + 2 UTF-16 + stats)
	// Sandstorm: zigzag count + N × DumpSpell (16 zigzag + uint16 + 2 UTF-16 + terminator)
	spellCount, err := reader.Read7BitEncodedInt64()
	if err != nil {
		log.Printf("MemoryDump: spells: read count failed (remaining=%d)", reader.Remaining())
		return
	}
	log.Printf("MemoryDump: pos=%d spellCount=%d", reader.Position(), spellCount)
	if spellCount < 0 || spellCount > 500 {
		log.Printf("MemoryDump: spells: bad count %d, skipping", spellCount)
		return
	}

	gs.ParsedSpells = nil
	for i := int64(0); i < spellCount; i++ {
		sp, err := parseSingleSpell(reader)
		if err != nil {
			log.Printf("MemoryDump: spell %d parse error: %v (remaining=%d)", i, err, reader.Remaining())
			return
		}
		gs.ParsedSpells = append(gs.ParsedSpells, sp)
	}
	log.Printf("MemoryDump: parsed %d spells", len(gs.ParsedSpells))
	gs.BuildHotbar()

	// Log items and spells to /tmp for debugging
	for _, item := range gs.ParsedItems {
		statStr := ""
		for _, st := range item.Stats {
			if statStr != "" {
				statStr += ","
			}
			statStr += fmt.Sprintf("%d=%d", st.ID, st.Value)
		}
		writeItemLog("MemDump", fmt.Sprintf("ITEM id=%d name=%q slot=%d equipLoc=%d quick=%d cost=%d icon=%d lvl=%d atk=%d dmg=%d stack=%d p4c=0x%X stats={%s}",
			item.ItemID, item.Name, item.Slot, item.EquipLocation, item.QuickSlot, item.ItemCost, item.ItemIcon, item.LevelReq, item.AttackType, item.WeaponDamage, item.MaxStack, item.P4C, statStr))
	}
	for _, sp := range gs.ParsedSpells {
		writeSpellLog(fmt.Sprintf("SPELL id=%d name=%q ability=%d addedOrder=%d range=%d cast=%d power=%d icon=%d scope=%d recast=%d",
			sp.SpellID, sp.Name, sp.AbilityLevel, sp.AddedOrder, sp.Range, sp.CastTime, sp.Power, sp.Icon, sp.Scope, sp.Recast))
	}

	// ─── Section 11-12: Skills / Stats / CM ─────────────────────────────
	// PS2: func_00BF71A0 (skills) + func_00BDCDC0 (stats block)
	// Sandstorm: speed(float) + 26×int32 skills + CM zigzag fields
	if reader.Remaining() < 112 {
		log.Printf("MemoryDump: COMPLETE — %d items, %d spells, no CM section (%d bytes remaining)",
			len(gs.ParsedItems), len(gs.ParsedSpells), reader.Remaining())
		return
	}

	cm := &ParsedCMData{}
	cm.Speed, _ = reader.ReadFloat32()

	// 26 int32 skill/craft values (104 bytes) — PS2: CHARACTER_SKILLS table
	for i := 0; i < 26; i++ {
		v, _ := reader.ReadInt32()
		cm.Skills[i] = v
	}

	// CM fields — all zigzag encoded (PS2: func_00BDCDC0)
	cm.UnspentCMs, _ = reader.Read7BitEncodedInt64()
	cm.SpentCMs, _ = reader.Read7BitEncodedInt64()
	cm.CMUnknown, _ = reader.Read7BitEncodedInt64()
	cm.CMPercentage, _ = reader.Read7BitEncodedInt64()
	cm.Speed2, _ = reader.ReadFloat32()

	// Base stat additions from CM investments (7 stats: STR STA AGI DEX WIS INT CHA)
	cm.StrAdd, _ = reader.Read7BitEncodedInt64()
	cm.StaAdd, _ = reader.Read7BitEncodedInt64()
	cm.AgiAdd, _ = reader.Read7BitEncodedInt64()
	cm.DexAdd, _ = reader.Read7BitEncodedInt64()
	cm.WisAdd, _ = reader.Read7BitEncodedInt64()
	cm.IntAdd, _ = reader.Read7BitEncodedInt64()
	cm.ChaAdd, _ = reader.Read7BitEncodedInt64()

	// CM stat maximums (7 stats)
	cm.StrMax, _ = reader.Read7BitEncodedInt64()
	cm.StaMax, _ = reader.Read7BitEncodedInt64()
	cm.AgiMax, _ = reader.Read7BitEncodedInt64()
	cm.DexMax, _ = reader.Read7BitEncodedInt64()
	cm.WisMax, _ = reader.Read7BitEncodedInt64()
	cm.IntMax, _ = reader.Read7BitEncodedInt64()
	cm.ChaMax, _ = reader.Read7BitEncodedInt64()

	// HP/Power additions
	cm.HPAdd, _ = reader.Read7BitEncodedInt64()
	cm.PowerAdd, _ = reader.Read7BitEncodedInt64()

	// HoT/PoT
	cm.HoT, _ = reader.Read7BitEncodedInt64()
	cm.PoT, _ = reader.Read7BitEncodedInt64()

	// AC addition
	cm.ACAdd, _ = reader.Read7BitEncodedInt64()

	// Offensive/Defensive/HP Factor mods
	cm.OFFMod, _ = reader.Read7BitEncodedInt64()
	cm.DEFMod, _ = reader.Read7BitEncodedInt64()
	cm.HPFactor, _ = reader.Read7BitEncodedInt64()

	// Resist additions (6 resists: FR LR CR AR PR DR)
	cm.FRAdd, _ = reader.Read7BitEncodedInt64()
	cm.LRAdd, _ = reader.Read7BitEncodedInt64()
	cm.CRAdd, _ = reader.Read7BitEncodedInt64()
	cm.ARAdd, _ = reader.Read7BitEncodedInt64()
	cm.PRAdd, _ = reader.Read7BitEncodedInt64()
	cm.DRAdd, _ = reader.Read7BitEncodedInt64()

	// Additional Sandstorm CM/stat fields (47 remaining zigzag values)
	// These are likely additional combat modifiers, all zero for low-level characters.
	trailingCount := 0
	for reader.Remaining() > 0 {
		reader.Read7BitEncodedInt64()
		trailingCount++
	}

	gs.ParsedCM = cm

	log.Printf("MemoryDump: CM speed=%.2f unspent=%d spent=%d pct=%d%% (+%d trailing fields)",
		cm.Speed, cm.UnspentCMs, cm.SpentCMs, cm.CMPercentage, trailingCount)
	log.Printf("MemoryDump: CM stats STR=%d/%d STA=%d/%d AGI=%d/%d DEX=%d/%d WIS=%d/%d INT=%d/%d CHA=%d/%d",
		cm.StrAdd, cm.StrMax, cm.StaAdd, cm.StaMax, cm.AgiAdd, cm.AgiMax, cm.DexAdd, cm.DexMax,
		cm.WisAdd, cm.WisMax, cm.IntAdd, cm.IntMax, cm.ChaAdd, cm.ChaMax)
	log.Printf("MemoryDump: CM combat HP=%d Power=%d AC=%d OFF=%d DEF=%d HPF=%d HoT=%d PoT=%d",
		cm.HPAdd, cm.PowerAdd, cm.ACAdd, cm.OFFMod, cm.DEFMod, cm.HPFactor, cm.HoT, cm.PoT)
	log.Printf("MemoryDump: CM resists FR=%d LR=%d CR=%d AR=%d PR=%d DR=%d",
		cm.FRAdd, cm.LRAdd, cm.CRAdd, cm.ARAdd, cm.PRAdd, cm.DRAdd)

	log.Printf("MemoryDump: COMPLETE — %d items, %d spells, CM data parsed",
		len(gs.ParsedItems), len(gs.ParsedSpells))
}

// writeSpellLog appends a line to /tmp/spells.log.
func writeSpellLog(text string) {
	f, err := os.OpenFile("/tmp/spells.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	ts := time.Now().Format("15:04:05")
	f.WriteString(fmt.Sprintf("[%s] %s\n", ts, text))
}

func (gs *GameState) handleTeleport(msg *protocol.Message) {
	data := msg.Data()
	// Wire format: uint16 opcode + byte world + float32 x,y,z,facing + 8 padding + uint32 counter
	if len(data) < 21 {
		log.Printf("Teleport: short data (%d bytes)", len(data))
		return
	}
	// Msg00B2 (0x00BD38C8): u8 world, the position and the HPR as two
	// VIVect3s (the heading is the HPR's first), then a u32 the client sends
	// straight back, zigzag, as 0x0049 — 0x49 is 73, SendOpSigned(73, x).
	reader := buffer.NewReader(data[2:]) // skip opcode
	world, _ := reader.ReadByte()
	x, _ := reader.ReadFloat32()
	y, _ := reader.ReadFloat32()
	z, _ := reader.ReadFloat32()
	facing, _ := reader.ReadFloat32()
	reader.Advance(8) // pitch, roll
	token, _ := reader.ReadUint32()
	gs.TeleportToken = int32(token)

	gs.Player.X = x
	gs.Player.Y = y
	gs.Player.Z = z
	gs.Player.Facing = facing

	// Respawn clears dead state (death → teleport to bind point)
	gs.Player.Dead = false

	// Clear dialogue state
	gs.DialogueActive = false
	gs.DialogueOptions = nil
	gs.MerchantOpen = false // CLIENT closes the merchant (and the bank, trade, loot)

	// Clear all entities from previous zone — PS2 VIEntity visibility is purely
	// state-channel-driven, so old entities are invalid after zone transition.
	oldCount := len(gs.Entities)
	for id := range gs.Entities {
		gs.SpellFXSys.NotifyActorWasDestroyed(id)
	}
	gs.Entities = make(map[uint32]*Entity)
	gs.ChannelEntity = [29]uint32{}

	// Reset state channel debug counters for entity channels
	for ch := byte(1); ch <= 28; ch++ {
		delete(gs.stateChannelCount, ch)
	}

	// Do NOT reset XOR delta bases on teleport. The server's XOR base
	// (stc.seqnum2) still references our last ACK'd sequence. If we clear
	// our bases, the server keeps sending deltas we can't decode (xorByte
	// grows 1,2,3,...) until it eventually sends a keyframe (20-40 seconds).
	// Instead, keep the delta chain intact — the server will send new entity
	// data on these channels through normal XOR deltas, and the decoded
	// result will naturally contain the new entity data.

	// Trigger loading screen (coaching/GM teleport zone transition)
	gs.MemoryDumpCount++

	// Signal engine to send EnableChannel + fresh 0x40 at new position
	gs.TeleportPending = true
	gs.TeleportWorld = world

	// Signal client to reset physics state (velocity, ground detection).
	// GameState can't access Client fields directly, so set a flag.
	gs.TeleportPhysicsReset = true

	// Detect world change (coaching, dockmaster travel)
	if world != gs.Player.World {
		gs.WorldChanged = true
		gs.PendingWorld = world
		log.Printf("Teleport: WORLD CHANGE %d → %d", gs.Player.World, world)
	}
	gs.Player.World = world

	log.Printf("Teleport: world=%d pos=(%.1f, %.1f, %.1f) facing=%.2f — cleared %d entities",
		world, x, y, z, facing, oldCount)
}

func (gs *GameState) handlePlayerSpeed(msg *protocol.Message) {
	data := msg.Data()
	if len(data) >= 6 {
		reader := buffer.NewReader(data[2:])
		speed, _ := reader.ReadFloat32()
		gs.Player.Speed = speed
		log.Printf("PlayerSpeed: %.1f", speed)
	}
}

// handleAdminCode processes AdminCode (0x0700) from the server.
// PS2 VIWndGame uses this to enable/disable GM menu items.
// Wire format: uint16 opcode + byte type + varint code.
func (gs *GameState) handleAdminCode(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 4 {
		log.Printf("AdminCode: short data (%d bytes)", len(data))
		return
	}
	menuType := data[2]
	// Code is 7-bit encoded starting at byte 3
	code := int64(0)
	shift := uint(0)
	for i := 3; i < len(data); i++ {
		b := data[i]
		code |= int64(b&0x7F) << shift
		shift += 7
		if b&0x80 == 0 {
			break
		}
	}
	log.Printf("AdminCode: type=%d code=%d", menuType, code)

	// Store for GM menu use — the server signals which admin features are available
	gs.AdminMenuType = menuType
	gs.AdminMenuCode = code
}

// readUnicodeString reads an int32 charCount + UTF-16LE string from data at the given offset.
// Returns the decoded string and the number of bytes consumed.
func readUnicodeString(data []byte, offset int) (string, int) {
	if offset+4 > len(data) {
		return "", 0
	}
	charCount := int(int32(binary.LittleEndian.Uint32(data[offset:])))
	if charCount <= 0 {
		return "", 4
	}
	strStart := offset + 4
	strEnd := strStart + charCount*2
	if strEnd > len(data) {
		return "", 4
	}
	// Decode UTF-16LE
	runes := make([]rune, charCount)
	for i := 0; i < charCount; i++ {
		runes[i] = rune(binary.LittleEndian.Uint16(data[strStart+i*2:]))
	}
	return string(runes), 4 + charCount*2
}

func (gs *GameState) handleColoredChat(msg *protocol.Message) {
	data := msg.Data()
	// Wire format: opcode(2) + int32(charCount) + UTF-16LE(chars) + 4 color bytes
	if len(data) < 6 {
		return
	}
	text, consumed := readUnicodeString(data, 2) // skip 2-byte opcode prefix
	if text == "" {
		return
	}

	// Read color bytes (4 bytes after the string)
	colorOff := 2 + consumed
	var r, g, b uint8
	if colorOff+4 <= len(data) {
		r = data[colorOff]
		g = data[colorOff+1]
		b = data[colorOff+2]
	} else {
		r, g, b = 63, 255, 63 // default shout green
	}

	// Determine chat channel from color bytes.
	// Sandstorm uses specific colors for different channels:
	//   Shout:  green (63, 255, 63) or (255, 247, 0) = default
	//   Guild:  typically teal/cyan or custom guild color
	//   Server: varies (welcome messages, etc)
	// The last 2 bytes after RGB may be channel flags.
	channel := ChatShout
	colorOff2 := colorOff + 3
	if colorOff2 < len(data) {
		flagByte := data[colorOff2]
		// Check known Sandstorm flag patterns
		if flagByte == 0xF7 {
			channel = ChatSystem // server message (0xFFF7 = system)
		}
	}
	// Detect channel from text content (Sandstorm embeds channel in the message text)
	if strings.Contains(text, " tells you:") {
		channel = ChatTell
	} else if strings.Contains(text, " shouts:") {
		channel = ChatShout
	} else if r == 255 && g == 63 && b == 63 {
		channel = ChatError
	} else if r == 48 && g == 244 && b == 255 {
		channel = ChatSystem // auction/system cyan
	}

	log.Printf("ColoredChat: %s (color: %d,%d,%d ch=%d)", text, r, g, b, channel)
	gs.ChatLog = append(gs.ChatLog, ChatMessage{
		Text:    text,
		Channel: channel,
		ColorR:  r, ColorG: g, ColorB: b,
	})
}

func (gs *GameState) handleClientMessage(msg *protocol.Message) {
	data := msg.Data()
	// Wire format: opcode(2) + int32(charCount) + UTF-16LE(chars)
	// On Sandstorm, 0x0A7A is used for guild chat messages.
	// Text includes "SenderName: message".
	if len(data) < 6 {
		return
	}
	text, _ := readUnicodeString(data, 2) // skip 2-byte opcode prefix
	if text == "" {
		return
	}

	log.Printf("ClientMessage[guild]: %s", text)
	gs.addChatMessage(fmt.Sprintf("[Guild] %s", text), ChatGuild)
}

func (gs *GameState) handleErrorMessage(msg *protocol.Message) {
	data := msg.Data()
	// Wire format: opcode(2) + int32(charCount) + UTF-16LE(chars)
	if len(data) < 6 {
		return
	}
	text, _ := readUnicodeString(data, 2)
	if text == "" {
		return
	}

	log.Printf("ErrorMessage: %s", text)
	gs.ChatLog = append(gs.ChatLog, ChatMessage{
		Text:    text,
		Channel: ChatError,
		ColorR:  255, ColorG: 0, ColorB: 0,
	})
}

func (gs *GameState) handleGroupChat(msg *protocol.Message) {
	data := msg.Data()
	// Wire format: opcode(2) + uint32(senderOID) + uint32(charCount) + UTF-16LE(chars)
	if len(data) < 10 {
		return
	}
	text, _ := readUnicodeString(data, 6) // skip opcode(2) + senderOID(4)
	if text == "" {
		return
	}

	log.Printf("GroupChat: %s", text)
	gs.ChatLog = append(gs.ChatLog, ChatMessage{
		Text:    text,
		Channel: ChatGroup,
		ColorR:  100, ColorG: 180, ColorB: 255,
	})
}

// handleGroupInvite processes a group invite from another player (opcode 0x0773).
// Wire: opcode(2) + uint32 groupID + uint32 inviterOID + uint32 nameLen + ASCII name
func (gs *GameState) handleGroupInvite(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 14 { // 2 + 4 + 4 + 4 minimum
		log.Printf("GroupInvite: short data (%d bytes)", len(data))
		return
	}
	groupID := binary.LittleEndian.Uint32(data[2:6])
	// inviterOID at data[6:10] — not stored, we use name
	nameLen := binary.LittleEndian.Uint32(data[10:14])
	if len(data) < int(14+nameLen) {
		log.Printf("GroupInvite: name truncated (need %d, have %d)", 14+nameLen, len(data))
		return
	}
	name := string(data[14 : 14+nameLen])

	gs.GroupInviteID = groupID
	gs.GroupInviter = name
	log.Printf("GroupInvite: %s invited you to group %d", name, groupID)
	gs.ChatLog = append(gs.ChatLog, ChatMessage{
		Text:    fmt.Sprintf("%s has invited you to a group. Type /accept or /decline", name),
		Channel: ChatSystem,
		ColorR:  255, ColorG: 255, ColorB: 100,
	})
}

// handleCreateGroup processes group roster creation/update (opcode 0x0772).
// Wire: opcode(2) + uint32 groupID + uint32 count + count×uint32 OIDs + count×(uint32 nameLen + ASCII name)
func (gs *GameState) handleCreateGroup(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 10 { // 2 + 4 + 4 minimum
		log.Printf("CreateGroup: short data (%d bytes)", len(data))
		return
	}
	groupID := binary.LittleEndian.Uint32(data[2:6])
	count := binary.LittleEndian.Uint32(data[6:10])
	if count > 6 {
		log.Printf("CreateGroup: suspicious count %d, capping at 6", count)
		count = 6
	}

	offset := 10
	// Read OIDs
	oids := make([]uint32, count)
	for i := uint32(0); i < count; i++ {
		if offset+4 > len(data) {
			log.Printf("CreateGroup: truncated at OID %d", i)
			return
		}
		oids[i] = binary.LittleEndian.Uint32(data[offset : offset+4])
		offset += 4
	}

	// Read names
	members := make([]GroupMember, count)
	for i := uint32(0); i < count; i++ {
		if offset+4 > len(data) {
			log.Printf("CreateGroup: truncated at name %d", i)
			return
		}
		nameLen := binary.LittleEndian.Uint32(data[offset : offset+4])
		offset += 4
		if offset+int(nameLen) > len(data) {
			log.Printf("CreateGroup: name %d truncated", i)
			return
		}
		members[i] = GroupMember{
			ObjectID: oids[i],
			Name:     string(data[offset : offset+int(nameLen)]),
		}
		offset += int(nameLen)
	}

	gs.GroupActive = true
	gs.GroupID = groupID
	gs.GroupMembers = members
	gs.GroupInviteID = 0
	gs.GroupInviter = ""

	var names []string
	for _, m := range members {
		names = append(names, m.Name)
	}
	log.Printf("CreateGroup: groupID=%d members=[%s]", groupID, strings.Join(names, ", "))
	gs.ChatLog = append(gs.ChatLog, ChatMessage{
		Text:    fmt.Sprintf("Group formed: %s", strings.Join(names, ", ")),
		Channel: ChatSystem,
		ColorR:  255, ColorG: 255, ColorB: 100,
	})
}

// handleRemoveGroupMember processes a member removal from the group (opcode 0x069A).
// Wire: opcode(2) + byte slot
// The byte is the server's internal group slot, NOT our array index.
// Slot 0 = self removed (group disbanded). Other values = a non-self member left.
func (gs *GameState) handleRemoveGroupMember(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 3 { // 2 + 1
		log.Printf("RemoveGroupMember: short data (%d bytes)", len(data))
		return
	}
	slot := data[2]
	log.Printf("RemoveGroupMember: slot=%d members=%d", slot, len(gs.GroupMembers))

	// Slot 0 = self removed from group
	if slot == 0 {
		gs.GroupActive = false
		gs.GroupID = 0
		gs.GroupMembers = nil
		gs.ChatLog = append(gs.ChatLog, ChatMessage{
			Text:    "You have left the group.",
			Channel: ChatSystem,
			ColorR:  255, ColorG: 255, ColorB: 100,
		})
		return
	}

	// Non-zero slot = another member left. Remove the first non-self member
	// we find (in a 2-person group there's only one). For larger groups the
	// server resends CreateGroup with the updated roster.
	for i, m := range gs.GroupMembers {
		if m.ObjectID != gs.PlayerObjectID {
			gs.GroupMembers = append(gs.GroupMembers[:i], gs.GroupMembers[i+1:]...)
			if len(gs.GroupMembers) <= 1 {
				gs.GroupActive = false
				gs.GroupID = 0
				gs.GroupMembers = nil
				gs.ChatLog = append(gs.ChatLog, ChatMessage{
					Text:    fmt.Sprintf("%s left the group. Group dissolved.", m.Name),
					Channel: ChatSystem,
					ColorR:  255, ColorG: 255, ColorB: 100,
				})
			} else {
				gs.ChatLog = append(gs.ChatLog, ChatMessage{
					Text:    fmt.Sprintf("%s has left the group.", m.Name),
					Channel: ChatSystem,
					ColorR:  255, ColorG: 255, ColorB: 100,
				})
			}
			return
		}
	}
}

// handleDisbandGroup processes a server group disband (opcode 0x062F).
// Sent when the group is dissolved (e.g. last other member leaves).
func (gs *GameState) handleDisbandGroup(msg *protocol.Message) {
	log.Printf("DisbandGroup: group %d disbanded by server", gs.GroupID)
	gs.GroupActive = false
	gs.GroupID = 0
	gs.GroupMembers = nil
	gs.ChatLog = append(gs.ChatLog, ChatMessage{
		Text:    "Your group has been disbanded.",
		Channel: ChatSystem,
		ColorR:  255, ColorG: 255, ColorB: 100,
	})
}

// handleClassicTargetInfo is 0x0010 (Msg0010, 0x00BD9D60): three bytes —
// the disposition (FrontiersForge 0..6; Go: face), then +0x2BB70 (Go: con)
// and +0x2BB74 — two 64-character wide strings, then a u32 that must equal
// the request's first word (Go sends 0) for the three bytes to land. The
// strings are stored either way. Sandstorm sends no u32 at all (149 of 149
// captured replies are 261 bytes), so the PS2 compares an uninitialised
// local; Go takes a missing one as a match.
func (gs *GameState) handleClassicTargetInfo(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 5 {
		log.Printf("ClassicTargetInfo: short data (%d bytes)", len(data))
		return
	}
	disp, con, flag := data[2], data[3], data[4]
	if len(data) >= 5+256 {
		gs.TargetTitle = decodeUTF16LE(data[5:133])
		gs.TargetGuildName = decodeUTF16LE(data[133:261])
	}
	if len(data) >= 5+256+4 && binary.LittleEndian.Uint32(data[261:265]) != 0 {
		log.Printf("ClassicTargetInfo: reply for request %d, not ours", binary.LittleEndian.Uint32(data[261:265]))
		return
	}
	gs.TargetFaceColor = disp
	gs.TargetConColor = con
	log.Printf("ClassicTargetInfo: dataLen=%d faceColor=%d conColor=%d flag=%d guild=%q title=%q",
		len(data), disp, con, flag, gs.TargetGuildName, gs.TargetTitle)
}

// decodePlayerState decodes channel 0 (player self) state data.
func (gs *GameState) decodePlayerState(data []byte) {
	if len(data) < 16 {
		return
	}

	gs.PlayerObjectID = uint32(data[1]) | uint32(data[2])<<8 | uint32(data[3])<<16 | uint32(data[4])<<24

	// Position: 3-byte big-endian signed mediums at offsets 6, 9, 12 (value / 128.0)
	// Only update position from server if we don't have a MemoryDump position yet.
	// Once spawned, the Go client owns its position (no PS2 gravity to fight).
	newY := decodeMediumBE(data[9:12])
	if !gs.WorldEntryAcked {
		gs.Player.X = decodeMediumBE(data[6:9])
		gs.Player.Y = newY
		gs.Player.Z = decodeMediumBE(data[12:15])
		log.Printf("decodePlayerState: ACCEPTED pos Y=%.4f (WorldEntryAcked=%v)", newY, gs.WorldEntryAcked)
	} else if newY != gs.Player.Y {
		log.Printf("decodePlayerState: IGNORED server Y=%.4f (our Y=%.4f)", newY, gs.Player.Y)
	}

	// Facing: offset 15 (0-255 mapped to 0-2*pi)
	// After world entry, client owns facing — ignore stale server values.
	if !gs.WorldEntryAcked {
		gs.Player.Facing = float32(data[15]) / 256.0 * 2 * 3.14159265
	}

	// World: offset 16 (continent ID)
	if len(data) >= 18 {
		gs.Player.World = data[17]
	}

	// HP: offset 26 flag (1=alive, 3=dead), offset 27 bar (0-255).
	if len(data) >= 28 {
		if data[26] == 3 && gs.Player.HPFlag != 3 {
			gs.SpellFXSys.StopIfThisWasCaster(gs.PlayerObjectID) // CLIENT: after SetDeath
		}
		gs.Player.HPFlag = data[26]
		gs.Player.HP = data[27]
		gs.Player.MaxHP = 255
	}

	// ModelID: offset 28-31 (uint32 LE) — changes during form/illusion spells.
	// PS2 pcap verified: server changes this from e.g. 0x70D898C6 (Human male) to
	// 0x8150E6AF (mouse form) when an illusion spell is active.
	if len(data) >= 32 {
		newModelID := binary.LittleEndian.Uint32(data[28:32])
		if newModelID != 0 && newModelID != gs.Player.ModelID {
			log.Printf("decodePlayerState: ModelID changed 0x%08X → 0x%08X (form change)", gs.Player.ModelID, newModelID)
			gs.Player.ModelID = newModelID
		}
	}

	// Scale: offset 36-39 (float32 LE)
	if len(data) >= 40 {
		gs.Player.Scale = math.Float32frombits(binary.LittleEndian.Uint32(data[36:40]))
	}

	// Animation: offset 58 — server's view of player animation state.
	// Client overrides this visually from input, but useful for diagnostics.
	if len(data) >= 59 {
		gs.Player.Animation = data[58]
	}

	// Target ObjectID: offset 59-62 — server's view of who we're targeting.
	// 0xFFFFFFFF = no target. Used to confirm ForcePositionFlush communicated target.
	if len(data) >= 63 {
		serverTarget := binary.LittleEndian.Uint32(data[59:63])
		if gs.Player.ServerTargetID != serverTarget {
			log.Printf("decodePlayerState: server target changed 0x%08X → 0x%08X", gs.Player.ServerTargetID, serverTarget)
			gs.Player.ServerTargetID = serverTarget
		}
	}

	// Combat flags: offset 63 = CombatEngaged, 64 = active weapon AttackType during combat
	if len(data) >= 65 {
		gs.Player.CombatEngaged = data[63] != 0
		gs.Player.CombatAttackType = data[64]
	}

	// Equipment: weapon model IDs at offsets 67-78, armor bytes at 93-98
	if len(data) >= 79 {
		gs.Player.PrimaryWeapon = binary.LittleEndian.Uint32(data[67:71])
		gs.Player.SecondaryWeapon = binary.LittleEndian.Uint32(data[71:75])
		gs.Player.Shield = binary.LittleEndian.Uint32(data[75:79])
	}
	// Weapon AttackType from state channel byte 91/92 (same as char select byte 17/18).
	// Only update from state channel if not already set from char select (which has full data).
	if len(data) >= 93 && gs.Player.PrimaryAttackType == 0 {
		gs.Player.PrimaryAttackType = data[91]
		gs.Player.SecondaryAttackType = data[92]
	}
	if len(data) >= 99 {
		gs.Player.Chest = data[93]
		gs.Player.Bracer = data[94]
		gs.Player.Gloves = data[95]
		gs.Player.Legs = data[96]
		gs.Player.Boots = data[97]
		gs.Player.Helm = data[98]
	}
	if len(data) >= 105 {
		gs.Player.ChestTint = data[99]
		gs.Player.BracerTint = data[100]
		gs.Player.GlovesTint = data[101]
		gs.Player.LegsTint = data[102]
		gs.Player.BootsTint = data[103]
		gs.Player.HelmTint = data[104]
	}
	if len(data) >= 111 {
		// Player's own appearance comes from char select list (0x002C),
		// not state channel 0x00. The server's self-state often has stale
		// default values (robe=0, hair=defaults). Only apply state channel
		// appearance if we don't have char select data.
		if gs.SelectedChar < 0 || gs.SelectedChar >= len(gs.Characters) {
			gs.Player.HairColor = data[105]
			gs.Player.HairLength = data[106]
			gs.Player.HairStyle = data[107]
			gs.Player.FaceOption = data[108]
			gs.Player.Robe = data[109]
			// Byte 110 is the robe tint (below); the channel carries no sex,
			// which the face takes from the model, as VICSprite::SetFace does.
			gs.Player.Sex = SexFromModelID(gs.Player.ModelID)
		}
	}
	if len(data) >= 111 {
		gs.Player.RobeTint = data[110]
	}
	if len(data) >= 143 {
		gs.Player.NameColor = data[142]
	}
	if len(data) >= 145 {
		gs.Player.Race = data[144]
	}
	// Class: offset 145 (same offset as NPCType low byte for entities, but for self channel it's class)
	if len(data) >= 146 {
		gs.Player.Class = data[145]
	}
	gs.Player.IsPlayer = true
}

// decodeEntityState reads another entity's state off its channel, once
// channelEntity has decided the slot holds it; created is a slot that has
// just made a new entity. The layout is VIEntity's reader (0x00CC4CC8:
// position, heading, pitch, world, a u64, two bytes, model, a u32, scale,
// to byte 40) and then VICreatureEntity's (0x00CC2DB0, to byte 162).
func (gs *GameState) decodeEntityState(channel byte, data []byte, created bool) {
	if len(data) < 16 {
		return
	}

	objectID := uint32(data[1]) | uint32(data[2])<<8 | uint32(data[3])<<16 | uint32(data[4])<<24
	gs.ChannelEntity[channel] = objectID

	ent, exists := gs.Entities[objectID]
	if !exists {
		ent = &Entity{
			ObjectID: objectID,
			Channel:  int(channel),
		}
		gs.Entities[objectID] = ent
	}
	ent.Channel = int(channel)
	prevUpdate := ent.LastUpdate // save for elapsed time computation
	ent.LastUpdate = time.Now()
	wasNew := !exists || created

	// Track average update interval (exponential moving average)
	if !prevUpdate.IsZero() {
		dt := float32(ent.LastUpdate.Sub(prevUpdate).Seconds())
		if dt > 0.05 && dt < 10.0 { // ignore sub-tick batched deltas and very stale gaps
			if ent.UpdateInterval <= 0 {
				ent.UpdateInterval = dt
			} else {
				ent.UpdateInterval = ent.UpdateInterval*0.7 + dt*0.3 // EMA
			}
		}
	}

	// Position: 3-byte big-endian signed mediums at offsets 6, 9, 12 (value / 128.0)
	newX := decodeMediumBE(data[6:9])
	newY := decodeMediumBE(data[9:12])
	newZ := decodeMediumBE(data[12:15])

	// Facing: offset 15 (0-255 mapped to 0-2*pi)
	newFacing := float32(data[15]) / 256.0 * 2 * 3.14159265

	// Debug: log position delta BEFORE updating target (so we see actual change)
	if gs.DebugEntityMovement && !wasNew && ent.Interpolating {
		dx := newX - ent.TargetX
		dy := newY - ent.TargetY
		dz := newZ - ent.TargetZ
		dist := mathutil.Dist3D(newX, newY, newZ, ent.TargetX, ent.TargetY, ent.TargetZ)
		elapsed := time.Since(prevUpdate)
		if dist > 0.01 {
			var rawVelX, rawVelY, rawVelZ int8
			if len(data) >= 43 {
				rawVelX = int8(data[40])
				rawVelY = int8(data[41])
				rawVelZ = int8(data[42])
			}
			log.Printf("MOVE ch=%d %q dt=%dms delta=(%.2f,%.2f,%.2f) dist=%.2f vel=(%d,%d,%d) anim=%d",
				channel, ent.Name, elapsed.Milliseconds(),
				dx, dy, dz, dist,
				rawVelX, rawVelY, rawVelZ, data[58])
		}
	}

	// Save old target for implicit velocity computation.
	oldTargetX := ent.TargetX
	oldTargetY := ent.TargetY
	oldTargetZ := ent.TargetZ

	// PS2 VIWalkControl: set target for interpolation.
	ent.TargetX = newX
	ent.TargetY = newY
	ent.TargetZ = newZ
	ent.TargetFacing = newFacing

	// Decode velocity from state channel offsets 40-42 (signed bytes).
	// Sandstorm velocity bytes encode direction with magnitude ~30 for walking NPCs.
	// VICreatureEntity's reader (0x00CC2DB0) scales them by 21/128 (func_00CC2890:
	// byte × scale × 1/128) into +0x218. Bytes 43-44, 45-47 and 48-49 follow the
	// same way at 8, 63 and 32 (two, three and two components).
	if len(data) >= 43 {
		rawVX := float32(int8(data[40]))
		rawVY := float32(int8(data[41]))
		rawVZ := float32(int8(data[42]))
		mag := float32(math.Sqrt(float64(rawVX*rawVX + rawVY*rawVY + rawVZ*rawVZ)))
		if mag > 0.5 {
			const velScale = float32(21.0 / 128.0)
			ent.VelX = rawVX * velScale
			ent.VelY = rawVY * velScale
			ent.VelZ = rawVZ * velScale
		} else {
			// No explicit velocity — compute implicit velocity from successive
			// target positions. Fixes Go server NPCs that don't send velocity
			// bytes, preventing pulsing/snapping during patrol movement.
			elapsed := float32(time.Since(prevUpdate).Seconds())
			if elapsed > 0.01 && elapsed < 2.0 && ent.Interpolating {
				dx := newX - oldTargetX
				dy := newY - oldTargetY
				dz := newZ - oldTargetZ
				dist := float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))
				if dist > 0.1 {
					ent.VelX = dx / elapsed
					ent.VelY = dy / elapsed
					ent.VelZ = dz / elapsed
				} else {
					ent.VelX = 0
					ent.VelY = 0
					ent.VelZ = 0
				}
			} else {
				ent.VelX = 0
				ent.VelY = 0
				ent.VelZ = 0
			}
		}
	}

	// Animation byte: read early so we can gate velocity.
	if len(data) >= 59 {
		ent.Animation = data[58]
	}

	// PS2: locomotion animation is derived from velocity (UpdateLocomotion),
	// NOT from the state channel animation byte.
	// For remote PLAYERS: animation byte is authoritative (0=idle means stopped).
	// Sandstorm sends stale velocity bytes on stop, so zero velocity when idle.
	// For NPCs: animation byte is always 0 (server doesn't set it), so don't
	// zero velocity — let the animation system derive locomotion from movement.
	if ent.Animation == 0 && ent.IsPlayer {
		ent.VelX = 0
		ent.VelY = 0
		ent.VelZ = 0
	}

	// Snap entity position to server on every update. Dead-reckoning between
	// updates provides smooth movement; this reset prevents drift accumulation.
	if ent.Interpolating && !wasNew {
		ent.X = newX
		ent.Y = newY
		ent.Z = newZ
	}
	// Save velocity for next update's direction change detection.
	ent.PrevVelX = ent.VelX
	ent.PrevVelZ = ent.VelZ

	if wasNew {
		// First appearance — snap position, enable interpolation
		ent.X = newX
		ent.Y = newY
		ent.Z = newZ
		ent.Facing = newFacing
		ent.VelX = 0
		ent.VelY = 0
		ent.VelZ = 0
		ent.PrevVelX = 0
		ent.PrevVelZ = 0
		ent.Interpolating = true
	}

	// World: offset 16 (continent ID, 0=Tunaria)
	if len(data) >= 18 {
		ent.World = data[17]
	}

	// HP bar: offset 26 (HP flag: 1=alive, 3=dead), offset 27 (0-255 proportional)
	if len(data) >= 28 {
		oldHP := ent.HP
		ent.HP = data[27]
		ent.MaxHP = 255
		hpFlag := data[26]
		if oldHP != 0 && (ent.HP != oldHP || hpFlag != ent.HPFlag) {
			log.Printf("ENTITY HP CHANGED ch=%d %q objID=0x%08X: hp=%d→%d flag=%d→%d",
				channel, ent.Name, ent.ObjectID, oldHP, ent.HP, ent.HPFlag, hpFlag)
		}
		if hpFlag == 3 && ent.HPFlag != 3 {
			gs.SpellFXSys.StopIfThisWasCaster(ent.ObjectID) // CLIENT: after SetDeath
		}
		ent.HPFlag = hpFlag
	}

	// ModelID: offset 27-30 (uint32 LE)
	if len(data) >= 32 {
		ent.ModelID = uint32(data[28]) | uint32(data[29])<<8 | uint32(data[30])<<16 | uint32(data[31])<<24
	}

	// Scale: offset 35-38 (float32 LE)
	if len(data) >= 40 {
		ent.Scale = math.Float32frombits(binary.LittleEndian.Uint32(data[36:40]))
	}

	// Facing float32: offset 54-57 (LE, radians) — believed to be spawn/idle facing,
	// NOT live movement facing. Using this overrides the byte[15] movement facing and
	// causes entities to freeze facing one direction. Disabled.
	// if len(data) >= 58 {
	// 	facingF := math.Float32frombits(binary.LittleEndian.Uint32(data[54:58]))
	// 	if facingF != 0 && !math.IsNaN(float64(facingF)) && !math.IsInf(float64(facingF), 0) {
	// 		ent.TargetFacing = facingF
	// 	}
	// }

	// Target: offset 58-61 (uint32 LE, 0xFFFFFFFF = none)
	if len(data) >= 63 {
		ent.Target = binary.LittleEndian.Uint32(data[59:63])
	}

	// Combat flags: offset 63 = CombatEngaged, 64 = active weapon AttackType during combat
	if len(data) >= 65 {
		ent.CombatEngaged = data[63] != 0
		ent.CombatAttackType = data[64]
	}

	// Equipment: weapon model IDs at offsets 66-78, armor bytes at 93-98
	if len(data) >= 79 {
		ent.PrimaryWeapon = binary.LittleEndian.Uint32(data[67:71])
		ent.SecondaryWeapon = binary.LittleEndian.Uint32(data[71:75])
		ent.Shield = binary.LittleEndian.Uint32(data[75:79])
	}
	// Weapon AttackType: offset 91 = primary, 92 = secondary
	// VICSpriteAttackType used by SetAttackAction for animation pool selection.
	// Confirmed: matches char select equipment block byte 17 exactly.
	if len(data) >= 93 {
		ent.PrimaryAttackType = data[91]
		ent.SecondaryAttackType = data[92]
	}
	if len(data) >= 99 {
		ent.Chest = data[93]
		ent.Bracer = data[94]
		ent.Gloves = data[95]
		ent.Legs = data[96]
		ent.Boots = data[97]
		ent.Helm = data[98]
	}
	if len(data) >= 105 {
		ent.ChestTint = data[99]
		ent.BracerTint = data[100]
		ent.GlovesTint = data[101]
		ent.LegsTint = data[102]
		ent.BootsTint = data[103]
		ent.HelmTint = data[104]
	}
	if len(data) >= 109 {
		ent.HairColor = data[105]
		ent.HairLength = data[106]
		ent.HairStyle = data[107]
		ent.FaceOption = data[108]
	}
	if len(data) >= 110 {
		ent.Robe = data[109]
	}
	if len(data) >= 111 {
		ent.RobeTint = data[110]
	}

	// Name: offset 116-139 (24 bytes null-padded)
	if len(data) >= 140 {
		nameEnd := 116
		for i := 116; i < 140 && i < len(data); i++ {
			if data[i] == 0 {
				break
			}
			nameEnd = i + 1
		}
		ent.Name = string(data[116:nameEnd])
	}

	// Level: offset 139
	if len(data) >= 141 {
		ent.Level = data[140]
	}

	// Movement: offset 140 (0=normal, 1=running)
	if len(data) >= 142 {
		ent.Movement = data[141]
	}

	// NameColor: offset 141 — raw VICSpriteNameColor enum byte.
	// PS2 VICSprite::SetName uses this to index into NameColors[3] table at 0x004E0BF8.
	// Value 0 = NPC (golden yellow), value 2 = Player/Pet (steel blue).
	if len(data) >= 143 {
		ent.NameColor = data[142]
		ent.IsPlayer = data[142] == 2
		ent.IsNPC = data[142] != 2
	}

	// NamePlate: offset 142 (0=own player, 1=show)
	if len(data) >= 144 {
		ent.NamePlate = data[143]
	}

	// Race: offset 143
	if len(data) >= 145 {
		ent.Race = data[144]
	}

	// Offset 145: Class for PC entities, NPCType low byte for NPCs.
	if len(data) >= 146 && ent.IsPlayer {
		ent.Class = data[145]
	}
	// NPCType: offsets 145-146 (uint16 LE)
	// PS2 pcap values: 0x8000=NPC, 0x8100=merchant, 0x8006=quest giver,
	// 0x8008=guild board, 0x800C=unknown, 0x810E=merchant+quest
	if len(data) >= 147 {
		ent.NPCType = uint16(data[145]) | uint16(data[146])<<8
	}
	if len(data) >= 148 {
		ent.NPCFlags = uint16(data[146]) | uint16(data[147])<<8
	}

	// Log new and changed entities
	if wasNew {
		kind := "NPC"
		if ent.IsPlayer {
			kind = "Player"
		}
		log.Printf("Entity ch=%d %s %q objID=0x%08X pos=(%.0f,%.0f,%.0f) model=0x%X level=%d",
			channel, kind, ent.Name, objectID, ent.X, ent.Y, ent.Z, ent.ModelID, ent.Level)
	}
}

// removeEntity forgets an entity, and tells the spell system its actor is
// gone, as CLIENT's actor release does (NotifyActorWasDestroyed).
func (gs *GameState) removeEntity(id uint32) {
	delete(gs.Entities, id)
	gs.SpellFXSys.NotifyActorWasDestroyed(id)
}

// PurgeStaleEntities removes entities whose state channel hasn't been updated
// within the given timeout. This handles the case where a server silently stops
// sending updates for an entity without zeroing the channel's objectID.
// Returns the number of entities purged.
func (gs *GameState) PurgeStaleEntities(timeout time.Duration) int {
	now := time.Now()
	purged := 0
	for id, ent := range gs.Entities {
		if !ent.LastUpdate.IsZero() && now.Sub(ent.LastUpdate) > timeout {
			log.Printf("Entity stale: ch=%d %q objID=0x%08X (no update for %v)",
				ent.Channel, ent.Name, id, now.Sub(ent.LastUpdate).Round(time.Second))
			gs.removeEntity(id)
			purged++
		}
	}
	return purged
}

// InterpolateEntities applies PS2 VIWalkControl smoothing each frame.
//
// Per entity per frame:
//   1. Dead reckoning:  pos += vel * dt (only when entity is moving)
//   2. Correction:      pos += (target - pos) * (1 - pow(smoothRate, dt))
//
// Key fixes over naive implementation:
//   - Velocity zeroed when anim=0 (entity stopped) — prevents runaway dead reckoning
//   - Velocity decayed when state data is stale (>2s since update) — prevents drift
//
// PS2 InitParms defaults (25 live EE instances confirmed):
//   smoothRateX=0.02, smoothRateY=0.80, smoothRateZ=0.01, smoothRateHeading=0.02
//   stepRemote=200ms (remote entity physics tick)
func (gs *GameState) InterpolateEntities(dt float32) {
	const (
		smoothRateHeading = float64(0.02)
	)

	corrH := float32(1.0 - math.Pow(smoothRateHeading, float64(dt)))

	for _, ent := range gs.Entities {
		if !ent.Interpolating {
			continue
		}
		// Dead entities: freeze position (no velocity, no correction).
		if ent.HPFlag == 3 {
			continue
		}

		// Dead-reckon X/Z from current position using velocity.
		// Handler snaps position to server on each update, so drift is bounded
		// to one update interval (~100ms).
		moving := ent.VelX != 0 || ent.VelZ != 0
		if moving {
			ent.X += ent.VelX * dt
			ent.Z += ent.VelZ * dt
			ent.GroundSnapped = false
		}

		// Snap Y to terrain surface.
		// Moving: every frame (slope following).
		// Stopped: once immediately (fix patrol stop positions).
		needSnap := moving || !ent.GroundSnapped
		if gs.SampleHeightFunc != nil && needSnap {
			if y, ok := gs.SampleHeightFunc(ent.X, ent.Z, ent.TargetY+2); ok {
				ent.Y = y
			} else {
				ent.Y = ent.TargetY
			}
			if !moving {
				ent.GroundSnapped = true
			}
		} else {
			ent.Y = ent.TargetY
		}

		// Heading: exponential decay correction (shortest arc) for smooth turning.
		facingDiff := ent.TargetFacing - ent.Facing
		for facingDiff > math.Pi {
			facingDiff -= 2 * math.Pi
		}
		for facingDiff < -math.Pi {
			facingDiff += 2 * math.Pi
		}
		ent.Facing += facingDiff * corrH

		for ent.Facing < 0 {
			ent.Facing += 2 * math.Pi
		}
		for ent.Facing >= 2*math.Pi {
			ent.Facing -= 2 * math.Pi
		}
	}
}

// decodeMediumBE decodes a 3-byte big-endian signed integer and returns float32(val) / 128.0.
// sexFromModelID derives sex (0=male, 1=female) from a character model ID.
// EQOA uses distinct model DictIDs per race+sex combination.
func SexFromModelID(modelID uint32) byte {
	// Female model IDs (from eqoa.live charactermodel table)
	switch modelID {
	case 0x0D537335, // Human female
		0xE18919F7, // Elf female
		0x8C9B4B39, // DarkElf female
		0x4C4084FC, // Gnome female
		0x350BFDBE, // Dwarf female
		0x4DE32701, // Troll female
		0xC44AD516, // Barbarian female
		0x8A27A731, // Halfling female
		0x4F1A3CBE, // Erudite female
		0xACC00B4F: // Ogre female
		return 1
	default:
		return 0 // male or unknown
	}
}

// RaceFromModelID derives race (0-9) from a character model ID.
// Returns -1 for non-player-race models (NPCs, creatures).
func RaceFromModelID(modelID uint32) int {
	switch modelID {
	case 0xC698D870, 0x0D537335: // Human male, female
		return 0
	case 0x156B9CA9, 0xE18919F7: // Elf male, female
		return 1
	case 0x3573530D, 0x8C9B4B39: // DarkElf male, female
		return 2
	case 0xDF3ECA61, 0x4C4084FC: // Gnome male, female
		return 3
	case 0x16D54AC4, 0x350BFDBE: // Dwarf male, female
		return 4
	case 0x4F0BC0AC, 0x4DE32701: // Troll male, female
		return 5
	case 0x90748084, 0xC44AD516: // Barbarian male, female
		return 6
	case 0xF86FD5D8, 0x8A27A731: // Halfling male, female
		return 7
	case 0xE23FDBA3, 0x4F1A3CBE: // Erudite male, female
		return 8
	case 0x2C35826A, 0xACC00B4F: // Ogre male, female
		return 9
	default:
		return -1
	}
}

func decodeMediumBE(b []byte) float32 {
	val := int32(b[0])<<16 | int32(b[1])<<8 | int32(b[2])
	// Sign extend from 24-bit
	if val&0x800000 != 0 {
		val |= ^0xFFFFFF // sign extend
	}
	return float32(val) / 128.0
}

// decodeStatUpdate is the stat channel (0x42), Channel42 → ReadStats
// (0x00BDCB30): 29 u32 stats, 29 u32 base stats and one byte, 233 bytes,
// read in that order. A message that runs short leaves every field after the
// break as it was (VIMemStream's error is sticky). Index 7 is the current HP
// (GetHP), 8 max HP, 9 power, 10 max power; the named fields below are the
// same values under their old offsets. claude-notes/sandstorm-stat-channel.md
// has the field map. Its "BaseAC" (base index 8, offset 148) is index 8 of the
// base block, which GetStatTotal reads as stat 8's base: the base max HP.
func (gs *GameState) decodeStatUpdate(data []byte) {
	p := 0
	read := func(dst *int32) bool {
		if p+4 > len(data) {
			p = len(data) + 1
			return false
		}
		*dst = int32(binary.LittleEndian.Uint32(data[p:]))
		p += 4
		return true
	}
	ok := true
	for i := range gs.PlayerStats {
		if ok = read(&gs.PlayerStats[i]); !ok {
			break
		}
	}
	for i := range gs.PlayerBaseStats {
		if !ok {
			break
		}
		ok = read(&gs.PlayerBaseStats[i])
	}
	if ok && p < len(data) {
		gs.PlayerStatTail = data[p]
	}

	st, base := &gs.PlayerStats, &gs.PlayerBaseStats
	gs.Player.Str, gs.Player.Sta, gs.Player.Agi, gs.Player.Dex = st[0], st[1], st[2], st[3]
	gs.Player.Wis, gs.Player.Int, gs.Player.Cha = st[4], st[5], st[6]
	hp, maxHP, power, maxPower := st[7], st[8], st[9], st[10]

	// Block 1 combat fields (offsets 44-56): 0 on Sandstorm except 56.
	gs.Player.OffMod = st[12]   // offset 48
	gs.Player.DefMod = st[13]   // offset 52
	gs.Player.HPFactor = st[14] // offset 56 (1703 on Zaknafein)

	// Resist additions, offsets 88-108 (order per the note: uncertain).
	gs.Player.FireRAdd = st[22]
	gs.Player.LightningRAdd = st[23]
	gs.Player.ColdRAdd = st[24]
	gs.Player.ArcaneRAdd = st[25]
	gs.Player.PoisonRAdd = st[26]
	gs.Player.DiseaseRAdd = st[27]

	// Base stats, offsets 116-140.
	gs.Player.BaseStr, gs.Player.BaseSta, gs.Player.BaseAgi, gs.Player.BaseDex = base[0], base[1], base[2], base[3]
	gs.Player.BaseWis, gs.Player.BaseInt, gs.Player.BaseCha = base[4], base[5], base[6]
	gs.Player.BaseAC = base[8] // offset 148: the base max HP (see above), kept under its old name
	gs.Player.AC = gs.Player.BaseAC
	gs.Player.BaseOffMod = base[12] // offset 164
	gs.Player.BaseDefMod = base[13] // offset 168

	// Total resists = base (48) + additions from channel
	const baseResist = int32(48)
	gs.Player.PoisonR = baseResist + gs.Player.PoisonRAdd
	gs.Player.DiseaseR = baseResist + gs.Player.DiseaseRAdd
	gs.Player.FireR = baseResist + gs.Player.FireRAdd
	gs.Player.ColdR = baseResist + gs.Player.ColdRAdd
	gs.Player.LightningR = baseResist + gs.Player.LightningRAdd
	gs.Player.ArcaneR = baseResist + gs.Player.ArcaneRAdd

	gs.Player.HPInt = hp
	gs.Player.MaxHPInt = maxHP
	gs.Player.PowerInt = power
	gs.Player.MaxPowerInt = maxPower

	// Scale to 0-255 byte range for HUD display.
	gs.Player.MaxHP = 255
	gs.Player.MaxPower = 255
	if maxPower > 0 {
		gs.Player.Power = byte(power * 255 / maxPower)
	} else {
		gs.Player.Power = 0
	}
}

// decodeBuffUpdate is the active effects channel (0x43), Channel43
// (0x00BD3D18): a u32 count, clamped to 8 (a negative one shows none), then
// one 132-byte record per effect from offset 4: the icon (u32) and a wide
// string (u32 length, UTF-16), which a name of 32 characters or more leaves
// empty (ReadString's 64-byte limit). A record short of data stops the read,
// and every later field keeps its old value (the error is sticky).
func (gs *GameState) decodeBuffUpdate(data []byte) {
	p, bad := 0, false
	take := func(n int) []byte {
		if bad || p < 0 || p+n > len(data) {
			bad = true
			return nil
		}
		b := data[p : p+n]
		p += n
		return b
	}
	if b := take(4); b != nil {
		gs.effectCount = int32(binary.LittleEndian.Uint32(b))
	}
	if gs.effectCount > 8 {
		gs.effectCount = 8
	}
	for i := 0; i < int(gs.effectCount); i++ {
		if b := take(4); b != nil {
			gs.effectIcon[i] = binary.LittleEndian.Uint32(b)
		}
		if b := take(4); b != nil {
			n := binary.LittleEndian.Uint32(b)
			if n+1 <= 32 {
				if w := take(int(n) * 2); w != nil {
					u := make([]uint16, n)
					for j := range u {
						u[j] = binary.LittleEndian.Uint16(w[2*j:])
					}
					gs.effectName[i] = string(utf16.Decode(u))
				} else if old := []rune(gs.effectName[i]); int(n) < len(old) {
					gs.effectName[i] = string(old[:n]) // the read failed; dst[n] = 0 still runs
				}
			} else {
				take(int(n) * 2)
				gs.effectName[i] = ""
			}
		}
		p = 4 + (i+1)*132
	}

	n := int(gs.effectCount)
	if n < 0 {
		n = 0
	}
	buffs := make([]BuffSlot, n)
	changed := len(buffs) != len(gs.Buffs)
	for i := range buffs {
		buffs[i] = BuffSlot{SpellHash: int32(gs.effectIcon[i]), Name: gs.effectName[i]}
		if !changed && gs.Buffs[i] != buffs[i] {
			changed = true
		}
	}
	gs.Buffs = buffs

	if changed {
		var active []string
		for _, b := range gs.Buffs {
			active = append(active, fmt.Sprintf("%s (icon=0x%08X)", b.Name, uint32(b.SpellHash)))
		}
		if len(active) > 0 {
			log.Printf("BuffUpdate: %s", strings.Join(active, ", "))
		} else {
			log.Printf("BuffUpdate: no active buffs")
		}
	}
}

// SpendTrainingPoints sends the pending TP allocation to the server (opcode 0x001D).
// Wire format: 7 × zigzag int64 (STR, STA, AGI, DEX, WIS, INT, CHA additions).
func (gs *GameState) SpendTrainingPoints() {
	total := 0
	for _, v := range gs.TPAlloc {
		total += v
	}
	if total <= 0 || int64(total) > gs.TrainingPoints {
		return
	}
	if gs.Conn == nil {
		return
	}

	data := make([]byte, 64)
	writer := buffer.NewWriter(data)
	for _, v := range gs.TPAlloc {
		writer.Write7BitEncodedInt64(int64(v))
	}
	gs.Conn.QueueReliable(protocol.OpcodeUpdateTrainingPoints, data[:writer.Position()])
	log.Printf("SpendTrainingPoints: STR+%d STA+%d AGI+%d DEX+%d WIS+%d INT+%d CHA+%d (total=%d)",
		gs.TPAlloc[0], gs.TPAlloc[1], gs.TPAlloc[2], gs.TPAlloc[3],
		gs.TPAlloc[4], gs.TPAlloc[5], gs.TPAlloc[6], total)
}

// SelectCharacter sends the character selection message to the server.
func (gs *GameState) SelectCharacter(index int) {
	if index < 0 || index >= len(gs.Characters) {
		return
	}

	ch := gs.Characters[index]
	gs.SelectedChar = index

	// PS2 SelectedChar format (20 bytes, verified from pcap + round-trip tests):
	// serverID(int32) + faceOption(int32) + hairStyle(int32) + hairLength(int32) + hairColor(int32)
	// Confirmed: color and style swap between fields 2 and 4.
	data := make([]byte, 20)
	binary.LittleEndian.PutUint32(data[0:4], ch.ServerID)
	binary.LittleEndian.PutUint32(data[4:8], uint32(ch.FaceOption))
	binary.LittleEndian.PutUint32(data[8:12], uint32(ch.HairStyle))
	binary.LittleEndian.PutUint32(data[12:16], uint32(ch.HairLength))
	binary.LittleEndian.PutUint32(data[16:20], uint32(ch.HairColor))

	gs.Conn.QueueReliable(protocol.OpcodeSelectedChar, data)
	gs.Conn.Flush()

	gs.Phase = PhaseEnteringWorld
	gs.StatusMsg = "Selecting " + ch.Name + "..."
	log.Printf("Selected character: %s (serverID=%d) hair(%d/%d/%d) face=%d",
		ch.Name, ch.ServerID, ch.HairColor, ch.HairLength, ch.HairStyle, ch.FaceOption)
}

// DeleteCharacter sends the character deletion message (opcode 0x002D).
// Wire format: serverID as zigzag varint (matches server Read7BitEncodedInt64).
func (gs *GameState) DeleteCharacter(index int) {
	if index < 0 || index >= len(gs.Characters) {
		return
	}
	ch := gs.Characters[index]
	var buf [8]byte
	w := buffer.NewWriter(buf[:])
	w.Write7BitEncodedInt64(int64(ch.ServerID))
	gs.Conn.QueueReliable(protocol.OpcodeDelCharacter, buf[:w.Position()])
	gs.Conn.Flush()
	log.Printf("DeleteCharacter: sent serverID=%d name='%s'", ch.ServerID, ch.Name)
}

// CreateCharacter sends the character creation message (opcode 0x002B) to the server.
// Wire format matches server's HandleCreateCharacter parser.
func (gs *GameState) CreateCharacter(name string, race, class, gender, hairColor, hairLength, hairStyle, faceOption, humType int, stats [7]int) {
	// Build message payload:
	// nameLen(int32 LE) + name(UTF-8) + level(zigzag=1) + race(zigzag) + class(zigzag) +
	// gender(zigzag) + hairColor(zigzag) + hairLength(zigzag) + hairStyle(zigzag) +
	// faceOption(zigzag) + humType(zigzag) + 7×stats(int32 LE, all 0)
	buf := make([]byte, 256)
	w := buffer.NewWriter(buf)

	w.WriteInt32(int32(len(name)))
	w.WriteBytes([]byte(name))
	w.Write7BitEncodedInt64(1)                // level = 1 (zigzag — matches char list 0x002C)
	w.Write7BitEncodedInt64(int64(race))       // race (zigzag)
	w.Write7BitEncodedInt64(int64(class))      // class (zigzag)
	w.Write7BitEncodedInt64(int64(gender))     // gender (zigzag)
	w.Write7BitEncodedInt64(int64(hairColor))  // hairColor (zigzag)
	w.Write7BitEncodedInt64(int64(hairLength)) // hairLength (zigzag)
	w.Write7BitEncodedInt64(int64(hairStyle))  // hairStyle (zigzag)
	w.Write7BitEncodedInt64(int64(faceOption)) // faceOption (zigzag)
	w.Write7BitEncodedInt64(int64(humType))    // humType (0=other, 1=east, 2=west)
	for i := 0; i < 7; i++ {
		w.WriteInt32(int32(stats[i])) // stats: STR, STA, AGI, DEX, WIS, INT, CHA
	}

	data := w.Bytes()
	gs.Conn.QueueReliable(protocol.OpcodeCreateCharacter, data)
	gs.Conn.Flush()

	log.Printf("CreateCharacter: sent name='%s' race=%d class=%d gender=%d humType=%d hair(%d/%d/%d) face=%d",
		name, race, class, gender, humType, hairColor, hairLength, hairStyle, faceOption)
}

// SendCharacterInWorld sends the "character in world" confirmation.
func (gs *GameState) SendCharacterInWorld() {
	gs.Conn.QueueReliable(protocol.OpcodeCharacterInWorld, nil)
	gs.Conn.Flush()
	log.Printf("Sent CharacterInWorld")
}

// =============================================================================
// New Opcode Handlers — Parse and log for DB population
// =============================================================================

// handleGrantXPOrItems is 0x0020 (the dispatcher's case): zigzag level, then
// zigzag experience, straight into the player record (+0xC300, +0xC304). A
// higher level than before plays UI sound 16 (not ported). The item-list
// reading of it this handler used to try has no basis in CLIENT.
func (gs *GameState) handleGrantXPOrItems(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 2 {
		return
	}
	reader := buffer.NewReader(data[2:])
	level, err := vimsSigned(reader)
	if err != nil {
		return
	}
	old := gs.Player.Level
	gs.Player.Level = byte(level)
	xp, err := vimsSigned(reader)
	if err != nil {
		return
	}
	gs.Player.XPRaw = int64(xp)
	gs.Player.XPPct = xpLevelPct(int(level), int64(xp))
	log.Printf("GrantXP: level=%d (was %d) xp=%d xpPct=%d", level, old, xp, gs.Player.XPPct)
	writeItemLog("XP", fmt.Sprintf("level=%d xp=%d xpPct=%d", level, xp, gs.Player.XPPct))
}

// FindItemIndex is VIItemList::Find on the inventory: the index of the record
// with this key, or -1.
func (gs *GameState) FindItemIndex(key uint32) int {
	return listFind(gs.Inventory(), key)
}

// handleInventoryItem handles opcode 0x003B, AddInvItem (CLIENT 0x00BCB0D8):
// one item record, filed as VIItemList::AddItem files it — onto the first
// stack of the same item, else a new record under the list's next key.
// HandleMessage routes 0x0018 here too, which is the corpse's list, not an
// inventory message (handleLoot).
func (gs *GameState) handleInventoryItem(msg *protocol.Message, source string) {
	d := msg.Data()
	if len(d) < 3 {
		return
	}
	if protocol.GameOpcode(d[0])|protocol.GameOpcode(d[1])<<8 == protocol.OpcodeLoot {
		gs.handleLoot(msg)
		return
	}
	item, err := readNetItem(buffer.NewReader(d[2:]))
	if err != nil {
		log.Printf("%s: %v", source, err)
		return
	}
	took := gs.addInventory(&item)
	log.Printf("%s: %q x%d, key %d (%d in the inventory)", source, item.Name, took, item.Key, gs.InventoryItemCount)
	logItemRecord(source, item)
}

// parseAndLogItems tries to extract item data and log it for DB population.
// It scans (parseItemData): the handlers it serves do not read an item record.
func (gs *GameState) parseAndLogItems(source string, data []byte) {
	// Try extracting UTF-16 strings as a starting point
	strs := extractUTF16Strings(data)
	if len(strs) > 0 {
		for _, s := range strs {
			writeItemLog(source, s)
		}
	}

	// Try DumpItem parse on data after opcode
	if len(data) > 2 {
		for _, item := range parseItemData(data[2:]) {
			logItemRecord(source, item)
		}
	}
}

func (gs *GameState) handleDialogueBox(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 10 {
		return
	}
	// Format: opcode(2) + uint32(counter) + stringUnicode(text)
	counter := binary.LittleEndian.Uint32(data[2:6])
	text, _ := readUnicodeString(data, 6)
	if text != "" {
		log.Printf("DialogueBox: counter=%d text=%s", counter, text)
		fmt.Printf("\n--- Dialogue ---\n%s\n----------------\n", text)
		gs.DialogueText = text
		gs.DialogueOptions = nil
		gs.DialogueCounter = counter
		gs.DialogueActive = true
	}
}

func (gs *GameState) handleQuestPopup(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 10 {
		return
	}
	// Format: opcode(2) + uint32(dialogueCounter) + stringUnicode(greeting) + byte(optionCount) + N × stringUnicode(option)
	counter := binary.LittleEndian.Uint32(data[2:6])
	greeting, consumed := readUnicodeString(data, 6)
	if greeting == "" {
		return
	}
	offset := 6 + consumed
	if offset >= len(data) {
		return
	}
	optionCount := int(data[offset])
	offset++

	options := make([]string, 0, optionCount)
	for i := 0; i < optionCount && offset < len(data); i++ {
		opt, n := readUnicodeString(data, offset)
		if n == 0 {
			break
		}
		options = append(options, opt)
		offset += n
	}

	log.Printf("OptionBox: counter=%d greeting=%q options=%v", counter, greeting, options)
	fmt.Printf("\n--- Dialogue (counter=%d) ---\n%s\n", counter, greeting)
	for i, opt := range options {
		fmt.Printf("  [%d] %s\n", i, opt)
	}
	fmt.Println("Use: option <N>  (255 to cancel)")
	fmt.Println("----------------------------")
	gs.DialogueText = greeting
	gs.DialogueOptions = options
	gs.DialogueCounter = counter
	gs.DialogueActive = true
}

func (gs *GameState) handleAddQuestLog(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 6 {
		return
	}
	// Format: opcode(2) + uint32(questNumber) + WriteStringUnicode(text)
	reader := buffer.NewReader(data[2:])
	questNum, _ := reader.ReadUint32()
	textLen, err := reader.ReadInt32()
	if err != nil || textLen <= 0 {
		log.Printf("AddQuestLog: quest=%d (no text)", questNum)
		return
	}
	text, _ := reader.ReadStringUnicode(int(textLen))
	log.Printf("AddQuestLog: quest=%d text=%q", questNum, text)
	writeItemLog("Quest", fmt.Sprintf("id=%d text=%q", questNum, text))
}

// handleDamageIndication is 0x00DB: u32 attacker, zigzag amount, u32 defender
// (the dispatcher's case, 0x00BD2D70), then Sandstorm's optional R, G, B. An
// attack of my own is DamageFromSelf (0x00BDC220): the number floats over the
// defender. Anyone else's is DamageFromOther (0x00BDC288): it floats over ME,
// and the defender is not used. VIClientPart4FD28 draws |amount| with "%d"
// (func_00BE96A8), green when the amount is positive, else white for my own
// hit and yellow for one on me (func_00BE9198); S2's colour replaces that.
// Not ported: both are skipped under game option +0x92, and DamageFromOther
// also in camera mode 0 or under a modal window.
func (gs *GameState) handleDamageIndication(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 2 {
		return
	}
	reader := buffer.NewReader(data[2:])
	sourceID, err := reader.ReadUint32()
	if err != nil {
		return
	}
	amount, err := vimsSigned(reader)
	if err != nil {
		return
	}
	targetID := uint32(0xFFFFFFFF)
	if v, err := reader.ReadUint32(); err == nil {
		targetID = v
	}
	// Sandstorm's S2 (cave 0x000FBA40): three more bytes are an RGB the
	// number is drawn in. Without them it keeps the retail colours.
	var color [3]uint8
	hasColor := false
	if reader.Remaining() >= 3 {
		rgb, _ := reader.ReadBytes(3)
		copy(color[:], rgb)
		hasColor = true
	}
	dmg := int64(amount)

	sourceName := gs.resolveObjectName(sourceID)
	targetName := gs.resolveObjectName(targetID)

	// Combat log
	if dmg > 0 {
		log.Printf("COMBAT: %s → %s heal=%+d", sourceName, targetName, dmg)
	} else {
		log.Printf("COMBAT: %s → %s dmg=%d", sourceName, targetName, dmg)
	}

	// Track when player takes damage (for hit reaction animation)
	if targetID == gs.PlayerObjectID && dmg < 0 {
		gs.PlayerHitTime = time.Now()
	}

	// Track when any entity takes damage (for NPC hit reaction animation)
	if ent, ok := gs.Entities[targetID]; ok && dmg < 0 {
		ent.LastDamageTime = time.Now()
	}

	// The number: over whom, and in which colour.
	over := gs.PlayerObjectID
	mine := sourceID == gs.PlayerObjectID
	if mine {
		over = targetID
	}
	var fx, fy, fz float32
	if over == gs.PlayerObjectID {
		fx, fy, fz = gs.Player.X, gs.Player.Y, gs.Player.Z
	} else if ent, ok := gs.Entities[over]; ok {
		fx, fy, fz = ent.X, ent.Y, ent.Z
	} else {
		return // AddDamage's actor is not in the scene: nothing is drawn
	}
	if !hasColor {
		switch {
		case amount > 0:
			color = [3]uint8{0, 255, 0}
		case mine:
			color = [3]uint8{255, 255, 255}
		default:
			color = [3]uint8{204, 204, 25} // (0.8, 0.8, 0.1)
		}
	}
	shown := amount
	if shown < 0 {
		shown = -shown // negu: INT32_MIN stays itself, as on the EE
	}
	gs.FloatingTexts = append(gs.FloatingTexts, FloatingText{
		Text:      fmt.Sprintf("%d", shown),
		X:         fx,
		Y:         fy,
		Z:         fz,
		SpawnTime: time.Now(),
		IsHeal:    amount > 0,
		Color:     color,
		HasColor:  hasColor,
	})
}

// handlePlayerSearchEntry appends one result of Sandstorm's player search,
// exactly as its S2 reads it (cave 0x000FE4B8): at least 3 bytes; the
// 12-byte name slot at 3; the id once 19 bytes are there; world, column and
// row once 22 are; the group byte at 22 (0xFF without it); then the note, up
// to 71 bytes.
func (gs *GameState) handlePlayerSearchEntry(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 2 {
		return
	}
	b := data[2:]
	if len(b) < 3 {
		return
	}
	ascii := func(p []byte, max int) string {
		if len(p) > max {
			p = p[:max]
		}
		if i := bytes.IndexByte(p, 0); i >= 0 {
			p = p[:i]
		}
		return string(p)
	}
	e := PlayerSearchEntry{Race: b[0], Class: b[1], Level: b[2], Group: 0xFF}
	if len(b) > 3 {
		e.Name = ascii(b[3:], 12)
	}
	if len(b) >= 19 {
		e.ID = binary.LittleEndian.Uint32(b[15:19])
	}
	if len(b) >= 22 {
		e.HasLocation = true
		e.World, e.Col, e.Row = int(b[19]), int(b[20]), int(b[21])
	}
	if len(b) >= 23 {
		e.Group = b[22]
	}
	if len(b) > 23 {
		e.Note = ascii(b[23:], 71)
	}
	gs.PlayerSearch = append(gs.PlayerSearch, e)
}

// getEquippedProcAnim returns the P4C of the first worn inventory item that
// has one. (Nothing calls it. Its namesake, CLIENT func_00BC76A0, is the
// auto-attack tick, which reads the worn weapons' attack type at +0x28.)
func (gs *GameState) getEquippedProcAnim() uint32 {
	for _, item := range gs.Inventory() {
		if item.EquipLocation < 0 {
			continue
		}
		if item.P4C != 0 {
			return uint32(item.P4C)
		}
	}
	return 0
}

// handleResourceUpdate is 0x0775 (the dispatcher's case, 0x00BD1930): u32
// entity id, u8 action, and nothing more. The entity's character sprite plays
// the action (VICSprite::SetAction, the same call a spell's SetAction event
// makes); on my own character only action 34 does, which the drowning
// capture sends (breath_capture.pcap).
func (gs *GameState) handleResourceUpdate(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 7 {
		return
	}
	objectID := binary.LittleEndian.Uint32(data[2:6])
	action := data[6]
	if objectID != gs.PlayerObjectID && gs.Entities[objectID] == nil {
		return // FindEntity fails
	}
	if objectID == gs.PlayerObjectID && action != 34 {
		return
	}
	log.Printf("ResourceUpdate: obj=0x%08X(%s) action=%d", objectID, gs.resolveObjectName(objectID), action)
	sys := gs.SpellFXSys
	if sys == nil || sys.OnSetAction == nil {
		return
	}
	if sys.IsCSprite != nil && !sys.IsCSprite(objectID) {
		return // GetActorSpriteType != 5
	}
	sys.OnSetAction(objectID, int32(action))
}

// resolveObjectName returns a display name for an ObjectID by checking known entities.
func (gs *GameState) resolveObjectName(objectID uint32) string {
	if objectID == gs.PlayerObjectID {
		return "SELF"
	}
	if ent, ok := gs.Entities[objectID]; ok && ent.Name != "" {
		return ent.Name
	}
	return "?"
}

// handleCharacterDied is 0x005E (Msg005E, 0x00BDAEB8). It has no payload:
// my character takes its death pose, the spells it cast stop, and the cast
// window clears. Not ported: the "death" cue at 0.4, UI mode 0, auto-attack
// off (m_autoAttack = 0), every pending group invite declined (op 1572,
// reason 3), the camera reset. The XP-loss, death-text and chat fields this
// handler used to read are not on the wire.
func (gs *GameState) handleCharacterDied(msg *protocol.Message) {
	if !gs.Player.Dead {
		gs.SpellFXSys.StopIfThisWasCaster(gs.PlayerObjectID) // CLIENT: after SetDeath
	}
	gs.Player.Dead = true
	gs.Player.DeadSince = time.Now()
	gs.Player.HP = 0
	gs.Player.HPInt = 0
	gs.Player.Speed = 0
	gs.CastStartTime = time.Time{} // m_p2BB40 = m_p2BB48 = 0
	gs.CastDurationMs = 0
	gs.addChatMessage("You have died!", ChatError) // Go's own line; CLIENT prints none
	log.Printf("CharacterDied")
}

// handleCooldownTimer is 0x00CD (Msg00CD, 0x00BCF500): a u8 display index,
// the ability stamp, then a zigzag lockout in ms. The first ability with that
// display index takes the lockout (Sandstorm sends 0 to clear it); one on the
// bar also runs the slot's cooldown.
func (gs *GameState) handleCooldownTimer(msg *protocol.Message) {
	r := abilityReader(msg)
	display, err := r.ReadByte()
	if err != nil {
		return
	}
	stamp, err := r.ReadUint32()
	if err != nil {
		return
	}
	gs.AbilityStamp = stamp
	ms, err := vimsSigned(r)
	if err != nil {
		return
	}
	i := gs.abilityByDisplay(int64(display))
	if i < 0 {
		log.Printf("CooldownTimer: no ability with display index %d", display)
		return
	}
	sp := &gs.ParsedSpells[i]
	sp.Lockout = int64(ms)
	log.Printf("CooldownTimer: %q display=%d lockout=%dms", sp.Name, display, ms)
	if slot := sp.WhereOnHotBar; sp.OnHotBar != 0 && slot >= 0 && slot < 10 && gs.Hotbar[slot] == sp {
		gs.Cooldowns[slot] = time.Now().Add(time.Duration(ms) * time.Millisecond)
		gs.CooldownDurs[slot] = float32(ms) / 1000.0
	}
}

func (gs *GameState) handleCastingCombat(msg *protocol.Message) {
	data := msg.Data()
	payload := data[2:] // skip opcode
	log.Printf("CastingCombat RAW: playerObjID=0x%08X payload=%X", gs.PlayerObjectID, payload)

	// PS2 wire format (0x00BD1F10 → func_00BD3738):
	//   casterObjID(uint32) + targetObjID(uint32) + extraCount(uint8)
	//   + extraData[count](uint32 each) + timerMs(uint32) + animHash(uint32)
	//
	// extraCount/extraData has dual semantics:
	//   - PS2 retail: hitCount + hitDamages[N] (damage values per swing)
	//   - Sandstorm:  secondaryCount + secondaryTargets[N] (AoE target ObjectIDs)
	// We disambiguate by checking if values match known entity ObjectIDs.
	//
	// Minimum: 4+4+1+4+4 = 17 bytes (when extraCount=0)
	if len(payload) < 17 {
		if len(payload) >= 8 {
			casterID := binary.LittleEndian.Uint32(payload[0:4])
			targetID := binary.LittleEndian.Uint32(payload[4:8])
			log.Printf("CastingCombat: caster=0x%08X target=0x%08X (short %d bytes)", casterID, targetID, len(payload))
		}
		return
	}

	casterID := binary.LittleEndian.Uint32(payload[0:4])
	targetID := binary.LittleEndian.Uint32(payload[4:8])
	extraCount := int(payload[8])
	off := 9

	// Read the N×uint32 extra data values
	var extraValues []uint32
	for i := 0; i < extraCount && off+4 <= len(payload); i++ {
		val := binary.LittleEndian.Uint32(payload[off : off+4])
		extraValues = append(extraValues, val)
		off += 4
	}

	if off+8 > len(payload) {
		log.Printf("CastingCombat: payload too short after %d extra values (off=%d, remaining=%d)",
			extraCount, off, len(payload)-off)
		return
	}

	timerVal := binary.LittleEndian.Uint32(payload[off : off+4])
	off += 4
	animHash := binary.LittleEndian.Uint32(payload[off : off+4])

	// PS2 func_00BD3738 treats ALL extra data as secondary target entity IDs.
	// Damage values come from DamageIndication (0x00DB), not CastingCombat.
	secondaryTargets := extraValues

	casterName := gs.resolveObjectName(casterID)
	targetName := gs.resolveObjectName(targetID)
	castType := "spell"
	if timerVal >= 3000 {
		castType = fmt.Sprintf("auto-attack(%dms)", timerVal)
	} else if timerVal > 0 {
		castType = fmt.Sprintf("timer=%dms", timerVal)
	}

	logMsg := fmt.Sprintf("CastingCombat: caster=0x%08X(%s) target=0x%08X(%s) %s anim=0x%08X",
		casterID, casterName, targetID, targetName, castType, animHash)
	if len(secondaryTargets) > 0 {
		logMsg += fmt.Sprintf(" secondaryTargets=%d", len(secondaryTargets))
	}
	log.Printf("%s", logMsg)
	gs.SpellFX.LogEffect(animHash, casterName, targetName)

	// PS2: floating damage text comes from DamageIndication (0x00DB), not CastingCombat.

	// === Sandstorm adaptations (NOT in PS2 CastingCombat handler) ===
	// PS2 retail sends CastingCombat at cast START. Sandstorm sends at cast END.
	// These extra tracking fields support Sandstorm's timing model.
	timerMs := uint16(timerVal)
	if casterID == gs.PlayerObjectID && timerMs >= 3000 {
		gs.PlayerSwingTime = time.Now()
		gs.PlayerInCombat = true
	}
	// Track any auto-attack event involving the player (as attacker or victim).
	// Used by visual client to detect combat end — if no auto-attack for 6+ seconds,
	// the server has stopped the combat cycle (target died, out of range, etc.).
	if timerMs >= 3000 && (casterID == gs.PlayerObjectID || targetID == gs.PlayerObjectID) {
		gs.LastAutoAttackEvent = time.Now()
	}

	// Casting bar: track spell casts by self.
	// Self-buffs (caster==target) are always spells regardless of timer.
	// Offensive spells: timer > 0 and < 4000 (auto-attacks use 3000-4000ms).
	isSelfBuff := casterID == gs.PlayerObjectID && targetID == gs.PlayerObjectID
	isSpellCast := casterID == gs.PlayerObjectID && timerMs > 0 && (isSelfBuff || timerMs < 4000)
	if isSpellCast {
		gs.CastStartTime = time.Now()
		gs.CastDurationMs = timerMs
		gs.CastTargetName = targetName
		gs.CastSelfTarget = (casterID == targetID)
	}

	// NPC casting animation: set cast end time so AnimationManager can play cast anim.
	if casterID != gs.PlayerObjectID && timerMs > 0 {
		if ent, ok := gs.Entities[casterID]; ok {
			ent.CastAnimEnd = time.Now().Add(time.Duration(timerMs) * time.Millisecond)
		}
	}

	// Start the runtime spell effect driver (timed event playback).
	// PS2 func_00BD3738: resolves caster/target to actor indices, looks up animHash
	// via VIDictionary::FindTyped(type=21=SpellEffect), calls VISpellEffectSystem::Play.
	// PS2 calls Play unconditionally for ALL CastingCombat (auto-attacks, spells, procs).
	// Attack animations are driven by spell FX PlayAttack events, NOT DamageIndication.
	//
	// Cast timing: Sandstorm sends CastingCombat at cast COMPLETION. We backdate
	// triggerTime so Wait(sub-type 2) expires immediately.
	casterKnown := casterID == gs.PlayerObjectID || gs.Entities[casterID] != nil
	targetKnown := targetID == gs.PlayerObjectID || gs.Entities[targetID] != nil
	if gs.SpellFXSys != nil && casterKnown && targetKnown {
		var fxStart time.Time
		if timerMs > 0 && timerMs < 3000 {
			if casterID == gs.PlayerObjectID && !gs.CastStartTime.IsZero() {
				fxStart = gs.CastStartTime
			} else {
				fxStart = time.Now().Add(-time.Duration(timerMs) * time.Millisecond)
			}
		}
		// ONE playback carries the secondaries (CLIENT func_00BD3738 skips
		// the ones it cannot resolve); playing the effect once per secondary
		// repeated the caster's whole event list for each of them.
		var secondary []uint32
		for _, secTarget := range secondaryTargets {
			if secTarget == gs.PlayerObjectID || gs.Entities[secTarget] != nil {
				secondary = append(secondary, secTarget)
			}
		}
		gs.SpellFXSys.Play(animHash, timerMs, casterID, targetID, fxStart, secondary...)
	}

	// Parse remaining tail data (Sandstorm sends stat/buff/position as separate opcodes)
	off += 4 // past animHash
	tailLen := len(payload) - off
	if tailLen > 0 {
		log.Printf("  CastingCombat tail: %d bytes remaining", tailLen)
	}
}

// handlePlayEffect is 0x0776 (Msg0776, 0x00BD9E50): u32 caster, u32 target,
// u32 effect. Unless I am the caster, and once both are found, the effect
// plays between them with no cast time and no secondaries.
func (gs *GameState) handlePlayEffect(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 14 {
		return
	}
	caster := binary.LittleEndian.Uint32(data[2:6])
	target := binary.LittleEndian.Uint32(data[6:10])
	effect := binary.LittleEndian.Uint32(data[10:14])
	if caster == gs.PlayerObjectID {
		return
	}
	known := func(id uint32) bool { return id == gs.PlayerObjectID || gs.Entities[id] != nil }
	if !known(caster) || !known(target) {
		return
	}
	log.Printf("PlayEffect: caster=0x%08X target=0x%08X effect=0x%08X", caster, target, effect)
	gs.SpellFXSys.Play(effect, 0, caster, target, time.Time{})
}

// handleSkillBarAck is 0x00B4, the server's echo of 0x0033: an ability
// memorized or forgotten (handleAbilityMemorize).
func (gs *GameState) handleSkillBarAck(msg *protocol.Message) {
	gs.handleAbilityMemorize(msg)
}

func (gs *GameState) handlePlayerTunar(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 4 {
		return
	}
	reader := buffer.NewReader(data[2:])
	tunarVal, err := vimsSigned(reader) // ReadSigned into the record's +0x34
	if err != nil {
		return
	}
	gs.Tunar = int64(tunarVal)
	log.Printf("PlayerTunar: tunar=%d", tunarVal)
}

// handleMerchantBox handles opcode 0x00B7 (CLIENT's dispatcher, 0x00BD21EC):
// the merchant (u32), the two rates in percent (zigzag: what it pays for an
// item, what it charges for its own), its item list (VIItemList::Read), then
// its greeting (ReadString16 into 128 characters), and the window opens
// (MerchantOpen; the PS2 opens it only when no other panel is up).
func (gs *GameState) handleMerchantBox(msg *protocol.Message) {
	r := buffer.NewReader(msg.Data()[2:])
	id, err := r.ReadUint32()
	if err != nil {
		return
	}
	buyRate, err1 := readSigned(r)
	sellRate, err2 := readSigned(r)
	items, _, err3 := readItemList(r)
	if err1 != nil || err2 != nil || err3 != nil {
		log.Printf("MerchantBox: 0x%08X: %v %v %v", id, err1, err2, err3)
		return
	}
	text, err := readWideString(r, 256)
	if err != nil {
		log.Printf("MerchantBox: 0x%08X text: %v", id, err)
	}
	gs.MerchantID = id
	gs.MerchantBuyRate, gs.MerchantSellRate = int(buyRate), int(sellRate)
	gs.MerchantItems = items
	gs.MerchantText = text
	if name := gs.tradeName(id, ""); name != "" {
		gs.MerchantName = name
	}
	gs.MerchantOpen = true
	gs.MerchantSelected = -1
	for _, it := range items {
		logItemRecord("MerchantBox", it)
	}
	log.Printf("MerchantBox: 0x%08X %q, %d items, rates %d%%/%d%%", id, text, len(items), buyRate, sellRate)
}

// handleWeatherControl handles opcode 0x00C5: CLIENT 0x00BD2CC0 reads one byte
// and calls func_00BD9F00(type, 0), i.e. VIAtmosphere::SetWeather(type, false)
// — a 30-second fade.
func (gs *GameState) handleWeatherControl(msg *protocol.Message) {
	data := msg.Data()
	if len(data) >= 3 {
		wt := WeatherType(data[2])
		log.Printf("WeatherControl: type=%d (fade)", wt)
		if gs.Weather != nil {
			gs.Weather.SetWeather(wt, false)
		}
	}
}

// handleSkyboxWeather handles opcode 0x00C7: CLIENT 0x00BD2CEC is the same
// read with func_00BD9F00(type, 1), SetWeather(type, true) — instant.
func (gs *GameState) handleSkyboxWeather(msg *protocol.Message) {
	data := msg.Data()
	if len(data) >= 3 {
		wt := WeatherType(data[2])
		log.Printf("SkyboxWeather: type=%d (instant)", wt)
		if gs.Weather != nil {
			gs.Weather.SetWeather(wt, true)
		}
	}
}

// handleWhoListResponse handles opcode 0x0E02 — server who list response.
// Format: byte(1) + byte(token) + zigzag(count) + count × { byte(race) + byte(class) + byte(level) + uint32_le(nameLen) + utf8(name) + uint32_le(objectID) }
func (gs *GameState) handleWhoListResponse(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 4 {
		return
	}

	reader := buffer.NewReader(data[4:]) // skip 2-byte opcode + byte(1) + byte(token)
	countRaw, err := reader.Read7BitEncodedInt64()
	if err != nil {
		log.Printf("WhoList: failed to read count: %v", err)
		return
	}
	count := int(countRaw)
	if count < 0 || count > 1000 {
		log.Printf("WhoList: invalid count %d", count)
		return
	}

	entries := make([]WhoEntry, 0, count)
	for i := 0; i < count; i++ {
		race, err := reader.ReadByte()
		if err != nil {
			break
		}
		class, err := reader.ReadByte()
		if err != nil {
			break
		}
		level, err := reader.ReadByte()
		if err != nil {
			break
		}
		nameLen, err := reader.ReadUint32()
		if err != nil {
			break
		}
		name, err := reader.ReadStringUTF8(int(nameLen))
		if err != nil {
			break
		}
		objectID, err := reader.ReadUint32()
		if err != nil {
			break
		}
		entries = append(entries, WhoEntry{
			Name:     name,
			Race:     race,
			Class:    class,
			Level:    level,
			ObjectID: objectID,
		})
	}

	gs.WhoList = entries
	log.Printf("WhoList: %d players online", len(entries))
}

func (gs *GameState) handleBuddyListEntry(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 12 {
		return
	}

	if !gs.tryParseLFGEntry(data) {
		// Fallback: log as buddy list entry
		strs := extractUTF16Strings(data)
		for _, s := range strs {
			log.Printf("BuddyListEntry: %s", s)
		}
	}
}

// tryParseLFGEntry attempts to parse a 0x03D5 message as an LFG group finder entry.
// Returns true if parsing succeeded.
func (gs *GameState) tryParseLFGEntry(data []byte) bool {
	// Format: varint(charID) + int32(nameLen) + ascii(name) + level + classZigzag + zone + guildFlag + int32(guildChars) [+ UTF-16 guild]
	reader := buffer.NewReader(data[2:]) // skip opcode
	charID, err := reader.Read7BitEncodedUInt64()
	if err != nil {
		return false
	}
	nameLen, err := reader.ReadInt32()
	if err != nil || nameLen <= 0 || nameLen > 40 {
		return false
	}
	nameBytes := make([]byte, nameLen)
	for i := int32(0); i < nameLen; i++ {
		b, err := reader.ReadByte()
		if err != nil {
			return false
		}
		nameBytes[i] = b
	}
	name := string(nameBytes)

	levelByte, err := reader.ReadByte()
	if err != nil {
		return false
	}
	classRaw, err := reader.ReadByte()
	if err != nil {
		return false
	}
	cls := byte((int64(classRaw) >> 1) ^ -(int64(classRaw) & 1))

	zoneByte, err := reader.ReadByte()
	if err != nil {
		return false
	}
	guildFlag, err := reader.ReadByte()
	if err != nil {
		return false
	}
	guildChars, err := reader.ReadInt32()
	if err != nil {
		return false
	}

	guildName := ""
	if guildFlag != 0 && guildChars > 0 && guildChars < 200 {
		u16 := make([]uint16, guildChars)
		for i := int32(0); i < guildChars; i++ {
			lo, err := reader.ReadByte()
			if err != nil {
				break
			}
			hi, err := reader.ReadByte()
			if err != nil {
				break
			}
			u16[i] = uint16(lo) | uint16(hi)<<8
		}
		guildName = string(utf16.Decode(u16))
	}

	entry := LFGEntry{
		CharID:    charID,
		Name:      name,
		Level:     levelByte,
		Class:     cls,
		Zone:      zoneByte,
		GuildName: guildName,
	}

	// Deduplicate by charID
	found := false
	for i, e := range gs.LFGList {
		if e.CharID == charID {
			gs.LFGList[i] = entry
			found = true
			break
		}
	}
	if !found {
		gs.LFGList = append(gs.LFGList, entry)
	}

	if guildName != "" {
		log.Printf("LFG: %s Lv%d class=%d zone=0x%02X guild=%q", name, levelByte, cls, zoneByte, guildName)
	} else {
		log.Printf("LFG: %s Lv%d class=%d zone=0x%02X", name, levelByte, cls, zoneByte)
	}
	return true
}

// handleCollectionRemove handles opcode 0x03D6.
// PS2: reads zigzag varint key, searches binary tree at entity+0x2C70, removes entry.
// Sandstorm sends this as buddy online/offline (charID + status byte).
// Dual-purpose: PS2 uses it for data collection removal, Sandstorm for buddy notifications.
func (gs *GameState) handleBuddyOnlineStatus(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 7 { // 2 opcode + 4 charID + 1 status
		return
	}
	charID := binary.LittleEndian.Uint32(data[2:6])
	status := data[6]
	log.Printf("BuddyOnlineStatus: charID=0x%08X status=%d", charID, status)
}

// handleGuildInfo handles Sandstorm opcode 0x0468 — guild info + online player listings.
// Sub-type 0x01: Guild info (guild name UTF-16 + MOTD UTF-16)
// Sub-type 0x03: Online player (varint charID + int32 nameLen + ASCII name + trailing bytes)
func (gs *GameState) handleGuildInfo(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 4 {
		return
	}
	// data[0:2] = opcode 0x0468
	subType := data[2]
	reader := buffer.NewReader(data[3:])

	switch subType {
	case 0x01:
		// Guild info: int32(guildNameLen) + UTF-16(guildName) + int32(motdLen) + UTF-16(motd)
		nameLen, err := reader.ReadInt32()
		if err != nil || nameLen <= 0 || nameLen > 200 {
			return
		}
		guildName, _ := reader.ReadStringUnicode(int(nameLen))
		motdLen, err := reader.ReadInt32()
		if err != nil || motdLen < 0 {
			return
		}
		motd := ""
		if motdLen > 0 && motdLen < 1000 {
			motd, _ = reader.ReadStringUnicode(int(motdLen))
		}
		log.Printf("GuildInfo: guild=%q motd=%q", guildName, motd)
		gs.GuildName = guildName
		gs.GuildMOTD = motd

		// Add MOTD to chat log if non-empty
		if motd != "" {
			gs.ChatLog = append(gs.ChatLog, ChatMessage{
				Text:    fmt.Sprintf("[Guild MOTD] %s: %s", guildName, motd),
				Channel: ChatGuild,
			})
		}

	case 0x03:
		// Online player: varint(charID) + int32(nameLen) + ASCII(name) + 5 trailing bytes
		charID, _ := reader.Read7BitEncodedInt64()
		nameLen, err := reader.ReadInt32()
		if err != nil || nameLen <= 0 || nameLen > 50 {
			return
		}
		name, _ := reader.ReadStringUTF8(int(nameLen))
		// Remaining bytes: level, race/class info
		remaining := reader.Remaining()
		var trailHex string
		if remaining > 0 {
			trail, _ := reader.ReadBytes(remaining)
			trailHex = fmt.Sprintf("%X", trail)
		}
		log.Printf("GuildInfo: player=%q charID=%d trailing=%s", name, charID, trailHex)

	case 0x02:
		// PS2 sub-type 2: Clear guild — set hasGuild=0, clear name+MOTD+members
		log.Printf("GuildInfo: sub=2 CLEAR GUILD")
		gs.GuildName = ""
		gs.GuildMOTD = ""
		gs.GuildMembers = nil
		gs.GuildLeader = false

	case 0x04:
		// PS2 sub-type 4: Member remove — zigzag memberID → remove from list
		memberID, _ := reader.Read7BitEncodedInt64()
		log.Printf("GuildInfo: sub=4 MEMBER_REMOVE id=%d", memberID)
		// Remove from member list
		for i, m := range gs.GuildMembers {
			if m.CharID == memberID {
				gs.GuildMembers = append(gs.GuildMembers[:i], gs.GuildMembers[i+1:]...)
				break
			}
		}

	case 0x05:
		// PS2 sub-type 5: Guild chat — uint32(senderID) + blob(0x200 message)
		if reader.Remaining() < 4 {
			return
		}
		senderID, _ := reader.ReadUint32()
		msgText := ""
		if reader.Remaining() > 0 {
			remaining := reader.Remaining()
			if remaining > 512 {
				remaining = 512
			}
			msgBytes, _ := reader.ReadBytes(remaining)
			msgText = decodeUTF16LE(msgBytes)
		}
		senderName := gs.resolveObjectName(senderID)
		log.Printf("GuildChat: sender=0x%08X(%s) msg=%q", senderID, senderName, msgText)
		if msgText != "" {
			gs.addChatMessage(fmt.Sprintf("[Guild] %s: %s", senderName, msgText), ChatGuild)
		}

	case 0x06:
		gs.addChatMessage("You have been promoted.", ChatGuild)
	case 0x07, 0x08:
		gs.addChatMessage("Guild rank changed.", ChatGuild)
	case 0x09:
		gs.addChatMessage("You have been demoted.", ChatGuild)
	case 0x0A, 0x0B, 0x0C:
		gs.addChatMessage("Guild notification.", ChatGuild)
	case 0x0D:
		// PS2: set guildLeader = 1
		gs.GuildLeader = true
		gs.addChatMessage("You are now the guild leader.", ChatGuild)
	case 0x0E:
		// PS2: set guildLeader = 0
		gs.GuildLeader = false
		gs.addChatMessage("Guild leadership transferred.", ChatGuild)
	case 0x10:
		gs.addChatMessage("Guild notification.", ChatGuild)

	default:
		log.Printf("GuildInfo: unknown sub-type 0x%02X len=%d", subType, len(data))
	}
}

// handleUnknownOpcode logs unhandled opcodes with hex dump and string extraction.
func (gs *GameState) handleUnknownOpcode(opcode protocol.GameOpcode, msg *protocol.Message) {
	data := msg.Data()

	// Log hex dump for analysis
	hexLen := len(data)
	if hexLen > 64 {
		hexLen = 64
	}
	if len(data) > 0 {
		log.Printf("UnhandledOpcode 0x%04X: dataLen=%d hex=%X", uint16(opcode), len(data), data[:hexLen])
	}

	// Extract all UTF-16 strings from the data
	strs := extractUTF16Strings(data)
	if len(strs) > 0 {
		for _, s := range strs {
			writeItemLog(fmt.Sprintf("0x%04X", uint16(opcode)), s)
		}
	}
}

// writeItemLog appends a line to /tmp/items.log for database population.
func writeItemLog(source, text string) {
	f, err := os.OpenFile("/tmp/items.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	ts := time.Now().Format("15:04:05")
	f.WriteString(fmt.Sprintf("[%s] [%s] %s\n", ts, source, text))
}

// xpLevelPct returns XP progress within the current level as 0-255.
func xpLevelPct(level int, xp int64) byte {
	cur := xpForLevel(level)
	next := xpForLevel(level + 1)
	if next <= cur {
		return 0
	}
	progress := xp - cur
	if progress < 0 {
		return 0
	}
	pct := progress * 255 / (next - cur)
	if pct > 255 {
		pct = 255
	}
	return byte(pct)
}

// xpForLevel returns the total XP required to reach a given level.
// From C# CharacterUtilities.CharXPDict (key=level, value=XP to advance past that level).
var xpTable = []int64{
	0, 0, 424, 2187, 6976, 17125, 35640, 66199, 113152, 181521,
	277000, 405955, 575424, 793117, 1067416, 1407375, 1822720, 2323849, 2921832, 3628411,
	4456000, 5417685, 6527224, 7799047, 9248256, 10890625, 12742600, 14821299, 17144512, 19730701,
	22599000, 25769215, 29261824, 33097977, 37299496, 41888875, 46889280, 52324549, 58219192, 64598391,
	71488000, 78914545, 86905224, 95487907, 104691136, 114544125, 125076760, 136319599, 148303872, 161061481,
	174625000, 189027675, 204303424, 220486837, 237613176, 255718375, 274839040, 295012449, 316276552, 338669971,
	362232000,
}

func xpForLevel(level int) int64 {
	if level < 0 || level >= len(xpTable) {
		return xpTable[len(xpTable)-1]
	}
	return xpTable[level]
}

// =============================================================================
// Comprehensive Opcode Logger
// =============================================================================

var (
	opcodeLogFile *os.File
	opcodeLogMu   sync.Mutex
	opcodeStats   = make(map[uint16]*opcodeStatEntry)
)

type opcodeStatEntry struct {
	Count    int
	TotalLen int
	MinLen   int
	MaxLen   int
}

// initOpcodeLog opens (or creates) the rolling opcode log.
func initOpcodeLog() {
	opcodeLogMu.Lock()
	defer opcodeLogMu.Unlock()
	if opcodeLogFile != nil {
		return
	}
	var err error
	opcodeLogFile, err = os.OpenFile("/tmp/opcodes.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Printf("Failed to open opcodes.log: %v", err)
	}
}

// logOpcode writes a comprehensive log entry for every received opcode.
func logOpcode(opcode uint16, msgType protocol.MessageType, seq uint16, data []byte) {
	initOpcodeLog()
	op := protocol.GameOpcode(opcode)
	name := op.String()

	// Update stats
	opcodeLogMu.Lock()
	s, ok := opcodeStats[opcode]
	if !ok {
		s = &opcodeStatEntry{MinLen: len(data), MaxLen: len(data)}
		opcodeStats[opcode] = s
	}
	s.Count++
	s.TotalLen += len(data)
	if len(data) < s.MinLen {
		s.MinLen = len(data)
	}
	if len(data) > s.MaxLen {
		s.MaxLen = len(data)
	}

	// Write to rolling log
	if opcodeLogFile != nil {
		ts := time.Now().Format("15:04:05.000")
		// First 64 bytes hex
		hexLen := len(data)
		if hexLen > 64 {
			hexLen = 64
		}
		hexStr := fmt.Sprintf("%X", data[:hexLen])
		if len(data) > 64 {
			hexStr += "..."
		}

		// Extract strings
		foundStrings := extractUTF16Strings(data)
		strPart := ""
		if len(foundStrings) > 0 {
			strPart = " strings=["
			for i, fs := range foundStrings {
				if i > 0 {
					strPart += "|"
				}
				if len(fs) > 60 {
					fs = fs[:60] + "..."
				}
				strPart += fs
			}
			strPart += "]"
		}

		entry := fmt.Sprintf("[%s] 0x%04X %-22s type=0x%02X seq=%d len=%d count=%d hex=%s%s\n",
			ts, opcode, name, byte(msgType), seq, len(data), s.Count, hexStr, strPart)
		opcodeLogFile.WriteString(entry)
	}
	opcodeLogMu.Unlock()

	// Also dump binary for new/important opcodes (first 3 occurrences)
	if s.Count <= 3 {
		base := fmt.Sprintf("/tmp/opcode_%04X_%d", opcode, s.Count)
		os.WriteFile(base+".bin", data, 0644)
		os.WriteFile(base+".hex", []byte(hex.Dump(data)), 0644)
	}

	// Console log for every opcode
	log.Printf("OPCODE 0x%04X %-22s type=0x%02X seq=%d len=%d count=%d",
		opcode, name, byte(msgType), seq, len(data), s.Count)
}

// extractUTF16Strings finds UTF-16LE strings in binary data.
// Looks for sequences of ASCII-range uint16 values (printable chars with 0x00 high byte).
func extractUTF16Strings(data []byte) []string {
	if len(data) < 4 {
		return nil
	}

	var result []string
	i := 0
	for i < len(data)-1 {
		// Look for potential string start: int32 length prefix followed by UTF-16LE
		if i+4 < len(data) {
			charCount := int(int32(binary.LittleEndian.Uint32(data[i:])))
			if charCount > 0 && charCount < 500 && i+4+charCount*2 <= len(data) {
				// Validate it's actually UTF-16LE text
				valid := true
				runes := make([]rune, charCount)
				for j := 0; j < charCount; j++ {
					c := binary.LittleEndian.Uint16(data[i+4+j*2:])
					if c < 0x20 || c > 0x7E {
						if c == 0x0027 || c == 0x002D || c == 0x003A || c == 0x002E { // ' - : .
							runes[j] = rune(c)
							continue
						}
						valid = false
						break
					}
					runes[j] = rune(c)
				}
				if valid && charCount >= 3 {
					result = append(result, string(runes))
					i += 4 + charCount*2
					continue
				}
			}
		}
		i++
	}
	return result
}

// PrintOpcodeStats prints accumulated opcode statistics.
func PrintOpcodeStats() {
	opcodeLogMu.Lock()
	defer opcodeLogMu.Unlock()
	log.Printf("=== Opcode Statistics ===")
	for opcode, s := range opcodeStats {
		op := protocol.GameOpcode(opcode)
		log.Printf("  0x%04X %-22s count=%d totalBytes=%d min=%d max=%d avg=%d",
			opcode, op.String(), s.Count, s.TotalLen, s.MinLen, s.MaxLen, s.TotalLen/s.Count)
	}
}

// parseItemData scans a payload for consecutive item records, for the item
// log only: it skips forward a byte at a time until one parses. Every handler
// that has a record in its message reads it with readNetItem (items.go).
func parseItemData(data []byte) []ParsedItem {
	if len(data) < 10 {
		return nil
	}

	var items []ParsedItem
	reader := buffer.NewReader(data)

	for reader.Remaining() > 10 {
		pos := reader.Position()
		item, err := parseSingleItem(reader)
		if err != nil {
			// If we parsed at least one item, that's progress
			if len(items) > 0 {
				break
			}
			// Try scanning forward for a string that looks like an item name
			reader = buffer.NewReader(data[pos+1:])
			continue
		}
		items = append(items, item)
	}
	return items
}

// ParsedItem is one item record as CLIENT holds it (game.h VINetItem): the
// six slot words that make an item a list entry, then the item proper
// (VIItem, 0x2E4 bytes). The fields are in wire order; the comment on each is
// its offset in the PS2's record. Names are FrontiersForge's (item.lua) and
// VIClient's getters' where the field has one, the offset where it has none.
type ParsedItem struct {
	InvSlot int // the record's index in ParsedItems: an inventory record's is its list index

	// The slot words (ReadNetItem, CLIENT 0x00CBB040).
	StackLeft     int64  // +0x2E4 quantity
	RemainingHP   int64  // +0x2E8 current HP; 0x0060 sets it
	Charges       int64  // +0x2EC; 0x003E counts it down
	EquipLocation int64  // +0x2F0 the equip slot worn in, -1 none (0x003F, 0x0040)
	QuickSlot     int64  // +0x2F4 the quick slot 0-3, -1 none (0x000A)
	Key           uint32 // +0x2F8 the list's own id for the record (GetSlotId): what 0x0060 and every request name it by

	// The item (func_00CBACE8).
	ItemID       int64      // +0x00
	ItemCost     int64      // +0x04 its value in tunar, before a merchant's rate
	P08          int64      // +0x08
	ItemIcon     int64      // +0x0C
	Icon2        int64      // +0x10 the Frontiers icon, used instead under expansion flag 2 (VIClient::ItemIcon)
	Slot         int64      // +0x14 the wear slot (1 head .. 27, -1 none)
	P18          int64      // +0x18
	NoTrade      int64      // +0x1C
	Rent         int64      // +0x20 0 = NO RENT
	P24          int64      // +0x24
	AttackType   int64      // +0x28 the weapon skill, 0 for none
	WeaponDamage int64      // +0x2C
	Range        int64      // +0x30
	LevelReq     int64      // +0x34
	MaxStack     int64      // +0x38
	MaxHP        int64      // +0x3C
	Durability   int64      // +0x40
	ClassUse     int64      // +0x44 class mask
	RaceUse      int64      // +0x48 race mask
	P4C          int64      // +0x4C quick-slot class: 2 may go in quick slots 0-1, 7 in quick slot 2 (Msg000A)
	IsLore       int64      // +0x50
	P58          int64      // +0x58, read before +0x54
	IsCraft      int64      // +0x54
	Name         string     // +0x5C, 63 characters at most
	Desc         string     // +0xDC, 255 characters at most
	Stats        []ItemStat // +0x2DC, in the record's order
}

// parseSingleItem reads one item record off the stream (readNetItem).
func parseSingleItem(reader *buffer.Reader) (ParsedItem, error) {
	return readNetItem(reader)
}

// AuctionItem holds a single auction house listing parsed from opcode 0x1206.
type AuctionItem struct {
	ListingID   int64      // auction listing ID (for cursor pagination)
	Seller      string     // seller character name (ASCII)
	Item        ParsedItem // item data (same format as MemoryDump items)
	CurrentBid  int64      // current bid in tunar
	BuyoutPrice int64      // buyout price in tunar (0 = no buyout)
	// The page's three per-listing arrays (VIBankControl +0x7880, +0x7888,
	// +0x7890). Not traced: on Sandstorm they are 0, -1 and the current bid
	// on every listing seen.
	PageValues [3]int64
}

// parseSingleSpell reads one ability of the MemoryDump's list. It is
// readAbility (VIAbility::Read): CLIENT's ReadAbilities (0x00BDCDC0) and
// 0x00B3 read the same record.
func parseSingleSpell(reader *buffer.Reader) (ParsedSpell, error) {
	return readAbility(reader)
}

// HotkeySlot holds one directional slot (N/W/E/S) within a hotkey entry.
type HotkeySlot struct {
	SlotID byte   // 0=N, 2=W, 4=E, 6=S
	Label  string // display label ("|14"=command code, " "=submenu, text=chat)
	Msg    string // message to send or submenu display name
}

// HotkeyEntry holds one node in the hotkey tree (dirCode encodes path).
type HotkeyEntry struct {
	DirCode int64        // path encoding: 0=root, 1=N, 5=W, 9=E, 13=S, deeper levels follow
	Slots   [4]HotkeySlot // N=0, W=1, E=2, S=3
}

// parseHotkeys reads the hotkey section of MemoryDump into HotkeyEntry structs.
// Wire format: count(zigzag) entries, each: zigzag(direction) + 4 × (byte(slotID) + WriteStringUnicode(label) + WriteStringUnicode(msg)).
func parseHotkeys(reader *buffer.Reader, count int64) []HotkeyEntry {
	entries := make([]HotkeyEntry, 0, count)
	for i := int64(0); i < count; i++ {
		dirCode, err := reader.Read7BitEncodedInt64()
		if err != nil {
			return entries
		}
		entry := HotkeyEntry{DirCode: dirCode}
		for slot := 0; slot < 4; slot++ {
			slotID, err := reader.ReadByte()
			if err != nil {
				return entries
			}
			entry.Slots[slot].SlotID = slotID

			// label: int32 charCount + UTF-16LE
			labelLen, err := reader.ReadInt32()
			if err != nil {
				return entries
			}
			if labelLen > 0 && labelLen < 500 {
				entry.Slots[slot].Label, _ = reader.ReadStringUnicode(int(labelLen))
			}

			// msg: int32 charCount + UTF-16LE
			msgLen, err := reader.ReadInt32()
			if err != nil {
				return entries
			}
			if msgLen > 0 && msgLen < 500 {
				entry.Slots[slot].Msg, _ = reader.ReadStringUnicode(int(msgLen))
			}
		}
		entries = append(entries, entry)
	}
	return entries
}

// ParsedSpell holds a decoded spell from the server (DumpSpell fields).
// ParsedSpell is one ability, VIAbility (game.h): the value of CLIENT's
// ability map, in VIAbility::Read's order.
type ParsedSpell struct {
	SpellID       int64           // +0x00: the map's key, the ability id
	AddedOrder    int64           // +0x04: display index, the server's key for it; 0xFC.. are items
	OnHotBar      int64           // +0x08: valid — memorized (0x00B4 swaps it)
	WhereOnHotBar int64           // +0x0C: its slot on the bar; 0x0032 sends it
	Lockout       int64           // +0x10: cooldown lockout in ms (0x00CD)
	ShowHide      int64           // +0x14: 0x00FD sets it; 0 also takes it off the bar
	AbilityLevel  int64           // +0x18
	RangeF        float32         // +0x1C: a raw float on the wire
	Range         int             // RangeF as an int, toward zero
	CastTime      int64           // +0x20
	Power         int64           // +0x24
	IconColor     int64           // +0x28: icon back, a UI texture hash
	Icon          int64           // +0x2C: icon front
	Scope         int64           // +0x30
	Recast        int64           // +0x34
	EqpRequire    int64           // +0x38: a bitmask
	Name          string          // +0x3C: 64 wide characters
	Desc          string          // +0xBC: 128
	Records       []AbilityRecord // +0x1BC: a counted list, empty on Sandstorm
}

// CMSkill represents one skill in the CM tree.
type CMSkill struct {
	ID       int
	Name     string
	HasName  bool // false = category placeholder (no skill to spend on)
	PrereqID int  // prerequisite skill ID (0 = none)
	Cost     int  // CM cost per rank
	Rank     byte // current rank
	MaxRank  byte // max rank (from tree flags)
}

// CMSkillDetail holds the detail view of a CM skill (name + description).
type CMSkillDetail struct {
	ID          int
	Name        string
	DisplayName string
	Description string
	Rank        byte
	MaxRank     byte
	Cost        int
}

// RequestCMTree sends the CM menu open request (0x1401 type=0x04).
func (gs *GameState) RequestCMTree() {
	if gs.Conn == nil {
		return
	}
	gs.Conn.QueueReliable(protocol.OpcodeClassMastery, []byte{0x04, 0x00, 0x00, 0x00, 0x00})
	gs.Conn.Flush()
	log.Printf("CM: requested tree (type=0x04)")
}

// RequestCMDetail sends a CM category browse request (0x1401 type=0x05).
func (gs *GameState) RequestCMDetail(skillID int) {
	if gs.Conn == nil {
		return
	}
	data := make([]byte, 9)
	data[0] = 0x05
	binary.LittleEndian.PutUint32(data[1:5], 0)
	binary.LittleEndian.PutUint32(data[5:9], uint32(skillID))
	gs.Conn.QueueReliable(protocol.OpcodeClassMastery, data)
	gs.Conn.Flush()
	log.Printf("CM: browse skill 0x%04X", skillID)
}

// SpendCM sends a CM spend request (0x1401 type=0x02).
func (gs *GameState) SpendCM(skillID int) {
	if gs.Conn == nil {
		return
	}
	data := make([]byte, 9)
	data[0] = 0x02
	binary.LittleEndian.PutUint32(data[1:5], 0)
	binary.LittleEndian.PutUint32(data[5:9], uint32(skillID))
	gs.Conn.QueueReliable(protocol.OpcodeClassMastery, data)
	gs.Conn.Flush()
	log.Printf("CM: spend on skill 0x%04X", skillID)
}

// FactionEntry represents one faction and the player's standing with it.
type FactionEntry struct {
	Name     string
	Standing int
}

// RequestFactions sends 34 ClientFaction (0x0AC4) requests, one per page.
// Server responds with FactionPage (0x07E2) for each.
func (gs *GameState) RequestFactions() {
	if gs.Conn == nil {
		return
	}
	gs.Factions = nil
	gs.FactionLoaded = false

	for pageIdx := 0; pageIdx <= 66; pageIdx += 2 {
		gs.FactionPageID++
		data := make([]byte, 16)
		w := buffer.NewWriter(data)
		w.WriteByte(0x66) // request marker
		w.Write7BitEncodedInt64(int64(gs.FactionPageID))
		w.WriteByte(byte(pageIdx))
		gs.Conn.QueueReliable(protocol.OpcodeClientFaction, data[:w.Position()])
	}
	gs.Conn.Flush()
	log.Printf("RequestFactions: sent 34 page requests (pageID starts at %d)", gs.FactionPageID-33)
}

// ParsedCMData holds the stats/skills/CM (Combat Mastery) section from the
// end of the MemoryDump message.
type ParsedCMData struct {
	Speed        float32    // Movement speed
	Skills       [26]int32  // 26 int32 craft/weapon skill values
	UnspentCMs   int64      // Unspent CM points
	SpentCMs     int64      // Spent CM points
	CMUnknown    int64      // Unknown zigzag (C# wrote as 3 raw bytes from pcap)
	CMPercentage int64      // CM progress percentage
	Speed2       float32    // Speed (written twice by server)

	// Base stat additions from CM investments
	StrAdd, StaAdd, AgiAdd, DexAdd, WisAdd, IntAdd, ChaAdd int64
	// CM stat maximums
	StrMax, StaMax, AgiMax, DexMax, WisMax, IntMax, ChaMax int64
	// HP/Power additions
	HPAdd, PowerAdd int64
	// HoT/PoT values
	HoT, PoT int64
	// AC addition
	ACAdd int64
	// Offensive/Defensive/HP Factor modifiers
	OFFMod, DEFMod, HPFactor int64
	// Resist additions
	FRAdd, LRAdd, CRAdd, ARAdd, PRAdd, DRAdd int64
}

// readStringUnicode reads int32(charCount) + UTF-16LE from a reader at the current position.
func readStringFromReader(reader *buffer.Reader) (string, error) {
	charCount, err := reader.ReadInt32()
	if err != nil {
		return "", err
	}
	if charCount <= 0 || charCount > 500 {
		return "", fmt.Errorf("bad string len: %d", charCount)
	}
	return reader.ReadStringUnicode(int(charCount))
}

// BuildHotbar populates the Hotbar slots from ParsedSpells.
// Spells with OnHotBar=1 are placed at their WhereOnHotBar slot (0-9).
func (gs *GameState) BuildHotbar() {
	// Clear hotbar
	for i := range gs.Hotbar {
		gs.Hotbar[i] = nil
		gs.HotbarNames[i] = ""
	}

	placed := 0
	for i := range gs.ParsedSpells {
		sp := &gs.ParsedSpells[i]
		if sp.OnHotBar != 0 && sp.WhereOnHotBar >= 0 && sp.WhereOnHotBar < 10 {
			slot := int(sp.WhereOnHotBar)
			gs.Hotbar[slot] = sp
			name := sp.Name
			if len(name) > 8 {
				name = name[:8]
			}
			gs.HotbarNames[slot] = name
			placed++
		}
	}
	log.Printf("Hotbar: %d/%d spells placed on hotbar", placed, len(gs.ParsedSpells))
	for i := 0; i < 10; i++ {
		if gs.Hotbar[i] != nil {
			log.Printf("  slot %d: %s (id=%d cast=%dms power=%d)",
				i, gs.Hotbar[i].Name, gs.Hotbar[i].SpellID, gs.Hotbar[i].CastTime, gs.Hotbar[i].Power)
		}
	}
}

// SetHotbarSlot assigns a spell to a hotbar slot (0-9). Pass nil to clear.
func (gs *GameState) SetHotbarSlot(slot int, sp *ParsedSpell) {
	if slot < 0 || slot >= 10 {
		return
	}
	// Clear old spell's OnHotBar flag if it was on this slot
	if gs.Hotbar[slot] != nil {
		gs.Hotbar[slot].OnHotBar = 0
		gs.Hotbar[slot].WhereOnHotBar = -1
	}
	gs.Hotbar[slot] = sp
	if sp != nil {
		sp.OnHotBar = 1
		sp.WhereOnHotBar = int64(slot)
		name := sp.Name
		if len(name) > 8 {
			name = name[:8]
		}
		gs.HotbarNames[slot] = name
		log.Printf("Hotbar: slot %d = %s", slot, sp.Name)
	} else {
		gs.HotbarNames[slot] = ""
		log.Printf("Hotbar: slot %d cleared", slot)
	}
}

// CooldownRemaining returns the remaining cooldown for a hotbar slot in seconds.
// Returns 0 if no cooldown is active.
func (gs *GameState) CooldownRemaining(slot int) float32 {
	if slot < 0 || slot >= 10 {
		return 0
	}
	rem := time.Until(gs.Cooldowns[slot]).Seconds()
	if rem <= 0 {
		return 0
	}
	return float32(rem)
}
