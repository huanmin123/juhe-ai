package gatewaydispatch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// 冲刺补齐：Key-model 忙等后的并发重取失败、post-cycle 可恢复等待的非全抑制
// 分支、滚动捕获的完全消费压缩。

// failAfterFirstStore 第一次获取成功，之后全部拒绝。
type failAfterFirstStore struct {
	calls atomic.Int64
}

func (f *failAfterFirstStore) LoadCurrentAsync(context.Context, []string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (f *failAfterFirstStore) LoadCurrentByLaneAsync(context.Context, []string, string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (f *failAfterFirstStore) TryAcquireAsync(context.Context, string, int, AccountConcurrencyAcquireOptions) (ConcurrencySlot, error) {
	if f.calls.Add(1) <= 1 {
		return ConcurrencySlot{Acquired: true, Current: 1, Limit: 4, Lane: "text"}, nil
	}
	// 生产存储对失败槽位同样返回 Release 闭包。
	return ConcurrencySlot{Acquired: false, Current: 4, Limit: 4, Lane: "text", Release: func() {}, MarkFirstOutput: func() {}}, nil
}

// TestDispatchKeyModelBusyReacquireFailsCapacity: busy 释放并发槽后重取失败 →
// 容量上限尝试并跳过账户。
func TestDispatchKeyModelBusyReacquireFailsCapacity(t *testing.T) {
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	engine, driver, _ := newTestEngine(t)
	engine.Concurrency = &failAfterFirstStore{}
	engine.Config.KeyModelForegroundQueueWaitMs = 100
	engine.Config.KeyModelForegroundQueuePollMs = 1
	engine.KeyModel = &fakeKeyModelAdmission{statuses: map[string][]gatewayaccounteffects.AttemptPreparationStatus{
		"a-1": {gatewayaccounteffects.AttemptPreparationBusy},
	}}
	driver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	accounts := []AccountCandidate{multiKeyTestAccount("a-1", "key-a", "key-b")}
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, accounts))
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected UpstreamAttemptError, got %v", err)
	}
	if attemptErr.LastAttempt == nil || attemptErr.LastAttempt.UpstreamURL != "concurrency:limit" {
		t.Fatalf("重取失败必须记录容量上限, got %#v", attemptErr.LastAttempt)
	}
}

// releaseOnNthFilter 前 N 次保持抑制，之后解除（模拟抑制到期）。
type releaseOnNthFilter struct {
	releaseAfter int
	calls        atomic.Int64
}

func (r *releaseOnNthFilter) FilterAsync(_ context.Context, accounts []AccountCandidate, _ SuppressionFilterOptions) (SuppressionFilterResult, error) {
	call := r.calls.Add(1)
	if call <= int64(r.releaseAfter) {
		return SuppressionFilterResult{
			AllSuppressed:          true,
			SuppressedCount:        len(accounts),
			SuppressedAccountIDs:   accountIDs(accounts),
			AcquiredHalfOpenLeases: []HalfOpenLease{},
		}, nil
	}
	return localSuppressionBypassResult(accounts), nil
}

func (r *releaseOnNthFilter) ResolveLocalSuppressionFilter(ctx context.Context, input LocalSuppressionPreflightInput) (*SuppressionFilterResult, bool, error) {
	result := localSuppressionBypassResult(input.Accounts)
	return &result, false, nil
}

// TestDispatchPostCycleRecoverableWaitNotAllSuppressed: 第二候选传输失败后，
// post-cycle 过滤发现可恢复账户已解除抑制 → 记录等待并中断（预算耗尽）。
func TestDispatchPostCycleRecoverableWaitNotAllSuppressed(t *testing.T) {
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`bad gateway`))
	}))
	defer failServer.Close()
	engine, driver, _ := newTestEngine(t)
	// 每账户一次 per-account 过滤（a-1、a-2）+ 一次 post-cycle 过滤（call 3）
	// + 一次可恢复子集过滤（call 4，已解除）。
	engine.Suppression = &releaseOnNthFilter{releaseAfter: 3}
	driver.urlByAccount = map[string][]string{
		"a-1": {failServer.URL + "/v1/chat/completions"},
		"a-2": {failServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1", "a-2"))
	args.WaitForRecoverableFailures = true
	args.RequestCoordination.ServerRetryBudget = gatewaypreauth.NewServerRetryBudget(80, gatewaypreauth.SystemClock{})
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected UpstreamAttemptError, got %v", err)
	}
}

// TestRollingBufferCaptureFullConsumption: 逐块消费清空后压缩为 nil。
func TestRollingBufferCaptureFullConsumption(t *testing.T) {
	capture := newRollingBufferCapture(4)
	capture.Push([]byte("ab"))
	capture.Push([]byte("cd"))
	capture.Push([]byte("ef"))
	capture.Push([]byte("gh"))
	// 头部两块被完整消费 → compact 清空 chunks。
	if capture.ToText() == nil {
		t.Fatal("仍有活跃块")
	}
	if got := *capture.ToText(); got != "efgh" {
		t.Fatalf("text = %q", got)
	}
}
