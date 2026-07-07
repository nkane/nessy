// nessy playground shell (#10). Owns everything around the wasm core:
// wasm boot + canvas mount, ROM file picker + drag/drop, gamepad polling,
// keyboard remap, IndexedDB save slots, screenshot, fullscreen, and PWA
// service-worker registration. The Go core exposes the `nessy` API
// (loadROM / setButton / romHash / setKeyboard / saveState / loadState);
// see cmd/nessy-wasm/main.go.

'use strict';

const NUM_SLOTS = 4;
const NES_W = 256, NES_H = 240;
// NES button indices as the Go core expects them (joypad.Button order).
const BTN = { A: 0, B: 1, SELECT: 2, START: 3, UP: 4, DOWN: 5, LEFT: 6, RIGHT: 7 };

const els = {};
let canvas = null;

function setStatus(msg) {
  if (els.status) els.status.textContent = msg;
}

// ---- wasm boot -------------------------------------------------------

async function boot() {
  ['status', 'canvasHost', 'romPicker', 'fullscreenBtn', 'shotBtn',
   'slots', 'remapBtn', 'gamepadState'].forEach((id) => {
    els[id] = document.getElementById(id.replace(/[A-Z]/g, (m) => '-' + m.toLowerCase()));
  });

  setStatus('fetching wasm…');
  const go = new Go();
  const resp = await fetch('nessy.wasm');
  const result = await WebAssembly.instantiateStreaming(resp, go.importObject);
  setStatus('starting emulator…');
  go.run(result.instance); // Ebiten owns the loop after this.

  // Ebiten mounts its canvas into <body>; relocate + size to 3× native.
  await new Promise((resolve) => {
    const t = setInterval(() => {
      canvas = document.querySelector('canvas');
      if (!canvas) return;
      clearInterval(t);
      els.canvasHost.appendChild(canvas);
      canvas.style.width = '768px';
      canvas.style.height = '720px';
      resolve();
    }, 50);
  });
  setStatus('running default demo');

  wireROMInput();
  wireDragDrop();
  wireButtons();
  buildSlotUI();
  refreshSlots();
  loadRemap();
  startGamepadLoop();
  wireKeyboardRemap();
  registerServiceWorker();
}

// ---- ROM loading -----------------------------------------------------

async function loadROMFile(file) {
  if (!file) return;
  const buf = new Uint8Array(await file.arrayBuffer());
  const msg = nessy.loadROM(buf);
  if (msg === 'ok') {
    setStatus(`loaded ${file.name}`);
    refreshSlots();
  } else {
    setStatus(`error: ${msg}`);
  }
}

function wireROMInput() {
  els.romPicker.addEventListener('change', (e) => loadROMFile(e.target.files[0]));
}

function wireDragDrop() {
  const host = els.canvasHost;
  const stop = (e) => { e.preventDefault(); e.stopPropagation(); };
  ['dragenter', 'dragover'].forEach((ev) => host.addEventListener(ev, (e) => {
    stop(e); host.classList.add('drag');
  }));
  ['dragleave', 'drop'].forEach((ev) => host.addEventListener(ev, (e) => {
    stop(e); host.classList.remove('drag');
  }));
  host.addEventListener('drop', (e) => {
    const f = e.dataTransfer.files[0];
    if (f) loadROMFile(f);
  });
}

// ---- screenshot + fullscreen ----------------------------------------

function wireButtons() {
  els.fullscreenBtn.addEventListener('click', () => {
    if (document.fullscreenElement) document.exitFullscreen();
    else els.canvasHost.requestFullscreen?.();
  });
  els.shotBtn.addEventListener('click', () => {
    // Pull the RGBA framebuffer from the core (Ebiten's WebGL canvas
    // reads back empty without preserveDrawingBuffer), paint it to an
    // offscreen canvas, and download the PNG.
    const rgba = nessy.screenshot();
    if (!rgba || rgba.length < NES_W * NES_H * 4) { setStatus('screenshot failed'); return; }
    const buf = new Uint8ClampedArray(NES_W * NES_H * 4);
    buf.set(rgba.subarray(0, buf.length));
    const off = document.createElement('canvas');
    off.width = NES_W; off.height = NES_H;
    off.getContext('2d').putImageData(new ImageData(buf, NES_W, NES_H), 0, 0);
    off.toBlob((blob) => {
      if (!blob) { setStatus('screenshot failed'); return; }
      const a = document.createElement('a');
      a.href = URL.createObjectURL(blob);
      a.download = `nessy-${Date.now()}.png`;
      a.click();
      URL.revokeObjectURL(a.href);
    }, 'image/png');
  });
}

// ---- save slots (IndexedDB) -----------------------------------------

const DB_NAME = 'nessy-saves';
const STORE = 'slots';
let dbPromise = null;

function db() {
  if (dbPromise) return dbPromise;
  dbPromise = new Promise((resolve, reject) => {
    const req = indexedDB.open(DB_NAME, 1);
    req.onupgradeneeded = () => req.result.createObjectStore(STORE);
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
  return dbPromise;
}

function slotKey(slot) {
  const hash = nessy.romHash();
  return `${hash}:${slot}`;
}

async function dbGet(key) {
  const d = await db();
  return new Promise((resolve, reject) => {
    const req = d.transaction(STORE, 'readonly').objectStore(STORE).get(key);
    req.onsuccess = () => resolve(req.result || null);
    req.onerror = () => reject(req.error);
  });
}

async function dbPut(key, val) {
  const d = await db();
  return new Promise((resolve, reject) => {
    const tx = d.transaction(STORE, 'readwrite');
    tx.objectStore(STORE).put(val, key);
    tx.oncomplete = () => resolve();
    tx.onerror = () => reject(tx.error);
  });
}

async function saveSlot(slot) {
  const bytes = nessy.saveState();
  if (!bytes) { setStatus(`slot ${slot}: save failed`); return; }
  const copy = new Uint8Array(bytes.length);
  copy.set(bytes);
  await dbPut(slotKey(slot), copy);
  setStatus(`saved slot ${slot}`);
  refreshSlots();
}

async function loadSlot(slot) {
  const bytes = await dbGet(slotKey(slot));
  if (!bytes) { setStatus(`slot ${slot} empty`); return; }
  const msg = nessy.loadState(new Uint8Array(bytes));
  setStatus(msg === 'ok' ? `loaded slot ${slot}` : `slot ${slot}: ${msg}`);
}

function buildSlotUI() {
  els.slots.innerHTML = '';
  for (let s = 1; s <= NUM_SLOTS; s++) {
    const row = document.createElement('div');
    row.className = 'slot';
    const label = document.createElement('span');
    label.textContent = `Slot ${s}`;
    const save = document.createElement('button');
    save.textContent = 'Save';
    save.addEventListener('click', () => saveSlot(s));
    const load = document.createElement('button');
    load.textContent = 'Load';
    load.dataset.slot = s;
    load.addEventListener('click', () => loadSlot(s));
    row.append(label, save, load);
    els.slots.append(row);
  }
}

async function refreshSlots() {
  for (const load of els.slots.querySelectorAll('button[data-slot]')) {
    const has = await dbGet(slotKey(load.dataset.slot));
    load.disabled = !has;
    load.classList.toggle('filled', !!has);
  }
}

// ---- gamepad ---------------------------------------------------------

// Standard-mapping gamepad → NES. buttons[0/1]=A/B, [8/9]=Select/Start,
// [12..15]=Dpad; left stick doubles the Dpad.
const GP_BUTTONS = [
  [0, BTN.A], [1, BTN.B], [8, BTN.SELECT], [9, BTN.START],
  [12, BTN.UP], [13, BTN.DOWN], [14, BTN.LEFT], [15, BTN.RIGHT],
];
let gamepadIndex = null;

function startGamepadLoop() {
  window.addEventListener('gamepadconnected', (e) => {
    gamepadIndex = e.gamepad.index;
    if (els.gamepadState) els.gamepadState.textContent = `gamepad: ${e.gamepad.id}`;
  });
  window.addEventListener('gamepaddisconnected', () => {
    gamepadIndex = null;
    if (els.gamepadState) els.gamepadState.textContent = 'gamepad: none';
    for (const [, nes] of GP_BUTTONS) nessy.setButton(nes, false);
  });

  const poll = () => {
    if (gamepadIndex !== null) {
      const gp = navigator.getGamepads()[gamepadIndex];
      if (gp) {
        for (const [gpBtn, nes] of GP_BUTTONS) {
          nessy.setButton(nes, gp.buttons[gpBtn]?.pressed || false);
        }
        const [ax, ay] = [gp.axes[0] || 0, gp.axes[1] || 0];
        const dz = 0.5;
        if (ax < -dz) nessy.setButton(BTN.LEFT, true);
        if (ax > dz) nessy.setButton(BTN.RIGHT, true);
        if (ay < -dz) nessy.setButton(BTN.UP, true);
        if (ay > dz) nessy.setButton(BTN.DOWN, true);
      }
    }
    requestAnimationFrame(poll);
  };
  requestAnimationFrame(poll);
}

// ---- keyboard remap --------------------------------------------------

// Default remap mirrors the built-in keymap; editable + persisted. When a
// custom map is active we disable the Go core's built-in keymap so the JS
// map fully owns keyboard input (otherwise the two OR together).
const DEFAULT_REMAP = {
  ArrowUp: BTN.UP, ArrowDown: BTN.DOWN, ArrowLeft: BTN.LEFT, ArrowRight: BTN.RIGHT,
  KeyZ: BTN.A, KeyX: BTN.B, Enter: BTN.START, ShiftRight: BTN.SELECT,
};
let remap = null;
let remapActive = false;

function loadRemap() {
  const raw = localStorage.getItem('nessy-remap');
  if (raw) {
    try { remap = JSON.parse(raw); remapActive = true; } catch { remap = null; }
  }
  applyRemapMode();
}

function applyRemapMode() {
  // Custom map → JS owns the keyboard (turn the Go keymap off).
  nessy.setKeyboard(!remapActive);
}

function wireKeyboardRemap() {
  const active = () => remapActive ? remap : DEFAULT_REMAP;
  window.addEventListener('keydown', (e) => {
    if (!remapActive) return; // built-in keymap is handled in Go
    const nes = active()[e.code];
    if (nes !== undefined) { nessy.setButton(nes, true); e.preventDefault(); }
  });
  window.addEventListener('keyup', (e) => {
    if (!remapActive) return;
    const nes = active()[e.code];
    if (nes !== undefined) { nessy.setButton(nes, false); e.preventDefault(); }
  });

  if (!els.remapBtn) return;
  els.remapBtn.addEventListener('click', () => runRemapWizard());
}

// runRemapWizard walks the 8 buttons, capturing the next keypress for each.
async function runRemapWizard() {
  const order = [
    ['Up', BTN.UP], ['Down', BTN.DOWN], ['Left', BTN.LEFT], ['Right', BTN.RIGHT],
    ['A', BTN.A], ['B', BTN.B], ['Start', BTN.START], ['Select', BTN.SELECT],
  ];
  const map = {};
  for (const [name, nes] of order) {
    setStatus(`remap: press a key for ${name} (Esc to cancel)`);
    const code = await nextKey();
    if (code === 'Escape') { setStatus('remap cancelled'); applyRemapMode(); return; }
    map[code] = nes;
  }
  remap = map;
  remapActive = true;
  localStorage.setItem('nessy-remap', JSON.stringify(map));
  applyRemapMode();
  setStatus('keyboard remapped (saved)');
}

function nextKey() {
  return new Promise((resolve) => {
    const on = (e) => {
      e.preventDefault();
      window.removeEventListener('keydown', on, true);
      resolve(e.code);
    };
    window.addEventListener('keydown', on, true);
  });
}

// ---- F1-F4 quick save / Shift+F load ---------------------------------

window.addEventListener('keydown', (e) => {
  const m = e.code.match(/^F([1-4])$/);
  if (!m) return;
  e.preventDefault();
  const slot = Number(m[1]);
  if (e.shiftKey) loadSlot(slot);
  else saveSlot(slot);
});

// ---- PWA -------------------------------------------------------------

function registerServiceWorker() {
  if ('serviceWorker' in navigator) {
    navigator.serviceWorker.register('service-worker.js').catch(() => {});
  }
}

boot();
