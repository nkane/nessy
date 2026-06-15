package apu

import "testing"

// An enabled MMC5 pulse toggles between its volume and 0 across the duty
// cycle; a disabled channel is silent.
func TestMMC5Audio_Pulse(t *testing.T) {
	m := NewMMC5Audio()
	m.Write(0x5015, 0x01)      // enable pulse 1
	m.Write(0x5000, 0x80|0x0A) // duty 2 (50%), constant volume 10
	m.Write(0x5002, 0x00)      // period low
	m.Write(0x5003, 0x01)      // period high → period = 0x100 (audible)

	sawVol, sawZero := false, false
	for range 20000 {
		m.Step()
		switch m.Output() {
		case 10:
			sawVol = true
		case 0:
			sawZero = true
		}
	}
	if !sawVol {
		t.Error("MMC5 pulse never output its volume (10)")
	}
	if !sawZero {
		t.Error("MMC5 pulse never output 0 across the duty cycle")
	}

	// Disable → silent.
	m.Write(0x5015, 0x00)
	silent := true
	for range 5000 {
		m.Step()
		if m.Output() != 0 {
			silent = false
		}
	}
	if !silent {
		t.Error("disabled MMC5 pulses still produced output")
	}
}

// The PCM channel ($5011) contributes its raw level (write mode).
func TestMMC5Audio_PCM(t *testing.T) {
	m := NewMMC5Audio()
	// Pulses disabled — output comes solely from PCM.
	m.Write(0x5010, 0x00) // write mode
	m.Write(0x5011, 0x80)
	if got := m.Output(); got != 0x80>>3 {
		t.Errorf("PCM output = %d; want %d", got, 0x80>>3)
	}
	// Read mode ignores $5011 writes (PCM-from-PRG is unmodeled → silent).
	m.Write(0x5010, 0x01)
	m.Write(0x5011, 0x40)
	if got := m.Output(); got != 0x80>>3 {
		t.Errorf("read-mode $5011 changed PCM to %d; want held %d", got, 0x80>>3)
	}
}
