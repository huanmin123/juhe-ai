package gatewaydispatch

import (
	"context"
	"errors"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// 准备管道的错误传播面：每个排序/配额/抑制端口的错误都必须中断准备并透传。

var errPortBoom = errors.New("端口爆炸")

type errorLatencyPort struct{}

func (errorLatencyPort) OrderAsync(context.Context, []AccountCandidate, *LatencyScopeInput, *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig, *gatewayrouting.GatewayAccountModelPriority) (LatencyDegradationOrder, error) {
	return LatencyDegradationOrder{}, errPortBoom
}

type errorProxyHealthPort struct{}

func (errorProxyHealthPort) OrderAsync(context.Context, []AccountCandidate, *gatewayrouting.GatewayAccountModelPriority) (ProxyHealthOrder, error) {
	return ProxyHealthOrder{}, errPortBoom
}

func (errorProxyHealthPort) RecordFailureAsync(context.Context, AccountCandidate, string) error {
	return nil
}

type errorClientIPAvoidancePort struct{}

func (errorClientIPAvoidancePort) OrderAsync(context.Context, []AccountCandidate, ClientIPAvoidanceScope, *gatewayrouting.GatewayAccountModelPriority) (AvoidanceOrder, error) {
	return AvoidanceOrder{}, errPortBoom
}

type errorClientSourceAvoidancePort struct{}

func (errorClientSourceAvoidancePort) OrderAsync(context.Context, []AccountCandidate, gatewaypreauth.ClientStrategyContext, *gatewayrouting.GatewayAccountModelPriority) (AvoidanceOrder, error) {
	return AvoidanceOrder{}, errPortBoom
}

type errorQuotaPort struct{}

func (errorQuotaPort) CheckBatchAsync(context.Context, gatewayruntimecache.GroupUsageAccessMetadata, []AccountCandidate) (map[string]QuotaDecision, error) {
	return nil, errPortBoom
}

type errorHotQualityPort struct{}

func (errorHotQualityPort) OrderAsync(context.Context, HotQualityOrderInput) (HotQualityOrder, error) {
	return HotQualityOrder{}, errPortBoom
}

type errorFilterSuppression struct{}

func (errorFilterSuppression) FilterAsync(context.Context, []AccountCandidate, SuppressionFilterOptions) (SuppressionFilterResult, error) {
	return SuppressionFilterResult{}, errPortBoom
}

func (errorFilterSuppression) ResolveLocalSuppressionFilter(ctx context.Context, input LocalSuppressionPreflightInput) (*SuppressionFilterResult, bool, error) {
	result := localSuppressionBypassResult(input.Accounts)
	return &result, false, nil
}

type errorAffinityPort struct {
	orderError bool
	busyError  bool
}

func (e *errorAffinityPort) OrderAsync(context.Context, []AccountCandidate, string, AffinityOrderingOptions) ([]AccountCandidate, error) {
	if e.orderError {
		return nil, errPortBoom
	}
	return nil, nil
}

func (e *errorAffinityPort) ClaimAsync(context.Context, string, string, AffinityScope) (string, bool) {
	return "", false
}

func (e *errorAffinityPort) RememberAsync(context.Context, string, string, AffinityScope) {}

func (e *errorAffinityPort) ForgetAsync(context.Context, string, string) error { return nil }

func (e *errorAffinityPort) AreHighConcurrencyAccountsBusyForLaneAsync(context.Context, []AccountCandidate, HighConcurrencyBusyOptions) (bool, error) {
	if e.busyError {
		return false, errPortBoom
	}
	return false, nil
}

type errorDegradationPort struct{}

func (errorDegradationPort) OrderGatewayAccountsByRuntimeDegradation([]AccountCandidate, map[string]int) DegradationOrder {
	return DegradationOrder{}
}

func (errorDegradationPort) OrderWithLaneAsync(context.Context, []AccountCandidate, string, *gatewayruntimecache.GroupSchedulingPolicy, *gatewayrouting.GatewayAccountModelPriority) (DegradationOrder, error) {
	return DegradationOrder{}, errPortBoom
}

func (errorDegradationPort) OrderSync([]AccountCandidate, *gatewayrouting.GatewayAccountModelPriority) DegradationOrder {
	return DegradationOrder{}
}

// appliedDegradationPort 报告已应用（触发审计元数据分支）。
type appliedDegradationPort struct{}

func (appliedDegradationPort) OrderGatewayAccountsByRuntimeDegradation(accounts []AccountCandidate, _ map[string]int) DegradationOrder {
	return DegradationOrder{Accounts: accounts, Applied: true, DegradedCount: 1}
}

func (appliedDegradationPort) OrderWithLaneAsync(ctx context.Context, accounts []AccountCandidate, requestLane string, policy *gatewayruntimecache.GroupSchedulingPolicy, priority *gatewayrouting.GatewayAccountModelPriority) (DegradationOrder, error) {
	return (appliedDegradationPort{}).OrderGatewayAccountsByRuntimeDegradation(accounts, nil), nil
}

func (appliedDegradationPort) OrderSync(accounts []AccountCandidate, priority *gatewayrouting.GatewayAccountModelPriority) DegradationOrder {
	return (appliedDegradationPort{}).OrderGatewayAccountsByRuntimeDegradation(accounts, nil)
}

func TestPrepareDispatchAccountsPortErrorsPropagate(t *testing.T) {
	cases := []struct {
		name  string
		setup func(engine *Engine)
	}{
		{"affinity_order", func(engine *Engine) { engine.Affinity = &errorAffinityPort{orderError: true} }},
		{"suppression_filter", func(engine *Engine) { engine.Suppression = errorFilterSuppression{} }},
		{"latency", func(engine *Engine) { engine.Latency = errorLatencyPort{} }},
		{"proxy_health", func(engine *Engine) { engine.ProxyHealth = errorProxyHealthPort{} }},
		{"client_ip_avoidance", func(engine *Engine) { engine.ClientIPAvoidance = errorClientIPAvoidancePort{} }},
		{"client_source_avoidance", func(engine *Engine) { engine.ClientSourceAvoidance = errorClientSourceAvoidancePort{} }},
		{"quota", func(engine *Engine) { engine.Quota = errorQuotaPort{} }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			pipeline, engine, _, _ := newPipeline(t)
			testCase.setup(engine)
			input := dispatchPreparationInput(t, testAccounts("a-1"))
			_, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
			if !errors.Is(err, errPortBoom) {
				t.Fatalf("expected 端口爆炸, got %v", err)
			}
		})
	}
}

func TestPrepareDispatchAccountsDegradationAppliedAudits(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	engine.Degradation = appliedDegradationPort{}
	sink := &fakeAuditSink{}
	input := dispatchPreparationInput(t, testAccounts("a-1", "a-2"))
	input.AuditCapture = AuditCapture{Context: &frozenAudit{sink: sink}, Sink: sink}
	result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("PrepareDispatchAccounts: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeAccounts {
		t.Fatalf("outcome = %s", result.Outcome)
	}
	if sink.started != 0 && sink.failed != 0 {
		t.Fatal("准备阶段不应产生尝试审计")
	}
}

func TestRequestRouteFallbackListStatesError(t *testing.T) {
	_, engine, _, _ := newPipeline(t)
	engine.Locks = listStatesErrorLocks{}
	pipeline := NewCandidatePipeline(engine)
	attempted, _, err := pipeline.requestRouteFallback(context.Background(), dispatchPreparationInput(t, testAccounts("a-1")), testAccounts("a-1"), "upstream_accounts_exhausted")
	if !errors.Is(err, errPortBoom) {
		t.Fatalf("expected 端口爆炸, got %v", err)
	}
	if attempted {
		t.Fatal("错误时不得回退")
	}
}

type listStatesErrorLocks struct {
	blockingLocks
}

func (listStatesErrorLocks) ListStatesAsync(context.Context, []string) (map[string]AccountLockStateView, error) {
	return nil, errPortBoom
}

func TestFetchFirstAvailableUpstreamDegradationError(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	engine.Degradation = errorDegradationPort{}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	if _, err := engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, testAccounts("a-1"))); !errors.Is(err, errPortBoom) {
		t.Fatalf("expected 端口爆炸, got %v", err)
	}
}
