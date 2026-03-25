package terrain

import (
	"encoding/binary"
	"log"
	"math"
)

// PS2 VIZoneTree BSP — ported from VIZone::FindLeaf (0x00495230) and
// VIScene::GetEnvFlags (0x00464368).
//
// The BSP tree partitions a zone into spatial leaves. Each leaf stores envFlags
// that encode liquid volumes (water, lava, slime, mud).
//
// Child encoding: bit 30 = leaf flag, bits 0-29 = index.

const (
	bspLeafFlag  uint32 = 0x40000000
	bspIndexMask uint32 = 0x0FFFFFFF
)

// Environment flag bits returned by GetEnvFlags (PS2 VIScene::GetEnvFlags).
const (
	EnvFlagWater uint32 = 0x01
	EnvFlagSlime uint32 = 0x02
	EnvFlagLava  uint32 = 0x04
	EnvFlagMud   uint32 = 0x08
)

// bspNode is a BSP split plane node (24 bytes in ESF).
type bspNode struct {
	NX, NY, NZ, Dist float32
	FrontChild       uint32 // positive half-space child
	BackChild        uint32 // negative half-space child
}

// bspLeaf is a BSP leaf (8 bytes in ESF).
type bspLeaf struct {
	RoomIndex int32
	EnvFlags  uint32
}

// ZoneTree is a parsed BSP tree for spatial environment queries.
type ZoneTree struct {
	Nodes  []bspNode
	Leaves []bspLeaf
}

// parseZoneTree reads a ZoneTree from raw ESF bytes.
// Format: int32 numNodes, int32 numLeaves, then nodes (24 bytes each),
// then leaves (8 bytes each).
func parseZoneTree(data []byte) *ZoneTree {
	if len(data) < 8 {
		return nil
	}

	numNodes := int(binary.LittleEndian.Uint32(data[0:4]))
	numLeaves := int(binary.LittleEndian.Uint32(data[4:8]))

	expected := 8 + numNodes*24 + numLeaves*8
	if len(data) < expected {
		log.Printf("water: ZoneTree data too short: have %d, need %d", len(data), expected)
		return nil
	}

	tree := &ZoneTree{
		Nodes:  make([]bspNode, numNodes),
		Leaves: make([]bspLeaf, numLeaves),
	}

	off := 8
	for i := 0; i < numNodes; i++ {
		tree.Nodes[i] = bspNode{
			NX:         math.Float32frombits(binary.LittleEndian.Uint32(data[off:])),
			NY:         math.Float32frombits(binary.LittleEndian.Uint32(data[off+4:])),
			NZ:         math.Float32frombits(binary.LittleEndian.Uint32(data[off+8:])),
			Dist:       math.Float32frombits(binary.LittleEndian.Uint32(data[off+12:])),
			FrontChild: binary.LittleEndian.Uint32(data[off+16:]),
			BackChild:  binary.LittleEndian.Uint32(data[off+20:]),
		}
		off += 24
	}
	for i := 0; i < numLeaves; i++ {
		tree.Leaves[i] = bspLeaf{
			RoomIndex: int32(binary.LittleEndian.Uint32(data[off:])),
			EnvFlags:  binary.LittleEndian.Uint32(data[off+4:]),
		}
		off += 8
	}

	return tree
}

// FindLeaf traverses the BSP tree and returns the leaf index for a 3D position.
// Port of PS2 VIZone::FindLeaf at 0x00495230.
func (t *ZoneTree) FindLeaf(x, y, z float32) int {
	if len(t.Nodes) == 0 {
		if len(t.Leaves) > 0 {
			return 0
		}
		return -1
	}

	idx := uint32(0) // start at root node
	for {
		node := &t.Nodes[idx]
		dot := node.NX*x + node.NY*y + node.NZ*z + node.Dist

		var child uint32
		if dot >= 0 {
			child = node.FrontChild
		} else {
			child = node.BackChild
		}

		if child&bspLeafFlag != 0 {
			return int(child & bspIndexMask)
		}
		idx = child & bspIndexMask
		if int(idx) >= len(t.Nodes) {
			return -1 // safety: invalid index
		}
	}
}

// GetEnvFlags returns the environment flags at a 3D position.
// Port of PS2 VIScene::GetEnvFlags at 0x00464368.
func (t *ZoneTree) GetEnvFlags(x, y, z float32) uint32 {
	leafIdx := t.FindLeaf(x, y, z)
	if leafIdx < 0 || leafIdx >= len(t.Leaves) {
		return 0
	}
	return t.Leaves[leafIdx].EnvFlags
}

// GetEnvFlags queries the zone's BSP tree for environment flags at a position.
// Returns 0 if no BSP tree is loaded.
func (zt *ZoneTerrain) GetEnvFlags(x, y, z float32) uint32 {
	if zt.Tree == nil {
		return 0
	}
	return zt.Tree.GetEnvFlags(x, y, z)
}

// WaterState describes the player's relationship to water, matching PS2
// VIWalkControl flag detection using 3 vertical probes.
type WaterState int

const (
	WaterNone    WaterState = iota // not in water
	WaterSurface                   // at water surface (feet wet, head above)
	WaterUnder                     // fully submerged
)

// FindWaterSurfaceY finds the Y coordinate of the water surface at (x, z)
// by binary-searching the BSP between footY and footY+charHeight.
// PS2 Move (0x486778) calls GetIntersectY on the BSP leaf to find this.
// Returns the surface Y and true if found, or 0, false if not in water.
func (zt *ZoneTerrain) FindWaterSurfaceY(x, footY, z, charHeight float32) (float32, bool) {
	if zt.Tree == nil {
		return 0, false
	}
	// Find Y range: water below, air above.
	lo := footY
	hi := footY + charHeight
	loFlags := zt.Tree.GetEnvFlags(x, lo, z)
	hiFlags := zt.Tree.GetEnvFlags(x, hi, z)

	if loFlags&EnvFlagWater == 0 {
		return 0, false // feet not in water
	}
	if hiFlags&EnvFlagWater != 0 {
		return hi, true // fully submerged, surface is above head
	}

	// Binary search for the transition point (12 iterations ≈ charHeight/4096 precision)
	for i := 0; i < 12; i++ {
		mid := (lo + hi) * 0.5
		if zt.Tree.GetEnvFlags(x, mid, z)&EnvFlagWater != 0 {
			lo = mid
		} else {
			hi = mid
		}
	}
	return lo, true
}

// ProbeWater performs the PS2 3-probe water detection.
// PS2 VIWalkControl::Move probes at center, +offset, and -offset to determine
// whether the player is under, at surface, or above water.
//
// probeY is the player's foot Y. charHeight is the character height.
func (zt *ZoneTerrain) ProbeWater(x, footY, z, charHeight float32) WaterState {
	if zt.Tree == nil {
		return WaterNone
	}

	// PS2 probes: center of body, top of head, feet
	centerY := footY + charHeight*0.5
	topY := footY + charHeight
	bottomY := footY + 0.1

	centerFlags := zt.Tree.GetEnvFlags(x, centerY, z)
	topFlags := zt.Tree.GetEnvFlags(x, topY, z)
	bottomFlags := zt.Tree.GetEnvFlags(x, bottomY, z)

	centerWater := centerFlags&EnvFlagWater != 0
	topWater := topFlags&EnvFlagWater != 0
	bottomWater := bottomFlags&EnvFlagWater != 0

	if centerWater && topWater {
		return WaterUnder // fully submerged
	}
	if centerWater || bottomWater {
		// Center or feet in water, but head above — at surface
		return WaterSurface
	}
	if topWater && !centerWater {
		// Head in water zone but center isn't — edge case, treat as surface
		return WaterSurface
	}

	return WaterNone
}
