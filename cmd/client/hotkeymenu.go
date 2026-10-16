package main

import (
	"fmt"
	"log"
	"strings"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/eqoa/client/internal/game"
	"github.com/eqoa/client/internal/ui"
)

// PS2 Hotkey Menu: Hold L2 (keyboard: L key) to open, D-pad to navigate tree.
// 25 entries in a tree structure. Each node shows 4 directional labels (N/W/E/S).
// Selecting a leaf executes its action (chat message, command code, or emote).
//
// DirCode encoding: child = parent + 1 + dir * 4^(depth+1)
// where dir: 0=N, 1=W, 2=E, 3=S and depth is the parent's tree depth.
// Root=0. Depth 0→1: offsets {1,5,9,13}. Depth 1→2: offsets {1,17,33,49}. Etc.

// hotkeyTree caches the parent/child relationships for fast navigation.
type hotkeyTree struct {
	children map[int64][4]int64 // parent → [N,W,E,S] child dirCodes (0 = no child)
	parent   map[int64]int64   // child → parent dirCode
}

// buildHotkeyTree computes parent/child maps from the flat entry list using BFS.
func buildHotkeyTree(entries []game.HotkeyEntry) *hotkeyTree {
	codeSet := make(map[int64]bool)
	for _, e := range entries {
		codeSet[e.DirCode] = true
	}

	tree := &hotkeyTree{
		children: make(map[int64][4]int64),
		parent:   make(map[int64]int64),
	}

	depth := make(map[int64]int)
	depth[0] = 0

	// BFS from root
	queue := []int64{0}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		d := depth[p]

		// 4^(d+1)
		pow := int64(1)
		for i := 0; i <= d; i++ {
			pow *= 4
		}

		var kids [4]int64
		for dir := 0; dir < 4; dir++ {
			childCode := p + 1 + int64(dir)*pow
			if codeSet[childCode] {
				kids[dir] = childCode
				depth[childCode] = d + 1
				tree.parent[childCode] = p
				queue = append(queue, childCode)
			}
		}
		tree.children[p] = kids
	}

	return tree
}

// findHotkeyEntry finds the entry with the given dirCode, or nil.
func findHotkeyEntry(entries []game.HotkeyEntry, dirCode int64) *game.HotkeyEntry {
	for i := range entries {
		if entries[i].DirCode == dirCode {
			return &entries[i]
		}
	}
	return nil
}

// handleHotkeyMenu processes hotkey menu input (keyboard L key or gamepad L2 hold).
// Returns true if the hotkey menu consumed the input this frame.
func (c *Client) handleHotkeyMenu() bool {
	gs := c.engine.State
	if gs == nil || len(gs.Hotkeys) == 0 {
		return false
	}

	// Lazily build tree on first use
	if c.hotkeyTree == nil {
		c.hotkeyTree = buildHotkeyTree(gs.Hotkeys)
	}

	// L key (keyboard) or L2 (gamepad) toggles the hotkey menu (not when main menu open — L2 is tab nav there)
	if rl.IsKeyPressed(rl.KeyL) || (gpAvailable() && !c.menuOpen && gpPressed(rl.GamepadButtonLeftTrigger2)) {
		c.hotkeyActive = !c.hotkeyActive
		if c.hotkeyActive {
			c.hotkeyDirCode = 0
			c.hotkeyFadeOut = 0
		} else {
			c.hotkeyFadeOut = 1.0
		}
	}

	if !c.hotkeyActive {
		return false
	}

	// D-pad navigation (keyboard arrows or gamepad D-pad)
	dirs := [4]bool{
		rl.IsKeyPressed(rl.KeyUp) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceUp)),
		rl.IsKeyPressed(rl.KeyLeft) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceLeft)),
		rl.IsKeyPressed(rl.KeyRight) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceRight)),
		rl.IsKeyPressed(rl.KeyDown) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceDown)),
	}

	for dir := 0; dir < 4; dir++ {
		if !dirs[dir] {
			continue
		}
		entry := findHotkeyEntry(gs.Hotkeys, c.hotkeyDirCode)
		if entry == nil {
			continue
		}
		slot := entry.Slots[dir]
		if slot.Label == "" && slot.Msg == "" {
			continue
		}

		// Submenu (label=" ") → navigate into child
		if slot.Label == " " {
			kids := c.hotkeyTree.children[c.hotkeyDirCode]
			if kids[dir] != 0 {
				c.hotkeyDirCode = kids[dir]
			}
		} else {
			// Leaf → execute action and close menu
			c.executeHotkeyAction(slot.Label, slot.Msg)
			c.hotkeyActive = false
			c.hotkeyFadeOut = 1.0
			c.hotkeyCloseTime = 0.25 // suppress arrow movement briefly
			return true
		}
	}

	// Escape/Backspace/Circle: go back to parent, or close if at root
	if rl.IsKeyPressed(rl.KeyEscape) || rl.IsKeyPressed(rl.KeyBackspace) ||
		(gpAvailable() && gpPressed(rl.GamepadButtonRightFaceRight)) {
		if c.hotkeyDirCode > 0 {
			if p, ok := c.hotkeyTree.parent[c.hotkeyDirCode]; ok {
				c.hotkeyDirCode = p
			}
		} else {
			c.hotkeyActive = false
			c.hotkeyFadeOut = 1.0
		}
	}

	return true
}

// executeHotkeyAction executes a hotkey slot action.
// Labels starting with "|" are client command codes:
//
//	|0=wave, |1=bow, |2=point, |3=cheer (emotes)
//	|4=invite, |5=boot, |6=leave group
//	|14=say mode, |15=group mode, |16=shout mode, |17=guild mode
//	|21=assist leader, |22=assist target, |23=roll 0-99
//	|24-30=pet commands
//
// Otherwise the label text is sent as a chat message in the current mode.
func (c *Client) executeHotkeyAction(label, msg string) {
	if label == "" {
		return
	}

	if strings.HasPrefix(label, "|") {
		code := label[1:]
		switch code {
		case "0", "1", "2", "3": // PlayEmote: wave, bow, point, cheer
			c.playEmote(int(code[0] - '0'))
		case "4":
			if c.targetObjectID != 0 {
				c.engine.SendGroupInvite(c.targetObjectID, c.getTargetName())
			}
		case "5":
			if c.targetObjectID != 0 {
				c.engine.SendBootGroupMember(c.targetObjectID)
			}
		case "6":
			c.engine.SendLeaveGroup()
		case "14":
			c.chatDefaultMode = 0
			log.Printf("HOTKEY: Chat mode → Say")
		case "15":
			c.chatDefaultMode = 2
			log.Printf("HOTKEY: Chat mode → Group")
		case "16":
			c.chatDefaultMode = 1
			log.Printf("HOTKEY: Chat mode → Shout")
		case "17":
			c.chatDefaultMode = 3
			log.Printf("HOTKEY: Chat mode → Guild")
		case "21":
			c.engine.SendSay("/assist leader")
		case "22":
			c.engine.SendSay("/assist")
		case "23":
			c.engine.SendSay("/roll")
		case "24":
			c.engine.SendPetCommand(0)
		case "25":
			c.engine.SendPetCommand(1)
		case "26":
			c.engine.SendPetCommand(2)
		case "27":
			c.engine.SendPetCommand(3)
		case "28":
			c.engine.SendPetCommand(4)
		case "29":
			c.engine.SendPetCommand(5)
		case "30":
			c.engine.SendPetCommand(6)
		default:
			log.Printf("HOTKEY: unhandled command |%s (%s)", code, msg)
		}
	} else {
		// Regular text — substitute variables and send as chat
		text := c.substituteHotkeyVars(label)
		c.sendChatInCurrentMode(text)
	}
}

// substituteHotkeyVars replaces PS2 variables: %4=target name, %2=class, %3=level.
func (c *Client) substituteHotkeyVars(text string) string {
	gs := c.engine.State
	targetName := c.getTargetName()
	if targetName == "" {
		targetName = "target"
	}
	text = strings.ReplaceAll(text, "%4", targetName)
	text = strings.ReplaceAll(text, "%3", fmt.Sprintf("%d", gs.Player.Level))
	text = strings.ReplaceAll(text, "%2", className(int(gs.Player.Class)))
	return text
}

// getTargetName returns the name of the currently targeted entity.
func (c *Client) getTargetName() string {
	if c.targetObjectID == 0 {
		return ""
	}
	if ent, ok := c.engine.State.Entities[c.targetObjectID]; ok {
		return ent.Name
	}
	return ""
}

// sendChatInCurrentMode sends a message using the current chat channel.
func (c *Client) sendChatInCurrentMode(text string) {
	switch c.chatDefaultMode {
	case 0:
		c.engine.SendSay(text)
	case 1:
		c.engine.SendShout(text)
	case 2:
		c.engine.SendGroupChat(text)
	default:
		c.engine.SendSay(text) // guild not yet implemented
	}
}

// drawHotkeyMenu renders the hotkey overlay when active or fading out.
func (c *Client) drawHotkeyMenu() {
	if c.engine == nil {
		return
	}
	gs := c.engine.State
	if gs == nil || len(gs.Hotkeys) == 0 {
		return
	}

	alpha := byte(220)
	if !c.hotkeyActive {
		if c.hotkeyFadeOut <= 0 {
			return
		}
		c.hotkeyFadeOut -= rl.GetFrameTime() * 3.0
		if c.hotkeyFadeOut <= 0 {
			c.hotkeyFadeOut = 0
			return
		}
		alpha = byte(float32(alpha) * c.hotkeyFadeOut)
	}

	entry := findHotkeyEntry(gs.Hotkeys, c.hotkeyDirCode)
	if entry == nil {
		return
	}

	cx := float32(screenWidth) / 2
	cy := float32(screenHeight) / 2

	textColor := rl.Color{R: 240, G: 230, B: 200, A: alpha}
	dimColor := rl.Color{R: 120, G: 110, B: 90, A: alpha / 2}
	subColor := rl.Color{R: 180, G: 160, B: 100, A: alpha}

	boxW := float32(260)
	boxH := float32(180)
	bx := cx - boxW/2
	by := cy - boxH/2

	c.drawPanelTextured(int32(bx), int32(by), int32(boxW), int32(boxH), ui.FrameThin, ui.FillThin)

	// Title from parent's slot msg
	title := "Hotkeys"
	if c.hotkeyDirCode > 0 && c.hotkeyTree != nil {
		parentCode := c.hotkeyTree.parent[c.hotkeyDirCode]
		parentEntry := findHotkeyEntry(gs.Hotkeys, parentCode)
		if parentEntry != nil {
			kids := c.hotkeyTree.children[parentCode]
			for dir := 0; dir < 4; dir++ {
				if kids[dir] == c.hotkeyDirCode {
					title = parentEntry.Slots[dir].Msg
					break
				}
			}
		}
	}
	titleW := rl.MeasureText(title, 14)
	c.drawText(title, int32(cx)-titleW/2, int32(by)+4, 14, rl.Color{R: 160, G: 140, B: 90, A: alpha})

	// Direction positions: N=up, W=left, E=right, S=down
	type dirPos struct {
		x, y   float32
		alignX int // -1=left, 0=center, 1=right
	}
	positions := [4]dirPos{
		{cx, by + 28, 0},
		{bx + 14, cy - 8, -1},
		{bx + boxW - 14, cy - 8, 1},
		{cx, by + boxH - 38, 0},
	}

	for dir := 0; dir < 4; dir++ {
		slot := entry.Slots[dir]
		pos := positions[dir]

		displayText := slot.Msg
		if displayText == "" && slot.Label == "" {
			continue
		}
		if displayText == "" {
			displayText = slot.Label
		}

		isSubmenu := slot.Label == " "
		clr := textColor
		if isSubmenu {
			clr = subColor
			displayText = displayText + " >"
		}
		if slot.Label == "" {
			clr = dimColor
		}

		sz := int32(12)
		tw := rl.MeasureText(displayText, sz)
		var tx int32
		switch pos.alignX {
		case 0:
			tx = int32(pos.x) - tw/2
		case -1:
			tx = int32(pos.x)
		case 1:
			tx = int32(pos.x) - tw
		}
		c.drawText(displayText, tx, int32(pos.y), sz, clr)
	}

	// Arrow hints
	arrowColor := rl.Color{R: 100, G: 90, B: 70, A: alpha / 2}
	c.drawText("^", int32(cx)-3, int32(by)+18, 10, arrowColor)
	c.drawText("v", int32(cx)-3, int32(by+boxH)-26, 10, arrowColor)
	c.drawText("<", int32(bx)+4, int32(cy)-4, 10, arrowColor)
	c.drawText(">", int32(bx+boxW)-10, int32(cy)-4, 10, arrowColor)

	// Footer hints
	if c.hotkeyDirCode > 0 {
		c.drawText("ESC: Back", int32(bx)+8, int32(by+boxH)-14, 9, dimColor)
	}
	c.drawText("Hold L2/L", int32(bx+boxW)-72, int32(by+boxH)-14, 9, dimColor)
}
