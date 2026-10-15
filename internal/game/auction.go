package game

import (
	"encoding/binary"
	"log"
	"unicode/utf16"
)

// The auction search, as CLIENT's VIBankControl sends it. A search and each
// page after it are opcode 0x1205, sub-command 7: the request number, a
// cursor, a direction, then the search's filter, all zigzag but the strings,
// the masks and the bytes. A new search (0x00BB8F10) sends cursor -1 and
// direction 1, the next page (0x00BB9110) the last listing's id and 1, and
// the previous page (0x00BB92C0) a listing id and 0. Every page of a search
// repeats its filter.

// AuctionFilter is the PS2's 192-byte search filter (VIBankControl+0x77A0)
// in the order func_00BB4A80 writes it, filled the way the Auctions window's
// Search (0x00C139B0) fills it. Fields this client has no control for keep
// the values every capture shows.
type AuctionFilter struct {
	Text     string // +26: the search text, UTF-16, at most 64 characters
	Text8    string // +1: an 8-bit string out of the window's +0x578, at most 23 bytes; not traced
	Mask1    uint32 // +156: 1 << the window's +0x4D4, or every bit for -1
	Mask2    uint32 // +160: 1 << +0x4D8, every bit for -1, none for -2
	MinLevel int32  // +164: Min. Level
	MaxLevel int32  // +168: Max. Level
	Unk172   int32  // +172: the window's +0x4E4
	MaxPrice int32  // +176: 999,999,999 is no limit
	Unk180   int32  // +180: the window's +0x4EC, -2 when unset
	Unk184   int32  // +184: the window's +0x4F0, -1 when unset
	Toggle   bool   // +188: the window's +0x4F4, which one of its buttons flips
}

// auctionNoPriceLimit is the Auctions window's "no limit" price.
const auctionNoPriceLimit = 999999999

// DefaultAuctionFilter is the filter of an untouched Auctions window, levels
// 1 to maxLevel (retail opens on 50, Sandstorm's S2 on 60). It encodes to the
// bytes the captured searches carry.
func DefaultAuctionFilter(maxLevel int) AuctionFilter {
	return AuctionFilter{
		Mask1: 0xFFFFFFFF, Mask2: 0xFFFFFFFF,
		MinLevel: 1, MaxLevel: int32(maxLevel),
		MaxPrice: auctionNoPriceLimit, Unk180: -2, Unk184: -1, Toggle: true,
	}
}

// flags is the filter's first byte, which says which fields are set: 0x10 a
// price limit, 0x02 +180, 0x01 +184, 0x0C search text.
func (f AuctionFilter) flags() byte {
	var b byte
	if f.MaxPrice < auctionNoPriceLimit {
		b |= 0x10
	}
	if f.Unk180 >= -1 {
		b |= 0x02
	}
	if f.Unk184 >= 0 {
		b |= 0x01
	}
	if f.Text != "" {
		b |= 0x0C
	}
	return b
}

// encode writes the filter as func_00BB4A80 does: the flags byte, the 8-bit
// string and the wide string (each a u32 length and no terminator), the two
// masks as raw u32s, six zigzag fields and the toggle byte.
func (f AuctionFilter) encode() []byte {
	b := []byte{f.flags()}
	text8 := f.Text8
	if len(text8) > 23 {
		text8 = text8[:23]
	}
	b = binary.LittleEndian.AppendUint32(b, uint32(len(text8)))
	b = append(b, text8...)
	wide := utf16.Encode([]rune(f.Text))
	if len(wide) > 64 {
		wide = wide[:64]
	}
	b = binary.LittleEndian.AppendUint32(b, uint32(len(wide)))
	for _, c := range wide {
		b = append(b, byte(c), byte(c>>8))
	}
	b = binary.LittleEndian.AppendUint32(b, f.Mask1)
	b = binary.LittleEndian.AppendUint32(b, f.Mask2)
	for _, v := range []int32{f.MinLevel, f.MaxLevel, f.Unk172, f.MaxPrice, f.Unk180, f.Unk184} {
		b = append(b, zigzagEncode(int64(v))...)
	}
	if f.Toggle {
		return append(b, 1)
	}
	return append(b, 0)
}

// SendAuctionSearch starts a search: cursor -1, direction 1. The filter is
// kept for the pages after it, and the pages already held are dropped.
func (e *Engine) SendAuctionSearch(f AuctionFilter) {
	gs := e.State
	gs.AuctionFilter = f
	gs.AuctionItems = nil
	gs.AuctionTotalCount = 0
	gs.AuctionLastListing = 0
	gs.AuctionSeq = 0
	seq := gs.AuctionSessionID
	if seq == 0 {
		seq = 0x04
	}
	e.sendAuctionPage(seq, -1)
}

// SendAuctionNextPage asks for the page after the last one: the cursor is
// the last listing's id, the filter the search's own.
func (e *Engine) SendAuctionNextPage() {
	gs := e.State
	if gs.AuctionLastListing == 0 || gs.AuctionSeq == 0 {
		log.Printf("AuctionNextPage: no previous page data")
		return
	}
	e.sendAuctionPage(gs.AuctionSeq, gs.AuctionLastListing)
}

func (e *Engine) sendAuctionPage(seq byte, cursor int64) {
	body := zigzagEncode(cursor)
	body = append(body, 1) // direction: 1 forward
	body = append(body, e.State.AuctionFilter.encode()...)
	e.SendAuctionRequest(0x07, seq, body)
}
