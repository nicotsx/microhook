# Release Process

## Goals

Each release should produce:

- static Linux binaries for `amd64` and `arm64`
- release tarballs that include docs, example config, and the `systemd` unit
- a convenience Docker image
- enough validation evidence to ship with confidence

## Build Release Artifacts

Set the release version and build the tarballs:

```bash
VERSION=v1.0.0 make release-artifacts
```

That produces:

- `dist/microhook_v1.0.0_linux_amd64.tar.gz`
- `dist/microhook_v1.0.0_linux_arm64.tar.gz`
- `dist/checksums.txt`

## Validate Release Readiness

Run the local release gate:

```bash
VERSION=v1.0.0 make release-check
```

`make release-check` runs:

- `go test ./...`
- release artifact packaging
- Linux install smoke validation when running on Linux
- packaged-binary release e2e validation when running on Linux
- `systemd` unit verification when `systemd-analyze` is available

The GitHub Actions workflow at `.github/workflows/release-readiness.yml` runs the same release-readiness path on a clean Ubuntu runner and also verifies the Docker build.

## Docker Image

Build the convenience image locally:

```bash
make docker-build
```

For a multi-arch published image, use `buildx` during release execution:

```bash
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  -t ghcr.io/<owner>/microhook:v1.0.0 \
  -t ghcr.io/<owner>/microhook:latest \
  --push .
```

## Clean-Host Verification

After building a release tarball, verify installation on a clean Linux host:

```bash
./scripts/install-smoke.sh dist/microhook_v1.0.0_linux_amd64.tar.gz
./scripts/release-e2e.sh dist/microhook_v1.0.0_linux_amd64.tar.gz
./scripts/verify-systemd.sh
```

The smoke test validates:

- artifact structure
- config validation
- service startup
- `/healthz`
- authenticated invocation
- run lookup
- invalid-config failure behavior

The packaged-binary release e2e validates:

- bearer-token auth boundaries (`401`, `403`)
- sync and async action execution through the real HTTP API
- `400`, `404`, and `409` API behavior
- timeout handling
- restart recovery for interrupted runs
- persisted lookup and filtered listing after restart

## Publish Checklist

Use `docs/release-checklist.md` as the sign-off document.

At a minimum, publish only after:

- the automated release-readiness job is green
- the clean-host verification pass is complete
- the manual security review checklist is signed off
