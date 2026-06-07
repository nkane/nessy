//go:build nessy

package main

import (
	"encoding/json"
	"fmt"

	"github.com/nkane/nessy/internal/nes/apu"
	"github.com/nkane/nessy/internal/nes/cart"
	"github.com/nkane/nessy/internal/nes/ppu"
)

// debugSnapshotVersion is bumped whenever the DebugSnapshot wire shape
// changes so the TUI can reject a mismatched server.
const debugSnapshotVersion = 1

// debugStateCommand is the DAP custom-request command the chippy TUI
// sends to pull a fresh NES debug-state snapshot. Namespaced under
// "nessy/" so it can't collide with a future built-in DAP command.
const debugStateCommand = "nessy/debugState"

// ppuViewerCommand pulls the heavyweight PPU-render state (pattern
// tables, nametables, palette, scroll) for the tilemap / pattern /
// palette panels (#29). Separate from debugStateCommand so a routine
// status poll stays cheap and the ~12 KiB viewer payload only crosses
// the wire when a panel is open.
const ppuViewerCommand = "nessy/ppuViewer"

// spriteViewerCommand pulls OAM + decoded sprite state for the sprite
// viewer panel (#30). OAM is small, but it's its own request so the
// routine status poll doesn't carry it.
const spriteViewerCommand = "nessy/spriteViewer"

// ppuMemoryCommand pulls the PPU-side memory spaces (VRAM / palette /
// OAM / CHR) for the memory viewer (#32). The CPU bus + PRG-RAM are
// served by the standard DAP readMemory request, so only the non-CPU-
// bus spaces are exposed here.
const ppuMemoryCommand = "nessy/ppuMemory"

// Event viewer commands (#31). Recording is opt-in (off by default for
// zero hot-path cost); eventFrame returns the last completed frame's
// per-dot event log.
const (
	eventStartCommand = "nessy/eventStart"
	eventStopCommand  = "nessy/eventStop"
	eventFrameCommand = "nessy/eventFrame"
)

// Trace logger commands (#35). Start opens a file + assigns the
// NES-aware tracer to the CPU; stop flushes/closes + detaches it;
// status reports progress. Detaching on stop keeps the no-trace hot
// path at zero cost.
const (
	traceStartCommand  = "nessy/traceStart"
	traceStopCommand   = "nessy/traceStop"
	traceStatusCommand = "nessy/traceStatus"
)

// traceStartArgs is the body of a traceStart request.
type traceStartArgs struct {
	Path string `json:"path"`
}

// traceStatus is the response for the trace control commands.
type traceStatus struct {
	Enabled bool   `json:"enabled"`
	Path    string `json:"path"`
	Lines   uint64 `json:"lines"`
}

// registerViewCommand pulls the fully-decoded PPU / APU / cart register
// state for the register viewer panel (#34) — self-contained so the
// panel renders without cross-referencing the routine status snapshot.
const registerViewCommand = "nessy/registers"

// RegisterView is the decoded register state for the register viewer
// (#34): PPU latches with named bit breakdowns, the full APU channel +
// frame-counter state (already field-named), and the active mapper's
// register state.
type RegisterView struct {
	PPU  ppu.PPURegisters `json:"ppu"`
	APU  apu.FullState    `json:"apu"`
	Cart cart.CartState   `json:"cart"`
}

// registerView captures the decoded register state. Callers hold the
// CPU mutex (the DAP dispatcher does) for a coherent read.
func (b *nesBus) registerView() (RegisterView, error) {
	cs, err := cart.SaveCart(b.cart)
	if err != nil {
		return RegisterView{}, fmt.Errorf("register view: cart state: %w", err)
	}
	return RegisterView{
		PPU:  b.ppu.DecodedRegisters(),
		APU:  b.apu.SaveFullState(),
		Cart: cs,
	}, nil
}

// DebugSnapshot is the coherent, paused-state capture of NES debug
// state served over the DAP "nessy/debugState" custom request (#28).
//
// It is the foundation the per-tool debugger panels build on: the
// PPU/sprite/event viewers (#29-#31), memory + register views
// (#32/#34), etc. each extend this struct with their own section.
// Captured under the CPU lock (the DAP dispatcher holds it for every
// non-continue/pause request), so every field reflects the same
// instruction boundary — no mid-step tearing.
//
// Heavyweight data (framebuffers, full VRAM/OAM dumps, access
// heatmaps) is deliberately NOT here; those land in the panel-specific
// issues so a routine poll of this snapshot stays cheap.
type DebugSnapshot struct {
	Version int            `json:"version"`
	Timing  DebugTiming    `json:"timing"`
	CPU     DebugCPU       `json:"cpu"`
	PPU     ppu.DebugRegs  `json:"ppu"`
	APU     apu.FullState  `json:"apu"`
	Cart    cart.CartState `json:"cart"`
}

// DebugTiming is the PPU frame/scanline/dot cursor at capture time.
type DebugTiming struct {
	Frame    uint64 `json:"frame"`
	Scanline int    `json:"scanline"`
	Dot      int    `json:"dot"`
}

// DebugCPU is the 6502 register file at capture time.
type DebugCPU struct {
	A      byte   `json:"a"`
	X      byte   `json:"x"`
	Y      byte   `json:"y"`
	SP     byte   `json:"sp"`
	P      byte   `json:"p"`
	PC     uint16 `json:"pc"`
	Cycles uint64 `json:"cycles"`
}

// debugSnapshot captures the current NES debug state. Callers must hold
// the CPU mutex (the DAP dispatcher does) so the capture is coherent.
func (b *nesBus) debugSnapshot() (DebugSnapshot, error) {
	cs, err := cart.SaveCart(b.cart)
	if err != nil {
		return DebugSnapshot{}, fmt.Errorf("debug snapshot: cart state: %w", err)
	}
	return DebugSnapshot{
		Version: debugSnapshotVersion,
		Timing: DebugTiming{
			Frame:    b.ppu.FrameCount(),
			Scanline: b.ppu.Scanline(),
			Dot:      b.ppu.Dot(),
		},
		CPU: DebugCPU{
			A:      b.cpu.A,
			X:      b.cpu.X,
			Y:      b.cpu.Y,
			SP:     b.cpu.SP,
			P:      b.cpu.P,
			PC:     b.cpu.PC,
			Cycles: b.cpu.Cycles,
		},
		PPU:  b.ppu.DebugRegs(),
		APU:  b.apu.SaveFullState(),
		Cart: cs,
	}, nil
}

// debugRequestHandler returns a chippy dap.AttachConfig.CustomRequestHandler
// that serves nessy's debug custom requests. Unrecognized commands
// return handled=false so the DAP server falls through to its standard
// "not implemented" error. The handler runs under the CPU lock held by
// the dispatcher, so debugSnapshot observes a coherent state.
func debugRequestHandler(bus *nesBus, tracer *nesTracer) func(command string, args json.RawMessage) (any, bool, error) {
	return func(command string, args json.RawMessage) (any, bool, error) {
		switch command {
		case debugStateCommand:
			snap, err := bus.debugSnapshot()
			if err != nil {
				return nil, true, err
			}
			return snap, true, nil
		case ppuViewerCommand:
			return bus.ppu.DebugPPUViewer(), true, nil
		case spriteViewerCommand:
			return bus.ppu.DebugSpriteViewer(), true, nil
		case registerViewCommand:
			rv, err := bus.registerView()
			if err != nil {
				return nil, true, err
			}
			return rv, true, nil
		case ppuMemoryCommand:
			return bus.ppu.DebugMemorySpaces(), true, nil
		case eventStartCommand:
			bus.ppu.SetEventRecording(true)
			return map[string]bool{"recording": true}, true, nil
		case eventStopCommand:
			bus.ppu.SetEventRecording(false)
			return map[string]bool{"recording": false}, true, nil
		case eventFrameCommand:
			return map[string]any{"events": bus.ppu.EventFrame()}, true, nil
		case traceStartCommand:
			var a traceStartArgs
			if len(args) > 0 {
				if err := json.Unmarshal(args, &a); err != nil {
					return nil, true, fmt.Errorf("traceStart: bad args: %w", err)
				}
			}
			if err := tracer.start(a.Path); err != nil {
				return nil, true, err
			}
			// Attach only while tracing so the no-trace hot path skips a
			// nil CPU.Tracer. Safe under the dispatcher's CPU lock.
			bus.cpu.Tracer = tracer
			en, p, l := tracer.status()
			return traceStatus{Enabled: en, Path: p, Lines: l}, true, nil
		case traceStopCommand:
			// Detach before flushing so no further LogStep can fire.
			bus.cpu.Tracer = nil
			p, l, err := tracer.stop()
			if err != nil {
				return nil, true, err
			}
			return traceStatus{Enabled: false, Path: p, Lines: l}, true, nil
		case traceStatusCommand:
			en, p, l := tracer.status()
			return traceStatus{Enabled: en, Path: p, Lines: l}, true, nil
		default:
			return nil, false, nil
		}
	}
}
