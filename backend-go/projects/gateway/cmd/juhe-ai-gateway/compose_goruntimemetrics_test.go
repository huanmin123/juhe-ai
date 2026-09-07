package main

// 去跨进程战役第三刀装配测试：gateway 进程内 Go runtime metrics 采样器的
// 生命周期——store 未启用（默认）时组合根不装配采样器；启用时采样器带着
// 配置的 interval/retention 装配、store 句柄进入 shutdowns，组合根 Shutdown
// 后采样器仍可独立停止（supervisor Run 由 main 挂载，ctx 停止单元在共享包
// gometrics 测试覆盖）。

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
	"github.com/huanminabc/juhe-ai/backend-go-platform/gometrics"
)

func TestComposeKeepsGoRuntimeSamplerUnassembledByDefault(t *testing.T) {
	cfg := composeTestConfig(t)
	if cfg.GoRuntimeMetrics.Enabled {
		t.Fatal("composeTestConfig must keep the Go runtime metrics store disabled by default")
	}
	composed := composeForGoRuntimeTest(t, cfg)
	defer composed.Shutdown()
	if composed.GoRuntimeSampler != nil {
		t.Fatal("disabled store must not assemble a Go runtime metrics sampler")
	}
}

func TestComposeAssemblesGoRuntimeSamplerWithConfiguredLifecycle(t *testing.T) {
	cfg := composeTestConfig(t)
	dbPath := filepath.Join(filepath.Dir(cfg.BusinessDatabasePath), "go-runtime-metrics.sqlite3")
	// The sampler checks (never creates) the pre-provisioned schema, mirroring
	// the maintenance-owned provisioning the deployed flow performs.
	provisionGoRuntimeSchema(t, dbPath)
	cfg.GoRuntimeMetrics = gometrics.Config{
		Enabled:       true,
		Store:         gometrics.DialectSQLite,
		DatabasePath:  dbPath,
		Interval:      20 * time.Second,
		RetentionDays: 45,
		Service:       "juhe-ai",
		Role:          "gateway",
	}
	composed := composeForGoRuntimeTest(t, cfg)
	if composed.GoRuntimeSampler == nil {
		t.Fatal("enabled store must assemble the Go runtime metrics sampler")
	}
	if composed.GoRuntimeSampler.Interval != 20*time.Second {
		t.Fatalf("sampler must inherit the configured interval: %v", composed.GoRuntimeSampler.Interval)
	}
	if composed.GoRuntimeSampler.Retention != 45*24*time.Hour {
		t.Fatalf("sampler must inherit the configured retention: %v", composed.GoRuntimeSampler.Retention)
	}
	if composed.GoRuntimeSampler.Collector == nil || composed.GoRuntimeSampler.Collector.Role() != "gateway" || composed.GoRuntimeSampler.Collector.Service() != "juhe-ai" {
		t.Fatalf("sampler collector must carry the gateway identity: %+v", composed.GoRuntimeSampler.Collector)
	}
	// Shutdown closes the sampler store handle via shutdowns; the composition
	// must still shut down cleanly (idempotent, no panic).
	composed.Shutdown()
}

func composeForGoRuntimeTest(t *testing.T, cfg runtimeConfig) *composition {
	t.Helper()
	store := openComposeOperationStore(t)
	auditConfig, auditProducer, closeAudit := openComposeAuditSources(t, filepath.Dir(cfg.DatasetDatabasePath))
	t.Cleanup(closeAudit)
	composed, err := composeSystemAPI(cfg, pgpool.NewRegistry(), store, openComposeOperationLease(t, store), auditProducer, auditConfig)
	if err != nil {
		t.Fatalf("compose system api: %v", err)
	}
	return composed
}

func provisionGoRuntimeSchema(t *testing.T, dbPath string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := gometrics.NewStore(db, gometrics.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
}
