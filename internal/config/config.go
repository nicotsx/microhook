package config

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultPath = "/etc/microhook/config.yml"
	EnvPathVar  = "MICROHOOK_CONFIG"
)

type Config struct {
	Server  ServerConfig  `yaml:"server"`
	Auth    AuthConfig    `yaml:"auth"`
	Storage StorageConfig `yaml:"storage"`
	Actions []Action      `yaml:"actions"`
}

type ServerConfig struct {
	Listen    string `yaml:"listen"`
	LogFormat string `yaml:"log_format"`
}

type AuthConfig struct {
	Tokens []Token `yaml:"tokens"`
}

type Token struct {
	Name    string   `yaml:"name"`
	Value   string   `yaml:"value"`
	Actions []string `yaml:"actions"`
}

type StorageConfig struct {
	Path          string `yaml:"path"`
	RetentionDays int    `yaml:"retention_days"`
	MaxRuns       int    `yaml:"max_runs"`
}

type Action struct {
	Name              string            `yaml:"name"`
	Description       string            `yaml:"description"`
	Command           []string          `yaml:"command"`
	ShellCommand      string            `yaml:"shell_command"`
	Cwd               string            `yaml:"cwd"`
	Timeout           string            `yaml:"timeout"`
	Env               map[string]string `yaml:"env"`
	ConcurrencyPolicy string            `yaml:"concurrency_policy"`
	MaxOutputBytes    int               `yaml:"max_output_bytes"`
	Enabled           *bool             `yaml:"enabled"`
}

func (a Action) IsEnabled() bool {
	return a.Enabled == nil || *a.Enabled
}

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

	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)

	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %q: %w", path, err)
	}

	cfg.applyDefaults()

	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid config %q:\n%s", path, err)
	}

	return cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Server.LogFormat == "" {
		c.Server.LogFormat = "json"
	}

	for i := range c.Actions {
		if c.Actions[i].ConcurrencyPolicy == "" {
			c.Actions[i].ConcurrencyPolicy = "allow"
		}
	}
}

func (c Config) Validate() error {
	var validationErrs ValidationErrors

	if strings.TrimSpace(c.Server.Listen) == "" {
		validationErrs.Add("server.listen is required")
	}

	switch c.Server.LogFormat {
	case "json", "text":
	case "":
		validationErrs.Add("server.log_format is required")
	default:
		validationErrs.Add("server.log_format must be one of: json, text")
	}

	if strings.TrimSpace(c.Storage.Path) == "" {
		validationErrs.Add("storage.path is required")
	}

	if c.Storage.RetentionDays < 0 {
		validationErrs.Add("storage.retention_days must be greater than or equal to 0")
	}

	if c.Storage.MaxRuns < 0 {
		validationErrs.Add("storage.max_runs must be greater than or equal to 0")
	}

	tokenNames := make(map[string]int)
	for i, token := range c.Auth.Tokens {
		prefix := fmt.Sprintf("auth.tokens[%d]", i)

		if strings.TrimSpace(token.Name) == "" {
			validationErrs.Add(prefix + ".name is required")
		} else if previous, exists := tokenNames[token.Name]; exists {
			validationErrs.Add(fmt.Sprintf("%s.name duplicates auth.tokens[%d].name (%q)", prefix, previous, token.Name))
		} else {
			tokenNames[token.Name] = i
		}

		if strings.TrimSpace(token.Value) == "" {
			validationErrs.Add(prefix + ".value is required")
		}

		for actionIndex, actionName := range token.Actions {
			if strings.TrimSpace(actionName) == "" {
				validationErrs.Add(fmt.Sprintf("%s.actions[%d] must not be empty", prefix, actionIndex))
			}
		}
	}

	actionNames := make(map[string]int)
	for i, action := range c.Actions {
		prefix := fmt.Sprintf("actions[%d]", i)

		if strings.TrimSpace(action.Name) == "" {
			validationErrs.Add(prefix + ".name is required")
		} else if previous, exists := actionNames[action.Name]; exists {
			validationErrs.Add(fmt.Sprintf("%s.name duplicates actions[%d].name (%q)", prefix, previous, action.Name))
		} else {
			actionNames[action.Name] = i
		}

		hasCommand := len(action.Command) > 0
		hasShellCommand := strings.TrimSpace(action.ShellCommand) != ""
		if hasCommand == hasShellCommand {
			validationErrs.Add(prefix + " must define exactly one of command or shell_command")
		}

		for commandIndex, arg := range action.Command {
			if strings.TrimSpace(arg) == "" {
				validationErrs.Add(fmt.Sprintf("%s.command[%d] must not be empty", prefix, commandIndex))
			}
		}

		if action.Timeout != "" {
			if _, err := time.ParseDuration(action.Timeout); err != nil {
				validationErrs.Add(fmt.Sprintf("%s.timeout must be a valid duration: %v", prefix, err))
			}
		}

		switch action.ConcurrencyPolicy {
		case "allow", "queue", "reject":
		default:
			validationErrs.Add(fmt.Sprintf("%s.concurrency_policy must be one of: allow, queue, reject", prefix))
		}

		if action.MaxOutputBytes < 0 {
			validationErrs.Add(prefix + ".max_output_bytes must be greater than or equal to 0")
		}

		if len(action.Env) > 0 {
			keys := make([]string, 0, len(action.Env))
			for key := range action.Env {
				keys = append(keys, key)
			}
			sort.Strings(keys)

			for _, key := range keys {
				if strings.TrimSpace(key) == "" {
					validationErrs.Add(prefix + ".env contains an empty key")
				}
			}
		}
	}

	return validationErrs.OrNil()
}

type ValidationErrors struct {
	messages []string
}

func (v *ValidationErrors) Add(message string) {
	v.messages = append(v.messages, message)
}

func (v ValidationErrors) OrNil() error {
	if len(v.messages) == 0 {
		return nil
	}

	return v
}

func (v ValidationErrors) Error() string {
	var builder strings.Builder
	for i, message := range v.messages {
		if i > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString("- ")
		builder.WriteString(message)
	}

	return builder.String()
}
