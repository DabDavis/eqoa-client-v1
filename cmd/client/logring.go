package main

import (
	"io"
	"log"
	"os"
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
const (
	reportLogLines   = 200
	reportLogLineMax = 1000 // a hex dump of a packet is not worth more than this
)

type logRing struct {
	mu    sync.Mutex
	lines [reportLogLines]string
	next  int
	full  bool
	part  string // a write that did not end its line yet
}

var clientLog = &logRing{}

// installLogRing sends the standard logger to stderr as before, and to the ring.
func installLogRing() {
	log.SetOutput(io.MultiWriter(os.Stderr, clientLog))
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
