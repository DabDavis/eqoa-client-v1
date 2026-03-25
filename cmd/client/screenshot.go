package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	rl "github.com/gen2brain/raylib-go/raylib"
)

// screenshotFlash is the remaining time to show "Screenshot saved" overlay.
var screenshotFlash float32

// screenshotPending is set to true when the key is pressed; the actual capture
// happens on the NEXT frame after EndDrawing/SwapBuffers ensures the full
// frame (including render textures and menu overlays) is in the framebuffer.
var screenshotPending bool

// checkScreenshot checks for screenshot key press and draws the flash overlay.
// Call before rl.EndDrawing().
func (c *Client) checkScreenshot() {
	// Tick down flash overlay.
	if screenshotFlash > 0 {
		screenshotFlash -= rl.GetFrameTime()
		if screenshotFlash > 0 {
			alpha := uint8(255)
			if screenshotFlash < 0.5 {
				alpha = uint8(screenshotFlash / 0.5 * 255)
			}
			c.drawText("Screenshot saved", 10, int32(rl.GetScreenHeight())-30, 14,
				rl.Color{R: 0, G: 212, B: 212, A: alpha})
		}
	}

	// Capture from previous frame's request (framebuffer now has full content).
	if screenshotPending {
		screenshotPending = false
		c.saveScreenshot()
	}

	// PrintScreen or F9 — schedule capture for next frame.
	if rl.IsKeyPressed(rl.KeyPrintScreen) || rl.IsKeyPressed(rl.KeyF9) {
		screenshotPending = true
	}
}

func (c *Client) saveScreenshot() {
	screenshotDir := "screenshots"
	if exePath, err := os.Executable(); err == nil {
		screenshotDir = filepath.Join(filepath.Dir(exePath), "screenshots")
	}
	os.MkdirAll(screenshotDir, 0755)

	fileName := fmt.Sprintf("eqoa_%s.png", time.Now().Format("2006-01-02_15-04-05"))
	finalPath := filepath.Join(screenshotDir, fileName)
	// Read framebuffer after EndDrawing (SwapBuffers completed, all render textures composited).
	img := rl.LoadImageFromScreen()
	rl.ExportImage(*img, finalPath)
	rl.UnloadImage(img)

	log.Printf("Screenshot saved: %s", finalPath)
	screenshotFlash = 2.0
}
