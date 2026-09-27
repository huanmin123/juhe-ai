package main

// anthropic 用量响应头派发装配回归（AI账户Grok用量快照设计 §8.2）：
//   - codexUsageHeadersChannelDispatcher.PersistAnthropicUsageHeaders 的 job
//     形状（kind anthropic_claude + claude_* payload）与无头静默契约；
//   - 失败面挂载（chain_ports.go，镜像 TestChainFailureDispatcherCodexUsageHeaders
//     FailureFace）：mutationEnabled 时对 anthropic OAuth 账户派发、source
//     重写 gateway_error；状态变更关闭与非 anthropic 账户静默。

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/tablemonitor"
)

// TestAnthropicUsageHeadersChannelDispatcherJobShape：合格 anthropic OAuth 头
// → account_usage_snapshot_upsert 行（kind/source/snapshot payload 键）；
// 无 unified rate limit 头不落行。
func TestAnthropicUsageHeadersChannelDispatcherJobShape(t *testing.T) {
	db, dispatch := newCodexUsageHeadersTestChannel(t)
	adapter := newAnthropicUsageHeadersChannelDispatcher(dispatch)
	if adapter == nil {
		t.Fatal("non-nil channel must build the dispatcher")
	}
	if newAnthropicUsageHeadersChannelDispatcher(nil) != nil {
		t.Fatal("nil channel must keep the nil dispatcher silent contract")
	}

	headers := http.Header{
		"Anthropic-Ratelimit-Unified-Status":         []string{"allowed"},
		"Anthropic-Ratelimit-Unified-5h-Utilization": []string{"0.14"},
		"Anthropic-Ratelimit-Unified-5h-Reset":       []string{"1790496000"},
	}
	adapter.PersistAnthropicUsageHeaders(context.Background(), "acc_claude", headers, gatewaycodex.AnthropicUsageSnapshotSource)

	var (
		rowType, accountID, kind, source, snapshotJSON, rowUpdatedAt string
		readErr                                                      error
	)
	// -race 全包运行下调度显著慢化：轮询窗放宽到 15s（命中时首轮即返回）。
	deadline := time.Now().Add(15 * time.Second)
	for {
		readErr = db.QueryRow(`SELECT type, account_id, kind, source, snapshot_json, updated_at
			FROM record_maintenance_jobs LIMIT 1`).
			Scan(&rowType, &accountID, &kind, &source, &snapshotJSON, &rowUpdatedAt)
		if readErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("snapshot job row never landed (fire-and-forget dispatch): %v", readErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if rowType != tablemonitor.RecordMaintenanceJobTypeAccountUsageSnapshotUpsert {
		t.Fatalf("job type = %q", rowType)
	}
	if accountID != "acc_claude" || kind != "anthropic_claude" || source != gatewaycodex.AnthropicUsageSnapshotSource {
		t.Fatalf("job envelope = %s/%s/%s", accountID, kind, source)
	}
	if rowUpdatedAt == "" {
		t.Fatal("updated_at must be persisted (executor validates it)")
	}
	snapshotPayload := map[string]any{}
	if err := json.Unmarshal([]byte(snapshotJSON), &snapshotPayload); err != nil {
		t.Fatalf("snapshot_json invalid: %v", err)
	}
	if snapshotPayload["claude_usage_updated_at"] == nil {
		t.Fatalf("payload must carry claude_usage_updated_at: %v", snapshotPayload)
	}
	if snapshotPayload["source"] != gatewaycodex.AnthropicUsageSnapshotSource {
		t.Fatalf("payload source = %v", snapshotPayload["source"])
	}
	if snapshotPayload["claude_5h_used_percent"] != float64(14) {
		t.Fatalf("claude_5h_used_percent = %v", snapshotPayload["claude_5h_used_percent"])
	}
	if snapshotPayload["claude_5h_reset_at"] != "2026-09-27T08:00:00.000Z" {
		t.Fatalf("claude_5h_reset_at = %v", snapshotPayload["claude_5h_reset_at"])
	}
	if snapshotPayload["claude_unified_status"] != "allowed" {
		t.Fatalf("claude_unified_status = %v", snapshotPayload["claude_unified_status"])
	}

	// 无 unified rate limit 头：派发口静默返回，不追加行。
	adapter.PersistAnthropicUsageHeaders(context.Background(), "acc_claude",
		http.Header{"X-Request-Id": []string{"plain"}}, gatewaycodex.AnthropicUsageSnapshotSource)
	time.Sleep(50 * time.Millisecond)
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM record_maintenance_jobs`).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("rows = %d want 1 (headers without anthropic data must not enqueue)", count)
	}
}

// TestChainFailureDispatcherAnthropicUsageHeadersFailureFace：失败面在
// mutationEnabled ≠ false 时对 anthropic OAuth 账户派发 unified rate limit
// 头持久化；非 anthropic 账户与状态变更关闭的请求保持静默。
func TestChainFailureDispatcherAnthropicUsageHeadersFailureFace(t *testing.T) {
	anthropicHeaders := http.Header{
		"Anthropic-Ratelimit-Unified-5h-Utilization": []string{"0.14"},
	}
	response := failureDispatchUpstreamResponseWithHeaders(t, http.StatusTooManyRequests, "application/json",
		`{"error":{"message":"rate limited"}}`, anthropicHeaders)
	sink := &failureDispatchAuditSink{}
	anthropicDispatcher := &fakeChainAnthropicHeadersDispatcher{}
	dispatcher := newFailureDispatcherForTest(nil)
	dispatcher.anthropicUsageHeaders = anthropicDispatcher

	input := gatewayFailedResponseInput(response, sink, "gateway")
	input.Account = gatewaydispatch.AccountCandidate{
		ID: "acc_claude", Name: "claude 账户",
		Type: "oauth", ProviderCode: "anthropic",
	}
	input.AccountStateMutationEnabled = true
	if _, err := dispatcher.HandleFailedUpstreamResponse(context.Background(), input); err != nil {
		t.Fatalf("handle failed upstream response: %v", err)
	}
	anthropicDispatcher.mu.Lock()
	calls := len(anthropicDispatcher.accountIDs)
	sources := append([]string{}, anthropicDispatcher.sources...)
	anthropicDispatcher.mu.Unlock()
	if calls != 1 {
		t.Fatalf("anthropic header dispatch calls = %d want 1", calls)
	}
	if sources[0] != gatewaycodex.AnthropicUsageSnapshotSource {
		t.Fatalf("anthropic header source = %q want %q", sources[0], gatewaycodex.AnthropicUsageSnapshotSource)
	}

	// 状态变更关闭：失败面不派发。
	anthropicDispatcher2 := &fakeChainAnthropicHeadersDispatcher{}
	dispatcher2 := newFailureDispatcherForTest(nil)
	dispatcher2.anthropicUsageHeaders = anthropicDispatcher2
	input2 := gatewayFailedResponseInput(response, sink, "gateway")
	input2.Account = input.Account
	input2.AccountStateMutationEnabled = false
	if _, err := dispatcher2.HandleFailedUpstreamResponse(context.Background(), input2); err != nil {
		t.Fatalf("handle mutation-disabled failure: %v", err)
	}
	anthropicDispatcher2.mu.Lock()
	disabledCalls := len(anthropicDispatcher2.accountIDs)
	anthropicDispatcher2.mu.Unlock()
	if disabledCalls != 0 {
		t.Fatalf("mutation-disabled failure must not dispatch anthropic headers, got %d", disabledCalls)
	}

	// 非 anthropic 账户（codex OAuth）：不派发（gatewaycodex 资格门）。
	anthropicDispatcher3 := &fakeChainAnthropicHeadersDispatcher{}
	dispatcher3 := newFailureDispatcherForTest(nil)
	dispatcher3.anthropicUsageHeaders = anthropicDispatcher3
	input3 := gatewayFailedResponseInput(response, sink, "gateway")
	input3.Account = gatewaydispatch.AccountCandidate{
		ID: "acc_codex", Name: "codex 账户",
		Type: "oauth", ProtocolCode: "openai", ProtocolVersion: "v1",
	}
	input3.AccountStateMutationEnabled = true
	if _, err := dispatcher3.HandleFailedUpstreamResponse(context.Background(), input3); err != nil {
		t.Fatalf("handle codex-account failure: %v", err)
	}
	anthropicDispatcher3.mu.Lock()
	codexCalls := len(anthropicDispatcher3.accountIDs)
	anthropicDispatcher3.mu.Unlock()
	if codexCalls != 0 {
		t.Fatalf("codex account must not dispatch anthropic headers, got %d", codexCalls)
	}
}

// fakeChainAnthropicHeadersDispatcher captures the anthropic usage-header
// dispatch on the failure face.
type fakeChainAnthropicHeadersDispatcher struct {
	mu         sync.Mutex
	accountIDs []string
	sources    []string
}

func (d *fakeChainAnthropicHeadersDispatcher) PersistAnthropicUsageHeaders(_ context.Context, accountID string, _ http.Header, source string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.accountIDs = append(d.accountIDs, accountID)
	d.sources = append(d.sources, source)
}

// TestPersistAnthropicUsageHeadersOnGatewayTraffic：成功面挂载门（chain_v1
// handleUpstreamResponse 的提取）——仅网关流量派发，其余流量、nil 派发器与
// 不合格账户静默（资格门在 gatewaycodex helper 内，此处验证流量门与透传）。
func TestPersistAnthropicUsageHeadersOnGatewayTraffic(t *testing.T) {
	anthropicHeaders := http.Header{
		"Anthropic-Ratelimit-Unified-5h-Utilization": []string{"0.14"},
	}
	account := gatewaydispatch.AccountCandidate{
		ID: "acc_claude", Name: "claude 账户",
		Type: "oauth", ProviderCode: "anthropic",
	}

	dispatcher := &fakeChainAnthropicHeadersDispatcher{}
	persistAnthropicUsageHeadersOnGatewayTraffic(context.Background(), gatewayTrafficSource,
		account, anthropicHeaders, dispatcher)
	dispatcher.mu.Lock()
	gatewayCalls := len(dispatcher.accountIDs)
	sources := append([]string{}, dispatcher.sources...)
	dispatcher.mu.Unlock()
	if gatewayCalls != 1 {
		t.Fatalf("gateway traffic must dispatch anthropic headers, got %d", gatewayCalls)
	}
	if sources[0] != gatewaycodex.AnthropicUsageSnapshotSource {
		t.Fatalf("source = %q want %q", sources[0], gatewaycodex.AnthropicUsageSnapshotSource)
	}

	nonGatewayDispatcher := &fakeChainAnthropicHeadersDispatcher{}
	persistAnthropicUsageHeadersOnGatewayTraffic(context.Background(), "diagnostic",
		account, anthropicHeaders, nonGatewayDispatcher)
	nonGatewayDispatcher.mu.Lock()
	nonGatewayCalls := len(nonGatewayDispatcher.accountIDs)
	nonGatewayDispatcher.mu.Unlock()
	if nonGatewayCalls != 0 {
		t.Fatalf("non-gateway traffic must not dispatch, got %d", nonGatewayCalls)
	}

	// nil 派发器不 panic（gatewaycodex helper 内静默）。
	persistAnthropicUsageHeadersOnGatewayTraffic(context.Background(), gatewayTrafficSource,
		account, anthropicHeaders, nil)

	// 非 anthropic 账户不派发（gatewaycodex 资格门）。
	codexDispatcher := &fakeChainAnthropicHeadersDispatcher{}
	persistAnthropicUsageHeadersOnGatewayTraffic(context.Background(), gatewayTrafficSource,
		gatewaydispatch.AccountCandidate{ID: "acc_codex", Type: "oauth", ProviderCode: "openai"},
		anthropicHeaders, codexDispatcher)
	codexDispatcher.mu.Lock()
	codexCalls := len(codexDispatcher.accountIDs)
	codexDispatcher.mu.Unlock()
	if codexCalls != 0 {
		t.Fatalf("codex account must not dispatch anthropic headers, got %d", codexCalls)
	}
}
