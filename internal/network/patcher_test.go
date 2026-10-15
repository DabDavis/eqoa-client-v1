package network

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"hash/crc32"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func gz(b []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

// A manifest server with one file to fetch, one to delete and one whose
// LocalName tries to leave the folder.
func patchServer(t *testing.T, content []byte, crc uint32) (string, int) {
	manifest := fmt.Sprintf(`<VerantPatcher Version="1"><Product Version="36"><Distribution Name="Test" UseCRC="True">`+
		`<File LocalName="ss-art.csf" RemotePath="" Server="" Name="ss-art-4.csf.gz" CRC="%d" TotalSize="%d" DownloadSize="1"></File>`+
		`<File LocalName="words.txt" Delete="True"></File>`+
		`<File LocalName="../escape" RemotePath="" Server="" Name="x.gz" CRC="0" TotalSize="0"></File>`+
		`</Distribution></Product></VerantPatcher>`, crc, len(content))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/patch/eqoa/eqoa-frontierslive-update.xml.gz":
			w.Write(gz([]byte(manifest)))
		case "/ss-art-4.csf.gz":
			w.Write(gz(content))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	host, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	p, _ := strconv.Atoi(port)
	return host, p
}

func TestRunPatcher(t *testing.T) {
	content := []byte("CESF pretend art")
	host, port := patchServer(t, content, crc32.ChecksumIEEE(content))
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "words.txt"), []byte("old"), 0o644)

	rep, err := RunPatcher(host, port, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "ss-art.csf")); !bytes.Equal(got, content) {
		t.Fatalf("ss-art.csf = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "words.txt")); !os.IsNotExist(err) {
		t.Fatal("a Delete entry left its file")
	}
	if _, bad := rep.Failed["../escape"]; !bad {
		t.Fatal("a LocalName that leaves the folder was not refused")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape")); err == nil {
		t.Fatal("wrote outside the folder")
	}

	// A second run keeps the file: same size, same CRC.
	rep, _ = RunPatcher(host, port, dir)
	if len(rep.Fetched) != 0 || len(rep.Kept) != 1 {
		t.Fatalf("second run fetched %v, kept %v", rep.Fetched, rep.Kept)
	}
	// Same size, different content: UseCRC fetches it again.
	os.WriteFile(filepath.Join(dir, "ss-art.csf"), bytes.Repeat([]byte("x"), len(content)), 0o644)
	if rep, _ = RunPatcher(host, port, dir); len(rep.Fetched) != 1 {
		t.Fatalf("a same-size change was not fetched: %v", rep.Fetched)
	}
}

// A download that does not match the manifest never replaces the file.
func TestRunPatcherRejectsBadCRC(t *testing.T) {
	content := []byte("CESF pretend art")
	host, port := patchServer(t, content, 12345)
	dir := t.TempDir()
	rep, _ := RunPatcher(host, port, dir)
	if rep.Failed["ss-art.csf"] == nil {
		t.Fatal("a CRC mismatch was accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "ss-art.csf")); !os.IsNotExist(err) {
		t.Fatal("a bad download was written")
	}
}
