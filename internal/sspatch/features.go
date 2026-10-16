package sspatch

import "strings"

// The addresses S2 hooks, as the July note found them and as the live S2
// (ss-patch-60, 2026-10-15) still writes them. Retail's instruction at each
// is in the comment; a reader that does not find the shape it expects
// leaves that feature as retail has it.
const (
	// SUPPORT, at its fixed base.
	getTintColor     = 0x004294E8 // VICSpriteCust::GetTintColor: lui v0, %hi($Tints) ...
	setArmorSlotSet  = 0x00422574 // SetArmorSlot+0x44: slti v0, s1, 9 — the armour set clamp
	setArmorSlotTint = 0x00422588 // SetArmorSlot+0x58: movz s3, zero, v1 — the tint clamp
	getHelm          = 0x004294D0 // VICSpriteCust::GetHelm
	getRobeTexture   = 0x00429480 // VICSpriteCust::GetRobeTexture: slti v0, a0, 4
	findTyped        = 0x003E43A8 // VIDictionary::FindTyped(id, type, &out)
	memCardReadFile  = 0x003E8098 // VIMemCardReadFile(name, buf, size)

	// CLIENT, by offset from its base.
	clientArtRead      = 0x0C904 // jal VIMemCardReadFile("addart.csf", buf, 16384)
	clientUnknownCmd   = 0x1BAA0 // addiu a0, s2, 4: the string-826 branch for an unknown /command
	clientOpcodeSwitch = 0x1C4A8 // lhu a1, 32(sp): the head of the opcode switch
	clientDamage       = 0x1E424 // lw a1, 2268(sp), in the DamageIndication handler
	clientWorldHi      = 0x11010 // lui v0, %hi(the world table)
	clientWorldLo      = 0x11018 // addiu v0, v0, %lo(the world table)
	clientAuctionLevel = 0x5CFE4 // li a1, 50: the Auctions window's Max. Level
)

// RGBA is one tint, as the table holds it: R, G, B, A bytes.
type RGBA [4]uint8

// World is one entry of the world table: what CLIENT knows a world by.
type World struct {
	Name   string // "tunaria"
	ESF    string // "data\tunaria.esf"
	NameID int    // its WORDS.TXT string
	MaxCol int    // the zone grid's last column and row
	MaxRow int
}

// Features is what a patch changes about the client, for the parts of S2
// this client ports. A zero field is retail's.
type Features struct {
	Tints            []RGBA            // the table GetTintColor returns into
	TintClampRemoved bool              // SetArmorSlot keeps a tint past the table
	ArmorSetLimit    int               // SetArmorSlot turns a set at or past it into 0
	HelmRemap        map[int]int       // GetHelm: armour set → the set whose helm it wears
	RobeLimit        int               // GetRobeTexture's robes are 0 .. RobeLimit-1
	RobeArt          map[int][2]uint32 // a robe past retail's four: its (chest, legs) DictIDs
	ArtFile          string            // the card file CLIENT reads its added art from
	PlayerSearch     bool              // the opcode switch takes 0x1500 / 0x1501
	DamageRGB        bool              // DamageIndication reads three trailing colour bytes
	CommandsToServer bool              // an unknown /command goes to the server as say
	Worlds           []World           // the world table
	AuctionMaxLevel  int               // the Auctions window opens on this Max. Level
}

// Read applies the patch at delta 0 and reads its features.
func Read(p *Patch) *Features {
	img := p.Apply(0)
	f := &Features{}
	f.readTints(img)
	if w, ok := img.Word(setArmorSlotSet); ok && w>>16 == 0x2A22 { // slti v0, s1, N
		f.ArmorSetLimit = int(int16(w))
	}
	if w, ok := img.Word(setArmorSlotTint); ok && w == 0 {
		f.TintClampRemoved = true
	}
	f.readHelm(img)
	f.readRobesAndArt(img)
	f.PlayerSearch = img.jalsIntoCave(ClientBase + clientOpcodeSwitch)
	f.DamageRGB = img.jalsIntoCave(ClientBase + clientDamage)
	if w, ok := img.Word(ClientBase + clientUnknownCmd); ok && w>>16 == 0x1000 { // b: beq zero, zero
		f.CommandsToServer = true
	}
	if w, ok := img.Word(ClientBase + clientAuctionLevel); ok && w>>16 == 0x2405 { // li a1, N
		f.AuctionMaxLevel = int(int16(w))
	}
	f.readWorlds(img)
	return f
}

// readTints: GetTintColor becomes `return table + tint*4` — lui and ori (or
// addiu) of the table into one register, then sll a0, a0, 2 — and the table
// runs to the end of the record that holds it.
func (f *Features) readTints(img *Image) {
	hi, ok1 := img.Word(getTintColor)
	lo, ok2 := img.Word(getTintColor + 4)
	sll, ok3 := img.Word(getTintColor + 8)
	if !ok1 || !ok2 || !ok3 || sll != 0x00042080 {
		return
	}
	table, ok := luiPair(hi, lo)
	if !ok {
		return
	}
	end, ok := img.recordEnd(table)
	if !ok {
		return
	}
	for a := table; a+4 <= end; a += 4 {
		b, _ := img.Bytes(a, 4)
		f.Tints = append(f.Tints, RGBA{b[0], b[1], b[2], b[3]})
	}
}

// readHelm: GetHelm jumps to a cave that does
//
//	li v0, A; bne a0, v0, +2; nop; li a0, B
//
// before indexing $Helm: the slot at byte offset A reads B's. A and B are
// byte offsets of four-byte slots, so set A/4 wears set B/4's helm.
func (f *Features) readHelm(img *Image) {
	cave, ok := img.jumpTarget(getHelm)
	if !ok {
		return
	}
	var w [4]uint32
	for i := range w {
		if w[i], ok = img.Word(cave + uint32(i)*4); !ok {
			return
		}
	}
	if w[0]>>16 != 0x2402 || w[1]>>16 != 0x1482 || w[2] != 0 || w[3]>>16 != 0x2404 {
		return
	}
	f.HelmRemap = map[int]int{int(int16(w[0])) / 4: int(int16(w[3])) / 4}
}

// readRobesAndArt reads two hooks that work together. GetRobeTexture jumps
// to a cave whose `sltiu v0, a0, N` after retail's `sltiu v0, a0, 4` is the
// new robe count, and whose lui/ori into t0 is a table of (chest, legs)
// raster ids, eight bytes a robe from robe 4. CLIENT's addart.csf read jumps
// to a cave that reads its own art file instead (the a0 of the first
// VIMemCardReadFile), parses it, and fills that table with
// FindTyped(DictID, 1, &slot) — so each call's a1 is the DictID of the slot
// a3 names.
func (f *Features) readRobesAndArt(img *Image) {
	var table uint32
	if cave, ok := img.jumpTarget(getRobeTexture); ok {
		seen4 := false
		img.walk(cave, 64, func(pc, w uint32, r *regs) bool {
			if w>>16 == 0x2C82 { // sltiu v0, a0, N
				if n := int(int16(w)); n == 4 {
					seen4 = true
				} else if seen4 && f.RobeLimit == 0 {
					f.RobeLimit = n
				}
			}
			if t, ok := r.get(8); ok && table == 0 && t >= caveLo && t < caveLo+caveSize { // t0
				table = t
			}
			return true
		})
	}
	call, ok := img.Word(ClientBase + clientArtRead)
	if !ok || call>>26 != 3 {
		return
	}
	cave := jTarget(ClientBase+clientArtRead, call)
	if cave < caveLo || cave >= caveLo+caveSize {
		return
	}
	img.walk(cave, 128, func(pc, w uint32, r *regs) bool {
		if w>>26 != 3 {
			return true
		}
		switch jTarget(pc, w) {
		case memCardReadFile:
			if f.ArtFile == "" {
				if a0, ok := r.after(4); ok {
					f.ArtFile, _ = img.CString(a0)
				}
			}
		case findTyped:
			id, ok1 := r.after(5)
			slot, ok2 := r.after(7)
			if ok1 && ok2 && table != 0 && f.RobeLimit > 4 && slot >= table && slot < table+uint32(f.RobeLimit-4)*8 {
				robe := 4 + int(slot-table)/8
				if f.RobeArt == nil {
					f.RobeArt = map[int][2]uint32{}
				}
				pair := f.RobeArt[robe]
				pair[(slot-table)%8/4] = id
				f.RobeArt[robe] = pair
			}
		}
		return true
	})
}

// readWorlds: six lui/addiu pairs in CLIENT point at the world table; the
// first is read. Each entry is eight words — name, ESF path, the name's
// string id, the zone grid's column and row bounds, and a data index — and
// the table runs to the end of its record.
func (f *Features) readWorlds(img *Image) {
	hi, ok1 := img.Word(ClientBase + clientWorldHi)
	lo, ok2 := img.Word(ClientBase + clientWorldLo)
	if !ok1 || !ok2 {
		return
	}
	table, ok := luiPair(hi, lo)
	if !ok {
		return
	}
	end, ok := img.recordEnd(table)
	if !ok {
		return
	}
	for a := table; a+32 <= end; a += 32 {
		var w [8]uint32
		for i := range w {
			w[i], _ = img.Word(a + uint32(i)*4)
		}
		name, ok1 := img.CString(w[0])
		esf, ok2 := img.CString(w[1])
		if !ok1 || !ok2 {
			return
		}
		f.Worlds = append(f.Worlds, World{Name: name, ESF: esf, NameID: int(w[2]), MaxCol: int(w[4]), MaxRow: int(w[6])})
	}
}

// ESFName is a world's ESF as a file name on the disc: "data2\Rathe.esf"
// is "RATHE.ESF".
func (w World) ESFName() string {
	name := w.ESF
	if i := strings.LastIndexAny(name, `\/`); i >= 0 {
		name = name[i+1:]
	}
	return strings.ToUpper(name)
}

// jalsIntoCave says whether the patch put a call into its arena at a.
func (img *Image) jalsIntoCave(a uint32) bool {
	w, ok := img.Word(a)
	if !ok || w>>26 != 3 {
		return false
	}
	t := jTarget(a, w)
	return t >= caveLo && t < caveLo+caveSize
}

// jumpTarget is where a hook's j or jal at a goes.
func (img *Image) jumpTarget(a uint32) (uint32, bool) {
	w, ok := img.Word(a)
	if !ok || (w>>26 != 2 && w>>26 != 3) {
		return 0, false
	}
	return jTarget(a, w), true
}

func jTarget(pc, w uint32) uint32 {
	return (pc+4)&0xF0000000 | (w&0x03FFFFFF)<<2
}

// luiPair reads `lui r, hi` followed by `ori r, r, lo` or `addiu r, r, lo`.
func luiPair(hi, lo uint32) (uint32, bool) {
	r := hi >> 16 & 31
	if hi>>26 != 15 || lo>>21&31 != r || lo>>16&31 != r {
		return 0, false
	}
	switch lo >> 26 {
	case 13: // ori
		return hi<<16 | lo&0xFFFF, true
	case 9: // addiu
		return hi<<16 + uint32(int32(int16(lo))), true
	}
	return 0, false
}

// regs is what walk knows of the registers: the ones that hold a constant.
type regs struct {
	val   [32]uint32
	known [32]bool
	// delay holds the registers as they stand after a jal's delay slot.
	delay *regs
}

func (r *regs) get(n uint32) (uint32, bool) {
	if n == 0 {
		return 0, true
	}
	return r.val[n], r.known[n]
}

// after is a register as the call sees it: after the delay slot.
func (r *regs) after(n uint32) (uint32, bool) {
	if r.delay != nil {
		return r.delay.get(n)
	}
	return r.get(n)
}

func (r *regs) set(n, v uint32) {
	if n != 0 {
		r.val[n], r.known[n] = v, true
	}
}

func (r *regs) forget(n uint32) { r.known[n] = false }

// step folds one instruction into the registers. Only the instructions that
// build constants are followed (lui, ori, addiu, and the register moves
// addu/daddu/or with $zero); anything else forgets what it writes.
func (r *regs) step(w uint32) {
	op, rs, rt, rd := w>>26, w>>21&31, w>>16&31, w>>11&31
	imm := w & 0xFFFF
	switch op {
	case 15: // lui
		r.set(rt, imm<<16)
	case 13: // ori
		if v, ok := r.get(rs); ok {
			r.set(rt, v|imm)
		} else {
			r.forget(rt)
		}
	case 9: // addiu
		if v, ok := r.get(rs); ok {
			r.set(rt, v+uint32(int32(int16(imm))))
		} else {
			r.forget(rt)
		}
	case 0: // SPECIAL
		switch w & 63 {
		case 0x21, 0x2D, 0x25: // addu, daddu, or
			a, oka := r.get(rs)
			b, okb := r.get(rt)
			if oka && okb {
				if w&63 == 0x25 {
					r.set(rd, a|b)
				} else {
					r.set(rd, a+b)
				}
			} else {
				r.forget(rd)
			}
		case 8, 0x0C, 0x0D: // jr, syscall, break: write nothing
		default:
			r.forget(rd)
		}
	case 3: // jal writes ra
		r.forget(31)
	case 8, 10, 11, 12, 14, 24, 25, // the other immediates, daddi(u)
		16, 17, 18, // coprocessor moves into a GPR
		26, 27, 30, 32, 33, 34, 35, 36, 37, 38, 39, 55: // loads, lq, ld
		r.forget(rt)
	case 28: // MMI
		r.forget(rd)
	}
}

// walk runs fn over straight-line code from a, in address order — branches
// are not followed, which is what the hooks' short caves need — with the
// registers as the constants so far leave them. A jal clobbers the caller's
// registers once fn has seen it (with its delay slot in r.after). It stops
// at jr ra, at a j, at an unpatched word, after n words, or when fn says so.
func (img *Image) walk(a uint32, n int, fn func(pc, w uint32, r *regs) bool) {
	r := &regs{}
	for i := 0; i < n; i++ {
		pc := a + uint32(i)*4
		w, ok := img.Word(pc)
		if !ok {
			return
		}
		if w>>26 == 3 { // jal: the delay slot runs first
			ds, ok := img.Word(pc + 4)
			if !ok {
				return
			}
			after := *r
			after.step(ds)
			r.delay = &after
			if !fn(pc, w, r) {
				return
			}
			*r = after
			r.delay = nil
			for _, n := range []uint32{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 24, 25, 31} {
				r.forget(n) // what a call may change
			}
			i++ // the delay slot is done
			continue
		}
		if !fn(pc, w, r) {
			return
		}
		r.step(w)
		if w == 0x03E00008 || w>>26 == 2 { // jr ra, j
			if ds, ok := img.Word(pc + 4); ok {
				fn(pc+4, ds, r)
			}
			return
		}
	}
}
