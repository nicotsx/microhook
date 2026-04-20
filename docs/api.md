# API Reference

## Authentication

All `/v1/...` endpoints require `Authorization: Bearer <token>`.

`GET /healthz` is intentionally unauthenticated.

Tokens may be global or scoped to a subset of actions.

## Endpoints

### `POST /v1/actions/:name/runs`

Invoke an action.

Request body:

```json
{
  "mode": "sync",
  "input": {
    "request_id": "backup-123",
    "reason": "backup-start"
  }
}
```

Notes:

- `mode` is required and must be `sync` or `async`.
- `input` may be any JSON value.
- If `X-Request-Id` is set, it overrides `input.request_id` for request tracking.

Sync response example:

```json
{
  "id": "run_0123456789abcdef0123456789abcdef",
  "action": "hello",
  "status": "succeeded",
  "exit_code": 0,
  "created_at": "2026-04-21T10:15:00Z",
  "started_at": "2026-04-21T10:15:00Z",
  "finished_at": "2026-04-21T10:15:00Z",
  "request_metadata": {
    "mode": "sync",
    "request_id": "backup-123"
  },
  "stdout_tail": "hello"
}
```

Async response example:

```json
{
  "id": "run_0123456789abcdef0123456789abcdef",
  "action": "hello",
  "status": "queued",
  "created_at": "2026-04-21T10:15:00Z",
  "request_metadata": {
    "mode": "async",
    "request_id": "backup-123"
  }
}
```

### `GET /v1/runs/:id`

Fetch one run by ID.

### `GET /v1/runs`

List runs.

Supported query parameters:

- `action=<name>`
- `status=<queued|running|succeeded|failed|timed_out|cancelled>`

### `GET /healthz`

Returns `200 OK` with a plain-text body of `ok`.

## Run Statuses

- `queued`
- `running`
- `succeeded`
- `failed`
- `timed_out`
- `cancelled`

## HTTP Status Codes

| Status | Meaning |
| --- | --- |
| `200` | Sync request completed or read succeeded |
| `202` | Async request accepted |
| `400` | Bad request body or invalid filter |
| `401` | Missing or invalid bearer token |
| `403` | Token is valid but not allowed to invoke the action |
| `404` | Unknown action or run |
| `409` | Concurrency conflict under `reject` |
| `500` | Internal server error |

## Common curl Examples

Health:

```bash
curl http://127.0.0.1:9464/healthz
```

Invoke sync:

```bash
curl \
  -H 'Authorization: Bearer <token>' \
  -H 'Content-Type: application/json' \
  -d '{"mode":"sync","input":{"request_id":"req-1","reason":"backup-start"}}' \
  http://127.0.0.1:9464/v1/actions/hello/runs
```

Invoke async with explicit request ID header:

```bash
curl \
  -H 'Authorization: Bearer <token>' \
  -H 'X-Request-Id: req-2' \
  -H 'Content-Type: application/json' \
  -d '{"mode":"async","input":{"request_id":"ignored-by-header"}}' \
  http://127.0.0.1:9464/v1/actions/hello/runs
```

List recent failed runs for one action:

```bash
curl \
  -H 'Authorization: Bearer <token>' \
  'http://127.0.0.1:9464/v1/runs?action=hello&status=failed'
```

Fetch a run by ID:

```bash
curl \
  -H 'Authorization: Bearer <token>' \
  http://127.0.0.1:9464/v1/runs/run_0123456789abcdef0123456789abcdef
```
