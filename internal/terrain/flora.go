package terrain

import (
	"image"
	"log"
	"math"
	"runtime"
	"sort"
	"unsafe"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
	"github.com/eqoa/client/internal/frustum"
	"github.com/eqoa/client/internal/gfx"
)

// Radial flora is VIRadialFloraSystem (decomp/src/viradialflorasystem.cpp):
// a 3×3 grid of 40-unit swatches around the camera, each grown once from the
// ground under it and seeded off its sub-cell, so a swatch always grows the
// same plants; drawn within 40 units, farthest first, fading out from 10.
const (
	floraSubCell   = 40   // CalcRadialLoc's sub-cell, in world units
	floraTriCap    = 100  // CreateFloraInsts: q.Init(100, ...)
	floraInstCap   = 500  // CreateFloraInsts: insts.Init(500)
	floraSortCap   = 700  // DrawSwatch: m_numSorted < 700
	floraDrawDist2 = 1600 // DrawSwatch: d < 1600, 40 units
	floraSubMin    = -25  // CalcRadialLoc's clamp, [-25, 24]
	floraSubMax    = 24
)

// floraSlopeCos is the query's cosf(30 * 0.017453292f): a triangle grows
// flora only when its normal is within 30° of up.
var floraSlopeCos = float32(math.Cos(float64(float32(30) * 0.017453292)))

// newlibRand is newlib's rand()/srand(): a 64-bit LCG whose bits 32..62 are
// the result. The engine seeds it per swatch, so the swatch's plants are a
// function of its sub-cell.
type newlibRand struct{ next uint64 }

func (r *newlibRand) srand(seed int32) { r.next = uint64(uint32(seed)) }

func (r *newlibRand) rand() int32 {
	r.next = r.next*6364136223846793005 + 1
	return int32((r.next >> 32) & 0x7FFFFFFF)
}

// virand is the flora TU's VIRAND: ten bits of rand() as a fraction in [0, 1].
func (r *newlibRand) virand() float32 {
	return float32(r.rand()&0x3FF) * (1.0 / 1023.0)
}

// floraLoc is VIRadialFloraLoc: the world-grid cell (row from z, column from
// x), the 40-unit sub-cell within it, and the zone the cell's centre is in.
type floraLoc struct {
	zone int
	cell [2]int // [0] row, [1] column
	sub  [2]int // [0] sub-row (z), [1] sub-column (x), each in [-25, 24]
}

// floraInst is VIRadialFloraInst: the world matrix, the bounding sphere and
// the model it draws.
type floraInst struct {
	world  rl.Matrix // VIMatrix44::Transform(hpr, scale, pos), in raylib's layout
	pos    [3]float32
	sphere [4]float32 // centre, radius
	src    *esf.RadialFloraModel
}

// floraSwatch is VIRadialFloraSwatch: its box over one sub-cell, spanning the
// world's height, the location it was built for, and its plants.
type floraSwatch struct {
	min, max [3]float32
	loc      floraLoc
	insts    []floraInst
	// pending: built before its zone (or the ground under it) was loaded;
	// built again once it is. The console streams before it grows, the
	// client streams asynchronously.
	pending bool
}

// floraModel is one flora sprite on the GPU: a mesh per palette material.
type floraModel struct {
	parts []floraPart
}

type floraPart struct {
	mesh     rl.Mesh
	material rl.Material
	color    [4]float32 // material layer 0 colour
	emissive [3]float32
}

type floraSort struct {
	dist float32
	inst *floraInst
}

// FloraRenderer is the radial flora system.
type FloraRenderer struct {
	loader *Loader   // every loaded zone's ground and flora (nil: the one zone Update is given)
	lights *Renderer // the terrain's lit shader and this frame's light (nil: unlit)
	zt     *ZoneTerrain

	swatches [9]*floraSwatch
	loc      floraLoc
	rebuild  bool
	rng      newlibRand

	models   map[*esf.FloraSprite]*floraModel
	textures map[*image.NRGBA]rl.Texture2D

	unlit       rl.Shader
	unlitLoaded bool
	locUnlitCol int32

	sorted []floraSort

	// Stats of the last swatch built, for the log and the tests.
	lastTris, lastInsts int
}

const floraUnlitVS = `#version 330
in vec3 vertexPosition;
in vec2 vertexTexCoord;
uniform mat4 mvp;
out vec2 fragTexCoord;
void main() {
    fragTexCoord = vertexTexCoord;
    gl_Position = mvp * vec4(vertexPosition, 1.0);
}
`

const floraUnlitFS = `#version 330
in vec2 fragTexCoord;
uniform sampler2D texture0;
uniform vec4 layerColor;
out vec4 finalColor;
void main() {
    vec4 t = texture(texture0, fragTexCoord);
    finalColor = vec4(t.rgb * layerColor.rgb, t.a * layerColor.a);
}
`

func NewFloraRenderer() *FloraRenderer {
	return &FloraRenderer{rebuild: true}
}

// Attach gives the flora system the zone loader, so swatches grow from every
// loaded zone's ground and take each cell's own flora tables, and the terrain
// renderer, whose lit shader and light the flora is drawn with. Without it,
// the flora comes from the one zone Update is handed and is drawn unlit.
func (fr *FloraRenderer) Attach(l *Loader, r *Renderer) {
	fr.loader, fr.lights = l, r
	fr.Reset()
}

// Reset is VIRadialFloraSystem::Reset: every swatch released and the grid
// rebuilt on the next Update. The GPU models and textures go too, since a
// world switch replaces the file they came from.
func (fr *FloraRenderer) Reset() {
	for i := range fr.swatches {
		fr.swatches[i] = nil
	}
	fr.rebuild = true
	for _, m := range fr.models {
		for i := range m.parts {
			rl.UnloadMesh(&m.parts[i].mesh)
			gfx.FreeMaterialMaps(&m.parts[i].material)
		}
	}
	fr.models = nil
	for _, t := range fr.textures {
		rl.UnloadTexture(t)
	}
	fr.textures = nil
	fr.zt = nil
}

func (fr *FloraRenderer) Cleanup() {
	fr.Reset()
	if fr.unlitLoaded {
		rl.UnloadShader(fr.unlit)
		fr.unlitLoaded = false
	}
}

// IsBuilt and UploadBatch are kept for the caller; the GPU work is lazy, per
// model, as swatches need it.
func (fr *FloraRenderer) IsBuilt() bool                    { return true }
func (fr *FloraRenderer) UploadBatch(zt *ZoneTerrain) bool { return true }

// HasFlora reports whether there may be flora to grow: any zone with flora
// tables, once a loader is attached.
func (fr *FloraRenderer) HasFlora(zt *ZoneTerrain) bool {
	if fr.loader != nil {
		return true
	}
	return zt != nil && zt.RadialFlora != nil && len(zt.RadialFlora.Sets) > 0
}

// --- the world grid ---

// floraGrid is the world grid CalcRadialLoc works in (VIWorld's rows,
// columns, cell size and origin, from the WorldBaseHeader).
type floraGrid struct {
	rows, cols int
	cell       float32
	origin     [3]float32
	minY, maxY float32
}

func (fr *FloraRenderer) grid() floraGrid {
	if fr.loader != nil && fr.loader.World != nil && fr.loader.World.Header != nil {
		h := fr.loader.World.Header
		if h.CellSize > 0 {
			return floraGrid{
				rows: int(h.NumRows), cols: int(h.NumCols), cell: h.CellSize,
				origin: [3]float32{h.Origin.X, h.Origin.Y, h.Origin.Z},
				minY:   h.BBox.MinY, maxY: h.BBox.MaxY,
			}
		}
	}
	// No header: one cell, which CalcWorldGridOffset leaves at the origin
	// and CalcRadialLoc does not clamp.
	return floraGrid{rows: 1, cols: 1, cell: 1, minY: -1e5, maxY: 1e5}
}

func (g floraGrid) single() bool { return g.rows == 1 || g.cols == 1 }

// center is VIScene::GetWorldGridCenter(col, row).
func (g floraGrid) center(row, col int) (x, z float32) {
	if g.rows == 1 && g.cols == 1 {
		return 0, 0
	}
	return float32(col)*g.cell + g.origin[0], float32(row)*g.cell + g.origin[2]
}

// calcRadialLoc is VIRadialFloraSystem::CalcRadialLoc over
// VIScene::CalcWorldGridOffset: the cell is truncated toward zero, the
// sub-cell truncated and then stepped down when negative, and clamped to
// [-25, 24] unless the world is a single row or column.
func (fr *FloraRenderer) calcRadialLoc(g floraGrid, x, z float32) floraLoc {
	var loc floraLoc
	var col, row int
	ox, oz := x, z
	if !(g.rows == 1 && g.cols == 1) {
		col = int(x / g.cell)
		row = int(z / g.cell)
		ox = x - float32(col)*g.cell - g.origin[0]
		oz = z - float32(row)*g.cell - g.origin[2]
	}
	loc.cell = [2]int{row, col}
	loc.sub[0] = int(oz / floraSubCell)
	if oz < 0 {
		loc.sub[0]--
	}
	loc.sub[1] = int(ox / floraSubCell)
	if ox < 0 {
		loc.sub[1]--
	}
	if !g.single() {
		loc.sub[0] = min(floraSubMax, max(floraSubMin, loc.sub[0]))
		loc.sub[1] = min(floraSubMax, max(floraSubMin, loc.sub[1]))
	}
	if col >= 0 && col < g.cols && row >= 0 && row < g.rows {
		loc.zone = fr.zoneIndex(g, loc)
	} else {
		loc.zone = -1
	}
	return loc
}

// zoneIndex is CalcZoneIndex: the zone the cell's centre is in.
func (fr *FloraRenderer) zoneIndex(g floraGrid, loc floraLoc) int {
	if fr.loader == nil {
		return 0
	}
	cx, cz := g.center(loc.cell[0], loc.cell[1])
	return fr.loader.FindZone(cx, cz)
}

// zoneTerrain is the loaded zone for an index, or the one zone Update was
// handed when no loader is attached.
func (fr *FloraRenderer) zoneTerrain(idx int) *ZoneTerrain {
	if fr.loader == nil {
		return fr.zt
	}
	if idx < 0 {
		return nil
	}
	return fr.loader.GetZoneTerrain(idx)
}

// --- Update: the 3×3 grid ---

// Update is VIRadialFloraSystem::Update at the camera's x and z: when the
// location moved, swatches still inside the grid slide to their new slot and
// the rest are released; then every empty slot whose cell is on the grid
// gets a fresh swatch.
func (fr *FloraRenderer) Update(camX, camZ float32, zt *ZoneTerrain) {
	if fr.loader == nil && zt != fr.zt {
		// Another zone's tables: VIRadialFloraSystem::Reset.
		fr.zt = zt
		for i := range fr.swatches {
			fr.swatches[i] = nil
		}
		fr.rebuild = true
	}
	g := fr.grid()
	loc := fr.calcRadialLoc(g, camX, camZ)

	if fr.rebuild || loc != fr.loc {
		fr.rebuild = false
		var grid [9]*floraSwatch
		d0 := (fr.loc.sub[0] - loc.sub[0]) + (fr.loc.cell[0]-loc.cell[0])*50
		d1 := (fr.loc.sub[1] - loc.sub[1]) + (fr.loc.cell[1]-loc.cell[1])*50
		k := d0*3 + d1
		for r, i := 0, 0; r < 3; r++ {
			for c := 0; c < 3; c, i = c+1, i+1 {
				if s := fr.swatches[i]; s != nil {
					if r+d0 >= 0 && r+d0 < 3 && c+d1 >= 0 && c+d1 < 3 {
						grid[k+i] = s
					}
					fr.swatches[i] = nil
				}
			}
		}
		fr.swatches = grid
	}

	var l floraLoc
	l.sub[0] = loc.sub[0] - 1
	l.cell[0] = loc.cell[0]
	if !g.single() && l.sub[0] < floraSubMin {
		l.cell[0]--
		l.sub[0] += 50
	}
	for r, i := 0, 0; r < 3; r++ {
		l.sub[1] = loc.sub[1] - 1
		l.cell[1] = loc.cell[1]
		if !g.single() && l.sub[1] < floraSubMin {
			l.cell[1]--
			l.sub[1] += 50
		}
		for c := 0; c < 3; c, i = c+1, i+1 {
			if (fr.swatches[i] == nil || fr.swatches[i].pending) &&
				l.cell[0] >= 0 && l.cell[0] < g.rows && l.cell[1] >= 0 && l.cell[1] < g.cols {
				l.zone = fr.zoneIndex(g, l)
				fr.swatches[i] = fr.createSwatch(g, l)
			}
			l.sub[1]++
			if !g.single() && l.sub[1] > floraSubMax {
				l.sub[1] = floraSubMin
				l.cell[1]++
			}
		}
		l.sub[0]++
		if !g.single() && l.sub[0] > floraSubMax {
			l.sub[0] = floraSubMin
			l.cell[0]++
		}
	}
	fr.loc = loc
}

// createSwatch is CreateSwatch: the box over the sub-cell, spanning the
// world's height, then its plants.
func (fr *FloraRenderer) createSwatch(g floraGrid, loc floraLoc) *floraSwatch {
	cx, cz := g.center(loc.cell[0], loc.cell[1])
	s := &floraSwatch{loc: loc}
	s.min = [3]float32{cx + float32(loc.sub[1])*floraSubCell, g.minY, cz + float32(loc.sub[0])*floraSubCell}
	s.max = [3]float32{s.min[0] + floraSubCell, g.maxY, s.min[2] + floraSubCell}
	fr.createFloraInsts(s)
	return s
}

// --- CreateFloraInsts ---

// floraTri is a VIQueryTri: the flora set (the triangle's flora byte) and the
// three corners of one piece of the triangle clipped to the swatch.
type floraTri struct {
	set        int
	v0, v1, v2 [3]float32
}

// queryTris is VIScene::QueryIntersection with the radial flora filter and a
// VIQueryTriList(100, box, up, 30°): every ground triangle under the swatch
// whose normal is within 30° of up, clipped to the box and fanned, up to 100
// pieces. The swatch's own zone is walked first, then the other loaded zones.
func (fr *FloraRenderer) queryTris(s *floraSwatch) (tris []floraTri, ready bool) {
	var zones []*ZoneTerrain
	if fr.loader == nil {
		if fr.zt != nil {
			zones = append(zones, fr.zt)
		}
	} else {
		if zt := fr.loader.GetZoneTerrain(s.loc.zone); zt != nil {
			zones = append(zones, zt)
		}
		loaded := fr.loader.GetLoadedZones()
		sort.Ints(loaded)
		for _, idx := range loaded {
			if idx != s.loc.zone {
				if zt := fr.loader.GetZoneTerrain(idx); zt != nil {
					zones = append(zones, zt)
				}
			}
		}
	}
	if len(zones) == 0 {
		return nil, false
	}

	tx0 := int(math.Floor(float64(s.min[0] / TileSize)))
	tx1 := int(math.Floor(float64(s.max[0] / TileSize)))
	tz0 := int(math.Floor(float64(s.min[2] / TileSize)))
	tz1 := int(math.Floor(float64(s.max[2] / TileSize)))
	for _, zt := range zones {
		for tz := tz0; tz <= tz1; tz++ {
			for tx := tx0; tx <= tx1; tx++ {
				tile := zt.CollTiles[TileKey{TX: tx, TZ: tz}]
				if tile == nil {
					continue
				}
				for i := range tile.Triangles {
					t := &tile.Triangles[i]
					// VIRadialFloraFilter rejects every actor: rooms only.
					if !t.Room || t.MaxX < s.min[0] || t.MinX > s.max[0] || t.MaxZ < s.min[2] || t.MinZ > s.max[2] {
						continue
					}
					// The PS2's strip order holds this triangle's first two
					// vertices swapped (see collideSphere); the plane and the
					// fan below are built in its order.
					a := [3]float32{t.X1, t.Y1, t.Z1}
					b := [3]float32{t.X0, t.Y0, t.Z0}
					c := [3]float32{t.X2, t.Y2, t.Z2}
					if !floraFacesUp(a, b, c) {
						continue
					}
					set := t.FloraType
					if set == 255 {
						set = -1 // the signed flora byte
					}
					poly := clipToBox([][3]float32{a, b, c}, s.min, s.max)
					for k := 2; k < len(poly); k++ {
						tris = append(tris, floraTri{set: set, v0: poly[0], v1: poly[k-1], v2: poly[k]})
						if len(tris) == floraTriCap {
							return tris, true
						}
					}
				}
			}
		}
	}
	return tris, true
}

// floraFacesUp is VIQueryTriList::Intersect's direction test: the plane of
// (a, b, c), VIPlane's (c−b)×(b−a) normalised, against up and cos 30°.
func floraFacesUp(a, b, c [3]float32) bool {
	u := [3]float32{b[0] - a[0], b[1] - a[1], b[2] - a[2]}
	v := [3]float32{c[0] - b[0], c[1] - b[1], c[2] - b[2]}
	n := [3]float32{v[1]*u[2] - v[2]*u[1], -v[0]*u[2] + v[2]*u[0], v[0]*u[1] - v[1]*u[0]}
	l := float32(math.Sqrt(float64(n[0]*n[0] + n[1]*n[1] + n[2]*n[2])))
	if l == 0 {
		return false
	}
	return n[1]/l >= floraSlopeCos
}

// clipToBox clips a convex polygon to the box's x and z slabs (its y slab is
// the world's height and clips nothing), Sutherland-Hodgman, keeping the
// winding.
func clipToBox(poly [][3]float32, lo, hi [3]float32) [][3]float32 {
	for _, pl := range [4]struct {
		axis int
		v    float32
		keep float32 // +1 keeps p[axis] >= v, -1 keeps p[axis] <= v
	}{{0, lo[0], 1}, {0, hi[0], -1}, {2, lo[2], 1}, {2, hi[2], -1}} {
		if len(poly) == 0 {
			return nil
		}
		var out [][3]float32
		for i := range poly {
			p, q := poly[i], poly[(i+1)%len(poly)]
			dp := (p[pl.axis] - pl.v) * pl.keep
			dq := (q[pl.axis] - pl.v) * pl.keep
			if dp >= 0 {
				out = append(out, p)
			}
			if (dp >= 0) != (dq >= 0) {
				t := dp / (dp - dq)
				out = append(out, [3]float32{p[0] + (q[0]-p[0])*t, p[1] + (q[1]-p[1])*t, p[2] + (q[2]-p[2])*t})
			}
		}
		poly = out
	}
	if len(poly) < 3 {
		return nil
	}
	return poly
}

// createFloraInsts is CreateFloraInsts: seeded off the sub-cell, each query
// triangle grows (int)(area × its set's summed weight) plants — a model by a
// binary search over the set's cumulative weights, a scale of
// base·(1 + var − 2·var·r), a random heading, tilted to the triangle when the
// dist's flags have bit 0, at a random point of the triangle — up to 500.
func (fr *FloraRenderer) createFloraInsts(s *floraSwatch) {
	fr.rng.srand(int32(s.loc.sub[0]*100 + s.loc.sub[1]))

	if fr.loader != nil && s.loc.zone < 0 {
		return // the cell's centre is in no zone: no flora tables
	}
	zt := fr.zoneTerrain(s.loc.zone)
	if zt == nil {
		s.pending = true
		return
	}
	flora := zt.RadialFlora
	if flora == nil || len(flora.Sets) == 0 {
		return
	}
	tris, ready := fr.queryTris(s)
	if !ready {
		s.pending = true
		return
	}
	fr.lastTris = len(tris)

	for k := 0; k < len(tris) && len(s.insts) < floraInstCap; k++ {
		t := &tris[k]
		if t.set == -1 {
			continue
		}
		if t.set < 0 || t.set >= len(flora.Sets) {
			continue // the console does not check; its data never does this
		}
		set := &flora.Sets[t.set]
		if set.Count <= 0 {
			continue
		}

		e1 := sub3(t.v1, t.v0)
		e2 := sub3(t.v2, t.v0)
		c := cross3(e2, e1)
		n := c
		cl := length3(c)
		if cl > 0 {
			n = [3]float32{c[0] / cl, c[1] / cl, c[2] / cl}
		}
		num := int(dot3(c, n) * 0.5 * set.Weight)

		for m := 0; m < num && len(s.insts) < floraInstCap; m++ {
			r := fr.rng.virand()
			lo, hi := int(set.First), int(set.First+set.Count-1)
			mid := lo
			for lo <= hi {
				mid = (lo + hi) >> 1
				d := &flora.Dists[mid]
				if r >= d.Lo && r < d.Hi {
					break
				}
				if r >= d.Hi {
					lo = mid + 1
				} else {
					hi = mid - 1
				}
			}
			dist := &flora.Dists[mid]

			jitter := dist.ScaleVar + 1 - dist.ScaleVar*float32(fr.rng.rand()&0x3FF)*(1.0/1023.0)*2
			scale := max(0, dist.BaseScale*jitter)

			var hpr [3]float32
			hpr[0] = 6.2831855 * fr.rng.virand()
			if dist.Flags&1 != 0 {
				// VIVect3::HPR(h, 0, 0), then a frame on the triangle's normal.
				h := [3]float32{sinf(hpr[0]), 0, cosf(hpr[0])}
				w := cross3(n, h)
				if wl := length3(w); wl > 0 {
					w = [3]float32{w[0] / wl, w[1] / wl, w[2] / wl}
				}
				h = cross3(w, n)
				hpr[1] = -atan2f(h[1], sqrtf(1-h[1]*h[1]))
				hpr[2] = atan2f(w[1], sqrtf(1-w[1]*w[1]))
			}

			u := fr.rng.virand()
			v := fr.rng.virand()
			if u+v > 1 {
				u, v = 1-u, 1-v
			}
			wgt := 1 - u - v
			var pos [3]float32
			for i := 0; i < 3; i++ {
				pos[i] = t.v0[i]*wgt + t.v1[i]*u + t.v2[i]*v
			}

			if dist.Model < 0 || int(dist.Model) >= len(flora.Models) {
				continue
			}
			mdl := &flora.Models[dist.Model]
			if mdl.Mesh == nil {
				continue
			}
			world := floraTransform(hpr, scale, pos)
			cx := mdl.SphereC.X*world.M0 + mdl.SphereC.Y*world.M4 + mdl.SphereC.Z*world.M8 + world.M12
			cy := mdl.SphereC.X*world.M1 + mdl.SphereC.Y*world.M5 + mdl.SphereC.Z*world.M9 + world.M13
			cz := mdl.SphereC.X*world.M2 + mdl.SphereC.Y*world.M6 + mdl.SphereC.Z*world.M10 + world.M14
			s.insts = append(s.insts, floraInst{
				world:  world,
				pos:    pos,
				sphere: [4]float32{cx, cy, cz, scale * mdl.SphereR},
				src:    mdl,
			})
		}
	}
	fr.lastInsts = len(s.insts)
}

// floraTransform is VIMatrix44::Transform(hpr, scale, pos) (vimatrix44.cpp:394),
// whose rows multiply a row vector, laid out as raylib's column-vector matrix.
func floraTransform(hpr [3]float32, scale float32, pos [3]float32) rl.Matrix {
	si, ci := sinf(hpr[0]), cosf(hpr[0])
	sj, cj := sinf(hpr[1]), cosf(hpr[1])
	sh, ch := sinf(hpr[2]), cosf(hpr[2])
	return rl.Matrix{
		M0: (ch*ci + sh*sj*si) * scale, M4: (-sh*ci + ch*sj*si) * scale, M8: (cj * si) * scale, M12: pos[0],
		M1: (sh * cj) * scale, M5: (ch * cj) * scale, M9: (-sj) * scale, M13: pos[1],
		M2: (-ch*si + sh*sj*ci) * scale, M6: (sh*si + ch*sj*ci) * scale, M10: (cj * ci) * scale, M14: pos[2],
		M15: 1,
	}
}

// --- the GPU side ---

// model uploads a flora sprite's prim buffer, once: a mesh per palette
// material with its layer 0 texture and colour. The strips are drawn both
// sides. Each vertex gets an up normal and a black colour with a full sun
// gate: the 0x1230 buffer has no normal and no colour for the lit shader to
// read.
func (fr *FloraRenderer) model(m *esf.RadialFloraModel) *floraModel {
	if m.Sprite == nil || m.Mesh == nil {
		return nil
	}
	if g, ok := fr.models[m.Sprite]; ok {
		return g
	}
	if fr.models == nil {
		fr.models = map[*esf.FloraSprite]*floraModel{}
		fr.textures = map[*image.NRGBA]rl.Texture2D{}
	}
	g := &floraModel{}
	fr.models[m.Sprite] = g

	type layer struct {
		img      *image.NRGBA
		uvt      [9]float32
		color    [4]float32
		emissive [3]float32
	}
	var layers []layer
	if mp := m.MatPal; mp != nil {
		for _, mat := range mp.Materials {
			var l layer
			l.color = [4]float32{1, 1, 1, 1}
			l.uvt = [9]float32{1, 0, 0, 0, 1}
			if mat != nil {
				l.emissive = [3]float32{float32(mat.EmissiveCol[0]) / 255, float32(mat.EmissiveCol[1]) / 255, float32(mat.EmissiveCol[2]) / 255}
				if len(mat.Layers) > 0 && mat.Layers[0] != nil {
					ly := mat.Layers[0]
					if ly.Surface != nil {
						l.img = ly.Surface.Image
					}
					if ly.UVTransform[0] != 0 || ly.UVTransform[4] != 0 {
						l.uvt = ly.UVTransform
					}
					l.color = [4]float32{float32(ly.Color[0]) / 255, float32(ly.Color[1]) / 255, float32(ly.Color[2]) / 255, float32(ly.Color[3]) / 255}
				}
			}
			layers = append(layers, l)
		}
	}

	byMat := map[int][]float32{} // material → x,y,z,u,v per triangle corner
	var order []int
	for _, p := range m.Mesh.Prims {
		if _, ok := byMat[p.Material]; !ok {
			order = append(order, p.Material)
		}
		for i := 0; i+2 < len(p.Verts); i++ {
			for _, v := range [3]esf.FloraVertex{p.Verts[i], p.Verts[i+1], p.Verts[i+2]} {
				byMat[p.Material] = append(byMat[p.Material], v.X, v.Y, v.Z, v.U, v.V)
			}
		}
	}
	for _, mi := range order {
		data := byMat[mi]
		if len(data) == 0 {
			continue
		}
		l := layer{color: [4]float32{1, 1, 1, 1}, uvt: [9]float32{1, 0, 0, 0, 1}}
		if mi >= 0 && mi < len(layers) {
			l = layers[mi]
		}
		nv := len(data) / 5
		verts := make([]float32, 0, nv*3)
		uvs := make([]float32, 0, nv*2)
		norms := make([]float32, 0, nv*3)
		cols := make([]uint8, 0, nv*4)
		for i := 0; i < nv; i++ {
			d := data[i*5:]
			u := d[3]*l.uvt[0] + d[4]*l.uvt[3] + l.uvt[6]
			v := d[3]*l.uvt[1] + d[4]*l.uvt[4] + l.uvt[7]
			verts = append(verts, d[0], d[1], d[2])
			uvs = append(uvs, u, v)
			norms = append(norms, 0, 1, 0)
			cols = append(cols, 0, 0, 0, 255)
		}
		mesh := rl.Mesh{
			VertexCount:   int32(nv),
			TriangleCount: int32(nv / 3),
			Vertices:      (*float32)(unsafe.Pointer(&verts[0])),
			Texcoords:     (*float32)(unsafe.Pointer(&uvs[0])),
			Normals:       (*float32)(unsafe.Pointer(&norms[0])),
			Colors:        (*uint8)(unsafe.Pointer(&cols[0])),
		}
		rl.UploadMesh(&mesh, false)
		runtime.KeepAlive(verts)
		runtime.KeepAlive(uvs)
		runtime.KeepAlive(norms)
		runtime.KeepAlive(cols)
		// The data is on the GPU; drop raylib's pointers into Go memory.
		mesh.Vertices, mesh.Texcoords, mesh.Normals, mesh.Colors = nil, nil, nil, nil

		mat := rl.LoadMaterialDefault()
		if l.img != nil {
			rl.SetMaterialTexture(&mat, rl.MapDiffuse, fr.texture(l.img))
		}
		g.parts = append(g.parts, floraPart{mesh: mesh, material: mat, color: l.color, emissive: l.emissive})
	}
	return g
}

// texture uploads an image once, the way the terrain renderer does.
func (fr *FloraRenderer) texture(img *image.NRGBA) rl.Texture2D {
	if t, ok := fr.textures[img]; ok {
		return t
	}
	rlImg := rl.NewImageFromImage(img)
	t := rl.LoadTextureFromImage(rlImg)
	rl.UnloadImage(rlImg)
	rl.GenTextureMipmaps(&t)
	rl.SetTextureFilter(t, rl.FilterTrilinear)
	rl.SetTextureWrap(t, rl.WrapRepeat)
	fr.textures[img] = t
	return t
}

// --- Render ---

// Draw is VIRadialFloraSystem::Render and DrawSwatch: every plant of every
// swatch the frustum keeps, within 40 units of the viewer and inside the
// frustum, up to 700, drawn farthest first with alpha 1 to 10 units and
// 1 − (d − 10)/30 beyond. The materials blend (layer 0 is mode 3 on every
// shipped flora model), so nothing writes depth.
func (fr *FloraRenderer) Draw(camX, camY, camZ float32, f interface{}) {
	fz, _ := f.(*frustum.Frustum)
	fr.sorted = fr.sorted[:0]
	for _, s := range fr.swatches {
		if s == nil || len(s.insts) == 0 {
			continue
		}
		if fz != nil && !fz.TestAABB(s.min[0], s.min[1], s.min[2], s.max[0], s.max[1], s.max[2]) {
			continue
		}
		for j := range s.insts {
			if len(fr.sorted) >= floraSortCap {
				break
			}
			in := &s.insts[j]
			dx, dy, dz := camX-in.pos[0], camY-in.pos[1], camZ-in.pos[2]
			d := dx*dx + dy*dy + dz*dz
			if d < floraDrawDist2 && (fz == nil || fz.TestSphere(in.sphere[0], in.sphere[1], in.sphere[2], in.sphere[3])) {
				fr.sorted = append(fr.sorted, floraSort{dist: sqrtf(d), inst: in})
			}
		}
	}
	if len(fr.sorted) == 0 {
		return
	}
	sort.Slice(fr.sorted, func(i, j int) bool { return fr.sorted[i].dist < fr.sorted[j].dist })

	lit := fr.lights != nil && fr.lights.litLoaded && fr.lights.LightingOn
	if !lit && !fr.unlitLoaded {
		fr.unlit = rl.LoadShaderFromMemory(floraUnlitVS, floraUnlitFS)
		fr.locUnlitCol = rl.GetShaderLocation(fr.unlit, "layerColor")
		fr.unlitLoaded = true
	}
	if lit {
		r := fr.lights
		rl.SetShaderValue(r.litShader, r.locBlendMode, []float32{math.Float32frombits(3)}, rl.ShaderUniformInt)
		rl.SetShaderValue(r.litShader, r.locUvOffsetLit, []float32{0, 0}, rl.ShaderUniformVec2)
	}

	rl.DisableBackfaceCulling()
	rl.DisableDepthMask()
	rl.BeginBlendMode(rl.BlendAlpha)
	for i := len(fr.sorted) - 1; i >= 0; i-- {
		e := fr.sorted[i]
		a := float32(1)
		if e.dist > 10 {
			a = 1 - (e.dist-10)*(1.0/30.0)
		}
		g := fr.model(e.inst.src)
		if g == nil {
			continue
		}
		for pi := range g.parts {
			p := &g.parts[pi]
			col := [4]float32{p.color[0], p.color[1], p.color[2], p.color[3] * a}
			mat := p.material
			if lit {
				r := fr.lights
				mat.Shader = r.litShader
				amb := [3]float32{r.ambient[0] + p.emissive[0], r.ambient[1] + p.emissive[1], r.ambient[2] + p.emissive[2]}
				rl.SetShaderValue(r.litShader, r.locAmbient, amb[:], rl.ShaderUniformVec3)
				rl.SetShaderValue(r.litShader, r.locLayerColor, col[:], rl.ShaderUniformVec4)
			} else {
				mat.Shader = fr.unlit
				rl.SetShaderValue(fr.unlit, fr.locUnlitCol, col[:], rl.ShaderUniformVec4)
			}
			rl.DrawMesh(p.mesh, mat, e.inst.world)
		}
	}
	rl.EndBlendMode()
	rl.EnableDepthMask()
	rl.EnableBackfaceCulling()
}

// --- float32 helpers, as the engine computes ---

func sinf(a float32) float32          { return float32(math.Sin(float64(a))) }
func cosf(a float32) float32          { return float32(math.Cos(float64(a))) }
func sqrtf(a float32) float32         { return float32(math.Sqrt(float64(a))) }
func atan2f(y, x float32) float32     { return float32(math.Atan2(float64(y), float64(x))) }
func sub3(a, b [3]float32) [3]float32 { return [3]float32{a[0] - b[0], a[1] - b[1], a[2] - b[2]} }
func dot3(a, b [3]float32) float32    { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }
func length3(a [3]float32) float32    { return sqrtf(dot3(a, a)) }

// cross3 is the flora TU's Cross(a, b): a × b.
func cross3(a, b [3]float32) [3]float32 {
	return [3]float32{a[1]*b[2] - a[2]*b[1], -a[0]*b[2] + a[2]*b[0], a[0]*b[1] - a[1]*b[0]}
}

var _ = log.Printf
