package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	rl "github.com/gen2brain/raylib-go/raylib"
)

// promptForISO shows a screen asking the user to provide the ISO path.
// Supports in-window file browser, drag-and-drop, and typed path entry.
// Returns the validated ISO path or "" if the user closes the window.
func (c *Client) promptForISO() string {
	typedPath := ""
	errorMsg := ""
	cursorBlink := float64(0)

	// In-window file browser state.
	var fb *fileBrowser

	for !rl.WindowShouldClose() {
		cursorBlink += float64(rl.GetFrameTime())

		// If in-window file browser is active, run it.
		if fb != nil {
			result := fb.update(c)
			if result == fbResultCancel {
				fb = nil
			} else if result != fbResultNone {
				if validated := validateISO(string(result)); validated != "" {
					return validated
				}
				typedPath = string(result)
				errorMsg = "Not a valid ISO file (must be .iso, > 1 GB)"
				fb = nil
			}

			if fb != nil {
				rl.BeginDrawing()
				rl.ClearBackground(rl.Color{R: 10, G: 10, B: 10, A: 255})
				fb.draw(c)
				c.checkScreenshot()
				rl.EndDrawing()
				continue
			}
		}

		// Handle file drop
		if rl.IsFileDropped() {
			files := rl.LoadDroppedFiles()
			if len(files) > 0 {
				path := files[0]
				if validated := validateISO(path); validated != "" {
					return validated
				}
				typedPath = path
				errorMsg = "Not a valid ISO file"
			}
		}

		// Handle typed input
		if rl.IsKeyPressed(rl.KeyBackspace) {
			if len(typedPath) > 0 {
				typedPath = typedPath[:len(typedPath)-1]
			}
			errorMsg = ""
		}

		// Paste support (Ctrl+V)
		if (rl.IsKeyDown(rl.KeyLeftControl) || rl.IsKeyDown(rl.KeyRightControl)) && rl.IsKeyPressed(rl.KeyV) {
			clip := rl.GetClipboardText()
			if clip != "" {
				typedPath = strings.TrimSpace(clip)
				errorMsg = ""
			}
		}

		for ch := rl.GetCharPressed(); ch != 0; ch = rl.GetCharPressed() {
			if ch >= 32 && ch < 127 {
				typedPath += string(rune(ch))
				errorMsg = ""
			}
		}

		// Enter to confirm typed path or open browser
		if rl.IsKeyPressed(rl.KeyEnter) {
			if typedPath != "" {
				path := strings.TrimSpace(typedPath)
				path = strings.Trim(path, "\"'")
				if validated := validateISO(path); validated != "" {
					return validated
				}
				errorMsg = "File not found or not a valid ISO (must be > 1 GB)"
			} else {
				fb = newFileBrowser()
			}
		}

		// Draw
		rl.BeginDrawing()
		rl.ClearBackground(rl.Color{R: 10, G: 10, B: 10, A: 255})

		sw := float32(rl.GetScreenWidth())
		sh := float32(rl.GetScreenHeight())
		cx := sw / 2

		// Title
		c.drawText("EQOA Client", int32(cx-70), int32(sh*0.15), 20, rl.Color{R: 0, G: 212, B: 212, A: 255})

		// Prompt
		c.drawText("EQOA Frontiers ISO Required", int32(cx-130), int32(sh*0.28), 16, rl.White)

		// Browse button
		btnW := int32(200)
		btnH := int32(34)
		btnX := int32(cx) - btnW/2
		btnY := int32(sh * 0.38)
		rl.DrawRectangle(btnX, btnY, btnW, btnH, rl.Color{R: 0, G: 212, B: 212, A: 255})
		c.drawText("Browse for ISO...", btnX+35, btnY+9, 13, rl.Color{R: 0, G: 0, B: 0, A: 255})

		// Mouse click on browse button
		if rl.IsMouseButtonPressed(rl.MouseLeftButton) {
			mx := rl.GetMouseX()
			my := rl.GetMouseY()
			if mx >= btnX && mx <= btnX+btnW && my >= btnY && my <= btnY+btnH {
				fb = newFileBrowser()
			}
		}

		// Divider
		c.drawText("or drag-and-drop / paste path below:", int32(cx-155), int32(sh*0.48), 12, rl.Color{R: 120, G: 120, B: 120, A: 255})

		// Text input box
		boxX := int32(sw * 0.1)
		boxW := int32(sw * 0.8)
		boxY := int32(sh * 0.54)
		boxH := int32(28)
		rl.DrawRectangle(boxX, boxY, boxW, boxH, rl.Color{R: 26, G: 26, B: 26, A: 255})
		rl.DrawRectangleLines(boxX, boxY, boxW, boxH, rl.Color{R: 0, G: 212, B: 212, A: 255})

		// Text with cursor
		displayText := typedPath
		if int(cursorBlink*2)%2 == 0 {
			displayText += "|"
		}
		if displayText == "|" {
			displayText = "| paste or type ISO path, then press Enter"
		}
		c.drawText(displayText, boxX+6, boxY+6, 12, rl.Color{R: 200, G: 200, B: 200, A: 255})

		// Error message
		if errorMsg != "" {
			c.drawText(errorMsg, int32(cx-140), int32(sh*0.63), 13, rl.Color{R: 255, G: 80, B: 80, A: 255})
		}

		// Help text
		c.drawText("File: EverQuest - Online Adventures - Frontiers (USA).iso", int32(cx-230), int32(sh*0.76), 11, rl.Color{R: 100, G: 100, B: 100, A: 255})
		c.drawText("SLUS-20744  /  ~1.3 GB", int32(cx-90), int32(sh*0.81), 11, rl.Color{R: 100, G: 100, B: 100, A: 255})

		c.checkScreenshot()
		rl.EndDrawing()
	}

	return ""
}

// validateISO checks if the given path contains EQOA game assets.
// Accepts any file that has CHAR.ESF inside it (no extension requirement).
func validateISO(path string) string {
	path = strings.TrimSpace(path)
	path = strings.Trim(path, "\"'")
	if path == "" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return ""
	}
	if info.Size() < 500_000_000 {
		return ""
	}
	// Verify it contains EQOA game data by trying to read a known ESF file.
	if err := verifyISOPath(path); err != "" {
		log.Printf("ISO validation failed for %s: %s", path, err)
		return ""
	}
	log.Printf("ISO validated: %s (%d bytes)", path, info.Size())
	return path
}

// --- In-window file browser ---

const (
	fbResultNone   = ""
	fbResultCancel = "\x00cancel"
)

type fbEntry struct {
	name  string
	isDir bool
	size  int64
}

type fileBrowser struct {
	dir       string
	entries   []fbEntry
	scroll    int
	sel       int
	lastClick float64
	lastRow   int
}

func newFileBrowser() *fileBrowser {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/"
	}
	fb := &fileBrowser{dir: home}
	fb.loadDir(home)
	return fb
}

func (fb *fileBrowser) loadDir(dir string) {
	fb.dir = dir
	fb.entries = nil
	fb.scroll = 0
	fb.sel = 0

	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	// Directories first, then .iso files. Skip hidden files.
	var dirs, files []fbEntry
	for _, e := range dirEntries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if e.IsDir() {
			dirs = append(dirs, fbEntry{name: name, isDir: true})
		} else if strings.HasSuffix(strings.ToLower(name), ".iso") {
			info, _ := e.Info()
			size := int64(0)
			if info != nil {
				size = info.Size()
			}
			files = append(files, fbEntry{name: name, isDir: false, size: size})
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return strings.ToLower(dirs[i].name) < strings.ToLower(dirs[j].name) })
	sort.Slice(files, func(i, j int) bool { return strings.ToLower(files[i].name) < strings.ToLower(files[j].name) })
	fb.entries = append(dirs, files...)
}

func (fb *fileBrowser) update(c *Client) string {
	// Escape = cancel
	if rl.IsKeyPressed(rl.KeyEscape) {
		return fbResultCancel
	}

	// Backspace = go up
	if rl.IsKeyPressed(rl.KeyBackspace) {
		parent := filepath.Dir(fb.dir)
		if parent != fb.dir {
			fb.loadDir(parent)
		}
		return fbResultNone
	}

	visibleRows := (rl.GetScreenHeight() - 100) / 22
	if visibleRows < 1 {
		visibleRows = 1
	}

	// Navigation
	if rl.IsKeyPressed(rl.KeyUp) && fb.sel > 0 {
		fb.sel--
		if fb.sel < fb.scroll {
			fb.scroll = fb.sel
		}
	}
	if rl.IsKeyPressed(rl.KeyDown) && fb.sel < len(fb.entries)-1 {
		fb.sel++
		if fb.sel >= fb.scroll+int(visibleRows) {
			fb.scroll = fb.sel - int(visibleRows) + 1
		}
	}
	if rl.IsKeyPressed(rl.KeyPageUp) {
		fb.sel -= int(visibleRows)
		if fb.sel < 0 {
			fb.sel = 0
		}
		fb.scroll = fb.sel
	}
	if rl.IsKeyPressed(rl.KeyPageDown) {
		fb.sel += int(visibleRows)
		if fb.sel >= len(fb.entries) {
			fb.sel = len(fb.entries) - 1
		}
		if fb.sel < 0 {
			fb.sel = 0
		}
		fb.scroll = fb.sel - int(visibleRows) + 1
		if fb.scroll < 0 {
			fb.scroll = 0
		}
	}

	// Mouse scroll
	wheel := rl.GetMouseWheelMove()
	if wheel != 0 {
		fb.scroll -= int(wheel * 3)
		if fb.scroll < 0 {
			fb.scroll = 0
		}
		max := len(fb.entries) - int(visibleRows)
		if max < 0 {
			max = 0
		}
		if fb.scroll > max {
			fb.scroll = max
		}
	}

	// Mouse click — single click selects, double click opens.
	if rl.IsMouseButtonPressed(rl.MouseLeftButton) {
		mx := rl.GetMouseX()
		my := rl.GetMouseY()
		if mx >= 20 && mx <= int32(rl.GetScreenWidth())-20 && my >= 70 {
			row := int(my-70)/22 + fb.scroll
			if row >= 0 && row < len(fb.entries) {
				now := rl.GetTime()
				if row == fb.lastRow && now-fb.lastClick < 0.4 {
					// Double-click: open directory or select file.
					e := fb.entries[row]
					full := filepath.Join(fb.dir, e.name)
					if e.isDir {
						fb.loadDir(full)
						return fbResultNone
					}
					return full
				}
				fb.sel = row
				fb.lastClick = now
				fb.lastRow = row
			}
		}
	}

	// Enter = open dir or select file
	if rl.IsKeyPressed(rl.KeyEnter) && fb.sel >= 0 && fb.sel < len(fb.entries) {
		e := fb.entries[fb.sel]
		full := filepath.Join(fb.dir, e.name)
		if e.isDir {
			fb.loadDir(full)
			return fbResultNone
		}
		return full
	}

	return fbResultNone
}

func (fb *fileBrowser) draw(c *Client) {
	sw := int32(rl.GetScreenWidth())
	sh := int32(rl.GetScreenHeight())
	teal := rl.Color{R: 0, G: 212, B: 212, A: 255}
	gray := rl.Color{R: 150, G: 150, B: 150, A: 255}
	white := rl.White

	// Header
	c.drawText("Select EQOA ISO", 20, 10, 16, teal)
	c.drawText(fb.dir, 20, 35, 12, gray)
	c.drawText("Backspace: go up  |  Enter: open  |  Escape: cancel", 20, sh-22, 10, rl.Color{R: 80, G: 80, B: 80, A: 255})

	// Separator
	rl.DrawLine(20, 58, sw-20, 58, rl.Color{R: 40, G: 40, B: 40, A: 255})

	// Entries
	visibleRows := (sh - 100) / 22
	y := int32(70)
	for i := fb.scroll; i < len(fb.entries) && i < fb.scroll+int(visibleRows); i++ {
		e := fb.entries[i]

		// Selection highlight
		if i == fb.sel {
			rl.DrawRectangle(18, y-2, sw-36, 20, rl.Color{R: 0, G: 212, B: 212, A: 30})
		}

		if e.isDir {
			c.drawText("[DIR]", 24, y, 12, teal)
			c.drawText(e.name, 80, y, 12, white)
		} else {
			isoColor := rl.Color{R: 200, G: 200, B: 100, A: 255}
			sizeGB := float64(e.size) / (1024 * 1024 * 1024)
			sizeStr := fmt.Sprintf("%.1f GB", sizeGB)
			c.drawText("[ISO]", 24, y, 12, isoColor)
			c.drawText(e.name, 80, y, 12, isoColor)
			c.drawText(sizeStr, sw-120, y, 12, gray)
		}

		y += 22
	}

	if len(fb.entries) == 0 {
		c.drawText("No folders or .iso files found", 80, 80, 13, gray)
	}
}
