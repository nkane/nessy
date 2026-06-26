package main

import (
	"testing"

	"github.com/nkane/chippy/cpu"
)

// recordBus is a flat 64 KiB memory that logs every read address so the
// dmaBus conflict formula can be asserted by the access pattern it
// produces. Reads return the low byte of the address (a stable marker)
// unless seeded.
type recordBus struct {
	mem   [0x10000]byte
	reads []uint16
}

func (r *recordBus) Read(addr uint16) byte {
	r.reads = append(r.reads, addr)
	return r.mem[addr]
}
func (r *recordBus) Write(addr uint16, v byte) { r.mem[addr] = v }

func newTestDMABus() (*dmaBus, *recordBus) {
	rec := &recordBus{}
	mmio := cpu.NewMMIO(rec)
	return newDMABus(mmio, false), rec
}

// A DMC sample read while the CPU was NOT halted on an internal register
// reads the DMC's own address (no glitch).
func TestDMABus_DmcRead_NotArmed(t *testing.T) {
	db, rec := newTestDMABus()
	rec.mem[0xC123] = 0xAB
	// Halt happened on a normal RAM read → glitch disarmed.
	db.ReadDma(0x0042, cpu.DmaDummyRead)
	if got := db.ReadDma(0xC123, cpu.DmaDmcRead); got != 0xAB {
		t.Fatalf("DMC read = $%02X; want $AB (own address)", got)
	}
	last := rec.reads[len(rec.reads)-1]
	if last != 0xC123 {
		t.Errorf("last bus read = $%04X; want $C123", last)
	}
}

// When disarmed, a DMC read that lands on a readable 2A03 register
// ($4015-$401A) sees open bus instead of the register.
func TestDMABus_DmcRead_NotArmed_RegisterOpenBus(t *testing.T) {
	db, rec := newTestDMABus()
	// The halt-cycle dummy read drives the external open bus to $5A;
	// the disarmed $4015 DMC read then sees that, not the register.
	rec.mem[0x0042] = 0x5A
	db.ReadDma(0x0042, cpu.DmaDummyRead) // disarmed halt, drives open bus
	if got := db.ReadDma(0x4015, cpu.DmaDmcRead); got != 0x5A {
		t.Errorf("DMC read of $4015 (disarmed) = $%02X; want $5A (open bus)", got)
	}
}

// The glitch: a DMC read while the CPU was halted reading an internal
// register ($4000-$401F) is redirected to $4000 | (dmcAddr & $1F). A
// sample address whose low 5 bits are $15 redirects to $4015.
func TestDMABus_DmcRead_Armed_RedirectsTo4015(t *testing.T) {
	db, rec := newTestDMABus()
	rec.mem[0x4015] = 0x10 // marker for the internal-register read
	// Halt cycle landed on $4015 → glitch armed.
	db.ReadDma(0x4015, cpu.DmaDummyRead)
	rec.reads = nil
	// DMC's intended address is $C015: low 5 bits = $15 → internalAddr
	// $4015. Not the same address, so $4015 is read AND the external
	// target $C015 is driven onto the bus.
	got := db.ReadDma(0xC015, cpu.DmaDmcRead)
	if got != 0x10 {
		t.Errorf("redirected DMC read = $%02X; want $10 ($4015 value)", got)
	}
	sawInternal, sawExternal := false, false
	for _, a := range rec.reads {
		if a == 0x4015 {
			sawInternal = true
		}
		if a == 0xC015 {
			sawExternal = true
		}
	}
	if !sawInternal {
		t.Errorf("armed DMC read didn't touch $4015; reads=%v", rec.reads)
	}
	if !sawExternal {
		t.Errorf("armed DMC read didn't also drive the external target $C015; reads=%v", rec.reads)
	}
}

// The halt-cycle dummy read is performed for real (side effects intact),
// e.g. a dummy read of $2007 must reach the PPU. recordBus stands in for
// the PPU here — we just assert the read happened at the right address.
func TestDMABus_DummyRead_HitsBus(t *testing.T) {
	db, rec := newTestDMABus()
	db.ReadDma(0x2007, cpu.DmaDummyRead)
	if len(rec.reads) == 0 || rec.reads[len(rec.reads)-1] != 0x2007 {
		t.Errorf("dummy read didn't hit the bus at $2007; reads=%v", rec.reads)
	}
	if db.haltAddr != 0x2007 {
		t.Errorf("haltAddr = $%04X; want $2007 (captured from dummy read)", db.haltAddr)
	}
}
