package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicotsx/microhook/internal/buildinfo"
	"github.com/nicotsx/microhook/internal/config"
	"github.com/nicotsx/microhook/internal/storage"
)

func TestBootstrapStartsHealthEndpointAndShutsDown(t *testing.T) {
	storagePath := filepath.Join(t.TempDir(), "microhook.db")
	cfg := config.Config{
		Server: config.ServerConfig{
			Listen:    "127.0.0.1:0",
			LogFormat: "json",
		},
		Storage: config.StorageConfig{
			Path: storagePath,
		},
	}

	application, err := Bootstrap(context.Background(), cfg, buildinfo.Current())
	if err != nil {
		t.Fatalf("bootstrap app: %v", err)
	}
	t.Cleanup(func() {
		if err := application.Close(); err != nil {
			t.Errorf("close app: %v", err)
		}
	})

	listener, err := net.Listen("tcp", cfg.Server.Listen)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	serveErrs := make(chan error, 1)
	go func() {
		serveErrs <- application.Serve(listener)
	}()

	client := &http.Client{Timeout: 2 * time.Second}
	healthURL := "http://" + listener.Addr().String() + "/healthz"

	assertEventually(t, 2*time.Second, func() error {
		response, err := client.Get(healthURL)
		if err != nil {
			return err
		}

		body, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil {
			return errors.Join(readErr, closeErr)
		}

		if response.StatusCode != http.StatusOK {
			return errors.New(response.Status)
		}

		if strings.TrimSpace(string(body)) != "ok" {
			return errors.New("unexpected health body")
		}

		return nil
	})

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := application.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown app: %v", err)
	}

	serveErr := <-serveErrs
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		t.Fatalf("serve returned unexpected error: %v", serveErr)
	}

	_, err = client.Get(healthURL)
	if err == nil {
		t.Fatalf("expected health endpoint to stop accepting requests after shutdown")
	}
}

func TestBootstrapFailsWhenStorageCannotInitialize(t *testing.T) {
	badParent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(badParent, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}

	cfg := config.Config{
		Server: config.ServerConfig{
			Listen:    "127.0.0.1:0",
			LogFormat: "json",
		},
		Storage: config.StorageConfig{
			Path: filepath.Join(badParent, "microhook.db"),
		},
	}

	_, err := Bootstrap(context.Background(), cfg, buildinfo.Current())
	if err == nil {
		t.Fatal("expected bootstrap error")
	}

	if !strings.Contains(err.Error(), "create storage directory") {
		t.Fatalf("expected storage initialization error, got %v", err)
	}
}

func TestBootstrapAppliesConfiguredRetentionOnStartup(t *testing.T) {
	ctx := context.Background()
	storagePath := filepath.Join(t.TempDir(), "microhook.db")
	store, err := storage.Open(ctx, storagePath)
	if err != nil {
		t.Fatalf("open seed storage: %v", err)
	}

	oldRunCreatedAt := time.Now().Add(-72 * time.Hour).UTC()
	if _, err := store.CreateRun(ctx, storage.CreateRunParams{
		ID:              "old-run",
		ActionName:      "deploy",
		Status:          storage.RunStatusSucceeded,
		CreatedAt:       oldRunCreatedAt,
		RequestMetadata: json.RawMessage(`{"request_id":"seed"}`),
		ActionSnapshot: storage.ActionSnapshot{
			Description:       "Deploy the service",
			Mode:              "command",
			Command:           []string{"echo", "deploy"},
			Cwd:               "/tmp",
			Timeout:           time.Minute,
			Env:               map[string]string{"ENVIRONMENT": "test"},
			ConcurrencyPolicy: "allow",
			MaxOutputBytes:    1024,
			Enabled:           true,
		},
	}); err != nil {
		t.Fatalf("seed old run: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close seed storage: %v", err)
	}

	cfg := config.Config{
		Server: config.ServerConfig{
			Listen:    "127.0.0.1:0",
			LogFormat: "json",
		},
		Storage: config.StorageConfig{
			Path:          storagePath,
			RetentionDays: 1,
		},
	}

	application, err := Bootstrap(ctx, cfg, buildinfo.Current())
	if err != nil {
		t.Fatalf("bootstrap app: %v", err)
	}
	if err := application.Close(); err != nil {
		t.Fatalf("close app: %v", err)
	}

	store, err = storage.Open(ctx, storagePath)
	if err != nil {
		t.Fatalf("reopen storage after bootstrap: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Errorf("close storage after verification: %v", err)
		}
	}()

	_, err = store.GetRun(ctx, "old-run")
	if !errors.Is(err, storage.ErrRunNotFound) {
		t.Fatalf("expected old run to be pruned during bootstrap, got %v", err)
	}

	prunedAt, err := store.LastRetentionPruneAt(ctx)
	if err != nil {
		t.Fatalf("read retention metadata: %v", err)
	}
	if prunedAt == nil || prunedAt.IsZero() {
		t.Fatal("expected bootstrap retention to update retention metadata")
	}
}

func TestBootstrapCancelsInterruptedRunsOnStartup(t *testing.T) {
	ctx := context.Background()
	storagePath := filepath.Join(t.TempDir(), "microhook.db")
	store, err := storage.Open(ctx, storagePath)
	if err != nil {
		t.Fatalf("open seed storage: %v", err)
	}

	startedAt := time.Date(2026, time.April, 21, 10, 15, 0, 0, time.UTC)
	if _, err := store.CreateRun(ctx, storage.CreateRunParams{
		ID:              "run-interrupted",
		ActionName:      "serial",
		Status:          storage.RunStatusRunning,
		CreatedAt:       startedAt.Add(-2 * time.Second),
		StartedAt:       &startedAt,
		RequestMetadata: json.RawMessage(`{"mode":"async","request_id":"interrupted"}`),
		ActionSnapshot: storage.ActionSnapshot{
			Description:       "Interrupted run",
			Mode:              config.ActionModeCommand,
			Command:           []string{"/bin/sh", "-c", "printf should-not-run"},
			Timeout:           time.Second,
			ConcurrencyPolicy: "reject",
			MaxOutputBytes:    64,
			Enabled:           true,
		},
	}); err != nil {
		t.Fatalf("create interrupted run: %v", err)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("close seed storage: %v", err)
	}

	cfg := config.Config{
		Server: config.ServerConfig{
			Listen:    "127.0.0.1:0",
			LogFormat: "json",
		},
		Storage: config.StorageConfig{
			Path: storagePath,
		},
		Actions: []config.Action{{
			Name:              "serial",
			Mode:              config.ActionModeCommand,
			Command:           []string{"/bin/sh", "-c", "printf registry-definition"},
			Timeout:           time.Second,
			ConcurrencyPolicy: "reject",
			MaxOutputBytes:    64,
			Enabled:           true,
		}},
	}

	application, err := Bootstrap(ctx, cfg, buildinfo.Current())
	if err != nil {
		t.Fatalf("bootstrap app: %v", err)
	}
	defer func() {
		if err := application.Close(); err != nil {
			t.Errorf("close app: %v", err)
		}
	}()

	assertEventually(t, 3*time.Second, func() error {
		run, err := application.storage.GetRun(ctx, "run-interrupted")
		if err != nil {
			return err
		}
		if run.Status != storage.RunStatusCancelled {
			return errors.New("interrupted run not cancelled yet")
		}
		if run.ErrorSummary != "service restarted before run completion" {
			return errors.New("interrupted run summary not recovered yet")
		}

		return nil
	})
}

func assertEventually(t *testing.T, timeout time.Duration, check func() error) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for {
		err := check()
		if err == nil {
			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("condition not met before timeout: %v", err)
		}

		time.Sleep(25 * time.Millisecond)
	}
}
