package protocol

import (
	"bytes"
	"math/rand"
	"testing"
)

// rleDecode is run_length_decode (rle.cpp).
func rleDecode(in []byte) []byte {
	var out []byte
	for i := 0; i < len(in); {
		c := in[i]
		i++
		if c == 0 {
			break
		}
		var lit, zeros int
		if c&0x80 != 0 {
			lit, zeros = int(c&0x7f), int(in[i])
			i++
		} else {
			lit, zeros = int(c>>4), int(c&0xf)
		}
		out = append(out, make([]byte, zeros)...)
		out = append(out, in[i:i+lit]...)
		i += lit
	}
	return out
}

// Encodings worked by hand from run_length_encode.
func TestRunLengthEncodeGolden(t *testing.T) {
	eight := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	cases := []struct {
		in, want []byte
	}{
		{nil, []byte{0x00}},
		{[]byte{1, 2, 3}, []byte{0x30, 1, 2, 3, 0x00}},
		{eight, append(append([]byte{0x88, 0x00}, eight...), 0x00)}, // 8 literals: long form
		{append(make([]byte, 20), 1), []byte{0x81, 20, 1, 0x00}},    // 16+ zeros: long form
		{[]byte{5, 0, 0, 0}, []byte{0x10, 5, 0x03, 0x00}},           // trailing zeros, then the end
		{make([]byte, 300), []byte{0x80, 0xFF, 0x80, 45, 0x00}},     // zero runs stop at 255
		{[]byte{0, 0, 9, 0, 9}, []byte{0x12, 9, 0x11, 9, 0x00}},     // a zero ends a literal run
	}
	for _, c := range cases {
		if got := RunLengthEncode(c.in); !bytes.Equal(got, c.want) {
			t.Errorf("encode %v:\n got % x\nwant % x", c.in, got, c.want)
		}
	}
}

// Every input survives the PS2's decoder, whatever its runs.
func TestRunLengthRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for n := 0; n < 2000; n++ {
		in := make([]byte, rng.Intn(400))
		for i := range in {
			// long runs of each kind, as a state channel has
			if rng.Intn(10) < 6 {
				in[i] = 0
			} else {
				in[i] = byte(1 + rng.Intn(255))
			}
		}
		if rng.Intn(3) == 0 {
			for i := rng.Intn(len(in) + 1); i < len(in) && i < 200; i++ {
				in[i] = 0x7f
			}
		}
		got := rleDecode(RunLengthEncode(in))
		if !bytes.Equal(got, in) && !(len(in) == 0 && len(got) == 0) {
			t.Fatalf("round trip of %d bytes failed", len(in))
		}
	}
}
