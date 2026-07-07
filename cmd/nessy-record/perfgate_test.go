//go:build perfgate

// Perf regression gate for issue #11. Benchmarks the per-frame emulation
// cost (the chippy per-cycle CPU↔PPU interleave + per-dot PPU render +
// APU tick + mapper IRQ) on committed demo ROMs, then TestPerfGate
// compares each ns/op against a checked-in ubuntu-latest baseline and
// fails when it regresses past the tolerance. Behind the `perfgate` build
// tag so a normal `go test` never pays for it. CI runs:
//
//	go test -tags=perfgate -timeout 5m -run TestPerfGate ./...
//
// Refresh the baseline on a known-good ubuntu-latest commit — see
// docs/perf-baseline.md.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nkane/nessy/internal/nes"
)

// perfDemos are the workloads. Each exercises a distinct hot path so a
// regression localized to one subsystem still trips the gate:
//   - hello-bg   — background-only per-dot fetch + shift pipeline.
//   - oam-grid   — full sprite evaluation + fetch every scanline.
//   - mmc3-split — MMC3 scanline IRQ (A12 low-time filter) + mid-frame
//     scroll split on top of rendering; the heaviest per-frame path.
var perfDemos = []struct {
	name string
	rom  string // path segments under roms/demos/
}{
	{"BenchmarkFrame_HelloBG", "hello-bg/hello-bg.nes"},
	{"BenchmarkFrame_OAMGrid", "oam-grid/oam-grid.nes"},
	{"BenchmarkFrame_MMC3Split", "mmc3-split/mmc3-split.nes"},
}

// benchFrames steps a warmed-up bus one frame per iteration.
func benchFrames(b *testing.B, romRel string) {
	romPath := filepath.Join("..", "..", "roms", "demos")
	for _, seg := range splitSlash(romRel) {
		romPath = filepath.Join(romPath, seg)
	}
	data, err := os.ReadFile(romPath)
	if err != nil {
		b.Fatalf("read %s: %v", romPath, err)
	}
	rom, err := nes.ParseBytes(data)
	if err != nil {
		b.Fatalf("parse %s: %v", romPath, err)
	}
	bus, err := buildBus(rom)
	if err != nil {
		b.Fatalf("build %s: %v", romPath, err)
	}
	// Warm up past the boot/vblank-wait so we measure steady-state
	// rendering, not the title fade-in.
	for i := 0; i < 60; i++ {
		bus.stepFrame()
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bus.stepFrame()
	}
}

func splitSlash(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '/' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	return append(out, cur)
}

func BenchmarkFrame_HelloBG(b *testing.B)   { benchFrames(b, "hello-bg/hello-bg.nes") }
func BenchmarkFrame_OAMGrid(b *testing.B)   { benchFrames(b, "oam-grid/oam-grid.nes") }
func BenchmarkFrame_MMC3Split(b *testing.B) { benchFrames(b, "mmc3-split/mmc3-split.nes") }

// realtimeFrameBudgetNs is one NTSC frame at 60.0988 Hz. A frame that
// emulates in less than this runs at ≥1× realtime; the perfgate logs the
// realtime multiple so the 60 fps acceptance (issue #11) is visible.
const realtimeFrameBudgetNs = 16639267.0

// TestPerfGate runs each benchmark, compares ns/op to the committed
// baseline ceiling, and fails on a regression past the tolerance. It also
// logs the realtime multiple (budget / ns_per_frame) so a reviewer can
// see the 60 fps headroom directly.
func TestPerfGate(t *testing.T) {
	const tolerance = 1.10 // >10% slower than the baseline ceiling fails (#11).

	type entry struct {
		NsOpMax float64 `json:"ns_op_max"`
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "perf-baseline.json"))
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	var asAny map[string]json.RawMessage
	if err := json.Unmarshal(raw, &asAny); err != nil {
		t.Fatalf("parse baseline: %v", err)
	}
	base := map[string]entry{}
	for k, v := range asAny {
		var e entry
		if err := json.Unmarshal(v, &e); err == nil && e.NsOpMax > 0 {
			base[k] = e
		}
	}

	for _, d := range perfDemos {
		want, ok := base[d.name]
		if !ok {
			t.Errorf("%s: no baseline entry", d.name)
			continue
		}
		fn := func(b *testing.B) { benchFrames(b, d.rom) }
		r := testing.Benchmark(fn)
		got := float64(r.NsPerOp())
		rt := realtimeFrameBudgetNs / got
		ceil := want.NsOpMax * tolerance
		if got > ceil {
			t.Errorf("%s: %.0f ns/frame exceeds baseline %.0f * %.2f = %.0f (%.1fx realtime)",
				d.name, got, want.NsOpMax, tolerance, ceil, rt)
			continue
		}
		t.Logf("%s: %.0f ns/frame (ceiling %.0f, %.1fx realtime, headroom %.0f%%)",
			d.name, got, want.NsOpMax, rt, (want.NsOpMax-got)/want.NsOpMax*100)
	}
}
