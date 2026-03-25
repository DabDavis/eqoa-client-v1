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

// PS2 VISceneSoundTrack::SetTracks (0x00471250) hardcodes two arrays:
// 12 "day" ambient tracks and 8 "night" ambient tracks (20 total = all XM modules).
// Track selection uses terrain profile blending (CalcProfileBlend) to pick the
// biome-appropriate track. For now we use zone index as a proxy for terrain type.
var (
	DayTrackIDs = [12]int32{
		-1705133364, // 0x9A5DBACC
		-1513750472, // 0xA5C60038
		-336400701,  // 0xEBF2EEC3
		-1868662553, // 0x909E78E7
		-1832277810, // 0x92C9A8CE
		-1596201910, // 0xA0DBE44A
		-1186844858, // 0xB9422F46
		803938549,   // 0x2FEB20F5
		-2041969022, // 0x864A0682
		1701799145,  // 0x656F64E9
		-614815237,  // 0xDB5AA9FB
		558085599,   // 0x2143B5DF
	}
	NightTrackIDs = [8]int32{
		1122422586,  // 0x42E6CF3A
		1928364566,  // 0x72F08216
		450674790,   // 0x1ADCC066
		1756659874,  // 0x68B480A2
		-1638519974, // 0x9E562B5A
		-822665418,  // 0xCEF71F36
		216312629,   // 0x0CE4AB35
		-42995991,   // 0xFD6FEEE9
	}
)

// SelectAmbientTrack picks a day or night ambient DictID based on region ID and time.
// regionID comes from WorldRegions (one per zone proxy, values 0-11 for day, 0-7 for night).
func SelectAmbientTrack(regionID int, isDusk bool) int32 {
	if isDusk {
		idx := regionID % len(NightTrackIDs)
		return NightTrackIDs[idx]
	}
	idx := regionID % len(DayTrackIDs)
	return DayTrackIDs[idx]
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
