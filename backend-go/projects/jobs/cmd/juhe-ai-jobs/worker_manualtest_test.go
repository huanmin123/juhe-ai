package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/internalapi"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/oauthrefresh"
	_ "modernc.org/sqlite"
)

// seedManualTestTables 预置手动测试族契约表（生产由迁移创建；列集与
// manualtestrepo 测试 fixture 同形）。
func seedManualTestTables(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	statements := []string{
		`CREATE TABLE IF NOT EXISTS account_test_tasks (
			id TEXT PRIMARY KEY,
			session_id TEXT,
			account_id TEXT NOT NULL,
			account_name TEXT NOT NULL DEFAULT '',
			provider_code TEXT NOT NULL DEFAULT '',
			provider_protocol_profile_id TEXT NOT NULL DEFAULT '',
			protocol_code TEXT NOT NULL DEFAULT '',
			protocol_version TEXT NOT NULL DEFAULT '',
			account_type TEXT NOT NULL DEFAULT '',
			request_system_account_id TEXT NOT NULL,
			request_role TEXT NOT NULL DEFAULT 'user',
			request_system_account_filter_id TEXT,
			diagnostics TEXT NOT NULL DEFAULT 'full',
			model TEXT,
			test_endpoint_mode TEXT,
			draft_account_encrypted TEXT,
			status TEXT NOT NULL DEFAULT 'queued',
			status_message TEXT,
			result_json TEXT,
			error_message TEXT,
			cancel_requested INTEGER NOT NULL DEFAULT 0,
			queued_at TEXT NOT NULL,
			queued_deadline_at TEXT,
			started_at TEXT,
			finished_at TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS account_test_sessions (
			id TEXT PRIMARY KEY,
			request_system_account_id TEXT NOT NULL,
			request_role TEXT NOT NULL DEFAULT 'user',
			request_system_account_filter_id TEXT,
			status TEXT NOT NULL DEFAULT 'running',
			cancel_reason TEXT,
			last_heartbeat_at TEXT NOT NULL,
			cancel_requested_at TEXT,
			finished_at TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS account_test_session_tasks (
			session_id TEXT NOT NULL,
			task_id TEXT NOT NULL PRIMARY KEY
		)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("手动测试族 fixture 建表失败: %v", err)
		}
	}
}

// manualTestWorkerTestEnv 在 smoke 环境上补齐手动测试族所需 env 与表。
func manualTestWorkerTestEnv(t *testing.T) map[string]string {
	t.Helper()
	env := probeWorkerTestEnv(t)
	seedManualTestTables(t, env["JUHE_AI_DATABASE_PATH"])
	return env
}

func insertDraftTestTask(t *testing.T, db *sql.DB, taskID string, draftEnvelope string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`
		INSERT INTO account_test_tasks (
			id, account_id, account_name, provider_code, provider_protocol_profile_id,
			protocol_code, protocol_version, account_type,
			request_system_account_id, request_role, diagnostics,
			model, test_endpoint_mode, draft_account_encrypted,
			status, status_message, cancel_requested, queued_at, created_at, updated_at
		) VALUES (?, 'acc-draft', '草稿账户', 'openai', 'profile_openai_openai_v1',
			'openai', 'v1', 'api_key',
			'sys-1', 'user', 'full',
			'gpt-test', 'chat_json', ?,
			'queued', '等待后台测试', 0, ?, ?, ?)`,
		taskID, draftEnvelope, now, now, now); err != nil {
		t.Fatal(err)
	}
}

func openTaskDB(t *testing.T, databasePath string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func waitForTaskStatus(t *testing.T, db *sql.DB, taskID string, statuses ...string) (string, string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		var status, resultJSON sql.NullString
		if err := db.QueryRow(`SELECT status, result_json FROM account_test_tasks WHERE id = ?`, taskID).
			Scan(&status, &resultJSON); err != nil {
			t.Fatal(err)
		}
		for _, wanted := range statuses {
			if status.String == wanted {
				return status.String, resultJSON.String
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	status := ""
	_ = db.QueryRow(`SELECT status FROM account_test_tasks WHERE id = ?`, taskID).Scan(&status)
	t.Fatalf("任务 %s 未到达 %v（当前 %q）", taskID, statuses, status)
	return "", ""
}

func signDispatchBody(t *testing.T, secret string, body []byte) string {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(internalapi.AccountTestDispatchSignatureDomain))
	_, _ = mac.Write(body)
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}

func postDispatch(t *testing.T, server *httptest.Server, path string, taskID string, secret string) int {
	t.Helper()
	body, err := json.Marshal(map[string]any{"version": 1, "taskId": taskID})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, server.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Juhe-Ai-Signature", signDispatchBody(t, secret, body))
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	return response.StatusCode
}

func chatTestUpstream(t *testing.T, hold *chan struct{}) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hold != nil {
			select {
			case <-r.Context().Done():
			case <-*hold:
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"juhe"}}]}`))
	}))
	t.Cleanup(server.Close)
	if hold != nil {
		t.Cleanup(func() { close(*hold) })
	}
	return server
}

const chatTestModel = "gpt-test"

func draftTestEnvelope(t *testing.T, secret, baseURL string) string {
	t.Helper()
	draft := map[string]any{
		"id":                        "acct-draft",
		"ownerSystemAccountId":      "sys-1",
		"groupId":                   "grp-1",
		"providerCode":              "openai",
		"providerProtocolProfileId": "profile_openai_openai_v1",
		"protocolCode":              "openai",
		"protocolVersion":           "v1",
		"name":                      "草稿账户",
		"type":                      "api_key",
		"credentials": map[string]any{
			"api_key":                  "sk-test-123456",
			"base_url":                 baseURL,
			"supported_endpoint_modes": []any{"chat_json"},
		},
		"clientCompatibility":     "openai_standard",
		"supportedModels":         []any{chatTestModel},
		"healthCheckModel":        chatTestModel,
		"healthCheckEndpointMode": "chat_json",
	}
	envelope, err := oauthrefresh.EncryptJSON(secret, draft)
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

// 全链路：httptest 上游 → internalapi 派发 → 队列执行 → 任务落 success +
// result_json 信封；取消路径 → canceled；族未接线 → 503 回退保留。
func TestManualTestFamilyEndToEndDispatchExecuteComplete(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end wiring test skipped in -short mode")
	}
	env := manualTestWorkerTestEnv(t)
	config, err := loadWorkerConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("loadWorkerConfig: %v", err)
	}
	assembly, err := buildWorkerAssembly(config, nil)
	if err != nil {
		t.Fatalf("buildWorkerAssembly: %v", err)
	}
	defer assembly.closeStores()
	if assembly.manualTestQueue == nil {
		t.Fatal("手动测试族应接线 manualTestQueue")
	}
	if assembly.dispatchHandler == nil {
		t.Fatal("dispatchHandler 未挂载")
	}

	// 队列组件运行（Start + Run）。
	queueCtx, stopQueue := context.WithCancel(context.Background())
	defer stopQueue()
	var queueComponent func(context.Context) error
	for _, component := range assembly.components() {
		if component.Name == "manual account test queue" {
			queueComponent = component.Run
		}
	}
	if queueComponent == nil {
		t.Fatal("组件清单缺少手动测试队列")
	}
	queueDone := make(chan error, 1)
	go func() { queueDone <- queueComponent(queueCtx) }()

	handlerServer := httptest.NewServer(assembly.dispatchHandler)
	t.Cleanup(handlerServer.Close)
	db := openTaskDB(t, env["JUHE_AI_DATABASE_PATH"])

	upstream := chatTestUpstream(t, nil)
	secret := env["JUHE_AI_SECRET"]
	insertDraftTestTask(t, db, "task-e2e", draftTestEnvelope(t, secret, upstream.URL+"/v1"))

	// 派发 → 202。
	if status := postDispatch(t, handlerServer, "/__aiinternal__/v1/account-test/dispatch", "task-e2e", secret); status != http.StatusAccepted {
		t.Fatalf("派发应 202, got %d", status)
	}
	status, resultJSON := waitForTaskStatus(t, db, "task-e2e", "success", "failed")
	if status != "success" {
		t.Fatalf("任务应 success: %s（result=%s）", status, resultJSON)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(resultJSON), &envelope); err != nil {
		t.Fatalf("result_json 不是 JSON: %v", err)
	}
	if envelope["accountId"] != "acct-draft" || envelope["success"] != true ||
		envelope["model"] != chatTestModel || envelope["testEndpointMode"] != "chat_json" {
		t.Fatalf("信封字段不符: %v", envelope)
	}

	// 取消路径：第二任务派发后在跑（上游挂住）→ cancel 路由 202 → canceled。
	hold := make(chan struct{})
	heldUpstream := chatTestUpstream(t, &hold)
	insertDraftTestTask(t, db, "task-cancel", draftTestEnvelope(t, secret, heldUpstream.URL+"/v1"))
	if status := postDispatch(t, handlerServer, "/__aiinternal__/v1/account-test/dispatch", "task-cancel", secret); status != http.StatusAccepted {
		t.Fatalf("第二任务派发应 202, got %d", status)
	}
	waitForTaskStatus(t, db, "task-cancel", "running")
	if status := postDispatch(t, handlerServer, "/__aiinternal__/v1/account-test/cancel", "task-cancel", secret); status != http.StatusAccepted {
		t.Fatalf("取消应 202, got %d", status)
	}
	cancelStatus, _ := waitForTaskStatus(t, db, "task-cancel", "canceled")
	if cancelStatus != "canceled" {
		t.Fatalf("任务应 canceled: %s", cancelStatus)
	}

	stopQueue()
	select {
	case err := <-queueDone:
		if err != nil {
			t.Fatalf("队列组件退出错误: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("队列组件未在 5s 内停止")
	}
}

// 族未接线（env 关闭）→ 派发回调保持 503 回退语义。
func TestManualTestFamilyDisabledKeeps503Fallback(t *testing.T) {
	if testing.Short() {
		t.Skip("wiring test skipped in -short mode")
	}
	env := manualTestWorkerTestEnv(t)
	env["JUHE_AI_JOBS_MANUAL_TEST_ENABLED"] = "false"
	config, err := loadWorkerConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("loadWorkerConfig: %v", err)
	}
	assembly, err := buildWorkerAssembly(config, nil)
	if err != nil {
		t.Fatalf("buildWorkerAssembly: %v", err)
	}
	defer assembly.closeStores()
	if assembly.manualTestQueue != nil {
		t.Fatal("手动测试族关闭时不得接线队列")
	}
	if assembly.dispatchHandler == nil {
		t.Fatal("internalapi handler 仍应挂载（503 回退面）")
	}
	handlerServer := httptest.NewServer(assembly.dispatchHandler)
	t.Cleanup(handlerServer.Close)
	if status := postDispatch(t, handlerServer, "/__aiinternal__/v1/account-test/dispatch", "task-x", env["JUHE_AI_SECRET"]); status != http.StatusServiceUnavailable {
		t.Fatalf("未接线派发应 503, got %d", status)
	}
	if status := postDispatch(t, handlerServer, "/__aiinternal__/v1/account-test/cancel", "task-x", env["JUHE_AI_SECRET"]); status != http.StatusServiceUnavailable {
		t.Fatalf("未接线取消应 503, got %d", status)
	}
}

// 契约校验失败（缺 account_test_* 表）→ fail closed 登记 disabled，派发保持 503。
func TestManualTestFamilyFailsClosedOnMissingTables(t *testing.T) {
	if testing.Short() {
		t.Skip("wiring test skipped in -short mode")
	}
	env := probeWorkerTestEnv(t)
	config, err := loadWorkerConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("loadWorkerConfig: %v", err)
	}
	assembly, err := buildWorkerAssembly(config, nil)
	if err != nil {
		t.Fatalf("buildWorkerAssembly: %v", err)
	}
	defer assembly.closeStores()
	if assembly.manualTestQueue != nil {
		t.Fatal("缺契约表时不得接线队列")
	}
	reasons := map[string]string{}
	for _, job := range assembly.disabledJobs {
		reasons[job.JobName] = job.Reason
	}
	if reason, ok := reasons["background_worker_account_test_tasks"]; !ok {
		t.Fatalf("应登记 background_worker_account_test_tasks disabled: %v", reasons)
	} else if !strings.Contains(reason, "契约校验失败") {
		t.Fatalf("disabled 原因不符: %q", reason)
	}
}

// env 门禁：手动测试族启用而缺业务库路径必须 fail closed。
func TestManualTestFamilyConfigGate(t *testing.T) {
	base := workerSmokeTestEnv(t)
	delete(base, "JUHE_AI_DATABASE_PATH")
	base["JUHE_AI_JOBS_PROBE_ENABLED"] = "false"
	if _, err := loadWorkerConfig(getenvFrom(base)); err == nil {
		t.Fatal("启用手动测试族而缺少 JUHE_AI_DATABASE_PATH 必须 fail closed")
	}
	staleEnv := workerSmokeTestEnv(t)
	staleEnv["JUHE_AI_BACKGROUND_ACCOUNT_TEST_RUNNING_STALE_MS"] = "59999"
	if _, err := loadWorkerConfig(getenvFrom(staleEnv)); err == nil {
		t.Fatal("running-stale 低于 60000 必须 fail closed")
	}
}
