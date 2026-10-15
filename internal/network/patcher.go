package network

import (
	"bytes"
	"compress/gzip"
	"encoding/xml"
	"fmt"
	"hash/crc32"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// The patcher is the PS2's: SETUP's VIWndPatcher drives PatcherAPI, which reads
// the server's VerantPatcher manifest off its patch port and brings the memory
// card's game folder in line with it before STATION and CLIENT load
// (claude-notes/patcher-system.md). This client keeps that folder on disk
// instead of on a card — one per server, since each server patches its own.

// CardFolder is the game's folder on the PS2's memory card. The save folder
// mirrors it, so a file lands under the name the PS2 would give it.
const CardFolder = "BASLUS-20744EQONLINE"

// maxPatchFile bounds one download. Sandstorm's largest is 45 KB; a memory
// card holds 8 MB.
const maxPatchFile = 8 << 20

// PatchFile is one <File> of the manifest.
type PatchFile struct {
	LocalName    string `xml:"LocalName,attr"`
	RemotePath   string `xml:"RemotePath,attr"`
	Server       string `xml:"Server,attr"`
	Name         string `xml:"Name,attr"`
	CRC          uint32 `xml:"CRC,attr"`
	TotalSize    int    `xml:"TotalSize,attr"`
	DownloadSize int    `xml:"DownloadSize,attr"`
	Delete       bool   `xml:"Delete,attr"`
}

// PatchManifest is the VerantPatcher manifest's one distribution.
type PatchManifest struct {
	Product struct {
		Version      int `xml:"Version,attr"`
		Distribution struct {
			Name   string      `xml:"Name,attr"`
			UseCRC bool        `xml:"UseCRC,attr"`
			Files  []PatchFile `xml:"File"`
		} `xml:"Distribution"`
	} `xml:"Product"`
}

// PatchReport says what a patcher run did.
type PatchReport struct {
	Dir                         string
	Fetched, Kept, Deleted      []string
	Failed                      map[string]error
	Distribution                string
	ProductVersion, ManifestLen int
}

// FetchManifest reads the manifest the PS2 asks for.
func FetchManifest(host string, port int) (*PatchManifest, error) {
	url := fmt.Sprintf("http://%s:%d/patch/eqoa/eqoa-frontierslive-update.xml.gz", host, port)
	data, err := httpGetGzip(url, 1<<20)
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	var m PatchManifest
	if err := xml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	return &m, nil
}

// RunPatcher brings dir (a server's save folder, standing in for the card
// folder) in line with the server's manifest: a Delete entry removes the file,
// and a file is fetched when it is missing or its size differs — the PS2's own
// test — or, under UseCRC, its CRC32 does. Every download is checked against
// the manifest's size and CRC before it replaces the old copy. A file that
// fails is reported and left as it was; the run carries on.
func RunPatcher(host string, port int, dir string) (*PatchReport, error) {
	m, err := FetchManifest(host, port)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	d := &m.Product.Distribution
	rep := &PatchReport{Dir: dir, Failed: map[string]error{}, Distribution: d.Name,
		ProductVersion: m.Product.Version, ManifestLen: len(d.Files)}
	for _, f := range d.Files {
		name, ok := cardName(f.LocalName)
		if !ok {
			rep.Failed[f.LocalName] = fmt.Errorf("refused: not a plain file name")
			continue
		}
		local := filepath.Join(dir, name)
		if f.Delete {
			if err := os.Remove(local); err == nil {
				rep.Deleted = append(rep.Deleted, name)
			} else if !os.IsNotExist(err) {
				rep.Failed[name] = err
			}
			continue
		}
		if have, err := os.ReadFile(local); err == nil && len(have) == f.TotalSize &&
			(!d.UseCRC || crc32.ChecksumIEEE(have) == f.CRC) {
			rep.Kept = append(rep.Kept, name)
			continue
		}
		if err := fetchPatchFile(host, port, f, local); err != nil {
			rep.Failed[name] = err
			continue
		}
		rep.Fetched = append(rep.Fetched, name)
	}
	log.Printf("Patcher: %s v%d, %d entries → %s: fetched %v, kept %d, deleted %v, failed %d",
		rep.Distribution, rep.ProductVersion, rep.ManifestLen, dir, rep.Fetched, len(rep.Kept), rep.Deleted, len(rep.Failed))
	for n, err := range rep.Failed {
		log.Printf("Patcher: %s: %v", n, err)
	}
	return rep, nil
}

// fetchPatchFile downloads one file from the patch port, at
// /<RemotePath>/<Name> (the root when RemotePath is empty), and writes it
// through a temporary file so a failed download never leaves half a file.
func fetchPatchFile(host string, port int, f PatchFile, local string) error {
	server := f.Server
	if server == "" {
		server = host
	}
	url := fmt.Sprintf("http://%s:%d/%s", server, port, strings.TrimPrefix(path.Join(f.RemotePath, f.Name), "/"))
	data, err := httpGetGzip(url, maxPatchFile)
	if err != nil {
		return err
	}
	if len(data) != f.TotalSize {
		return fmt.Errorf("size %d, manifest says %d", len(data), f.TotalSize)
	}
	if crc := crc32.ChecksumIEEE(data); crc != f.CRC {
		return fmt.Errorf("CRC %d, manifest says %d", crc, f.CRC)
	}
	tmp := local + ".part"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, local)
}

// cardName is a manifest LocalName as a file in the save folder: a plain
// name, nothing that climbs out of the folder.
func cardName(n string) (string, bool) {
	if n == "" || n == "." || n == ".." || strings.ContainsAny(n, `/\:`) || strings.ContainsRune(n, 0) {
		return "", false
	}
	return n, true
}

// httpGetGzip fetches a gzipped body and returns it decompressed, at most
// limit bytes of it.
func httpGetGzip(url string, limit int64) ([]byte, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: status %d", url, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	data, err := io.ReadAll(io.LimitReader(gz, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s: larger than %d bytes", url, limit)
	}
	return data, nil
}
