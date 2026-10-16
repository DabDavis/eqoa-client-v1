package game

// abilities.go — the ability list, VIClient+0x2BB2C: a VIMap<int, VIAbility>
// keyed by ability id. The MemoryDump fills it (ReadAbilities, 0x00BDCDC0)
// and eight messages edit it — 0x00B3, B4, B5, B6, B8, CD, FD and FF all take
// the map (claude-notes/frontiersforge-viclient-xref-20261015.md), which is
// why the old "entity" names for that family were wrong. Go keeps the list in
// gs.ParsedSpells, in the order it arrived; the hotbar is a view of it: an
// ability is on the bar when it is valid, at its slot.
//
// Every message but 0x00CD starts with the same u32, VIClient+0x2BB60, which
// the client stores and sends back as the first word of 0x0032, 0x0033 and
// 0x00FE (Send0032/Send0033/Send00FE).

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/DabDavis/eqoa-esf-tools/pkg/buffer"
	"github.com/eqoa/client/internal/protocol"
)

// AbilityRecord is one of an ability's counted 144-byte sub-records
// (VIRec144_BE08C8): four zigzag ints and a wide name. Meaning unknown; no
// Sandstorm ability carries one.
type AbilityRecord struct {
	P00, P04, P08, P0C int32
	Name               string
}

// vimsSigned is VIMemStream::ReadSigned (0x00CB8610) over ReadUnsigned
// (0x00CB86B8): seven bits a byte, low first, gathered in 32 bits — a shift
// past 31 wraps, since the EE's sllv takes its low five bits — and then the
// zigzag sign out of bit 0.
func vimsSigned(r *buffer.Reader) (int32, error) {
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

// vimsWString is VIMemStream::ReadString(unsigned short *, MaxBytes)
// (0x00CB87E8): a u32 count of characters, then UTF-16LE. A string that
// would not fit its buffer with the terminator is skipped and reads as "".
func vimsWString(r *buffer.Reader, maxBytes uint32) (string, error) {
	n, err := r.ReadUint32()
	if err != nil {
		return "", err
	}
	if n+1 <= maxBytes/2 {
		return r.ReadStringUnicode(int(n))
	}
	if uint64(n)*2 > uint64(r.Remaining()) {
		return "", buffer.ErrBufferOverflow
	}
	return "", r.Advance(int(n) * 2)
}

// readAbility is VIAbility::Read (0x00BDCBE0; read order checked against its
// disassembly): seven zigzag ints, the range as a raw float, seven more, the
// name (64 wide characters) and description (128), then a zigzag count of
// sub-records. The C# reference's "Unk2, Unk3, uint16 range" is that float:
// its two low bytes written as zigzag zeros and the high half as a u16.
func readAbility(r *buffer.Reader) (ParsedSpell, error) {
	var sp ParsedSpell
	ints := []*int64{&sp.SpellID, &sp.AddedOrder, &sp.OnHotBar, &sp.WhereOnHotBar,
		&sp.Lockout, &sp.ShowHide, &sp.AbilityLevel}
	for _, p := range ints {
		v, err := vimsSigned(r)
		if err != nil {
			return sp, err
		}
		*p = int64(v)
	}
	rng, err := r.ReadFloat32()
	if err != nil {
		return sp, err
	}
	sp.RangeF = rng
	sp.Range = int(rng) // a C cast: toward zero
	ints = []*int64{&sp.CastTime, &sp.Power, &sp.IconColor, &sp.Icon,
		&sp.Scope, &sp.Recast, &sp.EqpRequire}
	for _, p := range ints {
		v, err := vimsSigned(r)
		if err != nil {
			return sp, err
		}
		*p = int64(v)
	}
	if sp.Name, err = vimsWString(r, 128); err != nil {
		return sp, err
	}
	if sp.Desc, err = vimsWString(r, 256); err != nil {
		return sp, err
	}
	n, err := vimsSigned(r)
	if err != nil {
		return sp, err
	}
	if n < 0 || int(n) > r.Remaining() {
		// VIArray::Init refuses it, and Read returns the error.
		return sp, fmt.Errorf("ability %d: bad sub-record count %d", sp.SpellID, n)
	}
	for i := int32(0); i < n; i++ {
		var rec AbilityRecord
		for _, p := range []*int32{&rec.P00, &rec.P04, &rec.P08, &rec.P0C} {
			if *p, err = vimsSigned(r); err != nil {
				return sp, err
			}
		}
		if rec.Name, err = vimsWString(r, 128); err != nil {
			return sp, err
		}
		sp.Records = append(sp.Records, rec)
	}
	return sp, nil
}

// abilityOrder is the map's in-order walk: the list's indices by ability id.
// The searches that stop at the first match (0x00B4, B8, CD) and the moves
// that keep the last (B5, B6) see the abilities in this order.
func (gs *GameState) abilityOrder() []int {
	order := make([]int, len(gs.ParsedSpells))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return gs.ParsedSpells[order[a]].SpellID < gs.ParsedSpells[order[b]].SpellID
	})
	return order
}

// abilityByDisplay is the first ability, in id order, whose display index
// (the server's key for it) is idx; -1 when none is.
func (gs *GameState) abilityByDisplay(idx int64) int {
	for _, i := range gs.abilityOrder() {
		if gs.ParsedSpells[i].AddedOrder == idx {
			return i
		}
	}
	return -1
}

// abilityByID is VIMap::Find: the record the id was first inserted with.
func (gs *GameState) abilityByID(id int64) int {
	for i := range gs.ParsedSpells {
		if gs.ParsedSpells[i].SpellID == id {
			return i
		}
	}
	return -1
}

// refreshHotbar rebuilds the bar from the list after the list changed: the
// bar holds pointers into it. A running cooldown follows its ability, which
// is where CLIENT keeps the lockout.
func (gs *GameState) refreshHotbar() {
	type cd struct {
		until time.Time
		dur   float32
	}
	running := map[int64]cd{}
	for i, sp := range gs.Hotbar {
		if sp != nil && gs.Cooldowns[i].After(time.Now()) {
			running[sp.SpellID] = cd{gs.Cooldowns[i], gs.CooldownDurs[i]}
		}
	}
	gs.BuildHotbar()
	gs.Cooldowns = [10]time.Time{}
	gs.CooldownDurs = [10]float32{}
	for i, sp := range gs.Hotbar {
		if sp == nil {
			continue
		}
		if c, ok := running[sp.SpellID]; ok {
			gs.Cooldowns[i], gs.CooldownDurs[i] = c.until, c.dur
		}
	}
}

// abilityReader opens a 0x00B3..0x00FF message past its opcode.
func abilityReader(msg *protocol.Message) *buffer.Reader {
	data := msg.Data()
	if len(data) < 2 {
		return buffer.NewReader(nil)
	}
	return buffer.NewReader(data[2:])
}

// handleAbilityAdd is 0x00B3 (Msg00B3, 0x00BCF3A0): the stamp, a u32 flag
// and one ability, inserted into the map. Insert keeps a record already
// there under that id. The flag shows word 721, "You have gained a new
// ability."
func (gs *GameState) handleAbilityAdd(msg *protocol.Message) {
	r := abilityReader(msg)
	stamp, err := r.ReadUint32()
	if err != nil {
		return
	}
	gs.AbilityStamp = stamp
	flag, err := r.ReadUint32()
	if err != nil {
		return
	}
	sp, err := readAbility(r)
	if err != nil {
		log.Printf("AbilityAdd: %v", err) // CLIENT inserts what it read anyway
	}
	if gs.abilityByID(sp.SpellID) < 0 {
		gs.ParsedSpells = append(gs.ParsedSpells, sp)
		gs.refreshHotbar()
	}
	log.Printf("AbilityAdd: id=%d display=%d %q flag=%d", sp.SpellID, sp.AddedOrder, sp.Name, flag)
	if flag != 0 {
		gs.addWordLine(gs.wordText(721), 255, 255, 255)
	}
}

// handleAbilityMemorize is 0x00B4 (Msg00B4, 0x00BCF0F8, read off its
// disassembly): the stamp, a u8 display index and a u8 slot — the server
// echoing the client's 0x0033. The first ability with that display index
// swaps between memorized and not and takes the slot; any other ability in
// the slot leaves it. Then "You have memorized %S." (824) or "You have
// forgotten %S." (825), and UI sound 6.
func (gs *GameState) handleAbilityMemorize(msg *protocol.Message) {
	r := abilityReader(msg)
	stamp, err := r.ReadUint32()
	if err != nil {
		return
	}
	gs.AbilityStamp = stamp
	display, err1 := r.ReadByte()
	slot, err2 := r.ReadByte()
	if err1 != nil || err2 != nil {
		return
	}
	found := gs.abilityByDisplay(int64(display))
	if found < 0 {
		log.Printf("AbilityMemorize: no ability with display index %d", display)
		return
	}
	for i := range gs.ParsedSpells {
		sp := &gs.ParsedSpells[i]
		if sp.WhereOnHotBar == int64(slot) && sp.AddedOrder != int64(display) {
			sp.OnHotBar = 0
			sp.WhereOnHotBar = -1
		}
	}
	sp := &gs.ParsedSpells[found]
	if sp.OnHotBar == 0 {
		sp.OnHotBar = 1
	} else {
		sp.OnHotBar = 0
	}
	sp.WhereOnHotBar = int64(slot)
	word := 825
	if sp.OnHotBar != 0 {
		word = 824
	}
	text := strings.Replace(gs.wordText(word), "%S", sp.Name, 1)
	log.Printf("AbilityMemorize: display=%d slot=%d %q valid=%d", display, slot, sp.Name, sp.OnHotBar)
	gs.refreshHotbar()
	gs.addWordLine(text, 255, 255, 255)
}

// handleAbilityInvalidateAll is 0x00FF (Msg00FF, 0x00BCF2D0): the stamp, then
// every ability with a display index below 0xFC (the rest are items) leaves
// the bar. The server's answer to 0x00FE.
func (gs *GameState) handleAbilityInvalidateAll(msg *protocol.Message) {
	r := abilityReader(msg)
	stamp, err := r.ReadUint32()
	if err != nil {
		return
	}
	gs.AbilityStamp = stamp
	for i := range gs.ParsedSpells {
		if sp := &gs.ParsedSpells[i]; sp.AddedOrder < 252 {
			sp.OnHotBar = 0
			sp.WhereOnHotBar = -1
		}
	}
	gs.refreshHotbar()
}

// handleAbilitySlotMove is 0x00B5 (Msg00B5, 0x00BCF5A8): the stamp, u8 from,
// u8 to. The bar entry at slot `from` moves to `to`, the valid abilities
// between closing up behind it and opening a gap in front of it.
func (gs *GameState) handleAbilitySlotMove(msg *protocol.Message) {
	r := abilityReader(msg)
	stamp, err := r.ReadUint32()
	if err != nil {
		return
	}
	gs.AbilityStamp = stamp
	f, err1 := r.ReadByte()
	t, err2 := r.ReadByte()
	if err1 != nil || err2 != nil {
		return
	}
	from, to := int64(f), int64(t)
	found := -1
	order := gs.abilityOrder()
	for _, i := range order {
		sp := &gs.ParsedSpells[i]
		if sp.OnHotBar != 0 {
			if sp.WhereOnHotBar == from {
				found = i
			}
			if from < sp.WhereOnHotBar {
				sp.WhereOnHotBar--
			}
		}
	}
	if from < to {
		to--
	}
	for _, i := range order {
		if sp := &gs.ParsedSpells[i]; sp.OnHotBar != 0 && sp.WhereOnHotBar >= to {
			sp.WhereOnHotBar++
		}
	}
	if found >= 0 {
		gs.ParsedSpells[found].WhereOnHotBar = to
	}
	gs.refreshHotbar()
}

// handleAbilityDisplayMove is 0x00B6 (Msg00B6, 0x00BCF730): the stamp, u8
// from, u8 to — the same move on the display index (the spell book's order),
// among the abilities below 0xFC.
func (gs *GameState) handleAbilityDisplayMove(msg *protocol.Message) {
	r := abilityReader(msg)
	stamp, err := r.ReadUint32()
	if err != nil {
		return
	}
	gs.AbilityStamp = stamp
	f, err1 := r.ReadByte()
	t, err2 := r.ReadByte()
	if err1 != nil || err2 != nil {
		return
	}
	from, to := int64(f), int64(t)
	found := -1
	order := gs.abilityOrder()
	for _, i := range order {
		sp := &gs.ParsedSpells[i]
		if sp.AddedOrder == from {
			found = i
		}
		if from < sp.AddedOrder && sp.AddedOrder < 252 {
			sp.AddedOrder--
		}
	}
	for _, i := range order {
		if sp := &gs.ParsedSpells[i]; sp.AddedOrder >= to && sp.AddedOrder < 252 {
			sp.AddedOrder++
		}
	}
	if found >= 0 {
		gs.ParsedSpells[found].AddedOrder = to
	}
}

// handleAbilitySetP14 is 0x00FD (Msg00FD, 0x00BCF8B0): the stamp, a u32
// ability id and a u32 value for the ability's +0x14 (Go: ShowHide); 0 also
// takes it off the bar.
func (gs *GameState) handleAbilitySetP14(msg *protocol.Message) {
	r := abilityReader(msg)
	stamp, err := r.ReadUint32()
	if err != nil {
		return
	}
	gs.AbilityStamp = stamp
	id, err1 := r.ReadUint32()
	v, err2 := r.ReadUint32()
	if err1 != nil || err2 != nil {
		return
	}
	i := gs.abilityByID(int64(int32(id)))
	if i < 0 {
		return
	}
	sp := &gs.ParsedSpells[i]
	sp.ShowHide = int64(int32(v))
	if v == 0 {
		sp.OnHotBar = 0
		sp.WhereOnHotBar = -1
		gs.refreshHotbar()
	}
}

// handleAbilityRemove is 0x00B8 (Msg00B8, 0x00BCF468): the stamp and a
// zigzag display index; the first ability with it is erased.
func (gs *GameState) handleAbilityRemove(msg *protocol.Message) {
	r := abilityReader(msg)
	stamp, err := r.ReadUint32()
	if err != nil {
		return
	}
	gs.AbilityStamp = stamp
	idx, err := vimsSigned(r)
	if err != nil {
		return
	}
	i := gs.abilityByDisplay(int64(idx))
	if i < 0 {
		return
	}
	log.Printf("AbilityRemove: display=%d %q", idx, gs.ParsedSpells[i].Name)
	gs.ParsedSpells = append(gs.ParsedSpells[:i], gs.ParsedSpells[i+1:]...)
	gs.refreshHotbar()
}



// addWordLine is VIWndGame::AddText: one line in the chat window, in a colour.
func (gs *GameState) addWordLine(text string, r, g, b uint8) {
	gs.ChatLog = append(gs.ChatLog, ChatMessage{Text: text, Channel: ChatSystem, ColorR: r, ColorG: g, ColorB: b})
	if len(gs.ChatLog) > 200 {
		gs.ChatLog = gs.ChatLog[len(gs.ChatLog)-200:]
	}
}
