package patcher

import (
	"fmt"
	"log"
	"strings"
)

// Callbacks are PatcherAPI's virtuals: what the patcher tells the game.
// VISetup (setup.h) overrides OnFile, OnTick and OnError and answers 1 to
// the rest; OnForceRestart it leaves to PatcherAPI, which cancels.
type Callbacks interface {
	OnStart() int
	OnProduct(name string) int
	OnFile(name string) int
	OnFileFinished(name string) int
	OnFinish() int
	OnSetTickCount(n int) int
	OnSetFileTickCount(n int) int
	OnTick(n int) int
	OnFileTick(n int) int
	OnStatusInfo(text string) int
	OnError(code int, text string) int
}

// Patcher is PatcherAPI_Impl (patcherapi_impl.cpp, 0x00582F40) with the
// PatcherAPI handle's forwarders folded in. Every operation runs
// Patcher_thread to completion; there is no thread.
type Patcher struct {
	cb Callbacks

	version      int32 // +0x008 the <VerantPatcher> Version; Product's is never read
	productName  cstr  // +0x00C
	server       cstr  // +0x010
	port         int   // +0x014
	file         cstr  // +0x018
	doc          *xmlDoc
	changes      *changes  // +0x3C4 the product's release notes
	distList     *distInfo // +0x3C8 the <Product>'s distribution
	downloading  bool      // +0x3D0
	initializing bool      // +0x3D4

	cancel bool // m_cancel (+0x6F8) and gPatcherCancel, which Cancel sets together

	bytesDone  int64 // +0x6FC
	bytesTotal int64 // +0x700 the DownloadSize of everything to fetch
	unzipDone  int64 // +0x718
	unzipTotal int64 // +0x71C the TotalSize of everything to fetch

	checkOnInitialize bool // +0x74C, set by the constructor and never cleared
	quickScan         bool // +0x750

	images *ImageList // +0x75C
}

// New is the constructor (0x00582F40), with VISetup as the callbacks.
func New(cb Callbacks) *Patcher {
	return &Patcher{cb: cb, checkOnInitialize: true}
}

// SetPatchImage (0x005867F8): the card's files, for every check and write.
func (p *Patcher) SetPatchImage(images *ImageList) {
	if images != nil {
		p.images = images
	}
}

// Version is GetVersion: the <VerantPatcher> element's Version attribute.
// The <Product>'s is not read by anything.
func (p *Patcher) Version() int { return int(p.version) }

// ProductName is GetProductName.
func (p *Patcher) ProductName() string { return str(p.productName) }

// ProductChanges is GetProductChanges' text: the product's release notes
// (the first <Changes> under <Product>), as the server sent them.
func (p *Patcher) ProductChanges() string { return p.changes.Text() }

// Cancel (0x005850B8) stops the patch: every download from here returns 100.
func (p *Patcher) Cancel() {
	p.cancel = true
}

// onForceRestart is PatcherAPI::OnForceRestart, which VISetup does not
// override: Restart(true), which is Cancel.
func (p *Patcher) onForceRestart(name string, now bool) {
	log.Printf("patcher: %s asks for a restart; the patch is cancelled", name)
	p.Cancel()
}

// tick is the bytes-downloaded tick, inline wherever the count moves.
func (p *Patcher) tick() {
	if p.bytesTotal > 100 {
		p.cb.OnTick(int(p.bytesDone / (p.bytesTotal / 100)))
	} else if p.bytesTotal != 0 {
		p.cb.OnTick(int(p.bytesDone * 100 / p.bytesTotal))
	}
}

// OnBytesDownloaded (0x00585A88) is the patcher as VUrl's listener: each
// chunk of a file moves the bar. The manifest's chunks, before any total is
// known, do not.
func (p *Patcher) OnBytesDownloaded(url, file string, got, total int) {
	if p.bytesTotal == 0 {
		return
	}
	p.bytesDone += int64(got)
	p.tick()
}

// DownloadMemoryFile (0x005831A0) fetches path into a buffer of size bytes,
// "Hello" until something is read into it; nil when the fetch failed.
func (p *Patcher) DownloadMemoryFile(size int, server cstr, port int32, path cstr, compressed bool) []byte {
	if size == 0 || server == nil || path == nil {
		return nil
	}
	buf := make([]byte, size)
	copy(buf, "Hello")
	if p.DownloadToMemory(buf, *server, port, path, compressed) == 0 {
		return buf
	}
	return nil
}

// Initialize (0x00583278) fetches the manifest at file on server:port into
// 0x19000 bytes and builds the distribution tree from it. A server
// rebuilding its manifest can answer with a page titled
// _rerun_xml_script_; the PS2 fetches again until the page goes away — and,
// a shipped bug, reports error 43 whenever the fetch SUCCEEDS, so a manifest
// that once said "come back later" always fails the patch.
func (p *Patcher) Initialize(server string, port int, file string) bool {
	p.server = cs(server)
	p.port = port
	p.file = cs(file)
	p.doc = nil
	ok := true
	mem := p.DownloadMemoryFile(0x19000, p.server, int32(p.port), p.file, true)
	if mem == nil {
		return false
	}
	told := false
	for strings.Contains(cstring(mem), "<TITLE>_rerun_xml_script_</TITLE>") {
		if !told {
			p.cb.OnStatusInfo("Updating server files... Please be patient...")
			told = true
		} else {
			p.cb.OnStatusInfo(".")
		}
		mem = p.DownloadMemoryFile(0x19000, p.server, int32(p.port), p.file, true)
		if mem == nil {
			return false // the PS2 strstr()s the NULL and crashes
		}
		p.cb.OnError(43, "Error opening remote xml file.\nCould not access patcher update file.  Please verify your network connection.  If the problem persists, please contact station.sony.com.\n")
		ok = false
	}
	if ok {
		p.doc = xmlParseMemory(mem)
		if r := p.doc.root(); r == nil || !strings.EqualFold(r.name, "VerantPatcher") {
			p.doc = nil
		}
	}
	return p.continueInitialization()
}

// continueInitialization (0x00583698): <VerantPatcher><Product> and nothing
// else will do. A Root on the product is a PC install directory; the PS2's
// chdir/mkdir are stubs, so it changes nothing, here either.
func (p *Patcher) continueInitialization() bool {
	root := p.doc.root()
	if root == nil || !strings.EqualFold(root.name, "VerantPatcher") {
		p.cb.OnError(41, "Document of the wrong type, VerantPatcher expected\n")
		p.doc = nil
		return false
	}
	p.version = PullXMLAttributeLong(root, "Version")
	n := root.children
	for n != nil && n.typ != xmlElementNode {
		n = n.next
	}
	if n == nil {
		p.cb.OnError(41, "No Product element found\n")
		return false
	}
	if !strings.EqualFold(n.name, "Product") {
		p.cb.OnError(41, "Outer node of the wrong type, Product expected\n")
		p.doc = nil
		return false
	}
	p.productName = PullXMLAttribute(n, "Name")
	p.cb.OnProduct(str(p.productName))
	p.initializeChanges(n)
	p.startInitializeThread(n)
	return true
}

// initializeChanges (0x00583A80): every <Changes> of the product.
func (p *Patcher) initializeChanges(node *xmlNode) {
	for c := node.children; c != nil; c = c.next {
		if strings.EqualFold(c.name, "Changes") {
			p.setChangesFile(c)
		}
	}
}

// setChangesFile (0x00583AF0): the release notes, an HTML (or plain) file on
// the server, fetched into 0x1F400 bytes — uncompressed unless the element
// says Compressed.
func (p *Patcher) setChangesFile(node *xmlNode) bool {
	server := PullXMLAttribute(node, "Server")
	port := PullXMLAttributeLong(node, "Port")
	remote := PullXMLAttribute(node, "RemotePath")
	compressed := PullXMLAttributeBool(node, "Compressed")
	version := int16(PullXMLAttributeLong(node, "Version"))
	if server == nil && p.server != nil {
		server = cs(*p.server)
	}
	if port == 0 {
		port = int32(p.port)
	}
	if str(remote) == "" || str(server) == "" {
		return false
	}
	html := p.DownloadMemoryFile(0x1F400, server, port, remote, compressed)
	if html == nil {
		return false
	}
	p.changes = insertChanges(p.changes, version, cstring(html))
	return true
}

// startInitializeThread (0x00583888) and Patcher_thread's op 0 (0x005865B8):
// the tree, then the needs rolled up from the files.
func (p *Patcher) startInitializeThread(node *xmlNode) {
	if p.initializing {
		return
	}
	p.initializing = true
	p.distList = &distInfo{}
	p.distList.initialize(node, p)
	p.distList.calculateHierarchicalProperties()
	p.initializing = false
	p.cb.OnStatusInfo("\n") // SetThreadInUse(0, 0)
}

// DownloadAll is PatcherAPI::DownloadAll: Download(0, flags).
func (p *Patcher) DownloadAll(flags int) bool {
	return p.download(flags)
}

// download is Download(0, flags) (0x00584830) and DownloadRoutine
// (0x00584D00). The totals are summed first — what is left to fetch, in
// DownloadSize, is the bar's 100% — then the product's tree downloads.
func (p *Patcher) download(flags int) bool {
	p.cancel = false
	if p.downloading {
		return false
	}
	p.downloading = true
	defer func() { p.downloading = false }()
	p.cb.OnStart()

	p.bytesDone, p.bytesTotal = 0, 0
	p.unzipDone, p.unzipTotal = 0, 0
	p.unzipTotal = p.getTotalSize(flags)
	p.bytesTotal = p.getDownloadSize(flags)
	if p.bytesTotal != 0 {
		p.cb.OnSetTickCount(100)
	}
	// The PS2 takes 50 estimates here; its time() is 0, so nothing comes of
	// them, and no OnEstimate is ever made.
	ok := true
	if p.distList == nil || !p.distList.download(flags) {
		log.Printf("patcher: PatcherAPI_Impl::DownloadRoutine successFalse 1")
		ok = false
	}
	if p.getDownloadSize(0) == 0 {
		p.cb.OnTick(100)
	}
	if !p.cancel {
		p.cb.OnFinish()
	}
	return ok
}

// getTotalSize (0x00585468), for every distribution.
func (p *Patcher) getTotalSize(flags int) int64 {
	if p.distList == nil {
		return 0
	}
	p.distList.resetScanFlag(3)
	return p.distList.getTotalSize(flags)
}

// getDownloadSize (0x005855C0).
func (p *Patcher) getDownloadSize(flags int) int64 {
	if p.distList == nil {
		return 0
	}
	p.distList.resetScanFlag(2)
	return p.distList.getDownloadSize(flags)
}

// DeleteOldFiles (0x00584808): every Delete entry marks its image.
func (p *Patcher) DeleteOldFiles() bool {
	if p.distList == nil {
		return false
	}
	p.distList.deleteOld()
	return true
}

// url builds a download's URL as both downloaders do: a path that starts
// with a slash (or a backslash, made a slash IN the caller's string) goes
// straight after the port; every backslash in it becomes a slash.
func url(server string, port int, path cstr) string {
	if len(*path) > 0 && ((*path)[0] == '\\' || (*path)[0] == '/') {
		*path = "/" + (*path)[1:]
		return strings.ReplaceAll(fmt.Sprintf("http://%s:%d%s", server, port, *path), `\`, "/")
	}
	return strings.ReplaceAll(fmt.Sprintf("http://%s:%d/%s", server, port, *path), `\`, "/")
}

// DownloadToMemory (0x00585718) fetches path into buf through Http_get:
// 0 done, 100 aborted (cancelled, or the fetch failed), -1 when there is no
// image list. The first OnFile names the buffer, which reads "Hello".
func (p *Patcher) DownloadToMemory(buf []byte, server string, port int32, path cstr, compressed bool) int {
	if p.images == nil {
		return -1
	}
	h := newHTTPGet(p.images)
	h.listener = p
	if p.cancel {
		return 100
	}
	if path == nil {
		p.cb.OnError(60, "Null path requested for download.\n")
		return 0
	}
	u := url(server, int(port), path)
	p.cb.OnFile(cstring(buf))
	h.MemoryGet(buf, u, compressed)
	switch h.status {
	case httpStatusFailed:
		return h.errorType
	case httpStatusDone:
		p.cb.OnFileTick(100)
		p.cb.OnFileFinished(cstring(buf))
		return 0
	}
	p.cb.OnError(h.errorType, str(h.errorText))
	return 100
}

// DownloadFile (0x00585B30) fetches path into the image named after file,
// with the tick and file-tick callbacks as it goes. The URL takes the port
// Initialize was given, not port. The four summed counters are put back to
// where they were before the fetch, plus what it moved.
func (p *Patcher) DownloadFile(server cstr, port int32, path, file cstr, compressed bool, downloadSize, totalSize int64) int {
	if p.images == nil {
		return -1
	}
	h := newHTTPGet(p.images)
	h.listener = p
	if p.cancel {
		return 100
	}
	if path == nil || file == nil {
		p.cb.OnError(60, "Null path requested for download.\n")
		return 0
	}
	u := url(cOrNull(server), p.port, path)
	*file = strings.ReplaceAll(*file, `\`, "/")
	started := downloadSize+totalSize != 0
	if started {
		p.cb.OnFile(*file)
		p.cb.OnSetFileTickCount(100)
	}
	total := p.bytesTotal + p.unzipTotal
	savedDone := p.bytesDone
	savedUnzipDone := p.unzipDone
	h.Get(u, *file, compressed)
	switch h.status {
	case httpStatusFailed:
		msg := fmt.Sprintf("Error downloading %s\n", *file)
		if h.errorText != nil {
			msg = fmt.Sprintf("Error downloading %s: %s\n", *file, *h.errorText)
		}
		p.cb.OnError(h.errorType, msg)
		if h.errorType == 32 {
			p.Cancel()
		}
		p.bytesDone = savedDone
		return h.errorType
	case httpStatusDone:
		if started {
			p.bytesDone = savedDone + int64(h.bytesDown)
			p.unzipDone = savedUnzipDone
			if total < p.bytesDone+p.unzipDone {
				p.cb.OnTick(100)
			} else {
				p.tick()
			}
			p.cb.OnFileTick(100)
			p.cb.OnFileFinished(*file)
		}
		return 0
	}
	// Aborted: the fetch failed.
	if started {
		p.cb.OnTick(0)
		p.cb.OnFileTick(0)
	}
	p.cb.OnError(h.errorType, str(h.errorText))
	return 100
}
