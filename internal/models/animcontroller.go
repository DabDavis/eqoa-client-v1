package models

import "math/rand"

// SpriteAnimController manages per-entity animation state with priority-gated
// replacement, matching the PS2 VICSprite::SetAnimation behavior (0x00422D78).
//
// PS2 architecture (verified from MIPS disassembly of SetAction at 0x00421770
// and SetLocomotion at 0x00421AA0): BOTH functions call SetAnimation TWICE for
// humanoid (skelType==1) — first with pair=0, then pair=1. The unified priority
// system inside SetAnimation determines which pair(s) accept the animation.
// For non-humanoid (skelType!=1), a single call with pair=0 is made.
//
// This means an attack animation (priority 5) called via SetAction will be
// accepted on BOTH pairs (since attack priority > locomotion priority 2),
// producing a full-body attack. Locomotion called via SetLocomotion will be
// accepted on pair 0 but rejected on pair 1 if a higher-priority action is
// playing. The VIHSprite per-bone-group blending then uses the channel
// assignments to control which bones each pair drives.
//
// PS2 CPlayList: 66 entries at VICSprite+0x1B8, 52 bytes each.
// Entry layout (verified from EE memory dump at 0x010DE918):
//   +0x00 float32  pair0Priority    (gating value: 0.0-1.0, per-model)
//   +0x04 float32  pair1Priority    (always 1.0 for humanoid)
//   +0x08 int32    playHandlePair0  (runtime state, 0 in init)
//   +0x0C int32    playHandlePair1  (runtime state, 0 in init)
//   +0x10 int32    enabled          (1 = loco, 0 = combat/emote)
//   +0x14 int32    animIndexPair0   (upper/primary raw anim index)
//   +0x18 int32    animIndexPair1   (lower/secondary raw anim index)
//   +0x1C float32  speed            (CPlayList playback speed)
//   +0x20 int32    playOnce         (0=loop, 1=play-once)
//   +0x24 int32    locoChannel      (bone-group channel 0-8)
//   +0x28 int32    actionChannel    (bone-group channel 0-8)
//   +0x2C int32    groupRef         (-1 = none)
//   +0x30 int32    sentinel         (-1 for valid entries)
//
// SetAnimPriorities (0x004259F8) sets per-slot CHANNEL indices at +0x24/+0x28.
// Channels 0-8 control per-bone-group blend priority on PS2:
//   0=special idle, 1=battle stance, 2=locomotion, 3=jump/swim-attack,
//   4=hit stun/emotes, 5=combat, 6=mounted/upper, 7=death(p0), 8=death(p1).
// PS2 uses slt (signed integer compare) on these values — higher = higher priority.

// AnimPair identifies which animation pair (locomotion or action).
type AnimPair int

const (
	PairLocomotion AnimPair = 0
	PairAction     AnimPair = 1
)

// PairState tracks the current state of one animation pair.
type PairState struct {
	AnimID   int     // VICSpriteAnimID (-1 = none)
	Priority float32 // PS2 CPlayList pair 0 priority (0.2/0.4/0.6/1.0)
	PlayOnce bool    // true = play-once (hold last frame), false = loop
	Finished bool    // play-once completed (held at last frame)
}

// SpriteAnimController manages the dual-pair animation state per entity.
type SpriteAnimController struct {
	pairs       [2]PairState
	isHumanoid  bool
	idleTimer   float32 // seconds in idle for periodic special idle (PS2: animTimerA)
	idleTarget  float32 // random target: 20-40s (PS2: GetTime() + 20000 + rand()%20000)
	CallerSpeed float32 // locomotion speed multiplier (PS2: sprite[432])
	InCombat    bool    // true while entity is in combat (used by callers for battle stance/BGM)
	combatTimer float32 // seconds since last combat activity (PS2: combatIdleTimer, 5s threshold)
	IsDead      bool    // death flag (PS2: VICSprite+0x1168, forces death animation)
	blendActive [2]bool // per-pair blend tracking (PS2: HasRemainingPlayback per play entry)

	// PS2 paired blend: when locomotion pair is set to same anim as action pair,
	// blend source should be the action pair's current frame position.
	// PS2: skelType==1 && pair==0 && pair1_animID==newAnimID → AddInterpolatedPlayWithSync.
	PairedBlendFromAction bool

	// TriggerResetPending is set when a new animation is accepted, signaling
	// that sound/FX triggers should be reset. PS2: FindTrigger×4 + ResetTrigger×4
	// inside SetAnimation after acceptance (0x00423310). Cleared by caller.
	TriggerResetPending bool
}

// NewSpriteAnimController creates a controller for the given model type.
func NewSpriteAnimController(isHumanoid bool) *SpriteAnimController {
	return &SpriteAnimController{
		pairs: [2]PairState{
			{AnimID: -1},
			{AnimID: -1},
		},
		isHumanoid:  isHumanoid,
		idleTarget:  randomIdleTarget(),
		CallerSpeed: 1.0,
	}
}

// randomIdleTarget returns a random special idle threshold in seconds.
// PS2: animTimerA = VIScene::GetTime() + 20000 + rand()%20000 (absolute ms).
// rand()%20000 gives 0-19999, so total = 20000-39999ms = 20.0-39.999s.
// Go rand.Intn(20000) gives 0-19999 → /1000 gives 0.0-19.999.
func randomIdleTarget() float32 {
	return 20.0 + float32(rand.Intn(20000))/1000.0
}

// SetLocomotion sets the locomotion animation.
// PS2 (0x00421AA0): for humanoid (skelType==1), calls SetAnimation TWICE —
// first with pair=0, then pair=1. The priority system determines acceptance.
// For non-humanoid: single call with pair=0.
func (c *SpriteAnimController) SetLocomotion(animID int) {
	if c.isHumanoid {
		c.setAnimation(animID, PairLocomotion)
		c.setAnimation(animID, PairAction)
	} else {
		c.setAnimation(animID, PairLocomotion)
	}
}

// SetAction sets the action animation.
// PS2 (0x00421770): for humanoid (skelType==1), calls SetAnimation TWICE —
// first with pair=0, then pair=1. For non-humanoid: single call with pair=0.
// Death guard: when dead, PS2's SetAnimation forces animID=14 inside, but
// we pre-reject non-death anims here as a fast path (same net result).
func (c *SpriteAnimController) SetAction(animID int) {
	if c.IsDead {
		if c.isHumanoid && animID != 14 {
			return
		}
		if !c.isHumanoid && animID != 15 {
			return
		}
	}
	if c.isHumanoid {
		c.setAnimation(animID, PairLocomotion)
		c.setAnimation(animID, PairAction)
	} else {
		c.setAnimation(animID, PairAction)
	}
}

// ClearAction resets pair 1 to empty (no action playing).
// PS2 dual-call: if pair 0 was set to the same animation (from SetAction driving
// both pairs), clear it too so locomotion can reclaim pair 0.
func (c *SpriteAnimController) ClearAction() {
	actionAnim := c.pairs[PairAction].AnimID
	c.pairs[PairAction] = PairState{AnimID: -1}
	if actionAnim >= 0 && c.pairs[PairLocomotion].AnimID == actionAnim {
		c.pairs[PairLocomotion] = PairState{AnimID: -1}
	}
}

// setAnimation implements PS2 SetAnimation (0x00422D78) — the unified entry
// point that both SetLocomotion and SetAction call on both pairs. All animation
// overrides (combat idle, swim/mounted, death) happen inside this function,
// matching PS2 exactly. The priority system uses per-pair channel values from
// the CPlayList to determine acceptance.
func (c *SpriteAnimController) setAnimation(animID int, pair AnimPair) {
	p := &c.pairs[pair]

	// --- PS2 Phase 1: Special idle override (0x422D88) ---
	// When specialIdleTimer expired and idle requested → force special idle.
	// PS2 checks inside SetAnimation; Go handles externally via UpdateIdleTimer.
	// Functionally equivalent — the timer fires and SetLocomotion(15) is called.

	// --- PS2 Phase 2: Combat idle override (0x422DB8) ---
	// If animID==0 (idle) and combat timer < 5s → force animID=17 (combat idle).
	// PS2 also checks GetTime() < 5001 as startup guard (skipped — negligible).
	if animID == 0 && c.isHumanoid && c.combatTimer < 5.0 {
		animID = 17
	}

	// --- PS2 Phase 3a: Swim/mounted combat override (0x422E60) ---
	// When combat animID (0x16-0x35, not 0x22) and other pair has swim/mounted,
	// force animID=44 (mounted attack). Prevents standing attack while swimming.
	if c.isHumanoid && animID >= 0x16 && animID <= 0x35 && animID != 0x22 {
		otherPair := PairAction
		if pair == PairAction {
			otherPair = PairLocomotion
		}
		otherAnimID := c.pairs[otherPair].AnimID
		if otherAnimID == 12 || otherAnimID == 13 || otherAnimID == 44 {
			animID = 44
		}
	}

	// --- PS2 Phase 3b: Death override (0x422EC8) ---
	// When isDead, force animID=14 unconditionally.
	if c.IsDead && c.isHumanoid {
		animID = 14
	}

	// --- PS2 Stage 1: Blend-active check (0x422ED8) ---
	// During active blend (IsBlendComplete==false), compare priorities.
	// Higher/equal → FreezePlayback + ErasePlay. Lower → reject.
	if c.blendActive[pair] && c.isHumanoid {
		newPri := c.getAnimPriorityForPair(animID, pair)
		if newPri < p.Priority {
			return
		}
		c.blendActive[pair] = false
	}

	// --- PS2: Same-animID rejection (0x423174) ---
	// If new == current and playHandle != -1 → return 0 (no change).
	if p.AnimID == animID {
		return
	}

	// --- PS2 Stage 2: Priority gating (0x422F9C..0x423068) ---
	switch {
	case p.AnimID == -1:
		// Empty pair → always accept.

	case !p.Finished:
		// Still playing: slt priority comparison → reject if strictly lower.
		newPri := c.getAnimPriorityForPair(animID, pair)
		if newPri < p.Priority {
			return
		}

	default:
		// Stopped/finished (IsPlaybackStopped returns true).
		// PS2 priority gate at 0x423068: rejection requires
		//   (stopped_flag != 1 OR IsPlaybackStopped == false)
		// For truly stopped, this is FALSE → no priority rejection.
		// PS2 accepts ANY animation over a stopped one.
	}

	// --- PS2: Paired blend detection (0x00423260) ---
	// When humanoid pair=0 and pair 1 has same animID: use
	// AddInterpolatedPlayWithSync for smooth cross-pair blend.
	if c.isHumanoid && pair == PairLocomotion && c.pairs[PairAction].AnimID == animID {
		c.PairedBlendFromAction = true
	} else if pair == PairLocomotion {
		c.PairedBlendFromAction = false
	}

	// --- PS2: Combat timer update (0x423120) ---
	// Reset combatIdleTimer for combat anims, EXCLUDING spell-related slots.
	// PS2: (animID 22-43 OR 50-53) AND NOT (31,32,33,35,37,38,39) → reset.
	if c.isHumanoid && isCombatTimerAnim(animID) {
		c.combatTimer = 0
	}

	// --- Accept animation ---
	p.AnimID = animID
	p.Priority = c.getAnimPriorityForPair(animID, pair)
	p.PlayOnce = c.getPlayOnce(animID)
	p.Finished = false

	// --- PS2: Trigger reset (0x423310) ---
	// FindTrigger×4 + ResetTrigger×4 after acceptance. Clears sound/FX
	// event triggers so they can fire again on the new animation.
	c.TriggerResetPending = true
}

// isCombatTimerAnim returns true if the animID should reset the combat idle timer.
// PS2 SetAnimation (0x423120): resets combatIdleTimer for combat anims 22-43 + 50-53,
// EXCLUDING spell-related slots: 31, 32, 33, 35, 37, 38, 39.
func isCombatTimerAnim(animID int) bool {
	if (animID >= 22 && animID <= 43) || (animID >= 50 && animID <= 53) {
		switch animID {
		case 31, 32, 33, 35, 37, 38, 39:
			return false
		}
		return true
	}
	return false
}

// Resolve returns the current animation IDs for locomotion and action.
// Returns (locoID, actionID) where actionID is -1 if no action is active.
func (c *SpriteAnimController) Resolve() (locoID, actionID int) {
	locoID = c.pairs[PairLocomotion].AnimID
	if locoID < 0 {
		locoID = 0 // default to idle
	}
	actionID = c.pairs[PairAction].AnimID
	return
}

// IsActionFinished returns true if the action pair's play-once animation
// has completed (held at last frame).
func (c *SpriteAnimController) IsActionFinished() bool {
	return c.pairs[PairAction].Finished
}

// IsActionPlayOnce returns true if the current action animation is play-once.
func (c *SpriteAnimController) IsActionPlayOnce() bool {
	return c.pairs[PairAction].PlayOnce
}

// IsActionActive returns true if an action animation is playing and not finished.
func (c *SpriteAnimController) IsActionActive() bool {
	return c.pairs[PairAction].AnimID >= 0 && !c.pairs[PairAction].Finished
}

// ActionAnimID returns the current action animation ID (-1 if none).
func (c *SpriteAnimController) ActionAnimID() int {
	return c.pairs[PairAction].AnimID
}

// LocoAnimID returns the current locomotion animation ID (0 = idle).
func (c *SpriteAnimController) LocoAnimID() int {
	id := c.pairs[PairLocomotion].AnimID
	if id < 0 {
		return 0
	}
	return id
}

// MarkActionFinished is called by applySkeletal when a play-once action
// animation's time reaches its duration.
// PS2 dual-call: if pair 0 has the same animation (from SetAction driving
// both pairs), mark it finished too so locomotion can replace it.
func (c *SpriteAnimController) MarkActionFinished() {
	c.pairs[PairAction].Finished = true
	if c.pairs[PairLocomotion].AnimID == c.pairs[PairAction].AnimID {
		c.pairs[PairLocomotion].Finished = true
	}
}

// SetBlendActive sets the per-pair blend-active flag. Called from applySkeletal
// to sync the AnimState's blend progress back to the controller for rejection
// gating. PS2: each play entry has independent HasRemainingPlayback.
func (c *SpriteAnimController) SetBlendActive(pair AnimPair, active bool) {
	c.blendActive[pair] = active
}

// Update advances frame-rate-dependent timers. Called once per frame from
// applySkeletal with the frame delta time.
func (c *SpriteAnimController) Update(dt float32) {
	// PS2 combat idle timer: combatIdleTimer is set to GetTime() inside
	// SetAnimation when a combat anim is accepted (see isCombatTimerAnim).
	// The timer advances by dt each frame. In SetAnimation, combat idle is
	// forced when combatTimer < 5.0 (PS2: GetTime() - combatIdleTimer < 5000).
	c.combatTimer += dt
}

// UpdateIdleTimer tracks idle time and returns a special idle animation ID
// when the entity has been idle long enough. Returns -1 if no special
// idle should play. Resets when movement detected.
//
// PS2 animTimerA (VICSprite+0x11B0): random special idle timer.
// Fires at 20000ms + rand()%20000 (20-40 seconds). When timer expires
// during idle, triggers special idle animation (CPlayList slot 15 humanoid, F8 ID 6 creature).
// Timer is recalculated with a new random target after each trigger.
func (c *SpriteAnimController) UpdateIdleTimer(dt float32, isMoving bool) int {
	if isMoving || c.pairs[PairAction].AnimID >= 0 {
		c.idleTimer = 0
		return -1
	}
	c.idleTimer += dt
	if c.idleTimer >= c.idleTarget {
		if c.isHumanoid {
			return 15 // humanoid special idle (CPlayList slot 15)
		}
		return 6 // non-humanoid special idle (F8 ID 6 → CPlayList slot 17 Special)
	}
	return -1
}

// ResetIdleTimer resets the idle timer and picks a new random target
// (called after special idle completes).
func (c *SpriteAnimController) ResetIdleTimer() {
	c.idleTimer = 0
	c.idleTarget = randomIdleTarget()
}

// getAnimPriorityForPair returns the priority for a given animID and pair.
// PS2 stores per-pair priorities at CPlayList[animID*0x34 + pair*4].
// These are integer channel indices (0-8) compared via slt.
func (c *SpriteAnimController) getAnimPriorityForPair(animID int, pair AnimPair) float32 {
	if c.isHumanoid {
		if pair == PairAction {
			return humanoidPair1Priority(animID)
		}
		return humanoidPriority(animID)
	}
	return nonHumanoidPriority(animID)
}

// getAnimPriority returns the priority for the given animID using pair 0 (default).
func (c *SpriteAnimController) getAnimPriority(animID int) float32 {
	return c.getAnimPriorityForPair(animID, PairLocomotion)
}

// getPlayOnce returns whether the animation plays once (true) or loops (false).
//
// PS2 source: CPlayList entry playOnce at this+0x01B8 + animID*52 + 0x20.
// applySkeletal also reads PlayOnce from the actual CPlayList data via
// resolvePlayList — this function is used by the controller for tracking
// when an action animation has completed for priority replacement purposes.
func (c *SpriteAnimController) getPlayOnce(animID int) bool {
	if c.isHumanoid {
		return humanoidPlayOnce(animID)
	}
	return nonHumanoidPlayOnce(animID)
}

// --- Humanoid animation priority tables ---
// From PS2 SetAnimPriorities (0x004259F8) — writes channel indices to all 66
// CPlayList entries. These are the SAME for all humanoid VICSprites.
// PS2 uses slt (signed integer compare) on these values — higher = higher priority.
// Channels 0-8: 0=lowest (special idle), 2=locomotion, 5=combat, 7=death, 8=death(pair1).

// humanoidPair0Channel maps CPlayList slot → pair 0 (locomotion layer) channel.
var humanoidPair0Channel = map[int]int{
	0: 2, 1: 2, 2: 2, 3: 2, 4: 2, 5: 2, 6: 2,
	7: 3, 8: 3, 9: 3, 10: 2, 11: 2, 12: 2, 13: 2,
	14: 7, // death
	15: 0, // special idle (lowest — anything can interrupt)
	16: 1, // battle stance (pair0=1, very low)
	17: 2, // combat idle
	18: 6, // mounted combat
	22: 5, 23: 5, 24: 5, 25: 5, 26: 5, 27: 5, 28: 5, 29: 5,
	30: 5, 31: 5, 32: 5, 33: 5,
	34: 4, // hit stun (below attacks at 5, above loco at 2)
	35: 5, 36: 5,
	37: 5, 38: 5, 39: 5, // spells
	40: 5, 41: 5, 42: 5,
	43: 5, 44: 5, 45: 5, 46: 5,
	50: 5, 51: 5, 52: 5, 53: 5,
	56: 4, 57: 4, 59: 4, 60: 4, 61: 4, 63: 4, 64: 4, 65: 4,
}

// humanoidPair1Channel maps CPlayList slot → pair 1 (action layer) channel.
var humanoidPair1Channel = map[int]int{
	0: 2, 1: 6, 2: 6, 3: 6, 4: 6, 5: 6, 6: 6,
	7: 7, 8: 3, 9: 3, 10: 6, 11: 6, 12: 6, 13: 6,
	14: 8, // death (highest)
	15: 0, // special idle
	16: 1, // battle stance
	17: 1, // combat idle (pair1 low — allows action to override)
	18: 7, // mounted combat
	22: 5, 23: 5, 24: 5, 25: 5, 26: 5, 27: 5, 28: 5, 29: 5,
	30: 5, 31: 5, 32: 5, 33: 5,
	34: 4, // hit stun
	35: 5, 36: 5,
	37: 5, 38: 5, 39: 5,
	40: 5, 41: 5, 42: 5,
	43: 7, 44: 7, // emotes (high on pair1)
	45: 5, 46: 5,
	50: 5, 51: 5, 52: 6, 53: 6,
	56: 4, 57: 4, 59: 4, 60: 4, 61: 4, 63: 4, 64: 4, 65: 4,
}

func humanoidPriority(animID int) float32 {
	ch, ok := humanoidPair0Channel[animID]
	if !ok {
		ch = 2
	}
	return float32(ch)
}

// humanoidPair1Priority returns pair 1 priority for action layer.
func humanoidPair1Priority(animID int) float32 {
	ch, ok := humanoidPair1Channel[animID]
	if !ok {
		ch = 5
	}
	return float32(ch)
}

// humanoidPlayOnce determines play-once behavior for ESF CPlayList slot numbers.
// Verified against ESF CPlayList PlayOnce field values from CHAR.ESF.
// animID is an ESF CPlayList slot number (0, 1, 3, 4, 6, 7, 10, 12-15, 17, 22-44, 56-61).
//
// Looping slots (PlayOnce=0): 0=idle, 1=walk, 2=walkBack, 3=run, 4=swim, 5=swimBack,
//   6=fall, 10=strafe, 11=strafeBack, 12-13=swim2/swimSlow, 17=battleStance.
// Play-once slots (PlayOnce=1): 7=landing, 14=death, 15=specialIdle,
//   22-44=combat/spells, 56-61=emotes.
var humanoidLoopSlots = map[int]bool{
	0: true, 1: true, 2: true, 3: true, 4: true, 5: true,
	6: true, 10: true, 11: true, 12: true, 13: true, 17: true,
}

func humanoidPlayOnce(animID int) bool {
	return !humanoidLoopSlots[animID]
}

// --- Non-humanoid (creature/monster) animation tables ---
//
// All 47 creature VICSprites in the EE dump have 66-entry CPlayLists
// (19 creature-specific + 47 humanoid-compatible entries at slots 22-65).
// The first 19 entries define the creature animation set:
//
//   CPlayList slot → Animation:
//   0=Idle, 1=Walk, 2=WalkBackward, 3=Run, 4=Strafe, 5=StrafeBack,
//   6=Turn, 7=JumpLand, 8=Attack1, 9=Attack2, 10=HitReaction,
//   11=SpellCast, 12=Swim, 13=SwimFast, 14=Emote1, 15=Death,
//   16=BattleStance, 17=Special, 18=IdleBattle
//
// F8 debug cycling on PS2 shows 12 of these 19 slots (skipping WalkBack,
// Strafe, StrafeBack, Turn, JumpLand, Swim, SwimFast). The F8 cycling IDs
// do NOT map 1:1 to CPlayList slots — they skip movement variants:
//
//   F8 ID → CPlayList slot:
//   0(idle)→0, 1(walk)→1, 2(run)→3, 3(run2)→?, 4(fall)→?,
//   5(death)→15, 6(special)→17, 7(stance)→16, 8(atk1)→8,
//   9(atk2)→9, 10(hit)→10, 11(cast)→11
//
// nhAnim* constants are CPlayList slot numbers (verified from ESF scan).
// Slots 8/9/16 do NOT exist on SkelType 0/2/3 models — attacks at 22/23.
//
// PS2 creature priorities vary by creature type (9 distinct patterns found).
// Battle stance and idle-battle are always 1.0; idle is always 0.0; other
// slots range from 0.2-1.0 depending on creature type.
//
// PS2 PlayOnce: Creature attacks/hit/spell are playOnce=0 (loop) on PS2 —
// the server controls when to stop. Our client uses time-gated play-once
// for these since entity attacks are controlled by AttackAnimEnd timers.

func nonHumanoidPriority(animID int) float32 {
	// animID values are CPlayList slots (verified from ESF scan of SkelType 0/2/3).
	// Creature CPlayList slots: 0,1,2,3,4,5,6,7,10,11,12,13,14,15,17,22,23,30,34,37-39,43,44
	switch animID {
	case 0:
		return 0.0 // Idle
	case 1:
		return 0.2 // Walk
	case 2:
		return 0.4 // WalkBackward
	case 3:
		return 0.4 // Run
	case 4, 5:
		return 0.4 // Strafe/StrafeBack
	case 6:
		return 0.4 // Turn
	case 7:
		return 1.0 // JumpLand
	case 12, 13:
		return 0.4 // Swim/SwimFast
	case 14:
		return 1.0 // Emote
	case 15:
		return 0.6 // Death
	case 17:
		return 1.0 // BattleStance/Special
	case 22, 23:
		return 0.2 // Attack1/2
	case 30:
		return 0.2 // Attack extra
	case 34:
		return 0.1 // HitReaction
	case 37, 38, 39:
		return 0.4 // SpellCast
	case 43, 44:
		return 1.0 // Emote3/4
	default:
		return 0.2
	}
}

func nonHumanoidPlayOnce(animID int) bool {
	// PS2 CPlayList PlayOnce values (creature slots):
	//   7=JumpLand(true), 14=Emote(true), 15=Death(true)
	//   All others including attacks/hit/spell = false (loop, server-controlled).
	// Our client keeps attack/hit/spell as play-once because entity combat
	// is time-gated by AttackAnimEnd / LastDamageTime timers.
	// animID values are CPlayList slots (verified from ESF scan of SkelType 0/2/3).
	// PS2 CPlayList PlayOnce: 7=JumpLand(1), 14=Emote(1), 15=Death(1), all others=0.
	// Go override: attacks/hit/spell are play-once because our server doesn't
	// send explicit animation-stop commands like SOE's did.
	switch animID {
	case 7: // JumpLand — play once (PS2: playOnce=1)
		return true
	case 14: // Emote — play once (PS2: playOnce=1)
		return true
	case 15: // Death — play once (PS2: playOnce=1)
		return true
	case 22, 23, 30: // Attack — play once (PS2: loop, but Go needs timer-gated end)
		return true
	case 34: // HitReaction — play once (PS2: loop, but Go needs timer-gated end)
		return true
	case 37, 38, 39: // SpellCast — play once (PS2: loop, but Go needs timer-gated end)
		return true
	default:
		return false
	}
}

