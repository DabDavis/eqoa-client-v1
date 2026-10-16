package main

import (
	"os"
	"testing"

	"github.com/eqoa/client/internal/patcher"
)

// data\psvrpath.dat, read through the disc's directories, is the manifest
// path SETUP fetches: patch/eqoa/eqoa-frontierslive-update.xml.gz, CRLF.
func TestPSVRPathOffTheDisc(t *testing.T) {
	const iso = "/home/sdg/claude-eqoa/EverQuest - Online Adventures - Frontiers (USA).iso"
	if _, err := os.Stat(iso); err != nil {
		t.Skip("no ISO")
	}
	data, err := readISOPath(iso, "DATA/PSVRPATH.DAT", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != patcher.DefaultManifest+"\r\n" {
		t.Fatalf("%q", data)
	}
	c := &Client{isoPath: iso}
	if got := c.psvrPath(); got != patcher.DefaultManifest {
		t.Fatalf("psvrPath %q", got)
	}
	if _, err := readISOPath(iso, "DATA/NOSUCH.DAT", 4096); err == nil {
		t.Fatal("found a file that is not there")
	}
}
