package terrain

import (
	"image"
	"log"
	"math"
	"runtime"
	"unsafe"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

const (
	floraChunkSize = 64.0  // world units per spatial chunk
	floraCullDist  = 100.0 // only draw chunks within this radius of player
	floraCullDist2 = floraCullDist * floraCullDist
)

type floraChunkKey struct {
	cx, cz int
}

// floraChunkMesh is one GPU mesh for a single model within a spatial chunk.
type floraChunkMesh struct {
	mesh     rl.Mesh
	material rl.Material
	uploaded bool
	triCount int
	modelIdx int
}

// floraChunk holds all GPU meshes for one spatial region.
type floraChunk struct {
	centerX, centerZ float32
	meshes           []floraChunkMesh
}

// FloraRenderer manages zone-wide procedural flora in spatial chunks.
type FloraRenderer struct {
	shader       rl.Shader
	shaderLoaded bool
	textures     []rl.Texture2D
	quadVerts    [][]float32
	quadUVs      [][]float32
	chunks       map[floraChunkKey]*floraChunk
	texUploaded  bool
	built        bool
	terrain      *ZoneTerrain
}

const floraVS = `#version 330
in vec3 vertexPosition;
in vec2 vertexTexCoord;
in vec4 vertexColor;
uniform mat4 mvp;
out vec2 fragTexCoord;
out vec4 fragColor;
void main() {
    fragTexCoord = vertexTexCoord;
    fragColor = vertexColor;
    gl_Position = mvp * vec4(vertexPosition, 1.0);
}
`

const floraFS = `#version 330
in vec2 fragTexCoord;
in vec4 fragColor;
uniform sampler2D texture0;
uniform vec4 colDiffuse;
out vec4 finalColor;
void main() {
    vec4 texel = texture(texture0, fragTexCoord);
    if (texel.a < 0.1) discard;
    // Undo dark fringe from filtering transparent pixels with black RGB
    vec3 rgb = texel.rgb / max(texel.a, 0.15);
    rgb *= colDiffuse.rgb * fragColor.rgb;
    finalColor = vec4(rgb, 0.5);
}
`

func floraHash(seed uint32) uint32 {
	seed = (seed ^ (seed >> 16)) * 0x45d9f3b
	seed = (seed ^ (seed >> 16)) * 0x45d9f3b
	return seed ^ (seed >> 16)
}

func floraHashF(seed uint32) float32 {
	return float32(seed&0x7FFFFF) / float32(0x800000)
}

func NewFloraRenderer() *FloraRenderer {
	return &FloraRenderer{}
}

func (fr *FloraRenderer) Reset() {
	fr.unloadAllChunks()
	for _, tex := range fr.textures {
		rl.UnloadTexture(tex)
	}
	fr.textures = nil
	fr.texUploaded = false
	fr.built = false
	fr.quadVerts = nil
	fr.quadUVs = nil
	fr.terrain = nil
}

func (fr *FloraRenderer) unloadAllChunks() {
	for _, chunk := range fr.chunks {
		for i := range chunk.meshes {
			if chunk.meshes[i].uploaded {
				rl.UnloadMesh(&chunk.meshes[i].mesh)
			}
		}
	}
	fr.chunks = nil
}

func (fr *FloraRenderer) Cleanup() {
	fr.unloadAllChunks()
	if fr.shaderLoaded {
		rl.UnloadShader(fr.shader)
		fr.shaderLoaded = false
	}
	for _, tex := range fr.textures {
		rl.UnloadTexture(tex)
	}
	fr.textures = nil
}

func (fr *FloraRenderer) IsBuilt() bool {
	return fr.texUploaded
}

func (fr *FloraRenderer) HasFlora(zt *ZoneTerrain) bool {
	return zt != nil && zt.RadialFlora != nil && len(zt.RadialFlora.Models) > 0
}

func buildQuadTemplate(bbox esf.Box) (verts []float32, uvs []float32) {
	w := (bbox.MaxX - bbox.MinX) * 0.5
	h := bbox.MaxY - bbox.MinY
	baseY := bbox.MinY

	type quad struct{ x0, z0, x1, z1 float32 }
	quads := []quad{
		{-w, 0, w, 0},
		{0, -w, 0, w},
	}

	verts = make([]float32, 0, 36)
	uvs = make([]float32, 0, 24)

	for _, q := range quads {
		verts = append(verts,
			q.x0, baseY, q.z0, q.x1, baseY, q.z1, q.x1, baseY+h, q.z1,
		)
		uvs = append(uvs, 0, 1, 1, 1, 1, 0)

		verts = append(verts,
			q.x0, baseY, q.z0, q.x1, baseY+h, q.z1, q.x0, baseY+h, q.z0,
		)
		uvs = append(uvs, 0, 1, 1, 0, 0, 0)
	}
	return
}

func (fr *FloraRenderer) UploadBatch(zt *ZoneTerrain) bool {
	if fr.texUploaded {
		return true
	}
	if zt == nil || zt.RadialFlora == nil || len(zt.RadialFlora.Models) == 0 {
		fr.texUploaded = true
		return true
	}

	if !fr.shaderLoaded {
		fr.shader = rl.LoadShaderFromMemory(floraVS, floraFS)
		fr.shaderLoaded = true
	}

	rf := zt.RadialFlora
	fr.textures = make([]rl.Texture2D, len(rf.Models))
	fr.quadVerts = make([][]float32, len(rf.Models))
	fr.quadUVs = make([][]float32, len(rf.Models))

	for i, model := range rf.Models {
		if model.Texture != nil {
			img := model.Texture
			if isAlphaOnlyTexture(img) {
				img = alphaToWhiteRGBKeepAlpha(img)
			}
			rlImg := rl.NewImageFromImage(img)
			tex := rl.LoadTextureFromImage(rlImg)
			rl.UnloadImage(rlImg)
			rl.GenTextureMipmaps(&tex)
			rl.SetTextureFilter(tex, rl.FilterTrilinear)
			rl.SetTextureWrap(tex, rl.WrapRepeat)
			fr.textures[i] = tex
		}
		fr.quadVerts[i], fr.quadUVs[i] = buildQuadTemplate(model.BBox)
	}

	fr.terrain = zt
	fr.texUploaded = true
	log.Printf("flora: uploaded %d model textures", len(fr.textures))
	return true
}

type floraInstance struct {
	x, y, z  float32
	scale    float32
	rotY     float32
	modelIdx int
}

// isGroundTriangle checks if a collision triangle is at ground level by comparing
// its center Y to the lowest walkable surface at that XZ. Rejects roofs/canopies/walls.
const floraElevationThreshold float32 = 3.0

func isGroundTriangle(zt *ZoneTerrain, tri *CollTriangle) bool {
	cx := (tri.X0 + tri.X1 + tri.X2) / 3.0
	cy := (tri.Y0 + tri.Y1 + tri.Y2) / 3.0
	cz := (tri.Z0 + tri.Z1 + tri.Z2) / 3.0
	lowestY, ok := zt.SampleLowestHeight(cx, cz)
	if !ok {
		return true
	}
	return cy-lowestY < floraElevationThreshold
}

// renderTriArea returns the area of a render Triangle.
func renderTriArea(tri *Triangle) float32 {
	e1x := tri.X1 - tri.X0
	e1y := tri.Y1 - tri.Y0
	e1z := tri.Z1 - tri.Z0
	e2x := tri.X2 - tri.X0
	e2y := tri.Y2 - tri.Y0
	e2z := tri.Z2 - tri.Z0
	cx := e1y*e2z - e1z*e2y
	cy := e1z*e2x - e1x*e2z
	cz := e1x*e2y - e1y*e2x
	return float32(math.Sqrt(float64(cx*cx+cy*cy+cz*cz))) * 0.5
}

// BuildZoneFlora scatters flora on render overlay triangles, filtered by collision
// mesh FloraType. Collision triangles mark which surfaces support flora (0+ = valid
// set, -1/255 = no flora). We build a spatial grid from collision data, then only
// place flora on overlay render triangles that overlap flora-valid areas.
// This gives correct Y positions (from render mesh) with correct biome filtering
// (from collision mesh).
func (fr *FloraRenderer) BuildZoneFlora(zt *ZoneTerrain) {
	if fr.built || zt == nil || zt.RadialFlora == nil || len(zt.RadialFlora.Models) == 0 {
		return
	}
	if len(zt.Tiles) == 0 {
		return
	}
	fr.terrain = zt
	rf := zt.RadialFlora
	nModels := len(rf.Models)

	// Build spatial grid of flora-valid areas from collision mesh.
	const floraGridCell = 4.0
	type gridKey struct{ gx, gz int }
	floraGrid := make(map[gridKey]int) // gridKey → flora set index

	for _, ct := range zt.CollTiles {
		for i := range ct.Triangles {
			tri := &ct.Triangles[i]
			if tri.FloraType < 0 || tri.FloraType == 255 {
				continue
			}
			cx := (tri.X0 + tri.X1 + tri.X2) / 3.0
			cz := (tri.Z0 + tri.Z1 + tri.Z2) / 3.0
			gk := gridKey{
				gx: int(math.Floor(float64(cx / floraGridCell))),
				gz: int(math.Floor(float64(cz / floraGridCell))),
			}
			floraGrid[gk] = tri.FloraType
		}
	}

	// Build set→dist lookup
	setDists := make(map[int][]int)
	for di, dist := range rf.Dists {
		setDists[int(dist.SetIndex)] = append(setDists[int(dist.SetIndex)], di)
	}

	type chunkModelKey struct {
		ck floraChunkKey
		mi int
	}
	buckets := make(map[chunkModelKey][]floraInstance)

	triIdx := uint32(0)
	totalInstances := 0
	for _, tile := range zt.Tiles {
		for ti := range tile.Triangles {
			tri := &tile.Triangles[ti]
			triIdx++

			if tri.Layer == 0 {
				continue
			}

			// Check flora grid at triangle centroid
			cx := (tri.X0 + tri.X1 + tri.X2) / 3.0
			cz := (tri.Z0 + tri.Z1 + tri.Z2) / 3.0
			gk := gridKey{
				gx: int(math.Floor(float64(cx / floraGridCell))),
				gz: int(math.Floor(float64(cz / floraGridCell))),
			}
			floraType, hasFlora := floraGrid[gk]
			if !hasFlora {
				continue
			}

			// Slope check
			e1x := tri.X1 - tri.X0
			e1y := tri.Y1 - tri.Y0
			e1z := tri.Z1 - tri.Z0
			e2x := tri.X2 - tri.X0
			e2y := tri.Y2 - tri.Y0
			e2z := tri.Z2 - tri.Z0
			nx := e1y*e2z - e1z*e2y
			ny := e1z*e2x - e1x*e2z
			nz := e1x*e2y - e1y*e2x
			nLen := float32(math.Sqrt(float64(nx*nx + ny*ny + nz*nz)))
			if nLen < 0.001 {
				continue
			}
			if ny/nLen < 0.7 {
				continue
			}

			area := renderTriArea(tri)
			if area < 0.01 {
				continue
			}

			distIdxs, ok := setDists[floraType]
			if !ok {
				continue
			}

			for _, di := range distIdxs {
				dist := &rf.Dists[di]
				if dist.SpriteIndex < 0 || int(dist.SpriteIndex) >= nModels {
					continue
				}
				mi := int(dist.SpriteIndex)

				expected := dist.Density * area * 0.07
				if expected < 0.001 {
					continue
				}

				baseSeed := floraHash(triIdx*73856093 + uint32(di)*19349669)
				count := int(expected)
				frac := expected - float32(count)
				if floraHashF(baseSeed) < frac {
					count++
				}
				if count <= 0 {
					continue
				}

				for j := 0; j < count; j++ {
					seed := floraHash(baseSeed + uint32(j)*2654435761)

					r1 := floraHashF(seed)
					r2 := floraHashF(seed * 2246822519)
					if r1+r2 > 1.0 {
						r1 = 1.0 - r1
						r2 = 1.0 - r2
					}
					r0 := 1.0 - r1 - r2

					fx := r0*tri.X0 + r1*tri.X1 + r2*tri.X2
					fy := r0*tri.Y0 + r1*tri.Y1 + r2*tri.Y2
					fz := r0*tri.Z0 + r1*tri.Z1 + r2*tri.Z2

					scale := dist.MinScale + floraHashF(seed*3266489917)*(dist.MaxScale-dist.MinScale)
					rotY := floraHashF(seed*1640531527) * math.Pi * 2

					ck := floraChunkKey{
						cx: int(math.Floor(float64(fx / floraChunkSize))),
						cz: int(math.Floor(float64(fz / floraChunkSize))),
					}
					key := chunkModelKey{ck: ck, mi: mi}
					buckets[key] = append(buckets[key], floraInstance{
						x: fx, y: fy, z: fz,
						scale: scale, rotY: rotY,
						modelIdx: mi,
					})
					totalInstances++
				}
			}
		}
	}

	// Step 2: Build GPU meshes per chunk
	fr.chunks = make(map[floraChunkKey]*floraChunk)

	for key, insts := range buckets {
		mi := key.mi
		if mi >= len(fr.textures) || fr.textures[mi].ID == 0 {
			continue
		}

		chunk, ok := fr.chunks[key.ck]
		if !ok {
			chunk = &floraChunk{
				centerX: (float32(key.ck.cx) + 0.5) * floraChunkSize,
				centerZ: (float32(key.ck.cz) + 0.5) * floraChunkSize,
			}
			fr.chunks[key.ck] = chunk
		}

		template := fr.quadVerts[mi]
		templateUV := fr.quadUVs[mi]
		vertsPerInst := len(template) / 3
		trisPerInst := vertsPerInst / 3

		totalVerts := len(insts) * vertsPerInst
		totalTris := len(insts) * trisPerInst

		verts := make([]float32, 0, totalVerts*3)
		uvs := make([]float32, 0, totalVerts*2)
		norms := make([]float32, 0, totalVerts*3)
		cols := make([]uint8, 0, totalVerts*4)

		for _, inst := range insts {
			cosR := float32(math.Cos(float64(inst.rotY)))
			sinR := float32(math.Sin(float64(inst.rotY)))

			for v := 0; v < vertsPerInst; v++ {
				lx := template[v*3+0] * inst.scale
				ly := template[v*3+1] * inst.scale
				lz := template[v*3+2] * inst.scale

				wx := lx*cosR - lz*sinR + inst.x
				wy := ly + inst.y
				wz := lx*sinR + lz*cosR + inst.z

				verts = append(verts, wx, wy, wz)
				uvs = append(uvs, templateUV[v*2], templateUV[v*2+1])
				norms = append(norms, 0, 1, 0)
				cols = append(cols, 255, 255, 255, 255)
			}
		}

		mesh := rl.Mesh{
			VertexCount:   int32(totalVerts),
			TriangleCount: int32(totalTris),
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

		mat := rl.LoadMaterialDefault()
		mat.Shader = fr.shader
		rl.SetMaterialTexture(&mat, rl.MapDiffuse, fr.textures[mi])

		chunk.meshes = append(chunk.meshes, floraChunkMesh{
			mesh:     mesh,
			material: mat,
			uploaded: true,
			triCount: totalTris,
			modelIdx: mi,
		})
	}

	fr.built = true
	log.Printf("flora: %d grid cells with valid flora, built %d instances in %d chunks across %d models",
		len(floraGrid), totalInstances, len(fr.chunks), nModels)
}

// Update builds zone flora on first call, then no-ops.
func (fr *FloraRenderer) Update(camX, camZ float32, zt *ZoneTerrain) {
	if !fr.built && zt != nil {
		fr.BuildZoneFlora(zt)
	}
}

// Draw renders only flora chunks within cull distance of the player.
func (fr *FloraRenderer) Draw(camX, camY, camZ float32, f interface{}) {
	if !fr.built || len(fr.chunks) == 0 {
		return
	}

	rl.DisableBackfaceCulling()
	rl.BeginBlendMode(rl.BlendAlpha)
	identity := rl.MatrixIdentity()

	for _, chunk := range fr.chunks {
		dx := chunk.centerX - camX
		dz := chunk.centerZ - camZ
		if dx*dx+dz*dz > floraCullDist2 {
			continue
		}

		for i := range chunk.meshes {
			cm := &chunk.meshes[i]
			if !cm.uploaded || cm.triCount == 0 {
				continue
			}
			rl.DrawMesh(cm.mesh, cm.material, identity)
		}
	}

	rl.EndBlendMode()
	rl.EnableBackfaceCulling()
}

var _ = (*image.NRGBA)(nil)
