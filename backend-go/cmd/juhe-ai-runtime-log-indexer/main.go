package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/huanminabc/juhe-ai/backend-go/internal/runtimelog"
)

func main() {
	config, err := runtimelog.LoadConfig(os.Getenv)
	if err != nil {
		fatal(err)
	}
	store, err := runtimelog.OpenStore(context.Background(), config)
	if err != nil {
		fatal(err)
	}
	defer store.Close()

	if err := runtimelog.EnsureSchema(context.Background(), store); err != nil {
		fatal(fmt.Errorf("运行日志索引 schema 初始化失败: %w", err))
	}
	if err := store.CheckSchema(context.Background()); err != nil {
		fatal(fmt.Errorf("运行日志索引 schema 验证失败: %w", err))
	}
	if len(os.Args) == 2 && os.Args[1] == "--migrate-legacy-sqlite" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		fatalIf(runtimelog.RunWithOwnerLease(ctx, config, store, func(ownerCtx context.Context) error {
			return runtimelog.MigrateLegacySQLite(ownerCtx, config, store)
		}))
		fmt.Fprintln(os.Stdout, "旧运行日志 SQLite 数据迁移和完整性校验完成")
		return
	}
	if len(os.Args) > 1 {
		fatal(fmt.Errorf("不支持的运行日志索引参数 %q", os.Args[1]))
	}

	indexer := runtimelog.NewIndexer(config, store)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fatalIf(runtimelog.RunWithOwnerLease(ctx, config, store, func(ownerCtx context.Context) error {
		if config.Once {
			if err := indexer.RunOnce(ownerCtx); err != nil {
				return err
			}
			return indexer.RunRetention(ownerCtx)
		}
		return indexer.Run(ownerCtx)
	}))
}

func fatalIf(err error) {
	if err != nil && !errors.Is(err, context.Canceled) {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
