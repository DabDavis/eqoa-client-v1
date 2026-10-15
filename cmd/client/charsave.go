package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"

	"github.com/eqoa/client/internal/models"
)

// charSave holds per-character state persisted to JSON on logout.
// Mirrors the PS2 memory card save that stores position/facing/zone
// plus equipment and appearance so the character select screen can
// render the 3D model with correct gear.
type charSave struct {
	// Position + camera
	X        float32 `json:"x"`
	Y        float32 `json:"y"`
	Z        float32 `json:"z"`
	Facing   float32 `json:"facing"`
	CamDist  float32 `json:"cam_dist"`
	CamPitch float32 `json:"cam_pitch"`
	CamAngle float32 `json:"cam_angle"`

	// Identity
	ModelID uint32 `json:"model_id"`
	Race    byte   `json:"race"`
	Class   byte   `json:"class"`
	Level   byte   `json:"level"`
	Sex     byte   `json:"sex"`

	// Appearance
	HairColor  byte `json:"hair_color"`
	HairLength byte `json:"hair_length"`
	HairStyle  byte `json:"hair_style"`
	FaceOption byte `json:"face_option"`

	// Equipment — armor slots (0-8 style index)
	Chest  byte `json:"chest"`
	Bracer byte `json:"bracer"`
	Gloves byte `json:"gloves"`
	Legs   byte `json:"legs"`
	Boots  byte `json:"boots"`
	Helm   byte `json:"helm"`
	Robe   byte `json:"robe"`

	// Equipment — weapon/shield model DictIDs
	PrimaryWeapon   uint32 `json:"primary_weapon"`
	SecondaryWeapon uint32 `json:"secondary_weapon"`
	Shield          uint32 `json:"shield"`

	// Tints (armor color indices)
	ChestTint  byte `json:"chest_tint"`
	BracerTint byte `json:"bracer_tint"`
	GlovesTint byte `json:"gloves_tint"`
	LegsTint   byte `json:"legs_tint"`
	BootsTint  byte `json:"boots_tint"`
	HelmTint   byte `json:"helm_tint"`
	RobeTint   byte `json:"robe_tint"`
}

func charSavePath(name string) string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, "eqoa", "saves", name+".json")
}

func loadCharSave(name string) (charSave, bool) {
	data, err := os.ReadFile(charSavePath(name))
	if err != nil {
		return charSave{}, false
	}
	var s charSave
	if json.Unmarshal(data, &s) != nil {
		return charSave{}, false
	}
	return s, true
}

func (c *Client) saveChar() {
	if c.engine == nil || c.offline != nil {
		return
	}
	p := &c.engine.State.Player
	name := p.Name
	if name == "" {
		return
	}
	s := charSave{
		// Position + camera
		X:        p.X,
		Y:        p.Y,
		Z:        p.Z,
		Facing:   p.Facing,
		CamDist:  c.camDist,
		CamPitch: c.camPitch,
		CamAngle: c.camAngle,

		// Identity
		ModelID: p.ModelID,
		Race:    p.Race,
		Class:   p.Class,
		Level:   p.Level,
		Sex:     p.Sex,

		// Appearance
		HairColor:  p.HairColor,
		HairLength: p.HairLength,
		HairStyle:  p.HairStyle,
		FaceOption: p.FaceOption,

		// Equipment
		Chest:  p.Chest,
		Bracer: p.Bracer,
		Gloves: p.Gloves,
		Legs:   p.Legs,
		Boots:  p.Boots,
		Helm:   p.Helm,
		Robe:   p.Robe,

		// Weapons
		PrimaryWeapon:   p.PrimaryWeapon,
		SecondaryWeapon: p.SecondaryWeapon,
		Shield:          p.Shield,

		// Tints
		ChestTint:  p.ChestTint,
		BracerTint: p.BracerTint,
		GlovesTint: p.GlovesTint,
		LegsTint:   p.LegsTint,
		BootsTint:  p.BootsTint,
		HelmTint:   p.HelmTint,
		RobeTint:   p.RobeTint,
	}
	path := charSavePath(name)
	os.MkdirAll(filepath.Dir(path), 0700)
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		log.Printf("CHARSAVE: failed to write %s: %v", path, err)
		return
	}
	log.Printf("CHARSAVE: saved %s at (%.1f, %.1f, %.1f) equip=[%d/%d/%d/%d/%d/%d] wpn=[0x%X/0x%X/0x%X]",
		name, p.X, p.Y, p.Z,
		p.Chest, p.Bracer, p.Gloves, p.Legs, p.Boots, p.Helm,
		p.PrimaryWeapon, p.SecondaryWeapon, p.Shield)
}

// equipFromSave builds an EquipmentState from saved character data for the
// character select 3D preview.
func equipFromSave(s charSave) *models.EquipmentState {
	// Sanitize equipment values: the server sends 0xFF (255) for "no equipment"
	// but the renderer treats any non-zero as "equipped". Clamp out-of-range
	// armor values to 0 (none = bare skin). Robe is NOT clamped: renderer
	// treats 0-3 as valid robe styles and >= 4 as "no robe".
	return &models.EquipmentState{
		Chest:      clampArmor(s.Chest),
		Bracer:     clampArmor(s.Bracer),
		Gloves:     clampArmor(s.Gloves),
		Legs:       clampArmor(s.Legs),
		Boots:      clampArmor(s.Boots),
		Helm:       clampArmor(s.Helm),
		Robe:       s.Robe,
		ChestTint:  s.ChestTint,
		BracerTint: s.BracerTint,
		GlovesTint: s.GlovesTint,
		LegsTint:   s.LegsTint,
		BootsTint:  s.BootsTint,
		HelmTint:   s.HelmTint,
		RobeTint:   s.RobeTint,
		HairColor:  s.HairColor,
		HairLength: s.HairLength,
		HairStyle:  s.HairStyle,
		FaceOption: s.FaceOption,
		Race:       s.Race,
		Sex:        s.Sex,
	}
}

// saveCharAt saves with explicit position (used during GM teleport).
func (c *Client) saveCharAt(x, y, z, facing float32) {
	if c.engine == nil {
		return
	}
	p := &c.engine.State.Player
	// Temporarily override position for save
	origX, origY, origZ, origF := p.X, p.Y, p.Z, p.Facing
	p.X, p.Y, p.Z, p.Facing = x, y, z, facing
	c.saveChar()
	p.X, p.Y, p.Z, p.Facing = origX, origY, origZ, origF
}
