package main

import (
	"math"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/eqoa/client/internal/ui"
)

const (
	minimapSize    = 120   // pixels on screen (square)
	minimapTexSize = 512   // render texture resolution for GPU capture
	minimapRadius  = 200.0 // world units visible radius
)

type minimapState struct {
	textures map[int]rl.Texture2D     // zoneIdx → captured top-down texture
	rt       rl.RenderTexture2D       // offscreen render target for GPU capture
	captured map[int]bool             // which zones have been captured
}

// captureMinimapTexture renders the terrain top-down into a texture using GPU.
func (c *Client) captureMinimapTexture(zoneIdx int) rl.Texture2D {
	if c.minimap.textures == nil {
		c.minimap.textures = make(map[int]rl.Texture2D)
		c.minimap.captured = make(map[int]bool)
	}
	if tex, ok := c.minimap.textures[zoneIdx]; ok {
		return tex
	}
	if c.minimap.captured[zoneIdx] {
		return rl.Texture2D{} // already tried, failed
	}
	c.minimap.captured[zoneIdx] = true

	if !c.terrainRender.IsZoneBuilt(zoneIdx) {
		return rl.Texture2D{} // meshes not uploaded yet
	}

	proxies := c.terrainLoader.GetZoneProxies()
	if proxies == nil || zoneIdx >= len(proxies.Zones) {
		return rl.Texture2D{}
	}
	zp := &proxies.Zones[zoneIdx]

	// Create render texture if needed
	if c.minimap.rt.ID == 0 {
		c.minimap.rt = rl.LoadRenderTexture(int32(minimapTexSize), int32(minimapTexSize))
	}

	// Zone center and extent
	cx := (zp.BBox.MinX + zp.BBox.MaxX) / 2
	cz := (zp.BBox.MinZ + zp.BBox.MaxZ) / 2
	zoneW := zp.BBox.MaxX - zp.BBox.MinX
	zoneH := zp.BBox.MaxZ - zp.BBox.MinZ
	extent := zoneW
	if zoneH > extent {
		extent = zoneH
	}
	halfExt := extent / 2

	// Orthographic top-down camera looking straight down
	cam := rl.Camera3D{
		Position: rl.Vector3{X: cx, Y: 500, Z: cz},
		Target:   rl.Vector3{X: cx, Y: 0, Z: cz},
		Up:       rl.Vector3{X: 0, Y: 0, Z: -1}, // -Z = up on screen = north at top
		Fovy:     halfExt * 2,                     // ortho extent
	}

	// Render into the RT
	rl.BeginTextureMode(c.minimap.rt)
	rl.ClearBackground(rl.Color{R: 20, G: 25, B: 15, A: 255})

	rl.BeginMode3D(cam)
	// Use orthographic projection
	proj := rl.MatrixOrtho(-halfExt, halfExt, -halfExt, halfExt, 0.1, 1000)
	rl.SetMatrixProjection(proj)

	// Draw terrain zone (no frustum cull — render everything)
	c.terrainRender.DrawZone(zoneIdx, nil, true, nil)

	rl.EndMode3D()
	rl.EndTextureMode()

	// Copy render texture to a standalone texture so we can reuse the RT
	rtImg := rl.LoadImageFromTexture(c.minimap.rt.Texture)
	rl.ImageFlipVertical(rtImg) // OpenGL RT is flipped
	tex := rl.LoadTextureFromImage(rtImg)
	rl.UnloadImage(rtImg)
	rl.SetTextureFilter(tex, rl.FilterBilinear)

	c.minimap.textures[zoneIdx] = tex
	return tex
}

func (c *Client) drawMinimap() {
	if c.engine == nil || c.terrainLoader == nil || c.terrainRender == nil {
		return
	}
	gs := c.engine.State
	px, pz := gs.Player.X, gs.Player.Z

	tex := c.captureMinimapTexture(c.terrainZoneIdx)
	if tex.ID == 0 {
		return
	}

	proxies := c.terrainLoader.GetZoneProxies()
	if proxies == nil || c.terrainZoneIdx >= len(proxies.Zones) {
		return
	}
	zp := &proxies.Zones[c.terrainZoneIdx]
	zoneW := zp.BBox.MaxX - zp.BBox.MinX
	zoneH := zp.BBox.MaxZ - zp.BBox.MinZ

	normX := (px - zp.BBox.MinX) / zoneW
	normZ := (pz - zp.BBox.MinZ) / zoneH

	// Source rect centered on player (already north-up from camera orientation)
	viewFrac := minimapRadius * 2 / zoneW
	srcW := float32(minimapTexSize) * viewFrac
	srcH := float32(minimapTexSize) * viewFrac
	srcX := normX*float32(minimapTexSize) - srcW/2
	srcY := normZ*float32(minimapTexSize) - srcH/2
	src := rl.Rectangle{X: srcX, Y: srcY, Width: srcW, Height: srcH}

	// Position: between target HUD and chat
	sw := float32(rl.GetScreenWidth())
	sh := float32(rl.GetScreenHeight())
	mapSz := float32(minimapSize)
	targetEnd := float32(200)
	chatStart := sw - sw*2/5 - 4
	drawX := (targetEnd + chatStart) / 2 - mapSz/2
	drawY := sh - mapSz - 4

	// Background
	rl.DrawRectangle(int32(drawX)-1, int32(drawY)-1, int32(mapSz)+2, int32(mapSz)+2,
		rl.Color{R: 0, G: 0, B: 0, A: 200})

	// Map texture
	dst := rl.Rectangle{X: drawX, Y: drawY, Width: mapSz, Height: mapSz}
	rl.DrawTexturePro(tex, src, dst, rl.Vector2{}, 0, rl.White)

	// Border (matches chat UI)
	if c.viui != nil && c.viui.OK() {
		c.viui.BlitFrame(drawX-1, drawY-1, mapSz+2, mapSz+2, ui.FrameLine, rl.White)
	} else {
		rl.DrawRectangleLines(int32(drawX)-1, int32(drawY)-1, int32(mapSz)+2, int32(mapSz)+2,
			rl.Color{R: 100, G: 200, B: 255, A: 150})
	}

	// Player dot
	cx := drawX + mapSz/2
	cy := drawY + mapSz/2
	rl.DrawCircle(int32(cx), int32(cy), 3, rl.Color{R: 255, G: 50, B: 50, A: 255})
	rl.DrawCircleLines(int32(cx), int32(cy), 3, rl.White)

	// Player facing indicator
	facing := gs.Player.Facing
	fLen := float32(8)
	fx := cx + float32(math.Sin(float64(facing)))*fLen
	fy := cy - float32(math.Cos(float64(facing)))*fLen
	rl.DrawLineEx(rl.Vector2{X: cx, Y: cy}, rl.Vector2{X: fx, Y: fy}, 2,
		rl.Color{R: 255, G: 100, B: 100, A: 200})

	// Entity dots
	r := mapSz / 2
	for _, ent := range gs.Entities {
		if ent.ObjectID == 0 {
			continue
		}
		dx := ent.X - px
		dz := ent.Z - pz
		dist := float32(math.Sqrt(float64(dx*dx + dz*dz)))
		if dist > minimapRadius || dist < 1 {
			continue
		}
		ex := cx + dx/minimapRadius*r
		ey := cy - dz/minimapRadius*r
		if ex < drawX+2 || ex > drawX+mapSz-2 || ey < drawY+2 || ey > drawY+mapSz-2 {
			continue
		}
		dotColor := rl.Yellow
		if ent.IsPlayer {
			dotColor = rl.Color{R: 0, G: 180, B: 255, A: 255}
		}
		rl.DrawCircle(int32(ex), int32(ey), 2, dotColor)
	}
}
