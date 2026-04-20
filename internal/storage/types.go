package storage

import (
	"encoding/json"
	"errors"
	"time"
)

const (
	RunStatusRunning   = "running"
	RunStatusSucceeded = "succeeded"
	RunStatusFailed    = "failed"
	RunStatusTimedOut  = "timed_out"
	RunStatusCancelled = "cancelled"
)

var (
	ErrRunNotFound      = errors.New("run not found")
	ErrInvalidRetention = errors.New("invalid retention policy")
	ErrInvalidRunState  = errors.New("invalid run state")
)

type Run struct {
	ID              string
	ActionName      string
	Status          string
	ExitCode        *int
	CreatedAt       time.Time
	StartedAt       *time.Time
	FinishedAt      *time.Time
	TimedOut        bool
	RequestMetadata json.RawMessage
	StdoutTail      string
	StderrTail      string
	ErrorSummary    string
	ActionSnapshot  ActionSnapshot
}

type ActionSnapshot struct {
	Description       string
	Mode              string
	Command           []string
	ShellCommand      string
	Cwd               string
	Timeout           time.Duration
	Env               map[string]string
	ConcurrencyPolicy string
	MaxOutputBytes    int
	Enabled           bool
}

type CreateRunParams struct {
	ID              string
	ActionName      string
	Status          string
	CreatedAt       time.Time
	StartedAt       *time.Time
	RequestMetadata json.RawMessage
	StdoutTail      string
	StderrTail      string
	ErrorSummary    string
	ActionSnapshot  ActionSnapshot
}

type UpdateRunParams struct {
	ID           string
	Status       string
	ExitCode     *int
	StartedAt    *time.Time
	FinishedAt   *time.Time
	TimedOut     bool
	StdoutTail   string
	StderrTail   string
	ErrorSummary string
}

type RunFilter struct {
	ActionName string
	Status     string
}

type RetentionPolicy struct {
	MaxAge  time.Duration
	MaxRuns int
}

type RetentionResult struct {
	DeletedRuns int
	PrunedAt    time.Time
}
