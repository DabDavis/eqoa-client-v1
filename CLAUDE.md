# EQOA Go Client (v1 — Production)

Raylib-based 3D client for the EQOA private server. Connects to Go game server or Sandstorm (eqoa.live), renders zone terrain and character models from ESF file data.

See [root CLAUDE.md](../CLAUDE.md) for project overview, research notes, ISO reverse engineering, and shared infrastructure.

## PS2 Client Porting (CRITICAL)

**This Go client is a 1:1 port of the PS2 EQOA client's VI engine.** When implementing or modifying any game system, always research the corresponding VI class/function in `claude-eqoa-iso/` first and match its behavior.

### How to Port

1. **Find the VI function** in `claude-eqoa-iso/decrypted_modules/SUPPORT_symbols.txt` or `CLIENT_symbols.txt`
2. **Read the RE docs** in `claude-eqoa-iso/EQOA_REVERSE_ENGINEERING.md`
3. **Check for decompiled code** in `claude-eqoa-iso/decrypted_modules/drdp_decompiled.c` and Ghidra project
4. **Match the PS2 pipeline** — same function decomposition, iteration counts, constants, data flow order

### VI → Go Port Map (implemented)

| PS2 VI Class/Function | Go Implementation | File |
|---|---|---|
| `VIWalkControl::Process` | `handleInWorldInput()` — camera-relative movement + facing | `cmd/client/input.go` |
| `VIWalkControl::SetTargetHeading` | `facing = atan2(moveX, moveZ)` — model +Z forward | `cmd/client/input.go` |
| `VIWalkControl::CalcFit` | `ZoneTerrain.CalcFit() → CalcFitResult` — iterative collide + Skitter | `internal/terrain/collision.go` |
| `VIWalkControl::CalcFloor` | `ZoneTerrain.SampleHeight()` — ground snap | `internal/terrain/collision.go` |
| `VICollRay::Init + Collide` | `castWallRay()` — Möller-Trumbore segment-triangle test | `internal/terrain/collision.go` |
| `VICollRay::Skitter` | Inline in `CalcFit` loop — `slide = rem - dot(rem,N)*N` | `internal/terrain/collision.go` |
| `VICollRay::PerturbMult` | `skin = 0.25` constant in `CalcFit` | `internal/terrain/collision.go` |
| `VIWalkCamera::Process` | Camera orbit in `handleInWorldInput()` | `cmd/client/input.go` |
| `VIParticleEmitter::Update` | Analytical particle physics (birth/death/position/size/color) | `internal/game/particles.go` |
| `VIParticleSprite::Raster` | Billboard quad rendering (camera-facing textured quads) | `internal/game/particles.go` |
| `VISpellEffectSystem::Play` | Spell effect playback with timed event sequences | `internal/game/spelldriver.go` |
| `VISpellEventDriver::Process` | 7-phase event state machine (17 event types) | `internal/game/spelldriver.go` |
| `ParseParticleDefinitionObj` | Parse 0xC020 particle attributes | `pkg/esf/spellfx.go` |
| `VICSprite::SetAnimation` | Priority-gated animation (dual-pair state machine) | `internal/models/animcontroller.go` |
| `VIHSprite::AddInterpolatedPlay` | 250ms cross-fade blend with per-pair rejection | `internal/models/manager.go` |
| `VICSprite::SetAttackAction` | Primary swing anims (11 weapon types) | `cmd/client/animation.go` |
| `VICSprite::SetAttackAltAction` | Alt swing anims (Primary/Alt alternation) | `cmd/client/animation.go` |
| `VIAtmosphere` | Weather particles (7 types), 30s crossfade, wind, splash | `internal/game/weather.go` |
| `VILightning` | Jagged bolt renderer (16 segments, 3 branches) | `internal/game/lightning.go` |
| `VIEffectVolume` | Environmental volumes (fog/dust/leaves/shafts/flock) | `internal/game/effectvolumes.go` |
| `VIZoneTree` | BSP tree for water/lava/slime/mud detection | `internal/terrain/water.go` |
| `VIUI` | 36-sheet UI textures, 9-slice frames, 252-entry TexID table | `internal/ui/viui.go` |

### VI Functions NOT Yet Ported

| PS2 VI Function | Purpose | Symbol Address (SUPPORT) |
|---|---|---|
| `VICollSphere::Init/Collide` | Swept sphere collision (player radius) | `0x005057A0` |
| `VIWalkControl::UpdatePhysics` | Gravity, vertical velocity, jumping | `0x005132F4` |
| `VIWalkControl::UpdateLocomotion` | Speed ramping (walk/run acceleration) | `0x00513372` |
| `VIWalkControl::IsUnderWater/IsInLava` | Liquid detection for swim mode | `0x005134C8+` |
| `VIWalkCamera::UpdateCameraPhysics` | Camera-wall collision | `0x00512F5A` |
| `VIWalkControl::CalcPitchRollToLandscape` | Terrain-following rotation | `0x00513612` |
| `VIMaterial::SetLayerTexture/BlendMode` | Multi-layer single-pass rendering | CLIENT symbols |

## Architecture

```
cmd/client/                          # Raylib 3D client (~23k lines)
  main.go                            # Window, Camera3D, game loop
  render.go                          # Entity/player rendering, animation dispatch
  animation.go                       # Combat animation mapping, weapon attack pools
  input.go                           # In-world keyboard/controller input, camera
  hud.go                             # HUD rendering, spell effects, entity lists
  colors.go                          # UI theme palette, drawPanel(), text helpers
  widgets.go                         # Immediate-mode UI (buttons, inputs, dropdowns, sliders)
  esfont.go                          # ESF bitmap font loader (EQOA.ESF glyph atlas)
  minimap.go                         # Real-time 512x512 GPU minimap (ortho capture)
  menu.go                            # F12 in-game menu (tabbed panel)
  login.go                           # Login screen, server config
  titlescreen.go                     # Animated title (fire/ship/smoke from SETUP.ESF)
  loadingscreen.go                   # Loading screen with procedural gear texture
  charselect.go                      # Character select (3D preview, delete, BGM)
  charcreate.go                      # Character creation wizard (5 steps, base stats)
  charsave.go                        # Per-character JSON state persistence
  isoprompt.go                       # ISO path entry with file browser + drag-and-drop
  transmog.go                        # Client-side cosmetic equipment override
  racecam.go                         # Per-race camera params (from CLIENT.AUT 0x00CDD8A0)
  gamepad.go                         # PS2 DualShock 2 button mapping
  entitysound.go                     # Animation-triggered positional audio
  screenshot.go                      # PrintScreen/F9 capture
  updater.go                         # Auto-updater (manifest, SHA-256, self-replace)
  gridrunner_stub.go                 # Dev-only GM grid runner stub
  pprof_dev.go                       # Dev-only pprof on :6060

cmd/headless/main.go                # Headless REPL client (Sandstorm testing)
cmd/crawler/                         # NPC/entity grid crawler (SQLite sightings)
cmd/statharvest/main.go             # Base stat harvester (all race/class combos)
cmd/presence/main.go                # HTTP heartbeat server (online player list)
cmd/dumpbgm/main.go                 # Zone BGM placement extractor

internal/game/
  engine.go                          # Core game loop, position updates, movement
  state.go                           # GameState, packet handling, state channels
  handlers.go                        # Entity lifecycle opcodes (0x00B3/B5/B8/FC/FD/FF)
  spelldriver.go                     # SpellEffectSystem/Driver/EventDriver (17 event types)
  particles.go                       # ParticleSystem/Emitter — billboard quad rendering
  spellfx.go                         # SPELLFX.ESF loader
  spellsprites.go                    # Spell sprite model loading/caching (GPU meshes)
  weather.go                         # VIAtmosphere: 7 weather types, transitions, splash
  lightning.go                       # VILightning: bolt renderer (16 segments, 3 branches)
  effectvolumes.go                   # VIEffectVolume: fog/dust/leaves/shafts/flock (512 max)

internal/terrain/
  terrain.go                         # ESF zone loading, ZoneTerrain, EffectVolumes, PointLights
  renderer.go                        # GPU mesh upload, multi-pass rendering, frustum culling
  collision.go                       # CalcFit (wall slide), castWallRay, SampleHeight
  flora.go                           # Radial flora (triangle scatter, 256-unit chunks)
  water.go                           # VIZoneTree BSP: water/lava/slime/mud detection, ProbeWater
  navmesh.go                         # Wrapper — delegates to server ESF navmesh builder
  obstaclenav.go                     # Obstacle avoidance waypoints, doorway probing
  gridnav.go                         # Local A* on 1-unit collision grid, LOS smoothing
  grid25d.go                         # Multi-level 2.5D pathfinding (ramps, bridges)
  sparsenav.go                       # Sparse node graph A* for open areas
  portalnav.go                       # Room/portal graph for indoor areas (BFS visible rooms)
  voxelsensor.go                     # 3D voxel grid for tactical step selection

internal/models/
  manager.go                         # Character model loading, animation, equipment rendering
  mesh.go                            # CSprite → triangle strip → rl.Mesh
  animcontroller.go                  # SpriteAnimController (dual-pair priority state machine)
  animstate.go                       # Animation playback time/blend tracking
  animprofile.go                     # 5 animation profiles (humanoid/NPC/creature variants)
  equipment.go                       # Equipment DictID tables (armor, hair, robe, helm, face)
  skeleton.go                        # Bone transforms, ComputeAnimPose* methods
  skinning.go                        # (placeholder — reserved for vertex skinning)

internal/network/
  connection.go                      # DRDP protocol client (reliable, state channels, XOR delta)
  packet.go                          # Packet construction, CRC, segment assembly
  auth.go                            # Auth/patch TCP flow

internal/audio/
  audio.go                           # Sound system: 3D positional, looping, distance attenuation
  ambient.go                         # AMBTRACK.ESF: XM→PCM, zone-based day/night selection
  bgm.go                             # BGM zones from TUNARIA.ESF StreamAudioSprite placements
  bgmstream.go                       # VAG ADPCM streaming decoder (block-interleaved stereo)
  vag.go                             # VAG decoder (EQOA reversed nibble variant)

internal/sky/sky.go                  # SKY.ESF dome, time-of-day tint, sun/moon/stars, weather
internal/ui/viui.go                  # PS2 UI renderer (36 sheets, 9-slice, 252-entry TexID table)
internal/ui/frames.go                # Frame presets (Panel/Ornate/Thin/Line)
internal/ui/table.go                 # 252-entry PS2 TexID→source rect table (from EE 0x004EDAF8)
internal/frustum/frustum.go          # Gribb-Hartmann frustum culling (AABB + sphere tests)
internal/gfx/gl.go                   # C bindings for GL depth/polygon ops not in rlgl
internal/gfx/videomode.go            # Display resolution enumeration
internal/mathutil/mathutil.go        # ClampF, FloatToUint8, Dist2D/3D, RectContains
internal/protocol/protocol.go        # DRDP opcodes (100+), message types, segment headers
```

## Quick Commands

```bash
cd go-eqoa-client-v1
go build -o client ./cmd/client/ && ./client
go build -o headless ./cmd/headless/ && ./headless

# With custom server/assets
./client <server-ip> <username> <assets-dir>

# Kill and restart
pkill -f './client'; sleep 1; ./client > /tmp/client.log 2>&1 &
```

## Deployment & Auto-Update

Client has a built-in auto-updater (`cmd/client/updater.go`) that checks `https://texturepacks.art/eqoa/manifest.json` on every launch before showing the title screen.

### Update Server (`texturepacks.art` / `10.8.0.1`)

Nginx serves from `/home/sdg/www/tpacks.art/eqoa/eqoa/`:
- `manifest.json` — version + platform URLs + SHA-256 hashes
- `eqoa-client` — Linux amd64 binary
- `eqoa-client.exe` — Windows amd64 binary

### Deploy Workflow

**Always use `deploy.sh`** for normal updates. It builds both platforms, generates the manifest, and pushes to the update server in one step.

Update-server Basic Auth creds are **injected at build time** (`-ldflags -X main.updateUser/updatePass`), not hardcoded. Set them via the git-ignored secrets file before deploying:

```bash
cd /home/sdg/claude-eqoa/go-eqoa-client-v1
cp scripts/.deploy-secrets.example scripts/.deploy-secrets   # first time only, then edit
source scripts/.deploy-secrets
./scripts/deploy.sh
```

The scripts fail fast if `EQOA_UPDATE_USER`/`EQOA_UPDATE_PASS` are unset. Note: the creds still end up embedded in the shipped binary (strings-extractable) — the client authenticates to a public download endpoint, so this Basic Auth is a soft scraping gate, not a real secret. Real integrity = SHA-256 check (+ future signing).

The client is a single binary — all assets are read from the EQOA Frontiers ISO at runtime. No fonts, asset folders, or archives needed.

### Build Version

Embedded at compile: `-ldflags "-X main.BuildVersion=20260306-abc1234"`. Generated automatically by `deploy.sh` from date + git hash. Default `"dev"` without ldflags.

Server IP: config `server_ip` > default `5.161.217.188` (Sandstorm).

### Key Files

| File | Purpose |
|------|---------|
| `cmd/client/updater.go` | Auto-updater (manifest fetch, download, SHA-256 verify, self-replace) |
| `scripts/deploy.sh` | Build + manifest + push to update server + download links |
| `scripts/deploy-dev.sh` | Dev build deploy variant |
| `scripts/build-dev.sh` | Dev build with pprof enabled |
| `cmd/client/login.go` | `loginConfig` with `ServerIP`, `ISOPath` fields |

## Terrain Rendering

### Data Flow
```
TUNARIA.ESF → Loader.loadZone() → ZoneTerrain (tiles + textures + effects)
  → Renderer.buildMeshes() → GPU meshes (one per TexID+Layer)
  → Renderer.Draw() → rl.DrawMesh() per group (frustum culled)
```

### Multi-Layer Materials
Materials have 1+ layers. Layer 0 = base terrain (pre-baked overview, identity UV). Layer 1+ = detail overlays (sand, grass, 40x UV tiling).

### Overlay Depth Rendering (DO NOT CHANGE)
Overlays use `glDepthFunc(GL_LEQUAL)` + `glDepthMask(GL_FALSE)` during Pass 2. This is the **only correct combination**:
- `GL_LEQUAL` — lets overlays draw at same depth as base (no edge flicker)
- `glDepthMask(GL_FALSE)` — overlays don't write depth (no geometry cracks at strip boundaries)

**Do NOT use `glPolygonOffset`** — it creates visible sub-pixel cracks between terrain strips. See `claude-notes/terrain-rendering.md` for full history of failed approaches.

### Radial Flora
Procedural flora uses a hybrid approach: scatter on **render overlay triangles** (correct visible Y positions) filtered by a **collision mesh FloraType grid** (PS2 CollBuffer type 3 per-vertex flora set index). This ensures flora only appears on natural surfaces (grass, dirt) and not on cobblestone, stone, etc. Also filtered by slope (normal Y > 0.7, ~45 degrees). Key file: `internal/terrain/flora.go`.

## Water & Environment Detection

`internal/terrain/water.go` implements the PS2 VIZoneTree BSP tree parsed from ESF zone data. Provides `ProbeWater()` (3-probe detection), `FindWaterSurfaceY()`, and `GetEnvFlags()` for water/lava/slime/mud detection. Used for swim/drown mechanics.

## Pathfinding / Navigation (5 strategies)

| Module | Strategy | Use Case |
|--------|----------|----------|
| `obstaclenav.go` | Building footprint + doorway probing | Waypoints around obstacles |
| `gridnav.go` | A* on 1-unit collision grid | Local wall/doorway detection |
| `grid25d.go` | Multi-level 2.5D grid | Ramps, stairs, bridges |
| `sparsenav.go` | Sparse node graph A* | Open areas, wall-adjacent |
| `portalnav.go` | Room/portal graph BFS + A* | Indoor zones (portal culling) |
| `voxelsensor.go` | 3D voxel tactical grid | Immediate next-step selection |

## Sky & Weather

**Sky** (`internal/sky/sky.go`): SKY.ESF dome with dual-layer materials (base + cloud overlay at 3x UV tiling). Procedural star field (~200 golden spiral points). Sun/moon billboards with celestial elevation visibility. PS2-derived 24-hour tint table with weather-driven overcast darkening.

**Weather** (`internal/game/weather.go`): 7 types (Clear, Rain, Snow, Sleet, Sandstorm, Lavastorm, Thunderstorm). 30-second crossfade transitions, wind effects, ground splash animations. Lightning flashes tied to thunderstorm.

**Lightning** (`internal/game/lightning.go`): 16-segment jagged bolts with random perpendicular jitter, up to 3 branching sub-bolts. Charge decays at 5.0 units/sec, jitter regenerates on 70-350ms intervals.

## Audio System

| File | Purpose |
|------|---------|
| `audio.go` | Sound manager: 3D positional, distance attenuation, stereo panning, BGM streaming |
| `ambient.go` | AMBTRACK.ESF: XM→PCM at 22050Hz, 12 day + 8 night tracks, zone selection |
| `bgm.go` | BGM zones from TUNARIA.ESF StreamAudioSprite placements |
| `bgmstream.go` | VAG ADPCM streaming decoder (block-interleaved stereo, auto-detect interleave) |
| `vag.go` | VAG decoder — EQOA variant with reversed nibble order (predictor=high, shift=low) |

Entity sounds triggered by animation keyframe crossings (`cmd/client/entitysound.go`), using per-model PLSlotSound maps.

## UI System

**VIUI** (`internal/ui/viui.go`): PS2-faithful renderer loading 36 texture sheets from UI.ESF via hardcoded DictID→page mapping. 252-entry TexID lookup table (`internal/ui/table.go`, extracted from EE 0x004EDAF8). 9-slice frame rendering with 4 presets (Panel/Ornate/Thin/Line in `frames.go`). PS2 alpha (0-128) → GL (0-255) conversion at load time.

**ESFont** (`cmd/client/esfont.go`): Bitmap font loaded from EQOA.ESF glyph metrics → texture atlas → Raylib GPU.

**Widgets** (`cmd/client/widgets.go`): Immediate-mode UI library — buttons, text input, tooltips, checkboxes, radio, dropdowns, sliders, lists. Supports "modern" (rounded/gradients) and "classic" (PS2 ESF sprite) themes.

## Equipment Texture System

Equipment textures live in **CHARCUST.CSF** (compressed ESF), NOT CHAR.ESF. Face textures in **CHARFACE.CSF**. ModelManager loads both at init.

### Lookup Flow
```
DrawEntity → GetArmorTextureDictID(armorSet, slot) → DictID
  → getEquipmentTexture(dictID) → search CHARCUST → CHARFACE → CHAR.ESF
  → FindObject(dictID) → *esf.Surface → GPU texture (cached)
```

### Data Tables (`equipment.go`)
| Table | Size | Source |
|-------|------|--------|
| `ArmorTextures[9][5]` | 8 sets × 5 slots | CHARCUST.CSF (DictIDs from VIRaster pool) |
| `HairTextures[8]` | 8 styles | CHARCUST.CSF |
| `HairNullTexture` | bald | CHARCUST.CSF |
| `RobeTextures[4][2]` | 4 styles × 2 parts | CHARCUST.CSF |
| `HelmModelDictIDs[9]` | 8 helm 3D models | CHARCUST.CSF SimpleSprites |
| `FaceTextures[9][8]` | 9 races × 4 faces × 2 sexes | CHARFACE.CSF + CHAR.ESF |

### Two-Pass Armor Rendering
Pass 1: skin texture + alpha cutoff. Pass 2: armor texture + alpha blend + depth mask OFF. Needed because armor textures have transparent areas showing skin (arms, neckline).

### TSlotList (`0x2930`)
Maps material indices to equipment slots. `count(int32) + count × { meshIndex, slotID, flag }`. Slots 0-4 = armor (chest/bracer/gloves/legs/boots), 5+ = hair/face/helm/robe.

### Helm Rendering
Helms are **separate 3D models** (NOT texture swaps). 8 SimpleSprite meshes in CHARCUST.CSF, rendered as standalone objects positioned at character head height. Hair hidden when helm equipped.

### Transmog System
Client-side cosmetic overrides (`cmd/client/transmog.go`). Tracks known weapon/shield DictIDs from visible entities. 9 armor set names, 14 tint color names. Applies overrides to equipment state before rendering.

## Animation System

### SpriteAnimController (`animcontroller.go`)
Dual-pair model matching PS2 `VICSprite::SetAnimation` (0x00422D78):
- **Pair 0**: Locomotion (idle, walk, run, swim)
- **Pair 1**: Action (attack, hit reaction, death, emotes)

**Priority gating**: Playing animations always interruptible. Only stopped/finished play-once animations protected by priority. Idle can never replace a stopped anim. Same-animID rejected.

**PlayOnce**: Attack anims hold last frame; locomotion loops. `applySkeletal` clamps play-once anims and notifies controller via `MarkActionFinished()`.

**Both `SetLocomotion` AND `SetAction` drive BOTH pairs** (PS2 dual-call). `SetLocomotion(walk)` sets pair0=pair1=walk so the upper body also animates the walk when no action is active; `SetAction(attack)` sets both to the attack (full-body swing). Because of this, `ClearAction` must only reclaim pair0 when it was set by an **action** anim (`!isLocomotionAnim`) — otherwise clearing the action wipes the locomotion pair too, and `Resolve()` returns idle: the character plays idle while moving ("walk shows idle / sliding"). This is per-frame (resolveCombat calls ClearAction every frame), so the bug is universal across humanoids, not race/class-specific. `SetLocomotion` runs before `resolveCombat` each frame, so a finished action is reclaimed by the priority system regardless. Regression-guarded by `internal/models/walk_repro_test.go`.

**Debugging animation "wrong-anim" bugs — rule out the model first**: dump the model's CPlayList slot→raw mapping straight from the ISO (`esf.OpenISOFile(iso,"CHAR.ESF").FindObject(modelID).(*esf.CSprite)`, then match `PlayList[].AnimDictID[]` against `Animations[].DictID`, mirroring `mesh.go`). If slot 1 (walk) maps to distinct valid raw indices, the model is fine and the bug is in the controller/resolve pipeline, not the model. The client also logs this at load: `Humanoid 0x%08X: N anims, M PL slots: [...]`.

### Animation Profiles (`animprofile.go`)
5 profiles for different model types:
- Humanoid (full weapon attack pools, dual wield/shield/2-hand)
- NPC humanoid
- 40-anim creatures
- 68-anim creatures
- 18-anim monsters

Each maps logical roles (SpecialIdle, BattleStance, Death, etc.) to ESF CPlayList slot numbers.

### Cross-Fade Blend
250ms interpolated transition (PS2 `AddInterpolatedPlay`). Per-pair rejection — locomotion blend doesn't block action pair.

### Negative Speed
CPlayList speed field negative = reverse playback (idle=-0.80, swim_back=-1.50). `math.Abs()` for rate, sign for direction.

### Combat Animations
- `animTimerB` — idle during combat with timer < 5s forces animID 17 (combat idle)
- Death forces animID 14, rejects non-death actions when dead
- Special idle: random 20-40s timer → CPlayList slot 15
- Per-weapon attack pools: `weaponAttackAnims` maps 12 VICSpriteAttackType values to PS2 CPlayList slot pairs

### ESF CPlayList Slot-Based Animation
All animation bytes (`animByte`) are ESF CPlayList slot numbers (sparse: 0,1,3,4,6,7,10,12-15,17,22-44,56-61). `PLSlotToRawPair` maps slot → [upper, lower] raw animation indices per model. This ensures correct animation across all model types (40, 64, 72, 80 anims) since raw indices differ per model but CPlayList slots are semantic.

### PS2 Combat Symbol Reference
| Symbol | Code Addr | Purpose |
|--------|-----------|---------|
| `VICSprite::SetAction` | 0x00421770 | Set upper body action |
| `VICSprite::SetLocomotion` | 0x00421AA0 | Set locomotion state |
| `VICSprite::SetAnimation` | 0x00422D78 | Priority check + AddPlay |
| `VICSprite::SetAttackAction` | 0x00421820 | Weapon attack anims (jump table) |
| `VICSprite::SetAnimPriorities` | 0x004259F8 | Per-bone priority table |
| `VIHSprite::AddPlay` | 0x00446970 | Start animation (no blend) |
| `VIHSprite::AddInterpolatedPlay` | 0x00446C58 | Start with cross-fade |

## Rendering Details

### Go Raylib MatrixRotateY Bug
Go raylib's `MatrixRotateY` has M2/M8 signs swapped vs C raylib. **Never use `rl.MatrixRotateY()`**. All render paths (`DrawEntity`, `DrawWeapon`, `drawHelm`) use manually constructed C-convention matrices:
```go
transform := rl.Matrix{
    M0: cosF, M4: 0, M8: sinF, M12: x,
    M1: 0, M5: 1, M9: 0, M13: y,
    M2: -sinF, M6: 0, M10: cosF, M14: z,
    M3: 0, M7: 0, M11: 0, M15: 1,
}
```

### Character Facing
`facing = atan2(moveX, moveZ)` — EQOA models face +Z in local space. Camera-relative movement:
```go
moveX = -cosA*inputFwd + sinA*inputRight
moveZ = -sinA*inputFwd - cosA*inputRight
```

### Weapon/Shield Bone Selection
`findBones()` in mesh.go: max-X = left hand, min-X = right hand (EQOA models face +Z, +X = character's left). `walkParents()` walks up from fingertip → palm bone.

### Body Mesh Shader
Body meshes use opaque shader (forces alpha=1.0) for slots 0-4. Prevents alpha cutoff from creating gaps at UV seam edges. Alpha cutoff retained for hair/face/equipment.

### Frustum Culling
`internal/frustum/frustum.go`: Gribb-Hartmann plane extraction from VP matrix. Tests AABBs and spheres against 6 clipping planes. Used by terrain renderer and entity draw.

## Client Screens & Flow

```
ISO Prompt → Title Screen → Login → Character Select → [Create/Delete] → Loading → In-World
                                                                                      ↓
                                                                              F12 Menu, Minimap,
                                                                              HUD, Gamepad input
```

- **ISO Prompt** (`isoprompt.go`): File browser, drag-and-drop, paste support. Validates >1GB .iso files.
- **Title Screen** (`titlescreen.go`): Animated fire/ship/smoke from SETUP.ESF. 45+ BGM tracks.
- **Character Select** (`charselect.go`): Up to 8 chars with 3D model preview, delete confirmation, per-race BGM.
- **Character Create** (`charcreate.go`): 5-step wizard (race/gender, class, appearance, stats, name). Hardcoded base stats from Sandstorm.
- **Loading Screen** (`loadingscreen.go`): Procedural 256x256 rotating gear, alpha fade transitions.
- **Race Camera** (`racecam.go`): Per-race/gender camera params from CLIENT.AUT 0x00CDD8A0.

## Character Persistence

`cmd/client/charsave.go`: Per-character JSON state files mirror PS2 memory card saves. Stores position/facing/zone, identity, appearance, equipment. Used to preserve 3D model in character select.

## Gamepad Support

`cmd/client/gamepad.go`: PS2 DualShock 2 mapping → Raylib gamepad. Left stick = movement, right stick = camera orbit/zoom/swim, D-pad = targeting, cross/circle/square/triangle = actions. Auto-shows spell bar on first gamepad frame.

## Runtime Robustness Invariants (do NOT regress — 2026-07-21)

Verified zero data races under `-race` vs Sandstorm. Future edits must preserve:

- **Packet handling is panic-guarded**: `engine.go dispatchIncoming` wraps every message in `recover()`. Handlers index into variable-length wire data — a short/malformed packet must not crash the client. Also bounds-check before indexing (e.g. HP reads in `state.go` guard `len(data) >= 28`).
- **Startup I/O runs off-thread, results applied on the main goroutine**: `runStartupIO` (blocking auth/patch/connect) touches ONLY its args, never GameState; `applyStartupResult` (called from `Tick`) writes `Phase`/`Conn`/`StatusMsg`/`AccountID`. Never write those fields from the startup goroutine — that races the game loop. Same pattern for login: goroutine → `loginResultCh` → `pollLoginResult` on the main goroutine.
- **World switch closes the old terrain loader**: `switchWorldTerrain` calls `terrain.Loader.Close()`, which stops the load-worker goroutine THEN closes the world ESF file handle + overlays. Skipping it leaks a goroutine + OS file handle per continent switch.
- **Zone eviction unloads GPU textures**: `renderer.go resetZoneSlot` must `rl.UnloadTexture` the slot's `texCache` (not just meshes) or VRAM grows monotonically while streaming.
- **Headless tool serializes GameState with `gameMu`** (`cmd/headless/main.go`): tick loop + all command handlers hold it; hold it around any GameState access but NEVER across a `time.Sleep` (blocking commands release it). The visual client doesn't need this — its Tick + reads are all on the render goroutine.
- **Update creds are build-injected, never hardcoded**: `-ldflags -X main.updateUser/updatePass` from git-ignored `scripts/.deploy-secrets` (see Deploy Workflow).

## DRDP Protocol Client (`internal/network/`)

### Connection Lifecycle
Auth → patch → server list → world entry. Handshake completes on GuaranteedAck (fast) or 5-packet count (fallback).

### State Channels
ACK all valid channels (0x00-0xF7). Dirty count 3 for UDP loss resilience. Cap 40 ACKs per packet. XOR delta with 32-entry base history per channel.

### Reliability
- Per-message retransmit with RTO (16-sample RTT buffer, 60s kill)
- Arrival history bitmap (forward/OOO/duplicate, gap > 64 reset)
- Fragment reassembly with 30s orphan timeout
- 60-second server silence detection
- `ConnectionMetrics`: PacketsRecv, PacketsDropped, ArrivalGaps, Retransmits, FragmentsExpired

### Position Updates (0x40 — 41 bytes)
```
[0]     World ID
[1-9]   Position: X,Y,Z as uint24 big-endian
[10-15] Velocity: uint16 (0x8000 = idle)
[16-21] Unknown uint16×3
[22]    Facing (0-255)
[23]    Turning (0x80 = center)
[24-29] Various center values
[30-34] Flags
[35]    Animation: 0=idle, 1=walk, 2=run
[36]    Skip byte
[37-40] Target ObjectID (0xFFFFFFFF = no target)
```

**Encoding**: `uint24((X+4000)/36000 * 0xFFFFFF)` for X/Z, `uint24((Y+1000)/2000 * 0xFFFFFF)` for Y.

**World entry**: All 4 reliable opcodes + first 0x40 in ONE packet. Then idle heartbeat every 3 ticks (~3.3/s).

### Sandstorm Entity State (channels 1-28, 162 bytes)
Byte 0 = update counter, bytes 1-4 = ObjectID (0 = removed). NameColor byte 142: 0=NPC, 2=Player.

## Combat System (verified on Sandstorm)

- **Attack (0x000F)**: Auto-attack at 4s intervals
- **UseSkill (0x0032)**: Byte 4 = WhereOnHotBar (0-9)
- **ForcePositionFlush**: Must call after SendTarget before combat opcodes (server reads target from 0x40 bytes 37-40)
- **DamageIndication (0x00DB)**: Zigzag-encoded. Negative=damage, positive=heal
- **CastingCombat (0x00B1)**: 17 bytes. target(4) + source(4) + unk(1) + timer_ms(2) + pad(2) + animHash(4)

## CLI Tools

| Tool | Purpose |
|------|---------|
| `cmd/crawler/` | NPC/entity grid crawler — crawls Tunaria in 2000×2000 cells, stores sightings in SQLite |
| `cmd/statharvest/` | Base stat harvester — iterates all race/class/humType combos from Sandstorm → SQLite |
| `cmd/presence/` | HTTP heartbeat server — tracks online players (90s prune, JSON response) |
| `cmd/dumpbgm/` | Zone BGM placement extractor — bounding boxes per audio file path |

## PS2 RE References

| Resource | Location |
|---|---|
| SUPPORT symbols (4,227) | `claude-eqoa-iso/decrypted_modules/SUPPORT_symbols.txt` |
| CLIENT symbols (3,776) | `claude-eqoa-iso/decrypted_modules/CLIENT_symbols.txt` |
| CLIENT strings (4,621) | `claude-eqoa-iso/decrypted_modules/CLIENT_strings.txt` |
| Master RE doc | `claude-eqoa-iso/EQOA_REVERSE_ENGINEERING.md` |
| DRDP decompilation | `claude-eqoa-iso/decrypted_modules/drdp_decompiled.c` |

## Dependencies

- `github.com/gen2brain/raylib-go/raylib` — 3D rendering (local replace → `../raylib-go/raylib`)
- `github.com/DabDavis/eqoa-esf-tools` — ESF file parsing (local replace → `../go-eqoa-esf-tools-v1`)
- `github.com/mattn/go-sqlite3` v1.14.34 — SQLite for crawler/statharvest tools

## TODO

- Helm scale adjustment (proportional to character height)
- Ogre face textures (race 9 not in FaceTextures table)
- Tint color application (VICSpriteCust::$Tints — 14 RGBA colors)
- Overlay back-face lighting fix
- `VIWalkControl::UpdatePhysics` — gravity, jumping
- `VICollSphere` — swept sphere collision
- Water/swim integration (VIZoneTree BSP parsed but not driving swim mode)
