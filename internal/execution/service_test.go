package execution

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicotsx/microhook/internal/config"
	"github.com/nicotsx/microhook/internal/storage"
)

func TestServiceInvokeCommandModePassesInputAndMetadata(t *testing.T) {
	t.Setenv("LEAK_ME", "top-secret")

	ctx := context.Background()
	inputPath := filepath.Join(t.TempDir(), "input.json")
	envPath := filepath.Join(t.TempDir(), "env.txt")
	requestMetadata := json.RawMessage(`{"mode":"sync","request_id":"req-123"}`)
	input := json.RawMessage("{\n  \"reason\": \"backup-start\",\n  \"request_id\": \"req-123\"\n}\n")

	service, store := newTestService(t, []config.Action{commandAction(
		"inspect",
		[]string{"/bin/sh", "-c", "cat > \"$INPUT_FILE\"; env | sort > \"$ENV_FILE\"; printf stdout-data; printf stderr-data >&2"},
		map[string]string{
			"INPUT_FILE": inputPath,
			"ENV_FILE":   envPath,
			"FIXED_ENV":  "configured",
		},
		200,
	)})

	service.newRunID = func() (string, error) { return "run-fixed", nil }

	run, err := service.Invoke(ctx, InvokeParams{
		ActionName:      "inspect",
		Input:           input,
		RequestMetadata: requestMetadata,
		RequestID:       "req-123",
	})
	if err != nil {
		t.Fatalf("invoke action: %v", err)
	}

	if run.Status != storage.RunStatusSucceeded {
		t.Fatalf("expected status %q, got %q", storage.RunStatusSucceeded, run.Status)
	}
	if run.ExitCode == nil || *run.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %v", run.ExitCode)
	}
	if string(run.RequestMetadata) != string(requestMetadata) {
		t.Fatalf("expected request metadata %q, got %q", string(requestMetadata), string(run.RequestMetadata))
	}
	if run.StdoutTail != "stdout-data" {
		t.Fatalf("expected stdout tail %q, got %q", "stdout-data", run.StdoutTail)
	}
	if run.StderrTail != "stderr-data" {
		t.Fatalf("expected stderr tail %q, got %q", "stderr-data", run.StderrTail)
	}
	if run.StartedAt == nil || run.FinishedAt == nil {
		t.Fatal("expected run timestamps to be recorded")
	}

	storedRun, err := store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("get stored run: %v", err)
	}
	if storedRun.Status != storage.RunStatusSucceeded {
		t.Fatalf("expected stored run status %q, got %q", storage.RunStatusSucceeded, storedRun.Status)
	}

	inputBytes, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatalf("read captured stdin: %v", err)
	}
	if string(inputBytes) != string(input) {
		t.Fatalf("expected stdin %q, got %q", string(input), string(inputBytes))
	}

	envBytes, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read captured env: %v", err)
	}
	envText := string(envBytes)
	for _, expected := range []string{
		"FIXED_ENV=configured",
		"MICROHOOK_ACTION=inspect",
		"MICROHOOK_REQUEST_ID=req-123",
		"MICROHOOK_RUN_ID=run-fixed",
		"PATH=",
	} {
		if !strings.Contains(envText, expected) {
			t.Fatalf("expected env to contain %q, got %q", expected, envText)
		}
	}
	if strings.Contains(envText, "LEAK_ME=top-secret") {
		t.Fatalf("expected child environment not to inherit process env, got %q", envText)
	}
}

func TestServiceInvokeShellModeCapturesBoundedOutputAndExitCode(t *testing.T) {
	service, _ := newTestService(t, []config.Action{shellAction(
		"shell-output",
		"printf abcdefghij; printf 1234567890 >&2; exit 7",
		4,
	)})

	run, err := service.Invoke(context.Background(), InvokeParams{ActionName: "shell-output"})
	if err != nil {
		t.Fatalf("invoke shell action: %v", err)
	}

	if run.Status != storage.RunStatusFailed {
		t.Fatalf("expected status %q, got %q", storage.RunStatusFailed, run.Status)
	}
	if run.ExitCode == nil || *run.ExitCode != 7 {
		t.Fatalf("expected exit code 7, got %v", run.ExitCode)
	}
	if run.StdoutTail != "ghij" {
		t.Fatalf("expected stdout tail %q, got %q", "ghij", run.StdoutTail)
	}
	if run.StderrTail != "7890" {
		t.Fatalf("expected stderr tail %q, got %q", "7890", run.StderrTail)
	}
	if run.ErrorSummary != "process exited with code 7" {
		t.Fatalf("expected error summary %q, got %q", "process exited with code 7", run.ErrorSummary)
	}
	if run.ActionSnapshot.Mode != config.ActionModeShell {
		t.Fatalf("expected action mode %q, got %q", config.ActionModeShell, run.ActionSnapshot.Mode)
	}
}

func TestServiceInvokeMarksTimedOutRuns(t *testing.T) {
	service, _ := newTestService(t, []config.Action{{
		Name:              "slow",
		Mode:              config.ActionModeCommand,
		Command:           []string{"/bin/sh", "-c", "printf start; sleep 1; printf end"},
		Timeout:           50 * time.Millisecond,
		ConcurrencyPolicy: "allow",
		MaxOutputBytes:    16,
		Enabled:           true,
	}})

	started := time.Now()
	run, err := service.Invoke(context.Background(), InvokeParams{ActionName: "slow"})
	if err != nil {
		t.Fatalf("invoke timed action: %v", err)
	}

	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("expected timeout enforcement to stop early, took %s", elapsed)
	}
	if run.Status != storage.RunStatusTimedOut {
		t.Fatalf("expected status %q, got %q", storage.RunStatusTimedOut, run.Status)
	}
	if !run.TimedOut {
		t.Fatal("expected run to be marked timed out")
	}
	if run.ExitCode != nil {
		t.Fatalf("expected exit code to be absent for timed out run, got %v", run.ExitCode)
	}
	if run.StdoutTail != "start" {
		t.Fatalf("expected stdout tail %q, got %q", "start", run.StdoutTail)
	}
	if !strings.Contains(run.ErrorSummary, "timed out after 50ms") {
		t.Fatalf("expected timeout summary, got %q", run.ErrorSummary)
	}
}

func TestServiceInvokePersistsFailedRunWhenProcessCannotStart(t *testing.T) {
	service, store := newTestService(t, []config.Action{commandAction(
		"missing",
		[]string{"/definitely/missing/microhook-binary"},
		nil,
		64,
	)})
	service.newRunID = func() (string, error) { return "run-missing", nil }

	run, err := service.Invoke(context.Background(), InvokeParams{ActionName: "missing"})
	if err != nil {
		t.Fatalf("invoke missing command: %v", err)
	}

	if run.Status != storage.RunStatusFailed {
		t.Fatalf("expected status %q, got %q", storage.RunStatusFailed, run.Status)
	}
	if run.StartedAt == nil || run.FinishedAt == nil {
		t.Fatal("expected timestamps for failed start")
	}
	if !strings.Contains(run.ErrorSummary, "start process") {
		t.Fatalf("expected start failure summary, got %q", run.ErrorSummary)
	}

	storedRun, err := store.GetRun(context.Background(), "run-missing")
	if err != nil {
		t.Fatalf("get stored failed run: %v", err)
	}
	if storedRun.Status != storage.RunStatusFailed {
		t.Fatalf("expected stored run status %q, got %q", storage.RunStatusFailed, storedRun.Status)
	}
}

func TestServiceInvokeRejectsUnknownAndDisabledActions(t *testing.T) {
	service, _ := newTestService(t, []config.Action{{
		Name:              "disabled",
		Mode:              config.ActionModeCommand,
		Command:           []string{"/bin/sh", "-c", "exit 0"},
		ConcurrencyPolicy: "allow",
		MaxOutputBytes:    16,
		Enabled:           false,
	}})

	_, err := service.Invoke(context.Background(), InvokeParams{ActionName: "missing"})
	if !errors.Is(err, ErrActionNotFound) {
		t.Fatalf("expected missing action error, got %v", err)
	}

	_, err = service.Invoke(context.Background(), InvokeParams{ActionName: "disabled"})
	if !errors.Is(err, ErrActionDisabled) {
		t.Fatalf("expected disabled action error, got %v", err)
	}
}

func TestServiceInvokeQueueSerializesRunsAndPersistsQueueState(t *testing.T) {
	service, store := newTestService(t, []config.Action{{
		Name:              "serial",
		Mode:              config.ActionModeCommand,
		Command:           []string{"/bin/sh", "-c", "sleep 0.1; printf \"$MICROHOOK_RUN_ID\""},
		Timeout:           time.Second,
		ConcurrencyPolicy: "queue",
		MaxOutputBytes:    64,
		Enabled:           true,
	}})

	runIDs := []string{"run-one", "run-two"}
	service.newRunID = func() (string, error) {
		id := runIDs[0]
		runIDs = runIDs[1:]
		return id, nil
	}

	first, err := service.Invoke(context.Background(), InvokeParams{ActionName: "serial", Mode: InvokeModeAsync})
	if err != nil {
		t.Fatalf("invoke first queued action: %v", err)
	}
	if first.Status != storage.RunStatusRunning {
		t.Fatalf("expected first run status %q, got %q", storage.RunStatusRunning, first.Status)
	}

	second, err := service.Invoke(context.Background(), InvokeParams{ActionName: "serial", Mode: InvokeModeAsync})
	if err != nil {
		t.Fatalf("invoke second queued action: %v", err)
	}
	if second.Status != storage.RunStatusQueued {
		t.Fatalf("expected second run status %q, got %q", storage.RunStatusQueued, second.Status)
	}
	if second.StartedAt != nil {
		t.Fatalf("expected queued run to omit started_at, got %v", second.StartedAt)
	}

	queuedRuns, err := store.ListQueuedRuns(context.Background(), "serial")
	if err != nil {
		t.Fatalf("list queued runs: %v", err)
	}
	if len(queuedRuns) != 1 {
		t.Fatalf("expected 1 queued run, got %d", len(queuedRuns))
	}
	if queuedRuns[0].RunID != second.ID {
		t.Fatalf("expected queued run id %q, got %q", second.ID, queuedRuns[0].RunID)
	}

	first = waitForTerminalRun(t, store, first.ID)
	second = waitForTerminalRun(t, store, second.ID)

	if first.Status != storage.RunStatusSucceeded {
		t.Fatalf("expected first run status %q, got %q", storage.RunStatusSucceeded, first.Status)
	}
	if second.Status != storage.RunStatusSucceeded {
		t.Fatalf("expected second run status %q, got %q", storage.RunStatusSucceeded, second.Status)
	}
	if second.StartedAt == nil || first.FinishedAt == nil {
		t.Fatal("expected queued runs to record started_at and finished_at")
	}
	if second.StartedAt.Before(*first.FinishedAt) {
		t.Fatalf("expected second run to start after first finished, got %s before %s", second.StartedAt, first.FinishedAt)
	}
	if first.StdoutTail != "run-one" {
		t.Fatalf("expected first stdout tail %q, got %q", "run-one", first.StdoutTail)
	}
	if second.StdoutTail != "run-two" {
		t.Fatalf("expected second stdout tail %q, got %q", "run-two", second.StdoutTail)
	}

	queuedRuns, err = store.ListQueuedRuns(context.Background(), "serial")
	if err != nil {
		t.Fatalf("list queued runs after completion: %v", err)
	}
	if len(queuedRuns) != 0 {
		t.Fatalf("expected queue to drain, got %+v", queuedRuns)
	}
}

func TestServiceRecoverCancelsRunningRunsAndReplaysQueuedRunsFromSnapshot(t *testing.T) {
	ctx := context.Background()
	storagePath := filepath.Join(t.TempDir(), "microhook.db")
	store, err := storage.Open(ctx, storagePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	}()

	startedAt := time.Date(2026, time.April, 21, 10, 15, 0, 0, time.UTC)
	if _, err := store.CreateRun(ctx, storage.CreateRunParams{
		ID:              "run-interrupted",
		ActionName:      "serial",
		Status:          storage.RunStatusRunning,
		CreatedAt:       startedAt.Add(-2 * time.Second),
		StartedAt:       &startedAt,
		RequestMetadata: json.RawMessage(`{"mode":"async","request_id":"interrupted"}`),
		ActionSnapshot: storage.ActionSnapshot{
			Description:       "Interrupted run",
			Mode:              config.ActionModeCommand,
			Command:           []string{"/bin/sh", "-c", "printf should-not-run"},
			Timeout:           time.Second,
			ConcurrencyPolicy: "queue",
			MaxOutputBytes:    64,
			Enabled:           true,
		},
	}); err != nil {
		t.Fatalf("create running run: %v", err)
	}

	createQueuedRun := func(id, requestID, shellCommand string, createdAt time.Time) {
		t.Helper()

		if _, err := store.CreateRun(ctx, storage.CreateRunParams{
			ID:              id,
			ActionName:      "serial",
			Status:          storage.RunStatusQueued,
			CreatedAt:       createdAt,
			RequestMetadata: json.RawMessage(`{"mode":"async","request_id":"` + requestID + `"}`),
			ActionSnapshot: storage.ActionSnapshot{
				Description:       "Recovered queued run",
				Mode:              config.ActionModeCommand,
				Command:           []string{"/bin/sh", "-c", shellCommand},
				Timeout:           time.Second,
				ConcurrencyPolicy: "queue",
				MaxOutputBytes:    64,
				Enabled:           true,
			},
		}); err != nil {
			t.Fatalf("create queued run %q: %v", id, err)
		}

		if _, err := store.EnqueueRun(ctx, storage.EnqueueRunParams{
			RunID:      id,
			ActionName: "serial",
			EnqueuedAt: createdAt,
			Input:      json.RawMessage(`{"request_id":"` + requestID + `"}`),
		}); err != nil {
			t.Fatalf("enqueue queued run %q: %v", id, err)
		}
	}

	createQueuedRun("run-queued-1", "recovered-1", `printf "$MICROHOOK_REQUEST_ID"; sleep 0.05`, startedAt.Add(time.Second))
	createQueuedRun("run-queued-2", "recovered-2", `printf "$MICROHOOK_REQUEST_ID"`, startedAt.Add(2*time.Second))

	service := New(store, mustRegistry(t, []config.Action{commandAction(
		"serial",
		[]string{"/bin/sh", "-c", `printf registry-definition`},
		nil,
		64,
	)}))
	if err := service.Recover(ctx); err != nil {
		t.Fatalf("recover executor state: %v", err)
	}

	interrupted, err := store.GetRun(ctx, "run-interrupted")
	if err != nil {
		t.Fatalf("get interrupted run: %v", err)
	}
	if interrupted.Status != storage.RunStatusCancelled {
		t.Fatalf("expected interrupted run status %q, got %q", storage.RunStatusCancelled, interrupted.Status)
	}
	if interrupted.ErrorSummary != restartedRunSummary {
		t.Fatalf("expected interrupted run summary %q, got %q", restartedRunSummary, interrupted.ErrorSummary)
	}

	first := waitForTerminalRun(t, store, "run-queued-1")
	second := waitForTerminalRun(t, store, "run-queued-2")

	if first.Status != storage.RunStatusSucceeded {
		t.Fatalf("expected first recovered run status %q, got %q", storage.RunStatusSucceeded, first.Status)
	}
	if second.Status != storage.RunStatusSucceeded {
		t.Fatalf("expected second recovered run status %q, got %q", storage.RunStatusSucceeded, second.Status)
	}
	if first.StdoutTail != "recovered-1" {
		t.Fatalf("expected first recovered stdout tail %q, got %q", "recovered-1", first.StdoutTail)
	}
	if second.StdoutTail != "recovered-2" {
		t.Fatalf("expected second recovered stdout tail %q, got %q", "recovered-2", second.StdoutTail)
	}
	if first.FinishedAt == nil || second.StartedAt == nil {
		t.Fatal("expected recovered runs to record finished_at and started_at")
	}
	if second.StartedAt.Before(*first.FinishedAt) {
		t.Fatalf("expected second recovered run to start after first finished, got %s before %s", second.StartedAt, first.FinishedAt)
	}

	queuedRuns, err := store.ListQueuedRuns(ctx, "serial")
	if err != nil {
		t.Fatalf("list queued runs after recovery: %v", err)
	}
	if len(queuedRuns) != 0 {
		t.Fatalf("expected recovered queue to drain, got %+v", queuedRuns)
	}
}

func newTestService(t *testing.T, actions []config.Action) (*Service, *storage.Store) {
	t.Helper()

	store, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "microhook.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})

	service := New(store, mustRegistry(t, actions))
	return service, store
}

func mustRegistry(t *testing.T, actions []config.Action) config.ActionRegistry {
	t.Helper()

	registry := config.Config{Actions: actions}.ActionRegistry()
	if registry.Len() != len(actions) {
		t.Fatalf("expected %d actions in registry, got %d", len(actions), registry.Len())
	}

	return registry
}

func commandAction(name string, command []string, env map[string]string, maxOutputBytes int) config.Action {
	return config.Action{
		Name:              name,
		Mode:              config.ActionModeCommand,
		Command:           command,
		Timeout:           time.Second,
		Env:               env,
		ConcurrencyPolicy: "allow",
		MaxOutputBytes:    maxOutputBytes,
		Enabled:           true,
	}
}

func shellAction(name, shellCommand string, maxOutputBytes int) config.Action {
	return config.Action{
		Name:              name,
		Mode:              config.ActionModeShell,
		ShellCommand:      shellCommand,
		Timeout:           time.Second,
		ConcurrencyPolicy: "allow",
		MaxOutputBytes:    maxOutputBytes,
		Enabled:           true,
	}
}

func waitForTerminalRun(t *testing.T, store *storage.Store, runID string) storage.Run {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for {
		run, err := store.GetRun(context.Background(), runID)
		if err != nil {
			t.Fatalf("get run %q: %v", runID, err)
		}
		if run.Status != storage.RunStatusQueued && run.Status != storage.RunStatusRunning {
			return run
		}

		if time.Now().After(deadline) {
			t.Fatalf("run %q did not reach a terminal state before timeout", runID)
		}

		time.Sleep(25 * time.Millisecond)
	}
}
