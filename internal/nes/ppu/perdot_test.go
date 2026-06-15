package ppu_test

import (
	"testing"

	"github.com/nkane/nessy/internal/nes"
	"github.com/nkane/nessy/internal/nes/ppu"
)

// bgCart is a minimal CHR-backed cart for BG-render tests.
type bgCart struct {
	chr [0x2000]byte
	mir nes.Mirroring
}

func (c *bgCart) PPURead(addr uint16) byte {
	if addr < 0x2000 {
		return c.chr[addr]
	}
	return 0
}
func (c *bgCart) PPUWrite(uint16, byte)    {}
func (c *bgCart) Mirroring() nes.Mirroring { return c.mir }

// setupBGScreen configures a PPU + cart with a deterministic static
// background: a CHR pattern, a tiled nametable, and a palette, scroll 0,
// BG enabled. Returns a PPU ready to render frames.
func setupBGScreen(perDot bool) *ppu.PPU {
	c := &bgCart{mir: nes.MirrorVertical}
	// CHR: give several tiles distinct non-zero pixel patterns.
	for tile := 0; tile < 16; tile++ {
		for row := 0; row < 8; row++ {
			c.chr[tile*16+row] = byte(tile*0x11 + row)   // low plane
			c.chr[tile*16+row+8] = byte(tile*0x07 + row) // high plane
		}
	}
	p := ppu.New(c, nil)
	p.SetPerDotBG(perDot)

	// Palette $3F00..$3F1F with varied entries.
	p.Write(0x2006, 0x3F)
	p.Write(0x2006, 0x00)
	for i := 0; i < 32; i++ {
		p.Write(0x2007, byte((i*3+1)&0x3F))
	}
	// Nametable 0 ($2000): tile index = (x+y) & 15 per cell, + attributes.
	p.Write(0x2006, 0x20)
	p.Write(0x2006, 0x00)
	for i := 0; i < 0x3C0; i++ {
		p.Write(0x2007, byte((i*7)&0x0F))
	}
	for i := 0; i < 0x40; i++ {
		p.Write(0x2007, byte(i*0x1B)) // attribute table
	}
	// $2000: base NT 0, BG pattern table $0000. $2005: scroll 0,0.
	p.Write(0x2000, 0x00)
	p.Write(0x2005, 0x00)
	p.Write(0x2005, 0x00)
	p.Write(0x2001, 0x08) // show BG
	return p
}

// The per-dot BG renderer produces a byte-identical frame to the batched
// renderer on a static (scroll-0, no mid-frame writes) screen — the
// phase-1 acceptance gate (#74).
func TestPerDotBG_MatchesBatched(t *testing.T) {
	frame := func(perDot bool) []byte {
		p := setupBGScreen(perDot)
		// Step two full frames so the pipeline is primed + a clean frame
		// is published.
		for range 2 * nes.NTSC.DotsPerScanline * nes.NTSC.ScanlinesPerFrame {
			p.Tick(1)
		}
		return p.FrameBuffer()
	}
	batched := frame(false)
	perDot := frame(true)

	if len(batched) != len(perDot) {
		t.Fatalf("frame sizes differ: batched %d, per-dot %d", len(batched), len(perDot))
	}
	diff, firstAt := 0, -1
	for i := range batched {
		if batched[i] != perDot[i] {
			diff++
			if firstAt < 0 {
				firstAt = i
			}
		}
	}
	if diff != 0 {
		px := firstAt / 4
		t.Errorf("per-dot BG differs from batched: %d/%d bytes, first at pixel (%d,%d)",
			diff, len(batched), px%ppu.ScreenWidth, px/ppu.ScreenWidth)
	}
}
