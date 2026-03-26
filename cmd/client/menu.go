package main

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/eqoa/client/internal/game"
	"github.com/eqoa/client/internal/gfx"
	"github.com/eqoa/client/internal/models"
	"github.com/eqoa/client/internal/ui"
)

// --- Main Menu (F12) ---

// Menu layout constants.
const (
	menuW      = int32(480) // panel width
	menuH      = int32(420) // panel height
	menuPad    = int32(12) // inner padding
	menuRowH   = int32(22) // item row height
	menuHdrH   = int32(36) // header height
	menuTabH   = int32(28) // tab bar height
	menuTabW   = int32(120)
)

// menuXPos returns the current menu X position (centered on screen).
func menuXPos() int32 { return (screenWidth - menuW) / 2 }

// menuYPos returns the current menu Y position (centered on screen).
func menuYPos() int32 { return (screenHeight - menuH) / 2 }

func (c *Client) closeMenu() {
	c.menuOpen = false
	c.menuTab = 0
	c.menuRow = 0
	c.menuScroll = 0
}

func (c *Client) handleMenuInput() {
	gs := c.engine.State

	// Decrement input delay (set on tab switch, context menu open, etc.)
	if c.menuInputDelay > 0 {
		c.menuInputDelay--
	}

	// Block menu input while display confirmation is pending
	if c.displayConfirmPending {
		return
	}

	// Group tab text input mode — consume keyboard input for invite name field
	if c.menuTab == 4 && c.socialSubTab == 1 && c.groupTyping {
		c.handleGroupInviteInput()
		return
	}

	// Triangle: back / Start: close menu
	gpTriangle := gpAvailable() && gpPressed(rl.GamepadButtonRightFaceUp)
	gpStart := gpAvailable() && gpPressed(rl.GamepadButtonMiddleRight)
	kbEsc := rl.IsKeyPressed(rl.KeyEscape) || rl.IsKeyPressed(rl.KeyF12)

	dpadUp := rl.IsKeyPressed(rl.KeyUp) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceUp))
	dpadDown := rl.IsKeyPressed(rl.KeyDown) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceDown))
	dpadLeft := rl.IsKeyPressed(rl.KeyLeft) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceLeft))
	dpadRight := rl.IsKeyPressed(rl.KeyRight) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceRight))
	gpCross := gpAvailable() && gpPressed(rl.GamepadButtonRightFaceDown)

	// --- TP popup active: block menu input ---
	if c.tpPopupOpen {
		if gpStart {
			c.closeMenu()
			c.tpPopupOpen = false
			gs.TPAlloc = [7]int{}
		}
		return // popup handles its own input in drawTPPopup
	}

	// --- Spell popup active: capture all input for the popup ---
	if c.spellCtxSpellID >= 0 {
		if gpStart {
			c.closeMenu()
			c.spellCtxSpellID = -1
			c.spellSlotPicker = false
			c.spellInfoPopup = false
			return
		}
		if gpTriangle || kbEsc {
			if c.spellSlotPicker {
				c.spellSlotPicker = false
			} else if c.spellInfoPopup {
				c.spellInfoPopup = false
				c.spellCtxSpellID = -1
			} else {
				c.spellCtxSpellID = -1
			}
			return
		}
		if c.spellSlotPicker {
			// Left/right to navigate slots, Cross to confirm
			if dpadLeft {
				c.spellSlotIdx--
				if c.spellSlotIdx < 0 {
					c.spellSlotIdx = 9
				}
			}
			if dpadRight {
				c.spellSlotIdx++
				if c.spellSlotIdx > 9 {
					c.spellSlotIdx = 0
				}
			}
			if gpCross {
				idx := findSpellIndex(gs, c.spellCtxSpellID)
				if idx >= 0 {
					c.engine.SendAddSkillToBar(1, byte(idx), byte(c.spellSlotIdx))
					gs.SetHotbarSlot(c.spellSlotIdx, &gs.ParsedSpells[idx])
				}
				c.spellCtxSpellID = -1
				c.spellSlotPicker = false
			}
		} else if c.spellInfoPopup {
			// Cross dismisses info popup
			if gpCross {
				c.spellInfoPopup = false
				c.spellCtxSpellID = -1
			}
		} else {
			// Context menu: up/down to pick row, Cross to select
			if dpadUp || dpadDown {
				c.spellCtxRow = 1 - c.spellCtxRow // toggle 0↔1
			}
			if gpCross {
				if c.spellCtxRow == 0 {
					c.spellSlotPicker = true
					c.spellSlotIdx = 0
				} else {
					c.spellInfoPopup = true
				}
			}
		}
		return
	}

	if gpStart {
		c.closeMenu()
		return
	}
	if gpTriangle || kbEsc {
		if c.menuTab > 0 {
			c.menuTab = 0
			c.menuRow = 0
			c.menuScroll = 0
		} else {
			c.closeMenu()
		}
		return
	}

	// Controller: L2/R2 = main tabs, L1/R1 = sub-tabs, D-pad = content navigation.
	const numTabs = 8
	mainMenuItems := 10 // Resume, Inventory, Character, Nearby, Social, Guild, Settings, Music, Lore, Exit

	gpL2 := gpAvailable() && gpPressed(rl.GamepadButtonLeftTrigger2)
	gpR2 := gpAvailable() && gpPressed(rl.GamepadButtonRightTrigger2)
	gpL1 := gpAvailable() && gpPressed(rl.GamepadButtonLeftTrigger1)
	gpR1 := gpAvailable() && gpPressed(rl.GamepadButtonRightTrigger1)

	// L2/R2 triggers cycle main tabs.
	if gpL2 {
		c.menuTab--
		if c.menuTab < 0 {
			c.menuTab = numTabs - 1
		}
		c.menuRow = 0
		c.menuScroll = 0
	}
	if gpR2 {
		c.menuTab++
		if c.menuTab >= numTabs {
			c.menuTab = 0
		}
		c.menuRow = 0
		c.menuScroll = 0
	}

	// L1/R1 bumpers cycle sub-tabs.
	if gpL1 || gpR1 {
		dir := 1
		if gpL1 {
			dir = -1
		}
		switch c.menuTab {
		case 1: // Inventory: Equipped / Carrying / Bank
			c.invSubTab = (c.invSubTab + dir + 3) % 3
			c.menuScroll = 0
		case 2: // Character: Stats / Spells / CM
			c.charSubTab = (c.charSubTab + dir + 3) % 3
			c.menuScroll = 0
		case 4: // Social: Who / Group / LFG / Auction
			c.socialSubTab = (c.socialSubTab + dir + 4) % 4
			c.menuScroll = 0
		case 5: // Guild: Roster / Chat
			c.guildSubTab = (c.guildSubTab + dir + 2) % 2
			c.menuScroll = 0
		case 6: // Settings: Graphics / Camera / Audio
			c.settingsSubTab = (c.settingsSubTab + dir + 3) % 3
			c.menuScroll = 0
		}
	}

	// D-pad navigates content rows. Skip for Inventory (tab 1) — it has its own D-pad handler.
	if c.menuTab > 1 {
		if dpadUp {
			c.menuRow--
		}
		if dpadDown {
			c.menuRow++
		}
	}
	if c.menuTab == 0 {
		if dpadDown {
			c.menuRow++
		}
		if dpadUp {
			c.menuRow--
		}
	}

	// Clamp row for main menu tab
	if c.menuTab == 0 {
		if c.menuRow < 0 {
			c.menuRow = mainMenuItems - 1
		}
		if c.menuRow >= mainMenuItems {
			c.menuRow = 0
		}
	} else {
		// For other tabs, allow scrolling
		if c.menuRow < 0 {
			c.menuRow = 0
		}
	}

	// Cross selects
	if gpCross {
		if c.menuTab == 0 {
			switch c.menuRow {
			case 0: // Resume
				c.closeMenu()
			case 1: // Inventory
				c.menuTab = 1
				c.menuRow = 0
				c.menuScroll = 0
				c.menuInputDelay = 10
			case 2: // Character
				c.menuTab = 2
				c.menuRow = 0
				c.menuScroll = 0
			case 3: // Nearby
				c.menuTab = 3
				c.menuRow = 0
				c.menuScroll = 0
			case 4: // Social
				c.menuTab = 4
				c.menuRow = 0
				c.menuScroll = 0
			case 5: // Guild
				c.menuTab = 5
				c.menuRow = 0
				c.menuScroll = 0
			case 6: // Settings
				c.menuTab = 6
				c.menuRow = 0
				c.menuScroll = 0
				c.menuInputDelay = 3
			case 7: // Music
				c.menuTab = 7
				c.menuRow = 0
				c.menuScroll = 0
			case 8: // Lore
				c.menuTab = 8
				c.menuRow = 0
				c.menuScroll = 0
			case 9: // Exit Game
				c.closeMenu()
				c.disconnectAndReturnToLogin()
			}
			return
		}
		// Content tab Cross actions — handled by c.menuCrossAction()
		c.menuCrossAction(gs)
		return
	}

	// Mouse wheel scroll for equipment/inventory tabs
	wheel := rl.GetMouseWheelMove()
	if wheel != 0 && c.menuTab > 0 {
		c.menuScroll -= int(wheel * 3)
		if c.menuScroll < 0 {
			c.menuScroll = 0
		}
	}

	// D-pad auto-scroll: keep cursor visible within content area
	if c.menuTab > 0 && (dpadUp || dpadDown) {
		contentH := menuH - menuHdrH - menuTabH - 16
		visibleRows := int(contentH / menuRowH)
		if visibleRows < 1 {
			visibleRows = 1
		}
		if c.menuRow < 0 {
			c.menuRow = 0
			if c.menuScroll > 0 {
				c.menuScroll--
			}
		}
		if c.menuRow >= visibleRows {
			c.menuRow = visibleRows - 1
			c.menuScroll++
		}
	}

	// Mouse click handling
	if rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
		mx := rl.GetMouseX()
		my := rl.GetMouseY()

		// Tab bar clicks — must match scrolling wheel rendering logic
		tabY := menuYPos() + menuHdrH
		if my >= tabY && my < tabY+menuTabH {
			tabs := []string{"Menu", "Inventory", "Character", "Nearby", "Social", "Guild", "Settings", "Music", "Lore"}
			const tabGapC = int32(4)
			const arrowWC = int32(16)
			tabWidthsC := make([]int32, len(tabs))
			totalWC := int32(0)
			for i, label := range tabs {
				tabWidthsC[i] = c.measureText(label, 14) + 20
				if i > 0 {
					totalWC += tabGapC
				}
				totalWC += tabWidthsC[i]
			}
			availWC := menuW - 2*menuPad
			if totalWC <= availWC {
				// All tabs fit — simple linear layout
				tx := menuXPos() + menuPad
				for i := range tabs {
					tw := tabWidthsC[i]
					if mx >= tx && mx < tx+tw {
						c.menuTab = i
						c.menuRow = 0
						c.menuScroll = 0
						c.menuInputDelay = 3 // prevent click-through to content
						return
					}
					tx += tw + tabGapC
				}
			} else {
				// Scrolling wheel — compute visible range same as renderer
				innerWC := availWC - 2*arrowWC
				lo, hi := c.menuTab, c.menuTab
				usedWC := tabWidthsC[c.menuTab]
				for {
					expanded := false
					nextR := (hi + 1) % len(tabs)
					if nextR != lo {
						need := tabWidthsC[nextR] + tabGapC
						if usedWC+need <= innerWC {
							hi = nextR
							usedWC += need
							expanded = true
						}
					}
					nextL := (lo - 1 + len(tabs)) % len(tabs)
					if nextL != hi {
						need := tabWidthsC[nextL] + tabGapC
						if usedWC+need <= innerWC {
							lo = nextL
							usedWC += need
							expanded = true
						}
					}
					if !expanded {
						break
					}
				}

				// Check clicks on arrow areas to cycle tabs
				leftArrowX := menuXPos() + menuPad
				rightArrowX := menuXPos() + menuW - menuPad - arrowWC
				if mx >= leftArrowX && mx < leftArrowX+arrowWC {
					c.menuTab = (c.menuTab - 1 + len(tabs)) % len(tabs)
					c.menuRow = 0
					c.menuScroll = 0
					return
				}
				if mx >= rightArrowX && mx < rightArrowX+arrowWC {
					c.menuTab = (c.menuTab + 1) % len(tabs)
					c.menuRow = 0
					c.menuScroll = 0
					return
				}

				// Check clicks on visible tabs
				tx := menuXPos() + menuPad + arrowWC
				idx := lo
				for {
					tw := tabWidthsC[idx]
					if mx >= tx && mx < tx+tw {
						c.menuTab = idx
						c.menuRow = 0
						c.menuScroll = 0
						c.menuInputDelay = 3
						return
					}
					tx += tw + tabGapC
					if idx == hi {
						break
					}
					idx = (idx + 1) % len(tabs)
				}
			}
		}

		// Content area clicks (main tab only)
		contentY := menuYPos() + menuHdrH + menuTabH + 8
		if c.menuTab == 0 && mx >= menuXPos() && mx < menuXPos()+menuW && my >= contentY {
			row := int(my-contentY) / int(menuRowH+4)
			c.menuRow = row
			switch row {
			case 0: // Resume
				c.closeMenu()
			case 1: // Inventory
				c.menuTab = 1
				c.menuRow = 0
				c.menuScroll = 0
				c.menuInputDelay = 10
			case 2: // Character
				c.menuTab = 2
				c.menuRow = 0
				c.menuScroll = 0
			case 3: // Nearby
				c.menuTab = 3
				c.menuRow = 0
				c.menuScroll = 0
			case 4: // Social
				c.menuTab = 4
				c.menuRow = 0
				c.menuScroll = 0
			case 5: // Guild
				c.menuTab = 5
				c.menuRow = 0
				c.menuScroll = 0
			case 6: // Settings
				c.menuTab = 6
				c.menuRow = 0
				c.menuScroll = 0
				c.menuInputDelay = 3
			case 7: // Music
				c.menuTab = 7
				c.menuRow = 0
				c.menuScroll = 0
			case 8: // Lore
				c.menuTab = 8
				c.menuRow = 0
				c.menuScroll = 0
			case 9: // Exit Game
				c.closeMenu()
				c.disconnectAndReturnToLogin()
			}
			return
		}

		// Click outside menu closes it
		if mx < menuXPos() || mx > menuXPos()+menuW || my < menuYPos() || my > menuYPos()+menuH {
			c.closeMenu()
			return
		}
	}

	_ = gs
}

// menuCrossAction handles Cross/Enter on content tabs (tab > 0).
func (c *Client) menuCrossAction(gs *game.GameState) {
	if gs == nil {
		return
	}
	switch c.menuTab {
	case 2: // Character
		if c.charSubTab == 1 {
			// Spells: open context menu for selected spell
			spells := make([]game.ParsedSpell, len(gs.ParsedSpells))
			copy(spells, gs.ParsedSpells)
			sort.Slice(spells, func(i, j int) bool {
				return spells[i].AbilityLevel < spells[j].AbilityLevel
			})
			idx := c.menuRow + c.menuScroll
			if idx >= 0 && idx < len(spells) {
				c.spellCtxSpellID = spells[idx].SpellID
				c.spellCtxX = menuXPos() + menuW/2
				c.spellCtxY = menuYPos() + menuH/2
				c.spellCtxRow = 0
				c.spellSlotPicker = false
				c.spellInfoPopup = false
			}
		}
	case 4: // Social
		if c.socialSubTab == 2 && c.menuRow == 0 {
			// LFG: toggle registration
			c.engine.SendLFGToggle(!gs.LFGRegistered)
		}
	case 5: // Settings — all interactions handled by mouse click in drawSettingsGraphics
	case 6: // Music
		idx := c.menuRow + c.menuScroll
		if idx >= 0 && idx < len(titleBGMTracks) {
			track := titleBGMTracks[idx]
			c.audioSys.StopBGM()
			c.playBGMFromISO(track)
			c.musicPlayerOn = true
			c.musicTrackIdx = idx
		}
	}
}

func (c *Client) drawMenu() {
	// Dim background
	rl.DrawRectangle(0, 0, screenWidth, screenHeight, rl.Color{R: 0, G: 0, B: 0, A: 120})

	// Panel background
	c.drawPanelTextured(menuXPos(), menuYPos(), menuW, menuH, ui.FrameLine, ui.FillBlack)

	// Header
	c.drawCenteredText("Main Menu", menuYPos()+8, 22, colorGoldTitle)

	// Tab bar — scrolling wheel when tabs exceed panel width.
	tabY := menuYPos() + menuHdrH
	rl.DrawRectangle(menuXPos()+1, tabY, menuW-2, menuTabH, rl.Color{R: 30, G: 28, B: 40, A: 255})
	tabs := []string{"Menu", "Inventory", "Character", "Nearby", "Social", "Settings", "Music", "Lore"}
	if c.menuTab < 0 || c.menuTab >= len(tabs) {
		c.menuTab = 0
	}
	gold := colorGold
	dimGold := colorGoldDim

	// Measure all tab widths.
	const tabGap = int32(4)
	const arrowW = int32(16) // space reserved for < > arrows
	tabWidths := make([]int32, len(tabs))
	totalW := int32(0)
	for i, label := range tabs {
		tabWidths[i] = c.measureText(label, 14) + 20
		if i > 0 {
			totalW += tabGap
		}
		totalW += tabWidths[i]
	}

	availW := menuW - 2*menuPad
	if totalW <= availW {
		// All tabs fit — draw normally, no scrolling needed.
		tx := menuXPos() + menuPad
		for i, label := range tabs {
			tw := tabWidths[i]
			clr := dimGold
			if i == c.menuTab {
				clr = gold
				rl.DrawRectangle(tx, tabY+menuTabH-2, tw, 2, gold)
			}
			c.drawText(label, tx+10, tabY+6, 14, clr)
			tx += tw + tabGap
		}
	} else {
		// Scrolling wheel — find visible range centered on selected tab.
		// Reserve space for arrow indicators on each side.
		innerW := availW - 2*arrowW
		// Start from selected tab, expand outward to fill width.
		lo, hi := c.menuTab, c.menuTab
		usedW := tabWidths[c.menuTab]
		// Expand right, then left, alternating.
		for {
			expanded := false
			nextR := (hi + 1) % len(tabs)
			if nextR != lo {
				need := tabWidths[nextR] + tabGap
				if usedW+need <= innerW {
					hi = nextR
					usedW += need
					expanded = true
				}
			}
			nextL := (lo - 1 + len(tabs)) % len(tabs)
			if nextL != hi {
				need := tabWidths[nextL] + tabGap
				if usedW+need <= innerW {
					lo = nextL
					usedW += need
					expanded = true
				}
			}
			if !expanded {
				break
			}
		}

		// Draw left arrow if there are hidden tabs to the left.
		hasLeft := (lo-1+len(tabs))%len(tabs) != hi
		hasRight := (hi+1)%len(tabs) != lo
		arrowColor := rl.Color{R: 180, G: 160, B: 100, A: 255}
		if hasLeft {
			c.drawText("<", menuXPos()+menuPad, tabY+6, 14, arrowColor)
		}

		// Draw visible tabs.
		tx := menuXPos() + menuPad + arrowW
		idx := lo
		for {
			tw := tabWidths[idx]
			clr := dimGold
			if idx == c.menuTab {
				clr = gold
				rl.DrawRectangle(tx, tabY+menuTabH-2, tw, 2, gold)
			}
			c.drawText(tabs[idx], tx+10, tabY+6, 14, clr)
			tx += tw + tabGap
			if idx == hi {
				break
			}
			idx = (idx + 1) % len(tabs)
		}

		// Draw right arrow.
		if hasRight {
			c.drawText(">", menuXPos()+menuW-menuPad-arrowW+4, tabY+6, 14, arrowColor)
		}
	}

	// Content area
	contentY := menuYPos() + menuHdrH + menuTabH + 8
	contentH := menuH - menuHdrH - menuTabH - 16

	switch c.menuTab {
	case 0:
		c.drawMenuMain(contentY)
	case 1:
		c.drawMenuInventory(contentY, contentH)
	case 2:
		c.drawMenuCharacter(contentY, contentH)
	case 3:
		c.drawMenuNearby(contentY, contentH)
	case 4:
		c.drawMenuSocial(contentY, contentH)
	case 5:
		c.drawMenuGuild(contentY, contentH)
	case 6:
		c.drawMenuSettings(contentY, contentH)
	case 7:
		c.drawMenuMusic(contentY, contentH)
	case 8:
		c.drawMenuLore(contentY, contentH)
	}

	// Draw spell popup overlays on top of everything
	if c.spellCtxSpellID >= 0 {
		if c.spellSlotPicker {
			c.drawSpellSlotPicker()
		} else if c.spellInfoPopup {
			c.drawSpellInfoPopup()
		} else {
			c.drawSpellContextMenu()
		}
	}

	// Training points popup overlay
	if c.tpPopupOpen {
		c.drawTPPopup()
	}
}

func (c *Client) drawMenuMain(startY int32) {
	mx := rl.GetMouseX()
	my := rl.GetMouseY()
	items := []string{"Resume", "Inventory", "Character", "Nearby", "Social", "Guild", "Settings", "Music", "Lore", "Exit Game"}
	white := colorWhiteFull
	hover := colorGoldHover
	exitClr := rl.Color{R: 200, G: 100, B: 100, A: 255}
	exitHover := rl.Color{R: 255, G: 130, B: 130, A: 255}

	for i, label := range items {
		y := startY + int32(i)*(menuRowH+4)
		btnX := menuXPos() + menuPad
		btnW := menuW - menuPad*2
		isHover := mx >= btnX && mx < btnX+btnW && my >= y && my < y+menuRowH
		isSelected := c.menuRow == i

		// Button background
		bg := colorBtnBg
		if isSelected || isHover {
			bg = colorBtnHover
		}
		rl.DrawRectangle(btnX, y, btnW, menuRowH, bg)
		rl.DrawRectangleLines(btnX, y, btnW, menuRowH, colorSep)
		if isSelected {
			rl.DrawRectangleLines(btnX, y, btnW, menuRowH, colorGold)
		}

		clr := white
		if i == len(items)-1 {
			clr = exitClr
			if isHover || isSelected {
				clr = exitHover
			}
		} else if isHover || isSelected {
			clr = hover
		}
		labelW := c.measureText(label, 16)
		c.drawText(label, btnX+(btnW-labelW)/2, y+3, 16, clr)
	}
}

// equipPreviewW and equipPreviewH are the fixed render texture dimensions
// for the 3D equipment preview panel.
const (
	equipPreviewW = int32(200)
	equipPreviewH = int32(300)
)

// drawEquipPreview renders a slowly-rotating 3D character model preview
// into a RenderTexture2D and draws it at the given screen position.
// The model shows the player's current equipment state.
func (c *Client) drawEquipPreview(x, y, w, h int32) {
	gs := c.engine.State
	if gs == nil || c.modelMgr == nil {
		return
	}
	p := &gs.Player
	modelID := int32(p.ModelID)
	if modelID == 0 {
		return
	}

	// Allocate render texture once.
	if !c.equipPreviewInit {
		c.equipPreviewTex = rl.LoadRenderTexture(equipPreviewW, equipPreviewH)
		c.equipPreviewInit = true
	}

	dt := rl.GetFrameTime()
	c.equipPreviewRot += dt * 0.5

	// Build equipment state from current player.
	equip := entityEquipment(&gs.Player, c.modelMgr)
	c.applyTransmog(equip)

	// Per-race camera parameters (same as charselect).
	type raceCam struct {
		dist, eyeY, targetY float32
	}
	raceCams := [10]raceCam{
		{4.5, 2.0, 1.1}, // HUM
		{4.5, 2.0, 1.1}, // ELF
		{4.5, 2.0, 1.1}, // DELF
		{4.0, 1.6, 0.8}, // GNO
		{4.0, 1.6, 0.8}, // DWF
		{5.5, 2.8, 1.5}, // TRL
		{5.0, 2.4, 1.3}, // BAR
		{4.0, 1.6, 0.8}, // HLF
		{4.5, 2.0, 1.1}, // ERU
		{5.5, 2.8, 1.5}, // OGR
	}
	rc := raceCams[0]
	if p.Race >= 0 && int(p.Race) < len(raceCams) {
		rc = raceCams[p.Race]
	}

	// Camera orbits around origin at the preview rotation angle.
	facing := float32(math.Mod(float64(c.equipPreviewRot), 2*math.Pi))
	cosF := float32(math.Cos(float64(facing)))
	sinF := float32(math.Sin(float64(facing)))

	// Raise to center model higher in tall preview panel.
	yOffset := rc.targetY * -0.2
	cam := rl.Camera3D{
		Position:   rl.Vector3{X: sinF * rc.dist, Y: rc.eyeY + yOffset, Z: cosF * rc.dist},
		Target:     rl.Vector3{X: 0, Y: rc.targetY + yOffset, Z: 0},
		Up:         rl.Vector3{X: 0, Y: 1, Z: 0},
		Fovy:       30,
		Projection: rl.CameraPerspective,
	}

	// Render to texture.
	rl.BeginTextureMode(c.equipPreviewTex)
	rl.ClearBackground(rl.Color{R: 0, G: 0, B: 0, A: 0})
	gfx.ClearDepth()
	rl.BeginMode3D(cam)

	// Use a unique entity ID for animation state (won't collide with real entities).
	entityID := uint32(0xFFFD0000)
	c.modelMgr.DrawEntity(modelID, 0, 0, 0, 0, 1.0, equip, entityID, 0, 0, dt, false, 1.0)

	// Draw weapons if humanoid (with transmog overrides).
	if c.modelMgr.ModelIsHumanoid(modelID) {
		pri, sec, sh := c.applyTransmogWeapons(p.PrimaryWeapon, p.SecondaryWeapon, p.Shield)
		c.modelMgr.DrawWeapon(int32(pri), modelID, 0, 0, 0, 0, models.WeaponSlotPrimary)
		c.modelMgr.DrawWeapon(int32(sec), modelID, 0, 0, 0, 0, models.WeaponSlotSecondary)
		c.modelMgr.DrawWeapon(int32(sh), modelID, 0, 0, 0, 0, models.WeaponSlotShield)
	}

	rl.EndMode3D()
	rl.EndTextureMode()

	// Draw the render texture to screen. Render textures are Y-flipped.
	src := rl.Rectangle{
		X:      0,
		Y:      0,
		Width:  float32(equipPreviewW),
		Height: -float32(equipPreviewH), // flip Y
	}
	dst := rl.Rectangle{
		X:      float32(x),
		Y:      float32(y),
		Width:  float32(w),
		Height: float32(h),
	}
	rl.DrawTexturePro(c.equipPreviewTex.Texture, src, dst, rl.Vector2{}, 0, rl.White)
}

func (c *Client) drawMenuInventory(startY, contentH int32) {
	gs := c.engine.State
	if gs == nil {
		return
	}

	gold := colorGold
	dimGold := colorGoldDim
	white := rl.Color{R: 200, G: 200, B: 200, A: 255}
	dim := colorDim
	rowBg1 := colorRowBg1
	rowBg2 := colorRowBg2

	// Sub-tab bar: Equipped / Carrying / Bank
	subTabs := []string{"Equipped", "Carrying", "Bank"}
	stx := menuXPos() + menuPad
	mx, my := rl.GetMouseX(), rl.GetMouseY()
	subTabH := int32(22)
	for i, label := range subTabs {
		tw := c.measureText(label, 13) + 16
		clr := dimGold
		if i == c.invSubTab {
			clr = gold
			rl.DrawRectangle(stx, startY+subTabH-2, tw, 2, gold)
		}
		// Click handling
		if rl.IsMouseButtonPressed(rl.MouseButtonLeft) && mx >= stx && mx < stx+tw && my >= startY && my < startY+subTabH {
			c.invSubTab = i
			c.menuScroll = 0
		}
		c.drawText(label, stx+8, startY+4, 13, clr)
		stx += tw + 4
	}
	startY += subTabH + 4

	// Filter items based on sub-tab.
	// EquipLocation >= 0 = equipped, < 0 = carrying.
	var items []ParsedItem
	invCount := gs.InventoryItemCount
	for idx, item := range gs.ParsedItems {
		switch c.invSubTab {
		case 0: // Equipped
			if item.EquipLocation >= 0 {
				items = append(items, item)
			}
		case 1: // Carrying (inventory, not equipped)
			if item.EquipLocation < 0 && idx < invCount {
				items = append(items, item)
			}
		case 2: // Bank
			if idx >= invCount {
				items = append(items, item)
			}
		}
	}
	// Sort equipped tab by body position for consistent display.
	if c.invSubTab == 0 {
		sort.Slice(items, func(i, j int) bool {
			return items[i].EquipLocation < items[j].EquipLocation
		})
	}

	// Remaining content height after sub-tabs
	listH := contentH - subTabH - 4

	// Equipment preview: on Equipped sub-tab, show 3D model on the left.
	// The preview panel shifts the item list to the right.
	previewW := int32(0) // width consumed by preview panel (0 if hidden)
	if c.invSubTab == 0 {
		pvW := int32(160)
		pvH := listH
		pvX := menuXPos() + menuPad
		pvY := startY
		// Dark background behind preview
		rl.DrawRectangle(pvX, pvY, pvW, pvH, rl.Color{R: 15, G: 15, B: 25, A: 255})
		rl.DrawRectangleLines(pvX, pvY, pvW, pvH, rl.Color{R: 60, G: 55, B: 80, A: 255})
		c.drawEquipPreview(pvX+2, pvY+2, pvW-4, pvH-22)

		// Transmog toggle button at bottom of preview panel
		tmBtnY := pvY + pvH - 20
		tmBtnW := pvW - 4
		tmLabel := "Transmog"
		tmColor := rl.Color{R: 200, G: 180, B: 100, A: 220}
		if c.transmogOpen {
			tmColor = rl.Color{R: 255, G: 220, B: 100, A: 255}
		}
		tmHover := mx >= pvX+2 && mx < pvX+2+tmBtnW && my >= tmBtnY && my < tmBtnY+18
		tmBg := rl.Color{R: 30, G: 30, B: 45, A: 255}
		if tmHover {
			tmBg = rl.Color{R: 50, G: 50, B: 70, A: 255}
		}
		rl.DrawRectangle(pvX+2, tmBtnY, tmBtnW, 18, tmBg)
		tmW := c.measureText(tmLabel, 10)
		c.drawText(tmLabel, pvX+2+(tmBtnW-tmW)/2, tmBtnY+3, 10, tmColor)
		if tmHover && rl.IsMouseButtonPressed(rl.MouseLeftButton) {
			c.transmogOpen = !c.transmogOpen
		}

		previewW = pvW + 6 // panel width + gap
	}

	// Compute left edge and width for item list (shifted right by preview width).
	listLeftX := menuXPos() + menuPad + previewW
	listW := menuW - menuPad*2 - previewW

	// ── Equipped tab: grid layout or transmog panel ──────────────────
	if c.invSubTab == 0 {
		if c.transmogOpen {
			c.drawTransmogPanel(listLeftX, startY, listW, listH, mx, my)
		} else {
			c.drawEquippedGrid(items, listLeftX, startY, listW, listH, mx, my)
		}
		return
	}

	// ── Carrying / Bank tabs: list layout (unchanged) ──────────────────

	// Clamp scroll
	maxVisible := int(listH / menuRowH)
	maxScroll := len(items) - maxVisible
	if maxScroll < 0 {
		maxScroll = 0
	}
	if c.menuScroll > maxScroll {
		c.menuScroll = maxScroll
	}

	// Column headers
	hdrClr := colorBorder
	c.drawText("Item Name", listLeftX, startY, 12, hdrClr)
	c.drawText("Stk", listLeftX+listW/2-30, startY, 12, hdrClr)
	startY += menuRowH
	listH -= menuRowH

	if len(items) == 0 {
		msgs := [3]string{"No equipped items", "No items", "Bank is empty"}
		c.drawText(msgs[c.invSubTab], listLeftX, startY+4, 14, dim)
		return
	}

	// Input delay after tab switch (prevents A press from immediately opening context menu).
	if c.menuInputDelay > 0 {
		goto drawList
	}

	// Block list navigation when context menu is open.
	if c.itemContextOpen {
		// Context menu handles its own input in drawItemContextMenu.
		goto drawList
	}

	// Controller/keyboard navigation
	if rl.IsKeyPressed(rl.KeyUp) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceUp)) {
		if c.menuRow > 0 {
			c.menuRow--
		}
		if c.menuRow < c.menuScroll {
			c.menuScroll = c.menuRow
		}
	}
	if rl.IsKeyPressed(rl.KeyDown) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceDown)) {
		if c.menuRow < len(items)-1 {
			c.menuRow++
		}
		if c.menuRow >= c.menuScroll+maxVisible {
			c.menuScroll = c.menuRow - maxVisible + 1
		}
	}
	if c.menuRow >= len(items) {
		c.menuRow = len(items) - 1
	}
	if c.menuRow < 0 {
		c.menuRow = 0
	}

	// Cross/A or Enter: open context menu for selected item
	if !c.itemContextOpen {
		if rl.IsKeyPressed(rl.KeyEnter) || (gpAvailable() && gpPressed(rl.GamepadButtonRightFaceDown)) {
			if c.menuRow >= 0 && c.menuRow < len(items) {
				item := items[c.menuRow]
				c.itemContextOpen = true
				c.itemContextKey = item.Key
				c.itemContextInvSlot = item.InvSlot
				c.itemContextSlot = item.Slot
				c.itemContextTab = c.invSubTab
				c.itemContextRow = 0
				c.itemContextDelay = 10 // skip input for 10 frames (~167ms at 60fps)
				// Position popup near the selected row
				c.itemContextX = menuXPos() + menuW/2
				c.itemContextY = startY + int32(c.menuRow-c.menuScroll)*menuRowH
			}
		}
	}

drawList:
	for i := c.menuScroll; i < len(items); i++ {
		row := i - c.menuScroll
		y := startY + int32(row)*menuRowH
		if y+menuRowH > startY+listH {
			break
		}

		item := items[i]
		isSelected := i == c.menuRow
		rowW := listW / 2 // left half for carrying/bank (right half = detail)
		isHover := mx >= listLeftX && mx < listLeftX+rowW &&
			my >= y && my < y+menuRowH

		bg := rowBg1
		if row%2 == 1 {
			bg = rowBg2
		}
		if isSelected {
			bg = rl.Color{R: 50, G: 45, B: 70, A: 255}
		}
		if isHover {
			bg = colorRowHover
		}
		rl.DrawRectangle(listLeftX, y, rowW, menuRowH, bg)

		// Click to select
		if isHover && rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
			c.menuRow = i
		}

		// Item icon (16x16, left of name)
		const iconSz = float32(16)
		iconOff := int32(0)
		if tex, ok := c.getItemIcon(item.ItemIcon); ok {
			ix := float32(listLeftX + 4)
			iy := float32(y) + (float32(menuRowH)-iconSz)/2
			cropW := float32(tex.Width) * 0.75
			cropH := float32(tex.Height) * 0.70
			src := rl.Rectangle{Width: cropW, Height: cropH}
			dst := rl.Rectangle{X: ix, Y: iy, Width: iconSz, Height: iconSz}
			rl.DrawTexturePro(tex, src, dst, rl.Vector2{}, 0, rl.White)
			iconOff = 20 // shift name right past icon
		}

		name := item.Name
		maxNameLen := 16 // shorter for carrying/bank (detail panel on right)
		if len(name) > maxNameLen {
			name = name[:maxNameLen] + "..."
		}
		nameColor := white
		if isSelected {
			nameColor = colorGoldBright
		}
		c.drawText(name, listLeftX+4+iconOff, y+3, 14, nameColor)

		stack := fmt.Sprintf("%d", item.StackLeft)
		if item.StackLeft <= 1 {
			stack = ""
		}
		sw := c.measureText(stack, 14)
		c.drawText(stack, listLeftX+listW/2-4-sw, y+3, 14, dim)
	}

	// Item detail panel for Carrying/Bank tabs — right column auto-updates on scroll.
	if c.menuRow >= 0 && c.menuRow < len(items) {
		detailX := listLeftX + listW/2 + 4
		detailW := listW/2 - 4
		detailY := startY - menuRowH // align with column headers
		c.drawItemDetailColumn(items[c.menuRow], detailX, detailY, detailW, listH+menuRowH)
	}

	// Bottom: action hint + scroll indicator
	bottomY := startY + listH + 2
	c.drawText("Enter/A: Actions", listLeftX, bottomY, 11, dim)

	// Draw context menu popup on top of everything
	if c.itemContextOpen {
		c.drawItemContextMenu()
	}

	if len(items) > maxVisible {
		indicator := fmt.Sprintf("%d-%d of %d", c.menuScroll+1,
			min(c.menuScroll+maxVisible, len(items)), len(items))
		iw := c.measureText(indicator, 11)
		c.drawText(indicator, menuXPos()+menuW-menuPad-iw, bottomY, 11, dim)
	}
}

// equipGridSlot defines a slot in the equipment grid.
type equipGridSlot struct {
	slot  int64  // ParsedItem.Slot value
	label string // display label
}

// equipGridSlots defines equipment grid positions (2 rows of 7 + 2 rows of 8 = 15 slots).
// The slot field is the EquipLocation value (position on character body).
// equipGridSlots is no longer used — equipped items are shown as a flat indexed grid.

// drawEquippedGrid renders the equipment grid + inspection panel for the Equipped tab.
func (c *Client) drawEquippedGrid(items []ParsedItem, leftX, startY, areaW, areaH, mx, my int32) {
	dim := colorDim
	gold := colorGold

	n := len(items)
	if c.equipGridSel < 0 {
		c.equipGridSel = 0
	}
	if c.equipGridSel >= n {
		c.equipGridSel = n - 1
	}
	if c.equipGridSel < 0 {
		c.equipGridSel = 0
	}

	// Grid: auto-layout items in rows of 8.
	const gridCols = 8
	cellW := (areaW - 4) / gridCols
	if cellW > 26 {
		cellW = 26
	}
	cellH := int32(26)
	gridGap := int32(2)
	gridRowCount := (n + gridCols - 1) / gridCols
	if gridRowCount < 1 {
		gridRowCount = 1
	}
	gridH := int32(gridRowCount)*(cellH+gridGap) + 2

	// ── Input handling ──
	if c.menuInputDelay > 0 {
		// skip input this frame
	} else if !c.itemContextOpen && n > 0 {
		if rl.IsKeyPressed(rl.KeyLeft) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceLeft)) {
			if c.equipGridSel > 0 {
				c.equipGridSel--
			}
		}
		if rl.IsKeyPressed(rl.KeyRight) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceRight)) {
			if c.equipGridSel < n-1 {
				c.equipGridSel++
			}
		}
		if rl.IsKeyPressed(rl.KeyUp) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceUp)) {
			if c.equipGridSel >= gridCols {
				c.equipGridSel -= gridCols
			}
		}
		if rl.IsKeyPressed(rl.KeyDown) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceDown)) {
			if c.equipGridSel+gridCols < n {
				c.equipGridSel += gridCols
			}
		}

		// Cross/A or Enter: open context menu
		if rl.IsKeyPressed(rl.KeyEnter) || (gpAvailable() && gpPressed(rl.GamepadButtonRightFaceDown)) {
			if c.equipGridSel < n {
				item := items[c.equipGridSel]
				c.itemContextOpen = true
				c.itemContextKey = item.Key
				c.itemContextInvSlot = item.InvSlot
				c.itemContextSlot = item.Slot
				c.itemContextTab = 0
				c.itemContextRow = 0
				c.itemContextDelay = 10
				col := c.equipGridSel % gridCols
				row := c.equipGridSel / gridCols
				c.itemContextX = leftX + int32(col)*(cellW+gridGap) + cellW/2
				c.itemContextY = startY + int32(row)*(cellH+gridGap) + cellH
			}
		}
	}

	// ── Draw grid cells ──
	for i := 0; i < n; i++ {
		col := i % gridCols
		row := i / gridCols
		cx := leftX + int32(col)*(cellW+gridGap)
		cy := startY + int32(row)*(cellH+gridGap)

		item := items[i]
		isSelected := i == c.equipGridSel
		isHover := mx >= cx && mx < cx+cellW && my >= cy && my < cy+cellH

		if isHover && rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
			c.equipGridSel = i
		}
		if isHover && rl.IsMouseButtonPressed(rl.MouseButtonRight) {
			c.equipGridSel = i
			c.itemContextOpen = true
			c.itemContextKey = item.Key
			c.itemContextInvSlot = item.InvSlot
				c.itemContextSlot = item.Slot
			c.itemContextTab = 0
			c.itemContextRow = 0
			c.itemContextDelay = 5
			c.itemContextX = cx + cellW/2
			c.itemContextY = cy + cellH
		}

		bg := rl.Color{R: 25, G: 22, B: 38, A: 255}
		if isSelected {
			bg = rl.Color{R: 45, G: 40, B: 65, A: 255}
		}
		if isHover {
			bg = rl.Color{R: 55, G: 48, B: 75, A: 255}
		}
		rl.DrawRectangle(cx, cy, cellW, cellH, bg)

		borderClr := rl.Color{R: 50, G: 45, B: 70, A: 255}
		if isSelected {
			borderClr = gold
		}
		rl.DrawRectangleLines(cx, cy, cellW, cellH, borderClr)

		// Icon fills cell
		pad := float32(2)
		iconSz := float32(cellW) - pad*2
		iconX := float32(cx) + pad
		iconY := float32(cy) + pad
		if tex, ok := c.getItemIcon(item.ItemIcon); ok {
			cropW := float32(tex.Width) * 0.75
			cropH := float32(tex.Height) * 0.70
			src := rl.Rectangle{Width: cropW, Height: cropH}
			dst := rl.Rectangle{X: iconX, Y: iconY, Width: iconSz, Height: iconSz}
			rl.DrawTexturePro(tex, src, dst, rl.Vector2{}, 0, rl.White)
		} else {
			slotLabel := equipLocName(item.EquipLocation)
			if len(slotLabel) > 0 {
				letter := slotLabel[:1]
				lw := c.measureText(letter, 12)
				c.drawText(letter, cx+(cellW-lw)/2, cy+5, 12, rl.Color{R: 160, G: 150, B: 200, A: 255})
			}
		}
	}

	if n == 0 {
		c.drawText("No equipped items", leftX+4, startY+4, 13, dim)
	}

	// ── Inspection panel below grid ──
	inspY := startY + gridH + 6
	inspH := areaH - gridH - 6
	if inspH < 40 {
		inspH = 40
	}

	if c.equipGridSel >= 0 && c.equipGridSel < n {
		c.drawItemDetailColumn(game.ParsedItem(items[c.equipGridSel]), leftX, inspY, areaW, inspH)
	} else {
		rl.DrawRectangle(leftX, inspY, areaW, inspH, rl.Color{R: 15, G: 15, B: 25, A: 230})
		rl.DrawRectangleLines(leftX, inspY, areaW, inspH, rl.Color{R: 60, G: 55, B: 80, A: 255})
		c.drawText("Select an item", leftX+8, inspY+8, 13, dim)
	}

	bottomY := startY + areaH + 2
	c.drawText("Enter/A: Actions   Esc: Back", leftX, bottomY, 11, dim)

	if c.itemContextOpen {
		c.drawItemContextMenu()
	}
}

func (c *Client) drawMenuCharacter(startY, contentH int32) {
	gold := colorGold
	dimGold := colorGoldDim

	// Sub-tab bar: Stats / Spells / Class Mastery / Faction
	subTabs := []string{"Stats", "Spells", "Class Mastery", "Faction"}
	stx := menuXPos() + menuPad
	mx, my := rl.GetMouseX(), rl.GetMouseY()
	subTabH := int32(22)
	for i, label := range subTabs {
		tw := c.measureText(label, 13) + 16
		clr := dimGold
		if i == c.charSubTab {
			clr = gold
			rl.DrawRectangle(stx, startY+subTabH-2, tw, 2, gold)
		}
		if rl.IsMouseButtonPressed(rl.MouseButtonLeft) && mx >= stx && mx < stx+tw && my >= startY && my < startY+subTabH {
			c.charSubTab = i
			c.menuScroll = 0
		}
		c.drawText(label, stx+8, startY+4, 13, clr)
		stx += tw + 4
	}
	innerY := startY + subTabH + 4
	innerH := contentH - subTabH - 4

	switch c.charSubTab {
	case 0:
		c.drawMenuStats(innerY, innerH)
	case 1:
		c.drawMenuSpells(innerY, innerH)
	case 2:
		c.drawMenuCM(innerY, innerH)
	case 3:
		c.drawMenuFaction(innerY, innerH)
	}
}

func (c *Client) drawMenuStats(startY, contentH int32) {
	gs := c.engine.State
	if gs == nil {
		return
	}

	gold := colorGold
	white := colorWhite
	valClr := colorStatVal
	lineH := int32(18)
	colW := (menuW - menuPad*3) / 2

	y := startY
	p := &gs.Player

	// Character header
	charName := p.Name
	if charName == "" && gs.SelectedChar >= 0 && gs.SelectedChar < len(gs.Characters) {
		charName = gs.Characters[gs.SelectedChar].Name
	}
	charClass := ""
	charRace := ""
	if gs.SelectedChar >= 0 && gs.SelectedChar < len(gs.Characters) {
		ch := gs.Characters[gs.SelectedChar]
		charClass = className(ch.Class)
		charRace = raceName(ch.Race)
	}
	if charRace == "" {
		charRace = raceName(int(p.Race))
	}

	header := charName
	if charRace != "" || charClass != "" {
		header += "  -  "
		if charRace != "" {
			header += charRace + " "
		}
		header += charClass
	}
	c.drawText(header, menuXPos()+menuPad, y, 16, gold)
	y += lineH + 4

	lvl := int(p.Level)
	if lvl == 0 && gs.SelectedChar >= 0 && gs.SelectedChar < len(gs.Characters) {
		lvl = gs.Characters[gs.SelectedChar].Level
	}
	c.drawText(fmt.Sprintf("Level %d", lvl), menuXPos()+menuPad, y, 14, white)
	y += lineH

	// HP / Power
	c.drawText(fmt.Sprintf("HP: %d / %d", p.HPInt, p.MaxHPInt), menuXPos()+menuPad, y, 14, white)
	c.drawText(fmt.Sprintf("Power: %d / %d", p.PowerInt, p.MaxPowerInt), menuXPos()+menuPad+colW+menuPad, y, 14, white)
	y += lineH

	// XP / Speed
	c.drawText(fmt.Sprintf("XP: %d", p.XPRaw), menuXPos()+menuPad, y, 14, white)
	if p.Speed > 0 {
		c.drawText(fmt.Sprintf("Speed: %.1f", p.Speed), menuXPos()+menuPad+colW+menuPad, y, 14, white)
	}
	y += lineH

	// AC
	c.drawText(fmt.Sprintf("AC: %d", p.AC), menuXPos()+menuPad, y, 14, white)
	y += lineH + 6

	// Separator
	rl.DrawLine(menuXPos()+menuPad, y, menuXPos()+menuW-menuPad, y, colorSep)
	y += 6

	// Base Stats
	c.drawText("Attributes", menuXPos()+menuPad, y, 14, gold)
	y += lineH

	drawStat := func(label string, val int32, col int) {
		x := menuXPos() + menuPad + int32(col)*(colW+menuPad)
		c.drawText(fmt.Sprintf("%s: %d", label, val), x, y, 13, valClr)
	}

	drawStat("STR", p.Str, 0)
	drawStat("STA", p.Sta, 1)
	y += lineH
	drawStat("AGI", p.Agi, 0)
	drawStat("DEX", p.Dex, 1)
	y += lineH
	drawStat("WIS", p.Wis, 0)
	drawStat("INT", p.Int, 1)
	y += lineH
	drawStat("CHA", p.Cha, 0)
	y += lineH + 6

	// Separator
	rl.DrawLine(menuXPos()+menuPad, y, menuXPos()+menuW-menuPad, y, colorSep)
	y += 6

	// Resists
	c.drawText("Resists", menuXPos()+menuPad, y, 14, gold)
	y += lineH

	drawStat("Fire", p.FireR, 0)
	drawStat("Lightning", p.LightningR, 1)
	y += lineH
	drawStat("Cold", p.ColdR, 0)
	drawStat("Arcane", p.ArcaneR, 1)
	y += lineH
	drawStat("Poison", p.PoisonR, 0)
	drawStat("Disease", p.DiseaseR, 1)
	y += lineH + 6

	// Training Points — compact one-liner with Spend button
	if gs.TrainingPoints > 0 {
		rl.DrawLine(menuXPos()+menuPad, y, menuXPos()+menuW-menuPad, y, colorSep)
		y += 6

		c.drawText(fmt.Sprintf("Training Points: %d", gs.TrainingPoints), menuXPos()+menuPad, y+2, 14, gold)

		// "Spend" button
		mx, my := rl.GetMouseX(), rl.GetMouseY()
		btnW := int32(60)
		btnH := int32(20)
		btnX := menuXPos() + menuW - menuPad - btnW
		green := rl.Color{R: 80, G: 200, B: 80, A: 255}
		rl.DrawRectangle(btnX, y, btnW, btnH, rl.Color{R: 40, G: 80, B: 40, A: 255})
		rl.DrawRectangleLines(btnX, y, btnW, btnH, green)
		c.drawText("Spend", btnX+12, y+3, 13, green)
		if rl.IsMouseButtonPressed(rl.MouseButtonLeft) && mx >= btnX && mx < btnX+btnW && my >= y && my < y+btnH {
			c.tpPopupOpen = true
		}
	}
}

func (c *Client) drawMenuSpells(startY, contentH int32) {
	gs := c.engine.State
	if gs == nil {
		return
	}

	gold := colorGold
	white := colorWhite
	dim := colorDim
	cyan := rl.Color{R: 100, G: 200, B: 220, A: 255}
	lineH := int32(18)

	y := startY

	// Sort spells by AbilityLevel (level learned)
	spells := make([]game.ParsedSpell, len(gs.ParsedSpells))
	copy(spells, gs.ParsedSpells)
	sort.Slice(spells, func(i, j int) bool {
		return spells[i].AbilityLevel < spells[j].AbilityLevel
	})

	// Header row: "Spells (N)" on left, "[Clear Bar]" on right
	c.drawText(fmt.Sprintf("Spells (%d)", len(spells)), menuXPos()+menuPad, y, 14, gold)

	clearLabel := "[Clear Bar]"
	clw := c.measureText(clearLabel, 13)
	clx := menuXPos() + menuW - menuPad - clw
	mx, my := rl.GetMouseX(), rl.GetMouseY()
	isClearHover := mx >= clx && mx < clx+clw && my >= y && my < y+lineH
	clrClear := dim
	if isClearHover {
		clrClear = gold
	}
	c.drawText(clearLabel, clx, y, 13, clrClear)

	// Handle [Clear Bar] click (only when no popup is active)
	if isClearHover && rl.IsMouseButtonPressed(rl.MouseButtonLeft) && c.spellCtxSpellID < 0 {
		c.engine.SendClearSpellBar()
		for i := 0; i < 10; i++ {
			gs.SetHotbarSlot(i, nil)
		}
	}

	y += lineH

	if len(spells) == 0 {
		c.drawText("No spells", menuXPos()+menuPad, y, 13, dim)
		return
	}

	// Scrollable list
	listTop := y
	listH := contentH - (y - startY) - lineH
	maxVisible := int(listH / menuRowH)

	maxScroll := len(spells) - maxVisible
	if maxScroll < 0 {
		maxScroll = 0
	}
	if c.menuScroll > maxScroll {
		c.menuScroll = maxScroll
	}

	rowBg1 := colorRowBg1
	rowBg2 := colorRowBg2
	hoverBg := colorRowHover

	for i := c.menuScroll; i < len(spells); i++ {
		row := i - c.menuScroll
		ry := listTop + int32(row)*menuRowH
		if ry+menuRowH > listTop+listH {
			break
		}

		rowX := menuXPos() + menuPad
		rowW := menuW - menuPad*2
		isRowHover := mx >= rowX && mx < rowX+rowW && my >= ry && my < ry+menuRowH

		isSelected := c.menuRow == row
		bg := rowBg1
		if row%2 == 1 {
			bg = rowBg2
		}
		if (isRowHover || isSelected) && c.spellCtxSpellID < 0 {
			bg = hoverBg
		}
		rl.DrawRectangle(rowX, ry, rowW, menuRowH, bg)
		if isSelected && c.spellCtxSpellID < 0 {
			rl.DrawRectangleLines(rowX, ry, rowW, menuRowH, colorGold)
		}

		sp := spells[i]

		// Spell icon (16x16, left of name)
		const iconSz = float32(16)
		iconOff := int32(0)
		if tex, ok := c.getSpellIcon(sp.Icon); ok {
			ix := float32(rowX + 4)
			iy := float32(ry) + (float32(menuRowH)-iconSz)/2
			src := rl.Rectangle{Width: float32(tex.Width), Height: float32(tex.Height)}
			dst := rl.Rectangle{X: ix, Y: iy, Width: iconSz, Height: iconSz}
			rl.DrawTexturePro(tex, src, dst, rl.Vector2{}, 0, rl.White)
			iconOff = int32(iconSz) + 4
		}

		name := sp.Name
		if len(name) > 26 {
			name = name[:26] + "..."
		}
		c.drawText(name, rowX+4+iconOff, ry+3, 13, white)

		// Hotbar indicator: show *N for spells on hotbar
		nameW := c.measureText(name, 13)
		nameW += iconOff
		if sp.OnHotBar != 0 && sp.WhereOnHotBar >= 0 && sp.WhereOnHotBar < 10 {
			// Map slot to key label: slots 0-8 → "1"-"9", slot 9 → "0"
			key := sp.WhereOnHotBar + 1
			if key == 10 {
				key = 0
			}
			barLabel := fmt.Sprintf("*%d", key)
			c.drawText(barLabel, rowX+4+nameW+6, ry+3, 13, cyan)
		}

		// Right side: spell level
		info := fmt.Sprintf("Lv %d", sp.AbilityLevel)
		iw := c.measureText(info, 13)
		c.drawText(info, menuXPos()+menuW-menuPad-4-iw, ry+3, 13, dim)

		// Click detection: open context menu (only when no popup active)
		if isRowHover && rl.IsMouseButtonPressed(rl.MouseButtonLeft) && c.spellCtxSpellID < 0 {
			c.spellCtxSpellID = sp.SpellID
			c.spellCtxX = mx
			c.spellCtxY = my
			c.spellSlotPicker = false
			c.spellInfoPopup = false
		}
	}

	if len(spells) > maxVisible {
		indicator := fmt.Sprintf("%d-%d of %d", c.menuScroll+1,
			min(c.menuScroll+maxVisible, len(spells)), len(spells))
		iw := c.measureText(indicator, 11)
		c.drawText(indicator, menuXPos()+menuW-menuPad-iw, listTop+listH+2, 11, dim)
	}
}

func (c *Client) drawMenuCM(startY, contentH int32) {
	gs := c.engine.State
	if gs == nil {
		return
	}

	gold := colorGold
	dim := colorDim
	valClr := colorStatVal
	white := colorWhite
	lineH := int32(18)

	y := startY

	// Request CM tree if not loaded
	if !gs.CMTreeLoaded && len(gs.CMSkills) == 0 {
		gs.RequestCMTree()
		c.drawText("Loading CM tree...", menuXPos()+menuPad, y, 14, dim)
		return
	}

	// Header: unspent CMs
	cmUnspent := gs.CMUnspent
	if gs.ParsedCM != nil {
		cmUnspent = int(gs.ParsedCM.UnspentCMs)
	}
	c.drawText(fmt.Sprintf("Class Mastery — %d unspent", cmUnspent), menuXPos()+menuPad, y, 16, gold)
	y += lineH + 4

	// Show detail panel if viewing a skill
	if gs.CMDetail != nil {
		d := gs.CMDetail
		c.drawText(d.Name, menuXPos()+menuPad, y, 14, gold)
		y += lineH
		if d.Description != "" {
			// Word-wrap description
			desc := d.Description
			maxLineLen := 42
			for len(desc) > 0 {
				line := desc
				if len(line) > maxLineLen {
					cut := maxLineLen
					for cut > 0 && line[cut] != ' ' {
						cut--
					}
					if cut > 0 {
						line = desc[:cut]
						desc = desc[cut+1:]
					} else {
						line = desc[:maxLineLen]
						desc = desc[maxLineLen:]
					}
				} else {
					desc = ""
				}
				c.drawText(line, menuXPos()+menuPad, y, 12, white)
				y += lineH - 4
			}
		}
		y += 4
		c.drawText(fmt.Sprintf("Rank: %d / %d", d.Rank, d.MaxRank), menuXPos()+menuPad, y, 13, valClr)
		y += lineH

		// Spend button
		if cmUnspent > 0 {
			mx, my := rl.GetMouseX(), rl.GetMouseY()
			btnW := int32(80)
			btnH := int32(22)
			btnX := menuXPos() + menuPad
			green := rl.Color{R: 80, G: 200, B: 80, A: 255}
			rl.DrawRectangle(btnX, y, btnW, btnH, rl.Color{R: 40, G: 80, B: 40, A: 255})
			rl.DrawRectangleLines(btnX, y, btnW, btnH, green)
			c.drawText("Spend", btnX+20, y+4, 13, green)
			if rl.IsMouseButtonPressed(rl.MouseButtonLeft) && mx >= btnX && mx < btnX+btnW && my >= y && my < y+btnH {
				gs.SpendCM(d.ID)
			}

			// Back button
			backX := btnX + btnW + 8
			backW := int32(60)
			rl.DrawRectangleLines(backX, y, backW, btnH, dim)
			c.drawText("Back", backX+14, y+4, 13, dim)
			if rl.IsMouseButtonPressed(rl.MouseButtonLeft) && mx >= backX && mx < backX+backW && my >= y && my < y+btnH {
				gs.CMDetail = nil
			}
		}
		return
	}

	// Separator
	rl.DrawLine(menuXPos()+menuPad, y, menuXPos()+menuW-menuPad, y, colorSep)
	y += 6

	if len(gs.CMSkills) == 0 {
		c.drawText("No CM skills available", menuXPos()+menuPad, y, 14, dim)
		return
	}

	// Scrollable skill list
	maxVisible := int(contentH-lineH*2-16) / int(lineH)
	if maxVisible < 1 {
		maxVisible = 1
	}

	// Mouse wheel scroll
	wheel := int(rl.GetMouseWheelMove())
	if wheel != 0 {
		mx := rl.GetMouseX()
		my := rl.GetMouseY()
		if mx >= menuXPos() && mx <= menuXPos()+menuW && my >= startY && my <= startY+contentH {
			c.menuScroll -= wheel
		}
	}
	if c.menuScroll < 0 {
		c.menuScroll = 0
	}
	maxScroll := len(gs.CMSkills) - maxVisible
	if maxScroll < 0 {
		maxScroll = 0
	}
	if c.menuScroll > maxScroll {
		c.menuScroll = maxScroll
	}

	mx, my := rl.GetMouseX(), rl.GetMouseY()
	colW := menuW - menuPad*2

	for i := c.menuScroll; i < c.menuScroll+maxVisible && i < len(gs.CMSkills); i++ {
		sk := gs.CMSkills[i]
		rowY := y

		// Click to view detail
		isHover := mx >= menuXPos()+menuPad && mx < menuXPos()+menuW-menuPad &&
			my >= rowY && my < rowY+lineH
		if isHover && rl.IsMouseButtonPressed(rl.MouseButtonLeft) && sk.HasName {
			gs.RequestCMDetail(sk.ID)
		}

		// Skill name
		clr := valClr
		if !sk.HasName {
			clr = dim
		}
		if isHover && sk.HasName {
			clr = gold
		}
		c.drawText(sk.Name, menuXPos()+menuPad, y, 12, clr)

		// Cost (right-aligned)
		if sk.Cost > 0 {
			costStr := fmt.Sprintf("%d CM", sk.Cost)
			cw := c.measureText(costStr, 12)
			c.drawText(costStr, menuXPos()+menuPad+colW-cw, y, 12, dim)
		}

		y += lineH
	}

	// Scroll indicator
	if len(gs.CMSkills) > maxVisible {
		indicator := fmt.Sprintf("%d-%d of %d", c.menuScroll+1,
			min(c.menuScroll+maxVisible, len(gs.CMSkills)), len(gs.CMSkills))
		iw := c.measureText(indicator, 11)
		c.drawText(indicator, menuXPos()+menuW-menuPad-iw, startY+contentH-14, 11, dim)
	}
}

func (c *Client) drawTPPopup() {
	gs := c.engine.State
	if gs == nil {
		c.tpPopupOpen = false
		return
	}

	gold := colorGold
	valClr := colorStatVal
	green := rl.Color{R: 80, G: 200, B: 80, A: 255}
	bg := colorPopupBg
	border := colorBorder

	popW := int32(300)
	popH := int32(280)
	px := (screenWidth - popW) / 2
	py := (screenHeight - popH) / 2
	pad := int32(12)
	lineH := int32(22)

	// Background + border
	rl.DrawRectangle(px, py, popW, popH, bg)
	rl.DrawRectangleLines(px, py, popW, popH, border)

	y := py + pad
	mx, my := rl.GetMouseX(), rl.GetMouseY()

	// Title
	totalAlloc := 0
	for _, v := range gs.TPAlloc {
		totalAlloc += v
	}
	remaining := int(gs.TrainingPoints) - totalAlloc
	c.drawText("Spend Training Points", px+pad, y, 16, gold)
	y += lineH

	c.drawText(fmt.Sprintf("Available: %d", remaining), px+pad, y, 13, valClr)
	y += lineH

	rl.DrawLine(px+pad, y, px+popW-pad, y, colorSep)
	y += 6

	// Stat rows with [+] [-] buttons
	statNames := [7]string{"STR", "STA", "AGI", "DEX", "WIS", "INT", "CHA"}
	currentStats := [7]int32{
		gs.Player.Str, gs.Player.Sta, gs.Player.Agi, gs.Player.Dex,
		gs.Player.Wis, gs.Player.Int, gs.Player.Cha,
	}
	btnSize := int32(20)

	for i := 0; i < 7; i++ {
		lx := px + pad
		// Stat name + current value
		cur := currentStats[i] + int32(gs.TPAlloc[i])
		if gs.TPAlloc[i] > 0 {
			c.drawText(fmt.Sprintf("%s: %d (+%d)", statNames[i], cur, gs.TPAlloc[i]), lx, y+2, 13, green)
		} else {
			c.drawText(fmt.Sprintf("%s: %d", statNames[i], currentStats[i]), lx, y+2, 13, valClr)
		}

		// [-] button
		minusX := px + popW - pad - btnSize
		rl.DrawRectangleLines(minusX, y, btnSize, btnSize, valClr)
		c.drawText("-", minusX+7, y+2, 13, valClr)
		if rl.IsMouseButtonPressed(rl.MouseButtonLeft) && mx >= minusX && mx < minusX+btnSize && my >= y && my < y+btnSize {
			if gs.TPAlloc[i] > 0 {
				gs.TPAlloc[i]--
			}
		}

		// [+] button
		plusX := minusX - btnSize - 4
		rl.DrawRectangleLines(plusX, y, btnSize, btnSize, valClr)
		c.drawText("+", plusX+6, y+2, 13, valClr)
		if rl.IsMouseButtonPressed(rl.MouseButtonLeft) && mx >= plusX && mx < plusX+btnSize && my >= y && my < y+btnSize {
			if remaining > 0 {
				gs.TPAlloc[i]++
			}
		}

		y += lineH
	}

	y += 4
	rl.DrawLine(px+pad, y, px+popW-pad, y, colorSep)
	y += 8

	// Bottom buttons: Save Changes / Reset / Cancel
	btnW := int32(82)
	btnH := int32(24)
	btnGap := int32(8)

	// Save Changes
	if totalAlloc > 0 {
		rl.DrawRectangle(px+pad, y, btnW, btnH, rl.Color{R: 40, G: 80, B: 40, A: 255})
		rl.DrawRectangleLines(px+pad, y, btnW, btnH, green)
		c.drawText("Save", px+pad+24, y+5, 13, green)
		if rl.IsMouseButtonPressed(rl.MouseButtonLeft) && mx >= px+pad && mx < px+pad+btnW && my >= y && my < y+btnH {
			gs.SpendTrainingPoints()
			c.tpPopupOpen = false
		}
	}

	// Reset
	resetX := px + pad + btnW + btnGap
	dim := rl.Color{R: 180, G: 160, B: 140, A: 255}
	rl.DrawRectangleLines(resetX, y, btnW-20, btnH, dim)
	c.drawText("Reset", resetX+10, y+5, 13, dim)
	if rl.IsMouseButtonPressed(rl.MouseButtonLeft) && mx >= resetX && mx < resetX+btnW-20 && my >= y && my < y+btnH {
		gs.TPAlloc = [7]int{}
	}

	// Cancel
	cancelX := resetX + btnW - 20 + btnGap
	rl.DrawRectangleLines(cancelX, y, btnW-20, btnH, dim)
	c.drawText("Cancel", cancelX+6, y+5, 13, dim)
	if rl.IsMouseButtonPressed(rl.MouseButtonLeft) && mx >= cancelX && mx < cancelX+btnW-20 && my >= y && my < y+btnH {
		gs.TPAlloc = [7]int{}
		c.tpPopupOpen = false
	}

	// ESC closes popup
	if rl.IsKeyPressed(rl.KeyEscape) {
		gs.TPAlloc = [7]int{}
		c.tpPopupOpen = false
	}
}

func (c *Client) drawMenuNearby(startY, contentH int32) {
	gs := c.engine.State
	if gs == nil {
		return
	}

	gold := colorGold
	white := colorWhite
	dim := colorDim
	playerClr := colorPlayerName
	npcClr := rl.Color{R: 200, G: 200, B: 160, A: 255}
	lineH := int32(18)

	// Collect and sort entities: players first, then NPCs
	type nearbyEntry struct {
		Name     string
		Level    int
		IsPlayer bool
	}
	var players, npcs []nearbyEntry
	for _, ent := range gs.Entities {
		if ent.Name == "" {
			continue
		}
		e := nearbyEntry{Name: ent.Name, Level: int(ent.Level), IsPlayer: ent.IsPlayer}
		if ent.IsPlayer {
			players = append(players, e)
		} else {
			npcs = append(npcs, e)
		}
	}

	// Sort each group by name
	sort.Slice(players, func(i, j int) bool { return players[i].Name < players[j].Name })
	sort.Slice(npcs, func(i, j int) bool { return npcs[i].Name < npcs[j].Name })

	// Header
	y := startY
	c.drawText(fmt.Sprintf("Nearby: %d players, %d NPCs", len(players), len(npcs)), menuXPos()+menuPad, y, 14, gold)
	y += lineH + 4

	// Scrollable list
	listTop := y
	listH := contentH - (y - startY) - 4
	maxVisible := int(listH / lineH)
	all := append(players, npcs...)

	if c.menuScroll > len(all)-maxVisible {
		c.menuScroll = len(all) - maxVisible
	}
	if c.menuScroll < 0 {
		c.menuScroll = 0
	}

	for i := c.menuScroll; i < len(all) && int32(i-c.menuScroll)*lineH < listH; i++ {
		ey := listTop + int32(i-c.menuScroll)*lineH
		e := all[i]

		// Alternating row background
		if i%2 == 0 {
			rl.DrawRectangle(menuXPos()+menuPad, ey, menuW-menuPad*2, lineH, colorRowBg1)
		}

		clr := npcClr
		if e.IsPlayer {
			clr = playerClr
		}
		c.drawText(e.Name, menuXPos()+menuPad+4, ey+2, 13, clr)

		// Level on right
		lvlStr := fmt.Sprintf("Lv %d", e.Level)
		lw := c.measureText(lvlStr, 13)
		c.drawText(lvlStr, menuXPos()+menuW-menuPad-lw-4, ey+2, 13, white)
	}

	// Scroll indicator
	if len(all) > maxVisible {
		indicator := fmt.Sprintf("%d-%d of %d", c.menuScroll+1, min(c.menuScroll+maxVisible, len(all)), len(all))
		iw := c.measureText(indicator, 11)
		c.drawText(indicator, menuXPos()+menuW-menuPad-iw, listTop+listH+2, 11, dim)
	}
}

func (c *Client) drawMenuSocial(startY, contentH int32) {
	gold := colorGold
	dimGold := colorGoldDim

	// Sub-tab bar: Who / Group / LFG / Auction
	subTabs := []string{"Who", "Group", "LFG", "Auction"}
	stx := menuXPos() + menuPad
	mx, my := rl.GetMouseX(), rl.GetMouseY()
	subTabH := int32(22)
	for i, label := range subTabs {
		tw := c.measureText(label, 13) + 16
		clr := dimGold
		if i == c.socialSubTab {
			clr = gold
			rl.DrawRectangle(stx, startY+subTabH-2, tw, 2, gold)
		}
		if rl.IsMouseButtonPressed(rl.MouseButtonLeft) && mx >= stx && mx < stx+tw && my >= startY && my < startY+subTabH {
			c.socialSubTab = i
			c.menuScroll = 0
		}
		c.drawText(label, stx+8, startY+4, 13, clr)
		stx += tw + 4
	}
	innerY := startY + subTabH + 4
	innerH := contentH - subTabH - 4

	switch c.socialSubTab {
	case 0:
		c.drawMenuWho(innerY, innerH)
	case 1:
		c.drawMenuGroup(innerY, innerH)
	case 2:
		c.drawMenuLFG(innerY, innerH)
	case 3:
		c.drawMenuAuction(innerY, innerH)
	}
}

func (c *Client) drawMenuWho(startY, contentH int32) {
	gs := c.engine.State
	if gs == nil {
		return
	}

	gold := colorGold
	white := colorWhite
	dim := colorDim
	lineH := int32(18)

	// Request who list if we don't have one yet
	if len(gs.WhoList) == 0 && !c.whoRequested {
		c.engine.SendWhoList()
		c.whoRequested = true
	}

	y := startY
	if len(gs.WhoList) == 0 {
		c.drawText("Requesting who list...", menuXPos()+menuPad, y, 14, dim)
		return
	}

	// Header
	c.drawText(fmt.Sprintf("Players Online: %d", len(gs.WhoList)), menuXPos()+menuPad, y, 14, gold)

	// Refresh + Sort buttons (right-aligned)
	mx, my := rl.GetMouseX(), rl.GetMouseY()

	sortLabel := "[A-Z]"
	if c.whoSortByLevel {
		sortLabel = "[Level]"
	}
	sw := c.measureText(sortLabel, 13)
	sx := menuXPos() + menuW - menuPad - sw

	refreshStr := "[Refresh]"
	rw := c.measureText(refreshStr, 13)
	rx := sx - rw - 8

	isSortHover := mx >= sx && mx < sx+sw && my >= y && my < y+lineH
	sClr := dim
	if isSortHover {
		sClr = gold
		if rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
			c.whoSortByLevel = !c.whoSortByLevel
			c.menuScroll = 0
		}
	}
	c.drawText(sortLabel, sx, y, 13, sClr)

	isRefreshHover := mx >= rx && mx < rx+rw && my >= y && my < y+lineH
	rClr := dim
	if isRefreshHover {
		rClr = gold
		if rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
			c.engine.SendWhoList()
		}
	}
	c.drawText(refreshStr, rx, y, 13, rClr)
	y += lineH + 4

	// Sort entries
	entries := make([]game.WhoEntry, len(gs.WhoList))
	copy(entries, gs.WhoList)
	if c.whoSortByLevel {
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].Level != entries[j].Level {
				return entries[i].Level > entries[j].Level // highest first
			}
			return entries[i].Name < entries[j].Name
		})
	} else {
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	}

	// Scrollable list
	listTop := y
	listH := contentH - (y - startY) - 4
	maxVisible := int(listH / lineH)
	if c.menuScroll > len(entries)-maxVisible {
		c.menuScroll = len(entries) - maxVisible
	}
	if c.menuScroll < 0 {
		c.menuScroll = 0
	}

	for i := c.menuScroll; i < len(entries) && int32(i-c.menuScroll)*lineH < listH; i++ {
		ey := listTop + int32(i-c.menuScroll)*lineH
		e := entries[i]

		if i%2 == 0 {
			rl.DrawRectangle(menuXPos()+menuPad, ey, menuW-menuPad*2, lineH, colorRowBg1)
		}

		c.drawText(e.Name, menuXPos()+menuPad+4, ey+2, 13, colorPlayerName)

		// Race/Class/Level on right
		info := fmt.Sprintf("Lv %d %s %s", e.Level, raceName(int(e.Race)), className(int(e.Class)))
		iw := c.measureText(info, 12)
		c.drawText(info, menuXPos()+menuW-menuPad-iw-4, ey+2, 12, white)
	}

	// Scroll indicator
	if len(entries) > maxVisible {
		indicator := fmt.Sprintf("%d-%d of %d", c.menuScroll+1, min(c.menuScroll+maxVisible, len(entries)), len(entries))
		iw := c.measureText(indicator, 11)
		c.drawText(indicator, menuXPos()+menuW-menuPad-iw, listTop+listH+2, 11, dim)
	}
}

// --- Settings Tab ---

// Resolution presets available in the Settings tab.
type resPreset struct {
	W, H  int32
	Label string
}

var resolutionPresets []resPreset

func buildResolutionPresets() {
	// Common gaming resolutions we want to show (if the monitor supports them).
	wanted := [][2]int32{
		{640, 480}, {800, 600}, {1024, 768}, {1280, 960},       // 4:3
		{1280, 800}, {1680, 1050}, {1920, 1200}, {2560, 1600},  // 16:10
		{1280, 720}, {1600, 900}, {1920, 1080}, {2560, 1440}, {3840, 2160}, // 16:9
		{2560, 1080}, {3440, 1440},                              // 21:9
		{5120, 1440},                                            // 32:9
	}

	// Build set of modes the monitor actually supports (from GLFW).
	// Track max refresh rate per resolution.
	modes := gfx.GetVideoModes()
	supported := make(map[[2]int32]bool)
	maxHz := make(map[[2]int32]int)
	for _, m := range modes {
		key := [2]int32{int32(m.Width), int32(m.Height)}
		supported[key] = true
		if m.RefreshRate > maxHz[key] {
			maxHz[key] = m.RefreshRate
		}
	}

	// Native resolution = highest mode reported by GLFW (not GetMonitorWidth
	// which returns current resolution — wrong if already in exclusive fullscreen).
	nativeW := int32(0)
	nativeH := int32(0)
	for _, m := range modes {
		w, h := int32(m.Width), int32(m.Height)
		if w*h > nativeW*nativeH {
			nativeW = w
			nativeH = h
		}
	}

	resolutionPresets = nil
	for _, wh := range wanted {
		if !supported[wh] {
			continue
		}
		hz := maxHz[wh]
		label := fmt.Sprintf("%dx%d %dHz (%s)", wh[0], wh[1], hz, aspectLabel(wh[0], wh[1]))
		if wh[0] == nativeW && wh[1] == nativeH {
			label += " Native"
		}
		resolutionPresets = append(resolutionPresets, resPreset{wh[0], wh[1], label})
	}

	// Add native if not already in list (unusual resolution)
	found := false
	for _, p := range resolutionPresets {
		if p.W == nativeW && p.H == nativeH {
			found = true
			break
		}
	}
	if !found && nativeW > 0 && nativeH > 0 {
		resolutionPresets = append(resolutionPresets, resPreset{
			nativeW, nativeH, fmt.Sprintf("%dx%d (Native)", nativeW, nativeH),
		})
	}
}

func aspectLabel(w, h int32) string {
	ratio := float64(w) / float64(h)
	switch {
	case ratio > 3.4: // 32:9
		return "32:9"
	case ratio > 2.2: // 21:9
		return "21:9"
	case ratio > 1.7: // 16:9
		return "16:9"
	case ratio > 1.55: // 16:10
		return "16:10"
	default: // 4:3
		return "4:3"
	}
}

var windowModeLabels = []string{"Windowed", "Fullscreen"}
var fpsCapLabels = []string{"30", "60", "75", "120", "144", "165", "240", "Unlimited"}
var fpsCapValues = []int32{30, 60, 75, 120, 144, 165, 240, 999}

func (c *Client) drawMenuLFG(startY, contentH int32) {
	gs := c.engine.State
	if gs == nil {
		return
	}

	gold := colorGold
	white := colorWhite
	dim := colorDim
	lineH := int32(18)
	mx, my := rl.GetMouseX(), rl.GetMouseY()

	y := startY

	// LFG toggle button
	regLabel := "[Register LFG]"
	if gs.LFGRegistered {
		regLabel = "[Unregister LFG]"
	}
	rw := c.measureText(regLabel, 13)
	rx := menuXPos() + menuPad
	isRegHover := mx >= rx && mx < rx+rw && my >= y && my < y+lineH
	isRegSelected := c.menuRow == 0
	regClr := dim
	if isRegHover || isRegSelected {
		regClr = gold
		if rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
			c.engine.SendLFGToggle(!gs.LFGRegistered)
		}
	}
	c.drawText(regLabel, rx, y, 13, regClr)
	y += lineH + 4

	if len(gs.LFGList) == 0 {
		c.drawText("No players in LFG list.", menuXPos()+menuPad, y, 14, dim)
		return
	}

	// Header
	c.drawText(fmt.Sprintf("Players LFG: %d", len(gs.LFGList)), menuXPos()+menuPad, y, 14, gold)
	y += lineH + 4

	// Sort by level descending
	entries := make([]game.LFGEntry, len(gs.LFGList))
	copy(entries, gs.LFGList)
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Level != entries[j].Level {
			return entries[i].Level > entries[j].Level
		}
		return entries[i].Name < entries[j].Name
	})

	// Scrollable list
	listTop := y
	listH := contentH - (y - startY) - 4
	maxVisible := int(listH / lineH)
	if c.menuScroll > len(entries)-maxVisible {
		c.menuScroll = len(entries) - maxVisible
	}
	if c.menuScroll < 0 {
		c.menuScroll = 0
	}

	for i := c.menuScroll; i < len(entries) && int32(i-c.menuScroll)*lineH < listH; i++ {
		ey := listTop + int32(i-c.menuScroll)*lineH
		e := entries[i]

		if i%2 == 0 {
			rl.DrawRectangle(menuXPos()+menuPad, ey, menuW-menuPad*2, lineH, colorRowBg1)
		}

		// Name (with guild if present)
		nameStr := e.Name
		if e.GuildName != "" {
			nameStr = fmt.Sprintf("%s <%s>", e.Name, e.GuildName)
		}
		c.drawText(nameStr, menuXPos()+menuPad+4, ey+2, 13, colorPlayerName)

		// Level/Class on right
		info := fmt.Sprintf("Lv %d %s", e.Level, className(int(e.Class)))
		iw := c.measureText(info, 12)
		c.drawText(info, menuXPos()+menuW-menuPad-iw-4, ey+3, 12, white)
	}
}

func (c *Client) drawMenuAuction(startY, contentH int32) {
	gs := c.engine.State
	if gs == nil {
		return
	}

	gold := colorGold
	white := colorWhite
	dim := colorDim
	cyan := rl.Color{R: 0, G: 200, B: 200, A: 255}
	green := rl.Color{R: 100, G: 220, B: 100, A: 255}
	lineH := int32(18)
	mx, my := rl.GetMouseX(), rl.GetMouseY()
	panelLeft := menuXPos() + menuPad
	panelRight := menuXPos() + menuW - menuPad
	filterW := int32(140) // left column width
	dividerX := panelLeft + filterW + 6
	resultLeft := dividerX + 4

	// === LEFT COLUMN: Search Filters ===
	y := startY
	filterBg := rl.Color{R: 20, G: 20, B: 32, A: 255}
	rl.DrawRectangle(panelLeft-2, y, filterW+4, contentH, filterBg)

	c.drawText("Search", panelLeft, y+2, 13, gold)
	y += lineH + 4

	// Name filter input box
	c.drawText("Name:", panelLeft, y+2, 11, dim)
	y += lineH
	boxBg := rl.Color{R: 12, G: 12, B: 20, A: 255}
	boxBorder := rl.Color{R: 50, G: 50, B: 70, A: 255}
	if c.auctionSearchFocus {
		boxBorder = cyan
	}
	rl.DrawRectangle(panelLeft, y, filterW, lineH, boxBg)
	rl.DrawRectangleLines(panelLeft, y, filterW, lineH, boxBorder)

	// Click to focus
	if mx >= panelLeft && mx < panelLeft+filterW && my >= y && my < y+lineH {
		if rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
			c.auctionSearchFocus = true
		}
	}

	// Text input when focused
	if c.auctionSearchFocus {
		key := rl.GetCharPressed()
		for key > 0 {
			if key >= 32 && key < 127 && len(c.auctionSearchName) < 20 {
				c.auctionSearchName += string(rune(key))
			}
			key = rl.GetCharPressed()
		}
		if rl.IsKeyPressed(rl.KeyBackspace) && len(c.auctionSearchName) > 0 {
			c.auctionSearchName = c.auctionSearchName[:len(c.auctionSearchName)-1]
		}
		if rl.IsKeyPressed(rl.KeyEnter) || rl.IsKeyPressed(rl.KeyEscape) {
			c.auctionSearchFocus = false
		}
	}

	displayText := c.auctionSearchName
	if displayText == "" {
		displayText = "(all)"
	}
	if c.auctionSearchFocus {
		displayText += "_"
	}
	c.drawText(displayText, panelLeft+3, y+3, 11, white)
	y += lineH + 6

	// Level range
	c.drawText("Level:", panelLeft, y+2, 11, dim)
	y += lineH
	if c.auctionLevelMin == 0 { c.auctionLevelMin = 1 }
	if c.auctionLevelMax == 0 { c.auctionLevelMax = 60 }
	lvlStr := fmt.Sprintf("%d - %d", c.auctionLevelMin, c.auctionLevelMax)
	c.drawText(lvlStr, panelLeft+3, y+2, 11, white)
	// - / + buttons for min
	minMinusX := panelLeft + 50
	if mx >= minMinusX && mx < minMinusX+14 && my >= y && my < y+lineH && rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
		if c.auctionLevelMin > 1 { c.auctionLevelMin -= 5 }
		if c.auctionLevelMin < 1 { c.auctionLevelMin = 1 }
	}
	if mx >= minMinusX+16 && mx < minMinusX+30 && my >= y && my < y+lineH && rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
		if c.auctionLevelMin < 60 { c.auctionLevelMin += 5 }
	}
	// - / + for max
	maxMinusX := panelLeft + 90
	if mx >= maxMinusX && mx < maxMinusX+14 && my >= y && my < y+lineH && rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
		if c.auctionLevelMax > 1 { c.auctionLevelMax -= 5 }
	}
	if mx >= maxMinusX+16 && mx < maxMinusX+30 && my >= y && my < y+lineH && rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
		if c.auctionLevelMax < 60 { c.auctionLevelMax += 5 }
		if c.auctionLevelMax > 60 { c.auctionLevelMax = 60 }
	}
	y += lineH + 8

	// Search button
	searchLabel := "[Search]"
	sw := c.measureText(searchLabel, 13)
	isSearchHover := mx >= panelLeft && mx < panelLeft+sw && my >= y && my < y+lineH
	searchClr := dim
	if isSearchHover { searchClr = cyan }
	if isSearchHover && rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
		c.auctionDoSearch(gs)
	}
	c.drawText(searchLabel, panelLeft, y, 13, searchClr)

	// Search All button
	allLabel := "[All]"
	aw := c.measureText(allLabel, 13)
	allX := panelLeft + sw + 8
	isAllHover := mx >= allX && mx < allX+aw && my >= y && my < y+lineH
	allClr := dim
	if isAllHover { allClr = cyan }
	if isAllHover && rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
		c.auctionSearchName = ""
		c.auctionLevelMin = 1
		c.auctionLevelMax = 60
		c.auctionDoSearch(gs)
		c.auctionFetchAll = true
	}
	c.drawText(allLabel, allX, y, 13, allClr)
	y += lineH + 6

	// Item count
	if len(gs.AuctionItems) > 0 {
		c.drawText(fmt.Sprintf("%d items", len(gs.AuctionItems)), panelLeft, y, 11, dim)
		y += lineH
	}

	// Load More
	if gs.AuctionLastListing != 0 {
		moreLabel := "[More]"
		mw := c.measureText(moreLabel, 12)
		isMoreHover := mx >= panelLeft && mx < panelLeft+mw && my >= y && my < y+lineH
		moreClr := dim
		if isMoreHover { moreClr = cyan }
		if isMoreHover && rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
			c.engine.SendAuctionNextPage()
		}
		c.drawText(moreLabel, panelLeft, y, 12, moreClr)
	}

	// === DIVIDER ===
	rl.DrawRectangle(dividerX, startY, 1, contentH, rl.Color{R: 40, G: 40, B: 60, A: 255})

	// === RIGHT COLUMN: Results ===
	y = startY
	resultW := panelRight - resultLeft

	// Auto-request on first open
	if !c.auctionRequested {
		c.auctionDoSearch(gs)
		c.auctionFetchAll = true
	}

	// Auto-fetch all pages
	if c.auctionFetchAll && gs.AuctionLastListing != 0 && time.Since(c.auctionLastFetch) > 1500*time.Millisecond {
		c.engine.SendAuctionNextPage()
		c.auctionLastFetch = time.Now()
		if len(gs.AuctionItems) > 500 {
			c.auctionFetchAll = false
		}
	}

	if len(gs.AuctionItems) == 0 {
		c.drawText("Loading...", resultLeft, y+20, 14, dim)
		return
	}

	// Column headers
	nameCol := resultLeft
	lvlCol := resultLeft + resultW*42/100
	sellerCol := resultLeft + resultW*50/100
	bidCol := resultLeft + resultW*70/100
	buyoutCol := resultLeft + resultW*86/100

	headerBg := rl.Color{R: 25, G: 25, B: 40, A: 255}
	rl.DrawRectangle(resultLeft-2, y, resultW+4, lineH, headerBg)
	c.drawText("Item", nameCol, y+2, 11, gold)
	c.drawText("Lv", lvlCol, y+2, 11, gold)
	c.drawText("Seller", sellerCol, y+2, 11, gold)
	c.drawText("Bid", bidCol, y+2, 11, gold)
	c.drawText("Buyout", buyoutCol, y+2, 11, gold)
	y += lineH + 1

	// Scrollable results
	listTop := y
	listH := contentH - (y - startY) - 4
	maxVisible := int(listH / lineH)
	totalItems := len(gs.AuctionItems)

	if c.menuScroll > totalItems-maxVisible {
		c.menuScroll = totalItems - maxVisible
	}
	if c.menuScroll < 0 {
		c.menuScroll = 0
	}

	rowBg1 := rl.Color{R: 18, G: 18, B: 28, A: 255}
	rowBg2 := rl.Color{R: 22, G: 22, B: 35, A: 255}
	hoverBg := rl.Color{R: 30, G: 30, B: 50, A: 255}

	for i := c.menuScroll; i < totalItems && int32(i-c.menuScroll)*lineH < listH; i++ {
		ey := listTop + int32(i-c.menuScroll)*lineH
		ai := gs.AuctionItems[i]

		bg := rowBg1
		if i%2 == 1 { bg = rowBg2 }
		isRowHover := mx >= resultLeft && mx < panelRight && my >= ey && my < ey+lineH
		if isRowHover { bg = hoverBg }
		rl.DrawRectangle(resultLeft-2, ey, resultW+4, lineH, bg)

		// Item name (color by level)
		nameClr := white
		if ai.Item.LevelReq >= 45 {
			nameClr = rl.Color{R: 255, G: 200, B: 50, A: 255}
		} else if ai.Item.LevelReq >= 30 {
			nameClr = rl.Color{R: 100, G: 180, B: 255, A: 255}
		}
		c.drawText(ai.Item.Name, nameCol, ey+2, 11, nameClr)

		if ai.Item.LevelReq > 0 {
			c.drawText(fmt.Sprintf("%d", ai.Item.LevelReq), lvlCol, ey+2, 10, dim)
		}
		c.drawText(ai.Seller, sellerCol, ey+2, 10, dim)
		c.drawText(formatTunar(ai.CurrentBid), bidCol, ey+2, 10, green)
		if ai.BuyoutPrice > 0 {
			c.drawText(formatTunar(ai.BuyoutPrice), buyoutCol, ey+2, 10, gold)
		} else {
			c.drawText("-", buyoutCol, ey+2, 10, dim)
		}
	}

	// Scrollbar
	if totalItems > maxVisible && maxVisible > 0 {
		scrollFrac := float32(c.menuScroll) / float32(totalItems-maxVisible)
		barH := listH * int32(maxVisible) / int32(totalItems)
		if barH < 10 { barH = 10 }
		barY := listTop + int32(scrollFrac*float32(listH-barH))
		rl.DrawRectangle(panelRight-2, barY, 3, barH, rl.Color{R: 60, G: 60, B: 80, A: 200})
	}
}

// auctionDoSearch sends an auction search with current filter settings.
func (c *Client) auctionDoSearch(gs *game.GameState) {
	gs.AuctionItems = nil
	gs.AuctionLastListing = 0
	gs.AuctionSeq = 0
	seq := gs.AuctionSessionID
	if seq == 0 { seq = 0x04 }

	// Build search filter matching PS2 format
	var filter []byte
	filter = append(filter, 0x01, 0x01) // flags

	// Name/type filter
	searchName := c.auctionSearchName
	if searchName == "" {
		// No filter: 9 zero bytes
		filter = append(filter, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00)
	} else {
		// Type filter: uint32(charCount) + UTF-16LE(name)
		filter = append(filter, 0x00, 0x00, 0x00, 0x00) // nameLen = 0 (no name filter)
		runes := []rune(searchName)
		lenBytes := make([]byte, 4)
		lenBytes[0] = byte(len(runes))
		filter = append(filter, lenBytes...)
		for _, r := range runes {
			filter = append(filter, byte(r), byte(r>>8))
		}
	}

	// Class mask (all classes)
	filter = append(filter, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF)

	// Level range
	filter = append(filter, byte(c.auctionLevelMin), byte(c.auctionLevelMax))

	// Unk + max price + sort
	filter = append(filter, 0x00, 0xFE, 0xA7, 0xD6, 0xB9, 0x07, 0x03, 0x01, 0x01)

	c.engine.SendAuctionRequest(0x07, seq, filter)
	c.auctionRequested = true
	c.auctionFetchAll = false
	c.menuScroll = 0
}

// formatTunar is defined below (shared with merchant/repair UI)

// drawSubTabs renders a row of clickable subtab labels with underline on active.
func (c *Client) drawSubTabs(labels []string, active int, y int32, onClick func(int)) {
	stx := menuXPos() + menuPad
	mx, my := rl.GetMouseX(), rl.GetMouseY()
	h := int32(22)
	for i, label := range labels {
		tw := c.measureText(label, 13) + 16
		clr := colorGoldDim
		if i == active {
			clr = colorGold
			rl.DrawRectangle(stx, y+h-2, tw, 2, colorGold)
		}
		if rl.IsMouseButtonPressed(rl.MouseButtonLeft) && mx >= stx && mx < stx+tw && my >= y && my < y+h {
			onClick(i)
		}
		c.drawText(label, stx+8, y+4, 13, clr)
		stx += tw + 4
	}
}

func (c *Client) drawMenuGuild(startY, contentH int32) {
	gs := c.engine.State
	gold := colorGold
	white := rl.White
	gray := rl.Color{R: 180, G: 180, B: 180, A: 255}

	// Sub-tab bar: Roster / Chat
	subTabs := []string{"Roster", "Chat"}
	subY := startY
	for i, label := range subTabs {
		x := menuXPos() + menuPad + int32(i)*80
		col := gray
		if i == c.guildSubTab {
			col = gold
		}
		c.drawText(label, x, subY, 14, col)
	}
	y := subY + 22

	switch c.guildSubTab {
	case 0: // Roster — show guild members from GuildInfo (0x0468)
		c.drawText("Guild Roster", menuXPos()+menuPad, y, 14, gold)
		y += 20

		// Show guild chat messages that have guild member info
		guildMembers := 0
		for _, msg := range gs.ChatLog {
			if msg.Channel == game.ChatGuild {
				guildMembers++
			}
		}

		if guildMembers == 0 {
			c.drawText("No guild info received yet.", menuXPos()+menuPad, y, 12, gray)
		} else {
			for _, msg := range gs.ChatLog {
				if msg.Channel == game.ChatGuild {
					if y > startY+contentH-20 {
						break
					}
					c.drawText(msg.Text, menuXPos()+menuPad, y, 12, white)
					y += 16
				}
			}
		}

	case 1: // Chat — guild chat log
		c.drawText("Guild Chat", menuXPos()+menuPad, y, 14, gold)
		y += 20

		count := 0
		for _, msg := range gs.ChatLog {
			if msg.Channel == game.ChatGuild {
				count++
			}
		}
		if count == 0 {
			c.drawText("No guild messages.", menuXPos()+menuPad, y, 12, gray)
		} else {
			// Show last N messages that fit
			maxRows := int(contentH-40) / 16
			start := count - maxRows
			if start < 0 {
				start = 0
			}
			idx := 0
			for _, msg := range gs.ChatLog {
				if msg.Channel != game.ChatGuild {
					continue
				}
				if idx < start {
					idx++
					continue
				}
				if y > startY+contentH-20 {
					break
				}
				col := rl.Color{R: 0, G: 200, B: 100, A: 255} // guild green
				c.drawText(msg.Text, menuXPos()+menuPad, y, 12, col)
				y += 16
				idx++
			}
		}
	}

	_ = white
}

func (c *Client) drawMenuSettings(startY, contentH int32) {
	gold := colorGold
	dim := colorDim

	mx := rl.GetMouseX()
	my := rl.GetMouseY()
	clicked := rl.IsMouseButtonPressed(rl.MouseButtonLeft)

	rowX := menuXPos() + menuPad
	rowW := menuW - menuPad*2

	// Subtab bar
	subtabs := []string{"Graphics", "Camera", "Audio"}
	c.drawSubTabs(subtabs, c.settingsSubTab, startY, func(i int) { c.settingsSubTab = i })
	y := startY + 22

	sliderX := rowX + 8
	sliderW := rowW - 16
	sliderH := int32(16)

	switch c.settingsSubTab {
	case 0: // Graphics
		c.drawSettingsGraphics(y, rowX, rowW, mx, my, clicked, gold, dim)
	case 1: // Camera
		c.drawSettingsCamera(y, sliderX, sliderW, sliderH, mx, my, gold, dim)
	case 2: // Audio
		c.drawSettingsAudio(y, sliderX, sliderW, sliderH, rowX, rowW, mx, my, clicked, gold, dim)
	}
}

func (c *Client) drawSettingsGraphics(y, rowX, rowW, mx, my int32, clicked bool, gold, dim rl.Color) {
	white := colorWhite
	hoverBg := colorRowHover
	rowH := int32(20)
	px := menuXPos() + menuPad

	// Block clicks while display confirmation pending or input delay active.
	if c.displayConfirmPending || c.menuInputDelay > 0 {
		clicked = false
	}

	// Build presets lazily (need raylib init first for monitor info)
	if resolutionPresets == nil {
		buildResolutionPresets()
	}

	// --- Window Mode ---
	c.drawText("Window Mode", px, y, 14, gold)
	y += 18
	for i, label := range windowModeLabels {
		bx := px + int32(i)*90
		isCurrent := c.windowMode == i
		isHover := mx >= bx && mx < bx+85 && my >= y && my < y+rowH
		clr := dim
		if isCurrent {
			clr = gold
		} else if isHover {
			clr = white
		}
		if isCurrent {
			rl.DrawRectangle(bx, y, 85, rowH, rl.Color{R: 40, G: 35, B: 55, A: 255})
		}
		c.drawText(label, bx+4, y+3, 12, clr)
		if clicked && isHover && !isCurrent {
			c.setWindowMode(i)
		}
	}
	y += rowH + 12

	// --- Resolution ---
	c.drawText("Resolution", px, y, 14, gold)
	y += 18
	for i, preset := range resolutionPresets {
		ry := y + int32(i)*rowH
		isHover := mx >= rowX && mx < rowX+rowW && my >= ry && my < ry+rowH
		isCurrent := screenWidth == preset.W && screenHeight == preset.H

		if isHover {
			rl.DrawRectangle(rowX, ry, rowW, rowH, hoverBg)
		}

		clr := white
		if isCurrent {
			clr = gold
		} else if isHover {
			clr = colorGoldHover
		}
		label := preset.Label
		if isCurrent {
			label = "> " + label
		}
		c.drawText(label, rowX+8, ry+3, 12, clr)

		if clicked && isHover && !isCurrent {
			c.applyResolution(preset.W, preset.H)
		}
	}
	y += int32(len(resolutionPresets))*rowH + 12

	// --- VSync ---
	vsLabel := "VSync: OFF"
	if c.vsyncOn {
		vsLabel = "VSync: ON"
	}
	isHover := mx >= px && mx < px+120 && my >= y && my < y+rowH
	clr := white
	if isHover {
		clr = gold
	}
	c.drawText(vsLabel, px, y+3, 13, clr)
	if clicked && isHover {
		c.vsyncOn = !c.vsyncOn
		c.applyVSync()
	}
	y += rowH + 8

	// --- FPS Cap ---
	c.drawText("FPS Cap", px, y, 14, gold)
	y += 18
	for i, label := range fpsCapLabels {
		bw := int32(48)
		if label == "Unlimited" {
			bw = 65
		}
		bx := px
		for j := 0; j < i; j++ {
			if fpsCapLabels[j] == "Unlimited" {
				bx += 68
			} else {
				bx += 51
			}
		}
		isCurrent := c.fpsCap == fpsCapValues[i]
		isHover := mx >= bx && mx < bx+bw && my >= y && my < y+rowH
		clr := dim
		if isCurrent {
			clr = gold
		} else if isHover {
			clr = white
		}
		if isCurrent {
			rl.DrawRectangle(bx, y, bw, rowH, rl.Color{R: 40, G: 35, B: 55, A: 255})
		}
		c.drawText(label, bx+4, y+3, 12, clr)
		if clicked && isHover && !isCurrent {
			c.fpsCap = fpsCapValues[i]
			c.applyFPSCap()
		}
	}
	y += rowH + 12

	// --- FPS Counter ---
	fpsLabel := "FPS Counter: OFF"
	if c.showFPS {
		fpsLabel = "FPS Counter: ON"
	}
	isHover = mx >= px && mx < px+140 && my >= y && my < y+rowH
	clr = white
	if isHover {
		clr = gold
	}
	c.drawText(fpsLabel, px, y+3, 13, clr)
	if clicked && isHover {
		c.showFPS = !c.showFPS
		c.saveDisplaySettings()
	}
	y += rowH + 12

	// --- Current info ---
	mon := rl.GetCurrentMonitor()
	c.drawText(fmt.Sprintf("Monitor: %s (%dx%d @%dHz)",
		rl.GetMonitorName(mon), rl.GetMonitorWidth(mon), rl.GetMonitorHeight(mon), rl.GetMonitorRefreshRate(mon)),
		px, y, 11, dim)
}

func (c *Client) drawSettingsCamera(y, sliderX, sliderW, sliderH, mx, my int32, gold, dim rl.Color) {
	white := colorWhite
	hoverBg := colorRowHover
	rowX := menuXPos() + menuPad
	rowW := menuW - menuPad*2
	rowH := int32(20)
	clicked := rl.IsMouseButtonPressed(rl.MouseButtonLeft)

	// --- Camera Sensitivity slider ---
	c.drawText("Camera Sensitivity", menuXPos()+menuPad, y, 14, gold)
	y += 20

	rl.DrawRectangle(sliderX, y, sliderW, sliderH, colorRowBg2)
	// Sensitivity range: 0.1 to 3.0
	sensFrac := (c.camSensitivity - 0.1) / (3.0 - 0.1)
	if sensFrac < 0 {
		sensFrac = 0
	}
	if sensFrac > 1 {
		sensFrac = 1
	}
	rl.DrawRectangle(sliderX, y, int32(float32(sliderW)*sensFrac), sliderH, gold)

	if rl.IsMouseButtonDown(rl.MouseButtonLeft) &&
		mx >= sliderX && mx <= sliderX+sliderW &&
		my >= y-4 && my <= y+sliderH+4 {
		newFrac := float32(mx-sliderX) / float32(sliderW)
		if newFrac < 0 {
			newFrac = 0
		}
		if newFrac > 1 {
			newFrac = 1
		}
		c.camSensitivity = 0.1 + newFrac*(3.0-0.1)
	}

	y += sliderH + 4
	c.drawText(fmt.Sprintf("%.0f%%", c.camSensitivity*100), menuXPos()+menuPad, y, 12, dim)
	y += 24

	// --- Max Camera Zoom slider ---
	c.drawText("Max Camera Zoom", menuXPos()+menuPad, y, 14, gold)
	y += 20

	rl.DrawRectangle(sliderX, y, sliderW, sliderH, colorRowBg2)
	frac := (c.camMaxDist - 2) / (300 - 2)
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	rl.DrawRectangle(sliderX, y, int32(float32(sliderW)*frac), sliderH, gold)

	if rl.IsMouseButtonDown(rl.MouseButtonLeft) &&
		mx >= sliderX && mx <= sliderX+sliderW &&
		my >= y-4 && my <= y+sliderH+4 {
		newFrac := float32(mx-sliderX) / float32(sliderW)
		if newFrac < 0 {
			newFrac = 0
		}
		if newFrac > 1 {
			newFrac = 1
		}
		c.camMaxDist = 2 + newFrac*(300-2)
		if c.camDist > c.camMaxDist {
			c.camDist = c.camMaxDist
		}
	}

	y += sliderH + 4
	c.drawText(fmt.Sprintf("%.0f units (max 300)", c.camMaxDist), menuXPos()+menuPad, y, 12, dim)
	y += 24

	// --- Invert Pitch toggle ---
	invertLabel := "Invert Pitch: OFF"
	if c.invertPitch {
		invertLabel = "Invert Pitch: ON"
	}
	invertHover := mx >= rowX && mx < rowX+rowW && my >= y && my < y+rowH
	invertClr := white
	if invertHover {
		invertClr = colorGoldHover
		rl.DrawRectangle(rowX, y, rowW, rowH, hoverBg)
	}
	c.drawText(invertLabel, rowX+8, y+3, 13, invertClr)
	if clicked && invertHover {
		c.invertPitch = !c.invertPitch
	}
	y += rowH + 4

	// --- Reset to Defaults button ---
	resetLabel := "[Reset to Defaults]"
	resetHover := mx >= rowX && mx < rowX+rowW && my >= y && my < y+rowH
	resetClr := white
	if resetHover {
		resetClr = colorGoldHover
		rl.DrawRectangle(rowX, y, rowW, rowH, hoverBg)
	}
	c.drawText(resetLabel, rowX+8, y+3, 13, resetClr)
	if clicked && resetHover {
		c.camSensitivity = 1.0
		c.camMaxDist = 8
		c.invertPitch = false
		if c.camDist > c.camMaxDist {
			c.camDist = c.camMaxDist
		}
	}

	if rl.IsMouseButtonReleased(rl.MouseButtonLeft) {
		cfg := loadLoginConfig()
		cfg.MaxZoom = c.camMaxDist
		cfg.CamSensitivity = c.camSensitivity
		cfg.InvertPitch = c.invertPitch
		saveLoginConfig(cfg)
	}
}

func (c *Client) drawSettingsAudio(y, sliderX, sliderW, sliderH, rowX, rowW, mx, my int32, clicked bool, gold, dim rl.Color) {
	white := colorWhite
	hoverBg := colorRowHover
	rowH := int32(20)

	// Master Volume
	y = c.drawVolumeSlider("Master Volume", sliderX, sliderW, sliderH, y, mx, my, gold, dim,
		c.masterVolume, func(f float32) { c.masterVolume = f; rl.SetMasterVolume(f) })

	// SFX Volume
	y = c.drawVolumeSlider("Sound Effects", sliderX, sliderW, sliderH, y, mx, my, gold, dim,
		c.sfxVolume, func(f float32) {
			c.sfxVolume = f
			if c.audioSys != nil {
				c.audioSys.SetSFXVolume(f)
			}
		})

	// BGM Volume
	y = c.drawVolumeSlider("Background Music", sliderX, sliderW, sliderH, y, mx, my, gold, dim,
		c.bgmVolume, func(f float32) {
			c.bgmVolume = f
			if c.audioSys != nil {
				c.audioSys.SetBGMVolume(f)
			}
		})

	// Mute All toggle
	muteLabel := "[ ] Mute All"
	if c.masterVolume == 0 {
		muteLabel = "[X] Mute All"
	}
	muteHover := mx >= rowX && mx < rowX+rowW && my >= y && my < y+rowH
	muteClr := white
	if muteHover {
		muteClr = colorGoldHover
		rl.DrawRectangle(rowX, y, rowW, rowH, hoverBg)
	}
	c.drawText(muteLabel, rowX+8, y+3, 13, muteClr)
	if clicked && muteHover {
		if c.masterVolume > 0 {
			c.prevMasterVolume = c.masterVolume
			c.masterVolume = 0
			rl.SetMasterVolume(0)
		} else {
			if c.prevMasterVolume <= 0 {
				c.prevMasterVolume = 1.0
			}
			c.masterVolume = c.prevMasterVolume
			rl.SetMasterVolume(c.masterVolume)
		}
	}

	// Save audio settings on mouse release
	if rl.IsMouseButtonReleased(rl.MouseButtonLeft) {
		cfg := loadLoginConfig()
		cfg.MasterVolume = c.masterVolume
		cfg.SFXVolume = c.sfxVolume
		cfg.BGMVolume = c.bgmVolume
		saveLoginConfig(cfg)
	}
}

// drawVolumeSlider draws a labeled volume slider and returns the next Y position.
func (c *Client) drawVolumeSlider(label string, sliderX, sliderW, sliderH, y, mx, my int32, gold, dim rl.Color, value float32, setter func(float32)) int32 {
	c.drawText(label, sliderX, y, 12, dim)
	y += 14
	rl.DrawRectangle(sliderX, y, sliderW, sliderH, colorRowBg2)
	rl.DrawRectangle(sliderX, y, int32(float32(sliderW)*value), sliderH, gold)
	if rl.IsMouseButtonDown(rl.MouseButtonLeft) &&
		mx >= sliderX && mx <= sliderX+sliderW &&
		my >= y-4 && my <= y+sliderH+4 {
		f := float32(mx-sliderX) / float32(sliderW)
		if f < 0 {
			f = 0
		}
		if f > 1 {
			f = 1
		}
		setter(f)
	}
	y += sliderH + 2
	c.drawText(fmt.Sprintf("%.0f%%", value*100), sliderX, y, 12, dim)
	y += 18
	return y
}

// applyResolution changes the window size.
// Saves immediately for windowed/borderless. Requires confirmation for fullscreen.
func (c *Client) applyResolution(w, h int32) {
	screenWidth = w
	screenHeight = h

	if c.windowMode == 1 {
		// Exclusive fullscreen: needs confirmation (could lock out user)
		c.prevScreenWidth = screenWidth
		c.prevScreenHeight = screenHeight
		c.prevWindowMode = c.windowMode
		if c.audioSys != nil {
			c.audioSys.StopBGM()
		}
		c.bgmRestartDelay = 10
		if rl.IsWindowFullscreen() {
			rl.ToggleFullscreen()
		}
		rl.SetWindowSize(int(w), int(h))
		rl.ToggleFullscreen()
		c.displayConfirmPending = true
		c.displayConfirmTimer = 10.0
	} else {
		// Windowed/borderless: apply and save immediately
		rl.SetWindowSize(int(w), int(h))
		c.saveDisplaySettings()
	}
}

// setWindowMode switches between windowed, borderless, and exclusive fullscreen.
// Only exclusive fullscreen requires confirmation (can lock out user).
func (c *Client) setWindowMode(mode int) {
	// Only stop BGM when entering/leaving exclusive fullscreen (resets audio context).
	needsBGMRestart := c.windowMode == 1 || mode == 1
	if needsBGMRestart && c.audioSys != nil {
		c.audioSys.StopBGM()
		c.bgmRestartDelay = 10
	}

	// Exit current mode first
	if rl.IsWindowFullscreen() {
		rl.ToggleFullscreen()
	}
	if rl.IsWindowState(rl.FlagBorderlessWindowedMode) {
		rl.ToggleBorderlessWindowed()
	}

	switch mode {
	case 0: // Windowed
		rl.SetWindowSize(int(screenWidth), int(screenHeight))
	case 1: // Exclusive fullscreen
		rl.SetWindowSize(int(screenWidth), int(screenHeight))
		rl.ToggleFullscreen()
	}

	c.windowMode = mode

	if mode == 1 {
		// Exclusive fullscreen: confirmation with auto-revert
		c.prevScreenWidth = screenWidth
		c.prevScreenHeight = screenHeight
		c.prevWindowMode = 0 // revert to windowed
		c.displayConfirmPending = true
		c.displayConfirmTimer = 10.0
	} else {
		// Windowed/borderless: save immediately
		c.saveDisplaySettings()
	}
}

// applyWindowModeImmediate applies window mode without confirmation (used by config load).
func (c *Client) applyWindowModeImmediate(mode int) {
	if c.audioSys != nil {
		c.audioSys.StopBGM()
	}
	c.currentBGM = ""
	if rl.IsWindowFullscreen() {
		rl.ToggleFullscreen()
	}
	if rl.IsWindowState(rl.FlagBorderlessWindowedMode) {
		rl.ToggleBorderlessWindowed()
	}
	switch mode {
	case 0:
		rl.SetWindowSize(int(screenWidth), int(screenHeight))
	case 1:
		rl.SetWindowSize(int(screenWidth), int(screenHeight))
		rl.ToggleFullscreen()
	}
	c.windowMode = mode
}

// confirmDisplayChange accepts the pending display change and saves to config.
func (c *Client) confirmDisplayChange() {
	c.displayConfirmPending = false
	c.saveDisplaySettings()
}

// revertDisplayChange reverts to the previous display settings.
func (c *Client) revertDisplayChange() {
	c.displayConfirmPending = false
	screenWidth = c.prevScreenWidth
	screenHeight = c.prevScreenHeight
	c.applyWindowModeImmediate(c.prevWindowMode)
	c.saveDisplaySettings()
}

// applyVSync toggles VSync on/off.
func (c *Client) applyVSync() {
	if c.vsyncOn {
		rl.SetWindowState(rl.FlagVsyncHint)
	} else {
		rl.ClearWindowState(rl.FlagVsyncHint)
	}
	c.saveDisplaySettings()
}

// applyFPSCap sets the target FPS.
func (c *Client) applyFPSCap() {
	fps := c.fpsCap
	if fps >= 999 {
		fps = 0 // 0 = unlimited in raylib
	}
	rl.SetTargetFPS(fps)
	c.saveDisplaySettings()
}

// saveDisplaySettings persists all display settings to the config file.
func (c *Client) saveDisplaySettings() {
	cfg := loadLoginConfig()
	cfg.ResWidth = screenWidth
	cfg.ResHeight = screenHeight
	cfg.WindowMode = c.windowMode
	cfg.VSync = c.vsyncOn
	cfg.FPSCap = c.fpsCap
	cfg.ShowFPS = c.showFPS
	saveLoginConfig(cfg)
}

// --- Group Tab ---

func (c *Client) drawMenuGroup(startY, contentH int32) {
	gs := c.engine.State
	if gs == nil {
		return
	}

	gold := colorGold
	white := colorWhite
	dim := colorDim
	green := rl.Color{R: 100, G: 220, B: 100, A: 255}
	red := rl.Color{R: 220, G: 100, B: 100, A: 255}
	rowBg1 := colorRowBg1
	rowBg2 := colorRowBg2
	hoverBg := colorRowHover

	mx := rl.GetMouseX()
	my := rl.GetMouseY()
	clicked := rl.IsMouseButtonPressed(rl.MouseButtonLeft)

	y := startY

	// --- Section 1: Invite by name ---
	c.drawText("Invite Player", menuXPos()+menuPad, y, 14, gold)
	y += 20

	// Text input field
	inputX := menuXPos() + menuPad
	inputW := menuW - menuPad*2 - 70 // leave room for Send button
	inputH := int32(22)
	inputBg := rl.Color{R: 15, G: 14, B: 25, A: 255}
	inputBorder := colorSep
	if c.groupTyping {
		inputBorder = gold
	}
	rl.DrawRectangle(inputX, y, inputW, inputH, inputBg)
	rl.DrawRectangleLines(inputX, y, inputW, inputH, inputBorder)

	// Display text or placeholder
	if c.groupInviteInput != "" {
		dispText := c.groupInviteInput
		if c.groupTyping {
			dispText += "_"
		}
		c.drawText(dispText, inputX+4, y+4, 13, white)
	} else if c.groupTyping {
		c.drawText("_", inputX+4, y+4, 13, white)
	} else {
		c.drawText("Enter name...", inputX+4, y+4, 13, dim)
	}

	// Click on input field activates typing
	if clicked && mx >= inputX && mx < inputX+inputW && my >= y && my < y+inputH {
		c.groupTyping = true
	}

	// Invite button
	btnX := inputX + inputW + 6
	btnW := int32(60)
	btnHover := mx >= btnX && mx < btnX+btnW && my >= y && my < y+inputH
	btnBg := colorBtnBg
	if btnHover {
		btnBg = hoverBg
	}
	rl.DrawRectangle(btnX, y, btnW, inputH, btnBg)
	rl.DrawRectangleLines(btnX, y, btnW, inputH, colorSep)
	invLabel := "Invite"
	invLW := c.measureText(invLabel, 13)
	c.drawText(invLabel, btnX+(btnW-invLW)/2, y+4, 13, green)

	if clicked && btnHover && c.groupInviteInput != "" {
		c.sendGroupInviteByName(c.groupInviteInput)
		c.groupInviteInput = ""
		c.groupTyping = false
	}
	y += inputH + 10

	// --- Section 2: Pending Invite ---
	if gs.GroupInviteID != 0 {
		c.drawText("Pending Invite", menuXPos()+menuPad, y, 14, gold)
		y += 20

		rl.DrawRectangle(menuXPos()+menuPad, y, menuW-menuPad*2, menuRowH, rowBg1)
		invText := fmt.Sprintf("From: %s", gs.GroupInviter)
		c.drawText(invText, menuXPos()+menuPad+4, y+3, 13, white)

		// Accept button
		accW := int32(54)
		accX := menuXPos() + menuW - menuPad - accW - 60
		accHover := mx >= accX && mx < accX+accW && my >= y && my < y+menuRowH
		accBg := rl.Color{R: 30, G: 50, B: 30, A: 255}
		if accHover {
			accBg = rl.Color{R: 40, G: 70, B: 40, A: 255}
		}
		rl.DrawRectangle(accX, y+1, accW, menuRowH-2, accBg)
		rl.DrawRectangleLines(accX, y+1, accW, menuRowH-2, rl.Color{R: 80, G: 120, B: 80, A: 255})
		aLbl := "Accept"
		aLW := c.measureText(aLbl, 12)
		c.drawText(aLbl, accX+(accW-aLW)/2, y+4, 12, green)

		if clicked && accHover {
			c.engine.SendAcceptGroupInvite(gs.GroupInviteID)
			gs.GroupInviteID = 0
			gs.GroupInviter = ""
		}

		// Decline button
		decW := int32(54)
		decX := menuXPos() + menuW - menuPad - decW
		decHover := mx >= decX && mx < decX+decW && my >= y && my < y+menuRowH
		decBg := rl.Color{R: 50, G: 30, B: 30, A: 255}
		if decHover {
			decBg = colorGmRedBg
		}
		rl.DrawRectangle(decX, y+1, decW, menuRowH-2, decBg)
		rl.DrawRectangleLines(decX, y+1, decW, menuRowH-2, rl.Color{R: 120, G: 80, B: 80, A: 255})
		dLbl := "Decline"
		dLW := c.measureText(dLbl, 12)
		c.drawText(dLbl, decX+(decW-dLW)/2, y+4, 12, red)

		if clicked && decHover {
			c.engine.SendDeclineGroupInvite(gs.GroupInviteID)
			gs.GroupInviteID = 0
			gs.GroupInviter = ""
		}

		y += menuRowH + 10
	}

	// --- Section 3: Group Members ---
	if gs.GroupActive {
		hdr := fmt.Sprintf("Group Members (%d)", len(gs.GroupMembers))
		c.drawText(hdr, menuXPos()+menuPad, y, 14, gold)

		// Leave button (top-right of members section)
		leaveW := int32(52)
		leaveX := menuXPos() + menuW - menuPad - leaveW
		leaveY := y - 2
		leaveH := int32(20)
		leaveHover := mx >= leaveX && mx < leaveX+leaveW && my >= leaveY && my < leaveY+leaveH
		leaveBg := rl.Color{R: 50, G: 30, B: 30, A: 255}
		if leaveHover {
			leaveBg = colorGmRedBg
		}
		rl.DrawRectangle(leaveX, leaveY, leaveW, leaveH, leaveBg)
		rl.DrawRectangleLines(leaveX, leaveY, leaveW, leaveH, rl.Color{R: 120, G: 80, B: 80, A: 255})
		lLbl := "Leave"
		lLW := c.measureText(lLbl, 12)
		c.drawText(lLbl, leaveX+(leaveW-lLW)/2, leaveY+3, 12, red)

		if clicked && leaveHover {
			c.engine.SendLeaveGroup()
		}

		y += 20

		for i, m := range gs.GroupMembers {
			ry := y + int32(i)*menuRowH

			// Alternating row background
			bg := rowBg1
			if i%2 == 1 {
				bg = rowBg2
			}
			rl.DrawRectangle(menuXPos()+menuPad, ry, menuW-menuPad*2, menuRowH, bg)

			// Self indicator
			nameClr := white
			if m.ObjectID == gs.PlayerObjectID {
				nameClr = colorPlayerName
			}
			c.drawText(m.Name, menuXPos()+menuPad+4, ry+3, 13, nameClr)

			// Boot button (not for self)
			if m.ObjectID != gs.PlayerObjectID {
				bootW := int32(42)
				bootX := menuXPos() + menuW - menuPad - bootW - 4
				bootHover := mx >= bootX && mx < bootX+bootW && my >= ry && my < ry+menuRowH
				bootBg := rl.Color{R: 50, G: 30, B: 30, A: 180}
				if bootHover {
					bootBg = colorGmRedBg
				}
				rl.DrawRectangle(bootX, ry+2, bootW, menuRowH-4, bootBg)
				bLbl := "Boot"
				bLW := c.measureText(bLbl, 11)
				c.drawText(bLbl, bootX+(bootW-bLW)/2, ry+5, 11, red)

				if clicked && bootHover {
					c.engine.SendBootGroupMember(m.ObjectID)
					// Remove locally — server may not echo RemoveGroupMember back to booter
					gs.GroupMembers = append(gs.GroupMembers[:i], gs.GroupMembers[i+1:]...)
					if len(gs.GroupMembers) <= 1 {
						gs.GroupActive = false
						gs.GroupID = 0
						gs.GroupMembers = nil
					}
				}
			}
		}
	} else {
		c.drawText("Not in a group", menuXPos()+menuPad, y, 14, dim)
	}
}

// handleGroupInviteInput processes keyboard input for the group invite name field.
func (c *Client) handleGroupInviteInput() {
	if rl.IsKeyPressed(rl.KeyEscape) {
		c.groupTyping = false
		return
	}

	if rl.IsKeyPressed(rl.KeyEnter) {
		if c.groupInviteInput != "" {
			c.sendGroupInviteByName(c.groupInviteInput)
			c.groupInviteInput = ""
		}
		c.groupTyping = false
		return
	}

	if rl.IsKeyPressed(rl.KeyBackspace) || rl.IsKeyPressedRepeat(rl.KeyBackspace) {
		if len(c.groupInviteInput) > 0 {
			c.groupInviteInput = c.groupInviteInput[:len(c.groupInviteInput)-1]
		}
		return
	}

	for {
		ch := rl.GetCharPressed()
		if ch == 0 {
			break
		}
		if len(c.groupInviteInput) < 30 {
			c.groupInviteInput += string(rune(ch))
		}
	}
}

// sendGroupInviteByName looks up a player name in nearby entities and sends a group invite.
func (c *Client) sendGroupInviteByName(name string) {
	gs := c.engine.State
	// Search entities for matching player name (case-insensitive)
	nameLower := strings.ToLower(name)
	for _, ent := range gs.Entities {
		if ent.IsPlayer && strings.ToLower(ent.Name) == nameLower {
			c.engine.SendGroupInvite(ent.ObjectID, ent.Name)
			gs.ChatLog = append(gs.ChatLog, game.ChatMessage{
				Text:    fmt.Sprintf("Invited %s to group.", ent.Name),
				Channel: game.ChatSystem,
				ColorR:  255, ColorG: 255, ColorB: 100,
			})
			return
		}
	}
	gs.ChatLog = append(gs.ChatLog, game.ChatMessage{
		Text:    fmt.Sprintf("Player '%s' not found nearby.", name),
		Channel: game.ChatError,
		ColorR:  255, ColorG: 0, ColorB: 0,
	})
}

// --- Spell Popup Methods ---

// findSpellIndex returns the index of spellID in gs.ParsedSpells (unsorted order),
// which is what SendAddSkillToBar expects as the spellListIndex parameter.
func findSpellIndex(gs *game.GameState, spellID int64) int {
	for i := range gs.ParsedSpells {
		if gs.ParsedSpells[i].SpellID == spellID {
			return i
		}
	}
	return -1
}

// findSpellByID returns a pointer to the ParsedSpell with the given SpellID, or nil.
func findSpellByID(gs *game.GameState, spellID int64) *game.ParsedSpell {
	for i := range gs.ParsedSpells {
		if gs.ParsedSpells[i].SpellID == spellID {
			return &gs.ParsedSpells[i]
		}
	}
	return nil
}

// drawSpellContextMenu draws a small "Memorize / Info" popup at the click position.
func (c *Client) drawSpellContextMenu() {
	gold := colorGold
	white := colorWhite
	hover := colorGoldHover
	border := colorBorder
	bg := colorPopupBg

	popW := int32(100)
	popH := int32(48)
	rowH := int32(22)

	// Clamp popup position to screen
	px := c.spellCtxX
	py := c.spellCtxY
	if px+popW > screenWidth {
		px = screenWidth - popW
	}
	if py+popH > screenHeight {
		py = screenHeight - popH
	}

	// Background + border
	rl.DrawRectangle(px, py, popW, popH, bg)
	rl.DrawRectangleLines(px, py, popW, popH, border)

	mx, my := rl.GetMouseX(), rl.GetMouseY()

	// Row 0: Memorize
	isMemHover := mx >= px && mx < px+popW && my >= py && my < py+rowH
	isMemSelected := c.spellCtxRow == 0
	memClr := white
	if isMemHover || isMemSelected {
		memClr = hover
		rl.DrawRectangle(px+1, py+1, popW-2, rowH-1, colorRowHover)
	}
	c.drawText("Memorize", px+8, py+3, 14, memClr)

	// Row 1: Info
	infoY := py + rowH + 2
	isInfoHover := mx >= px && mx < px+popW && my >= infoY && my < infoY+rowH
	isInfoSelected := c.spellCtxRow == 1
	infoClr := white
	if isInfoHover || isInfoSelected {
		infoClr = hover
		rl.DrawRectangle(px+1, infoY, popW-2, rowH-1, colorRowHover)
	}
	c.drawText("Info", px+8, infoY+3, 14, infoClr)

	_ = gold

	// Click handling
	if rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
		if isMemHover {
			c.spellSlotPicker = true
		} else if isInfoHover {
			c.spellInfoPopup = true
		} else {
			// Click outside dismisses
			c.spellCtxSpellID = -1
		}
	}
}

// drawSpellSlotPicker draws a centered "Choose Slot: 1 2 3 4 5 6 7 8 9 0" overlay.
func (c *Client) drawSpellSlotPicker() {
	gs := c.engine.State
	if gs == nil {
		return
	}

	gold := colorGold
	white := colorWhite
	hover := colorGoldHover
	border := colorBorder
	bg := colorPopupBg
	btnBg := colorBtnBg
	btnHover := colorBtnHover

	popW := int32(300)
	popH := int32(60)
	px := (screenWidth - popW) / 2
	py := (screenHeight - popH) / 2

	// Background + border
	rl.DrawRectangle(px, py, popW, popH, bg)
	rl.DrawRectangleLines(px, py, popW, popH, border)

	// Title
	c.drawText("Choose Slot:", px+8, py+4, 14, gold)

	// 10 slot buttons in a row
	btnW := int32(24)
	btnH := int32(22)
	btnY := py + 28
	btnStartX := px + 10
	mx, my := rl.GetMouseX(), rl.GetMouseY()

	// Keys displayed: 1 2 3 4 5 6 7 8 9 0
	keys := [10]string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "0"}

	clicked := false
	for k := 0; k < 10; k++ {
		bx := btnStartX + int32(k)*(btnW+4)
		isBtnHover := mx >= bx && mx < bx+btnW && my >= btnY && my < btnY+btnH

		isGpSelected := c.spellSlotIdx == k
		bbg := btnBg
		if isBtnHover || isGpSelected {
			bbg = btnHover
		}
		rl.DrawRectangle(bx, btnY, btnW, btnH, bbg)
		if isGpSelected {
			rl.DrawRectangleLines(bx, btnY, btnW, btnH, gold)
		} else {
			rl.DrawRectangleLines(bx, btnY, btnW, btnH, border)
		}

		clr := white
		if isBtnHover || isGpSelected {
			clr = hover
		}
		kw := c.measureText(keys[k], 14)
		c.drawText(keys[k], bx+(btnW-kw)/2, btnY+3, 14, clr)

		if isBtnHover && rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
			// Map key to slot: 1→0, 2→1, ..., 9→8, 0→9
			slot := k
			if k == 9 {
				slot = 9
			}
			idx := findSpellIndex(gs, c.spellCtxSpellID)
			if idx >= 0 {
				c.engine.SendAddSkillToBar(1, byte(idx), byte(slot))
				// Update local hotbar state
				gs.SetHotbarSlot(slot, &gs.ParsedSpells[idx])
			}
			c.spellCtxSpellID = -1
			c.spellSlotPicker = false
			clicked = true
		}
	}

	// Click outside or Escape dismisses
	if !clicked && rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
		inPopup := mx >= px && mx < px+popW && my >= py && my < py+popH
		if !inPopup {
			c.spellCtxSpellID = -1
			c.spellSlotPicker = false
		}
	}
}

// drawSpellInfoPopup draws a centered popup with spell details.
func (c *Client) drawSpellInfoPopup() {
	gs := c.engine.State
	if gs == nil {
		return
	}

	sp := findSpellByID(gs, c.spellCtxSpellID)
	if sp == nil {
		c.spellCtxSpellID = -1
		c.spellInfoPopup = false
		return
	}

	gold := colorGold
	white := colorWhite
	dim := colorDim
	valClr := colorStatVal
	border := colorBorder
	bg := colorPopupBg
	sepClr := colorSep

	popW := int32(340)
	popH := int32(220)
	px := (screenWidth - popW) / 2
	py := (screenHeight - popH) / 2

	// Background + border
	rl.DrawRectangle(px, py, popW, popH, bg)
	rl.DrawRectangleLines(px, py, popW, popH, border)

	pad := int32(12)
	lineH := int32(16)
	y := py + pad

	// Spell name (gold, 16pt)
	c.drawText(sp.Name, px+pad, y, 16, gold)
	y += lineH + 4

	// Description (white, wrapped manually to ~40 chars per line)
	desc := sp.Desc
	if desc != "" {
		maxLineLen := 45
		for len(desc) > 0 {
			line := desc
			if len(line) > maxLineLen {
				// Try to break at a space
				cut := maxLineLen
				for cut > 0 && line[cut] != ' ' {
					cut--
				}
				if cut == 0 {
					cut = maxLineLen
				}
				line = desc[:cut]
				desc = desc[cut:]
				if len(desc) > 0 && desc[0] == ' ' {
					desc = desc[1:]
				}
			} else {
				desc = ""
			}
			c.drawText(line, px+pad, y, 13, white)
			y += lineH
		}
	} else {
		c.drawText("No description", px+pad, y, 13, dim)
		y += lineH
	}
	y += 4

	// Separator
	rl.DrawLine(px+pad, y, px+popW-pad, y, sepClr)
	y += 6

	// Stats: 2-column layout
	colW := (popW - pad*3) / 2

	// Power / Cast Time
	c.drawText(fmt.Sprintf("Power: %d", sp.Power), px+pad, y, 13, valClr)
	castSec := float64(sp.CastTime) / 1000.0
	c.drawText(fmt.Sprintf("Cast: %.1fs", castSec), px+pad+colW+pad, y, 13, valClr)
	y += lineH

	// Range / Recast
	c.drawText(fmt.Sprintf("Range: %d", sp.Range), px+pad, y, 13, valClr)
	recastSec := float64(sp.Recast) / 1000.0
	c.drawText(fmt.Sprintf("Recast: %.1fs", recastSec), px+pad+colW+pad, y, 13, valClr)
	y += lineH

	// Level / Hotbar status
	c.drawText(fmt.Sprintf("Level: %d", sp.AbilityLevel), px+pad, y, 13, valClr)
	if sp.OnHotBar != 0 && sp.WhereOnHotBar >= 0 && sp.WhereOnHotBar < 10 {
		key := sp.WhereOnHotBar + 1
		if key == 10 {
			key = 0
		}
		c.drawText(fmt.Sprintf("Hotbar: slot %d", key), px+pad+colW+pad, y, 13, valClr)
	} else {
		c.drawText("Hotbar: -", px+pad+colW+pad, y, 13, dim)
	}
	y += lineH + 6

	// Close hint
	c.drawText("Click to close", px+pad, py+popH-pad-lineH, 11, dim)

	// Click anywhere or Escape dismisses
	if rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
		c.spellCtxSpellID = -1
		c.spellInfoPopup = false
	}
}

// --- Merchant Window ---

const (
	merchW    = int32(520) // panel width
	merchH    = int32(440) // panel height
	merchPad  = int32(12)  // inner padding
	merchRowH = int32(24)  // item row height
)

func (c *Client) handleMerchantInput() {
	gs := c.engine.State
	if gs == nil || !gs.MerchantOpen {
		return
	}

	// Escape or Triangle or Circle closes merchant
	if rl.IsKeyPressed(rl.KeyEscape) ||
		(gpAvailable() && (gpPressed(rl.GamepadButtonRightFaceUp) || gpPressed(rl.GamepadButtonRightFaceRight))) {
		gs.MerchantOpen = false
		gs.MerchantSelected = -1
		c.merchantScroll = 0
		return
	}

	maxVisible := c.merchantVisibleRows()
	maxScroll := len(gs.MerchantItems) - maxVisible
	if maxScroll < 0 {
		maxScroll = 0
	}

	// D-pad up/down or keyboard up/down: navigate items
	if rl.IsKeyPressed(rl.KeyUp) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceUp)) {
		if gs.MerchantSelected > 0 {
			gs.MerchantSelected--
		} else if gs.MerchantSelected < 0 && len(gs.MerchantItems) > 0 {
			gs.MerchantSelected = 0
		}
		// Auto-scroll to keep selection visible
		if gs.MerchantSelected < c.merchantScroll {
			c.merchantScroll = gs.MerchantSelected
		}
	}
	if rl.IsKeyPressed(rl.KeyDown) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceDown)) {
		if gs.MerchantSelected < len(gs.MerchantItems)-1 {
			gs.MerchantSelected++
		} else if gs.MerchantSelected < 0 && len(gs.MerchantItems) > 0 {
			gs.MerchantSelected = 0
		}
		// Auto-scroll to keep selection visible
		if gs.MerchantSelected >= c.merchantScroll+maxVisible {
			c.merchantScroll = gs.MerchantSelected - maxVisible + 1
		}
	}

	// Cross/A or Enter: buy selected item
	if rl.IsKeyPressed(rl.KeyEnter) || (gpAvailable() && gpPressed(rl.GamepadButtonRightFaceDown)) {
		if gs.MerchantSelected >= 0 && gs.MerchantSelected < len(gs.MerchantItems) {
			item := &gs.MerchantItems[gs.MerchantSelected]
			if item.ItemCost <= gs.Tunar {
				c.engine.SendMerchantBuy(gs.MerchantSelected, 1)
			}
		}
	}

	// Mouse wheel scrolls item list
	wheel := int(rl.GetMouseWheelMove())
	if wheel != 0 {
		c.merchantScroll -= wheel
		if c.merchantScroll < 0 {
			c.merchantScroll = 0
		}
		if c.merchantScroll > maxScroll {
			c.merchantScroll = maxScroll
		}
	}

	// Clamp scroll
	if c.merchantScroll < 0 {
		c.merchantScroll = 0
	}
	if c.merchantScroll > maxScroll {
		c.merchantScroll = maxScroll
	}
}

func (c *Client) merchantVisibleRows() int {
	// List area height: total height minus header, tunar line, footer button area
	listH := merchH - 36 - 20 - merchRowH - 50 // header + tunar + column hdr + buy button area
	return int(listH / merchRowH)
}

func (c *Client) drawMenuMusic(startY, contentH int32) {
	mx := rl.GetMouseX()
	my := rl.GetMouseY()

	// HUD toggle button at top
	toggleX := menuXPos() + menuPad
	toggleW := menuW - menuPad*2
	toggleH := int32(22)
	toggleLabel := "Show Music Player HUD"
	if c.musicHudOn {
		toggleLabel = "Hide Music Player HUD"
	}
	isToggleHover := mx >= toggleX && mx < toggleX+toggleW && my >= startY && my < startY+toggleH
	toggleBg := colorBtnBg
	if isToggleHover {
		toggleBg = colorBtnHover
	}
	rl.DrawRectangle(toggleX, startY, toggleW, toggleH, toggleBg)
	rl.DrawRectangleLines(toggleX, startY, toggleW, toggleH, colorSep)
	toggleClr := rl.Color{R: 180, G: 180, B: 180, A: 255}
	if c.musicHudOn {
		toggleClr = rl.Color{R: 100, G: 255, B: 150, A: 255}
	}
	if isToggleHover {
		toggleClr = colorGoldHover
	}
	lw := c.measureText(toggleLabel, 14)
	c.drawText(toggleLabel, toggleX+(toggleW-lw)/2, startY+4, 14, toggleClr)
	if isToggleHover && rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
		c.musicHudOn = !c.musicHudOn
	}

	startY += toggleH + 4
	contentH -= toggleH + 4

	tracks := titleBGMTracks
	visibleRows := int(contentH / menuRowH)
	if visibleRows < 1 {
		visibleRows = 1
	}

	// Clamp scroll
	maxScroll := len(tracks) - visibleRows
	if maxScroll < 0 {
		maxScroll = 0
	}
	if c.menuScroll > maxScroll {
		c.menuScroll = maxScroll
	}
	if c.menuScroll < 0 {
		c.menuScroll = 0
	}

	// Clamp row
	if c.menuRow >= visibleRows {
		c.menuRow = visibleRows - 1
	}
	if c.menuRow < 0 {
		c.menuRow = 0
	}

	// Scroll if row goes off screen
	idx := c.menuRow + c.menuScroll
	if idx >= len(tracks) {
		c.menuRow = len(tracks) - c.menuScroll - 1
		if c.menuRow < 0 {
			c.menuRow = 0
		}
	}

	// Get currently playing track name
	var nowPlaying string
	if c.audioSys != nil && c.audioSys.BGMActive() {
		nowPlaying = c.audioSys.BGMPath()
	}

	for i := 0; i < visibleRows && i+c.menuScroll < len(tracks); i++ {
		trackIdx := i + c.menuScroll
		track := tracks[trackIdx]
		y := startY + int32(i)*menuRowH
		btnX := menuXPos() + menuPad
		btnW := menuW - menuPad*2

		isHover := mx >= btnX && mx < btnX+btnW && my >= y && my < y+menuRowH
		isSelected := c.menuRow == i

		// Row background
		bg := colorRowBg1
		if trackIdx%2 == 1 {
			bg = colorRowBg2
		}
		if isSelected || isHover {
			bg = colorBtnHover
		}
		rl.DrawRectangle(btnX, y, btnW, menuRowH, bg)

		// Playing indicator
		clr := rl.Color{R: 200, G: 200, B: 200, A: 255}
		if track == nowPlaying {
			clr = rl.Color{R: 100, G: 255, B: 150, A: 255}
		}
		if isHover || isSelected {
			clr = colorGoldHover
			if track == nowPlaying {
				clr = rl.Color{R: 100, G: 255, B: 150, A: 255}
			}
		}

		label := track
		if track == nowPlaying {
			label = "> " + track
		}
		c.drawText(label, btnX+8, y+3, 14, clr)

		// Mouse click plays track
		if isHover && rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
			c.audioSys.StopBGM()
			c.playBGMFromISO(track)
			c.musicPlayerOn = true
			c.musicTrackIdx = trackIdx
		}
	}

	// Scroll indicators
	if c.menuScroll > 0 {
		c.drawText("^ more ^", menuXPos()+menuW/2-30, startY-14, 12, colorDim)
	}
	if c.menuScroll+visibleRows < len(tracks) {
		c.drawText("v more v", menuXPos()+menuW/2-30, startY+int32(visibleRows)*menuRowH+2, 12, colorDim)
	}
}

func (c *Client) drawMenuFaction(startY, contentH int32) {
	gs := c.engine.State
	if gs == nil {
		return
	}

	gold := colorGold
	white := colorWhite
	dim := colorDim
	lineH := int32(18)

	y := startY

	// Request factions if not loaded
	if !gs.FactionLoaded && len(gs.Factions) == 0 {
		gs.RequestFactions()
		c.drawText("Loading factions...", menuXPos()+menuPad, y, 14, dim)
		return
	}

	if len(gs.Factions) == 0 {
		c.drawText("No faction data", menuXPos()+menuPad, y, 14, dim)
		return
	}

	// Header
	c.drawText(fmt.Sprintf("Faction Standings (%d)", len(gs.Factions)), menuXPos()+menuPad, y, 16, gold)
	y += lineH + 4

	// Scrollable list
	maxVisible := int(contentH-lineH-8) / int(lineH)
	if maxVisible < 1 {
		maxVisible = 1
	}

	// Mouse wheel scroll
	wheel := int(rl.GetMouseWheelMove())
	if wheel != 0 {
		mx := rl.GetMouseX()
		my := rl.GetMouseY()
		if mx >= menuXPos() && mx <= menuXPos()+menuW && my >= startY && my <= startY+contentH {
			c.menuScroll -= wheel
		}
	}
	if c.menuScroll < 0 {
		c.menuScroll = 0
	}
	if c.menuScroll > len(gs.Factions)-maxVisible {
		c.menuScroll = len(gs.Factions) - maxVisible
	}
	if c.menuScroll < 0 {
		c.menuScroll = 0
	}

	colW := menuW - menuPad*2

	for i := c.menuScroll; i < c.menuScroll+maxVisible && i < len(gs.Factions); i++ {
		f := gs.Factions[i]

		// Standing color
		clr := white
		if f.Standing >= 100 {
			clr = rl.Color{R: 100, G: 200, B: 255, A: 255} // ally blue
		} else if f.Standing > 0 {
			clr = rl.Color{R: 100, G: 255, B: 100, A: 255} // friendly green
		} else if f.Standing == 0 {
			clr = dim // neutral gray
		} else if f.Standing > -100 {
			clr = rl.Color{R: 255, G: 255, B: 100, A: 255} // slightly hostile yellow
		} else if f.Standing > -500 {
			clr = rl.Color{R: 255, G: 150, B: 50, A: 255} // hostile orange
		} else {
			clr = rl.Color{R: 255, G: 80, B: 80, A: 255} // KOS red
		}

		// Name
		c.drawText(f.Name, menuXPos()+menuPad, y, 12, clr)

		// Standing value (right-aligned)
		standStr := fmt.Sprintf("%d", f.Standing)
		sw := c.measureText(standStr, 12)
		c.drawText(standStr, menuXPos()+menuPad+colW-sw, y, 12, clr)

		y += lineH
	}

	// Scroll indicator
	if len(gs.Factions) > maxVisible {
		indicator := fmt.Sprintf("%d-%d of %d", c.menuScroll+1,
			min(c.menuScroll+maxVisible, len(gs.Factions)), len(gs.Factions))
		iw := c.measureText(indicator, 11)
		c.drawText(indicator, menuXPos()+menuW-menuPad-iw, startY+contentH-14, 11, dim)
	}
}

func (c *Client) drawMerchantWindow() {
	gs := c.engine.State
	if gs == nil || !gs.MerchantOpen {
		return
	}

	c.handleMerchantInput()

	mx, my := rl.GetMouseX(), rl.GetMouseY()

	// Center the window
	px := (screenWidth - merchW) / 2
	py := (screenHeight - merchH) / 2

	// Background panel
	c.drawPanelTextured(px, py, merchW, merchH, ui.FrameLine, ui.FillBlack)

	y := py + merchPad

	// Header: merchant name
	title := gs.MerchantName
	if title == "" {
		title = "Merchant"
	}
	tw := c.measureText(title, 16)
	c.drawText(title, px+(merchW-tw)/2, y, 16, colorGoldBright)
	y += 24

	// Tunar display
	tunarStr := fmt.Sprintf("Your Tunar: %d", gs.Tunar)
	c.drawText(tunarStr, px+merchPad, y, 12, colorGold)
	y += 18

	// Separator
	rl.DrawLine(px+merchPad, y, px+merchW-merchPad, y, colorSep)
	y += 4

	// Column headers
	c.drawText("Item", px+merchPad+4, y, 12, colorDim)
	c.drawText("Lvl", px+merchW-merchPad-130, y, 12, colorDim)
	c.drawText("Cost", px+merchW-merchPad-70, y, 12, colorDim)
	y += merchRowH

	// Item list
	listTop := y
	maxVisible := c.merchantVisibleRows()

	maxScroll := len(gs.MerchantItems) - maxVisible
	if maxScroll < 0 {
		maxScroll = 0
	}
	if c.merchantScroll > maxScroll {
		c.merchantScroll = maxScroll
	}

	if len(gs.MerchantItems) == 0 {
		c.drawText("No items available", px+merchPad+4, y+4, 13, colorDim)
	}

	for i := c.merchantScroll; i < len(gs.MerchantItems); i++ {
		row := i - c.merchantScroll
		ry := listTop + int32(row)*merchRowH
		if ry+merchRowH > py+merchH-50 { // leave room for buy button
			break
		}

		rowX := px + merchPad
		rowW := merchW - merchPad*2
		isHover := mx >= rowX && mx < rowX+rowW && my >= ry && my < ry+merchRowH
		isSelected := gs.MerchantSelected == i

		// Row background
		bg := colorRowBg1
		if row%2 == 1 {
			bg = colorRowBg2
		}
		if isSelected {
			bg = rl.Color{R: 50, G: 45, B: 70, A: 255}
		}
		if isHover {
			bg = colorRowHover
		}
		rl.DrawRectangle(rowX, ry, rowW, merchRowH, bg)

		item := gs.MerchantItems[i]

		// Item name
		name := item.Name
		if len(name) > 30 {
			name = name[:30] + "..."
		}
		nameColor := colorWhite
		if isSelected {
			nameColor = colorGoldBright
		}
		c.drawText(name, rowX+4, ry+4, 13, nameColor)

		// Level requirement
		if item.LevelReq > 0 {
			lvlStr := fmt.Sprintf("%d", item.LevelReq)
			c.drawText(lvlStr, px+merchW-merchPad-130, ry+4, 13, colorDim)
		}

		// Cost
		costStr := fmt.Sprintf("%d", item.ItemCost)
		costColor := colorGold
		if item.ItemCost > gs.Tunar {
			costColor = rl.Color{R: 200, G: 80, B: 80, A: 255} // red if can't afford
		}
		c.drawText(costStr, px+merchW-merchPad-70, ry+4, 13, costColor)

		// Click to select
		if isHover && rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
			gs.MerchantSelected = i
		}
	}

	// Bottom area: Buy button + selected item info
	btnY := py + merchH - 42
	btnW := int32(100)
	btnH := int32(30)
	btnX := px + merchW - merchPad - btnW

	canBuy := gs.MerchantSelected >= 0 && gs.MerchantSelected < len(gs.MerchantItems)
	var selectedItem *game.ParsedItem
	if canBuy {
		selectedItem = &gs.MerchantItems[gs.MerchantSelected]
		canBuy = selectedItem.ItemCost <= gs.Tunar
	}

	// Selected item description (bottom-left)
	if selectedItem != nil && selectedItem.Desc != "" {
		desc := selectedItem.Desc
		if len(desc) > 50 {
			desc = desc[:50] + "..."
		}
		c.drawText(desc, px+merchPad, btnY+8, 11, colorDim)
	}

	// Buy button
	btnBg := colorBtnBg
	btnBorder := colorBtnBorder
	btnText := colorDim
	isBtnHover := mx >= btnX && mx < btnX+btnW && my >= btnY && my < btnY+btnH

	if canBuy {
		btnText = colorGold
		if isBtnHover {
			btnBg = colorBtnHover
			btnText = colorGoldBright
		}
	}
	drawPanel(btnX, btnY, btnW, btnH, btnBg, btnBorder)
	buyLabel := "Buy"
	blw := c.measureText(buyLabel, 14)
	c.drawText(buyLabel, btnX+(btnW-blw)/2, btnY+8, 14, btnText)

	// Handle buy click
	if canBuy && isBtnHover && rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
		c.engine.SendMerchantBuy(gs.MerchantSelected, 1)
	}

	// Close button (top-right X)
	closeX := px + merchW - 24
	closeY := py + 4
	isCloseHover := mx >= closeX && mx < closeX+20 && my >= closeY && my < closeY+20
	closeColor := colorDim
	if isCloseHover {
		closeColor = colorGoldBright
	}
	c.drawText("X", closeX+4, closeY+2, 14, closeColor)
	if isCloseHover && rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
		gs.MerchantOpen = false
		gs.MerchantSelected = -1
		c.merchantScroll = 0
	}

	// Scroll indicator
	if len(gs.MerchantItems) > maxVisible {
		scrollInfo := fmt.Sprintf("%d-%d of %d",
			c.merchantScroll+1,
			min(c.merchantScroll+maxVisible, len(gs.MerchantItems)),
			len(gs.MerchantItems))
		siw := c.measureText(scrollInfo, 11)
		c.drawText(scrollInfo, px+(merchW-siw)/2, py+merchH-14, 11, colorDim)
	}
}

// --- Item Context Menu ---

func (c *Client) itemContextOptions() []string {
	if c.itemContextTab == 0 {
		return []string{"Unequip", "Cancel"}
	}
	// Only show Equip for items with a valid equipment slot
	if c.itemContextSlot > 0 {
		return []string{"Equip", "Cancel"}
	}
	return []string{"Cancel"}
}

func (c *Client) drawItemContextMenu() {
	opts := c.itemContextOptions()
	mx, my := rl.GetMouseX(), rl.GetMouseY()

	popW := int32(120)
	popH := int32(len(opts))*20 + 8
	px := c.itemContextX
	py := c.itemContextY

	// Keep on screen
	if px+popW > screenWidth {
		px = screenWidth - popW - 4
	}
	if py+popH > screenHeight {
		py = screenHeight - popH - 4
	}

	// Background
	rl.DrawRectangle(px, py, popW, popH, rl.Color{R: 20, G: 18, B: 30, A: 240})
	rl.DrawRectangleLines(px, py, popW, popH, colorGold)

	// Skip input for a few frames after opening to prevent the same
	// keypress from immediately executing an option.
	if c.itemContextDelay > 0 {
		c.itemContextDelay--
		return
	}

	// Handle input
	if rl.IsKeyPressed(rl.KeyUp) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceUp)) {
		if c.itemContextRow > 0 {
			c.itemContextRow--
		}
	}
	if rl.IsKeyPressed(rl.KeyDown) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceDown)) {
		if c.itemContextRow < len(opts)-1 {
			c.itemContextRow++
		}
	}

	// Escape / Circle cancels
	if rl.IsKeyPressed(rl.KeyEscape) || (gpAvailable() && gpPressed(rl.GamepadButtonRightFaceRight)) {
		c.itemContextOpen = false
		return
	}

	// Draw options
	for i, opt := range opts {
		oy := py + 4 + int32(i)*20
		isHover := mx >= px && mx < px+popW && my >= oy && my < oy+20
		isSelected := i == c.itemContextRow

		if isHover || isSelected {
			rl.DrawRectangle(px+2, oy, popW-4, 18, rl.Color{R: 60, G: 50, B: 90, A: 200})
		}

		clr := colorWhite
		if isHover || isSelected {
			clr = colorGoldBright
		}
		c.drawText(opt, px+10, oy+2, 13, clr)

		// Mouse click
		if isHover && rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
			c.itemContextRow = i
			c.executeItemContext(i)
			return
		}
	}

	// Enter/A confirms
	if rl.IsKeyPressed(rl.KeyEnter) || (gpAvailable() && gpPressed(rl.GamepadButtonRightFaceDown)) {
		c.executeItemContext(c.itemContextRow)
	}
}

func (c *Client) executeItemContext(optIdx int) {
	opts := c.itemContextOptions()
	if optIdx < 0 || optIdx >= len(opts) {
		c.itemContextOpen = false
		return
	}

	action := opts[optIdx]
	c.itemContextOpen = false

	switch action {
	case "Equip":
		c.engine.SendEquipItem(c.itemContextInvSlot)
	case "Unequip":
		c.engine.SendUnequipItem(c.itemContextInvSlot)
	case "Cancel":
		// already closed
	}
}

// drawItemDetailColumn renders item stats in a right-side column panel.
// Auto-updates as the player scrolls through items.
func (c *Client) drawItemDetailColumn(item game.ParsedItem, x, y, w, h int32) {
	dim := colorDim
	white := colorWhite
	gold := colorGold

	// Background panel
	rl.DrawRectangle(x, y, w, h, rl.Color{R: 15, G: 15, B: 25, A: 230})
	rl.DrawRectangleLines(x, y, w, h, rl.Color{R: 60, G: 55, B: 80, A: 255})

	pad := int32(8)
	cx := x + pad
	cy := y + pad
	lineH := int32(16)

	// Item name (gold, bold)
	name := item.Name
	if len(name) > 22 {
		name = name[:22] + "..."
	}
	c.drawText(name, cx, cy, 14, colorGoldBright)
	cy += lineH + 4

	// Separator
	rl.DrawLine(cx, cy, x+w-pad, cy, rl.Color{R: 60, G: 55, B: 80, A: 255})
	cy += 4

	// Slot
	if item.Slot > 0 {
		c.drawText(equipSlotName(item.Slot), cx, cy, 11, dim)
		cy += lineH
	}

	// Level
	if item.LevelReq > 0 {
		c.drawText(fmt.Sprintf("Lvl %d", item.LevelReq), cx, cy, 11, dim)
		cy += lineH
	}

	// Value
	if item.ItemCost > 0 {
		c.drawText(fmt.Sprintf("%s tunar", formatTunar(item.ItemCost)), cx, cy, 11, gold)
		cy += lineH
	}

	// Durability
	if item.MaxHP > 0 {
		c.drawText(fmt.Sprintf("HP %d/%d", item.RemainingHP, item.MaxHP), cx, cy, 11, dim)
		cy += lineH
	}

	// Attack
	if item.AttackType > 0 || item.WeaponDamage > 0 {
		c.drawText(fmt.Sprintf("Dmg %d", item.WeaponDamage), cx, cy, 11, white)
		cy += lineH
	}

	// Stats (sorted by index to prevent flickering)
	if len(item.Stats) > 0 {
		cy += 2
		statKeys := make([]int, 0, len(item.Stats))
		for k := range item.Stats {
			statKeys = append(statKeys, k)
		}
		sort.Ints(statKeys)
		for _, idx := range statKeys {
			val := item.Stats[idx]
			if val == 0 {
				continue
			}
			sign := "+"
			clr := rl.Color{R: 100, G: 255, B: 150, A: 255}
			if val < 0 {
				sign = ""
				clr = rl.Color{R: 255, G: 100, B: 100, A: 255}
			}
			c.drawText(fmt.Sprintf("%s%d %s", sign, val, statName(idx)), cx, cy, 11, clr)
			cy += lineH
			if cy > y+h-pad-lineH {
				break // don't overflow panel
			}
		}
	}

	// Description (word-wrapped at bottom)
	if item.Desc != "" && cy < y+h-pad-lineH*2 {
		cy += 4
		rl.DrawLine(cx, cy, x+w-pad, cy, rl.Color{R: 40, G: 38, B: 55, A: 255})
		cy += 4
		// Simple word wrap
		words := strings.Fields(item.Desc)
		line := ""
		maxLineW := int(w - pad*2)
		for _, word := range words {
			test := line + " " + word
			if line == "" {
				test = word
			}
			if int(c.measureText(test, 10)) > maxLineW {
				c.drawText(line, cx, cy, 10, dim)
				cy += lineH - 2
				line = word
				if cy > y+h-pad {
					break
				}
			} else {
				line = test
			}
		}
		if line != "" && cy <= y+h-pad {
			c.drawText(line, cx, cy, 10, dim)
		}
	}
}

// buildEquippedSet returns a set of ParsedItems indices that are currently equipped,
// derived from the player's state channel equipment bytes (always accurate).
func (c *Client) buildEquippedSet(gs *game.GameState) map[int]bool {
	set := make(map[int]bool)
	p := &gs.Player

	// For each item, check if it matches what the state channel says is equipped.
	// Use EquipLocation from the original MemoryDump (set once at world entry).
	for i, item := range gs.ParsedItems {
		if item.EquipLocation < 0 {
			continue // not equipped per original DSP data
		}

		// Cross-validate with state channel: is this slot still occupied?
		equipped := false
		switch item.Slot {
		case 1: // Helm
			equipped = p.Helm > 0
		case 2: // Robe
			equipped = p.Robe != 0xFF && p.Robe != 0
		case 5: // Chest
			equipped = p.Chest > 0
		case 7: // Bracer
			equipped = p.Bracer > 0
		case 10: // Legs
			equipped = p.Legs > 0
		case 11: // Boots
			equipped = p.Boots > 0
		case 19: // Gloves
			equipped = p.Gloves > 0
		case 12: // Primary weapon
			equipped = p.PrimaryWeapon != 0
		case 14: // Secondary weapon
			equipped = p.SecondaryWeapon != 0
		case 13: // Shield
			equipped = p.Shield != 0
		case 3, 4, 8, 9, 6, 18: // Earring, Neck, Ring, Belt, Bracelet, Held — no state channel byte, trust EquipLocation
			equipped = true
		default:
			equipped = true // unknown slot type, trust EquipLocation
		}

		if equipped {
			set[i] = true
		}
	}
	return set
}

func statName(idx int) string {
	names := map[int]string{
		0: "STR", 1: "STA", 2: "AGI", 3: "DEX", 4: "WIS", 5: "INT", 6: "CHA",
		8: "HP", 10: "Power", 12: "PoT", 13: "HoT", 14: "AC",
		22: "Poison Resist", 23: "Disease Resist", 24: "Fire Resist",
		25: "Cold Resist", 26: "Lightning Resist", 27: "Arcane Resist",
	}
	if n, ok := names[idx]; ok {
		return n
	}
	return fmt.Sprintf("Stat%d", idx)
}

// --- Blacksmith/Repair Window ---

func (c *Client) handleBlacksmithInput() {
	gs := c.engine.State
	if gs == nil || !gs.BlacksmithOpen {
		return
	}

	// Escape / Triangle / Circle closes
	if rl.IsKeyPressed(rl.KeyEscape) ||
		(gpAvailable() && (gpPressed(rl.GamepadButtonRightFaceUp) || gpPressed(rl.GamepadButtonRightFaceRight))) {
		c.engine.SendCloseBlacksmith()
		c.blacksmithScroll = 0
		c.blacksmithSel = -1
		return
	}

	// Build repairable items list (items with RemainingHP < MaxHP).
	items := c.repairableItems()

	maxVisible := 10
	maxScroll := len(items) - maxVisible
	if maxScroll < 0 {
		maxScroll = 0
	}

	// D-pad / arrow navigation
	if rl.IsKeyPressed(rl.KeyUp) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceUp)) {
		if c.blacksmithSel > 0 {
			c.blacksmithSel--
		} else if c.blacksmithSel < 0 && len(items) > 0 {
			c.blacksmithSel = 0
		}
		if c.blacksmithSel < c.blacksmithScroll {
			c.blacksmithScroll = c.blacksmithSel
		}
	}
	if rl.IsKeyPressed(rl.KeyDown) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceDown)) {
		if c.blacksmithSel < len(items)-1 {
			c.blacksmithSel++
		} else if c.blacksmithSel < 0 && len(items) > 0 {
			c.blacksmithSel = 0
		}
		if c.blacksmithSel >= c.blacksmithScroll+maxVisible {
			c.blacksmithScroll = c.blacksmithSel - maxVisible + 1
		}
	}

	// Cross/A or Enter: repair selected item
	if rl.IsKeyPressed(rl.KeyEnter) || (gpAvailable() && gpPressed(rl.GamepadButtonRightFaceDown)) {
		if c.blacksmithSel >= 0 && c.blacksmithSel < len(items) {
			item := items[c.blacksmithSel]
			c.engine.SendRepairItem(item.InvSlot, uint32(gs.BlacksmithRepairCost))
		}
	}

	// Scroll
	wheel := int(rl.GetMouseWheelMove())
	if wheel != 0 {
		c.blacksmithScroll -= wheel
		if c.blacksmithScroll < 0 {
			c.blacksmithScroll = 0
		}
		if c.blacksmithScroll > maxScroll {
			c.blacksmithScroll = maxScroll
		}
	}
}

func (c *Client) repairableItems() []game.ParsedItem {
	gs := c.engine.State
	if gs == nil {
		return nil
	}
	var items []game.ParsedItem
	for _, item := range gs.ParsedItems {
		if item.MaxHP > 0 && item.RemainingHP < item.MaxHP {
			items = append(items, item)
		}
	}
	return items
}

func (c *Client) drawBlacksmithWindow() {
	gs := c.engine.State
	if gs == nil || !gs.BlacksmithOpen {
		return
	}

	c.handleBlacksmithInput()

	mx, my := rl.GetMouseX(), rl.GetMouseY()

	panelW := int32(460)
	panelH := int32(360)
	px := (screenWidth - panelW) / 2
	py := (screenHeight - panelH) / 2
	pad := int32(12)
	rowH := int32(24)

	c.drawPanelTextured(px, py, panelW, panelH, ui.FrameLine, ui.FillBlack)

	y := py + pad

	// Title
	title := "Blacksmith - Repair"
	tw := c.measureText(title, 16)
	c.drawText(title, px+(panelW-tw)/2, y, 16, colorGoldBright)
	y += 24

	// Tunar display
	tunarStr := fmt.Sprintf("Your Tunar: %d", gs.Tunar)
	c.drawText(tunarStr, px+pad, y, 12, colorGold)
	y += 18

	// Separator
	rl.DrawLine(px+pad, y, px+panelW-pad, y, colorSep)
	y += 4

	// Column headers
	c.drawText("Item", px+pad+4, y, 12, colorDim)
	c.drawText("HP", px+panelW-pad-100, y, 12, colorDim)
	c.drawText("Cost", px+panelW-pad-45, y, 12, colorDim)
	y += rowH

	items := c.repairableItems()
	listTop := y
	maxVisible := 10

	if c.blacksmithSel >= len(items) {
		c.blacksmithSel = len(items) - 1
	}

	if len(items) == 0 {
		c.drawText("All items in good condition", px+pad+4, y+4, 14, colorDim)
	}

	for i := c.blacksmithScroll; i < len(items); i++ {
		row := i - c.blacksmithScroll
		ry := listTop + int32(row)*rowH
		if row >= maxVisible {
			break
		}

		item := items[i]
		isSelected := i == c.blacksmithSel
		rowX := px + pad
		rowW := panelW - pad*2
		isHover := mx >= rowX && mx < rowX+rowW && my >= ry && my < ry+rowH

		bg := colorRowBg1
		if row%2 == 1 {
			bg = colorRowBg2
		}
		if isSelected {
			bg = rl.Color{R: 50, G: 45, B: 70, A: 255}
		}
		if isHover {
			bg = colorRowHover
		}
		rl.DrawRectangle(rowX, ry, rowW, rowH, bg)

		if isHover && rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
			c.blacksmithSel = i
		}

		name := item.Name
		if len(name) > 25 {
			name = name[:25] + "..."
		}
		nameColor := colorWhite
		if isSelected {
			nameColor = colorGoldBright
		}
		c.drawText(name, rowX+4, ry+4, 13, nameColor)

		// HP bar
		hpStr := fmt.Sprintf("%d/%d", item.RemainingHP, item.MaxHP)
		c.drawText(hpStr, px+panelW-pad-100, ry+4, 13, colorDim)

		// Cost
		costStr := fmt.Sprintf("%d", gs.BlacksmithRepairCost)
		c.drawText(costStr, px+panelW-pad-45, ry+4, 13, colorGold)
	}

	// Bottom: repair button + hint
	btnY := py + panelH - 42
	btnW := int32(120)
	btnH := int32(30)
	btnX := px + panelW - pad - btnW

	canRepair := c.blacksmithSel >= 0 && c.blacksmithSel < len(items)
	btnBg := colorBtnBg
	btnText := colorDim
	isBtnHover := mx >= btnX && mx < btnX+btnW && my >= btnY && my < btnY+btnH

	if canRepair {
		btnText = colorGold
		if isBtnHover {
			btnBg = colorBtnHover
			btnText = colorGoldBright
		}
	}
	drawPanel(btnX, btnY, btnW, btnH, btnBg, colorBtnBorder)
	repairLabel := "Repair"
	rlw := c.measureText(repairLabel, 14)
	c.drawText(repairLabel, btnX+(btnW-rlw)/2, btnY+8, 14, btnText)

	if canRepair && isBtnHover && rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
		item := items[c.blacksmithSel]
		c.engine.SendRepairItem(item.InvSlot, uint32(gs.BlacksmithRepairCost))
	}

	// Hint
	c.drawText("Enter/A: Repair  Esc/B: Close", px+pad, btnY+8, 11, colorDim)

	// Close button (top-right X)
	closeX := px + panelW - 24
	closeY := py + 4
	isCloseHover := mx >= closeX && mx < closeX+20 && my >= closeY && my < closeY+20
	closeClr := colorDim
	if isCloseHover {
		closeClr = colorGoldBright
	}
	c.drawText("X", closeX+4, closeY+2, 14, closeClr)
	if isCloseHover && rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
		c.engine.SendCloseBlacksmith()
		c.blacksmithScroll = 0
		c.blacksmithSel = -1
	}
}

// --- Item Inspect Window ---

// attackTypeName returns a human-readable name for an AttackType value.
func attackTypeName(at int64) string {
	names := map[int64]string{
		1: "1H Slash", 2: "2H Slash", 3: "1H Blunt", 4: "2H Blunt",
		5: "1H Pierce", 6: "2H Pierce", 7: "Bow", 8: "Thrown",
		9: "H2H", 10: "Shield",
	}
	if n, ok := names[at]; ok {
		return n
	}
	return fmt.Sprintf("Type %d", at)
}

// drawInspectWindow renders a dedicated item inspect overlay window.
func (c *Client) drawInspectWindow() {
	if !c.inspectOpen || c.inspectItem == nil {
		return
	}
	item := c.inspectItem

	const (
		inspW   = int32(320)
		inspPad = int32(14)
		lineH   = int32(18)
	)

	// Build content lines to calculate dynamic height.
	type inspLine struct {
		text  string
		color rl.Color
		size  int32
		gap   int32 // extra vertical gap before this line
	}
	var lines []inspLine

	// Item name (gold, 16pt)
	lines = append(lines, inspLine{text: item.Name, color: colorGoldBright, size: 16})

	// Separator gap
	lines = append(lines, inspLine{gap: 6})

	// Level Required
	if item.LevelReq > 0 {
		lines = append(lines, inspLine{
			text: fmt.Sprintf("Level Required: %d", item.LevelReq), color: colorWhite, size: 13,
		})
	}

	// Slot
	if item.Slot > 0 {
		lines = append(lines, inspLine{
			text: fmt.Sprintf("Slot: %s", equipSlotName(item.Slot)), color: colorWhite, size: 13,
		})
	}

	// Value
	if item.ItemCost > 0 {
		costStr := fmt.Sprintf("%d", item.ItemCost)
		// Add comma separators for readability
		if item.ItemCost >= 1000 {
			costStr = formatTunar(item.ItemCost)
		}
		lines = append(lines, inspLine{
			text: fmt.Sprintf("Value: %s tunar", costStr), color: colorWhite, size: 13,
		})
	}

	// Durability
	if item.MaxHP > 0 {
		lines = append(lines, inspLine{
			text: fmt.Sprintf("Durability: %d / %d", item.RemainingHP, item.MaxHP), color: colorWhite, size: 13,
		})
	}

	// Attack info (if weapon)
	if item.AttackType > 0 {
		lines = append(lines, inspLine{gap: 6})
		lines = append(lines, inspLine{
			text: fmt.Sprintf("Attack Type: %s", attackTypeName(item.AttackType)), color: colorWhite, size: 13,
		})
		if item.WeaponDamage > 0 {
			lines = append(lines, inspLine{
				text: fmt.Sprintf("Damage: %d", item.WeaponDamage), color: colorWhite, size: 13,
			})
		}
	}

	// Stats
	if len(item.Stats) > 0 {
		lines = append(lines, inspLine{gap: 6})
		lines = append(lines, inspLine{text: "Stats:", color: colorGold, size: 13})
		// Sort stat keys for deterministic order.
		statKeys := make([]int, 0, len(item.Stats))
		for k := range item.Stats {
			statKeys = append(statKeys, k)
		}
		sort.Ints(statKeys)
		for _, k := range statKeys {
			v := item.Stats[k]
			sign := "+"
			if v < 0 {
				sign = ""
			}
			lines = append(lines, inspLine{
				text: fmt.Sprintf("  %s%d %s", sign, v, statName(k)), color: colorWhite, size: 12,
			})
		}
	}

	// Description (word-wrapped)
	if item.Desc != "" {
		lines = append(lines, inspLine{gap: 8})
		wrapped := c.wrapText(item.Desc, 12, inspW-inspPad*2)
		for _, wl := range wrapped {
			lines = append(lines, inspLine{text: wl, color: colorDim, size: 12})
		}
	}

	// Close hint
	lines = append(lines, inspLine{gap: 10})
	lines = append(lines, inspLine{text: "[Close]  (Enter / Esc)", color: colorDim, size: 11})

	// Calculate total height.
	totalH := inspPad * 2 // top + bottom padding
	for _, ln := range lines {
		totalH += ln.gap
		if ln.text != "" {
			totalH += lineH
		}
	}
	// Add space for separator line after name.
	totalH += 4

	// Center on screen.
	px := (screenWidth - inspW) / 2
	py := (screenHeight - totalH) / 2

	// Background panel.
	c.drawPanelTextured(px, py, inspW, totalH, ui.FrameLine, ui.FillBlack)

	// Draw content.
	y := py + inspPad
	nameDrawn := false
	for _, ln := range lines {
		y += ln.gap
		if ln.text == "" {
			continue
		}
		if !nameDrawn {
			// Center the item name.
			tw := c.measureText(ln.text, ln.size)
			c.drawText(ln.text, px+(inspW-tw)/2, y, ln.size, ln.color)
			nameDrawn = true
			y += lineH
			// Draw separator line under name.
			rl.DrawLine(px+inspPad, y, px+inspW-inspPad, y, colorSep)
			y += 4
		} else {
			c.drawText(ln.text, px+inspPad, y, ln.size, ln.color)
			y += lineH
		}
	}
}

// formatTunar adds comma separators to a tunar value for readability.
func formatTunar(v int64) string {
	s := fmt.Sprintf("%d", v)
	if len(s) <= 3 {
		return s
	}
	var result []byte
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result = append(result, ',')
		}
		result = append(result, byte(c))
	}
	return string(result)
}
