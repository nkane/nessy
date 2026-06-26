package main

import "github.com/nkane/chippy/cpu"

// dmaBus wraps the CPU's MMIO bus to reproduce the 2A03's DMA-read
// bus-conflict glitch (#20). chippy's CPU owns *when* a DMC/OAM DMA
// steals a cycle and tags each DMA read with a cpu.DmaKind (chippy#481
// DmaReadBus seam); the open-bus latch and the internal-register
// conflict formula are host-owned and reconstructed here against
// MesenCE's NesCpu::ProcessDmaRead.
//
// The glitch: if the CPU is halted by a DMC DMA *while reading an
// internal register* ($4000-$401F), the DMA unit's own fetch is
// redirected to / conflicts with that internal register. Side effects:
// a stray $4015 read (clears the frame IRQ), controller bit-deletions
// on $4016/$4017, and corruption of the byte the DMC plays. The
// dmc_dma_during_read suite (dma_2007_read, nessy #20) pins this.
//
// dmaBus embeds *cpu.MMIO so every other MMIO method (Register, Tick,
// Peek, Freeze, …) is promoted unchanged; only Read/Write are
// overridden (to latch the external open-bus value) and ReadDma is
// added.
type dmaBus struct {
	*cpu.MMIO

	// openBus is the last value driven on the external CPU data bus.
	// Internal-register reads ($4015-$4017) do NOT update it.
	openBus byte
	// haltAddr is the address the CPU was reading when the DMA halt
	// fired — captured from the halt-cycle dummy read (DmaDummyRead),
	// which chippy issues at the CPU's pending read address. It decides
	// whether the internal-register glitch is armed.
	haltAddr uint16
	// prevReadAddr tracks the last DMA-read target so a back-to-back
	// read of the same controller port can skip its bit-deletion
	// (MesenCE isNesBehavior).
	prevReadAddr uint16
	// isPAL gates the PAL controller-read behavior (no bit deletions).
	isPAL bool
}

// newDMABus wraps mmio. region PAL flag tunes the $4016/$4017 path.
func newDMABus(mmio *cpu.MMIO, isPAL bool) *dmaBus {
	return &dmaBus{MMIO: mmio, isPAL: isPAL}
}

// Read forwards to MMIO and latches the value as external open bus.
func (b *dmaBus) Read(addr uint16) byte {
	v := b.MMIO.Read(addr)
	b.openBus = v
	return v
}

// Write forwards to MMIO and latches the value (writes also drive the
// data bus).
func (b *dmaBus) Write(addr uint16, v byte) {
	b.MMIO.Write(addr, v)
	b.openBus = v
}

// ReadDma routes a DMA-window bus read. The halt-cycle and alignment
// dummy reads (DmaDummyRead) carry the CPU's pending read address —
// captured into haltAddr and performed for real (so a dummy read of
// $2007 still increments PPUADDR, the double-2007-read glitch). The
// sprite read is an ordinary external read. The DMC sample read runs
// the ProcessDmaRead conflict formula.
func (b *dmaBus) ReadDma(addr uint16, kind cpu.DmaKind) byte {
	switch kind {
	case cpu.DmaDummyRead:
		b.haltAddr = addr
		return b.Read(addr)
	case cpu.DmaSpriteRead:
		return b.Read(addr)
	case cpu.DmaDmcRead:
		return b.processDmcRead(addr)
	default:
		return b.Read(addr)
	}
}

// processDmcRead ports MesenCE NesCpu::ProcessDmaRead for the DMC
// sample fetch. addr is the DMC's intended read address. When the CPU
// was halted reading $4000-$401F (enableInternalRegReads), the fetch is
// redirected to internalAddr = $4000 | (addr & $1F).
func (b *dmaBus) processDmcRead(addr uint16) byte {
	enableInternal := (b.haltAddr & 0xFFE0) == 0x4000
	if !enableInternal {
		// The DMC reads its own address. The readable 2A03 registers
		// ($4015-$401A) can't be seen by DMA in this case — they return
		// open bus instead.
		if addr >= 0x4015 && addr <= 0x401A {
			b.prevReadAddr = addr
			return b.openBus
		}
		b.prevReadAddr = addr
		return b.Read(addr)
	}

	internalAddr := uint16(0x4000) | (addr & 0x1F)
	isSame := internalAddr == addr
	var val byte
	switch internalAddr {
	case 0x4015:
		// $4015 reads update only the internal CPU bus, not external
		// open bus. Reading it here is the stray read that acks the
		// frame-counter IRQ behind the program's back.
		val = b.MMIO.Read(0x4015)
		if !isSame {
			// Also drive the address the DMA actually targeted onto the
			// external bus.
			ext := b.MMIO.Read(addr)
			b.openBus = ext
		}
	case 0x4016, 0x4017:
		if b.isPAL || b.prevReadAddr == internalAddr {
			// Reading the same input port twice in a row skips the read
			// (the controller's /OE stays asserted, so no extra shift) —
			// return the last bus value. PAL never deletes bits.
			val = b.openBus
		} else {
			val = b.MMIO.Read(internalAddr)
			b.openBus = val
		}
		if !isSame {
			// The DMA targeted a different address — read it too and
			// resolve the bus conflict. The controller drives bit 0 on
			// nessy (obMask); cartridge/open-bus bits AND together.
			const obMask = byte(0x01)
			ext := b.MMIO.Read(addr)
			b.openBus = (ext & obMask) | (val &^ obMask)
			val = (ext & obMask) | ((val &^ obMask) & (ext &^ obMask))
		}
	default:
		val = b.Read(addr)
	}
	b.prevReadAddr = internalAddr
	return val
}

// compile-time checks: dmaBus is a full Bus + the DMA-read extension.
var (
	_ cpu.Bus        = (*dmaBus)(nil)
	_ cpu.DmaReadBus = (*dmaBus)(nil)
	_ cpu.Ticker     = (*dmaBus)(nil)
	_ cpu.Peeker     = (*dmaBus)(nil)
)
