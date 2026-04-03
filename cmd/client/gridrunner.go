//go:build dev

package main

import (
	"database/sql"
	"fmt"
	"log"
	"math"
	"sync"
	"time"

	"github.com/eqoa/client/internal/terrain"
	_ "github.com/mattn/go-sqlite3"
)

// gridRunnerAdjustY samples terrain height and keeps the player 20 units above ground.
// This prevents drowning in water or clipping underground during sweeps.
func (c *Client) gridRunnerAdjustY() {
	gr := c.gridRunner
	if gr == nil || !gr.active {
		return
	}
	gs := c.engine.State
	if c.terrainLoader == nil {
		return
	}

	// Sample terrain height at current position
	var zt *terrain.ZoneTerrain
	if c.terrainZoneIdx >= 0 {
		zt = c.terrainLoader.GetZoneTerrain(c.terrainZoneIdx)
	}
	if zt == nil {
		return
	}

	groundY, ok := zt.SampleHeight(gs.Player.X, gs.Player.Z, gs.Player.Y)
	if ok {
		targetY := groundY + 20
		if gs.Player.Y < targetY-5 || gs.Player.Y > targetY+50 {
			gs.Player.Y = targetY
		}
	}
}

// Tunaria zone grid — copied from crawler/zonegrid.go for land detection.
const (
	grGridCols   = 12
	grGridRows   = 15
	grCellSize   = float32(2000.0)
	grGridOrigX  = float32(3000.0)
	grGridOrigZ  = float32(1000.0)
	grOcean      = uint16(641)
	grMountain   = uint16(1047)
)

var grTunariaGrid = [grGridRows][grGridCols]uint16{
	{641, 641, 641, 641, 641, 641, 641, 641, 641, 1152, 1153, 1176},
	{1051, 1046, 1057, 1058, 1074, 1075, 364, 1097, 1123, 1124, 1151, 1154},
	{1175, 1042, 1047, 1056, 1059, 1073, 1076, 1096, 1098, 1122, 1125, 1150},
	{361, 1174, 1045, 1047, 1055, 1060, 1072, 1077, 1095, 608, 1121, 1126},
	{1149, 1155, 1173, 1047, 1047, 1054, 1061, 1071, 1078, 1094, 1099, 1120},
	{1127, 1148, 1156, 1172, 1047, 1047, 1053, 1062, 602, 1079, 1093, 1100},
	{1119, 1128, 1147, 1157, 1171, 1047, 1047, 1052, 1063, 1070, 1080, 1092},
	{1101, 1118, 1129, 1146, 1158, 358, 1047, 1047, 357, 1064, 1069, 1081},
	{1091, 1102, 1117, 1130, 1145, 1159, 1170, 1047, 1047, 1050, 1065, 1068},
	{1082, 1090, 1103, 1116, 1131, 1144, 1160, 1047, 1047, 1048, 366, 1066},
	{1067, 1083, 1089, 1104, 1115, 1132, 1143, 1161, 1047, 1047, 1049, 1049},
	{641, 641, 641, 1088, 1105, 1114, 1133, 1142, 1162, 1047, 1047, 641},
	{641, 641, 641, 641, 1087, 1106, 1113, 1134, 1141, 1163, 1169, 1047},
	{641, 641, 641, 641, 641, 1086, 1107, 1112, 1135, 1140, 1164, 1168},
	{1047, 641, 641, 641, 641, 641, 1085, 1108, 1111, 1136, 1139, 1165},
}

// grIsLand returns true if the world position is over a land zone (not ocean/mountain/void).
func grIsLand(x, z float32) bool {
	col := int((x - grGridOrigX + grCellSize/2) / grCellSize)
	row := int((z - grGridOrigZ + grCellSize/2) / grCellSize)
	if col < 0 || col >= grGridCols || row < 0 || row >= grGridRows {
		return false
	}
	zid := grTunariaGrid[row][col]
	return zid != 0 && zid != grOcean && zid != grMountain
}

// grLandZRange returns the min/max Z that is land for a given X column.
func grLandZRange(x float32) (minZ, maxZ float32, ok bool) {
	col := int((x - grGridOrigX + grCellSize/2) / grCellSize)
	if col < 0 || col >= grGridCols {
		return 0, 0, false
	}
	first, last := -1, -1
	for row := 0; row < grGridRows; row++ {
		zid := grTunariaGrid[row][col]
		if zid != 0 && zid != grOcean && zid != grMountain {
			if first < 0 {
				first = row
			}
			last = row
		}
	}
	if first < 0 {
		return 0, 0, false
	}
	minZ = grGridOrigZ + float32(first)*grCellSize - grCellSize/2
	maxZ = grGridOrigZ + float32(last)*grCellSize + grCellSize/2
	return minZ, maxZ, true
}

// gridRunnerState automates serpentine traversal of the world, harvesting entity data.
// Activated from the GM menu. Uses the visual client's normal movement system —
// the server sees legitimate walking/running with proper velocity and animation.
type gridRunnerState struct {
	active    bool
	db        *sql.DB
	mu        sync.Mutex
	seen      map[uint32]bool // ObjectIDs already recorded
	npcCount  int
	startTime time.Time

	// Grid parameters
	stepSize  float32 // column spacing (X distance between sweeps)
	minX      float32
	maxX      float32
	col       int     // current column index
	totalCols int
	goingDown bool    // true = sweeping +Z, false = sweeping -Z
	targetX   float32 // current waypoint X
	targetZ   float32 // current waypoint Z
	colMinZ   float32 // land Z range for current column
	colMaxZ   float32

	// State
	phase       int // 0=moving to column start, 1=sweeping column, 2=stepping to next column
	lastFlush   time.Time
	lastHarvest time.Time
}

const (
	grPhaseMoveTo  = 0
	grPhaseSweep   = 1
	grPhaseStep    = 2
	grPhaseDone    = 3
)

// gridRunnerStart initializes and starts the grid runner from current position.
func (c *Client) gridRunnerStart() {
	if c.gridRunner != nil && c.gridRunner.active {
		c.gridRunner.active = false
		log.Printf("GRID RUNNER: stopped")
		return
	}

	gs := c.engine.State

	db, err := sql.Open("sqlite3", "crawled_npcs.db")
	if err != nil {
		log.Printf("GRID RUNNER: failed to open DB: %v", err)
		return
	}
	initgridRunnerStateDB(db)

	// Grid bounds: X covers zone grid columns 0-11 (X=2000 to X=26000)
	gr := &gridRunnerState{
		active:    true,
		db:        db,
		seen:      make(map[uint32]bool),
		stepSize:  500,
		minX:      2000,
		maxX:      26000,
		startTime: time.Now(),
		lastFlush: time.Now(),
	}

	// Start from current X, round to nearest column
	curX := gs.Player.X
	gr.col = int((curX - gr.minX) / gr.stepSize)
	if gr.col < 0 {
		gr.col = 0
	}
	gr.totalCols = int((gr.maxX-gr.minX)/gr.stepSize) + 1

	// First column target — find land Z range
	gr.targetX = gr.minX + float32(gr.col)*gr.stepSize
	gr.goingDown = true
	gr.setupColumn() // sets colMinZ, colMaxZ, targetZ
	gr.phase = grPhaseMoveTo

	// Load progress from previous runs
	gr.loadProgress()

	c.gridRunner = gr
	c.autoRun = false // disable normal autorun

	if gr.col >= gr.totalCols {
		log.Printf("GRID RUNNER: all %d columns already completed!", gr.totalCols)
		gr.active = false
		return
	}

	log.Printf("GRID RUNNER: started at col %d/%d (X=%.0f), step=%.0f",
		gr.col+1, gr.totalCols, gr.targetX, gr.stepSize)
}

// setupColumn configures the Z sweep range for the current column using the zone grid.
// Skips columns that have no land zones.
func (gr *gridRunnerState) setupColumn() {
	for gr.col < gr.totalCols {
		x := gr.minX + float32(gr.col)*gr.stepSize
		gr.targetX = x
		minZ, maxZ, hasLand := grLandZRange(x)
		if hasLand {
			gr.colMinZ = minZ
			gr.colMaxZ = maxZ
			if gr.goingDown {
				gr.targetZ = gr.colMinZ
			} else {
				gr.targetZ = gr.colMaxZ
			}
			return
		}
		// No land — skip this column
		log.Printf("GRID RUNNER: col %d (X=%.0f) — no land, skipping", gr.col+1, x)
		gr.saveColumnProgress(gr.col)
		gr.col++
		gr.goingDown = !gr.goingDown
	}
}

// gridRunnerUpdate is called each frame from handleInWorldInput.
// Returns (moveX, moveZ) direction override, or (0,0) if not active.
func (c *Client) gridRunnerUpdate() (float32, float32) {
	gr := c.gridRunner
	if gr == nil || !gr.active {
		return 0, 0
	}

	gs := c.engine.State
	px, pz := gs.Player.X, gs.Player.Z

	// Keep player above terrain
	c.gridRunnerAdjustY()

	// Harvest entities every frame
	c.gridRunnerHarvest()

	// Flush to DB every 60 seconds
	if time.Since(gr.lastFlush) > 60*time.Second {
		c.gridRunnerFlush()
		gr.lastFlush = time.Now()
	}

	const arriveThresh = float32(15.0) // close enough to waypoint

	switch gr.phase {
	case grPhaseMoveTo:
		// Moving to column start
		dx := gr.targetX - px
		dz := gr.targetZ - pz
		dist := float32(math.Sqrt(float64(dx*dx + dz*dz)))
		if dist < arriveThresh {
			// Arrived at column start, begin sweep
			gr.phase = grPhaseSweep
			if gr.goingDown {
				gr.targetZ = gr.colMaxZ
			} else {
				gr.targetZ = gr.colMinZ
			}
			log.Printf("GRID RUNNER: column %d/%d sweep X=%.0f Z=%.0f→%.0f | NPCs: %d",
				gr.col+1, gr.totalCols, gr.targetX,
				pz, gr.targetZ, gr.npcCount)
			return 0, 0
		}
		return dx / dist, dz / dist

	case grPhaseSweep:
		// Sweeping the column (north or south)
		dz := gr.targetZ - pz
		dist := float32(math.Abs(float64(dz)))
		if dist < arriveThresh {
			// Column complete — save progress and advance
			gr.saveColumnProgress(gr.col)
			gr.col++
			gr.goingDown = !gr.goingDown
			if gr.col >= gr.totalCols {
				gr.phase = grPhaseDone
				c.gridRunnerFlush()
				log.Printf("GRID RUNNER: COMPLETE | %d unique NPCs | elapsed: %s",
					gr.npcCount, time.Since(gr.startTime).Round(time.Second))
				gr.active = false
				return 0, 0
			}
			// Find next column with land
			gr.setupColumn()
			if gr.col >= gr.totalCols {
				gr.phase = grPhaseDone
				c.gridRunnerFlush()
				log.Printf("GRID RUNNER: COMPLETE | %d unique NPCs | elapsed: %s",
					gr.npcCount, time.Since(gr.startTime).Round(time.Second))
				gr.active = false
				return 0, 0
			}
			gr.phase = grPhaseStep
			log.Printf("GRID RUNNER: stepping to col %d (X=%.0f, Z=%.0f-%.0f) | NPCs: %d",
				gr.col+1, gr.targetX, gr.colMinZ, gr.colMaxZ, gr.npcCount)
			return 0, 0
		}
		// Move straight along Z
		if dz > 0 {
			return 0, 1
		}
		return 0, -1

	case grPhaseStep:
		// Moving to next column's start position (X + Z)
		dx := gr.targetX - px
		dz := gr.targetZ - pz
		dist := float32(math.Sqrt(float64(dx*dx + dz*dz)))
		if dist < arriveThresh {
			// At next column, start sweep
			gr.phase = grPhaseSweep
			if gr.goingDown {
				gr.targetZ = gr.colMaxZ
			} else {
				gr.targetZ = gr.colMinZ
			}
			log.Printf("GRID RUNNER: column %d/%d sweep X=%.0f Z=%.0f→%.0f | NPCs: %d",
				gr.col+1, gr.totalCols, gr.targetX,
				pz, gr.targetZ, gr.npcCount)
			return 0, 0
		}
		return dx / dist, dz / dist

	case grPhaseDone:
		gr.active = false
		return 0, 0
	}

	return 0, 0
}

// gridRunnerHarvest records visible entities to the seen map.
func (c *Client) gridRunnerHarvest() {
	gr := c.gridRunner
	gs := c.engine.State

	gr.mu.Lock()
	defer gr.mu.Unlock()

	for _, ent := range gs.Entities {
		if ent.IsPlayer || ent.NameColor == 2 {
			continue
		}
		if ent.ObjectID == 0 || ent.Name == "" {
			continue
		}
		if gr.seen[ent.ObjectID] {
			continue
		}
		gr.seen[ent.ObjectID] = true
		gr.npcCount++

		// Insert directly
		gr.db.Exec(`INSERT OR IGNORE INTO npcs (
			npc_id, npc_name, x_coord, y_coord, z_coord, facing,
			modelid, npc_level, npc_type, race, size,
			primary_weapon, secondary_weapon, shield,
			torso, forearm, gloves, legs, feet, head,
			torso_tint, forearm_tint, gloves_tint, legs_tint, feet_tint, head_tint,
			hair_color, hair_length, hair_style, face,
			robe_style, robe_tint, hp
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			ent.ObjectID, ent.Name, ent.X, ent.Y, ent.Z, ent.Facing,
			ent.ModelID, ent.Level, ent.NPCType, ent.Race, ent.Scale,
			ent.PrimaryWeapon, ent.SecondaryWeapon, ent.Shield,
			ent.Chest, ent.Bracer, ent.Gloves, ent.Legs, ent.Boots, ent.Helm,
			ent.ChestTint, ent.BracerTint, ent.GlovesTint, ent.LegsTint, ent.BootsTint, ent.HelmTint,
			ent.HairColor, ent.HairLength, ent.HairStyle, ent.FaceOption,
			ent.Robe, ent.RobeTint, ent.HP,
		)
	}
}

// gridRunnerFlush commits pending data (no-op since we insert directly).
func (c *Client) gridRunnerFlush() {
	gr := c.gridRunner
	elapsed := time.Since(gr.startTime).Round(time.Second)
	log.Printf("GRID RUNNER: col %d/%d | NPCs: %d | elapsed: %s",
		gr.col+1, gr.totalCols, gr.npcCount, elapsed)
}

// gridRunnerStatus returns a short status string for the HUD.
func (c *Client) gridRunnerStatus() string {
	gr := c.gridRunner
	if gr == nil || !gr.active {
		return ""
	}
	dir := "S"
	if !gr.goingDown {
		dir = "N"
	}
	return fmt.Sprintf("GR: col %d/%d %s | %d NPCs", gr.col+1, gr.totalCols, dir, gr.npcCount)
}

func initgridRunnerStateDB(db *sql.DB) {
	db.Exec(`CREATE TABLE IF NOT EXISTS npcs (
		npc_id INTEGER PRIMARY KEY,
		npc_name TEXT,
		x_coord REAL, y_coord REAL, z_coord REAL,
		facing REAL,
		modelid INTEGER,
		npc_level INTEGER,
		npc_type INTEGER,
		race INTEGER,
		size REAL,
		primary_weapon INTEGER,
		secondary_weapon INTEGER,
		shield INTEGER,
		torso INTEGER, forearm INTEGER, gloves INTEGER, legs INTEGER, feet INTEGER, head INTEGER,
		torso_tint INTEGER, forearm_tint INTEGER, gloves_tint INTEGER, legs_tint INTEGER, feet_tint INTEGER, head_tint INTEGER,
		hair_color INTEGER, hair_length INTEGER, hair_style INTEGER, face INTEGER,
		robe_style INTEGER, robe_tint INTEGER,
		hp INTEGER
	)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_npcs_name ON npcs(npc_name)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_npcs_level ON npcs(npc_level)`)

	db.Exec(`CREATE TABLE IF NOT EXISTS gr_progress (
		col_id INTEGER PRIMARY KEY,
		completed_at TEXT
	)`)
}

// loadProgress loads completed columns and previously seen ObjectIDs from DB.
func (gr *gridRunnerState) loadProgress() {
	// Load completed columns
	rows, err := gr.db.Query(`SELECT col_id FROM gr_progress`)
	if err != nil {
		return
	}
	defer rows.Close()
	completed := 0
	maxCol := -1
	for rows.Next() {
		var col int
		rows.Scan(&col)
		completed++
		if col > maxCol {
			maxCol = col
		}
	}

	// Resume from the column after the last completed one
	if maxCol >= 0 {
		gr.col = maxCol + 1
		gr.targetX = gr.minX + float32(gr.col)*gr.stepSize
		// Alternate direction based on column parity
		gr.goingDown = gr.col%2 == 0
		log.Printf("GRID RUNNER: resuming — %d columns done, starting at col %d (X=%.0f)",
			completed, gr.col+1, gr.targetX)
	}

	// Load previously seen ObjectIDs so we don't double-count
	idRows, err := gr.db.Query(`SELECT npc_id FROM npcs`)
	if err != nil {
		return
	}
	defer idRows.Close()
	for idRows.Next() {
		var id uint32
		idRows.Scan(&id)
		gr.seen[id] = true
		gr.npcCount++
	}
	if gr.npcCount > 0 {
		log.Printf("GRID RUNNER: loaded %d previously seen NPCs", gr.npcCount)
	}
}

// saveColumnProgress marks a column as completed in the DB.
func (gr *gridRunnerState) saveColumnProgress(col int) {
	gr.db.Exec(`INSERT OR REPLACE INTO gr_progress (col_id, completed_at) VALUES (?, datetime('now'))`, col)
}
