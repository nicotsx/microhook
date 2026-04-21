// Package cli provides the command-line interface for Microhook.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nicotsx/microhook/internal/app"
	"github.com/nicotsx/microhook/internal/auth/tokenformat"
	"github.com/nicotsx/microhook/internal/buildinfo"
	"github.com/nicotsx/microhook/internal/config"
)

const shutdownTimeout = 10 * time.Second

type Runner struct {
	stdout io.Writer
	stderr io.Writer
	build  func() buildinfo.Info
}

func Execute(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	runner := Runner{
		stdout: stdout,
		stderr: stderr,
		build:  buildinfo.Current,
	}

	return runner.Run(ctx, args)
}

func (r Runner) Run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		if err := r.printUsage(); err != nil {
			return 1
		}
		return 2
	}

	switch args[0] {
	case "serve":
		return r.runServe(ctx, args[1:])
	case "validate-config":
		return r.runValidateConfig(args[1:])
	case "generate-token":
		return r.runGenerateToken(args[1:])
	case "version":
		return r.runVersion(args[1:])
	case "help", "-h", "--help":
		if err := r.printUsage(); err != nil {
			return 1
		}
		return 0
	default:
		if err := r.writef(r.stderr, "unknown command %q\n\n", args[0]); err != nil {
			return 1
		}
		if err := r.printUsage(); err != nil {
			return 1
		}
		return 2
	}
}

func (r Runner) runServe(ctx context.Context, args []string) int {
	flagSet := newFlagSet("serve", r.stderr)
	configPath := flagSet.String("config", "", "Path to the config file")

	if err := flagSet.Parse(args); err != nil {
		return flagExitCode(err)
	}

	if flagSet.NArg() != 0 {
		if err := r.writef(r.stderr, "serve does not accept positional arguments: %s\n", strings.Join(flagSet.Args(), " ")); err != nil {
			return 1
		}
		return 2
	}

	resolvedConfigPath := config.ResolvePath(*configPath)
	cfg, err := config.Load(resolvedConfigPath)
	if err != nil {
		if writeErr := r.writeln(r.stderr, err); writeErr != nil {
			return 1
		}
		return 1
	}

	application, err := app.Bootstrap(ctx, cfg, r.build())
	if err != nil {
		if writeErr := r.writef(r.stderr, "bootstrap service: %v\n", err); writeErr != nil {
			return 1
		}
		return 1
	}

	listener, err := net.Listen("tcp", cfg.Server.Listen)
	if err != nil {
		if closeErr := application.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close service after listen failure: %w", closeErr))
		}
		if writeErr := r.writef(r.stderr, "listen on %q: %v\n", cfg.Server.Listen, err); writeErr != nil {
			return 1
		}
		return 1
	}

	serveErrs := make(chan error, 1)
	go func() {
		serveErrs <- application.Serve(listener)
	}()

	serveCtx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	select {
	case err := <-serveErrs:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			if closeErr := application.Close(); closeErr != nil {
				err = errors.Join(err, fmt.Errorf("close service after serve failure: %w", closeErr))
			}
			if writeErr := r.writef(r.stderr, "serve: %v\n", err); writeErr != nil {
				return 1
			}
			return 1
		}

		if err := application.Close(); err != nil {
			if writeErr := r.writef(r.stderr, "close service: %v\n", err); writeErr != nil {
				return 1
			}
			return 1
		}

		return 0
	case <-serveCtx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	shutdownErr := application.Shutdown(shutdownCtx)
	serveErr := <-serveErrs

	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		if writeErr := r.writef(r.stderr, "serve: %v\n", serveErr); writeErr != nil {
			return 1
		}
		return 1
	}

	if shutdownErr != nil {
		if writeErr := r.writef(r.stderr, "shutdown service: %v\n", shutdownErr); writeErr != nil {
			return 1
		}
		return 1
	}

	return 0
}

func (r Runner) runValidateConfig(args []string) int {
	flagSet := newFlagSet("validate-config", r.stderr)
	configPath := flagSet.String("config", "", "Path to the config file")

	if err := flagSet.Parse(args); err != nil {
		return flagExitCode(err)
	}

	if flagSet.NArg() != 0 {
		if err := r.writef(r.stderr, "validate-config does not accept positional arguments: %s\n", strings.Join(flagSet.Args(), " ")); err != nil {
			return 1
		}
		return 2
	}

	resolvedConfigPath := config.ResolvePath(*configPath)
	if _, err := config.Load(resolvedConfigPath); err != nil {
		if writeErr := r.writeln(r.stderr, err); writeErr != nil {
			return 1
		}
		return 1
	}

	if err := r.writef(r.stdout, "config is valid: %s\n", resolvedConfigPath); err != nil {
		return 1
	}
	return 0
}

func (r Runner) runGenerateToken(args []string) int {
	flagSet := newFlagSet("generate-token", r.stderr)
	if err := flagSet.Parse(args); err != nil {
		return flagExitCode(err)
	}

	if flagSet.NArg() != 0 {
		if err := r.writef(r.stderr, "generate-token does not accept positional arguments: %s\n", strings.Join(flagSet.Args(), " ")); err != nil {
			return 1
		}
		return 2
	}

	token, err := tokenformat.Generate()
	if err != nil {
		if writeErr := r.writef(r.stderr, "generate token: %v\n", err); writeErr != nil {
			return 1
		}
		return 1
	}

	if err := r.writeln(r.stdout, token); err != nil {
		return 1
	}

	return 0
}

func (r Runner) runVersion(args []string) int {
	flagSet := newFlagSet("version", r.stderr)
	if err := flagSet.Parse(args); err != nil {
		return flagExitCode(err)
	}

	if flagSet.NArg() != 0 {
		if err := r.writef(r.stderr, "version does not accept positional arguments: %s\n", strings.Join(flagSet.Args(), " ")); err != nil {
			return 1
		}
		return 2
	}

	for _, line := range r.build().Lines() {
		if err := r.writeln(r.stdout, line); err != nil {
			return 1
		}
	}

	return 0
}

func (r Runner) printUsage() error {
	for _, line := range []string{
		"Usage: microhook <command> [flags]",
		"",
		"Commands:",
		"  serve             Start the Microhook service",
		"  generate-token    Print a new bearer token",
		"  validate-config   Validate the Microhook config file",
		"  version           Print build metadata",
	} {
		if err := r.writeln(r.stderr, line); err != nil {
			return err
		}
	}

	return nil
}

func newFlagSet(name string, output io.Writer) *flag.FlagSet {
	flagSet := flag.NewFlagSet(name, flag.ContinueOnError)
	flagSet.SetOutput(output)
	return flagSet
}

func flagExitCode(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}

	return 2
}

func (r Runner) writef(writer io.Writer, format string, args ...any) error {
	_, err := fmt.Fprintf(writer, format, args...)
	return err
}

func (r Runner) writeln(writer io.Writer, args ...any) error {
	_, err := fmt.Fprintln(writer, args...)
	return err
}
