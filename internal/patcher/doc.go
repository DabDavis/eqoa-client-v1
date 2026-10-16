// Package patcher is SETUP's Verant patcher, ported from the matching decomp
// (decomp/modules/setup/src, 454 of its 462 functions byte-exact), with the
// memory-card half of VISetup::RunPatcher working on a save folder instead of
// a card.
//
// The PS2 runs the patcher to completion on one thread: Patcher_thread is
// called, never started, and Http_get::Get_thread returns before Get does.
// This port is the same synchronous code; the client runs it off the render
// goroutine and reads its state through the Callbacks.
//
// Where the files map:
//
//	api.go         PatcherAPI_Impl (patcherapi_impl.cpp), PatcherAPI's virtuals
//	downloader.go  PatcherDownloader and the Directory/Distribution/File/
//	               FileDelta downloaders (patcherdownloader.cpp and four more)
//	distinfo.go    PatcherDistributionInfo(_Impl), PatcherChanges(_Impl)
//	httpget.go     Http_get (http_get.cpp), the gzip-header skip and inflate
//	vurl.go        VUrl (vurl.cpp), the HTTP/1.1 client every download ends in
//	pullxml.go     PullXMLAttribute* (pullxml.cpp)
//	xml.go         the slice of libxml2 2.3.6 the patcher walks
//	image.go       VIPatchImage, the card's files in memory
//	card.go        VISetup::ReadPatchData / WritePatchData, over a folder
//	run.go         VISetup::RunPatcher's patch half, and its progress (OnTick)
//
// What is not ported, and why, is said where the code would have been: the
// Win32 leftovers the PS2 stubs out (chdir, mkdir, getcwd, time() is 0, so no
// estimate is ever made), the operations nothing in SETUP calls (rescan,
// UseCRC, uninstall), and the delta download, which FileDownloader can never
// take (NeedsPartialPatch returns 0).
package patcher
