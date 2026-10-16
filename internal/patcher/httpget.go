package patcher

import (
	"bytes"
	"compress/flate"
	"io"
	"strings"
)

// Http_get's status (+0x1340), as PatcherAPI_Impl reads it: 4 the bytes are
// in, 5 failed, 6 done, 8 aborted. Get_thread runs to completion before Get
// returns, so the patcher only ever sees the last one.
const (
	httpStatusReading = 4
	httpStatusFailed  = 5
	httpStatusDone    = 6
	httpStatusAborted = 8
)

// fileBufferSize is Http_get::Get's staging buffer, malloc(0x200000): a file
// the patcher downloads is at most 2 MB once inflated.
const fileBufferSize = 0x200000

// httpGet is Http_get (http_get.cpp, 0x005816A8): one HTTP fetch into
// memory, inflated when asked, and from there into the image list.
type httpGet struct {
	images     *ImageList // +0x1330
	listener   urlListener
	errorText  cstr // +0x1338
	errorType  int  // +0x133C
	status     int  // +0x1340
	size       int  // +0x1344, Get_thread's length
	mem        []byte
	bytesDown  int // +0x1350
	closed     bool
	compressed bool
}

// newHTTPGet (0x005816A8).
func newHTTPGet(images *ImageList) *httpGet {
	return &httpGet{images: images, status: -1, compressed: true}
}

// Get (0x005817F0) fetches url into a 2 MB buffer and hands it to WriteFile
// under file's name. It always returns 0: the outcome is the status. The
// image is written whatever happened — a failed fetch leaves a 0-byte dirty
// image — which is harmless only because a failed download fails the whole
// patch, and VISetup then writes nothing.
func (h *httpGet) Get(url, file string, compressed bool) int {
	h.mem = nil
	if im := h.images.find(file); im != nil {
		im.Data = nil
	}
	h.mem = make([]byte, fileBufferSize)
	h.MemoryGet(h.mem, url, compressed)
	h.WriteFile(file)
	h.mem = nil
	h.size = 0
	return 0
}

// WriteFile (0x005818F8) puts what Get_thread left in the image whose name
// occurs in file, or in a new one at the end of the chain named after file's
// last path component. Either way it is now dirty, with its CRC recomputed.
func (h *httpGet) WriteFile(file string) {
	im := h.images.find(file)
	if im != nil {
		im.Data = nil
	} else {
		im = &Image{}
		h.images.appendImage(im)
	}
	name := ""
	tk := strtok{s: file}
	for t, ok := tk.next(`/\`); ok; t, ok = tk.next(`/\`) {
		name = t
	}
	if name == "" {
		return
	}
	im.Name = name
	im.Data = append([]byte(nil), h.mem[:h.size]...)
	im.CRC = crc32Bytes(im.Data)
	im.Dirty = true
}

// MemoryGet (0x00581AD0) fetches url into dest, which ends up holding a C
// string: the inflated body, or the raw one copied up to its first NUL.
func (h *httpGet) MemoryGet(dest []byte, url string, compressed bool) int {
	h.compressed = compressed
	return h.getThread(url, dest)
}

// getThread is Get_thread (0x00581E30). What it leaves depends on whether
// the body inflated:
//
//   - compressed and it inflated: dest holds the inflated bytes, then a NUL,
//     and the length (m_size) is how many came out;
//   - otherwise dest still reads "Hello", and the raw body is strncpy'd over
//     it — up to the first NUL — and the length stays 0.
//
// So a <File Compressed="False"> always becomes a 0-byte image, and its CRC
// check fails: on the PS2 every file must be served gzipped. The manifest is
// read as a C string, so for it either way works.
func (h *httpGet) getThread(url string, dest []byte) int {
	length := 0
	if dest == nil {
		return 0
	}
	temp := make([]byte, len(dest))
	if len(dest) > 0 {
		dest[0] = 0
	}
	u := newVURL(url)
	u.listener = h.listener
	if !u.get(temp) {
		h.setError(cs(u.errText), u.err)
		h.status = httpStatusAborted
		return 0
	}
	bytesIn := 0
	if !h.closed {
		bytesIn = u.bytes
		h.status = httpStatusReading
		h.bytesDown = bytesIn
	}
	copy(dest, "Hello\x00")
	size := len(dest)
	if h.compressed {
		size = httpUncompress(dest, temp[:bytesIn])
		length = size
	}
	if strings.EqualFold(cstring(dest), "Hello") {
		// strncpy(m_dest, m_temp, bytes + 1): to the first NUL, NUL-padded.
		n := min(bytesIn+1, len(dest))
		k := bytes.IndexByte(temp[:min(n, len(temp))], 0)
		if k < 0 {
			k = min(n, len(temp))
		}
		copy(dest, temp[:k])
		clear(dest[k:n])
	} else if size < len(dest) {
		dest[size] = 0
	}
	if h.closed {
		h.status = httpStatusAborted
	} else {
		h.status = httpStatusDone
	}
	h.size = length
	return length
}

// setError (0x00582090).
func (h *httpGet) setError(msg cstr, typ int) {
	h.errorType = typ
	h.errorText = nil
	if msg != nil {
		h.errorText = cs(*msg)
	}
	h.status = httpStatusFailed
	h.closed = true
}

// httpUncompress is http_uncompress (0x00581D60): zlib 1.1.4's uncompress,
// cut down. inflateInit is followed by poking the inflate state's mode to
// BLOCKS, so no zlib header is read; http_check_header skips a gzip header
// if there is one; and the trailer is never checked — the adler32 compare
// that BLOCKS falls into fails on a gzip CRC, after every byte is out, and
// nobody looks at the return. It returns total_out, as *destLen.
func httpUncompress(dest, src []byte) int {
	r := bytes.NewReader(src)
	httpCheckHeader(r)
	fr := flate.NewReader(r)
	n, _ := io.ReadFull(fr, dest)
	return n
}

// httpCheckHeader is http_check_header (0x00581B58), gzio.c's check_header:
// the magic, then method and flags, six bytes of time/xfl/os, and the
// optional extra, name, comment and header CRC. No magic: the bytes go back
// and the stream is read as raw deflate. A method other than 8 consumes the
// four bytes and stops.
func httpCheckHeader(r *bytes.Reader) {
	// Whichever byte of the magic is wrong, or missing, the bytes it read are
	// all put back (avail_in++, next_in--), so the stream is where it began.
	start, _ := r.Seek(0, io.SeekCurrent)
	magic := [2]byte{0x1F, 0x8B}
	for i := 0; i < 2; i++ {
		if c, err := r.ReadByte(); err != nil || c != magic[i] {
			r.Seek(start, io.SeekStart)
			return
		}
	}
	method, _ := r.ReadByte()
	flags, _ := r.ReadByte()
	if method != 8 {
		return
	}
	for i := 0; i < 6; i++ {
		r.ReadByte()
	}
	if flags&0x04 != 0 {
		lo, _ := r.ReadByte()
		hi, _ := r.ReadByte()
		for n := int(lo) | int(hi)<<8; n > 0; n-- {
			if _, err := r.ReadByte(); err != nil {
				break
			}
		}
	}
	if flags&0x08 != 0 {
		for {
			if c, err := r.ReadByte(); err != nil || c == 0 {
				break
			}
		}
	}
	if flags&0x10 != 0 {
		for {
			if c, err := r.ReadByte(); err != nil || c == 0 {
				break
			}
		}
	}
	if flags&0x02 != 0 {
		r.ReadByte()
		r.ReadByte()
	}
}

// cstring is the C string at the start of b.
func cstring(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		return string(b[:i])
	}
	return string(b)
}
