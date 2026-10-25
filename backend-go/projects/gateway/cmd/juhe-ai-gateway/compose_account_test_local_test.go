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
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
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
	tdb := composeTestPostgres(t)
	cfg := composeTestConfigForDSN(t, tdb.DSN)
	store := openComposeOperationStore(t, tdb.DSN)
	composed, err := composeSystemAPI(cfg, pgpool.NewRegistry(), store, openComposeOperationLease(t, store), mustComposeAuditProducer(t, tdb.DSN), composeTestAuditConfig(t, tdb.DSN), composeTestOwnerHealth())
	if err != nil {
		t.Fatalf("compose system api: %v", err)
	}
	defer composed.Shutdown()

	accountStore, err := accounts.NewStore(composed.DB, cfg.Secret, time.Now, newCompositionID)
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

	// 组合根业务句柄即 PG 共享池；夹具直读/直写 juhe_business 行。
	db := composed.DB

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"` + composeProbeAnswer(body) + `"}}]}`))
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
		INSERT INTO juhe_business.account_test_tasks (
			id, account_id, account_name, provider_protocol_profile_id,
			protocol_code, protocol_version, account_type,
			request_system_account_id, request_role, diagnostics,
			model, test_endpoint_mode, draft_account_encrypted,
			status, status_message, cancel_requested, queued_at, created_at, updated_at
		) VALUES ($1, 'acct-compose-e2e', '组合测试草稿账户', 'profile_openai_openai_v1',
			'openai', 'v1', 'api_key',
			'sys_admin', 'user', 'full',
			'gpt-compose', 'chat_json', $2,
			'queued', '等待后台测试', FALSE, $3, $4, $5)`,
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
		if err := db.QueryRow(`SELECT status, result_json FROM juhe_business.account_test_tasks WHERE id = $1`, taskID).
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

// composeProbeChallengePattern 匹配探针算术挑战报文（accountprobe
// CreateOutputChallenge 的 prompt 格式："请计算 A+B，只回复数字"；2026-10-15
// 起该算术题取代历史固定令牌 "juhe"）。
var composeProbeChallengePattern = regexp.MustCompile(`请计算 (\d+)\+(\d+)`)

// composeProbeAnswer 从探针请求体解析算术挑战并返回两数之和的十进制文本：
// 探针期望输出是随机两数之和，假上游必须真实作答才能通过输出校验（返回固定
// 令牌会落到 invalid_probe_output）。无匹配时回退历史固定令牌，让请求形态
// 漂移在断言处显式失败而不是静默通过。
func composeProbeAnswer(body []byte) string {
	match := composeProbeChallengePattern.FindSubmatch(body)
	if match == nil {
		return "juhe"
	}
	left, _ := strconv.Atoi(string(match[1]))
	right, _ := strconv.Atoi(string(match[2]))
	return strconv.Itoa(left + right)
}
