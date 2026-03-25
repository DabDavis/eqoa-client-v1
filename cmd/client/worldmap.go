package main

import (
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"

	rl "github.com/gen2brain/raylib-go/raylib"
)

const mapDownloadURL = "https://texturepacks.art/eqoa/tunaria-map-hires.jpg"

// World map coordinate mapping (Tunaria)
// eqoa.live DZI grid: 14 cols x 17 rows, each 1024px = 2000 world units
// Image pixel = worldCoord * 1024 / 2000
// World range: X=0→28000, Z=0→34000
const (
	mapWorldMinX = float32(0)
	mapWorldMaxX = float32(28000)
	mapWorldMinZ = float32(0)
	mapWorldMaxZ = float32(34000)
)

func (c *Client) toggleWorldMap() {
	c.worldMapOpen = !c.worldMapOpen

	if c.worldMapOpen && c.worldMapTex.ID == 0 {
		path := filepath.Join(c.assetsDir, "tunaria-map-hires.jpg")

		if _, err := os.Stat(path); os.IsNotExist(err) {
			os.MkdirAll(filepath.Dir(path), 0755)
			log.Printf("World map: downloading from %s", mapDownloadURL)
			if err := downloadFile(path, mapDownloadURL); err != nil {
				log.Printf("World map: download failed: %v", err)
				c.worldMapOpen = false
				return
			}
			log.Printf("World map: saved to %s", path)
		}

		img := rl.LoadImage(path)
		if img.Width == 0 {
			c.worldMapOpen = false
			return
		}
		c.worldMapTex = rl.LoadTextureFromImage(img)
		rl.UnloadImage(img)
		rl.SetTextureFilter(c.worldMapTex, rl.FilterBilinear)

		// Center on player
		c.mapZoom = 1.0
		c.mapCenterOnPlayer()
	}

	if c.worldMapOpen {
		c.mapCenterOnPlayer()
	}
}

// mapCenterOnPlayer sets the map pan offset to center on the player.
func (c *Client) mapCenterOnPlayer() {
	gs := c.engine.State
	normX := (gs.Player.X - mapWorldMinX) / (mapWorldMaxX - mapWorldMinX)
	normZ := (gs.Player.Z - mapWorldMinZ) / (mapWorldMaxZ - mapWorldMinZ)
	c.mapPanX = normX
	c.mapPanY = normZ
}

func (c *Client) drawWorldMap() {
	if !c.worldMapOpen || c.worldMapTex.ID == 0 {
		return
	}

	sw := float32(rl.GetScreenWidth())
	sh := float32(rl.GetScreenHeight())

	mapW := float32(c.worldMapTex.Width)
	mapH := float32(c.worldMapTex.Height)

	// Handle zoom (mouse wheel or +/-)
	wheel := rl.GetMouseWheelMove()
	if wheel != 0 {
		c.mapZoom *= 1.0 + wheel*0.15
		if c.mapZoom < 0.5 {
			c.mapZoom = 0.5
		}
		if c.mapZoom > 8.0 {
			c.mapZoom = 8.0
		}
	}
	if rl.IsKeyDown(rl.KeyEqual) || rl.IsKeyDown(rl.KeyKpAdd) {
		c.mapZoom *= 1.02
		if c.mapZoom > 8.0 {
			c.mapZoom = 8.0
		}
	}
	if rl.IsKeyDown(rl.KeyMinus) || rl.IsKeyDown(rl.KeyKpSubtract) {
		c.mapZoom *= 0.98
		if c.mapZoom < 0.5 {
			c.mapZoom = 0.5
		}
	}

	// Handle pan (arrow keys or mouse drag)
	panSpeed := float32(0.005) / c.mapZoom
	if rl.IsKeyDown(rl.KeyLeft) {
		c.mapPanX -= panSpeed
	}
	if rl.IsKeyDown(rl.KeyRight) {
		c.mapPanX += panSpeed
	}
	if rl.IsKeyDown(rl.KeyUp) {
		c.mapPanY -= panSpeed
	}
	if rl.IsKeyDown(rl.KeyDown) {
		c.mapPanY += panSpeed
	}

	// Mouse drag
	if rl.IsMouseButtonDown(rl.MouseLeftButton) {
		delta := rl.GetMouseDelta()
		c.mapPanX -= delta.X / (sw * c.mapZoom)
		c.mapPanY -= delta.Y / (sh * c.mapZoom)
	}

	// Space: recenter on player
	if rl.IsKeyPressed(rl.KeySpace) {
		c.mapCenterOnPlayer()
	}

	// Compute base scale to fit map on screen
	baseScale := sw / mapW
	if mapH*baseScale > sh {
		baseScale = sh / mapH
	}
	scale := baseScale * c.mapZoom

	// Scaled map dimensions
	drawW := mapW * scale
	drawH := mapH * scale

	// Pan offset: mapPanX/Y is the normalized (0-1) point that should be at screen center
	drawX := sw/2 - c.mapPanX*drawW
	drawY := sh/2 - c.mapPanY*drawH

	// Dim background
	rl.DrawRectangle(0, 0, int32(sw), int32(sh), rl.Color{R: 0, G: 0, B: 0, A: 180})

	// Draw map
	srcRect := rl.Rectangle{X: 0, Y: 0, Width: mapW, Height: mapH}
	dstRect := rl.Rectangle{X: drawX, Y: drawY, Width: drawW, Height: drawH}
	rl.DrawTexturePro(c.worldMapTex, srcRect, dstRect, rl.Vector2{}, 0, rl.White)

	// Zone grid overlay from WorldZoneProxies
	if c.terrainLoader != nil {
		proxies := c.terrainLoader.GetZoneProxies()
		if proxies != nil {
			gridColor := rl.Color{R: 100, G: 200, B: 255, A: 40}
			currentColor := rl.Color{R: 0, G: 255, B: 200, A: 60}
			for i, zp := range proxies.Zones {
				// Zone proxy bbox → screen coords
				x1 := drawX + (zp.BBox.MinX-mapWorldMinX)/(mapWorldMaxX-mapWorldMinX)*drawW
				z1 := drawY + (zp.BBox.MinZ-mapWorldMinZ)/(mapWorldMaxZ-mapWorldMinZ)*drawH
				x2 := drawX + (zp.BBox.MaxX-mapWorldMinX)/(mapWorldMaxX-mapWorldMinX)*drawW
				z2 := drawY + (zp.BBox.MaxZ-mapWorldMinZ)/(mapWorldMaxZ-mapWorldMinZ)*drawH
				w := x2 - x1
				h := z2 - z1

				if i == c.terrainZoneIdx {
					// Current zone: highlighted fill
					rl.DrawRectangle(int32(x1), int32(z1), int32(w), int32(h), currentColor)
				}
				// Grid lines
				rl.DrawRectangleLines(int32(x1), int32(z1), int32(w), int32(h), gridColor)
			}
		}
	}

	// Town/city markers (coachman positions from PS2 data)
	type townMarker struct {
		name string
		x, z float32
	}
	towns := []townMarker{
		{"Freeport", 25274, 15719},
		{"Qeynos", 5300, 15200},
		{"Halas", 7800, 3400},
		{"Highpass", 16781, 15356},
		{"Moradhim", 15352, 9267},
		{"Rivervale", 18335, 11358},
		{"Neriak", 24925, 9429},
		{"Klick'Anon", 23464, 6489},
		{"Fayspires", 19482, 7044},
		{"Tethelin", 18426, 7495},
		{"Muniels", 23370, 19636},
		{"Fort Seriak", 21781, 8388},
		{"Grobb", 21600, 31200},
		{"Oggok", 7200, 29600},
		{"Bobble", 24641, 11865},
		{"Wyndhaven", 4711, 13202},
		{"Surefall Glade", 9136, 12737},
		{"Murnf", 8937, 10507},
		{"Forkwatch", 12528, 18761},
		{"Darvor Manor", 13603, 14763},
		{"Castle LightWolf", 9281, 6495},
		{"Highbourne", 4859, 21459},
	}

	townColor := rl.Color{R: 255, G: 220, B: 100, A: 230}
	for _, t := range towns {
		tx := drawX + (t.x-mapWorldMinX)/(mapWorldMaxX-mapWorldMinX)*drawW
		ty := drawY + (t.z-mapWorldMinZ)/(mapWorldMaxZ-mapWorldMinZ)*drawH
		// Diamond marker
		sz := int32(4)
		rl.DrawRectanglePro(
			rl.Rectangle{X: float32(tx), Y: float32(ty), Width: float32(sz * 2), Height: float32(sz * 2)},
			rl.Vector2{X: float32(sz), Y: float32(sz)}, 45, townColor)
		// Label
		tw := rl.MeasureText(t.name, 12)
		lx := int32(tx) - tw/2
		ly := int32(ty) - 14
		rl.DrawText(t.name, lx+1, ly+1, 12, rl.Color{R: 0, G: 0, B: 0, A: 200})
		rl.DrawText(t.name, lx, ly, 12, townColor)
	}

	// Player marker
	gs := c.engine.State
	px, pz := gs.Player.X, gs.Player.Z

	normX := (px - mapWorldMinX) / (mapWorldMaxX - mapWorldMinX)
	normZ := (pz - mapWorldMinZ) / (mapWorldMaxZ - mapWorldMinZ)

	markerX := drawX + normX*drawW
	markerY := drawY + normZ*drawH

	// Pulsing circle
	t := float32(math.Sin(float64(rl.GetTime()) * 4.0))
	radius := float32(5) + t*2
	rl.DrawCircle(int32(markerX), int32(markerY), radius+1, rl.Black)
	rl.DrawCircle(int32(markerX), int32(markerY), radius, rl.Color{R: 255, G: 50, B: 50, A: 255})
	rl.DrawCircleLines(int32(markerX), int32(markerY), radius+3, rl.White)

	// Label
	label := fmt.Sprintf("%s (%.0f, %.0f)", gs.Player.Name, px, pz)
	labelW := rl.MeasureText(label, 16)
	labelX := int32(markerX) - labelW/2
	labelY := int32(markerY) - 22
	rl.DrawText(label, labelX+1, labelY+1, 16, rl.Color{R: 0, G: 0, B: 0, A: 200})
	rl.DrawText(label, labelX, labelY, 16, rl.White)

	// Zoom indicator + hints
	zoomPct := int(c.mapZoom * 100)
	hint := fmt.Sprintf("Scroll=Zoom (%d%%)  Drag=Pan  Space=Center  M=Close", zoomPct)
	hintW := rl.MeasureText(hint, 14)
	rl.DrawText(hint, int32(sw)/2-hintW/2, int32(sh)-20, 14, rl.Color{R: 180, G: 180, B: 180, A: 200})
}

// downloadFile fetches a URL and saves it to disk.
func downloadFile(path, url string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}
