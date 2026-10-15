package game

// Port of VIParticleSystem / VIParticleEmitter from PS2 SUPPORT module.
// See claude-notes/particle-system-re.md for decompiled struct layouts.
//
// Position: analytical physics pos = birthPos + vel*t + 0.5*gravity*t², or the
// friction-damped solution of the same ODE (VIParticleSystem::Render's Euler step
// in the limit)
// Size: startSize + growthRate*t
// Color: gradient[(int)(age/lifespan * 31 * repeat) & 31] (PS2 hard-step)
// Birth: nozzle→outer→inner HPR rotation order (PS2 Matrix33SetHpr)
//
// Rendered as camera-facing billboard quads (textured, alpha-blended).

import (
	"image"
	"log"
	"math"
	"math/rand"
	"time"

	"github.com/eqoa/client/internal/mathutil"
	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
	rl "github.com/gen2brain/raylib-go/raylib"
)

const (
	particleGravity = float32(-5.0) // PS2 default gravity when GravityOn=true

	// maxParticles is the pool VIScene::Init gives VIParticleSystem::Init:
	// an emitter births nothing once all 2,000 are in use.
	maxParticles = 2000
)

// Particle stores birth conditions for analytical position computation.
// Port of PS2 104-byte particle struct.
type Particle struct {
	BirthPos      [3]float32 // world position at birth
	BirthVel      [3]float32 // velocity at birth
	Gravity       float32    // m_gravity: -5.0 with GravityOn, a motif blend in between
	Friction      float32    // velocity dampening (0.0 = none, 0.62 = heavy drag)
	Frame         int32      // sprite-sheet layout (0 = whole texture), from TexConfig
	StartSize      float32 // radius at birth (already halved); may be negative
	GrowthRate     float32 // (endSize-startSize)/lifespan
	GradientRepeat float32 // PS2 particle+0x1C: per-particle copy (set at birth)
	Lifespan       float32 // seconds
	Age            float32 // current age in seconds
	// Attractor Euler accumulator (PS2 modifies velocity per-frame, can't be analytical)
	AttrVel  [3]float32 // accumulated velocity from attractor pulls
	AttrDisp [3]float32 // accumulated displacement from attractor
	alive          bool
}

// ParticleEmitter manages a pool of particles for one CreateEmitter event.
// Port of VIParticleEmitter (SUPPORT module, ~960 bytes).
type ParticleEmitter struct {
	def        *esf.ParticleDefinition
	particles  []Particle
	pos        [3]float32 // emitter world position (entity pos + offset)
	prevPos    [3]float32 // position at the last update: VIParticleEmitter::m_lastLocation
	vel        [3]float32 // emitter velocity, (pos - prevPos) / dt, for InheritVelocity
	offset     [3]float32 // XYZ offset from entity origin (from event Floats[0-2])
	originPos  [3]float32 // emitter position at creation (for local-space particle rendering)
	trackPos   [3]float32 // actor raw position (without shape offset), updated per-frame
	trackOrigin [3]float32 // actor raw position at creation
	birthAccum float32    // fractional birth accumulator
	startTime  time.Time
	emitterID  int32      // spell event emitter ID (Fields[0] from CreateEmitter)
	texID     int32 // ParticleDefinition texture DictID
	blendMode int32 // PS2 BlendMode: 1=additive, 2=subtractive, 3=alpha
	zWrite    bool  // PS2 per-def depth write (true for ~8% of defs)
	zTest     bool  // PS2 per-def depth test (false for ~8% of defs)
	alive     bool
	hasPrevPos bool       // prevPos holds the location at the last update (m_lastLocation)
	maxLife    time.Duration // auto-expire if no explicit destroy
	// The motif blenders linked to this emitter (m_blenders) and the attributes
	// they blend the definition to (m_attributes, rebuilt by BlendMotifs when a
	// factor changes). attrs is valid once attrsInit is set.
	blenders  []*MotifBlender
	attrs     esf.ParticleAttributes
	gravity   float32 // attrs' m_gravity, which blends as a float
	attrsInit bool
	nozzleRot    [3]float32              // HPR rotation applied to nozzle axis (zone emitters only)
	facing       float32                // actor facing angle (radians) — PS2 direction matrix Y-rotation
	isZone     bool       // true for zone-placed emitters (torches, lamps)
	alwaysOn   bool       // true for always-visible emitters (blue flames)
	culled     bool       // true when too far from camera — skip birthing new particles
	// Attractor (set by SpellEvent type 5 — SetEmitterAttractor)
	attractorPos      [3]float32 // world position of attractor target
	attractorStrength float32    // pull strength (0 = disabled)
	attractorActive   bool       // true when attractor has been set
	// Draining: stop birthing new particles but keep emitter alive for existing ones.
	// PS2 ProcessInactiveEvents calls ProcessCreateEmitter(isActive=false) to let
	// particles die naturally after a DestroyEmitter event, rather than killing them.
	draining bool
}

// ParticleSystem manages all active particle emitters.
// Port of VIParticleSystem from SUPPORT module.
type ParticleSystem struct {
	emitters []*ParticleEmitter
	texCache map[int32]rl.Texture2D // DictID → GPU texture
	db       *SpellFXDB
	ZoneLightsOn  bool  // when false, zone emitters (torches/lamps) are suppressed
	nextTempID    int32 // decrements from -1 for projectile/temp emitters (avoids collision with spell event IDs)
	nextGlobalID  int32 // increments from 1000 for globally unique spell emitter IDs
	// PS2 VIParticleSystem: 8-frame rolling average for delta time smoothing
	dtHistory [8]float32
	dtIdx     int
	deltaTime float32 // VIParticleSystem::DeltaTime, the smoothed step of the last Update
}

// NewParticleSystem creates a particle system tied to a SpellFXDB.
// VIParticleSystem::Init seeds the delta-time history with 1/30 s.
func NewParticleSystem(db *SpellFXDB) *ParticleSystem {
	ps := &ParticleSystem{
		db:       db,
		texCache: make(map[int32]rl.Texture2D),
	}
	for i := range ps.dtHistory {
		ps.dtHistory[i] = 1.0 / 30.0
	}
	return ps
}

// NextTempEmitterID returns a unique negative emitter ID for projectile/temp emitters.
// Spell event emitter IDs (Fields[0]) are positive DictID hashes, so negative IDs avoid collision.
func (ps *ParticleSystem) NextTempEmitterID() int32 {
	ps.nextTempID--
	return ps.nextTempID
}

// NextGlobalEmitterID returns a globally unique positive emitter ID for spell emitters.
// Avoids ID collisions between concurrent spell effects that reuse local IDs (1-6).
func (ps *ParticleSystem) NextGlobalEmitterID() int32 {
	ps.nextGlobalID++
	return ps.nextGlobalID
}

// CreateEmitter spawns a new particle emitter at the given position using
// the ParticleDefinition referenced by pdDictID.
// emitterID is the spell event emitter ID (Fields[0] from CreateEmitter event).
// offset is the XYZ offset from entity origin.
func (ps *ParticleSystem) CreateEmitter(emitterID, pdDictID int32, pos [3]float32, offset [3]float32, maxLife time.Duration) *ParticleEmitter {
	if ps == nil || ps.db == nil {
		return nil
	}
	pd := ps.db.LookupParticle(pdDictID)
	if pd == nil {
		return nil
	}

	// Load texture if not cached
	ps.ensureTexture(pd.TexDictID)

	attr := &pd.Base
	log.Printf("[PD] id=%d pd=0x%08X nozzle=[%.2f,%.2f,%.2f] nozzleHPR=[%.2f,%.2f,%.2f] outerOff=[%.2f,%.2f,%.2f] outerHPR=[%.2f,%.2f,%.2f] innerOff=[%.2f,%.2f,%.2f] innerHPR=[%.2f,%.2f,%.2f] vel=%.2f velVar=%.2f friction=%.2f gravity=%v startSz=%.2f endSz=%.2f life=%.2f lifeVar=%.2f rate=%.1f blend=%d",
		emitterID, uint32(pdDictID),
		attr.NozzleAxis[0], attr.NozzleAxis[1], attr.NozzleAxis[2],
		attr.NozzleHprVar[0], attr.NozzleHprVar[1], attr.NozzleHprVar[2],
		attr.OuterOffset[0], attr.OuterOffset[1], attr.OuterOffset[2],
		attr.OuterHprVar[0], attr.OuterHprVar[1], attr.OuterHprVar[2],
		attr.InnerOffset[0], attr.InnerOffset[1], attr.InnerOffset[2],
		attr.InnerHprVar[0], attr.InnerHprVar[1], attr.InnerHprVar[2],
		attr.Velocity, attr.VelocityVar, attr.Friction, attr.GravityOn,
		attr.StartSize, attr.EndSize, attr.Lifespan, attr.LifespanVar,
		attr.Birthrate, pd.BlendMode)

	initPos := [3]float32{pos[0] + offset[0], pos[1] + offset[1], pos[2] + offset[2]}
	em := &ParticleEmitter{
		def:         pd,
		pos:         initPos,
		originPos:   initPos,
		trackPos:    pos, // actor raw position (without offset)
		trackOrigin: pos,
		offset:      offset,
		startTime: time.Now(),
		emitterID: emitterID,
		texID:     pd.TexDictID,
		blendMode: pd.BlendMode,
		zWrite:    pd.ZWrite,
		zTest:     pd.ZTest,
		alive:     true,
		maxLife:   maxLife,
		particles: make([]Particle, 0, 32),
	}
	ps.emitters = append(ps.emitters, em)
	return em
}

// CreateZoneEmitter spawns a permanent particle emitter from a zone-placed ParticleSprite.
// Uses an embedded ParticleDefinition and pre-resolved texture image.
// alwaysOn=true for effects that should render regardless of time of day (blue flames).
func (ps *ParticleSystem) CreateZoneEmitter(pd *esf.ParticleDefinition, pos, rot [3]float32, tex *image.NRGBA, alwaysOn bool) *ParticleEmitter {
	if ps == nil || pd == nil {
		return nil
	}

	// Upload texture if provided and not already cached
	if tex != nil && pd.TexDictID != 0 {
		if _, ok := ps.texCache[pd.TexDictID]; !ok {
			rlImg := rl.NewImageFromImage(tex)
			t := rl.LoadTextureFromImage(rlImg)
			rl.UnloadImage(rlImg)
			rl.SetTextureFilter(t, rl.FilterBilinear)
			rl.SetTextureWrap(t, rl.WrapClamp)
			ps.texCache[pd.TexDictID] = t
		}
	}

	em := &ParticleEmitter{
		def:       pd,
		pos:       pos,
		prevPos:   pos,
		startTime: time.Now(),
		texID:     pd.TexDictID,
		blendMode: pd.BlendMode,
		zWrite:    pd.ZWrite,
		zTest:     pd.ZTest,
		alive:     true,
		isZone:    true,
		alwaysOn:  alwaysOn,
		maxLife:   0, // permanent — no auto-expire
		particles: make([]Particle, 0, 32),
		nozzleRot: rot,
	}
	ps.emitters = append(ps.emitters, em)
	return em
}

// MotifBlender is VIMotifBlender: one motif of an emitter's definition and the
// factor it is blended in at. A SetEmitterMotif event owns one and ramps its
// factor; the emitter's attributes are its definition plus, for every linked
// motif, (motif - definition) * factor, so motifs STACK rather than replace.
type MotifBlender struct {
	motif   *esf.ParticleMotif // nil once the emitter is gone, or if no motif matched
	factor  float32
	changed bool
}

// Factor is VIMotifBlender::Factor.
func (b *MotifBlender) Factor() float32 { return b.factor }

// SetFactor is VIMotifBlender::SetFactor: a new value marks the blender changed,
// which makes the emitter re-blend its attributes on its next update.
func (b *MotifBlender) SetFactor(f float32) {
	if b.factor != f {
		b.factor = f
		b.changed = true
	}
}

// Linked reports whether the blender still has a motif: false once its emitter
// was destroyed (VIParticleEmitter::UnlinkAllMotifBlenders) or when the motif
// was never found, which is how the PS2's SetEmitterMotif event knows to retire.
func (b *MotifBlender) Linked() bool { return b != nil && b.motif != nil }

// LinkMotifBlender is VIParticleEmitter::LinkMotifBlender on the emitter with
// the given ID: the motif whose name hashes to motifHash is linked at factor 0.
// The blender comes back unlinked when there is no such emitter or motif.
func (ps *ParticleSystem) LinkMotifBlender(emitterID, motifHash int32) *MotifBlender {
	b := &MotifBlender{changed: true}
	if ps == nil {
		return b
	}
	for _, em := range ps.emitters {
		if em.emitterID != emitterID || !em.alive || em.def == nil {
			continue
		}
		for i := range em.def.Motifs {
			if viHashResourceID(em.def.Motifs[i].Name) == motifHash {
				b.motif = &em.def.Motifs[i]
				em.blenders = append(em.blenders, b)
				break
			}
		}
		return b
	}
	return b
}

// unlinkBlenders is VIParticleEmitter::UnlinkAllMotifBlenders, run when the
// emitter is destroyed.
func (em *ParticleEmitter) unlinkBlenders() {
	for _, b := range em.blenders {
		b.motif = nil
	}
	em.blenders = nil
}

// viHashResourceID matches PS2 VIHashResourceID (SUPPORT 0x003EC238):
// hash = hash*131 + sign_extend(c) for each byte.
func viHashResourceID(name string) int32 {
	var hash int32
	for i := 0; i < len(name); i++ {
		hash = hash*131 + int32(int8(name[i]))
	}
	return hash
}

// DestroyEmitter removes the first emitter matching the given pdDictID.
func (ps *ParticleSystem) DestroyEmitter(emitterID int32) {
	if ps == nil {
		return
	}
	for i, em := range ps.emitters {
		if em.emitterID == emitterID {
			ps.removeAt(i)
			return
		}
	}
}

// removeAt drops emitter i, which destroys it: its motif blenders unlink.
func (ps *ParticleSystem) removeAt(i int) {
	em := ps.emitters[i]
	em.alive = false
	em.unlinkBlenders()
	ps.emitters = append(ps.emitters[:i], ps.emitters[i+1:]...)
}

// EmitterHasParticles returns true if an emitter with the given emitter ID
// still has live particles (used by processInactive to defer cleanup).
func (ps *ParticleSystem) EmitterHasParticles(emitterID int32) bool {
	if ps == nil {
		return false
	}
	for _, em := range ps.emitters {
		if em.emitterID == emitterID && len(em.particles) > 0 {
			return true
		}
	}
	return false
}

// DrainEmitter stops an emitter from birthing new particles but keeps it alive
// so existing particles can die naturally. Matches PS2 ProcessInactiveEvents
// which calls ProcessCreateEmitter(ae, isActive=false) for destroyed emitters.
func (ps *ParticleSystem) DrainEmitter(emitterID int32) {
	if ps == nil {
		return
	}
	for _, em := range ps.emitters {
		if em.emitterID == emitterID {
			em.draining = true
			return
		}
	}
}

// Update ticks all emitters: births new particles, ages and kills old ones.
// Port of VIParticleEmitter::Update (0x004570E8).
// camX/Y/Z are for the 100-unit purge, which applies only to zone and model
// emitters: VIParticleSprite::Raster and VICSprite::UpdateParticleEmitters purge
// past 100, while a spell's CreateEmitter updates at any distance.
func (ps *ParticleSystem) Update(dt float32, camX, camY, camZ float32) {
	if ps == nil {
		return
	}
	// PS2 VIParticleSystem::Render caps dt to 0.1s to prevent explosion during lag spikes
	if dt > 0.1 {
		dt = 0.1
	}
	// PS2: 8-frame rolling average for smoothed delta time
	ps.dtHistory[ps.dtIdx%8] = dt
	ps.dtIdx++
	var dtSum float32
	for i := 0; i < 8; i++ {
		dtSum += ps.dtHistory[i]
	}
	dt = dtSum * 0.125 // divide by 8
	ps.deltaTime = dt
	now := time.Now()

	// The free pool: VIParticleSystem holds 2,000 particles and an emitter's
	// births stop when they are all in use.
	free := maxParticles
	for _, em := range ps.emitters {
		free -= len(em.particles)
	}

	// PS2 VIParticleSprite::Raster: 100.0f cull distance (Purge kills all particles)
	const emitterCullDistSq = float32(100 * 100)
	// Culling disabled when no camera provided (headless/engine mode)
	doCull := camX != 0 || camY != 0 || camZ != 0

	for i := len(ps.emitters) - 1; i >= 0; i-- {
		em := ps.emitters[i]
		if !em.alive {
			free += len(em.particles)
			ps.removeAt(i)
			continue
		}
		// Auto-expire
		if em.maxLife > 0 && now.Sub(em.startTime) > em.maxLife {
			free += len(em.particles)
			ps.removeAt(i)
			continue
		}
		// Draining emitters (from DestroyEmitter event): remove when all particles dead.
		// PS2 ProcessInactiveEvents keeps destroyed emitters alive until their
		// resource check returns <= 0 (no more live particles).
		if em.draining && len(em.particles) == 0 {
			ps.removeAt(i)
			continue
		}
		// Zone emitters burn day and night: VIParticleSprite::Raster has no
		// time-of-day gate, only the 100-unit purge below. (The old gate hid
		// every torch whose flame was not bluer than red until 19:00.)
		// Distance cull — PS2 Purge() kills all particles when dist > 100.
		if doCull && em.isZone {
			dx := em.pos[0] - camX
			dy := em.pos[1] - camY
			dz := em.pos[2] - camZ
			if dx*dx+dy*dy+dz*dz > emitterCullDistSq {
				em.culled = true
				free += len(em.particles)
				em.particles = em.particles[:0] // PS2: Purge kills all particles
				em.prevPos = em.pos            // SetLocation still ran this frame
				continue
			}
			em.culled = false
		}
		em.update(dt, &free)
	}
}

// getAttr returns the emitter's current attributes: its definition with the
// linked motifs blended in (VIParticleEmitter::m_attributes).
func (em *ParticleEmitter) getAttr() *esf.ParticleAttributes {
	if !em.attrsInit {
		em.blendMotifs()
	}
	return &em.attrs
}

// gravityOf is the m_gravity ParseParticleDefinition stores: SetGravityOn
// writes -5 or 0.
func gravityOf(a *esf.ParticleAttributes) float32 {
	if a.GravityOn {
		return particleGravity
	}
	return 0
}

// blendMotifs is VIParticleEmitter::BlendMotifs + VIParticleDefinitionEx::
// BlendMotifs: when a blender changed, the attributes are rebuilt as the
// definition plus (motif - definition) * factor for each motif in definition
// order, every motif with a nonzero factor adding its own share. A motif's
// factor is that of the last blender linked to it.
func (em *ParticleEmitter) blendMotifs() {
	changed := !em.attrsInit
	for _, b := range em.blenders {
		if b.changed {
			b.changed = false
			changed = true
		}
	}
	if !changed {
		return
	}
	base := &em.def.Base
	em.attrs = *base
	em.gravity = gravityOf(base)
	em.attrsInit = true
	for i := range em.def.Motifs {
		m := &em.def.Motifs[i]
		var f float32
		for _, b := range em.blenders {
			if b.motif == m {
				f = b.factor
			}
		}
		if f == 0 {
			continue
		}
		blendAttrs(&em.attrs, base, &m.Attributes, f)
		em.gravity += (gravityOf(&m.Attributes) - gravityOf(base)) * f
	}
	em.attrs.GravityOn = em.gravity < 0 // VIParticleAttributes::GravityOn
}

// blendAttrs adds one motif's share, out += (m - base) * f, field by field as
// VIParticleDefinitionEx::BlendMotifs does. The "set" flags the PS2 recomputes
// afterwards are the nonzero tests update() already makes.
func blendAttrs(out, base, m *esf.ParticleAttributes, f float32) {
	lerp := func(o *float32, b, v float32) { *o += (v - b) * f }
	lerp(&out.Friction, base.Friction, m.Friction)
	lerp(&out.Birthrate, base.Birthrate, m.Birthrate)
	lerp(&out.BirthrateVar, base.BirthrateVar, m.BirthrateVar)
	lerp(&out.Lifespan, base.Lifespan, m.Lifespan)
	lerp(&out.LifespanVar, base.LifespanVar, m.LifespanVar)
	lerp(&out.Velocity, base.Velocity, m.Velocity)
	lerp(&out.VelocityVar, base.VelocityVar, m.VelocityVar)
	lerp(&out.StartSize, base.StartSize, m.StartSize)
	lerp(&out.StartSizeVar, base.StartSizeVar, m.StartSizeVar)
	lerp(&out.EndSize, base.EndSize, m.EndSize)
	lerp(&out.EndSizeVar, base.EndSizeVar, m.EndSizeVar)
	color := func(o *esf.Color32F, b, v esf.Color32F) {
		lerp(&o.R, b.R, v.R)
		lerp(&o.G, b.G, v.G)
		lerp(&o.B, b.B, v.B)
		lerp(&o.A, b.A, v.A)
	}
	for i := range out.Gradient {
		color(&out.Gradient[i], base.Gradient[i], m.Gradient[i])
	}
	color(&out.StartColorVar, base.StartColorVar, m.StartColorVar)
	color(&out.EndColorVar, base.EndColorVar, m.EndColorVar)
	lerp(&out.InheritVelocity, base.InheritVelocity, m.InheritVelocity)
	lerp(&out.DeltaSpawn, base.DeltaSpawn, m.DeltaSpawn)
	lerp(&out.GradientRepeat, base.GradientRepeat, m.GradientRepeat)
	for j := 0; j < 3; j++ {
		lerp(&out.InnerOffset[j], base.InnerOffset[j], m.InnerOffset[j])
		lerp(&out.InnerHprVar[j], base.InnerHprVar[j], m.InnerHprVar[j])
		lerp(&out.OuterOffset[j], base.OuterOffset[j], m.OuterOffset[j])
		lerp(&out.OuterHprVar[j], base.OuterHprVar[j], m.OuterHprVar[j])
		lerp(&out.NozzleAxis[j], base.NozzleAxis[j], m.NozzleAxis[j])
		lerp(&out.NozzleHprVar[j], base.NozzleHprVar[j], m.NozzleHprVar[j])
	}
}

// texFrame is the sprite-sheet layout a new particle draws with, picked by
// VIParticleEmitter::Update off the definition's texture configuration: 1 is
// the whole texture, 2 a random half, 3 one of three quadrants, 4 one of four.
func texFrame(config int32) int32 {
	switch config {
	case 2:
		return 5 + rand.Int31n(2)
	case 3:
		return 1 + rand.Int31n(3)
	case 4:
		return 1 + rand.Int31n(4)
	}
	return 0
}

// billboardFrameUV is the UV rectangle of each layout in BeginBillboards'
// table (bb->m_frame = frame << 2 indexes its corners): the whole texture, the
// four quadrants (top-left, top-right, bottom-left, bottom-right) and the
// left and right halves.
var billboardFrameUV = [7][4]float32{
	{0, 0, 1, 1},
	{0, 0, 0.5, 0.5},
	{0.5, 0, 1, 0.5},
	{0, 0.5, 0.5, 1},
	{0.5, 0.5, 1, 1},
	{0, 0, 0.5, 1},
	{0.5, 0, 1, 1},
}

// update is one frame of an emitter. Its particles age and die here (the
// half of VIParticleSystem::Render that moves them is Render's analytic
// position), then, unless it is draining or purged, VIParticleEmitter::Update
// births. free is the room left in the 2,000-particle pool.
//
// prevPos is m_lastLocation: the location at the previous update, however
// many SetPositions came between. It used to be overwritten by SetPosition
// itself, which left pos - prevPos zero at every update: DeltaSpawn never
// added a particle and the births were never spread along the path moved.
func (em *ParticleEmitter) update(dt float32, free *int) {
	// Age existing particles and remove dead ones
	for i := len(em.particles) - 1; i >= 0; i-- {
		p := &em.particles[i]
		p.Age += dt
		if p.Age >= p.Lifespan {
			em.particles[i] = em.particles[len(em.particles)-1]
			em.particles = em.particles[:len(em.particles)-1]
			*free++
		}
	}

	// SetLocation's first call makes the last location the location.
	if !em.hasPrevPos {
		em.prevPos = em.pos
		em.hasPrevPos = true
	}

	// PS2: a draining emitter (ProcessCreateEmitter with active=false) is not
	// updated: no births, and its attributes stay as they were.
	if em.culled || em.draining {
		em.prevPos = em.pos
		return
	}
	em.blendMotifs()
	attr := &em.attrs

	// n = (birthrate + VIRAND·var)·dt + |moved|·DeltaSpawn + last frame's
	// fraction. Nothing is clamped: a negative n is carried as a debt, and a
	// motif with no birthrate still births from movement alone (a trail).
	move := vsub(em.pos, em.prevPos)
	n := (attr.Birthrate + triangleRand()*attr.BirthrateVar) * dt
	if attr.DeltaSpawn != 0 {
		n += vlen(move) * attr.DeltaSpawn
	}
	n += em.birthAccum
	births := int(n)
	em.birthAccum = n - float32(births)

	// The emitter's velocity for InheritVelocity: (location - lastLocation)
	// times 1/DeltaTime.
	if dt > 0 {
		em.vel = vscale(move, 1/dt)
	}

	// The births are spread along the path: lastLocation, then a step of
	// (location - lastLocation)/count after each.
	spawnBaseX, spawnBaseY, spawnBaseZ := em.prevPos[0], em.prevPos[1], em.prevPos[2]
	var spawnStepX, spawnStepY, spawnStepZ float32
	if births > 0 {
		step := vscale(move, 1/float32(births))
		spawnStepX, spawnStepY, spawnStepZ = step[0], step[1], step[2]
	}
	em.prevPos = em.pos

	for b := 0; b < births && *free > 0; b++ {
		*free--
		p := Particle{alive: true}

		// PS2 VIParticleEmitter::Update: position offset and nozzle direction.
		// Order: nozzle HPR → outer HPR → inner HPR → world-space transform.
		// The axis is unit length as parsed (SetNozzleAxis normalizes); a
		// motif blend can shorten it, and that scales the velocity.
		nozzleDir := [3]float32{attr.NozzleAxis[0], attr.NozzleAxis[1], attr.NozzleAxis[2]}
		posOffset := [3]float32{0, 0, 0}

		// Step 1: Nozzle HPR variance (rotates nozzle direction only)
		nh := attr.NozzleHprVar
		if nh[0] != 0 || nh[1] != 0 || nh[2] != 0 {
			nozzleDir = rotateHPR(
				nh[0]*triangleRand(), nh[1]*triangleRand(), nh[2]*triangleRand(),
				nozzleDir)
		}

		// Step 2: Outer HPR variance (rotates outerOffset → posOffset AND nozzle)
		// PS2: enabled when outerOffset OR outerHprVar is nonzero.
		// This creates ring/sphere distributions via HPR rotation of outerOffset.
		oo := attr.OuterOffset
		oh := attr.OuterHprVar
		if oo[0] != 0 || oo[1] != 0 || oo[2] != 0 || oh[0] != 0 || oh[1] != 0 || oh[2] != 0 {
			h := oh[0] * triangleRand()
			p2 := oh[1] * triangleRand()
			r := oh[2] * triangleRand()
			posOffset = rotateHPR(h, p2, r, oo)
			nozzleDir = rotateHPR(h, p2, r, nozzleDir)
		}

		// Step 3: Inner HPR variance (rotates accumulated posOffset+innerOffset AND nozzle)
		// PS2: enabled when innerOffset OR innerHprVar is nonzero.
		io := attr.InnerOffset
		ih := attr.InnerHprVar
		if io[0] != 0 || io[1] != 0 || io[2] != 0 || ih[0] != 0 || ih[1] != 0 || ih[2] != 0 {
			combined := [3]float32{
				posOffset[0] + io[0],
				posOffset[1] + io[1],
				posOffset[2] + io[2],
			}
			h := ih[0] * triangleRand()
			p2 := ih[1] * triangleRand()
			r := ih[2] * triangleRand()
			posOffset = rotateHPR(h, p2, r, combined)
			nozzleDir = rotateHPR(h, p2, r, nozzleDir)
		}

		// Step 4: World-space transform by emitter direction matrix.
		// PS2: ALWAYS applies this->direction (3x3 at emitter+0x28) to BOTH
		// nozzleDir and posOffset. For zone emitters this is the GroupSprite
		// composite HPR. For spell emitters this is actor facing (Y-rotation).
		if em.nozzleRot[0] != 0 || em.nozzleRot[1] != 0 || em.nozzleRot[2] != 0 {
			nozzleDir = rotateHPR(em.nozzleRot[0], em.nozzleRot[1], em.nozzleRot[2], nozzleDir)
			posOffset = rotateHPR(em.nozzleRot[0], em.nozzleRot[1], em.nozzleRot[2], posOffset)
		} else if em.facing != 0 {
			// Spell emitter: apply actor facing as Y-rotation to both vectors.
			cosF := float32(math.Cos(float64(em.facing)))
			sinF := float32(math.Sin(float64(em.facing)))
			// Y-rotation: [cosF 0 sinF; 0 1 0; -sinF 0 cosF]
			nx := nozzleDir[0]*cosF + nozzleDir[2]*sinF
			nz := -nozzleDir[0]*sinF + nozzleDir[2]*cosF
			nozzleDir[0], nozzleDir[2] = nx, nz
			px := posOffset[0]*cosF + posOffset[2]*sinF
			pz := -posOffset[0]*sinF + posOffset[2]*cosF
			posOffset[0], posOffset[2] = px, pz
		}

		p.BirthPos = [3]float32{
			spawnBaseX + posOffset[0],
			spawnBaseY + posOffset[1],
			spawnBaseZ + posOffset[2],
		}

		// Velocity: nozzle direction (already rotated) scaled by speed with variance
		vel := attr.Velocity + attr.VelocityVar*triangleRand()
		p.BirthVel[0] = nozzleDir[0] * vel
		p.BirthVel[1] = nozzleDir[1] * vel
		p.BirthVel[2] = nozzleDir[2] * vel

		// InheritVelocity: add emitter velocity scaled by attribute
		if attr.InheritVelocity != 0 {
			p.BirthVel[0] += em.vel[0] * attr.InheritVelocity
			p.BirthVel[1] += em.vel[1] * attr.InheritVelocity
			p.BirthVel[2] += em.vel[2] * attr.InheritVelocity
		}

		// Lifespan with variance (triangular)
		// PS2 clamps to [0, 1000] seconds
		ls := attr.Lifespan + attr.LifespanVar*triangleRand()
		if ls < 0.01 {
			ls = 0.01
		}
		if ls > 1000 {
			ls = 1000
		}
		p.Lifespan = ls

		// Friction (velocity dampening per second)
		p.Friction = attr.Friction

		// Size (halved = radius, matching PS2). Not clamped: a negative size
		// is a billboard with both axes flipped, the same size drawn upside down.
		ss := (attr.StartSize + attr.StartSizeVar*triangleRand()) * 0.5
		es := (attr.EndSize + attr.EndSizeVar*triangleRand()) * 0.5
		p.StartSize = ss
		p.GrowthRate = (es - ss) / p.Lifespan

		// PS2 stores gradientRepeat per-particle at birth (particle+0x1C)
		p.GradientRepeat = attr.GradientRepeat

		// Gravity: m_gravity, -5 or 0 unless a motif blend sits between
		p.Gravity = em.gravity
		p.Frame = texFrame(em.def.TexConfig)

		// PS2: StartColorVar/EndColorVar are used only in motif blending
		// (VIMotifBlender), NOT per-particle. No per-particle color variance.

		em.particles = append(em.particles, p)

		// PS2: advance spawn position AFTER use (for next particle)
		spawnBaseX += spawnStepX
		spawnBaseY += spawnStepY
		spawnBaseZ += spawnStepZ
	}
}

// SetPosition is VIParticleEmitter::SetLocation (the stored offset applied).
// The last location is NOT taken here but at each update, so the distance an
// update sees is the whole move since the previous one even when the spell
// driver and the render loop both place the emitter in one frame. The first
// location is also the last, as SetLocation's first call makes it.
func (em *ParticleEmitter) SetPosition(x, y, z float32) {
	if em == nil {
		return
	}
	em.pos = [3]float32{x + em.offset[0], y + em.offset[1], z + em.offset[2]}
	if !em.hasPrevPos {
		em.prevPos = em.pos
		em.hasPrevPos = true
	}
}

// SetFacing updates the emitter's direction angle (actor facing).
// PS2: emitter direction matrix (this+0x28) is set from actor world transform.
// We simplify to Y-rotation only since EQOA actors rotate around Y axis.
func (em *ParticleEmitter) SetFacing(facing float32) {
	if em == nil {
		return
	}
	em.facing = facing
}

// SetTrackPos updates the actor's raw position (without emit shape offset).
// Used for emitter-local rendering: particles follow actor movement but not
// shape offset drift.
func (em *ParticleEmitter) SetTrackPos(x, y, z float32) {
	if em == nil {
		return
	}
	em.trackPos = [3]float32{x, y, z}
}

// Render draws all active particles as camera-facing billboard quads.
// Port of VIParticleSprite::Raster (0x00454FB0) — billboard rendering path.
//
// PS2 per-definition rendering flags (from SPELLFX.ESF survey, 332 defs):
//   BlendMode 1 (additive): 317  BlendMode 3 (alpha): 14  BlendMode 2: 1
//   ZWrite=true: 27, all of them additive with ZTest=false
//
// The blend is the material layer's GS ALPHA (VIMaterial::ConstructTextPass):
// 1 = Cs·As + Cd (0x48), 2 = Cd − Cs·As (0x42), 3 = (Cs − Cd)·As + Cd (0x44),
// 4 = Cd·As (0x89). A blended layer's TEST is ATST=NEVER with AFAIL=RGB_ONLY,
// so it never writes Z whatever ZWrite says: only blend 0 can.
func (ps *ParticleSystem) Render(camera rl.Camera3D) {
	if ps == nil {
		return
	}

	// Camera right and up vectors for billboard computation
	camRight, camUp := billboardVectors(camera)

	// The attractor pull is integrated on the particle system's own step.
	frameDT := ps.deltaTime

	for _, em := range ps.emitters {
		if !em.alive || em.culled || len(em.particles) == 0 {
			continue
		}

		// Per-definition depth mask (PS2 VIParticleDefinition::ZWrite), which
		// only an unblended material honours.
		if em.zWrite && em.blendMode == 0 {
			rl.EnableDepthMask()
		} else {
			rl.DisableDepthMask()
		}
		// Per-definition depth test (PS2 VIParticleDefinition::ZTest)
		if !em.zTest {
			rl.DisableDepthTest()
		}

		// Per-definition blend mode (PS2 VIParticleDefinition::BlendMode)
		switch em.blendMode {
		case 1:
			rl.BeginBlendMode(rl.BlendAdditive)
		case 2:
			// Cd − Cs·As. raylib's BlendSubtractColors is Cs − Cd.
			rl.SetBlendFactors(rl.SrcAlpha, rl.One, rl.FuncReverseSubtract)
			rl.BeginBlendMode(rl.BlendCustom)
		case 4:
			rl.SetBlendFactors(rl.Zero, rl.SrcAlpha, rl.FuncAdd)
			rl.BeginBlendMode(rl.BlendCustom)
		default: // 3; 0 is opaque with an alpha test on the PS2, approximated
			rl.BeginBlendMode(rl.BlendAlpha)
		}

		tex, hasTex := ps.texCache[em.texID]
		attr := em.getAttr()

		// Bind texture and begin batch for all particles in this emitter.
		if hasTex && tex.ID > 0 {
			rl.SetTexture(tex.ID)
		}
		rl.Begin(rl.Quads)

		for pi := range em.particles {
			p := &em.particles[pi]
			if p.Age >= p.Lifespan {
				continue
			}

			t := p.Age
			px, py, pz := p.analyticPos()

			// Attractor: PS2 Render() applies per-frame: vel += normalize(dir)*strength*dt
			// Direction is normalized, so pull magnitude is constant regardless of distance.
			// Since analytical physics can't integrate a changing-direction force, we
			// Euler-accumulate attractor velocity/displacement per-particle each frame.
			if em.attractorActive {
				// Current position including prior attractor displacement
				apx := px + p.AttrDisp[0]
				apy := py + p.AttrDisp[1]
				apz := pz + p.AttrDisp[2]
				dx := em.attractorPos[0] - apx
				dy := em.attractorPos[1] - apy
				dz := em.attractorPos[2] - apz
				dist := float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))
				// The PS2 steps pos += vel·dt, then vel *= 1 − f·dt, then
				// adds the pull; the pull's share of vel is damped too.
				p.AttrDisp[0] += p.AttrVel[0] * frameDT
				p.AttrDisp[1] += p.AttrVel[1] * frameDT
				p.AttrDisp[2] += p.AttrVel[2] * frameDT
				damp := 1 - p.Friction*frameDT
				p.AttrVel[0] *= damp
				p.AttrVel[1] *= damp
				p.AttrVel[2] *= damp
				if dist > 0.01 {
					// Normalize direction (PS2 calls VIVect3::Normalize)
					invDist := 1.0 / dist
					nx := dx * invDist
					ny := dy * invDist
					nz := dz * invDist
					// vel += normalize(dir) * strength * dt
					pull := em.attractorStrength * frameDT
					p.AttrVel[0] += nx * pull
					p.AttrVel[1] += ny * pull
					p.AttrVel[2] += nz * pull
				}
				px += p.AttrDisp[0]
				py += p.AttrDisp[1]
				pz += p.AttrDisp[2]
			}

			// No render-time delta for spell emitters. PS2 uses pure world-space
			// particles: born at emitter pos, rendered at birth pos + velocity*age.
			// Old particles stay at their birth location; only new ones track the
			// emitter. Combined with short lifespans and high birthrate, this
			// creates the effect of particles "following" the entity.

			// Size. A negative one flips both billboard axes: the quad is the
			// same size, its texture turned half a turn.
			size := p.StartSize + p.GrowthRate*t
			if size == 0 {
				continue
			}

			// Color from 32-entry gradient (PS2: hard-step, no interpolation)
			// PS2 VIParticleSystem::Render: index = (int)(age * 31.0 * gradientRepeat) & 31
			// gradientRepeat is per-particle (stored at birth), gradient colors from emitter.
			progress := p.Age / p.Lifespan
			gradIdx := int(progress*31.0*p.GradientRepeat) & 31
			c := attr.Gradient[gradIdx]
			cr := c.R
			cg := c.G
			cb := c.B
			ca := c.A

			clr := rl.Color{
				R: uint8(mathutil.ClampF(cr*255, 0, 255)),
				G: uint8(mathutil.ClampF(cg*255, 0, 255)),
				B: uint8(mathutil.ClampF(cb*255, 0, 255)),
				A: uint8(mathutil.ClampF(ca*255, 0, 255)),
			}

			// Skip particles that cannot change the frame. A multiply (4) at
			// alpha 0 blacks the frame out, so it is always drawn.
			switch em.blendMode {
			case 1:
				if (clr.R == 0 && clr.G == 0 && clr.B == 0) || clr.A == 0 {
					continue
				}
			case 4:
			default:
				if clr.A == 0 {
					continue
				}
			}
			uv := billboardFrameUV[0]
			if p.Frame > 0 && int(p.Frame) < len(billboardFrameUV) {
				uv = billboardFrameUV[p.Frame]
			}
			u0, v0, u1, v1 := uv[0], uv[1], uv[2], uv[3]

			// Billboard quad corners
			cx, cy, cz := px, py, pz
			rx := camRight.X * size
			ry := camRight.Y * size
			rz := camRight.Z * size
			ux := camUp.X * size
			uy := camUp.Y * size
			uz := camUp.Z * size

			rl.Color4ub(clr.R, clr.G, clr.B, clr.A)
			if hasTex && tex.ID > 0 {
				rl.TexCoord2f(u0, v1)
				rl.Vertex3f(cx-rx-ux, cy-ry-uy, cz-rz-uz)
				rl.TexCoord2f(u1, v1)
				rl.Vertex3f(cx+rx-ux, cy+ry-uy, cz+rz-uz)
				rl.TexCoord2f(u1, v0)
				rl.Vertex3f(cx+rx+ux, cy+ry+uy, cz+rz+uz)
				rl.TexCoord2f(u0, v0)
				rl.Vertex3f(cx-rx+ux, cy-ry+uy, cz-rz+uz)
			} else {
				rl.Vertex3f(cx-rx-ux, cy-ry-uy, cz-rz-uz)
				rl.Vertex3f(cx+rx-ux, cy+ry-uy, cz+rz-uz)
				rl.Vertex3f(cx+rx+ux, cy+ry+uy, cz+rz+uz)
				rl.Vertex3f(cx-rx+ux, cy-ry+uy, cz-rz+uz)
			}
		}

		rl.End()
		if hasTex && tex.ID > 0 {
			rl.SetTexture(0)
		}
		rl.EndBlendMode()

		// Restore depth state after each emitter
		if !em.zTest {
			rl.EnableDepthTest()
		}
	}
	// Restore depth mask after all emitters
	rl.EnableDepthMask()
}

// ActiveCount returns the total number of live particles across all emitters.
func (ps *ParticleSystem) ActiveCount() int {
	if ps == nil {
		return 0
	}
	n := 0
	for _, em := range ps.emitters {
		n += len(em.particles)
	}
	return n
}

// EmitterCount returns the number of active emitters.
func (ps *ParticleSystem) EmitterCount() int {
	if ps == nil {
		return 0
	}
	return len(ps.emitters)
}

// ensureTexture loads a Surface texture from SPELLFX.ESF into GPU if not cached.
func (ps *ParticleSystem) ensureTexture(dictID int32) {
	if dictID == 0 {
		return
	}
	if _, ok := ps.texCache[dictID]; ok {
		return
	}

	f := ps.db.File()
	if f == nil {
		return
	}

	obj, err := f.FindObject(dictID)
	if err != nil || obj == nil {
		return
	}
	surf, ok := obj.(*esf.Surface)
	if !ok || surf == nil || surf.Image == nil {
		return
	}

	// Alpha is uploaded unchanged: the palette holds 0..255, and the PS2's
	// billboard pass sends a blended particle's vertex alpha as 64 (half).
	rlImg := rl.NewImageFromImage(surf.Image)
	tex := rl.LoadTextureFromImage(rlImg)
	rl.UnloadImage(rlImg)

	// Set texture filtering and wrapping
	rl.SetTextureFilter(tex, rl.FilterBilinear)
	rl.SetTextureWrap(tex, rl.WrapClamp)

	ps.texCache[dictID] = tex
}

// analyticPos is where VIParticleSystem::Render's Euler step puts a particle,
// solved exactly: pos += vel·dt, vel *= 1 − f·dt, vel.y += g·dt is, as dt → 0,
// dv/dt = −f·v + g. Without friction that is p₀ + v₀t + ½gt²; with it the
// velocity relaxes to the terminal g/f:
//   p(t) = p₀ + (v₀ − g/f)·(1 − e^(−ft))/f + (g/f)·t   (g on y only)
// The friction branch had g's sign flipped, so with friction on, gravity LIFTED
// the particle toward −g/f (21 attribute sets in SPELLFX have both).
func (p *Particle) analyticPos() (px, py, pz float32) {
	t := p.Age
	if p.Friction > 0.001 {
		expDecay := float32(math.Exp(float64(-p.Friction * t)))
		invF := 1.0 / p.Friction
		displace := (1.0 - expDecay) * invF // (1 − e^(−ft))/f
		gOverF := p.Gravity * invF
		px = p.BirthPos[0] + p.BirthVel[0]*displace
		py = p.BirthPos[1] + (p.BirthVel[1]-gOverF)*displace + gOverF*t
		pz = p.BirthPos[2] + p.BirthVel[2]*displace
		return
	}
	px = p.BirthPos[0] + p.BirthVel[0]*t
	py = p.BirthPos[1] + p.BirthVel[1]*t + 0.5*p.Gravity*t*t
	pz = p.BirthPos[2] + p.BirthVel[2]*t
	return
}

// billboardVectors computes camera right and up vectors for billboard rendering.
func billboardVectors(cam rl.Camera3D) (right, up rl.Vector3) {
	// Forward = normalize(target - position)
	fwd := rl.Vector3{
		X: cam.Target.X - cam.Position.X,
		Y: cam.Target.Y - cam.Position.Y,
		Z: cam.Target.Z - cam.Position.Z,
	}
	fLen := float32(math.Sqrt(float64(fwd.X*fwd.X + fwd.Y*fwd.Y + fwd.Z*fwd.Z)))
	if fLen > 0.0001 {
		fwd.X /= fLen
		fwd.Y /= fLen
		fwd.Z /= fLen
	}

	// Right = normalize(cross(forward, up))
	worldUp := cam.Up
	right = rl.Vector3{
		X: fwd.Y*worldUp.Z - fwd.Z*worldUp.Y,
		Y: fwd.Z*worldUp.X - fwd.X*worldUp.Z,
		Z: fwd.X*worldUp.Y - fwd.Y*worldUp.X,
	}
	rLen := float32(math.Sqrt(float64(right.X*right.X + right.Y*right.Y + right.Z*right.Z)))
	if rLen > 0.0001 {
		right.X /= rLen
		right.Y /= rLen
		right.Z /= rLen
	}

	// Up = cross(right, forward)
	up = rl.Vector3{
		X: right.Y*fwd.Z - right.Z*fwd.Y,
		Y: right.Z*fwd.X - right.X*fwd.Z,
		Z: right.X*fwd.Y - right.Y*fwd.X,
	}
	return
}

// rotateHPR applies a heading-pitch-roll rotation to a 3D vector.
// PS2 VIMatrix33::SetHPR: M = Ry(heading) * Rx(pitch) * Rz(roll).
// Used by VIParticleEmitter::Update for nozzle/outer/inner HPR variance.
func rotateHPR(h, p, r float32, v [3]float32) [3]float32 {
	sinH := float32(math.Sin(float64(h)))
	cosH := float32(math.Cos(float64(h)))
	sinP := float32(math.Sin(float64(p)))
	cosP := float32(math.Cos(float64(p)))
	sinR := float32(math.Sin(float64(r)))
	cosR := float32(math.Cos(float64(r)))

	x, y, z := v[0], v[1], v[2]
	return [3]float32{
		(cosH*cosR+sinH*sinP*sinR)*x + (-cosH*sinR+sinH*sinP*cosR)*y + sinH*cosP*z,
		cosP*sinR*x + cosP*cosR*y + (-sinP)*z,
		(-sinH*cosR+cosH*sinP*sinR)*x + (sinH*sinR+cosH*sinP*cosR)*y + cosH*cosP*z,
	}
}

// CreateModelEmitter spawns a particle emitter from a CSprite bone-attached
// PartEmitter. Unlike CreateEmitter (which looks up PD from SpellFXDB), this
// takes a pre-parsed ParticleDefinition and texture image directly — needed
// because model-attached PDs live in CHAR.ESF, not SPELLFX.ESF.
// PS2: CreateParticleEmitters at 0x00423A68.
func (ps *ParticleSystem) CreateModelEmitter(pd *esf.ParticleDefinition, pos [3]float32, tex *image.NRGBA) *ParticleEmitter {
	if ps == nil || pd == nil {
		return nil
	}

	// Upload texture if provided and not already cached
	if tex != nil && pd.TexDictID != 0 {
		if _, ok := ps.texCache[pd.TexDictID]; !ok {
			rlImg := rl.NewImageFromImage(tex)
			t := rl.LoadTextureFromImage(rlImg)
			rl.UnloadImage(rlImg)
			rl.SetTextureFilter(t, rl.FilterBilinear)
			rl.SetTextureWrap(t, rl.WrapClamp)
			ps.texCache[pd.TexDictID] = t
		}
	}

	attr := &pd.Base
	log.Printf("[MODEL-PD] pd=0x%08X tex=0x%08X blend=%d pos=[%.1f,%.1f,%.1f] rate=%.1f life=%.2f sz=%.3f→%.3f vel=%.2f texLoaded=%v",
		uint32(pd.DictID), uint32(pd.TexDictID), pd.BlendMode,
		pos[0], pos[1], pos[2],
		attr.Birthrate, attr.Lifespan, attr.StartSize, attr.EndSize, attr.Velocity,
		tex != nil)

	em := &ParticleEmitter{
		def:       pd,
		pos:       pos,
		prevPos:   pos,
		startTime: time.Now(),
		emitterID: ps.NextTempEmitterID(),
		texID:     pd.TexDictID,
		blendMode: pd.BlendMode,
		zWrite:    pd.ZWrite,
		zTest:     pd.ZTest,
		alive:     true,
		isZone:    true, // world-space particles — position tracked externally via SetPosition
		alwaysOn:  true, // model/weapon emitters not affected by day/night light toggle
		maxLife:   0,    // permanent — tracked by bone/weapon emitter system
		particles: make([]Particle, 0, 32),
	}
	ps.emitters = append(ps.emitters, em)
	return em
}

// DestroyEmitterByPtr kills an emitter by pointer reference.
// Used by bone emitter cleanup when entities disappear.
func (ps *ParticleSystem) DestroyEmitterByPtr(em *ParticleEmitter) {
	if ps == nil || em == nil {
		return
	}
	em.alive = false
	em.draining = true
}

// triangleRand returns a triangularly-distributed random value in [-1, +1).
// Matches PS2: (rand() - rand()) / RAND_MAX.
func triangleRand() float32 {
	return rand.Float32() - rand.Float32()
}
