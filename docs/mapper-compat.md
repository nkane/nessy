# Mapper compatibility

The iNES mapper byte selects the cartridge hardware nessy emulates. The
table below lists every mapper nessy ships today, one headliner ROM per
mapper, and any known gap still open against it. Unsupported mappers
fail closed at load with an explicit `cart: unsupported mapper N` error
naming the supported set, so a ROM that doesn't boot tells you why.

Dispatch lives in [`internal/nes/cart/cart.go`](../internal/nes/cart/cart.go);
each mapper has its own file + table-driven tests beside it.

## Supported mappers

| iNES # | Chip | PRG / CHR banking | Audio expansion | Headliner | Known gaps |
|---|---|---|---|---|---|
| 0 | NROM | Fixed 16/32 KiB PRG, 8 KiB CHR | — | Super Mario Bros, Donkey Kong | none |
| 1 | MMC1 | 16 KiB PRG banks, 4 KiB CHR banks | — | Zelda 1, Metroid, Final Fantasy | none |
| 2 | UxROM | 16 KiB switchable + fixed last, 8 KiB CHR-RAM | — | Mega Man 1-2, Contra, DuckTales | none |
| 3 | CNROM | Fixed PRG, 8 KiB switchable CHR | — | Arkanoid, Bump'n'Jump | none |
| 4 | MMC3 | 8/16 KiB PRG banks, 1/2 KiB CHR banks, scanline IRQ | — | Super Mario Bros 3, Kirby's Adventure | none — Sharp rev-B + NEC rev-A IRQ both modelled (rev-A keyed by ROM hash) |
| 5 | MMC5 | 8/16/32 KiB PRG banks, 1 KiB CHR banks, ExRAM, hardware multiplier, scanline IRQ | 2 pulse + PCM | Castlevania III (US), Just Breed | none |
| 7 | AOROM | 32 KiB switchable PRG, single-screen mirror flip | — | Battletoads, Marble Madness, R.C. Pro-Am | none |
| 21, 22, 23, 25 | VRC2 / VRC4 | 8 KiB PRG banks, 1 KiB CHR banks, VRC4 CPU/scanline IRQ | — | Crisis Force, Gradius II (JP) | none — one chip family across four pinouts via sub-bit routing |
| 24, 26 | VRC6a / VRC6b | 16+8 KiB PRG banks, 1 KiB CHR banks, IRQ | 2 pulse + sawtooth | Akumajou Densetsu (Castlevania III JP) | none |
| 69 | FME-7 / Sunsoft 5B | 8 KiB PRG banks (ROM or RAM sourced), 1 KiB CHR banks, IRQ | Sunsoft 5B (3 square) | Gimmick!, Batman: Return of the Joker | none |
| 85 | VRC7 | 8 KiB PRG banks, 1 KiB CHR banks, IRQ | YM2413 (OPLL) 6-voice FM | Lagrange Point | none for playability — the OPLL is a *functional* float-FM synth (audible, recognisable), not a cycle-exact log/exp-LUT OPLL ([chippy #315](https://github.com/nkane/chippy/issues/315)) |

## Notes

- **MMC3 IRQ revision.** The iNES header doesn't encode whether a cart
  carries the Sharp (rev-B) or NEC (rev-A) MMC3, and the two differ only
  in IRQ-on-reload timing. nessy keys the rev-A path off a `sha256(PRG||CHR)`
  hash table (see `mmc3.go`); everything else uses the rev-B default.
- **VRC2/4 family.** Mappers 21/22/23/25 are the same Konami silicon under
  different pinouts. Each mapper number (plus submapper) selects which CPU
  address bits carry the register sub-bits; VRC2 leaves the VRC4 IRQ silent.
- **VRC7 audio.** Complete — the cartridge banking / CHR / IRQ surface
  plus the YM2413 (OPLL) FM synth (`apu.VRC7Audio`): six melodic 2-operator
  voices from the 15-patch instrument ROM + one user patch, so Lagrange
  Point's soundtrack plays. It's a *functional* float-FM implementation
  (phase + ADSR advance once per emitted sample), not a cycle-exact OPLL
  log/exp pipeline — audible and recognisable; a bit-exact pass would be a
  separate effort (shipped v0.7, ADR 0007).

## Adding a mapper

Each mapper is a `Cartridge` implementation in `internal/nes/cart/` with a
constructor wired into `Open`'s dispatch switch and a table-driven test
covering its bank math and any IRQ/audio behaviour. Update this matrix in
the same change so it stays current each release (tracked by
[issue #9](https://github.com/nkane/nessy/issues/9)).
