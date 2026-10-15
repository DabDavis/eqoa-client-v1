package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// The log, as a problem report carries it.
//
// The client logs to stderr and nowhere else, so what a player's client said
// on the way to a bug is gone by the time anybody asks: a Windows player never
// sees the console, and nobody starts a game with its output redirected. The
// ring keeps the last reportLogLines lines in memory, and F3 writes them into
// the report (report.go). This is the only copy an ordinary player has.
//
// Something that logs every frame empties the ring in a few seconds, so a log
// line in a per-frame path costs every report its history.
//
// The ring dies with the process, so the log also goes to client.log beside
// the binary, and the last run's is kept as client.prev.log: a crash that
// takes the process with it is reported by the next run, which reads the log
// tail from there (crash.go). The file's first line names its run.
const (
	reportLogLines   = 200
	reportLogLineMax = 1000 // a hex dump of a packet is not worth more than this

	clientLogName     = "client.log"
	clientPrevLogName = "client.prev.log"
	clientLogTag      = "eqoa client log, run"
	clientLogMax      = 64 << 20
)

type logRing struct {
	mu    sync.Mutex
	lines [reportLogLines]string
	next  int
	full  bool
	part  string // a write that did not end its line yet
}

var clientLog = &logRing{}

// installLogRing sends the standard logger to the ring, to client.log in dir,
// and to stderr as before. Stderr goes last: io.MultiWriter stops at the
// first writer that fails, and the ring and the file never do.
func installLogRing(dir string) {
	path := filepath.Join(dir, clientLogName)
	prev := filepath.Join(dir, clientPrevLogName)
	os.Remove(prev)
	os.Rename(path, prev)

	out := []io.Writer{clientLog}
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); err == nil {
		fmt.Fprintf(f, "%s %s\n", clientLogTag, crashRun)
		out = append(out, &logFile{f: f})
	}
	out = append(out, os.Stderr)
	log.SetOutput(io.MultiWriter(out...))
}

// logFile is client.log: written straight through, so a crash loses nothing
// that was logged, and stopped at clientLogMax rather than filling a disk in
// a run that logs too much. It never fails a write.
type logFile struct {
	f    *os.File
	n    int64
	full bool
}

func (l *logFile) Write(p []byte) (int, error) {
	if l.full {
		return len(p), nil
	}
	if l.n+int64(len(p)) > clientLogMax {
		l.full = true
		l.f.WriteString("client.log stops here, at its size cap; the in-memory ring carries on\n")
		return len(p), nil
	}
	n, _ := l.f.Write(p)
	l.n += int64(n)
	return len(p), nil
}

// Write takes whatever the logger hands it. log.Logger writes one whole line
// per call, but nothing else that is handed the ring is held to that.
func (r *logRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	text := r.part + string(p)
	for {
		nl := strings.IndexByte(text, '\n')
		if nl < 0 {
			break
		}
		r.add(text[:nl])
		text = text[nl+1:]
	}
	if len(text) > reportLogLineMax {
		r.add(text)
		text = ""
	}
	r.part = text
	return len(p), nil
}

func (r *logRing) add(line string) {
	if len(line) > reportLogLineMax {
		line = line[:reportLogLineMax] + " [cut]"
	}
	r.lines[r.next] = line
	r.next = (r.next + 1) % reportLogLines
	if r.next == 0 {
		r.full = true
	}
}

// Tail is the ring's lines, oldest first.
func (r *logRing) Tail() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var out []string
	if r.full {
		out = append(out, r.lines[r.next:]...)
	}
	out = append(out, r.lines[:r.next]...)
	if r.part != "" {
		out = append(out, r.part)
	}
	return out
}
