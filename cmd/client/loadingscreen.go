package main

import (
	"image"
	"math"

	rl "github.com/gen2brain/raylib-go/raylib"
)

// loadingScreen holds textures and state for the in-game loading screen.
type loadingScreen struct {
	gearTex rl.Texture2D // procedural gear texture
	loaded  bool
}

// initLoadingScreen creates the procedural gear texture.
func initLoadingScreen() *loadingScreen {
	ls := &loadingScreen{}
	ls.gearTex = generateGearTexture(256, 12, 0.65, 0.85)
	ls.loaded = ls.gearTex.Width > 0
	return ls
}

// cleanupLoadingScreen frees GPU resources.
func (ls *loadingScreen) cleanup() {
	if ls.gearTex.Width > 0 {
		rl.UnloadTexture(ls.gearTex)
	}
}

// drawLoadingScreen renders the full loading screen (fully opaque).
func (c *Client) drawLoadingScreen(rotation float32) {
	c.drawLoadingScreenAlpha(rotation, 1.0)
}

// drawLoadingScreenAlpha renders the loading screen with the given alpha (0..1).
func (c *Client) drawLoadingScreenAlpha(rotation float32, alpha float32) {
	a := uint8(alpha * 255)
	if alpha >= 1.0 {
		rl.ClearBackground(rl.Black)
	} else {
		rl.DrawRectangle(0, 0, int32(screenWidth), int32(screenHeight), rl.Color{R: 0, G: 0, B: 0, A: a})
	}

	tint := rl.Color{R: 255, G: 255, B: 255, A: a}

	sw := float32(screenWidth)
	sh := float32(screenHeight)
	cx := sw / 2

	// Scale factor relative to reference 480p height
	scale := sh / 480.0
	if scale < 1.0 {
		scale = 1.0
	}

	// --- "EVERQUEST ONLINE" title from UI atlas (sheet 1) ---
	if c.viui != nil {
		if sheet, ok := c.viui.Sheet(1); ok {
			src := rl.Rectangle{X: 32, Y: 1, Width: 468, Height: 105}
			titleW := float32(420) * scale
			titleH := titleW * (105.0 / 468.0)
			titleX := cx - titleW/2
			titleY := sh * 0.08
			dst := rl.Rectangle{X: titleX, Y: titleY, Width: titleW, Height: titleH}
			rl.DrawTexturePro(sheet, src, dst, rl.Vector2{}, 0, tint)

			// --- "ADVENTURES" text (sheet 1, below ONLINE) ---
			advSrc := rl.Rectangle{X: 32, Y: 112, Width: 290, Height: 35}
			advW := float32(262) * scale
			advH := advW * (35.0 / 290.0)
			advX := cx - advW/2
			advY := titleY + titleH + 2*scale
			advDst := rl.Rectangle{X: advX, Y: advY, Width: advW, Height: advH}
			rl.DrawTexturePro(sheet, advSrc, advDst, rl.Vector2{}, 0, tint)

			// --- "FRONTIERS" logo (page 35, 512x64) ---
			if fSheet, fOK := c.viui.Sheet(35); fOK {
				fSrcH := float32(fSheet.Height) - 1
				fSrc := rl.Rectangle{X: 0, Y: 0, Width: float32(fSheet.Width), Height: fSrcH}
				fW := float32(260) * scale
				fH := fW * (fSrcH / float32(fSheet.Width))
				fX := cx - fW/2
				fY := advY + advH + 4*scale
				fDst := rl.Rectangle{X: fX, Y: fY, Width: fW, Height: fH}
				rl.DrawTexturePro(fSheet, fSrc, fDst, rl.Vector2{}, 0, tint)
			}
		}
	}

	// --- Spinning golden gear ---
	if c.loadScr != nil && c.loadScr.loaded {
		gearSize := float32(80) * scale
		gearX := cx
		gearY := sh * 0.55
		src := rl.Rectangle{X: 0, Y: 0, Width: float32(c.loadScr.gearTex.Width), Height: float32(c.loadScr.gearTex.Height)}
		origin := rl.Vector2{X: gearSize / 2, Y: gearSize / 2}
		dst := rl.Rectangle{X: gearX, Y: gearY, Width: gearSize, Height: gearSize}
		gearTint := rl.Color{R: 240, G: 200, B: 100, A: a}
		rl.DrawTexturePro(c.loadScr.gearTex, src, dst, origin, rotation, gearTint)
	}

	// --- "Loading, please wait..." text ---
	loadText := "Loading, please wait..."
	fontSize := int32(16 * scale)
	textW := c.measureText(loadText, fontSize)
	textX := int32(cx) - textW/2
	textY := int32(sh * 0.70)
	c.drawText(loadText, textX, textY, fontSize, rl.Color{R: 255, G: 255, B: 255, A: a})
}

// generateGearTexture creates a procedural gear/cog texture as an NRGBA image
// uploaded to the GPU. teeth = number of teeth, innerR/outerR = radius fractions (0-1).
func generateGearTexture(size, teeth int, innerR, outerR float64) rl.Texture2D {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	cx, cy := float64(size)/2, float64(size)/2
	maxR := float64(size) / 2

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx := float64(x) - cx + 0.5
			dy := float64(y) - cy + 0.5
			dist := math.Sqrt(dx*dx+dy*dy) / maxR
			angle := math.Atan2(dy, dx)

			toothAngle := angle * float64(teeth) / (2 * math.Pi)
			toothFrac := toothAngle - math.Floor(toothAngle)

			var edgeR float64
			if toothFrac < 0.5 {
				edgeR = outerR
			} else {
				edgeR = innerR
			}

			hubR := innerR * 0.55

			var alpha uint8
			if dist <= hubR {
				alpha = 255
			} else if dist <= edgeR {
				alpha = 255
			} else if dist <= edgeR+0.03 {
				frac := (dist - edgeR) / 0.03
				alpha = uint8((1 - frac) * 255)
			}

			holeR := hubR * 0.35
			if dist < holeR {
				alpha = 0
			} else if dist < holeR+0.03 {
				frac := (dist - holeR) / 0.03
				a2 := uint8(frac * 255)
				if a2 < alpha {
					alpha = a2
				}
			}

			off := y*img.Stride + x*4
			img.Pix[off+0] = 255
			img.Pix[off+1] = 255
			img.Pix[off+2] = 255
			img.Pix[off+3] = alpha
		}
	}

	rlImg := rl.NewImageFromImage(img)
	tex := rl.LoadTextureFromImage(rlImg)
	rl.UnloadImage(rlImg)
	rl.SetTextureFilter(tex, rl.FilterBilinear)
	return tex
}
