# nessy — Claude instructions

Go-based NES emulator (Ebiten, cycle-accurate 2A03 / 2C02 / 5B / VRC6 /
VRC7). Built on the [chippy](https://github.com/nkane/chippy) 6502 CPU +
DAP debugger library, pinned via `go.mod`. Carved out of the chippy
monorepo at chippy v1.2.0 on 2026-05-31 (chippy#386).

## Authorship

- I am the sole author. **Do not** add `Co-Authored-By: Claude` trailers
  to commit messages.
- **Do not** add "🤖 Generated with Claude Code" footers to PR bodies.

## Branch & PR flow

- One GitHub issue → branch `feat/<short-name>` off `main`.
- Conventional Commits: `feat:`, `fix:`, `docs:`, `ci:`, `test:`,
  `refactor:`, `chore:`.
- PR body ends with `Closes #N`.
- Squash-merge with `--delete-branch`. Defer follow-ups by filing new issues.

## Release tag scheme

- nessy ships under bare `vX.Y.Z` tags. The `nessy-v*` prefix used in the
  chippy monorepo was renamed at carve-out (`git filter-repo --tag-rename
  nessy-v:v`).
- Last released: `v0.8.0` pre-carve. v0.9.x / v1.0.0 to follow under the
  v1.0 epic (#13).
- **Release pipeline (#8) — DONE, hand-rolled** in
  `.github/workflows/release.yml` (NOT goreleaser). chippy cross-compiles
  every target on one runner with goreleaser because it's pure-Go
  (`CGO_ENABLED=0`); **nessy is an Ebiten CGO app** — it can't
  cross-compile from one runner, and goreleaser OSS can't ingest
  externally-built CGO binaries (the `prebuilt` builder was removed,
  split/merge is Pro-only). So the workflow builds each target **natively
  in a per-OS matrix** (macos-latest+macos-13, ubuntu-latest+ubuntu-24.04
  -arm, windows-latest), then a finalize job produces the same artifact
  shape as chippy: archives + `checksums.txt`, keyless-cosign
  `.cosign.bundle` on every artifact, syft SPDX SBOMs, `.deb`/`.rpm`/`.apk`
  via `nfpm` (`packaging/nfpm.yaml`, with the GL/X11/ALSA runtime deps the
  CGO binary needs), Homebrew cask → `nkane/homebrew-tap`, and AUR
  `nessy-bin`. Homebrew + AUR run on **stable tags only** (a `-` in the tag
  = prerelease → skipped, so an `-rc` tag is a secret-free smoke test).
  Version stamped via `-X main.version/commit/date` (`-version` flag on
  both `nessy` + `nessy-record`). Playbook: `docs/RELEASE.md`. Stable
  releases need the `HOMEBREW_TAP_GITHUB_TOKEN` + `AUR_SSH_PRIVATE_KEY`
  repo secrets.

## Docs are part of every PR

Every PR ships with the documentation changes its diff implies. Update
**in the same PR**, never as a separate cleanup:

- **`README.md`** — when controls, install instructions, demos, save-state
  semantics, DAP attach flow, or any user-facing behavior change.
- **`docs/`** — `install.md`, `debugging.md`, `demos.md`, `index.md`.
- **This file (`CLAUDE.md`)** — when load-bearing invariants change.
- **Code-level doc comments** — when an exported type / function changes
  shape or contract.

## Module structure

```
cmd/
  nessy/          — Ebiten game binary, DAP server, save state, joypad
  nessy-record/   — headless GIF / MP4 recorder (deterministic capture)
  nessy-wasm/     — js/wasm playground build
internal/nes/
  apu/            — 2A03 audio (pulse / triangle / noise / DMC) +
                    Sunsoft 5B, VRC6, VRC7 OPLL extension chips
  cart/           — iNES + NES 2.0 parsing, every supported mapper
  dma/            — $4014 OAMDMA peripheral (state-signal model)
  ines/           — header parse + region detection
  joypad/         — $4016/$4017 strobe + serial shift
  ppu/            — 2C02 / 2C07 (NTSC / PAL / Dendy) + per-cycle
                    interleave + sprite-overflow silicon bug
  timing/         — region clocks + frame geometry
roms/demos/       — hand-rolled ca65 demos doubling as regression tests
web/              — WASM playground
test/             — recorder smoke tests
docs/             — install / debugging / mapper compat
```

## Build tag

`cmd/nessy*` files use `//go:build nessy`. This was a chippy-monorepo
isolation artifact — the tag still works post-carve and the build chain
expects it. Default `go build ./...` skips Ebiten-dependent files; full
build is `go build -tags=nessy ./...`.

## Load-bearing invariants (don't break without flagging)

These are the cycle-precision details that took the most work to land
and would silently break dozens of ROMs if regressed. The work crossed
chippy + nessy when they were a single repo; the implementations now
straddle the two:

### chippy-side (in the `github.com/nkane/chippy/cpu` dep)

- **Per-cycle CPU↔PPU interleave** (chippy#342). `cpu.Step` runs in 1:1
  lockstep for `VariantNES`: every bus access ticks the chain one
  cycle, with addressing-mode dummy reads added per template (`c.idle`,
  `addrDummies`).
- **Master-clock model** (chippy#375). NTSC 12 mc/CPU cycle, 4 mc/PPU
  dot, ppuOffset=1. Read = +5 pre / +7 post mc; write = +7 pre / +5 post.
- **NMI hijack check** is BETWEEN `push16(PC)` and `push(P)` in
  `serviceVector` — Mesen2 `NesCpu::IRQ` order. Cleared the `nmi_and_irq`
  test 3 of cpu_interrupts_v2.
- **NMI / IRQ poll latches** sample at the penultimate cycle via
  `nmiPollPrev` / `irqPollPrev` one-cycle delay. `cli_latency`,
  `nmi_and_brk` depend on this.
- **Branch IRQ-poll quirk** (chippy#377): a taken non-page-cross branch
  ignores IRQ asserted at its last clock. `branch()` rolls back
  `irqPollPrev` if it just rose this cycle. Mesen
  `NesCpu::BranchRelative`.
- **Mesen2 `ProcessPendingDma` port** (chippy#377). OAMDMA + DMC fetches
  drain via `cpu.ProcessPendingDma` inside `CPU.read` at opcode fetch
  when `needHalt` is set. Halt cycle = dummy read at K+1's PC; loop on
  cycle parity (sprite reads on getCycle, writes on putCycle, DMC reads
  merged, alignment dummies).
- **`serviceNMI` bug class**: clear the NMI edge latch BEFORE the 7
  service cycles or a spurious second NMI fires.

### nessy-side (in this repo's `internal/nes`)

- **PPU `renderingEnabled` has a 1-PPU-clock delay** (chippy#375). The
  odd-frame dot-skip latch at pre-render dot 339 uses
  `renderingEnabledDelayed` (synced at end of each stepDot from the
  live mask), NOT the live mask. Mesen comment in `NesPpu::UpdateState`:
  "Rendering enabled flag is apparently set with a 1 cycle delay". This
  cleared the last `ppu_vbl_nmi` sub-test (10/10).
- **NTSC 4-step frame counter intervals** are NON-UNIFORM and total 29830
  CPU cycles per frame, NOT 4*7457=29828.
  `frameStepIntervalsNtsc4Step = [6]int{7456, 7458, 7457, 1, 1, 7457}`.
  The user-visible "step 3" spans 3 CPU cycles (29828, 29829, 29830);
  the half-frame tick fires at cycle 29829 (Mesen step 4). 5-step
  analogue: `[6]int{7456, 7458, 7458, 7452, 1, 7457}`. Source:
  Mesen `ApuFrameCounter.h:19`.
- **DMC Clock matches Mesen exactly** — 8 fires per byte (not 9),
  fire-to-fire = period (not period+1), schedule-fetch check runs every
  clock (not just at boundaries). `bitsRemaining` initialises to 8.
  Reload to `period - 1` so the down-counter ticks `period` cycles
  between fires (matches Mesen `ApuTimer::Run` which advances by
  `_timer + 1`).
- **DMC `$4015` read does NOT clear the DMC IRQ flag** — only frame
  counter IRQ is cleared on `$4015` read. DMC IRQ acks via `$4015`
  write (any value) or `$4010` bit-7 clear. Per nesdev + Mesen
  `NesApu.cpp:101`. Blargg apu_test 7-dmc_basics test 10 pins this.
- **DMC `maybeRefill` silences only when buffer empty AND
  bytesRemaining==0** — not just on buffer-empty boundaries. Otherwise
  schedule a fetch.
- **DMC enable schedules an initial fetch** when buffer is empty +
  bytes pending (Mesen `StartDmcTransfer` condition). Inits with
  `bufferEmpty=true, silenced=true`. The fetch is CYCLE-DELAYED
  (`transferStartDelay` 2/3 by CPU parity, `processDelays`, #20) — a
  `$4015` enable loads `bytesRemaining` (so bit 4 reads active) at once
  but defers the DMA flag 2-3 cycles; a `$4015` disable defers zeroing
  `bytesRemaining` by `disableDelay`. Matches MesenCE; see the #20 note.

- **Per-dot rendering is the SOLE path** (epic #73, phase 4 #77). The
  PPU reproduces the 2C02 fetch pipeline one dot at a time (`perdot.go`):
  8-dot tile fetch → 16-bit shift registers, draw-then-shift, the 321-336
  prefetch, sprite eval+fetch in the 257-320 window, dot-exact sprite-0
  hit, and the buggy sprite-overflow flag (`evaluateSpriteOverflow`,
  called from `prepareSpritesFor`). The batched `renderFrame` /
  `renderScanline` / `compositeScanlineSprites` walk + the scroll-event
  log (`scrollEvents` / `recordScrollChange` / `frameStartScroll`) are
  GONE — scroll falls out of `v`/`x` per dot. MMC5 extended attributes +
  CHR-bank context (`setCHRContext`) are handled inside the per-dot BG
  fetch; rendering-disabled visible dots paint the backdrop
  (`renderBackdropPixel`).

- **MMC3 A12 clocks from REAL per-dot fetch addresses** (#16, #76). With
  per-dot rendering, A12 follows the actual fetch bus: BG fetches at
  `$0xxx`, sprite + garbage fetches at `$1xxx` (the empty sprite slots
  still read `sprPatternBase` every scanline, so A12 toggles even with no
  sprites in range). `prepareSpritesFor` runs on EVERY rendering scanline
  including line 239 (its fetches target the off-screen "line 240") so
  A12 clocks all 241 rendering scanlines. Off-render, the PPU still drives
  `v` onto the bus via `NotifyVRAMAddr` on the $2006 second write + the
  non-rendering $2007 increment (Blargg mmc3_test 1/3).
- **MMC3 A12 low-time filter** (Mesen `A12Watcher`, `minDelay=10` PPU
  dots). A rising edge only clocks the IRQ counter once A12 has stayed
  low for >10 dots — without it the per-dot pattern fetches (A12 high on
  every `$1xxx` fetch, low only for the NT/AT reads between) would
  over-clock the counter ~32×/scanline. The PPU pushes its running dot
  count (`p.dots`) to `MMC3.SetA12Cycle` before each CHR fetch +
  PPUADDR-driven bus change (optional `a12Cycler` interface); `clockA12`
  accumulates the low stretch. Net: exactly **241 A12 clocks/frame**
  (mmc3_test 2 "details" #7). Closes mmc3_test 1/2/3/5. The remaining 4/6
  need the deferred $2006 v-update (#25) — see below.

### #25 — mmc3_test 4 + 6 DONE (full mmc3_test suite passes)

Both remaining sub-tests now pass; the whole Blargg mmc3_test 1-6 suite
is green. Two earlier hypotheses were FALSIFIED before the real causes
were found by an A12 edge trace (env-gated logger in `MMC3.clockA12`).
Recorded so nobody re-walks them:

1. **Deferred $2006/$2007 v-commit** (a Mesen `UpdateState` port) —
   moved neither test, regressed the mmc3-split demo, reverted.
2. **Per-dot fetch-A12 "over-clocking"** — the trace showed ONE A12
   rise per scanline (timing correct), not multiple. Not over-clocking.

**test 6 (MMC6) — DONE.** It is a pure MMC3 IRQ-counter revision issue.
The 2C02 fetch A12 timing was already correct (1 rise/scanline). The
trace's spurious fires were the rev-B counter firing on a STUCK-AT-ZERO
clock (counter already 0, no reload flag → reload 0→0 → fire). mmc3_test
5 (rev-B, SMB3/MM3 silicon) wants exactly that; mmc3_test 6 (rev-A,
Crystalis/MMC6 silicon) wants it SILENT. The two ROMs are **byte-
identical in their iNES headers** (mapper 4, no submapper), so the chip
revision cannot be read from the header — it is resolved by content hash
(`mmc3RevAHashes` in `cart/mmc3.go`), mirroring Mesen's game database
(chip == "MMC3A"). The fix: (a) `clockA12RevA` now matches Mesen —
`(count > 0 || reloadFlag) && newCount == 0` (was an inverted
`!preReload` test); (b) `NewMMC3` sets `revA` from the PRG‖CHR hash.
mmc3_test 1/2/3/5/6 all PASS; the `mmc3-split` demo (rev-B) is unchanged.

**test 4 (scanline_timing #2) — DONE.** "Scanline 0 IRQ should occur
later when $2000=$08" ($2000=$08 puts sprite patterns at $1000). This
one IS about the exact dot the sprite-fetch A12 edge rises. nessy batches
the sprite fetch into a single stepDot; it was at dot 257, so the A12
rise was ~4 dots early. The 2C02 emits the first sprite PATTERN fetch at
dot 261 (slot-0 phase 4, after the slot's two $2xxx garbage NT fetches —
MesenCE `LoadSpriteTileInfo`). Fix: run the batched fetch at
`spriteFetchDot = 261` (`perdot.go`) instead of 257. This shifts only the
A12-edge DOT, not the per-scanline count (still one filtered rise), so
mmc3_test 1/2/3/5 + `a12_test` are unaffected; the `mmc3-split` demo's
IRQ split moves down ~1 row (more accurate — its golden was regenerated).
The sprite pattern DATA is still fetched in one batch (rendering reads it
next scanline regardless); only the A12 emission dot matters here. Fully
spreading the 8 sprite slots across 257-320 is unnecessary for this test.

### #20 — dmc_dma_during_read4: ROOT-CAUSED + FIXED (staged behind a chippy release)

`dma_2007_read.nes` has NO `$6000` shell and no zero-page result byte. It
spins in per-sub-test calibration loops (e.g. PRG `$E06D-$E076`: countdown
delay → `STA $4015` enable 1-byte DMC → `NOP` → `BIT $4015` → `BNE`) that
only ESCAPE — to a final `$E72F-$E735` `SEI / STA $2000 / JMP *` terminal
hang — when the DMC-DMA steal lands on the cycle-correct CPU cycle so its
glitch makes the poll read bit 4 clear. Wrong timing ⇒ the loop period
locks to the DMC sample period (zero phase drift) ⇒ spins forever.

**Root cause (chippy), found by a from-boot `(PC, cycle)` diff vs a
headless MesenCE reference** (built `make core`/`pgohelper`; per-instruction
trace hook on `NesCpu::Exec`). chippy + MesenCE were **bit-identical for
62,741 instructions**, then diverged at one steal: chippy halted at the
branch *target* (`$E062`, even cycle → 3-cycle steal), MesenCE on the
taken-branch *dummy read* (`$E078`, odd → 4-cycle steal). chippy's
`idle()` — used for the taken-branch dummy read (`branch()` →
`c.idle(c.PC)`) and other dummy cycles — did **not** poll
`ProcessPendingDma` the way `busRead` does, so a DMA halt armed into an
idle cycle drained one cycle late. The earlier "cumulative cycle-parity
offset" theory was FALSIFIED by the bit-identical prefix — it is a local
halt-poll-point bug.

**Fix — chippy #493 / PR nkane/chippy#497:** `idle()` polls
`ProcessPendingDma` when `needHalt` is set, mirroring `busRead` (gated by
`nesCycle`, so Harte/Klaus/non-NES are untouched). Regression test
`TestIdle_DrainsPendingDmaHalt`. With it, `dma_2007_read` reaches the same
`$E72F` terminal as MesenCE.

**nessy side (`cmd/nessy/dmabus.go`, `accuracy_test.go`):**
- `dmaBus` wraps `*cpu.MMIO` (embeds it; `Read`/`Write` latch external open
  bus, `ReadDma` added) installed via `processor.SetBus(...)` in
  `buildNES`. Pure pass-through off the DMA path. `processDmcRead` ports
  MesenCE `NesCpu::ProcessDmaRead` (halt `DmaDummyRead` captures the CPU
  pending-read addr into `haltAddr`; on the `DmaDmcRead`, a `$4000-$401F`
  halt redirects the fetch to `$4000 | (dmcAddr & $1F)`). Unit-tested
  (`dmabus_test.go`).
- DMC `setEnabled` parity fix (`apu.go`): `dbgCycles&1==1` (Mesen-even ⟺
  `dbgCycles` odd, matching `SetFrameCounter`); DMC sample-timeline phase
  (`timer` inits to `period-1`) — both in nessy PR #94.
- **Grader**: no `$6000`, no result byte → `runTerminalLoop`
  (`terminalLoop: [2]uint16{0xE72F, 0xE735}`). Pass = the CPU settles into
  the terminal window (validated cycle-for-cycle against MesenCE);
  frame-cap exhaustion = still spinning = fail.

**Status: PASSES against local chippy `main` + #497.** Staged behind a
**temporary `go.mod` replace → `/Users/nkane/dev/chippy`** (the `idle()`
fix is unreleased). To finish: merge chippy #497 → chippy release (v1.8.0)
→ drop the `replace`, bump `require` to the tag. The accuracy row is
already green; no knownFail.

## Accuracy harness

Live tracker: [#1](https://github.com/nkane/nessy/issues/1). Wire ROMs
into `cmd/nessy/accuracy_test.go` (`accuracyROMs` slice). CI accuracy
job downloads + runs.

| ROM | Result | Notes |
|---|---|---|
| ppu_vbl_nmi.nes | 10/10 PASS | hard gate |
| instr_timing.nes | PASS | |
| cpu_interrupts_v2.nes | 5/5 PASS | cli_latency, nmi_and_brk, nmi_and_irq, irq_and_dma, branch_delays_irq |
| apu_test.nes | 8/8 PASS | len_ctr, len_table, irq_flag, irq_timing, len_timing, irq_flag_timing, dmc_basics, dmc_rates |
| instr_misc.nes | 4/4 PASS | abs_x_wrap, branch_wrap, dummy_reads, dummy_reads_apu |
| instr_test-v5_official.nes | 16/16 PASS | every official opcode × every addressing mode |
| instr_test-v5.nes (all_instrs) | SKIP | test 3 fails at $AB LXA/ATX — unstable illegal, analog-noise dependent |
| mmc3_test 1/2/3/4/5 | PASS | clocking, details (incl #7 "241 clocks/frame"), A12_clocking, scanline_timing (sprite-fetch A12 at dot 261), MMC3 (rev-B) |
| mmc3_test 6 | PASS | MMC6 — rev-A IRQ counter (stuck-at-zero stays silent), selected by content hash since the header matches the rev-B test 5 ROM (#25) |
| sprite_overflow_tests 1.Basics | 8/8 PASS | no $6000 shell — graded via `runParkedResult` on zero-page result $F8 (1=pass). test 7 ($2001=$08, BG-only) pins that sprite eval/overflow runs when BG OR sprites enabled (#19) |
| ppu_open_bus | 11/11 PASS | per-bit open-bus DRAM decay (`setOpenBus`/`applyOpenBus`, decay >3 frames) + the OAM attribute-byte (sprite byte 2) bits-2-4-read-0 quirk (#17) |
| oam_read / oam_stress | PASS | $2003/$2004 OAMADDR/OAMDATA access; oam_stress's random R/W also leans on the attribute-byte bits-2-4-read-0 mask (#18, closed by #17) |
| dmc_dma_during_read4 (dma_2007_read) | PASS | no $6000 / no result byte — graded via `runTerminalLoop` on the $E72F-$E735 terminal hang (validated vs MesenCE). chippy v1.8.0 `idle()` polls ProcessPendingDma → taken-branch dummy-read DMA halt → 4-cycle steal (#493/#497) + the host `dmaBus` glitch formula (#20) |
| sprite_hit_tests 2005 (01.basics) | PASS | same generation as sprite_overflow — no $6000, parks with zero-page $F8 result (1=pass) → `runParkedResult` (#21) |
| cpu_timing_test6 | PASS | visual-only — prints "6502 TIMING TEST / OFFICIAL INSTRUCTIONS ONLY / PASSED" to the nametable, then parks. Graded via `runScreenText`: decode the nametable ($2006/$2007, blargg font tile==ASCII), pass iff it contains "PASSED" (#21) |
| mmc5test_v2 (Drag) | PASS | MMC5 (#6). Interactive — no $6000 / result byte / verdict text; boots to the "MMC5 CHR BANK TEST" menu + waits for input. Graded via `gradeScreenGolden`: step a fixed 450 frames, encode the framebuffer as an ascii-ramp grid (`asciiFrame`, shared with the demo-ascii goldens), diff vs `testdata/accuracy-screen/mmc5test_v2.nes.golden`. Frame-stable from frame 400. Regression gate for PRG-exec + CHR banking + nametable mapping + font render — the whole MMC5 PPU-integration surface (phases 1-2d, #57/#67-70; audio #56/#71). Regen the golden with `-asciiref-update` |

The `instrCycles == accounted` panic in `cpu.Step` is a proven invariant
guard — if it fires, a dummy-cycle template is wrong.

`knownFail` string on a ROM = tracked gap; harness logs the status +
skips so the existing PASS suite stays green. Real regression in a
passing ROM still fails CI.

Five grading paths in `accuracy_test.go`: the default `runBlargg` polls
the `$6000` status shell; `screenPass` ROMs use `runScreenText` (visual-
only ROMs that print a verdict to the nametable — decode tiles as ASCII,
pass on "PASSED"; cpu_timing_test6, #21); `terminalLoop` ROMs use
`runTerminalLoop` (self-calibrating ROMs with no `$6000`/result byte that
converge to a fixed terminal hang — dma_2007_read, #20); ROMs with a
`resultAddr` set (the older sprite_overflow / sprite_hit / dmc_dma
generation that reports by screen + APU beeps, no `$6000`) use
`runParkedResult` — run to the CPU's tight self-loop park, then read the
zero-page result byte (1 = passed, else the failed sub-test number); and
`screenGolden` ROMs use `gradeScreenGolden` (interactive ROMs with NO
self-report at all — they boot, render a static screen, wait for input)
— step a fixed `maxFrames`, encode the framebuffer with `asciiFrame`, and
diff vs a committed `testdata/accuracy-screen/<name>.golden` (regen with
`-asciiref-update`); a picture divergence is a rendering regression
(mmc5test_v2, #6).

**Blargg `apu_mixer` is deliberately NOT in the suite (#5).** Its four
ROMs (square/triangle/noise/dmc) cancel the channel-under-test against the
DMC DAC to near silence and have NO programmatic `$6000` verdict — they
print play/listen instructions and report "done" (status `$00` =
*completed*, not *passed*), so a human ear is the oracle. Wiring them
would falsely green regardless of mixer correctness. The non-linear-DAC
behaviour they exercise is deterministic at `mixSample`, so it's pinned
instead by property unit tests in `internal/nes/apu/mixer_test.go`
(square cross-attenuation, tnd cross-attenuation, pulse/tnd group
independence). The non-linear DAC itself shipped pre-#5 (`mixer.go` +
`emitSample`); expansion chips (5B/VRC6/VRC7/MMC5) sum as post-DAC int16
addends because they mix on the cart's analog out, not through the 2A03
DAC — see the `emitSample` comment.

## v1.0 release epic

[#13](https://github.com/nkane/nessy/issues/13) tracks the v1.0 work
across these sub-issues:

| # | Title |
|---|---|
| #4 | complete the accuracy ROM suite (sprite_hit, sprite_overflow, dmc_dma, mmc3_test, ppu_open_bus, oam_stress) |
| #5 | non-linear DAC mixer |
| #6 | MMC5 mapper |
| #7 | headliner playtest pass |
| #8 | release pipeline — cosign + homebrew + AUR |
| #9 | docs — install, debugging, mapper compat matrix |
| #10 | WASM playground UX |
| #11 | perfgate — sustained 60 fps on Intel-class CPUs |
| #12 | sprite-0 hit + sprite overflow accuracy |

Rolling trackers (carried over from chippy):
- #1 nesdev test ROM suite integration
- #2 manual playtest matrix
- #3 nesdev wiki manual ROM catalog

## Chippy library dependency

`go.mod` pins `github.com/nkane/chippy v1.5.0` (or later). Bumping the
chippy version pulls in new CPU core fixes; the public packages used:

- `chippy/cpu` — the 6502 / 2A03 implementation, `cpu.Variant{NMOS, NES,
  CMOS65C02}`, `cpu.ProcessPendingDma`, `cpu.SetNeedSpriteDma`,
  `cpu.SetNeedDmcDma`.
- `chippy/dap` — the DAP server used by `cmd/nessy/dap.go` for the game-side
  `chippy -dap-attach` workflow. `AttachConfig.CustomRequestHandler`
  (v1.4.0) backs nessy's `nessy/*` debug requests (#28+).
- **Debugger host hooks (v1.5.0, chippy#419)** — for the remaining
  debugger panels: `cpu.SetAccessHook` + `cpu.AccessKind` (memory access
  heatmap, #41), `cpu.RAM.Freeze`/`Unfreeze`/`Frozen` (freeze, #41),
  `dap.Server.SetHostVars` (NES-aware breakpoint conditions) +
  `SetStopPredicate` (run-to-NMI / step-scanline, #33).
- `chippy/peripheral` — `peripheral.Peripheral` interface for MMIO.
- `chippy/symbols` — symbol table for source-level debugging.
- `chippy/trace` — execution trace.
- `chippy/loader` — ROM loader.
- `chippy/expr` — DAP watch expression parser.

Public API surface stability: chippy uses semver. Pin a tagged version;
breaking changes show up as major bumps.

## When in doubt

- Ask before destructive git ops (force-push, reset --hard, branch -D).
- Ask before scope creep — bug fixes don't need cleanup; one-shot tasks
  don't need helpers.
- The accuracy harness is the safety net. Run
  `go test -tags=accuracy ./cmd/nessy/...` before claiming an accuracy
  fix landed.

## Reference emulator: MesenCE (was Mesen2)

The cycle-precision source of truth is Mesen's `Core/NES/`. As of
2026-06-16 the reference is **MesenCE**
([nesdev-org/MesenCE](https://github.com/nesdev-org/MesenCE)) — the
community-managed fork that succeeds the wound-down
[SourMesen/Mesen2](https://github.com/SourMesen/Mesen2). MesenCE is a
fork with the same `Core/NES/` file + line layout, so every existing
`Mesen2 Core/NES/...` source-line reference in this file and the ADRs
stays valid. **New cycle-precision work cites MesenCE; existing "ported
from Mesen2" notes are accurate provenance and stay as written.**

## How this code came to be

The accuracy + sub-cycle ordering work landed across many chippy PRs
between 2026-05-15 and 2026-05-31, ported from Mesen2
([SourMesen/Mesen2](https://github.com/SourMesen/Mesen2), now succeeded
by [MesenCE](https://github.com/nesdev-org/MesenCE)) (`Core/NES/`).
Notable references:

- `Core/NES/NesCpu.cpp:325-447` — `ProcessPendingDma` (chippy#377).
- `Core/NES/NesPpu.cpp:553-582` — `SetMaskRegister` deferred state
  (chippy#375).
- `Core/NES/NesPpu.cpp:946-955` — odd-frame dot-skip.
- `Core/NES/APU/ApuFrameCounter.h:19` — 6-substep frame counter table
  (chippy#380, ported to nessy in the same PR).
- `Core/NES/APU/DeltaModulationChannel.cpp:119-164` — DMC `Run`/`Clock`
  pattern (chippy#382).
- `Core/NES/APU/NesApu.cpp:97-105` — `$4015` read clears frame-counter
  IRQ ONLY (chippy#381).
