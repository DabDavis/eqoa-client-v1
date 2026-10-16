package game

import (
	"encoding/binary"
	"fmt"
	"log"
	"strings"
	"unicode/utf16"

	"github.com/eqoa/client/internal/protocol"
)

// The messages a PS2 sends for chat, guild and group commands, ported from
// CLIENT's senders (decomp/modules/client/src/viclient.cpp, and its
// disassembly where a function is not written yet). Each names the CLIENT
// function it is. They go out through socialSend, so a test can take them.

// The chat modes, VIClient+0x382E4: what GroupChanged keeps and sends as
// 0x000E, named by words 595-599. Typed text goes out in the mode; a
// /command switches for one message and back (func_00BCFE78).
const (
	ChatModeSay     byte = 0
	ChatModeWhisper byte = 1
	ChatModeGroup   byte = 2
	ChatModeShout   byte = 3
	ChatModeGuild   byte = 4
)

// chatMaxUnits is func_00BCFE78's limit: a line of 128 UTF-16 units or more
// is not sent at all.
const chatMaxUnits = 128

// The 0x0578 guild commands (the VIClientPart2EF24 senders, 0x00BEEC08 ..
// 0x00BEF600). The numbers are 0x0579's: its failure for 1 is "Guild create
// failed:" (563) and for 2 "Guild disband failed:" (564).
const (
	guildCreate  byte = 1  // + wide name (func_00BEEC08)
	guildDisband byte = 2  // the "Disband the guild?" (543) prompt's (func_00BEECD8)
	guildLeave   byte = 3  // the "Leave the guild?" (542) prompt's (func_00BEED98)
	guildPromote byte = 4  // + zigzag member id: "Promote selected guild member?" (544)
	guildDemote  byte = 5  // + zigzag member id: "Demote selected guild member?" (545)
	guildRemove  byte = 6  // + zigzag member id: "Remove selected guild member?" (536)
	guildInvite  byte = 8  // + name8 + wide guild name (func_00BEF478)
	guildJoin    byte = 9  // + u32 inviter: the invitation taken (func_00BEF280)
	guildRefuse  byte = 10 // + u32 inviter + reason: 1 declined, 2 busy, 4 in a guild
	guildCharter byte = 11 // + wide text (func_00BEF0E0)
	guildChat    byte = 12 // + wide text (func_00BEF1B0)
)

// appendWide is VIMemStream::WriteString(const unsigned short *) (0x00CB8918):
// a u32 count of UTF-16 units, then the units.
func appendWide(b []byte, s string) []byte {
	u := utf16.Encode([]rune(s))
	b = binary.LittleEndian.AppendUint32(b, uint32(len(u)))
	for _, c := range u {
		b = binary.LittleEndian.AppendUint16(b, c)
	}
	return b
}

// appendNarrow is VIMemStream::WriteString(const char *) (0x00CB88A0): a u32
// length, then the bytes.
func appendNarrow(b []byte, s string) []byte {
	b = binary.LittleEndian.AppendUint32(b, uint32(len(s)))
	return append(b, s...)
}

// appendSigned is VIMemStream::WriteSigned (0x00CB85E0): zigzag, then seven
// bits a byte.
func appendSigned(b []byte, v int32) []byte {
	u := uint32(v) << 1
	if v < 0 {
		u = ^(uint32(v) << 1)
	}
	for {
		c := byte(u & 0x7F)
		u >>= 7
		if u != 0 {
			b = append(b, c|0x80)
			continue
		}
		return append(b, c)
	}
}

// chatUnits is strlen16 of the line as the PS2 would hold it.
func chatUnits(s string) int { return len(utf16.Encode([]rune(s))) }

// noTarget is m_target when nothing is targeted.
func noTarget(id uint32) uint32 {
	if id == 0 {
		return 0xFFFFFFFF
	}
	return id
}

// GroupChanged is VIClient::GroupChanged (0x00BD9B48, byte-exact): the mode
// is kept, sent as 0x000E, and named in the chat window ("Chat Mode: Group.").
func (gs *GameState) GroupChanged(mode byte) {
	gs.ChatMode = mode
	gs.socialSend(protocol.OpcodeChangeChatMode, []byte{mode})
	if mode <= ChatModeGuild {
		gs.socialShowWord(595 + int(mode))
	}
}

// sendSayChat is func_00BE6188(11, text, m_target): 0x000B, the wide line
// and the target. Sandstorm's server reads its commands out of it.
func (gs *GameState) sendSayChat(text string, target uint32) {
	gs.socialSend(protocol.OpcodeClientSayChat, binary.LittleEndian.AppendUint32(appendWide(nil, text), noTarget(target)))
}

// sendShoutChat is func_00CBBE10 (VIClientPart2EFCC): 0x0C01, 2, the line.
func (gs *GameState) sendShoutChat(text string) {
	gs.socialSend(protocol.OpcodeClientShout, appendWide([]byte{2}, text))
}

// sendGuildChat is func_00BEF1B0: 0x0578, 12, the line.
func (gs *GameState) sendGuildChat(text string) {
	gs.socialSend(protocol.GameOpcode(0x0578), appendWide([]byte{guildChat}, text))
}

// chatIn is func_00BCFE78's /say, /group, /guild and /shout: 0x000E for
// that mode, the message, and 0x000E back to the kept mode. The kept mode
// itself does not change (SendOpcodeByte, not GroupChanged).
func (gs *GameState) chatIn(mode byte, send func()) {
	gs.socialSend(protocol.OpcodeChangeChatMode, []byte{mode})
	send()
	gs.socialSend(protocol.OpcodeChangeChatMode, []byte{gs.ChatMode})
}

// chatTooLong is func_00BCFE78's length test.
func chatTooLong(s string) bool {
	if chatUnits(s) >= chatMaxUnits {
		log.Printf("chat: %d units, not sent (the PS2 sends under %d)", chatUnits(s), chatMaxUnits)
		return true
	}
	return false
}

// ChatSay is "/say": 0x000E 0, 0x000B, 0x000E back.
func (gs *GameState) ChatSay(text string, target uint32) {
	if chatTooLong(text) {
		return
	}
	gs.chatIn(ChatModeSay, func() { gs.sendSayChat(text, target) })
}

// ChatGroup is "/group": 0x000E 2, 0x000B, 0x000E back.
func (gs *GameState) ChatGroup(text string, target uint32) {
	if chatTooLong(text) {
		return
	}
	gs.chatIn(ChatModeGroup, func() { gs.sendSayChat(text, target) })
}

// ChatGuild is "/guild": 0x000E 4, 0x0578 {12, text}, 0x000E back.
func (gs *GameState) ChatGuild(text string) {
	if chatTooLong(text) {
		return
	}
	gs.chatIn(ChatModeGuild, func() { gs.sendGuildChat(text) })
}

// ChatShout is "/shout": 0x000E 3, 0x0C01 {2, text}, 0x000E back.
func (gs *GameState) ChatShout(text string) {
	if chatTooLong(text) {
		return
	}
	gs.chatIn(ChatModeShout, func() { gs.sendShoutChat(text) })
}

// ChatLine is typed text with no /command: it goes out in the kept mode,
// with no 0x000E (func_00BCFE78's tail at 0x00BD0474).
func (gs *GameState) ChatLine(text string, target uint32) {
	if chatTooLong(text) {
		return
	}
	switch gs.ChatMode {
	case ChatModeShout:
		gs.sendShoutChat(text)
	case ChatModeGuild:
		gs.sendGuildChat(text)
	default:
		gs.sendSayChat(text, target)
	}
}

// ChatCommand sends a whole /command line as 0x000B, slash included and in
// no mode: "/tell" and "/t" (the server reads the name out of the line),
// and, under Sandstorm's S2, any /command retail does not know.
func (gs *GameState) ChatCommand(line string, target uint32) {
	if chatTooLong(line) {
		return
	}
	gs.sendSayChat(line, target)
}

// ChatReply is "/reply" and "/r": "/t <the last teller> <text>" as 0x000B
// (snprintf16 "/t %S %S" with VIClient+0x4F0B8). With nobody to reply to
// the PS2 sends nothing; this reports false.
func (gs *GameState) ChatReply(text string, target uint32) bool {
	if gs.Session.ReplyName == "" {
		return false
	}
	gs.sendSayChat(fmt.Sprintf("/t %s %s", gs.Session.ReplyName, text), target)
	return true
}

// SetChatMode is the chat window's mode choice: GroupChanged in the world,
// where the server keeps the mode too; before it the mode is only kept, and
// the world entry's 0x000E (Msg000D) carries it.
func (gs *GameState) SetChatMode(mode byte) {
	if gs.Phase != PhaseInWorld {
		gs.ChatMode = mode
		return
	}
	gs.GroupChanged(mode)
}

// --- the guild --------------------------------------------------------------

func (gs *GameState) guildSend(payload []byte) {
	gs.socialSend(protocol.GameOpcode(0x0578), payload)
}

// GuildCreate is 0x0578 {1, name} (func_00BEEC08).
func (gs *GameState) GuildCreate(name string) { gs.guildSend(appendWide([]byte{guildCreate}, name)) }

// GuildDisband is 0x0578 {2} (func_00BEECD8).
func (gs *GameState) GuildDisband() { gs.guildSend([]byte{guildDisband}) }

// GuildLeave is 0x0578 {3} (func_00BEED98).
func (gs *GameState) GuildLeave() { gs.guildSend([]byte{guildLeave}) }

// GuildCharter is 0x0578 {11, text} (func_00BEF0E0), the guild's charter:
// the text 0x0468/1 carries after the name.
func (gs *GameState) GuildCharter(text string) { gs.guildSend(appendWide([]byte{guildCharter}, text)) }

// InviteToGuild is func_00BEF478: never oneself (word 679, in red), else
// 0x0578 {8, the name, the guild's name}.
func (gs *GameState) InviteToGuild(name string) {
	if strings.EqualFold(name, gs.Player.Name) {
		gs.addWordLine(gs.wordText(679), 255, 63, 63)
		return
	}
	gs.guildSend(appendWide(appendNarrow([]byte{guildInvite}, name), gs.GuildName))
}

// guildMember is the member a name stands for, the way the member window
// picks one (func_00BF0C58 hands the record's id to the sender).
func (gs *GameState) guildMember(name string) (GuildMember, bool) {
	for _, m := range gs.GuildMembers {
		if strings.EqualFold(m.Name, name) {
			return m, true
		}
	}
	return GuildMember{}, false
}

// guildMemberCommand is func_00BEEE58 / BEEF30 / BEF008: the sub-command and
// the member's id, zigzag.
func (gs *GameState) guildMemberCommand(sub byte, name string) bool {
	m, ok := gs.guildMember(name)
	if !ok {
		log.Printf("guild: no member %q", name)
		return false
	}
	gs.guildSend(appendSigned([]byte{sub}, int32(m.CharID)))
	return true
}

// GuildPromote, GuildDemote and GuildRemove are the member window's three
// commands, 0x0578 {4|5|6, id}. They report false for a name not in the
// member list, where nothing is sent.
func (gs *GameState) GuildPromote(name string) bool { return gs.guildMemberCommand(guildPromote, name) }
func (gs *GameState) GuildDemote(name string) bool  { return gs.guildMemberCommand(guildDemote, name) }
func (gs *GameState) GuildRemove(name string) bool  { return gs.guildMemberCommand(guildRemove, name) }

// AnswerGuildInvite answers the open invitation: taken, 0x0578 {9, inviter}
// (func_00BEF280); declined, {10, inviter, 1} (func_00BEF370), which the
// inviter sees as "%s has declined your invitation." (558).
func (gs *GameState) AnswerGuildInvite(accept bool) {
	inv := gs.GuildInvite
	if inv == nil {
		return
	}
	p := []byte{guildJoin}
	if !accept {
		p = []byte{guildRefuse}
	}
	p = binary.LittleEndian.AppendUint32(p, inv.ID)
	if !accept {
		p = append(p, 1)
	}
	gs.guildSend(p)
	gs.GuildInvite = nil
}

// --- the group --------------------------------------------------------------

// InviteToGroup is VIClient::InviteToGroup (0x00BD8128, byte-exact): not
// oneself (679), only as the leader of a group (640), not someone in it
// already (1023); then "You give a group invitation to %s." (637), a first
// invitation makes the group (in it, leading), 0x0625 {id, name}, and every
// invitation waiting here is declined (Reset). It reports whether it sent.
func (gs *GameState) InviteToGroup(id uint32, name string) bool {
	if id == gs.PlayerObjectID && id != 0 {
		gs.socialShowWord(679)
		return false
	}
	if gs.GroupActive && !gs.GroupIsLeader {
		gs.socialShowWord(640)
		return false
	}
	for _, m := range gs.GroupMembers {
		if m.ObjectID == id {
			gs.socialShowText(wordFormat(gs.wordText(1023), name))
			return false
		}
	}
	if !gs.GroupActive {
		gs.GroupIsLeader = true
		gs.GroupActive = true
	}
	gs.socialShowText(wordFormat(gs.wordText(637), name))
	gs.socialSend(protocol.OpcodeGroupInvite, appendNarrow(binary.LittleEndian.AppendUint32(nil, id), name))
	gs.declineInvites(2)
	return true
}

// declineInvites is VIClient::Reset (0x00BD3A60), and Msg005E's loop: every
// waiting invitation declined, 0x0624 {id, reason}, and dropped. A death
// declines with 3, an invitation of our own or an accepted one with 2.
func (gs *GameState) declineInvites(reason byte) {
	for _, inv := range gs.GroupInvites {
		gs.socialSendMsg(protocol.OpcodeDeclineGroupInvite, inv.ID, reason)
	}
	gs.GroupInvites = nil
	gs.GroupInviteID, gs.GroupInviter = 0, ""
}

// dropInvites is VIClient::DropInvites (0x00BD7B30): every invitation from
// id out of the queue; the first left is mirrored.
func (gs *GameState) dropInvites(id uint32) {
	kept := gs.GroupInvites[:0]
	for _, inv := range gs.GroupInvites {
		if inv.ID != id {
			kept = append(kept, inv)
		}
	}
	gs.GroupInvites = kept
	gs.GroupInviteID, gs.GroupInviter = 0, ""
	if len(kept) > 0 {
		gs.GroupInviteID, gs.GroupInviter = kept[0].ID, kept[0].Name
	}
}

// firstInvite is m_p2BD58.Begin(): the oldest invitation, the one the
// accept and decline answer. The id the menu shows is the same one.
func (gs *GameState) firstInvite() (GroupInviteEntry, bool) {
	if len(gs.GroupInvites) == 0 {
		return GroupInviteEntry{}, false
	}
	return gs.GroupInvites[0], true
}

// AcceptGroupInvite is VIClient::AcceptInvite (0x00BD7BE8, byte-exact): with
// no invitation, "You have no invitations." (624); else "You accept the
// invitation from %s." (622), 0x0622 {id}, in a group but not leading it,
// and the rest declined (Reset).
func (gs *GameState) AcceptGroupInvite() {
	inv, ok := gs.firstInvite()
	if !ok {
		gs.socialShowWord(624)
		return
	}
	gs.socialShowText(wordFormat(gs.wordText(622), inv.Name))
	gs.dropInvites(inv.ID)
	gs.socialSend(protocol.OpcodeAcceptGroupInvite, binary.LittleEndian.AppendUint32(nil, inv.ID))
	gs.GroupActive = true
	gs.GroupIsLeader = false
	gs.declineInvites(2)
}

// DeclineGroupInvite is VIClient::DeclineInvite (0x00BD7D00, byte-exact):
// "You decline the invitation from %s." (623), 0x0624 {id, 1}.
func (gs *GameState) DeclineGroupInvite() {
	inv, ok := gs.firstInvite()
	if !ok {
		gs.socialShowWord(624)
		return
	}
	gs.socialShowText(wordFormat(gs.wordText(623), inv.Name))
	gs.dropInvites(inv.ID)
	gs.socialSendMsg(protocol.OpcodeDeclineGroupInvite, inv.ID, 1)
}

// LeaveGroup is VIClient::LeaveGroup (0x00BD7E00, byte-exact): 0x0623 with
// nothing, out of the group, and a group chat mode back to say.
func (gs *GameState) LeaveGroup() {
	gs.socialSend(protocol.OpcodeLeaveGroup, nil)
	gs.socialLeaveGroup()
}

// DisbandGroup is VIClient::DisbandGroup (0x00BD8368, byte-exact): the same
// with 0x0627.
func (gs *GameState) DisbandGroup() {
	gs.socialSend(protocol.OpcodeDisbandGroup, nil)
	gs.socialLeaveGroup()
}

// --- the payloads of the engine's item and list senders --------------------

// merchantBuyPayload is SendOpSigned2Int(0x004A, slot, n, merchant): two
// zigzags and a raw word (ps2_main_capture: 00 02 B9 0B 10 00).
func merchantBuyPayload(slot, n int, merchant uint32) []byte {
	return binary.LittleEndian.AppendUint32(appendSigned(appendSigned(nil, int32(slot)), int32(n)), merchant)
}

// merchantSellPayload is SendOpIntSignedInt(0x004B, key, n, merchant): a raw
// word, a zigzag, a raw word (ps2_main_capture: 1F 00 00 00 02 B9 0B 10 00).
func merchantSellPayload(key, n int, merchant uint32) []byte {
	return binary.LittleEndian.AppendUint32(appendSigned(binary.LittleEndian.AppendUint32(nil, uint32(key)), int32(n)), merchant)
}

// useItemPayload is SendOpIntIntByte(0x003E, key, target, b).
func useItemPayload(key, target uint32, b byte) []byte {
	return append(binary.LittleEndian.AppendUint32(binary.LittleEndian.AppendUint32(nil, key), target), b)
}

// whoPayload is 0x0E01's body: 1, then the token zigzag.
func whoPayload(token int32) []byte { return appendSigned([]byte{1}, token) }
