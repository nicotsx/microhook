package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestStorePersistsRunsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	storagePath := filepath.Join(t.TempDir(), "microhook.db")

	store := openStore(t, storagePath)
	createdAt := time.Date(2026, time.April, 21, 10, 15, 0, 0, time.UTC)
	startedAt := createdAt.Add(2 * time.Second)
	finishedAt := startedAt.Add(3 * time.Second)

	_, err := store.CreateRun(ctx, CreateRunParams{
		ID:              "run-success",
		ActionName:      "deploy",
		Status:          RunStatusRunning,
		CreatedAt:       createdAt,
		StartedAt:       &startedAt,
		RequestMetadata: json.RawMessage(`{"request_id":"abc123","mode":"sync"}`),
		ActionSnapshot:  testActionSnapshot(),
	})
	if err != nil {
		t.Fatalf("create running run: %v", err)
	}

	exitCode := 0
	if err := store.UpdateRun(ctx, UpdateRunParams{
		ID:         "run-success",
		Status:     RunStatusSucceeded,
		ExitCode:   &exitCode,
		StartedAt:  &startedAt,
		FinishedAt: &finishedAt,
		StdoutTail: "deploy complete",
	}); err != nil {
		t.Fatalf("update run: %v", err)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("close store before reopen: %v", err)
	}

	store = openStore(t, storagePath)

	run, err := store.GetRun(ctx, "run-success")
	if err != nil {
		t.Fatalf("get persisted run: %v", err)
	}

	assertRunMatches(t, run, Run{
		ID:              "run-success",
		ActionName:      "deploy",
		Status:          RunStatusSucceeded,
		ExitCode:        &exitCode,
		CreatedAt:       createdAt,
		StartedAt:       &startedAt,
		FinishedAt:      &finishedAt,
		RequestMetadata: json.RawMessage(`{"request_id":"abc123","mode":"sync"}`),
		StdoutTail:      "deploy complete",
		ActionSnapshot:  testActionSnapshot(),
	})
}

func TestStoreListRunsSupportsFilters(t *testing.T) {
	ctx := context.Background()
	store := openStore(t, filepath.Join(t.TempDir(), "microhook.db"))

	for _, run := range []CreateRunParams{
		{
			ID:             "run-1",
			ActionName:     "deploy",
			Status:         RunStatusSucceeded,
			CreatedAt:      time.Date(2026, time.April, 21, 10, 15, 0, 0, time.UTC),
			ActionSnapshot: testActionSnapshot(),
		},
		{
			ID:             "run-2",
			ActionName:     "deploy",
			Status:         RunStatusFailed,
			CreatedAt:      time.Date(2026, time.April, 21, 10, 16, 0, 0, time.UTC),
			ActionSnapshot: testActionSnapshot(),
		},
		{
			ID:             "run-3",
			ActionName:     "backup",
			Status:         RunStatusRunning,
			CreatedAt:      time.Date(2026, time.April, 21, 10, 17, 0, 0, time.UTC),
			ActionSnapshot: testActionSnapshot(),
		},
	} {
		if _, err := store.CreateRun(ctx, run); err != nil {
			t.Fatalf("create run %q: %v", run.ID, err)
		}
	}

	deployRuns, err := store.ListRuns(ctx, RunFilter{ActionName: "deploy"})
	if err != nil {
		t.Fatalf("list runs by action: %v", err)
	}
	if len(deployRuns) != 2 {
		t.Fatalf("expected 2 deploy runs, got %d", len(deployRuns))
	}
	if deployRuns[0].ID != "run-2" || deployRuns[1].ID != "run-1" {
		t.Fatalf("expected deploy runs ordered newest-first, got %q then %q", deployRuns[0].ID, deployRuns[1].ID)
	}
	if deployRuns[0].ActionSnapshot.Mode == "" {
		t.Fatal("expected list runs to include action snapshot data")
	}

	runningRuns, err := store.ListRuns(ctx, RunFilter{Status: RunStatusRunning})
	if err != nil {
		t.Fatalf("list runs by status: %v", err)
	}
	if len(runningRuns) != 1 {
		t.Fatalf("expected 1 running run, got %d", len(runningRuns))
	}
	if runningRuns[0].ID != "run-3" {
		t.Fatalf("expected running run id %q, got %q", "run-3", runningRuns[0].ID)
	}
}

func TestStoreApplyRetentionPrunesTerminalRunsByAgeAndCount(t *testing.T) {
	ctx := context.Background()
	store := openStore(t, filepath.Join(t.TempDir(), "microhook.db"))
	now := time.Date(2026, time.April, 21, 12, 0, 0, 0, time.UTC)

	for _, run := range []CreateRunParams{
		{
			ID:             "old-success",
			ActionName:     "deploy",
			Status:         RunStatusSucceeded,
			CreatedAt:      now.Add(-72 * time.Hour),
			ActionSnapshot: testActionSnapshot(),
		},
		{
			ID:             "recent-success",
			ActionName:     "deploy",
			Status:         RunStatusSucceeded,
			CreatedAt:      now.Add(-3 * time.Hour),
			ActionSnapshot: testActionSnapshot(),
		},
		{
			ID:             "recent-failed",
			ActionName:     "deploy",
			Status:         RunStatusFailed,
			CreatedAt:      now.Add(-2 * time.Hour),
			ActionSnapshot: testActionSnapshot(),
		},
		{
			ID:             "still-running",
			ActionName:     "deploy",
			Status:         RunStatusRunning,
			CreatedAt:      now.Add(-90 * time.Minute),
			ActionSnapshot: testActionSnapshot(),
		},
	} {
		if _, err := store.CreateRun(ctx, run); err != nil {
			t.Fatalf("create run %q: %v", run.ID, err)
		}
	}

	result, err := store.ApplyRetention(ctx, RetentionPolicy{MaxAge: 48 * time.Hour, MaxRuns: 1})
	if err != nil {
		t.Fatalf("apply retention: %v", err)
	}
	if result.DeletedRuns != 2 {
		t.Fatalf("expected 2 deleted runs, got %d", result.DeletedRuns)
	}
	if result.PrunedAt.IsZero() {
		t.Fatal("expected prune result to include prune timestamp")
	}

	for _, missingID := range []string{"old-success", "recent-success"} {
		_, err := store.GetRun(ctx, missingID)
		if !errors.Is(err, ErrRunNotFound) {
			t.Fatalf("expected %q to be pruned, got %v", missingID, err)
		}
	}

	for _, keptID := range []string{"recent-failed", "still-running"} {
		if _, err := store.GetRun(ctx, keptID); err != nil {
			t.Fatalf("expected %q to remain after retention, got %v", keptID, err)
		}
	}

	prunedAt, err := store.LastRetentionPruneAt(ctx)
	if err != nil {
		t.Fatalf("get last retention prune time: %v", err)
	}
	if prunedAt == nil || prunedAt.IsZero() {
		t.Fatal("expected retention metadata to be stored")
	}
}

func TestOpenBootstrapsMissingTablesInExistingDatabase(t *testing.T) {
	ctx := context.Background()
	storagePath := filepath.Join(t.TempDir(), "microhook.db")
	db, err := sql.Open("sqlite", sqliteDSN(storagePath))
	if err != nil {
		t.Fatalf("open raw sqlite db: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
CREATE TABLE microhook_metadata (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
)
`); err != nil {
		t.Fatalf("create existing metadata table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw sqlite db: %v", err)
	}

	store := openStore(t, storagePath)
	if _, err := store.CreateRun(ctx, CreateRunParams{
		ID:             "run-after-bootstrap",
		ActionName:     "deploy",
		Status:         RunStatusRunning,
		CreatedAt:      time.Date(2026, time.April, 21, 10, 0, 0, 0, time.UTC),
		ActionSnapshot: testActionSnapshot(),
	}); err != nil {
		t.Fatalf("create run after bootstrap: %v", err)
	}
}

func openStore(t *testing.T, path string) *Store {
	t.Helper()

	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})

	return store
}

func testActionSnapshot() ActionSnapshot {
	return ActionSnapshot{
		Description:       "Deploy the stack",
		Mode:              "command",
		Command:           []string{"docker", "compose", "up", "-d"},
		Cwd:               "/srv/app",
		Timeout:           2 * time.Minute,
		Env:               map[string]string{"ENVIRONMENT": "production"},
		ConcurrencyPolicy: "allow",
		MaxOutputBytes:    4096,
		Enabled:           true,
	}
}

func assertRunMatches(t *testing.T, got, want Run) {
	t.Helper()

	if got.ID != want.ID {
		t.Fatalf("expected run id %q, got %q", want.ID, got.ID)
	}
	if got.ActionName != want.ActionName {
		t.Fatalf("expected action %q, got %q", want.ActionName, got.ActionName)
	}
	if got.Status != want.Status {
		t.Fatalf("expected status %q, got %q", want.Status, got.Status)
	}
	if got.ExitCode == nil || want.ExitCode == nil || *got.ExitCode != *want.ExitCode {
		t.Fatalf("expected exit code %v, got %v", want.ExitCode, got.ExitCode)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("expected created at %s, got %s", want.CreatedAt, got.CreatedAt)
	}
	if got.StartedAt == nil || want.StartedAt == nil || !got.StartedAt.Equal(*want.StartedAt) {
		t.Fatalf("expected started at %v, got %v", want.StartedAt, got.StartedAt)
	}
	if got.FinishedAt == nil || want.FinishedAt == nil || !got.FinishedAt.Equal(*want.FinishedAt) {
		t.Fatalf("expected finished at %v, got %v", want.FinishedAt, got.FinishedAt)
	}
	if string(got.RequestMetadata) != string(want.RequestMetadata) {
		t.Fatalf("expected request metadata %q, got %q", string(want.RequestMetadata), string(got.RequestMetadata))
	}
	if got.StdoutTail != want.StdoutTail {
		t.Fatalf("expected stdout tail %q, got %q", want.StdoutTail, got.StdoutTail)
	}
	if got.ActionSnapshot.Description != want.ActionSnapshot.Description {
		t.Fatalf("expected action description %q, got %q", want.ActionSnapshot.Description, got.ActionSnapshot.Description)
	}
	if got.ActionSnapshot.Mode != want.ActionSnapshot.Mode {
		t.Fatalf("expected action mode %q, got %q", want.ActionSnapshot.Mode, got.ActionSnapshot.Mode)
	}
	if got.ActionSnapshot.Cwd != want.ActionSnapshot.Cwd {
		t.Fatalf("expected action cwd %q, got %q", want.ActionSnapshot.Cwd, got.ActionSnapshot.Cwd)
	}
	if got.ActionSnapshot.Timeout != want.ActionSnapshot.Timeout {
		t.Fatalf("expected action timeout %s, got %s", want.ActionSnapshot.Timeout, got.ActionSnapshot.Timeout)
	}
	if got.ActionSnapshot.ConcurrencyPolicy != want.ActionSnapshot.ConcurrencyPolicy {
		t.Fatalf("expected concurrency policy %q, got %q", want.ActionSnapshot.ConcurrencyPolicy, got.ActionSnapshot.ConcurrencyPolicy)
	}
	if got.ActionSnapshot.MaxOutputBytes != want.ActionSnapshot.MaxOutputBytes {
		t.Fatalf("expected max output bytes %d, got %d", want.ActionSnapshot.MaxOutputBytes, got.ActionSnapshot.MaxOutputBytes)
	}
	if got.ActionSnapshot.Enabled != want.ActionSnapshot.Enabled {
		t.Fatalf("expected enabled %t, got %t", want.ActionSnapshot.Enabled, got.ActionSnapshot.Enabled)
	}
	if len(got.ActionSnapshot.Command) != len(want.ActionSnapshot.Command) {
		t.Fatalf("expected %d command args, got %d", len(want.ActionSnapshot.Command), len(got.ActionSnapshot.Command))
	}
	for i := range want.ActionSnapshot.Command {
		if got.ActionSnapshot.Command[i] != want.ActionSnapshot.Command[i] {
			t.Fatalf("expected command[%d] %q, got %q", i, want.ActionSnapshot.Command[i], got.ActionSnapshot.Command[i])
		}
	}
	if len(got.ActionSnapshot.Env) != len(want.ActionSnapshot.Env) {
		t.Fatalf("expected env size %d, got %d", len(want.ActionSnapshot.Env), len(got.ActionSnapshot.Env))
	}
	for key, value := range want.ActionSnapshot.Env {
		if got.ActionSnapshot.Env[key] != value {
			t.Fatalf("expected env[%q] %q, got %q", key, value, got.ActionSnapshot.Env[key])
		}
	}
}
