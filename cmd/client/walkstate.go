package main

import (
	"github.com/eqoa/client/internal/game"
)

// The walker's half of the 0x40 (internal/game/selfstate.go): what the
// visual client's walker hands the engine each frame, in the terms of the
// VIWalkPhysics CLIENT's writer reads.

// walkFlags is m_flags (decomp/src/vi.h VIWalkFlags) for this walker:
//
//   - onFloor (1) when standing, and landscape (0x400) with it: CalcFloor
//     sets that bit when the floor is not an actor, and this walker's floor
//     probe (terrain.Fit) does not say, so standing is taken as standing on
//     landscape -- 0x501, the PS2 captures' commonest value;
//   - the liquid bits 0x4..0x1000 and the feet-in-mud bit 0x2000, which
//     ProbeLiquid already returns in CLIENT's values;
//   - 0x100 in third person: VIWalkCamera::SetCameraMode sets it for the
//     camera mode whose walker turns to its target heading (mode 3) and
//     clears it for the rest, first person (mode 0) among them.
//
// The wall bit (2, the sweep hit something this tick) is not kept.
func (c *Client) walkFlags() uint32 {
	f := c.liquidFlags
	if c.onGround {
		f |= game.WalkOnFloor | game.WalkLandscape
	}
	if !c.firstPerson {
		f |= game.WalkTurnTrack
	}
	return f
}

// driveForce is the thrust VIWalkControl::Run or Walk adds for a move along
// (moveX, moveZ) toward speed: Run's forward component walks in mud.
func (c *Client) driveForce(moveX, moveZ, speed, walk float32) (float32, float32) {
	if c.inMud() && speed > walk {
		speed = walk
	}
	a := game.DriveAccel(speed)
	return moveX * a, moveZ * a
}
