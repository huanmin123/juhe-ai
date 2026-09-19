package gatewaydispatch

// R4：上游尝试循环退避上限注入（EngineConfig.UpstreamRetryBackoffDelaysMs）
// 的注入路径测试。生产契约由 nil 回落保证（upstreamRetryBackoffCap 的
// fallback 即原硬编码 1000/500/3000）；本文件证明：
//  1. helper 的选择逻辑（注入生效 / nil 回落 / 缺位索引回落）；
//  2. FetchFirstAvailableUpstream 的 post-cycle recoverable 等待分支
//     （原 997 行 waitForDelayMs 上限）真实消费注入值——
//     recoverable_upstream_failure_dispatch_wait 审计 metadata 里的
//     retryDelayMs 等于注入值而非生产 3000。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

func TestUpstreamRetryBackoffCapSelection(t *testing.T) {
	injected := []int64{5, 6, 7}
	cases := []struct {
		name     string
		delays   []int64
		index    int
		fallback int64
		want     int64
	}{
		{"nil 回落 capacity queued", nil, 0, 1000, 1000},
		{"nil 回落 capacity plain", nil, 1, 500, 500},
		{"nil 回落 recoverable wait", nil, 2, 3000, 3000},
		{"注入生效", injected, 2, 3000, 7},
		{"缺位索引回落", []int64{5}, 1, 500, 500},
	}
	for _, testCase := range cases {
		if got := upstreamRetryBackoffCap(testCase.delays, testCase.index, testCase.fallback); got != testCase.want {
			t.Fatalf("%s: cap = %d want %d", testCase.name, got, testCase.want)
		}
	}
}

// retryBackoffMetadataAudit 捕获 gateway metadata 值（frozenAudit 只记 label）。
type retryBackoffMetadataAudit struct {
	entries map[string][]map[string]any
}

func newRetryBackoffMetadataAudit() *retryBackoffMetadataAudit {
	return &retryBackoffMetadataAudit{entries: map[string][]map[string]any{}}
}

func (a *retryBackoffMetadataAudit) BindContext(gatewaypreauth.AuditGatewayContext) {}

func (a *retryBackoffMetadataAudit) AddGatewayMetadata(label string, metadata map[string]any) {
	a.entries[label] = append(a.entries[label], metadata)
}

func (a *retryBackoffMetadataAudit) Finalize(gatewaypreauth.AuditFinalizeInput) {}

// TestDispatchPostCycleRecoverableWaitConsumesInjectedBackoff: 与
// TestDispatchPostCycleRecoverableWaitContinuesOnce 同一 post-cycle 场景，
// 注入短退避后审计里的 retryDelayMs 必须是注入值 5 而非生产 3000——证明
// 注入点接进了真实等待上限，且重试轮次契约（过滤调用 ≥ 4）不变。
func TestDispatchPostCycleRecoverableWaitConsumesInjectedBackoff(t *testing.T) {
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`bad gateway`))
	}))
	defer failServer.Close()
	engine, driver, _ := newTestEngine(t)
	engine.Config.UpstreamRetryBackoffDelaysMs = []int64{5, 5, 5}
	suppression := &phasedSuppression{}
	engine.Suppression = suppression
	driver.urlByAccount = map[string][]string{
		"a-1": {failServer.URL + "/v1/chat/completions"},
		"a-2": {failServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1", "a-2"))
	args.WaitForRecoverableFailures = true
	args.RequestCoordination.ServerRetryBudget = gatewaypreauth.NewServerRetryBudget(4_000, gatewaypreauth.SystemClock{})
	audit := newRetryBackoffMetadataAudit()
	args.AuditCapture = AuditCapture{Context: audit, Sink: &fakeAuditSink{}}
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected UpstreamAttemptError, got %v", err)
	}
	if suppression.calls.Load() < 4 {
		t.Fatalf("过滤调用不足, calls = %d", suppression.calls.Load())
	}
	waits := audit.entries["recoverable_upstream_failure_dispatch_wait"]
	if len(waits) == 0 {
		t.Fatalf("缺少 recoverable 等待审计, entries = %v", audit.entries)
	}
	for _, wait := range waits {
		retryDelayMs, ok := wait["retryDelayMs"].(int64)
		if !ok {
			t.Fatalf("retryDelayMs 缺失或类型不符: %v", wait)
		}
		if retryDelayMs != 5 {
			t.Fatalf("注入未生效: retryDelayMs = %d want 5", retryDelayMs)
		}
	}
}

// TestDispatchSameAccountRetryConsumesInjectedBackoff: 传输失败 → 同账户重试
// 预约（attemptoutcomes.go upstream_transport_failure）→ reserveSameAccountRetry
// 的 waitForDelayMs（原 752 行，生产 = settings.
// TemporaryUnschedulableRetryIntervalSeconds * 1000）消费语义位 3。settings
// 间隔 5s 而注入 5ms：same_account_retry_dispatch 审计照常出现（delayMs 字段
// 仍记录配置值），但实际墙钟必须远低于 5s——注入失效时该断言必然超时失败。
func TestDispatchSameAccountRetryConsumesInjectedBackoff(t *testing.T) {
	engine, driver, _ := newTestEngine(t)
	// 连接拒绝端口：每次尝试都是 transport failure，驱动同账户重试链。
	driver.urlByAccount = map[string][]string{"a-1": {"https://127.0.0.1:9/v1/chat/completions"}}
	engine.Config.UpstreamRetryBackoffDelaysMs = []int64{5, 5, 5, 5}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := dispatchArgs(t, req, testAccounts("a-1"))
	// settings 同账户重试间隔 5s（未注入时的真实睡眠时长）。
	args.Settings.TemporaryUnschedulableRetryIntervalSeconds = 5
	audit := newRetryBackoffMetadataAudit()
	args.AuditCapture = AuditCapture{Context: audit, Sink: &fakeAuditSink{}}
	startedMs := gatewayupstream.NowMs()
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	elapsedMs := gatewayupstream.NowMs() - startedMs
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected UpstreamAttemptError, got %v", err)
	}
	if len(audit.entries["same_account_retry_dispatch"]) == 0 {
		t.Fatalf("缺少同账户重试审计, entries = %v", audit.entries)
	}
	// 未注入时仅一次同账户等待就是 5000ms；注入后是 5ms，留出慢机余量。
	if elapsedMs >= 2_000 {
		t.Fatalf("注入未生效: elapsed = %dms (want < 2000ms)", elapsedMs)
	}
}
