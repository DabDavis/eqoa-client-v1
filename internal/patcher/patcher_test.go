package patcher

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// gz is a gzip the way Sandstorm's server holds its files: with the original
// name in the header (FNAME), which http_check_header has to skip.
func gz(name string, b []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Name = name
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

// server is a patch port: path → body, and a log of what was asked for.
type server struct {
	mu    sync.Mutex
	files map[string][]byte
	asked []string
	host  string
	port  int
}

func newServer(t *testing.T, files map[string][]byte) *server {
	s := &server{files: files}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.asked = append(s.asked, r.URL.Path)
		body, ok := s.files[r.URL.Path]
		s.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.Write(body)
	}))
	t.Cleanup(ts.Close)
	host, port, _ := net.SplitHostPort(ts.Listener.Addr().String())
	s.host = host
	s.port, _ = strconv.Atoi(port)
	return s
}

func (s *server) count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, p := range s.asked {
		if p == path {
			n++
		}
	}
	return n
}

type entry struct {
	local, name string
	data        []byte
	attrs       string // extra attributes, verbatim
	del         bool
}

// manifest builds a <VerantPatcher> shaped like Sandstorm's: one
// distribution, UseCRC, files at the root with an empty RemotePath, and a
// <Changes> under the product.
func manifest(s *server, useCRC bool, entries []entry) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, `<VerantPatcher Version="1"><Product Version="36"><Distribution Name="Test" UseCRC="%v" >`, useCRC)
	for _, e := range entries {
		if e.del {
			fmt.Fprintf(&b, `<File LocalName="%s" Delete="True"></File>`, e.local)
			continue
		}
		fmt.Fprintf(&b, `<File LocalName="%s" RemotePath="" Server="%s" Name="%s" CRC="%d" TotalSize="%d" DownloadSize="%d"%s></File>`,
			e.local, s.host, e.name, crc32.ChecksumIEEE(e.data), len(e.data), len(gz(e.local, e.data)), e.attrs)
	}
	fmt.Fprintf(&b, `</Distribution><Changes Server="%s" Port="%d" Version="0" RemotePath="patch/eqoa/Changes.txt" CRC="1"></Changes></Product></VerantPatcher>`,
		s.host, s.port)
	return []byte(b.String())
}

func serve(t *testing.T, useCRC bool, entries []entry, notes string) *server {
	s := newServer(t, map[string][]byte{})
	s.files["/patch/eqoa/Changes.txt"] = []byte(notes)
	for _, e := range entries {
		if !e.del {
			s.files["/"+e.name] = gz(e.local, e.data)
		}
	}
	s.files["/"+DefaultManifest] = gz("manifest.xml", manifest(s, useCRC, entries))
	return s
}

func run(t *testing.T, s *server, dir string) Snapshot {
	st := NewStatus()
	Run(Config{Host: s.host, Port: s.port, Dir: dir}, st)
	return st.Snapshot()
}

var sandstormLike = []entry{
	{local: "eqahosts.txt", name: "eqahosts.txt.gz", data: []byte("5.161.217.188:10071\n")},
	{local: "SS", name: "ss-stage1-6.dat.gz", data: bytes.Repeat([]byte{0x53, 0x53, 0, 1}, 512)},
	{local: "S2", name: "ss-patch-60.dat.gz", data: bytes.Repeat([]byte("S2 record "), 300)},
	{local: "addart.csf", del: true},
	{local: "ss-art.csf", name: "ss-art-4.csf.gz", data: []byte("CESF pretend art")},
	{local: "words.txt", del: true},
	{local: "WORDS.TXT", name: "words-12.txt.gz", data: []byte("English\r\nJapanese\r\n")},
}

func TestRunBringsTheFolderInLine(t *testing.T) {
	s := serve(t, true, sandstormLike, "Sandstorm notes")
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "addart.csf"), []byte("old art"), 0o644)
	os.WriteFile(filepath.Join(dir, "words.txt"), []byte("old words"), 0o644)

	snap := run(t, s, dir)
	if snap.State != StateDone || !snap.Done || snap.Err != "" {
		t.Fatalf("state %d, err %q", snap.State, snap.Err)
	}
	if !snap.Changed || snap.Progress != 1 {
		t.Fatalf("changed %v, progress %v", snap.Changed, snap.Progress)
	}
	if snap.Notes != "Sandstorm notes" {
		t.Fatalf("notes %q", snap.Notes)
	}
	for _, e := range sandstormLike {
		got, err := os.ReadFile(filepath.Join(dir, e.local))
		if e.del {
			if err == nil {
				t.Errorf("%s: a Delete entry left it", e.local)
			}
			continue
		}
		if !bytes.Equal(got, e.data) {
			t.Errorf("%s: %d bytes, want %d", e.local, len(got), len(e.data))
		}
	}
	// The <Changes> under the <Product> is fetched twice: by
	// PatcherAPI_Impl::InitializeChanges and by the product's distribution.
	if n := s.count("/patch/eqoa/Changes.txt"); n != 2 {
		t.Errorf("Changes.txt fetched %d times, want 2", n)
	}

	// A second run finds everything in place.
	before := s.count("/ss-patch-60.dat.gz")
	snap = run(t, s, dir)
	if snap.State != StateDone || snap.Changed {
		t.Fatalf("second run: state %d, changed %v", snap.State, snap.Changed)
	}
	if s.count("/ss-patch-60.dat.gz") != before {
		t.Fatal("second run downloaded S2 again")
	}
}

// Under UseCRC a same-size change is fetched again; without it the PS2 looks
// only at the size, and keeps it.
func TestSameSizeChangeNeedsUseCRC(t *testing.T) {
	for _, useCRC := range []bool{true, false} {
		s := serve(t, useCRC, sandstormLike, "")
		dir := t.TempDir()
		run(t, s, dir)
		s2 := filepath.Join(dir, "S2")
		bad := bytes.Repeat([]byte("x"), len(sandstormLike[2].data))
		os.WriteFile(s2, bad, 0o644)
		run(t, s, dir)
		got, _ := os.ReadFile(s2)
		if fetched := !bytes.Equal(got, bad); fetched != useCRC {
			t.Errorf("UseCRC=%v: fetched %v", useCRC, fetched)
		}
	}
}

// A download whose CRC is not the manifest's fails the whole patch, and the
// folder is left exactly as it was.
func TestBadCRCFailsAndWritesNothing(t *testing.T) {
	s := serve(t, true, sandstormLike, "")
	s.files["/ss-art-4.csf.gz"] = gz("ss-art.csf", []byte("CESF other art!!"))
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "addart.csf"), []byte("old art"), 0o644)
	snap := run(t, s, dir)
	if snap.State != StateFailed {
		t.Fatalf("state %d", snap.State)
	}
	names := listDir(t, dir)
	if len(names) != 1 || names[0] != "addart.csf" {
		t.Fatalf("folder after a failed patch: %v", names)
	}
}

// The PS2 reads a download as a C string unless it inflates: a file served
// uncompressed becomes a 0-byte image, and fails its CRC.
func TestUncompressedFileFails(t *testing.T) {
	es := []entry{{local: "S2", name: "s2.bin", data: []byte("raw"), attrs: ` Compressed="False"`}}
	s := serve(t, true, es, "")
	s.files["/s2.bin"] = []byte("raw")
	if snap := run(t, s, t.TempDir()); snap.State != StateFailed {
		t.Fatalf("state %d", snap.State)
	}
}

// A Restart file asks for a restart, which PatcherAPI answers by cancelling:
// every download after it fails.
func TestRestartFileCancelsTheRest(t *testing.T) {
	es := []entry{
		{local: "A", name: "a.gz", data: []byte("first"), attrs: ` Restart="True"`},
		{local: "B", name: "b.gz", data: []byte("second")},
	}
	s := serve(t, true, es, "")
	snap := run(t, s, t.TempDir())
	if snap.State != StateFailed {
		t.Fatalf("state %d", snap.State)
	}
	if s.count("/b.gz") != 0 {
		t.Fatal("B was fetched after the restart")
	}
}

// One LocalName as both a download and a Delete: the downloads run first,
// then DeleteOldFiles, so the file ends up gone.
func TestDeleteAfterDownloadOfTheSameName(t *testing.T) {
	es := []entry{
		{local: "SD", name: "sd.gz", data: []byte("payload")},
		{local: "SD", del: true},
	}
	s := serve(t, true, es, "")
	dir := t.TempDir()
	if snap := run(t, s, dir); snap.State != StateDone {
		t.Fatalf("state %d", snap.State)
	}
	if _, err := os.Stat(filepath.Join(dir, "SD")); err == nil {
		t.Fatal("SD survived its own Delete")
	}
}

func TestUnreachableServerFails(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	st := NewStatus()
	Run(Config{Host: "127.0.0.1", Port: port, Dir: t.TempDir()}, st)
	if snap := st.Snapshot(); snap.State != StateFailed || !snap.Done {
		t.Fatalf("state %d", snap.State)
	}
}

// A manifest LocalName that climbs out of the folder lands as its last
// component, as the PS2's WriteFile names it; one that IS ".." is refused.
func TestLocalNamesStayInTheFolder(t *testing.T) {
	es := []entry{{local: "../../escape", name: "e.gz", data: []byte("x")}}
	s := serve(t, true, es, "")
	root := t.TempDir()
	dir := filepath.Join(root, "a", "b")
	if snap := run(t, s, dir); snap.State != StateDone {
		t.Fatalf("state %d", snap.State)
	}
	if _, err := os.Stat(filepath.Join(dir, "escape")); err != nil {
		t.Fatal("escape was not written into the folder")
	}
	if _, err := os.Stat(filepath.Join(root, "escape")); err == nil {
		t.Fatal("wrote outside the folder")
	}
	if safeName("..") || safeName("C:x") || safeName("CON.txt") || !safeName("S2") {
		t.Fatal("safeName")
	}
}

// The request is the PS2's, byte for byte, and the answer must carry
// Content-Length.
func TestVURLRequestAndContentLength(t *testing.T) {
	for _, tc := range []struct {
		name, resp string
		ok         bool
	}{
		{"length", "HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nhello", true},
		{"no length", "HTTP/1.1 200 OK\r\nConnection: close\r\n\r\nhello", false},
		{"lower case", "HTTP/1.1 200 OK\r\ncontent-length: 5\r\nConnection: close\r\n\r\nhello", false},
		{"short", "HTTP/1.1 200 OK\r\nContent-Length: 9\r\n\r\nhello", false},
	} {
		l, _ := net.Listen("tcp", "127.0.0.1:0")
		got := make(chan string, 1)
		go func() {
			c, err := l.Accept()
			if err != nil {
				return
			}
			r := bufio.NewReader(c)
			var req strings.Builder
			for {
				line, err := r.ReadString('\n')
				req.WriteString(line)
				if err != nil || line == "\r\n" {
					break
				}
			}
			got <- req.String()
			io.WriteString(c, tc.resp)
			c.Close()
		}()
		port := l.Addr().(*net.TCPAddr).Port
		u := newVURL(fmt.Sprintf("http://127.0.0.1:%d:%d/patch/eqoa/a b.gz", port, port))
		buf := make([]byte, 64)
		ok := u.get(buf)
		l.Close()
		if want := "GET /patch/eqoa/a%20b.gz HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n"; <-got != want {
			t.Errorf("%s: request differs", tc.name)
		}
		if ok != tc.ok {
			t.Errorf("%s: ok %v", tc.name, ok)
		}
		if ok && cstring(buf) != "hello" {
			t.Errorf("%s: body %q", tc.name, cstring(buf))
		}
	}
}

func TestParseURL(t *testing.T) {
	u := newVURL("http://patch.everquestonlineadventures.com:7000:7000/patch/eqoa/x.xml.gz#frag")
	if str(u.server) != "patch.everquestonlineadventures.com" || u.port != 7000 || str(u.path) != "patch/eqoa/x.xml.gz" {
		t.Fatalf("%q %d %q", str(u.server), u.port, str(u.path))
	}
	if u := newVURL("ftp://x/y"); u.protocol != -1 || u.path != nil {
		t.Fatal("ftp accepted")
	}
}

func TestAtoi(t *testing.T) {
	for s, want := range map[string]uint32{
		"2294946724":           2294946724, // a CRC above 2^31
		"  -1":                 0xFFFFFFFF,
		"758416449xyz":         758416449,
		"":                     0,
		"99999999999999999999": 0xFFFFFFFF, // LONG_MAX's low word
	} {
		if got := uint32(atoi(s)); got != want {
			t.Errorf("atoi(%q) = %d, want %d", s, got, want)
		}
	}
}

func TestHTTPUncompress(t *testing.T) {
	data := []byte("the inflated bytes")
	dest := make([]byte, 64)
	if n := httpUncompress(dest, gz("named.bin", data)); n != len(data) || !bytes.Equal(dest[:n], data) {
		t.Fatalf("gzip: %d %q", n, dest[:n])
	}
	// No magic: read as raw deflate, which plain text is not.
	if n := httpUncompress(make([]byte, 64), []byte("<VerantPatcher/>")); n != 0 {
		t.Fatalf("plain text inflated to %d bytes", n)
	}
}

// The tree keeps whitespace text nodes, as libxml2 does.
func TestXMLKeepsBlanks(t *testing.T) {
	doc := xmlParseMemory([]byte("<A>\n <B x=\"1\"/>\n</A>"))
	r := doc.root()
	if r == nil || r.children == nil || r.children.name != "text" || r.children.next.name != "B" {
		t.Fatal("tree")
	}
	if v := xmlGetProp(r.children.next, "x"); v == nil || *v != "1" || xmlGetProp(r.children.next, "X") != nil {
		t.Fatal("attributes")
	}
	if xmlParseMemory([]byte("<A/><B/>")) != nil {
		t.Fatal("two roots parsed")
	}
}

// Sandstorm's own set, served as Sandstorm serves it, into an empty folder.
// Skips when the local copy (extracted-assets/, never committed) is absent.
func TestSandstormLiveSet(t *testing.T) {
	base := "../../../extracted-assets/sandstorm-patch-20261015"
	m, err := os.ReadFile(filepath.Join(base, "served", "manifest.xml.gz"))
	if err != nil {
		t.Skip("no local copy of Sandstorm's patch set")
	}
	xmlBytes, _ := io.ReadAll(must(gzip.NewReader(bytes.NewReader(m))))
	s := newServer(t, map[string][]byte{"/patch/eqoa/Changes.txt": []byte("notes")})
	// Point every Server= at the test server.
	re := strings.ReplaceAll(string(xmlBytes), `Server="5.161.217.188"`, `Server="`+s.host+`"`)
	re = strings.ReplaceAll(re, `Port="7000"`, `Port="`+strconv.Itoa(s.port)+`"`)
	s.files["/"+DefaultManifest] = gz("", []byte(re))
	for local, remote := range map[string]string{
		"eqahosts.txt": "eqahosts.txt.gz", "SS": "ss-stage1-6.dat.gz", "S2": "ss-patch-60.dat.gz",
		"ss-art.csf": "ss-art-4.csf.gz", "station.pak": "station4.pak.gz", "WORDS.TXT": "words-12.txt.gz",
	} {
		data, err := os.ReadFile(filepath.Join(base, local))
		if err != nil {
			t.Fatal(err)
		}
		s.files["/"+remote] = gz(local, data)
	}
	if served, err := os.ReadFile(filepath.Join(base, "served", "ss-patch-60.dat.gz")); err == nil {
		s.files["/ss-patch-60.dat.gz"] = served // Sandstorm's own gzip, FNAME and all
	}
	dir := t.TempDir()
	snap := run(t, s, dir)
	if snap.State != StateDone {
		t.Fatalf("state %d: %s", snap.State, snap.Err)
	}
	for _, name := range []string{"eqahosts.txt", "SS", "S2", "ss-art.csf", "station.pak", "WORDS.TXT"} {
		want, _ := os.ReadFile(filepath.Join(base, name))
		got, _ := os.ReadFile(filepath.Join(dir, name))
		if !bytes.Equal(got, want) {
			t.Errorf("%s: %d bytes, want %d", name, len(got), len(want))
		}
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func listDir(t *testing.T, dir string) []string {
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}
