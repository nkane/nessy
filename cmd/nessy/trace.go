//go:build nessy

package main

import (
	"bufio"
	"fmt"
	"os"
	"sync"

	"github.com/nkane/chippy/cpu"
	"github.com/nkane/nessy/internal/nes/ppu"
)

// nesTracer is an NES-aware cpu.Tracer for the debugger's trace logger
// (#35). It writes the chippy CPU trace columns (PC, opcode bytes,
// disassembly, A/X/Y/P/SP, CYC) plus the NES-specific PPU cursor
// (scanline,dot + frame) so a trace can be correlated with what the
// PPU was doing — which the chippy core's FileTracer can't know.
//
// It's assigned to CPU.Tracer only while a trace is running, so the
// no-trace hot path stays at zero cost (CPU skips a nil Tracer). The
// internal mutex guards the file + enabled flag against the trace
// start/stop requests, which arrive on the DAP dispatch goroutine while
// LogStep runs on the CPU-step goroutine.
type nesTracer struct {
	ppu *ppu.PPU

	mu      sync.Mutex
	enabled bool
	f       *os.File
	w       *bufio.Writer
	path    string
	lines   uint64
}

func newNESTracer(p *ppu.PPU) *nesTracer { return &nesTracer{ppu: p} }

// LogStep implements cpu.Tracer: one line per instruction boundary.
func (t *nesTracer) LogStep(c *cpu.CPU, bus cpu.Bus) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.enabled || t.w == nil {
		return
	}
	pc := c.PC
	op := bus.Read(pc)
	dis, n := cpu.DisasmCPU(c, pc)
	if n < 1 {
		n = 1
	} else if n > 3 {
		n = 3
	}
	var bytesStr string
	switch n {
	case 1:
		bytesStr = fmt.Sprintf("%02X      ", op)
	case 2:
		bytesStr = fmt.Sprintf("%02X %02X   ", op, bus.Read(pc+1))
	default:
		bytesStr = fmt.Sprintf("%02X %02X %02X", op, bus.Read(pc+1), bus.Read(pc+2))
	}
	fmt.Fprintf(t.w, "%04X  %s  %-13s  A:%02X X:%02X Y:%02X P:%02X SP:%02X CYC:%d  PPU:%3d,%3d FRM:%d\n",
		pc, bytesStr, dis, c.A, c.X, c.Y, c.P, c.SP, c.Cycles,
		t.ppu.Scanline(), t.ppu.Dot(), t.ppu.FrameCount())
	t.lines++
}

// LogInterrupt implements cpu.Tracer: a marker line on NMI/IRQ/BRK
// vector entry.
func (t *nesTracer) LogInterrupt(c *cpu.CPU, kind string, vector uint16) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.enabled || t.w == nil {
		return
	}
	fmt.Fprintf(t.w, "---- %s -> $%04X (PC=$%04X SP=%02X CYC:%d  PPU:%d,%d)\n",
		kind, vector, c.PC, c.SP, c.Cycles, t.ppu.Scanline(), t.ppu.Dot())
	t.lines++
}

// start opens path and begins tracing. Errors if already running or the
// file can't be created.
func (t *nesTracer) start(path string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if path == "" {
		return fmt.Errorf("trace: a path is required")
	}
	if t.enabled {
		return fmt.Errorf("trace: already running (%s)", t.path)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("trace: create %s: %w", path, err)
	}
	t.f = f
	t.w = bufio.NewWriterSize(f, 64*1024)
	t.path = path
	t.lines = 0
	t.enabled = true
	return nil
}

// stop flushes + closes the trace file. Idempotent: stopping an
// already-stopped tracer returns the last path/line count, no error.
func (t *nesTracer) stop() (string, uint64, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.enabled {
		return t.path, t.lines, nil
	}
	t.enabled = false
	var err error
	if t.w != nil {
		err = t.w.Flush()
	}
	if t.f != nil {
		if cerr := t.f.Close(); err == nil {
			err = cerr
		}
	}
	t.w, t.f = nil, nil
	return t.path, t.lines, err
}

// status reports the current trace state.
func (t *nesTracer) status() (enabled bool, path string, lines uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.enabled, t.path, t.lines
}
