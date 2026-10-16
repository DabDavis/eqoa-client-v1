package patcher

// distInfo is PatcherDistributionInfo and its _Impl
// (patcherdistributioninfo*.cpp, 0x00586D00 .. 0x005880F0): one
// distribution's attributes, out of the manifest, and the
// DistributionDownloader that does its work.
type distInfo struct {
	name, title, path, server cstr
	port                      int32
	optional                  bool // "Optional"
	restricted                bool // "Restricted"
	restrictedFile            cstr // "RestrictedFile"
	restrictedFileExists      bool
	selected                  bool // what a download takes: not Optional
	api                       *Patcher
	changes                   *changes
	downloader                *distributionDownloader
}

// initialize is _Impl::Initialize (0x00587570): the top level's downloader,
// over the <Product>.
func (in *distInfo) initialize(node *xmlNode, api *Patcher) {
	in.api = api
	in.downloader = newDistributionDownloader()
	in.downloader.setDistributionInfo(in)
	in.setXMLInfo(node)
	if node != nil {
		in.downloader.initialize(node, nil, nil, nil, nil)
		in.downloader.topLevel = true
	}
}

// findPatcherAPI (0x00587640): the patcher lives on the top level; a child
// distribution asks the distribution before it, else its parent.
func (in *distInfo) findPatcherAPI() *Patcher {
	if in.api != nil {
		return in.api
	}
	if prev := in.relative(in.downloader.prev); prev != nil {
		return prev.findPatcherAPI()
	}
	if parent := in.relative(in.downloader.parent); parent != nil {
		return parent.findPatcherAPI()
	}
	return nil
}

// relative is how GetParent/GetPrevious (0x00587770 ..) read the tree: the
// neighbouring downloader's info, if that downloader is a distribution.
func (in *distInfo) relative(d downloader) *distInfo {
	if d != nil && d.base().isDistribution {
		return d.base().info
	}
	return nil
}

func (in *distInfo) checkPatcherAPI() bool {
	in.api = in.findPatcherAPI()
	return in.api != nil
}

// setXMLInfo (0x00587E48). A name, title, path, server or port already set
// is kept; Optional and Restricted are read every time. A Restricted
// distribution is selected only when its RestrictedFile exists — fopen on
// the PS2, with a bare name and no device to look on, which never finds it.
// A child takes Restricted and Optional from its parent when it does not
// set them, after its own selection was decided.
func (in *distInfo) setXMLInfo(node *xmlNode) {
	if in.name == nil {
		in.name = PullXMLAttribute(node, "Name")
	}
	if in.title == nil {
		in.title = PullXMLAttribute(node, "Title")
	}
	if in.path == nil {
		in.path = PullXMLAttribute(node, "RemotePath")
	}
	if in.server == nil {
		in.server = PullXMLAttribute(node, "Server")
	}
	if in.port == 0 {
		in.port = PullXMLAttributeLong(node, "Port")
	}
	in.optional = PullXMLAttributeBool(node, "Optional")
	in.restricted = PullXMLAttributeBool(node, "Restricted")
	if in.restrictedFile == nil {
		in.restrictedFile = PullXMLAttribute(node, "RestrictedFile")
	}
	in.restrictedFileExists = true
	in.selected = !in.optional
	if in.restricted {
		in.restrictedFileExists = false
		in.selected = false
	}
	if in.downloader != nil {
		if parent := in.relative(in.downloader.parent); parent != nil {
			if !in.restricted {
				in.restricted = parent.restricted
				in.restrictedFileExists = parent.restrictedFileExists
			}
			if !in.optional {
				in.optional = parent.optional
			}
		}
	}
}

// download is _Impl::Download (0x00587BD0): 1 when the tree downloaded
// without an error.
func (in *distInfo) download(flags int) bool {
	return in.downloader.download(flags) == 0
}

// deleteOld is _Impl::DeleteOld (0x00587C98).
func (in *distInfo) deleteOld() {
	if in.downloader != nil {
		in.downloader.deleteOld()
	}
}

func (in *distInfo) getTotalSize(flags int) int64 {
	if in.downloader == nil {
		return 0
	}
	return in.downloader.getTotalSize(flags)
}

func (in *distInfo) getDownloadSize(flags int) int64 {
	if in.downloader == nil {
		return 0
	}
	return in.downloader.getDownloadSize(flags)
}

func (in *distInfo) resetScanFlag(flags int) {
	if in.downloader != nil {
		in.downloader.resetScanFlag(flags)
	}
}

func (in *distInfo) calculateHierarchicalProperties() {
	if in.downloader != nil {
		in.downloader.calculateHierarchicalProperties()
	}
}

// The callbacks, guarded: the _Impl's inline members (0x00588130 ..).
func (in *distInfo) callError(code int, msg string) {
	if in.api != nil {
		in.api.cb.OnError(code, msg)
	}
}

func (in *distInfo) callFinish() {
	if in.api != nil {
		in.api.cb.OnFinish()
	}
}

func (in *distInfo) callForceRestart(name string, now bool) {
	if in.api != nil {
		in.api.onForceRestart(name, now)
	}
}

// changes is PatcherChanges and its _Impl (patcherchanges*.cpp,
// 0x00586900 ..): a product's or distribution's release notes, one node per
// <Changes>, kept in Version order. The PS2 keeps the HTML as it came:
// ExtractTextChangesFromHTMLChanges copies it.
type changes struct {
	version    int16
	text, html string
	prev, next *changes
}

// Text is GetTextChanges.
func (c *changes) Text() string {
	if c == nil {
		return ""
	}
	return c.text
}

// insertChanges threads a new node into the list headed by head, as both
// SetChangesFile do (0x00583AF0, 0x0057ECE0), and returns the head. It is the
// PS2's walk, quirks included: the loop does not stop at the first later
// version, so with two or more later ones the node is threaded again before
// each, and only the head pointer is ever made to reach it.
func insertChanges(head *changes, version int16, html string) *changes {
	if head == nil {
		return &changes{version: version, html: html, text: html}
	}
	nc := &changes{}
	done := false
	for c := head; c != nil; c = c.next {
		if version < c.version {
			prev := c.prev
			c.prev = nc
			*nc = changes{version: version, html: html, text: html, prev: prev, next: c}
			if nc.prev == nil {
				head = nc
			}
			done = true
		}
	}
	if !done {
		c := head
		for c.next != nil {
			c = c.next
		}
		c.next = nc
		*nc = changes{version: version, html: html, text: html, prev: c}
	}
	return head
}
