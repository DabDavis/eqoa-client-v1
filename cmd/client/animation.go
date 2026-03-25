package main

import (
	"log"
	"time"

	"github.com/eqoa/client/internal/game"
	"github.com/eqoa/client/internal/models"
)

// Humanoid animation constants — ESF CPlayList slot numbers.
// animByte is a CPlayList slot number resolved via PLSlotToRawPair in applySkeletal.
// This ensures correct animation mapping across all model types (40, 64, 72, 80 anims).
const (
	// Locomotion (ESF CPlayList slots)
	animIdle       = 0
	animWalk       = 1
	animRun        = 3
	animFall       = 6  // humanoid CPlayList slot 6 (NOT 4 which is swim forward)
	animSwim       = 12 // CPlayList slot 12 (swim)
	animLanding    = 7  // humanoid CPlayList slot 7 (NOT 5 which is swim backward)
	animStrafe     = 10 // 64+ anim models only

	// State
	animDeath          = 14
	animSpecialIdle    = 15
	animBattleStance   = 17 // fighting stance
	animHitStun        = 34 // hit reaction

	// Combat attacks — PS2 SetAttackAction CPlayList indices.
	// All attack slots (22-44) exist on all model types.
	animAttack1 = 22 // first attack (unarmed slot 0)
	animAttack2 = 23 // second attack (unarmed slot 1)
	animAttack3 = 24 // third attack
	animAttack4 = 25 // fourth attack

	// Spell casting
	animSpellDamage = 37 // damage spell cast
	animSpellAlt    = 38 // spell cast alt
	animSpellBuff   = 39 // spell buff

	// Emotes (72+ anim models; 64-anim models lack 60/61)
	animEmoteWave  = 56
	animEmoteBow   = 57
	animEmotePoint = 60
	animEmoteCheer = 61

	// Non-humanoid (monster) CPlayList slots (must match PS2 creature CPlayList table).
	// Previously used F8 debug indices; corrected to CPlayList slots after PLSlotToRaw dispatch fix.
	nhAnimAttack1      = 8  // CL slot 8: Attack1
	nhAnimAttack2      = 9  // CL slot 9: Attack2
	nhAnimHitStun      = 10 // CL slot 10: HitReaction
	nhAnimCastSpell    = 11 // CL slot 11: SpellCast
	nhAnimDeath        = 15 // CL slot 15: Death
	nhAnimBattleStance = 16 // CL slot 16: BattleStance
	nhAnimSpecialIdle  = 17 // CL slot 17: Special
)

// weaponAttackAnims maps VICSpriteAttackType → [Primary(s=0), Alt(s=0)] CPlayList slots.
// PS2 alternates between SetAttackAction (0x00421820) and SetAttackAltAction (0x00421960)
// on successive swings. Single-wield always uses attackSlot=0 (right hand).
// Keys match ParsedItem.AttackType from MemoryDump / eqoa.db items table.
var weaponAttackAnims = map[int][]byte{
	0:  {23, 25},     // safety fallback (PS2 default is type 11, not 0)
	1:  {25, 41},     // 1H Slash (sword): Primary=25, Alt=41
	2:  {26, 42},     // 2H Slash (greatsword): Primary=26, Alt=42
	3:  {41, 28},     // 1H Blunt (mace): Primary=41, Alt=28
	4:  {42, 29},     // 2H Blunt (maul): Primary=42, Alt=29
	5:  {28, 25},     // Hand-to-Hand (fist): Primary=28, Alt=25
	6:  {29, 26},     // 2H Pierce (spear): Primary=29, Alt=26
	7:  {31, 31},     // Bow
	8:  {35, 35},     // Throwing: Primary=35, Alt=35
	9:  {33, 33},     // Crossbow
	10: {36, 36},     // Spell/Wand
	11: {23, 25},     // Dagger: Primary=23, Alt=25
}

// weaponAttackAnimsDW is the dual-wield variant combining both PS2 functions.
// 4-entry arrays: [Primary(s=0), Primary(s=1), Alt(s=0), Alt(s=1)] for full
// 4-swing cycle. PS2 alternates right/left hand AND Primary/Alt per swing.
var weaponAttackAnimsDW = map[int][]byte{
	0:  {22, 23, 24, 25},     // safety fallback — offhand first
	1:  {24, 25, 40, 41},     // 1H Slash: left/right × Primary/Alt
	2:  {26, 42},             // 2H Slash (not dual-wielded)
	3:  {40, 41, 27, 28},     // 1H Blunt: left/right × Primary/Alt
	4:  {42, 29},             // 2H Blunt (not dual-wielded)
	5:  {27, 28, 24, 25},     // Hand-to-Hand: left/right × Primary/Alt
	6:  {29, 26},             // 2H Pierce (not dual-wielded)
	7:  {31, 31},             // Bow
	8:  {35, 32, 35, 32},     // Throwing: right(35)/left(32) hand throws
	9:  {33, 33},             // Crossbow
	10: {36, 36},             // Spell/Wand
	11: {22, 23, 24, 25},     // Dagger: left/right × Primary/Alt
}

// combatAnimSet defines the animation bytes for a model type's combat actions.
type combatAnimSet struct {
	battleStance byte
	hitStun      byte
	castDamage   byte
	castBuff     byte
}

var humanoidAnims = combatAnimSet{animBattleStance, animHitStun, animSpellDamage, animSpellBuff}
var nonHumanoidAnims = combatAnimSet{nhAnimBattleStance, nhAnimHitStun, nhAnimCastSpell, nhAnimCastSpell}

// swingsPerAttack returns the number of attack animations per auto-attack cycle
// based on class, level, and equipment. Used by character select for preview attacks.
//
// Casters (DRD=7, SHA=8, CL=9, MAG=10, NEC=11, ENC=12, WIZ=13, ALC=14): always 1.
// PAL(2), SK(3): No dual wield. Shield/primary or 2H = 2 swings at level 20+.
// WAR(0): DW = 4 swings at level 35+. Shield/primary = 2 at 35+. 2H = 2 at 20+.
// RAN(1), MNK(4), BRD(5), RGE(6): DW = 4 swings. Non-DW = 2 at level 20+.
func swingsPerAttack(class byte, level byte, dualWield bool) int {
	switch class {
	case 7, 8, 9, 10, 11, 12, 13, 14:
		return 1
	case 2, 3:
		if level >= 20 {
			return 2
		}
		return 1
	case 0:
		if dualWield {
			if level >= 35 {
				return 4
			}
			return 2
		}
		if level >= 20 {
			return 2
		}
		return 1
	case 1, 4, 5, 6:
		if dualWield {
			return 4
		}
		if level >= 20 {
			return 2
		}
		return 1
	}
	return 1
}

// PS2 Combat Animation System (func_00CBDEF8 + func_00CC5190)
//
// The PS2 client runs a per-frame combat tick that resolves class/level/weapon
// into an attack type (0-12), then maps that to a specific CPlayList animation slot.
// This is independent of DamageIndication — animations are client-side timed.

// combatAttackType represents the resolved attack type from func_00CBDEF8.
// Each type maps to a specific CPlayList animation slot and spell FX.
type combatAttackType int

// resolveCombatAttackType ports PS2 func_00CBDEF8.
// Jump table at 0x00CF3490 indexed by class (0-14).
// Item proc type flags come from equipped items (item+0x2F0 types 14-20).
// For our Go client, we simplify: hasPrimary/hasRanged/hasShield from equipment.
func resolveCombatAttackType(class, level byte, hasPrimary, hasRanged, hasShield, hasDW bool) int {
	switch class {
	case 7, 8, 9, 10, 11, 12, 13, 14:
		// Casters: ranged→8, shield→1, else→0 (no attack anim, just idle)
		if hasRanged {
			return 8
		}
		if hasPrimary {
			return 1
		}
		return 0 // combat idle

	case 2, 3:
		// PAL/SK: shield melee, level 20/35 thresholds
		if hasRanged {
			if level < 20 {
				return 8
			}
			return 9
		}
		if level < 20 {
			if hasPrimary {
				return 1
			}
			return 0
		}
		if level < 35 {
			if hasShield {
				return 3
			}
			return 2
		}
		// 35+
		if hasShield {
			return 7
		}
		return 5

	case 0, 1, 4:
		// WAR/RAN/MNK: full melee + DW, level 20/35
		if hasRanged {
			if level < 20 {
				return 8
			}
			return 9
		}
		if level < 20 {
			if hasPrimary {
				return 1
			}
			return 0
		}
		if level < 35 {
			if hasDW {
				return 3
			}
			if hasShield {
				return 3
			}
			return 2
		}
		// 35+
		if hasDW {
			return 8 // DW slash
		}
		if hasShield {
			return 7
		}
		return 5

	case 5, 6:
		// BRD/RGE: DW melee focus
		if hasRanged {
			if level < 20 {
				return 8
			}
			return 9
		}
		if level < 20 {
			if hasPrimary {
				return 1
			}
			return 0
		}
		if level < 35 {
			if hasDW {
				return 1
			}
			if hasShield {
				return 3
			}
			return 2
		}
		// 35+
		if hasDW {
			return 8
		}
		if hasShield {
			return 7
		}
		return 5
	}
	return 0
}

// combatAttackAnim ports PS2 func_00CC5190.
// Jump table at 0x00CF3D70 maps attack type (0-10) → CPlayList animation slot.
// The alt flag alternates primary/alt for dual-wield and multi-swing attacks.
func combatAttackAnim(attackType int, alt bool) byte {
	switch attackType {
	case 0:
		return 23 // combat idle (no attack)
	case 1:
		if alt {
			return 24 // 2H slash alt
		}
		return 25 // 2H slash primary
	case 2:
		return 26 // 2H sword overhead
	case 3:
		if alt {
			return 40 // DW throw left
		}
		return 41 // DW throw right
	case 4:
		return 42 // archery / HtoH
	case 5:
		if alt {
			return 27 // 2H blunt alt
		}
		return 28 // 2H blunt primary
	case 6:
		return 29 // 2H blunt overhead
	case 7:
		return 31 // 1H blunt primary
	case 8:
		if alt {
			return 32 // DW slash alt (left hand)
		}
		return 35 // DW slash primary (right hand)
	case 9:
		return 33 // 1H blunt alt
	case 10:
		return 36 // 2H blunt heavy
	}
	return 23 // default: combat idle
}

// combatAttackFXHash ports PS2 func_00CBE0A8.
// Returns the spell effect DictID hash for the attack type.
// Attack1-4 = melee hit effects, Range1-4 = ranged hit effects.
func combatAttackFXHash(attackType int) uint32 {
	switch attackType {
	case 1:
		return 0xDA9BEA12 // "Attack2"
	case 2:
		return 0xDA9BEA11 // "Attack1"
	case 3:
		return 0xDA9BEA13 // "Attack3"
	case 4:
		return 0xDA9BEA14 // "Attack4"
	case 5:
		return 0x45EAC4A0 // "Range1"
	case 6:
		return 0x45EAC4A1 // "Range2"
	case 7:
		return 0x45EAC4A2 // "Range3"
	case 8:
		return 0x45EAC4A3 // "Range4"
	}
	return 0 // no FX
}

// isDualWield returns true if the entity has weapons in both hands (no shield).
func isDualWield(primary, secondary, shield uint32) bool {
	return primary != 0 && secondary != 0 && shield == 0
}

// isDualWieldFromItems checks parsed inventory for actual dual-wield weapons.
// Secondary slot items with AttackType 0 or 10 are held items (focus/orb), not weapons.
func isDualWieldFromItems(items []game.ParsedItem) bool {
	var hasPrimary, hasSecondary bool
	for _, item := range items {
		// Only check equipped items
		if item.EquipLocation < 0 || item.Location != -1 {
			continue
		}
		if item.Slot == 12 && item.AttackType > 0 && item.AttackType != 10 {
			hasPrimary = true
		}
		if item.Slot == 14 && item.AttackType > 0 && item.AttackType != 10 {
			hasSecondary = true
		}
	}
	return hasPrimary && hasSecondary
}

// combatState tracks combat animation state for any combatant (player or entity).
// PS2 architecture: func_00BC76A0 runs a per-frame combat tick with a timer
// (entity+0x71E8). Animations are client-side timed, not DamageIndication-driven.
type combatState struct {
	inCombat        bool
	combatAnim      byte
	combatAnimEnd   time.Time
	seenAttackCount int       // last processed AttackCount (triggers combat start)
	seenHitTime     time.Time // last processed damage-taken time
	attackCycleIdx  int       // alternates primary/alt for PS2 func_00CC5190
	lastActivity    time.Time // for auto-end detection
	pendingHitFX    int       // queued hit FX count (fired when swing animation starts)
	castBarEnd      time.Time // when cast bar finishes (anim starts after this)
	castAnimEnd     time.Time // when post-cast animation ends (computed once bar completes)
	castingSelf     bool      // true = self-buff anim, false = offensive
	nextSwingTime   time.Time // PS2 entity+0x71E8: next attack animation time
	combatAttackType int      // resolved attack type (0-12) from func_00CBDEF8
}

// resolveCombat is the shared combat animation loop used by both player and entities.
// PS2 func_00BC76A0: per-frame combat tick. Timer-driven swing animations.
// DamageIndication starts combat + refreshes activity timer.
// The client drives attack animations at 3500ms intervals (PS2 default).
func (cs *combatState) resolveCombat(am *AnimationManager, ctrl *models.SpriteAnimController, now time.Time,
	attackCount int, hitTime time.Time, attackAnims []byte,
	cas combatAnimSet, modelID int32, showBattleStance bool, serverDrivenCombat bool,
	dbgLabel string,
) {
	// Auto-detect combat end: no activity for 5+ seconds.
	if cs.inCombat && !cs.lastActivity.IsZero() && now.Sub(cs.lastActivity) > 5*time.Second {
		cs.inCombat = false
		cs.combatAnim = 0
		cs.nextSwingTime = time.Time{}
		ctrl.ClearAction()
	}

	// Detect new damage dealt — starts/refreshes combat.
	// PS2: entity+0x71E4 (combat active flag) set by CastingCombat/DamageIndication.
	if attackCount != cs.seenAttackCount {
		cs.seenAttackCount = attackCount
		cs.lastActivity = now
		if !cs.inCombat {
			cs.inCombat = true
			// First swing immediately on combat start
			cs.nextSwingTime = now
		}
	}

	// PS2 timer-driven combat tick (func_00BC76A0).
	// When nextSwingTime arrives, play the attack animation and advance timer.
	if cs.inCombat && !cs.nextSwingTime.IsZero() && !now.Before(cs.nextSwingTime) {
		// Select animation: PS2 func_00CC5190
		alt := cs.attackCycleIdx%2 != 0
		var anim byte
		if cs.combatAttackType > 0 {
			// Use PS2 combat tick animation (specific CPlayList slot)
			anim = combatAttackAnim(cs.combatAttackType, alt)
		} else {
			// Fallback to weaponAttackAnims table for entities without resolved type
			anim = attackAnims[cs.attackCycleIdx%len(attackAnims)]
		}
		cs.attackCycleIdx++

		dur := am.animDur(modelID, anim)
		cs.combatAnim = anim
		cs.combatAnimEnd = now.Add(dur)
		ctrl.SetAction(int(anim))

		// PS2: advance timer by 3500ms (default) or 2000ms (after spell FX).
		// Use animation duration + small gap to pace swings naturally.
		swingInterval := dur + 200*time.Millisecond
		if swingInterval < 600*time.Millisecond {
			swingInterval = 600*time.Millisecond
		}
		cs.nextSwingTime = now.Add(swingInterval)
		cs.lastActivity = now

		log.Printf("ANIM [%s]: ATTACK anim=%d atkType=%d dur=%v alt=%v cycle=%d",
			dbgLabel, anim, cs.combatAttackType, dur, alt, cs.attackCycleIdx)
	}

	// Detect new hit taken.
	newHitTaken := !hitTime.IsZero() && hitTime != cs.seenHitTime

	// Casting animation — highest priority after death.
	if !cs.castBarEnd.IsZero() && !now.Before(cs.castBarEnd) {
		if cs.castAnimEnd.IsZero() {
			ab := cas.castDamage
			if cs.castingSelf {
				ab = cas.castBuff
			}
			dur := am.animDur(modelID, ab)
			cs.castAnimEnd = cs.castBarEnd.Add(dur)
			ctrl.SetAction(int(ab))
			log.Printf("ANIM [%s]: CAST anim=%d dur=%v", dbgLabel, ab, dur)
		}
		if now.Before(cs.castAnimEnd) {
			return
		}
		cs.castAnimEnd = cs.castBarEnd
	}

	// Hit reaction — controller priority gates (attack 0.4 > hitStun 0.3).
	if newHitTaken {
		cs.seenHitTime = hitTime
		cs.lastActivity = now
		if !cs.inCombat {
			cs.inCombat = true
		}
		ctrl.SetAction(int(cas.hitStun))
		if ctrl.ActionAnimID() == int(cas.hitStun) {
			cs.combatAnim = cas.hitStun
			dur := am.animDur(modelID, cas.hitStun)
			cs.combatAnimEnd = now.Add(dur)
			log.Printf("ANIM [%s]: HIT STUN anim=%d dur=%v", dbgLabel, cas.hitStun, dur)
			return
		}
		log.Printf("ANIM [%s]: HIT SKIP (action=%d)", dbgLabel, ctrl.ActionAnimID())
	}

	// Combat anim still playing.
	if cs.combatAnim != 0 && now.Before(cs.combatAnimEnd) && !ctrl.IsActionFinished() {
		return
	}

	// Battle stance — only when idle (not moving).
	baseLoco := ctrl.LocoAnimID()
	isMoving := baseLoco == animWalk || baseLoco == animRun || baseLoco == animSwim || baseLoco == animStrafe
	if cs.inCombat && showBattleStance && !isMoving {
		cs.combatAnim = 0
		ctrl.ClearAction()
		ctrl.SetLocomotion(int(cas.battleStance))
		return
	}
	if cs.inCombat && showBattleStance && isMoving {
		cs.combatAnim = 0
		ctrl.ClearAction()
		return
	}

	// No combat — clear action.
	cs.combatAnim = 0
	ctrl.ClearAction()
}

// ConsumePendingHitFX returns the number of pending hit FX and resets the counter.
// Called by the render loop to fire FX in sync with swing animations.
func (am *AnimationManager) ConsumePendingPlayerHitFX() int {
	n := am.playerCombat.pendingHitFX
	am.playerCombat.pendingHitFX = 0
	return n
}

// PlayerController returns the player's SpriteAnimController (nil if not created yet).
func (am *AnimationManager) PlayerController() *models.SpriteAnimController {
	return am.playerController
}

// EntityController returns the SpriteAnimController for an entity (nil if not created).
func (am *AnimationManager) EntityController(objID uint32) *models.SpriteAnimController {
	if am.entityControllers == nil {
		return nil
	}
	return am.entityControllers[objID]
}

// AnimationManager consolidates all combat/debug animation state and logic.
type AnimationManager struct {
	modelMgr *models.ModelManager

	// Debug animation cycling (F8 key)
	DebugAnimOverride int
	DebugAnimActive   bool
	DebugModelID      int32 // F7 model override (0 = use real model)

	// Player combat state
	playerCombat  combatState
	InCombat      bool // exported for input.go
	CombatEngaged bool // input guard: prevents re-sending Attack opcode
	LandingTime   time.Time

	// Hit FX — fire SpellFX per swing when the animation starts.
	HitFXFunc       func(casterID, targetID uint32)
	targetObjectID  uint32 // current target for hit FX
	playerObjectID  uint32 // player's object ID

	// Emote state (play-once animation override)
	EmoteAnim    byte      // CPlayList slot (0 = no emote active)
	EmoteEnd     time.Time // when the emote animation finishes

	// Per-entity combat state (keyed by ObjectID)
	entityStates map[uint32]*combatState

	// SpriteAnimController instances (dual-pair priority system).
	// Routes ALL animation decisions through PS2 VICSprite priority gating.
	playerController  *models.SpriteAnimController
	entityControllers map[uint32]*models.SpriteAnimController

	// Cached resolved animation bytes (avoid double-resolution in sound system).
	cachedPlayerLoco   byte
	cachedPlayerAction byte
	cachedEntityAnims  map[uint32][2]byte // [loco, action] per entity
}

// animParams holds inputs for the unified animation resolver.
type animParams struct {
	cs                 *combatState
	modelID            int32
	isDead             bool
	attackCount        int
	hitTime            time.Time
	primaryWeapon      uint32
	secondaryWeapon    uint32
	shield             uint32
	class              byte
	level              byte
	isPC               bool // true = use class for swing count
	weaponAttackType   int  // >=0: use weaponAttackAnims[type]. <0: derive from equipment
	parsedItems        []game.ParsedItem // player inventory (for dual-wield detection)
	showBattleStance   bool
	serverDrivenCombat bool
	castBarEnd         time.Time
	castingSelf        bool
	baseAnim           byte   // locomotion fallback
	dbgLabel           string // "PLAYER" or entity name for debug logs
}

// resolveAnim is the unified animation resolver for both player and entities.
// Routes decisions through the SpriteAnimController — no return value needed.
func (am *AnimationManager) resolveAnim(ctrl *models.SpriteAnimController, now time.Time, p animParams) {
	isHumanoid := am.modelMgr != nil && am.modelMgr.ModelIsHumanoid(p.modelID)

	// Query per-model animation profile for correct pair indices.
	var prof *models.AnimProfile
	if am.modelMgr != nil {
		prof = am.modelMgr.ModelAnimProfile(p.modelID)
	}

	// Death pose — highest priority, holds last frame via clamp in manager.go.
	if p.isDead {
		deathAnim := byte(nhAnimDeath)
		if prof != nil {
			deathAnim = prof.Death
		}
		ctrl.IsDead = true
		ctrl.SetLocomotion(int(deathAnim))
		ctrl.SetAction(int(deathAnim))
		return
	}
	ctrl.IsDead = false

	// Set locomotion from base anim (idle/walk/run/swim/fall).
	ctrl.SetLocomotion(int(p.baseAnim))

	// Select anim set and attack pool based on model type.
	cas := nonHumanoidAnims
	attackAnims := []byte{nhAnimAttack1, nhAnimAttack2}
	if prof != nil {
		cas = combatAnimSet{prof.BattleStance, prof.HitStun, prof.CastDamage, prof.CastBuff}
		attackAnims = []byte{prof.Attack1, prof.Attack2}
	}
	if isHumanoid {
		dw := isDualWield(p.primaryWeapon, p.secondaryWeapon, p.shield)
		// For player characters, use inventory AttackType to distinguish
		// held items (focus/orb, AttackType 0 or 10) from dual-wield weapons.
		if p.isPC && p.parsedItems != nil {
			dw = isDualWieldFromItems(p.parsedItems)
		}
		// Per-profile weapon attack pools override the global table (for gnolls, etc.).
		if prof != nil && prof.DualWieldAtks != nil {
			if dw {
				attackAnims = prof.DualWieldAtks
			} else if p.shield != 0 && p.primaryWeapon != 0 {
				attackAnims = prof.ShieldAtks
			} else if prof.TwoHandAtks != nil && p.secondaryWeapon == 0 && p.shield == 0 && p.primaryWeapon != 0 {
				attackAnims = prof.TwoHandAtks
			} else {
				attackAnims = prof.DualWieldAtks // default to DW pool
			}
		} else {
			// Global humanoid weapon attack table (PS2 SetAttackAction).
			atkTable := weaponAttackAnims
			if dw {
				atkTable = weaponAttackAnimsDW
			}
			if p.weaponAttackType >= 0 {
				attackAnims = atkTable[p.weaponAttackType]
				if attackAnims == nil {
					attackAnims = atkTable[11]
				}
			} else if p.shield != 0 && p.primaryWeapon != 0 {
				attackAnims = atkTable[3] // 1H blunt: primary hand fallback
			} else {
				attackAnims = atkTable[11] // unarmed (PS2 DetachItem default = type 11)
			}
		}
		// Limit attack anims by class: casters/healers (7-14) only get single primary.
		// Melee classes (0-6) keep full pool for double/quad from server DamageIndication.
		if p.class >= 7 && len(attackAnims) > 1 {
			attackAnims = attackAnims[:1] // single swing only
		}
	}

	// Sync cast state into combatState.
	if !p.castBarEnd.IsZero() {
		if p.cs.castBarEnd.IsZero() || p.cs.castBarEnd != p.castBarEnd {
			p.cs.castBarEnd = p.castBarEnd
			p.cs.castAnimEnd = time.Time{}
			p.cs.castingSelf = p.castingSelf
		}
	}

	// PS2 func_00CBDEF8: resolve class/level/weapon → attack type for combat tick.
	// Only for humanoid player/entity models — NPCs with profiles use attackAnims.
	if isHumanoid {
		hasPrimary := p.primaryWeapon != 0
		hasRanged := false // TODO: detect ranged weapons from AttackType 7/8/9
		hasShield := p.shield != 0
		dw := isDualWield(p.primaryWeapon, p.secondaryWeapon, p.shield)
		if p.isPC && p.parsedItems != nil {
			dw = isDualWieldFromItems(p.parsedItems)
		}
		p.cs.combatAttackType = resolveCombatAttackType(p.class, p.level, hasPrimary, hasRanged, hasShield, dw)
	}

	// Combat animation routing — mutates controller state.
	p.cs.resolveCombat(am, ctrl, now,
		p.attackCount, p.hitTime, attackAnims,
		cas, p.modelID, p.showBattleStance, p.serverDrivenCombat, p.dbgLabel,
	)
}

// ResolvePlayerAnim computes the final animation for the local player.
// Returns (locoAnim, actionAnim): locoAnim drives lower body, actionAnim
// drives upper body when nonzero (PS2 dual-pair: pair 0 = loco, pair 1 = action).
func (am *AnimationManager) ResolvePlayerAnim(gs *game.GameState, p *game.Entity, engine *game.Engine, now time.Time) (locoAnim, actionAnim byte) {
	// Cache resolved anims on return for sound system (avoids double-resolution).
	defer func() {
		am.cachedPlayerLoco = locoAnim
		am.cachedPlayerAction = actionAnim
	}()

	if am.DebugAnimActive {
		return byte(am.DebugAnimOverride), 0
	}

	// Use F7 debug model if active, so animation profile matches rendered model.
	modelID := int32(p.ModelID)
	if am.DebugModelID != 0 {
		modelID = am.DebugModelID
	}

	// Lazily create or re-create the player controller matching the current model type.
	isHumanoid := am.modelMgr != nil && am.modelMgr.ModelIsHumanoid(modelID)
	if am.playerController == nil {
		am.playerController = models.NewSpriteAnimController(isHumanoid)
	}
	ctrl := am.playerController
	// Wire controller to AnimState for feedback (play-once completion, blend state).
	if am.modelMgr != nil {
		am.modelMgr.SetEntityController(p.ObjectID, ctrl)
	}

	// Sync server-confirmed combat state (one-shot flag from handleCastingCombat).
	if gs.PlayerInCombat {
		if am.CombatEngaged {
			am.playerCombat.inCombat = true
			am.playerCombat.lastActivity = now
			am.InCombat = true
			engine.InCombat = true
		}
		gs.PlayerInCombat = false
	}

	// Set controller dead/combat flags.
	ctrl.IsDead = gs.Player.Dead || gs.Player.HPFlag == 3
	ctrl.InCombat = am.playerCombat.inCombat

	// Compute cast bar end time from GameState.
	var castBarEnd time.Time
	if gs.CastDurationMs > 0 {
		castBarEnd = gs.CastStartTime.Add(time.Duration(gs.CastDurationMs) * time.Millisecond)
	}

	// Sync hit FX target info for resolveCombat.
	am.playerObjectID = gs.PlayerObjectID
	am.targetObjectID = engine.TargetObjectID

	// Base locomotion animation (idle/walk/run/swim/fall).
	baseLoco := engine.AnimByte

	// Route through resolveAnim → resolveCombat → controller.
	am.resolveAnim(ctrl, now, animParams{
		cs:                 &am.playerCombat,
		modelID:            modelID,
		isDead:             gs.Player.Dead || gs.Player.HPFlag == 3,
		attackCount:        p.AttackCount,
		hitTime:            gs.PlayerHitTime,
		primaryWeapon:      p.PrimaryWeapon,
		secondaryWeapon:    p.SecondaryWeapon,
		shield:             p.Shield,
		class:              gs.Player.Class,
		level:              gs.Player.Level,
		isPC:               true,
		weaponAttackType:   am.getWeaponAttackType(gs),
		parsedItems:        gs.ParsedItems,
		showBattleStance:   am.InCombat,
		serverDrivenCombat: true,
		castBarEnd:         castBarEnd,
		castingSelf:        gs.CastSelfTarget,
		baseAnim:           baseLoco,
		dbgLabel:           "PLAYER",
	})

	// Sync exported combat state for input.go / engine.
	if am.playerCombat.inCombat != am.InCombat {
		am.InCombat = am.playerCombat.inCombat
		engine.InCombat = am.InCombat
		if !am.InCombat {
			am.CombatEngaged = false
			engine.CombatEngaged = false
			engine.AttackSent = false
			log.Printf("COMBAT: auto-ended (no activity for 5s)")
		}
	}

	// Emote animation (play-once, overrides locomotion but not combat/death).
	if am.EmoteAnim != 0 {
		if now.Before(am.EmoteEnd) {
			ctrl.SetAction(int(am.EmoteAnim))
			engine.EmoteAnimByte = am.EmoteAnim
		} else {
			am.EmoteAnim = 0
			engine.EmoteAnimByte = 0
		}
	} else {
		engine.EmoteAnimByte = 0
	}

	// Landing animation (brief crouch after fall — full body).
	if !am.LandingTime.IsZero() && now.Sub(am.LandingTime) < 300*time.Millisecond {
		return animLanding, 0 // landing is full body, no split
	}

	// Resolve final animations from controller.
	locoID, actionID := ctrl.Resolve()
	locoAnim = byte(locoID)

	// If action is active and differs from loco, decide split vs full-body.
	isMoving := locoAnim == animWalk || locoAnim == animRun || locoAnim == animSwim || locoAnim == animStrafe
	if actionID >= 0 {
		actByte := byte(actionID)
		if actByte != locoAnim {
			// Death is always full-body.
			if actByte == animDeath {
				return actByte, 0
			}
			if isMoving {
				actionAnim = actByte // split: upper=action, lower=locomotion
			} else {
				locoAnim = actByte // idle: action controls full body
			}
		} else {
			locoAnim = actByte
		}
	}

	return locoAnim, actionAnim
}

// PlayEmote starts an emote animation on the local player.
func (am *AnimationManager) PlayEmote(animSlot byte, modelID int32) {
	// Verify the CPlayList slot exists on this model
	if am.modelMgr != nil {
		model, ok := am.modelMgr.GetCachedModel(modelID)
		if ok && model != nil && model.PLSlotToRawPair != nil {
			if _, exists := model.PLSlotToRawPair[int(animSlot)]; !exists {
				// Slot doesn't exist — dump available slots for debugging
				slots := make([]int, 0, len(model.PLSlotToRawPair))
				for s := range model.PLSlotToRawPair {
					slots = append(slots, s)
				}
				log.Printf("EMOTE: slot %d NOT in PLSlotToRawPair for model 0x%08X. Available: %v",
					animSlot, uint32(modelID), slots)
				return
			}
		}
	}
	dur := am.animDur(modelID, animSlot)
	am.EmoteAnim = animSlot
	am.EmoteEnd = time.Now().Add(dur)
	log.Printf("EMOTE: playing anim slot %d for %v", animSlot, dur)
}

// ResolveEntityAnim computes the animation for an NPC/entity.
// Returns (locoAnim, actionAnim) for dual-pair upper/lower body split.
func (am *AnimationManager) ResolveEntityAnim(ent *game.Entity, now time.Time) (locoAnim, actionAnim byte) {
	// Cache resolved anims on return for sound system (avoids double-resolution).
	defer func() {
		if am.cachedEntityAnims == nil {
			am.cachedEntityAnims = make(map[uint32][2]byte)
		}
		am.cachedEntityAnims[ent.ObjectID] = [2]byte{locoAnim, actionAnim}
	}()

	if am.entityStates == nil {
		am.entityStates = make(map[uint32]*combatState)
	}
	es := am.entityStates[ent.ObjectID]
	if es == nil {
		es = &combatState{}
		am.entityStates[ent.ObjectID] = es
	}

	// Lazily create entity controller.
	if am.entityControllers == nil {
		am.entityControllers = make(map[uint32]*models.SpriteAnimController)
	}
	ctrl := am.entityControllers[ent.ObjectID]
	if ctrl == nil {
		isHumanoid := am.modelMgr != nil && am.modelMgr.ModelIsHumanoid(int32(ent.ModelID))
		ctrl = models.NewSpriteAnimController(isHumanoid)
		am.entityControllers[ent.ObjectID] = ctrl
	}
	// Wire controller to AnimState for feedback (play-once completion, blend state).
	if am.modelMgr != nil {
		am.modelMgr.SetEntityController(ent.ObjectID, ctrl)
	}

	// Debug: log combat state changes for entities
	if ent.AttackCount != es.seenAttackCount || (!ent.LastDamageTime.IsZero() && ent.LastDamageTime != es.seenHitTime) {
		log.Printf("ENTITY COMBAT DEBUG: 0x%08X(%s) atkCount=%d(seen=%d) hitTime=%v(seen=%v) inCombat=%v combatAnim=%d",
			ent.ObjectID, ent.Name, ent.AttackCount, es.seenAttackCount,
			!ent.LastDamageTime.IsZero(), !es.seenHitTime.IsZero(),
			es.inCombat, es.combatAnim)
	}

	// Set controller flags.
	ctrl.IsDead = ent.HPFlag == 3
	ctrl.InCombat = es.inCombat

	// Server animation byte → CPlayList slot mapping.
	baseLoco := ent.Animation

	// Route through resolveAnim → resolveCombat → controller.
	am.resolveAnim(ctrl, now, animParams{
		cs:                 es,
		modelID:            int32(ent.ModelID),
		isDead:             ent.HPFlag == 3,
		attackCount:        ent.AttackCount,
		hitTime:            ent.LastDamageTime,
		primaryWeapon:      ent.PrimaryWeapon,
		secondaryWeapon:    ent.SecondaryWeapon,
		shield:             ent.Shield,
		class:              ent.Class,
		level:              ent.Level,
		isPC:               ent.IsPlayer,
		weaponAttackType:   inferAttackType(ent.PrimaryWeapon, ent.SecondaryWeapon, ent.Shield),
		showBattleStance:   true,
		serverDrivenCombat: false,
		castBarEnd:         ent.CastAnimEnd,
		castingSelf:        false,
		baseAnim:           baseLoco,
		dbgLabel:           ent.Name,
	})

	// Resolve final animations from controller.
	locoID, actionID := ctrl.Resolve()
	locoAnim = byte(locoID)

	isMoving := locoAnim == animWalk || locoAnim == animRun || locoAnim == animSwim || locoAnim == animStrafe
	if actionID >= 0 {
		actByte := byte(actionID)
		if actByte != locoAnim {
			if actByte == animDeath || actByte == nhAnimDeath {
				return actByte, 0
			}
			if isMoving {
				actionAnim = actByte
			} else {
				locoAnim = actByte
			}
		} else {
			locoAnim = actByte
		}
	}

	return locoAnim, actionAnim
}

// animDur returns the animation duration for a model, with 800ms fallback.
func (am *AnimationManager) animDur(modelID int32, animByte byte) time.Duration {
	dur := time.Duration(800) * time.Millisecond
	if am.modelMgr != nil {
		if d := am.modelMgr.AnimDuration(modelID, animByte); d > 0 && d <= 2.0 {
			dur = time.Duration(d * float32(time.Second))
		}
	}
	return dur
}

// inferAttackType determines attack type from visible equipment.
// Without item database AttackType, we infer from slot occupancy.
func inferAttackType(primary, secondary, shield uint32) int {
	if primary == 0 {
		return 11 // unarmed → dagger anims
	}
	if shield != 0 {
		return 1 // weapon + shield → 1H slash
	}
	if secondary != 0 {
		return 1 // dual wield → 1H slash
	}
	// Primary only, no secondary, no shield → 2H weapon
	return 2 // default to 2H slash
}

// getWeaponAttackTypeFromItems returns AttackType from parsed inventory items.
// Slot 12=primary, 14=secondary, 15=twohand. Returns 11 if no weapon.
func getWeaponAttackTypeFromItems(items []game.ParsedItem) int {
	for _, item := range items {
		// Only check equipped items
		if item.EquipLocation < 0 || item.Location != -1 {
			continue
		}
		if (item.Slot == 12 || item.Slot == 14 || item.Slot == 15) && item.AttackType > 0 {
			return int(item.AttackType)
		}
	}
	return 11
}

// getWeaponAttackType returns the AttackType of the equipped weapon.
// Slot 12=primary, 14=secondary, 15=twohand.
// Returns 11 (dagger/unarmed) if no weapon equipped — matches PS2 DetachItem
// which resets attackType to 11 (VICSpriteAttackType default).
func (am *AnimationManager) getWeaponAttackType(gs *game.GameState) int {
	if gs == nil {
		return 11
	}
	for _, item := range gs.ParsedItems {
		// Only check equipped weapon items (EquipLocation >= 0 and Location == -1)
		if item.EquipLocation < 0 || item.Location != -1 {
			continue
		}
		if (item.Slot == 12 || item.Slot == 14 || item.Slot == 15) && item.AttackType > 0 {
			return int(item.AttackType)
		}
	}
	return 11
}

// ResetCombat clears all combat animation state. Called on disconnect/escape.
func (am *AnimationManager) ResetCombat() {
	am.playerCombat = combatState{}
	am.InCombat = false
	am.CombatEngaged = false
	if am.playerController != nil {
		am.playerController.ClearAction()
	}
}

// CachedPlayerAnim returns the last-resolved player animation bytes.
// Used by the sound system to avoid re-resolving (which would mutate controller state).
func (am *AnimationManager) CachedPlayerAnim() (loco, action byte) {
	return am.cachedPlayerLoco, am.cachedPlayerAction
}

// CachedEntityAnim returns the last-resolved entity animation bytes.
// Used by the sound system to avoid re-resolving (which would mutate controller state).
func (am *AnimationManager) CachedEntityAnim(objectID uint32) (loco, action byte) {
	if am.cachedEntityAnims == nil {
		return 0, 0
	}
	pair := am.cachedEntityAnims[objectID]
	return pair[0], pair[1]
}
