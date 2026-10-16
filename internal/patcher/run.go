package patcher

import (
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"
)

// State is VIWndPatcher's m_state (setup.h), which RunPatcher sets as it
// goes; the strings are WORDS.TXT ids, shown after "Status:" (803).
type State int

const (
	StateCancelled  State = -1 // the window draws nothing of its own
	StateConnecting State = 0  // 757 "Connecting to update server..."
	StateChecking   State = 1  // 754 "Checking for updates..."
	StateFailed     State = 2  // 768 "Error while updating.", and the box 769
	StateUpdating   State = 3  // 812 "Updating" (OnFile, once the manifest is in)
	StateDone       State = 4  // 764 "Done.", and the choices
	StateReading    State = 5  // 792 "Reading game data"
	StateWriting    State = 6  // 814 "Writing game data"
	StateAuthFailed State = 7  // 768, and the box 787: DNAS refused a module
)

// DefaultManifest is data\psvrpath.dat on the retail disc, the path the
// patcher fetches the manifest from (CRLF stripped).
const DefaultManifest = "patch/eqoa/eqoa-frontierslive-update.xml.gz"

// DefaultPort is the patch port SETUP passes Initialize.
const DefaultPort = 7000

// Config is what RunPatcher takes from the console that this client has to
// be told: SETUP's host is "patch.everquestonlineadventures.com:7000", and
// the console's DNS decides which server that is.
type Config struct {
	Host     string // the server's address
	Port     int    // DefaultPort
	Manifest string // data\psvrpath.dat's line, DefaultManifest
	Dir      string // the save folder
	// FrameTime is one of RunPatcher's MainLoop()s, which it runs in counts
	// between the steps (20, 10, 32, ...) so each status can be read. The
	// PS2's frame is two vsyncs; 0 runs straight through.
	FrameTime time.Duration
	// Record, when set, is where a finished patch writes the module's record
	// (check.go): beside the card folder, never in it.
	Record string
}

func (c *Config) defaults() {
	if c.Port == 0 {
		c.Port = DefaultPort
	}
	if c.Manifest == "" {
		c.Manifest = DefaultManifest
	}
}

// Status is the patch window's state, written by Run and read by the screen
// each frame.
type Status struct {
	mu       sync.Mutex
	state    State
	progress float32
	notes    string
	changed  bool
	done     bool
	err      string
}

// Snapshot is a copy of the Status, safe to read.
type Snapshot struct {
	State    State
	Progress float32 // the bar, 0..1
	Notes    string  // the product's release notes, as the server sent them
	Changed  bool    // files were written: the PS2 opens the notes at Done
	Done     bool    // Run has returned
	Err      string  // why it failed, for the log and a report
}

func NewStatus() *Status { return &Status{state: StateCancelled} }

func (s *Status) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Snapshot{s.state, s.progress, s.notes, s.changed, s.done, s.err}
}

func (s *Status) setState(st State) {
	s.mu.Lock()
	s.state = st
	s.mu.Unlock()
}

func (s *Status) setProgress(p float32) {
	s.mu.Lock()
	s.progress = p
	s.mu.Unlock()
}

// setup is VISetup in its role as the patcher's callbacks: OnFile, OnTick
// and OnError are its own (visetup.cpp, 0x00569478 ..), the rest answer 1.
type setup struct {
	st           *Status
	cfg          Config
	initializing bool    // m_x218B0: the manifest is being read; OnFile leaves the state alone
	lastError    string  // the last OnError's text, for Check
	readDone     float32 // m_progress: how much of the card was read
	writeDone    float32 // m_x218AC: how much of it was written back
}

func (s *setup) frames(n int) {
	if s.cfg.FrameTime > 0 {
		time.Sleep(time.Duration(n) * s.cfg.FrameTime)
	}
}

// OnFile (0x00569478): the first file of the download proper turns the
// status to "Updating", for four frames.
func (s *setup) OnFile(name string) int {
	if !s.initializing {
		s.st.setState(StateUpdating)
		s.frames(4)
	}
	return 1
}

// OnTick (0x00569500): the bar. Reading the card is the first 20%, the
// download the next 60% and writing the card the last 20%; n is the
// download's percentage, and 100 adds the write.
func (s *setup) OnTick(n int) int {
	p := s.readDone * 0.2
	if n != 0 {
		p += float32(n) / 100 * 0.6
	}
	if n == 100 {
		p += s.writeDone * 0.2
	}
	s.st.setProgress(min(max(0, p), 1))
	return 1
}

// OnError (0x00569660) only prints: "ERROR CODE %d / '%s'".
func (s *setup) OnError(code int, text string) int {
	log.Printf("patcher: ERROR CODE %d / %q", code, text)
	s.lastError = text
	return 1
}

func (s *setup) OnStart() int                 { return 1 }
func (s *setup) OnProduct(string) int         { return 1 }
func (s *setup) OnFileFinished(string) int    { return 1 }
func (s *setup) OnFinish() int                { return 1 }
func (s *setup) OnSetTickCount(int) int       { return 1 }
func (s *setup) OnSetFileTickCount(int) int   { return 1 }
func (s *setup) OnFileTick(int) int           { return 1 }
func (s *setup) OnStatusInfo(text string) int { return 1 }

// Run is VISetup::RunPatcher's patch half (0x00568A28, from its
// SetState(-1)): the card read into memory, the manifest fetched, every file
// it names brought in line, the deletes marked, the release notes taken, and
// the card written back — with the window's state and bar moving as the
// console's do, frame counts included. DNAS, before it on the PS2, is not
// this client's to run.
//
// It always ends in StateDone or StateFailed, with Status.Done set. On a
// failure nothing is written: the save folder is as it was.
func Run(cfg Config, st *Status) {
	cfg.defaults()
	s := &setup{st: st, cfg: cfg}
	fail := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		log.Printf("patcher: %s", msg)
		st.mu.Lock()
		st.state = StateFailed
		st.err = msg
		st.done = true
		st.mu.Unlock()
	}

	st.setState(StateCancelled)
	s.frames(20)
	st.setState(StateReading)
	s.frames(10)
	log.Printf("patcher: reading patch data (%s)", cfg.Dir)
	images, err := ReadFolder(cfg.Dir, func(f float32) {
		s.readDone = f
		s.OnTick(0)
	})
	if err != nil {
		// The PS2 gives up on the card here and restarts the game.
		fail("reading the save folder: %v", err)
		return
	}
	st.setState(StateConnecting)
	s.frames(32)

	p := New(s)
	p.SetPatchImage(images)
	s.initializing = true
	log.Printf("patcher: Patching with XML: %s", cfg.Manifest)
	ok := p.Initialize(cfg.Host+":"+strconv.Itoa(cfg.Port), cfg.Port, cfg.Manifest)
	s.initializing = false
	if !ok {
		fail("Patcher (Initialize) Failed")
		return
	}
	st.setState(StateChecking)
	s.frames(32)
	if !p.DownloadAll(0) {
		fail("Patcher (DownloadAll) Failed")
		return
	}
	if !p.DeleteOldFiles() {
		fail("Patcher (DeleteOldFiles) Failed")
		return
	}
	notes := p.ProductChanges()
	st.setState(StateWriting)
	s.frames(10)
	changed, err := WriteFolder(cfg.Dir, images, func(f float32) {
		s.writeDone = f
		s.OnTick(100)
	})
	if err != nil {
		fail("writing the save folder: %v", err)
		return
	}
	if cfg.Record != "" {
		if err := writeRecord(cfg, p.ProductName(), images); err != nil {
			log.Printf("patcher: the module's record: %v", err)
		}
	}
	log.Printf("patcher: %s version %d: done (%s)", cfg.Host, p.Version(), changedWord(changed))
	st.mu.Lock()
	st.progress = 1
	st.state = StateDone
	st.notes = notes
	st.changed = changed
	st.done = true
	st.mu.Unlock()
}

func changedWord(changed bool) string {
	if changed {
		return "files changed"
	}
	return "nothing to change"
}
