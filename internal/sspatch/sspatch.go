// Package sspatch applies Sandstorm's S2 patch the way their SS stub applies
// it on the console, and reads off the patched addresses what the patch
// changes about the client.
//
// On a PS2, the patcher only puts S2 on the card. A loader in the launcher
// reads SS off the card and writes it into memory; SS installs a stub and a
// hook in CLIENT, and the stub, once SUPPORT is resident, reads S2 and writes
// its records over CLIENT, SUPPORT and a 32 KB arena of new code and tables
// (claude-notes/sandstorm-patch-s2-20260730.md). This client does not run
// MIPS, so it reads the same records the other way round: each feature the
// Go client ports from S2 is looked up at the address S2 hooks, and what the
// hook says — a table, a limit, a string — is what the client uses. A hook
// that is absent, or not the shape the reader knows, leaves that feature
// as retail has it.
package sspatch

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// ClientBase is CLIENT's SNR1 header in every EE dump we have. S2's
// CLIENT-relative records are kept at ClientBase + their offset, so the
// addresses here read like the dumps'.
const ClientBase = 0x00BB4980

// The cave window: a record or relocation whose address, high bit cleared,
// lies in [caveLo, caveLo+caveSize) is in the arena the stub allocates.
const (
	caveLo   = 0x000F8000
	caveSize = 0x8000
)

// maxFile is the stub's staging buffer, malloc(0x40000): VIMemCardReadFile
// reads at most this much, and a read that fills it is refused.
const maxFile = 0x40000

// rlc1 is "RLC1", the relocation table's tag.
const rlc1 = 0x31434C52

// Record is one (address, data) pair of the patch, its address as the file
// gives it: high bit clear is CLIENT-relative, set is absolute (or a cave).
type Record struct {
	Addr uint32
	Data []byte
}

// Patch is a parsed S2.
type Patch struct {
	Records []Record
	Relocs  []uint32
}

// Parse validates an S2 the way the stub does before it disables interrupts
// and writes anything (ss-stage1-6, 0x00158170 .. 0x00158340):
//
//   - fewer than 8 bytes, or exactly the 0x40000 the read buffer holds, is
//     refused;
//   - each record is an 8-byte header and its data padded to 4, and must fit
//     in the file; a cave record must also fit in the 32 KB window;
//   - the records end at (0, 0), followed by "RLC1", a count, and exactly
//     count relocation words to the end of the file.
//
// The stub applies nothing unless all of it holds; neither does Parse.
func Parse(data []byte) (*Patch, error) {
	// The read stops at the buffer, so a larger file reads as one that
	// fills it.
	n := len(data)
	if n < 8 || n >= maxFile {
		return nil, fmt.Errorf("S2: %d bytes", n)
	}
	p := &Patch{}
	pos := 0
	for {
		if n < pos+8 {
			return nil, errors.New("S2: a record header runs past the end")
		}
		addr := binary.LittleEndian.Uint32(data[pos:])
		size := binary.LittleEndian.Uint32(data[pos+4:])
		pos += 8
		if addr|size == 0 {
			break
		}
		if addr&0x80000000 != 0 {
			if off := addr&0x7FFFFFFF - caveLo; off < caveSize && off+size > caveSize {
				return nil, fmt.Errorf("S2: record %08X runs out of the cave", addr)
			}
		}
		if uint32(n-pos) < size {
			return nil, fmt.Errorf("S2: record %08X runs past the end", addr)
		}
		p.Records = append(p.Records, Record{Addr: addr, Data: data[pos : pos+int(size)]})
		pos += int(size+3) &^ 3
	}
	rest := n - pos
	if rest < 8 || binary.LittleEndian.Uint32(data[pos:]) != rlc1 {
		return nil, errors.New("S2: no RLC1 after the records")
	}
	count := binary.LittleEndian.Uint32(data[pos+4:])
	if uint64(count)*4+8 != uint64(rest) {
		return nil, fmt.Errorf("S2: %d relocations do not fill the %d bytes left", count, rest-8)
	}
	for i := uint32(0); i < count; i++ {
		p.Relocs = append(p.Relocs, binary.LittleEndian.Uint32(data[pos+8+int(i)*4:]))
	}
	return p, nil
}

// resolve is where the stub writes an address: CLIENT-relative onto
// ClientBase, a cave address moved by delta, anything else as it is.
func resolve(addr, delta uint32) uint32 {
	if addr&0x80000000 == 0 {
		return addr + ClientBase
	}
	a := addr & 0x7FFFFFFF
	if a-caveLo < caveSize {
		a += delta
	}
	return a
}

// Image is the EE memory the patch writes, and only that: every byte a
// record put there. The rest of memory (SUPPORT, CLIENT, the heap) is not
// this client's to have.
type Image struct {
	mem     map[uint32]byte
	records []span // where each record landed, in file order
}

type span struct{ lo, hi uint32 }

// Apply writes the records, then the relocations, as the stub does
// (0x00158348 .. 0x0015849C), with the arena at caveLo+delta. The stub's
// delta is always a multiple of 0x10000; Features reads an image applied at
// delta 0, where every cave pointer is the address the file was linked at.
func (p *Patch) Apply(delta uint32) *Image {
	img := &Image{mem: map[uint32]byte{}}
	for _, r := range p.Records {
		a := resolve(r.Addr, delta)
		for i, b := range r.Data {
			img.mem[a+uint32(i)] = b
		}
		img.records = append(img.records, span{a, a + uint32(len(r.Data))})
	}
	for _, w := range p.Relocs {
		a := resolve(w&0x7FFFFFFC|w&0x80000000, delta)
		v, ok := img.Word(a)
		if !ok {
			continue // a word of retail code or data: not this client's to have
		}
		switch w & 3 {
		case 0:
			v += delta
		case 1: // a J-type target
			target := ((v&0x03FFFFFF)<<2 + delta) & 0x0FFFFFFF
			v = (v & 0xFC000000) | (target >> 2)
		default: // the lui half of a lui/addiu pair; 3 is taken as 2
			v = (v & 0xFFFF0000) | ((v&0xFFFF)+(delta>>16))&0xFFFF
		}
		img.putWord(a, v)
	}
	return img
}

// Word reads a little-endian word, and whether the patch wrote all of it.
func (img *Image) Word(a uint32) (uint32, bool) {
	var v uint32
	for i := uint32(0); i < 4; i++ {
		b, ok := img.mem[a+i]
		if !ok {
			return 0, false
		}
		v |= uint32(b) << (8 * i)
	}
	return v, true
}

func (img *Image) putWord(a, v uint32) {
	for i := uint32(0); i < 4; i++ {
		img.mem[a+i] = byte(v >> (8 * i))
	}
}

// Bytes reads n patched bytes at a.
func (img *Image) Bytes(a uint32, n int) ([]byte, bool) {
	out := make([]byte, n)
	for i := range out {
		b, ok := img.mem[a+uint32(i)]
		if !ok {
			return nil, false
		}
		out[i] = b
	}
	return out, true
}

// CString reads the NUL-terminated string at a, if the patch wrote it.
func (img *Image) CString(a uint32) (string, bool) {
	var s []byte
	for i := uint32(0); i < 256; i++ {
		b, ok := img.mem[a+i]
		if !ok {
			return "", false
		}
		if b == 0 {
			return string(s), true
		}
		s = append(s, b)
	}
	return "", false
}

// recordEnd is the end of the record that wrote a, so a table's length can
// be read off the record that holds it.
func (img *Image) recordEnd(a uint32) (uint32, bool) {
	for _, s := range img.records {
		if a >= s.lo && a < s.hi {
			return s.hi, true
		}
	}
	return 0, false
}
