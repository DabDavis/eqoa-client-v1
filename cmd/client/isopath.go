package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// readISOPath reads a file off the disc image by its path ("DATA/PSVRPATH.DAT"),
// through the disc's own ISO 9660 directories — esf-tools reads by a table of
// sectors, which has only the files it parses. limit bounds the file.
func readISOPath(isoPath, path string, limit int) ([]byte, error) {
	f, err := os.Open(isoPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	pvd := make([]byte, 2048)
	if _, err := f.ReadAt(pvd, 16*2048); err != nil {
		return nil, err
	}
	if pvd[0] != 1 || string(pvd[1:6]) != "CD001" {
		return nil, errors.New("not an ISO 9660 image")
	}
	lba := binary.LittleEndian.Uint32(pvd[156+2:])
	size := binary.LittleEndian.Uint32(pvd[156+10:])
	parts := strings.Split(strings.Trim(path, `/\`), "/")
	for i, part := range parts {
		last := i == len(parts)-1
		if size > 1<<20 {
			return nil, fmt.Errorf("%s: directory of %d bytes", path, size)
		}
		dir := make([]byte, size)
		if _, err := f.ReadAt(dir, int64(lba)*2048); err != nil && err != io.EOF {
			return nil, err
		}
		found := false
		for off := 0; off < len(dir); {
			n := int(dir[off])
			if n == 0 { // records do not cross a sector: the rest of this one is padding
				off = (off/2048 + 1) * 2048
				continue
			}
			if n < 34 || off+n > len(dir) || 33+int(dir[off+32]) > n {
				break
			}
			rec := dir[off : off+n]
			name := string(rec[33 : 33+int(rec[32])])
			if j := strings.IndexByte(name, ';'); j >= 0 {
				name = name[:j]
			}
			isDir := rec[25]&2 != 0
			if strings.EqualFold(strings.TrimSuffix(name, "."), part) && isDir != last {
				lba = binary.LittleEndian.Uint32(rec[2:])
				size = binary.LittleEndian.Uint32(rec[10:])
				found = true
				break
			}
			off += n
		}
		if !found {
			return nil, fmt.Errorf("%s: not on the disc", path)
		}
	}
	if int(size) > limit {
		return nil, fmt.Errorf("%s: %d bytes", path, size)
	}
	data := make([]byte, size)
	if _, err := f.ReadAt(data, int64(lba)*2048); err != nil && err != io.EOF {
		return nil, err
	}
	return data, nil
}
