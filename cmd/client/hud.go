package main

import (
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/eqoa/client/internal/game"
	"github.com/eqoa/client/internal/mathutil"
	"github.com/eqoa/client/internal/models"
	"github.com/eqoa/client/internal/ui"
)

// spellSpriteFragSrc is the fragment shader for spell sprite meshes.
// Alpha blends texture with vertex colors (no cutoff).
const spellSpriteFragSrc = `#version 330
in vec2 fragTexCoord;
in vec4 fragColor;
uniform sampler2D texture0;
uniform vec4 colDiffuse;
out vec4 finalColor;
void main() {
    vec4 texel = texture(texture0, fragTexCoord);
    finalColor = texel * fragColor * colDiffuse;
}
` + "\x00"

// Sandstorm race IDs: HUM ELF DELF GNO DWF TRL BAR HLF ERU OGR
func raceName(id int) string {
	names := [...]string{
		0: "Human", 1: "Elf", 2: "Dark Elf", 3: "Gnome", 4: "Dwarf",
		5: "Troll", 6: "Barbarian", 7: "Halfling", 8: "Erudite", 9: "Ogre",
	}
	if id >= 0 && id < len(names) {
		return names[id]
	}
	return fmt.Sprintf("Race %d", id)
}

// Sandstorm class IDs: WAR RAN PAL SK MNK BRD RGE DRD SHA CL MAG NEC ENC WIZ ALC
func className(id int) string {
	names := [...]string{
		0: "Warrior", 1: "Ranger", 2: "Paladin", 3: "Shadow Knight",
		4: "Monk", 5: "Bard", 6: "Rogue", 7: "Druid", 8: "Shaman",
		9: "Cleric", 10: "Magician", 11: "Necromancer", 12: "Enchanter",
		13: "Wizard", 14: "Alchemist",
	}
	if id >= 0 && id < len(names) {
		return names[id]
	}
	return fmt.Sprintf("Class %d", id)
}

// ParsedItem alias for drawMenuItems (uses game package type).
type ParsedItem = game.ParsedItem

// equipSlotName maps Sandstorm item Slot ID to display name.
// Slot IDs from eqoa.live SCP: 0=none, 1=helm, 2=robe, ... 27=alchemy.
func equipSlotName(slot int64) string {
	names := [...]string{
		0: "None", 1: "Helm", 2: "Robe", 3: "Ear", 4: "Neck",
		5: "Chest", 6: "Bracelet", 7: "Bracers", 8: "Ring", 9: "Belt",
		10: "Legs", 11: "Feet", 12: "Primary", 13: "Shield", 14: "Secondary",
		15: "2-Hand", 16: "Bow", 17: "Thrown", 18: "Held", 19: "Gloves",
	}
	if slot >= 0 && slot < int64(len(names)) {
		return names[slot]
	}
	return fmt.Sprintf("Slot %d", slot)
}

// equipLocName returns the display name for a body EquipLocation value.
func equipLocName(loc int64) string {
	switch loc {
	case 0:
		return "Helm"
	case 1:
		return "Gloves"
	case 2:
		return "Earring"
	case 3:
		return "Earring"
	case 4:
		return "Neck"
	case 5:
		return "Chest"
	case 6:
		return "Bracelet"
	case 7:
		return "Bracelet"
	case 8:
		return "Bracers"
	case 9:
		return "Ring"
	case 10:
		return "Ring"
	case 11:
		return "Belt"
	case 12:
		return "Leggings"
	case 13:
		return "Boots"
	case 14:
		return "Primary"
	case 15:
		return "Secondary"
	case 21:
		return "Robe"
	}
	return fmt.Sprintf("Loc %d", loc)
}

// drawInspectionWnd renders the VIInspectionWND popup near the mouse cursor.
// Shows entity name, level, race/class (players) or NPC type, matching PS2 VIWndInspect.
func (c *Client) drawInspectionWnd(ent *game.Entity, mousePos rl.Vector2) {
	const nameFontSz = int32(14)
	const infoFontSz = int32(12)
	const pad = int32(6)

	// Build lines: name, info, optional NPC type
	type inspLine struct {
		text  string
		color rl.Color
		size  int32
	}
	var lines []inspLine

	// Line 1: name
	nameClr := nameColorFromByte(ent.NameColor)
	lines = append(lines, inspLine{ent.Name, nameClr, nameFontSz})

	// Line 2: guild (under name, cyan)
	if c.engine != nil && c.engine.State != nil {
		gs := c.engine.State
		if ent.ObjectID == c.targetObjectID && gs.TargetGuildName != "" {
			lines = append(lines, inspLine{
				fmt.Sprintf("<%s>", gs.TargetGuildName),
				rl.Color{R: 100, G: 200, B: 200, A: 255}, infoFontSz,
			})
		}
	}

	// Line 3+: level, race/class (player) or level + NPC type
	if ent.IsPlayer {
		lines = append(lines, inspLine{
			fmt.Sprintf("Lv %d  %s %s", ent.Level, raceName(int(ent.Race)), className(int(ent.Class))),
			colorWhite, infoFontSz,
		})
	} else {
		lines = append(lines, inspLine{
			fmt.Sprintf("Lv %d", ent.Level),
			colorWhite, infoFontSz,
		})
		if label := npcTypeLabel(ent.NPCType); label != "" {
			lines = append(lines, inspLine{label, colorDim, infoFontSz})
		}
	}

	// Compute dimensions
	maxW := int32(0)
	for _, l := range lines {
		w := c.measureText(l.text, l.size)
		if w > maxW {
			maxW = w
		}
	}
	boxW := maxW + pad*2
	lineH := nameFontSz + pad/2
	boxH := int32(len(lines))*lineH + pad*2

	// Position tooltip offset from mouse cursor
	tx := int32(mousePos.X) + 16
	ty := int32(mousePos.Y) - boxH - 4
	sw := int32(rl.GetScreenWidth())
	sh := int32(rl.GetScreenHeight())
	if tx+boxW > sw {
		tx = sw - boxW
	}
	if ty < 0 {
		ty = int32(mousePos.Y) + 20
	}
	if ty+boxH > sh {
		ty = sh - boxH
	}

	// Background + border
	c.drawPanelTextured(tx-1, ty-1, boxW+2, boxH+2, ui.FrameLine, ui.FillBlack)

	// Draw lines
	y := ty + pad
	for _, l := range lines {
		c.drawText(l.text, tx+pad, y, l.size, l.color)
		y += lineH
	}
}

// npcTypeLabel returns a display label for NPCType values.
func npcTypeLabel(npcType uint16) string {
	switch {
	case npcType&0x0100 != 0:
		return "Merchant"
	case npcType == 0x8006:
		return "Quest"
	case npcType == 0x8008:
		return "Guild"
	case npcType == 0x800C:
		return "Banker"
	default:
		return ""
	}
}

// conColor returns the PS2-style consider color and label for a target level vs player level.
// Matches server con byte: 0=red, 1=yellow, 2=white, 3=dkblue, 4=ltblue, 5=green.
func conColor(targetLvl, playerLvl byte) (rl.Color, string) {
	diff := int(targetLvl) - int(playerLvl)
	switch {
	case diff >= 6:
		return rl.Color{R: 255, G: 50, B: 50, A: 255}, "Red"
	case diff >= 3:
		return rl.Color{R: 255, G: 255, B: 50, A: 255}, "Yellow"
	case diff >= -2:
		return rl.Color{R: 255, G: 255, B: 255, A: 255}, "White"
	case diff >= -5:
		return rl.Color{R: 150, G: 200, B: 255, A: 255}, "Light Blue"
	case diff >= -9:
		return rl.Color{R: 100, G: 100, B: 255, A: 255}, "Blue"
	default:
		return rl.Color{R: 50, G: 255, B: 50, A: 255}, "Green"
	}
}

// drawText draws text using the client's custom font (Liberation Sans).
// Drop-in replacement for rl.DrawText with the same parameter order.
func (c *Client) drawText(text string, posX, posY, fontSize int32, color rl.Color) {
	rl.DrawTextEx(c.font, text, rl.Vector2{X: float32(posX), Y: float32(posY)}, float32(fontSize), 1, color)
}

// measureText measures text width using the client's custom font.
// Drop-in replacement for rl.MeasureText.
func (c *Client) measureText(text string, fontSize int32) int32 {
	v := rl.MeasureTextEx(c.font, text, float32(fontSize), 1)
	return int32(v.X)
}

type wrapCacheKey struct {
	text     string
	fontSize int32
	maxW     int32
}

// wrapText splits text into lines that fit within maxW pixels at the given font size.
// Results are cached to avoid re-measuring unchanged text every frame.
func (c *Client) wrapText(text string, fontSize int32, maxW int32) []string {
	key := wrapCacheKey{text, fontSize, maxW}
	if c.wrapCache != nil {
		if lines, ok := c.wrapCache[key]; ok {
			return lines
		}
	}
	if maxW <= 0 {
		return []string{text}
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{""}
	}
	var lines []string
	cur := words[0]
	for _, w := range words[1:] {
		test := cur + " " + w
		if c.measureText(test, fontSize) <= maxW {
			cur = test
		} else {
			lines = append(lines, cur)
			cur = w
		}
	}
	lines = append(lines, cur)

	// Force-break any lines still wider than maxW (very long words)
	var result []string
	for _, ln := range lines {
		for c.measureText(ln, fontSize) > maxW && len(ln) > 1 {
			// Binary search for the split point
			lo, hi := 1, len(ln)
			for lo < hi {
				mid := (lo + hi + 1) / 2
				if c.measureText(ln[:mid], fontSize) <= maxW {
					lo = mid
				} else {
					hi = mid - 1
				}
			}
			result = append(result, ln[:lo])
			ln = ln[lo:]
		}
		result = append(result, ln)
	}
	if c.wrapCache == nil {
		c.wrapCache = make(map[wrapCacheKey][]string)
	}
	if len(c.wrapCache) > 256 {
		c.wrapCache = make(map[wrapCacheKey][]string)
	}
	c.wrapCache[key] = result
	return result
}

// handleChatInput processes keyboard input while in chat typing mode.
func (c *Client) handleChatInput() {
	// Escape cancels
	if rl.IsKeyPressed(rl.KeyEscape) {
		c.chatTyping = false
		c.chatBuffer = ""
		return
	}

	// Enter sends
	if rl.IsKeyPressed(rl.KeyEnter) {
		text := c.chatBuffer
		c.chatTyping = false
		c.chatBuffer = ""
		if text == "" {
			return
		}
		// Reset chat activity so the message shows
		c.chatLastActivity = time.Now()

		// Parse chat command prefix
		cmd, body := parseChatCommand(text)
		switch cmd {
		case "s", "shout":
			c.engine.SendShout(body)
		case "sa", "say":
			c.engine.SendSay(body)
		case "g", "group":
			if body == "" {
				gs := c.engine.State
				gs.ChatLog = append(gs.ChatLog, game.ChatMessage{
					Text: "Usage: /g <message>", Channel: game.ChatError,
					ColorR: 255, ColorG: 0, ColorB: 0,
				})
			} else {
				c.engine.SendGroupChat(body)
			}
		case "gu", "guild":
			if body == "" {
				gs := c.engine.State
				gs.ChatLog = append(gs.ChatLog, game.ChatMessage{
					Text: "Usage: /gu <message>", Channel: game.ChatSystem,
				})
			} else {
				c.engine.SendGuildChat(body)
			}
		case "t", "tell":
			// The whole line, slash and name included, goes out as the say
			// message: the server reads the name out of it (func_00BCFE78).
			c.engine.SendChatCommand(text)
		case "r", "reply":
			// "/t <the last teller> <text>" (0x07A3's name, GetReplyName)
			if !c.engine.SendReply(body) {
				gs := c.engine.State
				gs.ChatLog = append(gs.ChatLog, game.ChatMessage{
					Text: "No one to reply to.", Channel: game.ChatSystem,
				})
			}
		case "time":
			gh := c.engine.State.GameHour()
			hour := int(gh)
			minute := int((gh - float32(hour)) * 60)
			period := "AM"
			displayHour := hour
			if hour >= 12 {
				period = "PM"
				if hour > 12 {
					displayHour = hour - 12
				}
			}
			if displayHour == 0 {
				displayHour = 12
			}
			gs := c.engine.State
			gs.ChatLog = append(gs.ChatLog, game.ChatMessage{
				Text:    fmt.Sprintf("Game time: %d:%02d %s", displayHour, minute, period),
				Channel: game.ChatSystem,
			})
		case "follow":
			if c.targetObjectID == 0 {
				gs := c.engine.State
				gs.ChatLog = append(gs.ChatLog, game.ChatMessage{
					Text: "You must have a target to follow.", Channel: game.ChatSystem,
				})
			} else {
				c.followTarget = !c.followTarget
				gs := c.engine.State
				if c.followTarget {
					gs.ChatLog = append(gs.ChatLog, game.ChatMessage{
						Text: "Following target.", Channel: game.ChatSystem,
					})
				} else {
					gs.ChatLog = append(gs.ChatLog, game.ChatMessage{
						Text: "No longer following.", Channel: game.ChatSystem,
					})
				}
			}
		case "wave":
			c.playEmote(animEmoteWave, "waves")
		case "bow":
			c.playEmote(animEmoteBow, "bows")
		case "point":
			c.playEmote(animEmotePoint, "points")
		case "cheer":
			c.playEmote(animEmoteCheer, "cheers")
		case "dance":
			c.playEmote(animSpecialIdle, "dances")
		default:
			if cmd == "" {
				// No prefix -> use default chat mode
				c.sendDefaultChat(text)
				break
			}
			// An unknown /command. Sandstorm's S2 sends the whole line,
			// slash and all, to the server as say chat (0x000B) in no chat
			// mode — its server-side commands arrive that way. Retail prints
			// string 826 as `%S "%s".` and sends nothing.
			if models.ActiveRules == models.RulesSandstorm {
				c.engine.SendChatCommand(text)
			} else {
				gs := c.engine.State
				gs.ChatLog = append(gs.ChatLog, game.ChatMessage{
					Text: fmt.Sprintf("Unrecognized command \"%s\".", text), Channel: game.ChatSystem,
				})
			}
		}
		return
	}

	// Backspace
	if rl.IsKeyPressed(rl.KeyBackspace) || rl.IsKeyPressedRepeat(rl.KeyBackspace) {
		if len(c.chatBuffer) > 0 {
			c.chatBuffer = c.chatBuffer[:len(c.chatBuffer)-1]
		}
		return
	}

	// Collect typed characters
	for {
		ch := rl.GetCharPressed()
		if ch == 0 {
			break
		}
		// func_00BCFE78 sends no line of 128 UTF-16 units or more.
		if utf16Len(c.chatBuffer+string(rune(ch))) < 128 {
			c.chatBuffer += string(rune(ch))
		}
	}
}

// parseChatCommand extracts a /command prefix and body from chat text.
// Returns ("", "") if no command prefix found.
func parseChatCommand(text string) (cmd, body string) {
	if len(text) < 2 || text[0] != '/' {
		return "", ""
	}
	// Find the space after the command
	for i := 1; i < len(text); i++ {
		if text[i] == ' ' {
			return strings.ToLower(text[1:i]), text[i+1:]
		}
	}
	// Command with no body (e.g. just "/s")
	return strings.ToLower(text[1:]), ""
}

// sendDefaultChat sends typed text in the kept chat mode, as the PS2 does
// for a line with no /command: no 0x000E, the mode the server already has.
func (c *Client) sendDefaultChat(text string) {
	c.engine.SendChat(text)
}

// The chat window's four modes (0 say, 1 shout, 2 group, 3 guild) are the
// PS2's chat modes 0, 3, 2 and 4 (GroupChanged, words 595-599).
var chatUIModes = [4]byte{game.ChatModeSay, game.ChatModeShout, game.ChatModeGroup, game.ChatModeGuild}

// chatUIMode is the window's mode for the kept chat mode (whisper shows as say).
func (c *Client) chatUIMode() int {
	for i, m := range chatUIModes {
		if m == c.engine.State.ChatMode {
			return i
		}
	}
	return 0
}

// setChatUIMode picks a window mode: GroupChanged, so the server and the
// "Chat Mode:" line follow.
func (c *Client) setChatUIMode(mode int) {
	if mode < 0 || mode >= len(chatUIModes) {
		mode = 0
	}
	c.engine.SetChatMode(chatUIModes[mode])
}

// utf16Len is strlen16 of s.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r >= 0x10000 {
			n++
		}
	}
	return n
}

// playEmote starts an emote animation on the local player.
func (c *Client) playEmote(animSlot byte, verb string) {
	modelID := int32(c.engine.State.Player.ModelID)
	if c.animMgr.DebugModelID != 0 {
		modelID = c.animMgr.DebugModelID
	}
	c.animMgr.PlayEmote(animSlot, modelID)
}

// chatModeName returns the display name for a chat mode index.
func chatModeName(mode int) string {
	switch mode {
	case 0:
		return "Say"
	case 1:
		return "Shout"
	case 2:
		return "Group"
	case 3:
		return "Guild"
	default:
		return "Say"
	}
}

// chatModeColor returns the display color for a chat mode.
func chatModeColor(mode int) rl.Color {
	switch mode {
	case 0:
		return colorWhiteFull // white
	case 1:
		return rl.Color{R: 255, G: 100, B: 100, A: 255} // red
	case 2:
		return rl.Color{R: 100, G: 180, B: 255, A: 255} // blue
	case 3:
		return rl.Color{R: 100, G: 255, B: 100, A: 255} // green
	default:
		return rl.White
	}
}

// drawChatModeButton draws a small chat mode indicator at the top-right of the chat panel
// and handles the popup context menu for switching modes.
func (c *Client) drawChatModeButton(chatX, chatY, chatW int32) {
	label := chatModeName(c.chatUIMode())
	labelW := c.measureText(label, 11)
	btnW := labelW + 12
	btnH := int32(16)
	btnX := chatX + chatW - btnW - 2
	btnY := chatY - btnH - 1

	mx := rl.GetMouseX()
	my := rl.GetMouseY()
	clicked := rl.IsMouseButtonPressed(rl.MouseButtonLeft)

	// Draw button
	btnHover := mx >= btnX && mx < btnX+btnW && my >= btnY && my < btnY+btnH
	bg := colorOverlay
	if btnHover {
		bg = rl.Color{R: 40, G: 40, B: 60, A: 220}
	}
	rl.DrawRectangle(btnX, btnY, btnW, btnH, bg)
	rl.DrawRectangleLines(btnX, btnY, btnW, btnH, colorBtnBorder)
	c.drawText(label, btnX+6, btnY+2, 11, chatModeColor(c.chatUIMode()))

	if clicked && btnHover {
		c.chatModePopup = !c.chatModePopup
		return
	}

	// Draw popup menu
	if !c.chatModePopup {
		return
	}

	modes := []int{0, 1, 2, 3}
	popW := int32(60)
	popRowH := int32(18)
	popH := int32(len(modes)) * popRowH
	popX := btnX + btnW - popW
	popY := btnY - popH

	drawPanel(popX, popY, popW, popH, colorPanelBg, colorBtnBorder)

	for i, mode := range modes {
		ry := popY + int32(i)*popRowH
		rowHover := mx >= popX && mx < popX+popW && my >= ry && my < ry+popRowH
		if rowHover {
			rl.DrawRectangle(popX+1, ry, popW-2, popRowH, colorRowHover)
		}
		name := chatModeName(mode)
		clr := chatModeColor(mode)
		if mode == c.chatUIMode() {
			name = "> " + name
		}
		c.drawText(name, popX+6, ry+2, 12, clr)

		if clicked && rowHover {
			c.setChatUIMode(mode)
			c.chatModePopup = false
		}
	}

	// Click outside popup closes it
	if clicked && !btnHover && !(mx >= popX && mx < popX+popW && my >= popY && my < popY+popH) {
		c.chatModePopup = false
	}
}

// drawCompass renders the PS2 compass bezel + rotating rose in the top-left corner.
func (c *Client) drawCompass() {
	if c.viui == nil || !c.viui.OK() {
		return
	}
	const (
		cx       = float32(8)  // position X
		cy       = float32(14) // position Y (below position text)
		bezelW   = float32(48) // bezel display size (preserves 59:69 aspect ratio)
		bezelH   = float32(56)
		roseSize = float32(40) // rose display size (centered inside bezel)
	)

	// Static bezel (outer ring)
	c.viui.BlitEx(texCompassBezel, cx, cy, bezelW, bezelH, rl.White)

	// Rotating rose -- rotate with camera facing so compass matches PS2.
	// PS2 world: +X = east, +Z = south. camAngle=0 faces +Z (south).
	// +90° offset so compass shows E when facing east, matching PS2.
	rotDeg := -c.camAngle*180/math.Pi + 90
	roseX := cx + (bezelW-roseSize)/2
	roseY := cy + (bezelH-roseSize)/2 + 5
	c.viui.BlitRot(texCompassRose, roseX, roseY, roseSize, roseSize, rotDeg, rl.White)
}

// drawSpellBar renders a vertical column of spell slots with scroll arrows (top-right).
// Slots 0-9 correspond to keys 1-9, 0. Shows spell name and cooldown overlay.
func (c *Client) drawSpellBar() {
	if c.viui == nil || !c.viui.OK() {
		return
	}
	const (
		slotW    = float32(32) // slot square size
		borderH  = float32(6)  // golden scroll border height
		gap      = float32(0)
		numSlots = 10
		padRight = float32(10)
		padTop   = float32(8)
		fontSize = int32(8)
	)

	gs := c.engine.State
	x := float32(screenWidth) - slotW - padRight
	y := padTop

	borderW := slotW + 7
	bx := x - 3.5

	// Pre-calculate bottom border Y position
	botY := y + borderH + gap + float32(numSlots)*(slotW+gap) - 3

	// Draw bottom border FIRST (under slots)
	c.viui.BlitEx(31, bx, botY, borderW, borderH, rl.White)

	// Golden scroll border (top)
	c.viui.BlitEx(texSpellBorder, bx, y, borderW, borderH, rl.White)
	y += borderH + gap

	// Spell slots
	mx := float32(rl.GetMouseX())
	my := float32(rl.GetMouseY())
	hoverSlot := -1

	for i := 0; i < numSlots; i++ {
		// When gamepad spell bar is active, rotate display so spellBarCursor is at top
		slotIdx := i
		if c.spellBarVisible {
			slotIdx = (i + c.spellBarCursor) % numSlots
		}

		// Slot background
		tint := rl.White
		cd := gs.CooldownRemaining(slotIdx)
		c.viui.BlitEx(texSpellSlot, x, y, slotW, slotW, tint)

		// Spell icon texture (or fallback text name)
		if gs.Hotbar[slotIdx] != nil {
			if tex, ok := c.getSpellIcon(gs.Hotbar[slotIdx].Icon); ok {
				// Inset icon within slot frame
				const padY = float32(5)
				const padL = float32(7.5) // shift right to center in slot frame
				iconSz := slotW - padY*2
				src := rl.Rectangle{Width: float32(tex.Width), Height: float32(tex.Height)}
				dst := rl.Rectangle{X: x + padL, Y: y + padY, Width: iconSz, Height: iconSz}
				rl.DrawTexturePro(tex, src, dst, rl.Vector2{}, 0, tint)
			} else if gs.HotbarNames[slotIdx] != "" {
				name := gs.HotbarNames[slotIdx]
				tw := c.measureText(name, fontSize)
				// Clamp text to slot left edge so it never extends outside spell bar
				tx := int32(x) + (int32(slotW)-tw)/2
				if tx < int32(x) {
					tx = int32(x)
				}
				ty := int32(y) + int32(slotW)/2 - fontSize/2
				c.drawText(name, tx+1, ty+1, fontSize, rl.Color{R: 0, G: 0, B: 0, A: 200})
				c.drawText(name, tx, ty, fontSize, rl.White)
			}
		}

		// Cooldown overlay: black rect depletes top-to-bottom (PS2 style)
		if cd > 0 {
			frac := float32(1.0) // fraction of slot still covered
			dur := gs.CooldownDurs[slotIdx]
			if dur > 0 {
				frac = cd / dur
				if frac > 1 {
					frac = 1
				}
			}
			coverH := slotW * frac
			rl.DrawRectangle(int32(x), int32(y), int32(slotW), int32(coverH),
				rl.Color{R: 0, G: 0, B: 0, A: 140})
		}

		// Hover and click detection
		if mx >= x && mx < x+slotW && my >= y && my < y+slotW && gs.Hotbar[slotIdx] != nil {
			hoverSlot = slotIdx
			if rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
				c.castHotbarSlot(slotIdx)
			}
		}

		y += slotW + gap
	}

	// Hover tooltip (drawn after all slots so it renders on top)
	if hoverSlot >= 0 {
		sp := gs.Hotbar[hoverSlot]
		tipText := sp.Name
		tipFontSz := int32(12)
		tipW := c.measureText(tipText, tipFontSz)
		tipPad := int32(4)
		tipH := tipFontSz + tipPad*2
		tipX := int32(x) - tipW - tipPad*2 - 4
		tipY := int32(my) - tipH/2
		c.drawPanelTextured(tipX, tipY, tipW+tipPad*2, tipH, ui.FrameLine, ui.FillBlack)
		c.drawText(tipText, tipX+tipPad, tipY+tipPad, tipFontSz, rl.White)
	}

	// Gamepad: full spell name drawn last so it's always on top, to the left of spell bar
	if c.spellBarVisible {
		topSlotIdx := c.spellBarCursor % numSlots
		if gs.Hotbar[topSlotIdx] != nil {
			alpha := uint8(255)
			if c.spellBarFade < 0.5 {
				alpha = uint8(c.spellBarFade / 0.5 * 255)
			}
			nameFontSz := int32(12)
			// Use full spell name (not HotbarNames which is truncated to 8 chars)
			spName := gs.Hotbar[topSlotIdx].Name
			if spName != "" {
				nameW := c.measureText(spName, nameFontSz)
				// Right-aligned, right edge pinned 10px left of spell bar border
				nameX := int32(bx) - nameW - 10
				nameY := int32(padTop+borderH) + int32(slotW)/2 - nameFontSz/2
				// Dark backing panel so text is always readable
				panelPad := int32(3)
				c.drawPanelTextured(nameX-panelPad, nameY-panelPad,
					nameW+panelPad*2, nameFontSz+panelPad*2, ui.FrameLine, ui.FillBlack)
				c.drawText(spName, nameX, nameY, nameFontSz, rl.Color{R: 255, G: 215, B: 0, A: alpha})
			}
		}
	}
}

// isMouseOverMusicHud returns true if the mouse cursor is within the music HUD.
func (c *Client) isMouseOverMusicHud() bool {
	if !c.musicHudOn {
		return false
	}
	mx := int32(rl.GetMouseX())
	my := int32(rl.GetMouseY())
	return mx >= c.musicHudX && mx < c.musicHudX+200 && my >= c.musicHudY && my < c.musicHudY+44
}

// isMouseOverChat returns true if the mouse cursor is within the chat panel area.
func (c *Client) isMouseOverChat() bool {
	chatW := int32(screenWidth) * 2 / 5
	chatH := int32(4*2 + 14*8) // chatPadY*2 + chatLineH*chatMaxLines
	chatX := int32(screenWidth) - chatW - 4
	chatY := int32(screenHeight) - chatH - 4
	mx := int32(rl.GetMouseX())
	my := int32(rl.GetMouseY())
	return mx >= chatX && mx < chatX+chatW && my >= chatY && my < chatY+chatH
}

// isMouseOverSpellBar returns true if the mouse cursor is within the spell bar area.
func (c *Client) isMouseOverSpellBar() bool {
	const (
		slotW    = float32(32)
		borderH  = float32(6)
		numSlots = 10
		padRight = float32(10)
		padTop   = float32(8)
	)
	x := float32(screenWidth) - slotW - padRight
	y := padTop
	h := borderH + float32(numSlots)*slotW + borderH
	mx := float32(rl.GetMouseX())
	my := float32(rl.GetMouseY())
	return mx >= x && mx < x+slotW && my >= y && my < y+h
}

// getSpellIcon resolves a spell Icon value to a GPU texture.
// lookupIcon resolves an icon ID to a GPU texture using the given texture map, sorted list, and cache.
// Tries direct DictID match first, then falls back to deterministic index.
func lookupIcon(iconID int64, texMap map[int32]rl.Texture2D, texList []int32, cache map[int64]rl.Texture2D) (rl.Texture2D, bool) {
	if tex, ok := cache[iconID]; ok {
		return tex, tex.ID > 0
	}
	if len(texMap) == 0 {
		return rl.Texture2D{}, false
	}
	if tex, ok := texMap[int32(iconID)]; ok {
		cache[iconID] = tex
		return tex, true
	}
	if len(texList) > 0 {
		idx := iconID
		if idx < 0 {
			idx = -idx
		}
		dictID := texList[idx%int64(len(texList))]
		if tex, ok := texMap[dictID]; ok {
			cache[iconID] = tex
			return tex, true
		}
	}
	cache[iconID] = rl.Texture2D{}
	return rl.Texture2D{}, false
}

func (c *Client) getSpellIcon(iconID int64) (rl.Texture2D, bool) {
	return lookupIcon(iconID, c.iconTextures, c.iconList, c.iconCacheSpell)
}

func (c *Client) getItemIcon(iconID int64) (rl.Texture2D, bool) {
	return lookupIcon(iconID, c.itemIconTextures, c.itemIconList, c.itemIconCache)
}

// castHotbarSlot attempts to cast the spell in the given hotbar slot.
func (c *Client) castHotbarSlot(slot int) {
	gs := c.engine.State
	sp := gs.Hotbar[slot]
	if sp == nil {
		log.Printf("Hotbar: slot %d is empty", slot)
		return
	}

	cd := gs.CooldownRemaining(slot)
	if cd > 0 {
		log.Printf("Hotbar: %s on cooldown (%.1fs)", sp.Name, cd)
		return
	}

	targetID := c.targetObjectID
	if targetID == 0 {
		targetID = 0xFFFFFFFF // self-cast
	}

	log.Printf("Hotbar: casting slot %d = %s (target=0x%08X)", slot, sp.Name, targetID)
	gs.CastSpellName = sp.Name
	c.engine.SendCast(uint8(sp.WhereOnHotBar), targetID)
}

// handleHotbarKeys checks 1-9,0 keys and casts the spell in that hotbar slot.
func (c *Client) handleHotbarKeys() {
	slot := -1
	for i := 0; i < 9; i++ {
		if rl.IsKeyPressed(int32(rl.KeyOne + i)) {
			slot = i
			break
		}
	}
	if rl.IsKeyPressed(rl.KeyZero) {
		slot = 9
	}
	if slot >= 0 {
		c.castHotbarSlot(slot)
	}
}

// drawBuffBar renders active buffs/debuffs as a horizontal row to the right of player HP/Power bars.
func (c *Client) drawBuffBar() {
	gs := c.engine.State
	if len(gs.Buffs) == 0 {
		return
	}

	const (
		iconSz = float32(24) // buff icon size
		gap    = float32(4)
		// Must match compass/bar layout in render.go
		compassX = float32(8)
		compassW = float32(48)
		barW     = float32(80)
	)

	// Position: right of HP/Power bars, vertically centered with compass
	startX := compassX + compassW + 4 + barW + 8
	y := float32(12) // align near top of compass area

	mx := float32(rl.GetMouseX())
	my := float32(rl.GetMouseY())
	hoverBuff := -1

	// Count active buffs, deduplicate by SpellHash
	seen := make(map[int32]bool)
	var activeIdx []int
	for i := range gs.Buffs {
		b := &gs.Buffs[i]
		if b.SpellHash != 0 && b.Name != "" && !seen[b.SpellHash] {
			seen[b.SpellHash] = true
			activeIdx = append(activeIdx, i)
		}
	}

	for j, i := range activeIdx {
		x := startX + float32(j)*(iconSz+gap)

		// Dark background for buff slot
		rl.DrawRectangle(int32(x-1), int32(y-1), int32(iconSz+2), int32(iconSz+2),
			rl.Color{R: 0, G: 0, B: 0, A: 160})

		// Buff icon (SpellHash is the icon DictID). Crop 75% to trim dead space.
		if tex, ok := c.getSpellIcon(int64(gs.Buffs[i].SpellHash)); ok {
			cropW := float32(tex.Width) * 0.75
			cropH := float32(tex.Height) * 0.70
			src := rl.Rectangle{Width: cropW, Height: cropH}
			dst := rl.Rectangle{X: x, Y: y, Width: iconSz, Height: iconSz}
			rl.DrawTexturePro(tex, src, dst, rl.Vector2{}, 0, rl.White)
		}

		// Hover detection
		if mx >= x-1 && mx < x+iconSz+1 && my >= y-1 && my < y+iconSz+1 {
			hoverBuff = i
		}
	}

	// Hover tooltip (below the buff row)
	if hoverBuff >= 0 {
		tipText := gs.Buffs[hoverBuff].Name
		tipFontSz := int32(12)
		tipW := c.measureText(tipText, tipFontSz)
		tipPad := int32(4)
		tipH := tipFontSz + tipPad*2
		tipX := int32(mx) - tipW/2
		tipY := int32(y+iconSz) + 4
		c.drawPanelTextured(tipX, tipY, tipW+tipPad*2, tipH, ui.FrameThin, ui.FillThin)
		c.drawText(tipText, tipX+tipPad, tipY+tipPad, tipFontSz, rl.White)
	}
}

// drawSpellEffects renders active spell visual effects in 3D space.
// Called during the 3D rendering pass (before EndMode3D).
func (c *Client) drawSpellEffects(gs *game.GameState, camera rl.Camera3D) {
	// Lazily initialize spell sprite system (needs Raylib shader, so done on render thread).
	if c.engine.SpellFX != nil && c.engine.SpellFX.Sprites == nil {
		shader := rl.LoadShaderFromMemory("", spellSpriteFragSrc)
		c.engine.SpellFX.Sprites = game.NewSpellSpriteSystem(c.engine.State.SpellFX, shader)
	}

	// Update emitter positions + particles at render frame rate (60fps).
	// The engine tick (100ms) drives spell event timelines but is too slow
	// for tracking moving entities. PS2 calls ProcessCreateEmitter every
	// frame (~33ms) to keep emitters attached to actor bones.
	if c.engine.SpellFX != nil {
		// Tell engine Process() to skip particle updates -- we handle it here at frame rate
		c.engine.SpellFX.ExternalParticles = true
		frameDt := rl.GetFrameTime()
		// Per-frame emitter position update — resolves current actor/bone positions
		// so emitters track moving entities without 100ms lag.
		c.engine.SpellFX.UpdateEmitterPositions(frameDt)
		c.engine.SpellFX.ProcessParticles(frameDt, camera.Position.X, camera.Position.Y, camera.Position.Z)
	}

	// Render particle billboard quads (textured, from SPELLFX.ESF).
	// Render() manages per-emitter blend modes internally (additive vs alpha).
	if c.engine.SpellFX != nil {
		c.engine.SpellFX.Particles.Render(camera)
	}

	// Lazily initialize lightning system
	if c.lightningSystem == nil {
		c.lightningSystem = game.NewLightningSystem()
	}

	// Render non-particle VFX (lightning, sprite placeholders)
	frameDt := rl.GetFrameTime()
	vfx := c.engine.SpellFX.VisualEffects()

	// Track which lightning bolts are active this frame (to clean up stale ones)
	activeLightningKeys := make(map[uint64]bool)

	for _, fx := range vfx {
		switch fx.Type {
		case game.VFXEmitter, game.VFXProjectile:
			// Now rendered by ParticleSystem -- skip

		case game.VFXLightning:
			// PS2 ProcessLightningEvent at 0x004789A8: bone mode (Fields[1] low byte)
			// selects which bones to use for bolt endpoints.
			// Bolt goes FROM caster TO target (source = caster, destination = target).
			// Destination is always target's hands midpoint (avg of right + left hand).
			//
			// Mode 0: Source = caster RIGHT hand    → Target hands midpoint
			// Mode 1: Source = caster LEFT hand     → Target hands midpoint
			// Mode 2: Source = caster hands midpoint → Target hands midpoint
			// Mode 3: TWO bolts — one from each caster hand → Target hands midpoint
			casterID := fx.SourceObjID
			targetID := fx.TargetObjID

			// Destination is always target's hands midpoint (PS2 common code at 0x00478C90)
			dx, dy, dz := c.resolveHandsMidpoint(gs, targetID)

			color := [3]float32{fx.ColorR, fx.ColorG, fx.ColorB}
			boneMode := fx.LightningBoneMode

			// Primary bolt source depends on bone mode
			var sx, sy, sz float32
			switch boneMode {
			case 0: // Caster RIGHT hand (node 0)
				sx, sy, sz = c.resolveBonePos(gs, casterID, 0)
			case 1: // Caster LEFT hand (node 1)
				sx, sy, sz = c.resolveBonePos(gs, casterID, 1)
			case 2: // Caster hands midpoint
				sx, sy, sz = c.resolveHandsMidpoint(gs, casterID)
			case 3: // Right hand (primary bolt; secondary bolt from left hand below)
				sx, sy, sz = c.resolveBonePos(gs, casterID, 0)
			default:
				sx, sy, sz = c.resolveHandsMidpoint(gs, casterID)
			}

			// Primary bolt
			key := uint64(casterID)<<32 | uint64(targetID)
			activeLightningKeys[key] = true
			bolt := c.lightningSystem.GetOrCreate(key, sx, sy, sz, dx, dy, dz, color)
			bolt.SetEndpoints(sx, sy, sz, dx, dy, dz)
			bolt.Color = color
			// The event's m_p10 caps the branches (SetMaxBranchCount, ≤ 3);
			// a negative one is read as none (Intn would panic on it).
			branches := max(0, int(fx.LightningMaxBranch))
			bolt.SetMaxBranchCount(branches)
			if fx.LightningHeld {
				bolt.Flash() // SetCharge(1), every frame of the duration
			}

			// Mode 3: secondary bolt from caster LEFT hand (PS2 allocates second VILightning at +0x2C)
			if boneMode == 3 {
				lx, ly, lz := c.resolveBonePos(gs, casterID, 1)
				key2 := key ^ 0x1 // distinct key for secondary bolt
				activeLightningKeys[key2] = true
				bolt2 := c.lightningSystem.GetOrCreate(key2, lx, ly, lz, dx, dy, dz, color)
				bolt2.SetEndpoints(lx, ly, lz, dx, dy, dz)
				bolt2.Color = color
				bolt2.SetMaxBranchCount(branches)
				if fx.LightningHeld {
					bolt2.Flash()
				}
			}

		case game.VFXSprite:
			sx, sy, sz := c.resolveObjPos(gs, fx.SourceObjID)
			sy += 3 // body center offset for sprite rendering
			facing := c.resolveObjFacing(gs, fx.SourceObjID)
			// Try real sprite mesh rendering via SpellSpriteSystem
			if c.engine.SpellFX != nil && c.engine.SpellFX.Sprites != nil && fx.SpriteDictID != 0 {
				model := c.engine.SpellFX.Sprites.LoadSprite(fx.SpriteDictID)
				if model != nil {
					c.engine.SpellFX.Sprites.DrawSprite(model, sx, sy, sz, facing, fx.Opacity)
					continue
				}
			}
			// Fallback: pulsing sphere for sprites without mesh data
			fadeAlpha := float32(1.0)
			if fx.MaxAge > 0 && fx.Progress > 0.7 {
				fadeAlpha = (1.0 - fx.Progress) / 0.3
			}
			pulse := float32(1.0 + 0.2*math.Sin(float64(fx.Age*4.0)))
			radius := 1.0 * pulse * (1.0 - fx.Progress*0.3)
			a := uint8(mathutil.ClampF(100*fadeAlpha*fx.Opacity, 0, 255))
			rl.DrawSphere(rl.Vector3{X: sx, Y: sy + 3, Z: sz}, radius, rl.Color{R: 200, G: 100, B: 255, A: a})
		}
	}

	// Update and render lightning bolts
	c.lightningSystem.Update(frameDt)
	c.lightningSystem.Render()

	// Clean up lightning bolts that no longer have a matching VFX
	// (the spell driver removed the lightning event).
	// PS2: lightningActive stays true until charge decays to 0 after flash.
	// We let the LightningSystem.Update handle natural decay; only force-remove
	// bolts whose spell driver is completely gone (no VFX emitted).
	for key := range c.lightningSystem.GetKeys() {
		if !activeLightningKeys[key] {
			c.lightningSystem.Remove(key)
		}
	}
}

// resolveBonePos returns the bone position for an entity, falling back to root + height offset.
// PS2 GetNodeTransform at 0x0047A490: nodeID 0=right hand, 1=left hand, 8=head.
func (c *Client) resolveBonePos(gs *game.GameState, objID uint32, nodeID uint16) (float32, float32, float32) {
	if c.modelMgr != nil {
		var modelID uint32
		var x, y, z, facing, scale float32
		if objID == gs.PlayerObjectID {
			modelID = gs.Player.ModelID
			x, y, z = gs.Player.X, gs.Player.Y, gs.Player.Z
			facing = gs.Player.Facing
			scale = gs.Player.Scale
		} else if ent, ok := gs.Entities[objID]; ok {
			modelID = ent.ModelID
			x, y, z = ent.X, ent.Y, ent.Z
			facing = ent.Facing
			scale = ent.Scale
		}
		if modelID != 0 {
			bx, by, bz, ok := c.modelMgr.BoneWorldPos(int32(modelID), objID, nodeID, x, y, z, facing, scale)
			if ok {
				return bx, by, bz
			}
		}
	}
	// Fallback: root position + body center offset
	x, y, z := c.resolveObjPos(gs, objID)
	return x, y + 4, z
}

// resolveHandsMidpoint returns the midpoint of both hands for an entity.
// PS2: averages right hand (node 0) and left hand (node 1) positions.
func (c *Client) resolveHandsMidpoint(gs *game.GameState, objID uint32) (float32, float32, float32) {
	rx, ry, rz := c.resolveBonePos(gs, objID, 0) // right hand
	lx, ly, lz := c.resolveBonePos(gs, objID, 1) // left hand
	return (rx + lx) * 0.5, (ry + ly) * 0.5, (rz + lz) * 0.5
}

// resolveObjPos returns the world position of an entity by ObjectID.
// Returns (0,0,0) for unknown entities — particles render far off-screen
// rather than incorrectly on the local player.
func (c *Client) resolveObjPos(gs *game.GameState, objID uint32) (float32, float32, float32) {
	if objID == gs.PlayerObjectID {
		return gs.Player.X, gs.Player.Y, gs.Player.Z
	}
	if ent, ok := gs.Entities[objID]; ok {
		return ent.X, ent.Y, ent.Z
	}
	return 0, 0, 0
}

// resolveObjFacing returns the Y-axis facing angle (radians) of an entity.
func (c *Client) resolveObjFacing(gs *game.GameState, objID uint32) float32 {
	if objID == gs.PlayerObjectID {
		return gs.Player.Facing
	}
	if ent, ok := gs.Entities[objID]; ok {
		return ent.Facing
	}
	return 0
}

// drawTexturedBar draws a PS2-style HP/Power/XP bar using VIUI BlitBar.
func (c *Client) drawTexturedBar(x, y, barW, barH float32, fillTexID int, fillFrac float32, label string) {
	c.drawTexturedBarTinted(x, y, barW, barH, fillTexID, fillFrac, label, rl.White)
}

func (c *Client) drawTexturedBarTinted(x, y, barW, barH float32, fillTexID int, fillFrac float32, label string, tint rl.Color) {
	if c.viui == nil || !c.viui.OK() {
		return
	}
	// BlitBar clips the source rect proportionally (no squish)
	c.viui.BlitBar(texDarkStrip, fillTexID, x, y, barW, barH, fillFrac, tint)
	if label != "" {
		c.drawText(label, int32(x+barW+4), int32(y-1), 14, rl.White)
	}
}

// faceTexID returns the TexID for a given faceColor value.
// PS2 TexIDs: 115=hostile(red), 116=neutral(gray), 117=friendly(blue).
func faceTexID(faceColor byte) int {
	switch faceColor {
	case 0, 1: // hostile / threatening
		return texFaceHostile
	case 4, 5: // friendly / ally
		return texFaceFriendly
	default: // 2, 3 = neutral / indifferent
		return texFaceNeutral
	}
}

func (c *Client) drawTargetHUD(ent *game.Entity) {
	gs := c.engine.State
	hasUI := c.viui != nil && c.viui.OK()

	// Target panel: left cap (25) + tiled middle (26) + right cap (25 flipped).
	const (
		texCap = 25 // (93,307, 13x25) -- border cap, flipped for right side
		texMid = 26 // (sheet 20, target_name_tile 32x25) tiled
		capW   = float32(13)
		panelH = float32(25) // match cap native height (TexID 25 = 13x25)
		midW   = float32(160)
		panelW = capW + midW + capW
		padX   = float32(12)
	)
	panelX := padX
	panelY := float32(screenHeight) - panelH - 30

	if hasUI {
		c.viui.BlitEx(texCap, panelX, panelY, capW, panelH, rl.White)
		midDrawH := panelH
		midNatW := float32(32)
		for mx := float32(0); mx < midW; mx += midNatW {
			w := midNatW
			if mx+w > midW {
				w = midW - mx
			}
			c.viui.BlitEx(texMid, panelX+capW+mx, panelY, w, midDrawH, rl.White)
		}
		c.viui.BlitExFlipH(texCap, panelX+capW+midW, panelY, capW, panelH, rl.White)
	} else {
		rl.DrawRectangle(int32(panelX), int32(panelY), int32(panelW), int32(panelH),
			colorOverlay)
	}

	// Name centered in panel (black text)
	nameTextW := c.measureText(ent.Name, 14)
	c.drawText(ent.Name, int32(panelX)+(int32(panelW)-nameTextW)/2, int32(panelY+7), 14, rl.Black)

	// Below panel: HP bar (left) | face icon (right of bar)
	const (
		faceSize = float32(19)
		barH     = float32(8)
	)

	barX := panelX + 10
	barY := panelY + panelH + 2
	barW := midW * 0.5 // fixed width, 50% of middle band
	faceX := barX + barW + 4
	faceY := panelY + panelH + 1

	// Face indicator icon (right of HP bar) — hide for dead entities
	if hasUI && ent.HPFlag != 3 {
		c.viui.BlitEx(faceTexID(gs.TargetFaceColor), faceX, faceY, faceSize, faceSize, rl.White)
	}
	hpFrac := float32(0)
	if ent.MaxHP > 0 {
		hpFrac = float32(ent.HP) / float32(ent.MaxHP)
	}
	if hasUI {
		c.drawTexturedBar(barX, barY, barW, barH, texBarHP, hpFrac, "")
	} else {
		rl.DrawRectangle(int32(barX), int32(barY), int32(barW), int32(barH),
			rl.Color{R: 0x20, G: 0x20, B: 0x20, A: 0xC0})
		if hpFrac > 0 {
			rl.DrawRectangle(int32(barX), int32(barY), int32(barW*hpFrac), int32(barH), rl.Red)
		}
	}
}

// drawGroupHUD draws group member panels below the compass/player bars.
// Mirrors PS2 target HUD style: name panel + HP bar, no face indicator.
// Up to 3 members shown (excludes self). EQOA max group = 4 (player + 3).
func (c *Client) drawGroupHUD() {
	gs := c.engine.State
	if !gs.GroupActive || len(gs.GroupMembers) == 0 {
		return
	}

	hasUI := c.viui != nil && c.viui.OK()

	// Panel textures (same as target HUD)
	const (
		texCap = 25
		texMid = 26
		capW   = float32(13)
		panelH = float32(25)
		midW   = float32(130) // slightly narrower than target HUD (160)
		panelW = capW + midW + capW
		barH   = float32(8)
	)

	// Start below compass + player bars area
	// Compass is at (8,8) with height 56, so bottom is at Y=64
	startX := float32(8)
	startY := float32(70) // just below compass area

	slot := 0
	for _, m := range gs.GroupMembers {
		if m.ObjectID == gs.PlayerObjectID {
			continue // skip self
		}
		if slot >= 3 {
			break
		}

		// Vertical stacking: each slot = panelH + barH + gap
		slotH := panelH + barH + 6 // panel + hp bar + spacing
		panelX := startX
		panelY := startY + float32(slot)*slotH

		// Draw panel background
		if hasUI {
			c.viui.BlitEx(texCap, panelX, panelY, capW, panelH, rl.White)
			midDrawH := panelH
			midNatW := float32(32)
			for mx := float32(0); mx < midW; mx += midNatW {
				w := midNatW
				if mx+w > midW {
					w = midW - mx
				}
				c.viui.BlitEx(texMid, panelX+capW+mx, panelY, w, midDrawH, rl.White)
			}
			c.viui.BlitExFlipH(texCap, panelX+capW+midW, panelY, capW, panelH, rl.White)
		} else {
			rl.DrawRectangle(int32(panelX), int32(panelY), int32(panelW), int32(panelH),
				colorOverlay)
		}

		// Name centered in panel
		nameW := c.measureText(m.Name, 13)
		c.drawText(m.Name, int32(panelX)+(int32(panelW)-nameW)/2, int32(panelY+7), 13, rl.Black)

		// HP bar below panel
		barX := panelX
		barY := panelY + panelH + 1
		barW := midW * 0.6

		// Look up entity for HP data
		hpFrac := float32(0)
		if ent, ok := gs.Entities[m.ObjectID]; ok && ent.MaxHP > 0 {
			hpFrac = float32(ent.HP) / float32(ent.MaxHP)
		}
		if hasUI {
			c.drawTexturedBar(barX, barY, barW, barH, texBarHP, hpFrac, "")
		} else {
			rl.DrawRectangle(int32(barX), int32(barY), int32(barW), int32(barH),
				rl.Color{R: 0x20, G: 0x20, B: 0x20, A: 0xC0})
			if hpFrac > 0 {
				rl.DrawRectangle(int32(barX), int32(barY), int32(barW*hpFrac), int32(barH), rl.Red)
			}
		}

		slot++
	}
}

func (c *Client) drawDialogueBox(gs *game.GameState) {
	screenW := float32(rl.GetScreenWidth())
	screenH := float32(rl.GetScreenHeight())

	// Calculate box dimensions based on content
	fontSize := int32(14)
	lineH := float32(fontSize + 4)
	padding := float32(12)
	maxTextW := float32(400)

	// Wrap greeting text to fit within box
	greetingLines := c.wrapText(gs.DialogueText, fontSize, int32(maxTextW))

	// Count lines needed
	lines := len(greetingLines) // greeting (wrapped)
	if len(gs.DialogueOptions) > 0 {
		lines += 1 // blank line
		lines += len(gs.DialogueOptions)
	}
	lines += 1 // bottom hint

	boxW := maxTextW + padding*2
	boxH := float32(lines)*lineH + padding*2
	boxX := (screenW - boxW) / 2
	boxY := (screenH - boxH) / 2

	// Background
	c.drawPanelTextured(int32(boxX), int32(boxY), int32(boxW), int32(boxH), ui.FrameLine, ui.FillBlack)

	y := boxY + padding

	// Greeting text (word-wrapped)
	for _, line := range greetingLines {
		c.drawText(line, int32(boxX+padding), int32(y), fontSize, rl.White)
		y += lineH
	}

	// Options (with cursor highlight + mouse click)
	if len(gs.DialogueOptions) > 0 {
		y += lineH * 0.5 // half-line gap
		mousePos := rl.GetMousePosition()
		for i, opt := range gs.DialogueOptions {
			optY := y
			label := fmt.Sprintf("%d) %s", i+1, opt)
			// Highlight if cursor matches or mouse hovers
			optRect := rl.Rectangle{X: boxX + padding, Y: optY, Width: maxTextW, Height: lineH}
			hovered := rl.CheckCollisionPointRec(mousePos, optRect)
			if hovered {
				c.dialogueCursor = i
			}
			clr := colorGoldBright
			if c.dialogueCursor == i {
				// Draw highlight bar behind selected option
				rl.DrawRectangle(int32(boxX+4), int32(optY), int32(boxW-8), int32(lineH),
					rl.Color{R: 180, G: 160, B: 100, A: 40})
				clr = rl.White
			}
			c.drawText(label, int32(boxX+padding), int32(optY), fontSize, clr)
			// Mouse click selects option
			if hovered && rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
				c.engine.SendDialogueOption(gs.DialogueCounter, byte(i))
				gs.DialogueActive = false
				gs.DialogueOptions = nil
				c.dialogueCursor = 0
				return
			}
			y += lineH
		}
		// Clamp cursor to valid range
		if c.dialogueCursor >= len(gs.DialogueOptions) {
			c.dialogueCursor = len(gs.DialogueOptions) - 1
		}
	}

	// Hint text
	y += lineH * 0.3
	hint := "[X/Esc] Close"
	if len(gs.DialogueOptions) > 0 {
		hint = "[D-Pad/Mouse] Select   [X/A] Confirm   [Esc] Cancel"
	}
	hintW := c.measureText(hint, 11)
	c.drawText(hint, int32(boxX+boxW/2)-hintW/2, int32(y), 11, rl.Color{R: 150, G: 150, B: 150, A: 200})
}

// drawMusicHud renders a floating draggable music player HUD with
// scrolling title, prev/pause/play/next buttons.
func (c *Client) drawMusicHud() {
	const (
		hudW    = int32(200)
		hudH    = int32(44)
		titleH  = int32(18)
		btnSize = int32(24)
		btnGap  = int32(8)
		padX    = int32(6)
	)

	// Default position: above the target HUD panel (padX=12, panelY=screenHeight-55)
	if c.musicHudX < 0 {
		c.musicHudX = 12
		c.musicHudY = int32(screenHeight) - 55 - hudH - 4
	}

	mx := int32(rl.GetMouseX())
	my := int32(rl.GetMouseY())

	// Drag handling
	if rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
		// Title bar area for drag (top portion)
		if mx >= c.musicHudX && mx < c.musicHudX+hudW && my >= c.musicHudY && my < c.musicHudY+titleH {
			// Check if not on a button
			btnY := c.musicHudY + titleH + 2
			if my < btnY {
				c.musicHudDrag = true
				c.musicHudDragOX = mx - c.musicHudX
				c.musicHudDragOY = my - c.musicHudY
			}
		}
	}
	if c.musicHudDrag {
		if rl.IsMouseButtonDown(rl.MouseButtonLeft) {
			c.musicHudX = mx - c.musicHudDragOX
			c.musicHudY = my - c.musicHudDragOY
			// Clamp to screen
			if c.musicHudX < 0 {
				c.musicHudX = 0
			}
			if c.musicHudY < 0 {
				c.musicHudY = 0
			}
			if c.musicHudX+hudW > int32(screenWidth) {
				c.musicHudX = int32(screenWidth) - hudW
			}
			if c.musicHudY+hudH > int32(screenHeight) {
				c.musicHudY = int32(screenHeight) - hudH
			}
		} else {
			c.musicHudDrag = false
		}
	}

	// Background panel
	c.drawPanelTextured(c.musicHudX, c.musicHudY, hudW, hudH, ui.FrameLine, ui.FillBlack)

	// Current track name (scrolling marquee)
	var trackName string
	if c.audioSys != nil && c.audioSys.BGMActive() {
		trackName = c.audioSys.BGMPath()
	} else {
		trackName = "No track"
	}

	// Strip .BGM extension for display
	displayName := trackName
	if len(displayName) > 4 && displayName[len(displayName)-4:] == ".BGM" {
		displayName = displayName[:len(displayName)-4]
	}

	titleX := c.musicHudX + padX
	titleW := hudW - padX*2
	nameW := c.measureText(displayName, 12)

	// Clip title text to panel width
	rl.BeginScissorMode(titleX, c.musicHudY+2, titleW, titleH)
	if nameW > titleW {
		// Scroll: move text left, wrap around
		scrollSpeed := float32(30) // pixels per second
		dt := rl.GetFrameTime()
		c.musicScrollX += scrollSpeed * dt
		totalScrollW := float32(nameW + 40) // gap before repeat
		if c.musicScrollX >= totalScrollW {
			c.musicScrollX -= totalScrollW
		}
		drawX := titleX - int32(c.musicScrollX)
		c.drawText(displayName, drawX, c.musicHudY+4, 12, rl.Color{R: 200, G: 200, B: 200, A: 255})
		// Draw second copy for seamless wrap
		c.drawText(displayName, drawX+int32(totalScrollW), c.musicHudY+4, 12, rl.Color{R: 200, G: 200, B: 200, A: 255})
	} else {
		// Center short names
		c.drawText(displayName, titleX+(titleW-nameW)/2, c.musicHudY+4, 12, rl.Color{R: 200, G: 200, B: 200, A: 255})
	}
	rl.EndScissorMode()

	// Control buttons: |<  ||/>  >|
	btnY := c.musicHudY + titleH + 2
	totalBtnW := btnSize*3 + btnGap*2
	btnStartX := c.musicHudX + (hudW-totalBtnW)/2

	prevX := btnStartX
	playX := btnStartX + btnSize + btnGap
	nextX := btnStartX + (btnSize+btnGap)*2

	btnColor := rl.Color{R: 180, G: 180, B: 180, A: 255}
	hoverColor := colorGoldHover

	// Prev button
	prevHover := mx >= prevX && mx < prevX+btnSize && my >= btnY && my < btnY+btnSize
	clr := btnColor
	if prevHover {
		clr = hoverColor
	}
	c.drawText("|<", prevX+4, btnY+4, 12, clr)
	if prevHover && rl.IsMouseButtonPressed(rl.MouseButtonLeft) && !c.musicHudDrag {
		c.musicPlayPrev()
	}

	// Play/Pause button
	playHover := mx >= playX && mx < playX+btnSize && my >= btnY && my < btnY+btnSize
	clr = btnColor
	if playHover {
		clr = hoverColor
	}
	isPlaying := c.audioSys != nil && c.audioSys.BGMActive()
	playLabel := ">"
	if isPlaying {
		playLabel = "||"
	}
	c.drawText(playLabel, playX+6, btnY+4, 12, clr)
	if playHover && rl.IsMouseButtonPressed(rl.MouseButtonLeft) && !c.musicHudDrag {
		if isPlaying {
			c.audioSys.StopBGM()
			c.musicPlayerOn = false
		} else {
			c.musicPlayTrack(c.musicTrackIdx)
		}
	}

	// Next button
	nextHover := mx >= nextX && mx < nextX+btnSize && my >= btnY && my < btnY+btnSize
	clr = btnColor
	if nextHover {
		clr = hoverColor
	}
	c.drawText(">|", nextX+4, btnY+4, 12, clr)
	if nextHover && rl.IsMouseButtonPressed(rl.MouseButtonLeft) && !c.musicHudDrag {
		c.musicPlayNext()
	}
}

// musicPlayTrack plays the track at the given index in titleBGMTracks.
func (c *Client) musicPlayTrack(idx int) {
	if idx < 0 || idx >= len(titleBGMTracks) {
		return
	}
	c.musicTrackIdx = idx
	c.audioSys.StopBGM()
	c.playBGMFromISO(titleBGMTracks[idx])
	c.musicPlayerOn = true
	c.musicScrollX = 0
}

// musicPlayNext advances to the next track.
func (c *Client) musicPlayNext() {
	idx := c.musicTrackIdx + 1
	if idx >= len(titleBGMTracks) {
		idx = 0
	}
	c.musicPlayTrack(idx)
}

// musicPlayPrev goes to the previous track.
func (c *Client) musicPlayPrev() {
	idx := c.musicTrackIdx - 1
	if idx < 0 {
		idx = len(titleBGMTracks) - 1
	}
	c.musicPlayTrack(idx)
}

// drawFPSCounter renders the FPS counter in the top-left corner.
func (c *Client) drawFPSCounter() {
	fps := rl.GetFPS()
	label := fmt.Sprintf("%d FPS", fps)
	clr := rl.Color{R: 0, G: 230, B: 0, A: 220} // green
	if fps < 30 {
		clr = rl.Color{R: 230, G: 0, B: 0, A: 220} // red
	} else if fps < 55 {
		clr = rl.Color{R: 230, G: 200, B: 0, A: 220} // yellow
	}
	rl.DrawRectangle(4, 4, 70, 18, rl.Color{R: 0, G: 0, B: 0, A: 140})
	c.drawText(label, 8, 6, 13, clr)
}

// drawDisplayConfirm renders the "Keep these settings?" confirmation overlay.
func (c *Client) drawDisplayConfirm() {
	boxW := int32(320)
	boxH := int32(100)
	boxX := (screenWidth - boxW) / 2
	boxY := (screenHeight - boxH) / 2

	// Dark overlay
	rl.DrawRectangle(0, 0, screenWidth, screenHeight, rl.Color{R: 0, G: 0, B: 0, A: 120})

	// Box
	rl.DrawRectangle(boxX, boxY, boxW, boxH, rl.Color{R: 20, G: 18, B: 30, A: 240})
	rl.DrawRectangleLines(boxX, boxY, boxW, boxH, colorGold)

	// Title
	secs := int(c.displayConfirmTimer) + 1
	title := fmt.Sprintf("Keep these display settings? (%ds)", secs)
	tw := c.measureText(title, 14)
	c.drawText(title, boxX+(boxW-tw)/2, boxY+12, 14, colorWhite)

	// Resolution info
	info := fmt.Sprintf("%dx%d %s", screenWidth, screenHeight, windowModeLabels[c.windowMode])
	iw := c.measureText(info, 12)
	c.drawText(info, boxX+(boxW-iw)/2, boxY+34, 12, colorDim)

	// Buttons
	btnW := int32(100)
	btnH := int32(28)
	btnY := boxY + boxH - btnH - 12
	mx, my := rl.GetMouseX(), rl.GetMouseY()

	// Keep button
	keepX := boxX + boxW/2 - btnW - 10
	keepHover := mx >= keepX && mx < keepX+btnW && my >= btnY && my < btnY+btnH
	keepClr := colorGold
	if keepHover {
		rl.DrawRectangle(keepX, btnY, btnW, btnH, rl.Color{R: 40, G: 55, B: 40, A: 255})
	}
	rl.DrawRectangleLines(keepX, btnY, btnW, btnH, keepClr)
	kw := c.measureText("Keep", 14)
	c.drawText("Keep", keepX+(btnW-kw)/2, btnY+6, 14, keepClr)

	// Revert button
	revX := boxX + boxW/2 + 10
	revHover := mx >= revX && mx < revX+btnW && my >= btnY && my < btnY+btnH
	revClr := colorDim
	if revHover {
		rl.DrawRectangle(revX, btnY, btnW, btnH, rl.Color{R: 55, G: 30, B: 30, A: 255})
		revClr = rl.Color{R: 230, G: 100, B: 100, A: 255}
	}
	rl.DrawRectangleLines(revX, btnY, btnW, btnH, revClr)
	rw := c.measureText("Revert", 14)
	c.drawText("Revert", revX+(btnW-rw)/2, btnY+6, 14, revClr)

	// Click handling
	if rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
		if keepHover {
			c.confirmDisplayChange()
		} else if revHover {
			c.revertDisplayChange()
		}
	}

	// Keyboard: Enter = keep, Escape = revert
	if rl.IsKeyPressed(rl.KeyEnter) {
		c.confirmDisplayChange()
	}
	if rl.IsKeyPressed(rl.KeyEscape) {
		c.revertDisplayChange()
	}

	// Gamepad: Cross = keep, Circle = revert
	if gpAvailable() {
		if gpPressed(rl.GamepadButtonRightFaceDown) {
			c.confirmDisplayChange()
		}
		if gpPressed(rl.GamepadButtonRightFaceRight) {
			c.revertDisplayChange()
		}
	}
}
