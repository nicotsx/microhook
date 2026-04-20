# Security Model

## Core Boundary

Microhook executes only actions declared in config.

It does not accept arbitrary commands from HTTP requests.

That boundary is the product's core safety property.

## Auth and Authorization

- All `/v1/...` endpoints require a bearer token.
- Tokens are compared in a timing-safe way.
- Tokens can be global or scoped to a list of allowed actions.
- Invalid or missing tokens return `401`.
- Valid tokens without action access return `403`.

Generate tokens with:

```bash
microhook generate-token
```

## Network Exposure

Microhook binds to `127.0.0.1` by default.

Keep it local unless you explicitly need remote callers.

If you do expose it remotely:

- put it behind a reverse proxy or equivalent trusted boundary
- keep TLS termination outside the service if needed
- treat every configured action as remotely triggerable
- rotate tokens if they are ever disclosed

## Process Spawning Model

- `command` mode executes an argv array directly
- `shell_command` mode runs `/bin/sh -c ...`
- request JSON is passed on stdin
- request content is not interpolated into argv or shell strings
- child processes get a minimal environment instead of the full parent environment

Microhook always sets:

- `MICROHOOK_RUN_ID`
- `MICROHOOK_ACTION`
- `MICROHOOK_REQUEST_ID`

## Logging

- Server logs are structured by default
- Request logs avoid printing bearer tokens
- Run output capture is bounded by `max_output_bytes`
- stdout and stderr tails are stored for inspection

Do not put secrets on stdout or stderr unless you are comfortable storing their tails in run history.

## Safe Action Guidance

Prefer these patterns:

- use `command` mode instead of `shell_command`
- keep actions small and purpose-specific
- use a dedicated service user
- set an explicit `cwd` when the action depends on one
- set a timeout for anything that could hang
- keep fixed environment variables explicit in config
- make actions idempotent when callers may retry

Avoid these patterns:

- concatenating request content into shell strings
- using Microhook as a generic command gateway
- running the service as `root` unless there is a very clear reason
- exposing it directly to the public internet

## Shell Mode Risk

`shell_command` is an escape hatch, not the default path.

It is less safe because shell parsing introduces:

- quoting mistakes
- glob expansion
- environment expansion
- command substitution risk
- more room for accidental request-driven behavior

Use shell mode only when shell features are genuinely required, and keep request data on stdin.

## File Permissions

Recommended starting point:

- config directory: `0750`
- config file: `0600`
- data directory: `0750`
- service user: dedicated, non-login account

The `systemd` unit and install docs assume a dedicated `microhook` user.
