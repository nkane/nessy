//go:build nessy

package main

import "testing"

// Heatmap recording stamps the CPU cycle of each access; after running a
// few instructions the executed PRG bytes carry a non-zero exec stamp,
// and stopping detaches the hook.
func TestAccessHeatmap(t *testing.T) {
	bus := newTestBus(t)
	h := newTestHandler(bus)

	if _, handled, err := h(heatmapStartCommand, nil); err != nil || !handled {
		t.Fatalf("heatmapStart: handled=%v err=%v", handled, err)
	}
	for range 16 {
		bus.cpu.Step()
	}

	// Window over the reset region ($8000) — the CPU fetched opcodes there.
	body, handled, err := h(heatmapCommand, []byte(`{"start":32768,"length":256}`))
	if err != nil || !handled {
		t.Fatalf("heatmap: handled=%v err=%v", handled, err)
	}
	w := body.(heatmapWindow)
	if w.CurrentCycle == 0 {
		t.Error("CurrentCycle = 0; CPU should have advanced")
	}
	if len(w.Exec) != 256 || len(w.Read) != 256 || len(w.Write) != 256 {
		t.Fatalf("window lens = exec %d read %d write %d; want 256 each", len(w.Exec), len(w.Read), len(w.Write))
	}
	var sawExec bool
	for _, s := range w.Exec {
		if s != 0 {
			sawExec = true
			break
		}
	}
	if !sawExec {
		t.Error("no exec stamps in the reset region; access hook didn't record")
	}

	if _, handled, err := h(heatmapStopCommand, nil); err != nil || !handled {
		t.Fatalf("heatmapStop: handled=%v err=%v", handled, err)
	}
}

// Freeze locks a CPU-bus RAM address: subsequent writes are suppressed
// and the value holds; unfreeze restores normal writes.
func TestFreeze(t *testing.T) {
	bus := newTestBus(t)
	h := newTestHandler(bus)

	if _, handled, err := h(freezeCommand, []byte(`{"addr":2,"value":66}`)); err != nil || !handled {
		t.Fatalf("freeze: handled=%v err=%v", handled, err)
	}
	// A CPU-bus write to $0002 (unclaimed work RAM) must not land.
	bus.mmio.Write(0x0002, 0x99)
	if got := bus.mmio.Read(0x0002); got != 66 {
		t.Errorf("frozen $0002 = %d; want 66 (write suppressed)", got)
	}

	// frozen lists the address.
	body, _, _ := h(frozenCommand, nil)
	addrs := body.(map[string]any)["addrs"].([]uint16)
	found := false
	for _, a := range addrs {
		if a == 2 {
			found = true
		}
	}
	if !found {
		t.Errorf("frozen addrs %v missing $0002", addrs)
	}

	// Unfreeze → writes resume.
	if _, handled, err := h(unfreezeCommand, []byte(`{"addr":2}`)); err != nil || !handled {
		t.Fatalf("unfreeze: handled=%v err=%v", handled, err)
	}
	bus.mmio.Write(0x0002, 0x55)
	if got := bus.mmio.Read(0x0002); got != 0x55 {
		t.Errorf("after unfreeze $0002 = %d; want 0x55", got)
	}
}
