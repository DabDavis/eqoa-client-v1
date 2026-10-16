package game

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/eqoa/client/internal/protocol"
)

// The senders against CLIENT's (egress pass, lane senders). Where a PS2
// capture holds the message, the Go bytes are pinned to the PS2's; the rest
// are pinned to the decomp's writes (VIMemStream: a raw word is 4 bytes LE,
// an int through operator<< is zigzag, a wide string is a u32 count of UTF-16
// units then the units).

type sentMsg struct {
	op protocol.GameOpcode
	p  []byte
}

func (m sentMsg) String() string { return fmt.Sprintf("%04X % X", uint16(m.op), m.p) }

// senderState is a GameState whose sends are taken, in order.
func senderState() (*GameState, *[]sentMsg) {
	gs := NewGameState("test", 10070, 9735, 7000, "test")
	var sent []sentMsg
	gs.sendHook = func(op protocol.GameOpcode, p []byte) {
		sent = append(sent, sentMsg{op, append([]byte(nil), p...)})
	}
	return gs, &sent
}

func wantSent(t *testing.T, got []sentMsg, want ...sentMsg) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("sent %d messages %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i].op != want[i].op || !bytes.Equal(got[i].p, want[i].p) {
			t.Errorf("message %d: %v, want %v", i, got[i], want[i])
		}
	}
}

func wide(s string) []byte { return appendWide(nil, s) }

func u32(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }

func cat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

const (
	opMode  = protocol.OpcodeChangeChatMode
	opSay   = protocol.OpcodeClientSayChat
	opShout = protocol.OpcodeClientShout
	opGuild = protocol.GameOpcode(0x0578)
)

func TestEgressChatCommands(t *testing.T) {
	gs, sent := senderState()
	gs.ChatMode = ChatModeGroup // a kept mode, so the restore is visible

	// /say: 0x000E 0, 0x000B {wide, target}, 0x000E back (func_00BCFE78 + func_00BE6188).
	gs.ChatSay("hi", 0x013CC7F3)
	wantSent(t, *sent,
		sentMsg{opMode, []byte{0}},
		sentMsg{opSay, cat(u32(2), []byte{'h', 0, 'i', 0}, u32(0x013CC7F3))},
		sentMsg{opMode, []byte{2}})
	*sent = nil

	// No target is m_target's -1.
	gs.ChatGroup("x", 0)
	wantSent(t, *sent,
		sentMsg{opMode, []byte{2}},
		sentMsg{opSay, cat(wide("x"), u32(0xFFFFFFFF))},
		sentMsg{opMode, []byte{2}})
	*sent = nil

	// /guild: mode 4 and 0x0578 {12, wide} (func_00BEF1B0); not 0x000B.
	gs.ChatGuild("guild")
	wantSent(t, *sent,
		sentMsg{opMode, []byte{4}},
		sentMsg{opGuild, cat([]byte{12}, wide("guild"))},
		sentMsg{opMode, []byte{2}})
	*sent = nil

	// /shout: mode 3 and 0x0C01 {2, wide} (func_00CBBE10); the old sub byte was 1.
	gs.ChatShout("loud")
	wantSent(t, *sent,
		sentMsg{opMode, []byte{3}},
		sentMsg{opShout, cat([]byte{2}, wide("loud"))},
		sentMsg{opMode, []byte{2}})
	*sent = nil

	if gs.ChatMode != ChatModeGroup {
		t.Errorf("a /command changed the kept mode to %d", gs.ChatMode)
	}

	// /tell goes out whole, in no mode; /reply as "/t <name> <text>".
	gs.ChatCommand("/t Bob hello", 0)
	gs.Session.ReplyName = "Ann"
	if !gs.ChatReply("yes", 7) {
		t.Fatal("ChatReply with a reply name sent nothing")
	}
	wantSent(t, *sent,
		sentMsg{opSay, cat(wide("/t Bob hello"), u32(0xFFFFFFFF))},
		sentMsg{opSay, cat(wide("/t Ann yes"), u32(7))})
	*sent = nil
	gs.Session.ReplyName = ""
	if gs.ChatReply("yes", 7) || len(*sent) != 0 {
		t.Errorf("ChatReply with nobody to reply to sent %v", *sent)
	}

	// 128 UTF-16 units or more: nothing at all.
	long := string(bytes.Repeat([]byte{'a'}, 128))
	gs.ChatSay(long, 0)
	gs.ChatLine(long, 0)
	if len(*sent) != 0 {
		t.Errorf("a 128-unit line was sent: %d messages", len(*sent))
	}
	gs.ChatSay(long[:127], 0)
	if len(*sent) != 3 {
		t.Errorf("a 127-unit line: %d messages, want 3", len(*sent))
	}
}

func TestEgressChatLineFollowsTheMode(t *testing.T) {
	gs, sent := senderState()
	for _, c := range []struct {
		mode byte
		want sentMsg
	}{
		{ChatModeSay, sentMsg{opSay, cat(wide("t"), u32(0xFFFFFFFF))}},
		{ChatModeWhisper, sentMsg{opSay, cat(wide("t"), u32(0xFFFFFFFF))}},
		{ChatModeGroup, sentMsg{opSay, cat(wide("t"), u32(0xFFFFFFFF))}},
		{ChatModeShout, sentMsg{opShout, cat([]byte{2}, wide("t"))}},
		{ChatModeGuild, sentMsg{opGuild, cat([]byte{12}, wide("t"))}},
	} {
		*sent = nil
		gs.ChatMode = c.mode
		gs.ChatLine("t", 0)
		wantSent(t, *sent, c.want) // no 0x000E: the server has the mode
	}
}

func TestEgressGroupChangedAndWorldEntry(t *testing.T) {
	gs, sent := senderState()
	gs.Phase = PhaseInWorld
	gs.SetChatMode(ChatModeShout)
	wantSent(t, *sent, sentMsg{opMode, []byte{3}})
	if n := len(gs.ChatLog); n == 0 || gs.ChatLog[n-1].Text != "Chat Mode: Shout." {
		t.Errorf("GroupChanged's line: %+v", gs.ChatLog)
	}
	*sent = nil

	// Msg000D's four, the kept mode in the 0x000E.
	gs.sendWorldEntry()
	wantSent(t, *sent,
		sentMsg{protocol.OpcodeCharacterInWorld, nil},
		sentMsg{opMode, []byte{3}},
		sentMsg{protocol.OpcodeFactionMessages, []byte{0, 0}},
		sentMsg{protocol.OpcodeLootMessages, []byte{0, 0}})

	// Before the world, the mode is only kept.
	gs2, sent2 := senderState()
	gs2.SetChatMode(ChatModeGroup)
	if len(*sent2) != 0 || gs2.ChatMode != ChatModeGroup {
		t.Errorf("out of the world: sent %v, mode %d", *sent2, gs2.ChatMode)
	}
}

// TestEgressWorldEntryAgainstThePS2 pins sendWorldEntry to what a PS2 sent
// after every MemoryDump: the promoted session's first four guaranteed
// messages (chat mode say in every capture).
func TestEgressWorldEntryAgainstThePS2(t *testing.T) {
	gs, sent := senderState()
	gs.sendWorldEntry()
	var want []sentMsg
	for _, m := range *sent {
		want = append(want, m)
	}
	found := 0
	all := egressAll(t)
	for i := 0; i+3 < len(all); i++ {
		if all[i].Opcode != 0x0014 || all[i].IsState() {
			continue
		}
		found++
		for k, w := range want {
			m := all[i+k]
			if m.Opcode != uint16(w.op) || !bytes.Equal(m.Payload(), w.p) {
				t.Errorf("%s datagram %d: PS2 message %d is %04X % X, Go sends %v",
					m.Capture, m.Datagram, k, m.Opcode, m.Payload(), w)
			}
		}
	}
	if found < 8 {
		t.Fatalf("%d PS2 world entries in the captures, want the 11 the census shows", found)
	}
}

func TestEgressGuildCommands(t *testing.T) {
	gs, sent := senderState()
	gs.Player.Name = "Mcgregor"
	gs.GuildName = "Prophets"
	gs.GuildMembers = []GuildMember{{CharID: 1, Name: "Hesher"}, {CharID: 300, Name: "Kake"}}

	gs.GuildCreate("Ab")
	gs.GuildDisband()
	gs.GuildLeave()
	gs.GuildCharter("c")
	gs.InviteToGuild("Bob")
	if !gs.GuildPromote("hesher") || !gs.GuildDemote("Kake") || !gs.GuildRemove("KAKE") {
		t.Fatal("a member command found no member")
	}
	wantSent(t, *sent,
		sentMsg{opGuild, cat([]byte{1}, wide("Ab"))},
		sentMsg{opGuild, []byte{2}},
		sentMsg{opGuild, []byte{3}},
		sentMsg{opGuild, cat([]byte{11}, wide("c"))},
		sentMsg{opGuild, cat([]byte{8}, u32(3), []byte("Bob"), wide("Prophets"))},
		sentMsg{opGuild, []byte{4, 2}},       // zigzag 1
		sentMsg{opGuild, []byte{5, 0xD8, 4}}, // zigzag 300
		sentMsg{opGuild, []byte{6, 0xD8, 4}})
	*sent = nil

	// Not oneself (679), and not a name that is no member.
	gs.InviteToGuild("mcgregor")
	if gs.GuildRemove("Nobody") || len(*sent) != 0 {
		t.Errorf("sent %v", *sent)
	}
	if n := len(gs.ChatLog); n == 0 || gs.ChatLog[n-1].Text != "You can't invite yourself." {
		t.Errorf("679 not shown: %+v", gs.ChatLog)
	}

	// The open invitation: taken {9, id}, declined {10, id, 1}.
	gs.GuildInvite = &GuildInviteEntry{ID: 0x01020304}
	gs.AnswerGuildInvite(true)
	gs.GuildInvite = &GuildInviteEntry{ID: 0x01020304}
	gs.AnswerGuildInvite(false)
	gs.AnswerGuildInvite(false) // none open: nothing
	wantSent(t, *sent,
		sentMsg{opGuild, cat([]byte{9}, u32(0x01020304))},
		sentMsg{opGuild, cat([]byte{10}, u32(0x01020304), []byte{1})})
}

func TestEgressGroupInvitations(t *testing.T) {
	gs, sent := senderState()
	gs.PlayerObjectID = 100
	gs.GroupInvites = []GroupInviteEntry{{ID: 7, Name: "Ann"}, {ID: 8, Name: "Bo"}, {ID: 7, Name: "Ann"}}
	gs.GroupInviteID, gs.GroupInviter = 7, "Ann"

	// Decline: 0x0624 {7, 1}, every invitation from 7 dropped, 8 shown.
	gs.DeclineGroupInvite()
	wantSent(t, *sent, sentMsg{protocol.OpcodeDeclineGroupInvite, cat(u32(7), []byte{1})})
	if gs.GroupInviteID != 8 || len(gs.GroupInvites) != 1 {
		t.Fatalf("after the decline: shown %d, queue %+v", gs.GroupInviteID, gs.GroupInvites)
	}
	*sent = nil

	// Accept: 0x0622 {8}, then the rest declined with 2 (Reset) -- none left.
	gs.GroupInvites = append(gs.GroupInvites, GroupInviteEntry{ID: 9, Name: "Cy"})
	gs.AcceptGroupInvite()
	wantSent(t, *sent,
		sentMsg{protocol.OpcodeAcceptGroupInvite, u32(8)},
		sentMsg{protocol.OpcodeDeclineGroupInvite, cat(u32(9), []byte{2})})
	if !gs.GroupActive || gs.GroupIsLeader || len(gs.GroupInvites) != 0 || gs.GroupInviteID != 0 {
		t.Errorf("after the accept: active %v leader %v queue %+v shown %d",
			gs.GroupActive, gs.GroupIsLeader, gs.GroupInvites, gs.GroupInviteID)
	}
	*sent = nil

	// No invitation: 624, nothing sent.
	gs.AcceptGroupInvite()
	gs.DeclineGroupInvite()
	if len(*sent) != 0 {
		t.Errorf("answered no invitation: %v", *sent)
	}

	// A death declines every waiting invitation with 3 (Msg005E).
	gs.GroupInvites = []GroupInviteEntry{{ID: 5}, {ID: 6}}
	gs.handleCharacterDied(&protocol.Message{})
	wantSent(t, *sent,
		sentMsg{protocol.OpcodeDeclineGroupInvite, cat(u32(5), []byte{3})},
		sentMsg{protocol.OpcodeDeclineGroupInvite, cat(u32(6), []byte{3})})
}

func TestEgressInviteToGroup(t *testing.T) {
	gs, sent := senderState()
	gs.PlayerObjectID = 100
	gs.GroupInvites = []GroupInviteEntry{{ID: 9, Name: "Cy"}}

	// Oneself: 679, nothing.
	if gs.InviteToGroup(100, "Me") || len(*sent) != 0 {
		t.Fatalf("invited oneself: %v", *sent)
	}
	// A first invitation makes the group, sends 0x0625 {id, name8}, declines the waiting ones.
	if !gs.InviteToGroup(0x00102030, "Bob") {
		t.Fatal("not sent")
	}
	wantSent(t, *sent,
		sentMsg{protocol.OpcodeGroupInvite, cat(u32(0x00102030), u32(3), []byte("Bob"))},
		sentMsg{protocol.OpcodeDeclineGroupInvite, cat(u32(9), []byte{2})})
	if !gs.GroupActive || !gs.GroupIsLeader {
		t.Errorf("group not formed: active %v leader %v", gs.GroupActive, gs.GroupIsLeader)
	}
	*sent = nil

	// In a group, not leading it: 640.
	gs.GroupIsLeader = false
	if gs.InviteToGroup(0x55, "Cal") || len(*sent) != 0 {
		t.Errorf("a member invited: %v", *sent)
	}
	// Already a member: 1023.
	gs.GroupIsLeader = true
	gs.GroupMembers = []GroupMember{{ObjectID: 0x55, Name: "Cal"}}
	if gs.InviteToGroup(0x55, "Cal") || len(*sent) != 0 {
		t.Errorf("a member re-invited: %v", *sent)
	}
}

func TestEgressLeaveGroupDropsGroupChat(t *testing.T) {
	gs, sent := senderState()
	gs.Phase = PhaseInWorld
	gs.GroupActive = true
	gs.ChatMode = ChatModeGroup
	gs.LeaveGroup()
	wantSent(t, *sent,
		sentMsg{protocol.OpcodeLeaveGroup, nil},
		sentMsg{opMode, []byte{0}}) // GroupChanged(0)
	if gs.GroupActive || gs.ChatMode != ChatModeSay {
		t.Errorf("active %v mode %d", gs.GroupActive, gs.ChatMode)
	}
	*sent = nil
	gs.ChatMode = ChatModeShout
	gs.GroupActive = true
	gs.DisbandGroup()
	wantSent(t, *sent, sentMsg{protocol.OpcodeDisbandGroup, nil}) // shout is kept
}

func TestEgressFactionPages(t *testing.T) {
	gs, sent := senderState()
	gs.FactionPageID = 1
	gs.RequestFactions()
	gs.RequestFactions() // the menu calls it every frame; one request
	wantSent(t, *sent, sentMsg{protocol.OpcodeClientFaction, []byte{0x66, 4, 0}})
	*sent = nil

	// A page that is not the last asks for the next with the same token.
	page := func(token, n, of int32) *protocol.Message {
		return socialWire{}.u8(0x77).zz(token).zz(n).zz(of).zz(1).s16("Gypsies").zz(5).msg(0x07E2)
	}
	gs.handleFactionPage(page(2, 0, 3))
	gs.handleFactionPage(page(2, 1, 3))
	gs.handleFactionPage(page(2, 2, 3))
	wantSent(t, *sent,
		sentMsg{protocol.OpcodeClientFaction, []byte{0x66, 4, 2}},
		sentMsg{protocol.OpcodeClientFaction, []byte{0x66, 4, 4}})
	if !gs.FactionLoaded || len(gs.Factions) != 3 {
		t.Errorf("loaded %v, %d factions", gs.FactionLoaded, len(gs.Factions))
	}
}

// TestEgressAgainstThePS2 pins the payload builders to the bytes a PS2 sent.
func TestEgressAgainstThePS2(t *testing.T) {
	seen := map[uint16][][]byte{}
	for _, m := range egressAll(t) {
		if !m.IsState() {
			seen[m.Opcode] = append(seen[m.Opcode], m.Payload())
		}
	}
	has := func(op uint16, p []byte) {
		t.Helper()
		for _, q := range seen[op] {
			if bytes.Equal(p, q) {
				return
			}
		}
		t.Errorf("0x%04X % X: no PS2 message carries it (the PS2 sent %d)", op, p, len(seen[op]))
	}
	// ps2_main_capture, the merchant 0x00100BB9.
	has(0x004A, merchantBuyPayload(0, 1, 0x00100BB9))
	has(0x004B, merchantSellPayload(0x1F, 1, 0x00100BB9))
	has(0x003E, useItemPayload(0, 0x00100BB9, 0))
	has(0x003E, useItemPayload(3, 0x00100BB9, 0))
	// ps2_main_capture's faction window: token 2, page 0.
	gs, sent := senderState()
	gs.FactionPageID = 1
	gs.RequestFactions()
	has(0x0AC4, (*sent)[0].p)
	// SelectCharacter, ps2_sandstorm.pcap: server id 0x6257, face 0, hair style 1, length 2, colour 1.
	has(0x002A, selectCharPayload(CharInfo{ServerID: 0x6257, FaceOption: 0, HairStyle: 1, HairLength: 2, HairColor: 1}))
	// SendNewChar, local-warp.pcap: "Aaaaaaa", race 10, class 3, the rest 0.
	has(0x002B, newCharPayload("Aaaaaaa", 10, 3, 0, 0, 0, 0, 0, 0, [7]int{}))
	// "Pppppppp": human enchanter, hair 1/2/2, face 2, eastern.
	has(0x002B, newCharPayload("Pppppppp", 0, 12, 0, 1, 2, 2, 2, 1, [7]int{}))
	// DeleteCharacter: zigzag server id 1.
	has(0x002D, appendSigned(nil, 1))

	// The who token is zigzag: one byte up to 63, two from 64 (the old raw byte broke there).
	if p := whoPayload(1); !bytes.Equal(p, []byte{1, 2}) {
		t.Errorf("who 1: % X", p)
	}
	if p := whoPayload(64); !bytes.Equal(p, []byte{1, 0x80, 1}) {
		t.Errorf("who 64: % X", p)
	}
}
