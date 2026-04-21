package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicotsx/microhook/internal/auth/tokenformat"
	"github.com/nicotsx/microhook/internal/buildinfo"
)

func TestValidateConfigRejectsInvalidConfig(t *testing.T) {
	configPath := writeConfig(t, `
server:
  log_format: json
storage:
  retention_days: 1
`)

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := Execute(context.Background(), []string{"validate-config", "-config", configPath}, &stdout, &stderr)
	if exitCode == 0 {
		t.Fatalf("expected non-zero exit code, got %d", exitCode)
	}

	output := stderr.String()
	if !strings.Contains(output, "storage.path is required") {
		t.Fatalf("expected storage.path validation error, got %q", output)
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected no stdout output, got %q", stdout.String())
	}
}

func TestExecuteWithoutCommandPrintsUsage(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := Execute(context.Background(), nil, &stdout, &stderr)
	if exitCode != 2 {
		t.Fatalf("expected exit code 2, got %d with stderr %q", exitCode, stderr.String())
	}

	if !strings.Contains(stderr.String(), "Usage: microhook <command> [flags]") {
		t.Fatalf("expected usage output, got %q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected no stdout output, got %q", stdout.String())
	}
}

func TestUnknownCommandPrintsUsage(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := Execute(context.Background(), []string{"wat"}, &stdout, &stderr)
	if exitCode != 2 {
		t.Fatalf("expected exit code 2, got %d with stderr %q", exitCode, stderr.String())
	}

	output := stderr.String()
	if !strings.Contains(output, `unknown command "wat"`) {
		t.Fatalf("expected unknown command error, got %q", output)
	}
	if !strings.Contains(output, "Usage: microhook <command> [flags]") {
		t.Fatalf("expected usage output, got %q", output)
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected no stdout output, got %q", stdout.String())
	}
}

func TestCompletionCommandPrintsScriptToStdout(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := Execute(context.Background(), []string{"completion", "bash"}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("expected zero exit code, got %d with stderr %q", exitCode, stderr.String())
	}

	if !strings.Contains(stdout.String(), "#!/bin/bash") {
		t.Fatalf("expected bash completion script, got %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "microhook") {
		t.Fatalf("expected script to reference command name, got %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected no stderr output, got %q", stderr.String())
	}
}

func TestCompletionCommandRequiresShell(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := Execute(context.Background(), []string{"completion"}, &stdout, &stderr)
	if exitCode == 0 {
		t.Fatalf("expected non-zero exit code, got %d", exitCode)
	}

	if !strings.Contains(stderr.String(), "no shell provided for completion command") {
		t.Fatalf("expected missing shell error, got %q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected no stdout output, got %q", stdout.String())
	}
}

func TestGenerateTokenPrintsValidToken(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := Execute(context.Background(), []string{"generate-token"}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("expected zero exit code, got %d with stderr %q", exitCode, stderr.String())
	}

	token := strings.TrimSpace(stdout.String())
	if err := tokenformat.Validate(token); err != nil {
		t.Fatalf("expected generated token to validate: %v", err)
	}

	if stderr.Len() != 0 {
		t.Fatalf("expected no stderr output, got %q", stderr.String())
	}
}

func TestValidateConfigUsesEnvironmentConfigPath(t *testing.T) {
	configPath := writeConfig(t, `
server:
  listen: 127.0.0.1:9464
storage:
  path: `+filepath.ToSlash(filepath.Join(t.TempDir(), "microhook.db"))+`
`)

	t.Setenv("MICROHOOK_CONFIG", configPath)

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := Execute(context.Background(), []string{"validate-config"}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("expected zero exit code, got %d with stderr %q", exitCode, stderr.String())
	}

	if !strings.Contains(stdout.String(), configPath) {
		t.Fatalf("expected stdout to mention resolved config path, got %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected no stderr output, got %q", stderr.String())
	}
}

func TestVersionPrintsBuildMetadata(t *testing.T) {
	originalVersion := buildinfo.Version
	originalCommit := buildinfo.Commit
	originalBuildTime := buildinfo.BuildTime
	originalBuiltBy := buildinfo.BuiltBy
	t.Cleanup(func() {
		buildinfo.Version = originalVersion
		buildinfo.Commit = originalCommit
		buildinfo.BuildTime = originalBuildTime
		buildinfo.BuiltBy = originalBuiltBy
	})

	buildinfo.Version = "1.2.3"
	buildinfo.Commit = "abc1234"
	buildinfo.BuildTime = "2026-04-21T10:15:00Z"
	buildinfo.BuiltBy = "test-suite"

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := Execute(context.Background(), []string{"version"}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("expected zero exit code, got %d with stderr %q", exitCode, stderr.String())
	}

	output := stdout.String()
	for _, expected := range []string{
		"version=1.2.3",
		"commit=abc1234",
		"build_time=2026-04-21T10:15:00Z",
		"built_by=test-suite",
		"go_version=",
		"platform=",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected output to contain %q, got %q", expected, output)
		}
	}

	if stderr.Len() != 0 {
		t.Fatalf("expected no stderr output, got %q", stderr.String())
	}
}

func writeConfig(t *testing.T, contents string) string {
	t.Helper()

	configPath := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(configPath, []byte(strings.TrimSpace(contents)+"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	return configPath
}
