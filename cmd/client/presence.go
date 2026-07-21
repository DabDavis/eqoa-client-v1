package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	rl "github.com/gen2brain/raylib-go/raylib"
)

const (
	presenceURL      = updateBaseURL + "/presence/heartbeat"
	presenceInterval = 30 * time.Second
)

// presenceState tracks online testers and notifications.
type presenceState struct {
	mu       sync.Mutex
	online   []string        // currently online usernames
	known    map[string]bool // users we've already seen (no re-notify)
	notifs   []presenceNotif // active notifications to display
	running  bool
}

type presenceNotif struct {
	text    string
	created time.Time
}

const notifDuration = 8 * time.Second

var presence = &presenceState{known: make(map[string]bool)}

// startPresence begins the heartbeat loop. Call after login.
func startPresence(username string) {
	presence.mu.Lock()
	if presence.running {
		presence.mu.Unlock()
		return
	}
	presence.running = true
	// Mark self as known so we don't notify about ourselves
	presence.known[username] = true
	presence.mu.Unlock()

	go presenceLoop(username)
}

func presenceLoop(username string) {
	// Immediate first heartbeat
	presencePing(username)

	ticker := time.NewTicker(presenceInterval)
	defer ticker.Stop()
	for range ticker.C {
		presencePing(username)
	}
}

func presencePing(username string) {
	client := &http.Client{Timeout: 10 * time.Second}
	url := fmt.Sprintf("%s?name=%s", presenceURL, username)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return
	}
	setUpdateAuth(req)

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Presence: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return
	}

	var online []string
	if err := json.NewDecoder(resp.Body).Decode(&online); err != nil {
		return
	}

	presence.mu.Lock()
	defer presence.mu.Unlock()

	// Detect new arrivals
	now := time.Now()
	onlineSet := make(map[string]bool, len(online))
	for _, name := range online {
		onlineSet[name] = true
		if !presence.known[name] {
			presence.known[name] = true
			presence.notifs = append(presence.notifs, presenceNotif{
				text:    fmt.Sprintf("%s is online", name),
				created: now,
			})
			log.Printf("Presence: %s came online", name)
		}
	}

	// Detect departures — remove from known so they re-notify if they return
	for name := range presence.known {
		if name == username {
			continue
		}
		if !onlineSet[name] {
			delete(presence.known, name)
			presence.notifs = append(presence.notifs, presenceNotif{
				text:    fmt.Sprintf("%s went offline", name),
				created: now,
			})
			log.Printf("Presence: %s went offline", name)
		}
	}

	sort.Strings(online)
	presence.online = online

	// Prune expired notifications
	pruned := presence.notifs[:0]
	for _, n := range presence.notifs {
		if now.Sub(n.created) < notifDuration {
			pruned = append(pruned, n)
		}
	}
	presence.notifs = pruned
}

// drawPresenceNotifs renders active notifications top-center with fade out.
func (c *Client) drawPresenceNotifs() {
	presence.mu.Lock()
	notifs := make([]presenceNotif, len(presence.notifs))
	copy(notifs, presence.notifs)
	online := make([]string, len(presence.online))
	copy(online, presence.online)
	presence.mu.Unlock()

	if len(notifs) == 0 && len(online) == 0 {
		return
	}

	// Bottom-left "testers online" with hover tooltip
	if len(online) > 0 {
		txt := fmt.Sprintf("%d tester(s) online", len(online))
		sz := int32(12)
		tw := c.measureText(txt, sz)
		tx := int32(8)
		ty := screenHeight - 18
		pad := int32(4)

		mx := int32(rl.GetMouseX())
		my := int32(rl.GetMouseY())
		hovered := mx >= tx-pad && mx <= tx+tw+pad && my >= ty-pad && my <= ty+sz+pad

		// Label
		labelColor := colorDim
		if hovered {
			labelColor = rl.Color{R: 100, G: 255, B: 150, A: 220}
		}
		c.drawText(txt, tx, ty, sz, labelColor)

		// Tooltip with names on hover
		if hovered && len(online) > 0 {
			tooltipSz := int32(12)
			tooltipW := int32(0)
			for _, name := range online {
				w := c.measureText(name, tooltipSz)
				if w > tooltipW {
					tooltipW = w
				}
			}
			tooltipPad := int32(6)
			tooltipH := int32(len(online))*(tooltipSz+2) + tooltipPad*2
			tooltipX := tx
			tooltipY := ty - tooltipH - 4

			rl.DrawRectangle(tooltipX-tooltipPad, tooltipY-tooltipPad,
				tooltipW+tooltipPad*2+tooltipPad, tooltipH+tooltipPad,
				rl.Color{R: 0, G: 0, B: 0, A: 200})
			ny := tooltipY
			for _, name := range online {
				c.drawText(name, tooltipX, ny, tooltipSz,
					rl.Color{R: 100, G: 255, B: 150, A: 240})
				ny += tooltipSz + 2
			}
		}
	}
}
