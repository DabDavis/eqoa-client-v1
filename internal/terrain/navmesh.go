package terrain

import (
	"github.com/DabDavis/eqoa-esf-tools/pkg/navmesh"
)

// Re-export shared types for client convenience.
type NavMesh = navmesh.NavMesh
type Waypoint = navmesh.Waypoint

// BuildNavMesh constructs a navigation mesh from the walkable collision
// triangles in the given ZoneTerrain. Call once per zone load.
func BuildNavMesh(zt *ZoneTerrain) *NavMesh {
	if zt == nil || len(zt.CollTiles) == 0 {
		return nil
	}

	// Collect walkable triangles into the shared InputTriangle format
	var tris []navmesh.InputTriangle
	for _, tile := range zt.CollTiles {
		for _, ct := range tile.Triangles {
			if !ct.Walkable {
				continue
			}
			tris = append(tris, navmesh.InputTriangle{
				X0: ct.X0, Y0: ct.Y0, Z0: ct.Z0,
				X1: ct.X1, Y1: ct.Y1, Z1: ct.Z1,
				X2: ct.X2, Y2: ct.Y2, Z2: ct.Z2,
			})
		}
	}

	return navmesh.Build(tris)
}
