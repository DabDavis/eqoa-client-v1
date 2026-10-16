package patcher

import (
	"hash/crc32"
	"strings"
)

// Image is VIPatchImage (patcher.h): one file of the card's game folder, held
// in memory for the length of a patch. VISetup reads the whole folder into a
// list of them before the patcher runs and writes the dirty ones back after;
// in between, every "file" the patcher checks, downloads or deletes is one of
// these. On the PS2 the list is a chain of 0x58-byte records; Name is
// m_name[64].
type Image struct {
	Name    string // the patcher's name for it (a module's .ENC reads as .AUT)
	Data    []byte
	CRC     uint32 // crc32 of Data, or the original CRC an .ENC header records
	Dirty   bool   // downloaded: to be written
	Deleted bool   // a Delete entry named it: to be removed

	// DiskName is the file the image came from, when it differs from Name
	// (an .ENC). Not the PS2's: the card keeps that in the directory read.
	DiskName string
}

// ImageList is the chain VISetup hands the patcher (SetPatchImage) and the
// downloads extend (Http_get::WriteFile appends). The PS2's list is a bare
// head pointer and needs one file on the card to exist at all —
// PatcherAPI_Impl::DownloadToMemory gives up (-1) when FindPatchImage(0) is
// NULL — which a PS2 card always has (icon.sys). A save folder can be empty,
// so the list here is a container: an empty folder is an empty list, not a
// failed patch.
type ImageList struct {
	images []*Image // newest first, as ReadPatchData prepends
}

// Images returns the list in chain order.
func (l *ImageList) Images() []*Image { return l.images }

// find is FindPatchImage (0x00586808) and the walks Http_get repeats: the
// first image whose name occurs IN name — strstr(name, image->m_name), so a
// path "data/S2" finds the image "S2". Case matters.
func (l *ImageList) find(name string) *Image {
	for _, im := range l.images {
		if strings.Contains(name, im.Name) {
			return im
		}
	}
	return nil
}

// appendImage puts a new image at the END of the chain (WriteFile's
// `last->m_next = image`), where ReadPatchData puts each one at the head.
func (l *ImageList) appendImage(im *Image) { l.images = append(l.images, im) }

func (l *ImageList) prepend(im *Image) {
	l.images = append([]*Image{im}, l.images...)
}

// crc32Bytes is zlib's crc32(0, data, len): the IEEE polynomial, as the
// manifest's CRC attribute is computed.
func crc32Bytes(b []byte) uint32 { return crc32.ChecksumIEEE(b) }
