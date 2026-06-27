//go:build accuracy

// Accuracy suite: runs nesdev / Blargg test ROMs through the full
// iNES → cart → MMIO → CPU + PPU + APU integration and checks the
// documented pass/fail signal. Build-tagged `accuracy` so the
// general suite stays fast + offline; CI runs it in a dedicated job.
//
// Blargg's test ROMs follow a fixed protocol:
//   - $6000 holds a status byte: $80 while running, $81 = "press
//     reset", and a value < $80 when finished (0 = pass, else a
//     fail code).
//   - $6001-$6003 hold the magic $DE $B0 $61 once the test has
//     started writing status (so we don't trust $6000 before then).
//   - $6004+ is a null-terminated ASCII result string.
//
// ROMs are downloaded + cached + SHA-pinned on first run (mirrors
// nestest_test.go). Override a ROM with its *_BIN env var to point
// at a local copy.
//
// Run with:
//
//	go test -tags=accuracy -timeout 5m -run TestAccuracy -v ./cmd/nessy/...
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nkane/nessy/internal/nes"
)

// accuracyCyclesPerFrame mirrors cmd/nessy's per-frame budget; the
// game.go const lives behind the `nessy` build tag, invisible under
// `accuracy`, so it's duplicated here.
const accuracyCyclesPerFrame = 29830

// accuracyROM describes one test-ROM fixture + the conditions under
// which it counts as passing.
type accuracyROM struct {
	name      string // cache filename + sub-test label
	url       string
	sha       string
	pathEnv   string // env override for a local copy
	maxFrames int    // step cap (frame = accuracyCyclesPerFrame cycles)
	// knownFail, when non-empty, marks a ROM that currently exposes a
	// real accuracy gap. The harness still runs it + logs the result,
	// but a non-zero status is reported as a tracked-gap skip instead
	// of a hard failure so CI stays green. Clear the field (+ delete
	// the tracking issue) once the gap is fixed.
	knownFail string
	// resultAddr, when non-zero, grades a ROM that does NOT use the
	// Blargg $6000 status shell — the older sprite_overflow_tests /
	// dmc_dma style that reports by on-screen text + APU beeps and then
	// parks in a tight self-loop. The harness runs to the park and reads
	// this zero-page result byte: 1 = passed, any other value = the
	// failed sub-test number (Blargg's beep convention). Mutually
	// exclusive with the $6000 path.
	resultAddr uint16
	// screenPass, when true, grades a visual-only ROM that prints its
	// verdict to the PPU nametable as text (blargg's pre-$6000 generation
	// whose font maps tile index == ASCII): cpu_timing_test6 shows
	// "6502 TIMING TEST / OFFICIAL INSTRUCTIONS ONLY / PASSED". The
	// harness runs to the CPU's park, decodes the nametable via
	// $2006/$2007, and passes iff the screen text contains "PASSED" (and
	// not "FAILED"). Mutually exclusive with the other paths (#21).
	screenPass bool
	// terminalLoop, when non-zero, grades a self-calibrating ROM that has
	// neither a $6000 shell nor a zero-page result byte (dmc_dma_during
	// _read4's dma_2007_read): it spins in per-sub-test calibration loops
	// that only ESCAPE to a final terminal hang when the timing under
	// test is cycle-correct, otherwise looping forever. Pass = the CPU
	// settles into the [lo, hi] terminal PC window. The window is the
	// $E72F-$E735 `SEI / STA $2000 / JMP *` hang, validated cycle-for-
	// cycle against MesenCE (#20 / chippy #493). Mutually exclusive with
	// the other two paths.
	terminalLoop [2]uint16
}

var accuracyROMs = []accuracyROM{
	{
		name:      "ppu_vbl_nmi.nes",
		url:       "https://github.com/christopherpow/nes-test-roms/raw/master/ppu_vbl_nmi/ppu_vbl_nmi.nes",
		sha:       "8dbab1be785585c399cf055ef02147b788ab75fd80e81cf9568a2feafc03fb7d",
		pathEnv:   "CHIPPY_ACCURACY_VBL_NMI_BIN",
		maxFrames: 2200, // ~37 s emulated; the suite finishes near frame 1550
		// Full pass (10/10) after the per-cycle CPU↔PPU rewrite (#342).
	},
	{
		// Per-instruction cycle timing (branches, page-cross, RMW, etc.).
		// Validates the per-cycle CPU model end-to-end (#318, #342).
		name:      "instr_timing.nes",
		url:       "https://github.com/christopherpow/nes-test-roms/raw/master/instr_timing/instr_timing.nes",
		sha:       "3d1bca14266f1e25b75a34ddd29c9df1ce9c6d990c8663a218f72e7861660fb0",
		pathEnv:   "CHIPPY_ACCURACY_INSTR_TIMING_BIN",
		maxFrames: 2400,
	},
	{
		// IRQ/NMI/BRK interaction + interrupt latency. Full 5/5
		// PASS as of #377 (cli_latency, nmi_and_brk, nmi_and_irq,
		// irq_and_dma, branch_delays_irq) — see #369/#376.
		name:      "cpu_interrupts_v2.nes",
		url:       "https://github.com/christopherpow/nes-test-roms/raw/master/cpu_interrupts_v2/cpu_interrupts.nes",
		sha:       "ccbac4e824eb96ecfe8b82d331a083be186eb6776aa57e25c52251eaf7df9c4f",
		pathEnv:   "CHIPPY_ACCURACY_INTERRUPTS_BIN",
		maxFrames: 2400,
	},
	{
		// Blargg apu_test — 8 sub-tests of APU behavior (len_ctr,
		// len_table, irq_flag, irq_timing, len_timing, irq_flag_
		// timing, dmc_basics, dmc_rates). Full 8/8 PASS after the
		// 6-substep frame counter (#380), DMC enable-fetch + $4015
		// read (#381), and Mesen-aligned DMC Clock pattern.
		name:      "apu_test.nes",
		url:       "https://github.com/christopherpow/nes-test-roms/raw/master/apu_test/apu_test.nes",
		sha:       "00d4722bae1c82a14528dd3220462d3fb9ce4b14b8cec996619dea23e07fef0a",
		pathEnv:   "CHIPPY_ACCURACY_APU_TEST_BIN",
		maxFrames: 3000,
	},
	{
		// Blargg instr_misc — 4 sub-tests: abs_x_wrap (LDA absX
		// wrapping past $FFFF), branch_wrap (branches wrapping
		// past $FFFF), dummy_reads (addressing-mode dummy reads
		// land on the right bus address), dummy_reads_apu (ditto
		// for APU register addresses, exercises bus-side reads
		// against side-effecting registers).
		name:      "instr_misc.nes",
		url:       "https://github.com/christopherpow/nes-test-roms/raw/master/instr_misc/instr_misc.nes",
		sha:       "b6762e20a285216304dfd2b5e1f192459354b23a5e48b2f5f9fb7cb0dac51243",
		pathEnv:   "CHIPPY_ACCURACY_INSTR_MISC_BIN",
		maxFrames: 3000,
	},
	{
		// Blargg instr_test-v5 (all_instrs) — every NMOS official
		// + illegal opcode under every addressing mode, across 16
		// sub-tests. Tests 1 (basics) + 2 (implied) PASS; test 3
		// (immediate) fails at $AB (LXA/ATX) — an *unstable*
		// illegal whose result depends on real-silicon analog
		// noise. chippy implements the common stable
		// approximation (`(A | 0xEE) & imm`) per #318 but
		// Blargg's tests pin a specific value that doesn't match
		// any magic constant. Use official_only.nes instead for a
		// clean pass on the non-unstable opcodes — wired below.
		name:      "instr_test-v5.nes",
		url:       "https://github.com/christopherpow/nes-test-roms/raw/master/instr_test-v5/all_instrs.nes",
		sha:       "353870c157242e3d428ef7387109deaee0d2e158bdb432ab9aae4e657072c785",
		pathEnv:   "CHIPPY_ACCURACY_INSTR_TEST_V5_BIN",
		maxFrames: 6000,
		knownFail: "tests 1-2 (basics, implied) PASS; test 3 (immediate) fails at $AB LXA/ATX — unstable illegal opcode whose silicon result depends on analog noise, chippy uses the 0xEE-magic stable approximation",
	},
	{
		// Blargg instr_test-v5 (official_only) — same 16 sub-test
		// matrix as all_instrs.nes but excludes the unstable
		// illegal opcodes ($8B XAA, $AB LXA, $93 SHA, $9F AHX,
		// $9C SHY, $9E SHX, $9B TAS, $BB LAS). Clean baseline
		// for the cycle-accurate CPU core (#318).
		name:      "instr_test-v5_official.nes",
		url:       "https://github.com/christopherpow/nes-test-roms/raw/master/instr_test-v5/official_only.nes",
		sha:       "589b8835deb5cbc69618dac193a3dbd675540f7f2794e2d2a92e97beb8abc3cb",
		pathEnv:   "CHIPPY_ACCURACY_INSTR_TEST_V5_OFFICIAL_BIN",
		maxFrames: 6000,
	},
	{
		// Blargg oam_read — OAMADDR/OAMDATA ($2003/$2004) read
		// behavior. Full PASS. Also the first NROM (mapper 0) ROM in
		// the suite: these write their status to $6000 work RAM, which
		// NROM now provides (8 KiB at $6000-$7FFF) — earlier NROM had
		// no WRAM so every mapper-0 test ROM read as "never started".
		name:      "oam_read.nes",
		url:       "https://github.com/christopherpow/nes-test-roms/raw/master/oam_read/oam_read.nes",
		sha:       "f298973dabeb61ca35007445f7a615f77e87703c958c870986af83b1aabde926",
		pathEnv:   "CHIPPY_ACCURACY_OAM_READ_BIN",
		maxFrames: 2500,
	},
	{
		// Blargg oam_stress — random OAM read/write patterns. PASSes once
		// the OAM attribute byte (sprite byte 2) reads bits 2-4 as 0 — the
		// same 2C02 quirk ppu_open_bus test 10 pins (#17 fixed both; #18).
		name:      "oam_stress.nes",
		url:       "https://github.com/christopherpow/nes-test-roms/raw/master/oam_stress/oam_stress.nes",
		sha:       "95882d72a7acabe928fd277e3b3e0372f21ef3d41e36d7d8fb17fc017a356f70",
		pathEnv:   "CHIPPY_ACCURACY_OAM_STRESS_BIN",
		maxFrames: 2500,
	},
	{
		// Blargg ppu_open_bus — PPU open-bus latch + per-bit decay + the
		// OAM attribute-byte read mask (11/11 PASS, #17).
		name:      "ppu_open_bus.nes",
		url:       "https://github.com/christopherpow/nes-test-roms/raw/master/ppu_open_bus/ppu_open_bus.nes",
		sha:       "d4208a3ff6340532dd0fced7f9d408d5b6585853a0ddc9c1f64ee1722ef08e67",
		pathEnv:   "CHIPPY_ACCURACY_PPU_OPEN_BUS_BIN",
		maxFrames: 2500,
	},
	// Blargg mmc3_test 1-6 — MMC3 scanline-IRQ counter + A12-edge
	// clocking. Tests 1, 2, 3, 5 PASS since the PPU drives the VRAM
	// address onto the bus on $2006 writes + non-rendering $2007
	// increments (NotifyVRAMAddr → MMC3.clockA12), closing the
	// "A12 toggled via PPUADDR" gap (#16). Tests 4 + 6 reach their
	// next sub-test, both gated on the 3-PPU-cycle deferred $2006
	// v-update + sub-cycle rendering A12 timing (tracked in #25).
	{
		name:      "mmc3_test_1_clocking.nes",
		url:       "https://github.com/christopherpow/nes-test-roms/raw/master/mmc3_test/1-clocking.nes",
		sha:       "57c77c66edde8c45e17bda02691dd3c7fd0b270c1ec024dff4e11a7778dfaa37",
		pathEnv:   "CHIPPY_ACCURACY_MMC3_1_BIN",
		maxFrames: 2500,
	},
	{
		name:      "mmc3_test_2_details.nes",
		url:       "https://github.com/christopherpow/nes-test-roms/raw/master/mmc3_test/2-details.nes",
		sha:       "89e1f16514aafeee90b5ab849dd73dbf1456dbd363ec2e3b798461125a33068a",
		pathEnv:   "CHIPPY_ACCURACY_MMC3_2_BIN",
		maxFrames: 2500,
	},
	{
		name:      "mmc3_test_3_a12_clocking.nes",
		url:       "https://github.com/christopherpow/nes-test-roms/raw/master/mmc3_test/3-A12_clocking.nes",
		sha:       "dc6779b3d64e27b8d3b2b6dee7a1b528b9b6401ac0e6a9a1d5ab928dcd8ad6bb",
		pathEnv:   "CHIPPY_ACCURACY_MMC3_3_BIN",
		maxFrames: 2500,
	},
	{
		name:      "mmc3_test_4_scanline_timing.nes",
		url:       "https://github.com/christopherpow/nes-test-roms/raw/master/mmc3_test/4-scanline_timing.nes",
		sha:       "0474550dbf811bf1acda2178bf355edd5c100088479a09d881f84994c1690b82",
		pathEnv:   "CHIPPY_ACCURACY_MMC3_4_BIN",
		maxFrames: 2500,
		// PASS — sprite-pattern A12 rise emitted at dot 261 (slot-0
		// phase 4) instead of the batched dot 257, so the $2000=$08
		// scanline-0 IRQ lands on the dot the test pins (spriteFetchDot).
	},
	{
		name:      "mmc3_test_5_mmc3.nes",
		url:       "https://github.com/christopherpow/nes-test-roms/raw/master/mmc3_test/5-MMC3.nes",
		sha:       "f714089b5d056a50d63854a8d13359914d20d6144d8b25e48f880116ae73d8fd",
		pathEnv:   "CHIPPY_ACCURACY_MMC3_5_BIN",
		maxFrames: 2500,
	},
	{
		name:      "mmc3_test_6_mmc6.nes",
		url:       "https://github.com/christopherpow/nes-test-roms/raw/master/mmc3_test/6-MMC6.nes",
		sha:       "e6bdbadf46cc4bf7b26e496ecab44e60a8b1279c1b9cf16df090c9832adf6943",
		pathEnv:   "CHIPPY_ACCURACY_MMC3_6_BIN",
		maxFrames: 2500,
		// PASS — rev-A IRQ counter (stuck-at-zero stays silent), selected
		// by content hash since the header matches the rev-B test 5 ROM.
	},
	{
		// Blargg sprite_overflow_tests (1.Basics representative). No
		// $6000 shell — reports by on-screen text + APU beeps, then
		// parks. Graded via runParkedResult on the zero-page result
		// byte $F8 (1 = passed). 8 sub-tests; test 7 ($2001=$08, BG
		// rendering only) pins that sprite evaluation — hence the
		// overflow flag — runs when EITHER BG or sprites are enabled
		// (#19, #12, chippy#283).
		name:       "sprite_overflow_basics.nes",
		url:        "https://github.com/christopherpow/nes-test-roms/raw/master/sprite_overflow_tests/1.Basics.nes",
		sha:        "1a6782f63ccb3a3dd1aa6a24272036c9c3aa232c2d1ff0b21e872741a3ee4fe2",
		pathEnv:    "CHIPPY_ACCURACY_SPRITE_OVERFLOW_BIN",
		maxFrames:  600,
		resultAddr: 0x00F8,
	},
	{
		// Blargg dmc_dma_during_read4 (dma_2007_read representative). No
		// $6000 shell, no zero-page result byte: per-sub-test DMC-active
		// poll loops calibrate the exact DMC-DMA-steal cycle and spin
		// forever unless the steal lands on the cycle-correct CPU cycle.
		// When correct they escape to the $E72F-$E735 `SEI / STA $2000 /
		// JMP *` terminal hang — graded by runTerminalLoop, validated
		// cycle-for-cycle against MesenCE (#20). Closed by chippy #493
		// (idle() polls ProcessPendingDma so a DMA halt drains on the
		// taken-branch dummy-read cycle → 4-cycle steal → phase drift)
		// plus the host-side DmaReadBus glitch formula (dmabus.go).
		name:         "dmc_dma_2007_read.nes",
		url:          "https://github.com/christopherpow/nes-test-roms/raw/master/dmc_dma_during_read4/dma_2007_read.nes",
		sha:          "a2e0fa3f6f155cbe0b8c9517b2f6a57f1fd68f13711c11d6d2fe5676c522d7b2",
		pathEnv:      "CHIPPY_ACCURACY_DMC_DMA_BIN",
		maxFrames:    600,
		terminalLoop: [2]uint16{0xE72F, 0xE735},
	},
	{
		// Blargg sprite_hit_tests 2005 (01.basics representative). Same
		// generation as sprite_overflow_tests — no $6000 shell, reports
		// on-screen + APU beeps, then parks in a tight self-loop with the
		// result in zero-page $F8 (1 = passed, else the failed sub-test
		// number). Graded via runParkedResult, not a framebuffer (#21).
		name:       "sprite_hit_basics.nes",
		url:        "https://github.com/christopherpow/nes-test-roms/raw/master/sprite_hit_tests_2005.10.05/01.basics.nes",
		sha:        "51819e8e502bd88fe3b7244198a074dbeef2e848f66c587be04b04f1f0d4bb52",
		pathEnv:    "CHIPPY_ACCURACY_SPRITE_HIT_BIN",
		maxFrames:  600,
		resultAddr: 0x00F8,
	},
	{
		// cpu_timing_test6 — visual-only, no $6000 protocol. Prints
		// "6502 TIMING TEST / OFFICIAL INSTRUCTIONS ONLY / PASSED" to the
		// nametable, then parks. Graded via runScreenText (nametable
		// verdict), not a framebuffer golden (#21).
		name:       "cpu_timing_test6.nes",
		url:        "https://github.com/christopherpow/nes-test-roms/raw/master/cpu_timing_test6/cpu_timing_test.nes",
		sha:        "6ab4fe8af23b12ca0dfccfc030de3d4069bf2498e3ef20ddcf1ca75555065b85",
		pathEnv:    "CHIPPY_ACCURACY_CPU_TIMING6_BIN",
		maxFrames:  900,
		screenPass: true,
	},
}

func TestAccuracy(t *testing.T) {
	for _, rom := range accuracyROMs {
		t.Run(rom.name, func(t *testing.T) {
			data, err := loadAccuracyROM(t, rom)
			if err != nil {
				t.Fatalf("load %s: %v", rom.name, err)
			}
			parsed, err := nes.ParseBytes(data)
			if err != nil {
				t.Fatalf("parse %s: %v", rom.name, err)
			}
			bus, err := buildNES(parsed)
			if err != nil {
				t.Fatalf("build %s: %v", rom.name, err)
			}

			var status byte
			var text string
			switch {
			case rom.screenPass:
				status, text = runScreenText(bus, rom.maxFrames)
			case rom.terminalLoop != [2]uint16{}:
				status, text = runTerminalLoop(bus, rom.maxFrames, rom.terminalLoop)
			case rom.resultAddr != 0:
				status, text = runParkedResult(bus, rom.maxFrames, rom.resultAddr)
			default:
				status, text = runBlargg(bus, rom.maxFrames)
			}
			t.Logf("%s: status=$%02X\n%s", rom.name, status, text)
			if status == 0 {
				return // pass
			}
			if rom.knownFail != "" {
				t.Skipf("%s: known accuracy gap (%s); status=$%02X", rom.name, rom.knownFail, status)
			}
			t.Errorf("%s FAILED: status=$%02X\n%s", rom.name, status, text)
		})
	}
}

// runScreenText grades a visual-only ROM that prints its verdict to the
// PPU nametable (blargg's pre-$6000 generation; font tile index ==
// ASCII). It runs to the CPU's park (PC unchanged across a step), decodes
// the $2000 nametable to text via $2006/$2007, and passes iff the screen
// contains "PASSED" without "FAILED". Returns status 0 on pass, else 0xFD
// with the decoded screen for the log.
func runScreenText(bus *nesBus, maxFrames int) (byte, string) {
	parked := false
	for f := 0; f < maxFrames && !parked; f++ {
		target := bus.cpu.Cycles + accuracyCyclesPerFrame
		for bus.cpu.Cycles < target && !bus.cpu.Halted {
			pc := bus.cpu.PC
			bus.cpu.Step()
			if bus.cpu.PC == pc {
				parked = true
				break
			}
		}
	}
	screen := decodeNametableText(bus)
	if !parked {
		return 0xFF, "timed out before the test parked\n" + screen
	}
	if strings.Contains(screen, "PASSED") && !strings.Contains(screen, "FAILED") {
		return 0, "passed (nametable verdict)\n" + screen
	}
	return 0xFD, "no PASSED verdict on screen\n" + screen
}

// decodeNametableText reads the $2000 nametable through $2006/$2007 and
// renders its 30×32 tile grid as text, mapping printable tile indices
// (blargg font: tile == ASCII) to characters, others to spaces. The
// $2007 read path is buffered, so one priming read is discarded first.
func decodeNametableText(bus *nesBus) string {
	bus.ppu.Read(0x2002) // reset the $2006 address latch
	bus.ppu.Write(0x2006, 0x20)
	bus.ppu.Write(0x2006, 0x00)
	_ = bus.ppu.Read(0x2007) // prime the read buffer
	var b strings.Builder
	for r := 0; r < 30; r++ {
		for c := 0; c < 32; c++ {
			tile := bus.ppu.Read(0x2007)
			if tile >= 0x20 && tile < 0x7F {
				b.WriteByte(tile)
			} else {
				b.WriteByte(' ')
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// runTerminalLoop grades a self-calibrating ROM (dmc_dma_during_read4's
// dma_2007_read) that has no $6000 shell and no zero-page result byte.
// It runs per-sub-test calibration loops that spin forever unless the
// timing under test is cycle-correct, in which case it escapes to a
// final terminal hang (`SEI / STA $2000 / JMP *`). The runner steps a
// frame at a time, tracking the PC range covered each frame; once the
// CPU is confined to the [lo, hi] terminal window for a whole frame it
// has converged → pass (status 0). Frame-cap exhaustion → status 0xFE
// (still spinning in a calibration loop = the timing-under-test is
// wrong). The window is validated cycle-for-cycle against MesenCE, which
// settles into the identical loop (#20 / chippy #493).
func runTerminalLoop(bus *nesBus, maxFrames int, window [2]uint16) (byte, string) {
	lo, hi := window[0], window[1]
	for f := 0; f < maxFrames; f++ {
		var seenLo, seenHi uint16 = 0xFFFF, 0
		target := bus.cpu.Cycles + accuracyCyclesPerFrame
		for bus.cpu.Cycles < target && !bus.cpu.Halted {
			pc := bus.cpu.PC
			if pc < seenLo {
				seenLo = pc
			}
			if pc > seenHi {
				seenHi = pc
			}
			bus.cpu.Step()
		}
		if seenLo >= lo && seenHi <= hi {
			return 0, fmt.Sprintf("passed (converged to terminal loop $%04X-$%04X at frame %d)", seenLo, seenHi, f)
		}
	}
	return 0xFE, fmt.Sprintf("timed out still spinning in a calibration loop (never reached terminal $%04X-$%04X) — DMA-steal timing wrong", lo, hi)
}

// runParkedResult grades a ROM that reports via on-screen text + APU
// beeps (no $6000 shell): the Blargg sprite_overflow_tests / dmc_dma
// generation. The test runs its sub-tests then parks in a tight
// self-loop (e.g. `JMP *`) with a result code in zero page — 1 =
// passed, otherwise the failed sub-test number. The runner steps until
// the CPU parks (PC unchanged across a step) or the frame cap trips,
// then reads resultAddr. Returns status 0 on pass ($result == 1), else
// the raw result code as the "status" so the caller's gap-skip logic +
// logging work unchanged.
func runParkedResult(bus *nesBus, maxFrames int, resultAddr uint16) (byte, string) {
	for f := 0; f < maxFrames; f++ {
		target := bus.cpu.Cycles + accuracyCyclesPerFrame
		for bus.cpu.Cycles < target && !bus.cpu.Halted {
			pc := bus.cpu.PC
			bus.cpu.Step()
			// A tight self-loop (PC didn't move) = the test parked after
			// reporting its result.
			if bus.cpu.PC == pc {
				res := bus.cpu.Bus.Read(resultAddr)
				if res == 1 {
					return 0, "passed (parked self-loop; result=1)"
				}
				return res, fmt.Sprintf("failed sub-test %d (parked; result byte $%02X)", res, res)
			}
		}
	}
	return 0xFF, "timed out before the test parked"
}

// runBlargg steps the bus until the ROM reports a finished status at
// $6000 (or the frame cap trips). Returns the final status byte +
// the result text at $6004. The magic at $6001-$6003 gates trusting
// $6000 — before the test writes it, $6000 is uninitialised RAM.
func runBlargg(bus *nesBus, maxFrames int) (byte, string) {
	started := false
	for f := 0; f < maxFrames; f++ {
		target := bus.cpu.Cycles + accuracyCyclesPerFrame
		for bus.cpu.Cycles < target && !bus.cpu.Halted {
			bus.cpu.Step()
		}
		magicOK := bus.cart.CPURead(0x6001) == 0xDE &&
			bus.cart.CPURead(0x6002) == 0xB0 &&
			bus.cart.CPURead(0x6003) == 0x61
		if !magicOK {
			continue
		}
		started = true
		switch bus.cart.CPURead(0x6000) {
		case 0x80, 0x81:
			// still running / awaiting reset
		default:
			return bus.cart.CPURead(0x6000), blarggText(bus)
		}
	}
	if !started {
		return 0xFF, "test never wrote the Blargg status magic ($6001-$6003)"
	}
	return 0xFF, "timed out before the test reported a finished status"
}

func blarggText(bus *nesBus) string {
	var b strings.Builder
	for addr := 0x6004; addr < 0x8000; addr++ {
		c := bus.cart.CPURead(uint16(addr))
		if c == 0 {
			break
		}
		b.WriteByte(c)
	}
	return strings.TrimSpace(b.String())
}

// loadAccuracyROM mirrors the nestest fixture loader: env override →
// SHA-pinned cache → download. An empty pinned SHA means "accept
// whatever downloads + log the hash" so the pin can be filled in on
// first run.
func loadAccuracyROM(t *testing.T, rom accuracyROM) ([]byte, error) {
	t.Helper()
	if p := os.Getenv(rom.pathEnv); p != "" {
		return os.ReadFile(p)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	cachePath := filepath.Join(cache, "chippy-tests", rom.name)
	if data, err := os.ReadFile(cachePath); err == nil {
		if rom.sha == "" || strings.EqualFold(hashHex(data), rom.sha) {
			return data, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return nil, err
	}
	t.Logf("downloading %s", rom.url)
	data, err := httpGet(rom.url, 30*time.Second)
	if err != nil {
		return nil, err
	}
	if rom.sha != "" && !strings.EqualFold(hashHex(data), rom.sha) {
		return nil, fmt.Errorf("%s sha mismatch: got %s", rom.name, hashHex(data))
	}
	if rom.sha == "" {
		t.Logf("%s downloaded; pin this SHA: %s", rom.name, hashHex(data))
	}
	_ = os.WriteFile(cachePath, data, 0o644)
	return data, nil
}

// httpGet fetches url with bounded retry + linear backoff. GitHub raw
// (where the test ROMs live) intermittently returns 5xx/504, so a
// single transient failure must not red the accuracy gate — see #46.
// Network errors and 5xx/429 responses are retried; other 4xx are
// fatal (a real bad URL won't self-heal).
func httpGet(url string, timeout time.Duration) ([]byte, error) {
	const attempts = 4
	var lastErr error
	for i := range attempts {
		if i > 0 {
			time.Sleep(time.Duration(i) * 2 * time.Second) // 2s, 4s, 6s
		}
		data, retry, err := httpGetOnce(url, timeout)
		if err == nil {
			return data, nil
		}
		lastErr = err
		if !retry {
			return nil, err
		}
	}
	return nil, fmt.Errorf("%s: giving up after %d attempts: %w", url, attempts, lastErr)
}

// httpGetOnce performs one fetch. retry reports whether the failure is
// transient (worth another attempt).
func httpGetOnce(url string, timeout time.Duration) (data []byte, retry bool, err error) {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return nil, true, err // network/timeout: transient
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		transient := resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests
		return nil, transient, fmt.Errorf("http %s: status %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, true, err
	}
	return body, false, nil
}

// httpGet retries a transient 5xx then succeeds — the GitHub raw 504
// case that used to red the accuracy gate (#46).
func TestHTTPGetRetriesTransient(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) < 2 {
			w.WriteHeader(http.StatusGatewayTimeout) // 504
			return
		}
		_, _ = w.Write([]byte("rom-bytes"))
	}))
	defer srv.Close()

	data, err := httpGet(srv.URL, 5*time.Second)
	if err != nil {
		t.Fatalf("httpGet: %v", err)
	}
	if string(data) != "rom-bytes" {
		t.Errorf("body = %q; want rom-bytes", data)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Errorf("server calls = %d; want 2 (one 504 retry + success)", n)
	}
}

// A non-transient 4xx is fatal — no retry (a bad URL won't self-heal).
func TestHTTPGetFatalOn4xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusNotFound) // 404
	}))
	defer srv.Close()

	if _, err := httpGet(srv.URL, 5*time.Second); err == nil {
		t.Fatal("httpGet: err = nil; want 404 error")
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("server calls = %d; want 1 (no retry on 404)", n)
	}
}

func hashHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
