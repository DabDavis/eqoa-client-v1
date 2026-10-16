package patcher

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A save folder is a server's patch module: everything that server's patcher
// put on "the card", kept in a folder of its own. A PS2 has one card folder,
// so playing on another server means patching it over; this client keeps one
// per server, so a module that is already current is loaded as it is, on
// demand, and patched only when the server's manifest asks for a change.

// Plan is what a patch would change in a save folder: the files it would
// fetch and the ones it would delete. An empty plan means the folder is what
// the manifest asks for.
type Plan struct {
	Fetch  []string
	Delete []string
}

// Empty says the patch would change nothing.
func (p *Plan) Empty() bool { return p != nil && len(p.Fetch) == 0 && len(p.Delete) == 0 }

// Check is RunPatcher's decision without its download: the folder read, the
// manifest fetched and the tree built against it exactly as Initialize builds
// it — so every file is judged as the patcher would judge it — then the files
// a download would fetch and the deletes that would find something, listed.
// It writes nothing.
func Check(cfg Config) (*Plan, error) {
	cfg.defaults()
	images, err := ReadFolder(cfg.Dir, nil)
	if err != nil {
		return nil, err
	}
	s := &setup{st: NewStatus(), cfg: Config{}, initializing: true}
	p := New(s)
	p.SetPatchImage(images)
	if !p.Initialize(cfg.Host+":"+strconv.Itoa(cfg.Port), cfg.Port, cfg.Manifest) {
		if s.lastError != "" {
			return nil, errors.New(strings.TrimSpace(s.lastError))
		}
		return nil, errors.New("no manifest")
	}
	plan := &Plan{}
	if p.distList != nil && p.distList.downloader != nil {
		planFetch(p.distList.downloader, plan)
		planDelete(p.distList.downloader, images, plan)
	}
	return plan, nil
}

// planFetch walks the tree as a download does: a distribution's files only
// when it is selected, its child distributions always; a directory only when
// something under it needs fetching.
func planFetch(d downloader, plan *Plan) {
	b := d.base()
	switch n := d.(type) {
	case *distributionDownloader:
		for c := b.child; c != nil; c = c.base().next {
			if c.base().isDistribution || n.info.selected {
				planFetch(c, plan)
			}
		}
	case *directoryDownloader:
		if b.needDownloadFor(0) != 0 && !b.needDelete() {
			for c := b.child; c != nil; c = c.base().next {
				planFetch(c, plan)
			}
		}
	case *fileDownloader:
		if !b.needDelete() && b.needDownloadFor(0) != 0 {
			plan.Fetch = append(plan.Fetch, str(b.localPath))
		}
	}
}

// planDelete walks the tree as DeleteOldFiles does — all of it — for the
// Delete entries that name a file the folder has.
func planDelete(d downloader, images *ImageList, plan *Plan) {
	b := d.base()
	if f, ok := d.(*fileDownloader); ok && f.needDelete() && images.find(str(f.localPath)) != nil {
		plan.Delete = append(plan.Delete, str(f.localPath))
	}
	for c := b.child; c != nil; c = c.base().next {
		planDelete(c, images, plan)
	}
}

// HasModule says whether a save folder holds a patch: any file at all, as a
// finished patch leaves (a failed one writes nothing).
func HasModule(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Type().IsRegular() && !strings.HasSuffix(e.Name(), ".part") {
			return true
		}
	}
	return false
}

// Record describes a patch module: where it came from and what it holds,
// written beside the card folder after each finished patch. The client reads
// the folder itself, not this; the record is for people, and for the
// problem report.
type Record struct {
	Server   string       `json:"server"`
	Port     int          `json:"port"`
	Manifest string       `json:"manifest"`
	Product  string       `json:"product,omitempty"`
	Patched  time.Time    `json:"patched"`
	Files    []RecordFile `json:"files"`
}

// RecordFile is one file of the module, as the patch left it.
type RecordFile struct {
	Name string `json:"name"`
	Size int    `json:"size"`
	CRC  uint32 `json:"crc"`
}

// ReadRecord reads a module's record; nil when it has none.
func ReadRecord(path string) *Record {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var r Record
	if json.Unmarshal(data, &r) != nil {
		return nil
	}
	return &r
}

func writeRecord(cfg Config, product string, images *ImageList) error {
	r := Record{Server: cfg.Host, Port: cfg.Port, Manifest: cfg.Manifest, Product: product, Patched: time.Now().UTC()}
	for _, im := range images.images {
		if im.Deleted || im.Name == "" {
			continue
		}
		r.Files = append(r.Files, RecordFile{Name: im.Name, Size: len(im.Data), CRC: im.CRC})
	}
	sort.Slice(r.Files, func(i, j int) bool { return r.Files[i].Name < r.Files[j].Name })
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Record), 0o755); err != nil {
		return err
	}
	tmp := cfg.Record + ".part"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, cfg.Record)
}
