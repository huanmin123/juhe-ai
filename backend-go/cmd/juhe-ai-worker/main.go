package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"juhe-ai/backend-go/internal/app"
	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/logging"
	"juhe-ai/backend-go/internal/version"
)

func main() {
	root := &cobra.Command{
		Use:     "juhe-ai-worker",
		Version: version.Version,
		Short:   "juhe-ai Go worker process",
	}

	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), version.Version)
		},
	})
	root.AddCommand(&cobra.Command{
		Use:   "ingest",
		Short: "Run ingest worker",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.LoadOptions{LoadDotEnv: true})
			if err != nil {
				return err
			}

			logger, err := logging.New(cfg.LogLevel, cmd.ErrOrStderr())
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			return app.RunIngestWorker(ctx, cfg, logger)
		},
	})

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
