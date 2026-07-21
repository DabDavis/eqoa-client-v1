package models

import "time"

// AnimState tracks per-entity animation playback using PS2-accurate frame
// position tracking (VIHSprite::ProcessPlayList port).
//
// PS2 tracks integer frame index + fractional time accumulator. We simplify
// to a single float32 FramePos (fractional frame position) which is equivalent:
//   PS2: currentFrame=3, fracTime=0.4 → our FramePos=3.4
//
// Frame advance: FramePos += dt * FPS * |CPlayListSpeed| * callerSpeed
// This matches PS2 ProcessPlayList exactly.
type AnimState struct {
	UpperIdx int // current upper body animation index (-1 = none)
	LowerIdx int // current lower body animation index (-1 = none)

	// PS2-accurate frame position tracking (ProcessPlayList port)
	FramePos  float32 // current fractional frame position (e.g. 3.4 = frame 3, 40% to frame 4)
	PrevFrame float32 // previous frame position (for keyframe crossing detection)
	NumFrames float32 // total frames in current animation (loop boundary)
	FPS       float32 // current animation FPS (for sound system to read)

	Speed   float32 // caller speed multiplier (PS2: sprite[432], slot[40] in AddPlay)
	CurSlot int     // current CPlayList slot number (for tracking previous during transitions)

	IdleTimer  float32 // seconds spent in idle (pair 0) for periodic special idle
	IdleTarget float32 // random threshold 20-40s (PS2: animTimerA, 20000+rand(20000)ms)

	// Cross-fade blending (PS2: AddInterpolatedPlay, 250ms)
	BlendFactor  float32 // 0→1 over BlendDur seconds; 1.0 = blend complete
	BlendDur     float32 // total blend duration (seconds)
	PrevUpperIdx int     // previous upper body anim index (-1 = none)
	PrevLowerIdx int     // previous lower body anim index (-1 = none)
	PrevFramePos float32 // frame position in previous animation (keeps advancing during blend)
	PrevNumFrames float32 // numFrames for previous animation
	PrevSlot     int     // previous CPlayList slot (for CPlayList Speed during blend)

	// Play state
	IsPlayOnce bool // current animation is play-once (hold last frame)
	IsDeath    bool // current animation is death (hold last frame forever)

	// Dual-pair split: independent lower body frame tracking.
	// When ActionSlot != 0, upper body uses FramePos (action animation)
	// and lower body uses LowerFramePos (locomotion animation).
	ActionSlot    int     // current action CPlayList slot (0 = no split)
	LowerFramePos float32 // lower body frame position (locomotion)
	LowerNumFrames float32 // lower body total frames

	// Controller reference for feedback from applySkeletal (play-once completion,
	// blend state). Set by the animation manager before rendering.
	Controller *SpriteAnimController

	playOnceDone bool // one-shot flag to log play-once completion once
	LoggedWalk   bool // diagnostic: true after first non-idle anim logged

	// PS2 HSprite trigger system: "Spell FX 1-4" keyframe triggers.
	// Indexed by trigger slot (0-3). Tripped when animation frame crosses
	// the keyframe Time for that trigger's RefID.
	SpellFXTripped [4]bool

	// Sound trigger system: "Primary Sound"/"Secondary Sound" keyframe triggers.
	// PS2 resets these on animation change (SetAnimation success path).
	// Tripped when frame crosses the keyframe, consumed by entity sound system.
	SoundTripped [2]bool // [0]=primary, [1]=secondary

	// Weapon trail state (PS2 VICSprite+0x11C0..+0x11D3).
	// Set by SetWeaponTrail during PlayAttack, rendered by ProcessWeaponTrails.
	TrailActive bool
	TrailID     int32
	TrailColor  [3]float32
	TrailStart  time.Time  // when trail was activated (auto-expire after duration)
	TrailPoints [][3]float32 // ring buffer of weapon tip world positions (last N frames)
}

// animStateMap stores per-entity animation states, keyed by a unique entity identifier.
// Using a map[uint64] to combine modelID+entityID for uniqueness.
type animStateMap map[uint64]*AnimState

// getOrCreate returns the AnimState for the given key, creating one if needed.
func (m animStateMap) getOrCreate(key uint64) *AnimState {
	if s, ok := m[key]; ok {
		return s
	}
	s := &AnimState{
		UpperIdx:     -1,
		LowerIdx:     -1,
		Speed:        1.0,
		BlendFactor:  1.0, // no blend active
		PrevUpperIdx: -1,
		PrevLowerIdx: -1,
	}
	m[key] = s
	return s
}
