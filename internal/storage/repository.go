// Package storage provides the Store struct
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

	storedb "github.com/nicotsx/microhook/internal/storage/sqlc"
)

const retentionLastPrunedAtKey = "retention_last_pruned_at_unix_nano"

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

	queries := s.queries.WithTx(tx)
	if err := queries.CreateRun(ctx, storedb.CreateRunParams{
		ID:                  params.ID,
		ActionName:          params.ActionName,
		Status:              params.Status,
		CreatedAtUnixNano:   createdAt.UnixNano(),
		StartedAtUnixNano:   startedAtValue,
		TimedOut:            0,
		RequestMetadataJson: requestMetadata,
		StdoutTail:          params.StdoutTail,
		StderrTail:          params.StderrTail,
		ErrorSummary:        params.ErrorSummary,
	}); err != nil {
		return Run{}, fmt.Errorf("create run %q: insert run row: %w", params.ID, err)
	}

	if err := queries.CreateActionSnapshot(ctx, storedb.CreateActionSnapshotParams{
		RunID:              params.ID,
		Description:        params.ActionSnapshot.Description,
		Mode:               params.ActionSnapshot.Mode,
		CommandJson:        commandJSON,
		ShellCommand:       params.ActionSnapshot.ShellCommand,
		Cwd:                params.ActionSnapshot.Cwd,
		TimeoutNanoseconds: int64(params.ActionSnapshot.Timeout),
		EnvJson:            envJSON,
		ConcurrencyPolicy:  params.ActionSnapshot.ConcurrencyPolicy,
		MaxOutputBytes:     int64(params.ActionSnapshot.MaxOutputBytes),
		Enabled:            boolToInt64(params.ActionSnapshot.Enabled),
	}); err != nil {
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

	affected, err := s.queries.UpdateRun(ctx, storedb.UpdateRunParams{
		Status:             params.Status,
		ExitCode:           nullableInt64(params.ExitCode),
		StartedAtUnixNano:  startedAtValue,
		FinishedAtUnixNano: finishedAtValue,
		TimedOut:           boolToInt64(params.TimedOut),
		StdoutTail:         params.StdoutTail,
		StderrTail:         params.StderrTail,
		ErrorSummary:       params.ErrorSummary,
		ID:                 params.ID,
	})
	if err != nil {
		return fmt.Errorf("update run %q: %w", params.ID, err)
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

	record, err := s.queries.GetRun(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, fmt.Errorf("get run %q: %w", id, ErrRunNotFound)
		}
		return Run{}, fmt.Errorf("get run %q: %w", id, err)
	}

	run, err := runFromRecord(record)
	if err != nil {
		return Run{}, fmt.Errorf("get run %q: %w", id, err)
	}

	return run, nil
}

func (s *Store) ListRuns(ctx context.Context, filter RunFilter) ([]Run, error) {
	actionName := strings.TrimSpace(filter.ActionName)
	status := strings.TrimSpace(filter.Status)
	if status != "" {
		if err := validateRunStatus(status); err != nil {
			return nil, fmt.Errorf("list runs: %w", err)
		}
	}

	var (
		records []storedb.MicrohookRunRecord
		err     error
	)

	switch {
	case actionName != "" && status != "":
		records, err = s.queries.ListRunsByActionNameAndStatus(ctx, storedb.ListRunsByActionNameAndStatusParams{
			FilterActionName: actionName,
			FilterStatus:     status,
		})
	case actionName != "":
		records, err = s.queries.ListRunsByActionName(ctx, actionName)
	case status != "":
		records, err = s.queries.ListRunsByStatus(ctx, status)
	default:
		records, err = s.queries.ListRuns(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}

	runs, err := runsFromRecords(records)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}

	return runs, nil
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

	queries := s.queries.WithTx(tx)
	deletedRuns := int64(0)
	if policy.MaxAge > 0 {
		affected, err := queries.DeleteRunsOlderThan(ctx, storedb.DeleteRunsOlderThanParams{
			ExcludedStatus:        RunStatusRunning,
			CreatedBeforeUnixNano: prunedAt.Add(-policy.MaxAge).UnixNano(),
		})
		if err != nil {
			return RetentionResult{}, fmt.Errorf("apply retention: prune by age: %w", err)
		}
		deletedRuns += affected
	}

	if policy.MaxRuns > 0 {
		affected, err := queries.DeleteRunsOverflow(ctx, storedb.DeleteRunsOverflowParams{
			ExcludedStatus: RunStatusRunning,
			MaxRuns:        int64(policy.MaxRuns),
		})
		if err != nil {
			return RetentionResult{}, fmt.Errorf("apply retention: prune by max runs: %w", err)
		}
		deletedRuns += affected
	}

	if err := setMetadata(ctx, queries, retentionLastPrunedAtKey, strconv.FormatInt(prunedAt.UnixNano(), 10)); err != nil {
		return RetentionResult{}, fmt.Errorf("apply retention: set retention metadata: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return RetentionResult{}, fmt.Errorf("apply retention: commit transaction: %w", err)
	}
	tx = nil

	return RetentionResult{DeletedRuns: int(deletedRuns), PrunedAt: prunedAt}, nil
}

func (s *Store) LastRetentionPruneAt(ctx context.Context) (*time.Time, error) {
	value, ok, err := getMetadata(ctx, s.queries, retentionLastPrunedAtKey)
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

func runFromRecord(record storedb.MicrohookRunRecord) (Run, error) {
	run := Run{
		ID:              record.ID,
		ActionName:      record.ActionName,
		Status:          record.Status,
		CreatedAt:       time.Unix(0, record.CreatedAtUnixNano).UTC(),
		TimedOut:        record.TimedOut != 0,
		RequestMetadata: cloneBytes(record.RequestMetadataJson),
		StdoutTail:      record.StdoutTail,
		StderrTail:      record.StderrTail,
		ErrorSummary:    record.ErrorSummary,
		ActionSnapshot: ActionSnapshot{
			Description:       record.Description,
			Mode:              record.Mode,
			ShellCommand:      record.ShellCommand,
			Cwd:               record.Cwd,
			Timeout:           time.Duration(record.TimeoutNanoseconds),
			ConcurrencyPolicy: record.ConcurrencyPolicy,
			MaxOutputBytes:    int(record.MaxOutputBytes),
			Enabled:           record.Enabled != 0,
		},
	}

	if record.ExitCode.Valid {
		exitCode := int(record.ExitCode.Int64)
		run.ExitCode = &exitCode
	}
	if record.StartedAtUnixNano.Valid {
		startedAt := time.Unix(0, record.StartedAtUnixNano.Int64).UTC()
		run.StartedAt = &startedAt
	}
	if record.FinishedAtUnixNano.Valid {
		finishedAt := time.Unix(0, record.FinishedAtUnixNano.Int64).UTC()
		run.FinishedAt = &finishedAt
	}

	if err := json.Unmarshal(record.CommandJson, &run.ActionSnapshot.Command); err != nil {
		return Run{}, fmt.Errorf("decode action snapshot command: %w", err)
	}
	if err := json.Unmarshal(record.EnvJson, &run.ActionSnapshot.Env); err != nil {
		return Run{}, fmt.Errorf("decode action snapshot env: %w", err)
	}

	return run, nil
}

func runsFromRecords(records []storedb.MicrohookRunRecord) ([]Run, error) {
	runs := make([]Run, 0, len(records))
	for _, record := range records {
		run, err := runFromRecord(record)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}

	return runs, nil
}

func validateRunStatus(status string) error {
	switch strings.TrimSpace(status) {
	case RunStatusRunning, RunStatusSucceeded, RunStatusFailed, RunStatusTimedOut, RunStatusCancelled:
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

func nullableUnixNano(value *time.Time) (sql.NullInt64, error) {
	if value == nil {
		return sql.NullInt64{}, nil
	}

	normalized, err := normalizeTime(*value)
	if err != nil {
		return sql.NullInt64{}, err
	}

	return sql.NullInt64{Int64: normalized.UnixNano(), Valid: true}, nil
}

func nullableInt64(value *int) sql.NullInt64 {
	if value == nil {
		return sql.NullInt64{}
	}

	return sql.NullInt64{Int64: int64(*value), Valid: true}
}

func boolToInt64(value bool) int64 {
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

func setMetadata(ctx context.Context, queries *storedb.Queries, key, value string) error {
	if err := queries.SetMetadata(ctx, storedb.SetMetadataParams{Key: key, Value: value}); err != nil {
		return err
	}

	return nil
}

func getMetadata(ctx context.Context, queries *storedb.Queries, key string) (string, bool, error) {
	value, err := queries.GetMetadata(ctx, key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}

	return value, true, nil
}
