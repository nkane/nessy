# nessy

NES emulator built on the [chippy](https://github.com/nkane/chippy) 6502 CPU
+ bus + DAP debugger library. NTSC + PAL + Dendy region timing, headless
recorder, in-browser WASM build, full DAP attach for source-level debugging.

## Install

### Source build

```sh
# darwin / windows
go install -tags=nessy github.com/nkane/nessy/cmd/nessy@latest
nessy game.nes

# linux (one-time install)
sudo apt-get install -y libgl1-mesa-dev xorg-dev libasound2-dev \
  libxcursor-dev libxinerama-dev libxi-dev libxrandr-dev
go install -tags=nessy github.com/nkane/nessy/cmd/nessy@latest
```

The `-tags=nessy` build tag is required — `cmd/nessy` pulls in
[Ebiten](https://ebitengine.org/) for the game window, which needs CGO +
X11 / GL dev headers on Linux that the default CI runners don't carry.

### Binary releases

Per-OS archives ship on the [releases page](https://github.com/nkane/nessy/releases).
Homebrew tap / AUR / signed artifacts land with v1.0.0
([issue #8](https://github.com/nkane/nessy/issues/8)).

## Controls

| Input | Mapping |
|---|---|
| D-pad | Arrow keys |
| A | Z |
| B | X |
| Start | Enter |
| Select | Right Shift |

**Gamepads** (any standard-layout: Xbox / DualSense / 8BitDo / etc.) auto-route
to P1. D-pad + left analog stick both drive the D-pad; bottom face button =
A, right face = B, Start / Select = centre buttons. Hot-plug notified on
stderr.

**Hotkeys:**
- `Tab` (hold) — 4× fast-forward.
- `F11` — fullscreen toggle.
- `F12` — save a PNG of the current frame to `~/.nessy/screenshots/`.
- `F1`–`F4` — save state into slots 1–4.
- `F5`–`F8` — load the matching slot.

Save states live in `~/.nessy/states/<rom-hash>-slot<N>.state` (gzip-compressed
gob). Slots are keyed by ROM SHA-256 — a save from one game can't accidentally
restore into another.

Recent ROMs: `nessy` with no args prints the last 5 ROMs you booted from
`~/.nessy/recent`; `nessy N` (1..5) opens the Nth recent slot.

Controller remap: `~/.nessy/controller.json` re-binds any NES button to any
Ebiten key. Example:

```json
{ "p1": { "A": "Space", "B": "LeftShift", "Start": "Enter" } }
```

Missing entries keep the default mapping.

## One-shell debug launch

`chippy -nessy ROM` spawns nessy in the background, dials its DAP listener,
and opens the chippy TUI in attach mode paused at the reset vector:

```sh
chippy -nessy roms/demos/hello-bg/hello-bg.nes
```

Press `r` to run, `s` to step, `b` to toggle a breakpoint at the current PC,
`q` to quit (also shuts down the nessy game window).

If `chippy` can't find `nessy` on `$PATH` or as a sibling, pass `-nessy-binary
PATH`. Install a chippy binary separately via
[its release page](https://github.com/nkane/chippy/releases).

## Demos

Homemade demos ship under [`roms/demos/`](roms/demos/) — hand-rolled ca65
sources + checked-in `.nes` artifacts. Each doubles as a framebuffer-hash or
audio-presence regression test under `cmd/nessy/demo_*_test.go`:

| Demo | What it tests | Source |
|---|---|---|
| [`hello-bg`](roms/demos/hello-bg/) | PPU bg renderer + palette + nametable + reset path | Static "HELLO NESSY" title screen |
| [`input-echo`](roms/demos/input-echo/) | $4016 joypad strobe + serial shift + per-frame VRAM writes | 8 indicator boxes light up under live joypad input |
| [`vblank-bounce`](roms/demos/vblank-bounce/) | PPU NMI line + CPU NMI service + `JMP self` idle | Single tile bounces inside the playfield |
| [`triangle-arpeggio`](roms/demos/triangle-arpeggio/) | APU triangle channel + NMI-driven note rotation | A-major arpeggio (audio only) |
| [`noise-drum`](roms/demos/noise-drum/) | APU noise channel + LFSR feedback path | Low/high noise drum hit (audio only) |
| [`all-channels`](roms/demos/all-channels/) | Non-linear DAC mixer under multi-channel load | Pulse 1+2 + triangle + noise chord (audio only) |
| [`dmc-sample`](roms/demos/dmc-sample/) | DMC channel DMA fetch + delta-PCM + loop bit | 65-byte alternating-bit sample looped (audio only) |
| [`mmc1-banks`](roms/demos/mmc1-banks/) | MMC1 serial-shift PRG bank switching (prgMode 3) | Background flashes between two colours twice per second |
| [`oam-grid`](roms/demos/oam-grid/) | $4014 OAMDMA + 64-sprite OAM walk + sprite priority | 8×8 grid of solid squares centred on the playfield |
| [`state-counter`](roms/demos/state-counter/) | Save-state round-trip probe (frame_cnt → $3F00) | BG colour cycles through the palette as frame_cnt advances |
| [`vrc6-chord`](roms/demos/vrc6-chord/) | VRC6 cart + 3-channel audio expansion (mapper 24) | Sustained low chord (audio only) |
| [`sunsoft5b-chord`](roms/demos/sunsoft5b-chord/) | FME-7 cart + Sunsoft 5B audio (mapper 69) | Three-tone chord via YM2149 clone (audio only) |
| [`scroll-split`](roms/demos/scroll-split/) | Mid-frame horizontal scroll split (per-scanline `$2005`) | Vertical stripes offset between top + bottom halves |
| [`mmc3-split`](roms/demos/mmc3-split/) | MMC3 scanline-IRQ split bar (A12-counted) | Top half blue, bottom half green |

Run any of them:

```sh
nessy roms/demos/hello-bg/hello-bg.nes
nessy roms/demos/input-echo/input-echo.nes
nessy roms/demos/vblank-bounce/vblank-bounce.nes
```

Rebuild from ca65 source (requires `brew install cc65` / `apt-get install
cc65`):

```sh
make -C roms/demos all
```

## Headless recording

`cmd/nessy-record` captures a ROM run as a GIF or MP4 (video + audio +
scripted input) with no window, no OpenGL, no screen grab — it synthesises
the recording straight from the emulator, so it's deterministic and
CI-friendly.

```sh
go build -o nessy-record ./cmd/nessy-record

# GIF (stdlib only, video):
./nessy-record -rom roms/demos/vblank-bounce/vblank-bounce.nes -frames 120 -o out.gif

# MP4 with audio (needs ffmpeg):
./nessy-record -rom roms/demos/all-channels/all-channels.nes -frames 120 -o out.mp4

# Scripted joypad input (JSON keyframe timeline):
echo '{"20":["Up"],"40":["Up","A"],"60":[]}' > in.json
./nessy-record -rom game.nes -script in.json -frames 90 -o out.gif
```

## Accuracy

nessy passes the standard nesdev / Blargg accuracy ROM suite headlessly:

| ROM | Result |
|---|---|
| ppu_vbl_nmi.nes | 10/10 |
| instr_timing.nes | PASS |
| cpu_interrupts_v2.nes | 5/5 |
| apu_test.nes | 8/8 |
| instr_misc.nes | 4/4 |
| instr_test-v5_official.nes | 16/16 |
| instr_test-v5.nes (all_instrs) | SKIP — `$AB LXA` unstable illegal opcode (analog-noise dependent) |

Rolling tracker: [#1](https://github.com/nkane/nessy/issues/1).

```sh
go test -tags=accuracy -run TestAccuracy -v ./cmd/nessy/...
```

ROMs cache under `$XDG_CACHE_HOME/chippy-tests/` (legacy path; pre-dates the
carve, kept for cache continuity).

## DAP server

nessy serves a [Debug Adapter Protocol](https://microsoft.github.io/debug-adapter-protocol/)
endpoint on port `:14785` so editor debuggers can attach to a running ROM.

```sh
nessy -dap-port 14785 -wait-for-debugger game.nes
chippy -dap-attach tcp:localhost:14785      # or your editor's DAP client
```

VS Code / nvim-dap onboarding lives in
[chippy's docs/dap.md](https://github.com/nkane/chippy/blob/main/docs/dap.md).

## License

MIT. See `LICENSE`.
