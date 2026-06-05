package ppu_test

import "testing"

// DecodedRegisters breaks PPUCTRL / PPUMASK into named flags.
func TestDecodedRegisters(t *testing.T) {
	p, _, _ := newMMC3PPU(t)
	// PPUCTRL = $A8: NMI enable (bit7) + 8x16 sprites (bit5) + sprite
	// pattern $1000 (bit3); base nametable 0.
	p.Write(0x2000, 0xA8)
	// PPUMASK = $1E: show BG/sprites + their left columns.
	p.Write(0x2001, 0x1E)

	r := p.DecodedRegisters()
	if r.Ctrl != 0xA8 || r.Mask != 0x1E {
		t.Fatalf("raw regs = ctrl$%02X mask$%02X; want $A8/$1E", r.Ctrl, r.Mask)
	}
	cb := r.CtrlBits
	if !cb.NMIEnable || !cb.Sprite8x16 || !cb.SpritePatternHigh {
		t.Errorf("ctrl decode = nmi%v 8x16%v sprPat%v; want all true", cb.NMIEnable, cb.Sprite8x16, cb.SpritePatternHigh)
	}
	if cb.BGPatternHigh || cb.VRAMIncrement32 || cb.BaseNametable != 0 {
		t.Errorf("ctrl decode unexpected: bgPat%v inc32%v baseNT%d", cb.BGPatternHigh, cb.VRAMIncrement32, cb.BaseNametable)
	}
	mb := r.MaskBits
	if !mb.ShowBG || !mb.ShowSprites || !mb.ShowBGLeft || !mb.ShowSpritesLeft {
		t.Errorf("mask decode = bg%v spr%v bgL%v sprL%v; want all true", mb.ShowBG, mb.ShowSprites, mb.ShowBGLeft, mb.ShowSpritesLeft)
	}
	if mb.Grayscale || mb.EmphasizeR || mb.EmphasizeG || mb.EmphasizeB {
		t.Errorf("mask decode unexpected emphasis/grayscale set")
	}
}

// DebugSpriteViewer decodes OAM into per-sprite fields. Write a sprite
// via $2003/$2004, flip PPUCTRL to 8x16, and verify the decode +
// register context.
func TestDebugSpriteViewer_Decode(t *testing.T) {
	p, _, _ := newMMC3PPU(t)
	p.Write(0x2000, 0x20) // PPUCTRL bit 5 → 8x16 sprites

	// Sprite 0: Y=$20, tile=$05, attr=$E3 (palette 3, behind, H+V flip), X=$40.
	p.Write(0x2003, 0x00) // OAMADDR = 0
	for _, b := range []byte{0x20, 0x05, 0xE3, 0x40} {
		p.Write(0x2004, b)
	}
	// Sprite 1: parked off-screen (Y=$F0 >= $EF).
	p.Write(0x2003, 0x04)
	for _, b := range []byte{0xF0, 0x00, 0x00, 0x00} {
		p.Write(0x2004, b)
	}

	v := p.DebugSpriteViewer()
	if !v.Sprite8x16 {
		t.Error("Sprite8x16 = false; want true (PPUCTRL bit 5 set)")
	}
	if len(v.Sprites) != 64 || len(v.OAM) != 256 {
		t.Fatalf("shape: sprites=%d oam=%d; want 64/256", len(v.Sprites), len(v.OAM))
	}
	s0 := v.Sprites[0]
	if s0.Y != 0x20 || s0.Tile != 0x05 || s0.Attr != 0xE3 || s0.X != 0x40 {
		t.Errorf("sprite0 raw = Y$%02X tile$%02X attr$%02X X$%02X; want $20/$05/$E3/$40", s0.Y, s0.Tile, s0.Attr, s0.X)
	}
	if s0.Palette != 3 || !s0.Priority || !s0.FlipH || !s0.FlipV {
		t.Errorf("sprite0 decode = pal%d pri%v H%v V%v; want 3/true/true/true", s0.Palette, s0.Priority, s0.FlipH, s0.FlipV)
	}
	if !s0.OnScreen {
		t.Error("sprite0 OnScreen = false; want true (Y=$20)")
	}
	if v.Sprites[1].OnScreen {
		t.Error("sprite1 OnScreen = true; want false (Y=$F0)")
	}
}

// DebugPPUViewer must be side-effect-free: dumping the pattern tables
// on an MMC3 cart must NOT clock the A12 IRQ counter (it reads through
// PeekCHR, not PPURead). A spurious IRQ here would corrupt a game's
// scanline timing the instant a user opened the tilemap panel (#29).
func TestDebugPPUViewer_NoA12SideEffect(t *testing.T) {
	p, c, sink := newMMC3PPU(t)
	c.CPUWrite(0xC000, 1) // latch = 1 → would fire on the first A12 edge
	c.CPUWrite(0xC001, 0) // arm reload
	c.CPUWrite(0xE001, 0) // enable IRQ

	v := p.DebugPPUViewer()

	if sink.asserts != 0 {
		t.Errorf("DebugPPUViewer clocked MMC3 A12 %d times; want 0 (must be side-effect-free)", sink.asserts)
	}
	if len(v.PatternTables) != 0x2000 {
		t.Errorf("PatternTables len = %d; want %d", len(v.PatternTables), 0x2000)
	}
	if len(v.NameTables) != 4 {
		t.Fatalf("NameTables count = %d; want 4", len(v.NameTables))
	}
	for i, nt := range v.NameTables {
		if len(nt) != 0x400 {
			t.Errorf("NameTables[%d] len = %d; want %d", i, len(nt), 0x400)
		}
	}
	if len(v.Palette) != 32 {
		t.Errorf("Palette len = %d; want 32", len(v.Palette))
	}
}

// DebugPPUViewer reflects live nametable + scroll state. Writing a tile
// to $2000 via PPUADDR/PPUDATA (rendering off) must show up at
// NameTables[0][0]; a $2006 address sets the decoded scroll cursor.
func TestDebugPPUViewer_ReflectsState(t *testing.T) {
	p, c, _ := newMMC3PPU(t)
	_ = c

	// Write tile $42 to nametable 0, offset 0 ($2000).
	p.Write(0x2006, 0x20)
	p.Write(0x2006, 0x00)
	p.Write(0x2007, 0x42)

	// Point v at a known address so the scroll decode is checkable.
	// $2406 → nametable 1, coarseX=6 ($06), coarseY=0.
	p.Write(0x2006, 0x24)
	p.Write(0x2006, 0x06)

	v := p.DebugPPUViewer()
	if v.NameTables[0][0] != 0x42 {
		t.Errorf("NameTables[0][0] = $%02X; want $42", v.NameTables[0][0])
	}
	if v.Scroll.NameTable != 1 {
		t.Errorf("Scroll.NameTable = %d; want 1", v.Scroll.NameTable)
	}
	if v.Scroll.CoarseX != 6 {
		t.Errorf("Scroll.CoarseX = %d; want 6", v.Scroll.CoarseX)
	}
	if v.Mirroring == "" {
		t.Error("Mirroring empty; want a mode name")
	}
}
