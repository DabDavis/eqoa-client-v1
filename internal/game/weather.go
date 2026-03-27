package game

// Weather particle system — ported from PS2 VIAtmosphere.
//
// PS2 WXTYPE enum (from SetWeather switch table at 0x00BD9F00):
//   0 = CLEAR, 1 = RAIN, 2 = SNOW, 3 = SLEET, 4 = SANDSTORM,
//   5 = LAVASTORM, 6 = THUNDERSTORM
//
// Wire format: opcodes 0x00C5 (WeatherControl) and 0x00C7 (SkyboxWeather)
// each send a single byte (0-6). 0xC5 = instant, 0xC7 = fade transition.
//
// PS2 ProcessTransitions (0x004198B0): 30-second crossfade.
// PS2 ProcessWeather (0x00418EE8): position-wrapping particles (no age/lifespan).
// PS2 GetLightning (0x0041AA58): thunderstorm screen flash + bolt geometry.

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
	weatherMaxParticles = 200   // offset +8 in VIAtmosphere
	weatherRange        = 10.0  // spawn radius around camera (Go scale)
	weatherTransRate    = 1.0 / 30.0 // PS2: 30-second full transition
)

// weatherParticle — PS2 struct is 36 bytes: pos/vel/groundY/hitGround.
// PS2 uses position wrapping (no age/lifespan) but we keep age for fade-in.
type weatherParticle struct {
	x, y, z    float32 // position relative to camera
	vx, vy, vz float32 // velocity
	size       float32 // billboard half-size
	age        float32 // time since spawn (for fade-in only)
}

// weatherSplash — ground contact splash from rain.
// PS2: 36-byte struct in linked list pool (RenderSplashes at 0x0041B420).
type weatherSplash struct {
	x, y, z    float32 // world position
	brightness float32 // 0.2-0.6 range
	frame      int     // 0-3 animation frame (4 = expired)
	age        float32 // seconds alive
}

// WeatherSystem manages weather particle rendering.
// PS2: VIAtmosphere (0x11A8 bytes).
type WeatherSystem struct {
	Type       WeatherType
	TargetType WeatherType
	Intensity  float32 // 0.0 = off, 1.0 = full (PS2 offset +0x48)

	// PS2 ProcessTransitions (0x004198B0): transitionProgress 0.0→1.0
	transitionProgress float32

	// PS2 overcast interpolation (offset +0x61C, GetOvercastInterp at 0x00418D48).
	// 0.0 = clear sky, 1.0 = fully overcast. Used by sky dome for:
	//   - Sky texture crossfade (Sunny→DarkClouds)
	//   - Sun/corona/moon alpha reduction
	//   - Star visibility reduction
	OvercastInterp float32

	// PS2 effect mix (offset +0x80, SetEffectMix at 0x00418DC0).
	// Blends between weather types during transitions.
	EffectMix float32

	particles []weatherParticle
	splashes  []weatherSplash // ground splashes (PS2: 200-node linked list pool)
	rng       *rand.Rand

	// PS2 VIAtmosphere wind (Update at 0x00418280)
	windX, windZ float32

	// Previous camera position for camera-relative particle movement (PS2 pattern)
	prevCamX, prevCamY, prevCamZ float32
	camInit                      bool

	// Lightning (PS2 GetLightning at 0x0041AA58)
	lightningTimer      float32 // countdown to next flash
	lightningProgress   float32 // time into current flash
	lightningDuration   float32 // how long current flash lasts
	lightningIntensity  float32 // peak brightness (0.0-1.0)
	LightningBrightness float32 // current brightness for screen tint (public for render)

	// PS2 VIAtmosphere textures from SKY.ESF
	rainTex   rl.Texture2D
	snowTex   rl.Texture2D
	splashTex rl.Texture2D
	hasTex    bool

	// PS2 weather sound DictIDs (from SetResources at 0x00417848)
	RainSoundDictID    int32 // "rain_lt" — looping rain ambient
	ThunderSoundDictID int32 // "lightning_far02" — thunder crack one-shot
	rainSoundPlaying   bool  // true when rain loop is active
}

// NewWeatherSystem creates a weather system.
func NewWeatherSystem() *WeatherSystem {
	return &WeatherSystem{
		Type:           WeatherClear,
		Intensity:      0,
		particles:      make([]weatherParticle, 0, weatherMaxParticles),
		splashes:       make([]weatherSplash, 0, weatherMaxParticles),
		rng:            rand.New(rand.NewSource(42)),
		lightningTimer: 3.0 + rand.Float32()*5.0,
	}
}

// SetTextures assigns GPU textures for weather particles.
func (ws *WeatherSystem) SetTextures(rain, snow, splash rl.Texture2D) {
	ws.rainTex = rain
	ws.snowTex = snow
	ws.splashTex = splash
	ws.hasTex = rain.ID > 0 || snow.ID > 0 || splash.ID > 0
}

// SetWeather changes the active weather type.
// PS2: SetWeather (0x00417F78) — simple stub that sets target state.
func (ws *WeatherSystem) SetWeather(wt WeatherType, fade bool) {
	ws.TargetType = wt
	if !fade {
		ws.Type = wt
		if wt == WeatherClear {
			ws.Intensity = 0
			ws.particles = ws.particles[:0]
			ws.splashes = ws.splashes[:0]
		} else {
			ws.Intensity = 1.0
		}
		ws.transitionProgress = 0
	}
}

// weatherConfig holds per-type particle parameters.
// PS2 color table at 0x4E0740.
type weatherConfig struct {
	velY    float32  // downward velocity
	windMul float32  // wind influence multiplier
	size    float32  // base particle size (PS2: particleSizeBase 0x088)
	color   rl.Color // particle tint
}

func getWeatherConfig(wt WeatherType) weatherConfig {
	switch wt {
	case WeatherRain:
		return weatherConfig{
			velY: -15.0, windMul: 0.02, size: 0.02,
			color: rl.Color{R: 180, G: 200, B: 255, A: 100}, // PS2: teal (71,235,174)
		}
	case WeatherSnow:
		return weatherConfig{
			velY: -1.5, windMul: 0.03, size: 0.06,
			color: rl.Color{R: 255, G: 255, B: 255, A: 180},
		}
	case WeatherSleet:
		return weatherConfig{
			velY: -10.0, windMul: 0.02, size: 0.03,
			color: rl.Color{R: 200, G: 210, B: 240, A: 120},
		}
	case WeatherSandstorm:
		return weatherConfig{
			velY: -0.5, windMul: 0.10, size: 0.05,
			color: rl.Color{R: 210, G: 180, B: 120, A: 100}, // PS2: amber (208,166,50)
		}
	case WeatherThunder:
		return weatherConfig{
			velY: -18.0, windMul: 0.01, size: 0.02,
			color: rl.Color{R: 160, G: 180, B: 220, A: 110},
		}
	default:
		return weatherConfig{}
	}
}

// Update advances the weather simulation by dt seconds.
// PS2: VIAtmosphere::Update (0x00418280) + ProcessWeather (0x00418EE8) + ProcessTransitions (0x004198B0).
func (ws *WeatherSystem) Update(dt, gameHour float32, camX, camY, camZ float32) {
	// PS2 wind: windAngle = gameTime * π/12
	windAngle := float64(gameHour) * (math.Pi / 12.0)
	ws.windX = float32(math.Sin(windAngle)) * 35.0
	ws.windZ = float32(math.Cos(windAngle)) * -35.0

	// Camera delta for position-wrapping (PS2 subtracts camera movement)
	var camDX, camDY, camDZ float32
	if ws.camInit {
		camDX = camX - ws.prevCamX
		camDY = camY - ws.prevCamY
		camDZ = camZ - ws.prevCamZ
	}
	ws.prevCamX, ws.prevCamY, ws.prevCamZ = camX, camY, camZ
	ws.camInit = true

	// --- PS2 ProcessTransitions (0x004198B0) ---
	// Transition speed: deltaTime * (1/30) = 30-second full transition.
	transitioning := ws.TargetType != ws.Type || (ws.TargetType == WeatherClear && ws.Intensity > 0)
	if transitioning {
		ws.transitionProgress += dt * weatherTransRate
		if ws.transitionProgress >= 1.0 {
			ws.transitionProgress = 0
			ws.Type = ws.TargetType
			if ws.Type == WeatherClear {
				ws.Intensity = 0
				ws.particles = ws.particles[:0]
				ws.splashes = ws.splashes[:0]
			} else {
				ws.Intensity = 1.0
			}
		} else {
			if ws.TargetType == WeatherClear {
				ws.Intensity = 1.0 - ws.transitionProgress
			} else {
				ws.Type = ws.TargetType
				ws.Intensity = ws.transitionProgress
			}
		}
	}

	// --- PS2 GetOvercastInterp (0x00418D48) ---
	// Determines sky darkening based on current/target weather.
	isOvercast := func(wt WeatherType) bool {
		return wt == WeatherRain || wt == WeatherSnow || wt == WeatherSleet ||
			wt == WeatherThunder || wt == WeatherSandstorm
	}
	curOC := isOvercast(ws.Type)
	tgtOC := isOvercast(ws.TargetType)
	if !curOC && !tgtOC {
		ws.OvercastInterp = 0.0 // clear → clear
	} else if !curOC && tgtOC {
		ws.OvercastInterp = ws.transitionProgress // clear → overcast
	} else if curOC && !tgtOC {
		ws.OvercastInterp = 1.0 - ws.transitionProgress // overcast → clear
	} else {
		ws.OvercastInterp = 1.0 // overcast → overcast
	}

	// --- PS2 effect mix (ProcessTransitions) ---
	// Controls particle blend between weather types during transition.
	if !curOC && !tgtOC {
		ws.EffectMix = 0.0
	} else if !curOC && tgtOC {
		ws.EffectMix = 1.0 - ws.transitionProgress
	} else if curOC && !tgtOC {
		ws.EffectMix = 1.0
	} else {
		ws.EffectMix = 0.5
	}

	if ws.Type == WeatherClear || ws.Type == WeatherLavastorm {
		ws.LightningBrightness = 0
		return
	}

	cfg := getWeatherConfig(ws.Type)
	halfRange := float32(weatherRange * 0.5)

	// --- PS2 ProcessWeather (0x00418EE8): position-wrapping particle update ---
	activeCount := int(ws.Intensity * weatherMaxParticles)

	// Ensure we have enough particles (spawn on first need)
	for len(ws.particles) < activeCount {
		ws.particles = append(ws.particles, weatherParticle{
			x:    (ws.rng.Float32()*2 - 1) * weatherRange,
			y:    ws.rng.Float32() * weatherRange,
			z:    (ws.rng.Float32()*2 - 1) * weatherRange,
			vx:   (ws.rng.Float32()*2 - 1) * 0.5,
			vy:   cfg.velY,
			vz:   (ws.rng.Float32()*2 - 1) * 0.5,
			size: cfg.size * (0.7 + ws.rng.Float32()*0.6),
			age:  0,
		})
	}

	// Update particles (position wrapping, no death)
	for i := 0; i < len(ws.particles) && i < activeCount; i++ {
		p := &ws.particles[i]
		p.age += dt

		// Move particle
		p.x += (p.vx + ws.windX*cfg.windMul) * dt
		p.y += p.vy * dt
		p.z += (p.vz + ws.windZ*cfg.windMul) * dt

		// Snow wobble
		if ws.Type == WeatherSnow {
			p.x += float32(math.Sin(float64(p.age*2.0+p.z))) * 0.3 * dt
			p.z += float32(math.Cos(float64(p.age*1.7+p.x))) * 0.3 * dt
		}

		// PS2: subtract camera movement (particles are camera-relative)
		p.x -= camDX
		p.y -= camDY
		p.z -= camDZ

		// PS2: wrap at range boundaries (particles never die)
		wrapped := false
		if p.y < -halfRange {
			p.y += weatherRange
			wrapped = true
		} else if p.y > halfRange {
			p.y -= weatherRange
			wrapped = true
		}
		if p.x < -weatherRange {
			p.x += weatherRange * 2
			wrapped = true
		} else if p.x > weatherRange {
			p.x -= weatherRange * 2
			wrapped = true
		}
		if p.z < -weatherRange {
			p.z += weatherRange * 2
			wrapped = true
		} else if p.z > weatherRange {
			p.z -= weatherRange * 2
			wrapped = true
		}

		// Ground contact: spawn splash (rain/thunder/sleet only)
		isRainType := ws.Type == WeatherRain || ws.Type == WeatherThunder || ws.Type == WeatherSleet
		if isRainType && p.y < -halfRange+0.5 && !wrapped {
			// Splash at ground contact point
			if len(ws.splashes) < weatherMaxParticles {
				ws.splashes = append(ws.splashes, weatherSplash{
					x: camX + p.x, y: camY + p.y + halfRange, z: camZ + p.z,
					brightness: 0.28 + ws.rng.Float32()*0.2, // PS2: 0.2825 + rand*0.2
					frame:      0,
				})
			}
		}
	}

	// Trim excess particles if intensity dropped
	if len(ws.particles) > activeCount {
		ws.particles = ws.particles[:activeCount]
	}

	// --- Update splashes (PS2: 4-frame animation, RenderSplashes at 0x0041B420) ---
	alive := 0
	for i := range ws.splashes {
		s := &ws.splashes[i]
		s.age += dt
		// PS2: frame advances each tick, 4 frames total (~0.13s per frame at 30fps)
		s.frame = int(s.age / 0.13)
		if s.frame < 4 {
			ws.splashes[alive] = ws.splashes[i]
			alive++
		}
	}
	ws.splashes = ws.splashes[:alive]

	// --- PS2 Lightning (GetLightning at 0x0041AA58) ---
	ws.LightningBrightness = 0
	if ws.Type == WeatherThunder && ws.Intensity > 0.5 {
		ws.lightningTimer -= dt
		if ws.lightningTimer <= 0 {
			// New flash: PS2 ResetLightning (0x0041A6C8)
			ws.lightningIntensity = 0.3 + ws.rng.Float32()*0.7
			ws.lightningDuration = ws.lightningIntensity * (0.6 + ws.rng.Float32()*0.6)
			ws.lightningProgress = 0
			ws.lightningTimer = 3.0 + ws.rng.Float32()*8.0 // 3-11 seconds between flashes
		}
		if ws.lightningProgress < ws.lightningDuration*1.5 {
			ws.lightningProgress += dt
			// PS2: first 0.2s ramp up, then ramp down
			var brightness float32
			if ws.lightningProgress < 0.2 {
				brightness = ws.lightningProgress / 0.2
			} else {
				brightness = 1.0 - (ws.lightningProgress-0.2)/(ws.lightningDuration-0.2)
			}
			if brightness < 0 {
				brightness = 0
			}
			// PS2: random flicker
			flicker := (ws.rng.Float32() - ws.rng.Float32()) * 0.3
			brightness = brightness*ws.lightningIntensity + flicker
			if brightness < 0 {
				brightness = 0
			}
			if brightness > 1 {
				brightness = 1
			}
			ws.LightningBrightness = brightness * 0.8 // PS2: * (1-intensity) * 0.8
		}
	}
}

// weatherTexture returns the appropriate texture for the current weather type.
// IsRainSoundPlaying returns whether the rain loop is currently active.
func (ws *WeatherSystem) IsRainSoundPlaying() bool {
	return ws != nil && ws.rainSoundPlaying
}

// SetRainSoundPlaying sets the rain sound playing state.
func (ws *WeatherSystem) SetRainSoundPlaying(playing bool) {
	if ws != nil {
		ws.rainSoundPlaying = playing
	}
}

// WantsRainLoop returns true if the weather system wants a rain ambient loop playing.
func (ws *WeatherSystem) WantsRainLoop() bool {
	if ws == nil {
		return false
	}
	return ws.Intensity > 0.1 && (ws.Type == WeatherRain || ws.Type == WeatherThunder || ws.Type == WeatherSleet)
}

// RainVolume returns the desired rain loop volume (0.0-1.0).
// PS2: intensity * (1 - effectMix) * masterVolume * constant
func (ws *WeatherSystem) RainVolume() float32 {
	return ws.Intensity * (1.0 - ws.EffectMix*0.5) * 0.4
}

// ThunderTriggered is set to true when a lightning flash starts (caller should clear after playing sound).
func (ws *WeatherSystem) PopThunder() bool {
	// Check if a new flash just started (progress near 0)
	if ws.lightningProgress > 0 && ws.lightningProgress < 0.05 && ws.lightningIntensity > 0.3 {
		return true
	}
	return false
}

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

// Render draws all weather particles as billboard quads.
// PS2: RenderWeather (0x00418700) + RenderSplashes (0x0041B420).
func (ws *WeatherSystem) Render(camera rl.Camera3D) {
	if ws.Type == WeatherClear || len(ws.particles) == 0 {
		return
	}

	cfg := getWeatherConfig(ws.Type)
	camRight, camUp := billboardVectors(camera)
	isRain := ws.Type == WeatherRain || ws.Type == WeatherSleet || ws.Type == WeatherThunder
	if isRain {
		camUp = rl.Vector3{X: 0, Y: 1, Z: 0}
	}

	rl.DisableDepthMask()

	// PS2: weather materials use additive blend mode 3
	isSnow := ws.Type == WeatherSnow
	if isSnow {
		rl.BeginBlendMode(rl.BlendAdditive)
	} else {
		rl.BeginBlendMode(rl.BlendAlpha)
	}

	tex := ws.weatherTexture()
	useTex := tex.ID > 0
	if useTex {
		rl.SetTexture(tex.ID)
	}
	rl.Begin(rl.Quads)

	alpha := uint8(float32(cfg.color.A) * ws.Intensity)
	clr := cfg.color

	activeCount := int(ws.Intensity * weatherMaxParticles)
	camX, camY, camZ := camera.Position.X, camera.Position.Y, camera.Position.Z

	for i := 0; i < len(ws.particles) && i < activeCount; i++ {
		p := &ws.particles[i]

		// Fade-in for first 0.3s (prevents pop-in)
		fade := float32(1.0)
		if p.age < 0.3 {
			fade = p.age / 0.3
		}
		a := uint8(float32(alpha) * fade)
		if a == 0 {
			continue
		}

		// World position = camera + relative offset
		wx := camX + p.x
		wy := camY + p.y
		wz := camZ + p.z

		size := p.size
		var rx, ry, rz, ux, uy, uz float32
		if isRain {
			rx = camRight.X * size * 0.3
			ry = camRight.Y * size * 0.3
			rz = camRight.Z * size * 0.3
			ux, uy, uz = 0, size*3.0, 0
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
		rl.Vertex3f(wx-rx-ux, wy-ry-uy, wz-rz-uz)
		if useTex {
			rl.TexCoord2f(1, 1)
		}
		rl.Vertex3f(wx+rx-ux, wy+ry-uy, wz+rz-uz)
		if useTex {
			rl.TexCoord2f(1, 0)
		}
		rl.Vertex3f(wx+rx+ux, wy+ry+uy, wz+rz+uz)
		if useTex {
			rl.TexCoord2f(0, 0)
		}
		rl.Vertex3f(wx-rx+ux, wy-ry+uy, wz-rz+uz)
	}

	rl.End()
	if useTex {
		rl.SetTexture(0)
	}

	// --- Render splashes (PS2: RenderSplashes at 0x0041B420) ---
	if len(ws.splashes) > 0 && ws.splashTex.ID > 0 {
		rl.SetTexture(ws.splashTex.ID)
		rl.Begin(rl.Quads)

		splashSize := float32(0.08) // PS2: 0x3DA3D70A
		for i := range ws.splashes {
			s := &ws.splashes[i]
			b := uint8(s.brightness * 255)
			rl.Color4ub(b, b, b, b)

			// PS2: 4-frame animated UV
			u0 := float32(s.frame) * 0.25
			u1 := u0 + 0.25

			// Ground-facing billboard (Y-up)
			sx, sy, sz := s.x, s.y+0.08, s.z // PS2: splashOffset = 0.08
			rl.TexCoord2f(u0, 1)
			rl.Vertex3f(sx-splashSize, sy, sz-splashSize)
			rl.TexCoord2f(u1, 1)
			rl.Vertex3f(sx+splashSize, sy, sz-splashSize)
			rl.TexCoord2f(u1, 0)
			rl.Vertex3f(sx+splashSize, sy, sz+splashSize)
			rl.TexCoord2f(u0, 0)
			rl.Vertex3f(sx-splashSize, sy, sz+splashSize)
		}
		rl.End()
		rl.SetTexture(0)
	}

	rl.EndBlendMode()
	rl.EnableDepthMask()
}
