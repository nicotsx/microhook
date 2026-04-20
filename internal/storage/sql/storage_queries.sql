-- name: CreateRun :exec
INSERT INTO microhook_runs (
    id,
    action_name,
    status,
    created_at_unix_nano,
    started_at_unix_nano,
    timed_out,
    request_metadata_json,
    stdout_tail,
    stderr_tail,
    error_summary
) VALUES (
    sqlc.arg(id),
    sqlc.arg(action_name),
    sqlc.arg(status),
    sqlc.arg(created_at_unix_nano),
    sqlc.narg(started_at_unix_nano),
    sqlc.arg(timed_out),
    sqlc.narg(request_metadata_json),
    sqlc.arg(stdout_tail),
    sqlc.arg(stderr_tail),
    sqlc.arg(error_summary)
);

-- name: CreateActionSnapshot :exec
INSERT INTO microhook_action_snapshots (
    run_id,
    description,
    mode,
    command_json,
    shell_command,
    cwd,
    timeout_nanoseconds,
    env_json,
    concurrency_policy,
    max_output_bytes,
    enabled
) VALUES (
    sqlc.arg(run_id),
    sqlc.arg(description),
    sqlc.arg(mode),
    sqlc.arg(command_json),
    sqlc.arg(shell_command),
    sqlc.arg(cwd),
    sqlc.arg(timeout_nanoseconds),
    sqlc.arg(env_json),
    sqlc.arg(concurrency_policy),
    sqlc.arg(max_output_bytes),
    sqlc.arg(enabled)
);

-- name: UpdateRun :execrows
UPDATE microhook_runs
SET
    status = sqlc.arg(status),
    exit_code = sqlc.narg(exit_code),
    started_at_unix_nano = sqlc.narg(started_at_unix_nano),
    finished_at_unix_nano = sqlc.narg(finished_at_unix_nano),
    timed_out = sqlc.arg(timed_out),
    stdout_tail = sqlc.arg(stdout_tail),
    stderr_tail = sqlc.arg(stderr_tail),
    error_summary = sqlc.arg(error_summary)
WHERE id = sqlc.arg(id);

-- name: GetRun :one
SELECT *
FROM microhook_run_records
WHERE id = sqlc.arg(run_id)
LIMIT 1;

-- name: ListRuns :many
SELECT *
FROM microhook_run_records
ORDER BY created_at_unix_nano DESC, id DESC;

-- name: ListRunsByActionName :many
SELECT *
FROM microhook_run_records
WHERE action_name = sqlc.arg(filter_action_name)
ORDER BY created_at_unix_nano DESC, id DESC;

-- name: ListRunsByStatus :many
SELECT *
FROM microhook_run_records
WHERE status = sqlc.arg(filter_status)
ORDER BY created_at_unix_nano DESC, id DESC;

-- name: ListRunsByActionNameAndStatus :many
SELECT *
FROM microhook_run_records
WHERE action_name = sqlc.arg(filter_action_name)
  AND status = sqlc.arg(filter_status)
ORDER BY created_at_unix_nano DESC, id DESC;

-- name: DeleteRunsOlderThan :execrows
DELETE FROM microhook_runs
WHERE id IN (
    SELECT id
    FROM microhook_runs
    WHERE microhook_runs.status <> sqlc.arg(excluded_status)
      AND microhook_runs.created_at_unix_nano < sqlc.arg(created_before_unix_nano)
);

-- name: DeleteRunsOverflow :execrows
DELETE FROM microhook_runs
WHERE id IN (
    SELECT id
    FROM microhook_runs
    WHERE microhook_runs.status <> sqlc.arg(excluded_status)
    ORDER BY microhook_runs.created_at_unix_nano DESC, microhook_runs.id DESC
    LIMIT -1 OFFSET sqlc.arg(max_runs)
);

-- name: SetMetadata :exec
INSERT INTO microhook_metadata (key, value)
VALUES (sqlc.arg(key), sqlc.arg(value))
ON CONFLICT(key) DO UPDATE SET value = excluded.value;

-- name: GetMetadata :one
SELECT value
FROM microhook_metadata
WHERE key = sqlc.arg(key)
LIMIT 1;
