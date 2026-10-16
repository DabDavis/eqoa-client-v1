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
| `VIWalkControl::Move` (sphere half) | `ZoneTerrain.CalcFit(…, grounded, inLiquid)` — sweep, two skitters, push-out only when wedged in the air | `internal/terrain/collision.go` |
| `VICollSphere` + `VICollide::Collide(sphere, tri/edge/vertex)` + `VICollRay::Skitter` | `collSphere` — one-sided faces, t=0 for an embedded start, rule for rule with `go-eqoa-vi/vicoll` in float32 | `internal/terrain/sweep.go` |
| `VIWalkControl::CalcFit` + `Move`'s floor half | `ZoneTerrain.Fit()` + `walkFloor` — floor and head probes, step up < 1.2, down < 0.6, refuse < 1.8 headroom; ceilings are not ground | `internal/terrain/collision.go`, `cmd/client/input.go` |
| `VIWalkControl::UpdatePhysics` (air) | airborne integration, damping 0.8^dt, no air control, no terminal velocity | `cmd/client/input.go` |
| `VIClient::SetSpeed` (0x00F8) | run = server value, walk = flat 3.0 | `cmd/client/input.go` |
| `VIPad::ScaleStick` | `scaleStick` — dead zone 88..167 of 255 | `cmd/client/gamepad.go` |
| `VINameSprite::Raster` / `RasterDeferred` | name plates: node 3 + 0.6, 22-unit cut, 24 max, near-first, overlaps dropped | `cmd/client/render.go` |
| `VIRadialFloraSystem` | 3×3 grid of 40-unit swatches, real FloraSprite meshes | `internal/terrain/flora.go` |
| `VIWorld::StreamLeaf` | zones load within 900, unload past 950 (3D) | `cmd/client/render.go` |
| `VIWalkControl::Move` (env half) | `ZoneTerrain.ProbeLiquid()` — the liquid bits at feet+1.2 ±0.1 and mud at the feet | `internal/terrain/water.go` |
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
| `VIAtmosphere` | WXTYPE 0 clear, 1-3 rain, 4-6 snow; 30 s fade (0xC5) or instant (0xC7); 8 s lightning; splashes | `internal/game/weather.go` |
| `VIWorld::SetProfiles` + `CalcProfileBlend` | Terrain profiles → fog colour/near/far, overcast weight, ambient track | `internal/terrain/profiles.go` |
| `VISpellEventDriver::ProcessProjectileEvent` | Projectiles and bolts ride the TARGET driver; 4 flight modes; `VIParticleSystem::DeltaTime` | `internal/game/spelldriver.go` |
| `VILightning` | Three harmonics, branches off points 3/6/9, three-frame afterimage | `internal/game/lightning.go` |
| `VIEffectVolume` | Environmental volumes (fog/dust/leaves/shafts/flock); `Render`'s distance fade and hour window | `internal/game/effectvolumes.go` |
| `VIZoneTree` | BSP tree for water/lava/slime/mud detection | `internal/terrain/water.go` |
| `VIUI` | 36-sheet UI textures, 252-entry TexID table; `BlitFrame` with per-piece flip flags, placed by corner sizes | `internal/ui/viui.go` |
| `VIWorld::RenderByPortals` | Portal culling only from an INDOOR room (zone-tree leaf room without bit 0) | `cmd/client/render.go`, `internal/terrain/portalnav.go` |

### VI Functions NOT Yet Ported

Code addresses (the old table listed name-string addresses):

| PS2 VI Function | Purpose | Code Address (SUPPORT) |
|---|---|---|
| `VIWalkControl::UpdatePhysics` | Ground half: force/velocity damping and the bang-bang turn (the air half is ported) | `0x00486198` |
| `VIWalkControl::UpdateLocomotion` | Locomotion state from measured displacement (only the forward run/walk thresholds are ported) | `0x00487178` |
| `VIWalkCamera::UpdateCameraPhysics` | Drag camera, `MoveCamera` + `FindOrbitAngle` wall swing | `0x00482758` |
| `VIWalkControl::CalcPitchRollToLandscape` | Terrain-following rotation | `0x00485FE8` |
| `VIRStrip4` (VU1) | Character lighting — not decompiled; characters are drawn unlit | — |

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
  playersearch.go                    # Sandstorm's Search for Player window (S2, 0x1500-0x1504)
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
  report.go                          # F3 Report a Problem: frame + state dump → cmd/reportd
  crash.go                           # Crash reports: recovered panics + the runtime's crash output
  logring.go                         # Log ring (200 lines) + client.log / client.prev.log
  crashtest_dev.go                   # Dev-only EQOA_CRASH_TEST=main|goroutine
  updater.go                         # Auto-updater (manifest, SHA-256, self-replace)
  gridrunner_stub.go                 # Dev-only GM grid runner stub
  pprof_dev.go                       # Dev-only pprof on :6060

cmd/headless/main.go                # Headless REPL client (Sandstorm testing)
cmd/crawler/                         # NPC/entity grid crawler (SQLite sightings)
cmd/statharvest/main.go             # Base stat harvester (all race/class combos)
cmd/presence/main.go                # HTTP heartbeat server (online player list)
cmd/reportd/                         # F3 problem reports, crash reports, public board (runs on the VPS)
cmd/dumpbgm/main.go                 # Zone BGM placement extractor

internal/game/
  engine.go                          # Core game loop, position updates, movement
  state.go                           # GameState, packet handling, state channels
  handlers.go                        # Entity lifecycle opcodes (0x00B3/B5/B8/FC/FD/FF)
  spelldriver.go                     # SpellEffectSystem/Driver/EventDriver (17 event types)
  particles.go                       # ParticleSystem/Emitter — billboard quad rendering
  spellfx.go                         # SPELLFX.ESF loader
  spellsprites.go                    # Spell sprite model loading/caching (GPU meshes)
  weather.go                         # VIAtmosphere: WXTYPE 0-6, transitions, splash, lightning cycle
  lightning.go                       # VILightning: harmonics, branches, afterimage
  effectvolumes.go                   # VIEffectVolume: fog/dust/leaves/shafts/flock, actor-local
  auction.go                         # VIBankControl's search: AuctionFilter, 0x1205 pages
  words.go / zones.go                # WORDS.TXT string table; world names + CLIENT's zone grids

internal/terrain/
  terrain.go                         # ESF zone loading, ZoneTerrain, EffectVolumes, PointLights
  renderer.go                        # GPU mesh upload, multi-pass rendering, frustum culling
  collision.go                       # CalcFit (wall slide), castWallRay, SampleHeight
  flora.go                           # VIRadialFloraSystem: 40-unit swatches around the camera
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
  ambient.go, ambientmix.go          # AMBTRACK.ESF: XM→PCM, (day, night) pairs, SetSoundTrack mixing
  bgm.go                             # Music regions from TUNARIA.ESF StreamAudioSprite placements
  vag.go                             # VAG decoder (standard layout)

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

## Deployment & Auto-Update (GitHub releases)

**Repo: [github.com/DabDavis/eqoa-client-v1](https://github.com/DabDavis/eqoa-client-v1) — PUBLIC.**
Releases are built by CI (`.github/workflows/release.yml`), the same scheme as the
Perfect Dark mod: there is no deploy script to run.

| You do | CI publishes | Clients that follow it |
|---|---|---|
| push to `master` | the rolling **dev** prerelease, tag `client-dev` (moved every push), version `dev-<sha>` | builds downloaded from the dev release |
| push a `v*` tag (`git tag v2026.10.15 && git push origin v2026.10.15`) | a **stable** release, never rewritten, version = the tag | builds downloaded from a stable release |

Each release carries `eqoa-client`, `eqoa-client.exe` and `manifest.json` (version,
channel, commit, per-platform URL + SHA-256, changelog: the last 5 commits for dev,
everything since the previous `v*` tag for stable). A push that touches only `*.md`
builds nothing. `go test ./...` gates every release (ISO tests skip in CI).

**The updater** (`cmd/client/updater.go`) follows the channel compiled into the binary:
`-X main.UpdateChannel=stable` reads `releases/latest/download/manifest.json` (GitHub's
redirect to the newest non-prerelease), `dev` reads `releases/download/client-dev/manifest.json`.
It updates when the manifest's `version` differs from `BuildVersion`, verifying SHA-256.
**A build without `UpdateChannel` (any local `go build`) never updates itself** and shows
"local"; GM builds (`-tags dev`) skip the check too. A tester switches channel by
downloading a binary from the other release once.

### The two dependencies are pinned in `scripts/deps.env`

`go.mod`'s replace directives point at `../go-eqoa-esf-tools-v1` (branch `client-v1` of
the public `DabDavis/eqoa-esf-tools`) and `../raylib-go` (branch `eqoa` of the public fork
`DabDavis/raylib-go`, three commits over upstream adding `GetVideoModes`). CI checks each
out beside the client at the SHA in `scripts/deps.env`. **After committing in either one:
push it, run `scripts/pin-deps.sh`, and commit `deps.env`** — otherwise CI keeps building
the old pin. The script refuses a dirty or unpushed sibling.

### Moving the pre-GitHub testers over

Builds from before the switch (version like `20260802-d919518`) poll
`texturepacks.art/eqoa/manifest.json` with Basic Auth and nothing else. After the first
stable release, run once (needs the VPN and `scripts/.deploy-secrets`):

```bash
source scripts/.deploy-secrets && scripts/bridge-legacy.sh   # latest stable, or pass a v-tag
```

It re-hosts that release on texturepacks.art byte for byte, so each legacy client updates
once into a build that follows GitHub. It refuses a dev build.

### What a release build does NOT carry

No credentials: a release is a public download. So **presence** (the "X is online"
heartbeat to `texturepacks.art/eqoa/presence`, Basic Auth) is OFF in release builds; it
runs only in a build given `-X main.presenceUser=… -X main.presencePass=…`. The world map
image on texturepacks.art is public and still downloads. The old update password and the
dev account password were scrubbed from the whole history when the repo went public
(pre-scrub backup: `../backups/go-eqoa-client-v1-prescrub-20261015.bundle`).

The client is a single binary — all assets are read from the EQOA Frontiers ISO at runtime.

Server IP: config `server_ip` > default `5.161.217.188` (Sandstorm).

### Key Files

| File | Purpose |
|------|---------|
| `.github/workflows/release.yml` | Test, build both platforms, publish the dev or stable release |
| `scripts/ci-build.sh` | Linux (zig cc, glibc 2.31) + Windows (mingw) builds; runs locally too |
| `scripts/ci-manifest.sh` | `manifest.json` + release notes |
| `scripts/deps.env` / `scripts/pin-deps.sh` | The esf-tools and raylib-go commits CI builds against |
| `scripts/bridge-legacy.sh` | One-time move of pre-GitHub testers onto GitHub releases |
| `scripts/build-dev.sh` | Local GM build (`-tags dev`: F11 GM menu, pprof) — never released |
| `cmd/client/updater.go` | Auto-updater (channel, manifest fetch, download, SHA-256 verify, self-replace) |
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
A port of `VIRadialFloraSystem` (`internal/terrain/flora.go`): a 3×3 grid of 40-unit swatches on the world grid follows the camera; each is grown from the ROOM collision triangles under it (actors rejected, ≤ 30° from up, the newest strip vertex's flora byte), (int)(area × Σweight) plants per triangle, newlib `rand` seeded per sub-cell, 100-triangle and 500-plant caps. Plants are the FloraSprite's own mesh (`esf.LoadFloraMesh`, type 0x1230), drawn within 40 units, ≤ 700, far to near, alpha 1 to 10 units then fading to 0 at 40, lit by the terrain shader. The dist record is (flags, model, weight, base scale, scale variance) — "SetIndex" was a flags word.

## Water & Environment Detection

`internal/terrain/water.go` implements the PS2 VIZoneTree BSP tree parsed from ESF zone data. `ProbeLiquid()` returns VIWalkControl::Move's liquid bits for water/lava/slime/mud. "In" is probed at the sphere centre (feet + 1.2), "at surface" at the centre ±0.1, and mud at the feet. `WaterStateOf()` reduces them to the swim states, so water is only swum in once it is about 1.1 deep. Only mud slows a run (`VIWalkControl::Run`). A submerged body is damped by 0.02^dt. The swim clamp keeps the centre at the surface.

**Rooms** (`esf.ZoneRoom` = sprite, palette, flags): flags bit 0 is OUTDOOR (81,172 of 81,773 Tunaria rooms). Portal culling runs only when the camera's zone-tree leaf room is indoor.

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

**Sky** (`internal/sky/sky.go`): the "sky" dome only, unrotated ("EQOA_Skydome_360" is the Plane of Sky's), its layers from the ESF material: layer 0 scrolling at 0.009/s, layer 1 ×3 at 0.05/s, blend 3. Every layer is the sky colour (`SetupTimeOfDaySky`, the renderer's `SkyColor`) × texture; `ProcessTransitions` rebuilds the dome under weather (DarkClouds). Sun, Corona and Moon are found by NAME, drawn 8/24/4 at 35 units, additive, hidden under overcast. 128 stars from `sStarNoise` with twinkle, plus a shooting star. 14 additive lightning patches during a flash. `render.go` feeds it `WeatherSystem.SkyAtmosphere()` each frame.

**Game clock**: one game day is 60 real minutes, `h = mod(ts2 + elapsed, 3.6e6)/3.6e6 × 24` (CLIENT `0x00BC7B70`).

**Weather** (`internal/game/weather.go`): a VIAtmosphere port. WXTYPE 0 clear, 1–3 rain and 4–6 snow at intensities {0, .15, .5, 1, .15, .5, 1}. Opcode **0xC5 fades over 30 s, 0xC7 is instant** (the server's `ps2opcodes.go` comments were swapped too). Particles ray-test the ground, so rain stops at a roof; there is no indoor switch. The outdoors factor is the baked sun gate 15 below the camera.

**Lightning** (`internal/game/lightning.go`): `VILightning` — three noise harmonics, branches off bolt-0 points 3/6/9 (the event's `m_p10`, at most 3), the 70–350 ms strike re-roll, a three-frame afterimage, four ribbons. A spell bolt is held at charge 1 for its event's duration, then decays at 5/s.

**Effect volumes** (`internal/game/effectvolumes.go`): rewritten from `vieffectvolume.cpp`, in volume-local space with the actor's rotation and scale. Fog, dust and leaves alpha-blend, shafts add; flock boids (int)(density·8)+1. Leaves and dust take the scene light (`SetLighting`).

**Terrain lighting** (`internal/terrain/renderer.go`): VU1 VIRStrip2/3. lit = min(1, vtx.rgb + ambient(+emissive) + sun·max(0, −N·D)·vtx.a + lantern). Vertex alpha is a SUN GATE, never opacity. Overlay blend 4 is a multiply (`glBlendFunc(ZERO, SRC_ALPHA)`). Only the player's lantern lights terrain. Fog and the far plane come from the terrain profiles.

## Audio System

| File | Purpose |
|------|---------|
| `audio.go` | Sound manager: 3D positional, distance attenuation, BGM streaming; pan is `CalcVolumeAndPan`'s, against the camera (−1 left … +1 right) |
| `ambient.go` / `ambientmix.go` | AMBTRACK.ESF XM→PCM; 11 (day, night) pairs by terrain profile (`SetTracks`); `SetSoundTrack`'s 15 s crossfade + 15 s hold-off, × (1 − rain)³ × outdoors, muted in liquid |
| `bgm.go` | Music regions: the camera taken into each StreamAudioSprite actor's space; priority, then nearest centre |
| `vag.go` | VAG decoder — the standard nibble layout (`AdpcmToPcm`); loops restart at block 1 |

Rain and thunder are SKY.CSF's bare 0xB000 Adpcm objects (`LoadAdpcmSounds`): rain two looping voices at pan ±1, thunder two one-shots. BGM fades out at 0.05 a frame before a switch.

Entity sounds triggered by animation keyframe crossings (`cmd/client/entitysound.go`), using per-model PLSlotSound maps.

## UI System

**VIUI** (`internal/ui/viui.go`): PS2-faithful renderer loading 36 texture sheets from UI.ESF via hardcoded DictID→page mapping. 252-entry TexID lookup table (`internal/ui/table.go`, extracted from EE 0x004EDAF8). 9-slice frames in `frames.go`: Thin = `VIUIWindow::Draw`'s fp[0] (corner 36 at all four, mirrored on the right), Panel = fp[1] (left 47, right 52, fill 35), Line = fp[3]. Each piece carries flip flags. **Texture alpha is uploaded unchanged**: the palettes already hold 0..255, and the old ×2 made everything twice as opaque.

**ESFont** (`cmd/client/esfont.go`): Bitmap font loaded from EQOA.ESF glyph metrics → texture atlas → Raylib GPU.

**Widgets** (`cmd/client/widgets.go`): Immediate-mode UI library — buttons, text input, tooltips, checkboxes, radio, dropdowns, sliders, lists. Supports "modern" (rounded/gradients) and "classic" (PS2 ESF sprite) themes.

## Equipment Texture System

Equipment textures live in **CHARCUST.CSF** (compressed ESF), NOT CHAR.ESF. Face textures in **CHARFACE.CSF**. ModelManager loads both at init.

### Lookup Flow
```
DrawEntity → resolveSlotTexture(equip, slot) → (DictID, texSource)
  → getEquipmentTexture(dictID, src)
      texSrcAuto    → search CHARCUST → CHARFACE → CHAR.ESF
      texSrcCharESF → CHAR.ESF only
  → FindObject(dictID) → *esf.Surface → GPU texture (cached)
```

**A DictID is not unique across files.** CHARCUST.CSF and CHAR.ESF both store
surfaces under the five plate DictIDs with *different* art. `texCache` is
therefore keyed by `texKey{dictID, src}`, not by DictID alone — keying by DictID
let `getTexture` and `getEquipmentTexture` shadow each other. See
`claude-notes/charesf-shadowed-armor.md`.

### Data Tables (`equipment.go`)
| Table | Size | Source |
|-------|------|--------|
| `ArmorTextures[9][5]` | 8 sets × 5 slots | CHARCUST.CSF (DictIDs from VIRaster pool) |
| `HairTextures[8]` | 8 styles | CHARCUST.CSF |
| `HairNullTexture` | bald | CHARCUST.CSF |
| `RobeTextures[6][2]` | 4 retail robes + Sandstorm's 4/5 × 2 parts | CHARCUST.CSF; 4/5 from `ss-art.csf` in the save folder |
| `Tints[30]` | 15 retail + 15 Sandstorm | `$Tints`; 15–29 read out of Sandstorm's S2 (`0x000FCD00`) |
| `HelmModelDictIDs[9]` | 8 helm 3D models | CHARCUST.CSF SimpleSprites |
| `FaceTextures[9][8]` | 9 races × 4 faces × 2 sexes | CHARFACE.CSF + CHAR.ESF |

### Two-Pass Armor Rendering
Pass 1: skin texture + alpha cutoff. Pass 2: armor texture, tinted, depth mask OFF, with the SAME alpha cutoff. On every CHAR.ESF body the armor layer is blend 0, which the PS2 alpha-tests at 128 and draws opaque. Needed because armor textures have transparent areas showing skin (arms, neckline).

### TSlotList (`0x2930`)
Maps material indices to equipment slots. `count(int32) + count × { meshIndex, slotID, flag }`. Slots 0-4 = armor (chest/bracer/gloves/legs/boots), 5+ = hair/face/helm/robe.

### Helm Rendering
Helms are **separate 3D models** (NOT texture swaps). 8 SimpleSprite meshes in CHARCUST.CSF, rendered as standalone objects positioned at character head height. Hair hidden when helm equipped.

### Transmog System
Client-side cosmetic armor override (`cmd/client/transmog.go`), applied to the
local player's `EquipmentState` in `render.go` before drawing. Purely local —
nothing is sent to the server, and other players see the real equipment.

UI: **F12 → Character → Appearance**. Picks any of the 11 armor sets, optionally
forces a single tint, and hides an equipped robe (robes cover chest and legs, so
without this the override looks like a no-op).

**Set 9 `ArmorSetOrnatePlate`** is client-side only: it reuses the plate DictIDs
but forces `texSrcCharESF`, surfacing the ornate gold-and-steel plate art that
CHARCUST.CSF otherwise shadows. Retail can't display it.

**Set 10 `ArmorSetTemplarPlate`** is also client-side only — silver plate with a
blue tabard and gold shoulder medallions, adapted from CHAR.ESF model
`0xD1300BD3`. Its DictIDs are unique to CHAR.ESF (nothing shadows them), and
**four slots share one DictID**: the donor packs all its limb art into a single
128×128 atlas laid out across the legs/boots/bracer/gloves UV tiles, the same
tiles the player body samples. Chest has its own 128×128.

The donor carries **no `CSpriteTSlotList`**, so it is invisible to
`npctex -compat`/`-matrix` and never appeared in the 63-pair NPC survey. Slot
assignment came from `npctex -fit`, which matches an untagged donor's materials
to the player body's per-slot UV footprints — see
`claude-notes/npc-armor-texture-survey.md`. An armor set is only the five
`$Armor` text slots, so the donor's face and hair are deliberately not carried
and the wearer keeps their own head.

#### Per-slot NPC art (`NPCArmorTextures`)

Below the set grid, each armor slot can instead take any of **63 CHAR.ESF NPC
clothing textures** that were authored on the player body meshes — cycled with
`<`/`>` per slot. These are individual materials off different NPCs, not
coordinated sets, hence per-slot rather than another set row.

`internal/models/npcarmor.go` is **generated** by
`go-eqoa-esf-tools-v2/cmd/npctex -gogen`; edit the generator, not the table. See
`claude-notes/npc-armor-texture-survey.md` for the regeneration command and how
the set was derived.

Two invariants (regression-tested in `npcarmor_test.go` / `transmog_test.go`):

- **A slot's UV layout is shared across races but splits by sex.** Each entry
  carries a `Compat` bitmask over (race, sex); the picker filters on the local
  body and `transmogState.apply` re-validates every frame, dropping art the
  rendered body cannot wear. Never offer a texture without checking `FitsBody`.
- **An override outranks a robe.** `resolveSlotTexture` returns it first, and the
  two-pass armor branch excludes overridden slots from `isRobe` — otherwise a
  robed character renders single-pass and loses the skin under transparent areas.

## Animation System

### SpriteAnimController (`animcontroller.go`)
Two-layer model matching PS2 `VICSprite::SetAnimation` (0x00422D78):
- **Layer 0 (`LayerUpper`)**: the upper body; also the whole body of a non-humanoid
- **Layer 1 (`LayerLower`)**: the lower body (humanoids only)

**Priority gating** is `SetAnimation`'s rules exactly (`setAnimation`, `animcontroller.go`): integer per-layer priorities (a slot the PS2 table omits scores 0), the idle-over-walk exception on the legs, and the in-progress-blend cases. A request while a blend is in flight replaces without blending (`AddPlay`).

**PlayOnce** comes from the model's play list (`BindModel`), not a guess: every attack, hit, cast and emote slot plays once on all 568 CHAR.ESF models. `applySkeletal` clamps play-once anims and notifies the controller via `MarkActionFinished()`. `SetDefaultEntries` (`ApplyDefaultEntries`, run at model load) fills the slots a model lacks.

**The two layers are body HALVES**: `LayerUpper` (0) and `LayerLower` (1). `Resolve()` returns (upper, lower). A humanoid's `SetLocomotion` and `SetAction` set both layers, and the priority tables decide each layer. Walking plus an attack leaves the upper body swinging and the legs walking. A creature uses layer 0 alone. Death forces slot 14 and the special idle is slot 15 for every skeleton. `ClearAction` empties only non-locomotion layers. Regression-guarded by `internal/models/walk_repro_test.go`.

**Action handles**: `SetAction` returns VICSprite's `m_actionSeq`, and `IsActionComplete(handle)` is true once anything later took layer 0 (a rejected action is complete at once) or the play stopped. Spell `Wait 0` waits on that handle, and `SetAction`/`PlayAttack` arm the "Spell FX 1-4" triggers for `Wait 3`.

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
250ms interpolated transition (PS2 `AddInterpolatedPlay`) for EVERY skeleton, creatures included. The outgoing play holds its frame (`StopPlayback` → `StopEnd`). The play rate is the play-list weight alone — nothing in the running game calls `SetPlayRate`, so a faster run does not animate faster.

### Negative Speed
CPlayList speed field negative = reverse playback (idle=-0.80, swim_back=-1.50). `math.Abs()` for rate, sign for direction.

### Combat Animations
- `animTimerB` — idle during combat with timer < 5s forces animID 17 (combat idle)
- Death forces animID 14, rejects non-death actions when dead
- Special idle: random 20-40s timer → CPlayList slot 15
- Attacks: `attackActionAnim` is `SetAttackAction`/`SetAttackAltAction` (vicsprite.cpp:551-665) per hand, from the attack type in each hand (11 = empty, the unarmed 23/22 and 25/24)

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

## Server Rules and the Patcher

**Two rule sets** (`internal/models/rules.go`). Sandstorm's players run the retail disc
with its S2 patch, which changes what retail fixes; a server of our own talks to the
retail disc. `models.ActiveRules` is chosen at startup by `RulesFor(server_ip, rules)`:
Sandstorm's address → `sandstorm`, anything else → `retail`, and `"rules"` in
`client.json` overrides. They differ in:

| | retail | sandstorm (S2) |
|---|---|---|
| tints | 15, ≥15 → 0 (`SetArmorSlot`) | 30, own table, 15 is clear (alpha 0) |
| armour set clamp | ≥9 → 0 | ≥10 → 0; set 9 = CHAR.ESF's shadowed plate (= `ArmorSetOrnatePlate`) |
| helm | `Helm[set]` | set 9 wears set 4's (plate) helm, S2's GetHelm `36 → 16` |
| robes | 0–3 | 0–5; 4/5 from `ss-art.csf`, robe 0 when it is missing |
| Social, third tab | LFG | **Search for Player** (`playersearch.go`): Register/Unregister `0x1503`/`0x1504`, search `0x1502`, results `0x1501` + `0x1500`, Location/Group lines, Tell, Invite |
| unknown `/command` | `Unrecognized command "…".` locally (string 826) | sent to the server as say `0x000B`, slash and all (Sandstorm's server-side commands) |
| worlds | 0–5 | 6 = `UNDERFOOT.ESF` (not on the disc; the assets folder), 7 = `SECRETS.ESF` |
| Auctions window | opens on Max. Level 50 | opens on 60 |

Applied where the server's bytes enter (`entityEquipment`); transmog runs after, so its
own sets 9/10 are never clamped. DamageIndication's optional trailing RGB tints the
floating number on either server (`FloatingText.Color`). **S2's identity and telemetry
layer is deliberately NOT ported**: DiscVersion 21 with a hardware address and a card
install id, the 11 bytes of pad timing on UseSkill, the `0x07E1` heap lines and the
`0x1505`/`0x1507` crash reports. Go sends the retail DiscVersion 18 and UseSkill, and
Sandstorm serves everything above to it. All of it, with the wire layouts, is in
`claude-notes/sandstorm-s2-live-20261015.md`.

**`WORDS.TXT`** (`game.Words`): string id N is line N, counted from 1. It is read from the
save folder when the patcher put one there, else off the disc. The player search's
Location line uses it, with CLIENT's per-world zone grids (`internal/game/zones.go`;
Tunaria is 13 × 16 — the crawler's 12 × 15 copy is wrapped wrong).

**The auction (`internal/game/auction.go`, `handleAuctionStuff`)** follows the decomp, not
a capture. A search is `0x1205` sub 7: the request number, a cursor (−1 new, else the last
listing's id), the direction 1, then `AuctionFilter` exactly as `func_00BB4A80` writes it
(flags byte, 8-bit string, UTF-16 search text, two u32 masks, six zigzag fields, a byte).
Every page repeats the search's filter. `0x1206` is `VIBankControl::Recv1206` and sub-type
`0x81` is a results page: echoed request number, a value, the listings, then three
per-listing arrays. **0x1206 is not a quest message** — the old handler that read it as
one swallowed every page. An empty page ends the paging ("No auctions found." /
"End of search results.").

**The patcher** (`internal/network/patcher.go`) is SETUP's `PatcherAPI`: the VerantPatcher
manifest off port 7000, then each entry against the **save folder**
`saves/<server>/BASLUS-20744EQONLINE/` beside the binary — the memory card's game folder,
one per server. Delete entries are removed; a file is fetched when missing or its size
differs (the PS2's test) or, under `UseCRC`, its CRC32; every download is checked
against the manifest's size and CRC and written through a `.part`. LocalNames that leave
the folder are refused. It runs in the startup I/O (`Engine.SaveDir`), non-fatally;
`headless -save DIR` runs it too. The model manager then reads the folder's art
(`SetSaveArt`: `ss-art.csf`, `addart.csf`) after the disc's. Offline mode reads the
configured server's folder as it last stood.

## Offline Mode

`./client --offline` enters the world with no server: no auth, patch, update check or presence, and no save written. It is how rendering and client-side systems are checked without a session.

```bash
xvfb-run -a ./client --offline --iso "$ISO" [--char NAME] [--pos X,Y,Z] [--facing DEG] \
    [--hour H[,H...]] [--world N] [--weather WXTYPE] [--npc 0xMODEL[:dead]]... [--npc-dist D] \
    [--spell 0xEFFECT] [--cam DIST,PITCHDEG,ANGLEDEG] [--size WxH] \
    [--shot out.png [--shot-wait S] [--shot-frames N]]
```

`--shot` captures once the world has streamed in, one file per `--hour`. Repeating an hour (`--hour 12,12,12`) takes a sequence `--shot-frames` apart. `--spell` plays an effect from the player at the first NPC. Under Xvfb the client runs at about 5 fps, and physics runs during the loading screen.

## Problem Reports (F3)

The F3 key from Dab's Mod (`~/perfect-dark`, `port/src/tracereport.c`), ported.
**F3** writes `traces/eqoa-<stamp>.txt` + `.png` beside the binary: the frame, and a
dump of the client's state (session, player, target, the 40 nearest entities, UI,
settings, runtime, and the last 200 log lines from `logring.go`, the only copy a
player has). A dialog then takes a note and an optional credit name (kept as
`report_name` in `client.json`). **Send** POSTs it to
`https://texturepacks.art/eqoa/reports/report` (`report_server` in `client.json`
overrides). **Close** sends nothing. **Shift+F3** is the bone debug toggle, which used to be F3.

- **Reading them:** `ssh sdg@10.8.0.1 'ls -t ~/eqoareports/reports | head'`, then
  `scp` the `<id>.*` pair. The public board is <https://texturepacks.art/eqoa/reports/board>.
  Statuses go in `~/eqoareports/reports/status.txt`, one line a report.
  `cmd/reportd/README.md` has the format, the bounds and the deploy steps.
- **The server** is `cmd/reportd`, a stdlib Go port of pdghostd's `/report` and board.
  It runs as `eqoa-reportd.service` on `127.0.0.1:9091` behind nginx's
  `location ^~ /eqoa/reports/`, which needs no password and allows an 8m body.
  It is deployed by hand, not by CI.
- **The game does not pause.** While the dialog is open it takes the keyboard the way
  chat and the menu do: `handleInWorldInput` returns early, and the login and
  char-select handlers are skipped. `reportBeginFrame` takes the frame's typed
  characters before any draw-time text box can, and moves the mouse off-screen
  (`SetMouseOffset`) until `reportFrame`, because widgets read clicks while drawing.
- **The picture is taken just before `EndDrawing`, after `rl.DrawRenderBatchActive()`.**
  raylib holds every 2D draw until `EndDrawing`, so without the flush the picture is the
  3D world with no HUD.
- **Nothing in a report may identify the player or hand over the account.** The
  home directory, the account name and the password are scrubbed. The auth log lines
  no longer print the token: with the AES key in the public source, that token *is* the
  password. **Never log a credential**: the log ring ships with every report.
  A log line in a per-frame path also empties the ring within seconds
  (`effectvol: active=…` already logs once a second).

### Crash reports

These are Dab's Mod's crash reports (`port/src/crashreport.c`), in two halves.

1. **A panic on the main goroutine.** That is the game loop, where nearly everything
   runs. `crashRecover` catches it: it is deferred in `runClient` *after* the window
   cleanup, so it runs first. It writes `crashes/crash-<run>.txt` at once, holding the
   panic, the stack, the state dump and the log ring. Then a crash screen offers
   **Send report to Dab / Close**, with a note, and the client exits with code 2.
2. **Anything that kills the process outright.** That is a panic on another goroutine
   (the terrain loader, the network), a fatal runtime error, or a fault in C under
   raylib. `debug.SetCrashOutput` points the runtime's own report at
   `crashes/run-<run>.out`. The next launch's `collectCrashes` turns it into a
   `crash-<run>.txt`, taking the log tail from `client.prev.log`, whose first line
   names its run. A `.out` with nothing past its header is a run that was killed
   and is deleted.

- **The next launch offers what is waiting**, once, on the login screen:
  **Send to Dab / Not now / Delete**. A report that is sent is deleted, and one that
  is not stays. Reports go to `/crash` on `cmd/reportd`. They land in
  `~/eqoareports/crashes` on the VPS, and no route serves them back.
- **`main` calls `crashCleanExit` itself, not with `defer`.** A deferred call also runs
  during a panic, and it would remove this run's crash output before the runtime wrote
  to it. Any other clean exit (`os.Exit`) must call `crashCleanExit` first, as offline
  `--shot` does.
- **The log now also goes to `client.log`** beside the binary, and the last run's log is
  kept as `client.prev.log`. The file stops at 64 MB; the ring keeps going.
- **Check it with a dev build:** `EQOA_CRASH_TEST=main` (the crash screen) or
  `=goroutine` (the next-launch offer) crashes 10 s into the world. Release builds have
  no such switch. `crash_test.go` crashes a child of the test binary for real.

## Gamepad Support

`cmd/client/gamepad.go`: PS2 DualShock 2 mapping → Raylib gamepad. Left stick = movement, right stick = camera orbit/zoom/swim, D-pad = targeting, cross/circle/square/triangle = actions. Auto-shows spell bar on first gamepad frame.

## Runtime Robustness Invariants (do NOT regress — 2026-07-21)

Verified zero data races under `-race` vs Sandstorm. Future edits must preserve:

- **Packet handling is panic-guarded**: `engine.go dispatchIncoming` wraps every message in `recover()`. Handlers index into variable-length wire data — a short/malformed packet must not crash the client. Also bounds-check before indexing (e.g. HP reads in `state.go` guard `len(data) >= 28`).
- **Startup I/O runs off-thread, results applied on the main goroutine**: `runStartupIO` (blocking auth/patch/connect) touches ONLY its args, never GameState; `applyStartupResult` (called from `Tick`) writes `Phase`/`Conn`/`StatusMsg`/`AccountID`. Never write those fields from the startup goroutine — that races the game loop. Same pattern for login: goroutine → `loginResultCh` → `pollLoginResult` on the main goroutine.
- **World switch closes the old terrain loader**: `switchWorldTerrain` calls `terrain.Loader.Close()`, which stops the load-worker goroutine THEN closes the world ESF file handle + overlays. Skipping it leaks a goroutine + OS file handle per continent switch.
- **Zone eviction unloads GPU textures**: `renderer.go resetZoneSlot` must `rl.UnloadTexture` the slot's `texCache` (not just meshes) or VRAM grows monotonically while streaming.
- **Headless tool serializes GameState with `gameMu`** (`cmd/headless/main.go`): tick loop + all command handlers hold it; hold it around any GameState access but NEVER across a `time.Sleep` (blocking commands release it). The visual client doesn't need this — its Tick + reads are all on the render goroutine.
- **No credential in source or in a release**: the repo and its releases are public. Tools read the game account password from `$EQOA_PASSWORD`; presence creds are `-ldflags` only, never in CI (see Deployment).

## DRDP Protocol Client (`internal/network/`)

### Connection Lifecycle
Auth → patch (the patcher, into the save folder) → server list → world entry. Handshake completes on GuaranteedAck (fast) or 5-packet count (fallback).

### The receive loop (`dispatchIncoming` = CLIENT's `VIClient::PumpMessages`)
One switch on the DRDP message type, as CLIENT's (`internal/game/intake_routing.go`):
0xFB → the opcode dispatcher (`HandleMessage`); **0xFC reads only the shard list 0x07B3**,
and only before a shard is chosen; 0xF9 (the 0x12 keepalive) is read and kept nowhere;
0xFF (the server opened the session: `ProcessIncoming` queues it ahead of the promoted
session's first messages) resets the world; 0xFD (the connection ended:
`Connection.TakeTerminate`, polled by `DrainIncoming`) maps its byte 1→1, 14→16, 15→15,
else 10, and shows the disconnect status; below 0xF8, the state channels. Every message
carries `Src`, the sender's endpoint id; **the MemoryDump's is the self id**
(`Msg000D: m_selfId = (int)src`), and until it arrives every entity message is dropped.

The shard list's count is ZIGZAG and its entries are filtered to the client's language:
Sandstorm's raw count byte 4 is two of its six entries, one of them English.

### State Channels
A message at or below the channel's rx is dropped and the channel re-acked with rx; a delta whose reference base is missing (or > 4,096 ahead) discards the WHOLE datagram, unacked. Acks repeat twice, uncapped. Bases are pruned wrap-safely. A reassigned channel keeps its sequence (`ClearStateAck` is a no-op).

**Outgoing RLE** is `run_length_encode` exactly: the short form holds at most 7 literals (8+ sets bit 7, the long-form flag), and zero runs stop at 255.

**Guaranteed-ack mask**: bit 0 = rx+2, unsigned LEB128 (`connection_get_ack_mask`), pinned to a PS2 capture. Body flag **0x04 is never sent**: the PS2 writes an always-zero field there, and no PS2 capture sets it.

### Reliability
- Every sequence compare is wrap-safe (`int16(a−b)`).
- Guaranteed messages are held and released strictly in order (`m_delayed`); 0xFA pieces collect until the next 0xFB/0xF9.
- RTO = the largest of 16 RTT samples + 100 ms, starting at 1,100 ms; fast retransmit once the server acks a later flush.
- A packet takes messages only while they fit; reliable messages over 1,300 bytes are split.
- Incoming ack masks are UNSIGNED (bit 0 = ack+2).
- Run live on Sandstorm 2026-10-15: world entry, the fragmented MemoryDump, ~110 s in world, 0 retransmits/drops.
- **Sandstorm refuses VPN addresses**: a real login from one gets auth opcode 0x43 and a close (reported as "auth read failed: unexpected EOF").

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

### Entity state channels (0x00-0x17, 162 bytes) — `ChannelEntity` (0x00BD41B8)
**24 slots, channel 0 an ordinary one**: the player's own entity is the one whose id is the
self id, on whatever channel. 0x18 and up are read by nothing (the Go server's 24-28 are
invisible to a PS2). Header: byte 0 a flag, 1-4 the id, 5 the type (0x81 `VIEntity`, 40
bytes read; 0x82 `VICreatureEntity`, 162; any other makes the PS2 assert). Under 5 bytes
empties the slot. A slot keeps its entity only if type and id match **and the flag is set**;
otherwise the old one is deleted and a new one made only if the flag is set — so **flag 0 on
the slot's own entity is the server removing it** (Sandstorm's first message on a channel
carries 0 and makes the entity because the slot is empty). Velocity bytes 40-42 are ×21/128.
Byte 145 is its own byte; bytes 146-147 are `m_npcFlags` (1 merchant, 2 bank, 8 blacksmith,
0x80 not attackable). NameColor byte 142: 0=NPC, 2=Player (CLIENT's interact also takes 3).

Other channels: 0x40 group positions (13-byte records), 0x42 stats (29 + 29 u32 + a byte;
index 7 is the current HP), 0x43 effects (u32 count ≤ 8, 132-byte records: icon, wide name),
0x44-0x6B forty `VIChannelRec`s.

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
| `cmd/reportd/` | F3 problem reports + public board (pdghostd port); `eqoa-reportd` on the VPS, see its README |
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

- `github.com/gen2brain/raylib-go/raylib` — 3D rendering (local replace → `../raylib-go/raylib`; fork `DabDavis/raylib-go` branch `eqoa`)
- `github.com/DabDavis/eqoa-esf-tools` — ESF file parsing (local replace → `../go-eqoa-esf-tools-v1`; branch `client-v1`)
- Both pinned for CI in `scripts/deps.env` — see Deployment
- `github.com/mattn/go-sqlite3` v1.14.34 — SQLite for crawler/statharvest tools

## TODO

- Helm scale adjustment (proportional to character height)
- Ogre face textures (race 9 not in FaceTextures table)
- Tint color application (VICSpriteCust::Tints — 15 RGBA colors, [14] yellow)
- Overlay back-face lighting fix
- `VIWalkControl::UpdatePhysics` — gravity, jumping
- `VICollSphere` — swept sphere collision
- Water/swim integration (VIZoneTree BSP parsed but not driving swim mode)
