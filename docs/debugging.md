# Debugging NES ROMs

nessy ships chippy's DAP server alongside the Ebiten game loop. Any DAP-speaking editor can attach — chippy's TUI is the easiest path, but VS Code / nvim-DAP / Zed work too.

## One-shell debug launch

The fastest way:

```sh
chippy -nessy roms/demos/hello-bg/hello-bg.nes
```

That spawns nessy in the background, dials its DAP listener, and opens the TUI in attach mode paused at the reset vector. No second terminal needed.

## Two-shell attach

If you want to keep nessy and chippy in separate windows:

**Terminal 1:**

```sh
nessy -wait-for-debugger game.nes
```

`-wait-for-debugger` pauses the CPU at boot until a DAP client attaches.

**Terminal 2:**

```sh
chippy -dap-attach tcp:localhost:14785
```

The TUI's panels (registers / disasm / memory / stack / source) all show live state synced from nessy.

## Symbol files

If your ROM was built with `ca65 -g` + `ld65 --dbgfile`, nessy auto-detects the `.dbg` sibling and feeds it to chippy through the DAP `Source` event so the TUI's source panel highlights the current line in real `.s` code.

```sh
nessy game.nes              # nessy auto-detects game.dbg
nessy -dbg path/to/game.dbg # explicit override
```

## What the TUI gives you

- **Step** (`s`), **step-into** / **step-over** (`o` / `O`), **step-back** (`b`), **continue** (`r`).
- **Breakpoints** — toggle at any source line or instruction address; survive across reattach.
- **Watch expressions** — read / write watches on any RAM address; the expression engine handles arithmetic + label resolution.
- **Memory editor** — byte-level cursor, hex edit mode, `:goto` jumps anywhere.
- **Stack panel** with JSR-frame annotation (`ret $XXXX  callee  file:NN`).
- **Source panel** with the current PC highlight + clickable line numbers.
- **Quake-style console** (\`backtick\`) for scrollback over `:` commands.

See [`dap.md`](../dap.md) for protocol details and [`editors.md`](../editors.md) for the per-editor support matrix.

## NES debug-state channel

Everything above is CPU-side — it's the chippy 6502 debugger. The
NES-specific inspection tools (PPU / nametable / sprite / event viewers,
multi-space memory + access heatmap, register + APU state) are landing
as TUI panels under the [debugger epic (#27)](https://github.com/nkane/nessy/issues/27).

Their foundation ([#28](https://github.com/nkane/nessy/issues/28)) is a
DAP **custom request** the chippy TUI sends to pull a coherent snapshot
of NES state:

- Command: `nessy/debugState` (served via chippy/dap's
  `AttachConfig.CustomRequestHandler`, added in chippy v1.4.0).
- The handler runs under the CPU lock the DAP dispatcher already holds,
  so every field reflects the same instruction boundary — no mid-step
  tearing.
- Response body is a versioned `DebugSnapshot`: frame/scanline/dot
  timing, the 6502 register file, PPU register latches + scroll state,
  APU channel + frame-counter state, and the active mapper's state.

The panel-specific issues (#29–#35) each extend the snapshot with their
own section (full OAM, nametable bytes, per-dot event log, access
heatmaps), so a routine poll stays cheap until a panel needs the heavy
data.

### PPU viewer ([#29](https://github.com/nkane/nessy/issues/29))

The tilemap / pattern / palette panels pull their (heavier ~12 KiB)
render state on demand via a second request so the routine status poll
stays light:

- Command: `nessy/ppuViewer`.
- Response is a `PPUViewer`: the 8 KiB pattern window ($0000-$1FFF) as
  currently banked, the four 1 KiB nametables after mirroring
  resolution, the 32-byte palette, the decoded scroll cursor
  (coarse/fine X+Y + nametable select), and the active mirroring mode.
- **Side-effect-free:** pattern reads go through the cart's `PeekCHR`
  on mappers whose `PPURead` has side effects (MMC3's A12 IRQ clock),
  so opening the viewer can't perturb a game's scanline-IRQ timing.

### Sprite / OAM viewer ([#30](https://github.com/nkane/nessy/issues/30))

- Command: `nessy/spriteViewer`.
- Response is a `SpriteViewer`: the raw 256-byte OAM, the `$2003`
  cursor, the 8x16 flag + 8x8 sprite pattern-table base, and all 64
  sprites decoded — X/Y, tile, palette, priority, H/V flip, and an
  on-screen flag (Y < `$EF`). OAM order is priority order.

### Register viewer ([#34](https://github.com/nkane/nessy/issues/34))

- Command: `nessy/registers`.
- Response is a `RegisterView`: the PPU register latches with named bit
  breakdowns (PPUCTRL/MASK/STATUS flags + scroll/address state), the
  full APU channel + frame-counter state, and the active mapper's
  register state — self-contained so the panel renders without
  cross-referencing the routine status snapshot.

## Live demo

![nessy-attach](https://github.com/nkane/chippy/raw/main/test/smoke/out/nessy-attach.gif)

Recorded via [VHS](https://github.com/charmbracelet/vhs). Tape source: [`test/smoke/nessy-attach.tape`](https://github.com/nkane/chippy/blob/main/test/smoke/nessy-attach.tape).
