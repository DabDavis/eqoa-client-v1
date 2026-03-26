package main

import (
	"fmt"

	rl "github.com/gen2brain/raylib-go/raylib"
)

// charCreateState holds UI state for the character creation wizard.
type charCreateState struct {
	active      bool
	step        int // 0=race/gender, 1=class, 2=appearance, 3=stats, 4=name
	race        int
	class       int
	gender      int // 0=male, 1=female
	humType     int  // 0=other, 1=eastern (Freeport), 2=western (Qeynos) — only for Human
	hairColor   int
	hairLength  int
	hairStyle   int
	faceOption  int
	name        string
	error       string // "Name is already taken" etc.
	selIdx      int    // highlighted item index within current step
	submitted   bool   // true after Create button pressed, waiting for server response
	lastCharCnt int    // character count when wizard was entered (detect refresh)
	stats       [7]int // stat allocation: STR,STA,AGI,DEX,WIS,INT,CHA
	statPoints  int    // remaining points to allocate
}

// Default base stats per race/class/humType — harvested from Sandstorm (eqoa.live).
// Key: {race, humType, class} → [7]int{STR, STA, AGI, DEX, WIS, INT, CHA}
var defaultBaseStats = map[raceClassKey2][7]int{
	{0,1,0}:{75,80,65,65,60,55,55}, {0,1,3}:{75,80,65,65,55,60,55}, {0,1,5}:{60,75,80,65,55,55,65},
	{0,1,6}:{65,65,75,80,55,55,60}, {0,1,10}:{55,65,75,60,65,80,55}, {0,1,11}:{65,75,55,65,60,80,55},
	{0,1,12}:{55,55,65,65,60,80,75}, {0,1,13}:{65,60,65,75,55,80,55}, {0,1,14}:{55,65,60,65,75,80,55},
	{0,2,0}:{75,80,65,65,60,55,55}, {0,2,1}:{65,65,75,80,60,55,55}, {0,2,2}:{75,80,65,65,60,55,55},
	{0,2,4}:{65,75,65,80,60,55,55}, {0,2,5}:{60,75,80,65,55,55,65}, {0,2,6}:{65,65,75,80,55,55,60},
	{0,2,7}:{65,55,55,75,80,60,65}, {0,2,9}:{65,65,60,55,80,55,75}, {0,2,10}:{55,65,75,60,65,80,55},
	{0,2,12}:{55,55,65,65,60,80,75}, {0,2,13}:{65,60,65,75,55,80,55}, {0,2,14}:{55,65,60,65,75,80,55},
	{1,0,1}:{60,60,70,85,65,55,60}, {1,0,2}:{70,75,60,70,65,55,60}, {1,0,5}:{55,70,75,70,60,55,70},
	{1,0,6}:{60,60,70,85,60,55,65}, {1,0,7}:{60,50,50,80,85,60,70}, {1,0,9}:{60,60,55,60,85,55,80},
	{1,0,10}:{50,60,70,65,70,80,60}, {1,0,12}:{50,50,60,70,65,80,80}, {1,0,13}:{60,55,60,80,60,80,60},
	{1,0,14}:{50,60,55,70,80,80,60},
	{2,0,0}:{70,80,60,70,60,65,50}, {2,0,3}:{70,80,60,70,55,70,50}, {2,0,6}:{60,65,70,85,55,65,55},
	{2,0,9}:{60,65,55,60,80,65,70}, {2,0,10}:{50,65,70,65,65,90,50}, {2,0,11}:{60,75,50,70,60,90,50},
	{2,0,12}:{50,55,60,70,60,90,70}, {2,0,13}:{60,60,60,80,55,90,50}, {2,0,14}:{50,65,55,70,75,90,50},
	{3,0,0}:{70,75,70,70,55,65,50}, {3,0,6}:{60,60,80,85,50,65,55}, {3,0,9}:{60,60,65,60,75,65,70},
	{3,0,10}:{50,60,80,65,60,90,50}, {3,0,11}:{60,70,60,70,55,90,50}, {3,0,12}:{50,50,70,70,55,90,70},
	{3,0,13}:{60,55,70,80,50,90,50}, {3,0,14}:{50,60,65,70,70,90,50},
	{4,0,0}:{80,85,60,65,65,50,50}, {4,0,2}:{80,85,60,65,65,50,50}, {4,0,6}:{70,70,70,80,60,50,55},
	{4,0,9}:{70,70,55,55,85,50,70},
	{5,0,0}:{90,90,60,60,55,50,50}, {5,0,3}:{90,90,60,60,50,55,50}, {5,0,8}:{70,85,60,55,75,50,60},
	{6,0,0}:{85,85,60,65,55,55,50}, {6,0,6}:{75,70,70,80,50,55,55}, {6,0,8}:{65,80,60,60,75,55,60},
	{7,0,0}:{70,85,70,75,55,50,50}, {7,0,6}:{60,70,80,90,50,50,55}, {7,0,7}:{60,60,60,85,75,55,60},
	{7,0,9}:{60,70,65,65,75,50,70},
	{8,0,2}:{70,75,60,65,65,70,50}, {8,0,3}:{70,75,60,65,60,75,50}, {8,0,9}:{60,60,55,55,85,70,70},
	{8,0,10}:{50,60,70,60,70,95,50}, {8,0,11}:{60,70,50,65,65,95,50}, {8,0,12}:{50,50,60,65,65,95,70},
	{8,0,13}:{60,55,60,75,60,95,50}, {8,0,14}:{50,60,55,65,80,95,50},
	{9,0,0}:{90,90,60,60,55,50,50}, {9,0,3}:{90,90,60,60,50,55,50}, {9,0,8}:{70,85,60,55,75,50,60},
	{9,0,11}:{80,85,50,60,55,75,50},
}

type raceClassKey2 struct{ race, humType, class int }

// charCreateBaseStats returns the base stats for the current creation combo.
func (c *Client) charCreateBaseStats() [7]int {
	ht := c.charCreate.humType
	if c.charCreate.race != 0 {
		ht = 0
	}
	return getBaseStats(c.charCreate.race, ht, c.charCreate.class)
}

// getBaseStats returns the base stats for a race/class/humType combo.
func getBaseStats(race, humType, class int) [7]int {
	if s, ok := defaultBaseStats[raceClassKey2{race, humType, class}]; ok {
		return s
	}
	return [7]int{60, 60, 60, 60, 60, 60, 60} // fallback
}

var statNames = [7]string{"STR", "STA", "AGI", "DEX", "WIS", "INT", "CHA"}

var statDescriptions = [7]string{
	"The might behind every swing. Determines the upper limit of your melee damage and the fury of your combat abilities.",
	"The will to endure. Governs your maximum hit points and how long you stand in battle.",
	"The art of not getting hit. Greatly improves your overall defense.",
	"Steady hands, deadly strikes. Raises your minimum melee damage. Sharpens the destructive force of Wizard and Magician nukes and area of effect spells.",
	"Ancient knowledge flows through you. Empowers undead nukes, Alchemist concoctions, and bow strikes. Subtly shapes certain spells.",
	"The depth of your arcane reserves. Determines your maximum power pool.",
	"Force of personality. Strengthens your healing and improves charm success.",
}

// raceClassKey encodes race + humType for the class restriction lookup.
// Non-humans use humType=0. Humans use 1 (East/Freeport) or 2 (West/Qeynos).
type raceClassKey struct {
	race    int
	humType int
}

// EQOA race-class restrictions (from defaultcharacter SQL table — 75 entries).
// WAR=0 RAN=1 PAL=2 SK=3 MNK=4 BRD=5 RGE=6 DRD=7 SHA=8 CL=9 MAG=10 NEC=11 ENC=12 WIZ=13 ALC=14
var raceClasses = map[raceClassKey][]int{
	{0, 1}: {0, 3, 5, 6, 9, 10, 11, 12, 13, 14},     // Human East (Freeport)
	{0, 2}: {0, 1, 2, 4, 5, 6, 7, 9, 10, 12, 13, 14}, // Human West (Qeynos)
	{1, 0}: {1, 2, 5, 6, 7, 9, 10, 12, 13, 14},       // Elf
	{2, 0}: {0, 3, 6, 9, 10, 11, 12, 13, 14},          // Dark Elf
	{3, 0}: {0, 6, 9, 10, 11, 12, 13, 14},             // Gnome
	{4, 0}: {0, 2, 6, 9},                               // Dwarf
	{5, 0}: {0, 3, 8},                                   // Troll
	{6, 0}: {0, 6, 8},                                   // Barbarian
	{7, 0}: {0, 6, 7, 9},                                // Halfling
	{8, 0}: {2, 3, 9, 10, 11, 12, 13, 14},              // Erudite
	{9, 0}: {0, 3, 8, 11},                               // Ogre
}

// classesForRace returns the valid classes for the current race+humType.
func (c *Client) classesForRace() []int {
	ht := 0
	if c.charCreate.race == 0 {
		ht = c.charCreate.humType
		if ht == 0 {
			ht = 1 // default to East if not set
		}
	}
	classes := raceClasses[raceClassKey{c.charCreate.race, ht}]
	if classes == nil {
		return []int{0} // fallback: Warrior
	}
	return classes
}

var ccRaceNames = []string{
	"Human", "Elf", "Dark Elf", "Gnome", "Dwarf",
	"Troll", "Barbarian", "Halfling", "Erudite", "Ogre",
}

var ccClassNames = []string{
	"Warrior", "Ranger", "Paladin", "Shadow Knight", "Monk",
	"Bard", "Rogue", "Druid", "Shaman", "Cleric",
	"Magician", "Necromancer", "Enchanter", "Wizard", "Alchemist",
}

// Model IDs per race+gender (mirrors server's getModelID).
// Index 0=male, 1=female. Uses int64 → int32 cast to handle values >0x7FFFFFFF.
var ccModelIDs = [10][2]int32{
	{int32(int64(1893243078)), int32(int64(223572789))},     // Human
	{int32(int64(-657100808)), int32(int64(-511108617))},    // Elf
	{int32(int64(2128125354)), int32(int64(-1935979719))},   // Dark Elf
	{int32(int64(-1449366763)), int32(int64(1279296764))},   // Gnome
	{int32(int64(-1436875705)), int32(int64(889978302))},    // Dwarf
	{int32(int64(1282385202)), int32(int64(1306732289))},    // Troll
	{int32(int64(1640644319)), int32(int64(-1001728746))},   // Barbarian
	{int32(int64(-1545912350)), int32(int64(-1977112783))},  // Halfling
	{int32(int64(839650119)), int32(int64(1327119550))},     // Erudite
	{int32(int64(-2071956336)), int32(int64(-1396700337))},  // Ogre
}

func createModelID(race, gender int) int32 {
	if race < 0 || race > 9 {
		race = 0
	}
	g := 0
	if gender != 0 {
		g = 1
	}
	return ccModelIDs[race][g]
}

// ccBackBtnRect returns the Back button rect (left side, bottom of panel).
func (c *Client) ccBackBtnRect() (x, y, w, h int32) {
	panelY := int32(50)
	panelH := screenHeight - panelY - 20
	btnW := (csListW - 20) / 2
	btnH := int32(30)
	btnX := csListX + 5
	btnY := panelY + panelH - 40
	return btnX, btnY, btnW, btnH
}

// ccNextBtnRect returns the Next button rect (right side, bottom of panel).
func (c *Client) ccNextBtnRect() (x, y, w, h int32) {
	panelY := int32(50)
	panelH := screenHeight - panelY - 20
	btnW := (csListW - 20) / 2
	btnH := int32(30)
	btnX := csListX + csListW - btnW - 5
	btnY := panelY + panelH - 40
	return btnX, btnY, btnW, btnH
}

// ccCreateBtnRect returns the Create button rectangle (right side, same row as Back).
func (c *Client) ccCreateBtnRect() (x, y, w, h int32) {
	return c.ccNextBtnRect()
}

// ccStepBack goes back one step, or exits the wizard from step 0.
func (c *Client) ccStepBack() {
	if c.charCreate.step == 0 {
		c.charCreate.active = false
	} else {
		c.charCreate.step--
		// Restore selIdx to the previously chosen value
		switch c.charCreate.step {
		case 0:
			c.charCreate.selIdx = c.charCreate.race
		case 1:
			// Find index of chosen class within the race's class list
			c.charCreate.selIdx = 0
			for i, cid := range c.classesForRace() {
				if cid == c.charCreate.class {
					c.charCreate.selIdx = i
					break
				}
			}
		case 2:
			c.charCreate.selIdx = 0
		case 3:
			c.charCreate.selIdx = 0
		}
	}
}

// ccStepNext advances to the next step, committing the current selection.
func (c *Client) ccStepNext() {
	switch c.charCreate.step {
	case 0:
		c.charCreate.race = c.charCreate.selIdx
		// Set default humType for humans
		if c.charCreate.race == 0 && c.charCreate.humType == 0 {
			c.charCreate.humType = 1 // default East
		}
		c.charCreate.step = 1
		c.charCreate.selIdx = 0
		c.charCreate.error = ""
	case 1:
		classes := c.classesForRace()
		if c.charCreate.selIdx < len(classes) {
			c.charCreate.class = classes[c.charCreate.selIdx]
		}
		c.charCreate.step = 2
		c.charCreate.selIdx = 0
		c.charCreate.error = ""
		// Initialize base stats from Sandstorm defaultcharacter table
		ht := c.charCreate.humType
		if c.charCreate.race != 0 {
			ht = 0
		}
		c.charCreate.stats = getBaseStats(c.charCreate.race, ht, c.charCreate.class)
		c.charCreate.statPoints = 20 // 20 training points to distribute
	case 2:
		c.charCreate.step = 3
		c.charCreate.selIdx = 0
		c.charCreate.error = ""
	case 3:
		c.charCreate.step = 4
		c.charCreate.selIdx = 0
		c.charCreate.error = ""
	}
}

// handleCharCreateInput dispatches to the current step's input handler.
func (c *Client) handleCharCreateInput() {
	// ESC or Triangle always goes back
	if rl.IsKeyPressed(rl.KeyEscape) || (gpAvailable() && gpPressed(rl.GamepadButtonRightFaceUp)) {
		c.ccStepBack()
		return
	}

	// Check Back/Next button clicks (all steps)
	if rl.IsMouseButtonPressed(rl.MouseLeftButton) {
		mx := int32(rl.GetMouseX())
		my := int32(rl.GetMouseY())
		bx, by, bw, bh := c.ccBackBtnRect()
		if mx >= bx && mx <= bx+bw && my >= by && my <= by+bh {
			c.ccStepBack()
			return
		}
		if c.charCreate.step < 4 {
			nx, ny, nw, nh := c.ccNextBtnRect()
			if mx >= nx && mx <= nx+nw && my >= ny && my <= ny+nh {
				c.ccStepNext()
				return
			}
		}
	}

	// Enter or Start advances (steps 0-3), or submits (step 4)
	gpStart := gpAvailable() && gpPressed(rl.GamepadButtonMiddleRight)
	if c.charCreate.step < 4 && (rl.IsKeyPressed(rl.KeyEnter) || gpStart) {
		c.ccStepNext()
		return
	}

	switch c.charCreate.step {
	case 0:
		c.handleCharCreateRace()
	case 1:
		c.handleCharCreateClass()
	case 2:
		c.handleCharCreateAppearance()
	case 3:
		c.handleCharCreateStats()
	case 4:
		c.handleCharCreateName()
	}
}

// handleCharCreateRace handles input for step 0: race and gender selection.
func (c *Client) handleCharCreateRace() {
	totalItems := 10 // 10 races
	cols := 2
	prevIdx := c.charCreate.selIdx

	// Arrow keys or D-pad navigate the grid
	if rl.IsKeyPressed(rl.KeyDown) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceDown)) {
		c.charCreate.selIdx = (c.charCreate.selIdx + cols) % totalItems
	}
	if rl.IsKeyPressed(rl.KeyUp) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceUp)) {
		c.charCreate.selIdx = (c.charCreate.selIdx - cols + totalItems) % totalItems
	}
	if rl.IsKeyPressed(rl.KeyRight) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceRight)) {
		c.charCreate.selIdx = (c.charCreate.selIdx + 1) % totalItems
	}
	if rl.IsKeyPressed(rl.KeyLeft) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceLeft)) {
		c.charCreate.selIdx = (c.charCreate.selIdx - 1 + totalItems) % totalItems
	}

	// Tab or L1/R1 toggles gender
	if rl.IsKeyPressed(rl.KeyTab) ||
		(gpAvailable() && (gpPressed(rl.GamepadButtonLeftTrigger1) || gpPressed(rl.GamepadButtonRightTrigger1))) {
		c.charCreate.gender = 1 - c.charCreate.gender
	}

	// Mouse clicks select race, toggle gender, or toggle city (no auto-advance)
	if rl.IsMouseButtonPressed(rl.MouseLeftButton) {
		mx := int32(rl.GetMouseX())
		my := int32(rl.GetMouseY())

		// Gender toggle buttons
		genderY := int32(86)
		maleX := csListX
		femaleX := csListX + 70
		btnW := int32(60)
		btnH := int32(22)
		if mx >= maleX && mx <= maleX+btnW && my >= genderY && my <= genderY+btnH {
			c.charCreate.gender = 0
		}
		if mx >= femaleX && mx <= femaleX+btnW && my >= genderY && my <= genderY+btnH {
			c.charCreate.gender = 1
		}

		// City toggle click handling moved to drawCharCreate (below model viewport)
		if false {
		}


		// Race buttons — select only
		startY := int32(114)
		colW := csListW / 2
		rowH := int32(26)
		for i := 0; i < totalItems; i++ {
			col := i % cols
			row := i / cols
			bx := csListX + int32(col)*colW
			by := startY + int32(row)*rowH
			if mx >= bx && mx <= bx+colW && my >= by && my <= by+rowH {
				c.charCreate.selIdx = i
				break
			}
		}
	}

	// Play race-specific BGM when highlighted race changes (PS2 VIWndCharCreateRace)
	if c.charCreate.selIdx != prevIdx {
		c.playCharSelectBGM(c.charCreate.selIdx)
	}
}

// handleCharCreateClass handles input for step 1: class selection.
func (c *Client) handleCharCreateClass() {
	classes := c.classesForRace()
	n := len(classes)

	cols := 2
	if rl.IsKeyPressed(rl.KeyDown) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceDown)) {
		c.charCreate.selIdx = (c.charCreate.selIdx + cols) % n
	}
	if rl.IsKeyPressed(rl.KeyUp) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceUp)) {
		c.charCreate.selIdx = (c.charCreate.selIdx - cols + n) % n
	}
	if rl.IsKeyPressed(rl.KeyRight) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceRight)) {
		c.charCreate.selIdx = (c.charCreate.selIdx + 1) % n
	}
	if rl.IsKeyPressed(rl.KeyLeft) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceLeft)) {
		c.charCreate.selIdx = (c.charCreate.selIdx - 1 + n) % n
	}

	// Mouse clicks — select only
	if rl.IsMouseButtonPressed(rl.MouseLeftButton) {
		mx := int32(rl.GetMouseX())
		my := int32(rl.GetMouseY())
		startY := int32(100)
		colW := csListW / 2
		rowH := int32(26)
		for i := 0; i < n; i++ {
			col := i % cols
			row := i / cols
			bx := csListX + int32(col)*colW
			by := startY + int32(row)*rowH
			if mx >= bx && mx <= bx+colW && my >= by && my <= by+rowH {
				c.charCreate.selIdx = i
				break
			}
		}
	}
}

// handleCharCreateAppearance handles input for step 2: appearance customization.
func (c *Client) handleCharCreateAppearance() {
	numOptions := 4 // 4 appearance options only
	if rl.IsKeyPressed(rl.KeyDown) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceDown)) {
		c.charCreate.selIdx = (c.charCreate.selIdx + 1) % numOptions
	}
	if rl.IsKeyPressed(rl.KeyUp) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceUp)) {
		c.charCreate.selIdx = (c.charCreate.selIdx - 1 + numOptions) % numOptions
	}

	// Left/Right to adjust the selected option
	adjustVal := func(val *int, delta, max int) {
		*val = (*val + delta + max) % max
	}

	leftPressed := rl.IsKeyPressed(rl.KeyLeft) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceLeft))
	rightPressed := rl.IsKeyPressed(rl.KeyRight) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceRight))
	if leftPressed || rightPressed {
		delta := 1
		if leftPressed {
			delta = -1
		}
		switch c.charCreate.selIdx {
		case 0:
			adjustVal(&c.charCreate.hairColor, delta, 8)
		case 1:
			adjustVal(&c.charCreate.hairLength, delta, 4)
		case 2:
			adjustVal(&c.charCreate.hairStyle, delta, 4)
		case 3:
			adjustVal(&c.charCreate.faceOption, delta, 4)
		}
	}

	// Mouse clicks on arrow buttons
	if rl.IsMouseButtonPressed(rl.MouseLeftButton) {
		mx := int32(rl.GetMouseX())
		my := int32(rl.GetMouseY())
		startY := int32(100)
		rowH := int32(26)
		arrowLeftX := csListX
		arrowRightX := csListX + csListW - 24
		arrowW := int32(24)

		for i := 0; i < numOptions; i++ {
			by := startY + int32(i)*rowH
			if my >= by && my <= by+rowH {
				var val *int
				max := 4
				switch i {
				case 0:
					val = &c.charCreate.hairColor
					max = 8
				case 1:
					val = &c.charCreate.hairLength
				case 2:
					val = &c.charCreate.hairStyle
				case 3:
					val = &c.charCreate.faceOption
				}
				if val != nil {
					if mx >= arrowLeftX && mx <= arrowLeftX+arrowW {
						adjustVal(val, -1, max)
					} else if mx >= arrowRightX && mx <= arrowRightX+arrowW {
						adjustVal(val, 1, max)
					}
				}
			}
		}
	}
}

// handleCharCreateName handles input for step 3: name entry.
// handleCharCreateStats handles input for step 3: stat allocation.
func (c *Client) handleCharCreateStats() {
	numStats := 7
	if rl.IsKeyPressed(rl.KeyDown) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceDown)) {
		c.charCreate.selIdx = (c.charCreate.selIdx + 1) % numStats
	}
	if rl.IsKeyPressed(rl.KeyUp) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceUp)) {
		c.charCreate.selIdx = (c.charCreate.selIdx - 1 + numStats) % numStats
	}

	leftPressed := rl.IsKeyPressed(rl.KeyLeft) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceLeft))
	rightPressed := rl.IsKeyPressed(rl.KeyRight) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceRight))
	if leftPressed || rightPressed {
		idx := c.charCreate.selIdx
		base := c.charCreateBaseStats()
		if rightPressed && c.charCreate.statPoints > 0 {
			c.charCreate.stats[idx]++
			c.charCreate.statPoints--
		} else if leftPressed && c.charCreate.stats[idx] > base[idx] {
			c.charCreate.stats[idx]--
			c.charCreate.statPoints++
		}
	}

	// Mouse clicks
	if rl.IsMouseButtonPressed(rl.MouseLeftButton) {
		mx := int32(rl.GetMouseX())
		my := int32(rl.GetMouseY())
		startY := int32(100)
		rowH := int32(28)
		arrowLeftX := csListX
		arrowRightX := csListX + csListW - 24
		arrowW := int32(24)
		base := c.charCreateBaseStats()

		for i := 0; i < numStats; i++ {
			by := startY + int32(i)*rowH
			if my >= by && my <= by+rowH {
				if mx >= arrowLeftX && mx <= arrowLeftX+arrowW {
					if c.charCreate.stats[i] > base[i] {
						c.charCreate.stats[i]--
						c.charCreate.statPoints++
					}
				} else if mx >= arrowRightX && mx <= arrowRightX+arrowW {
					if c.charCreate.statPoints > 0 {
						c.charCreate.stats[i]++
						c.charCreate.statPoints--
					}
				}
			}
		}
	}
}

func (c *Client) handleCharCreateName() {
	// Backspace
	if rl.IsKeyPressed(rl.KeyBackspace) && len(c.charCreate.name) > 0 {
		c.charCreate.name = c.charCreate.name[:len(c.charCreate.name)-1]
	}

	// Character input (letters only for character names)
	for ch := rl.GetCharPressed(); ch != 0; ch = rl.GetCharPressed() {
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') {
			if len(c.charCreate.name) < 15 {
				c.charCreate.name += string(rune(ch))
			}
		}
	}

	// Enter or Start: send creation request
	gpStart := gpAvailable() && gpPressed(rl.GamepadButtonMiddleRight)
	if (rl.IsKeyPressed(rl.KeyEnter) || gpStart) && len(c.charCreate.name) >= 3 {
		c.sendCreateCharacter()
		return
	}

	// Mouse click on Create button
	if rl.IsMouseButtonPressed(rl.MouseLeftButton) && len(c.charCreate.name) >= 3 {
		mx := int32(rl.GetMouseX())
		my := int32(rl.GetMouseY())
		btnX, btnY, btnW, btnH := c.ccCreateBtnRect()
		if mx >= btnX && mx <= btnX+btnW && my >= btnY && my <= btnY+btnH {
			c.sendCreateCharacter()
		}
	}
}

func (c *Client) sendCreateCharacter() {
	gs := c.engine.State
	// humType: 0 for non-humans, 1 or 2 for humans
	humType := 0
	if c.charCreate.race == 0 {
		humType = c.charCreate.humType
	}
	// Gnome: always send hairLength=3 (head hair + hat top visible)
	hairLength := c.charCreate.hairLength
	if c.charCreate.race == 3 {
		hairLength = 3
	}

	// Server expects TP deltas (points added), not total stat values.
	// stats[i] = base + allocated, so delta = stats[i] - base[i].
	ht := humType
	baseStats := getBaseStats(c.charCreate.race, ht, c.charCreate.class)
	var tpDeltas [7]int
	for i := 0; i < 7; i++ {
		tpDeltas[i] = c.charCreate.stats[i] - baseStats[i]
		if tpDeltas[i] < 0 {
			tpDeltas[i] = 0
		}
	}

	gs.CreateCharacter(
		c.charCreate.name,
		c.charCreate.race,
		c.charCreate.class,
		c.charCreate.gender,
		c.charCreate.hairColor,
		hairLength,
		c.charCreate.hairStyle,
		c.charCreate.faceOption,
		humType,
		tpDeltas,
	)
	c.charCreate.error = ""
	c.charCreate.submitted = true
}

// drawCharCreate renders the character creation wizard.
func (c *Client) drawCharCreate() {
	gold := colorGoldWarm
	dimGold := rl.Color{R: 140, G: 120, B: 80, A: 255}

	// Panel geometry (same as charselect)
	panelX := csListX - csPanelPad
	panelY := int32(50)
	panelW := csListW + csPanelPad*2
	panelH := screenHeight - panelY - 20

	// Dark panel
	rl.DrawRectangle(panelX, panelY, panelW, panelH, colorDarkPanel)

	// Title
	stepNames := []string{"Race & Gender", "Class", "Appearance", "Stats", "Name"}
	title := "Create Character"
	if c.charCreate.step < len(stepNames) {
		title = stepNames[c.charCreate.step]
	}
	c.drawTextInRect(title, csListX, panelY+8, csListW, 18, gold)

	// Step indicator — above Back/Next buttons
	_, btnY, _, _ := c.ccBackBtnRect()
	c.drawTextInRect(fmt.Sprintf("Step %d of 5", c.charCreate.step+1), csListX, btnY-16, csListW, 10, dimGold)

	// Dispatch to step-specific drawing
	switch c.charCreate.step {
	case 0:
		c.drawCharCreateRace()
	case 1:
		c.drawCharCreateClass()
	case 2:
		c.drawCharCreateAppearance()
	case 3:
		c.drawCharCreateStats()
	case 4:
		c.drawCharCreateName()
	}

	// City toggle for Humans (step 0) — drawn below model viewport
	if c.charCreate.step == 0 && c.charCreate.selIdx == 0 {
		modelX := csModelPanelX
		modelBottom := panelY + panelH

		cityY := modelBottom - 28 - 8

		// Map humType (1=East, 2=West) to 0-indexed radio
		cityIdx := c.charCreate.humType - 1
		if cityIdx < 0 {
			cityIdx = 0
		}
		if c.uiRadioGroup(modelX+5, cityY, "Starting City", []string{"Freeport", "Qeynos"}, &cityIdx) {
			c.charCreate.humType = cityIdx + 1
			c.playCharSelectBGM(0)
		}
	}

	// Back / Next buttons (all steps get Back; steps 0-3 get Next; step 4 gets Create)
	c.drawBackNextButtons()

	// Error message (if any)
	if c.charCreate.error != "" {
		errW := c.measureText(c.charCreate.error, 12)
		c.drawText(c.charCreate.error, csListX+(csListW-errW)/2, panelY+panelH-30, 12,
			rl.Color{R: 255, G: 80, B: 80, A: 255})
	}

	// ESC hint
	c.drawText("ESC - Back", 10, 10, 12, colorHintGray)
}

// drawBackNextButtons draws the Back and Next/Create buttons at the bottom of the wizard panel.
func (c *Client) drawBackNextButtons() {
	gold := colorGoldWarm
	mx := int32(rl.GetMouseX())
	my := int32(rl.GetMouseY())

	// Back button (always shown)
	bx, by, bw, bh := c.ccBackBtnRect()
	backLabel := "Back"
	if c.charCreate.step == 0 {
		backLabel = "Cancel"
	}
	backHover := mx >= bx && mx <= bx+bw && my >= by && my <= by+bh
	c.drawWizardBtn(bx, by, bw, bh, backLabel, true, backHover, gold)

	// Next or Create button
	nx, ny, nw, nh := c.ccNextBtnRect()
	if c.charCreate.step < 4 {
		nextHover := mx >= nx && mx <= nx+nw && my >= ny && my <= ny+nh
		c.drawWizardBtn(nx, ny, nw, nh, "Next", true, nextHover, gold)
	} else {
		enabled := len(c.charCreate.name) >= 3
		createHover := enabled && mx >= nx && mx <= nx+nw && my >= ny && my <= ny+nh
		c.drawWizardBtn(nx, ny, nw, nh, "Create", enabled, createHover, gold)
	}
}

// drawWizardBtn draws a styled button.
func (c *Client) drawWizardBtn(x, y, w, h int32, label string, enabled, hover bool, gold rl.Color) {
	bg := colorGmBtnBg
	border := rl.Color{R: 60, G: 60, B: 80, A: 255}
	textColor := rl.Color{R: 160, G: 150, B: 120, A: 255}
	if hover {
		bg = colorBtnHoverDk
		border = gold
		textColor = rl.Color{R: 255, G: 245, B: 180, A: 255}
	} else if enabled {
		textColor = gold
	}
	rl.DrawRectangle(x-1, y-1, w+2, h+2, border)
	rl.DrawRectangle(x, y, w, h, bg)
	c.drawTextInRect(label, x, y+7, w, 16, textColor)
}

// drawCharCreateRace draws step 0: race grid + gender toggle + city toggle for Human.
func (c *Client) drawCharCreateRace() {
	// Gender toggle
	genderY := int32(86)
	c.uiRadioGroup(csListX, genderY, "", []string{"Male", "Female"}, &c.charCreate.gender)
	tabHint := c.measureText("TAB=Gender", 10)
	c.drawText("TAB=Gender", csListX+csListW-tabHint, genderY+6, 10, colorHintGray)

	// Race grid (2 columns, 5 rows)
	startY := int32(114)
	colW := csListW / 2
	rowH := int32(26)
	cols := 2

	for i, name := range ccRaceNames {
		col := i % cols
		row := i / cols
		bx := csListX + int32(col)*colW
		by := startY + int32(row)*rowH

		selected := i == c.charCreate.selIdx
		textColor := rl.Color{R: 200, G: 190, B: 140, A: 255}
		if selected {
			rl.DrawRectangle(bx, by, colW-2, rowH-2, colorSelectBg)
			textColor = rl.Color{R: 255, G: 245, B: 180, A: 255}
		}
		c.drawText(name, bx+4, by+5, 14, textColor)
	}

	// Race description
	descY := startY + int32(5)*rowH + 8
	if c.charCreate.selIdx >= 0 && c.charCreate.selIdx < 10 {
		desc := raceDescriptions[c.charCreate.selIdx]
		c.drawWrappedText(desc, csListX, descY, csListW, 11, rl.Color{R: 170, G: 170, B: 190, A: 220})
	}

}

// drawCharCreateClass draws step 1: class list filtered by race.
func (c *Client) drawCharCreateClass() {
	dimGold := rl.Color{R: 140, G: 120, B: 80, A: 255}
	classes := c.classesForRace()

	// Race label
	raceName := ccRaceNames[c.charCreate.race]
	if c.charCreate.race == 0 {
		city := "Freeport"
		if c.charCreate.humType == 2 {
			city = "Qeynos"
		}
		raceName = fmt.Sprintf("%s (%s)", raceName, city)
	}
	c.drawText(raceName, csListX, int32(75), 12, dimGold)

	startY := int32(100)
	colW := csListW / 2
	rowH := int32(26)
	cols := 2

	for i, classID := range classes {
		col := i % cols
		row := i / cols
		bx := csListX + int32(col)*colW
		by := startY + int32(row)*rowH

		name := "Unknown"
		if classID >= 0 && classID < len(ccClassNames) {
			name = ccClassNames[classID]
		}

		selected := i == c.charCreate.selIdx
		textColor := rl.Color{R: 200, G: 190, B: 140, A: 255}
		if selected {
			rl.DrawRectangle(bx, by, colW-2, rowH-2, colorSelectBg)
			textColor = rl.Color{R: 255, G: 245, B: 180, A: 255}
		}
		c.drawText(name, bx+4, by+5, 14, textColor)
	}

	// Class description
	rows := (len(classes) + cols - 1) / cols
	descY := startY + int32(rows)*rowH + 8
	if c.charCreate.selIdx >= 0 && c.charCreate.selIdx < len(classes) {
		classID := classes[c.charCreate.selIdx]
		if classID >= 0 && classID < len(classDescriptions) {
			c.drawWrappedText(classDescriptions[classID], csListX, descY, csListW, 11,
				rl.Color{R: 170, G: 170, B: 190, A: 220})
		}
	}
}

// drawCharCreateAppearance draws step 2: hair/face options with arrows.
func (c *Client) drawCharCreateAppearance() {
	startY := int32(100)
	rowH := int32(30)

	options := []struct{ label string; value int }{
		{"Hair Color", c.charCreate.hairColor},
		{"Hair Length", c.charCreate.hairLength},
		{"Hair Style", c.charCreate.hairStyle},
		{"Face", c.charCreate.faceOption},
	}

	for i, opt := range options {
		by := startY + int32(i)*rowH
		selected := i == c.charCreate.selIdx
		textColor := rl.Color{R: 200, G: 190, B: 140, A: 255}
		if selected {
			rl.DrawRectangle(csListX, by, csListW, rowH-2, colorSelectBg)
			textColor = rl.Color{R: 255, G: 245, B: 180, A: 255}
		}
		arrowColor := colorHintGray
		if selected {
			arrowColor = colorGoldWarm
		}
		c.drawText("<", csListX+4, by+7, 14, arrowColor)
		text := fmt.Sprintf("%s: %d", opt.label, opt.value)
		textW := c.measureText(text, 14)
		c.drawText(text, csListX+(csListW-textW)/2, by+7, 14, textColor)
		c.drawText(">", csListX+csListW-18, by+7, 14, arrowColor)
	}
}

// drawCharCreateStats draws step 3: stat allocation with flavor text.
func (c *Client) drawCharCreateStats() {
	startY := int32(100)
	rowH := int32(28)
	base := c.charCreateBaseStats()

	for i := 0; i < 7; i++ {
		by := startY + int32(i)*rowH
		selected := i == c.charCreate.selIdx
		textColor := rl.Color{R: 200, G: 190, B: 140, A: 255}
		if selected {
			rl.DrawRectangle(csListX, by, csListW, rowH-2, colorSelectBg)
			textColor = rl.Color{R: 255, G: 245, B: 180, A: 255}
		}

		// Green if above base
		if c.charCreate.stats[i] > base[i] {
			textColor = rl.Color{R: 100, G: 255, B: 100, A: 255}
			if selected {
				textColor = rl.Color{R: 150, G: 255, B: 150, A: 255}
			}
		}

		arrowColor := colorHintGray
		if selected {
			arrowColor = colorGoldWarm
		}
		c.drawText("<", csListX+4, by+5, 14, arrowColor)
		text := fmt.Sprintf("%s: %d", statNames[i], c.charCreate.stats[i])
		textW := c.measureText(text, 14)
		c.drawText(text, csListX+(csListW-textW)/2, by+5, 14, textColor)
		c.drawText(">", csListX+csListW-18, by+5, 14, arrowColor)
	}

	// Points remaining
	ptY := startY + 7*rowH + 4
	ptColor := rl.Color{R: 200, G: 200, B: 140, A: 255}
	if c.charCreate.statPoints == 0 {
		ptColor = rl.Color{R: 140, G: 200, B: 140, A: 255}
	}
	ptText := fmt.Sprintf("Training Points: %d", c.charCreate.statPoints)
	c.drawText(ptText, csListX+4, ptY, 13, ptColor)

	// Flavor text for selected stat
	if c.charCreate.selIdx >= 0 && c.charCreate.selIdx < 7 {
		descY := ptY + 22
		c.drawWrappedText(statDescriptions[c.charCreate.selIdx], csListX, descY, csListW, 11,
			rl.Color{R: 170, G: 170, B: 190, A: 220})
	}
}

// drawCharCreateName draws step 3: name input field.
func (c *Client) drawCharCreateName() {
	// Name input field
	fieldY := int32(110)
	fieldH := int32(28)
	fieldBg := rl.Color{R: 20, G: 20, B: 35, A: 255}
	fieldBorder := rl.Color{R: 60, G: 60, B: 80, A: 255}

	c.drawText("Character Name:", csListX, fieldY-18, 12, rl.Color{R: 140, G: 120, B: 80, A: 255})

	rl.DrawRectangle(csListX-1, fieldY-1, csListW+2, fieldH+2, fieldBorder)
	rl.DrawRectangle(csListX, fieldY, csListW, fieldH, fieldBg)

	// Name text + cursor
	displayName := c.charCreate.name
	showCursor := int(rl.GetTime()*2)%2 == 0
	if showCursor {
		displayName += "_"
	}
	c.drawText(displayName, csListX+6, fieldY+6, 16, rl.Color{R: 255, G: 245, B: 180, A: 255})

	// Min length hint
	if len(c.charCreate.name) < 3 {
		c.drawText("(3-15 letters)", csListX, fieldY+fieldH+6, 10, colorHintGray)
	}
}
