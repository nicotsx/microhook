package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadNormalizesConfigAndBuildsActionRegistry(t *testing.T) {
	storagePath := filepath.ToSlash(filepath.Join(t.TempDir(), "microhook.db"))
	configPath := writeConfigFile(t, `
auth:
  tokens:
    - name: "deploy"
      value: "super-secret"
      actions: ["argv-action"]

storage:
  path: `+storagePath+`

actions:
  - name: "argv-action"
    command: ["echo", "hello"]
    timeout: "5s"

  - name: "shell-action"
    shell_command: "echo shell"
    concurrency_policy: "queue"
    max_output_bytes: 128
    enabled: false
`)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.Server.Listen != DefaultListenAddress {
		t.Fatalf("expected default listen address %q, got %q", DefaultListenAddress, cfg.Server.Listen)
	}

	if cfg.Server.LogFormat != DefaultLogFormat {
		t.Fatalf("expected default log format %q, got %q", DefaultLogFormat, cfg.Server.LogFormat)
	}

	if len(cfg.Actions) != 2 {
		t.Fatalf("expected 2 actions, got %d", len(cfg.Actions))
	}

	argvAction, ok := cfg.Action("argv-action")
	if !ok {
		t.Fatal("expected argv-action to be in registry")
	}

	if argvAction.Mode != ActionModeCommand {
		t.Fatalf("expected argv-action mode %q, got %q", ActionModeCommand, argvAction.Mode)
	}

	if argvAction.Timeout != 5*time.Second {
		t.Fatalf("expected argv-action timeout 5s, got %s", argvAction.Timeout)
	}

	if argvAction.ConcurrencyPolicy != DefaultConcurrencyPolicy {
		t.Fatalf("expected argv-action concurrency policy %q, got %q", DefaultConcurrencyPolicy, argvAction.ConcurrencyPolicy)
	}

	if argvAction.MaxOutputBytes != DefaultMaxOutputBytes {
		t.Fatalf("expected argv-action max output %d, got %d", DefaultMaxOutputBytes, argvAction.MaxOutputBytes)
	}

	enabledAction, ok := cfg.EnabledAction("argv-action")
	if !ok {
		t.Fatal("expected argv-action to be enabled")
	}

	if !enabledAction.IsEnabled() {
		t.Fatal("expected enabled action to report enabled")
	}

	shellAction, ok := cfg.Action("shell-action")
	if !ok {
		t.Fatal("expected shell-action to be in registry")
	}

	if shellAction.Mode != ActionModeShell {
		t.Fatalf("expected shell-action mode %q, got %q", ActionModeShell, shellAction.Mode)
	}

	if !shellAction.UsesShell() {
		t.Fatal("expected shell-action to report shell mode")
	}

	if shellAction.ConcurrencyPolicy != "queue" {
		t.Fatalf("expected shell-action concurrency policy %q, got %q", "queue", shellAction.ConcurrencyPolicy)
	}

	if shellAction.MaxOutputBytes != 128 {
		t.Fatalf("expected shell-action max output 128, got %d", shellAction.MaxOutputBytes)
	}

	if shellAction.IsEnabled() {
		t.Fatal("expected shell-action to be disabled")
	}

	if _, ok := cfg.EnabledAction("shell-action"); ok {
		t.Fatal("expected disabled action to be absent from enabled registry")
	}

	registry := cfg.ActionRegistry()
	if registry.Len() != 2 {
		t.Fatalf("expected registry length 2, got %d", registry.Len())
	}

	if allActions := registry.All(); len(allActions) != 2 {
		t.Fatalf("expected registry.All length 2, got %d", len(allActions))
	}

	if len(cfg.Auth.Tokens) != 1 {
		t.Fatalf("expected 1 token, got %d", len(cfg.Auth.Tokens))
	}

	token := cfg.Auth.Tokens[0]
	if token.IsGlobal() {
		t.Fatal("expected scoped token")
	}

	if !token.AllowsAction("argv-action") {
		t.Fatal("expected token to allow argv-action")
	}

	if token.AllowsAction("shell-action") {
		t.Fatal("expected token to reject shell-action")
	}
}

func TestLoadRejectsInvalidConfig(t *testing.T) {
	storagePath := filepath.ToSlash(filepath.Join(t.TempDir(), "microhook.db"))
	configPath := writeConfigFile(t, `
server:
  log_format: "yaml"

auth:
  tokens:
    - name: "deploy"
      value: "secret"
      actions: ["missing-action", "missing-action", ""]

storage:
  path: `+storagePath+`
  retention_days: -1
  max_runs: -5

actions:
  - name: "dup"
    command: ["echo", "hello"]
    timeout: "not-a-duration"
    concurrency_policy: "later"
    max_output_bytes: -1
    env:
      "": "value"

  - name: "dup"
    command: ["echo"]
    shell_command: "echo shell"
`)

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected load error")
	}

	message := err.Error()
	for _, expected := range []string{
		"server.log_format must be one of: json, text",
		"storage.retention_days must be greater than or equal to 0",
		"storage.max_runs must be greater than or equal to 0",
		"auth.tokens[0].actions[0] references unknown action \"missing-action\"",
		"auth.tokens[0].actions[1] duplicates auth.tokens[0].actions[0] (\"missing-action\")",
		"auth.tokens[0].actions[2] must not be empty",
		"actions[0].timeout must be a valid duration:",
		"actions[0].concurrency_policy must be one of: allow, queue, reject",
		"actions[0].max_output_bytes must be greater than or equal to 0",
		"actions[0].env contains an empty key",
		"actions[1].name duplicates actions[0].name (\"dup\")",
		"actions[1] must define exactly one of command or shell_command",
	} {
		if !strings.Contains(message, expected) {
			t.Fatalf("expected error to contain %q, got %q", expected, message)
		}
	}
}

func TestLoadRejectsMalformedSections(t *testing.T) {
	configPath := writeConfigFile(t, `
server:
  listen: "127.0.0.1:9464"

auth:
  tokens: "not-a-list"

storage:
  path: "/tmp/microhook.db"
`)

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected load error")
	}

	if !strings.Contains(err.Error(), "parse config") {
		t.Fatalf("expected parse error, got %q", err.Error())
	}

	if !strings.Contains(err.Error(), "cannot unmarshal !!str") {
		t.Fatalf("expected YAML type error, got %q", err.Error())
	}
}

func writeConfigFile(t *testing.T, contents string) string {
	t.Helper()

	configPath := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(configPath, []byte(strings.TrimSpace(contents)+"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	return configPath
}
