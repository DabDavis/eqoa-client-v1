package audio

import (
	"log"
	"math"
	"strings"

	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

// BGMZone maps a world region to a background music file: a
// VIStreamAudioSprite's box, in the space of the actor that places it.
type BGMZone struct {
	MinX, MinY, MinZ float32 // the box, in the actor's own space
	MaxX, MaxY, MaxZ float32
	Path             string // e.g. "Music0\Torgiran.bgm"
	Loop             bool
	Priority         int32 // 0..2; higher wins (m_priority)

	// The placing actor's objectToWorld, VIMatrix44::Transform(hpr, scale,
	// pos): rows are the local axes scaled, then the position.
	Rows  [3][3]float32
	Pos   [3]float32
	Scale float32
}

// actorRows is VIMatrix44::Transform(v, scale, t) (vimatrix44.cpp:394)
// without the scale: the three unit rows. v is the actor's (x, y, z) rotation
// as ParseZoneActor reads it.
func actorRows(x, y, z float32) [3][3]float32 {
	si, ci := sincos(x)
	sj, cj := sincos(y)
	sh, ch := sincos(z)
	return [3][3]float32{
		{ch*ci + sh*sj*si, sh * cj, -ch*si + sh*sj*ci},
		{-sh*ci + ch*sj*si, ch * cj, sh*si + ch*sj*ci},
		{cj * si, -sj, cj * ci},
	}
}

func sincos(a float32) (float32, float32) {
	s, c := math.Sincos(float64(a))
	return float32(s), float32(c)
}

// local takes a world point into the zone's box space: the inverse of the
// actor transform (VIMatrix44::InvertAffine of objectToWorld).
func (bz *BGMZone) local(x, y, z float32) [3]float32 {
	d := [3]float32{x - bz.Pos[0], y - bz.Pos[1], z - bz.Pos[2]}
	var l [3]float32
	for i := 0; i < 3; i++ {
		l[i] = (d[0]*bz.Rows[i][0] + d[1]*bz.Rows[i][1] + d[2]*bz.Rows[i][2]) / bz.Scale
	}
	return l
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

			// The box stays in the actor's space; CheckBGMZone takes the
			// camera there. Adding the position alone dropped the scale (850
			// of Tunaria's 1,101 placements) and the rotation (321).
			pl := actor.Placement
			scale := pl.Scale
			if scale == 0 {
				continue // no placement on the disc has one; it would not invert
			}
			zones = append(zones, BGMZone{
				MinX: sas.MinX, MinY: sas.MinY, MinZ: sas.MinZ,
				MaxX: sas.MaxX, MaxY: sas.MaxY, MaxZ: sas.MaxZ,
				Path:     sas.Path,
				Loop:     sas.Loop,
				Priority: sas.Priority,
				Rows:     actorRows(pl.Rot.X, pl.Rot.Y, pl.Rot.Z),
				Pos:      [3]float32{pl.Pos.X, pl.Pos.Y, pl.Pos.Z},
				Scale:    scale,
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

// CheckBGMZone is VIScene::UpdateSoundEmitters' choice of streamed music at
// the CAMERA (x, y, z): of the zones whose box holds it, in the actor's own
// space, the highest priority wins and, among equals, the one whose box
// centre is nearest — VIStreamAudioSprite::QueryEmitters keys each on
// (priority + 1) − distance·0.0001 and the scene keeps the last of the
// highest priority. A priority outside 0..2 fails the PS2's whole query; it
// is skipped here (none is on the disc). Returns "" when no zone holds it.
//
// The PS2 asks only the actors listed in the camera's room; this asks all.
func CheckBGMZone(zones []BGMZone, x, y, z float32) string {
	best := -1
	var bestPri int32
	var bestKey float32
	for i := range zones {
		bz := &zones[i]
		if bz.Priority < 0 || bz.Priority > 2 {
			continue
		}
		p := bz.local(x, y, z)
		if p[0] < bz.MinX || p[0] > bz.MaxX || p[1] < bz.MinY || p[1] > bz.MaxY || p[2] < bz.MinZ || p[2] > bz.MaxZ {
			continue
		}
		cx := p[0] - (bz.MinX+bz.MaxX)*0.5
		cy := p[1] - (bz.MinY+bz.MaxY)*0.5
		cz := p[2] - (bz.MinZ+bz.MaxZ)*0.5
		key := float32(bz.Priority+1) - float32(math.Sqrt(float64(cx*cx+cy*cy+cz*cz)))*0.0001
		if best < 0 || bz.Priority > bestPri || (bz.Priority == bestPri && key >= bestKey) {
			best, bestPri, bestKey = i, bz.Priority, key
		}
	}
	if best < 0 {
		return ""
	}
	return zones[best].Path
}
