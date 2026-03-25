package models

import "math/rand"

// SpriteAnimController manages per-entity animation state with priority-gated
// replacement, matching the PS2 VICSprite::SetAnimation behavior (0x00422D78).
//
// PS2 architecture: SetLocomotion and SetAction both call SetAnimation with
// pair=0 for skeletal sprites. The VIHSprite internally handles per-bone
// priority blending between locomotion and action layers. Our Go implementation
// uses an explicit dual-pair model (pair 0 = locomotion, pair 1 = action) that
// achieves the same visual result: action drives upper body, locomotion drives
// lower body. The priority gating logic (accept if empty, finished, or
// newPri >= curPri) matches the PS2 exactly.
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
// Priority values at +0x00/+0x04 are set during PS2 initialization and
// VARY by character model (verified: 32 humanoid VICSprites have different
// priority sets). Not stored in ESF CPlayList (type 0x2910). Source unknown
// — likely computed from bone-group channel assignments or model properties.
// SetDefaultEntries (0x004251C0) fills uninitialized slots by copying from
// templates: slot 0 (idle) for locomotion, slots 22/23 (attack) for combat.
//
// SetAnimPriorities (0x004259F8) sets per-slot CHANNEL indices at +0x24/+0x28
// (NOT the priority floats). Channels 0-8 control per-bone-group blend
// priority on PS2 (channel 2=legs, 5=full body, 6=upper body, 7-8=override).
// Our dual-pair model approximates this: pair 0=locomotion(legs),
// pair 1=action(upper body).
//
// Priority values extracted from EE memory dump: multiple humanoid VICSprites
// cross-referenced. CPlayList at +0x01B8, 52-byte entries, pair 0 priority
// at entry+0x00 (float32). PS2 uses slt (signed integer compare) on raw
// IEEE 754 bit patterns — works correctly because all values are positive
// floats whose bit patterns preserve magnitude ordering.
//
// Pair 1 (action layer) priority is always 1.0 for all humanoid animations
// on PS2. Since our dual-pair model separates locomotion and action into
// different pairs (no cross-pair competition), we use pair 0 priorities for
// both pairs to get correct relative ordering within each pair.

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
	idleTarget  float32 // random target: 20 + rand(20) seconds (PS2: GetTime() + 20000 + rand()%20000)
	CallerSpeed float32 // locomotion speed multiplier (PS2: sprite[432])
	InCombat    bool    // true while entity is in combat (PS2: animTimerB gating)
	combatTimer float32 // seconds since last combat activity (PS2: animTimerB, 5s threshold)
	IsDead      bool    // death flag (PS2: VICSprite+0x1168, forces death animation)
	blendActive [2]bool // per-pair blend tracking (PS2: HasRemainingPlayback per play entry)
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
// As relative duration this is 20 + rand(20) seconds — exact match.
func randomIdleTarget() float32 {
	return 20.0 + float32(rand.Intn(20))
}

// SetLocomotion sets the locomotion (pair 0) animation.
// PS2 combat idle override (animTimerB at VICSprite+0x11B8):
//   - If animID==0 (idle requested) and entity is in combat with timer < 5s,
//     force animID=17 (combat idle) instead. PS2 also forces combat idle
//     when VIScene::GetTime() < 5001 (first 5s of scene life — startup guard).
//     We skip the startup guard (negligible impact).
func (c *SpriteAnimController) SetLocomotion(animID int) {
	// PS2 Phase 2: combat idle override.
	// Force combat idle when idle requested during active combat.
	if animID == 0 && c.InCombat && c.isHumanoid && c.combatTimer < 5.0 {
		animID = 17 // combat idle (VICSpriteAnimID 17)
	}

	// PS2 Phase 3: death state override.
	if c.IsDead && c.isHumanoid {
		animID = 14
	}

	// Locomotion pair always accepts — no priority gating.
	// PS2: locomotion is directly set every frame from movement state.
	// Priority gating only applies to the action pair.
	p := &c.pairs[PairLocomotion]
	if p.AnimID == animID {
		return // same anim, no change
	}
	p.AnimID = animID
	p.Priority = c.getAnimPriority(animID)
	p.PlayOnce = c.getPlayOnce(animID)
	p.Finished = false
}

// SetAction sets the action (pair 1) animation.
// PS2 Phase 3 death-state guard: when entity is dead, reject combat animations.
// This prevents attack swings from overriding the death pose — important because
// death anims are loops (playOnce=false), so the "playing → always accept" rule
// in setAnimation would otherwise let combat animations through.
func (c *SpriteAnimController) SetAction(animID int) {
	if c.IsDead {
		// Only allow death animation through when dead.
		if c.isHumanoid {
			// CPlayList slot 14 = death.
			if animID != 14 {
				return
			}
		} else {
			// Non-humanoid: only allow death (F8 ID 5).
			if animID != 5 {
				return
			}
		}
	}
	c.setAnimation(animID, PairAction)
}

// ClearAction resets pair 1 to empty (no action playing).
func (c *SpriteAnimController) ClearAction() {
	c.pairs[PairAction] = PairState{AnimID: -1}
}

// setAnimation implements PS2 SetAnimation priority gating (0x00422D78).
//
// PS2 two-stage accept/reject (from decompilation):
//   Stage 1 (blend active): IsBlendComplete check → if blend in progress,
//     compare priorities. Higher/equal → FreezePlayback + ErasePlay.
//     Lower → reject.
//   Stage 2 (normal): IsPlaybackStopped check.
//     Still playing: priority comparison (slt on float bits) → reject if lower.
//     Stopped/finished: priority gating + idle guard.
//
// Both playing AND stopped animations are protected by priority on PS2.
// Locomotion (0.2) cannot interrupt attacks (0.4). Only equal or higher
// priority animations (death=1.0, other attacks=0.4) can interrupt.
func (c *SpriteAnimController) setAnimation(animID int, pair AnimPair) {
	p := &c.pairs[pair]

	// PS2 Stage 1: FreezePlayback (0x00446FD8).
	// During active blend, compare priorities. Higher/equal → freeze current
	// animation at its current frame and allow replacement. Lower → reject.
	// PS2: IsBlendComplete → if not complete, slt priority comparison,
	// then FreezePlayback(current) + ErasePlay(prev).
	if c.blendActive[pair] && c.isHumanoid {
		newPri := c.getAnimPriority(animID)
		if newPri < p.Priority {
			return // lower priority rejected during blend
		}
		// Equal or higher priority: freeze current pose, allow replacement.
		c.blendActive[pair] = false
	}

	// Same animation already set (playing or finished) → no change.
	// PS2: playing same anim → return -1; stopped same anim → STAGE2 reject.
	if p.AnimID == animID {
		return
	}

	// PS2 Stage 2: IsPlaybackStopped check + priority gating.
	switch {
	case p.AnimID == -1:
		// Empty pair → always accept.
	case !p.Finished:
		// Still playing (IsPlaybackStopped returns false).
		// PS2: slt priority comparison on playing animations — reject if
		// new priority is strictly lower than current. Equal or higher accepts.
		// This means attacks (0.4) can't be interrupted by locomotion (0.2),
		// but CAN be interrupted by death (1.0) or another attack (0.4).
		newPri := c.getAnimPriority(animID)
		if newPri < p.Priority {
			return
		}
	default:
		// Stopped/finished play-once (IsPlaybackStopped returns true).
		// PS2: priority gating applies.
		// PS2 additional rule: idle (animID 0) can never replace a stopped anim.
		if animID == 0 {
			return
		}
		newPri := c.getAnimPriority(animID)
		if newPri < p.Priority {
			return
		}
	}

	p.AnimID = animID
	p.Priority = c.getAnimPriority(animID)
	p.PlayOnce = c.getPlayOnce(animID)
	p.Finished = false
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
func (c *SpriteAnimController) MarkActionFinished() {
	c.pairs[PairAction].Finished = true
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
	// Advance combat idle timer (PS2: animTimerB, 5000ms threshold).
	// When InCombat is cleared, combatTimer keeps counting. After 5s it
	// expires and idle is allowed again (combat idle override in SetLocomotion
	// checks combatTimer < 5.0).
	if c.InCombat {
		c.combatTimer = 0
	} else {
		c.combatTimer += dt
	}
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

// getAnimPriority returns the pair 0 priority for a given VICSpriteAnimID.
// We use pair 0 priorities for both locomotion and action pairs since our
// dual-pair model doesn't have cross-pair competition (PS2 pair 1 is always
// 1.0, which would mean zero gating on actions).
func (c *SpriteAnimController) getAnimPriority(animID int) float32 {
	if c.isHumanoid {
		return humanoidPriority(animID)
	}
	return nonHumanoidPriority(animID)
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

// --- Humanoid animation priority table ---
// Indexed by ESF CPlayList slot number. Priority values from PS2 EE memory
// dump (cross-referenced across multiple humanoid VICSprites). NOTE: values
// vary slightly by character model — this table uses the most common values.
//   0.2 (lowest): locomotion — idle, walk, run, swim, fall, landing, strafe
//   0.4 (medium): combat attacks, spells, hit reactions
//   0.6 (high-medium): spell channel (slot 30)
//   1.0 (highest): death, special idle, battle stance, emotes
var humanoidSlotPriority = map[int]float32{
	// Locomotion — low priority (0.2)
	0: 0.2, 1: 0.2, 2: 0.2, 3: 0.2, 4: 0.2, 5: 0.2,
	6: 0.2, 7: 0.2, 10: 0.2, 11: 0.2, 12: 0.2, 13: 0.2,
	// Death, special idle — high priority (1.0)
	14: 1.0, // death
	15: 1.0, // special idle (plays to completion)
	17: 1.0, // battle stance (loops — PS2 pair0 priority is 1.0)
	// Combat actions — medium priority (0.4)
	18: 0.4, 19: 0.4, 20: 0.4, 21: 0.4, // 1HS/2HS attack variants
	22: 0.4, 23: 0.4, 24: 0.4, 25: 0.4, 26: 0.4, 27: 0.4,
	28: 0.4, 29: 0.4, 30: 0.6, 31: 0.4, 32: 0.4, // 30=spellChannel (0.6)
	33: 0.4, 34: 0.3, 35: 0.4, 36: 0.4, // 34=hitStun (0.3, below attacks so it can't interrupt swings)
	40: 0.4, 41: 0.4, 42: 0.4,
	// Spells — medium priority (0.4)
	37: 0.4, 38: 0.4, 39: 0.4,
	// Emotes — high priority (1.0)
	43: 1.0, 44: 1.0, 45: 1.0, 46: 0.4, 47: 0.4, 48: 0.4,
	51: 0.4, 52: 0.4, // crafting
	56: 1.0, 57: 1.0, 60: 1.0, 61: 1.0,
}

func humanoidPriority(animID int) float32 {
	if p, ok := humanoidSlotPriority[animID]; ok {
		return p
	}
	return 0.2 // default: lowest priority
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
// Our nhAnim* constants use F8 IDs (verified on Sandstorm). Priority values
// below are from the most common creature pattern (14 of 47 instances),
// mapped from CPlayList slots to F8 IDs by animation name.
//
// PS2 creature priorities vary by creature type (9 distinct patterns found).
// Battle stance and idle-battle are always 1.0; idle is always 0.0; other
// slots range from 0.2-1.0 depending on creature type.
//
// PS2 PlayOnce: Creature attacks/hit/spell are playOnce=0 (loop) on PS2 —
// the server controls when to stop. Our client uses time-gated play-once
// for these since entity attacks are controlled by AttackAnimEnd timers.

func nonHumanoidPriority(animID int) float32 {
	// animID values are CPlayList slots (not F8 indices).
	// P0 values from PS2 creature CPlayList (most common pattern, 14 of 47 instances).
	switch animID {
	case 0:
		return 0.0 // Idle (P0=0.0)
	case 1:
		return 0.2 // Walk (P0=0.2)
	case 2:
		return 0.4 // WalkBackward (P0=0.4)
	case 3:
		return 0.4 // Run (P0=0.4)
	case 4, 5:
		return 0.4 // Strafe/StrafeBack (P0=0.4)
	case 6:
		return 0.4 // Turn (P0=0.4)
	case 7:
		return 1.0 // JumpLand (P0=1.0)
	case 8, 9:
		return 0.2 // Attack1/2 (P0=0.2)
	case 10:
		return 0.1 // HitReaction (below attacks so it can't interrupt swings)
	case 11:
		return 0.4 // SpellCast (P0=0.4)
	case 12, 13:
		return 0.4 // Swim/SwimFast (P0=0.4)
	case 14:
		return 1.0 // Emote (P0=1.0)
	case 15:
		return 0.6 // Death (P0=0.6)
	case 16:
		return 1.0 // BattleStance (P0=1.0)
	case 17:
		return 0.2 // Special (P0=0.2)
	case 18:
		return 1.0 // IdleBattle (P0=1.0)
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
	// animID values are now CPlayList slots (not F8 indices).
	switch animID {
	case 7: // JumpLand — play once (PS2: playOnce=1)
		return true
	case 8, 9: // Attack1/2 — play once (PS2: loop, but our timers need it)
		return true
	case 10: // HitReaction — play once (PS2: loop, but our timers need it)
		return true
	case 11: // SpellCast — play once (PS2: loop, but our timers need it)
		return true
	case 14: // Emote — play once (PS2: playOnce=1)
		return true
	case 15: // Death — play once (PS2: playOnce=1)
		return true
	default:
		return false
	}
}

