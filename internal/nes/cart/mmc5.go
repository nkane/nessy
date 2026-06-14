package cart

import (
	"fmt"

	"github.com/nkane/nessy/internal/nes"
)

// MMC5 is mapper 5 — Nintendo's most capable cartridge ASIC. Powers
// Castlevania III (US), Just Breed, Bandit Kings of Ancient China,
// Romance of the Three Kingdoms II, and Uchuu Keibitai SDF.
//
// This is the Phase-1 implementation (#6): the CPU-side register file +
// PRG / CHR banking + PRG-RAM + the hardware multiplier + the 1 KiB
// ExRAM store. It boots the mapper and runs the CPU side; the
// PPU-integration features that need a richer PPU↔cart seam land in
// follow-up phases:
//
//   - Per-quadrant nametable mapping ($5105), ExRAM-as-nametable, and
//     fill-mode rendering (the registers are decoded + stored here;
//     the PPU just sees the best-effort Mirroring() approximation).
//   - Scanline IRQ via the in-frame PPU-fetch counter ($5203/$5204)
//     — register state is held; detection needs the PPU fetch hook.
//   - Dual CHR banks for 8x16 sprites (the sprite 'A' set is used for
//     all fetches here; the BG 'B' set selection needs fetch context).
//   - Extended-attribute CHR override + vertical split + audio.
//
// Register map (writes at $5000-$5FFF; the $8000+ window is read-only
// banked ROM/RAM):
//
//	$5100 PRG mode (0-3)      $5101 CHR mode (0-3)
//	$5102/$5103 PRG-RAM write-protect latches (need $02 then $01)
//	$5104 ExRAM mode          $5105 nametable mapping
//	$5106 fill tile           $5107 fill colour
//	$5113-$5117 PRG banks     $5120-$512B CHR banks (A:$5120-$5127, B:$5128-$512B)
//	$5130 CHR upper bits      $5203 IRQ target    $5204 IRQ enable/status
//	$5205/$5206 multiplier    $5C00-$5FFF ExRAM
type MMC5 struct {
	prg      []byte
	chr      []byte
	chrIsRAM bool
	prgRAM   []byte // up to 64 KiB; $6000-$7FFF window banks into it
	battery  bool

	// Register file.
	prgMode        byte
	chrMode        byte
	prgRAMProtect1 byte
	prgRAMProtect2 byte
	exramMode      byte
	ntMapping      byte
	fillTile       byte
	fillColor      byte
	chrUpperBits   byte
	prgBanks       [5]byte  // $5113-$5117
	chrBanks       [12]byte // $5120-$512B
	mult1, mult2   byte

	// IRQ register state (detection is a follow-up phase).
	irqTarget  byte
	irqEnabled bool
	irqPending bool

	exram [0x400]byte // $5C00-$5FFF
}

// NewMMC5 constructs an MMC5 cart. PRG must be a non-zero multiple of
// 8 KiB; CHR is 0 (CHR-RAM) or a multiple of 1 KiB.
func NewMMC5(rom *nes.ROM) (*MMC5, error) {
	if len(rom.PRG) == 0 || len(rom.PRG)%(8*1024) != 0 {
		return nil, fmt.Errorf("mmc5: PRG must be a non-zero multiple of 8 KiB; got %d bytes", len(rom.PRG))
	}
	c := &MMC5{
		prg:     rom.PRG,
		battery: rom.Battery,
		// MMC5 boards carry up to 64 KiB of work RAM; allocate the max
		// so any $5113 bank selection lands in-bounds. The battery save
		// persists the whole region.
		prgRAM: make([]byte, 64*1024),
		// Power-on: PRG mode 3 with the last bank fixed at $E000 (the
		// reset vector), matching the common boot expectation.
		prgMode: 3,
	}
	switch {
	case len(rom.CHR) == 0:
		c.chr = make([]byte, 8*1024)
		c.chrIsRAM = true
	case len(rom.CHR)%(1*1024) == 0:
		c.chr = rom.CHR
	default:
		return nil, fmt.Errorf("mmc5: CHR must be 0 or a multiple of 1 KiB; got %d bytes", len(rom.CHR))
	}
	// $5117 powers on pointing at the last 8 KiB PRG bank so the reset
	// vector resolves before the game writes any banking register.
	c.prgBanks[4] = 0xFF
	return c, nil
}

// CPURead serves $5000-$FFFF.
func (c *MMC5) CPURead(addr uint16) byte {
	switch {
	case addr == 0x5204:
		// IRQ status: bit 7 = pending, bit 6 = in-frame. Reading clears
		// the pending flag. (In-frame tracking is a follow-up phase.)
		v := byte(0)
		if c.irqPending {
			v |= 0x80
		}
		c.irqPending = false
		return v
	case addr == 0x5205:
		return byte(uint16(c.mult1) * uint16(c.mult2)) // product low
	case addr == 0x5206:
		return byte((uint16(c.mult1) * uint16(c.mult2)) >> 8) // product high
	case addr >= 0x5C00 && addr <= 0x5FFF:
		// ExRAM readable in RAM modes (2, 3); write-only modes read 0.
		if c.exramMode >= 2 {
			return c.exram[addr-0x5C00]
		}
		return 0
	case addr >= 0x6000 && addr < 0x8000:
		return c.prgRAM[c.prgRAMOffset(addr)]
	case addr >= 0x8000:
		isRAM, off := c.prgOffset(addr)
		if isRAM {
			return c.prgRAM[off]
		}
		return c.prg[off]
	}
	return 0
}

// CPUWrite handles the register file, ExRAM, and PRG-RAM.
func (c *MMC5) CPUWrite(addr uint16, v byte) {
	switch {
	case addr == 0x5100:
		c.prgMode = v & 0x03
	case addr == 0x5101:
		c.chrMode = v & 0x03
	case addr == 0x5102:
		c.prgRAMProtect1 = v & 0x03
	case addr == 0x5103:
		c.prgRAMProtect2 = v & 0x03
	case addr == 0x5104:
		c.exramMode = v & 0x03
	case addr == 0x5105:
		c.ntMapping = v
	case addr == 0x5106:
		c.fillTile = v
	case addr == 0x5107:
		c.fillColor = v & 0x03
	case addr >= 0x5113 && addr <= 0x5117:
		c.prgBanks[addr-0x5113] = v
	case addr >= 0x5120 && addr <= 0x512B:
		c.chrBanks[addr-0x5120] = v
	case addr == 0x5130:
		c.chrUpperBits = v & 0x03
	case addr == 0x5203:
		c.irqTarget = v
	case addr == 0x5204:
		c.irqEnabled = v&0x80 != 0
	case addr == 0x5205:
		c.mult1 = v
	case addr == 0x5206:
		c.mult2 = v
	case addr >= 0x5C00 && addr <= 0x5FFF:
		// Writable in ExRAM modes 0/1/2 (mode 3 is read-only).
		if c.exramMode != 3 {
			c.exram[addr-0x5C00] = v
		}
	case addr >= 0x6000 && addr < 0x8000:
		if c.prgRAMWritable() {
			c.prgRAM[c.prgRAMOffset(addr)] = v
		}
	}
	// $5000-$5015 (audio) + $8000+ (ROM) writes are no-ops in Phase 1.
}

// prgRAMWritable reports whether PRG-RAM writes are enabled — the two
// protect latches must hold the $02 / $01 unlock pattern.
func (c *MMC5) prgRAMWritable() bool {
	return c.prgRAMProtect1 == 0x02 && c.prgRAMProtect2 == 0x01
}

// prgRAMOffset maps $6000-$7FFF through the $5113 bank selector into the
// work-RAM slice.
func (c *MMC5) prgRAMOffset(addr uint16) int {
	bank := int(c.prgBanks[0] & 0x07) // $5113, 8 KiB banks
	off := bank*0x2000 + int(addr-0x6000)
	return off % len(c.prgRAM)
}

// prgOffset resolves a $8000-$FFFF address to (isRAM, offset). bit 7 of
// a bank register selects ROM (1) vs RAM (0); $5117 is always ROM.
func (c *MMC5) prgOffset(addr uint16) (bool, int) {
	// region: 0=$8000 1=$A000 2=$C000 3=$E000 (8 KiB granularity)
	region := int((addr >> 13) & 0x03)
	var reg byte // the $511x bank register value driving this region
	switch c.prgMode {
	case 0:
		reg = c.prgBanks[4] // 32 KiB from $5117
	case 1:
		if region < 2 {
			reg = c.prgBanks[2] // $5115, 16 KiB at $8000
		} else {
			reg = c.prgBanks[4] // $5117, 16 KiB at $C000
		}
	case 2:
		switch region {
		case 0, 1:
			reg = c.prgBanks[2] // $5115, 16 KiB at $8000
		case 2:
			reg = c.prgBanks[3] // $5116, 8 KiB at $C000
		default:
			reg = c.prgBanks[4] // $5117, 8 KiB at $E000
		}
	default: // mode 3 — four 8 KiB banks
		reg = c.prgBanks[region+1] // $5114-$5117
	}
	last := region == 3 // $E000 region is always ROM ($5117)
	isROM := last || reg&0x80 != 0
	bank8k := int(reg & 0x7F)
	// 32K/16K modes align the bank number to the window size.
	switch {
	case c.prgMode == 0:
		bank8k &^= 0x03
	case c.prgMode == 1, c.prgMode == 2 && region < 2:
		bank8k &^= 0x01
	}
	off := bank8k*0x2000 + int(addr&0x1FFF)
	if isROM {
		return false, off % len(c.prg)
	}
	return true, off % len(c.prgRAM)
}

// PPURead returns CHR; PPUWrite is effective for CHR-RAM.
func (c *MMC5) PPURead(addr uint16) byte {
	if addr >= 0x2000 {
		return 0
	}
	return c.chr[c.chrOffset(addr)]
}

func (c *MMC5) PPUWrite(addr uint16, v byte) {
	if addr >= 0x2000 || !c.chrIsRAM {
		return
	}
	c.chr[c.chrOffset(addr)] = v
}

// chrOffset maps a $0000-$1FFF PPU address through the active CHR bank
// mode. Phase 1 uses the sprite 'A' set ($5120-$5127) for every fetch;
// the 8x16 BG 'B' set needs PPU fetch context (follow-up phase).
func (c *MMC5) chrOffset(addr uint16) int {
	upper := int(c.chrUpperBits) << 8 // $5130 high bits
	var bank, size int
	switch c.chrMode {
	case 0: // 8 KiB
		bank = int(c.chrBanks[7]) | upper
		size = 0x2000
	case 1: // 4 KiB — $5123 ($0000-$0FFF), $5127 ($1000-$1FFF)
		idx := 3
		if addr >= 0x1000 {
			idx = 7
		}
		bank = int(c.chrBanks[idx]) | upper
		size = 0x1000
	case 2: // 2 KiB
		idx := 1 + int(addr>>11)*2 // $5121/$5123/$5125/$5127
		bank = int(c.chrBanks[idx]) | upper
		size = 0x800
	default: // 1 KiB
		idx := int(addr >> 10) // $5120-$5127
		bank = int(c.chrBanks[idx]) | upper
		size = 0x400
	}
	off := bank*size + int(addr)%size
	return off % len(c.chr)
}

// Mirroring is a best-effort map of $5105's per-quadrant nametable
// selection onto nessy's mirroring enum. Full per-quadrant control
// (ExRAM / fill quadrants) is a follow-up phase; the common
// horizontal / vertical / single-screen layouts decode correctly.
func (c *MMC5) Mirroring() nes.Mirroring {
	q0 := c.ntMapping & 0x03
	q1 := (c.ntMapping >> 2) & 0x03
	q2 := (c.ntMapping >> 4) & 0x03
	q3 := (c.ntMapping >> 6) & 0x03
	switch {
	case q0 == 0 && q1 == 1 && q2 == 0 && q3 == 1:
		return nes.MirrorVertical
	case q0 == 0 && q1 == 0 && q2 == 1 && q3 == 1:
		return nes.MirrorHorizontal
	case q0 == 0 && q1 == 0 && q2 == 0 && q3 == 0:
		return nes.MirrorSingleLower
	case q0 == 1 && q1 == 1 && q2 == 1 && q3 == 1:
		return nes.MirrorSingleUpper
	default:
		return nes.MirrorHorizontal
	}
}

// nametableID returns $5105's 2-bit source code for the quadrant
// containing a $2000-$2FFF (or mirrored $3000-$3EFF) address:
// 0/1 = CIRAM bank, 2 = ExRAM, 3 = fill mode.
func (c *MMC5) nametableID(addr uint16) byte {
	quadrant := (addr >> 10) & 0x03
	return (c.ntMapping >> (quadrant * 2)) & 0x03
}

// MapNametable reports the physical CIRAM bank (0/1) for the quadrant,
// or -1 when the cart backs it (ExRAM / fill / empty). Implements the
// ppu nametableMapper hook (#55).
func (c *MMC5) MapNametable(addr uint16) int {
	switch c.nametableID(addr) {
	case 0:
		return 0
	case 1:
		return 1
	default: // 2 (ExRAM) or 3 (fill) — cart-backed
		return -1
	}
}

// ReadNametable serves a cart-backed quadrant. ExRAM ($5105 code 2) acts
// as a nametable only in ExRAM modes 0/1 (otherwise the quadrant reads
// as empty); fill mode ($5105 code 3) returns the fill tile across the
// name area and the replicated fill colour across the attribute area.
func (c *MMC5) ReadNametable(addr uint16) byte {
	off := addr & 0x03FF
	switch c.nametableID(addr) {
	case 2:
		if c.exramMode <= 1 {
			return c.exram[off]
		}
		return 0 // ExRAM as RAM (modes 2/3) → empty nametable
	default: // fill mode
		if off < 0x03C0 {
			return c.fillTile
		}
		// Attribute byte: the 2-bit fill colour in all four 2-bit slots.
		return c.fillColor * 0x55
	}
}

// WriteNametable absorbs a write to a cart-backed quadrant — only ExRAM
// (modes 0/1) is writable; fill / empty quadrants drop the write.
func (c *MMC5) WriteNametable(addr uint16, v byte) {
	if c.nametableID(addr) == 2 && c.exramMode <= 1 {
		c.exram[addr&0x03FF] = v
	}
}

func (c *MMC5) BatteryBacked() bool { return c.battery }

// PRGRAM exposes the first 8 KiB work-RAM bank for save / restore — the
// battery region commercial MMC5 boards persist.
func (c *MMC5) PRGRAM() []byte {
	if !c.battery {
		return nil
	}
	return c.prgRAM[:0x2000]
}
