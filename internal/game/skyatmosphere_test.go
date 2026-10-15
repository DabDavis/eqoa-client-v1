package game

import (
	"testing"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/eqoa/client/internal/sky"
)

// The sky sees the transition's value on the frame it completes (1), and the
// texture it heads for: DarkClouds for weather, the region's own for clear.
// A storm's flashes reach it, with their patches.
func TestSkyAtmosphere(t *testing.T) {
	ws := NewWeatherSystem()
	cam := rl.Camera3D{Target: rl.Vector3{X: 1}, Up: rl.Vector3{Y: 1}}
	ws.SetWeather(WeatherHeavyRain, true)
	ws.Update(1.0/30, 23, cam)
	a := ws.SkyAtmosphere()
	if a.Transition != 1 || a.NextSky != sky.SkyTexDark || a.SunAlpha != 0 || a.NightFactor != 0 {
		t.Fatalf("after an instant storm: %+v", a)
	}
	ws.Update(1.0/30, 23, cam)
	if a = ws.SkyAtmosphere(); a.Transition != 0 {
		t.Fatalf("the frame after: transition %.2f, want 0", a.Transition)
	}
	flashes := 0
	for i := 0; i < 30*20; i++ {
		ws.Update(1.0/30, 23, cam)
		if a = ws.SkyAtmosphere(); a.Flash > 0 {
			flashes++
			if a.Patches[0][3] < 70 || a.Patches[0][3] > 90 || a.Patches[0][1] <= 0 {
				t.Fatalf("patch %v: want 70..90 across, above the horizon", a.Patches[0])
			}
		}
	}
	if flashes == 0 {
		t.Fatal("no flash in 20 s of heavy rain")
	}
	ws.SetWeather(WeatherClear, false)
	ws.Update(1.0/30, 23, cam)
	if a = ws.SkyAtmosphere(); a.NextSky != sky.SkyTexDefault || a.Transition <= 0 || a.Transition >= 1 {
		t.Fatalf("clearing: %+v", a)
	}
}
