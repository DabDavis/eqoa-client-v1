package terrain

import (
	"fmt"
	"image"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

// TileSize is the world-unit width/height of each cached tile.
const TileSize float32 = 128

// TileKey identifies a tile by its integer grid coordinates.
type TileKey struct {
	TX, TZ int
}

// Triangle holds three 3D vertices with position, UV, normal, and color+alpha.
type Triangle struct {
	X0, Y0, Z0, U0, V0, NX0, NY0, NZ0, R0, G0, B0, A0 float32
	X1, Y1, Z1, U1, V1, NX1, NY1, NZ1, R1, G1, B1, A1 float32
	X2, Y2, Z2, U2, V2, NX2, NY2, NZ2, R2, G2, B2, A2 float32
	TexID                                                int32
	Layer                                                int32   // 0 = base, 1+ = overlay
	URate, VRate                                         float32 // UV scroll (per-material, not per-texture)
}

// Tile holds all triangles that fall within a tile grid cell.
type Tile struct {
	Triangles []Triangle
}

// UVRateKey identifies a mesh group for UV animation lookup.
type UVRateKey struct {
	TexID int32
	Layer int32
}

// ZoneTerrain is the processed output: a map of tiles keyed by grid position.
type ZoneTerrain struct {
	Tiles         map[TileKey]*Tile
	CollTiles     map[TileKey]*CollTile          // collision mesh (from CollBuffer)
	Nav           *NavMesh                       // pathfinding navmesh (built from walkable CollTiles)
	Rooms         []esf.ZoneRoom                // room/portal data for indoor pathfinding
	Textures      map[int32]*image.NRGBA         // TexID → CPU image
	Tree          *ZoneTree                       // BSP tree for environment queries (water/lava)
	EffectVolumes []esf.EffectVolumePlacement     // localized environmental effects
	FloraTriangles []Triangle                     // flora objects extracted for separate alpha-blended rendering
	FloraTextures  map[int32]*image.NRGBA         // textures used only by flora (may overlap with Textures)
	RadialFlora    *esf.ZoneRadialFlora           // procedural flora models + distribution data
	PointLights    []esf.PointLightPlacement      // zone point lights (torches, lamps)
	ZoneParticles  []esf.ZoneParticlePlacement    // zone-placed particle effects (fire, smoke)
	UVRates       map[UVRateKey][2]float32        // non-zero UV scroll rates (water animation)
	MinTX         int
	MaxTX         int
	MinTZ         int
	MaxTZ         int
	NumTris       int
	// Actor obstacle footprints for pathfinding (computed during zone load)
	Obstacles     []ActorObstacle
}

// ActorObstacle is the 2D XZ bounding rectangle of a zone actor's collision mesh.
// Used by auto-approach pathfinding to generate waypoints around buildings/walls.
type ActorObstacle struct {
	MinX, MinZ, MaxX, MaxZ float32 // axis-aligned bounds in world space
	CX, CZ                float32 // center
}

// ProbeDoorways finds openings in an obstacle by casting CalcFit probes from
// its center outward in 36 directions (every 10°). Directions where the probe
// passes through without hitting a wall = door opening. Called at pathfinding
// time when the character needs to enter/exit this obstacle.
func ProbeDoorways(zt *ZoneTerrain, obs *ActorObstacle, refY float32) []ObstacleWaypoint {
	if zt == nil {
		return nil
	}

	cx, cz := obs.CX, obs.CZ
	// Probe radius = half the obstacle's larger dimension + margin
	extX := (obs.MaxX - obs.MinX) / 2
	extZ := (obs.MaxZ - obs.MinZ) / 2
	radius := extX
	if extZ > radius {
		radius = extZ
	}
	radius += 2.0 // overshoot past the wall

	// Cast 36 probes from center outward (every 10°)
	const numProbes = 36
	var openings []ObstacleWaypoint

	for i := 0; i < numProbes; i++ {
		angle := float64(i) * 2 * math.Pi / numProbes
		dirX := float32(math.Sin(angle))
		dirZ := float32(math.Cos(angle))

		probeX := cx + dirX*radius
		probeZ := cz + dirZ*radius

		fit := zt.CalcFit(cx, refY, cz, probeX, probeZ)
		if !fit.Hit {
			// No wall hit — this direction has an opening.
			// Place the waypoint at the obstacle boundary (not center, not overshoot).
			wpX := cx + dirX*(radius-1.0)
			wpZ := cz + dirZ*(radius-1.0)
			openings = append(openings, ObstacleWaypoint{X: wpX, Z: wpZ})
		}
	}

	// Cluster nearby openings — consecutive clear probes are the same doorway.
	// Take the midpoint of each cluster.
	if len(openings) == 0 {
		return nil
	}

	var doors []ObstacleWaypoint
	clusterX := openings[0].X
	clusterZ := openings[0].Z
	clusterN := 1

	for i := 1; i < len(openings); i++ {
		dx := openings[i].X - openings[i-1].X
		dz := openings[i].Z - openings[i-1].Z
		if dx*dx+dz*dz < 9.0 { // within 3 units = same cluster
			clusterX += openings[i].X
			clusterZ += openings[i].Z
			clusterN++
		} else {
			doors = append(doors, ObstacleWaypoint{
				X: clusterX / float32(clusterN),
				Z: clusterZ / float32(clusterN),
			})
			clusterX = openings[i].X
			clusterZ = openings[i].Z
			clusterN = 1
		}
	}
	doors = append(doors, ObstacleWaypoint{
		X: clusterX / float32(clusterN),
		Z: clusterZ / float32(clusterN),
	})

	return doors
}

// loadedZone tracks one loaded zone's state.
type loadedZone struct {
	proxyIdx int
	terrain  *ZoneTerrain
	ready    bool
	loading  bool
	err      error
}

// Loader opens a TUNARIA.ESF file and provides async zone loading.
// Supports multiple simultaneously loaded zones (PS2 radial streaming).
type Loader struct {
	file      *esf.ObjFile
	proxies   *esf.WorldZoneProxies
	worldTree *esf.WorldTree      // PS2 spatial quadtree for O(log N) zone lookup
	Regions   *esf.WorldRegions   // biome region IDs per zone proxy (for ambient sounds)
	zones     []*esf.ObjInfo

	mu        sync.Mutex
	loaded    map[int]*loadedZone   // proxyIdx → loaded zone
	loadQueue chan loadRequest       // serialized load queue

	// Zone overlays: proxyIdx → standalone ESF file replacing that zone.
	overlays map[int]*esf.ObjFile

	// Legacy single-zone API (kept for backwards compat during transition)
	terrain *ZoneTerrain
	ready   atomic.Bool
	loading atomic.Bool
	err     error
}

// NewLoader opens the ESF file and parses WorldZoneProxies and Zone list.
func NewLoader(esfPath string) (*Loader, error) {
	log.Printf("terrain: opening %s", esfPath)
	file, err := esf.Open(esfPath)
	if err != nil {
		return nil, err
	}
	return NewLoaderFromFile(file)
}

// File returns the underlying ObjFile for direct access (e.g. BGM zone loading).
func (l *Loader) File() *esf.ObjFile { return l.file }

// NewLoaderFromFile creates a Loader from an already-opened ObjFile.
func NewLoaderFromFile(file *esf.ObjFile) (*Loader, error) {
	root, err := file.Root()
	if err != nil {
		return nil, err
	}

	// Load WorldZoneProxies for bounding-box zone lookup.
	worldBase := root.Child(esf.TypeWorldBase)
	if worldBase == nil {
		return nil, err
	}
	proxiesInfo := worldBase.Child(esf.TypeWorldZoneProxies)
	if proxiesInfo == nil {
		log.Printf("terrain: no WorldZoneProxies found")
		return &Loader{file: file}, nil
	}
	proxiesObj, err := file.GetObject(proxiesInfo)
	if err != nil {
		return nil, err
	}
	proxies := proxiesObj.(*esf.WorldZoneProxies)
	log.Printf("terrain: loaded %d zone proxies", len(proxies.Zones))

	// Load WorldTree and WorldRegions for spatial lookup and ambient sounds.
	var worldTree *esf.WorldTree
	var worldRegions *esf.WorldRegions
	wbObj, err := file.GetObject(worldBase)
	if err == nil && wbObj != nil {
		wb := wbObj.(*esf.WorldBase)
		if wb.Tree != nil {
			worldTree = wb.Tree
			log.Printf("terrain: loaded world tree (%d nodes, %d leaf zones)", len(worldTree.Nodes), len(worldTree.LeafZones))
		}
		if wb.Regions != nil {
			worldRegions = wb.Regions
			log.Printf("terrain: loaded world regions (%d entries)", len(worldRegions.RegionIDs))
		}
	}

	// Collect Zone ObjInfos from root.
	world := root.Child(esf.TypeWorld)
	var zones []*esf.ObjInfo
	if world != nil {
		zones = world.ChildrenOfType(esf.TypeZone)
	}
	log.Printf("terrain: found %d zones in world tree", len(zones))

	return &Loader{
		file:      file,
		proxies:   proxies,
		worldTree: worldTree,
		Regions:   worldRegions,
		zones:     zones,
	}, nil
}

// FindZone returns the index into the proxies.Zones slice for the zone
// containing the given XZ world position. If the point is outside all
// bounding boxes, returns the nearest zone within 200 units, or -1.
func (l *Loader) FindZone(x, z float32) int {
	if l.proxies == nil {
		return -1
	}
	// Use WorldTree quadtree for O(log N) lookup when available.
	if l.worldTree != nil && len(l.worldTree.Nodes) > 0 {
		idx := l.findZoneByTree(x, z)
		if idx >= 0 {
			return idx
		}
	}
	// Fallback: linear scan of zone proxy bounding boxes.
	pt := esf.Point{X: x, Y: 0, Z: z}
	for i := range l.proxies.Zones {
		if l.proxies.Zones[i].BBox.ContainsXZ(pt) {
			return i
		}
	}
	// Player is between zones — find the nearest bbox center within 200 units.
	const maxDist float32 = 200 * 200
	bestIdx := -1
	bestDist := maxDist
	for i := range l.proxies.Zones {
		c := l.proxies.Zones[i].Center
		dx := x - c.X
		dz := z - c.Z
		d2 := dx*dx + dz*dz
		if d2 < bestDist {
			bestDist = d2
			bestIdx = i
		}
	}
	return bestIdx
}

// findZoneByTree walks the WorldTree quadtree to find the zone proxy
// containing the given XZ position. Returns -1 if not found.
// PS2 uses this in VIScene for zone streaming decisions.
func (l *Loader) findZoneByTree(x, z float32) int {
	tree := l.worldTree
	if len(tree.Nodes) == 0 {
		return -1
	}
	// Walk from root (node 0) down the tree.
	nodeIdx := uint32(0)
	for {
		if int(nodeIdx) >= len(tree.Nodes) {
			return -1
		}
		node := &tree.Nodes[nodeIdx]
		// Check if point is inside this node's 2D rect
		if x < node.MinX || x > node.MaxX || z < node.MinZ || z > node.MaxZ {
			return -1 // outside bounds
		}
		// Check if this is a leaf node.
		// Convention: if ChildA >= numNodes, it's a leaf.
		// ChildA = start index into LeafZones, ChildB = count.
		numNodes := uint32(len(tree.Nodes))
		if node.ChildA >= numNodes {
			// Leaf node — search the leaf zone indices for containment.
			start := node.ChildA - numNodes
			count := node.ChildB
			if int(start+count) > len(tree.LeafZones) {
				return -1
			}
			pt := esf.Point{X: x, Y: 0, Z: z}
			for i := start; i < start+count; i++ {
				zoneIdx := int(tree.LeafZones[i])
				if zoneIdx >= 0 && zoneIdx < len(l.proxies.Zones) {
					if l.proxies.Zones[zoneIdx].BBox.ContainsXZ(pt) {
						return zoneIdx
					}
				}
			}
			// Point is in leaf's rect but not in any zone bbox — nearest
			bestIdx := -1
			bestDist := float32(1e18)
			for i := start; i < start+count; i++ {
				zoneIdx := int(tree.LeafZones[i])
				if zoneIdx >= 0 && zoneIdx < len(l.proxies.Zones) {
					c := l.proxies.Zones[zoneIdx].Center
					dx := x - c.X
					dz := z - c.Z
					d2 := dx*dx + dz*dz
					if d2 < bestDist {
						bestDist = d2
						bestIdx = zoneIdx
					}
				}
			}
			return bestIdx
		}
		// Internal node — descend into the child that contains the point.
		// Try ChildA first, then ChildB.
		childA := &tree.Nodes[node.ChildA]
		if x >= childA.MinX && x <= childA.MaxX && z >= childA.MinZ && z <= childA.MaxZ {
			nodeIdx = node.ChildA
			continue
		}
		nodeIdx = node.ChildB
	}
}

// LoadZoneAsync starts loading the zone at proxyIndex in a background goroutine.
// Safe to call multiple times; only the first call starts loading.
func (l *Loader) LoadZoneAsync(proxyIndex int) {
	if l.loading.Swap(true) {
		return // already loading
	}
	go func() {
		terrain, err := l.loadZone(proxyIndex)
		l.mu.Lock()
		l.terrain = terrain
		l.err = err
		l.mu.Unlock()
		if err != nil {
			log.Printf("terrain: load error: %v", err)
		} else {
			log.Printf("terrain: ready — %d triangles in %d tiles", terrain.NumTris, len(terrain.Tiles))
		}
		l.ready.Store(err == nil)
	}()
}

// ResetForReload clears the loading state so a new zone can be loaded.
// The caller should also reset the Renderer (create a new one) since GPU meshes
// belong to the previous zone.
func (l *Loader) ResetForReload() {
	l.ready.Store(false)
	l.loading.Store(false)
	l.mu.Lock()
	l.terrain = nil
	l.err = nil
	l.mu.Unlock()
}

// IsReady returns true once the zone terrain has been loaded.
func (l *Loader) IsReady() bool {
	return l.ready.Load()
}

// GetTerrain returns the loaded zone terrain, or nil if not ready.
func (l *Loader) GetTerrain() *ZoneTerrain {
	if !l.ready.Load() {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.terrain
}

// GetZoneProxies returns the zone proxy list, or nil if not loaded.
func (l *Loader) GetZoneProxies() *esf.WorldZoneProxies {
	return l.proxies
}

// FindNearbyZones returns all zone proxy indices whose bounding box is within
// radius of the given XZ position. This is the PS2 VISceneStreamRadialVars
// approach: load all zones within streaming distance of the player.
func (l *Loader) FindNearbyZones(x, z, radius float32) []int {
	if l.proxies == nil {
		return nil
	}
	r2 := radius * radius
	var result []int
	for i := range l.proxies.Zones {
		bb := &l.proxies.Zones[i].BBox
		// Distance from point to AABB (XZ only)
		dx := float32(0)
		dz := float32(0)
		if x < bb.MinX {
			dx = bb.MinX - x
		} else if x > bb.MaxX {
			dx = x - bb.MaxX
		}
		if z < bb.MinZ {
			dz = bb.MinZ - z
		} else if z > bb.MaxZ {
			dz = z - bb.MaxZ
		}
		if dx*dx+dz*dz <= r2 {
			result = append(result, i)
		}
	}
	return result
}

// EnsureZoneLoaded queues a zone for loading if not already loaded/loading.
// Loading is serialized (one at a time) because esf.ObjFile is not goroutine-safe.
func (l *Loader) EnsureZoneLoaded(proxyIndex int) {
	l.mu.Lock()
	if l.loaded == nil {
		l.loaded = make(map[int]*loadedZone)
	}
	if _, exists := l.loaded[proxyIndex]; exists {
		l.mu.Unlock()
		return // already loaded or loading
	}
	lz := &loadedZone{proxyIdx: proxyIndex, loading: true}
	l.loaded[proxyIndex] = lz

	// Start the serialized loader goroutine if not already running.
	needStart := l.loadQueue == nil
	if l.loadQueue == nil {
		l.loadQueue = make(chan loadRequest, 32)
	}
	l.mu.Unlock()

	if needStart {
		go l.loadWorker()
	}
	l.loadQueue <- loadRequest{proxyIndex: proxyIndex, lz: lz}
}

type loadRequest struct {
	proxyIndex int
	lz         *loadedZone
}

// loadWorker processes zone loads one at a time to avoid concurrent ObjFile access.
func (l *Loader) loadWorker() {
	for req := range l.loadQueue {
		terrain, err := l.loadZone(req.proxyIndex)
		// Release ESF object cache after each zone load — parsed objects are no longer
		// needed once terrain data is extracted. Saves ~86MB from GC pointer scanning.
		// Objects are re-parsed if needed for the next zone load (rare, one-time cost).
		l.file.ReleaseCache()
		runtime.GC() // force GC now (during load screen) so it stays quiet during gameplay
		l.mu.Lock()
		req.lz.terrain = terrain
		req.lz.err = err
		req.lz.loading = false
		req.lz.ready = err == nil
		l.mu.Unlock()
		if err != nil {
			log.Printf("terrain: zone %d load error: %v", req.proxyIndex, err)
		} else {
			log.Printf("terrain: zone %d ready — %d tris in %d tiles", req.proxyIndex, terrain.NumTris, len(terrain.Tiles))
		}
	}
}

// UnloadZone removes a loaded zone. Caller must also clean up GPU resources.
func (l *Loader) UnloadZone(proxyIndex int) {
	l.mu.Lock()
	delete(l.loaded, proxyIndex)
	l.mu.Unlock()
	log.Printf("terrain: zone %d unloaded", proxyIndex)
}

// GetLoadedZones returns all zone proxy indices that are fully loaded and ready.
func (l *Loader) GetLoadedZones() []int {
	l.mu.Lock()
	defer l.mu.Unlock()
	var result []int
	for idx, lz := range l.loaded {
		if lz.ready {
			result = append(result, idx)
		}
	}
	return result
}

// GetZoneTerrain returns the ZoneTerrain for a specific zone, or nil.
func (l *Loader) GetZoneTerrain(proxyIndex int) *ZoneTerrain {
	l.mu.Lock()
	defer l.mu.Unlock()
	if lz, ok := l.loaded[proxyIndex]; ok && lz.ready {
		return lz.terrain
	}
	return nil
}

// SetZoneOverlay registers a standalone ESF file to replace a specific zone.
// The overlay ESF should contain a World with exactly one Zone child (index 0).
func (l *Loader) SetZoneOverlay(proxyIndex int, overlayPath string) error {
	overlayFile, err := esf.Open(overlayPath)
	if err != nil {
		return fmt.Errorf("opening overlay %s: %w", overlayPath, err)
	}
	l.mu.Lock()
	if l.overlays == nil {
		l.overlays = make(map[int]*esf.ObjFile)
	}
	l.overlays[proxyIndex] = overlayFile
	l.mu.Unlock()
	log.Printf("terrain: zone %d overlay set: %s", proxyIndex, overlayPath)
	return nil
}

// LoadZoneOverlays scans a directory for zone_N.esf files and registers them.
func (l *Loader) LoadZoneOverlays(dir string) {
	log.Printf("terrain: scanning overlay dir: %s", dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Printf("terrain: overlay dir not found: %v", err)
		return
	}
	log.Printf("terrain: overlay dir has %d entries", len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		var idx int
		if _, err := fmt.Sscanf(e.Name(), "zone_%d.esf", &idx); err == nil {
			path := filepath.Join(dir, e.Name())
			if err := l.SetZoneOverlay(idx, path); err != nil {
				log.Printf("terrain: overlay %s: %v", e.Name(), err)
			}
		}
	}
}

// loadZoneFromFile loads a zone from a standalone ESF file (overlay).
// The overlay ESF has World → Zone(0) structure.
func (l *Loader) loadZoneFromFile(overlayFile *esf.ObjFile, proxyIndex int) (*ZoneTerrain, error) {
	root, err := overlayFile.Root()
	if err != nil {
		return nil, fmt.Errorf("overlay root: %w", err)
	}
	world := root.Child(esf.TypeWorld)
	if world == nil {
		return nil, fmt.Errorf("overlay has no World node")
	}
	zones := world.ChildrenOfType(esf.TypeZone)
	if len(zones) == 0 {
		return nil, fmt.Errorf("overlay has no Zone nodes")
	}
	obj, err := overlayFile.GetObject(zones[0])
	if err != nil {
		return nil, fmt.Errorf("overlay zone load: %w", err)
	}
	zone, ok := obj.(*esf.Zone)
	if !ok {
		return nil, fmt.Errorf("overlay zone type mismatch")
	}

	placements, err := zone.GetSpritePlacements(overlayFile)
	if err != nil {
		return nil, fmt.Errorf("overlay placements: %w", err)
	}
	log.Printf("terrain: overlay zone has %d sprite placements", len(placements))

	// Use the same loading pipeline but with overlayFile as the data source.
	return l.loadZoneData(zone, overlayFile, placements, proxyIndex)
}

// loadZone does the heavy work of extracting collision mesh triangles from a zone.
func (l *Loader) loadZone(proxyIndex int) (*ZoneTerrain, error) {
	proxy := &l.proxies.Zones[proxyIndex]
	log.Printf("terrain: loading zone %d (offset=%d, bbox=%.0f,%.0f..%.0f,%.0f)",
		proxyIndex, proxy.ZoneOffset, proxy.BBox.MinX, proxy.BBox.MinZ, proxy.BBox.MaxX, proxy.BBox.MaxZ)

	// Check for zone overlay first.
	if overlayFile, ok := l.overlays[proxyIndex]; ok {
		log.Printf("terrain: *** ZONE %d USING OVERLAY ESF ***", proxyIndex)
		return l.loadZoneFromFile(overlayFile, proxyIndex)
	}
	log.Printf("terrain: zone %d no overlay (have %d overlays)", proxyIndex, len(l.overlays))

	// Find the Zone object by matching ObjInfo.Offset to proxy.ZoneOffset.
	var zone *esf.Zone
	for _, zi := range l.zones {
		if int64(zi.Offset) == proxy.ZoneOffset {
			obj, err := l.file.GetObject(zi)
			if err != nil {
				continue
			}
			if z, ok := obj.(*esf.Zone); ok {
				zone = z
				break
			}
		}
	}

	// Fallback: use proxyIndex as zone array index.
	if zone == nil && proxyIndex < len(l.zones) {
		obj, err := l.file.GetObject(l.zones[proxyIndex])
		if err == nil {
			if z, ok := obj.(*esf.Zone); ok {
				zone = z
			}
		}
	}

	if zone == nil {
		log.Printf("terrain: could not resolve zone for proxy %d", proxyIndex)
		// Return empty terrain rather than error, so we don't block rendering
		return &ZoneTerrain{Tiles: make(map[TileKey]*Tile)}, nil
	}

	placements, err := zone.GetSpritePlacements(l.file)
	if err != nil {
		return nil, err
	}
	log.Printf("terrain: zone has %d sprite placements", len(placements))

	return l.loadZoneData(zone, l.file, placements, proxyIndex)
}

// loadZoneData is the shared loading pipeline used by both loadZone and overlay loading.
func (l *Loader) loadZoneData(zone *esf.Zone, file *esf.ObjFile, placements []*esf.SpritePlacement, proxyIndex int) (*ZoneTerrain, error) {
	zt := &ZoneTerrain{
		Tiles:         make(map[TileKey]*Tile),
		CollTiles:     make(map[TileKey]*CollTile),
		Textures:      make(map[int32]*image.NRGBA),
		FloraTextures: make(map[int32]*image.NRGBA),
		UVRates:       make(map[UVRateKey][2]float32),
		MinTX:         math.MaxInt32,
		MaxTX:         math.MinInt32,
		MinTZ:         math.MaxInt32,
		MaxTZ:         math.MinInt32,
	}

	var primCount, collCount, collTriCount, multiLayerCount int
	var nilSpriteCount, noMatPalCount, totalLayers, nilSurfTotal, nilImgTotal int
	var floraCount, bakedLitCount int

	for _, sp := range placements {
		sprite, err := sp.GetSprite(file)
		if err != nil || sprite == nil {
			nilSpriteCount++
			continue
		}

		// Always load CollBuffer for collision mesh (independent of PrimBuffer).
		cb, cbErr := sprite.GetCollBuffer(file)
		if cbErr == nil && cb != nil {
			collCount++
			// Track XZ bounds for this actor's collision mesh (obstacle footprint)
			actorMinX := float32(math.MaxFloat32)
			actorMinZ := float32(math.MaxFloat32)
			actorMaxX := float32(-math.MaxFloat32)
			actorMaxZ := float32(-math.MaxFloat32)
			actorTriCount := 0

			for _, list := range cb.Lists {
				nv := len(list.Vertices)
				if nv < 3 {
					continue
				}
				for i := 0; i < nv-2; i++ {
					var a, b, c esf.CollVertex
					if i%2 == 0 {
						a, b, c = list.Vertices[i+1], list.Vertices[i], list.Vertices[i+2]
					} else {
						a, b, c = list.Vertices[i], list.Vertices[i+1], list.Vertices[i+2]
					}

					pa := esf.Point{X: a.X, Y: a.Y, Z: a.Z}
					pbl := esf.Point{X: b.X, Y: b.Y, Z: b.Z}
					pc := esf.Point{X: c.X, Y: c.Y, Z: c.Z}
					sp.Transform(&pa)
					sp.Transform(&pbl)
					sp.Transform(&pc)

					// Flora type from vertices (PS2 CollBuffer type 3 stores per-vertex flora set)
					ft := a.FloraType
					if ft < 0 {
						ft = b.FloraType
					}
					if ft < 0 {
						ft = c.FloraType
					}
					ct := CollTriangle{
						X0: pa.X, Y0: pa.Y, Z0: pa.Z,
						X1: pbl.X, Y1: pbl.Y, Z1: pbl.Z,
						X2: pc.X, Y2: pc.Y, Z2: pc.Z,
						FloraType: ft,
					}

					// Skip degenerate collision triangles.
					const eps = 0.001
					if close3D(ct.X0, ct.Y0, ct.Z0, ct.X1, ct.Y1, ct.Z1, eps) ||
						close3D(ct.X1, ct.Y1, ct.Z1, ct.X2, ct.Y2, ct.Z2, eps) ||
						close3D(ct.X0, ct.Y0, ct.Z0, ct.X2, ct.Y2, ct.Z2, eps) {
						continue
					}

					addCollTriToZone(zt, ct)
					collTriCount++
					actorTriCount++

					// Expand actor XZ bounds
					for _, vx := range []float32{pa.X, pbl.X, pc.X} {
						if vx < actorMinX { actorMinX = vx }
						if vx > actorMaxX { actorMaxX = vx }
					}
					for _, vz := range []float32{pa.Z, pbl.Z, pc.Z} {
						if vz < actorMinZ { actorMinZ = vz }
						if vz > actorMaxZ { actorMaxZ = vz }
					}
				}
			}

			// Store obstacle footprint if actor is large enough to matter for pathfinding
			// (skip tiny collision meshes like fence posts, small rocks)
			extentX := actorMaxX - actorMinX
			extentZ := actorMaxZ - actorMinZ
			if actorTriCount >= 4 && extentX > 2 && extentZ > 2 {
				zt.Obstacles = append(zt.Obstacles, ActorObstacle{
					MinX: actorMinX, MinZ: actorMinZ,
					MaxX: actorMaxX, MaxZ: actorMaxZ,
					CX:   (actorMinX + actorMaxX) / 2,
					CZ:   (actorMinZ + actorMaxZ) / 2,
				})
			}
		}

		// Try PrimBuffer for visual mesh (textured rendering).
		pb, pbErr := sprite.GetPrimBuffer(file)
		if pbErr == nil && pb != nil {
			primCount++
			if sp.IsFlora {
				floraCount++
			}
			if pb.MatPal == nil {
				noMatPalCount++
			}
			bakedOff := 0 // tracks vertex offset into sp.BakedColors across vertex lists
		if len(sp.BakedColors) > 0 {
			bakedLitCount++
		}
		for _, vl := range pb.VertexLists {
				nv := len(vl.Vertices)
				if nv < 3 {
					continue
				}

				// Resolve texture layers from material palette.
				type layerInfo struct {
					TexID     int32
					UVT       [9]float32 // UVTransform
					Color     [4]byte    // Layer color (RGBA) from MaterialLayer
					BlendMode int32
					URate     float32
					VRate     float32
				}
				var layers []layerInfo
				if pb.MatPal != nil && vl.Material < len(pb.MatPal.Materials) {
					mat := pb.MatPal.Materials[vl.Material]
					for li, layer := range mat.Layers {
						totalLayers++
						if layer.Surface == nil {
							nilSurfTotal++
							continue
						}
						if layer.Surface.Image == nil {
							nilImgTotal++
							continue
						}
						texID := layer.TexID
						// Store texture in the appropriate map.
						if sp.IsFlora {
							if _, exists := zt.FloraTextures[texID]; !exists {
								zt.FloraTextures[texID] = layer.Surface.Image
							}
						} else {
							if _, exists := zt.Textures[texID]; !exists {
								zt.Textures[texID] = layer.Surface.Image
							}
						}
						if li > 0 && multiLayerCount < 3 {
							log.Printf("terrain: overlay layer %d: texID=%d blendMode=%d color=(%d,%d,%d,%d) uvt=[%.0f,%.0f,%.0f,%.0f,%.0f,%.0f]",
								li, texID, layer.BlendMode,
								layer.Color[0], layer.Color[1], layer.Color[2], layer.Color[3],
								layer.UVTransform[0], layer.UVTransform[1], layer.UVTransform[3], layer.UVTransform[4], layer.UVTransform[6], layer.UVTransform[7])
						}
						layers = append(layers, layerInfo{TexID: texID, UVT: layer.UVTransform, Color: layer.Color, BlendMode: layer.BlendMode, URate: layer.URate, VRate: layer.VRate})
					// Track non-zero UV scroll rates for water animation.
					if layer.URate != 0 || layer.VRate != 0 {
						rk := UVRateKey{TexID: texID, Layer: int32(li)}
						zt.UVRates[rk] = [2]float32{layer.URate, layer.VRate}
					}
					}
				}
				if len(layers) == 0 {
					layers = []layerInfo{{TexID: 0}}
				}
				if len(layers) > 1 {
					multiLayerCount++
				}

				// Triangle strip → individual triangles, emitted for each layer.
				// PS2/EQOA uses CW front-face convention; OpenGL uses CCW.
				// Swap even/odd vertex order to flip winding for correct face culling.
				for i := 0; i < nv-2; i++ {
					var a, b, c esf.Vertex
					if i%2 == 0 {
						a, b, c = vl.Vertices[i+1], vl.Vertices[i], vl.Vertices[i+2]
					} else {
						a, b, c = vl.Vertices[i], vl.Vertices[i+1], vl.Vertices[i+2]
					}

					pa := esf.Point{X: a.X, Y: a.Y, Z: a.Z}
					pb := esf.Point{X: b.X, Y: b.Y, Z: b.Z}
					pc := esf.Point{X: c.X, Y: c.Y, Z: c.Z}
					sp.Transform(&pa)
					sp.Transform(&pb)
					sp.Transform(&pc)

					ar, ag, ab := ensureColor(a.R, a.G, a.B)
					br, bg, bb := ensureColor(b.R, b.G, b.B)
					cr, cg, cb := ensureColor(c.R, c.G, c.B)

					// Apply baked static lighting (per-vertex RGBA from ColorBuffer).
					if len(sp.BakedColors) > 0 {
						var ai, bi, ci int
						if i%2 == 0 {
							ai, bi, ci = bakedOff+i+1, bakedOff+i, bakedOff+i+2
						} else {
							ai, bi, ci = bakedOff+i, bakedOff+i+1, bakedOff+i+2
						}
						if ci < len(sp.BakedColors) {
							bc := sp.BakedColors[ai]
							ar *= float32(bc.R) / 255
							ag *= float32(bc.G) / 255
							ab *= float32(bc.B) / 255
							bc = sp.BakedColors[bi]
							br *= float32(bc.R) / 255
							bg *= float32(bc.G) / 255
							bb *= float32(bc.B) / 255
							bc = sp.BakedColors[ci]
							cr *= float32(bc.R) / 255
							cg *= float32(bc.G) / 255
							cb *= float32(bc.B) / 255
						}
					}

					for li, layer := range layers {
						// Apply UVTransform: newU = U*m[0] + V*m[3] + m[6]
						//                    newV = U*m[1] + V*m[4] + m[7]
						// Zero diagonal means identity (no transform).
						m := layer.UVT
						if m[0] == 0 && m[4] == 0 {
							m[0] = 1
							m[4] = 1
						}
						u0 := a.U*m[0] + a.V*m[3] + m[6]
						v0 := a.U*m[1] + a.V*m[4] + m[7]
						u1 := b.U*m[0] + b.V*m[3] + m[6]
						v1 := b.U*m[1] + b.V*m[4] + m[7]
						u2 := c.U*m[0] + c.V*m[3] + m[6]
						v2 := c.U*m[1] + c.V*m[4] + m[7]

						tri := Triangle{
							X0: pa.X, Y0: pa.Y, Z0: pa.Z, U0: u0, V0: v0, NX0: a.NX, NY0: a.NY, NZ0: a.NZ, R0: ar, G0: ag, B0: ab, A0: a.A,
							X1: pb.X, Y1: pb.Y, Z1: pb.Z, U1: u1, V1: v1, NX1: b.NX, NY1: b.NY, NZ1: b.NZ, R1: br, G1: bg, B1: bb, A1: b.A,
							X2: pc.X, Y2: pc.Y, Z2: pc.Z, U2: u2, V2: v2, NX2: c.NX, NY2: c.NY, NZ2: c.NZ, R2: cr, G2: cg, B2: cb, A2: c.A,
							TexID: layer.TexID,
							Layer: int32(li),
							URate: layer.URate,
							VRate: layer.VRate,
						}

						if isDegenerate(tri) {
							continue
						}

						// Flora sprites are collected separately for alpha-blended rendering.
						if sp.IsFlora {
							zt.FloraTriangles = append(zt.FloraTriangles, tri)
						} else {
							addTriToZone(zt, tri)
						}
					}
				}
				bakedOff += nv
			}
			continue
		}

		// CollBuffer visual fallback (solid vertex colors) — only when no PrimBuffer.
		if cbErr == nil && cb != nil {
			for _, list := range cb.Lists {
				nv := len(list.Vertices)
				if nv < 3 {
					continue
				}
				for i := 0; i < nv-2; i++ {
					var a, b, c esf.CollVertex
					if i%2 == 0 {
						a, b, c = list.Vertices[i+1], list.Vertices[i], list.Vertices[i+2]
					} else {
						a, b, c = list.Vertices[i], list.Vertices[i+1], list.Vertices[i+2]
					}

					pa := esf.Point{X: a.X, Y: a.Y, Z: a.Z}
					pbl := esf.Point{X: b.X, Y: b.Y, Z: b.Z}
					pc := esf.Point{X: c.X, Y: c.Y, Z: c.Z}
					sp.Transform(&pa)
					sp.Transform(&pbl)
					sp.Transform(&pc)

					// Earthy green fallback color, upward normal.
					const fr, fg, fb float32 = 0.48, 0.61, 0.35

					tri := Triangle{
						X0: pa.X, Y0: pa.Y, Z0: pa.Z, U0: 0, V0: 0, NX0: 0, NY0: 1, NZ0: 0, R0: fr, G0: fg, B0: fb, A0: 1,
						X1: pbl.X, Y1: pbl.Y, Z1: pbl.Z, U1: 0, V1: 0, NX1: 0, NY1: 1, NZ1: 0, R1: fr, G1: fg, B1: fb, A1: 1,
						X2: pc.X, Y2: pc.Y, Z2: pc.Z, U2: 0, V2: 0, NX2: 0, NY2: 1, NZ2: 0, R2: fr, G2: fg, B2: fb, A2: 1,
						TexID: 0,
					}

					if isDegenerate(tri) {
						continue
					}

					addTriToZone(zt, tri)
				}
			}
		}
	}

	// Extract effect volumes (fog, dust, leaves, light shafts) from zone actors.
	if evs, evErr := zone.GetEffectVolumes(file); evErr == nil && len(evs) > 0 {
		zt.EffectVolumes = evs
		log.Printf("terrain: found %d effect volumes in zone", len(evs))
	}

	// Extract point lights (torches, lamps) from zone actors.
	if lights, lightErr := zone.GetPointLights(file); lightErr == nil && len(lights) > 0 {
		zt.PointLights = lights
		log.Printf("terrain: found %d point lights in zone", len(lights))
	}

	// Extract zone-placed particle effects (fire, smoke on torches/lamps).
	if zp, zpErr := zone.GetZoneParticles(file); zpErr == nil && len(zp) > 0 {
		zt.ZoneParticles = zp
		log.Printf("terrain: found %d zone particle effects", len(zp))
	}

	// Parse ZoneTree BSP for water/environment detection.
	// PS2 path: Zone → ZoneBase → ZoneTree (type 0x3220).
	if zoneBaseInfo := zone.ObjInfo().Child(esf.TypeZoneBase); zoneBaseInfo != nil {
		if treeInfo := zoneBaseInfo.Child(esf.TypeZoneTree); treeInfo != nil {
			raw := file.RawBytes(treeInfo.Offset, int(treeInfo.Size))
			if tree := parseZoneTree(raw); tree != nil {
				zt.Tree = tree
				// Count water leaves for logging.
				waterLeaves := 0
				for i := range tree.Leaves {
					if tree.Leaves[i].EnvFlags&EnvFlagWater != 0 {
						waterLeaves++
					}
				}
				log.Printf("terrain: ZoneTree loaded — %d nodes, %d leaves (%d water)",
					len(tree.Nodes), len(tree.Leaves), waterLeaves)
			}
		}

		// Parse ZoneRooms for portal-based pathfinding
		rooms := esf.GetZoneRooms(file, zoneBaseInfo)
		if len(rooms) > 0 {
			zt.Rooms = rooms
			totalPortals := 0
			for _, r := range rooms {
				totalPortals += len(r.Portals)
			}
			log.Printf("terrain: loaded %d rooms with %d portals", len(rooms), totalPortals)
		}
	}

	if len(zt.UVRates) > 0 {
		for rk, rates := range zt.UVRates {
			log.Printf("terrain: UV animated texID=0x%x layer=%d uRate=%.4f vRate=%.4f", rk.TexID, rk.Layer, rates[0], rates[1])
		}
	}
	log.Printf("terrain: %d textures, %d prim sprites (%d flora, %d baked-lit), %d coll sprites (%d coll tris), %d multi-layer vertex lists",
		len(zt.Textures), primCount, floraCount, bakedLitCount, collCount, collTriCount, multiLayerCount)
	if len(zt.FloraTriangles) > 0 {
		log.Printf("terrain: %d flora triangles separated (%d flora textures)", len(zt.FloraTriangles), len(zt.FloraTextures))
	}
	log.Printf("terrain: nilSprites=%d noMatPal=%d totalLayers=%d nilSurf=%d nilImg=%d",
		nilSpriteCount, noMatPalCount, totalLayers, nilSurfTotal, nilImgTotal)
	// Log which textures have alpha.
	for texID, img := range zt.Textures {
		hasAlpha := false
		bounds := img.Bounds()
		for y := bounds.Min.Y; y < bounds.Min.Y+4 && y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				_, _, _, a := img.At(x, y).RGBA()
				if a < 0xFFFF {
					hasAlpha = true
					break
				}
			}
			if hasAlpha {
				break
			}
		}
		if hasAlpha {
			log.Printf("terrain: texture 0x%x (%dx%d) HAS ALPHA", texID, bounds.Dx(), bounds.Dy())
		}
	}

	// Save textures to disk for inspection.
	os.MkdirAll("/tmp/eqoa-textures", 0o755)
	for texID, img := range zt.Textures {
		f, err := os.Create(fmt.Sprintf("/tmp/eqoa-textures/tex_%d.png", texID))
		if err == nil {
			png.Encode(f, img)
			f.Close()
		}
	}
	log.Printf("terrain: saved %d textures to /tmp/eqoa-textures/", len(zt.Textures))

	// Load radial flora data (procedural grass/flowers).
	rf, rfErr := zone.GetRadialFlora(file)
	if rfErr != nil {
		log.Printf("terrain: radial flora error: %v", rfErr)
	} else if rf != nil {
		zt.RadialFlora = rf
		// Debug: count flora types in collision mesh
		ftCounts := make(map[int]int)
		for _, ct := range zt.CollTiles {
			for _, tri := range ct.Triangles {
				ftCounts[tri.FloraType]++
			}
		}
		log.Printf("terrain: radial flora — %d models, %d dists, %d sets, collTri floraTypes=%v",
			len(rf.Models), len(rf.Dists), len(rf.Sets), ftCounts)
	}

	// Build navigation mesh for pathfinding
	zt.Nav = BuildNavMesh(zt)
	if zt.Nav != nil {
		edgeCount := 0
		for _, el := range zt.Nav.Edges {
			edgeCount += len(el)
		}
		log.Printf("terrain: navmesh built — %d walkable tris, %d edges, %d stitched, %d components (largest=%d)",
			zt.Nav.TriCount(), edgeCount/2, zt.Nav.Stitched, zt.Nav.Components, zt.Nav.LargestCC)
	}

	if len(zt.Obstacles) > 0 {
		log.Printf("terrain: %d actor obstacles for pathfinding", len(zt.Obstacles))
	}

	return zt, nil
}

// addTriToZone buckets a triangle into the appropriate tile and updates bounds.
func addTriToZone(zt *ZoneTerrain, tri Triangle) {
	cx := (tri.X0 + tri.X1 + tri.X2) / 3
	cz := (tri.Z0 + tri.Z1 + tri.Z2) / 3
	key := TileKey{
		TX: int(math.Floor(float64(cx / TileSize))),
		TZ: int(math.Floor(float64(cz / TileSize))),
	}

	tile, ok := zt.Tiles[key]
	if !ok {
		tile = &Tile{}
		zt.Tiles[key] = tile
	}
	tile.Triangles = append(tile.Triangles, tri)
	zt.NumTris++

	if key.TX < zt.MinTX {
		zt.MinTX = key.TX
	}
	if key.TX > zt.MaxTX {
		zt.MaxTX = key.TX
	}
	if key.TZ < zt.MinTZ {
		zt.MinTZ = key.TZ
	}
	if key.TZ > zt.MaxTZ {
		zt.MaxTZ = key.TZ
	}
}

// ensureColor returns the vertex colors as-is unless all are zero, in which
// case it returns white (1,1,1) so untinted textures remain visible.
func ensureColor(r, g, b float32) (float32, float32, float32) {
	if r == 0 && g == 0 && b == 0 {
		return 1, 1, 1
	}
	return r, g, b
}

func isDegenerate(t Triangle) bool {
	const eps = 0.001
	if close3D(t.X0, t.Y0, t.Z0, t.X1, t.Y1, t.Z1, eps) {
		return true
	}
	if close3D(t.X1, t.Y1, t.Z1, t.X2, t.Y2, t.Z2, eps) {
		return true
	}
	if close3D(t.X0, t.Y0, t.Z0, t.X2, t.Y2, t.Z2, eps) {
		return true
	}
	return false
}

func close3D(x0, y0, z0, x1, y1, z1, eps float32) bool {
	dx := x0 - x1
	dy := y0 - y1
	dz := z0 - z1
	return dx*dx+dy*dy+dz*dz < eps*eps
}

