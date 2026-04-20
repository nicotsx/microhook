package config

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

func ResolvePath(explicit string) string {
	if explicit != "" {
		return explicit
	}

	if envPath := os.Getenv(EnvPathVar); envPath != "" {
		return envPath
	}

	return DefaultPath
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}

	var fileCfg fileConfig
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)

	if err := decoder.Decode(&fileCfg); err != nil {
		return Config{}, fmt.Errorf("parse config %q: %w", path, err)
	}

	cfg, errs := normalize(fileCfg)
	errs.Append(cfg.Validate())
	if err := errs.OrNil(); err != nil {
		return Config{}, fmt.Errorf("invalid config %q:\n%s", path, err)
	}

	registry, err := newActionRegistry(cfg.Actions)
	if err != nil {
		return Config{}, fmt.Errorf("invalid config %q:\n%s", path, err)
	}

	cfg.actionRegistry = registry
	return cfg, nil
}

type fileConfig struct {
	Server  fileServerConfig  `yaml:"server"`
	Auth    fileAuthConfig    `yaml:"auth"`
	Storage fileStorageConfig `yaml:"storage"`
	Actions []fileAction      `yaml:"actions"`
}

type fileServerConfig struct {
	Listen    string `yaml:"listen"`
	LogFormat string `yaml:"log_format"`
}

type fileAuthConfig struct {
	Tokens []fileToken `yaml:"tokens"`
}

type fileToken struct {
	Name    string   `yaml:"name"`
	Value   string   `yaml:"value"`
	Actions []string `yaml:"actions"`
}

type fileStorageConfig struct {
	Path          string `yaml:"path"`
	RetentionDays int    `yaml:"retention_days"`
	MaxRuns       int    `yaml:"max_runs"`
}

type fileAction struct {
	Name              string            `yaml:"name"`
	Description       string            `yaml:"description"`
	Command           []string          `yaml:"command"`
	ShellCommand      string            `yaml:"shell_command"`
	Cwd               string            `yaml:"cwd"`
	Timeout           string            `yaml:"timeout"`
	Env               map[string]string `yaml:"env"`
	ConcurrencyPolicy string            `yaml:"concurrency_policy"`
	MaxOutputBytes    *int              `yaml:"max_output_bytes"`
	Enabled           *bool             `yaml:"enabled"`
}

func normalize(fileCfg fileConfig) (Config, ValidationErrors) {
	cfg := Config{
		Server: ServerConfig{
			Listen:    DefaultListenAddress,
			LogFormat: DefaultLogFormat,
		},
		Auth: AuthConfig{
			Tokens: normalizeTokens(fileCfg.Auth.Tokens),
		},
		Storage: StorageConfig{
			Path:          strings.TrimSpace(fileCfg.Storage.Path),
			RetentionDays: fileCfg.Storage.RetentionDays,
			MaxRuns:       fileCfg.Storage.MaxRuns,
		},
	}

	if listen := strings.TrimSpace(fileCfg.Server.Listen); listen != "" {
		cfg.Server.Listen = listen
	}

	if logFormat := strings.TrimSpace(fileCfg.Server.LogFormat); logFormat != "" {
		cfg.Server.LogFormat = logFormat
	}

	actions, errs := normalizeActions(fileCfg.Actions)
	cfg.Actions = actions

	return cfg, errs
}

func normalizeTokens(fileTokens []fileToken) []Token {
	if len(fileTokens) == 0 {
		return nil
	}

	tokens := make([]Token, 0, len(fileTokens))
	for _, fileToken := range fileTokens {
		actions := normalizeActionNames(fileToken.Actions)
		tokens = append(tokens, Token{
			Name:      strings.TrimSpace(fileToken.Name),
			Value:     strings.TrimSpace(fileToken.Value),
			Actions:   actions,
			actionSet: makeActionSet(actions),
		})
	}

	return tokens
}

func normalizeActionNames(actionNames []string) []string {
	if len(actionNames) == 0 {
		return nil
	}

	normalized := make([]string, 0, len(actionNames))
	for _, actionName := range actionNames {
		normalized = append(normalized, strings.TrimSpace(actionName))
	}

	return normalized
}

func normalizeActions(fileActions []fileAction) ([]Action, ValidationErrors) {
	if len(fileActions) == 0 {
		return nil, ValidationErrors{}
	}

	actions := make([]Action, 0, len(fileActions))
	var errs ValidationErrors

	for i, fileAction := range fileActions {
		action := Action{
			Name:              strings.TrimSpace(fileAction.Name),
			Description:       fileAction.Description,
			Command:           cloneStrings(fileAction.Command),
			ShellCommand:      fileAction.ShellCommand,
			Cwd:               fileAction.Cwd,
			Env:               cloneStringMap(fileAction.Env),
			ConcurrencyPolicy: DefaultConcurrencyPolicy,
			MaxOutputBytes:    DefaultMaxOutputBytes,
			Enabled:           true,
		}

		if policy := strings.TrimSpace(fileAction.ConcurrencyPolicy); policy != "" {
			action.ConcurrencyPolicy = policy
		}

		if fileAction.MaxOutputBytes != nil {
			action.MaxOutputBytes = *fileAction.MaxOutputBytes
		}

		if fileAction.Enabled != nil {
			action.Enabled = *fileAction.Enabled
		}

		if timeoutText := strings.TrimSpace(fileAction.Timeout); timeoutText != "" {
			timeout, err := time.ParseDuration(timeoutText)
			if err != nil {
				errs.Add(fmt.Sprintf("actions[%d].timeout must be a valid duration: %v", i, err))
			} else {
				action.Timeout = timeout
			}
		}

		hasCommand := len(fileAction.Command) > 0
		hasShellCommand := strings.TrimSpace(fileAction.ShellCommand) != ""
		switch {
		case hasCommand && !hasShellCommand:
			action.Mode = ActionModeCommand
		case !hasCommand && hasShellCommand:
			action.Mode = ActionModeShell
		}

		actions = append(actions, action)
	}

	return actions, errs
}

func makeActionSet(actionNames []string) map[string]struct{} {
	if len(actionNames) == 0 {
		return nil
	}

	actionSet := make(map[string]struct{}, len(actionNames))
	for _, actionName := range actionNames {
		actionSet[actionName] = struct{}{}
	}

	return actionSet
}
