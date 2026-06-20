package cart

import (
	"testing"

	"github.com/nkane/nessy/internal/nes"
)

func newMMC3ForRev(t *testing.T, sub uint8) *MMC3 {
	t.Helper()
	rom := &nes.ROM{
		Mapper:    4,
		SubMapper: sub,
		PRG:       make([]byte, 32*1024),
		CHR:       make([]byte, 8*1024),
		Mirroring: nes.MirrorHorizontal,
	}
	c, err := NewMMC3(rom)
	if err != nil {
		t.Fatalf("NewMMC3: %v", err)
	}
	return c
}

// pulseA12 emulates one A12 rising edge: addr toggles low → high, with
// the PPU dot count advanced past the MMC3 A12 low-time filter (>10
// dots) between the two so the rise counts — real fetches are always
// many dots apart. These tests exercise the IRQ-counter logic, not the
// filter, so the spacing is just enough to clear it.
func pulseA12(c *MMC3) {
	c.SetA12Cycle(c.a12Cycle + 1)
	c.PPURead(0x0000) // A12 = 0 (low)
	c.SetA12Cycle(c.a12Cycle + 16)
	c.PPURead(0x1000) // A12 = 1 (rising, low stretch > filter)
}

// RevB (default sub-mapper 0): explicit $C001 reload with latch=0 +
// IRQ enabled fires on the next A12 edge.
func TestMMC3_RevB_ReloadWithZeroLatchFires(t *testing.T) {
	c := newMMC3ForRev(t, 0)
	sink := &fakeIRQSink{}
	c.SetIRQSink(sink)
	c.CPUWrite(0xC000, 0) // latch = 0
	c.CPUWrite(0xC001, 0) // reload flag
	c.CPUWrite(0xE001, 0) // IRQ enable on
	pulseA12(c)
	if sink.asserts != 1 {
		t.Errorf("RevB explicit-reload-zero asserts = %d; want 1", sink.asserts)
	}
}

// RevA (sub-mapper 3): an EXPLICIT $C001 reload with latch=0 fires on
// the next A12 edge, same as RevB. The rev-A/rev-B difference is NOT the
// explicit-reload path (both fire when the reload flag is set) — it is
// the STUCK-AT-ZERO clock, exercised below. Matches Mesen's RevA
// condition `(count > 0 || reloadFlag) && newCount == 0`.
func TestMMC3_RevA_ExplicitReloadZeroFires(t *testing.T) {
	c := newMMC3ForRev(t, 3)
	sink := &fakeIRQSink{}
	c.SetIRQSink(sink)
	c.CPUWrite(0xC000, 0) // latch = 0
	c.CPUWrite(0xC001, 0) // reload flag
	c.CPUWrite(0xE001, 0) // IRQ enable on
	pulseA12(c)
	if sink.asserts != 1 {
		t.Errorf("RevA explicit-reload-zero asserts = %d; want 1", sink.asserts)
	}
}

// The defining rev-A behaviour: once the counter sits at 0, a further
// A12 edge with NO reload flag reloads 0->0 and stays SILENT on rev-A
// (rev-B would fire on every such edge). This is what Blargg mmc3_test 6
// (MMC6) pins; the byte-identical rev-B test 5 ROM expects the opposite,
// so the revision is resolved by content hash (mmc3RevAHashes).
func TestMMC3_RevA_StuckAtZeroSilent(t *testing.T) {
	c := newMMC3ForRev(t, 3)
	sink := &fakeIRQSink{}
	c.SetIRQSink(sink)
	c.CPUWrite(0xC000, 1) // latch = 1
	c.CPUWrite(0xC001, 0) // reload
	c.CPUWrite(0xE001, 0) // enable
	pulseA12(c)           // reload -> counter = 1 (silent)
	pulseA12(c)           // 1 -> 0, fires
	if sink.asserts != 1 {
		t.Fatalf("RevA setup: natural countdown asserts = %d; want 1", sink.asserts)
	}
	// Now latch = 0 with NO reload flag: each further edge reloads 0->0.
	// Rev-A keeps SILENT (rev-B would fire every edge).
	c.CPUWrite(0xC000, 0)
	pulseA12(c)
	pulseA12(c)
	if sink.asserts != 1 {
		t.Errorf("RevA stuck-at-zero fired: asserts = %d; want 1", sink.asserts)
	}
}

// RevB contrast: the same stuck-at-zero clock (latch=0) fires every edge.
func TestMMC3_RevB_StuckAtZeroFires(t *testing.T) {
	c := newMMC3ForRev(t, 0)
	sink := &fakeIRQSink{}
	c.SetIRQSink(sink)
	c.CPUWrite(0xC000, 1)
	c.CPUWrite(0xC001, 0)
	c.CPUWrite(0xE001, 0)
	pulseA12(c) // reload -> 1
	pulseA12(c) // 1 -> 0, fires
	c.CPUWrite(0xC000, 0)
	pulseA12(c) // 0 -> 0 reload, RevB fires
	pulseA12(c) // fires again
	if sink.asserts < 3 {
		t.Errorf("RevB stuck-at-zero asserts = %d; want >= 3 (fires every edge)", sink.asserts)
	}
}

// Natural countdown path still fires on RevA — only the explicit-
// reload IRQ is suppressed.
func TestMMC3_RevA_NaturalCountdownStillFires(t *testing.T) {
	c := newMMC3ForRev(t, 3)
	sink := &fakeIRQSink{}
	c.SetIRQSink(sink)
	c.CPUWrite(0xC000, 1) // latch = 1
	c.CPUWrite(0xC001, 0) // reload
	c.CPUWrite(0xE001, 0) // enable
	// First A12 rising: reload path (silent for RevA), counter = 1.
	pulseA12(c)
	if sink.asserts != 0 {
		t.Errorf("RevA reload phase asserted")
	}
	// Second A12: counter 1 → 0, fires.
	pulseA12(c)
	if sink.asserts != 1 {
		t.Errorf("RevA natural countdown asserts = %d; want 1", sink.asserts)
	}
}
