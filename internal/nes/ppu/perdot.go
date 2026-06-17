package ppu

// Per-dot background renderer (epic #73). Reproduces the 2C02
// background fetch pipeline one dot at a time: an 8-dot tile fetch
// (nametable / attribute / pattern-low / pattern-high) feeds 16-bit
// pattern + attribute shift registers, and one pixel is composited per
// visible dot from the live `v`/`x` scroll registers. This is the sole
// rendering path; it owns the fetch, shifters, pixel output, and the
// v-increment schedule.

// bgTick runs one dot of the per-dot background pipeline. Called from
// stepDot on visible + pre-render scanlines while rendering is enabled.
// It owns the fetch, shifter, pixel output, and the v-increment schedule
// (dots 1-256 + the 321-336 prefetch) for the per-dot path.
func (p *PPU) bgTick() {
	d := p.dot
	visible := p.scanline < ScreenHeight

	switch {
	case d >= 1 && d <= 256:
		// Fetch + reload (cycle 1 reloads the tile fetched 8 dots ago).
		p.bgLoadTileInfo()
		if d&0x07 == 0 {
			p.incCoarseX()
			if d == 256 {
				p.incY()
			}
		}
		// Draw THEN shift (Mesen ProcessScanlineImpl order).
		if visible {
			p.renderBGPixel(d - 1)
			p.renderSpritePixel(d - 1)
		}
		p.bgShiftLo <<= 1
		p.bgShiftHi <<= 1
		p.bgAttrShiftLo <<= 1
		p.bgAttrShiftHi <<= 1

	case d == 257:
		p.copyXFromT()
		// Sprite eval + fetch for the NEXT scanline happens in the
		// 257-320 window on real silicon; doing it here drives A12 from
		// the real sprite-pattern fetches (#76). Pre-render (no visible
		// next line of its own) prepares line 0.
		next := p.scanline + 1
		if p.scanline == p.timing.PreRenderScanline {
			next = 0
		}
		// Run the sprite-fetch pass on EVERY rendering scanline (incl.
		// the last visible line 239, whose fetches target the off-screen
		// "line 240"). Real silicon does the garbage fetches regardless,
		// so A12 toggles on all 241 rendering scanlines — the MMC3 IRQ
		// counter clocks exactly 241×/frame (mmc3_test 2 #7). The units
		// for an off-screen line are simply never composited.
		p.prepareSpritesFor(next)

	case d >= 321 && d <= 336:
		// Prefetch the next scanline's first two tiles. No per-dot shift
		// + no draw here; instead a full-byte shift at 328/336 moves each
		// fetched tile up so both land in the 16-bit registers.
		p.bgLoadTileInfo()
		if d == 328 || d == 336 {
			p.bgShiftLo <<= 8
			p.bgShiftHi <<= 8
			p.bgAttrShiftLo <<= 8
			p.bgAttrShiftHi <<= 8
			p.incCoarseX()
		}
	}

	if p.scanline == p.timing.PreRenderScanline && d >= 280 && d <= 304 {
		p.copyYFromT()
	}
}

// bgLoadTileInfo runs the 8-dot fetch sub-cycle: reload the shifters at
// dot&7==1 from the previous tile's latches, then fetch NT/AT/pattern
// for the next tile. Mirrors Mesen `NesPpu::LoadTileInfo`.
func (p *PPU) bgLoadTileInfo() {
	p.setCHRContext(false) // background CHR fetches (MMC5 'B' set in 8x16)
	switch p.dot & 0x07 {
	case 1:
		p.reloadBGShifters()
		p.bgFetchNT()
	case 3:
		p.bgFetchAT()
	case 5:
		p.bgFetchPatLo()
	case 7:
		p.bgFetchPatHi()
	}
}

// spriteUnit is one in-range sprite prepared for a scanline's per-dot
// sprite multiplexer.
type spriteUnit struct {
	x        int
	low      byte
	high     byte
	palette  byte
	behindBG bool
	hflip    bool
	isZero   bool // OAM index 0 (drives sprite-0 hit)
}

// prepareSpritesFor evaluates the up-to-8 sprites in range on the given
// display line (OAM order, the 2C02 8-sprite limit) and fetches their
// pattern bytes. Run during dots 257-320 of the PREVIOUS scanline — the
// real sprite-fetch window — so the CHR reads drive the cart's A12 line
// at the correct dot (#76); the fetched units are displayed next line.
func (p *PPU) prepareSpritesFor(line int) {
	p.sprCount = 0
	p.setCHRContext(true)
	spriteH := 8
	if p.ctrl&0x20 != 0 {
		spriteH = 16
	}
	sprPatternBase := uint16(0)
	if p.ctrl&0x08 != 0 && p.ctrl&0x20 == 0 {
		sprPatternBase = 0x1000
	}
	// Garbage sprite fetches fill the unused slots on real silicon, so
	// the sprite pattern table is read (A12 toggled) every scanline even
	// with no sprites in range — which is what drives the MMC3/MMC5
	// scanline IRQ (#76). Reproduce that by reading sprPatternBase for
	// each slot not filled by a real sprite.
	defer func() {
		for s := p.sprCount; s < 8; s++ {
			_ = p.busRead(sprPatternBase)
		}
	}()
	if p.mask&0x10 == 0 {
		return // sprites hidden: only the garbage A12 fetches run
	}
	// Drive the sprite-overflow flag ($2002 bit 5) through the 2C02's
	// buggy evaluator (#283) for the line being evaluated. Real silicon
	// evaluates line N's sprites during line N-1 (dots 65-256), so doing
	// it here at the previous line's dot 257 matches hardware ordering.
	p.evaluateSpriteOverflow(line, spriteH)
	for i := 0; i < 64 && p.sprCount < 8; i++ {
		spriteY := int(p.oam[i*4+0]) + 1
		if line < spriteY || line >= spriteY+spriteH {
			continue
		}
		tileIdx := p.oam[i*4+1]
		attr := p.oam[i*4+2]
		vflip := attr&0x80 != 0
		fineY := line - spriteY
		if vflip {
			fineY = spriteH - 1 - fineY
		}
		var tileAddr uint16
		if spriteH == 16 {
			base := uint16(0)
			if tileIdx&1 != 0 {
				base = 0x1000
			}
			tileNum := uint16(tileIdx & 0xFE)
			if fineY >= 8 {
				tileNum |= 1
			}
			tileAddr = base + tileNum*16 + uint16(fineY&7)
		} else {
			tileAddr = sprPatternBase + uint16(tileIdx)*16 + uint16(fineY)
		}
		p.sprUnits[p.sprCount] = spriteUnit{
			x:        int(p.oam[i*4+3]),
			low:      p.busRead(tileAddr),
			high:     p.busRead(tileAddr + 8),
			palette:  attr & 0x03,
			behindBG: attr&0x20 != 0,
			hflip:    attr&0x40 != 0,
			isZero:   i == 0,
		}
		p.sprCount++
	}
}

// renderSpritePixel composites the winning sprite pixel at column x over
// the BG pixel already drawn this dot, and latches the sprite-0 hit.
// First non-transparent sprite in OAM order wins; priority + sprite-0
// overlap follow the 2C02 rules.
func (p *PPU) renderSpritePixel(x int) {
	canHit := p.mask&0x08 != 0 && p.mask&0x10 != 0
	bgHere := p.bgOpaque[p.scanline*ScreenWidth+x]
	for i := 0; i < p.sprCount; i++ {
		s := &p.sprUnits[i]
		col := x - s.x
		if col < 0 || col >= 8 {
			continue
		}
		bit := uint(col)
		if !s.hflip {
			bit = uint(7 - col)
		}
		val := ((s.high>>bit)&1)<<1 | ((s.low >> bit) & 1)
		if val == 0 {
			continue
		}
		// Sprite-0 hit: opaque sprite-0 pixel over opaque BG (x != 255).
		if s.isZero && canHit && bgHere && x != 255 {
			p.status |= 0x40
		}
		// First opaque sprite owns this pixel; priority decides drawing.
		if !s.behindBG || !bgHere {
			colorIdx := p.palette[0x10|(s.palette<<2)|val]
			r, g, b := paletteRGB(colorIdx)
			off := (p.scanline*ScreenWidth + x) * 4
			p.frame[off+0] = r
			p.frame[off+1] = g
			p.frame[off+2] = b
			p.frame[off+3] = 0xFF
		}
		return
	}
}

// reloadBGShifters ORs the just-fetched tile into the low byte of the
// pattern shifters + replicates its 2-bit attribute across the low byte
// of the attribute shifters (the high bytes hold the tile being drawn).
func (p *PPU) reloadBGShifters() {
	p.bgShiftLo |= uint16(p.bgPatLoLatch)
	p.bgShiftHi |= uint16(p.bgPatHiLatch)
	if p.bgAttrLatch&0x01 != 0 {
		p.bgAttrShiftLo |= 0x00FF
	}
	if p.bgAttrLatch&0x02 != 0 {
		p.bgAttrShiftHi |= 0x00FF
	}
}

func (p *PPU) bgFetchNT() {
	p.bgNTAddr = 0x2000 | (p.v & 0x0FFF)
	p.bgNTLatch = p.busRead(p.bgNTAddr)
}

// bgExtAttrActive reports whether MMC5 extended attributes drive this
// tile's palette + CHR bank from ExRAM instead of the attribute table
// and normal CHR banking (#55, ExRAM mode 1).
func (p *PPU) bgExtAttrActive() bool {
	return p.extAttr != nil && p.extAttr.ExtendedAttributeActive()
}

func (p *PPU) bgFetchAT() {
	if p.bgExtAttrActive() {
		// ExtAttrTile returns the palette + both pattern planes in one
		// shot (it owns the ExRAM CHR-bank selection). Latch all three
		// here; the pattern-fetch stages become no-ops this tile.
		fineY := (p.v >> 12) & 0x07
		pal, low, high := p.extAttr.ExtAttrTile(p.bgNTAddr, p.bgNTLatch, fineY)
		p.bgAttrLatch = pal
		p.bgPatLoLatch = low
		p.bgPatHiLatch = high
		return
	}
	addr := uint16(0x23C0) | (p.v & 0x0C00) | ((p.v >> 4) & 0x38) | ((p.v >> 2) & 0x07)
	at := p.busRead(addr)
	shift := ((p.v >> 4) & 0x04) | (p.v & 0x02)
	p.bgAttrLatch = (at >> shift) & 0x03
}

func (p *PPU) bgPatternBase() uint16 {
	if p.ctrl&0x10 != 0 {
		return 0x1000
	}
	return 0
}

func (p *PPU) bgFetchPatLo() {
	if p.bgExtAttrActive() {
		return // pattern planes already latched by bgFetchAT
	}
	fineY := (p.v >> 12) & 0x07
	p.bgPatLoLatch = p.busRead(p.bgPatternBase() + uint16(p.bgNTLatch)*16 + fineY)
}

func (p *PPU) bgFetchPatHi() {
	if p.bgExtAttrActive() {
		return // pattern planes already latched by bgFetchAT
	}
	fineY := (p.v >> 12) & 0x07
	p.bgPatHiLatch = p.busRead(p.bgPatternBase() + uint16(p.bgNTLatch)*16 + fineY + 8)
}

// renderBackdropPixel paints the universal backdrop ($3F00) at column x
// on a visible scanline when rendering is disabled, clearing bgOpaque so
// nothing composites as "over BG". Used for the rendering-off path that
// the per-dot fetch pipeline (gated on rendering) doesn't cover.
func (p *PPU) renderBackdropPixel(x int) {
	r, g, b := paletteRGB(p.palette[0])
	off := (p.scanline*ScreenWidth + x) * 4
	p.frame[off+0], p.frame[off+1], p.frame[off+2], p.frame[off+3] = r, g, b, 0xFF
	p.bgOpaque[p.scanline*ScreenWidth+x] = false
}

// renderBGPixel composites one background pixel at screen column x using
// the pattern + attribute shifters and the fine-X mux, recording opacity
// in bgOpaque for the sprite compositor + sprite-0 hit.
func (p *PPU) renderBGPixel(x int) {
	// BG-show off ($2001 bit 3) while rendering is still enabled (sprites
	// on): the BG layer is the universal backdrop + no opaque BG pixels
	// (so sprites composite over the backdrop, sprite-0 can't hit).
	if p.mask&0x08 == 0 {
		r, g, b := paletteRGB(p.palette[0])
		off := (p.scanline*ScreenWidth + x) * 4
		p.frame[off+0], p.frame[off+1], p.frame[off+2], p.frame[off+3] = r, g, b, 0xFF
		p.bgOpaque[p.scanline*ScreenWidth+x] = false
		return
	}
	bit := 15 - uint(p.x)
	lo := byte((p.bgShiftLo >> bit) & 1)
	hi := byte((p.bgShiftHi >> bit) & 1)
	val := (hi << 1) | lo

	var colorIdx byte
	if val == 0 {
		colorIdx = p.palette[0]
	} else {
		aLo := byte((p.bgAttrShiftLo >> bit) & 1)
		aHi := byte((p.bgAttrShiftHi >> bit) & 1)
		pal := (aHi << 1) | aLo
		colorIdx = p.palette[(pal<<2)|val]
	}
	r, g, b := paletteRGB(colorIdx)
	off := (p.scanline*ScreenWidth + x) * 4
	p.frame[off+0] = r
	p.frame[off+1] = g
	p.frame[off+2] = b
	p.frame[off+3] = 0xFF
	p.bgOpaque[p.scanline*ScreenWidth+x] = val != 0
}
