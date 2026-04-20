package app

import (
	"context"
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
