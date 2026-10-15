package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestLogRingKeepsTheLastLines(t *testing.T) {
	r := &logRing{}
	for i := 0; i < reportLogLines+50; i++ {
		fmt.Fprintf(r, "line %d\n", i)
	}
	tail := r.Tail()
	if len(tail) != reportLogLines {
		t.Fatalf("%d lines kept, want %d", len(tail), reportLogLines)
	}
	if tail[0] != "line 50" || tail[len(tail)-1] != fmt.Sprintf("line %d", reportLogLines+49) {
		t.Fatalf("tail runs %q .. %q", tail[0], tail[len(tail)-1])
	}
}

func TestLogRingJoinsPartialWritesAndCutsLongLines(t *testing.T) {
	r := &logRing{}
	r.Write([]byte("half a "))
	r.Write([]byte("line\nand a start"))
	if tail := r.Tail(); len(tail) != 2 || tail[0] != "half a line" || tail[1] != "and a start" {
		t.Fatalf("tail %q", tail)
	}
	r = &logRing{}
	r.Write([]byte(strings.Repeat("x", reportLogLineMax*3) + "\n"))
	tail := r.Tail()
	if len(tail) != 1 || !strings.HasSuffix(tail[0], " [cut]") || len(tail[0]) > reportLogLineMax+10 {
		t.Fatalf("a long line came back %d bytes", len(tail[0]))
	}
}

func TestScrubReport(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	c := &Client{loginUser: "dolph", loginPass: "hunter2pass"}
	in := "ISO: " + home + "/eqoa.iso\nauth as dolph ok, key hunter2pass\ncharacter: Dolph\nadolphus\n"
	out := c.scrubReport(in)
	for _, gone := range []string{home, "dolph ok", "hunter2pass"} {
		if strings.Contains(out, gone) {
			t.Errorf("%q survived: %q", gone, out)
		}
	}
	for _, kept := range []string{"ISO: ~/eqoa.iso", "<account> ok", "<password>", "character: Dolph", "adolphus"} {
		if !strings.Contains(out, kept) {
			t.Errorf("want %q in %q", kept, out)
		}
	}

	// A password too short to tell from part of a word is left alone.
	c = &Client{loginPass: "a"}
	if out := c.scrubReport("a cat sat"); out != "a cat sat" {
		t.Errorf("a one-letter password rewrote the text: %q", out)
	}
}

func TestCleanReportText(t *testing.T) {
	if got := cleanReportText("two\nlines\tand \x1b[2J é", 100); got != "two lines and [2J " {
		t.Errorf("got %q", got)
	}
	if got := cleanReportText(strings.Repeat("n", 50), 32); len(got) != 32 {
		t.Errorf("cut to %d, want 32", len(got))
	}
}

func readyCapture(dump string, shot []byte) *reportCapture {
	cp := &reportCapture{stamp: "20261015-120000", dump: dump, shot: shot, ready: make(chan struct{})}
	close(cp.ready)
	return cp
}

func TestSendReport(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/report" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("%s %s (%s)", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
		}
		json.NewDecoder(r.Body).Decode(&got)
		fmt.Fprint(w, `{"ok":true,"id":"20261015-120001-0a0b0c0d"}`)
	}))
	defer srv.Close()

	shot := []byte("\x89PNG\r\n\x1a\nnot really")
	id, err := sendReport(srv.URL, readyCapture("eqoa client trace\n[session]\n", shot), "the fog\nflickers", "Velvet")
	if err != nil || id != "20261015-120001-0a0b0c0d" {
		t.Fatalf("id %q, err %v", id, err)
	}
	for _, k := range []string{"version", "platform", "channel", "name", "note", "screenshot", "report"} {
		if _, ok := got[k]; !ok {
			t.Errorf("no %q in the body", k)
		}
	}
	if got["name"] != "Velvet" || got["note"] != "the fog\nflickers" || got["channel"] != "local" {
		t.Errorf("body %v", got)
	}
	if b, _ := base64.StdEncoding.DecodeString(got["screenshot"]); string(b) != string(shot) {
		t.Errorf("the picture did not survive the trip")
	}
	// The header the server shows with `head`, then the dump.
	if !strings.HasPrefix(got["report"], "EQOA client problem report\n") ||
		!strings.Contains(got["report"], "note: the fog flickers\n") ||
		!strings.Contains(got["report"], "trace: eqoa-20261015-120000.txt\n") ||
		!strings.HasSuffix(got["report"], "--- trace ---\neqoa client trace\n[session]\n") {
		t.Errorf("report %q", got["report"])
	}
}

func TestSendReportSaysWhyNot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"ok":false,"error":"too many reports from here, try again later"}`)
	}))
	if _, err := sendReport(srv.URL, readyCapture("dump", nil), "", ""); err == nil || err.Error() != "too many reports from here, try again later" {
		t.Errorf("err %v", err)
	}
	srv.Close()

	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	if _, err := sendReport(srv.URL, readyCapture("dump", nil), "", ""); err == nil || err.Error() != "the server answered 502" {
		t.Errorf("err %v", err)
	}
	srv.Close()

	// Closed: nothing listening any more.
	if _, err := sendReport(srv.URL, readyCapture("dump", nil), "", ""); err == nil {
		t.Errorf("a server that is not there took the report")
	}
}
