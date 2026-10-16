package game

import (
	"os"
	"testing"

	"github.com/eqoa/client/internal/protocol"
	"github.com/DabDavis/eqoa-esf-tools/pkg/buffer"
)

// TestParseMemoryDump verifies the full MemoryDump parsing pipeline against a
// captured binary from the Sandstorm server (opcode 0x000D, ~26KB).
//
// Sandstorm format (differs from C# reference — no weapon hotbar section):
//
//	header → hotkeys → quests → inventory (zigzag+uint32 count + DumpItem[]) →
//	bank (zigzag+uint32 count + DumpItem[]) → 4 transition bytes →
//	spell count (zigzag) → DumpSpell[] (9 zig + uint16 range + 7 zig + name + desc + 0x00) →
//	speed (float32) → 26×int32 skills → CM zigzag fields
func TestParseMemoryDump(t *testing.T) {
	data, err := os.ReadFile("/tmp/opcode_000D_1.bin")
	if err != nil {
		t.Skipf("No captured MemoryDump at /tmp/opcode_000D_1.bin: %v", err)
	}
	t.Logf("Loaded MemoryDump: %d bytes", len(data))

	gs := NewGameState("test", 10070, 9735, 7000, "test")
	gs.Characters = []CharInfo{{ServerID: 1, ModelID: 12345}}
	gs.SelectedChar = 0

	msg := &protocol.Message{}
	msg.SetData(data)
	msg.Opcode = protocol.OpcodeMemoryDump

	gs.handleMemoryDump(msg)

	t.Logf("Zone: %s", gs.ZonePath)
	t.Logf("Player: %s (Level %d)", gs.Player.Name, gs.Player.Level)
	t.Logf("Position: (%.1f, %.1f, %.1f)", gs.Player.X, gs.Player.Y, gs.Player.Z)
	t.Logf("Parsed items: %d", len(gs.ParsedItems))
	t.Logf("Parsed spells: %d", len(gs.ParsedSpells))

	if len(gs.ParsedItems) == 0 {
		t.Error("Expected at least some items from MemoryDump")
	}
	if len(gs.ParsedSpells) == 0 {
		t.Error("Expected at least some spells from MemoryDump")
	}

	// Print first few items
	for i, item := range gs.ParsedItems {
		if i >= 5 {
			break
		}
		t.Logf("  Item[%d]: id=%d name=%q slot=%d lvl=%d cost=%d stats=%v",
			i, item.ItemID, item.Name, item.Slot, item.LevelReq, item.ItemCost, item.Stats)
	}

	// Print first few spells
	for i, sp := range gs.ParsedSpells {
		if i >= 5 {
			break
		}
		t.Logf("  Spell[%d]: id=%d name=%q ability=%d range=%d cast=%d power=%d",
			i, sp.SpellID, sp.Name, sp.AbilityLevel, sp.Range, sp.CastTime, sp.Power)
	}

	// Verify CM data
	if gs.ParsedCM == nil {
		t.Fatal("Expected CM data from MemoryDump")
	}
	cm := gs.ParsedCM
	t.Logf("CM: speed=%.2f unspent=%d spent=%d pct=%d%%",
		cm.Speed, cm.UnspentCMs, cm.SpentCMs, cm.CMPercentage)
	t.Logf("CM: skills=%v", cm.Skills)

	if cm.Speed <= 0 {
		t.Error("Expected positive Speed in CM data")
	}
	if cm.SpentCMs == 0 && cm.UnspentCMs == 0 {
		t.Error("Expected non-zero CM values for a level 51 character")
	}
}

// TestRoundTripServerFormat builds a MemoryDump in the Go server's Sandstorm
// format and verifies the Go client parser can consume it fully.
func TestRoundTripServerFormat(t *testing.T) {
	buf := make([]byte, 8192)
	w := buffer.NewWriter(buf)

	// Opcode
	w.WriteUint16(uint16(protocol.OpcodeMemoryDump))

	// === DumpCharacter header ===
	w.WriteByte(0) // unknown byte

	zone := "data\\tunaria.esf"
	w.WriteInt32(int32(len(zone)))
	w.WriteStringUTF8Raw(zone)

	w.Write7BitEncodedInt64(1000) // CharServerID
	name := "TestChar"
	w.WriteInt32(int32(len(name)))
	w.WriteStringUTF8Raw(name)
	w.Write7BitEncodedInt64(0)    // Class
	w.Write7BitEncodedInt64(0)    // Race
	w.Write7BitEncodedInt64(10)   // Level
	w.Write7BitEncodedInt64(5000) // XP
	w.Write7BitEncodedInt64(0)    // Debt
	w.WriteByte(255)              // Breath
	w.Write7BitEncodedInt64(100)  // Tunar
	w.Write7BitEncodedInt64(0)    // BankTunar
	w.Write7BitEncodedInt64(0)    // UnusedTP
	w.Write7BitEncodedInt64(0)    // BaseMaxStat
	w.Write7BitEncodedInt64(0)    // World

	// Position
	w.WriteFloat32(100.0)
	w.WriteFloat32(50.0)
	w.WriteFloat32(200.0)
	w.WriteFloat32(1.5)

	// Two unknown floats
	w.WriteFloat32(0.0)
	w.WriteFloat32(0.0)

	// Hotkeys: 0
	w.Write7BitEncodedInt64(0)

	// Two int32 zeros
	w.WriteInt32(0)
	w.WriteInt32(0)

	// Quests: 0
	w.WriteUint32(0)

	// === Inventory: 2 items ===
	w.Write7BitEncodedInt64(2)
	w.WriteUint32(2)

	// Item 1: "Iron Sword" with str+5, dex+3
	writeTestItem(w, 1, 1, 100, 0, 0, 42, // stack=1, hp=100, charges=0, equip=0, loc=0, key=42
		1001, 500, 0, 12345, 0, 14, 0, // id=1001, cost=500, icon=12345, slot=14
		0, 0, 0, 5, 200, 0, // noTrade=0, noRent=0, atkType=5, dmg=200
		10, 1, 100, 0, // lvlReq=10, maxStack=1, maxHP=100
		0, 0, 0, 0, 0, // classUse=0, raceUse=0, procAnim=0, lore=0, unk6=0
		0, "Iron Sword", "A sturdy iron blade", // craft=0
		map[int]int64{0: 5, 3: 3}) // str=5, dex=3

	// Item 2: "Health Potion" (stackable, no stats)
	writeTestItem(w, 5, 0, 0, -1, 0, 43, // stack=5, hp=0, charges=0, equip=-1, loc=0, key=43
		2001, 50, 0, 67890, 0, 0, 0, // id=2001, cost=50, icon=67890, slot=0
		0, 0, 0, 0, 0, 0, // noTrade=0, noRent=0, atkType=0, dmg=0
		1, 20, 0, 0, // lvlReq=1, maxStack=20, maxHP=0
		0, 0, 0, 0, 0, // classUse=0, raceUse=0, procAnim=0, lore=0, unk6=0
		0, "Health Potion", "Restores health", // craft=0
		nil) // no stats

	// === Bank: 1 item ===
	w.Write7BitEncodedInt64(1)
	w.WriteUint32(1)

	writeTestItem(w, 10, 0, 0, -1, 1, 100, // stack=10, loc=1 (bank), key=100
		3001, 10, 0, 11111, 0, 0, 0,
		0, 0, 0, 0, 0, 0,
		1, 99, 0, 0,
		0, 0, 0, 0, 0,
		1, "Crafting Material", "Used for crafting",
		nil)

	// Transition bytes
	w.WriteByte(0x00)
	w.WriteByte(0x00)
	w.WriteByte(0x01)
	w.WriteByte(0x00)

	// === Spells: 2 spells ===
	w.Write7BitEncodedInt64(2)

	// Spell 1: "Fireball"
	writeTestSpell(w, 101, 1, 0, 0, 0, 0, 5, 0, 0, // id=101, addedOrder=1, abilityLvl=5
		50, 3, 100, 0, 12345, 1, 6, 0, // range=50, cast=3, power=100, scope=1, recast=6
		"Fireball", "Hurls a ball of fire")

	// Spell 2: "Heal"
	writeTestSpell(w, 201, 2, 1, 3, 0, 0, 10, 0, 0, // id=201, addedOrder=2, onHotBar=1, where=3, abilityLvl=10
		30, 2, 80, 0, 67890, 2, 4, 0, // range=30, cast=2, power=80, scope=2, recast=4
		"Heal", "Restores health to target")

	// === Stats/CM section ===
	w.WriteFloat32(8.0) // Speed

	// 26 × int32 skills
	for i := 0; i < 26; i++ {
		w.WriteInt32(0)
	}

	// CM data — all zigzag
	w.Write7BitEncodedInt64(0) // UnspentCMs
	w.Write7BitEncodedInt64(0) // SpentCMs
	w.Write7BitEncodedInt64(0) // Unknown
	w.Write7BitEncodedInt64(0) // CMPercentage
	w.WriteFloat32(8.0)        // Speed2

	// 7 stat additions + 7 stat maxes
	for i := 0; i < 14; i++ {
		w.Write7BitEncodedInt64(0)
	}
	// HP, Power, HoT, PoT, AC
	for i := 0; i < 5; i++ {
		w.Write7BitEncodedInt64(0)
	}
	// OFF, DEF, HP Factor
	for i := 0; i < 3; i++ {
		w.Write7BitEncodedInt64(0)
	}
	// 6 resists
	for i := 0; i < 6; i++ {
		w.Write7BitEncodedInt64(0)
	}

	data := buf[:w.Position()]

	// Parse it
	gs := NewGameState("test", 10070, 9735, 7000, "test")
	gs.Characters = []CharInfo{{ServerID: 1, ModelID: 100}}
	gs.SelectedChar = 0

	msg := &protocol.Message{}
	msg.SetData(data)
	msg.Opcode = protocol.OpcodeMemoryDump

	gs.handleMemoryDump(msg)

	if gs.Player.Name != "TestChar" {
		t.Errorf("Expected name 'TestChar', got %q", gs.Player.Name)
	}
	if gs.Player.Level != 10 {
		t.Errorf("Expected level 10, got %d", gs.Player.Level)
	}

	// Verify items: 2 inventory + 1 bank = 3 total
	if len(gs.ParsedItems) != 3 {
		t.Fatalf("Expected 3 items, got %d", len(gs.ParsedItems))
	}
	if gs.InventoryItemCount != 2 {
		t.Errorf("Expected InventoryItemCount=2, got %d", gs.InventoryItemCount)
	}

	// Check item 1
	item0 := gs.ParsedItems[0]
	if item0.Name != "Iron Sword" {
		t.Errorf("Item[0] name: expected 'Iron Sword', got %q", item0.Name)
	}
	if item0.ItemID != 1001 {
		t.Errorf("Item[0] ID: expected 1001, got %d", item0.ItemID)
	}
	if item0.Stats[0] != 5 {
		t.Errorf("Item[0] str stat: expected 5, got %d", item0.Stats[0])
	}
	if item0.Stats[3] != 3 {
		t.Errorf("Item[0] dex stat: expected 3, got %d", item0.Stats[3])
	}
	if item0.WeaponDamage != 200 {
		t.Errorf("Item[0] damage: expected 200, got %d", item0.WeaponDamage)
	}

	// Check item 2 (stackable, no stats)
	item1 := gs.ParsedItems[1]
	if item1.Name != "Health Potion" {
		t.Errorf("Item[1] name: expected 'Health Potion', got %q", item1.Name)
	}
	if item1.StackLeft != 5 {
		t.Errorf("Item[1] stack: expected 5, got %d", item1.StackLeft)
	}
	if len(item1.Stats) != 0 {
		t.Errorf("Item[1] stats: expected 0, got %d", len(item1.Stats))
	}

	// Check bank item
	item2 := gs.ParsedItems[2]
	if item2.Name != "Crafting Material" {
		t.Errorf("Item[2] name: expected 'Crafting Material', got %q", item2.Name)
	}
	if item2.Location != 1 {
		t.Errorf("Item[2] location: expected 1 (bank), got %d", item2.Location)
	}

	// Verify spells
	if len(gs.ParsedSpells) != 2 {
		t.Fatalf("Expected 2 spells, got %d", len(gs.ParsedSpells))
	}
	sp0 := gs.ParsedSpells[0]
	if sp0.Name != "Fireball" {
		t.Errorf("Spell[0] name: expected 'Fireball', got %q", sp0.Name)
	}
	if sp0.SpellID != 101 {
		t.Errorf("Spell[0] ID: expected 101, got %d", sp0.SpellID)
	}
	if sp0.Power != 100 {
		t.Errorf("Spell[0] power: expected 100, got %d", sp0.Power)
	}
	if sp0.Range != 50 {
		t.Errorf("Spell[0] range: expected 50, got %d", sp0.Range)
	}

	sp1 := gs.ParsedSpells[1]
	if sp1.Name != "Heal" {
		t.Errorf("Spell[1] name: expected 'Heal', got %q", sp1.Name)
	}
	if sp1.OnHotBar != 1 {
		t.Errorf("Spell[1] onHotBar: expected 1, got %d", sp1.OnHotBar)
	}

	// Verify CM
	if gs.ParsedCM == nil {
		t.Fatal("Expected CM data to be parsed")
	}
	if gs.ParsedCM.Speed != 8.0 {
		t.Errorf("Expected speed 8.0, got %.2f", gs.ParsedCM.Speed)
	}

	t.Logf("Round-trip OK: name=%s level=%d speed=%.2f items=%d spells=%d",
		gs.Player.Name, gs.Player.Level, gs.ParsedCM.Speed,
		len(gs.ParsedItems), len(gs.ParsedSpells))
}

// writeTestItem writes a single DumpItem to the buffer (test helper).
// Matches the exact wire format: 29 zigzag fields + name + desc + stats.
func writeTestItem(w *buffer.Writer,
	stackLeft, remainHP, charges, equipLoc, location int64, key uint32,
	itemID, cost, unk1 int64, icon int64, unk2, slot, unk3 int64,
	noTrade, noRent, unk4, atkType, weaponDmg, unk5 int64,
	lvlReq, maxStack, maxHP, duration int64,
	classUse, raceUse, procAnim, lore, unk6 int64,
	craft int64, name, desc string,
	stats map[int]int64) {

	w.Write7BitEncodedInt64(stackLeft)
	w.Write7BitEncodedInt64(remainHP)
	w.Write7BitEncodedInt64(charges)
	w.Write7BitEncodedInt64(equipLoc)
	w.Write7BitEncodedInt64(location)
	w.WriteUint32(key)
	w.Write7BitEncodedInt64(itemID)
	w.Write7BitEncodedInt64(cost)
	w.Write7BitEncodedInt64(unk1)
	w.Write7BitEncodedInt64(icon)
	w.Write7BitEncodedInt64(unk2)
	w.Write7BitEncodedInt64(slot)
	w.Write7BitEncodedInt64(unk3)
	w.Write7BitEncodedInt64(noTrade)
	w.Write7BitEncodedInt64(noRent)
	w.Write7BitEncodedInt64(unk4)
	w.Write7BitEncodedInt64(atkType)
	w.Write7BitEncodedInt64(weaponDmg)
	w.Write7BitEncodedInt64(unk5)
	w.Write7BitEncodedInt64(lvlReq)
	w.Write7BitEncodedInt64(maxStack)
	w.Write7BitEncodedInt64(maxHP)
	w.Write7BitEncodedInt64(duration)
	w.Write7BitEncodedInt64(classUse)
	w.Write7BitEncodedInt64(raceUse)
	w.Write7BitEncodedInt64(procAnim)
	w.Write7BitEncodedInt64(lore)
	w.Write7BitEncodedInt64(unk6)
	w.Write7BitEncodedInt64(craft)
	w.WriteStringUnicode(name)
	w.WriteStringUnicode(desc)

	if stats == nil {
		w.Write7BitEncodedInt64(0)
	} else {
		w.Write7BitEncodedInt64(int64(len(stats)))
		// Sort indices for deterministic output
		keys := make([]int, 0, len(stats))
		for k := range stats {
			keys = append(keys, k)
		}
		for i := 0; i < len(keys); i++ {
			for j := i + 1; j < len(keys); j++ {
				if keys[j] < keys[i] {
					keys[i], keys[j] = keys[j], keys[i]
				}
			}
		}
		for _, k := range keys {
			w.Write7BitEncodedInt64(int64(k))
			w.Write7BitEncodedInt64(stats[k])
		}
	}
}

// writeTestSpell writes a single ability to the buffer (test helper), as
// VIAbility::Read takes it: 7 zigzag + float32 range + 7 zigzag + name + desc
// + a zigzag sub-record count (0). unk2 and unk3 are unused: the C#
// reference's two zigzags there were the float's low bytes.
func writeTestSpell(w *buffer.Writer,
	spellID, addedOrder, onHotBar, whereOnHotBar, unk1, showHide, abilityLvl, unk2, unk3 int64,
	spellRange uint16, castTime, power int64, iconColor, icon int64, scope, recast, eqpReq int64,
	name, desc string) {

	w.Write7BitEncodedInt64(spellID)
	w.Write7BitEncodedInt64(addedOrder)
	w.Write7BitEncodedInt64(onHotBar)
	w.Write7BitEncodedInt64(whereOnHotBar)
	w.Write7BitEncodedInt64(unk1)
	w.Write7BitEncodedInt64(showHide)
	w.Write7BitEncodedInt64(abilityLvl)
	w.WriteFloat32(float32(spellRange))
	w.Write7BitEncodedInt64(castTime)
	w.Write7BitEncodedInt64(power)
	w.Write7BitEncodedInt64(iconColor)
	w.Write7BitEncodedInt64(icon)
	w.Write7BitEncodedInt64(scope)
	w.Write7BitEncodedInt64(recast)
	w.Write7BitEncodedInt64(eqpReq)
	w.WriteStringUnicode(name)
	w.WriteStringUnicode(desc)
	w.WriteByte(0x00) // zigzag 0: no sub-records
}
