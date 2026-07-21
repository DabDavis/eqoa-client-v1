package terrain

import (
	"runtime"
	"testing"
	"time"
)

// startIdleWorker wires up a Loader's load worker the same way EnsureZoneLoaded
// does, but without enqueuing any zone load (which would require a real ESF).
// This lets us exercise the Close/worker lifecycle in isolation.
func startIdleWorker(l *Loader) {
	l.loadQueue = make(chan loadRequest, 32)
	l.workerStopped = make(chan struct{})
	go l.loadWorker(l.loadQueue, l.workerStopped)
}

// TestLoaderCloseStopsWorker verifies Close stops the load worker and returns
// (does not hang), and that a second Close is a safe no-op.
func TestLoaderCloseStopsWorker(t *testing.T) {
	l := &Loader{}
	startIdleWorker(l)
	stopped := l.workerStopped

	done := make(chan struct{})
	go func() { l.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close hung — worker was not stopped")
	}

	// Worker signalled exit.
	select {
	case <-stopped:
	default:
		t.Fatal("worker did not close its stopped channel")
	}

	if !l.closed {
		t.Fatal("closed flag not set after Close")
	}
	// Idempotent.
	l.Close()
}

// TestLoaderCloseNoWorker verifies Close is safe when no worker was ever
// started and when there is no underlying file, and that EnsureZoneLoaded is a
// no-op after Close (does not resurrect the worker).
func TestLoaderCloseNoWorker(t *testing.T) {
	l := &Loader{}
	l.Close() // no worker, no file — must not hang or panic
	l.Close() // idempotent

	l.EnsureZoneLoaded(0) // after Close: must be ignored
	if l.loadQueue != nil {
		t.Fatal("EnsureZoneLoaded started a worker after Close")
	}
}

// TestLoaderCloseNoGoroutineLeak verifies Close does not leak the worker
// goroutine — the core of the #5 fix (each world switch previously leaked one).
func TestLoaderCloseNoGoroutineLeak(t *testing.T) {
	before := runtime.NumGoroutine()

	l := &Loader{}
	startIdleWorker(l)
	l.Close()

	// Close waits for the worker's range loop to end, but the goroutine may take
	// a scheduler tick to fully retire — poll briefly.
	var after int
	for i := 0; i < 200; i++ {
		after = runtime.NumGoroutine()
		if after <= before {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("worker goroutine leaked: before=%d after=%d", before, after)
}
