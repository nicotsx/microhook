// Package cli provides the command-line interface for Microhook.
package cli

import (
	"context"
	"errors"
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
	urfavecli "github.com/urfave/cli/v3"
)

const (
	shutdownTimeout  = 10 * time.Second
	rootHelpTemplate = `Usage: {{.Name}} <command> [flags]

Commands:{{range .VisibleCommands}}
  {{printf "%-17s" .Name}} {{.Usage}}{{end}}
`
)

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
	root := r.command()
	err := root.Run(ctx, append([]string{root.Name}, args...))
	return commandExitCode(err)
}

func (r Runner) command() *urfavecli.Command {
	onUsageError := func(ctx context.Context, cmd *urfavecli.Command, err error, _ bool) error {
		if writeErr := r.writef(r.stderr, "Incorrect Usage: %s\n\n", err); writeErr != nil {
			return writeErr
		}

		if helpErr := showCommandHelp(ctx, cmd); helpErr != nil {
			return helpErr
		}

		return exit(2)
	}

	return &urfavecli.Command{
		Name:                          "microhook",
		Writer:                        r.stderr,
		ErrWriter:                     r.stderr,
		HideHelpCommand:               true,
		HideVersion:                   true,
		CustomRootCommandHelpTemplate: rootHelpTemplate,
		ExitErrHandler:                func(context.Context, *urfavecli.Command, error) {},
		OnUsageError:                  onUsageError,
		Action:                        r.runRoot,
		Commands: []*urfavecli.Command{
			{
				Name:         "serve",
				Usage:        "Start the Microhook service",
				OnUsageError: onUsageError,
				Flags: []urfavecli.Flag{
					&urfavecli.StringFlag{Name: "config", Usage: "Path to the config file"},
				},
				Action: r.runServe,
			},
			{
				Name:         "generate-token",
				Usage:        "Print a new bearer token",
				OnUsageError: onUsageError,
				Action:       r.runGenerateToken,
			},
			{
				Name:         "validate-config",
				Usage:        "Validate the Microhook config file",
				OnUsageError: onUsageError,
				Flags: []urfavecli.Flag{
					&urfavecli.StringFlag{Name: "config", Usage: "Path to the config file"},
				},
				Action: r.runValidateConfig,
			},
			{
				Name:         "version",
				Usage:        "Print build metadata",
				OnUsageError: onUsageError,
				Action:       r.runVersion,
			},
		},
	}
}

func (r Runner) runRoot(_ context.Context, cmd *urfavecli.Command) error {
	if cmd.NArg() == 0 {
		if err := urfavecli.ShowRootCommandHelp(cmd); err != nil {
			return err
		}
		return exit(2)
	}

	if err := r.writef(r.stderr, "unknown command %q\n\n", cmd.Args().First()); err != nil {
		return err
	}

	if err := urfavecli.ShowRootCommandHelp(cmd); err != nil {
		return err
	}

	return exit(2)
}

func (r Runner) runServe(ctx context.Context, cmd *urfavecli.Command) error {
	if err := r.rejectPositionalArgs("serve", cmd); err != nil {
		return err
	}

	resolvedConfigPath := config.ResolvePath(cmd.String("config"))
	cfg, err := config.Load(resolvedConfigPath)
	if err != nil {
		if writeErr := r.writeln(r.stderr, err); writeErr != nil {
			return writeErr
		}
		return exit(1)
	}

	application, err := app.Bootstrap(ctx, cfg, r.build())
	if err != nil {
		if writeErr := r.writef(r.stderr, "bootstrap service: %v\n", err); writeErr != nil {
			return writeErr
		}
		return exit(1)
	}

	listener, err := net.Listen("tcp", cfg.Server.Listen)
	if err != nil {
		if closeErr := application.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close service after listen failure: %w", closeErr))
		}
		if writeErr := r.writef(r.stderr, "listen on %q: %v\n", cfg.Server.Listen, err); writeErr != nil {
			return writeErr
		}
		return exit(1)
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
				return writeErr
			}
			return exit(1)
		}

		if err := application.Close(); err != nil {
			if writeErr := r.writef(r.stderr, "close service: %v\n", err); writeErr != nil {
				return writeErr
			}
			return exit(1)
		}

		return nil
	case <-serveCtx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	shutdownErr := application.Shutdown(shutdownCtx)
	serveErr := <-serveErrs

	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		if writeErr := r.writef(r.stderr, "serve: %v\n", serveErr); writeErr != nil {
			return writeErr
		}
		return exit(1)
	}

	if shutdownErr != nil {
		if writeErr := r.writef(r.stderr, "shutdown service: %v\n", shutdownErr); writeErr != nil {
			return writeErr
		}
		return exit(1)
	}

	return nil
}

func (r Runner) runValidateConfig(_ context.Context, cmd *urfavecli.Command) error {
	if err := r.rejectPositionalArgs("validate-config", cmd); err != nil {
		return err
	}

	resolvedConfigPath := config.ResolvePath(cmd.String("config"))
	if _, err := config.Load(resolvedConfigPath); err != nil {
		if writeErr := r.writeln(r.stderr, err); writeErr != nil {
			return writeErr
		}
		return exit(1)
	}

	if err := r.writef(r.stdout, "config is valid: %s\n", resolvedConfigPath); err != nil {
		return err
	}

	return nil
}

func (r Runner) runGenerateToken(_ context.Context, cmd *urfavecli.Command) error {
	if err := r.rejectPositionalArgs("generate-token", cmd); err != nil {
		return err
	}

	token, err := tokenformat.Generate()
	if err != nil {
		if writeErr := r.writef(r.stderr, "generate token: %v\n", err); writeErr != nil {
			return writeErr
		}
		return exit(1)
	}

	if err := r.writeln(r.stdout, token); err != nil {
		return err
	}

	return nil
}

func (r Runner) runVersion(_ context.Context, cmd *urfavecli.Command) error {
	if err := r.rejectPositionalArgs("version", cmd); err != nil {
		return err
	}

	for _, line := range r.build().Lines() {
		if err := r.writeln(r.stdout, line); err != nil {
			return err
		}
	}

	return nil
}

func (r Runner) rejectPositionalArgs(command string, cmd *urfavecli.Command) error {
	if cmd.NArg() == 0 {
		return nil
	}

	if err := r.writef(r.stderr, "%s does not accept positional arguments: %s\n", command, strings.Join(cmd.Args().Slice(), " ")); err != nil {
		return err
	}

	return exit(2)
}

func showCommandHelp(ctx context.Context, cmd *urfavecli.Command) error {
	if cmd == cmd.Root() {
		return urfavecli.ShowRootCommandHelp(cmd)
	}

	return urfavecli.ShowCommandHelp(ctx, cmd.Root(), cmd.Name)
}

func commandExitCode(err error) int {
	if err == nil {
		return 0
	}

	var exitErr urfavecli.ExitCoder
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}

	return 1
}

func exit(code int) error {
	return urfavecli.Exit("", code)
}

func (r Runner) writef(writer io.Writer, format string, args ...any) error {
	_, err := fmt.Fprintf(writer, format, args...)
	return err
}

func (r Runner) writeln(writer io.Writer, args ...any) error {
	_, err := fmt.Fprintln(writer, args...)
	return err
}
