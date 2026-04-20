CREATE TABLE IF NOT EXISTS microhook_metadata (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS microhook_runs (
    id TEXT PRIMARY KEY,
    action_name TEXT NOT NULL,
    status TEXT NOT NULL,
    exit_code INTEGER,
    created_at_unix_nano INTEGER NOT NULL,
    started_at_unix_nano INTEGER,
    finished_at_unix_nano INTEGER,
    timed_out INTEGER NOT NULL DEFAULT 0,
    request_metadata_json BLOB,
    stdout_tail TEXT NOT NULL DEFAULT '',
    stderr_tail TEXT NOT NULL DEFAULT '',
    error_summary TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS microhook_action_snapshots (
    run_id TEXT PRIMARY KEY REFERENCES microhook_runs(id) ON DELETE CASCADE,
    description TEXT NOT NULL,
    mode TEXT NOT NULL,
    command_json BLOB NOT NULL,
    shell_command TEXT NOT NULL,
    cwd TEXT NOT NULL,
    timeout_nanoseconds INTEGER NOT NULL,
    env_json BLOB NOT NULL,
    concurrency_policy TEXT NOT NULL,
    max_output_bytes INTEGER NOT NULL,
    enabled INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_microhook_runs_action_created
    ON microhook_runs(action_name, created_at_unix_nano DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_microhook_runs_status_created
    ON microhook_runs(status, created_at_unix_nano DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_microhook_runs_created
    ON microhook_runs(created_at_unix_nano DESC, id DESC);

CREATE VIEW IF NOT EXISTS microhook_run_records AS
SELECT
    r.id,
    r.action_name,
    r.status,
    r.exit_code,
    r.created_at_unix_nano,
    r.started_at_unix_nano,
    r.finished_at_unix_nano,
    r.timed_out,
    r.request_metadata_json,
    r.stdout_tail,
    r.stderr_tail,
    r.error_summary,
    s.description,
    s.mode,
    s.command_json,
    s.shell_command,
    s.cwd,
    s.timeout_nanoseconds,
    s.env_json,
    s.concurrency_policy,
    s.max_output_bytes,
    s.enabled
FROM microhook_runs r
JOIN microhook_action_snapshots s ON s.run_id = r.id;
