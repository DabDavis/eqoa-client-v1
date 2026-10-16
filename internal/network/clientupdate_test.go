package network

import (
	"bytes"
	"testing"
	"time"

	"github.com/eqoa/client/internal/protocol"
)

// The 0x40 channel's outgoing side against connection_send_state
// (connection.cpp:1181) and connection_record_state_acks (:2374).

func clientUpdates(c *Connection) []*protocol.Message {
	var out []*protocol.Message
	for _, m := range c.outgoing {
		if m.Type == protocol.MessageTypeClientUpdate {
			out = append(out, m)
		}
	}
	return out
}

func flushable(t *testing.T) *Connection {
	c := newTestConn(t)
	c.handshakeComplete = true
	c.lastRecvTime = time.Now()
	return c
}

func ackState(c *Connection, arrival, seq uint16) {
	pkt := seg(arrival)
	pkt.Body.StateAcks = []protocol.StateAck{{Channel: 0x40, Ack: seq}}
	c.ProcessIncoming(pkt)
}

func TestClientUpdateSameStateIsNotSent(t *testing.T) {
	c := flushable(t)
	a := []byte{1, 2, 3}
	c.QueueClientUpdate(a)
	c.QueueClientUpdate([]byte{1, 2, 3})
	if n := len(clientUpdates(c)); n != 1 {
		t.Fatalf("%d 0x40s queued, want 1", n)
	}
	c.Flush()
	c.QueueClientUpdate(a) // the one just written: iSame against the tx list's tail
	if n := len(clientUpdates(c)); n != 0 {
		t.Fatalf("an unchanged state was queued again (%d)", n)
	}
	ackState(c, 1, c.sendStateSeq[0x40])
	c.QueueClientUpdate(a) // acked: the tail is still the same state
	if n := len(clientUpdates(c)); n != 0 {
		t.Fatalf("an unchanged state was queued after its ack (%d)", n)
	}
}

func TestClientUpdatePendingIsReplaced(t *testing.T) {
	c := flushable(t)
	c.QueueClientUpdate([]byte{1})
	seq := c.sendStateSeq[0x40]
	c.QueueClientUpdate([]byte{2})
	q := clientUpdates(c)
	if len(q) != 1 || !bytes.Equal(q[0].Data(), []byte{2}) || q[0].Sequence != seq {
		t.Fatalf("queued %d, want one carrying the newest state under seq %d", len(q), seq)
	}
	if c.sendStateSeq[0x40] != seq {
		t.Fatalf("a replaced state took a sequence")
	}
}

func TestClientUpdateRetransmittedUntilAcked(t *testing.T) {
	c := flushable(t)
	c.QueueClientUpdate([]byte{7, 7})
	c.Flush()
	sent := c.cuSent
	if sent == nil || c.cuPending != nil {
		t.Fatal("the written 0x40 is not outstanding")
	}
	c.cuSentTime = time.Now().Add(-c.rto - time.Millisecond)
	c.arrivalDirty = true
	c.Flush()
	if c.metrics.StateRetransmits != 1 || c.cuSent == nil || c.cuSent.Sequence != sent.Sequence {
		t.Fatalf("retransmits %d, outstanding %v: want the same seq again", c.metrics.StateRetransmits, c.cuSent)
	}
	ackState(c, 1, sent.Sequence-1) // an older ack retires nothing
	if c.cuSent == nil {
		t.Fatal("an ack of an older sequence retired the outstanding 0x40")
	}
	ackState(c, 2, sent.Sequence)
	if c.cuSent != nil {
		t.Fatal("the ack of its sequence did not retire the outstanding 0x40")
	}
	c.cuSentTime = time.Time{}
	c.Flush()
	if c.metrics.StateRetransmits != 1 {
		t.Fatal("an acked 0x40 was retransmitted")
	}
}

func TestClientUpdateNewStateSupersedesTheOutstanding(t *testing.T) {
	c := flushable(t)
	c.QueueClientUpdate([]byte{1})
	c.Flush()
	old := c.cuSent.Sequence
	c.QueueClientUpdate([]byte{2})
	if c.cuSent != nil {
		t.Fatal("the superseded 0x40 is still outstanding")
	}
	if q := clientUpdates(c); len(q) != 1 || q[0].Sequence != old+1 {
		t.Fatalf("want one new 0x40 under seq %d", old+1)
	}
}

func TestClientUpdateForgottenOnANewConnection(t *testing.T) {
	c := flushable(t)
	c.QueueClientUpdate([]byte{1})
	c.Flush()
	c.SwitchToGameServer(0x2222)
	if c.cuSent != nil || c.cuPending != nil || c.cuLast != nil {
		t.Fatal("the old connection's 0x40 survived the switch")
	}
	c.QueueClientUpdate([]byte{1}) // the same state goes out on the new connection
	if len(clientUpdates(c)) != 1 {
		t.Fatal("the first 0x40 of a new connection was suppressed")
	}
}
