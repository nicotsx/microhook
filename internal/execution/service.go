package execution

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nicotsx/microhook/internal/config"
	"github.com/nicotsx/microhook/internal/storage"
)

const (
	InvokeModeSync  = "sync"
	InvokeModeAsync = "async"
)

var (
	ErrActionNotFound = errors.New("action not found")
	ErrActionDisabled = errors.New("action disabled")
	ErrActionConflict = errors.New("action already running")
	ErrInvalidMode    = errors.New("invalid invoke mode")
)

type runStore interface {
	CreateRun(context.Context, storage.CreateRunParams) (storage.Run, error)
	UpdateRun(context.Context, storage.UpdateRunParams) error
	GetRun(context.Context, string) (storage.Run, error)
	ListRuns(context.Context, storage.RunFilter) ([]storage.Run, error)
}

type Service struct {
	store    runStore
	registry config.ActionRegistry
	now      func() time.Time
	newRunID func() (string, error)

	mu            sync.Mutex
	rejectRunning map[string]bool
}

type InvokeParams struct {
	ActionName      string
	Mode            string
	Input           json.RawMessage
	RequestMetadata json.RawMessage
	RequestID       string
}

func New(store runStore, registry config.ActionRegistry) *Service {
	return &Service{
		store:    store,
		registry: registry,
		now: func() time.Time {
			return time.Now().UTC()
		},
		newRunID:      generateRunID,
		rejectRunning: make(map[string]bool),
	}
}

func (s *Service) Recover(ctx context.Context) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("recover execution state: execution store is required")
	}

	return s.cancelInterruptedRuns(ctx)
}

func (s *Service) Invoke(ctx context.Context, params InvokeParams) (storage.Run, error) {
	if s == nil || s.store == nil {
		return storage.Run{}, fmt.Errorf("invoke action: execution store is required")
	}

	actionName := strings.TrimSpace(params.ActionName)
	if actionName == "" {
		return storage.Run{}, fmt.Errorf("invoke action: action name is required")
	}

	action, ok := s.registry.Get(actionName)
	if !ok {
		return storage.Run{}, fmt.Errorf("invoke action %q: %w", actionName, ErrActionNotFound)
	}
	if !action.IsEnabled() {
		return storage.Run{}, fmt.Errorf("invoke action %q: %w", actionName, ErrActionDisabled)
	}

	if err := validateJSONPayload(params.Input, "input"); err != nil {
		return storage.Run{}, fmt.Errorf("invoke action %q: %w", actionName, err)
	}
	if err := validateJSONPayload(params.RequestMetadata, "request metadata"); err != nil {
		return storage.Run{}, fmt.Errorf("invoke action %q: %w", actionName, err)
	}

	mode, err := normalizeInvokeMode(params.Mode)
	if err != nil {
		return storage.Run{}, fmt.Errorf("invoke action %q: %w", actionName, err)
	}

	input := cloneJSON(params.Input)
	requestMetadata := cloneJSON(params.RequestMetadata)

	switch action.ConcurrencyPolicy {
	case "allow":
		return s.invokeAllow(ctx, action, mode, input, requestMetadata, params.RequestID)
	case "reject":
		return s.invokeReject(ctx, action, mode, input, requestMetadata, params.RequestID)
	default:
		return storage.Run{}, fmt.Errorf("invoke action %q: unsupported concurrency policy %q", actionName, action.ConcurrencyPolicy)
	}
}

func (s *Service) HasAction(name string) bool {
	_, ok := s.registry.Get(strings.TrimSpace(name))
	return ok
}

func (s *Service) createRun(ctx context.Context, action config.Action, status string, requestMetadata json.RawMessage, started bool) (storage.Run, error) {
	runID, err := s.newRunID()
	if err != nil {
		return storage.Run{}, fmt.Errorf("invoke action %q: generate run id: %w", action.Name, err)
	}

	createdAt := s.now()
	var startedAt *time.Time
	if started {
		startedAt = &createdAt
	}

	run, err := s.store.CreateRun(ctx, storage.CreateRunParams{
		ID:              runID,
		ActionName:      action.Name,
		Status:          status,
		CreatedAt:       createdAt,
		StartedAt:       startedAt,
		RequestMetadata: requestMetadata,
		ActionSnapshot:  actionSnapshotFromConfig(action),
	})
	if err != nil {
		return storage.Run{}, fmt.Errorf("invoke action %q: create run: %w", action.Name, err)
	}

	return run, nil
}

func (s *Service) finishRun(ctx context.Context, run storage.Run, status string, result processResult) (storage.Run, error) {
	finishedAt := result.finishedAt
	if err := s.store.UpdateRun(ctx, storage.UpdateRunParams{
		ID:           run.ID,
		Status:       status,
		ExitCode:     result.exitCode,
		StartedAt:    run.StartedAt,
		FinishedAt:   &finishedAt,
		TimedOut:     result.timedOut,
		StdoutTail:   result.stdoutTail,
		StderrTail:   result.stderrTail,
		ErrorSummary: result.errorSummary,
	}); err != nil {
		return storage.Run{}, fmt.Errorf("update run %q: %w", run.ID, err)
	}

	updatedRun, err := s.store.GetRun(ctx, run.ID)
	if err != nil {
		return storage.Run{}, fmt.Errorf("get run %q after update: %w", run.ID, err)
	}

	return updatedRun, nil
}

func (s *Service) Close() error {
	return nil
}

func validateJSONPayload(value json.RawMessage, name string) error {
	if len(value) == 0 {
		return nil
	}
	if !json.Valid(value) {
		return fmt.Errorf("%s must be valid JSON", name)
	}

	return nil
}

func cloneJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return nil
	}

	cloned := make([]byte, len(value))
	copy(cloned, value)
	return cloned
}

func slicesClone(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}

func generateRunID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}

	return "run_" + hex.EncodeToString(bytes), nil
}

func normalizeInvokeMode(mode string) (string, error) {
	switch strings.TrimSpace(mode) {
	case "", InvokeModeSync:
		return InvokeModeSync, nil
	case InvokeModeAsync:
		return InvokeModeAsync, nil
	default:
		return "", ErrInvalidMode
	}
}
