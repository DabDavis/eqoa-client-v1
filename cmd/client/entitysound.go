package main

import ()

// entitySoundState tracks per-entity sound playback for keyframe triggers
// and continuous ambient sounds.
// Frame position tracking is now handled by the animation system (AnimState.FramePos)
// — this eliminates duplicate frame tracking between animation and sound.
type entitySoundState struct {
	// Continuous/ambient sound (PS2 0x2940 ContSound)
	contSoundActive bool  // true if continuous sound is playing for this entity
	contSoundID     int32 // DictID of the active continuous sound
}

// entitySoundCleanupCounter tracks frames for periodic cleanup.
var entitySoundCleanupCounter int

// updateEntitySounds processes PS2-accurate animation-triggered sounds.
// PS2: VIHSprite::ProcessPlayTiming fires triggers when animation frame
// crosses keyframe positions; VICSprite::Process matches triggers to sound DictIDs.
// Frame position is read from the animation system's AnimState — no duplicate tracking.
func (c *Client) updateEntitySounds(dt float32) {
	if c.audioSys == nil || c.modelMgr == nil {
		return
	}

	gs := c.engine.State
	p := &gs.Player

	// Player sounds — use cached anim bytes (avoid double-resolution which mutates controller).
	playerModelID := int32(p.ModelID)
	if c.debugModelID != 0 {
		playerModelID = c.debugModelID
	}
	playerLocoAnim, playerActionAnim := c.animMgr.CachedPlayerAnim()
	// Use action anim for sound triggers (attack sounds come from upper body).
	playerSoundAnim := playerLocoAnim
	if playerActionAnim != 0 {
		playerSoundAnim = playerActionAnim
	}
	c.processEntitySound(p.ObjectID, playerModelID, playerSoundAnim,
		p.X, p.Y, p.Z)
	c.processContSound(p.ObjectID, playerModelID, p.X, p.Y, p.Z)

	// Visible entity sounds — use cached anim bytes (avoid double-resolution).
	for _, ent := range c.visibleEnts {
		entLocoAnim, entActionAnim := c.animMgr.CachedEntityAnim(ent.ObjectID)
		entSoundAnim := entLocoAnim
		if entActionAnim != 0 {
			entSoundAnim = entActionAnim
		}
		c.processEntitySound(ent.ObjectID, int32(ent.ModelID), entSoundAnim,
			ent.X, ent.Y, ent.Z)
		c.processContSound(ent.ObjectID, int32(ent.ModelID), ent.X, ent.Y, ent.Z)
	}

	// Periodic cleanup of stale entity sound state (~every 5s)
	entitySoundCleanupCounter++
	if entitySoundCleanupCounter >= 300 {
		entitySoundCleanupCounter = 0
		c.cleanupEntitySounds()
	}
}

// processEntitySound handles PS2-accurate sound triggering for a single entity.
// PS2 ProcessPlayTiming (0x00449850) detects keyframe crossings in applySkeletal
// and sets SoundTripped[ch] flags. We consume those flags here to play sounds.
// PS2 CSprite::Process (0x00423ED8) matches trigger RefID → sound DictID per slot.
func (c *Client) processEntitySound(objectID uint32, modelID int32, animByte byte,
	x, y, z float32) {

	model, ok := c.modelMgr.GetCachedModel(modelID)
	if !ok || model == nil || model.PLSlotSound == nil {
		return
	}

	// Get sound config for current CPlayList slot
	slot := int(animByte)
	slotSound, hasSound := model.PLSlotSound[slot]
	if !hasSound {
		return
	}

	// Consume SoundTripped flags set by applySkeletal's keyframe crossing detection.
	// PS2: ProcessPlayTiming fires triggers → CSprite::Process plays sounds.
	as := c.modelMgr.GetAnimState(objectID)
	if as == nil {
		return
	}

	pos := [3]float32{x, y, z}
	for ch := 0; ch < 2; ch++ {
		if !as.SoundTripped[ch] {
			continue
		}
		as.SoundTripped[ch] = false // consume

		dictID := slotSound.SoundDictID[ch]
		if dictID == 0 {
			continue
		}

		c.audioSys.Play(dictID, 0, pos, slotSound.Volume[ch], 80)
	}
}

// processContSound starts/updates continuous ambient sounds (PS2 0x2940 ContSound).
// These loop indefinitely while the entity is visible (campfire crackle, growl, etc).
func (c *Client) processContSound(objectID uint32, modelID int32, x, y, z float32) {
	model, ok := c.modelMgr.GetCachedModel(modelID)
	if !ok || model == nil || model.ContSoundID == 0 {
		return
	}

	state := c.getEntitySoundState(objectID)
	pos := [3]float32{x, y, z}

	if !state.contSoundActive {
		// Use negative objectID as loop ID to avoid collision with spell event IDs
		loopID := -int32(objectID)
		if c.audioSys.PlayLoop(model.ContSoundID, loopID, pos, model.ContSoundVol, 80) {
			state.contSoundActive = true
			state.contSoundID = model.ContSoundID
		}
	} else {
		// Update position for distance attenuation
		loopID := -int32(objectID)
		c.audioSys.UpdateLoopPosition(loopID, pos)
	}
}

// getEntitySoundState returns or creates the sound state for an entity.
func (c *Client) getEntitySoundState(objectID uint32) *entitySoundState {
	if c.entitySounds == nil {
		c.entitySounds = make(map[uint32]*entitySoundState)
	}
	s, ok := c.entitySounds[objectID]
	if !ok {
		s = &entitySoundState{}
		c.entitySounds[objectID] = s
	}
	return s
}

// cleanupEntitySounds removes sound state for entities that are no longer visible.
func (c *Client) cleanupEntitySounds() {
	if c.entitySounds == nil {
		return
	}
	gs := c.engine.State
	active := make(map[uint32]bool, len(gs.Entities)+1)
	active[gs.Player.ObjectID] = true
	for id := range gs.Entities {
		active[id] = true
	}
	for id, state := range c.entitySounds {
		if !active[id] {
			// Stop continuous sound if playing
			if state.contSoundActive {
				loopID := -int32(id)
				c.audioSys.StopLoop(loopID)
			}
			delete(c.entitySounds, id)
		}
	}
}
