package models

import (
	"math"
	"math/rand"
)

// SpriteAnimController manages per-entity animation state with priority-gated
// replacement, matching the PS2 VICSprite::SetAnimation behavior (0x00422D78,
// decomp/src/vicsprite.cpp).
//
// PS2 architecture: SetAction and SetLocomotion both call SetAnimation once
// per LAYER — layers 0 and 1 for a humanoid (skelType 1), layer 0 alone for
// any other skeleton. Layer 0 is the upper body and layer 1 the lower, and
// each has its own priority table: an attack (5) takes the upper layer from a
// walk (2) but not the lower (walk is 6 there), so a character attacking on
// the move swings its arms and keeps walking; standing still, the attack
// takes both layers (idle is 2 on each) and plays full-body.
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

// AnimPair identifies one of VICSprite's two animation LAYERS. Layer 0 drives
// the upper body (its animations are each CPlayList entry's m_animation[0],
// and its triggers the BPU ones), layer 1 the lower body (m_animation[1],
// BPL). A humanoid plays every request on both layers, each gated by its own
// priority table, so walking while attacking keeps the walk in the legs (6
// beats 5 on layer 1) and the swing in the arms (5 beats 2 on layer 0). Any
// other skeleton uses layer 0 alone (VICSprite::SetAction/SetLocomotion).
type AnimPair int

const (
	LayerUpper AnimPair = 0
	LayerLower AnimPair = 1
)

// PairState tracks the current state of one animation pair.
type PairState struct {
	AnimID   int     // VICSpriteAnimID (-1 = none)
	Priority float32 // PS2 CPlayList pair 0 priority (0.2/0.4/0.6/1.0)
	PlayOnce bool    // true = play-once (hold last frame), false = loop
	Finished bool    // play-once completed (held at last frame)
}

// SpriteAnimController manages the two-layer animation state per entity.
type SpriteAnimController struct {
	pairs       [2]PairState // indexed by layer: LayerUpper, LayerLower
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

	// actionSeq and action are VICSprite's m_actionSeq and m_action: every
	// SetAction and SetLocomotion takes the next sequence number, and action
	// is the one whose layer-0 request was last accepted — what
	// IsActionComplete compares a handle against.
	actionSeq int
	action    int

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
		action:      -1,
		idleTarget:  randomIdleTarget(),
		CallerSpeed: 1.0,
		// m_idleTime starts at 0 and the scene clock runs, so the combat-idle
		// rule (idle within 5 s of an attack becomes 17) never fires before a
		// first attack: no battle stance at spawn.
		combatTimer: math.MaxFloat32,
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
	if c.setAnimation(animID, LayerUpper) {
		c.action = c.actionSeq
	}
	if c.isHumanoid {
		c.setAnimation(animID, LayerLower)
	}
	c.actionSeq++
}

// SetAction sets an action animation — VICSprite::SetAction (0x00421770),
// which is SetLocomotion's twin: both layers for a humanoid, layer 0 alone
// otherwise. When dead, setAnimation turns every request into death (14).
// It returns the action's handle for IsActionComplete, whether or not the
// animation was accepted.
func (c *SpriteAnimController) SetAction(animID int) int {
	if c.setAnimation(animID, LayerUpper) {
		c.action = c.actionSeq
	}
	if c.isHumanoid {
		c.setAnimation(animID, LayerLower)
	}
	h := c.actionSeq
	c.actionSeq++
	return h
}

// IsActionComplete is VICSprite::IsActionComplete: an action is over once
// anything later has taken layer 0 (or it never did — a rejected action is
// complete at once), or when layer 0's play has stopped. An empty layer 0
// counts as stopped: only ClearAction, which the PS2 lacks, empties one.
func (c *SpriteAnimController) IsActionComplete(handle int) bool {
	if c.action != handle {
		return true
	}
	return c.pairs[LayerUpper].AnimID < 0 || c.pairs[LayerUpper].Finished
}

// ClearAction ends any action still holding a layer, so the next
// SetLocomotion takes it. The PS2 has no such call — a play-once action
// simply stops and anything replaces a stopped play — but the combat code
// uses it to end time-gated actions. Layers playing a locomotion animation
// are left alone: clearing those froze the legs at idle while moving ("walk
// shows idle / sliding").
func (c *SpriteAnimController) ClearAction() {
	for i := range c.pairs {
		if id := c.pairs[i].AnimID; id >= 0 && !isLocomotionAnim(id) {
			c.pairs[i] = PairState{AnimID: -1}
		}
	}
}

// isLocomotionAnim reports whether a CPlayList slot is a locomotion-layer
// animation — the only slots SetLocomotion is ever called with (idle, walk,
// walkBack, run, swim, fall, landing, strafe, special idle, battle/combat idle,
// mounted). Used by ClearAction to avoid wiping locomotion when the action pair
// happens to mirror it.
func isLocomotionAnim(animID int) bool {
	switch animID {
	case 0, 1, 2, 3, 4, 5, 6, 7, 10, 11, 12, 13, 15, 16, 17, 18:
		return true
	}
	return false
}

// setAnimation implements PS2 SetAnimation (0x00422D78) — the unified entry
// point that both SetLocomotion and SetAction call on both pairs. All animation
// overrides (combat idle, swim/mounted, death) happen inside this function,
// matching PS2 exactly. The priority system uses per-pair channel values from
// the CPlayList to determine acceptance. It reports whether the request was
// accepted, as SetAnimation's return does.
func (c *SpriteAnimController) setAnimation(animID int, pair AnimPair) bool {
	p := &c.pairs[pair]

	// --- PS2 Phase 1: Special idle override (0x422D88) ---
	// When specialIdleTimer expired and idle requested → force special idle.
	// PS2 checks inside SetAnimation; Go handles externally via UpdateIdleTimer.
	// Functionally equivalent — the timer fires and SetLocomotion(15) is called.

	// --- PS2 Phase 2: Combat idle override (0x422DB8) ---
	// Idle within 5 s of the last attack becomes 17 (combat idle), for every
	// skeleton type.
	if animID == 0 && c.combatTimer < 5.0 {
		animID = 17
	}

	// --- PS2 Phase 3a: Swim/mounted combat override (0x422E60) ---
	// A combat animation (22..53, not 34) while swimming becomes 44, the
	// swimming attack. A humanoid looks at its LOWER layer's animation, any
	// other skeleton at layer 0's, and only when setting layer 0.
	if animID >= 22 && animID <= 53 && animID != 34 {
		var other int
		switch {
		case c.isHumanoid:
			other = c.pairs[LayerLower].AnimID
		case pair == LayerUpper:
			other = c.pairs[LayerUpper].AnimID
		default:
			other = -1
		}
		if other == 12 || other == 13 || other == 44 {
			animID = 44
		}
	}

	// --- PS2 Phase 3b: Death override (0x422EC8) ---
	// When dead, every request is death — slot 14 for every skeleton type.
	if c.IsDead {
		animID = 14
	}

	// --- PS2 Stage 1: Blend-active check (0x422ED8) ---
	// During active blend (IsBlendComplete==false), compare priorities.
	// Higher/equal → FreezePlayback + ErasePlay. Lower → reject.
	if c.blendActive[pair] {
		newPri := c.getAnimPriorityForPair(animID, pair)
		if newPri < p.Priority {
			return false
		}
		c.blendActive[pair] = false
	}

	// --- PS2: Same-animID rejection (0x423174) ---
	// If new == current and playHandle != -1 → return 0 (no change).
	if p.AnimID == animID {
		return false
	}

	// --- PS2 Stage 2: Priority gating (0x422F9C..0x423068) ---
	switch {
	case p.AnimID == -1:
		// Empty pair → always accept.

	case !p.Finished:
		// Still playing: slt priority comparison → reject if strictly lower.
		newPri := c.getAnimPriorityForPair(animID, pair)
		if newPri < p.Priority {
			return false
		}

	default:
		// Stopped/finished (IsPlaybackStopped returns true).
		// PS2 priority gate at 0x423068: rejection requires
		//   (stopped_flag != 1 OR IsPlaybackStopped == false)
		// For truly stopped, this is FALSE → no priority rejection.
		// PS2 accepts ANY animation over a stopped one.
	}

	// --- PS2: Paired blend detection (0x00423260) ---
	// When humanoid layer 0 takes the animation layer 1 already plays, it
	// starts in sync with it (AddInterpolatedPlayWithSync).
	if c.isHumanoid && pair == LayerUpper && c.pairs[LayerLower].AnimID == animID {
		c.PairedBlendFromAction = true
	} else if pair == LayerUpper {
		c.PairedBlendFromAction = false
	}

	// --- PS2: Combat timer update (0x423120) ---
	// m_idleTime = now for combat anims, EXCLUDING spell-related slots.
	// PS2: (animID 22-43 OR 50-53) AND NOT (31,32,33,35,37,38,39) → reset.
	if isCombatTimerAnim(animID) {
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
	return true
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

// Resolve returns the animation each half of the body plays: the upper body
// layer 0's, the lower body layer 1's (a non-humanoid has only layer 0, which
// drives both). An empty layer plays idle.
func (c *SpriteAnimController) Resolve() (upper, lower int) {
	upper = c.pairs[LayerUpper].AnimID
	if upper < 0 {
		upper = 0
	}
	lower = upper
	if c.isHumanoid {
		lower = c.pairs[LayerLower].AnimID
		if lower < 0 {
			lower = 0
		}
	}
	return
}

// IsActionFinished reports whether layer 0's play-once animation has
// completed — the half of VICSprite::IsActionComplete that tests whether the
// upper layer's play has stopped.
func (c *SpriteAnimController) IsActionFinished() bool {
	return c.pairs[LayerUpper].Finished
}

// IsActionPlayOnce returns true if layer 0's animation is play-once.
func (c *SpriteAnimController) IsActionPlayOnce() bool {
	return c.pairs[LayerUpper].PlayOnce
}

// IsActionActive returns true if layer 0 plays an action that has not finished.
func (c *SpriteAnimController) IsActionActive() bool {
	id := c.pairs[LayerUpper].AnimID
	return id >= 0 && !isLocomotionAnim(id) && !c.pairs[LayerUpper].Finished
}

// ActionAnimID returns layer 0's animation when it is an action (-1 if none):
// every action reaches layer 0, whose locomotion priorities are the lowest.
func (c *SpriteAnimController) ActionAnimID() int {
	if id := c.pairs[LayerUpper].AnimID; id >= 0 && !isLocomotionAnim(id) {
		return id
	}
	return -1
}

// LocoAnimID returns the animation the legs play (0 = idle): layer 1 for a
// humanoid, layer 0 otherwise.
func (c *SpriteAnimController) LocoAnimID() int {
	_, lower := c.Resolve()
	return lower
}

// MarkActionFinished is called by applySkeletal when the upper body's
// play-once animation reaches its end: layer 0 has stopped, and layer 1 too
// when it plays the same animation, so the next SetLocomotion replaces them.
func (c *SpriteAnimController) MarkActionFinished() {
	c.pairs[LayerUpper].Finished = true
	if c.pairs[LayerLower].AnimID == c.pairs[LayerUpper].AnimID {
		c.pairs[LayerLower].Finished = true
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
	if isMoving || c.ActionAnimID() >= 0 {
		c.idleTimer = 0
		return -1
	}
	c.idleTimer += dt
	if c.idleTimer >= c.idleTarget {
		return 15 // special idle: slot 15 for every skeleton (SetAnimation)
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
// Every CSprite gets the same table (VIESFParse::ParseCSpriteObj calls
// SetAnimPriorities whatever the skeleton), and a non-humanoid only ever uses
// layer 0's.
func (c *SpriteAnimController) getAnimPriorityForPair(animID int, pair AnimPair) float32 {
	if pair == LayerLower {
		return humanoidPair1Priority(animID)
	}
	return humanoidPriority(animID)
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

