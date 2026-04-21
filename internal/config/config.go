// Package config defines the configuration structures and constants for the microhook application.
package config

import "time"

const (
	DefaultPath              = "/etc/microhook/config.yml"
	EnvPathVar               = "MICROHOOK_CONFIG"
	DefaultListenAddress     = "127.0.0.1:9464"
	DefaultLogFormat         = "json"
	DefaultConcurrencyPolicy = "allow"
	DefaultMaxOutputBytes    = 64 * 1024

	ActionModeCommand = "command"
	ActionModeShell   = "shell"
)

type Config struct {
	Server  ServerConfig
	Auth    AuthConfig
	Storage StorageConfig
	Actions []Action

	actionRegistry ActionRegistry
}

type ServerConfig struct {
	Listen    string
	LogFormat string
}

type AuthConfig struct {
	Tokens []Token
}

type Token struct {
	Name    string
	Value   string
	Actions []string

	actionSet map[string]struct{}
}

func (t Token) IsGlobal() bool {
	return len(t.Actions) == 0
}

func (t Token) AllowsAction(actionName string) bool {
	if t.IsGlobal() {
		return true
	}

	_, ok := t.actionSet[actionName]
	return ok
}

type StorageConfig struct {
	Path          string
	RetentionDays int
	MaxRuns       int
}

type Action struct {
	Name              string
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

func (a Action) IsEnabled() bool {
	return a.Enabled
}

func (a Action) UsesShell() bool {
	return a.Mode == ActionModeShell
}
