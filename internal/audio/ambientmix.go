package audio

import (
	"log"
	"time"

	rl "github.com/gen2brain/raylib-go/raylib"
)

// ambientFadeMs is VISceneSoundTrack::m_p10: a track change waits this long
// after the last one, and a track fades in or out over the same 15,000 ticks.
const ambientFadeMs = 15000

// ps2TicksPerSecond is VITimer::GetTime's rate (SUPPORT vitimer.cpp: 2 a
// compare interrupt at 9.216 MHz / 18,750), which VIScene::GetTime runs at.
const ps2TicksPerSecond = 983.04

// ambientGain is this client's mix level for the ambient XMs, under the
// level SetSoundTrack computes (which peaks at 1).
const ambientGain = 0.15

// ambSlot is a VISceneSoundTrackState.
type ambSlot struct {
	state   int     // m_p00: 0 steady, 1 fading out, 2 fading in, 3 faded out
	start   int64   // m_p08: when the fade began, ms
	from    float32 // m_p10: the volume it began at
	vol     float32 // m_p14
	track   int32   // m_p20: the XM's DictID, 0 = silence
	ready   bool    // m_p24 != -1: the track's data is in hand
	playing bool    // m_p1C != -1
}

func (a *ambSlot) clear() { *a = ambSlot{track: -1} }

// ambientMix is VIScene::SetSoundTrack's state (VISceneSoundTrack), with the
// playing itself left to the caller: Step says which slots start and stop and
// at what volume.
type ambientMix struct {
	inited     bool
	first      bool  // m_p00: no track has played yet
	lastChange int64 // m_p18
	slot       [2]ambSlot
}

// ambAction is what Step asks of the voices for one slot.
type ambAction struct {
	start, stop bool
	track       int32
	vol         float32 // the volume to set, every frame the slot plays
}

// Step is one call of SetSoundTrack (viscene.cpp:3472) at now (ms):
//   - a change of track is taken at most once per 15 s (or at once before
//     any track has played): the playing slot moves to slot 1 and fades out
//     from where it is, and slot 0 takes the new track;
//   - a slot fades to 0 (out) or 1 (in) over 15 s; one faded out is retired;
//   - a ready slot that is not playing starts — at full volume if it is the
//     first track ever, otherwise from 0, fading in;
//   - what is set is the slot's volume × (1 − rain)³ × outdoors, or 0 with the
//     camera in any liquid (env & 0xF).
//
// ready reports whether a track's data exists; track 0 never does, so it is
// silence. A slot's actions come back in its own index; moved means slot 0
// became slot 1 first, so the caller stops slot 1's voice and swaps the two
// before acting.
func (m *ambientMix) Step(track int32, ready func(int32) bool, now int64, rain, outdoors float32, inLiquid bool) (act [2]ambAction, moved bool) {
	if !m.inited {
		m.inited, m.first = true, true
		m.slot[0].clear()
		m.slot[1].clear()
	}
	level := func(v float32) float32 {
		q := 1 - rain
		v = v * q * q * q * outdoors
		if inLiquid {
			v = 0
		}
		return v
	}

	if now-m.lastChange > ambientFadeMs || m.first {
		if track != m.slot[0].track {
			moved = true
			if m.slot[0].ready {
				m.slot[0].state = 1
				m.slot[0].start = now
				m.slot[0].from = m.slot[0].vol
			}
			m.slot[1] = m.slot[0]
			m.slot[0].clear()
			m.slot[0].track = track
			m.lastChange = now
		}
	}
	if !m.slot[0].ready && ready(m.slot[0].track) {
		m.slot[0].ready = true
	}

	for j := range m.slot {
		sl := &m.slot[j]
		if !sl.playing {
			continue
		}
		if sl.state == 1 || sl.state == 2 {
			t := float32(now-sl.start) / ambientFadeMs
			t = min(1, max(0, t))
			var to float32
			if sl.state != 1 {
				to = 1
			}
			sl.vol = (to-sl.from)*t + sl.from
			if t == 1 {
				if sl.state == 1 {
					sl.state = 3
				} else {
					sl.state = 0
				}
			}
		}
		act[j].vol = level(sl.vol)
	}

	for j := range m.slot {
		if m.slot[j].state == 3 {
			if m.slot[j].playing {
				act[j].stop = true
			}
			m.slot[j].clear()
		}
	}

	for j := range m.slot {
		sl := &m.slot[j]
		if sl.playing || !sl.ready {
			continue
		}
		sl.playing = true
		if m.first {
			sl.vol = 1
			m.first = false
		} else {
			sl.vol = 0
		}
		sl.state = 2
		sl.start = now
		sl.from = sl.vol
		act[j].start = true
		act[j].track = sl.track
		act[j].vol = level(sl.vol)
	}
	return act, moved
}

// ambVoice is one slot's XM loop, pre-rendered and streamed.
type ambVoice struct {
	music  rl.Music
	wav    []byte
	active bool
	track  int32
}

func (v *ambVoice) stop() {
	if v.active {
		rl.StopMusicStream(v.music)
		rl.UnloadMusicStream(v.music)
		v.active = false
		v.wav = nil
	}
}

func (v *ambVoice) start(track int32, t *AmbientTrack) {
	v.stop()
	if t == nil || len(t.PCM) == 0 {
		return
	}
	wav := pcmToWAV(t.PCM, t.SampleRate, 2)
	music := rl.LoadMusicStreamFromMemory(".wav", wav, int32(len(wav)))
	if !rl.IsMusicValid(music) {
		return
	}
	music.Looping = true
	rl.SetMusicVolume(music, 0)
	rl.PlayMusicStream(music)
	v.music, v.wav, v.active, v.track = music, wav, true, track
	log.Printf("Audio: ambient %s started", AmbientKey(track))
}

// SetAmbient is VIScene::SetSoundTrack, once a frame: track is the camera's
// (day, night) pick (AmbientTrackFor), tracks the rendered AMBTRACK XMs, rain
// the weather intensity, outdoors the baked-light outdoors factor, and
// inLiquid whether the CAMERA is in water, lava, slime or mud. Tracks cross-
// fade over 15 s and change at most once per 15 s; see ambientMix.Step.
//
// The PS2 plays "amb_underwater" in a liquid, but no file on the disc holds
// that resource, so its Find fails and the music is only muted.
func (s *System) SetAmbient(track int32, tracks map[int32]*AmbientTrack, rain, outdoors float32, inLiquid bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ambStart.IsZero() {
		s.ambStart = time.Now()
	}
	// The scene's clock is VITimer's: 983.04 ticks a real second, so the
	// 15,000 of a fade or a hold-off are 15.26 s.
	now := int64(time.Since(s.ambStart).Seconds() * ps2TicksPerSecond)
	ready := func(id int32) bool { t, ok := tracks[id]; return ok && t != nil && len(t.PCM) > 0 }
	act, moved := s.ambMix.Step(track, ready, now, rain, outdoors, inLiquid)
	if moved {
		s.ambVoices[1].stop()
		s.ambVoices[0], s.ambVoices[1] = s.ambVoices[1], s.ambVoices[0]
	}
	for j := range act {
		v := &s.ambVoices[j]
		if act[j].stop {
			v.stop()
		}
		if act[j].start {
			v.start(act[j].track, tracks[act[j].track])
		}
		if v.active {
			rl.SetMusicVolume(v.music, act[j].vol*ambientGain)
		}
	}
}

// StopSoundTrack is VIScene::StopSoundTrack (viscene.cpp:718), which CLIENT
// calls as the player arrives anywhere (Msg000D, Respawn, MoveTo): both
// slots stop and are cleared, and the next SetAmbient takes its track at
// once, as before the first one ever (m_p00 = 1), at full volume.
func (s *System) StopSoundTrack() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ambVoices[0].stop()
	s.ambVoices[1].stop()
	if s.ambMix.inited {
		s.ambMix.slot[0].clear()
		s.ambMix.slot[1].clear()
		s.ambMix.first = true
	}
}
