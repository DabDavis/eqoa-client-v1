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

// PairState tracks the current state of one animation layer — VICSprite's
// m_cur[layer]: the slot (m_anim), whether it plays once (m_sync, out of the
// model's CPlayList), and whether that once-play has stopped
// (IsPlaybackStopped). An empty layer (AnimID -1) is m_play == -1.
type PairState struct {
	AnimID   int  // VICSpriteAnimID (-1 = none)
	Priority int  // m_entries[AnimID].m_priority[layer] (SetAnimPriorities)
	PlayOnce bool // m_sync == VIPlaybackTypeOnce: hold the last frame, then stopped
	Finished bool // the once-play reached its end (VIPlaybackTypeStopEnd)
	age      float32
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
	blendActive [2]bool // per-layer: m_prev[layer] holds a play whose blend is not complete

	// sync is the model's per-slot play-once flag (m_entries[i].m_sync), set
	// by BindModel once the model has loaded; nil uses defaultPlayOnce.
	sync       map[int]bool
	bound      bool
	boundModel int32

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

// IsHumanoid reports the skeleton type the controller was made for: two
// layers (skelType 1) or one.
func (c *SpriteAnimController) IsHumanoid() bool { return c.isHumanoid }

// BindModel hands the controller its loaded model's per-slot play-once flags
// (CharModel.PLSlotPlayOnce, after ApplyDefaultEntries) — m_entries[i].m_sync,
// which SetAnimation copies into m_cur[layer].m_sync. A slot missing from the
// map is a constructor entry: sync 0, loop.
func (c *SpriteAnimController) BindModel(modelID int32, sync map[int]bool) {
	c.bound, c.boundModel, c.sync = true, modelID, sync
}

// BoundTo reports whether BindModel was last called for modelID.
func (c *SpriteAnimController) BoundTo(modelID int32) bool {
	return c.bound && c.boundModel == modelID
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

// setAnimation is VICSprite::SetAnimation (0x00422D78), the one door every
// animation change goes through, for one layer. It reports whether the
// request was accepted, as SetAnimation's nonzero return does — which is
// what moves m_action.
//
// Not ported here: the special-idle deadline (m_animTime), which the model
// manager still applies at render time, and the scene-start guard
// (GetTime() > 5000) on the combat idle, which only matters in a session's
// first five seconds.
//
// The blend test reads blendActive, which the model manager reports once a
// frame, so two requests on one layer within the same frame do not see the
// blend the first of them started.
func (c *SpriteAnimController) setAnimation(id int, layer AnimPair) bool {
	// Idle within 5 s of the last attack becomes 17 (combat idle), for every
	// skeleton type.
	if id == 0 && c.combatTimer < 5.0 {
		id = 17
	}

	// A combat animation (22..53, not 34) while swimming becomes 44, the
	// swimming attack. A humanoid looks at its LOWER layer's animation on
	// either call, any other skeleton at layer 0's and only when setting it.
	if id >= 22 && id <= 53 && id != 34 {
		other := -1
		switch {
		case c.isHumanoid:
			other = c.pairs[LayerLower].AnimID
		case layer == LayerUpper:
			other = c.pairs[LayerUpper].AnimID
		}
		if other == 12 || other == 13 || other == 44 {
			id = 44
		}
	}

	// When dead, every request is death — slot 14 for every skeleton type.
	if c.IsDead {
		id = 14
	}

	cur := &c.pairs[layer]
	blending := cur.AnimID >= 0 && c.blendActive[layer]
	if cur.AnimID >= 0 {
		pri := animPriority(id, layer)
		curPri := animPriority(cur.AnimID, layer)
		stopped := cur.Finished
		playingOnce := cur.PlayOnce && !stopped

		// A blend still running into the current play (m_prev set, not
		// IsBlendComplete): an equal or higher request that is neither the
		// same animation nor idle freezes the current play and takes over;
		// anything else is ranked with the blend left running. Over the
		// special idle the request goes straight to the special-idle rule.
		special := false
		if blending {
			switch {
			case pri < curPri, id == cur.AnimID:
			case cur.AnimID == 15:
				special = true
			case id == 0:
			default:
				// FreezePlayback + ErasePlay(m_prev): the old blend is gone.
				cur.Finished = true
				stopped, playingOnce = true, false
				blending = false
				c.blendActive[layer] = false
			}
		}

		// check: a once-play still running keeps anything lower out.
		if !special && cur.AnimID == 15 {
			special = true
		}
		if !special && playingOnce && pri < curPri {
			return false
		}
		// special: idle does not cut a special idle short.
		if special && id == 0 && playingOnce {
			return false
		}
		// rank: a lower request loses to anything but a stopped once-play,
		// except the special idle over idle, and — on the lower layer — idle
		// or combat idle over anything below death.
		if pri < curPri && (!cur.PlayOnce || !stopped) &&
			(id != 15 || cur.AnimID != 0) &&
			(layer == LayerUpper || (id != 0 && id != 17) || cur.AnimID >= 14) {
			return false
		}
	}

	// m_idleTime = now for the attacks: 22..43 and 50..53, but not the
	// casts (37-39), the bow and crossbow (31-33) or 35.
	if isCombatTimerAnim(id) {
		c.combatTimer = 0
	}

	if id == cur.AnimID {
		return false // already playing: SetAnimation's `done`
	}

	// Accepted. With a blend still running (m_prev in use) SetAnimation
	// starts nothing — the request is taken but the layer keeps its play.
	c.TriggerResetPending = true
	if blending {
		return true
	}

	// When humanoid layer 0 takes the animation layer 1 already plays, it
	// starts in sync with it (AddInterpolatedPlayWithSync).
	if c.isHumanoid && layer == LayerUpper && c.pairs[LayerLower].AnimID == id {
		c.PairedBlendFromAction = true
	} else if layer == LayerUpper {
		c.PairedBlendFromAction = false
	}

	*cur = PairState{
		AnimID:   id,
		Priority: animPriority(id, layer),
		PlayOnce: c.slotPlayOnce(id),
	}
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

// LowerLayer returns what the lower layer plays: its slot, whether that is a
// once-play still running, and how long it has played (by Update's clock).
// MarkActionFinished only ever learns of layer 0's play ending, so a
// once-play the lower layer holds on its own — a landing or a kick the upper
// body has since left for another swing — needs its end found by its length.
func (c *SpriteAnimController) LowerLayer() (slot int, playingOnce bool, age float32) {
	p := &c.pairs[LayerLower]
	return p.AnimID, p.AnimID >= 0 && p.PlayOnce && !p.Finished, p.age
}

// MarkLowerFinished records that the lower layer's once-play has reached its
// end (VIPlaybackTypeStopEnd), so anything may replace it.
func (c *SpriteAnimController) MarkLowerFinished() {
	c.pairs[LayerLower].Finished = true
}

// Update advances frame-rate-dependent timers. Called once per frame from
// applySkeletal with the frame delta time.
func (c *SpriteAnimController) Update(dt float32) {
	c.pairs[LayerUpper].age += dt
	c.pairs[LayerLower].age += dt
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

// animPriority is m_entries[animID].m_priority[layer], as SetAnimPriorities
// (0x004259F8, vicsprite.cpp) writes it — the same table for every CSprite,
// creatures included (ParseCSpriteObj calls it whatever the skeleton; a
// non-humanoid only ever uses layer 0's). SetAnimation compares them with
// slt. A slot SetAnimPriorities does not list keeps the constructor's 0:
// SetDefaultEntries runs BEFORE it, so even a copied entry's priority is the
// table's, not its source's.
func animPriority(animID int, layer AnimPair) int {
	if animID < 0 || animID >= len(animPriorities[0]) {
		return 0
	}
	return int(animPriorities[layer][animID])
}

// animPriorities[layer][slot], transcribed from SetAnimPriorities.
var animPriorities = [2][66]int8{
	{ // layer 0, the upper body
		0: 2, 1: 2, 2: 2, 3: 2, 4: 2, 5: 2, 6: 2,
		7: 3, 8: 3, 9: 3, 10: 2, 11: 2, 12: 2, 13: 2,
		14: 7, 15: 0, 16: 1, 17: 2, 18: 6,
		22: 5, 23: 5, 24: 5, 25: 5, 26: 5, 27: 5, 28: 5, 29: 5,
		30: 5, 31: 5, 32: 5, 33: 5, 34: 4, 35: 5, 36: 5, 37: 5,
		38: 5, 39: 5, 40: 5, 41: 5, 42: 5, 43: 5, 44: 5, 45: 5, 46: 5,
		50: 5, 51: 5, 52: 5, 53: 5,
		56: 4, 57: 4, 59: 4, 60: 4, 61: 4, 63: 4, 64: 4, 65: 4,
	},
	{ // layer 1, the lower body
		0: 2, 1: 6, 2: 6, 3: 6, 4: 6, 5: 6, 6: 6,
		7: 7, 8: 3, 9: 3, 10: 6, 11: 6, 12: 6, 13: 6,
		14: 8, 15: 0, 16: 1, 17: 1, 18: 7,
		22: 5, 23: 5, 24: 5, 25: 5, 26: 5, 27: 5, 28: 5, 29: 5,
		30: 5, 31: 5, 32: 5, 33: 5, 34: 4, 35: 5, 36: 5, 37: 5,
		38: 5, 39: 5, 40: 5, 41: 5, 42: 5, 43: 7, 44: 7, 45: 5, 46: 5,
		50: 5, 51: 5, 52: 6, 53: 6,
		56: 4, 57: 4, 59: 4, 60: 4, 61: 4, 63: 4, 64: 4, 65: 4,
	},
}

// slotPlayOnce is m_entries[animID].m_sync == VIPlaybackTypeOnce: the
// model's own CPlayList flag once SetSlotSync has handed it over, otherwise
// defaultPlayOnce.
func (c *SpriteAnimController) slotPlayOnce(animID int) bool {
	if c.sync != nil {
		return c.sync[animID]
	}
	return defaultPlayOnce(animID)
}

// defaultPlayOnce is what CHAR.ESF's play lists say, for a controller whose
// model has not loaded. Across all 568 CSprites, every skeleton type alike,
// the locomotion slots loop — 0-6, 10-13 and 17 — and everything else they
// carry plays once: landing (7), death (14), the special idle (15), every
// attack, hit reaction, cast and emote (22-65). The only exceptions are 25
// models whose idle (0) plays once. (An old note had creature attacks
// looping; no creature in the file does.)
func defaultPlayOnce(animID int) bool {
	switch animID {
	case 0, 1, 2, 3, 4, 5, 6, 10, 11, 12, 13, 17:
		return false
	}
	return true
}
