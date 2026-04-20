package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"
)

const retentionLastPrunedAtKey = "retention_last_pruned_at_unix_nano"

const runSelectColumns = `
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
JOIN microhook_action_snapshots s ON s.run_id = r.id`

type metadataStore interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) CreateRun(ctx context.Context, params CreateRunParams) (Run, error) {
	if strings.TrimSpace(params.ID) == "" {
		return Run{}, fmt.Errorf("create run: %w: run id is required", ErrInvalidRunState)
	}
	if strings.TrimSpace(params.ActionName) == "" {
		return Run{}, fmt.Errorf("create run %q: %w: action name is required", params.ID, ErrInvalidRunState)
	}
	if err := validateRunStatus(params.Status); err != nil {
		return Run{}, fmt.Errorf("create run %q: %w", params.ID, err)
	}
	if err := validateActionSnapshot(params.ActionSnapshot); err != nil {
		return Run{}, fmt.Errorf("create run %q: %w", params.ID, err)
	}

	createdAt := params.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	createdAt, err := normalizeTime(createdAt)
	if err != nil {
		return Run{}, fmt.Errorf("create run %q: %w", params.ID, err)
	}

	startedAtValue, err := nullableUnixNano(params.StartedAt)
	if err != nil {
		return Run{}, fmt.Errorf("create run %q: %w", params.ID, err)
	}

	requestMetadata, err := normalizeRawJSON(params.RequestMetadata)
	if err != nil {
		return Run{}, fmt.Errorf("create run %q: request metadata: %w", params.ID, err)
	}

	commandJSON, envJSON, err := marshalActionSnapshot(params.ActionSnapshot)
	if err != nil {
		return Run{}, fmt.Errorf("create run %q: marshal action snapshot: %w", params.ID, err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, fmt.Errorf("create run %q: begin transaction: %w", params.ID, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	_, err = tx.ExecContext(ctx, `
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
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
		params.ID,
		params.ActionName,
		params.Status,
		createdAt.UnixNano(),
		startedAtValue,
		0,
		requestMetadata,
		params.StdoutTail,
		params.StderrTail,
		params.ErrorSummary,
	)
	if err != nil {
		return Run{}, fmt.Errorf("create run %q: insert run row: %w", params.ID, err)
	}

	_, err = tx.ExecContext(ctx, `
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
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
		params.ID,
		params.ActionSnapshot.Description,
		params.ActionSnapshot.Mode,
		commandJSON,
		params.ActionSnapshot.ShellCommand,
		params.ActionSnapshot.Cwd,
		int64(params.ActionSnapshot.Timeout),
		envJSON,
		params.ActionSnapshot.ConcurrencyPolicy,
		params.ActionSnapshot.MaxOutputBytes,
		boolToInt(params.ActionSnapshot.Enabled),
	)
	if err != nil {
		return Run{}, fmt.Errorf("create run %q: insert action snapshot: %w", params.ID, err)
	}

	if err := tx.Commit(); err != nil {
		return Run{}, fmt.Errorf("create run %q: commit transaction: %w", params.ID, err)
	}
	tx = nil

	return s.GetRun(ctx, params.ID)
}

func (s *Store) UpdateRun(ctx context.Context, params UpdateRunParams) error {
	if strings.TrimSpace(params.ID) == "" {
		return fmt.Errorf("update run: %w: run id is required", ErrInvalidRunState)
	}
	if err := validateRunStatus(params.Status); err != nil {
		return fmt.Errorf("update run %q: %w", params.ID, err)
	}

	startedAtValue, err := nullableUnixNano(params.StartedAt)
	if err != nil {
		return fmt.Errorf("update run %q: %w", params.ID, err)
	}
	finishedAtValue, err := nullableUnixNano(params.FinishedAt)
	if err != nil {
		return fmt.Errorf("update run %q: %w", params.ID, err)
	}

	result, err := s.db.ExecContext(ctx, `
UPDATE microhook_runs
SET
	status = ?,
	exit_code = ?,
	started_at_unix_nano = ?,
	finished_at_unix_nano = ?,
	timed_out = ?,
	stdout_tail = ?,
	stderr_tail = ?,
	error_summary = ?
WHERE id = ?
`,
		params.Status,
		nullableInt64(params.ExitCode),
		startedAtValue,
		finishedAtValue,
		boolToInt(params.TimedOut),
		params.StdoutTail,
		params.StderrTail,
		params.ErrorSummary,
		params.ID,
	)
	if err != nil {
		return fmt.Errorf("update run %q: %w", params.ID, err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update run %q: read affected rows: %w", params.ID, err)
	}
	if affected == 0 {
		return fmt.Errorf("update run %q: %w", params.ID, ErrRunNotFound)
	}

	return nil
}

func (s *Store) GetRun(ctx context.Context, id string) (Run, error) {
	if strings.TrimSpace(id) == "" {
		return Run{}, fmt.Errorf("get run: %w: run id is required", ErrInvalidRunState)
	}

	row := s.db.QueryRowContext(ctx, runSelectColumns+`
WHERE r.id = ?
`, id)

	run, err := scanRun(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, fmt.Errorf("get run %q: %w", id, ErrRunNotFound)
		}
		return Run{}, fmt.Errorf("get run %q: %w", id, err)
	}

	return run, nil
}

func (s *Store) ListRuns(ctx context.Context, filter RunFilter) ([]Run, error) {
	args := make([]any, 0, 2)
	conditions := make([]string, 0, 2)

	if actionName := strings.TrimSpace(filter.ActionName); actionName != "" {
		conditions = append(conditions, "r.action_name = ?")
		args = append(args, actionName)
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		if err := validateRunStatus(status); err != nil {
			return nil, fmt.Errorf("list runs: %w", err)
		}
		conditions = append(conditions, "r.status = ?")
		args = append(args, status)
	}

	var query strings.Builder
	query.WriteString(runSelectColumns)
	if len(conditions) > 0 {
		query.WriteString("\nWHERE ")
		query.WriteString(strings.Join(conditions, " AND "))
	}
	query.WriteString("\nORDER BY r.created_at_unix_nano DESC, r.id DESC")

	rows, err := s.db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer rows.Close()

	runs := make([]Run, 0)
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("list runs: %w", err)
		}
		runs = append(runs, run)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}

	return runs, nil
}

func (s *Store) EnqueueRun(ctx context.Context, params EnqueueRunParams) (QueuedRun, error) {
	if strings.TrimSpace(params.RunID) == "" {
		return QueuedRun{}, fmt.Errorf("enqueue run: %w: run id is required", ErrInvalidQueueRecord)
	}
	if strings.TrimSpace(params.ActionName) == "" {
		return QueuedRun{}, fmt.Errorf("enqueue run %q: %w: action name is required", params.RunID, ErrInvalidQueueRecord)
	}

	enqueuedAt := params.EnqueuedAt
	if enqueuedAt.IsZero() {
		enqueuedAt = time.Now().UTC()
	}
	enqueuedAt, err := normalizeTime(enqueuedAt)
	if err != nil {
		return QueuedRun{}, fmt.Errorf("enqueue run %q: %w", params.RunID, err)
	}

	inputJSON, err := normalizeRawJSON(params.Input)
	if err != nil {
		return QueuedRun{}, fmt.Errorf("enqueue run %q: input payload: %w", params.RunID, err)
	}

	_, err = s.db.ExecContext(ctx, `
INSERT INTO microhook_queued_runs (
	run_id,
	action_name,
	enqueued_at_unix_nano,
	input_json
) VALUES (?, ?, ?, ?)
`, params.RunID, params.ActionName, enqueuedAt.UnixNano(), inputJSON)
	if err != nil {
		return QueuedRun{}, fmt.Errorf("enqueue run %q: %w", params.RunID, err)
	}

	return s.GetQueuedRun(ctx, params.RunID)
}

func (s *Store) GetQueuedRun(ctx context.Context, runID string) (QueuedRun, error) {
	if strings.TrimSpace(runID) == "" {
		return QueuedRun{}, fmt.Errorf("get queued run: %w: run id is required", ErrInvalidQueueRecord)
	}

	row := s.db.QueryRowContext(ctx, `
SELECT queue_sequence, run_id, action_name, enqueued_at_unix_nano, input_json
FROM microhook_queued_runs
WHERE run_id = ?
`, runID)

	queuedRun, err := scanQueuedRun(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return QueuedRun{}, fmt.Errorf("get queued run %q: %w", runID, ErrQueuedRunNotFound)
		}
		return QueuedRun{}, fmt.Errorf("get queued run %q: %w", runID, err)
	}

	return queuedRun, nil
}

func (s *Store) ListQueuedRuns(ctx context.Context, actionName string) ([]QueuedRun, error) {
	args := make([]any, 0, 1)
	query := `
SELECT queue_sequence, run_id, action_name, enqueued_at_unix_nano, input_json
FROM microhook_queued_runs`

	if actionName = strings.TrimSpace(actionName); actionName != "" {
		query += "\nWHERE action_name = ?"
		args = append(args, actionName)
	}

	query += "\nORDER BY action_name ASC, queue_sequence ASC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list queued runs: %w", err)
	}
	defer rows.Close()

	queuedRuns := make([]QueuedRun, 0)
	for rows.Next() {
		queuedRun, err := scanQueuedRun(rows)
		if err != nil {
			return nil, fmt.Errorf("list queued runs: %w", err)
		}
		queuedRuns = append(queuedRuns, queuedRun)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list queued runs: %w", err)
	}

	return queuedRuns, nil
}

func (s *Store) DeleteQueuedRun(ctx context.Context, runID string) error {
	if strings.TrimSpace(runID) == "" {
		return fmt.Errorf("delete queued run: %w: run id is required", ErrInvalidQueueRecord)
	}

	result, err := s.db.ExecContext(ctx, `DELETE FROM microhook_queued_runs WHERE run_id = ?`, runID)
	if err != nil {
		return fmt.Errorf("delete queued run %q: %w", runID, err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete queued run %q: read affected rows: %w", runID, err)
	}
	if affected == 0 {
		return fmt.Errorf("delete queued run %q: %w", runID, ErrQueuedRunNotFound)
	}

	return nil
}

func (s *Store) ApplyRetention(ctx context.Context, policy RetentionPolicy) (RetentionResult, error) {
	if policy.MaxAge < 0 || policy.MaxRuns < 0 {
		return RetentionResult{}, fmt.Errorf("apply retention: %w", ErrInvalidRetention)
	}

	prunedAt := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RetentionResult{}, fmt.Errorf("apply retention: begin transaction: %w", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	deletedRuns := int64(0)
	if policy.MaxAge > 0 {
		result, err := tx.ExecContext(ctx, `
DELETE FROM microhook_runs
WHERE id IN (
	SELECT id
	FROM microhook_runs
	WHERE status NOT IN (?, ?)
	AND created_at_unix_nano < ?
)
`, RunStatusQueued, RunStatusRunning, prunedAt.Add(-policy.MaxAge).UnixNano())
		if err != nil {
			return RetentionResult{}, fmt.Errorf("apply retention: prune by age: %w", err)
		}

		affected, err := result.RowsAffected()
		if err != nil {
			return RetentionResult{}, fmt.Errorf("apply retention: prune by age affected rows: %w", err)
		}
		deletedRuns += affected
	}

	if policy.MaxRuns > 0 {
		result, err := tx.ExecContext(ctx, `
DELETE FROM microhook_runs
WHERE id IN (
	SELECT id
	FROM microhook_runs
	WHERE status NOT IN (?, ?)
	ORDER BY created_at_unix_nano DESC, id DESC
	LIMIT -1 OFFSET ?
)
`, RunStatusQueued, RunStatusRunning, policy.MaxRuns)
		if err != nil {
			return RetentionResult{}, fmt.Errorf("apply retention: prune by max runs: %w", err)
		}

		affected, err := result.RowsAffected()
		if err != nil {
			return RetentionResult{}, fmt.Errorf("apply retention: prune by max runs affected rows: %w", err)
		}
		deletedRuns += affected
	}

	if err := setMetadata(ctx, tx, retentionLastPrunedAtKey, strconv.FormatInt(prunedAt.UnixNano(), 10)); err != nil {
		return RetentionResult{}, fmt.Errorf("apply retention: set retention metadata: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return RetentionResult{}, fmt.Errorf("apply retention: commit transaction: %w", err)
	}
	tx = nil

	return RetentionResult{DeletedRuns: int(deletedRuns), PrunedAt: prunedAt}, nil
}

func (s *Store) LastRetentionPruneAt(ctx context.Context) (*time.Time, error) {
	value, ok, err := getMetadata(ctx, s.db, retentionLastPrunedAtKey)
	if err != nil {
		return nil, fmt.Errorf("get last retention prune time: %w", err)
	}
	if !ok {
		return nil, nil
	}

	unixNano, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("get last retention prune time: parse metadata %q: %w", value, err)
	}

	prunedAt := time.Unix(0, unixNano).UTC()
	return &prunedAt, nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanRun(scanner rowScanner) (Run, error) {
	var (
		run                 Run
		exitCode            sql.NullInt64
		startedAtUnixNano   sql.NullInt64
		finishedAtUnixNano  sql.NullInt64
		requestMetadataJSON []byte
		commandJSON         []byte
		envJSON             []byte
		timedOut            int
		enabled             int
		createdAtUnixNano   int64
		timeoutNanoseconds  int64
	)

	err := scanner.Scan(
		&run.ID,
		&run.ActionName,
		&run.Status,
		&exitCode,
		&createdAtUnixNano,
		&startedAtUnixNano,
		&finishedAtUnixNano,
		&timedOut,
		&requestMetadataJSON,
		&run.StdoutTail,
		&run.StderrTail,
		&run.ErrorSummary,
		&run.ActionSnapshot.Description,
		&run.ActionSnapshot.Mode,
		&commandJSON,
		&run.ActionSnapshot.ShellCommand,
		&run.ActionSnapshot.Cwd,
		&timeoutNanoseconds,
		&envJSON,
		&run.ActionSnapshot.ConcurrencyPolicy,
		&run.ActionSnapshot.MaxOutputBytes,
		&enabled,
	)
	if err != nil {
		return Run{}, err
	}

	run.CreatedAt = time.Unix(0, createdAtUnixNano).UTC()
	run.TimedOut = timedOut != 0
	run.ActionSnapshot.Timeout = time.Duration(timeoutNanoseconds)
	run.ActionSnapshot.Enabled = enabled != 0
	run.RequestMetadata = cloneBytes(requestMetadataJSON)

	if exitCode.Valid {
		exitCodeValue := int(exitCode.Int64)
		run.ExitCode = &exitCodeValue
	}
	if startedAtUnixNano.Valid {
		startedAt := time.Unix(0, startedAtUnixNano.Int64).UTC()
		run.StartedAt = &startedAt
	}
	if finishedAtUnixNano.Valid {
		finishedAt := time.Unix(0, finishedAtUnixNano.Int64).UTC()
		run.FinishedAt = &finishedAt
	}

	if err := json.Unmarshal(commandJSON, &run.ActionSnapshot.Command); err != nil {
		return Run{}, fmt.Errorf("decode action snapshot command: %w", err)
	}
	if err := json.Unmarshal(envJSON, &run.ActionSnapshot.Env); err != nil {
		return Run{}, fmt.Errorf("decode action snapshot env: %w", err)
	}

	return run, nil
}

func scanQueuedRun(scanner rowScanner) (QueuedRun, error) {
	var (
		queuedRun          QueuedRun
		enqueuedAtUnixNano int64
		inputJSON          []byte
	)

	err := scanner.Scan(
		&queuedRun.Sequence,
		&queuedRun.RunID,
		&queuedRun.ActionName,
		&enqueuedAtUnixNano,
		&inputJSON,
	)
	if err != nil {
		return QueuedRun{}, err
	}

	queuedRun.EnqueuedAt = time.Unix(0, enqueuedAtUnixNano).UTC()
	queuedRun.Input = cloneBytes(inputJSON)
	return queuedRun, nil
}

func validateRunStatus(status string) error {
	switch strings.TrimSpace(status) {
	case RunStatusQueued, RunStatusRunning, RunStatusSucceeded, RunStatusFailed, RunStatusTimedOut, RunStatusCancelled:
		return nil
	default:
		return fmt.Errorf("%w: unsupported run status %q", ErrInvalidRunState, status)
	}
}

func validateActionSnapshot(snapshot ActionSnapshot) error {
	if strings.TrimSpace(snapshot.Mode) == "" {
		return fmt.Errorf("%w: action snapshot mode is required", ErrInvalidRunState)
	}
	if strings.TrimSpace(snapshot.ConcurrencyPolicy) == "" {
		return fmt.Errorf("%w: action snapshot concurrency policy is required", ErrInvalidRunState)
	}
	if snapshot.MaxOutputBytes < 0 {
		return fmt.Errorf("%w: action snapshot max output bytes must be greater than or equal to 0", ErrInvalidRunState)
	}

	return nil
}

func marshalActionSnapshot(snapshot ActionSnapshot) ([]byte, []byte, error) {
	commandJSON, err := json.Marshal(cloneStrings(snapshot.Command))
	if err != nil {
		return nil, nil, err
	}
	envJSON, err := json.Marshal(cloneStringMap(snapshot.Env))
	if err != nil {
		return nil, nil, err
	}

	return commandJSON, envJSON, nil
}

func normalizeRawJSON(data json.RawMessage) ([]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("must be valid JSON")
	}

	return cloneBytes(data), nil
}

func normalizeTime(value time.Time) (time.Time, error) {
	if value.IsZero() {
		return time.Time{}, fmt.Errorf("timestamp must not be zero")
	}

	return value.UTC(), nil
}

func nullableUnixNano(value *time.Time) (any, error) {
	if value == nil {
		return nil, nil
	}

	normalized, err := normalizeTime(*value)
	if err != nil {
		return nil, err
	}

	return normalized.UnixNano(), nil
}

func nullableInt64(value *int) any {
	if value == nil {
		return nil
	}

	return int64(*value)
}

func boolToInt(value bool) int {
	if value {
		return 1
	}

	return 0
}

func cloneBytes(value []byte) []byte {
	if len(value) == 0 {
		return nil
	}

	cloned := make([]byte, len(value))
	copy(cloned, value)
	return cloned
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}

	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return map[string]string{}
	}

	cloned := make(map[string]string, len(values))
	maps.Copy(cloned, values)

	return cloned
}

func setMetadata(ctx context.Context, store metadataStore, key, value string) error {
	_, err := store.ExecContext(ctx, `
INSERT INTO microhook_metadata (key, value)
VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value
`, key, value)
	if err != nil {
		return err
	}

	return nil
}

func getMetadata(ctx context.Context, store metadataStore, key string) (string, bool, error) {
	var value string
	err := store.QueryRowContext(ctx, `SELECT value FROM microhook_metadata WHERE key = ?`, key).Scan(&value)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}

	return value, true, nil
}
