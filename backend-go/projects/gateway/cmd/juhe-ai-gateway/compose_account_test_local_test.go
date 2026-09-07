package main

// 手动账号测试进程内执行链组合测试（去跨进程战役：原 jobsAccountTestDispatchBridge
// HTTP 桥删除后的接线收口）：
//  1. 组合根源码断言：compose.go 必须经 wireInProcessAccountTestDispatch 装配
//     进程内执行链（先于该装配行的账户 store 构造），复用 revoker 断言的既有先例；
//  2. 生效断言测试：composeSystemAPI 提供真实业务库 schema（SQLite，
//     account_test_* 由 maintenance bootstrap 拥有），生产同款构造把
//     gatewayAccountTestDispatch 装配到 accounts.Store，真实插入的 queued
//     draft 任务经本地队列执行后 result_json 落列、路由同款状态读取可读；
//     幂等重复派发不置败 running/终态任务；取消路径落 canceled。

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountcrypto"
	_ "modernc.org/sqlite"
)

// TestComposeSystemAPIWiresInProcessAccountTestDispatch pins the
// composition-root wiring line (装配断线零容忍：源码级断言复用 revoker 先例).
func TestComposeSystemAPIWiresInProcessAccountTestDispatch(t *testing.T) {
	source, err := os.ReadFile("compose.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	needle := "wireInProcessAccountTestDispatch(composed, cfg, accountStore)"
	if !strings.Contains(text, needle) {
		t.Fatalf("compose root must wire the in-process account test dispatch chain: %s", needle)
	}
	accountStorePos := strings.Index(text, "accountStore, err := accounts.NewStore")
	wirePos := strings.Index(text, needle)
	if accountStorePos < 0 || wirePos < accountStorePos {
		t.Fatalf("in-process account test dispatch must be wired after account store construction")
	}
	if strings.Contains(text, "newJobsAccountTestDispatchBridge") {
		t.Fatal("跨进程 HTTP 桥必须保持删除状态（去跨进程战役）")
	}
}

// TestInProcessAccountTestDispatchEndToEnd is the effectiveness assertion:
// the production-same construction over a composed system API executes a real
// queued draft task on the local queue and lands the result envelope in
// account_test_tasks.result_json.
func TestInProcessAccountTestDispatchEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end wiring test skipped in -short mode")
	}
	cfg := composeTestConfig(t)
	store := openComposeOperationStore(t)
	createRuntimeLogDataset(t, cfg.RuntimeLogDatabasePath)
	auditConfig, auditProducer, closeAudit := openComposeAuditSources(t, filepath.Dir(cfg.DatasetDatabasePath))
	defer closeAudit()
	composed, err := composeSystemAPI(cfg, pgpool.NewRegistry(), store, openComposeOperationLease(t, store), auditProducer, auditConfig)
	if err != nil {
		t.Fatalf("compose system api: %v", err)
	}
	defer composed.Shutdown()

	accountStore, err := accounts.NewStore(composed.DB, false, cfg.Secret, time.Now, newCompositionID)
	if err != nil {
		t.Fatalf("accounts store: %v", err)
	}
	// 生产同款装配（compose.go 同调用）：契约表由 bootstrap schema 提供，
	// 装配成功后端口可达。
	if err := wireInProcessAccountTestDispatch(composed, cfg, accountStore); err != nil {
		t.Fatalf("wire in-process account test dispatch: %v", err)
	}
	effects := accountStore.TestDispatchEffects()
	if effects == nil {
		t.Fatal("进程内装配后 TestDispatchEffects 端口必须已接线")
	}

	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(cfg.BusinessDatabasePath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"juhe"}}]}`))
	}))
	t.Cleanup(upstream.Close)

	draft := map[string]any{
		"id":                        "acct-compose-e2e",
		"ownerSystemAccountId":      "sys_admin",
		"groupId":                   "grp-1",
		"providerCode":              "openai",
		"providerProtocolProfileId": "profile_openai_openai_v1",
		"protocolCode":              "openai",
		"protocolVersion":           "v1",
		"name":                      "组合测试草稿账户",
		"type":                      "api_key",
		"credentials": map[string]any{
			"api_key":                  "sk-compose-e2e",
			"base_url":                 upstream.URL + "/v1",
			"supported_endpoint_modes": []any{"chat_json"},
		},
		"clientCompatibility":     "openai_standard",
		"supportedModels":         []any{"gpt-compose"},
		"healthCheckModel":        "gpt-compose",
		"healthCheckEndpointMode": "chat_json",
	}
	envelope, err := accountcrypto.EncryptJSON(cfg.Secret, draft)
	if err != nil {
		t.Fatal(err)
	}
	insertComposeDraftTask(t, db, "task-compose-e2e", envelope)

	// 路由同款派发调用（test_dispatch_routes.go：effects.DispatchAccountTestTasks）。
	if !effects.DispatchAccountTestTasks(context.Background(), []string{"task-compose-e2e"}) {
		t.Fatal("本地队列必须受理新任务")
	}
	status, resultJSON := waitForComposeTaskStatus(t, db, "task-compose-e2e", "success", "failed")
	if status != "success" {
		t.Fatalf("任务应 success: %s（result=%s）", status, resultJSON)
	}
	var envelopeResult map[string]any
	if err := json.Unmarshal([]byte(resultJSON), &envelopeResult); err != nil {
		t.Fatalf("result_json 不是 JSON: %v", err)
	}
	if envelopeResult["accountId"] != "acct-compose-e2e" || envelopeResult["success"] != true ||
		envelopeResult["model"] != "gpt-compose" || envelopeResult["testEndpointMode"] != "chat_json" {
		t.Fatalf("结果信封字段不符: %v", envelopeResult)
	}

	// 幂等 quirk 修正：同任务重复派发（终态后本地队列不再持有）按幂等成功
	// 处理，不得把任务置败（status 保持 success，无失败写入）。
	if !effects.DispatchAccountTestTasks(context.Background(), []string{"task-compose-e2e"}) {
		t.Fatal("重复派发必须按幂等成功返回 true")
	}
	if got, _ := waitForComposeTaskStatus(t, db, "task-compose-e2e", "success"); got != "success" {
		t.Fatalf("重复派发后任务状态漂移: %s", got)
	}

	// 取消路径：在跑任务（上游挂住）→ 路由同款取消派发 → canceled。
	hold := make(chan struct{})
	heldUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-hold:
		}
	}))
	t.Cleanup(heldUpstream.Close)
	t.Cleanup(func() { close(hold) })
	heldEnvelope, err := accountcrypto.EncryptJSON(cfg.Secret, func() map[string]any {
		held := map[string]any{}
		for key, value := range draft {
			held[key] = value
		}
		held["credentials"] = map[string]any{
			"api_key":                  "sk-compose-e2e",
			"base_url":                 heldUpstream.URL + "/v1",
			"supported_endpoint_modes": []any{"chat_json"},
		}
		return held
	}())
	if err != nil {
		t.Fatal(err)
	}
	insertComposeDraftTask(t, db, "task-compose-cancel", heldEnvelope)
	if !effects.DispatchAccountTestTasks(context.Background(), []string{"task-compose-cancel"}) {
		t.Fatal("第二个任务必须受理")
	}
	waitForComposeTaskStatus(t, db, "task-compose-cancel", "running")
	effects.DispatchAccountTestCancel("task-compose-cancel")
	if status, _ := waitForComposeTaskStatus(t, db, "task-compose-cancel", "canceled"); status != "canceled" {
		t.Fatalf("取消任务应 canceled: %s", status)
	}
}

// insertComposeDraftTask 插入一条 queued 的 draft 测试任务（列集与生产
// account_test_tasks 表同形的最小集）。
func insertComposeDraftTask(t *testing.T, db *sql.DB, taskID, draftEnvelope string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`
		INSERT INTO account_test_tasks (
			id, account_id, account_name, provider_code, provider_protocol_profile_id,
			protocol_code, protocol_version, account_type,
			request_system_account_id, request_role, diagnostics,
			model, test_endpoint_mode, draft_account_encrypted,
			status, status_message, cancel_requested, queued_at, created_at, updated_at
		) VALUES (?, 'acct-compose-e2e', '组合测试草稿账户', 'openai', 'profile_openai_openai_v1',
			'openai', 'v1', 'api_key',
			'sys_admin', 'user', 'full',
			'gpt-compose', 'chat_json', ?,
			'queued', '等待后台测试', 0, ?, ?, ?)`,
		taskID, draftEnvelope, now, now, now); err != nil {
		t.Fatalf("插入 draft 任务失败: %v", err)
	}
}

// waitForComposeTaskStatus 轮询任务状态直到命中目标集合（本地队列执行为
// 异步 goroutine + sweep，8s 上限覆盖 images 之外的分级预算）。
func waitForComposeTaskStatus(t *testing.T, db *sql.DB, taskID string, statuses ...string) (string, string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	status := ""
	var resultJSON sql.NullString
	for time.Now().Before(deadline) {
		if err := db.QueryRow(`SELECT status, result_json FROM account_test_tasks WHERE id = ?`, taskID).
			Scan(&status, &resultJSON); err != nil {
			t.Fatal(err)
		}
		for _, wanted := range statuses {
			if status == wanted {
				return status, resultJSON.String
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("任务 %s 未到达 %v（当前 %q，result=%q）", taskID, statuses, status, resultJSON.String)
	return "", ""
}
