//go:build nessy

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The trace logger writes NES-aware lines while enabled, and detaches
// from the CPU on stop. Drives the control plane through the custom
// request handler, the same path the TUI uses.
func TestTraceLogger(t *testing.T) {
	bus := newTestBus(t)
	tracer := newNESTracer(bus.ppu)
	h := debugRequestHandler(bus, tracer)
	path := filepath.Join(t.TempDir(), "trace.log")

	// Start.
	st, handled, err := h(traceStartCommand, []byte(`{"path":"`+path+`"}`))
	if err != nil || !handled {
		t.Fatalf("traceStart: handled=%v err=%v", handled, err)
	}
	if s := st.(traceStatus); !s.Enabled || s.Path != path {
		t.Fatalf("traceStart status = %+v; want enabled at %s", s, path)
	}
	if bus.cpu.Tracer == nil {
		t.Fatal("CPU.Tracer not attached after traceStart")
	}

	// Run a few instructions so the tracer emits lines.
	for range 8 {
		bus.cpu.Step()
	}

	// Stop.
	st, handled, err = h(traceStopCommand, nil)
	if err != nil || !handled {
		t.Fatalf("traceStop: handled=%v err=%v", handled, err)
	}
	s := st.(traceStatus)
	if s.Enabled {
		t.Error("traceStop status Enabled = true; want false")
	}
	if s.Lines == 0 {
		t.Error("traceStop reported 0 lines; want > 0")
	}
	if bus.cpu.Tracer != nil {
		t.Error("CPU.Tracer still attached after traceStop")
	}

	// File exists, non-empty, NES-aware (carries the PPU cursor column).
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("trace file is empty")
	}
	if !strings.Contains(string(data), "PPU:") {
		t.Errorf("trace missing NES PPU column; got:\n%s", data)
	}
}

// traceStart with no path is a handled error; traceStatus reports idle.
func TestTraceLogger_NoPathErrorsAndStatus(t *testing.T) {
	bus := newTestBus(t)
	tracer := newNESTracer(bus.ppu)
	h := debugRequestHandler(bus, tracer)

	_, handled, err := h(traceStartCommand, []byte(`{"path":""}`))
	if !handled {
		t.Fatal("traceStart: handled=false; want true")
	}
	if err == nil {
		t.Error("traceStart with empty path: err=nil; want error")
	}

	st, handled, err := h(traceStatusCommand, nil)
	if err != nil || !handled {
		t.Fatalf("traceStatus: handled=%v err=%v", handled, err)
	}
	if s := st.(traceStatus); s.Enabled {
		t.Error("traceStatus Enabled = true; want false (idle)")
	}
}
