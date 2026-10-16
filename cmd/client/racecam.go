package main

// The race camera table, CLIENT's VIRaceCamera, read straight out of the image
// (claude-eqoa-iso/decrypted_modules/CLIENT_decompressed_SNR1.bin; racecam_test.go
// pins every byte when the image is present).
//
// GetRaceCamera (0x00BFC048, read off the disassembly):
//
//	key   = race < 21 ? race : 0
//	table = expansion & 2 ? 0x00CDDD38 : 0x00CDD8A0
//	for each of the 21 entries: if entry.race == key { found = entry }  (the LAST match)
//	return found
//
// so the two tables are not male and female: the second is chosen by bit 1 of
// VIClient::ExpansionFlags(), the disc's VIGetExpansionFlags() (3 in a live
// retail game, read at 0x004E3770 in two EE dumps) masked with the u32 0x07D1
// carries (every server sends -1). Entries 10..20 are empty (race -1), so a
// race 10..20 finds nothing: ApplyRaceCamera then does nothing, and
// SetCameraStyle and BeginManualCamera would read through NULL.
//
// An entry is 56 bytes: the race, three camera settings of four floats each
// (target height, look-at height above, pitch, distance -- the order every
// caller stores them into a VIWalkCamera) and an eye height. Which caller reads
// which (each read off its disassembly):
//
//	+0x04 Login  VIWndLogin's character preview (0x00C21B08) and three more
//	             windows (0x00C25B48, 0x00C2C8D8, 0x00C2E8C0), each turning
//	             the model with OrbitRight
//	+0x14 Close  two more windows (0x00C28BC8, 0x00C2FC50); a human's looks at
//	             1.58 from 1.0 away, the face
//	+0x24 World  VIClient::ApplyRaceCamera (0x00BC7A80 [V]): the camera put IN
//	             FRONT of the player while the character panel is open
//	+0x34 EyeHeight  SetCameraStyle 0's eye and style 3's look-at height
//	             (0x00BD8B40 [V]), BeginManualCamera's eye (0x00BCE270)
//
// The two tables differ only in Login and Close, so in the world the choice of
// table changes nothing. The old Go table read the 13 floats as one record of
// body sizes, zoom limits and a "pitch limit", scaled the distances by an
// invented 5.875 and picked the table by sex; none of that is in CLIENT.

// raceCamSet is one camera setting, in the order the callers store it.
type raceCamSet struct {
	TargetHeight      float32 // VIWalkCamera::m_targetHeight (+0x274)
	LookAtHeightAbove float32 // VIWalkCamera::m_lookAtHeightAbove (+0x278)
	Pitch             float32 // SetOrbitPitchAngle / SetDefOrbitPitchAngle
	Distance          float32 // SetOrbitDistance
}

// raceCamera is one VIRaceCamera entry.
type raceCamera struct {
	Race      int32
	Login     raceCamSet
	Close     raceCamSet
	World     raceCamSet
	EyeHeight float32
}

// raceCameraBase is the table at 0x00CDD8A0 (ExpansionFlags() & 2 clear).
var raceCameraBase = [21]raceCamera{
	{Race: 0, Login: raceCamSet{0.4, 0.98, 0.3926991, 2}, Close: raceCamSet{1, 1.58, 0.7853982, 1}, World: raceCamSet{0.45, 0.45, 0.17453294, 4}, EyeHeight: 1.6},             // Human
	{Race: 1, Login: raceCamSet{0.4, 0.98, 0.6108653, 1.75}, Close: raceCamSet{0.8, 1.38, 0.69813174, 0.75}, World: raceCamSet{0.45, 0.45, 0.17453294, 3.6}, EyeHeight: 1.55}, // Elf
	{Race: 2, Login: raceCamSet{0.3, 0.88, 0.47996554, 2}, Close: raceCamSet{0.9, 1.48, 0.7417649, 1}, World: raceCamSet{0.45, 0.45, 0.17453294, 3.6}, EyeHeight: 1.55},       // Dark Elf
	{Race: 3, Login: raceCamSet{0.1, 0.68, 0.5235988, 1.75}, Close: raceCamSet{0.4, 0.98, 0.87266463, 1}, World: raceCamSet{0.35, 0.35, 0.17453294, 3}, EyeHeight: 1.35},      // Gnome
	{Race: 4, Login: raceCamSet{0.1, 0.68, 0.6108653, 2}, Close: raceCamSet{0.6, 1.18, 0.567232, 1.25}, World: raceCamSet{0.37, 0.37, 0.17453294, 3.4}, EyeHeight: 1.35},      // Dwarf
	{Race: 5, Login: raceCamSet{0.7, 1.28, 0.47996554, 2.75}, Close: raceCamSet{1.4, 1.98, 0.5235988, 1.5}, World: raceCamSet{0.65, 0.65, 0.17453294, 5.9}, EyeHeight: 1.7},   // Troll
	{Race: 6, Login: raceCamSet{0.6, 1.18, 0.34906587, 2.5}, Close: raceCamSet{1.3, 1.88, 0.5235988, 1.25}, World: raceCamSet{0.65, 0.65, 0.17453294, 4.8}, EyeHeight: 1.68},  // Barbarian
	{Race: 7, Login: raceCamSet{0, 0.58, 0.5235988, 2}, Close: raceCamSet{0.4, 0.98, 0.6544985, 1.25}, World: raceCamSet{0.35, 0.35, 0.17453294, 3.2}, EyeHeight: 1.35},       // Halfling
	{Race: 8, Login: raceCamSet{0.4, 0.98, 0.47996554, 2.5}, Close: raceCamSet{1.1, 1.68, 0.6544985, 1}, World: raceCamSet{0.6, 0.6, 0.17453294, 4.2}, EyeHeight: 1.65},       // Erudite
	{Race: 9, Login: raceCamSet{1, 1.57, 0.47996554, 2.5}, Close: raceCamSet{1.6, 2.18, 0.43633232, 1.25}, World: raceCamSet{0.8, 0.8, 0.17453294, 5}, EyeHeight: 1.7},        // Ogre
	{Race: -1}, {Race: -1}, {Race: -1}, {Race: -1}, {Race: -1}, {Race: -1},
	{Race: -1}, {Race: -1}, {Race: -1}, {Race: -1}, {Race: -1},
}

// raceCameraExp is the table at 0x00CDDD38 (ExpansionFlags() & 2 set, the
// retail game's case).
var raceCameraExp = [21]raceCamera{
	{Race: 0, Login: raceCamSet{0.3, 0.88, 0.5235988, 2.5}, Close: raceCamSet{0.95, 1.53, 0.7417649, 1}, World: raceCamSet{0.45, 0.45, 0.17453294, 4}, EyeHeight: 1.6},        // Human
	{Race: 1, Login: raceCamSet{0.3, 0.88, 0.6544985, 2.25}, Close: raceCamSet{0.85, 1.36, 0.7417649, 0.89}, World: raceCamSet{0.45, 0.45, 0.17453294, 3.6}, EyeHeight: 1.55}, // Elf
	{Race: 2, Login: raceCamSet{0.2, 0.88, 0.6108653, 2.5}, Close: raceCamSet{0.89, 1.44, 0.7417649, 0.94}, World: raceCamSet{0.45, 0.45, 0.17453294, 3.6}, EyeHeight: 1.55},  // Dark Elf
	{Race: 3, Login: raceCamSet{0.1, 0.68, 0.6108653, 2.25}, Close: raceCamSet{0.64, 1.03, 0.5235988, 1.1}, World: raceCamSet{0.35, 0.35, 0.17453294, 3}, EyeHeight: 1.35},    // Gnome
	{Race: 4, Login: raceCamSet{0.1, 0.68, 0.6108653, 2.25}, Close: raceCamSet{0.74, 1.19, 0.6108653, 1.1}, World: raceCamSet{0.37, 0.37, 0.17453294, 3.4}, EyeHeight: 1.35},  // Dwarf
	{Race: 5, Login: raceCamSet{0.7, 1.28, 0.47996554, 3.25}, Close: raceCamSet{1.37, 2.2, 0.7417649, 1.44}, World: raceCamSet{0.65, 0.65, 0.17453294, 5.9}, EyeHeight: 1.7},  // Troll
	{Race: 6, Login: raceCamSet{0.4, 0.98, 0.5235988, 3}, Close: raceCamSet{1.16, 1.87, 0.7417649, 1.22}, World: raceCamSet{0.65, 0.65, 0.17453294, 4.8}, EyeHeight: 1.68},    // Barbarian
	{Race: 7, Login: raceCamSet{0.1, 0.68, 0.6544985, 2.5}, Close: raceCamSet{0.68, 1.1, 0.6544985, 1}, World: raceCamSet{0.35, 0.35, 0.17453294, 3.2}, EyeHeight: 1.35},      // Halfling
	{Race: 8, Login: raceCamSet{0.3, 0.88, 0.47996554, 2.75}, Close: raceCamSet{1.03, 1.65, 0.7417649, 1.08}, World: raceCamSet{0.6, 0.6, 0.17453294, 4.2}, EyeHeight: 1.65},  // Erudite
	{Race: 9, Login: raceCamSet{0.8, 1.38, 0.69813174, 2.75}, Close: raceCamSet{1.4, 2.25, 0.7417649, 1.47}, World: raceCamSet{0.8, 0.8, 0.17453294, 5}, EyeHeight: 1.7},      // Ogre
	{Race: -1}, {Race: -1}, {Race: -1}, {Race: -1}, {Race: -1}, {Race: -1},
	{Race: -1}, {Race: -1}, {Race: -1}, {Race: -1}, {Race: -1},
}

// discExpansionFlags is VIGetExpansionFlags() in a live retail game
// (gExpansionFlags, SUPPORT 0x004E3770, 3 in memory-dumps/inworld-eqoa.live and
// eqoa.live-freeport). No CLIENT, SETUP or STATION code sets it.
const discExpansionFlags = 3

// getRaceCamera is GetRaceCamera(race, expansion) (0x00BFC048 [A]): nil for a
// race that has no entry (10..20).
func getRaceCamera(race int, expansion uint32) *raceCamera {
	key := int32(0)
	if race >= 0 && race < 21 {
		key = int32(race)
	}
	table := &raceCameraBase
	if expansion&2 != 0 {
		table = &raceCameraExp
	}
	var found *raceCamera
	for i := range table {
		if table[i].Race == key {
			found = &table[i]
		}
	}
	return found
}

// expansionFlags is VIClient::ExpansionFlags() (0x00BDD4E8 [V]): the disc's
// flags masked with the server's 0x07D1.
func (c *Client) expansionFlags() uint32 {
	if c.engine == nil || c.engine.State == nil {
		return 0
	}
	return discExpansionFlags & c.engine.State.Session.ExpansionFlags
}

// playerRaceCamera is GetRaceCamera(GetRace(), ExpansionFlags()) for the
// player. A race with no entry (the custom froglok, 10) falls back to the
// human's: CLIENT would read through NULL there.
func (c *Client) playerRaceCamera() *raceCamera {
	race := 0
	if c.engine != nil && c.engine.State != nil {
		race = int(c.engine.State.Player.Race)
	}
	if rc := getRaceCamera(race, c.expansionFlags()); rc != nil {
		return rc
	}
	return getRaceCamera(0, c.expansionFlags())
}
