package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
