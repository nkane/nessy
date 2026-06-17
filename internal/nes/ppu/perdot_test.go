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
func setupBGScreen() *ppu.PPU {
	c := &bgCart{mir: nes.MirrorVertical}
	// CHR: give several tiles distinct non-zero pixel patterns.
	for tile := 0; tile < 16; tile++ {
		for row := 0; row < 8; row++ {
			c.chr[tile*16+row] = byte(tile*0x11 + row)   // low plane
			c.chr[tile*16+row+8] = byte(tile*0x07 + row) // high plane
		}
	}
	p := ppu.New(c, nil)

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
	// Sprites: 6 sprites on distinct rows (≤1 per scanline, so well under
	// the 8-per-line limit → the per-dot 8-sprite path must still match
	// the batched all-sprites path byte-for-byte). Varied tile, attr, x.
	p.Write(0x2003, 0x00)
	for i := 0; i < 6; i++ {
		p.Write(0x2004, byte(20+i*30))       // Y
		p.Write(0x2004, byte(3+i))           // tile
		p.Write(0x2004, byte((i*0x41)&0xE3)) // attr (palette + flips + priority)
		p.Write(0x2004, byte(16+i*24))       // X
	}

	// $2000: base NT 0, BG + sprite pattern tables $0000. $2005: scroll 0.
	p.Write(0x2000, 0x00)
	p.Write(0x2005, 0x00)
	p.Write(0x2005, 0x00)
	p.Write(0x2001, 0x18) // show BG + sprites
	return p
}

// With the per-dot path the MMC3 scanline IRQ is driven by the REAL
// per-dot fetches (BG at $0xxx, sprite/garbage fetches at $1xxx) — not
// the dot-260 dummy — so it still fires per scanline (#76).
func TestPerDotA12_MMC3ScanlineIRQ(t *testing.T) {
	p, c, sink := newMMC3PPU(t)
	c.CPUWrite(0xC000, 8) // latch = 8
	c.CPUWrite(0xC001, 0) // reload
	c.CPUWrite(0xE001, 0) // enable
	p.Write(0x2000, 0x08) // BG pattern $0000, sprite pattern $1000
	p.Write(0x2001, 0x08) // show BG (rendering enabled)

	for range nes.NTSC.DotsPerScanline * nes.NTSC.ScanlinesPerFrame {
		before := sink.asserts
		p.Tick(1)
		if sink.asserts > before {
			c.CPUWrite(0xE000, 0) // ack
			c.CPUWrite(0xE001, 0) // re-enable
		}
	}
	if sink.asserts < 20 {
		t.Errorf("per-dot MMC3 scanline IRQ fired %d times; want >= 20 (real-fetch A12)", sink.asserts)
	}
}

// The per-dot pipeline renders a deterministic, non-trivial frame from
// a static (scroll-0, no mid-frame writes) screen: two independent runs
// of the same setup produce byte-identical framebuffers, and the result
// is not a flat backdrop fill (the tiled nametable actually rasterized).
func TestPerDotBG_DeterministicStaticFrame(t *testing.T) {
	frame := func() ([]byte, byte) {
		p := setupBGScreen()
		// Step two full frames so the pipeline is primed + a clean frame
		// is published.
		for range 2 * nes.NTSC.DotsPerScanline * nes.NTSC.ScanlinesPerFrame {
			p.Tick(1)
		}
		return p.FrameBuffer(), p.Status() & 0x40 // bit 6 = sprite-0 hit
	}
	a, aS0 := frame()
	b, bS0 := frame()

	if aS0 != bS0 {
		t.Errorf("sprite-0 hit non-deterministic: run1=$%02X run2=$%02X", aS0, bS0)
	}
	if len(a) != len(b) {
		t.Fatalf("frame sizes differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			px := i / 4
			t.Fatalf("per-dot render non-deterministic at pixel (%d,%d)",
				px%ppu.ScreenWidth, px/ppu.ScreenWidth)
		}
	}
	// Not a flat fill: at least two distinct pixel colours rasterized
	// from the tiled nametable.
	distinct := false
	for i := 4; i < len(a); i += 4 {
		if a[i] != a[0] || a[i+1] != a[1] || a[i+2] != a[2] {
			distinct = true
			break
		}
	}
	if !distinct {
		t.Error("per-dot frame is a flat fill; expected the tiled nametable to rasterize")
	}
}
