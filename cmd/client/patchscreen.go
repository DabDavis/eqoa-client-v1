package main

import (
	"log"
	"path/filepath"
	"strings"
	"time"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/eqoa/client/internal/game"
	"github.com/eqoa/client/internal/models"
	"github.com/eqoa/client/internal/patcher"
	"github.com/eqoa/client/internal/ui"
)

// The patch step is SETUP's: the title's "Play" runs VISetup::RunPatcher —
// DNAS, then the patcher — and only a finished patch goes on to STATION's
// login. DNAS is not this client's to run; the patcher is
// internal/patcher, ported from the decomp, run in the background while
// this screen is VIWndPatcher (decomp/modules/setup/src/viwndpatcher.cpp)
// over the title painting.
//
// Where the PS2 has one card folder, and so patches it over to play on
// another server, this client keeps a patch module per server
// (saves/<server>/) and loads it on demand: a check made in the background
// at launch (patcher.Check, the patcher's own decision with nothing written)
// lets a module that is already current load with no patch at all, and one
// whose server cannot be reached load as it was saved. The patch window runs
// only when there is something to fetch or delete, or no module yet.

// patchOutcome is how the patch screen ends: RunPatcher's 3 (the game goes
// on) or 4 (back to the title; a failed patch's "Restart" restarts the
// game on a PS2, which here is the title again).
type patchOutcome int

const (
	patchQuit patchOutcome = iota // the window closed
	patchPlay
	patchBack
)

// patchScreen is VIWndPatcher's state; m_state and m_progress live in the
// patcher's Status.
type patchScreen struct {
	status     *patcher.Status
	sel        int  // m_sel: 0 View Messages, 1 Play (or Restart)
	confirm    bool // the "close your network connection?" box is open
	confirmSel int  // its choice: 0 Yes, 1 No — it opens on No
	notesOpen  bool // VIWndReadMessage: the release notes
	notesTop   int  // its first row shown
	notesShown bool // ShowPatchMessage has run
	outcome    patchOutcome
	done       bool
}

// patchCheck is the background check's answer.
type patchCheck struct {
	plan *patcher.Plan
	err  error
}

// patchConfig is the patcher's view of the configured server's module.
func (c *Client) patchConfig() patcher.Config {
	return patcher.Config{
		Host:      c.serverIP,
		Port:      sandstormPatchPort,
		Manifest:  c.psvrPath(),
		Dir:       saveDir(c.serverIP),
		FrameTime: time.Second / 30, // a MainLoop: two vsyncs
		Record:    moduleRecord(c.serverIP),
	}
}

// startPatchCheck checks the configured server's module in the background,
// once per Play: the title is up meanwhile.
func (c *Client) startPatchCheck() {
	if c.patchCheckCh != nil {
		return
	}
	cfg := c.patchConfig()
	ch := make(chan patchCheck, 1)
	c.patchCheckCh = ch
	go func() {
		plan, err := patcher.Check(cfg)
		ch <- patchCheck{plan, err}
	}()
}

// runPatchScreen loads the configured server's patch module, patching it
// first when it needs it, and returns how it ended. A loaded module is
// applied (the rules, the art, the words) before it returns.
func (c *Client) runPatchScreen() patchOutcome {
	cfg := c.patchConfig()
	ps := &patchScreen{status: patcher.NewStatus()}
	c.startPatchCheck()
	var res patchCheck
	for waiting := true; waiting; {
		select {
		case res = <-c.patchCheckCh:
			c.patchCheckCh = nil
			waiting = false
			continue
		default:
		}
		if rl.WindowShouldClose() {
			return patchQuit
		}
		c.patchFrame(ps, &patcher.Snapshot{State: patcher.StateConnecting})
	}
	c.patchNotice = ""
	switch {
	case res.err == nil && res.plan.Empty():
		log.Printf("patch: %s's module is current: loaded, nothing to patch", cfg.Host)
		c.applyPatch(cfg.Dir)
		return patchPlay
	case res.err != nil && patcher.HasModule(cfg.Dir):
		log.Printf("patch: %s: %v; loading its saved module", cfg.Host, res.err)
		c.patchNotice = "The patch server did not answer: playing with the saved patch."
		c.applyPatch(cfg.Dir)
		return patchPlay
	case res.err == nil:
		log.Printf("patch: %s: fetch %v, delete %v", cfg.Host, res.plan.Fetch, res.plan.Delete)
	}

	go patcher.Run(cfg, ps.status)
	for !rl.WindowShouldClose() && !ps.done {
		c.patchFrame(ps, nil)
	}
	if ps.outcome == patchPlay {
		c.applyPatch(cfg.Dir)
	}
	return ps.outcome
}

// patchFrame is one frame of the patch window: the player's keys, then the
// title painting and the window over it. over replaces the patcher's state
// while there is none yet.
func (c *Client) patchFrame(ps *patchScreen, over *patcher.Snapshot) {
	c.reportBeginFrame()
	if over == nil && !c.reportOpen() {
		ps.input()
	}
	c.audioSys.UpdateBGM()
	c.audioSys.UpdateAmbient()
	rl.BeginDrawing()
	rl.ClearBackground(rl.Black)
	if c.titleScr != nil && c.titleScr.loaded {
		c.titleScr.drawTitleScene(c, false)
	}
	snap := ps.status.Snapshot()
	if over != nil {
		snap = *over
	}
	ps.draw(c, snap)
	c.checkScreenshot()
	c.reportFrame()
	rl.EndDrawing()
}

// moduleRecord is where a server's module keeps its record: beside its card
// folder, in saves/<server>/.
func moduleRecord(server string) string {
	return filepath.Join(filepath.Dir(saveDir(server)), "patch.json")
}

// psvrPath is data\psvrpath.dat off the disc, the path of the manifest on
// the patch server: its first line, cut at CR, LF or a blank, as RunPatcher
// reads it. The PS2 retries until the file opens; a client without the disc
// uses the line every retail disc carries.
func (c *Client) psvrPath() string {
	if c.isoPath != "" {
		if data, err := readISOPath(c.isoPath, "DATA/PSVRPATH.DAT", 4096); err == nil {
			s := string(data)
			if len(s) > 255 {
				s = s[:255]
			}
			if i := strings.IndexAny(s, "\r\n "); i >= 0 {
				s = s[:i]
			}
			if s != "" {
				return s
			}
		} else {
			log.Printf("patcher: data\\psvrpath.dat: %v", err)
		}
	}
	return patcher.DefaultManifest
}

// applyPatch makes the session what the save folder's patch says it is: the
// rules out of its S2 (or the config's choice), its art and its string table.
// The PS2 gets the same by booting with the patched card: SS, then S2.
func (c *Client) applyPatch(dir string) {
	models.ActiveRules = models.RulesFor(dir, c.rulesSetting)
	game.WorldNameIDs = models.ActiveRules.WorldNameIDs()
	log.Printf("rules: %s", models.ActiveRules)
	if c.modelMgr != nil {
		c.modelMgr.SetSaveArt(dir)
	}
	c.loadWords(dir)
	c.saveArtFor = dir
}

// input is VIWndPatcher::OnKeyDown, VIWndMessage's and VIWndReadMessage's:
// keyboard and pad.
func (ps *patchScreen) input() {
	snap := ps.status.Snapshot()
	up := rl.IsKeyPressed(rl.KeyUp) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceUp))
	down := rl.IsKeyPressed(rl.KeyDown) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceDown))
	left := rl.IsKeyPressed(rl.KeyLeft) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceLeft))
	right := rl.IsKeyPressed(rl.KeyRight) || (gpAvailable() && gpPressed(rl.GamepadButtonLeftFaceRight))
	ok := rl.IsKeyPressed(rl.KeyEnter) || (gpAvailable() && gpPressed(rl.GamepadButtonRightFaceDown))
	back := rl.IsKeyPressed(rl.KeyEscape) || (gpAvailable() && gpPressed(rl.GamepadButtonRightFaceUp))

	switch {
	case snap.State == patcher.StateFailed || snap.State == patcher.StateAuthFailed:
		if ok { // "Restart"
			ps.finish(patchBack)
		}
	case snap.State != patcher.StateDone:
		// Nothing answers a key while the patcher runs.
	case ps.notesOpen:
		switch {
		case up && ps.notesTop > 0:
			ps.notesTop--
		case down:
			ps.notesTop++ // clamped where the rows are counted
		case back:
			ps.notesOpen = false
		}
	case ps.confirm:
		switch {
		case left:
			ps.confirmSel = 0
		case right:
			ps.confirmSel = 1
		case ok:
			ps.confirm = false
			if ps.confirmSel == 0 { // Yes: the patch window closes, back to the title
				ps.finish(patchBack)
			}
		}
	default:
		hasNotes := snap.Notes != ""
		switch {
		case up && hasNotes && ps.sel > 0:
			ps.sel--
		case down && hasNotes && ps.sel < 1:
			ps.sel++
		case ok && ps.sel == 0 && hasNotes:
			ps.notesOpen, ps.notesTop = true, 0
		case ok:
			ps.finish(patchPlay)
		case back:
			ps.confirm, ps.confirmSel = true, 1
		}
	}
}

func (ps *patchScreen) finish(o patchOutcome) {
	ps.outcome = o
	ps.done = true
}

// The window, VIUIWindow(320, 246, 430 wide, 8 rows) on the PS2's 640×448,
// here in pixels about the middle of the screen, as the login is.
const (
	patchWinW  = int32(430)
	patchRowH  = int32(22)
	patchRows  = int32(9)
	patchTextX = int32(40)
	patchFont  = int32(16)
)

// draw is VIWndPatcher::OnDrawSelf (0x00578F98), and the two windows it
// opens over itself.
func (ps *patchScreen) draw(c *Client, snap patcher.Snapshot) {
	c.drawFakeSplashInterface()
	if snap.State == patcher.StateCancelled {
		return // phase -1 draws the title bar alone
	}
	if snap.State == patcher.StateDone && snap.Changed && !ps.notesShown {
		// ShowPatchMessage: something was written, so the notes open, with
		// Play chosen behind them.
		ps.notesShown = true
		ps.sel = 1
		ps.notesOpen, ps.notesTop = true, 0
	}
	if snap.State == patcher.StateDone && snap.Notes == "" {
		ps.sel = 1 // "|-": View Messages is grey and cannot be chosen
	}

	x := (screenWidth - patchWinW) / 2
	h := patchRows*patchRowH + 16
	y := patchWindowY(h)
	c.patchWindow(x, y, patchWinW, h, c.word(811, "Update"))
	row := func(r int32) int32 { return y + 8 + r*patchRowH }

	// "Status: ..." The PS2 builds "..." for 3, 5 and 6 and then formats
	// over it, so the dots never show (viwndpatcher.cpp).
	status := c.word(803, "Status:")
	if s := patchStateText(c, snap.State); s != "" {
		status += " " + s
	}
	c.drawText(status, x+patchTextX, row(1), patchFont, rl.White)
	c.drawPatchProgress(x+patchTextX, row(3), patchWinW-2*patchTextX, snap.Progress)

	if snap.State == patcher.StateDone {
		gold, dim := colorGoldWarm, rl.Color{R: 120, G: 120, B: 120, A: 255}
		view, play := c.word(813, "View Messages"), c.word(790, "Play")
		viewColor := rl.White
		if snap.Notes == "" {
			viewColor = dim
		} else if ps.sel == 0 {
			viewColor = gold
		}
		playColor := rl.White
		if ps.sel == 1 {
			playColor = gold
		}
		c.drawText(view, x+patchTextX, row(5), patchFont, viewColor)
		c.drawText(play, x+patchTextX, row(6), patchFont, playColor)
		c.drawText(">", x+patchTextX-16, row(int32(5+ps.sel)), patchFont, gold)
		c.drawText(patchHint(c, true), x+patchTextX, row(8), 14, rl.LightGray)
	} else {
		for i, line := range c.wrapText(c.word(763, "Do not remove memory card (PS2) in MEMORY CARD slot 1."), patchFont, patchWinW-2*patchTextX) {
			if i < 2 {
				c.drawText(line, x+patchTextX, row(int32(5+i)), patchFont, rl.White)
			}
		}
	}

	switch snap.State {
	case patcher.StateFailed, patcher.StateAuthFailed:
		text := c.word(769, "Errors were encountered while updating. Please verify your network settings are correct and try again.")
		if snap.State == patcher.StateAuthFailed {
			text = c.word(787, "One or more updates failed authentication. Please verify your network settings are correct and try again.")
		}
		c.patchMessageBox(c.word(767, "Error"), text, []string{c.word(793, "Restart")}, 0, 480)
	}
	if ps.notesOpen {
		ps.drawNotes(c, snap.Notes)
	}
	if ps.confirm {
		c.patchMessageBox(c.word(785, "Notice"),
			c.word(810, "This will close your network connection. Are you sure you want to continue?"),
			[]string{c.word(21, "Yes"), c.word(22, "No")}, ps.confirmSel, 400)
	}
}

// drawNotes is VIWndReadMessage::OnDrawSelf (0x0057AD30): the notes, wrapped
// at 360, seven rows at a time, over the patch window.
func (ps *patchScreen) drawNotes(c *Client, notes string) {
	const visible = 7
	var lines []string
	for _, para := range strings.Split(strings.ReplaceAll(notes, "\r\n", "\n"), "\n") {
		if para == "" {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, c.wrapText(para, patchFont, 360)...)
	}
	maxTop := max(0, len(lines)-visible)
	ps.notesTop = min(ps.notesTop, maxTop)
	w, h := int32(440), int32(10)*patchRowH+16
	x, y := (screenWidth-w)/2, patchWindowY(h)
	c.patchWindow(x, y, w, h, c.word(789, "Update Messages"))
	for i := 0; i < visible && ps.notesTop+i < len(lines); i++ {
		c.drawText(lines[ps.notesTop+i], x+40, y+8+int32(1+i)*patchRowH, patchFont, rl.White)
	}
	if len(lines) > visible {
		drawScrollIndicator(x+w-30, y+8+patchRowH, float32(visible*int(patchRowH)), ps.notesTop, len(lines), visible, colorGoldWarm)
	}
	c.drawText(patchHint(c, false), x+40, y+8+9*patchRowH, 14, rl.LightGray)
}

// patchStateText is the status line's word for each phase of RunPatcher.
func patchStateText(c *Client, s patcher.State) string {
	switch s {
	case patcher.StateConnecting:
		return c.word(757, "Connecting to update server...")
	case patcher.StateChecking:
		return c.word(754, "Checking for updates...")
	case patcher.StateUpdating:
		return c.word(812, "Updating")
	case patcher.StateDone:
		return c.word(764, "Done.")
	case patcher.StateReading:
		return c.word(792, "Reading game data")
	case patcher.StateWriting:
		return c.word(814, "Writing game data")
	case patcher.StateFailed, patcher.StateAuthFailed:
		return c.word(768, "Error while updating.")
	}
	return ""
}

// patchHint is "|x Select   |t Back" (212, 750), with the button named for
// the pad when there is one and for the keyboard otherwise.
func patchHint(c *Client, selectToo bool) string {
	ok, back := "Enter", "Esc"
	if gpAvailable() {
		ok, back = "X", "Triangle"
	}
	h := back + " " + c.word(750, "Back")
	if selectToo {
		h = ok + " " + c.word(212, "Select") + "   " + h
	}
	return h
}

// word is WORDS.TXT string id, or the retail text when the table lacks it.
func (c *Client) word(id int, fallback string) string {
	if s := c.words.Get(id); s != "" {
		return s
	}
	return fallback
}

// patchWindowY puts a window of height h where the PS2 centres its patch
// windows: (320, 246) on its 640×448, a little below the middle, clear of the
// title's logo.
func patchWindowY(h int32) int32 {
	return screenHeight*246/448 - h/2
}

// patchWindow is a VIUIWindow with a title on its top edge, framed as the
// client's own menus are.
func (c *Client) patchWindow(x, y, w, h int32, title string) {
	c.drawPanelTextured(x, y, w, h, ui.FrameLine, ui.FillBlack)
	tw := c.measureText(title, patchFont)
	rl.DrawRectangle(x+(w-tw)/2-8, y-10, tw+16, 22, rl.Color{R: 20, G: 20, B: 32, A: 230})
	c.drawText(title, x+(w-tw)/2, y-7, patchFont, colorGoldWarm)
}

// patchMessageBox is VIWndMessage: a title, the text, and a row of choices,
// the chosen one lit.
func (c *Client) patchMessageBox(title, text string, choices []string, sel int, width int32) {
	lines := c.wrapText(text, patchFont, width-80)
	h := int32(len(lines)+3)*patchRowH + 16
	x, y := (screenWidth-width)/2, patchWindowY(h)
	c.patchWindow(x, y, width, h, title)
	for i, line := range lines {
		c.drawTextInRect(line, x+40, y+8+int32(1+i)*patchRowH, width-80, patchFont, rl.White)
	}
	cy := y + 8 + int32(len(lines)+2)*patchRowH
	for i, ch := range choices {
		col := rl.White
		if i == sel {
			col = colorGoldWarm
		}
		cx := x + width*int32(i+1)/int32(len(choices)+1)
		c.drawTextInRect(ch, cx-100, cy, 200, patchFont, col)
	}
}

// drawPatchProgress is VIUIWindow::DrawProgress: the bar.
func (c *Client) drawPatchProgress(x, y, w int32, p float32) {
	rl.DrawRectangle(x, y, w, 14, rl.Color{R: 10, G: 10, B: 16, A: 255})
	rl.DrawRectangle(x, y, int32(float32(w)*min(max(p, 0), 1)), 14, colorGoldWarm)
	rl.DrawRectangleLines(x, y, w, 14, rl.Color{R: 90, G: 80, B: 60, A: 255})
}

// drawFakeSplashInterface (0x00579D10) is the title screen's menu bar behind
// the patch window, Play lit: what the player chose to get here.
func (c *Client) drawFakeSplashInterface() {
	y := screenHeight * 360 / 448
	third := screenWidth / 3
	for i, item := range []string{c.word(790, "Play"), c.word(1635, "Network Setup"), c.word(422, "Options")} {
		col := rl.White
		if i == 0 {
			col = rl.Color{R: 255, G: 255, B: 0, A: 255}
			item = "< " + item + " >"
		}
		c.drawTextInRect(item, int32(i)*third, y, third, 18, col)
	}
}
