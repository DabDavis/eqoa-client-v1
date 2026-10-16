package game

// items.go — the item records and the lists that hold them, as CLIENT keeps them.
//
// An item on the wire is ReadNetItem (CLIENT 0x00CBB040): six slot words, then
// the item proper (func_00CBACE8). A list is VIItemList::Read (0x00BBC8D0): a
// count, the counter its AddItem numbers new records from, and the records.
// The inventory (VIClient+0xC428), the corpse (+0x13B98), the merchant
// (+0x1B310), the trade copy (+0x22B7C) and the bank (VIBankControl+8) are all
// that one class (decomp/modules/client/src/game.h, VIItemList).
//
// The server addresses the inventory two ways, and the difference matters:
//   - by LIST INDEX: 0x003C, 0x003D, 0x003E, 0x003F, 0x000A, 0x0019, 0x1250;
//     the list is kept packed (Took shifts the rest down), so an index moves;
//   - by KEY, the record's +0x2F8 (VIItemList::GetSlotId): 0x0060, and every
//     request the PS2 sends (UseItem, SellItem, Send0062, LootOne, ...).
// AddItem gives a new record the list's next key, not the one it arrived with.

import (
	"fmt"
	"log"
	"unicode/utf16"

	"github.com/DabDavis/eqoa-esf-tools/pkg/buffer"
	"github.com/eqoa/client/internal/protocol"
)

// listCap is the slots a VIItemList has (ItemInternal[40]); AddItem's third
// argument is 40 at every call site.
const listCap = 40

// ItemStat is one of an item's stat bonuses (VIItemStat): the stat id and its
// value, in the order the record lists them.
type ItemStat struct {
	ID    int64
	Value int64
}

// itemLists is the VIItemList state the flat ParsedItems does not carry.
type itemLists struct {
	invNext, bankNext uint32 // VIItemList::m_p04: the key AddItem gives the next new record

	// The corpse being looted (0x0018 .. 0x0016), m_corpse at VIClient+0x13B98.
	Corpse     []ParsedItem
	corpseNext uint32
	HaveCorpse bool // +0x13B94, named by 0x0018's assert

	BankTunar int64  // VIPlayer+0x38 (+0xC314): 0x1253 (VIBankControl::Recv)
	PlayerP44 uint32 // VIPlayer+0x44 (+0xC320): 0x001C; the stat-training window reads it plus the CM count (GetP44Total)

	MerchantID   uint32 // +0x1B300: 0x00B7's merchant; 0xFFFFFFFF = none (IsMerchantOpen)
	MerchantText string // +0x22A78: 0x00B7's text, the merchant's greeting ("I have some decent bait here, ...")
	BlacksmithID uint32 // +0x22B78: 0x0061's blacksmith, sent back with every repair (Send0062); 0xFFFFFFFF = none

	// The trade (0x0057/0x0056). There is no trade window, so an offer is
	// answered the way a busy PS2 answers it, and no trade is ever open.
	TradePartner uint32 // +0x2BB08 m_tradePartner; 0xFFFFFFFF = none
	tradeEntity  uint32 // +0x2BB00: the partner's entity, for the name
	tradeAsked   bool   // +0x2BB04: the trade was this side's request
}

// --- the stream reads (VIMemStream) ------------------------------------------

// readSigned is VIMemStream::ReadSigned (0x00CB8610) over ReadUnsigned
// (0x00CB86B8): seven bits a byte, low first, until a byte without the top
// bit, into 32 bits — the shift is an sllv, so it wraps at 32 — then zigzag.
func readSigned(r *buffer.Reader) (int32, error) {
	var u uint32
	var shift uint
	for {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		u |= uint32(b&0x7F) << (shift & 31)
		shift += 7
		if b&0x80 == 0 {
			break
		}
	}
	v := int32(u >> 1)
	if u&1 != 0 {
		v = ^v
	}
	return v, nil
}

// readWideString is VIMemStream::ReadString(unsigned short *, MaxBytes)
// (0x00CB87E8): a u32 character count, then the characters if they fit with
// their terminator in maxBytes; otherwise they are skipped and the string is
// empty. A NUL inside ends it, as it does for the C string the PS2 keeps.
func readWideString(r *buffer.Reader, maxBytes int) (string, error) {
	n, err := r.ReadUint32()
	if err != nil {
		return "", err
	}
	if uint64(n)*2 > uint64(r.Remaining()) {
		return "", fmt.Errorf("wide string of %d characters, %d bytes left", n, r.Remaining())
	}
	if uint64(n)+1 > uint64(maxBytes/2) {
		r.Advance(int(n) * 2)
		return "", nil
	}
	u := make([]uint16, n)
	for i := range u {
		u[i], _ = r.ReadUint16()
	}
	for i, c := range u {
		if c == 0 {
			u = u[:i]
			break
		}
	}
	return string(utf16.Decode(u)), nil
}

// --- the records ---------------------------------------------------------------

// readItemBody is func_00CBACE8, the item proper (VIItem): 23 zigzag fields —
// +0x58 is read before +0x54 — the name (64 characters) and the description
// (256), then the stats (a count and that many id/value pairs, func_00CBB590).
func readItemBody(r *buffer.Reader, it *ParsedItem) error {
	for _, f := range []*int64{
		&it.ItemID, &it.ItemCost, &it.P08, &it.ItemIcon, &it.Icon2, &it.Slot,
		&it.P18, &it.NoTrade, &it.Rent, &it.P24, &it.AttackType, &it.WeaponDamage,
		&it.Range, &it.LevelReq, &it.MaxStack, &it.MaxHP, &it.Durability,
		&it.ClassUse, &it.RaceUse, &it.P4C, &it.IsLore, &it.P58, &it.IsCraft,
	} {
		v, err := readSigned(r)
		if err != nil {
			return err
		}
		*f = int64(v)
	}
	var err error
	if it.Name, err = readWideString(r, 128); err != nil {
		return fmt.Errorf("name: %w", err)
	}
	if it.Desc, err = readWideString(r, 512); err != nil {
		return fmt.Errorf("description: %w", err)
	}
	n, err := readSigned(r)
	if err != nil {
		return err
	}
	if n < 0 || int(n)*2 > r.Remaining() {
		return fmt.Errorf("stat count %d, %d bytes left", n, r.Remaining())
	}
	it.Stats = make([]ItemStat, 0, n)
	for i := int32(0); i < n; i++ {
		id, err := readSigned(r)
		if err != nil {
			return err
		}
		val, err := readSigned(r)
		if err != nil {
			return err
		}
		it.Stats = append(it.Stats, ItemStat{int64(id), int64(val)})
	}
	return nil
}

// readNetItem is ReadNetItem (CLIENT 0x00CBB040): an item in a list slot —
// quantity, current HP, charges, equip slot and quick slot zigzag, the key a
// raw u32 — then the item proper.
func readNetItem(r *buffer.Reader) (ParsedItem, error) {
	var it ParsedItem
	for _, f := range []*int64{&it.StackLeft, &it.RemainingHP, &it.Charges, &it.EquipLocation, &it.QuickSlot} {
		v, err := readSigned(r)
		if err != nil {
			return it, err
		}
		*f = int64(v)
	}
	var err error
	if it.Key, err = r.ReadUint32(); err != nil {
		return it, err
	}
	return it, readItemBody(r, &it)
}

// readItemList is VIItemList::Read (0x00BBC8D0): a zigzag count, the u32
// counter AddItem numbers new records from, then the records. The PS2 does
// not clamp the count to its 40 slots.
func readItemList(r *buffer.Reader) ([]ParsedItem, uint32, error) {
	n, err := readSigned(r)
	if err != nil {
		return nil, 0, err
	}
	next, err := r.ReadUint32()
	if err != nil {
		return nil, 0, err
	}
	if n < 0 || int(n) > r.Remaining() {
		return nil, next, fmt.Errorf("item count %d, %d bytes left", n, r.Remaining())
	}
	items := make([]ParsedItem, 0, n)
	for i := int32(0); i < n; i++ {
		it, err := readNetItem(r)
		if err != nil {
			return items, next, fmt.Errorf("item %d of %d: %w", i, n, err)
		}
		it.InvSlot = int(i)
		items = append(items, it)
	}
	return items, next, nil
}

// --- the list operations (VIItemList) ----------------------------------------

// listAdd is VIItemList::AddItem (0x00BBD130). A stackable item (maxStack 2
// or more) goes onto the FIRST record with the same item id, clamped at
// maxStack, whatever that leaves over is dropped; failing that, and for
// anything else, it takes a new record at the end, with the list's next key
// and its quantity clamped (a stackable one) or set to 1. A full list takes
// nothing. It returns how many it took, and gives the item the key it was
// filed under.
func listAdd(list []ParsedItem, next *uint32, item *ParsedItem, maxStack int64, capacity int) ([]ParsedItem, int64) {
	if maxStack >= 2 {
		for i := range list {
			if list[i].ItemID != item.ItemID {
				continue
			}
			have, took := list[i].StackLeft, item.StackLeft
			if sum := have + took; maxStack < sum {
				list[i].StackLeft = maxStack
				took = maxStack - have
			} else {
				list[i].StackLeft = sum
			}
			item.Key = list[i].Key
			return list, took
		}
	}
	if len(list) >= capacity {
		return list, 0
	}
	rec := *item
	rec.Key = *next
	*next++
	item.Key = rec.Key
	took := int64(1)
	if maxStack >= 2 {
		took = item.StackLeft
		if maxStack < took {
			took = maxStack
		}
	}
	rec.StackLeft = took
	return append(list, rec), took
}

// listGet is VIItemList::Get (0x00BBD398): a copy of the record, or a record
// with quantity 0 when the slot is past the end or holds none.
func listGet(list []ParsedItem, slot int) ParsedItem {
	if slot < 0 || slot >= len(list) || list[slot].StackLeft <= 0 {
		return ParsedItem{}
	}
	return list[slot]
}

// listTook is VIItemList::Took (0x00BBD430, VIItemList.h:324): n off the
// record's quantity, and the record leaves the list, the rest moving down,
// when none is left. Its assert (a slot in range holding at least n) parks
// the PS2 for good; here the message is refused.
func listTook(list []ParsedItem, slot int, n int64) ([]ParsedItem, bool) {
	if slot < 0 || slot >= len(list) || list[slot].StackLeft < n {
		return list, false
	}
	list[slot].StackLeft -= n
	if list[slot].StackLeft == 0 {
		list = append(list[:slot], list[slot+1:]...)
	}
	return list, true
}

// listMove is VIItemList::Op003D (0x00BE02F0): the record at from goes to to,
// the ones between moving one place towards from.
func listMove(list []ParsedItem, from, to int) bool {
	if from < 0 || from >= len(list) || to < 0 || to >= len(list) {
		return false
	}
	rec := list[from]
	if from < to {
		copy(list[from:to], list[from+1:to+1])
	} else {
		copy(list[to+1:from+1], list[to:from])
	}
	list[to] = rec
	return true
}

// listFind is VIItemList::Find (0x00BDFFA8): the index of the record with
// this key, or -1.
func listFind(list []ParsedItem, key uint32) int {
	for i := range list {
		if list[i].Key == key {
			return i
		}
	}
	return -1
}

// listUnequip is VIItemList::Unequip (0x00BE0568): every record worn in that
// equip slot comes off; false when none was.
func listUnequip(list []ParsedItem, equip int64) bool {
	found := false
	for i := range list {
		if list[i].EquipLocation == equip {
			list[i].EquipLocation = -1
			found = true
		}
	}
	return found
}

// --- the inventory and the bank, as ParsedItems holds them ---------------------

func (gs *GameState) invCount() int {
	n := gs.InventoryItemCount
	if n < 0 {
		n = 0
	}
	if n > len(gs.ParsedItems) {
		n = len(gs.ParsedItems)
	}
	return n
}

// Inventory is the player's inventory (VIClient+0xC428): ParsedItems' head.
// It shares ParsedItems' storage.
func (gs *GameState) Inventory() []ParsedItem { return gs.ParsedItems[:gs.invCount()] }

// Bank is the bank's list (VIBankControl+8): ParsedItems' tail.
func (gs *GameState) Bank() []ParsedItem { return gs.ParsedItems[gs.invCount():] }

// setLists writes the two lists back, renumbering InvSlot as the index into
// ParsedItems (an inventory record's is its list index).
func (gs *GameState) setLists(inv, bank []ParsedItem) {
	all := make([]ParsedItem, 0, len(inv)+len(bank))
	all = append(append(all, inv...), bank...)
	for i := range all {
		all[i].InvSlot = i
	}
	gs.ParsedItems = all
	gs.InventoryItemCount = len(inv)
}

func (gs *GameState) invCopy() []ParsedItem  { return append([]ParsedItem(nil), gs.Inventory()...) }
func (gs *GameState) bankCopy() []ParsedItem { return append([]ParsedItem(nil), gs.Bank()...) }

// SetInventoryNextKey and SetBankNextKey take the counter each list's header
// carries (VIItemList::m_p04, the u32 after the count): the key the next new
// record gets. The MemoryDump reads both headers.
func (gs *GameState) SetInventoryNextKey(k uint32) { gs.invNext = k }
func (gs *GameState) SetBankNextKey(k uint32)      { gs.bankNext = k }

// addInventory is the inventory's AddItem, as AddInvItem, Msg0019 and
// Msg001A call it: the item's own max stack, 40 slots.
func (gs *GameState) addInventory(item *ParsedItem) int64 {
	inv, took := listAdd(gs.invCopy(), &gs.invNext, item, item.MaxStack, listCap)
	gs.setLists(inv, gs.Bank())
	return took
}

// tookInventory is the inventory's Took.
func (gs *GameState) tookInventory(slot int, n int64) bool {
	inv, ok := listTook(gs.invCopy(), slot, n)
	if ok {
		gs.setLists(inv, gs.Bank())
	}
	return ok
}

// --- what the item windows show, from VIClient's getters -----------------------

// roundRate is value × rate / 100 with C's truncating division, plus one when
// the remainder is 50 or more (GetMerchantPrice, GetInvItemSellPrice).
func roundRate(value, rate int64) int64 {
	v := int32(value) * int32(rate)
	r := v / 100
	if v%100 >= 50 {
		r++
	}
	return int64(r)
}

// MerchantPrice is what merchant item i costs (VIClient::GetMerchantPrice,
// 0x00BCC950): its value at the merchant's +0x1B30C rate, in percent.
func (gs *GameState) MerchantPrice(i int) int64 {
	if i < 0 || i >= len(gs.MerchantItems) {
		return 0
	}
	return roundRate(gs.MerchantItems[i].ItemCost, int64(gs.MerchantSellRate))
}

// ItemCondition is VIClient::GetInvItemCondition (0x00BCC9C0): current HP
// over max HP, 1 when the item has none.
func ItemCondition(it ParsedItem) float32 {
	if int32(it.MaxHP) == 0 {
		return 1
	}
	return float32(int32(it.RemainingHP)) / float32(int32(it.MaxHP))
}

// ItemWear is VIClient::GetInvItemWear (0x00BCC8C0): half the lost condition,
// 0.5 at none left and 0 at full.
func ItemWear(it ParsedItem) float32 {
	c := ItemCondition(it)
	if c == 0 {
		return 0.5
	}
	if c == 1 {
		return 0
	}
	return (1 - c) * 0.5
}

// ItemRepairCost is what repairing it costs (VIClient::GetInvItemRepairCost,
// 0x00BCCC00): its value times the lost condition, truncated.
func ItemRepairCost(it ParsedItem) int64 {
	return int64(int32(float32(int32(it.ItemCost)) * (1 - ItemCondition(it))))
}

// SellPrice is what the merchant pays for it (VIClient::GetInvItemSellPrice,
// 0x00BCC818): its value at the merchant's +0x1B308 rate, less its wear.
func (gs *GameState) SellPrice(it ParsedItem) int64 {
	r := int32(roundRate(it.ItemCost, int64(gs.MerchantBuyRate)))
	return int64(r - int32(float32(r)*ItemWear(it)))
}

// --- handlers --------------------------------------------------------------------

// handleRemoveInvItem handles 0x003C, Msg003C (CLIENT 0x00BCB138): a slot
// byte, a sound byte, then a zigzag count — n off that inventory slot.
func (gs *GameState) handleRemoveInvItem(msg *protocol.Message) {
	r := buffer.NewReader(msg.Data()[2:])
	slot, err1 := r.ReadByte()
	sound, err2 := r.ReadByte()
	n, err3 := readSigned(r)
	if err1 != nil || err2 != nil || err3 != nil {
		log.Printf("RemoveInvItem: short (%d bytes)", len(msg.Data()))
		return
	}
	if !gs.tookInventory(int(slot), int64(n)) {
		log.Printf("RemoveInvItem: slot %d n=%d refused (inventory %d)", slot, n, gs.invCount())
		return
	}
	log.Printf("RemoveInvItem: slot %d n=%d sound=%d", slot, n, sound)
}

// handleUseItemResult handles 0x003E, Msg003E (CLIENT 0x00BCB228): a slot
// byte and a charge byte — a charge off it (the record goes at none left), or
// the record itself.
func (gs *GameState) handleUseItemResult(msg *protocol.Message) {
	d := msg.Data()
	if len(d) < 4 {
		log.Printf("UseItemResult: short (%d bytes)", len(d))
		return
	}
	slot, charge := int(d[2]), d[3]
	inv := gs.invCopy()
	if slot >= len(inv) {
		log.Printf("UseItemResult: slot %d of %d", slot, len(inv))
		return
	}
	if charge != 0 {
		// VIItemList::UseCharge (0x00BE0478)
		inv[slot].Charges--
		if inv[slot].Charges == 0 {
			inv, _ = listTook(inv, slot, 1)
		}
	} else {
		inv, _ = listTook(inv, slot, 1) // VIItemList::Remove
	}
	gs.setLists(inv, gs.Bank())
	log.Printf("UseItemResult: slot %d charge=%d", slot, charge)
}

// handleLootOne handles 0x0019, Msg0019 (CLIENT 0x00BD99A0): a slot byte and
// a zigzag the PS2 does not use — that corpse record into the inventory, and
// off the corpse as much as went in.
func (gs *GameState) handleLootOne(msg *protocol.Message) {
	r := buffer.NewReader(msg.Data()[2:])
	slot, err := r.ReadByte()
	if err != nil {
		return
	}
	item := listGet(gs.Corpse, int(slot))
	if item.StackLeft <= 0 {
		log.Printf("LootOne: corpse slot %d of %d is empty", slot, len(gs.Corpse))
		return
	}
	item.EquipLocation = -1
	took := gs.addInventory(&item)
	gs.Corpse, _ = listTook(gs.Corpse, int(slot), took)
	log.Printf("LootOne: %q x%d from corpse slot %d", item.Name, took, slot)
}

// handleBankWithdrawn handles 0x1250 (VIBankControl::Recv, func_00BB9F68):
// a slot byte and a zigzag count, n off that bank slot.
func (gs *GameState) handleBankWithdrawn(msg *protocol.Message) {
	r := buffer.NewReader(msg.Data()[2:])
	slot, err1 := r.ReadByte()
	n, err2 := readSigned(r)
	if err1 != nil || err2 != nil {
		return
	}
	bank, ok := listTook(gs.bankCopy(), int(slot), int64(n))
	if !ok {
		log.Printf("BankWithdrawn: slot %d n=%d refused (bank %d)", slot, n, len(gs.Bank()))
		return
	}
	gs.setLists(gs.invCopy(), bank)
}

// handleBankDeposited handles 0x1251 (VIBankControl::Recv, func_00BB9EE8): one
// item record, into the bank as AddItem files it.
func (gs *GameState) handleBankDeposited(msg *protocol.Message) {
	item, err := readNetItem(buffer.NewReader(msg.Data()[2:]))
	if err != nil {
		log.Printf("BankDeposited: %v", err)
		return
	}
	bank, took := listAdd(gs.bankCopy(), &gs.bankNext, &item, item.MaxStack, listCap)
	gs.setLists(gs.invCopy(), bank)
	log.Printf("BankDeposited: %q x%d", item.Name, took)
}

// sendLootClosed is LootNoWindow (CLIENT 0x00BCB098): with no loot window to
// open, the PS2 tells the server at once that it closed the corpse (0x0016).
func (gs *GameState) sendLootClosed() {
	if gs.Conn != nil {
		gs.Conn.QueueReliable(protocol.OpcodeClientCloseLoot, nil)
	}
}

// fitsEquip is func_00CC61A8: whether an item worn in wear slot w may go in
// equip slot e.
func fitsEquip(w, e int64) bool {
	switch w {
	case 1:
		return e == 0
	case 2:
		return e == 21
	case 3:
		return e == 2 || e == 3
	case 4:
		return e == 4
	case 5:
		return e == 5
	case 6:
		return e == 6 || e == 7
	case 7:
		return e == 8
	case 8:
		return e == 9 || e == 10
	case 9:
		return e == 11
	case 10:
		return e == 12
	case 11:
		return e == 13
	case 12:
		return e == 14
	case 13:
		return e == 17
	case 14:
		return e == 14 || e == 15
	case 15:
		return e == 16
	case 16, 17:
		return e == 18
	case 18:
		return e == 19 || e == 20
	case 19:
		return e == 1
	case 20:
		return e == 14
	case 21:
		return e == 15
	case 22, 23, 24, 25, 26, 27:
		return e == 19
	}
	return false
}

// equipDisplaces is Msg003F's jump table (data_00CDA880, by wear slot): the
// equip slots an item of wear slot w takes off besides its own.
func equipDisplaces(w, e int64) []int64 {
	switch w {
	case 6: // forearm
		return []int64{8}
	case 7: // both forearms
		return []int64{7, 6}
	case 12, 20: // primary, fishing
		return []int64{16, 18, 19}
	case 13: // shield
		return []int64{15, 16, 18, 20}
	case 14, 21: // secondary, bait
		if e == 15 {
			return []int64{16, 18, 17, 20}
		}
		return []int64{16, 18, 19}
	case 15: // two-handed
		return []int64{14, 15, 17, 18, 19, 20}
	case 16: // bow
		return []int64{14, 15, 17, 16, 19, 20}
	case 18: // held
		if e != 20 {
			return []int64{16, 18, 14}
		}
		return []int64{16, 18, 17, 15}
	case 22, 23, 24, 25, 26, 27: // crafting tools
		return []int64{16, 18, 14}
	}
	return nil
}

// fitsQuickSlot3 is VIClient::FitsSlot3 (0x00BDC470): the wear slots quick
// slot 3 takes.
func fitsQuickSlot3(w int64) bool {
	switch w {
	case 12, 14, 15, 16, 18, 20:
		return true
	}
	return false
}

// tradeName is VIClient::TradeName (0x00BCCCB8)'s lookup, with the fallback
// the caller passes: the entity's name, or that.
func (gs *GameState) tradeName(id uint32, fallback string) string {
	if e, ok := gs.Entities[id]; ok && e.Name != "" {
		return e.Name
	}
	return fallback
}

// tradeReset is VIClient::TradeReset (0x00BDA168).
func (gs *GameState) tradeReset() {
	gs.TradePartner = 0xFFFFFFFF
	gs.tradeEntity = 0xFFFFFFFF
	gs.tradeAsked = false
}

// newItemLists is what VIClient's constructor leaves: no merchant, blacksmith
// or trade partner.
func newItemLists() itemLists {
	return itemLists{MerchantID: 0xFFFFFFFF, BlacksmithID: 0xFFFFFFFF, TradePartner: 0xFFFFFFFF, tradeEntity: 0xFFFFFFFF}
}

// handleLoot handles opcode 0x0018 (CLIENT's dispatcher, 0x00BD1DD0): the
// corpse's item list (VIItemList::Read). The PS2 opens the loot window when no
// other panel is up, and otherwise tells the server at once that it closed it
// (LootNoWindow). The Go client has no loot window, so it is always the second.
func (gs *GameState) handleLoot(msg *protocol.Message) {
	if gs.HaveCorpse {
		log.Printf("Loot: a second corpse while one is open (the PS2 asserts !HaveCorpse)")
	}
	items, next, err := readItemList(buffer.NewReader(msg.Data()[2:]))
	if err != nil {
		log.Printf("Loot: %v", err)
	}
	gs.Corpse, gs.corpseNext, gs.HaveCorpse = items, next, true
	for _, it := range items {
		logItemRecord("Loot", it)
	}
	gs.sendLootClosed()
}

// logItemRecord writes an item to the item log the DB is filled from.
func logItemRecord(source string, it ParsedItem) {
	writeItemLog(source, fmt.Sprintf("ITEM: name=%q desc=%q id=%d cost=%d icon=%d lvl=%d slot=%d maxStack=%d stats=%v",
		it.Name, it.Desc, it.ItemID, it.ItemCost, it.ItemIcon, it.LevelReq, it.Slot, it.MaxStack, it.Stats))
}
