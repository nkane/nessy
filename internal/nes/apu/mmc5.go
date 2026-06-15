package apu

// MMC5Audio is the expansion sound on Nintendo's MMC5 cart (mapper 5,
// #56): two pulse channels ($5000-$5007) plus a raw PCM channel
// ($5010/$5011), mixed outside the 2A03 DAC like the VRC6 / Sunsoft 5B
// chips. The pulses are 2A03 squares minus the sweep unit; this models
// duty + constant volume (the common case) — envelope / length-counter
// clocking + PCM read-mode are a later refinement, matching the VRC6
// chip's altitude.
type MMC5Audio struct {
	pulse1  mmc5Pulse
	pulse2  mmc5Pulse
	pcm     byte // $5011 raw 8-bit output (write mode)
	pcmRead bool // $5010 bit 0: 1 = read mode (PCM from PRG; unmodeled → silent)
}

type mmc5Pulse struct {
	enabled  bool
	duty     byte // 0..3
	dutyStep byte // 0..7
	volume   byte // constant volume (0..15)
	period   uint16
	timer    uint16
}

// mmc5DutyTable is the standard 2A03 8-step duty sequence.
var mmc5DutyTable = [4][8]byte{
	{0, 1, 0, 0, 0, 0, 0, 0}, // 12.5%
	{0, 1, 1, 0, 0, 0, 0, 0}, // 25%
	{0, 1, 1, 1, 1, 0, 0, 0}, // 50%
	{1, 0, 0, 1, 1, 1, 1, 1}, // 25% negated
}

// NewMMC5Audio constructs a silent MMC5 audio chip.
func NewMMC5Audio() *MMC5Audio { return &MMC5Audio{} }

// Write implements cart.MMC5AudioSink for $5000-$5015.
func (m *MMC5Audio) Write(addr uint16, val byte) {
	switch addr {
	case 0x5000:
		m.pulse1.duty = (val >> 6) & 0x03
		m.pulse1.volume = val & 0x0F
	case 0x5002:
		m.pulse1.period = (m.pulse1.period & 0x0700) | uint16(val)
	case 0x5003:
		m.pulse1.period = (m.pulse1.period & 0x00FF) | uint16(val&0x07)<<8
		m.pulse1.dutyStep = 0
	case 0x5004:
		m.pulse2.duty = (val >> 6) & 0x03
		m.pulse2.volume = val & 0x0F
	case 0x5006:
		m.pulse2.period = (m.pulse2.period & 0x0700) | uint16(val)
	case 0x5007:
		m.pulse2.period = (m.pulse2.period & 0x00FF) | uint16(val&0x07)<<8
		m.pulse2.dutyStep = 0
	case 0x5010:
		m.pcmRead = val&0x01 != 0
	case 0x5011:
		if !m.pcmRead {
			m.pcm = val
		}
	case 0x5015:
		m.pulse1.enabled = val&0x01 != 0
		m.pulse2.enabled = val&0x02 != 0
	}
	// $5001 / $5005 (sweep) are unused on MMC5 pulses — ignored.
}

// Step advances both pulse timers by one CPU cycle.
func (m *MMC5Audio) Step() {
	m.pulse1.step()
	m.pulse2.step()
}

func (p *mmc5Pulse) step() {
	if !p.enabled {
		return
	}
	if p.timer == 0 {
		p.timer = p.period
		p.dutyStep = (p.dutyStep + 1) & 0x07
	} else {
		p.timer--
	}
}

func (p *mmc5Pulse) output() byte {
	// Periods below 8 are inaudible on real silicon (the timer is too
	// fast); the 2A03 squares mute them, and MMC5's do the same.
	if !p.enabled || p.period < 8 {
		return 0
	}
	if mmc5DutyTable[p.duty][p.dutyStep] == 1 {
		return p.volume
	}
	return 0
}

// Output sums the two pulses (0..15 each) plus the PCM level folded in
// at a comparable scale. The caller scales the total into the mix.
func (m *MMC5Audio) Output() int {
	return int(m.pulse1.output()) + int(m.pulse2.output()) + int(m.pcm>>3)
}
