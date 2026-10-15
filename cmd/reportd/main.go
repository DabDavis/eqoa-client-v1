// Command eqoa-reportd is the other end of the client's F3 "Report a Problem"
// dialog: POST /report stores what a player chose to send, and GET /board is
// a public, read-only page of those reports and what became of each.
//
// It is a port of the problem-report route and the report board in Dab's Mod's
// pdghostd (tools/pdghostd/pdghostd.py in the perfect-dark repo), which has
// been taking F3 reports from testers since 2026-09, and it keeps that
// daemon's rules: no account, nothing sent unless the player pressed Send,
// every field bounded and stripped of control characters, a report on disk as
// two files a person reads over ssh, and a board that reads back only the
// public part of each. What it leaves out is the patch notes and the monthly
// archive; see README.md.
//
// It sits behind nginx at /eqoa/reports/, which strips the prefix, so every
// path here is the path after it and every link the board writes is relative.
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// The bounds on a report, pdghostd's. Variables rather than constants so the
// tests can bring the big ones within reach.
var (
	// A report is ~100 KB of text and a picture the client has already
	// scaled down; this clears the worst of both with room to spare, and
	// nginx's client_max_body_size must clear it too.
	reportMaxBody int64 = 8 * 1024 * 1024
	reportMaxText       = 512 * 1024
	reportMaxNote       = 1000
	// The name a reporter asks to be credited by: optional, and twice the
	// client's own cap.
	reportMaxName = 64
	reportMaxShot = 5 * 1024 * 1024
	// version, platform and channel: pdghostd's CRASH_MAX_FIELD.
	fieldMax = 64

	// Reports an hour from one address. Keen testers file a zone at a time.
	reportWindow = time.Hour
	reportMax    = 120
	// A backstop against a full disk, not the budget: ~10 GB at the caps.
	// Counted by the shape of a report's name, so status.txt is not one.
	reportMaxFiles = 10000

	// Keys live in a rate table until something clears them, and one key is
	// one address. Past this many the expired ones are swept.
	rateTableMax = 4096
)

var pngMagic = []byte("\x89PNG\r\n\x1a\n")

// A report's name is its id everywhere, and reportIDRe is the only way an id
// from a URL becomes a filename: it is exactly the shape POST /report writes,
// so nothing that matches it can name anything but a report.
var (
	reportIDRe    = regexp.MustCompile(`\A[0-9]{8}-[0-9]{6}-[0-9a-f]{8}\z`)
	reportStampRe = regexp.MustCompile(`\A[0-9]{8}-[0-9]{6}\z`)
)

type server struct {
	reportDir string // <dir>/reports: <id>.txt, <id>.png and status.txt
	thumbDir  string // <dir>/reportthumbs, kept apart so a listing of reports is only reports

	reports rateTable // address -> reports sent
	shots   rateTable // address -> full-size board pictures fetched

	board boardCache

	thumbMu     sync.Mutex
	thumbFailed map[string]bool
}

func newServer(dir string) *server {
	return &server{
		reportDir:   filepath.Join(dir, "reports"),
		thumbDir:    filepath.Join(dir, "reportthumbs"),
		thumbFailed: map[string]bool{},
	}
}

func main() {
	home, _ := os.UserHomeDir()
	listen := flag.String("listen", "127.0.0.1:9091", "address to serve on; only nginx should reach it")
	dir := flag.String("dir", filepath.Join(home, "eqoareports"), "data directory (reports/, reportthumbs/)")
	flag.Parse()

	s := newServer(*dir)
	srv := &http.Server{
		Addr:    *listen,
		Handler: s,
		// An 8 MiB body over a slow line is the longest thing here.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 * 1024,
	}
	log.Printf("eqoa-reportd on %s, reports in %s", *listen, s.reportDir)
	log.Fatal(srv.ListenAndServe())
}

// ServeHTTP is the whole route table. It is not an http.ServeMux on purpose:
// the mux cleans "..", and answers with a redirect to the cleaned path, where
// a request for a picture that is not one should simply be a 404.
func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")

	path := strings.TrimRight(r.URL.Path, "/")
	switch r.Method {
	case http.MethodGet:
		if path == "" || path == "/ping" {
			sendJSON(w, http.StatusOK, struct {
				OK      bool   `json:"ok"`
				Service string `json:"service"`
				Version int    `json:"version"`
			}{true, "eqoa-reportd", 1})
			return
		}
		if path == "/board" || path == "/board.json" || strings.HasPrefix(path, "/board/") {
			s.boardGet(w, r, path)
			return
		}
	case http.MethodPost:
		if path == "/report" {
			s.postReport(w, r)
			return
		}
	}
	sendError(w, http.StatusNotFound, "no such endpoint")
}

type apiError struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

func sendError(w http.ResponseWriter, code int, msg string) {
	sendJSON(w, code, apiError{false, msg})
}

func sendJSON(w http.ResponseWriter, code int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		code, body = http.StatusInternalServerError, []byte(`{"ok":false,"error":"internal error"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write(body)
}

// clientIP is who is asking, as far as the rate limiter is concerned.
//
// X-Real-IP first, because the nginx in front of this sets it from
// $remote_addr and a client cannot influence it. X-Forwarded-For is read from
// the RIGHT: $proxy_add_x_forwarded_for appends the peer to whatever the
// client sent, so the first element is whatever the client wanted it to be
// and the last is the one our own proxy wrote. Both are only trustworthy
// because nothing reaches this except through the proxy, which is why it
// listens on loopback.
func clientIP(r *http.Request) string {
	if real := strings.TrimSpace(r.Header.Get("X-Real-IP")); real != "" {
		return real
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		parts := strings.Split(fwd, ",")
		return strings.TrimSpace(parts[len(parts)-1])
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// rateTable counts what each address has done within a window.
type rateTable struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

// ok reports whether key is under its limit, counting this call against it.
func (t *rateTable) ok(key string, window time.Duration, limit int) bool {
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.hits == nil {
		t.hits = map[string][]time.Time{}
	}
	if len(t.hits) > rateTableMax {
		for k, v := range t.hits {
			if len(v) == 0 || now.Sub(v[len(v)-1]) >= window {
				delete(t.hits, k)
			}
		}
	}
	var live []time.Time
	for _, at := range t.hits[key] {
		if now.Sub(at) < window {
			live = append(live, at)
		}
	}
	if len(live) >= limit {
		t.hits[key] = live
		return false
	}
	t.hits[key] = append(live, now)
	return true
}

// crashField is one field of a report as text a file can hold: cut to limit
// characters, and with the control characters taken out. A report is read in
// a terminal, and one that carries an escape sequence can rewrite the screen
// of whoever cats it. Newlines and tabs stay, because a report is lines.
func crashField(v string, limit int) string {
	if utf8.RuneCountInString(v) > limit {
		n := 0
		for i := range v {
			if n == limit {
				v = v[:i]
				break
			}
			n++
		}
	}
	return strings.Map(func(c rune) rune {
		if c == '\n' || c == '\t' || (c >= 0x20 && c < 0x7f) || c > 0x9f {
			return c
		}
		return -1
	}, v)
}

// readJSON is the body as a JSON object, or nil if it is not one: not JSON,
// JSON that is not an object (null included), something after the object,
// or a body past the limit.
func readJSON(w http.ResponseWriter, r *http.Request, limit int64) map[string]any {
	if r.ContentLength > limit || r.ContentLength == 0 {
		return nil
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		return nil
	}
	var req map[string]any
	if json.Unmarshal(data, &req) != nil || req == nil {
		return nil
	}
	return req
}

// textField is one string field of a request, bounded and cleaned. Absent is
// empty - clients that predate a field do not send it - and anything that is
// not a string, null included, is not ok.
func textField(req map[string]any, key string, limit int) (string, bool) {
	v, present := req[key]
	if !present {
		return "", true
	}
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	return crashField(s, limit), true
}

// reportShot is a report's screenshot as PNG bytes: empty for none, ok false
// for one that is bad. Only a PNG is kept - the file is opened by whoever
// reads the report, and a picture that is not what it claims to be has no
// business on disk.
func reportShot(v any) ([]byte, bool) {
	if v == nil {
		return nil, true
	}
	s, ok := v.(string)
	if !ok {
		return nil, false
	}
	if s == "" {
		return nil, true
	}
	// Go's decoder skips line breaks where pdghostd's strict one refuses them.
	if len(s) > reportMaxShot*4/3+8 || strings.ContainsAny(s, "\r\n") {
		return nil, false
	}
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(data) > reportMaxShot || !bytes.HasPrefix(data, pngMagic) {
		return nil, false
	}
	return data, true
}

// reportCount is how many reports are on disk (their .txt halves), or -1 if
// the directory cannot be read. By the shape of the name, not the extension:
// status.txt lives in the same directory and is not a report.
func (s *server) reportCount() int {
	entries, err := os.ReadDir(s.reportDir)
	if err != nil {
		return -1
	}
	n := 0
	for _, e := range entries {
		if name := e.Name(); strings.HasSuffix(name, ".txt") && reportIDRe.MatchString(strings.TrimSuffix(name, ".txt")) {
			n++
		}
	}
	return n
}

// headerLine is a field as one line of the header. pdghostd writes version,
// platform and channel as they come; a newline in one would start a header
// line of the sender's choosing, so here it is a space.
func headerLine(v string) string {
	v = strings.ReplaceAll(v, "\n", " ")
	if v == "" {
		return "-"
	}
	return v
}

func (s *server) postReport(w http.ResponseWriter, r *http.Request) {
	req := readJSON(w, r, reportMaxBody)
	if req == nil {
		sendError(w, http.StatusBadRequest, "bad body")
		return
	}

	report, ok1 := textField(req, "report", reportMaxText)
	note, ok2 := textField(req, "note", reportMaxNote)
	name, ok3 := textField(req, "name", reportMaxName)
	version, ok4 := textField(req, "version", fieldMax)
	platform, ok5 := textField(req, "platform", fieldMax)
	channel, ok6 := textField(req, "channel", fieldMax)
	if !(ok1 && ok2 && ok3 && ok4 && ok5 && ok6) {
		sendError(w, http.StatusBadRequest, "bad body")
		return
	}
	shot, ok := reportShot(req["screenshot"])
	if !ok {
		sendError(w, http.StatusBadRequest, "bad screenshot")
		return
	}

	// The dump alone is kilobytes; anything much shorter is not one.
	if utf8.RuneCountInString(strings.TrimSpace(report)) < 32 {
		sendError(w, http.StatusBadRequest, "empty report")
		return
	}

	ip := clientIP(r)
	if !s.reports.ok(ip, reportWindow, reportMax) {
		sendError(w, http.StatusTooManyRequests, "too many reports from here, try again later")
		return
	}

	if err := os.MkdirAll(s.reportDir, 0o700); err != nil {
		log.Printf("problem report not written: %v", err)
		sendError(w, http.StatusInternalServerError, "no report directory")
		return
	}
	count := s.reportCount()
	if count < 0 {
		sendError(w, http.StatusInternalServerError, "no report directory")
		return
	}
	if count >= reportMaxFiles {
		sendError(w, http.StatusInsufficientStorage, "no room for more reports")
		return
	}

	var rnd [4]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		sendError(w, http.StatusInternalServerError, "could not store the report")
		return
	}
	now := time.Now().UTC()
	base := now.Format("20060102-150405") + "-" + hex.EncodeToString(rnd[:])

	shotName := "-"
	if len(shot) > 0 {
		shotName = base + ".png"
	}
	// The note goes on one header line: that line is what `head` shows
	// across a listing.
	var head strings.Builder
	head.WriteString("received: " + now.Format("2006-01-02 15:04:05") + "\n")
	head.WriteString("from: " + ip + "\n")
	head.WriteString("version: " + headerLine(version) + "\n")
	head.WriteString("platform: " + headerLine(platform) + "\n")
	head.WriteString("channel: " + headerLine(channel) + "\n")
	head.WriteString("screenshot: " + shotName + "\n")
	head.WriteString("name: " + headerLine(strings.TrimSpace(strings.ReplaceAll(name, "\n", " "))) + "\n")
	head.WriteString("note: " + headerLine(note) + "\n\n")
	head.WriteString(report)
	if !strings.HasSuffix(report, "\n") {
		head.WriteString("\n")
	}

	// The picture first and the text last, so that a .txt on disk always
	// has its picture - the board takes a .txt as the sign a report is there.
	var written []string
	err := func() error {
		if len(shot) > 0 {
			written = append(written, base+".png")
			if err := writeNew(filepath.Join(s.reportDir, base+".png"), shot); err != nil {
				return err
			}
		}
		written = append(written, base+".txt")
		return writeNew(filepath.Join(s.reportDir, base+".txt"), []byte(head.String()))
	}()
	if err != nil {
		log.Printf("problem report not written: %v", err)
		for _, n := range written {
			os.Remove(filepath.Join(s.reportDir, n))
		}
		sendError(w, http.StatusInternalServerError, "could not store the report")
		return
	}

	who := strings.TrimSpace(name)
	if who == "" {
		who = "-"
	}
	log.Printf("problem report %s from %s (%s, %s, %d bytes, picture %d bytes)",
		base, who, headerLine(version), headerLine(platform), len(report), len(shot))
	sendJSON(w, http.StatusOK, struct {
		OK bool   `json:"ok"`
		ID string `json:"id"`
	}{true, base})
}

// writeNew creates path, which must not exist yet, readable by its owner only.
func writeNew(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return errors.New("a report of that name is already there")
		}
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
