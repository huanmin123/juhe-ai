package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"
)

// workerSmokeTestEnv 构造隔离的 SQLite 目录与可通过存储门禁的完整 worker env。
func workerSmokeTestEnv(t *testing.T) map[string]string {
	t.Helper()
	root := t.TempDir()
	return map[string]string{
		"JUHE_AI_POSTGRES_URL":                    "postgres://smoke-test.invalid/juhe_ai_smoke",
		"JUHE_AI_INSTANCE_ID":                     "smoke-instance",
		"JUHE_AI_WORKER_ROLE":                     "stats-worker",
		"JUHE_AI_SECRET":                          "0123456789abcdef0123456789abcdef",
		"JUHE_AI_JOBS_DRAIN_TIMEOUT_MS":           "5000",
		"JUHE_AI_DATASET_DATABASE_PATH":           filepath.Join(root, "dataset.sqlite3"),
		"JUHE_AI_CHAT_DATABASE_PATH":              filepath.Join(root, "chat.sqlite3"),
		"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT":  filepath.Join(root, "codex-state"),
		"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT": "1",
		"JUHE_AI_CHAT_ASSETS_ROOT":                filepath.Join(root, "chat-assets"),
		"JUHE_AI_CODEX_CONTEXT_ROOT":              filepath.Join(root, "codex-context"),
	}
}

func getenvFrom(env map[string]string) func(string) string {
	return func(name string) string { return env[name] }
}

// TestWorkerConfigGatesFailsClosed 验证零配置语义：空 env（sqlite 路径按
// DATA_DIR 派生、SECRET 回退开发密钥）可成功加载；postgres 缺连接串与
// production 空 SECRET 仍 fail closed。
func TestWorkerConfigGatesFailsClosed(t *testing.T) {
	config, err := loadWorkerConfig(getenvFrom(map[string]string{}))
	if err != nil {
		t.Fatalf("空 env 零配置必须可加载 worker 配置: %v", err)
	}
	if config.Secret != "juhe-ai-dev-secret-change-me" {
		t.Fatalf("非生产空 SECRET 应回退开发密钥（与 gateway defaultRuntimeSecret 同值）: %q", config.Secret)
	}
	if _, err := loadWorkerConfig(getenvFrom(map[string]string{})); err == nil || !strings.Contains(err.Error(), "JUHE_AI_POSTGRES_URL") {
		t.Fatalf("缺 JUHE_AI_POSTGRES_URL 必须 fail closed（sqlite 零配置已退役）: %v", err)
	}
	if _, err := loadWorkerConfig(getenvFrom(map[string]string{
		"NODE_ENV": "production",
	})); err == nil || !strings.Contains(err.Error(), "JUHE_AI_SECRET") {
		t.Fatalf("production 空 JUHE_AI_SECRET 必须 fail closed: %v", err)
	}
	explicit, err := loadWorkerConfig(getenvFrom(map[string]string{"JUHE_AI_SECRET": "explicit-secret"}))
	if err != nil || explicit.Secret != "explicit-secret" {
		t.Fatalf("显式 JUHE_AI_SECRET 必须优先: err=%v secret=%q", err, explicit.Secret)
	}
}

// TestWorkerAssemblySchedulerAndOutboxPrunerWiring：基础 assembly 恒分配
// scheduler；outbox 消费面恒装配 drain 与 pruner（J1 恒开终态）。
func TestWorkerAssemblySchedulerAndOutboxPrunerWiring(t *testing.T) {
	// PG 唯一形态：outbox face 惰性装配（原 sqlite 库文件 schema 断言随
	// sqlite 形态退役删除；EnsureSchema 真实执行由 w16j PG outbox 臂覆盖）。
	assembly := newWorkerAssembly(workerConfig{}, nil)
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
