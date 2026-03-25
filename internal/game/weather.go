package game

// Weather particle system — ported from PS2 VIAtmosphere.
//
// PS2 WXTYPE enum (from SetWeather switch table at 0x00BD9F00):
//   0 = CLEAR, 1 = RAIN, 2 = SNOW, 3 = SLEET, 4 = SANDSTORM,
//   5 = LAVASTORM, 6 = THUNDERSTORM
//
// Wire format: opcodes 0x00C5 (WeatherControl) and 0x00C7 (SkyboxWeather)
// each send a single byte (0-6). 0xC5 = instant, 0xC7 = fade transition.

import (
	"math"
	"math/rand"

	rl "github.com/gen2brain/raylib-go/raylib"
)

// WeatherType matches PS2 WXTYPE enum.
type WeatherType int

const (
	WeatherClear      WeatherType = 0
	WeatherRain       WeatherType = 1
	WeatherSnow       WeatherType = 2
	WeatherSleet      WeatherType = 3
	WeatherSandstorm  WeatherType = 4
	WeatherLavastorm  WeatherType = 5
	WeatherThunder    WeatherType = 6
)

// PS2 VIAtmosphere constants (from Init at 0x00416B60).
const (
	weatherMaxParticles = 200  // offset +8 in VIAtmosphere
	weatherRange        = 10.0 // offset +4 — spawn radius around camera
)

// weatherParticle is a single rain/snow/sand/etc particle.
type weatherParticle struct {
	x, y, z    float32 // world position
	vx, vy, vz float32 // velocity
	age        float32 // seconds alive
	lifespan   float32 // seconds to live
	size       float32 // billboard half-size
}

// WeatherSystem manages weather particle rendering.
type WeatherSystem struct {
	Type       WeatherType
	TargetType WeatherType
	Intensity  float32 // 0.0 = off, 1.0 = full (transition blend)

	particles []weatherParticle
	rng       *rand.Rand

	// PS2 VIAtmosphere wind direction (computed from game time).
	// Update at 0x00418280: windAngle = gameTime * π/12
	// windX = sin(windAngle) * 35.0, windZ = cos(windAngle) * -35.0
	windX, windZ float32

	// PS2 VIAtmosphere textures from SKY.ESF (set via SetTextures).
	rainTex   rl.Texture2D
	snowTex   rl.Texture2D
	splashTex rl.Texture2D
	hasTex    bool // true if any texture was loaded
}

// NewWeatherSystem creates a weather system.
func NewWeatherSystem() *WeatherSystem {
	return &WeatherSystem{
		Type:      WeatherClear,
		Intensity: 0,
		particles: make([]weatherParticle, 0, weatherMaxParticles),
		rng:       rand.New(rand.NewSource(42)),
	}
}

// SetTextures assigns GPU textures for weather particles.
// Called from asset loading after extracting from SKY.ESF.
func (ws *WeatherSystem) SetTextures(rain, snow, splash rl.Texture2D) {
	ws.rainTex = rain
	ws.snowTex = snow
	ws.splashTex = splash
	ws.hasTex = rain.ID > 0 || snow.ID > 0 || splash.ID > 0
}

// SetWeather changes the active weather type.
// If fade is true, transition smoothly; otherwise instant.
func (ws *WeatherSystem) SetWeather(wt WeatherType, fade bool) {
	if wt == WeatherClear {
		ws.TargetType = WeatherClear
		if !fade {
			ws.Type = WeatherClear
			ws.Intensity = 0
			ws.particles = ws.particles[:0]
		}
		return
	}
	ws.TargetType = wt
	if !fade {
		ws.Type = wt
		ws.Intensity = 1.0
	}
}

// weatherConfig holds per-type particle parameters.
type weatherConfig struct {
	velY     float32 // downward velocity (negative = down)
	windMul  float32 // multiplier for global wind direction (0 = no wind influence)
	size     float32 // particle size
	lifespan float32 // seconds
	color    rl.Color
}

func getWeatherConfig(wt WeatherType) weatherConfig {
	switch wt {
	case WeatherRain:
		return weatherConfig{
			velY: -15.0, windMul: 0.02,
			size: 0.02, lifespan: 1.6,
			color: rl.Color{R: 180, G: 200, B: 255, A: 100},
		}
	case WeatherSnow:
		return weatherConfig{
			velY: -1.5, windMul: 0.03,
			size: 0.06, lifespan: 8.0,
			color: rl.Color{R: 255, G: 255, B: 255, A: 180},
		}
	case WeatherSleet:
		return weatherConfig{
			velY: -10.0, windMul: 0.02,
			size: 0.03, lifespan: 2.0,
			color: rl.Color{R: 200, G: 210, B: 240, A: 120},
		}
	case WeatherSandstorm:
		return weatherConfig{
			velY: -0.5, windMul: 0.10,
			size: 0.05, lifespan: 3.0,
			color: rl.Color{R: 210, G: 180, B: 120, A: 100},
		}
	case WeatherThunder:
		return weatherConfig{
			velY: -18.0, windMul: 0.01,
			size: 0.02, lifespan: 1.3,
			color: rl.Color{R: 160, G: 180, B: 220, A: 110},
		}
	default:
		return weatherConfig{}
	}
}

// Update advances the weather simulation by dt seconds.
// gameHour is 0.0-24.0 (same as sky dome time).
func (ws *WeatherSystem) Update(dt, gameHour float32, camX, camY, camZ float32) {
	// PS2 VIAtmosphere::Update at 0x00418280:
	// windAngle = gameTime * π/12
	// windX = sin(windAngle) * 35.0, windZ = cos(windAngle) * -35.0
	windAngle := float64(gameHour) * (math.Pi / 12.0)
	ws.windX = float32(math.Sin(windAngle)) * 35.0
	ws.windZ = float32(math.Cos(windAngle)) * -35.0

	// Transition intensity toward target.
	if ws.TargetType == WeatherClear {
		ws.Intensity -= dt * 0.5 // fade out over 2 seconds
		if ws.Intensity <= 0 {
			ws.Intensity = 0
			ws.Type = WeatherClear
			ws.particles = ws.particles[:0]
			return
		}
	} else {
		if ws.Type != ws.TargetType {
			ws.Type = ws.TargetType
			ws.particles = ws.particles[:0]
		}
		ws.Intensity += dt * 0.5
		if ws.Intensity > 1.0 {
			ws.Intensity = 1.0
		}
	}

	if ws.Type == WeatherClear || ws.Type == WeatherLavastorm {
		return
	}

	cfg := getWeatherConfig(ws.Type)

	// Age existing particles, remove dead ones.
	alive := 0
	for i := range ws.particles {
		p := &ws.particles[i]
		p.age += dt
		if p.age >= p.lifespan {
			continue
		}
		// Move particle: gravity + coherent wind drift.
		p.x += (p.vx + ws.windX*cfg.windMul) * dt
		p.y += p.vy * dt
		p.z += (p.vz + ws.windZ*cfg.windMul) * dt

		// Snow: gentle sinusoidal wobble on top of wind.
		if ws.Type == WeatherSnow {
			p.x += float32(math.Sin(float64(p.age*2.0+p.z))) * 0.3 * dt
			p.z += float32(math.Cos(float64(p.age*1.7+p.x))) * 0.3 * dt
		}

		ws.particles[alive] = ws.particles[i]
		alive++
	}
	ws.particles = ws.particles[:alive]

	// PS2: particleCount = intensity * maxParticles (uniform 200 for all types).
	targetCount := int(ws.Intensity * weatherMaxParticles)
	for len(ws.particles) < targetCount {
		p := weatherParticle{
			x:        camX + (ws.rng.Float32()*2-1)*weatherRange,
			y:        camY + weatherRange*ws.rng.Float32(),
			z:        camZ + (ws.rng.Float32()*2-1)*weatherRange,
			vx:       (ws.rng.Float32()*2 - 1) * 0.5, // small random per-particle variation
			vy:       cfg.velY,
			vz:       (ws.rng.Float32()*2 - 1) * 0.5,
			age:      ws.rng.Float32() * cfg.lifespan * 0.5, // stagger births
			lifespan: cfg.lifespan * (0.8 + ws.rng.Float32()*0.4),
			size:     cfg.size * (0.7 + ws.rng.Float32()*0.6),
		}
		ws.particles = append(ws.particles, p)
	}
}

// weatherTexture returns the appropriate texture for the current weather type.
func (ws *WeatherSystem) weatherTexture() rl.Texture2D {
	switch ws.Type {
	case WeatherRain, WeatherSleet, WeatherThunder:
		return ws.rainTex
	case WeatherSnow:
		return ws.snowTex
	default:
		return rl.Texture2D{}
	}
}

// weatherBlendAdditive returns true for particle types that should use additive
// blending (texture has opaque black background, not premultiplied alpha).
func (ws *WeatherSystem) weatherBlendAdditive() bool {
	switch ws.Type {
	case WeatherSnow:
		return true
	default:
		return false
	}
}

// Render draws all weather particles as billboard quads.
func (ws *WeatherSystem) Render(camera rl.Camera3D) {
	if ws.Type == WeatherClear || len(ws.particles) == 0 {
		return
	}

	cfg := getWeatherConfig(ws.Type)

	camRight, camUp := billboardVectors(camera)

	// Rain: use vertical-stretch billboards instead of camera-facing.
	isRain := ws.Type == WeatherRain || ws.Type == WeatherSleet || ws.Type == WeatherThunder
	if isRain {
		// Stretch vertically for rain streak effect.
		camUp = rl.Vector3{X: 0, Y: 1, Z: 0}
	}

	rl.DisableDepthMask()

	// Snow/textured particles: additive blend (black bg adds nothing).
	// Rain/sand/etc: standard alpha blend.
	tex := ws.weatherTexture()
	useTex := tex.ID > 0
	if useTex && ws.weatherBlendAdditive() {
		rl.BeginBlendMode(rl.BlendAdditive)
	} else {
		rl.BeginBlendMode(rl.BlendAlpha)
	}

	if useTex {
		rl.SetTexture(tex.ID)
	}
	rl.Begin(rl.Quads)

	alpha := uint8(float32(cfg.color.A) * ws.Intensity)
	clr := cfg.color
	clr.A = alpha

	for i := range ws.particles {
		p := &ws.particles[i]
		if p.age >= p.lifespan {
			continue
		}

		// Fade in/out at edges of lifespan.
		fadeAge := p.age / p.lifespan
		fade := float32(1.0)
		if fadeAge < 0.1 {
			fade = fadeAge * 10.0
		} else if fadeAge > 0.8 {
			fade = (1.0 - fadeAge) * 5.0
		}
		a := uint8(float32(alpha) * fade)
		if a == 0 {
			continue
		}

		size := p.size
		var rx, ry, rz, ux, uy, uz float32
		if isRain {
			// Rain streaks: narrow horizontal, tall vertical.
			rx = camRight.X * size * 0.3
			ry = camRight.Y * size * 0.3
			rz = camRight.Z * size * 0.3
			ux = 0
			uy = size * 3.0
			uz = 0
		} else {
			rx = camRight.X * size
			ry = camRight.Y * size
			rz = camRight.Z * size
			ux = camUp.X * size
			uy = camUp.Y * size
			uz = camUp.Z * size
		}

		rl.Color4ub(clr.R, clr.G, clr.B, a)
		if useTex {
			rl.TexCoord2f(0, 1)
		}
		rl.Vertex3f(p.x-rx-ux, p.y-ry-uy, p.z-rz-uz)
		if useTex {
			rl.TexCoord2f(1, 1)
		}
		rl.Vertex3f(p.x+rx-ux, p.y+ry-uy, p.z+rz-uz)
		if useTex {
			rl.TexCoord2f(1, 0)
		}
		rl.Vertex3f(p.x+rx+ux, p.y+ry+uy, p.z+rz+uz)
		if useTex {
			rl.TexCoord2f(0, 0)
		}
		rl.Vertex3f(p.x-rx+ux, p.y-ry+uy, p.z-rz+uz)
	}

	rl.End()
	if useTex {
		rl.SetTexture(0) // unbind
	}
	rl.EndBlendMode()
	rl.EnableDepthMask()
}
