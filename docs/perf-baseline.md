# Perf baseline (#11 perfgate)

The perfgate locks in sustained 60 fps so the per-cycle CPU↔PPU interleave
(chippy #342/#372/#377) can't quietly regress. It benchmarks the
steady-state per-frame emulation cost on three committed demo ROMs, each
hitting a distinct hot path:

| Benchmark | ROM | Hot path |
|---|---|---|
| `BenchmarkFrame_HelloBG` | `hello-bg` | background-only per-dot fetch + shift pipeline |
| `BenchmarkFrame_OAMGrid` | `oam-grid` | full sprite evaluation + fetch every scanline |
| `BenchmarkFrame_MMC3Split` | `mmc3-split` | MMC3 scanline IRQ (A12 low-time filter) + mid-frame scroll split over rendering |

The benchmark + gate live in `cmd/nessy-record/perfgate_test.go` (headless
— no Ebiten/CGO, so CI runs it with a plain `-tags=perfgate`). The
committed ceilings are in `cmd/nessy-record/testdata/perf-baseline.json`.

## The gate

`TestPerfGate` runs each benchmark via `testing.Benchmark`, then fails if
its `ns/op` (nanoseconds per emulated frame) exceeds the baseline
`ns_op_max` by more than the tolerance (**1.10 → a >10% regression
fails**, per #11). It also logs the realtime multiple
(`16.64 ms / ns_per_frame`) so the 60 fps headroom is visible in the job
output.

CI (`.github/workflows/ci.yml`, job `perf baseline`):

```sh
go test -tags=perfgate -timeout 5m -run TestPerfGate ./...
```

## Why the baseline is measured on CI, not locally

`ns/op` is hardware-absolute. A baseline recorded on a fast dev machine
(Apple Silicon runs these ~5× realtime) would be far tighter than the
ubuntu-latest CI runner can meet, so CI would false-fail immediately. The
baseline must therefore be recorded **on the same runner class that
enforces it** — ubuntu-latest.

## Reseeding the baseline

Reseed on a known-good `ubuntu-latest` commit (a green `main`, no
in-flight perf work):

1. Read the actual per-frame numbers the perfgate logged on that commit's
   CI run — each line is `BenchmarkFrame_X: <N> ns/frame (...)`.
2. Set each `ns_op_max` in `testdata/perf-baseline.json` to that observed
   `N`, rounded up ~5% to absorb shared-runner noise. With the 1.10 gate
   tolerance on top, a clean run then sits ~15% under the fail line —
   enough margin that runner jitter alone won't trip it, while a real
   >10% regression still does.
3. Commit as `chore(perf): reseed #11 baseline from ubuntu-latest`.

Bump the ceilings (with justification in the commit) only for an
intentional, understood cost — never to paper over an unexplained
regression.
