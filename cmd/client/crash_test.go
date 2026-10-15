package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The whole of the path nothing of ours runs on: a child that panics on a
// goroutine of its own, the runtime writing that down through
// SetCrashOutput, and the next run turning it into a report with the dead
// run's log.
func TestCrashOutputCatchesAGoroutinePanic(t *testing.T) {
	if dir := os.Getenv("EQOA_CRASH_CHILD"); dir != "" {
		installLogRing(dir)
		installCrashCapture(dir)
		log.Printf("child: the last line before the crash")
		go func() { panic("child: a goroutine panicked") }()
		time.Sleep(5 * time.Second)
		return
	}

	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashOutputCatchesAGoroutinePanic$")
	cmd.Env = append(os.Environ(), "EQOA_CRASH_CHILD="+dir)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("the child did not crash:\n%s", out)
	}

	// What the next run does first: its own log rotation, then the collection.
	if err := os.Rename(filepath.Join(dir, clientLogName), filepath.Join(dir, clientPrevLogName)); err != nil {
		t.Fatal(err)
	}
	crashes := filepath.Join(dir, "crashes")
	collectCrashes(crashes, filepath.Join(dir, clientPrevLogName), "", "")

	files, _ := filepath.Glob(filepath.Join(crashes, "crash-*.txt"))
	if len(files) != 1 {
		t.Fatalf("%d reports, want 1", len(files))
	}
	if outs, _ := filepath.Glob(filepath.Join(crashes, "run-*.out")); len(outs) != 0 {
		t.Errorf("the run's crash output is still there: %v", outs)
	}
	data, _ := os.ReadFile(files[0])
	report := string(data)
	for _, want := range []string{
		"EQOA client crash report\nversion: local\nchannel: local\n",
		"panic: child: a goroutine panicked",
		"goroutine ",
		"[log] last ",
		"child: the last line before the crash",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("no %q in the report:\n%s", want, report)
		}
	}
}

func writeRun(t *testing.T, dir, run, body string) string {
	t.Helper()
	path := filepath.Join(dir, "run-"+run+".out")
	head := "EQOA client run\nversion: v2026.10.20\nchannel: stable\nplatform: windows/amd64\nrun: " + run + "\nstarted: then\n\n"
	if err := os.WriteFile(path, []byte(head+body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCollectCrashes(t *testing.T) {
	dir := t.TempDir()
	prev := filepath.Join(dir, clientPrevLogName)
	var lines []string
	for i := 0; i < reportLogLines+20; i++ {
		lines = append(lines, fmt.Sprintf("2026/10/15 12:00:00 line %d", i))
	}
	os.WriteFile(prev, []byte(clientLogTag+" 20261015-120000-1\n"+strings.Join(lines, "\n")+"\n"), 0o644)
	home, _ := os.UserHomeDir()

	clean := writeRun(t, dir, "20261015-110000-1", "")
	crashed := writeRun(t, dir, "20261015-120000-1", "panic: boom in "+home+"/eqoa\n\ngoroutine 7 [running]:\nmain.loader()\n")
	older := writeRun(t, dir, "20261014-090000-5", "fatal error: concurrent map writes\n")
	os.WriteFile(filepath.Join(dir, "crash-20261013-080000-9.txt"), []byte("EQOA client crash report\n\npanic: first\n"), 0o600)
	again := writeRun(t, dir, "20261013-080000-9", "panic: and again on the crash screen\n")

	collectCrashes(dir, prev, "", "")

	for _, gone := range []string{clean, crashed, older, again} {
		if _, err := os.Stat(gone); err == nil {
			t.Errorf("%s is still there", filepath.Base(gone))
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "crash-20261015-110000-1.txt")); err == nil {
		t.Errorf("a run that did not crash became a report")
	}

	read := func(run string) string {
		data, err := os.ReadFile(filepath.Join(dir, "crash-"+run+".txt"))
		if err != nil {
			t.Fatalf("no report for %s", run)
		}
		return string(data)
	}
	r := read("20261015-120000-1")
	if !strings.Contains(r, "version: v2026.10.20\nchannel: stable\nplatform: windows/amd64\nrun: 20261015-120000-1\n") {
		t.Errorf("the header is not the crashed run's:\n%s", r)
	}
	if !strings.Contains(r, "panic: boom in ~/eqoa") || strings.Contains(r, home+"/") {
		t.Errorf("the home directory was not scrubbed:\n%s", r)
	}
	if !strings.Contains(r, fmt.Sprintf("[log] last %d lines of that run\n2026/10/15 12:00:00 line 20\n", reportLogLines)) ||
		!strings.HasSuffix(r, fmt.Sprintf("line %d\n", reportLogLines+19)) {
		t.Errorf("the log tail is not the last %d lines:\n%s", reportLogLines, r)
	}
	if r := read("20261014-090000-5"); strings.Contains(r, "[log]") {
		t.Errorf("another run's log was given to this one:\n%s", r)
	}
	if r := read("20261013-080000-9"); !strings.HasPrefix(r, "EQOA client crash report\n\npanic: first\n") ||
		!strings.Contains(r, "--- then, while the crash screen was up ---\npanic: and again on the crash screen\n") {
		t.Errorf("a crash on the crash screen is not added to the report:\n%s", r)
	}
}

func TestCapCrashText(t *testing.T) {
	text := "HEAD" + strings.Repeat("x", crashMaxText) + "TAIL"
	got := capCrashText(text)
	if len(got) > crashMaxText || !strings.HasPrefix(got, "HEAD") || !strings.HasSuffix(got, "TAIL") ||
		!strings.Contains(got, "[... cut to fit ...]") {
		t.Errorf("cut to %d bytes, head %q tail %q", len(got), got[:4], got[len(got)-4:])
	}
	if capCrashText("short") != "short" {
		t.Errorf("a short report was changed")
	}
}

func TestSendCrashReports(t *testing.T) {
	dir := t.TempDir()
	var a, b string
	for i, p := range []*string{&a, &b} {
		*p = filepath.Join(dir, fmt.Sprintf("crash-20261015-12000%d-1.txt", i))
		os.WriteFile(*p, []byte(crashHeader("dev-abc1234", "dev", "linux/amd64", "r", "t")+"panic: boom\n"), 0o600)
	}

	var got []map[string]string
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/crash" {
			t.Errorf("POST %s", r.URL.Path)
		}
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		got = append(got, body)
		if fail {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"ok":false,"error":"too many reports from here, try again later"}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"id":"20261015-120001-0a0b0c0d"}`)
	}))
	defer srv.Close()

	fail = true
	if err := sendCrashReports(srv.URL, []string{a, b}, "note"); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("err %v", err)
	}
	if _, err := os.Stat(a); err != nil || len(got) != 1 {
		t.Errorf("a refused report was deleted, or the next was sent anyway")
	}

	fail, got = false, nil
	if err := sendCrashReports(srv.URL, []string{a, b}, "in Freeport"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0]["note"] != "in Freeport" || got[0]["version"] != "dev-abc1234" ||
		got[0]["platform"] != "linux/amd64" || got[0]["channel"] != "dev" || !strings.Contains(got[0]["report"], "panic: boom") {
		t.Errorf("sent %v", got)
	}
	for _, p := range []string{a, b} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s was sent and kept", filepath.Base(p))
		}
	}
}
