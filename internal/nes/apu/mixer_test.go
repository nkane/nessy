package apu

import (
	"math"
	"testing"
)

// All channels silent → mixer outputs exactly 0. Catches a
// regression where the pulse / tnd formulas leak a non-zero base.
func TestMixer_SilentInputsZero(t *testing.T) {
	if got := mixSample(0, 0, 0, 0, 0); got != 0 {
		t.Errorf("mixSample(0,0,0,0,0) = %v; want 0", got)
	}
}

// Pulse table value at i=0 is 0; values monotonically increase.
// Peak (p1+p2 = 30) sits around 0.258 per nesdev's published
// formula. Allow ±5% slack for float32 rounding.
func TestMixer_PulseTableShape(t *testing.T) {
	if pulseTable[0] != 0 {
		t.Errorf("pulseTable[0] = %v; want 0", pulseTable[0])
	}
	for i := 1; i < len(pulseTable); i++ {
		if pulseTable[i] <= pulseTable[i-1] {
			t.Errorf("pulseTable[%d]=%v not > pulseTable[%d]=%v",
				i, pulseTable[i], i-1, pulseTable[i-1])
		}
	}
	peak := pulseTable[30]
	if math.Abs(float64(peak-0.258)) > 0.02 {
		t.Errorf("pulseTable[30] = %v; want ~0.258", peak)
	}
}

// tnd term contribution: a triangle-only mix has different
// magnitude than a pulse-only mix at "equivalent" volume (the
// non-linearity is the whole point). Compare pulse=15 vs triangle=15
// — both at half-volume of their respective max — and assert they
// produce different output levels.
func TestMixer_TriangleVsPulseDistinctLevels(t *testing.T) {
	pulseOnly := mixSample(15, 0, 0, 0, 0)
	triOnly := mixSample(0, 0, 15, 0, 0)
	if pulseOnly == triOnly {
		t.Errorf("pulse-15 + tri-15 mix to identical levels; non-linear DAC should differ")
	}
	// Both must be positive.
	if pulseOnly <= 0 || triOnly <= 0 {
		t.Errorf("got non-positive levels: pulse=%v tri=%v", pulseOnly, triOnly)
	}
}

// Combined channels produce strictly greater output than any single
// channel alone — additive within each group, with the non-linear
// pulse + tnd sum staying monotone.
func TestMixer_CombinedExceedsSingle(t *testing.T) {
	pulseOnly := mixSample(15, 15, 0, 0, 0)
	triOnly := mixSample(0, 0, 15, 0, 0)
	all := mixSample(15, 15, 15, 8, 64)
	if all <= pulseOnly {
		t.Errorf("all-channels (%v) should exceed pulse-only (%v)", all, pulseOnly)
	}
	if all <= triOnly {
		t.Errorf("all-channels (%v) should exceed triangle-only (%v)", all, triOnly)
	}
}

// Output stays within int16 headroom after scaling. With the
// chosen scale factor (30000) and all channels at peak, the
// mixer output must not clip int16.
func TestMixer_NoInt16Clipping(t *testing.T) {
	mix := mixSample(15, 15, 15, 15, 127) // every channel max
	scaled := mix * 30000
	if scaled > 32767 || scaled < -32768 {
		t.Errorf("max-mix scaled = %v; would clip int16", scaled)
	}
}

// The next three tests encode the exact non-linear-DAC properties that
// Blargg's apu_mixer suite (square/triangle/noise/dmc) verifies AUDIBLY.
// Those ROMs cancel the channel-under-test against the DMC DAC to near
// silence and have no programmatic $6000 verdict — they print play/listen
// instructions and report "done", so they can't gate CI. The behaviour
// they exercise is deterministic at the mixSample level, so we pin it
// here instead (#5). Reference: https://www.nesdev.org/wiki/APU_Mixer.

// Square non-linearity: the pulse term is a function of (p1+p2) through a
// saturating curve, so a second square at the same volume adds LESS than
// the first — "how one square affects the other (slightly)" in the
// apu_mixer square test. mix(15,15) must be strictly below 2*mix(15,0).
func TestMixer_SquareCrossAttenuation(t *testing.T) {
	both := mixSample(15, 15, 0, 0, 0)
	twiceOne := 2 * mixSample(15, 0, 0, 0, 0)
	if !(both < twiceOne) {
		t.Errorf("mix(15,15)=%v not < 2*mix(15,0)=%v; pulse DAC should saturate", both, twiceOne)
	}
}

// tnd non-linearity: triangle, noise, and DMC share one saturating tnd
// term, so playing the DMC alongside the triangle ATTENUATES the
// triangle's contribution — the apu_mixer triangle/noise/dmc tests verify
// "the DMC DAC affects attenuation of them properly". The combined level
// must be strictly below the sum of the two played separately.
func TestMixer_TndCrossAttenuation(t *testing.T) {
	tri := mixSample(0, 0, 15, 0, 0)
	dmc := mixSample(0, 0, 0, 0, 64)
	both := mixSample(0, 0, 15, 0, 64)
	if !(both < tri+dmc) {
		t.Errorf("tnd combined %v not < separate sum %v; tnd group should saturate", both, tri+dmc)
	}
}

// Group independence: the pulse and tnd terms are summed, not cross-mixed,
// so a square's marginal contribution is the SAME regardless of DMC level
// — the apu_mixer square test checks "the square DAC non-linearity is
// separate from the DMC". Equal to within float32 epsilon (the add/sub of
// the shared tnd term loses a bit or two of precision).
func TestMixer_PulseTndGroupsIndependent(t *testing.T) {
	d0 := mixSample(15, 0, 0, 0, 0) - mixSample(0, 0, 0, 0, 0)
	dHi := mixSample(15, 0, 0, 0, 100) - mixSample(0, 0, 0, 0, 100)
	if diff := math.Abs(float64(d0 - dHi)); diff > 1e-5 {
		t.Errorf("pulse delta varies with DMC (%v vs %v, |Δ|=%v); groups should be independent", d0, dHi, diff)
	}
}
