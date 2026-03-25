# EQOA Go Client

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
| `VIWalkControl::Process` | `handleInWorldInput()` — camera-relative movement + facing | `cmd/client/main.go` |
| `VIWalkControl::SetTargetHeading` | `facing = atan2(moveX, moveZ)` — model +Z forward | `cmd/client/main.go` |
| `VIWalkControl::CalcFit` | `ZoneTerrain.CalcFit() → CalcFitResult` — iterative collide + Skitter | `internal/terrain/collision.go` |
| `VIWalkControl::CalcFloor` | `ZoneTerrain.SampleHeight()` — ground snap | `internal/terrain/collision.go` |
| `VICollRay::Init + Collide` | `castWallRay()` — Möller-Trumbore segment-triangle test | `internal/terrain/collision.go` |
| `VICollRay::Skitter` | Inline in `CalcFit` loop — `slide = rem - dot(rem,N)*N` | `internal/terrain/collision.go` |
| `VICollRay::PerturbMult` | `skin = 0.25` constant in `CalcFit` | `internal/terrain/collision.go` |
| `VIWalkCamera::Process` | Camera orbit in `handleInWorldInput()` | `cmd/client/main.go` |
| `VIParticleEmitter::Update` | Analytical particle physics (birth/death/position/size/color) | `internal/game/particles.go` |
| `VIParticleSprite::Raster` | Billboard quad rendering (camera-facing textured quads) | `internal/game/particles.go` |
| `VISpellEffectSystem::Play` | Spell effect playback with timed event sequences | `internal/game/spelldriver.go` |
| `VISpellEventDriver::Process` | 7-phase event state machine (17 event types) | `internal/game/spelldriver.go` |
| `ParseParticleDefinitionObj` | Parse 0xC020 particle attributes | `pkg/esf/spellfx.go` |
| `VICSprite::SetAnimation` | Priority-gated animation (dual-pair state machine) | `internal/models/animcontroller.go` |
| `VIHSprite::AddInterpolatedPlay` | 250ms cross-fade blend with per-pair rejection | `internal/models/manager.go` |
| `VICSprite::SetAttackAction` | Primary swing anims (11 weapon types) | `cmd/client/animation.go` |
| `VICSprite::SetAttackAltAction` | Alt swing anims (Primary/Alt alternation) | `cmd/client/animation.go` |

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
cmd/client/main.go              # Raylib window, Camera3D, game loop, HUD
cmd/client/render.go             # Entity/player rendering, animation dispatch
cmd/headless/main.go             # Headless REPL client (testing against Sandstorm)
internal/game/engine.go          # Core game loop, position updates, movement
internal/game/state.go           # GameState, packet handling, state channels
internal/game/spelldriver.go     # SpellEffectSystem/Driver/EventDriver (17 event types)
internal/game/particles.go       # ParticleSystem/Emitter — billboard quad rendering
internal/game/spellfx.go         # SPELLFX.ESF loader
internal/terrain/terrain.go      # ESF zone loading, Triangle/Tile/ZoneTerrain structs
internal/terrain/renderer.go     # GPU mesh upload, multi-pass rendering
internal/terrain/collision.go    # CalcFit (wall slide), castWallRay, SampleHeight
internal/models/manager.go       # Character model loading, animation playback, equipment rendering
internal/models/mesh.go          # CSprite → triangle strip → rl.Mesh
internal/models/animcontroller.go # SpriteAnimController (dual-pair priority state machine)
internal/models/animstate.go     # Animation playback time/blend tracking
internal/models/equipment.go     # Equipment DictID tables (armor, hair, robe, helm, face)
internal/models/skeleton.go      # Bone transforms, ComputeAnimPose* methods
internal/network/connection.go   # DRDP protocol client (reliable, state channels, XOR delta)
internal/network/packet.go       # Packet construction, CRC, segment assembly
```

## Quick Commands

```bash
cd go-eqoa-client
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

```bash
cd /home/sdg/claude-eqoa/go-eqoa-client
./scripts/deploy.sh
```

The client is a single binary — all assets are read from the EQOA Frontiers ISO at runtime. No fonts, asset folders, or archives needed.

### Build Version

Embedded at compile: `-ldflags "-X main.BuildVersion=20260306-abc1234"`. Generated automatically by `deploy.sh` from date + git hash. Default `"dev"` without ldflags.

Server IP: config `server_ip` > default `5.161.217.188` (Sandstorm).

### Key Files

| File | Purpose |
|------|---------|
| `cmd/client/updater.go` | Auto-updater (manifest fetch, download, SHA-256 verify, self-replace) |
| `scripts/deploy.sh` | Build + manifest + push to update server + download links |
| `cmd/client/login.go` | `loginConfig` with `ServerIP`, `ISOPath` fields |

## Terrain Rendering

### Data Flow
```
TUNARIA.ESF → Loader.loadZone() → ZoneTerrain (tiles + textures)
  → Renderer.buildMeshes() → GPU meshes (one per TexID+Layer)
  → Renderer.Draw() → rl.DrawMesh() per group
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

**TODO**: Helm scale adjustment (models authored at fixed size, need to scale proportionally to character height).

## Animation State Machine

### SpriteAnimController (`animcontroller.go`)
Dual-pair model matching PS2 `VICSprite::SetAnimation` (0x00422D78):
- **Pair 0**: Locomotion (idle, walk, run, swim)
- **Pair 1**: Action (attack, hit reaction, death, emotes)

**Priority gating**: Playing animations always interruptible. Only stopped/finished play-once animations protected by priority. Idle can never replace a stopped anim. Same-animID rejected.

**PlayOnce**: Attack anims hold last frame; locomotion loops. `applySkeletal` clamps play-once anims and notifies controller via `MarkActionFinished()`.

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

## PS2 RE References

| Resource | Location |
|---|---|
| SUPPORT symbols (4,227) | `claude-eqoa-iso/decrypted_modules/SUPPORT_symbols.txt` |
| CLIENT symbols (3,776) | `claude-eqoa-iso/decrypted_modules/CLIENT_symbols.txt` |
| CLIENT strings (4,621) | `claude-eqoa-iso/decrypted_modules/CLIENT_strings.txt` |
| Master RE doc | `claude-eqoa-iso/EQOA_REVERSE_ENGINEERING.md` |
| DRDP decompilation | `claude-eqoa-iso/decrypted_modules/drdp_decompiled.c` |

## Dependencies

- `github.com/gen2brain/raylib-go/raylib` v0.55.1 — 3D rendering
- `github.com/eqoa/server/pkg/esf` — ESF file parsing (local replace → ../go-eqoa-server)

## TODO

- Helm scale adjustment (proportional to character height)
- Ogre face textures (race 9 not in FaceTextures table)
- Tint color application (VICSpriteCust::$Tints — 14 RGBA colors)
- Overlay back-face lighting fix
- `VIWalkControl::UpdatePhysics` — gravity, jumping
- `VICollSphere` — swept sphere collision
