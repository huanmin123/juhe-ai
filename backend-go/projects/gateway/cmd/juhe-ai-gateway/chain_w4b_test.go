package main

// W4-B（BUG-0175）D-98/D-111/D-114/D-132 的组合根适配层 Mock 回归测试。
// 全部依赖以 Mock 注入（内存 store / spy writer / fake 端口），可回放、
// 结果稳定，覆盖关键正常与边界路径。

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accountkeystates"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------
// D-98：配额恢复 hint 的数组递归
// ---------------------------------------------------------------------------

func TestExtractAPIKeyQuotaRecoveryHintRecursesIntoArrays(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	resetAt := float64(1767225600000) // 2026-01-01T01:00:00Z
	body := `{"error":{"errors":[{"type":"quota_exceeded","reset_at":` + formatFloat(resetAt) + `}]}}`
	hint := extractAPIKeyQuotaRecoveryHint(body, nil, now)
	if hint == nil {
		t.Fatal("array-nested reset_at must produce an explicit reset hint")
	}
	if hint.Mode != quotaRecoveryModeExplicitReset || hint.Source != "reset_at" {
		t.Errorf("hint = %+v", hint)
	}
	want := time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC).UTC().Format(rfc3339MillisUTC)
	if hint.CooldownUntil != want {
		t.Errorf("cooldownUntil = %s, want %s", hint.CooldownUntil, want)
	}
	// 顶层字段语义不变。
	topLevel := `{"reset_after_seconds":30}`
	hint2 := extractAPIKeyQuotaRecoveryHint(topLevel, nil, now)
	if hint2 == nil || hint2.Source != "reset_at" {
		t.Fatalf("top-level hint = %+v", hint2)
	}
	// 数组缺字段时保持 nil。
	if hint3 := extractAPIKeyQuotaRecoveryHint(`{"data":[{}]}`, nil, now); hint3 != nil {
		t.Errorf("unexpected hint = %+v", hint3)
	}
}

func formatFloat(value float64) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// ---------------------------------------------------------------------------
// D-111：API Key 效果链端口适配与效果链消费
// ---------------------------------------------------------------------------

type w4bSpyKeyWriter struct {
	failures []gatewayaccounteffects.AccountAPIKeyFailureWrite
	successes []gatewayaccounteffects.AccountAPIKeySuccessWrite
}

func (w *w4bSpyKeyWriter) RecordFailure(_ context.Context, write gatewayaccounteffects.AccountAPIKeyFailureWrite) (gatewayaccounteffects.APIKeyWriteResult, error) {
	w.failures = append(w.failures, write)
	return gatewayaccounteffects.APIKeyWriteResult{Changed: true}, nil
}

func (w *w4bSpyKeyWriter) RecordSuccess(_ context.Context, write gatewayaccounteffects.AccountAPIKeySuccessWrite) (gatewayaccounteffects.APIKeyWriteResult, error) {
	w.successes = append(w.successes, write)
	return gatewayaccounteffects.APIKeyWriteResult{Changed: true}, nil
}

func w4bKeyEffectsFixture() (*chainAPIKeyEffectsPort, *w4bSpyKeyWriter, *gatewayaccounteffects.AccountAPIKeyFailureGuard) {
	guardConfig := gatewayaccounteffects.SideEffectsConfig{RuntimeStateDriver: "memory"}
	guard := gatewayaccounteffects.NewAccountAPIKeyFailureGuard(guardConfig, gatewayaccounteffects.SystemClock{}, nil, nil)
	writer := &w4bSpyKeyWriter{}
	effects := gatewayaccounteffects.NewAccountAPIKeyEffects(
		guardConfig, guard, writer, nil,
		gatewayaccounteffects.SystemClock{}, gatewayaccounteffects.RealScheduler{}, nil,
	)
	return &chainAPIKeyEffectsPort{effects: effects, guard: guard}, writer, guard
}

func w4bAccount() gatewaydispatch.AccountCandidate {
	fingerprint := "fp-1"
	return gatewaydispatch.AccountCandidate{
		ID:                        "acc-1",
		Type:                      "api_key",
		SelectedAPIKeyFingerprint: &fingerprint,
	}
}

func TestChainAPIKeyEffectsPortRecordFailureReachesWriter(t *testing.T) {
	port, writer, _ := w4bKeyEffectsFixture()
	epoch := port.CaptureFailureObservation(w4bAccount())
	if epoch == "" {
		t.Fatal("observation epoch must be captured")
	}
	err := port.RecordFailure(context.Background(), w4bAccount(), gatewaydispatch.RecordAPIKeyFailureInput{
		Status:        "temporary_unavailable",
		StatusCode:    503,
		ErrorMessage:  "upstream exploded",
		TraceID:       "trace-1",
		MutationContext: map[string]any{"authority": "confirmed_same_account_key_rotation", "trafficSource": "gateway"},
		ObservationEpoch: epoch,
		TrafficSource:    "gateway",
		Source:           "same_account_api_key_rotation_confirmed",
	})
	if err != nil {
		t.Fatalf("record failure must never surface the write error: %v", err)
	}
	if len(writer.failures) != 1 {
		t.Fatalf("writer failures = %d, want 1", len(writer.failures))
	}
	write := writer.failures[0]
	if write.Input.Status != gatewayaccounteffects.APIKeyStatusTemporaryUnavailable {
		t.Errorf("status = %s", write.Input.Status)
	}
	if write.Input.StatusCode == nil || *write.Input.StatusCode != 503 {
		t.Errorf("statusCode = %+v", write.Input.StatusCode)
	}
	if write.Input.TraceID == nil || *write.Input.TraceID != "trace-1" {
		t.Errorf("traceId = %+v", write.Input.TraceID)
	}
	if write.Input.MutationContext.Authority != gatewayaccounteffects.MutationAuthorityConfirmedSameAccountKeyRotation {
		t.Errorf("mutation authority = %s", write.Input.MutationContext.Authority)
	}
	if write.Input.ObservationEpoch == "" {
		t.Error("observedAt must carry the attempt start instant")
	}
}

func TestChainAPIKeyEffectsPortRecordSuccessClearsLocalAvoidance(t *testing.T) {
	port, writer, guard := w4bKeyEffectsFixture()
	account := w4bAccount()
	// 先记一次本地失败（gateway 流量、无 mutation context → 进程内屏蔽）。
	if err := port.RecordFailure(context.Background(), account, gatewaydispatch.RecordAPIKeyFailureInput{
		Status:        "temporary_unavailable",
		TrafficSource: "gateway",
		Source:        "upstream_request_error",
	}); err != nil {
		t.Fatalf("record failure: %v", err)
	}
	states := guard.LocalRuntimeStatesForDispatch("acc-1")
	if len(states) != 1 {
		t.Fatalf("local states = %+v, want one suppression", states)
	}
	// 成功结算：guard 复位（瞬态清理在 memory 驱动下是进程内面）。
	if err := port.RecordSuccess(context.Background(), account, gatewaydispatch.RecordAPIKeySuccessInput{
		Source:        "upstream_dispatch_success",
		TrafficSource: "gateway",
	}); err != nil {
		t.Fatalf("record success: %v", err)
	}
	if states := guard.LocalRuntimeStatesForDispatch("acc-1"); len(states) != 0 {
		t.Errorf("states after success = %+v, want cleared", states)
	}
	if len(writer.successes) != 0 {
		t.Errorf("gateway traffic must not authorize the persistent success write, got %d", len(writer.successes))
	}
}

func TestChainAPIKeyObservationEpochRoundTrip(t *testing.T) {
	port, writer, _ := w4bKeyEffectsFixture()
	epoch := port.CaptureFailureObservation(w4bAccount())
	parsed := chainObservationEpochPtrOf(epoch)
	if parsed == nil || *parsed <= 0 {
		t.Fatalf("epoch round trip = %+v from %q", parsed, epoch)
	}
	if chainObservationEpochPtrOf("") != nil {
		t.Error("empty epoch must map to nil")
	}
	_ = writer
}

func TestChainRuntimeCachePortTransientStatesFolding(t *testing.T) {
	_, _, guard := w4bKeyEffectsFixture()
	account := w4bAccount()
	if err := guard.RecordTransientFailure(context.Background(), account, gatewayaccounteffects.APIKeyStatusRateLimited); err != nil {
		t.Fatalf("transient failure (memory driver no-op): %v", err)
	}
	// memory 驱动：先注入进程内屏蔽再读取。
	guard.RecordFailureGuard(account, gatewayaccounteffects.GatewayAccountApiKeyFailureGuardInput{
		Status:        gatewayaccounteffects.APIKeyStatusRateLimited,
		TrafficSource: "gateway",
	})
	port := &chainRuntimeCachePort{guard: guard}
	states, err := port.LoadApiKeyTransientStatesForDispatch(context.Background(), "acc-1", []string{"fp-1", "fp-2"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("states = %+v, want one folded entry", states)
	}
	if states[0].Fingerprint != "fp-1" || !states[0].Disabled {
		t.Errorf("state = %+v, want fingerprint fp-1 folded to Disabled", states[0])
	}
	// 守卫未装配（组合测试装配）保持空集降级。
	nilPort := &chainRuntimeCachePort{}
	empty, err := nilPort.LoadApiKeyTransientStatesForDispatch(context.Background(), "acc-1", []string{"fp-1"})
	if err != nil || empty != nil {
		t.Errorf("nil guard degrade = %+v err %v, want nil/nil", empty, err)
	}
}

// ---------------------------------------------------------------------------
// D-114：速度优先运行态配置解码 + 时延降级端口
// ---------------------------------------------------------------------------

func w4bSpeedFirstConfig(deadline int64, overrides map[string]any) *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig {
	raw := map[string]any{"speedFirstConfig": overrides}
	return &gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig{
		SchedulingPreference: "speed_first",
		FirstByteDeadlineMs:  &deadline,
		Raw:                  raw,
	}
}

func TestChainSpeedFirstRuntimeConfigDecodesKnobsAndDefaults(t *testing.T) {
	deadline := int64(8_000)
	typed := chainSpeedFirstRuntimeConfigOf(w4bSpeedFirstConfig(deadline, map[string]any{
		"slowTriggerCount":              4,
		"slowWindowSeconds":             90,
		"recoverySuccessCount":          5,
		"probeIntervalSeconds":          20,
		"degradedTtlSeconds":            600,
		"maxFirstByteRetriesPerRequest": 3,
	}))
	if typed == nil {
		t.Fatal("typed config missing")
	}
	if typed.FirstByteDeadlineMs != 8_000 || typed.SlowTriggerCount != 4 || typed.SlowWindowSeconds != 90 ||
		typed.RecoverySuccessCount != 5 || typed.ProbeIntervalSeconds != 20 || typed.DegradedTTLSeconds != 600 ||
		typed.MaxFirstByteRetriesPerRequest != 3 {
		t.Errorf("typed = %+v", typed)
	}
	// 缺省旋钮回落归一化默认（routestrategies 默认值组）。
	defaults := chainSpeedFirstRuntimeConfigOf(w4bSpeedFirstConfig(deadline, map[string]any{}))
	if defaults == nil || defaults.SlowTriggerCount != 3 || defaults.SlowWindowSeconds != 120 ||
		defaults.RecoverySuccessCount != 3 || defaults.ProbeIntervalSeconds != 30 || defaults.DegradedTTLSeconds != 300 ||
		defaults.MaxFirstByteRetriesPerRequest != 2 {
		t.Errorf("defaults = %+v", defaults)
	}
	// deadline 缺失 = 配置无效（排序直通、观测跳过）。
	noDeadline := &gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig{SchedulingPreference: "speed_first", Raw: map[string]any{}}
	if chainSpeedFirstRuntimeConfigOf(noDeadline) != nil {
		t.Error("nil deadline must invalidate the config")
	}
	if chainSpeedFirstRuntimeConfigOf(nil) != nil {
		t.Error("nil config must stay nil")
	}
}

func TestChainLatencyDegradationPortOrdersAndRecords(t *testing.T) {
	service := gatewayproxyhealth.NewLatencyDegradationService(gatewayproxyhealth.NewMemoryRuntimeStateStore(nil), nil, gatewayproxyhealth.LatencyDegradationOptions{
		LockRetryDelay: func(int) {},
	})
	port := chainLatencyDegradationPort{service: service}
	scope := &gatewaydispatch.LatencyScopeInput{SystemAccountID: "sys-1", RouteStrategyID: "rs-1", GroupID: "grp-1"}
	config := w4bSpeedFirstConfig(5_000, map[string]any{"slowTriggerCount": 2})

	slow := gatewaydispatch.AccountCandidate{ID: "acc-slow"}
	fast := gatewaydispatch.AccountCandidate{ID: "acc-fast"}
	accounts := []gatewaydispatch.AccountCandidate{slow, fast}

	// 无状态：排序直通。
	order, err := port.OrderAsync(context.Background(), accounts, scope, config, nil)
	if err != nil || order.Applied {
		t.Fatalf("plain order = %+v err %v", order, err)
	}
	// 两次慢采样触发降级。
	deadline := int64(5_000)
	if _, err := port.RecordFirstByteSlowAsync(context.Background(), slow, scope, config, "慢采样 1"); err != nil {
		t.Fatalf("slow 1: %v", err)
	}
	if _, err := port.RecordFirstByteSlowAsync(context.Background(), slow, scope, config, "慢采样 2"); err != nil {
		t.Fatalf("slow 2: %v", err)
	}
	degraded, err := port.IsAccountLatencyDegradedAsync(context.Background(), slow, scope)
	if err != nil || !degraded {
		t.Fatalf("degraded = %v err %v", degraded, err)
	}
	order, err = port.OrderAsync(context.Background(), accounts, scope, config, nil)
	if err != nil {
		t.Fatalf("degraded order: %v", err)
	}
	if !order.Applied || len(order.DegradedAccountIDs) != 1 || order.DegradedAccountIDs[0] != "acc-slow" {
		t.Errorf("order = %+v", order)
	}
	if order.Accounts[0].ID != "acc-fast" || order.Accounts[1].ID != "acc-slow" {
		t.Errorf("accounts = %+v, want fast first", order.Accounts)
	}
	// 达标成功采样进入恢复计数；达到 recoverySuccessCount（默认 3）后清理。
	for i := 0; i < 3; i++ {
		result, err := port.RecordFirstByteSuccessAsync(context.Background(), slow, scope, config, 1_000)
		if err != nil {
			t.Fatalf("success %d: %v", i, err)
		}
		if i == 2 && (result == nil || !result.Cleared) {
			t.Errorf("third success should clear, got %+v", result)
		}
	}
	cleared, err := port.IsAccountLatencyDegradedAsync(context.Background(), slow, scope)
	if err != nil || cleared {
		t.Errorf("cleared = %v err %v, want false", cleared, err)
	}
	// nil 端口保持直通（组合测试语义）。
	var nilPort chainLatencyDegradationPort
	passthrough, err := nilPort.OrderAsync(context.Background(), accounts, scope, config, nil)
	if err != nil || len(passthrough.Accounts) != 2 || passthrough.Accounts[0].ID != "acc-slow" {
		t.Errorf("nil port order = %+v err %v", passthrough, err)
	}
}

// ---------------------------------------------------------------------------
// D-132：配置策略避让装饰器与响应层副作用
// ---------------------------------------------------------------------------

type w4bInnerSuppression struct {
	accounts []gatewaydispatch.AccountCandidate
}

func (s *w4bInnerSuppression) FilterAsync(_ context.Context, accounts []gatewaydispatch.AccountCandidate, _ gatewaydispatch.SuppressionFilterOptions) (gatewaydispatch.SuppressionFilterResult, error) {
	s.accounts = accounts
	return gatewaydispatch.SuppressionFilterResult{Accounts: accounts}, nil
}

func (s *w4bInnerSuppression) ResolveLocalSuppressionFilter(ctx context.Context, input gatewaydispatch.LocalSuppressionPreflightInput) (*gatewaydispatch.SuppressionFilterResult, bool, error) {
	result, err := s.FilterAsync(ctx, input.Accounts, gatewaydispatch.SuppressionFilterOptions{})
	return &result, false, err
}

func TestChainConfiguredPolicyAvoidanceSuppressionFiltersAndMerges(t *testing.T) {
	avoidance := gatewayaccounteffects.NewConfiguredPolicyAvoidanceService(
		gatewayaccounteffects.NewConfiguredPolicyAvoidanceMemoryStoreForTest(),
		nil, nil, gatewayaccounteffects.SystemClock{},
	)
	seconds := int64(300)
	if err := avoidance.SuppressGatewayAccountLocallyForSeconds(
		context.Background(), gatewayaccounteffects.SuppressibleGatewayAccount{ID: "acc-a"}, &seconds, "响应检查策略命中：TTL 避让"); err != nil {
		t.Fatalf("suppress: %v", err)
	}
	inner := &w4bInnerSuppression{}
	decorated := &chainConfiguredPolicyAvoidanceSuppression{inner: inner, avoidance: avoidance}
	accounts := []gatewaydispatch.AccountCandidate{
		{ID: "acc-a"},
		{ID: "acc-b"},
	}
	result, err := decorated.FilterAsync(context.Background(), accounts, gatewaydispatch.SuppressionFilterOptions{})
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	if len(result.Accounts) != 1 || result.Accounts[0].ID != "acc-b" {
		t.Errorf("accounts = %+v, want acc-b only", result.Accounts)
	}
	if len(result.ConfiguredPolicySuppressedAccountIDs) != 1 || result.ConfiguredPolicySuppressedAccountIDs[0] != "acc-a" {
		t.Errorf("configured ids = %+v", result.ConfiguredPolicySuppressedAccountIDs)
	}
	if result.NextRetryAfterMs == nil || *result.NextRetryAfterMs <= 0 || *result.NextRetryAfterMs > 300_000 {
		t.Errorf("nextRetryAfterMs = %+v", result.NextRetryAfterMs)
	}
	// 幸存者交给内层过滤。
	if len(inner.accounts) != 1 || inner.accounts[0].ID != "acc-b" {
		t.Errorf("inner accounts = %+v", inner.accounts)
	}
	// 全员被避让：allSuppressed 合并语义。
	suppressed, err := decorated.FilterAsync(context.Background(), []gatewaydispatch.AccountCandidate{{ID: "acc-a"}}, gatewaydispatch.SuppressionFilterOptions{})
	if err != nil {
		t.Fatalf("filter all: %v", err)
	}
	if !suppressed.AllSuppressed || len(suppressed.Accounts) != 0 {
		t.Errorf("all-suppressed result = %+v", suppressed)
	}
}

func TestChainResponseAccountEffectsAppliesInspectionSideEffects(t *testing.T) {
	avoidance := gatewayaccounteffects.NewConfiguredPolicyAvoidanceService(
		gatewayproxyhealth.NewMemoryRuntimeStateStore(nil),
		nil, nil, gatewayaccounteffects.SystemClock{},
	)
	effects := &chainResponseAccountEffects{
		avoidance:   avoidance,
		proxyHealth: gatewayproxyhealth.NewProxyHealthService(nil, gatewayproxyhealth.NewMemoryRuntimeStateStore(nil), gatewayproxyhealth.ProxyHealthOptions{}, nil),
		cache:       newChainFixture(t).cache,
		affinity:    nil,
	}
	decision := &gatewayresponse.ResponseInspectionDecision{
		Reason:       "configured_response_policy",
		Action:       "replace_with_failure",
		AccountState: "runtime_avoidance",
		PolicyName:   "封禁词策略",
	}
	account := gatewayresponse.OpenAIAccountView{Account: gatewayruntimecache.OpenAIAccountSecret{ID: "acc-ttl"}}
	if err := effects.ApplyInspectionPolicySideEffects(decision, account, true); err != nil {
		t.Fatalf("apply: %v", err)
	}
	states, err := avoidance.LoadConfiguredPolicyAvoidanceStates(context.Background(), []string{"acc-ttl"})
	if err != nil || states[0] == nil {
		t.Fatalf("avoidance state = %+v err %v, want written", states, err)
	}
	if states[0].Reason != "响应检查策略命中：封禁词策略" {
		t.Errorf("reason = %s", states[0].Reason)
	}
	// dry_run / 非配置策略分支不写。
	dryRun := &gatewayresponse.ResponseInspectionDecision{Reason: "configured_response_policy", Action: "dry_run", AccountState: "runtime_avoidance"}
	if err := effects.ApplyInspectionPolicySideEffects(dryRun, account, true); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	systemDefault := &gatewayresponse.ResponseInspectionDecision{Reason: "before_downstream_write_response_failure", AccountState: "runtime_avoidance"}
	if err := effects.ApplyInspectionPolicySideEffects(systemDefault, account, true); err != nil {
		t.Fatalf("system default: %v", err)
	}
	final, err := avoidance.LoadConfiguredPolicyAvoidanceStates(context.Background(), []string{"acc-ttl"})
	if err != nil || final[0] == nil {
		t.Fatalf("state vanished: %+v %v", final, err)
	}
	if final[0].Reason != "响应检查策略命中：封禁词策略" {
		t.Error("non-authorizing decisions must not overwrite the state")
	}
}

func TestChainAccountKeystateTargetProjection(t *testing.T) {
	target := chainKeyStateTargetOf(w4bAccount())
	if target.AccountID != "acc-1" || target.SelectedAPIKeyFingerprint != "fp-1" {
		t.Errorf("target = %+v", target)
	}
}
