# eqoa-client

Native PC client for EverQuest Online Adventures (EQOA). Renders zone terrain, character models, animations, spell effects, and flora from the PS2 game disc using Raylib. Connects to [Sandstorm](https://eqoa.live) via the DRDP UDP protocol.

Built as a 1:1 port of the PS2 client's VI engine — movement, collision, animation, and rendering match original PS2 behavior.

![EQOA Client](https://img.shields.io/badge/platform-linux%20%7C%20windows-blue)

## Download

Single binary — no installer, no dependencies. From [Releases](https://github.com/DabDavis/eqoa-client-v1/releases):

- **Stable** — the [latest release](https://github.com/DabDavis/eqoa-client-v1/releases/latest): `eqoa-client` (Linux amd64) or `eqoa-client.exe` (Windows amd64)
- **Dev** — the [latest dev build](https://github.com/DabDavis/eqoa-client-v1/releases/tag/client-dev), rebuilt on every push to `master`; ahead of stable and less tested

Auto-updates on every launch, staying on the channel it was downloaded from. Requires an EQOA Frontiers ISO disc image (SLUS-20744) — all game assets are read directly from the ISO at runtime.

## Features

- **PS2-accurate rendering** — centerTransform pipeline, day/night TOD table, player lantern, EQOA bitmap font
- **Zone rendering** — Multi-layer terrain with overlay blending, procedural radial flora (1.2M instances)
- **Character models** — Skeletal animation, equipment rendering (armor, hair, helm, robe, face), bone-attached weapons
- **Animation system** — Dual-pair priority state machine with 250ms cross-fade, Gram-Schmidt re-orthonormalization, per-weapon attack pools
- **Spell effects** — Particle emitters, 17-event-type driver, timed sequences from SPELLFX.ESF
- **Sound system** — Animation-triggered SFX, 3D positional audio, zone BGM streaming from ISO
- **Collision** — Wall-sliding (CalcFit), ground snapping (SampleHeight), Möller-Trumbore ray-triangle
- **Networking** — Full DRDP protocol client: reliable messaging, state channels, XOR delta, fragment reassembly
- **UI** — PS2-accurate 9-slice textured frames from UI.ESF, equipment management, merchant/dialogue/hotkey menus
- **Display settings** — Resolution selection, fullscreen, FPS cap, VSync, FPS counter
- **Auto-updater** — Checks for new builds on launch, self-updates with SHA-256 verification
- **Headless mode** — REPL client for protocol testing without rendering

## Quick Start

```bash
# Download and run
chmod +x eqoa-client
./eqoa-client
```

On first launch, use the built-in file browser to select your EQOA Frontiers ISO. The path is saved to config — you only need to do this once.

### Build from Source

```bash
go build -o client ./cmd/client/
./client
```

Headless mode (no GPU required):

```bash
go build -o headless ./cmd/headless/
./headless
```

## Package Structure

```
cmd/
  client/           Raylib 3D client (main game loop, input, rendering, HUD)
  headless/         Headless REPL client (protocol testing)

internal/
  game/             Game engine: state, movement, spells, particles, effect volumes
  terrain/          Zone loading, multi-pass rendering, collision, radial flora
  models/           Character models, skeletal animation, equipment, bone transforms
  network/          DRDP protocol client (reliable, state channels, XOR delta)
  protocol/         Opcode definitions, message types
  audio/            Sound effects, BGM streaming, VAG decoding
  frustum/          View frustum culling
  sky/              Skybox rendering with time-of-day
  ui/               PS2-accurate VIUI texture system (36 sheets, 252 elements)
  gfx/              OpenGL/ES abstraction layer
```

## Terrain Rendering

```
ISO → TUNARIA.ESF → loadZone() → ZoneTerrain (tiles + textures)
  → buildMeshes() → GPU meshes (one per texture + layer)
  → Draw() → multi-pass: base terrain → overlays → flora
```

- **Base pass**: Opaque terrain with pre-baked overview textures
- **Overlay pass**: Detail textures (sand, grass) with alpha blend, `GL_LEQUAL` depth, no depth write
- **Flora pass**: Procedural flora scattered on collision triangles, alpha-defringed shader

## Animation System

Dual-pair model matching PS2 `VICSprite::SetAnimation`:

- **Pair 0**: Locomotion (idle, walk, run, swim) — loops
- **Pair 1**: Action (attack, hit, death, emotes) — play-once with priority gating

All animation IDs are ESF CPlayList slot numbers. `PLSlotToRawPair` maps slots to per-model upper/lower body animation indices. 250ms cross-fade blend with Gram-Schmidt re-orthonormalization to prevent model squishing.

## Networking (DRDP Protocol)

Full implementation of EQOA's custom UDP protocol:

- Reliable messaging with per-message retransmit and RTT-based RTO
- 29 state channels (self + 28 entities) with XOR delta compression
- Arrival history bitmap for duplicate/out-of-order detection
- Fragment reassembly with orphan timeout
- Position updates at ~3.3 Hz (0x40 opcode, 41 bytes)

## System Requirements

- **Windows**: Windows 10+ (64-bit)
- **Linux**: glibc 2.29+, OpenGL, X11 (Wayland works via XWayland)
- **EQOA Frontiers ISO** (SLUS-20744) — validated on first launch

## Dependencies

- [raylib-go](https://github.com/gen2brain/raylib-go) v0.55.1 — 3D rendering
- [eqoa-esf-tools](https://github.com/DabDavis/eqoa-esf-tools) `pkg/esf` — ESF file parsing

## Related Projects

- [eqoa-esf-tools](https://github.com/DabDavis/eqoa-esf-tools) — ESF/CSF asset inspection, export, and patching tools
- [eqoa-pipeline](https://github.com/DabDavis/eqoa-pipeline) — PCSX2 debug tools and zone serve pipeline
