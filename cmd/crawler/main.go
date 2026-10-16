package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/eqoa/client/internal/game"
	_ "github.com/mattn/go-sqlite3"
)

// CrawledNPC holds all data collected from a single entity sighting.
type CrawledNPC struct {
	ObjectID        uint32
	Name            string
	X, Y, Z         float32
	Facing          float32
	ModelID         uint32
	Level           byte
	NPCType         uint16
	Race            byte
	Scale           float32
	PrimaryWeapon   uint32
	SecondaryWeapon uint32
	Shield          uint32
	Chest, Bracer, Gloves, Legs, Boots, Helm byte
	ChestTint, BracerTint, GlovesTint, LegsTint, BootsTint, HelmTint byte
	HairColor, HairLength, HairStyle, FaceOption byte
	Robe, RobeTint  byte
	HP              byte
}

// Crawler manages the grid scan and entity collection.
type Crawler struct {
	engine    *game.Engine
	db        *sql.DB
	mu        sync.Mutex
	seen      map[uint32]*CrawledNPC // ObjectID → best sighting
	stepSize  float32
	moveSpeed float32 // world units per second
	dwellMs   int
	minX      float32
	maxX      float32
	minZ      float32
	maxZ      float32
	travelY   float32 // Y coordinate for travel
	points    int     // total grid points
	visited   int     // grid points visited
	buffName     string  // buff spell to keep active (substring match)
	buffSlot     int     // hotbar slot of buff spell (-1 = none)
	lastBuff     time.Time
	buffInterval time.Duration
	interacted   map[uint32]bool // ObjectIDs of NPCs we've already interacted with
	passes       int             // passes per column (>1 for dense areas)
}

// accountPass is the game account password, from $EQOA_PASSWORD.
var accountPass string

func main() {
	serverIP := flag.String("server", "sandstorm", "Server: 'sandstorm' or IP")
	charIdx := flag.Int("char", 3, "Character index (1-indexed)")
	stepSize := flag.Float64("step", 500, "Column spacing in world units (entity visibility ~300 radius, 500 for overlap)")
	speed := flag.Float64("speed", 30, "Movement speed hint (server interpolates)")
	dwell := flag.Int("dwell", 0, "Dwell time at each point (ms, 0=continuous)")
	travelY := flag.Float64("y", 0, "Travel Y override (0 = auto from zone bbox)")
	dbPath := flag.String("db", "crawled_npcs.db", "Output SQLite database path")
	minX := flag.Float64("min-x", -4000, "Min X coordinate")
	maxX := flag.Float64("max-x", 32000, "Max X coordinate")
	minZ := flag.Float64("min-z", -4000, "Min Z coordinate")
	maxZ := flag.Float64("max-z", 32000, "Max Z coordinate")
	buff := flag.String("buff", "Lila", "Buff spell name to keep active (default: Lila's Refrain)")
	buffSecs := flag.Int("buff-interval", 270, "Seconds between buff recasts (Lila's Refrain ~5min)")
	passes := flag.Int("passes", 1, "Passes per column (>1 captures more than 28 entities in dense areas)")
	flag.Parse()

	if accountPass = os.Getenv("EQOA_PASSWORD"); accountPass == "" {
		log.Fatal("set EQOA_PASSWORD to the account password")
	}

	switch strings.ToLower(*serverIP) {
	case "sandstorm", "eqoa.live":
		*serverIP = "5.161.217.188"
	case "spire", "local":
		*serverIP = "166.113.93.253"
	}

	// Open output database
	db, err := sql.Open("sqlite3", *dbPath)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()
	initDB(db)

	// Create engine and connect
	engine := game.NewEngine(*serverIP, 10070, 9735, 7000, "dab", accountPass)
	engine.AutoConnect = true

	stepsX := int((*maxX-*minX) / *stepSize) + 1

	c := &Crawler{
		engine:       engine,
		db:           db,
		seen:         make(map[uint32]*CrawledNPC),
		stepSize:     float32(*stepSize),
		moveSpeed:    float32(*speed),
		dwellMs:      *dwell,
		minX:         float32(*minX),
		maxX:         float32(*maxX),
		minZ:         float32(*minZ),
		maxZ:         float32(*maxZ),
		travelY:      float32(*travelY),
		points:       stepsX,
		buffName:     *buff,
		buffSlot:     -1,
		buffInterval: time.Duration(*buffSecs) * time.Second,
		interacted:   make(map[uint32]bool),
		passes:       *passes,
	}

	// Start tick loop — references c.engine so it follows reconnects
	ticker := time.NewTicker(100 * time.Millisecond)
	go func() {
		for range ticker.C {
			if c.engine != nil {
				c.engine.Tick()
			}
		}
	}()

	// Graceful shutdown — flush DB on Ctrl+C
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Printf("\nInterrupted. Flushing %d NPCs to database...\n", len(c.seen))
		c.flushToDB()
		fmt.Printf("Saved. Visited %d/%d grid points.\n", c.visited, c.points)
		os.Exit(0)
	}()

	// Wait for character select
	log.Printf("Connecting to %s, selecting character %d...", *serverIP, *charIdx)
	waitForPhase(engine, game.PhaseCharSelect, 30*time.Second)

	gs := engine.State
	if *charIdx-1 >= len(gs.Characters) {
		log.Fatalf("Character %d not found (have %d)", *charIdx, len(gs.Characters))
	}
	log.Printf("Selecting: %s", gs.Characters[*charIdx-1].Name)
	gs.SelectCharacter(*charIdx - 1)

	// Wait for world entry (MemoryDump parsed)
	waitForPhase(engine, game.PhaseInWorld, 30*time.Second)
	log.Printf("In world at (%.0f, %.0f, %.0f). Starting grid crawl...",
		gs.Player.X, gs.Player.Y, gs.Player.Z)

	// Small delay to let initial state channels arrive
	time.Sleep(3 * time.Second)

	// Dump MemoryDump data (items, spells) to DB
	c.dumpItems()
	c.dumpSpells()

	// Find buff spell on hotbar
	if c.buffName != "" {
		c.findBuffSpell()
	}

	// Cast initial buff
	c.maintainBuff()

	// Harvest initial entities at spawn point
	c.harvestEntities()

	// Run the grid crawl
	c.crawl()

	// Final flush
	c.flushToDB()
	log.Printf("Crawl complete. %d unique NPCs found across %d grid points.", len(c.seen), c.visited)
}

func (c *Crawler) findBuffSpell() {
	gs := c.engine.State
	needle := strings.ToLower(c.buffName)
	for i, sp := range gs.ParsedSpells {
		if strings.Contains(strings.ToLower(sp.Name), needle) {
			if sp.WhereOnHotBar >= 0 && sp.WhereOnHotBar < 10 {
				c.buffSlot = int(sp.WhereOnHotBar)
				log.Printf("Found buff spell: %q at hotbar slot %d", sp.Name, c.buffSlot)
			} else {
				// Put it on slot 9
				c.engine.MemorizeAbility(&gs.ParsedSpells[i], 9)
				c.buffSlot = 9
				log.Printf("Added buff spell %q to hotbar slot 9 (spell index %d)", sp.Name, i)
				time.Sleep(500 * time.Millisecond)
			}
			return
		}
	}
	log.Printf("WARNING: buff spell %q not found in spell list", c.buffName)
}

func (c *Crawler) maintainBuff() {
	if c.buffSlot < 0 {
		return
	}
	if time.Since(c.lastBuff) < c.buffInterval {
		return
	}
	gs := c.engine.State
	log.Printf("Casting buff (slot %d) at pos=(%.0f,%.0f)", c.buffSlot, gs.Player.X, gs.Player.Z)
	c.engine.SendCast(uint8(c.buffSlot), gs.Player.ObjectID)
	c.lastBuff = time.Now()
	// Wait for cast to complete
	time.Sleep(2 * time.Second)
}

// getY returns the surface Y for the given (x,z) position.
// Uses zone bbox lookup for auto mode, or fixed Y if overridden.
func (c *Crawler) getY(x, z float32) float32 {
	if c.travelY != 0 {
		return c.travelY
	}
	return lookupSurfaceY(x, z)
}

// moveTo smoothly moves from current position to target, harvesting entities along the way.
// Returns when destination is reached.
func (c *Crawler) moveTo(targetX, targetZ float32) {
	gs := c.engine.State
	const tickInterval = 100 * time.Millisecond
	movePerTick := c.moveSpeed * 0.1 // units per 100ms tick

	for {
		curX := gs.Player.X
		curZ := gs.Player.Z

		dx := targetX - curX
		dz := targetZ - curZ
		dist := float32(math.Sqrt(float64(dx*dx + dz*dz)))

		if dist < movePerTick {
			// Close enough — snap to target
			c.engine.SetPosition(targetX, c.getY(targetX, targetZ), targetZ, gs.Player.Facing)
			return
		}

		// Move one step toward target
		ratio := movePerTick / dist
		newX := curX + dx*ratio
		newZ := curZ + dz*ratio

		// Face direction of travel
		facing := float32(math.Atan2(float64(dx), float64(dz)))
		y := c.getY(newX, newZ)
		c.engine.SetPosition(newX, y, newZ, facing)

		// Set velocity so the server sees us as moving (needed for EntityInRange updates)
		velRatio := c.moveSpeed / dist
		c.engine.VelX = dx * velRatio
		c.engine.VelZ = dz * velRatio
		c.engine.AnimByte = 2 // running animation

		// Harvest entities while traveling
		c.harvestEntities()
		c.maintainBuff()

		time.Sleep(tickInterval)
	}
}

func (c *Crawler) crawl() {
	startTime := time.Now()

	// Movement-based sweep: move from column to column without disconnecting.
	// The server tracks position from continuous 0x40 updates and sends
	// entity state channels for nearby NPCs.

	columns := int((c.maxX - c.minX) / c.stepSize) + 1
	completed := c.loadProgress()
	col := 0

	for x := c.minX; x <= c.maxX; x += c.stepSize {
		col++

		// Skip completed columns on resume
		if completed[col] {
			log.Printf("=== Column %d/%d: X=%.0f — SKIPPING (already done) ===", col, columns, x)
			continue
		}

		// Skip columns with no land zones (ocean/mountain only)
		landMinZ, landMaxZ, hasLand := landColumnRanges(x)
		if !hasLand {
			log.Printf("=== Column %d/%d: X=%.0f — SKIPPING (no land zones) ===", col, columns, x)
			c.saveProgress(col)
			continue
		}

		// Clip Z range to user-specified bounds
		if landMinZ < c.minZ {
			landMinZ = c.minZ
		}
		if landMaxZ > c.maxZ {
			landMaxZ = c.maxZ
		}

		sweepDown := col%2 == 1 // alternate direction per column

		var startZ, endZ float32
		if sweepDown {
			startZ = landMinZ
			endZ = landMaxZ
		} else {
			startZ = landMaxZ
			endZ = landMinZ
		}

		startY := c.getY(x, startZ)
		log.Printf("=== Column %d/%d: X=%.0f Z=%.0f→%.0f Y=%.0f ===",
			col, columns, x, startZ, endZ, startY)

		// Teleport to column start — disconnect/reconnect for fresh entity channels
		c.gmTeleportTo(x, startY, startZ)

		// Wait for world entry and entity channels
		time.Sleep(5 * time.Second)
		c.harvestEntities()

		// Cast buff after reconnect
		if c.buffName != "" {
			c.findBuffSpell()
			c.lastBuff = time.Time{}
			c.maintainBuff()
		}

		// Sweep the column
		log.Printf("Column %d: sweeping Z=%.0f → %.0f at X=%.0f",
			col, startZ, endZ, x)
		c.sweepColumn(x, 0, startZ, endZ)

		// Flush and save progress after each column
		c.flushToDB()
		c.saveProgress(col)
		elapsed := time.Since(startTime)
		done := 0
		for _, v := range completed {
			if v {
				done++
			}
		}
		done++ // this column
		remaining := columns - done
		if done > 0 {
			perCol := elapsed / time.Duration(done)
			eta := perCol * time.Duration(remaining)
			log.Printf("Column %d/%d complete | NPCs: %d | elapsed: %s | ETA: %s",
				col, columns, len(c.seen), elapsed.Round(time.Second), eta.Round(time.Minute))
		}
	}
}

// sweepColumn moves from startZ to endZ at fixed X, harvesting entities the whole way.
func (c *Crawler) sweepColumn(x, _, startZ, endZ float32) {
	const tickInterval = 200 * time.Millisecond
	step := c.moveSpeed * 0.2 // units per tick
	if endZ < startZ {
		step = -step
	}

	z := startZ
	lastLog := time.Now()
	for {
		// Check if we've reached the end
		if (step > 0 && z >= endZ) || (step < 0 && z <= endZ) {
			break
		}

		z += step
		y := lookupSurfaceY(x, z) + 20 // stay 20 units above local terrain
		c.engine.SetPosition(x, y, z, 0)

		// Set velocity so server tracks our movement for EntityInRange
		c.engine.VelX = 0
		c.engine.VelZ = step / 0.2 // step per tick / tick duration = units per second
		c.engine.AnimByte = 2       // running

		// Harvest every tick
		c.harvestEntities()
		c.maintainBuff()

		// Progress every 30 seconds
		if time.Since(lastLog) > 30*time.Second {
			gs := c.engine.State
			log.Printf("  sweep: X=%.0f Z=%.0f | entities=%d | NPCs=%d | player=(%.0f,%.0f,%.0f)",
				x, z, len(gs.Entities), len(c.seen), gs.Player.X, gs.Player.Y, gs.Player.Z)
			lastLog = time.Now()
		}

		// Flush every 5 minutes during long sweeps
		c.visited++
		if c.visited%1500 == 0 {
			c.flushToDB()
		}

		time.Sleep(tickInterval)
	}
}

// gmTeleportTo moves to position, lets the server save it, then disconnects and reconnects.
// The reconnect spawns us at the saved position with fresh entity channels.
func (c *Crawler) gmTeleportTo(x, y, z float32) {
	// Send position updates for a few seconds so the server saves the new location
	c.engine.SetPosition(x, y, z, 0)
	c.engine.VelX = 0
	c.engine.VelZ = 0
	c.engine.AnimByte = 0 // idle
	for i := 0; i < 30; i++ { // 3 seconds of position updates
		time.Sleep(100 * time.Millisecond)
	}

	// Disconnect — server saves current position
	c.engine.SendDisconnect()
	if c.engine.State.Conn != nil {
		c.engine.State.Conn.Close()
	}
	time.Sleep(1 * time.Second)

	// Reconnect
	engine := game.NewEngine(
		c.engine.State.ServerIP, c.engine.State.GamePort,
		c.engine.State.AuthPort, c.engine.State.PatchPort,
		c.engine.State.Username, accountPass)
	engine.AutoConnect = true
	engine.PosOverride = true
	engine.PosOverrideX = x
	engine.PosOverrideY = y
	engine.PosOverrideZ = z
	c.engine = engine // main ticker goroutine follows c.engine

	// Wait for char select
	waitForPhase(engine, game.PhaseCharSelect, 30*time.Second)

	// Auto-select character (use index from flag)
	gs := engine.State
	charIdx := 2 // Mcgregor is index 2 (0-indexed) = char 3
	if charIdx >= len(gs.Characters) {
		charIdx = 0
	}
	log.Printf("GM teleport: selecting character %d (%s)", charIdx, gs.Characters[charIdx].Name)
	gs.SelectCharacter(charIdx)

	// Wait for world entry
	waitForPhase(engine, game.PhaseInWorld, 30*time.Second)
	log.Printf("GM teleport: in world at (%.0f, %.0f, %.0f)", gs.Player.X, gs.Player.Y, gs.Player.Z)
}

func (c *Crawler) harvestEntities() {
	gs := c.engine.State
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, ent := range gs.Entities {
		// Skip players
		if ent.IsPlayer || ent.NameColor == 2 {
			continue
		}
		// Skip empty/unnamed
		if ent.ObjectID == 0 || ent.Name == "" {
			continue
		}

		// Store by ObjectID (= serverID, unique per entity).
		// Always update position (mobs patrol, NPCs may move between sightings).
		if _, ok := c.seen[ent.ObjectID]; !ok {
			c.seen[ent.ObjectID] = &CrawledNPC{
				ObjectID:        ent.ObjectID,
				Name:            ent.Name,
				X:               ent.X,
				Y:               ent.Y,
				Z:               ent.Z,
				Facing:          ent.Facing,
				ModelID:         ent.ModelID,
				Level:           ent.Level,
				NPCType:         ent.NPCType,
				Race:            ent.Race,
				Scale:           ent.Scale,
				PrimaryWeapon:   ent.PrimaryWeapon,
				SecondaryWeapon: ent.SecondaryWeapon,
				Shield:          ent.Shield,
				Chest:           ent.Chest,
				Bracer:          ent.Bracer,
				Gloves:          ent.Gloves,
				Legs:            ent.Legs,
				Boots:           ent.Boots,
				Helm:            ent.Helm,
				ChestTint:       ent.ChestTint,
				BracerTint:      ent.BracerTint,
				GlovesTint:      ent.GlovesTint,
				LegsTint:        ent.LegsTint,
				BootsTint:       ent.BootsTint,
				HelmTint:        ent.HelmTint,
				HairColor:       ent.HairColor,
				HairLength:      ent.HairLength,
				HairStyle:       ent.HairStyle,
				FaceOption:      ent.FaceOption,
				Robe:            ent.Robe,
				RobeTint:        ent.RobeTint,
				HP:              ent.HP,
			}
		} else {
			// Update position for existing entity (mobs patrol)
			existing := c.seen[ent.ObjectID]
			existing.X = ent.X
			existing.Y = ent.Y
			existing.Z = ent.Z
			existing.Facing = ent.Facing
		}
	}
}

// interactWithMerchants targets and interacts with any merchant NPCs we haven't visited yet.
func (c *Crawler) interactWithMerchants() {
	gs := c.engine.State

	for _, ent := range gs.Entities {
		if ent.ObjectID == 0 || ent.Name == "" {
			continue
		}
		// 0x8100 = merchant, 0x8101 = merchant variant
		if ent.NPCType != 0x8100 && ent.NPCType != 0x8101 {
			continue
		}
		if c.interacted[ent.ObjectID] {
			continue
		}
		c.interacted[ent.ObjectID] = true

		log.Printf("Interacting with merchant %q (0x%08X) at (%.0f,%.0f,%.0f)",
			ent.Name, ent.ObjectID, ent.X, ent.Y, ent.Z)

		// Move to NPC position first (must be in range)
		c.engine.SetPosition(ent.X, ent.Y, ent.Z, ent.Facing)
		time.Sleep(300 * time.Millisecond)

		// Target → ForcePositionFlush → Interact → MerchantDiag
		gs.Player.Target = ent.ObjectID
		c.engine.SendTarget(ent.ObjectID)
		c.engine.ForcePositionFlush()
		time.Sleep(300 * time.Millisecond)

		c.engine.SendInteract(ent.ObjectID)
		time.Sleep(1 * time.Second)

		c.engine.SendMerchantDiag(ent.ObjectID)
		time.Sleep(2 * time.Second) // wait for item list

		// Harvest merchant items
		c.mu.Lock()
		if len(gs.MerchantItems) > 0 {
			c.saveMerchantItems(ent.ObjectID, ent.Name, gs.MerchantItems)
			gs.MerchantItems = nil
			gs.MerchantOpen = false
		}
		c.mu.Unlock()
	}
}

func (c *Crawler) saveMerchantItems(npcObjID uint32, npcName string, items []game.ParsedItem) {
	tx, err := c.db.Begin()
	if err != nil {
		return
	}
	stmtItem, _ := tx.Prepare(`INSERT OR IGNORE INTO items (
		item_id, name, description, slot, equip_location, cost, icon,
		level_req, attack_type, weapon_damage, max_stack, max_hp,
		no_trade, no_rent, is_lore, is_craft, class_use, race_use,
		proc_anim, duration, stats
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	stmtLink, _ := tx.Prepare(`INSERT OR IGNORE INTO merchant_items (npc_id, item_id, cost) VALUES (?, ?, ?)`)
	if stmtItem == nil || stmtLink == nil {
		tx.Rollback()
		return
	}
	defer stmtItem.Close()
	defer stmtLink.Close()

	for _, item := range items {
		if item.ItemID <= 0 {
			continue
		}
		statsStr := ""
		for _, st := range item.Stats {
			if statsStr != "" {
				statsStr += ","
			}
			statsStr += fmt.Sprintf("%d=%d", st.ID, st.Value)
		}
		stmtItem.Exec(
			item.ItemID, item.Name, item.Desc, item.Slot, item.EquipLocation,
			item.ItemCost, item.ItemIcon, item.LevelReq, item.AttackType,
			item.WeaponDamage, item.MaxStack, item.MaxHP,
			item.NoTrade, item.Rent, item.IsLore, item.IsCraft,
			item.ClassUse, item.RaceUse, item.P4C, item.Durability, statsStr,
		)
		stmtLink.Exec(npcObjID, item.ItemID, item.ItemCost)
	}
	tx.Commit()
	log.Printf("Saved %d items from merchant %q", len(items), npcName)
}

func (c *Crawler) flushToDB() {
	c.mu.Lock()
	defer c.mu.Unlock()

	tx, err := c.db.Begin()
	if err != nil {
		log.Printf("DB begin error: %v", err)
		return
	}

	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO npcs (
		npc_id, npc_name, x_coord, y_coord, z_coord, facing,
		modelid, npc_level, npc_type, race, size,
		primary_weapon, secondary_weapon, shield,
		torso, forearm, gloves, legs, feet, head,
		torso_tint, forearm_tint, gloves_tint, legs_tint, feet_tint, head_tint,
		hair_color, hair_length, hair_style, face,
		robe_style, robe_tint, hp
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		log.Printf("DB prepare error: %v", err)
		tx.Rollback()
		return
	}
	defer stmt.Close()

	count := 0
	for _, npc := range c.seen {
		_, err := stmt.Exec(
			npc.ObjectID, npc.Name, npc.X, npc.Y, npc.Z, npc.Facing,
			npc.ModelID, npc.Level, npc.NPCType, npc.Race, npc.Scale,
			npc.PrimaryWeapon, npc.SecondaryWeapon, npc.Shield,
			npc.Chest, npc.Bracer, npc.Gloves, npc.Legs, npc.Boots, npc.Helm,
			npc.ChestTint, npc.BracerTint, npc.GlovesTint, npc.LegsTint, npc.BootsTint, npc.HelmTint,
			npc.HairColor, npc.HairLength, npc.HairStyle, npc.FaceOption,
			npc.Robe, npc.RobeTint, npc.HP,
		)
		if err != nil {
			log.Printf("DB insert error for %q: %v", npc.Name, err)
			continue
		}
		count++
	}

	if err := tx.Commit(); err != nil {
		log.Printf("DB commit error: %v", err)
		return
	}
	log.Printf("Flushed %d NPCs to database", count)
}

func (c *Crawler) dumpItems() {
	gs := c.engine.State
	if len(gs.ParsedItems) == 0 {
		return
	}

	tx, _ := c.db.Begin()
	stmt, _ := tx.Prepare(`INSERT OR IGNORE INTO items (
		item_id, name, description, slot, equip_location, cost, icon,
		level_req, attack_type, weapon_damage, max_stack, max_hp,
		no_trade, no_rent, is_lore, is_craft, class_use, race_use,
		proc_anim, duration, stats
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if stmt == nil {
		tx.Rollback()
		return
	}
	defer stmt.Close()

	count := 0
	for _, item := range gs.ParsedItems {
		if item.ItemID <= 0 {
			continue
		}
		statsStr := ""
		for _, st := range item.Stats {
			if statsStr != "" {
				statsStr += ","
			}
			statsStr += fmt.Sprintf("%d=%d", st.ID, st.Value)
		}
		stmt.Exec(
			item.ItemID, item.Name, item.Desc, item.Slot, item.EquipLocation,
			item.ItemCost, item.ItemIcon, item.LevelReq, item.AttackType,
			item.WeaponDamage, item.MaxStack, item.MaxHP,
			item.NoTrade, item.Rent, item.IsLore, item.IsCraft,
			item.ClassUse, item.RaceUse, item.P4C, item.Durability, statsStr,
		)
		count++
	}
	tx.Commit()
	log.Printf("Dumped %d items to database", count)
}

func (c *Crawler) dumpSpells() {
	gs := c.engine.State
	if len(gs.ParsedSpells) == 0 {
		return
	}

	tx, _ := c.db.Begin()
	stmt, _ := tx.Prepare(`INSERT OR IGNORE INTO spells (
		spell_id, name, ability_level, icon, cast_time, power, range_val, scope, recast
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if stmt == nil {
		tx.Rollback()
		return
	}
	defer stmt.Close()

	count := 0
	for _, sp := range gs.ParsedSpells {
		if sp.SpellID <= 0 {
			continue
		}
		stmt.Exec(
			sp.SpellID, sp.Name, sp.AbilityLevel, sp.Icon,
			sp.CastTime, sp.Power, sp.Range, sp.Scope, sp.Recast,
		)
		count++
	}
	tx.Commit()
	log.Printf("Dumped %d spells to database", count)
}

func initDB(db *sql.DB) {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS npcs (
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
	if err != nil {
		log.Fatalf("Failed to create table: %v", err)
	}

	db.Exec(`CREATE INDEX IF NOT EXISTS idx_npcs_name ON npcs(npc_name)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_npcs_level ON npcs(npc_level)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_npcs_type ON npcs(npc_type)`)

	db.Exec(`CREATE TABLE IF NOT EXISTS items (
		item_id INTEGER PRIMARY KEY,
		name TEXT, description TEXT,
		slot INTEGER, equip_location INTEGER,
		cost INTEGER, icon INTEGER,
		level_req INTEGER, attack_type INTEGER, weapon_damage INTEGER,
		max_stack INTEGER, max_hp INTEGER,
		no_trade INTEGER, no_rent INTEGER, is_lore INTEGER, is_craft INTEGER,
		class_use INTEGER, race_use INTEGER,
		proc_anim INTEGER, duration INTEGER,
		stats TEXT
	)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_items_name ON items(name)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_items_slot ON items(slot)`)

	db.Exec(`CREATE TABLE IF NOT EXISTS spells (
		spell_id INTEGER PRIMARY KEY,
		name TEXT,
		ability_level INTEGER,
		icon INTEGER,
		cast_time INTEGER,
		power INTEGER,
		range_val INTEGER,
		scope INTEGER,
		recast INTEGER
	)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_spells_name ON spells(name)`)

	db.Exec(`CREATE TABLE IF NOT EXISTS merchant_items (
		npc_id INTEGER,
		item_id INTEGER,
		cost INTEGER,
		PRIMARY KEY (npc_id, item_id)
	)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_merchant_items_npc ON merchant_items(npc_id)`)
}

func waitForPhase(engine *game.Engine, phase game.Phase, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if engine.State.Phase == phase {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	log.Fatalf("Timeout waiting for phase %d", phase)
}

// Progress tracking — saves completed column numbers to a JSON file.
// On restart, completed columns are skipped.

func (c *Crawler) loadProgress() map[int]bool {
	// Store progress in the SQLite DB itself
	c.db.Exec(`CREATE TABLE IF NOT EXISTS crawl_progress (
		col_id INTEGER PRIMARY KEY,
		completed_at TEXT
	)`)

	rows, err := c.db.Query(`SELECT col_id FROM crawl_progress`)
	if err != nil {
		return make(map[int]bool)
	}
	defer rows.Close()

	completed := make(map[int]bool)
	for rows.Next() {
		var col int
		rows.Scan(&col)
		completed[col] = true
	}
	if len(completed) > 0 {
		log.Printf("Crawler: resuming — %d columns already completed", len(completed))
	}
	return completed
}

func (c *Crawler) saveProgress(col int) {
	c.db.Exec(`INSERT OR REPLACE INTO crawl_progress (col_id, completed_at) VALUES (?, datetime('now'))`, col)
}
