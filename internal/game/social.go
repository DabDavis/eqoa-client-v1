package game

// social.go — the social half of CLIENT's message intake: chat, the group, the
// buddy list, the guild, NPC dialogs, quests and the small lists. Each handler
// follows the CLIENT decomp (decomp/modules/client/src/viclient.cpp; the
// opcode map is claude-notes/decomp-phase62-dispatcher-survey-20261015.md),
// read for read: the same field widths, the same string limits and the same
// word-table lines. Handlers the Go dispatcher already routes keep their old
// names in state.go and handlers.go; the ones it does not route yet are here.

import (
	"encoding/binary"
	"fmt"
	"log"
	"sort"
	"strings"
	"unicode/utf16"

	"github.com/eqoa/client/internal/protocol"
)

// socialStream is VIMemStream's read side as CLIENT's handlers use it. A read
// that does not fit sets a sticky error and reads nothing, so a short message
// leaves zeroes behind instead of a panic or a misaligned parse.
type socialStream struct {
	b   []byte
	pos int
	err bool
}

// newSocialStream opens a message past its two opcode bytes, as the dispatcher
// does before it switches on them.
func newSocialStream(msg *protocol.Message) *socialStream {
	d := msg.Data()
	if len(d) < 2 {
		return &socialStream{err: true}
	}
	return &socialStream{b: d[2:]}
}

// take is VIMemStream::Read's size check: all n bytes or none.
func (s *socialStream) take(n int) []byte {
	if s.err || n < 0 || len(s.b)-s.pos < n {
		s.err = true
		return nil
	}
	p := s.b[s.pos : s.pos+n]
	s.pos += n
	return p
}

func (s *socialStream) u8() byte {
	if p := s.take(1); p != nil {
		return p[0]
	}
	return 0
}

func (s *socialStream) u32() uint32 {
	if p := s.take(4); p != nil {
		return binary.LittleEndian.Uint32(p)
	}
	return 0
}

// zigzag is ReadSigned (0x00CB8610): ReadUnsigned's seven bits a byte, low
// first, then the sign out of the low bit.
func (s *socialStream) zigzag() int32 {
	var u uint32
	for shift := uint(0); ; shift += 7 {
		b := s.u8()
		if shift < 32 {
			u |= uint32(b&0x7F) << shift
		}
		if b&0x80 == 0 || s.err {
			break
		}
	}
	if u&1 != 0 {
		return int32(^(u >> 1))
	}
	return int32(u >> 1)
}

// str8 is ReadString(char *, MaxBytes) (0x00CB8740): a u32 length, then the
// bytes when they fit MaxBytes with their terminator. A longer string is
// skipped and reads as empty. The bytes widen to characters one for one,
// as VIString::str8to16 does.
func (s *socialStream) str8(maxBytes int) string {
	n := s.u32()
	if uint64(n)+1 > uint64(maxBytes) {
		s.take(int(n))
		return ""
	}
	p := s.take(int(n))
	if p == nil {
		return ""
	}
	r := make([]rune, 0, len(p))
	for _, c := range p {
		if c == 0 {
			break
		}
		r = append(r, rune(c))
	}
	return string(r)
}

// str16 is ReadString(unsigned short *, MaxBytes) (0x00CB87E8): the same
// with a length in characters, which must fit MaxBytes/2 with the terminator.
func (s *socialStream) str16(maxBytes int) string {
	n := s.u32()
	if uint64(n)+1 > uint64(maxBytes/2) {
		s.take(int(n) * 2)
		return ""
	}
	p := s.take(int(n) * 2)
	if p == nil {
		return ""
	}
	u := make([]uint16, 0, n)
	for i := 0; i+1 < len(p); i += 2 {
		c := binary.LittleEndian.Uint16(p[i:])
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return string(utf16.Decode(u))
}

// --- the state this file keeps --------------------------------------------

// BuddyEntry is VIBuddy: the player record (zigzag id, name, class, race and
// level) plus the online byte and a wide string the record calls its second
// name. Sandstorm puts a note in it ("2 afk", "[Xtcmonk]").
type BuddyEntry struct {
	ID     int32
	Name   string
	Class  int32
	Race   int32
	Level  int32
	Online bool
	Note   string
}

// GroupInviteEntry is one pending group invitation (VIRec32_BE2DE8): the
// inviter's id, a second id 0x0773 carries, and the inviter's name.
type GroupInviteEntry struct {
	ID   uint32
	X    uint32
	Name string
}

// GuildInviteEntry is VIClientPart2EF24's invitation: who sent it and the
// guild's name.
type GuildInviteEntry struct {
	ID        uint32
	Name      string
	GuildName string
}

// OptionDialogue is an 0x0034 dialog: a question and up to six answers.
type OptionDialogue struct {
	ID      uint32
	Text    string
	Options []string
}

// MailHeader is one mailbox line out of 0x0D41's list (func_00CA9380).
type MailHeader struct {
	A, B, C uint32 // the three words before the sender, in wire order
	Sender  string
	Subject string
	Year    uint16
	Month   byte
	Day     byte
	Hour    byte
	Minute  byte
	Second  byte
	Read    bool
}

// SummonRequest is 0x00D3's question: someone wants to summon or
// resurrect the player.
type SummonRequest struct {
	Name      string
	Resurrect bool
}

// CMTreeEntry is one class-mastery tree record as func_00BF80A0 reads it.
type CMTreeEntry struct {
	ID        int32
	Owned     byte // +0x04: 0x1402/5 sets it, /6 clears it
	Available byte // +0x05: 0x1402/7 sets it; 0 on the "x" placeholders
	Index     int32
	Prereq    int32
	B16       byte
	Cost      int32
	Name      string
}

// SocialState is what the social handlers keep. GameState embeds it.
type SocialState struct {
	// Words is the string table, WORDS.TXT. A field of the same name on
	// GameState itself takes precedence over this one; either way an empty
	// table falls back to socialWords' retail English.
	Words Words

	// Buddies is m_buddies, a map keyed and ordered by id.
	Buddies []BuddyEntry

	// BuddyRequestID / BuddyRequestName are 0x076F's open invitation
	// (VIClient+0x2BB10, 0xFFFFFFFF = none).
	BuddyRequestID   uint32
	BuddyRequestName string

	// GroupInvites are the invitations not yet answered, oldest first.
	// GameState.GroupInviteID/GroupInviter mirror the first.
	GroupInvites []GroupInviteEntry
	// GroupIsLeader is VIClient+0x2BD78.
	GroupIsLeader bool

	// InGuild is VIClientGuild+0x04; GuildChatVisible its +0x29C.
	InGuild          bool
	GuildChatVisible bool
	// GuildInvite is the open guild invitation, nil for none.
	GuildInvite *GuildInviteEntry

	// DialogueQueue holds 0x0046 dialogs that arrived while one was open
	// (VIClient+0x2E7E8: twelve slots, eleven usable).
	DialogueQueue []string
	// PendingOptionDialogue is the 0x0034 dialog that arrived while a plain
	// one was open (VIClient+0x2E7F4, flag +0x2EBFC).
	PendingOptionDialogue *OptionDialogue

	// QuestTitles is the quest log, at most eight (VIClient+0x2C6E0, count
	// +0x2CEE0). QuestLogStamp is the u32 0x007C and 0x007D carry first
	// (VIClient+0x2BB0C).
	QuestTitles   []string
	QuestLogStamp uint32

	// PetID is VIClient+0x2BE50, 0xFFFFFFFF for none (0x007E, 0x007F).
	PetID uint32

	// AssistTarget is 0x007B's id. CLIENT targets it at once and sends 0x0010;
	// here it waits for the client to take it (AssistTargetPending).
	AssistTarget        uint32
	AssistTargetPending bool

	// ProfanityFilter is the last 0x0045.
	ProfanityFilter bool

	// Summon is the open 0x00D3 question, nil for none.
	Summon *SummonRequest

	// RandomName is 0x0012's name for the character-creation window;
	// RandomNameSeq counts them so a window can tell a new one.
	RandomName    string
	RandomNameSeq int

	// WhoToken is the token of the who list in WhoList: a page with the
	// same token adds to it, a new token starts it over.
	WhoToken int32

	// Mail is VIClientPart2EFD0. MailWait is its +0x04: the reply it waits
	// for (1 list, 2 message, 3 send, 4 delete), 0 for none, negative for
	// a failure code. Only a request sets it, so with no mail window every
	// reply but 0x0D41/5 is ignored, as on the PS2.
	MailWait    int32
	MailNew     bool
	MailHeaders []MailHeader
	MailWantID  int32
	MailBody    string

	// TradeskillSerial is VIClientPart3834C+0x78C0, the u32 0x1102's
	// results carry.
	TradeskillSerial uint32

	// CMTree is the class-mastery tree as sent; CMPoints is
	// VIClientPart4EC58+0x0C, whose rise 0x1402/3 announces.
	CMTree   []CMTreeEntry
	CMPoints uint32

	// sendHook, when set, takes the replies these handlers send (tests).
	sendHook func(op protocol.GameOpcode, payload []byte)
}

// --- text ----------------------------------------------------------------

// socialWords are the retail English strings these handlers print, for when
// no WORDS.TXT is loaded (the headless tool, tests).
var socialWords = map[int]string{
	519:  "Buddy invite from . . .",
	535:  "Guild invite from . . .",
	546:  "You are not qualified for that operation",
	550:  "Profanity Filter",
	551:  "On",
	552:  "Off",
	557:  "%s has accepted your invitation.",
	558:  "%s has declined your invitation.",
	559:  "%s is busy and cannot accept invitations.",
	560:  "%s cannot accept invitations.",
	563:  "Guild create failed: ",
	564:  "Guild disband failed: ",
	565:  "One or more group members are already in guilds.",
	566:  "That guild name belongs to another guild.",
	567:  "You must have at least %u Tunar to create a guild.",
	568:  "You must be the leader of a full group to create a guild.",
	570:  "You are not in a guild.",
	576:  "%s is already in a guild and cannot accept invitations.",
	578:  "The group leader has created a guild.",
	579:  "Your guild rank has changed.",
	580:  "Guild joined.",
	581:  "The guild charter has changed.",
	582:  "The guild has been disbanded.",
	583:  "You have been removed from the guild.",
	584:  "Invite failed: The guild is full.",
	586:  "%s has a full buddy list and cannot accept invitations.",
	587:  "Invitation to %s has expired.",
	589:  "Invitation failed: Too many open invites.",
	590:  "Add buddy failed: Your buddy list is full.",
	591:  "Add buddy failed: Your buddy's buddy list is full.",
	592:  "%s is already in your buddy list.",
	610:  "wants to summon you, do you accept?",
	611:  "wants to resurrect you, do you accept?",
	625:  "You have received more training points!",
	627:  "Recipient not found.",
	628:  "%s is already in a group.",
	630:  "The group leader has ejected you.",
	631:  "The group has been disbanded.",
	632:  "You have not been invited into this group.",
	633:  "The group is full.",
	634:  "Your group has been lost.",
	635:  "Invitation cancelled.",
	636:  "%s sends you a group invitation.  Accept?",
	642:  "%s is ignoring you.",
	647:  "Messages from your guild are visible.",
	648:  "Messages from your guild are hidden.",
	649:  "%s sends you a buddy list invitation, but you are busy.",
	650:  "%s sends you a guild invitation, but you are busy.",
	717:  "%s has added %s to the guild.",
	745:  "\"Member\" is the lowest rank and cannot be demoted.",
	838:  "There is no target for assisting.",
	839:  "Your mail has been sent.",
	840:  "Message deleted.",
	1022: "%s is still loading.",
	1041: "The guild has been disbanded by a server administrator.",
	1437: "You damaged your pole",
	1544: "You have scribed a new recipe.",
	1567: "You have not yet recovered.",
	1627: "You have earned %d mastery points.",
	1628: "You have earned 1 mastery point.",
}

// guildCreateCost is the Tunar a guild costs, the word CLIENT formats into
// string 567 (data_00CD7A50).
const guildCreateCost = 5000

// socialWord is VIStringTable::GetString(n): the loaded table's line, else
// the retail one.
func (gs *GameState) socialWord(n int) string {
	if s := gs.Words.Get(n); s != "" {
		return s
	}
	if s, ok := socialWords[n]; ok {
		return s
	}
	return fmt.Sprintf("#%d", n)
}

// socialFormat is the part of VIString::snprintf16 the words use: %s and %S
// take a string, %d and %u a number, %% is a percent sign.
func socialFormat(format string, args ...interface{}) string {
	var b strings.Builder
	a := 0
	for i := 0; i < len(format); i++ {
		c := format[i]
		if c != '%' || i+1 == len(format) {
			b.WriteByte(c)
			continue
		}
		i++
		switch format[i] {
		case 's', 'S', 'd', 'u':
			if a < len(args) {
				fmt.Fprint(&b, args[a])
				a++
			}
		case '%':
			b.WriteByte('%')
		default:
			b.WriteByte('%')
			b.WriteByte(format[i])
		}
	}
	return b.String()
}

// The chat colours CLIENT passes as VIColor32F, as bytes.
var (
	socialWhite = [3]uint8{255, 255, 255} // AddText's default (data_00CF5458, set to 1,1,1,1)
	socialRed   = [3]uint8{255, 63, 63}   // (1, .25, .25)
	socialGroup = [3]uint8{127, 127, 255} // (.5, .5, 1): 0x069E
	socialGuild = [3]uint8{63, 255, 204}  // (.25, 1, .8): guild chat, 0x0468/5
)

// socialLine is VIWndGame::AddText: one line in the chat window.
func (gs *GameState) socialLine(text string, ch ChatChannel, c [3]uint8) {
	gs.ChatLog = append(gs.ChatLog, ChatMessage{Text: text, Channel: ch, ColorR: c[0], ColorG: c[1], ColorB: c[2]})
	if len(gs.ChatLog) > 200 {
		gs.ChatLog = gs.ChatLog[len(gs.ChatLog)-200:]
	}
}

// socialShowWord is ShowMsg(id); socialShowText is ShowText. Both are a
// white chat line.
func (gs *GameState) socialShowWord(id int) {
	gs.socialLine(gs.socialWord(id), ChatSystem, socialWhite)
}

func (gs *GameState) socialShowText(text string) {
	gs.socialLine(text, ChatSystem, socialWhite)
}

// socialSend queues a reply the way a CLIENT handler sends one.
func (gs *GameState) socialSend(op protocol.GameOpcode, payload []byte) {
	if gs.sendHook != nil {
		gs.sendHook(op, payload)
		return
	}
	if gs.Conn == nil {
		return
	}
	if !gs.Conn.QueueReliable(op, payload) {
		log.Printf("social: 0x%04X send window full", uint16(op))
	}
}

// socialSendMsg is VIClient::SendMsg(op, v, b): a u32 and a byte.
func (gs *GameState) socialSendMsg(op protocol.GameOpcode, v uint32, b byte) {
	p := binary.LittleEndian.AppendUint32(nil, v)
	gs.socialSend(op, append(p, b))
}

// socialBusy stands in for !VIWndGame::IsLootable(), the game window being
// in any mode but its own: a dialog, a merchant or a blacksmith open.
func (gs *GameState) socialBusy() bool {
	return gs.DialogueActive || gs.MerchantOpen || gs.BlacksmithOpen
}

// --- dialogs ---------------------------------------------------------------

// socialShowDialog is VIClient::ShowDialog (0x00BDBC70): open the dialog when
// the window is free, else queue it, eleven deep, dropping it when full. The
// text is cut where the PS2's 218-character buffer cuts it. (The PS2 expands
// '%' escapes through VIClientText::Expand first; that is not ported.)
func (gs *GameState) socialShowDialog(id uint32, text string) {
	if r := []rune(text); len(r) > 217 {
		text = string(r[:217])
	}
	if !gs.socialBusy() {
		gs.DialogueText = text
		gs.DialogueOptions = nil
		gs.DialogueCounter = id
		gs.DialogueActive = true
		return
	}
	if len(gs.DialogueQueue) < 11 {
		gs.DialogueQueue = append(gs.DialogueQueue, text)
	}
}

// NextDialogue is what VIClient::Process does once the game window is free
// again: the oldest queued dialog first, then a waiting 0x0034 one. It
// returns false when nothing was waiting. The client calls it after closing
// a dialog.
func (gs *GameState) NextDialogue() bool {
	if gs.socialBusy() {
		return false
	}
	if len(gs.DialogueQueue) > 0 {
		gs.DialogueText = gs.DialogueQueue[0]
		gs.DialogueOptions = nil
		gs.DialogueActive = true
		gs.DialogueQueue = gs.DialogueQueue[1:]
		return true
	}
	if d := gs.PendingOptionDialogue; d != nil {
		gs.DialogueText = d.Text
		gs.DialogueOptions = d.Options
		gs.DialogueCounter = d.ID
		gs.DialogueActive = true
		gs.PendingOptionDialogue = nil
		return true
	}
	return false
}

// --- the group -------------------------------------------------------------

// socialLeaveGroup is the tail 0x069A, 0x062F, LeaveGroup and DisbandGroup
// share: no group, no members, no leader. (CLIENT also drops a group chat
// mode back to Say; the Go chat mode lives in the Engine.)
func (gs *GameState) socialLeaveGroup() {
	gs.GroupActive = false
	gs.GroupID = 0
	gs.GroupMembers = nil
	gs.GroupIsLeader = false
}

// socialSyncInvites drops the first pending invitation once the client has
// answered it, which it shows by clearing GroupInviteID, and mirrors the next.
func (gs *GameState) socialSyncInvites() {
	if len(gs.GroupInvites) > 0 && gs.GroupInviteID != gs.GroupInvites[0].ID {
		answered := gs.GroupInvites[0].ID
		kept := gs.GroupInvites[:0]
		for _, v := range gs.GroupInvites {
			if v.ID != answered {
				kept = append(kept, v)
			}
		}
		gs.GroupInvites = kept
	}
	gs.GroupInviteID, gs.GroupInviter = 0, ""
	if len(gs.GroupInvites) > 0 {
		gs.GroupInviteID = gs.GroupInvites[0].ID
		gs.GroupInviter = gs.GroupInvites[0].Name
	}
}

// handleGroupStuff is 0x069C: a byte, then for most values a name, and one
// line of the word table.
func (gs *GameState) handleGroupStuff(msg *protocol.Message) {
	s := newSocialStream(msg)
	sub := s.u8()
	word := map[byte]int{1: 558, 2: 628, 4: 642, 5: 1022}
	switch sub {
	case 3:
		gs.socialShowWord(627)
	case 1, 2, 4, 5:
		name := s.str8(24)
		gs.socialShowText(socialFormat(gs.socialWord(word[sub]), name))
	default:
		log.Printf("GroupStuff: sub %d ignored", sub)
	}
}

// handleGroupInviteCancel is 0x0774 (0x00BD3548): the first pending
// invitation from that id goes, with "Invitation cancelled."
func (gs *GameState) handleGroupInviteCancel(msg *protocol.Message) {
	s := newSocialStream(msg)
	id := s.u32()
	gs.socialSyncInvites()
	for i, v := range gs.GroupInvites {
		if v.ID == id {
			gs.GroupInvites = append(gs.GroupInvites[:i], gs.GroupInvites[i+1:]...)
			gs.socialShowWord(635)
			break
		}
	}
	gs.GroupInviteID, gs.GroupInviter = 0, ""
	if len(gs.GroupInvites) > 0 {
		gs.GroupInviteID = gs.GroupInvites[0].ID
		gs.GroupInviter = gs.GroupInvites[0].Name
	}
}

// handleGroupInviteAccepted is 0x0770 (0x00BDB650): a name and string 557.
func (gs *GameState) handleGroupInviteAccepted(msg *protocol.Message) {
	s := newSocialStream(msg)
	name := s.str8(24)
	gs.socialShowText(socialFormat(gs.socialWord(557), name))
}

// --- buddies -----------------------------------------------------------------

// socialSetBuddy is the map's Insert-or-replace by id; the slice stays in
// key order, as the tree is walked.
func (gs *GameState) socialSetBuddy(b BuddyEntry) {
	i := sort.Search(len(gs.Buddies), func(i int) bool { return gs.Buddies[i].ID >= b.ID })
	if i < len(gs.Buddies) && gs.Buddies[i].ID == b.ID {
		gs.Buddies[i] = b
	} else {
		gs.Buddies = append(gs.Buddies, BuddyEntry{})
		copy(gs.Buddies[i+1:], gs.Buddies[i:])
		gs.Buddies[i] = b
	}
	gs.socialMirrorBuddies()
}

// socialMirrorBuddies keeps LFGList, which the client's LFG tab draws, the
// same records: Sandstorm's "lfg" list arrives as buddy entries.
func (gs *GameState) socialMirrorBuddies() {
	gs.LFGList = gs.LFGList[:0]
	for _, b := range gs.Buddies {
		gs.LFGList = append(gs.LFGList, LFGEntry{
			CharID:    uint64(uint32(b.ID)),
			Name:      b.Name,
			Level:     byte(b.Level),
			Class:     byte(b.Class),
			GuildName: b.Note,
		})
	}
}

// handleBuddyListFull is 0x03D7: string 590.
func (gs *GameState) handleBuddyListFull(msg *protocol.Message) {
	gs.socialShowWord(590)
}

// --- pets, names --------------------------------------------------------------

// handlePetSet is 0x007E: the pet's id, VIClient+0x2BE50. 0x007F clears it.
func (gs *GameState) handlePetSet(msg *protocol.Message) {
	s := newSocialStream(msg)
	gs.PetID = s.u32()
	log.Printf("Pet: id=0x%08X", gs.PetID)
}

// handleRandomName is 0x0012: a name for the character-creation window
// (m_wnd2BB88->SetName).
func (gs *GameState) handleRandomName(msg *protocol.Message) {
	s := newSocialStream(msg)
	gs.RandomName = s.str8(24)
	gs.RandomNameSeq++
	log.Printf("RandomName: %q", gs.RandomName)
}

// --- the guild ---------------------------------------------------------------

// socialSetGuildMember is VIClientGuild::UpdateMember's Insert-or-replace.
func (gs *GameState) socialSetGuildMember(m GuildMember) {
	i := sort.Search(len(gs.GuildMembers), func(i int) bool { return gs.GuildMembers[i].CharID >= m.CharID })
	if i < len(gs.GuildMembers) && gs.GuildMembers[i].CharID == m.CharID {
		gs.GuildMembers[i] = m
		return
	}
	gs.GuildMembers = append(gs.GuildMembers, GuildMember{})
	copy(gs.GuildMembers[i+1:], gs.GuildMembers[i:])
	gs.GuildMembers[i] = m
}

// --- class mastery -------------------------------------------------------------

// readCMTreeEntry is func_00BF80A0.
func readCMTreeEntry(s *socialStream) CMTreeEntry {
	var e CMTreeEntry
	e.ID = s.zigzag()
	e.Owned = s.u8()
	e.Available = s.u8()
	e.Index = s.zigzag()
	e.Prereq = s.zigzag()
	e.B16 = s.u8()
	e.Cost = s.zigzag()
	e.Name = s.str16(128)
	return e
}
