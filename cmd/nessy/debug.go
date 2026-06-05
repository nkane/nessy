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
func debugRequestHandler(bus *nesBus) func(command string, args json.RawMessage) (any, bool, error) {
	return func(command string, _ json.RawMessage) (any, bool, error) {
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
		default:
			return nil, false, nil
		}
	}
}
