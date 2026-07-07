//go:build js && wasm

package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"fmt"

	"github.com/nkane/chippy/cpu"

	"github.com/nkane/nessy/internal/nes/apu"
	"github.com/nkane/nessy/internal/nes/cart"
	"github.com/nkane/nessy/internal/nes/joypad"
	"github.com/nkane/nessy/internal/nes/ppu"
)

// The wasm save-state mirrors the desktop format (cmd/nessy/savestate.go):
// each subsystem's FullState, gob-encoded inside gzip. The browser holds
// the bytes in IndexedDB (keyed by ROMHash + slot) instead of on disk, so
// F1-F4 slots survive a page reload (#10). The magic/version guard rejects
// a slot saved by an incompatible build, and ROMHash guards against
// restoring a slot into the wrong game.
const (
	wasmStateMagic   = "nessy-wasm-state"
	wasmStateVersion = 1
)

type wasmSaveState struct {
	Magic   string
	Version int
	ROMHash string
	CPU     cpu.FullState
	RAM     []byte
	PPU     ppu.FullState
	APU     apu.FullState
	Joypad  joypad.FullState
	Cart    cart.CartState
}

// romHashHex is the SHA-256 of the ROM bytes, hex-encoded — the per-game
// key the browser uses to scope save slots.
func romHashHex(rom []byte) string {
	h := sha256.Sum256(rom)
	return hex.EncodeToString(h[:])
}

// captureState snapshots the live bus into gzip(gob) bytes. Safe to call
// from a JS callback: the browser's single thread means no Update tick is
// mid-flight (JS calls don't interleave with requestAnimationFrame).
func captureState(b *bus, romHash string) ([]byte, error) {
	cs, err := cart.SaveCart(b.cart)
	if err != nil {
		return nil, fmt.Errorf("cart: %w", err)
	}
	s := wasmSaveState{
		Magic:   wasmStateMagic,
		Version: wasmStateVersion,
		ROMHash: romHash,
		CPU:     b.cpu.SaveFullState(),
		RAM:     b.ram.SaveFullState(),
		PPU:     b.ppu.SaveFullState(),
		APU:     b.apu.SaveFullState(),
		Joypad:  b.joy.SaveFullState(),
		Cart:    cs,
	}
	var raw bytes.Buffer
	if err := gob.NewEncoder(&raw).Encode(s); err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}
	var out bytes.Buffer
	w := gzip.NewWriter(&out)
	if _, err := w.Write(raw.Bytes()); err != nil {
		return nil, fmt.Errorf("gzip write: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("gzip close: %w", err)
	}
	return out.Bytes(), nil
}

// applyState restores a snapshot into the live bus. It rejects a state
// from a different build (magic/version) or a different ROM (ROMHash), so
// a slot from another game can't corrupt the current session.
func applyState(b *bus, data []byte, romHash string) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("gzip open: %w", err)
	}
	defer func() { _ = gz.Close() }()
	var s wasmSaveState
	if err := gob.NewDecoder(gz).Decode(&s); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	if s.Magic != wasmStateMagic {
		return fmt.Errorf("bad magic %q", s.Magic)
	}
	if s.Version != wasmStateVersion {
		return fmt.Errorf("version %d unsupported (build expects %d)", s.Version, wasmStateVersion)
	}
	if s.ROMHash != romHash {
		return fmt.Errorf("save is for a different ROM")
	}
	if err := b.ram.LoadFullState(s.RAM); err != nil {
		return fmt.Errorf("ram: %w", err)
	}
	if err := b.ppu.LoadFullState(s.PPU); err != nil {
		return fmt.Errorf("ppu: %w", err)
	}
	if err := b.apu.LoadFullState(s.APU); err != nil {
		return fmt.Errorf("apu: %w", err)
	}
	b.joy.LoadFullState(s.Joypad)
	if err := cart.LoadCart(b.cart, s.Cart); err != nil {
		return fmt.Errorf("cart: %w", err)
	}
	// CPU last so cart-restore IRQ-sink side effects don't clobber it.
	b.cpu.LoadFullState(s.CPU)
	return nil
}
