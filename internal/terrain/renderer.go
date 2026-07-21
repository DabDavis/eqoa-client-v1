package terrain

import (
	"image"
	"log"
	"math"
	"runtime"
	"time"
	"unsafe"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/eqoa/client/internal/frustum"
	"github.com/eqoa/client/internal/gfx"
	"github.com/eqoa/client/internal/mathutil"
	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

// meshGroup holds a GPU mesh and its material for one texture.
type meshGroup struct {
	Mesh     rl.Mesh
	Material rl.Material
	Texture  rl.Texture2D // stored separately for lit shader material swap
	Overlay  bool         // true for layer > 0 (drawn with alpha blending)
	// UV scroll animation (water). Non-zero means this mesh is animated.
	URate, VRate float32
	// Axis-aligned bounding box for frustum culling.
	MinX, MinY, MinZ float32
	MaxX, MaxY, MaxZ float32
	// Portal room assignment (-1 = exterior/outdoor, always visible).
	RoomID int
}

// pendingMeshGroup holds CPU-prepared mesh data ready for GPU upload.
type pendingMeshGroup struct {
	key   meshKey
	verts []float32
	uvs   []float32
	norms []float32
	cols  []uint8
	nTris int
	// UV scroll animation (water)
	uRate, vRate float32
	// AABB
	minX, minY, minZ float32
	maxX, maxY, maxZ float32
}

// pendingTexture holds a CPU-prepared texture image ready for GPU upload.
type pendingTexture struct {
	texID int32
	img   *image.NRGBA
}

// Alpha cutoff shader (no lighting) — discards fragments with alpha < 0.5.
// Required because GL_ALPHA_TEST is not available in OpenGL 3.3 core profile.
const terrainAlphaCutoffVS = `#version 330
in vec3 vertexPosition;
in vec2 vertexTexCoord;
in vec4 vertexColor;
uniform mat4 mvp;
uniform vec2 uvOffset;
out vec2 fragTexCoord;
out vec4 fragColor;
void main() {
    fragTexCoord = vertexTexCoord + uvOffset;
    fragColor = vertexColor;
    gl_Position = mvp * vec4(vertexPosition, 1.0);
}
`

const terrainAlphaCutoffFS = `#version 330
in vec2 fragTexCoord;
in vec4 fragColor;
uniform sampler2D texture0;
uniform vec4 colDiffuse;
out vec4 finalColor;
void main() {
    vec4 texel = texture(texture0, fragTexCoord);
    vec4 color = texel * colDiffuse * fragColor;
    if (color.a < 0.5) discard;
    finalColor = color;
}
`

// Overlay shader — uses vertex alpha for smooth blending at biome boundaries.
// Discards very low alpha fragments to avoid faint edge wash.
const terrainOverlayFS = `#version 330
in vec2 fragTexCoord;
in vec4 fragColor;
uniform sampler2D texture0;
uniform vec4 colDiffuse;
out vec4 finalColor;
void main() {
    vec4 texel = texture(texture0, fragTexCoord);
    vec4 color = texel * colDiffuse * fragColor;
    if (color.a < 0.02) discard;
    finalColor = color;
}
`

// Lit terrain shader — sun directional light + distance fog + alpha cutoff.
// PS2 VIRaster::SetSunLight + SetAmbient + SetFog ported to GLSL.
const terrainLitVS = `#version 330
in vec3 vertexPosition;
in vec2 vertexTexCoord;
in vec3 vertexNormal;
in vec4 vertexColor;
uniform mat4 mvp;
uniform mat4 matModel;
uniform vec2 uvOffset;
out vec2 fragTexCoord;
out vec3 fragNormal;
out vec4 fragColor;
out vec3 fragWorldPos;
void main() {
    fragTexCoord = vertexTexCoord + uvOffset;
    fragNormal = vertexNormal;
    fragColor = vertexColor;
    fragWorldPos = vec3(matModel * vec4(vertexPosition, 1.0));
    gl_Position = mvp * vec4(vertexPosition, 1.0);
}
`

const maxShaderPointLights = 16

const terrainLitFS = `#version 330
in vec2 fragTexCoord;
in vec3 fragNormal;
in vec4 fragColor;
in vec3 fragWorldPos;
uniform sampler2D texture0;
uniform vec4 colDiffuse;
uniform vec3 viewPos;
uniform vec3 sunDir;       // normalized, pointing toward sun
uniform vec3 sunColor;     // sun light RGB intensity
uniform vec3 ambientColor; // global ambient RGB
uniform vec3 fogColor;     // distance fog color
uniform float fogStart;    // fog start distance
uniform float fogEnd;      // fog end distance
// Point lights (torches, lamps)
uniform int numPointLights;
uniform vec3 pointLightPos[16];
uniform vec3 pointLightColor[16];
uniform float pointLightRadius[16];
out vec4 finalColor;
void main() {
    vec4 texel = texture(texture0, fragTexCoord);
    vec4 base = texel * colDiffuse * fragColor;
    if (base.a < 0.5) discard;

    // Keep pre-baked vertex colors (static lighting) and add dynamic lighting on top.
    vec3 baked = base.rgb;

    // Normalize the interpolated normal
    vec3 N = normalize(fragNormal);

    // Half-lambert diffuse — additive sun on top of baked lighting.
    // Linear wrap (no squaring) for smoother falloff across slope crests.
    float NdotL = dot(N, sunDir);
    float diffuse = NdotL * 0.5 + 0.5;

    vec3 lit = baked * (ambientColor + sunColor * diffuse);

    // Point lights — additive contribution
    // Close-range wrap: fixtures touching the light source glow fully
    // regardless of normal direction (emissive look for lamp bowls/glass).
    for (int i = 0; i < numPointLights; i++) {
        vec3 toLight = pointLightPos[i] - fragWorldPos;
        float dist2 = dot(toLight, toLight);
        float radius = pointLightRadius[i];
        float radius2 = radius * radius;
        if (dist2 < radius2) {
            float d = sqrt(dist2);
            float atten = 1.0 - d / radius;
            atten = atten * atten; // quadratic falloff
            // Fade out light for fragments below the light source.
            // toLight.y > 0 means fragment is below light (light points up from frag).
            // Allow full light at light level and above; fade over 2 units below.
            float belowFade = clamp(toLight.y / 2.0 + 1.0, 0.0, 1.0);
            atten *= belowFade;
            float NdotPL = max(dot(N, normalize(toLight)), 0.0);
            // Wrap factor: 1.0 at light center, 0.0 at 30% radius and beyond.
            // Makes lamp fixtures glow without requiring normal alignment.
            float wrap = clamp(1.0 - d / (radius * 0.3), 0.0, 1.0);
            float light = mix(NdotPL, 1.0, wrap);
            lit += pointLightColor[i] * atten * light * 0.6;
        }
    }

    // Cap total illumination to prevent additive light blowout.
    lit = min(lit, vec3(1.2));

    // Distance fog
    float dist = length(fragWorldPos - viewPos);
    float fogFactor = clamp((dist - fogStart) / (fogEnd - fogStart), 0.0, 1.0);
    vec3 result = mix(lit, fogColor, fogFactor);

    finalColor = vec4(result, 1.0);
}
`

// Renderer draws zone terrain as 3D meshes.
// zoneSlot holds per-zone GPU mesh state for multi-zone rendering.
type zoneSlot struct {
	proxyIdx      int
	groups        []meshGroup
	built         bool
	prepared      bool
	pendingTexList []pendingTexture
	texCache      map[int32]rl.Texture2D
	texUploaded   bool
	pendingGroups []pendingMeshGroup
	uploadIdx     int
	prepareTime   time.Time
}

type Renderer struct {
	groups    []meshGroup
	built     bool
	whiteTex  rl.Texture2D // solid white texture for overlay fill pass
	solidMat  rl.Material  // opaque white material for overlay solid fill
	cutoffShader  rl.Shader // alpha cutoff shader for tree/foliage transparency
	overlayShader rl.Shader // overlay shader with vertex alpha blending

	// Lit terrain shader (sun + fog)
	litShader    rl.Shader
	litLoaded    bool
	LightingOn   bool // toggle with F4 — when false, uses old unlit cutoff shader
	locViewPos   int32
	locSunDir    int32
	locSunColor  int32
	locAmbient   int32
	locFogColor  int32
	locFogStart  int32
	locFogEnd    int32
	locMatModel  int32
	// Point light uniforms
	locNumPointLights  int32
	locPointLightPos   int32
	locPointLightColor int32
	locPointLightRadius int32
	// UV scroll animation uniforms (water)
	locUvOffsetCutoff  int32
	locUvOffsetOverlay int32
	locUvOffsetLit     int32

	// Player radial light (lantern)
	playerLightOn     bool
	playerLightPos    [3]float32
	playerLightRadius float32
	playerLightColor  [3]float32

	// Ground fill plane — solid base layer under terrain (PS2-style).
	groundFillMesh    rl.Mesh
	groundFillReady   bool

	// Incremental upload state (legacy single-zone)
	prepared       bool                      // true after prepareMeshes has run
	pendingTexList []pendingTexture          // textures awaiting GPU upload
	texCache       map[int32]rl.Texture2D    // uploaded textures
	texUploaded    bool                      // all textures uploaded
	pendingGroups  []pendingMeshGroup        // mesh groups awaiting GPU upload
	uploadIdx      int                       // next pendingGroups index to upload
	prepareTime    time.Time                 // when prepareMeshes started
	shaderLoaded   bool                      // true after cutoff shader compiled
	OverlaysOff    bool                      // F5 toggle: skip overlay rendering for debugging

	// Multi-zone mesh state (PS2 radial streaming)
	zoneSlots map[int]*zoneSlot // proxyIdx → per-zone mesh data
}

// NewRenderer creates a terrain renderer. Must be called after rl.InitWindow.
func NewRenderer() *Renderer {
	return &Renderer{}
}

// Reset clears GPU meshes so a new zone can be built.
func (r *Renderer) Reset() {
	for i := range r.groups {
		rl.UnloadMesh(&r.groups[i].Mesh)
	}
	r.groups = nil
	r.built = false
	r.prepared = false
	r.pendingTexList = nil
	r.texCache = nil
	r.texUploaded = false
	r.pendingGroups = nil
	r.uploadIdx = 0
}

// IsBuilt returns true when all meshes have been uploaded to the GPU.
func (r *Renderer) IsBuilt() bool {
	return r.built
}

// meshKey groups triangles by texture and layer.
type meshKey struct {
	TexID int32
	Layer int32
	URate float32
	VRate float32
}

// prepareMeshes performs CPU-only work: groups triangles, prepares vertex arrays,
// computes AABBs, and prepares texture images. No GPU calls.
func (r *Renderer) prepareMeshes(zt *ZoneTerrain) {
	if r.prepared {
		return
	}
	r.prepared = true
	r.prepareTime = time.Now()
	log.Printf("terrain: prepareMeshes START — %d tiles", len(zt.Tiles))

	// Collect all triangles grouped by TexID+Layer.
	groups := make(map[meshKey][]Triangle, 16)
	for _, tile := range zt.Tiles {
		for _, tri := range tile.Triangles {
			key := meshKey{TexID: tri.TexID, Layer: tri.Layer, URate: tri.URate, VRate: tri.VRate}
			groups[key] = append(groups[key], tri)
		}
	}
	log.Printf("terrain: prepareMeshes grouped %d keys in %v", len(groups), time.Since(r.prepareTime))

	// Prepare texture images (CPU-only: alpha detection + conversion).
	r.pendingTexList = make([]pendingTexture, 0, len(zt.Textures))
	for texID, img := range zt.Textures {
		if img == nil {
			continue
		}
		uploadImg := img
		if isAlphaOnlyTexture(img) {
			uploadImg = alphaToWhiteRGBKeepAlpha(img)
		}
		r.pendingTexList = append(r.pendingTexList, pendingTexture{texID: texID, img: uploadImg})
	}
	r.texCache = make(map[int32]rl.Texture2D, len(r.pendingTexList))

	// Prepare mesh vertex data (CPU-only).
	r.pendingGroups = make([]pendingMeshGroup, 0, len(groups))
	for key, tris := range groups {
		n := len(tris) * 3

		verts := make([]float32, n*3)
		uvs := make([]float32, n*2)
		norms := make([]float32, n*3)
		cols := make([]uint8, n*4)

		bbMinX := float32(math.MaxFloat32)
		bbMinY := float32(math.MaxFloat32)
		bbMinZ := float32(math.MaxFloat32)
		bbMaxX := float32(-math.MaxFloat32)
		bbMaxY := float32(-math.MaxFloat32)
		bbMaxZ := float32(-math.MaxFloat32)

		for i, tri := range tris {
			v3 := i * 9
			v2 := i * 6
			v4 := i * 12

			verts[v3+0] = tri.X0
			verts[v3+1] = tri.Y0
			verts[v3+2] = tri.Z0
			uvs[v2+0] = tri.U0
			uvs[v2+1] = tri.V0
			norms[v3+0] = tri.NX0
			norms[v3+1] = tri.NY0
			norms[v3+2] = tri.NZ0

			verts[v3+3] = tri.X1
			verts[v3+4] = tri.Y1
			verts[v3+5] = tri.Z1
			uvs[v2+2] = tri.U1
			uvs[v2+3] = tri.V1
			norms[v3+3] = tri.NX1
			norms[v3+4] = tri.NY1
			norms[v3+5] = tri.NZ1

			verts[v3+6] = tri.X2
			verts[v3+7] = tri.Y2
			verts[v3+8] = tri.Z2
			uvs[v2+4] = tri.U2
			uvs[v2+5] = tri.V2
			norms[v3+6] = tri.NX2
			norms[v3+7] = tri.NY2
			norms[v3+8] = tri.NZ2

			cols[v4+0] = mathutil.FloatToUint8(tri.R0)
			cols[v4+1] = mathutil.FloatToUint8(tri.G0)
			cols[v4+2] = mathutil.FloatToUint8(tri.B0)
			cols[v4+4] = mathutil.FloatToUint8(tri.R1)
			cols[v4+5] = mathutil.FloatToUint8(tri.G1)
			cols[v4+6] = mathutil.FloatToUint8(tri.B1)
			cols[v4+8] = mathutil.FloatToUint8(tri.R2)
			cols[v4+9] = mathutil.FloatToUint8(tri.G2)
			cols[v4+10] = mathutil.FloatToUint8(tri.B2)
			// Base terrain (layer 0) must be fully opaque — vertex alpha only
			// controls overlay blending. Without this, ESF alpha < 0.5 would
			// cause the alpha-cutoff shader to discard base terrain fragments.
			if key.Layer == 0 {
				cols[v4+3] = 255
				cols[v4+7] = 255
				cols[v4+11] = 255
			} else {
				cols[v4+3] = mathutil.FloatToUint8(tri.A0)
				cols[v4+7] = mathutil.FloatToUint8(tri.A1)
				cols[v4+11] = mathutil.FloatToUint8(tri.A2)
			}

			for _, vx := range []float32{tri.X0, tri.X1, tri.X2} {
				if vx < bbMinX {
					bbMinX = vx
				}
				if vx > bbMaxX {
					bbMaxX = vx
				}
			}
			for _, vy := range []float32{tri.Y0, tri.Y1, tri.Y2} {
				if vy < bbMinY {
					bbMinY = vy
				}
				if vy > bbMaxY {
					bbMaxY = vy
				}
			}
			for _, vz := range []float32{tri.Z0, tri.Z1, tri.Z2} {
				if vz < bbMinZ {
					bbMinZ = vz
				}
				if vz > bbMaxZ {
					bbMaxZ = vz
				}
			}
		}

		pg := pendingMeshGroup{
			key:   key,
			verts: verts,
			uvs:   uvs,
			norms: norms,
			cols:  cols,
			nTris: len(tris),
			minX:  bbMinX, minY: bbMinY, minZ: bbMinZ,
			maxX:  bbMaxX, maxY: bbMaxY, maxZ:  bbMaxZ,
		}
		pg.uRate = key.URate
		pg.vRate = key.VRate
		r.pendingGroups = append(r.pendingGroups, pg)
	}

	log.Printf("terrain: prepareMeshes done — %d textures, %d mesh groups prepared in %v",
		len(r.pendingTexList), len(r.pendingGroups), time.Since(r.prepareTime))
}

// UploadBatch uploads up to maxCount textures or mesh groups to the GPU per call.
// Returns true when all uploads are complete. Must be called on the main/GL thread.
func (r *Renderer) UploadBatch(maxCount int) bool {
	if r.built {
		return true
	}
	if !r.prepared {
		return false
	}

	uploaded := 0

	// Phase 1: upload white texture + solid material (once, before anything else).
	// Use local variable for SetMaterialTexture to avoid CGo "Go pointer to
	// unpinned Go pointer" panic — &r.solidMat is a heap pointer (Renderer is
	// heap-allocated) containing Material.Maps (another Go pointer). Local
	// variables on the stack don't trigger this CGo check.
	if r.whiteTex.ID == 0 {
		whiteImg := rl.GenImageColor(4, 4, rl.White)
		r.whiteTex = rl.LoadTextureFromImage(whiteImg)
		rl.UnloadImage(whiteImg)
		mat := rl.LoadMaterialDefault()
		rl.SetMaterialTexture(&mat, rl.MapDiffuse, r.whiteTex)
		r.solidMat = mat
		uploaded++
	}
	if !r.shaderLoaded {
		r.cutoffShader = rl.LoadShaderFromMemory(terrainAlphaCutoffVS, terrainAlphaCutoffFS)
		r.overlayShader = rl.LoadShaderFromMemory(terrainAlphaCutoffVS, terrainOverlayFS)
		r.litShader = rl.LoadShaderFromMemory(terrainLitVS, terrainLitFS)
		r.locViewPos = rl.GetShaderLocation(r.litShader, "viewPos")
		r.locSunDir = rl.GetShaderLocation(r.litShader, "sunDir")
		r.locSunColor = rl.GetShaderLocation(r.litShader, "sunColor")
		r.locAmbient = rl.GetShaderLocation(r.litShader, "ambientColor")
		r.locFogColor = rl.GetShaderLocation(r.litShader, "fogColor")
		r.locFogStart = rl.GetShaderLocation(r.litShader, "fogStart")
		r.locFogEnd = rl.GetShaderLocation(r.litShader, "fogEnd")
		r.locMatModel = rl.GetShaderLocation(r.litShader, "matModel")
		r.locNumPointLights = rl.GetShaderLocation(r.litShader, "numPointLights")
		r.locPointLightPos = rl.GetShaderLocation(r.litShader, "pointLightPos")
		r.locPointLightColor = rl.GetShaderLocation(r.litShader, "pointLightColor")
		r.locPointLightRadius = rl.GetShaderLocation(r.litShader, "pointLightRadius")
		r.locUvOffsetCutoff = rl.GetShaderLocation(r.cutoffShader, "uvOffset")
		r.locUvOffsetOverlay = rl.GetShaderLocation(r.overlayShader, "uvOffset")
		r.locUvOffsetLit = rl.GetShaderLocation(r.litShader, "uvOffset")
		r.litLoaded = true
		r.LightingOn = true // enabled by default, F4 to toggle
		r.shaderLoaded = true
	}

	// Phase 2: upload textures.
	if !r.texUploaded {
		for uploaded < maxCount && len(r.pendingTexList) > 0 {
			pt := r.pendingTexList[0]
			r.pendingTexList = r.pendingTexList[1:]

			rlImg := rl.NewImageFromImage(pt.img)
			tex := rl.LoadTextureFromImage(rlImg)
			rl.UnloadImage(rlImg)
			rl.GenTextureMipmaps(&tex)
			rl.SetTextureFilter(tex, rl.FilterTrilinear)
			rl.SetTextureWrap(tex, rl.WrapRepeat)
			r.texCache[pt.texID] = tex
			uploaded++
		}
		if len(r.pendingTexList) == 0 {
			r.texUploaded = true
			log.Printf("terrain: UploadBatch textures done (%d uploaded) in %v",
				len(r.texCache), time.Since(r.prepareTime))
		}
		if uploaded >= maxCount {
			return false
		}
	}

	// Phase 3: upload mesh groups.
	for r.uploadIdx < len(r.pendingGroups) && uploaded < maxCount {
		pg := &r.pendingGroups[r.uploadIdx]
		n := pg.nTris * 3

		mesh := rl.Mesh{
			VertexCount:   int32(n),
			TriangleCount: int32(pg.nTris),
			Vertices:      (*float32)(unsafe.Pointer(&pg.verts[0])),
			Texcoords:     (*float32)(unsafe.Pointer(&pg.uvs[0])),
			Colors:        (*uint8)(unsafe.Pointer(&pg.cols[0])),
		}
		if pg.key.Layer == 0 {
			mesh.Normals = (*float32)(unsafe.Pointer(&pg.norms[0]))
		}

		rl.UploadMesh(&mesh, false)

		runtime.KeepAlive(pg.verts)
		runtime.KeepAlive(pg.uvs)
		runtime.KeepAlive(pg.norms)
		runtime.KeepAlive(pg.cols)

		mesh.Vertices = nil
		mesh.Texcoords = nil
		mesh.Normals = nil
		mesh.Colors = nil

		mat := rl.LoadMaterialDefault()
		if r.shaderLoaded {
			if pg.key.Layer == 0 {
				// Base layers use alpha cutoff shader to discard transparent tree/foliage pixels.
				mat.Shader = r.cutoffShader
			} else {
				// Overlay layers use vertex alpha for smooth biome boundary blending.
				mat.Shader = r.overlayShader
			}
		}
		tex := r.whiteTex
		if t, ok := r.texCache[pg.key.TexID]; ok {
			tex = t
		}
		rl.SetMaterialTexture(&mat, rl.MapDiffuse, tex)

		r.groups = append(r.groups, meshGroup{
			Mesh:     mesh,
			Material: mat,
			Texture:  tex,
			Overlay:  pg.key.Layer > 0,
			URate:    pg.uRate,
			VRate:    pg.vRate,
			MinX:     pg.minX,
			MinY:     pg.minY,
			MinZ:     pg.minZ,
			MaxX:     pg.maxX,
			MaxY:     pg.maxY,
			MaxZ:     pg.maxZ,
		})

		r.uploadIdx++
		uploaded++

		if r.uploadIdx%20 == 0 {
			log.Printf("terrain: UploadBatch %d/%d meshes (%v)",
				r.uploadIdx, len(r.pendingGroups), time.Since(r.prepareTime))
		}
	}

	if r.uploadIdx >= len(r.pendingGroups) {
		r.built = true
		// Free pending data — no longer needed.
		r.pendingGroups = nil
		r.pendingTexList = nil

		var baseCount, overlayCount int
		for _, g := range r.groups {
			if g.Overlay {
				overlayCount++
			} else {
				baseCount++
			}
		}
		log.Printf("terrain: UploadBatch COMPLETE — %d GPU meshes (%d base, %d overlay) in %v",
			len(r.groups), baseCount, overlayCount, time.Since(r.prepareTime))
		return true
	}

	return false
}

// SetPlayerLight enables/disables the player lantern light.
// When on, it occupies one of the 8 point light slots (always nearest).
func (r *Renderer) SetPlayerLight(on bool, x, y, z, radius float32, cr, cg, cb float32) {
	r.playerLightOn = on
	r.playerLightPos = [3]float32{x, y, z}
	r.playerLightRadius = radius
	r.playerLightColor = [3]float32{cr, cg, cb}
}

// SetLightingUniforms sets per-frame lighting uniforms. Call before Draw().
// camPos is the camera world position. gameHour is 0-24 for day/night cycle.
// lights are zone point lights (torches, lamps) — nearest 8 are sent to GPU.
func (r *Renderer) SetLightingUniforms(camX, camY, camZ, gameHour float32, lights []esf.PointLightPlacement) {
	if !r.litLoaded || !r.LightingOn {
		return
	}

	// Camera position for fog distance
	viewPos := [3]float32{camX, camY, camZ}
	rl.SetShaderValue(r.litShader, r.locViewPos, viewPos[:], rl.ShaderUniformVec3)

	// PS2 SetTimeOfDay (0x00467B98): sunDir.X = -sin(angle), sunDir.Y = cos(angle)
	// angle = hour * 15° (15 degrees per hour).
	// Hour 0: (0,-1,0) below, Hour 6: (-1,0,0) east, Hour 12: (0,1,0) above, Hour 18: (1,0,0) west.
	sunAngleRad := float64(gameHour) * 15.0 * math.Pi / 180.0
	sunDir := [3]float32{
		-float32(math.Sin(sunAngleRad)),
		float32(math.Cos(sunAngleRad)),
		0,
	}
	rl.SetShaderValue(r.litShader, r.locSunDir, sunDir[:], rl.ShaderUniformVec3)

	// PS2 24-keyframe time-of-day colors (from VIScene+0x1FF4, decompiled).
	sunColor, ambientColor, fogColor := ps2TimeOfDayColors(gameHour)

	rl.SetShaderValue(r.litShader, r.locSunColor, sunColor[:], rl.ShaderUniformVec3)
	rl.SetShaderValue(r.litShader, r.locAmbient, ambientColor[:], rl.ShaderUniformVec3)
	rl.SetShaderValue(r.litShader, r.locFogColor, fogColor[:], rl.ShaderUniformVec3)

	fogStart := float32(300.0)
	fogEnd := float32(800.0)
	rl.SetShaderValue(r.litShader, r.locFogStart, []float32{fogStart}, rl.ShaderUniformFloat)
	rl.SetShaderValue(r.litShader, r.locFogEnd, []float32{fogEnd}, rl.ShaderUniformFloat)

	// Identity model matrix (terrain is in world space)
	matModel := rl.MatrixIdentity()
	rl.SetShaderValueMatrix(r.litShader, r.locMatModel, matModel)

	// Inject player lantern light as an additional point light
	if r.playerLightOn {
		lights = append(lights, esf.PointLightPlacement{
			Pos:    esf.Point{X: r.playerLightPos[0], Y: r.playerLightPos[1], Z: r.playerLightPos[2]},
			Radius: r.playerLightRadius,
			Color:  esf.Color32F{R: r.playerLightColor[0], G: r.playerLightColor[1], B: r.playerLightColor[2], A: 1},
		})
	}

	// Point lights — find nearest 8 and upload to shader
	numLights := 0
	if len(lights) > 0 {
		// Sort by distance to camera, pick nearest maxShaderPointLights
		type lightDist struct {
			idx    int
			distSq float32
		}
		sorted := make([]lightDist, len(lights))
		for i, l := range lights {
			dx := l.Pos.X - camX
			dy := l.Pos.Y - camY
			dz := l.Pos.Z - camZ
			sorted[i] = lightDist{i, dx*dx + dy*dy + dz*dz}
		}
		// Simple selection of nearest N (insertion sort for small N)
		for i := 0; i < len(sorted) && i < maxShaderPointLights; i++ {
			minIdx := i
			for j := i + 1; j < len(sorted); j++ {
				if sorted[j].distSq < sorted[minIdx].distSq {
					minIdx = j
				}
			}
			sorted[i], sorted[minIdx] = sorted[minIdx], sorted[i]
		}

		n := len(sorted)
		if n > maxShaderPointLights {
			n = maxShaderPointLights
		}
		numLights = n

		var pos [maxShaderPointLights * 3]float32
		var col [maxShaderPointLights * 3]float32
		var rad [maxShaderPointLights]float32
		for i := 0; i < n; i++ {
			l := &lights[sorted[i].idx]
			pos[i*3+0] = l.Pos.X
			pos[i*3+1] = l.Pos.Y
			pos[i*3+2] = l.Pos.Z
			col[i*3+0] = l.Color.R
			col[i*3+1] = l.Color.G
			col[i*3+2] = l.Color.B
			rad[i] = l.Radius
		}
		rl.SetShaderValueV(r.litShader, r.locPointLightPos, pos[:n*3], rl.ShaderUniformVec3, int32(n))
		rl.SetShaderValueV(r.litShader, r.locPointLightColor, col[:n*3], rl.ShaderUniformVec3, int32(n))
		rl.SetShaderValueV(r.litShader, r.locPointLightRadius, rad[:n], rl.ShaderUniformFloat, int32(n))
	}
	// Pass int uniform via float32 slice — reinterpret int32 bits as float32.
	numLightsI := [1]int32{int32(numLights)}
	rl.SetShaderValue(r.litShader, r.locNumPointLights, (*[1]float32)(unsafe.Pointer(&numLightsI))[:], rl.ShaderUniformInt)
}

// ToggleLighting flips the lighting shader on/off. Returns new state.
func (r *Renderer) ToggleLighting() bool {
	r.LightingOn = !r.LightingOn
	return r.LightingOn
}

// setUvOffset sets the uvOffset uniform on the appropriate shader for a mesh group.
// Returns true if an offset was set (caller should reset to zero after draw).
func (r *Renderer) setUvOffset(g *meshGroup, t float64, useLit bool) bool {
	if g.URate == 0 && g.VRate == 0 {
		return false
	}
	ft := float32(t)
	// PS2 VIMaterial::Process wraps offsets to [-1, 1] (fractional part).
	// We compute rate*time mod 1.0 to avoid float32 precision loss over long sessions.
	u := g.URate * ft
	v := g.VRate * ft
	u -= float32(int(u)) // fractional part, preserves sign
	v -= float32(int(v))
	offset := [2]float32{u, v}
	if g.Overlay {
		rl.SetShaderValue(r.overlayShader, r.locUvOffsetOverlay, offset[:], rl.ShaderUniformVec2)
	} else if useLit {
		rl.SetShaderValue(r.litShader, r.locUvOffsetLit, offset[:], rl.ShaderUniformVec2)
	} else {
		rl.SetShaderValue(r.cutoffShader, r.locUvOffsetCutoff, offset[:], rl.ShaderUniformVec2)
	}
	return true
}

// clearUvOffset resets the uvOffset uniform to zero on the appropriate shader.
func (r *Renderer) clearUvOffset(g *meshGroup, useLit bool) {
	zero := [2]float32{0, 0}
	if g.Overlay {
		rl.SetShaderValue(r.overlayShader, r.locUvOffsetOverlay, zero[:], rl.ShaderUniformVec2)
	} else if useLit {
		rl.SetShaderValue(r.litShader, r.locUvOffsetLit, zero[:], rl.ShaderUniformVec2)
	} else {
		rl.SetShaderValue(r.cutoffShader, r.locUvOffsetCutoff, zero[:], rl.ShaderUniformVec2)
	}
}

// Draw renders all terrain meshes. Must be called between BeginMode3D/EndMode3D.
// Base layers are drawn first, then overlays with alpha blending.
// If f is non-nil, mesh groups outside the frustum are skipped.
// Returns (drawn, total) counts for HUD display.
// Draws whatever groups have been uploaded so far (partial rendering during upload).
func (r *Renderer) Draw(zt *ZoneTerrain, f *frustum.Frustum) (drawn, total int) {
	if zt == nil || len(zt.Tiles) == 0 {
		return 0, 0
	}

	// Kick off CPU preparation if not already done.
	r.prepareMeshes(zt)

	total = len(r.groups)
	if total == 0 {
		return 0, 0
	}

	identity := rl.MatrixIdentity()
	t := rl.GetTime()

	// All passes use alpha blending so textures with transparency
	// (tree leaves, biome overlays) render correctly.
	rl.BeginBlendMode(rl.BlendAlpha)

	// Pass 1: base layers.
	// When lighting is on, swap in the lit shader for base layers.
	useLit := r.LightingOn && r.litLoaded
	for i := range r.groups {
		g := &r.groups[i]
		if g.Overlay {
			continue
		}
		if f != nil && !f.TestAABB(g.MinX, g.MinY, g.MinZ, g.MaxX, g.MaxY, g.MaxZ) {
			continue
		}
		animated := r.setUvOffset(g, t, useLit)
		if useLit {
			// Build a stack-local material with the lit shader + group's texture.
			// Must be fully local to avoid CGo "Go pointer" panics.
			litMat := rl.LoadMaterialDefault()
			litMat.Shader = r.litShader
			rl.SetMaterialTexture(&litMat, rl.MapDiffuse, g.Texture)
			rl.DrawMesh(g.Mesh, litMat, identity)
		} else {
			rl.DrawMesh(g.Mesh, g.Material, identity)
		}
		if animated {
			r.clearUvOffset(g, useLit)
		}
		drawn++
	}

	// Pass 2: overlay layers (biome detail).
	// GL_LEQUAL lets overlays draw at identical depth to base without z-fighting.
	// Depth writes OFF so overlays don't create cracks at strip boundaries.
	if !r.OverlaysOff {
		gfx.DepthFuncLEqual()
		rl.DisableDepthMask()

		for i := range r.groups {
			g := &r.groups[i]
			if !g.Overlay {
				continue
			}
			if f != nil && !f.TestAABB(g.MinX, g.MinY, g.MinZ, g.MaxX, g.MaxY, g.MaxZ) {
				continue
			}
			animated := r.setUvOffset(g, t, useLit)
			rl.DrawMesh(g.Mesh, g.Material, identity)
			if animated {
				r.clearUvOffset(g, useLit)
			}
			drawn++
		}

		rl.EnableDepthMask()
		gfx.DepthFuncLess()
	}

	rl.EndBlendMode()
	return drawn, total
}

// AssignRooms assigns each mesh group to a room by testing if the group's
// AABB center falls inside a room's BBox. Groups outside all rooms get RoomID = -1.
// Called once after zone meshes are built.
func (r *Renderer) AssignRooms(rooms []esf.ZoneRoom) {
	assignGroups := func(groups []meshGroup) int {
		assigned := 0
		for i := range groups {
			g := &groups[i]
			g.RoomID = -1 // exterior by default
			cx := (g.MinX + g.MaxX) / 2
			cy := (g.MinY + g.MaxY) / 2
			cz := (g.MinZ + g.MaxZ) / 2
			for ri, room := range rooms {
				b := room.BBox
				if cx >= b.MinX && cx <= b.MaxX && cy >= b.MinY && cy <= b.MaxY && cz >= b.MinZ && cz <= b.MaxZ {
					g.RoomID = ri
					assigned++
					break
				}
			}
		}
		return assigned
	}

	// Assign legacy single-zone groups
	assigned := assignGroups(r.groups)

	// Assign multi-zone slot groups
	for _, zs := range r.zoneSlots {
		assigned += assignGroups(zs.groups)
	}
	_ = assigned
}

// Cleanup unloads all GPU resources.
func (r *Renderer) Cleanup() {
	for _, g := range r.groups {
		rl.UnloadMesh(&g.Mesh)
		if g.Material.Shader.ID > 0 {
			rl.UnloadMaterial(g.Material)
		}
	}
	r.groups = nil
	// Cleanup multi-zone slots
	for idx, zs := range r.zoneSlots {
		r.resetZoneSlot(zs)
		delete(r.zoneSlots, idx)
	}
	if r.solidMat.Shader.ID > 0 {
		rl.UnloadMaterial(r.solidMat)
	}
	if r.whiteTex.ID > 0 {
		rl.UnloadTexture(r.whiteTex)
	}
	if r.shaderLoaded {
		rl.UnloadShader(r.cutoffShader)
		rl.UnloadShader(r.overlayShader)
		r.shaderLoaded = false
	}
	if r.litLoaded {
		rl.UnloadShader(r.litShader)
		r.litLoaded = false
	}
}

// --- Multi-zone rendering (PS2 radial streaming) ---

func (r *Renderer) getOrCreateSlot(proxyIdx int) *zoneSlot {
	if r.zoneSlots == nil {
		r.zoneSlots = make(map[int]*zoneSlot)
	}
	zs, ok := r.zoneSlots[proxyIdx]
	if !ok {
		zs = &zoneSlot{proxyIdx: proxyIdx}
		r.zoneSlots[proxyIdx] = zs
	}
	return zs
}

func (r *Renderer) resetZoneSlot(zs *zoneSlot) {
	for i := range zs.groups {
		rl.UnloadMesh(&zs.groups[i].Mesh)
	}
	// Unload GPU textures uploaded for this zone. Without this, evicted
	// zones leak VRAM — texture memory grows monotonically as the player
	// streams zones over a long session.
	for _, tex := range zs.texCache {
		if tex.ID > 0 {
			rl.UnloadTexture(tex)
		}
	}
	zs.groups = nil
	zs.built = false
	zs.prepared = false
	zs.pendingTexList = nil
	zs.texCache = nil
	zs.texUploaded = false
	zs.pendingGroups = nil
	zs.uploadIdx = 0
}

// ResetZone unloads GPU meshes for a single zone.
func (r *Renderer) ResetZone(proxyIdx int) {
	if zs, ok := r.zoneSlots[proxyIdx]; ok {
		r.resetZoneSlot(zs)
		delete(r.zoneSlots, proxyIdx)
	}
}

// IsZoneBuilt returns true when all meshes for a zone have been uploaded.
func (r *Renderer) IsZoneBuilt(proxyIdx int) bool {
	if zs, ok := r.zoneSlots[proxyIdx]; ok {
		return zs.built
	}
	return false
}

// prepareMeshesForSlot is the per-zone equivalent of prepareMeshes.
func (r *Renderer) prepareMeshesForSlot(zs *zoneSlot, zt *ZoneTerrain) {
	if zs.prepared {
		return
	}
	zs.prepared = true
	zs.prepareTime = time.Now()

	groups := make(map[meshKey][]Triangle, 16)
	for _, tile := range zt.Tiles {
		for _, tri := range tile.Triangles {
			key := meshKey{TexID: tri.TexID, Layer: tri.Layer, URate: tri.URate, VRate: tri.VRate}
			groups[key] = append(groups[key], tri)
		}
	}

	zs.pendingTexList = make([]pendingTexture, 0, len(zt.Textures))
	for texID, img := range zt.Textures {
		if img == nil {
			continue
		}
		uploadImg := img
		if isAlphaOnlyTexture(img) {
			uploadImg = alphaToWhiteRGBKeepAlpha(img)
		}
		zs.pendingTexList = append(zs.pendingTexList, pendingTexture{texID: texID, img: uploadImg})
	}
	zs.texCache = make(map[int32]rl.Texture2D, len(zs.pendingTexList))

	zs.pendingGroups = make([]pendingMeshGroup, 0, len(groups))
	for key, tris := range groups {
		n := len(tris) * 3
		verts := make([]float32, n*3)
		uvs := make([]float32, n*2)
		norms := make([]float32, n*3)
		cols := make([]uint8, n*4)
		bbMinX := float32(math.MaxFloat32)
		bbMinY := float32(math.MaxFloat32)
		bbMinZ := float32(math.MaxFloat32)
		bbMaxX := float32(-math.MaxFloat32)
		bbMaxY := float32(-math.MaxFloat32)
		bbMaxZ := float32(-math.MaxFloat32)

		for i, tri := range tris {
			v3 := i * 9
			v2 := i * 6
			v4 := i * 12
			verts[v3+0] = tri.X0; verts[v3+1] = tri.Y0; verts[v3+2] = tri.Z0
			uvs[v2+0] = tri.U0; uvs[v2+1] = tri.V0
			norms[v3+0] = tri.NX0; norms[v3+1] = tri.NY0; norms[v3+2] = tri.NZ0
			verts[v3+3] = tri.X1; verts[v3+4] = tri.Y1; verts[v3+5] = tri.Z1
			uvs[v2+2] = tri.U1; uvs[v2+3] = tri.V1
			norms[v3+3] = tri.NX1; norms[v3+4] = tri.NY1; norms[v3+5] = tri.NZ1
			verts[v3+6] = tri.X2; verts[v3+7] = tri.Y2; verts[v3+8] = tri.Z2
			uvs[v2+4] = tri.U2; uvs[v2+5] = tri.V2
			norms[v3+6] = tri.NX2; norms[v3+7] = tri.NY2; norms[v3+8] = tri.NZ2
			cols[v4+0] = mathutil.FloatToUint8(tri.R0); cols[v4+1] = mathutil.FloatToUint8(tri.G0)
			cols[v4+2] = mathutil.FloatToUint8(tri.B0)
			cols[v4+4] = mathutil.FloatToUint8(tri.R1); cols[v4+5] = mathutil.FloatToUint8(tri.G1)
			cols[v4+6] = mathutil.FloatToUint8(tri.B1)
			cols[v4+8] = mathutil.FloatToUint8(tri.R2); cols[v4+9] = mathutil.FloatToUint8(tri.G2)
			cols[v4+10] = mathutil.FloatToUint8(tri.B2)
			if key.Layer == 0 {
				cols[v4+3] = 255; cols[v4+7] = 255; cols[v4+11] = 255
			} else {
				cols[v4+3] = mathutil.FloatToUint8(tri.A0)
				cols[v4+7] = mathutil.FloatToUint8(tri.A1)
				cols[v4+11] = mathutil.FloatToUint8(tri.A2)
			}
			for _, vx := range []float32{tri.X0, tri.X1, tri.X2} {
				if vx < bbMinX { bbMinX = vx }
				if vx > bbMaxX { bbMaxX = vx }
			}
			for _, vy := range []float32{tri.Y0, tri.Y1, tri.Y2} {
				if vy < bbMinY { bbMinY = vy }
				if vy > bbMaxY { bbMaxY = vy }
			}
			for _, vz := range []float32{tri.Z0, tri.Z1, tri.Z2} {
				if vz < bbMinZ { bbMinZ = vz }
				if vz > bbMaxZ { bbMaxZ = vz }
			}
		}
		pg := pendingMeshGroup{
			key: key, verts: verts, uvs: uvs, norms: norms, cols: cols,
			nTris: len(tris),
			minX: bbMinX, minY: bbMinY, minZ: bbMinZ,
			maxX: bbMaxX, maxY: bbMaxY, maxZ: bbMaxZ,
		}
		pg.uRate = key.URate
		pg.vRate = key.VRate
		zs.pendingGroups = append(zs.pendingGroups, pg)
	}
	log.Printf("terrain: zone %d prepared — %d textures, %d mesh groups in %v",
		zs.proxyIdx, len(zs.pendingTexList), len(zs.pendingGroups), time.Since(zs.prepareTime))
}

// UploadZoneBatch uploads up to maxCount items for a zone. Returns true when done.
func (r *Renderer) UploadZoneBatch(proxyIdx int, zt *ZoneTerrain, maxCount int) bool {
	zs := r.getOrCreateSlot(proxyIdx)
	if zs.built {
		return true
	}

	// Ensure shared resources are ready.
	r.ensureSharedResources()

	r.prepareMeshesForSlot(zs, zt)
	if !zs.prepared {
		return false
	}

	uploaded := 0

	// Upload textures.
	if !zs.texUploaded {
		for uploaded < maxCount && len(zs.pendingTexList) > 0 {
			pt := zs.pendingTexList[0]
			zs.pendingTexList = zs.pendingTexList[1:]
			rlImg := rl.NewImageFromImage(pt.img)
			tex := rl.LoadTextureFromImage(rlImg)
			rl.UnloadImage(rlImg)
			rl.GenTextureMipmaps(&tex)
			rl.SetTextureFilter(tex, rl.FilterTrilinear)
			rl.SetTextureWrap(tex, rl.WrapRepeat)
			zs.texCache[pt.texID] = tex
			uploaded++
		}
		if len(zs.pendingTexList) == 0 {
			zs.texUploaded = true
		}
		if uploaded >= maxCount {
			return false
		}
	}

	// Upload mesh groups.
	for zs.uploadIdx < len(zs.pendingGroups) && uploaded < maxCount {
		pg := &zs.pendingGroups[zs.uploadIdx]
		n := pg.nTris * 3
		mesh := rl.Mesh{
			VertexCount:   int32(n),
			TriangleCount: int32(pg.nTris),
			Vertices:      (*float32)(unsafe.Pointer(&pg.verts[0])),
			Texcoords:     (*float32)(unsafe.Pointer(&pg.uvs[0])),
			Colors:        (*uint8)(unsafe.Pointer(&pg.cols[0])),
		}
		if pg.key.Layer == 0 {
			mesh.Normals = (*float32)(unsafe.Pointer(&pg.norms[0]))
		}
		rl.UploadMesh(&mesh, false)
		runtime.KeepAlive(pg.verts)
		runtime.KeepAlive(pg.uvs)
		runtime.KeepAlive(pg.norms)
		runtime.KeepAlive(pg.cols)
		mesh.Vertices = nil; mesh.Texcoords = nil; mesh.Normals = nil; mesh.Colors = nil

		mat := rl.LoadMaterialDefault()
		if r.shaderLoaded {
			if pg.key.Layer == 0 {
				mat.Shader = r.cutoffShader
			} else {
				mat.Shader = r.overlayShader
			}
		}
		tex := r.whiteTex
		if t, ok := zs.texCache[pg.key.TexID]; ok {
			tex = t
		}
		rl.SetMaterialTexture(&mat, rl.MapDiffuse, tex)

		zs.groups = append(zs.groups, meshGroup{
			Mesh: mesh, Material: mat, Texture: tex,
			Overlay: pg.key.Layer > 0,
			URate: pg.uRate, VRate: pg.vRate,
			MinX: pg.minX, MinY: pg.minY, MinZ: pg.minZ,
			MaxX: pg.maxX, MaxY: pg.maxY, MaxZ: pg.maxZ,
		})
		zs.uploadIdx++
		uploaded++
	}

	if zs.uploadIdx >= len(zs.pendingGroups) {
		zs.built = true
		zs.pendingGroups = nil
		zs.pendingTexList = nil
		log.Printf("terrain: zone %d upload COMPLETE — %d GPU meshes in %v",
			zs.proxyIdx, len(zs.groups), time.Since(zs.prepareTime))
		return true
	}
	return false
}

// ensureSharedResources makes sure whiteTex, shaders, etc. are loaded.
func (r *Renderer) ensureSharedResources() {
	if r.whiteTex.ID == 0 {
		whiteImg := rl.GenImageColor(4, 4, rl.White)
		r.whiteTex = rl.LoadTextureFromImage(whiteImg)
		rl.UnloadImage(whiteImg)
		mat := rl.LoadMaterialDefault()
		rl.SetMaterialTexture(&mat, rl.MapDiffuse, r.whiteTex)
		r.solidMat = mat
	}
	if !r.shaderLoaded {
		r.cutoffShader = rl.LoadShaderFromMemory(terrainAlphaCutoffVS, terrainAlphaCutoffFS)
		r.overlayShader = rl.LoadShaderFromMemory(terrainAlphaCutoffVS, terrainOverlayFS)
		r.litShader = rl.LoadShaderFromMemory(terrainLitVS, terrainLitFS)
		r.locViewPos = rl.GetShaderLocation(r.litShader, "viewPos")
		r.locSunDir = rl.GetShaderLocation(r.litShader, "sunDir")
		r.locSunColor = rl.GetShaderLocation(r.litShader, "sunColor")
		r.locAmbient = rl.GetShaderLocation(r.litShader, "ambientColor")
		r.locFogColor = rl.GetShaderLocation(r.litShader, "fogColor")
		r.locFogStart = rl.GetShaderLocation(r.litShader, "fogStart")
		r.locFogEnd = rl.GetShaderLocation(r.litShader, "fogEnd")
		r.locMatModel = rl.GetShaderLocation(r.litShader, "matModel")
		r.locNumPointLights = rl.GetShaderLocation(r.litShader, "numPointLights")
		r.locPointLightPos = rl.GetShaderLocation(r.litShader, "pointLightPos")
		r.locPointLightColor = rl.GetShaderLocation(r.litShader, "pointLightColor")
		r.locPointLightRadius = rl.GetShaderLocation(r.litShader, "pointLightRadius")
		r.locUvOffsetCutoff = rl.GetShaderLocation(r.cutoffShader, "uvOffset")
		r.locUvOffsetOverlay = rl.GetShaderLocation(r.overlayShader, "uvOffset")
		r.locUvOffsetLit = rl.GetShaderLocation(r.litShader, "uvOffset")
		r.litLoaded = true
		r.LightingOn = true
		r.shaderLoaded = true
	}
}

// DrawZone renders meshes for a specific zone slot.
// isPrimary=true for the zone the player stands in; secondary zones are drawn
// first, then primary zone last with GL_LEQUAL so it wins at shared boundary edges.
func (r *Renderer) DrawZone(proxyIdx int, f *frustum.Frustum, isPrimary bool, visibleRooms map[int]bool) (drawn, total int) {
	zs, ok := r.zoneSlots[proxyIdx]
	if !ok || len(zs.groups) == 0 {
		return 0, 0
	}

	total = len(zs.groups)
	identity := rl.MatrixIdentity()
	t := rl.GetTime()
	rl.BeginBlendMode(rl.BlendAlpha)

	useLit := r.LightingOn && r.litLoaded
	hasPortalCull := len(visibleRooms) > 0

	// Pass 1: base layers.
	for i := range zs.groups {
		g := &zs.groups[i]
		if g.Overlay {
			continue
		}
		// Portal room culling: skip groups in non-visible rooms
		if hasPortalCull && g.RoomID >= 0 && !visibleRooms[g.RoomID] {
			continue
		}
		if f != nil && !f.TestAABB(g.MinX, g.MinY, g.MinZ, g.MaxX, g.MaxY, g.MaxZ) {
			continue
		}
		animated := r.setUvOffset(g, t, useLit)
		if useLit {
			litMat := rl.LoadMaterialDefault()
			litMat.Shader = r.litShader
			rl.SetMaterialTexture(&litMat, rl.MapDiffuse, g.Texture)
			rl.DrawMesh(g.Mesh, litMat, identity)
		} else {
			rl.DrawMesh(g.Mesh, g.Material, identity)
		}
		if animated {
			r.clearUvOffset(g, useLit)
		}
		drawn++
	}

	// Pass 2: overlay layers.
	if !r.OverlaysOff {
		gfx.DepthFuncLEqual()
		rl.DisableDepthMask()
		for i := range zs.groups {
			g := &zs.groups[i]
			if !g.Overlay {
				continue
			}
			if hasPortalCull && g.RoomID >= 0 && !visibleRooms[g.RoomID] {
				continue
			}
			if f != nil && !f.TestAABB(g.MinX, g.MinY, g.MinZ, g.MaxX, g.MaxY, g.MaxZ) {
				continue
			}
			animated := r.setUvOffset(g, t, useLit)
			rl.DrawMesh(g.Mesh, g.Material, identity)
			if animated {
				r.clearUvOffset(g, useLit)
			}
			drawn++
		}
		rl.EnableDepthMask()
		gfx.DepthFuncLess()
	}

	rl.EndBlendMode()
	return drawn, total
}

// DrawGroundFill renders a large opaque greenish-tan base plane.
// PS2 draws this as the first terrain pass — a solid color underneath all
// textured terrain. Fills sub-pixel cracks between terrain mesh strips.
// Call BEFORE DrawZone.
func (r *Renderer) DrawGroundFill(centerX, centerZ, groundY float32) {
	if !r.groundFillReady {
		// Create a large quad centered on origin — we'll position via matrix.
		const half = float32(3000)
		positions := []float32{
			-half, 0, -half,
			half, 0, -half,
			half, 0, half,
			-half, 0, -half,
			half, 0, half,
			-half, 0, half,
		}
		uvs := make([]float32, 12) // dummy UVs
		norms := []float32{
			0, 1, 0, 0, 1, 0, 0, 1, 0,
			0, 1, 0, 0, 1, 0, 0, 1, 0,
		}
		// Vertex colors set to fog color (updated each frame via material color).
		cols := make([]uint8, 24)
		for i := 0; i < 24; i += 4 {
			cols[i] = 255
			cols[i+1] = 255
			cols[i+2] = 255
			cols[i+3] = 255
		}

		r.groundFillMesh = rl.Mesh{
			VertexCount:   6,
			TriangleCount: 2,
			Vertices:      (*float32)(unsafe.Pointer(&positions[0])),
			Texcoords:     (*float32)(unsafe.Pointer(&uvs[0])),
			Normals:       (*float32)(unsafe.Pointer(&norms[0])),
			Colors:        (*uint8)(unsafe.Pointer(&cols[0])),
		}
		rl.UploadMesh(&r.groundFillMesh, false)
		runtime.KeepAlive(positions)
		runtime.KeepAlive(uvs)
		runtime.KeepAlive(norms)
		runtime.KeepAlive(cols)
		r.groundFillMesh.Vertices = nil
		r.groundFillMesh.Texcoords = nil
		r.groundFillMesh.Normals = nil
		r.groundFillMesh.Colors = nil
		r.groundFillReady = true
	}

	// Greenish-tan base color (PS2 draws this under all terrain).
	mat := rl.LoadMaterialDefault()
	if r.whiteTex.ID != 0 {
		rl.SetMaterialTexture(&mat, rl.MapDiffuse, r.whiteTex)
	}
	maps := (*[32]rl.MaterialMap)(unsafe.Pointer(mat.Maps))
	maps[rl.MapDiffuse].Color = rl.NewColor(120, 115, 80, 255)

	transform := rl.Matrix{
		M0: 1, M4: 0, M8: 0, M12: centerX,
		M1: 0, M5: 1, M9: 0, M13: groundY,
		M2: 0, M6: 0, M10: 1, M14: centerZ,
		M3: 0, M7: 0, M11: 0, M15: 1,
	}
	rl.DrawMesh(r.groundFillMesh, mat, transform)
}

// isAlphaOnlyTexture returns true if all RGB pixels are zero (texture data
// is entirely in the alpha channel). These are biome detail masks (sand
// grain, grass blades) that need special rendering.
func isAlphaOnlyTexture(img *image.NRGBA) bool {
	pix := img.Pix
	for i := 0; i < len(pix); i += 4 {
		if pix[i] != 0 || pix[i+1] != 0 || pix[i+2] != 0 {
			return false
		}
	}
	return true
}

// alphaToWhiteRGBKeepAlpha converts an alpha-only texture to white RGB while
// keeping the original alpha channel. The white pixels get tinted by vertex
// colors (via raylib's texture×color multiply), and the alpha channel controls
// how much the overlay shows vs the base terrain underneath.
func alphaToWhiteRGBKeepAlpha(img *image.NRGBA) *image.NRGBA {
	bounds := img.Bounds()
	out := image.NewNRGBA(bounds)
	pix := img.Pix
	opix := out.Pix
	for i := 0; i < len(pix); i += 4 {
		opix[i+0] = 255   // R = white
		opix[i+1] = 255   // G = white
		opix[i+2] = 255   // B = white
		opix[i+3] = pix[i+3] // A = keep original
	}
	return out
}

// --- PS2 Time-of-Day Color Table (decompiled from VIScene+0x1FF4) ---
//
// 24 keyframes, one per hour. Each has ambient (RGB), sun (RGB), and fog (RGB).
// Values from PS2 CLIENT.AUT, byte-scale 0-255 converted to float 0.0-1.0.
// SetupTimeOfDayLights linearly interpolates between adjacent keyframes.

type todKeyframe struct {
	ambR, ambG, ambB float32 // ambient light
	sunR, sunG, sunB float32 // directional sun/moon color
	fogR, fogG, fogB float32 // fog/sky gradient
}

// ps2TODTable holds the 24-hour PS2 lighting keyframes.
// Ambient: blue-tinted at night (elevated B channel).
// Sun: indigo→golden sunrise→warm white noon→golden sunset→indigo.
// Fog: derived from sky gradient colors (offsets 76-92 in PS2 entry).
var ps2TODTable = [24]todKeyframe{
	// Hour 0: Midnight
	{70.0 / 255, 70.0 / 255, 100.0 / 255, 70.0 / 255, 75.0 / 255, 190.0 / 255, 20.0 / 255, 20.0 / 255, 40.0 / 255},
	// Hour 1
	{75.0 / 255, 75.0 / 255, 100.0 / 255, 80.0 / 255, 0.0 / 255, 220.0 / 255, 20.0 / 255, 20.0 / 255, 45.0 / 255},
	// Hour 2
	{75.0 / 255, 75.0 / 255, 100.0 / 255, 85.0 / 255, 0.0 / 255, 240.0 / 255, 22.0 / 255, 22.0 / 255, 50.0 / 255},
	// Hour 3
	{75.0 / 255, 75.0 / 255, 100.0 / 255, 90.0 / 255, 0.0 / 255, 255.0 / 255, 25.0 / 255, 25.0 / 255, 55.0 / 255},
	// Hour 4: Pre-dawn purple
	{75.0 / 255, 75.0 / 255, 100.0 / 255, 120.0 / 255, 55.0 / 255, 240.0 / 255, 40.0 / 255, 35.0 / 255, 70.0 / 255},
	// Hour 5: Lavender dawn
	{75.0 / 255, 75.0 / 255, 100.0 / 255, 170.0 / 255, 55.0 / 255, 240.0 / 255, 80.0 / 255, 60.0 / 255, 100.0 / 255},
	// Hour 6: Golden sunrise
	{85.0 / 255, 85.0 / 255, 85.0 / 255, 255.0 / 255, 175.0 / 255, 55.0 / 255, 180.0 / 255, 140.0 / 255, 100.0 / 255},
	// Hour 7: Warm yellow-orange
	{85.0 / 255, 85.0 / 255, 85.0 / 255, 255.0 / 255, 210.0 / 255, 100.0 / 255, 200.0 / 255, 190.0 / 255, 170.0 / 255},
	// Hour 8: Warm yellow morning
	{95.0 / 255, 95.0 / 255, 95.0 / 255, 255.0 / 255, 248.0 / 255, 160.0 / 255, 210.0 / 255, 210.0 / 255, 210.0 / 255},
	// Hour 9: Warm white-yellow
	{100.0 / 255, 100.0 / 255, 100.0 / 255, 255.0 / 255, 250.0 / 255, 200.0 / 255, 220.0 / 255, 220.0 / 255, 225.0 / 255},
	// Hour 10: Warm white
	{100.0 / 255, 100.0 / 255, 100.0 / 255, 255.0 / 255, 250.0 / 255, 210.0 / 255, 225.0 / 255, 225.0 / 255, 230.0 / 255},
	// Hour 11: Near-white warm
	{102.0 / 255, 102.0 / 255, 102.0 / 255, 255.0 / 255, 250.0 / 255, 230.0 / 255, 230.0 / 255, 230.0 / 255, 235.0 / 255},
	// Hour 12: Noon
	{102.0 / 255, 102.0 / 255, 102.0 / 255, 255.0 / 255, 250.0 / 255, 240.0 / 255, 230.0 / 255, 230.0 / 255, 240.0 / 255},
	// Hour 13: Afternoon (mirrors 11)
	{100.0 / 255, 100.0 / 255, 100.0 / 255, 255.0 / 255, 250.0 / 255, 230.0 / 255, 230.0 / 255, 230.0 / 255, 235.0 / 255},
	// Hour 14: (mirrors 10)
	{100.0 / 255, 100.0 / 255, 100.0 / 255, 255.0 / 255, 250.0 / 255, 210.0 / 255, 225.0 / 255, 225.0 / 255, 230.0 / 255},
	// Hour 15: (mirrors 9)
	{100.0 / 255, 100.0 / 255, 100.0 / 255, 255.0 / 255, 250.0 / 255, 200.0 / 255, 220.0 / 255, 220.0 / 255, 225.0 / 255},
	// Hour 16: Late afternoon (mirrors 8)
	{95.0 / 255, 95.0 / 255, 95.0 / 255, 255.0 / 255, 248.0 / 255, 160.0 / 255, 210.0 / 255, 210.0 / 255, 210.0 / 255},
	// Hour 17: Warm orange (mirrors 7)
	{75.0 / 255, 75.0 / 255, 100.0 / 255, 255.0 / 255, 210.0 / 255, 100.0 / 255, 200.0 / 255, 160.0 / 255, 120.0 / 255},
	// Hour 18: Warm orange sunset
	{75.0 / 255, 75.0 / 255, 100.0 / 255, 255.0 / 255, 175.0 / 255, 100.0 / 255, 160.0 / 255, 120.0 / 255, 80.0 / 255},
	// Hour 19: Golden-green dusk
	{75.0 / 255, 75.0 / 255, 100.0 / 255, 170.0 / 255, 175.0 / 255, 55.0 / 255, 100.0 / 255, 80.0 / 255, 70.0 / 255},
	// Hour 20: Purple twilight (mirrors 4)
	{75.0 / 255, 75.0 / 255, 100.0 / 255, 120.0 / 255, 55.0 / 255, 240.0 / 255, 50.0 / 255, 40.0 / 255, 70.0 / 255},
	// Hour 21: Deep purple
	{75.0 / 255, 75.0 / 255, 100.0 / 255, 90.0 / 255, 55.0 / 255, 255.0 / 255, 35.0 / 255, 30.0 / 255, 60.0 / 255},
	// Hour 22: Dark blue-violet (mirrors 2)
	{75.0 / 255, 75.0 / 255, 100.0 / 255, 85.0 / 255, 0.0 / 255, 240.0 / 255, 25.0 / 255, 25.0 / 255, 50.0 / 255},
	// Hour 23: Deep blue-violet (mirrors 1)
	{75.0 / 255, 75.0 / 255, 100.0 / 255, 80.0 / 255, 0.0 / 255, 220.0 / 255, 22.0 / 255, 22.0 / 255, 45.0 / 255},
}

// ps2TimeOfDayColors interpolates the PS2 24-keyframe lighting table at the
// given fractional game hour (0.0-23.999) and returns sun, ambient, fog colors.
func ps2TimeOfDayColors(gameHour float32) (sun, ambient, fog [3]float32) {
	// Clamp to valid range
	if gameHour < 0 {
		gameHour = 0
	}
	if gameHour >= 24 {
		gameHour -= 24
	}

	idx := int(gameHour)
	if idx >= 24 {
		idx = 23
	}
	next := (idx + 1) % 24
	t := gameHour - float32(idx) // fractional interpolation factor

	a := &ps2TODTable[idx]
	b := &ps2TODTable[next]

	lerp := func(v0, v1, f float32) float32 { return v0 + (v1-v0)*f }

	sun = [3]float32{
		lerp(a.sunR, b.sunR, t),
		lerp(a.sunG, b.sunG, t),
		lerp(a.sunB, b.sunB, t),
	}
	ambient = [3]float32{
		lerp(a.ambR, b.ambR, t),
		lerp(a.ambG, b.ambG, t),
		lerp(a.ambB, b.ambB, t),
	}
	fog = [3]float32{
		lerp(a.fogR, b.fogR, t),
		lerp(a.fogG, b.fogG, t),
		lerp(a.fogB, b.fogB, t),
	}
	return
}
