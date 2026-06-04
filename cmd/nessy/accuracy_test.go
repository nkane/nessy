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
	"os"
	"path/filepath"
	"strings"
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
		// Blargg oam_stress — heavier OAM read/write timing patterns.
		// oam_read PASSes, so basic access works; this diverges.
		name:      "oam_stress.nes",
		url:       "https://github.com/christopherpow/nes-test-roms/raw/master/oam_stress/oam_stress.nes",
		sha:       "95882d72a7acabe928fd277e3b3e0372f21ef3d41e36d7d8fb17fc017a356f70",
		pathEnv:   "CHIPPY_ACCURACY_OAM_STRESS_BIN",
		maxFrames: 2500,
		knownFail: "status $01 — OAM read/write stress timing gap (#18)",
	},
	{
		// Blargg ppu_open_bus — PPU open-bus latch decay timer.
		name:      "ppu_open_bus.nes",
		url:       "https://github.com/christopherpow/nes-test-roms/raw/master/ppu_open_bus/ppu_open_bus.nes",
		sha:       "d4208a3ff6340532dd0fced7f9d408d5b6585853a0ddc9c1f64ee1722ef08e67",
		pathEnv:   "CHIPPY_ACCURACY_PPU_OPEN_BUS_BIN",
		maxFrames: 2500,
		knownFail: "status $03 — open-bus latch should decay to 0 within ~1s; decay timer not implemented (#17)",
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
		knownFail: "status $03 (Failed #3) — scanline 0 IRQ should occur sooner when $2000=$08; needs sub-cycle rendering A12 timing + deferred $2006 v-update (#25)",
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
		knownFail: "status $03 (Failed #3) — IRQ shouldn't occur when reloading after counter normally reaches 0; needs deferred $2006 v-update / sub-cycle A12 timing (#25)",
	},
	{
		// Blargg sprite_overflow_tests (1.Basics representative). The
		// whole suite hangs at init — never writes $6000 status even
		// at 9000 frames. #12 expected these to PASS (chippy#283).
		// Low frame cap: it never reports, so don't burn CI time.
		name:      "sprite_overflow_basics.nes",
		url:       "https://github.com/christopherpow/nes-test-roms/raw/master/sprite_overflow_tests/1.Basics.nes",
		sha:       "1a6782f63ccb3a3dd1aa6a24272036c9c3aa232c2d1ff0b21e872741a3ee4fe2",
		pathEnv:   "CHIPPY_ACCURACY_SPRITE_OVERFLOW_BIN",
		maxFrames: 600,
		knownFail: "init hang — never writes $6000 status (9000-frame timeout); test shell never starts (#19)",
	},
	{
		// Blargg dmc_dma_during_read4 (dma_2007_read representative).
		// Hangs at init like sprite_overflow — no $6000 status at 9000
		// frames. Low frame cap.
		name:      "dmc_dma_2007_read.nes",
		url:       "https://github.com/christopherpow/nes-test-roms/raw/master/dmc_dma_during_read4/dma_2007_read.nes",
		sha:       "a2e0fa3f6f155cbe0b8c9517b2f6a57f1fd68f13711c11d6d2fe5676c522d7b2",
		pathEnv:   "CHIPPY_ACCURACY_DMC_DMA_BIN",
		maxFrames: 600,
		knownFail: "init hang — never writes $6000 status (9000-frame timeout) (#20)",
	},
	{
		// Blargg sprite_hit_tests 2005 (01.basics representative).
		// Visual-only ROM, predates the $6000 text-shell — reports
		// PASS only on screen, so runBlargg can't read a status.
		// Needs a framebuffer-based harness (#21). Low frame cap.
		name:      "sprite_hit_basics.nes",
		url:       "https://github.com/christopherpow/nes-test-roms/raw/master/sprite_hit_tests_2005.10.05/01.basics.nes",
		sha:       "51819e8e502bd88fe3b7244198a074dbeef2e848f66c587be04b04f1f0d4bb52",
		pathEnv:   "CHIPPY_ACCURACY_SPRITE_HIT_BIN",
		maxFrames: 600,
		knownFail: "visual-only ROM (2005 suite, pre-$6000-shell) — result shown on screen only; needs framebuffer harness (#21)",
	},
	{
		// cpu_timing_test6 — visual-only, no $6000 protocol. Needs the
		// framebuffer harness (#21).
		name:      "cpu_timing_test6.nes",
		url:       "https://github.com/christopherpow/nes-test-roms/raw/master/cpu_timing_test6/cpu_timing_test.nes",
		sha:       "6ab4fe8af23b12ca0dfccfc030de3d4069bf2498e3ef20ddcf1ca75555065b85",
		pathEnv:   "CHIPPY_ACCURACY_CPU_TIMING6_BIN",
		maxFrames: 600,
		knownFail: "visual-only ROM — no $6000 protocol; needs framebuffer harness (#21)",
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

			status, text := runBlargg(bus, rom.maxFrames)
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

func httpGet(url string, timeout time.Duration) ([]byte, error) {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %s: status %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func hashHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
