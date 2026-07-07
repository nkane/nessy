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

	// regBP holds the CPU-register-window ($4000-$4017) breakpoints (#53).
	// dmaBus is the single chokepoint every CPU bus access passes through,
	// so it checks each read/write here — the APU/DMA/joypad analogue of
	// the PPU's own #49 register breakpoints. Never nil (newDMABus inits
	// it); the check is a cheap flag test until a breakpoint is armed.
	regBP *cpuRegBreakpoints
	// dmaActive suppresses regBP checks while a DMC/OAM DMA fetch is in
	// flight (ReadDma calls back into Read for its dummy/redirected
	// reads). A user breakpoint means "my program touched this register",
	// not "the DMA unit's internal fetch did".
	dmaActive bool

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
	return &dmaBus{MMIO: mmio, isPAL: isPAL, regBP: &cpuRegBreakpoints{}}
}

// Read forwards to MMIO and latches the value as external open bus.
func (b *dmaBus) Read(addr uint16) byte {
	v := b.MMIO.Read(addr)
	b.openBus = v
	if !b.dmaActive {
		b.regBP.check(addr, false)
	}
	return v
}

// Write forwards to MMIO and latches the value (writes also drive the
// data bus).
func (b *dmaBus) Write(addr uint16, v byte) {
	b.MMIO.Write(addr, v)
	b.openBus = v
	if !b.dmaActive {
		b.regBP.check(addr, true)
	}
}

// ReadDma routes a DMA-window bus read. The halt-cycle and alignment
// dummy reads (DmaDummyRead) carry the CPU's pending read address —
// captured into haltAddr and performed for real (so a dummy read of
// $2007 still increments PPUADDR, the double-2007-read glitch). The
// sprite read is an ordinary external read. The DMC sample read runs
// the ProcessDmaRead conflict formula.
func (b *dmaBus) ReadDma(addr uint16, kind cpu.DmaKind) byte {
	// Suppress $4000-$4017 breakpoints for the DMA's own fetches (the
	// dummy/redirected reads below route back through Read). Restored on
	// return so the next real CPU access is checked.
	b.dmaActive = true
	defer func() { b.dmaActive = false }()
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

// cpuRegBreakpoint window: the 2A03 CPU register block that the APU, the
// $4014 OAMDMA, and the $4016/$4017 joypad ports respond to. chippy's
// CPU-bus breakpoints and the PPU-side #49 breakpoints can't cover it, so
// dmaBus checks it directly (#53).
const (
	cpuRegLo = 0x4000
	cpuRegHi = 0x4017
)

// regBPFlags records which access directions ($4000-$4017 read / write)
// an armed breakpoint should trip on.
type regBPFlags struct{ read, write bool }

// cpuRegBreakpoints is the $4000-$4017 read/write breakpoint set, mirroring
// the PPU's #49 breakpoint latch: a matching access sets pendingStop,
// which the shared armBreakpointStop predicate drains via takePendingStop.
// `has` keeps the per-access hot path to a single bool test until a
// breakpoint is actually armed.
type cpuRegBreakpoints struct {
	bp          map[uint16]regBPFlags
	has         bool
	pendingStop bool
}

// set arms a breakpoint on addr (must be in $4000-$4017) for the given
// directions; clearing both read+write removes it. Returns false if addr
// is out of the CPU-register window.
func (c *cpuRegBreakpoints) set(addr uint16, read, write bool) bool {
	if addr < cpuRegLo || addr > cpuRegHi {
		return false
	}
	if c.bp == nil {
		c.bp = map[uint16]regBPFlags{}
	}
	if !read && !write {
		delete(c.bp, addr)
	} else {
		c.bp[addr] = regBPFlags{read, write}
	}
	c.has = len(c.bp) > 0
	return true
}

// clear removes all CPU-register breakpoints + any latched stop.
func (c *cpuRegBreakpoints) clear() {
	c.bp = nil
	c.has = false
	c.pendingStop = false
}

// check latches a pending stop if an armed breakpoint matches the access.
func (c *cpuRegBreakpoints) check(addr uint16, write bool) {
	if !c.has {
		return
	}
	if bp, ok := c.bp[addr]; ok && ((write && bp.write) || (!write && bp.read)) {
		c.pendingStop = true
	}
}

// takePendingStop reports + clears whether a breakpoint has fired since
// the last call.
func (c *cpuRegBreakpoints) takePendingStop() bool {
	s := c.pendingStop
	c.pendingStop = false
	return s
}
