# Architecture Decision Records

nessy keeps one ADR per released version under `docs/adr/`, capturing the
architectural decisions made in that version with their context and
consequences. They're a reconstruction from the git history, `CLAUDE.md`, and
`docs/context.md` — the canonical "why we built it this way" log.

Each entry uses a compact form: **Context** (the forces), **Decision** (what we
chose), **Consequences** (what it bought / cost). Decisions that a later release
reversed or refined are marked *Superseded by* with a link.

## Index

| ADR | Release | Date | Theme |
|-----|---------|------|-------|
| [0001](0001-v0.1.0.md) | v0.1.0 | 2026-05-15 | NROM foundation; Ebiten window; chippy CPU+DAP dependency; one-shell debug attach |
| [0002](0002-v0.2.0.md) | v0.2.0 | 2026-05-20 | Sprites + $4014 OAMDMA + mid-frame scroll; per-scanline render |
| [0003](0003-v0.3.0.md) | v0.3.0 | 2026-05-24 | 5-channel APU + frame counter; MMC1 serial-shift mapper |
| [0004](0004-v0.4.0.md) | v0.4.0 | 2026-05-24 | Per-dot PPU (stepDot); save-state v1; battery PRG-RAM; UxROM/CNROM/MMC3 |
| [0005](0005-v0.5.0.md) | v0.5.0 | 2026-05-24 | VRC2/4 family; FME-7 + Sunsoft 5B audio; $2007 open-bus latch |
| [0006](0006-v0.6.0.md) | v0.6.0 | 2026-05-24 | VRC6 audio; VRC7 cart shell; WASM playground; DMC/OAMDMA contention |
| [0007](0007-v0.7.0.md) | v0.7.0 | 2026-05-25 | VRC7 OPLL synth; PAL/Dendy region timing; headless recorder; accuracy ROM suite |
| [0008](0008-v0.8.0.md) | v0.8.0 | 2026-05-25 | chippy monorepo carve-out; Mesen2 cycle-precision reference; load-bearing invariants |

## Conventions captured across releases

- **One GitHub issue → `feat/<name>` branch off `main` → squash-merge `--delete-branch`.** Conventional Commits (`feat:`, `fix:`, `docs:`, `ci:`, `test:`, `refactor:`, `chore:`); PR body ends with `Closes #N`.
- **Quality gate (every PR):** `go build -tags=nessy ./...`, `go test -race`, `golangci-lint run`, with docs updated in the same PR (`README.md`, `docs/`, `CLAUDE.md`, and code-level doc comments as the diff implies).
- **Releases:** bare `vX.Y.Z` tags — the `nessy-v*` prefix used in the chippy monorepo was renamed at carve-out (`git filter-repo --tag-rename nessy-v:v`).
- **GPL accuracy/test ROMs are never vendored** — downloaded on demand + sha256-pinned (or ca65-ported from source).
- **Sole author** — commits carry no `Co-Authored-By: Claude` trailer, and PRs no "Generated with Claude Code" footer.

## Forthcoming

The v0.9 / v1.0 ADR — covering the post-carve DAP debugger epic, the chippy
v1.5.0 host hooks, and the MMC5 mapper — lands when that release is tagged. The
v1.0 epic is tracked in [issue #13](https://github.com/nkane/nessy/issues/13).
