package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
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
	root.AddCommand(newStatsSchemaContractPreflightCommand())
	root.AddCommand(newSchemaUpCommand())
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

func newStatsSchemaContractPreflightCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "stats-schema-contract-preflight",
		Short: "Validate Node-owned PostgreSQL stats tables required by Go readers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rawPostgresURL := strings.TrimSpace(os.Getenv("JUHE_AI_POSTGRES_URL"))
			if rawPostgresURL == "" {
				return fmt.Errorf("JUHE_AI_POSTGRES_URL is required")
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			return maintenance.RunStatsSchemaContractPreflight(ctx, rawPostgresURL, cmd.OutOrStdout())
		},
	}
}

func executeCommand(root *cobra.Command, stderr io.Writer) int {
	root.SetErr(stderr)
	root.SilenceErrors = true
	root.SilenceUsage = true
	if err := root.Execute(); err != nil {
		logging.WriteFatal(stderr, err)
		return 1
	}
	return 0
}

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
			return maintenance.RunMigrationCatalogPreflight(ctx, directory, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&directory, "dir", "db/migrations", "migration catalog directory")
	return cmd
}

func newSchemaUpCommand() *cobra.Command {
	var directory string
	cmd := &cobra.Command{
		Use:   "schema-up",
		Short: "Apply the current PostgreSQL migration catalog through Goose",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			return maintenance.RunSchemaUp(
				ctx,
				os.Getenv("JUHE_AI_POSTGRES_URL"),
				directory,
				cmd.OutOrStdout(),
			)
		},
	}
	cmd.Flags().StringVar(&directory, "dir", "db/migrations", "migration catalog directory")
	return cmd
}
