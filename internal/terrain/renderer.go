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

// PS2 GS alpha-blend modes, VIMaterial's layer m_blendMode (ConstructTextPass
// writes the ALPHA_1 register from it, vimaterial.cpp).
const (
	blendOpaque   = 0 // 0x64: Cs, alpha-tested at 128 (TEST 0x3080B)
	blendAdd      = 1 // 0x48: Cs·As + Cd
	blendSubtract = 2 // 0x42: Cd − Cs·As
	blendAlpha    = 3 // 0x44: (Cs − Cd)·As + Cd
	blendMultiply = 4 // 0x89: Cd·As, unfogged — the terrain detail overlays
)

// meshGroup holds a GPU mesh and its material for one texture.
type meshGroup struct {
	Mesh     rl.Mesh
	Material rl.Material
	Texture  rl.Texture2D // stored separately for lit shader material swap
	Overlay  bool         // true for layer > 0 (drawn over the base at equal depth)
	// Material layer state (VIMaterial layer): blend mode, layer colour, and
	// the material's emissive colour, which the PS2 adds to the ambient.
	Blend    int32
	Color    [4]float32
	Emissive [3]float32
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
	// AABB
	minX, minY, minZ float32
	maxX, maxY, maxZ float32
}

// pendingTexture holds a CPU-prepared texture image ready for GPU upload.
type pendingTexture struct {
	texID int32
	img   *image.NRGBA
}

// Unlit shader (F4 / seamtest -lighting=false): the texture times the layer
// colour, alpha-tested like an opaque layer. A debugging view only.
const terrainUnlitVS = `#version 330
in vec3 vertexPosition;
in vec2 vertexTexCoord;
uniform mat4 mvp;
uniform vec2 uvOffset;
out vec2 fragTexCoord;
void main() {
    fragTexCoord = vertexTexCoord + uvOffset;
    gl_Position = mvp * vec4(vertexPosition, 1.0);
}
`

const terrainUnlitFS = `#version 330
in vec2 fragTexCoord;
uniform sampler2D texture0;
uniform vec4 layerColor;
out vec4 finalColor;
void main() {
    vec4 color = texture(texture0, fragTexCoord) * layerColor;
    if (color.a < 0.5) discard;
    finalColor = vec4(color.rgb, 1.0);
}
`

// Lit terrain shader — the PS2 VU1 strip programs VIRStrip2/VIRStrip3, which
// light every world vertex (Gouraud) as
//
//	lit = min(1, vtx.rgb + ambient + sun·max(0, −N·D)·vtx.a + lantern)
//
// where D is the direction the sunlight travels (m_tod[h].m_b), the vertex
// colour's RGB is light ADDED to the rest (a ColorBuffer replaces it with the
// baked lighting) and its alpha gates the sun, so it is a shadow term and
// never an opacity. The lantern falls off linearly, (1 − min(d/r, 1)), times
// max(0, cos). The microcode then multiplies by the layer colour and the GS
// modulates the texture by the result. Fog is linear in view depth; a
// multiply (blend 4) layer is drawn unfogged.
const terrainLitVS = `#version 330
in vec3 vertexPosition;
in vec2 vertexTexCoord;
in vec3 vertexNormal;
in vec4 vertexColor;
uniform mat4 mvp;
uniform vec2 uvOffset;
uniform vec3 sunDir;       // normalized, the direction the light TRAVELS (PS2 m_tod[h].m_b)
uniform vec3 sunColor;
uniform vec3 ambientColor; // scene ambient + the material's emissive
uniform vec3 lanternPos;
uniform vec3 lanternColor;
uniform float lanternRange; // 0 = off
uniform float fogStart;
uniform float fogEnd;
out vec2 fragTexCoord;
out vec3 fragLight;
out float fragFog;
void main() {
    fragTexCoord = vertexTexCoord + uvOffset;
    vec3 N = vertexNormal;
    vec3 lit = vertexColor.rgb + ambientColor
             + sunColor * (max(dot(N, -sunDir), 0.0) * vertexColor.a);
    if (lanternRange > 0.0) {
        vec3 d = lanternPos - vertexPosition;
        float len = length(d);
        if (len > 0.0) {
            float fall = 1.0 - min(len / lanternRange, 1.0);
            lit += lanternColor * (fall * max(dot(d, N) / len, 0.0));
        }
    }
    fragLight = min(lit, vec3(1.0));
    gl_Position = mvp * vec4(vertexPosition, 1.0);
    fragFog = clamp((gl_Position.w - fogStart) / (fogEnd - fogStart), 0.0, 1.0);
}
`

const terrainLitFS = `#version 330
in vec2 fragTexCoord;
in vec3 fragLight;
in float fragFog;
uniform sampler2D texture0;
uniform vec4 layerColor;
uniform int blendMode;
uniform vec3 fogColor;
out vec4 finalColor;
void main() {
    vec4 texel = texture(texture0, fragTexCoord);
    // A blended layer's vertex alpha goes out as half the layer alpha (and the
    // GS reads 0x80 as 1.0), so As is the texel alpha over 255 either way.
    float a = texel.a * layerColor.a;
    if (blendMode == 4) {
        finalColor = vec4(0.0, 0.0, 0.0, a); // Cd·As via glBlendFunc(ZERO, SRC_ALPHA)
        return;
    }
    if (blendMode == 0 && a < 0.5) discard; // TEST: alpha >= 128
    vec3 rgb = mix(texel.rgb * layerColor.rgb * fragLight, fogColor, fragFog);
    finalColor = vec4(rgb, blendMode == 0 ? 1.0 : a);
}
`

// zoneSlot holds per-zone GPU mesh state for multi-zone rendering.
type zoneSlot struct {
	proxyIdx       int
	groups         []meshGroup
	built          bool
	prepared       bool
	pendingTexList []pendingTexture
	texCache       map[int32]rl.Texture2D
	texUploaded    bool
	roomsAssigned  bool
	pendingGroups  []pendingMeshGroup
	uploadIdx      int
	prepareTime    time.Time
}

// Renderer draws zone terrain as 3D meshes.
type Renderer struct {
	whiteTex rl.Texture2D // solid white texture for untextured layers
	solidMat rl.Material  // opaque white material for the ground fill

	unlitShader  rl.Shader // F4 off: texture only
	shaderLoaded bool

	// Lit terrain shader (PS2 lighting + fog)
	litShader      rl.Shader
	litLoaded      bool
	LightingOn     bool // toggle with F4 — when false, uses the unlit shader
	locSunDir      int32
	locSunColor    int32
	locAmbient     int32
	locFogColor    int32
	locFogStart    int32
	locFogEnd      int32
	locLanternPos  int32
	locLanternCol  int32
	locLanternR    int32
	locLayerColor  int32
	locBlendMode   int32
	locUvOffsetLit int32
	locUnlitLayer  int32
	locUnlitUv     int32

	// Per-frame lighting, kept so DrawZone can add each material's emissive.
	ambient [3]float32

	// Overcast is the atmosphere's overcast interpolation (0..1), set by the
	// client each frame. It pulls the ambient toward 0.9·red grey and the sun
	// toward black, as SetupTimeOfDayLights does.
	Overcast float32
	// Flash is the weather's lightning brightness this frame and Outdoors the
	// outdoors factor: a flash whitens the sun by flash·outdoors and pins its
	// direction to 10:00's (SetupTimeOfDayLights).
	Flash    float32
	Outdoors float32

	// Profile is the terrain-profile blend at the camera and EnvFlags the
	// liquid the camera is in (water 1, slime 2, lava 4, mud 8). Together they
	// decide the fog (SetupTimeOfDayFog) and how far the overcast dims the
	// lights. Set by the client each frame.
	Profile  ProfileMix
	EnvFlags uint32

	// SkyColor is this frame's sky colour — VIScene::SetupTimeOfDaySky's,
	// which is also the fog colour out of liquid — for the sky dome.
	SkyColor [3]float32

	// Player radial light (lantern) — the PS2's one point light.
	playerLightOn     bool
	playerLightPos    [3]float32
	playerLightRadius float32
	playerLightColor  [3]float32

	// Ground fill plane — solid base layer under terrain (PS2-style).
	groundFillMesh  rl.Mesh
	groundFillReady bool

	OverlaysOff bool // F5 toggle: skip overlay rendering for debugging

	// Multi-zone mesh state (PS2 radial streaming)
	zoneSlots map[int]*zoneSlot // proxyIdx → per-zone mesh data
}

// NewRenderer creates a terrain renderer. Must be called after rl.InitWindow.
func NewRenderer() *Renderer {
	return &Renderer{}
}

// meshKey groups triangles by texture, layer and material layer state.
type meshKey struct {
	TexID    int32
	Layer    int32
	URate    float32
	VRate    float32
	Blend    int32
	Color    [4]byte
	Emissive [4]byte
}

// SetPlayerLight enables/disables the player lantern — the PS2's only point
// light (VIRaster::MaxPointLights returns 1, so the zone point-light loop in
// SetupTimeOfDayLights, behind `>= 2`, never runs).
func (r *Renderer) SetPlayerLight(on bool, x, y, z, radius float32, cr, cg, cb float32) {
	r.playerLightOn = on && radius > 0
	r.playerLightPos = [3]float32{x, y, z}
	r.playerLightRadius = radius
	r.playerLightColor = [3]float32{cr, cg, cb}
}

// SetLightingUniforms sets per-frame lighting uniforms. Call before DrawZone.
// camPos is the camera world position. gameHour is 0-24 for day/night cycle.
func (r *Renderer) SetLightingUniforms(camX, camY, camZ, gameHour float32) {
	if !r.litLoaded || !r.LightingOn {
		return
	}

	// PS2 InitTimeOfDay (viscene.cpp): m_tod[h].m_b = (-sin a, cos a, 0) with
	// a = hour * 15°. That is the direction the light TRAVELS, not the way to
	// the sun: hour 12 gives (0,-1,0), straight down; hour 6 gives (-1,0,0),
	// light moving toward -X from a sun in the +X sky; hour 0 gives (0,1,0),
	// from below the horizon. The shader lights with max(0, -N·m_b).
	sunAngleRad := float64(gameHour) * 15.0 * math.Pi / 180.0
	sunDir := [3]float32{
		-float32(math.Sin(sunAngleRad)),
		float32(math.Cos(sunAngleRad)),
		0,
	}
	rl.SetShaderValue(r.litShader, r.locSunDir, sunDir[:], rl.ShaderUniformVec3)

	// PS2 24-keyframe time-of-day colours (VIScene::InitTimeOfDay): the sun
	// (m_c[0], which is also the clear sky m_d[0]), the ambient (m_a[0]) and
	// the overcast sky's grey (m_d[1]).
	sunColor, ambientColor, grey := ps2TimeOfDayColors(gameHour)
	skyClear := sunColor

	// Overcast (SetupTimeOfDayLights): the ambient blends toward its own red
	// channel times 0.9, as grey, and the sun toward black, by the overcast
	// times the profiles' own weight (0.5 in the swampy and murky ones).
	o := r.Overcast
	if k := o * r.Profile.OvercastWeight(); k > 0 {
		g := ambientColor[0] * 0.9
		for i := range ambientColor {
			ambientColor[i] += (g - ambientColor[i]) * k
			sunColor[i] -= sunColor[i] * k
		}
	}
	r.ambient = ambientColor

	if r.Flash > 0 {
		k := r.Flash * r.Outdoors
		sunColor[0] += (1 - sunColor[0]) * k * 0.9
		sunColor[1] += (1 - sunColor[1]) * k * 0.9
		sunColor[2] += (1 - sunColor[2]) * k
		a := 10 * 15.0 * math.Pi / 180.0
		sunDir = [3]float32{-float32(math.Sin(a)), float32(math.Cos(a)), 0}
		rl.SetShaderValue(r.litShader, r.locSunDir, sunDir[:], rl.ShaderUniformVec3)
	}

	// The sky colour (SetupTimeOfDaySky) and fog (SetupTimeOfDayFog): the
	// clear sky blended toward the overcast grey by the overcast, tinted by
	// the profiles, between the profiles' distances — except in a liquid,
	// where a fixed tint over the light colour closes in to 20 units (5 in
	// mud). The far distance never exceeds the 900-unit streaming radius.
	tint := r.Profile.Color()
	var sky [3]float32
	for i := range sky {
		sky[i] = (skyClear[i] + (grey[i]-skyClear[i])*o) * tint[i]
	}
	r.SkyColor = sky
	fogColor := sky
	fogStart, fogEnd := r.Profile.ClipPlanes()
	light := [3]float32{max(sunColor[0], 0.4), max(sunColor[1], 0.4), max(sunColor[2], 0.4)}
	switch env := r.EnvFlags; {
	case env&0x1 != 0: // water
		fogColor = [3]float32{0, light[1] * 0.5, light[2]}
		fogStart, fogEnd = 0, min(20, fogEnd)
	case env&0x4 != 0: // lava
		fogColor = [3]float32{1, 0, 0}
		fogStart, fogEnd = 0, min(20, fogEnd)
	case env&0x2 != 0: // slime
		fogColor = [3]float32{light[0] * 0.18, light[1] * 0.56, light[2] * 0.313}
		fogStart, fogEnd = 0, min(20, fogEnd)
	case env&0x8 != 0: // mud
		fogColor = [3]float32{light[0] * 70 / 255, light[1] * 65 / 255, light[2] * 50 / 255}
		fogStart, fogEnd = 0, min(5, fogEnd)
	}
	fogEnd = min(900, fogEnd)

	rl.SetShaderValue(r.litShader, r.locSunColor, sunColor[:], rl.ShaderUniformVec3)
	rl.SetShaderValue(r.litShader, r.locAmbient, ambientColor[:], rl.ShaderUniformVec3)
	rl.SetShaderValue(r.litShader, r.locFogColor, fogColor[:], rl.ShaderUniformVec3)
	rl.SetShaderValue(r.litShader, r.locFogStart, []float32{fogStart}, rl.ShaderUniformFloat)
	rl.SetShaderValue(r.litShader, r.locFogEnd, []float32{fogEnd}, rl.ShaderUniformFloat)

	lanternRange := float32(0)
	if r.playerLightOn {
		lanternRange = r.playerLightRadius
	}
	// Copies: a slice into the Renderer itself would hand cgo a Go pointer to
	// memory that holds Go pointers, which the cgo check rejects.
	pos, col := r.playerLightPos, r.playerLightColor
	rl.SetShaderValue(r.litShader, r.locLanternPos, pos[:], rl.ShaderUniformVec3)
	rl.SetShaderValue(r.litShader, r.locLanternCol, col[:], rl.ShaderUniformVec3)
	rl.SetShaderValue(r.litShader, r.locLanternR, []float32{lanternRange}, rl.ShaderUniformFloat)
}

// ToggleLighting flips the lighting shader on/off. Returns new state.
func (r *Renderer) ToggleLighting() bool {
	r.LightingOn = !r.LightingOn
	return r.LightingOn
}

// uvOffset returns a mesh group's UV scroll at time t.
func uvOffset(g *meshGroup, t float64) [2]float32 {
	ft := float32(t)
	// PS2 VIMaterial::Process wraps offsets to [-1, 1] (fractional part).
	// We compute rate*time mod 1.0 to avoid float32 precision loss over long sessions.
	u := g.URate * ft
	v := g.VRate * ft
	u -= float32(int(u)) // fractional part, preserves sign
	v -= float32(int(v))
	return [2]float32{u, v}
}

// setGroupBlend selects the GL blend equation for a PS2 layer blend mode.
func setGroupBlend(mode int32) {
	const (
		glZero            = 0
		glOne             = 1
		glSrcAlpha        = 0x0302
		glFuncAdd         = 0x8006
		glFuncRevSubtract = 0x800B
	)
	switch mode {
	case blendAdd:
		rl.BeginBlendMode(rl.BlendAdditive)
	case blendSubtract:
		rl.SetBlendFactors(glSrcAlpha, glOne, glFuncRevSubtract)
		rl.BeginBlendMode(rl.BlendCustom)
	case blendMultiply:
		rl.SetBlendFactors(glZero, glSrcAlpha, glFuncAdd)
		rl.BeginBlendMode(rl.BlendCustom)
	default:
		rl.BeginBlendMode(rl.BlendAlpha)
	}
}

// AssignRooms gives each of one zone's mesh groups the smallest of the zone's
// rooms whose box holds the WHOLE group, or -1 — never culled. A group is
// every triangle of one material across the zone, so most span many rooms
// and stay -1; deciding by the group's centre hid geometry that was in view.
// It used to walk every zone's groups with the primary zone's rooms.
func (r *Renderer) AssignRooms(proxyIdx int, rooms []esf.ZoneRoom) {
	zs, ok := r.zoneSlots[proxyIdx]
	if !ok || !zs.built {
		return
	}
	for i := range zs.groups {
		g := &zs.groups[i]
		g.RoomID = -1
		best := float32(math.MaxFloat32)
		for ri := range rooms {
			b := rooms[ri].BBox
			if g.MinX >= b.MinX && g.MaxX <= b.MaxX && g.MinY >= b.MinY && g.MaxY <= b.MaxY &&
				g.MinZ >= b.MinZ && g.MaxZ <= b.MaxZ {
				if v := (b.MaxX - b.MinX) * (b.MaxY - b.MinY) * (b.MaxZ - b.MinZ); v < best {
					best = v
					g.RoomID = ri
				}
			}
		}
	}
	zs.roomsAssigned = true
	n := 0
	for i := range zs.groups {
		if zs.groups[i].RoomID >= 0 {
			n++
		}
	}
	log.Printf("terrain: zone %d — %d of %d mesh groups sit in one room", proxyIdx, n, len(zs.groups))
}

// RoomsAssigned reports whether AssignRooms has run on the zone's built meshes.
func (r *Renderer) RoomsAssigned(proxyIdx int) bool {
	zs, ok := r.zoneSlots[proxyIdx]
	return ok && zs.roomsAssigned
}

// Cleanup unloads all GPU resources.
func (r *Renderer) Cleanup() {
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
		rl.UnloadShader(r.unlitShader)
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
		// Each group's material is its own LoadMaterialDefault allocation (the
		// maps array); the shaders and textures it points at are shared, so
		// only the maps are freed here.
		gfx.FreeMaterialMaps(&zs.groups[i].Material)
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

// prepareMeshesForSlot performs CPU-only work: groups triangles, prepares
// vertex arrays and AABBs, and lists the textures to upload. No GPU calls.
func (r *Renderer) prepareMeshesForSlot(zs *zoneSlot, zt *ZoneTerrain) {
	if zs.prepared {
		return
	}
	zs.prepared = true
	zs.prepareTime = time.Now()

	groups := make(map[meshKey][]Triangle, 16)
	for _, tile := range zt.Tiles {
		for _, tri := range tile.Triangles {
			key := meshKey{
				TexID: tri.TexID, Layer: tri.Layer, URate: tri.URate, VRate: tri.VRate,
				Blend: tri.Blend, Color: tri.LayerColor, Emissive: tri.Emissive,
			}
			groups[key] = append(groups[key], tri)
		}
	}

	zs.pendingTexList = make([]pendingTexture, 0, len(zt.Textures))
	for texID, img := range zt.Textures {
		if img == nil {
			continue
		}
		zs.pendingTexList = append(zs.pendingTexList, pendingTexture{texID: texID, img: img})
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
			// Vertex colour: RGB is added light, alpha the sun gate — for
			// every layer, since the PS2 lights a strip once for all its passes.
			cols[v4+0] = mathutil.FloatToUint8(tri.R0); cols[v4+1] = mathutil.FloatToUint8(tri.G0)
			cols[v4+2] = mathutil.FloatToUint8(tri.B0); cols[v4+3] = mathutil.FloatToUint8(tri.A0)
			cols[v4+4] = mathutil.FloatToUint8(tri.R1); cols[v4+5] = mathutil.FloatToUint8(tri.G1)
			cols[v4+6] = mathutil.FloatToUint8(tri.B1); cols[v4+7] = mathutil.FloatToUint8(tri.A1)
			cols[v4+8] = mathutil.FloatToUint8(tri.R2); cols[v4+9] = mathutil.FloatToUint8(tri.G2)
			cols[v4+10] = mathutil.FloatToUint8(tri.B2); cols[v4+11] = mathutil.FloatToUint8(tri.A2)
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
		zs.pendingGroups = append(zs.pendingGroups, pendingMeshGroup{
			key: key, verts: verts, uvs: uvs, norms: norms, cols: cols,
			nTris: len(tris),
			minX: bbMinX, minY: bbMinY, minZ: bbMinZ,
			maxX: bbMaxX, maxY: bbMaxY, maxZ: bbMaxZ,
		})
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
			Normals:       (*float32)(unsafe.Pointer(&pg.norms[0])),
			Colors:        (*uint8)(unsafe.Pointer(&pg.cols[0])),
		}
		rl.UploadMesh(&mesh, false)
		runtime.KeepAlive(pg.verts)
		runtime.KeepAlive(pg.uvs)
		runtime.KeepAlive(pg.norms)
		runtime.KeepAlive(pg.cols)
		mesh.Vertices = nil; mesh.Texcoords = nil; mesh.Normals = nil; mesh.Colors = nil

		tex := r.whiteTex
		if t, ok := zs.texCache[pg.key.TexID]; ok {
			tex = t
		}
		mat := rl.LoadMaterialDefault()
		mat.Shader = r.litShader
		rl.SetMaterialTexture(&mat, rl.MapDiffuse, tex)

		k := pg.key
		zs.groups = append(zs.groups, meshGroup{
			Mesh: mesh, Material: mat, Texture: tex,
			Overlay:  k.Layer > 0,
			Blend:    k.Blend,
			Color:    [4]float32{float32(k.Color[0]) / 255, float32(k.Color[1]) / 255, float32(k.Color[2]) / 255, float32(k.Color[3]) / 255},
			Emissive: [3]float32{float32(k.Emissive[0]) / 255, float32(k.Emissive[1]) / 255, float32(k.Emissive[2]) / 255},
			URate:    k.URate, VRate: k.VRate,
			MinX: pg.minX, MinY: pg.minY, MinZ: pg.minZ,
			MaxX: pg.maxX, MaxY: pg.maxY, MaxZ: pg.maxZ,
			RoomID: -1, // exterior until AssignRooms says otherwise
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
		r.unlitShader = rl.LoadShaderFromMemory(terrainUnlitVS, terrainUnlitFS)
		r.locUnlitLayer = rl.GetShaderLocation(r.unlitShader, "layerColor")
		r.locUnlitUv = rl.GetShaderLocation(r.unlitShader, "uvOffset")
		r.litShader = rl.LoadShaderFromMemory(terrainLitVS, terrainLitFS)
		r.locSunDir = rl.GetShaderLocation(r.litShader, "sunDir")
		r.locSunColor = rl.GetShaderLocation(r.litShader, "sunColor")
		r.locAmbient = rl.GetShaderLocation(r.litShader, "ambientColor")
		r.locFogColor = rl.GetShaderLocation(r.litShader, "fogColor")
		r.locFogStart = rl.GetShaderLocation(r.litShader, "fogStart")
		r.locFogEnd = rl.GetShaderLocation(r.litShader, "fogEnd")
		r.locLanternPos = rl.GetShaderLocation(r.litShader, "lanternPos")
		r.locLanternCol = rl.GetShaderLocation(r.litShader, "lanternColor")
		r.locLanternR = rl.GetShaderLocation(r.litShader, "lanternRange")
		r.locLayerColor = rl.GetShaderLocation(r.litShader, "layerColor")
		r.locBlendMode = rl.GetShaderLocation(r.litShader, "blendMode")
		r.locUvOffsetLit = rl.GetShaderLocation(r.litShader, "uvOffset")
		r.litLoaded = true
		r.LightingOn = true
		r.shaderLoaded = true
	}
}

// drawGroup draws one mesh group with its layer state.
func (r *Renderer) drawGroup(g *meshGroup, identity rl.Matrix, t float64, useLit bool) {
	mat := g.Material
	off := [2]float32{}
	if g.URate != 0 || g.VRate != 0 {
		off = uvOffset(g, t)
	}
	layer := g.Color // a copy, for cgo (see SetLightingUniforms)
	if useLit {
		mat.Shader = r.litShader
		amb := [3]float32{r.ambient[0] + g.Emissive[0], r.ambient[1] + g.Emissive[1], r.ambient[2] + g.Emissive[2]}
		rl.SetShaderValue(r.litShader, r.locAmbient, amb[:], rl.ShaderUniformVec3)
		rl.SetShaderValue(r.litShader, r.locLayerColor, layer[:], rl.ShaderUniformVec4)
		rl.SetShaderValue(r.litShader, r.locBlendMode, []float32{math.Float32frombits(uint32(g.Blend))}, rl.ShaderUniformInt)
		rl.SetShaderValue(r.litShader, r.locUvOffsetLit, off[:], rl.ShaderUniformVec2)
	} else {
		mat.Shader = r.unlitShader
		rl.SetShaderValue(r.unlitShader, r.locUnlitLayer, layer[:], rl.ShaderUniformVec4)
		rl.SetShaderValue(r.unlitShader, r.locUnlitUv, off[:], rl.ShaderUniformVec2)
	}
	rl.DrawMesh(g.Mesh, mat, identity)
}

// DrawZone renders meshes for a specific zone slot.
//
// Pass 1 draws the opaque base layers. Pass 2 draws every layer above the base
// at equal depth (GL_LEQUAL, no depth writes) with its own blend mode — the
// detail overlays are blend 4, which multiplies the ground by the overlay's
// alpha. Pass 3 draws base layers that blend (water), after the opaque world
// and without depth writes, which is what the PS2's TEST (AFAIL RGB_ONLY) gives
// any blended layer.
func (r *Renderer) DrawZone(proxyIdx int, f *frustum.Frustum, isPrimary bool, visibleRooms map[int]bool) (drawn, total int) {
	zs, ok := r.zoneSlots[proxyIdx]
	if !ok || len(zs.groups) == 0 {
		return 0, 0
	}

	total = len(zs.groups)
	identity := rl.MatrixIdentity()
	t := rl.GetTime()

	useLit := r.LightingOn && r.litLoaded
	hasPortalCull := len(visibleRooms) > 0
	visible := func(g *meshGroup) bool {
		// Portal room culling: skip groups in non-visible rooms
		if hasPortalCull && g.RoomID >= 0 && !visibleRooms[g.RoomID] {
			return false
		}
		return f == nil || f.TestAABB(g.MinX, g.MinY, g.MinZ, g.MaxX, g.MaxY, g.MaxZ)
	}

	// Pass 1: opaque base layers.
	rl.BeginBlendMode(rl.BlendAlpha)
	for i := range zs.groups {
		g := &zs.groups[i]
		if g.Overlay || g.Blend != blendOpaque || !visible(g) {
			continue
		}
		r.drawGroup(g, identity, t, useLit)
		drawn++
	}

	// Pass 2: overlay layers. GL_LEQUAL lets them draw at the base's depth
	// without z-fighting, and with depth writes OFF they leave no cracks at
	// strip boundaries (see "Overlay Depth Rendering" in CLAUDE.md).
	if !r.OverlaysOff {
		gfx.DepthFuncLEqual()
		rl.DisableDepthMask()
		for i := range zs.groups {
			g := &zs.groups[i]
			if !g.Overlay || !visible(g) {
				continue
			}
			setGroupBlend(g.Blend)
			r.drawGroup(g, identity, t, useLit)
			drawn++
		}
		rl.EnableDepthMask()
		gfx.DepthFuncLess()
	}

	// Pass 3: blended base layers.
	rl.DisableDepthMask()
	for i := range zs.groups {
		g := &zs.groups[i]
		if g.Overlay || g.Blend == blendOpaque || !visible(g) {
			continue
		}
		setGroupBlend(g.Blend)
		r.drawGroup(g, identity, t, useLit)
		drawn++
	}
	rl.EnableDepthMask()

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
// Ambient and sun are VIScene::InitTimeOfDay's m_a[0]/m_c[0] (viscene.cpp),
// byte-scale 0-255 converted to float 0.0-1.0.
// SetupTimeOfDayLights linearly interpolates between adjacent keyframes.

type todKeyframe struct {
	ambR, ambG, ambB float32 // ambient light (m_a[0])
	sunR, sunG, sunB float32 // directional sun/moon color (m_c[0], and the clear sky m_d[0])
}

// ps2TODTable holds the 24-hour PS2 lighting keyframes.
// Ambient: blue-tinted at night (elevated B channel).
// Sun: indigo→golden sunrise→warm white noon→golden sunset→indigo.
var ps2TODTable = [24]todKeyframe{
	// Hour 0: Midnight
	{75.0 / 255, 75.0 / 255, 100.0 / 255, 70.0 / 255, 0.0 / 255, 190.0 / 255},
	// Hour 1
	{75.0 / 255, 75.0 / 255, 100.0 / 255, 80.0 / 255, 0.0 / 255, 220.0 / 255},
	// Hour 2
	{75.0 / 255, 75.0 / 255, 100.0 / 255, 85.0 / 255, 0.0 / 255, 240.0 / 255},
	// Hour 3
	{75.0 / 255, 75.0 / 255, 100.0 / 255, 90.0 / 255, 0.0 / 255, 255.0 / 255},
	// Hour 4: Pre-dawn purple
	{75.0 / 255, 75.0 / 255, 100.0 / 255, 120.0 / 255, 55.0 / 255, 240.0 / 255},
	// Hour 5: Lavender dawn
	{75.0 / 255, 75.0 / 255, 100.0 / 255, 170.0 / 255, 55.0 / 255, 240.0 / 255},
	// Hour 6: Golden sunrise
	{85.0 / 255, 85.0 / 255, 85.0 / 255, 255.0 / 255, 175.0 / 255, 55.0 / 255},
	// Hour 7: Warm yellow-orange
	{85.0 / 255, 85.0 / 255, 85.0 / 255, 255.0 / 255, 210.0 / 255, 100.0 / 255},
	// Hour 8: Warm yellow morning
	{95.0 / 255, 95.0 / 255, 95.0 / 255, 255.0 / 255, 248.0 / 255, 160.0 / 255},
	// Hour 9: Warm white-yellow
	{100.0 / 255, 100.0 / 255, 100.0 / 255, 255.0 / 255, 250.0 / 255, 200.0 / 255},
	// Hour 10: Warm white
	{100.0 / 255, 100.0 / 255, 100.0 / 255, 255.0 / 255, 250.0 / 255, 210.0 / 255},
	// Hour 11: Near-white warm
	{102.0 / 255, 102.0 / 255, 102.0 / 255, 255.0 / 255, 250.0 / 255, 230.0 / 255},
	// Hour 12: Noon
	{102.0 / 255, 102.0 / 255, 102.0 / 255, 255.0 / 255, 250.0 / 255, 240.0 / 255},
	// Hour 13: Afternoon (mirrors 11)
	{100.0 / 255, 100.0 / 255, 100.0 / 255, 255.0 / 255, 250.0 / 255, 230.0 / 255},
	// Hour 14: (mirrors 10)
	{100.0 / 255, 100.0 / 255, 100.0 / 255, 255.0 / 255, 250.0 / 255, 210.0 / 255},
	// Hour 15: (mirrors 9)
	{100.0 / 255, 100.0 / 255, 100.0 / 255, 255.0 / 255, 250.0 / 255, 200.0 / 255},
	// Hour 16: Late afternoon (mirrors 8)
	{95.0 / 255, 95.0 / 255, 95.0 / 255, 255.0 / 255, 248.0 / 255, 160.0 / 255},
	// Hour 17: Warm orange (mirrors 7)
	{75.0 / 255, 75.0 / 255, 100.0 / 255, 255.0 / 255, 210.0 / 255, 100.0 / 255},
	// Hour 18: Warm orange sunset
	{75.0 / 255, 75.0 / 255, 100.0 / 255, 255.0 / 255, 175.0 / 255, 55.0 / 255},
	// Hour 19: Lavender dusk (mirrors 5)
	{75.0 / 255, 75.0 / 255, 100.0 / 255, 170.0 / 255, 55.0 / 255, 240.0 / 255},
	// Hour 20: Purple twilight (mirrors 4)
	{75.0 / 255, 75.0 / 255, 100.0 / 255, 120.0 / 255, 55.0 / 255, 240.0 / 255},
	// Hour 21: Deep purple
	{75.0 / 255, 75.0 / 255, 100.0 / 255, 90.0 / 255, 0.0 / 255, 255.0 / 255},
	// Hour 22: Dark blue-violet (mirrors 2)
	{75.0 / 255, 75.0 / 255, 100.0 / 255, 85.0 / 255, 0.0 / 255, 240.0 / 255},
	// Hour 23: Deep blue-violet (mirrors 1)
	{75.0 / 255, 75.0 / 255, 100.0 / 255, 80.0 / 255, 0.0 / 255, 220.0 / 255},
}

// ps2TimeOfDayColors interpolates the PS2 24-keyframe lighting table at the
// given fractional game hour (0.0-23.999) and returns the sun colour, the
// ambient, and the overcast sky's grey (m_d[1]): InitTimeOfDay sets that to
// max(0.2, 0.7*(1 - |h-12|/12)) for each whole hour, brightest at noon.
func ps2TimeOfDayColors(gameHour float32) (sun, ambient, grey [3]float32) {
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
	g := lerp(todGrey(idx), todGrey(next), t)
	grey = [3]float32{g, g, g}
	return
}

// todGrey is InitTimeOfDay's overcast-sky grey for a whole hour.
func todGrey(h int) float32 {
	f := 1 - float32(math.Abs(float64(h-12)/12))
	return max(f*0.7, 0.2)
}
