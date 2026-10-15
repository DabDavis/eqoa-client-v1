package main

import (
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/eqoa/client/internal/game"
)

// Offline mode puts the client straight into the world with no server, so the
// rendering, animation and other client-side systems can be looked at without
// a session:
//
//	./client --offline [--iso PATH] [--char NAME] [--pos X,Y,Z] [--facing DEG]
//	         [--hour H] [--world N] [--weather TYPE] [--npc MODEL[:dead]]...
//	         [--npc-dist D] [--spell EFFECT] [--cam DIST,PITCHDEG,ANGLEDEG]
//	         [--size WxH] [--shot FILE.png [--shot-wait S] [--shot-frames N]]
//	         [assetsDir]
//
// --char loads ~/.config/eqoa/saves/NAME.json (position, camera, appearance,
// equipment); without it the player is a default human male at Freeport.
// Nothing is sent anywhere: no auth, patch, update check or presence
// heartbeat, and the character save is never written back.
//
// --shot captures the screen once the world has streamed in and exits, which
// is how a rendering change is checked without a display:
//
//	xvfb-run -a ./client --offline --char Dolph --hour 6,12,18 --shot /tmp/fp.png
//
// With several hours it writes one file per hour (fp_h06.png, fp_h12.png...);
// repeating an hour (--hour 12,12,12) takes a sequence --shot-frames frames
// apart (default 10), numbered fp_00_h12.png, fp_01_h12.png...
//
// --spell plays a SPELLFX effect (its DictID) from the player at the first
// NPC the moment the world is ready, and --npc-dist sets how far in front of
// the player the NPCs stand (default 3), so a projectile has room to fly.
type offlineConfig struct {
	iso        string
	size       [2]int32
	char       string
	pos        *[3]float32
	facing     *float32
	hours      []float32
	world      int
	weather    int
	npcs       []offlineNPC
	npcDist    float32
	spell      uint32
	cam        *[3]float32
	shot       string
	shotWait   float32
	shotFrames int

	// Capture sequencing.
	readyTime float32 // seconds the world has been ready
	shotIdx   int
	settle    int  // frames left before the next capture
	cast      bool // --spell has been played
}

type offlineNPC struct {
	model uint32
	dead  bool
}

// offlineDefaultPos is the Freeport square (GM location "Freeport Announcements").
var offlineDefaultPos = [3]float32{25305.6, 54.125, 15748.4}

// parseOfflineArgs takes the offline options out of args. It returns nil when
// --offline is absent, and the remaining (positional) arguments either way.
func parseOfflineArgs(args []string) (*offlineConfig, []string, error) {
	cfg := &offlineConfig{world: 0, weather: -1, shotWait: 1, shotFrames: 10, npcDist: 3}
	on := false
	var rest []string
	next := func(i *int, name string) (string, error) {
		if *i+1 >= len(args) {
			return "", fmt.Errorf("%s needs a value", name)
		}
		*i++
		return args[*i], nil
	}
	floats := func(s string, n int) ([]float32, error) {
		parts := strings.Split(s, ",")
		if n > 0 && len(parts) != n {
			return nil, fmt.Errorf("%q: want %d comma-separated numbers", s, n)
		}
		out := make([]float32, len(parts))
		for i, p := range parts {
			v, err := strconv.ParseFloat(strings.TrimSpace(p), 32)
			if err != nil {
				return nil, fmt.Errorf("%q: %v", s, err)
			}
			out[i] = float32(v)
		}
		return out, nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			rest = append(rest, a)
			continue
		}
		var v string
		var err error
		switch a {
		case "--offline":
			on = true
			continue
		case "--iso", "--size", "--char", "--pos", "--facing", "--hour", "--world", "--weather", "--npc", "--npc-dist", "--spell", "--cam", "--shot", "--shot-wait", "--shot-frames":
			if v, err = next(&i, a); err != nil {
				return nil, nil, err
			}
		default:
			return nil, nil, fmt.Errorf("unknown option %s", a)
		}
		switch a {
		case "--iso":
			cfg.iso = v
		case "--size":
			var w, h int32
			if _, err := fmt.Sscanf(v, "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
				return nil, nil, fmt.Errorf("--size %q: want WxH", v)
			}
			cfg.size = [2]int32{w, h}
		case "--char":
			cfg.char = v
		case "--pos":
			f, err := floats(v, 3)
			if err != nil {
				return nil, nil, err
			}
			cfg.pos = &[3]float32{f[0], f[1], f[2]}
		case "--facing":
			f, err := floats(v, 1)
			if err != nil {
				return nil, nil, err
			}
			rad := f[0] * math.Pi / 180
			cfg.facing = &rad
		case "--hour":
			if cfg.hours, err = floats(v, 0); err != nil {
				return nil, nil, err
			}
		case "--world":
			if cfg.world, err = strconv.Atoi(v); err != nil {
				return nil, nil, err
			}
		case "--weather":
			if cfg.weather, err = strconv.Atoi(v); err != nil {
				return nil, nil, err
			}
		case "--npc":
			spec, dead := strings.CutSuffix(v, ":dead")
			m, err := strconv.ParseUint(spec, 0, 32)
			if err != nil {
				return nil, nil, fmt.Errorf("--npc %q: %v", v, err)
			}
			cfg.npcs = append(cfg.npcs, offlineNPC{model: uint32(m), dead: dead})
		case "--npc-dist":
			f, err := floats(v, 1)
			if err != nil {
				return nil, nil, err
			}
			cfg.npcDist = f[0]
		case "--spell":
			h, err := strconv.ParseUint(v, 0, 32)
			if err != nil {
				return nil, nil, fmt.Errorf("--spell %q: %v", v, err)
			}
			cfg.spell = uint32(h)
		case "--cam":
			f, err := floats(v, 3)
			if err != nil {
				return nil, nil, err
			}
			cfg.cam = &[3]float32{f[0], f[1] * math.Pi / 180, f[2] * math.Pi / 180}
		case "--shot":
			cfg.shot = v
		case "--shot-wait":
			f, err := floats(v, 1)
			if err != nil {
				return nil, nil, err
			}
			cfg.shotWait = f[0]
		case "--shot-frames":
			if cfg.shotFrames, err = strconv.Atoi(v); err != nil {
				return nil, nil, err
			}
		}
	}
	if !on {
		return nil, rest, nil
	}
	if len(cfg.hours) == 0 {
		cfg.hours = []float32{12}
	}
	return cfg, rest, nil
}

// newOfflineEngine builds an engine that enters the world on its first Tick
// without a connection, with the player taken from the character save.
func (c *Client) newOfflineEngine() *game.Engine {
	o := c.offline
	e := game.NewEngine("", 0, 0, 0, "offline", "")
	e.Offline = true
	e.AutoConnect = false
	e.AssetsDir = c.assetsDir
	e.ISOPath = c.isoPath
	e.Audio = c.audioSys

	gs := e.State
	gs.PlayerObjectID = 1
	p := &gs.Player
	p.ObjectID = 1
	p.IsPlayer = true
	p.Scale = 1
	p.Level = 1
	p.Robe = 0xFF
	p.Chest, p.Bracer, p.Gloves, p.Legs, p.Boots, p.Helm = 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF
	p.ModelID = 0x70D898C6 // human male, as the state channel carries it
	p.X, p.Y, p.Z = offlineDefaultPos[0], offlineDefaultPos[1], offlineDefaultPos[2]
	p.Name = "Offline"

	if o.char != "" {
		s, ok := loadCharSave(o.char)
		if !ok {
			log.Printf("OFFLINE: no save for %q at %s — using the default character", o.char, charSavePath(o.char))
		} else {
			p.Name = o.char
			p.X, p.Y, p.Z, p.Facing = s.X, s.Y, s.Z, s.Facing
			p.ModelID, p.Race, p.Class, p.Level, p.Sex = s.ModelID, s.Race, s.Class, s.Level, s.Sex
			p.HairColor, p.HairLength, p.HairStyle, p.FaceOption = s.HairColor, s.HairLength, s.HairStyle, s.FaceOption
			p.Chest, p.Bracer, p.Gloves, p.Legs, p.Boots, p.Helm, p.Robe = s.Chest, s.Bracer, s.Gloves, s.Legs, s.Boots, s.Helm, s.Robe
			p.PrimaryWeapon, p.SecondaryWeapon, p.Shield = s.PrimaryWeapon, s.SecondaryWeapon, s.Shield
			p.ChestTint, p.BracerTint, p.GlovesTint, p.LegsTint, p.BootsTint, p.HelmTint, p.RobeTint =
				s.ChestTint, s.BracerTint, s.GlovesTint, s.LegsTint, s.BootsTint, s.HelmTint, s.RobeTint
		}
	}
	if p.Sex == 0 {
		p.Sex = game.SexFromModelID(p.ModelID)
	}
	if o.pos != nil {
		p.X, p.Y, p.Z = o.pos[0], o.pos[1], o.pos[2]
	}
	if o.facing != nil {
		p.Facing = *o.facing
	}
	gs.SetGameHour(o.hours[0])

	// NPCs stand in a row in front of the player (--npc-dist), facing back.
	fx, fz := float32(math.Sin(float64(p.Facing))), float32(math.Cos(float64(p.Facing)))
	for i, n := range o.npcs {
		side := float32(i) - float32(len(o.npcs)-1)/2
		ent := &game.Entity{
			ObjectID: uint32(1000 + i),
			Name:     fmt.Sprintf("NPC %d", i),
			ModelID:  n.model,
			X:        p.X + fx*o.npcDist + fz*side*2.5,
			Y:        p.Y,
			Z:        p.Z + fz*o.npcDist - fx*side*2.5,
			Facing:   p.Facing + math.Pi,
			Scale:    1,
			IsNPC:    true,
			Dead:     n.dead,
			Level:    1,
			Robe:     0xFF,
		}
		gs.Entities[ent.ObjectID] = ent
	}
	log.Printf("OFFLINE: %s model 0x%08X at (%.1f, %.1f, %.1f), hour %.1f, %d NPCs",
		p.Name, p.ModelID, p.X, p.Y, p.Z, o.hours[0], len(o.npcs))
	return e
}

// offlineEnterWorld applies what offline mode sets on the first in-world frame,
// after the normal world-entry code has run.
func (c *Client) offlineEnterWorld() {
	o := c.offline
	if o.size[0] > 0 {
		if c.windowMode != 0 {
			c.applyWindowModeImmediate(0)
		}
		screenWidth, screenHeight = o.size[0], o.size[1]
		rl.SetWindowSize(int(o.size[0]), int(o.size[1]))
	}
	if o.world != int(c.currentWorld) {
		c.switchWorldTerrain(byte(o.world))
	}
	if o.cam != nil {
		c.camDist, c.camPitch, c.camAngle = o.cam[0], o.cam[1], o.cam[2]
	}
	if o.weather >= 0 && c.weather != nil {
		c.weather.SetWeather(game.WeatherType(o.weather), true)
	}
}

// offlineShotTick runs after EndDrawing each frame. Once the world has streamed
// in and the loading overlay is gone it waits shotWait seconds, captures one
// image per --hour, and exits.
func (c *Client) offlineShotTick() {
	o := c.offline
	if o == nil || c.engine == nil || c.engine.State.Phase != game.PhaseInWorld {
		return
	}
	if o.spell != 0 && !o.cast && c.offlineWorldReady() {
		o.cast = true
		gs := c.engine.State
		target := gs.PlayerObjectID
		if len(o.npcs) > 0 {
			target = 1000
		}
		log.Printf("OFFLINE: playing spell effect 0x%08X at 0x%X", o.spell, target)
		c.engine.SpellFX.Play(o.spell, 0, gs.PlayerObjectID, target, time.Time{})
	}
	if o.shot == "" {
		return
	}
	if o.shotIdx == 0 && o.settle == 0 {
		if !c.offlineWorldReady() {
			o.readyTime = 0
			return
		}
		o.readyTime += rl.GetFrameTime()
		if o.readyTime < o.shotWait {
			return
		}
	}
	if o.settle > 0 {
		o.settle--
		return
	}

	path := o.shot
	if len(o.hours) > 1 {
		ext := filepath.Ext(path)
		base := strings.TrimSuffix(path, ext)
		repeated := false
		for i, h := range o.hours {
			for _, g := range o.hours[:i] {
				repeated = repeated || g == h
			}
		}
		if repeated {
			base = fmt.Sprintf("%s_%02d", base, o.shotIdx)
		}
		path = fmt.Sprintf("%s_h%02.0f%s", base, o.hours[o.shotIdx], ext)
	}
	img := rl.LoadImageFromScreen()
	rl.ExportImage(*img, path)
	rl.UnloadImage(img)
	log.Printf("OFFLINE: wrote %s (hour %.1f)", path, o.hours[o.shotIdx])

	o.shotIdx++
	if o.shotIdx >= len(o.hours) {
		rl.CloseWindow()
		os.Exit(0)
	}
	c.engine.State.SetGameHour(o.hours[o.shotIdx])
	o.settle = o.shotFrames // let the sky, lantern and lighting catch up
}

// offlineWorldReady reports whether every streamed zone is loaded and uploaded
// and the loading overlay has finished.
func (c *Client) offlineWorldReady() bool {
	if c.loadScrFade > 0 || c.terrainLoader == nil || c.terrainRender == nil || c.terrainZoneIdx < 0 {
		return false
	}
	for idx := range c.loadedZoneIdxs {
		if c.terrainLoader.GetZoneTerrain(idx) == nil || !c.terrainRender.IsZoneBuilt(idx) {
			return false
		}
	}
	return len(c.loadedZoneIdxs) > 0
}
