package patcher

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// urlListener is VIUrlListener: VUrl::Get reports every chunk to it.
type urlListener interface {
	OnBytesDownloaded(url, file string, got, total int)
}

// stallTimeout is not the PS2's. Its receive loop has no deadline at all and
// waits for the server to close or to meet Content-Length; a server that does
// neither hangs the console on "Updating". A PC client gives up instead.
const stallTimeout = 30 * time.Second

// dialTimeout is sceInetOpen's 10,000 ms.
const dialTimeout = 10 * time.Second

// vurl is VUrl (vurl.cpp, 0x005DC568): the HTTP/1.x client over libinet that
// every download ends in. Only Get is the patcher's; GetPoll, GetToMemoryCard
// and the memory-card writer are STATION's, and are not ported.
type vurl struct {
	protocol int  // 0 http, -1 none
	server   cstr // +0x04
	path     cstr // +0x08
	port     int  // +0x0C, 80 until the URL names one
	err      int  // +0x14
	errText  string
	bytes    int // +0x45C, the body bytes the last Get received
	listener urlListener
}

// newVURL is VUrl::VUrl(const char *url): Initialize, then ParseURL.
func newVURL(url string) *vurl {
	u := &vurl{}
	u.initialize()
	u.parseURL(url)
	return u
}

// initialize (0x005DC698).
func (u *vurl) initialize() {
	u.listener = nil
	u.server = nil
	u.path = nil
	u.err = 0
	u.bytes = 0
	u.protocol = -1
	u.port = 80
	u.errText = "No error"
}

// setProtocol (0x005DC748): http only.
func (u *vurl) setProtocol(p int) {
	if p != 0 {
		u.err = 1
		u.errText = fmt.Sprintf("Unsupported protocol %d", p)
		u.protocol = -1
		return
	}
	u.protocol = 0
}

// setServer (0x005DC7A8).
func (u *vurl) setServer(s cstr) {
	if s == nil {
		u.err = 3
		u.errText = "Cannot set server to NULL"
		return
	}
	u.server = cs(*s)
}

// setRelativePath (0x005DC820).
func (u *vurl) setRelativePath(p cstr) {
	if p == nil {
		u.err = 4
		u.errText = "Cannot set path to NULL"
		return
	}
	u.path = cs(*p)
}

// parseURL (0x005DC898): "http://server[:port]/path[#...]", split with
// strtok, so runs of '/' between the parts collapse; the path's blanks become
// %20. The port is atoi of everything after the FIRST colon: SETUP's server
// string "patch.everquestonlineadventures.com:7000" gets ":7000" appended
// again by DownloadToMemory, and atoi("7000:7000") is still 7000.
func (u *vurl) parseURL(url string) {
	tk := strtok{s: url}
	tok, ok := tk.next("/")
	if ok && tok == "http:" {
		u.setProtocol(0)
	} else {
		u.setProtocol(-1)
		return
	}
	tok, ok = tk.next("/")
	if !ok {
		u.setServer(nil)
		return
	}
	if c := strings.IndexByte(tok, ':'); c < 0 {
		u.setServer(cs(tok))
	} else {
		u.setServer(cs(tok[:c]))
		u.port = int(atoi(tok[c+1:]))
	}
	tok, ok = tk.next("#")
	if !ok {
		u.setRelativePath(cs("/"))
		return
	}
	u.setRelativePath(cs(strings.ReplaceAll(tok, " ", "%20")))
}

// openSocket (0x005DCAA0): resolve, then connect. The two failures are
// errors 3 and 5.
func (u *vurl) openSocket() net.Conn {
	if u.server == nil {
		u.err = 3
		u.errText = "Cannot connect to NULL server"
		return nil
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(*u.server, strconv.Itoa(u.port)), dialTimeout)
	if err != nil {
		var dns *net.DNSError
		if errors.As(err, &dns) {
			u.err = 3
			u.errText = fmt.Sprintf("Cannot resolve server %s", *u.server)
		} else {
			u.err = 5
			u.errText = fmt.Sprintf("Could not connect to %s", *u.server)
		}
		return nil
	}
	return conn
}

// get (0x005DCBB8) fetches the whole body into buf. The request is exactly
// the PS2's — "GET /<path> HTTP/1.1", a Host line naming the server without
// its port, nothing else — and so is the reading of the answer:
//
//   - the header ends at the first CR that follows an LF;
//   - Content-Length is found by a case-sensitive substring match on each
//     header line, and is atoi of the text two characters after its colon;
//   - it succeeds only when Content-Length was given and met. A server that
//     sends no Content-Length (chunked, or close-delimited) fails, every time.
//
// Two PS2 failures are not reproduced. A header that does not end in the
// first chunk makes the console memcpy a negative length (it crashes); here
// the scan carries on into the next chunk. A body larger than buf overruns
// the console's heap; here it fails.
func (u *vurl) get(buf []byte) bool {
	if u.path == nil {
		u.err = 4
		u.errText = "Cannot get NULL path"
		return false
	}
	conn := u.openSocket()
	if conn == nil {
		return false
	}
	defer conn.Close()
	clear(buf)
	req := fmt.Sprintf("GET /%s HTTP/1.1\r\nHost: %s\r\n\r\n", *u.path, *u.server)
	conn.Write([]byte(req)) // sceInetSend; the PS2 does not look at the result

	total := 0
	headerDone := false
	parsed := false
	var lastChar byte
	var header []byte
	contentLength := -1
	data := make([]byte, 1460)
	lastData := time.Now()
	for {
		conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond)) // sceInetRecv's 100 ms
		n, err := conn.Read(data)
		closed := false
		if err != nil {
			var ne net.Error
			switch {
			case errors.Is(err, io.EOF):
				closed = true // recvFlags & 4
			case errors.As(err, &ne) && ne.Timeout():
				if time.Since(lastData) > stallTimeout {
					u.err = 5
					u.errText = fmt.Sprintf("No data from %s for %v", *u.server, stallTimeout)
					return false
				}
			default:
				return false // "Error, code = %i": m_error is left as it was
			}
		}
		if n > 0 {
			lastData = time.Now()
			start := 0
			if !headerDone {
				for i := 0; i < n; i++ {
					if data[i] == '\r' && lastChar == '\n' {
						headerDone = true
						start = i + 2 // past the blank line's LF
						break
					}
					lastChar = data[i]
					header = append(header, data[i])
				}
				if !headerDone {
					start = n
				}
				if start > n {
					start = n // the blank line's LF is in the next chunk
				}
			}
			if headerDone && !parsed {
				parsed = true
				for _, line := range strings.FieldsFunc(string(header), func(r rune) bool { return r == '\r' || r == '\n' }) {
					if strings.Contains(line, "Content-Length") {
						if c := strings.IndexByte(line, ':'); c >= 0 && c+2 <= len(line) {
							contentLength = int(atoi(line[c+2:]))
						}
					}
				}
			}
			body := n - start
			if total+body > len(buf) {
				u.err = 6
				u.errText = fmt.Sprintf("Body larger than the %d-byte buffer", len(buf))
				return false
			}
			if u.listener != nil {
				u.listener.OnBytesDownloaded(*u.server, *u.path, body, total+body)
			}
			copy(buf[total:], data[start:n])
			total += body
			if contentLength != -1 && total >= contentLength {
				break
			}
		} else if u.listener != nil {
			u.listener.OnBytesDownloaded(*u.server, *u.path, 0, total)
		}
		if closed {
			break
		}
	}
	u.bytes = total
	if contentLength == -1 || total < contentLength {
		return false
	}
	return true
}

// strtok is C's, over one string: each call skips leading delimiters, takes
// the token, and consumes the one delimiter that ends it.
type strtok struct {
	s   string
	pos int
}

func (t *strtok) next(delims string) (string, bool) {
	for t.pos < len(t.s) && strings.IndexByte(delims, t.s[t.pos]) >= 0 {
		t.pos++
	}
	if t.pos >= len(t.s) {
		return "", false
	}
	start := t.pos
	for t.pos < len(t.s) && strings.IndexByte(delims, t.s[t.pos]) < 0 {
		t.pos++
	}
	tok := t.s[start:t.pos]
	if t.pos < len(t.s) {
		t.pos++
	}
	return tok, true
}
