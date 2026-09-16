package gatewayrouting

// w11d 覆盖补齐：normalroute 错误臂、coordination 校验与预算分支、
// tracker 注册错误臂、requestview 空值臂、first-byte deadline 错误传播。

import (
	"context"
	"errors"
	"math"
	"testing"
)


func TestW11DNormalRouteErrorArms(t *testing.T) {
	binding := GroupBindingRow{
		ID: "b1", APIKeyID: "key1", SystemAccountID: "owner1",
		GroupID: "g1", Priority: 1, Status: RowStatusActive,
		ProviderCode: "gpt", GroupEnabled: 1,
	}
	apiKey := &APIKeyRow{
		ID: "key1", SystemAccountID: "owner1",
		RouteStrategyMode: RouteStrategyModeNormal, Status: RowStatusActive,
		GroupBindings: []GroupBindingRow{binding},
	}
	request := RequestView{Method: "POST", OriginalURL: "/v1/chat/completions", BodyModel: "gpt-4o"}
	input := ResolveNormalGatewayModelRouteInput{
		APIKeyRecord: apiKey,
		Request:      request,
	}

	// 目录路由解析失败 → 错误上抛（127/296 臂）；目录解析需多供应商绑定。
	bindingAnthropic := binding
	bindingAnthropic.ID = "b2"
	bindingAnthropic.GroupID = "g2"
	bindingAnthropic.ProviderCode = "anthropic"
	multiKey := &APIKeyRow{
		ID: "key1", SystemAccountID: "owner1",
		RouteStrategyMode: RouteStrategyModeNormal, Status: RowStatusActive,
		GroupBindings: []GroupBindingRow{binding, bindingAnthropic},
	}
	multiInput := input
	multiInput.APIKeyRecord = multiKey
	cacheErr := newFakeRuntimeCache()
	cacheErr.groupAccess["g1"] = GroupUsageAccessMetadata{ProviderCode: "gpt", GroupAccessType: GroupAccessTypeOwner}
	cacheErr.groupAccess["g2"] = GroupUsageAccessMetadata{ProviderCode: "anthropic", GroupAccessType: GroupAccessTypeOwner}
	cacheErr.accounts["g1"] = []UpstreamAccount{{ID: "a1", ProviderCode: "gpt"}}
	cacheErr.accounts["g2"] = []UpstreamAccount{{ID: "a2", ProviderCode: "anthropic"}}
	cacheErr.routeErr = errors.New("w11d route down")
	serviceErr := NewNormalModelRouteService(cacheErr, PassthroughCapabilityFilter{})
	if _, err := serviceErr.ResolveNormalGatewayModelRoute(context.Background(), multiInput); err == nil {
		t.Fatal("目录路由失败必须上抛")
	}

	// 账户装载失败 → 错误上抛（154 臂）。
	cacheAccounts := newFakeRuntimeCache()
	cacheAccounts.groupAccess["g1"] = GroupUsageAccessMetadata{ProviderCode: "gpt", GroupAccessType: GroupAccessTypeOwner}
	cacheAccounts.groupAccess["g2"] = GroupUsageAccessMetadata{ProviderCode: "anthropic", GroupAccessType: GroupAccessTypeOwner}
	cacheAccounts.accounts["g1"] = []UpstreamAccount{{ID: "a1", ProviderCode: "gpt"}}
	cacheAccounts.providerRoute = func(in ProviderModelRouteInput) (ProviderModelRouteResolution, error) {
		return ProviderModelRouteResolution{
			Outcome: ProviderModelRouteMatched, ModelKey: in.Model,
			ProviderCode: "gpt", MatchedProviderCodes: []string{"gpt"},
		}, nil
	}
	cacheAccounts.accountsErr = errors.New("w11d accounts down")
	serviceAccounts := NewNormalModelRouteService(cacheAccounts, PassthroughCapabilityFilter{})
	if _, err := serviceAccounts.ResolveNormalGatewayModelRoute(context.Background(), multiInput); err == nil {
		t.Fatal("账户装载失败必须上抛")
	}

	// 目录 ambiguous → failed（199 臂）。
	cacheAmbiguous := newFakeRuntimeCache()
	cacheAmbiguous.groupAccess["g1"] = GroupUsageAccessMetadata{ProviderCode: "gpt", GroupAccessType: GroupAccessTypeOwner}
	cacheAmbiguous.groupAccess["g2"] = GroupUsageAccessMetadata{ProviderCode: "anthropic", GroupAccessType: GroupAccessTypeOwner}
	cacheAmbiguous.accounts["g1"] = []UpstreamAccount{{ID: "a1", ProviderCode: "gpt"}}
	cacheAmbiguous.providerRoute = func(in ProviderModelRouteInput) (ProviderModelRouteResolution, error) {
		return ProviderModelRouteResolution{
			Outcome: ProviderModelRouteAmbiguous, ModelKey: in.Model,
			MatchedProviderCodes: []string{"gpt", "anthropic"},
		}, nil
	}
	serviceAmbiguous := NewNormalModelRouteService(cacheAmbiguous, PassthroughCapabilityFilter{})
	result, err := serviceAmbiguous.ResolveNormalGatewayModelRoute(context.Background(), multiInput)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != NormalRouteOutcomeFailed {
		t.Fatalf("ambiguous outcome = %+v", result)
	}

	// 候选绑定为空 → failed（217 臂）：唯一绑定分组禁用。
	disabled := binding
	disabled.GroupEnabled = 0
	disabled.Status = RowStatusDisabled
	emptyKey := &APIKeyRow{
		ID: "key1", SystemAccountID: "owner1",
		RouteStrategyMode: RouteStrategyModeNormal, Status: RowStatusActive,
		GroupBindings: []GroupBindingRow{disabled},
	}
	cacheEmpty := newFakeRuntimeCache()
	serviceEmpty := NewNormalModelRouteService(cacheEmpty, PassthroughCapabilityFilter{})
	emptyInput := input
	emptyInput.APIKeyRecord = emptyKey
	result, err = serviceEmpty.ResolveNormalGatewayModelRoute(context.Background(), emptyInput)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != NormalRouteOutcomeSkipped || result.Reason != SkipReasonEmptyBinding {
		t.Fatalf("空候选 outcome = %+v", result)
	}

	// 未知 provider code 的绑定跳过目录匹配（284 臂）。
	mixed := []GroupBindingRow{binding}
	other := binding
	other.ID = "b2"
	other.GroupID = "g2"
	other.ProviderCode = "other"
	mixed = append(mixed, other)
	mixedKey := &APIKeyRow{
		ID: "key1", SystemAccountID: "owner1",
		RouteStrategyMode: RouteStrategyModeNormal, Status: RowStatusActive,
		GroupBindings: mixed,
	}
	cacheMixed := newFakeRuntimeCache()
	cacheMixed.groupAccess["g1"] = GroupUsageAccessMetadata{ProviderCode: "gpt", GroupAccessType: GroupAccessTypeOwner}
	cacheMixed.groupAccess["g2"] = GroupUsageAccessMetadata{ProviderCode: "other", GroupAccessType: GroupAccessTypeOwner}
	cacheMixed.accounts["g1"] = []UpstreamAccount{{ID: "a1", ProviderCode: "gpt"}}
	cacheMixed.accounts["g2"] = []UpstreamAccount{{ID: "a2", ProviderCode: "other"}}
	serviceMixed := NewNormalModelRouteService(cacheMixed, PassthroughCapabilityFilter{})
	mixedInput := input
	mixedInput.APIKeyRecord = mixedKey
	if _, err := serviceMixed.ResolveNormalGatewayModelRoute(context.Background(), mixedInput); err != nil {
		t.Fatal(err)
	}
	if len(cacheMixed.callsRoute) > 1 {
		t.Fatalf("未知 provider code 不应进目录匹配: %+v", cacheMixed.callsRoute)
	}
}

func TestW11DCoordinationValidationArms(t *testing.T) {
	// 空 RequestID / BudgetID / Now 默认。
	if _, err := NewRouteCoordinationBudget(RouteCoordinationBudgetOptions{RequestID: " "}); err == nil {
		t.Fatal("空 RequestID 必须失败")
	}
	budget, err := NewRouteCoordinationBudget(RouteCoordinationBudgetOptions{RequestID: "r1", BudgetID: " custom "})
	if err != nil {
		t.Fatal(err)
	}
	if budget.BudgetID != "custom" {
		t.Fatalf("BudgetID 归一 = %q", budget.BudgetID)
	}
	defaulted, err := NewRouteCoordinationBudget(RouteCoordinationBudgetOptions{RequestID: "r2"})
	if err != nil || defaulted.now() <= 0 {
		t.Fatal("Now 默认时钟必须可用")
	}

	// 初始快照带非法键 → 构造失败（769-775 臂）。
	badSnapshot := &GatewayRequestAttemptSnapshot{
		AttemptedAccountRuntimeKeys:     []string{"ok", " "},
		AttemptedPhysicalCredentialKeys: []string{"ok"},
	}
	if _, err := NewGatewayRequestAttemptTracker(badSnapshot); err == nil {
		t.Fatal("非法初始键必须失败")
	}
	badFingerprint := &GatewayRequestAttemptSnapshot{
		AttemptedKeyFingerprints:   []string{" "},
		AttemptedProtocolModelKeys: []string{"ok"},
	}
	if _, err := NewGatewayRequestAttemptTracker(badFingerprint); err == nil {
		t.Fatal("非法指纹键必须失败")
	}

	tracker, err := NewGatewayRequestAttemptTracker(nil)
	if err != nil {
		t.Fatal(err)
	}
	// 空身份注册 → 错误。
	if _, err := tracker.TryReserveSameAccountRetry(GatewaySameAccountRetryReservationInput{
		GatewayDispatchAttemptIdentity: GatewayDispatchAttemptIdentity{},
		MaxRetries:                     1,
	}); err == nil {
		t.Fatal("空身份预约必须失败")
	}
	if _, err := tracker.TryReserveSameAccountRetry(GatewaySameAccountRetryReservationInput{
		GatewayDispatchAttemptIdentity: GatewayDispatchAttemptIdentity{
			ProtocolModelKey: "pm", AccountRuntimeKey: "ar", PhysicalCredentialKey: "pc",
		},
		MaxRetries: 99,
	}); err == nil {
		t.Fatal("越界 MaxRetries 必须失败")
	}
	if _, err := tracker.CanAttemptAccount(CanAttemptAccountInput{}); err == nil {
		t.Fatal("空 CanAttemptAccount 必须失败")
	}
	if _, err := tracker.CanAttemptAccount(CanAttemptAccountInput{AccountRuntimeKey: "ar"}); err == nil {
		t.Fatal("缺物理键必须失败")
	}
	if _, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{}); err == nil {
		t.Fatal("空记录身份必须失败")
	}
	// 合法注册后再看 has/rotation 臂。
	valid := GatewayDispatchAttemptIdentity{
		ProtocolModelKey: "pm", AccountRuntimeKey: "ar", PhysicalCredentialKey: "pc", KeyFingerprint: "fp",
	}
	if reg, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{
		GatewayDispatchAttemptIdentity: valid,
	}); err != nil || !reg.Allowed {
		t.Fatalf("首次注册 = %+v err=%v", reg, err)
	}
	// 同身份重复注册。
	if reg, err := tracker.TryRecordDispatchAttempt(GatewayDispatchAttemptRecordInput{
		GatewayDispatchAttemptIdentity: valid,
	}); err != nil || reg.Allowed {
		t.Fatalf("重复注册 = %+v err=%v", reg, err)
	}
	// RecordAccountRuntimeKey 空。
	if _, err := tracker.RecordAccountRuntimeKey(" "); err == nil {
		t.Fatal("空运行态键必须失败")
	}
}

func TestW11DWallBudgetRemainingArms(t *testing.T) {
	now := int64(10_000)
	// 无界预算：WithMinimumBudgetMs 原样返回；acceptedAt<=0 时 BudgetMs=MaxInt64。
	unbounded, err := NewGatewayRequestWallBudget(GatewayRequestWallBudgetOptions{Unbounded: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	same, err := unbounded.WithMinimumBudgetMs(1)
	if err != nil || same != unbounded {
		t.Fatal("无界 WithMinimumBudgetMs 必须原样返回")
	}
	lateUnbounded, err := NewGatewayRequestWallBudget(GatewayRequestWallBudgetOptions{
		Unbounded: true, RequestAcceptedAtMs: now, Now: func() int64 { return now },
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if lateUnbounded.BudgetMs != int64(^uint64(0)>>1)-now {
		t.Fatalf("有 acceptedAt 的无界预算 = %d", lateUnbounded.BudgetMs)
	}
	clone := lateUnbounded.WithoutLimit()
	if !clone.Unbounded || clone.BudgetMs != math.MaxInt64-now {
		t.Fatalf("WithoutLimit clone = %+v", clone)
	}

	// HandoffRequired 经由 AvailableDecisionMs 错误（344 臂）。
	bounded, err := NewGatewayRequestWallBudget(GatewayRequestWallBudgetOptions{
		RequestAcceptedAtMs: now, BudgetMs: w11dInt64(5_000), Now: func() int64 { return now },
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bounded.HandoffRequired(GatewayRequestWallBudgetDecision{FinalResponseReserveMs: w11dInt64(-1)}); err == nil {
		t.Fatal("负 reserve 经 handoff 必须失败")
	}
	// PrecommitRemainingMs：precommit deadline 已过 → remaining 0（406 臂）。
	got, err := bounded.PrecommitRemainingMs(PrecommitBudgetInput{
		NowMs:                        w11e(now),
		RequestPrecommitDeadlineAtMs: w11e(now - 5_000),
		FinalResponseReserveMs:       w11e(0),
	})
	if err != nil || got != 0 {
		t.Fatalf("过期 precommit = %d err=%v", got, err)
	}
	// ClipFirstByteDeadlineMs：UncommittedAttemptDeadlineAtMs 已过 → clipped 0（413 臂）。
	clipped, err := bounded.ClipFirstByteDeadlineMs(FirstByteDeadlineClipInput{
		NowMs:                          w11e(now),
		FirstByteDeadlineMs:            3_000,
		FinalResponseReserveMs:         w11e(0),
		UncommittedAttemptDeadlineAtMs: w11e(now - 1),
	})
	if err != nil || clipped != 0 {
		t.Fatalf("过期未提交期限 clip = %d err=%v", clipped, err)
	}
	// first-byte deadline 组合解析错误传播（103 臂）：负 reserve 使 precommit 失败。
	if _, err := ResolveNormalRouteAttemptFirstByteDeadline(NormalRouteAttemptFirstByteDeadlineInput{
		AttemptStartedAtMs:       now,
		GatewayRequestWallBudget: bounded,
		FinalResponseReserveMs:   w11e(-1),
	}); err == nil {
		t.Fatal("预算错误必须传播")
	}
}

// w11dInt64 助手。
func w11dInt64(v int64) *int64 { return &v }

// 避免与现有 w9eInt64 冲突时直接复用。
var _ = w9eInt64

func w11e(v int64) *int64 { return &v }

func TestW11DBindingWeightAndRedisArms(t *testing.T) {
	// 非法权重（0 与 200）在绑定归一化时报错。
	badBinding := GroupBindingRow{
		ID: "b1", APIKeyID: "key1", SystemAccountID: "owner1",
		GroupID: "g1", Priority: 1, Status: RowStatusActive,
		ProviderCode: "gpt", GroupEnabled: 1, Weight: w11e(0),
	}
	goodBinding := badBinding
	goodBinding.ID = "b2"
	goodBinding.GroupID = "g2"
	goodBinding.Weight = w11e(5)
	key := &APIKeyRow{
		ID: "key1", SystemAccountID: "owner1", RouteStrategyID: "rs1",
		RouteStrategyMode: RouteStrategyModeWeighted, Status: RowStatusActive,
		GroupBindings: []GroupBindingRow{badBinding, goodBinding},
	}
	selector := NewAPIKeyGroupRouteSelector("", nil, "")
	if _, err := selector.OrderAPIKeyGroupBindingsForDispatch(key); err == nil {
		t.Fatal("非法权重必须报错")
	}
	// Async 归一化同样报错。
	if _, err := selector.OrderAPIKeyGroupBindingsForDispatchAsync(context.Background(), key); err == nil {
		t.Fatal("Async 非法权重必须报错")
	}
	// 空 GroupID 绑定被归一化跳过。
	emptyGroup := goodBinding
	emptyGroup.ID = "b3"
	emptyGroup.GroupID = ""
	single := &APIKeyRow{
		ID: "key1", SystemAccountID: "owner1",
		RouteStrategyMode: RouteStrategyModeNormal, Status: RowStatusActive,
		GroupBindings: []GroupBindingRow{emptyGroup},
	}
	ordered, err := selector.OrderAPIKeyGroupBindingsForDispatch(single)
	if err != nil || len(ordered) != 1 {
		t.Fatalf("单绑定直通 = %v err=%v", ordered, err)
	}
	// Redis 驱动 + 坏 URL → 轮询计数错误上抛（410 臂）。
	redisSelector := NewAPIKeyGroupRouteSelector("redis", nil, "://bad")
	roundRobin := &APIKeyRow{
		ID: "key1", SystemAccountID: "owner1", RouteStrategyID: "rs2",
		RouteStrategyMode: RouteStrategyModeRoundRobin, Status: RowStatusActive,
		GroupBindings: []GroupBindingRow{goodBinding, badBinding},
	}
	if _, err := redisSelector.OrderAPIKeyGroupBindingsForDispatchAsync(context.Background(), roundRobin); err == nil {
		t.Fatal("坏 Redis URL 必须报错")
	}
}

func TestW11DRequestViewEmptyArms(t *testing.T) {
	// 空 endpoint / path。
	view := RequestView{Method: "GET", OriginalURL: ""}
	if got := view.requestEndpointFamily(); got != "" {
		t.Logf("空视图 family = %q", got)
	}
	if got := openAIRequestEndpointFamily("  "); got != "" {
		t.Fatalf("空 openai family = %q", got)
	}
	if got := anthropicMessagesRequestEndpointFamily("GET", "  "); got != "" {
		t.Fatalf("空 anthropic family = %q", got)
	}
	if got := geminiEndpointFamilyFromPath("  "); got != "" {
		t.Fatalf("空 gemini family = %q", got)
	}
	_ = replaceV1Prefix("")
}
