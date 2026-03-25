package main

import (
	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/eqoa/client/internal/ui"
)

// UI theme colors — shared palette used across all screens.
var (
	// Primary gold palette
	colorGold       = rl.Color{R: 200, G: 180, B: 100, A: 255} // main gold, menu headers, labels
	colorGoldWarm   = rl.Color{R: 200, G: 180, B: 120, A: 255} // warm gold, login/charsel/title
	colorGoldBright = rl.Color{R: 255, G: 220, B: 100, A: 255} // bright gold, highlights
	colorGoldHover  = rl.Color{R: 255, G: 230, B: 120, A: 255} // hover highlight
	colorGoldSemi   = rl.Color{R: 200, G: 180, B: 100, A: 220} // semi-transparent gold
	colorGoldDim    = rl.Color{R: 120, G: 110, B: 70, A: 255}  // dim gold, inactive tabs
	colorGoldTitle  = rl.Color{R: 220, G: 200, B: 80, A: 255}  // title text

	// Neutral text colors
	colorWhite    = rl.Color{R: 210, G: 210, B: 210, A: 255} // standard text
	colorWhiteFull = rl.Color{R: 220, G: 220, B: 220, A: 255} // brighter text
	colorLightGray = rl.Color{R: 200, G: 200, B: 200, A: 255} // light gray text
	colorDim      = rl.Color{R: 150, G: 150, B: 150, A: 255}  // dim/disabled text
	colorHintGray = rl.Color{R: 120, G: 120, B: 120, A: 255}  // hint text

	// Accent colors
	colorStatVal    = rl.Color{R: 180, G: 220, B: 255, A: 255} // stat values, blue tint
	colorPlayerName = rl.Color{R: 130, G: 180, B: 255, A: 255} // player names
	colorGoldenBdr  = rl.Color{R: 180, G: 140, B: 60, A: 255}  // golden border/accent

	// Panel and background colors
	colorPanelBg    = rl.Color{R: 20, G: 18, B: 30, A: 240}  // main panel background
	colorPopupBg    = rl.Color{R: 25, G: 22, B: 38, A: 245}  // popup background
	colorDarkPanel  = rl.Color{R: 0, G: 0, B: 10, A: 200}    // dark blue panel
	colorFieldBg    = rl.Color{R: 20, G: 20, B: 35, A: 255}  // input field background
	colorOverlay    = rl.Color{R: 0, G: 0, B: 0, A: 180}     // dark overlay
	colorTooltipBg  = rl.Color{R: 0, G: 0, B: 0, A: 210}     // tooltip background

	// Button and row colors
	colorBtnBg    = rl.Color{R: 40, G: 38, B: 55, A: 255}  // button background
	colorBtnHover = rl.Color{R: 60, G: 55, B: 75, A: 255}  // button hover
	colorRowBg1   = rl.Color{R: 30, G: 28, B: 42, A: 255}  // alternating row bg 1
	colorRowBg2   = rl.Color{R: 36, G: 34, B: 48, A: 255}  // alternating row bg 2
	colorRowHover = rl.Color{R: 50, G: 45, B: 65, A: 255}  // row hover highlight

	// Border and separator colors
	colorBorder    = rl.Color{R: 160, G: 140, B: 80, A: 255}  // panel border
	colorSep       = rl.Color{R: 80, G: 75, B: 95, A: 255}    // separator lines
	colorBtnBorder = rl.Color{R: 80, G: 80, B: 100, A: 200}   // button/popup border

	// GM menu specific
	colorGmHighlight = rl.Color{R: 40, G: 40, B: 60, A: 200}  // selected row bg
	colorGmLabel     = rl.Color{R: 140, G: 140, B: 160, A: 200} // muted labels
	colorGmFaint     = rl.Color{R: 100, G: 100, B: 120, A: 180} // faint detail text
	colorGmDimLabel  = rl.Color{R: 140, G: 140, B: 140, A: 180} // dim label text
	colorGmBtnBg     = rl.Color{R: 40, G: 40, B: 60, A: 255}   // gm button bg
	colorGmRedBg     = rl.Color{R: 70, G: 40, B: 40, A: 255}   // red hover bg
)

// clampArmor clamps armor set index to valid range (0-8), defaulting to 0.
func clampArmor(v byte) byte {
	if v > 8 {
		return 0
	}
	return v
}

// drawPanel draws a filled rectangle with a border.
func drawPanel(x, y, w, h int32, bg, border rl.Color) {
	rl.DrawRectangle(x, y, w, h, bg)
	rl.DrawRectangleLines(x, y, w, h, border)
}

// drawPanelTextured draws a 9-slice textured frame using UI.ESF textures,
// falling back to flat drawPanel() if VIUI is unavailable.
func (c *Client) drawPanelTextured(x, y, w, h int32, frame ui.UIFrameParam, fillTex int) {
	if c.viui != nil && c.viui.OK() {
		c.viui.BlitFillFrame(float32(x), float32(y), float32(w), float32(h), frame, fillTex, rl.White)
	} else {
		drawPanel(x, y, w, h, colorPanelBg, colorBorder)
	}
}

// drawCenteredText measures and draws text centered horizontally within the screen.
func (c *Client) drawCenteredText(text string, y, fontSize int32, color rl.Color) {
	w := c.measureText(text, fontSize)
	c.drawText(text, (screenWidth-w)/2, y, fontSize, color)
}
