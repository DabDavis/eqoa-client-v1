package audio

import (
	"log"
	"strings"

	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

// BGMZone maps a world region to a background music file.
type BGMZone struct {
	MinX, MinY, MinZ float32
	MaxX, MaxY, MaxZ float32
	Path             string // e.g. "Music0\Torgiran.bgm"
	Loop             bool
}

// LoadBGMZones extracts StreamAudioSprite (0x2E00) objects from TUNARIA.ESF
// and returns a list of BGM zones with world-space bounding boxes.
//
// StreamAudioSprites are placed as zone actors — their bounding boxes are in
// local coordinates and must be offset by the actor's world position.
func LoadBGMZones(file *esf.ObjFile) []BGMZone {
	if file == nil {
		return nil
	}

	file.BuildDictionary()

	// Step 1: Parse all StreamAudioSprite definitions into a DictID → data map.
	sasMap := map[int32]*esf.StreamAudioSprite{}
	for _, info := range file.AllObjects() {
		if info.Type != esf.TypeStreamAudioSprite {
			continue
		}
		obj, err := file.GetObject(info)
		if err != nil {
			continue
		}
		sas, ok := obj.(*esf.StreamAudioSprite)
		if !ok || sas.Path == "" {
			continue
		}
		sasMap[sas.DictID] = sas
	}

	if len(sasMap) == 0 {
		return nil
	}

	// Step 2: Iterate all zone actors to find placements of StreamAudioSprites.
	// Each ZoneActor has a SpriteID (DictID) and world position.
	root, err := file.Root()
	if err != nil {
		return nil
	}
	world := root.Child(esf.TypeWorld)
	if world == nil {
		return nil
	}

	var zones []BGMZone
	for _, zoneInfo := range world.ChildrenOfType(esf.TypeZone) {
		zoneObj, err := file.GetObject(zoneInfo)
		if err != nil || zoneObj == nil {
			continue
		}
		zone := zoneObj.(*esf.Zone)
		actors, err := zone.GetZoneActors(file)
		if err != nil || actors == nil {
			continue
		}

		for _, actor := range actors.GetActors() {
			sas, ok := sasMap[actor.Placement.SpriteID]
			if !ok {
				continue
			}

			// Translate local bbox to world space by adding actor position.
			pos := actor.Placement.Pos
			zones = append(zones, BGMZone{
				MinX: sas.MinX + pos.X,
				MinY: sas.MinY + pos.Y,
				MinZ: sas.MinZ + pos.Z,
				MaxX: sas.MaxX + pos.X,
				MaxY: sas.MaxY + pos.Y,
				MaxZ: sas.MaxZ + pos.Z,
				Path: sas.Path,
				Loop: sas.Loop,
			})
		}
	}

	if len(zones) > 0 {
		unique := map[string]bool{}
		for _, z := range zones {
			unique[z.Path] = true
		}
		log.Printf("Audio: loaded %d BGM zones (%d unique tracks)", len(zones), len(unique))
	}

	return zones
}

// ResolveBGMName extracts and uppercases the BGM filename from a StreamAudioSprite
// path (e.g. "Music0\Torgiran.bgm" → "TORGIRAN.BGM").
func ResolveBGMName(sasPath string) string {
	name := sasPath
	if idx := strings.LastIndexAny(name, "\\/"); idx >= 0 {
		name = name[idx+1:]
	}
	return strings.ToUpper(name)
}

// CheckBGMZone checks if position (x, y, z) is inside any BGM zone.
// Returns the BGM path if found, empty string otherwise.
func CheckBGMZone(zones []BGMZone, x, y, z float32) string {
	for i := range zones {
		bz := &zones[i]
		if x >= bz.MinX && x <= bz.MaxX &&
			y >= bz.MinY && y <= bz.MaxY &&
			z >= bz.MinZ && z <= bz.MaxZ {
			return bz.Path
		}
	}
	return ""
}
