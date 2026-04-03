//go:build dev

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/eqoa/client/internal/game"
	"github.com/eqoa/client/internal/mathutil"
	"github.com/eqoa/client/internal/terrain"
)

// GM menu categories (PS2 VIWndGame command groups).
const (
	gmCatTop       = 0 // top-level category list
	gmCatTeleport  = 1 // Goto Location, Goto Zone, Goto Player, bookmarks
	gmCatPlayer    = 2 // Fast Run, Display Pos, Toggle Invis, Enter Level, Noclip
	gmCatEntity    = 3 // Change Sprite, Revert Sprite, Goto Target
	gmCatDebug     = 4 // Toggle Stats, Collision Debug, Extrapolate/Interpolate
	gmCatZoneList  = 5 // zone list sub-view (from Goto Zone)
	gmCatEntList   = 6 // entity list sub-view (from Goto Player/Entity)
	gmCatBookmarks = 7 // saved teleport locations list
	gmCatWorldMap  = 8 // world map zone list (from eqoa.live/map grid)
)

// GM input actions — what the text input is for.
const (
	gmActionNone       = 0
	gmActionCoord      = 1 // XYZ coordinate entry
	gmActionLevel      = 2 // enter level number
	gmActionSprite     = 3 // enter sprite/model ID
	gmActionPlayerName = 4 // enter player name
)

// gmMenuEntry is a clickable row in the GM menu.
type gmMenuEntry struct {
	Label  string
	Action func(c *Client) // executed on click
	Status func(c *Client) string // optional right-side status text
}

// gmSavedLocation is a bookmarked teleport destination (persisted to JSON).
type gmSavedLocation struct {
	Name string  `json:"name"`
	X    float32 `json:"x"`
	Y    float32 `json:"y"`
	Z    float32 `json:"z"`
}

// gmSavedLocations is the in-memory list of saved teleport destinations.
var gmSavedLocations []gmSavedLocation

func gmLocationsPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, "eqoa", "gm_locations.json")
}

func loadGMLocations() {
	data, err := os.ReadFile(gmLocationsPath())
	if err != nil {
		return
	}
	json.Unmarshal(data, &gmSavedLocations)
}

func saveGMLocations() {
	p := gmLocationsPath()
	os.MkdirAll(filepath.Dir(p), 0700)
	data, err := json.MarshalIndent(gmSavedLocations, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(p, data, 0600)
}

func gmAddLocation(name string, x, y, z float32) {
	gmSavedLocations = append(gmSavedLocations, gmSavedLocation{Name: name, X: x, Y: y, Z: z})
	saveGMLocations()
	log.Printf("GM: Saved location '%s' at (%.1f, %.1f, %.1f)", name, x, y, z)
}

// gmWorldMapZones — zone centers from eqoa.live/map/ grid data.
// World coords: X = col*2000+1000, Z = row*2000+1000, Y = 0 (ground level).
// Sorted alphabetically for easy browsing.
var gmWorldMapZones = []gmSavedLocation{
	{"Al Farak Ruins", 23000, 0, 21000},
	{"Al Karad Ruins", 11000, 0, 15000},
	{"Alseop's Wall", 13000, 0, 17000},
	{"Ant Colonies", 21000, 0, 29000},
	{"Anu Village", 9000, 0, 5000},
	{"Aviak Village", 9000, 0, 21000},
	{"Baga Village", 15000, 0, 11000},
	{"Bandit Hills", 15000, 0, 15000},
	{"Basher's Enclave", 23000, 0, 31000},
	{"Bastable Village", 19000, 0, 15000},
	{"Bear Cave", 9000, 0, 17000},
	{"Blackburrow", 13000, 0, 13000},
	{"Blakedown", 11000, 0, 17000},
	{"Bobble-by-Water", 25000, 0, 11000},
	{"Bogman Village", 5000, 0, 9000},
	{"Box Canyons", 21000, 0, 21000},
	{"Brog Fens", 15000, 0, 23000},
	{"Brokenskull Rock", 21000, 0, 31000},
	{"Burial Mounds", 19000, 0, 29000},
	{"Castle Felstar", 21000, 0, 9000},
	{"Cazic-Thule", 15000, 0, 31000},
	{"Centaur Valley", 15000, 0, 21000},
	{"Chiktar Hive", 19000, 0, 23000},
	{"Collonridge Cemetary", 23000, 0, 9000},
	{"Crethley Manor", 7000, 0, 15000},
	{"Deathfist Citadel", 21000, 0, 19000},
	{"Deathfist Forge", 21000, 0, 17000},
	{"Deathfist Horde", 19000, 0, 21000},
	{"Desert Hate", 19000, 0, 19000},
	{"Dinbak", 17000, 0, 31000},
	{"Diren Village", 13000, 0, 9000},
	{"Druid's Watch", 7000, 0, 19000},
	{"Dshinn's Redoubt", 17000, 0, 19000},
	{"Elephant Graveyard", 15000, 0, 27000},
	{"Elemental Towers", 25000, 0, 25000},
	{"Eternal Desert", 21000, 0, 23000},
	{"Fayspire Gate", 19000, 0, 5000},
	{"Fayspires, Tethelin", 19000, 0, 7000},
	{"Ferran's Hope", 19000, 0, 17000},
	{"Fog Marsh", 9000, 0, 15000},
	{"Forkwatch", 13000, 0, 19000},
	{"Fort Alliance", 15000, 0, 25000},
	{"Freeport", 25000, 0, 15000},
	{"Freezeblood Village", 13000, 0, 7000},
	{"Frosteye Valley", 11000, 0, 5000},
	{"Gerntar Mines", 13000, 0, 27000},
	{"Goldfeather Eyrie", 15000, 0, 7000},
	{"Gramash Ruins", 11000, 0, 9000},
	{"Great Waste", 23000, 0, 25000},
	{"Greyvax's Caves", 17000, 0, 5000},
	{"Grobb", 25000, 0, 31000},
	{"Guardian Forest", 11000, 0, 7000},
	{"Guk", 25000, 0, 29000},
	{"Hagley", 7000, 0, 17000},
	{"Halas", 13000, 0, 5000},
	{"Hazinak", 25000, 0, 27000},
	{"Highbourne", 5000, 0, 21000},
	{"Highpass Hold", 17000, 0, 15000},
	{"Hodstock and Temby", 25000, 0, 13000},
	{"Isle of Dread", 25000, 0, 3000},
	{"Jared's Blight", 13000, 0, 15000},
	{"Jethro's Cast", 7000, 0, 13000},
	{"Kara Village", 21000, 0, 7000},
	{"Kelinar", 13000, 0, 25000},
	{"Kerplunk Outpost", 15000, 0, 29000},
	{"Klick'Anon", 23000, 0, 7000},
	{"Lake Noregard", 17000, 0, 29000},
	{"Mariel Village", 5000, 0, 11000},
	{"Mayfly Glade", 11000, 0, 19000},
	{"Merry-by-Water", 15000, 0, 13000},
	{"Mila's Reef", 13000, 0, 31000},
	{"Misty Thicket", 17000, 0, 11000},
	{"Moradhim", 15000, 0, 9000},
	{"Moss Mouth Cavern", 19000, 0, 13000},
	{"Mt. Hatespike", 13000, 0, 11000},
	{"Mu Lin's Reach", 23000, 0, 13000},
	{"Muniels's Tea Garden", 23000, 0, 19000},
	{"Murnf", 9000, 0, 11000},
	{"Neriak, Nektulos", 25000, 0, 9000},
	{"North Kithicor", 21000, 0, 11000},
	{"North Wilderlands", 9000, 0, 7000},
	{"Northern Ro", 25000, 0, 17000},
	{"Northwestern Ro", 23000, 0, 17000},
	{"Oasis", 21000, 0, 25000},
	{"Oggok Gate", 13000, 0, 29000},
	{"Permafrost", 5000, 0, 5000},
	{"Qeynos", 5000, 0, 17000},
	{"Qeynos Prison", 5000, 0, 19000},
	{"Rivervale", 19000, 0, 11000},
	{"Rogue Clockworks", 25000, 0, 7000},
	{"Runnyeye", 17000, 0, 13000},
	{"Saerk Towers", 21000, 0, 13000},
	{"Safitzer's House", 17000, 0, 7000},
	{"Salisearaneen", 9000, 0, 9000},
	{"Salt Mine", 15000, 0, 19000},
	{"Sea of Lions", 21000, 0, 27000},
	{"Serpent Hills", 17000, 0, 23000},
	{"Shon-To Monastery", 17000, 0, 9000},
	{"Slithtar Hive", 23000, 0, 27000},
	{"Snowblind Plains", 7000, 0, 5000},
	{"Snowfist", 15000, 0, 5000},
	{"South Crossroads", 13000, 0, 21000},
	{"Spider Mine", 9000, 0, 19000},
	{"Spirit Talker's Wood", 11000, 0, 11000},
	{"Sslathis", 23000, 0, 29000},
	{"Stoneclaw", 7000, 0, 21000},
	{"Stone Watchers", 19000, 0, 31000},
	{"Strag's Rest", 15000, 0, 17000},
	{"Surefall Glade", 9000, 0, 13000},
	{"Sycamore Joy's Rest", 23000, 0, 23000},
	{"Tak 'Xiv", 19000, 0, 25000},
	{"Tak 'Xiz", 17000, 0, 25000},
	{"Tak 'Xiz South", 17000, 0, 27000},
	{"Takish'Hiz", 19000, 0, 27000},
	{"Temple of Light", 23000, 0, 15000},
	{"The Green Rift", 23000, 0, 11000},
	{"Thedruk", 19000, 0, 9000},
	{"Tomb of Kings", 21000, 0, 15000},
	{"Trail's End", 17000, 0, 17000},
	{"Twisted Tower", 7000, 0, 11000},
	{"Unkempt Glade", 7000, 0, 9000},
	{"Unkempt North", 7000, 0, 7000},
	{"Urglunt's Wall", 11000, 0, 21000},
	{"Whale Hill", 5000, 0, 15000},
	{"Widow's Peak", 13000, 0, 23000},
	{"Wktaan's 4th Talon", 17000, 0, 21000},
	{"Wymondham", 11000, 0, 13000},
	{"Wyndhaven", 5000, 0, 13000},
	{"Zentar's Keep", 5000, 0, 7000},
}

func gmDeleteLocation(idx int) {
	if idx < 0 || idx >= len(gmSavedLocations) {
		return
	}
	log.Printf("GM: Deleted location '%s'", gmSavedLocations[idx].Name)
	gmSavedLocations = append(gmSavedLocations[:idx], gmSavedLocations[idx+1:]...)
	saveGMLocations()
}

// teleportTo moves the player to the given world coordinates.
// Sandstorm workaround: set position in 0x40 state channel, flush to server,
// then disconnect + auto-reconnect. On reconnect Sandstorm loads the player
// at the last known position (server-side EE offsets +0x54/+0x58/+0x5C/+0x60).
// Once Sandstorm implements proper GM teleport (opcode 0x00B2), this can be
// replaced with the coaching teleport flow (EnableChannel + fresh 0x40).
func (c *Client) teleportTo(x, y, z float32) {
	// Try to snap Y to ground level if terrain is loaded for target area.
	// If terrain not available (cross-zone), use safe fallback height.
	const safeHeight float32 = 100 // safe spawn height above ground plane
	const groundPad float32 = 5    // small offset above ground to avoid clipping
	groundFound := false
	if c.terrainLoader != nil && c.terrainZoneIdx >= 0 {
		if zt := c.terrainLoader.GetZoneTerrain(c.terrainZoneIdx); zt != nil {
			if groundY, ok := zt.SampleHeight(x, z, y+safeHeight); ok {
				y = groundY + groundPad
				groundFound = true
				log.Printf("GM TELEPORT: ground sampled at Y=%.1f", y)
			}
		}
	}
	if !groundFound && y < safeHeight {
		y = safeHeight
		log.Printf("GM TELEPORT: no terrain data, using safe height Y=%.1f", y)
	}

	// PS2 GM teleport flow:
	// 1. Disconnect immediately (no heartbeats — server rate-limits 0x40 position jumps)
	// 2. Reconnect — world entry 0x40 carries destination position (server accepts
	//    world entry as-is for a fresh session, no rate-limiting)
	// 3. State channels repopulate with entities at the new location

	p := &c.engine.State.Player
	log.Printf("GM TELEPORT: (%.1f, %.1f, %.1f) → (%.1f, %.1f, %.1f)", p.X, p.Y, p.Z, x, y, z)

	// Persist destination so it survives a full client restart
	c.saveCharAt(x, y, z, p.Facing)

	// Save reconnect state — position override applied pre-Tick so world entry
	// trigger carries the correct destination coords.
	c.gmReconnectChar = c.engine.State.SelectedChar
	c.gmReconnectX = x
	c.gmReconnectY = y
	c.gmReconnectZ = z
	c.gmReconnectReady = false

	// Disconnect immediately
	c.engine.SendDisconnect()
	if c.engine.State.Conn != nil {
		c.engine.State.Conn.Close()
	}
	c.engine = nil
	c.gmReconnect = true

	log.Printf("GM TELEPORT: disconnected, reconnecting with destination in world entry")
}

// gmTopEntries returns the top-level category entries.
func gmTopEntries() []gmMenuEntry {
	return []gmMenuEntry{
		{"Teleport", func(c *Client) { c.gmCategory = gmCatTeleport; c.gmMenuScroll = 0 }, nil},
		{"Player State", func(c *Client) { c.gmCategory = gmCatPlayer; c.gmMenuScroll = 0 }, nil},
		{"Entity", func(c *Client) { c.gmCategory = gmCatEntity; c.gmMenuScroll = 0 }, nil},
		{"Debug", func(c *Client) { c.gmCategory = gmCatDebug; c.gmMenuScroll = 0 }, nil},
		{"Grid Runner", func(c *Client) {
			c.gridRunnerStart()
			c.gmMenuOpen = false
		}, func(c *Client) string {
			if c.gridRunner != nil && c.gridRunner.active {
				return c.gridRunnerStatus()
			}
			return "OFF"
		}},
	}
}

// gmTeleportEntries returns the teleport sub-menu entries.
func gmTeleportEntries() []gmMenuEntry {
	return []gmMenuEntry{
		{"Goto Location (XYZ)", func(c *Client) {
			c.gmInputMode = true
			c.gmInputAction = gmActionCoord
			c.gmInputField = 0
			p := c.engine.State.Player
			c.gmInputVals = [3]float32{p.X, p.Y, p.Z}
			c.gmInputBuf = fmt.Sprintf("%.1f", p.X)
		}, nil},
		{"Goto Zone", func(c *Client) {
			c.gmCategory = gmCatZoneList
			c.gmMenuScroll = 0
		}, nil},
		{"Goto Player", func(c *Client) {
			c.gmCategory = gmCatEntList
			c.gmMenuScroll = 0
		}, nil},
		{"Goto Target", func(c *Client) {
			if c.targetObjectID == 0 {
				return
			}
			if ent, ok := c.engine.State.Entities[c.targetObjectID]; ok {
				c.teleportTo(ent.X, ent.Y, ent.Z)
				c.gmMenuOpen = false
			}
		}, func(c *Client) string {
			if c.targetObjectID == 0 {
				return "(no target)"
			}
			return ""
		}},
		{"Saved Locations", func(c *Client) {
			c.gmCategory = gmCatBookmarks
			c.gmMenuScroll = 0
		}, func(c *Client) string {
			return fmt.Sprintf("%d", len(gmSavedLocations))
		}},
		{"World Map", func(c *Client) {
			c.gmCategory = gmCatWorldMap
			c.gmMenuScroll = 0
		}, func(c *Client) string {
			return fmt.Sprintf("%d zones", len(gmWorldMapZones))
		}},
	}
}

// gmPlayerEntries returns the player state sub-menu entries.
func gmPlayerEntries() []gmMenuEntry {
	return []gmMenuEntry{
		{"Fast Run", func(c *Client) {
			c.gmFastRun = !c.gmFastRun
			if c.gmFastRun {
				log.Printf("GM: Fast Run ON (4x speed)")
			} else {
				log.Printf("GM: Fast Run OFF")
			}
		}, func(c *Client) string {
			if c.gmFastRun {
				return "ON"
			}
			return "OFF"
		}},
		{"DISPLAY POS", func(c *Client) {
			c.gmDisplayPos = !c.gmDisplayPos
			log.Printf("GM: Display Pos %v", c.gmDisplayPos)
		}, func(c *Client) string {
			if c.gmDisplayPos {
				return "ON"
			}
			return "OFF"
		}},
		{"Toggle Invis", func(c *Client) {
			c.gmInvis = !c.gmInvis
			log.Printf("GM: Invis %v", c.gmInvis)
		}, func(c *Client) string {
			if c.gmInvis {
				return "ON"
			}
			return "OFF"
		}},
		{"Noclip (Fly)", func(c *Client) {
			c.gmNoClip = !c.gmNoClip
			if c.gmNoClip {
				c.onGround = false
				// Clear VIFlyCamera velocity on entry
				c.flyVelX, c.flyVelY, c.flyVelZ = 0, 0, 0
				c.flyForceX, c.flyForceY, c.flyForceZ = 0, 0, 0
				log.Printf("GM: Noclip ON — fly with arrows, Space=up, Shift=down")
			} else {
				log.Printf("GM: Noclip OFF")
			}
		}, func(c *Client) string {
			if c.gmNoClip {
				return "ON"
			}
			return "OFF"
		}},
		{"Enter Level", func(c *Client) {
			c.gmInputMode = true
			c.gmInputAction = gmActionLevel
			c.gmInputBuf = fmt.Sprintf("%d", c.engine.State.Player.Level)
		}, func(c *Client) string {
			return fmt.Sprintf("Lv %d", c.engine.State.Player.Level)
		}},
	}
}

// gmEntityEntries returns the entity sub-menu entries.
func gmEntityEntries() []gmMenuEntry {
	return []gmMenuEntry{
		{"Change Sprite", func(c *Client) {
			c.gmInputMode = true
			c.gmInputAction = gmActionSprite
			if c.debugModelID != 0 {
				c.gmInputBuf = fmt.Sprintf("0x%X", c.debugModelID)
			} else {
				c.gmInputBuf = ""
			}
		}, func(c *Client) string {
			if c.debugModelID != 0 {
				return fmt.Sprintf("0x%X", c.debugModelID)
			}
			return "(normal)"
		}},
		{"Revert Sprite", func(c *Client) {
			c.debugModelID = 0
			log.Printf("GM: Sprite reverted to normal")
		}, nil},
		{"Goto Target", func(c *Client) {
			if c.targetObjectID == 0 {
				return
			}
			if ent, ok := c.engine.State.Entities[c.targetObjectID]; ok {
				c.teleportTo(ent.X, ent.Y, ent.Z)
				c.gmMenuOpen = false
			}
		}, func(c *Client) string {
			if c.targetObjectID == 0 {
				return "(no target)"
			}
			if ent, ok := c.engine.State.Entities[c.targetObjectID]; ok {
				return ent.Name
			}
			return fmt.Sprintf("#%d", c.targetObjectID)
		}},
	}
}

// gmDebugEntries returns the debug sub-menu entries.
func gmDebugEntries() []gmMenuEntry {
	return []gmMenuEntry{
		{"Toggle Stats", func(c *Client) {
			c.gmShowStats = !c.gmShowStats
			log.Printf("GM: Stats display %v", c.gmShowStats)
		}, func(c *Client) string {
			if c.gmShowStats {
				return "ON"
			}
			return "OFF"
		}},
		{"Collision Debug", func(c *Client) {
			// Uses the existing terrain.CollDebug toggle
			if c.terrainLoader != nil {
				// Toggle via package-level var
				toggleCollDebug()
			}
		}, func(c *Client) string {
			if getCollDebug() {
				return "ON"
			}
			return "OFF"
		}},
		{"Extrapolate/Interpolate", func(c *Client) {
			c.gmExtrapolate = !c.gmExtrapolate
			mode := "Interpolate"
			if c.gmExtrapolate {
				mode = "Extrapolate"
			}
			log.Printf("GM: Entity movement mode: %s", mode)
		}, func(c *Client) string {
			if c.gmExtrapolate {
				return "Extrap"
			}
			return "Interp"
		}},
		{"Effect Volumes Info", func(c *Client) {
			if c.effectVolumes != nil {
				log.Printf("GM: Effect volumes: %d loaded", c.effectVolumes.VolumeCount())
			} else {
				log.Printf("GM: No effect volume system")
			}
		}, func(c *Client) string {
			if c.effectVolumes != nil {
				return fmt.Sprintf("%d vols", c.effectVolumes.VolumeCount())
			}
			return "N/A"
		}},
		{"Cycle Weather", func(c *Client) {
			if c.weather != nil {
				names := []string{"Clear", "Rain", "Snow", "Sleet", "Sandstorm", "Lavastorm", "Thunder"}
				next := (int(c.weather.Type) + 1) % 7
				c.weather.SetWeather(game.WeatherType(next), true)
				log.Printf("GM: Weather → %s", names[next])
			}
		}, func(c *Client) string {
			if c.weather == nil {
				return "N/A"
			}
			names := []string{"Clear", "Rain", "Snow", "Sleet", "Sand", "Lava", "Thunder"}
			return names[int(c.weather.Type)%7]
		}},
		{"Clear Entities", func(c *Client) {
			gs := c.engine.State
			count := len(gs.Entities)
			gs.Entities = make(map[uint32]*game.Entity)
			log.Printf("GM: Cleared %d entities — server will re-populate", count)
		}, func(c *Client) string {
			return fmt.Sprintf("%d", len(c.engine.State.Entities))
		}},
		{"Force Reconnect", func(c *Client) {
			// Return to login and auto-reconnect — fresh MemoryDump + entity channels
			log.Printf("GM: Force reconnect — returning to login for fresh session")
			c.gmMenuOpen = false
			c.disconnectAndReturnToLogin()
		}, nil},
		{"Reload Zone", func(c *Client) {
			if c.terrainLoader != nil {
				// Unload all multi-zone slots
				for idx := range c.loadedZoneIdxs {
					c.terrainRender.ResetZone(idx)
					c.terrainLoader.UnloadZone(idx)
				}
				c.loadedZoneIdxs = make(map[int]bool)
				c.terrainLoader.ResetForReload()
				c.terrainZoneIdx = -1
				c.terrainStarted = false
				c.zoneParticlesSpawned = false
				log.Printf("GM: Zone reload triggered")
			}
		}, nil},
	}
}

// handleGMMenuInput processes input while the GM menu is open.
func (c *Client) handleGMMenuInput() {
	// Escape: back one level or close
	if rl.IsKeyPressed(rl.KeyEscape) {
		if c.gmInputMode {
			c.gmInputMode = false
			c.gmInputAction = gmActionNone
		} else if c.gmCategory != gmCatTop {
			// Go back to parent category
			switch c.gmCategory {
			case gmCatZoneList, gmCatEntList, gmCatBookmarks, gmCatWorldMap:
				c.gmCategory = gmCatTeleport
			default:
				c.gmCategory = gmCatTop
			}
			c.gmMenuScroll = 0
		} else {
			c.gmMenuOpen = false
		}
		return
	}

	// Text input modes
	if c.gmInputMode {
		switch c.gmInputAction {
		case gmActionCoord:
			c.handleGMCoordInput()
		case gmActionLevel:
			c.handleGMSingleInput("Enter Level:", func(val string) {
				if lvl, err := strconv.Atoi(strings.TrimSpace(val)); err == nil && lvl > 0 && lvl <= 60 {
					c.engine.State.Player.Level = byte(lvl)
					log.Printf("GM: Level set to %d", lvl)
				}
				c.gmInputMode = false
				c.gmInputAction = gmActionNone
			})
		case gmActionSprite:
			c.handleGMSingleInput("Enter Sprite ID (hex or decimal):", func(val string) {
				val = strings.TrimSpace(val)
				var id int64
				var err error
				if strings.HasPrefix(val, "0x") || strings.HasPrefix(val, "0X") {
					id, err = strconv.ParseInt(val[2:], 16, 64)
				} else {
					id, err = strconv.ParseInt(val, 10, 64)
				}
				if err == nil {
					c.debugModelID = int32(id)
					log.Printf("GM: Sprite changed to 0x%X", c.debugModelID)
				}
				c.gmInputMode = false
				c.gmInputAction = gmActionNone
			})
		case gmActionPlayerName:
			c.handleGMSingleInput("Enter Player Name:", func(val string) {
				name := strings.TrimSpace(val)
				if name != "" {
					c.gmGotoPlayerByName(name)
				}
				c.gmInputMode = false
				c.gmInputAction = gmActionNone
			})
		default:
			c.gmInputMode = false
			c.gmInputAction = gmActionNone
		}
		return
	}

	// Zone list sub-view
	if c.gmCategory == gmCatZoneList {
		c.handleGMZoneList()
		return
	}

	// Entity list sub-view
	if c.gmCategory == gmCatEntList {
		c.handleGMEntityList()
		return
	}

	// Bookmark list sub-view
	if c.gmCategory == gmCatBookmarks {
		c.handleGMBookmarkList()
		return
	}

	// World map sub-view
	if c.gmCategory == gmCatWorldMap {
		c.handleGMWorldMapList()
		return
	}

	// Get entries for current category
	entries := c.gmGetEntries()
	if len(entries) == 0 {
		return
	}

	// Scroll with mouse wheel
	wheel := rl.GetMouseWheelMove()
	if wheel != 0 {
		c.gmMenuScroll -= int(wheel * 3)
		c.gmClampScroll(len(entries))
	}

	// Number keys 1-9 for quick select
	for i := 0; i < 9; i++ {
		if rl.IsKeyPressed(int32(rl.KeyOne + i)) {
			idx := c.gmMenuScroll + i
			if idx < len(entries) {
				entries[idx].Action(c)
				return
			}
		}
	}
}

// gmGetEntries returns the entry list for the current category.
func (c *Client) gmGetEntries() []gmMenuEntry {
	switch c.gmCategory {
	case gmCatTop:
		return gmTopEntries()
	case gmCatTeleport:
		return gmTeleportEntries()
	case gmCatPlayer:
		return gmPlayerEntries()
	case gmCatEntity:
		return gmEntityEntries()
	case gmCatDebug:
		return gmDebugEntries()
	default:
		return nil
	}
}

// gmClampScroll ensures scroll is within bounds for the given item count.
func (c *Client) gmClampScroll(count int) {
	maxScroll := count - 10
	if maxScroll < 0 {
		maxScroll = 0
	}
	if c.gmMenuScroll < 0 {
		c.gmMenuScroll = 0
	}
	if c.gmMenuScroll > maxScroll {
		c.gmMenuScroll = maxScroll
	}
}

// gmCategoryName returns the display name for a category.
func gmCategoryName(cat int) string {
	switch cat {
	case gmCatTop:
		return "GM Menu"
	case gmCatTeleport:
		return "Teleport"
	case gmCatPlayer:
		return "Player State"
	case gmCatEntity:
		return "Entity"
	case gmCatDebug:
		return "Debug"
	case gmCatZoneList:
		return "Goto Zone"
	case gmCatEntList:
		return "Goto Player/Entity"
	case gmCatBookmarks:
		return "Saved Locations"
	case gmCatWorldMap:
		return "World Map"
	default:
		return "GM Menu"
	}
}

// gmGotoPlayerByName teleports to a player or entity by name.
func (c *Client) gmGotoPlayerByName(name string) {
	nameLower := strings.ToLower(name)
	for _, ent := range c.engine.State.Entities {
		if strings.ToLower(ent.Name) == nameLower {
			c.teleportTo(ent.X, ent.Y, ent.Z)
			c.gmMenuOpen = false
			log.Printf("GM: Teleported to %s at (%.1f, %.1f, %.1f)", ent.Name, ent.X, ent.Y, ent.Z)
			return
		}
	}
	log.Printf("GM: Player/entity '%s' not found in visible range", name)
}

// handleGMZoneList handles the zone list sub-view.
func (c *Client) handleGMZoneList() {
	// This is handled in draw; input is scroll + click + number keys
	if c.terrainLoader == nil {
		return
	}
	proxies := c.terrainLoader.GetZoneProxies()
	if proxies == nil {
		return
	}
	count := len(proxies.Zones)

	wheel := rl.GetMouseWheelMove()
	if wheel != 0 {
		c.gmMenuScroll -= int(wheel * 3)
		c.gmClampScroll(count)
	}

	// Number keys 1-9 for quick teleport
	for i := 0; i < 9; i++ {
		if rl.IsKeyPressed(int32(rl.KeyOne + i)) {
			idx := c.gmMenuScroll + i
			if idx < count {
				z := &proxies.Zones[idx]
				c.teleportTo(z.Center.X, z.Center.Y+10, z.Center.Z)
				c.gmMenuOpen = false
				return
			}
		}
	}
}

// handleGMBookmarkList handles the saved locations bookmark list.
func (c *Client) handleGMBookmarkList() {
	count := len(gmSavedLocations)

	wheel := rl.GetMouseWheelMove()
	if wheel != 0 {
		c.gmMenuScroll -= int(wheel * 3)
		c.gmClampScroll(count)
	}

	// Number keys 1-9 teleport to saved location
	for i := 0; i < 9; i++ {
		if rl.IsKeyPressed(int32(rl.KeyOne + i)) {
			idx := c.gmMenuScroll + i
			if idx < count {
				loc := &gmSavedLocations[idx]
				c.teleportTo(loc.X, loc.Y, loc.Z)
				c.gmMenuOpen = false
				return
			}
		}
	}

	// Delete key: delete hovered item
	if rl.IsKeyPressed(rl.KeyDelete) || rl.IsKeyPressed(rl.KeyX) {
		idx := c.gmBookmarkHoveredIdx()
		if idx >= 0 {
			gmDeleteLocation(idx)
			if c.gmMenuScroll > 0 && c.gmMenuScroll >= len(gmSavedLocations) {
				c.gmMenuScroll = len(gmSavedLocations) - 1
				if c.gmMenuScroll < 0 {
					c.gmMenuScroll = 0
				}
			}
		}
	}
}

// gmBookmarkHoveredIdx returns the index of the hovered bookmark row, or -1.
func (c *Client) gmBookmarkHoveredIdx() int {
	sw := float32(rl.GetScreenWidth())
	sh := float32(rl.GetScreenHeight())
	menuW := float32(360)
	menuH := float32(440)
	menuX := (sw - menuW) / 2
	menuY := (sh - menuH) / 2
	sepY := int32(menuY) + 58
	listY := sepY + 20
	rowH := int32(22)

	mx := rl.GetMouseX()
	my := rl.GetMouseY()
	if mx < int32(menuX)+8 || mx >= int32(menuX+menuW)-8 {
		return -1
	}
	if my < listY {
		return -1
	}
	idx := c.gmMenuScroll + int(my-listY)/int(rowH)
	if idx >= len(gmSavedLocations) {
		return -1
	}
	return idx
}

// handleGMEntityList handles the entity list sub-view.
func (c *Client) handleGMEntityList() {
	entities := c.gmSortedEntities()
	count := len(entities)

	wheel := rl.GetMouseWheelMove()
	if wheel != 0 {
		c.gmMenuScroll -= int(wheel * 3)
		c.gmClampScroll(count)
	}

	// Number keys 1-9 for quick teleport
	for i := 0; i < 9; i++ {
		if rl.IsKeyPressed(int32(rl.KeyOne + i)) {
			idx := c.gmMenuScroll + i
			if idx < count {
				ent := entities[idx]
				c.teleportTo(ent.X, ent.Y, ent.Z)
				c.gmMenuOpen = false
				return
			}
		}
	}

	// S key: save hovered entity's location as a bookmark
	if rl.IsKeyPressed(rl.KeyS) {
		idx := c.gmEntityHoveredIdx(entities)
		if idx >= 0 {
			ent := entities[idx]
			gmAddLocation(ent.Name, ent.X, ent.Y, ent.Z)
		}
	}

	// P key: enter player name for search
	if rl.IsKeyPressed(rl.KeyP) {
		c.gmInputMode = true
		c.gmInputAction = gmActionPlayerName
		c.gmInputBuf = ""
	}
}

// gmEntityHoveredIdx returns the index of the hovered entity row, or -1.
func (c *Client) gmEntityHoveredIdx(entities []*gmEntityInfo) int {
	sw := float32(rl.GetScreenWidth())
	sh := float32(rl.GetScreenHeight())
	menuW := float32(360)
	menuH := float32(440)
	menuX := (sw - menuW) / 2
	menuY := (sh - menuH) / 2
	sepY := int32(menuY) + 58
	listY := sepY + 20
	rowH := int32(20)

	mx := rl.GetMouseX()
	my := rl.GetMouseY()
	if mx < int32(menuX)+8 || mx >= int32(menuX+menuW)-8 {
		return -1
	}
	if my < listY {
		return -1
	}
	idx := c.gmMenuScroll + int(my-listY)/int(rowH)
	if idx >= len(entities) {
		return -1
	}
	return idx
}

// gmSortedEntities returns entities sorted by distance.
func (c *Client) gmSortedEntities() []*gmEntityInfo {
	p := c.engine.State.Player
	var result []*gmEntityInfo
	for _, ent := range c.engine.State.Entities {
		dist := mathutil.Dist2D(ent.X, ent.Z, p.X, p.Z)
		result = append(result, &gmEntityInfo{
			Name:     ent.Name,
			ObjectID: ent.ObjectID,
			X:        ent.X,
			Y:        ent.Y,
			Z:        ent.Z,
			Dist:     dist,
			IsPlayer: ent.IsPlayer,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Dist < result[j].Dist
	})
	return result
}

type gmEntityInfo struct {
	Name     string
	ObjectID uint32
	X, Y, Z  float32
	Dist     float32
	IsPlayer bool
}

// gmSaveCurrentField parses the current input buffer and saves it.
func (c *Client) gmSaveCurrentField() {
	buf := strings.TrimSpace(c.gmInputBuf)
	if v, err := strconv.ParseFloat(buf, 32); err == nil {
		c.gmInputVals[c.gmInputField] = float32(v)
	}
}

// handleGMCoordInput processes keyboard input for custom coordinate entry.
func (c *Client) handleGMCoordInput() {
	// Enter confirms current field, advances to next, teleports on Z
	if rl.IsKeyPressed(rl.KeyEnter) {
		c.gmSaveCurrentField()
		switch c.gmInputField {
		case 0:
			c.gmInputField = 1
			c.gmInputBuf = fmt.Sprintf("%.1f", c.gmInputVals[1])
		case 1:
			c.gmInputField = 2
			c.gmInputBuf = fmt.Sprintf("%.1f", c.gmInputVals[2])
		case 2:
			// All 3 fields entered — teleport
			c.teleportTo(c.gmInputVals[0], c.gmInputVals[1], c.gmInputVals[2])
			c.gmInputMode = false
			c.gmInputAction = gmActionNone
			c.gmMenuOpen = false
		}
		return
	}

	// Tab moves to next field (saves current first)
	if rl.IsKeyPressed(rl.KeyTab) {
		c.gmSaveCurrentField()
		c.gmInputField = (c.gmInputField + 1) % 3
		c.gmInputBuf = fmt.Sprintf("%.1f", c.gmInputVals[c.gmInputField])
		return
	}

	c.gmHandleTextInput()
}

// handleGMSingleInput handles a single-field text input with a callback on Enter.
func (c *Client) handleGMSingleInput(prompt string, onSubmit func(string)) {
	_ = prompt // used only in draw

	if rl.IsKeyPressed(rl.KeyEnter) {
		onSubmit(c.gmInputBuf)
		return
	}

	c.gmHandleTextInput()
}

// gmHandleTextInput handles common text editing (backspace, char input).
func (c *Client) gmHandleTextInput() {
	// Backspace
	if rl.IsKeyPressed(rl.KeyBackspace) || rl.IsKeyPressedRepeat(rl.KeyBackspace) {
		if len(c.gmInputBuf) > 0 {
			c.gmInputBuf = c.gmInputBuf[:len(c.gmInputBuf)-1]
		}
		return
	}

	// Collect typed characters
	for {
		ch := rl.GetCharPressed()
		if ch == 0 {
			break
		}
		r := rune(ch)
		// Allow alphanumeric, minus, period, space, comma, hex chars
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			r == '-' || r == '.' || r == ' ' || r == ',' || r == 'x' || r == 'X' {
			c.gmInputBuf += string(r)
		}
	}
}

// toggleCollDebug toggles the terrain collision debug flag.
func toggleCollDebug() {
	terrain.CollDebug = !terrain.CollDebug
	log.Printf("GM: Collision Debug %v", terrain.CollDebug)
}

// getCollDebug returns the current collision debug state.
func getCollDebug() bool {
	return terrain.CollDebug
}

// drawGMMenu renders the GM menu overlay.
func (c *Client) drawGMMenu() {
	if !c.gmMenuOpen {
		return
	}

	sw := float32(rl.GetScreenWidth())
	sh := float32(rl.GetScreenHeight())

	// Menu dimensions
	menuW := float32(360)
	menuH := float32(440)
	menuX := (sw - menuW) / 2
	menuY := (sh - menuH) / 2

	// Background
	rl.DrawRectangle(int32(menuX), int32(menuY), int32(menuW), int32(menuH),
		rl.Color{R: 10, G: 10, B: 25, A: 230})
	rl.DrawRectangleLines(int32(menuX), int32(menuY), int32(menuW), int32(menuH),
		colorGoldenBdr)

	// Title with category breadcrumb
	title := gmCategoryName(c.gmCategory) + "  (F11)"
	c.drawTextInRect(title, int32(menuX), int32(menuY)+6, int32(menuW), 16, colorGoldBright)

	// Current position
	p := c.engine.State.Player
	posStr := fmt.Sprintf("Pos: (%.1f, %.1f, %.1f)  Facing: %.0f°",
		p.X, p.Y, p.Z, float64(p.Facing)*180/math.Pi)
	c.drawText(posStr, int32(menuX)+10, int32(menuY)+28, 11,
		rl.Color{R: 180, G: 180, B: 180, A: 255})

	// Current zone info
	zoneName := fmt.Sprintf("Zone: %d", c.terrainZoneIdx)
	if c.terrainLoader != nil {
		proxies := c.terrainLoader.GetZoneProxies()
		if proxies != nil && c.terrainZoneIdx >= 0 && c.terrainZoneIdx < len(proxies.Zones) {
			z := &proxies.Zones[c.terrainZoneIdx]
			if z.Name != "" {
				zoneName = fmt.Sprintf("Zone %d: %s", c.terrainZoneIdx, z.Name)
			}
		}
	}
	c.drawText(zoneName, int32(menuX)+10, int32(menuY)+42, 11,
		colorDim)

	// Separator
	sepY := int32(menuY) + 58
	rl.DrawLine(int32(menuX)+8, sepY, int32(menuX+menuW)-8, sepY,
		colorBtnBorder)

	// Custom input modes
	if c.gmInputMode {
		switch c.gmInputAction {
		case gmActionCoord:
			c.drawGMCoordInput(menuX, menuY, menuW)
		default:
			c.drawGMSingleInput(menuX, menuY, menuW)
		}
		return
	}

	// Zone list sub-view
	if c.gmCategory == gmCatZoneList {
		c.drawGMZoneList(menuX, menuY, menuW, menuH, sepY)
		return
	}

	// Entity list sub-view
	if c.gmCategory == gmCatEntList {
		c.drawGMEntityList(menuX, menuY, menuW, menuH, sepY)
		return
	}

	// Bookmark list sub-view
	if c.gmCategory == gmCatBookmarks {
		c.drawGMBookmarkList(menuX, menuY, menuW, menuH, sepY)
		return
	}

	// World map sub-view
	if c.gmCategory == gmCatWorldMap {
		c.drawGMWorldMapList(menuX, menuY, menuW, menuH, sepY)
		return
	}

	// Instructions
	backHint := ""
	if c.gmCategory != gmCatTop {
		backHint = "  [Esc] Back"
	}
	c.drawText("[1-9] Select"+backHint,
		int32(menuX)+10, sepY+4, 10,
		colorGmLabel)

	// Entry list
	listY := sepY + 20
	rowH := int32(24)
	mx := rl.GetMouseX()
	my := rl.GetMouseY()
	clicked := rl.IsMouseButtonPressed(rl.MouseButtonLeft)

	entries := c.gmGetEntries()
	maxVisible := int((menuH - float32(listY-int32(menuY)) - 10) / float32(rowH))
	endIdx := c.gmMenuScroll + maxVisible
	if endIdx > len(entries) {
		endIdx = len(entries)
	}

	for i := c.gmMenuScroll; i < endIdx; i++ {
		entry := &entries[i]
		ry := listY + int32(i-c.gmMenuScroll)*rowH

		displayIdx := i - c.gmMenuScroll
		keyStr := ""
		if displayIdx < 9 {
			keyStr = fmt.Sprintf("%d)", displayIdx+1)
		} else {
			keyStr = "  "
		}

		// Hover highlight
		hover := mx >= int32(menuX)+8 && mx < int32(menuX+menuW)-8 &&
			my >= ry && my < ry+rowH
		if hover {
			rl.DrawRectangle(int32(menuX)+8, ry, int32(menuW)-16, rowH,
				colorGmHighlight)
		}

		// Click
		if hover && clicked {
			entry.Action(c)
			return
		}

		// Key number
		c.drawText(keyStr, int32(menuX)+12, ry+4, 12,
			colorGoldSemi)

		// Label
		labelColor := colorWhiteFull
		if c.gmCategory == gmCatTop {
			// Category headers get a slightly different color
			labelColor = rl.Color{R: 180, G: 200, B: 255, A: 255}
		}
		c.drawText(entry.Label, int32(menuX)+38, ry+4, 13, labelColor)

		// Status text (right-aligned)
		if entry.Status != nil {
			status := entry.Status(c)
			if status != "" {
				statusW := c.measureText(status, 11)
				statusColor := rl.Color{R: 140, G: 180, B: 140, A: 220}
				if status == "ON" {
					statusColor = rl.Color{R: 100, G: 255, B: 100, A: 255}
				} else if status == "OFF" {
					statusColor = rl.Color{R: 180, G: 140, B: 140, A: 200}
				}
				c.drawText(status, int32(menuX+menuW)-statusW-16, ry+5, 11, statusColor)
			}
		}

		// Arrow indicator for sub-menu categories
		if c.gmCategory == gmCatTop {
			c.drawText(">", int32(menuX+menuW)-20, ry+4, 13,
				rl.Color{R: 120, G: 120, B: 140, A: 180})
		}
	}
}

// drawGMCoordInput draws the custom coordinate entry UI.
func (c *Client) drawGMCoordInput(menuX, menuY, menuW float32) {
	baseY := int32(menuY) + 70

	c.drawText("Goto Location — Enter coordinates:", int32(menuX)+10, baseY, 12,
		colorLightGray)

	fields := [3]string{"X:", "Y:", "Z:"}

	var parsedVals [3]string
	for i := range fields {
		parsedVals[i] = fmt.Sprintf("%.1f", c.gmInputVals[i])
	}

	for i, label := range fields {
		fy := baseY + int32(i+1)*28 + 8

		active := c.gmInputField == i
		labelColor := rl.Color{R: 160, G: 160, B: 160, A: 255}
		if active {
			labelColor = colorGoldBright
		}

		c.drawText(label, int32(menuX)+20, fy, 14, labelColor)

		// Input box
		boxX := int32(menuX) + 45
		boxW := int32(menuW) - 70
		boxH := int32(22)
		boxColor := colorFieldBg
		borderColor := rl.Color{R: 60, G: 60, B: 80, A: 200}
		if active {
			borderColor = colorGoldenBdr
		}
		rl.DrawRectangle(boxX, fy-2, boxW, boxH, boxColor)
		rl.DrawRectangleLines(boxX, fy-2, boxW, boxH, borderColor)

		if active {
			cursor := "_"
			if int(rl.GetTime()*2)%2 == 0 {
				cursor = ""
			}
			c.drawText(c.gmInputBuf+cursor, boxX+4, fy+1, 13, rl.White)
		} else {
			c.drawText(parsedVals[i], boxX+4, fy+1, 13,
				rl.Color{R: 180, G: 180, B: 180, A: 200})
		}
	}

	// Instructions
	instrY := baseY + 4*28 + 20
	c.drawText("[Enter] Next / Teleport  [Tab] Switch  [Esc] Cancel",
		int32(menuX)+10, instrY, 10,
		colorGmLabel)

	// "Teleport" button
	goY := instrY + 22
	goW := int32(70)
	goH := int32(24)
	goX := int32(menuX) + (int32(menuW)-goW)/2

	mx := rl.GetMouseX()
	my := rl.GetMouseY()
	goHover := mx >= goX && mx < goX+goW && my >= int32(goY) && my < int32(goY)+goH

	bgColor := rl.Color{R: 40, G: 60, B: 40, A: 220}
	if goHover {
		bgColor = rl.Color{R: 60, G: 100, B: 60, A: 240}
	}
	rl.DrawRectangle(goX, int32(goY), goW, goH, bgColor)
	rl.DrawRectangleLines(goX, int32(goY), goW, goH, rl.Color{R: 100, G: 180, B: 100, A: 220})
	goLabel := "Teleport"
	goLabelW := c.measureText(goLabel, 12)
	c.drawText(goLabel, goX+(goW-goLabelW)/2, int32(goY)+5, 12,
		rl.Color{R: 200, G: 255, B: 200, A: 255})

	if goHover && rl.IsMouseButtonPressed(rl.MouseButtonLeft) {
		c.gmSaveCurrentField()
		c.teleportTo(c.gmInputVals[0], c.gmInputVals[1], c.gmInputVals[2])
		c.gmInputMode = false
		c.gmInputAction = gmActionNone
		c.gmMenuOpen = false
	}
}

// drawGMSingleInput draws a single-field text input overlay.
func (c *Client) drawGMSingleInput(menuX, menuY, menuW float32) {
	baseY := int32(menuY) + 70

	// Prompt based on action
	prompt := "Enter value:"
	switch c.gmInputAction {
	case gmActionLevel:
		prompt = "Enter Level (1-60):"
	case gmActionSprite:
		prompt = "Enter Sprite ID (hex 0x... or decimal):"
	case gmActionPlayerName:
		prompt = "Enter Player/Entity Name:"
	}

	c.drawText(prompt, int32(menuX)+10, baseY, 12,
		colorLightGray)

	// Input box
	boxX := int32(menuX) + 20
	boxY := baseY + 24
	boxW := int32(menuW) - 40
	boxH := int32(24)
	rl.DrawRectangle(boxX, boxY, boxW, boxH, colorFieldBg)
	rl.DrawRectangleLines(boxX, boxY, boxW, boxH, colorGoldenBdr)

	cursor := "_"
	if int(rl.GetTime()*2)%2 == 0 {
		cursor = ""
	}
	c.drawText(c.gmInputBuf+cursor, boxX+4, boxY+4, 13, rl.White)

	c.drawText("[Enter] Confirm  [Esc] Cancel",
		int32(menuX)+10, boxY+boxH+10, 10,
		colorGmLabel)
}

// drawGMZoneList draws the zone proxy list for Goto Zone.
func (c *Client) drawGMZoneList(menuX, menuY, menuW, menuH float32, sepY int32) {
	c.drawText("[1-9] Teleport to zone center  [Esc] Back",
		int32(menuX)+10, sepY+4, 10,
		colorGmLabel)

	if c.terrainLoader == nil {
		c.drawText("No terrain loaded", int32(menuX)+10, sepY+24, 12, rl.Gray)
		return
	}
	proxies := c.terrainLoader.GetZoneProxies()
	if proxies == nil || len(proxies.Zones) == 0 {
		c.drawText("No zones found", int32(menuX)+10, sepY+24, 12, rl.Gray)
		return
	}

	listY := sepY + 20
	rowH := int32(20)
	mx := rl.GetMouseX()
	my := rl.GetMouseY()
	clicked := rl.IsMouseButtonPressed(rl.MouseButtonLeft)

	maxVisible := int((menuH - float32(listY-int32(menuY)) - 10) / float32(rowH))
	endIdx := c.gmMenuScroll + maxVisible
	if endIdx > len(proxies.Zones) {
		endIdx = len(proxies.Zones)
	}

	for i := c.gmMenuScroll; i < endIdx; i++ {
		z := &proxies.Zones[i]
		ry := listY + int32(i-c.gmMenuScroll)*rowH

		displayIdx := i - c.gmMenuScroll
		keyStr := ""
		if displayIdx < 9 {
			keyStr = fmt.Sprintf("%d)", displayIdx+1)
		} else {
			keyStr = "  "
		}

		hover := mx >= int32(menuX)+8 && mx < int32(menuX+menuW)-8 &&
			my >= ry && my < ry+rowH
		if hover {
			rl.DrawRectangle(int32(menuX)+8, ry, int32(menuW)-16, rowH,
				colorGmHighlight)
		}

		if hover && clicked {
			c.teleportTo(z.Center.X, z.Center.Y+10, z.Center.Z)
			c.gmMenuOpen = false
			return
		}

		// Highlight current zone
		nameColor := colorLightGray
		if i == c.terrainZoneIdx {
			nameColor = colorGoldBright
		}

		c.drawText(keyStr, int32(menuX)+12, ry+2, 11,
			colorGoldSemi)

		name := z.Name
		if name == "" {
			name = fmt.Sprintf("Zone %d", i)
		}
		c.drawText(name, int32(menuX)+35, ry+2, 11, nameColor)

		coordStr := fmt.Sprintf("(%.0f, %.0f)", z.Center.X, z.Center.Z)
		coordW := c.measureText(coordStr, 9)
		c.drawText(coordStr, int32(menuX+menuW)-coordW-14, ry+4, 9,
			colorGmDimLabel)
	}

	// Scroll indicator
	drawScrollIndicator(int32(menuX+menuW)-6, listY,
		menuH-float32(listY-int32(menuY))-10,
		c.gmMenuScroll, len(proxies.Zones), maxVisible, colorGmFaint)
}

// drawGMBookmarkList draws the saved teleport locations.
func (c *Client) drawGMBookmarkList(menuX, menuY, menuW, menuH float32, sepY int32) {
	c.drawText("[1-9] Teleport  [Del/X] Delete  [Esc] Back",
		int32(menuX)+10, sepY+4, 10,
		colorGmLabel)

	if len(gmSavedLocations) == 0 {
		c.drawText("No saved locations.", int32(menuX)+10, sepY+24, 12, rl.Gray)
		c.drawText("Use [S] in Entity list to save NPC positions.", int32(menuX)+10, sepY+40, 10,
			rl.Color{R: 120, G: 120, B: 140, A: 180})
		return
	}

	listY := sepY + 20
	rowH := int32(22)
	mx := rl.GetMouseX()
	my := rl.GetMouseY()
	clicked := rl.IsMouseButtonPressed(rl.MouseButtonLeft)

	maxVisible := int((menuH - float32(listY-int32(menuY)) - 10) / float32(rowH))
	endIdx := c.gmMenuScroll + maxVisible
	if endIdx > len(gmSavedLocations) {
		endIdx = len(gmSavedLocations)
	}

	for i := c.gmMenuScroll; i < endIdx; i++ {
		loc := &gmSavedLocations[i]
		ry := listY + int32(i-c.gmMenuScroll)*rowH

		displayIdx := i - c.gmMenuScroll
		keyStr := ""
		if displayIdx < 9 {
			keyStr = fmt.Sprintf("%d)", displayIdx+1)
		} else {
			keyStr = "  "
		}

		hover := mx >= int32(menuX)+8 && mx < int32(menuX+menuW)-8 &&
			my >= ry && my < ry+rowH
		if hover {
			rl.DrawRectangle(int32(menuX)+8, ry, int32(menuW)-16, rowH,
				colorGmHighlight)
		}

		if hover && clicked {
			c.teleportTo(loc.X, loc.Y, loc.Z)
			c.gmMenuOpen = false
			return
		}

		c.drawText(keyStr, int32(menuX)+12, ry+3, 12,
			colorGoldSemi)
		c.drawText(loc.Name, int32(menuX)+35, ry+3, 12,
			colorWhiteFull)

		coordStr := fmt.Sprintf("(%.0f, %.0f, %.0f)", loc.X, loc.Y, loc.Z)
		coordW := c.measureText(coordStr, 10)
		c.drawText(coordStr, int32(menuX+menuW)-coordW-14, ry+5, 10,
			rl.Color{R: 140, G: 140, B: 140, A: 200})
	}

	// Scroll indicator
	drawScrollIndicator(int32(menuX+menuW)-6, listY,
		menuH-float32(listY-int32(menuY))-10,
		c.gmMenuScroll, len(gmSavedLocations), maxVisible, colorGmFaint)
}

// handleGMWorldMapList handles the world map zone list sub-view.
func (c *Client) handleGMWorldMapList() {
	count := len(gmWorldMapZones)

	wheel := rl.GetMouseWheelMove()
	if wheel != 0 {
		c.gmMenuScroll -= int(wheel * 3)
		c.gmClampScroll(count)
	}

	for i := 0; i < 9; i++ {
		if rl.IsKeyPressed(int32(rl.KeyOne + i)) {
			idx := c.gmMenuScroll + i
			if idx < count {
				loc := &gmWorldMapZones[idx]
				c.teleportTo(loc.X, loc.Y, loc.Z)
				c.gmMenuOpen = false
				return
			}
		}
	}
}

// drawGMWorldMapList draws the world map zone list.
func (c *Client) drawGMWorldMapList(menuX, menuY, menuW, menuH float32, sepY int32) {
	c.drawText("[1-9] Teleport to zone center  [Esc] Back",
		int32(menuX)+10, sepY+4, 10,
		colorGmLabel)

	listY := sepY + 20
	rowH := int32(20)
	mx := rl.GetMouseX()
	my := rl.GetMouseY()
	clicked := rl.IsMouseButtonPressed(rl.MouseButtonLeft)

	maxVisible := int((menuH - float32(listY-int32(menuY)) - 10) / float32(rowH))
	endIdx := c.gmMenuScroll + maxVisible
	if endIdx > len(gmWorldMapZones) {
		endIdx = len(gmWorldMapZones)
	}

	for i := c.gmMenuScroll; i < endIdx; i++ {
		loc := &gmWorldMapZones[i]
		ry := listY + int32(i-c.gmMenuScroll)*rowH

		displayIdx := i - c.gmMenuScroll
		keyStr := ""
		if displayIdx < 9 {
			keyStr = fmt.Sprintf("%d)", displayIdx+1)
		} else {
			keyStr = "  "
		}

		hover := mx >= int32(menuX)+8 && mx < int32(menuX+menuW)-8 &&
			my >= ry && my < ry+rowH
		if hover {
			rl.DrawRectangle(int32(menuX)+8, ry, int32(menuW)-16, rowH,
				colorGmHighlight)
		}

		if hover && clicked {
			c.teleportTo(loc.X, loc.Y, loc.Z)
			c.gmMenuOpen = false
			return
		}

		c.drawText(keyStr, int32(menuX)+12, ry+2, 11,
			colorGoldSemi)
		c.drawText(loc.Name, int32(menuX)+35, ry+2, 11,
			colorWhiteFull)

		coordStr := fmt.Sprintf("(%.0f, %.0f)", loc.X, loc.Z)
		coordW := c.measureText(coordStr, 9)
		c.drawText(coordStr, int32(menuX+menuW)-coordW-14, ry+4, 9,
			colorGmDimLabel)
	}

	// Scroll indicator
	drawScrollIndicator(int32(menuX+menuW)-6, listY,
		menuH-float32(listY-int32(menuY))-10,
		c.gmMenuScroll, len(gmWorldMapZones), maxVisible, colorGmFaint)
}

// drawGMEntityList draws the nearby entity list for Goto Player/Entity.
func (c *Client) drawGMEntityList(menuX, menuY, menuW, menuH float32, sepY int32) {
	c.drawText("[1-9] Teleport  [S] Save  [P] Search  [Esc] Back",
		int32(menuX)+10, sepY+4, 10,
		colorGmLabel)

	entities := c.gmSortedEntities()
	if len(entities) == 0 {
		c.drawText("No entities in range", int32(menuX)+10, sepY+24, 12, rl.Gray)
		return
	}

	listY := sepY + 20
	rowH := int32(20)
	mx := rl.GetMouseX()
	my := rl.GetMouseY()
	clicked := rl.IsMouseButtonPressed(rl.MouseButtonLeft)

	maxVisible := int((menuH - float32(listY-int32(menuY)) - 10) / float32(rowH))
	endIdx := c.gmMenuScroll + maxVisible
	if endIdx > len(entities) {
		endIdx = len(entities)
	}

	for i := c.gmMenuScroll; i < endIdx; i++ {
		ent := entities[i]
		ry := listY + int32(i-c.gmMenuScroll)*rowH

		displayIdx := i - c.gmMenuScroll
		keyStr := ""
		if displayIdx < 9 {
			keyStr = fmt.Sprintf("%d)", displayIdx+1)
		} else {
			keyStr = "  "
		}

		hover := mx >= int32(menuX)+8 && mx < int32(menuX+menuW)-8 &&
			my >= ry && my < ry+rowH
		if hover {
			rl.DrawRectangle(int32(menuX)+8, ry, int32(menuW)-16, rowH,
				colorGmHighlight)
		}

		if hover && clicked {
			c.teleportTo(ent.X, ent.Y, ent.Z)
			c.gmMenuOpen = false
			return
		}

		// Color by type
		nameColor := colorLightGray
		if ent.IsPlayer {
			nameColor = rl.Color{R: 100, G: 200, B: 255, A: 255}
		}
		if ent.ObjectID == c.targetObjectID {
			nameColor = colorGoldBright
		}

		c.drawText(keyStr, int32(menuX)+12, ry+2, 11,
			colorGoldSemi)

		name := ent.Name
		if name == "" {
			name = fmt.Sprintf("Entity #%d", ent.ObjectID)
		}
		c.drawText(name, int32(menuX)+35, ry+2, 11, nameColor)

		// Show XYZ coords + distance on the right side
		coordStr := fmt.Sprintf("(%.0f,%.0f,%.0f) %.0fm", ent.X, ent.Y, ent.Z, ent.Dist)
		coordW := c.measureText(coordStr, 9)
		c.drawText(coordStr, int32(menuX+menuW)-coordW-14, ry+4, 9,
			colorGmDimLabel)
	}

	// Scroll indicator
	drawScrollIndicator(int32(menuX+menuW)-6, listY,
		menuH-float32(listY-int32(menuY))-10,
		c.gmMenuScroll, len(entities), maxVisible, colorGmFaint)
}

// drawGMDisplayPos renders the persistent position HUD when enabled.
func (c *Client) drawGMDisplayPos() {
	if !c.gmDisplayPos {
		return
	}

	p := c.engine.State.Player
	lines := []string{
		fmt.Sprintf("X: %.2f", p.X),
		fmt.Sprintf("Y: %.2f", p.Y),
		fmt.Sprintf("Z: %.2f", p.Z),
		fmt.Sprintf("Facing: %.1f°", float64(p.Facing)*180/math.Pi),
		fmt.Sprintf("Zone: %d", c.terrainZoneIdx),
	}
	if c.gmFastRun {
		lines = append(lines, "FAST RUN")
	}
	if c.gmNoClip {
		lines = append(lines, "NOCLIP")
	}
	if c.gmInvis {
		lines = append(lines, "INVIS")
	}
	if s := c.gridRunnerStatus(); s != "" {
		lines = append(lines, s)
	}

	x := int32(10)
	y := int32(rl.GetScreenHeight()) - int32(len(lines))*14 - 10
	for _, ln := range lines {
		c.drawText(ln, x+1, y+1, 12, colorOverlay)
		c.drawText(ln, x, y, 12, rl.Color{R: 200, G: 255, B: 200, A: 230})
		y += 14
	}
}

// drawGMStats renders the debug stats overlay when enabled.
func (c *Client) drawGMStats() {
	if !c.gmShowStats {
		return
	}

	fps := rl.GetFPS()
	entCount := len(c.engine.State.Entities)
	dt := rl.GetFrameTime()

	lines := []string{
		fmt.Sprintf("FPS: %d  (%.1fms)", fps, dt*1000),
		fmt.Sprintf("Entities: %d", entCount),
		fmt.Sprintf("Speed: %.1f u/s", c.currentSpeed),
	}
	if c.effectVolumes != nil {
		lines = append(lines, fmt.Sprintf("EffVols: %d", c.effectVolumes.VolumeCount()))
	}
	if c.terrainRender != nil {
		lines = append(lines, fmt.Sprintf("Zone: %d", c.terrainZoneIdx))
	}
	if c.gmExtrapolate {
		lines = append(lines, "Move: EXTRAPOLATE")
	} else {
		lines = append(lines, "Move: INTERPOLATE")
	}

	x := int32(rl.GetScreenWidth()) - 180
	y := int32(10)
	// Background
	rl.DrawRectangle(x-4, y-2, 178, int32(len(lines))*14+4,
		rl.Color{R: 0, G: 0, B: 0, A: 160})
	for _, ln := range lines {
		c.drawText(ln, x, y, 11, rl.Color{R: 180, G: 255, B: 180, A: 230})
		y += 14
	}
}
