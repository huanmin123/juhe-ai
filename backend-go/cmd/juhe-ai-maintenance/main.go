package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/logging"
	"juhe-ai/backend-go/internal/maintenance"
	"juhe-ai/backend-go/internal/version"
)

func main() {
	root := &cobra.Command{
		Use:     "juhe-ai-maintenance",
		Version: version.Version,
		Short:   "juhe-ai Go maintenance commands",
	}

	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), version.Version)
		},
	})
	root.AddCommand(newMigrationCatalogPreflightCommand())
	root.AddCommand(&cobra.Command{
		Use:   "w0-smoke",
		Short: "Run W0 PostgreSQL, Redis and Asynq smoke checks",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.LoadOptions{LoadDotEnv: true})
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			return maintenance.RunW0Smoke(ctx, cfg, cmd.OutOrStdout())
		},
	})
	root.AddCommand(&cobra.Command{
		Use:   "w1a-public-settings-smoke",
		Short: "Run W1a public settings smoke checks",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.LoadOptions{LoadDotEnv: true})
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			return maintenance.RunW1aPublicSettingsSmoke(ctx, cfg, cmd.OutOrStdout())
		},
	})
	root.AddCommand(&cobra.Command{
		Use:   "w1b-public-api-smoke",
		Short: "Run W1b public API opt-in smoke checks",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.LoadOptions{LoadDotEnv: true})
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			return maintenance.RunW1bPublicAPISmoke(ctx, cfg, cmd.OutOrStdout())
		},
	})
	root.AddCommand(&cobra.Command{
		Use:   "w2-operation-logs-smoke",
		Short: "Run W2 operation logs management API opt-in smoke checks",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.LoadOptions{LoadDotEnv: true})
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			return maintenance.RunW2OperationLogsSmoke(ctx, cfg, cmd.OutOrStdout())
		},
	})

	os.Exit(executeCommand(root, os.Stderr))
}

func executeCommand(root *cobra.Command, stderr io.Writer) int {
	root.SetErr(stderr)
	root.SilenceErrors = true
	root.SilenceUsage = true
	if err := root.Execute(); err != nil {
		var reported reportedCommandError
		if errors.As(err, &reported) {
			return 1
		}
		logging.WriteFatal(stderr, err)
		return 1
	}
	return 0
}

type reportedCommandError struct {
	err error
}

func (e reportedCommandError) Error() string { return e.err.Error() }
func (e reportedCommandError) Unwrap() error { return e.err }

func newMigrationCatalogPreflightCommand() *cobra.Command {
	var directory string
	cmd := &cobra.Command{
		Use:   "migration-catalog-preflight",
		Short: "Validate the migration catalog without connecting to a database",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			err := maintenance.RunMigrationCatalogPreflight(ctx, directory, cmd.OutOrStdout())
			if err != nil {
				return reportedCommandError{err: err}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&directory, "dir", "db/migrations", "migration catalog directory")
	return cmd
}
