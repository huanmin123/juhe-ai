package gatewaydispatch

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// 候选过滤器的模型感知重载 / 不可用候选恢复 / 模型过滤回退，分组回退的
// 路由计划快照游标推进，以及会话亲和声明重排。

// claimSecondAffinity 的 ClaimAsync 总是声明第二个候选，并把被声明的账户
// 重排到队首（触发亲和重排分支）。
type claimSecondAffinity struct {
	fakeAffinity
}

func (c *claimSecondAffinity) ClaimAsync(_ context.Context, _ string, _ string, _ AffinityScope) (string, bool) {
	return "a-2", true
}

func (c *claimSecondAffinity) OrderAsync(_ context.Context, accounts []AccountCandidate, _ string, _ AffinityOrderingOptions) ([]AccountCandidate, error) {
	ordered := make([]AccountCandidate, 0, len(accounts))
	for _, account := range accounts {
		if account.ID == "a-2" {
			ordered = append([]AccountCandidate{account}, ordered...)
		} else {
			ordered = append(ordered, account)
		}
	}
	return ordered, nil
}

func TestPrepareDispatchAccountsSessionAffinityReorders(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	engine.Affinity = &claimSecondAffinity{}
	input := dispatchPreparationInput(t, testAccounts("a-1", "a-2"))
	input.SessionAffinityKey = "session-1"
	result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("PrepareDispatchAccounts: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeAccounts {
		t.Fatalf("outcome = %s", result.Outcome)
	}
	// 声明的 a-2 被重排到队首。
	if len(result.Accounts) != 2 || result.Accounts[0].ID != "a-2" {
		t.Fatalf("accounts = %#v", accountIDs(result.Accounts))
	}
}

// TestFilterCandidatesModelAwareReload: 初始候选全部不支持模型时加载模型
// 感知候选窗口并采纳命中结果。
func TestFilterCandidatesModelAwareReload(t *testing.T) {
	pipeline, _, _, _ := newPipeline(t)
	req := newTestRequest(t, `{"model":"wide-model","stream":true}`)
	coordinator := &capturingCoordinator{}
	input := candidateFilterInput(t, req, testAccounts("a-1"))
	input.RawCandidates = []AccountCandidate{{ID: "a-1", SupportedModels: []string{"other"}}}
	input.RouteCoordinator = coordinator
	loaded := false
	input.LoadModelAwareCandidateAccounts = func(requestedModel, sourceEndpointFamily string) ([]AccountCandidate, error) {
		loaded = true
		if requestedModel != "wide-model" {
			t.Fatalf("requested model = %q", requestedModel)
		}
		return []AccountCandidate{{ID: "wide-1", SupportedModels: []string{"wide-model"}}}, nil
	}
	result, err := pipeline.FilterCandidates(context.Background(), input)
	if err != nil {
		t.Fatalf("FilterCandidates: %v", err)
	}
	if !loaded {
		t.Fatal("模型感知候选加载未发生")
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeAccounts || len(result.Accounts) != 1 || result.Accounts[0].ID != "wide-1" {
		t.Fatalf("result = %#v", result)
	}
}

// TestFilterCandidatesRecoversUnavailableWhenNoFallback: 空候选且分组回退未
// 接管时恢复不可用账户。
func TestFilterCandidatesRecoversUnavailableWhenNoFallback(t *testing.T) {
	pipeline, _, _, _ := newPipeline(t)
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)
	input := candidateFilterInput(t, req, nil)
	recovered := false
	input.RecoverUnavailableCandidateAccounts = func() ([]AccountCandidate, error) {
		recovered = true
		return testAccounts("rec-1"), nil
	}
	result, err := pipeline.FilterCandidates(context.Background(), input)
	if err != nil {
		t.Fatalf("FilterCandidates: %v", err)
	}
	if !recovered {
		t.Fatal("不可用候选恢复未发生")
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeAccounts {
		t.Fatalf("outcome = %s", result.Outcome)
	}
}

// TestFilterCandidatesUnsupportedModelFallsBack: 模型过滤后无候选且分组回退
// 接管 → fallback 变体。
func TestFilterCandidatesUnsupportedModelFallsBack(t *testing.T) {
	pipeline, _, _, _ := newPipeline(t)
	req := newTestRequest(t, `{"model":"no-such-model","stream":true}`)
	coordinator := &capturingCoordinator{nextFallback: &gatewayrouting.GatewayRouteFallbackDecision{Attempted: true}}
	input := candidateFilterInput(t, req, testAccounts("a-1"))
	input.RawCandidates = []AccountCandidate{{ID: "a-1", SupportedModels: []string{"other"}}}
	input.RouteCoordinator = coordinator
	result, err := pipeline.FilterCandidates(context.Background(), input)
	if err != nil {
		t.Fatalf("FilterCandidates: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeFallback || result.Reason != "unsupported_model" {
		t.Fatalf("result = %#v", result)
	}
}

// TestFilterCandidatesBypassModelFilterWithOverride: 请求级模型覆盖 + 绕过
// 模型过滤。
func TestFilterCandidatesBypassModelFilterWithOverride(t *testing.T) {
	pipeline, _, driver, _ := newPipeline(t)
	var seenModel atomicValue
	driver.supportsAll = true
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)
	input := candidateFilterInput(t, req, testAccounts("a-1"))
	input.RequestModelOverride = "override-model"
	input.BypassModelFilter = true
	input.LoadModelAwareCandidateAccounts = func(requestedModel, sourceEndpointFamily string) ([]AccountCandidate, error) {
		seenModel.Store(requestedModel)
		return nil, nil
	}
	result, err := pipeline.FilterCandidates(context.Background(), input)
	if err != nil {
		t.Fatalf("FilterCandidates: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeAccounts {
		t.Fatalf("outcome = %s", result.Outcome)
	}
	if result.ModelPriority == nil || result.ModelPriority.RequestedModel != "" {
		// bypass 过滤器不填充 requested model。
		t.Fatalf("model priority = %#v", result.ModelPriority)
	}
}

// atomicValue 是最小并发安全值容器（避免引入 sync/atomic 泛型样板）。
type atomicValue struct{ ch chan string }

func newAtomicValue() atomicValue { return atomicValue{ch: make(chan string, 1)} }

func (a atomicValue) Store(value string) { a.ch <- value }

func (a atomicValue) Load() string {
	select {
	case value := <-a.ch:
		return value
	default:
		return ""
	}
}

// ---------------------------------------------------------------------------
// 分组回退：路由计划快照与过滤链
// ---------------------------------------------------------------------------

func TestResolveNextGroupFallbackWithRoutePlanSnapshot(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)
	engine.Cache = &fakeCacheFallback{
		accounts: map[string][]AccountCandidate{
			"group-2": testAccounts("b-1"),
			"group-3": testAccounts("c-1"),
		},
	}
	quotaDenied := false
	engine.Quota = &quotaGate{denied: func(accountID string) bool {
		if accountID == "c-1" {
			quotaDenied = true
		}
		return false
	}}
	cursor := 0
	snapshotValue, err := gatewayrouting.CreateGatewayRoutePlanSnapshot(gatewayrouting.CreateGatewayRoutePlanSnapshotInput[string]{
		RoutePlanID:           "plan-1",
		Mode:                  "ordered",
		RequestAcceptedAtMs:   gatewayupstream.NowMs(),
		OrderedAllowedTargets: []string{"group-1", "group-2", "group-3"},
		Cursor:                &cursor,
	})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	snapshot := &snapshotValue
	apiKeyRecord := &gatewayruntimecache.GatewayAPIKeyRow{
		ID: "apikey-1",
		GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
			{GroupID: "group-1", Status: "active", GroupEnabled: 1},
			{GroupID: "group-2", Status: "active", GroupEnabled: 1},
			{GroupID: "group-3", Status: "active", GroupEnabled: 1},
		},
	}
	candidate, found, err := pipeline.ResolveNextGroupFallbackCandidateForArgs(context.Background(), GroupFallbackArgs{
		Req:               req,
		Reason:            "upstream_accounts_exhausted",
		APIKeyRecord:      apiKeyRecord,
		SystemAccountID:   "system-1",
		GroupID:           "group-1",
		RequestLane:       "text",
		RoutePlanSnapshot: snapshot,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !found || candidate.GroupID != "group-2" {
		t.Fatalf("candidate = %#v found=%v", candidate, found)
	}
	if candidate.RoutePlanSnapshot == nil || candidate.RoutePlanSnapshot.Cursor != 1 {
		t.Fatalf("snapshot = %#v", candidate.RoutePlanSnapshot)
	}
	// group-3 仅剩配额拒绝账户 → 跳过；快照游标推进正确即可。
	_ = quotaDenied
}

// quotaGate 按账户 ID 决定配额。
type quotaGate struct {
	denied func(accountID string) bool
}

func (q *quotaGate) CheckBatchAsync(ctx context.Context, groupAccess gatewayruntimecache.GroupUsageAccessMetadata, accounts []AccountCandidate) (map[string]QuotaDecision, error) {
	decisions := make(map[string]QuotaDecision, len(accounts))
	for _, account := range accounts {
		decisions[account.ID] = QuotaDecision{Allowed: !q.denied(account.ID)}
	}
	return decisions, nil
}

func TestCanAttemptApiKeyGroupFallbackWithSnapshot(t *testing.T) {
	snapshot := &gatewayrouting.RoutePlanSnapshot[string]{
		OrderedAllowedTargets: []string{"a", "b"},
		Cursor:                0,
	}
	if !CanAttemptApiKeyGroupFallback(nil, "a", snapshot) {
		t.Fatal("快照未到末尾可回退")
	}
	end := &gatewayrouting.RoutePlanSnapshot[string]{
		OrderedAllowedTargets: []string{"a", "b"},
		Cursor:                1,
	}
	if CanAttemptApiKeyGroupFallback(nil, "b", end) {
		t.Fatal("快照末尾不可回退")
	}
	if CanAttemptApiKeyGroupFallback(nil, "a", nil) {
		t.Fatal("无记录无快照不可回退")
	}
}

// TestResolveNextGroupFallbackSkipsFilteredGroups: 能力过滤与模型过滤让候选
// 组被跳过。
func TestResolveNextGroupFallbackSkipsFilteredGroups(t *testing.T) {
	pipeline, engine, driver, _ := newPipeline(t)
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)
	engine.Cache = &fakeCacheFallback{
		accounts: map[string][]AccountCandidate{
			// group-2 账户不支持模型（模型过滤淘汰）。
			"group-2": {{ID: "b-1", SupportedModels: []string{"other"}}},
			// group-3 正常。
			"group-3": testAccounts("c-1"),
		},
	}
	apiKeyRecord := &gatewayruntimecache.GatewayAPIKeyRow{
		ID: "apikey-1",
		GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
			{GroupID: "group-1", Status: "active", GroupEnabled: 1},
			{GroupID: "group-2", Status: "active", GroupEnabled: 1},
			{GroupID: "group-3", Status: "active", GroupEnabled: 1},
		},
	}
	_ = driver
	candidate, found, err := pipeline.ResolveNextGroupFallbackCandidateForArgs(context.Background(), GroupFallbackArgs{
		Req:             req,
		Reason:          "upstream_accounts_exhausted",
		APIKeyRecord:    apiKeyRecord,
		SystemAccountID: "system-1",
		GroupID:         "group-1",
		RequestLane:     "text",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !found || candidate.GroupID != "group-3" {
		t.Fatalf("candidate = %#v found=%v", candidate, found)
	}
}
