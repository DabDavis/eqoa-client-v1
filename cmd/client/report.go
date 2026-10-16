package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
	"unsafe"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/eqoa/client/internal/game"
	"github.com/eqoa/client/internal/models"
	"github.com/eqoa/client/internal/patcher"
)

// Report a Problem: the F3 key, the same thing Dab's Mod has.
//
// F3 captures the frame on screen and a dump of the client's state (the
// player, the target, what is nearby, the UI, the settings and the last lines
// of the log) and writes both into traces/ beside the binary. A dialog then
// comes up over the game with the keyboard already typing into a note, and
// Send puts the dump, the note and the picture on the wire to the report
// server's /report (cmd/reportd), which keeps them for Dab and shows the note
// and picture on a public board.
//
// Nothing is sent unless the player presses Send, and Close leaves the files
// in traces/. The game does not pause - it is an MMO - but while the dialog is
// up nothing under it gets a key or a click (reportBeginFrame).
//
// A report also carries an optional name, which is the only thing that can
// credit the person who sent it: nothing else in a report identifies anybody,
// on purpose. The account name and password are scrubbed out of the dump, and
// so is the home directory, which carries the player's name on every desktop.
// The name is typed once and kept in client.json as report_name.

const (
	// The report server, behind nginx on the VPS. client.json's
	// report_server overrides it, which is how a build is pointed at a local
	// copy of cmd/reportd for testing.
	reportServerDefault = "https://texturepacks.art/eqoa/reports"

	// What the server keeps of each; nothing past these would be read.
	reportMaxNote = 1000
	reportMaxName = 32
	// The picture is scaled to at most this wide before it is encoded: the
	// board shows a thumbnail, and a 4K frame is several MB of PNG.
	reportShotWidth   = 1280
	reportSendTimeout = 60 * time.Second
	// How long a sent report's dialog stays up saying so.
	reportSentLinger = 5 * time.Second
	// How many of the nearest entities the dump lists.
	reportNearby = 40
)

// processStart is when the client started, for the dump's uptime.
var processStart = time.Now()

type reportState int

const (
	reportIdle reportState = iota
	reportBusy
	reportSent
	reportFailed
)

const (
	reportFieldNote = iota
	reportFieldName
)

// What the dialog is for. A crash report has no name and no picture, and is
// sent to /crash, which nothing reads back (crash.go).
const (
	reportKindProblem     = iota // F3
	reportKindCrashOffer         // what earlier runs left, offered on the login screen
	reportKindCrashScreen        // the crash that just happened; the client exits after
)

const (
	reportActSend = iota
	reportActClose
	reportActDelete
)

// reportCapture is one F3: the dump at once, and the picture once the
// goroutine that encodes it closes ready. Nothing in it changes after that,
// so the send goroutine and the dialog read it without a lock.
type reportCapture struct {
	when  time.Time
	stamp string // names both files: eqoa-<stamp>.txt and .png
	dump  string
	ready chan struct{}
	shot  []byte // PNG, or nil when the frame could not be read
	saved string // where the files went, or "" if they could not be written
}

type reportResult struct {
	id  string
	err error
}

type reportDialog struct {
	open      bool
	kind      int
	cap       *reportCapture // a problem report's capture
	crashes   []string       // a crash report's files, sent and deleted together
	crashWhen time.Time      // when the (newest) crash happened
	crashErr  string         // the crash screen's panic
	note      string
	name      string // the kept name, as client.json has it
	nameEdit  string // the name field while it is being typed
	field     int
	padUsed   bool

	// This frame's typed characters, taken at the top of the frame so that
	// no text box drawn under the dialog sees them.
	chars []rune

	state    reportState
	err      string
	id       string
	closeAt  time.Time
	resultCh chan reportResult

	server string
	loaded bool
}

func (c *Client) reportOpen() bool { return c.report.open }

// reportLoadConfig reads the kept name and the server override, once.
func (c *Client) reportLoadConfig() {
	r := &c.report
	if r.loaded {
		return
	}
	r.loaded = true
	cfg := loadLoginConfig()
	r.name = cleanReportText(cfg.ReportName, reportMaxName)
	r.server = strings.TrimRight(strings.TrimSpace(cfg.ReportServer), "/")
	if r.server == "" {
		r.server = reportServerDefault
	}
}

// reportBeginFrame runs at the top of every frame of the login and game
// loops, before any input handler or draw. While the dialog is open it takes
// the frame's typed characters for itself and moves the mouse out of reach of
// everything drawn under it: widgets read clicks while they draw
// (widgets.go), and a click meant for Send must not also press a menu button
// beneath it. reportFrame puts the mouse back before the dialog reads it.
func (c *Client) reportBeginFrame() {
	r := &c.report
	if r.resultCh != nil {
		select {
		case res := <-r.resultCh:
			r.resultCh = nil
			if res.err != nil {
				r.state = reportFailed
				r.err = res.err.Error()
			} else {
				r.state = reportSent
				r.id = res.id
				r.closeAt = time.Now().Add(reportSentLinger)
			}
		default:
		}
	}
	if !r.open {
		return
	}
	r.chars = r.chars[:0]
	for ch := rl.GetCharPressed(); ch != 0; ch = rl.GetCharPressed() {
		r.chars = append(r.chars, ch)
	}
	rl.SetMouseOffset(-1<<20, -1<<20)
}

// reportFrame runs once a frame just before EndDrawing, when the frame is
// complete. F3 takes the picture here - before the dialog is drawn, so the
// dialog is never in it - and the dialog reads its input and draws itself.
func (c *Client) reportFrame() {
	r := &c.report
	rl.SetMouseOffset(0, 0)

	shift := rl.IsKeyDown(rl.KeyLeftShift) || rl.IsKeyDown(rl.KeyRightShift)
	if !r.open && rl.IsKeyPressed(rl.KeyF3) && !shift {
		if r.state == reportBusy {
			log.Printf("report: the last report is still being sent")
		} else {
			c.reportCaptureFrame()
		}
		return // drawn from the next frame, which is the first one it owns
	}
	if !r.open {
		return
	}
	if r.state == reportSent && time.Now().After(r.closeAt) {
		c.reportClose()
		return
	}
	c.reportInput()
	if r.open {
		c.drawReport()
	}
}

// reportCaptureFrame is F3: the picture and the dump now, the encoding and
// the files on a goroutine, and the dialog open.
func (c *Client) reportCaptureFrame() {
	c.reportLoadConfig()
	now := time.Now()
	cp := &reportCapture{
		when:  now,
		stamp: now.Format("20060102-150405"),
		ready: make(chan struct{}),
	}
	pix, w, h := reportReadScreen()
	cp.dump = c.scrubReport(c.reportDump(now))

	go func() {
		defer close(cp.ready)
		if pix != nil {
			img := &image.RGBA{Pix: pix, Stride: w * 4, Rect: image.Rect(0, 0, w, h)}
			var buf bytes.Buffer
			enc := png.Encoder{CompressionLevel: png.BestSpeed}
			if err := enc.Encode(&buf, img); err != nil {
				log.Printf("report: the picture could not be encoded: %v", err)
			} else {
				cp.shot = buf.Bytes()
			}
		}
		cp.saved = writeReportFiles(cp)
	}()

	r := &c.report
	*r = reportDialog{
		open:   true,
		cap:    cp,
		name:   r.name,
		server: r.server,
		loaded: r.loaded,
		chars:  r.chars,
		field:  reportFieldNote,
	}
	// A text box focused under the dialog would take its backspaces.
	c.auctionSearchFocus = false
	c.groupTyping = false
	log.Printf("report: captured the frame (%dx%d) and the state for eqoa-%s", w, h, cp.stamp)
}

// reportReadScreen copies the framebuffer out, scaled to at most
// reportShotWidth wide. raylib's own ToImage keeps C memory it never frees,
// so the pixels are copied out and the image unloaded here.
//
// The 2D batch is flushed first: raylib queues every rectangle, texture and
// glyph until EndDrawing, so without it the picture is the 3D world with no
// HUD, menu or chat on it - only the meshes draw at once.
func reportReadScreen() (pix []byte, w, h int) {
	rl.DrawRenderBatchActive()
	img := rl.LoadImageFromScreen()
	if img == nil || img.Data == nil || img.Width <= 0 || img.Height <= 0 {
		return nil, 0, 0
	}
	defer rl.UnloadImage(img)
	if img.Format != rl.UncompressedR8g8b8a8 {
		return nil, 0, 0
	}
	if img.Width > reportShotWidth {
		rl.ImageResize(img, reportShotWidth, int32(int64(img.Height)*reportShotWidth/int64(img.Width)))
	}
	w, h = int(img.Width), int(img.Height)
	pix = make([]byte, w*h*4)
	copy(pix, unsafe.Slice((*byte)(img.Data), w*h*4))
	return pix, w, h
}

// writeReportFiles writes the dump and the picture into traces/ beside the
// binary, and says where, or "" if they could not be written. A report is
// sent from memory, so a read-only install still sends.
func writeReportFiles(cp *reportCapture) string {
	dir := "traces"
	if exe, err := os.Executable(); err == nil {
		dir = filepath.Join(filepath.Dir(exe), "traces")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("report: %v", err)
		return ""
	}
	base := filepath.Join(dir, "eqoa-"+cp.stamp)
	if err := os.WriteFile(base+".txt", []byte(cp.dump), 0644); err != nil {
		log.Printf("report: %v", err)
		return ""
	}
	if cp.shot != nil {
		if err := os.WriteFile(base+".png", cp.shot, 0644); err != nil {
			log.Printf("report: %v", err)
		}
	}
	return dir
}

func (c *Client) reportClose() {
	r := &c.report
	c.reportKeepName()
	r.open = false
	r.chars = r.chars[:0]
}

// reportKeepName finishes the name field: trimmed, and written to
// client.json at once if it changed. An emptied field never replaces a kept
// name - that is a slip of the backspace key - so the name comes back.
func (c *Client) reportKeepName() {
	r := &c.report
	if r.field != reportFieldName {
		return
	}
	r.field = reportFieldNote
	name := strings.TrimSpace(r.nameEdit)
	if name == "" || name == r.name {
		return
	}
	r.name = name
	cfg := loadLoginConfig()
	cfg.ReportName = name
	saveLoginConfig(cfg)
	log.Printf("report: name saved to client.json")
}

func (c *Client) reportSetField(field int) {
	r := &c.report
	if field == r.field {
		return
	}
	if field == reportFieldName {
		r.nameEdit = r.name
		r.field = reportFieldName
	} else {
		c.reportKeepName()
	}
}

func (c *Client) reportTyping() bool {
	s := c.report.state
	return s != reportBusy && s != reportSent
}

// reportInput is the dialog's keyboard, pad and mouse.
func (c *Client) reportInput() {
	r := &c.report

	if rl.IsKeyPressed(rl.KeyEscape) {
		c.reportClose()
		return
	}
	if gpAvailable() {
		if gpPressed(rl.GamepadButtonRightFaceRight) {
			c.reportClose()
			return
		}
		if gpPressed(rl.GamepadButtonRightFaceDown) {
			r.padUsed = true
			c.reportStartSend()
		}
	}
	if r.state == reportSent {
		if rl.IsKeyPressed(rl.KeyEnter) || rl.IsKeyPressed(rl.KeyKpEnter) {
			c.reportClose()
		}
		return
	}

	lay := c.reportLayout()
	if rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
		mx, my := rl.GetMouseX(), rl.GetMouseY()
		switch {
		case lay.note.contains(mx, my):
			c.reportSetField(reportFieldNote)
		case r.kind == reportKindProblem && lay.name.contains(mx, my):
			c.reportSetField(reportFieldName)
		}
		for _, b := range lay.buttons {
			if b.box.contains(mx, my) {
				c.reportAction(b.action)
				return
			}
		}
	}

	if !c.reportTyping() {
		return
	}
	if r.kind == reportKindProblem && rl.IsKeyPressed(rl.KeyTab) {
		if r.field == reportFieldNote {
			c.reportSetField(reportFieldName)
		} else {
			c.reportSetField(reportFieldNote)
		}
	}
	if rl.IsKeyPressed(rl.KeyEnter) || rl.IsKeyPressed(rl.KeyKpEnter) {
		if r.field == reportFieldName {
			c.reportSetField(reportFieldNote) // ENTER finishes the name
		} else {
			c.reportStartSend()
		}
		return
	}

	text, limit := &r.note, reportMaxNote
	if r.kind != reportKindProblem {
		limit = crashMaxNote
	}
	if r.field == reportFieldName {
		text, limit = &r.nameEdit, reportMaxName
	}
	if (rl.IsKeyPressed(rl.KeyBackspace) || rl.IsKeyPressedRepeat(rl.KeyBackspace)) && len(*text) > 0 {
		*text = (*text)[:len(*text)-1]
	}
	ctrl := rl.IsKeyDown(rl.KeyLeftControl) || rl.IsKeyDown(rl.KeyRightControl)
	if ctrl && rl.IsKeyPressed(rl.KeyV) {
		*text = cleanReportText(*text+rl.GetClipboardText(), limit)
	}
	for _, ch := range r.chars {
		if ch >= 32 && ch < 127 && len(*text) < limit {
			*text += string(ch)
		}
	}
}

func (c *Client) reportAction(action int) {
	r := &c.report
	switch action {
	case reportActSend:
		c.reportStartSend()
	case reportActClose:
		c.reportClose()
	case reportActDelete:
		// Not while a send is reading them.
		if r.state != reportBusy {
			deleteCrashReports(r.crashes)
			r.crashes = nil
			c.reportClose()
		}
	}
}

// reportStartSend puts the report on the wire from a goroutine; the result
// comes back through resultCh to reportBeginFrame, on the main goroutine.
func (c *Client) reportStartSend() {
	r := &c.report
	if r.state == reportBusy || r.state == reportSent {
		return
	}
	if (r.kind == reportKindProblem && r.cap == nil) || (r.kind != reportKindProblem && len(r.crashes) == 0) {
		return
	}
	c.reportKeepName()
	r.state = reportBusy
	r.err = ""
	ch := make(chan reportResult, 1)
	r.resultCh = ch
	kind, cp, files := r.kind, r.cap, append([]string(nil), r.crashes...)
	note, name, server := strings.TrimSpace(r.note), r.name, r.server
	go func() {
		var id string
		var err error
		if kind == reportKindProblem {
			id, err = sendReport(server, cp, note, name)
		} else {
			err = sendCrashReports(server, files, note)
			id = fmt.Sprintf("%d crash report(s)", len(files))
		}
		if err != nil {
			log.Printf("report: not sent: %v", err)
		} else {
			log.Printf("report: sent as %s", id)
		}
		ch <- reportResult{id, err}
	}()
}

// sendReport POSTs one report and returns the id the server filed it under.
func sendReport(server string, cp *reportCapture, note, name string) (string, error) {
	<-cp.ready

	channel := buildChannel()
	header := fmt.Sprintf("EQOA client problem report\nversion: %s\nchannel: %s\ntrace: eqoa-%s.txt\nname: %s\nnote: %s\n\n--- trace ---\n",
		BuildVersion, channel, cp.stamp, orDash(name), orDash(strings.ReplaceAll(note, "\n", " ")))
	body, err := json.Marshal(map[string]string{
		"version":    BuildVersion,
		"platform":   runtime.GOOS + "/" + runtime.GOARCH,
		"channel":    channel,
		"name":       name,
		"note":       note,
		"screenshot": base64.StdEncoding.EncodeToString(cp.shot),
		"report":     header + cp.dump,
	})
	if err != nil {
		return "", err
	}

	client := &http.Client{Timeout: reportSendTimeout}
	resp, err := client.Post(server+"/report", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", errors.New("the report server could not be reached")
	}
	defer resp.Body.Close()
	var answer struct {
		OK    bool   `json:"ok"`
		ID    string `json:"id"`
		Error string `json:"error"`
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	json.Unmarshal(data, &answer)
	if resp.StatusCode != http.StatusOK || !answer.OK {
		if answer.Error != "" {
			return "", errors.New(answer.Error)
		}
		return "", fmt.Errorf("the server answered %d", resp.StatusCode)
	}
	return answer.ID, nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// cleanReportText keeps printable ASCII, which is all the font draws and all
// the board needs, cut to limit.
func cleanReportText(s string, limit int) string {
	var b strings.Builder
	for _, ch := range s {
		if ch == '\n' || ch == '\t' {
			ch = ' '
		}
		if ch >= 32 && ch < 127 && b.Len() < limit {
			b.WriteRune(ch)
		}
	}
	return b.String()
}

// --- Drawing ---

type reportRect struct{ x, y, w, h int32 }

func (r reportRect) contains(x, y int32) bool {
	return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

type reportButton struct {
	box    reportRect
	label  string
	action int
}

type reportLayoutBoxes struct {
	panel, note, name reportRect
	buttons           []reportButton
	intro             []string
	pad, textW        int32
}

const (
	reportFont       = int32(14)
	reportSmallFont  = int32(12)
	reportLineH      = int32(17)
	reportNoteLines  = 6
	reportCrashLines = 3
)

func (c *Client) reportTitle() string {
	switch c.report.kind {
	case reportKindCrashOffer:
		return "Send a Crash Report?"
	case reportKindCrashScreen:
		return "The client crashed"
	}
	return "Report a Problem"
}

// reportIntro says what is in a report before it is offered: a player
// deciding whether to send their log should be told that it is their log.
func (c *Client) reportIntro() string {
	r := &c.report
	const holds = "the error and where in the code it happened, the build, the game's state, " +
		"your settings and the last lines of the log"
	switch r.kind {
	case reportKindCrashOffer:
		s := "The client crashed on " + r.crashWhen.Format("Mon 2 Jan at 15:04") + "."
		if n := len(r.crashes); n > 1 {
			s += fmt.Sprintf(" %d reports are waiting, and Send sends them all.", n)
		}
		return s + " A report holds " + holds + " - nothing else, and nothing unless you press Send."
	case reportKindCrashScreen:
		return trimTo(r.crashErr, 200) + "\n\nA report of this was saved. Sending it to Dab includes " + holds + "."
	}
	return "Captured the frame at " + r.cap.when.Format("15:04:05") + ": a screenshot and a dump of the game's state, " +
		"your settings and the last lines of the log. Nothing is sent unless you press Send."
}

// reportButtons is the row of buttons, left to right, with Send's label
// saying how the send is going.
func (c *Client) reportButtons() []reportButton {
	r := &c.report
	send := [...]string{"Send", "Send to Dab", "Send report to Dab"}[r.kind]
	switch r.state {
	case reportBusy:
		send = "Sending..."
	case reportSent:
		send = "Sent"
	case reportFailed:
		send = "Try again"
	}
	b := func(w int32, label string, action int) reportButton {
		return reportButton{reportRect{w: w, h: 30}, label, action}
	}
	switch r.kind {
	case reportKindCrashOffer:
		return []reportButton{b(140, send, reportActSend), b(110, "Not now", reportActClose), b(100, "Delete", reportActDelete)}
	case reportKindCrashScreen:
		return []reportButton{b(180, send, reportActSend), b(100, "Close", reportActClose)}
	}
	return []reportButton{b(120, send, reportActSend), b(100, "Close", reportActClose)}
}

// reportLayout places the dialog in the middle of the screen, the same
// boxes for drawing and for clicks. Its height follows what it has to say.
func (c *Client) reportLayout() reportLayoutBoxes {
	r := &c.report
	sw, sh := int32(rl.GetScreenWidth()), int32(rl.GetScreenHeight())
	pw := int32(560)
	if pw > sw-32 {
		pw = sw - 32
	}
	l := reportLayoutBoxes{pad: 16}
	l.textW = pw - 2*l.pad
	for _, para := range strings.Split(c.reportIntro(), "\n") {
		l.intro = append(l.intro, c.reportWrap(para, reportSmallFont, l.textW)...)
	}

	// Offsets from the panel's top-left first, then placed.
	lines := int32(reportNoteLines)
	if r.kind != reportKindProblem {
		lines = reportCrashLines
	}
	note := reportRect{0, 40 + int32(len(l.intro))*15 + 34, l.textW, lines*reportLineH + 10}
	y := note.y + note.h
	var name reportRect
	if r.kind == reportKindProblem {
		name = reportRect{0, y + 28, l.textW, 26}
		y = name.y + name.h
	}
	buttonY := y + 22
	ph := buttonY + 30 + 12 + 3*15 + 4 // the buttons, then up to three lines of status

	l.panel = reportRect{(sw - pw) / 2, max((sh-ph)/2, 0), pw, ph}
	x, top := l.panel.x+l.pad, l.panel.y
	l.note = reportRect{x, top + note.y, note.w, note.h}
	if name.h > 0 {
		l.name = reportRect{x, top + name.y, name.w, name.h}
	}
	for _, b := range c.reportButtons() {
		b.box.x, b.box.y = x, top+buttonY
		l.buttons = append(l.buttons, b)
		x += b.box.w + 12
	}
	return l
}

func (c *Client) drawReport() {
	r := &c.report
	l := c.reportLayout()
	p := l.panel
	x := p.x + l.pad

	drawPanel(p.x, p.y, p.w, p.h, colorPopupBg, colorBorder)
	c.drawText(c.reportTitle(), x, p.y+12, 18, colorGoldTitle)
	y := p.y + 40
	for _, ln := range l.intro {
		c.drawText(ln, x, y, reportSmallFont, colorLightGray)
		y += 15
	}

	typing := c.reportTyping()
	blink := int(rl.GetTime()*2)%2 == 0

	// The note.
	label, hint := "What went wrong?", "  (ENTER sends, ESC closes)"
	if r.kind != reportKindProblem {
		label, hint = "What were you doing? (optional)", "  (ENTER sends)"
	}
	if typing && r.field == reportFieldNote {
		label += hint
	}
	c.drawText(label, x, l.note.y-20, reportFont, colorGold)
	border := colorBtnBorder
	if typing && r.field == reportFieldNote {
		border = colorGoldBright
	}
	drawPanel(l.note.x, l.note.y, l.note.w, l.note.h, colorFieldBg, border)
	note := r.note
	if typing && r.field == reportFieldNote && blink {
		note += "_"
	}
	lines := c.reportWrap(note, reportFont, l.note.w-12)
	if shown := int(l.note.h-10) / int(reportLineH); len(lines) > shown {
		lines = lines[len(lines)-shown:] // the end, where the typing is
	}
	if r.note == "" && !(typing && r.field == reportFieldNote) {
		lines = []string{"No note."}
	}
	for i, ln := range lines {
		c.drawText(ln, l.note.x+6, l.note.y+5+int32(i)*reportLineH, reportFont, colorWhiteFull)
	}

	// The name, and what it is for: a field called "Name" on a bug report
	// reads like something the game needs rather than an offer. A crash
	// report is not on the board, so it has none.
	if r.kind == reportKindProblem {
		label = "Your name, for the credits on the report board (optional, TAB)"
		if typing && r.field == reportFieldName {
			label = "Your name, kept for next time (ENTER: done)"
		} else if r.padUsed && r.name == "" {
			label = "Your name: optional, typed on a keyboard"
		}
		c.drawText(label, x, l.name.y-20, reportFont, colorGold)
		border = colorBtnBorder
		if typing && r.field == reportFieldName {
			border = colorGoldBright
		}
		drawPanel(l.name.x, l.name.y, l.name.w, l.name.h, colorFieldBg, border)
		name := r.name
		if typing && r.field == reportFieldName {
			name = r.nameEdit
			if blink {
				name += "_"
			}
		} else if name == "" {
			name = "anonymous"
		}
		c.drawText(name, l.name.x+6, l.name.y+6, reportFont, colorWhiteFull)
	}

	mx, my := rl.GetMouseX(), rl.GetMouseY()
	for _, b := range l.buttons {
		bg := colorBtnBg
		if b.box.contains(mx, my) {
			bg = colorBtnHover
		}
		drawPanel(b.box.x, b.box.y, b.box.w, b.box.h, bg, colorBorder)
		c.drawTextInRect(b.label, b.box.x, b.box.y+8, b.box.w, reportFont, colorGoldBright)
	}

	// What happened to it.
	var status string
	color := colorDim
	switch r.state {
	case reportBusy:
		status = "Sending..."
	case reportSent:
		color = rl.Color{R: 120, G: 220, B: 120, A: 255}
		if r.kind == reportKindProblem {
			where := strings.TrimPrefix(strings.TrimPrefix(r.server, "https://"), "http://")
			status = "Sent as " + r.id + ". Thank you! See all reports at " + where + "/board"
		} else {
			status = "Sent. Thank you - that is the whole of what Dab gets to see of a crash."
		}
	case reportFailed:
		color = rl.Color{R: 255, G: 110, B: 110, A: 255}
		status = "Not sent: " + r.err + "."
		if r.kind != reportKindProblem {
			status += " The report is still saved, and the client offers it again next time it starts."
		} else if r.savedDir() != "" {
			status += " The files are still in traces/."
		}
	default:
		switch r.kind {
		case reportKindCrashOffer:
			status = "Not now keeps it for next time; Delete throws it away."
		case reportKindCrashScreen:
			status = "Close keeps the report, and the client offers it again next time it starts."
		default:
			status = "TAB moves between the note and the name."
		}
	}
	y = l.buttons[0].box.y + 30 + 12
	for _, ln := range c.reportWrap(status, reportSmallFont, l.textW) {
		c.drawText(ln, x, y, reportSmallFont, color)
		y += 15
	}
}

// savedDir is where the capture's files went, once they are written.
func (r *reportDialog) savedDir() string {
	if r.cap == nil {
		return ""
	}
	select {
	case <-r.cap.ready:
		return r.cap.saved
	default:
		return ""
	}
}

// reportWrap folds text into lines no wider than maxW, at spaces where it
// can. Unlike wrapText it keeps the text as typed - a trailing space shows -
// and caches nothing, since the note changes every keystroke.
func (c *Client) reportWrap(text string, size, maxW int32) []string {
	var lines []string
	for text != "" {
		if c.measureText(text, size) <= maxW {
			lines = append(lines, text)
			break
		}
		cut := 1
		for cut < len(text) && c.measureText(text[:cut+1], size) <= maxW {
			cut++
		}
		if sp := strings.LastIndexByte(text[:cut], ' '); sp > 0 {
			cut = sp + 1
		}
		lines = append(lines, strings.TrimRight(text[:cut], " "))
		text = text[cut:]
	}
	if len(lines) == 0 {
		lines = []string{""}
	}
	return lines
}

// --- The dump ---

// reportDump is the client's state as F3 found it, as text. Everything it
// reads is the main goroutine's, which is where F3 runs.
func (c *Client) reportDump(now time.Time) string {
	var b strings.Builder
	f := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }

	channel := buildChannel()
	f("eqoa client trace %s", now.Format("2006-01-02 15:04:05 -0700"))
	f("build: %s (%s), %s %s/%s", BuildVersion, channel, runtime.Version(), runtime.GOOS, runtime.GOARCH)
	if gmEnabled() {
		f("gm build: yes")
	}
	f("uptime: %s", now.Sub(processStart).Round(time.Second))
	f("screen: %dx%d, framebuffer %dx%d, window mode %d, vsync %v, fps cap %d, fps %d (frame %.1f ms)",
		rl.GetScreenWidth(), rl.GetScreenHeight(), rl.GetRenderWidth(), rl.GetRenderHeight(),
		c.windowMode, c.vsyncOn, c.fpsCap, rl.GetFPS(), rl.GetFrameTime()*1000)

	f("")
	f("[session]")
	switch {
	case c.offline != nil:
		f("mode: offline")
	case c.engine == nil:
		f("mode: not connected (login screen)")
	}
	f("server: %s", c.serverIP)
	f("rules: %s", models.ActiveRules)
	if rec := patcher.ReadRecord(moduleRecord(c.serverIP)); rec != nil {
		f("patch module: %d files, patched %s", len(rec.Files), rec.Patched.Format(time.RFC3339))
	} else {
		f("patch module: none recorded")
	}
	if c.patchNotice != "" {
		f("patch: %s", c.patchNotice)
	}
	var gs *game.GameState
	if c.engine != nil {
		gs = c.engine.State
	}
	if gs != nil {
		f("phase: %v", gs.Phase)
		f("account id: %d", gs.AccountID)
		f("master session: %v, world entry acked: %v, memory dumps: %d", gs.MasterSession, gs.WorldEntryAcked, gs.MemoryDumpCount)
		if gs.StatusMsg != "" {
			f("status: %s", gs.StatusMsg)
		}
		if gs.ZoneTransferActive {
			f("zone transfer: state %d to %q", gs.ZoneTransferState, gs.ZoneTransferZone)
		}
	}

	if gs != nil && gs.Phase == game.PhaseInWorld {
		c.reportDumpWorld(f, gs)
	} else if gs != nil && gs.Phase == game.PhaseCharSelect {
		f("")
		f("[character select]")
		f("characters: %d, selected %d", len(gs.Characters), c.charSelectIdx)
	}

	f("")
	f("[ui]")
	f("menu open %v (tab %d), chat typing %v, world map %v, minimap %v, inspect %v",
		c.menuOpen, c.menuTab, c.chatTyping, c.worldMapOpen, c.minimapOn, c.inspectOpen)
	if gs != nil {
		f("dialogue %v, merchant %v, blacksmith %v", gs.DialogueActive, gs.MerchantOpen, gs.BlacksmithOpen)
		if gs.DialogueActive {
			f("dialogue text: %q (%d options)", trimTo(gs.DialogueText, 120), len(gs.DialogueOptions))
		}
	}
	if gmEnabled() {
		f("gm: menu %v, noclip %v, fast run %v, invis %v", c.gmMenuOpen, c.gmNoClip, c.gmFastRun, c.gmInvis)
	}

	f("")
	f("[settings]")
	f("max zoom %.1f, camera sensitivity %.2f, invert pitch %v, camera collision %v, fixed follow %v, free pitch %v, remember zoom %v",
		c.camMaxDist, c.camSensitivity, c.invertPitch, c.camCollision, c.camFixedFollow, c.camFreePitch, c.camRememberZoom)
	f("volume master %.2f, sfx %.2f, bgm %.2f (muted %v), show fps %v",
		c.masterVolume, c.sfxVolume, c.bgmVolume, c.bgmMuted, c.showFPS)
	if c.isoPath != "" {
		f("iso: %s", filepath.Base(c.isoPath))
	} else {
		f("iso: none (assets directory)")
	}
	if c.currentBGM != "" {
		f("bgm: %s", c.currentBGM)
	}

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	f("")
	f("[runtime]")
	f("goroutines %d, heap %d MB in use of %d MB from the OS, %d GCs",
		runtime.NumGoroutine(), ms.HeapAlloc>>20, ms.Sys>>20, ms.NumGC)

	tail := clientLog.Tail()
	f("")
	f("[log] last %d lines", len(tail))
	for _, ln := range tail {
		b.WriteString(ln)
		b.WriteByte('\n')
	}
	return b.String()
}

func (c *Client) reportDumpWorld(f func(string, ...any), gs *game.GameState) {
	p := &gs.Player
	worldNames := map[byte]string{0: "Tunaria", 1: "Rathe", 2: "Odus", 3: "Lavastorm", 4: "Plane of Sky", 5: "Secrets"}

	f("")
	f("[player]")
	f("character: %s, object 0x%08X, level %d %s %s, sex %d, model 0x%08X",
		p.Name, gs.PlayerObjectID, p.Level, raceName(int(p.Race)), className(int(p.Class)), p.Sex, p.ModelID)
	f("world: %d %s (client %d), zone path %q", p.World, worldNames[p.World], c.currentWorld, gs.ZonePath)
	zones := make([]int, 0, len(c.loadedZoneIdxs))
	for z := range c.loadedZoneIdxs {
		zones = append(zones, z)
	}
	sort.Ints(zones)
	f("terrain zone: %d, loaded %v, loading overlay %.2f", c.terrainZoneIdx, zones, c.loadScrFade)
	f("position: %.2f %.2f %.2f, facing %.3f rad", p.X, p.Y, p.Z, p.Facing)
	f("hp %d/%d, power %d/%d, dead %v, breath timer %.1f", p.HPInt, p.MaxHPInt, p.PowerInt, p.MaxPowerInt, p.Dead, c.breathTimer)
	f("speed %.2f (server %.3f), velocity y %.2f, on ground %v, water %d, liquid 0x%X, auto-run %v, follow %v",
		c.currentSpeed, p.Speed, c.velocityY, c.onGround, c.waterState, c.liquidFlags, c.autoRun, c.engine.Following())
	f("camera: mode %d (%s), angle %.3f, pitch %.3f, distance %.2f (%.2f..%.2f), flags 0x%X, walker pitch %.3f, turn rate %.3f, panel %v, dead %v",
		c.wcam.mode, cameraModeName(c.wcam.mode), c.camAngle, c.camPitch, c.camDist, c.wcam.minOrbitDist, c.wcam.maxOrbitDist,
		c.wcam.flags, c.wcam.pitch, c.wcam.turnRate, c.wcam.panelCam, c.wcam.dead)
	if c.weather != nil {
		f("game hour %.2f, weather %v -> %v, intensity %.2f", gs.GameHour(), c.weather.Type, c.weather.TargetType, c.weather.Intensity)
	}
	if c.logoutActive {
		f("logging out: %.1f s left", time.Until(c.logoutTime).Seconds())
	}

	if t, ok := gs.Entities[c.targetObjectID]; ok && c.targetObjectID != 0 {
		f("")
		f("[target]")
		f("%s", reportEntityLine(t, p))
		f("con color %d, face %d, info valid %v", gs.TargetConColor, gs.TargetFaceColor, gs.TargetInfoValid)
	}
	kind, effect, delay := c.engine.Swing()
	f("auto-attack %v (toggle mode %v), swing kind %d, effect 0x%08X, delay %d ms",
		c.engine.CombatEngaged, c.engine.AutoAttackToggle, kind, effect, delay)

	ents := make([]*game.Entity, 0, len(gs.Entities))
	for _, e := range gs.Entities {
		if e != nil && e.ObjectID != gs.PlayerObjectID {
			ents = append(ents, e)
		}
	}
	sort.Slice(ents, func(i, j int) bool { return reportDist(ents[i], p) < reportDist(ents[j], p) })
	f("")
	f("[nearby] %d entities, the nearest %d", len(ents), min(len(ents), reportNearby))
	for _, e := range ents[:min(len(ents), reportNearby)] {
		f("%s", reportEntityLine(e, p))
	}
}

func reportDist(e, p *game.Entity) float64 {
	dx, dy, dz := float64(e.X-p.X), float64(e.Y-p.Y), float64(e.Z-p.Z)
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

func reportEntityLine(e, p *game.Entity) string {
	kind := "npc"
	if e.IsPlayer {
		kind = "player"
	}
	flags := ""
	if e.Dead {
		flags += " dead"
	}
	if e.CombatEngaged {
		flags += " combat"
	}
	return fmt.Sprintf("0x%08X %7.1f  %-24q lvl %2d %-6s model 0x%08X anim %2d hp %3d/%3d  at %.1f %.1f %.1f ch %d%s",
		e.ObjectID, reportDist(e, p), trimTo(e.Name, 24), e.Level, kind, e.ModelID, e.Animation,
		e.HP, e.MaxHP, e.X, e.Y, e.Z, e.Channel, flags)
}

func trimTo(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// scrubReport takes out of a report what would identify the player or hand
// over their account: the home directory (the player's name on every
// desktop), the account name and the password. The log lines that printed
// the password-derived auth token no longer print it (internal/network); this
// is the second line of defence, for whatever logs them next.
func (c *Client) scrubReport(text string) string {
	return scrubText(text, c.loginUser, c.loginPass)
}

func scrubText(text, user, pass string) string {
	if home, err := os.UserHomeDir(); err == nil && len(home) > 1 {
		text = strings.ReplaceAll(text, home, "~")
	}
	// Under four characters a password is as likely to be part of a word.
	if len(pass) >= 4 {
		text = strings.ReplaceAll(text, pass, "<password>")
	}
	// Case-sensitive, so a character named after the account ("Dolph" for
	// "dolph") keeps its name: the dump is no use without it.
	if user != "" {
		re := regexp.MustCompile(`\b` + regexp.QuoteMeta(user) + `\b`)
		text = re.ReplaceAllString(text, "<account>")
	}
	return text
}
