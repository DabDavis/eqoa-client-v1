package patcher

import (
	"fmt"
	"log"
	"strings"
)

// downloader is PatcherDownloader's vtable: the calls one node makes on
// another, or on itself through `this`, that a subclass may override. The
// base's own code calls them through self, so an override is what runs.
type downloader interface {
	base() *downloaderBase
	initialize(node *xmlNode, parent, prev downloader, root, path cstr) bool
	scanAsTest() bool
	usingCRC() bool
	setXMLInfo(node *xmlNode)
	getTotalSize(flags int) int64
	getDownloadSize(flags int) int64
	download(flags int) int
	deleteOld()
	resetScanFlag(flags int)
}

// downloaderBase is PatcherDownloader (patcherdownloader.cpp, 0x00588270):
// one node of the tree the manifest becomes. Each node owns its next sibling
// and its first child; a File looks itself up in the card's images by
// localPath.
type downloaderBase struct {
	self downloader

	name            cstr  // +0x00 "Name"
	server          cstr  // +0x04 "Server", else the parent's
	port            int32 // +0x08 "Port", else the parent's
	remotePath      cstr  // +0x0C "RemotePath", made whole by Initialize
	localPath       cstr  // +0x10 "LocalPath", made whole by Initialize
	defaultRoot     cstr  // +0x14 a distribution's "Root"
	root            cstr  // +0x18
	useCRCMode      int   // +0x1C 0 no, 1 yes, 2 as the parent does
	ignoreTimeStamp bool  // +0x20
	installOnly     bool  // +0x24
	scanAsTestFlag  bool  // +0x28 a child is a test file
	special         [2]bool
	xtraFile        bool
	needDownload    int  // +0x38
	deleteFlag      bool // +0x3C "Delete"
	isDistribution  bool // +0x40
	next, prev      downloader
	child, parent   downloader
	info            *distInfo // +0x54
	api             *Patcher  // +0x58
	topLevel        bool      // +0x60
}

func (d *downloaderBase) base() *downloaderBase { return d }

func newBase(self downloader) downloaderBase {
	// The constructor (0x00588270): UseCRC inherits, time stamps are ignored.
	return downloaderBase{self: self, useCRCMode: 2, ignoreTimeStamp: true}
}

// initializeBase is PatcherDownloader::Initialize (0x00588430): the
// attributes every node has. Delete, IgnoreTimeStamp and InstallOnly pass
// down from the parent; a test, beta or xtra file is always checked by CRC.
func (d *downloaderBase) initializeBase(node *xmlNode, parent, prev downloader) {
	d.parent = parent
	d.prev = prev
	d.name = PullXMLAttribute(node, "Name")
	d.localPath = PullXMLAttribute(node, "LocalPath")
	d.remotePath = PullXMLAttribute(node, "RemotePath")
	d.server = PullXMLAttribute(node, "Server")
	d.port = PullXMLAttributeLong(node, "Port")
	d.deleteFlag = PullXMLAttributeBool(node, "Delete")
	d.ignoreTimeStamp = PullXMLAttributeBoolDef(node, "IgnoreTimeStamp", false)
	d.installOnly = PullXMLAttributeBoolDef(node, "InstallOnly", false)
	d.special[0] = PullXMLAttributeBoolDef(node, "TestFile", false)
	d.special[1] = PullXMLAttributeBoolDef(node, "BetaFile", false)
	d.xtraFile = PullXMLAttributeBoolDef(node, "XtraFile", false)
	if d.special[0] || d.special[1] || d.xtraFile {
		d.useCRCMode = 1
	} else {
		d.useCRCMode = PullDefinedXMLAttBool(node, "UseCRC")
	}
	if parent != nil {
		if parent.base().needDelete() {
			d.deleteFlag = true
		}
		if parent.base().ignoreTimeStamp {
			d.ignoreTimeStamp = true
		}
		if parent.base().installOnly {
			d.installOnly = true
		}
	}
	if d.deleteFlag {
		d.needDownload = 0
	}
}

// initializeWithInfo is the six-argument Initialize (0x00588678): the
// distribution's info and its patcher, then the virtual Initialize.
func (d *downloaderBase) initializeWithInfo(node *xmlNode, parent, prev downloader, root, path cstr, info *distInfo) bool {
	d.info = info
	if info != nil {
		d.api = info.api
	}
	return d.self.initialize(node, parent, prev, root, path)
}

// usingCRC (0x005887C8): a quick scan never reads a CRC (and SETUP never
// asks for one); otherwise this node's setting, or its parent's.
func (d *downloaderBase) usingCRC() bool {
	if d.api != nil && d.api.quickScan {
		return false
	}
	if d.useCRCMode == 1 {
		return true
	}
	return d.useCRCMode == 2 && d.parent != nil && d.parent.usingCRC()
}

// calculateHierarchicalProperties (0x00588850): a node needs downloading
// when any child does. It never clears the flag, only sets it.
func (d *downloaderBase) calculateHierarchicalProperties() {
	for c := d.child; c != nil; c = c.base().next {
		c.base().calculateHierarchicalProperties()
		if c.base().needDownloadFor(0x10) != 0 {
			d.needDownload = 1
		}
		if c.scanAsTest() {
			d.scanAsTestFlag = true
		}
	}
	if d.needDelete() && d.isDistribution {
		d.needDownload = 0
	}
}

// needDownloadFor is NeedDownload (0x00588928): flag 0x10 counts everything;
// 4 skips test files, 8 beta files. SETUP passes 0.
func (d *downloaderBase) needDownloadFor(flags int) int {
	if flags&0x10 == 0 {
		if flags&4 != 0 && d.special[0] {
			return 0
		}
		if flags&8 != 0 && d.special[1] {
			return 0
		}
	}
	return d.needDownload
}

func (d *downloaderBase) scanAsTest() bool { return d.scanAsTestFlag }
func (d *downloaderBase) needDelete() bool { return d.deleteFlag }
func (d *downloaderBase) flagDownloaded()  { d.needDownload = 0 }
func (d *downloaderBase) flagDeleted()     { d.deleteFlag = false }

// getServer (0x00588980): this node's Server, else its parent's.
func (d *downloaderBase) getServer() cstr {
	for n := d; ; n = n.parent.base() {
		if n.server != nil || n.parent == nil {
			return n.server
		}
	}
}

// getPort (0x005889A8): this node's Port, else its parent's.
func (d *downloaderBase) getPort() int32 {
	for n := d; ; n = n.parent.base() {
		if n.port != 0 || n.parent == nil {
			return n.port
		}
	}
}

// resetScanFlag (0x00588B70).
func (d *downloaderBase) resetScanFlag(flags int) {
	for c := d.child; c != nil; c = c.base().next {
		c.resetScanFlag(flags)
	}
}

func (d *downloaderBase) setDistributionInfo(info *distInfo) {
	d.info = info
	if info != nil {
		d.api = info.api
	}
}

// link adds child after prev, as both InitializeChildren do.
func (d *downloaderBase) link(child, prev downloader) {
	if d.child == nil {
		d.child = child
	}
	if prev != nil {
		prev.base().next = child
	}
}

// ---- DirectoryDownloader (directorydownloader.cpp, 0x0057D938) ----

// A <Directory>, whose children are directories and files. A PS2 card
// folder has no subdirectories: a file under one looks itself up as
// "dir/name", which the image "name" matches, and is written back flat.
type directoryDownloader struct {
	downloaderBase
	localName cstr // +0x68 "LocalName"
}

func newDirectoryDownloader() *directoryDownloader {
	d := &directoryDownloader{}
	d.downloaderBase = newBase(d)
	return d
}

// initialize (0x0057D9D0): the paths the XML gives relative to the parent,
// made whole, then the children.
func (d *directoryDownloader) initialize(node *xmlNode, parent, prev downloader, root, path cstr) bool {
	d.initializeBase(node, parent, prev)
	d.self.setXMLInfo(node)
	local, remote := d.localPath, d.remotePath
	d.localPath, d.remotePath = nil, nil
	if d.localName != nil {
		d.localPath = dirFullPath(d.localName, local, root)
	} else {
		d.localPath = dirFullPath(d.name, local, root)
		if d.name != nil {
			d.localName = cs(*d.name)
		}
	}
	d.remotePath = dirFullPath(d.name, remote, path)
	d.initializeChildren(node)
	return true
}

// initializeChildren (0x0057DAF0): directories and files only.
func (d *directoryDownloader) initializeChildren(node *xmlNode) {
	var prev downloader
	for n := node.children; n != nil; n = n.next {
		if d.api != nil && d.api.cancel {
			return
		}
		var c downloader
		switch strings.ToLower(n.name) {
		case "directory":
			c = newDirectoryDownloader()
		case "file":
			c = newFileDownloader()
		}
		if c == nil {
			continue
		}
		c.base().initializeWithInfo(n, d, prev, d.localPath, d.remotePath, d.info)
		d.link(c, prev)
		prev = c
	}
}

func (d *directoryDownloader) setXMLInfo(node *xmlNode) {
	d.localName = PullXMLAttribute(node, "LocalName")
}

func (d *directoryDownloader) getTotalSize(flags int) int64 {
	var total int64
	for c := d.child; c != nil; c = c.base().next {
		total += c.getTotalSize(flags)
	}
	return total
}

func (d *directoryDownloader) getDownloadSize(flags int) int64 {
	var total int64
	for c := d.child; c != nil; c = c.base().next {
		total += c.getDownloadSize(flags)
	}
	return total
}

// download (0x0057DDF8). Making the directory is a no-op on the PS2 (its
// mkdir is a stub that claims success), and here.
func (d *directoryDownloader) download(flags int) int {
	if d.needDownloadFor(flags) == 0 {
		return 0
	}
	result := 0
	if !d.needDelete() && d.needDownloadFor(flags) != 0 {
		for c := d.child; c != nil && result != 100; c = c.base().next {
			if r := c.download(flags); r != 0 && result != 100 {
				result = r
			}
		}
		if result == 0 {
			d.flagDownloaded()
		}
	}
	return result
}

// deleteOld is Delete (0x0057DFA8): the children, then the directory, whose
// rmdir is a stub.
func (d *directoryDownloader) deleteOld() {
	for c := d.child; c != nil; c = c.base().next {
		c.deleteOld()
	}
	if d.needDelete() {
		d.flagDeleted()
	}
}

// dirFullPath is DirectoryDownloader::GetFullPath (0x0057E158): a LocalPath
// stands for the whole path, the name is not appended to it.
func dirFullPath(name, path, root cstr) cstr {
	if path != nil {
		switch strings.ToLower(*path) {
		case "::currentdirectory::":
			return copyStr(root)
		case "::homedirectory::", "::windowshome::":
			return nil // windir: getenv is NULL on the PS2
		}
		return cs(*path)
	}
	switch {
	case root != nil && name != nil:
		return cs(*root + "/" + *name)
	case root != nil:
		return cs(*root)
	case name != nil:
		return cs(*name)
	}
	return nil
}

func copyStr(s cstr) cstr {
	if s == nil {
		return nil
	}
	return cs(*s)
}

// ---- FileDeltaDownloader (filedeltadownloader.cpp, 0x0057FE40) ----

// fileDelta is one <DeltaFile> of a <File>: the bytes that would turn the
// copy whose CRC is crc into the current one. The PS2 reads them and keeps
// their sizes, and never downloads one (see fileDownloader.download).
type fileDelta struct {
	server       cstr
	port         int32
	remotePath   cstr
	deltaPath    cstr
	localPath    cstr
	downloadSize int32
	totalSize    int32
	crc          uint32
}

// ---- FileDownloader (filedownloader.cpp, 0x005800A8) ----

// A <File>. On the PS2 the "disk" a file is checked against is the card
// image of that name.
type fileDownloader struct {
	downloaderBase
	discrepancy  bool         // +0x68
	localCRC     uint32       // +0x6C
	deltas       []*fileDelta // +0x70
	delta        *fileDelta   // +0x74 the one whose CRC is the card's
	localName    cstr         // +0x78 "LocalName"
	tempName     cstr         // +0x7C "TempFilename"
	partialPatch bool         // +0x80
	totalSize    int64        // +0x88 "TotalSize"
	downloadSize int64        // +0x90 "DownloadSize"
	crc          uint32       // +0xA0 "CRC"
	compressed   bool         // +0xA4 "Compressed", true when absent
	restart      bool         // +0xA8 "Restart"
	keepBackup   bool         // +0xAC "Keep_Backup"
}

func newFileDownloader() *fileDownloader {
	d := &fileDownloader{compressed: true}
	d.downloaderBase = newBase(d)
	return d
}

// initialize (0x005801B0).
func (d *fileDownloader) initialize(node *xmlNode, parent, prev downloader, root, path cstr) bool {
	d.initializeBase(node, parent, prev)
	d.self.setXMLInfo(node)
	local, remote := d.localPath, d.remotePath
	d.localPath, d.remotePath = nil, nil
	if d.localName != nil {
		d.localPath = fileFullPath(d.localName, local, root)
	} else {
		d.localPath = fileFullPath(d.name, local, root)
		d.localName = copyStr(d.name)
	}
	d.remotePath = fileFullPath(d.name, remote, path)
	deltaName := cs(str(d.localName) + "_delta")
	deltaLocal := fileFullPath(deltaName, local, root)
	for n := node.children; n != nil; n = n.next {
		if strings.EqualFold(n.name, "DeltaFile") {
			file := PullXMLAttribute(n, "FilePath")
			d.addDeltaFile(n, deltaName, deltaLocal, fileFullPath(file, remote, path))
		}
	}
	if d.localPath == nil {
		d.needDownload = 0
	}
	if d.api == nil || d.api.checkOnInitialize {
		d.checkForNeedDownload()
	}
	return true
}

// checkForNeedDownload (0x00580498). A file whose temporary name occurs in
// the command line is the running patcher, which the game patches; on the
// PS2 the command line is "host0:/main.elf", and a temporary name starts with
// '_' unless TempFilename gives one, so that is never true.
func (d *fileDownloader) checkForNeedDownload() bool {
	if d.parent != nil && d.parent.base().needDelete() {
		return false
	}
	if d.localName != nil {
		if d.tempName == nil {
			t := []byte(*d.localName)
			if len(t) > 0 {
				t[0] = '_'
			}
			d.tempName = cs(string(t))
		}
		if strings.Contains(strings.ToLower(commandLine), strings.ToLower(*d.tempName)) {
			d.needDownload = 0
			d.deleteFlag = false
			return true // PatchPatcher: what is left of it compares two strings
		}
	}
	if d.needDelete() {
		return true
	}
	d.needDownload = 0
	if d.fileDiscrepancy() {
		d.needDownload = 1
		d.partialPatch = d.partialPatchCheck()
	}
	return true
}

// commandLine is GetCommandLine and GetCurrentExecutableName on the PS2.
const commandLine = "host0:/main.elf"

// setXMLInfo (0x00580630).
func (d *fileDownloader) setXMLInfo(node *xmlNode) {
	d.localName = PullXMLAttribute(node, "LocalName")
	d.totalSize = int64(PullXMLAttributeLong(node, "TotalSize"))
	d.downloadSize = int64(PullXMLAttributeLong(node, "DownloadSize"))
	d.restart = PullXMLAttributeBool(node, "Restart")
	d.keepBackup = PullXMLAttributeBool(node, "Keep_Backup")
	d.tempName = PullXMLAttribute(node, "TempFilename")
	d.crc = uint32(PullXMLAttributeLong(node, "CRC"))
	d.compressed = PullXMLAttributeBoolDef(node, "Compressed", true)
	// A file the manifest gives no CRC is not checked by one.
	if d.usingCRC() && d.crc == 0 {
		d.useCRCMode = 0
	}
}

// usingCRC (0x00580930): a file that restarts the patcher is always checked
// by CRC.
func (d *fileDownloader) usingCRC() bool {
	if d.restart {
		return true
	}
	return d.downloaderBase.usingCRC()
}

// scanAsTest (0x00581618).
func (d *fileDownloader) scanAsTest() bool {
	if d.special[0] || d.special[1] || d.xtraFile {
		return true
	}
	return d.downloaderBase.scanAsTest()
}

// getTotalSize (0x005807A0): a matching delta's size stands in for the
// file's, though the delta is never what is downloaded.
func (d *fileDownloader) getTotalSize(flags int) int64 {
	if d.needDownloadFor(flags) == 0 {
		return 0
	}
	if d.delta != nil {
		return int64(d.delta.totalSize)
	}
	return d.totalSize
}

// getDownloadSize (0x00580860).
func (d *fileDownloader) getDownloadSize(flags int) int64 {
	if d.needDownloadFor(flags) == 0 {
		return 0
	}
	if d.delta != nil {
		return int64(d.delta.downloadSize)
	}
	return d.downloadSize
}

func (d *fileDownloader) resetScanFlag(flags int) { d.downloaderBase.resetScanFlag(flags) }

// download (0x00580960). The whole file, always: NeedsPartialPatch
// (0x00580D78) returns 0, so a matching delta is never fetched. Whatever the
// fetch reported, the image must then exist and carry the manifest's CRC —
// UseCRC or not — or the file failed: 100 for that, 20 for any other code.
// A Restart file then finishes the patch and asks for a restart, which
// PatcherAPI answers by cancelling (OnForceRestart → Restart → Cancel), so
// every file after it fails.
func (d *fileDownloader) download(flags int) int {
	if d.needDelete() {
		return 0
	}
	if d.needDownloadFor(flags) == 0 {
		return 0
	}
	self := d.localName != nil && strings.EqualFold(commandLine, *d.localName)
	target := d.localPath
	if self {
		target = d.tempName
	}
	result := d.api.DownloadFile(d.getServer(), d.getPort(), d.remotePath, target, d.compressed,
		d.downloadSize, d.totalSize)
	if im := d.api.images.find(str(d.localPath)); im != nil {
		if crc := crc32Bytes(im.Data); crc != d.crc {
			log.Printf("patcher: %s: CRC %d, manifest says %d", str(d.localPath), crc, d.crc)
			result = 100
		}
	} else {
		result = 100
	}
	if result != 0 {
		if result != 100 {
			result = 20
		}
		d.info.callError(result, fmt.Sprintf("Download error for %s\n", str(target)))
		return result
	}
	d.flagDownloaded()
	if d.restart {
		d.info.callFinish()
		if self {
			d.info.callForceRestart(str(d.tempName), true)
		} else {
			d.info.callForceRestart(str(d.localName), false)
		}
	}
	return 0
}

// deleteOld is Delete (0x00580BF0): the image is marked, and VISetup removes
// it from the card when it writes the patch back.
func (d *fileDownloader) deleteOld() {
	if !d.needDelete() {
		return
	}
	if im := d.api.images.find(str(d.localPath)); im != nil {
		im.Deleted = true
	}
	d.flagDeleted()
}

// fileDiscrepancy (0x00580C58): by CRC when the file uses one, else by size —
// a missing image, or one whose size is not TotalSize (a TotalSize of 0
// matches anything). The PC also compared time stamps; on the PS2
// TimeDiscrepancy is always 0.
func (d *fileDownloader) fileDiscrepancy() bool {
	if d.usingCRC() {
		return d.crcDiscrepancy()
	}
	if d.sizeDiscrepancy() {
		d.discrepancy = true
		if len(d.deltas) > 0 {
			d.localCRC = d.getCRC()
		}
		return true
	}
	return false
}

// partialPatchCheck (0x00580CF0): is there a delta from the copy we have?
func (d *fileDownloader) partialPatchCheck() bool {
	if !d.discrepancy || d.localCRC == 0 {
		return false
	}
	for _, dl := range d.deltas {
		if dl.crc == d.localCRC {
			d.delta = dl
			return true
		}
	}
	return false
}

// sizeDiscrepancy (0x00580ED8).
func (d *fileDownloader) sizeDiscrepancy() bool {
	im := d.api.images.find(str(d.localPath))
	if im == nil {
		return true
	}
	if d.totalSize == 0 {
		return false
	}
	return int64(len(im.Data)) != d.totalSize
}

// getCRC is Get_crc (0x00580F90): the image's CRC, as ReadPatchData left it.
func (d *fileDownloader) getCRC() uint32 {
	if im := d.api.images.find(str(d.localPath)); im != nil {
		return im.CRC
	}
	return 0
}

// crcDiscrepancy (0x00580FD8): no CRC in the manifest counts as different.
func (d *fileDownloader) crcDiscrepancy() bool {
	d.discrepancy = true
	d.localCRC = d.getCRC()
	return d.crc == 0 || d.crc != d.localCRC
}

// addDeltaFile (0x00581480).
func (d *fileDownloader) addDeltaFile(node *xmlNode, deltaName, deltaLocal, remotePath cstr) {
	server := PullXMLAttribute(node, "Server")
	if server == nil {
		server = copyStr(d.getServer())
	}
	port := PullXMLAttributeLong(node, "Port")
	if port == 0 {
		port = d.getPort()
	}
	if server == nil {
		return
	}
	d.deltas = append(d.deltas, &fileDelta{
		server: server, port: port, remotePath: remotePath,
		deltaPath: deltaLocal, localPath: deltaName,
		downloadSize: PullXMLAttributeLong(node, "DownloadSize"),
		totalSize:    PullXMLAttributeLong(node, "TotalSize"),
		crc:          uint32(PullXMLAttributeLong(node, "CRC")),
	})
}

// fileFullPath is FileDownloader::GetFullPath (0x005811C0): LocalPath (or
// RemotePath) + "/" + name, else root + "/" + name, else the name. An empty
// RemotePath is still a path: <File RemotePath="" Name="x.gz"> fetches
// "/x.gz".
func fileFullPath(name, path, root cstr) cstr {
	havePath, haveRoot := path != nil, root != nil
	if havePath {
		switch strings.ToLower(*path) {
		case "::homedirectory::", "::desktop::", "::startmenu::", "::windowshome::":
			havePath, haveRoot = false, false
		}
	}
	switch {
	case havePath && name != nil:
		return cs(*path + "/" + *name)
	case havePath:
		return cs(*path)
	case haveRoot && name != nil:
		return cs(*root + "/" + *name)
	case haveRoot:
		return cs(*root)
	case name != nil:
		return cs(*name)
	}
	return nil
}

// ---- DistributionDownloader (distributiondownloader.cpp, 0x0057E308) ----

// A <Distribution> (the <Product> is one too): it may live in an XML file of
// its own on the server, and each walk over it (sizes) runs once until the
// matching resetScanFlag.
type distributionDownloader struct {
	downloaderBase
	scanFlag [5]bool // ScanFiles, GetFileCount, GetDownloadSize, GetTotalSize, checked on initialize
}

func newDistributionDownloader() *distributionDownloader {
	d := &distributionDownloader{}
	d.downloaderBase = newBase(d)
	d.isDistribution = true
	return d
}

// initialize (0x0057E440): a child distribution makes its own info and
// finds the patcher through its parent.
func (d *distributionDownloader) initialize(node *xmlNode, parent, prev downloader, root, path cstr) bool {
	d.initializeBase(node, parent, prev)
	if d.info == nil {
		d.setDistributionInfo(&distInfo{})
		d.info.downloader = d
	}
	if !d.info.checkPatcherAPI() {
		return false
	}
	d.api = d.info.api
	return d.testAndFinalizeDistribution(node)
}

// testAndFinalizeDistribution (0x0057E988). A Restricted distribution keeps
// its children only when its RestrictedFile exists, and on the PS2 no such
// file ever does (see distInfo.setXMLInfo).
func (d *distributionDownloader) testAndFinalizeDistribution(node *xmlNode) bool {
	d.info.setXMLInfo(node)
	d.setXMLInfo(node)
	if node.children == nil && d.remotePath != nil {
		return d.getExternalTree()
	}
	if !d.info.restricted || d.info.restrictedFileExists {
		d.initializeChildren(node)
		if d.api.checkOnInitialize {
			d.scanFlag[4] = true
		}
	}
	return true
}

// getExternalTree (0x0057E558): a distribution with no children and a
// RemotePath is an XML file of its own. When the in-memory fetch fails, the
// PS2 downloads it again into a patch image named ~VPatchTemp<n>.xml and asks
// libxml to parse that NAME off the file system, where it is not, so the
// fallback never yields a tree; it is not ported (nor the temporary image,
// which would otherwise have been written to the card).
func (d *distributionDownloader) getExternalTree() bool {
	mem := d.api.DownloadMemoryFile(0x19000, d.getServer(), d.getPort(), d.remotePath, true)
	if mem == nil {
		d.info.callError(44, fmt.Sprintf("Error accessing external xml file: %s:%d/%s.\n",
			cOrNull(d.getServer()), d.getPort(), cOrNull(d.remotePath)))
		return false
	}
	doc := xmlParseMemory(mem)
	if doc == nil {
		d.info.callError(40, fmt.Sprintf("Error parsing xml file: %s.\n", cOrNull(d.remotePath)))
		return false
	}
	root := doc.root()
	if !strings.EqualFold(root.name, "VerantPatcher") {
		d.info.callError(41, fmt.Sprintf("Document %s of the wrong type, VerantPatcher expected.\n", cOrNull(d.remotePath)))
		return false
	}
	done := true
	for n := root.children; n != nil; n = n.next {
		if !d.externalInitialize(n) {
			done = false
		}
	}
	return done
}

// externalInitialize (0x0057E810): Initialize again, for each node of the
// external file — every node, text included, and each one replaces the
// attributes the last one set.
func (d *distributionDownloader) externalInitialize(node *xmlNode) bool {
	d.name = PullXMLAttribute(node, "Name")
	d.localPath = PullXMLAttribute(node, "LocalPath")
	d.remotePath = PullXMLAttribute(node, "RemotePath")
	d.server = PullXMLAttribute(node, "Server")
	d.port = PullXMLAttributeLong(node, "Port")
	d.useCRCMode = 0
	if PullXMLAttributeBool(node, "UseCRC") {
		d.useCRCMode = 1
	}
	d.deleteFlag = PullXMLAttributeBool(node, "Delete")
	if d.parent != nil {
		if d.parent.base().needDelete() {
			d.deleteFlag = true
		}
		if d.parent.usingCRC() {
			d.useCRCMode = 1
		}
	}
	return d.testAndFinalizeDistribution(node)
}

// initializeChildren (0x0057EA90): a child distribution finds its own info,
// the rest share this one's; a <Changes> is the distribution's release notes.
func (d *distributionDownloader) initializeChildren(node *xmlNode) {
	var prev downloader
	for n := node.children; n != nil; n = n.next {
		var c downloader
		switch strings.ToLower(n.name) {
		case "distribution":
			c = newDistributionDownloader()
		case "directory":
			c = newDirectoryDownloader()
		case "file":
			c = newFileDownloader()
		}
		if c == nil {
			if strings.EqualFold(n.name, "Changes") {
				d.setChangesFile(n)
			}
			continue
		}
		if c.base().isDistribution {
			c.initialize(n, d, prev, nil, nil)
		} else {
			c.base().initializeWithInfo(n, d, prev, nil, nil, d.info)
		}
		d.link(c, prev)
		prev = c
	}
}

// setXMLInfo (0x0057EC70). TestDistribution / BetaDistribution name the
// distributions a download folds in under flags 4 and 8; SETUP passes 0, so
// they are not read.
func (d *distributionDownloader) setXMLInfo(node *xmlNode) {
	d.defaultRoot = PullXMLAttribute(node, "Root")
}

// setChangesFile (0x0057ECE0): the distribution's release notes. SETUP reads
// only the product's (PatcherAPI_Impl::SetChangesFile), but this one is
// fetched too, so a <Changes> under the <Product> is downloaded twice.
func (d *distributionDownloader) setChangesFile(node *xmlNode) bool {
	server := PullXMLAttribute(node, "Server")
	port := PullXMLAttributeLong(node, "Port")
	remote := PullXMLAttribute(node, "RemotePath")
	compressed := PullXMLAttributeBool(node, "Compressed")
	version := int16(PullXMLAttributeLong(node, "Version"))
	if server == nil {
		server = copyStr(d.getServer())
	}
	if port == 0 {
		port = d.getPort()
	}
	if remote == nil || server == nil {
		return false
	}
	html := d.api.DownloadMemoryFile(0x1F400, server, port, remote, compressed)
	if html == nil {
		return false
	}
	d.info.changes = insertChanges(d.info.changes, version, cstring(html))
	return true
}

// getTotalSize (0x0057F028): the selected distribution's own files, and its
// child distributions. The first non-distribution child starts a walk over
// it and every sibling after it, distributions included.
func (d *distributionDownloader) getTotalSize(flags int) int64 {
	if d.scanFlag[3] {
		return 0
	}
	d.scanFlag[3] = true
	return d.sumChildren(func(c downloader) int64 { return c.getTotalSize(flags) })
}

// getDownloadSize (0x0057F1D8).
func (d *distributionDownloader) getDownloadSize(flags int) int64 {
	if d.scanFlag[2] {
		return 0
	}
	d.scanFlag[2] = true
	return d.sumChildren(func(c downloader) int64 { return c.getDownloadSize(flags) })
}

func (d *distributionDownloader) sumChildren(size func(downloader) int64) int64 {
	var total int64
	c := d.child
	for c != nil {
		if !c.base().isDistribution {
			if d.info.selected {
				for e := c; e != nil; e = e.base().next {
					total += size(e)
				}
				c = nil
			} else {
				c = c.base().next
			}
		} else {
			total += size(c)
			c = c.base().next
		}
	}
	return total
}

// resetScanFlag (0x0057F388).
func (d *distributionDownloader) resetScanFlag(flags int) {
	d.downloaderBase.resetScanFlag(flags)
	if flags < 5 {
		d.scanFlag[flags] = false
	}
}

// download (0x0057F5B8): 100 from a child stops the walk. The inner walk
// over a selected distribution's files starts again at each sibling, so a
// file that failed with anything but 100 is tried once per file after it.
func (d *distributionDownloader) download(flags int) int {
	result := 0
	for c := d.child; c != nil && result != 100; c = c.base().next {
		if !c.base().isDistribution {
			if d.info.selected {
				for e := c; e != nil && result != 100; e = e.base().next {
					if r := e.download(flags); r != 0 && result != 100 {
						result = r
					}
				}
				if result == 0 {
					d.flagDownloaded()
				}
			}
		} else if r := c.download(flags); r != 0 && result != 100 {
			result = r
		}
	}
	return result
}

// deleteOld is Delete (0x0057FA00).
func (d *distributionDownloader) deleteOld() {
	for c := d.child; c != nil; c = c.base().next {
		c.deleteOld()
	}
}

// cOrNull is how newlib's printf shows a NULL %s.
func cOrNull(s cstr) string {
	if s == nil {
		return "(null)"
	}
	return *s
}
