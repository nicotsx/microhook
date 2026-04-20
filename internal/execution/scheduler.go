package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/nicotsx/microhook/internal/config"
	"github.com/nicotsx/microhook/internal/storage"
)

const (
	restartedRunSummary   = "service restarted before run completion"
	missingQueueSummary   = "queued run was cancelled because its queue metadata was missing after restart"
	dequeueFailureSummary = "remove queued run from persisted scheduler state"
)

func (s *Service) invokeAllow(ctx context.Context, action config.Action, mode string, input, requestMetadata json.RawMessage, requestID string) (storage.Run, error) {
	run, err := s.createRun(ctx, action, storage.RunStatusRunning, requestMetadata, true)
	if err != nil {
		return storage.Run{}, err
	}
	storedAction := actionFromStoredRun(run)

	if mode == InvokeModeAsync {
		s.startAsyncInvocation(storedAction, run, input, requestID, context.Background(), nil, nil)
		return run, nil
	}

	return s.execute(context.WithoutCancel(ctx), ctx, run, storedAction, input, requestID)
}

func (s *Service) invokeReject(ctx context.Context, action config.Action, mode string, input, requestMetadata json.RawMessage, requestID string) (storage.Run, error) {
	s.mu.Lock()
	state := s.actionState(action.Name)
	if state.running > 0 {
		s.mu.Unlock()
		return storage.Run{}, fmt.Errorf("invoke action %q: %w", action.Name, ErrActionConflict)
	}
	state.running = 1
	s.mu.Unlock()

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
		s.startAsyncInvocation(storedAction, run, input, requestID, context.Background(), finish, nil)
		return run, nil
	}

	finishedRun, err := s.execute(context.WithoutCancel(ctx), ctx, run, storedAction, input, requestID)
	finish()
	return finishedRun, err
}

func (s *Service) invokeQueue(ctx context.Context, action config.Action, mode string, input, requestMetadata json.RawMessage, requestID string) (storage.Run, error) {
	runCtx := context.Background()
	var waitCh chan invokeResult
	if mode == InvokeModeSync {
		runCtx = ctx
		waitCh = make(chan invokeResult, 1)
	}

	s.mu.Lock()
	state := s.actionState(action.Name)
	if state.running == 0 && len(state.queue) == 0 && state.preparing == 0 {
		state.running = 1
		s.mu.Unlock()

		run, err := s.createRun(ctx, action, storage.RunStatusRunning, requestMetadata, true)
		if err != nil {
			s.finishQueueAction(action.Name)
			return storage.Run{}, err
		}
		storedAction := actionFromStoredRun(run)

		finish := func() {
			s.finishQueueAction(action.Name)
		}

		if mode == InvokeModeAsync {
			s.startAsyncInvocation(storedAction, run, input, requestID, context.Background(), finish, nil)
			return run, nil
		}

		finishedRun, err := s.execute(context.WithoutCancel(ctx), ctx, run, storedAction, input, requestID)
		finish()
		return finishedRun, err
	}

	state.preparing++
	s.mu.Unlock()

	run, err := s.createRun(ctx, action, storage.RunStatusQueued, requestMetadata, false)
	if err != nil {
		s.finishQueuePreparation(action.Name)
		return storage.Run{}, err
	}
	if _, err := s.store.EnqueueRun(ctx, storage.EnqueueRunParams{
		RunID:      run.ID,
		ActionName: run.ActionName,
		EnqueuedAt: run.CreatedAt,
		Input:      input,
	}); err != nil {
		_, finishErr := s.finishRun(context.Background(), run, storage.RunStatusFailed, processResult{
			finishedAt:   s.now(),
			errorSummary: fmt.Sprintf("enqueue run: %v", err),
		})
		s.finishQueuePreparation(action.Name)
		return storage.Run{}, errors.Join(err, finishErr)
	}

	s.completeQueuePreparation(action.Name, queuedInvocation{
		action:    actionFromStoredRun(run),
		run:       run,
		input:     input,
		requestID: requestID,
		runCtx:    runCtx,
		waitCh:    waitCh,
	})

	if mode == InvokeModeAsync {
		return run, nil
	}

	select {
	case result := <-waitCh:
		return result.run, result.err
	case <-ctx.Done():
		if s.removeQueuedInvocation(action.Name, run.ID) {
			cancelledRun, err := s.finishRun(context.Background(), run, storage.RunStatusCancelled, processResult{
				finishedAt:   s.now(),
				errorSummary: fmt.Sprintf("execution cancelled: %v", ctx.Err()),
			})
			if err != nil {
				return storage.Run{}, err
			}
			deleteErr := s.deleteQueuedRun(context.Background(), run.ID)
			return cancelledRun, errors.Join(ctx.Err(), deleteErr)
		}

		return storage.Run{}, ctx.Err()
	}
}

func (s *Service) startAsyncInvocation(action config.Action, run storage.Run, input json.RawMessage, requestID string, runCtx context.Context, onFinish func(), waitCh chan invokeResult) {
	go func() {
		finishedRun, err := s.execute(context.Background(), runCtx, run, action, cloneJSON(input), requestID)
		deliverInvokeResult(waitCh, finishedRun, err)
		if onFinish != nil {
			onFinish()
		}
	}()
}

func (s *Service) startQueuedInvocation(item queuedInvocation) {
	if err := item.runCtx.Err(); err != nil {
		cancelledRun, cancelErr := s.finishRun(context.Background(), item.run, storage.RunStatusCancelled, processResult{
			finishedAt:   s.now(),
			errorSummary: fmt.Sprintf("execution cancelled: %v", err),
		})
		deleteErr := s.deleteQueuedRun(context.Background(), item.run.ID)
		deliverInvokeResult(item.waitCh, cancelledRun, errors.Join(err, cancelErr, deleteErr))
		s.finishQueueAction(item.action.Name)
		return
	}

	run, err := s.markRunRunning(context.Background(), item.run.ID)
	if err != nil {
		failedRun, finishErr := s.finishRun(context.Background(), item.run, storage.RunStatusFailed, processResult{
			finishedAt:   s.now(),
			errorSummary: fmt.Sprintf("start queued run: %v", err),
		})
		deliverInvokeResult(item.waitCh, failedRun, errors.Join(err, finishErr))
		s.finishQueueAction(item.action.Name)
		return
	}
	if err := s.deleteQueuedRun(context.Background(), item.run.ID); err != nil {
		failedRun, finishErr := s.finishRun(context.Background(), run, storage.RunStatusFailed, processResult{
			finishedAt:   s.now(),
			errorSummary: fmt.Sprintf("%s: %v", dequeueFailureSummary, err),
		})
		deliverInvokeResult(item.waitCh, failedRun, errors.Join(err, finishErr))
		s.finishQueueAction(item.action.Name)
		return
	}

	s.startAsyncInvocation(item.action, run, item.input, item.requestID, item.runCtx, func() {
		s.finishQueueAction(item.action.Name)
	}, item.waitCh)
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

func (s *Service) cancelQueuedRunsMissingMetadata(ctx context.Context, queuedRuns []storage.QueuedRun) error {
	queuedRunIDs := make(map[string]struct{}, len(queuedRuns))
	for _, queuedRun := range queuedRuns {
		queuedRunIDs[queuedRun.RunID] = struct{}{}
	}

	runs, err := s.store.ListRuns(ctx, storage.RunFilter{Status: storage.RunStatusQueued})
	if err != nil {
		return fmt.Errorf("recover execution state: list queued run records: %w", err)
	}

	for _, run := range runs {
		if _, ok := queuedRunIDs[run.ID]; ok {
			continue
		}

		if _, err := s.finishRun(ctx, run, storage.RunStatusCancelled, processResult{
			finishedAt:   s.now(),
			errorSummary: missingQueueSummary,
		}); err != nil {
			return fmt.Errorf("recover execution state: cancel queued run %q with missing metadata: %w", run.ID, err)
		}
	}

	return nil
}

func (s *Service) rebuildQueueState(ctx context.Context, queuedRuns []storage.QueuedRun) ([]string, error) {
	s.mu.Lock()
	s.actions = make(map[string]*actionState)
	s.mu.Unlock()

	actionNames := make([]string, 0)
	actionSeen := make(map[string]struct{}, len(queuedRuns))

	for _, queuedRun := range queuedRuns {
		run, err := s.store.GetRun(ctx, queuedRun.RunID)
		if err != nil {
			return nil, fmt.Errorf("recover execution state: load queued run %q: %w", queuedRun.RunID, err)
		}

		if run.Status != storage.RunStatusQueued {
			if err := s.deleteQueuedRun(ctx, queuedRun.RunID); err != nil {
				return nil, fmt.Errorf("recover execution state: cleanup stale queue record for run %q: %w", queuedRun.RunID, err)
			}
			continue
		}
		if run.ActionName != queuedRun.ActionName {
			return nil, fmt.Errorf("recover execution state: queued run %q action mismatch: run=%q queue=%q", queuedRun.RunID, run.ActionName, queuedRun.ActionName)
		}
		if run.ActionSnapshot.ConcurrencyPolicy != "queue" {
			return nil, fmt.Errorf("recover execution state: queued run %q has unsupported concurrency policy %q", queuedRun.RunID, run.ActionSnapshot.ConcurrencyPolicy)
		}

		s.mu.Lock()
		state := s.actionState(run.ActionName)
		state.queue = append(state.queue, queuedInvocation{
			action:    actionFromStoredRun(run),
			run:       run,
			input:     cloneJSON(queuedRun.Input),
			requestID: requestIDFromMetadata(run.RequestMetadata),
			runCtx:    context.Background(),
		})
		s.mu.Unlock()

		if _, ok := actionSeen[run.ActionName]; ok {
			continue
		}

		actionSeen[run.ActionName] = struct{}{}
		actionNames = append(actionNames, run.ActionName)
	}

	sort.Strings(actionNames)
	return actionNames, nil
}

func (s *Service) actionState(actionName string) *actionState {
	state, ok := s.actions[actionName]
	if ok {
		return state
	}

	state = &actionState{}
	s.actions[actionName] = state
	return state
}

func (s *Service) finishRejectAction(actionName string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.actions[actionName]
	if !ok {
		return
	}

	if state.running > 0 {
		state.running--
	}
	if state.running == 0 && state.preparing == 0 && len(state.queue) == 0 {
		delete(s.actions, actionName)
	}
}

func (s *Service) finishQueueAction(actionName string) {
	var next *queuedInvocation

	s.mu.Lock()
	state, ok := s.actions[actionName]
	if !ok {
		s.mu.Unlock()
		return
	}

	if len(state.queue) == 0 {
		state.running = 0
		if state.preparing == 0 {
			delete(s.actions, actionName)
		}
		s.mu.Unlock()
		return
	}

	queued := state.queue[0]
	state.queue = state.queue[1:]
	state.running = 1
	next = &queued
	s.mu.Unlock()

	s.startQueuedInvocation(*next)
}

func (s *Service) finishQueuePreparation(actionName string) {
	trigger := false

	s.mu.Lock()
	state, ok := s.actions[actionName]
	if !ok {
		s.mu.Unlock()
		return
	}

	if state.preparing > 0 {
		state.preparing--
	}

	switch {
	case state.running == 0 && state.preparing == 0 && len(state.queue) == 0:
		delete(s.actions, actionName)
	case state.running == 0 && state.preparing == 0 && len(state.queue) > 0:
		trigger = true
	}
	s.mu.Unlock()

	if trigger {
		s.finishQueueAction(actionName)
	}
}

func (s *Service) completeQueuePreparation(actionName string, item queuedInvocation) {
	trigger := false

	s.mu.Lock()
	state := s.actionState(actionName)
	if state.preparing > 0 {
		state.preparing--
	}
	state.queue = append(state.queue, item)
	if state.running == 0 && state.preparing == 0 && len(state.queue) > 0 {
		trigger = true
	}
	s.mu.Unlock()

	if trigger {
		s.finishQueueAction(actionName)
	}
}

func (s *Service) removeQueuedInvocation(actionName, runID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.actions[actionName]
	if !ok {
		return false
	}

	for i, queued := range state.queue {
		if queued.run.ID != runID {
			continue
		}

		state.queue = append(state.queue[:i], state.queue[i+1:]...)
		if state.running == 0 && state.preparing == 0 && len(state.queue) == 0 {
			delete(s.actions, actionName)
		}
		return true
	}

	return false
}

func (s *Service) deleteQueuedRun(ctx context.Context, runID string) error {
	err := s.store.DeleteQueuedRun(ctx, runID)
	if err == nil || errors.Is(err, storage.ErrQueuedRunNotFound) {
		return nil
	}

	return fmt.Errorf("delete queued run %q: %w", runID, err)
}
