//go:build dev

package main

import (
	"net/http"
	_ "net/http/pprof"
)

func init() {
	// pprof for CPU profiling (http://localhost:6060/debug/pprof/)
	go func() { _ = http.ListenAndServe("localhost:6060", nil) }()
}
