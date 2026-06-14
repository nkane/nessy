package cart

import (
	"testing"

	"github.com/nkane/nessy/internal/nes"
)

// fillMMC5Rom builds a mapper-5 ROM with each 8 KiB PRG bank + 1 KiB CHR
// bank stamped with its index at its first byte, so banking tests can
// identify which bank a read landed in.
func fillMMC5Rom(t *testing.T, prgBanks, chrBanks int) *nes.ROM {
	t.Helper()
	prg := make([]byte, prgBanks*8*1024)
	for i := range prgBanks {
		prg[i*8*1024] = byte(i)
	}
	chr := make([]byte, chrBanks*1024)
	for i := range chrBanks {
		chr[i*1024] = byte(i)
	}
	return &nes.ROM{Mapper: 5, PRG: prg, CHR: chr, Battery: true}
}

// $5117 powers on at the last bank so the reset vector resolves before
// any banking write.
func TestMMC5_PowerOnLastBank(t *testing.T) {
	c, err := NewMMC5(fillMMC5Rom(t, 8, 8)) // 8 × 8 KiB = 64 KiB PRG
	if err != nil {
		t.Fatalf("NewMMC5: %v", err)
	}
	// mode 3, $E000 region = $5117 = $FF → masked to bank 7 (last).
	if got := c.CPURead(0xE000); got != 7 {
		t.Errorf("$E000 bank marker = %d; want 7 (last bank)", got)
	}
}

// The hardware multiplier returns the 16-bit product split across
// $5205 (low) / $5206 (high). Acceptance item for #6.
func TestMMC5_Multiplier(t *testing.T) {
	c, _ := NewMMC5(fillMMC5Rom(t, 8, 8))
	cases := [][2]byte{{0, 0}, {1, 1}, {0xFF, 0xFF}, {0x12, 0x34}, {200, 3}}
	for _, tc := range cases {
		c.CPUWrite(0x5205, tc[0])
		c.CPUWrite(0x5206, tc[1])
		want := uint16(tc[0]) * uint16(tc[1])
		lo := c.CPURead(0x5205)
		hi := c.CPURead(0x5206)
		if got := uint16(hi)<<8 | uint16(lo); got != want {
			t.Errorf("%d * %d = %d; want %d", tc[0], tc[1], got, want)
		}
	}
}

// PRG mode 3 banks four independent 8 KiB windows from $5114-$5117.
func TestMMC5_PRGMode3Banking(t *testing.T) {
	c, _ := NewMMC5(fillMMC5Rom(t, 8, 8))
	c.CPUWrite(0x5100, 3)      // PRG mode 3
	c.CPUWrite(0x5114, 0x80|2) // $8000 → bank 2 (ROM bit set)
	c.CPUWrite(0x5115, 0x80|4) // $A000 → bank 4
	c.CPUWrite(0x5116, 0x80|5) // $C000 → bank 5
	c.CPUWrite(0x5117, 6)      // $E000 → bank 6 (always ROM)
	for region, want := range map[uint16]byte{0x8000: 2, 0xA000: 4, 0xC000: 5, 0xE000: 6} {
		if got := c.CPURead(region); got != want {
			t.Errorf("$%04X bank = %d; want %d", region, got, want)
		}
	}
}

// PRG mode 0 maps one 32 KiB bank from $5117 (bank aligned to 4).
func TestMMC5_PRGMode0Banking(t *testing.T) {
	c, _ := NewMMC5(fillMMC5Rom(t, 8, 8))
	c.CPUWrite(0x5100, 0)      // 32 KiB mode
	c.CPUWrite(0x5117, 0x80|5) // bank 5 → aligned to 4 → banks 4..7
	if got := c.CPURead(0x8000); got != 4 {
		t.Errorf("$8000 = %d; want 4 (32 KiB-aligned)", got)
	}
}

// CHR 1 KiB mode banks each $0400 slot from $5120-$5127.
func TestMMC5_CHR1KBanking(t *testing.T) {
	c, _ := NewMMC5(fillMMC5Rom(t, 8, 16))
	c.CPUWrite(0x5101, 3) // 1 KiB CHR mode
	for slot := range 8 {
		c.CPUWrite(uint16(0x5120+slot), byte(8+slot)) // map slot → bank 8+slot
	}
	for slot := range 8 {
		addr := uint16(slot) * 0x400
		if got := c.PPURead(addr); got != byte(8+slot) {
			t.Errorf("CHR slot %d ($%04X) = %d; want %d", slot, addr, got, 8+slot)
		}
	}
}

// ExRAM is read/write in mode 2, read-only in mode 3, write-only in 0/1.
func TestMMC5_ExRAMModes(t *testing.T) {
	c, _ := NewMMC5(fillMMC5Rom(t, 8, 8))
	c.CPUWrite(0x5104, 2) // RAM read/write
	c.CPUWrite(0x5C00, 0x42)
	if got := c.CPURead(0x5C00); got != 0x42 {
		t.Errorf("ExRAM mode 2 read = $%02X; want $42", got)
	}
	// Mode 3: read-only — write ignored, prior value holds.
	c.CPUWrite(0x5104, 3)
	c.CPUWrite(0x5C00, 0x99)
	if got := c.CPURead(0x5C00); got != 0x42 {
		t.Errorf("ExRAM mode 3 held = $%02X; want $42 (write-protected)", got)
	}
	// Mode 0: write-only — read returns 0.
	c.CPUWrite(0x5104, 0)
	if got := c.CPURead(0x5C00); got != 0 {
		t.Errorf("ExRAM mode 0 read = $%02X; want 0 (write-only)", got)
	}
}

// PRG-RAM writes need the $02 / $01 protect-latch unlock.
func TestMMC5_PRGRAMWriteProtect(t *testing.T) {
	c, _ := NewMMC5(fillMMC5Rom(t, 8, 8))
	c.CPUWrite(0x5113, 0) // $6000 → RAM bank 0
	// Locked by default: write dropped.
	c.CPUWrite(0x6000, 0x11)
	if got := c.CPURead(0x6000); got == 0x11 {
		t.Error("PRG-RAM write landed while protected")
	}
	// Unlock + write.
	c.CPUWrite(0x5102, 0x02)
	c.CPUWrite(0x5103, 0x01)
	c.CPUWrite(0x6000, 0x22)
	if got := c.CPURead(0x6000); got != 0x22 {
		t.Errorf("PRG-RAM after unlock = $%02X; want $22", got)
	}
}

// $5105 maps each nametable quadrant to a CIRAM bank, ExRAM, or fill.
func TestMMC5_NametableMapping(t *testing.T) {
	c, _ := NewMMC5(fillMMC5Rom(t, 8, 8))
	// q0=CIRAM0(0), q1=CIRAM1(1), q2=ExRAM(2), q3=fill(3): 0b11_10_01_00.
	c.CPUWrite(0x5105, 0xE4)
	c.CPUWrite(0x5104, 1)    // ExRAM mode 1 → ExRAM usable as a nametable
	c.CPUWrite(0x5106, 0x2A) // fill tile
	c.CPUWrite(0x5107, 0x03) // fill colour (2 bits)

	if got := c.MapNametable(0x2000); got != 0 {
		t.Errorf("q0 bank = %d; want 0 (CIRAM0)", got)
	}
	if got := c.MapNametable(0x2400); got != 1 {
		t.Errorf("q1 bank = %d; want 1 (CIRAM1)", got)
	}
	if got := c.MapNametable(0x2800); got != -1 {
		t.Errorf("q2 (ExRAM) = %d; want -1 (cart-backed)", got)
	}
	if got := c.MapNametable(0x2C00); got != -1 {
		t.Errorf("q3 (fill) = %d; want -1 (cart-backed)", got)
	}

	// ExRAM quadrant round-trips through the cart.
	c.WriteNametable(0x2810, 0x7C)
	if got := c.ReadNametable(0x2810); got != 0x7C {
		t.Errorf("ExRAM NT read = $%02X; want $7C", got)
	}
	// Fill quadrant: tile byte in the name area, replicated colour in attr.
	if got := c.ReadNametable(0x2C05); got != 0x2A {
		t.Errorf("fill tile = $%02X; want $2A", got)
	}
	if got := c.ReadNametable(0x2FC0); got != 0x03*0x55 {
		t.Errorf("fill attr = $%02X; want $%02X", got, 0x03*0x55)
	}
}

// save / load round-trips the register file + ExRAM + work RAM.
func TestMMC5_SaveLoadRoundTrip(t *testing.T) {
	c, _ := NewMMC5(fillMMC5Rom(t, 8, 8))
	c.CPUWrite(0x5100, 2)
	c.CPUWrite(0x5114, 0x83)
	c.CPUWrite(0x5205, 7)
	c.CPUWrite(0x5206, 9)
	c.CPUWrite(0x5104, 2)
	c.CPUWrite(0x5C10, 0xAB)

	st := c.saveState()
	c2, _ := NewMMC5(fillMMC5Rom(t, 8, 8))
	if err := c2.loadState(*st); err != nil {
		t.Fatalf("loadState: %v", err)
	}
	if c2.prgMode != 2 || c2.prgBanks[1] != 0x83 || c2.mult1 != 7 || c2.mult2 != 9 {
		t.Error("register file did not round-trip")
	}
	c2.CPUWrite(0x5104, 2)
	if got := c2.CPURead(0x5C10); got != 0xAB {
		t.Errorf("ExRAM after restore = $%02X; want $AB", got)
	}
}
