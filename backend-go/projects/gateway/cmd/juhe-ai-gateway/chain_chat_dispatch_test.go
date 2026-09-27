package main

// AI 问答会话三种绑定模式的进程内调度覆盖通道测试（设计 §6/§9）：
//  - 执行器目标绑定 → /v1 context 注入（未导出 key，外部请求不可构造）；
//  - group 模式候选组固定为指定分组（全链：派发落在绑定分组上游）；
//  - account 模式候选收敛单账户且 usage 归属按账户 BoundGroupID 记账；
//  - 无 target 请求（外部请求 / api_key 模式）调度路径与现状一致；
//  - 绑定目标不可用走既有"无可用账户"语义，分组回退被禁用不逃逸。
// 测试风格与 chain_test.go 的 full-chain smoke 一致（真实 SQLite fixture +
// mock 上游 + spool 用量断言）。

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

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// chatDispatchUpstream 是可命中计数的 mock 上游。
type chatDispatchUpstream struct {
	server *httptest.Server
	hits   int
}

func newChatDispatchUpstream(t *testing.T, body string) *chatDispatchUpstream {
	t.Helper()
	upstream := &chatDispatchUpstream{}
	upstream.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(upstream.server.Close)
	return upstream
}

// seedChatDispatchPinGroup 在 fixture 上补一组双分组事实：group_main 的 acc_1
// 指向主上游（Key 默认路由），group_pin 的 acc_pin 指向绑定分组上游。
func seedChatDispatchPinGroup(t *testing.T, fixture *chainFixture, mainURL, pinURL, accountStatus string) {
	t.Helper()
	now := "2026-09-04T00:00:00.000Z"
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(query, args...); err != nil {
			t.Fatalf("seed pin group: %v: %s", err, query)
		}
	}
	seed(`UPDATE accounts SET credentials_encrypted = ? WHERE id = ?`,
		mustEncryptCredentials(t, map[string]any{"api_key": "sk-upstream-account-key", "base_url": mainURL}), fixture.accountID)
	pinCredentials := mustEncryptCredentials(t, map[string]any{"api_key": "sk-upstream-pin-key", "base_url": pinURL})
	seed(`INSERT INTO accounts (
			id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
			name, type, status, schedulable, credentials_encrypted, deleted_at, health_check_model
		) VALUES ('acc_pin', ?, 'openai', 'prof_1', 'openai', 'v1', '绑定账户', 'api_key', ?, 1, ?, NULL, 'gpt-test')`,
		fixture.systemAccount, accountStatus, pinCredentials)
	seed(`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type, scheduling_policy_json)
		VALUES ('group_pin', ?, 'openai', 1, 'personal', NULL)`, fixture.systemAccount)
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
		VALUES ('group_pin', ?, 'acc_pin', 1, ?)`, fixture.systemAccount, now)
	seed(`INSERT INTO account_supported_models (account_id, provider_code, model, created_at)
		VALUES ('acc_pin', 'openai', 'gpt-test', ?)`, now)
}

// composeChatDispatchChain 组装 full-chain（含 spool 目录），返回 chain 与
// spool 目录。spool 刻意放在 t.TempDir() 之外：异步用量记录的落盘与
// TempDir 的 RemoveAll 存在 Windows unlinkat 竞态（"directory is not
// empty"，停机后仍可能有迟到记录重建目录），改为自有目录 + 尽力清理。
func composeChatDispatchChain(t *testing.T, fixture *chainFixture) (http.Handler, string) {
	t.Helper()
	spoolDir := filepath.Join(os.TempDir(), "juhe-ai-chatdispatch-spool-"+newCompositionID("t"))
	chain, shutdown, err := composeGatewayChain(chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, spoolDir))
	if err != nil {
		t.Fatalf("compose gateway chain: %v", err)
	}
	t.Cleanup(func() {
		shutdown()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if err := os.RemoveAll(spoolDir); err == nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
	return chain, spoolDir
}

// chatDispatchRequest 以进程内直驱（与 chatGatewayExecutor.Dispatch 同一
// 通道：handler 直调 + context 注入，不经 loopback TCP——TCP 会丢失请求
// context）按可选调度覆盖目标发一次 /v1/chat/completions。
func chatDispatchRequest(t *testing.T, chain http.Handler, fixture *chainFixture, target chatDispatchTarget, hasTarget bool) (int, string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"你好"}]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+fixture.apiKeySecret)
	if hasTarget {
		request = request.WithContext(contextWithChatDispatchTarget(request.Context(), target))
	}
	recorder := httptest.NewRecorder()
	chain.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.String()
}

// waitForSpoolRecords 轮询 spool 目录直到出现记录文件并解析为 JSON 列表。
func waitForSpoolRecords(t *testing.T, spoolDir string) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		// spool 布局：<spoolDir>/<instanceID>/<token>.json（一文件一条记录）。
		instances, err := os.ReadDir(spoolDir)
		if err == nil {
			records := []map[string]any{}
			for _, instance := range instances {
				if !instance.IsDir() {
					continue
				}
				files, err := os.ReadDir(filepath.Join(spoolDir, instance.Name()))
				if err != nil {
					continue
				}
				for _, file := range files {
					if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
						continue
					}
					raw, err := os.ReadFile(filepath.Join(spoolDir, instance.Name(), file.Name()))
					if err != nil {
						continue
					}
					var record map[string]any
					if json.Unmarshal(raw, &record) == nil && len(record) > 0 {
						records = append(records, record)
					}
				}
			}
			if len(records) > 0 {
				return records
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("spool 无用量记录: %s", spoolDir)
	return nil
}

// TestChatConversationDispatchTargetReadsBindColumns：观察适配器的会话绑定
// 目标只读查询（chat_conversations 同表同列）。
func TestChatConversationDispatchTargetReadsBindColumns(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "obs-target.sqlite3"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE chat_conversations (
		id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL,
		bind_mode TEXT NOT NULL DEFAULT 'api_key',
		bind_group_id TEXT, bind_account_id TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	bind := func(query string) string { return query }
	insert := func(id, mode string, groupID, accountID any) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO chat_conversations (id, system_account_id, bind_mode, bind_group_id, bind_account_id)
			VALUES (?, 'sys_owner', ?, ?, ?)`, id, mode, groupID, accountID); err != nil {
			t.Fatalf("insert conversation: %v", err)
		}
	}
	insert("conv_group", "group", "group_pin", nil)
	insert("conv_account", "account", nil, "acc_pin")
	insert("conv_api_key", "api_key", nil, nil)

	target, found, err := chatConversationDispatchTarget(db, "chat_conversations", bind, "conv_group", "sys_owner")
	if err != nil || !found || !target.pinned() || target.Mode != "group" || target.GroupID != "group_pin" {
		t.Fatalf("group target = %+v found=%v err=%v", target, found, err)
	}
	target, found, err = chatConversationDispatchTarget(db, "chat_conversations", bind, "conv_account", "sys_owner")
	if err != nil || !found || !target.pinned() || target.Mode != "account" || target.AccountID != "acc_pin" {
		t.Fatalf("account target = %+v found=%v err=%v", target, found, err)
	}
	target, found, err = chatConversationDispatchTarget(db, "chat_conversations", bind, "conv_api_key", "sys_owner")
	if err != nil || !found || target.pinned() {
		t.Fatalf("api_key target = %+v found=%v err=%v", target, found, err)
	}
	if _, found, err := chatConversationDispatchTarget(db, "chat_conversations", bind, "conv_missing", "sys_owner"); err == nil || found {
		t.Fatalf("missing conversation = found=%v err=%v", found, err)
	}
}

const chatDispatchSmokeBody = `{"id":"chatcmpl-pin","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"CONTENT"},"finish_reason":"stop"}]}`

// TestChatGatewayExecutorInjectsDispatchTargetContext：绑定目标的执行器视图
// 在派发时把目标注入进程内 context；api_key/legacy 模式返回原实例且 context
// 无目标。
func TestChatGatewayExecutorInjectsDispatchTargetContext(t *testing.T) {
	var seenTarget chatDispatchTarget
	var sawKey bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawKey = r.Context().Value(chatDispatchTargetContextKey{}).(chatDispatchTarget)
		seenTarget, _ = chatDispatchTargetFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	executor := newChatGatewayExecutor(handler)

	// api_key 模式：WithChatDispatchTarget 返回原实例，context 无目标。
	same := executor.WithChatDispatchTarget("api_key", "", "")
	if _, identical := same.(*chatGatewayExecutor); !identical {
		t.Fatalf("api_key 模式必须返回原执行器实例")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := executor.Dispatch(ctx, chatGenerationDispatchSmoke()); err != nil {
		t.Fatalf("api_key dispatch: %v", err)
	}
	if sawKey || seenTarget.pinned() {
		t.Fatalf("api_key 模式 context 不得携带目标: %v key=%v", seenTarget, sawKey)
	}

	// group 模式：新实例 + context 注入目标。
	bound := executor.WithChatDispatchTarget("group", "group_pin", "")
	if bound == executor {
		t.Fatalf("group 模式必须返回绑定目标的执行器视图")
	}
	sawKey = false
	if _, err := bound.Dispatch(ctx, chatGenerationDispatchSmoke()); err != nil {
		t.Fatalf("group dispatch: %v", err)
	}
	if !sawKey || !seenTarget.pinned() || seenTarget.Mode != "group" || seenTarget.GroupID != "group_pin" {
		t.Fatalf("group 目标未注入 context: %v key=%v", seenTarget, sawKey)
	}
}

func chatGenerationDispatchSmoke() chat.GenerationDispatchRequest {
	return chat.GenerationDispatchRequest{Path: "/v1/chat/completions", Method: http.MethodPost}
}

// TestChatDispatchTargetFromContextRejectsExternal：外部请求 context 不可能
// 携带未导出 key（服务端构造的请求 context 读取恒 false）。
func TestChatDispatchTargetFromContextRejectsExternal(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	if _, ok := chatDispatchTargetFromContext(request.Context()); ok {
		t.Fatal("外部请求 context 不得解析出调度目标")
	}
	// 非目标值类型挂入同 key 不可能（未导出类型包外无法构造）；错误类型值经
	// 包内路径也不应误读。
	if _, ok := chatDispatchTargetFromContext(context.WithValue(request.Context(), chatDispatchTargetContextKey{}, "group_pin")); ok {
		t.Fatal("非目标类型值不得解析为调度目标")
	}
	if _, ok := chatDispatchTargetFromContext(context.WithValue(request.Context(), chatDispatchTargetContextKey{}, chatDispatchTarget{Mode: "api_key"})); ok {
		t.Fatal("api_key 目标不构成覆盖")
	}
}

// withChatDispatchAccountGroups 置位 account 模式承载分组解析端口（生产由
// composeChatFamily 按域 A EnabledGroupIDs 口径装配），测试结束还原。
func withChatDispatchAccountGroups(t *testing.T, groups ...string) {
	t.Helper()
	resolved := append([]string{}, groups...)
	setChainChatDispatchAccountGroups(func(accountID string) ([]string, bool) {
		if accountID == "" {
			return nil, false
		}
		return resolved, true
	})
	t.Cleanup(func() { setChainChatDispatchAccountGroups(nil) })
}

// TestChatDispatchGroupPinFullChain：group 模式会话的派发固定落在指定分组
// （group_pin 上游命中），Key 默认路由分组（group_main 上游）零命中。
func TestChatDispatchGroupPinFullChain(t *testing.T) {
	fixture := newChainFixture(t)
	shortenChainWaitBudgets(t, fixture)
	main := newChatDispatchUpstream(t, strings.Replace(chatDispatchSmokeBody, "CONTENT", "主分组内容", 1))
	pin := newChatDispatchUpstream(t, strings.Replace(chatDispatchSmokeBody, "CONTENT", "绑定分组内容", 1))
	seedChatDispatchPinGroup(t, fixture, main.server.URL, pin.server.URL, "active")
	chain, _ := composeChatDispatchChain(t, fixture)

	status, body := chatDispatchRequest(t, chain, fixture, chatDispatchTarget{Mode: "group", GroupID: "group_pin"}, true)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if !strings.Contains(body, "绑定分组内容") {
		t.Fatalf("派发未落在绑定分组上游: %s", body)
	}
	if pin.hits != 1 || main.hits != 0 {
		t.Fatalf("upstream hits main=%d pin=%d, want main=0 pin=1", main.hits, pin.hits)
	}
}

// TestChatDispatchAccountPinFullChain：account 模式按生产真实形状构造目标
// （{account, GroupID:"", acc_pin}——会话不持久化承载分组）经消费侧承载分组
// 解析收敛单账户，usage 归属按实际派发账户记账（accountId=acc_pin，
// groupId=承载分组 group_pin）。
func TestChatDispatchAccountPinFullChain(t *testing.T) {
	fixture := newChainFixture(t)
	shortenChainWaitBudgets(t, fixture)
	main := newChatDispatchUpstream(t, chatDispatchSmokeBody)
	pin := newChatDispatchUpstream(t, chatDispatchSmokeBody)
	seedChatDispatchPinGroup(t, fixture, main.server.URL, pin.server.URL, "active")
	withChatDispatchAccountGroups(t, "group_pin")
	chain, spoolDir := composeChatDispatchChain(t, fixture)

	status, body := chatDispatchRequest(t, chain, fixture, chatDispatchTarget{Mode: "account", GroupID: "", AccountID: "acc_pin"}, true)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if pin.hits != 1 || main.hits != 0 {
		t.Fatalf("upstream hits main=%d pin=%d, want main=0 pin=1", main.hits, pin.hits)
	}
	records := waitForSpoolRecords(t, spoolDir)
	var successRecord map[string]any
	for _, record := range records {
		if success, _ := record["success"].(bool); success {
			successRecord = record
			break
		}
	}
	if successRecord == nil {
		t.Fatalf("spool 缺成功记录: %v", records)
	}
	if got := successRecord["accountId"]; got != "acc_pin" {
		t.Fatalf("usage accountId = %v, want acc_pin", got)
	}
	if got := successRecord["groupId"]; got != "group_pin" {
		t.Fatalf("usage groupId = %v, want group_pin（账户 BoundGroupID 记账）", got)
	}
	if got := successRecord["apiKeyId"]; got != "key_1" {
		t.Fatalf("usage apiKeyId = %v, want key_1（鉴权主体照常）", got)
	}
}

// TestChatDispatchAccountMultiGroupConvergesFullChain：account 会话属于多个
// 启用分组时，逐组解析收敛到该账户单元素（启用分组列表第二位的承载分组命中），
// 生效分组与 usage 归属都落在承载分组。
func TestChatDispatchAccountMultiGroupConvergesFullChain(t *testing.T) {
	fixture := newChainFixture(t)
	shortenChainWaitBudgets(t, fixture)
	main := newChatDispatchUpstream(t, chatDispatchSmokeBody)
	pin := newChatDispatchUpstream(t, chatDispatchSmokeBody)
	seedChatDispatchPinGroup(t, fixture, main.server.URL, pin.server.URL, "active")
	// 第二个启用分组（启用但不含 acc_pin）：验证逐组解析跳过无命中组。
	now := "2026-09-04T00:00:00.000Z"
	if _, err := fixture.db.Exec(`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type)
		VALUES ('group_other', ?, 'openai', 1, 'personal')`, fixture.systemAccount); err != nil {
		t.Fatalf("seed other group: %v", err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
		VALUES ('group_other', ?, ?, 1, ?)`, fixture.systemAccount, fixture.accountID, now); err != nil {
		t.Fatalf("seed other binding: %v", err)
	}
	// 承载分组（group_pin）位于启用分组列表第二位。
	withChatDispatchAccountGroups(t, "group_other", "group_pin")
	chain, spoolDir := composeChatDispatchChain(t, fixture)

	status, body := chatDispatchRequest(t, chain, fixture, chatDispatchTarget{Mode: "account", GroupID: "", AccountID: "acc_pin"}, true)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if pin.hits != 1 || main.hits != 0 {
		t.Fatalf("upstream hits main=%d pin=%d, want main=0 pin=1（收敛单账户）", main.hits, pin.hits)
	}
	records := waitForSpoolRecords(t, spoolDir)
	var successRecord map[string]any
	for _, record := range records {
		if success, _ := record["success"].(bool); success {
			successRecord = record
			break
		}
	}
	if successRecord == nil {
		t.Fatalf("spool 缺成功记录: %v", records)
	}
	if got := successRecord["accountId"]; got != "acc_pin" {
		t.Fatalf("usage accountId = %v, want acc_pin", got)
	}
	if got := successRecord["groupId"]; got != "group_pin" {
		t.Fatalf("usage groupId = %v, want group_pin（承载分组）", got)
	}
}

// TestChatDispatchUnpinnedRequestUnchanged：无目标（外部请求 / api_key 模式）
// 的调度路径与现状一致——Key 默认路由分组命中，绑定分组零命中。
func TestChatDispatchUnpinnedRequestUnchanged(t *testing.T) {
	fixture := newChainFixture(t)
	shortenChainWaitBudgets(t, fixture)
	main := newChatDispatchUpstream(t, strings.Replace(chatDispatchSmokeBody, "CONTENT", "主分组内容", 1))
	pin := newChatDispatchUpstream(t, chatDispatchSmokeBody)
	seedChatDispatchPinGroup(t, fixture, main.server.URL, pin.server.URL, "active")
	chain, _ := composeChatDispatchChain(t, fixture)

	status, body := chatDispatchRequest(t, chain, fixture, chatDispatchTarget{}, false)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if !strings.Contains(body, "主分组内容") {
		t.Fatalf("无目标请求未走 Key 默认路由: %s", body)
	}
	if main.hits != 1 || pin.hits != 0 {
		t.Fatalf("upstream hits main=%d pin=%d, want main=1 pin=0", main.hits, pin.hits)
	}
}

// TestChatDispatchTargetUnavailableUsesExistingNoAccountSemantics：绑定分组
// 解析不出可派发账户时走既有"无可用账户"错误语义（不新造错误分支），且
// 不回退到 Key 的其他绑定分组。
func TestChatDispatchTargetUnavailableUsesExistingNoAccountSemantics(t *testing.T) {
	fixture := newChainFixture(t)
	shortenChainWaitBudgets(t, fixture)
	main := newChatDispatchUpstream(t, chatDispatchSmokeBody)
	pin := newChatDispatchUpstream(t, chatDispatchSmokeBody)
	seedChatDispatchPinGroup(t, fixture, main.server.URL, pin.server.URL, "active")
	// 空绑定分组：存在但无任何账户。
	if _, err := fixture.db.Exec(`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type)
		VALUES ('group_empty', ?, 'openai', 1, 'personal')`, fixture.systemAccount); err != nil {
		t.Fatalf("seed empty group: %v", err)
	}
	chain, _ := composeChatDispatchChain(t, fixture)

	status, body := chatDispatchRequest(t, chain, fixture, chatDispatchTarget{Mode: "group", GroupID: "group_empty"}, true)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", status, body)
	}
	// 绑定目标会话的候选耗尽渲染既有固定文案族（候选排空的 route action、
	// fetch 层耗尽、抑制等待耗尽或协调/时间预算耗尽等既有 503 出口），不新造
	// 错误分支；诊断细节不得泄漏到客户端。
	fixedCopy := strings.Contains(body, "当前路由没有可用的上游账户") ||
		strings.Contains(body, "没有可用的上游账户") ||
		strings.Contains(body, "当前路由暂时没有可派发账户") ||
		strings.Contains(body, "上游暂时不可用，请重试") ||
		strings.Contains(body, "网关请求时间预算已用尽") ||
		strings.Contains(body, "当前路由暂时无法继续派发") ||
		strings.Contains(body, "网关请求协调预算已到")
	if !fixedCopy {
		t.Fatalf("无可用账户语义文案不符: %s", body)
	}
	if strings.Contains(body, "最后一次尝试") || strings.Contains(body, "127.0.0.1:") {
		t.Fatalf("诊断细节泄漏到客户端: %s", body)
	}
	if main.hits != 0 || pin.hits != 0 {
		t.Fatalf("无可用账户不得派发上游: main=%d pin=%d", main.hits, pin.hits)
	}
}

// TestChatDispatchPinnedGroupExhaustedDoesNotEscape：绑定分组账户全部失败时
// 禁用 API Key 分组回退——Key 绑定的 group_main 不被尝试，走既有耗尽 503。
func TestChatDispatchPinnedGroupExhaustedDoesNotEscape(t *testing.T) {
	fixture := newChainFixture(t)
	shortenChainWaitBudgets(t, fixture)
	dead := newDeadUpstreamAddr(t)
	main := newChatDispatchUpstream(t, chatDispatchSmokeBody)
	pin := newChatDispatchUpstream(t, chatDispatchSmokeBody)
	seedChatDispatchPinGroup(t, fixture, main.server.URL, dead, "active")
	chain, _ := composeChatDispatchChain(t, fixture)

	status, body := chatDispatchRequest(t, chain, fixture, chatDispatchTarget{Mode: "group", GroupID: "group_pin"}, true)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if main.hits != 0 || pin.hits != 0 {
		t.Fatalf("绑定分组耗尽不得逃逸: main=%d pin=%d", main.hits, pin.hits)
	}
}

// TestChatDispatchGroupContextAlignsPinnedPolicy：窗口级分组上下文对齐——
// 高并发（动态调度）绑定分组的 scheduling policy 与 usage 窗口组取绑定分组。
func TestChatDispatchGroupContextAlignsPinnedPolicy(t *testing.T) {
	fixture := newChainFixture(t)
	now := "2026-09-04T00:00:00.000Z"
	if _, err := fixture.db.Exec(`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type, scheduling_policy_json)
		VALUES ('group_hc', ?, 'openai', 1, 'high_concurrency', '{"weighted":{}}')`, fixture.systemAccount); err != nil {
		t.Fatalf("seed hc group: %v", err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
		VALUES ('group_hc', ?, ?, 1, ?)`, fixture.systemAccount, fixture.accountID, now); err != nil {
		t.Fatalf("seed hc binding: %v", err)
	}
	access, err := fixture.cache.ResolveCachedGroupUsageAccessMetadataAsync(context.Background(), "group_hc", fixture.systemAccount)
	if err != nil || access == nil || access.SchedulingPolicy == nil {
		t.Fatalf("resolve hc group access: %v %+v", err, access)
	}
	chain := &gatewayChain{preauth: &gatewaypreauth.Service{RuntimeCache: fixture.cache}}
	dispatchContext := &gatewaypreauth.DispatchContext{
		GroupSchedulingPolicy: nil,
		UsageContext:          gatewaypreauth.GatewayFailureUsageContext{GroupID: "group_main"},
	}
	// group 模式生效分组 = 绑定分组（account 模式由 resolveChatDispatchTargetScope
	// 解析承载分组后传入同一入口）。
	if err := chain.applyChatDispatchGroupContext(context.Background(), fixture.systemAccount, "group_hc", dispatchContext); err != nil {
		t.Fatalf("applyChatDispatchGroupContext: %v", err)
	}
	if dispatchContext.GroupSchedulingPolicy == nil || *dispatchContext.GroupSchedulingPolicy == nil {
		t.Fatalf("调度策略未对齐到绑定分组: %+v", dispatchContext.GroupSchedulingPolicy)
	}
	if got := (*dispatchContext.GroupSchedulingPolicy)["weighted"]; got == nil {
		t.Fatalf("调度策略内容不符: %+v", *dispatchContext.GroupSchedulingPolicy)
	}
	if dispatchContext.UsageContext.GroupID != "group_hc" {
		t.Fatalf("usage 窗口组 = %q, want group_hc", dispatchContext.UsageContext.GroupID)
	}
}
