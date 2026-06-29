# Release process

nessy ships per-OS binaries under bare `vX.Y.Z` tags via
`.github/workflows/release.yml`. Every artifact is cosign-signed
(keyless) and carries an SPDX SBOM; Linux also gets `.deb` / `.rpm` /
`.apk`, and stable releases push a Homebrew cask + an AUR package.

## Why this is hand-rolled (not goreleaser like chippy)

chippy is pure Go (`CGO_ENABLED=0`) and goreleaser cross-compiles every
target on one runner. **nessy is an Ebiten CGO app** — it can't be
cross-compiled from a single runner, and goreleaser's OSS build can't
ingest externally-built CGO binaries (the `prebuilt` builder was removed;
split/merge is Pro-only). So the workflow builds each target **natively
in a matrix** (`macos-latest`+`macos-13` for darwin arm64/amd64,
`ubuntu-latest`+`ubuntu-24.04-arm` for linux, `windows-latest`), then a
finalize job archives, checksums, signs (cosign), SBOMs (syft), packages
(`nfpm`, config in `packaging/nfpm.yaml`), and publishes. The artifacts
match chippy's shape; only the driver differs.

## Cutting a release

```sh
git checkout main && git pull
git tag v1.0.0          # bare semver; prerelease = a hyphen, e.g. v1.0.0-rc.1
git push origin v1.0.0
```

A **prerelease** tag (any `-`, e.g. `v0.9.0-rc.1`) builds + signs + packages
+ publishes a GitHub prerelease but **skips Homebrew + AUR** — so it's the
safe end-to-end smoke test and needs no secrets. A **stable** tag runs
everything.

## Pre-tag checklist

- `go build -tags=nessy ./...`
- `go test ./...` + `go test -tags=accuracy ./cmd/nessy/...`
- `golangci-lint run ./...`
- README + `docs/` current

## Secrets (required only for STABLE releases)

Add to the `nkane/nessy` repo (Settings → Secrets → Actions):

- `HOMEBREW_TAP_GITHUB_TOKEN` — a PAT with `repo` scope on
  `nkane/homebrew-tap` (the cask is written to `Casks/nessy.rb`).
- `AUR_SSH_PRIVATE_KEY` — the SSH private key for the AUR account that
  owns `nessy-bin`.

cosign signing + SBOMs + Linux packages need **no** secrets (keyless OIDC
via `id-token: write`).

## Verifying an artifact

```sh
cosign verify-blob \
  --certificate-identity=https://github.com/nkane/nessy/.github/workflows/release.yml@refs/tags/<TAG> \
  --certificate-oidc-issuer=https://token.actions.githubusercontent.com \
  --bundle nessy_<VERSION>_linux_x86_64.tar.gz.cosign.bundle \
  nessy_<VERSION>_linux_x86_64.tar.gz
```
