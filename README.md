# Microhook

Microhook is a lightweight, self-hosted action runner that exposes an authenticated HTTP API for triggering pre-registered actions on a local machine.

It is built for operators who want a small, auditable alternative to "remote shell over HTTP".

Disclaimer: This is an experiment, do not run this in production without a thorough security review and understanding of the code and its limitations. See `docs/security.md` for more details.

## What It Is

- A single Go binary.
- A local daemon with a small HTTP API.
- A fixed-action runner driven by YAML config.
- A Linux-first service intended to run under `systemd`.
- A tool that captures per-run status, timestamps, and bounded stdout/stderr.

## What It Is Not

- A generic remote shell.
- A workflow engine.
- A scheduler or cron replacement.
- A secrets manager.
- An internet-facing automation gateway.

## Quick Start

1. Build the binary:

```bash
make build
```

2. Generate a bearer token:

```bash
./bin/microhook generate-token
```

3. Start from `packaging/examples/microhook.yml`, replace the sample token, then validate it:

```bash
./bin/microhook validate-config -config packaging/examples/microhook.yml
```

4. Run the service:

```bash
./bin/microhook serve -config packaging/examples/microhook.yml
```

5. Trigger an action:

```bash
curl \
  -H 'Authorization: Bearer <token>' \
  -H 'Content-Type: application/json' \
  -d '{"mode":"sync","input":{"request_id":"demo-1"}}' \
  http://127.0.0.1:9464/v1/actions/hello/runs
```

## Docs

- Install and operations: `docs/install.md`
- Config reference: `docs/config.md`
- API reference: `docs/api.md`
- Security model and safe action guidance: `docs/security.md`
- Integration examples: `docs/examples.md`
- Troubleshooting: `docs/troubleshooting.md`
- Release process: `docs/release.md`
- Release checklist: `docs/release-checklist.md`

## Packaging

- Example config: `packaging/examples/microhook.yml`
- `systemd` unit: `packaging/systemd/microhook.service`
- Release artifact builder: `scripts/build-release.sh`
- Linux install smoke test: `scripts/install-smoke.sh`
- Packaged-binary Linux e2e: `scripts/release-e2e.sh`
- `systemd` verifier: `scripts/verify-systemd.sh`

## Release Commands

- `make release-artifacts` builds static Linux tarballs for `amd64` and `arm64`.
- `make docker-build` builds the convenience container image.
- `make release-check` runs tests, packages release artifacts, runs the Linux install smoke and packaged-binary e2e checks when possible, and verifies the `systemd` unit when possible.
