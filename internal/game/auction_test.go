package game

import (
	"bytes"
	"encoding/binary"
	"testing"
	"unicode/utf16"

	"github.com/eqoa/client/internal/protocol"
)

// An untouched window's filter is the bytes every captured search carries:
// flags 0, two empty strings, both masks full, levels 1-50, 0, no price
// limit, -2, -1, 1.
func TestAuctionFilterDefault(t *testing.T) {
	want := []byte{
		0x00, 0, 0, 0, 0, 0, 0, 0, 0,
		0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
		0x02, 0x64, 0x00, 0xFE, 0xA7, 0xD6, 0xB9, 0x07, 0x03, 0x01, 0x01,
	}
	if got := DefaultAuctionFilter(50).encode(); !bytes.Equal(got, want) {
		t.Fatalf("default filter\n got % x\nwant % x", got, want)
	}
	// Sandstorm's window opens on 60: zigzag 120.
	if got := DefaultAuctionFilter(60).encode(); got[18] != 0x78 {
		t.Fatalf("max level 60 encodes as %#x", got[18])
	}
}

// Search text sets flag bits 0x0C and goes in the WIDE string, after the
// empty 8-bit one.
func TestAuctionFilterText(t *testing.T) {
	f := DefaultAuctionFilter(60)
	f.Text = "Rusty"
	got := f.encode()
	want := []byte{0x0C, 0, 0, 0, 0, 5, 0, 0, 0}
	for _, c := range "Rusty" {
		want = append(want, byte(c), 0)
	}
	if !bytes.Equal(got[:len(want)], want) {
		t.Fatalf("text filter\n got % x\nwant % x", got[:len(want)], want)
	}
	if !bytes.Equal(got[len(want):], DefaultAuctionFilter(60).encode()[9:]) {
		t.Fatalf("fields after the strings moved: % x", got[len(want):])
	}
}

func zz(v int64) []byte { return zigzagEncode(v) }

// auctionListing writes one listing the way the 0x81 page carries it.
func auctionListing(id int64, seller, name string, level int64) []byte {
	var b []byte
	for _, v := range []int64{id, 1, 0, 0, 100, 500, 4242} {
		b = append(b, zz(v)...)
	}
	b = binary.LittleEndian.AppendUint32(b, uint32(len(seller)))
	b = append(b, seller...)
	for i := 0; i < 23; i++ {
		v := int64(0)
		if i == 13 {
			v = level
		}
		b = append(b, zz(v)...)
	}
	w := utf16.Encode([]rune(name))
	b = binary.LittleEndian.AppendUint32(b, uint32(len(w)))
	for _, c := range w {
		b = append(b, byte(c), byte(c>>8))
	}
	b = binary.LittleEndian.AppendUint32(b, 0) // no description
	return append(b, zz(0)...)                 // no stats
}

func auctionPage(seq int64, listings ...[]byte) []byte {
	b := append([]byte{0x81}, zz(seq)...)
	b = append(b, zz(0)...)
	b = append(b, zz(int64(len(listings)))...)
	for _, l := range listings {
		b = append(b, l...)
	}
	for k := int64(0); k < 3; k++ { // the three per-listing arrays
		b = append(b, zz(int64(len(listings)))...)
		for range listings {
			b = append(b, zz(k-1)...)
		}
	}
	return b
}

// 0x1206 sub-type 0x81 is a page of auction results, never a quest; an
// empty page ends the paging.
func TestAuctionResultsPage(t *testing.T) {
	gs := NewGameState("test", 10070, 9735, 7000, "test")
	gs.HandleMessage(s2msg(protocol.OpcodeAuctionStuff, auctionPage(2,
		auctionListing(1001, "Tester", "Rusty Sword", 12),
		auctionListing(1002, "Tester", "Cloth Cap", 3))))
	if len(gs.AuctionItems) != 2 || gs.AuctionItems[0].Item.Name != "Rusty Sword" ||
		gs.AuctionItems[0].Seller != "Tester" || gs.AuctionItems[0].Item.LevelReq != 12 {
		t.Fatalf("page 1: %+v", gs.AuctionItems)
	}
	if gs.AuctionItems[1].PageValues != [3]int64{-1, 0, 1} {
		t.Fatalf("per-listing values %v", gs.AuctionItems[1].PageValues)
	}
	if gs.AuctionLastListing != 1002 || gs.AuctionSeq != 6 {
		t.Fatalf("cursor %d, next request byte %d (want 1002, 6)", gs.AuctionLastListing, gs.AuctionSeq)
	}
	gs.HandleMessage(s2msg(protocol.OpcodeAuctionStuff, auctionPage(3)))
	if gs.AuctionLastListing != 0 || len(gs.AuctionItems) != 2 {
		t.Fatalf("an empty page should end the paging: cursor %d, %d listings", gs.AuctionLastListing, len(gs.AuctionItems))
	}
	if n := len(gs.ChatLog); n == 0 || gs.ChatLog[n-1].Text != "End of search results." {
		t.Fatalf("chat %+v", gs.ChatLog)
	}
}
