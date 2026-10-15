package main

import (
	"image"
	"log"
	"math"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

const fireFrameCount = 8
const smokeParticleCount = 20

// titleBGMTracks lists all music BGM tracks available in the ISO for the music player.
// Excludes narrator/vocal tracks (SS, SL, CS, CC, TC, GC, PW, RT, BL, CN, DC, EE, AK,
// AC, CD, RS, CLASS, ATTRIB, CHARREV, CHARNAME, EEGG) — those are voice-over narration.
var titleBGMTracks = []string{
	// Title
	"THEME.BGM",
	// City / zone BGMs
	"FREEPORT.BGM",
	"QEYNOS.BGM",
	"BARBARIA.BGM",
	"DARKELVE.BGM",
	"DWARVES.BGM",
	"ELVES.BGM",
	"ERUDITES.BGM",
	"GNOMES.BGM",
	"HALFLING.BGM",
	"OGRE.BGM",
	"TROLLS.BGM",
	// Frontiers zones
	"JUSTICE.BGM",
	"VALOR.BGM",
	"GUNTHAK.BGM",
	"INNOVATE.BGM",
	"EARTH.BGM",
	"TACTICS.BGM",
	"DISEASE.BGM",
	"NADOX.BGM",
	"THUNDER.BGM",
	"WAR.BGM",
	"TIME.BGM",
	"DECAY.BGM",
	"FIRE.BGM",
	"TRANQ.BGM",
	"STORMS.BGM",
	"KNOWL.BGM",
	"SOL_RO.BGM",
	"SKY.BGM",
	"TORMENT.BGM",
	"TORGIRAN.BGM",
	"DULAK.BGM",
	"HONOR.BGM",
	"NIGHTM.BGM",
	"DRUNDER.BGM",
	"WATER.BGM",
	// Battle BGMs
	"BATTLE01.BGM",
	"BATTLE02.BGM",
	"BATTLE03.BGM",
	"BATTLE04.BGM",
	"BATTLE05.BGM",
	"BATTLE06.BGM",
	"BATTLE07.BGM",
	"BATTLE08.BGM",
	"COMBAT_1.BGM",
	"COMBAT_2.BGM",
	// Frontiers battle/build BGMs
	"BATTLE1K.BGM",
	"BATTLE2K.BGM",
	"BATTLE3K.BGM",
	"BATTLE4K.BGM",
	"BATTLE5K.BGM",
	"BATTLE7K.BGM",
	"BATTLE8K.BGM",
	"BATTLE9K.BGM",
	"BUILD_2K.BGM",
	"BUILD3K_.BGM",
	"BUILD5K_.BGM",
	"BUILD6K_.BGM",
	"BUILD7K.BGM",
	"BUILD8K.BGM",
	"BUILD9K.BGM",
	"BUILD10K.BGM",
}

// titleScreen holds textures extracted from SETUP.ESF for the animated title
// screen shown before login.  The PS2 VISetup class composites sprite atlas
// overlays (birds, fire, ship, sky, vignettes) over a painted landscape.
type titleScreen struct {
	bgLeft, bgRight rl.Texture2D                 // 512x512 background textures (content in top-left 320x448)
	fireFrames      [fireFrameCount]rl.Texture2D // 8 individual flame frame textures
	fireLoaded      bool
	shipTex         rl.Texture2D // ship sprite extracted from atlas BL quadrant
	shipLoaded      bool
	smokeTex        rl.Texture2D // soft circle texture for smoke particles
	smokeLoaded     bool
	fireAtlas       rl.Texture2D // 128x128 fire/coast atlas (surface 6) — used for sparkle frames
	vignetteAtlas   rl.Texture2D // 128x128 vignettes atlas (surface 9)
	rockAtlas       rl.Texture2D // 64x64 rocks atlas (surface 5)
	loaded          bool
	startTime       float64 // rl.GetTime() when title screen started
}

// loadTitleScreen loads the title screen textures from a pre-opened SETUP.ESF.
func loadTitleScreen(f *esf.ObjFile) *titleScreen {
	ts := &titleScreen{}

	if err := f.BuildDictionary(); err != nil {
		log.Printf("loadTitleScreen: SETUP.ESF dict: %v", err)
		return ts
	}

	// Collect Surface objects by sequential index (same pattern as loadSpellIcons).
	allInfos := f.AllObjects()
	surfIdx := 0
	for _, info := range allInfos {
		if info.Type != esf.TypeSurface {
			continue
		}
		idx := surfIdx
		surfIdx++

		// SOE logo halves (1,2) — skip
		if idx == 1 || idx == 2 {
			continue
		}

		obj, err := f.GetObject(info)
		if err != nil {
			log.Printf("loadTitleScreen: surface %d load: %v", idx, err)
			continue
		}
		surf, ok := obj.(*esf.Surface)
		if !ok || surf == nil || surf.Image == nil {
			continue
		}

		img := surf.Image

		// Fire atlas: extract flame frames + keep full atlas for sparkles
		if idx == 6 {
			ts.extractFireFrames(img)
			rlFull := rl.NewImageFromImage(img)
			ts.fireAtlas = rl.LoadTextureFromImage(rlFull)
			rl.UnloadImage(rlFull)
			continue
		}

		// Ship atlas: extract ship sprite from BL quadrant bottom half
		if idx == 7 {
			ts.extractShip(img)
			continue
		}

		// Sky atlas: not currently used
		if idx == 8 {
			continue
		}

		rlImg := rl.NewImageFromImage(img)
		tex := rl.LoadTextureFromImage(rlImg)
		rl.UnloadImage(rlImg)
		rl.SetTextureFilter(tex, rl.FilterBilinear)

		switch idx {
		case 3:
			ts.bgLeft = tex
		case 4:
			ts.bgRight = tex
		case 5:
			ts.rockAtlas = tex
		case 9:
			ts.vignetteAtlas = tex
		default:
			rl.UnloadTexture(tex)
		}
	}

	ts.loaded = ts.bgLeft.Width > 0 && ts.bgRight.Width > 0
	if ts.loaded {
		ts.startTime = rl.GetTime()
		ts.initSmokeTex()
	}
	log.Printf("loadTitleScreen: loaded=%v, fireLoaded=%v (%d surfaces)", ts.loaded, ts.fireLoaded, surfIdx)
	return ts
}

// extractFireFrames crops 8 flame sprites from the bottom strip of the fire
// atlas and uploads each as a separate GPU texture.
// The atlas uses a 15px grid: 14px content + 1px separator column.
// Separators at x=14,29,44,59,74,89,104,119. Flames in y=100..125.
func (ts *titleScreen) extractFireFrames(atlas *image.NRGBA) {
	const cellStride = 15 // 14px content + 1px separator
	const frameW = 14     // content width (excludes separator)
	const frameY = 100    // top of flame strip
	const frameH = 26     // flame height (y=100..125)

	for i := 0; i < fireFrameCount; i++ {
		srcX := i * cellStride
		frame := image.NewNRGBA(image.Rect(0, 0, frameW, frameH))
		for y := 0; y < frameH; y++ {
			for x := 0; x < frameW; x++ {
				srcOff := (frameY+y)*atlas.Stride + (srcX+x)*4
				dstOff := y*frame.Stride + x*4
				copy(frame.Pix[dstOff:dstOff+4], atlas.Pix[srcOff:srcOff+4])
			}
		}
		rlImg := rl.NewImageFromImage(frame)
		ts.fireFrames[i] = rl.LoadTextureFromImage(rlImg)
		rl.UnloadImage(rlImg)
		rl.SetTextureFilter(ts.fireFrames[i], rl.FilterBilinear)
	}
	ts.fireLoaded = true
	log.Printf("extractFireFrames: %d frames (%dx%d each, stride %d)", fireFrameCount, frameW, frameH, cellStride)
}

// extractShip crops the ship sprite from the BL quadrant of the ship atlas.
// Ship is at atlas y=96..125, x=0..63 (bottom half of BL quadrant).
func (ts *titleScreen) extractShip(atlas *image.NRGBA) {
	const srcX, srcY = 0, 96
	const w, h = 53, 30
	ship := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sOff := (srcY+y)*atlas.Stride + (srcX+x)*4
			dOff := y*ship.Stride + x*4
			copy(ship.Pix[dOff:dOff+4], atlas.Pix[sOff:sOff+4])
		}
	}
	rlImg := rl.NewImageFromImage(ship)
	ts.shipTex = rl.LoadTextureFromImage(rlImg)
	rl.UnloadImage(rlImg)
	rl.SetTextureFilter(ts.shipTex, rl.FilterBilinear)
	ts.shipLoaded = true
	log.Printf("extractShip: %dx%d", w, h)
}

// initSmokeTex generates a soft circular texture for smoke particles.
func (ts *titleScreen) initSmokeTex() {
	const sz = 32
	img := image.NewNRGBA(image.Rect(0, 0, sz, sz))
	cx, cy := float64(sz)/2, float64(sz)/2
	for y := 0; y < sz; y++ {
		for x := 0; x < sz; x++ {
			dx := float64(x) - cx + 0.5
			dy := float64(y) - cy + 0.5
			dist := math.Sqrt(dx*dx+dy*dy) / (float64(sz) / 2)
			if dist > 1 {
				dist = 1
			}
			// Soft falloff
			a := uint8((1 - dist*dist) * 180)
			off := y*img.Stride + x*4
			img.Pix[off+0] = 180 // R — warm grey
			img.Pix[off+1] = 160 // G
			img.Pix[off+2] = 140 // B
			img.Pix[off+3] = a
		}
	}
	rlImg := rl.NewImageFromImage(img)
	ts.smokeTex = rl.LoadTextureFromImage(rlImg)
	rl.UnloadImage(rlImg)
	rl.SetTextureFilter(ts.smokeTex, rl.FilterBilinear)
	ts.smokeLoaded = true
}

// drawTitleScene draws the full animated title screen (background + overlays).
// If showPrompt is true, the "Press any key" text is shown.
func (ts *titleScreen) drawTitleScene(c *Client, showPrompt bool) {
	if !ts.loaded {
		return
	}

	ts.drawBG()

	elapsed := rl.GetTime() - ts.startTime

	// Volcano smoke: procedural rising particles
	if ts.smokeLoaded {
		const originX float32 = 224  // volcano mouth X
		const originY float32 = 162  // volcano mouth Y
		const lifespan float64 = 8.0 // seconds per particle
		src := rl.Rectangle{X: 0, Y: 0, Width: float32(ts.smokeTex.Width), Height: float32(ts.smokeTex.Height)}
		for i := 0; i < smokeParticleCount; i++ {
			// Stagger particles evenly across the lifespan
			phase := math.Mod(elapsed+float64(i)*lifespan/float64(smokeParticleCount), lifespan)
			t := phase / lifespan // 0..1 normalized age
			// Rise upward, drift left with slight sine wobble
			px := originX - float32(t*25) + float32(6*math.Sin(elapsed*0.8+float64(i)*1.7))
			py := originY - float32(t*60)
			// Grow as it rises
			size := float32(10 + t*15)
			// Fade out as it ages
			alpha := uint8((1 - t*t) * 100)
			tint := rl.Color{R: 180, G: 160, B: 140, A: alpha}
			dst := rl.Rectangle{X: px - size/2, Y: py - size/2, Width: size, Height: size}
			rl.DrawTexturePro(ts.smokeTex, src, dst, rl.Vector2{}, 0, tint)
		}
	}

	// Fire: 3 fire pits on lower middle-right of painting
	if ts.fireLoaded {
		fireFrame := int(math.Floor(elapsed*5)) % fireFrameCount
		tex := ts.fireFrames[fireFrame]
		src := rl.Rectangle{X: 0, Y: 0, Width: float32(tex.Width), Height: float32(tex.Height)}
		fires := [][2]float32{{395, 283}, {432, 310}, {517, 305}}
		for _, pos := range fires {
			dst := rl.Rectangle{X: pos[0], Y: pos[1], Width: 22, Height: 38}
			rl.DrawTexturePro(tex, src, dst, rl.Vector2{}, 0, rl.White)
		}
	}

	// Water sparkles: crosshair sprites from fire atlas (x=88, 3 frames ~29px tall each)
	// Additive blending so dark bg adds nothing, bright crosses glow
	if ts.fireAtlas.Width > 0 {
		sparkleOrder := [5]int{2, 1, 0, 1, 2}
		sparkleIdx := int(math.Floor(elapsed*2)) % 5
		sparkleFrame := sparkleOrder[sparkleIdx]
		rl.BeginBlendMode(rl.BlendAdditive)
		frameY := [3]float32{0, 29, 58}
		src := rl.Rectangle{X: 88, Y: frameY[sparkleFrame], Width: 40, Height: 28}
		tint := rl.Color{R: 255, G: 255, B: 255, A: 77} // 30% opacity
		type sparkleTemplate struct {
			spots [][2]float32
		}
		templates := [3]sparkleTemplate{
			{spots: [][2]float32{{205, 295}, {218, 320}, {210, 340}}},
			{spots: [][2]float32{{200, 305}, {222, 310}, {208, 330}, {215, 345}}},
			{spots: [][2]float32{{212, 290}, {203, 315}, {220, 335}}},
		}
		cycle := int(math.Floor(elapsed * 2 / 5))
		tmpl := templates[cycle%3]
		for _, pos := range tmpl.spots {
			dst := rl.Rectangle{X: pos[0], Y: pos[1], Width: 24, Height: 18}
			rl.DrawTexturePro(ts.fireAtlas, src, dst, rl.Vector2{}, 0, tint)
			rl.DrawTexturePro(ts.fireAtlas, src, dst, rl.Vector2{}, 0, tint)
			rl.DrawTexturePro(ts.fireAtlas, src, dst, rl.Vector2{}, 0, tint)
		}
		rl.EndBlendMode()
	}

	// Ship: sails right to x=335, pauses, then returns to start. Repeats.
	if ts.shipLoaded {
		srcW := float32(ts.shipTex.Width)
		srcH := float32(ts.shipTex.Height)
		src := rl.Rectangle{X: 0, Y: 0, Width: srcW, Height: srcH}

		const shipStartX float32 = 100
		const shipEndX float32 = 290
		const sailSpeed float64 = 20  // px/s sailing
		const pauseDur float64 = 2    // seconds floating at destination

		dist := float64(shipEndX - shipStartX)
		legTime := dist / sailSpeed             // time to sail one way
		cycleTime := legTime + pauseDur + legTime + pauseDur // full cycle
		t := math.Mod(elapsed, cycleTime)

		var shipX float32
		var flipH bool // true = sailing left (return trip)
		if t < legTime {
			// Sailing right
			shipX = shipStartX + float32(t*sailSpeed)
		} else if t < legTime+pauseDur {
			// Floating at destination
			shipX = shipEndX
		} else if t < legTime+pauseDur+legTime {
			// Sailing left (return)
			shipX = shipEndX - float32((t-legTime-pauseDur)*sailSpeed)
			flipH = true
		} else {
			// Floating at start
			shipX = shipStartX
		}

		dstW := float32(48)
		dstH := float32(22)
		if flipH {
			// Flip horizontally by using negative source width
			src.Width = -srcW
		}

		// Fade in near start, fade out near end
		const fadeInStart float32 = 140  // fully transparent at/before this X
		const fadeInEnd float32 = 150    // fully opaque at/after this X
		const fadeOutStart float32 = 250 // start fading out
		const fadeOutEnd float32 = 260   // fully transparent at/after this X
		var shipAlpha uint8 = 255
		if shipX < fadeInEnd {
			frac := (shipX - fadeInStart) / (fadeInEnd - fadeInStart)
			if frac < 0 {
				frac = 0
			}
			shipAlpha = uint8(frac * 255)
		} else if shipX > fadeOutStart {
			frac := 1.0 - (shipX-fadeOutStart)/(fadeOutEnd-fadeOutStart)
			if frac < 0 {
				frac = 0
			}
			shipAlpha = uint8(frac * 255)
		}
		shipTint := rl.Color{R: 255, G: 255, B: 255, A: shipAlpha}

		dst := rl.Rectangle{X: shipX, Y: 300, Width: dstW, Height: dstH}
		rl.DrawTexturePro(ts.shipTex, src, dst, rl.Vector2{}, 0, shipTint)
	}

	// Sky plane overlay: hovers gently between y=125 and y=130
	if ts.rockAtlas.Width > 0 {
		src := rl.Rectangle{X: 0, Y: 0, Width: float32(ts.rockAtlas.Width), Height: float32(ts.rockAtlas.Height)}
		hoverY := float32(117.5 + 2.5*math.Sin(elapsed*1.5))
		dst := rl.Rectangle{X: 470, Y: hoverY, Width: 64, Height: 64}
		rl.DrawTexturePro(ts.rockAtlas, src, dst, rl.Vector2{}, 0, rl.White)
	}

	// Worm creature: 12 frames in 2 cols × 6 rows, appears at 3 spots
	if ts.vignetteAtlas.Width > 0 {
		const wormFrames = 12
		const strideW float32 = 23
		const strideH float32 = 19
		const trimW float32 = 20
		const trimH float32 = 16
		wormSpots := [3][2]float32{{210, 240}, {155, 250}, {50, 270}}
		spotOrder := [4]int{0, 1, 2, 1} // 1→2→3→2 then repeat
		animDur := float64(wormFrames+1) / 6.0 // 12 frames + 1 blank at 6fps
		// Use per-spot pseudo-random hide durations (3-8s) based on spot index
		hideDurs := [4]float64{5.0, 3.5, 7.0, 4.5}
		// Walk through variable-length cycles to find current spot and local time
		var totalCycle float64
		for _, h := range hideDurs {
			totalCycle += animDur + h
		}
		t := math.Mod(elapsed, totalCycle)
		var spotIdx [2]float32
		var spotT float64
		acc := 0.0
		for si, h := range hideDurs {
			cd := animDur + h
			if t < acc+cd {
				spotIdx = wormSpots[spotOrder[si]]
				spotT = t - acc
				break
			}
			acc += cd
		}
		if spotT < animDur {
			wormFrame := int(math.Floor(spotT * 6)) // 6fps
			if wormFrame < wormFrames {
				col := float32(wormFrame % 2)
				row := float32(wormFrame / 2)
				wormSrc := rl.Rectangle{X: col*strideW + 1, Y: row*strideH + 1, Width: trimW, Height: trimH}
				wormDst := rl.Rectangle{X: spotIdx[0], Y: spotIdx[1], Width: 36, Height: 30}
				rl.DrawTexturePro(ts.vignetteAtlas, wormSrc, wormDst, rl.Vector2{}, 0, rl.White)
			}
		}
	}


	// "Press any key" — pulsing alpha (title screen only)
	if showPrompt {
		pulse := uint8(140 + int(80*math.Sin(elapsed*2.5)))
		promptColor := rl.Color{R: 200, G: 180, B: 120, A: pulse}
		prompt := "Press any key to continue"
		promptW := c.measureText(prompt, 20)
		c.drawText(prompt, (screenWidth-promptW)/2, screenHeight-50, 20, promptColor)
	}

	// Mute button (top-left)
	c.uiMuteButton(6, 6)
}

// drawBackground draws just the background (no overlays) — used behind the login form.
func (ts *titleScreen) drawBackground() {
	if !ts.loaded {
		return
	}
	ts.drawBG()
}

// drawBG renders the two background paintings to fill the 640x480 screen.
// Each 512x512 texture has 320x448 of actual painting content in its top-left;
// the remaining columns/rows are dark filler. Two 320px-wide halves = 640px.
func (ts *titleScreen) drawBG() {
	const contentW float32 = 320 // actual painting width in each texture
	const contentH float32 = 448 // actual painting height in each texture
	halfScreen := float32(screenWidth) / 2
	sh := float32(screenHeight)

	srcLeft := rl.Rectangle{X: 0, Y: 0, Width: contentW, Height: contentH}
	dstLeft := rl.Rectangle{X: 0, Y: 0, Width: halfScreen, Height: sh}
	rl.DrawTexturePro(ts.bgLeft, srcLeft, dstLeft, rl.Vector2{}, 0, rl.White)

	srcRight := rl.Rectangle{X: 0, Y: 0, Width: contentW, Height: contentH}
	dstRight := rl.Rectangle{X: halfScreen, Y: 0, Width: halfScreen, Height: sh}
	rl.DrawTexturePro(ts.bgRight, srcRight, dstRight, rl.Vector2{}, 0, rl.White)
}

// cleanup unloads all title screen GPU textures.
func (ts *titleScreen) cleanup() {
	if ts.bgLeft.Width > 0 {
		rl.UnloadTexture(ts.bgLeft)
	}
	if ts.bgRight.Width > 0 {
		rl.UnloadTexture(ts.bgRight)
	}
	for i := range ts.fireFrames {
		if ts.fireFrames[i].Width > 0 {
			rl.UnloadTexture(ts.fireFrames[i])
		}
	}
	if ts.shipTex.Width > 0 {
		rl.UnloadTexture(ts.shipTex)
	}
	if ts.smokeTex.Width > 0 {
		rl.UnloadTexture(ts.smokeTex)
	}
	if ts.fireAtlas.Width > 0 {
		rl.UnloadTexture(ts.fireAtlas)
	}
	if ts.vignetteAtlas.Width > 0 {
		rl.UnloadTexture(ts.vignetteAtlas)
	}
	if ts.rockAtlas.Width > 0 {
		rl.UnloadTexture(ts.rockAtlas)
	}
}

// anyKeyPressed returns true if any keyboard key, mouse button, or gamepad button was pressed this frame.
func anyKeyPressed() bool {
	if rl.GetKeyPressed() != 0 || rl.IsMouseButtonPressed(0) {
		return true
	}
	if gpAvailable() {
		for btn := int32(0); btn <= 17; btn++ {
			if rl.IsGamepadButtonPressed(gpID, btn) {
				return true
			}
		}
	}
	return false
}
