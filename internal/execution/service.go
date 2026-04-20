package execution

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nicotsx/microhook/internal/config"
	"github.com/nicotsx/microhook/internal/storage"
)

const (
	shellPath   = "/bin/sh"
	defaultPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

	InvokeModeSync  = "sync"
	InvokeModeAsync = "async"

	envRunID     = "MICROHOOK_RUN_ID"
	envAction    = "MICROHOOK_ACTION"
	envRequestID = "MICROHOOK_REQUEST_ID"
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
}

type Service struct {
	store    runStore
	registry config.ActionRegistry
	now      func() time.Time
	newRunID func() (string, error)

	mu      sync.Mutex
	actions map[string]*actionState
}

type InvokeParams struct {
	ActionName      string
	Mode            string
	Input           json.RawMessage
	RequestMetadata json.RawMessage
	RequestID       string
}

type actionState struct {
	running int
	queue   []queuedInvocation
}

type queuedInvocation struct {
	action    config.Action
	run       storage.Run
	input     json.RawMessage
	requestID string
	runCtx    context.Context
	waitCh    chan invokeResult
}

type invokeResult struct {
	run storage.Run
	err error
}

func New(store runStore, registry config.ActionRegistry) *Service {
	return &Service{
		store:    store,
		registry: registry,
		now: func() time.Time {
			return time.Now().UTC()
		},
		newRunID: generateRunID,
		actions:  make(map[string]*actionState),
	}
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
	case "queue":
		return s.invokeQueue(ctx, action, mode, input, requestMetadata, params.RequestID)
	default:
		return storage.Run{}, fmt.Errorf("invoke action %q: unsupported concurrency policy %q", actionName, action.ConcurrencyPolicy)
	}
}

func (s *Service) HasAction(name string) bool {
	_, ok := s.registry.Get(strings.TrimSpace(name))
	return ok
}

func (s *Service) invokeAllow(ctx context.Context, action config.Action, mode string, input, requestMetadata json.RawMessage, requestID string) (storage.Run, error) {
	run, err := s.createRun(ctx, action, storage.RunStatusRunning, requestMetadata, true)
	if err != nil {
		return storage.Run{}, err
	}

	if mode == InvokeModeAsync {
		s.startAsyncInvocation(action, run, input, requestID, context.Background(), nil, nil)
		return run, nil
	}

	return s.execute(context.WithoutCancel(ctx), ctx, run, action, input, requestID)
}

func (s *Service) invokeReject(ctx context.Context, action config.Action, mode string, input, requestMetadata json.RawMessage, requestID string) (storage.Run, error) {
	s.mu.Lock()
	state := s.actionState(action.Name)
	if state.running > 0 {
		s.mu.Unlock()
		return storage.Run{}, fmt.Errorf("invoke action %q: %w", action.Name, ErrActionConflict)
	}

	run, err := s.createRun(ctx, action, storage.RunStatusRunning, requestMetadata, true)
	if err == nil {
		state.running = 1
	}
	s.mu.Unlock()
	if err != nil {
		return storage.Run{}, err
	}

	finish := func() {
		s.finishRejectAction(action.Name)
	}

	if mode == InvokeModeAsync {
		s.startAsyncInvocation(action, run, input, requestID, context.Background(), finish, nil)
		return run, nil
	}

	finishedRun, err := s.execute(context.WithoutCancel(ctx), ctx, run, action, input, requestID)
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
	if state.running == 0 && len(state.queue) == 0 {
		run, err := s.createRun(ctx, action, storage.RunStatusRunning, requestMetadata, true)
		if err == nil {
			state.running = 1
		}
		s.mu.Unlock()
		if err != nil {
			return storage.Run{}, err
		}

		finish := func() {
			s.finishQueueAction(action.Name)
		}

		if mode == InvokeModeAsync {
			s.startAsyncInvocation(action, run, input, requestID, context.Background(), finish, nil)
			return run, nil
		}

		finishedRun, err := s.execute(context.WithoutCancel(ctx), ctx, run, action, input, requestID)
		finish()
		return finishedRun, err
	}

	run, err := s.createRun(ctx, action, storage.RunStatusQueued, requestMetadata, false)
	if err != nil {
		s.mu.Unlock()
		return storage.Run{}, err
	}

	state.queue = append(state.queue, queuedInvocation{
		action:    action,
		run:       run,
		input:     input,
		requestID: requestID,
		runCtx:    runCtx,
		waitCh:    waitCh,
	})
	s.mu.Unlock()

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
			return cancelledRun, ctx.Err()
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
		deliverInvokeResult(item.waitCh, cancelledRun, errors.Join(err, cancelErr))
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

	s.startAsyncInvocation(item.action, run, item.input, item.requestID, item.runCtx, func() {
		s.finishQueueAction(item.action.Name)
	}, item.waitCh)
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

func (s *Service) markRunRunning(ctx context.Context, runID string) (storage.Run, error) {
	startedAt := s.now()
	if err := s.store.UpdateRun(ctx, storage.UpdateRunParams{
		ID:        runID,
		Status:    storage.RunStatusRunning,
		StartedAt: &startedAt,
	}); err != nil {
		return storage.Run{}, fmt.Errorf("mark run %q running: %w", runID, err)
	}

	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return storage.Run{}, fmt.Errorf("get run %q after start: %w", runID, err)
	}

	return run, nil
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
	if state.running == 0 && len(state.queue) == 0 {
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
		delete(s.actions, actionName)
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
		if state.running == 0 && len(state.queue) == 0 {
			delete(s.actions, actionName)
		}
		return true
	}

	return false
}

func (s *Service) execute(persistCtx, runCtx context.Context, run storage.Run, action config.Action, input json.RawMessage, requestID string) (storage.Run, error) {
	stdoutTail := newTailBuffer(action.MaxOutputBytes)
	stderrTail := newTailBuffer(action.MaxOutputBytes)

	cmd, err := buildCommand(action)
	if err != nil {
		return s.finishRun(persistCtx, run, storage.RunStatusFailed, processResult{
			finishedAt:   s.now(),
			errorSummary: err.Error(),
		})
	}

	cmd.Dir = action.Cwd
	cmd.Env = buildEnv(action.Env, run.ID, action.Name, requestID)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stdout = stdoutTail
	cmd.Stderr = stderrTail
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	execCtx, cancel := executionContext(runCtx, action.Timeout)
	defer cancel()

	if err := cmd.Start(); err != nil {
		return s.finishRun(persistCtx, run, storage.RunStatusFailed, processResult{
			finishedAt:   s.now(),
			stdoutTail:   stdoutTail.String(),
			stderrTail:   stderrTail.String(),
			errorSummary: fmt.Sprintf("start process: %v", err),
		})
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	select {
	case waitErr := <-waitCh:
		return s.finishCompletion(persistCtx, run, stdoutTail.String(), stderrTail.String(), cmd.ProcessState, waitErr)
	case <-execCtx.Done():
	}

	select {
	case waitErr := <-waitCh:
		return s.finishCompletion(persistCtx, run, stdoutTail.String(), stderrTail.String(), cmd.ProcessState, waitErr)
	default:
	}

	terminateErr := terminateProcessGroup(cmd.Process)
	waitErr := <-waitCh
	exitCode := processExitCode(cmd.ProcessState)
	if waitErr == nil || exitCode != nil {
		return s.finishCompletion(persistCtx, run, stdoutTail.String(), stderrTail.String(), cmd.ProcessState, waitErr)
	}

	status, errorSummary := interruptedStatus(execCtx, runCtx, action.Timeout)
	if terminateErr != nil {
		errorSummary = joinSummary(errorSummary, fmt.Sprintf("terminate process group: %v", terminateErr))
	}

	finishedRun, err := s.finishRun(persistCtx, run, status, processResult{
		finishedAt:   s.now(),
		timedOut:     status == storage.RunStatusTimedOut,
		stdoutTail:   stdoutTail.String(),
		stderrTail:   stderrTail.String(),
		errorSummary: errorSummary,
	})
	if err != nil {
		return storage.Run{}, err
	}
	if terminateErr != nil {
		return storage.Run{}, fmt.Errorf("invoke action %q run %q: %w", action.Name, run.ID, terminateErr)
	}

	return finishedRun, nil
}

func (s *Service) finishCompletion(ctx context.Context, run storage.Run, stdoutTail, stderrTail string, state *os.ProcessState, waitErr error) (storage.Run, error) {
	result := processResult{
		finishedAt: s.now(),
		stdoutTail: stdoutTail,
		stderrTail: stderrTail,
		exitCode:   processExitCode(state),
	}

	status := storage.RunStatusSucceeded
	if waitErr != nil {
		status = storage.RunStatusFailed
		result.errorSummary = failureSummary(waitErr, result.exitCode)
	}

	return s.finishRun(ctx, run, status, result)
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

type processResult struct {
	exitCode     *int
	finishedAt   time.Time
	timedOut     bool
	stdoutTail   string
	stderrTail   string
	errorSummary string
}

type tailBuffer struct {
	limit int
	buf   []byte
}

func newTailBuffer(limit int) *tailBuffer {
	return &tailBuffer{limit: limit}
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 || len(p) == 0 {
		return len(p), nil
	}

	if len(p) >= b.limit {
		b.buf = append(b.buf[:0], p[len(p)-b.limit:]...)
		return len(p), nil
	}

	overflow := len(b.buf) + len(p) - b.limit
	if overflow > 0 {
		b.buf = append(b.buf[:0], b.buf[overflow:]...)
	}

	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *tailBuffer) String() string {
	return string(b.buf)
}

func buildCommand(action config.Action) (*exec.Cmd, error) {
	switch action.Mode {
	case config.ActionModeCommand:
		if len(action.Command) == 0 {
			return nil, fmt.Errorf("action %q command is required", action.Name)
		}
		return exec.Command(action.Command[0], action.Command[1:]...), nil

	case config.ActionModeShell:
		if strings.TrimSpace(action.ShellCommand) == "" {
			return nil, fmt.Errorf("action %q shell command is required", action.Name)
		}
		return exec.Command(shellPath, "-c", action.ShellCommand), nil

	default:
		return nil, fmt.Errorf("action %q has unsupported mode %q", action.Name, action.Mode)
	}
}

func buildEnv(configured map[string]string, runID, actionName, requestID string) []string {
	env := make(map[string]string, len(configured)+4)
	if path, ok := os.LookupEnv("PATH"); ok && strings.TrimSpace(path) != "" {
		env["PATH"] = path
	} else {
		env["PATH"] = defaultPath
	}

	maps.Copy(env, configured)

	env[envRunID] = runID
	env[envAction] = actionName
	env[envRequestID] = requestID

	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	values := make([]string, 0, len(keys))
	for _, key := range keys {
		values = append(values, key+"="+env[key])
	}

	return values
}

func actionSnapshotFromConfig(action config.Action) storage.ActionSnapshot {
	return storage.ActionSnapshot{
		Description:       action.Description,
		Mode:              action.Mode,
		Command:           slicesClone(action.Command),
		ShellCommand:      action.ShellCommand,
		Cwd:               action.Cwd,
		Timeout:           action.Timeout,
		Env:               maps.Clone(action.Env),
		ConcurrencyPolicy: action.ConcurrencyPolicy,
		MaxOutputBytes:    action.MaxOutputBytes,
		Enabled:           action.Enabled,
	}
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

func executionContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}

	return context.WithTimeout(ctx, timeout)
}

func terminateProcessGroup(process *os.Process) error {
	if process == nil {
		return nil
	}

	if err := syscall.Kill(-process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		if killErr := process.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
			return errors.Join(err, killErr)
		}
		return err
	}

	return nil
}

func interruptedStatus(execCtx, runCtx context.Context, timeout time.Duration) (string, string) {
	if timeout > 0 && errors.Is(execCtx.Err(), context.DeadlineExceeded) && runCtx.Err() == nil {
		return storage.RunStatusTimedOut, fmt.Sprintf("process timed out after %s", timeout)
	}

	return storage.RunStatusCancelled, fmt.Sprintf("execution cancelled: %v", execCtx.Err())
}

func processExitCode(state *os.ProcessState) *int {
	if state == nil {
		return nil
	}

	exitCode := state.ExitCode()
	if exitCode < 0 {
		return nil
	}

	return &exitCode
}

func failureSummary(waitErr error, exitCode *int) string {
	if exitCode != nil {
		return fmt.Sprintf("process exited with code %d", *exitCode)
	}

	return waitErr.Error()
}

func joinSummary(parts ...string) string {
	nonEmpty := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			nonEmpty = append(nonEmpty, part)
		}
	}

	return strings.Join(nonEmpty, "; ")
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

func deliverInvokeResult(waitCh chan invokeResult, run storage.Run, err error) {
	if waitCh == nil {
		return
	}

	waitCh <- invokeResult{run: run, err: err}
	close(waitCh)
}
