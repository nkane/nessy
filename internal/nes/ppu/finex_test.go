package ppu

import "testing"

// Sub-tile horizontal scroll: column 0 is the transparent tile $00
// (black), columns 1+ are tile $01 (white). At fine-X scroll 3, a
// screen pixel near the tile boundary that was black at scroll 0
// slides into the white tile — proof the per-dot fine-X mux (the
// 15-p.x shifter bit select) reaches the rendered pixels, not just
// coarse 8-pixel steps (#282).
func TestPerDot_FineXBitSlide(t *testing.T) {
	bgR, _, _ := paletteRGB(0x0F)
	whiteR, _, _ := paletteRGB(0x30)

	// Scroll 0: screen x=5 maps to source x=5 → tile $00 → black.
	p := twoTilePPU(t)
	p.t, p.v, p.x = 0, 0, 0
	for range 2 * frameDots {
		p.stepDot()
	}
	if got := p.frame[(64*ScreenWidth+5)*4+0]; got != bgR {
		t.Fatalf("scroll 0 x=5 should be black (tile $00); got R=$%02X", got)
	}

	// Fine-X scroll 3: screen x=5 maps to source x=8 → tile $01 → white.
	// coarseX stays 0; only the fine-X latch shifts the window.
	p2 := twoTilePPU(t)
	p2.t, p2.v, p2.x = 0, 0, 3
	for range 2 * frameDots {
		p2.stepDot()
	}
	if got := p2.frame[(64*ScreenWidth+5)*4+0]; got != whiteR {
		t.Errorf("fine-X scroll 3 x=5 should slide into tile $01 (white); got R=$%02X", got)
	}
}
