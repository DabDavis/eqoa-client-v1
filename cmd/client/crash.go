package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	rl "github.com/gen2brain/raylib-go/raylib"
)

// Crash reports: what Dab's Mod does when it dies (port/src/crashreport.c),
// for a Go client.
//
// A crash is written down one of two ways. A panic on the main goroutine -
// the game loop, which is where nearly all of the client runs - is recovered
// by crashRecover while the window is still up: the report is written at once
// with the panic, its stack, the state dump and the log ring, and a crash
// screen offers to send it. Anything else - a panic on another goroutine (the
// terrain loader, the network), a fatal runtime error, a fault in C under
// raylib - takes the process with it and nothing of ours runs. For those the
// runtime writes the report itself: debug.SetCrashOutput hands it this run's
// crashes/run-<id>.out, and the next run turns what it finds there, with the
// dead run's client.log, into a report (collectCrashes).
//
// Nothing is sent unless the player says so: on the crash screen, or when
// the next run offers what is waiting, on the login screen. A report that is
// sent is deleted; one that is not stays in crashes/ until it is sent or the
// player deletes it from that offer. A startup error the player fixes from the
// message (log.Fatalf: a bad argument) is not a crash and writes none.

const (
	crashMaxNote = 200
	// The server keeps 256 KiB; the report stays under it with room.
	crashMaxText = 240 * 1024
)

var (
	// crashRun names this run's files: run-<id>.out, and crash-<id>.txt if it
	// crashes. The stamp keeps a listing in order; the pid keeps two clients
	// started in the same second apart.
	crashRun = time.Now().Format("20060102-150405") + fmt.Sprintf("-%d", os.Getpid())
	crashDir string
	crashOut *os.File

	crashRunRe = regexp.MustCompile(`^run-([0-9]{8}-[0-9]{6}-[0-9]+)\.out$`)
)

// clientDir is the directory beside the binary, where client.json,
// screenshots/ and traces/ already live.
func clientDir() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe)
	}
	return "."
}

// installCrashCapture turns what earlier runs left in dir/crashes into
// reports, then points the runtime's crash output at this run's file.
func installCrashCapture(dir string) {
	crashDir = filepath.Join(dir, "crashes")
	if err := os.MkdirAll(crashDir, 0o700); err != nil {
		log.Printf("crash: no crash directory: %v", err)
		return
	}
	cfg := loadLoginConfig()
	user, pass := "", ""
	if cfg.Remember {
		user, pass = cfg.Username, cfg.Password
	}
	collectCrashes(crashDir, filepath.Join(dir, clientPrevLogName), user, pass)
	if n := len(pendingCrashes()); n > 0 {
		log.Printf("crash: %d report(s) waiting to be offered", n)
	}

	f, err := os.OpenFile(filepath.Join(crashDir, "run-"+crashRun+".out"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		log.Printf("crash: %v", err)
		return
	}
	// The header is the run's; the runtime writes after it, so a file with
	// nothing past its first blank line is a run that did not crash.
	fmt.Fprintf(f, "EQOA client run\nversion: %s\nchannel: %s\nplatform: %s/%s\nrun: %s\nstarted: %s\n\n",
		BuildVersion, buildChannel(), runtime.GOOS, runtime.GOARCH, crashRun, time.Now().Format("2006-01-02 15:04:05 -0700"))
	// Every goroutine: a fatal error such as a concurrent map write is about
	// two of them.
	debug.SetTraceback("all")
	if err := debug.SetCrashOutput(f, debug.CrashOptions{}); err != nil {
		log.Printf("crash: %v", err)
		f.Close()
		return
	}
	crashOut = f
}

// crashCleanExit is the end of a run that did not crash: its .out goes.
func crashCleanExit() {
	if crashOut == nil {
		return
	}
	debug.SetCrashOutput(nil, debug.CrashOptions{})
	crashOut.Close()
	os.Remove(crashOut.Name())
	crashOut = nil
}

func buildChannel() string {
	if UpdateChannel == "" {
		return "local"
	}
	return UpdateChannel
}

func crashHeader(version, channel, platform, run, when string) string {
	return fmt.Sprintf("EQOA client crash report\nversion: %s\nchannel: %s\nplatform: %s\nrun: %s\ntime: %s\n\n",
		orDash(version), orDash(channel), orDash(platform), run, when)
}

// crashFields reads the "key: value" lines of a header, up to its blank line.
func crashFields(text string) map[string]string {
	head, _, _ := strings.Cut(text, "\n\n")
	fields := map[string]string{}
	for _, line := range strings.Split(head, "\n") {
		if k, v, ok := strings.Cut(line, ": "); ok {
			fields[k] = v
		}
	}
	return fields
}

// collectCrashes turns every run-<id>.out an earlier run left in dir into
// crash-<id>.txt. One with nothing past its header was a run that ended
// without saying so - killed, or an os.Exit - and goes. One whose run also
// wrote a report itself crashed again on its crash screen, and that is added
// to the report. Otherwise the report is the runtime's own words, with the
// log tail of that run when client.prev.log is its log.
func collectCrashes(dir, prevLog, user, pass string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		m := crashRunRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		run, out := m[1], filepath.Join(dir, e.Name())
		data, err := readHead(out, crashMaxText)
		if err != nil {
			continue
		}
		head, body, _ := strings.Cut(string(data), "\n\n")
		body = strings.TrimSpace(body)
		if body == "" {
			os.Remove(out)
			continue
		}

		txt := filepath.Join(dir, "crash-"+run+".txt")
		var text string
		if old, err := os.ReadFile(txt); err == nil {
			text = string(old) + "\n--- then, while the crash screen was up ---\n" + body + "\n"
		} else {
			f := crashFields(head)
			when := "-"
			if info, err := e.Info(); err == nil {
				when = info.ModTime().Format("2006-01-02 15:04:05 -0700")
			}
			var b strings.Builder
			b.WriteString(crashHeader(f["version"], f["channel"], f["platform"], run, when))
			b.WriteString(body + "\n")
			if tail := logTailOf(prevLog, run); len(tail) > 0 {
				fmt.Fprintf(&b, "\n[log] last %d lines of that run\n", len(tail))
				b.WriteString(strings.Join(tail, "\n") + "\n")
			}
			text = b.String()
		}
		text = capCrashText(scrubText(text, user, pass))
		if os.WriteFile(txt, []byte(text), 0o600) == nil {
			os.Remove(out)
		}
	}
}

// readHead reads at most max bytes of a file.
func readHead(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, max))
}

// logTailOf is the last reportLogLines lines of the log at path, if its first
// line says it is the log of that run.
func logTailOf(path, run string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	first := make([]byte, 128)
	n, _ := io.ReadFull(f, first)
	firstLine, _, _ := strings.Cut(string(first[:n]), "\n")
	if firstLine != clientLogTag+" "+run {
		return nil
	}
	const window = 256 * 1024
	if info, err := f.Stat(); err == nil && info.Size() > window {
		f.Seek(info.Size()-window, io.SeekStart)
	} else {
		f.Seek(0, io.SeekStart)
	}
	data, _ := io.ReadAll(io.LimitReader(f, window))
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if lines[0] == firstLine {
		lines = lines[1:]
	}
	if len(lines) > reportLogLines {
		lines = lines[len(lines)-reportLogLines:]
	}
	return lines
}

// capCrashText keeps a report under crashMaxText: the head, where the error
// and its stack are, and the tail, where the log is.
func capCrashText(text string) string {
	if len(text) <= crashMaxText {
		return text
	}
	const tail = 80 * 1024
	return text[:crashMaxText-tail-64] + "\n\n[... cut to fit ...]\n\n" + text[len(text)-tail:]
}

// pendingCrashes is every report waiting to be sent, oldest first.
func pendingCrashes() []string {
	if crashDir == "" {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(crashDir, "crash-*.txt"))
	sort.Strings(files)
	return files
}

func deleteCrashReports(files []string) {
	for _, f := range files {
		os.Remove(f)
	}
	log.Printf("crash: %d report(s) deleted", len(files))
}

// crashRecover is deferred in runClient after the window's own cleanup, so it
// runs first, with the window still there to say what happened. It never
// returns from a crash: the process exits once the crash screen closes.
func (c *Client) crashRecover() {
	r := recover()
	if r == nil {
		return
	}
	stack := debug.Stack()
	msg := fmt.Sprint(r)
	// What the runtime would have printed, for whoever ran it from a terminal.
	fmt.Fprintf(os.Stderr, "panic: %s\n\n%s", msg, stack)

	path := c.writeCrashReport(msg, stack)
	if path == "" {
		// Nowhere to write it: let the runtime say it, on stderr at least.
		panic(r)
	}
	log.Printf("crash: %s, report %s", msg, filepath.Base(path))
	if rl.IsWindowReady() && !rl.WindowShouldClose() {
		c.crashScreen(msg, path)
	}
	crashCleanExit()
	os.Exit(2)
}

// writeCrashReport writes crash-<run>.txt: the panic, its stack, and the
// state dump with the log ring at its end. The dump reads state the panic
// may have left half-written, so a panic inside it costs the dump, not the
// report.
func (c *Client) writeCrashReport(msg string, stack []byte) string {
	if crashDir == "" {
		return ""
	}
	now := time.Now()
	var b strings.Builder
	b.WriteString(crashHeader(BuildVersion, buildChannel(), runtime.GOOS+"/"+runtime.GOARCH, crashRun,
		now.Format("2006-01-02 15:04:05 -0700")))
	b.WriteString("panic: " + msg + "\n\n")
	b.Write(stack)
	b.WriteString("\n--- state ---\n")
	b.WriteString(c.crashStateDump(now))

	path := filepath.Join(crashDir, "crash-"+crashRun+".txt")
	if err := os.WriteFile(path, []byte(capCrashText(c.scrubReport(b.String()))), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "crash: the report could not be written: %v\n", err)
		return ""
	}
	return path
}

func (c *Client) crashStateDump(now time.Time) (dump string) {
	defer func() {
		if r := recover(); r != nil {
			tail := clientLog.Tail()
			dump = fmt.Sprintf("(the state could not be read: %v)\n\n[log] last %d lines\n%s\n", r, len(tail), strings.Join(tail, "\n"))
		}
	}()
	return c.reportDump(now)
}

// crashScreen offers the report that was just written, over whatever the
// window last showed, until the player sends it or closes it.
func (c *Client) crashScreen(msg, path string) {
	// Whatever the frame was in the middle of is abandoned. Each End is safe
	// when its Begin was not called.
	rl.EndMode3D()
	rl.EndMode2D()
	rl.EndTextureMode()
	rl.EndScissorMode()
	rl.EndShaderMode()
	rl.EndBlendMode()
	rl.EndDrawing()

	c.reportLoadConfig()
	r := &c.report
	*r = reportDialog{
		open:      true,
		kind:      reportKindCrashScreen,
		crashes:   []string{path},
		crashErr:  msg,
		crashWhen: time.Now(),
		name:      r.name,
		server:    r.server,
		loaded:    r.loaded,
	}
	for !rl.WindowShouldClose() && r.open {
		c.reportBeginFrame()
		rl.BeginDrawing()
		rl.ClearBackground(rl.Color{R: 10, G: 10, B: 20, A: 255})
		c.reportFrame()
		rl.EndDrawing()
	}
}

// crashOfferOnce opens the offer of what earlier runs left, once a launch,
// on the login screen.
func (c *Client) crashOfferOnce() {
	if c.crashOffered || c.report.open {
		return
	}
	c.crashOffered = true
	files := pendingCrashes()
	if len(files) == 0 {
		return
	}
	when := time.Now()
	if info, err := os.Stat(files[len(files)-1]); err == nil {
		when = info.ModTime()
	}
	c.reportLoadConfig()
	r := &c.report
	*r = reportDialog{
		open:      true,
		kind:      reportKindCrashOffer,
		crashes:   files,
		crashWhen: when,
		name:      r.name,
		server:    r.server,
		loaded:    r.loaded,
	}
}

// sendCrashReports sends each report and deletes each one the server took,
// stopping at the first that it did not.
func sendCrashReports(server string, files []string, note string) error {
	for _, f := range files {
		if err := sendCrashFile(server, f, note); err != nil {
			return err
		}
		os.Remove(f)
	}
	return nil
}

func sendCrashFile(server, path, note string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return errors.New("the report could not be read")
	}
	text := string(data)
	f := crashFields(text)
	body, err := json.Marshal(map[string]string{
		"version":  f["version"],
		"platform": f["platform"],
		"channel":  f["channel"],
		"note":     note,
		"report":   text,
	})
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: reportSendTimeout}
	resp, err := client.Post(server+"/crash", "application/json", bytes.NewReader(body))
	if err != nil {
		return errors.New("the report server could not be reached")
	}
	defer resp.Body.Close()
	var answer struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	answerData, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	json.Unmarshal(answerData, &answer)
	if resp.StatusCode != http.StatusOK || !answer.OK {
		if answer.Error != "" {
			return errors.New(answer.Error)
		}
		return fmt.Errorf("the server answered %d", resp.StatusCode)
	}
	return nil
}
