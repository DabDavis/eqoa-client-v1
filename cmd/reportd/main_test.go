package main

// These are pdghostd's own tests of the same two routes (test_problem_reports
// and test_report_board in tools/pdghostd/test_pdghostd.py), case for case,
// less the patch notes and the archive this port leaves out.

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// tinyPNG is pdghostd's tiny_png, byte for byte: 1x2, 8-bit RGB.
var tinyPNG = mustB64("iVBORw0KGgoAAAANSUhEUgAAAAEAAAACCAIAAAAW4yFwAAAADklEQVR4nGP4z8DAAMQACf4B/4PiLjgAAAAASUVORK5CYII=")

func mustB64(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// makePNG is a gradient, w by h, as Go's encoder writes it (with row filters).
func makePNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x*7 + y*3), uint8(x*7 + y*3 + 50), uint8(x*7 + y*3 + 100), 255})
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

// tunables brings the big bounds within reach of a test, as pdghostd's tests
// do by rewriting its constants, and puts them back after.
func tunables(t *testing.T) {
	saved := []int{reportMaxFiles, boardPage, thumbWidth}
	reportMaxFiles, boardPage, thumbWidth = 4, 3, 16
	t.Cleanup(func() { reportMaxFiles, boardPage, thumbWidth = saved[0], saved[1], saved[2] })
}

type resp struct {
	code   int
	body   []byte
	header http.Header
	js     map[string]any
}

func do(s *server, method, target, ip string, body []byte) resp {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	if ip != "" {
		req.Header.Set("X-Real-IP", ip)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	r := resp{code: rec.Code, body: rec.Body.Bytes(), header: rec.Header()}
	json.Unmarshal(r.body, &r.js)
	return r
}

func sendReport(s *server, report, note string, shot []byte, ip string, extra map[string]any) resp {
	body := map[string]any{"report": report, "note": note, "version": "abc1234",
		"platform": "x86_64-linux", "channel": "dev"}
	if shot != nil {
		body["screenshot"] = base64.StdEncoding.EncodeToString(shot)
	}
	for k, v := range extra {
		body[k] = v
	}
	data, _ := json.Marshal(body)
	return do(s, "POST", "/report", ip, data)
}

func reportFiles(s *server) []string {
	entries, _ := os.ReadDir(s.reportDir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func clearReports(s *server) {
	for _, n := range reportFiles(s) {
		os.Remove(filepath.Join(s.reportDir, n))
	}
}

func readReport(t *testing.T, s *server, id string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(s.reportDir, id+".txt"))
	if err != nil {
		t.Fatalf("report %q: %v", id, err)
	}
	return string(data)
}

func check(t *testing.T, ok bool, what string) {
	t.Helper()
	if !ok {
		t.Errorf("FAIL: %s", what)
	}
}

const testDump = "EQOA client problem report\nversion: abc1234\n\n--- state ---\nzone tunaria/freeprt\nposition 25305.6 54.1 15748.4\n"

func TestProblemReports(t *testing.T) {
	tunables(t)
	s := newServer(t.TempDir())
	shot := tinyPNG

	r := sendReport(s, testDump, "guard's gun is floating\nnext to him", shot, "10.6.0.1", nil)
	id, _ := r.js["id"].(string)
	check(t, r.code == 200 && r.js["ok"] == true && id != "", "a report with a picture is taken -> 200")
	check(t, fmt.Sprint(reportFiles(s)) == fmt.Sprint([]string{id + ".png", id + ".txt"}), "and written as a .txt and a .png")
	got, _ := os.ReadFile(filepath.Join(s.reportDir, id+".png"))
	check(t, bytes.Equal(got, shot), "the picture is the bytes that were sent")
	stored := readReport(t, s, id)
	check(t, strings.Contains(stored, "from: 10.6.0.1") && strings.Contains(stored, "screenshot: "+id+".png") &&
		strings.Contains(stored, "note: guard's gun is floating next to him\n"),
		"the header names the address, the picture and the note on one line")
	check(t, strings.Contains(stored, "position 25305.6 54.1 15748.4"), "and the dump is underneath")
	check(t, strings.HasPrefix(stored, "received: ") && strings.Contains(stored, "\n\nEQOA client problem report\n"),
		"the header comes first and a blank line ends it")
	if st, err := os.Stat(filepath.Join(s.reportDir, id+".txt")); err == nil {
		check(t, st.Mode().Perm() == 0o600, "a report is readable by its owner only")
	}
	clearReports(s)

	r = sendReport(s, testDump, "", nil, "10.6.0.2", nil)
	id, _ = r.js["id"].(string)
	check(t, r.code == 200 && fmt.Sprint(reportFiles(s)) == fmt.Sprint([]string{id + ".txt"}), "a report without a picture is a .txt alone")
	stored = readReport(t, s, id)
	check(t, strings.Contains(stored, "screenshot: -\n"), "and says so")
	check(t, strings.Contains(stored, "name: -\n"), "a report with no name field is taken and says so")
	clearReports(s)

	r = sendReport(s, testDump, "", nil, "10.6.0.2", map[string]any{"name": "Velvet Dark"})
	check(t, r.code == 200 && strings.Contains(readReport(t, s, r.js["id"].(string)), "name: Velvet Dark\n"),
		"a name is written into the header, for the credits")
	clearReports(s)

	lead := " \n  two\nlines "
	r = sendReport(s, testDump, "", nil, "10.6.0.2", map[string]any{"name": lead + strings.Repeat("z", 200)})
	check(t, r.code == 200 && strings.Contains(readReport(t, s, r.js["id"].(string)),
		"name: two lines "+strings.Repeat("z", reportMaxName-len(lead))+"\n"),
		"a name is one line, trimmed, and cut to its cap")
	clearReports(s)

	r = sendReport(s, testDump, "", nil, "10.6.0.2", map[string]any{"name": 12345})
	check(t, r.code == 400 && r.js["error"] == "bad body", "a name that is not a string -> 400")
	r = sendReport(s, testDump, "", nil, "10.6.0.2", map[string]any{"note": nil})
	check(t, r.code == 400 && r.js["error"] == "bad body", "a note that is null -> 400")

	r = sendReport(s, testDump, "", []byte("GIF89a not a png"), "10.6.0.3", nil)
	check(t, r.code == 400 && r.js["error"] == "bad screenshot" && len(reportFiles(s)) == 0,
		"a picture that is not a PNG -> 400, nothing written")
	r = sendReport(s, testDump, "", nil, "10.6.0.3", map[string]any{"screenshot": "!!!not base64!!!"})
	check(t, r.code == 400 && r.js["error"] == "bad screenshot", "a picture that is not base64 -> 400")
	r = sendReport(s, testDump, "", nil, "10.6.0.3", map[string]any{"screenshot": nil})
	check(t, r.code == 200, "a null picture is no picture")
	clearReports(s)

	r = sendReport(s, "short", "", nil, "10.6.0.3", nil)
	check(t, r.code == 400 && r.js["error"] == "empty report", "an empty report -> 400")

	for _, body := range []string{"", "null", "[1,2]", `"x"`, "{} trailing", "{nope"} {
		r = do(s, "POST", "/report", "10.6.0.3", []byte(body))
		check(t, r.code == 400 && r.js["error"] == "bad body", fmt.Sprintf("a body of %q -> 400", body))
	}

	r = sendReport(s, testDump+"\x1b[2J\x7f\u0085", strings.Repeat("n", 5000), nil, "10.6.0.4", nil)
	stored = readReport(t, s, r.js["id"].(string))
	check(t, r.code == 200 && !strings.ContainsAny(stored, "\x1b\x7f\u0085") &&
		strings.Contains(stored, "note: "+strings.Repeat("n", 1000)+"\n"),
		"control characters out, the note cut to its cap")
	clearReports(s)

	r = sendReport(s, testDump, "", nil, "10.6.0.4", map[string]any{"version": "v1\n\nfrom: 6.6.6.6"})
	stored = readReport(t, s, r.js["id"].(string))
	check(t, r.code == 200 && strings.Contains(stored, "version: v1  from: 6.6.6.6\n") &&
		strings.Index(stored, "\n\n") > strings.Index(stored, "note: "),
		"a newline in a field cannot start a header line of its own")
	clearReports(s)

	big := strings.Repeat("x", 3*1024*1024)
	r = sendReport(s, testDump+big, "", shot, "10.6.0.5", nil)
	check(t, r.code == 200, "a body past 2MB is taken ...")
	if r.code == 200 {
		check(t, len(readReport(t, s, r.js["id"].(string))) < 520*1024, "... and its text cut to the cap")
	}
	clearReports(s)

	r = do(s, "POST", "/report", "10.6.0.5", bytes.Repeat([]byte("x"), int(reportMaxBody)+1))
	check(t, r.code == 400 && r.js["error"] == "bad body", "a body past the route's limit -> 400")

	check(t, do(s, "GET", "/report", "10.6.0.5", nil).code == 404, "there is no way to read one back out")
	check(t, do(s, "POST", "/board", "10.6.0.5", []byte("{}")).code == 404, "nor any other POST")
	r = do(s, "GET", "/ping", "", nil)
	check(t, r.code == 200 && r.js["service"] == "eqoa-reportd", "ping says what it is")

	seen := map[int]bool{}
	for i := 0; i < 6; i++ {
		seen[sendReport(s, testDump, "", shot, "10.6.0.9", nil).code] = true
	}
	txts := 0
	for _, n := range reportFiles(s) {
		if strings.HasSuffix(n, ".txt") {
			txts++
		}
	}
	check(t, seen[507] && txts == reportMaxFiles, "a full report directory refuses at its cap, counting reports rather than files")
	clearReports(s)

	taken := 0
	for i := 0; i < 120; i++ {
		if sendReport(s, testDump, "", nil, "10.6.1.1", nil).code == 200 {
			taken++
		}
		clearReports(s)
	}
	check(t, taken == 120, "120 reports in an hour from one address are taken")
	r = sendReport(s, testDump, "", nil, "10.6.1.1", nil)
	check(t, r.code == 429, "the 121st -> 429")
	check(t, sendReport(s, testDump, "", nil, "10.6.1.2", nil).code == 200, "while another address is still taken")
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	check(t, clientIP(r) == "127.0.0.1", "no headers: the peer")
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 1.2.3.4")
	check(t, clientIP(r) == "1.2.3.4", "X-Forwarded-For is read from the right, where our proxy wrote")
	r.Header.Set("X-Real-IP", "5.6.7.8")
	check(t, clientIP(r) == "5.6.7.8", "X-Real-IP first")
}

// What a report carries that the board must never show: the address it came
// from, and the dump, settings, log and paths underneath the header.
var secrets = []string{"10.99.88.77", "10.66.55.44", "SECRET-DUMP-LINE", "SecretSetting", "/home/tester", "from:"}

func putReport(t *testing.T, s *server, base, name, note string, shot []byte, platform string, headerName bool) {
	t.Helper()
	os.MkdirAll(s.reportDir, 0o700)
	if shot != nil {
		os.WriteFile(filepath.Join(s.reportDir, base+".png"), shot, 0o600)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "received: %s-%s-%s %s:%s:%s\n", base[0:4], base[4:6], base[6:8], base[9:11], base[11:13], base[13:15])
	fmt.Fprintf(&b, "from: 10.99.88.77\nversion: abc1234\nplatform: %s\nchannel: dev\n", platform)
	if shot != nil {
		fmt.Fprintf(&b, "screenshot: %s.png\n", base)
	} else {
		b.WriteString("screenshot: -\n")
	}
	if headerName {
		fmt.Fprintf(&b, "name: %s\n", name)
	}
	fmt.Fprintf(&b, "note: %s\n\n", note)
	b.WriteString("EQOA client problem report\nSECRET-DUMP-LINE\n[settings]\nSecretSetting=1\npath: /home/tester/eqoa\n")
	if err := os.WriteFile(filepath.Join(s.reportDir, base+".txt"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// putStatus writes the status file. The board notices a change by mtime and
// size; a rewrite within one tick of the clock at the same size would be
// missed, which a person editing by hand never does and a test can.
func putStatus(t *testing.T, s *server, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.reportDir, "status.txt"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
}

func readStatus(s *server) string {
	data, _ := os.ReadFile(filepath.Join(s.reportDir, "status.txt"))
	return string(data)
}

type boardJS struct {
	Page    int               `json:"page"`
	Pages   int               `json:"pages"`
	Total   int               `json:"total"`
	Reports []boardJSONReport `json:"reports"`
	Fixes   []boardFix        `json:"fixes"`
}

func getJSON(s *server, query string) (int, boardJS, string) {
	r := do(s, "GET", "/board.json"+query, "", nil)
	var js boardJS
	json.Unmarshal(r.body, &js)
	return r.code, js, string(r.body)
}

func getPage(s *server, query string) (int, string, http.Header) {
	r := do(s, "GET", "/board"+query, "", nil)
	return r.code, string(r.body), r.header
}

func TestReportBoard(t *testing.T) {
	tunables(t)
	s := newServer(t.TempDir())

	// One through the real route, so that what it writes is what is read.
	r := sendReport(s, "EQOA client problem report\nSECRET-DUMP-LINE\n"+strings.Repeat("x", 64),
		"posted <b>live</b>", tinyPNG, "10.66.55.44", map[string]any{"name": "Poster"})
	posted, _ := r.js["id"].(string)
	check(t, r.code == 200, "a report posted to /report ...")

	shot := makePNG(64, 32)
	putReport(t, s, "20260920-100000-aaaaaaa1", "Alice", "first", shot, "x86_64-windows", true)
	putReport(t, s, "20260920-110000-aaaaaaa2", "-", "-", nil, "x86_64-windows", true)
	putReport(t, s, "20260921-100000-aaaaaaa3", "", `<script>alert(1)</script> & "quotes" 'too'`, nil, "x86_64-windows", false)
	putReport(t, s, "20260921-110000-aaaaaaa4", "<i>Bob</i>", "second", shot, "x86_64-linux", true)
	putReport(t, s, "20260922-100000-aaaaaaa5", "alice", "third", nil, "x86_64-windows", true)
	putReport(t, s, "20260922-110000-aaaaaaa6", "Carol", "hide me", shot, "x86_64-windows", true)

	code, page, headers := getPage(s, "")
	check(t, code == 200 && strings.HasPrefix(headers.Get("Content-Type"), "text/html"), "... and GET /board is a page")
	check(t, strings.Contains(page, "posted &lt;b&gt;live&lt;/b&gt;") && strings.Contains(page, "Poster"),
		"which shows it, its note escaped")
	for _, x := range secrets {
		check(t, !strings.Contains(page, x), "no address, dump, setting or path on the page: "+x)
	}
	_, _, raw := getJSON(s, "?page=1")
	for _, x := range secrets {
		check(t, !strings.Contains(raw, x), "nor in board.json: "+x)
	}
	_, one, _ := getPage(s, "?id=20260921-100000-aaaaaaa3")
	check(t, !strings.Contains(one, "<script>alert(1)</script>") &&
		strings.Contains(one, "&lt;script&gt;alert(1)&lt;/script&gt; &amp; &quot;quotes&quot; &#x27;too&#x27;"),
		"a note's markup is escaped, quotes included")
	check(t, !strings.Contains(page, "<i>Bob</i>") && strings.Contains(page, "&lt;i&gt;Bob&lt;/i&gt;"), "and so is a name")
	csp := headers.Get("Content-Security-Policy")
	check(t, !strings.Contains(csp, "script-src") && strings.Contains(csp, "default-src 'none'"),
		"and the page is served with a policy that runs no script")
	check(t, headers.Get("X-Content-Type-Options") == "nosniff" && headers.Get("Referrer-Policy") == "no-referrer",
		"and says nosniff and no-referrer")

	// No status file: everything is received.
	_, js, _ := getJSON(s, "")
	allReceived := true
	for _, r := range js.Reports {
		allReceived = allReceived && r.Status == "received"
	}
	check(t, js.Total == 7 && allReceived, "with no status file every report is received")
	check(t, len(js.Reports) > 0 && js.Reports[0].ID == posted, "newest first")

	names := map[string]string{}
	for _, p := range []string{"?page=2", "?page=3"} {
		_, js, _ := getJSON(s, p)
		for _, r := range js.Reports {
			names[r.ID] = r.Name
		}
	}
	check(t, names["20260920-110000-aaaaaaa2"] == "anonymous" && names["20260921-100000-aaaaaaa3"] == "anonymous",
		`a name of "-", or no name line at all, is "anonymous"`)
	_, js, _ = getJSON(s, "?page=2")
	for _, r := range js.Reports {
		if r.ID == "20260921-110000-aaaaaaa4" {
			check(t, r.Name == "<i>Bob</i>" && r.OS == "Linux" && r.Version == "abc1234" && r.Screenshot,
				"a report's public fields: name, OS, build, whether it has a picture")
		}
	}

	// Pagination: seven reports at three a page.
	check(t, js.Page == 2 && js.Pages == 3 && len(js.Reports) == 3, "three to a page, three pages")
	_, js, _ = getJSON(s, "?page=99")
	check(t, js.Page == 3, "a page past the end is the last")
	_, js, _ = getJSON(s, "?page=nope")
	check(t, js.Page == 1, "a page that is not a number is the first")
	_, page, _ = getPage(s, "?page=2")
	check(t, strings.Contains(page, "Page 2 of 3") && strings.Contains(page, "Newer") && strings.Contains(page, "Older"),
		"the page links both ways from the middle")

	// The status file.
	putStatus(t, s, "# a comment line\n"+
		"\n"+
		"20260920-100000-aaaaaaa1 fixed 2026-09-24 Fixed in abc1234 - the thing works\n"+
		"20260921-110000-aaaaaaa4 fixed 2026-09-24 Fixed in abc1234 - the thing works\n"+
		"20260922-100000-aaaaaaa5 duplicate 20260920-100000-aaaaaaa1 - same thing\n"+
		"20260921-100000 needinfo which level? <b>bold</b>\n"+
		"20260920-110000-aaaaaaa2 bogusword not a status\n"+
		"not-an-id fixed nothing\n"+
		"20260922-110000-aaaaaaa6 hidden\n"+
		posted+" working first\n"+
		posted+" working second, later lines win\n")
	byID := map[string]boardJSONReport{}
	for _, p := range []string{"?page=1", "?page=2", "?page=3"} {
		_, js, _ := getJSON(s, p)
		for _, r := range js.Reports {
			byID[r.ID] = r
		}
	}
	a1 := byID["20260920-100000-aaaaaaa1"]
	check(t, a1.Status == "fixed" && a1.Comment == "Fixed in abc1234 - the thing works",
		"a status line sets the word and the comment, the date apart")
	check(t, byID["20260921-100000-aaaaaaa3"].Status == "needinfo", "a stamp alone names the report sent that second")
	check(t, byID["20260920-110000-aaaaaaa2"].Status == "received", "a line with an unknown word is skipped")
	check(t, byID[posted].Comment == "second, later lines win", "a later line replaces an earlier one")
	_, js, _ = getJSON(s, "")
	_, hiddenShown := byID["20260922-110000-aaaaaaa6"]
	check(t, !hiddenShown && js.Total == 6, "a hidden report is off the board ...")
	check(t, do(s, "GET", "/board/shot/20260922-110000-aaaaaaa6.png", "", nil).code == 404, "... picture and all")
	_, page, _ = getPage(s, "?status=needinfo")
	check(t, strings.Contains(page, "which level? &lt;b&gt;bold&lt;/b&gt;"), "a status comment is escaped too")

	// Filters.
	_, js, _ = getJSON(s, "?name=ALICE")
	var ids []string
	for _, r := range js.Reports {
		ids = append(ids, r.ID)
	}
	sort.Strings(ids)
	check(t, fmt.Sprint(ids) == "[20260920-100000-aaaaaaa1 20260922-100000-aaaaaaa5]",
		"the tester filter takes a name whatever its capitals")
	_, js, _ = getJSON(s, "?status=fixed")
	check(t, js.Total == 2, "the status filter")
	_, js, _ = getJSON(s, "?status=hidden")
	check(t, js.Total == 6, "and hidden is not one it takes")
	_, js, _ = getJSON(s, "?id=20260921-100000-aaaaaaa3")
	check(t, js.Total == 1, "one report by its id")

	// The Fixed column: one change, two reports and a duplicate, one entry.
	_, js, _ = getJSON(s, "")
	check(t, len(js.Fixes) == 1 && fmt.Sprint(js.Fixes[0].Names) == "[Alice <i>Bob</i>]" && len(js.Fixes[0].Reports) == 3,
		"reports fixed by one change are one fix, crediting each name once")
	putStatus(t, s, readStatus(s)+"20260921-110000-aaaaaaa4 fixed 2026-09-25 Fixed in def5678 - another thing\n")
	_, js, _ = getJSON(s, "")
	var dates []string
	for _, f := range js.Fixes {
		dates = append(dates, f.Date)
	}
	check(t, fmt.Sprint(dates) == "[2026-09-25 2026-09-24]", "newest fix first")
	_, page, _ = getPage(s, "")
	check(t, strings.Contains(page, `id="fixes"`) && strings.Contains(page, "Thanks Alice") && strings.Contains(page, "build def5678"),
		"and the page has the column")

	// A status for a report that is not there changes nothing and breaks nothing.
	putStatus(t, s, readStatus(s)+"20250101-000000-deadbeef fixed 2025-01-01 gone\n")
	code, js, _ = getJSON(s, "")
	check(t, code == 200 && js.Total == 6 && len(js.Fixes) == 2, "a status line for a report that does not exist is ignored")

	// The feature queue: its own word, chip and filter, and not a fix.
	before := readStatus(s)
	putStatus(t, s, before+"20260920-110000-aaaaaaa2 feature A spell bar on the pad\n")
	_, js, _ = getJSON(s, "?status=feature")
	check(t, len(js.Reports) == 1 && js.Reports[0].ID == "20260920-110000-aaaaaaa2" && js.Reports[0].Status == "feature",
		"a feature line puts the report in the feature queue")
	check(t, len(js.Fixes) == 2, "a queued feature is not a fix")
	_, page, _ = getPage(s, "?status=feature")
	check(t, strings.Contains(page, "Feature queue") && strings.Contains(page, "s-feature") && strings.Contains(page, "spell bar on the pad"),
		"the page names the feature queue and draws its badge")
	putStatus(t, s, before)

	// A fix that names a patch notes line, which this board does not read,
	// is a fix with no date - after every dated one.
	putStatus(t, s, before+"20260921-100000-aaaaaaa3 fixed 1.2 Fixed in abc1234 - from the notes\n")
	_, js, _ = getJSON(s, "")
	last := js.Fixes[len(js.Fixes)-1]
	check(t, last.Date == "" && strings.Contains(last.Comment, "from the notes"), "a fix with a notes line has no date and goes last")
	_, page, _ = getPage(s, "")
	check(t, strings.Contains(page, "Earlier &middot; build abc1234"), "and the page says Earlier")
	putStatus(t, s, before)

	// Pictures, and everything that is not one.
	rr := do(s, "GET", "/board/shot/20260920-100000-aaaaaaa1.png", "", nil)
	check(t, rr.code == 200 && bytes.Equal(rr.body, shot) && rr.header.Get("Content-Type") == "image/png",
		"a report's picture is served as it was sent")
	rr = do(s, "GET", "/board/thumb/20260920-100000-aaaaaaa1.png", "", nil)
	check(t, rr.code == 200 && len(rr.body) > 24 && binary.BigEndian.Uint32(rr.body[16:20]) == 16 &&
		binary.BigEndian.Uint32(rr.body[20:24]) == 8, "its thumbnail is the picture shrunk to the thumbnail width")
	_, err := os.Stat(filepath.Join(s.thumbDir, "20260920-100000-aaaaaaa1.png"))
	check(t, err == nil, "and is kept for next time")
	rr = do(s, "GET", "/board/thumb/"+posted+".png", "", nil)
	check(t, rr.code == 200 && bytes.Equal(rr.body, tinyPNG), "a picture already small is its own thumbnail")
	for _, path := range []string{"/board/shot/..%2F..%2Fghosts.db", "/board/shot/../../ghosts.db",
		"/board/shot/../reports/status.txt", "/board/shot/20260920-100000-aaaaaaa1.txt",
		"/board/shot/20260920-100000-AAAAAAA1.png", "/board/shot/20260920-100000-aaaaaaa1",
		"/board/shot/20260920-110000-aaaaaaa2.png", "/board/thumb/status.png",
		"/board/shot/%2e%2e/ghosts.png", "/board/other"} {
		check(t, do(s, "GET", path, "", nil).code == 404, path+" -> 404")
	}

	rr = do(s, "GET", "/board/?status=fixed", "", nil)
	check(t, rr.code == 301 && rr.header.Get("Location") == "../board?status=fixed",
		"a trailing slash is sent back to where the relative links work")

	// The status file is not a report, for the directory's cap.
	clearReports(s)
	putStatus(t, s, "# nothing\n")
	taken := 0
	for i := 0; i < reportMaxFiles; i++ {
		if sendReport(s, "EQOA client problem report\n"+strings.Repeat("y", 64), "", nil, fmt.Sprintf("10.6.2.%d", i), nil).code == 200 {
			taken++
		}
	}
	check(t, taken == reportMaxFiles, "the status file does not count against the report cap")
}

func TestThumbnailIsABoxFilter(t *testing.T) {
	src := makePNG(64, 32)
	thumb := pngThumbnail(src, 16)
	if thumb == nil {
		t.Fatal("no thumbnail")
	}
	img, _ := png.Decode(bytes.NewReader(thumb))
	orig, _ := png.Decode(bytes.NewReader(src))
	// The top-left output pixel is the floor mean of the 4x4 block under it.
	var sum uint32
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			r, _, _, _ := orig.At(x, y).RGBA()
			sum += r >> 8
		}
	}
	r, _, _, a := img.At(0, 0).RGBA()
	check(t, r>>8 == sum/16 && a>>8 == 255, fmt.Sprintf("thumb (0,0) red %d, want %d, opaque", r>>8, sum/16))
	check(t, pngThumbnail(tinyPNG, 16) == nil, "a picture no wider than asked has no thumbnail")
	check(t, pngThumbnail([]byte("GIF89a"), 16) == nil, "nor does something that is not a PNG")
}

func TestBoardSurvivesNoDirectory(t *testing.T) {
	s := newServer(filepath.Join(t.TempDir(), "absent"))
	code, js, _ := getJSON(s, "")
	check(t, code == 200 && js.Total == 0 && js.Reports != nil, "an empty board before any report arrives")
	code, page, _ := getPage(s, "")
	check(t, code == 200 && strings.Contains(page, "No reports to show."), "and its page says so")
}
