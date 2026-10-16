package game

import (
	"encoding/binary"
	"log"
	"math"
	"sort"
	"time"

	"github.com/eqoa/client/internal/protocol"
)

// The player's own combat decisions, off CLIENT (decomp/modules/client/src/
// viclient.cpp and game.h; addresses rather than lines, since that file is
// still being written): when the attack button sends 0x000F, the swing the
// client plays itself and the 0x00D8 it sends when it cannot, which target a
// key picks and the 0x0010 that goes with it, what interacting sends, the
// emotes, and following. Tags as everywhere: [V] the function is byte-exact
// (decomp/modules/client/src/MATCHED.txt), [A] written but not matched, or
// read off the disassembly.

// noTarget is CLIENT's "no target": m_target = 0xFFFFFFFF (lui+ori). Go keeps
// 0 in Engine.TargetObjectID for none; wireTarget converts.
const noTarget = 0xFFFFFFFF

// targeting is the part of VIClient these functions keep on the Engine.
// Engine.CombatEngaged is m_autoAttack (+0x71E4) and Engine.TargetObjectID
// m_target (+0xC140); the 0x40 update carries both (bytes 36 and 37-40).
type targeting struct {
	// AutoAttackToggle is VIGameOptions::m_x95, the options' auto-attack mode.
	// SetDefaults zeroes it, and then the attack button attacks only while it
	// is held: ToggleAutoAttack sets m_autoAttack on the press and
	// MaybeStopAutoAttack clears it on the release (VIWndGame's OnKeyDown and
	// OnKeyUp, keys 32 and 134). Set, a press toggles it.
	AutoAttackToggle bool

	nextSwing   int64 // m_p071E8: the game time the next swing is due; 0 before the first
	lastSwingFX int64 // m_p071F0: when AutoAttackTick last played a swing's effect

	following bool   // m_p00078: following a group member
	followID  uint32 // m_p0007C: the one followed

	cycleDir int // CycleTarget's static (data_00CCE5A4): the list the last cycle used

	clock0 time.Time
	clock  func() int64 // tests: the game clock instead of the wall clock
}

// CombatState is the part the GameState handlers touch.
type CombatState struct {
	// TargetInfoValid is m_targetInfoValid (+0x2BB64): 0x0010's reply to the
	// current request sets it (Msg0010, 0x00BD9D60 [V]); every change of
	// target clears it, so the target window shows nothing stale.
	TargetInfoValid bool

	// stopAutoAttack and stopFollow are a handler's StopAutoAttack and its
	// m_p00078 = 0, for the Engine, which owns both (0x077A; Msg005E).
	stopAutoAttack bool
	stopFollow     bool
}

// gameMs is m_p07568, the game clock in milliseconds. It is never 0, the
// value m_p071E8 holds before the first swing.
func (e *Engine) gameMs() int64 {
	if e.clock != nil {
		return e.clock()
	}
	if e.clock0.IsZero() {
		e.clock0 = time.Now()
	}
	return time.Since(e.clock0).Milliseconds() + 1
}

// wireTarget is a Go target id as CLIENT holds it: 0 is noTarget.
func wireTarget(id uint32) uint32 {
	if id == 0 {
		return noTarget
	}
	return id
}

// goTarget is the other way round.
func goTarget(id uint32) uint32 {
	if id == noTarget {
		return 0
	}
	return id
}

// target is m_target.
func (e *Engine) target() uint32 { return wireTarget(e.TargetObjectID) }

// combatEntity is what these functions read of a VIEntity: the entity and
// whether it is a VICreatureEntity (type 0x82; Virtual3(130) says so).
type combatEntity struct {
	ent      *Entity
	creature bool
}

// findEntity is VIClient::FindEntity: the entity in m_entities with that id,
// the player's own included (the self id's slot is gs.Player). An entity Go
// holds with no slot (offline mode makes them) counts as a creature.
func (gs *GameState) findEntity(id uint32) (combatEntity, bool) {
	if id == noTarget {
		return combatEntity{}, false
	}
	for i := range gs.entitySlots {
		s := &gs.entitySlots[i]
		if !s.used || s.id != id {
			continue
		}
		if id == gs.PlayerObjectID {
			return combatEntity{&gs.Player, s.kind == entityTypeCreature}, true
		}
		if ent := gs.Entities[id]; ent != nil {
			return combatEntity{ent, s.kind == entityTypeCreature}, true
		}
		return combatEntity{}, false
	}
	if id == gs.PlayerObjectID && id != 0 {
		return combatEntity{&gs.Player, true}, true
	}
	if ent := gs.Entities[id]; ent != nil {
		return combatEntity{ent, true}, true
	}
	return combatEntity{}, false
}

// groupCount is GetP2BE4C (0x00BD7AD0 [V]): the roster's size while grouped.
func (gs *GameState) groupCount() int {
	if !gs.GroupActive {
		return 0
	}
	return len(gs.GroupMembers)
}

// combatSendU32 is SendOpU32 / SendOpInt (0x00BE63A8, 0x00BE4C28 [V]): the
// opcode and one raw u32.
func (gs *GameState) combatSendU32(op protocol.GameOpcode, v uint32) {
	gs.socialSend(op, binary.LittleEndian.AppendUint32(nil, v))
}

// red is VIColor32F(1, 0.25, 0.25, 1), the colour of every refusal here.
func (gs *GameState) combatRefuse(word int) {
	gs.addWordLine(gs.wordText(word), 255, 64, 64)
}

// --- the target -------------------------------------------------------------

// SetTarget is the tail SelectTarget (0x00BC9730 [V]), CycleTarget
// (0x00BC8B98 [A]) and TargetCentre (0x00BC92B0 [A]) share: the new m_target,
// auto-attack off, the target info invalid, and 0x0010 asking for it
// (SendOpIntInt(16, m_p2BB68, m_target): two raw u32s, the first always 0 in
// every capture). id 0 is no target, sent as 0xFFFFFFFF.
func (e *Engine) SetTarget(id uint32) {
	e.TargetObjectID = goTarget(id)
	e.CombatEngaged = false
	e.State.TargetInfoValid = false
	e.SendTarget(wireTarget(id))
}

// ClearTarget is VIClient::ClearTarget (0x00BD5F38 [V]): no target, and no
// message — the server sees it in the next 0x40 (bytes 37-40).
func (e *Engine) ClearTarget() {
	e.State.TargetInfoValid = false
	e.TargetObjectID = 0
}

// SelectTarget is VIClient::SelectTarget (0x00BC9730 [V]), the keyboard's
// 1-6: 1 the player, 2-5 the group roster, 6 the pet. A roster slot holding
// the player gives the next one instead (so with the player first, 2 and 3
// both pick the second member), and one past the roster's size is no target.
func (e *Engine) SelectTarget(which int) {
	gs := e.State
	id := uint32(noTarget)
	n := gs.groupCount()
	switch which {
	case 1:
		id = gs.PlayerObjectID
	case 2, 3, 4, 5:
		if which <= n {
			m := gs.GroupMembers
			if m[which-2].ObjectID == gs.PlayerObjectID {
				id = m[which-1].ObjectID
			} else {
				id = m[which-2].ObjectID
			}
		}
	case 6:
		id = gs.PetID
	}
	e.SetTarget(id)
}

// TargetView is what CycleTarget reads off m_camera (VIClient+0x190): its
// position (+0x1AC) and direction (+0x198), whether its frustum (+0x2C0)
// holds a point, and a point's clip-space x over w through m_viewProj
// (+0x280) — the screen's centre is 0 and its edges ±1.
type TargetView struct {
	Pos, Dir [3]float32
	Visible  func(x, y, z float32) bool
	ClipX    func(x, y, z float32) float32
}

// targetCandidate is one of CycleTarget's 8-byte records.
type targetCandidate struct {
	id    uint32
	score float32
}

// CycleTarget is VIClient::CycleTarget (0x00BC8B98, [A]: read off the
// disassembly, not yet written in the decomp), the target keys (Tab and pad
// code 142 cycle the scene, ` and code 140 the party).
//
// The party list is the player, the roster's other members and the pet, in
// that order. The scene list is every entity in m_entities but the player,
// the pet and the roster, and a creature whose name colour (+0x256) is 3; it
// must be targetable (+0x18 bit 0), inside 200 of the camera, and in the
// frustum unless it is within sqrt(15) of the camera and less than ~49
// degrees off its direction (in XZ, cos > 0.65). That close one scores 0;
// otherwise the score is 0.35 * distance / 50, plus the square of its screen
// x once it is 5 or more away; a corpse (m_state 14) scores 2 more. The list
// is sorted by score (qsort, CompareY).
//
// The next target follows the current one in the list, wrapping; the first
// entry when the current is not in it or the last cycle used the other list.
// Auto-attack goes off even when the target stays, and 0x0010 always goes
// out — with 0xFFFFFFFF when the list is empty.
func (e *Engine) CycleTarget(party bool, v *TargetView) {
	gs := e.State
	dir := 0
	if party {
		dir = 1
	}
	self := gs.PlayerObjectID
	var list []targetCandidate
	if party {
		list = append(list, targetCandidate{id: self})
		for i := 0; i < gs.groupCount(); i++ {
			if id := gs.GroupMembers[i].ObjectID; id != self {
				list = append(list, targetCandidate{id: id})
			}
		}
		if gs.PetID != noTarget && gs.PetID != 0 {
			list = append(list, targetCandidate{id: gs.PetID})
		}
	} else if v != nil {
		list = gs.sceneCandidates(v)
		sort.SliceStable(list, func(i, j int) bool { return list[i].score < list[j].score })
	}

	at := len(list)
	if e.cycleDir == dir {
		cur := e.target()
		for i := range list {
			if list[i].id == cur {
				at = i
				break
			}
		}
	}
	next := uint32(noTarget)
	switch {
	case len(list) == 0:
	case at == len(list):
		next = list[0].id
	default:
		next = list[(at+1)%len(list)].id
	}
	e.CombatEngaged = false
	e.TargetObjectID = goTarget(next)
	gs.TargetInfoValid = false
	e.SendTarget(next)
	e.cycleDir = dir
}

// sceneCandidates is CycleTarget's scan of m_entities, in slot order.
func (gs *GameState) sceneCandidates(v *TargetView) []targetCandidate {
	var list []targetCandidate
	self, pet := gs.PlayerObjectID, gs.PetID
	for ch := range gs.entitySlots {
		s := &gs.entitySlots[ch]
		if !s.used || s.id == self || s.id == pet {
			continue
		}
		ent := gs.Entities[s.id]
		if ent == nil {
			continue
		}
		member := false
		for i := 0; i < gs.groupCount(); i++ {
			if gs.GroupMembers[i].ObjectID == s.id {
				member = true
				break
			}
		}
		if member {
			continue
		}
		corpse := false
		if s.kind == entityTypeCreature {
			if ent.NameColor == 3 {
				continue
			}
			corpse = ent.Animation == 14
		}
		dx := ent.X - v.Pos[0]
		dy := ent.Y - v.Pos[1]
		dz := ent.Z - v.Pos[2]
		d2 := dx*dx + dy*dy + dz*dz
		close := false
		if d2 < 15 {
			ax, az := normXZ(dx, dz)
			bx, bz := normXZ(v.Dir[0], v.Dir[2])
			close = ax*bx+az*bz > 0.65
		}
		if !close && (v.Visible == nil || !v.Visible(ent.X, ent.Y, ent.Z)) {
			continue
		}
		if ent.HPFlag&1 == 0 {
			continue
		}
		if !(d2 < 40000) {
			continue
		}
		var score float32
		if !close {
			score = float32(math.Sqrt(float64(d2))) / 50 * 0.35
			if d2 >= 25 && v.ClipX != nil {
				x := v.ClipX(ent.X, ent.Y, ent.Z)
				score += x * x
			}
		}
		if corpse {
			score += 2
		}
		list = append(list, targetCandidate{id: s.id, score: score})
	}
	return list
}

// SceneTargetOrder is CycleTarget's scene list, its ids in score order (for
// the Go client's reverse cycle, which CLIENT does not have).
func (gs *GameState) SceneTargetOrder(v *TargetView) []uint32 {
	if v == nil {
		return nil
	}
	list := gs.sceneCandidates(v)
	sort.SliceStable(list, func(i, j int) bool { return list[i].score < list[j].score })
	ids := make([]uint32, len(list))
	for i := range list {
		ids[i] = list[i].id
	}
	return ids
}

// normXZ is VIVect3::Normalize of (x, 0, z).
func normXZ(x, z float32) (float32, float32) {
	m := float32(math.Sqrt(float64(x*x + z*z)))
	if m == 0 {
		return 0, 0
	}
	return x / m, z / m
}

// SetAssistTarget is Msg007B's own target change (0x007B, an id other than
// 0xFFFFFFFF): m_target and 0x0010, and unlike SetTarget it leaves
// auto-attack and the target info as they were.
func (e *Engine) SetAssistTarget(id uint32) {
	e.TargetObjectID = goTarget(id)
	e.SendTarget(wireTarget(id))
}

// CastAbility is Send0032 (0x00BCBEE8 [V]) for a hotbar slot: the ability
// stamp, the slot and the current target (0xFFFFFFFF with none).
func (e *Engine) CastAbility(slot uint8) {
	e.SendCast(slot, e.target())
}

// --- auto-attack ------------------------------------------------------------

// ToggleAutoAttack is VIClient::ToggleAutoAttack (0x00BD59E8 [V]), the attack
// button's press. The target must be in m_entities. A corpse (+0x18 bit 1)
// is looted instead (0x0015, while the HUD is up: VIWndGame+0x5C, which the
// Go client never hides); otherwise it must be a creature without the
// not-attackable flag (0x80). 0x000F goes out only when the next swing is
// due, which holds the button's repeats to the swing's own pace — the
// captures' 0x000F are 7.6 and 10.3 s apart over taps every 0.25-2 s, with
// the 6 s swing of the bard holding them. CLIENT does not face the target
// here, nor walk to it: it sends at any range.
func (e *Engine) ToggleAutoAttack() {
	gs := e.State
	tgt := e.target()
	c, ok := gs.findEntity(tgt)
	if !ok {
		return
	}
	if c.ent.HPFlag&2 != 0 {
		gs.combatSendU32(protocol.OpcodeLootCorpse, tgt)
		return
	}
	if !c.creature || c.ent.NPCFlags&0x80 != 0 {
		return
	}
	if !e.AutoAttackToggle || !e.CombatEngaged {
		now := e.gameMs()
		if e.nextSwing <= now || e.nextSwing == 0 {
			e.nextSwing = now
			if tgt != noTarget {
				e.SendAttack(tgt)
			}
		}
	}
	if !e.AutoAttackToggle {
		e.CombatEngaged = true
	} else {
		e.CombatEngaged = !e.CombatEngaged
	}
}

// MaybeStopAutoAttack is VIClient::MaybeStopAutoAttack (0x00BD5B30 [V]): the
// attack button's release, and every change of the game window's mode
// (func_00C50220: loot, merchant, dialog, bank and panel windows opening or
// closing). It does nothing in toggle mode.
func (e *Engine) MaybeStopAutoAttack() {
	if !e.AutoAttackToggle {
		e.CombatEngaged = false
	}
}

// StopAutoAttack is VIClient::StopAutoAttack [V]: the main menu or a panel
// opening (func_00C55A20), 0x077A and my death (Msg005E). No message.
func (e *Engine) StopAutoAttack() {
	e.CombatEngaged = false
}

// Swing kinds, GetSwing's result (0x00CBDEF8 [A]). VISwing::Effect
// (0x00CBE0A8 [A]) names a spell effect for each and VISwing::Delay
// (0x00CBE190 [A]) the time to the next swing:
//
//	kind  effect   delay   kind  effect   delay
//	0     Attack1  3000    6     Attack3  4000
//	1     Attack1  3500    7     Attack3  5000
//	2     Attack3  4000    8     Range1   3500
//	3     Attack3  5000    9     Range2   5000
//	4     Attack2  4000    10    Range3   5000
//	5     Attack4  6000    11    Range4   5000
//	12    none     3000 (and anything past it)
const swingNone = 12

// getSwing is GetSwing (0x00CBDEF8 [A]): the swing a class makes at a level
// with what is worn in equip slots 14 (primary), 15 (secondary), 16 (two
// hands), 18 (ranged), 17 (shield), 19 and 20 (held). A two-hander or a bow
// beside a hand weapon makes kind 0; a bow is Range1 under level 20, Range2
// under 35 and Range4 after; a held item in 19 makes no swing.
func getSwing(cls, level int, b14, b15, b16, b18, b17, b19, b20 bool) int {
	if b16 {
		if b14 || b15 || b18 {
			return 0
		}
	} else if b18 && (b14 || b15) {
		return 0
	}
	// 0x00CBDF64: the low-level tail every class shares.
	low := func() int {
		switch {
		case b16:
			return 1
		case b19:
			return swingNone
		}
		return 0
	}
	ranged := func() int {
		switch {
		case level < 20:
			return 8
		case level < 35:
			return 9
		}
		return 11
	}
	// 0x00CBE074 and 0x00CBE020: the shield and held-item tails.
	mid := func() int {
		switch {
		case b17:
			return 0
		case !b20:
			return 4
		}
		return 0
	}
	high := func() int {
		switch {
		case b19:
			return swingNone
		case b17:
			return 2
		case !b20:
			return 5
		}
		return 2
	}
	switch cls {
	case 7, 8, 9, 10, 11, 12, 13, 14: // DRD, SHA, CL, MAG, NEC, ENC, WIZ, ALC
		if b18 {
			return 8
		}
		return low()
	case 2, 3: // PAL, SK
		switch {
		case b18:
			return ranged()
		case level < 20:
			return low()
		case b16 && level < 35:
			return 3
		case b16:
			return 7
		case b19:
			return swingNone
		}
		return 2
	case 0, 1, 4: // WAR, RAN, MNK
		switch {
		case b18:
			return ranged()
		case level < 20:
			return low()
		case level < 35:
			if b16 {
				return 3
			}
			if b19 {
				return swingNone
			}
			return mid()
		case b16:
			return 7
		}
		return high()
	case 5, 6: // BRD, RGE
		switch {
		case b18:
			return ranged()
		case level < 20:
			return low()
		case b16:
			return 1
		case level < 35:
			if b19 {
				return swingNone
			}
			return mid()
		}
		return high()
	}
	return 0
}

// swingEffectName is VISwing::Effect's table: the name it hashes
// (VIHashResourceID) into a spell effect id.
func swingEffectName(kind int) string {
	switch kind {
	case 0, 1:
		return "Attack1"
	case 4:
		return "Attack2"
	case 2, 3, 6, 7:
		return "Attack3"
	case 5:
		return "Attack4"
	case 8:
		return "Range1"
	case 9:
		return "Range2"
	case 10:
		return "Range3"
	case 11:
		return "Range4"
	}
	return ""
}

// swingEffect is VISwing::Effect: the spell effect id, or 0.
func swingEffect(kind int) uint32 {
	if n := swingEffectName(kind); n != "" {
		return uint32(viHashResourceID(n))
	}
	return 0
}

// swingDelay is VISwing::Delay in milliseconds.
func swingDelay(kind int) int64 {
	switch kind {
	case 1, 8:
		return 3500
	case 2, 4, 6:
		return 4000
	case 3, 7, 9, 10, 11:
		return 5000
	case 5:
		return 6000
	}
	return 3000
}

// attackAnim is AttackAnim (0x00CC5190 [A]): the VICSprite action a weapon's
// attack type swings with, the alternate one for the second hand where there
// is one.
func attackAnim(attackType int64, alt bool) int32 {
	pick := func(main, off int32) int32 {
		if alt {
			return off
		}
		return main
	}
	switch attackType {
	case 1:
		return pick(25, 24)
	case 2:
		return 26
	case 3:
		return pick(41, 40)
	case 4:
		return 42
	case 5:
		return pick(28, 27)
	case 6:
		return 29
	case 7:
		return 31
	case 8:
		return pick(35, 32)
	case 9:
		return 33
	case 10:
		return 36
	}
	return 23
}

// equippedSwing is AutoAttackTick's walk of the inventory: which of equip
// slots 14-20 are worn, the first swinging item's index (slots 14, 16, 18)
// and the animation of the last one found (23, bare hands, without one).
func (gs *GameState) equippedSwing() (worn [21]bool, slot int, anim int32) {
	slot, anim = -1, 23
	for i, it := range gs.Inventory() {
		e := it.EquipLocation
		if e < 14 || e > 20 {
			continue
		}
		worn[e] = true
		if e == 14 || e == 16 || e == 18 {
			anim = attackAnim(it.AttackType, false)
			if slot == -1 {
				slot = i
			}
		}
	}
	return
}

// Swing is the swing AutoAttackTick would make now: its kind, the effect id
// (0 for none) and the delay to the next.
func (e *Engine) Swing() (kind int, effect uint32, delay int64) {
	gs := e.State
	w, _, _ := gs.equippedSwing()
	kind = getSwing(int(gs.Player.Class), int(gs.Player.Level), w[14], w[15], w[16], w[18], w[17], w[19], w[20])
	return kind, swingEffect(kind), swingDelay(kind)
}

// autoAttackTick is VIClient::AutoAttackTick (0x00BC76A0, [A]), from the
// frame (func_00BC6500) while auto-attack is on and a swing is due, for a
// player drawn as a character. In reach of the target, with a swing that has
// an effect, the effect plays from me to the target — nothing goes to the
// server — and the next swing waits the swing's delay. Otherwise my
// character swings by its weapon (not with a bow in slot 18, nor with a held
// item), 0x00D8 goes out (a bare opcode; the server learns the swing missed
// its reach) and the next waits 2 s. The delay adds to the due time, not to
// now.
func (e *Engine) autoAttackTick() {
	gs := e.State
	if !e.CombatEngaged {
		return
	}
	now := e.gameMs()
	if now < e.nextSwing {
		return
	}
	self := gs.PlayerObjectID
	if _, ok := gs.findEntity(self); !ok {
		return // Process makes the call only with the player's entity
	}
	if e.isCSpriteCB != nil && !e.isCSpriteCB(self) {
		return // GetActorSpriteType != 5
	}
	worn, slot, anim := gs.equippedSwing()
	kind := getSwing(int(gs.Player.Class), int(gs.Player.Level), worn[14], worn[15], worn[16], worn[18], worn[17], worn[19], worn[20])
	effect := swingEffect(kind)
	noEffect := worn[19] || effect == 0
	delay := int64(3500)
	if effect != 0 {
		delay = swingDelay(kind)
	}
	if e.targetInRange(slot) && !noEffect {
		if gs.SpellFXSys.has(effect) {
			tgt := e.target()
			if _, ok := gs.findEntity(tgt); ok {
				gs.SpellFXSys.Play(effect, 0, self, tgt, time.Time{})
				e.lastSwingFX = now
			}
		}
	} else {
		ranged := slot >= 0 && gs.Inventory()[slot].EquipLocation == 18
		if !ranged && !noEffect && e.setActionCB != nil {
			e.setActionCB(self, anim)
		}
		gs.socialSend(protocol.GameOpcode(0x00D8), nil)
		delay = 2000
	}
	e.nextSwing += delay
}

// has reports whether SPELLFX holds an effect: CLIENT's
// m_dict.FindTyped(effect, 21, idx).
func (sys *SpellEffectSystem) has(effectID uint32) bool {
	return sys != nil && sys.db != nil && sys.db.Lookup(effectID) != nil
}

// targetInRange is VIClient::TargetInRange (0x00BDC4B8, [A]): whether the
// target is in reach of the item at inventory index slot (-1: bare hands).
// Both must be creatures, the target alive by its HP byte (+0x19), and when I
// am a player (name colour 2) it may not be one (2 or 3). Reach is 4.5 for a
// melee weapon (attack types 1-6) and the item's own range for types 7, 9 and
// 10, which also need power and ammunition; a large target (+0x25A bit 0x400)
// adds 3 and a huge one (0x800) 5. Measured in 3D. Out of reach with a ranged
// weapon, word 723 ("Out of range.") in red.
func (e *Engine) targetInRange(slot int) bool {
	gs := e.State
	tgt := e.target()
	if tgt == noTarget {
		return false
	}
	t, ok := gs.findEntity(tgt)
	if !ok || t.ent.HP == 0 {
		return false
	}
	me, ok := gs.findEntity(gs.PlayerObjectID)
	if !ok || !t.creature || !me.creature {
		return false
	}
	if me.ent.NameColor == 2 && (t.ent.NameColor == 2 || t.ent.NameColor == 3) {
		return false
	}
	if tgt == gs.PlayerObjectID {
		return false
	}
	var extra float32
	if t.ent.NPCFlags&0x400 != 0 {
		extra += 3
	} else if t.ent.NPCFlags&0x800 != 0 {
		extra += 5
	}
	dx, dy, dz := me.ent.X-t.ent.X, me.ent.Y-t.ent.Y, me.ent.Z-t.ent.Z
	d := dx*dx + dy*dy + dz*dz
	reach := extra + 4.5
	var attackType int64
	if slot != -1 {
		item := gs.Inventory()[slot]
		attackType = item.AttackType
		switch attackType {
		case 1, 2, 3, 4, 5, 6:
		case 7, 9, 10:
			reach = float32(item.Range) + extra
			if !e.checkPower() || !e.hasAmmoFor(item) {
				return false
			}
		default:
			return false
		}
	}
	if 0 <= d && d <= reach*reach {
		return true
	}
	if attackType == 7 || attackType == 9 || attackType == 10 {
		gs.combatRefuse(723)
	}
	return false
}

// checkPower is VIClient::CheckPower (0x00BDC850 [V]): a shot needs a tenth
// of my power (m_stat[9] against m_stat[10]), else word 731 ("Not enough
// power to shoot.") in red.
func (e *Engine) checkPower() bool {
	gs := e.State
	if gs.PlayerStats[9] >= int32(float32(gs.PlayerStats[10])/10) {
		return true
	}
	gs.combatRefuse(731)
	return false
}

// hasAmmoFor is VIClient::HasAmmoFor (0x00BDC8F8 [V]): attack type 7 wants an
// item of kind 1 (+0x18) in the pack that cannot be worn, 9 one of kind 2, 10
// nothing; else word 724 ("Out of arrows.") for 7 and 725 ("Out of bolts.")
// for the rest, in red.
func (e *Engine) hasAmmoFor(weapon ParsedItem) bool {
	gs := e.State
	want := int64(0)
	switch weapon.AttackType {
	case 7:
		want = 1
	case 9:
		want = 2
	case 10:
		return true
	}
	if want != 0 {
		for _, it := range gs.Inventory() {
			if it.P18 == want && it.Slot == -1 {
				return true
			}
		}
	}
	if weapon.AttackType == 7 {
		gs.combatRefuse(724)
	} else {
		gs.combatRefuse(725)
	}
	return false
}

// FaceTargetHeading is the target-facing half of the local walk update
// (func_00BC3AE8 at 0x00BC3FE4, [A]): while auto-attack is on, or within a
// second of a swing's effect (with no turn key held), the walker turns to the
// target. CLIENT steers a bang-bang turn past 10 degrees, or in camera style
// 3 takes the target's heading outright; Go's camera-relative walking is
// style 3's and it has no turn rate, so this returns the heading to take. The
// target must be an entity in the scene and not the player.
func (e *Engine) FaceTargetHeading() (float32, bool) {
	gs := e.State
	now := e.gameMs()
	recent := e.lastSwingFX != 0 && now-e.lastSwingFX < 1000
	if !recent && !e.CombatEngaged {
		return 0, false
	}
	tgt := e.target()
	if tgt == noTarget || tgt == gs.PlayerObjectID {
		return 0, false
	}
	me, ok := gs.findEntity(gs.PlayerObjectID)
	if !ok {
		return 0, false
	}
	t, ok := gs.findEntity(tgt)
	if !ok {
		return 0, false
	}
	dx, dz := t.ent.X-me.ent.X, t.ent.Z-me.ent.Z
	if dx == 0 && dz == 0 {
		return 0, false
	}
	return float32(math.Atan2(float64(dx), float64(dz))), true
}

// combatTick is the frame's share of these, in Process's order
// (func_00BC6500): a handler's stops and an assist's target (Msg007B), the
// local walk update's facing, then AutoAttackTick.
func (e *Engine) combatTick() {
	gs := e.State
	if gs.stopAutoAttack {
		gs.stopAutoAttack = false
		e.StopAutoAttack()
	}
	if gs.stopFollow {
		gs.stopFollow = false
		e.following = false
	}
	if gs.AssistTargetPending {
		gs.AssistTargetPending = false
		e.SetAssistTarget(gs.AssistTarget)
	}
	if h, ok := e.FaceTargetHeading(); ok {
		gs.Player.Facing = h
	}
	e.autoAttackTick()
}

// --- interacting, emotes, following -------------------------------------------

// InteractWithTarget is VIClient::InteractWithTarget (0x00BD5B68 [V]), the
// interact key (Q, pad code 133): by the target's +0x25A flags a merchant
// gets 0x004C, a banker 0x124D and a blacksmith 0x0061 — none of them 0x0004
// first — a player or a pet (name colour 2 or 3) opens the game window's
// player menu with no message, and anything else gets 0x0004. Each carries
// the target as a raw u32; only a creature can be interacted with.
func (e *Engine) InteractWithTarget() {
	gs := e.State
	tgt := e.target()
	c, ok := gs.findEntity(tgt)
	if !ok || !c.creature {
		return
	}
	f := c.ent.NPCFlags
	switch {
	case f&1 != 0:
		gs.MerchantName = c.ent.Name
		gs.combatSendU32(protocol.OpcodeMerchantDiag, tgt)
	case f&2 != 0:
		gs.combatSendU32(protocol.OpcodeBankUI, tgt)
	case f&8 != 0:
		gs.combatSendU32(protocol.OpcodeBlackSmithMenu, tgt)
	case c.ent.NameColor == 2 || c.ent.NameColor == 3:
		log.Printf("Interact: 0x%08X is a player; CLIENT opens its player menu (VIWndGame+0x678), the Go client has none", tgt)
	default:
		gs.combatSendU32(protocol.OpcodeInteract, tgt)
	}
}

// Emotes, VIClient::PlayEmote (0x00BCFC38 [V]): each a bare opcode, and the
// action on my own character.
const (
	EmoteWave  = 0 // 0x001E, action 56
	EmoteBow   = 1 // 0x0042, action 57
	EmotePoint = 2 // 0x0043, action 60
	EmoteCheer = 3 // 0x0044, action 61
)

// PlayEmote sends emote which and returns the action to play on my character,
// or -1 (no such emote, or no self id yet). The 0x40 update's animation byte
// does not carry it: the server relays the opcode.
func (e *Engine) PlayEmote(which int) int32 {
	gs := e.State
	if !gs.selfKnown && !e.Offline {
		return -1
	}
	ops := [4]protocol.GameOpcode{0x001E, 0x0042, 0x0043, 0x0044}
	acts := [4]int32{56, 57, 60, 61}
	if which < 0 || which >= len(ops) {
		return -1
	}
	gs.socialSend(ops[which], nil)
	return acts[which]
}

// ToggleFollow is VIClient::ToggleFollow (0x00BD5D70 [V]), the /follow
// command: follow the target if it is in the group roster and not me, else
// word 1195 ("You must target a group member to follow.") as a notice and no
// following. No message either way.
func (e *Engine) ToggleFollow() {
	gs := e.State
	e.following = !e.following
	if !e.following {
		return
	}
	found := false
	if tgt := e.target(); tgt != gs.PlayerObjectID {
		for i := 0; i < gs.groupCount(); i++ {
			if gs.GroupMembers[i].ObjectID == tgt {
				e.followID = tgt
				found = true
				break
			}
		}
	}
	if !found {
		gs.socialShowWord(1195)
		e.following = false
	}
}

// Following reports whether ToggleFollow's following is on.
func (e *Engine) Following() bool { return e.following }

// FollowStep is the follow half of the local walk update (func_00BC3AE8 at
// 0x00BC41A8, [A]). Auto-attack ends following, and so does the walk stick
// (forward input past 0.7); so does the member leaving the roster or the
// scene. Otherwise the walker heads for the member: heading is the direction
// (Go facing), forward 0 within 3 (in XZ), a walk from 3 to 6 and a run past
// 6. forwardInput is this frame's forward stick or keys, -1..1.
func (e *Engine) FollowStep(forwardInput float32) (heading float32, move bool, walk bool) {
	gs := e.State
	if !e.following {
		return 0, false, false
	}
	if e.CombatEngaged || forwardInput > 0.7 || forwardInput < -0.7 {
		e.following = false
		return 0, false, false
	}
	inGroup := false
	for i := 0; i < gs.groupCount(); i++ {
		if gs.GroupMembers[i].ObjectID == e.followID {
			if _, ok := gs.findEntity(e.followID); ok {
				inGroup = true
			}
		}
	}
	if !inGroup || e.followID == noTarget || e.followID == gs.PlayerObjectID {
		e.following = false
		return 0, false, false
	}
	me, ok := gs.findEntity(gs.PlayerObjectID)
	leader, ok2 := gs.findEntity(e.followID)
	if !ok || !ok2 {
		e.following = false
		return 0, false, false
	}
	dx, dz := leader.ent.X-me.ent.X, leader.ent.Z-me.ent.Z
	dist := float32(math.Sqrt(float64(dx*dx + dz*dz)))
	heading = float32(math.Atan2(float64(dx), float64(dz)))
	switch {
	case dist > 6:
		return heading, true, false
	case dist < 3:
		return heading, false, false
	}
	return heading, true, true
}
