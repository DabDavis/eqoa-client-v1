package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"syscall"
	"time"

	rl "github.com/gen2brain/raylib-go/raylib"


)

// BuildVersion is set at compile time via -ldflags.
// Example: go build -ldflags "-X main.BuildVersion=abc123"
var BuildVersion = "dev"

const (
	updateBaseURL   = "https://texturepacks.art/eqoa"
	manifestURL     = updateBaseURL + "/manifest.json"
	updateTimeout   = 15 * time.Second
	downloadTimeout = 5 * time.Minute
	updateUser      = "eqoa"
	updatePass      = "REDACTED"
)

// updateManifest is the JSON served by the update server.
type updateManifest struct {
	Version   string         `json:"version"`
	LinuxURL  string         `json:"linux_url"`
	LinuxHash string         `json:"linux_sha256"`
	WinURL    string         `json:"windows_url"`
	WinHash   string         `json:"windows_sha256"`
	Changelog string         `json:"changelog"`
	Files     []manifestFile `json:"files,omitempty"`
}

// manifestFile describes an extra file (overlay ESF, etc.) to sync to the assets dir.
type manifestFile struct {
	Path   string `json:"path"`      // relative to assets dir, e.g. "overlays/zone_17.esf"
	URL    string `json:"url"`       // download URL
	SHA256 string `json:"sha256"`    // expected hash
}

// updateState tracks the updater UI state.
type updateState struct {
	status      string  // status text shown on screen
	progress    float32 // 0..1 download progress
	done        bool    // check complete (proceed to title/login)
	err         string  // error message (non-fatal, proceed anyway)
	needRestart bool    // binary was replaced, user should restart
	changelog   string  // changelog from manifest (shown on restart prompt)
}

// verifyISOPath checks that the ISO path is a valid EQOA Frontiers disc image.
// Reads only the ISO header (~1MB) to verify:
//   1. ISO 9660 "CD001" signature at sector 16 with System ID "PLAYSTATION"
//   2. "SLUS_207.44" (EQOA Frontiers game ID) present in the first 1MB
// Returns an error string if invalid, or "" if OK.
func verifyISOPath(isoPath string) string {
	if isoPath == "" {
		return "EQOA Frontiers ISO required"
	}
	info, err := os.Stat(isoPath)
	if err != nil {
		return fmt.Sprintf("ISO not found: %s", isoPath)
	}
	if info.IsDir() {
		return fmt.Sprintf("ISO path is a directory: %s", isoPath)
	}
	if info.Size() < 500_000_000 {
		return fmt.Sprintf("ISO too small (%d MB) — expected EQOA Frontiers disc image", info.Size()/(1024*1024))
	}

	f, err := os.Open(isoPath)
	if err != nil {
		return fmt.Sprintf("Cannot open: %v", err)
	}
	defer f.Close()

	// Check ISO 9660 Primary Volume Descriptor at sector 16 (offset 32768).
	pvd := make([]byte, 2048)
	if _, err := f.ReadAt(pvd, 32768); err != nil {
		return "Cannot read ISO header"
	}
	// Bytes 1-5 must be "CD001".
	if string(pvd[1:6]) != "CD001" {
		return "Not a valid ISO 9660 disc image"
	}
	// System ID (bytes 8-39) should contain "PLAYSTATION".
	sysID := strings.TrimSpace(string(pvd[8:40]))
	if sysID != "PLAYSTATION" {
		return fmt.Sprintf("Not a PlayStation disc (System ID: %s)", sysID)
	}

	// Search first 1MB for EQOA Frontiers game ID "SLUS_207.44".
	header := make([]byte, 1024*1024)
	if _, err := f.ReadAt(header, 0); err != nil {
		return "Cannot read ISO data"
	}
	if !bytes.Contains(header, []byte("SLUS_207.44")) {
		return "Not an EQOA Frontiers disc (SLUS-20744 not found)"
	}

	return ""
}

// runUpdateCheck shows the update check scene. Returns when done.
func (c *Client) runUpdateCheck() {
	state := &updateState{status: "Checking for updates..."}

	// Run check in background
	go checkForUpdate(state, c.assetsDir)

	// Draw update screen until check completes
	for !rl.WindowShouldClose() && !state.done && !state.needRestart {
		rl.BeginDrawing()
		rl.ClearBackground(rl.Color{R: 10, G: 10, B: 20, A: 255})

		if c.titleScr != nil && c.titleScr.loaded {
			c.titleScr.drawTitleScene(c, false)
		}

		// Status text
		c.drawCenteredText(state.status, screenHeight/2-20, 16, colorGoldWarm)

		// Progress bar (during download)
		if state.progress > 0 && state.progress < 1 {
			barW := int32(300)
			barH := int32(12)
			barX := screenWidth/2 - barW/2
			barY := screenHeight/2 + 10
			fillW := int32(float32(barW) * state.progress)

			if c.uiTheme == uiStyleModern {
				rect := rl.Rectangle{X: float32(barX), Y: float32(barY), Width: float32(barW), Height: float32(barH)}
				rl.DrawRectangleRounded(rect, 0.3, 4, rl.Color{R: 30, G: 30, B: 50, A: 200})
				fillRect := rl.Rectangle{X: float32(barX), Y: float32(barY), Width: float32(fillW), Height: float32(barH)}
				rl.DrawRectangleRounded(fillRect, 0.3, 4, rl.Color{R: 212, G: 192, B: 111, A: 220})
			} else {
				rl.DrawRectangle(barX-1, barY-1, barW+2, barH+2, rl.Color{R: 60, G: 60, B: 80, A: 200})
				rl.DrawRectangle(barX, barY, fillW, barH, rl.Color{R: 212, G: 192, B: 111, A: 220})
			}
			pctText := fmt.Sprintf("%d%%", int(state.progress*100))
			c.drawCenteredText(pctText, barY+barH+4, 12, colorDim)
		}

		// Error (non-fatal)
		if state.err != "" {
			c.drawCenteredText(state.err, screenHeight/2+40, 12, rl.Color{R: 255, G: 100, B: 100, A: 200})
		}

		c.drawCenteredText(fmt.Sprintf("v%s", BuildVersion), screenHeight-20, 10, colorDim)

		rl.EndDrawing()
	}

	// Verify ISO path is valid — if missing/invalid, open the ISO prompt.
	if !state.needRestart {
		isoErr := verifyISOPath(c.isoPath)
		if isoErr != "" {
			log.Printf("ISO check: %s — opening ISO prompt", isoErr)
			if path := c.promptForISO(); path != "" {
				c.isoPath = path
				cfg := loadLoginConfig()
				cfg.ISOPath = path
				saveLoginConfig(cfg)
				log.Printf("ISO: %s (saved to config)", path)
			}
			if rl.WindowShouldClose() {
				return
			}
		}
	}

	// Binary was replaced — show changelog then auto-restart
	if state.needRestart {
		// Parse changelog lines, sanitize non-ASCII for font rendering
		var changeLines []string
		if state.changelog != "" {
			sanitized := sanitizeASCII(state.changelog)
			changeLines = strings.Split(sanitized, "\n")
		}

		restartTimer := time.Now()
		const autoRestartDelay = 5 * time.Second

		for !rl.WindowShouldClose() {
			if anyKeyPressed() || rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
				break
			}
			if time.Since(restartTimer) >= autoRestartDelay {
				break
			}
			rl.BeginDrawing()
			rl.ClearBackground(rl.Color{R: 10, G: 10, B: 20, A: 255})
			if c.titleScr != nil && c.titleScr.loaded {
				c.titleScr.drawTitleScene(c, false)
			}

			// Changelog popup box
			boxW := int32(500)
			boxH := int32(60 + len(changeLines)*18 + 50)
			if boxH < 160 {
				boxH = 160
			}
			boxX := screenWidth/2 - boxW/2
			boxY := screenHeight/2 - boxH/2
			c.uiPanel(boxX, boxY, boxW, boxH)

			c.drawCenteredText("Update installed!", boxY+15, 18, colorGoldWarm)

			y := boxY + 45
			if len(changeLines) > 0 {
				for _, line := range changeLines {
					line = strings.TrimSpace(line)
					if line == "" {
						continue
					}
					c.drawCenteredText(line, y, 12, rl.White)
					y += 18
				}
			}

			remaining := autoRestartDelay - time.Since(restartTimer)
			if remaining < 0 {
				remaining = 0
			}
			c.drawCenteredText(fmt.Sprintf("Restarting in %d...", int(remaining.Seconds())+1), y+10, 14, rl.White)
			c.drawCenteredText("Press any key to restart now.", y+32, 12, colorDim)
			rl.EndDrawing()
		}
		// Restart with new binary
		rl.CloseWindow()
		exePath, err := os.Executable()
		if err == nil {
			exePath, _ = filepath.EvalSymlinks(exePath)
		}
		if runtime.GOOS == "windows" {
			// Windows: can't exec-replace, launch new process and exit
			cmd := exec.Command(exePath, os.Args[1:]...)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			cmd.Start()
			os.Exit(0)
		} else {
			// Linux: exec-replace current process with new binary
			syscall.Exec(exePath, os.Args, os.Environ())
		}
		os.Exit(0)
	}
}

// authGet performs an HTTP GET with Basic Auth credentials.
func authGet(httpClient *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(updateUser, updatePass)
	return httpClient.Do(req)
}

func checkForUpdate(state *updateState, assetsDir string) {
	defer func() { state.done = true }()

	// Dev builds skip auto-updater for rapid iteration.
	if gmEnabled() {
		log.Println("Dev build — skipping auto-update check")
		state.status = "Dev build"
		return
	}

	// Fetch manifest
	client := &http.Client{Timeout: updateTimeout}
	resp, err := authGet(client, manifestURL)
	if err != nil {
		log.Printf("Update check failed: %v", err)
		state.status = "Update server unreachable"
		state.err = "Continuing with current version..."
		time.Sleep(1500 * time.Millisecond)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		log.Printf("Update check: HTTP %d", resp.StatusCode)
		state.status = "No updates available"
		time.Sleep(1 * time.Second)
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Update check: read body: %v", err)
		state.status = "Update check failed"
		state.err = "Continuing with current version..."
		time.Sleep(1500 * time.Millisecond)
		return
	}

	var manifest updateManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		log.Printf("Update check: bad manifest: %v", err)
		state.status = "Update check failed"
		state.err = "Continuing with current version..."
		time.Sleep(1500 * time.Millisecond)
		return
	}

	log.Printf("Update: local=%s remote=%s", BuildVersion, manifest.Version)

	// Sync extra files (overlays, etc.) regardless of binary version.
	if len(manifest.Files) > 0 {
		syncExtraFiles(state, manifest.Files, assetsDir)
	}

	// Compare versions
	if manifest.Version == BuildVersion || manifest.Version == "" {
		state.status = "Client is up to date"
		time.Sleep(1 * time.Second)
		return
	}

	// Determine platform-specific URL and hash
	var downloadURL, expectedHash string
	if runtime.GOOS == "windows" {
		downloadURL = manifest.WinURL
		expectedHash = manifest.WinHash
	} else {
		downloadURL = manifest.LinuxURL
		expectedHash = manifest.LinuxHash
	}

	if downloadURL == "" {
		log.Printf("Update: no download URL for %s", runtime.GOOS)
		state.status = "No update for this platform"
		time.Sleep(1500 * time.Millisecond)
		return
	}

	// Download new binary
	state.status = fmt.Sprintf("Downloading update %s...", manifest.Version)
	log.Printf("Update: downloading %s", downloadURL)

	dlClient := &http.Client{Timeout: downloadTimeout}
	dlResp, err := authGet(dlClient, downloadURL)
	if err != nil {
		log.Printf("Update download failed: %v", err)
		state.err = "Download failed, continuing with current version..."
		time.Sleep(2 * time.Second)
		return
	}
	defer dlResp.Body.Close()

	if dlResp.StatusCode != 200 {
		log.Printf("Update download: HTTP %d", dlResp.StatusCode)
		state.err = "Download failed, continuing with current version..."
		time.Sleep(2 * time.Second)
		return
	}

	// Write to temp file next to current binary
	exePath, err := os.Executable()
	if err != nil {
		log.Printf("Update: can't find self: %v", err)
		state.err = "Update failed"
		time.Sleep(2 * time.Second)
		return
	}
	exePath, _ = filepath.EvalSymlinks(exePath)
	exeDir := filepath.Dir(exePath)
	tmpPath := filepath.Join(exeDir, ".eqoa-update.tmp")

	tmpFile, err := os.Create(tmpPath)
	if err != nil {
		log.Printf("Update: can't create temp file: %v", err)
		state.err = "Update failed (write permission?)"
		time.Sleep(2 * time.Second)
		return
	}

	// Download with progress tracking
	totalBytes := dlResp.ContentLength
	hasher := sha256.New()
	writer := io.MultiWriter(tmpFile, hasher)
	var downloaded int64

	buf := make([]byte, 64*1024)
	for {
		n, readErr := dlResp.Body.Read(buf)
		if n > 0 {
			_, writeErr := writer.Write(buf[:n])
			if writeErr != nil {
				tmpFile.Close()
				os.Remove(tmpPath)
				log.Printf("Update: write error: %v", writeErr)
				state.err = "Update failed (disk error)"
				time.Sleep(2 * time.Second)
				return
			}
			downloaded += int64(n)
			if totalBytes > 0 {
				state.progress = float32(downloaded) / float32(totalBytes)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			tmpFile.Close()
			os.Remove(tmpPath)
			log.Printf("Update: download error: %v", readErr)
			state.err = "Download interrupted, continuing with current version..."
			time.Sleep(2 * time.Second)
			return
		}
	}
	tmpFile.Close()

	// Verify hash if provided
	if expectedHash != "" {
		gotHash := hex.EncodeToString(hasher.Sum(nil))
		if gotHash != expectedHash {
			os.Remove(tmpPath)
			log.Printf("Update: hash mismatch: got %s expected %s", gotHash, expectedHash)
			state.err = "Update verification failed"
			time.Sleep(2 * time.Second)
			return
		}
		log.Printf("Update: hash verified OK")
	}

	// Replace binary
	state.status = "Installing update..."
	state.progress = 1

	if runtime.GOOS == "windows" {
		// Windows can't overwrite running exe — rename old, move new in
		oldPath := exePath + ".old"
		os.Remove(oldPath) // clean up previous .old if exists
		if err := os.Rename(exePath, oldPath); err != nil {
			os.Remove(tmpPath)
			log.Printf("Update: can't rename old binary: %v", err)
			state.err = "Update failed (file locked?)"
			time.Sleep(2 * time.Second)
			return
		}
		if err := os.Rename(tmpPath, exePath); err != nil {
			// Try to restore old binary
			os.Rename(oldPath, exePath)
			os.Remove(tmpPath)
			log.Printf("Update: can't place new binary: %v", err)
			state.err = "Update failed"
			time.Sleep(2 * time.Second)
			return
		}
		os.Remove(oldPath)
	} else {
		// Linux: overwrite in place (atomic via rename)
		if err := os.Chmod(tmpPath, 0755); err != nil {
			os.Remove(tmpPath)
			log.Printf("Update: chmod failed: %v", err)
			state.err = "Update failed"
			time.Sleep(2 * time.Second)
			return
		}
		if err := os.Rename(tmpPath, exePath); err != nil {
			os.Remove(tmpPath)
			log.Printf("Update: rename failed: %v", err)
			state.err = "Update failed"
			time.Sleep(2 * time.Second)
			return
		}
	}

	log.Printf("Update: installed %s successfully", manifest.Version)
	state.status = "Update installed!"
	state.changelog = manifest.Changelog
	state.needRestart = true
}

// hashLocalFile computes the SHA-256 of a local file by streaming (no full read into RAM).
// Returns empty string on error (file missing, etc.).
func hashLocalFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// syncExtraFiles downloads any missing or outdated files (core ESFs, overlays, etc.)
// into the assets directory. Skips files whose local SHA-256 already matches.
// Streams large files to disk with progress tracking.
// Also removes local overlay files not present in the manifest (server-driven cleanup).
func syncExtraFiles(state *updateState, files []manifestFile, assetsDir string) {
	// Track which overlay files the manifest declares (for cleanup).
	wantedOverlays := make(map[string]bool)

	for _, f := range files {
		localPath := filepath.Join(assetsDir, f.Path)

		// Track overlay paths for cleanup.
		if strings.HasPrefix(f.Path, "overlays/") {
			wantedOverlays[filepath.Base(f.Path)] = true
		}

		// Check if file already exists with correct hash (streaming, no full RAM load).
		if f.SHA256 != "" {
			localHash := hashLocalFile(localPath)
			if localHash == f.SHA256 {
				log.Printf("Update: file %s up to date", f.Path)
				continue
			}
		}

		// Download the file.
		state.status = fmt.Sprintf("Downloading %s...", f.Path)
		state.progress = 0
		log.Printf("Update: downloading %s", f.Path)

		dlClient := &http.Client{Timeout: downloadTimeout}
		resp, err := authGet(dlClient, f.URL)
		if err != nil {
			log.Printf("Update: download %s failed: %v", f.Path, err)
			continue
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			log.Printf("Update: download %s failed: HTTP %d", f.Path, resp.StatusCode)
			continue
		}

		// Create parent directories.
		if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
			resp.Body.Close()
			log.Printf("Update: mkdir for %s: %v", f.Path, err)
			continue
		}

		// Stream to temp file with progress + hash.
		tmpPath := localPath + ".tmp"
		tmpFile, err := os.Create(tmpPath)
		if err != nil {
			resp.Body.Close()
			log.Printf("Update: create temp for %s: %v", f.Path, err)
			continue
		}

		hasher := sha256.New()
		writer := io.MultiWriter(tmpFile, hasher)
		totalBytes := resp.ContentLength
		var downloaded int64
		buf := make([]byte, 256*1024)
		dlOK := true

		for {
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				if _, writeErr := writer.Write(buf[:n]); writeErr != nil {
					log.Printf("Update: write %s: %v", f.Path, writeErr)
					dlOK = false
					break
				}
				downloaded += int64(n)
				if totalBytes > 0 {
					state.progress = float32(downloaded) / float32(totalBytes)
					// Update status with size info for large files.
					dlMB := float64(downloaded) / (1024 * 1024)
					totalMB := float64(totalBytes) / (1024 * 1024)
					state.status = fmt.Sprintf("Downloading %s... (%.0f/%.0f MB)", f.Path, dlMB, totalMB)
				}
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				log.Printf("Update: download %s: %v", f.Path, readErr)
				dlOK = false
				break
			}
		}
		resp.Body.Close()
		tmpFile.Close()

		if !dlOK {
			os.Remove(tmpPath)
			continue
		}

		// Verify hash.
		if f.SHA256 != "" {
			gotHash := hex.EncodeToString(hasher.Sum(nil))
			if gotHash != f.SHA256 {
				log.Printf("Update: hash mismatch for %s (got %s)", f.Path, gotHash)
				os.Remove(tmpPath)
				continue
			}
		}

		// Atomic rename into place.
		if err := os.Rename(tmpPath, localPath); err != nil {
			log.Printf("Update: rename %s: %v", f.Path, err)
			os.Remove(tmpPath)
			continue
		}
		state.progress = 1
		log.Printf("Update: synced %s (%d bytes)", f.Path, downloaded)
	}

	// Remove local overlay files not in the manifest.
	overlayDir := filepath.Join(assetsDir, "overlays")
	entries, err := os.ReadDir(overlayDir)
	if err != nil {
		return // no overlay dir yet, nothing to clean
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".esf") {
			continue
		}
		if !wantedOverlays[e.Name()] {
			removePath := filepath.Join(overlayDir, e.Name())
			log.Printf("Update: removing stale overlay %s", e.Name())
			os.Remove(removePath)
		}
	}
}

// sanitizeASCII replaces common non-ASCII characters with ASCII equivalents
// so they render correctly with Raylib's default font codepoint range.
func sanitizeASCII(s string) string {
	r := strings.NewReplacer(
		"\u2014", "--", // em dash
		"\u2013", "-",  // en dash
		"\u2018", "'",  // left single quote
		"\u2019", "'",  // right single quote
		"\u201C", "\"", // left double quote
		"\u201D", "\"", // right double quote
		"\u2026", "...", // ellipsis
	)
	result := r.Replace(s)
	// Drop any remaining non-ASCII bytes
	var buf strings.Builder
	for _, c := range result {
		if c >= 32 && c < 127 || c == '\n' || c == '\t' {
			buf.WriteRune(c)
		}
	}
	return buf.String()
}
