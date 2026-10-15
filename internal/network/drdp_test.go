package network

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/eqoa/client/internal/protocol"
)

// These pin the receive and send halves of the connection to the PS2's DRDP
// library as decompiled in decomp/src/connection.cpp: in-order release of
// guaranteed messages (connection_move_delayed_to_arrivals), the state
// channel rules of connection_parse_segment_body and
// connection_record_state_message, connection_record_guaranteed_ack, the
// RTO, and connection_append's size budget.

const testServerEP = 0x1111

func newTestConn(t *testing.T) *Connection {
	t.Helper()
	c, err := NewConnection("127.0.0.1", 9, 0xFFFE)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func seg(arrival uint16, msgs ...*protocol.Message) *ReceivedPacket {
	body := protocol.NewSegmentBody()
	body.ArrivalNumber = arrival
	body.Messages = msgs
	return &ReceivedPacket{
		LocalEndpoint: testServerEP,
		Header:        &protocol.SegmentHeader{},
		Body:          body,
	}
}

func gmsg(typ protocol.MessageType, seq uint16, data []byte) *protocol.Message {
	m := &protocol.Message{Type: typ, Sequence: seq}
	if typ != protocol.MessageTypePing && len(data) >= 2 {
		m.Opcode = protocol.GameOpcode(binary.LittleEndian.Uint16(data))
	}
	m.SetData(data)
	return m
}

func smsg(ch byte, seq uint16, ref byte, data []byte) *protocol.Message {
	m := &protocol.Message{Type: protocol.MessageType(ch), Sequence: seq, XORByte: ref}
	m.SetData(data)
	return m
}

func seqs(msgs []*protocol.Message) []uint16 {
	var out []uint16
	for _, m := range msgs {
		out = append(out, m.Sequence)
	}
	return out
}

func TestGuaranteedReleasedInSequence(t *testing.T) {
	c := newTestConn(t)
	op := []byte{0x13, 0x00, 1}
	if got := seqs(c.ProcessIncoming(seg(1, gmsg(protocol.MessageTypeReliable, 1, op)))); fmt.Sprint(got) != "[1]" {
		t.Fatalf("seq 1: %v", got)
	}
	if got := c.ProcessIncoming(seg(2, gmsg(protocol.MessageTypeReliable, 3, op))); len(got) != 0 {
		t.Fatalf("seq 3 overtook 2 and was delivered: %v", seqs(got))
	}
	if m := GuaranteedAckMask(c.delayed, c.recvReliable); m != 1 || c.recvReliable != 1 {
		t.Fatalf("holding 3: rx %d mask %b, want rx 1 mask 1", c.recvReliable, m)
	}
	if got := seqs(c.ProcessIncoming(seg(3, gmsg(protocol.MessageTypeReliable, 2, op)))); fmt.Sprint(got) != "[2 3]" {
		t.Fatalf("seq 2 should release 2 then 3: %v", got)
	}
	if got := c.ProcessIncoming(seg(4, gmsg(protocol.MessageTypeReliable, 2, op))); len(got) != 0 {
		t.Fatalf("a retransmitted 2 was delivered again")
	}
}

// Sequence 0 follows 65535 like any other. The old tracker skipped seq 0
// entirely, so after the wrap recvReliable stuck at 65535 and every later
// message was held for ever -- the session died 5-28 hours in.
func TestGuaranteedAcrossTheWrap(t *testing.T) {
	c := newTestConn(t)
	c.recvReliable = 65534
	op := []byte{0x13, 0x00}
	got := c.ProcessIncoming(seg(1, gmsg(protocol.MessageTypeReliable, 0, op)))
	if len(got) != 0 {
		t.Fatalf("0 delivered before 65535")
	}
	if m := GuaranteedAckMask(c.delayed, c.recvReliable); m != 1 {
		t.Fatalf("mask across the wrap %b, want 1 (0 = 65534+2)", m)
	}
	got = c.ProcessIncoming(seg(2, gmsg(protocol.MessageTypeReliable, 65535, op), gmsg(protocol.MessageTypeReliable, 1, op)))
	if fmt.Sprint(seqs(got)) != "[65535 0 1]" || c.recvReliable != 1 {
		t.Fatalf("got %v rx %d, want [65535 0 1] rx 1", seqs(got), c.recvReliable)
	}
}

// fragmentStream is pcaps/ps2_main_capture.pcap's world entry as the server
// sent it, piece for piece (seqs, types and sizes; the bytes are synthetic):
// a MemoryDump in 22 0xFA pieces and a final 0xFB (seqs 3..25), then the
// retransmissions of 11..18 the PS2 asked for, with a 0xF9 keepalive and a
// 0xFB 0x0013 (26, 27) overtaking 14..18 in one datagram.
func fragmentStream() (segs []*ReceivedPacket, dump []byte) {
	piece := func(seq uint16, n int) []byte {
		b := make([]byte, n)
		for i := range b {
			b[i] = byte(int(seq)*7 + i)
		}
		return b
	}
	pieces := map[uint16][]byte{}
	for s := uint16(3); s <= 25; s++ {
		n := 1156
		if s == 25 {
			n = 653
		}
		pieces[s] = piece(s, n)
		if s == 3 {
			pieces[s][0], pieces[s][1] = 0x0D, 0x00
		}
		dump = append(dump, pieces[s]...)
	}
	arr := uint16(1)
	add := func(msgs ...*protocol.Message) {
		segs = append(segs, seg(arr, msgs...))
		arr++
	}
	frag := func(s uint16) *protocol.Message {
		typ := protocol.MessageTypeSegmentReliable
		if s == 25 {
			typ = protocol.MessageTypeReliable
		}
		return gmsg(typ, s, pieces[s])
	}
	add(gmsg(protocol.MessageTypeReliable, 1, []byte{0xD1, 0x07, 0xFF, 0xFF, 0xFF, 0xFF}),
		gmsg(protocol.MessageTypeReliable, 2, append([]byte{0x13, 0x00}, piece(2, 16)...)))
	for s := uint16(3); s <= 25; s++ { // arrivals 2..24; 11..18 are arrivals 10..17
		add(frag(s))
	}
	for s := uint16(11); s <= 13; s++ {
		add(frag(s))
	}
	add(gmsg(protocol.MessageTypePing, 26, []byte{0x12}),
		gmsg(protocol.MessageTypeReliable, 27, append([]byte{0x13, 0x00}, piece(27, 16)...)))
	for s := uint16(14); s <= 18; s++ {
		add(frag(s))
	}
	return segs, dump
}

func TestFragmentedMemoryDumpSurvivesLoss(t *testing.T) {
	segs, dump := fragmentStream()
	run := func(lose func(arr uint16) bool) []*protocol.Message {
		c := newTestConn(t)
		var out []*protocol.Message
		for _, s := range segs {
			if lose(s.Body.ArrivalNumber) {
				continue
			}
			out = append(out, c.ProcessIncoming(s)...)
		}
		if len(c.delayed) != 0 || len(c.arrivals) != 0 || c.recvReliable != 27 {
			t.Fatalf("left holding %d delayed + %d pieces, rx %d", len(c.delayed), len(c.arrivals), c.recvReliable)
		}
		return out
	}
	clean := run(func(uint16) bool { return false })
	lossy := run(func(arr uint16) bool { return arr >= 10 && arr <= 17 }) // the PS2 lost 11..18
	for name, got := range map[string][]*protocol.Message{"clean": clean, "lossy": lossy} {
		if fmt.Sprint(seqs(got)) != "[1 2 3 26 27]" {
			t.Fatalf("%s: delivered seqs %v, want [1 2 3 26 27]", name, seqs(got))
		}
		md := got[2]
		if md.Type != protocol.MessageTypeReliable || md.Opcode != 0x000D || !bytes.Equal(md.Data(), dump) {
			t.Fatalf("%s: MemoryDump type 0x%02X opcode 0x%04X, %d bytes (want %d)", name, byte(md.Type), uint16(md.Opcode), md.Size(), len(dump))
		}
		if got[3].Type != protocol.MessageTypePing || got[4].Opcode != 0x0013 {
			t.Fatalf("%s: 26/27 came out as 0x%02X / 0x%04X", name, byte(got[3].Type), uint16(got[4].Opcode))
		}
	}
}

func TestStateStaleDroppedAckNeverBackwards(t *testing.T) {
	c := newTestConn(t)
	c.handshakeComplete = true
	if got := c.ProcessIncoming(seg(1, smsg(5, 7, 0, []byte{1, 2, 3}))); len(got) != 1 {
		t.Fatalf("keyframe not delivered")
	}
	// An older datagram arriving late, and a retransmission of 7.
	if got := c.ProcessIncoming(seg(2, smsg(5, 6, 0, []byte{9, 9, 9}), smsg(5, 7, 0, []byte{1, 2, 3}))); len(got) != 0 {
		t.Fatalf("stale state delivered: %v", seqs(got))
	}
	if c.stateRx[5] != 7 || c.stateRxFlag[5] != 2 {
		t.Fatalf("rx %d flag %d, want 7 and 2", c.stateRx[5], c.stateRxFlag[5])
	}
	pkt, _ := BuildPacket(c, nil)
	if !bytes.Contains(pkt, []byte{5, 7, 0, 0xF8}) {
		t.Fatalf("state ack (5, 7) not in % x", pkt)
	}
}

// "refnum does not exist" fails the whole segment: its arrival is not
// recorded and nothing in it -- not even a guaranteed message -- is taken.
func TestStateMissingReferenceDropsTheSegment(t *testing.T) {
	c := newTestConn(t)
	c.ProcessIncoming(seg(1, smsg(3, 1, 0, []byte{1, 2, 3, 4})))
	got := c.ProcessIncoming(seg(2,
		gmsg(protocol.MessageTypeReliable, 1, []byte{0x13, 0x00}),
		smsg(3, 5, 2, []byte{0, 0, 0, 1}))) // references 3, never seen
	if len(got) != 0 || c.recvArrival != 1 || c.recvReliable != 0 || c.stateRx[3] != 1 {
		t.Fatalf("segment partly applied: %d msgs, arrival %d, rx %d, ch3 rx %d", len(got), c.recvArrival, c.recvReliable, c.stateRx[3])
	}
	got = c.ProcessIncoming(seg(2, smsg(3, 2, 1, []byte{0, 0, 0, 1})))
	if len(got) != 1 || !bytes.Equal(got[0].Data(), []byte{1, 2, 3, 5}) {
		t.Fatalf("the retransmitted arrival 2 did not decode against 1")
	}
}

// The bases kept are the delta's reference onward (or the last 32 after a
// keyframe), and an entity leaving its channel forgets nothing: the next
// entity's first state is a delta against the last one acked.
func TestStateBasesOutliveTheEntity(t *testing.T) {
	c := newTestConn(t)
	c.ProcessIncoming(seg(1, smsg(9, 1, 0, []byte{0x10, 0xAA, 0xBB, 0, 0})))
	c.ProcessIncoming(seg(2, smsg(9, 2, 1, []byte{0x01, 0, 0, 0, 0})))
	c.ProcessIncoming(seg(3, smsg(9, 3, 2, []byte{0x02, 0xAA, 0xBB, 0, 0}))) // against 1: objectID 0
	if _, ok := c.stateBases[9][1]; !ok {
		t.Fatalf("base 1, the reference of 3, was pruned")
	}
	c.ClearStateAck(9) // the game calls this on objectID 0
	got := c.ProcessIncoming(seg(4, smsg(9, 4, 1, []byte{0x01, 0x11, 0x22, 0, 0}))) // a new entity
	if len(got) != 1 || !bytes.Equal(got[0].Data(), []byte{0x13, 0x11, 0x22, 0, 0}) {
		t.Fatalf("delta after ClearStateAck did not decode")
	}
	if _, ok := c.stateBases[9][2]; ok {
		t.Fatalf("base 2 is older than 4's reference 3 and should be gone")
	}
}

// connection_record_guaranteed_ack: mask bit N retires ack+2+N, ack+1 never.
func TestRetireGuaranteedMaskIsPS2(t *testing.T) {
	c := newTestConn(t)
	for s := uint16(11); s <= 20; s++ {
		c.unacked = append(c.unacked, gmsg(protocol.MessageTypeReliable, s, []byte{0, 0}))
		c.unackedSendTime[s] = time.Now()
	}
	c.sendReliable = 20
	c.retireGuaranteed(10, 1|1<<3)
	if fmt.Sprint(seqs(c.unacked)) != "[11 13 14 16 17 18 19 20]" {
		t.Fatalf("unacked after ack 10 mask 1001: %v", seqs(c.unacked))
	}
	c.retireGuaranteed(14, 0)
	if fmt.Sprint(seqs(c.unacked)) != "[16 17 18 19 20]" {
		t.Fatalf("unacked after ack 14: %v", seqs(c.unacked))
	}
}

// The incoming masks are buffer_read_packed_uns64. Read zigzag, mask 1
// ("ack+2 held") became -1, every bit, and retired 64 messages the server
// had not got.
func TestIncomingMasksAreUnsigned(t *testing.T) {
	c := newTestConn(t)
	c.sendReliable = 30
	for s := uint16(11); s <= 30; s++ {
		c.unacked = append(c.unacked, gmsg(protocol.MessageTypeReliable, s, []byte{0, 0}))
	}
	raw := []byte{0x34, 0x12, 0x00, 0x00} // endpoints
	body := []byte{0x0A, 0x01, 0x00, 0x0A, 0x00, 0x01}
	raw = append(raw, byte(len(body))|0x80, 0x20) // varint(ClientInitiated | size)
	raw = append(raw, body...)
	raw = protocol.AppendCRC(raw)
	pkt, err := ParsePacket(raw, c)
	if err != nil {
		t.Fatal(err)
	}
	if pkt.Body.GuaranteedAckMask != 1 {
		t.Fatalf("mask read as %d, want 1", pkt.Body.GuaranteedAckMask)
	}
	pkt.LocalEndpoint = testServerEP
	c.ProcessIncoming(pkt)
	if fmt.Sprint(seqs(c.unacked)[:2]) != "[11 13]" || len(c.unacked) != 19 {
		t.Fatalf("after ack 10 mask 1: %v", seqs(c.unacked))
	}
}

// A piece shorter than an opcode keeps exactly its bytes.
func TestOneBytePieceParses(t *testing.T) {
	body := []byte{0x00, 0x01, 0x00, 0xFB, 0x01, 0x07, 0x00, 0x5A}
	raw := append([]byte{0x34, 0x12, 0x00, 0x00, byte(len(body)) | 0x80, 0x20}, body...)
	pkt, err := ParsePacket(protocol.AppendCRC(raw), nil)
	if err != nil {
		t.Fatal(err)
	}
	if m := pkt.Body.Messages[0]; m.Sequence != 7 || !bytes.Equal(m.Data(), []byte{0x5A}) {
		t.Fatalf("seq %d data % x, want 7 [5a]", m.Sequence, m.Data())
	}
}

func TestBuildPacketKeepsToTheBudget(t *testing.T) {
	c := newTestConn(t)
	big := make([]byte, 998)
	c.QueueUnreliable(0x0040, big)
	c.QueueUnreliable(0x0040, big)
	c.QueueReliable(0x0013, make([]byte, 3000)) // three pieces: 1300, 1300, 402
	c.mu.Lock()
	var sent [][]byte
	pieces := 0
	for i := 0; i < 8 && len(c.outgoing) > 0; i++ {
		msgs := c.outgoing
		pkt, n := BuildPacket(c, msgs)
		if n == 0 || len(pkt) > 1404 {
			t.Fatalf("flush %d wrote %d messages in %d bytes", i, n, len(pkt))
		}
		for _, m := range msgs[:n] {
			if m.Type.IsReliable() {
				pieces++
				want := protocol.MessageTypeSegmentReliable
				if m.Sequence == 3 {
					want = protocol.MessageTypeReliable
				}
				if m.Type != want {
					t.Fatalf("piece %d is 0x%02X", m.Sequence, byte(m.Type))
				}
			}
		}
		c.outgoing = msgs[n:]
		sent = append(sent, pkt)
	}
	c.mu.Unlock()
	// 1,386 bytes of body room: each 1,000-byte message and each 1,300-byte
	// piece leaves too little for the next, so five packets.
	if len(sent) != 5 || pieces != 3 {
		t.Fatalf("%d packets carrying %d pieces, want 5 packets and 3 pieces", len(sent), pieces)
	}

	// The far end rebuilds the 3,002 bytes from the pieces.
	r := newTestConn(t)
	var got []*protocol.Message
	for _, raw := range sent {
		pkt, err := ParsePacket(raw, r)
		if err != nil {
			t.Fatal(err)
		}
		pkt.LocalEndpoint = testServerEP
		for _, m := range r.ProcessIncoming(pkt) {
			if m.Type.IsReliable() {
				got = append(got, m)
			}
		}
	}
	if len(got) != 1 || got[0].Size() != 3002 || got[0].Opcode != 0x0013 {
		t.Fatalf("reassembled %d messages", len(got))
	}
}

func TestRTOIsMaxRTTPlus100(t *testing.T) {
	c := newTestConn(t)
	if c.rto != 1100*time.Millisecond {
		t.Fatalf("initial RTO %v, want 1.1s", c.rto)
	}
	c.updateRTT(50 * time.Millisecond)
	c.updateRTT(80 * time.Millisecond)
	c.updateRTT(30 * time.Millisecond)
	if c.rto != 180*time.Millisecond {
		t.Fatalf("RTO %v, want 180ms", c.rto)
	}
	for i := 0; i < 16; i++ {
		c.updateRTT(10 * time.Millisecond)
	}
	if c.rto != 110*time.Millisecond {
		t.Fatalf("RTO %v after the ring turned over, want 110ms", c.rto)
	}
}

// A message whose flush the server has passed is resent at once -- unless
// the server's own arrivals are not clean (body flag 0x20).
func TestFastRetransmitOnLaterFlushAck(t *testing.T) {
	for _, short := range []bool{false, true} {
		c := newTestConn(t)
		c.handshakeComplete = true
		c.lastRecvTime = time.Now()
		c.sendReliable = 1
		c.sendArrival = 9
		m := gmsg(protocol.MessageTypeReliable, 1, []byte{0x13, 0x00})
		c.unacked = []*protocol.Message{m}
		c.unackedSendTime[1] = time.Now()
		c.unackedFlush[1] = 5
		c.flushAckMax = 7
		c.serverShort = short
		c.arrivalDirty = true
		c.Flush()
		if resent := c.metrics.Retransmits == 1; resent == short {
			t.Fatalf("short=%v: retransmits %d", short, c.metrics.Retransmits)
		}
		if !short && c.unackedFlush[1] != 9 {
			t.Fatalf("resent in flush %d, want 9", c.unackedFlush[1])
		}
	}
}

// --- The PS2's own captures, replayed (skipped where pcaps/ is absent) ---

func readPCAPUDP(t *testing.T, path string, port uint16) (s2c [][]byte) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("no capture: %v", err)
	}
	if binary.LittleEndian.Uint32(raw) != 0xa1b2c3d4 || binary.LittleEndian.Uint32(raw[20:]) != 1 {
		t.Fatalf("%s: not a little-endian Ethernet pcap", path)
	}
	for p := 24; p+16 <= len(raw); {
		n := int(binary.LittleEndian.Uint32(raw[p+8:]))
		frame := raw[p+16 : p+16+n]
		p += 16 + n
		if len(frame) < 14+20+8 || binary.BigEndian.Uint16(frame[12:]) != 0x0800 {
			continue
		}
		ip := frame[14:]
		ihl := int(ip[0]&0x0F) * 4
		if ip[9] != 17 || len(ip) < ihl+8 {
			continue
		}
		udp := ip[ihl:]
		if binary.BigEndian.Uint16(udp) != port {
			continue
		}
		ulen := int(binary.BigEndian.Uint16(udp[4:]))
		if ulen > len(udp) {
			continue
		}
		s2c = append(s2c, append([]byte(nil), udp[8:ulen]...))
	}
	return s2c
}

// replayPromoted feeds every server datagram of the promoted session (the
// endpoint with the most traffic) through ParsePacket + ProcessIncoming.
func replayPromoted(t *testing.T, datagrams [][]byte, lose func(*ReceivedPacket) bool) (out []*protocol.Message) {
	t.Helper()
	var pkts []*ReceivedPacket
	count := map[uint16]int{}
	for _, d := range datagrams {
		pkt, err := ParsePacket(d, nil)
		if err != nil || pkt.Body == nil {
			continue
		}
		pkts = append(pkts, pkt)
		count[pkt.LocalEndpoint]++
	}
	var ep uint16
	for e, n := range count {
		if n > count[ep] {
			ep = e
		}
	}
	c := newTestConn(t)
	for _, pkt := range pkts {
		if pkt.LocalEndpoint != ep || (lose != nil && lose(pkt)) {
			continue
		}
		out = append(out, c.ProcessIncoming(pkt)...)
	}
	return out
}

func digest(msgs []*protocol.Message) (sum string, state int, dumps []int) {
	h := sha256.New()
	for _, m := range msgs {
		fmt.Fprintf(h, "%02x %d %d|", byte(m.Type), m.Sequence, m.Size())
		h.Write(m.Data())
		if m.Type.IsStateChannel() {
			state++
		} else if m.Opcode == protocol.OpcodeMemoryDump {
			dumps = append(dumps, m.Size())
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil)), state, dumps
}

// pcaps/ps2_main_capture.pcap: the PS2 lost the MemoryDump's pieces 11..18
// and the server resent them, with a keepalive and the next message
// overtaking 14..18. Replayed clean and with that loss, the client delivers
// the same messages; and every state message the PS2's rules take (1,269,
// counted independently with re-tools/drdp_pcap_parser.py) decodes -- not one
// "refnum does not exist".
func TestReplayPS2MainCapture(t *testing.T) {
	d := readPCAPUDP(t, filepath.Join("..", "..", "..", "pcaps", "ps2_main_capture.pcap"), 10070)
	clean := replayPromoted(t, d, nil)
	lossy := replayPromoted(t, d, func(p *ReceivedPacket) bool {
		for _, m := range p.Body.Messages {
			if m.Type == protocol.MessageTypeSegmentReliable && m.Sequence >= 11 && m.Sequence <= 18 &&
				p.Body.ArrivalNumber < 24 {
				return true
			}
		}
		return false
	})
	cs, cstate, cdumps := digest(clean)
	ls, _, _ := digest(lossy)
	if cs != ls {
		t.Fatalf("losing 11..18 changed what was delivered")
	}
	if cstate != 1269 {
		t.Fatalf("%d state messages delivered, want 1269", cstate)
	}
	if fmt.Sprint(cdumps) != "[26085]" {
		t.Fatalf("MemoryDumps %v, want one of 26,085 bytes", cdumps)
	}
}

func TestReplayPS2SandstormCapture(t *testing.T) {
	d := readPCAPUDP(t, filepath.Join("..", "..", "..", "pcaps", "ps2_sandstorm.pcap"), 10070)
	_, state, _ := digest(replayPromoted(t, d, nil))
	if state != 573 {
		t.Fatalf("%d state messages delivered, want 573", state)
	}
}
