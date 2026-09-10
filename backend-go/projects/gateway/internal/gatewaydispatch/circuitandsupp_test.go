package gatewaydispatch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// 冲刺收尾：分阶段抑制过滤驱动 post-cycle 可恢复等待的继续分支，以及
// 短电路装配下的传输失败上报。

// phasedSuppression 按调用序切换 a-1 的抑制状态：
// 调用 1（a-1 的账户级过滤）抑制；调用 3（post-cycle 全量过滤）仅抑制 a-1；
// 调用 4 起（可恢复子集过滤）全部放行。
type phasedSuppression struct {
	calls atomic.Int64
}

func (p *phasedSuppression) result(accounts []AccountCandidate, suppressA1 bool) SuppressionFilterResult {
	result := SuppressionFilterResult{
		Accounts:               []AccountCandidate{},
		SuppressedAccountIDs:   []string{},
		AcquiredHalfOpenLeases: []HalfOpenLease{},
	}
	for _, account := range accounts {
		if account.ID == "a-1" && suppressA1 {
			result.SuppressedAccountIDs = append(result.SuppressedAccountIDs, account.ID)
			result.SuppressedCount++
		} else {
			result.Accounts = append(result.Accounts, account)
		}
	}
	result.AllSuppressed = len(result.Accounts) == 0 && len(accounts) > 0
	return result
}

func (p *phasedSuppression) FilterAsync(_ context.Context, accounts []AccountCandidate, _ SuppressionFilterOptions) (SuppressionFilterResult, error) {
	call := p.calls.Add(1)
	suppressA1 := call == 1 || call == 3
	return p.result(accounts, suppressA1), nil
}

func (p *phasedSuppression) ResolveLocalSuppressionFilter(ctx context.Context, input LocalSuppressionPreflightInput) (*SuppressionFilterResult, bool, error) {
	result := localSuppressionBypassResult(input.Accounts)
	return &result, false, nil
}

// TestDispatchPostCycleRecoverableWaitContinuesOnce: post-cycle 发现 a-1 仍可
// 恢复且子集过滤放行 → 记录等待并重排继续一个周期，直至预算耗尽。
func TestDispatchPostCycleRecoverableWaitContinuesOnce(t *testing.T) {
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`bad gateway`))
	}))
	defer failServer.Close()
	engine, driver, _ := newTestEngine(t)
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
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected UpstreamAttemptError, got %v", err)
	}
	if suppression.calls.Load() < 4 {
		t.Fatalf("过滤调用不足, calls = %d", suppression.calls.Load())
	}
}

// TestDispatchCircuitWiredTransportFailureReports: 短电路装配下已开始的传输
// 失败经 ReportTransportFailure 上报并跳过账户。
func TestDispatchCircuitWiredTransportFailureReports(t *testing.T) {
	memoryStore, err := gatewaycircuit.NewMemoryStore(gatewaycircuit.MemoryStoreOptions{Capacity: 64})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	circuits, err := gatewaycircuit.NewCircuitService(memoryStore, gatewaycircuit.ServiceOptions{})
	if err != nil {
		t.Fatalf("circuits: %v", err)
	}
	engine, driver, _ := newTestEngine(t)
	engine.Circuits = circuits
	dead := "https://127.0.0.1:9/v1/chat/completions"
	driver.urlByAccount = map[string][]string{"a-1": {dead}}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1"))
	// 无同账户重试窗口（次数为 0）→ 传输失败直接跳过并上报电路。
	args.Settings.TemporaryUnschedulableRetryAttempts = 0
	_, err = engine.FetchFirstAvailableUpstream(context.Background(), args)
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected UpstreamAttemptError, got %v", err)
	}
	if attemptErr.LastAttempt == nil || attemptErr.LastAttempt.TransportFailureKind != TransportFailureKindConnection {
		t.Fatalf("lastAttempt = %#v", attemptErr.LastAttempt)
	}
}
