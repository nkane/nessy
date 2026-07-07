//go:build js && wasm

// nessy-wasm is the browser entry point for the nessy NES emulator.
// Build with:
//
//	GOOS=js GOARCH=wasm go build -o web/nessy/nessy.wasm ./cmd/nessy-wasm
//
// The page's JS shell loads nessy.wasm + Go's wasm_exec.js + Ebiten's
// canvas helpers, then RunGame takes over (Ebiten's js/wasm path
// uses requestAnimationFrame internally). A default demo ROM is
// embedded so the playground boots into something immediately;
// follow-up issues will wire a File-API picker for user ROMs.
package main

import (
	_ "embed"
	"fmt"
	"syscall/js"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/nkane/chippy/cpu"
	"github.com/nkane/nessy/internal/nes"
	"github.com/nkane/nessy/internal/nes/apu"
	"github.com/nkane/nessy/internal/nes/cart"
	"github.com/nkane/nessy/internal/nes/dma"
	"github.com/nkane/nessy/internal/nes/joypad"
	"github.com/nkane/nessy/internal/nes/ppu"
)

// Default demo ROM bundled into the wasm so a visitor sees output
// immediately. Pick one that exercises the BG + NMI path so the
// page proves the emulator is alive.
//
//go:embed default.nes
var defaultROM []byte

// cpuCyclesPerFrame mirrors cmd/nessy's constant — ~29830 cycles
// per 60 Hz frame derived from the 1.789773 MHz NTSC CPU clock.
const cpuCyclesPerFrame = 29830

var keyMap = []struct {
	key ebiten.Key
	btn joypad.Button
}{
	{ebiten.KeyArrowUp, joypad.ButtonUp},
	{ebiten.KeyArrowDown, joypad.ButtonDown},
	{ebiten.KeyArrowLeft, joypad.ButtonLeft},
	{ebiten.KeyArrowRight, joypad.ButtonRight},
	{ebiten.KeyZ, joypad.ButtonA},
	{ebiten.KeyX, joypad.ButtonB},
	{ebiten.KeyEnter, joypad.ButtonStart},
	{ebiten.KeyShiftRight, joypad.ButtonSelect},
}

type bus struct {
	cpu  *cpu.CPU
	ppu  *ppu.PPU
	joy  *joypad.Port
	apu  *apu.APU
	mmio *cpu.MMIO
	ram  *cpu.RAM
	cart cart.Cartridge
}

// cartPeripheral adapts a cart.Cartridge to cpu.Peripheral over the
// $4020-$FFFF window. Mirrors the non-wasm copy in cmd/nessy/wiring.go;
// duplicated rather than imported since the two main packages can't
// share types directly without a refactor.
type cartPeripheral struct{ cart cart.Cartridge }

func (w *cartPeripheral) Range() (uint16, uint16)   { return 0x4020, 0xFFFF }
func (w *cartPeripheral) Read(addr uint16) byte     { return w.cart.CPURead(addr) }
func (w *cartPeripheral) Write(addr uint16, v byte) { w.cart.CPUWrite(addr, v) }
func (w *cartPeripheral) Peek(addr uint16) byte     { return w.cart.CPURead(addr) }
func (w *cartPeripheral) Tick(cycles int) {
	if t, ok := w.cart.(cpu.Ticker); ok {
		t.Tick(cycles)
	}
}

func buildBus(rom *nes.ROM) (*bus, error) {
	c, err := cart.Open(rom)
	if err != nil {
		return nil, err
	}
	ram := cpu.NewRAM()
	mmio := cpu.NewMMIO(ram)
	if err := mmio.Register(&cartPeripheral{cart: c}); err != nil {
		return nil, err
	}
	jp := joypad.New()
	if err := mmio.Register(jp); err != nil {
		return nil, err
	}
	ap := apu.New()
	if err := mmio.Register(ap); err != nil {
		return nil, err
	}
	if err := mmio.Register(apu.NewStatus(ap)); err != nil {
		return nil, err
	}
	jp.AttachFrameCounter(ap)

	processor := cpu.NewVariant(mmio, cpu.VariantNES)
	ap.SetIRQSink(processor)
	ap.SetDMCBus(mmio, processor)
	if m, ok := c.(interface{ SetIRQSink(cart.IRQSink) }); ok {
		m.SetIRQSink(processor)
	}
	if m, ok := c.(interface{ SetAudioSink(cart.Sunsoft5BSink) }); ok {
		s5b := apu.NewSunsoft5B()
		ap.SetSunsoft5B(s5b)
		m.SetAudioSink(s5b)
	}
	if m, ok := c.(interface{ SetAudioSink(cart.VRC6AudioSink) }); ok {
		v6 := apu.NewVRC6Audio()
		ap.SetVRC6Audio(v6)
		m.SetAudioSink(v6)
	}
	if m, ok := c.(interface{ SetAudioSink(cart.VRC7AudioSink) }); ok {
		v7 := apu.NewVRC7Audio()
		ap.SetVRC7Audio(v7)
		m.SetAudioSink(v7)
	}

	pp := ppu.New(c, processor)
	if err := mmio.Register(pp); err != nil {
		return nil, err
	}
	if err := mmio.Register(dma.New(processor)); err != nil {
		return nil, err
	}
	processor.Reset()
	return &bus{cpu: processor, ppu: pp, joy: jp, apu: ap, mmio: mmio, ram: ram, cart: c}, nil
}

// game holds the live bus plus the JS-injected controller state. pad is
// written by the nessy.setButton API (gamepad polling + remapped keys on
// the JS side) and OR'd with the Ebiten keyboard each Update, so the
// default keys keep working with no config. Indexed by joypad.Button
// (A,B,Select,Start,Up,Down,Left,Right = 0..7). romHash keys save slots.
type game struct {
	bus     *bus
	pad     [8]bool
	romHash string
	// keyboardOn gates the built-in Ebiten keymap. JS turns it off when
	// the user saves a custom keyboard remap so the shell can fully own
	// keyboard input via setButton; on (default) the built-in
	// arrows/Z/X/Enter/RShift work with no configuration.
	keyboardOn bool
}

func (g *game) Update() error {
	for _, m := range keyMap {
		down := g.pad[m.btn]
		if g.keyboardOn {
			down = down || ebiten.IsKeyPressed(m.key)
		}
		g.bus.joy.P1.Set(m.btn, down)
	}
	target := g.bus.cpu.Cycles + cpuCyclesPerFrame
	for g.bus.cpu.Cycles < target && !g.bus.cpu.Halted {
		g.bus.cpu.Step()
	}
	// Drain APU samples to prevent the ring buffer from filling.
	// Audio output is not yet wired on the wasm path (Ebiten's
	// js/wasm audio context needs a user-gesture unlock; deferred).
	_ = g.bus.apu.Samples()
	return nil
}

func (g *game) Draw(screen *ebiten.Image) {
	screen.WritePixels(g.bus.ppu.FrameBuffer())
}

func (g *game) Layout(_, _ int) (int, int) { return ppu.ScreenWidth, ppu.ScreenHeight }

// installAPI exposes the "nessy" object on the JS side. All methods run
// on the browser's single thread, which never interleaves with Ebiten's
// requestAnimationFrame Update — so they mutate the game/bus directly
// without locking. The JS shell (web/nessy/app.js) drives:
//   - loadROM(Uint8Array)   → swap the running ROM; returns "ok" or error.
//   - setButton(idx, down)  → gamepad / remapped-key input (idx 0..7 =
//     A,B,Select,Start,Up,Down,Left,Right); OR'd with the keyboard.
//   - romHash()             → current ROM's SHA-256 hex (save-slot key).
//   - setKeyboard(on)       → toggle the built-in keymap (off = JS owns
//     keyboard for a custom remap).
//   - screenshot()          → Uint8Array of the 256×240 RGBA framebuffer.
//   - saveState()           → Uint8Array snapshot, or null on error.
//   - loadState(Uint8Array) → restore a snapshot; "ok" or error string.
func installAPI(g *game) {
	js.Global().Set("nessy", js.ValueOf(map[string]any{
		"loadROM": js.FuncOf(func(this js.Value, args []js.Value) any {
			if len(args) < 1 {
				return js.ValueOf("nessy.loadROM(romBytes): missing argument")
			}
			data := make([]byte, args[0].Length())
			js.CopyBytesToGo(data, args[0])
			rom, err := nes.ParseBytes(data)
			if err != nil {
				return js.ValueOf(fmt.Sprintf("parse: %v", err))
			}
			b, err := buildBus(rom)
			if err != nil {
				return js.ValueOf(fmt.Sprintf("build: %v", err))
			}
			g.bus = b
			g.romHash = romHashHex(data)
			g.pad = [8]bool{} // drop any held buttons across the swap
			return js.ValueOf("ok")
		}),
		"setButton": js.FuncOf(func(this js.Value, args []js.Value) any {
			if len(args) < 2 {
				return js.ValueOf(false)
			}
			idx := args[0].Int()
			if idx < 0 || idx >= len(g.pad) {
				return js.ValueOf(false)
			}
			g.pad[idx] = args[1].Truthy()
			return js.ValueOf(true)
		}),
		"romHash": js.FuncOf(func(this js.Value, args []js.Value) any {
			return js.ValueOf(g.romHash)
		}),
		"setKeyboard": js.FuncOf(func(this js.Value, args []js.Value) any {
			if len(args) < 1 {
				return js.ValueOf(g.keyboardOn)
			}
			g.keyboardOn = args[0].Truthy()
			return js.ValueOf(g.keyboardOn)
		}),
		"screenshot": js.FuncOf(func(this js.Value, args []js.Value) any {
			// Return the raw 256×240 RGBA framebuffer. JS paints it to an
			// offscreen canvas → PNG. Pulling pixels from the core avoids
			// the empty-readback of Ebiten's WebGL canvas (no
			// preserveDrawingBuffer).
			fb := g.bus.ppu.FrameBuffer()
			out := js.Global().Get("Uint8Array").New(len(fb))
			js.CopyBytesToJS(out, fb)
			return out
		}),
		"saveState": js.FuncOf(func(this js.Value, args []js.Value) any {
			data, err := captureState(g.bus, g.romHash)
			if err != nil {
				return js.Null()
			}
			out := js.Global().Get("Uint8Array").New(len(data))
			js.CopyBytesToJS(out, data)
			return out
		}),
		"loadState": js.FuncOf(func(this js.Value, args []js.Value) any {
			if len(args) < 1 {
				return js.ValueOf("nessy.loadState(bytes): missing argument")
			}
			data := make([]byte, args[0].Length())
			js.CopyBytesToGo(data, args[0])
			if err := applyState(g.bus, data, g.romHash); err != nil {
				return js.ValueOf(fmt.Sprintf("load: %v", err))
			}
			return js.ValueOf("ok")
		}),
	}))
}

func main() {
	rom, err := nes.ParseBytes(defaultROM)
	if err != nil {
		panic(fmt.Errorf("default ROM: %w", err))
	}
	b, err := buildBus(rom)
	if err != nil {
		panic(fmt.Errorf("build bus: %w", err))
	}
	g := &game{bus: b, romHash: romHashHex(defaultROM), keyboardOn: true}
	installAPI(g)
	ebiten.SetWindowSize(ppu.ScreenWidth*3, ppu.ScreenHeight*3)
	ebiten.SetWindowTitle("nessy")
	ebiten.SetTPS(60)
	if err := ebiten.RunGame(g); err != nil {
		panic(err)
	}
}
