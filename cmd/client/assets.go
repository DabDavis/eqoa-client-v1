package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/eqoa/client/internal/audio"
	"github.com/eqoa/client/internal/game"
	"github.com/eqoa/client/internal/models"
	"github.com/eqoa/client/internal/sky"
	"github.com/eqoa/client/internal/terrain"
	"github.com/eqoa/client/internal/ui"
	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

// openAssetESF opens a named ESF/CSF file, trying the ISO first (if configured),
// then falling back to the assets directory. CSF files are auto-decompressed.
func (c *Client) openAssetESF(name string) (*esf.ObjFile, error) {
	// Try ISO first.
	if c.isoPath != "" && esf.ISOHasFile(name) {
		f, err := esf.OpenISOFile(c.isoPath, name)
		if err == nil {
			log.Printf("assets: %s loaded from ISO", name)
			return f, nil
		}
		log.Printf("assets: ISO read %s failed: %v (trying file)", name, err)
	}

	// Try standalone file. CSF files need decompression first.
	path := filepath.Join(c.assetsDir, name)
	if _, err := os.Stat(path); err == nil {
		if filepath.Ext(name) == ".CSF" || filepath.Ext(name) == ".csf" {
			data, err := esf.DecompressCSF(path)
			if err != nil {
				return nil, fmt.Errorf("decompress %s: %w", name, err)
			}
			f, err := esf.OpenBytes(data)
			if err != nil {
				return nil, fmt.Errorf("parse %s: %w", name, err)
			}
			log.Printf("assets: %s loaded (decompressed)", name)
			return f, nil
		}
		f, err := esf.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", name, err)
		}
		return f, nil
	}

	// For files referenced as .ESF that only exist as .CSF on disk,
	// try the CSF version with decompression.
	if filepath.Ext(name) == ".ESF" {
		csfName := name[:len(name)-4] + ".CSF"
		csfPath := filepath.Join(c.assetsDir, csfName)
		if _, err := os.Stat(csfPath); err == nil {
			data, err := esf.DecompressCSF(csfPath)
			if err != nil {
				return nil, fmt.Errorf("decompress %s: %w", csfName, err)
			}
			f, err := esf.OpenBytes(data)
			if err != nil {
				return nil, fmt.Errorf("parse %s: %w", csfName, err)
			}
			log.Printf("assets: %s loaded from %s (decompressed)", name, csfName)
			return f, nil
		}
	}

	return nil, fmt.Errorf("%s not found (no ISO, no file in %s)", name, c.assetsDir)
}

func (c *Client) loadAssets(assetsDir string) {
	// Load saved GM teleport locations
	loadGMLocations()

	if f, err := c.openAssetESF("TUNARIA.ESF"); err != nil {
		log.Printf("loadAssets: terrain: %v", err)
	} else if loader, err := terrain.NewLoaderFromFile(f); err != nil {
		log.Printf("loadAssets: terrain loader: %v", err)
	} else {
		c.terrainLoader = loader
		c.terrainRender = terrain.NewRenderer()
		c.floraRender = terrain.NewFloraRenderer()
		c.floraRender.Attach(loader, c.terrainRender)
		overlayDir := filepath.Join(assetsDir, "overlays")
		loader.LoadZoneOverlays(overlayDir)
		log.Printf("loadAssets: terrain loader ready")

		// Load BGM zones from StreamAudioSprite objects in TUNARIA
		c.bgmZones = audio.LoadBGMZones(loader.File())

		// Load ambient tracks from AMBTRACK.ESF
		if ambFile, err := c.openAssetESF("AMBTRACK.ESF"); err == nil {
			c.ambientTracks = audio.LoadAmbientTracks(ambFile)
		}
	}

	charFile, err := c.openAssetESF("CHAR.ESF")
	if err != nil {
		log.Printf("loadAssets: CHAR.ESF: %v", err)
	} else {
		custFile, _ := c.openAssetESF("CHARCUST.CSF")
		faceFile, _ := c.openAssetESF("CHARFACE.CSF")
		if mgr, err := models.NewManagerFromFiles(charFile, custFile, faceFile); err != nil {
			log.Printf("loadAssets: character models: %v", err)
		} else {
			if c.audioSys != nil {
				mgr.SetSoundLoader(c.audioSys)
			}
			c.modelMgr = mgr
			log.Printf("loadAssets: character model manager ready")
		}
	}

	if c.modelMgr != nil {
		if itemFile, err := c.openAssetESF("ITEM.ESF"); err != nil {
			log.Printf("loadAssets: ITEM.ESF: %v", err)
		} else {
			if err := c.modelMgr.LoadItemESFFromFile(itemFile); err != nil {
				log.Printf("loadAssets: item models: %v", err)
			}
		}
	}

	if skyFile, err := c.openAssetESF("SKY.ESF"); err != nil {
		log.Printf("loadAssets: SKY.ESF: %v", err)
	} else if s := sky.NewSkyFromFile(skyFile); s != nil {
		c.skyDome = s
		log.Printf("loadAssets: sky dome ready")
	}

	// Load PS2 VIUI rendering system from UI.ESF (all 36 texture sheets).
	if uiFile, err := c.openAssetESF("UI.ESF"); err != nil {
		log.Printf("loadAssets: UI.ESF: %v", err)
	} else {
		c.viui = ui.NewVIUIFromFile(uiFile)
		c.loadReticleFromFile(uiFile)
		c.loadSpellIconsFromFile(uiFile)
	}

	// Load EQOA bitmap font from ARIAL.CSF.
	if fontFile, err := c.openAssetESF("ARIAL.CSF"); err != nil {
		log.Printf("loadAssets: ARIAL.CSF: %v", err)
	} else {
		if esfFont, ok := loadESFFont(fontFile); ok {
			c.font = esfFont
			log.Printf("loadAssets: EQOA font active")
		} else {
			log.Printf("loadAssets: ARIAL.CSF font parse failed")
		}
	}

	// Load item icon textures from ITEMICON.ESF.
	c.loadItemIcons()

	// Initialize zone effect volume system.
	c.effectVolumes = game.NewEffectVolumeSystem()

	// Initialize weather system and load PS2 VIAtmosphere textures from SKY.ESF.
	c.weather = game.NewWeatherSystem()
	c.loadWeatherTextures()
	c.loadWeatherSounds()
}

// loadWeatherSounds loads rain/thunder sounds from SKY.ESF into the audio system.
// PS2 VIAtmosphere::SetResources: "rain_lt" (looping), "lightning_far02" (one-shot).
func (c *Client) loadWeatherSounds() {
	if c.weather == nil || c.audioSys == nil {
		return
	}
	f, err := c.openAssetESF("SKY.ESF")
	if err != nil {
		return
	}
	// rain_lt and lightning_far02 are bare 0xB000 Adpcm objects, which the
	// SoundSprite loader never registered: rain and thunder never played.
	n := c.audioSys.LoadAdpcmSounds(f)
	if n > 0 {
		log.Printf("loadWeatherSounds: loaded %d sounds from SKY.ESF", n)
	}
}

// loadReticleFromFile loads both Selection_Rings textures from a pre-opened UI.ESF.
// PS2 HSprite has 2 surfaces: main ring + twinkle sparkle overlay.
func (c *Client) loadReticleFromFile(uiFile *esf.ObjFile) {
	const (
		ringDictID    int32 = 476495583   // Surface 0x1C66BEDF — main ring
		twinkleDictID int32 = -685926708  // Surface 0xD71D96CC — twinkle/sparkle
	)

	// Load main ring texture.
	if obj, err := uiFile.FindObject(ringDictID); err == nil && obj != nil {
		if surf, ok := obj.(*esf.Surface); ok && surf != nil && surf.Image != nil {
			rlImg := rl.NewImageFromImage(surf.Image)
			c.reticleTex = rl.LoadTextureFromImage(rlImg)
			rl.UnloadImage(rlImg)
			rl.SetTextureFilter(c.reticleTex, rl.FilterBilinear)
			c.reticleOK = true
			log.Printf("loadReticle: ring loaded (%dx%d)", surf.Image.Bounds().Dx(), surf.Image.Bounds().Dy())
		}
	}

	// Load twinkle/sparkle texture.
	if obj, err := uiFile.FindObject(twinkleDictID); err == nil && obj != nil {
		if surf, ok := obj.(*esf.Surface); ok && surf != nil && surf.Image != nil {
			rlImg := rl.NewImageFromImage(surf.Image)
			c.twinkleTex = rl.LoadTextureFromImage(rlImg)
			rl.UnloadImage(rlImg)
			rl.SetTextureFilter(c.twinkleTex, rl.FilterBilinear)
			c.twinkleOK = true
			log.Printf("loadReticle: twinkle loaded (%dx%d)", surf.Image.Bounds().Dx(), surf.Image.Bounds().Dy())
		}
	}
}

// loadIconsFromFile loads surface textures from a pre-opened ESF file.
// filter is called with (surfaceIndex, *esf.Surface) and returns true to include the surface.
func loadIconsFromFile(f *esf.ObjFile, filter func(int, *esf.Surface) bool) (map[int32]rl.Texture2D, []int32) {
	texMap := make(map[int32]rl.Texture2D)
	var texList []int32

	allInfos := f.AllObjects()
	surfIdx := 0
	for _, info := range allInfos {
		if info.Type != esf.TypeSurface {
			continue
		}
		idx := surfIdx
		surfIdx++

		obj, err := f.GetObject(info)
		if err != nil {
			continue
		}
		surf, ok := obj.(*esf.Surface)
		if !ok || surf == nil || surf.Image == nil {
			continue
		}

		if filter != nil && !filter(idx, surf) {
			continue
		}

		rlImg := rl.NewImageFromImage(surf.Image)
		tex := rl.LoadTextureFromImage(rlImg)
		rl.UnloadImage(rlImg)
		rl.SetTextureFilter(tex, rl.FilterBilinear)

		texMap[info.DictID] = tex
		texList = append(texList, info.DictID)
	}

	sort.Slice(texList, func(i, j int) bool { return texList[i] < texList[j] })
	log.Printf("loadIcons: %d icons loaded", len(texMap))
	return texMap, texList
}

// loadSpellIconsFromFile loads 32x32 ability icon textures from a pre-opened UI.ESF.
func (c *Client) loadSpellIconsFromFile(uiFile *esf.ObjFile) {
	c.iconCacheSpell = make(map[int64]rl.Texture2D)
	c.iconTextures, c.iconList = loadIconsFromFile(
		uiFile,
		func(idx int, surf *esf.Surface) bool {
			if idx < 29 {
				return false // skip HUD texture sheets
			}
			b := surf.Image.Bounds()
			return b.Dx() == 32 && b.Dy() == 32
		},
	)
}

// loadItemIcons loads item icon textures from ITEMICON.ESF.
func (c *Client) loadItemIcons() {
	c.itemIconCache = make(map[int64]rl.Texture2D)
	f, err := c.openAssetESF("ITEMICON.ESF")
	if err != nil {
		log.Printf("loadItemIcons: %v", err)
		c.itemIconTextures = make(map[int32]rl.Texture2D)
		return
	}
	// Trigger ESF parsing — AllObjects() returns empty until Root() is called.
	f.Root()
	c.itemIconTextures, c.itemIconList = loadIconsFromFile(f, nil)
}

// loadWeatherTextures loads rain/snow/splash textures from SKY.ESF.
// PS2 VIAtmosphere::SetResources loads these by name via VIHashResourceID.
func (c *Client) loadWeatherTextures() {
	if c.weather == nil {
		return
	}
	const (
		rainDictID   int32 = 1567384092
		snowDictID   int32 = -2064041545
		splashDictID int32 = 467371099
	)

	f, err := c.openAssetESF("SKY.ESF")
	if err != nil {
		log.Printf("loadWeatherTextures: %v", err)
		return
	}
	if err := f.BuildDictionary(); err != nil {
		log.Printf("loadWeatherTextures: SKY.ESF dict: %v", err)
		return
	}

	load := func(dictID int32, name string) rl.Texture2D {
		obj, err := f.FindObject(dictID)
		if err != nil || obj == nil {
			log.Printf("loadWeatherTextures: %s not found (dict %d): %v", name, dictID, err)
			return rl.Texture2D{}
		}
		surf, ok := obj.(*esf.Surface)
		if !ok || surf == nil || surf.Image == nil {
			log.Printf("loadWeatherTextures: %s is not a Surface", name)
			return rl.Texture2D{}
		}
		rlImg := rl.NewImageFromImage(surf.Image)
		tex := rl.LoadTextureFromImage(rlImg)
		rl.UnloadImage(rlImg)
		rl.SetTextureFilter(tex, rl.FilterBilinear)
		log.Printf("loadWeatherTextures: %s loaded (%dx%d)", name, surf.Image.Bounds().Dx(), surf.Image.Bounds().Dy())
		return tex
	}

	rain := load(rainDictID, "rain.tga")
	snow := load(snowDictID, "snow.tga")
	splash := load(splashDictID, "splash.tga")
	c.weather.SetTextures(rain, snow, splash)
}

// worldESFName maps PS2 world IDs to ESF filenames.
var worldESFName = map[byte]string{
	0: "TUNARIA.ESF",
	1: "RATHE.ESF",
	2: "ODUS.ESF",
	3: "LAVASTM.ESF",
	4: "PLANESKY.ESF",
	5: "SECRETS.ESF",
}

// switchWorldTerrain unloads the current terrain and loads a new world's ESF.
func (c *Client) switchWorldTerrain(worldID byte) {
	esfName, ok := worldESFName[worldID]
	if !ok {
		log.Printf("switchWorldTerrain: unknown world ID %d", worldID)
		return
	}

	log.Printf("switchWorldTerrain: loading %s (world %d)", esfName, worldID)

	// Unload all current zones from GPU.
	if c.terrainRender != nil {
		for idx := range c.loadedZoneIdxs {
			c.terrainRender.ResetZone(idx)
		}
	}
	if c.floraRender != nil {
		c.floraRender.Reset()
	}
	if c.effectVolumes != nil {
		c.effectVolumes.Clear()
	}
	if c.engine != nil && c.engine.SpellFX != nil {
		c.engine.SpellFX.StopAll()
	}
	c.boneEmitters = make(map[uint32][]boneEmitterEntry)
	c.weaponEmitters = make(map[uint32][]weaponEmitterEntry)
	c.loadedZoneIdxs = make(map[int]bool)
	c.terrainZoneIdx = -1
	c.terrainStarted = false
	c.safeValid = false
	c.zoneParticlesSpawned = false

	// Close the previous world's loader — stops its load worker goroutine and
	// releases the old world ESF file handle (plus any overlay handles). Without
	// this, every world switch leaks a goroutine and an OS file handle. (#5)
	if c.terrainLoader != nil {
		c.terrainLoader.Close()
		c.terrainLoader = nil
	}

	f, err := c.openAssetESF(esfName)
	if err != nil {
		log.Printf("switchWorldTerrain: %v — falling back to no terrain", err)
		c.terrainLoader = nil
		return
	}

	loader, err := terrain.NewLoaderFromFile(f)
	if err != nil {
		log.Printf("switchWorldTerrain: %v — falling back to no terrain", err)
		c.terrainLoader = nil
		return
	}

	overlayDir := filepath.Join(c.assetsDir, "overlays")
	loader.LoadZoneOverlays(overlayDir)

	c.terrainLoader = loader
	if c.floraRender != nil {
		c.floraRender.Attach(loader, c.terrainRender) // a new world's flora
	}
	c.currentWorld = worldID
	proxies := loader.GetZoneProxies()
	proxyCount := 0
	if proxies != nil {
		proxyCount = len(proxies.Zones)
	}
	log.Printf("switchWorldTerrain: loaded %s (%d zone proxies)", esfName, proxyCount)
}

// --- PS2 VICSprite::NameColors table (EE 0x004E0BF8) ---
// 3 entries x RGBA, extracted from VICSprite::SetName disassembly at 0x00422038.
// Each byte is converted to float via x (1/255) for VIColor32F on PS2.
var nameColors = [3]rl.Color{
	{R: 212, G: 192, B: 111, A: 255}, // [0] NPC -- golden yellow
	{R: 100, G: 175, B: 220, A: 255}, // [1] Player/Pet -- steel blue
	{R: 90, G: 255, B: 80, A: 255},   // [2] GM/special -- bright green
}

// nameColorFromByte maps the state channel NameColor byte (offset 142)
// to a NameColors table entry, matching the CLIENT code translation:
//
//	byte 0 -> index 0 (NPC)
//	byte 2 -> index 1 (Player)
//	other  -> index 0 (fallback to NPC)
func nameColorFromByte(b byte) rl.Color {
	switch b {
	case 2:
		return nameColors[1]
	case 1:
		return nameColors[2]
	default:
		return nameColors[0]
	}
}

// drawNameplate renders an entity name at a screen position.
// VIFont::DrawCenteredString -- center name horizontally.
func (c *Client) drawNameplate(screenPos rl.Vector2, name string, clr rl.Color) {
	const fontSize = int32(14)
	nameW := c.measureText(name, fontSize)
	nx := int32(screenPos.X) - nameW/2
	ny := int32(screenPos.Y)
	c.drawText(name, nx, ny, fontSize, clr)
}

// entityEquipment builds an EquipmentState from an Entity's armor/appearance bytes.
// mgr is used to read the ESF Race from the loaded CSprite (PS2 VICSprite::GetRace).
func entityEquipment(ent *game.Entity, mgr *models.ModelManager) *models.EquipmentState {
	// The server's bytes go through its client's rules (SetArmorSlot's set
	// clamp, Sandstorm's robes); transmog is applied after, so its own sets
	// are not clamped.
	r := models.ActiveRules
	return &models.EquipmentState{
		Chest:      r.ClampArmorSet(ent.Chest),
		Bracer:     r.ClampArmorSet(ent.Bracer),
		Gloves:     r.ClampArmorSet(ent.Gloves),
		Legs:       r.ClampArmorSet(ent.Legs),
		Boots:      r.ClampArmorSet(ent.Boots),
		Helm:       r.ClampArmorSet(ent.Helm),
		Robe:       r.Robe(ent.Robe),
		ChestTint:  ent.ChestTint,
		BracerTint: ent.BracerTint,
		GlovesTint: ent.GlovesTint,
		LegsTint:   ent.LegsTint,
		BootsTint:  ent.BootsTint,
		HelmTint:   ent.HelmTint,
		RobeTint:   ent.RobeTint,
		HairColor:  ent.HairColor,
		HairLength: ent.HairLength,
		HairStyle:  ent.HairStyle,
		FaceOption: ent.FaceOption,
		Race:       deriveRace(ent, mgr),
		Sex:        deriveSex(ent, mgr),
	}
}

// deriveSex is the sex the entity's face is chosen by. VICSprite::SetFace
// reads it from the model's header; the state channel carries none, and
// ent.Sex is only set by opcode 0x00B3, which no server sends — so every
// woman on the channel got a man's face.
func deriveSex(ent *game.Entity, mgr *models.ModelManager) byte {
	if mgr != nil {
		if s := mgr.ModelSex(int32(ent.ModelID)); s >= 0 {
			return byte(s)
		}
	}
	if s := game.SexFromModelID(ent.ModelID); s != 0 {
		return s
	}
	return ent.Sex
}

// deriveRace returns the race matching the entity's current model.
// PS2 reads Race from the ESF CSprite data (VICSprite::GetRace at 0x00506968).
// When an illusion changes ModelID, this returns the illusion model's race
// so face/skin textures match the rendered model.
// The result is always in CSprite.Race space, because that is what
// models.GetFaceTexture expects. The fallback therefore converts the network
// race byte rather than returning it raw: the two enums agree for 0-8 but the
// network puts Ogre at 9, where CSprite.Race uses 9 as its "unset" sentinel and
// puts Ogre at 10.
func deriveRace(ent *game.Entity, mgr *models.ModelManager) byte {
	// Prefer ESF CSprite Race (PS2-accurate, stored during model load).
	if mgr != nil {
		if r := mgr.ModelRace(int32(ent.ModelID)); r >= 0 {
			return byte(r)
		}
	}
	return networkRaceToCSpriteRace(ent.Race)
}

// networkRaceToCSpriteRace converts a wire race byte (HUM=0 … OGR=9) into the
// CSprite.Race space the model tables are keyed on. Used only until the model
// loads and its real Race is known.
func networkRaceToCSpriteRace(netRace byte) byte {
	if netRace == 9 {
		return 10 // Ogre
	}
	return netRace
}

// streaming winEnd fix
