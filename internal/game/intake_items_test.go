package game

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"log"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/DabDavis/eqoa-esf-tools/pkg/buffer"
	"github.com/eqoa/client/internal/network"
	"github.com/eqoa/client/internal/protocol"
)

func newReader(b []byte) *buffer.Reader { return buffer.NewReader(b) }

// itemWire writes what CLIENT's VIMemStream writers write: WriteSigned's
// zigzag, WriteU32, WriteString's u32 count and characters.
type itemWire struct{ b []byte }

func (w *itemWire) signed(v int32) *itemWire {
	u := uint32(v) << 1
	if v < 0 {
		u = uint32(^v)<<1 | 1
	}
	for {
		b := byte(u & 0x7F)
		u >>= 7
		if u != 0 {
			b |= 0x80
		}
		w.b = append(w.b, b)
		if u == 0 {
			return w
		}
	}
}

func (w *itemWire) u8(v byte) *itemWire { w.b = append(w.b, v); return w }

func (w *itemWire) u32(v uint32) *itemWire {
	w.b = binary.LittleEndian.AppendUint32(w.b, v)
	return w
}

func (w *itemWire) wide(s string) *itemWire {
	u := utf16.Encode([]rune(s))
	w.u32(uint32(len(u)))
	for _, c := range u {
		w.b = binary.LittleEndian.AppendUint16(w.b, c)
	}
	return w
}

// testItem is one record's worth of values, in wire order.
type testItem struct {
	qty, hp, charges, equip, quick int32
	key                            uint32
	body                           [23]int32 // +0x00 .. +0x50, then +0x58, +0x54
	name, desc                     string
	stats                          [][2]int32
}

func (w *itemWire) item(it testItem) *itemWire {
	w.signed(it.qty).signed(it.hp).signed(it.charges).signed(it.equip).signed(it.quick).u32(it.key)
	for _, v := range it.body {
		w.signed(v)
	}
	w.wide(it.name).wide(it.desc).signed(int32(len(it.stats)))
	for _, s := range it.stats {
		w.signed(s[0]).signed(s[1])
	}
	return w
}

// newTestItem is a plain record: item id, wear slot, max stack and quantity
// as given, -1 for the equip and quick slots, the key it is filed under.
func newTestItem(id, wear, maxStack, qty int32, key uint32, name string) testItem {
	it := testItem{qty: qty, equip: -1, quick: -1, key: key, name: name}
	it.body[0] = id
	it.body[5] = wear
	it.body[14] = maxStack
	return it
}

func itemMsg(op protocol.GameOpcode, body []byte) *protocol.Message {
	m := &protocol.Message{Opcode: op}
	m.SetData(append([]byte{byte(op), byte(op >> 8)}, body...))
	return m
}

// itemState is a GameState holding these inventory and bank records, with
// each list's next key the count, as Sandstorm's MemoryDump has it.
func itemState(inv, bank []testItem) *GameState {
	gs := NewGameState("test", 10070, 9735, 7000, "test")
	var w itemWire
	w.signed(int32(len(inv))).u32(uint32(len(inv)))
	for _, it := range inv {
		w.item(it)
	}
	invList, invNext, err := readItemList(newReader(w.b))
	if err != nil {
		panic(err)
	}
	w = itemWire{}
	w.signed(int32(len(bank))).u32(uint32(len(bank)))
	for _, it := range bank {
		w.item(it)
	}
	bankList, bankNext, err := readItemList(newReader(w.b))
	if err != nil {
		panic(err)
	}
	gs.setLists(invList, bankList)
	gs.SetInventoryNextKey(invNext)
	gs.SetBankNextKey(bankNext)
	return gs
}

// captureLog runs f with the log written to a buffer and returns it.
func captureLog(f func()) string {
	var b bytes.Buffer
	out, flags := log.Writer(), log.Flags()
	log.SetOutput(&b)
	log.SetFlags(0)
	defer func() { log.SetOutput(out); log.SetFlags(flags) }()
	f()
	return b.String()
}

// Every field lands where func_00CBACE8 and ReadNetItem put it, +0x58 before
// +0x54, and the stats keep their order (and a repeated id).
func TestReadNetItemFieldOrder(t *testing.T) {
	it := testItem{qty: 3, hp: -4, charges: 5, equip: 14, quick: 2, key: 0xA1B2C3D4,
		name: "Name", desc: "Desc", stats: [][2]int32{{8, 50}, {0, -2}, {8, 7}}}
	for i := range it.body {
		it.body[i] = int32(1000 + i)
	}
	var w itemWire
	got, err := readNetItem(newReader(w.item(it).b))
	if err != nil {
		t.Fatal(err)
	}
	want := ParsedItem{StackLeft: 3, RemainingHP: -4, Charges: 5, EquipLocation: 14, QuickSlot: 2, Key: 0xA1B2C3D4,
		ItemID: 1000, ItemCost: 1001, P08: 1002, ItemIcon: 1003, Icon2: 1004, Slot: 1005, P18: 1006,
		NoTrade: 1007, Rent: 1008, P24: 1009, AttackType: 1010, WeaponDamage: 1011, Range: 1012,
		LevelReq: 1013, MaxStack: 1014, MaxHP: 1015, Durability: 1016, ClassUse: 1017, RaceUse: 1018,
		P4C: 1019, IsLore: 1020, P58: 1021, IsCraft: 1022, Name: "Name", Desc: "Desc",
		Stats: []ItemStat{{8, 50}, {0, -2}, {8, 7}}}
	if !itemsEqual(got, want) {
		t.Fatalf("got\n%+v\nwant\n%+v", got, want)
	}
}

func itemsEqual(a, b ParsedItem) bool { return reflect.DeepEqual(a, b) }

// The 0x003B a Sandstorm merchant sent in ps2_main_capture.pcap, byte for byte.
func TestReadNetItemSandstormRecord(t *testing.T) {
	const h = "3B0002C8010001011F000000CAFD034C00D0BBDFE205002A000002000000002A28C80164FEFF03FE0F0000000011000000460069007300680069006E0067002000420061006900740020004C0076003200310037000000410020006D0069007800740075007200650020006F006600200077006F0072006D007300200061006E006400200067007200750062007300200069006E00200061006E00200075006E006900640065006E007400690066006900610062006C006500200067006F006F0070002E00023806"
	raw, _ := hex.DecodeString(h)
	r := newReader(raw[2:])
	it, err := readNetItem(r)
	if err != nil {
		t.Fatal(err)
	}
	if r.Remaining() != 0 {
		t.Errorf("%d bytes left over", r.Remaining())
	}
	if it.StackLeft != 1 || it.RemainingHP != 100 || it.EquipLocation != -1 || it.QuickSlot != -1 || it.Key != 31 ||
		it.ItemID != 32613 || it.ItemCost != 38 || it.Slot != 21 || it.Rent != 1 || it.LevelReq != 21 ||
		it.MaxStack != 20 || it.MaxHP != 100 || it.Durability != 50 || it.ClassUse != 32767 || it.RaceUse != 1023 ||
		it.Name != "Fishing Bait Lv21" || !strings.HasSuffix(it.Desc, "unidentifiable goop.") ||
		len(it.Stats) != 1 || it.Stats[0] != (ItemStat{28, 3}) {
		t.Errorf("%+v", it)
	}
}

// ReadUnsigned's shift is an sllv: past 32 bits it wraps, it does not drop.
func TestReadSignedWrapsAt32Bits(t *testing.T) {
	v, err := readSigned(newReader([]byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x01}))
	if err != nil || v != 4 {
		t.Fatalf("got %d, %v; want 4 (1<<35 lands at bit 3, zigzag 8 = 4)", v, err)
	}
}

// A string must fit with its terminator; a longer one is skipped, not read,
// and the record after it still parses.
func TestReadWideStringLimit(t *testing.T) {
	var w itemWire
	w.wide(strings.Repeat("a", 63)).wide(strings.Repeat("b", 64)).wide("x\x00y").u8(0x7E)
	r := newReader(w.b)
	a, _ := readWideString(r, 128)
	b, _ := readWideString(r, 128)
	c, _ := readWideString(r, 128)
	tail, _ := r.ReadByte()
	if len(a) != 63 || b != "" || c != "x" || tail != 0x7E {
		t.Fatalf("%d %q %q %#x", len(a), b, c, tail)
	}
}

// VIItemList::AddItem: onto the first stack of the same item, clamped; else a
// new record under the list's next key, never the key it arrived with.
func TestAddInvItemFilesLikeAddItem(t *testing.T) {
	gs := itemState([]testItem{
		newTestItem(500, -1, 20, 18, 0, "Bait"),
		newTestItem(600, 12, 1, 1, 1, "Sword"),
	}, []testItem{newTestItem(500, -1, 20, 4, 0, "Banked bait")})

	var w itemWire
	gs.HandleMessage(itemMsg(protocol.OpcodeAddInvItem, w.item(newTestItem(500, -1, 20, 5, 77, "Bait")).b))
	inv := gs.Inventory()
	if len(inv) != 2 || inv[0].StackLeft != 20 {
		t.Fatalf("stack: %d records, first holds %d (want 20, the clamp)", len(inv), inv[0].StackLeft)
	}

	w = itemWire{}
	gs.HandleMessage(itemMsg(protocol.OpcodeAddInvItem, w.item(newTestItem(600, 12, 1, 3, 77, "Sword")).b))
	inv = gs.Inventory()
	if len(inv) != 3 || inv[2].Key != 2 || inv[2].StackLeft != 1 || inv[2].InvSlot != 2 {
		t.Fatalf("non-stackable: %+v", inv[len(inv)-1])
	}
	if len(gs.Bank()) != 1 || gs.Bank()[0].Name != "Banked bait" || gs.Bank()[0].StackLeft != 4 {
		t.Fatalf("the bank changed: %+v", gs.Bank())
	}

	// A full list takes nothing.
	for i := 0; i < 50; i++ {
		w = itemWire{}
		gs.HandleMessage(itemMsg(protocol.OpcodeAddInvItem, w.item(newTestItem(int32(1000+i), -1, 1, 1, 0, "x")).b))
	}
	if gs.InventoryItemCount != listCap {
		t.Fatalf("%d records, want the cap %d", gs.InventoryItemCount, listCap)
	}
}

// 0x003C and 0x003E address the list by INDEX, and Took packs it; 0x0060
// addresses it by KEY, which does not move.
func TestIndexAndKeyAddressing(t *testing.T) {
	a := newTestItem(1, 1, 1, 1, 0, "A")
	b := newTestItem(2, -1, 50, 5, 1, "B")
	c := newTestItem(3, 5, 1, 1, 2, "C")
	c.charges = 2
	gs := itemState([]testItem{a, b, c}, nil)

	// 0x003C, 0x003E, 0x0019, 0x1250 and 0x1251 have no case in HandleMessage
	// yet, so their handlers are called directly.
	var w itemWire
	gs.handleRemoveInvItem(itemMsg(protocol.OpcodeRemoveInvItem, w.u8(0).u8(0).signed(1).b)) // A goes
	inv := gs.Inventory()
	if len(inv) != 2 || inv[0].Name != "B" || inv[1].Name != "C" || inv[1].Key != 2 {
		t.Fatalf("after 0x003C: %v", names(inv))
	}

	w = itemWire{}
	gs.handleAdjustItemHP(itemMsg(protocol.OpcodeAdjustItemHP, w.u32(2).signed(-5).b))
	if gs.Inventory()[1].RemainingHP != 0 {
		t.Fatalf("0x0060 by key 2: HP %d, want 0 (floored)", gs.Inventory()[1].RemainingHP)
	}
	w = itemWire{}
	gs.handleAdjustItemHP(itemMsg(protocol.OpcodeAdjustItemHP, w.u32(2).signed(8730).b))
	if gs.Inventory()[1].RemainingHP != 8730 {
		t.Fatalf("0x0060: HP %d", gs.Inventory()[1].RemainingHP)
	}

	// A charge off C, then its last charge takes the record.
	gs.handleUseItemResult(itemMsg(protocol.OpcodeInteractItem, []byte{1, 1}))
	if inv := gs.Inventory(); len(inv) != 2 || inv[1].Charges != 1 {
		t.Fatalf("one charge: %+v", inv)
	}
	gs.handleUseItemResult(itemMsg(protocol.OpcodeInteractItem, []byte{1, 1}))
	if inv := gs.Inventory(); len(inv) != 1 || inv[0].Name != "B" {
		t.Fatalf("last charge: %v", names(inv))
	}

	// More than a slot holds is refused (the PS2's assert).
	w = itemWire{}
	gs.handleRemoveInvItem(itemMsg(protocol.OpcodeRemoveInvItem, w.u8(0).u8(0).signed(6).b))
	if gs.Inventory()[0].StackLeft != 5 {
		t.Fatalf("over-take: %d left", gs.Inventory()[0].StackLeft)
	}
}

func names(l []ParsedItem) []string {
	var s []string
	for _, it := range l {
		s = append(s, it.Name)
	}
	return s
}

// 0x003D moves one record, the rest shifting towards where it was.
func TestArrangeItem(t *testing.T) {
	gs := itemState([]testItem{
		newTestItem(1, -1, 1, 1, 0, "A"), newTestItem(2, -1, 1, 1, 1, "B"),
		newTestItem(3, -1, 1, 1, 2, "C"), newTestItem(4, -1, 1, 1, 3, "D"),
	}, nil)
	gs.HandleMessage(itemMsg(protocol.OpcodeArrangeItem, []byte{0, 2}))
	if got := strings.Join(names(gs.Inventory()), ""); got != "BCAD" {
		t.Fatalf("0 -> 2: %s", got)
	}
	gs.HandleMessage(itemMsg(protocol.OpcodeArrangeItem, []byte{3, 0}))
	if got := strings.Join(names(gs.Inventory()), ""); got != "DBCA" {
		t.Fatalf("3 -> 0: %s", got)
	}
	for i, it := range gs.Inventory() {
		if it.InvSlot != i {
			t.Fatalf("InvSlot %d at %d", it.InvSlot, i)
		}
	}
}

// 0x003F's fit check and its jump table: a two-hander takes off both hands,
// a shield the off hand; an item that does not fit the slot changes nothing.
func TestEquipDisplaces(t *testing.T) {
	primary := newTestItem(1, 12, 1, 1, 0, "Primary")
	primary.equip = 14
	offhand := newTestItem(2, 14, 1, 1, 1, "Offhand")
	offhand.equip = 15
	twoHander := newTestItem(3, 15, 1, 1, 2, "Two-hander")
	shield := newTestItem(4, 13, 1, 1, 3, "Shield")
	helm := newTestItem(5, 1, 1, 1, 4, "Helm")
	gs := itemState([]testItem{primary, offhand, twoHander, shield, helm}, nil)

	gs.HandleMessage(itemMsg(protocol.OpcodeEquipItem, []byte{4, 5})) // a helm will not go on the chest
	if gs.Inventory()[4].EquipLocation != -1 {
		t.Fatal("a helm went in equip slot 5")
	}
	gs.HandleMessage(itemMsg(protocol.OpcodeEquipItem, []byte{2, 16}))
	inv := gs.Inventory()
	if inv[0].EquipLocation != -1 || inv[1].EquipLocation != -1 || inv[2].EquipLocation != 16 {
		t.Fatalf("two-hander: %d %d %d", inv[0].EquipLocation, inv[1].EquipLocation, inv[2].EquipLocation)
	}
	gs.HandleMessage(itemMsg(protocol.OpcodeEquipItem, []byte{0, 14})) // a primary takes the two-hander off
	gs.HandleMessage(itemMsg(protocol.OpcodeEquipItem, []byte{1, 15}))
	gs.HandleMessage(itemMsg(protocol.OpcodeEquipItem, []byte{3, 17})) // a shield takes the off hand off
	inv = gs.Inventory()
	if inv[0].EquipLocation != 14 || inv[1].EquipLocation != -1 || inv[2].EquipLocation != -1 || inv[3].EquipLocation != 17 {
		t.Fatalf("primary+shield: %d %d %d %d", inv[0].EquipLocation, inv[1].EquipLocation, inv[2].EquipLocation, inv[3].EquipLocation)
	}
	gs.HandleMessage(itemMsg(protocol.OpcodeUnequipItem, []byte{17}))
	if gs.Inventory()[3].EquipLocation != -1 {
		t.Fatal("0x0040 left the shield on")
	}
}

// 0x000A: a quick slot takes only the item class that fits it, and the item
// that held it lets go.
func TestQuickSlots(t *testing.T) {
	potion := newTestItem(1, -1, 10, 1, 0, "Potion")
	potion.body[19] = 2 // +0x4C
	stone := newTestItem(2, -1, 10, 1, 1, "Stone")
	stone.body[19] = 7
	potion2 := newTestItem(3, -1, 10, 1, 2, "Potion2")
	potion2.body[19] = 2
	gs := itemState([]testItem{potion, stone, potion2}, nil)

	gs.HandleMessage(itemMsg(protocol.OpcodePrepareItem, []byte{0, 1}))
	gs.HandleMessage(itemMsg(protocol.OpcodePrepareItem, []byte{1, 0})) // a 7 does not fit quick slot 0
	gs.HandleMessage(itemMsg(protocol.OpcodePrepareItem, []byte{1, 2}))
	gs.HandleMessage(itemMsg(protocol.OpcodePrepareItem, []byte{2, 1})) // takes quick slot 1 from the first
	inv := gs.Inventory()
	if inv[0].QuickSlot != -1 || inv[1].QuickSlot != 2 || inv[2].QuickSlot != 1 {
		t.Fatalf("quick slots %d %d %d", inv[0].QuickSlot, inv[1].QuickSlot, inv[2].QuickSlot)
	}
}

// The corpse: 0x0018 is its own list, never the inventory, and with no loot
// window the PS2 closes it at once (0x0016 out); 0x0019 takes one record,
// 0x001A all of them, 0x0016 empties it, 0x0085 says why it cannot be looted.
func TestCorpseLoot(t *testing.T) {
	c, _ := network.NewConnection("127.0.0.1", 9, 0xFFFE)
	defer c.Close()
	gs := itemState([]testItem{newTestItem(500, -1, 20, 18, 0, "Bait")}, nil)
	gs.Conn = c

	var w itemWire
	w.signed(3).u32(3).
		item(newTestItem(500, -1, 20, 6, 0, "Bait")).
		item(newTestItem(700, 5, 1, 1, 1, "Tunic")).
		item(newTestItem(800, -1, 10, 4, 2, "Tooth"))
	out := captureLog(func() { gs.HandleMessage(itemMsg(protocol.OpcodeLoot, w.b)) })
	if !gs.HaveCorpse || len(gs.Corpse) != 3 || gs.InventoryItemCount != 1 {
		t.Fatalf("0x0018: corpse %d, inventory %d", len(gs.Corpse), gs.InventoryItemCount)
	}
	if !strings.Contains(out, "QueueReliable: opcode=0x0016") {
		t.Fatalf("no 0x0016 sent:\n%s", out)
	}

	w = itemWire{}
	gs.handleLootOne(itemMsg(protocol.OpcodeClientLoot, w.u8(1).signed(0).b))
	if len(gs.Corpse) != 2 || gs.InventoryItemCount != 2 || gs.Inventory()[1].Name != "Tunic" || gs.Inventory()[1].Key != 1 {
		t.Fatalf("0x0019: corpse %v, inventory %v", names(gs.Corpse), names(gs.Inventory()))
	}

	// Loot all: two of the six bait fit the stack, so that record stays with four.
	gs.HandleMessage(itemMsg(protocol.OpcodeLootResult, nil))
	if len(gs.Corpse) != 1 || gs.Corpse[0].StackLeft != 4 || gs.Inventory()[0].StackLeft != 20 ||
		gs.InventoryItemCount != 3 || gs.Inventory()[2].Name != "Tooth" {
		t.Fatalf("0x001A: corpse %+v, inventory %v", gs.Corpse, names(gs.Inventory()))
	}

	gs.HandleMessage(itemMsg(protocol.OpcodeClientCloseLoot, nil))
	if gs.HaveCorpse || len(gs.Corpse) != 0 {
		t.Fatal("0x0016 left the corpse")
	}

	gs.HandleMessage(itemMsg(protocol.OpcodeLootOptions, []byte{3}))
	gs.HandleMessage(itemMsg(protocol.OpcodeLootOptions, []byte{9}))
	if n := len(gs.ChatLog); n != 1 || gs.ChatLog[0].Text != "That corpse belongs to another player or group" {
		t.Fatalf("0x0085: %+v", gs.ChatLog)
	}
}

// The merchant: its own list, its text, and prices at its rates.
func TestMerchantBox(t *testing.T) {
	gs := itemState([]testItem{}, nil)
	var w itemWire
	sword := newTestItem(10, 12, 1, 1, 0, "Sword")
	sword.body[1] = 1249 // value
	gs.Entities[0x00100BB9] = &Entity{ObjectID: 0x00100BB9, Name: "Pole Merchant Susan"}
	w.u32(0x00100BB9).signed(60).signed(150).signed(1).u32(1).item(sword).wide("Weapons, cheap")
	gs.HandleMessage(itemMsg(protocol.OpcodeMerchantBox, w.b))
	if !gs.MerchantOpen || gs.MerchantID != 0x00100BB9 || gs.MerchantName != "Pole Merchant Susan" ||
		gs.MerchantText != "Weapons, cheap" || len(gs.MerchantItems) != 1 {
		t.Fatalf("%v %#x %q %q %d", gs.MerchantOpen, gs.MerchantID, gs.MerchantName, gs.MerchantText, len(gs.MerchantItems))
	}
	if p := gs.MerchantPrice(0); p != 1874 { // 1249 x 150 = 187350 -> 1873 r 50 -> 1874
		t.Errorf("MerchantPrice %d, want 1874", p)
	}
	worn := ParsedItem{ItemCost: 1000, RemainingHP: 30, MaxHP: 40}
	if p := gs.SellPrice(worn); p != 525 { // 600, wear 0.125: 600 - 75
		t.Errorf("SellPrice %d, want 525", p)
	}
	if c := ItemRepairCost(worn); c != 250 {
		t.Errorf("ItemRepairCost %d, want 250", c)
	}
}

// The Sandstorm merchant in ps2_main_capture.pcap reads to its last byte.
func TestMerchantBoxSandstormCapture(t *testing.T) {
	d := replayReadUDP(t, filepath.Join("..", "..", "..", "pcaps", "ps2_main_capture.pcap"), 10070)
	var msgs []*protocol.Message
	var pkts []*network.ReceivedPacket
	count := map[uint16]int{}
	for _, x := range d {
		if pkt, err := network.ParsePacket(x, nil); err == nil && pkt.Body != nil {
			pkts = append(pkts, pkt)
			count[pkt.LocalEndpoint]++
		}
	}
	var ep uint16
	for e, n := range count {
		if n > count[ep] {
			ep = e
		}
	}
	c, _ := network.NewConnection("127.0.0.1", 9, 0xFFFE)
	defer c.Close()
	captureLog(func() {
		for _, pkt := range pkts {
			if pkt.LocalEndpoint == ep {
				for _, m := range c.ProcessIncoming(pkt) {
					if dd := m.Data(); len(dd) > 2 && dd[0] == 0xB7 && dd[1] == 0 {
						msgs = append(msgs, m)
					}
				}
			}
		}
	})
	if len(msgs) == 0 {
		t.Skip("no 0x00B7 in the capture")
	}
	r := newReader(msgs[0].Data()[2:])
	id, _ := r.ReadUint32()
	buy, _ := readSigned(r)
	sell, _ := readSigned(r)
	items, next, err := readItemList(r)
	if err != nil {
		t.Fatal(err)
	}
	text, err := readWideString(r, 256)
	if err != nil || r.Remaining() != 0 {
		t.Fatalf("text %q, %v, %d left", text, err, r.Remaining())
	}
	if id != 0x00100BB9 || buy != 100 || sell != 100 || len(items) != 10 || next != 10 {
		t.Fatalf("%#x %d %d %d %d", id, buy, sell, len(items), next)
	}
	t.Logf("merchant 0x%08X %q: %v", id, text, names(items))
}

// With no trade window, an offer is answered as a busy PS2 answers it.
func TestTradeRequestBusy(t *testing.T) {
	c, _ := network.NewConnection("127.0.0.1", 9, 0xFFFE)
	defer c.Close()
	gs := NewGameState("test", 10070, 9735, 7000, "test")
	gs.Conn = c
	gs.Entities[0x1234] = &Entity{ObjectID: 0x1234, Name: "Grimefod"}
	var w itemWire
	out := captureLog(func() { gs.HandleMessage(itemMsg(protocol.OpcodeTradeRequest, w.u32(77).u32(0x1234).b)) })
	if len(gs.ChatLog) != 1 || gs.ChatLog[0].Text != "Grimefod wants to trade with you, but you are busy." {
		t.Fatalf("%+v", gs.ChatLog)
	}
	if !strings.Contains(out, "QueueReliable: opcode=0x0056") || !strings.Contains(out, "dataLen=5") {
		t.Fatalf("no decline sent:\n%s", out)
	}
	if gs.TradePartner != 0xFFFFFFFF {
		t.Fatal("the trade was not reset")
	}
	// 0x0056 from anyone but the partner is dropped unless it is 1, 2 or 20.
	w = itemWire{}
	gs.HandleMessage(itemMsg(protocol.OpcodeTradeUI, w.u8(17).u32(99).b))
	if len(gs.ChatLog) != 1 {
		t.Fatalf("a stray sub-type 17 was shown: %+v", gs.ChatLog)
	}
}

// The bank: 0x1251 files a record into the bank, 0x1250 takes from it by
// index, 0x1253 is its tunar, and none of it touches the inventory.
func TestBankMessages(t *testing.T) {
	gs := itemState([]testItem{newTestItem(1, -1, 1, 1, 0, "Carried")},
		[]testItem{newTestItem(2, -1, 50, 5, 0, "Gem")})
	var w itemWire
	gs.handleBankDeposited(itemMsg(0x1251, w.item(newTestItem(3, 12, 1, 1, 9, "Sword")).b))
	w = itemWire{}
	gs.handleBankDeposited(itemMsg(0x1251, w.item(newTestItem(2, -1, 50, 4, 9, "Gem")).b))
	if b := gs.Bank(); len(b) != 2 || b[0].StackLeft != 9 || b[1].Key != 1 || gs.InventoryItemCount != 1 {
		t.Fatalf("0x1251: bank %+v", names(b))
	}
	w = itemWire{}
	gs.handleBankWithdrawn(itemMsg(0x1250, w.u8(0).signed(2).b))
	if b := gs.Bank(); b[0].StackLeft != 7 {
		t.Fatalf("0x1250: %d left", b[0].StackLeft)
	}
	w = itemWire{}
	gs.HandleMessage(itemMsg(protocol.OpcodeConfirmBankTunar, w.signed(0).b)) // one byte: the old length test dropped it
	w = itemWire{}
	gs.HandleMessage(itemMsg(protocol.OpcodeConfirmBankTunar, w.signed(123456).b))
	if gs.BankTunar != 123456 {
		t.Fatalf("0x1253: %d", gs.BankTunar)
	}
	if gs.Inventory()[0].Name != "Carried" || len(gs.Inventory()) != 1 {
		t.Fatal("the inventory changed")
	}
}

// 0x001C is one u32, 0x0061 the blacksmith, 0x0063 forgets it.
func TestSmallItemMessages(t *testing.T) {
	gs := NewGameState("test", 10070, 9735, 7000, "test")
	var w itemWire
	gs.HandleMessage(itemMsg(protocol.OpcodeNPCInteraction, w.u32(37).b))
	if gs.PlayerP44 != 37 {
		t.Fatalf("0x001C: %d", gs.PlayerP44)
	}
	w = itemWire{}
	gs.HandleMessage(itemMsg(protocol.OpcodeBlackSmithMenu, w.u32(0x00100BBE).b))
	if !gs.BlacksmithOpen || gs.BlacksmithID != 0x00100BBE {
		t.Fatalf("0x0061: %v %#x", gs.BlacksmithOpen, gs.BlacksmithID)
	}
	gs.HandleMessage(itemMsg(protocol.OpcodeCloseBlacksmithMenu, nil))
	if gs.BlacksmithOpen || gs.BlacksmithID != 0xFFFFFFFF {
		t.Fatal("0x0063 left it open")
	}
	gs.HandleMessage(itemMsg(protocol.OpcodeInventoryFull, nil))
	if len(gs.ChatLog) != 0 {
		t.Fatalf("0x00D7 shows nothing on the PS2: %+v", gs.ChatLog)
	}
}
