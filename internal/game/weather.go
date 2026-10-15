package game

// Weather — a port of the weather half of the PS2's VIAtmosphere
// (decomp/src/viatmosphere.cpp): SetWeather and ProcessTransitions run the
// state machine, ProcessWeather moves the 200 camera-relative particles,
// RenderWeather/RenderSplashes draw them, and ResetLightning/GetLightning run
// the storm's flash and thunder.
//
// The weather type is VIAtmosphere::WXTYPE, the byte opcodes 0x00C5 and 0x00C7
// carry (CLIENT func_00BD9F00 passes 0..6 straight through): 0 clear, 1..3
// light, medium and heavy rain, 4..6 light, medium and heavy snow. 0xC5 fades
// over 30 seconds (SetWeather(t, false)); 0xC7 is instant (SetWeather(t, true)).
// There is no sleet, sandstorm or thunder type: lightning accompanies any rain
// under a fully overcast sky, and the Lavastorm is a separate per-region effect.

import (
	"math"
	"math/rand"

	rl "github.com/gen2brain/raylib-go/raylib"
)

// WeatherType is the PS2 WXTYPE.
type WeatherType int

const (
	WeatherClear     WeatherType = 0
	WeatherLightRain WeatherType = 1
	WeatherRain      WeatherType = 2
	WeatherHeavyRain WeatherType = 3
	WeatherLightSnow WeatherType = 4
	WeatherSnow      WeatherType = 5
	WeatherHeavySnow WeatherType = 6
)

// WeatherNames labels each WXTYPE.
var WeatherNames = [7]string{"Clear", "Light rain", "Rain", "Heavy rain", "Light snow", "Snow", "Heavy snow"}

// weatherLevel is ProcessTransitions' lv[]: the intensity each type settles at.
var weatherLevel = [7]float32{0, 0.15, 0.5, 1, 0.15, 0.5, 1}

// weatherKind is ProcessTransitions' st[]: 0 none, 1 rain, 2 snow.
func weatherKind(t WeatherType) int {
	switch {
	case t <= WeatherClear:
		return 0
	case t >= WeatherLightSnow:
		return 2
	}
	return 1
}

func (t WeatherType) valid() bool { return t >= WeatherClear && t <= WeatherHeavySnow }

// VIAtmosphere::Init.
const (
	weatherParticles = 200  // m_numParticles
	weatherRange     = 10.0 // m_range: the box the particles wrap in
	weatherAhead     = 5.0  // the box is centred on cam + dir*5
	weatherFadeTime  = 30.0 // ProcessTransitions: m_p608 += dt/30
)

// Per-kind particle constants from VIAtmosphere::Init; index 0 is rain, 1 snow.
var (
	weatherSize    = [2]float32{0.095, 0.05}                                       // m_p88: billboard half-size
	weatherSquashW = [2]float32{0, 0}                                              // m_p90
	weatherSquashH = [2]float32{0.8, 0}                                            // m_p98
	weatherWobble  = [2][3]float32{{0.0025, 0.008, 0.0025}, {0.0106, 0.01723, 0.0106}} // m_p50
	weatherFall    = [2]float32{-0.21042, -0.02199}                                // m_p68.y, per 1/60 s
)

// weatherParticle is VIWeatherParticle: its position is relative to the box
// centre, groundY (m_p1C) is the world height of the topmost surface beneath
// it — ground, roof or water — and water (m_p20) says the surface is water.
type weatherParticle struct {
	x, y, z float32
	groundY float32
	water   bool
	alpha   float32
}

// weatherSplash is VISplash: one splash billboard stepping through the
// splash texture's four quadrants.
type weatherSplash struct {
	x, y, z float32
	bright  float32
	frame   int
}

// WeatherSystem is the PS2 VIAtmosphere's weather state.
type WeatherSystem struct {
	Type       WeatherType // m_p5FC: the weather now
	TargetType WeatherType // m_p600: the weather a transition is heading to
	pending    WeatherType // m_p604: a request queued while a transition runs
	t          float32     // m_p608: 0 when idle, else the transition (1 = done)
	skyNow     int         // m_p5EC: 0 clear sky, 1 overcast
	skyNext    int         // m_p5F0
	defaultSky int         // m_p60C: the region's default sky (Init: 1; 2 is overcast)

	Intensity float32 // m_intensity
	EffectMix float32 // m_effectMix: 0 all rain .. 1 all snow

	// Read by the sky and the lights each frame.
	OvercastInterp float32 // GetOvercastInterp
	NightFactor    float32 // m_p61C: how much of the night sky shows
	SkyScale       float32 // m_skyScale: sun, corona and moon visibility
	SunAlpha       uint8   // the alpha ProcessTransitions gives the sun and moon materials
	CoronaAlpha    uint8   // ... and the corona's, 255-(255-a)*2

	// LightningBrightness is GetLightning's output this frame, 0 when no flash
	// is in progress. It whitens the sun and lights the sky's flash patches.
	LightningBrightness float32

	// Outdoors is the PS2's m_soundTrack.m_p30 — the baked sun gate below the
	// camera, eased at 0.2/s (VIScene::CalcOutdoorsIntensity) — set by the caller.
	Outdoors float32

	// Ground reports the topmost surface at (x, z) — world y, whether it is
	// water, and whether there is any — for the per-particle ray ProcessWeather
	// casts from 5000 above to 5000 below. Rain stops at a roof because of it.
	Ground func(x, z float32) (y float32, water bool, ok bool)

	// Lightning (ResetLightning / GetLightning).
	cycle     float32 // m_p1198: seconds left in the 8 s cycle
	flashAt   float32 // m_p119C: when in the cycle the flash starts
	flashT    float32 // m_p11A0: time into the flash, 0 = none
	flashDist float32 // m_p11A8: 0.1..0.8, dims the flash and the thunder
	flashDur  float32 // m_p11A4
	thunder   []float32
	rainOn    bool // m_p1190 != -1: the rain voices are started

	particles [weatherParticles]weatherParticle
	ref       [3]float32 // m_camera: last frame's box centre
	wobble    [64][3]float32
	fallVar   [64]float32
	phase     float32 // m_p84
	splashes  []weatherSplash
	splashDt  float32
	rng       *rand.Rand

	rainTex   rl.Texture2D
	snowTex   rl.Texture2D
	splashTex rl.Texture2D

	// PS2 weather sounds (SetResources): "rain_lt" and "lightning_far02".
	RainSoundDictID    int32
	ThunderSoundDictID int32
	rainSoundPlaying   bool
}

// NewWeatherSystem creates a weather system — VIAtmosphere::Init.
func NewWeatherSystem() *WeatherSystem {
	ws := &WeatherSystem{
		defaultSky:  1,
		NightFactor: 1,
		SkyScale:    1,
		SunAlpha:    255,
		CoronaAlpha: 255,
		Outdoors:    1,
		rng:         rand.New(rand.NewSource(1)),
	}
	for i := 0; i < 64; i++ {
		a := float64(i) * 2 * math.Pi / 64 * 2
		ws.wobble[i] = [3]float32{float32(math.Sin(a)), float32(math.Cos(a)), float32(math.Cos(a))}
		ws.fallVar[i] = float32(ws.rng.Intn(256))/255 + 1
	}
	for i := range ws.particles {
		p := &ws.particles[i]
		p.x = ws.rng.Float32()*weatherRange - weatherRange/2
		p.y = ws.rng.Float32()*weatherRange - weatherRange/2
		p.z = ws.rng.Float32()*weatherRange - weatherRange/2
		p.groundY = -math.MaxFloat32
		p.alpha = ws.virand()*0.08 + 0.1 // 0.02 .. 0.18
	}
	return ws
}

// virand is the PS2's VIRAND, (rand() - rand()) / 2^31: -1..1, triangular.
func (ws *WeatherSystem) virand() float32 {
	return ws.rng.Float32() - ws.rng.Float32()
}

// rand255 is ((rand()*131) & 0xFF) / 255.0f: 0..1.
func (ws *WeatherSystem) rand255() float32 {
	return float32(ws.rng.Intn(256)) / 255
}

// SetTextures assigns the rain, snow and splash textures (rain.tga, snow.tga,
// splash.tga in SKY.ESF).
func (ws *WeatherSystem) SetTextures(rain, snow, splash rl.Texture2D) {
	ws.rainTex = rain
	ws.snowTex = snow
	ws.splashTex = splash
}

// SetWeather is VIAtmosphere::SetWeather: start a transition to t — instant,
// or over 30 seconds — or queue it if one is already running.
func (ws *WeatherSystem) SetWeather(t WeatherType, instant bool) {
	if !t.valid() {
		return
	}
	if ws.t != 0 {
		ws.pending = t
		return
	}
	if ws.Type == t {
		return
	}
	if instant {
		ws.t = 1
	} else {
		ws.t = 1e-5
	}
	ws.pending = t
	ws.TargetType = t
	if t == WeatherClear {
		ws.skyNext = 0
		if ws.defaultSky == 2 {
			ws.skyNext = 1
		}
	} else {
		ws.skyNext = 1
	}
}

// setIntensity is VIAtmosphere::SetIntensity.
func (ws *WeatherSystem) setIntensity(f float32) {
	if f > 0 && ws.Intensity == 0 {
		ws.splashes = ws.splashes[:0]
	}
	ws.Intensity = f
}

// nightFactor is m_p61C outside a transition: 1 at midnight, falling to 0 at
// 6:00 and rising again from 18:00.
func nightFactor(hour float32) float32 {
	switch {
	case hour < 6:
		return 1 - hour/6
	case hour > 18:
		return (hour - 18) / 6
	}
	return 0
}

// resetLightning is VIAtmosphere::ResetLightning: a new 8-second cycle whose
// flash starts flashDist*6 seconds before its end.
func (ws *WeatherSystem) resetLightning() {
	ws.flashDist = ws.rand255()
	ws.flashDist = min(max(0.1, ws.flashDist), 0.8)
	ws.cycle = 8
	ws.flashAt = ws.flashDist * 6
	ws.flashDur = ws.rand255()*0.6 + 0.6
}

// overcastInterp is VIAtmosphere::GetOvercastInterp.
func (ws *WeatherSystem) overcastInterp() float32 {
	if ws.skyNow == 0 {
		if ws.skyNext == 0 {
			return 0
		}
		if ws.skyNext == 1 {
			return ws.t
		}
	}
	if ws.skyNow == 1 && ws.skyNext == 0 {
		return 1 - ws.t
	}
	return 1
}

// Update advances the weather by dt seconds — VIAtmosphere::Update, which runs
// ProcessWeather and then ProcessTransitions — and computes this frame's
// lightning flash (GetLightning, which SetupTimeOfDayLights calls).
func (ws *WeatherSystem) Update(dt, gameHour float32, camera rl.Camera3D) {
	ws.processWeather(dt, camera)
	ws.processTransitions(dt, gameHour)
	ws.LightningBrightness = ws.getLightning(dt)
	ws.OvercastInterp = ws.overcastInterp()

	// RenderSplashes steps each splash once per frame of the PS2's 30 Hz loop.
	ws.splashDt += dt
	for ws.splashDt >= 1.0/30 {
		ws.splashDt -= 1.0 / 30
		alive := ws.splashes[:0]
		for _, s := range ws.splashes {
			s.frame++
			if s.frame < 4 {
				alive = append(alive, s)
			}
		}
		ws.splashes = alive
	}
}

// processTransitions is VIAtmosphere::ProcessTransitions minus the sky-dome
// material work, which the sky does from t, skyNow and skyNext.
func (ws *WeatherSystem) processTransitions(dt, hour float32) {
	if ws.t != 0 {
		ws.NightFactor = nightFactor(hour)
		ws.t = min(ws.t+dt/weatherFadeTime, 1)

		var alpha int
		switch {
		case ws.skyNow == 0 && ws.skyNext == 1:
			alpha = 255 - int(ws.t*255)
			ws.NightFactor *= 1 - ws.t
		case ws.skyNow == 1 && ws.skyNext == 0:
			alpha = int(ws.t * 255)
			ws.NightFactor *= ws.t
		case ws.skyNow != 0 && ws.skyNext != 0:
			alpha = 0
			ws.NightFactor = 0
		default:
			alpha = 255
		}
		ws.SunAlpha = uint8(alpha)
		ws.CoronaAlpha = uint8(max(255-(255-alpha)*2, 0))
		ws.SkyScale = float32(alpha) / 255

		if ws.Type > 0 || ws.TargetType > 0 {
			from, to := weatherKind(ws.Type), weatherKind(ws.TargetType)
			switch {
			case from == 1 && to == 2:
				ws.EffectMix = ws.t
			case from == 2 && to == 1:
				ws.EffectMix = 1 - ws.t
			case from == 1 && to == 1:
				ws.EffectMix = 0
			case from == 2 && to == 2:
				ws.EffectMix = 1
			case from == 0:
				if to == 1 {
					ws.EffectMix = 0
				} else {
					ws.EffectMix = 1
				}
			case from == 1:
				ws.EffectMix = 0
			default:
				ws.EffectMix = 1
			}
			lf, lt := weatherLevel[ws.Type], weatherLevel[ws.TargetType]
			ws.setIntensity(lf + (lt-lf)*ws.t)
			if !ws.rainOn {
				ws.rainOn = true
				ws.resetLightning()
			}
		} else {
			ws.setIntensity(0)
			ws.rainOn = false
		}

		if ws.t == 1 {
			ws.skyNow = ws.skyNext
			ws.Type = ws.TargetType
			ws.t = 0
			if ws.pending != ws.Type {
				ws.SetWeather(ws.pending, false)
			}
		}
	} else if ws.skyNow == 0 {
		ws.NightFactor = nightFactor(hour)
		ws.SkyScale = 1
		ws.SunAlpha, ws.CoronaAlpha = 255, 255
	} else {
		ws.NightFactor = 0
		ws.SkyScale = 0
		ws.SunAlpha, ws.CoronaAlpha = 0, 0
	}

	// Lightning and thunder accompany any rain: an 8-second cycle whose flash
	// starts flashAt seconds before its end and whose thunder — two voices,
	// quieter the farther the flash — ends it.
	if ws.Intensity != 0 && ws.EffectMix < 1 {
		if ws.cycle < ws.flashAt {
			ws.flashAt = 0
			ws.flashT = 1e-5
		}
		ws.cycle -= dt
		if ws.cycle <= 0 {
			ws.thunder = append(ws.thunder, (1-ws.flashDist)*ws.Intensity*(1-ws.EffectMix)*0.9*ws.Outdoors)
			ws.resetLightning()
		}
	} else if ws.Intensity == 0 {
		ws.flashT, ws.cycle, ws.flashAt = 0, 0, 0
	}
}

// getLightning is VIAtmosphere::GetLightning: the flash brightness this frame,
// only while the sky is fully overcast. It ramps in over 0.2 s, flickers, and
// is dimmed by the flash's distance.
func (ws *WeatherSystem) getLightning(dt float32) float32 {
	if ws.skyNow != 1 || ws.skyNext != 1 {
		ws.flashT = 0
		return 0
	}
	t := ws.flashT
	if t == 0 {
		return 0
	}
	ws.flashT = t + dt
	var b float32
	if ws.flashT < 0.2 {
		b = ws.flashT/0.2 + ws.virand()*0.2
	} else {
		b = (ws.flashT-0.2)/(ws.flashDur-0.2) + ws.virand()*0.3
		if ws.rng.Intn(16) < 2 {
			b = ws.rand255()*0.2 + 0.8
		}
	}
	if ws.flashT >= ws.flashDur {
		ws.flashT = 0
	}
	c := min(max(0, b), 1)
	return c * (1 - ws.flashDist) * 0.8
}

// retrace casts a particle's ray (ProcessWeather: from 5000 above to 5000
// below) and drops it to a random height between the box top and the surface.
func (ws *WeatherSystem) retrace(p *weatherParticle) {
	if ws.Ground == nil {
		return
	}
	y, water, ok := ws.Ground(p.x+ws.ref[0], p.z+ws.ref[2])
	if !ok {
		return
	}
	p.groundY = y
	p.water = water
	half := float32(weatherRange / 2)
	p.y = y - ws.ref[1]
	p.y = half + (p.y-half)*ws.rand255()
}

// processWeather is VIAtmosphere::ProcessWeather.
func (ws *WeatherSystem) processWeather(dt float32, camera rl.Camera3D) {
	if ws.Intensity == 0 {
		return
	}
	fwd := rl.Vector3Normalize(rl.Vector3Subtract(camera.Target, camera.Position))
	c := [3]float32{
		camera.Position.X + fwd.X*weatherAhead,
		camera.Position.Y + fwd.Y*weatherAhead,
		camera.Position.Z + fwd.Z*weatherAhead,
	}
	d := [3]float32{c[0] - ws.ref[0], c[1] - ws.ref[1], c[2] - ws.ref[2]}
	n := int(ws.Intensity * weatherParticles)
	k := int(float32(n) * ws.EffectMix)
	ws.ref = c
	if weatherRange < d[0] || weatherRange < d[1] || weatherRange < d[2] {
		d = [3]float32{}
		for i := range ws.particles {
			ws.retrace(&ws.particles[i])
		}
	}

	j := int(ws.phase)
	ws.phase += dt * 10
	rate := dt * 60
	if ws.phase > 64 {
		ws.phase -= 64
	}
	half := float32(weatherRange / 2)

	for i := 0; i < weatherParticles; i, j = i+1, j+29 {
		p := &ws.particles[i]
		sel := 0
		if i < k {
			sel = 1
		}
		w := ws.wobble[(j+i)&63]
		p.x += w[0] * weatherWobble[sel][0] * rate
		p.y += (w[1]*weatherWobble[sel][1] + weatherFall[sel]*ws.fallVar[i&63]) * rate
		p.z += w[2] * weatherWobble[sel][2] * rate

		p.y -= d[1]
		if p.y < -half {
			p.y += weatherRange
		} else if half <= p.y {
			p.y -= weatherRange
		}
		wrapped := false
		p.x -= d[0]
		p.z -= d[2]
		if p.x < -half {
			wrapped = true
			p.x += weatherRange
		} else if half <= p.x {
			wrapped = true
			p.x -= weatherRange
		}
		if p.z < -half {
			wrapped = true
			p.z += weatherRange
		} else if half <= p.z {
			wrapped = true
			p.z -= weatherRange
		}
		if wrapped {
			ws.retrace(p)
		}

		if p.y+ws.ref[1] < p.groundY {
			if sel == 0 && i < n {
				ws.addSplash(p.x+ws.ref[0], p.groundY+0.08, p.z+ws.ref[2])
			}
			if p.water {
				p.x = ws.rand255()*weatherRange - half
				p.z = ws.rand255()*weatherRange - half
			}
			p.y = max(p.groundY-ws.ref[1], half)
		}
	}
}

// addSplash is VIAtmosphere::AddSplash: up to one per particle.
func (ws *WeatherSystem) addSplash(x, y, z float32) {
	if len(ws.splashes) >= weatherParticles {
		return
	}
	ws.splashes = append(ws.splashes, weatherSplash{x: x, y: y, z: z, bright: ws.rand255()*0.2 + 0.282})
}

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

// WantsRainLoop reports whether the rain voices should be playing. The PS2
// starts them with any weather and holds them at a volume that is zero for
// pure snow.
func (ws *WeatherSystem) WantsRainLoop() bool {
	return ws.Intensity > 0 && ws.EffectMix < 1
}

// RainVolume is ProcessTransitions' rain voice volume:
// intensity * (1 - effect mix) * 0.5 * outdoors.
func (ws *WeatherSystem) RainVolume() float32 {
	return ws.Intensity * (1 - ws.EffectMix) * 0.5 * ws.Outdoors
}

// PopThunder returns the volume of a thunder clap due this frame, if any. The
// PS2 plays it as two voices panned hard left and right, the second pitched
// down (2496/4096).
func (ws *WeatherSystem) PopThunder() (float32, bool) {
	if len(ws.thunder) == 0 {
		return 0, false
	}
	v := ws.thunder[0]
	ws.thunder = ws.thunder[1:]
	return v, true
}

// Render draws the precipitation and the splashes — VIAtmosphere::
// RenderWeather and RenderSplashes. Particles are camera-facing billboards in
// the box around cam + dir*5: the first k = n*effectMix are snow, the rest
// rain, alpha-blended white at 0.02..0.18 times the texture, with depth test
// and no depth writes. Rain squashes vertically toward 0.2 of its height as
// the camera pitches. Splashes are additive and step through the four
// quadrants of splash.tga.
func (ws *WeatherSystem) Render(camera rl.Camera3D) {
	if ws.Intensity == 0 {
		return
	}
	n := int(ws.Intensity * weatherParticles)
	n = min(max(n, 0), weatherParticles)
	k := int(float32(n) * ws.EffectMix)

	fwd := rl.Vector3Normalize(rl.Vector3Subtract(camera.Target, camera.Position))
	flat := rl.Vector3Normalize(rl.Vector3{X: fwd.X, Z: fwd.Z})
	aspect := 1 - (fwd.X*flat.X + fwd.Y*flat.Y + fwd.Z*flat.Z)
	right, up := billboardVectors(camera)

	rl.DisableDepthMask()
	rl.BeginBlendMode(rl.BlendAlpha)
	// The PS2 draws snow first, then rain, each with its own material; in a
	// mix the squash constants are indexed by the LOOP, not the kind, so snow
	// takes rain's height squash (RenderWeather's m_p98[i]).
	if k > 0 && k < n {
		ws.drawParticles(camera, fwd, right, up, 0, k, ws.snowTex, weatherSize[1], weatherSquashW[0], weatherSquashH[0], aspect)
		ws.drawParticles(camera, fwd, right, up, k, n, ws.rainTex, weatherSize[0], weatherSquashW[1], weatherSquashH[1], aspect)
	} else {
		idx := 0
		tex := ws.rainTex
		if k > 0 {
			idx = 1
			tex = ws.snowTex
		}
		ws.drawParticles(camera, fwd, right, up, 0, n, tex, weatherSize[idx], weatherSquashW[idx], weatherSquashH[idx], aspect)
	}
	rl.EndBlendMode()

	if len(ws.splashes) > 0 && ws.splashTex.ID > 0 {
		rl.BeginBlendMode(rl.BlendAdditive)
		rl.SetTexture(ws.splashTex.ID)
		rl.Begin(rl.Quads)
		const s = 0.08
		for _, sp := range ws.splashes {
			// Frames (f+1)*4 of the billboard UV table: the quadrants in turn.
			u0 := float32(sp.frame%2) * 0.5
			v0 := float32(sp.frame/2) * 0.5
			b := uint8(sp.bright * 255)
			rl.Color4ub(b, b, b, b)
			billboardQuad(sp.x, sp.y, sp.z, right, up, s, s, u0, v0, u0+0.5, v0+0.5)
		}
		rl.End()
		rl.SetTexture(0)
		rl.EndBlendMode()
	}
	rl.EnableDepthMask()
}

func (ws *WeatherSystem) drawParticles(camera rl.Camera3D, fwd, right, up rl.Vector3, first, last int, tex rl.Texture2D, sz, squashW, squashH, aspect float32) {
	if first >= last || tex.ID == 0 {
		return
	}
	wide := sz - sz*squashW*aspect
	high := sz - sz*squashH*aspect
	rl.SetTexture(tex.ID)
	rl.Begin(rl.Quads)
	for i := first; i < last; i++ {
		p := &ws.particles[i]
		x, y, z := p.x+ws.ref[0], p.y+ws.ref[1], p.z+ws.ref[2]
		// Within 1.5 units of the camera plane the PS2 draws it at size zero.
		if (x-camera.Position.X)*fwd.X+(y-camera.Position.Y)*fwd.Y+(z-camera.Position.Z)*fwd.Z < 1.5 {
			continue
		}
		rl.Color4ub(255, 255, 255, uint8(p.alpha*255))
		billboardQuad(x, y, z, right, up, wide, high, 0, 0, 1, 1)
	}
	rl.End()
	rl.SetTexture(0)
}

// billboardQuad emits one camera-facing quad of half-extents (w, h).
func billboardQuad(x, y, z float32, right, up rl.Vector3, w, h, u0, v0, u1, v1 float32) {
	rx, ry, rz := right.X*w, right.Y*w, right.Z*w
	ux, uy, uz := up.X*h, up.Y*h, up.Z*h
	rl.TexCoord2f(u0, v1)
	rl.Vertex3f(x-rx-ux, y-ry-uy, z-rz-uz)
	rl.TexCoord2f(u1, v1)
	rl.Vertex3f(x+rx-ux, y+ry-uy, z+rz-uz)
	rl.TexCoord2f(u1, v0)
	rl.Vertex3f(x+rx+ux, y+ry+uy, z+rz+uz)
	rl.TexCoord2f(u0, v0)
	rl.Vertex3f(x-rx+ux, y-ry+uy, z-rz+uz)
}
