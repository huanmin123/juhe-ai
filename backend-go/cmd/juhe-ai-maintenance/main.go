package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"juhe-ai/backend-go/internal/config"
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

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
