# Example Integrations

## Backup Window Stop/Start

Config:

```yaml
actions:
  - name: "stop-mydb"
    description: "Stop the compose stack before backup"
    command: ["docker", "compose", "down"]
    cwd: "/srv/mydb"
    timeout: "120s"
    concurrency_policy: "reject"
    max_output_bytes: 65536
    enabled: true

  - name: "start-mydb"
    description: "Start the compose stack after backup"
    command: ["docker", "compose", "up", "-d"]
    cwd: "/srv/mydb"
    timeout: "120s"
    concurrency_policy: "reject"
    max_output_bytes: 65536
    enabled: true
```

Caller example:

```bash
curl \
  -H 'Authorization: Bearer <token>' \
  -H 'Content-Type: application/json' \
  -d '{"mode":"sync","input":{"request_id":"backup-start-001"}}' \
  http://127.0.0.1:9464/v1/actions/stop-mydb/runs
```

## GitHub Actions Caller

```yaml
name: Trigger Microhook

on:
  workflow_dispatch:

jobs:
  run-action:
    runs-on: ubuntu-latest
    steps:
      - name: Invoke Microhook
        env:
          MICROHOOK_TOKEN: ${{ secrets.MICROHOOK_TOKEN }}
        run: |
          curl \
            -H "Authorization: Bearer ${MICROHOOK_TOKEN}" \
            -H "X-Request-Id: github-${GITHUB_RUN_ID}" \
            -H 'Content-Type: application/json' \
            -d '{"mode":"async","input":{"source":"github-actions"}}' \
            http://microhook.internal/v1/actions/deploy/runs
```

## Local Diagnostics Action

```yaml
actions:
  - name: "collect-diagnostics"
    description: "Capture local service state for investigation"
    command: ["/bin/sh", "-c", "cat >/dev/null; systemctl status myapp; journalctl -u myapp -n 200"]
    timeout: "30s"
    concurrency_policy: "reject"
    max_output_bytes: 65536
    enabled: true
```

That pattern keeps the remote contract narrow while still gathering useful local information.

## Request Metadata Pattern

The request body is available on stdin.

Example request:

```json
{
  "mode": "sync",
  "input": {
    "request_id": "alert-2048",
    "reason": "disk-space-low",
    "host": "db-1"
  }
}
```

Example action that reads stdin explicitly:

```yaml
actions:
  - name: "inspect-request"
    command: ["/bin/sh", "-c", "cat > /tmp/microhook-input.json; printf captured"]
    timeout: "10s"
    concurrency_policy: "allow"
    max_output_bytes: 1024
    enabled: true
```
