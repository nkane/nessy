//go:build nessy

package main

import (
	"encoding/json"
	"fmt"

	"github.com/nkane/chippy/cpu"
	"github.com/nkane/chippy/dap"
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

// Memory access-heatmap + freeze commands (#41). Heatmap recording is
// opt-in (allocates 1.5 MiB only on start); freeze locks a CPU-bus
// address to a value via chippy's RAM.Freeze.
const (
	heatmapStartCommand = "nessy/heatmapStart"
	heatmapStopCommand  = "nessy/heatmapStop"
	heatmapCommand      = "nessy/heatmap"
	freezeCommand       = "nessy/freeze"
	unfreezeCommand     = "nessy/unfreeze"
	frozenCommand       = "nessy/frozen"
)

// heatmapArgs / freezeArgs are the request bodies.
type heatmapArgs struct {
	Start  int `json:"start"`
	Length int `json:"length"`
}

type freezeArgs struct {
	Addr  uint16 `json:"addr"`
	Value byte   `json:"value"`
}

// Breakpoint / step-granularity commands (#33). NES-aware conditional
// breakpoints work via SetHostVars (registered at attach), so they need
// no command here. These arm chippy's host stop-predicate for NES step
// granularity; the client arms one, sends `continue`, and clears it when
// the `stopped` event arrives.
const (
	stepScanlineCommand = "nessy/stepScanline"
	stepFrameCommand    = "nessy/stepFrame"
	runToNMICommand     = "nessy/runToNMI"
	clearStepCommand    = "nessy/clearStep"
)

// Typed breakpoint commands (#49) — read/write breakpoints on PPU-bus
// addresses + PPU registers, which chippy's CPU-bus breakpoints can't
// reach. A hit latches a pending stop; armBreakpointStop wires the
// host stop-predicate to drain it (shares the predicate slot with the
// step modes — arm one at a time; clearStep disarms either).
const (
	setMemBreakpointCommand    = "nessy/setMemBreakpoint"
	clearMemBreakpointsCommand = "nessy/clearMemBreakpoints"
	armBreakpointStopCommand   = "nessy/armBreakpointStop"
)

// memBreakpointArgs is the setMemBreakpoint request body. Space is
// "ppu" (PPU bus $0000-$3FFF) or "reg" (PPU register $2000-$2007).
type memBreakpointArgs struct {
	Space string `json:"space"`
	Addr  uint16 `json:"addr"`
	Read  bool   `json:"read"`
	Write bool   `json:"write"`
}

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
func debugRequestHandler(bus *nesBus, tracer *nesTracer, srv *dap.Server, heatmap *accessHeatmap) func(command string, args json.RawMessage) (any, bool, error) {
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
		case stepScanlineCommand:
			srv.SetStopPredicate(stepScanlinePredicate(bus))
			return map[string]string{"armed": "scanline"}, true, nil
		case stepFrameCommand:
			srv.SetStopPredicate(stepFramePredicate(bus))
			return map[string]string{"armed": "frame"}, true, nil
		case runToNMICommand:
			srv.SetStopPredicate(runToNMIPredicate(bus))
			return map[string]string{"armed": "nmi"}, true, nil
		case clearStepCommand:
			srv.SetStopPredicate(nil)
			return map[string]string{"armed": "none"}, true, nil
		case setMemBreakpointCommand:
			var a memBreakpointArgs
			if err := json.Unmarshal(args, &a); err != nil {
				return nil, true, fmt.Errorf("setMemBreakpoint: bad args: %w", err)
			}
			switch a.Space {
			case "ppu":
				bus.ppu.SetPPUBusBreakpoint(a.Addr, a.Read, a.Write)
			case "reg":
				bus.ppu.SetRegBreakpoint(a.Addr, a.Read, a.Write)
			default:
				return nil, true, fmt.Errorf("setMemBreakpoint: unknown space %q (want ppu|reg)", a.Space)
			}
			return map[string]any{"space": a.Space, "addr": a.Addr, "read": a.Read, "write": a.Write}, true, nil
		case clearMemBreakpointsCommand:
			bus.ppu.ClearBreakpoints()
			return map[string]bool{"cleared": true}, true, nil
		case armBreakpointStopCommand:
			srv.SetStopPredicate(func() bool { return bus.ppu.TakePendingStop() })
			return map[string]string{"armed": "breakpoint"}, true, nil
		case heatmapStartCommand:
			heatmap.start()
			bus.cpu.SetAccessHook(func(addr uint16, kind cpu.AccessKind) {
				heatmap.record(addr, kind, bus.cpu.Cycles)
			})
			return map[string]bool{"recording": true}, true, nil
		case heatmapStopCommand:
			bus.cpu.SetAccessHook(nil)
			heatmap.stop()
			return map[string]bool{"recording": false}, true, nil
		case heatmapCommand:
			var a heatmapArgs
			if len(args) > 0 {
				if err := json.Unmarshal(args, &a); err != nil {
					return nil, true, fmt.Errorf("heatmap: bad args: %w", err)
				}
			}
			return heatmap.window(a.Start, a.Length, bus.cpu.Cycles), true, nil
		case freezeCommand:
			var a freezeArgs
			if err := json.Unmarshal(args, &a); err != nil {
				return nil, true, fmt.Errorf("freeze: bad args: %w", err)
			}
			bus.ram.Freeze(a.Addr, a.Value)
			return map[string]any{"addr": a.Addr, "value": a.Value, "frozen": true}, true, nil
		case unfreezeCommand:
			var a freezeArgs
			if err := json.Unmarshal(args, &a); err != nil {
				return nil, true, fmt.Errorf("unfreeze: bad args: %w", err)
			}
			bus.ram.Unfreeze(a.Addr)
			return map[string]any{"addr": a.Addr, "frozen": false}, true, nil
		case frozenCommand:
			return map[string]any{"addrs": bus.ram.FrozenAddrs()}, true, nil
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
