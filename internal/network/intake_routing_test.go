package network

import (
	"testing"

	"github.com/eqoa/client/internal/protocol"
)

// The two notices the DRDP library hands the application itself, and the
// source every message carries (message_get_src).

// connection_parse_segment_body: the first segment of a connection the far
// end opened carries MESSAGE_TYPE_CONNECT ahead of its own messages; CLIENT's
// pump resets its world on it. Every message is from the segment's sender.
func TestServerOpenedSessionAnnouncesItself(t *testing.T) {
	c := newTestConn(t)
	promoted := func(arrival, seq uint16) *ReceivedPacket {
		p := seg(arrival, gmsg(protocol.MessageTypeReliable, seq, []byte{0x13, 0x00, 1, 2, 3, 4}))
		p.Header.Flags = protocol.SegmentFlagDidServerInitiate
		p.SessionID = 0x1D9F67A2
		return p
	}
	out := c.ProcessIncoming(promoted(1, 1))
	if len(out) != 2 || out[0].Type != protocol.MessageTypeConnect || out[1].Type != protocol.MessageTypeReliable {
		t.Fatalf("first segment delivered %d messages", len(out))
	}
	for _, m := range out {
		if m.Src != 0x1D9F67A2 {
			t.Fatalf("type 0x%02X from 0x%08X", byte(m.Type), m.Src)
		}
	}
	out = c.ProcessIncoming(promoted(2, 2))
	if len(out) != 1 || out[0].Type != protocol.MessageTypeReliable {
		t.Fatalf("a later segment announced the session again (%d messages)", len(out))
	}
}

// connection_terminate latches, and the application gets its notice once,
// with the reason byte.
func TestTerminateNoticeOnce(t *testing.T) {
	c := newTestConn(t)
	if c.TakeTerminate() != nil {
		t.Fatal("a live connection reported an end")
	}
	c.mu.Lock()
	c.terminateLocked(3)
	c.terminateLocked(2)
	c.mu.Unlock()
	m := c.TakeTerminate()
	if m == nil || m.Type != protocol.MessageTypeDisconnect || len(m.Data()) != 1 || m.Data()[0] != 3 {
		t.Fatalf("notice %+v", m)
	}
	if c.TakeTerminate() != nil || !c.TimedOut() {
		t.Fatal("the notice came twice, or the connection is not ended")
	}
}
