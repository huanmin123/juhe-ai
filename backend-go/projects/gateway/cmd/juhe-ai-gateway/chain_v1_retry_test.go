package main

// D1 修复（常驻审查第五轮）的回归测试：chain_v1 响应处理必须消费
// gatewayresponse 的 RetryUpstream 决定（Node routes.ts:1899
// `if (handledResponse.retryUpstream)`）：
//
//   - inspection server-retry 触发 → 排除当前账户续 dispatch 换号循环，
//     换号成功返回下一账户内容（不再把空 200 交给客户端）；
//   - 换号不可行（无剩余候选且无 fallback 分组）→ 渲染 503 流式服务端
//     重试耗尽出口（Node sendStreamServerRetryExhaustedResponse 的
//     pre_commit_http_error 分支），而不是空 200；
//   - response_inspection 决策未要求换号 → no_dispatch_change 终态。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
)

// inspectionRetryHandling 构造一个 response_inspection 换号决定
// （gatewayresponse.finalizeStreamFailure 的 RetryUpstream 分支产物）。
func inspectionRetryHandling(excludeCurrentAccount bool) gatewayresponse.UpstreamResponseHandlingResult {
	return gatewayresponse.UpstreamResponseHandlingResult{
		RetryUpstream:         true,
		RetryReason:           gatewayresponse.StreamServerRetryResponseInspection,
		ExcludeCurrentAccount: excludeCurrentAccount,
		Message:               "响应命中检查策略：过载重试",
		ErrorCode:             gatewaypreauth.GatewayStreamClientRetryErrorCode,
		ResponseInspection: &gatewayresponse.ResponseInspectionDecision{
			PolicyID:      "pol_retry",
			PolicyName:    "过载重试策略",
			PolicySource:  gatewayresponse.PolicySourceManagement,
			AccountSwitch: "request_next_account",
			RetryEnabled:  true,
		},
	}
}

// TestV1ResponseRetryRotatesAccounts：策略要求换号且组内仍有候选 → 循环继续，
// 当前账户进入 per-group 排除集，请求不在此刻收尾。
func TestV1ResponseRetryRotatesAccounts(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	loop.current.Accounts = []gatewaydispatch.AccountCandidate{
		{ID: "acc_1"}, {ID: "acc_2"},
	}

	settled := loop.settleResponseStreamServerRetry(context.Background(),
		gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: "acc_1"}},
		inspectionRetryHandling(true))
	if settled {
		t.Fatal("retry with remaining candidates must continue the loop")
	}
	if len(sink.inputs) != 0 {
		t.Fatalf("retry must not render a terminal response: %+v", sink.inputs)
	}
	if _, excluded := loop.streamRetryExcludedAccounts["acc_1"]; !excluded {
		t.Fatalf("acc_1 must be stream-retry excluded: %v", loop.streamRetryExcludedAccounts)
	}
	if _, exhausted := loop.exhaustedAccounts["acc_1"]; exhausted {
		t.Fatalf("stream-retry exclusion must not touch the exhausted set: %v", loop.exhaustedAccounts)
	}
	if loop.streamServerRetryCount != 1 {
		t.Fatalf("retry count = %d", loop.streamServerRetryCount)
	}
}

// TestV1ResponseRetryNoDispatchChangeStops：response_inspection 决定未要求
// 换号 → no_dispatch_change 终态（Node routes.ts:2326-2356）。
func TestV1ResponseRetryNoDispatchChangeStops(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	loop.current.Accounts = []gatewaydispatch.AccountCandidate{{ID: "acc_1"}, {ID: "acc_2"}}

	settled := loop.settleResponseStreamServerRetry(context.Background(),
		gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: "acc_1"}},
		inspectionRetryHandling(false))
	if !settled {
		t.Fatal("no-dispatch-change retry must settle the request")
	}
	if len(sink.inputs) != 1 {
		t.Fatalf("inputs=%d", len(sink.inputs))
	}
	exit := sink.inputs[0]
	if exit.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", exit.StatusCode)
	}
	if exit.Audit.Outcome != gatewaypreauth.AuditOutcomeStreamFailed {
		t.Fatalf("audit outcome=%s", exit.Audit.Outcome)
	}
	if exit.RecordUsage == nil || *exit.RecordUsage {
		t.Fatal("exhausted exit must skip usage recording")
	}
	if len(loop.streamRetryExcludedAccounts) != 0 {
		t.Fatalf("no exclusion requested: %v", loop.streamRetryExcludedAccounts)
	}
}

// TestV1ResponseRetryExhaustedRenders503：换号后组内无剩余候选且无 fallback
// 分组（单分组 key、无绑定记录）→ 排除集并入请求级 exhausted 集合，渲染 503
// 耗尽出口——不是空 200。
func TestV1ResponseRetryExhaustedRenders503(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	loop.current.Accounts = []gatewaydispatch.AccountCandidate{{ID: "acc_1"}}

	settled := loop.settleResponseStreamServerRetry(context.Background(),
		gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: "acc_1"}},
		inspectionRetryHandling(true))
	if !settled {
		t.Fatal("exhausted retry must settle the request")
	}
	if len(sink.inputs) != 1 {
		t.Fatalf("inputs=%d", len(sink.inputs))
	}
	exit := sink.inputs[0]
	if exit.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", exit.StatusCode)
	}
	if _, exhausted := loop.exhaustedAccounts["acc_1"]; !exhausted {
		t.Fatalf("excluded account must merge into the exhausted set: %v", loop.exhaustedAccounts)
	}
}

// seedInspectionRetryPolicy 写入一条 openai 协议级 response inspection 策略：
// generic_openai 画像 + 指定错误码 → retry_next_account（Node
// response_inspection_policies 行形状，scope_type='protocol'）。
func seedInspectionRetryPolicy(t *testing.T, fixture *chainFixture, id, errorCode string) {
	t.Helper()
	if _, err := fixture.db.Exec(`INSERT INTO response_inspection_policies (
			id, name, enabled, priority, scope_type, protocol_code, provider_code, match_json, action, notes, created_at, updated_at
		) VALUES (?, '过载重试策略', 1, 10, 'protocol', 'openai', NULL, ?, 'retry_next_account', NULL,
			'2026-09-04T00:00:00.000Z', '2026-09-04T00:00:00.000Z')`,
		id, `{"clientProfiles":["generic_openai"],"errorCodes":["`+errorCode+`"]}`); err != nil {
		t.Fatalf("seed inspection policy: %v", err)
	}
}

// newInspectionRetryChain 把 acc_1 指向返回 badSSE 错误帧的上游，并可选注册
// acc_good 指向 goodUpstream（nil 表示单账户耗尽场景）。
func newInspectionRetryChain(t *testing.T, fixture *chainFixture, badSSE string, goodUpstream *httptest.Server) {
	t.Helper()
	badUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(badSSE))
	}))
	t.Cleanup(badUpstream.Close)
	if _, err := fixture.db.Exec(`UPDATE accounts SET credentials_encrypted = ? WHERE id = ?`,
		mustEncryptCredentials(t, map[string]any{"api_key": "sk-upstream-account-key", "base_url": badUpstream.URL}), fixture.accountID); err != nil {
		t.Fatalf("update bad account: %v", err)
	}
	if goodUpstream == nil {
		return
	}
	now := "2026-09-04T00:00:00.000Z"
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(query, args...); err != nil {
			t.Fatalf("seed: %v: %v", query, err)
		}
	}
	credentials := mustEncryptCredentials(t, map[string]any{"api_key": "sk-upstream-account-key", "base_url": goodUpstream.URL})
	seed(`INSERT INTO accounts (
			id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
			name, type, status, schedulable, credentials_encrypted, deleted_at, health_check_model
		) VALUES ('acc_good', ?, 'openai', 'prof_1', 'openai', 'v1', '备用账户', 'api_key', 'active', 1, ?, NULL, 'gpt-test')`,
		fixture.systemAccount, credentials)
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at) VALUES (?, ?, 'acc_good', 1, ?)`,
		fixture.groupID, fixture.systemAccount, now)
	seed(`INSERT INTO account_supported_models (account_id, provider_code, model, created_at) VALUES ('acc_good', 'openai', 'gpt-test', ?)`, now)
}

// TestGatewayChainResponseInspectionRetrySwitchesAccount：端到端——acc_1 的
// 200 SSE 流命中 inspection 策略（pre-commit，客户端未收到语义字节）→
// RetryUpstream 换号重试 → acc_good 成功返回内容。
func TestGatewayChainResponseInspectionRetrySwitchesAccount(t *testing.T) {
	fixture := newChainFixture(t)
	goodUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-good\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"换号成功内容\"},\"finish_reason\":null}]}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer goodUpstream.Close()
	newInspectionRetryChain(t, fixture,
		"data: {\"error\":{\"code\":\"inspected_overloaded\",\"message\":\"上游过载\"}}\n\n",
		goodUpstream)
	seedInspectionRetryPolicy(t, fixture, "pol_retry", "inspected_overloaded")

	chain, shutdown, err := composeGatewayChain(chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, filepath.Join(t.TempDir(), "spool")))
	if err != nil {
		t.Fatalf("compose gateway chain: %v", err)
	}
	defer shutdown()
	server := httptest.NewServer(chain)
	defer server.Close()

	status, raw := chainV1ChatRequest(t, server.URL, fixture.apiKeySecret,
		`{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"你好"}]}`)
	if status != http.StatusOK {
		t.Fatalf("status=%d want 200 after account switch: %s", status, raw)
	}
	if !strings.Contains(raw, "换号成功内容") {
		t.Fatalf("switched-account content missing: %s", raw)
	}
	if strings.Contains(raw, "inspected_overloaded") {
		t.Fatalf("first-account failure leaked to client: %s", raw)
	}
}

// TestGatewayChainResponseInspectionRetryExhaustedRenders503：端到端——单账户
// 命中 inspection 策略后无候选可换、无 fallback 分组 → 503 耗尽出口
// （Node sendStreamServerRetryExhaustedResponse 的 pre_commit_http_error 分支）。
func TestGatewayChainResponseInspectionRetryExhaustedRenders503(t *testing.T) {
	fixture := newChainFixture(t)
	newInspectionRetryChain(t, fixture,
		"data: {\"error\":{\"code\":\"inspected_overloaded\",\"message\":\"上游过载\"}}\n\n",
		nil)
	seedInspectionRetryPolicy(t, fixture, "pol_retry", "inspected_overloaded")

	chain, shutdown, err := composeGatewayChain(chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, filepath.Join(t.TempDir(), "spool")))
	if err != nil {
		t.Fatalf("compose gateway chain: %v", err)
	}
	defer shutdown()
	server := httptest.NewServer(chain)
	defer server.Close()

	status, raw := chainV1ChatRequest(t, server.URL, fixture.apiKeySecret,
		`{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"你好"}]}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 after retry exhaustion: %s", status, raw)
	}
	if !strings.Contains(raw, "service_unavailable") {
		t.Fatalf("exhausted contract missing: %s", raw)
	}
}
