package main

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"
)

// workerSmokeTestEnv 构造可通过存储门禁的完整 worker env（PG + Redis 单形态；
// PG 连接串指向保留 TLD .invalid，配置装载只做形态校验不真正连接）。
func workerSmokeTestEnv(t *testing.T) map[string]string {
	t.Helper()
	root := t.TempDir()
	return map[string]string{
		"JUHE_AI_POSTGRES_URL":          "postgres://smoke-test.invalid/juhe_ai_smoke",
		"JUHE_AI_INSTANCE_ID":           "smoke-instance",
		"JUHE_AI_WORKER_ROLE":           "stats-worker",
		"JUHE_AI_SECRET":                "0123456789abcdef0123456789abcdef",
		"JUHE_AI_JOBS_DRAIN_TIMEOUT_MS": "5000",
		"JUHE_AI_CHAT_ASSETS_ROOT":      filepath.Join(root, "chat-assets"),
		"JUHE_AI_CODEX_CONTEXT_ROOT":    filepath.Join(root, "codex-context"),
	}
}

func getenvFrom(env map[string]string) func(string) string {
	return func(name string) string { return env[name] }
}

// TestWorkerConfigGatesFailsClosed 验证 PG 唯一形态的配置门禁：
// JUHE_AI_POSTGRES_URL 必填（worker 任务族强制常开），零配置直接 fail
// closed（原 sqlite 零配置可加载契约随 sqlite 形态退役）；非生产缺 SECRET
// 回退开发密钥，production 缺 SECRET 仍 fail closed。
func TestWorkerConfigGatesFailsClosed(t *testing.T) {
	if _, err := loadWorkerConfig(getenvFrom(map[string]string{})); err == nil || !strings.Contains(err.Error(), "JUHE_AI_POSTGRES_URL") {
		t.Fatalf("空 env 缺 JUHE_AI_POSTGRES_URL 必须 fail closed: %v", err)
	}
	// 非生产缺 SECRET 回退开发密钥（与 gateway defaultRuntimeSecret 同值）。
	config, err := loadWorkerConfig(getenvFrom(map[string]string{"JUHE_AI_POSTGRES_URL": "postgres://smoke-test.invalid/juhe_ai_smoke"}))
	if err != nil {
		t.Fatalf("仅缺 SECRET 必须回退开发密钥: %v", err)
	}
	if config.Secret != "juhe-ai-dev-secret-change-me" {
		t.Fatalf("非生产空 SECRET 应回退开发密钥: %q", config.Secret)
	}
	if _, err := loadWorkerConfig(getenvFrom(map[string]string{
		"JUHE_AI_POSTGRES_URL": "postgres://smoke-test.invalid/juhe_ai_smoke",
		"NODE_ENV":             "production",
	})); err == nil || !strings.Contains(err.Error(), "JUHE_AI_SECRET") {
		t.Fatalf("production 空 JUHE_AI_SECRET 必须 fail closed: %v", err)
	}
	explicit, err := loadWorkerConfig(getenvFrom(map[string]string{
		"JUHE_AI_POSTGRES_URL": "postgres://smoke-test.invalid/juhe_ai_smoke",
		"JUHE_AI_SECRET":       "explicit-secret",
	}))
	if err != nil || explicit.Secret != "explicit-secret" {
		t.Fatalf("显式 JUHE_AI_SECRET 必须优先: err=%v secret=%q", err, explicit.Secret)
	}
}

// TestWorkerAssemblySchedulerAndOutboxPrunerWiring：基础 assembly 恒分配
// scheduler；outbox 消费面恒装配 drain 与 pruner（J1 恒开终态）。
func TestWorkerAssemblySchedulerAndOutboxPrunerWiring(t *testing.T) {
	// PG 唯一形态：outbox face 装配需真实 PG 库执行 EnsureSchema（原 sqlite
	// 库文件 schema 断言随 sqlite 形态退役删除；outbox 消费语义由 w16j PG 臂
	// 另覆盖）。
	_, dsn := wgOpenPGDialectDB(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.Exec(`CREATE SCHEMA IF NOT EXISTS juhe_business`)
		return err
	})
	assembly := newWorkerAssembly(workerConfig{PostgresURL: dsn, PostgresMaxOpenConns: 50, PostgresMaxIdleConns: 10}, nil)
	if assembly == nil || assembly.scheduler == nil {
		t.Fatal("worker assembly must allocate its scheduler")
	}
	defer assembly.closeStores()
	face, err := assembly.wireHealthProbeOutboxFace(func(string) string { return "" })
	if err != nil {
		t.Fatalf("wire health probe outbox face: %v", err)
	}
	if face == nil || face.pruner == nil || face.drain == nil {
		t.Fatal("worker assembly must expose the health probe outbox drain and pruner")
	}
}

func TestInternalDispatchRouteAbsent(t *testing.T) {
	handler := jobsHTTPHandler(jobsHTTPDeps{OwnerMode: ownermode.Active, RuntimeRunning: &atomic.Bool{}})
	for _, path := range []string{
		"/__aiinternal__/v1/account-health-check/dispatch",
		"/__aiinternal__/v1/account-test/dispatch",
		"/__aiinternal__",
	} {
		record := httptest.NewRecorder()
		handler.ServeHTTP(record, httptest.NewRequest(http.MethodPost, path, nil))
		if record.Code != http.StatusNotFound {
			t.Fatalf("%s must be gone (404), got %d", path, record.Code)
		}
	}
}
