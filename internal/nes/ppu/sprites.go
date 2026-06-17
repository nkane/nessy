package ppu

// Sprite-overflow evaluation. The per-dot pipeline (perdot.go) owns
// sprite evaluation, fetch, and compositing per scanline; this file
// retains only the 2C02's buggy sprite-overflow flag evaluator, which
// prepareSpritesFor calls once per rendering scanline.
//
// OAM byte layout (4 bytes per sprite, 64 sprites total):
//
//	byte 0: Y position (sprite is drawn at Y+1 — the 2C02 latches Y
//	        on the prior scanline, so the visible row is one below)
//	byte 1: tile index (in 8×16 mode bit 0 selects pattern table,
//	        the top half = tile_id & ~1, bottom = tile_id | 1)
//	byte 2: attributes
//	          bits 0-1 : palette select (sprite palette $3F10-$3F1F)
//	          bit 5    : priority (0 = sprite in front of BG, 1 = behind)
//	          bit 6    : horizontal flip
//	          bit 7    : vertical flip
//	byte 3: X position

// evaluateSpriteOverflow reproduces the 2C02's buggy sprite-overflow
// flag (#283). Real silicon evaluates OAM into secondary OAM during
// each visible scanline; once 8 sprites are found it keeps scanning
// for a 9th but, on a NOT-in-range result, increments BOTH the sprite
// index n AND the byte index m (instead of resetting m to 0). The
// floating m makes it read tile-index / attribute / X bytes as if
// they were Y coordinates, producing the hardware-specific false
// positives + false negatives that games like Battletoads lean on.
//
// y is the visible scanline; spriteY in the OAM is stored as
// (drawn_y - 1), so the in-range test compares against oam[base]+1
// matching the compositor's spriteY convention.
func (p *PPU) evaluateSpriteOverflow(y, spriteH int) {
	n := 0 // sprite index 0..63
	m := 0 // byte index within a sprite; should stay 0, the bug drifts it
	count := 0
	for n < 64 {
		yByte := int(p.oam[(4*n+m)&0xFF])
		spriteY := yByte + 1
		inRange := y >= spriteY && y < spriteY+spriteH
		if count < 8 {
			if inRange {
				count++
			}
			n++ // normal scan: advance to next sprite, m stays 0
			continue
		}
		// count == 8 — scanning for the 9th sprite.
		if inRange {
			p.status |= 0x20 // overflow latched
			return
		}
		// Hardware bug: m drifts alongside n on a miss.
		m = (m + 1) & 3
		n++
	}
}
