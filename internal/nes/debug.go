package nes

// DebugEventSink receives significant emulation events for the
// debugger's event viewer (#31 / #44). The PPU implements it (recording
// at its current scanline/dot); components that fire events the PPU
// can't observe directly — the mapper IRQ, the DMC + OAM DMA — hold an
// optional sink wired at build time and call RecordDebugEvent when the
// event occurs. Recording is gated inside the sink, so an unset or
// idle sink costs nothing on the hot path.
type DebugEventSink interface {
	RecordDebugEvent(kind string)
}

// Event kinds for the sources outside the PPU. The PPU-internal kinds
// (register reads/writes, NMI, sprite-0) live in the ppu package.
const (
	EventMapperIRQ = "mapperIRQ" // mapper scanline IRQ asserted (e.g. MMC3 A12)
	EventDMCDMA    = "dmcDMA"    // DMC sample-fetch DMA (CPU stall)
	EventOAMDMA    = "oamDMA"    // $4014 OAM DMA (CPU stall)
)
