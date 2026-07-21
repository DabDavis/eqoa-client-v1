package main

import (
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/eqoa/client/internal/audio"
	"github.com/eqoa/client/internal/game"
	"github.com/eqoa/client/internal/models"
	"github.com/eqoa/client/internal/sky"
	"github.com/eqoa/client/internal/terrain"
	"github.com/eqoa/client/internal/ui"
	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

var (
	screenWidth  = int32(640)
	screenHeight = int32(480)
	renderWidth  = int32(640) // actual framebuffer pixels (differs from screen on HiDPI)
	renderHeight = int32(480)
)

// PS2 VIUI TexIDs used by the HUD (from EE memory GetUITexture table at 0x004EDAF8).
const (
	texCompassBezel = 6   // silver ring + blue N arrow (59x69)
	texCompassRose  = 129 // N/S/E/W stone disc (44x44)
	texDarkStrip    = 136 // thin dark bar background (60x10)
	texBarHP        = 7   // red HP fill (55x6)
	texBarXP        = 8   // gold XP fill (55x6)
	texBarPower     = 9   // blue power fill (55x6)
	texSeparator    = 135 // ornate stone separator (104x15)
	texSpellSlot    = 29  // spell slot square (36x31), just below golden scroll border
	texScrollUp     = 65  // scroll up arrow (17x16)
	texScrollDown   = 67  // scroll down arrow (17x16)
	texSpellBorder  = 30  // golden scroll border (44x9)
	texFaceHostile  = 115 // hostile face (red, 23x21)
	texFaceNeutral  = 116 // neutral face (gray, 19x19)
	texFaceFriendly = 117 // friendly face (blue, 21x19)
)

type debugModelEntry struct {
	modelID int32
	name    string
}

type Client struct {
	engine         *game.Engine
	terrainLoader    *terrain.Loader
	terrainRender    *terrain.Renderer
	floraRender      *terrain.FloraRenderer
	terrainStarted   bool
	terrainZoneIdx   int // primary zone (for collision, flora, effects)
	loadedZoneIdxs   map[int]bool // all currently loaded zone indices
	modelMgr       *models.ModelManager
	skyDome        *sky.Sky
	camAngle       float32 // horizontal orbit angle (radians)
	camPitch       float32 // vertical elevation angle (radians)
	camDist        float32 // distance from player
	camMaxDist     float32 // max zoom-out distance (settings adjustable, hard max 300)
	camSensitivity float32 // camera sensitivity multiplier (default 1.0)
	camAngleVel    float32 // PS2 VIWalkCamera: angular velocity for orbit damping
	camPitchVel    float32 // PS2 VIWalkCamera: pitch velocity for orbit damping
	camDistVel     float32 // PS2 VIWalkCamera: distance velocity for zoom damping
	camFollow      bool    // PS2 VIWalkCamera mode: false=tether (default), true=follow
	camSavedDist   float32 // saved distance for camera mode toggle
	camFollowDist  float32 // locked follow cam distance
	firstPerson    bool    // true = first-person camera mode
	camMode        int     // 0=tether, 1=follow, 2=first-person
	invertPitch    bool    // invert controller pitch (stick up = look down)
	camCollision   bool    // camera terrain/wall collision clamping
	camCollDist    float32 // smoothed collision distance (fraction 0-1, prevents bouncing)
	safeX, safeY, safeZ float32 // last known position outside walls
	safeValid      bool         // true once first safe position is recorded

	// PS2 VIWalkControl state
	runMode          bool    // always true (run mode)
	autoRun          bool    // auto-run: continuous forward movement (L3 toggle)
	currentSpeed     float32 // current movement speed (smoothly interpolated)
	lastMoveX        float32 // last non-zero movement direction X (for deceleration)
	lastMoveZ        float32 // last non-zero movement direction Z (for deceleration)
	targetFacing     float32 // PS2 VIWalkControl+0x108: target heading (set by SetTargetHeading)
	turnVel          float32 // PS2 VIWalkControl: turn angular velocity (force-based with damping)

	// PS2 VIWalkPhysics vertical state (ported from UpdatePhysics at 0x486198)
	velocityY float32 // vertical velocity (VIWalkPhysics +0xCC in PS2 struct)
	onGround  bool    // true when player is on walkable ground
	swimVelY  float32 // player-requested swim vertical velocity (Q/E/gamepad right stick)

	// PS2 VIWalkControl swim state (ported from VIWalkControl::Move at 0x486778)
	waterState  terrain.WaterState // current water/swim state
	breathTimer float32            // seconds of breath remaining (60 = full, 0 = drowning)

	// Animation manager (combat, debug cycling, landing)
	animMgr *AnimationManager

	// Audio system for spell sounds and ambient audio
	audioSys        *audio.System
	bgmZones        []audio.BGMZone // StreamAudioSprite bounding boxes from TUNARIA
	ambientTracks   map[int32]*audio.AmbientTrack
	lastAmbientID   int32
	masterVolume    float32         // master volume 0-1
	sfxVolume       float32         // SFX volume 0-1
	bgmVolume       float32         // BGM volume 0-1
	bgmMuted        bool            // BGM muted
	currentBGM      string          // currently playing BGM name (avoid re-reading ISO)
	battleBGMActive bool            // PS2 VIBattleMusic: battle BGM currently playing
	battleTrackRand int             // rotating index for battle track selection

	// Display settings
	windowMode      int   // 0=windowed, 1=borderless, 2=fullscreen
	vsyncOn         bool  // VSync enabled
	fpsCap          int32 // FPS cap (30/60/120/999)
	showFPS         bool  // show FPS counter
	wasFocused      bool  // track focus changes for exclusive fullscreen restore

	// Display change confirmation
	displayConfirmPending bool    // true while waiting for user to confirm display change
	displayConfirmTimer   float32 // seconds remaining before auto-revert
	prevScreenWidth       int32   // previous resolution (for revert)
	prevScreenHeight      int32
	prevWindowMode        int
	bgmRestartDelay       int     // frames to wait before restarting BGM after display change
	musicPlayerOn   bool            // true while music player is overriding zone BGM
	musicHudOn      bool            // true to show persistent music player HUD
	musicTrackIdx   int             // current track index in titleBGMTracks
	musicScrollX    float32         // scrolling title X offset
	musicHudX       int32           // floating window position
	musicHudY       int32
	musicHudDrag    bool            // true while dragging
	musicHudDragOX  int32           // drag offset from window origin
	musicHudDragOY  int32
	prevMasterVolume float32        // saved master vol before mute

	// Per-entity sound state (footsteps, attack grunts)
	entitySounds    map[uint32]*entitySoundState

	// Lightning rendering system (VILightning port)
	lightningSystem *game.LightningSystem

	// Debug spell effect cycling (F9 key)
	debugFXIndex  int     // current index into effect DictID list
	debugFXActive bool    // true when F9 cycling is active
	debugFXIDs    []int32 // sorted list of all effect DictIDs (cached)

	// Debug model override (F7 key) — visual only, not sent to server
	debugModelID    int32  // override model DictID (0 = use real model)
	debugModelNames []debugModelEntry // cached list of nearby entity models

	// Targeting
	targetObjectID uint32 // currently targeted entity ObjectID (0 = none)
	spaceWasDown   bool   // debounce for spacebar toggle (IsKeyPressed fires multi-frame)

	// Gamepad state
	// PS2 Hotkey Menu (L2 + D-pad tree navigation)
	hotkeyActive    bool        // true while hotkey overlay visible
	hotkeyDirCode   int64       // current position in tree (0=root)
	hotkeyFadeOut   float32     // fade-out alpha timer (1.0 → 0.0 after release)
	hotkeyTree      *hotkeyTree // cached parent/child maps (built on first use)
	hotkeyCloseTime float32     // countdown suppressing arrow movement after menu close

	// Gamepad spell bar cursor (D-pad navigation when no menu is open)
	spellBarCursor  int     // currently highlighted slot (0-9)
	spellBarVisible bool    // true when gamepad cursor is shown
	spellBarFade    float32 // fade-out timer after inactivity

	// Selection ring (PS2 Selection_Rings HSprite from UI.ESF — 2 textures)
	reticleTex     rl.Texture2D // main ring texture (Surface 0x1C66BEDF)
	twinkleTex     rl.Texture2D // twinkle/sparkle texture (Surface 0xD71D96CC)
	reticleOK      bool         // true if main ring texture loaded
	twinkleOK      bool         // true if twinkle texture loaded
	reticleSpin    float32      // current rotation angle (degrees), slow spin
	twinkleAlpha   float32      // twinkle fade alpha (1.0 on target switch, fades to 0)
	lastTargetID    uint32       // previous target ObjectID for detecting switches
	lastPlayerHitFX time.Time   // throttle player melee hit FX to once per round

	// Merchant window state
	merchantScroll int // scroll offset for merchant item list

	// Blacksmith/repair window
	blacksmithScroll int
	blacksmithSel    int

	// Item context menu
	itemContextOpen    bool           // true when context popup is visible
	itemContextKey     uint32         // item Key for inspect lookup
	itemContextInvSlot int            // server inventory slot for equip/unequip
	itemContextSlot    int64         // item Slot type (>0 = equippable)
	itemContextTab  int            // which inv sub-tab the item is from (0=equipped, 1=carrying)
	itemContextRow  int            // cursor row within context menu
	itemContextX     int32          // popup screen X
	itemContextY     int32          // popup screen Y
	itemContextDelay int            // frames to skip input after opening

	// Item inspect window (dedicated overlay, replaces dialogue-based inspect)
	inspectOpen bool            // true when inspect window is visible
	inspectItem *game.ParsedItem // item being inspected (nil = closed)


	// Dialogue cursor for gamepad/keyboard navigation
	dialogueCursor int // highlighted option index (-1 = none)

	// Custom font (Liberation Sans = Arial)
	font rl.Font

	// PS2-faithful VIUI rendering system (replaces uiAtlasTex)
	viui *ui.VIUI

	// Chat fade state -- opaque when active, fades to transparent
	chatLastActivity time.Time // last time a new chat message arrived
	chatLastCount    int       // previous ChatLog length (to detect new messages)

	// Chat input state
	chatTyping      bool   // true when text input mode is active
	chatBuffer      string // current text being typed
	chatDefaultMode int    // default chat channel: 0=say, 1=shout, 2=group, 3=guild
	chatModePopup    bool   // true when chat mode picker is visible
	chatScrollOffset int    // lines scrolled up from bottom (0 = latest)
	chatDragging     bool   // true while dragging chat scrollbar
	lastTellSender  string // name of last player who sent a tell (for /r)
	followTarget    bool   // true when auto-following current target
	wrapCache       map[wrapCacheKey][]string // cached wrapText results

	// Login screen state
	loginUser     string
	loginPass     string
	loginFocus    int    // 0=username, 1=password, 2=remember checkbox
	loginRemember bool   // "Remember me" checkbox state
	loginError    string // error message from last attempt (main goroutine only)
	loginBusy      bool   // true while auth goroutine running (main goroutine only)
	loginResultCh  chan loginResult // auth goroutine → main goroutine handoff
	loginWantsExit  bool // true when window close requested
	loginWantsTitle bool // true when back arrow clicked (return to title)
	assetsDir      string // saved for engine creation after login
	isoPath        string // path to EQOA ISO (optional, reads assets directly from disc image)
	serverIP       string // server IP (from config or default)

	// Logout timer (Alt+F4 in-world)
	logoutActive bool
	logoutTime   time.Time // when the logout will complete

	// Main menu (F12)
	menuOpen       bool // true when main menu overlay is visible
	menuTab        int  // 0=main, 1=equipment, 2=inventory
	menuRow        int  // controller cursor row within current tab
	menuScroll     int  // scroll offset for inventory/equipment lists
	menuInputDelay int  // frames to skip input after tab switch
	invSubTab    int  // 0=Equipped, 1=Carrying, 2=Bank
	equipGridSel int  // 0-7 cursor position in equipment grid (Equipped tab)
	charSubTab   int  // 0=Stats, 1=Spells, 2=CM
	socialSubTab   int // 0=Who, 1=Group, 2=LFG, 3=Auction
	guildSubTab    int // 0=Roster, 1=Chat
	settingsSubTab int     // 0=Camera, 1=Audio
	menuStickLatched bool // right stick sub-tab latch (fires once per deflection)
	whoRequested      bool // true after first who list request sent
	whoSortByLevel    bool // false=alphabetical, true=by level
	auctionRequested  bool      // true after first auction browse sent
	auctionFetchAll    bool      // auto-fetch all pages
	auctionLastFetch   time.Time // throttle for auto-fetch
	auctionPage        int       // current page for UI
	auctionSearchName  string    // filter: item name search text
	auctionSearchFocus bool      // text input active
	auctionLevelMin    int       // filter: min level (0=any)
	auctionLevelMax    int       // filter: max level (100=any)

	// Group tab state
	groupInviteInput  string // text input for invite-by-name
	groupTyping       bool   // true when typing in invite input field

	// Spell icon textures (loaded from UI.ESF)
	iconTextures   map[int32]rl.Texture2D // DictID → GPU texture
	iconList       []int32                // ordered DictIDs for fallback indexing
	iconCacheSpell map[int64]rl.Texture2D // spell Icon value → resolved texture (cached)

	// Item icon textures (loaded from ITEMICON.ESF)
	itemIconTextures map[int32]rl.Texture2D // DictID → GPU texture
	itemIconList     []int32                // ordered DictIDs for fallback indexing
	itemIconCache    map[int64]rl.Texture2D // ItemIcon value → resolved texture (cached)

	// Spell popup state (Memorize / Info context menu)
	spellCtxSpellID int64 // SpellID of clicked spell (-1 = no popup)
	spellCtxX       int32 // popup X position
	spellCtxY       int32 // popup Y position
	spellSlotPicker bool  // true = slot picker mode
	spellInfoPopup  bool  // true = info popup mode
	spellCtxRow     int   // gamepad-highlighted row in context menu (0=Memorize, 1=Info)
	spellSlotIdx    int   // gamepad-highlighted slot in slot picker (0-9)

	// Training points popup
	tpPopupOpen bool

	// Auto-approach pathfinding
	navDebug     bool // F7 toggles debug overlay
	portalTarget uint32
	portalNav    *terrain.PortalNav
	navGridPath  []terrain.GridWaypoint
	navGridIdx   int
	navGridTX    float32
	navGridTZ    float32
	rampClimbing bool // true when Y nudge is active (skip all ground snaps)

	// Zone effect volumes (fog, dust, leaves, light shafts)
	effectVolumes *game.EffectVolumeSystem
	weather       *game.WeatherSystem
	zoneParticlesSpawned bool // true after zone ParticleSprites have been spawned

	// GM menu (F11) — PS2 VIWndGame GM command system
	gmMenuOpen     bool
	gmMenuScroll   int
	gmCategory     int        // 0=top-level, 1=teleport, 2=player, 3=entity, 4=debug, 5=zone list, 6=entity list
	gmInputBuf     string     // text input for current field
	gmInputField   int        // 0=X, 1=Y, 2=Z (coord input) or generic single-field
	gmInputMode    bool       // true when typing coordinates or text
	gmInputVals    [3]float32 // parsed X, Y, Z values
	gmInputAction  int        // what action the input is for (gmAction* constants)
	gmFastRun      bool       // PS2 "Fast Run" toggle — 4x speed multiplier
	gmDisplayPos   bool       // PS2 "DISPLAY POS" — persistent position HUD
	gmInvis        bool       // PS2 "Toggle Invis" — client-side invisibility
	gmNoClip       bool       // noclip fly mode (no collision, no gravity)
	gmShowStats    bool       // PS2 "Toggle Stats" — FPS, entity count, network
	gmExtrapolate  bool       // PS2 extrapolate/interpolate toggle (default: interpolate)
	gridRunner     *gridRunnerState // grid runner autopilot (GM menu)
	// Appearance editor on char select
	appearanceEditActive bool    // true when editing appearance of existing char
	appearanceEditIdx    int     // which char index is being edited
	appearanceFacing     float32 // manual rotation angle during appearance edit

	loreCategory   int  // -1=category list, 0=history, 1=races, 2=gods

	// World map overlay (M key)
	worldMapOpen bool
	worldMapTex  rl.Texture2D
	mapZoom      float32 // zoom level (1.0 = fit screen)
	mapPanX      float32 // normalized pan center X (0-1)
	mapPanY      float32 // normalized pan center Y (0-1)
	minimap      minimapState
	minimapOn    bool

	// PS2 VIFlyCamera state (noclip fly mode — force-based exponential damping)
	flyVelX, flyVelY, flyVelZ float32 // linear velocity
	flyForceX, flyForceY, flyForceZ float32 // per-frame force accumulator

	// Current world ID — 0=Tunaria (default), changes via MemoryDump/Teleport
	currentWorld byte

	// GM teleport auto-reconnect (Sandstorm workaround: disconnect → reconnect → override pos)
	gmReconnect          bool    // true = skip login, auto-login + auto-select character
	gmReconnectChar      int     // saved character index to auto-select
	gmReconnectX         float32 // target position to apply after memory dump
	gmReconnectY         float32
	gmReconnectZ         float32
	gmReconnectReady     bool    // true once in-world, waiting to apply coords

	// Title screen (loaded from SETUP.ESF)
	titleScr   *titleScreen // title screen textures (loaded once)
	titleShown bool         // true after title screen has been shown (skip on subsequent loops)

	// Loading screen (procedural gear + UI.ESF text)
	loadScr          *loadingScreen // loading screen textures
	loadScrAngle     float32        // current gear rotation angle (degrees)
	loadScrFade      float32        // seconds remaining for loading overlay (0 = hidden)
	lastMemDumpCount int            // tracks MemoryDumpCount to detect coaching/death

	// Bone-attached particle emitters (CSprite PartEmitters 0x2960).
	// PS2: CreateParticleEmitters spawns per-entity, UpdateParticleEmitters tracks bones.
	boneEmitters   map[uint32][]boneEmitterEntry   // entityObjectID → active bone emitters
	weaponEmitters map[uint32][]weaponEmitterEntry  // entityObjectID → active weapon particle emitters // entityObjectID → active emitters

	// Pre-allocated per-frame entity buffers (avoid allocation every frame)
	visibleEnts   []*game.Entity             // entities passing frustum cull (reused each frame)
	pointLightBuf []esf.PointLightPlacement  // point lights buffer (reused each frame)

	// Equipment preview (inventory menu 3D character model)
	equipPreviewTex  rl.RenderTexture2D // render target for equipment preview
	equipPreviewInit bool               // true once render texture allocated
	equipPreviewRot  float32            // slow rotation angle

	// Character select 3D preview
	charSelectIdx   int                  // currently highlighted character index (-1 = none)
	charSelectTime  float64              // elapsed time for model rotation animation

	// Delete character confirmation
	deleteConfirmActive bool // true when delete confirmation dialog is shown

	// Character creation wizard
	charCreate charCreateState
}

// Default server constants (Sandstorm)
const (
	defaultServerIP    = "5.161.217.188"
	sandstormGamePort  = 10070
	sandstormAuthPort  = 9735
	sandstormPatchPort = 7000
)

func main() {
	// Reduce GC frequency — live heap has many pointers causing high GC CPU.
	// GOGC=200: trigger at 2x live heap instead of default 1x.
	debug.SetGCPercent(200)

	// Resolve assets directory: CLI arg > config > ./assets (distro) > ../extracted-assets (dev)
	assetsDir := ""
	if len(os.Args) > 1 {
		assetsDir = os.Args[1]
	}
	if assetsDir == "" {
		if cfg := loadLoginConfig(); cfg.AssetsDir != "" {
			assetsDir = cfg.AssetsDir
		}
	}
	if assetsDir == "" {
		// Check relative to CWD first, then relative to binary location
		if info, err := os.Stat("assets"); err == nil && info.IsDir() {
			assetsDir = "assets"
		} else if exePath, err := os.Executable(); err == nil {
			exeDir := filepath.Dir(exePath)
			candidate := filepath.Join(exeDir, "assets")
			if info, err := os.Stat(candidate); err == nil && info.IsDir() {
				assetsDir = candidate
			}
		}
		if assetsDir == "" {
			assetsDir = filepath.Join("..", "extracted-assets")
		}
	}

	// Resolve ISO path: config > auto-detect next to binary or in assets dir.
	isoPath := ""
	if cfg := loadLoginConfig(); cfg.ISOPath != "" {
		isoPath = cfg.ISOPath
	}
	if isoPath == "" {
		// Auto-detect ISO in common locations.
		candidates := []string{
			filepath.Join(assetsDir, "eqoa.iso"),
		}
		if exePath, err := os.Executable(); err == nil {
			candidates = append(candidates, filepath.Join(filepath.Dir(exePath), "eqoa.iso"))
		}
		// Also check for the full-name ISO.
		for _, dir := range []string{assetsDir, "."} {
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".iso") {
					candidates = append(candidates, filepath.Join(dir, e.Name()))
				}
			}
		}
		for _, c := range candidates {
			if info, err := os.Stat(c); err == nil && !info.IsDir() && info.Size() > 1_000_000_000 {
				isoPath = c
				break
			}
		}
	}

	log.Printf("EQOA Client starting")
	log.Printf("Assets: %s", assetsDir)
	if isoPath != "" {
		log.Printf("ISO: %s", isoPath)
	}

	client := &Client{
		camAngle:        -math.Pi * 0.75,
		camPitch:        0.5236, // PS2 default: PI/6 (30 degrees)
		camDist:         11.75, // PS2 VIWalkCamera default: InitParms+0x254
		camMaxDist:      8,
		camSensitivity:  1.0,
		onGround:        true,
		terrainZoneIdx:  -1,
		loadedZoneIdxs:  make(map[int]bool),
		animMgr:         &AnimationManager{DebugAnimOverride: -1},
		spellCtxSpellID: -1,
		charSelectIdx:   -1,
		runMode:         true,
		loreCategory:    -1,
		masterVolume:    1.0,
		sfxVolume:       1.0,
		bgmVolume:       0.5,
		fpsCap:          60,
		musicHudX:       -1, // -1 = uninitialized, set to default on first draw
	}

	// Init raylib window
	rl.SetTraceLogLevel(rl.LogWarning)
	rl.InitWindow(640, 480, "EverQuest Online Adventures")
	rl.SetExitKey(0)
	rl.SetTargetFPS(60)
	rl.SetClipPlanes(1.0, 5000.0)

	// Initialize audio device
	client.audioSys = audio.NewSystem()

	// Font starts as Raylib default; loadAssets() replaces with EQOA bitmap font (ARIAL.CSF).
	client.font = rl.GetFontDefault()

	// Set asset paths before loading (openAssetESF needs these).
	client.assetsDir = assetsDir
	client.isoPath = isoPath

	// Load assets
	client.loadAssets(assetsDir)
	client.animMgr.modelMgr = client.modelMgr

	// Load title screen textures (SETUP.ESF)
	if setupFile, err := client.openAssetESF("SETUP.ESF"); err != nil {
		log.Printf("titleScreen: %v", err)
		client.titleScr = &titleScreen{}
	} else {
		client.titleScr = loadTitleScreen(setupFile)
	}

	// Start title screen BGM
	if client.titleScr != nil && client.titleScr.loaded {
		client.playBGMFromISO("THEME.BGM")
	}

	// Loading screen (procedural gear texture)
	client.loadScr = initLoadingScreen()

	defer func() {
		if client.equipPreviewInit {
			rl.UnloadRenderTexture(client.equipPreviewTex)
		}
		if client.audioSys != nil {
			client.audioSys.Close()
		}
		rl.UnloadFont(client.font)
		if client.titleScr != nil {
			client.titleScr.cleanup()
		}
		if client.loadScr != nil {
			client.loadScr.cleanup()
		}
		if client.reticleOK {
			rl.UnloadTexture(client.reticleTex)
		}
		if client.viui != nil {
			client.viui.Cleanup()
		}
		if client.skyDome != nil {
			client.skyDome.Cleanup()
		}
		if client.modelMgr != nil {
			client.modelMgr.Cleanup()
		}
		if client.terrainRender != nil {
			client.terrainRender.Cleanup()
		}
		if client.floraRender != nil {
			client.floraRender.Cleanup()
		}
		rl.CloseWindow()
	}()

	// Load saved config
	client.serverIP = defaultServerIP
	if cfg := loadLoginConfig(); true {
		if cfg.Remember {
			client.loginUser = cfg.Username
			client.loginPass = cfg.Password
			client.loginRemember = true
		}
		if cfg.ServerIP != "" {
			client.serverIP = cfg.ServerIP
		}
	}

	// Check for updates before showing title/login
	client.runUpdateCheck()

	// If ISO was provided via the prompt (updater), reload assets.
	if client.isoPath != isoPath && client.isoPath != "" {
		client.loadAssets(client.assetsDir)
		client.animMgr.modelMgr = client.modelMgr
		if setupFile, err := client.openAssetESF("SETUP.ESF"); err == nil {
			client.titleScr = loadTitleScreen(setupFile)
		}
		if client.titleScr != nil && client.titleScr.loaded {
			client.playBGMFromISO("THEME.BGM")
		}
	}
	if rl.WindowShouldClose() {
		return
	}

	// Outer loop: login -> game -> back to login on disconnect/escape
	for !rl.WindowShouldClose() {
		// Login screen phase (skipped during GM teleport auto-reconnect)
		client.engine = nil
		client.loginWantsExit = false

		if client.gmReconnect {
			// GM teleport auto-reconnect: skip login screen, re-auth silently
			log.Printf("GM RECONNECT: auto-login as %s, char slot %d", client.loginUser, client.gmReconnectChar)
			client.attemptLogin()
			// Spin until engine is created or login fails (draw a status frame each tick)
			for !rl.WindowShouldClose() && client.engine == nil && !client.loginWantsExit {
				client.pollLoginResult()
				rl.BeginDrawing()
				client.loadScrAngle += 60.0 * rl.GetFrameTime()
				client.drawLoadingScreen(client.loadScrAngle)
				client.checkScreenshot()
			rl.EndDrawing()
				if client.loginError != "" {
					log.Printf("GM RECONNECT failed: %s", client.loginError)
					client.gmReconnect = false
					break
				}
			}
			if client.engine == nil {
				// Auth failed — fall through to normal login
				client.gmReconnect = false
			}
		}

		if !client.gmReconnect {
			// Title screen: press any key to continue (shown once per launch)
			if client.titleScr != nil && client.titleScr.loaded && !client.titleShown {
				client.titleShown = true
				for !rl.WindowShouldClose() {
					// Skip "any key" if clicking in mute button area (top-left 30x30)
					muteArea := rl.IsMouseButtonPressed(0) && rl.GetMouseX() < 32 && rl.GetMouseY() < 32
					if !muteArea && anyKeyPressed() {
						break
					}
					client.audioSys.UpdateBGM()
				client.audioSys.UpdateAmbient()
					rl.BeginDrawing()
					rl.ClearBackground(rl.Black)
					client.titleScr.drawTitleScene(client, true)
					client.checkScreenshot()
			rl.EndDrawing()
				}
			}

			// Login screen with landscape background
			for !rl.WindowShouldClose() && !client.loginWantsExit && client.engine == nil {
				client.pollLoginResult()
				client.handleLoginInput()
				client.audioSys.UpdateBGM()
				client.audioSys.UpdateAmbient()

				rl.BeginDrawing()
				rl.ClearBackground(rl.Black)
				if client.titleScr != nil && client.titleScr.loaded {
					client.titleScr.drawTitleScene(client, false)
				} else {
					rl.ClearBackground(rl.Color{R: 10, G: 10, B: 20, A: 255})
				}
				client.drawLoginScreen()
				client.checkScreenshot()
			rl.EndDrawing()
			}
		}

		if rl.WindowShouldClose() || client.loginWantsExit {
			break
		}

		// Clear currentBGM so char select can play race-specific music
		client.currentBGM = ""

		// Set up spell effect position resolver (resolves ObjectID -> world position)
		engine := client.engine
		engine.State.Weather = client.weather
		engine.SetPosResolver(func(objID uint32) (float32, float32, float32) {
			return client.resolveObjPos(engine.State, objID)
		})

		// Bone resolver — spell effects attach particles/sprites to specific bones.
		// PS2: GetNodeTransform at 0x0047A490 → CalcSlotWorldTransform.
		engine.SetBoneResolver(func(objID uint32, nodeID uint16) (float32, float32, float32, bool) {
			if client.modelMgr == nil {
				return 0, 0, 0, false
			}
			gs := engine.State
			if objID == gs.PlayerObjectID {
				modelID := int32(gs.Player.ModelID)
				return client.modelMgr.BoneWorldPos(modelID, objID, nodeID, gs.Player.X, gs.Player.Y, gs.Player.Z, gs.Player.Facing, gs.Player.Scale)
			}
			if ent, ok := gs.Entities[objID]; ok {
				return client.modelMgr.BoneWorldPos(int32(ent.ModelID), objID, nodeID, ent.X, ent.Y, ent.Z, ent.Facing, ent.Scale)
			}
			return 0, 0, 0, false
		})

		// Facing resolver — spell effect emitters rotate to match entity facing.
		// PS2 ProcessCreateEmitter applies actor node transform rotation to emitter direction.
		engine.SetFacingResolver(func(objID uint32) float32 {
			gs := engine.State
			if objID == gs.PlayerObjectID {
				return gs.Player.Facing
			}
			if ent, ok := gs.Entities[objID]; ok {
				return ent.Facing
			}
			return 0
		})

		// PlayAttack callback — spell effects trigger attack animations on entities.
		// PS2 NewPlayAttack at 0x00476370: Fields[0] = attack type (0-3).
		//   0 → SetAttackAction(slot=0)    — primary weapon, primary swing
		//   1 → SetAttackAction(slot=1)    — secondary weapon, primary swing
		//   2 → SetAttackAltAction(slot=0) — primary weapon, alt swing
		//   3 → SetAttackAltAction(slot=1) — secondary weapon, alt swing
		// PS2 also reads Fields[1]=targetActorID, Floats[0-2]=targetPos for PlayDirectedAttack.
		engine.SetPlayAttackCallback(func(actorObjID uint32, animPair int32, trailColor [3]float32, trailID int32) {
			// PS2 NewPlayAttack (0x00476370): Fields[0] selects attack pair.
			//   0 = SetAttackAction(slot=0)    — primary weapon, primary swing
			//   1 = SetAttackAction(slot=1)    — secondary weapon, primary swing
			//   2 = SetAttackAltAction(slot=0) — primary weapon, alt swing
			//   3 = SetAttackAltAction(slot=1) — secondary weapon, alt swing
			// PS2 SetAttackAction/SetAttackAltAction use the weapon's AttackType
			// jump table to pick the correct CPlayList slot. We index into the
			// verified 4-entry weaponAttackAnims[attackType] table:
			//   [0]=Pri(s=0), [1]=Pri(s=1), [2]=Alt(s=0), [3]=Alt(s=1)
			idx := int(animPair)
			if idx < 0 || idx > 3 {
				idx = 0
			}

			var animByte byte
			if actorObjID == engine.State.PlayerObjectID {
				atkType := client.animMgr.GetWeaponAttackType(engine.State)
				if anims, ok := weaponAttackAnims[atkType]; ok {
					animByte = anims[idx]
				} else {
					animByte = weaponAttackAnims[11][idx] // default dagger
				}
			} else {
				// NPC: use generic attack slots (PS2 uses NPC's own attackType,
				// but we don't track per-NPC weapon types)
				animByte = weaponAttackAnims[11][idx]
			}
			client.triggerSpellAnim(actorObjID, animByte)

			// Mark combat active — spell FX PlayAttack means we're in combat.
			if actorObjID == engine.State.PlayerObjectID {
				client.animMgr.PlayerCombatActivity()
			}
		})

		// SetAction callback — spell effects trigger cast/action animations.
		// PS2 VICSprite::SetAction at 0x00421770: Fields[0] is a CPlayList slot number
		// (e.g. 37=spell damage, 38=spell alt, 39=spell buff, 34=hit stun).
		engine.SetSetActionCallback(func(actorObjID uint32, actionType int32) {
			client.triggerSpellAnim(actorObjID, byte(actionType))
		})

		// IsActionComplete callback — PS2 Wait sub-type 0 checks actor animation state.
		// Returns true when the actor's play-once action animation has finished.
		engine.SetIsActionCompleteCallback(func(actorObjID uint32) bool {
			if client.animMgr == nil {
				return true // no animation system — complete immediately
			}
			if actorObjID == engine.State.PlayerObjectID {
				if client.animMgr.PlayerController() != nil {
					return client.animMgr.PlayerController().IsActionFinished()
				}
				return true
			}
			ctrl := client.animMgr.EntityController(actorObjID)
			if ctrl != nil {
				return ctrl.IsActionFinished()
			}
			return true
		})

		// Trigger callbacks for spell FX Wait(sub-type 3) HasTripped.
		engine.SetTriggerCallbacks(
			func(actorObjID uint32, triggerName string) (int, bool) {
				if client.modelMgr == nil {
					return -1, false
				}
				return client.modelMgr.FindSpellFXTrigger(actorObjID, triggerName)
			},
			func(actorObjID uint32, triggerIdx int) {
				if client.modelMgr != nil {
					client.modelMgr.ResetSpellFXTrigger(actorObjID, triggerIdx)
				}
			},
			func(actorObjID uint32, triggerIdx int) bool {
				if client.modelMgr == nil {
					return false
				}
				return client.modelMgr.HasSpellFXTripped(actorObjID, triggerIdx)
			},
			// IsCSprite: check if actor is a humanoid model (CSprite with TSlotList).
			// PS2 checks sprite->state == 5. Non-CSprite actors clear Wait type 3 immediately.
			func(actorObjID uint32) bool {
				if client.modelMgr == nil {
					return false
				}
				gs := engine.State
				if gs == nil {
					return false
				}
				var modelID uint32
				if actorObjID == gs.PlayerObjectID {
					modelID = gs.Player.ModelID
				} else if ent, ok := gs.Entities[actorObjID]; ok {
					modelID = ent.ModelID
				}
				return modelID != 0 && client.modelMgr.ModelIsHumanoid(int32(modelID))
			},
			// SetWeaponTrail: activate weapon swing trail on actor.
			func(actorObjID uint32, trailID int32, color [3]float32) {
				if client.modelMgr != nil {
					client.modelMgr.SetWeaponTrail(actorObjID, trailID, color)
				}
			},
		)

		// Game loop -- exits when window closes or client.engine is set to nil (back to login).
		// On exit, send disconnect opcode if still connected.
		inWorldResApplied := false
		charSaveLoaded := false
		for !rl.WindowShouldClose() && client.engine != nil {
			engine.Tick()

			// Sync screen/render dimensions each frame (handles resize, HiDPI).
			screenWidth = int32(rl.GetScreenWidth())
			screenHeight = int32(rl.GetScreenHeight())
			renderWidth = int32(rl.GetRenderWidth())
			renderHeight = int32(rl.GetRenderHeight())

			// Exclusive fullscreen: drop to windowed on focus loss, restore on regain.
			focused := rl.IsWindowFocused()
			if client.windowMode == 1 {
				if client.wasFocused && !focused {
					if client.audioSys != nil {
						client.audioSys.StopBGM()
					}
					client.bgmRestartDelay = 10
					rl.ToggleFullscreen()
				} else if !client.wasFocused && focused {
					if client.audioSys != nil {
						client.audioSys.StopBGM()
					}
					client.currentBGM = ""
					rl.SetWindowSize(int(screenWidth), int(screenHeight))
					rl.ToggleFullscreen()
				}
			}
			client.wasFocused = focused

			// Detect MemoryDump (coaching, death respawn) — trigger loading screen.
			// Skip the initial world entry (handled by inWorldResApplied below).
			if engine.State.MemoryDumpCount > client.lastMemDumpCount {
				if client.lastMemDumpCount > 0 {
					// Subsequent MemoryDump = coaching or death respawn
					client.loadScrFade = 4.0
					log.Printf("MemoryDump #%d: loading screen triggered", engine.State.MemoryDumpCount)
				}
				client.lastMemDumpCount = engine.State.MemoryDumpCount
			}

			// Detect world change (login to different continent) — reload terrain ESF.
			if engine.State.WorldChanged {
				engine.State.WorldChanged = false
				newWorld := engine.State.PendingWorld
				if newWorld != client.currentWorld {
					client.switchWorldTerrain(newWorld)
				}
			}

			// Apply saved settings on first frame in-world (login/charselect stay at 640x480)
			if engine.State.Phase == game.PhaseInWorld && !inWorldResApplied {
				inWorldResApplied = true
				client.loadScrFade = 4.0 // 4s loading overlay
				cfg := loadLoginConfig()

				// Display
				if cfg.ResWidth > 0 && cfg.ResHeight > 0 {
					screenWidth = cfg.ResWidth
					screenHeight = cfg.ResHeight
					rl.SetWindowSize(int(screenWidth), int(screenHeight))
				}
				if cfg.WindowMode > 0 {
					wm := cfg.WindowMode
					if wm > 1 { wm = 1 } // map old mode 2 to new fullscreen (1)
					client.applyWindowModeImmediate(wm)
					client.wasFocused = true
				}
				if cfg.VSync {
					client.vsyncOn = true
					client.applyVSync()
				}
				if cfg.FPSCap > 0 {
					client.fpsCap = cfg.FPSCap
					client.applyFPSCap()
				} else {
					client.fpsCap = 60
				}
				client.showFPS = cfg.ShowFPS

				// Camera
				if cfg.MaxZoom > 0 {
					client.camMaxDist = cfg.MaxZoom
				}
				if cfg.CamSensitivity > 0 {
					client.camSensitivity = cfg.CamSensitivity
				}
				if cfg.InvertPitch {
					client.invertPitch = true
				}
				if cfg.CamCollision {
					client.camCollision = true
				}

				// Audio
				if cfg.MasterVolume > 0 {
					client.masterVolume = cfg.MasterVolume
					rl.SetMasterVolume(cfg.MasterVolume)
				}
				if cfg.SFXVolume > 0 {
					client.sfxVolume = cfg.SFXVolume
					if client.audioSys != nil {
						client.audioSys.SetSFXVolume(cfg.SFXVolume)
					}
				}
				if cfg.BGMVolume > 0 {
					client.bgmVolume = cfg.BGMVolume
					if client.audioSys != nil {
						client.audioSys.SetBGMVolume(cfg.BGMVolume)
					}
				}
			}

			// GM teleport: after reconnect, once in-world, apply destination
			// and reset physics. The position was already overridden pre-Tick
			// so the world entry trigger carried the correct coords.
			if engine.State.Phase == game.PhaseInWorld && client.gmReconnectReady {
				client.gmReconnectReady = false
				engine.PosOverride = false // world entry already used correct pos
				p := &engine.State.Player
				log.Printf("GM TELEPORT: in-world, destination (%.1f,%.1f,%.1f)",
					client.gmReconnectX, client.gmReconnectY, client.gmReconnectZ)
				p.X = client.gmReconnectX
				p.Y = client.gmReconnectY
				p.Z = client.gmReconnectZ
				// Reset physics state for new location
				client.velocityY = 0
				client.onGround = false
				client.safeValid = false
				client.currentSpeed = 0
				client.flyVelX, client.flyVelY, client.flyVelZ = 0, 0, 0
				// Flush position to confirm destination with server
				engine.ForcePositionFlush()
			}

			// Normal login: restore camera from character save file.
			// Position override is only used by GM teleport (which sets
			// PosOverride before world entry so the 0x40 packet carries
			// the destination). Normal logins just restore the camera.
			if engine.State.Phase == game.PhaseInWorld && !charSaveLoaded && !client.gmReconnectReady {
				charSaveLoaded = true
				startPresence(engine.State.Player.Name)

				// Always apply appearance from char select list on world entry.
				// The char select 0x002C response has authoritative appearance data.
				// State channel 0x00 may have stale/default values for the player's
				// own appearance (server doesn't always update self-appearance immediately).
				{
					p := &engine.State.Player
					idx := engine.State.SelectedChar
					if idx >= 0 && idx < len(engine.State.Characters) {
						ch := &engine.State.Characters[idx]
						p.HairColor = ch.HairColor
						p.HairLength = ch.HairLength
						p.HairStyle = ch.HairStyle
						p.FaceOption = ch.FaceOption
						p.Robe = ch.Robe
						p.RobeTint = ch.RobeTint
					}
				}
				if save, ok := loadCharSave(engine.State.Player.Name); ok {
					client.camDist = save.CamDist
					client.camPitch = save.CamPitch
					client.camAngle = save.CamAngle
					log.Printf("CHARSAVE: restored camera for %s", engine.State.Player.Name)
				} else {
					// No saved camera — set PS2 race-specific defaults.
					p := &engine.State.Player
					rp := getRaceParams(p.Race, p.Sex)
					client.camDist = rp.CamDist * raceCamScale
					client.camPitch = rp.CamPitch
					log.Printf("RACECAM: race %d sex %d → dist=%.1f pitch=%.3f",
						p.Race, p.Sex, client.camDist, client.camPitch)
				}
			}

			// Handle input
			switch engine.State.Phase {
			case game.PhaseCharSelect:
				client.audioSys.UpdateBGM()
				client.audioSys.UpdateAmbient()
				if client.gmReconnect && len(engine.State.Characters) > 0 {
					// Auto-select character for GM teleport reconnect
					idx := client.gmReconnectChar
					if idx >= len(engine.State.Characters) {
						idx = 0
					}
					log.Printf("GM RECONNECT: auto-selecting character %d", idx)
					engine.State.SelectCharacter(idx)
					client.gmReconnect = false
					client.gmReconnectReady = true // apply coords once PhaseInWorld
					// Set position override on engine — applied inside Tick() AFTER
					// processIncoming overwrites Player from state channel, but BEFORE
					// sendWorldEntryTrigger builds the 0x40 packet.
					engine.PosOverride = true
					engine.PosOverrideX = client.gmReconnectX
					engine.PosOverrideY = client.gmReconnectY
					engine.PosOverrideZ = client.gmReconnectZ
				} else {
					client.handleCharSelectInput()
				}
			case game.PhaseInWorld:
				client.handleInWorldInput()
			}

			// Skip draw if disconnected mid-frame (e.g. Escape on char select)
			if client.engine == nil {
				rl.BeginDrawing()
				client.checkScreenshot()
			rl.EndDrawing()
				break
			}

			// Draw
			rl.BeginDrawing()
			rl.ClearBackground(rl.Color{R: 0x10, G: 0x10, B: 0x20, A: 0xFF})

			switch engine.State.Phase {
			case game.PhaseDisconnected:
				client.drawStatus()
			case game.PhaseConnecting, game.PhaseAuth, game.PhaseConnectingGame, game.PhaseEnteringWorld:
				// Loading screen with spinning gear
				client.loadScrAngle += 60.0 * rl.GetFrameTime() // 60 deg/sec
				client.drawLoadingScreen(client.loadScrAngle)
			case game.PhaseServerList:
				client.drawServerList()
			case game.PhaseCharSelect:
				client.drawCharacterSelect()
			case game.PhaseInWorld:
				// Always call drawWorld so terrain zone detection + loading runs
				client.drawWorld()
				if client.loadScrFade > 0 {
					client.loadScrAngle += 60.0 * rl.GetFrameTime()
					// Fade alpha: opaque for first 3s, fade out over last 1s
					alpha := float32(1.0)
					if client.loadScrFade < 1.0 {
						alpha = client.loadScrFade
					}
					client.drawLoadingScreenAlpha(client.loadScrAngle, alpha)
					terrainReady := client.terrainLoader != nil && client.terrainZoneIdx >= 0 && client.terrainLoader.GetZoneTerrain(client.terrainZoneIdx) != nil
					if terrainReady {
						// Terrain loaded — fade normally
						client.loadScrFade -= rl.GetFrameTime()
					} else {
						// No terrain (ocean/void) — slow countdown so player can move
						client.loadScrFade -= rl.GetFrameTime() * 0.5
					}
				}
			default:
				client.drawStatus()
			}

			// Display change confirmation overlay
			if client.displayConfirmPending {
				client.displayConfirmTimer -= rl.GetFrameTime()
				if client.displayConfirmTimer <= 0 {
					client.revertDisplayChange()
				} else {
					client.drawDisplayConfirm()
				}
			}

			if client.showFPS {
				client.drawFPSCounter()
			}
			rl.EndDrawing()
			// Screenshot after EndDrawing — framebuffer fully composited
			client.checkScreenshot()
		}

		// Revert to windowed 640x480 for login/charselect/title screens.
		// Saved settings are preserved in config and reapplied on next world entry.
		if client.windowMode != 0 {
			client.applyWindowModeImmediate(0)
		}
		client.displayConfirmPending = false
		client.charSelectIdx = -1 // reset so char select BGM re-triggers
		client.currentBGM = ""
		screenWidth = 640
		screenHeight = 480
		rl.SetWindowSize(640, 480)

		// Save character position before disconnect
		if client.engine != nil && client.engine.State.Phase == game.PhaseInWorld {
			client.saveChar()
		}

		// Clean disconnect if game loop exited while still connected (e.g. window close)
		if client.engine != nil && client.engine.State.Conn != nil {
			client.engine.SendDisconnect()
			client.engine.State.Conn.Close()
			client.engine = nil
		}
	}
}
