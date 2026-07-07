//go:build nessy

package main

import (
	"github.com/nkane/chippy/cpu"
	"github.com/nkane/chippy/expr"
)

// nessyHostVars exposes NES PPU timing to chippy's expression evaluator
// (chippy#433) so conditional breakpoints + watch / evaluate expressions
// can reference NES state the 6502 core can't see — e.g. `scanline == 30`,
// `dot > 256`, `frame == 120` (#33). The getters read the live PPU at
// evaluation time. Registered once per session via dap.Server.SetHostVars.
func nessyHostVars(bus *nesBus) expr.HostVarResolver {
	return func(name string) (func() uint32, bool) {
		switch name {
		case "scanline":
			return func() uint32 { return uint32(bus.ppu.Scanline()) }, true
		case "dot":
			return func() uint32 { return uint32(bus.ppu.Dot()) }, true
		case "frame":
			return func() uint32 { return uint32(bus.ppu.FrameCount()) }, true
		default:
			return nil, false
		}
	}
}

// stepScanlinePredicate stops the run loop when the PPU scanline advances
// from the value captured at arm time (step-scanline, #33).
func stepScanlinePredicate(bus *nesBus) func() bool {
	start := bus.ppu.Scanline()
	return func() bool { return bus.ppu.Scanline() != start }
}

// stepFramePredicate stops when the PPU crosses into the next frame
// (step-frame, #33).
func stepFramePredicate(bus *nesBus) func() bool {
	start := bus.ppu.FrameCount()
	return func() bool { return bus.ppu.FrameCount() != start }
}

// runToNMIPredicate stops on the next rising edge of the PPU /NMI line
// (run-to-NMI, #33). Captures the level at arm time so an arm made while
// already in vblank waits for the next frame's NMI rather than firing
// immediately.
func runToNMIPredicate(bus *nesBus) func() bool {
	prev := bus.ppu.NMILine()
	return func() bool {
		cur := bus.ppu.NMILine()
		rising := cur && !prev
		prev = cur
		return rising
	}
}

// runToIRQPredicate stops when the CPU is about to service a hardware IRQ
// — a mapper (MMC3/MMC5), APU frame-counter, or DMC IRQ (run-to-IRQ, #53).
// The signal is (IRQ line asserted AND the I flag clear): the level the
// 6502 samples to take an IRQ. That cleanly excludes BRK (a software
// opcode, no IRQ line) and NMI (its own line + predicate). It's an edge —
// prev captured at arm time — so arming while an IRQ is already pending
// waits for the next assertion rather than firing on the same one, and a
// resume after the handler sets FlagI runs on to the following IRQ.
func runToIRQPredicate(bus *nesBus) func() bool {
	pending := func() bool {
		return bus.cpu.IRQAsserted() && bus.cpu.P&cpu.FlagI == 0
	}
	prev := pending()
	return func() bool {
		cur := pending()
		rising := cur && !prev
		prev = cur
		return rising
	}
}
