package main

// 请求级提前耗尽（L1）回归测试（请求级提前耗尽与负面缓存设计 §9）：
//
//   - 计数状态机：合取阈值（同桶 ≥3 只不同账户且合计 ≥5 次）恰在第 5 次、
//     第 3 只账户时触发；次数达 5 但账户仅 2 只不触发；触发后保持触发态；
//     兄弟 Key / 同账户瞬态重试按 attempt 合计、账户不虚增。
//   - 计数输入结构排除：传输类失败（HandleUpstreamRequestError）、诊断 /
//     非网关透传、媒体参数错误与音频业务拒绝透传（return_response）均不推进
//     计数；语义提交后失败与配置截止取消不经失败派发器（调用图排除）。
//   - 计数桶键 = usage 写库同源 upstream_model：映射桶与直连桶独立计数，
//     互不烧断。
//   - 端到端：3 账户 5 次完整非 2xx（含兄弟 Key 轮换）第 5 次后立即耗尽、
//     同桶剩余候选不被扫描（第 4 账户 0 次上游命中）、既有 503 固定文案契约
//     不变、响应不泄漏上游地址；2 账户 6 次不触发、自然耗尽。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaysession"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// httptestAlwaysRejectedUpstream 返回恒定 400 JSON 的 mock 上游（完整非 2xx、
// 非瞬态同账户重试状态——400 不进同账户瞬态重试预算，attempt 形状由 Key 池
// 轮换决定，夹具可精确断言命中次数）。
func httptestAlwaysRejectedUpstream(t *testing.T, hits *int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"request rejected by upstream"}}`))
	}))
	t.Cleanup(server.Close)
	return server
}

// requestFastExhaustInput 构造一次 gateway 流量的完整非 2xx 候选拒绝输入：
// 账户 ID / 上游状态码可变，其余沿用失败派发器单测夹具形状。请求按 gemini
// 路径形态携带模型 gpt-test（RequestModel 的路径来源，无需 body 管道），
// 计数桶键与 usage 模型提示取到非空请求模型；sink 同时挂为 Context
// （AddGatewayMetadata 只路由到 Context，审计标记断言依赖此处）。
func requestFastExhaustInput(response *gatewaydispatch.GatewayUpstreamResponse, sink *failureDispatchAuditSink, accountID string) gatewaydispatch.FailedUpstreamResponseInput {
	input := gatewayFailedResponseInput(response, sink, gatewayTrafficSource)
	input.AuditCapture.Context = sink
	input.Req = gatewaypreauth.NewGatewayRequest(
		httptest.NewRequest(http.MethodPost, "/v1beta/models/gpt-test:generateContent", nil))
	input.Account.ID = accountID
	input.Account.Name = "账户-" + accountID
	input.LastAttempt = &gatewaydispatch.UpstreamAttempt{
		AccountID: accountID, UpstreamURL: "https://upstream.example/chat",
		Status: response.Status(), HasStatus: true,
	}
	return input
}

// TestRequestFastExhaustStateConjunctionThreshold：合取阈值恰在"第 5 次失败
// 且第 3 只账户"同时成立的 attempt 触发；2 账户 5 次不触发；触发后保持触发态。
func TestRequestFastExhaustStateConjunctionThreshold(t *testing.T) {
	state := requestFastExhaustStateFromContext(withRequestFastExhaustState(context.Background()))
	if state == nil {
		t.Fatal("state must be installed in context")
	}
	// a1×2 + a2×2 = 4 次 2 账户：不触发。
	for i := 0; i < 4; i++ {
		account := "acc_1"
		if i >= 2 {
			account = "acc_2"
		}
		if _, triggered := state.observe("gpt-test", account); triggered {
			t.Fatalf("attempt %d must not trigger: 2 accounts < 3", i+1)
		}
	}
	// 第 5 次失败（第 3 只账户）：合取同时成立 → 触发。
	snapshot, triggered := state.observe("gpt-test", "acc_3")
	if !triggered {
		t.Fatal("3rd distinct account at 5th attempt must trigger")
	}
	if snapshot.DistinctAccountCount != 3 || snapshot.AttemptCount != 5 {
		t.Fatalf("snapshot = %+v want 3 accounts / 5 attempts", snapshot)
	}
	if want := []string{"acc_1", "acc_2", "acc_3"}; strings.Join(snapshot.FailedAccountIDs, ",") != strings.Join(want, ",") {
		t.Fatalf("failedAccountIds = %v want %v", snapshot.FailedAccountIDs, want)
	}
	// 触发态保持：后续同桶拒绝立即再触发（服务窗口展开后的快速终止）。
	if _, triggered := state.observe("gpt-test", "acc_4"); !triggered {
		t.Fatal("triggered bucket must stay triggered")
	}
	// 次数达 5 但账户仅 2 只：不触发（合取条件的另一独立桶）。
	other := requestFastExhaustStateFromContext(withRequestFastExhaustState(context.Background()))
	for i := 0; i < 5; i++ {
		account := "acc_1"
		if i >= 3 {
			account = "acc_2"
		}
		if _, triggered := other.observe("gpt-test", account); triggered {
			t.Fatalf("5 attempts across 2 accounts must not trigger (attempt %d)", i+1)
		}
	}
}

// TestRequestFastExhaustStateSiblingKeyAndTransientAccountDedup：同一账户的
// 兄弟 Key 轮换与瞬态重试把 attempt 合计推到 5，但账户计数仍为 1 → 不触发。
func TestRequestFastExhaustStateSiblingKeyAndTransientAccountDedup(t *testing.T) {
	state := requestFastExhaustStateFromContext(withRequestFastExhaustState(context.Background()))
	for i := 0; i < requestFastExhaustMinTotalAttempts; i++ {
		snapshot, triggered := state.observe("gpt-test", "acc_1")
		if triggered {
			t.Fatalf("same-account retries must not trigger (attempt %d)", i+1)
		}
		if i == requestFastExhaustMinTotalAttempts-1 {
			if snapshot.DistinctAccountCount != 1 || snapshot.AttemptCount != requestFastExhaustMinTotalAttempts {
				t.Fatalf("snapshot = %+v want 1 account / %d attempts", snapshot, requestFastExhaustMinTotalAttempts)
			}
		}
	}
}

// TestChainFailureDispatcherRequestFastExhaustTriggersAtConjunction：失败派发
// 器在 gateway 候选拒绝上计数；混合状态码（400/429/503/401，无任何状态码
// 分支）按同一规则在第 5 次触发，返回既有耗尽出口形状的 UpstreamAttemptError
// 并落 request_level_fast_exhausted 审计标记；未触发前 skip_account 行为不变。
func TestChainFailureDispatcherRequestFastExhaustTriggersAtConjunction(t *testing.T) {
	dispatcher := newFailureDispatcherForTest(nil)
	state := requestFastExhaustStateFromContext(withRequestFastExhaustState(context.Background()))
	ctx := context.WithValue(context.Background(), requestFastExhaustContextKey{}, state)

	statuses := []int{http.StatusBadRequest, http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusUnauthorized, http.StatusBadRequest}
	accounts := []string{"acc_1", "acc_1", "acc_2", "acc_2", "acc_3"}
	for i := range statuses {
		response := failureDispatchUpstreamResponse(t, statuses[i], "application/json", `{"error":{"message":"rejected"}}`)
		sink := &failureDispatchAuditSink{}
		result, err := dispatcher.HandleFailedUpstreamResponse(ctx, requestFastExhaustInput(response, sink, accounts[i]))
		if i < len(statuses)-1 {
			if err != nil {
				t.Fatalf("attempt %d must not abort: %v", i+1, err)
			}
			if result.Action != gatewaydispatch.FailedResponseActionSkipAccount {
				t.Fatalf("attempt %d action=%s want skip_account", i+1, result.Action)
			}
			for _, entry := range sink.metadata {
				if entry.label == "request_level_fast_exhausted" {
					t.Fatalf("attempt %d must not carry the fast-exhausted marker", i+1)
				}
			}
			continue
		}
		var attemptErr *gatewaydispatch.UpstreamAttemptError
		if !errors.As(err, &attemptErr) {
			t.Fatalf("attempt 5 must abort with UpstreamAttemptError, got %v", err)
		}
		if attemptErr.TerminalUpstreamFailure {
			t.Fatal("fast exhaustion must keep the non-terminal settle path (fallback groups stay reachable)")
		}
		if attemptErr.LastAttempt == nil || attemptErr.LastAttempt.AccountID != "acc_3" {
			t.Fatalf("lastAttempt must carry the triggering failure: %+v", attemptErr.LastAttempt)
		}
		if len(attemptErr.FailedAccountIDs) != 3 {
			t.Fatalf("failedAccountIds = %v want 3 accounts", attemptErr.FailedAccountIDs)
		}
		found := false
		for _, entry := range sink.metadata {
			if entry.label != "request_level_fast_exhausted" {
				continue
			}
			found = true
			if entry.metadata["upstreamModel"] != "gpt-test" {
				t.Fatalf("bucket key must be the request model hint: %v", entry.metadata["upstreamModel"])
			}
			if entry.metadata["distinctAccountCount"] != 3 || entry.metadata["attemptCount"] != 5 {
				t.Fatalf("marker counts wrong: %v", entry.metadata)
			}
		}
		if !found {
			t.Fatal("request_level_fast_exhausted audit marker missing on trigger")
		}
	}
}

// TestChainFailureDispatcherRequestFastExhaustExclusionsDoNotCount：传输类
// 失败、诊断与非网关透传、媒体参数错误与音频业务拒绝透传都不推进计数。
// 语义提交后失败与配置截止取消不经失败派发器（引擎错误臂），调用图排除。
func TestChainFailureDispatcherRequestFastExhaustExclusionsDoNotCount(t *testing.T) {
	dispatcher := newFailureDispatcherForTest(nil)
	state := requestFastExhaustStateFromContext(withRequestFastExhaustState(context.Background()))
	ctx := context.WithValue(context.Background(), requestFastExhaustContextKey{}, state)

	// 传输类失败：HandleUpstreamRequestError 不计数。
	transportInput := gatewaydispatch.UpstreamRequestErrorInput{
		UsageContext: gatewaypreauth.GatewayFailureUsageContext{TrafficSource: gatewayTrafficSource},
		AuditCapture: gatewaydispatch.AuditCapture{Sink: &failureDispatchAuditSink{}},
		Account:      gatewaydispatch.AccountCandidate{ID: "acc_transport", Name: "账户-传输"},
		UpstreamURL:  "https://upstream.example/chat",
		Error:        errors.New("dial tcp: connection refused"),
	}
	for i := 0; i < requestFastExhaustMinTotalAttempts+1; i++ {
		if _, err := dispatcher.HandleUpstreamRequestError(ctx, transportInput); err != nil {
			t.Fatalf("transport failure dispatch: %v", err)
		}
	}

	// 诊断 / 非网关流量的完整非 2xx：return_response 分支不计数。
	for _, trafficSource := range []string{"manual_account_test", "hybrid_scoring"} {
		response := failureDispatchUpstreamResponse(t, http.StatusTooManyRequests, "application/json", `{"error":"x"}`)
		result, err := dispatcher.HandleFailedUpstreamResponse(ctx, gatewayFailedResponseInput(response, &failureDispatchAuditSink{}, trafficSource))
		if err != nil {
			t.Fatalf("%s traffic must not abort: %v", trafficSource, err)
		}
		if result.Action != gatewaydispatch.FailedResponseActionReturnResponse {
			t.Fatalf("%s traffic action=%s want return_response", trafficSource, result.Action)
		}
	}

	// 媒体创建参数错误短路（video lane 400）与音频同步业务拒绝短路（audio
	// lane 400）：透传分支不计数。
	mediaResponse := failureDispatchUpstreamResponse(t, http.StatusBadRequest, "application/json", `{"error":"bad request"}`)
	mediaInput := requestFastExhaustInput(mediaResponse, &failureDispatchAuditSink{}, "acc_media")
	mediaInput.RequestLane = string(gatewayproto.LaneVideo)
	mediaResult, err := dispatcher.HandleFailedUpstreamResponse(ctx, mediaInput)
	if err != nil {
		t.Fatalf("media param short-circuit must not abort: %v", err)
	}
	if mediaResult.Action != gatewaydispatch.FailedResponseActionReturnResponse {
		t.Fatalf("media param short-circuit action=%s want return_response", mediaResult.Action)
	}
	audioInput := requestFastExhaustInput(mediaResponse, &failureDispatchAuditSink{}, "acc_audio")
	audioInput.RequestLane = string(gatewayproto.LaneAudio)
	audioResult, err := dispatcher.HandleFailedUpstreamResponse(ctx, audioInput)
	if err != nil {
		t.Fatalf("audio rejection short-circuit must not abort: %v", err)
	}
	if audioResult.Action != gatewaydispatch.FailedResponseActionReturnResponse {
		t.Fatalf("audio rejection short-circuit action=%s want return_response", audioResult.Action)
	}

	// 以上全部未计数：同桶再补 5 次真实候选拒绝恰在第 5 次触发（计数器未被
	// 排除路径污染）。
	for i := 0; i < requestFastExhaustMinTotalAttempts; i++ {
		response := failureDispatchUpstreamResponse(t, http.StatusBadRequest, "application/json", `{"error":"rejected"}`)
		account := "acc_1"
		if i > 0 {
			account = "acc_2"
		}
		if i > 3 {
			account = "acc_3"
		}
		_, err := dispatcher.HandleFailedUpstreamResponse(ctx, requestFastExhaustInput(response, &failureDispatchAuditSink{}, account))
		if i < requestFastExhaustMinTotalAttempts-1 && err != nil {
			t.Fatalf("clean attempt %d must not abort: %v", i+1, err)
		}
		if i == requestFastExhaustMinTotalAttempts-1 && err == nil {
			t.Fatal("5 clean attempts across 3 accounts must trigger after excluded paths stayed uncounted")
		}
	}
}

// fakeMappedModelResolver 把映射账户集合的请求模型解析到独立上游模型
// （gpt-test → gpt-test-mapped），其余账户直连（原样返回请求模型）。
type fakeMappedModelResolver struct{ mappedAccountIDs map[string]struct{} }

func (r fakeMappedModelResolver) ResolveUsageModel(account gatewayusage.UsageModelAccount, requestedModel string, _ string) gatewayusage.UsageModelResolution {
	if _, mapped := r.mappedAccountIDs[account.ID]; mapped {
		return gatewayusage.UsageModelResolution{UpstreamModel: requestedModel + "-mapped"}
	}
	return gatewayusage.UsageModelResolution{UpstreamModel: requestedModel}
}

// TestChainFailureDispatcherRequestFastExhaustBucketByUpstreamModel：计数桶
// 键 = usage 写库同源 upstream_model——映射桶（3 账户共享同一上游模型）
// 5 次触发，直连桶计数独立、互不烧断。
func TestChainFailureDispatcherRequestFastExhaustBucketByUpstreamModel(t *testing.T) {
	usageService := gatewayusage.NewService(nil, gatewayusage.ServiceConfig{}).WithModelResolver(fakeMappedModelResolver{mappedAccountIDs: map[string]struct{}{
		"acc_mapped": {}, "acc_m2": {}, "acc_m3": {},
	}})
	dispatcher := &chainFailureDispatcher{usage: usageService}
	state := requestFastExhaustStateFromContext(withRequestFastExhaustState(context.Background()))
	ctx := context.WithValue(context.Background(), requestFastExhaustContextKey{}, state)

	// 映射桶：acc_mapped×2 + acc_m2×2 + acc_m3×1 → 第 5 次触发。
	// 直连桶：acc_direct×2 → 仅计数、不触发（请求级计数不被映射桶烧断）。
	plan := []struct {
		account string
		status  int
		trigger bool
	}{
		{account: "acc_mapped", status: http.StatusBadRequest},
		{account: "acc_direct", status: http.StatusTooManyRequests},
		{account: "acc_mapped", status: http.StatusServiceUnavailable},
		{account: "acc_direct", status: http.StatusTooManyRequests},
		{account: "acc_m2", status: http.StatusForbidden},
		{account: "acc_m2", status: http.StatusTooManyRequests},
		{account: "acc_m3", status: http.StatusBadRequest, trigger: true},
	}
	for i, step := range plan {
		response := failureDispatchUpstreamResponse(t, step.status, "application/json", `{"error":"rejected"}`)
		sink := &failureDispatchAuditSink{}
		_, err := dispatcher.HandleFailedUpstreamResponse(ctx, requestFastExhaustInput(response, sink, step.account))
		if step.trigger {
			var attemptErr *gatewaydispatch.UpstreamAttemptError
			if !errors.As(err, &attemptErr) {
				t.Fatalf("mapped bucket attempt %d must trigger: %v", i+1, err)
			}
			for _, entry := range sink.metadata {
				if entry.label == "request_level_fast_exhausted" && entry.metadata["upstreamModel"] != "gpt-test-mapped" {
					t.Fatalf("mapped bucket key wrong: %v", entry.metadata["upstreamModel"])
				}
			}
			continue
		}
		if err != nil {
			t.Fatalf("attempt %d (%s) must not abort: %v", i+1, step.account, err)
		}
	}
}

// requestFastExhaustSeedAccount 以既有链夹具形状补一个指向 mock 上游的账户
// （API Key 池数量决定兄弟 Key 轮换的 attempt 上限）。
func requestFastExhaustSeedAccount(t *testing.T, fixture *chainFixture, accountID, upstreamURL string, apiKeyCount int, localPriority int) {
	t.Helper()
	keys := make([]any, 0, apiKeyCount)
	for i := 0; i < apiKeyCount; i++ {
		keys = append(keys, "sk-upstream-key-"+accountID+"-"+strings.Repeat("k", i+1))
	}
	now := "2026-09-04T00:00:00.000Z"
	credentials := map[string]any{"api_key": keys[0], "api_keys": keys, "base_url": upstreamURL,
		"supported_endpoint_modes": []string{"chat_json", "chat_sse", "responses_json", "responses_sse"}}
	// vendor_code='gpt' 是账户 API Key 池隔离的供应商支持门
	//（isAccountAPIKeyPoolProviderSupported）：缺失时兄弟 Key 不轮换。
	if _, err := fixture.db.Exec(fixtureBind(`INSERT INTO accounts (id, system_account_id, vendor_code, provider_protocol_profile_id, protocol_code, protocol_version, name, type, status, schedulable, credentials_encrypted, deleted_at, health_check_model) VALUES (?, ?, 'gpt', 'prof_1', 'openai', 'v1', ?, 'api_key', 'active', 1, ?, NULL, 'gpt-test')`),
		accountID, fixture.systemAccount, "账户-"+accountID, mustEncryptCredentials(t, credentials)); err != nil {
		t.Fatalf("seed account %s: %v", accountID, err)
	}
	if _, err := fixture.db.Exec(fixtureBind(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, local_priority, created_at) VALUES (?, ?, ?, 1, ?, ?)`),
		fixture.groupID, fixture.systemAccount, accountID, localPriority, now); err != nil {
		t.Fatalf("seed group account %s: %v", accountID, err)
	}
	if _, err := fixture.db.Exec(fixtureBind(`INSERT INTO account_supported_models (account_id, model, model_id, created_at) VALUES (?, 'gpt-test', 'mdl_gpt-test', ?)`), accountID, now); err != nil {
		t.Fatalf("seed supported model %s: %v", accountID, err)
	}
	chainSeedAccountConnection(t, fixture.db, accountID, upstreamURL)
}

// TestGatewayChainRequestFastExhaustStopsScanningBucket：acc_1（3 Key 轮换）
// + acc_2 + acc_3 的 5 次完整非 2xx 触发提前耗尽——第 5 次失败后同桶剩余候选
// （acc_4）不被扫描（0 次上游命中），客户端拿到既有统一耗尽出口的 503 固定
// 文案，响应不泄漏上游地址。
func TestGatewayChainRequestFastExhaustStopsScanningBucket(t *testing.T) {
	fixture := newChainFixture(t)
	var hits int32
	upstream := httptestAlwaysRejectedUpstream(t, &hits)
	requestFastExhaustSeedAccount(t, fixture, "acc_2", upstream.URL, 1, 10)
	requestFastExhaustSeedAccount(t, fixture, "acc_3", upstream.URL, 1, 11)
	requestFastExhaustSeedAccount(t, fixture, "acc_4", upstream.URL, 1, 12)
	if _, err := fixture.db.Exec(fixtureBind(`UPDATE accounts SET credentials_encrypted = ? WHERE id = ?`),
		mustEncryptCredentials(t, map[string]any{"api_key": "sk-upstream-key-acc_1-a",
			"api_keys": []any{"sk-upstream-key-acc_1-a", "sk-upstream-key-acc_1-b", "sk-upstream-key-acc_1-c"},
			"base_url": upstream.URL, "supported_endpoint_modes": []string{"chat_json", "chat_sse", "responses_json", "responses_sse"}}), fixture.accountID); err != nil {
		t.Fatalf("update acc_1 credentials: %v", err)
	}
	chainPointAccountAtUpstream(t, fixture.db, fixture.accountID, upstream.URL, "chat_json", "chat_sse", "responses_json", "responses_sse")

	chain, shutdown, err := composeGatewayChain(chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, filepath.Join(t.TempDir(), "spool")))
	if err != nil {
		t.Fatalf("compose gateway chain: %v", err)
	}
	defer shutdown()
	server := httptest.NewServer(chain)
	defer server.Close()

	status, raw := chainV1ChatRequest(t, server.URL, fixture.apiKeySecret,
		`{"model":"gpt-test","messages":[{"role":"user","content":"你好"}]}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503: %s", status, raw)
	}
	if !strings.Contains(raw, "上游暂时不可用，请重试") {
		t.Fatalf("fixed exhaustion copy missing: %s", raw)
	}
	if strings.Contains(raw, upstream.URL) || strings.Contains(raw, "acc_1") {
		t.Fatal("exhaustion response must not leak upstream address or account identity")
	}
	// 触发形状：acc_1 兄弟 Key 轮换 3 次 + acc_2 + acc_3 各 1 次 = 5 次
	// 完整非 2xx（3 只不同账户）——第 5 次触发；acc_4 不被扫描。
	if got := atomic.LoadInt32(&hits); got != 5 {
		t.Fatalf("upstream hits=%d want exactly 5 (3 sibling-key + 2 next-account) before fast exhaustion", got)
	}
}

// TestGatewayChainRequestFastExhaustNegativeCacheRetryHitsOnce：首次调用 L1
// 提前耗尽并懒写入 L2 负面缓存后，同会话重试同一请求在第一次候选拒绝即命中
// ——第二次请求上游调用次数恰为 1（一次上游 RTT 后秒级进入既有耗尽出口），
// 客户端契约不变。
func TestGatewayChainRequestFastExhaustNegativeCacheRetryHitsOnce(t *testing.T) {
	fixture := newChainFixture(t)
	var hits int32
	upstream := httptestAlwaysRejectedUpstream(t, &hits)
	requestFastExhaustSeedAccount(t, fixture, "acc_2", upstream.URL, 1, 10)
	requestFastExhaustSeedAccount(t, fixture, "acc_3", upstream.URL, 1, 11)
	requestFastExhaustSeedAccount(t, fixture, "acc_4", upstream.URL, 1, 12)
	if _, err := fixture.db.Exec(fixtureBind(`UPDATE accounts SET credentials_encrypted = ? WHERE id = ?`),
		mustEncryptCredentials(t, map[string]any{"api_key": "sk-upstream-key-acc_1-a",
			"api_keys": []any{"sk-upstream-key-acc_1-a", "sk-upstream-key-acc_1-b", "sk-upstream-key-acc_1-c"},
			"base_url": upstream.URL, "supported_endpoint_modes": []string{"chat_json", "chat_sse", "responses_json", "responses_sse"}}), fixture.accountID); err != nil {
		t.Fatalf("update acc_1 credentials: %v", err)
	}
	chainPointAccountAtUpstream(t, fixture.db, fixture.accountID, upstream.URL, "chat_json", "chat_sse", "responses_json", "responses_sse")

	deps := chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, filepath.Join(t.TempDir(), "spool"))
	// L2 来源解析依赖 G14 身份服务（组合根按 deps.Identity 装配
	// ClientStrategyDeps.Source；smoke deps 默认缺席 = 来源键 missing = L2
	// 不写不查）。本用例只消费 Identity（generic 请求不走官方会话档，
	// content_fingerprint / ip_api_key_fallback 档不触 Affinity）。
	identityService, identityErr := gatewaysession.NewIdentityService("chain-test-secret")
	if identityErr != nil {
		t.Fatalf("create identity service: %v", identityErr)
	}
	deps.Identity = &sessionIdentityServices{Identity: identityService, Secret: "chain-test-secret"}
	chain, shutdown, err := composeGatewayChain(deps)
	if err != nil {
		t.Fatalf("compose gateway chain: %v", err)
	}
	defer shutdown()
	server := httptest.NewServer(chain)
	defer server.Close()

	requestBody := `{"model":"gpt-test","messages":[{"role":"user","content":"你好"}]}`
	status, raw := chainV1ChatRequest(t, server.URL, fixture.apiKeySecret, requestBody)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("first call status=%d want 503: %s", status, raw)
	}
	firstHits := atomic.LoadInt32(&hits)
	if firstHits != 5 {
		t.Fatalf("first-call upstream hits=%d want 5 (L1 fast exhaustion)", firstHits)
	}

	status, raw = chainV1ChatRequest(t, server.URL, fixture.apiKeySecret, requestBody)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("retry status=%d want 503: %s", status, raw)
	}
	if !strings.Contains(raw, "上游暂时不可用，请重试") {
		t.Fatalf("fixed exhaustion copy missing on retry: %s", raw)
	}
	retryHits := atomic.LoadInt32(&hits) - firstHits
	if retryHits != 1 {
		t.Fatalf("retry upstream hits=%d want exactly 1 (L2 negative-cache hit after one upstream RTT)", retryHits)
	}
}

// TestGatewayChainRequestFastExhaustTwoAccountsNotTriggered：6 次失败但只有
// 2 只不同账户（各 3 Key 轮换）——合取条件不成立，不触发；候选池自然耗尽
// （两账户全部 Key 被扫描，6 次上游命中），既有耗尽契约不变。

func TestGatewayChainRequestFastExhaustTwoAccountsNotTriggered(t *testing.T) {
	fixture := newChainFixture(t)
	var hits int32
	upstream := httptestAlwaysRejectedUpstream(t, &hits)
	requestFastExhaustSeedAccount(t, fixture, "acc_2", upstream.URL, 3, 10)
	if _, err := fixture.db.Exec(fixtureBind(`UPDATE accounts SET credentials_encrypted = ? WHERE id = ?`),
		mustEncryptCredentials(t, map[string]any{"api_key": "sk-upstream-key-acc_1-a",
			"api_keys": []any{"sk-upstream-key-acc_1-a", "sk-upstream-key-acc_1-b", "sk-upstream-key-acc_1-c"},
			"base_url": upstream.URL, "supported_endpoint_modes": []string{"chat_json", "chat_sse", "responses_json", "responses_sse"}}), fixture.accountID); err != nil {
		t.Fatalf("update acc_1 credentials: %v", err)
	}
	chainPointAccountAtUpstream(t, fixture.db, fixture.accountID, upstream.URL, "chat_json", "chat_sse", "responses_json", "responses_sse")

	chain, shutdown, err := composeGatewayChain(chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, filepath.Join(t.TempDir(), "spool")))
	if err != nil {
		t.Fatalf("compose gateway chain: %v", err)
	}
	defer shutdown()
	server := httptest.NewServer(chain)
	defer server.Close()

	status, raw := chainV1ChatRequest(t, server.URL, fixture.apiKeySecret,
		`{"model":"gpt-test","messages":[{"role":"user","content":"你好"}]}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503: %s", status, raw)
	}
	if !strings.Contains(raw, "上游暂时不可用，请重试") {
		t.Fatalf("fixed exhaustion copy missing: %s", raw)
	}
	// 自然耗尽：3 Key + 3 Key = 6 次上游命中（若提前耗尽误触发会在第 5 次停）。
	if got := atomic.LoadInt32(&hits); got != 6 {
		t.Fatalf("upstream hits=%d want 6 (natural exhaustion of both 3-key accounts)", got)
	}
}
