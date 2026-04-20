# Config Reference

Microhook uses YAML configuration.

The default path is `/etc/microhook/config.yml`. You can override it with `-config` or `MICROHOOK_CONFIG`.

## Full Example

```yaml
server:
  listen: "127.0.0.1:9464"
  log_format: "json"

auth:
  tokens:
    - name: "backup-system"
      value: "mhv1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
      actions: ["hello"]

storage:
  path: "/var/lib/microhook/microhook.db"
  retention_days: 30
  max_runs: 10000

actions:
  - name: "hello"
    description: "Simple smoke-test action"
    command: ["/bin/sh", "-c", "cat >/dev/null; printf hello"]
    cwd: "/var/lib/microhook"
    timeout: "30s"
    env:
      EXAMPLE_ENV: "true"
    concurrency_policy: "allow"
    max_output_bytes: 65536
    enabled: true
```

## `server`

| Field | Required | Default | Notes |
| --- | --- | --- | --- |
| `listen` | no | `127.0.0.1:9464` | TCP listen address |
| `log_format` | no | `json` | `json` or `text` |

## `auth.tokens[]`

| Field | Required | Notes |
| --- | --- | --- |
| `name` | yes | Unique token name |
| `value` | yes | Must be a valid token from `microhook generate-token` |
| `actions` | no | Empty means global token; otherwise scopes the token to listed actions |

## `storage`

| Field | Required | Notes |
| --- | --- | --- |
| `path` | yes | SQLite database file path |
| `retention_days` | no | `0` disables age-based pruning |
| `max_runs` | no | `0` disables count-based pruning |

Retention pruning runs on startup.

## `actions[]`

| Field | Required | Default | Notes |
| --- | --- | --- | --- |
| `name` | yes | none | Unique action name |
| `description` | no | `""` | Free-form description |
| `command` | exactly one of `command` or `shell_command` | none | argv mode |
| `shell_command` | exactly one of `command` or `shell_command` | none | `/bin/sh -c ...`; less safe |
| `cwd` | no | `""` | Working directory for the child process |
| `timeout` | no | no timeout | Go duration string such as `30s` or `2m` |
| `env` | no | empty | Fixed environment variables added to the child process |
| `concurrency_policy` | no | `allow` | `allow` or `reject` |
| `max_output_bytes` | no | `65536` | Captured tail size for stdout and stderr |
| `enabled` | no | `true` | Disabled actions remain in config but cannot be invoked |

## Command Mode vs Shell Mode

Prefer `command`.

`command` executes an argv array directly. That keeps argument boundaries explicit.

`shell_command` uses `/bin/sh -c`. That enables shell features, but it also introduces quoting, expansion, and injection risk. Do not build shell strings from request content.

## Child Process Environment

Microhook does not inherit the full service environment.

Child processes receive:

- `PATH`
- Configured `env` entries
- `MICROHOOK_RUN_ID`
- `MICROHOOK_ACTION`
- `MICROHOOK_REQUEST_ID`

The request JSON body is passed on stdin.

## Validation Rules

Microhook fails fast on invalid config.

Common validation errors include:

- missing `storage.path`
- duplicate action names
- duplicate token names
- unknown actions in token scopes
- invalid token format
- invalid timeout strings
- unsupported concurrency policies
- defining both `command` and `shell_command`

Validate before deployment:

```bash
microhook validate-config -config /etc/microhook/config.yml
```
