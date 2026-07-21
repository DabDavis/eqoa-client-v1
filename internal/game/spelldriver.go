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
	id        int32           // Fields[0] — emitter/sprite/sound ID
	event     *esf.SpellEvent // reference to source event data
	startTime time.Time       // when this event was created
	maxLife   time.Duration   // auto-expire after this duration
	// Fade parameters (type 14)
	fadeStart float32
	fadeEnd   float32
	fadeDurMS int32
	// Attractor parameters (type 5 — SetEmitterAttractor)
	attractorTarget   int32   // actor ID for attraction
	attractorStrength float32 // attraction pull strength
	// Emit shape parameters (type 3 — CreateEmitter, shapes 1-3)
	// PS2 ProcessCreateEmitter: emitShape=(Fields[2]>>20)&3
	// Shapes 1-3 use rotating radius from Floats[0-4]:
	//   interpRadius = Floats[0] + (Floats[1]-Floats[0]) * (sin(angle)*0.5+0.5)
	//   angle advances by Floats[2]*dt, radius param by Floats[3]*2*dt
	emitShape    int32   // 0=point, 1=line, 2=ring, 3=sphere
	emitAngle    float32 // current rotation angle (advances per frame)
	emitRadParam float32 // current radius interpolation parameter
	// Bone attachment (type 3 — CreateEmitter)
	// PS2: nodeID = Fields[2] & 0xFFFF, 0xFFFF = no specific bone
	nodeID uint16 // bone/node index for position attachment
	emitDebugCounter int // throttle debug logging
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

	// Projectile state (phase 1)
	// PS2 creates TWO emitters per projectile (activeEvent+0x78 = src, +0x7C = dst).
	// Both use the same PD DictID. Source tracks flight position, destination tracks target.
	// PS2 uses frame-based progress accumulation, NOT a timer. Completion is distance-based.
	projectileActive     bool
	projectileStart      time.Time
	projectileFlightMode int32            // (Fields[2]>>8) & 0x0F: 0=linear, 1=arc/spiral, 2=scatter, 3=tracking
	projectilePDDictID   int32            // PD DictID for projectile trail particles
	projectileSrcEmitter *ParticleEmitter // source emitter — follows flight path
	projectileDstEmitter *ParticleEmitter // destination emitter — tracks target position
	projectilePos        [3]float32       // current projectile position (velocity-integrated)
	projectileStartPos   [3]float32       // initial position (for PS2 distance-traveled arrival check)
	projectileVel        [3]float32       // current velocity vector (units/sec)
	projectileLastTick   time.Time        // last update time for dt computation
	projectileProgress   float32          // PS2 offset 0x9C: rotation angle (mode 1), arrival state (mode 2)
	projectileArcHeight  float32          // PS2 offset 0xA0: Y offset for arc (mode 1)
	projectileInitDist   float32          // initial caster-to-target distance (for speed calc)
	projectileEvent      *esf.SpellEvent  // source event for field access during flight

	// PS2 actionNodeID at +0x34. Return value from SetAttackAction/SetAttackAltAction.
	// Used by Wait(sub-type 0) to check IsActionComplete for the specific action.
	// -1 = no action set (Wait type 0 clears immediately when -1).
	actionNodeID int // PS2 VISpellEventDriver+0x34

	// PS2 VISpellEventDriver animNodes[4] at +0x38..+0x44.
	// Set by NewPlayAttack via FindTrigger("Spell FX 1-4").
	// Checked by Wait(sub-type 3) via HasTripped.
	animNodes [4]int // trigger indices (-1 = not set)

	// Lightning state (phase 2)
	lightningActive  bool
	lightningStart   time.Time
	lightningDurMS   int32        // duration from Fields[2] (PS2: event->Fields[2])
	lightningColor   [3]float32   // RGB from Floats[0-2] (PS2: event->Floats[0-2])
	lightningFlashed   bool         // true after timer expired (charge flash sent)
	lightningFlashTime time.Time    // when the flash was triggered (for decay timing)
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

	// Map local emitter IDs (Fields[0], 1-6 per effect) to globally unique IDs.
	// Without this, concurrent spell effects reuse the same local IDs, causing
	// DestroyEmitter/DrainEmitter/SetPosition to target the wrong emitter.
	localToGlobalID map[int32]int32
}

// resolveGlobalID maps a local emitter ID (Fields[0]) to its globally unique ID.
// Returns the local ID unchanged if no mapping exists (fallback for unmapped IDs).
func (d *SpellEventDriver) resolveGlobalID(localID int32) int32 {
	if d.localToGlobalID != nil {
		if gid, ok := d.localToGlobalID[localID]; ok {
			return gid
		}
	}
	return localID
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
		if d.actionNodeID == -1 {
			d.waitEvent = nil
		} else if d.effDriver.system != nil && d.effDriver.system.IsActionComplete != nil {
			if d.effDriver.system.IsActionComplete(d.actorID) {
				d.waitEvent = nil
			}
		} else {
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

// processProjectile updates active projectile position via velocity integration.
// PS2 ProcessProjectileEvent at 0x00478FE8: per-frame velocity-based movement.
// PS2 uses progress accumulation (not timer) and timeScale = min(dist*0.6, 1.0).
// Flight modes: 0=direct, 1=arc/spiral, 2=scatter, 3=tracking.
// Completion is distance-based (arrival at target), not timer-based.
func (d *SpellEventDriver) processProjectile(now time.Time) {
	if d.effDriver.resolvePos == nil {
		return
	}

	// Safety fallback: prevent infinite flight (30s max, well beyond any spell)
	if now.Sub(d.projectileStart).Seconds() > 30.0 {
		d.completeProjectile()
		return
	}

	// Compute dt since last update
	dt := float32(now.Sub(d.projectileLastTick).Seconds())
	d.projectileLastTick = now
	if dt <= 0 || dt > 0.2 {
		dt = 0.033 // clamp to ~30fps
	}

	// Get current target position (target may be moving)
	tx, ty, tz := d.effDriver.resolvePos(d.effDriver.targetObjID)

	// Distance to target
	dx := tx - d.projectilePos[0]
	dy := ty - d.projectilePos[1]
	dz := tz - d.projectilePos[2]
	dist := mathutil.Dist3D(tx, ty, tz, d.projectilePos[0], d.projectilePos[1], d.projectilePos[2])
	if dist < 0.001 {
		d.projectilePos = [3]float32{tx, ty, tz}
		d.completeProjectile()
		return
	}

	// PS2 speed comes from event->Floats[0], NOT computed from distance.
	var speed float32
	if d.projectileEvent != nil {
		speed = d.projectileEvent.Floats[0]
	}
	if speed <= 0 {
		speed = 10.0 // fallback if event has no speed
	}

	// Compute distance traveled from start
	travelDX := d.projectilePos[0] - d.projectileStartPos[0]
	travelDY := d.projectilePos[1] - d.projectileStartPos[1]
	travelDZ := d.projectilePos[2] - d.projectileStartPos[2]
	distTraveled := float32(math.Sqrt(float64(travelDX*travelDX + travelDY*travelDY + travelDZ*travelDZ)))

	// PS2 timeScale = min(distFromSrc * 0.6, 1.0) — accelerates away from source
	// NOT distance-to-target (which would decelerate near target)
	timeScale := distTraveled * 0.6
	if timeScale > 1.0 {
		timeScale = 1.0
	}

	switch d.projectileFlightMode {
	case 0: // Direct — velocity * timeScale toward target
		// PS2 arrival: distFromSrc > distToTarget (overshot midpoint) OR distToTarget < 3.0
		if distTraveled > dist || dist < 3.0 {
			d.projectilePos = [3]float32{tx, ty, tz}
			d.completeProjectile()
			return
		}
		d.projectileVel[0] = (dx / dist) * speed * timeScale
		d.projectileVel[1] = (dy / dist) * speed * timeScale
		d.projectileVel[2] = (dz / dist) * speed * timeScale

	case 1: // Arc/spiral — rotation angle accumulates, arc height grows
		// PS2 arrival: distFromSrc > distToTarget (overshot midpoint) OR distToTarget < 3.0
		if distTraveled > dist || dist < 3.0 {
			d.projectilePos = [3]float32{tx, ty, tz}
			d.completeProjectile()
			return
		}
		rotRate := float32(1.0)
		if d.projectileEvent != nil {
			rotRate = d.projectileEvent.Floats[2]
			if rotRate == 0 {
				rotRate = 1.0
			}
		}
		d.projectileProgress += rotRate * 5.0 * dt
		d.projectileArcHeight += dt * 3.0
		// PS2 at 0x004795C0-0x004795DC: clamp arc height at event Floats[1]
		if d.projectileEvent != nil && d.projectileArcHeight > d.projectileEvent.Floats[1] && d.projectileEvent.Floats[1] > 0 {
			d.projectileArcHeight = d.projectileEvent.Floats[1]
		}

		d.projectileVel[0] = (dx / dist) * speed * timeScale
		d.projectileVel[1] = (dy / dist) * speed * timeScale
		d.projectileVel[2] = (dz / dist) * speed * timeScale

		// Add arc Y component (parabolic arc that peaks mid-flight)
		arcY := d.projectileArcHeight * timeScale * float32(math.Sin(float64(d.projectileProgress)))
		d.projectilePos[1] += arcY * dt

	case 2: // Scatter — settling vibration at target with decay
		// PS2: maintains prevPos/jitterPos pairs, interpolates by progress,
		// jitter amplitude decays by Floats[1] each cycle, progress accumulates at dt*15.0.
		// projectileArcHeight stores current jitter amplitude (initialized to 1.0).
		if d.projectileProgress >= 1.0 {
			// Decay jitter amplitude by Floats[1] each cycle
			decay := d.projectileEvent.Floats[1]
			if decay <= 0 {
				decay = 0.5 // fallback if unset
			}
			d.projectileArcHeight *= decay
			// PS2: completes when amplitude drops below threshold
			if d.projectileArcHeight < 0.01 {
				d.completeProjectile()
				return
			}
			// Random scatter jitter scaled by current amplitude
			// PS2: rand() scaled by 0.2 (XZ) and 0.05 (Y)
			scale := d.projectileArcHeight
			d.projectilePos[0] = tx + (randFloat()*0.4-0.2)*scale
			d.projectilePos[1] = ty + (randFloat()*0.1-0.05)*scale
			d.projectilePos[2] = tz + (randFloat()*0.4-0.2)*scale
			d.projectileProgress = 0 // PS2 resets to 0 each cycle
		}
		d.projectileProgress += dt * 15.0
		goto updateEmitters

	case 3: // Tracking — velocity toward target with parabolic arc
		// PS2 arrival: distFromSrc > distToTarget (overshot midpoint) OR distToTarget < 3.0
		if distTraveled > dist || dist < 3.0 {
			d.projectilePos = [3]float32{tx, ty, tz}
			d.completeProjectile()
			return
		}
		d.projectileProgress += dt * 15.0
		d.projectileVel[0] = (dx / dist) * speed * timeScale
		d.projectileVel[1] = (dy / dist) * speed * timeScale
		d.projectileVel[2] = (dz / dist) * speed * timeScale
		// PS2 parabolic arc: Y offset peaks at midpoint
		if d.projectileInitDist > 0 && speed > 0 {
			arcAngle := (dist / d.projectileInitDist) * math.Pi
			sinArc := float32(math.Sin(float64(arcAngle)))
			invSpeed := 1.0 / speed
			arcY := sinArc * invSpeed * invSpeed * d.projectileInitDist * d.projectileInitDist
			d.projectileVel[1] += arcY * dt
		}
	}

	// Integrate position
	d.projectilePos[0] += d.projectileVel[0] * dt
	d.projectilePos[1] += d.projectileVel[1] * dt
	d.projectilePos[2] += d.projectileVel[2] * dt

updateEmitters:
	// Update source emitter — follows flight path
	if d.projectileSrcEmitter != nil {
		d.projectileSrcEmitter.SetPosition(d.projectilePos[0], d.projectilePos[1], d.projectilePos[2])
	}
	// Update destination emitter — tracks target position
	if d.projectileDstEmitter != nil {
		d.projectileDstEmitter.SetPosition(tx, ty, tz)
	}
}

// completeProjectile marks the projectile as done and cleans up emitters.
func (d *SpellEventDriver) completeProjectile() {
	d.projectileActive = false
	d.projectileSrcEmitter = nil
	d.projectileDstEmitter = nil
	d.projectileEvent = nil
}

// processLightning updates active lightning bolt.
// PS2 ProcessLightningEvent at 0x004789A8: duration from Fields[2] ms.
// When timer expires, PS2 sets charge=1.0 (final flash) then waits for
// charge to decay to 0 via VILightning::Update before removing.
// Charge starts at 1.0 and decays at ~1.5/sec, so the bolt persists
// ~0.67s after the flash before fully fading out.
func (d *SpellEventDriver) processLightning(now time.Time) {
	elapsedMS := now.Sub(d.lightningStart).Milliseconds()
	if int64(d.lightningDurMS) < elapsedMS {
		if !d.lightningFlashed {
			// PS2: SetCharge(1.0) on all bolts for final bright flash
			d.lightningFlashed = true
			d.lightningFlashTime = now
		}
		// Wait for charge decay (~0.67s at 1.5/sec from charge=1.0)
		if now.Sub(d.lightningFlashTime).Milliseconds() > 700 {
			d.lightningActive = false
		}
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
			d.activeEvents = append(d.activeEvents[:i], d.activeEvents[i+1:]...)
			continue
		}

		switch ae.eventType {
		case 3: // Emitter — update position to track actor per-frame
			// PS2 ProcessCreateEmitter: Fields[2] bit 16=caster, bit 17=target override
			// PS2: nodeID = Fields[2] & 0xFFFF for bone attachment
			if d.effDriver.particles != nil {
				actorID := d.actorID
				if ae.event.Fields[2]&0x10000 != 0 {
					actorID = d.effDriver.casterObjID
				}
				if ae.event.Fields[2]&0x20000 != 0 {
					actorID = d.effDriver.targetObjID
				}
				// Use bone-aware position resolution
				ax, ay, az := d.resolveActorPos(actorID, ae.nodeID)

				// PS2 ProcessCreateEmitter emit shape offset (shapes 1-3).
				// PS2 builds a rotation matrix from emitAngle, defines a local offset
				// vector, rotates it, then transforms by actor world matrix.
				// Shape 0 (point): no offset, emitter at actor position.
				var ox, oy, oz float32
				if ae.emitShape >= 1 && ae.emitShape <= 3 {
					ev := ae.event

					// Radius interpolation: sinf(radParam)*0.5+0.5 maps [-1,1] to [0,1]
					t := float32(math.Sin(float64(ae.emitRadParam)))*0.5 + 0.5
					interpR := ev.Floats[0] + (ev.Floats[1]-ev.Floats[0])*t
					extent := ev.Floats[4]

					sinA := float32(math.Sin(float64(ae.emitAngle)))
					cosA := float32(math.Cos(float64(ae.emitAngle)))

					// PS2: BuildRotationMatrix(eulerAngles) × localOffset
					// PS2 euler convention: angles[0]=Y-axis, angles[1]=X-axis, angles[2]=Z-axis
					// Shape 1 stores angle in angles[1] (X-axis/pitch rotation)
					// Shape 2 stores angle in angles[0] (Y-axis/heading rotation)
					// Shape 3 stores angle in angles[2] (Z-axis/roll rotation)
					switch ae.emitShape {
					case 1: // X-axis rotation of (interpR, extent, 0)
						ox = interpR
						oy = extent * cosA
						oz = -extent * sinA
					case 2: // Y-axis rotation of (0, interpR, extent) — horizontal orbit
						ox = -extent * sinA
						oy = interpR
						oz = extent * cosA
					case 3: // Z-axis rotation of (extent, 0, interpR)
						ox = extent * cosA
						oy = -extent * sinA
						oz = interpR
					}

					// Debug: log emit shape state every ~60 frames
					ae.emitDebugCounter++
					if ae.emitDebugCounter%60 == 1 {
						log.Printf("[SPELL]   shape id=%d shape=%d angle=%.2f radParam=%.2f t=%.3f interpR=%.3f extent=%.3f offset=(%.2f,%.2f,%.2f)",
							ae.id, ae.emitShape, ae.emitAngle, ae.emitRadParam, t, interpR, extent, ox, oy, oz)
					}

					// Advance angles per frame
					// PS2 confirmed: Floats[3]=rotation (*2), Floats[2]=radius oscillation
					ae.emitAngle += ev.Floats[3] * 2 * dt
					ae.emitRadParam += ev.Floats[2] * dt
					// Wrap to [-2PI, +2PI]
					twoPi := float32(2 * math.Pi)
					for ae.emitAngle > twoPi {
						ae.emitAngle -= twoPi
					}
					for ae.emitAngle < -twoPi {
						ae.emitAngle += twoPi
					}
					for ae.emitRadParam > twoPi {
						ae.emitRadParam -= twoPi
					}
					for ae.emitRadParam < -twoPi {
						ae.emitRadParam += twoPi
					}
				}

				// PS2 Step 2: Transform offset by actor world matrix.
				// PS2 does TransformPoint(actorWorldMatrix, rotatedOffset, worldPos).
				// We approximate with Y-rotation by actor facing angle, which makes
				// the orbit plane follow the character's orientation.
				if (ox != 0 || oy != 0 || oz != 0) && d.effDriver.resolveFacing != nil {
					facing := d.effDriver.resolveFacing(actorID)
					cosF := float32(math.Cos(float64(facing)))
					sinF := float32(math.Sin(float64(facing)))
					rx := ox*cosF + oz*sinF
					rz := -ox*sinF + oz*cosF
					ox, oz = rx, rz
				}

					for _, em := range d.effDriver.particles.emitters {
					if em.emitterID == ae.id {
						em.SetPosition(ax+ox, ay+oy, az+oz)
						em.SetTrackPos(ax, ay, az) // actor raw pos for local-space tracking
						// PS2: direction matrix updated per-frame from actor transform
						if d.effDriver.resolveFacing != nil {
							em.SetFacing(d.effDriver.resolveFacing(actorID))
						}

						// Apply attractor if set (from SetEmitterAttractor, type 5)
						if ae.attractorStrength > 0 {
							tx, ty, tz := d.effDriver.resolvePos(uint32(ae.attractorTarget))
							em.attractorPos = [3]float32{tx, ty, tz}
							em.attractorStrength = ae.attractorStrength
							em.attractorActive = true
						}
					}
				}
			}
			// PS2: Fields[3] as auto-expire timer (handled by ae.maxLife + expired() check above)
		case 6: // SetEmitterMotif — PS2 does per-frame interpolation over duration.
			// We apply the change instantly (no blender), auto-expire via maxLife.
		case 8: // PlayTargetProjectile emitter variant — PS2 updates per-frame
			// PS2 ProcessCreateEmitter variant for type 8: updates emitter position/motif
			// per frame. Currently handled by the projectile state machine (processProjectile),
			// which updates projectileSrcEmitter/projectileDstEmitter directly.
			// Active event auto-expires via maxLife.
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

	// Map local ID (Fields[0], 1-6 per effect) to a globally unique ID.
	// Without this, concurrent spell effects reuse the same local IDs,
	// causing DestroyEmitter/DrainEmitter/SetPosition to target wrong emitters.
	localID := ev.Fields[0]
	globalID := localID
	if ps != nil {
		globalID = ps.NextGlobalEmitterID()
	}
	if d.localToGlobalID == nil {
		d.localToGlobalID = make(map[int32]int32)
	}
	d.localToGlobalID[localID] = globalID

	ae := &ActiveEvent{
		eventType: ev.EventType,
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

func (d *SpellEventDriver) newSetEmitterMotif(ev *esf.SpellEvent, now time.Time) {
	// PS2 ProcessSetEmitterMotif: per-frame interpolation over Fields[2] duration.
	// We now use a motif blender that lerps particle attributes over the duration.
	var maxLife time.Duration
	if ev.Fields[2] > 0 {
		maxLife = time.Duration(ev.Fields[2]) * time.Millisecond
	}
	globalID := d.resolveGlobalID(ev.Fields[0])
	ae := &ActiveEvent{
		eventType: ev.EventType,
		id:        globalID,
		event:     ev,
		startTime: now,
		maxLife:   maxLife,
	}
	d.activeEvents = append(d.activeEvents, ae)

	// PS2 NewSetEmitterMotif:
	//   Fields[0] = emitter ID (which emitter to modify)
	//   Fields[1] = motif DictID/hash to switch to
	//   Fields[2] = blend duration in ms (0 = instant)
	if d.effDriver.particles != nil && globalID != 0 {
		d.effDriver.particles.StartMotifBlend(globalID, ev.Fields[1], ev.Fields[2])
	}
}

// newSetEmitterAttractor sets attractor parameters on an active emitter.
// PS2 NewSetEmitterAttractor at 0x00476B88:
//   Fields[0] = emitter ID to modify (passed to FindActiveEvent helper)
//   Fields[1] = target actor ID for attraction (stored at ae+0x84)
//   Floats[0] = attraction strength (stored at ae+0x88)
func (d *SpellEventDriver) newSetEmitterAttractor(ev *esf.SpellEvent) {
	id := d.resolveGlobalID(ev.Fields[0])
	for _, ae := range d.activeEvents {
		if ae.eventType == 3 && ae.id == id {
			ae.attractorTarget = ev.Fields[1] // PS2: event+0x08 = Fields[1], NOT Fields[2]
			ae.attractorStrength = ev.Floats[0]
			return
		}
	}
}

// newPlayTargetProjectile creates a projectile from caster to target.
// PS2 NewPlayTargetProjectile at 0x00476CD8:
//   Fields[0] bitmask: bit 0 = Target list (1) for targetActorID,
//                      bit 1 = Area list (2) for secondary targets
//   Fields[1] = PD DictID for projectile trail particles (negative = skip emitter creation)
//   Fields[2] bits 8-11 = emitType/flightMode (SAME bits for both bone selection AND flight):
//     0=center/direct, 1=bone1/arc, 2=midpoint/scatter, 3=both-bones/tracking
//   Fields[2] low byte (bits 0-7): sub-mode within emitType 1 (3=instant, else random init angle)
//   PS2: NO duration in ms — flight is frame-based with distance-based arrival.
func (d *SpellEventDriver) newPlayTargetProjectile(ev *esf.SpellEvent, now time.Time) {
	// Create Target/Area event drivers based on Fields[0] bitmask
	flags := ev.Fields[0]
	if flags&1 != 0 {
		d.effDriver.addEventDriver(1, d.effDriver.targetObjID) // Target list for target
	}
	if flags&2 != 0 {
		d.effDriver.addEventDriver(2, d.effDriver.targetObjID) // Area list (secondary targets)
	}

	d.projectileActive = true
	d.projectileStart = now
	d.projectileLastTick = now
	d.projectileFlightMode = (ev.Fields[2] >> 8) & 0x0F
	d.projectilePDDictID = ev.Fields[1]
	d.projectileEvent = ev
	d.projectileArcHeight = 0

	// Initialize progress per flight mode (PS2 at 0x00476FDC-0x00477034)
	// PS2: flightMode = Fields[2] & 0xFF (low byte, sub-mode within emitType 1)
	lowByte := ev.Fields[2] & 0xFF
	switch d.projectileFlightMode {
	case 0: // Direct — progress not used
		d.projectileProgress = 0
	case 1: // Arc/spiral
		if lowByte == 3 {
			d.projectileProgress = 0 // "instant" — no initial rotation
		} else {
			// PS2: rand() / 5347.0 gives random initial rotation angle (0-6.13 rad)
			d.projectileProgress = randFloat() * 6.13
		}
	case 2: // Scatter — PS2: initialAngle=1.0 (progress), jitterAmplitude=1.0 (ae+0xA0)
		d.projectileProgress = 1.0
		d.projectileArcHeight = 1.0 // PS2: jitterAmplitude must start > 0 for visible scatter
	default: // Mode 3+ — progress not used initially
		d.projectileProgress = 0
	}

	// PS2 NewPlayTargetProjectile: emitType = (Fields[2] >> 8) & 0x0F — SAME bits as flightMode.
	// On PS2, bone selection IS the flight mode value:
	//   0 = GetActorLocation (entity center, no bone)
	//   1 = GetNodeTransform(actorID, 1) — bone 1 (right hand / cast point)
	//   2 = midpoint of bones 0 and 1
	//   3 = both bones 0 and 1, averaged for srcPos
	var startPos [3]float32
	switch d.projectileFlightMode {
	case 0: // Direct — entity center (no bone)
		if d.effDriver.resolvePos != nil {
			sx, sy, sz := d.effDriver.resolvePos(d.effDriver.casterObjID)
			startPos = [3]float32{sx, sy, sz}
		}
	case 1: // Arc/spiral — bone 1 (right hand / cast point)
		sx, sy, sz := d.resolveActorPos(d.actorID, 1)
		startPos = [3]float32{sx, sy, sz}
	case 2: // Scatter — midpoint of bones 0 and 1
		x0, y0, z0 := d.resolveActorPos(d.actorID, 0)
		x1, y1, z1 := d.resolveActorPos(d.actorID, 1)
		startPos = [3]float32{(x0 + x1) / 2, (y0 + y1) / 2, (z0 + z1) / 2}
	default: // 3+ (tracking) — both bones 0 and 1, averaged
		x0, y0, z0 := d.resolveActorPos(d.actorID, 0)
		x1, y1, z1 := d.resolveActorPos(d.actorID, 1)
		startPos = [3]float32{(x0 + x1) / 2, (y0 + y1) / 2, (z0 + z1) / 2}
	}
	d.projectilePos = startPos
	d.projectileStartPos = startPos
	log.Printf("[SPELL]   projectile start: mode=%d pos=(%.2f,%.2f,%.2f) caster=0x%08X",
		d.projectileFlightMode, startPos[0], startPos[1], startPos[2], d.effDriver.casterObjID)

	// Compute initial distance for speed calculation
	d.projectileInitDist = 1.0 // fallback
	if d.effDriver.resolvePos != nil {
		tx, ty, tz := d.effDriver.resolvePos(d.effDriver.targetObjID)
		dx := tx - startPos[0]
		dy := ty - startPos[1]
		dz := tz - startPos[2]
		d.projectileInitDist = float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))
		if d.projectileInitDist < 1.0 {
			d.projectileInitDist = 1.0
		}
	}

	// PS2 creates source emitter always (if PD DictID >= 0).
	// Destination emitter (activeEvent+0x7C) only created for emitType 3 (both-bones mode).
	if d.effDriver.particles != nil && ev.Fields[1] >= 0 {
		var offset [3]float32 // zero offset — PS2 positions from actor transform
		// Generous emitter lifetime (10s) — projectile manages cleanup via completeProjectile
		emLife := 10 * time.Second

		// Source emitter at caster position — use unique temp IDs to avoid cross-contamination
		srcID := d.effDriver.particles.NextTempEmitterID()
		d.projectileSrcEmitter = d.effDriver.particles.CreateEmitter(srcID, ev.Fields[1], startPos, offset, emLife)

		// PS2: Destination emitter only created for emitType/flightMode 3 (both-bones)
		if d.projectileFlightMode == 3 {
			var dstPos [3]float32
			if d.effDriver.resolvePos != nil {
				x, y, z := d.effDriver.resolvePos(d.effDriver.targetObjID)
				dstPos = [3]float32{x, y, z}
			}
			dstID := d.effDriver.particles.NextTempEmitterID()
			d.projectileDstEmitter = d.effDriver.particles.CreateEmitter(dstID, ev.Fields[1], dstPos, offset, emLife)
		}
	}
}

// newPlayTargetInstant creates Target/Area event drivers for instant effects.
// PS2 NewPlayTargetInstant at 0x00476C30:
//   bit 0 = create Target list (index 1) driver for targetActorID
//   bit 1 = create Area list (index 2) driver for each secondary target (we use targetObjID)
func (d *SpellEventDriver) newPlayTargetInstant(ev *esf.SpellEvent) {
	flags := ev.Fields[0]
	if flags&1 != 0 {
		d.effDriver.addEventDriver(1, d.effDriver.targetObjID) // Target list for target
	}
	if flags&2 != 0 {
		d.effDriver.addEventDriver(2, d.effDriver.targetObjID) // Area list (secondary targets)
	}
}

// newCreateSprite creates a sprite active event.
// PS2 NewCreateSprite at 0x004777E0:
//   Fields[1] = sprite DictID, Fields[2] = bit flags (bit 18=loop, bit 19=play-once)
//   Fields[3] = duration in ms (0 = no auto-expire, waits for DestroySprite)
//   PS2 does NOT read Fields[0] here — Go uses it as ID for Destroy matching.
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

// newCreateSound creates a sound active event and starts audio playback.
// PS2 NewCreateSound at 0x00477660:
//   Fields[1] = sound DictID, Fields[2] = bit flags (bit 18=pan, bits 19-20=attenuation)
//   Fields[3] = duration in ms (0 = no auto-expire, waits for DestroySound)
//   Floats[0] = volume/distance parameter
//   PS2 does NOT read Fields[0] here — Go uses it as ID for Destroy matching.
func (d *SpellEventDriver) newCreateSound(ev *esf.SpellEvent, now time.Time) {
	// PS2: Fields[3] as lifetime, 0 = infinite (no auto-expire, waits for DestroySound)
	var maxLife time.Duration
	if ev.Fields[3] > 0 {
		maxLife = time.Duration(ev.Fields[3]) * time.Millisecond
	}
	ae := &ActiveEvent{
		eventType: ev.EventType,
		id:        ev.Fields[0],
		event:     ev,
		startTime: now,
		maxLife:   maxLife,
	}
	d.activeEvents = append(d.activeEvents, ae)

	// Play audio if audio system is available
	if audio := d.effDriver.system.Audio; audio != nil {
		soundDictID := ev.Fields[1]
		if audio.HasSound(soundDictID) {
			// Resolve source actor position for 3D audio
			var pos [3]float32
			if d.effDriver.resolvePos != nil {
				x, y, z := d.effDriver.resolvePos(d.actorID)
				pos = [3]float32{x, y, z}
			}
			// PS2: Floats[0] is the max audible distance (typically 10.0),
			// not a 0-1 volume. Scale to world units for attenuation.
			maxDist := ev.Floats[0]
			if maxDist <= 0 {
				maxDist = 50.0
			}
			maxDist *= 5.0 // scale to world units (10.0 → 50 units)
			audio.Play(soundDictID, ev.Fields[0], pos, 1.0, maxDist)
		}
	}
}

// newPlayTargetLightning creates a lightning bolt from caster to target.
// PS2 NewPlayTargetLightning at 0x004779E0:
//   Fields[0] bitmask: bit 0 = create caster target driver, bit 1 = create target driver
//   Fields[1] low byte = node selector (0-3)
//   Fields[2] = duration in ms
//   Floats[0-2] = lightning color RGB
func (d *SpellEventDriver) newPlayTargetLightning(ev *esf.SpellEvent, now time.Time) {
	// Create Target/Area event drivers based on Fields[0] bitmask
	// PS2: bit 0 = Target list (1) for targetActorID, bit 1 = Area list (2) for secondary targets
	flags := ev.Fields[0]
	if flags&1 != 0 {
		d.effDriver.addEventDriver(1, d.effDriver.targetObjID) // Target list for target
	}
	if flags&2 != 0 {
		d.effDriver.addEventDriver(2, d.effDriver.targetObjID) // Area list (secondary targets)
	}

	d.lightningActive = true
	d.lightningFlashed = false
	d.lightningStart = now
	d.lightningDurMS = ev.Fields[2]
	d.lightningColor = [3]float32{ev.Floats[0], ev.Floats[1], ev.Floats[2]}
	d.lightningMaxBranch = ev.Fields[3]
	if d.lightningMaxBranch > 3 {
		d.lightningMaxBranch = 3
	}
	d.lightningBoneMode = ev.Fields[1] & 0xFF
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

// newDestroyByType destroys the first active event matching the given type and ID.
// PS2 behavior differs by type:
//   - Emitters (type 3): Move to inactiveEvents for deferred drain (particles fade out)
//   - Sprites (type 9): Immediate DestroyActiveEvent (no deferred cleanup)
//   - Sounds (type 11): Immediate DestroyActiveEvent (no deferred cleanup)
func (d *SpellEventDriver) newDestroyByType(ev *esf.SpellEvent, matchType uint32) {
	id := d.resolveGlobalID(ev.Fields[0])
	for i, ae := range d.activeEvents {
		if ae.eventType == matchType && ae.id == id {
			if matchType == 3 {
				// PS2: DestroyEmitter moves to inactiveEvents. ProcessInactiveEvents
				// then calls ProcessCreateEmitter(isActive=false) until particles drain.
				if d.effDriver.particles != nil {
					d.effDriver.particles.DrainEmitter(ae.id)
				}
				d.inactiveEvents = append(d.inactiveEvents, ae)
			}
			if matchType == 11 {
				// Stop audio playback for this sound
				if audio := d.effDriver.system.Audio; audio != nil {
					audio.Stop(ae.id)
				}
			}
			// Remove from active list (emitters go to inactive, sprites/sounds just removed)
			d.activeEvents = append(d.activeEvents[:i], d.activeEvents[i+1:]...)
			return
		}
	}
}

func (d *SpellEventDriver) newSetAction(ev *esf.SpellEvent) {
	// SetAction is a one-shot animation trigger (no ActiveEvent created).
	// PS2 NewSetAction at 0x00476200: calls VICSprite::SetAction(actor, Fields[0], Floats[0]).
	// Fields[0] = action type (maps to cast/action animations on PS2).
	// Only fires if actor's state == 5 (action complete); we skip that check since
	// we don't track actor action state.
	actionType := ev.Fields[0]

	if d.effDriver.system != nil && d.effDriver.system.OnSetAction != nil {
		d.effDriver.system.OnSetAction(d.actorID, actionType)
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
	if sys == nil {
		return
	}

	// Trigger attack animation.
	// PS2 stores SetAttackAction return value as actionNodeID for Wait(type 0).
	if sys.OnPlayAttack != nil {
		sys.OnPlayAttack(d.actorID, animPair, trailColor, trailID)
		d.actionNodeID = 0 // 0 = "action set" (vs -1 = "no action")
	}

	// PS2 SetWeaponTrail: stores trail attachment ID + RGBA color on CSprite.
	// ProcessWeaponTrails renders trail geometry from consecutive bone positions.
	if sys.SetWeaponTrail != nil {
		sys.SetWeaponTrail(d.actorID, trailID, trailColor)
	}

	// PS2: Find and reset "Spell FX 1-4" trigger points on the actor's HSprite.
	// These triggers fire at animation keyframes. Wait(sub-type 3) checks HasTripped
	// on these indices to know when the swing animation has reached its impact point.
	triggerNames := [4]string{"Spell FX 1", "Spell FX 2", "Spell FX 3", "Spell FX 4"}
	for i := 0; i < 4; i++ {
		d.animNodes[i] = -1
		if sys.FindTrigger != nil {
			if idx, ok := sys.FindTrigger(d.actorID, triggerNames[i]); ok {
				d.animNodes[i] = idx
				if sys.ResetTrigger != nil {
					sys.ResetTrigger(d.actorID, idx)
				}
			}
		}
	}
}

func (d *SpellEventDriver) newShakeCamera(ev *esf.SpellEvent) {
	// PS2 NewShakeCamera at 0x00476638:
	//   Fields[0] = flags bitmask: bit 16 = enable shake, bit 17 = enable interpolation
	//   Fields[1] = speed enum (low byte, 0-3): 0=stop, 1=slow, 2=medium, 3=fast
	//   Floats[0] = shake amplitude/magnitude
	//   Floats[1] = target magnitude (for ShakeTerp interpolation end value)
	//   Floats[2] = duration in seconds (for ShakeTerp interpolation)
	flags := ev.Fields[0]
	amplitude := ev.Floats[0]
	targetMag := ev.Floats[1]
	durationSec := ev.Floats[2]

	if d.effDriver.system == nil {
		return
	}

	// PS2 checks bit 16 for shake, bit 17 for interpolation.
	// PS2 also uses Fields[1] low byte as speed enum (0=stop, 1-3=speed).
	// Speed 0 forces amplitude to 0.0 (stop shaking).
	speed := ev.Fields[1] & 0xFF
	if flags&0x10000 != 0 {
		// PS2: speed=0 forces amplitude to 0.0 (stop shake command)
		if speed == 0 {
			amplitude = 0
		}
		if amplitude > 0 {
			d.effDriver.system.ShakeStartTime = time.Now()
			d.effDriver.system.ShakeDuration = time.Duration(durationSec*1000) * time.Millisecond
			if d.effDriver.system.ShakeDuration <= 0 {
				d.effDriver.system.ShakeDuration = 500 * time.Millisecond
			}
			d.effDriver.system.ShakeAmplitude = amplitude
		} else {
			// Stop shaking
			d.effDriver.system.ShakeDuration = 0
			d.effDriver.system.ShakeAmplitude = 0
		}
	}
	if flags&0x20000 != 0 {
		// Interpolation mode: ramp to targetMag over durationSec
		if durationSec <= 0 {
			// Instant snap to target magnitude
			d.effDriver.system.ShakeAmplitude = targetMag
		} else {
			d.effDriver.system.ShakeStartTime = time.Now()
			d.effDriver.system.ShakeDuration = time.Duration(durationSec*1000) * time.Millisecond
			d.effDriver.system.ShakeAmplitude = targetMag
		}
	}
}

// CameraShakeOffset returns the current XY camera offset from active shake effects.
// Returns (0,0) if no shake is active.
func (sys *SpellEffectSystem) CameraShakeOffset() (float32, float32) {
	if sys == nil || sys.ShakeDuration == 0 {
		return 0, 0
	}
	elapsed := time.Since(sys.ShakeStartTime)
	if elapsed >= sys.ShakeDuration {
		sys.ShakeDuration = 0
		return 0, 0
	}
	// Decaying sinusoidal shake
	progress := float32(elapsed.Seconds())
	decay := 1.0 - float32(elapsed)/float32(sys.ShakeDuration)
	amp := sys.ShakeAmplitude * decay
	// Use different frequencies for X and Y to avoid circular motion
	ox := amp * float32(math.Sin(float64(progress)*37.0))
	oy := amp * float32(math.Sin(float64(progress)*53.0))
	return ox, oy
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
func (d *SpellEffectDriver) addEventDriver(listIndex int, actorID uint32) {
	if listIndex < 0 || listIndex > 2 {
		return
	}
	events := d.effect.EventLists[listIndex]
	if len(events) == 0 {
		return
	}
	ed := newSpellEventDriver(d, events, actorID, listIndex)
	d.eventDrivers = append(d.eventDrivers, ed)
}

// trackEmitter records an emitter ID (Fields[0]) for cleanup when this driver finishes.
func (d *SpellEffectDriver) trackEmitter(emitterID int32) {
	d.emitterPDs = append(d.emitterPDs, emitterID)
}

// cleanupEmitters destroys all particle emitters created by this driver.
// Called when the driver finishes or is force-expired.
// PS2: emitters are destroyed with their parent VISpellEffectDriver.
func (d *SpellEffectDriver) cleanupEmitters() {
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

	// Camera shake state (from ShakeCamera event type 17)
	ShakeStartTime time.Time
	ShakeDuration  time.Duration
	ShakeAmplitude float32

	// Audio system for spell sound playback (nil = no audio).
	Audio AudioPlayer

	// PlayAttack callback — visual client sets this to receive animation triggers.
	// Called with (actorObjID, animPairIndex, trailColor, trailID). One-shot.
	// PS2 also calls SetWeaponTrail with color from Floats[0-2] and ID from Fields[1].
	OnPlayAttack func(actorObjID uint32, animPair int32, trailColor [3]float32, trailID int32)

	// SetAction callback — visual client sets this to receive action/cast animation triggers.
	// PS2 NewSetAction calls VICSprite::SetAction(actor, Fields[0], Floats[0]).
	// Fields[0] = action type, Floats[0] = parameter (unused by most actions).
	OnSetAction func(actorObjID uint32, actionType int32)

	// IsActionComplete callback — PS2 Wait sub-type 0 checks actor animation state.
	// Returns true when the actor's play-once action animation has finished.
	IsActionComplete func(actorObjID uint32) bool

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

	// IsCSprite returns true if the actor is a CSprite (skeletal model with equipment).
	// PS2 checks sprite->state == 5. Non-CSprite actors (creatures, SimpleSprites)
	// skip HasTripped in Wait type 3 and clear immediately.
	IsCSprite func(actorObjID uint32) bool

	// SetWeaponTrail callback — visual client sets this to activate weapon swing trails.
	// PS2 SetWeaponTrail at 0x00422C60: stores trail ID and color on the CSprite.
	// ProcessWeaponTrails renders trail geometry from bone positions across frames.
	SetWeaponTrail func(actorObjID uint32, trailID int32, color [3]float32)
}

// NewSpellEffectSystem creates a new spell effect system.
func NewSpellEffectSystem(db *SpellFXDB) *SpellEffectSystem {
	return &SpellEffectSystem{
		db:        db,
		Particles: NewParticleSystem(db),
	}
}

// Play starts a spell effect playback.
// effectID = SpellEffect DictID (animHash from CastingCombat 0x00B1).
// castTimeMS = cast duration in ms (0 for instant).
// casterObjID/targetObjID = entity ObjectIDs.
// startTime = when the cast began (zero = now). Allows backdating triggerTime
// so Wait(sub-type 2) expires relative to actual cast start, not arrival time.
func (sys *SpellEffectSystem) Play(effectID uint32, castTimeMS uint16, casterObjID, targetObjID uint32, startTime time.Time) {
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
		if ae.eventType != 3 {
			continue
		}
		actorID := d.actorID
		if ae.event.Fields[2]&0x10000 != 0 {
			actorID = d.effDriver.casterObjID
		}
		if ae.event.Fields[2]&0x20000 != 0 {
			actorID = d.effDriver.targetObjID
		}
		ax, ay, az := d.resolveActorPos(actorID, ae.nodeID)

		var ox, oy, oz float32
		if ae.emitShape >= 1 && ae.emitShape <= 3 {
			ev := ae.event
			t := float32(math.Sin(float64(ae.emitRadParam)))*0.5 + 0.5
			interpR := ev.Floats[0] + (ev.Floats[1]-ev.Floats[0])*t
			extent := ev.Floats[4]
			sinA := float32(math.Sin(float64(ae.emitAngle)))
			cosA := float32(math.Cos(float64(ae.emitAngle)))
			switch ae.emitShape {
			case 1:
				ox = interpR
				oy = extent * cosA
				oz = -extent * sinA
			case 2:
				ox = -extent * sinA
				oy = interpR
				oz = extent * cosA
			case 3:
				ox = extent * cosA
				oy = -extent * sinA
				oz = interpR
			}
		}

		if (ox != 0 || oy != 0 || oz != 0) && d.effDriver.resolveFacing != nil {
			facing := d.effDriver.resolveFacing(actorID)
			cosF := float32(math.Cos(float64(facing)))
			sinF := float32(math.Sin(float64(facing)))
			rx := ox*cosF + oz*sinF
			rz := -ox*sinF + oz*cosF
			ox, oz = rx, rz
		}

		for _, em := range d.effDriver.particles.emitters {
			if em.emitterID == ae.id {
				em.SetPosition(ax+ox, ay+oy, az+oz)
				em.SetTrackPos(ax, ay, az)
				if d.effDriver.resolveFacing != nil {
					em.SetFacing(d.effDriver.resolveFacing(actorID))
				}
				if ae.attractorStrength > 0 && d.effDriver.resolvePos != nil {
					tx, ty, tz := d.effDriver.resolvePos(uint32(ae.attractorTarget))
					em.attractorPos = [3]float32{tx, ty, tz}
					em.attractorStrength = ae.attractorStrength
					em.attractorActive = true
				}
			}
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
	LightningFlashed   bool  // true after charge flash (timer expired)
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
				// Compute progress from distance (0=at caster, 1=at target)
				var progress float32
				if ed.projectileInitDist > 0.01 && drv.resolvePos != nil {
					tx, ty, tz := drv.resolvePos(drv.targetObjID)
					dx := tx - ed.projectilePos[0]
					dy := ty - ed.projectilePos[1]
					dz := tz - ed.projectilePos[2]
					distToTarget := float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))
					progress = mathutil.ClampF(1.0-distToTarget/ed.projectileInitDist, 0, 1)
				}
				fx := VisualFX{
					Type:        VFXProjectile,
					SourceObjID: drv.casterObjID,
					TargetObjID: drv.targetObjID,
					Progress:    progress,
					Age:         elapsed,
					MaxAge:      10.0, // generous — projectile completes via distance
				}
				// Store current projectile position in Color fields for rendering
				fx.ColorR = ed.projectilePos[0]
				fx.ColorG = ed.projectilePos[1]
				fx.ColorB = ed.projectilePos[2]
				effects = append(effects, fx)
			}

			// Lightning
			if ed.lightningActive {
				durMS := float32(ed.lightningDurMS)
				if durMS <= 0 {
					durMS = 1 // avoid division by zero; PS2 expires immediately
				}
				elapsed := float32(now.Sub(ed.lightningStart).Seconds())
				maxAge := durMS / 1000.0
				effects = append(effects, VisualFX{
					Type:               VFXLightning,
					SourceObjID:        drv.casterObjID,
					TargetObjID:        drv.targetObjID,
					Progress:           mathutil.ClampF(elapsed/maxAge, 0, 1),
					Age:                elapsed,
					MaxAge:             maxAge,
					ColorR:             ed.lightningColor[0],
					ColorG:             ed.lightningColor[1],
					ColorB:             ed.lightningColor[2],
					LightningMaxBranch: ed.lightningMaxBranch,
					LightningBoneMode:  ed.lightningBoneMode,
					LightningDurMS:     ed.lightningDurMS,
					LightningFlashed:   ed.lightningFlashed,
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
