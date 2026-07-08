# nessy WASM playground

The browser build of nessy — the Go core compiled to `js/wasm`, driven by
Ebiten, with a plain-JS shell (`app.js`) around it: ROM file picker +
drag/drop, gamepad + remappable keyboard, IndexedDB save slots (F1–F4),
screenshot, fullscreen, and an offline PWA service worker (#10).

## Build + run locally

```sh
# 1. Compile the core to wasm.
GOOS=js GOARCH=wasm go build -o web/nessy/nessy.wasm ./cmd/nessy-wasm

# 2. Copy Go's wasm loader shim next to it.
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" web/nessy/   # older Go: misc/wasm/wasm_exec.js

# 3. Serve over HTTP (service workers + wasm need a real origin, not file://).
cd web/nessy && python3 -m http.server 8080
# → http://localhost:8080
```

`nessy.wasm` and `wasm_exec.js` are build artifacts — not committed. The
committed shell is `index.html`, `app.js`, `manifest.webmanifest`,
`service-worker.js`, and `icon.svg`.

## Controls

- **Keyboard (default):** Arrows = D-pad, Z/X = A/B, Enter = Start,
  RShift = Select. **F1–F4** save slots 1–4; **Shift+F1–F4** load them.
- **Gamepad:** standard mapping (face buttons A/B, Start/Select, D-pad +
  left stick). Plug in and it's picked up automatically.
- **Remap keys:** rebinds the keyboard (persisted in `localStorage`); a
  custom map disables the built-in keys so it fully owns input.

## JS ↔ Go API

`cmd/nessy-wasm/main.go` installs a global `nessy` object:

| method | purpose |
|---|---|
| `loadROM(Uint8Array)` | swap the running ROM; `"ok"` or an error string |
| `setButton(idx, down)` | inject input (`idx` 0..7 = A,B,Select,Start,Up,Down,Left,Right) |
| `romHash()` | current ROM's SHA-256 hex — the save-slot key |
| `setKeyboard(on)` | toggle the built-in keymap |
| `screenshot()` | `Uint8Array` of the 256×240 RGBA framebuffer |
| `saveState()` | `Uint8Array` snapshot (gzip+gob), or `null` |
| `loadState(Uint8Array)` | restore a snapshot; `"ok"` or an error string |

Save-state bytes use the same subsystem `FullState` serialization as the
desktop build (`cmd/nessy/savestate.go`), with a magic/version + ROM-hash
guard; slots live in the `nessy-saves` IndexedDB database.
