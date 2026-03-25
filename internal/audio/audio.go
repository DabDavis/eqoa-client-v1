package audio

import (
	"encoding/binary"
	"log"
	"math"
	"path/filepath"
	"sync"
	"unsafe"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

// soundData holds decoded PCM data for creating independent Sound instances.
type soundData struct {
	pcm        []int16
	sampleRate int32
}

// System manages all audio playback for the EQOA client.
type System struct {
	mu       sync.Mutex
	sounds   map[int32]*soundData // DictID → decoded PCM data
	active   []*ActiveSound       // currently playing sounds
	listener [3]float32           // camera/player position
	inited   bool
	sfxMuted  bool                // true = spell/SFX sounds muted
	sfxVolume float32             // SFX volume multiplier 0-1 (default 1.0)

	// BGM state
	bgmMusic   rl.Music        // currently playing BGM (streaming via raylib Music)
	bgmWavData []byte          // WAV data kept alive for Music streaming
	bgmActive  bool            // true if bgm is loaded and playing
	bgmPath    string          // path of currently playing BGM
	bgmVol     float32         // BGM volume (0-1)

	// Ambient track state (plays alongside BGM)
	ambMusic   rl.Music        // currently playing ambient loop
	ambWavData []byte          // WAV data kept alive for Music streaming
	ambActive  bool
	ambKey     string          // current ambient track key
	ambVol     float32
}

// ActiveSound tracks a playing sound instance with 3D position.
type ActiveSound struct {
	DictID   int32
	Sound    rl.Sound
	Position [3]float32 // world position of source
	Volume   float32    // base volume (0-1)
	MaxDist  float32    // max audible distance
	ID       int32      // spell event ID for matching
	Loop     bool       // if true, restart when finished (continuous sounds)
}

// NewSystem creates the audio system and initializes the raylib audio device.
func NewSystem() *System {
	rl.InitAudioDevice()
	rl.SetAudioStreamBufferSizeDefault(4096) // larger buffer for smoother streaming
	return &System{
		sounds:    make(map[int32]*soundData),
		sfxVolume: 1.0,
		inited:    true,
	}
}

// Close shuts down the audio system and frees all loaded sounds.
func (s *System) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Stop BGM and ambient
	s.stopBGMLocked()
	s.stopAmbientLocked()

	// Unload all active sound instances
	for _, a := range s.active {
		rl.StopSound(a.Sound)
		rl.UnloadSound(a.Sound)
	}
	s.active = nil
	s.sounds = nil

	if s.inited {
		rl.CloseAudioDevice()
		s.inited = false
	}
}

// LoadSpellSounds extracts all SoundSprite objects from a SPELLFX ObjFile
// and loads them as playable sounds.
func (s *System) LoadSpellSounds(file *esf.ObjFile) int {
	if file == nil {
		return 0
	}

	count := 0
	for _, info := range file.AllObjects() {
		if info.Type != esf.TypeSoundSprite {
			continue
		}

		dictID, sd, ok := s.parseSoundSprite(file, info)
		if !ok {
			continue
		}

		s.mu.Lock()
		s.sounds[dictID] = sd
		s.mu.Unlock()
		count++
	}

	if count > 0 {
		log.Printf("Audio: loaded %d spell sounds", count)
	}
	return count
}

// LoadModelSounds decodes and registers SoundClips from a CSprite for playback.
// Returns the number of sounds loaded.
func (s *System) LoadModelSounds(clips []esf.SoundClip) int {
	if s == nil || len(clips) == 0 {
		return 0
	}

	count := 0
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, clip := range clips {
		if _, exists := s.sounds[clip.DictID]; exists {
			count++
			continue // already loaded
		}
		pcm := DecodeVAG(clip.RawVAG)
		if len(pcm) == 0 {
			continue
		}
		sr := clip.SampleRate
		if sr <= 0 {
			sr = 11025
		}
		s.sounds[clip.DictID] = &soundData{pcm: pcm, sampleRate: sr}
		count++
	}
	return count
}

// parseSoundSprite extracts DictID and audio data from a SoundSprite ESF object.
func (s *System) parseSoundSprite(file *esf.ObjFile, info *esf.ObjInfo) (int32, *soundData, bool) {
	var dictID int32

	// Read SoundSpriteHeader (0xb101) for DictID
	for _, ch := range info.Children {
		if ch.Type == 0xb101 {
			data := file.RawBytes(ch.Offset, int(ch.Size))
			if len(data) >= 4 {
				dictID = int32(binary.LittleEndian.Uint32(data[:4]))
			}
			break
		}
	}

	if dictID == 0 {
		return 0, nil, false
	}

	// Find Adpcm child (0xb000)
	for _, ch := range info.Children {
		if ch.Type != esf.TypeAdpcm {
			continue
		}

		params, vagData := s.parseAdpcm(file, ch)
		if vagData == nil {
			continue
		}

		// Decode VAG ADPCM → PCM
		pcm := DecodeVAG(vagData)
		if len(pcm) == 0 {
			continue
		}

		return dictID, &soundData{pcm: pcm, sampleRate: params.SampleRate}, true
	}

	return 0, nil, false
}

// adpcmParams holds parsed AdpcmHeader fields (PS2: 7 × int32/float32 = 28 bytes).
type adpcmParams struct {
	DictID     int32
	Channels   int32
	NumBlocks  int32
	SampleRate int32
	Volume1    float32
	Volume2    float32
	LoopStart  int32
}

// parseAdpcm reads the AdpcmHeader (0xb010) and raw audio data (0xb020) children.
func (s *System) parseAdpcm(file *esf.ObjFile, info *esf.ObjInfo) (adpcmParams, []byte) {
	var params adpcmParams
	params.SampleRate = 11025 // default
	params.Volume1 = 1.0
	params.Volume2 = 1.0

	for _, ch := range info.Children {
		if ch.Type == 0xb010 {
			data := file.RawBytes(ch.Offset, int(ch.Size))
			if len(data) >= 16 {
				params.DictID = int32(binary.LittleEndian.Uint32(data[0:4]))
				params.Channels = int32(binary.LittleEndian.Uint32(data[4:8]))
				params.NumBlocks = int32(binary.LittleEndian.Uint32(data[8:12]))
				params.SampleRate = int32(binary.LittleEndian.Uint32(data[12:16]))
			}
			if len(data) >= 28 {
				params.Volume1 = math.Float32frombits(binary.LittleEndian.Uint32(data[16:20]))
				params.Volume2 = math.Float32frombits(binary.LittleEndian.Uint32(data[20:24]))
				params.LoopStart = int32(binary.LittleEndian.Uint32(data[24:28]))
			}
		}
	}

	// Raw VAG data is in the 0xb020 child
	for _, ch := range info.Children {
		if ch.Type == 0xb020 {
			if ch.Size > 0 {
				return params, file.RawBytes(ch.Offset, int(ch.Size))
			}
		}
	}

	return params, nil
}

// newSoundInstance creates a fresh raylib Sound from stored PCM data.
// Each call returns an independent playback handle that must be UnloadSound'd.
func newSoundInstance(sd *soundData) rl.Sound {
	wave := rl.Wave{
		FrameCount: uint32(len(sd.pcm)),
		SampleRate: uint32(sd.sampleRate),
		SampleSize: 16,
		Channels:   1,
		Data:       unsafe.Pointer(&sd.pcm[0]),
	}
	return rl.LoadSoundFromWave(wave)
}

// Play starts playing a sound by DictID with optional 3D position.
// Each call creates an independent sound instance so overlapping plays work.
func (s *System) Play(dictID int32, id int32, pos [3]float32, volume, maxDist float32) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.sfxMuted {
		return
	}

	sd, ok := s.sounds[dictID]
	if !ok {
		return
	}

	// Create independent sound instance
	snd := newSoundInstance(sd)

	// Calculate distance-based volume, scaled by SFX volume
	vol := s.calcVolume(pos, volume, maxDist) * s.sfxVolume
	pan := s.calcPan(pos)

	rl.SetSoundVolume(snd, vol)
	rl.SetSoundPan(snd, pan)
	rl.PlaySound(snd)

	s.active = append(s.active, &ActiveSound{
		DictID:   dictID,
		Sound:    snd,
		Position: pos,
		Volume:   volume,
		MaxDist:  maxDist,
		ID:       id,
	})
}

// PlayLoop starts a looping sound by DictID. The sound will restart when finished.
// Use StopLoop to stop it. Returns true if the sound was started.
func (s *System) PlayLoop(dictID int32, id int32, pos [3]float32, volume, maxDist float32) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.sfxMuted {
		return false
	}

	sd, ok := s.sounds[dictID]
	if !ok {
		return false
	}

	snd := newSoundInstance(sd)
	vol := s.calcVolume(pos, volume, maxDist) * s.sfxVolume
	pan := s.calcPan(pos)

	rl.SetSoundVolume(snd, vol)
	rl.SetSoundPan(snd, pan)
	rl.PlaySound(snd)

	s.active = append(s.active, &ActiveSound{
		DictID:   dictID,
		Sound:    snd,
		Position: pos,
		Volume:   volume,
		MaxDist:  maxDist,
		ID:       id,
		Loop:     true,
	})
	return true
}

// StopLoop stops a looping sound by ID.
func (s *System) StopLoop(id int32) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := len(s.active) - 1; i >= 0; i-- {
		if s.active[i].ID == id && s.active[i].Loop {
			rl.StopSound(s.active[i].Sound)
			rl.UnloadSound(s.active[i].Sound)
			s.active = append(s.active[:i], s.active[i+1:]...)
			return
		}
	}
}

// UpdateLoopPosition updates the position of a looping sound.
func (s *System) UpdateLoopPosition(id int32, pos [3]float32) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, a := range s.active {
		if a.ID == id && a.Loop {
			a.Position = pos
			return
		}
	}
}

// Stop stops a playing sound by spell event ID.
func (s *System) Stop(id int32) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := len(s.active) - 1; i >= 0; i-- {
		if s.active[i].ID == id {
			rl.StopSound(s.active[i].Sound)
			rl.UnloadSound(s.active[i].Sound)
			s.active = append(s.active[:i], s.active[i+1:]...)
			return
		}
	}
}

// SetListener updates the listener (camera/player) position for 3D audio.
func (s *System) SetListener(x, y, z float32) {
	s.mu.Lock()
	s.listener = [3]float32{x, y, z}
	s.mu.Unlock()
}

// Update should be called each frame to update active sound volumes based on distance,
// and clean up finished sounds.
func (s *System) Update() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := len(s.active) - 1; i >= 0; i-- {
		a := s.active[i]
		if !rl.IsSoundPlaying(a.Sound) {
			if a.Loop {
				// Restart looping sounds
				rl.PlaySound(a.Sound)
				continue
			}
			rl.UnloadSound(a.Sound)
			s.active = append(s.active[:i], s.active[i+1:]...)
			continue
		}

		// Update volume/pan based on current listener position
		vol := s.calcVolume(a.Position, a.Volume, a.MaxDist) * s.sfxVolume
		pan := s.calcPan(a.Position)
		rl.SetSoundVolume(a.Sound, vol)
		rl.SetSoundPan(a.Sound, pan)
	}
}

// calcVolume computes distance-attenuated volume.
func (s *System) calcVolume(pos [3]float32, baseVol, maxDist float32) float32 {
	if maxDist <= 0 {
		return baseVol
	}

	dx := pos[0] - s.listener[0]
	dy := pos[1] - s.listener[1]
	dz := pos[2] - s.listener[2]
	dist := float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))

	if dist >= maxDist {
		return 0
	}

	// Linear attenuation
	atten := 1.0 - dist/maxDist
	return baseVol * atten
}

// calcPan computes stereo pan from source position relative to listener.
// Returns 0.0 (full left) to 1.0 (full right), 0.5 = center.
func (s *System) calcPan(pos [3]float32) float32 {
	dx := pos[0] - s.listener[0]
	dz := pos[2] - s.listener[2]
	dist := float32(math.Sqrt(float64(dx*dx + dz*dz)))

	if dist < 0.01 {
		return 0.5 // center
	}

	// Simple left/right based on X offset, clamped
	pan := 0.5 + (dx/dist)*0.4
	if pan < 0 {
		pan = 0
	} else if pan > 1 {
		pan = 1
	}
	return pan
}

// HasSound returns true if a sound with the given DictID is loaded.
func (s *System) HasSound(dictID int32) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.sounds[dictID]
	return ok
}

// IsPlaying returns true if any active sound has the given ID.
func (s *System) IsPlaying(id int32) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.active {
		if a.ID == id && rl.IsSoundPlaying(a.Sound) {
			return true
		}
	}
	return false
}

// SoundCount returns the number of loaded sounds.
func (s *System) SoundCount() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sounds)
}

// PlayBGMData plays BGM from raw VAG ADPCM bytes (read from ISO).
// key is used to identify the track for dedup (e.g. "BARBARIA.BGM").
func (s *System) PlayBGMData(key string, data []byte, volume float32) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.bgmActive && s.bgmPath == key {
		return
	}

	s.playBGMDataLocked(key, data, volume)
}

func (s *System) playBGMDataLocked(key string, data []byte, volume float32) {
	s.stopBGMLocked()

	// BGM files are block-interleaved stereo VAG at 44100 Hz.
	// Decode to PCM, wrap in WAV header, use raylib Music for streaming playback.
	pcm := DecodeVAGStereo(data)
	if len(pcm) == 0 {
		log.Printf("Audio: BGM decode produced no samples: %s", key)
		return
	}

	// Validate PCM data — check for anomalies that cause pops/tones.
	maxSample := int16(0)
	minSample := int16(0)
	clipped := 0
	for _, s := range pcm {
		if s > maxSample {
			maxSample = s
		}
		if s < minSample {
			minSample = s
		}
		if s == 32767 || s == -32768 {
			clipped++
		}
	}
	if clipped > 100 {
		log.Printf("Audio: WARNING BGM %s has %d clipped samples (max=%d min=%d) — may cause distortion",
			filepath.Base(key), clipped, maxSample, minSample)
	}

	wavData := pcmToWAV(pcm, 44100, 2)
	music := rl.LoadMusicStreamFromMemory(".wav", wavData, int32(len(wavData)))
	if !rl.IsMusicValid(music) {
		log.Printf("Audio: ERROR BGM %s LoadMusicStreamFromMemory returned invalid Music!", filepath.Base(key))
		return
	}
	music.Looping = true
	rl.SetMusicVolume(music, volume)
	rl.PlayMusicStream(music)

	s.bgmMusic = music
	s.bgmWavData = wavData // keep alive for streaming
	s.bgmActive = true
	s.bgmPath = key
	s.bgmVol = volume

	log.Printf("Audio: BGM %s playing (%d frames, %d samples, max=%d min=%d, WAV=%d bytes)",
		filepath.Base(key), len(pcm)/2, len(pcm), maxSample, minSample, len(wavData))
}

// StopBGM stops the currently playing background music.
func (s *System) StopBGM() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopBGMLocked()
}

// PauseBGM temporarily pauses BGM playback (e.g. during window mode switch).
func (s *System) PauseBGM() {
	if s == nil || !s.bgmActive {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rl.PauseMusicStream(s.bgmMusic)
}

// ResumeBGM resumes paused BGM playback.
func (s *System) ResumeBGM() {
	if s == nil || !s.bgmActive {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rl.ResumeMusicStream(s.bgmMusic)
}

// UpdateBGM must be called each frame to feed the Music stream.
func (s *System) UpdateBGM() {
	if s == nil || !s.bgmActive {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rl.UpdateMusicStream(s.bgmMusic)
}

// PlayAmbientPCM plays pre-rendered stereo PCM as a looping ambient track.
// Plays concurrently with BGM. key is used for dedup.
func (s *System) PlayAmbientPCM(key string, pcm []int16, sampleRate int, volume float32) {
	if s == nil || len(pcm) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ambActive && s.ambKey == key {
		return
	}
	s.stopAmbientLocked()

	wavData := pcmToWAV(pcm, sampleRate, 2)
	music := rl.LoadMusicStreamFromMemory(".wav", wavData, int32(len(wavData)))
	if !rl.IsMusicValid(music) {
		return
	}
	music.Looping = true
	rl.SetMusicVolume(music, volume)
	rl.PlayMusicStream(music)

	s.ambMusic = music
	s.ambWavData = wavData
	s.ambActive = true
	s.ambKey = key
	s.ambVol = volume
	log.Printf("Audio: ambient %s playing (%d samples, %.1fs)", key, len(pcm)/2, float64(len(pcm)/2)/float64(sampleRate))
}

// StopAmbient stops the ambient track.
func (s *System) StopAmbient() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopAmbientLocked()
}

func (s *System) stopAmbientLocked() {
	if s.ambActive {
		rl.StopMusicStream(s.ambMusic)
		rl.UnloadMusicStream(s.ambMusic)
		s.ambActive = false
		s.ambKey = ""
		s.ambWavData = nil
	}
}

// UpdateAmbient must be called each frame to feed the ambient Music stream.
func (s *System) UpdateAmbient() {
	if s == nil || !s.ambActive {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rl.UpdateMusicStream(s.ambMusic)
}

// pcmToWAV wraps raw int16 PCM samples in a WAV header.
func pcmToWAV(pcm []int16, sampleRate, channels int) []byte {
	dataSize := len(pcm) * 2 // 2 bytes per int16
	fileSize := 44 + dataSize
	buf := make([]byte, fileSize)

	// RIFF header
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], uint32(fileSize-8))
	copy(buf[8:12], "WAVE")

	// fmt chunk
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 16)                                      // chunk size
	binary.LittleEndian.PutUint16(buf[20:22], 1)                                        // PCM format
	binary.LittleEndian.PutUint16(buf[22:24], uint16(channels))                         // channels
	binary.LittleEndian.PutUint32(buf[24:28], uint32(sampleRate))                       // sample rate
	binary.LittleEndian.PutUint32(buf[28:32], uint32(sampleRate*channels*2))            // byte rate
	binary.LittleEndian.PutUint16(buf[32:34], uint16(channels*2))                       // block align
	binary.LittleEndian.PutUint16(buf[34:36], 16)                                       // bits per sample

	// data chunk
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:44], uint32(dataSize))

	// PCM data
	for i, s := range pcm {
		binary.LittleEndian.PutUint16(buf[44+i*2:], uint16(s))
	}
	return buf
}

func (s *System) stopBGMLocked() {
	if s.bgmActive {
		log.Printf("Audio: stopping BGM %s", filepath.Base(s.bgmPath))
		rl.StopMusicStream(s.bgmMusic)
		rl.UnloadMusicStream(s.bgmMusic)
		s.bgmWavData = nil
	}
	s.bgmActive = false
	s.bgmPath = ""
}


// SetSFXMuted toggles muting of spell/SFX sounds.
func (s *System) SetSFXMuted(muted bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sfxMuted = muted
	if muted {
		// Stop all active SFX
		for _, a := range s.active {
			rl.StopSound(a.Sound)
			rl.UnloadSound(a.Sound)
		}
		s.active = nil
	}
}

// SFXMuted returns true if SFX sounds are muted.
func (s *System) SFXMuted() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sfxMuted
}

// SetSFXVolume adjusts the SFX volume multiplier (0-1).
// Updates all currently playing sounds immediately.
func (s *System) SetSFXVolume(vol float32) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sfxVolume = vol
	// Update all active sounds with new volume
	for _, a := range s.active {
		v := s.calcVolume(a.Position, a.Volume, a.MaxDist) * vol
		rl.SetSoundVolume(a.Sound, v)
	}
}

// BGMActive returns true if BGM is currently playing.
func (s *System) BGMActive() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bgmActive
}

// BGMPath returns the key of the currently playing BGM track.
func (s *System) BGMPath() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bgmPath
}

// SetBGMVolume adjusts the BGM volume.
func (s *System) SetBGMVolume(vol float32) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bgmVol = vol
	if s.bgmActive {
		rl.SetMusicVolume(s.bgmMusic, vol)
	}
}
