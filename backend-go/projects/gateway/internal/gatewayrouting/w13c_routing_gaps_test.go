package gatewayrouting

// w13c 覆盖率补充测试：补齐 w11d 之后仍 uncovered 的分支。
//
// 不可达语句清单（维持守卫、不删除）：
//   - normalroute.go ResolveNormalGatewayModelRoute 中 199-208（outcome !=
//     Matched 的兜底失败臂）与 217-227（candidateBindings 为空）：outcome
//     仅有 matched/missing/ambiguous 三种，missing 在 130 提前返回、ambiguous
//     在 175/188 提前返回，且 resolveCatalogProviderRoute 已在 Matched 时
//     校验 providerCode 必然绑定，两处兜底不可达。
//   - firstbytedeadline.go ResolveNormalRouteAttemptFirstByteDeadline 的
//     ClipFirstByteDeadlineMs 错误臂：Clip 的全部错误源（负 configured、
//     负 reserve、precommit 错误）都在同函数更早处被归一化或先行返回，
//     limiting.value 全部来自归一化后的非负值。
//   - rediscounter.go NextRouteCounterIndex 的 int64/string 负值与非数值
//     分支：Lua 脚本恒返回 (INCR-1)%modulo >= 0 的整数，go-redis 对该脚本
//     只会返回 int64。
//   - routecoordination.go ClipFirstByteDeadlineMs 的 precommit 错误臂与
//     clipped < 0 钳位：reserve 已先行归一化，clipped 的三个来源均已
//     非负钳位。
//   - routecoordination.go CanAttemptAccount/TryRecordDispatchAttempt 中
//     stringSet.has/add 的错误臂：入参在进入前均经过
//     normalizedRequiredKey 归一化，空 key 无法到达。
//   - routecoordination.go sameAccountRetryRemaining 的负值钳位：每次预留
//     自身即占用额度（reserve 递增 count 且要求 remaining > 0），count 恒
//     <= limit，无法通过公开 API 产生负值。

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

type w13cFakeCounter struct {
	index int64
	err   error
}

func (c *w13cFakeCounter) NextRouteCounterIndex(context.Context, string, int64) (int64, error) {
	return c.index, c.err
}

func TestW13CRouteStateWeightedTrim(t *testing.T) {
	registry := newRouteStateRegistry()
	for i := 0; i < apiKeyGroupRouteStateMaxEntries+3; i++ {
		registry.weightedStateStore(fmt.Sprintf("w13c-key-%d", i), map[string]int64{"a": 1})
	}
	registry.mu.Lock()
	size := len(registry.weighted)
	keys := len(registry.weightedKeys)
	registry.mu.Unlock()
	if size > apiKeyGroupRouteStateMaxEntries || keys > apiKeyGroupRouteStateMaxEntries {
		t.Fatalf("weighted state must trim: size=%d keys=%d", size, keys)
	}
}

func TestW13COrderWeightedSyncDebtComparator(t *testing.T) {
	selector := NewAPIKeyGroupRouteSelector("memory", nil, "")
	apiKey := &APIKeyRow{
		ID:                "w13c-weighted-sync",
		RouteStrategyMode: RouteStrategyModeWeighted,
		GroupBindings: []GroupBindingRow{
			w9eBinding("b1", "g1", 1, w9eInt64(1)),
			w9eBinding("b2", "g2", 2, w9eInt64(2)),
			w9eBinding("b3", "g3", 3, w9eInt64(3)),
		},
	}
	for round := 0; round < 7; round++ {
		ordered, err := selector.OrderAPIKeyGroupBindingsForDispatch(apiKey)
		if err != nil {
			t.Fatalf("round %d = %v", round, err)
		}
		if len(ordered) != 3 {
			t.Fatalf("round %d length = %d", round, len(ordered))
		}
	}
}

func TestW13COrderWeightedRedisDebtComparator(t *testing.T) {
	mr := miniredis.RunT(t)
	selector := NewAPIKeyGroupRouteSelector("redis", NewRedisRouteStateCounter("redis://"+mr.Addr()+"/0"), "redis://"+mr.Addr()+"/0")
	apiKey := &APIKeyRow{
		ID:                "w13c-weighted-redis",
		RouteStrategyMode: RouteStrategyModeWeighted,
		GroupBindings: []GroupBindingRow{
			w9eBinding("b1", "g1", 1, w9eInt64(2)),
			w9eBinding("b2", "g2", 2, w9eInt64(1)),
			w9eBinding("b3", "g3", 3, w9eInt64(3)),
		},
	}
	for round := 0; round < 7; round++ {
		ordered, err := selector.OrderAPIKeyGroupBindingsForDispatchAsync(context.Background(), apiKey)
		if err != nil {
			t.Fatalf("round %d = %v", round, err)
		}
		if len(ordered) != 3 {
			t.Fatalf("round %d length = %d", round, len(ordered))
		}
	}
}

func TestW13CAsyncFallbackUnknownModeAndCounterClamp(t *testing.T) {
	// 非 redis 驱动的 async 回落同步路径。
	local := NewAPIKeyGroupRouteSelector("memory", nil, "")
	apiKey := &APIKeyRow{
		ID:                "w13c-async-local",
		RouteStrategyMode: RouteStrategyModeRoundRobin,
		GroupBindings:     []GroupBindingRow{w9eBinding("b1", "g1", 1, nil), w9eBinding("b2", "g2", 2, nil)},
	}
	ordered, err := local.OrderAPIKeyGroupBindingsForDispatchAsync(context.Background(), apiKey)
	if err != nil || len(ordered) != 2 {
		t.Fatalf("local async = %v err=%v", ordered, err)
	}

	// redis 驱动下未知策略模式直接返回原序。
	mr := miniredis.RunT(t)
	redisSelector := NewAPIKeyGroupRouteSelector("redis", NewRedisRouteStateCounter("redis://"+mr.Addr()+"/0"), "redis://"+mr.Addr()+"/0")
	plainKey := &APIKeyRow{
		ID:                "w13c-async-plain",
		RouteStrategyMode: RouteStrategyModeNormal,
		GroupBindings:     []GroupBindingRow{w9eBinding("b1", "g1", 1, nil), w9eBinding("b2", "g2", 2, nil)},
	}
	ordered, err = redisSelector.OrderAPIKeyGroupBindingsForDispatchAsync(context.Background(), plainKey)
	if err != nil || len(ordered) != 2 {
		t.Fatalf("plain async = %v err=%v", ordered, err)
	}

	// 计数器负值钳 0、错误透传。
	negative := &w13cFakeCounter{index: -7}
	clamped := NewAPIKeyGroupRouteSelector("redis", negative, "redis://unused:6379/0")
	index, err := clamped.nextRedisRouteCounterIndex(context.Background(), "k", 3)
	if err != nil || index != 0 {
		t.Fatalf("negative index must clamp to 0, got %d err=%v", index, err)
	}
	failing := NewAPIKeyGroupRouteSelector("redis", &w13cFakeCounter{err: errors.New("w13c counter down")}, "redis://unused:6379/0")
	if _, err := failing.nextRedisRouteCounterIndex(context.Background(), "k", 3); err == nil {
		t.Fatal("counter error must propagate")
	}
}

func TestW13CRedisCounterNilContextAndTransportFailure(t *testing.T) {
	mr := miniredis.RunT(t)
	counter := NewRedisRouteStateCounter("redis://" + mr.Addr() + "/0")
	first, err := counter.NextRouteCounterIndex(nil, "w13c-nil-ctx", 5)
	if err != nil || first != 0 {
		t.Fatalf("nil ctx = %d err=%v", first, err)
	}
	mr2 := miniredis.RunT(t)
	broken := NewRedisRouteStateCounter("redis://" + mr2.Addr() + "/0")
	if _, err := broken.NextRouteCounterIndex(context.Background(), "w13c-warm", 5); err != nil {
		t.Fatalf("warm-up must succeed, got %v", err)
	}
	mr2.Close()
	if _, err := broken.NextRouteCounterIndex(context.Background(), "w13c-warm", 5); err == nil {
		t.Fatal("closed redis must fail the eval")
	}
}

func TestW13CRequestViewFamilyArms(t *testing.T) {
	if got := anthropicMessagesRequestEndpointFamily("POST", "/v1"); got != "" {
		t.Fatalf("root v1 has no specific family, got %q", got)
	}
	if got := geminiEndpointFamilyFromPath("/v1beta/models/m:unknownAction"); got != "" {
		t.Fatalf("unknown gemini action must be empty, got %q", got)
	}
	if got := requestModelFromGeminiPath("/v1beta/models/%zz:generateContent", ""); got != "%zz" {
		t.Fatalf("bad escape must fall back to raw match, got %q", got)
	}
}

func TestW13CWallBudgetArms(t *testing.T) {
	// WithoutLimit 在 accepted<=0 时预算取 MaxInt64。
	unanchored := &GatewayRequestWallBudget{RequestAcceptedAtMs: 0, BudgetMs: 1_000, DeadlineAtMs: 1_000}
	unbounded := unanchored.WithoutLimit()
	if !unbounded.Unbounded || unbounded.BudgetMs <= 0 {
		t.Fatalf("WithoutLimit zero anchor = %+v", unbounded)
	}

	// PrecommitRemainingMs 取预算截止与 precommit 截止的较小者。
	now := int64(5_000)
	budget, err := NewGatewayRequestWallBudget(GatewayRequestWallBudgetOptions{
		RequestAcceptedAtMs: 4_000, BudgetMs: w9eInt64(2_000), Now: func() int64 { return now },
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	remaining, err := budget.PrecommitRemainingMs(PrecommitBudgetInput{
		NowMs:                        &now,
		RequestPrecommitDeadlineAtMs: w9eInt64(9_000),
		FinalResponseReserveMs:       w9eInt64(100),
	})
	if err != nil || remaining != 1_000-100 {
		t.Fatalf("precommit remaining = %d err=%v", remaining, err)
	}

	// HandoffRequired 走 normalizedNonNegativeMs(nil) 默认臂。
	if _, err := budget.HandoffRequired(GatewayRequestWallBudgetDecision{NowMs: &now}); err != nil {
		t.Fatal(err)
	}
}

func TestW13CPauseWaitArms(t *testing.T) {
	budget, err := NewRouteCoordinationBudget(RouteCoordinationBudgetOptions{
		RequestID: "w13c-pause", Now: func() int64 { return 1_000 },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := budget.PauseWait(RouteCoordinationBudgetTransitionInput{WaitToken: "  "}); err == nil {
		t.Fatal("blank wait token must fail")
	}
	begin, err := budget.BeginWait(RouteCoordinationBudgetTransitionInput{WaitToken: "w13c-token", ExpectedVersion: 0})
	if err != nil || begin.Outcome != BudgetTransitionApplied {
		t.Fatalf("begin = %+v err=%v", begin, err)
	}
	if _, err := budget.PauseWait(RouteCoordinationBudgetTransitionInput{WaitToken: "w13c-token", ExpectedVersion: -1}); err == nil {
		t.Fatal("negative version must fail")
	}
	conflict, err := budget.PauseWait(RouteCoordinationBudgetTransitionInput{WaitToken: "w13c-token", ExpectedVersion: 99})
	if err != nil || conflict.Outcome != BudgetTransitionVersionConflict {
		t.Fatalf("conflict = %+v err=%v", conflict, err)
	}
	pause, err := budget.PauseWait(RouteCoordinationBudgetTransitionInput{WaitToken: "w13c-token", ExpectedVersion: 1})
	if err != nil || pause.Outcome != BudgetTransitionApplied {
		t.Fatalf("pause = %+v err=%v", pause, err)
	}
}

func TestW13CAttemptTrackerArms(t *testing.T) {
	blankAccount, err := NewGatewayRequestAttemptTracker(&GatewayRequestAttemptSnapshot{
		AttemptedAccountRuntimeKeys: []string{"a", " "},
	})
	if err == nil || blankAccount != nil {
		t.Fatal("blank initial account key must fail")
	}
	blankFingerprint, err := NewGatewayRequestAttemptTracker(&GatewayRequestAttemptSnapshot{
		AttemptedKeyFingerprints: []string{" "},
	})
	if err == nil || blankFingerprint != nil {
		t.Fatal("blank initial fingerprint must fail")
	}

	tracker, err := NewGatewayRequestAttemptTracker(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tracker.HasAccountRuntimeKey("  "); err == nil {
		t.Fatal("blank has key must fail")
	}

	identity := GatewayDispatchAttemptIdentity{
		ProtocolModelKey: "pm", AccountRuntimeKey: "A1", PhysicalCredentialKey: "P1", KeyFingerprint: "F1",
	}
	if _, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{GatewayDispatchAttemptIdentity: GatewayDispatchAttemptIdentity{AccountRuntimeKey: "A1"}}); err == nil {
		t.Fatal("blank protocol model key must fail")
	}
	if _, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{GatewayDispatchAttemptIdentity: GatewayDispatchAttemptIdentity{ProtocolModelKey: "pm", PhysicalCredentialKey: "P1"}}); err == nil {
		t.Fatal("blank account key must fail")
	}
	if _, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{GatewayDispatchAttemptIdentity: GatewayDispatchAttemptIdentity{ProtocolModelKey: "pm", AccountRuntimeKey: "A1"}}); err == nil {
		t.Fatal("blank physical key must fail")
	}
	if _, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{
		GatewayDispatchAttemptIdentity: identity,
		SameAccountRetryID:             "  ",
	}); err == nil {
		t.Fatal("blank same-account retry id must fail")
	}

	first, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{GatewayDispatchAttemptIdentity: identity})
	if err != nil || !first.Allowed {
		t.Fatalf("first = %+v err=%v", first, err)
	}

	// 确认重试 + 允许换 key：确认已尝试 + 指纹已尝试 → 两个拒绝臂。
	confirmation := identity
	confirmation.KeyFingerprint = "F1"
	confirm, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{
		GatewayDispatchAttemptIdentity: confirmation, MatchingConfirmation: true,
	})
	if err != nil || !confirm.Allowed {
		t.Fatalf("confirm = %+v err=%v", confirm, err)
	}
	retry, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{
		GatewayDispatchAttemptIdentity: confirmation, MatchingConfirmation: true, AllowKeyRotation: true,
	})
	if err != nil || retry.Allowed {
		t.Fatalf("rotation retry = %+v err=%v", retry, err)
	}
	if retry.Reason != RejectKeyFingerprintAlreadyAttempted && retry.Reason != RejectKeyRotationNotApplicable {
		t.Fatalf("rotation retry reason = %q", retry.Reason)
	}
}

func TestW13CSameAccountRetryBudgetFlow(t *testing.T) {
	tracker, err := NewGatewayRequestAttemptTracker(nil)
	if err != nil {
		t.Fatal(err)
	}
	identity := GatewayDispatchAttemptIdentity{
		ProtocolModelKey: "pm", AccountRuntimeKey: "A9", PhysicalCredentialKey: "P9",
	}
	registered, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{GatewayDispatchAttemptIdentity: identity})
	if err != nil || !registered.Allowed {
		t.Fatalf("register = %+v err=%v", registered, err)
	}
	// 预留本身即占用重试额度：同一 identity 的第二次预留必然额度耗尽。
	first, err := tracker.TryReserveSameAccountRetry(GatewaySameAccountRetryReservationInput{
		GatewayDispatchAttemptIdentity: identity, MaxRetries: 1,
	})
	if err != nil || !first.Reserved {
		t.Fatalf("first reserve = %+v err=%v", first, err)
	}
	second, err := tracker.TryReserveSameAccountRetry(GatewaySameAccountRetryReservationInput{
		GatewayDispatchAttemptIdentity: identity, MaxRetries: 1,
	})
	if err != nil || second.Reserved || second.Reason != SameAccountRetryBudgetExhausted {
		t.Fatalf("second reserve = %+v err=%v", second, err)
	}
	got, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{
		GatewayDispatchAttemptIdentity: identity,
		SameAccountRetryID:             first.RetryID,
	})
	if err != nil || !got.Allowed {
		t.Fatalf("record retry = %+v err=%v", got, err)
	}
	replayed, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{
		GatewayDispatchAttemptIdentity: identity,
		SameAccountRetryID:             first.RetryID,
	})
	if err != nil || replayed.Allowed {
		t.Fatalf("replayed retry must be rejected: %+v err=%v", replayed, err)
	}
}

func TestW13CCreateRoutePlanSnapshotErrorArms(t *testing.T) {
	base := CreateGatewayRoutePlanSnapshotInput[string]{
		RoutePlanID:           "w13c-plan",
		OrderedAllowedTargets: []string{"t1"},
	}
	if _, err := CreateGatewayRoutePlanSnapshot[string](base); err != nil {
		t.Fatal(err)
	}
	badBudget := base
	badBudget.GatewayRequestWallBudgetMs = w9eInt64(-1)
	if _, err := CreateGatewayRoutePlanSnapshot[string](badBudget); err == nil {
		t.Fatal("negative wall budget must fail")
	}
	badReserve := base
	badReserve.FinalResponseReserveMs = w9eInt64(-1)
	if _, err := CreateGatewayRoutePlanSnapshot[string](badReserve); err == nil {
		t.Fatal("negative reserve must fail")
	}
}

func TestW13CUniqueGroupBindingsSkipsEmpty(t *testing.T) {
	unique := uniqueGatewayGroupBindings([]GroupBindingRow{
		{ID: "b0", GroupID: ""},
		{ID: "b1", GroupID: "g1"},
		{ID: "b2", GroupID: "g1"},
		{ID: "b3", GroupID: "g2"},
	})
	if len(unique) != 2 || unique[0].ID != "b1" || unique[1].ID != "b3" {
		t.Fatalf("unique = %+v", unique)
	}
}

func TestW13CNormalRouteDuplicateProviderCodes(t *testing.T) {
	binding := GroupBindingRow{
		ID: "b1", APIKeyID: "key1", SystemAccountID: "owner1",
		GroupID: "g1", Priority: 1, Status: RowStatusActive,
		ProviderCode: "gpt", GroupEnabled: 1,
	}
	duplicate := binding
	duplicate.ID = "b2"
	duplicate.GroupID = "g2"
	multiKey := &APIKeyRow{
		ID: "key1", SystemAccountID: "owner1",
		RouteStrategyMode: RouteStrategyModeNormal, Status: RowStatusActive,
		GroupBindings: []GroupBindingRow{binding, duplicate},
	}
	cache := newFakeRuntimeCache()
	cache.groupAccess["g1"] = GroupUsageAccessMetadata{ProviderCode: "gpt", GroupAccessType: GroupAccessTypeOwner}
	cache.groupAccess["g2"] = GroupUsageAccessMetadata{ProviderCode: "gpt", GroupAccessType: GroupAccessTypeOwner}
	cache.accounts["g1"] = []UpstreamAccount{{ID: "a1", ProviderCode: "gpt"}}
	cache.accounts["g2"] = []UpstreamAccount{{ID: "a2", ProviderCode: "gpt"}}
	service := NewNormalModelRouteService(cache, PassthroughCapabilityFilter{})
	request := RequestView{Method: http.MethodPost, OriginalURL: "/v1/chat/completions", BodyModel: "gpt-4o"}
	recorder := httptest.NewRecorder()
	_ = recorder
	if strings.TrimSpace(request.BodyModel) == "" {
		t.Fatal("model must be present")
	}
	if _, err := service.ResolveNormalGatewayModelRoute(context.Background(), ResolveNormalGatewayModelRouteInput{
		APIKeyRecord: multiKey,
		Request:      request,
	}); err != nil {
		t.Fatalf("duplicate provider codes must resolve: %v", err)
	}
}
