// juhe-ai-audit-log-writer currently validates and owns the F3 persistence
// foundation. HTTP input, hot search, retention, and Node cutover are later
// F3 stages and are intentionally not started here.
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
	slog.New(slog.NewJSONHandler(os.Stdout, nil)).Info("F3 原始审计持久化 foundation 已就绪；未启动 HTTP 输入、Node 接线、热搜索或保留清理", "mode", cfg.Mode, "instanceID", cfg.InstanceID)
}

func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
