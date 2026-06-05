//go:build nessy

package main

import (
	"encoding/json"
	"testing"

	"github.com/nkane/nessy/internal/nes"
	"github.com/nkane/nessy/internal/nes/ppu"
)

func newTestBus(t *testing.T) *nesBus {
	t.Helper()
	rom, err := nes.ParseBytes(buildiNES(nil))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	bus, err := buildNES(rom)
	if err != nil {
		t.Fatalf("buildNES: %v", err)
	}
	return bus
}

// debugSnapshot captures coherent state and survives a JSON round-trip
// (the DAP transport marshals the body to JSON). The reset CPU state
// (PC = $8000 from the cart vector) must come back intact.
func TestDebugSnapshot_JSONRoundTrip(t *testing.T) {
	bus := newTestBus(t)

	snap, err := bus.debugSnapshot()
	if err != nil {
		t.Fatalf("debugSnapshot: %v", err)
	}
	if snap.Version != debugSnapshotVersion {
		t.Errorf("Version = %d; want %d", snap.Version, debugSnapshotVersion)
	}
	if snap.CPU.PC != 0x8000 {
		t.Errorf("CPU.PC = $%04X; want $8000 (reset vector)", snap.CPU.PC)
	}
	if snap.Cart.Kind != "NROM" {
		t.Errorf("Cart.Kind = %q; want %q", snap.Cart.Kind, "NROM")
	}

	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got DebugSnapshot
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.CPU.PC != snap.CPU.PC {
		t.Errorf("round-trip CPU.PC = $%04X; want $%04X", got.CPU.PC, snap.CPU.PC)
	}
	if got.Version != snap.Version {
		t.Errorf("round-trip Version = %d; want %d", got.Version, snap.Version)
	}
	if got.Cart.Kind != snap.Cart.Kind {
		t.Errorf("round-trip Cart.Kind = %q; want %q", got.Cart.Kind, snap.Cart.Kind)
	}
}

// The custom-request handler serves debugStateCommand (handled=true,
// no error, snapshot body) and defers everything else to the DAP
// server's "not implemented" path (handled=false).
func TestDebugRequestHandler(t *testing.T) {
	bus := newTestBus(t)
	h := debugRequestHandler(bus)

	body, handled, err := h(debugStateCommand, nil)
	if err != nil {
		t.Fatalf("%s: err = %v", debugStateCommand, err)
	}
	if !handled {
		t.Fatalf("%s: handled = false; want true", debugStateCommand)
	}
	snap, ok := body.(DebugSnapshot)
	if !ok {
		t.Fatalf("%s: body type = %T; want DebugSnapshot", debugStateCommand, body)
	}
	if snap.Version != debugSnapshotVersion {
		t.Errorf("body Version = %d; want %d", snap.Version, debugSnapshotVersion)
	}

	// ppuViewer command → handled with a PPUViewer body.
	pv, handled, err := h(ppuViewerCommand, nil)
	if err != nil {
		t.Fatalf("%s: err = %v", ppuViewerCommand, err)
	}
	if !handled {
		t.Fatalf("%s: handled = false; want true", ppuViewerCommand)
	}
	view, ok := pv.(ppu.PPUViewer)
	if !ok {
		t.Fatalf("%s: body type = %T; want ppu.PPUViewer", ppuViewerCommand, pv)
	}
	if len(view.PatternTables) != 0x2000 || len(view.NameTables) != 4 || len(view.Palette) != 32 {
		t.Errorf("PPUViewer shape: pattern=%d nametables=%d palette=%d; want 8192/4/32",
			len(view.PatternTables), len(view.NameTables), len(view.Palette))
	}

	// spriteViewer command → handled with a SpriteViewer body.
	sv, handled, err := h(spriteViewerCommand, nil)
	if err != nil {
		t.Fatalf("%s: err = %v", spriteViewerCommand, err)
	}
	if !handled {
		t.Fatalf("%s: handled = false; want true", spriteViewerCommand)
	}
	sview, ok := sv.(ppu.SpriteViewer)
	if !ok {
		t.Fatalf("%s: body type = %T; want ppu.SpriteViewer", spriteViewerCommand, sv)
	}
	if len(sview.OAM) != 256 || len(sview.Sprites) != 64 {
		t.Errorf("SpriteViewer shape: oam=%d sprites=%d; want 256/64", len(sview.OAM), len(sview.Sprites))
	}

	// registers command → handled with a decoded RegisterView body.
	rvAny, handled, err := h(registerViewCommand, nil)
	if err != nil {
		t.Fatalf("%s: err = %v", registerViewCommand, err)
	}
	if !handled {
		t.Fatalf("%s: handled = false; want true", registerViewCommand)
	}
	rv, ok := rvAny.(RegisterView)
	if !ok {
		t.Fatalf("%s: body type = %T; want RegisterView", registerViewCommand, rvAny)
	}
	if rv.Cart.Kind != "NROM" {
		t.Errorf("RegisterView Cart.Kind = %q; want NROM", rv.Cart.Kind)
	}
	// PPU sub-struct must be populated (decoded bits consistent with raw).
	if rv.PPU.CtrlBits.NMIEnable != (rv.PPU.Ctrl&0x80 != 0) {
		t.Error("RegisterView PPU ctrl decode inconsistent with raw byte")
	}

	// ppuMemory command → handled with a MemorySpaces body.
	mAny, handled, err := h(ppuMemoryCommand, nil)
	if err != nil {
		t.Fatalf("%s: err = %v", ppuMemoryCommand, err)
	}
	if !handled {
		t.Fatalf("%s: handled = false; want true", ppuMemoryCommand)
	}
	ms, ok := mAny.(ppu.MemorySpaces)
	if !ok {
		t.Fatalf("%s: body type = %T; want ppu.MemorySpaces", ppuMemoryCommand, mAny)
	}
	if len(ms.VRAM) != 0x800 || len(ms.Palette) != 32 || len(ms.OAM) != 256 || len(ms.CHR) != 0x2000 {
		t.Errorf("MemorySpaces shape: vram=%d pal=%d oam=%d chr=%d; want 2048/32/256/8192",
			len(ms.VRAM), len(ms.Palette), len(ms.OAM), len(ms.CHR))
	}

	_, handled, err = h("some/unknown", nil)
	if err != nil {
		t.Errorf("unknown command: err = %v; want nil", err)
	}
	if handled {
		t.Error("unknown command: handled = true; want false (defer to not-implemented)")
	}
}
