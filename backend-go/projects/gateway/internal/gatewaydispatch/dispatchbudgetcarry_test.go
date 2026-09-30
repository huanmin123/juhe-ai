package gatewaydispatch

// BUG-0247 项 2 回归：请求级并发等待预算（concurrencyRetryWaitBudgetMs）必须
// 跨候选账户递减携带——首个账户的短等消耗预算后，同周期内后续账户按剩余
// 预算获取，预算耗尽时不再进入短等循环（Node upstream-dispatch.ts:383/:793/
// :866 的请求级 carry；外环容量等待后的三处重置 :1892/:1909/:1926 只发生在
// 候选周期之间）。修复前 remainingConcurrencyWaitBudget 恒等返回入参，同一
// 周期内每个候选账户都独立获得全额预算。
//
// 计数设计（恒忙存储）：并发预算 120ms（initial=50/max=100 → 等待 50+70，
// 3 次获取调用耗尽）；服务端重试预算 1500ms 限制总时长（外环重试延迟按余量
// 收窄，约 3-4 个候选周期后 handoff 收尾）。断言采用对周期边界漂移稳健的
// 比例不变量：携带生效时每个候选周期内 a-1 短等到预算耗尽（3 次获取调用，
// 至多一个收尾周期被 serverRetryBudget 钳制成 1 次），a-2 继承 0 预算恒为
// 1 次调用 → calls(a-1) ≥ 2 × calls(a-2) 恒成立；修复前恒等 carry 使 a-2
// 每个完整周期同样 3 次调用，比例退化为 ~1:1，不变量必然破坏。

import (
	"context"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// perAccountBusyStore 对所有账户恒拒绝并按账户统计 TryAcquire 调用次数。
type perAccountBusyStore struct {
	mu    sync.Mutex
	calls map[string]int
}

func (s *perAccountBusyStore) LoadCurrentAsync(context.Context, []string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (s *perAccountBusyStore) LoadCurrentByLaneAsync(context.Context, []string, string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (s *perAccountBusyStore) TryAcquireAsync(_ context.Context, accountID string, _ int, _ AccountConcurrencyAcquireOptions) (ConcurrencySlot, error) {
	s.mu.Lock()
	if s.calls == nil {
		s.calls = map[string]int{}
	}
	s.calls[accountID]++
	s.mu.Unlock()
	return ConcurrencySlot{Acquired: false, Current: 4, Limit: 4, Lane: "text"}, nil
}

func (s *perAccountBusyStore) callCount(accountID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[accountID]
}

func TestDispatchConcurrencyWaitBudgetCarriesAcrossAccounts(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	store := &perAccountBusyStore{}
	engine.Concurrency = store
	engine.Config.AccountConcurrencyRetryBudgetMs = 120
	engine.Config.AccountConcurrencyRetryInitialDelayMs = 50
	engine.Config.AccountConcurrencyRetryMaxDelayMs = 100

	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1", "a-2"))
	args.RequestCoordination.ServerRetryBudget = gatewaypreauth.NewServerRetryBudget(1_500, gatewaypreauth.SystemClock{})
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("容量耗尽应返回 UpstreamAttemptError, got %v", err)
	}
	if attemptErr.LastAttempt == nil || attemptErr.LastAttempt.UpstreamURL != "concurrency:limit" {
		t.Fatalf("lastAttempt = %#v（应以容量上限收尾）", attemptErr.LastAttempt)
	}
	callsA1 := store.callCount("a-1")
	callsA2 := store.callCount("a-2")
	if callsA1 < 6 {
		t.Fatalf("a-1 获取调用次数=%d，至少应有 2 个完整候选周期（每周期 3 次调用）", callsA1)
	}
	// 核心 carry 断言：每个周期内 a-2 继承 a-1 耗尽后的 0 预算，仅 1 次获取
	// 调用即容量失败；修复前恒等 carry 使 a-2 每周期同样 3 次调用（比例 ~1:1）。
	if 2*callsA2 > callsA1 {
		t.Fatalf("预算 carry 不变量破坏: calls(a-1)=%d calls(a-2)=%d（携带生效时每周期 a-1 短等 3 次调用、a-2 继承 0 预算仅 1 次，比例应 ≥ 2:1；修复前退化为 ~1:1）", callsA1, callsA2)
	}
}
