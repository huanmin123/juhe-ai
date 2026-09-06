package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/internalapi"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobsched"
)

const healthDispatchTestSecret = "health-dispatch-composition-secret"

// recordingFenceSettler 记录 source fence 结算调用（装配注入点）。
type recordingFenceSettler struct {
	mu     sync.Mutex
	fences []internalapi.HealthCheckSourceFence
	states []string
}

func (s *recordingFenceSettler) settle(_ context.Context, fence internalapi.HealthCheckSourceFence, state string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fences = append(s.fences, fence)
	s.states = append(s.states, state)
	return nil
}

// healthDispatchCompositionEnv 在 workerSmokeTestEnv 基础上追加 J1 配置与
// 输入目录；返回 env 与 J1 输入密钥字节。
func healthDispatchCompositionEnv(t *testing.T) (map[string]string, []byte) {
	t.Helper()
	env := workerSmokeTestEnv(t)
	root := t.TempDir()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	env["JUHE_AI_ACCOUNT_HEALTH_ENABLED"] = "true"
	env["JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER"] = "go"
	env["JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID"] = "health-dispatch-instance"
	env["JUHE_AI_ACCOUNT_HEALTH_STORE"] = "sqlite"
	env["JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH"] = filepath.Join(root, "j1-store.sqlite3")
	env["JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY"] = filepath.Join(root, "j1-input")
	env["JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY"] = base64.RawURLEncoding.EncodeToString(key)
	env["JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET"] = "0123456789abcdef0123456789abcdef"
	return env, key
}

// seedHealthDispatchBusinessTables 建立派发 Boundary 读取的两张业务表并写入
// 一个处于 J1 冻结范围的账户事实。
func seedHealthDispatchBusinessTables(t *testing.T, databasePath, accountID string, configRevision, dispatchRevision, inputVersion int64) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	statements := []string{
		`CREATE TABLE IF NOT EXISTS accounts (
			id TEXT PRIMARY KEY,
			deleted_at TEXT,
			config_revision INTEGER,
			dispatch_revision INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS account_health_jobs_input_versions (
			account_id TEXT PRIMARY KEY,
			current_version INTEGER NOT NULL,
			reserved_at TEXT
		)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if accountID != "" {
		if _, err := db.Exec(`INSERT INTO accounts (id, config_revision, dispatch_revision) VALUES (?, ?, ?)`, accountID, configRevision, dispatchRevision); err != nil {
			t.Fatal(err)
		}
		if inputVersion >= 1 {
			if _, err := db.Exec(`INSERT INTO account_health_jobs_input_versions (account_id, current_version, reserved_at) VALUES (?, ?, ?)`, accountID, inputVersion, "2026-01-01T00:00:00Z"); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// newHealthDispatchAssembly 构造轻量 workerAssembly（仅派发装配所需字段）。
func newHealthDispatchAssembly(env map[string]string) *workerAssembly {
	return &workerAssembly{
		config: workerConfig{
			Driver:             "sqlite",
			BusinessSQLitePath: env["JUHE_AI_DATABASE_PATH"],
			Secret:             healthDispatchTestSecret,
			InternalAPIEnabled: true,
			RedisStateURL:      env["JUHE_AI_REDIS_STATE_URL"],
			RedisNamespace:     env["JUHE_AI_REDIS_NAMESPACE"],
		},
		logger:     slog.Default(),
		wiredTasks: map[string]jobsched.Task{},
	}
}

// postHealthDispatchComposition 以 loopback 地址发起签名派发请求。
func postHealthDispatchComposition(t *testing.T, handler http.Handler, rawBody []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, internalapi.FullHealthCheckDispatchPath(), bytes.NewReader(rawBody))
	request.RemoteAddr = "127.0.0.1:54321"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Juhe-Ai-Signature", internalapi.CreateHealthCheckDispatchSignature(healthDispatchTestSecret, rawBody))
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, request)
	return record
}

func healthDispatchCompositionBody(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	payload := map[string]any{"version": 1, "accountId": "acc-1", "reason": "request_failure"}
	if mutate != nil {
		mutate(payload)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// loadPublishedRequests 经 J1 Runner 的消费入口读取已发布请求文件。
func loadPublishedRequests(t *testing.T, inputDirectory string, key []byte) []accounthealth.ProbeRequest {
	t.Helper()
	requests, err := accounthealth.LoadSignedProbeRequests(inputDirectory, map[string][]byte{"runtime-v1": key})
	if err != nil {
		t.Fatalf("读取已发布 J1 request 失败: %v", err)
	}
	return requests
}

// TestHealthDispatchPublishesSignedRequestFile 覆盖 J1 范围内派发主链：
// 202 受理 → 签名 request 文件落盘（Runner 消费入口可验签读取）→ source
// fence 不结算（等 J1 outcome 回传）。
func TestHealthDispatchPublishesSignedRequestFile(t *testing.T) {
	env, key := healthDispatchCompositionEnv(t)
	seedHealthDispatchBusinessTables(t, env["JUHE_AI_DATABASE_PATH"], "acc-1", 3, 5, 2)
	settler := &recordingFenceSettler{}
	assembly := newHealthDispatchAssembly(env)
	assembly.healthFenceSettler = settler.settle
	defer assembly.closeStores()

	options := assembly.healthCheckDispatchOptions(getenvFrom(env))
	if options.Dispatch == nil {
		t.Fatal("J1 配置齐备时派发能力必须装配")
	}
	record := postHealthDispatchComposition(t, internalapi.NewHealthCheckDispatchHandler(options), healthDispatchCompositionBody(t, nil))
	if record.Code != http.StatusAccepted {
		t.Fatalf("J1 范围内派发必须 202: %d body=%s", record.Code, record.Body.String())
	}
	requests := loadPublishedRequests(t, env["JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY"], key)
	if len(requests) != 1 {
		t.Fatalf("必须恰好发布一个 request 文件: %d", len(requests))
	}
	request := requests[0]
	if request.AccountID != "acc-1" || request.Reason != "request_failure" {
		t.Fatalf("request 账户/原因不一致: %+v", request)
	}
	if request.InputVersion != 2 || request.ConfigRevision != 3 || request.DispatchRevision != 5 {
		t.Fatalf("request revision 投影不一致: %+v", request)
	}
	if !request.MutateAccount {
		t.Fatal("无 source fence 的派发必须 mutate_account=true")
	}
	if !request.Deadline.After(time.Now().Add(30 * time.Second)) {
		t.Fatalf("request deadline 必须在未来: %s", request.Deadline)
	}
	if len(settler.fences) != 0 {
		t.Fatalf("发布成功不得结算 source fence: %+v", settler.fences)
	}
}

// TestHealthDispatchWithSourceFencePublishesFencedRequest 覆盖带 fence 的
// 探针信封派发：request 内嵌 source_fence 投影且 mutate_account=false。
func TestHealthDispatchWithSourceFencePublishesFencedRequest(t *testing.T) {
	env, key := healthDispatchCompositionEnv(t)
	seedHealthDispatchBusinessTables(t, env["JUHE_AI_DATABASE_PATH"], "acc-1", 3, 5, 2)
	settler := &recordingFenceSettler{}
	assembly := newHealthDispatchAssembly(env)
	assembly.healthFenceSettler = settler.settle
	defer assembly.closeStores()

	options := assembly.healthCheckDispatchOptions(getenvFrom(env))
	raw := healthDispatchCompositionBody(t, func(payload map[string]any) {
		payload["sourceFence"] = map[string]any{
			"state_key":         "state-key-1",
			"account_id":        "acc-1",
			"source_generation": 7,
			"source_fence_id":   "2f0a1b3c-4d5e-6f70-8192-a3b4c5d6e7f8",
			"runtime_key":       "availability:acc-1:codex_source_avoidance:r3",
			"probe_generation":  11,
			"config_revision":   3,
		}
	})
	record := postHealthDispatchComposition(t, internalapi.NewHealthCheckDispatchHandler(options), raw)
	if record.Code != http.StatusAccepted {
		t.Fatalf("带 fence 派发必须 202: %d body=%s", record.Code, record.Body.String())
	}
	requests := loadPublishedRequests(t, env["JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY"], key)
	if len(requests) != 1 {
		t.Fatalf("必须恰好发布一个 request 文件: %d", len(requests))
	}
	if requests[0].MutateAccount {
		t.Fatal("带 source fence 的派发必须 mutate_account=false")
	}
	fence := requests[0].SourceFence
	if fence == nil {
		t.Fatal("request 文件必须内嵌 source_fence")
	}
	if fence.StateKey != "state-key-1" || fence.AccountID != "acc-1" || fence.SourceGeneration != 7 ||
		fence.SourceFenceID != "2f0a1b3c-4d5e-6f70-8192-a3b4c5d6e7f8" ||
		fence.RuntimeKey != "availability:acc-1:codex_source_avoidance:r3" ||
		fence.ProbeGeneration != 11 || fence.ConfigRevision != 3 {
		t.Fatalf("request source_fence 投影不一致: %+v", fence)
	}
	if len(settler.fences) != 0 {
		t.Fatalf("发布成功不得结算 source fence: %+v", settler.fences)
	}
}

// TestHealthDispatchOutsideJ1ScopeSettlesUnknownFence 覆盖冻结范围外账户：
// 静默跳过发布（不算失败），仍以 unknown 结算 source fence（Node
// dispatchAccountHealthCheckWithOutcome 的同语义分支）。
func TestHealthDispatchOutsideJ1ScopeSettlesUnknownFence(t *testing.T) {
	env, key := healthDispatchCompositionEnv(t)
	// 不写 accounts / input_versions 行：账户在 J1 冻结范围外。
	seedHealthDispatchBusinessTables(t, env["JUHE_AI_DATABASE_PATH"], "", 0, 0, 0)
	// 范围外不发布文件，预建空输入目录供消费入口读取断言。
	if err := os.MkdirAll(env["JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY"], 0o755); err != nil {
		t.Fatal(err)
	}
	settler := &recordingFenceSettler{}
	assembly := newHealthDispatchAssembly(env)
	assembly.healthFenceSettler = settler.settle
	defer assembly.closeStores()

	options := assembly.healthCheckDispatchOptions(getenvFrom(env))
	raw := healthDispatchCompositionBody(t, func(payload map[string]any) {
		payload["sourceFence"] = map[string]any{
			"state_key":         "state-key-2",
			"account_id":        "acc-1",
			"source_generation": 1,
			"source_fence_id":   "0a0a0a0a-0b0b-0c0c-0d0d-0e0e0e0e0e0e",
			"runtime_key":       "availability:acc-1:codex_source_avoidance:r9",
			"probe_generation":  4,
			"config_revision":   2,
		}
	})
	record := postHealthDispatchComposition(t, internalapi.NewHealthCheckDispatchHandler(options), raw)
	if record.Code != http.StatusAccepted {
		t.Fatalf("范围外派发仍必须 202（跳过 + 结算 fence）: %d body=%s", record.Code, record.Body.String())
	}
	if requests := loadPublishedRequests(t, env["JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY"], key); len(requests) != 0 {
		t.Fatalf("范围外账户不得发布 request 文件: %d", len(requests))
	}
	if len(settler.fences) != 1 {
		t.Fatalf("范围外派发必须结算一次 source fence: %d", len(settler.fences))
	}
	if settler.states[0] != "unknown" {
		t.Fatalf("结算 state 必须 unknown: %s", settler.states[0])
	}
	fence := settler.fences[0]
	if fence.StateKey != "state-key-2" || fence.RuntimeKey != "availability:acc-1:codex_source_avoidance:r9" || fence.ProbeGeneration != 4 {
		t.Fatalf("结算 fence 投影不一致: %+v", fence)
	}
}

// TestHealthDispatchRepeatedDispatchesAreIdempotent 覆盖重复派发：每次派发
// 独立生成 requestID 并各自落一个 request 文件（与 Node fire-and-forget 语义
// 一致，网关侧 per-request Symbol 负责节流），不产生状态损坏。
func TestHealthDispatchRepeatedDispatchesAreIdempotent(t *testing.T) {
	env, key := healthDispatchCompositionEnv(t)
	seedHealthDispatchBusinessTables(t, env["JUHE_AI_DATABASE_PATH"], "acc-1", 3, 5, 2)
	assembly := newHealthDispatchAssembly(env)
	defer assembly.closeStores()

	options := assembly.healthCheckDispatchOptions(getenvFrom(env))
	for i := 0; i < 2; i++ {
		record := postHealthDispatchComposition(t, internalapi.NewHealthCheckDispatchHandler(options), healthDispatchCompositionBody(t, nil))
		if record.Code != http.StatusAccepted {
			t.Fatalf("重复派发必须持续 202: %d", record.Code)
		}
	}
	requests := loadPublishedRequests(t, env["JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY"], key)
	if len(requests) != 2 {
		t.Fatalf("重复派发必须落两个独立 request 文件: %d", len(requests))
	}
	if requests[0].RequestID == requests[1].RequestID {
		t.Fatalf("重复派发必须使用独立 requestID: %s", requests[0].RequestID)
	}
	for _, request := range requests {
		if !strings.HasPrefix(request.RequestID, internalapi.HealthCheckProbeRequestIDPrefix) {
			t.Fatalf("requestID 必须带 J1 前缀: %s", request.RequestID)
		}
	}
}

// TestHealthDispatchUnavailableWithoutJ1Config 覆盖派发依赖缺失的显式拒绝：
// J1 未启用时 Dispatch 未装配 → 503（网关桥按 rejected 显式降级）。
func TestHealthDispatchUnavailableWithoutJ1Config(t *testing.T) {
	env := workerSmokeTestEnv(t)
	assembly := newHealthDispatchAssembly(env)
	defer assembly.closeStores()
	options := assembly.healthCheckDispatchOptions(getenvFrom(env))
	if options.Dispatch != nil {
		t.Fatal("J1 未启用时不得装配派发能力")
	}
	record := postHealthDispatchComposition(t, internalapi.NewHealthCheckDispatchHandler(options), healthDispatchCompositionBody(t, nil))
	if record.Code != http.StatusServiceUnavailable {
		t.Fatalf("派发能力未装配必须 503: %d", record.Code)
	}
}

// TestHealthDispatchRejectsInvalidProbeDeadline 覆盖 deadline 配置非法：
// 装配失败 → 派发能力不可用（503 显式拒绝 + 装配告警）。
func TestHealthDispatchRejectsInvalidProbeDeadline(t *testing.T) {
	env, _ := healthDispatchCompositionEnv(t)
	seedHealthDispatchBusinessTables(t, env["JUHE_AI_DATABASE_PATH"], "acc-1", 3, 5, 2)
	env["JUHE_AI_BACKGROUND_ACCOUNT_HEALTH_CHECK_PROBE_DEADLINE_MS"] = "not-a-number"
	assembly := newHealthDispatchAssembly(env)
	defer assembly.closeStores()
	options := assembly.healthCheckDispatchOptions(getenvFrom(env))
	if options.Dispatch != nil {
		t.Fatal("deadline 配置非法时不得装配派发能力")
	}
}

// TestInternalAPIHandlerRoutesHealthAndAccountTest 覆盖 loopback 路由组合：
// 健康检查路径与账户测试路径共存，未知路径 404。
func TestInternalAPIHandlerRoutesHealthAndAccountTest(t *testing.T) {
	env := workerSmokeTestEnv(t)
	assembly := newHealthDispatchAssembly(env)
	defer assembly.closeStores()
	handler := assembly.internalAPIHandler(healthDispatchTestSecret)

	healthRecord := postHealthDispatchComposition(t, handler, healthDispatchCompositionBody(t, nil))
	if healthRecord.Code != http.StatusServiceUnavailable {
		t.Fatalf("健康检查路由必须可达（无 J1 → 503）: %d", healthRecord.Code)
	}
	accountTest := httptest.NewRequest(http.MethodPost, internalapi.AccountTestDispatchInternalPrefix+"/v1/account-test/dispatch", strings.NewReader(`{"version":1,"taskId":"t"}`))
	accountTest.RemoteAddr = "127.0.0.1:54321"
	accountTest.Header.Set("Content-Type", "application/json")
	accountTest.Header.Set("X-Juhe-Ai-Signature", internalapi.CreateAccountTestDispatchSignature(healthDispatchTestSecret, []byte(`{"version":1,"taskId":"t"}`)))
	accountTestRecord := httptest.NewRecorder()
	handler.ServeHTTP(accountTestRecord, accountTest)
	if accountTestRecord.Code == http.StatusNotFound {
		t.Fatalf("账户测试路由必须保持可达: %d", accountTestRecord.Code)
	}
	unknown := httptest.NewRequest(http.MethodPost, internalapi.AccountTestDispatchInternalPrefix+"/v1/unknown", nil)
	unknown.RemoteAddr = "127.0.0.1:54321"
	unknownRecord := httptest.NewRecorder()
	handler.ServeHTTP(unknownRecord, unknown)
	if unknownRecord.Code != http.StatusNotFound {
		t.Fatalf("未知内部路径必须 404: %d", unknownRecord.Code)
	}
}
