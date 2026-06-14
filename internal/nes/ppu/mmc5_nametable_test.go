package ppu_test

import (
	"testing"

	"github.com/nkane/nessy/internal/nes"
	"github.com/nkane/nessy/internal/nes/cart"
	"github.com/nkane/nessy/internal/nes/ppu"
)

// With an MMC5 cart the PPU routes $2000-$2FFF through the cart's
// per-quadrant nametable map: CIRAM banks are PPU-owned + distinct,
// ExRAM + fill quadrants are cart-backed. Exercised through the real
// $2006/$2007 PPU bus path.
func TestPPU_MMC5NametableRouting(t *testing.T) {
	rom := &nes.ROM{Mapper: 5, PRG: make([]byte, 32*1024), CHR: make([]byte, 8*1024)}
	c, err := cart.NewMMC5(rom)
	if err != nil {
		t.Fatalf("NewMMC5: %v", err)
	}
	p := ppu.New(c, nil)

	// q0=CIRAM0, q1=CIRAM1, q2=ExRAM, q3=fill (0b11_10_01_00); ExRAM
	// mode 1; fill tile $3C.
	c.CPUWrite(0x5105, 0xE4)
	c.CPUWrite(0x5104, 1)
	c.CPUWrite(0x5106, 0x3C)

	write := func(addr uint16, v byte) {
		p.Write(0x2006, byte(addr>>8))
		p.Write(0x2006, byte(addr))
		p.Write(0x2007, v)
	}
	read := func(addr uint16) byte {
		p.Write(0x2006, byte(addr>>8))
		p.Write(0x2006, byte(addr))
		_ = p.Read(0x2007) // $2007 reads are buffered — first read primes
		p.Write(0x2006, byte(addr>>8))
		p.Write(0x2006, byte(addr))
		return p.Read(0x2007)
	}

	// CIRAM banks 0 and 1 are independent stores.
	write(0x2000, 0xA1) // q0 → CIRAM bank 0
	write(0x2400, 0xB2) // q1 → CIRAM bank 1
	if got := read(0x2000); got != 0xA1 {
		t.Errorf("$2000 (CIRAM0) = $%02X; want $A1", got)
	}
	if got := read(0x2400); got != 0xB2 {
		t.Errorf("$2400 (CIRAM1) = $%02X; want $B2", got)
	}

	// ExRAM quadrant: writes land in the cart's ExRAM, read back.
	write(0x2800, 0x5D)
	if got := read(0x2800); got != 0x5D {
		t.Errorf("$2800 (ExRAM) = $%02X; want $5D", got)
	}

	// Fill quadrant: read-only, returns the fill tile in the name area.
	if got := read(0x2C00); got != 0x3C {
		t.Errorf("$2C00 (fill) = $%02X; want $3C", got)
	}
	write(0x2C00, 0xFF) // dropped
	if got := read(0x2C00); got != 0x3C {
		t.Errorf("$2C00 after write = $%02X; want $3C (fill is read-only)", got)
	}
}
