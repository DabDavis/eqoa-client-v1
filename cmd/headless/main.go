package main

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/eqoa/client/internal/game"
)

// lastTargetID tracks the most recently targeted ObjectID (set by target command).
var lastTargetID uint32

func main() {
	serverIP := flag.String("server", "spire", "Server: 'spire' (166.113.93.253), 'sandstorm' (5.161.217.188), or an IP address")
	gamePort := flag.Int("game-port", 10070, "Game UDP port")
	authPort := flag.Int("auth-port", 9735, "Auth TCP port")
	patchPort := flag.Int("patch-port", 7000, "Patch HTTP port")
	username := flag.String("user", "dab", "Username")
	password := flag.String("password", "REDACTED", "Password")
	autoConnect := flag.Bool("auto-connect", true, "Auto-connect to first game server")
	autoSelect := flag.Int("auto-select", 0, "Auto-select character N (1-indexed, 0=disabled)")
	assetsDir := flag.String("assets", "", "Assets directory for SPELLFX.CSF/ESF (optional)")
	cmdPortFlag := flag.Int("cmd-port", 7778, "TCP command port for agent control")
	flag.Parse()

	// Resolve named server presets
	switch strings.ToLower(*serverIP) {
	case "spire", "local":
		*serverIP = "166.113.93.253"
	case "sandstorm", "eqoa.live":
		*serverIP = "5.161.217.188"
	}

	// pprof server (http://localhost:6061/debug/pprof/)
	go func() { log.Println(http.ListenAndServe("localhost:6061", nil)) }()

	log.Printf("EQOA Headless Client starting")
	log.Printf("Server: %s (game=%d, auth=%d, patch=%d)", *serverIP, *gamePort, *authPort, *patchPort)
	log.Printf("Username: %s", *username)

	engine := game.NewEngine(*serverIP, *gamePort, *authPort, *patchPort, *username, *password)
	engine.AutoConnect = *autoConnect
	engine.AssetsDir = *assetsDir

	// Auto-select character when list arrives
	if *autoSelect > 0 {
		charIdx := *autoSelect - 1
		go func() {
			for {
				time.Sleep(200 * time.Millisecond)
				gs := engine.State
				if gs.Phase == game.PhaseCharSelect && len(gs.Characters) > charIdx {
					log.Printf("Auto-selecting character %d: %s", *autoSelect, gs.Characters[charIdx].Name)
					gs.SelectCharacter(charIdx)
					// CharacterInWorld is sent automatically by handleMemoryDump
					// in state.go after the server's MemoryDump response is fully
					// processed. Sending it early confuses Sandstorm and prevents
					// entity state channels from being delivered.
					return
				}
			}
		}()
	}

	// Start tick loop (100ms interval) — also monitors combat/state events
	ticker := time.NewTicker(100 * time.Millisecond)
	var lastDICount, lastCCCount int
	var lastCombatLog time.Time
	go func() {
		for range ticker.C {
			engine.Tick()

			// Live combat monitor — prints DamageIndication, CastingCombat, and
			// state diffs to stdout so they scroll in the interactive console.
			gs := engine.State
			if gs == nil || gs.Phase != game.PhaseInWorld {
				continue
			}

			// Print new floating texts (damage/heal numbers) as they appear
			for i := lastDICount; i < len(gs.FloatingTexts); i++ {
				ft := gs.FloatingTexts[i]
				fmt.Printf("  >> %s at (%.0f, %.0f, %.0f)\n", ft.Text, ft.X, ft.Y, ft.Z)
			}
			lastDICount = len(gs.FloatingTexts)

			// Periodic combat status when in combat
			if gs.Player.AttackCount > 0 && time.Since(lastCombatLog) > 2*time.Second {
				lastCombatLog = time.Now()
				fmt.Printf("  [combat] attacks=%d target=0x%08X fxHash=0x%08X\n",
					gs.Player.AttackCount, engine.TargetObjectID, gs.CombatAttackFXHash)
			}

			// CastingCombat count tracking
			_ = lastCCCount
		}
	}()

	// Print opcode stats on Ctrl+C
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		game.PrintOpcodeStats()
		os.Exit(0)
	}()

	fmt.Println("EQOA Headless Client — type 'help' for commands")

	// TCP command port: allows agents to send commands via `nc localhost 7778`
	// while the headless runs in the background. Each connection is a session.
	cmdPort := *cmdPortFlag
	go func() {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", cmdPort))
		if err != nil {
			fmt.Fprintf(os.Stderr, "CMD port %d unavailable: %v\n", cmdPort, err)
			return
		}
		fmt.Fprintf(os.Stderr, "CMD port listening on 127.0.0.1:%d\n", cmdPort)
		for {
			conn, err := ln.Accept()
			if err != nil {
				continue
			}
			go func(c net.Conn) {
				defer c.Close()
				scanner := bufio.NewScanner(c)
				for scanner.Scan() {
					line := strings.TrimSpace(scanner.Text())
					if line == "" {
						continue
					}
					fmt.Fprintf(c, "> %s\n", line)
					// Capture stdout during command by temporarily redirecting
					oldStdout := os.Stdout
					r, w, _ := os.Pipe()
					os.Stdout = w
					handleCommand(engine, line)
					w.Close()
					os.Stdout = oldStdout
					var buf [4096]byte
					n, _ := r.Read(buf[:])
					r.Close()
					if n > 0 {
						c.Write(buf[:n])
					}
				}
			}(conn)
		}
	}()

	// Stdin REPL (interactive or piped)
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		handleCommand(engine, line)
	}

	// If stdin closes (piped input), keep running so the tick loop continues
	select {}
}

func handleCommand(engine *game.Engine, line string) {
	parts := strings.Fields(line)
	cmd := strings.ToLower(parts[0])

	switch cmd {
	case "help":
		fmt.Println("Commands:")
		fmt.Println("  status          — Show current phase, player info")
		fmt.Println("  servers         — List available game servers")
		fmt.Println("  connect <n>     — Connect to server N (1-indexed)")
		fmt.Println("  chars           — List characters")
		fmt.Println("  select <n>      — Select character N (1-indexed)")
		fmt.Println("  enter           — Send CharacterInWorld")
		fmt.Println("  entities        — List nearby entities")
		fmt.Println("  near [range]    — List entities sorted by distance (default range=50)")
		fmt.Println("  pos             — Show player position")
		fmt.Println("  walk <dir>      — Walk: n/s/e/w/ne/nw/se/sw (20 units)")
		fmt.Println("  move <dx> <dz>  — Move by delta X, Z")
		fmt.Println("  tp <x> <y> <z>  — Teleport to absolute position")
		fmt.Println("  goto <name>     — Walk toward NPC by name (substring match)")
		fmt.Println("  follow <name>   — Continuously follow a player/NPC")
		fmt.Println("  stop            — Stop movement/follow")
		fmt.Println("  target <id|name> — Target entity by object ID or closest name match")
		fmt.Println("  tnear           — Target nearest entity")
		fmt.Println("  con             — Show current target info (name, level, distance)")
		fmt.Println("  attack          — Auto-attack current target")
		fmt.Println("  engage          — PS2 combat sequence: PetCmd(0x05) + Target + 0x40 flush")
		fmt.Println("  fight [slot] [count] [delay_ms] — Full combat test with proper timing")
		fmt.Println("  cast <name|id>  — Cast spell by name or SpellID on current target")
		fmt.Println("  cast raw <N>    — Send raw ability byte N (for pcap correlation)")
		fmt.Println("  interact        — Interact with current target (NPC dialogue)")
		fmt.Println("  option <n>      — Select dialogue option N (0-indexed, 255=cancel)")
		fmt.Println("  dialogue        — Re-display current dialogue/options")
		fmt.Println("  say <message>   — Send chat message")
		fmt.Println("  inventory       — List inventory and bank items")
		fmt.Println("  spells          — List known spells (with AddedOrder)")
		fmt.Println("  skills          — Show craft/weapon skill levels")
		fmt.Println("  cm              — Show Combat Mastery stats")
		fmt.Println("  dump            — Write parsed items/spells/CM to JSON files")
		fmt.Println("  hotbar          — Show hotbar slot assignments")
		fmt.Println("  hotbar set <slot> <name> — Assign spell to hotbar slot (0-9)")
		fmt.Println("  hotbar clear <slot>      — Clear a hotbar slot")
		fmt.Println("  memorize <idx> <slot>    — Memorize spell to hotbar (server, idx=spell list index)")
		fmt.Println("  unmemorize <slot>        — Remove spell from hotbar slot (server)")
		fmt.Println("  clearbar                 — Clear all spells from hotbar (server)")
		fmt.Println("  shout <message> — Send shout message")
		fmt.Println("  gsay <message>  — Send group chat message")
		fmt.Println("  ginvite <name>  — Invite player to group (by name)")
		fmt.Println("  gaccept         — Accept pending group invite")
		fmt.Println("  gdecline        — Decline pending group invite")
		fmt.Println("  gleave          — Leave current group")
		fmt.Println("  gboot <name>    — Boot member from group")
		fmt.Println("  group           — Show group members")
		fmt.Println("  guildsay <msg>  — Send guild chat message")
		fmt.Println("  guildcreate <n> — Create guild with name")
		fmt.Println("  guildleave      — Leave current guild")
		fmt.Println("  guildmotd <msg> — Set guild MOTD")
		fmt.Println("  guildinvite <n> — Invite player to guild")
		fmt.Println("  guildkick <n>   — Kick member from guild")
		fmt.Println("  who             — Request who list")
		fmt.Println("  lfg             — Toggle LFG registration")
		fmt.Println("  create <name> <race> <class> [gender] — Create character")
		fmt.Println("  spellfx <hex>   — Dump spell effect by DictID (e.g. spellfx 0xDA9BEA13)")
		fmt.Println("  spellfx-stats   — Show loaded spell effect / particle counts")
		fmt.Println("  effects         — Show active spell effects currently playing")
		fmt.Println("  track <name> [dur] [ms] — Track entity interpolation in real-time")
		fmt.Println("  watch           — Toggle entity movement debug logging")
		fmt.Println("  statediff       — Toggle self state channel byte-diff (combat RE)")
		fmt.Println("  interp          — Run 30s interpolation simulation with snap detection")
		fmt.Println("  stats           — Print opcode statistics")
		fmt.Println("  sleep <seconds> — Wait N seconds (for scripted input)")
		fmt.Println("  disconnect      — Send clean disconnect to server")
		fmt.Println("  quit            — Disconnect and exit")

	case "sleep":
		secs := 1.0
		if len(parts) >= 2 {
			if v, err := strconv.ParseFloat(parts[1], 64); err == nil && v > 0 {
				secs = v
			}
		}
		time.Sleep(time.Duration(secs * float64(time.Second)))

	case "status":
		gs := engine.State
		fmt.Printf("Phase:    %s\n", gs.Phase)
		fmt.Printf("Status:   %s\n", gs.StatusMsg)
		fmt.Printf("Server:   %s:%d\n", gs.ServerIP, gs.GamePort)
		fmt.Printf("User:     %s\n", gs.Username)
		if gs.PlayerObjectID != 0 {
			fmt.Printf("ObjectID: %d\n", gs.PlayerObjectID)
		}
		if gs.Player.Name != "" {
			fmt.Printf("Player:   %s (Level %d)\n", gs.Player.Name, gs.Player.Level)
			fmt.Printf("Position: (%.1f, %.1f, %.1f)\n", gs.Player.X, gs.Player.Y, gs.Player.Z)
		}
		fmt.Printf("Entities: %d\n", len(gs.Entities))

	case "servers":
		gs := engine.State
		if len(gs.Servers) == 0 {
			fmt.Println("No servers received yet.")
			return
		}
		for i, srv := range gs.Servers {
			fmt.Printf("  [%d] %s (%s:%d) endpoint=0x%04X\n", i+1, srv.Name, srv.IP, srv.Port, srv.EndpointID)
		}

	case "connect":
		if len(parts) < 2 {
			fmt.Println("Usage: connect <server-number>")
			return
		}
		n, err := strconv.Atoi(parts[1])
		if err != nil || n < 1 {
			fmt.Println("Invalid server number.")
			return
		}
		gs := engine.State
		if n > len(gs.Servers) {
			fmt.Printf("Only %d server(s) available.\n", len(gs.Servers))
			return
		}
		fmt.Printf("Connecting to server %d: %s\n", n, gs.Servers[n-1].Name)
		go engine.ConnectToGameServer(n - 1)

	case "chars":
		gs := engine.State
		if len(gs.Characters) == 0 {
			fmt.Println("No characters received yet.")
			return
		}
		raceNames := []string{"Human", "Elf", "Dark Elf", "Gnome", "Dwarf", "Troll", "Barbarian", "Halfling", "Erudite", "Ogre"}
		classNames := []string{"Warrior", "Ranger", "Paladin", "Shadow Knight", "Monk", "Bard", "Rogue", "Druid", "Shaman", "Cleric", "Magician", "Necromancer", "Enchanter", "Wizard", "Alchemist"}
		for i, ch := range gs.Characters {
			raceName := "Unknown"
			if ch.Race >= 0 && ch.Race < len(raceNames) {
				raceName = raceNames[ch.Race]
			}
			className := "Unknown"
			if ch.Class >= 0 && ch.Class < len(classNames) {
				className = classNames[ch.Class]
			}
			fmt.Printf("  [%d] %s — Level %d %s %s\n", i+1, ch.Name, ch.Level, raceName, className)
		}

	case "select":
		if len(parts) < 2 {
			fmt.Println("Usage: select <character-number>")
			return
		}
		n, err := strconv.Atoi(parts[1])
		if err != nil || n < 1 {
			fmt.Println("Invalid character number.")
			return
		}
		gs := engine.State
		if n > len(gs.Characters) {
			fmt.Printf("Only %d character(s) available.\n", len(gs.Characters))
			return
		}
		fmt.Printf("Selecting character %d: %s\n", n, gs.Characters[n-1].Name)
		gs.SelectCharacter(n - 1)

	case "enter":
		gs := engine.State
		if gs.Conn == nil {
			fmt.Println("Not connected.")
			return
		}
		gs.SendCharacterInWorld()
		fmt.Println("Sent CharacterInWorld.")

	case "entities":
		gs := engine.State
		if len(gs.Entities) == 0 {
			fmt.Println("No nearby entities.")
			return
		}
		for _, ent := range gs.Entities {
			kind := "NPC"
			if ent.IsPlayer {
				kind = "Player"
			}
			npcStr := ""
			if ent.NPCType != 0 {
				npcStr = fmt.Sprintf(" npc=0x%04X", ent.NPCType)
			}
			fmt.Printf("  [%d] ch=%d %s %s pos=(%.1f, %.1f, %.1f)%s\n",
				ent.ObjectID, ent.Channel, kind, ent.Name, ent.X, ent.Y, ent.Z, npcStr)
		}

	case "pos":
		gs := engine.State
		if gs.Player.Name == "" {
			fmt.Println("Not in world yet.")
			return
		}
		fmt.Printf("Player: %s (Level %d)\n", gs.Player.Name, gs.Player.Level)
		fmt.Printf("Position: (%.1f, %.1f, %.1f) facing=%.2f\n",
			gs.Player.X, gs.Player.Y, gs.Player.Z, gs.Player.Facing)

	case "walk":
		gs := engine.State
		if gs.Player.Name == "" {
			fmt.Println("Not in world yet.")
			return
		}
		dir := "n"
		if len(parts) >= 2 {
			dir = strings.ToLower(parts[1])
		}
		step := float32(20.0)
		if len(parts) >= 3 {
			if v, err := strconv.ParseFloat(parts[2], 32); err == nil {
				step = float32(v)
			}
		}
		dx, dz := float32(0), float32(0)
		var facing float32
		switch dir {
		case "n":
			dz = step
			facing = 0
		case "s":
			dz = -step
			facing = math.Pi
		case "e":
			dx = step
			facing = math.Pi / 2
		case "w":
			dx = -step
			facing = 3 * math.Pi / 2
		case "ne":
			dx = step * 0.707
			dz = step * 0.707
			facing = math.Pi / 4
		case "nw":
			dx = -step * 0.707
			dz = step * 0.707
			facing = 7 * math.Pi / 4
		case "se":
			dx = step * 0.707
			dz = -step * 0.707
			facing = 3 * math.Pi / 4
		case "sw":
			dx = -step * 0.707
			dz = -step * 0.707
			facing = 5 * math.Pi / 4
		default:
			fmt.Println("Unknown direction. Use: n/s/e/w/ne/nw/se/sw")
			return
		}
		newX := gs.Player.X + dx
		newZ := gs.Player.Z + dz
		engine.SetPosition(newX, gs.Player.Y, newZ, facing)
		fmt.Printf("Walked %s → (%.1f, %.1f, %.1f)\n", dir, newX, gs.Player.Y, newZ)

	case "move":
		gs := engine.State
		if gs.Player.Name == "" {
			fmt.Println("Not in world yet.")
			return
		}
		if len(parts) < 3 {
			fmt.Println("Usage: move <dx> <dz>")
			return
		}
		dx, err1 := strconv.ParseFloat(parts[1], 32)
		dz, err2 := strconv.ParseFloat(parts[2], 32)
		if err1 != nil || err2 != nil {
			fmt.Println("Invalid coordinates.")
			return
		}
		newX := gs.Player.X + float32(dx)
		newZ := gs.Player.Z + float32(dz)
		facing := float32(math.Atan2(dx, dz))
		if facing < 0 {
			facing += 2 * math.Pi
		}
		engine.SetPosition(newX, gs.Player.Y, newZ, facing)
		fmt.Printf("Moved → (%.1f, %.1f, %.1f)\n", newX, gs.Player.Y, newZ)

	case "tp":
		gs := engine.State
		if gs.Conn == nil {
			fmt.Println("Not connected.")
			return
		}
		if len(parts) < 4 {
			fmt.Println("Usage: tp <x> <y> <z>")
			return
		}
		x, err1 := strconv.ParseFloat(parts[1], 32)
		y, err2 := strconv.ParseFloat(parts[2], 32)
		z, err3 := strconv.ParseFloat(parts[3], 32)
		if err1 != nil || err2 != nil || err3 != nil {
			fmt.Println("Invalid coordinates.")
			return
		}
		engine.SetPosition(float32(x), float32(y), float32(z), gs.Player.Facing)
		fmt.Printf("Teleported → (%.1f, %.1f, %.1f)\n", x, y, z)

	case "goto":
		gs := engine.State
		if gs.Player.Name == "" {
			fmt.Println("Not in world yet.")
			return
		}
		if len(parts) < 2 {
			fmt.Println("Usage: goto <npc name>")
			return
		}
		search := strings.ToLower(strings.Join(parts[1:], " "))
		var found *game.Entity
		for _, ent := range gs.Entities {
			if strings.Contains(strings.ToLower(ent.Name), search) {
				found = ent
				break
			}
		}
		if found == nil {
			fmt.Printf("No entity matching '%s' found.\n", search)
			return
		}
		dx := found.X - gs.Player.X
		dz := found.Z - gs.Player.Z
		dist := float32(math.Sqrt(float64(dx*dx + dz*dz)))
		fmt.Printf("Moving to %s [%d] at (%.1f, %.1f, %.1f) — distance %.1f\n",
			found.Name, found.ObjectID, found.X, found.Y, found.Z, dist)
		engine.SetMoveTarget(found.X, found.Y, found.Z)

	case "follow":
		gs := engine.State
		if gs.Player.Name == "" {
			fmt.Println("Not in world yet.")
			return
		}
		if len(parts) < 2 {
			fmt.Println("Usage: follow <name>")
			return
		}
		search := strings.ToLower(strings.Join(parts[1:], " "))
		var found *game.Entity
		for _, ent := range gs.Entities {
			if strings.Contains(strings.ToLower(ent.Name), search) {
				found = ent
				break
			}
		}
		if found == nil {
			fmt.Printf("No entity matching '%s' found.\n", search)
			return
		}
		fmt.Printf("Following %s [%d]\n", found.Name, found.ObjectID)
		engine.Follow(found.ObjectID, 5.0)

	case "stop":
		engine.StopMovement()
		fmt.Println("Movement stopped.")

	case "target":
		if len(parts) < 2 {
			fmt.Println("Usage: target <objectID|name>")
			return
		}
		// Try numeric ObjectID first
		if id, err := strconv.ParseUint(parts[1], 10, 32); err == nil {
			lastTargetID = uint32(id)
		} else {
			// Name substring match
			search := strings.ToLower(strings.Join(parts[1:], " "))
			gs := engine.State
			var bestEnt *game.Entity
			bestDist := float64(math.MaxFloat64)
			for _, ent := range gs.Entities {
				if strings.Contains(strings.ToLower(ent.Name), search) {
					dx := float64(ent.X - gs.Player.X)
					dy := float64(ent.Y - gs.Player.Y)
					dz := float64(ent.Z - gs.Player.Z)
					d := math.Sqrt(dx*dx + dy*dy + dz*dz)
					if d < bestDist {
						bestDist = d
						bestEnt = ent
					}
				}
			}
			if bestEnt == nil {
				fmt.Printf("No entity matching '%s' found.\n", search)
				return
			}
			lastTargetID = bestEnt.ObjectID
			fmt.Printf("Matched: %s [%d] dist=%.1f\n", bestEnt.Name, bestEnt.ObjectID, bestDist)
		}
		engine.TargetObjectID = lastTargetID
		engine.SendTarget(lastTargetID)
		engine.ForcePositionFlush() // ensure 0x40 with target propagates immediately
		fmt.Printf("Targeting objectID %d (0x40 flushed)\n", lastTargetID)

	case "tnear":
		gs := engine.State
		if len(gs.Entities) == 0 {
			fmt.Println("No nearby entities.")
			return
		}
		var bestEnt *game.Entity
		bestDist := float64(math.MaxFloat64)
		for _, ent := range gs.Entities {
			dx := float64(ent.X - gs.Player.X)
			dy := float64(ent.Y - gs.Player.Y)
			dz := float64(ent.Z - gs.Player.Z)
			d := math.Sqrt(dx*dx + dy*dy + dz*dz)
			if d < bestDist {
				bestDist = d
				bestEnt = ent
			}
		}
		lastTargetID = bestEnt.ObjectID
		engine.TargetObjectID = lastTargetID
		engine.SendTarget(lastTargetID)
		engine.ForcePositionFlush() // ensure 0x40 with target propagates immediately
		fmt.Printf("Targeting nearest: %s [%d] dist=%.1f (0x40 flushed)\n", bestEnt.Name, bestEnt.ObjectID, bestDist)

	case "say":
		if len(parts) < 2 {
			fmt.Println("Usage: say <message>")
			return
		}
		text := strings.Join(parts[1:], " ")
		engine.SendSay(text)
		fmt.Printf("Said: %s\n", text)

	case "inventory", "inv":
		gs := engine.State
		if len(gs.ParsedItems) == 0 {
			fmt.Println("No items yet. Wait for MemoryDump after world entry.")
			return
		}
		slotNames := map[int64]string{
			1: "Head", 2: "Face", 3: "Shoulders", 4: "Chest",
			5: "Chest", 6: "Back", 7: "Bracers", 8: "Gloves",
			9: "Belt", 10: "Legs", 11: "Boots", 12: "Ring",
			13: "Ring", 14: "Primary", 15: "Secondary", 16: "Range",
			17: "Ammo", 18: "Charm",
		}
		// Show equipped items first
		fmt.Println("=== Equipped ===")
		equipped := 0
		for _, item := range gs.ParsedItems[:gs.InventoryItemCount] {
			if item.EquipLocation >= 0 {
				slotName := slotNames[item.Slot]
				if slotName == "" {
					slotName = fmt.Sprintf("slot%d", item.Slot)
				}
				fmt.Printf("  [%-10s] %s (id=%d lvl=%d invSlot=%d equipLoc=%d)\n", slotName, item.Name, item.ItemID, item.LevelReq, item.InvSlot, item.EquipLocation)
				equipped++
			}
		}
		if equipped == 0 {
			fmt.Println("  (none)")
		}
		// Inventory (unequipped)
		fmt.Printf("=== Inventory (%d items) ===\n", gs.InventoryItemCount)
		for i, item := range gs.ParsedItems[:gs.InventoryItemCount] {
			if item.EquipLocation < 0 {
				marker := ""
				if item.MaxStack > 1 {
					marker = fmt.Sprintf(" ×%d", item.StackLeft)
				}
				fmt.Printf("  [%2d] %s%s (id=%d lvl=%d)\n", i, item.Name, marker, item.ItemID, item.LevelReq)
			}
		}
		// Bank
		bankCount := len(gs.ParsedItems) - gs.InventoryItemCount
		if bankCount > 0 {
			fmt.Printf("=== Bank (%d items) ===\n", bankCount)
			for i, item := range gs.ParsedItems[gs.InventoryItemCount:] {
				marker := ""
				if item.MaxStack > 1 {
					marker = fmt.Sprintf(" ×%d", item.StackLeft)
				}
				fmt.Printf("  [%2d] %s%s (id=%d lvl=%d)\n", i, item.Name, marker, item.ItemID, item.LevelReq)
			}
		}

	case "buy":
		// buy <merchantSlot> [quantity]
		if len(parts) < 2 {
			fmt.Println("Usage: buy <merchantSlot> [quantity]")
			return
		}
		slot, _ := strconv.Atoi(parts[1])
		qty := 1
		if len(parts) >= 3 {
			qty, _ = strconv.Atoi(parts[2])
		}
		engine.SendMerchantBuy(slot, qty)

	case "buyraw":
		// buyraw <hex> — send raw MerchantBuy payload for exploit testing
		if len(parts) < 2 {
			fmt.Println("Usage: buyraw <hex bytes e.g. 08027FFFFFFF>")
			return
		}
		raw, err := hex.DecodeString(parts[1])
		if err != nil {
			fmt.Printf("Invalid hex: %v\n", err)
			return
		}
		engine.SendMerchantBuyRaw(raw)

	case "equip", "unequip":
		gs := engine.State
		if len(gs.ParsedItems) == 0 {
			fmt.Println("No items yet. Wait for MemoryDump after world entry.")
			return
		}
		if len(parts) < 2 {
			fmt.Printf("Usage: %s <invSlot>\n", parts[0])
			return
		}
		slot, err := strconv.Atoi(parts[1])
		if err != nil || slot < 0 || slot >= len(gs.ParsedItems) {
			fmt.Printf("Invalid slot %q (0-%d)\n", parts[1], len(gs.ParsedItems)-1)
			return
		}
		item := gs.ParsedItems[slot]
		fmt.Printf("InteractItem: slot=%d name=%q equipLoc=%d\n", slot, item.Name, item.EquipLocation)
		engine.SendInteractItem(slot)

	case "weaponbar", "wb":
		// Equipment hotbar swap: PS2 sends PrepareItem (0x000A) with slot + action byte
		if len(parts) < 3 {
			fmt.Println("Usage: weaponbar <invSlot> <action>")
			fmt.Println("  action 0 = unequip to hotbar")
			fmt.Println("  action 2 = equip from hotbar")
			fmt.Println("  action 3 = swap hotbar slot")
			return
		}
		slot, err := strconv.Atoi(parts[1])
		if err != nil {
			fmt.Printf("Invalid slot: %v\n", err)
			return
		}
		action, err := strconv.Atoi(parts[2])
		if err != nil || action < 0 || action > 3 {
			fmt.Printf("Invalid action: %v (0-3)\n", err)
			return
		}
		engine.SendPrepareItem(slot, byte(action))
		fmt.Printf("PrepareItem: slot=%d action=%d\n", slot, action)

	case "spells":
		gs := engine.State
		if len(gs.ParsedSpells) == 0 {
			fmt.Println("No spells yet. Wait for MemoryDump after world entry.")
			return
		}
		fmt.Printf("=== Spells (%d) ===\n", len(gs.ParsedSpells))
		for _, sp := range gs.ParsedSpells {
			rangeStr := ""
			if sp.Range > 0 {
				rangeStr = fmt.Sprintf(" range=%d", sp.Range)
			}
			castStr := ""
			if sp.CastTime > 0 {
				castStr = fmt.Sprintf(" cast=%d", sp.CastTime)
			}
			bar := "  "
			if sp.OnHotBar != 0 {
				bar = fmt.Sprintf("*%d", sp.WhereOnHotBar)
			}
			fmt.Printf("  [%4d] %-30s bar=%-3s ability=%-2d added=%-2d power=%-3d%s%s\n",
				sp.SpellID, sp.Name, bar, sp.AbilityLevel, sp.AddedOrder, sp.Power, castStr, rangeStr)
			if sp.Desc != "" {
				fmt.Printf("         %s\n", sp.Desc)
			}
		}

	case "skills":
		gs := engine.State
		if gs.ParsedCM == nil {
			fmt.Println("No skill data yet. Wait for MemoryDump after world entry.")
			return
		}
		cm := gs.ParsedCM
		fmt.Println("=== Skills (26 int32 values) ===")
		// Skills are 26 int32 values. The exact mapping is not fully known,
		// but they contain craft/weapon skill levels and ranks.
		for i := 0; i < 26; i++ {
			if cm.Skills[i] != 0 {
				fmt.Printf("  skill[%2d] = %d\n", i, cm.Skills[i])
			}
		}

	case "cm":
		gs := engine.State
		if gs.ParsedCM == nil {
			fmt.Println("No CM data yet. Wait for MemoryDump after world entry.")
			return
		}
		cm := gs.ParsedCM
		fmt.Printf("=== Combat Mastery ===\n")
		fmt.Printf("  Speed:       %.2f\n", cm.Speed)
		fmt.Printf("  Unspent CMs: %d\n", cm.UnspentCMs)
		fmt.Printf("  Spent CMs:   %d\n", cm.SpentCMs)
		fmt.Printf("  CM %%:        %d%%\n", cm.CMPercentage)

		// Show non-zero stat additions
		statAdds := []struct {
			name string
			val  int64
		}{
			{"Str", cm.StrAdd}, {"Sta", cm.StaAdd}, {"Agi", cm.AgiAdd},
			{"Dex", cm.DexAdd}, {"Wis", cm.WisAdd}, {"Int", cm.IntAdd}, {"Cha", cm.ChaAdd},
			{"HP", cm.HPAdd}, {"Power", cm.PowerAdd}, {"AC", cm.ACAdd},
			{"HoT", cm.HoT}, {"PoT", cm.PoT},
			{"OFF", cm.OFFMod}, {"DEF", cm.DEFMod}, {"HP Factor", cm.HPFactor},
			{"FR", cm.FRAdd}, {"LR", cm.LRAdd}, {"CR", cm.CRAdd},
			{"AR", cm.ARAdd}, {"PR", cm.PRAdd}, {"DR", cm.DRAdd},
		}
		hasAdds := false
		for _, s := range statAdds {
			if s.val != 0 {
				if !hasAdds {
					fmt.Println("  --- Stat Additions ---")
					hasAdds = true
				}
				fmt.Printf("  +%-10s %d\n", s.name, s.val)
			}
		}

		// Show non-zero stat maxes
		statMaxes := []struct {
			name string
			val  int64
		}{
			{"Str", cm.StrMax}, {"Sta", cm.StaMax}, {"Agi", cm.AgiMax},
			{"Dex", cm.DexMax}, {"Wis", cm.WisMax}, {"Int", cm.IntMax}, {"Cha", cm.ChaMax},
		}
		hasMaxes := false
		for _, s := range statMaxes {
			if s.val != 0 {
				if !hasMaxes {
					fmt.Println("  --- CM Stat Maximums ---")
					hasMaxes = true
				}
				fmt.Printf("  %-10s max=%d\n", s.name, s.val)
			}
		}
		if !hasAdds && !hasMaxes {
			fmt.Println("  (no CM investments)")
		}

	case "playerstats":
		p := &engine.State.Player
		fmt.Printf("=== Player Stats (from stat channel 0x42) ===\n")
		fmt.Printf("  STR=%d  STA=%d  AGI=%d  DEX=%d  WIS=%d  INT=%d  CHA=%d\n",
			p.Str, p.Sta, p.Agi, p.Dex, p.Wis, p.Int, p.Cha)
		fmt.Printf("  Base: STR=%d  STA=%d  AGI=%d  DEX=%d  WIS=%d  INT=%d  CHA=%d\n",
			p.BaseStr, p.BaseSta, p.BaseAgi, p.BaseDex, p.BaseWis, p.BaseInt, p.BaseCha)
		fmt.Printf("  MaxHP=%d  Power=%d/%d  AC=%d\n",
			p.MaxHPInt, p.PowerInt, p.MaxPowerInt, p.AC)
		fmt.Printf("  Resists: Poison=%d  Disease=%d  Fire=%d  Ice=%d  Lightning=%d  Arcane=%d\n",
			p.PoisonR, p.DiseaseR, p.FireR, p.ColdR, p.LightningR, p.ArcaneR)

	case "dump":
		gs := engine.State
		if len(gs.ParsedItems) == 0 && len(gs.ParsedSpells) == 0 {
			fmt.Println("No parsed items/spells yet. Wait for MemoryDump after world entry.")
			return
		}

		// Write items JSON
		itemsJSON, _ := json.MarshalIndent(gs.ParsedItems, "", "  ")
		os.WriteFile("/tmp/memdump_items.json", itemsJSON, 0644)

		// Write spells JSON
		spellsJSON, _ := json.MarshalIndent(gs.ParsedSpells, "", "  ")
		os.WriteFile("/tmp/memdump_spells.json", spellsJSON, 0644)

		// Write CM data JSON
		if gs.ParsedCM != nil {
			cmJSON, _ := json.MarshalIndent(gs.ParsedCM, "", "  ")
			os.WriteFile("/tmp/memdump_cm.json", cmJSON, 0644)
			fmt.Printf("Dumped CM data to /tmp/memdump_cm.json (speed=%.2f, spent=%d, pct=%d%%)\n",
				gs.ParsedCM.Speed, gs.ParsedCM.SpentCMs, gs.ParsedCM.CMPercentage)
		}

		fmt.Printf("Dumped %d items to /tmp/memdump_items.json\n", len(gs.ParsedItems))
		fmt.Printf("Dumped %d spells to /tmp/memdump_spells.json\n", len(gs.ParsedSpells))
		fmt.Println("Run: cd go-eqoa-server && go run ./cmd/extractdb/ — to import into eqoa.db")

	case "track":
		// Track a specific entity's interpolation state in real-time.
		// Usage: track <name|id> [duration_s] [interval_ms]
		// Prints: pos, target, velocity, gap every interval.
		// Also writes CSV to /tmp/track_<name>.csv
		gs := engine.State
		if len(parts) < 2 {
			fmt.Println("Usage: track <name|objectID> [duration_s] [interval_ms]")
			return
		}
		durationS := 30.0
		intervalMs := 100
		if len(parts) >= 3 {
			if v, err := strconv.ParseFloat(parts[2], 64); err == nil {
				durationS = v
			}
		}
		if len(parts) >= 4 {
			if v, err := strconv.Atoi(parts[3]); err == nil && v > 0 {
				intervalMs = v
			}
		}
		// Find entity by name or ID
		var trackEnt *game.Entity
		if id, err := strconv.ParseUint(parts[1], 10, 32); err == nil {
			trackEnt = gs.Entities[uint32(id)]
		} else {
			search := strings.ToLower(strings.Join(parts[1:2], " "))
			bestDist := float64(math.MaxFloat64)
			for _, ent := range gs.Entities {
				if strings.Contains(strings.ToLower(ent.Name), search) {
					dx := float64(ent.X - gs.Player.X)
					dz := float64(ent.Z - gs.Player.Z)
					d := math.Sqrt(dx*dx + dz*dz)
					if d < bestDist {
						bestDist = d
						trackEnt = ent
					}
				}
			}
		}
		if trackEnt == nil {
			fmt.Printf("No entity matching '%s' found.\n", parts[1])
			return
		}
		trackID := trackEnt.ObjectID
		trackName := trackEnt.Name
		fmt.Printf("Tracking %q [%d] for %.0fs at %dms intervals\n", trackName, trackID, durationS, intervalMs)
		fmt.Printf("Writing CSV to /tmp/track_%s.csv\n", strings.ReplaceAll(trackName, " ", "_"))

		// Enable debug logging for handler-level snap visibility
		gs.DebugEntityMovement = true

		go func() {
			csvPath := fmt.Sprintf("/tmp/track_%s.csv", strings.ReplaceAll(trackName, " ", "_"))
			f, _ := os.Create(csvPath)
			if f != nil {
				fmt.Fprintln(f, "time_ms,x,y,z,targetX,targetY,targetZ,velX,velZ,gap,anim,age_ms,updateInterval_ms")
			}

			start := time.Now()
			var prevX, prevZ float32
			firstFrame := true

			for time.Since(start).Seconds() < durationS {
				ent, ok := gs.Entities[trackID]
				if !ok {
					fmt.Printf("  [track] Entity %d despawned\n", trackID)
					break
				}

				dx := ent.TargetX - ent.X
				dz := ent.TargetZ - ent.Z
				gap := float32(math.Sqrt(float64(dx*dx + dz*dz)))
				ageMs := time.Since(ent.LastUpdate).Milliseconds()
				elapsed := time.Since(start).Milliseconds()
				intervalMsF := ent.UpdateInterval * 1000

				// Detect snap: large frame-to-frame displacement
				snapMarker := ""
				if !firstFrame {
					frameDX := ent.X - prevX
					frameDZ := ent.Z - prevZ
					frameDist := float32(math.Sqrt(float64(frameDX*frameDX + frameDZ*frameDZ)))
					velMag := float32(math.Sqrt(float64(ent.VelX*ent.VelX + ent.VelZ*ent.VelZ)))
					expectedMove := velMag * float32(intervalMs) / 1000.0
					if expectedMove > 0.1 && frameDist > expectedMove*3.0 {
						snapMarker = fmt.Sprintf(" <<< SNAP dist=%.2f expected=%.2f", frameDist, expectedMove)
					}
				}
				prevX, prevZ = ent.X, ent.Z
				firstFrame = false

				fmt.Printf("  [%5dms] pos=(%.2f,%.2f,%.2f) tgt=(%.2f,%.2f,%.2f) vel=(%.1f,%.1f) gap=%.2f anim=%d age=%dms%s\n",
					elapsed,
					ent.X, ent.Y, ent.Z,
					ent.TargetX, ent.TargetY, ent.TargetZ,
					ent.VelX, ent.VelZ, gap,
					ent.Animation, ageMs, snapMarker)

				if f != nil {
					fmt.Fprintf(f, "%d,%.3f,%.3f,%.3f,%.3f,%.3f,%.3f,%.3f,%.3f,%.3f,%d,%d,%.0f\n",
						elapsed, ent.X, ent.Y, ent.Z,
						ent.TargetX, ent.TargetY, ent.TargetZ,
						ent.VelX, ent.VelZ, gap,
						ent.Animation, ageMs, intervalMsF)
				}

				time.Sleep(time.Duration(intervalMs) * time.Millisecond)
			}
			if f != nil {
				f.Close()
			}
			gs.DebugEntityMovement = false
			fmt.Printf("Tracking %q complete. CSV at /tmp/track_%s.csv\n", trackName, strings.ReplaceAll(trackName, " ", "_"))
		}()

	case "watch":
		gs := engine.State
		gs.DebugEntityMovement = !gs.DebugEntityMovement
		if gs.DebugEntityMovement {
			fmt.Println("Entity movement tracking ON — check log output for MOVE lines")
		} else {
			fmt.Println("Entity movement tracking OFF")
		}

	case "statediff":
		gs := engine.State
		gs.DebugStateDiffs = !gs.DebugStateDiffs
		if gs.DebugStateDiffs {
			gs.ResetStateDiffBaseline()
			fmt.Println("State channel diff ON — shows byte-level changes on self channel 0x00")
			fmt.Println("  Use 'attack' to engage combat and watch which unknown bytes change")
			fmt.Println("  Known offsets: 0=counter, 1-4=objID, 5=type, 6-14=pos, 15=facing,")
			fmt.Println("  17=world, 26=hpFlag, 27=hp, 28-31=model, 36-39=scale, 40-42=vel,")
			fmt.Println("  58=anim, 59-62=target, 67-78=weapons, 93-98=armor, 99-104=tints,")
			fmt.Println("  105-110=hair/face/robe, 116-139=name, 140=level, 141-146=misc")
			fmt.Println("  UNKNOWN: 16,18-25,32-35,43-57,63-66,79-92,111-115,147-161")
		} else {
			fmt.Println("State channel diff OFF")
		}

	case "interp":
		// Start continuous interpolation simulation at 60fps in background.
		// Tracks per-entity: max speed between frames (snap detection),
		// gap distribution, and logs snap events (speed > 2x entity velocity).
		gs := engine.State
		gs.DebugEntityMovement = true
		fmt.Println("Starting continuous interpolation simulation (60fps, 30s)...")
		fmt.Println("Tracking: frame-to-frame speed spikes (snaps), gap distribution")
		fmt.Println("Detailed trace of first moving entity written to /tmp/interp_trace.csv")
		go func() {
			traceFile, _ := os.Create("/tmp/interp_trace.csv")
			if traceFile != nil {
				fmt.Fprintln(traceFile, "frame,time_s,name,x,y,z,targetX,targetY,targetZ,gap,frameSpeed,velMag,anim,age_ms")
			}
			var traceEntity uint32 // first moving entity we find
			dt := float32(1.0 / 60.0)
			// Track previous positions to detect speed spikes
			type prevPos struct {
				x, y, z float32
			}
			prev := make(map[uint32]prevPos)
			// Snap tracking
			type snapStats struct {
				name     string
				maxSpeed float32
				snapCount int // frames where speed > 2x entity vel
				totalFrames int
				maxGap   float32
			}
			stats := make(map[uint32]*snapStats)

			reportInterval := 5 * time.Second
			lastReport := time.Now()
			startTime := time.Now()

			for frame := 0; time.Since(startTime) < 30*time.Second; frame++ {
				gs.InterpolateEntities(dt)

				for id, ent := range gs.Entities {
					if !ent.Interpolating {
						continue
					}

					// Init stats
					if stats[id] == nil {
						stats[id] = &snapStats{name: ent.Name}
					}
					s := stats[id]
					s.totalFrames++

					// Compute gap to prediction
					dx := ent.TargetX - ent.X
					dy := ent.TargetY - ent.Y
					dz := ent.TargetZ - ent.Z
					gap := float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))
					if gap > s.maxGap {
						s.maxGap = gap
					}

					// Compute frame-to-frame speed
					velMag := float32(math.Sqrt(float64(ent.VelX*ent.VelX + ent.VelY*ent.VelY + ent.VelZ*ent.VelZ)))
					var frameSpeed float32
					if p, ok := prev[id]; ok {
						mx := ent.X - p.x
						my := ent.Y - p.y
						mz := ent.Z - p.z
						moveDist := float32(math.Sqrt(float64(mx*mx + my*my + mz*mz)))
						frameSpeed = moveDist / dt
						if frameSpeed > s.maxSpeed {
							s.maxSpeed = frameSpeed
						}
						if velMag > 0.5 && frameSpeed > velMag*2.5 {
							s.snapCount++
						}
					}
					prev[id] = prevPos{ent.X, ent.Y, ent.Z}

					// Per-frame trace for first moving entity
					if traceFile != nil {
						if traceEntity == 0 && velMag > 1.0 && ent.Animation > 0 {
							traceEntity = id
							log.Printf("Tracing entity %q (ObjectID=%d) for detailed analysis", ent.Name, id)
						}
						if id == traceEntity {
							age := time.Since(ent.LastUpdate).Milliseconds()
							fmt.Fprintf(traceFile, "%d,%.3f,%s,%.2f,%.2f,%.2f,%.2f,%.2f,%.2f,%.2f,%.1f,%.1f,%d,%d\n",
								frame, float64(frame)/60.0, ent.Name,
								ent.X, ent.Y, ent.Z,
								ent.TargetX, ent.TargetY, ent.TargetZ,
								gap, frameSpeed, velMag, ent.Animation, age)
						}
					}
				}

				// Periodic report
				if time.Since(lastReport) > reportInterval {
					lastReport = time.Now()
					fmt.Printf("=== INTERP REPORT (t=%.0fs) ===\n", time.Since(startTime).Seconds())
					for _, s := range stats {
						if s.totalFrames > 0 {
							snapPct := float64(s.snapCount) / float64(s.totalFrames) * 100
							fmt.Printf("  %-20s maxSpd=%.1f maxGap=%.1f snaps=%d/%d (%.1f%%)\n",
								s.name, s.maxSpeed, s.maxGap, s.snapCount, s.totalFrames, snapPct)
						}
					}
					// Reset per-period stats
					for _, s := range stats {
						s.maxSpeed = 0
						s.snapCount = 0
						s.totalFrames = 0
						s.maxGap = 0
					}
				}

				time.Sleep(time.Second / 60)
			}
			if traceFile != nil {
				traceFile.Close()
			}
			fmt.Println("=== INTERPOLATION SIMULATION COMPLETE ===")
		}()

	case "con":
		gs := engine.State
		if lastTargetID == 0 {
			fmt.Println("No target. Use: target <objectID>")
			return
		}
		ent, ok := gs.Entities[lastTargetID]
		if !ok {
			fmt.Printf("Target objectID %d not in entity list.\n", lastTargetID)
			return
		}
		dx := ent.X - gs.Player.X
		dy := ent.Y - gs.Player.Y
		dz := ent.Z - gs.Player.Z
		dist := float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))
		kind := "NPC"
		if ent.IsPlayer {
			kind = "Player"
		}
		fmt.Printf("Target: %s (%s) — ObjectID=%d Level=%d\n", ent.Name, kind, ent.ObjectID, ent.Level)
		fmt.Printf("  Position: (%.1f, %.1f, %.1f) Distance: %.1f\n", ent.X, ent.Y, ent.Z, dist)
		fmt.Printf("  HP=%d/%d Model=0x%X Channel=%d", ent.HP, ent.MaxHP, ent.ModelID, ent.Channel)
		if ent.NPCType != 0 {
			fmt.Printf(" NPCType=0x%04X", ent.NPCType)
		}
		fmt.Println()

	case "near":
		gs := engine.State
		if len(gs.Entities) == 0 {
			fmt.Println("No nearby entities.")
			return
		}
		maxRange := float64(50)
		if len(parts) >= 2 {
			if r, err := strconv.ParseFloat(parts[1], 64); err == nil {
				maxRange = r
			}
		}
		type entDist struct {
			ent  *game.Entity
			dist float64
		}
		var sorted []entDist
		for _, ent := range gs.Entities {
			dx := float64(ent.X - gs.Player.X)
			dy := float64(ent.Y - gs.Player.Y)
			dz := float64(ent.Z - gs.Player.Z)
			dist := math.Sqrt(dx*dx + dy*dy + dz*dz)
			if dist <= maxRange {
				sorted = append(sorted, entDist{ent, dist})
			}
		}
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].dist < sorted[j].dist })
		fmt.Printf("=== Entities within %.0f units (%d) ===\n", maxRange, len(sorted))
		for _, ed := range sorted {
			kind := "NPC"
			if ed.ent.IsPlayer {
				kind = "Player"
			}
			npcStr := ""
			if ed.ent.NPCType != 0 && !ed.ent.IsPlayer {
				switch ed.ent.NPCType {
				case 0x8100:
					npcStr = " [merchant]"
				case 0x8006:
					npcStr = " [quest]"
				case 0x8008:
					npcStr = " [guild]"
				default:
					npcStr = fmt.Sprintf(" [0x%04X]", ed.ent.NPCType)
				}
			}
			fmt.Printf("  [%d] %5.1f  %-6s %-25s Lv%-3d (%.0f, %.0f, %.0f)%s\n",
				ed.ent.ObjectID, ed.dist, kind, ed.ent.Name, ed.ent.Level,
				ed.ent.X, ed.ent.Y, ed.ent.Z, npcStr)
		}

	case "attack":
		if lastTargetID == 0 {
			fmt.Println("No target. Use: target <objectID>")
			return
		}
		engine.CombatEngaged = true // set byte 36 in 0x40 packet (auto-attack flag)
		engine.SendAttack(lastTargetID)
		engine.ForcePositionFlush() // send 0x40 (with new facing + target) + attack in one packet
		fmt.Printf("Attacking objectID %d\n", lastTargetID)

	case "engage":
		// PS2 combat sequence: PetCommand(0x05) → Target → then user casts/attacks
		if lastTargetID == 0 {
			fmt.Println("No target. Use: tnear or target <objectID> first")
			return
		}
		engine.SendPetCommand(0x05)
		engine.SendTarget(lastTargetID)
		engine.ForcePositionFlush() // ensure 0x40 with target propagates
		fmt.Printf("Engaged combat: PetCmd(0x05) + Target(%d) + 0x40 flush\n", lastTargetID)

	case "fight":
		// Full combat test: target → flush → wait → cast multiple slots with cooldown waits.
		// Usage: fight <slot> [count] [delay_ms]
		// Default: cast slot 6 (Clashing Verses) 3 times with 3000ms delay
		if lastTargetID == 0 {
			fmt.Println("No target. Use: tnear or target <objectID> first")
			return
		}
		slot := uint8(6) // default: Clashing Verses
		count := 3
		delayMs := 3000
		if len(parts) >= 2 {
			if n, err := strconv.ParseUint(parts[1], 10, 8); err == nil {
				slot = uint8(n)
			}
		}
		if len(parts) >= 3 {
			if n, err := strconv.Atoi(parts[2]); err == nil {
				count = n
			}
		}
		if len(parts) >= 4 {
			if n, err := strconv.Atoi(parts[3]); err == nil {
				delayMs = n
			}
		}
		fmt.Printf("Fight sequence: slot=%d count=%d delay=%dms target=0x%08X\n", slot, count, delayMs, lastTargetID)

		// Step 1: Target + flush 0x40 to propagate target to server
		engine.SendTarget(lastTargetID)
		engine.ForcePositionFlush()
		fmt.Println("  [1] Target + 0x40 flush sent")

		// Step 2: Wait for server to process 0x40 with target
		time.Sleep(500 * time.Millisecond)

		// Step 3: Send attack to start auto-attack
		engine.SendAttack(lastTargetID)
		fmt.Println("  [2] Attack sent")

		// Step 4: Cast spells with delays for cooldowns
		go func() {
			for i := 0; i < count; i++ {
				time.Sleep(time.Duration(delayMs) * time.Millisecond)
				engine.SendCast(slot, lastTargetID)
				fmt.Printf("  [%d] Cast slot %d sent\n", i+3, slot)
			}
			fmt.Println("Fight sequence complete")
		}()

	case "cast":
		gs := engine.State
		if len(parts) < 2 {
			fmt.Println("Usage: cast <name|spellID>  OR  cast raw <N>  OR  cast self <name>")
			return
		}
		targetID := lastTargetID
		// cast self <name> — self-cast (target=0xFFFFFFFF)
		if strings.ToLower(parts[1]) == "self" {
			targetID = 0xFFFFFFFF
			parts = append(parts[:1], parts[2:]...) // remove "self"
			if len(parts) < 2 {
				fmt.Println("Usage: cast self <name|spellID>")
				return
			}
		}
		if targetID == 0 {
			fmt.Println("No target. Use: target <objectID>  OR  cast self <name>")
			return
		}
		// cast raw <N> — send raw hotbar byte for pcap correlation
		if strings.ToLower(parts[1]) == "raw" {
			if len(parts) < 3 {
				fmt.Println("Usage: cast raw <hotbar-position>")
				return
			}
			n, err := strconv.ParseUint(parts[2], 10, 8)
			if err != nil {
				fmt.Println("Invalid hotbar position.")
				return
			}
			engine.SendCast(uint8(n), targetID)
			fmt.Printf("Cast raw hotbar=%d target=%d\n", n, targetID)
			return
		}
		// Try SpellID (numeric)
		if spellID, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
			for _, sp := range gs.ParsedSpells {
				if sp.SpellID == spellID {
					if sp.OnHotBar == 0 {
						fmt.Printf("WARNING: %s is NOT on hotbar — may cause kick!\n", sp.Name)
					}
					engine.SendCast(uint8(sp.WhereOnHotBar), targetID)
					fmt.Printf("Casting %s (id=%d hotbar=%d) on target %d\n",
						sp.Name, sp.SpellID, sp.WhereOnHotBar, targetID)
					return
				}
			}
			fmt.Printf("No spell with ID %d found.\n", spellID)
			return
		}
		// Name substring match
		search := strings.ToLower(strings.Join(parts[1:], " "))
		for _, sp := range gs.ParsedSpells {
			if strings.Contains(strings.ToLower(sp.Name), search) {
				if sp.OnHotBar == 0 {
					fmt.Printf("WARNING: %s is NOT on hotbar — may cause kick!\n", sp.Name)
				}
				engine.SendCast(uint8(sp.WhereOnHotBar), targetID)
				fmt.Printf("Casting %s (id=%d hotbar=%d added=%d) on target %d\n",
					sp.Name, sp.SpellID, sp.WhereOnHotBar, sp.AddedOrder, targetID)
				return
			}
		}
		fmt.Printf("No spell matching '%s' found.\n", search)

	case "interact":
		if lastTargetID == 0 {
			fmt.Println("No target. Use: target <objectID>")
			return
		}
		engine.SendInteract(lastTargetID)
		// Auto-send MerchantDiag for merchant NPCs
		if ent, ok := engine.State.Entities[lastTargetID]; ok && ent.IsNPC {
			if ent.NPCType&0x0100 != 0 || strings.HasPrefix(ent.Name, "Merchant") {
				engine.State.MerchantName = ent.Name
				engine.SendMerchantDiag(lastTargetID)
				fmt.Printf("Sent MerchantDiag for %s\n", ent.Name)
			}
		}
		fmt.Printf("Interacting with objectID %d\n", lastTargetID)

	case "shop":
		if lastTargetID == 0 {
			fmt.Println("No target. Use: target <objectID>")
			return
		}
		engine.SendMerchantDiag(lastTargetID)
		fmt.Printf("Sent MerchantDiag for objectID %d\n", lastTargetID)

	case "option":
		gs := engine.State
		if len(parts) < 2 {
			fmt.Println("Usage: option <N>  (0-indexed, or 255 to cancel)")
			return
		}
		n, err := strconv.ParseUint(parts[1], 10, 8)
		if err != nil {
			fmt.Println("Invalid option number.")
			return
		}
		if !gs.DialogueActive {
			fmt.Println("No active dialogue. Sending option anyway.")
		}
		engine.SendDialogueOption(gs.DialogueCounter, byte(n))
		fmt.Printf("Sent dialogue option %d (counter=%d)\n", n, gs.DialogueCounter)

	case "dialogue":
		gs := engine.State
		if !gs.DialogueActive {
			fmt.Println("No active dialogue.")
			return
		}
		fmt.Printf("--- Dialogue (counter=%d) ---\n%s\n", gs.DialogueCounter, gs.DialogueText)
		for i, opt := range gs.DialogueOptions {
			fmt.Printf("  [%d] %s\n", i, opt)
		}
		if len(gs.DialogueOptions) == 0 {
			fmt.Println("  (no options — simple dialogue)")
		}
		fmt.Println("----------------------------")

	case "spellfx":
		sfx := engine.State.SpellFX
		if sfx == nil {
			fmt.Println("SpellFX not loaded. Use --assets flag to specify assets directory.")
			return
		}
		if len(parts) < 2 {
			fmt.Println("Usage: spellfx <dictid>  (hex e.g. 0xDA9BEA13, or decimal)")
			return
		}
		raw, err := strconv.ParseUint(strings.TrimPrefix(parts[1], "0x"), 16, 32)
		if err != nil {
			// Try decimal
			raw, err = strconv.ParseUint(parts[1], 10, 32)
			if err != nil {
				fmt.Println("Invalid DictID. Use hex (0xABC) or decimal.")
				return
			}
		}
		sfx.DumpEffect(int32(raw))

	case "spellfx-stats":
		sfx := engine.State.SpellFX
		if sfx == nil {
			fmt.Println("SpellFX not loaded. Use --assets flag to specify assets directory.")
			return
		}
		effects, particles := sfx.Stats()
		fmt.Printf("SpellFX: %d effects, %d particle definitions\n", effects, particles)

	case "effects":
		sys := engine.SpellFX
		if sys == nil {
			fmt.Println("SpellFX system not initialized.")
			return
		}
		infos := sys.ActiveEffects()
		if len(infos) == 0 {
			fmt.Println("No active spell effects.")
			return
		}
		fmt.Printf("%d active spell effects:\n", len(infos))
		for i, info := range infos {
			fmt.Printf("  [%d] effect=0x%08X caster=0x%08X target=0x%08X elapsed=%dms lists=%d\n",
				i, info.EffectID, info.CasterObjID, info.TargetObjID, info.ElapsedMS, info.ListsActive)
		}

	case "hotbar":
		gs := engine.State
		if len(parts) >= 3 && parts[1] == "set" {
			// hotbar set <slot> <spellname> — assign spell to slot
			slotNum, err := strconv.Atoi(parts[2])
			if err != nil || slotNum < 0 || slotNum > 9 {
				fmt.Println("Usage: hotbar set <slot 0-9> <spell name>")
				return
			}
			if len(parts) < 4 {
				// Clear the slot
				gs.SetHotbarSlot(slotNum, nil)
				fmt.Printf("Cleared hotbar slot %d\n", slotNum)
				return
			}
			name := strings.Join(parts[3:], " ")
			name = strings.ToLower(name)
			var found *game.ParsedSpell
			for i := range gs.ParsedSpells {
				if strings.Contains(strings.ToLower(gs.ParsedSpells[i].Name), name) {
					found = &gs.ParsedSpells[i]
					break
				}
			}
			if found == nil {
				fmt.Printf("No spell matching %q\n", name)
				return
			}
			gs.SetHotbarSlot(slotNum, found)
			fmt.Printf("Set slot %d = %s\n", slotNum, found.Name)
		} else if len(parts) >= 2 && parts[1] == "clear" {
			// hotbar clear <slot>
			if len(parts) >= 3 {
				slotNum, err := strconv.Atoi(parts[2])
				if err == nil && slotNum >= 0 && slotNum < 10 {
					gs.SetHotbarSlot(slotNum, nil)
					fmt.Printf("Cleared hotbar slot %d\n", slotNum)
					return
				}
			}
			fmt.Println("Usage: hotbar clear <slot 0-9>")
		} else {
			// List hotbar
			fmt.Println("=== Hotbar (keys 1-9, 0) ===")
			for i := 0; i < 10; i++ {
				key := (i + 1) % 10
				sp := gs.Hotbar[i]
				cd := gs.CooldownRemaining(i)
				if sp != nil {
					cdStr := ""
					if cd > 0 {
						cdStr = fmt.Sprintf(" [CD %.1fs]", cd)
					}
					fmt.Printf("  [%d] slot %d: %-25s cast=%dms power=%d%s\n",
						key, i, sp.Name, sp.CastTime, sp.Power, cdStr)
				} else {
					fmt.Printf("  [%d] slot %d: (empty)\n", key, i)
				}
			}
		}

	case "memorize":
		if len(parts) < 3 {
			fmt.Println("Usage: memorize <spellListIndex> <hotbarSlot>")
			fmt.Println("  spellListIndex: 0-based index in spell list (see 'spells')")
			fmt.Println("  hotbarSlot: 0-9")
			return
		}
		idx, err1 := strconv.Atoi(parts[1])
		slot, err2 := strconv.Atoi(parts[2])
		if err1 != nil || err2 != nil || idx < 0 || slot < 0 || slot > 9 {
			fmt.Println("Invalid arguments. spellListIndex >= 0, slot 0-9.")
			return
		}
		gs := engine.State
		if idx < len(gs.ParsedSpells) {
			fmt.Printf("Memorizing spell [%d] %s → slot %d\n", idx, gs.ParsedSpells[idx].Name, slot)
		} else {
			fmt.Printf("Memorizing spell index %d → slot %d\n", idx, slot)
		}
		engine.SendAddSkillToBar(1, byte(idx), byte(slot))

	case "unmemorize":
		if len(parts) < 2 {
			fmt.Println("Usage: unmemorize <hotbarSlot>")
			return
		}
		slot, err := strconv.Atoi(parts[1])
		if err != nil || slot < 0 || slot > 9 {
			fmt.Println("Invalid slot. Must be 0-9.")
			return
		}
		fmt.Printf("Removing spell from slot %d\n", slot)
		engine.SendAddSkillToBar(0, 0, byte(slot))

	case "clearbar":
		fmt.Println("Clearing all spells from hotbar")
		engine.SendClearSpellBar()
		// Locally clear all slots
		gs := engine.State
		for i := 0; i < 10; i++ {
			gs.SetHotbarSlot(i, nil)
		}

	case "shout":
		if len(parts) < 2 {
			fmt.Println("Usage: shout <message>")
			return
		}
		text := strings.Join(parts[1:], " ")
		engine.SendShout(text)
		fmt.Printf("Shouted: %s\n", text)

	case "gsay":
		if len(parts) < 2 {
			fmt.Println("Usage: gsay <message>")
			return
		}
		text := strings.Join(parts[1:], " ")
		engine.SendGroupChat(text)
		fmt.Printf("Group: %s\n", text)

	case "ginvite":
		if len(parts) < 2 {
			fmt.Println("Usage: ginvite <player name>")
			return
		}
		gs := engine.State
		search := strings.ToLower(strings.Join(parts[1:], " "))
		var found *game.Entity
		for _, ent := range gs.Entities {
			if ent.IsPlayer && strings.Contains(strings.ToLower(ent.Name), search) {
				found = ent
				break
			}
		}
		if found == nil {
			fmt.Printf("No player matching '%s' found nearby.\n", search)
			return
		}
		engine.SendGroupInvite(found.ObjectID, found.Name)
		fmt.Printf("Invited %s [%d] to group\n", found.Name, found.ObjectID)

	case "gaccept":
		gs := engine.State
		if gs.GroupInviteID == 0 {
			fmt.Println("No pending group invite.")
			return
		}
		engine.SendAcceptGroupInvite(gs.GroupInviteID)
		fmt.Printf("Accepted group invite from %s (groupID=%d)\n", gs.GroupInviter, gs.GroupInviteID)

	case "gdecline":
		gs := engine.State
		if gs.GroupInviteID == 0 {
			fmt.Println("No pending group invite.")
			return
		}
		engine.SendDeclineGroupInvite(gs.GroupInviteID)
		fmt.Printf("Declined group invite from %s\n", gs.GroupInviter)

	case "gleave":
		engine.SendLeaveGroup()
		fmt.Println("Left group.")

	case "guildsay", "gu":
		if len(parts) < 2 {
			fmt.Println("Usage: guildsay <message>")
			return
		}
		text := strings.Join(parts[1:], " ")
		engine.SendGuildChat(text)
		fmt.Printf("Guild: %s\n", text)

	case "guildcreate":
		if len(parts) < 2 {
			fmt.Println("Usage: guildcreate <guild name>")
			return
		}
		name := strings.Join(parts[1:], " ")
		engine.SendGuildCreate(name)
		fmt.Printf("Creating guild: %s\n", name)

	case "guildleave":
		engine.SendGuildLeave()
		fmt.Println("Left guild.")

	case "guildmotd":
		if len(parts) < 2 {
			fmt.Println("Usage: guildmotd <message>")
			return
		}
		text := strings.Join(parts[1:], " ")
		engine.SendGuildSetMOTD(text)
		fmt.Printf("Set guild MOTD: %s\n", text)

	case "guildinvite":
		if len(parts) < 2 {
			fmt.Println("Usage: guildinvite <player name>")
			return
		}
		name := strings.Join(parts[1:], " ")
		engine.SendGuildInvite(name)
		fmt.Printf("Invited %s to guild\n", name)

	case "guildkick":
		if len(parts) < 2 {
			fmt.Println("Usage: guildkick <player name>")
			return
		}
		name := strings.Join(parts[1:], " ")
		engine.SendGuildKick(name)
		fmt.Printf("Kicked %s from guild\n", name)

	case "gboot":
		if len(parts) < 2 {
			fmt.Println("Usage: gboot <player name>")
			return
		}
		gs := engine.State
		search := strings.ToLower(strings.Join(parts[1:], " "))
		var found *game.Entity
		for _, ent := range gs.Entities {
			if ent.IsPlayer && strings.Contains(strings.ToLower(ent.Name), search) {
				found = ent
				break
			}
		}
		if found == nil {
			fmt.Printf("No player matching '%s' found nearby.\n", search)
			return
		}
		engine.SendBootGroupMember(found.ObjectID)
		fmt.Printf("Booted %s [%d] from group\n", found.Name, found.ObjectID)

	case "group":
		gs := engine.State
		if !gs.GroupActive {
			fmt.Println("Not in a group.")
			return
		}
		fmt.Printf("=== Group (ID=%d, %d members) ===\n", gs.GroupID, len(gs.GroupMembers))
		for i, m := range gs.GroupMembers {
			fmt.Printf("  [%d] %s (ObjectID=%d)\n", i+1, m.Name, m.ObjectID)
		}

	case "who":
		engine.SendWhoList()
		fmt.Println("Who list requested. Check log for results.")
		// Print any cached results
		gs := engine.State
		if len(gs.WhoList) > 0 {
			fmt.Printf("=== Who List (%d) ===\n", len(gs.WhoList))
			for _, w := range gs.WhoList {
				fmt.Printf("  %-20s Level %d\n", w.Name, w.Level)
			}
		}

	case "lfg":
		gs := engine.State
		newState := !gs.LFGRegistered
		engine.SendLFGToggle(newState)
		if newState {
			fmt.Println("LFG registered.")
		} else {
			fmt.Println("LFG unregistered.")
		}

	case "create":
		// create <name> <race> <class> [gender]
		// race: 0-9 (HUM,ELF,DELF,GNO,DWF,TRL,BAR,HLF,ERU,OGR)
		// class: 0-14 (WAR,RAN,PAL,SK,MNK,BRD,RGE,DRD,SHA,CL,MAG,NEC,ENC,WIZ,ALC)
		if len(parts) < 4 {
			fmt.Println("Usage: create <name> <race> <class> [gender]")
			fmt.Println("  race:   0=HUM 1=ELF 2=DELF 3=GNO 4=DWF 5=TRL 6=BAR 7=HLF 8=ERU 9=OGR")
			fmt.Println("  class:  0=WAR 1=RAN 2=PAL 3=SK 4=MNK 5=BRD 6=RGE 7=DRD 8=SHA 9=CL 10=MAG 11=NEC 12=ENC 13=WIZ 14=ALC")
			fmt.Println("  gender: 0=male 1=female (default: 0)")
			return
		}
		charName := parts[1]
		race, err1 := strconv.Atoi(parts[2])
		class, err2 := strconv.Atoi(parts[3])
		if err1 != nil || err2 != nil || race < 0 || race > 9 || class < 0 || class > 14 {
			fmt.Println("Invalid race or class number.")
			return
		}
		gender := 0
		if len(parts) >= 5 {
			if g, err := strconv.Atoi(parts[4]); err == nil {
				gender = g
			}
		}
		hairColor, hairLength, hairStyle, faceOption := 0, 0, 0, 0
		if len(parts) >= 6 { hairColor, _ = strconv.Atoi(parts[5]) }
		if len(parts) >= 7 { hairLength, _ = strconv.Atoi(parts[6]) }
		if len(parts) >= 8 { hairStyle, _ = strconv.Atoi(parts[7]) }
		if len(parts) >= 9 { faceOption, _ = strconv.Atoi(parts[8]) }
		gs := engine.State
		gs.CreateCharacter(charName, race, class, gender, hairColor, hairLength, hairStyle, faceOption, 0, [7]int{})
		fmt.Printf("Creating character: %s (race=%d class=%d gender=%d hair=%d/%d/%d face=%d)\n",
			charName, race, class, gender, hairColor, hairLength, hairStyle, faceOption)

	case "disconnect":
		engine.SendDisconnect()
		fmt.Println("Sent disconnect to server.")

	case "stats":
		game.PrintOpcodeStats()

	case "quit":
		game.PrintOpcodeStats()
		engine.SendDisconnect()
		time.Sleep(200 * time.Millisecond) // let disconnect flush
		gs := engine.State
		if gs.Conn != nil {
			gs.Conn.Close()
		}
		fmt.Println("Disconnected.")
		os.Exit(0)

	default:
		fmt.Printf("Unknown command: %s (type 'help' for commands)\n", cmd)
	}
}
