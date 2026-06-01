# nessy — architecture & history context dump

The "handoff doc" for future maintainers + AI agents. Captures **why**
the current design looks the way it does and what's known-broken /
known-tracked. Pair this with `CLAUDE.md` (load-bearing invariants
+ workflow conventions).

Sections:

1. [Project overview](#1-project-overview)
2. [Architecture](#2-architecture)
3. [Conventions & workflow](#3-conventions--workflow)
4. [Progress](#4-progress)
5. [Key decisions & rationale](#5-key-decisions--rationale)
6. [Critical context](#6-critical-context)

---

## 1. Project overview

nessy is an NES emulator written in Go. It targets the full licensed
NTSC + PAL library: every shipping mapper through mapper 24
(VRC6) / 26 (VRC6 alt) / 69 (FME-7) / 85 (VRC7), with accuracy enough
to pass the standard nesdev / Blargg test ROM suite + drive the
chippy DAP source-level debugger.

### Vision

- **Cycle-accurate 2A03 + 2C02 + cart audio** — no shortcuts on
  interrupt timing, sub-cycle DMA ordering, or APU frame counter.
- **Source-level debugging out of the box** — attach chippy's TUI via
  DAP, set breakpoints, step, watch.
- **Deterministic headless captures** — `cmd/nessy-record` for GIFs /
  MP4s with no GL / window dependency.
- **In-browser playground** via `cmd/nessy-wasm`.

### Why a separate repo from chippy

chippy is library-shaped: a 6502 CPU + bus + DAP server with no graphics
deps. nessy needs Ebiten (CGO, X11/GL on Linux). Keeping them in the
same repo forced anyone consuming chippy's CPU as a library to drag in
the entire NES + Ebiten dep graph. Splitting (chippy#386, 2026-05-31)
let each side version on its own cadence + lets chippy ship as a clean
6502 library.

### Out of scope (today)

- **FDS** disk system.
- **MMC5 split-screen ExGrafix mode 2/3** (#6 covers MMC5 baseline).
- **Famicom Disk System BIOS** ROMs.
- **Tape-input** ROMs.

---

## 2. Architecture

### Package layout

```
cmd/
  nessy/          — Ebiten game binary; per-frame loop calls CPU.Step
                    in a tight inner loop, draws PPU framebuffer, queues
                    APU samples to Ebiten audio, polls joypad.
  nessy-record/   — Headless recorder; no Ebiten window. Drives the
                    emulator deterministically, encodes GIF / MP4 via
                    image/gif + ffmpeg pipe.
  nessy-wasm/     — js/wasm playground build. Same internal/nes, drives
                    via syscall/js + HTML canvas.

internal/nes/
  apu/            — 2A03 audio. Channels: pulse 1, pulse 2, triangle,
                    noise, DMC. Frame counter (4-step / 5-step) with
                    Mesen-aligned 6-internal-sub-step intervals.
                    Expansion audio: VRC6 (3-channel), VRC7 (OPLL FM
                    synth), Sunsoft 5B (3-channel YM2149 clone).
  cart/           — iNES + NES 2.0 header parse; per-mapper Go file
                    (nrom.go, mmc1.go, uxrom.go, cnrom.go, mmc3.go,
                    vrc2.go, vrc4.go, vrc6.go, vrc7.go, fme7.go,
                    aorom.go). Each implements cart.Cartridge.
  dma/            — $4014 OAMDMA peripheral. After chippy#377, just
                    flags the CPU's sprite-DMA state machine; the
                    513/514-cycle bus-steal runs inside the CPU's
                    ProcessPendingDma loop on the next read.
  ines/           — header parse + region detection.
  joypad/         — $4016/$4017 strobe + serial shift. $4017 also
                    forwards to APU.SetFrameCounter (the joypad +
                    APU share that register).
  ppu/            — 2C02 / 2C07. Per-cycle stepDot model; runs
                    dot-by-dot under cpuDriven flag (set by
                    cmd/nessy/wiring.go after the PPU is registered).
                    Sprite-overflow silicon bug emulated. Odd-frame
                    dot-skip uses renderingEnabledDelayed (Mesen
                    1-PPU-clock delay model).
  timing/         — region clocks: NTSC (1.789 MHz), PAL (1.662 MHz),
                    Dendy. Frame geometry + APU frame counter periods.

roms/demos/       — Hand-rolled ca65 demos that double as
                    framebuffer-hash / audio-presence regression tests.
                    See cmd/nessy/demo_*_test.go.

test/smoke/       — VHS recorder smoke tapes. Render to GIF +
                    upload as PR comments via the CI smoke job.

web/              — WASM playground HTML + JS shim.

docs/             — install, debugging, demos guides + this file.
```

### Core types

**`*cpu.CPU` from `github.com/nkane/chippy/cpu`** is the 2A03 CPU.
Constructed via `cpu.NewVariant(bus, cpu.VariantNES)`. nessy never
implements the CPU itself; the chippy dependency owns it.

**`*ppu.PPU`** implements `peripheral.Peripheral` claiming $2000-$3FFF
(8-byte register window mirrored). Drives /NMI level via `cpu.SetNMILine`.

**`*apu.APU`** implements `peripheral.Peripheral` claiming $4000-$4013.
`*apu.StatusPeripheral` wraps $4015 separately so the discontiguous APU
surface doesn't collide with $4014 OAMDMA.

**`*dma.OAMDMA`** claims $4014. Write forwards to `cpu.SetNeedSpriteDma`.

**`*joypad.Port`** claims $4016-$4017 with the forwarder to APU.

### MMIO bus chain

```
CPU ─ tui.WBus ─ cpu.MMIO ─ cpu.RAM
                          ├─ cartPeripheral (cart.Cartridge wrapped)
                          ├─ joypad.Port
                          ├─ apu.APU + apu.StatusPeripheral
                          ├─ ppu.PPU
                          └─ dma.OAMDMA
```

The loader + reset-vector helpers write directly to `RAM`, deliberately
bypassing MMIO (so they don't trigger PPU register writes during
initialisation).

### Per-cycle interleave model

`VariantNES` selects the per-cycle path in `cpu.Step`:

1. Each bus access (read/write/idle) ticks the chain one cycle BEFORE
   the access.
2. The PPU runs `Run(masterClockDeadline)` to catch up to the CPU's
   mid-cycle phase, dot by dot. Each CPU cycle = 3 PPU dots NTSC.
3. The APU runs `Tick(1)` per CPU cycle, advancing the frame counter +
   per-channel timers.
4. Cart `Tick(1)` for mappers with CPU-clock IRQs (FME-7, VRC4).
5. The 6502's addressing-mode dummy cycles are added per template via
   `cpu.addrDummies`.
6. `instrCycles == accounted` is asserted at end of each instruction —
   a regression in any dummy-cycle template panics immediately.

### PPU ticker

Per chippy#372 / chippy#375: PPU operates in `cpuDriven` mode. CPU.read /
write / idle calls `ppuRunner.Run(masterClock - cpuPPUOffset)`. MMIO's
generic `Ticker` fan-out skips the PPU in this mode to avoid
double-advancing.

The PPU runs `stepDot()` until its internal master clock reaches the
deadline. Each dot:
- Increments `dot`. At 341, wrap to dot 0 + bump scanline.
- At scanline 241 dot 1, set vblank flag + maybe-raise NMI level.
- At scanline 261 dot 1, clear vblank.
- At pre-render dot 339, latch the odd-frame skip from
  `renderingEnabledDelayed` (Mesen's 1-PPU-clock delayed render-enable
  state).
- At dot 340 of pre-render, if oddSkipArmed + odd frame, skip dot 340
  (NTSC odd-frame dot-skip).

### APU frame counter (6 internal sub-steps)

Mesen2 `ApuFrameCounter.h:19` encodes the NTSC frame counter as a
6-entry step table:

NTSC 4-step `_stepCyclesNtsc[0]`:
- step 0 fires at CPU cycle 7457 (quarter)
- step 1 at 14913 (quarter + half)
- step 2 at 22371 (quarter)
- step 3 at 29828 (IRQ assert, no tick)
- step 4 at 29829 (quarter + half + IRQ)
- step 5 at 29830 (IRQ, no tick, frame reset)

Total = 29830 CPU cycles per cycle.

nessy implements the same as `frameStepIntervalsNtsc4Step =
[6]int{7456, 7458, 7457, 1, 1, 7457}` — the per-step delay-until-next.
Sum = 29830.

5-step is the analogue without IRQ: `[6]int{7456, 7458, 7458, 7452, 1,
7457}`. Total = 37282 cycles.

### DMA state machine

The DMC and OAMDMA bus-steals run inside `chippy/cpu.ProcessPendingDma`
(in the chippy lib, not nessy). Peripherals flag intent:

- `OAMDMA.Write` → `cpu.SetNeedSpriteDma(page)` → CPU sets
  `spriteDmaTransfer=true, needHalt=true`.
- `dmcChannel.maybeRefill` → `cpu.SetNeedDmcDma()` → CPU sets
  `dmcDmaRunning=true, needHalt=true`.

On the next CPU.read where `needHalt` is set, ProcessPendingDma:
1. Halt cycle: dummy read at the opcode-fetch PC.
2. Loop while `dmcDmaRunning || spriteDmaTransfer`:
   - getCycle (`Cycles & 1 == 0`): DMC read OR sprite read OR alignment dummy.
   - putCycle: sprite write to $2004 OR alignment dummy.

The DMC fetch path uses `apu.GetDmcReadAddress()` + `apu.SetDmcReadBuffer()`
to bridge.

---

## 3. Conventions & workflow

### Branch & PR flow

- One issue → branch `feat/<short-name>` off `main`.
- Conventional Commits.
- Squash-merge with `--delete-branch`.
- Defer follow-ups by filing new issues, not by adding scope to in-flight
  PRs.

### Quality bar (must pass before commit)

- `go build ./...`
- `go test -race -count=1 ./...`
- `golangci-lint run ./...`
- `go test -tags=accuracy ./cmd/nessy/...` — full ROM suite.
- Persistence files (`~/.nessy/states/<rom-hash>-slot<N>.state`) follow
  the v1 freeze contract — new fields stay optional inside v1.x;
  semantic changes or removals require bumping the schema version + a
  migration.

### Code style

- No comments explaining WHAT well-named code already says. Only
  non-obvious WHY.
- No backwards-compat shims for in-tree code we control. Just change it.
- Prefer editing existing files over creating new ones.
- Keep the game loop responsive — every `Update` key path returns
  quickly.

---

## 4. Progress

### Shipped (carried forward from chippy)

The major accuracy + architecture work shipped while nessy was still
inside the chippy monorepo. See chippy's git log for the full thread;
the carve preserved that history. Highlights:

- **Per-cycle CPU↔PPU interleave** (chippy#342). For `VariantNES`,
  `cpu.Step` runs in 1:1 lockstep: every bus access ticks the chain one
  cycle before the access. PPU advances 3 dots per CPU cycle. /NMI
  becomes a level the PPU drives; CPU edge-detects in `sampleNMI`.
  Suppression race (vblank-flag clear in same cycle the line rises)
  falls out. Penultimate-cycle NMI poll via `nmiDue` + 1-cycle delay
  gives the 6502's 1-instruction NMI latency. `ppu_vbl_nmi` 5/10 → 9/10
  on landing.
- **Odd-frame dot-skip latch at dot 339** (chippy#367). Uses
  `renderingEnabledDelayed` (Mesen's 1-PPU-clock delayed render-enable
  state). Closed the last `ppu_vbl_nmi` sub-test → **10/10**.
- **Master-clock model** (chippy#375). NTSC 12 mc/CPU cycle, 4 mc/PPU
  dot, ppuOffset=1. CPU read/write splits the cycle's master-clock budget
  around the bus access (+5 pre / +7 post for read; +7 pre / +5 post
  for write) and calls `PPU.Run(deadline)` at each split. Mirrors
  Mesen2 `Start/EndCpuCycle`.
- **NMI hijack between push16(PC) and push(P)** (chippy#375). Mesen2
  ordering; cleared `cpu_interrupts_v2` test 3.
- **NTSC 4-step frame counter intervals**, originally 4-entry
  (chippy#377), promoted to 6-substep (chippy#380). Half-frame tick at
  cycle 29829 (Mesen step 4), not 29828. Sum 29830 per frame.
- **Mesen2 `ProcessPendingDma` port** (chippy#377). Replaced
  `cpu.Stall(513)` + per-cycle `StallStepper` with the
  cycle-parity-driven sprite/DMC interleave. cpu_interrupts_v2 test 4
  `irq_and_dma` passes; test 5 cleared by the frame-counter +
  branch-quirk combo.
- **Branch IRQ-poll quirk** (chippy#377). Taken non-page-cross branch
  ignores IRQ at its last clock. NMI not affected. Closed
  cpu_interrupts_v2 test 5 + the branch_delays_irq later sub-tests.
- **DMC buffer-fill + enable-fetch + $4015 read** (chippy#381). Three
  real-silicon DMC bugs fixed. $4015 read no longer clears DMC IRQ.
  apu_test 6/8 → 7/8.
- **Mesen-aligned DMC Clock** (chippy#382). 8 fires per byte (was 9),
  fire-to-fire = period (was period+1), schedule-fetch every clock.
  apu_test 7/8 → **8/8**.
- **Accuracy harness** (chippy#318). 7 ROMs wired, 6 PASS + 1 SKIP.
- **VRC7 OPLL FM synth** (chippy#315). Lagrange Point soundtrack plays.
- **Sunsoft 5B audio** (chippy#306). Gimmick! plays correctly.
- **VRC6 audio** (chippy#302). Castlevania III (JP) plays.
- **Sprite-overflow silicon bug** (chippy#283). Battletoads' pause
  trick works.
- **PAL / Dendy region support** (chippy#320). Per-region clock + PPU
  geometry + APU frame counter; NTSC is the default.
- **AOROM mapper** (chippy#360, mapper 7). Unlocked Battletoads,
  Marble Madness, R.C. Pro-Am.
- **MMC3 scanline-IRQ split-screen demo** (chippy#323). Top-half /
  bottom-half colour split driven by the mapper's A12-counted IRQ.
- **Per-cycle DMC sample-byte DMA contention with OAMDMA** (chippy#300).
  Subsumed by the full DMA port in chippy#377.

### Closed accuracy gaps (in chippy era; nessy now inherits)

- ppu_vbl_nmi 10/10.
- instr_timing PASS (including 8 unstable illegal opcodes with stable
  approximations: XAA/ANE, LXA, SHA/AHX × 2, SHY, SHX, TAS/SHS, LAS;
  KIL/JAM stay NOP-stubbed).
- cpu_interrupts_v2 5/5.
- apu_test 8/8.
- instr_misc 4/4.
- instr_test-v5_official 16/16.
- instr_test-v5 (all_instrs) — SKIP at $AB LXA (unstable illegal).

### Open issues

- **#1** — accuracy ROM suite (rolling tracker).
- **#2** — manual playtest matrix.
- **#3** — nesdev wiki ROM catalog (rolling).
- **#13** v1.0 epic and its sub-issues (#4-#12).

---

## 5. Key decisions & rationale

### Architecture

- **Per-cycle interleave only for `VariantNES`.** chippy keeps the
  instruction-stepped batch tick for NMOS / 65C02 so the chippy
  debugger + Klaus functional tests + BCD sweeps run byte-identically.
  Per-cycle is opt-in via the variant + ticker wiring.
- **Mesen2 as reference implementation.** When in doubt about a
  cycle-precision detail, Mesen2's `Core/NES/` is the source of truth.
  The PR thread carries explicit line references to Mesen sources for
  every load-bearing borrow.
- **`stallTick` retained for Reset only.** chippy's pre-PR-#377 stall
  drain is gone, but the per-cycle clock advance helper is still used
  by `cpu.Reset`'s 8-cycle warmup so the APU $4017 reset delay is
  primed.

### Save-state format

v1 freeze. JSON for the schema with `schemaVersion: 1`. New fields stay
optional inside v1.x; semantic changes or removals require bumping
`StateSchemaVersion` + writing a migration. The CPU's `pendingStall`
field stays in the JSON even though the live emulator no longer touches
it — backward compat with v1.x save files predating chippy#377.

### Audio mixing

Current mixer is a linear sum + scale. Per-channel scale factors
empirically tuned so peak multi-channel output sits within int16 range
without clipping. Real silicon uses non-linear DAC LUTs per nesdev's
"APU Mixer" — replacement tracked under #5. The current approximation
is "directionally correct + audibly OK" but doesn't pass `apu_mixer`
ROM if it has a programmatic check.

---

## 6. Critical context

### Build tag

`cmd/nessy*` files use `//go:build nessy`. Inherited from the chippy
monorepo (where the tag isolated Ebiten from chippy's default build).
Post-carve the tag isn't strictly required, but stripping it would
churn ~100 files for no functional benefit. Leave it.

### Common pitfalls

- **Save-state schema bumps require a migration.** Adding a field to
  `cpu.FullState` or any nessy-side persisted struct is fine inside
  v1.x as long as the field is optional. Removing or repurposing a
  field requires a major bump.
- **PPU `cpuDriven` flag must be true** for the per-cycle interleave
  to work. Wired in `cmd/nessy/wiring.go` after PPU registration.
- **`processor.SetPPURunner(pp)` must be called** after both the CPU
  and PPU exist. The constructor order in wiring.go matters.
- **`processor.SetDMCFetcher(ap)`** must be called for DMC fetches to
  decrement bytesRemaining. Without this hook the CPU's ProcessPendingDma
  has nowhere to push the fetched byte back.
- **Frame-counter $4017 write delay is 3 or 4 CPU cycles** based on
  cycle parity. Polarity matches Mesen via `dbgCycles` parity (chippy
  era #372). Don't flip without a Mesen-side cross-check.
- **OAMDMA's old `OAMDMA.Step` is gone.** Don't try to drive the DMA
  from outside the CPU; the state-signal model is the only path.

### Chippy version pinning

`go.mod` requires `github.com/nkane/chippy vX.Y.Z`. Latest is v1.2.0
(the carve baseline). Bumping to a future chippy version pulls in any
new CPU core fixes — but chippy's public API is semver-stable, so this
should always be safe within a major.

If chippy makes a breaking CPU-core change (major bump), nessy needs
matching code updates. The CPU surface nessy depends on is documented
in `CLAUDE.md` (Chippy library dependency section).
