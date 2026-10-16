package game

import (
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"runtime/debug"
	"time"

	"github.com/eqoa/client/internal/network"
	"github.com/eqoa/client/internal/protocol"
	"github.com/DabDavis/eqoa-esf-tools/pkg/buffer"
	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

// Engine is the core game loop, independent of any UI framework.
type Engine struct {
	State       *GameState
	AutoConnect bool // Auto-connect to first game server after receiving server list
	lastFlush   time.Time
	started     bool
	password     string
	sessionToken []byte // 32-byte token from TCP auth
	startupCh    chan startupResult // off-thread startup I/O → main goroutine (#4)
	tickCount    int
	whoToken       int32  // VIClientPart38334+0x04: one more per who request (Close)
	TargetObjectID uint32 // Current target ObjectID (sent in 0x40 position updates)

	// Movement target (goto command)
	moveTargetActive bool
	moveTargetX      float32
	moveTargetY      float32
	moveTargetZ      float32
	moveSpeed        float32 // units per tick (default 5.0 = 50 units/sec)
	moveArrivalDist  float32 // stop distance (default 3.0)

	// Follow mode
	followTarget   uint32 // ObjectID to follow (0 = not following)
	followDist     float32

	// Assets directory for loading SPELLFX, etc.
	AssetsDir string
	// SaveDir is this server's save folder, the stand-in for the memory
	// card's game folder that the patcher brings in line with the server's
	// manifest (network.RunPatcher). Empty runs only the old manifest check.
	SaveDir string
	ISOPath   string // path to EQOA ISO (optional, for reading assets directly)

	// Spell effect runtime system
	SpellFX     *SpellEffectSystem
	Audio       AudioPlayer        // set by visual client for sound playback
	posResolver    PosResolver    // set by visual client for entity position lookups
	boneResolver   BoneResolver   // set by visual client for bone position lookups
	facingResolver FacingResolver // set by visual client for entity facing angle

	// PlayAttack callback (set by visual client for spell effect animation triggers)
	playAttackCB func(actorObjID uint32, animPair int32, trailColor [3]float32, trailID int32) int

	// SetAction callback (set by visual client for cast/action animation triggers)
	setActionCB func(actorObjID uint32, actionType int32) int

	// IsActionComplete callback (set by visual client for Wait sub-type 0)
	isActionCompleteCB func(actorObjID uint32, handle int) bool

	// Trigger callbacks for spell FX HasTripped (Wait sub-type 3)
	findTriggerCB  func(actorObjID uint32, triggerName string) (int, bool)
	resetTriggerCB func(actorObjID uint32, triggerIdx int)
	hasTrippedCB   func(actorObjID uint32, triggerIdx int) bool
	isCSpriteCB    func(actorObjID uint32) bool
	weaponTrailCB  func(actorObjID uint32, trailID int32, color [3]float32)

	// 0x40 position send throttle — PS2 runs at 30fps and sends 0x40 at ~6/s
	// (~every 5 frames). Visual client at 60fps must NOT queue per frame.
	lastPositionSend time.Time

	// Arrow-key walking (set by UI layer each frame)
	Walking       bool
	AnimByte      byte // CPlayList slot: 0=idle, 1=walk, 3=run, 4=swim (matches PS2 wire format)
	EmoteAnimByte byte // non-zero while an emote is playing (overrides AnimByte in 0x40)
	InCombat      bool // true when server confirms auto-attack (sets animation byte to 0x09)
	CombatEngaged bool // true when player pressed spacebar to initiate auto-attack
	AttackSent    bool // true once the Attack opcode (0x000F) has been sent for current engage

	// PS2 0x40 velocity encoding — world-space velocity set by movement handler each frame.
	// Encoded as uint16 big-endian in 0x40 bytes 10-15, 0x8000 = center (zero velocity).
	VelX, VelY, VelZ float32

	// PS2 0x40 auxiliary fields (set by visual client each frame)
	StickMagnitude byte    // byte 24: analog stick deflection (128=center, 254=max). Keyboard: 254 when moving, 128 idle.
	AccelState     byte    // byte 26: 0x80=steady/idle, 0xFF=accelerating, 0x00=decelerating
	SmoothedFacing float32 // byte 28: exponentially smoothed facing (lags during turns)
	TurnRate       float32 // byte 23: heading change rate in rad/s (0=no turn, positive=right, negative=left)
	PrevFacing     float32 // previous frame facing for turn rate computation

	// Deferred game server switch — avoids goroutine deadlock with Flush mutex.
	// After server list arrives, we wait a few ticks for the ACK to go out,
	// then switch synchronously from Tick.
	deferredGameServerIdx  int
	deferredGameServerTick int // countdown ticks before switching (-1 = no pending switch)

	// GM teleport position override — applied AFTER processIncoming (which
	// overwrites Player pos from state channel 0x00) but BEFORE sendWorldEntryTrigger
	// builds the 0x40 packet. Set by visual client for GM teleport reconnect.
	PosOverride      bool
	PosOverrideX     float32
	PosOverrideY     float32
	PosOverrideZ     float32

	// Offline puts the engine straight into PhaseInWorld on its first Tick,
	// without auth, patch or a connection (the visual client's --offline mode).
	// Everything that sends is already skipped when Conn is nil.
	Offline bool
}

// NewEngine creates a new Engine with the given connection parameters.
func NewEngine(host string, gamePort, authPort, patchPort int, username, password string) *Engine {
	return &Engine{
		State:                  NewGameState(host, gamePort, authPort, patchPort, username),
		AutoConnect:            true,
		lastFlush:              time.Now(),
		password:               password,
		deferredGameServerTick: -1,
	}
}

// NewEngineWithAuth creates an Engine with pre-completed TCP auth credentials.
// Used when the login screen performs auth before engine creation.
func NewEngineWithAuth(host string, gamePort, authPort, patchPort int,
	username, password string, accountID uint32, sessionToken []byte) *Engine {
	e := &Engine{
		State:                  NewGameState(host, gamePort, authPort, patchPort, username),
		AutoConnect:            true,
		lastFlush:              time.Now(),
		password:               password,
		sessionToken:           sessionToken,
		deferredGameServerTick: -1,
	}
	e.State.AccountID = accountID
	return e
}

// SetPosResolver sets the position resolver for spell effect particle emitters.
// Should be called by the visual client after engine creation.
func (e *Engine) SetPosResolver(resolve PosResolver) {
	e.posResolver = resolve
}

// SetBoneResolver sets the bone position resolver for spell effect bone attachment.
func (e *Engine) SetBoneResolver(resolve BoneResolver) {
	e.boneResolver = resolve
}

// SetFacingResolver sets the facing angle resolver for spell effect emitter direction.
// PS2 ProcessCreateEmitter applies actor facing to emitter direction matrix.
func (e *Engine) SetFacingResolver(resolve FacingResolver) {
	e.facingResolver = resolve
}

// SetPlayAttackCallback sets a callback for PlayAttack spell events (type 15).
// Called with (actorObjID, animPairIndex) when a spell effect triggers an attack
// animation; it returns the action handle (SetAttackAction's), or -1.
func (e *Engine) SetPlayAttackCallback(cb func(actorObjID uint32, animPair int32, trailColor [3]float32, trailID int32) int) {
	e.playAttackCB = cb
}

// SetSetActionCallback sets a callback for SetAction spell events (type 2).
// Called with (actorObjID, actionType) when a spell effect triggers a
// cast/action animation; it returns the action handle (SetAction's), or -1.
func (e *Engine) SetSetActionCallback(cb func(actorObjID uint32, actionType int32) int) {
	e.setActionCB = cb
}

// SetIsActionCompleteCallback sets a callback for Wait sub-type 0:
// VICSprite::IsActionComplete(handle) on the actor.
func (e *Engine) SetIsActionCompleteCallback(cb func(actorObjID uint32, handle int) bool) {
	e.isActionCompleteCB = cb
}

// SetTriggerCallbacks sets the FindTrigger/ResetTrigger/HasTripped/IsCSprite/WeaponTrail
// callbacks for the PS2 spell FX trigger and weapon trail systems.
func (e *Engine) SetTriggerCallbacks(
	find func(actorObjID uint32, triggerName string) (int, bool),
	reset func(actorObjID uint32, triggerIdx int),
	hasTripped func(actorObjID uint32, triggerIdx int) bool,
	isCSprite func(actorObjID uint32) bool,
	weaponTrail func(actorObjID uint32, trailID int32, color [3]float32),
) {
	e.findTriggerCB = find
	e.resetTriggerCB = reset
	e.hasTrippedCB = hasTripped
	e.isCSpriteCB = isCSprite
	e.weaponTrailCB = weaponTrail
}

// loadSpellFXAuto tries ISO first, then assetsDir for SPELLFX loading.
func (e *Engine) loadSpellFXAuto() (*SpellFXDB, error) {
	if e.ISOPath != "" {
		file, err := esf.OpenISOFile(e.ISOPath, "SPELLFX.ESF")
		if err == nil {
			log.Printf("SpellFX: loaded from ISO")
			return LoadSpellFXFromFile(file)
		}
		log.Printf("SpellFX: ISO read failed: %v (trying files)", err)
	}
	if e.AssetsDir != "" {
		return LoadSpellFX(e.AssetsDir)
	}
	return nil, fmt.Errorf("no ISO or assets dir configured")
}

// Tick runs one iteration of the game loop: drain packets + position update + flush.
func (e *Engine) Tick() {
	if !e.started {
		e.started = true
		// Load SpellFX database (non-fatal). Try ISO first, then assets dir.
		if e.State.SpellFX == nil {
			sfx, err := e.loadSpellFXAuto()
			if err != nil {
				log.Printf("SpellFX: %v (spell effect logging disabled)", err)
			} else {
				effects, particles := sfx.Stats()
				log.Printf("Loaded %d spell effects, %d particle definitions", effects, particles)
				e.State.SpellFX = sfx
				e.SpellFX = NewSpellEffectSystem(sfx)
				e.SpellFX.ResolvePos = e.posResolver
				e.SpellFX.ResolveBone = e.boneResolver
				e.SpellFX.ResolveFacing = e.facingResolver
				e.SpellFX.OnPlayAttack = e.playAttackCB
				e.SpellFX.OnSetAction = e.setActionCB
				e.SpellFX.IsActionComplete = e.isActionCompleteCB
				e.SpellFX.FindTrigger = e.findTriggerCB
				e.SpellFX.ResetTrigger = e.resetTriggerCB
				e.SpellFX.HasTripped = e.hasTrippedCB
				e.SpellFX.IsCSprite = e.isCSpriteCB
				e.SpellFX.SetWeaponTrail = e.weaponTrailCB
				// Camera shake is the local player's alone.
				e.SpellFX.IsPlayer = func(id uint32) bool { return id == e.State.Player.ObjectID }
				e.SpellFX.Audio = e.Audio
				// Load spell sounds from SPELLFX file if audio available
				if e.Audio != nil && sfx.File() != nil {
					e.Audio.LoadSpellSounds(sfx.File())
				}
				e.State.SpellFXSys = e.SpellFX
			}
		}
		if e.Offline {
			e.State.Phase = PhaseInWorld
			e.State.StatusMsg = "Offline"
			return
		}
		// Kick off startup I/O off-thread. The blocking auth/patch/connect runs
		// in a goroutine, but its results are applied to GameState on the main
		// goroutine (applyStartupResult) so Phase/Conn/StatusMsg/AccountID are
		// never written concurrently with the game loop. (#4)
		gs := e.State
		gs.Phase = PhaseConnecting
		gs.StatusMsg = "Connecting..."
		e.startupCh = make(chan startupResult, 1)
		ch := e.startupCh
		serverIP, authPort, gamePort, patchPort := gs.ServerIP, gs.AuthPort, gs.GamePort, gs.PatchPort
		username, password := gs.Username, e.password
		accountID, token := gs.AccountID, e.sessionToken
		saveDir := e.SaveDir
		go func() {
			ch <- runStartupIO(serverIP, authPort, gamePort, patchPort, username, password, accountID, token, saveDir)
		}()
		return
	}

	// Apply the startup result once it arrives — on the main goroutine.
	if e.startupCh != nil {
		select {
		case res := <-e.startupCh:
			e.startupCh = nil
			e.applyStartupResult(res)
		default:
		}
	}

	if e.State.Conn != nil {
		e.DrainIncoming()
	}

	// Handle deferred game server switch (avoids goroutine deadlock with Flush mutex).
	// We count down ticks to let the server list ACK flush out first.
	if e.deferredGameServerTick > 0 {
		e.deferredGameServerTick--
	} else if e.deferredGameServerTick == 0 {
		e.deferredGameServerTick = -1 // Consume the trigger
		e.ConnectToGameServer(e.deferredGameServerIdx)
	}

	// Apply GM teleport position override after processIncoming (which may
	// overwrite Player pos from state channel 0x00) but before we build
	// the world entry 0x40 packet.
	if e.PosOverride && e.State.Phase == PhaseInWorld {
		e.State.Player.X = e.PosOverrideX
		e.State.Player.Y = e.PosOverrideY
		e.State.Player.Z = e.PosOverrideZ
	}

	// Send position updates when in world.
	if e.State.Phase == PhaseInWorld && e.State.Conn != nil {
		e.tickCount++
		e.updateFollow()
		e.updateMovement()

		// Send 0x40 position updates.
		// The PS2 client sends 0x40 every frame even when idle. Sandstorm
		// uses continuous 0x40 receipt to initialize EntityInRange (visibility).
		// Without periodic updates, entity state channels never arrive.
		if !e.State.WorldEntryAcked {
			e.sendWorldEntryTrigger()
		} else if e.State.TeleportPending {
			// After teleport, PS2 sends EnableChannel (0x0049) + fresh 0x40.
			// Sandstorm waits for this before processing position at new location.
			e.sendTeleportArrival()
		} else {
			// Face target continuously while server-confirmed auto-attack is active.
			// CombatEngaged = spacebar was pressed; InCombat = server confirmed via CastingCombat.
			// Casting spells and being attacked by NPCs do NOT cause auto-facing.
			if e.InCombat && e.CombatEngaged && e.TargetObjectID != 0 {
				e.faceTarget(e.TargetObjectID)
			}

			// Throttle 0x40 to ~6/s (every ~166ms), matching PS2's observed
			// rate of 5.87/s. PS2 runs at 30fps and sends 0x40 every ~5 frames.
			// At 60fps visual client, this prevents queuing 6 XOR-chained
			// messages per 100ms flush cycle.
			if time.Since(e.lastPositionSend) >= 150*time.Millisecond {
				e.sendPositionUpdate()
				e.lastPositionSend = time.Now()
			}
		}

		// Update spell effect system (event timelines, wait timers)
		e.SpellFX.Process()

		// Connection health: warn if server stops sending packets
		if e.tickCount%100 == 0 { // every 10 seconds
			lastRecv := e.State.Conn.LastRecvTime()
			if !lastRecv.IsZero() {
				sinceRecv := time.Since(lastRecv)
				if sinceRecv > 10*time.Second {
					log.Printf("WARNING: no server packets for %.1fs — connection may be dead", sinceRecv.Seconds())
				} else {
					log.Printf("HEALTH: lastRecv=%.1fs ago entities=%d",
						sinceRecv.Seconds(), len(e.State.Entities))
				}
			}
		}
	} else if e.Offline && e.State.Phase == PhaseInWorld && e.SpellFX != nil {
		e.SpellFX.Process()
	}

	if e.State.Conn != nil && time.Since(e.lastFlush) > 100*time.Millisecond {
		e.State.Conn.Flush()
		e.lastFlush = time.Now()
	}
}

// startupResult is the outcome of the off-thread startup I/O (auth → patch →
// UDP connect). It is applied to GameState on the main goroutine by
// applyStartupResult, so those fields are never written concurrently with the
// game loop. (#4)
type startupResult struct {
	conn         *network.Connection
	accountID    uint32
	sessionToken []byte
	statusMsg    string // terminal status text (error on failure)
	ok           bool
}

// runStartupIO runs the blocking auth → patch → UDP connection flow off the
// main goroutine. It only touches its arguments and locals — never GameState —
// and returns the result for the main goroutine to apply. Must run in a
// goroutine (network.Login/CheckPatch block).
func runStartupIO(serverIP string, authPort, gamePort, patchPort int, username, password string, accountID uint32, sessionToken []byte, saveDir string) startupResult {
	// Step 1: TCP Auth (skip if pre-authenticated via login screen).
	if accountID != 0 && sessionToken != nil {
		log.Printf("Skipping TCP auth (pre-authenticated, accountID=%d)", accountID)
	} else {
		log.Printf("=== Step 1: TCP Auth ===")
		aid, tok, err := network.Login(serverIP, authPort, username, password)
		if err != nil {
			log.Printf("Auth failed: %v", err)
			return startupResult{statusMsg: fmt.Sprintf("Auth failed: %v", err)}
		}
		accountID, sessionToken = aid, tok
		log.Printf("Auth success: accountID=%d, token=%d bytes", accountID, len(sessionToken))
	}

	// Step 2: the patcher, as SETUP runs it before login: the save folder is
	// brought in line with the server's manifest. Non-fatal, as before — a
	// server with no patch port still plays.
	log.Printf("=== Step 2: Patch ===")
	if saveDir != "" {
		if _, err := network.RunPatcher(serverIP, patchPort, saveDir); err != nil {
			log.Printf("Patcher failed (non-fatal): %v", err)
		}
	} else if _, err := network.CheckPatch(serverIP, patchPort); err != nil {
		log.Printf("Patch check failed (non-fatal): %v", err)
	}
	log.Printf("Patch step complete, server IP: %s", serverIP)

	// Step 3: UDP connection to login endpoint (0xFFFE).
	log.Printf("=== Step 3: UDP Login ===")
	conn, err := network.NewConnection(serverIP, gamePort, 0xFFFE)
	if err != nil {
		log.Printf("UDP connect failed: %v", err)
		return startupResult{statusMsg: fmt.Sprintf("UDP connect failed: %v", err)}
	}

	// Start receive loop and send initial login messages (DiscVersion +
	// Authenticate). These touch only the local conn, not GameState.
	go conn.RecvLoop()
	conn.SendInitialPacket(protocol.GameVersionEQOAFrontiers, username, sessionToken)
	log.Printf("Sent initial login packet to 0xFFFE")

	return startupResult{
		conn:         conn,
		accountID:    accountID,
		sessionToken: sessionToken,
		statusMsg:    "Waiting for server list...",
		ok:           true,
	}
}

// applyStartupResult applies the off-thread startup outcome to GameState. Runs
// on the main goroutine (called from Tick), so Conn/Phase/StatusMsg/AccountID
// are written without racing the game loop or its readers. (#4)
func (e *Engine) applyStartupResult(res startupResult) {
	gs := e.State
	if !res.ok {
		gs.StatusMsg = res.statusMsg
		return
	}
	gs.AccountID = res.accountID
	e.sessionToken = res.sessionToken
	gs.Conn = res.conn
	gs.Phase = PhaseAuth
	gs.StatusMsg = res.statusMsg
}

// DrainIncoming processes all pending incoming packets (non-blocking).
// ACKs are included in the regular Flush with the 0x40 state channel message,
// matching PS2 behavior (PS2 never sends empty packets).
func (e *Engine) DrainIncoming() {
	conn := e.State.Conn
	// A connection that has ended says so once, as the DRDP library's
	// terminate notice (0xFD) after everything that arrived before it.
	defer func() {
		if m := conn.TakeTerminate(); m != nil {
			e.State.dispatchIncoming(m)
		}
	}()
	for {
		select {
		case pkt, ok := <-conn.Incoming:
			if !ok {
				return
			}
			e.ProcessPacket(pkt)
		default:
			return
		}
	}
}

// dispatchIncoming routes one received message to its handler, recovering
// from any panic so a single malformed packet cannot crash the client.
// Handlers index into variable-length wire data, so a short packet that
// slips past a length check would otherwise take down the whole client.
func (gs *GameState) dispatchIncoming(msg *protocol.Message) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC recovered in packet handler (type=0x%04X, len=%d): %v\n%s",
				msg.Type, len(msg.Data()), r, debug.Stack())
		}
	}()

	// Disconnect destroys the DRDP object, so the pump's loop condition
	// (m_drdp) ends it: nothing after a disconnect is read.
	if gs.Session.Disconnect.Pending {
		return
	}
	// VIClient::PumpMessages (0x00BD4C10): one switch on the DRDP type.
	switch t := msg.Type; {
	case t == protocol.MessageTypeConnect:
		gs.resetWorld()
	case t == protocol.MessageTypeConnectionless:
		gs.handleConnectionless(msg)
	case t == protocol.MessageTypeDisconnect:
		gs.handleDRDPDisconnect(msg)
	case t == protocol.MessageTypeUnreliable:
		gs.handleBestEffort(msg)
	case t == protocol.MessageTypeReliable:
		gs.HandleMessage(msg)
	case t == protocol.MessageTypePing, t == 0xFE:
		// 0xF9: ProcessMessageF9 reads its byte (the 0x12 keepalive) and
		// keeps nothing. 0xFE: nothing at all.
	case t.IsStateChannel():
		// Already XOR-decoded by ProcessIncoming.
		gs.HandleStateChannel(byte(t), msg)
	}
}

// ProcessPacket handles a single received packet.
func (e *Engine) ProcessPacket(pkt *network.ReceivedPacket) {
	gs := e.State

	// Detect server reset/terminate packets
	isReset := pkt.Header.Flags&protocol.SegmentFlagResetConnection != 0
	if isReset {
		log.Printf("SERVER RESET: received reset packet (flags=0x%05X) — server terminated connection",
			pkt.Header.Flags)
		return
	}

	// Track session promotion (server sends NewInstance + DidServerInitiate)
	if pkt.Header.Flags&protocol.SegmentFlagDidServerInitiate != 0 && pkt.SessionID != 0 {
		if !gs.MasterSession {
			log.Printf("Master session: playerObjectID=%d", pkt.SessionID)
		}
		gs.PlayerObjectID = pkt.SessionID
		gs.MasterSession = true
	}

	// Process messages
	messages := gs.Conn.ProcessIncoming(pkt)

	for _, msg := range messages {
		gs.dispatchIncoming(msg)
	}

	// Auto-transition: after server list, connect to first game server (once).
	// Set a deferred switch — the actual switch happens in Tick after one flush
	// cycle to ensure the server list ACK goes out first.
	if e.AutoConnect && gs.Phase == PhaseServerList && len(gs.Servers) > 0 {
		gs.Phase = PhaseConnectingGame // Prevent re-trigger
		e.deferredGameServerIdx = 0
		e.deferredGameServerTick = 3 // Wait 3 ticks (~300ms) for ACK flush
	}
}

// ConnectToGameServer switches the UDP connection to the game server at the given index.
func (e *Engine) ConnectToGameServer(index int) {
	gs := e.State
	if index < 0 || index >= len(gs.Servers) {
		return
	}

	server := gs.Servers[index]
	log.Printf("=== Connecting to game server: %s (endpoint=0x%04X, port=%d) ===",
		server.Name, server.EndpointID, server.Port)

	gs.Phase = PhaseConnectingGame
	gs.StatusMsg = fmt.Sprintf("Connecting to %s...", server.Name)

	// Send Reset to close login endpoint connection (PS2 does this before switching).
	// Called synchronously from Tick (deferred by 3 ticks to let ACK flush out first).
	gs.Conn.SendReset()

	// Switch connection to game server endpoint
	gs.Conn.SwitchToGameServer(server.EndpointID)

	// Send game server auth
	gs.Conn.SendGameAuth(protocol.GameVersionEQOAFrontiers, gs.Username, e.sessionToken)
	log.Printf("Sent game auth to endpoint 0x%04X", server.EndpointID)

	gs.StatusMsg = "Waiting for character list..."
}

// SetPosition updates the player's desired position. The engine will
// continuously send this position to the server on each tick.
func (e *Engine) SetPosition(x, y, z, facing float32) {
	e.State.Player.X = x
	e.State.Player.Y = y
	e.State.Player.Z = z
	e.State.Player.Facing = facing
}

// sendWorldEntryTrigger sends the first 0x40 with the player's current position.
// PS2 sends the first 0x40 in the SAME packet as the world entry opcodes
// (CharacterInWorld + ChangeChatMode + FactionMessages + LootMessages).
// The reliable messages were already queued by handleMemoryDump but NOT flushed.
// We queue the 0x40, then flush everything together in one packet.
func (e *Engine) sendWorldEntryTrigger() {
	// Initialize smoothed facing to current facing to prevent initial lag.
	p := &e.State.Player
	e.SmoothedFacing = p.Facing
	e.PrevFacing = p.Facing
	e.AccelState = 0x80
	e.StickMagnitude = 0x80

	e.State.Conn.QueueClientUpdate(e.buildPositionBuffer())
	e.State.Conn.Flush()
	e.State.WorldEntryAcked = true
	log.Printf("sendWorldEntryTrigger: sent world entry batch + initial 0x40 in one packet")
}

// buildPositionBuffer builds a 41-byte 0x40 state buffer matching the PS2 format.
// PS2 sends 41 bytes: position + velocity + facing + turning + flags + animation + target.
func (e *Engine) buildPositionBuffer() []byte {
	p := &e.State.Player

	rawX := uint32(float64(p.X+4000.0) / 36000.0 * float64(0xFFFFFF))
	rawY := uint32(float64(p.Y+1000.0) / 2000.0 * float64(0xFFFFFF))
	rawZ := uint32(float64(p.Z+4000.0) / 36000.0 * float64(0xFFFFFF))

	// Clamp to uint24 range
	if rawX > 0xFFFFFF {
		rawX = 0xFFFFFF
	}
	if rawY > 0xFFFFFF {
		rawY = 0xFFFFFF
	}
	if rawZ > 0xFFFFFF {
		rawZ = 0xFFFFFF
	}

	// PS2 facing byte encoding:
	// facingByte = heading * 128/PI + 128
	// The +128 offset means: heading=0 (north/+Z) → byte 128, heading=PI (south) → byte 0.
	// Java server reads: getByte(22) + 128, which undoes the offset to get unsigned 0-255.
	// C# ConvertFacing: (sbyte)(byte - 128) * PI/128 recovers radians.
	// PS2 pcap verified: facing byte 128 when facing north, 0 when facing south.
	facingByte := byte(p.Facing*128/math.Pi) + 128

	// Build 41-byte state buffer matching PS2 format exactly.
	// The Java Sandstorm server reads the full buffer for enteredGame() / EntityInRange.
	buf := make([]byte, 41)

	// [0] World ID — must match server's world for entity visibility
	buf[0] = p.World

	// [1-9] Position: X, Y, Z as uint24 big-endian
	buf[1] = byte(rawX >> 16)
	buf[2] = byte(rawX >> 8)
	buf[3] = byte(rawX)
	buf[4] = byte(rawY >> 16)
	buf[5] = byte(rawY >> 8)
	buf[6] = byte(rawY)
	buf[7] = byte(rawZ >> 16)
	buf[8] = byte(rawZ >> 8)
	buf[9] = byte(rawZ)

	// [10-15] Velocity: VelX(uint16), VelY(uint16), VelZ(uint16) — big-endian
	// 0x8000 = center (zero velocity). PS2 encodes actual movement velocity.
	// Java Sandstorm decodes: vx = 15.3*2*raw/0xFFFF - 15.3 = 30.6*raw/65535 - 15.3
	// Inverting: raw = (vx + 15.3) / 30.6 * 65535 = vx * 2142.0 + 32768
	// So raw = 0x8000 + vx * 2142.0 (matching uint16 offset-from-center encoding).
	//
	// Sandstorm relays this velocity to PS2 clients via state channel bytes 40-42
	// (scaled by speed_adjust=6.0 → int8). PS2 clients use it for dead-reckoning
	// between state updates. Zero velocity forces PS2 to compute implicit velocity
	// from position deltas, causing overshoot/slingshot on stop.
	const velEncode = 65535.0 / 30.6 // 2142.16 — converts u/s to uint16 offset
	rawVX := int(float64(e.VelX)*velEncode) + 0x8000
	rawVZ := int(float64(e.VelZ)*velEncode) + 0x8000
	if rawVX < 0 {
		rawVX = 0
	} else if rawVX > 0xFFFF {
		rawVX = 0xFFFF
	}
	if rawVZ < 0 {
		rawVZ = 0
	} else if rawVZ > 0xFFFF {
		rawVZ = 0xFFFF
	}
	buf[10] = byte(rawVX >> 8)
	buf[11] = byte(rawVX)
	// PS2 pcap: VelY bytes are always 0xF739 (even when idle). Not actually velocity —
	// the server code comments `// float vy= ???`. Match PS2 exactly.
	buf[12] = 0xF7
	buf[13] = 0x39
	buf[14] = byte(rawVZ >> 8)
	buf[15] = byte(rawVZ)

	// [16-21] Target Velocity — 0x8000 (center) for now.
	// PS2 pcap shows these are input-desired velocity, but populating them
	// may confuse Sandstorm. Keep at center until proven safe.
	buf[16] = 0x80
	buf[17] = 0x00
	buf[18] = 0x80
	buf[19] = 0x00
	buf[20] = 0x80
	buf[21] = 0x00

	// [22] Facing (0-255 → 0-360°)
	buf[22] = facingByte

	// [23] Turning — 0x80 = center (no turn)
	buf[23] = 0x80

	// [24-29] All 0x80 center values.
	// PS2 pcap shows byte 24 varies (stick mag), byte 26 varies (accel state),
	// byte 28 = smoothed facing. But changing these from 0x80 causes overshoot
	// on Sandstorm — byte 24 may act as a speed multiplier server-side.
	buf[24] = 0x80
	buf[25] = 0x80
	buf[26] = 0x80
	buf[27] = 0x80
	buf[28] = facingByte // same as byte 22
	buf[29] = 0x80

	// [30-34] Flags/state
	// PS2 pcap: byte 32 = 0x01 for first packet (world entry), 0x05 for all subsequent.
	// Bit 2 (0x04) signals "fully in world" state.
	buf[30] = 0x00
	buf[31] = 0x01
	if e.State.WorldEntryAcked {
		buf[32] = 0x05 // in world
	} else {
		buf[32] = 0x01 // initial/entering
	}
	buf[33] = 0x00
	buf[34] = 0x00

	// [35] Animation byte → Sandstorm copies to state channel byte 58.
	// PS2 clients use this for remote entity animation (idle/walk/run).
	// Without it, PS2 shows idle while position moves → sliding.
	// Combat anim (0x09) is NOT sent here — combat is driven by Attack
	// opcode (0x000F), not the animation byte.
	if e.EmoteAnimByte != 0 {
		buf[35] = e.EmoteAnimByte
	} else {
		buf[35] = e.AnimByte
	}

	// [36] Auto-attack toggle — set to 1 when player presses spacebar/cross to engage.
	if e.CombatEngaged {
		buf[36] = 0x01
	}

	// [37-40] Target ObjectID (uint32 LITTLE-ENDIAN). 0xFFFFFFFF = no target.
	// PS2 pcap confirms LE format: target 0x013CC7F3 at bytes 37-40 = F3 C7 3C 01.
	targetID := e.TargetObjectID
	if targetID == 0 {
		targetID = 0xFFFFFFFF
	}
	buf[37] = byte(targetID)
	buf[38] = byte(targetID >> 8)
	buf[39] = byte(targetID >> 16)
	buf[40] = byte(targetID >> 24)

	return buf
}

// sendPositionUpdate sends a 0x40 client update with the current position.
func (e *Engine) sendPositionUpdate() {
	e.State.Conn.QueueClientUpdate(e.buildPositionBuffer())
}

// ForcePositionFlush queues an immediate 0x40 position update with the current
// target ObjectID and flushes it. Call after setting TargetObjectID to ensure
// Sandstorm sees the target in the 0x40 buffer before processing UseSkill.
func (e *Engine) ForcePositionFlush() {
	if e.State.Conn == nil || e.State.Phase != PhaseInWorld {
		return
	}
	buf := e.buildPositionBuffer()
	targetInBuf := uint32(buf[37]) | uint32(buf[38])<<8 | uint32(buf[39])<<16 | uint32(buf[40])<<24
	log.Printf("FLUSH: 0x40 target=0x%08X facing=0x%02X anim=0x%02X", targetInBuf, buf[22], buf[35])
	e.State.Conn.QueueClientUpdate(buf)
	e.State.Conn.Flush()
}

// sendTeleportArrival sends EnableChannel (0x0049) + fresh 0x40 after a teleport.
// PS2 sends this sequence after a loading screen to re-register with the server
// at the new position. Without this, Sandstorm doesn't process the new location.
func (e *Engine) sendTeleportArrival() {
	world := e.State.TeleportWorld
	e.State.TeleportPending = false

	// 1. Send 0x0049: Msg00B2's SendOpSigned(73, x), the 0x00B2's last u32
	// back as a zigzag int. Not the world: local-warp.pcap's world-8 warps
	// carry x = 1 and the PS2 answers 02.
	w := buffer.NewWriterSize(8)
	w.Write7BitEncodedInt64(int64(e.State.TeleportToken))
	if !e.State.Conn.QueueReliable(protocol.OpcodeEnableChannel, w.Bytes()) {
		log.Printf("WARNING: EnableChannel send window full")
	}

	// 2. Send 0x40 with new position
	e.State.Conn.QueueClientUpdate(e.buildPositionBuffer())

	// 3. Flush both in one packet
	e.State.Conn.Flush()

	log.Printf("sendTeleportArrival: 0x0049(token=%d) world=%d + 0x40 at (%.1f, %.1f, %.1f)",
		e.State.TeleportToken, world, e.State.Player.X, e.State.Player.Y, e.State.Player.Z)
}

// SetMoveTarget starts smooth movement toward a destination.
func (e *Engine) SetMoveTarget(x, y, z float32) {
	e.moveTargetActive = true
	e.moveTargetX = x
	e.moveTargetY = y
	e.moveTargetZ = z
	if e.moveSpeed == 0 {
		e.moveSpeed = e.parsedSpeed()
	}
	if e.moveArrivalDist == 0 {
		e.moveArrivalDist = 3.0
	}
}

// StopMovement cancels any active goto/follow movement.
func (e *Engine) StopMovement() {
	e.moveTargetActive = false
	e.followTarget = 0
	e.VelX = 0
	e.VelY = 0
	e.VelZ = 0
	e.StickMagnitude = 0x80
	e.AccelState = 0x80
}

// Follow starts continuously following an entity by objectID.
func (e *Engine) Follow(objectID uint32, dist float32) {
	e.followTarget = objectID
	if dist == 0 {
		dist = 5.0
	}
	e.followDist = dist
}

// updateFollow refreshes the move target from the followed entity's current position.
func (e *Engine) updateFollow() {
	if e.followTarget == 0 {
		return
	}
	ent, ok := e.State.Entities[e.followTarget]
	if !ok {
		return
	}
	// Update move target to entity's current position
	e.moveTargetX = ent.X
	e.moveTargetY = ent.Y
	e.moveTargetZ = ent.Z
	e.moveTargetActive = true
	if e.moveSpeed == 0 {
		e.moveSpeed = e.parsedSpeed()
	}
	if e.moveArrivalDist == 0 {
		e.moveArrivalDist = 3.0
	}
}

// parsedSpeed returns the movement speed from the MemoryDump CM data,
// or 5.0 as fallback if CM data isn't available yet.
func (e *Engine) parsedSpeed() float32 {
	if e.State.ParsedCM != nil && e.State.ParsedCM.Speed > 0 {
		return e.State.ParsedCM.Speed
	}
	return 5.0
}

// IsMoving returns true if the engine is actively moving toward a target.
func (e *Engine) IsMoving() bool {
	return e.moveTargetActive
}

// updateMovement steps the player toward the movement target.
func (e *Engine) updateMovement() {
	if !e.moveTargetActive {
		return
	}

	p := &e.State.Player
	dx := e.moveTargetX - p.X
	dy := e.moveTargetY - p.Y
	dz := e.moveTargetZ - p.Z
	dist := float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))

	if dist <= e.moveArrivalDist {
		if e.followTarget == 0 {
			// Only print arrival for goto, not follow (follow re-acquires target each tick)
			e.moveTargetActive = false
			fmt.Printf("Arrived at (%.1f, %.1f, %.1f) — distance %.1f\n", p.X, p.Y, p.Z, dist)
		}
		return
	}

	// Normalize and step
	step := e.moveSpeed
	if step > dist {
		step = dist
	}
	ratio := step / dist
	p.X += dx * ratio
	p.Y += dy * ratio
	p.Z += dz * ratio

	// Update facing (atan2 of XZ movement direction).
	// PS2 heading range is [-PI, +PI] from atan2. No normalization needed —
	// the 0x40 facing byte encoding (heading * 128/PI) handles any range.
	p.Facing = float32(math.Atan2(float64(dx), float64(dz)))

	// Set velocity for 0x40 encoding (headless has no speed ramping).
	e.VelX = dx * ratio / 0.1 // approximate velocity from step/tick (100ms tick)
	e.VelZ = dz * ratio / 0.1

	// PS2 0x40 auxiliary fields for headless client.
	e.StickMagnitude = 254 // full deflection (no analog)
	e.AccelState = 0x80    // steady (no ramping in headless)
	e.SmoothedFacing = p.Facing // no lag (headless doesn't turn gradually)
}

// SendSay is "/say": 0x000E 0, the say message 0x000B (the line and the
// target), and 0x000E back to the kept chat mode (func_00BCFE78).
func (e *Engine) SendSay(text string) {
	if e.State.Conn == nil {
		return
	}
	e.State.ChatSay(text, e.TargetObjectID)
	log.Printf("Sent say chat: %s", text)
}

// SendShout is "/shout": 0x000E 3, 0x0C01 {2, the line} (func_00CBBE10),
// and 0x000E back.
func (e *Engine) SendShout(text string) {
	if e.State.Conn == nil {
		return
	}
	e.State.ChatShout(text)
	log.Printf("Sent shout chat: %s", text)
}

// SendGroupChat is "/group": 0x000E 2, the say message 0x000B, and 0x000E
// back. The server routes 0x000B by the mode, not by an opcode of its own.
func (e *Engine) SendGroupChat(text string) {
	if e.State.Conn == nil {
		return
	}
	e.State.ChatGroup(text, e.TargetObjectID)
	log.Printf("Sent group chat: %s", text)
}

// SendGuildChat is "/guild": 0x000E 4, the guild message 0x0578 {12, the
// line} (func_00BEF1B0), and 0x000E back.
func (e *Engine) SendGuildChat(text string) {
	if e.State.Conn == nil {
		return
	}
	e.State.ChatGuild(text)
	log.Printf("Sent guild chat: %s", text)
}

// SendChat is a typed line with no /command, in the kept chat mode: a shout
// in mode 3, a guild message in 4, else the say message (no 0x000E).
func (e *Engine) SendChat(text string) {
	if e.State.Conn == nil {
		return
	}
	e.State.ChatLine(text, e.TargetObjectID)
	log.Printf("Sent chat (mode %d): %s", e.State.ChatMode, text)
}

// SendChatCommand sends a whole /command line as the say message, slash and
// all: "/tell" and "/t" (the server reads the name out of it), and S2's
// forwarded commands.
func (e *Engine) SendChatCommand(line string) {
	if e.State.Conn == nil {
		return
	}
	e.State.ChatCommand(line, e.TargetObjectID)
	log.Printf("Sent chat command: %s", line)
}

// SendReply is "/reply": "/t <the last teller> <text>" as the say message.
// It reports false, and sends nothing, with nobody to reply to.
func (e *Engine) SendReply(text string) bool {
	if e.State.Conn == nil {
		return false
	}
	return e.State.ChatReply(text, e.TargetObjectID)
}

// SetChatMode is the chat window's mode choice (GroupChanged in the world).
func (e *Engine) SetChatMode(mode byte) {
	if e.State.Conn == nil {
		e.State.ChatMode = mode
		return
	}
	e.State.SetChatMode(mode)
}

// SendGuildCreate is 0x0578 {1, the name} (func_00BEEC08).
func (e *Engine) SendGuildCreate(name string) {
	if e.State.Conn == nil {
		return
	}
	e.State.GuildCreate(name)
	log.Printf("Sent GuildCreate: %s", name)
}

// SendGuildLeave is 0x0578 {3} (func_00BEED98, the "Leave the guild?" answer).
func (e *Engine) SendGuildLeave() {
	if e.State.Conn == nil {
		return
	}
	e.State.GuildLeave()
	log.Printf("Sent GuildLeave")
}

// SendGuildDisband is 0x0578 {2} (func_00BEECD8, "Disband the guild?").
func (e *Engine) SendGuildDisband() {
	if e.State.Conn == nil {
		return
	}
	e.State.GuildDisband()
	log.Printf("Sent GuildDisband")
}

// SendGuildSetMOTD sets the guild's charter, 0x0578 {11, the text}
// (func_00BEF0E0): the text 0x0468/1 sends back after the guild's name.
func (e *Engine) SendGuildSetMOTD(motd string) {
	if e.State.Conn == nil {
		return
	}
	e.State.GuildCharter(motd)
	log.Printf("Sent GuildSetMOTD: %s", motd)
}

// SendGuildInvite is 0x0578 {8, the name, the guild's name} (func_00BEF478);
// inviting oneself shows word 679 instead.
func (e *Engine) SendGuildInvite(name string) {
	if e.State.Conn == nil {
		return
	}
	e.State.InviteToGuild(name)
	log.Printf("Sent GuildInvite: %s", name)
}

// SendGuildKick removes a member, 0x0578 {6, the member's id} (func_00BEF008).
// The PS2 picks the member out of the member list; a name not in it sends
// nothing.
func (e *Engine) SendGuildKick(name string) {
	if e.State.Conn == nil {
		return
	}
	if e.State.GuildRemove(name) {
		log.Printf("Sent GuildKick: %s", name)
	}
}

// SendGroupInvite is VIClient::InviteToGroup: its checks and texts, then
// 0x0625 {id, name}, and the invitations waiting here declined.
func (e *Engine) SendGroupInvite(targetObjectID uint32, targetName string) {
	if e.State.Conn == nil {
		return
	}
	if e.State.InviteToGroup(targetObjectID, targetName) {
		log.Printf("Sent group invite: target=0x%08X name=%s", targetObjectID, targetName)
	}
}

// SendAcceptGroupInvite is VIClient::AcceptInvite: the oldest invitation
// taken, 0x0622 {its id}, the rest declined. groupID is the id the menu
// shows, which is the oldest invitation's.
func (e *Engine) SendAcceptGroupInvite(groupID uint32) {
	if e.State.Conn == nil {
		return
	}
	e.State.AcceptGroupInvite()
	log.Printf("Sent accept group invite: groupID=%d", groupID)
}

// SendDeclineGroupInvite is VIClient::DeclineInvite: 0x0624 {id, 1}.
func (e *Engine) SendDeclineGroupInvite(groupID uint32) {
	if e.State.Conn == nil {
		return
	}
	e.State.DeclineGroupInvite()
	log.Printf("Sent decline group invite: groupID=%d", groupID)
}

// SendLeaveGroup is VIClient::LeaveGroup: 0x0623, out of the group.
func (e *Engine) SendLeaveGroup() {
	if e.State.Conn == nil {
		return
	}
	e.State.LeaveGroup()
	log.Printf("Sent leave group")
}

// SendBootGroupMember boots a member from the group (opcode 0x0626).
// Wire format: uint32(targetObjectID)
func (e *Engine) SendBootGroupMember(targetObjectID uint32) {
	if e.State.Conn == nil {
		return
	}
	data := make([]byte, 4)
	binary.LittleEndian.PutUint32(data[0:4], targetObjectID)
	if !e.State.Conn.QueueReliable(protocol.OpcodeBootGroupMember, data) {
		log.Printf("WARNING: BootGroupMember send window full")
	}
	log.Printf("Sent boot group member: target=0x%08X", targetObjectID)
}

// SendTarget sends a target request for the given objectID.
func (e *Engine) SendTarget(objectID uint32) {
	if e.State.Conn == nil {
		return
	}

	data := make([]byte, 8)
	// PS2 pcap: first 4 bytes are always 0x00000000 (not an incrementing counter)
	binary.LittleEndian.PutUint32(data[0:4], 0)
	binary.LittleEndian.PutUint32(data[4:8], objectID)

	if !e.State.Conn.QueueReliable(protocol.OpcodeClassicTargetInfo, data) {
		log.Printf("WARNING: Target send window full")
	}
	log.Printf("Sent target request: objectID=0x%08X", objectID)
}

// SendInteract sends an interact request for the given objectID (opcode 0x0004).
func (e *Engine) SendInteract(objectID uint32) {
	if e.State.Conn == nil {
		return
	}
	data := make([]byte, 4)
	binary.LittleEndian.PutUint32(data[0:4], objectID)
	if !e.State.Conn.QueueReliable(protocol.OpcodeInteract, data) {
		log.Printf("WARNING: Interact send window full")
	}
	log.Printf("Sent interact: objectID=%d", objectID)
}

// SendMerchantDiag sends opcode 0x004C — request merchant item list.
// PS2 flow: Interact(0x0004) → server greeting → client sends MerchantDiag(0x004C)
// → server responds with MerchantBox(0x00B7) containing items.
func (e *Engine) SendMerchantDiag(objectID uint32) {
	if e.State.Conn == nil {
		return
	}
	data := make([]byte, 4)
	binary.LittleEndian.PutUint32(data[0:4], objectID)
	if !e.State.Conn.QueueReliable(protocol.OpcodeMerchantDiag, data) {
		log.Printf("WARNING: MerchantDiag send window full")
	}
	log.Printf("Sent MerchantDiag: objectID=%d", objectID)
}

// SendMerchantBuy is VIClient::MerchantRequest (0x00BCCA38, byte-exact):
// SendOpSigned2Int(0x004A, slot, quantity, the merchant), the merchant being
// the id 0x00B7 opened the window with (VIClient+0x1B300), not the target.
func (e *Engine) SendMerchantBuy(merchantSlot int, quantity int) {
	if e.State.Conn == nil {
		return
	}
	npcObjID := e.State.MerchantID
	if !e.State.Conn.QueueReliable(protocol.OpcodeMerchantBuy, merchantBuyPayload(merchantSlot, quantity, npcObjID)) {
		log.Printf("WARNING: MerchantBuy send window full")
	}
	log.Printf("Sent MerchantBuy: slot=%d qty=%d npc=0x%08X", merchantSlot, quantity, npcObjID)
}

// SendMerchantBuyRaw sends a raw MerchantBuy for exploit testing.
func (e *Engine) SendMerchantBuyRaw(data []byte) {
	if e.State.Conn == nil {
		return
	}
	if !e.State.Conn.QueueReliable(protocol.OpcodeMerchantBuy, data) {
		log.Printf("WARNING: MerchantBuy send window full")
	}
	log.Printf("Sent MerchantBuyRaw: %d bytes hex=%X", len(data), data)
}

// SendMerchantSell is VIClient::SellItem (0x00BCCA80, byte-exact):
// SendOpIntSignedInt(0x004B, the item's KEY, quantity, the merchant). key is
// the record's key (GetSlotId), not its place in the list.
func (e *Engine) SendMerchantSell(key int, quantity int) {
	if e.State.Conn == nil {
		return
	}
	if !e.State.Conn.QueueReliable(protocol.OpcodeMerchantSell, merchantSellPayload(key, quantity, e.State.MerchantID)) {
		log.Printf("WARNING: MerchantSell send window full")
	}
	log.Printf("Sent MerchantSell: key=%d qty=%d npc=0x%08X", key, quantity, e.State.MerchantID)
}

// SendInteractItem is VIClient::UseItem (0x00BCABA0, byte-exact):
// SendOpIntIntByte(0x003E, the item's key, the target, 0). The target is
// m_target, -1 with none: a PS2 using an item on a merchant sends the
// merchant (ps2_main_capture). The server decides equip, unequip or use.
func (e *Engine) SendInteractItem(invSlot int) {
	if e.State.Conn == nil {
		return
	}
	if !e.State.Conn.QueueReliable(protocol.OpcodeInteractItem, useItemPayload(uint32(invSlot), noTarget(e.TargetObjectID), 0)) {
		log.Printf("WARNING: InteractItem send window full")
	}
	log.Printf("Sent InteractItem: invSlot=%d opcode=0x003E", invSlot)
}

// SendEquipItem sends InteractItem for the given inventory slot.
func (e *Engine) SendEquipItem(invSlot int) {
	e.SendInteractItem(invSlot)
}

// SendUnequipItem sends InteractItem for the given inventory slot.
func (e *Engine) SendUnequipItem(invSlot int) {
	e.SendInteractItem(invSlot)
}

// SendPrepareItem sends opcode 0x000A — equipment hotbar swap.
// PS2 pcap: invSlot(uint32 LE) + action(uint8) = 5 bytes.
// Action: 0=unequip to hotbar, 2=equip from hotbar, 3=swap hotbar slot.
func (e *Engine) SendPrepareItem(invSlot int, action byte) {
	if e.State.Conn == nil {
		return
	}
	data := make([]byte, 5)
	binary.LittleEndian.PutUint32(data[0:4], uint32(invSlot))
	data[4] = action
	if !e.State.Conn.QueueReliable(protocol.OpcodePrepareItem, data) {
		log.Printf("WARNING: PrepareItem send window full")
	}
	log.Printf("Sent PrepareItem: slot=%d action=%d", invSlot, action)
}

// SendBlacksmithOpen sends opcode 0x0061 — open blacksmith menu.
func (e *Engine) SendBlacksmithOpen(npcData uint32) {
	if e.State.Conn == nil {
		return
	}
	data := make([]byte, 4)
	binary.LittleEndian.PutUint32(data[0:4], npcData)
	if !e.State.Conn.QueueReliable(protocol.OpcodeBlackSmithMenu, data) {
		log.Printf("WARNING: BlackSmithMenu send window full")
	}
	log.Printf("Sent BlackSmithMenu: data=0x%08X", npcData)
}

// SendRepairItem is VIClient::Send0062 (0x00BCCC70, byte-exact):
// SendOpIntInt(0x0062, the item's key, the blacksmith 0x0061 named).
func (e *Engine) SendRepairItem(invSlot int, blacksmith uint32) {
	if e.State.Conn == nil {
		return
	}
	data := make([]byte, 8)
	binary.LittleEndian.PutUint32(data[0:4], uint32(invSlot))
	binary.LittleEndian.PutUint32(data[4:8], blacksmith)
	if !e.State.Conn.QueueReliable(protocol.OpcodeRequestRepair, data) {
		log.Printf("WARNING: RequestRepair send window full")
	}
	log.Printf("Sent RequestRepair: invSlot=%d blacksmith=0x%08X", invSlot, blacksmith)
}

// SendCloseBlacksmith sends opcode 0x0063 — close blacksmith window.
func (e *Engine) SendCloseBlacksmith() {
	if e.State.Conn == nil {
		return
	}
	if !e.State.Conn.QueueReliable(protocol.OpcodeCloseBlacksmithMenu, nil) {
		log.Printf("WARNING: CloseBlacksmith send window full")
	}
	log.Printf("Sent CloseBlacksmith")
	e.State.BlacksmithOpen = false
}

// SendPetCommand is VIClient::PetCommand (0x00BD7430): with no pet (0x007E's
// id, HasPet), "You do not have a pet." (1021) and nothing sent; else 0x0041
// and the command byte. The pet menu sends 0-4 and 7, and 8 with the target
// after it (SendOpByteRaw, pet attack); 5 is only ever the "Dismiss pet?"
// prompt's yes (Send0041), and no CLIENT path sends 6.
func (e *Engine) SendPetCommand(cmd byte) {
	if e.State.Conn == nil {
		return
	}
	if e.State.PetID == 0xFFFFFFFF {
		e.State.socialShowWord(1021)
		return
	}
	data := []byte{cmd}
	if cmd == 8 {
		data = binary.LittleEndian.AppendUint32(data, noTarget(e.TargetObjectID))
	}
	if !e.State.Conn.QueueReliable(protocol.OpcodePetCommands, data) {
		log.Printf("WARNING: PetCommand send window full")
	}
	log.Printf("Sent PetCommand: cmd=0x%02X", cmd)
}

// SendAttack sends an auto-attack request (opcode 0x000F) to the given target.
// Wire format: uint32 LE target ObjectID (4 bytes).
// PS2 auto-faces the target on attack — we do the same by computing the
// angle from player to target entity and updating facing before sending.
func (e *Engine) SendAttack(objectID uint32) {
	if e.State.Conn == nil {
		return
	}
	// Auto-face target (PS2 behavior)
	e.faceTarget(objectID)
	data := make([]byte, 4)
	binary.LittleEndian.PutUint32(data, objectID)
	if !e.State.Conn.QueueReliable(protocol.OpcodeAttack, data) {
		log.Printf("WARNING: Attack send window full, command dropped")
	}
	log.Printf("Sent attack: objectID=%d", objectID)
}

// faceTarget computes the angle from the player to the target entity and
// updates the player's facing direction. PS2 does this automatically on
// attack/cast via VIWalkControl::SetTargetHeading.
func (e *Engine) faceTarget(objectID uint32) {
	if ent, ok := e.State.Entities[objectID]; ok {
		dx := float64(ent.X - e.State.Player.X)
		dz := float64(ent.Z - e.State.Player.Z)
		if dx != 0 || dz != 0 {
			e.State.Player.Facing = float32(math.Atan2(dx, dz))
		}
	}
}

// SendCast sends a UseSkill request (opcode 0x0032) to the server.
// Wire format (9 bytes): uint32 LE zero + uint8 hotbarSlot + uint32 LE target ObjectID.
// hotbarSlot = ParsedSpell.WhereOnHotBar (confirmed: NOT AbilityLevel).
// Use 0xFFFFFFFF as objectID for self-cast.
func (e *Engine) SendCast(abilityByte uint8, objectID uint32) {
	if e.State.Conn == nil {
		return
	}
	data := make([]byte, 9)
	// bytes 0-3: the ability stamp (Send0032), 0 on Sandstorm — every one of
	// 35 PS2 pcap samples is zero
	binary.LittleEndian.PutUint32(data[0:4], e.State.AbilityStamp)
	data[4] = abilityByte
	binary.LittleEndian.PutUint32(data[5:9], objectID)
	log.Printf("CAST: ability=%d target=0x%08X hex=%X", abilityByte, objectID, data)
	if !e.State.Conn.QueueReliable(protocol.OpcodeUseSkill, data) {
		log.Printf("WARNING: UseSkill send window full, command dropped")
	}
}

// SendDialogueOption sends a dialogue option selection (opcode 0x0035).
// dialogueCounter is the counter from the server's 0x0034 OptionBox response.
// option is the 0-based option index, or 255 to cancel.
func (e *Engine) SendDialogueOption(dialogueCounter uint32, option byte) {
	if e.State.Conn == nil {
		return
	}
	data := make([]byte, 5)
	binary.LittleEndian.PutUint32(data[0:4], dialogueCounter)
	data[4] = option
	if !e.State.Conn.QueueReliable(protocol.OpcodeDialogueBoxOption, data) {
		log.Printf("WARNING: DialogueOption send window full")
	}
	log.Printf("Sent dialogue option: counter=%d option=%d", dialogueCounter, option)
}

// SendAddSkillToBar sends opcode 0x0033 (Send0033): the ability stamp, the
// ability's DISPLAY index (ParsedSpell.AddedOrder) and a bar slot 0-9. The
// server echoes it as 0x00B4, which memorizes the ability if it was not and
// forgets it if it was — so nothing changes locally until then.
func (e *Engine) SendAddSkillToBar(displayIndex byte, hotbarSlot byte) {
	if e.State.Conn == nil {
		return
	}
	data := make([]byte, 6)
	binary.LittleEndian.PutUint32(data[0:4], e.State.AbilityStamp)
	data[4] = displayIndex
	data[5] = hotbarSlot
	if !e.State.Conn.QueueReliable(protocol.OpcodeAddSkillToBar, data) {
		log.Printf("WARNING: AddSkillToBar send window full")
	}
	log.Printf("Sent AddSkillToBar: display=%d slot=%d", displayIndex, hotbarSlot)
}

// MemorizeAbility puts an ability on the bar at slot with 0x0033. The
// server's echo (0x00B4) swaps memorized and forgotten, so an ability on the
// bar elsewhere is forgotten there first, and one already at slot is left.
func (e *Engine) MemorizeAbility(sp *ParsedSpell, slot byte) {
	if sp == nil {
		return
	}
	if sp.OnHotBar != 0 {
		if sp.WhereOnHotBar == int64(slot) {
			return
		}
		e.SendAddSkillToBar(byte(sp.AddedOrder), byte(sp.WhereOnHotBar))
	}
	e.SendAddSkillToBar(byte(sp.AddedOrder), slot)
}

// ForgetAbility takes a memorized ability off the bar: 0x0033 at its slot.
func (e *Engine) ForgetAbility(sp *ParsedSpell) {
	if sp == nil || sp.OnHotBar == 0 {
		return
	}
	e.SendAddSkillToBar(byte(sp.AddedOrder), byte(sp.WhereOnHotBar))
}

// SendDisconnect sends opcode 0x09B0 to notify the server of a clean disconnect,
// then flushes to ensure it's sent before the socket closes.
func (e *Engine) SendDisconnect() {
	if e.State.Conn == nil {
		return
	}
	data := make([]byte, 0)
	if !e.State.Conn.QueueReliable(protocol.OpcodeDisconnectClient, data) {
		log.Printf("WARNING: Disconnect send window full")
	}
	e.State.Conn.Flush()
	log.Printf("Sent Disconnect (0x09B0)")
}

// SendClearSpellBar sends opcode 0x00FE to clear all spells from the hotbar.
func (e *Engine) SendClearSpellBar() {
	if e.State.Conn == nil {
		return
	}
	data := make([]byte, 4) // the ability stamp (Send00FE); the answer is 0x00FF
	binary.LittleEndian.PutUint32(data, e.State.AbilityStamp)
	if !e.State.Conn.QueueReliable(protocol.OpcodeClearSpellBar, data) {
		log.Printf("WARNING: ClearSpellBar send window full")
	}
	log.Printf("Sent ClearSpellBar")
}

// SendWhoList is VIClientPart38334::Open (0x00C03038): Close (the token one
// more), then 0x0E01 {1, the token, zigzag}. A raw byte stood in for the
// zigzag and went bad at the 64th request, a one-byte varint that is not one.
func (e *Engine) SendWhoList() {
	if e.State.Conn == nil {
		return
	}
	e.whoToken++
	if !e.State.Conn.QueueReliable(protocol.OpcodeWhoList, whoPayload(e.whoToken)) {
		log.Printf("WARNING: WhoList send window full")
	}
	log.Printf("Sent WhoList request token=%d", e.whoToken)
}

// SendPlayerSearch is Sandstorm's search request (0x1502, its S2 cave
// 0x000FFD80): race, class, minimum and maximum level, one byte each, 0xFF
// for any. The results arrive as 0x1501 (clear) and 0x1500 (one each).
func (e *Engine) SendPlayerSearch(race, class, minLevel, maxLevel byte) {
	if e.State.Conn == nil {
		return
	}
	e.State.Conn.QueueReliable(protocol.OpcodePlayerSearchRequest, []byte{race, class, minLevel, maxLevel})
	log.Printf("Sent player search: race=%d class=%d level %d-%d", race, class, minLevel, maxLevel)
}

// SendPlayerSearchRegister lists (true) or unlists the player in Sandstorm's
// player search: 0x1503 or 0x1504, an opcode and nothing else (the S2 Group
// menu's Register / Unregister).
func (e *Engine) SendPlayerSearchRegister(register bool) {
	if e.State.Conn == nil {
		return
	}
	op := protocol.OpcodePlayerSearchUnregister
	if register {
		op = protocol.OpcodePlayerSearchRegister
	}
	e.State.Conn.QueueReliable(op, nil)
	log.Printf("Sent player search register=%v", register)
}

// SendAuctionRequest sends an auction request (opcode 0x1205): the
// sub-command (7 = a search page, see auction.go), the request number as the
// one zigzag byte it is in practice, and the rest.
func (e *Engine) SendAuctionRequest(sub byte, seq byte, extra []byte) {
	if e.State.Conn == nil {
		return
	}
	data := make([]byte, 2+len(extra))
	data[0] = sub
	data[1] = seq
	copy(data[2:], extra)
	if !e.State.Conn.QueueReliable(protocol.OpcodeAuctionRequest, data) {
		log.Printf("WARNING: AuctionRequest send window full")
	}
	log.Printf("Sent AuctionRequest sub=0x%02X seq=%d extraLen=%d", sub, seq, len(extra))
}

// zigzagEncode encodes an int64 as zigzag varint bytes.
func zigzagEncode(val int64) []byte {
	uval := uint64((val << 1) ^ (val >> 63))
	var buf []byte
	for uval >= 0x80 {
		buf = append(buf, byte(uval)|0x80)
		uval >>= 7
	}
	buf = append(buf, byte(uval))
	return buf
}

// SendAuctionInit sends opcode 0x1101 — auction board init/navigation.
// PS2 pcap: sub=4 opens auction, sub=6 selects category.
func (e *Engine) SendAuctionInit(sub byte, extra []byte) {
	if e.State.Conn == nil {
		return
	}
	data := make([]byte, 5+len(extra))
	data[0] = sub
	binary.LittleEndian.PutUint32(data[1:5], 0) // unk1 = 0
	copy(data[5:], extra)
	if !e.State.Conn.QueueReliable(0x1101, data) {
		log.Printf("WARNING: AuctionInit send window full")
	}
	log.Printf("Sent AuctionInit sub=%d extraLen=%d", sub, len(extra))
}

// SendLFGToggle toggles the player's LFG registration by sending "lfg" or "lfg off" in chat.
// Sandstorm uses chat commands for LFG, not a dedicated opcode.
func (e *Engine) SendLFGToggle(register bool) {
	if register {
		e.SendSay("lfg")
	} else {
		e.SendSay("lfg off")
	}
	e.State.LFGRegistered = register
}
