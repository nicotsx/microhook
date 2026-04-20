package execution

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nicotsx/microhook/internal/config"
	"github.com/nicotsx/microhook/internal/storage"
)

const restartedRunSummary = "service restarted before run completion"

func (s *Service) invokeAllow(ctx context.Context, action config.Action, mode string, input, requestMetadata json.RawMessage, requestID string) (storage.Run, error) {
	run, err := s.createRun(ctx, action, storage.RunStatusRunning, requestMetadata, true)
	if err != nil {
		return storage.Run{}, err
	}
	storedAction := actionFromStoredRun(run)

	if mode == InvokeModeAsync {
		s.startAsyncInvocation(storedAction, run, input, requestID, nil)
		return run, nil
	}

	return s.execute(context.WithoutCancel(ctx), ctx, run, storedAction, input, requestID)
}

func (s *Service) invokeReject(ctx context.Context, action config.Action, mode string, input, requestMetadata json.RawMessage, requestID string) (storage.Run, error) {
	if !s.beginRejectAction(action.Name) {
		return storage.Run{}, fmt.Errorf("invoke action %q: %w", action.Name, ErrActionConflict)
	}

	run, err := s.createRun(ctx, action, storage.RunStatusRunning, requestMetadata, true)
	if err != nil {
		s.finishRejectAction(action.Name)
		return storage.Run{}, err
	}
	storedAction := actionFromStoredRun(run)

	finish := func() {
		s.finishRejectAction(action.Name)
	}

	if mode == InvokeModeAsync {
		s.startAsyncInvocation(storedAction, run, input, requestID, finish)
		return run, nil
	}

	finishedRun, err := s.execute(context.WithoutCancel(ctx), ctx, run, storedAction, input, requestID)
	finish()
	return finishedRun, err
}

func (s *Service) startAsyncInvocation(action config.Action, run storage.Run, input json.RawMessage, requestID string, onFinish func()) {
	go func() {
		_, _ = s.execute(context.Background(), context.Background(), run, action, cloneJSON(input), requestID)
		if onFinish != nil {
			onFinish()
		}
	}()
}

func (s *Service) cancelInterruptedRuns(ctx context.Context) error {
	runs, err := s.store.ListRuns(ctx, storage.RunFilter{Status: storage.RunStatusRunning})
	if err != nil {
		return fmt.Errorf("recover execution state: list running runs: %w", err)
	}

	for _, run := range runs {
		if _, err := s.finishRun(ctx, run, storage.RunStatusCancelled, processResult{
			finishedAt:   s.now(),
			errorSummary: restartedRunSummary,
		}); err != nil {
			return fmt.Errorf("recover execution state: cancel interrupted run %q: %w", run.ID, err)
		}
	}

	return nil
}

func (s *Service) beginRejectAction(actionName string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.rejectRunning[actionName] {
		return false
	}

	s.rejectRunning[actionName] = true
	return true
}

func (s *Service) finishRejectAction(actionName string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.rejectRunning, actionName)
}
