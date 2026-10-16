package game

// arrival.go — what CLIENT does as the player arrives somewhere, and the
// screen fade.

// Arrival is a placement CLIENT makes once the world is there. Msg000D (the
// MemoryDump, 0x00BD0630) and Respawn (0x00BD5628 [V], 0x00B2's) both end in
// EnterWorld (0x00BD51C0 [V]), which loads the world and the zones around the
// point before it returns; Go streams them in, so the visual client finishes
// the placement (cmd/client/arrival.go) once the zone under the point is
// loaded.
type Arrival struct {
	World   byte
	X, Y, Z float32
	Heading float32
	// Reach is the cast that sets the walker down (terrain.ArrivalFloor): 3
	// for Respawn, 0 for the MemoryDump, which puts it where the dump says.
	Reach float32
	// Respawn also clears VIClient+0x70..+0x78 — the follow (ToggleFollow's
	// +0x78) and its neighbours.
	StopFollow bool
}

// arrive records an arrival. All three of CLIENT's (MoveTo, Respawn and the
// MemoryDump) also stop the battle music at once (VIBattleMusic::StopNow) and
// the zone's sound track (VIScene::StopSoundTrack), and put the camera behind
// the walker (VIWalkCamera::SetLocation + SetOrbitAngleBehind); the visual
// client does those as it takes the arrival.
func (gs *GameState) arrive(a Arrival) {
	gs.PendingArrival = &a
}

// TakeArrival hands the pending arrival to the visual client, once.
func (gs *GameState) TakeArrival() *Arrival {
	a := gs.PendingArrival
	gs.PendingArrival = nil
	return a
}

// ScreenFade is VIClient's screen fade, +0xC168..+0xC184: SetFade aims it and
// DrawFade moves it and draws it over everything, last in the frame.
type ScreenFade struct {
	Cur, To, Rate float32    // m_fadeCur, m_fadeTo, m_fadeRate
	Color         [4]float32 // m_fadeColor: InitBody (0x00BC0098) makes it black, alpha 1
}

// SetFade is VIClient::SetFade (0x00BC7098 [V]): to at once when time is 0,
// otherwise at (to − cur)/time a second.
func (gs *GameState) SetFade(to, time float32) {
	f := &gs.Fade
	if time == 0 {
		f.To = to
		f.Cur = to
		f.Rate = 0
		return
	}
	f.To = to
	f.Rate = (to - f.Cur) / time
}

// AdvanceFade is DrawFade's (0x00BC55D8 [V]) movement: cur toward to at the
// rate, over the particle system's delta time — a frame over a tenth of a
// second counts as a thirtieth. It returns the alpha to draw the fade's
// colour at: the colour's own alpha × cur.
func (gs *GameState) AdvanceFade(dt float32) float32 {
	f := &gs.Fade
	if dt > 0.1 {
		dt = 1.0 / 30.0
	}
	f.Cur += f.Rate * dt
	if f.Rate > 0 {
		f.Cur = min(f.Cur, f.To)
	} else {
		f.Cur = max(f.To, f.Cur)
	}
	return f.Color[3] * f.Cur
}
