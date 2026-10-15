package network

import (
	"bytes"
	"testing"

	"github.com/DabDavis/eqoa-esf-tools/pkg/buffer"
)

// The PS2's own ack from pcaps/ps2_sandstorm.pcap (packet #40): having taken
// 3..10 in order, lost 11..18 and held 19..25, it acked rx=10 with
// 0b11111110000000 written unsigned — 0x80 0x7F on the wire.
func TestGuaranteedAckMaskMatchesPS2(t *testing.T) {
	held := map[uint16]bool{}
	for s := uint16(19); s <= 25; s++ {
		held[s] = true
	}
	mask := GuaranteedAckMask(held, 10)
	if mask != 0b11111110000000 {
		t.Fatalf("mask %b, want 11111110000000", mask)
	}
	var buf [16]byte
	w := buffer.NewWriter(buf[:])
	w.Write7BitEncodedUInt64(mask)
	if got := buf[:w.Position()]; !bytes.Equal(got, []byte{0x80, 0x7F}) {
		t.Fatalf("wire % x, want 80 7f", got)
	}
	// rx+1 is never held; one past the window is dropped; the seqnum wraps.
	if m := GuaranteedAckMask(map[uint16]bool{11: true, 12: true, 76: true}, 10); m != 1 {
		t.Fatalf("mask %b, want 1 (only 12)", m)
	}
	if m := GuaranteedAckMask(map[uint16]bool{1: true}, 0xFFFF); m != 1 {
		t.Fatalf("across the wrap: %b, want 1", m)
	}
}
