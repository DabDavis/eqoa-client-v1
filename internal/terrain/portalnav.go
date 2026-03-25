package terrain

import (
	"container/heap"
	"math"

	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

// PortalNav provides pathfinding through the room/portal system.
// Rooms are nodes, portals (doorways) are edges with world-space positions.
type PortalNav struct {
	rooms []esf.ZoneRoom
}

// PortalWaypoint is a doorway to pass through on the path.
type PortalWaypoint struct {
	X, Y, Z float32 // portal centroid (world space)
	RoomID  int     // room index we enter through this portal
}

// NewPortalNav builds a portal navigator from zone rooms.
func NewPortalNav(rooms []esf.ZoneRoom) *PortalNav {
	if len(rooms) == 0 {
		return nil
	}
	return &PortalNav{rooms: rooms}
}

// findRoom returns the room index containing point (x,y,z), or -1.
// Uses room bounding boxes for fast lookup.
func (pn *PortalNav) findRoom(x, y, z float32) int {
	bestIdx := -1
	bestVol := float32(math.MaxFloat32)
	for i, r := range pn.rooms {
		b := r.BBox
		if b.MinX == 0 && b.MaxX == 0 {
			continue // no bbox (version < 2)
		}
		if x >= b.MinX && x <= b.MaxX && y >= b.MinY && y <= b.MaxY && z >= b.MinZ && z <= b.MaxZ {
			// If multiple rooms overlap, pick smallest (most specific)
			vol := (b.MaxX - b.MinX) * (b.MaxY - b.MinY) * (b.MaxZ - b.MinZ)
			if vol < bestVol {
				bestVol = vol
				bestIdx = i
			}
		}
	}
	return bestIdx
}

// portalCentroid computes the average position of a portal's vertices.
func portalCentroid(p *esf.ZoneRoomPortal) (float32, float32, float32) {
	if len(p.Vertices) == 0 {
		return 0, 0, 0
	}
	var sx, sy, sz float32
	for _, v := range p.Vertices {
		sx += v.X
		sy += v.Y
		sz += v.Z
	}
	n := float32(len(p.Vertices))
	return sx / n, sy / n, sz / n
}

// FindRoomAt returns the room index containing point (x,y,z), or -1 if outdoor.
func (pn *PortalNav) FindRoomAt(x, y, z float32) int {
	if pn == nil {
		return -1
	}
	return pn.findRoom(x, y, z)
}

// GetVisibleRooms returns the set of room indices visible from position (x,y,z)
// via BFS through portal connections. If the position is outside all rooms
// (outdoor), returns nil (meaning all rooms should be drawn).
func (pn *PortalNav) GetVisibleRooms(x, y, z float32) map[int]bool {
	if pn == nil {
		return nil
	}

	startRoom := pn.findRoom(x, y, z)
	if startRoom < 0 {
		return nil // outdoor — draw everything
	}

	visible := make(map[int]bool)
	queue := []int{startRoom}
	visible[startRoom] = true

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		if cur < 0 || cur >= len(pn.rooms) {
			continue
		}
		for _, portal := range pn.rooms[cur].Portals {
			dest := int(portal.DestRoomID)
			if dest < 0 {
				// Portal to exterior — mark exterior as visible but don't BFS from it
				visible[-1] = true
				continue
			}
			if dest < len(pn.rooms) && !visible[dest] {
				visible[dest] = true
				queue = append(queue, dest)
			}
		}
	}

	return visible
}

// FindPath returns a list of portal waypoints to pass through to get from
// (sx,sy,sz) to (gx,gy,gz). Returns nil if both points are in the same room,
// or if no portal path exists.
func (pn *PortalNav) FindPath(sx, sy, sz, gx, gy, gz float32) []PortalWaypoint {
	if pn == nil {
		return nil
	}

	startRoom := pn.findRoom(sx, sy, sz)
	goalRoom := pn.findRoom(gx, gy, gz)

	if startRoom < 0 || goalRoom < 0 {
		return nil // one or both points are outside any room
	}
	if startRoom == goalRoom {
		return nil // same room, no portals needed
	}

	// A* on room graph
	n := len(pn.rooms)
	gScore := make([]float32, n)
	parent := make([]int, n)
	parentPortal := make([]int, n) // which portal index in rooms[parent] leads to this room
	closed := make([]bool, n)
	for i := range gScore {
		gScore[i] = math.MaxFloat32
		parent[i] = -1
	}
	gScore[startRoom] = 0

	// Heuristic: distance from room center to goal
	heuristic := func(roomIdx int) float32 {
		r := &pn.rooms[roomIdx]
		cx := (r.BBox.MinX + r.BBox.MaxX) / 2
		cy := (r.BBox.MinY + r.BBox.MaxY) / 2
		cz := (r.BBox.MinZ + r.BBox.MaxZ) / 2
		dx := cx - gx
		dy := cy - gy
		dz := cz - gz
		return float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))
	}

	pq := &portalHeap{}
	heap.Init(pq)
	heap.Push(pq, portalNode{room: startRoom, f: heuristic(startRoom)})

	found := false
	for pq.Len() > 0 {
		cur := heap.Pop(pq).(portalNode)
		if cur.room == goalRoom {
			found = true
			break
		}
		if closed[cur.room] {
			continue
		}
		closed[cur.room] = true

		// Expand through portals
		for pi, portal := range pn.rooms[cur.room].Portals {
			dest := int(portal.DestRoomID)
			if dest < 0 || dest >= n || closed[dest] {
				continue
			}

			// Cost = distance between portal centroid and previous position
			px, py, pz := portalCentroid(&portal)
			var fromX, fromY, fromZ float32
			if cur.room == startRoom {
				fromX, fromY, fromZ = sx, sy, sz
			} else {
				// Use the portal we entered this room through
				pp := pn.rooms[parent[cur.room]].Portals[parentPortal[cur.room]]
				fromX, fromY, fromZ = portalCentroid(&pp)
			}
			dx := px - fromX
			dy := py - fromY
			dz := pz - fromZ
			cost := float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))

			newG := gScore[cur.room] + cost
			if newG < gScore[dest] {
				gScore[dest] = newG
				parent[dest] = cur.room
				parentPortal[dest] = pi
				heap.Push(pq, portalNode{room: dest, f: newG + heuristic(dest)})
			}
		}
	}

	if !found {
		return nil
	}

	// Reconstruct path: collect portal waypoints from start→goal
	var path []int // room indices from goal back to start
	cur := goalRoom
	for cur != startRoom {
		path = append(path, cur)
		cur = parent[cur]
	}

	// Reverse to get start→goal order
	var waypoints []PortalWaypoint
	for i := len(path) - 1; i >= 0; i-- {
		roomIdx := path[i]
		pi := parentPortal[roomIdx]
		portal := &pn.rooms[parent[roomIdx]].Portals[pi]
		px, py, pz := portalCentroid(portal)
		waypoints = append(waypoints, PortalWaypoint{
			X: px, Y: py, Z: pz,
			RoomID: roomIdx,
		})
	}

	return waypoints
}

// --- A* priority queue for portal nav ---

type portalNode struct {
	room int
	f    float32
}

type portalHeap []portalNode

func (h portalHeap) Len() int            { return len(h) }
func (h portalHeap) Less(i, j int) bool  { return h[i].f < h[j].f }
func (h portalHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *portalHeap) Push(x interface{}) { *h = append(*h, x.(portalNode)) }
func (h *portalHeap) Pop() interface{} {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}
