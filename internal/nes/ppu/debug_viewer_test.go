package ppu_test

import "testing"

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
