package patcher

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// CardFolder is the game's folder on the PS2's memory card. A save folder
// mirrors it, so a file lands under the name the PS2 would give it.
const CardFolder = "BASLUS-20744EQONLINE"

// cardReadLimit is ReadPatchData's buffer, memalign(128, 0x400000): a file
// is read up to 4 MB.
const cardReadLimit = 0x400000

// ReadFolder is VISetup::ReadPatchData (0x00569798) over a save folder:
// every file in it, into memory, one Image each, newest first (each is put
// at the head of the chain). An .ENC — a module the PS2 keeps personalised
// to its console — reads as its .AUT name, with the size and CRC its 8-byte
// header records and a body of zeros: all the patcher compares.
//
// progress is VISetup::OnTick(0)'s input: the fraction of files read.
//
// Not the PS2's: a missing folder is created (the console would have
// "prepared" its card), a .part (this client's half-written download) is
// skipped, and the PS2 reads at most 18 directory entries
// (VIMemCardGetDir(dir, 18)), four of them its own icon and option files,
// which a save folder does not have — so no limit is applied.
func ReadFolder(dir string, progress func(float32)) (*ImageList, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []os.DirEntry
	for _, e := range entries {
		if e.Type().IsRegular() && !strings.HasSuffix(e.Name(), ".part") {
			files = append(files, e)
		}
	}
	list := &ImageList{}
	for i, e := range files {
		data, err := readLimited(filepath.Join(dir, e.Name()), cardReadLimit)
		if err != nil {
			// The PS2 retries a failed read twice, then goes on without it.
			log.Printf("patcher: reading %s: %v", e.Name(), err)
			continue
		}
		im := &Image{Name: e.Name()}
		if j := strings.Index(im.Name, ".ENC"); j >= 0 && len(data) >= 8 {
			im.DiskName = im.Name
			im.Name = im.Name[:j] + ".AUT" + im.Name[j+4:]
			size := int(binary.LittleEndian.Uint32(data))
			im.CRC = binary.LittleEndian.Uint32(data[4:])
			im.Data = make([]byte, max(size, 0))
		} else {
			im.Data = data
			im.CRC = crc32Bytes(data)
		}
		list.prepend(im)
		if progress != nil {
			progress(float32(i+1) / float32(len(files)))
		}
	}
	return list, nil
}

func readLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit))
}

// WriteFolder is VISetup::WritePatchData (0x00569B98) over a save folder:
// every dirty image written, every deleted one removed, in chain order. It
// reports whether anything changed — RunPatcher's m_x218B4, which opens the
// release notes when the patch is done — and progress is OnTick(100)'s input
// (the fraction of images handled).
//
// Not the PS2's, because it is the card's or the console's: the card's
// pad.dat is not unpadded and repadded, the game options are not saved
// (this client keeps its own), and a module (.AUT) is written as it came —
// the PS2 personalises it to the console with DNAS and keeps it as .ENC. The
// PS2 then asks for a restart when SUPPORT or SETUP changed, and hands a new
// STATION or CLIENT to the launcher; this client runs no PS2 module, so a
// patched one is noted and changes nothing.
//
// Each file goes through a .part and a rename, so a failed write never
// leaves half a file. A name that is not a plain file name in the folder is
// refused (the PS2 writes whatever the manifest says onto its card).
func WriteFolder(dir string, images *ImageList, progress func(float32)) (bool, error) {
	changed := false
	for _, im := range images.images {
		if im.Dirty || im.Deleted {
			changed = true
			break
		}
	}
	if !changed {
		if progress != nil {
			progress(1)
		}
		return false, nil
	}
	total := len(images.images)
	for i, im := range images.images {
		if im.Dirty {
			if !safeName(im.Name) {
				return true, fmt.Errorf("refusing to write %q: not a plain file name", im.Name)
			}
			log.Printf("patcher: POST PATCH: Writing (new/modified) file (%s), CRC = %d", im.Name, im.CRC)
			if isModule(im.Name) {
				log.Printf("patcher: %s is a PS2 module; it is kept, and this client does not run it", im.Name)
			}
			path := filepath.Join(dir, im.Name)
			if err := os.WriteFile(path+".part", im.Data, 0o644); err != nil {
				return true, err
			}
			if err := os.Rename(path+".part", path); err != nil {
				return true, err
			}
			if im.DiskName != "" && im.DiskName != im.Name {
				os.Remove(filepath.Join(dir, im.DiskName))
				im.DiskName = ""
			}
		}
		if im.Deleted {
			name := im.Name
			if im.DiskName != "" {
				name = im.DiskName
			}
			log.Printf("patcher: POST PATCH: Deleting file (%s)", name)
			if safeName(name) {
				if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
					return true, err
				}
			}
		}
		if progress != nil {
			progress(float32(i) / float32(total))
		}
	}
	if progress != nil {
		progress(1)
	}
	return true, nil
}

// isModule is the names WritePatchData treats as modules (.AUT).
func isModule(name string) bool { return strings.Contains(name, ".AUT") }

// safeName is a plain file name in the folder: no separator, no drive or
// stream colon, no control character, not "." or "..", and none of the
// device names Windows will not let a file take.
func safeName(n string) bool {
	if n == "" || n == "." || n == ".." || strings.ContainsAny(n, `/\:`) {
		return false
	}
	for _, r := range n {
		if r < ' ' {
			return false
		}
	}
	stem := strings.ToUpper(n)
	if i := strings.IndexByte(stem, '.'); i >= 0 {
		stem = stem[:i]
	}
	switch stem {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return false
	}
	return true
}
