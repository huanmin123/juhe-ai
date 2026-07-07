package cmdroot

import (
	"context"
	"fmt"
	"io"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"juhe-ai/backend-go/internal/app"
	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/logging"
	"juhe-ai/backend-go/internal/version"
)

func New(stdout io.Writer, stderr io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:     "juhe-ai",
		Version: version.Version,
		Short:   "juhe-ai Go backend",
	}
	root.SetOut(stdout)
	root.SetErr(stderr)

	root.AddCommand(newServerCommand())
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), version.Version)
		},
	})

	return root
}

func newServerCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "server",
		Short: "Run HTTP server",
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

			return app.RunServer(ctx, cfg, logger)
		},
	}
}

func ExecuteWithContext(ctx context.Context, stdout io.Writer, stderr io.Writer, args ...string) error {
	root := New(stdout, stderr)
	root.SetArgs(args)
	return root.ExecuteContext(ctx)
}
