package ppu

import (
	"testing"

	"github.com/nkane/nessy/internal/nes"
)

// twoTilePPU builds a PPU whose nametable 0 holds tile $00 in column
// 0 and tile $01 everywhere else, so a horizontal scroll shift is
// visually detectable as the column-0 tile sliding off-screen.
// Pattern table:
//
//	tile $00: low plane all 0s   → pixel value 0 everywhere
//	tile $01: low plane all 1s   → pixel value 1 everywhere
//
// With BG show + palette[0]=$0F (black) + palette[1]=$30 (white-ish),
// tile $00 reads black and tile $01 reads white.
func twoTilePPU(t *testing.T) *PPU {
	t.Helper()
	cart := &fakeCart{mir: nes.MirrorHorizontal}
	// tile $00: empty pattern (all transparent → universal bg)
	// tile $01: low plane all 1s
	for r := range 8 {
		cart.chr[0x10+r] = 0xFF // low plane of tile $01
		cart.chr[0x18+r] = 0x00 // high plane of tile $01
	}
	p := New(cart, nil)
	clearOAM(p)
	p.Write(0x2001, 0x08) // BG show
	// Palette
	p.Write(0x2006, 0x3F)
	p.Write(0x2006, 0x00)
	p.Write(0x2007, 0x0F) // universal bg = black
	p.Write(0x2007, 0x30) // BG[1]
	// Fill nametable 0 with tile $01 everywhere, then make column 0
	// tile $00 (transparent). Poke vram directly.
	for row := range 30 {
		for col := range 32 {
			if col == 0 {
				p.vram[row*32+col] = 0x00
			} else {
				p.vram[row*32+col] = 0x01
			}
		}
	}
	return p
}

// Horizontal scroll: shift left by 8 px (coarse-X = 1). The column-0
// tile $00 (transparent) moves off-screen, so screen x=0 now reads
// tile $01 (white). Driven through the per-dot pipeline from the live
// v register.
func TestPerDot_HorizontalScrollShiftsLeft(t *testing.T) {
	bgR, _, _ := paletteRGB(0x0F)
	whiteR, _, _ := paletteRGB(0x30)

	// Scroll 0: screen x=0 = tile $00 → black.
	p := twoTilePPU(t)
	p.t, p.v, p.x = 0, 0, 0
	for range 2 * frameDots {
		p.stepDot()
	}
	if got := p.frame[(64*ScreenWidth+0)*4+0]; got != bgR {
		t.Fatalf("scroll 0 x=0 should be tile $00 (black); got R=$%02X", got)
	}

	// Scroll +8 (coarse-X = 1): screen x=0 now shows tile $01 (white).
	p2 := twoTilePPU(t)
	p2.t, p2.v, p2.x = 0x0001, 0x0001, 0
	for range 2 * frameDots {
		p2.stepDot()
	}
	if got := p2.frame[(64*ScreenWidth+0)*4+0]; got != whiteR {
		t.Errorf("scroll +8 x=0 should show tile $01 (white); got R=$%02X", got)
	}
}

// Horizontal nametable wrap: scroll-X = 200 with vertical mirroring.
// NT0 is filled with the opaque tile $05 (white); NT1 stays at the
// default tile $00 (black). As the per-dot coarse-X increments past 31
// it flips the horizontal nametable bit, so the right side of the
// screen crosses into NT1 → black.
func TestPerDot_HorizontalNametableWrap(t *testing.T) {
	cart := &fakeCart{mir: nes.MirrorVertical}
	// Tile $05 in pattern table: all-opaque.
	for r := range 8 {
		cart.chr[0x50+r] = 0xFF
	}
	p := New(cart, nil)
	clearOAM(p)
	p.Write(0x2001, 0x08)
	p.Write(0x2006, 0x3F)
	p.Write(0x2006, 0x00)
	p.Write(0x2007, 0x0F)
	p.Write(0x2007, 0x30)
	// Pre-load NT0 with tile $05 everywhere; NT1 stays default tile $00.
	for i := range p.vram[:0x400] {
		p.vram[i] = 0x05
	}
	// Scroll-X = 200 → coarse-X 25, fine-X 0. Base nametable 0.
	p.t, p.v, p.x = 0x0019, 0x0019, 0
	for range 2 * frameDots {
		p.stepDot()
	}

	whiteR, _, _ := paletteRGB(0x30)
	bgR, _, _ := paletteRGB(0x0F)
	// x=0: source coarse-X 25 → still NT0 → tile $05 (white).
	if got := p.frame[(50*ScreenWidth+0)*4+0]; got != whiteR {
		t.Errorf("x=0 should be NT0 tile $05 (white); got R=$%02X", got)
	}
	// x=100: source effX 300 wraps past 256 into NT1 → tile $00 (black).
	if got := p.frame[(50*ScreenWidth+100)*4+0]; got != bgR {
		t.Errorf("x=100 (post-wrap) should be NT1 tile $00 (black); got R=$%02X", got)
	}
}
