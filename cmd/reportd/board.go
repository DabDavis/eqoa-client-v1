package main

// The report board: GET /board, a public page that lists the problem reports
// and says what became of each. It is the one thing here that reads a report
// back out, and it reads back only what the player typed or chose to send -
// when, the name they gave, their note, their picture, the build, the kind of
// computer - and never the address, the dump, the settings or the log, which
// are what make a report useful to read over ssh and are nobody else's.
//
// Everything the board shows comes through readEntry() and parseStatus(), and
// each builds its answer out of named fields rather than passing a report
// through: a line that neither asks for is never read into anything the page
// can reach. That is what keeps the address off the page - not care in the
// template, which is one edit away from forgetting.

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	boardPage       = 25
	boardMaxComment = 300
	// Thumbnails are made once each, the first time the board shows one.
	thumbWidth     = 320
	thumbMaxPixels = 4096 * 4096
	// Full-size pictures are the one heavy thing the board serves.
	boardShotWindow = time.Hour
	boardShotMax    = 300
)

// What became of each report: written by hand at each pass over the reports,
// read by the board whenever it changes. The order is the order the page
// counts them in. "hidden" takes a report off the board altogether - the note
// is whatever a stranger typed, and a public page needs a way to take one down
// that does not mean deleting the report.
var reportStatuses = []struct{ word, label string }{
	{"received", "Received"},
	{"working", "In progress"},
	{"fixed", "Fixed"},
	{"feature", "Feature queue"},
	{"needinfo", "Needs more info"},
	{"notabug", "Not a bug"},
	{"wontfix", "Won't fix"},
	{"duplicate", "Duplicate"},
	{"hidden", "Hidden"},
}

func statusLabel(word string) (string, bool) {
	for _, s := range reportStatuses {
		if s.word == word {
			return s.label, true
		}
	}
	return "", false
}

// reportEntry is what the board may show of one report.
type reportEntry struct {
	ID       string
	Received string
	Name     string
	Note     string
	Version  string
	OS       string
	Shot     *[2]int // width, height; nil for no picture
}

// boardReport is an entry with what became of it.
type boardReport struct {
	reportEntry
	Status  string
	Comment string
	Date    string
}

type statusLine struct{ word, comment, when string }

type fileKey struct {
	exists      bool
	mtime, size int64
}

type boardCache struct {
	mu       sync.Mutex
	haveDir  bool
	dirMtime int64
	entries  map[string]*reportEntry

	statusRead bool
	statusKey  fileKey
	statuses   map[string]statusLine
}

// boardText is text from a report or the status file as one line fit for the
// page: control characters out and the length bounded, the same as a report's
// fields on the way in, so that a file edited by hand is held to what the
// route would have written. Escaping is the page's job and happens there.
func boardText(v string, limit int) string {
	return strings.Join(strings.Fields(crashField(v, limit)), " ")
}

// boardOS is the kind of computer, from the build's target, and nothing finer.
func boardOS(platform string) string {
	platform = strings.ToLower(platform)
	for _, m := range []struct{ key, name string }{
		{"windows", "Windows"}, {"linux", "Linux"}, {"mac", "macOS"}, {"darwin", "macOS"},
	} {
		if strings.Contains(platform, m.key) {
			return m.name
		}
	}
	return ""
}

// pngSize is a PNG's (width, height) from its header, or nil if it is not one.
func pngSize(path string) *[2]int {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	head := make([]byte, 24)
	if _, err := io.ReadFull(f, head); err != nil {
		return nil
	}
	if !bytes.HasPrefix(head, pngMagic) || string(head[12:16]) != "IHDR" {
		return nil
	}
	w, h := binary.BigEndian.Uint32(head[16:20]), binary.BigEndian.Uint32(head[20:24])
	if w == 0 || w > 65535 || h == 0 || h > 65535 {
		return nil
	}
	return &[2]int{int(w), int(h)}
}

// readEntry is what the board may show of one report, or nil if it is not
// readable yet. Only the header is read, and only the lines named here are
// kept. A header with no "received:" line is one the report route is still
// writing - it creates the file before it fills it - and is left for the next
// look rather than cached half-read.
func (s *server) readEntry(base string) *reportEntry {
	f, err := os.Open(filepath.Join(s.reportDir, base+".txt"))
	if err != nil {
		return nil
	}
	defer f.Close()

	fields := map[string]string{}
	br := bufio.NewReaderSize(f, 4096)
	for i := 0; i < 16; i++ {
		raw, err := br.ReadSlice('\n')
		line := string(raw)
		if strings.TrimSpace(line) == "" {
			break
		}
		if key, value, ok := strings.Cut(line, ": "); ok {
			switch key {
			case "received", "version", "platform", "screenshot", "name", "note":
				if _, seen := fields[key]; !seen {
					fields[key] = strings.TrimRight(value, "\n")
				}
			}
		}
		if err != nil && err != bufio.ErrBufferFull {
			break
		}
	}

	received := boardText(fields["received"], 32)
	if received == "" {
		return nil
	}
	name := boardText(fields["name"], reportMaxName)
	if name == "" || name == "-" {
		name = "anonymous"
	}
	note := boardText(fields["note"], reportMaxNote)
	if note == "-" {
		note = ""
	}
	shot := fields["screenshot"]
	e := &reportEntry{
		ID:       base,
		Received: received,
		Name:     name,
		Note:     note,
		Version:  strings.Trim(boardText(fields["version"], fieldMax), "-"),
		OS:       boardOS(fields["platform"]),
	}
	if shot == base+".png" {
		e.Shot = pngSize(filepath.Join(s.reportDir, base+".png"))
	}
	return e
}

var statusWhenRe = regexp.MustCompile(`^([0-9]{4}-[0-9]{2}-[0-9]{2}|[0-9]{1,6}\.[0-9]{1,4})(?:\s+|$)`)

// parseStatus reads the status file's lines as {id or stamp: line}.
//
// One report a line: its id (or its stamp, for every report sent in that
// second), a status word, optionally a "when", and the rest of the line as the
// comment the page shows. The when of a fix is the date it shipped,
// YYYY-MM-DD, and it is what places the fix in the Fixed column. Blank lines
// and lines starting with # are skipped, and so is a line whose id or word is
// not one - a typo costs that line, not the board. A later line for the same
// key replaces an earlier one, so a status can be changed by adding a line as
// well as by editing one.
func parseStatus(text string) map[string]statusLine {
	statuses := map[string]statusLine{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, rest := cutField(line)
		word, rest := cutField(rest)
		word = strings.ToLower(word)
		if word == "" {
			continue
		}
		if !reportIDRe.MatchString(key) && !reportStampRe.MatchString(key) {
			continue
		}
		if _, ok := statusLabel(word); !ok {
			continue
		}
		when := ""
		if m := statusWhenRe.FindStringSubmatchIndex(rest); m != nil {
			when, rest = rest[m[2]:m[3]], rest[m[1]:]
		}
		statuses[key] = statusLine{word, boardText(rest, boardMaxComment), when}
	}
	return statuses
}

// cutField splits off the first whitespace-separated field, the way Python's
// str.split(None, n) does: the rest comes back with its leading space gone.
func cutField(s string) (string, string) {
	s = strings.TrimLeftFunc(s, unicode.IsSpace)
	i := strings.IndexFunc(s, unicode.IsSpace)
	if i < 0 {
		return s, ""
	}
	return s[:i], strings.TrimLeftFunc(s[i:], unicode.IsSpace)
}

var fixWhenNotesRe = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)

// boardReports is every report the board may show, newest first, with its
// status. The headers are read once each and kept; the directory is listed
// again only when its mtime moves (a report arrives or one is deleted), and
// the status file is read again only when its mtime or size does.
func (s *server) boardReports() []boardReport {
	c := &s.board
	c.mu.Lock()
	if st, err := os.Stat(s.reportDir); err != nil {
		c.entries, c.haveDir = nil, false
	} else if mt := st.ModTime().UnixNano(); !c.haveDir || mt != c.dirMtime {
		old := c.entries
		entries := map[string]*reportEntry{}
		complete := true
		names, _ := os.ReadDir(s.reportDir)
		for _, d := range names {
			name := d.Name()
			base := strings.TrimSuffix(name, ".txt")
			if !strings.HasSuffix(name, ".txt") || !reportIDRe.MatchString(base) {
				continue
			}
			e := old[base]
			if e == nil {
				e = s.readEntry(base)
			}
			if e == nil {
				complete = false
				continue
			}
			entries[base] = e
		}
		c.entries = entries
		// A report caught half-written keeps the listing stale, so that the
		// next request reads it again rather than never.
		c.haveDir, c.dirMtime = complete, mt
	}
	entries := c.entries

	statusPath := filepath.Join(s.reportDir, "status.txt")
	key := fileKey{}
	if st, err := os.Stat(statusPath); err == nil {
		key = fileKey{true, st.ModTime().UnixNano(), st.Size()}
	}
	// Never read is not the same as missing: a file that is not there still
	// has to be parsed once, as nothing.
	if !c.statusRead || key != c.statusKey {
		text := ""
		if key.exists {
			if f, err := os.Open(statusPath); err == nil {
				data, _ := io.ReadAll(io.LimitReader(f, 4*1024*1024))
				f.Close()
				text = string(data)
			}
		}
		c.statuses, c.statusKey, c.statusRead = parseStatus(text), key, true
	}
	statuses := c.statuses
	c.mu.Unlock()

	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))

	reports := make([]boardReport, 0, len(ids))
	for _, id := range ids {
		// The id's own line beats its stamp's, wherever each is in the file.
		st, ok := statuses[id]
		if !ok {
			st, ok = statuses[id[:15]]
		}
		if !ok {
			st = statusLine{"received", "", ""}
		}
		if st.word == "hidden" {
			continue
		}
		// pdghostd's "when" can also name a line of the patch notes, which
		// this board does not read: such a fix has no date.
		if fixWhenNotesRe.MatchString(st.when) {
			st.when = ""
		}
		reports = append(reports, boardReport{*entries[id], st.word, st.comment, st.when})
	}
	return reports
}

type boardWant struct{ name, status, id string }

// boardQuery parses a query the way pdghostd's parse_qs(max_num_fields=16)
// does: past sixteen fields it is no query at all.
func boardQuery(raw string) url.Values {
	if strings.Count(raw, "&")+1 > 16 {
		return url.Values{}
	}
	q, _ := url.ParseQuery(raw)
	return q
}

// boardFilter is the reports a query asks for, and the query as the page
// understood it.
func boardFilter(reports []boardReport, q url.Values) (boardWant, []boardReport, []boardReport) {
	one := func(key string) string {
		return cutRunes(strings.TrimSpace(q.Get(key)), reportMaxName)
	}
	want := boardWant{one("name"), strings.ToLower(one("status")), one("id")}
	if _, ok := statusLabel(want.status); !ok || want.status == "hidden" {
		want.status = ""
	}
	if !reportIDRe.MatchString(want.id) && !reportStampRe.MatchString(want.id) {
		want.id = ""
	}

	var byName, shown []boardReport
	for _, r := range reports {
		if want.name == "" || strings.EqualFold(r.Name, want.name) {
			byName = append(byName, r)
		}
	}
	for _, r := range byName {
		if (want.status == "" || r.Status == want.status) && (want.id == "" || strings.HasPrefix(r.ID, want.id)) {
			shown = append(shown, r)
		}
	}
	return want, byName, shown
}

func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// boardPageOf is which page, clamped to the ones there are, and how many.
func boardPageOf(q url.Values, total int) (int, int) {
	pages := (total + boardPage - 1) / boardPage
	if pages < 1 {
		pages = 1
	}
	page := 1
	if v := q.Get("page"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			page = n
		}
	}
	return max(1, min(page, pages)), pages
}

type boardFix struct {
	Date    string   `json:"date"`
	Comment string   `json:"comment"`
	Names   []string `json:"names"`
	Reports []string `json:"reports"`
	order   string
}

// boardFixes is the Fixed column: one entry per fix, newest first, crediting
// its reporters. A fix is the reports marked fixed with the same date and the
// same comment - one change that answered several reports is written as the
// same line for each, and shows once - and a report marked a duplicate of one
// of them is part of it, its sender credited.
func boardFixes(reports []boardReport) []boardFix {
	type key struct{ date, comment string }
	groups := map[key][]boardReport{}
	var order []key
	ofReport := map[string]key{}
	for _, r := range reports {
		if r.Status == "fixed" {
			k := key{r.Date, r.Comment}
			if _, ok := groups[k]; !ok {
				order = append(order, k)
			}
			groups[k] = append(groups[k], r)
			ofReport[r.ID] = k
		}
	}
	for _, r := range reports {
		if r.Status == "duplicate" {
			if words := strings.Fields(r.Comment); len(words) > 0 {
				if k, ok := ofReport[strings.TrimRight(words[0], ".,;:")]; ok {
					groups[k] = append(groups[k], r)
				}
			}
		}
	}

	fixes := make([]boardFix, 0, len(order))
	for _, k := range order {
		members := groups[k]
		sort.Slice(members, func(i, j int) bool { return members[i].ID > members[j].ID })
		names := []string{}
		seen := map[string]bool{}
		for i := len(members) - 1; i >= 0; i-- {
			n := members[i].Name
			if n != "anonymous" && !seen[strings.ToLower(n)] {
				seen[strings.ToLower(n)] = true
				names = append(names, n)
			}
		}
		ids := make([]string, len(members))
		for i, r := range members {
			ids[i] = r.ID
		}
		// Newest fix first; a fix with no date is written after every one with.
		fixes = append(fixes, boardFix{k.date, k.comment, names, ids, k.date + "\x00" + ids[0]})
	}
	sort.SliceStable(fixes, func(i, j int) bool { return fixes[i].order > fixes[j].order })
	return fixes
}

// esc is Python's html.escape: quotes included, so a string is safe in an
// attribute as well as in text.
var esc = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#x27;").Replace

// boardLink is a link back to the board with the filters changed, relative to
// it. Only a query, never a path: nginx serves this under /eqoa/reports/ and
// the server never sees the prefix, so a path written here would be wrong
// behind the proxy.
func boardLink(want boardWant, change map[string]string) string {
	vals := map[string]string{"name": want.name, "status": want.status, "id": want.id}
	for k, v := range change {
		vals[k] = v
	}
	var parts []string
	for _, k := range []string{"name", "status", "id", "page"} {
		if v := vals[k]; v != "" {
			parts = append(parts, k+"="+url.QueryEscape(v))
		}
	}
	if len(parts) == 0 {
		return "board"
	}
	return "?" + strings.Join(parts, "&")
}

var commentSplitRe = regexp.MustCompile(`\s+`)

// boardComment is a status comment with any report id it names made a link to
// that report.
func boardComment(comment string, known map[string]bool) string {
	var out strings.Builder
	last := 0
	emit := func(word string) {
		core := strings.TrimRight(word, ".,;:)")
		if known[core] {
			fmt.Fprintf(&out, `<a href="?id=%s#r-%s">%s</a>%s`, core, core, esc(core), esc(word[len(core):]))
		} else {
			out.WriteString(esc(word))
		}
	}
	for _, m := range commentSplitRe.FindAllStringIndex(comment, -1) {
		emit(comment[last:m[0]])
		out.WriteString(esc(comment[m[0]:m[1]]))
		last = m[1]
	}
	emit(comment[last:])
	return out.String()
}

// boardWhen is a report's stamp as a short date and time, "25 Sep 22:42".
func boardWhen(stamp string) string {
	if len(stamp) < 15 {
		return stamp
	}
	t, err := time.Parse("20060102-150405", stamp[:15])
	if err != nil {
		return stamp
	}
	return strings.TrimLeft(t.Format("02 Jan 15:04"), "0")
}

const boardCSS = `
:root{--bg:#f6f6f4;--card:#fff;--text:#1b1b1b;--dim:#5d5d5d;--line:#dcdcd6;
--link:#0b57b0;--hl:#fff3c4;--received:#6b6b6b;--working:#9a6200;--fixed:#1d7a35;
--needinfo:#7a3fb0;--notabug:#35607a;--wontfix:#8a3030;--duplicate:#6b6b6b;
--feature:#1f6f8b}
@media (prefers-color-scheme:dark){:root{--bg:#121314;--card:#1d1f21;--text:#e6e6e3;
--dim:#a2a29d;--line:#34373a;--link:#8ab8ff;--hl:#3a3420;--received:#9a9a9a;
--working:#e0a33a;--fixed:#5cc475;--needinfo:#c49af0;--notabug:#7fb4d6;
--wontfix:#f08a8a;--duplicate:#9a9a9a;--feature:#6cc3e0}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--text);
font:15px/1.45 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
main{max-width:1320px;margin:0 auto;padding:16px}
a{color:var(--link)}
h1{font-size:1.5em;margin:.2em 0}
h2{font-size:1.05em;margin:1.4em 0 .5em;color:var(--dim);font-weight:600}
h2.col{font-size:1.2em;color:var(--text);margin:.2em 0 .6em}
.lead{color:var(--dim);margin:.2em 0 1em;max-width:60em}
.cols{display:grid;grid-template-columns:minmax(0,1fr) 360px;gap:24px;align-items:start}
.fixes{position:sticky;top:0;max-height:100vh;overflow:auto;padding:12px 4px 12px 0}
.jump{display:none}
.chips{display:flex;flex-wrap:wrap;gap:6px;margin:0 0 12px}
.chip{padding:3px 10px;border:1px solid var(--line);border-radius:999px;
text-decoration:none;color:var(--text);background:var(--card);font-size:.9em}
.chip.on{border-color:var(--text);font-weight:600}
form{display:flex;flex-wrap:wrap;gap:8px;align-items:center;margin:0 0 8px}
select,button{font:inherit;padding:4px 8px;border:1px solid var(--line);border-radius:6px;
background:var(--card);color:var(--text)}
.card{display:flex;gap:14px;background:var(--card);border:1px solid var(--line);
border-radius:10px;padding:12px;margin:0 0 10px;scroll-margin-top:12px}
.card:target{background:var(--hl);border-color:var(--working)}
.shot{flex:0 0 240px}
.shot img{display:block;width:100%;height:auto;border-radius:6px;background:var(--line)}
.body{flex:1;min-width:0}
.note{margin:0 0 .3em;font-size:1.05em;overflow-wrap:anywhere}
.none{color:var(--dim);font-style:italic}
.meta{color:var(--dim);font-size:.88em;margin:0}
.status{margin:.6em 0 0;padding:.4em .6em;border-left:3px solid var(--line);
overflow-wrap:anywhere}
.badge{display:inline-block;font-size:.8em;font-weight:700;letter-spacing:.02em;
padding:0 8px;border-radius:999px;border:1px solid currentColor;margin-right:6px}
.s-received{color:var(--received)}.s-working{color:var(--working)}.s-fixed{color:var(--fixed)}
.s-needinfo{color:var(--needinfo)}.s-notabug{color:var(--notabug)}
.s-wontfix{color:var(--wontfix)}.s-duplicate{color:var(--duplicate)}
.s-feature{color:var(--feature)}.status.s-feature{border-color:var(--feature)}
.status.s-fixed{border-color:var(--fixed)}.status.s-working{border-color:var(--working)}
.status.s-needinfo{border-color:var(--needinfo)}.status>span:last-child{color:var(--text)}
.fix{border-left:3px solid var(--fixed);padding:.1em 0 .1em .7em;margin:0 0 14px}
.fix p{margin:0}
.fix .when{color:var(--dim);font-size:.85em}
.fix .thanks{color:var(--dim);font-size:.88em}
.pager{display:flex;justify-content:space-between;align-items:center;margin:18px 0;gap:8px}
.foot{color:var(--dim);font-size:.85em;margin:24px 0}
@media (max-width:860px){
.cols{display:block}
.jump{display:inline-block;margin:0 0 12px}
.fixes{display:none;position:static;max-height:none;overflow:visible;padding:0}
.fixes:target{display:block}
.cols:has(.fixes:target) .reports{display:none}}
@media (max-width:620px){.card{flex-direction:column}.shot{flex:none}}
`

var fixedInRe = regexp.MustCompile(`^Fixed in (\S+) - (.+)`)

// boardHTML is the board as a page. Two columns: the reports on the left,
// paged and filtered, and on the right every fix, each linking to the reports
// it answered. On a narrow screen the Fixed column is a link away (#fixes)
// rather than a second screenful above the reports, and CSS alone switches.
func boardHTML(reports []boardReport, q url.Values) []byte {
	want, byName, shown := boardFilter(reports, q)
	page, pages := boardPageOf(q, len(shown))
	rows := shown[min((page-1)*boardPage, len(shown)):min(page*boardPage, len(shown))]
	known := map[string]bool{}
	for _, r := range reports {
		known[r.ID] = true
	}
	where := map[string]int{}
	for i, r := range shown {
		where[r.ID] = i/boardPage + 1
	}

	fixes := boardFixes(reports)
	if want.name != "" {
		var mine []boardFix
		for _, f := range fixes {
			for _, n := range f.Names {
				if strings.EqualFold(n, want.name) {
					mine = append(mine, f)
					break
				}
			}
		}
		fixes = mine
	}

	reportHref := func(base string) string {
		// Same page: only the fragment, so nothing reloads. Elsewhere in the
		// current list: that page. Filtered out of it: that report alone.
		if p, ok := where[base]; ok {
			if p == page {
				return "#r-" + base
			}
			pg := ""
			if p > 1 {
				pg = strconv.Itoa(p)
			}
			return boardLink(want, map[string]string{"page": pg}) + "#r-" + base
		}
		return "?id=" + base + "#r-" + base
	}

	counts := map[string]int{}
	for _, r := range byName {
		counts[r.Status]++
	}

	// Testers by how many reports each sent, one entry per name whatever its
	// capitals, spelled the way their newest report spells it.
	type tester struct {
		name string
		n    int
	}
	testers := map[string]*tester{}
	var testerOrder []*tester
	for _, r := range reports {
		k := strings.ToLower(r.Name)
		if testers[k] == nil {
			testers[k] = &tester{name: r.Name}
			testerOrder = append(testerOrder, testers[k])
		}
		testers[k].n++
	}
	sort.SliceStable(testerOrder, func(i, j int) bool {
		a, b := testerOrder[i], testerOrder[j]
		if a.n != b.n {
			return a.n > b.n
		}
		return strings.ToLower(a.name) < strings.ToLower(b.name)
	})

	var p strings.Builder
	p.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<meta name="color-scheme" content="light dark">` +
		`<title>EQOA client problem reports</title>` +
		`<style>` + boardCSS + `</style></head><body><main>`)
	p.WriteString(`<h1>Problem reports</h1>`)
	p.WriteString(`<p class="lead">Everything sent from the F3 <b>Report a Problem</b> ` +
		`dialog in the EQOA client, newest first, and what became of it. ` +
		`This page is read-only; Dab updates the statuses as reports are ` +
		`looked at. Bugs and crashes come first; feature requests wait ` +
		`in the <b>Feature queue</b>. Times are UTC.</p>`)
	p.WriteString(`<div class="cols"><section class="reports" id="reports">`)
	fmt.Fprintf(&p, `<a class="jump chip s-fixed" href="#fixes">Fixed so far: %d &darr;</a>`, len(fixes))

	reset := map[string]string{"page": "", "id": ""}
	with := func(k, v string) map[string]string {
		m := map[string]string{k: v}
		for ck, cv := range reset {
			if ck != k {
				m[ck] = cv
			}
		}
		return m
	}
	on := func(b bool) string {
		if b {
			return " on"
		}
		return ""
	}
	p.WriteString(`<nav class="chips">`)
	fmt.Fprintf(&p, `<a class="chip%s" href="%s">All %d</a>`,
		on(want.status == ""), esc(boardLink(want, with("status", ""))), len(byName))
	for _, s := range reportStatuses {
		if counts[s.word] > 0 {
			fmt.Fprintf(&p, `<a class="chip s-%s%s" href="%s">%s %d</a>`, s.word, on(want.status == s.word),
				esc(boardLink(want, with("status", s.word))), esc(s.label), counts[s.word])
		}
	}
	p.WriteString(`</nav>`)

	selected := func(b bool) string {
		if b {
			return " selected"
		}
		return ""
	}
	p.WriteString(`<form method="get" action="board"><label>Tester <select name="name">` +
		`<option value="">Everyone</option>`)
	for _, t := range testerOrder {
		fmt.Fprintf(&p, `<option value="%s"%s>%s (%d)</option>`,
			esc(t.name), selected(strings.EqualFold(t.name, want.name)), esc(t.name), t.n)
	}
	p.WriteString(`</select></label><label>Status <select name="status"><option value="">Any status</option>`)
	for _, s := range reportStatuses {
		if s.word != "hidden" {
			fmt.Fprintf(&p, `<option value="%s"%s>%s</option>`, s.word, selected(want.status == s.word), esc(s.label))
		}
	}
	p.WriteString(`</select></label><button type="submit">Show</button>`)
	if want != (boardWant{}) {
		p.WriteString(` <a href="board">Clear</a>`)
	}
	p.WriteString(`</form>`)

	if len(rows) == 0 {
		p.WriteString(`<p class="lead">No reports to show.</p>`)
	}

	day := ""
	for _, r := range rows {
		if r.ID[:8] != day {
			day = r.ID[:8]
			heading := day
			if t, err := time.Parse("20060102", day); err == nil {
				heading = t.Format("Monday 02 January 2006")
			}
			fmt.Fprintf(&p, `<h2>%s</h2>`, esc(heading))
		}

		shot := ""
		if r.Shot != nil {
			w, h := r.Shot[0], r.Shot[1]
			tw := min(w, thumbWidth)
			shot = fmt.Sprintf(`<div class="shot"><a href="board/shot/%s.png">`+
				`<img src="board/thumb/%s.png" loading="lazy" width="%d" height="%d" `+
				`alt="Screenshot sent with the report"></a></div>`, r.ID, r.ID, tw, max(1, h*tw/w))
		}
		note := `<p class="note none">No note.</p>`
		if r.Note != "" {
			note = `<p class="note">` + esc(r.Note) + `</p>`
		}
		hm := r.Received
		if len(hm) >= 16 {
			hm = hm[11:16]
		}
		meta := []string{esc(hm), fmt.Sprintf(`<a href="%s">%s</a>`,
			esc(boardLink(want, map[string]string{"name": r.Name, "page": "", "id": ""})), esc(r.Name))}
		if r.Version != "" {
			meta = append(meta, "build "+esc(r.Version))
		}
		if r.OS != "" {
			meta = append(meta, esc(r.OS))
		}
		meta = append(meta, fmt.Sprintf(`<a href="?id=%s#r-%s">link</a>`, r.ID, r.ID))
		label, _ := statusLabel(r.Status)
		status := fmt.Sprintf(`<p class="status s-%s"><span class="badge">%s</span><span>%s</span></p>`,
			r.Status, esc(label), boardComment(r.Comment, known))
		fmt.Fprintf(&p, `<article class="card" id="r-%s">%s<div class="body">%s<p class="meta">%s</p>%s</div></article>`,
			r.ID, shot, note, strings.Join(meta, " &middot; "), status)
	}

	if pages > 1 {
		newer, older := "<span></span>", "<span></span>"
		if page > 1 {
			pg := ""
			if page > 2 {
				pg = strconv.Itoa(page - 1)
			}
			newer = fmt.Sprintf(`<a href="%s">&larr; Newer</a>`, esc(boardLink(want, map[string]string{"page": pg})))
		}
		if page < pages {
			older = fmt.Sprintf(`<a href="%s">Older &rarr;</a>`, esc(boardLink(want, map[string]string{"page": strconv.Itoa(page + 1)})))
		}
		fmt.Fprintf(&p, `<nav class="pager">%s<span>Page %d of %d</span>%s</nav>`, newer, page, pages, older)
	}
	p.WriteString(`</section>`)

	p.WriteString(`<aside class="fixes" id="fixes"><h2 class="col">Fixed</h2>`)
	p.WriteString(`<a class="jump" href="#reports">&uarr; Back to the reports</a>`)
	if len(fixes) == 0 {
		p.WriteString(`<p class="lead">Nothing yet.</p>`)
	}
	for _, f := range fixes {
		var links []string
		for _, b := range f.Reports {
			links = append(links, fmt.Sprintf(`<a href="%s">%s</a>`, esc(reportHref(b)), esc(boardWhen(b))))
		}
		thanks := ""
		if len(f.Names) > 0 {
			thanks = "Thanks " + esc(strings.Join(f.Names, ", "))
		} else if len(f.Reports) > 0 {
			thanks = "Reported anonymously"
		}
		// "Fixed in <build> - what" is how a fixed report's comment reads on
		// its card; in a column headed Fixed the build goes by the date.
		when := f.Date
		if t, err := time.Parse("2006-01-02", when); err == nil {
			when = strings.TrimLeft(t.Format("02 Jan 2006"), "0")
		} else if when == "" {
			when = "Earlier"
		}
		text := f.Comment
		if m := fixedInRe.FindStringSubmatch(text); m != nil {
			when, text = esc(when)+" &middot; build "+esc(m[1]), m[2]
		} else {
			when = esc(when)
		}
		var credit []string
		if thanks != "" {
			credit = append(credit, thanks)
		}
		if len(links) > 0 {
			word := "reports"
			if len(f.Reports) == 1 {
				word = "report"
			}
			credit = append(credit, word+" "+strings.Join(links, ", "))
		}
		body := boardComment(text, known)
		if body == "" {
			body = "Fixed"
		}
		creditHTML := ""
		if len(credit) > 0 {
			creditHTML = `<p class="thanks">` + strings.Join(credit, " &middot; ") + `</p>`
		}
		fmt.Fprintf(&p, `<div class="fix"><p class="when">%s</p><p>%s</p>%s</div>`, when, body, creditHTML)
	}
	p.WriteString(`</aside></div>`)

	p.WriteString(`<p class="foot">Send one from the game with F3. Only the date, ` +
		`the name you typed, your note, your screenshot, the build and the ` +
		`kind of computer are shown here; the rest of a report is only ` +
		`read by Dab. Also as <a href="board.json">JSON</a>.</p>`)
	p.WriteString(`</main></body></html>`)
	return []byte(p.String())
}

type boardJSONReport struct {
	ID         string `json:"id"`
	Received   string `json:"received"`
	Name       string `json:"name"`
	Note       string `json:"note"`
	Version    string `json:"version"`
	OS         string `json:"os"`
	Status     string `json:"status"`
	Comment    string `json:"comment"`
	Screenshot bool   `json:"screenshot"`
}

type boardJSONPage struct {
	OK      bool              `json:"ok"`
	Page    int               `json:"page"`
	Pages   int               `json:"pages"`
	Total   int               `json:"total"`
	Reports []boardJSONReport `json:"reports"`
	Fixes   []boardFix        `json:"fixes"`
}

// boardJSON is the same reports and filters as the page, and every fix, for
// anything that wants data rather than a page.
func boardJSON(reports []boardReport, q url.Values) boardJSONPage {
	_, _, shown := boardFilter(reports, q)
	page, pages := boardPageOf(q, len(shown))
	rows := shown[min((page-1)*boardPage, len(shown)):min(page*boardPage, len(shown))]
	out := boardJSONPage{OK: true, Page: page, Pages: pages, Total: len(shown),
		Reports: []boardJSONReport{}, Fixes: boardFixes(reports)}
	for _, r := range rows {
		out.Reports = append(out.Reports, boardJSONReport{r.ID, r.Received, r.Name, r.Note,
			r.Version, r.OS, r.Status, r.Comment, r.Shot != nil})
	}
	return out
}

// sendPage is a page or a picture. The policy says the page runs no script and
// loads nothing that is not its own, which is what it is written to do - said
// here too, so that a note that got past the escaping would have nothing to run.
func sendPage(w http.ResponseWriter, body []byte, ctype, cache string) {
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	h.Set("Cache-Control", cache)
	if strings.HasPrefix(ctype, "text/html") {
		h.Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; "+
			"form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	}
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

var boardPictureRe = regexp.MustCompile(`^/board/(shot|thumb)/([^/]+)\.png$`)

// boardGet serves the board: its page, its JSON and its pictures, all read-only.
func (s *server) boardGet(w http.ResponseWriter, r *http.Request, path string) {
	q := boardQuery(r.URL.RawQuery)

	// The page's links are relative to "board", which a trailing slash would
	// move one level down; send that back to where they work. The target is
	// relative too, because the proxy's prefix is not ours.
	if r.URL.EscapedPath() == "/board/" {
		loc := "../board"
		if r.URL.RawQuery != "" {
			loc += "?" + r.URL.RawQuery
		}
		w.Header().Set("Location", loc)
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusMovedPermanently)
		return
	}

	reports := s.boardReports()

	switch path {
	case "/board":
		sendPage(w, boardHTML(reports, q), "text/html; charset=utf-8", "no-cache")
		return
	case "/board.json":
		sendJSON(w, http.StatusOK, boardJSON(reports, q))
		return
	}

	// A picture: by an id of exactly the shape the report route writes, of a
	// report the board is showing, which has a picture.
	m := boardPictureRe.FindStringSubmatch(path)
	if m == nil || !reportIDRe.MatchString(m[2]) {
		sendError(w, http.StatusNotFound, "no such picture")
		return
	}
	base := m[2]
	var entry *boardReport
	for i := range reports {
		if reports[i].ID == base {
			entry = &reports[i]
			break
		}
	}
	if entry == nil || entry.Shot == nil {
		sendError(w, http.StatusNotFound, "no such picture")
		return
	}

	if m[1] == "thumb" {
		if data := s.reportThumbnail(base); data != nil {
			sendPage(w, data, "image/png", "public, max-age=86400")
			return
		}
	} else if !s.shots.ok(clientIP(r), boardShotWindow, boardShotMax) {
		sendError(w, http.StatusTooManyRequests, "too many pictures, wait a while")
		return
	}

	data, err := readCapped(filepath.Join(s.reportDir, base+".png"), reportMaxShot+1)
	if err != nil {
		sendError(w, http.StatusNotFound, "no such picture")
		return
	}
	sendPage(w, data, "image/png", "public, max-age=86400")
}

func readCapped(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, int64(n)))
}

// reportThumbnail is the thumbnail of a report's picture, made and kept on
// first ask, or nil when there is none to be had - which the caller answers
// with the full picture. One is made at a time, and at most one per report
// ever, so a queue costs nothing a thundering first page would not anyway.
func (s *server) reportThumbnail(base string) []byte {
	path := filepath.Join(s.thumbDir, base+".png")
	if data, err := os.ReadFile(path); err == nil {
		return data
	}

	s.thumbMu.Lock()
	defer s.thumbMu.Unlock()
	if s.thumbFailed[base] {
		return nil
	}
	if data, err := os.ReadFile(path); err == nil {
		return data
	}
	data, err := readCapped(filepath.Join(s.reportDir, base+".png"), reportMaxShot+1)
	if err != nil {
		return nil
	}
	thumb := pngThumbnail(data, thumbWidth)
	if thumb == nil {
		s.thumbFailed[base] = true
		return nil
	}
	// Written aside and renamed, so a thumbnail on disk is always whole.
	if os.MkdirAll(s.thumbDir, 0o700) == nil {
		tmp := path + ".tmp"
		if os.WriteFile(tmp, thumb, 0o600) == nil {
			os.Rename(tmp, path)
		}
	}
	return thumb
}

// pngThumbnail is a smaller copy of a PNG, shrunk by a k-by-k box filter to
// no wider than width, or nil if it cannot be made - which covers one that is
// already no wider than asked, and one whose header claims more pixels than a
// screenshot could have, checked before anything is inflated.
func pngThumbnail(data []byte, width int) []byte {
	if !bytes.HasPrefix(data, pngMagic) {
		return nil
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > thumbMaxPixels {
		return nil
	}
	k := (cfg.Width + width - 1) / width
	if k <= 1 {
		return nil
	}
	outw, outh := cfg.Width/k, cfg.Height/k
	if outw == 0 || outh == 0 {
		return nil
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}

	// RGB as stored, alpha ignored: the client's screenshots are opaque.
	b := img.Bounds()
	pix := func(x, y int) (uint32, uint32, uint32) {
		switch m := img.(type) {
		case *image.RGBA:
			i := m.PixOffset(b.Min.X+x, b.Min.Y+y)
			return uint32(m.Pix[i]), uint32(m.Pix[i+1]), uint32(m.Pix[i+2])
		case *image.NRGBA:
			i := m.PixOffset(b.Min.X+x, b.Min.Y+y)
			return uint32(m.Pix[i]), uint32(m.Pix[i+1]), uint32(m.Pix[i+2])
		}
		c := color.NRGBAModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA)
		return uint32(c.R), uint32(c.G), uint32(c.B)
	}

	out := image.NewRGBA(image.Rect(0, 0, outw, outh))
	area := uint32(k * k)
	for y := 0; y < outh; y++ {
		for x := 0; x < outw; x++ {
			var sr, sg, sb uint32
			for dy := 0; dy < k; dy++ {
				for dx := 0; dx < k; dx++ {
					r, g, bl := pix(x*k+dx, y*k+dy)
					sr, sg, sb = sr+r, sg+g, sb+bl
				}
			}
			i := out.PixOffset(x, y)
			out.Pix[i], out.Pix[i+1], out.Pix[i+2], out.Pix[i+3] =
				uint8(sr/area), uint8(sg/area), uint8(sb/area), 255
		}
	}
	var buf bytes.Buffer
	if (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, out) != nil {
		return nil
	}
	return buf.Bytes()
}
