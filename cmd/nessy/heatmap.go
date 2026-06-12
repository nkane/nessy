//go:build nessy

package main

import "github.com/nkane/chippy/cpu"

// accessHeatmap records the CPU cycle of the last read / write / exec of
// every CPU-bus byte, so the debugger's memory viewer can shade a
// Mesen-style access heatmap that decays with age (#41). Backed by
// chippy v1.5.0's cpu.SetAccessHook (chippy#435). chippy itself records
// nothing — this owns the recency state.
//
// The stamp arrays allocate (3 × 64 KiB × 8 B = 1.5 MiB) only when a
// heatmap is started, so an idle debug session pays nothing. record runs
// on the CPU hot path but only when enabled; both it and the start/stop/
// query paths run under the DAP server's CPU lock, so no extra guard is
// needed. A stamp of 0 means "never accessed".
type accessHeatmap struct {
	enabled           bool
	read, write, exec []uint64
}

func newAccessHeatmap() *accessHeatmap { return &accessHeatmap{} }

// start allocates the stamp arrays (once) and begins recording.
func (h *accessHeatmap) start() {
	if h.read == nil {
		h.read = make([]uint64, 0x10000)
		h.write = make([]uint64, 0x10000)
		h.exec = make([]uint64, 0x10000)
	}
	h.enabled = true
}

// stop halts recording. The stamps are kept so a final window can still
// be queried after stopping.
func (h *accessHeatmap) stop() { h.enabled = false }

// record stamps addr's last access of the given kind. Cheap + only when
// enabled — installed as the CPU access hook while a heatmap runs.
func (h *accessHeatmap) record(addr uint16, kind cpu.AccessKind, cycle uint64) {
	if !h.enabled {
		return
	}
	switch kind {
	case cpu.AccessRead:
		h.read[addr] = cycle
	case cpu.AccessWrite:
		h.write[addr] = cycle
	case cpu.AccessExec:
		h.exec[addr] = cycle
	}
}

// heatmapWindow is the per-byte recency for a CPU-address range. The
// panel computes decay from CurrentCycle - stamp.
type heatmapWindow struct {
	Start        uint16   `json:"start"`
	CurrentCycle uint64   `json:"currentCycle"`
	Read         []uint64 `json:"read"`
	Write        []uint64 `json:"write"`
	Exec         []uint64 `json:"exec"`
}

// window returns the recency stamps for [start, start+length), clamped
// to the 64 KiB address space. Empty when recording was never started.
func (h *accessHeatmap) window(start, length int, currentCycle uint64) heatmapWindow {
	w := heatmapWindow{Start: uint16(start), CurrentCycle: currentCycle}
	if h.read == nil {
		return w
	}
	if start < 0 {
		start = 0
	}
	end := start + length
	if end > 0x10000 {
		end = 0x10000
	}
	if start > end {
		start = end
	}
	w.Start = uint16(start)
	w.Read = append([]uint64(nil), h.read[start:end]...)
	w.Write = append([]uint64(nil), h.write[start:end]...)
	w.Exec = append([]uint64(nil), h.exec[start:end]...)
	return w
}
