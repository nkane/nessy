//go:build nessy

package main

import (
	"encoding/json"
	"testing"

	"github.com/nkane/nessy/internal/nes"
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

	_, handled, err = h("some/unknown", nil)
	if err != nil {
		t.Errorf("unknown command: err = %v; want nil", err)
	}
	if handled {
		t.Error("unknown command: handled = true; want false (defer to not-implemented)")
	}
}
