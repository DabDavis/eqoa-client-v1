package game

// Port of VISpellEffectSystem / VISpellEffectDriver / VISpellEventDriver
// from PS2 SUPPORT module. See claude-notes/spell-effect-system.md for
// decompiled struct layouts and process logic.

import (
	"log"
	"math"
	"math/rand"
	"time"

	"github.com/eqoa/client/internal/mathutil"
	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

// randFloat returns a random float32 in [0, 1).
func randFloat() float32 {
	return rand.Float32()
}

// AudioPlayer is the interface for spell sound playback.
// Implemented by audio.System in the visual client.
type AudioPlayer interface {
	Play(dictID int32, id int32, pos [3]float32, volume, maxDist float32)
	Stop(id int32)
	HasSound(dictID int32) bool
	LoadSpellSounds(file *esf.ObjFile) int
}

// PS2 VISpellEffectSystem::Process has a 60-second global timeout per effect
// ($s3 = 0xEA60 = 60000ms). If elapsed > 60000, calls Stop().
// Fields[3]=0 means infinite lifetime for emitters, sprites, and sounds
// (destroyed explicitly by Destroy* events).

// --- Active Event (tracked running effect) ---

// ActiveEvent represents a running effect instance (emitter, sprite, sound, etc.)
// Port of VISpellActiveEvent (0xB8 bytes on PS2).
type ActiveEvent struct {
	eventType uint32          // from SpellEvent.EventType
	localID   int32           // Fields[0] (m_p04): what Destroy*/SetEmitter* find it by
	id        int32           // the emitter's global ID, the sound's audio ID, else Fields[0]
	event     *esf.SpellEvent // reference to source event data
	startTime time.Time       // when this event was created
	maxLife   time.Duration   // auto-expire after this duration
	// Fade parameters (type 14)
	fadeStart float32
	fadeEnd   float32
	fadeDurMS int32
	// Attractor (type 5 — SetEmitterAttractor): m_p84 and m_f88. The selector
	// is NOT an actor: bit 16 = caster, bit 17 = target, else the list's
	// actor, and the low 16 bits a node (0xFFFF = the actor itself). It is -1
	// (no attractor) until an event sets it.
	attractorSet      bool
	attractorSel      int32
	attractorStrength float32
	// Motif (type 6 — SetEmitterMotif): the blender it drives and the factor
	// the ramp starts from (m_f9C).
	blender    *MotifBlender
	blendStart float32
	// Emit shape parameters (type 3 — CreateEmitter, shapes 1-3)
	// PS2 ProcessCreateEmitter: emitShape=(Fields[2]>>20)&3
	// Shapes 1-3 use rotating radius from Floats[0-4]:
	//   interpRadius = Floats[0] + (Floats[1]-Floats[0]) * (sin(radParam)*0.5+0.5)
	//   angle advances by Floats[3]*2*dt, radius param by Floats[2]*dt
	emitShape    int32   // 0=point, 1=line, 2=ring, 3=sphere
	emitAngle    float32 // current rotation angle (advances per frame)
	emitRadParam float32 // current radius interpolation parameter
	// Bone attachment (type 3 — CreateEmitter)
	// PS2: nodeID = Fields[2] & 0xFFFF, 0xFFFF = no specific bone
	nodeID uint16 // bone/node index for position attachment
	// Sprite (type 9 — CreateSprite)
	spriteDictID int32   // Fields[1] — sprite DictID in SPELLFX.ESF
	spriteActorID uint32 // actor this sprite is attached to
	spriteOpacity float32 // current opacity (1.0 = fully visible, faded by FadeSprite)
}

// expired returns true if the active event has exceeded its max lifetime.
func (ae *ActiveEvent) expired(now time.Time) bool {
	if ae.maxLife <= 0 {
		return false
	}
	return now.Sub(ae.startTime) > ae.maxLife
}

// --- Spell Event Driver (processes one event list) ---

// SpellEventDriver processes a single event list (Caster/Target/Area) sequentially.
// Port of VISpellEventDriver from SUPPORT module (struct ≥0x60 bytes).
//
// Process flow (7 phases per tick):
//  1. Update projectile (blocking if active)
//  2. Update lightning
//  3. Update wait timer
//  4. Cleanup inactive events
//  5. Update active events (emitters, sprites, sounds, fades)
//  6. Dispatch next events until Wait encountered
//  7. Check completion
type SpellEventDriver struct {
	effDriver *SpellEffectDriver
	events    []esf.SpellEvent // event list data
	listIndex int              // 0=Caster, 1=Target, 2=Area
	actorID   uint32           // actor this list targets

	eventIndex int  // cursor into events array
	finished   bool // all events processed and no active events

	// Wait state (phase 3)
	waitEvent    *ActiveEvent // current wait (nil = no wait active)
	waitSubType  int32        // Fields[0] of wait event
	waitDuration int32        // ms: Fields[1] for sub-type 1, Fields[2] for sub-type 3

	// Projectile state (phase 1) — the PS2's m_projectile. PlayTargetProjectile
	// hands it to the TARGET (or area) driver it creates, so that driver's own
	// events — the impact — wait for the arrival while the caster's carry on.
	// It flies at a constant Floats[0] units/s toward the target's node 4 and
	// arrives when it stops closing in within 3 units (ProcessProjectileEvent).
	projectileActive   bool
	projectileStart    time.Time
	projectileEvent    *esf.SpellEvent
	projectileSource   uint32        // the caster actor it left from
	projectileMode     int32         // (Fields[2] >> 8) & 0xF: 0 straight, 1 orbit, 2 jitter, 3 lob
	projectilePos      [3]float32    // m_v14: where it is
	projectileStartPos [3]float32    // m_v20: where it started
	projectileA        [2][3]float32 // m_a: the two ends' offsets (two-ended projectiles)
	projectileB        [2][3]float32 // m_b: jitter endpoints, previous
	projectileC        [2][3]float32 // m_c: jitter endpoints, next
	projectileAngle    float32       // m_f9C: orbit angle (mode 1) / jitter phase (mode 2)
	projectileRadius   float32       // m_fA0: orbit radius (mode 1, one end)
	projectileEmitters [2]*ParticleEmitter
	projectileEmitIDs  [2]int32
	projectileDrawPos  [3]float32 // this frame's lead point, for rendering
	projectileLastTick time.Time

	// PS2 actionNodeID at +0x34. Return value from SetAttackAction/SetAttackAltAction.
	// Used by Wait(sub-type 0) to check IsActionComplete for the specific action.
	// -1 = no action set (Wait type 0 clears immediately when -1).
	actionNodeID int // PS2 VISpellEventDriver+0x34

	// PS2 VISpellEventDriver animNodes[4] at +0x38..+0x44.
	// Set by NewPlayAttack via FindTrigger("Spell FX 1-4").
	// Checked by Wait(sub-type 3) via HasTripped.
	animNodes [4]int // trigger indices (-1 = not set)

	// Lightning state (phase 2) — like the projectile, the PS2 hands it to
	// the target driver; the bolt runs from lightningSource to this driver's actor.
	lightningSource  uint32
	lightningActive  bool
	lightningStart   time.Time
	lightningDurMS   int32        // duration from Fields[2] (PS2: event->Fields[2])
	lightningColor   [3]float32   // RGB from Floats[0-2] (PS2: event->Floats[0-2])
	lightningHeld      bool         // within Fields[2] ms: the bolt is held at full charge
	lightningMaxBranch int32        // max branches from Fields[3] (clamped 0-3)
	lightningBoneMode  int32        // source bone mode from Fields[1] low byte (0-3)

	// Active effects (phase 5)
	activeEvents []*ActiveEvent

	// Inactive events pending cleanup (phase 4)
	inactiveEvents []*ActiveEvent

	// Frame timing for per-frame updates (emit shape advancement etc.)
	lastProcessTime time.Time

	// PS2 dispatches one event per frame at ~30fps. Throttle dispatch to match.
	lastDispatchTime time.Time
}

// findActive is FindCreateEmitter / FindCreateSound / FindCreateSprite: the
// first active event of that type whose Fields[0] is id. Each type has its own
// ids, and they are this list's own: an emitter and a sound may both be 1.
// (A local→global map shared by all types used to send DestroySound and
// DestroySprite looking for an emitter's ID whenever the numbers coincided —
// both such events in SPELLFX do — so the sound or sprite was never removed.)
func (d *SpellEventDriver) findActive(eventType uint32, id int32) *ActiveEvent {
	for _, ae := range d.activeEvents {
		if ae.eventType == eventType && ae.localID == id {
			return ae
		}
	}
	return nil
}

func newSpellEventDriver(eff *SpellEffectDriver, events []esf.SpellEvent, actorID uint32, listIndex int) *SpellEventDriver {
	return &SpellEventDriver{
		effDriver:    eff,
		events:       events,
		listIndex:    listIndex,
		actionNodeID: -1,
		animNodes:    [4]int{-1, -1, -1, -1},
		actorID:   actorID,
	}
}

// resolveActorPos resolves an actor's position, optionally at a specific bone.
// PS2 GetNodeTransform at 0x0047A490: if sprite is CSprite (type 5), uses
// CalcSlotWorldTransform for bone position; otherwise uses GetActorTransform.
// nodeID 0xFFFF means no specific bone — returns actor root (feet level).
// Height offsets come from the spell event's Floats[0-2], not added here.
func (d *SpellEventDriver) resolveActorPos(actorID uint32, nodeID uint16) (float32, float32, float32) {
	// Try bone resolver first (if available and nodeID specified)
	if nodeID != 0xFFFF && d.effDriver.resolveBone != nil {
		if bx, by, bz, ok := d.effDriver.resolveBone(actorID, nodeID); ok {
			return bx, by, bz
		}
	}
	// PS2 GetActorTransform returns actor root position (feet level).
	// No blanket height offset — spell event Floats[1] provides Y offset when needed.
	if d.effDriver.resolvePos != nil {
		x, y, z := d.effDriver.resolvePos(actorID)
		return x, y, z
	}
	return 0, 0, 0
}

// Process runs the 7-phase event state machine. Returns true when finished.
func (d *SpellEventDriver) Process() bool {
	now := time.Now()

	// Phase 1: Projectile (blocking)
	if d.projectileActive {
		d.processProjectile(now)
		if d.projectileActive {
			return false // still flying
		}
	}

	// Phase 2: Lightning (non-blocking)
	if d.lightningActive {
		d.processLightning(now)
	}

	// Phase 3: Wait timer
	if d.waitEvent != nil {
		d.processWait(now)
	}

	// Phase 4: Cleanup inactive events
	d.processInactive()

	// Phase 5: Update active events (auto-expire stale ones)
	// Compute actual frame dt for per-frame updates (emit shape advancement).
	frameDT := float32(0.033)
	if !d.lastProcessTime.IsZero() {
		frameDT = float32(now.Sub(d.lastProcessTime).Seconds())
		if frameDT > 0.2 {
			frameDT = 0.2
		}
		if frameDT <= 0 {
			frameDT = 0.033
		}
	}
	d.lastProcessTime = now
	d.processActive(now, frameDT)

	// Phase 6: Dispatch exactly ONE event per Process() call.
	// PS2 MIPS at 0x0047A700-0x0047A868 has NO backward branch — dispatches one
	// event, increments eventIndex, falls through to Phase 7. Consecutive
	// non-Wait events are staggered by one frame (~33ms) each.
	// PS2: lightning is non-blocking for dispatch (only blocks completion in Phase 7).
	// Projectile blocks via early return in Phase 1, so !d.projectileActive is redundant but safe.
	if d.waitEvent == nil && !d.projectileActive && d.eventIndex < len(d.events) {
		d.dispatchEvent(now)
	}

	// Phase 7: Check completion — PS2 checks ALL of these:
	//   eventIndex >= count AND activeEvents empty AND inactiveEvents empty
	//   AND waitEvent == NULL AND lightningEvent == NULL
	if d.eventIndex >= len(d.events) &&
		len(d.activeEvents) == 0 &&
		len(d.inactiveEvents) == 0 &&
		d.waitEvent == nil &&
		!d.projectileActive &&
		!d.lightningActive {
		d.finished = true
		return true
	}
	return false
}

// processWait handles the current wait event.
// PS2 ProcessWaitEvent at 0x0047A278 dispatches on Fields[0] (5 sub-types).
// All timing uses VIScene::GetTime() (int64 ms) compared via integer slt.
func (d *SpellEventDriver) processWait(now time.Time) {
	if d.waitEvent == nil {
		return
	}

	elapsedMS := now.Sub(d.waitEvent.startTime).Milliseconds()

	switch d.waitSubType {
	case 0:
		// Wait for actor action completion.
		// PS2: if actionNodeID == -1, clear immediately (no action to wait for).
		// Otherwise: GetActorSpritePtr → check state==5 → IsActionComplete(sprite, actionNodeID).
		sys := d.effDriver.system
		if d.actorID == 0xFFFFFFFF || d.actionNodeID == -1 || sys == nil || sys.IsActionComplete == nil ||
			!d.isCSprite() || sys.IsActionComplete(d.actorID, d.actionNodeID) {
			d.waitEvent = nil
		}

	case 1:
		// Wait for elapsed time: Fields[1] = duration in ms.
		// PS2: slt(duration, elapsed) — true when duration < elapsed.
		if int64(d.waitDuration) < elapsedMS {
			d.waitEvent = nil
		}

	case 2:
		// Wait for trigger duration: effDriver->triggerDuration.
		triggerElapsedMS := now.Sub(d.effDriver.triggerTime).Milliseconds()
		if int64(d.effDriver.triggerDurMS) < triggerElapsedMS {
			d.waitEvent = nil
		}

	case 3:
		// Wait for actor condition with Fields[2] as timeout.
		// PS2 at 0x0047A370: checks sprite->state==5 AND HasTripped(sprite, animNodes[Fields[1]])
		// If sprite is not a CSprite (state!=5) or actorID==-1, wait clears immediately.
		// If CSprite: HasTripped OR Fields[2] timeout.
		sys := d.effDriver.system

		// PS2 fast-clear: non-CSprite actors skip HasTripped entirely.
		isCSprite := true
		if sys != nil && sys.IsCSprite != nil {
			isCSprite = sys.IsCSprite(d.actorID)
		}
		if !isCSprite {
			d.waitEvent = nil
			break
		}

		cleared := false
		if sys != nil && sys.HasTripped != nil {
			nodeIdx := int(d.waitEvent.event.Fields[1])
			if nodeIdx >= 0 && nodeIdx < 4 {
				triggerIdx := d.animNodes[nodeIdx]
				if triggerIdx >= 0 {
					cleared = sys.HasTripped(d.actorID, triggerIdx)
				}
			}
		}
		if cleared || int64(d.waitDuration) < elapsedMS {
			d.waitEvent = nil
		}

	case 4:
		// Wait for trigger flags as duration: effDriver->triggerFlags.
		// PS2 at 0x0047A444: lw $v1, 0x44($a0) = triggerFlags.
		triggerElapsedMS := now.Sub(d.effDriver.triggerTime).Milliseconds()
		if int64(d.effDriver.triggerFlags) < triggerElapsedMS {
			d.waitEvent = nil
		}

	default:
		// Invalid sub-type — clean up
		d.waitEvent = nil
	}
}

// processProjectile is VISpellEventDriver::ProcessProjectileEvent.
func (d *SpellEventDriver) processProjectile(now time.Time) {
	ev := d.projectileEvent
	// The target is gone (NotifyActorWasDestroyed set it to -1): done.
	if ev == nil || d.actorID == 0xFFFFFFFF || d.effDriver.resolvePos == nil {
		d.completeProjectile()
		return
	}
	// Safety net only; the PS2 has none, but a target that vanishes from the
	// resolver would otherwise hold the driver forever.
	if now.Sub(d.projectileStart).Seconds() > 30.0 {
		d.completeProjectile()
		return
	}
	dt := d.frameDelta(now, d.projectileLastTick)
	d.projectileLastTick = now

	tx, ty, tz := d.resolveActorPos(d.actorID, 4) // the target's node 4
	tgt := [3]float32{tx, ty, tz}
	pos := d.projectilePos
	total := vlen(vsub(tgt, d.projectileStartPos))
	dist := vlen(vsub(pos, tgt))

	// The flight frame: forward toward the target, right and up from it.
	dv := vsub(tgt, pos)
	fwd := dv
	right := vcross([3]float32{0, 1, 0}, fwd)
	up := vcross(fwd, right)
	right, up, fwd = vnorm(right), vnorm(up), vnorm(fwd)
	frame := func(q [3]float32) [3]float32 { // VIMatrix44::Mul, row vectors
		return [3]float32{
			q[0]*right[0] + q[1]*up[0] + q[2]*fwd[0] + pos[0],
			q[0]*right[1] + q[1]*up[1] + q[2]*fwd[1] + pos[1],
			q[0]*right[2] + q[1]*up[2] + q[2]*fwd[2] + pos[2],
		}
	}
	scale := min(dist*0.6, 1) // eases the ends in from the source; not the speed
	speed := ev.Floats[0]
	twoEnds := d.projectileEmitters[1] != nil
	var p0, p1 [3]float32
	switch d.projectileMode {
	case 0:
		if twoEnds {
			p0 = frame(vscale(d.projectileA[0], scale))
			p1 = frame(vscale(d.projectileA[1], scale))
		} else {
			p0 = pos
		}
	case 1:
		roll := func(q [3]float32) [3]float32 { // RotateHPR(0, 0, angle)
			c, sn := float32(math.Cos(float64(d.projectileAngle))), float32(math.Sin(float64(d.projectileAngle)))
			return [3]float32{q[0]*c - q[1]*sn, q[0]*sn + q[1]*c, q[2]}
		}
		angle := d.projectileAngle
		if twoEnds {
			p0 = frame(roll(vscale(d.projectileA[0], scale)))
			p1 = frame(roll(vscale(d.projectileA[1], scale)))
		} else {
			d.projectileRadius = min(d.projectileRadius+dt*3, ev.Floats[1])
			p0 = frame(roll([3]float32{d.projectileRadius * scale, 0, 0}))
		}
		d.projectileAngle = angle + ev.Floats[2]*5*dt
	case 2:
		jitter := func(base [3]float32) [3]float32 {
			return [3]float32{
				(base[0] + virand()*0.2) * ev.Floats[1],
				(base[1] + virand()*0.2) * ev.Floats[1],
				(base[2] + virand()*0.05) * ev.Floats[1],
			}
		}
		ends := 1
		if twoEnds {
			ends = 2
		}
		if d.projectileAngle >= 1 {
			for i := 0; i < ends; i++ {
				d.projectileB[i] = d.projectileC[i]
				base := [3]float32{}
				if twoEnds {
					base = d.projectileA[i]
				}
				d.projectileC[i] = jitter(base)
			}
			d.projectileAngle = 0
		}
		lerp := func(i int) [3]float32 {
			b, c := d.projectileB[i], d.projectileC[i]
			t := d.projectileAngle
			return vscale([3]float32{b[0] + (c[0]-b[0])*t, b[1] + (c[1]-b[1])*t, b[2] + (c[2]-b[2])*t}, scale)
		}
		p0 = frame(lerp(0))
		if twoEnds {
			p1 = frame(lerp(1))
		}
		d.projectileAngle += dt * 15
	case 3:
		if twoEnds {
			p0 = frame(vscale(d.projectileA[0], scale))
			p1 = frame(vscale(d.projectileA[1], scale))
		} else {
			p0 = pos
		}
		if total > 0 && speed != 0 {
			h := float32(math.Sin(float64(vlen(vsub(pos, d.projectileStartPos))/total*math.Pi))) /
				(speed * speed) * total * total
			p0[1] += h
			p1[1] += h
		}
	}

	facing := float32(math.Atan2(float64(dv[0]), float64(dv[2])))
	for i, p := range [2][3]float32{p0, p1} {
		if em := d.projectileEmitters[i]; em != nil {
			em.SetPosition(p[0], p[1], p[2])
			em.SetFacing(facing)
		}
	}
	d.projectileDrawPos = p0

	step := vscale(vnorm(dv), speed*dt)
	d.projectilePos = [3]float32{pos[0] + step[0], pos[1] + step[1], pos[2] + step[2]}
	dist1 := vlen(vsub(d.projectilePos, tgt))
	if dist <= dist1 && dist1 < 3 {
		d.completeProjectile()
	}
}

// frameDelta is the step a spell motion advances by: the system's
// VIParticleSystem::DeltaTime, or — with no system, as in a test — the clock
// difference under the same 0.1 s cap.
func (d *SpellEventDriver) frameDelta(now, last time.Time) float32 {
	if sys := d.effDriver.system; sys != nil && sys.deltaTime > 0 {
		return sys.deltaTime
	}
	return min(max(float32(now.Sub(last).Seconds()), 0), 0.1)
}

// completeProjectile retires the projectile: its emitters stop emitting and
// drain, as the PS2's move to the inactive list does.
func (d *SpellEventDriver) completeProjectile() {
	d.projectileActive = false
	for i, em := range d.projectileEmitters {
		if em != nil && d.effDriver.particles != nil {
			d.effDriver.particles.DrainEmitter(d.projectileEmitIDs[i])
		}
	}
	d.projectileEmitters = [2]*ParticleEmitter{}
	d.projectileEvent = nil
}

func vsub(a, b [3]float32) [3]float32 { return [3]float32{a[0] - b[0], a[1] - b[1], a[2] - b[2]} }
func vlen(a [3]float32) float32       { return float32(math.Sqrt(float64(a[0]*a[0] + a[1]*a[1] + a[2]*a[2]))) }
func vscale(a [3]float32, k float32) [3]float32 {
	return [3]float32{a[0] * k, a[1] * k, a[2] * k}
}
func vcross(a, b [3]float32) [3]float32 {
	return [3]float32{a[1]*b[2] - a[2]*b[1], a[2]*b[0] - a[0]*b[2], a[0]*b[1] - a[1]*b[0]}
}
func vnorm(a [3]float32) [3]float32 {
	if l := vlen(a); l > 0 {
		return vscale(a, 1/l)
	}
	return a
}

// virand is the PS2's VIRAND, (rand() - rand()) / 2^31: -1..1.
func virand() float32 { return rand.Float32() - rand.Float32() }

// processLightning updates active lightning bolt.
// PS2 ProcessLightningEvent at 0x004789A8: duration from Fields[2] ms.
// When timer expires, PS2 sets charge=1.0 (final flash) then waits for
// charge to decay to 0 via VILightning::Update before removing.
// Charge starts at 1.0 and decays at ~1.5/sec, so the bolt persists
// ~0.67s after the flash before fully fading out.
// VISpellEventDriver::ProcessLightningEvent: while the event is no older than
// Fields[2] ms the bolt is put back to full charge every frame (SetCharge(1));
// after that VILightning::Update lets it decay at 5/s, and the event ends once
// it is past the duration and the charge is spent — 200 ms at most.
func (d *SpellEventDriver) processLightning(now time.Time) {
	elapsedMS := now.Sub(d.lightningStart).Milliseconds()
	d.lightningHeld = elapsedMS <= int64(d.lightningDurMS)
	if d.lightningHeld {
		return
	}
	// A source or target that is gone draws nothing, so nothing is left to decay.
	if d.actorID == 0xFFFFFFFF || d.lightningSource == 0xFFFFFFFF ||
		elapsedMS > int64(d.lightningDurMS)+200 {
		d.lightningActive = false
	}
}

// processInactive cleans up finished events.
// PS2 ProcessInactiveEvents at 0x0047A538: iterates inactive events.
// For type 3 (emitter) with active resource, calls ProcessCreateEmitter(ae, isActive=false)
// to let particles drain naturally. Removes event when resource is done.
// For other types, removes immediately.
func (d *SpellEventDriver) processInactive() {
	for i := len(d.inactiveEvents) - 1; i >= 0; i-- {
		ae := d.inactiveEvents[i]
		if ae.eventType == 3 {
			// PS2: keep emitter event alive while particles are draining.
			// The emitter has been set to draining mode (no new births).
			// Check if the emitter still has live particles.
			if d.effDriver.particles != nil {
				if d.effDriver.particles.EmitterHasParticles(ae.id) {
					// ProcessCreateEmitter(e, false): no placing, no births,
					// but the attractor still pulls.
					for _, em := range d.effDriver.particles.emitters {
						if em.emitterID == ae.id {
							d.applyAttractor(ae, em)
						}
					}
					continue // still draining — keep in inactive list
				}
			}
		}
		// Remove finished inactive event
		d.inactiveEvents = append(d.inactiveEvents[:i], d.inactiveEvents[i+1:]...)
	}
}

// processActive updates running effects and auto-expires stale ones.
// PS2 uses jump table at 0x004EBA40 for types 3,6,9,11,14.
// On PS2, particle emitters naturally expire via VIParticleDefinition::Lifespan.
// Since we don't have that, active events use a maxLife timeout.
func (d *SpellEventDriver) processActive(now time.Time, dt float32) {
	for i := len(d.activeEvents) - 1; i >= 0; i-- {
		ae := d.activeEvents[i]

		// Auto-expire any active event past its lifetime.
		// PS2: expired emitters (type 3) move to inactiveEvents for particle drain.
		// Other types are removed immediately.
		if ae.expired(now) {
			if ae.eventType == 3 {
				// Emitter: move to inactive for particle drain
				if d.effDriver.particles != nil {
					d.effDriver.particles.DrainEmitter(ae.id)
				}
				d.inactiveEvents = append(d.inactiveEvents, ae)
			}
			if ae.eventType == 11 {
				d.stopSound(ae) // deleting the event releases its sound actor
			}
			d.activeEvents = append(d.activeEvents[:i], d.activeEvents[i+1:]...)
			continue
		}

		switch ae.eventType {
		case 3: // ProcessCreateEmitter: follow the actor's node, then turn the shape
			if d.effDriver.particles != nil {
				d.placeEmitter(ae)
				if ae.emitShape >= 1 && ae.emitShape <= 3 {
					// The angles step on the particle system's DeltaTime after
					// the offset is taken: Floats[3]·2 turns, Floats[2] the radius.
					step := dt
					if sys := d.effDriver.system; sys != nil && sys.deltaTime > 0 {
						step = sys.deltaTime
					}
					ev := ae.event
					ae.emitAngle = wrap2Pi(ae.emitAngle + ev.Floats[3]*2*step)
					ae.emitRadParam = wrap2Pi(ae.emitRadParam + ev.Floats[2]*step)
				}
			}
			// PS2: Fields[3] as auto-expire timer (handled by ae.maxLife + expired() check above)
		case 6: // ProcessSetEmitterMotif: ramp to Floats[0] over Fields[2] ms, then hold
			ev := ae.event
			elapsed := now.Sub(ae.startTime).Milliseconds()
			if int64(ev.Fields[2]) < elapsed || ev.Fields[2] == 0 {
				if ae.blender.Factor() != ev.Floats[0] {
					ae.blender.SetFactor(ev.Floats[0])
				}
			} else {
				f := float32(elapsed) / float32(ev.Fields[2])
				f = ae.blendStart + (ev.Floats[0]-ae.blendStart)*f
				ae.blender.SetFactor(min(max(0, f), 1))
			}
			// It retires only when its emitter is gone and the motif unlinked.
			if !ae.blender.Linked() {
				d.activeEvents = append(d.activeEvents[:i], d.activeEvents[i+1:]...)
			}
		case 8: // PlayTargetProjectile — never an active event: the projectile
			// lives on the target driver and processProjectile moves its emitters.
		case 9: // CreateSprite — track per-frame (auto-expires via maxLife)
			// Sprite stays attached to actor; position resolved at render time.
		case 11: // CreateSound — PS2 updates position/volume per-frame
			// PS2: tracks sound to actor position via GetNodeTransform each frame.
			// Keep the active event alive until maxLife expires so it properly
			// blocks completion checks. Audio position updated by audio.System.Update().
		case 14: // FadeSprite — interpolate opacity on matching sprite, remove when done
			elapsed := now.Sub(ae.startTime).Milliseconds()
			if ae.fadeDurMS > 0 {
				t := float32(elapsed) / float32(ae.fadeDurMS)
				if t > 1.0 {
					t = 1.0
				}
				// Update opacity on the matching CreateSprite active event
				// PS2 clamps to [0.0, 1.0]
				targetOpacity := ae.fadeStart + (ae.fadeEnd-ae.fadeStart)*t
				if targetOpacity < 0 {
					targetOpacity = 0
				}
				if targetOpacity > 1 {
					targetOpacity = 1
				}
				for _, other := range d.activeEvents {
					if other.eventType == 9 && other.id == ae.id {
						other.spriteOpacity = targetOpacity
					}
				}
				if elapsed >= int64(ae.fadeDurMS) {
					d.activeEvents = append(d.activeEvents[:i], d.activeEvents[i+1:]...)
				}
			}
		}
	}
}

// dispatchEvent dispatches exactly ONE event per Process() call.
// PS2 Process() at 0x0047A5F0: reads events[eventIndex], dispatches via switch,
// increments eventIndex, then falls through to completion check — NO loop.
// This means consecutive non-Wait events are staggered by one frame (~33ms) each.
func (d *SpellEventDriver) dispatchEvent(now time.Time) {
	if d.eventIndex >= len(d.events) {
		return
	}

	ev := &d.events[d.eventIndex]
	d.eventIndex++

	// Debug: log every dispatched event
	evNames := [18]string{"?", "Wait", "SetAction", "CreateEmitter", "DestroyEmitter",
		"SetAttractor", "SetMotif", "PlayTargetInstant", "PlayTargetProjectile",
		"CreateSprite", "DestroySprite", "CreateSound", "DestroySound",
		"PlayTargetLightning", "FadeSprite", "PlayAttack", "VibrateController", "ShakeCamera"}
	evName := "Unknown"
	if ev.EventType < uint32(len(evNames)) {
		evName = evNames[ev.EventType]
	}
	log.Printf("[SPELL] list=%d ev[%d] %s Fields=[%d,0x%08X,0x%08X,%d] Floats=[%.2f,%.2f,%.2f,%.2f,%.2f]",
		d.listIndex, d.eventIndex-1, evName,
		ev.Fields[0], uint32(ev.Fields[1]), uint32(ev.Fields[2]), ev.Fields[3],
		ev.Floats[0], ev.Floats[1], ev.Floats[2], ev.Floats[3], ev.Floats[4])

	switch ev.EventType {
	case 1: // Wait
		d.newWait(ev, now)
	case 2: // SetAction — trigger action/cast animation on actor
		d.newSetAction(ev)
	case 3: // CreateEmitter
		d.newCreateEmitter(ev, now)
	case 4: // DestroyEmitter
		d.newDestroyByType(ev, 3)
	case 5: // SetEmitterAttractor
		d.newSetEmitterAttractor(ev)
	case 6: // SetEmitterMotif
		d.newSetEmitterMotif(ev, now)
	case 7: // PlayTargetInstant
		d.newPlayTargetInstant(ev)
	case 8: // PlayTargetProjectile
		d.newPlayTargetProjectile(ev, now)
	case 9: // CreateSprite
		d.newCreateSprite(ev, now)
	case 10: // DestroySprite
		d.newDestroyByType(ev, 9)
	case 11: // CreateSound
		d.newCreateSound(ev, now)
	case 12: // DestroySound
		d.newDestroyByType(ev, 11)
	case 13: // PlayTargetLightning
		d.newPlayTargetLightning(ev, now)
	case 14: // FadeSprite
		d.newFadeSprite(ev, now)
	case 15: // PlayAttack — one-shot animation trigger on actor
		d.newPlayAttack(ev)
	case 16: // VibrateController
		// No-op
	case 17: // ShakeCamera
		d.newShakeCamera(ev)
	}
}

// --- Event creators (New* functions) ---

func (d *SpellEventDriver) newWait(ev *esf.SpellEvent, now time.Time) {
	subType := ev.Fields[0]

	// PS2 ProcessWaitEvent reads different fields per sub-type:
	//   case 1: duration = Fields[1] (event+0x08) — elapsed time in ms
	//   case 3: timeout  = Fields[2] (event+0x0C) — actor condition timeout in ms
	//   cases 0,2,4: duration comes from actor state or effDriver, not event fields
	var durMS int32
	switch subType {
	case 1:
		durMS = ev.Fields[1]
	case 3:
		durMS = ev.Fields[2]
	default:
		durMS = ev.Fields[2] // fallback (sub-types 0,2,4 don't use this)
	}

	// PS2 NewWait (0x004761A0) is only 14 instructions: allocate, init, store, return.
	// PS2 ALWAYS creates the wait event — even for sub-type 0. This gives a one-frame
	// delay before subsequent events dispatch (ProcessWaitEvent handles it next tick).
	// Sub-type 0: wait for actor action completion (actor->state == 5). Since we lack
	// actor state, ProcessWaitEvent will clear it immediately on the next Process() call,
	// but the one-frame gap is preserved.

	ae := &ActiveEvent{
		eventType: ev.EventType,
		event:     ev,
		startTime: now,
	}
	d.waitEvent = ae
	d.waitSubType = subType
	d.waitDuration = durMS
}

// newCreateEmitter creates a particle emitter.
// PS2 NewCreateEmitter at 0x00476760: Creates VIParticleEmitter, finds PD by Fields[1].
// PS2 ProcessCreateEmitter at 0x00477B48 (per-frame):
//   Fields[2] bits 0-15 = nodeID for bone attachment (0xFFFF = no bone)
//   Fields[2] bit 16 = use casterActorID for position, bit 17 = use targetActorID
//   Fields[2] bits 20-21 = emit shape (0=point, 1=line, 2=ring, 3=sphere)
//   Fields[3] = auto-expire lifetime in ms (0 = no auto-expire)
//   Position set per-frame from actor transform/bone, NOT from Floats at creation time.
func (d *SpellEventDriver) newCreateEmitter(ev *esf.SpellEvent, now time.Time) {
	pdDictID := ev.Fields[1] // ParticleDefinition DictID
	ps := d.effDriver.particles

	// PS2 uses Fields[3] as per-emitter lifetime (0 = no auto-expire, destroyed by DestroyEmitter)
	var maxLife time.Duration
	if ev.Fields[3] > 0 {
		maxLife = time.Duration(ev.Fields[3]) * time.Millisecond
	}

	// PS2: emitShape = (Fields[2] >> 20) & 0x3
	emitShape := (ev.Fields[2] >> 20) & 0x3

	// PS2: nodeID = Fields[2] & 0xFFFF (bottom 16 bits = bone index)
	nodeID := uint16(ev.Fields[2] & 0xFFFF)

	// The particle system knows the emitter by a global ID, since concurrent
	// effects reuse the same local IDs (Fields[0], 1-6 per effect); the
	// list's own events find it by the local one (findActive).
	localID := ev.Fields[0]
	globalID := localID
	if ps != nil {
		globalID = ps.NextGlobalEmitterID()
	}

	ae := &ActiveEvent{
		eventType: ev.EventType,
		localID:   localID,
		id:        globalID,
		event:     ev,
		startTime: now,
		maxLife:   maxLife,
		emitShape: emitShape,
		nodeID:    nodeID,
	}
	d.activeEvents = append(d.activeEvents, ae)

	log.Printf("[SPELL]   emitter local=%d global=%d shape=%d nodeID=%d Fields[2]=0x%08X actor=0x%08X caster=0x%08X target=0x%08X lifetime=%dms",
		localID, globalID, emitShape, nodeID, uint32(ev.Fields[2]),
		d.actorID, d.effDriver.casterObjID, d.effDriver.targetObjID, ev.Fields[3])
	if emitShape >= 1 {
		log.Printf("[SPELL]   shape params: Floats[0]=%.3f Floats[1]=%.3f Floats[2]=%.3f Floats[3]=%.3f Floats[4]=%.3f",
			ev.Floats[0], ev.Floats[1], ev.Floats[2], ev.Floats[3], ev.Floats[4])
	}

	// Track this emitter's global ID for cleanup when driver finishes
	d.effDriver.trackEmitter(globalID)

	// Create a real particle emitter if particle system is available
	if ps != nil {
		// Determine which actor to track based on Fields[2] override bits
		actorID := d.actorID
		if ev.Fields[2]&0x10000 != 0 {
			actorID = d.effDriver.casterObjID // bit 16: use caster
		}
		if ev.Fields[2]&0x20000 != 0 {
			actorID = d.effDriver.targetObjID // bit 17: use target
		}

		// Resolve initial position — use bone if available, otherwise entity center
		var pos [3]float32
		x, y, z := d.resolveActorPos(actorID, nodeID)
		pos = [3]float32{x, y, z}

		// PS2: for shapes 1-3, Floats[0-4] control rotating radius/offset.
		// For shape 0 (point), Floats are NOT used as position offsets.
		// The per-frame emit shape offset is computed in processActive.
		var offset [3]float32
		// PS2: Fields[3]=0 means infinite — emitter lives until DestroyEmitter.
		// Pass a generous fallback for the particle system's internal timeout.
		emitterLife := maxLife
		if emitterLife <= 0 {
			emitterLife = 30 * time.Second // generous fallback for infinite emitters
		}
		em := ps.CreateEmitter(globalID, pdDictID, pos, offset, emitterLife)
		// PS2: emitter direction matrix set from actor world transform.
		// Apply actor facing as Y-rotation for particle birth direction.
		if em != nil && d.effDriver.resolveFacing != nil {
			facing := d.effDriver.resolveFacing(actorID)
			em.SetFacing(facing)
		}
	}
}

// newSetEmitterMotif is VISpellEventDriver::NewSetEmitterMotif. Fields[0]
// names the CreateEmitter, Fields[1] the motif (the hash of its name, which
// ParseSpellEffectObj leaves unresolved), Fields[2] the ramp in ms and
// Floats[0] the factor it ramps to. The event lives as long as the emitter —
// the PS2 retires it once the blender's motif is unlinked — so the factor is
// HELD after the ramp; it used to expire after Fields[2] ms, or never when that
// was 0 (66 of 612), which held the effect for the 60 s timeout. A second event
// on the same emitter and motif takes the blender over from where it had got.
func (d *SpellEventDriver) newSetEmitterMotif(ev *esf.SpellEvent, now time.Time) {
	ce := d.findActive(3, ev.Fields[0])
	if ce == nil {
		return
	}
	ae := &ActiveEvent{
		eventType: ev.EventType,
		localID:   ev.Fields[0],
		id:        ce.id,
		event:     ev,
		startTime: now,
	}
	taken := false
	for i, old := range d.activeEvents {
		if old.eventType == 6 && old.localID == ev.Fields[0] && old.blender.Linked() &&
			viHashResourceID(old.blender.motif.Name) == ev.Fields[1] {
			ae.blender = old.blender
			ae.blendStart = old.blender.Factor()
			d.activeEvents = append(d.activeEvents[:i], d.activeEvents[i+1:]...)
			taken = true
			break
		}
	}
	if !taken {
		ae.blender = d.effDriver.particles.LinkMotifBlender(ce.id, ev.Fields[1])
	}
	d.activeEvents = append(d.activeEvents, ae)
}

// newSetEmitterAttractor is VISpellEventDriver::NewSetEmitterAttractor:
// Fields[0] names the CreateEmitter, Fields[1] is the attractor SELECTOR
// (bit 16 caster, bit 17 target, else this list's actor; low 16 bits the
// node) and Floats[0] the pull. Fields[1] used to be taken for an object ID.
func (d *SpellEventDriver) newSetEmitterAttractor(ev *esf.SpellEvent) {
	if ae := d.findActive(3, ev.Fields[0]); ae != nil {
		ae.attractorSet = true
		ae.attractorSel = ev.Fields[1]
		ae.attractorStrength = ev.Floats[0]
	}
}

// flagActor is the actor a CreateEmitter/Sound/Sprite flags word picks:
// bit 16 the caster, bit 17 the target (which wins), else this list's actor.
func (d *SpellEventDriver) flagActor(flags int32) uint32 {
	actor := d.actorID
	if flags&0x10000 != 0 {
		actor = d.effDriver.casterObjID
	}
	if flags&0x20000 != 0 {
		actor = d.effDriver.targetObjID
	}
	return actor
}

// emitShapeOffset is ProcessCreateEmitter's offset for emit shapes 1-3, in the
// actor's frame: the shape's vector times RotateHPR of its angle, with row
// vectors (VIMatrix44::Mul). Shape 1 turns (r, extent, 0) about X, shape 2
// turns (0, r, extent) about Y and shape 3 turns (extent, 0, r) about Z. The
// port had the transpose's signs, so every orbit ran the other way round.
func emitShapeOffset(ae *ActiveEvent) [3]float32 {
	ev := ae.event
	t := float32(math.Sin(float64(ae.emitRadParam)))*0.5 + 0.5
	r := ev.Floats[0] + (ev.Floats[1]-ev.Floats[0])*t
	ext := ev.Floats[4]
	a := ae.emitAngle
	switch ae.emitShape {
	case 1:
		return rotateHPR(0, a, 0, [3]float32{r, ext, 0})
	case 2:
		return rotateHPR(a, 0, 0, [3]float32{0, r, ext})
	case 3:
		return rotateHPR(0, 0, a, [3]float32{ext, 0, r})
	}
	return [3]float32{}
}

// wrap2Pi folds an emit-shape angle back once past ±2π, as
// ProcessCreateEmitter does.
func wrap2Pi(a float32) float32 {
	const twoPi = float32(6.2831855)
	if a > twoPi {
		return a - twoPi
	}
	if a < -twoPi {
		return a + twoPi
	}
	return a
}

// placeEmitter is ProcessCreateEmitter's placing: the emitter goes to the node
// on the actor its flags pick, plus the shape offset turned by the actor's
// facing (the actor transform, heading only here), takes that facing as its
// direction, and has its attractor set or cleared.
func (d *SpellEventDriver) placeEmitter(ae *ActiveEvent) {
	actorID := d.flagActor(ae.event.Fields[2])
	ax, ay, az := d.resolveActorPos(actorID, ae.nodeID)
	var off [3]float32
	if ae.emitShape >= 1 && ae.emitShape <= 3 {
		off = emitShapeOffset(ae)
		if d.effDriver.resolveFacing != nil {
			facing := d.effDriver.resolveFacing(actorID)
			c := float32(math.Cos(float64(facing)))
			sn := float32(math.Sin(float64(facing)))
			off[0], off[2] = off[0]*c+off[2]*sn, -off[0]*sn+off[2]*c
		}
	}
	for _, em := range d.effDriver.particles.emitters {
		if em.emitterID != ae.id {
			continue
		}
		em.SetPosition(ax+off[0], ay+off[1], az+off[2])
		em.SetTrackPos(ax, ay, az) // actor raw pos for local-space tracking
		if d.effDriver.resolveFacing != nil {
			em.SetFacing(d.effDriver.resolveFacing(actorID))
		}
		d.applyAttractor(ae, em)
	}
}

// applyAttractor is the attractor half of ProcessCreateEmitter, which runs for
// a draining emitter as well: no SetEmitterAttractor, or a selector of -1,
// clears it; otherwise it is the selected actor's node at the event's pull.
func (d *SpellEventDriver) applyAttractor(ae *ActiveEvent, em *ParticleEmitter) {
	if !ae.attractorSet || ae.attractorSel == -1 {
		em.attractorActive = false // ClearAttractor
		return
	}
	actor := d.flagActor(ae.attractorSel)
	if actor == 0xFFFFFFFF {
		return // no sprite: the PS2 leaves the attractor as it was
	}
	x, y, z := d.resolveActorPos(actor, uint16(ae.attractorSel&0xFFFF))
	em.attractorPos = [3]float32{x, y, z}
	em.attractorStrength = ae.attractorStrength
	em.attractorActive = true
}

// targetDrivers creates the drivers a PlayTarget event asks for — PS2
// NewPlayTargetProjectile / NewPlayTargetLightning walk i = 0..numSecondary:
// the Target list for the primary target at i = 0 when Fields[0] bit 0 is set,
// the Area list for secondary i-1 at every later i when bit 1 is.
func (d *SpellEventDriver) targetDrivers(flags int32) []*SpellEventDriver {
	var out []*SpellEventDriver
	eff := d.effDriver
	for i := 0; i <= len(eff.secondary); i++ {
		var nd *SpellEventDriver
		if i == 0 && flags&1 != 0 {
			nd = eff.addEventDriver(1, eff.targetObjID)
		}
		if i != 0 && flags&2 != 0 {
			nd = eff.addEventDriver(2, eff.secondary[i-1])
		}
		if nd != nil {
			out = append(out, nd)
		}
	}
	return out
}

// newPlayTargetProjectile is VISpellEventDriver::NewPlayTargetProjectile: a
// projectile per target driver, starting at the caster.
//
//	Fields[0]  bit 0 = Target list for the target, bit 1 = Area list per secondary
//	Fields[1]  the trail's particle definition (-1 or 0 = none)
//	Fields[2]  low byte: where it leaves the caster — node 0, node 1, their
//	           midpoint, or both (two ends); bits 8-11 the flight mode;
//	           bit 12 the caster's item projectile as an actor (not drawn here)
//	Floats[0]  speed; Floats[1] orbit radius / jitter size; Floats[2] orbit rate
func (d *SpellEventDriver) newPlayTargetProjectile(ev *esf.SpellEvent, now time.Time) {
	if d.actorID == 0xFFFFFFFF {
		return
	}
	for _, td := range d.targetDrivers(ev.Fields[0]) {
		td.projectileActive = true
		td.projectileStart = now
		td.projectileLastTick = now
		td.projectileEvent = ev
		td.projectileSource = d.actorID
		td.projectileMode = (ev.Fields[2] >> 8) & 0xF
		td.projectileAngle, td.projectileRadius = 0, 0
		td.projectileB, td.projectileC = [2][3]float32{}, [2][3]float32{}
		switch td.projectileMode {
		case 1:
			// rand() is 0..2^31-1 on this build, so this is an angle of up to
			// ~401,000 rad, and at that size a float32 steps in 1/32s: the
			// PS2's orbit advances in quantised steps, and so does this.
			if ev.Fields[2]&0xFF != 3 {
				td.projectileAngle = float32(rand.Int31()) / 5347
			}
		case 2:
			td.projectileAngle = 1
		}

		// The start, off the caster's nodes (GetNodeTransform).
		n0x, n0y, n0z := d.resolveActorPos(d.actorID, 0)
		n1x, n1y, n1z := d.resolveActorPos(d.actorID, 1)
		n0, n1 := [3]float32{n0x, n0y, n0z}, [3]float32{n1x, n1y, n1z}
		mid := [3]float32{(n0[0] + n1[0]) / 2, (n0[1] + n1[1]) / 2, (n0[2] + n1[2]) / 2}
		var start [3]float32
		twoEnds := false
		switch ev.Fields[2] & 0xFF {
		case 0:
			start = n0
		case 1:
			start = n1
		case 2:
			start = mid
		case 3:
			start = mid
			twoEnds = true
			// The two ends' offsets from the midpoint, in the caster's own
			// frame (its transform inverted); in flight they are laid along
			// the projectile's frame instead.
			facing := float32(0)
			if d.effDriver.resolveFacing != nil {
				facing = d.effDriver.resolveFacing(d.actorID)
			}
			c, sn := float32(math.Cos(float64(facing))), float32(math.Sin(float64(facing)))
			local := func(w [3]float32) [3]float32 {
				o := vsub(w, mid)
				return [3]float32{c*o[0] - sn*o[2], o[1], sn*o[0] + c*o[2]}
			}
			td.projectileA = [2][3]float32{local(n0), local(n1)}
			td.projectileC = td.projectileA
		default:
			start = mid
		}
		td.projectilePos = start
		td.projectileStartPos = start
		td.projectileDrawPos = start

		td.projectileEmitters = [2]*ParticleEmitter{}
		// The file holds a DictID; the PS2 parser resolves it to an index and
		// turns -1 or 0 into -1, so its `m_id >= 0` means "has a definition".
		if eff := d.effDriver; eff.particles != nil && ev.Fields[1] != -1 && ev.Fields[1] != 0 {
			ends := 1
			if twoEnds {
				ends = 2
			}
			for i := 0; i < ends; i++ {
				id := eff.particles.NextTempEmitterID()
				td.projectileEmitIDs[i] = id
				td.projectileEmitters[i] = eff.particles.CreateEmitter(id, ev.Fields[1], start, [3]float32{}, 60*time.Second)
			}
		}
		log.Printf("[SPELL]   projectile 0x%08X -> 0x%08X: mode=%d from=%d speed=%.1f",
			d.actorID, td.actorID, td.projectileMode, ev.Fields[2]&0xFF, ev.Floats[0])
	}
}

// newPlayTargetInstant creates Target/Area event drivers for instant effects.
// PS2 NewPlayTargetInstant at 0x00476C30:
//   bit 0 = create Target list (index 1) driver for targetActorID
//   bit 1 = create Area list (index 2) driver for each secondary target (we use targetObjID)
func (d *SpellEventDriver) newPlayTargetInstant(ev *esf.SpellEvent) {
	d.targetDrivers(ev.Fields[0])
}

// newCreateSprite creates a sprite active event.
// PS2 NewCreateSprite at 0x004777E0:
//   Fields[1] = sprite DictID, Fields[2] = bit flags (bit 18=loop, bit 19=play-once)
//   Fields[3] = duration in ms (0 = no auto-expire, waits for DestroySprite)
//   Fields[0] is the id NewDestroySprite / NewFadeSprite find it by.
func (d *SpellEventDriver) newCreateSprite(ev *esf.SpellEvent, now time.Time) {
	// PS2: Fields[3] as lifetime, 0 = infinite (no auto-expire, waits for DestroySprite)
	var maxLife time.Duration
	if ev.Fields[3] > 0 {
		maxLife = time.Duration(ev.Fields[3]) * time.Millisecond
	}

	// Pre-load the sprite model into GPU cache
	spriteDictID := ev.Fields[1]
	if d.effDriver.system != nil && d.effDriver.system.Sprites != nil && spriteDictID != 0 {
		d.effDriver.system.Sprites.LoadSprite(spriteDictID)
	}

	// PS2 ProcessCreateSprite: Fields[2] bit 16 = use caster, bit 17 = use target
	// (Same convention as CreateEmitter — confirmed from effDriver+0x08=caster, +0x0C=target)
	actorID := d.actorID
	if ev.Fields[2]&0x10000 != 0 {
		actorID = d.effDriver.casterObjID
	}
	if ev.Fields[2]&0x20000 != 0 {
		actorID = d.effDriver.targetObjID
	}

	ae := &ActiveEvent{
		eventType:     ev.EventType,
		localID:       ev.Fields[0],
		id:            ev.Fields[0],
		event:         ev,
		startTime:     now,
		maxLife:       maxLife,
		spriteDictID:  spriteDictID,
		spriteActorID: actorID,
		spriteOpacity: 1.0,
	}
	d.activeEvents = append(d.activeEvents, ae)
}

// newCreateSound is VISpellEventDriver::NewCreateSound + ProcessCreateSound.
// The sound is a scene actor at the node Fields[2] names, on the actor its bits
// 16/17 pick, at scale Floats[0]. VIScene::UpdateSoundEmitters fades it to
// silence at scale × the sound sprite's radius, with the sprite's volume; the
// 0xB101 header holds radius and volume, or neither in a version-0 header
// (1 and 1): 491 of SPELLFX's 527 CreateSounds, so their reach is 10 units,
// not the 50 this used to give every sound. Fields[3] is its lifetime, and
// the sound stops when the event goes. Fields[0] is the id DestroySound finds
// it by; the audio system gets an ID of its own.
//
// Not ported (the AudioPlayer cannot say them): Fields[2] bit 18 (pan; without
// it the sound is centred), bits 19/20 (the two quartic attenuation curves),
// and moving with the actor after it starts.
func (d *SpellEventDriver) newCreateSound(ev *esf.SpellEvent, now time.Time) {
	if d.actorID == 0xFFFFFFFF {
		return
	}
	var maxLife time.Duration
	if ev.Fields[3] > 0 {
		maxLife = time.Duration(ev.Fields[3]) * time.Millisecond
	}
	sys := d.effDriver.system
	ae := &ActiveEvent{
		eventType: ev.EventType,
		localID:   ev.Fields[0],
		id:        ev.Fields[0],
		event:     ev,
		startTime: now,
		maxLife:   maxLife,
	}
	if sys != nil {
		ae.id = sys.newSoundID()
	}
	d.activeEvents = append(d.activeEvents, ae)

	if sys == nil || sys.Audio == nil || !sys.Audio.HasSound(ev.Fields[1]) {
		return
	}
	actor := d.flagActor(ev.Fields[2])
	if actor == 0xFFFFFFFF {
		return
	}
	x, y, z := d.resolveActorPos(actor, uint16(ev.Fields[2]&0xFFFF))
	radius, volume := sys.soundSpriteParams(ev.Fields[1])
	sys.Audio.Play(ev.Fields[1], ae.id, [3]float32{x, y, z}, volume, ev.Floats[0]*radius)
}

// stopSound stops a CreateSound event's sound.
func (d *SpellEventDriver) stopSound(ae *ActiveEvent) {
	if sys := d.effDriver.system; sys != nil && sys.Audio != nil {
		sys.Audio.Stop(ae.id)
	}
}

// newPlayTargetLightning creates a lightning bolt from caster to target.
// PS2 NewPlayTargetLightning at 0x004779E0:
//   Fields[0] bitmask: bit 0 = create caster target driver, bit 1 = create target driver
//   Fields[1] low byte = node selector (0-3)
//   Fields[2] = duration in ms
//   Floats[0-2] = lightning color RGB
func (d *SpellEventDriver) newPlayTargetLightning(ev *esf.SpellEvent, now time.Time) {
	if d.actorID == 0xFFFFFFFF {
		return
	}
	// The bolt belongs to each target driver, from this (the caster's) actor.
	for _, td := range d.targetDrivers(ev.Fields[0]) {
		if td.lightningActive {
			continue
		}
		td.lightningSource = d.actorID
		td.lightningActive = true
		td.lightningHeld = true
		td.lightningStart = now
		td.lightningDurMS = ev.Fields[2]
		td.lightningColor = [3]float32{ev.Floats[0], ev.Floats[1], ev.Floats[2]}
		td.lightningMaxBranch = ev.Fields[3]
		if td.lightningMaxBranch > 3 {
			td.lightningMaxBranch = 3
		}
		td.lightningBoneMode = ev.Fields[1] & 0xFF
	}
}

// newFadeSprite starts a sprite opacity fade.
// PS2 NewFadeSprite at 0x00476920 + ProcessFadeSprite at 0x00478778:
//   Fields[0] = sprite ID to fade
//   Fields[1] = fade duration as int32 (PS2: lwc1+cvt.s.w = int→float, NOT float bits)
//   Floats[0] = target opacity (0.0-1.0)
//   fadeStart = captured from current sprite opacity (we use 1.0 since no scene access)
func (d *SpellEventDriver) newFadeSprite(ev *esf.SpellEvent, now time.Time) {
	// PS2 ProcessFadeSprite: lwc1 $f2, 8($s3) + cvt.s.w $f2, $f2
	// This is integer-to-float conversion, NOT float bit reinterpretation.
	// Fields[1] is an integer duration (frames or ms).
	durMS := ev.Fields[1]
	if durMS <= 0 {
		durMS = 1 // PS2: Fields[1]=0 → instant fade (division yields ∞ which clamps)
	}

	// PS2 NewFadeSprite: captures current sprite opacity from the matching
	// CreateSprite active event's material alpha. We read spriteOpacity instead.
	fadeStart := float32(1.0)
	for _, ae := range d.activeEvents {
		if ae.eventType == 9 && ae.id == ev.Fields[0] {
			fadeStart = ae.spriteOpacity
			break
		}
	}

	ae := &ActiveEvent{
		eventType: ev.EventType,
		localID:   ev.Fields[0],
		id:        ev.Fields[0],
		event:     ev,
		startTime: now,
		maxLife:   time.Duration(durMS) * time.Millisecond,
		fadeStart: fadeStart,    // PS2 captures current sprite opacity
		fadeEnd:   ev.Floats[0], // PS2 target opacity from Floats[0]
		fadeDurMS: durMS,
	}
	d.activeEvents = append(d.activeEvents, ae)
}

// newDestroyByType is NewDestroyEmitter / NewDestroySound / NewDestroySprite:
// the first active event of the type with that Fields[0] (findActive).
//   - Emitters (type 3): moved to inactiveEvents to drain (particles fade out)
//   - Sprites (type 9) and sounds (type 11): deleted at once, a sound stopping
func (d *SpellEventDriver) newDestroyByType(ev *esf.SpellEvent, matchType uint32) {
	ae := d.findActive(matchType, ev.Fields[0])
	if ae == nil {
		return
	}
	for i, x := range d.activeEvents {
		if x == ae {
			d.activeEvents = append(d.activeEvents[:i], d.activeEvents[i+1:]...)
			break
		}
	}
	switch matchType {
	case 3:
		if d.effDriver.particles != nil {
			d.effDriver.particles.DrainEmitter(ae.id)
		}
		d.inactiveEvents = append(d.inactiveEvents, ae)
	case 11:
		d.stopSound(ae)
	}
}

// newSetAction is VISpellEventDriver::NewSetAction: the actor plays action
// Fields[0] (a CPlayList slot), the handle is kept for Wait 0, and the
// animation's "Spell FX 1-4" triggers are armed for Wait 3 — which used to
// wait out its timeout instead, because only PlayAttack armed them.
func (d *SpellEventDriver) newSetAction(ev *esf.SpellEvent) {
	sys := d.effDriver.system
	if d.actorID == 0xFFFFFFFF || sys == nil {
		return
	}
	if !d.isCSprite() {
		d.actionNodeID = -1
		d.animNodes = [4]int{-1, -1, -1, -1}
		return
	}
	d.actionNodeID = -1
	if sys.OnSetAction != nil {
		d.actionNodeID = sys.OnSetAction(d.actorID, ev.Fields[0])
	}
	d.armSpellFXTriggers()
}

// isCSprite is the PS2's `GetActorSpritePtr(m_actor)->m_type == 5`; with
// no way to ask, every actor counts as one.
func (d *SpellEventDriver) isCSprite() bool {
	sys := d.effDriver.system
	return sys == nil || sys.IsCSprite == nil || sys.IsCSprite(d.actorID)
}

// armSpellFXTriggers finds the actor's "Spell FX 1".."Spell FX 4" triggers
// and resets each one found — the tail NewSetAction and NewPlayAttack share.
func (d *SpellEventDriver) armSpellFXTriggers() {
	sys := d.effDriver.system
	triggerNames := [4]string{"Spell FX 1", "Spell FX 2", "Spell FX 3", "Spell FX 4"}
	for i := 0; i < 4; i++ {
		d.animNodes[i] = -1
		if sys.FindTrigger != nil {
			if idx, ok := sys.FindTrigger(d.actorID, triggerNames[i]); ok {
				d.animNodes[i] = idx
			}
		}
	}
	for _, idx := range d.animNodes {
		if idx != -1 && sys.ResetTrigger != nil {
			sys.ResetTrigger(d.actorID, idx)
		}
	}
}

func (d *SpellEventDriver) newPlayAttack(ev *esf.SpellEvent) {
	// PlayAttack is a one-shot animation trigger (no ActiveEvent created).
	// PS2 NewPlayAttack at 0x00476370: uses this->actorID (+0x18) for sprite lookup.
	//   Fields[0] = attack type (0-3):
	//     0 → SetAttackAction(slot=0)    — primary weapon, primary swing
	//     1 → SetAttackAction(slot=1)    — secondary weapon, primary swing
	//     2 → SetAttackAltAction(slot=0) — primary weapon, alt swing
	//     3 → SetAttackAltAction(slot=1) — secondary weapon, alt swing
	//   Fields[1] = weapon trail attachment ID (passed to SetWeaponTrail)
	//   Floats[0-2] = weapon trail color RGB (passed to SetWeaponTrail as VIColor32F)
	animPair := ev.Fields[0]
	trailColor := [3]float32{ev.Floats[0], ev.Floats[1], ev.Floats[2]}
	trailID := ev.Fields[1]

	sys := d.effDriver.system
	if d.actorID == 0xFFFFFFFF || sys == nil {
		return
	}
	if !d.isCSprite() {
		d.actionNodeID = -1
		d.animNodes = [4]int{-1, -1, -1, -1}
		return
	}

	// The attack animation; SetAttackAction's handle is what Wait 0 waits on.
	// Only types 0-3 set one: the PS2's switch has no default.
	if sys.OnPlayAttack != nil && animPair >= 0 && animPair <= 3 {
		d.actionNodeID = sys.OnPlayAttack(d.actorID, animPair, trailColor, trailID)
	}

	// PS2 SetWeaponTrail: stores trail attachment ID + RGBA color on CSprite.
	// ProcessWeaponTrails renders trail geometry from consecutive bone positions.
	if sys.SetWeaponTrail != nil {
		sys.SetWeaponTrail(d.actorID, trailID, trailColor)
	}

	// The "Spell FX 1-4" triggers: Wait 3 waits for the swing to reach one.
	d.armSpellFXTriggers()
}

// newShakeCamera is VISpellEventDriver::NewShakeCamera. It acts only when this
// list's actor is the local player. Fields[0] bit 16 starts a shake at speed
// Fields[1] (low byte: 0 stops it, 1-3 slow/medium/fast) with amplitude
// Floats[0]; bit 17 ramps the amplitude to Floats[1] over Floats[2] seconds.
// The port's own model (a decaying sine with a duration) is gone with it.
func (d *SpellEventDriver) newShakeCamera(ev *esf.SpellEvent) {
	sys := d.effDriver.system
	if sys == nil || sys.IsPlayer == nil || !sys.IsPlayer(d.actorID) {
		return
	}
	ce := &sys.Camera
	speed := int32(uint8(ev.Fields[1]))
	if ev.Fields[0]&0x10000 != 0 {
		switch speed {
		case 0:
			ce.Shake(0, 0)
		case 1, 2, 3:
			ce.Shake(speed, ev.Floats[0])
		}
	}
	if ev.Fields[0]&0x20000 != 0 {
		ce.ShakeTerp(ev.Floats[1], ev.Floats[2])
	}
}

// CameraEffect is VICameraEffect's shake (its manual camera move is not used by
// spells). The speed picks a cycle of samples, and each Apply bobs the eye by
// amplitude·0.025 and the look direction by amplitude·0.009 times the next
// sample, VERTICALLY only. It does not end by itself: a speed-0 Shake stops it,
// and a ShakeTerp to 0 silences it.
type CameraEffect struct {
	speed         int32
	sample        int32
	amplitude     float32
	amplitudeEnd  float32
	amplitudeRate float32
}

// shakeCount and shakeSample are VICameraEffect's two file-scope tables.
var shakeCount = [4]int32{0, 6, 4, 2}
var shakeSample = [4][6]float32{
	{0, 0, 0, 0, 0, 0},
	{1, 0.7, -0.7, -1, -0.7, 0.7},
	{1, 1, -1, -1, 0, 0},
	{1, -1, 0, 0, 0, 0},
}

// Shake is VICameraEffect::Shake.
func (c *CameraEffect) Shake(speed int32, amp float32) {
	c.speed = speed
	c.amplitude = amp
	c.amplitudeEnd = amp
	c.amplitudeRate = 0
}

// ShakeTerp is VICameraEffect::ShakeTerp: ramp from the last target to amp
// over time seconds (at once when time is 0).
func (c *CameraEffect) ShakeTerp(amp, time float32) {
	if time == 0 {
		c.Shake(c.speed, amp)
		return
	}
	from := c.amplitudeEnd
	c.amplitudeEnd = amp
	c.amplitude = from
	c.amplitudeRate = (amp - from) / time
}

// Update is VICameraEffect::Update's shake half: the amplitude ramp, dt in
// seconds.
func (c *CameraEffect) Update(dt float32) {
	if c.speed == 0 || c.amplitudeRate == 0 {
		return
	}
	c.amplitude += c.amplitudeRate * dt
	if (c.amplitudeRate > 0 && c.amplitude >= c.amplitudeEnd) ||
		(c.amplitudeRate < 0 && c.amplitude <= c.amplitudeEnd) {
		c.amplitude = c.amplitudeEnd
		c.amplitudeRate = 0
	}
}

// Apply is VICameraEffect::Apply's shake half, once per frame: pos is the eye
// and dir the look direction, which comes back normalized and bobbed.
func (c *CameraEffect) Apply(pos, dir *[3]float32) {
	if c.speed <= 0 || c.speed > 3 {
		return
	}
	*dir = vnorm(*dir)
	c.sample %= shakeCount[c.speed]
	s := shakeSample[c.speed][c.sample]
	pos[1] += c.amplitude * 0.025 * s
	dir[1] += c.amplitude * 0.009 * s
	c.sample++
}

// --- Spell Effect Driver (manages one active spell) ---

// SpellEffectDriver manages one active spell effect instance.
// Port of VISpellEffectDriver from SUPPORT module (struct ≥0x68 bytes).
type SpellEffectDriver struct {
	effect       *esf.SpellEffect
	casterObjID  uint32
	targetObjID  uint32
	triggerTime  time.Time
	triggerDurMS int // cast duration in ms
	triggerFlags int
	eventDrivers []*SpellEventDriver
	finished     bool
	particles    *ParticleSystem // shared particle system reference
	resolvePos   PosResolver     // position resolver for entity tracking
	resolveBone  BoneResolver    // bone position resolver (nil = use entity center)
	resolveFacing FacingResolver // facing angle resolver for emitter direction
	emitterPDs   []int32         // PD DictIDs of emitters created (for cleanup)
	system       *SpellEffectSystem // back-reference for camera shake etc.
	// secondary are the effect's secondary targets (AddSecondaryTarget, at
	// most 8): the Area list plays on each of them.
	secondary []uint32
}

func newSpellEffectDriver(effect *esf.SpellEffect, caster, target uint32, ps *ParticleSystem, resolve PosResolver, resolveBone BoneResolver, resolveFacing FacingResolver, sys *SpellEffectSystem) *SpellEffectDriver {
	return &SpellEffectDriver{
		effect:        effect,
		casterObjID:   caster,
		targetObjID:   target,
		particles:     ps,
		resolvePos:    resolve,
		resolveBone:   resolveBone,
		resolveFacing: resolveFacing,
		system:        sys,
	}
}

// Trigger starts the effect playback, creating ONLY the Caster event driver.
// Port of VISpellEffectDriver::Trigger at 0x0047AC90.
// PS2: Trigger() calls AddSpellEventDriver(eventLists[0], casterActorID) — only Caster.
// Target/Area event drivers are created on-demand by PlayTargetProjectile/Lightning/Instant.
func (d *SpellEffectDriver) Trigger(durationMS, flags int, startTime time.Time) {
	if len(d.eventDrivers) > 0 {
		return
	}
	// PS2 Trigger guard: reject if casterActorID == -1 (0xFFFFFFFF)
	if d.casterObjID == 0xFFFFFFFF {
		return
	}
	if startTime.IsZero() {
		startTime = time.Now()
	}
	d.triggerTime = startTime
	d.triggerDurMS = durationMS
	d.triggerFlags = flags

	// Only create Caster event driver (list index 0)
	casterEvents := d.effect.EventLists[0]
	if len(casterEvents) > 0 {
		ed := newSpellEventDriver(d, casterEvents, d.casterObjID, 0)
		d.eventDrivers = append(d.eventDrivers, ed)
	}
}

// addEventDriver creates a new event driver for the given list index and actor.
// PS2: Called by PlayTargetProjectile/Lightning/Instant to create Target event drivers
// on-demand from the Caster event list's PlayTarget* events.
// Fields[0] bitmask: bit 0 = create driver for casterObjID, bit 1 = create driver for targetObjID.
// The PS2's AddSpellEventDriver creates one even for an empty list: a target
// driver with no events still carries the projectile or bolt it was made for.
func (d *SpellEffectDriver) addEventDriver(listIndex int, actorID uint32) *SpellEventDriver {
	if listIndex < 0 || listIndex > 2 {
		return nil
	}
	ed := newSpellEventDriver(d, d.effect.EventLists[listIndex], actorID, listIndex)
	d.eventDrivers = append(d.eventDrivers, ed)
	return ed
}

// trackEmitter records an emitter ID (Fields[0]) for cleanup when this driver finishes.
func (d *SpellEffectDriver) trackEmitter(emitterID int32) {
	d.emitterPDs = append(d.emitterPDs, emitterID)
}

// cleanupEmitters destroys all particle emitters created by this driver.
// Called when the driver finishes or is force-expired.
// PS2: emitters are destroyed with their parent VISpellEffectDriver.
func (d *SpellEffectDriver) cleanupEmitters() {
	// Sounds still playing (the 60 s timeout, StopAll) stop with the driver,
	// as ~VISpellEventDriver deleting their events releases the sound actors.
	for _, ed := range d.eventDrivers {
		for _, ae := range ed.activeEvents {
			if ae.eventType == 11 {
				ed.stopSound(ae)
			}
		}
	}
	if d.particles == nil {
		return
	}
	for _, pdID := range d.emitterPDs {
		d.particles.DestroyEmitter(pdID)
	}
	d.emitterPDs = nil
}

// Process updates all event drivers. Returns true if still active.
// Port of VISpellEffectDriver::Process at 0x0047AD88.
// PS2 has NO absolute timeout — drivers run until all event drivers finish naturally.
func (d *SpellEffectDriver) Process() bool {
	for i := len(d.eventDrivers) - 1; i >= 0; i-- {
		if d.eventDrivers[i].Process() {
			d.eventDrivers = append(d.eventDrivers[:i], d.eventDrivers[i+1:]...)
		}
	}
	d.finished = len(d.eventDrivers) == 0
	if d.finished {
		d.cleanupEmitters()
	}
	return !d.finished
}

// --- Spell Effect System (singleton manager) ---

// PosResolver resolves an entity ObjectID to a world position.
// Returns (x, y, z). Used by particle emitters to track entity position.
type PosResolver func(objID uint32) (float32, float32, float32)

// BoneResolver resolves a bone/node world position for a given entity.
// objID = entity ObjectID, nodeID = bone index (0xFFFF = no specific bone).
// Returns (x, y, z, ok). If ok is false, caller should fall back to entity center.
// PS2: GetNodeTransform at 0x0047A490 — looks up CSprite, calls CalcSlotWorldTransform.
type BoneResolver func(objID uint32, nodeID uint16) (float32, float32, float32, bool)

// FacingResolver resolves an entity's facing angle (radians, 0=north, CW).
// PS2: ProcessCreateEmitter applies the actor's node transform rotation to the
// emitter direction matrix. This is how spell particles face the right direction.
type FacingResolver func(objID uint32) float32

// SpellEffectSystem manages all active spell effect playbacks.
// Port of VISpellEffectSystem from SUPPORT module.
type SpellEffectSystem struct {
	db              *SpellFXDB
	drivers         []*SpellEffectDriver
	Particles       *ParticleSystem
	Sprites         *SpellSpriteSystem // spell sprite mesh loading + rendering
	ResolvePos      PosResolver    // set by visual client for entity position lookups
	ResolveBone     BoneResolver   // set by visual client for bone position lookups (optional)
	ResolveFacing   FacingResolver // set by visual client for entity facing angle
	lastProcess     time.Time    // tracks time between Process() calls for actual dt
	ExternalParticles bool       // when true, Process() skips particle Update (visual client handles it)

	// deltaTime is VIParticleSystem::DeltaTime, the step every spell motion
	// runs on: the frame's clock difference capped at 0.1 s and averaged over
	// eight frames. A slow frame slows a projectile rather than jumping it.
	deltaTime float32
	dtHistory [8]float32

	nextSoundID int32 // newSoundID's counter

	// Camera is the VICameraEffect a ShakeCamera event (type 17) drives. The
	// client runs Camera.Update(dt) and Camera.Apply(&eye, &dir) each frame.
	Camera CameraEffect

	// IsPlayer reports whether an actor is the local player, the only actor a
	// ShakeCamera event acts for (VISpellEffectDriver::m_playerActor).
	IsPlayer func(objID uint32) bool

	// Audio system for spell sound playback (nil = no audio).
	Audio AudioPlayer

	// PlayAttack callback — visual client sets this to receive animation triggers.
	// Called with (actorObjID, animPairIndex, trailColor, trailID). One-shot.
	// PS2 also calls SetWeaponTrail with color from Floats[0-2] and ID from Fields[1].
	// Returns the action handle SetAttackAction gave, or -1.
	OnPlayAttack func(actorObjID uint32, animPair int32, trailColor [3]float32, trailID int32) int

	// SetAction callback — visual client sets this to receive action/cast animation triggers.
	// PS2 NewSetAction calls VICSprite::SetAction(actor, Fields[0], Floats[0]).
	// Fields[0] = action type, Floats[0] = parameter (unused by most actions).
	// Returns the action handle SetAction gave, or -1.
	OnSetAction func(actorObjID uint32, actionType int32) int

	// IsActionComplete is VICSprite::IsActionComplete(handle) on the actor —
	// Wait sub-type 0.
	IsActionComplete func(actorObjID uint32, handle int) bool

	// FindTrigger finds the trigger index for a named trigger on an actor's HSprite.
	// PS2 FindTrigger at 0x004464E8: linear search trigger array for name hash match.
	// Returns (index, true) if found, (-1, false) if not.
	FindTrigger func(actorObjID uint32, triggerName string) (int, bool)

	// ResetTrigger clears the tripped flag for a trigger at the given index.
	// PS2 ResetTrigger at 0x00446550: sets triggerArray[idx*16+4] = 0.
	ResetTrigger func(actorObjID uint32, triggerIdx int)

	// HasTripped returns true if the trigger at the given index has fired.
	// PS2 HasTripped at 0x00446538: returns triggerArray[idx*16+4] != 0.
	HasTripped func(actorObjID uint32, triggerIdx int) bool

	// IsCSprite reports whether the actor's sprite is a VICSprite — the PS2's
	// `m_type == 5`. Every character model is one, creatures included; for
	// anything else SetAction/PlayAttack do nothing and Wait 3 clears at once.
	IsCSprite func(actorObjID uint32) bool

	// SetWeaponTrail callback — visual client sets this to activate weapon swing trails.
	// PS2 SetWeaponTrail at 0x00422C60: stores trail ID and color on the CSprite.
	// ProcessWeaponTrails renders trail geometry from bone positions across frames.
	SetWeaponTrail func(actorObjID uint32, trailID int32, color [3]float32)
}

// NewSpellEffectSystem creates a new spell effect system.
// VIParticleSystem::Init seeds the delta-time history with 1/30 s.
func NewSpellEffectSystem(db *SpellFXDB) *SpellEffectSystem {
	sys := &SpellEffectSystem{
		db:        db,
		Particles: NewParticleSystem(db),
	}
	for i := range sys.dtHistory {
		sys.dtHistory[i] = 1.0 / 30.0
	}
	return sys
}

// newSoundID gives each spell sound an audio ID of its own, clear of the IDs
// the client's other sounds use (0, negative object IDs, 9990/9991).
func (sys *SpellEffectSystem) newSoundID() int32 {
	sys.nextSoundID++
	return 0x100000 + sys.nextSoundID
}

// soundSpriteParams is a SoundSprite's radius and volume as ParseSoundSpriteObj
// reads them: the 0xB101 header's two floats, or 1 and 1 when the header is
// version 0. (esf.SoundSprite calls them InnerRadius and OuterRadius.)
func (sys *SpellEffectSystem) soundSpriteParams(dictID int32) (radius, volume float32) {
	radius, volume = 1, 1
	if sys.db == nil || sys.db.File() == nil {
		return
	}
	obj, err := sys.db.File().FindObject(dictID)
	if err != nil {
		return
	}
	ss, ok := obj.(*esf.SoundSprite)
	if !ok {
		return
	}
	if h := ss.ObjInfo().Child(esf.TypeSoundSpriteHeader); h != nil && h.Version > 0 {
		return ss.InnerRadius, ss.OuterRadius
	}
	return
}

// Play starts a spell effect playback.
// effectID = SpellEffect DictID (animHash from CastingCombat 0x00B1).
// castTimeMS = cast duration in ms (0 for instant).
// casterObjID/targetObjID = entity ObjectIDs.
// startTime = when the cast began (zero = now). Allows backdating triggerTime
// so Wait(sub-type 2) expires relative to actual cast start, not arrival time.
//
// secondary are the effect's secondary targets: the PS2 plays an effect ONCE
// with up to eight of them (VISpellEffectSystem::Play's AddSecondaryTarget),
// and its PlayTarget events run the Area list on each.
func (sys *SpellEffectSystem) Play(effectID uint32, castTimeMS uint16, casterObjID, targetObjID uint32, startTime time.Time, secondary ...uint32) {
	if sys == nil || sys.db == nil {
		return
	}
	// PS2 Play() at 0x0047BCA8: rejects if caster or target actorID == -1 (0xFFFFFFFF).
	// This prevents creating effects with no valid actor reference.
	if casterObjID == 0xFFFFFFFF || targetObjID == 0xFFFFFFFF {
		return
	}
	se := sys.db.Lookup(effectID)
	if se == nil {
		log.Printf("[SPELL] Play: effectID=0x%08X NOT FOUND in db (caster=0x%08X target=0x%08X castTime=%dms)",
			effectID, casterObjID, targetObjID, castTimeMS)
		return
	}

	// PS2 Play(): 24-slot fixed pool — reject if full
	if len(sys.drivers) >= 24 {
		log.Printf("  SpellFX.Play: pool full (24), rejecting 0x%08X", effectID)
		return
	}

	log.Printf("  SpellFX.Play: 0x%08X cast=%dms caster=0x%08X target=0x%08X",
		effectID, castTimeMS, casterObjID, targetObjID)

	driver := newSpellEffectDriver(se, casterObjID, targetObjID, sys.Particles, sys.ResolvePos, sys.ResolveBone, sys.ResolveFacing, sys)
	for _, a := range secondary {
		if len(driver.secondary) == 8 {
			break
		}
		driver.secondary = append(driver.secondary, a)
	}
	// PS2 Play() at 0x0047BCA8: Trigger(castTime, 10000) — flags hardcoded to 0x2710
	driver.Trigger(int(castTimeMS), 10000, startTime)
	sys.drivers = append(sys.drivers, driver)
}

// Process updates all active spell effects and particles. Called once per engine tick.
func (sys *SpellEffectSystem) Process() {
	if sys == nil {
		return
	}
	// Compute actual dt since last Process() call.
	// PS2 runs at 30fps (33ms). Headless engine ticks at 100ms.
	// Visual client should call ProcessParticles() at render frame rate instead.
	now := time.Now()
	dt := float32(0.033) // default ~30fps
	if !sys.lastProcess.IsZero() {
		dt = float32(now.Sub(sys.lastProcess).Seconds())
		if dt > 0.2 { // clamp to 200ms max (5fps minimum)
			dt = 0.2
		}
	}
	sys.lastProcess = now

	// VIParticleSystem::Render's delta: min(0.1, step), then the mean of it
	// and the previous seven.
	step := min(dt, 0.1)
	sum := step
	for _, h := range sys.dtHistory[:7] {
		sum += h
	}
	copy(sys.dtHistory[1:], sys.dtHistory[:7])
	sys.dtHistory[0] = step
	sys.deltaTime = sum * 0.125

	for i := len(sys.drivers) - 1; i >= 0; i-- {
		drv := sys.drivers[i]
		alive := drv.Process()
		// PS2 VISpellEffectSystem::Process (0x0047C040): 60-second global timeout.
		// $s3 = 0xEA60 (60000ms). If elapsed > 60000 → Stop().
		if alive && now.Sub(drv.triggerTime).Milliseconds() > 60000 {
			log.Printf("[SPELL] 60s timeout: effect caster=0x%08X target=0x%08X", drv.casterObjID, drv.targetObjID)
			drv.cleanupEmitters()
			alive = false
		}
		if !alive {
			sys.drivers = append(sys.drivers[:i], sys.drivers[i+1:]...)
		}
	}
	// Update particle system (births, deaths, age) with actual delta time.
	// Skip if visual client handles particle updates at frame rate.
	if !sys.ExternalParticles {
		sys.Particles.Update(dt, 0, 0, 0) // no camera culling in headless/engine mode
	}
}

// ProcessParticles updates only the particle system with the given delta time.
// Called by the visual client at render frame rate for smooth particle animation,
// separate from the engine tick that drives spell event timelines.
func (sys *SpellEffectSystem) ProcessParticles(dt float32, camX, camY, camZ float32) {
	if sys == nil {
		return
	}
	sys.Particles.Update(dt, camX, camY, camZ)
}

// UpdateEmitterPositions updates all active CreateEmitter positions at render
// frame rate. PS2 calls ProcessCreateEmitter every frame (~33ms), but our
// engine tick is 100ms. Without per-frame updates, emitters decouple from
// moving entities (bone attachment lags behind by up to 100ms).
func (sys *SpellEffectSystem) UpdateEmitterPositions(dt float32) {
	if sys == nil {
		return
	}
	for _, drv := range sys.drivers {
		drv.updateEmitterPositions(dt)
	}
}

// updateEmitterPositions updates emitter positions for all event drivers.
func (d *SpellEffectDriver) updateEmitterPositions(dt float32) {
	for _, ed := range d.eventDrivers {
		ed.updateEmitterPositions(dt)
	}
}

// updateEmitterPositions resolves current actor/bone positions for all active
// CreateEmitter events and updates emitter world positions. Also advances emit
// shape rotation angles at frame rate for smooth orbit animation.
// PS2: ProcessCreateEmitter at 0x00477B48 runs this logic every render frame.
func (d *SpellEventDriver) updateEmitterPositions(dt float32) {
	if d.effDriver.particles == nil {
		return
	}
	for _, ae := range d.activeEvents {
		if ae.eventType == 3 {
			d.placeEmitter(ae)
		}
	}
}

// StopAll destroys all active spell effects and their emitters.
// Called on zone change to prevent stale effects from tracking gone entities.
func (sys *SpellEffectSystem) StopAll() {
	if sys == nil {
		return
	}
	for _, drv := range sys.drivers {
		drv.cleanupEmitters()
	}
	sys.drivers = nil
	log.Printf("[SPELL] StopAll: cleared all active effects")
}

// NotifyActorWasDestroyed is VISpellEffectSystem::NotifyActorWasDestroyed,
// called by CLIENT when it releases an entity's actor: every driver forgets
// the actor, so a projectile flying at it retires at once instead of heading
// for wherever a missing entity resolves to.
func (sys *SpellEffectSystem) NotifyActorWasDestroyed(actor uint32) {
	if sys == nil {
		return
	}
	const gone = 0xFFFFFFFF
	for _, drv := range sys.drivers {
		if drv.casterObjID == actor {
			drv.casterObjID = gone
		}
		if drv.targetObjID == actor {
			drv.targetObjID = gone
		}
		for i := range drv.secondary {
			if drv.secondary[i] == actor {
				drv.secondary[i] = gone
			}
		}
		for _, ed := range drv.eventDrivers {
			if ed.actorID == actor {
				ed.actorID = gone
			}
		}
	}
}

// StopIfThisWasCaster stops every effect the actor is casting. CLIENT calls
// it the moment an entity dies, right after VICSprite::SetDeath.
func (sys *SpellEffectSystem) StopIfThisWasCaster(actor uint32) {
	if sys == nil {
		return
	}
	kept := sys.drivers[:0]
	for _, drv := range sys.drivers {
		if drv.casterObjID == actor {
			drv.cleanupEmitters()
			continue
		}
		kept = append(kept, drv)
	}
	for i := len(kept); i < len(sys.drivers); i++ {
		sys.drivers[i] = nil
	}
	sys.drivers = kept
}

// ActiveCount returns the number of currently playing effects.
func (sys *SpellEffectSystem) ActiveCount() int {
	if sys == nil {
		return 0
	}
	return len(sys.drivers)
}

// ActiveEffects returns info about currently playing effects for display.
func (sys *SpellEffectSystem) ActiveEffects() []ActiveEffectInfo {
	if sys == nil {
		return nil
	}
	infos := make([]ActiveEffectInfo, len(sys.drivers))
	for i, d := range sys.drivers {
		infos[i] = ActiveEffectInfo{
			EffectID:    uint32(d.effect.DictID),
			CasterObjID: d.casterObjID,
			TargetObjID: d.targetObjID,
			ElapsedMS:   int(time.Since(d.triggerTime).Milliseconds()),
			ListsActive: len(d.eventDrivers),
		}
	}
	return infos
}

// --- Visual effect query (for rendering layer) ---

// VisualFXType classifies a visual effect for rendering.
type VisualFXType int

const (
	VFXEmitter    VisualFXType = iota // particle emitter on an entity
	VFXProjectile                     // projectile flying from caster to target
	VFXLightning                      // lightning bolt from caster to target
	VFXSprite                         // 3D sprite on an entity
)

// VisualFX describes a renderable spell effect for the visual client.
type VisualFX struct {
	Type        VisualFXType
	SourceObjID uint32  // caster entity ObjectID
	TargetObjID uint32  // target entity ObjectID
	Progress    float32 // 0-1 for projectiles/fades (elapsed / duration)
	Age         float32 // seconds since creation
	MaxAge      float32 // max lifetime in seconds (for fade-out)
	ColorR      float32
	ColorG      float32
	ColorB      float32
	// Sprite-specific fields
	SpriteDictID int32   // DictID of sprite mesh in SPELLFX.ESF (VFXSprite only)
	Opacity      float32 // current opacity 0-1 (VFXSprite only, affected by FadeSprite)
	// Lightning-specific fields
	LightningMaxBranch int32 // max branching bolts (0-3, from Fields[3])
	LightningBoneMode  int32 // source bone mode (0-3, from Fields[1] low byte)
	LightningDurMS     int32 // duration in ms (from Fields[2])
	LightningHeld      bool  // within its duration: hold the bolt at full charge
}

// VisualEffects returns all renderable spell effects for the current frame.
func (sys *SpellEffectSystem) VisualEffects() []VisualFX {
	if sys == nil {
		return nil
	}
	var effects []VisualFX
	now := time.Now()

	for _, drv := range sys.drivers {
		for _, ed := range drv.eventDrivers {
			// Projectile — position comes from velocity integration in processProjectile
			if ed.projectileActive {
				elapsed := float32(now.Sub(ed.projectileStart).Seconds())
				// Progress 0 at the start, 1 at the target.
				var progress float32
				tx, ty, tz := ed.resolveActorPos(ed.actorID, 4)
				if total := vlen(vsub([3]float32{tx, ty, tz}, ed.projectileStartPos)); total > 0.01 {
					progress = mathutil.ClampF(1-vlen(vsub([3]float32{tx, ty, tz}, ed.projectilePos))/total, 0, 1)
				}
				fx := VisualFX{
					Type:        VFXProjectile,
					SourceObjID: ed.projectileSource,
					TargetObjID: ed.actorID,
					Progress:    progress,
					Age:         elapsed,
					MaxAge:      10.0, // generous — projectile completes via distance
				}
				// Store current projectile position in Color fields for rendering
				fx.ColorR = ed.projectileDrawPos[0]
				fx.ColorG = ed.projectileDrawPos[1]
				fx.ColorB = ed.projectileDrawPos[2]
				effects = append(effects, fx)
			}

			// Lightning
			if ed.lightningActive && ed.actorID != 0xFFFFFFFF && ed.lightningSource != 0xFFFFFFFF {
				durMS := float32(ed.lightningDurMS)
				if durMS <= 0 {
					durMS = 1 // avoid division by zero; PS2 expires immediately
				}
				elapsed := float32(now.Sub(ed.lightningStart).Seconds())
				maxAge := durMS / 1000.0
				effects = append(effects, VisualFX{
					Type:               VFXLightning,
					SourceObjID:        ed.lightningSource,
					TargetObjID:        ed.actorID,
					Progress:           mathutil.ClampF(elapsed/maxAge, 0, 1),
					Age:                elapsed,
					MaxAge:             maxAge,
					ColorR:             ed.lightningColor[0],
					ColorG:             ed.lightningColor[1],
					ColorB:             ed.lightningColor[2],
					LightningMaxBranch: ed.lightningMaxBranch,
					LightningBoneMode:  ed.lightningBoneMode,
					LightningDurMS:     ed.lightningDurMS,
					LightningHeld:      ed.lightningHeld,
				})
			}

			// Active emitters and sprites
			for _, ae := range ed.activeEvents {
				age := float32(now.Sub(ae.startTime).Seconds())
				maxAge := float32(ae.maxLife.Seconds())
				// PS2: progress = age/maxAge clamped to [0,1]; infinite lifetime (maxAge=0) → progress stays 0
				progress := float32(0)
				if maxAge > 0 {
					progress = mathutil.ClampF(age/maxAge, 0, 1)
				}
				switch ae.eventType {
				case 3: // CreateEmitter
					effects = append(effects, VisualFX{
						Type:        VFXEmitter,
						SourceObjID: ed.actorID,
						TargetObjID: ed.actorID,
						Age:         age,
						MaxAge:      maxAge,
						Progress:    progress,
						ColorR:      ae.event.Floats[0],
						ColorG:      ae.event.Floats[1],
						ColorB:      ae.event.Floats[2],
					})
				case 9: // CreateSprite
					effects = append(effects, VisualFX{
						Type:         VFXSprite,
						SourceObjID:  ae.spriteActorID, // PS2: actor override from Fields[2] bits 16-17
						TargetObjID:  ae.spriteActorID,
						Age:          age,
						MaxAge:       maxAge,
						Progress:     progress,
						SpriteDictID: ae.spriteDictID,
						Opacity:      ae.spriteOpacity,
					})
				}
			}
		}
	}
	return effects
}

// ActiveEffectInfo describes a currently playing spell effect.
type ActiveEffectInfo struct {
	EffectID    uint32
	CasterObjID uint32
	TargetObjID uint32
	ElapsedMS   int
	ListsActive int
}
