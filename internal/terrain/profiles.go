package terrain

// TerrainProfile is one of the 22 terrain profiles VIWorld::SetProfiles
// hard-codes (decomp/src/viworld.cpp). The world's region grid assigns one to
// every sub-cell, and the four around the camera are blended
// (VIWorld::CalcProfileBlend) for the fog, the sky and the ambient track.
type TerrainProfile struct {
	FogNear   float32    // m_p00
	FogFar    float32    // m_p04, also the camera's far plane (VIScene::CalcFarPlane)
	Color     [3]float32 // m_color: tints the fog and the sky
	Sky       int        // m_p18: the default sky type
	Overcast  float32    // m_p1C: how much an overcast sky dims the lights
	Music     int        // m_p20: the ambient track pair (VISceneSoundTrack::m_tracks)
	Lavastorm float32    // m_p24: only profile 19 storms
}

func rgb(r, g, b float32) [3]float32 { return [3]float32{r / 255, g / 255, b / 255} }

var white = [3]float32{1, 1, 1}

// TerrainProfiles is VIWorld::SetProfiles.
var TerrainProfiles = [22]TerrainProfile{
	{250, 800, white, 1, 1, 9, 0},
	{0, 400, rgb(210, 230, 180), 1, 1, 4, 0},
	{250, 800, rgb(227, 247, 247), 0, 1, 6, 0},
	{250, 800, rgb(233, 218, 166), 0, 1, 3, 0},
	{150, 800, white, 2, 0.5, 5, 0},
	{0, 400, rgb(90, 140, 65), 0, 1, 7, 0},
	{0, 350, rgb(140, 170, 120), 2, 0.5, 10, 0},
	{250, 800, white, 1, 1, 2, 0},
	{250, 800, white, 1, 1, 1, 0},
	{0, 400, rgb(210, 230, 180), 1, 1, 1, 0},
	{250, 800, rgb(227, 247, 247), 0, 1, 1, 0},
	{250, 800, rgb(233, 218, 166), 0, 1, 1, 0},
	{150, 800, white, 2, 0.5, 1, 0},
	{0, 400, rgb(90, 140, 65), 0, 1, 1, 0},
	{0, 350, rgb(140, 170, 120), 2, 0.5, 1, 0},
	{250, 800, white, 1, 1, 8, 0},
	{250, 800, white, 1, 1, 1, 0},
	{0, 800, rgb(255, 170, 170), 0, 1, 3, 0},
	{0, 550, rgb(110, 140, 30), 1, 1, 8, 0},
	{0, 600, rgb(255, 170, 170), 2, 1, 8, 1},
	{250, 800, rgb(175, 200, 240), 1, 1, 6, 0},
	{0, 600, rgb(140, 170, 160), 1, 1, 7, 0},
}

// ProfileMix is a VITerrainProfileBlend: four profiles and their weights.
type ProfileMix struct {
	Index  [4]int
	Weight [4]float32
}

// DefaultProfileMix is profile 0 alone, for a world without a region grid.
var DefaultProfileMix = ProfileMix{Weight: [4]float32{1, 0, 0, 0}}

func (m ProfileMix) blend(f func(p *TerrainProfile) float32) float32 {
	var v float32
	for i := range m.Index {
		v += f(&TerrainProfiles[m.Index[i]]) * m.Weight[i]
	}
	return v
}

// ClipPlanes is VIScene::CalcTimeOfDayClipPlanes: the blended fog distances.
func (m ProfileMix) ClipPlanes() (near, far float32) {
	return m.blend(func(p *TerrainProfile) float32 { return p.FogNear }),
		m.blend(func(p *TerrainProfile) float32 { return p.FogFar })
}

// OvercastWeight is the blended m_p1C SetupTimeOfDayLights scales the
// overcast by.
func (m ProfileMix) OvercastWeight() float32 {
	return m.blend(func(p *TerrainProfile) float32 { return p.Overcast })
}

// Lavastorm is the blended m_p24 SetupTimeOfDaySky hands the atmosphere.
func (m ProfileMix) Lavastorm() float32 {
	return m.blend(func(p *TerrainProfile) float32 { return p.Lavastorm })
}

// Color is the blended profile tint (ColorScale + three ColorAddScaled).
func (m ProfileMix) Color() [3]float32 {
	var c [3]float32
	for i := range m.Index {
		p := &TerrainProfiles[m.Index[i]]
		for k := range c {
			c[k] += p.Color[k] * m.Weight[i]
		}
	}
	return c
}

// Heaviest returns the profile with the largest weight, the first on a tie —
// VIScene::SetSoundTrack picks the ambient track off it.
func (m ProfileMix) Heaviest() *TerrainProfile {
	k := 0
	for j := 1; j < 4; j++ {
		if m.Weight[k] < m.Weight[j] {
			k = j
		}
	}
	return &TerrainProfiles[m.Index[k]]
}

// ProfileBlend is VIWorld::CalcProfileBlend at (x, z).
func (l *Loader) ProfileBlend(x, z float32) ProfileMix {
	if l == nil || l.World == nil {
		return DefaultProfileMix
	}
	idx, w, ok := l.World.ProfileBlend(x, z)
	if !ok {
		return DefaultProfileMix
	}
	for i := range idx {
		if idx[i] < 0 || idx[i] >= len(TerrainProfiles) {
			idx[i] = 0
		}
	}
	return ProfileMix{Index: idx, Weight: w}
}
