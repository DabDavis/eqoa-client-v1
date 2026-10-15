package game

import (
	"math"
	"testing"

	rl "github.com/gen2brain/raylib-go/raylib"
)

var testCam = rl.Camera3D{Position: rl.Vector3{X: 0, Y: 10, Z: 0}, Target: rl.Vector3{X: 0, Y: 10, Z: 1}, Up: rl.Vector3{Y: 1}}

func step(ws *WeatherSystem, seconds float32) {
	const dt = 1.0 / 30
	for t := float32(0); t < seconds-1e-4; t += dt {
		ws.Update(dt, 12, testCam)
	}
}

func near(a, b float32) bool { return math.Abs(float64(a-b)) < 0.02 }

// 0xC5 fades over 30 seconds: half-way, rain is at half of heavy rain's
// intensity and the sky half overcast; at the end it settles.
func TestWeatherFade(t *testing.T) {
	ws := NewWeatherSystem()
	ws.SetWeather(WeatherHeavyRain, false)
	step(ws, 15)
	if !near(ws.Intensity, 0.5) || !near(ws.OvercastInterp, 0.5) || ws.EffectMix != 0 {
		t.Fatalf("half-way: intensity %.3f overcast %.3f mix %.3f", ws.Intensity, ws.OvercastInterp, ws.EffectMix)
	}
	step(ws, 15.1)
	if ws.Type != WeatherHeavyRain || ws.Intensity != 1 || ws.OvercastInterp != 1 || ws.t != 0 {
		t.Fatalf("settled: type %d intensity %.3f overcast %.3f t %.3f", ws.Type, ws.Intensity, ws.OvercastInterp, ws.t)
	}
}

// 0xC7 is instant; the level table is {0, .15, .5, 1} for rain and snow alike,
// and snow is EffectMix 1.
func TestWeatherInstantLevels(t *testing.T) {
	for wt, want := range map[WeatherType]float32{
		WeatherLightRain: 0.15, WeatherRain: 0.5, WeatherHeavyRain: 1,
		WeatherLightSnow: 0.15, WeatherSnow: 0.5, WeatherHeavySnow: 1,
	} {
		ws := NewWeatherSystem()
		ws.SetWeather(wt, true)
		ws.Update(1.0/30, 12, testCam)
		mix := float32(0)
		if wt >= WeatherLightSnow {
			mix = 1
		}
		if ws.Type != wt || ws.Intensity != want || ws.EffectMix != mix {
			t.Errorf("%s: type %d intensity %.3f mix %.3f", WeatherNames[wt], ws.Type, ws.Intensity, ws.EffectMix)
		}
	}
}

// A request during a transition is queued, and starts its own fade when the
// running one completes.
func TestWeatherQueue(t *testing.T) {
	ws := NewWeatherSystem()
	ws.SetWeather(WeatherRain, false)
	step(ws, 5)
	ws.SetWeather(WeatherSnow, true) // queued, and the queued change fades
	if ws.TargetType != WeatherRain {
		t.Fatalf("target changed mid-transition to %d", ws.TargetType)
	}
	step(ws, 25.1)
	if ws.Type != WeatherRain || ws.TargetType != WeatherSnow || ws.t == 0 {
		t.Fatalf("after first fade: type %d target %d t %.3f", ws.Type, ws.TargetType, ws.t)
	}
	step(ws, 15)
	// Rain to snow: EffectMix follows t, intensity stays at 0.5.
	if !near(ws.EffectMix, 0.5) || !near(ws.Intensity, 0.5) {
		t.Fatalf("rain→snow half-way: mix %.3f intensity %.3f", ws.EffectMix, ws.Intensity)
	}
}

// Clearing fades the intensity out and the sky back; with the sky overcast the
// whole time there is no star or sun.
func TestWeatherClearing(t *testing.T) {
	ws := NewWeatherSystem()
	ws.SetWeather(WeatherHeavyRain, true)
	ws.Update(1.0/30, 12, testCam)
	if ws.SkyScale != 0 || ws.SunAlpha != 0 {
		t.Fatalf("overcast: sky scale %.2f sun alpha %d", ws.SkyScale, ws.SunAlpha)
	}
	ws.SetWeather(WeatherClear, false)
	step(ws, 15)
	if !near(ws.Intensity, 0.5) || !near(ws.OvercastInterp, 0.5) {
		t.Fatalf("clearing half-way: intensity %.3f overcast %.3f", ws.Intensity, ws.OvercastInterp)
	}
	step(ws, 15.1)
	if ws.Type != WeatherClear || ws.Intensity != 0 || ws.OvercastInterp != 0 || ws.SkyScale != 1 {
		t.Fatalf("cleared: type %d intensity %.3f overcast %.3f sky %.2f", ws.Type, ws.Intensity, ws.OvercastInterp, ws.SkyScale)
	}
}

// Lightning needs a fully overcast sky and rain; thunder ends each 8 s cycle.
func TestWeatherThunderCycle(t *testing.T) {
	ws := NewWeatherSystem()
	ws.SetWeather(WeatherRain, true)
	claps := 0
	flashed := false
	for i := 0; i < 30*17; i++ {
		ws.Update(1.0/30, 12, testCam)
		if ws.LightningBrightness > 0 {
			flashed = true
		}
		if _, ok := ws.PopThunder(); ok {
			claps++
		}
	}
	if claps != 2 || !flashed {
		t.Fatalf("17 s of rain: %d thunder claps, flashed %v; want 2 and a flash", claps, flashed)
	}
	snow := NewWeatherSystem()
	snow.SetWeather(WeatherSnow, true)
	for i := 0; i < 30*17; i++ {
		snow.Update(1.0/30, 12, testCam)
		if _, ok := snow.PopThunder(); ok {
			t.Fatal("thunder in pure snow")
		}
	}
}
