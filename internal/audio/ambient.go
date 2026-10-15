package audio

import (
	"fmt"
	"log"

	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

// AmbientZone maps a world region to a pre-rendered ambient track.
type AmbientZone struct {
	MinX, MinY, MinZ float32
	MaxX, MaxY, MaxZ float32
	DictID           int32
	Volume           float32
}

// AmbientTrack holds a pre-rendered ambient loop.
type AmbientTrack struct {
	PCM        []int16 // stereo interleaved PCM
	SampleRate int
	Volume     float32
}

// LoadAmbientTracks loads AMBTRACK.ESF, pre-renders all XM modules to PCM,
// and returns them keyed by DictID.
func LoadAmbientTracks(file *esf.ObjFile) map[int32]*AmbientTrack {
	if file == nil {
		return nil
	}
	file.BuildDictionary()

	tracks := make(map[int32]*AmbientTrack)
	for _, info := range file.AllObjects() {
		if info.Type != esf.TypeXm {
			continue
		}
		obj, err := file.GetObject(info)
		if err != nil {
			continue
		}
		xm, ok := obj.(*esf.Xm)
		if !ok {
			continue
		}

		mod := esf.ParseXmModule(xm)
		if mod == nil {
			continue
		}

		const sampleRate = 22050
		pcm := esf.RenderXmToWAV(mod, sampleRate)
		if len(pcm) == 0 {
			continue
		}

		tracks[xm.DictID] = &AmbientTrack{
			PCM:        pcm,
			SampleRate: sampleRate,
			Volume:     xm.Volume,
		}
	}

	if len(tracks) > 0 {
		log.Printf("Audio: loaded %d ambient tracks from AMBTRACK.ESF", len(tracks))
	}
	return tracks
}

// AmbientTrackPairs is VISceneSoundTrack::SetTracks: the (day, night) XM
// pair for each terrain-profile music index (TerrainProfile.Music). Index 0
// is silence. These are the twenty AMBTRACK.ESF modules, paired.
var AmbientTrackPairs = [11][2]int32{
	{0, 0},
	{-1705133364, -1513750472}, // 0x9A5DBACC, 0xA5C60038
	{-336400701, -1868662553},  // 0xEBF2EEC3, 0x909E78E7
	{-1832277810, -1596201910}, // 0x92C9A8CE, 0xA0DBE44A
	{-1186844858, 803938549},   // 0xB9422F46, 0x2FEB20F5
	{-2041969022, 1701799145},  // 0x864A0682, 0x656F64E9
	{-614815237, 558085599},    // 0xDB5AA9FB, 0x2143B5DF
	{1122422586, 1928364566},   // 0x42E6CF3A, 0x72F08216
	{450674790, 1756659874},    // 0x1ADCC066, 0x68B480A2
	{-1638519974, -822665418},  // 0x9E562B5A, 0xCEF71F36
	{216312629, -42995991},     // 0x0CE4AB35, 0xFD6FEEE9
}

// AmbientTrackFor is VIScene::SetSoundTrack's choice: the music index's day or
// night module (night is before 5:00 or after 20:00). 0 means silence.
func AmbientTrackFor(music int, night bool) int32 {
	if music < 0 || music >= len(AmbientTrackPairs) {
		return 0
	}
	if night {
		return AmbientTrackPairs[music][1]
	}
	return AmbientTrackPairs[music][0]
}

// CheckAmbientZone returns the DictID and volume of the ambient track for the given position.
// Returns 0 if no ambient zone contains the position.
func CheckAmbientZone(zones []AmbientZone, x, y, z float32) (int32, float32) {
	for _, az := range zones {
		if x >= az.MinX && x <= az.MaxX &&
			y >= az.MinY && y <= az.MaxY &&
			z >= az.MinZ && z <= az.MaxZ {
			return az.DictID, az.Volume
		}
	}
	return 0, 0
}

// AmbientKey returns a string key for dedup based on DictID.
func AmbientKey(dictID int32) string {
	return fmt.Sprintf("amb_%08X", uint32(dictID))
}
