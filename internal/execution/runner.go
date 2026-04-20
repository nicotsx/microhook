package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/nicotsx/microhook/internal/config"
	"github.com/nicotsx/microhook/internal/storage"
)

const (
	shellPath   = "/bin/sh"
	defaultPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

	envRunID     = "MICROHOOK_RUN_ID"
	envAction    = "MICROHOOK_ACTION"
	envRequestID = "MICROHOOK_REQUEST_ID"
)

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

func actionFromStoredRun(run storage.Run) config.Action {
	return actionFromSnapshot(run.ActionName, run.ActionSnapshot)
}

func actionFromSnapshot(name string, snapshot storage.ActionSnapshot) config.Action {
	return config.Action{
		Name:              name,
		Description:       snapshot.Description,
		Mode:              snapshot.Mode,
		Command:           slicesClone(snapshot.Command),
		ShellCommand:      snapshot.ShellCommand,
		Cwd:               snapshot.Cwd,
		Timeout:           snapshot.Timeout,
		Env:               maps.Clone(snapshot.Env),
		ConcurrencyPolicy: snapshot.ConcurrencyPolicy,
		MaxOutputBytes:    snapshot.MaxOutputBytes,
		Enabled:           snapshot.Enabled,
	}
}

func requestIDFromMetadata(metadata json.RawMessage) string {
	if len(metadata) == 0 {
		return ""
	}

	var values map[string]json.RawMessage
	if err := json.Unmarshal(metadata, &values); err != nil {
		return ""
	}

	rawRequestID, ok := values["request_id"]
	if !ok {
		return ""
	}

	var requestID string
	if err := json.Unmarshal(rawRequestID, &requestID); err != nil {
		return ""
	}

	return strings.TrimSpace(requestID)
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
