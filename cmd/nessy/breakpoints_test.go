//go:build nessy

package main

import "testing"

// nessyHostVars resolves the NES timing identifiers and tracks live PPU
// state; unknown names report ok=false.
func TestNessyHostVars(t *testing.T) {
	bus := newTestBus(t)
	r := nessyHostVars(bus)

	for _, name := range []string{"scanline", "dot", "frame"} {
		get, ok := r(name)
		if !ok {
			t.Errorf("%q: ok=false; want true", name)
			continue
		}
		if get == nil {
			t.Errorf("%q: nil getter", name)
		}
	}
	if _, ok := r("bogus"); ok {
		t.Error(`"bogus": ok=true; want false`)
	}

	get, _ := r("scanline")
	if got, want := get(), uint32(bus.ppu.Scanline()); got != want {
		t.Errorf("scanline getter = %d; want %d", got, want)
	}
}

// stepScanlinePredicate is false at arm and fires once the scanline
// advances.
func TestStepScanlinePredicate(t *testing.T) {
	bus := newTestBus(t)
	pred := stepScanlinePredicate(bus)
	if pred() {
		t.Fatal("predicate true before any advance")
	}
	for i := 0; i < 2000 && !pred(); i++ {
		bus.cpu.Step()
	}
	if !pred() {
		t.Error("predicate never fired after scanline advanced")
	}
}

// stepFramePredicate fires once the PPU crosses into the next frame.
func TestStepFramePredicate(t *testing.T) {
	bus := newTestBus(t)
	pred := stepFramePredicate(bus)
	if pred() {
		t.Fatal("predicate true before frame advance")
	}
	for i := 0; i < 40000 && !pred(); i++ {
		bus.cpu.Step()
	}
	if !pred() {
		t.Error("predicate never fired after frame advanced")
	}
}

// runToNMIPredicate fires on the /NMI line's rising edge — reachable by
// enabling NMI and running into vblank.
func TestRunToNMIPredicate(t *testing.T) {
	bus := newTestBus(t)
	bus.ppu.Write(0x2000, 0x80) // enable NMI (PPUCTRL.7)

	pred := runToNMIPredicate(bus)
	fired := false
	for i := 0; i < 50000; i++ {
		bus.cpu.Step()
		if pred() {
			fired = true
			break
		}
	}
	if !fired {
		t.Error("run-to-NMI predicate never fired within a frame of vblank")
	}
}
