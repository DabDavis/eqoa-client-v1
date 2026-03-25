package main

// PS2 Race Body/Camera Parameters from CLIENT.AUT data section (0x00CDD8A0).
// 14 float32 values per race, 56-byte stride. Male at 0x00CDD8A0, female at 0x00CDDD38.
//
// Fields:
//   F01/F02 = height min/max (character model height range)
//   F03     = camera pitch angle (radians, default orbit elevation)
//   F04     = camera distance (default orbit distance)
//   F05/F06 = zoom min/max (closest/furthest orbit distance)
//   F07     = pitch limit (radians, max downward pitch angle)
//   F08     = scale multiplier
//   F09/F10 = hitbox width (equal pair)
//   F11     = constant 10° (0.1745 rad) -- unused
//   F12     = max camera distance
//   F13     = height multiplier
type raceParams struct {
	HeightMin    float32
	HeightMax    float32
	CamPitch     float32 // default orbit pitch (radians)
	CamDist      float32 // default orbit distance
	ZoomMin      float32 // closest zoom distance
	ZoomMax      float32 // furthest zoom distance
	PitchLimit   float32 // max downward pitch (radians)
	Scale        float32 // model scale multiplier
	HitboxWidth  float32 // collision width
	MaxCamDist   float32 // maximum camera distance
	HeightMult   float32 // height multiplier
}

// PS2 CLIENT.AUT: Male race parameters at 0x00CDD8A0.
// Index by race ID: HUM=0, ELF=1, DELF=2, GNO=3, DWF=4, TRL=5, BAR=6, HLF=7, ERU=8, OGR=9
var raceParamsMale = [10]raceParams{
	// Human
	{HeightMin: 0.40, HeightMax: 0.98, CamPitch: 0.393, CamDist: 2.00,
		ZoomMin: 1.00, ZoomMax: 1.58, PitchLimit: 0.785, Scale: 1.00,
		HitboxWidth: 0.45, MaxCamDist: 4.00, HeightMult: 1.60},
	// Elf
	{HeightMin: 0.40, HeightMax: 0.98, CamPitch: 0.611, CamDist: 1.75,
		ZoomMin: 0.80, ZoomMax: 1.38, PitchLimit: 0.698, Scale: 0.75,
		HitboxWidth: 0.45, MaxCamDist: 3.60, HeightMult: 1.55},
	// Dark Elf
	{HeightMin: 0.30, HeightMax: 0.88, CamPitch: 0.480, CamDist: 2.00,
		ZoomMin: 0.90, ZoomMax: 1.48, PitchLimit: 0.742, Scale: 1.00,
		HitboxWidth: 0.45, MaxCamDist: 3.60, HeightMult: 1.55},
	// Gnome
	{HeightMin: 0.10, HeightMax: 0.68, CamPitch: 0.524, CamDist: 1.75,
		ZoomMin: 0.40, ZoomMax: 0.98, PitchLimit: 0.873, Scale: 1.00,
		HitboxWidth: 0.35, MaxCamDist: 3.00, HeightMult: 1.35},
	// Dwarf
	{HeightMin: 0.10, HeightMax: 0.68, CamPitch: 0.611, CamDist: 2.00,
		ZoomMin: 0.60, ZoomMax: 1.18, PitchLimit: 0.567, Scale: 1.25,
		HitboxWidth: 0.37, MaxCamDist: 3.40, HeightMult: 1.35},
	// Troll
	{HeightMin: 0.70, HeightMax: 1.28, CamPitch: 0.480, CamDist: 2.75,
		ZoomMin: 1.40, ZoomMax: 1.98, PitchLimit: 0.524, Scale: 1.50,
		HitboxWidth: 0.65, MaxCamDist: 5.90, HeightMult: 1.70},
	// Barbarian
	{HeightMin: 0.60, HeightMax: 1.18, CamPitch: 0.349, CamDist: 2.50,
		ZoomMin: 1.30, ZoomMax: 1.88, PitchLimit: 0.524, Scale: 1.25,
		HitboxWidth: 0.65, MaxCamDist: 4.80, HeightMult: 1.68},
	// Halfling
	{HeightMin: 0.00, HeightMax: 0.58, CamPitch: 0.524, CamDist: 2.00,
		ZoomMin: 0.40, ZoomMax: 0.98, PitchLimit: 0.655, Scale: 1.25,
		HitboxWidth: 0.35, MaxCamDist: 3.20, HeightMult: 1.35},
	// Erudite
	{HeightMin: 0.40, HeightMax: 0.98, CamPitch: 0.480, CamDist: 2.50,
		ZoomMin: 1.10, ZoomMax: 1.68, PitchLimit: 0.655, Scale: 1.00,
		HitboxWidth: 0.60, MaxCamDist: 4.20, HeightMult: 1.65},
	// Ogre
	{HeightMin: 1.00, HeightMax: 1.57, CamPitch: 0.480, CamDist: 2.50,
		ZoomMin: 1.60, ZoomMax: 2.18, PitchLimit: 0.436, Scale: 1.25,
		HitboxWidth: 0.80, MaxCamDist: 5.00, HeightMult: 1.70},
}

// PS2 CLIENT.AUT: Female race parameters at 0x00CDDD38.
var raceParamsFemale = [10]raceParams{
	// Human
	{HeightMin: 0.30, HeightMax: 0.88, CamPitch: 0.524, CamDist: 2.50,
		ZoomMin: 0.95, ZoomMax: 1.53, PitchLimit: 0.742, Scale: 1.00,
		HitboxWidth: 0.45, MaxCamDist: 4.00, HeightMult: 1.60},
	// Elf
	{HeightMin: 0.30, HeightMax: 0.88, CamPitch: 0.655, CamDist: 2.25,
		ZoomMin: 0.85, ZoomMax: 1.36, PitchLimit: 0.742, Scale: 0.89,
		HitboxWidth: 0.45, MaxCamDist: 3.60, HeightMult: 1.55},
	// Dark Elf
	{HeightMin: 0.20, HeightMax: 0.88, CamPitch: 0.611, CamDist: 2.50,
		ZoomMin: 0.89, ZoomMax: 1.44, PitchLimit: 0.742, Scale: 0.94,
		HitboxWidth: 0.45, MaxCamDist: 3.60, HeightMult: 1.55},
	// Gnome
	{HeightMin: 0.10, HeightMax: 0.68, CamPitch: 0.611, CamDist: 2.25,
		ZoomMin: 0.64, ZoomMax: 1.03, PitchLimit: 0.524, Scale: 1.10,
		HitboxWidth: 0.35, MaxCamDist: 3.00, HeightMult: 1.35},
	// Dwarf
	{HeightMin: 0.10, HeightMax: 0.68, CamPitch: 0.611, CamDist: 2.25,
		ZoomMin: 0.74, ZoomMax: 1.19, PitchLimit: 0.611, Scale: 1.10,
		HitboxWidth: 0.37, MaxCamDist: 3.40, HeightMult: 1.35},
	// Troll
	{HeightMin: 0.70, HeightMax: 1.28, CamPitch: 0.480, CamDist: 3.25,
		ZoomMin: 1.37, ZoomMax: 2.20, PitchLimit: 0.742, Scale: 1.44,
		HitboxWidth: 0.65, MaxCamDist: 5.90, HeightMult: 1.70},
	// Barbarian
	{HeightMin: 0.40, HeightMax: 0.98, CamPitch: 0.524, CamDist: 3.00,
		ZoomMin: 1.16, ZoomMax: 1.87, PitchLimit: 0.742, Scale: 1.22,
		HitboxWidth: 0.65, MaxCamDist: 4.80, HeightMult: 1.68},
	// Halfling
	{HeightMin: 0.10, HeightMax: 0.68, CamPitch: 0.655, CamDist: 2.50,
		ZoomMin: 0.68, ZoomMax: 1.10, PitchLimit: 0.655, Scale: 1.00,
		HitboxWidth: 0.35, MaxCamDist: 3.20, HeightMult: 1.35},
	// Erudite
	{HeightMin: 0.30, HeightMax: 0.88, CamPitch: 0.480, CamDist: 2.75,
		ZoomMin: 1.03, ZoomMax: 1.65, PitchLimit: 0.742, Scale: 1.08,
		HitboxWidth: 0.60, MaxCamDist: 4.20, HeightMult: 1.65},
	// Ogre
	{HeightMin: 0.80, HeightMax: 1.38, CamPitch: 0.698, CamDist: 2.75,
		ZoomMin: 1.40, ZoomMax: 2.25, PitchLimit: 0.742, Scale: 1.47,
		HitboxWidth: 0.80, MaxCamDist: 5.00, HeightMult: 1.70},
}

// getRaceParams returns the PS2 race camera/body parameters for a given race and sex.
// Falls back to human male if race is out of range.
func getRaceParams(race, sex byte) raceParams {
	idx := int(race)
	if idx < 0 || idx >= 10 {
		idx = 0
	}
	if sex == 1 {
		return raceParamsFemale[idx]
	}
	return raceParamsMale[idx]
}

// PS2 VIWalkCamera uses these race parameters to scale the camera orbit:
//   - CamDist is multiplied by a world-scale factor (~4-6x) to get actual orbit distance
//   - PitchLimit caps how far down the player can look
//   - MaxCamDist caps the maximum zoom-out
//   - ZoomMin/ZoomMax define the scroll wheel range
//
// The PS2 values are in "character units" — the world uses larger coordinates,
// so we scale CamDist/ZoomMin/ZoomMax/MaxCamDist by a conversion factor.
// From PS2 InitParms: default orbit distance = 11.75 world units for a Human
// whose CamDist = 2.00, giving a scale factor of ~5.875.
const raceCamScale = float32(5.875)

// playerRaceParams returns the PS2 race camera/body parameters for the current player.
// Falls back to human male if state is not available.
func (c *Client) playerRaceParams() raceParams {
	if c.engine != nil && c.engine.State != nil {
		p := &c.engine.State.Player
		return getRaceParams(p.Race, p.Sex)
	}
	return raceParamsMale[0]
}

// raceDefaultCamDist returns the PS2 default orbit distance for a race, scaled to world units.
func raceDefaultCamDist(race, sex byte) float32 {
	rp := getRaceParams(race, sex)
	return rp.CamDist * raceCamScale
}
