package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/huanminabc/juhe-ai/backend-go/internal/auditlog"
)

func main() {
	cfg, err := auditlog.LoadConfig(os.Getenv)
	if err != nil {
		fail(err)
	}
	inputCfg, err := auditlog.LoadInputServerConfig(os.Getenv)
	if err != nil {
		fail(err)
	}
	store, err := auditlog.OpenStore(cfg)
	if err != nil {
		fail(err)
	}
	defer store.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := store.EnsureSchema(ctx); err != nil {
		fail(err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	logger.Info("F3 原始审计输入 sidecar 启动", "mode", cfg.Mode, "instanceID", cfg.InstanceID, "listenAddress", inputCfg.ListenAddress)
	if err := auditlog.RunInputServer(ctx, store, cfg, inputCfg, logger); err != nil {
		fail(err)
	}
}

func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
