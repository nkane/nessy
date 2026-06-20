package cart

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/nkane/nessy/internal/nes"
)

// mmc3RevAHashes maps sha256(PRG||CHR) → true for ROMs whose MMC3 IRQ
// counter is the NEC MMC3 rev-A (vs the default Sharp rev-B). The two
// revisions differ ONLY in the IRQ-on-counter-reload semantics
// (clockA12RevA vs clockA12), and the iNES header does NOT encode which
// chip a cart uses — the Blargg mmc3_test 5 (rev-B) and 6 (rev-A) ROMs
// are byte-identical in their headers (mapper 4, iNES, no submapper).
// So, like Mesen's game database (chip == "MMC3A"), the revision is
// resolved by content hash. Seeded with the rev-A test vehicle; extend
// with real rev-A games (Crystalis, Klax, …) as they're verified.
var mmc3RevAHashes = map[string]bool{
	// Blargg mmc3_test "6-MMC6.nes" — rev-A IRQ reload behaviour.
	"6c1c987f14dc25c7632f5f905b7609faa238d953a6cff0fac38b5993c47d4253": true,
}

// MMC3 is mapper 4 — Nintendo's late-NES workhorse. Powers SMB3,
// Mega Man 3-6, Kirby's Adventure, Crystalis, Battletoads, and
// most late-catalog titles. The headline feature is the scanline
// IRQ that fires on A12 rising edges (PPU pattern-table fetch
// boundary) so games can split the screen into a status bar +
// scrolling playfield using sprite/BG fetch timing rather than
// sprite-0 hit polling.
//
// Register file (write-only at $8000-$FFFF; even/odd address bit
// 0 picks which sub-register):
//
//	$8000 even — Bank select:
//	    bits 0-2: which R0..R7 the next $8001 write lands in.
//	    bit 6:    PRG bank mode.
//	    bit 7:    CHR A12 invert (CHR bank mode).
//	$8001 odd  — Bank data: writes to whichever R0..R7 $8000
//	    last selected.
//	$A000 even — Mirroring (bit 0: 0 = vertical, 1 = horizontal).
//	    Ignored on 4-screen carts.
//	$A001 odd  — PRG-RAM protect (bits 6 + 7). Cosmetic on most
//	    modern emulators; we accept the writes silently.
//	$C000 even — IRQ counter reload value.
//	$C001 odd  — IRQ counter reload (latches; the actual counter
//	    reloads on the NEXT A12 rising edge).
//	$E000 even — IRQ enable off + clear any pending IRQ.
//	$E001 odd  — IRQ enable on.
//
// PRG bank modes (control bit 6):
//
//	mode 0: $8000 = R6, $A000 = R7, $C000 = fixed(N-2), $E000 = fixed(N-1)
//	mode 1: $8000 = fixed(N-2), $A000 = R7, $C000 = R6, $E000 = fixed(N-1)
//
// CHR bank modes (control bit 7):
//
//	mode 0: $0000-$0FFF = R0|R1 (two 2 KiB); $1000-$1FFF = R2..R5 (four 1 KiB)
//	mode 1: $0000-$0FFF = R2..R5 (four 1 KiB); $1000-$1FFF = R0|R1 (two 2 KiB)
//
// R0 + R1 are 2 KiB banks (low bit of the value ignored when used).
// R2..R5 are 1 KiB banks.
//
// IRQ counter timing: decremented on each A12 rising edge during
// rendering. On counter underflow (when armed) the IRQ line goes
// to the CPU via the named source "mmc3". $E000 acks.
type MMC3 struct {
	prg      []byte
	chr      []byte
	chrIsRAM bool
	prgRAM   [0x2000]byte
	battery  bool

	bankSelect byte // last $8000 even write
	bankRegs   [8]byte
	mirrorH    bool // $A000 bit 0
	fourScreen bool
	irqLatch   byte
	irqCounter byte
	irqReload  bool
	irqEnabled bool
	irqPending bool
	prevA12    bool
	// A12 low-time filter (Mesen A12Watcher, minDelay=10 PPU dots). A
	// rising edge only clocks the IRQ counter once A12 has stayed low
	// for >10 dots — so the rapid intra-scanline A12 toggles a per-dot
	// fetch pipeline produces (pattern fetches at $1xxx between NT/AT at
	// $2xxx) don't over-clock the counter. a12Cycle is the PPU's running
	// dot count, pushed via SetA12Cycle before each clock.
	a12Cycle      uint64 // current PPU dot count (set by the PPU)
	a12LastCycle  uint64 // dot count at the previous clockA12 call
	a12CyclesDown uint32 // dots A12 has been continuously low (0 = high)
	// revA selects the NEC rev-A IRQ counter (clockA12RevA) over the
	// default Sharp rev-B (clockA12). The two differ only in the
	// stuck-at-zero fire rule (see clockA12RevA). The iNES header can't
	// encode the chip revision, so revA is set from NES 2.0 sub-mapper 3
	// OR a content-hash lookup (mmc3RevAHashes) for known rev-A carts.
	revA bool

	irqSink   IRQSink
	debugSink nes.DebugEventSink
}

// IRQSink is the cart's view of the CPU's named-source IRQ
// surface from #247. *cpu.CPU satisfies it via AssertIRQSource /
// ClearIRQSource. cmd/nessy/wiring.go calls SetIRQSink after both
// peripherals exist.
type IRQSink interface {
	AssertIRQSource(src string)
	ClearIRQSource(src string)
}

const mmc3IRQSource = "mmc3"

// NewMMC3 constructs an MMC3 cart from a parsed iNES ROM. PRG
// must be a non-zero multiple of 8 KiB (MMC3's PRG bank size);
// CHR is either 0 (CHR-RAM, unusual for MMC3) or a non-zero
// multiple of 1 KiB.
func NewMMC3(rom *nes.ROM) (*MMC3, error) {
	if len(rom.PRG) == 0 || len(rom.PRG)%(8*1024) != 0 {
		return nil, fmt.Errorf("mmc3: PRG must be a non-zero multiple of 8 KiB; got %d bytes", len(rom.PRG))
	}
	c := &MMC3{
		prg:        rom.PRG,
		battery:    rom.Battery,
		fourScreen: rom.Mirroring == nes.MirrorFourScreen,
		revA:       rom.SubMapper == 3 || mmc3IsRevA(rom.PRG, rom.CHR),
	}
	switch {
	case len(rom.CHR) == 0:
		c.chr = make([]byte, 8*1024)
		c.chrIsRAM = true
	case len(rom.CHR)%(1*1024) == 0:
		c.chr = rom.CHR
	default:
		return nil, fmt.Errorf("mmc3: CHR must be 0 or a multiple of 1 KiB; got %d bytes", len(rom.CHR))
	}
	// Power-on mirroring: ROM-header-derived for the first frame
	// before the game writes $A000.
	c.mirrorH = rom.Mirroring == nes.MirrorHorizontal
	return c, nil
}

// mmc3IsRevA reports whether a cart's PRG||CHR content hash is in the
// known rev-A set. The header can't distinguish rev-A from rev-B, so
// the revision is resolved by content (mirrors Mesen's game database).
func mmc3IsRevA(prg, chr []byte) bool {
	h := sha256.New()
	h.Write(prg)
	h.Write(chr)
	return mmc3RevAHashes[hex.EncodeToString(h.Sum(nil))]
}

// SetIRQSink wires the CPU's IRQ-source surface. May be nil for
// headless tests — IRQ flag still tracks; just nothing on the CPU
// line.
func (c *MMC3) SetIRQSink(s IRQSink) { c.irqSink = s }

// SetDebugSink wires the event-viewer sink so a mapper-IRQ assertion is
// recorded at the PPU's current scanline/dot (#44). Optional; nil is fine.
func (c *MMC3) SetDebugSink(s nes.DebugEventSink) { c.debugSink = s }

// recordIRQ stamps a mapper-IRQ event when a debug sink is wired.
func (c *MMC3) recordIRQ() {
	if c.debugSink != nil {
		c.debugSink.RecordDebugEvent(nes.EventMapperIRQ)
	}
}

// CPURead serves $6000-$FFFF.
func (c *MMC3) CPURead(addr uint16) byte {
	switch {
	case addr < 0x6000:
		return 0
	case addr < 0x8000:
		return c.prgRAM[addr-0x6000]
	}
	return c.prg[c.prgOffset(addr)]
}

// CPUWrite handles PRG-RAM at $6000-$7FFF + register writes at
// $8000-$FFFF. Even/odd address bit 0 picks the sub-register
// within each register window.
func (c *MMC3) CPUWrite(addr uint16, v byte) {
	switch {
	case addr < 0x6000:
		return
	case addr < 0x8000:
		c.prgRAM[addr-0x6000] = v
		return
	}
	window := addr & 0xE001
	switch window {
	case 0x8000:
		c.bankSelect = v
	case 0x8001:
		c.bankRegs[c.bankSelect&0x07] = v
	case 0xA000:
		c.mirrorH = v&0x01 != 0
	case 0xA001:
		// PRG-RAM protect — accepted, not enforced.
	case 0xC000:
		c.irqLatch = v
	case 0xC001:
		c.irqReload = true
	case 0xE000:
		c.irqEnabled = false
		c.irqPending = false
		if c.irqSink != nil {
			c.irqSink.ClearIRQSource(mmc3IRQSource)
		}
	case 0xE001:
		c.irqEnabled = true
	}
}

// prgOffset computes the byte offset into c.prg for a CPU address
// in $8000-$FFFF based on the active PRG bank mode + bank
// registers.
func (c *MMC3) prgOffset(addr uint16) int {
	bankSize := 8 * 1024
	totalBanks := len(c.prg) / bankSize
	last := totalBanks - 1
	mode := c.bankSelect & 0x40
	var bank int
	region := (addr >> 13) & 0x03 // 0..3 for $8000/$A000/$C000/$E000
	switch region {
	case 0:
		if mode == 0 {
			bank = int(c.bankRegs[6] & 0x3F)
		} else {
			bank = last - 1
		}
	case 1:
		bank = int(c.bankRegs[7] & 0x3F)
	case 2:
		if mode == 0 {
			bank = last - 1
		} else {
			bank = int(c.bankRegs[6] & 0x3F)
		}
	case 3:
		bank = last
	}
	bank %= totalBanks
	off := int(addr&0x1FFF) + bank*bankSize
	return off % len(c.prg)
}

// PPURead returns CHR data + clocks the A12 IRQ counter on rising
// edges. A12 = bit 12 of the PPU bus address; PPU fetches BG
// patterns from $0000-$0FFF (A12 low) and sprite patterns from
// $1000-$1FFF (A12 high), so during render the line toggles at
// predictable scanline boundaries.
func (c *MMC3) PPURead(addr uint16) byte {
	c.clockA12(addr)
	if addr >= 0x2000 {
		return 0
	}
	return c.chr[c.chrOffset(addr)]
}

// PPUWrite is effective for CHR-RAM carts only. Still clocks A12.
func (c *MMC3) PPUWrite(addr uint16, v byte) {
	c.clockA12(addr)
	if addr >= 0x2000 || !c.chrIsRAM {
		return
	}
	c.chr[c.chrOffset(addr)] = v
}

// PeekCHR reads a CHR byte WITHOUT clocking the A12 IRQ counter — the
// side-effect-free path the debugger's PPU viewer uses to dump the
// pattern tables (#29). MMC3 is the only mapper whose PPURead has a
// side effect (the A12 edge), so it's the only one that needs this;
// the PPU falls back to plain PPURead for every other (pure) mapper.
func (c *MMC3) PeekCHR(addr uint16) byte {
	if addr >= 0x2000 {
		return 0
	}
	return c.chr[c.chrOffset(addr)]
}

// NotifyVRAMAddr clocks the A12 IRQ counter when the PPU drives a new
// VRAM address onto the bus without a CHR fetch — the $2006 second
// write and the non-rendering $2007 auto-increment. Real silicon sees
// A12 follow the PPU address bus regardless of whether a pattern fetch
// is in flight; Blargg mmc3_test 1 (clocking) + 3 (A12_clocking)
// toggle A12 purely through PPUADDR and require the counter to clock.
// Shares the same prevA12 edge state as the CHR-fetch path so the two
// can't double-count a single rising edge. The ppu package calls this
// via its optional vramAddrHook interface (Mesen2
// NesPpu::NotifyVramAddressChange).
func (c *MMC3) NotifyVRAMAddr(addr uint16) { c.clockA12(addr) }

// SetA12Cycle feeds the PPU's running dot count to the A12 low-time
// filter. The PPU calls it before each pattern fetch + PPUADDR-driven
// bus change so clockA12 can measure how long A12 stayed low (the
// Mesen A12Watcher debounce). The ppu package calls this via its
// optional a12Cycler interface.
func (c *MMC3) SetA12Cycle(dot uint64) { c.a12Cycle = dot }

// chrOffset computes the byte offset into c.chr for a PPU address
// in $0000-$1FFF based on the active CHR bank mode + bank
// registers.
func (c *MMC3) chrOffset(addr uint16) int {
	totalBytes := len(c.chr)
	mode := c.bankSelect & 0x80
	// Effective region in 1 KiB slots: 0..7.
	slot := int(addr>>10) & 0x07
	if mode != 0 {
		// CHR A12 invert: swap low/high halves.
		slot ^= 0x04
	}
	var bank int
	switch slot {
	case 0:
		bank = int(c.bankRegs[0] & 0xFE)
	case 1:
		bank = int(c.bankRegs[0] | 0x01)
	case 2:
		bank = int(c.bankRegs[1] & 0xFE)
	case 3:
		bank = int(c.bankRegs[1] | 0x01)
	case 4:
		bank = int(c.bankRegs[2])
	case 5:
		bank = int(c.bankRegs[3])
	case 6:
		bank = int(c.bankRegs[4])
	case 7:
		bank = int(c.bankRegs[5])
	}
	off := bank*1024 + int(addr&0x03FF)
	return off % totalBytes
}

// clockA12 decrements the IRQ counter on every rising edge of bit
// 12 of the PPU address bus. A12 must stay low for at least one
// "filter cycle" before the next rising edge counts — without the
// debounce, fine-X scrolling can produce spurious back-to-back
// triggers.
//
// Real silicon's filter is ~16 CPU cycles; we approximate by
// gating on whether A12 was low last time PPURead/PPUWrite ran.
//
// Two revisions:
//
//	RevB (Sharp, default) — counter==0 OR reload flag → reload to
//	  latch; then if new counter is 0 + IRQ enabled, fire. Behaviour
//	  most games (SMB3, Mega Man 3-6) assume.
//
//	RevA (NEC, sub-mapper 3) — reload flag → reload only; counter==0
//	  reloads but ALSO immediately fires if enabled. The functional
//	  difference: with latch=1, RevB fires every other A12 edge,
//	  RevA fires every edge. Klax depends on RevA.
func (c *MMC3) clockA12(addr uint16) {
	a12 := addr&0x1000 != 0
	c.prevA12 = a12

	// Mesen A12Watcher: accumulate the time A12 stays low; a rise only
	// counts once that low stretch exceeds minDelay (10 PPU dots).
	// a12Cycle is the PPU's monotonic dot count, so the delta is normally
	// >= 0 — but a save-state restore or reset can move it backward;
	// guard the unsigned subtraction so a backward jump can't underflow
	// to a near-max value (which would spuriously satisfy minDelay).
	const minDelay = 10
	if c.a12CyclesDown > 0 && c.a12Cycle > c.a12LastCycle {
		c.a12CyclesDown += uint32(c.a12Cycle - c.a12LastCycle)
	}
	c.a12LastCycle = c.a12Cycle
	rising := false
	if !a12 {
		if c.a12CyclesDown == 0 {
			c.a12CyclesDown = 1 // start counting the low stretch
		}
	} else {
		if c.a12CyclesDown > minDelay {
			rising = true
		}
		c.a12CyclesDown = 0
	}
	if !rising {
		return
	}
	if c.revA {
		c.clockA12RevA()
		return
	}
	if c.irqCounter == 0 || c.irqReload {
		c.irqCounter = c.irqLatch
		c.irqReload = false
	} else {
		c.irqCounter--
	}
	if c.irqCounter == 0 && c.irqEnabled {
		c.irqPending = true
		c.recordIRQ()
		if c.irqSink != nil {
			c.irqSink.AssertIRQSource(mmc3IRQSource)
		}
	}
}

// clockA12RevA implements the NEC MMC3 rev-A IRQ counter. The only
// functional difference vs rev-B is the fire condition: rev-A fires
// only when the counter was reloaded from a NONZERO value or by an
// explicit $C001 reload — a "stuck at zero" clock (counter already 0,
// no reload flag) reloads 0->0 and stays SILENT. Rev-B fires on ANY
// post-clock zero, including the stuck-at-zero case. Mesen MMC3.h
// `NotifyVramAddressChange`: `(count > 0 || _irqReload) && newCount == 0`.
// Blargg mmc3_test 6 (MMC6) pins rev-A; the byte-identical test 5 ROM
// pins rev-B, so the revision is chosen by content hash (see
// mmc3RevAHashes), not the iNES header.
func (c *MMC3) clockA12RevA() {
	before := c.irqCounter
	reloadFlag := c.irqReload
	if c.irqCounter == 0 || c.irqReload {
		c.irqCounter = c.irqLatch
		c.irqReload = false
	} else {
		c.irqCounter--
	}
	// MesenCE RevA (MMC3.h NotifyVramAddressChange): fire only when the
	// counter was reloaded from a NONZERO value or by an explicit $C001
	// reload — i.e. a 0->0 stuck-at-zero clock does NOT fire. RevB fires
	// on any post-clock zero.
	if (before > 0 || reloadFlag) && c.irqCounter == 0 && c.irqEnabled {
		c.irqPending = true
		c.recordIRQ()
		if c.irqSink != nil {
			c.irqSink.AssertIRQSource(mmc3IRQSource)
		}
	}
}

// Mirroring derives from the runtime $A000 bit 0; 4-screen carts
// keep the iNES MirrorFourScreen value (ignored by $A000 writes).
func (c *MMC3) Mirroring() nes.Mirroring {
	if c.fourScreen {
		return nes.MirrorFourScreen
	}
	if c.mirrorH {
		return nes.MirrorHorizontal
	}
	return nes.MirrorVertical
}

// BatteryBacked + PRGRAM match the MMC1 surface so cmd/nessy's
// save/restore handles MMC3 carts identically.
func (c *MMC3) BatteryBacked() bool { return c.battery }
func (c *MMC3) PRGRAM() []byte      { return c.prgRAM[:] }
