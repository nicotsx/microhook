package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"

	"github.com/nicotsx/microhook/internal/auth"
	"github.com/nicotsx/microhook/internal/buildinfo"
	"github.com/nicotsx/microhook/internal/config"
	"github.com/nicotsx/microhook/internal/execution"
	"github.com/nicotsx/microhook/internal/httpapi"
	"github.com/nicotsx/microhook/internal/storage"
)

type App struct {
	config   config.Config
	build    buildinfo.Info
	logger   *slog.Logger
	auth     *auth.Service
	executor *execution.Service
	storage  *storage.Store
	http     *httpapi.Server
}

func Bootstrap(ctx context.Context, cfg config.Config, build buildinfo.Info) (*App, error) {
	logger := newLogger(cfg.Server.LogFormat, os.Stderr)

	store, err := storage.Open(ctx, cfg.Storage.Path)
	if err != nil {
		return nil, err
	}

	authService, err := auth.New(cfg.Auth)
	if err != nil {
		if closeErr := store.Close(); closeErr != nil {
			return nil, errors.Join(err, fmt.Errorf("close storage after auth bootstrap failure: %w", closeErr))
		}
		return nil, err
	}

	executorService := execution.New()

	return &App{
		config:   cfg,
		build:    build,
		logger:   logger,
		auth:     authService,
		executor: executorService,
		storage:  store,
		http:     httpapi.New(cfg.Server.Listen, logger, authService),
	}, nil
}

func (a *App) Serve(listener net.Listener) error {
	a.logger.Info(
		"microhook serving",
		"address", listener.Addr().String(),
		"storage_path", a.storage.Path(),
		"version", a.build.Version,
		"commit", a.build.Commit,
	)

	return a.http.Serve(listener)
}

func (a *App) Shutdown(ctx context.Context) error {
	var shutdownErr error

	if err := a.http.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		shutdownErr = errors.Join(shutdownErr, fmt.Errorf("shutdown http server: %w", err))
	}

	return errors.Join(shutdownErr, a.Close())
}

func (a *App) Close() error {
	var closeErr error

	if a.executor != nil {
		closeErr = errors.Join(closeErr, a.executor.Close())
	}

	if a.storage != nil {
		closeErr = errors.Join(closeErr, a.storage.Close())
	}

	return closeErr
}

func (a *App) Logger() *slog.Logger {
	return a.logger
}

func newLogger(format string, writer io.Writer) *slog.Logger {
	options := &slog.HandlerOptions{Level: slog.LevelInfo}
	if format == "text" {
		return slog.New(slog.NewTextHandler(writer, options))
	}

	return slog.New(slog.NewJSONHandler(writer, options))
}
