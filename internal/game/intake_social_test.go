package game

import (
	"encoding/binary"
	"math"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/eqoa/client/internal/protocol"
)

// socialWire builds a message body the way the server writes it and CLIENT's
// VIMemStream reads it back.
type socialWire []byte

func (w socialWire) u8(v byte) socialWire { return append(w, v) }
func (w socialWire) u32(v uint32) socialWire {
	return binary.LittleEndian.AppendUint32(w, v)
}
func (w socialWire) zz(v int32) socialWire {
	u := uint32(v) << 1
	if v < 0 {
		u = uint32(^v)<<1 | 1
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
func (w socialWire) s8(s string) socialWire { return append(w.u32(uint32(len(s))), s...) }
func (w socialWire) s16(s string) socialWire {
	u := utf16.Encode([]rune(s))
	w = w.u32(uint32(len(u)))
	for _, c := range u {
		w = append(w, byte(c), byte(c>>8))
	}
	return w
}
func (w socialWire) msg(op uint16) *protocol.Message {
	m := &protocol.Message{Opcode: protocol.GameOpcode(op)}
	m.SetData(append([]byte{byte(op), byte(op >> 8)}, w...))
	return m
}

type socialSent struct {
	op      uint16
	payload []byte
}

func newSocialTestState() (*GameState, *[]socialSent) {
	gs := NewGameState("test", 10070, 9735, 7000, "test")
	var sent []socialSent
	gs.sendHook = func(op protocol.GameOpcode, p []byte) {
		sent = append(sent, socialSent{uint16(op), append([]byte(nil), p...)})
	}
	return gs, &sent
}

func lastChat(t *testing.T, gs *GameState) ChatMessage {
	t.Helper()
	if len(gs.ChatLog) == 0 {
		t.Fatal("no chat line")
	}
	return gs.ChatLog[len(gs.ChatLog)-1]
}

func wantChat(t *testing.T, gs *GameState, text string, rgb [3]uint8) {
	t.Helper()
	c := lastChat(t, gs)
	if c.Text != text || c.ColorR != rgb[0] || c.ColorG != rgb[1] || c.ColorB != rgb[2] {
		t.Fatalf("chat %q (%d,%d,%d), want %q %v", c.Text, c.ColorR, c.ColorG, c.ColorB, text, rgb)
	}
}

// The read primitives keep VIMemStream's limits: a string that does not fit
// its buffer is skipped whole and reads empty, the stream staying aligned; a
// read past the end sets the error and returns zero.
func TestSocialStreamLimits(t *testing.T) {
	m := socialWire{}.s8(strings.Repeat("a", 24)).s8("ok").s16(strings.Repeat("b", 64)).s16("fits").zz(-3).zz(300).msg(0)
	s := newSocialStream(m)
	if got := s.str8(24); got != "" {
		t.Fatalf("24 bytes in a 24-byte buffer read %q", got)
	}
	if got := s.str8(24); got != "ok" {
		t.Fatalf("next string %q", got)
	}
	if got := s.str16(128); got != "" {
		t.Fatalf("64 characters in 128 bytes read %q", got)
	}
	if got := s.str16(128); got != "fits" {
		t.Fatalf("next wide string %q", got)
	}
	if a, b := s.zigzag(), s.zigzag(); a != -3 || b != 300 || s.err {
		t.Fatalf("zigzag %d %d err %v", a, b, s.err)
	}
	if v := s.u32(); v != 0 || !s.err {
		t.Fatalf("read past the end: %d err %v", v, s.err)
	}
}

func TestSocialChatLines(t *testing.T) {
	gs, _ := newSocialTestState()

	// 0x0A7B: the colour is the three bytes after the string. (The old
	// handler wanted four and drew every line in its default green.)
	gs.handleColoredChat(socialWire{}.s16("Please review our server guidelines.").u8(255).u8(247).u8(0).msg(0x0A7B))
	wantChat(t, gs, "Please review our server guidelines.", [3]uint8{255, 247, 0})
	gs.handleColoredChat(socialWire{}.s16("Out of range.").u8(255).u8(63).u8(63).msg(0x0A7B))
	wantChat(t, gs, "Out of range.", [3]uint8{255, 63, 63})
	gs.handleColoredChat(socialWire{}.s16("Bob tells you: hi").u8(1).u8(2).u8(3).msg(0x0A7B))
	if c := lastChat(t, gs); c.Channel != ChatTell {
		t.Fatalf("tell sorted to channel %d", c.Channel)
	}

	// 0x0A7A: white, no "[Guild]".
	gs.handleClientMessage(socialWire{}.s16("You receive 38 tunar.").msg(0x0A7A))
	wantChat(t, gs, "You receive 38 tunar.", socialWhite)

	// 0x00CE and 0x069E.
	gs.handleErrorMessage(socialWire{}.s16("No.").msg(0x00CE))
	wantChat(t, gs, "No.", [3]uint8{255, 63, 63})
	gs.handleGroupChat(socialWire{}.u32(0x1234).s16("Bob: pull").msg(0x069E))
	wantChat(t, gs, "Bob: pull", [3]uint8{127, 127, 255})

	// A short message adds nothing.
	n := len(gs.ChatLog)
	gs.handleColoredChat(socialWire{}.u32(40).msg(0x0A7B))
	if len(gs.ChatLog) != n {
		t.Fatal("a short 0x0A7B added a line")
	}
}

func TestSocialGroupInvitations(t *testing.T) {
	gs, sent := newSocialTestState()

	// 0x0773 queues; the first is mirrored for the group menu.
	gs.handleGroupInvite(socialWire{}.u32(0x111).u32(7).s8("Alice").msg(0x0773))
	gs.handleGroupInvite(socialWire{}.u32(0x222).u32(8).s8("Bob").msg(0x0773))
	if len(gs.GroupInvites) != 2 || gs.GroupInviteID != 0x111 || gs.GroupInviter != "Alice" {
		t.Fatalf("invites %+v, mirror 0x%X %q", gs.GroupInvites, gs.GroupInviteID, gs.GroupInviter)
	}
	wantChat(t, gs, "Bob sends you a group invitation.  Accept?", socialWhite)

	// The menu answers the first by clearing the mirror; the next one moves up.
	gs.GroupInviteID = 0
	gs.handleGroupInviteCancel(socialWire{}.u32(0x999).msg(0x0774))
	if len(gs.GroupInvites) != 1 || gs.GroupInviteID != 0x222 {
		t.Fatalf("after the answer: %+v, mirror 0x%X", gs.GroupInvites, gs.GroupInviteID)
	}

	// 0x0774 cancels it.
	gs.handleGroupInviteCancel(socialWire{}.u32(0x222).msg(0x0774))
	if len(gs.GroupInvites) != 0 || gs.GroupInviteID != 0 {
		t.Fatalf("after the cancel: %+v, mirror 0x%X", gs.GroupInvites, gs.GroupInviteID)
	}
	wantChat(t, gs, "Invitation cancelled.", socialWhite)

	// Grouped, an invitation is declined at once: 0x0624, the id and 2.
	gs.GroupActive = true
	gs.handleGroupInvite(socialWire{}.u32(0x333).u32(9).s8("Carl").msg(0x0773))
	if len(*sent) != 1 || (*sent)[0].op != 0x0624 || string((*sent)[0].payload) != "\x33\x03\x00\x00\x02" {
		t.Fatalf("sent %+v", *sent)
	}
	if len(gs.GroupInvites) != 0 {
		t.Fatal("a declined invitation was queued")
	}

	// 0x0770, 0x069C.
	gs.handleGroupInviteAccepted(socialWire{}.s8("Dana").msg(0x0770))
	wantChat(t, gs, "Dana has accepted your invitation.", socialWhite)
	for sub, want := range map[byte]string{1: "Ed has declined your invitation.", 2: "Ed is already in a group.",
		4: "Ed is ignoring you.", 5: "Ed is still loading."} {
		gs.handleGroupStuff(socialWire{}.u8(sub).s8("Ed").msg(0x069C))
		wantChat(t, gs, want, socialWhite)
	}
	gs.handleGroupStuff(socialWire{}.u8(3).msg(0x069C))
	wantChat(t, gs, "Recipient not found.", socialWhite)
}

func TestSocialGroupRoster(t *testing.T) {
	gs, _ := newSocialTestState()
	gs.PlayerObjectID = 0x10

	roster := socialWire{}.u32(77).u32(3).u32(0x10).u32(0x20).u32(0x30).s8("Me").s8("Two").s8("Three")
	gs.handleCreateGroup(roster.msg(0x0772))
	if !gs.GroupActive || gs.GroupID != 77 || len(gs.GroupMembers) != 3 || !gs.GroupIsLeader {
		t.Fatalf("group %v id %d members %+v leader %v", gs.GroupActive, gs.GroupID, gs.GroupMembers, gs.GroupIsLeader)
	}
	if gs.GroupMembers[2].ObjectID != 0x30 || gs.GroupMembers[2].Name != "Three" {
		t.Fatalf("member 2 %+v", gs.GroupMembers[2])
	}
	if n := len(gs.ChatLog); n != 0 {
		t.Fatalf("a roster printed %d lines", n)
	}

	// 0x069A: out of the group, with the reason.
	gs.handleRemoveGroupMember(socialWire{}.u8(1).msg(0x069A))
	wantChat(t, gs, "The group leader has ejected you.", socialWhite)
	if gs.GroupActive || gs.GroupMembers != nil || gs.GroupIsLeader {
		t.Fatal("the group survived 0x069A")
	}
	// Not in a group: ignored.
	n := len(gs.ChatLog)
	gs.handleRemoveGroupMember(socialWire{}.u8(2).msg(0x069A))
	gs.handleDisbandGroup(socialWire{}.msg(0x062F))
	if len(gs.ChatLog) != n {
		t.Fatal("0x069A/0x062F printed outside a group")
	}
	gs.handleCreateGroup(roster.msg(0x0772))
	gs.handleDisbandGroup(socialWire{}.msg(0x062F))
	wantChat(t, gs, "Your group has been lost.", socialWhite)
}

func TestSocialDialogs(t *testing.T) {
	gs, sent := newSocialTestState()

	gs.handleDialogueBox(socialWire{}.u32(2).s16("Take this horse.").msg(0x0046))
	if !gs.DialogueActive || gs.DialogueText != "Take this horse." || gs.DialogueOptions != nil {
		t.Fatalf("dialog %v %q %v", gs.DialogueActive, gs.DialogueText, gs.DialogueOptions)
	}
	// A second waits its turn.
	gs.handleDialogueBox(socialWire{}.u32(3).s16(strings.Repeat("x", 300)).msg(0x0046))
	if len(gs.DialogueQueue) != 1 || len([]rune(gs.DialogueQueue[0])) != 217 {
		t.Fatalf("queue %d, first %d characters", len(gs.DialogueQueue), len([]rune(gs.DialogueQueue[0])))
	}
	// An option dialog over a plain one waits too, one at a time.
	opt := socialWire{}.u32(9).s16("Where to?").u8(2).s16("Guk").s16("Odus")
	gs.handleQuestPopup(opt.msg(0x0034))
	if d := gs.PendingOptionDialogue; d == nil || d.ID != 9 || len(d.Options) != 2 || d.Options[1] != "Odus" {
		t.Fatalf("pending %+v", gs.PendingOptionDialogue)
	}
	// A second is answered 255 at once.
	gs.handleQuestPopup(socialWire{}.u32(10).s16("Again?").u8(0).msg(0x0034))
	if len(*sent) != 1 || (*sent)[0].op != 0x0035 || string((*sent)[0].payload) != "\x0a\x00\x00\x00\xff" {
		t.Fatalf("sent %+v", *sent)
	}

	// Closing: the queued plain dialog first, then the option dialog.
	gs.DialogueActive = false
	if !gs.NextDialogue() || gs.DialogueOptions != nil || len(gs.DialogueQueue) != 0 {
		t.Fatal("the queued dialog did not open")
	}
	gs.DialogueActive = false
	if !gs.NextDialogue() || gs.DialogueCounter != 9 || len(gs.DialogueOptions) != 2 || gs.PendingOptionDialogue != nil {
		t.Fatalf("the option dialog did not open: %d %v", gs.DialogueCounter, gs.DialogueOptions)
	}
	gs.DialogueActive = false
	if gs.NextDialogue() {
		t.Fatal("NextDialogue opened from nothing")
	}

	// 0x0036: training points, and a dialog when they change. No experience.
	gs.TrainingPoints = 4
	xp := gs.Player.XPRaw
	gs.handleQuestStageComplete(socialWire{}.u8(1).zz(9).msg(0x0036))
	if gs.TrainingPoints != 9 || gs.DialogueText != "You have received more training points!" || gs.Player.XPRaw != xp {
		t.Fatalf("tp %d dialog %q xp %d", gs.TrainingPoints, gs.DialogueText, gs.Player.XPRaw)
	}
	gs.DialogueActive = false
	gs.DialogueText = ""
	gs.handleQuestStageComplete(socialWire{}.u8(0).zz(9).msg(0x0036))
	if gs.DialogueActive {
		t.Fatal("an unchanged count opened a dialog")
	}
}

func TestSocialQuestLogPetAssist(t *testing.T) {
	gs, _ := newSocialTestState()
	for _, q := range []string{"A", "B", "C"} {
		gs.handleAddQuestLog(socialWire{}.u32(5).s16(q).msg(0x007C))
	}
	gs.handleDeleteQuest(socialWire{}.u32(6).zz(1).msg(0x007D))
	if strings.Join(gs.QuestTitles, ",") != "A,C" || gs.QuestLogStamp != 6 {
		t.Fatalf("log %v stamp %d", gs.QuestTitles, gs.QuestLogStamp)
	}
	gs.handleDeleteQuest(socialWire{}.u32(6).zz(5).msg(0x007D)) // past the count: the last goes
	gs.handleDeleteQuest(socialWire{}.u32(6).zz(8).msg(0x007D)) // 8 or more: nothing
	if strings.Join(gs.QuestTitles, ",") != "A" {
		t.Fatalf("log %v", gs.QuestTitles)
	}
	for i := 0; i < 10; i++ {
		gs.handleAddQuestLog(socialWire{}.u32(5).s16("Q").msg(0x007C))
	}
	if len(gs.QuestTitles) != 8 {
		t.Fatalf("%d titles", len(gs.QuestTitles))
	}

	gs.handlePetSet(socialWire{}.u32(0xABCD).msg(0x007E))
	if gs.PetID != 0xABCD {
		t.Fatalf("pet 0x%X", gs.PetID)
	}
	gs.handleClearQuestTracker(socialWire{}.msg(0x007F))
	if gs.PetID != 0xFFFFFFFF {
		t.Fatalf("pet 0x%X after 0x007F", gs.PetID)
	}

	gs.handleUpdateQuestProgress(socialWire{}.u32(0x4242).msg(0x007B))
	if !gs.AssistTargetPending || gs.AssistTarget != 0x4242 {
		t.Fatalf("assist %v 0x%X", gs.AssistTargetPending, gs.AssistTarget)
	}
	gs.handleUpdateQuestProgress(socialWire{}.u32(0xFFFFFFFF).msg(0x007B))
	wantChat(t, gs, "There is no target for assisting.", socialWhite)

	gs.handleQuestDialogueResp(socialWire{}.zz(1).msg(0x0045))
	wantChat(t, gs, "Profanity Filter: On", socialWhite)
	gs.handleQuestDialogueResp(socialWire{}.zz(0).msg(0x0045))
	wantChat(t, gs, "Profanity Filter: Off", socialWhite)

	gs.handleNPCMessage(socialWire{}.s8("Cleric").u8(1).msg(0x00D3))
	wantChat(t, gs, "Clericwants to resurrect you, do you accept?", socialWhite)
	if gs.Summon == nil || !gs.Summon.Resurrect || gs.Summon.Name != "Cleric" {
		t.Fatalf("summon %+v", gs.Summon)
	}
	gs.handleRandomName(socialWire{}.s8("Zorbo").msg(0x0012))
	if gs.RandomName != "Zorbo" || gs.RandomNameSeq != 1 {
		t.Fatalf("random name %q %d", gs.RandomName, gs.RandomNameSeq)
	}
	n := len(gs.ChatLog)
	gs.handleStubNOP(socialWire{}.u32(1).u32(2).msg(0x006E))
	if len(gs.ChatLog) != n {
		t.Fatal("0x006E did something")
	}
}

func TestSocialBuddies(t *testing.T) {
	gs, sent := newSocialTestState()
	buddy := func(id int32, name string, class, race, level int32, online byte, note string) *protocol.Message {
		return socialWire{}.zz(id).s8(name).zz(class).zz(race).zz(level).u8(online).s16(note).msg(0x03D5)
	}
	gs.handleBuddyListEntry(buddy(500, "Zed", 10, 0, 45, 0, ""))
	gs.handleBuddyListEntry(buddy(100, "Amy", 5, 1, 38, 1, "[alt]"))
	gs.handleBuddyListEntry(buddy(500, "Zed", 10, 0, 46, 1, "2 afk"))
	if len(gs.Buddies) != 2 || gs.Buddies[0].Name != "Amy" || gs.Buddies[1].Level != 46 || !gs.Buddies[1].Online {
		t.Fatalf("buddies %+v", gs.Buddies)
	}
	if l := gs.LFGList; len(l) != 2 || l[1].Level != 46 || l[1].Class != 10 || l[1].GuildName != "2 afk" {
		t.Fatalf("LFG mirror %+v", gs.LFGList)
	}
	gs.handleBuddyOnlineStatus(socialWire{}.zz(100).msg(0x03D6))
	if len(gs.Buddies) != 1 || gs.Buddies[0].ID != 500 || len(gs.LFGList) != 1 {
		t.Fatalf("after 0x03D6: %+v", gs.Buddies)
	}

	gs.handleBuddyListFull(socialWire{}.msg(0x03D7))
	wantChat(t, gs, "Add buddy failed: Your buddy list is full.", socialWhite)
	gs.handleAddBuddyFailed(socialWire{}.msg(0x03D8))
	wantChat(t, gs, "Add buddy failed: Your buddy's buddy list is full.", socialWhite)
	gs.handleBuddyListStuff(socialWire{}.u8(6).s8("Ann").msg(0x0771))
	wantChat(t, gs, "Invitation to Ann has expired.", socialWhite)

	// 0x076F is a buddy invitation: free, it waits for an answer.
	gs.handleBuffNotification(socialWire{}.u32(0x77).s8("Pal").msg(0x076F))
	if gs.BuddyRequestID != 0x77 || len(*sent) != 0 {
		t.Fatalf("request 0x%X, sent %+v", gs.BuddyRequestID, *sent)
	}
	wantChat(t, gs, "Buddy invite from . . . Pal", socialWhite)
	gs.AnswerBuddyRequest(true)
	if len(*sent) != 1 || (*sent)[0].op != 0x0367 || string((*sent)[0].payload) != "\x77\x00\x00\x00" {
		t.Fatalf("accept sent %+v", *sent)
	}
	// Busy: declined with 1.
	gs.DialogueActive = true
	gs.handleBuffNotification(socialWire{}.u32(0x78).s8("Pal").msg(0x076F))
	if s := (*sent)[len(*sent)-1]; s.op != 0x0368 || string(s.payload) != "\x78\x00\x00\x00\x01" {
		t.Fatalf("busy sent %+v", s)
	}
	wantChat(t, gs, "Pal sends you a buddy list invitation, but you are busy.", socialWhite)
	// Thirty-two buddies: declined with 8.
	gs.DialogueActive = false
	for i := int32(0); i < 32; i++ {
		gs.handleBuddyListEntry(buddy(1000+i, "B", 0, 0, 1, 0, ""))
	}
	gs.handleBuffNotification(socialWire{}.u32(0x79).s8("Pal").msg(0x076F))
	if s := (*sent)[len(*sent)-1]; s.op != 0x0368 || string(s.payload) != "\x79\x00\x00\x00\x08" {
		t.Fatalf("full sent %+v", s)
	}
}

func TestSocialGuild(t *testing.T) {
	gs, sent := newSocialTestState()
	gs.handleGuildInfo(socialWire{}.u8(1).s16("Prophets").s16("Be nice").msg(0x0468))
	if !gs.InGuild || gs.GuildName != "Prophets" || gs.GuildMOTD != "Be nice" {
		t.Fatalf("guild %v %q %q", gs.InGuild, gs.GuildName, gs.GuildMOTD)
	}
	wantChat(t, gs, "[Guild MOTD] Prophets: Be nice", socialGuild)

	member := func(id int32, name string, class, race, level, rank int32, online byte) *protocol.Message {
		return socialWire{}.u8(3).zz(id).s8(name).zz(class).zz(race).zz(level).zz(rank).u8(online).msg(0x0468)
	}
	gs.handleGuildInfo(member(25175, "Mcgregor", 5, 0, 51, 0, 1))
	gs.handleGuildInfo(member(18955, "Dank", 13, 2, 55, 1, 0))
	gs.handleGuildInfo(member(25175, "Mcgregor", 5, 0, 52, 0, 1))
	if len(gs.GuildMembers) != 2 || gs.GuildMembers[0].Name != "Dank" || gs.GuildMembers[1].Level != 52 ||
		gs.GuildMembers[0].Race != 2 || gs.GuildMembers[0].Rank != 1 || !gs.GuildMembers[1].Online {
		t.Fatalf("members %+v", gs.GuildMembers)
	}
	gs.handleGuildInfo(socialWire{}.u8(4).zz(18955).msg(0x0468))
	if len(gs.GuildMembers) != 1 {
		t.Fatalf("after remove: %+v", gs.GuildMembers)
	}

	gs.handleGuildInfo(socialWire{}.u8(5).u32(1).s16("Mcgregor: hi").msg(0x0468))
	wantChat(t, gs, "Mcgregor: hi", socialGuild)
	gs.handleGuildInfo(socialWire{}.u8(13).msg(0x0468))
	if !gs.GuildChatVisible || gs.GuildLeader {
		t.Fatal("13 is the guild-messages flag, not leadership")
	}
	wantChat(t, gs, "Messages from your guild are visible.", socialWhite)
	gs.handleGuildInfo(socialWire{}.u8(9).msg(0x0468))
	wantChat(t, gs, "Guild joined.", socialWhite)
	gs.handleGuildInfo(socialWire{}.u8(15).s8("Ann").s8("Bob").msg(0x0468))
	wantChat(t, gs, "Ann has added Bob to the guild.", socialWhite)

	// 0x057A/1 while in a guild: declined with 4.
	inv := socialWire{}.u8(1).zz(3).u32(0x55).s8("Ann").s16("Other Guild")
	gs.handleGuildInviteStuff(inv.msg(0x057A))
	if len(*sent) != 1 || (*sent)[0].op != 0x0578 || string((*sent)[0].payload) != "\x0a\x55\x00\x00\x00\x04" {
		t.Fatalf("in-guild sent %+v", *sent)
	}
	gs.handleGuildInfo(socialWire{}.u8(2).msg(0x0468))
	if gs.InGuild || gs.GuildName != "" || gs.GuildMembers != nil {
		t.Fatal("2 left the guild in place")
	}
	gs.handleGuildInviteStuff(inv.msg(0x057A))
	if gs.GuildInvite == nil || gs.GuildInvite.ID != 0x55 || gs.GuildInvite.GuildName != "Other Guild" || len(*sent) != 1 {
		t.Fatalf("invite %+v sent %d", gs.GuildInvite, len(*sent))
	}
	gs.handleGuildInviteStuff(socialWire{}.u8(3).u8(4).s8("Cy").msg(0x057A))
	wantChat(t, gs, "Cy is already in a guild and cannot accept invitations.", socialWhite)

	// 0x0579 is a failed guild command, not a world entry.
	gs.Phase = PhaseInWorld
	gs.handleGuildWorldEntry(socialWire{}.u8(1).u8(6).msg(0x0579))
	if gs.Phase != PhaseInWorld {
		t.Fatalf("phase %v after 0x0579", gs.Phase)
	}
	wantChat(t, gs, "You must have at least 5000 Tunar to create a guild.", socialWhite)
	if gs.ChatLog[len(gs.ChatLog)-2].Text != "Guild create failed: " {
		t.Fatalf("line before %q", gs.ChatLog[len(gs.ChatLog)-2].Text)
	}
	n := len(gs.ChatLog)
	gs.handleGuildWorldEntry(socialWire{}.u8(1).u8(0).msg(0x0579))
	if len(gs.ChatLog) != n {
		t.Fatal("reason 0 printed")
	}
}

func TestSocialLists(t *testing.T) {
	gs, _ := newSocialTestState()

	// 0x07E2: the page fields are zigzag, then the factions.
	page := func(token, pg, pages int32, names ...string) *protocol.Message {
		w := socialWire{}.u8(0x77).zz(token).zz(pg).zz(pages).zz(int32(len(names)))
		for i, n := range names {
			w = w.s16(n).zz(int32(2000 - i))
		}
		return w.msg(0x07E2)
	}
	gs.handleFactionPage(page(2, 0, 2, "Gypsies of Tunaria", "Black Swan Inn"))
	if len(gs.Factions) != 2 || gs.Factions[1].Standing != 1999 || gs.FactionLoaded {
		t.Fatalf("factions %+v loaded %v", gs.Factions, gs.FactionLoaded)
	}
	gs.handleFactionPage(page(70, 1, 2, "Citizens of Freeport"))
	if len(gs.Factions) != 3 || !gs.FactionLoaded {
		t.Fatalf("factions %d loaded %v", len(gs.Factions), gs.FactionLoaded)
	}

	// 0x0E02: a two-byte token; a page with the same token adds to the list.
	who := func(token int32, names ...string) *protocol.Message {
		w := socialWire{}.u8(1).zz(token).zz(int32(len(names)))
		for i, n := range names {
			w = w.u8(1).u8(2).u8(byte(40 + i)).s8(n).u32(uint32(0x100 + i))
		}
		return w.msg(0x0E02)
	}
	gs.handleWhoListResponse(who(100, "Ann", "Bob"))
	gs.handleWhoListResponse(who(100, "Cy"))
	if len(gs.WhoList) != 3 || gs.WhoList[2].Name != "Cy" || gs.WhoList[1].Level != 41 {
		t.Fatalf("who %+v", gs.WhoList)
	}
	gs.handleWhoListResponse(who(101, "Dee"))
	if len(gs.WhoList) != 1 || gs.WhoList[0].Name != "Dee" {
		t.Fatalf("new token: %+v", gs.WhoList)
	}

	// 0x0F02 is fishing; it leaves the who list alone.
	gs.handleWhoListFiltered(socialWire{}.u8(10).msg(0x0F02))
	wantChat(t, gs, "You damaged your pole", socialWhite)
	if len(gs.WhoList) != 1 {
		t.Fatal("0x0F02 touched the who list")
	}

	// 0x1102 is tradeskills; it leaves the auction and the ignore list alone.
	gs.handleIgnoreListResult(socialWire{}.u8(12).u8(4).u32(0x1234).msg(0x1102))
	gs.handleIgnoreListResult(socialWire{}.u8(1).u32(0x1235).msg(0x1102))
	wantChat(t, gs, "You have scribed a new recipe.", socialWhite)
	if gs.AuctionSessionID != 0 || len(gs.IgnoreList) != 0 || gs.TradeskillSerial != 0x1235 {
		t.Fatalf("auction %d ignore %v serial 0x%X", gs.AuctionSessionID, gs.IgnoreList, gs.TradeskillSerial)
	}
	gs.handleIgnoreListResult(socialWire{}.u8(5).u32(1).msg(0x1102))
	wantChat(t, gs, "You have not yet recovered.", [3]uint8{255, 63, 63})

	// 0x0D41: only 5 without a request.
	hdr := socialWire{}.u8(2).u8(2).zz(1).u32(7).u32(8).u32(9).s8("Ann").s16("Hello").
		u8(0xEA).u8(0x07).u8(10).u8(15).u8(12).u8(30).u8(5).u8(0)
	gs.handleMailDelivery(hdr.msg(0x0D41))
	if len(gs.MailHeaders) != 0 {
		t.Fatal("an unrequested list was kept")
	}
	gs.handleMailDelivery(socialWire{}.u8(5).msg(0x0D41))
	if !gs.MailNew {
		t.Fatal("0x0D41/5 did not flag new mail")
	}
	gs.MailWait = 1
	gs.handleMailDelivery(hdr.msg(0x0D41))
	if len(gs.MailHeaders) != 1 || gs.MailNew || gs.MailWait != 0 {
		t.Fatalf("headers %+v new %v wait %d", gs.MailHeaders, gs.MailNew, gs.MailWait)
	}
	if h := gs.MailHeaders[0]; h.A != 7 || h.Sender != "Ann" || h.Subject != "Hello" || h.Year != 2026 || h.Minute != 30 {
		t.Fatalf("header %+v", h)
	}
	gs.MailWait = 3
	gs.handleMailDelivery(socialWire{}.u8(1).u8(10).msg(0x0D41))
	if gs.MailWait != -2 {
		t.Fatalf("send result 10 left wait %d", gs.MailWait)
	}
}

func TestSocialClassMastery(t *testing.T) {
	gs, _ := newSocialTestState()
	entry := func(w socialWire, id int32, owned, avail byte, prereq, cost int32, name string) socialWire {
		return w.zz(id).u8(owned).u8(avail).zz(id).zz(prereq).u8(0).zz(cost).s16(name)
	}
	w := socialWire{}.u8(8).u32(0).u32(4)
	w = entry(w, 1, 0, 0, 0, 10, "x")
	w = entry(w, 8, 1, 1, 7, 20, "Vigor 3")
	w = entry(w, 9, 1, 1, 8, 25, "Vigor 4")
	w = entry(w, 993, 0, 1, 0, 100, "Lycanthropic Resistances")
	gs.handleClassMasteryServer(w.msg(0x1402))
	if len(gs.CMTree) != 4 || len(gs.CMSkills) != 3 || !gs.CMTreeLoaded || gs.CMUnspent != 0 {
		t.Fatalf("tree %d skills %d loaded %v unspent %d", len(gs.CMTree), len(gs.CMSkills), gs.CMTreeLoaded, gs.CMUnspent)
	}
	if sk := gs.CMSkills[1]; sk.ID != 9 || sk.Cost != 25 || sk.PrereqID != 8 || sk.Rank != 1 || sk.Name != "Vigor 4" {
		t.Fatalf("Vigor 4 %+v", sk)
	}
	if sk := gs.CMSkills[2]; sk.ID != 993 || sk.Cost != 100 || sk.Rank != 0 {
		t.Fatalf("two-byte id %+v", sk)
	}

	// 9: the detail record.
	d := socialWire{}.u8(9).u32(0).zz(9).u8(1).u8(0).zz(9).zz(8).u8(0).u8(45)
	for i := 0; i < 9; i++ {
		d = d.zz(0)
	}
	d = d.s16("Vigor 4").zz(0).s16("Vigor 4").s16("Vigor increases your stamina.").
		zz(2).u8(1).zz(4).u8(8).zz(30).zz(0).zz(1).s16("Vigor 3")
	gs.handleClassMasteryServer(d.msg(0x1402))
	if cd := gs.CMDetail; cd == nil || cd.ID != 9 || cd.Description != "Vigor increases your stamina." || cd.Cost != 25 || cd.Rank != 1 {
		t.Fatalf("detail %+v", gs.CMDetail)
	}

	// 3: the points; a rise is announced.
	gs.handleClassMasteryServer(socialWire{}.u8(3).u32(12).u32(710).msg(0x1402))
	wantChat(t, gs, "You have earned 12 mastery points.", socialWhite)
	gs.handleClassMasteryServer(socialWire{}.u8(3).u32(13).u32(709).msg(0x1402))
	wantChat(t, gs, "You have earned 1 mastery point.", socialWhite)
	if gs.CMUnspent != 709 || gs.CMPoints != 13 {
		t.Fatalf("points %d unspent %d", gs.CMPoints, gs.CMUnspent)
	}

	// 5: bought, with the run speed.
	gs.handleClassMasteryServer(socialWire{}.u8(5).u32(0).u32(993).u32(13).u32(609).u32(math.Float32bits(7.5)).msg(0x1402))
	if gs.CMSkills[2].Rank != 1 || gs.CMUnspent != 609 || gs.Player.Speed != 7.5 {
		t.Fatalf("after buying: %+v unspent %d speed %v", gs.CMSkills[2], gs.CMUnspent, gs.Player.Speed)
	}
	// 7: availability.
	gs.handleClassMasteryServer(socialWire{}.u8(7).u32(1).u32(1).msg(0x1402))
	if len(gs.CMSkills) != 4 {
		t.Fatalf("%d skills after 7", len(gs.CMSkills))
	}
}
