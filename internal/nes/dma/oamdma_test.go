package dma

import (
	"testing"

	"github.com/nkane/nessy/internal/nes"
)

// fakeDebugSink records event-viewer kinds.
type fakeDebugSink struct{ kinds []string }

func (s *fakeDebugSink) RecordDebugEvent(kind string) { s.kinds = append(s.kinds, kind) }

// A $4014 write records an OAM-DMA event for the event viewer (#44).
func TestOAMDMA_RecordsDebugEvent(t *testing.T) {
	d := New(&fakeSink{})
	dbg := &fakeDebugSink{}
	d.SetDebugSink(dbg)
	d.Write(0x4014, 0x02)
	if len(dbg.kinds) != 1 || dbg.kinds[0] != nes.EventOAMDMA {
		t.Errorf("debug events = %v; want one %q", dbg.kinds, nes.EventOAMDMA)
	}
}

// fakeSink records SetNeedSpriteDma calls. OAMDMA hands the page
// across; CPU's ProcessPendingDma drains the actual transfer (#376).
type fakeSink struct {
	calls []byte
}

func (s *fakeSink) SetNeedSpriteDma(page byte) {
	s.calls = append(s.calls, page)
}

// A $4014 write hands the source page to the CPU's DMA sink. The
// 256-byte copy + 513-cycle stall are now driven from
// cpu.ProcessPendingDma, not from this peripheral.
func TestOAMDMA_WriteSignalsSpriteDma(t *testing.T) {
	sink := &fakeSink{}
	d := New(sink)

	d.Write(0x4014, 0x02)

	if len(sink.calls) != 1 {
		t.Fatalf("SetNeedSpriteDma calls = %d; want 1", len(sink.calls))
	}
	if sink.calls[0] != 0x02 {
		t.Fatalf("SetNeedSpriteDma page = $%02X; want $02", sink.calls[0])
	}
	if d.LastPage() != 0x02 {
		t.Fatalf("LastPage = $%02X; want $02", d.LastPage())
	}
}

// Reads of $4014 return zero (open-bus stub). No state mutation.
func TestOAMDMA_ReadReturnsZero(t *testing.T) {
	d := New(&fakeSink{})
	if v := d.Read(0x4014); v != 0 {
		t.Fatalf("Read = $%02X; want $00", v)
	}
}

// Range claims exactly $4014. The single-byte window matters for
// MMIO registration alongside the cart ($4020-$FFFF) and joypad
// ($4016-$4017).
func TestOAMDMA_Range(t *testing.T) {
	d := New(&fakeSink{})
	lo, hi := d.Range()
	if lo != 0x4014 || hi != 0x4014 {
		t.Fatalf("Range = $%04X-$%04X; want $4014-$4014", lo, hi)
	}
}

// Multiple writes overwrite the latched page; the CPU sink sees one
// signal per write so a back-to-back $4014 store re-triggers DMA.
func TestOAMDMA_WriteLatchesLatestPage(t *testing.T) {
	sink := &fakeSink{}
	d := New(sink)

	d.Write(0x4014, 0x02)
	d.Write(0x4014, 0x07)

	if d.LastPage() != 0x07 {
		t.Fatalf("LastPage = $%02X; want $07", d.LastPage())
	}
	if len(sink.calls) != 2 {
		t.Fatalf("SetNeedSpriteDma calls = %d; want 2", len(sink.calls))
	}
	if sink.calls[1] != 0x07 {
		t.Fatalf("second SetNeedSpriteDma page = $%02X; want $07", sink.calls[1])
	}
}
