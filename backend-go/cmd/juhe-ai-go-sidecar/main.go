package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/huanminabc/juhe-ai/backend-go/internal/auditlog"
	"github.com/huanminabc/juhe-ai/backend-go/internal/runtimelog"
	"github.com/huanminabc/juhe-ai/backend-go/internal/supervisor"
)

func main() {
	defer func() {
		if recovered := recover(); recovered != nil {
			fmt.Fprintf(os.Stderr, "juhe-ai-go-sidecar fatal panic: %v\n%s", recovered, debug.Stack())
			os.Exit(1)
		}
	}()
	once := flag.Bool("once", false, "unsupported for the persistent sidecar")
	runtimeLegacyMigration := flag.Bool("migrate-runtime-log-legacy-sqlite", false, "offline F1 legacy SQLite migration")
	auditLegacyMigration := flag.Bool("migrate-audit-log-legacy-sqlite", false, "offline F3 legacy SQLite migration")
	var auditMigration auditlog.LegacyMigrationOptions
	flag.StringVar(&auditMigration.SourceDatabasePath, "source-db", "", "legacy Node audit SQLite database")
	flag.StringVar(&auditMigration.TargetDatabasePath, "target-db", "", "dedicated Go F3 SQLite database")
	flag.StringVar(&auditMigration.SourceBlobDirectory, "source-blob-dir", "", "legacy audit blob directory")
	flag.StringVar(&auditMigration.TargetBlobDirectory, "target-blob-dir", "", "dedicated Go F3 blob directory")
	flag.BoolVar(&auditMigration.NodeStopped, "node-stopped", false, "confirm Node is stopped for the offline migration")
	flag.BoolVar(&auditMigration.GoStopped, "go-stopped", false, "confirm all Go sidecars are stopped for the offline migration")
	flag.Parse()
	if (*runtimeLegacyMigration && *auditLegacyMigration) || (*once && (*runtimeLegacyMigration || *auditLegacyMigration)) {
		fmt.Fprintln(os.Stderr, "--once and the two offline migration modes are mutually exclusive")
		os.Exit(2)
	}
	if *runtimeLegacyMigration {
		runRuntimeLegacyMigration()
		return
	}
	if *auditLegacyMigration {
		runAuditLegacyMigration(auditMigration)
		return
	}
	if *once {
		fmt.Fprintln(os.Stderr, "--once is not supported by juhe-ai-go-sidecar because F3 is a persistent input server; use an explicit offline command")
		os.Exit(2)
	}
	if flag.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "unsupported sidecar arguments: %v\n", flag.Args())
		os.Exit(2)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	startupCtx := context.Background()
	sidecar, err := supervisor.New(startupCtx, os.Getenv, logger)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := sidecar.Run(ctx, logger); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runRuntimeLegacyMigration() {
	config, err := runtimelog.LoadConfig(os.Getenv)
	if err != nil {
		fail(err)
	}
	store, err := runtimelog.OpenStore(context.Background(), config)
	if err != nil {
		fail(err)
	}
	defer store.Close()
	if err := runtimelog.EnsureSchema(context.Background(), store); err != nil {
		fail(fmt.Errorf("运行日志索引 schema 初始化失败: %w", err))
	}
	if err := store.CheckSchema(context.Background()); err != nil {
		fail(fmt.Errorf("运行日志索引 schema 验证失败: %w", err))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runtimelog.RunWithOwnerLease(ctx, config, store, func(ownerCtx context.Context) error {
		return runtimelog.MigrateLegacySQLite(ownerCtx, config, store)
	}); err != nil {
		fail(err)
	}
	fmt.Fprintln(os.Stdout, "旧运行日志 SQLite 数据迁移和完整性校验完成")
}

func runAuditLegacyMigration(options auditlog.LegacyMigrationOptions) {
	result, err := auditlog.MigrateLegacySQLite(context.Background(), options)
	if err != nil {
		fail(fmt.Errorf("F3 审计 SQLite 迁移失败: %w", err))
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fail(fmt.Errorf("序列化迁移结果失败: %w", err))
	}
	fmt.Println(string(encoded))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
