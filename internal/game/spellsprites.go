package game

import (
	"image"
	"log"
	"math"
	"runtime"
	"unsafe"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

// SpellSpriteGroup holds one GPU mesh + material for a sprite sub-mesh.
type SpellSpriteGroup struct {
	Mesh     rl.Mesh
	Material rl.Material
}

// SpellSpriteModel holds the loaded GPU resources for one spell sprite.
type SpellSpriteModel struct {
	DictID int32
	Groups []SpellSpriteGroup
}

// SpellSpriteSystem manages spell sprite loading, caching, and rendering.
type SpellSpriteSystem struct {
	db       *SpellFXDB
	cache    map[int32]*SpellSpriteModel // DictID → loaded model
	texCache map[int32]rl.Texture2D      // Surface DictID → GPU texture
	shader   rl.Shader                   // alpha blend shader
}

// NewSpellSpriteSystem creates a new sprite system.
func NewSpellSpriteSystem(db *SpellFXDB, shader rl.Shader) *SpellSpriteSystem {
	return &SpellSpriteSystem{
		db:       db,
		cache:    make(map[int32]*SpellSpriteModel),
		texCache: make(map[int32]rl.Texture2D),
		shader:   shader,
	}
}

// LoadSprite loads a sprite by DictID from SPELLFX.ESF, caching the result.
func (ss *SpellSpriteSystem) LoadSprite(dictID int32) *SpellSpriteModel {
	if ss == nil || ss.db == nil || dictID == 0 {
		return nil
	}
	if m, ok := ss.cache[dictID]; ok {
		return m
	}

	f := ss.db.File()
	if f == nil {
		return nil
	}

	obj, err := f.FindObject(dictID)
	if err != nil || obj == nil {
		ss.cache[dictID] = nil // negative cache
		return nil
	}

	var model *SpellSpriteModel
	switch s := obj.(type) {
	case *esf.SimpleSprite:
		model = ss.loadSimpleSprite(f, s, dictID)
	case *esf.Surface:
		model = ss.loadSurfaceBillboard(s, dictID)
	default:
		log.Printf("spellsprites: DictID 0x%08X is unsupported type %T", uint32(dictID), obj)
		ss.cache[dictID] = nil
		return nil
	}

	ss.cache[dictID] = model
	if model != nil {
		log.Printf("spellsprites: loaded DictID 0x%08X: %d groups", uint32(dictID), len(model.Groups))
	}
	return model
}

// loadSimpleSprite builds GPU meshes from a SimpleSprite's PrimBuffer.
func (ss *SpellSpriteSystem) loadSimpleSprite(f *esf.ObjFile, sprite *esf.SimpleSprite, dictID int32) *SpellSpriteModel {
	pb, err := sprite.GetPrimBuffer(f)
	if err != nil || pb == nil {
		return nil
	}

	mp := pb.MatPal
	if mp == nil {
		mp, _ = sprite.GetMatPal(f)
	}

	// Collect triangles by material
	groups := make(map[int][]spellTriVert)
	matSurfaces := make(map[int]*esf.Surface)

	for _, vl := range pb.VertexLists {
		if len(vl.Vertices) < 3 {
			continue
		}
		if _, seen := matSurfaces[vl.Material]; !seen {
			matSurfaces[vl.Material] = ss.resolveSurface(f, mp, vl.Material)
		}
		tris := destripSpellVertices(vl.Vertices)
		groups[vl.Material] = append(groups[vl.Material], tris...)
	}

	model := &SpellSpriteModel{DictID: dictID}
	for matIdx, verts := range groups {
		n := len(verts)
		if n < 3 {
			continue
		}

		positions := make([]float32, n*3)
		uvs := make([]float32, n*2)
		normals := make([]float32, n*3)
		colors := make([]uint8, n*4)

		for i, v := range verts {
			positions[i*3+0] = v.X
			positions[i*3+1] = v.Y
			positions[i*3+2] = v.Z
			uvs[i*2+0] = v.U
			uvs[i*2+1] = v.V
			normals[i*3+0] = v.NX
			normals[i*3+1] = v.NY
			normals[i*3+2] = v.NZ
			colors[i*4+0] = floatToU8(v.R)
			colors[i*4+1] = floatToU8(v.G)
			colors[i*4+2] = floatToU8(v.B)
			colors[i*4+3] = floatToU8(v.A)
		}

		mesh := rl.Mesh{
			VertexCount:   int32(n),
			TriangleCount: int32(n / 3),
			Vertices:      (*float32)(unsafe.Pointer(&positions[0])),
			Texcoords:     (*float32)(unsafe.Pointer(&uvs[0])),
			Normals:       (*float32)(unsafe.Pointer(&normals[0])),
			Colors:        (*uint8)(unsafe.Pointer(&colors[0])),
		}
		rl.UploadMesh(&mesh, false)
		runtime.KeepAlive(positions)
		runtime.KeepAlive(uvs)
		runtime.KeepAlive(normals)
		runtime.KeepAlive(colors)
		mesh.Vertices = nil
		mesh.Texcoords = nil
		mesh.Normals = nil
		mesh.Colors = nil

		mat := rl.LoadMaterialDefault()
		if ss.shader.ID > 0 {
			mat.Shader = ss.shader
		}
		surf := matSurfaces[matIdx]
		tex := ss.ensureTexture(surf)
		if tex.ID > 0 {
			rl.SetMaterialTexture(&mat, rl.MapDiffuse, tex)
		}

		model.Groups = append(model.Groups, SpellSpriteGroup{
			Mesh:     mesh,
			Material: mat,
		})
	}

	if len(model.Groups) == 0 {
		return nil
	}
	return model
}

// loadSurfaceBillboard creates a billboard quad from a Surface texture.
func (ss *SpellSpriteSystem) loadSurfaceBillboard(surf *esf.Surface, dictID int32) *SpellSpriteModel {
	tex := ss.ensureTexture(surf)
	if tex.ID == 0 {
		return nil
	}

	// Build a 2-triangle quad: 2x2 units centered at origin, facing +Y
	// Will be oriented as billboard at render time.
	positions := []float32{
		-1, 0, -1,
		1, 0, -1,
		1, 0, 1,
		-1, 0, -1,
		1, 0, 1,
		-1, 0, 1,
	}
	uvs := []float32{
		0, 1,
		1, 1,
		1, 0,
		0, 1,
		1, 0,
		0, 0,
	}
	normals := []float32{
		0, 1, 0,
		0, 1, 0,
		0, 1, 0,
		0, 1, 0,
		0, 1, 0,
		0, 1, 0,
	}
	colors := []uint8{
		255, 255, 255, 255,
		255, 255, 255, 255,
		255, 255, 255, 255,
		255, 255, 255, 255,
		255, 255, 255, 255,
		255, 255, 255, 255,
	}

	mesh := rl.Mesh{
		VertexCount:   6,
		TriangleCount: 2,
		Vertices:      (*float32)(unsafe.Pointer(&positions[0])),
		Texcoords:     (*float32)(unsafe.Pointer(&uvs[0])),
		Normals:       (*float32)(unsafe.Pointer(&normals[0])),
		Colors:        (*uint8)(unsafe.Pointer(&colors[0])),
	}
	rl.UploadMesh(&mesh, false)
	runtime.KeepAlive(positions)
	runtime.KeepAlive(uvs)
	runtime.KeepAlive(normals)
	runtime.KeepAlive(colors)
	mesh.Vertices = nil
	mesh.Texcoords = nil
	mesh.Normals = nil
	mesh.Colors = nil

	mat := rl.LoadMaterialDefault()
	if ss.shader.ID > 0 {
		mat.Shader = ss.shader
	}
	rl.SetMaterialTexture(&mat, rl.MapDiffuse, tex)

	return &SpellSpriteModel{
		DictID: dictID,
		Groups: []SpellSpriteGroup{{Mesh: mesh, Material: mat}},
	}
}

// resolveSurface finds a Surface from a MaterialPalette by material index.
func (ss *SpellSpriteSystem) resolveSurface(f *esf.ObjFile, mp *esf.MaterialPalette, matIdx int) *esf.Surface {
	if mp == nil || matIdx < 0 || matIdx >= len(mp.Materials) {
		return nil
	}
	mat := mp.Materials[matIdx]
	if mat == nil || len(mat.Layers) == 0 || mat.Layers[0].Surface == nil {
		return nil
	}
	return mat.Layers[0].Surface
}

// ensureTexture loads a Surface's image into GPU, caching by DictID.
func (ss *SpellSpriteSystem) ensureTexture(surf *esf.Surface) rl.Texture2D {
	if surf == nil || surf.Image == nil {
		return rl.Texture2D{}
	}
	if tex, ok := ss.texCache[surf.DictID]; ok {
		return tex
	}

	// Scale PS2 alpha (0-128 → 0-255)
	img := scalePS2Alpha(surf.Image)
	rlImg := rl.NewImageFromImage(img)
	tex := rl.LoadTextureFromImage(rlImg)
	rl.UnloadImage(rlImg)
	rl.SetTextureFilter(tex, rl.FilterBilinear)
	rl.SetTextureWrap(tex, rl.WrapClamp)

	ss.texCache[surf.DictID] = tex
	return tex
}

// scalePS2Alpha converts PS2 alpha (0-128) to standard (0-255).
func scalePS2Alpha(src *image.NRGBA) *image.NRGBA {
	bounds := src.Bounds()
	dst := image.NewNRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			i := src.PixOffset(x, y)
			r := src.Pix[i+0]
			g := src.Pix[i+1]
			b := src.Pix[i+2]
			a := src.Pix[i+3]
			a2 := int(a) * 255 / 128
			if a2 > 255 {
				a2 = 255
			}
			j := dst.PixOffset(x, y)
			dst.Pix[j+0] = r
			dst.Pix[j+1] = g
			dst.Pix[j+2] = b
			dst.Pix[j+3] = uint8(a2)
		}
	}
	return dst
}

// spellTriVert holds a single triangle vertex for spell sprite meshes.
type spellTriVert struct {
	X, Y, Z    float32
	U, V       float32
	NX, NY, NZ float32
	R, G, B, A float32
}

// destripSpellVertices converts a triangle strip to individual triangles.
func destripSpellVertices(strip []esf.Vertex) []spellTriVert {
	numTris := len(strip) - 2
	if numTris <= 0 {
		return nil
	}
	out := make([]spellTriVert, 0, numTris*3)
	for i := 0; i < numTris; i++ {
		v0, v1, v2 := strip[i], strip[i+1], strip[i+2]
		// Check degenerate
		if v0.X == v1.X && v0.Y == v1.Y && v0.Z == v1.Z {
			continue
		}
		if v1.X == v2.X && v1.Y == v2.Y && v1.Z == v2.Z {
			continue
		}
		if v0.X == v2.X && v0.Y == v2.Y && v0.Z == v2.Z {
			continue
		}
		// Swap winding on even triangles (PS2 CW → OpenGL CCW)
		if i%2 == 0 {
			v0, v1 = v1, v0
		}
		for _, v := range []esf.Vertex{v0, v1, v2} {
			out = append(out, spellTriVert{
				X: v.X, Y: v.Y, Z: v.Z,
				U: v.U, V: v.V,
				NX: v.NX, NY: v.NY, NZ: v.NZ,
				R: v.R, G: v.G, B: v.B, A: v.A,
			})
		}
	}
	return out
}

// floatToU8 converts a float32 (0-1) to uint8 (0-255).
func floatToU8(f float32) uint8 {
	v := int(f * 255)
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// DrawSprite renders a spell sprite model at the given position with opacity.
// PS2 ProcessCreateSprite inherits the actor's node transform, so the sprite
// rotates with the character's facing. facing = Y-axis rotation in radians.
func (ss *SpellSpriteSystem) DrawSprite(model *SpellSpriteModel, x, y, z, facing, opacity float32) {
	if ss == nil || model == nil {
		return
	}

	cosF := float32(math.Cos(float64(facing)))
	sinF := float32(math.Sin(float64(facing)))
	// Y-rotation matrix (C-convention, same as render.go DrawEntity)
	transform := rl.Matrix{
		M0: cosF, M4: 0, M8: sinF, M12: x,
		M1: 0, M5: 1, M9: 0, M13: y,
		M2: -sinF, M6: 0, M10: cosF, M14: z,
		M3: 0, M7: 0, M11: 0, M15: 1,
	}

	for i := range model.Groups {
		g := &model.Groups[i]
		// Modulate alpha via material color
		maps := (*[32]rl.MaterialMap)(unsafe.Pointer(g.Material.Maps))
		origColor := maps[rl.MapDiffuse].Color
		a := uint8(float32(origColor.A) * opacity)
		maps[rl.MapDiffuse].Color = rl.Color{R: origColor.R, G: origColor.G, B: origColor.B, A: a}
		rl.DrawMesh(g.Mesh, g.Material, transform)
		maps[rl.MapDiffuse].Color = origColor
	}
}

// Cleanup unloads all GPU resources.
func (ss *SpellSpriteSystem) Cleanup() {
	if ss == nil {
		return
	}
	for _, m := range ss.cache {
		if m == nil {
			continue
		}
		for i := range m.Groups {
			rl.UnloadMesh(&m.Groups[i].Mesh)
		}
	}
	for _, tex := range ss.texCache {
		if tex.ID > 0 {
			rl.UnloadTexture(tex)
		}
	}
	ss.cache = nil
	ss.texCache = nil
}
