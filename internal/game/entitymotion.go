package game

import "math"

// entitymotion.go — how CLIENT moves every entity but the player.
//
// Read off the CLIENT decomp (decomp/modules/client/src/viclient.cpp) and,
// where it is not written, the disassembly:
//
//   - An update (ChannelEntity 0x00BD41B8 [A], VICreatureEntity's reader
//     0x00CC2DB0 and its tail 0x00CC2340 [A]) sets the entity's OWN position
//     and HPR from the wire; a creature not walking (byte 141 clear) is then
//     set down on the floor (0x00CBCE40 [A]). The scene actor — what is drawn
//     — stays where it was, and the difference is kept as an error, with an
//     end time 200 ticks (2 × gEntityUpdateRate) on. A creature whose
//     velocity did not change keeps no position error.
//   - Every frame, UpdateCreatures (0x00BC72C0 [A]) steps each creature by
//     its velocity over the ticks since its last step and sets it down on the
//     floor again (VICreatureEntity::Walk 0x00CC3938 → 0x00CBCD10 [A], the
//     "VIAIExtrapolateFilter" call of npc-extrapolation-limiter-20260816.md),
//     and Extrapolate (0x00BC7478 [V]) draws each entity at its position plus
//     the error × t, t falling from 1 at the update to 0 200 ticks later. So
//     a correction is blended out linearly over 203 ms instead of snapped.
//
// The player's own entity is skipped by both (Go keeps it apart, as Player).
// The VIWalkControl a walking creature (byte 141 set) is driven by is not
// ported: no server sends it (0 in every creature record of the 11 captures),
// so such a creature is stepped like any other.

const (
	// entityUpdateRate is gEntityUpdateRate (0x00CD7AE8): 100, never written.
	entityUpdateRate = 100
	// entityBlendTicks is how long Extrapolate blends an update's error out.
	entityBlendTicks = 2 * entityUpdateRate

	// The creature's floor probe (0x00CBCE40): a sphere of radius 0.1 swept
	// from 1.5 above to 1.5 below the position, which a hit puts on the
	// floor (the sphere's centre less its radius). Characters are not in its
	// way (VIAIExtrapolateFilter rejects every VICSprite actor).
	creatureFloorReach  = 1.5
	creatureFloorRadius = 0.1

	// headingScale is the entity reader's s8 → radians, 2π/256 (0x3CC90FDB).
	headingScale = float32(2 * math.Pi / 256)
	// creatureVelScale is the velocity bytes' scale, 21/128 (func_00CC2890).
	creatureVelScale = float32(21.0 / 128.0)
)

// EntityMotion is what CLIENT keeps of one entity's motion.
type EntityMotion struct {
	Pos      [3]float32 // VIEntity +0x40 (GetPos): the entity's own position
	Hpr      [3]float32 // VIEntity +0x00 (GetHpr): heading, pitch, roll
	Vel      [3]float32 // VICreatureEntity +0x218, units per tick-second
	Err      [3]float32 // m_entVel[ch] (+0x71FC): the drawn position less Pos at the update
	ErrRot   [3]float32 // m_entRot[ch] (+0x731C): the same for the HPR, each wrapped to [−π, π)
	Until    int64      // m_entStamp[ch] (+0x7440): the update's tick + entityBlendTicks
	Last     int64      // VICreatureEntity +0x2A0: the tick of its last step
	ActorHpr [3]float32 // the scene actor's HPR; its position is Entity.X/Y/Z
	Creature bool       // a VICreatureEntity (type 0x82), not a plain VIEntity (0x81)
	Walking  bool       // +0x255 (byte 141): driven by its own VIWalkControl
}

// facing0To2Pi is a heading in Go's [0, 2π) facing convention.
func facing0To2Pi(a float32) float32 {
	f := float32(math.Mod(float64(a), 2*math.Pi))
	if f < 0 {
		f += 2 * math.Pi
	}
	return f
}

// creatureFloor is the position half of 0x00CBCE40: on a hit the position
// drops onto the floor, otherwise it stays.
func (gs *GameState) creatureFloor(p *[3]float32) {
	if gs.CreatureFloorFunc == nil {
		return
	}
	if y, ok := gs.CreatureFloorFunc(p[0], p[1], p[2]); ok {
		p[1] = y
	}
}

// entityUpdate applies an entity channel's position, HPR and (for a
// creature) velocity the way ChannelEntity and the creature's reader do.
// created is a slot that has just made its entity.
func (gs *GameState) entityUpdate(ent *Entity, created, creature bool, pos, hpr, vel [3]float32, walking bool) {
	m := &ent.Motion
	now := gs.ps2Now()
	oldVel := m.Vel // ChannelEntity saves +0x218 before the read
	m.Creature = creature
	m.Pos, m.Hpr = pos, hpr
	if creature {
		m.Vel = vel
		m.Walking = walking
		m.Last = now // 0x00CC2340: +0x2A0 = the scene's time
		if !walking {
			gs.creatureFloor(&m.Pos)
		}
	}
	if created {
		// A new entity keeps no error: its actor is made where it is.
		m.Err, m.ErrRot = [3]float32{}, [3]float32{}
		ent.X, ent.Y, ent.Z = m.Pos[0], m.Pos[1], m.Pos[2]
		m.ActorHpr = m.Hpr
		ent.Facing = facing0To2Pi(m.Hpr[0])
	} else {
		m.Err = [3]float32{ent.X - m.Pos[0], ent.Y - m.Pos[1], ent.Z - m.Pos[2]}
		for i := range m.ErrRot {
			m.ErrRot[i] = wrapPi(m.ActorHpr[i] - m.Hpr[i])
		}
	}
	m.Until = now + entityBlendTicks
	if creature && vel == oldVel {
		m.Err = [3]float32{}
	}
}

// UpdateEntityMotion is the frame's two passes in CLIENT's order (the frame,
// func_00BC6500): UpdateCreatures steps every creature, then Extrapolate puts
// every entity where it is drawn. The player is not among gs.Entities.
func (gs *GameState) UpdateEntityMotion() {
	now := gs.ps2Now()
	for _, ent := range gs.Entities {
		if !ent.Interpolating {
			continue
		}
		m := &ent.Motion
		// UpdateCreatures → VICreatureEntity::Walk, while m_p2BD48 (on from
		// the constructor; only a console toggle clears it).
		if m.Creature && !gs.NoCreatureSteps {
			dt := float32(int32(now-m.Last)) * 0.001 // func_00CC38F8: (float)(int)dt × 0.001
			for i := range m.Pos {
				m.Pos[i] += m.Vel[i] * dt // 0x00CBCD10
			}
			if !m.Walking {
				gs.creatureFloor(&m.Pos)
			}
			m.Last = now
		}
		// Extrapolate: t from 1 to 0 over the blend; m_p2BD4C (also on from
		// the constructor) clear moves nothing but the error.
		t := float32(m.Until-now) / float32(entityBlendTicks)
		if gs.NoEntityBlend || t < 0 {
			t = 0
		}
		ent.X = m.Pos[0] + m.Err[0]*t
		ent.Y = m.Pos[1] + m.Err[1]*t
		ent.Z = m.Pos[2] + m.Err[2]*t
		for i := range m.ActorHpr {
			m.ActorHpr[i] = m.Hpr[i] + m.ErrRot[i]*t
		}
		if m.Creature && m.Walking {
			m.ActorHpr[1], m.ActorHpr[2] = 0, 0
		}
		ent.Facing = facing0To2Pi(m.ActorHpr[0])
	}
}

// LeaderByte263 is VIClient::LeaderByte263 (0x00BD9350, [A]): out of a group,
// the player's own +0x263 (CombatFlag); in one, the leader's — the first
// member's — when that entity is a creature. The frame plays the battle music
// while it is set.
func (gs *GameState) LeaderByte263() bool {
	if !gs.GroupActive {
		return gs.Player.CombatFlag != 0
	}
	if len(gs.GroupMembers) == 0 {
		return false
	}
	id := gs.GroupMembers[0].ObjectID
	if id == gs.PlayerObjectID {
		return gs.Player.CombatFlag != 0
	}
	if e, ok := gs.Entities[id]; ok && e.Motion.Creature {
		return e.CombatFlag != 0
	}
	return false
}
