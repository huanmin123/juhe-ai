package gatewaydispatch

// AI 账户每分钟请求上限设计 §5 的调度语义行为测试：Kind 归属与文案、RPM 失败
// 跳过下一候选、全池 RPM 耗尽携带 RetryAfterMs、RPM 上限聚合与最早重置计算。

import (
	"context"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

func TestAccountCapacityFailureKindAndLimitMessage(t *testing.T) {
	// 契约：并发仍有余量而 RPM 窗口满 → rpm；否则归并发。
	rpmSlot := ConcurrencySlot{Acquired: false, Current: 1, Limit: 5, Lane: "text", LaneCurrent: 1, LaneLimit: 5, RPMCurrent: 60, RPMLimit: 60, RPMResetAtMs: gatewayupstream.NowMs() + 5_000}
	if kind := accountCapacityFailureKind(rpmSlot); kind != AccountCapacityFailureKindRPM {
		t.Fatalf("kind = %s, want rpm", kind)
	}
	if got := accountConcurrencyLimitMessage(rpmSlot, 0); !strings.Contains(got, "每分钟请求数已达到上限 60/60") || !strings.Contains(got, "秒后重置") {
		t.Fatalf("RPM 文案 = %q", got)
	}
	// 契约：并发满（两约束同时满）→ 并发优先。
	concurrencySlot := ConcurrencySlot{Acquired: false, Current: 5, Limit: 5, Lane: "text", LaneCurrent: 5, LaneLimit: 5, RPMCurrent: 60, RPMLimit: 60, RPMResetAtMs: rpmSlot.RPMResetAtMs}
	if kind := accountCapacityFailureKind(concurrencySlot); kind != AccountCapacityFailureKindConcurrency {
		t.Fatalf("kind = %s, want concurrency", kind)
	}
	if got := accountConcurrencyLimitMessage(concurrencySlot, 0); !strings.Contains(got, "账户并发已达到上限 5/5") {
		t.Fatalf("并发文案 = %q", got)
	}
	// 契约：lane 满 → image lane 文案（既有行为保持）。
	laneSlot := ConcurrencySlot{Acquired: false, Current: 2, Limit: 5, Lane: "image", LaneCurrent: 3, LaneLimit: 3, RPMCurrent: 0, RPMLimit: 0}
	if got := accountConcurrencyLimitMessage(laneSlot, 0); !strings.Contains(got, "账户图像通道并发已达到上限") {
		t.Fatalf("image 文案 = %q", got)
	}
	// 契约：RPM 未配置（0）不误判 rpm。
	unlimited := ConcurrencySlot{Acquired: false, Current: 4, Limit: 4, Lane: "text", LaneCurrent: 4, LaneLimit: 4, RPMCurrent: 99, RPMLimit: 0}
	if kind := accountCapacityFailureKind(unlimited); kind != AccountCapacityFailureKindConcurrency {
		t.Fatalf("kind = %s, want concurrency（RPM 未配置）", kind)
	}
}

func TestAccountCapacityLimitRejectionCarriesKindAndRetryAfter(t *testing.T) {
	slot := ConcurrencySlot{Acquired: false, Current: 1, Limit: 5, Lane: "text", LaneCurrent: 1, LaneLimit: 5, RPMCurrent: 2, RPMLimit: 2, RPMResetAtMs: gatewayupstream.NowMs() + 2_500}
	attempt, failure := accountCapacityLimitRejection(testAccounts("a-1")[0], slot, 0)
	if attempt.UpstreamURL != "rpm:limit" {
		t.Fatalf("attempt upstreamURL = %q, want rpm:limit", attempt.UpstreamURL)
	}
	if attempt.RetryAfterMs < 1 || attempt.RetryAfterMs > 2_500 {
		t.Fatalf("attempt RetryAfterMs = %d, want [1, 2500]", attempt.RetryAfterMs)
	}
	if failure.kind != AccountCapacityFailureKindRPM || failure.retryAfterMs != attempt.RetryAfterMs {
		t.Fatalf("failure = %+v", failure)
	}
	concurrencyAttempt, concurrencyFailure := accountCapacityLimitRejection(testAccounts("a-1")[0], ConcurrencySlot{Acquired: false, Current: 5, Limit: 5, Lane: "text", LaneCurrent: 5, LaneLimit: 5}, 0)
	if concurrencyAttempt.UpstreamURL != "concurrency:limit" || concurrencyAttempt.RetryAfterMs != 0 || concurrencyFailure.kind != AccountCapacityFailureKindConcurrency {
		t.Fatalf("并发拒绝投影 = %+v / %+v", concurrencyAttempt, concurrencyFailure)
	}
}

// rpmFullStore 对指定账户恒返回 RPM 满拒绝（并发有余），其余账户成功。
type rpmFullStore struct {
	rpmFullAccountID string
	rpmResetAtMs     int64
	attempts         int
}

func (s *rpmFullStore) LoadCurrentAsync(context.Context, []string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (s *rpmFullStore) LoadCurrentByLaneAsync(context.Context, []string, string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (s *rpmFullStore) LoadCurrentRPMAsync(context.Context, []string) (map[string]AccountRPMState, error) {
	return map[string]AccountRPMState{}, nil
}

func (s *rpmFullStore) TryAcquireAsync(_ context.Context, accountID string, _ int, options AccountConcurrencyAcquireOptions) (ConcurrencySlot, error) {
	s.attempts++
	if accountID == s.rpmFullAccountID {
		return ConcurrencySlot{Acquired: false, Current: 0, Limit: 5, Lane: options.Lane, LaneCurrent: 0, LaneLimit: 5, RPMCurrent: options.RPMLimit, RPMLimit: options.RPMLimit, RPMResetAtMs: s.rpmResetAtMs}, nil
	}
	return ConcurrencySlot{Acquired: true, Current: 1, Limit: 5, Lane: options.Lane, Release: func() {}, MarkFirstOutput: func() {}}, nil
}

// TestDispatchRPMFailureSkipsToNextAccount：首候选 RPM 满时直接跳过并选中下一
// 候选（设计 §5.1：不写账户状态、无冷却，行为同本地容量不可用）。
func TestDispatchRPMFailureSkipsToNextAccount(t *testing.T) {
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	engine, driver, _ := newTestEngine(t)
	store := &rpmFullStore{rpmFullAccountID: "a-1", rpmResetAtMs: gatewayupstream.NowMs() + 60_000}
	engine.Concurrency = store
	engine.Config.AccountConcurrencyRetryBudgetMs = 0
	driver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
		"a-2": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1", "a-2"))
	args.RequestCoordination.ServerRetryBudget = gatewaypreauth.NewServerRetryBudget(5_000, gatewaypreauth.SystemClock{})
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("RPM 满候选应被跳过: %v", err)
	}
	if result.Account.ID != "a-2" {
		t.Fatalf("account = %s, want a-2", result.Account.ID)
	}
	if store.attempts < 2 {
		t.Fatalf("store attempts = %d, want 至少两次（首候选 + 下一候选）", store.attempts)
	}
}

func TestGatewayAccountRPMLimitsByAccountID(t *testing.T) {
	source := "src-1"
	accounts := []AccountCandidate{
		{ID: "a", RPMLimit: 60, CredentialSourceAccountID: &source},
		{ID: "b", RPMLimit: 30, CredentialSourceAccountID: &source},
		{ID: "c", RPMLimit: 0},
		{ID: "d", RPMLimit: -5},
	}
	limits := GatewayAccountRPMLimitsByAccountID(accounts)
	// 同 ID 多候选取较小非零上限；0 视为不限制；负值归一 0。
	if limits["src-1"] != 30 || limits["c"] != 0 || limits["d"] != 0 {
		t.Fatalf("limits = %#v", limits)
	}
	mixed := []AccountCandidate{
		{ID: "e", RPMLimit: 0},
		{ID: "e", RPMLimit: 45},
	}
	if limits := GatewayAccountRPMLimitsByAccountID(mixed); limits["e"] != 45 {
		t.Fatalf("0 与非零混合 = %#v, want 45", limits)
	}
}

func TestCapacityLimitFailureRPMHelpers(t *testing.T) {
	if allCapacityLimitFailuresAreRPM(nil) {
		t.Fatal("空列表不算全 RPM")
	}
	mixed := []AccountCapacityLimitFailure{
		{kind: AccountCapacityFailureKindRPM, retryAfterMs: 900},
		{kind: AccountCapacityFailureKindConcurrency},
	}
	if allCapacityLimitFailuresAreRPM(mixed) {
		t.Fatal("混合失败不算全 RPM")
	}
	if got := earliestRPMResetWaitMs(mixed); got != 900 {
		t.Fatalf("earliest = %d, want 900", got)
	}
	rpmOnly := []AccountCapacityLimitFailure{
		{kind: AccountCapacityFailureKindRPM, retryAfterMs: 1_500},
		{kind: AccountCapacityFailureKindRPM, retryAfterMs: 700},
	}
	if !allCapacityLimitFailuresAreRPM(rpmOnly) || earliestRPMResetWaitMs(rpmOnly) != 700 {
		t.Fatalf("全 RPM 判定/最早重置错误: %v %d", allCapacityLimitFailuresAreRPM(rpmOnly), earliestRPMResetWaitMs(rpmOnly))
	}
}

// rpmSequenceStore 按调用序回放预设槽（验证短等循环内的 RPM 短路）；序列耗尽
// 后重复返回末位槽。
type rpmSequenceStore struct {
	slots []ConcurrencySlot
	calls int
}

func (s *rpmSequenceStore) LoadCurrentAsync(context.Context, []string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (s *rpmSequenceStore) LoadCurrentByLaneAsync(context.Context, []string, string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (s *rpmSequenceStore) LoadCurrentRPMAsync(context.Context, []string) (map[string]AccountRPMState, error) {
	return map[string]AccountRPMState{}, nil
}

func (s *rpmSequenceStore) TryAcquireAsync(_ context.Context, _ string, _ int, options AccountConcurrencyAcquireOptions) (ConcurrencySlot, error) {
	s.calls++
	index := s.calls - 1
	if index >= len(s.slots) {
		index = len(s.slots) - 1
	}
	slot := s.slots[index]
	slot.Lane = options.Lane
	return slot, nil
}

// TestAcquireShortRetrySkipsWaitOnPureRPM：纯 RPM 拒绝（并发有余）不做并发短
// 等——首拒直接返回，waitedMs=0 且请求级预算原样保留（AI 账户每分钟请求上限
// 设计 §5.1：RPM 计数不随并发释放下降，短等只会叠加延迟）。
func TestAcquireShortRetrySkipsWaitOnPureRPM(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	engine.Config.AccountConcurrencyRetryInitialDelayMs = 20
	engine.Config.AccountConcurrencyRetryMaxDelayMs = 20
	store := &rpmSequenceStore{slots: []ConcurrencySlot{{
		Acquired: false, Current: 1, Limit: 5, LaneCurrent: 1, LaneLimit: 5,
		RPMCurrent: 2, RPMLimit: 2, RPMResetAtMs: gatewayupstream.NowMs() + 60_000,
	}}}
	engine.Concurrency = store
	coordination := newTestCoordination(t)
	slot, waitedMs, remainingWaitBudgetMs, err := engine.acquireAccountConcurrencyWithShortRetry(
		context.Background(), context.Background(), "a-1", 5, 2, 1_200, gatewayprotoLane("text"), nil, coordination.ServerRetryBudget, 0,
	)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if slot.Acquired {
		t.Fatal("RPM 满不应获取槽位")
	}
	if waitedMs != 0 {
		t.Fatalf("纯 RPM 拒绝不应短等, waitedMs=%d", waitedMs)
	}
	if remainingWaitBudgetMs != 1_200 {
		t.Fatalf("预算不应递减, remaining=%d want 1200", remainingWaitBudgetMs)
	}
	if store.calls != 1 {
		t.Fatalf("不应重试, acquire calls=%d", store.calls)
	}
}

// TestAcquireShortRetryBreaksOnPureRPMAfterWait：并发拒绝进入短等后，重取仍纯
// RPM 时立即结束（保留已等待时间与预算扣减，随外层候选切换），不再继续重试。
func TestAcquireShortRetryBreaksOnPureRPMAfterWait(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	engine.Config.AccountConcurrencyRetryInitialDelayMs = 20
	engine.Config.AccountConcurrencyRetryMaxDelayMs = 20
	store := &rpmSequenceStore{slots: []ConcurrencySlot{
		{Acquired: false, Current: 5, Limit: 5, LaneCurrent: 5, LaneLimit: 5, RPMCurrent: 0, RPMLimit: 2},
		{Acquired: false, Current: 1, Limit: 5, LaneCurrent: 1, LaneLimit: 5, RPMCurrent: 2, RPMLimit: 2, RPMResetAtMs: gatewayupstream.NowMs() + 60_000},
	}}
	engine.Concurrency = store
	coordination := newTestCoordination(t)
	slot, waitedMs, remainingWaitBudgetMs, err := engine.acquireAccountConcurrencyWithShortRetry(
		context.Background(), context.Background(), "a-1", 5, 2, 200, gatewayprotoLane("text"), nil, coordination.ServerRetryBudget, 0,
	)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if slot.Acquired {
		t.Fatal("RPM 满不应获取槽位")
	}
	if waitedMs != 20 || remainingWaitBudgetMs != 180 {
		t.Fatalf("已等待与预算扣减应保留, waited=%d remaining=%d want 20/180", waitedMs, remainingWaitBudgetMs)
	}
	if store.calls != 2 {
		t.Fatalf("转纯 RPM 后不应继续重试, acquire calls=%d", store.calls)
	}
}

// TestAcquireShortRetryStillWaitsOnDoubleFull：并发 + RPM 双满归属并发，保持既
// 有短等行为（并发槽可能随时释放）。
func TestAcquireShortRetryStillWaitsOnDoubleFull(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	engine.Config.AccountConcurrencyRetryInitialDelayMs = 20
	engine.Config.AccountConcurrencyRetryMaxDelayMs = 20
	store := &rpmSequenceStore{slots: []ConcurrencySlot{{
		Acquired: false, Current: 5, Limit: 5, LaneCurrent: 5, LaneLimit: 5,
		RPMCurrent: 2, RPMLimit: 2, RPMResetAtMs: gatewayupstream.NowMs() + 60_000,
	}}}
	engine.Concurrency = store
	coordination := newTestCoordination(t)
	slot, waitedMs, remainingWaitBudgetMs, err := engine.acquireAccountConcurrencyWithShortRetry(
		context.Background(), context.Background(), "a-1", 5, 2, 60, gatewayprotoLane("text"), nil, coordination.ServerRetryBudget, 0,
	)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if slot.Acquired {
		t.Fatal("双满不应获取槽位")
	}
	if waitedMs != 60 || remainingWaitBudgetMs != 0 {
		t.Fatalf("双满应走满短等预算, waited=%d remaining=%d want 60/0", waitedMs, remainingWaitBudgetMs)
	}
	if store.calls < 2 {
		t.Fatalf("双满应存在多次重试, acquire calls=%d", store.calls)
	}
}

// rpmStateStore 在 rpmFullStore 基础上回放 RPM 窗口状态，供预派发等待封顶
// helper 的单测使用。
type rpmStateStore struct {
	rpmFullStore
	states map[string]AccountRPMState
}

func (s *rpmStateStore) LoadCurrentRPMAsync(context.Context, []string) (map[string]AccountRPMState, error) {
	return s.states, nil
}
