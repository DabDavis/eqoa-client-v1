package game

// Port of VIEffectVolume system from PS2 SUPPORT module.
// Provides localized environmental effects: ground fog, dust storms,
// falling leaves, and light shafts, placed in zones via EffectVolumeSprite.

import (
	"fmt"
	"image"
	"log"
	"math"
	"math/rand"
	"unsafe"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/eqoa/client/internal/mathutil"
	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

const (
	maxVolumes         = 512
	maxParticlesPerVol = 512
	volumeCullDist     = float32(100)
	volumeCullDistSq   = volumeCullDist * volumeCullDist
	fogTemplateCount   = 8 // PS2 GlobalInit creates 8 rotated billboard templates
)

// fogBillboardTemplate is a pre-rotated billboard quad (4 corners in XY plane).
// PS2 GlobalInit creates 8 of these at 45-degree increments via RotateZ.
type fogBillboardTemplate struct {
	corners [4][3]float32 // 4 vertices, each XYZ
}

// EffectVolumeSystem manages all active effect volumes for the current zone.
type EffectVolumeSystem struct {
	volumes      []*EffectVolume
	volumesReady bool
	debugTimer   float32 // periodic debug logging
	fogTemplates [fogTemplateCount]fogBillboardTemplate
}

// EffectVolume is a single active volume producing particles.
type EffectVolume struct {
	BBox       esf.Box
	Center     [3]float32
	Dims       [3]float32 // half-extents
	EffectType esf.EffectVolumeType
	Density    float32
	Speed      float32 // ESF speed/animation rate
	particles  []EffectParticle
	birthAccum float32
	tex        rl.Texture2D // GPU texture from ESF Surface (0 if none)
	hasTex     bool
	fogAlpha   uint8 // PS2 per-volume alpha: speed * densityMod * 255
	// Flock (type=5): Reynolds boids with random waypoint targeting.
	flockTarget [3]float32 // current waypoint target
	flockTimer  float32    // time until next waypoint (3-12s)
	birdMesh    rl.Mesh     // CSprite bird model mesh (uploaded to GPU)
	birdMat     rl.Material // simple unlit material for bird silhouette
	hasBirdMesh bool
	culled      bool // true when far from camera — skip update and render
}

// EffectParticle is a single particle within an effect volume.
type EffectParticle struct {
	X, Y, Z    float32
	VX, VY, VZ float32
	Size       float32
	SizeY      float32 // vertical scale (fog/light shafts use non-uniform scaling)
	Age        float32
	Lifespan   float32
	Phase      float32 // random offset for sinusoidal motion / billboard template
	State      int32   // PS2 state machine: 0=dead, 1=active/falling, 2=dying
	Wobble     float32 // PS2 FallingLeaves: oscillator [0, 30), rate dt*30
	Direction  int32   // PS2 FallingLeaves: 0 or 1 (random drift direction)
	FallY      float32 // PS2 FallingLeaves: bottom bound = -bboxSizeY*0.5
}

// NewEffectVolumeSystem creates a new empty system.
// Initializes 8 pre-rotated billboard templates matching PS2 GlobalInit.
func NewEffectVolumeSystem() *EffectVolumeSystem {
	s := &EffectVolumeSystem{}

	// PS2 GlobalInit: 8 rotated quads at i/8 * 2*PI via RotateZ.
	// Base corners: (-0.5,0.5,0), (0.5,0.5,0), (0.5,-0.5,0), (-0.5,-0.5,0)
	base := [4][3]float32{
		{-0.5, 0.5, 0},
		{0.5, 0.5, 0},
		{0.5, -0.5, 0},
		{-0.5, -0.5, 0},
	}
	for i := 0; i < fogTemplateCount; i++ {
		angle := float64(i) * (2.0 * math.Pi / float64(fogTemplateCount))
		cosA := float32(math.Cos(angle))
		sinA := float32(math.Sin(angle))
		for v := 0; v < 4; v++ {
			// RotateZ: x' = x*cos - y*sin, y' = x*sin + y*cos, z' = z
			x, y := base[v][0], base[v][1]
			s.fogTemplates[i].corners[v][0] = x*cosA - y*sinA
			s.fogTemplates[i].corners[v][1] = x*sinA + y*cosA
			s.fogTemplates[i].corners[v][2] = 0
		}
	}

	return s
}

// VolumeCount returns the number of loaded effect volumes.
func (s *EffectVolumeSystem) VolumeCount() int {
	return len(s.volumes)
}

// LoadZoneVolumes loads effect volumes from zone data. Clears any previous volumes.
func (s *EffectVolumeSystem) LoadZoneVolumes(placements []esf.EffectVolumePlacement) {
	s.Clear()
	count := len(placements)
	if count > maxVolumes {
		count = maxVolumes
	}
	for i := 0; i < count; i++ {
		p := &placements[i]

		vol := &EffectVolume{
			BBox:       p.BBox,
			EffectType: p.EffectType,
			Density:    p.Density,
			Speed:      p.Speed,
		}
		vol.Center[0] = (p.BBox.MinX + p.BBox.MaxX) * 0.5
		vol.Center[1] = (p.BBox.MinY + p.BBox.MaxY) * 0.5
		vol.Center[2] = (p.BBox.MinZ + p.BBox.MaxZ) * 0.5
		vol.Dims[0] = (p.BBox.MaxX - p.BBox.MinX) * 0.5
		vol.Dims[1] = (p.BBox.MaxY - p.BBox.MinY) * 0.5
		vol.Dims[2] = (p.BBox.MaxZ - p.BBox.MinZ) * 0.5

		// Default density if zero.
		if vol.Density <= 0 {
			vol.Density = 1.0
		}

		// PS2 fog alpha: speed * densityMod
		densityMod := (1.0-vol.Density)*0.5 + 0.5
		vol.fogAlpha = uint8(mathutil.ClampF(vol.Speed*densityMod*255, 1, 255))

		// Upload particle texture to GPU.
		if (vol.EffectType == esf.EffectTypeGroundFog || vol.EffectType == esf.EffectTypeDustStorm || vol.EffectType == esf.EffectTypeFallingLeaves) && p.Texture != nil {
			// Fog/Dust/Leaves: ESF texture with radial alpha mask + additive blending.
			// Premultiplied edges contribute nothing under additive blend.
			img := applyRadialAlphaMask(scalePS2AlphaEffectVol(p.Texture))
			rlImg := rl.NewImageFromImage(img)
			vol.tex = rl.LoadTextureFromImage(rlImg)
			rl.UnloadImage(rlImg)
			rl.SetTextureFilter(vol.tex, rl.FilterBilinear)
			rl.SetTextureWrap(vol.tex, rl.WrapClamp)
			vol.hasTex = vol.tex.ID > 0
		} else if p.Texture != nil {
			img := scalePS2AlphaEffectVol(p.Texture)
			rlImg := rl.NewImageFromImage(img)
			vol.tex = rl.LoadTextureFromImage(rlImg)
			rl.UnloadImage(rlImg)
			rl.SetTextureFilter(vol.tex, rl.FilterBilinear)
			rl.SetTextureWrap(vol.tex, rl.WrapClamp)
			vol.hasTex = vol.tex.ID > 0
		}

		// Build CSprite bird mesh for flock volumes.
		if vol.EffectType == esf.EffectTypeFlock && len(p.MeshVertices) > 0 {
			mesh, mat, ok := buildBirdMesh(p.MeshVertices)
			if ok {
				vol.birdMesh = mesh
				vol.birdMat = mat
				vol.hasBirdMesh = true
			}
		}

		// Pre-spawn particle types at load time (no delay).
		if vol.EffectType == esf.EffectTypeGroundFog {
			vol.spawnGroundFog()
		} else if vol.EffectType == esf.EffectTypeDustStorm {
			vol.spawnDustStorm()
		} else if vol.EffectType == esf.EffectTypeFallingLeaves {
			vol.spawnFallingLeaves()
		} else if vol.EffectType == esf.EffectTypeLightShafts {
			vol.spawnLightShafts()
		} else if vol.EffectType == esf.EffectTypeFlock {
			vol.spawnFlock()
		}

		texStr := ""
		if vol.hasTex {
			texStr = fmt.Sprintf(" tex=%dx%d", vol.tex.Width, vol.tex.Height)
		}
		log.Printf("effectvol: [%d] %s center=(%.0f,%.0f,%.0f) dims=(%.0f,%.0f,%.0f) density=%.2f speed=%.2f%s",
			i, vol.EffectType, vol.Center[0], vol.Center[1], vol.Center[2],
			vol.Dims[0], vol.Dims[1], vol.Dims[2], vol.Density, vol.Speed, texStr)
		s.volumes = append(s.volumes, vol)
	}
	log.Printf("effectvol: loaded %d volumes (capped from %d)", len(s.volumes), len(placements))
	s.volumesReady = true
}

// Clear removes all volumes and unloads GPU textures/meshes (zone change).
func (s *EffectVolumeSystem) Clear() {
	for _, vol := range s.volumes {
		if vol.hasTex {
			rl.UnloadTexture(vol.tex)
		}
		if vol.hasBirdMesh {
			rl.UnloadMesh(&vol.birdMesh)
			rl.UnloadMaterial(vol.birdMat)
		}
	}
	s.volumes = s.volumes[:0]
	s.volumesReady = false
}

// IsReady returns true if volumes have been loaded for the current zone.
func (s *EffectVolumeSystem) IsReady() bool {
	return s.volumesReady
}

// Update ticks all volumes: spawns/kills particles, updates positions.
// Distance culling skips updates for volumes far from camera.
func (s *EffectVolumeSystem) Update(dt, camX, camY, camZ float32) {
	totalParticles := 0
	activeVolumes := 0
	for _, vol := range s.volumes {
		// Distance cull — skip updates for far volumes
		dx := vol.Center[0] - camX
		dy := vol.Center[1] - camY
		dz := vol.Center[2] - camZ
		if dx*dx+dy*dy+dz*dz > volumeCullDistSq {
			vol.culled = true
			continue
		}
		vol.culled = false
		activeVolumes++

		switch vol.EffectType {
		case esf.EffectTypeGroundFog:
			vol.updateGroundFog(dt)
		case esf.EffectTypeDustStorm:
			vol.updateDustStorm(dt)
		case esf.EffectTypeFallingLeaves:
			vol.updateFallingLeaves(dt)
		case esf.EffectTypeLightShafts:
			vol.updateLightShafts(dt)
		case esf.EffectTypeFlock:
			vol.updateFlock(dt)
		default:
		}
		totalParticles += len(vol.particles)
	}

	// Periodic debug log every 1 second for diagnosing render issues.
	s.debugTimer += dt
	if s.debugTimer >= 1.0 {
		s.debugTimer = 0
		fogVols := 0
		fogParticles := 0
		for _, vol := range s.volumes {
			if vol.EffectType == esf.EffectTypeGroundFog {
				fogVols++
				fogParticles += len(vol.particles)
			}
		}
		log.Printf("effectvol: active=%d/%d total_particles=%d fog_vols=%d fog_particles=%d tex_bound=%v",
			activeVolumes, len(s.volumes), totalParticles, fogVols, fogParticles,
			func() bool {
				for _, vol := range s.volumes {
					if vol.EffectType == esf.EffectTypeGroundFog && vol.hasTex {
						return true
					}
				}
				return false
			}())
	}
}

// Render draws all visible effect volume particles.
// Fog uses pre-built rl.Mesh + DrawMesh (same pipeline as terrain/entities).
// Other types use immediate-mode Begin/End quads.
func (s *EffectVolumeSystem) Render(camera rl.Camera3D) {
	if len(s.volumes) == 0 {
		return
	}

	camRight, camUp := billboardVectors(camera)

	// Disable depth writes for transparent particles.
	rl.DisableDepthMask()
	defer rl.EnableDepthMask()

	for _, vol := range s.volumes {
		if vol.culled || len(vol.particles) == 0 {
			continue
		}

		// Select blend mode per effect type.
		// Fog, DustStorm, FallingLeaves, LightShafts use additive — edges are invisible
		// with additive blend because dark/transparent pixels add nothing.
		additive := vol.EffectType == esf.EffectTypeLightShafts || vol.EffectType == esf.EffectTypeGroundFog || vol.EffectType == esf.EffectTypeDustStorm || vol.EffectType == esf.EffectTypeFallingLeaves
		if additive {
			rl.BeginBlendMode(rl.BlendAdditive)
		} else {
			rl.BeginBlendMode(rl.BlendAlpha)
		}

		if vol.EffectType == esf.EffectTypeGroundFog || vol.EffectType == esf.EffectTypeDustStorm || vol.EffectType == esf.EffectTypeFallingLeaves {
			s.renderGroundFogBillboard(vol, camera)
		} else if vol.EffectType == esf.EffectTypeFlock {
			s.renderFlock(vol, camRight, camUp)
		} else {
			if vol.hasTex {
				rl.SetTexture(vol.tex.ID)
			}
			s.renderStandard(vol, camRight, camUp)
			if vol.hasTex {
				rl.SetTexture(0)
			}
		}
		rl.EndBlendMode()
	}
}

// renderGroundFogBillboard renders fog using DrawBillboardPro — same immediate-mode
// pipeline as spell particles. Additive blend hides edges (dark pixels add nothing).
func (s *EffectVolumeSystem) renderGroundFogBillboard(vol *EffectVolume, camera rl.Camera3D) {
	if len(vol.particles) == 0 || !vol.hasTex {
		return
	}

	texW := float32(vol.tex.Width)
	texH := float32(vol.tex.Height)
	source := rl.Rectangle{X: 0, Y: 0, Width: texW, Height: texH}

	// PS2 alpha factors for fog/dust
	densityMod := float32((1.0-float64(vol.Density))*0.5 + 0.5)
	baseAlpha := vol.Speed * densityMod
	if vol.EffectType == esf.EffectTypeDustStorm {
		baseAlpha *= 0.5
	}

	for i := range vol.particles {
		p := &vol.particles[i]

		var a uint8
		var sizeXZ, sizeY float32

		if vol.EffectType == esf.EffectTypeFallingLeaves {
			// PS2 FallingLeaves: alpha = lifetime (starts 1.0, shrinks in dying state)
			// Only render falling (state=1) and dying (state=2) particles
			if p.State == 0 {
				continue
			}
			leafAlpha := p.Lifespan // PS2 lifetime field, 0.0-1.0
			if leafAlpha > 1.0 {
				leafAlpha = 1.0
			}
			if leafAlpha <= 0 {
				continue
			}
			a = uint8(mathutil.ClampF(leafAlpha*vol.Speed*255, 0, 255))
			// PS2 leaf size: scale value directly (already [0.5, 1.3])
			sizeXZ = p.Size
			sizeY = p.Size
		} else {
			// Fog/Dust: PS2 sin envelope
			progress := p.Age / p.Lifespan
			sinAlpha := float32(math.Sin(float64(progress) * math.Pi))
			a = uint8(mathutil.ClampF(sinAlpha*baseAlpha*255, 0, 255))
			sizeXZ = p.Size
			sizeY = p.SizeY
			if sizeY <= 0 {
				sizeY = sizeXZ
			}
		}

		if a == 0 {
			continue
		}

		pos := rl.Vector3{X: p.X, Y: p.Y, Z: p.Z}
		size := rl.Vector2{X: sizeXZ * 2, Y: sizeY * 2}
		origin := rl.Vector2{X: sizeXZ, Y: sizeY}
		// PS2 rotation template (0-7) → degrees
		rotation := float32(p.Phase) * (360.0 / float32(fogTemplateCount))
		tint := rl.Color{R: 255, G: 255, B: 255, A: a}

		rl.DrawBillboardPro(camera, vol.tex, source, pos, camera.Up, size, origin, rotation, tint)
	}
}

// renderFlock renders flock members. If CSprite mesh is available, renders
// the 3D bird model per bird with yaw rotation from flight direction.
// Falls back to dark quad silhouettes if no mesh was extracted.
func (s *EffectVolumeSystem) renderFlock(vol *EffectVolume, camRight, camUp rl.Vector3) {
	if vol.hasBirdMesh {
		s.renderFlockMesh(vol)
		return
	}
	// Fallback: flat quad silhouettes
	for i := range vol.particles {
		p := &vol.particles[i]
		cx, cy, cz := p.X, p.Y, p.Z
		size := p.Size

		rx := camRight.X * size * 1.5
		ry := camRight.Y * size * 1.5
		rz := camRight.Z * size * 1.5
		ux := camUp.X * size * 0.4
		uy := camUp.Y * size * 0.4
		uz := camUp.Z * size * 0.4

		clr := rl.Color{R: 30, G: 25, B: 20, A: 220}

		rl.Begin(rl.Quads)
		rl.Color4ub(clr.R, clr.G, clr.B, clr.A)
		rl.Vertex3f(cx-rx-ux, cy-ry-uy, cz-rz-uz)
		rl.Vertex3f(cx+rx-ux, cy+ry-uy, cz+rz-uz)
		rl.Vertex3f(cx+rx+ux, cy+ry+uy, cz+rz+uz)
		rl.Vertex3f(cx-rx+ux, cy-ry+uy, cz-rz+uz)
		rl.End()
	}
}

// renderFlockMesh renders each flock bird using the CSprite 3D mesh.
// Each bird gets a transform: translate to position + rotate to yaw (flight direction).
func (s *EffectVolumeSystem) renderFlockMesh(vol *EffectVolume) {
	for i := range vol.particles {
		p := &vol.particles[i]

		// Yaw rotation from Phase (atan2 of velocity)
		cosF := float32(math.Cos(float64(p.Phase)))
		sinF := float32(math.Sin(float64(p.Phase)))
		sc := p.Size / 0.4 // normalize: bird mesh is small, scale up

		// Transform: scale + rotate Y (yaw) + translate
		// C-convention matrix (same as render.go DrawEntity)
		transform := rl.Matrix{
			M0: cosF * sc, M4: 0, M8: sinF * sc, M12: p.X,
			M1: 0, M5: sc, M9: 0, M13: p.Y,
			M2: -sinF * sc, M6: 0, M10: cosF * sc, M14: p.Z,
			M3: 0, M7: 0, M11: 0, M15: 1,
		}

		rl.DrawMesh(vol.birdMesh, vol.birdMat, transform)
	}
}

// renderStandard renders non-fog particles as standard camera-facing billboards.
func (s *EffectVolumeSystem) renderStandard(vol *EffectVolume, camRight, camUp rl.Vector3) {
	for i := range vol.particles {
		p := &vol.particles[i]
		clr := vol.particleColor(p)
		if clr.A == 0 {
			continue
		}

		cx, cy, cz := p.X, p.Y, p.Z
		sizeH := p.Size
		sizeV := p.SizeY
		if sizeV <= 0 {
			sizeV = sizeH
		}

		rx := camRight.X * sizeH
		ry := camRight.Y * sizeH
		rz := camRight.Z * sizeH
		ux := camUp.X * sizeV
		uy := camUp.Y * sizeV
		uz := camUp.Z * sizeV

		rl.Begin(rl.Quads)
		rl.Color4ub(clr.R, clr.G, clr.B, clr.A)
		if vol.hasTex {
			rl.TexCoord2f(0, 1)
			rl.Vertex3f(cx-rx-ux, cy-ry-uy, cz-rz-uz)
			rl.TexCoord2f(1, 1)
			rl.Vertex3f(cx+rx-ux, cy+ry-uy, cz+rz-uz)
			rl.TexCoord2f(1, 0)
			rl.Vertex3f(cx+rx+ux, cy+ry+uy, cz+rz+uz)
			rl.TexCoord2f(0, 0)
			rl.Vertex3f(cx-rx+ux, cy-ry+uy, cz-rz+uz)
		} else {
			rl.Vertex3f(cx-rx-ux, cy-ry-uy, cz-rz-uz)
			rl.Vertex3f(cx+rx-ux, cy+ry-uy, cz+rz-uz)
			rl.Vertex3f(cx+rx+ux, cy+ry+uy, cz+rz+uz)
			rl.Vertex3f(cx-rx+ux, cy-ry+uy, cz-rz+uz)
		}
		rl.End()
	}
}

// --- Ground Fog ---
// PS2 VIEffectVolume::ProcessGroundFog: particles are 100% STATIC.
// They spawn at random XZ within the volume, Y=ground. No velocity.
// Alpha follows sin(age/lifespan * PI) — smooth fade in/out.
// Lifespan: 10-40 seconds. Size: XZ=(rand+2)*2, Y=bboxH*(0.85-1.0).
// Particle count = bboxSizeX * 20 * density.

func (vol *EffectVolume) spawnGroundFog() {
	bboxSizeX := vol.BBox.MaxX - vol.BBox.MinX
	bboxSizeY := vol.BBox.MaxY - vol.BBox.MinY

	// PS2: count = bboxSize.X * 20 * density
	count := int(bboxSizeX * 20.0 * vol.Density)
	if count < 1 {
		count = 1
	}
	if count > maxParticlesPerVol {
		count = maxParticlesPerVol
	}

	vol.particles = make([]EffectParticle, count)
	for i := range vol.particles {
		// PS2 sizeXZ: (rand + 2.0) * 2.0 => range [4.0, 6.0], then 1.5x render multiplier
		sizeXZ := (rand.Float32() + 2.0) * 2.0 * 1.5
		// PS2 sizeY: bboxSizeY * (rand*0.15 + 0.85), with 1.5x render multiplier
		sizeY := bboxSizeY * (rand.Float32()*0.15 + 0.85) * 1.5
		// PS2 lifespan: rand*30 + 10 => [10, 40] seconds
		lifespan := rand.Float32()*30.0 + 10.0
		// Randomize initial age so particles don't all start at zero
		age := rand.Float32() * lifespan
		// PS2: texFrame = rand & 7 — selects one of 8 pre-rotated billboard templates
		phase := float32(rand.Intn(fogTemplateCount))

		// PS2 triangular distribution: (rand1 - rand2) * halfExtent (center-biased)
		vol.particles[i] = EffectParticle{
			X:        vol.Center[0] + (rand.Float32()-rand.Float32())*(bboxSizeX*0.5),
			Y:        vol.BBox.MinY, // PS2: Y=0 (ground level = bottom of volume)
			Z:        vol.Center[2] + (rand.Float32()-rand.Float32())*((vol.BBox.MaxZ-vol.BBox.MinZ)*0.5),
			Size:     sizeXZ,
			SizeY:    sizeY,
			Age:      age,
			Lifespan: lifespan,
			Phase:    phase, // template index (0-7)
		}
	}
}

func (vol *EffectVolume) updateGroundFog(dt float32) {
	if len(vol.particles) == 0 {
		vol.spawnGroundFog()
		return
	}
	bboxSizeX := vol.BBox.MaxX - vol.BBox.MinX
	bboxSizeY := vol.BBox.MaxY - vol.BBox.MinY
	// Age particles, respawn when expired (PS2 behavior).
	for i := range vol.particles {
		p := &vol.particles[i]
		p.Age += dt
		if p.Age >= p.Lifespan {
			// Respawn at new random position with new size/lifespan/orientation
			// PS2 triangular distribution: (rand1 - rand2) * halfExtent
			p.X = vol.Center[0] + (rand.Float32()-rand.Float32())*(bboxSizeX*0.5)
			p.Z = vol.Center[2] + (rand.Float32()-rand.Float32())*((vol.BBox.MaxZ-vol.BBox.MinZ)*0.5)
			p.Size = (rand.Float32() + 2.0) * 2.0 * 1.5
			p.SizeY = bboxSizeY * (rand.Float32()*0.15 + 0.85) * 1.5
			p.Lifespan = rand.Float32()*30.0 + 10.0
			p.Phase = float32(rand.Intn(fogTemplateCount))
			p.Age = 0
		}
	}
}

// --- Dust Storm ---
// PS2 VIEffectVolume::ProcessDustStorm (0x0042C568):
// Count = bboxSizeX * 20 * density (same as GroundFog)
// VX/VZ = (rand/255+2)*2*(speed*0.5+0.5) → [4,6]*speedMod
// VY = -bboxSizeX*(0.5 + 0.25*rand) — large negative (downward start, rendered upward)
// Scale = rand*0.5 + 0.5 → [0.5, 1.0]
// Lifespan = (2-speed)*(rand*0.7+1.0)
// Spawn: XZ centered at volume center (triangular dist), Y from velY
// PS2 Render: pos = savedPos + age*vel (analytical, no per-frame integration)
// No wrapping — particles die at lifespan and respawn.
// 60-frame texture animation cycle.

func (vol *EffectVolume) spawnDustStorm() {
	bboxSizeX := vol.BBox.MaxX - vol.BBox.MinX
	bboxSizeY := vol.BBox.MaxY - vol.BBox.MinY
	bboxSizeZ := vol.BBox.MaxZ - vol.BBox.MinZ

	// PS2: count = bboxSizeX * 20.0 * density
	count := int(bboxSizeX * 20.0 * vol.Density)
	if count < 1 {
		count = 1
	}
	if count > maxParticlesPerVol {
		count = maxParticlesPerVol
	}

	speedMod := vol.Speed*0.5 + 0.5

	vol.particles = make([]EffectParticle, count)
	for i := range vol.particles {
		// PS2 VX/VZ: (rand+2)*2*speedMod → [4,6]*speedMod
		vx := (rand.Float32() + 2.0) * 2.0 * speedMod
		vz := (rand.Float32() + 2.0) * 2.0 * speedMod
		// PS2 VY: gentle upward drift
		vy := rand.Float32() * 0.3

		// PS2 lifespan: (2-speed)*(rand*0.7+1.0)
		lifespan := (2.0 - vol.Speed) * (rand.Float32()*0.7 + 1.0)
		if lifespan < 0.5 {
			lifespan = 0.5
		}
		// PS2: first-frame randomizes initial ages
		age := rand.Float32() * lifespan

		// PS2 scale: rand*0.5+0.5 → [0.5, 1.0]
		scale := rand.Float32()*0.5 + 0.5

		// PS2 triangular distribution: (rand1 - rand2) * halfExtent
		spawnX := (rand.Float32() - rand.Float32()) * (bboxSizeX * 0.5)
		spawnY := (rand.Float32() - 0.5) * bboxSizeY * 0.5
		spawnZ := (rand.Float32() - rand.Float32()) * (bboxSizeZ * 0.5)

		// Store world positions (analytical: center + saved + age*vel)
		vol.particles[i] = EffectParticle{
			X:        vol.Center[0] + spawnX + age*vx,
			Y:        vol.Center[1] + spawnY + age*vy,
			Z:        vol.Center[2] + spawnZ + age*vz,
			VX:       vx,
			VY:       vy,
			VZ:       vz,
			Size:     scale,
			Age:      age,
			Lifespan: lifespan,
			Phase:    float32(rand.Intn(fogTemplateCount)),
		}
	}
}

func (vol *EffectVolume) updateDustStorm(dt float32) {
	if len(vol.particles) == 0 {
		vol.spawnDustStorm()
		return
	}
	bboxSizeX := vol.BBox.MaxX - vol.BBox.MinX
	bboxSizeY := vol.BBox.MaxY - vol.BBox.MinY
	bboxSizeZ := vol.BBox.MaxZ - vol.BBox.MinZ
	speedMod := vol.Speed*0.5 + 0.5

	for i := range vol.particles {
		p := &vol.particles[i]
		p.Age += dt
		if p.Age >= p.Lifespan {
			// PS2: dead→respawn with new random params
			// PS2 ProcessDustStorm (0x0042C568): VX = (rand+2)*2*speedMod
			p.VX = (rand.Float32() + 2.0) * 2.0 * speedMod
			p.VZ = (rand.Float32() + 2.0) * 2.0 * speedMod
			p.VY = rand.Float32() * 0.3
			// PS2 triangular distribution: (rand1 - rand2) * halfExtent
			p.X = vol.Center[0] + (rand.Float32()-rand.Float32())*(bboxSizeX*0.5)
			p.Y = vol.Center[1] + (rand.Float32()-0.5)*bboxSizeY*0.5
			p.Z = vol.Center[2] + (rand.Float32()-rand.Float32())*(bboxSizeZ*0.5)
			p.Lifespan = (2.0 - vol.Speed) * (rand.Float32()*0.7 + 1.0)
			if p.Lifespan < 0.5 {
				p.Lifespan = 0.5
			}
			p.Size = rand.Float32()*0.5 + 0.5
			p.Phase = float32(rand.Intn(fogTemplateCount))
			p.Age = 0
			continue
		}
		// PS2 analytical: pos = savedPos + age*vel
		// We approximate by integrating per frame (same visual result).
		p.X += p.VX * dt
		p.Y += p.VY * dt
		p.Z += p.VZ * dt
	}
}

// --- Falling Leaves ---
// PS2 VIEffectVolume::ProcessFallingLeaves (0x0042ACB8):
// Pre-spawned particles with 3-state machine: 0=dead, 1=falling, 2=dying.
// Count = bboxSizeX * bboxSizeY * 0.4 * density.
// Scale = rand*0.8 + 0.5 → [0.5, 1.3].
// Fall speed = scale * dt (proportional to scale).
// Wobble oscillator: 0→30 at dt*30 rate, wraps to 0.
// Dying: lifetime -= dt*0.6 when wobble < 1.0; dead when lifetime < 0.
// Spawn Y: top of volume (bboxSizeY * 0.5) on normal spawn, random on first frame.

func (vol *EffectVolume) spawnFallingLeaves() {
	bboxSizeX := vol.BBox.MaxX - vol.BBox.MinX
	bboxSizeY := vol.BBox.MaxY - vol.BBox.MinY
	bboxSizeZ := vol.BBox.MaxZ - vol.BBox.MinZ

	// PS2 AllocParticles: count = bboxSizeX * bboxSizeY * 0.4 * density
	count := int(bboxSizeX * bboxSizeY * 0.4 * vol.Density)
	if count < 1 {
		count = 1
	}
	if count > maxParticlesPerVol {
		count = maxParticlesPerVol
	}

	halfY := bboxSizeY * 0.5

	vol.particles = make([]EffectParticle, count)
	for i := range vol.particles {
		// PS2 first-frame (initialized=0): randomize all particles at random Y heights
		// (not all at top) so the volume looks full immediately.
		scale := rand.Float32()*0.8 + 0.5 // PS2: [0.5, 1.3]
		wobble := float32(rand.Intn(30))   // PS2: rand % 30
		dir := int32(rand.Intn(2))         // PS2: rand & 1

		// PS2 triangular distribution: (rand1 - rand2) * halfExtent
		posX := (rand.Float32() - rand.Float32()) * (bboxSizeX * 0.5)
		posZ := (rand.Float32() - rand.Float32()) * (bboxSizeZ * 0.5)
		// First-frame: random Y within full volume (triangle distribution, center-biased)
		posY := (rand.Float32() - rand.Float32()) * (bboxSizeY * 0.5)

		vol.particles[i] = EffectParticle{
			X:         vol.Center[0] + posX,
			Y:         vol.Center[1] + posY,
			Z:         vol.Center[2] + posZ,
			Size:      scale,
			Lifespan:  1.0, // PS2: lifetime starts at 1.0
			State:     1,   // Start falling (first-frame particles are already active)
			Wobble:    wobble,
			Direction: dir,
			FallY:     -halfY, // bottom bound relative to center
			Phase:     float32(rand.Intn(fogTemplateCount)), // billboard template
		}
	}
}

func (vol *EffectVolume) updateFallingLeaves(dt float32) {
	if len(vol.particles) == 0 {
		vol.spawnFallingLeaves()
		return
	}

	bboxSizeX := vol.BBox.MaxX - vol.BBox.MinX
	bboxSizeY := vol.BBox.MaxY - vol.BBox.MinY
	bboxSizeZ := vol.BBox.MaxZ - vol.BBox.MinZ
	halfY := bboxSizeY * 0.5

	for i := range vol.particles {
		p := &vol.particles[i]

		switch p.State {
		case 0:
			// Dead → spawn at top of volume
			p.Size = rand.Float32()*0.8 + 0.5 // PS2: [0.5, 1.3]
			p.Wobble = float32(rand.Intn(30))
			p.Direction = int32(rand.Intn(2))
			// PS2 triangular distribution for XZ
			posX := (rand.Float32() - rand.Float32()) * (bboxSizeX * 0.5)
			posZ := (rand.Float32() - rand.Float32()) * (bboxSizeZ * 0.5)
			p.X = vol.Center[0] + posX
			p.Y = vol.Center[1] + halfY // spawn at top
			p.Z = vol.Center[2] + posZ
			p.FallY = -halfY
			p.Lifespan = 1.0
			p.Phase = float32(rand.Intn(fogTemplateCount))
			p.State = 1

		case 1:
			// Falling: wobble oscillator + fall downward
			p.Wobble += dt * 30.0
			if p.Wobble >= 30.0 {
				p.Wobble = 0 // PS2: reset to 0, not wrap
			}
			// PS2: posY -= scale * dt (fall speed proportional to scale)
			p.Y -= p.Size * dt
			// Hit bottom → transition to dying
			if p.Y-vol.Center[1] < p.FallY {
				p.State = 2
			}

		case 2:
			// Dying: PS2 two-phase death (mutually exclusive):
			//   wobble >= 1.0 → finish current tumble cycle (advance wobble)
			//   wobble < 1.0  → shrink lifeLeft (fade out)
			if p.Wobble >= 1.0 {
				p.Wobble += dt * 30.0
				if p.Wobble >= 30.0 {
					p.Wobble = 0
				}
			} else {
				p.Lifespan -= dt * 0.6
			}
			// Dead when lifetime < 0
			if p.Lifespan < 0 {
				p.State = 0
			}
		}
	}
}

// --- Light Shafts ---
// PS2: Static particles. Count = bboxSizeX * 0.5 * density.
// sizeX: (rand*0.4 + 1.5) * 0.5 => [0.75, 0.95]
// sizeY: bboxSizeY * (rand*0.2 + 0.8) => [0.8*H, 1.0*H]
// lifespan: (1-speed)*60 + 4 + rand*((1-speed)*10 + 2) seconds
// Alpha: sin(age/lifespan * PI)

func (vol *EffectVolume) spawnLightShafts() {
	bboxSizeX := vol.BBox.MaxX - vol.BBox.MinX
	bboxSizeY := vol.BBox.MaxY - vol.BBox.MinY

	// PS2: count = bboxSizeX * 0.5 * density
	count := int(bboxSizeX * 0.5 * vol.Density)
	if count < 1 {
		count = 1
	}
	if count > maxParticlesPerVol {
		count = maxParticlesPerVol
	}

	vol.particles = make([]EffectParticle, count)
	for i := range vol.particles {
		// PS2 sizeX: (rand*0.4 + 1.5) * 0.5
		sizeX := (rand.Float32()*0.4 + 1.5) * 0.5
		// PS2 sizeY: bboxSizeY * (rand*0.2 + 0.8)
		sizeY := bboxSizeY * (rand.Float32()*0.2 + 0.8)
		// PS2 lifespan: base=(1-speed)*60+4, variance=(1-speed)*10+2
		base := (1.0-vol.Speed)*60.0 + 4.0
		variance := (1.0-vol.Speed)*10.0 + 2.0
		lifespan := base + rand.Float32()*variance
		// Randomize initial age
		age := lifespan * rand.Float32()

		// PS2 triangular distribution: (rand1 - rand2) * halfExtent
		vol.particles[i] = EffectParticle{
			X:        vol.Center[0] + (rand.Float32()-rand.Float32())*((vol.BBox.MaxX-vol.BBox.MinX)*0.5),
			Y:        vol.BBox.MaxY, // PS2: spawn at top of bbox
			Z:        vol.Center[2] + (rand.Float32()-rand.Float32())*((vol.BBox.MaxZ-vol.BBox.MinZ)*0.5),
			Size:     sizeX,
			SizeY:    sizeY,
			Age:      age,
			Lifespan: lifespan,
		}
	}
}

func (vol *EffectVolume) updateLightShafts(dt float32) {
	// PS2: static particles, just advance age and respawn when expired.
	for i := range vol.particles {
		p := &vol.particles[i]
		p.Age += dt
		if p.Age >= p.Lifespan {
			// Respawn at new random position
			// PS2 triangular distribution: (rand1 - rand2) * halfExtent
			p.X = vol.Center[0] + (rand.Float32()-rand.Float32())*((vol.BBox.MaxX-vol.BBox.MinX)*0.5)
			p.Z = vol.Center[2] + (rand.Float32()-rand.Float32())*((vol.BBox.MaxZ-vol.BBox.MinZ)*0.5)
			p.Size = (rand.Float32()*0.4 + 1.5) * 0.5
			bboxSizeY := vol.BBox.MaxY - vol.BBox.MinY
			p.SizeY = bboxSizeY * (rand.Float32()*0.2 + 0.8)
			base := (1.0-vol.Speed)*60.0 + 4.0
			variance := (1.0-vol.Speed)*10.0 + 2.0
			p.Lifespan = base + rand.Float32()*variance
			p.Age = 0
		}
	}
}

// --- Flock (type=5) ---
// PS2: Reynolds boids simulation — separation, alignment, cohesion.
// Random waypoint every 3-12 seconds. Uses CSprite (animated sprite),
// but we render as small billboards until CSprite rendering is available.

func (vol *EffectVolume) spawnFlock() {
	count := 3
	if count > maxParticlesPerVol {
		count = maxParticlesPerVol
	}

	// Initial waypoint at center of volume
	vol.flockTarget = vol.Center
	vol.flockTimer = 3.0 + rand.Float32()*9.0 // 3-12s

	vol.particles = make([]EffectParticle, count)
	for i := range vol.particles {
		// Stagger Y positions so birds fly at slightly different altitudes
		yOffset := float32(i-count/2) * 1.0 // ~1.0 units apart vertically
		vol.particles[i] = EffectParticle{
			X:    vol.Center[0] + (rand.Float32()-0.5)*vol.Dims[0],
			Y:    vol.Center[1] + yOffset,
			Z:    vol.Center[2] + (rand.Float32()-0.5)*vol.Dims[2],
			VX:   (rand.Float32() - 0.5) * 2.0 * vol.Speed,
			VY:   (rand.Float32() - 0.5) * 0.5 * vol.Speed,
			VZ:   (rand.Float32() - 0.5) * 2.0 * vol.Speed,
			Size: 0.3 + rand.Float32()*0.2, // small bird size
		}
	}
}

func (vol *EffectVolume) updateFlock(dt float32) {
	if len(vol.particles) == 0 {
		vol.spawnFlock()
		return
	}

	// Update waypoint timer — pick new random target within volume
	vol.flockTimer -= dt
	if vol.flockTimer <= 0 {
		vol.flockTarget[0] = vol.Center[0] + (rand.Float32()-0.5)*vol.Dims[0]*1.5
		vol.flockTarget[1] = vol.Center[1] + (rand.Float32()-0.5)*vol.Dims[1]*0.5
		vol.flockTarget[2] = vol.Center[2] + (rand.Float32()-0.5)*vol.Dims[2]*1.5
		vol.flockTimer = 3.0 + rand.Float32()*9.0 // PS2: 3-12 seconds
	}

	n := len(vol.particles)
	maxSpeed := vol.Speed * 5.0
	if maxSpeed < 2.0 {
		maxSpeed = 2.0
	}

	for i := range vol.particles {
		p := &vol.particles[i]

		// --- Boids forces ---
		var sepX, sepY, sepZ float32     // separation
		var alignVX, alignVY, alignVZ float32 // alignment
		var cohX, cohY, cohZ float32     // cohesion center
		neighbors := 0

		for j := 0; j < n; j++ {
			if j == i {
				continue
			}
			o := &vol.particles[j]
			dx := p.X - o.X
			dy := p.Y - o.Y
			dz := p.Z - o.Z
			distSq := dx*dx + dy*dy + dz*dz

			if distSq < 9.0 { // within 3 units
				neighbors++
				// Separation: push away from nearby birds
				if distSq < 1.0 { // too close (< 1 unit)
					if distSq < 0.01 {
						distSq = 0.01
					}
					invD := 1.0 / distSq
					sepX += dx * invD
					sepY += dy * invD
					sepZ += dz * invD
				}
				// Alignment: match velocity of nearby birds
				alignVX += o.VX
				alignVY += o.VY
				alignVZ += o.VZ
				// Cohesion: average position of nearby birds
				cohX += o.X
				cohY += o.Y
				cohZ += o.Z
			}
		}

		var ax, ay, az float32

		// Separation force
		ax += sepX * 2.0
		ay += sepY * 2.0
		az += sepZ * 2.0

		if neighbors > 0 {
			invN := 1.0 / float32(neighbors)
			// Alignment: steer toward average velocity
			ax += (alignVX*invN - p.VX) * 0.5
			ay += (alignVY*invN - p.VY) * 0.5
			az += (alignVZ*invN - p.VZ) * 0.5
			// Cohesion: steer toward average position
			ax += (cohX*invN - p.X) * 0.3
			ay += (cohY*invN - p.Y) * 0.3
			az += (cohZ*invN - p.Z) * 0.3
		}

		// Waypoint attraction
		ax += (vol.flockTarget[0] - p.X) * 0.2
		ay += (vol.flockTarget[1] - p.Y) * 0.2
		az += (vol.flockTarget[2] - p.Z) * 0.2

		// Boundary steering: push back toward volume center
		bboxPad := float32(2.0)
		if p.X < vol.BBox.MinX-bboxPad {
			ax += 3.0
		} else if p.X > vol.BBox.MaxX+bboxPad {
			ax -= 3.0
		}
		if p.Y < vol.BBox.MinY-bboxPad {
			ay += 3.0
		} else if p.Y > vol.BBox.MaxY+bboxPad {
			ay -= 3.0
		}
		if p.Z < vol.BBox.MinZ-bboxPad {
			az += 3.0
		} else if p.Z > vol.BBox.MaxZ+bboxPad {
			az -= 3.0
		}

		// Apply acceleration
		p.VX += ax * dt
		p.VY += ay * dt
		p.VZ += az * dt

		// Clamp speed
		speed := float32(math.Sqrt(float64(p.VX*p.VX + p.VY*p.VY + p.VZ*p.VZ)))
		if speed > maxSpeed {
			scale := maxSpeed / speed
			p.VX *= scale
			p.VY *= scale
			p.VZ *= scale
		}

		// Update position
		p.X += p.VX * dt
		p.Y += p.VY * dt
		p.Z += p.VZ * dt

		// Phase tracks heading for rendering (yaw angle)
		p.Phase = float32(math.Atan2(float64(p.VX), float64(p.VZ)))
	}
}

// buildBirdMesh converts ESF triangle strips (VertexList) into an rl.Mesh.
// The mesh is centered at origin — each bird instance is rendered with a
// per-bird transform matrix (position + yaw rotation from flight direction).
func buildBirdMesh(strips []esf.VertexList) (rl.Mesh, rl.Material, bool) {
	// Destrip all triangle strips into individual triangles.
	type tri struct {
		v0, v1, v2 esf.Vertex
	}
	var tris []tri
	for _, strip := range strips {
		numTris := len(strip.Vertices) - 2
		if numTris <= 0 {
			continue
		}
		odd := true
		for t := 0; t < numTris; t++ {
			var i0, i1, i2 int
			if odd {
				i0, i1, i2 = t, t+2, t+1
			} else {
				i0, i1, i2 = t, t+1, t+2
			}
			odd = !odd

			v0 := strip.Vertices[i0]
			v1 := strip.Vertices[i1]
			v2 := strip.Vertices[i2]

			// Skip degenerate triangles.
			if v0.X == v1.X && v0.Y == v1.Y && v0.Z == v1.Z {
				continue
			}
			if v0.X == v2.X && v0.Y == v2.Y && v0.Z == v2.Z {
				continue
			}
			if v1.X == v2.X && v1.Y == v2.Y && v1.Z == v2.Z {
				continue
			}
			tris = append(tris, tri{v0, v1, v2})
		}
	}

	if len(tris) == 0 {
		return rl.Mesh{}, rl.Material{}, false
	}

	vertCount := int32(len(tris) * 3)
	triCount := int32(len(tris))

	mesh := rl.Mesh{
		VertexCount:   vertCount,
		TriangleCount: triCount,
	}

	// Allocate vertex arrays.
	verts := make([]float32, vertCount*3)
	normals := make([]float32, vertCount*3)
	colors := make([]uint8, vertCount*4)

	for i, t := range tris {
		base := i * 9
		verts[base+0] = t.v0.X
		verts[base+1] = t.v0.Y
		verts[base+2] = t.v0.Z
		verts[base+3] = t.v1.X
		verts[base+4] = t.v1.Y
		verts[base+5] = t.v1.Z
		verts[base+6] = t.v2.X
		verts[base+7] = t.v2.Y
		verts[base+8] = t.v2.Z

		normals[base+0] = t.v0.NX
		normals[base+1] = t.v0.NY
		normals[base+2] = t.v0.NZ
		normals[base+3] = t.v1.NX
		normals[base+4] = t.v1.NY
		normals[base+5] = t.v1.NZ
		normals[base+6] = t.v2.NX
		normals[base+7] = t.v2.NY
		normals[base+8] = t.v2.NZ

		cbase := i * 12
		for j := 0; j < 3; j++ {
			// Dark bird silhouette color
			colors[cbase+j*4+0] = 30
			colors[cbase+j*4+1] = 25
			colors[cbase+j*4+2] = 20
			colors[cbase+j*4+3] = 220
		}
	}

	mesh.Vertices = (*float32)(unsafe.Pointer(&verts[0]))
	mesh.Normals = (*float32)(unsafe.Pointer(&normals[0]))
	mesh.Colors = (*uint8)(unsafe.Pointer(&colors[0]))

	rl.UploadMesh(&mesh, false)

	// Simple unlit material.
	mat := rl.LoadMaterialDefault()

	log.Printf("effectvol: built bird mesh: %d triangles, %d vertices from %d strips",
		triCount, vertCount, len(strips))
	return mesh, mat, true
}

// --- Color per effect type ---

// particleColor returns the color for non-fog particle types.
// Fog color/alpha is computed inline in renderGroundFog.
func (vol *EffectVolume) particleColor(p *EffectParticle) rl.Color {
	progress := p.Age / p.Lifespan
	if progress > 1 {
		progress = 1
	}

	switch vol.EffectType {
	case esf.EffectTypeLightShafts:
		// PS2: alpha = sin(progress * PI) * speed (stationary, fade cycle)
		sinAlpha := float32(math.Sin(float64(progress) * math.Pi))
		finalAlpha := sinAlpha * vol.Speed
		a := uint8(mathutil.ClampF(finalAlpha*255, 0, 255))
		if vol.hasTex {
			return rl.Color{R: 255, G: 255, B: 255, A: a}
		}
		return rl.Color{R: 255, G: 240, B: 180, A: a}

	default:
		// For dust storm, falling leaves, and fallback: linear fade in/out.
		alphaScale := float32(1.0)
		if progress < 0.1 {
			alphaScale = progress / 0.1
		} else if progress > 0.8 {
			alphaScale = (1.0 - progress) / 0.2
		}

		if vol.hasTex {
			a := uint8(mathutil.ClampF(alphaScale*255, 0, 255))
			return rl.Color{R: 255, G: 255, B: 255, A: a}
		}

		switch vol.EffectType {
		case esf.EffectTypeDustStorm:
			a := uint8(mathutil.ClampF(0.4*alphaScale*255, 0, 255))
			return rl.Color{R: 180, G: 160, B: 120, A: a}
		case esf.EffectTypeFallingLeaves:
			// PS2: alpha = lifetime * speed, only visible in states 1-2
			if p.State == 0 {
				return rl.Color{A: 0}
			}
			leafAlpha := p.Lifespan
			if leafAlpha > 1.0 {
				leafAlpha = 1.0
			}
			a := uint8(mathutil.ClampF(leafAlpha*0.6*255, 0, 255))
			t := float32(math.Sin(float64(p.Phase)))*0.5 + 0.5
			r := uint8(60 + t*100)
			g := uint8(120 - t*40)
			b := uint8(30 + t*20)
			return rl.Color{R: r, G: g, B: b, A: a}
		default:
			return rl.Color{R: 200, G: 200, B: 200, A: 100}
		}
	}
}


// generateGaussianBlob creates a soft white circle with smooth alpha gradient
// from center to edge. No flat opaque center — the entire radius is a smooth
// translucent falloff: (1-dist²)² giving natural fog appearance.
func generateGaussianBlob(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	cx, cy := float64(size)/2.0, float64(size)/2.0
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx := (float64(x) + 0.5 - cx) / cx
			dy := (float64(y) + 0.5 - cy) / cy
			dist2 := dx*dx + dy*dy
			// Smooth translucent falloff: (1-dist²)², zero at edges, max at center.
			a := 0.0
			if dist2 < 1.0 {
				t := 1.0 - dist2
				a = t * t
			}
			i := img.PixOffset(x, y)
			img.Pix[i+0] = 255
			img.Pix[i+1] = 255
			img.Pix[i+2] = 255
			img.Pix[i+3] = uint8(a * 255)
		}
	}
	return img
}

// applyRadialAlphaMask multiplies BOTH RGB and alpha by a radial falloff.
// Premultiplied: edge pixels have RGB=0 AND alpha=0, so they contribute
// absolutely nothing under any blend mode (additive or alpha).
func applyRadialAlphaMask(src *image.NRGBA) *image.NRGBA {
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	cx, cy := float64(w)/2.0, float64(h)/2.0
	dst := image.NewNRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			i := src.PixOffset(x, y)
			dx := (float64(x) + 0.5 - cx) / cx
			dy := (float64(y) + 0.5 - cy) / cy
			dist := math.Sqrt(dx*dx + dy*dy)
			// Smooth falloff: (1-dist^2)^2, fully transparent at edges
			mask := 0.0
			if dist < 1.0 {
				t := 1.0 - dist*dist
				mask = t * t
			}
			j := dst.PixOffset(x, y)
			dst.Pix[j+0] = uint8(float64(src.Pix[i+0]) * mask)
			dst.Pix[j+1] = uint8(float64(src.Pix[i+1]) * mask)
			dst.Pix[j+2] = uint8(float64(src.Pix[i+2]) * mask)
			dst.Pix[j+3] = uint8(float64(src.Pix[i+3]) * mask)
		}
	}
	return dst
}

// scalePS2AlphaEffectVol converts PS2 alpha (0-128) to standard (0-255).
func scalePS2AlphaEffectVol(src *image.NRGBA) *image.NRGBA {
	bounds := src.Bounds()
	dst := image.NewNRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			i := src.PixOffset(x, y)
			a := src.Pix[i+3]
			a2 := int(a) * 255 / 128
			if a2 > 255 {
				a2 = 255
			}
			j := dst.PixOffset(x, y)
			dst.Pix[j+0] = src.Pix[i+0]
			dst.Pix[j+1] = src.Pix[i+1]
			dst.Pix[j+2] = src.Pix[i+2]
			dst.Pix[j+3] = uint8(a2)
		}
	}
	return dst
}
