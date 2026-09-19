package main

// 合并路由（merge 模式）请求链路端到端（合并路由设计 3.1/3.4/3.5/B14-B18/T4，
// 复用 chain 测试 SQLite fixture 范式）：
//   a) 首组账号不能服务请求模型时，请求由第二组映射账号服务（自有组 + 授权
//      组两种 fixture），usage 行 group_id = 第二组、访问五元组成套；
//   b) 全池耗尽 → 现有耗尽终局契约（503 + upstream_retryable_error，上游全
//      500 可重试耗尽路径）；
//   c) 解析期逐片段配额批查（B14 权威门）：首片段被剔次片段服务；全部超限
//      → 429；
//   d) 速度优先慢样本观察写入的存储 Scope.GroupID = 账号所在组（degraded
//      runtime 读回断言）；
//   e) 请求级审计身份 = 窗口组、尝试级审计/usage 身份 = 账号所在组。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

const mergeTestNow = "2026-09-04T00:00:00.000Z"

// mergeSecondGroup 描述 merge 策略的第二绑定分组。
type mergeSecondGroup struct {
	groupID   string
	ownerID   string // 空 = 与 fixture 同租户（owner 组）；否则为跨租户授权组
	accountID string
	// authorizeGroup 为跨租户组种组授权行（group_authorized fixture）。
	authorizeGroup bool
}

// w1FreshCacheFor 在凭据更新后重建运行时缓存（缓存快照在构造时生成）。
func w1FreshCacheFor(t *testing.T, fixture *chainFixture) *gatewayruntimecache.Service {
	t.Helper()
	models, err := gatewayruntimecache.NewSQLReadModels(fixture.db, false, time.Now, nil, nil, nil)
	if err != nil {
		t.Fatalf("read models = %v", err)
	}
	selector, err := newChainAccountsSelectorWithStats(fixture.db, fixture.statsDB, false, "chain-test-secret", time.Now, 20)
	if err != nil {
		t.Fatalf("selector = %v", err)
	}
	models.SetAccountsSelector(selector)
	catalogSource, err := newChainCatalogSource(fixture.db, false)
	if err != nil {
		t.Fatalf("catalog source = %v", err)
	}
	models.SetCatalogSource(catalogSource)
	models.SetConcurrencySource(gatewayclientip.NewMemoryAccountConcurrency(nil))
	cache, err := gatewayruntimecache.New(models, gatewayruntimecache.Options{Clock: gatewayruntimecache.SystemClock()})
	if err != nil {
		t.Fatalf("runtime cache = %v", err)
	}
	t.Cleanup(cache.Close)
	return cache
}

// mergeSeedFixture 在 chainFixture 基线上种两组 merge 栈：group_main（acc_1，
// 支持 gpt-test）+ second（second.accountID，glm-test → ds-test 模型映射），
// key_1 切到 merge 策略 rs_merge。compatSupportedGPT 为第二组账号追加
// gpt-test 支持模型（全池两段场景用）。
func mergeSeedFixture(t *testing.T, second mergeSecondGroup, compatSupportedGPT bool) *chainFixture {
	t.Helper()
	fixture := newChainFixture(t)
	now := mergeTestNow
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(query, args...); err != nil {
			t.Fatalf("seed merge fixture: %v: %v", query, err)
		}
	}
	ownerID := second.ownerID
	if ownerID == "" {
		ownerID = fixture.systemAccount
	}
	if ownerID != fixture.systemAccount {
		seed(`INSERT INTO system_accounts (id, status, image_generation_enabled) VALUES (?, 'active', 1)`, ownerID)
	}
	seed(`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type) VALUES (?, ?, 'openai', 1, 'personal')`,
		second.groupID, ownerID)
	seed(`INSERT INTO accounts (
			id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
			name, type, status, schedulable, credentials_encrypted, deleted_at
		) VALUES (?, ?, 'openai', 'prof_1', 'openai', 'v1', '合并第二组账户', 'api_key', 'active', 1, ?, NULL)`,
		second.accountID, ownerID, w1vEncrypt(t, map[string]any{"api_key": "sk-merge-compat-key"}))
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at) VALUES (?, ?, ?, 1, ?)`,
		second.groupID, ownerID, second.accountID, now)
	seed(`INSERT INTO account_model_mappings (account_id, provider_code, source_model, source_endpoint_family, upstream_model, upstream_endpoint_family, enabled, created_at, updated_at)
		VALUES (?, 'openai', 'glm-test', 'chat_completions', 'ds-test', 'chat_completions', 1, ?, ?)`, second.accountID, now, now)
	// 路由层模型过滤要求映射的上游模型本身在支持模型内（modelfilter.go
	// isMappingAllowedBySupportedModels）。
	seed(`INSERT INTO account_supported_models (account_id, provider_code, model, created_at) VALUES (?, 'openai', 'ds-test', ?)`,
		second.accountID, now)
	if compatSupportedGPT {
		seed(`INSERT INTO account_supported_models (account_id, provider_code, model, created_at) VALUES (?, 'openai', 'gpt-test', ?)`,
			second.accountID, now)
	}
	if second.authorizeGroup {
		seed(`CREATE TABLE IF NOT EXISTS resource_authorization_grants (
			id TEXT PRIMARY KEY, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL,
			resource_owner_system_account_id TEXT, grantee_type TEXT, grantee_team_id TEXT,
			grantee_system_account_id TEXT, status TEXT NOT NULL DEFAULT 'active', limits_json TEXT)`)
		seed(`INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, scope, status, effective_source_type, effective_source_team_id)
			VALUES (?, 'group', ?, ?, ?, 'usage', 'active', 'team', 'team_merge_a')`, "ra_"+second.groupID, second.groupID, ownerID, fixture.systemAccount)
		seed(`INSERT INTO group_authorization_settings (authorization_id, system_account_id, group_id, enabled, group_type, scheduling_policy_json)
			VALUES (?, ?, ?, 1, 'personal', NULL)`, "ra_"+second.groupID, fixture.systemAccount, second.groupID)
	}
	seed(`INSERT INTO route_strategies (id, system_account_id, name, mode, config_json, status) VALUES ('rs_merge', ?, '合并路由', 'merge', NULL, 'active')`,
		fixture.systemAccount)
	seed(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at)
		VALUES ('rsg_m1', 'rs_merge', ?, 'group_main', 1, 1, 'active', ?)`, fixture.systemAccount, now)
	seed(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at)
		VALUES ('rsg_m2', 'rs_merge', ?, ?, 2, 1, 'active', ?)`, fixture.systemAccount, second.groupID, now)
	seed(`UPDATE api_keys SET route_strategy_id = 'rs_merge' WHERE id = 'key_1'`)
	fixture.cache = w1FreshCacheFor(t, fixture)
	return fixture
}

// mergePointAccountAt 把账号指向 mock 上游并重建运行时缓存（缓存快照在构造
// 时生成）。
func mergePointAccountAt(t *testing.T, fixture *chainFixture, accountID, baseURL string) {
	t.Helper()
	if _, err := fixture.db.Exec(`UPDATE accounts SET credentials_encrypted = ? WHERE id = ?`,
		w1vEncrypt(t, map[string]any{"api_key": "sk-merge-upstream", "base_url": baseURL}), accountID); err != nil {
		t.Fatalf("update account credentials: %v", err)
	}
	fixture.cache = w1FreshCacheFor(t, fixture)
}

// mergeWaitForSpoolRecords 轮询 spool 目录直到至少 minimum 条 usage 记录。
func mergeWaitForSpoolRecords(t *testing.T, dir string, minimum int) []gatewayusage.UsageRecordInput {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		records := mergeSpoolRecords(dir)
		if len(records) >= minimum {
			return records
		}
		time.Sleep(25 * time.Millisecond)
	}
	return mergeSpoolRecords(dir)
}

func mergeSpoolRecords(dir string) []gatewayusage.UsageRecordInput {
	var records []gatewayusage.UsageRecordInput
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		var record gatewayusage.UsageRecordInput
		if json.Unmarshal(data, &record) == nil {
			records = append(records, record)
		}
		return nil
	})
	return records
}

// mergeComposeChain 组装真实链并返回 spool 目录；mutate 供注入
// LatencyService / 审计派发器等。
func mergeComposeChain(t *testing.T, fixture *chainFixture, mutate func(*chainRuntimeDeps)) (*gatewayChain, string, func()) {
	t.Helper()
	spoolRoot, err := os.MkdirTemp("", "merge-spool-")
	if err != nil {
		t.Fatalf("create spool root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(spoolRoot) })
	spoolDir := filepath.Join(spoolRoot, "spool")
	deps := chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, spoolDir)
	if mutate != nil {
		mutate(&deps)
	}
	chain, shutdown, err := composeGatewayChain(deps)
	if err != nil {
		t.Fatalf("compose gateway chain: %v", err)
	}
	return chain, spoolDir, shutdown
}

// a) 自有组场景：首组（group_main）账号不支持 glm-test，请求由第二组
// group_compat 的映射账号服务；usage 行 group_id = 第二组、五元组成套。
func TestMergeRouteServesBySecondGroupMappedAccount(t *testing.T) {
	compat := mergeSecondGroup{groupID: "group_compat", accountID: "acc_compat"}
	fixture := mergeSeedFixture(t, compat, false)

	upstreamModel := ""
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		upstreamModel = body.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-merge-a","object":"chat.completion","model":"ds-test","choices":[{"index":0,"message":{"role":"assistant","content":"合并路由命中"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	mergePointAccountAt(t, fixture, compat.accountID, upstream.URL)

	chain, spoolDir, shutdown := mergeComposeChain(t, fixture, nil)
	defer shutdown()
	server := httptest.NewServer(chain)
	defer server.Close()

	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"glm-test","messages":[{"role":"user","content":"你好"}]}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+fixture.apiKeySecret)
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("POST /v1/chat/completions: %v", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	bodyText := string(body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, bodyText)
	}
	if !strings.Contains(bodyText, "合并路由命中") {
		t.Fatalf("body missing merged content: %s", bodyText)
	}
	// 跨组模型映射生效：上游收到映射后的模型。
	if upstreamModel != "ds-test" {
		t.Fatalf("upstream model = %q, want ds-test", upstreamModel)
	}

	records := mergeWaitForSpoolRecords(t, spoolDir, 1)
	var success *gatewayusage.UsageRecordInput
	for index := range records {
		if records[index].Success && records[index].AccountID == compat.accountID {
			success = &records[index]
			break
		}
	}
	if success == nil {
		t.Fatalf("no success usage record for %s: %+v", compat.accountID, records)
	}
	// 按账号所属组记账（3.5）：group_id 与访问五元组整体取账号组。
	if success.GroupID != compat.groupID {
		t.Errorf("usage GroupID = %q, want %s", success.GroupID, compat.groupID)
	}
	if success.GroupOwnerSystemAccountID != fixture.systemAccount ||
		success.GroupAccessType != gatewayusage.GroupAccessTypeOwner {
		t.Errorf("usage group tuple = %q/%q, want owner tuple of %s",
			success.GroupOwnerSystemAccountID, success.GroupAccessType, compat.groupID)
	}
	if success.AccountID != compat.accountID {
		t.Errorf("usage AccountID = %q, want %s", success.AccountID, compat.accountID)
	}
}

// a2) 授权组场景（group_authorized fixture）：第二组为跨租户授权组，请求由
// 授权组账号服务；usage 五元组取授权组的组元组（owner/授权 ID/来源成套）。
func TestMergeRouteServesByAuthorizedGroupAccount(t *testing.T) {
	compat := mergeSecondGroup{groupID: "group_auth", ownerID: "sys_other_merge", accountID: "acc_auth", authorizeGroup: true}
	fixture := mergeSeedFixture(t, compat, false)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-merge-a2","object":"chat.completion","model":"ds-test","choices":[{"index":0,"message":{"role":"assistant","content":"授权组合并命中"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	mergePointAccountAt(t, fixture, compat.accountID, upstream.URL)

	chain, spoolDir, shutdown := mergeComposeChain(t, fixture, nil)
	defer shutdown()
	server := httptest.NewServer(chain)
	defer server.Close()

	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"glm-test","messages":[{"role":"user","content":"你好"}]}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+fixture.apiKeySecret)
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("POST /v1/chat/completions: %v", err)
	}
	defer response.Body.Close()
	_, _ = io.ReadAll(response.Body)
	records := mergeWaitForSpoolRecords(t, spoolDir, 1)
	var success *gatewayusage.UsageRecordInput
	for index := range records {
		if records[index].Success && records[index].AccountID == compat.accountID {
			success = &records[index]
			break
		}
	}
	if success == nil {
		t.Fatalf("no success usage record for %s: %+v", compat.accountID, records)
	}
	if success.GroupID != compat.groupID {
		t.Errorf("usage GroupID = %q, want %s", success.GroupID, compat.groupID)
	}
	// 授权组五元组成套：owner = 组属租户、accessType = authorized、
	// 授权 ID / 来源类型 / 来源团队取组授权行。
	if success.GroupOwnerSystemAccountID != compat.ownerID ||
		success.GroupAccessType != gatewayusage.GroupAccessTypeAuthorized ||
		success.GroupAuthorizationID != "ra_"+compat.groupID ||
		success.GroupAuthorizationSourceType != "team" ||
		success.GroupAuthorizationSourceTeamID != "team_merge_a" {
		t.Errorf("usage group tuple incomplete: owner=%q accessType=%q authID=%q sourceType=%q teamID=%q",
			success.GroupOwnerSystemAccountID, success.GroupAccessType, success.GroupAuthorizationID,
			success.GroupAuthorizationSourceType, success.GroupAuthorizationSourceTeamID)
	}
}

// b) 全池耗尽：两组账号的上游全部失败 → 现有耗尽终局契约
// （503 + upstream_retryable_error），不发生分组回退。
func TestMergeRoutePoolExhaustedTerminalContract(t *testing.T) {
	compat := mergeSecondGroup{groupID: "group_compat", accountID: "acc_compat"}
	fixture := mergeSeedFixture(t, compat, true)

	failMain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"主组故障"}}`))
	}))
	defer failMain.Close()
	failCompat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"兼容组故障"}}`))
	}))
	defer failCompat.Close()
	mergePointAccountAt(t, fixture, fixture.accountID, failMain.URL)
	mergePointAccountAt(t, fixture, compat.accountID, failCompat.URL)

	chain, _, shutdown := mergeComposeChain(t, fixture, nil)
	defer shutdown()
	status, body := w1vServeV1(t, chain, fixture.apiKeySecret, `{"model":"gpt-test","messages":[{"role":"user","content":"你好"}]}`, nil)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", status, body)
	}
	// 现有耗尽终局契约：池内尝试全部失败后 503 + upstream_retryable_error
	// （既有的客户端可重试语义；两个账号都被尝试，未发生分组回退）。
	if !strings.Contains(body, "upstream_retryable_error") {
		t.Fatalf("body missing upstream_retryable_error: %s", body)
	}
}

// e) 审计双身份：全池两账号先后失败时，尝试级审计行 group_id 各取所服务账
// 号所在组，请求级审计身份保持窗口组（首片段组）。
func TestMergeRouteAuditAttemptGroupIdentity(t *testing.T) {
	compat := mergeSecondGroup{groupID: "group_compat", accountID: "acc_compat"}
	fixture := mergeSeedFixture(t, compat, true)

	failMain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"主组故障"}}`))
	}))
	defer failMain.Close()
	failCompat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"兼容组故障"}}`))
	}))
	defer failCompat.Close()
	mergePointAccountAt(t, fixture, fixture.accountID, failMain.URL)
	mergePointAccountAt(t, fixture, compat.accountID, failCompat.URL)

	dispatcher := &mergeCapturingAuditDispatcher{}
	chain, spoolDir, shutdown := mergeComposeChain(t, fixture, func(deps *chainRuntimeDeps) {
		deps.AuditLogEnabled = func() bool { return true }
		deps.AuditUsageDispatch = dispatcher
	})
	defer shutdown()

	status, body := w1vServeV1(t, chain, fixture.apiKeySecret, `{"model":"gpt-test","messages":[{"role":"user","content":"你好"}]}`, nil)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", status, body)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(dispatcher.logs) == 0 {
		time.Sleep(25 * time.Millisecond)
	}
	if len(dispatcher.logs) == 0 {
		t.Fatal("audit log 未派发")
	}
	log := dispatcher.logs[len(dispatcher.logs)-1]
	// 请求级审计身份 = 窗口组（首片段组 group_main）。
	if log.GroupID != fixture.groupID {
		t.Errorf("request-level audit GroupID = %q, want %s", log.GroupID, fixture.groupID)
	}
	// 尝试级审计行 group_id 各取所服务账号所在组。
	attemptGroups := map[string]string{}
	for _, attempt := range log.Attempts {
		attemptGroups[attempt.AccountID] = attempt.GroupID
	}
	if attemptGroups[fixture.accountID] != fixture.groupID {
		t.Errorf("attempt(%s) GroupID = %q, want %s", fixture.accountID, attemptGroups[fixture.accountID], fixture.groupID)
	}
	if attemptGroups[compat.accountID] != compat.groupID {
		t.Errorf("attempt(%s) GroupID = %q, want %s", compat.accountID, attemptGroups[compat.accountID], compat.groupID)
	}

	// 尝试级 usage 失败记录同样按账号组记账（3.5）。
	records := mergeWaitForSpoolRecords(t, spoolDir, 2)
	failedGroups := map[string]string{}
	for _, record := range records {
		if !record.Success && record.AccountID != "" {
			failedGroups[record.AccountID] = record.GroupID
		}
	}
	if failedGroups[fixture.accountID] != fixture.groupID {
		t.Errorf("failed usage(%s) GroupID = %q, want %s", fixture.accountID, failedGroups[fixture.accountID], fixture.groupID)
	}
	if failedGroups[compat.accountID] != compat.groupID {
		t.Errorf("failed usage(%s) GroupID = %q, want %s", compat.accountID, failedGroups[compat.accountID], compat.groupID)
	}
}

// d) 速度优先：慢样本观察写入的存储 Scope.GroupID = 账号所在组（解析期窗口
// 组后移后与账号组一致），degraded runtime 读回断言。
func TestMergeRouteSpeedFirstSlowSampleScopeFollowsAccountGroup(t *testing.T) {
	compat := mergeSecondGroup{groupID: "group_compat", accountID: "acc_compat"}
	fixture := mergeSeedFixture(t, compat, false)
	// merge 策略配置速度优先（首字截止 30ms + 单样本触发降级）。
	if _, err := fixture.db.Exec(`UPDATE route_strategies SET config_json = ? WHERE id = 'rs_merge'`,
		`{"normalRoutingConfig":{"schedulingPreference":"speed_first","firstByteDeadlineMs":30,"speedFirstConfig":{"slowTriggerCount":1}}}`); err != nil {
		t.Fatalf("update strategy config: %v", err)
	}
	// 运行时缓存快照在构造时生成：改写策略配置后必须重建。
	fixture.cache = w1FreshCacheFor(t, fixture)

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl-merge-d","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"慢"}}]}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer slow.Close()
	mergePointAccountAt(t, fixture, compat.accountID, slow.URL)

	var latency *gatewayproxyhealth.LatencyDegradationService
	chain, _, shutdown := mergeComposeChain(t, fixture, func(deps *chainRuntimeDeps) {
		latency = gatewayproxyhealth.NewLatencyDegradationService(
			gatewayproxyhealth.NewMemoryRuntimeStateStore(nil), nil, gatewayproxyhealth.LatencyDegradationOptions{})
		deps.LatencyService = latency
	})
	defer shutdown()

	status, body := w1vServeV1(t, chain, fixture.apiKeySecret, `{"model":"glm-test","stream":true,"messages":[{"role":"user","content":"你好"}]}`, nil)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s", status, body)
	}

	var items []gatewayproxyhealth.DegradedRuntimeItem
	var err error
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		items, err = latency.ListNormalRouteLatencyDegradedRuntime(context.Background(),
			gatewayproxyhealth.ListNormalRouteLatencyDegradedRuntimeInput{
				SystemAccountID:  &fixture.systemAccount,
				RouteStrategyIDs: []string{"rs_merge"},
			})
		if err == nil && len(items) > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("list degraded runtime: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("慢样本未写入降级运行态")
	}
	var item *gatewayproxyhealth.DegradedRuntimeItem
	for index := range items {
		if items[index].AccountID == compat.accountID {
			item = &items[index]
			break
		}
	}
	if item == nil {
		t.Fatalf("degraded items = %+v, want %s", items, compat.accountID)
	}
	// B25（3.6）：键与存储 Scope 同源，Scope.GroupID = 账号所在组。
	if item.ScopeGroupID != compat.groupID {
		t.Errorf("degraded ScopeGroupID = %q, want %s", item.ScopeGroupID, compat.groupID)
	}
	if item.ScopeRouteStrategyID != "rs_merge" {
		t.Errorf("degraded ScopeRouteStrategyID = %q, want rs_merge", item.ScopeRouteStrategyID)
	}
}

// c) 解析期逐片段配额批查（B14 权威门，组合根层）：首片段配额超限被剔、次
// 片段正常服务且窗口组后移；全部片段超限 → 429 终局。
func TestMergeRouteResolverPerSegmentQuotaGate(t *testing.T) {
	compat := mergeSecondGroup{groupID: "group_compat", accountID: "acc_compat"}
	fixture := mergeSeedFixture(t, compat, true)
	normal := gatewayrouting.NewNormalModelRouteService(chainRoutingCache{cache: fixture.cache}, chainCapabilityFilter{})
	request := mergeRouteRequest("gpt-test")
	record := &gatewayruntimecache.GatewayAPIKeyRow{
		ID: "key_1", SystemAccountID: fixture.systemAccount,
		RouteStrategyID: "rs_merge", RouteStrategyMode: gatewayruntimecache.RouteStrategyModeMerge,
		SelectedGroupID: fixture.groupID, Status: "active",
		GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
			{ID: "rsg_m1", APIKeyID: "key_1", GroupID: fixture.groupID, Status: "active", GroupEnabled: 1, Priority: 1},
			{ID: "rsg_m2", APIKeyID: "key_1", GroupID: compat.groupID, Status: "active", GroupEnabled: 1, Priority: 2},
		},
	}

	// 控制臂：无配额否决 → 扁平池两段全保留，窗口组 = 首片段组。
	resolver := &chainRouteResolver{cache: fixture.cache, normal: normal, quota: &mergeStubQuota{}}
	result, err := resolver.ResolveNormalGatewayModelRoute(context.Background(), gatewaypreauth.NormalRouteInput{
		Req: request, APIKeyRecord: record,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if result.Outcome != gatewaypreauth.NormalRouteOutcomeSelected {
		t.Fatalf("outcome = %s reason=%s", result.Outcome, result.Reason)
	}
	if result.RouteSource != "merged" || len(result.Accounts) != 2 {
		t.Fatalf("routeSource=%q accounts=%d, want merged/2", result.RouteSource, len(result.Accounts))
	}
	if result.GroupID != fixture.groupID || result.APIKeyRecord.SelectedGroupID != fixture.groupID {
		t.Fatalf("window = %q selected = %q, want %s", result.GroupID, result.APIKeyRecord.SelectedGroupID, fixture.groupID)
	}
	for _, account := range result.Accounts {
		if account.BoundGroupID == nil {
			t.Fatalf("account %s 未组标", account.ID)
		}
	}
	// 剔除臂：首片段（group_main 账号）配额超限被剔 → 次片段服务，窗口组后
	// 移到次片段组，SelectedGroupID 同步。
	resolverCulled := &chainRouteResolver{cache: fixture.cache, normal: normal,
		quota: &mergeStubQuota{denied: map[string]bool{fixture.accountID: true}}}
	culled, err := resolverCulled.ResolveNormalGatewayModelRoute(context.Background(), gatewaypreauth.NormalRouteInput{
		Req: request, APIKeyRecord: record,
	})
	if err != nil {
		t.Fatalf("resolve culled: %v", err)
	}
	if culled.Outcome != gatewaypreauth.NormalRouteOutcomeSelected {
		t.Fatalf("culled outcome = %s", culled.Outcome)
	}
	if len(culled.Accounts) != 1 || culled.Accounts[0].ID != compat.accountID {
		t.Fatalf("culled accounts = %v, want [%s]", mergeAccountIDs(culled.Accounts), compat.accountID)
	}
	if culled.GroupID != compat.groupID || culled.APIKeyRecord.SelectedGroupID != compat.groupID {
		t.Fatalf("culled window = %q selected = %q, want %s", culled.GroupID, culled.APIKeyRecord.SelectedGroupID, compat.groupID)
	}
	if bound := culled.Accounts[0].BoundGroupID; bound == nil || *bound != compat.groupID {
		t.Fatalf("culled account stamp = %v, want %s", bound, compat.groupID)
	}
	// 终局臂：全部片段超限 → 429 + rate_limit_exceeded + 配额超限文案。
	resolverDenied := &chainRouteResolver{cache: fixture.cache, normal: normal,
		quota: &mergeStubQuota{deniedAll: true}}
	denied, err := resolverDenied.ResolveNormalGatewayModelRoute(context.Background(), gatewaypreauth.NormalRouteInput{
		Req: request, APIKeyRecord: record,
	})
	if err != nil {
		t.Fatalf("resolve denied: %v", err)
	}
	if denied.Outcome != gatewaypreauth.NormalRouteOutcomeFailed ||
		denied.StatusCode != 429 || denied.Code != "rate_limit_exceeded" ||
		denied.Type != "rate_limit_exceeded" {
		t.Fatalf("denied = %+v", denied)
	}
	if !strings.Contains(denied.Message, "额度已用完") {
		t.Fatalf("denied message = %q", denied.Message)
	}
}

// mergeStubQuota 是解析期配额批查的测试桩。
type mergeStubQuota struct {
	denied    map[string]bool
	deniedAll bool
	calls     int
}

func (q *mergeStubQuota) CheckBatchAsync(_ context.Context, _ gatewayruntimecache.GroupUsageAccessMetadata, accounts []gatewaydispatch.AccountCandidate) (map[string]gatewaydispatch.QuotaDecision, error) {
	q.calls++
	decisions := make(map[string]gatewaydispatch.QuotaDecision, len(accounts))
	for _, account := range accounts {
		decisions[account.ID] = gatewaydispatch.QuotaDecision{Allowed: !q.deniedAll && !q.denied[account.ID]}
	}
	return decisions, nil
}

// mergeCapturingAuditDispatcher 捕获最终化审计日志。
type mergeCapturingAuditDispatcher struct {
	logs []gatewayusage.AuditLogInput
}

func (d *mergeCapturingAuditDispatcher) DispatchAuditLog(_ gatewayusage.Ctx, input gatewayusage.AuditLogInput) {
	d.logs = append(d.logs, input)
}

func mergeRouteRequest(model string) *gatewaypreauth.GatewayRequest {
	request := gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/chat/completions", nil))
	request.Body = &gatewaybody.Request{
		RawBody: []byte(`{"model":"` + model + `"}`),
		Body:    map[string]any{"model": model},
		State:   &gatewaybody.BodyState{Model: &model},
	}
	return request
}

func mergeAccountIDs(accounts []gatewayruntimecache.OpenAIAccountSecret) []string {
	ids := make([]string, 0, len(accounts))
	for _, account := range accounts {
		ids = append(ids, account.ID)
	}
	return ids
}
