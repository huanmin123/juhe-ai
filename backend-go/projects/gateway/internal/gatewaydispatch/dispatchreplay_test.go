package gatewaydispatch

// 2026-10-22 生产故障形态回放（调度慢失败直切与来源避让记账及墙钟收尾修复设
// 计 §7「回放」行，PLAN-20261022T171949672Z）：同一 (apiKeyID, clientIP) scope
// 下三个候选按派发优先序 a-0 → a-1 → a-2，a-0 上游慢失败（时间缩放：120ms
// 耗时 ≥ 50ms 阈值）、a-1 健康。组合回放三刀：
//  A 同账户重试快速失败门槛——慢失败不预留同账户重试（handleFailedCalls==1），
//    直接轮换下一候选，审计带 same_account_retry_skipped_slow_failure；
//  B 来源避让记账 + 成功确认——请求内失败进 PendingFailures，成功结算确认
//    FailureCount+1，两次确认后下一请求读侧沉底；探针/诊断流量不记账不确认；
//  C 墙钟——时间缩放下预算充足，全程不出现 GatewayRequestWallBudgetExhaustedError。

import (
	"context"
	"net/http"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// dispatchReplay 是回放组合夹具：真实 gatewayclientip.Avoidance 记忆体跨请求
// 持有避让状态，每个请求新建 tracker（与生产 per-request 生命周期同构）；
// 记账回调与成功确认镜像 chain 层接线（clientIPAccountAvoidanceRememberFunc /
// confirmClientIPAccountAvoidanceAfterSuccess），诊断流量侧由真实的
// gatewayusage.IsAccountDiagnosticTrafficSource 过滤。
type dispatchReplay struct {
	engine     *Engine
	driver     *fakeDriver
	dispatcher *fakeFailureDispatcher
	avoidance  *gatewayclientip.Avoidance
	scope      gatewayclientip.AvoidanceScopeInput
	settings   gatewaySettingsType
	accounts   []AccountCandidate
}

func newDispatchReplay(t *testing.T, accounts []AccountCandidate) *dispatchReplay {
	t.Helper()
	engine, driver, dispatcher := newTestEngine(t)
	avoidance, err := gatewayclientip.NewAvoidance(gatewayclientip.AvoidanceOptions{})
	if err != nil {
		t.Fatalf("NewAvoidance: %v", err)
	}
	t.Cleanup(avoidance.Close)
	return &dispatchReplay{
		engine:     engine,
		driver:     driver,
		dispatcher: dispatcher,
		avoidance:  avoidance,
		scope: gatewayclientip.AvoidanceScopeInput{
			SystemAccountID: "system-1", GroupID: "group-1", APIKeyID: "apikey-1", ClientIP: "203.0.113.9",
		},
		settings: fastDispatchSettings(),
		accounts: accounts,
	}
}

// replayOutcome 汇总单次请求的可观察结果。
type replayOutcome struct {
	result         UpstreamDispatchResult
	tracker        *gatewayclientip.AvoidanceTracker
	audit          *retryBackoffMetadataAudit
	failedCalls    int64
	handleErrCalls int64
}

// dispatchOnce 派发一次请求：慢失败阈值按既有时间缩放手法取 50ms；诊断流量
// 不注入记账回调（与 chain 层同构）；拒绝任何墙钟拒启（场景 5）。
func (r *dispatchReplay) dispatchOnce(t *testing.T, trafficSource string, candidates []AccountCandidate) replayOutcome {
	t.Helper()
	args := fastDispatchArgs(t, newTestRequest(t, `{"model":"gpt-test","stream":false}`), candidates)
	args.Settings.SameAccountRetryFastFailMaxMs = 50
	args.UsageContext.TrafficSource = trafficSource
	audit := newRetryBackoffMetadataAudit()
	args.AuditCapture = AuditCapture{Context: audit, Sink: &fakeAuditSink{}}
	tracker := r.avoidance.CreateAvoidanceTracker(r.scope)
	if !gatewayusage.IsAccountDiagnosticTrafficSource(trafficSource) {
		args.RememberUpstreamAttemptFailure = func(accountID string, failure gatewayclientip.AccountFailure) {
			r.avoidance.RememberPendingFailure(tracker, accountID, failure.AccountName, failure)
		}
	}
	failedBefore := r.dispatcher.handleFailedCalls.Load()
	handleErrBefore := r.dispatcher.handleErrorCalls.Load()
	result, err := r.engine.FetchFirstAvailableUpstream(context.Background(), args)
	var budgetErr *GatewayRequestWallBudgetExhaustedError
	if errorsAs(err, &budgetErr) {
		t.Fatalf("时间缩放下墙钟预算充足，不得出现墙钟拒启: %v", err)
	}
	if err != nil {
		t.Fatalf("FetchFirstAvailableUpstream: %v", err)
	}
	return replayOutcome{
		result:         result,
		tracker:        tracker,
		audit:          audit,
		failedCalls:    r.dispatcher.handleFailedCalls.Load() - failedBefore,
		handleErrCalls: r.dispatcher.handleErrorCalls.Load() - handleErrBefore,
	}
}

// confirmAfterSuccess 镜像 chain 层成功结算确认：诊断流量直接跳过，其余流量
// ConfirmAfterFinalFailureAsync 把 PendingFailures 确认为避让 entry。
func (r *dispatchReplay) confirmAfterSuccess(t *testing.T, trafficSource string, tracker *gatewayclientip.AvoidanceTracker) {
	t.Helper()
	if gatewayusage.IsAccountDiagnosticTrafficSource(trafficSource) {
		return
	}
	if _, err := r.avoidance.ConfirmAfterFinalFailureAsync(context.Background(), tracker, &r.settings); err != nil {
		t.Fatalf("ConfirmAfterFinalFailureAsync: %v", err)
	}
}

// avoidanceFailureCount 读共享避让记忆体中该账号的 FailureCount（无记录为 0）。
func (r *dispatchReplay) avoidanceFailureCount(t *testing.T, accountID string) int {
	t.Helper()
	for _, row := range r.avoidance.SnapshotForTest() {
		if row.AccountID == accountID {
			return row.FailureCount
		}
	}
	return 0
}

// candidatesByAvoidance 用读侧内存路径 OrderAccountsByClientIPAccountAvoidance
// 按当前避让状态排候选序（「避让只在下一请求排序时读取」的生产读法）。
func (r *dispatchReplay) candidatesByAvoidance(t *testing.T) ([]AccountCandidate, gatewayclientip.AvoidanceOrderResult) {
	t.Helper()
	secrets := make([]gatewayruntimecache.OpenAIAccountSecret, 0, len(r.accounts))
	for _, account := range r.accounts {
		secrets = append(secrets, gatewayruntimecache.OpenAIAccountSecret{ID: account.ID, Priority: account.Priority})
	}
	ordered := r.avoidance.OrderAccountsByClientIPAccountAvoidance(secrets, r.scope, nil)
	byID := make(map[string]AccountCandidate, len(r.accounts))
	for _, account := range r.accounts {
		byID[account.ID] = account
	}
	candidates := make([]AccountCandidate, 0, len(ordered.Accounts))
	for _, secret := range ordered.Accounts {
		candidates = append(candidates, byID[secret.ID])
	}
	return candidates, ordered
}

// assertDirectSwitchToHealthy 断言一次「a-0 慢失败直切 a-1 成功」请求的共有
// 形态：终态落在 a-1 且 2xx、无同账户重试预留、带慢失败直切审计。
func assertDirectSwitchToHealthy(t *testing.T, outcome replayOutcome, label string) {
	t.Helper()
	if outcome.result.Account.ID != "a-1" || !outcome.result.Response.OK() {
		t.Fatalf("%s: 慢失败应直切 a-1 成功, account = %s ok = %v", label, outcome.result.Account.ID, outcome.result.Response.OK())
	}
	if outcome.failedCalls != 1 {
		t.Fatalf("%s: 慢失败不得预留同账户重试, failed calls delta = %d", label, outcome.failedCalls)
	}
	if skips := outcome.audit.entries["same_account_retry_skipped_slow_failure"]; len(skips) != 1 {
		t.Fatalf("%s: 缺少慢失败直切审计, entries = %v", label, outcome.audit.entries)
	}
}

// TestDispatchReplay20261022SlowFailureDirectSwitchAndSourceAvoidance：
// 2026-10-22 生产故障形态回放——三账户 trace 中 a-0 连续慢失败，A 刀直切保
// 交付，B 刀按请求把失败计入来源避让并在两次确认后沉底，C 刀预算内必达终态。
func TestDispatchReplay20261022SlowFailureDirectSwitchAndSourceAvoidance(t *testing.T) {
	// 三个候选同层、按派发优先序 a-0 → a-1 → a-2（fakeDegradation 按入参透传，
	// 照既有用例）。来源避让的沉底只在同一派发优先层内生效（gatewayclientip
	// 排序保层），与生产 trace 的三账户同层形态一致。
	accounts := testAccounts("a-0", "a-1", "a-2")
	failServer := slowFailServer(t, 120, http.StatusBadGateway)
	okServer := sequentialServer(t, 0, http.StatusBadGateway)
	spareServer := sequentialServer(t, 0, http.StatusBadGateway)
	replay := newDispatchReplay(t, accounts)
	replay.driver.urlByAccount = map[string][]string{
		"a-0": {failServer.URL + "/v1/chat/completions"},
		"a-1": {okServer.URL + "/v1/chat/completions"},
		"a-2": {spareServer.URL + "/v1/chat/completions"},
	}

	// 请求 1：a-0 attempt 1 慢失败（120ms ≥ 50ms 阈值）→ 无同账户重试，
	// attempt 2 落 a-1 成功；成功结算确认后 a-0 FailureCount==1（< 阈值 2，
	// 未沉底）。
	first := replay.dispatchOnce(t, gatewayusage.TrafficSourceGateway, accounts)
	assertDirectSwitchToHealthy(t, first, "请求 1")
	if len(first.tracker.PendingFailures) != 1 || first.tracker.PendingFailures[0].AccountID != "a-0" {
		t.Fatalf("请求 1 结算点应记入 a-0 一条 pending: %+v", first.tracker.PendingFailures)
	}
	pending := first.tracker.PendingFailures[0]
	if pending.ErrorPhase != "upstream_response" || pending.StatusCode == nil || *pending.StatusCode != http.StatusBadGateway {
		t.Fatalf("请求 1 pending 现场不符: %+v", pending)
	}
	if pending.Endpoint != failServer.URL+"/v1/chat/completions" {
		t.Fatalf("请求 1 endpoint = %q", pending.Endpoint)
	}
	replay.confirmAfterSuccess(t, gatewayusage.TrafficSourceGateway, first.tracker)
	if len(first.tracker.PendingFailures) != 0 {
		t.Fatalf("请求 1 确认后 pending 应清空: %+v", first.tracker.PendingFailures)
	}
	if got := replay.avoidanceFailureCount(t, "a-0"); got != 1 {
		t.Fatalf("请求 1 确认后 FailureCount = %d, want 1", got)
	}
	if _, ordered := replay.candidatesByAvoidance(t); ordered.Applied || len(ordered.AvoidedAccountIDs) != 0 {
		t.Fatalf("FailureCount=1 低于阈值 2 不得沉底: %+v", ordered)
	}

	// 请求 2：a-0 再次慢失败 → 直切 a-1 成功 → FailureCount==2 达阈值，下一
	// 请求读侧沉底。
	second := replay.dispatchOnce(t, gatewayusage.TrafficSourceGateway, accounts)
	assertDirectSwitchToHealthy(t, second, "请求 2")
	if len(second.tracker.PendingFailures) != 1 || second.tracker.PendingFailures[0].AccountID != "a-0" {
		t.Fatalf("请求 2 结算点应记入 a-0 一条 pending: %+v", second.tracker.PendingFailures)
	}
	replay.confirmAfterSuccess(t, gatewayusage.TrafficSourceGateway, second.tracker)
	if got := replay.avoidanceFailureCount(t, "a-0"); got != 2 {
		t.Fatalf("请求 2 确认后 FailureCount = %d, want 2", got)
	}
	avoidedOrder, ordered := replay.candidatesByAvoidance(t)
	if !ordered.Applied || len(ordered.AvoidedAccountIDs) != 1 || ordered.AvoidedAccountIDs[0] != "a-0" {
		t.Fatalf("请求 2 确认后应沉底 a-0: %+v", ordered)
	}
	if avoidedOrder[0].ID != "a-1" || avoidedOrder[len(avoidedOrder)-1].ID != "a-0" {
		t.Fatalf("沉底排序不符: %v", accountIDs(avoidedOrder))
	}

	// 穿插探针/诊断流量：带 a-0 失败尝试但不记账不确认——FailureCount 不变
	//（探针/诊断流量不进记账，正常号与避让计数零扰动）。
	beforeDiagnostic := replay.avoidanceFailureCount(t, "a-0")
	diagnostic := replay.dispatchOnce(t, gatewayusage.TrafficSourceManualAccountTest, accounts)
	assertDirectSwitchToHealthy(t, diagnostic, "诊断请求")
	if len(diagnostic.tracker.PendingFailures) != 0 {
		t.Fatalf("诊断流量不得记账: %+v", diagnostic.tracker.PendingFailures)
	}
	replay.confirmAfterSuccess(t, gatewayusage.TrafficSourceManualAccountTest, diagnostic.tracker)
	if got := replay.avoidanceFailureCount(t, "a-0"); got != beforeDiagnostic {
		t.Fatalf("诊断流量不得进记账: FailureCount = %d, want %d", got, beforeDiagnostic)
	}

	// 请求 3：a-0 被避让沉底——按读侧沉底序派发，首次尝试直接是 a-1
	//（attempted accounts 不含 a-0：a-0 上游只会慢失败，若被尝试必然产生
	// 失败分类调用），成功交付。
	third := replay.dispatchOnce(t, gatewayusage.TrafficSourceGateway, avoidedOrder)
	if third.result.Account.ID != "a-1" || !third.result.Response.OK() {
		t.Fatalf("请求 3 首次尝试应直接落 a-1 成功, account = %s ok = %v", third.result.Account.ID, third.result.Response.OK())
	}
	if third.failedCalls != 0 || third.handleErrCalls != 0 {
		t.Fatalf("请求 3 不得触达 a-0（零失败分类）, failed = %d, handleErr = %d", third.failedCalls, third.handleErrCalls)
	}
	if len(third.tracker.PendingFailures) != 0 {
		t.Fatalf("请求 3 无失败尝试, pending 应为空: %+v", third.tracker.PendingFailures)
	}
	replay.confirmAfterSuccess(t, gatewayusage.TrafficSourceGateway, third.tracker)

	// 全程收尾：只有 a-0 进记账（两次前台失败，诊断不计数），a-1/a-2 正常号
	// 零扰动；终态全部在墙钟预算内达成（dispatchOnce 内逐请求断言）。
	snapshot := replay.avoidance.SnapshotForTest()
	if len(snapshot) != 1 || snapshot[0].AccountID != "a-0" || snapshot[0].FailureCount != 2 || !snapshot[0].Active {
		t.Fatalf("终态 snapshot = %+v", snapshot)
	}
}
