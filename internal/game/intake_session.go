package game

// intake_session.go — the session, login and world-entry opcodes, read the way
// CLIENT reads them: decomp/modules/client/src/viclient.cpp, whose dispatcher
// (VIClient::ProcessMessage, 0x00BD0DC8) maps each opcode to the code below.
// The opcode map is claude-notes/decomp-phase62-dispatcher-survey-20261015.md §2.

import (
	"encoding/binary"
	"fmt"
	"log"
	"unicode/utf16"

	"github.com/DabDavis/eqoa-esf-tools/pkg/buffer"
	"github.com/eqoa/client/internal/protocol"
)

// SessionIntake is the part of VIClient the session opcodes write.
type SessionIntake struct {
	// Disconnect is a server-ordered VIClient::Disconnect(reason, quiet).
	// The visual client sees Pending, closes the connection and goes back to
	// the login screen showing Text. A quiet one (0x0777) shows nothing.
	Disconnect DisconnectState

	// CharSelectMessage is the word VIClient::CharSelectMsg puts in the login
	// window (0x07F0..0x07F4). Selecting a character clears it.
	CharSelectMessage string

	// ExpansionFlags is VIClient+0x50304, the u32 0x07D1 carries (the old
	// "Camera1"). Masked with the disc's own flags (VIGetExpansionFlags) it
	// picks the race camera (GetRaceCamera), and bit 1 makes GetFrameRate 60.
	// Every server sends -1.
	ExpansionFlags uint32

	// P07578 is VIClient+0x7578, the u32 0x07B2 carries: the 0x40's
	// interval in ms. VIClient::Process writes the 0x40 once it is that long
	// since the last (0x00BC7F54, an `lwu`); the constructor sets 100.
	P07578 uint32

	// ReplyName is VIClient+0x4F0B8 (GetReplyName), the name 0x07A3 carries.
	ReplyName string

	// Probe is what 0x0790 sets (VIClient::Msg0790, +0x4F0F0..+0x4F108).
	Probe ProbeState

	// NoCollide is VIClientPart38300 (+0x38300), the names 0x1003..0x1005
	// add and remove. RefreshCollision makes a creature whose name is listed
	// non-collideable (SetActorCollideable(actor, Find(name) == -1)). The
	// list a pet arrives on carries the pet: "Elder Familiar" in
	// ps2_sandstorm.pcap.
	NoCollide []NoCollideName

	// SpeedSet is true once a 0x00F8 has set the walk control's speeds.
	// Before that the walk control runs at its InitParms defaults (run
	// 8.83333, walk 3.0); after it, a speed of 0 stops the player.
	SpeedSet bool

	// WorldPathGate is VIClient+0x662C, the MemoryDump's first byte (b != 0).
	// Set, the world loads from the path string (+0xC1DC); clear, from the
	// world table by index (claude-notes/memcard-world-loading.md). Every
	// server sends 0, and the Go client loads by index.
	WorldPathGate bool

	// AdminLevel is VIClientPart382E8+0x14, which 0x0700 sub-type 35 sets.
	AdminLevel int32
}

// DisconnectState is VIClient::Disconnect's record of why.
type DisconnectState struct {
	Pending bool
	Reason  int    // VIClient+0x4F0E8
	Quiet   bool   // Disconnect(reason, 1): no notice
	Text    string // ConnectStatusString()
	// Code is VIClient+0x4F0EC, the byte a DRDP 0xFD carried; HasCode false
	// is CLIENT's -1.
	Code    int
	HasCode bool
}

// ProbeState is VIClient::Msg0790's: an endpoint id (+0x4F0F0, which IS an
// address: IP, UDP port, DRDP port, high to low), a token (+0x4F0F8) and a
// value (+0x4F100). Every net tick Msg0790Done sends 0x0992 {token, value}
// to that address, at most once per min(connection timeout, 1,000 ms), and a
// connectionless 0x0993 carrying the token is answered with 0x0791 {token}
// (ProcessMessageF8). The Go client does not send either.
type ProbeState struct {
	Set   bool
	Addr  uint64
	Token uint32
	Value uint64
}

// NoCollideName is one VIRec36_BFBCB8: name[32] and an id at +0x20.
type NoCollideName struct {
	Name string
	ID   uint32
}

// ---------------------------------------------------------------- stream --

// psString is VIMemStream::ReadString(char *, MaxBytes) (0x00CB8740): a u32
// length, then the bytes if they fit with their terminator. A longer string
// is skipped and reads as "". C stops at the first NUL, so does this.
func psString(r *buffer.Reader, maxBytes int) string {
	n, err := r.ReadUint32()
	if err != nil {
		return ""
	}
	if uint64(n)+1 > uint64(maxBytes) {
		r.Advance(int(n))
		return ""
	}
	b, err := r.ReadBytes(int(n))
	if err != nil {
		return ""
	}
	for i, c := range b {
		if c == 0 {
			b = b[:i]
			break
		}
	}
	return string(b)
}

// psWString is VIMemStream::ReadString(unsigned short *, MaxBytes)
// (0x00CB87E8): the length counts characters and must fit in MaxBytes/2 with
// the terminator; a longer string is skipped (2n bytes) and reads as "".
func psWString(r *buffer.Reader, maxBytes int) string {
	n, err := r.ReadUint32()
	if err != nil {
		return ""
	}
	if uint64(n)+1 > uint64(maxBytes/2) {
		r.Advance(int(n) * 2)
		return ""
	}
	b, err := r.ReadBytes(int(n) * 2)
	if err != nil {
		return ""
	}
	u := make([]uint16, 0, n)
	for i := 0; i+1 < len(b); i += 2 {
		c := binary.LittleEndian.Uint16(b[i:])
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return string(utf16.Decode(u))
}

// psSigned is VIMemStream::ReadSigned (0x00CB8610): a zigzag varint, kept to
// the 32 bits the client stores.
func psSigned(r *buffer.Reader) (int32, error) {
	v, err := r.Read7BitEncodedInt64()
	return int32(v), err
}

// ----------------------------------------------------------------- words --

// ------------------------------------------------------------ disconnect --

// disconnectReasons are the opcodes VIClient::ProcessMessage answers with
// Disconnect(reason, 0) (0x07A1 reads 8 bytes first; 0x0000 does it only for a
// version other than 18, reason 16). The printf each one makes is beside it.
var disconnectReasons = map[uint16]int{
	0x0729: 3,  // "Disconnect-5"
	0x072B: 4,  // "Disconnect-6"
	0x0728: 6,  // "Disconnect-7"
	0x072A: 7,  // "Disconnect-8"
	0x00EC: 8,  // "Disconnect-9"
	0x07A4: 8,  // "Disconnect-9"
	0x07A1: 13, // Msg07A1
	0x07A2: 14, // Msg07A2
	0x07C0: 17,
	0x07C1: 18,
	0x07D2: 19,
}

// connectStatusWords is ConnectStatusString's jump table (0x00BC2268, table
// 0x00CDA620): the word a disconnect reason shows. Reasons 0 and 10 add the
// 0xFD code when there is one.
var connectStatusWords = [22]int{
	272, 666, 431, 270, 593, 594, 620, 621, 619, 820, 667,
	1017, 821, 677, 716, 719, 1024, 718, 718, 1420, 758, 822,
}

// ConnectStatusString is CLIENT's 0x00BC2268 for a reason and a 0xFD code.
func (gs *GameState) ConnectStatusString(reason int, code int, hasCode bool) string {
	if reason < 0 || reason >= len(connectStatusWords) {
		return "" // "ConnectStatusString() : error"
	}
	w := gs.word(connectStatusWords[reason])
	if hasCode {
		switch reason {
		case 0:
			return fmt.Sprintf("%s (%s: %d)", w, gs.word(819), code)
		case 10:
			return fmt.Sprintf("%s (%d)", w, code)
		}
	}
	return w
}

// DisconnectKind is VIClient::DisconnectKind (0x00BC24C8), what the notice
// does once dismissed (Msg0777Done): 2 reload the world and show the login
// window, 3 leave the client (exit code 0), 4 leave it with -1 and the
// network down, 0 nothing. The Go client goes back to its login screen for
// 2, 3 and 4 alike.
func DisconnectKind(reason int) int {
	switch reason {
	case 3, 4, 8, 9:
		return 3
	case 0, 1, 2, 6, 7, 10, 13, 15, 17, 19:
		return 2
	case 5, 11, 12, 14, 16, 18:
		return 4
	}
	return 0
}

// serverDisconnect is VIClient::Disconnect(reason, quiet) ordered by the
// server. CLIENT stores the reason, drops every entity, closes the connection
// and destroys DRDP, so nothing after it in the packet is read; Notify then
// shows ConnectStatusString unless quiet.
func (gs *GameState) serverDisconnect(reason int, quiet bool) {
	d := &gs.Session.Disconnect
	d.Pending = true
	d.Reason = reason
	d.Quiet = quiet
	d.Text = ""
	if !quiet {
		d.Text = gs.ConnectStatusString(reason, d.Code, d.HasCode)
	}
	gs.dropEntities()
	gs.Phase = PhaseDisconnected
	gs.StatusMsg = d.Text
	log.Printf("*** SERVER DISCONNECT: reason=%d quiet=%v kind=%d %q ***",
		reason, quiet, DisconnectKind(reason), d.Text)
}

// DisconnectFromDRDP is the receive loop's 0xFD arm (VIClient::PumpMessages):
// the code byte is kept (+0x4F0EC) and picks the reason, printf
// "Disconnect-11".
func (gs *GameState) DisconnectFromDRDP(code byte) {
	gs.Session.Disconnect.Code = int(code)
	gs.Session.Disconnect.HasCode = true
	reason := 10
	switch code {
	case 1:
		reason = 1
	case 14:
		reason = 16
	case 15:
		reason = 15
	}
	gs.serverDisconnect(reason, false)
}

// guaranteed is whether a message is one the PS2's dispatcher sees at all:
// VIClient::PumpMessages hands ProcessMessage DRDP type 0xFB only (a
// best-effort 0xFC goes to ProcessMessageFC, which knows 0x07B3 alone).
// The drastic session opcodes check it, so a best-effort message that
// happens to start with one of their numbers cannot end the session.
func guaranteed(msg *protocol.Message) bool {
	return msg.Type == protocol.MessageTypeReliable || msg.Type == protocol.MessageTypeSegmentReliable
}

// handleServerDisconnect is every opcode in disconnectReasons: CLIENT reads
// nothing but 0x07A1's eight bytes, and none of it is used.
func (gs *GameState) handleServerDisconnect(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 2 || !guaranteed(msg) {
		return
	}
	op := uint16(data[0]) | uint16(data[1])<<8
	reason, ok := disconnectReasons[op]
	if !ok {
		return
	}
	gs.serverDisconnect(reason, false)
}

// ------------------------------------------------------- character select --

// charSelectMsg is VIClient::CharSelectMsg: back to the character list (state
// 3, the create window hidden, the login window active) with a word in it.
// 0x07F2/0x07F4 show 1662, 0x07F0/0x07F1/0x07F3 676.
func (gs *GameState) charSelectMsg(word int) {
	gs.Phase = PhaseCharSelect
	gs.Session.CharSelectMessage = gs.word(word)
	gs.StatusMsg = gs.Session.CharSelectMessage
	log.Printf("CharSelectMsg(%d): %q", word, gs.Session.CharSelectMessage)
}

// handleCharSelectMsg is 0x07F0..0x07F4.
func (gs *GameState) handleCharSelectMsg(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 2 || !guaranteed(msg) {
		return
	}
	switch uint16(data[0]) | uint16(data[1])<<8 {
	case 0x07F2, 0x07F4:
		gs.charSelectMsg(1662)
	case 0x07F0, 0x07F1, 0x07F3:
		gs.charSelectMsg(676)
	}
}

// createFailed is 0x002E (word 269), 0x0065 (828) and 0x002F (829): the
// login window shows the word and m_charCount comes down by one. The Go client
// counts characters off the list the server sends, so only the text is kept.
func (gs *GameState) createFailed(word int) {
	gs.CreateError = gs.word(word)
	log.Printf("Character create failed (word %d): %q", word, gs.CreateError)
}

// handleNameTaken is 0x002F.
func (gs *GameState) handleNameTaken(msg *protocol.Message) {
	gs.createFailed(829)
}

// --------------------------------------------------------- no-collide list --

// handleNoCollide is 0x1003..0x1005, VIClientPart38300::Recv (0x00BFA850),
// then Msg1003Done: every entity's slot-9 virtual runs and every creature but
// the player is flagged (+0x2B8) for RefreshCollision. The Go client has no
// collision between entities, so only the list is kept.
//
//	0x1003  ReadString(name, 32), u32 id: add
//	0x1004  u32 id: erase the first entry with it
//	0x1005  zigzag count, then count × (name, id): add, WITHOUT clearing —
//	        the list is cleared by ResetWorld (a 0xFF new connection)
func (gs *GameState) handleNoCollide(msg *protocol.Message) {
	data := msg.Data()
	if len(data) < 2 {
		return
	}
	op := uint16(data[0]) | uint16(data[1])<<8
	r := buffer.NewReader(data[2:])
	switch op {
	case 0x1003:
		name := psString(r, 32)
		id, err := r.ReadUint32()
		if err != nil {
			return
		}
		gs.Session.NoCollide = append(gs.Session.NoCollide, NoCollideName{Name: name, ID: id})
		log.Printf("NoCollide: + %q id=0x%08X", name, id)
	case 0x1004:
		id, err := r.ReadUint32()
		if err != nil {
			return
		}
		for i, e := range gs.Session.NoCollide {
			if e.ID == id {
				gs.Session.NoCollide = append(gs.Session.NoCollide[:i], gs.Session.NoCollide[i+1:]...)
				log.Printf("NoCollide: - %q id=0x%08X", e.Name, id)
				break
			}
		}
	case 0x1005:
		n, err := psSigned(r)
		if err != nil {
			return
		}
		for i := int32(0); i < n; i++ {
			name := psString(r, 32)
			id, err := r.ReadUint32()
			if err != nil {
				return
			}
			gs.Session.NoCollide = append(gs.Session.NoCollide, NoCollideName{Name: name, ID: id})
		}
		log.Printf("NoCollide: list of %d (now %d)", n, len(gs.Session.NoCollide))
	}
}

// IsNoCollide is VIClientPart38300::Find(name) != -1.
func (gs *GameState) IsNoCollide(name string) bool {
	for _, e := range gs.Session.NoCollide {
		if e.Name == name {
			return true
		}
	}
	return false
}

// ------------------------------------------------------------ MemoryDump --

// skipItemBody consumes func_00CBACE8, the item body every item row ends in:
// 23 zigzag, the name and the description (wide), and the stat list (zigzag
// count, then two zigzag each, func_00CBB590 / func_00CBAFB8).
func skipItemBody(r *buffer.Reader) error {
	for i := 0; i < 23; i++ {
		if _, err := r.Read7BitEncodedInt64(); err != nil {
			return err
		}
	}
	for i := 0; i < 2; i++ {
		n, err := r.ReadUint32()
		if err != nil {
			return err
		}
		if err := r.Advance(int(n) * 2); err != nil {
			return err
		}
	}
	n, err := psSigned(r)
	if err != nil {
		return err
	}
	for i := int32(0); i < n*2; i++ {
		if _, err := r.Read7BitEncodedInt64(); err != nil {
			return err
		}
	}
	return nil
}

// skipBankListings consumes func_00BBCD10, the list the bank control reads
// after its items (VIBankControl +0x7770): zigzag count, then rows of seven
// zigzag, ReadString(name, 24) and an item body (func_00CBB150, 800 bytes in
// memory). The row is the auction listing's shape. Every captured dump sends
// an empty one. It returns the count.
func skipBankListings(r *buffer.Reader) (int32, error) {
	n, err := psSigned(r)
	if err != nil {
		return 0, err
	}
	for i := int32(0); i < n; i++ {
		for j := 0; j < 7; j++ {
			if _, err := r.Read7BitEncodedInt64(); err != nil {
				return n, err
			}
		}
		psString(r, 24)
		if err := skipItemBody(r); err != nil {
			return n, err
		}
	}
	return n, nil
}

// --------------------------------------------------------------- weather --

// weatherFromWire is VIClient::SetWeather (0x00BD9F00): wire bytes 0..6 are
// the WXTYPE of the same number and anything else is clear (the old code
// ignored it and kept the weather).
func weatherFromWire(b byte) WeatherType {
	if t := WeatherType(b); t.valid() {
		return t
	}
	return WeatherClear
}
