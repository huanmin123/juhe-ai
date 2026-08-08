package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go/internal/tablemonitor"
)

func main() {
	runOnce := flag.Bool("once", false, "仅执行一次采样和保留清理")
	flag.Parse()
	cfg, err := tablemonitor.LoadConfig(os.Getenv)
	if err != nil {
		fail(err)
	}
	store, err := tablemonitor.OpenStore(cfg)
	if err != nil {
		fail(err)
	}
	defer store.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := store.Ping(ctx); err != nil {
		fail(fmt.Errorf("表监控 Store 不可用: %w", err))
	}
	if err := store.EnsureSchema(ctx); err != nil {
		fail(fmt.Errorf("表监控 schema 初始化失败: %w", err))
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	run := func() error {
		result, err := tablemonitor.RunOnce(ctx, cfg, store, time.Now().UTC())
		if err != nil {
			return err
		}
		logger.Info("表存储监控采样完成", "sampledAt", result.SampledAt.Format(time.RFC3339Nano), "databaseSnapshots", result.DatabaseSnapshots, "tableSnapshots", result.TableSnapshots, "deletedSnapshots", result.DeletedSnapshots)
		return nil
	}
	if *runOnce {
		if err := run(); err != nil {
			fail(err)
		}
		return
	}
	logger.Info("Go 表存储监控 worker 启动", "interval", cfg.Interval.String(), "retentionDays", cfg.RetentionDays, "maxTables", cfg.MaxTables)
	if err := run(); err != nil {
		logger.Error("表存储监控采样失败", "error", err)
	}
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := run(); err != nil {
				logger.Error("表存储监控采样失败", "error", err)
			}
		}
	}
}

func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
