package gatewaypreauth

// 合并路由（merge 模式）预检语义单测（合并路由设计 3.4/B18/B20）：
//   - RoutePlanSnapshot 单目标（无分组回退）；
//   - candidateLoader / recoverableLoader 按绑定扇出重建扁平池（去重 + 组标），
//     防止单组重读把池收窄回窗口组；
//   - 交互资源亲和全池过滤：亲和账号来自任一绑定组均可服务，命中后窗口身份
//     取账号所在片段组。

import (
	"context"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaygemini"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// mergeRuntimeRow 构造 merge 策略 + 两个启用绑定（priority 序）的 API Key 行。
func mergeRuntimeRow() *gatewayruntimecache.GatewayAPIKeyRow {
	return &gatewayruntimecache.GatewayAPIKeyRow{
		ID:                "key_1",
		SystemAccountID:   "sys_1",
		RouteStrategyMode: gatewayruntimecache.RouteStrategyModeMerge,
		SelectedGroupID:   "group_1",
		Status:            "active",
		GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
			{ID: "b1", APIKeyID: "key_1", GroupID: "group_1", Status: "active", GroupEnabled: 1, Priority: 1},
			{ID: "b2", APIKeyID: "key_1", GroupID: "group_2", Status: "active", GroupEnabled: 1, Priority: 2},
		},
	}
}

// mergeRuntimeCache 提供按分组的 Cached/Fresh/Recoverable 列出与组访问解析。
type mergeRuntimeCache struct {
	*fakeRuntimeCache
	cachedByGroup      map[string][]gatewayruntimecache.OpenAIAccountSecret
	freshByGroup       map[string][]gatewayruntimecache.OpenAIAccountSecret
	recoverableByGroup map[string][]gatewayruntimecache.OpenAIAccountSecret
	listedFreshGroups  []string
	listedCachedGroups []string
}

func (c *mergeRuntimeCache) listByGroup(source map[string][]gatewayruntimecache.OpenAIAccountSecret, groupID string) ([]gatewayruntimecache.OpenAIAccountSecret, error) {
	accounts, ok := source[groupID]
	if !ok {
		return []gatewayruntimecache.OpenAIAccountSecret{}, nil
	}
	return accounts, nil
}

func (c *mergeRuntimeCache) ListCachedOpenAIAccountsForGroupAsync(_ context.Context, groupID, _ string, _ gatewayruntimecache.CachedOpenAIAccountsForGroupOptions) ([]gatewayruntimecache.OpenAIAccountSecret, error) {
	c.listedCachedGroups = append(c.listedCachedGroups, groupID)
	return c.listByGroup(c.cachedByGroup, groupID)
}

func (c *mergeRuntimeCache) ListFreshOpenAIAccountsForGroupAsync(_ context.Context, groupID, _ string, _ gatewayruntimecache.CachedOpenAIAccountsForGroupOptions) ([]gatewayruntimecache.OpenAIAccountSecret, error) {
	c.listedFreshGroups = append(c.listedFreshGroups, groupID)
	return c.listByGroup(c.freshByGroup, groupID)
}

func (c *mergeRuntimeCache) ListRecoverableUnavailableOpenAIAccountsForGroupAsync(_ context.Context, groupID, _ string, _ gatewayruntimecache.CachedOpenAIAccountsForGroupOptions, _ *int64) ([]gatewayruntimecache.OpenAIAccountSecret, error) {
	return c.listByGroup(c.recoverableByGroup, groupID)
}

// B18：merge 快照单目标 [窗口组]（cursor=0）→ canAttemptAPIKeyGroupFallback
// 快照分支恒假；对照 normal 记录仍产出全部启用绑定目标（存量行为不变）。
func TestMergeRoutePlanSnapshotSingleTarget(t *testing.T) {
	service, _, _ := newTestService(t, nil)
	snapshot, err := service.createOpenAIGatewayRoutePlanSnapshot(routePlanInput{
		traceID: "trace", startedAt: 1, groupId: "group_1", apiKeyRecord: mergeRuntimeRow(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Mode != gatewayruntimecache.RouteStrategyModeMerge {
		t.Fatalf("mode = %s, want merge", snapshot.Mode)
	}
	if len(snapshot.OrderedAllowedTargets) != 1 || snapshot.OrderedAllowedTargets[0] != "group_1" {
		t.Fatalf("targets = %v, want [group_1]", snapshot.OrderedAllowedTargets)
	}
	if snapshot.Cursor != 0 {
		t.Fatalf("cursor = %d, want 0", snapshot.Cursor)
	}
	if canAttemptAPIKeyGroupFallback(mergeRuntimeRow(), "group_1", &snapshot) {
		t.Fatal("merge 快照单目标下不得尝试分组回退")
	}
	normalRecord := mergeRuntimeRow()
	normalRecord.RouteStrategyMode = gatewayruntimecache.RouteStrategyModeNormal
	normalSnapshot, err := service.createOpenAIGatewayRoutePlanSnapshot(routePlanInput{
		traceID: "trace", startedAt: 1, groupId: "group_1", apiKeyRecord: normalRecord,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(normalSnapshot.OrderedAllowedTargets) != 2 {
		t.Fatalf("normal targets = %v, want both bindings", normalSnapshot.OrderedAllowedTargets)
	}
}

// 3.4：candidateLoader 按绑定扇出重建扁平池——跨组重复账号按绑定优先级去重
// （首次出现组胜出），重建后全量重新组标；对照非 merge 记录保持单组读取且
// 不组标（存量语义不变）。
func TestMergeCandidateLoaderFansOutAcrossBindings(t *testing.T) {
	shared := gatewayruntimecache.OpenAIAccountSecret{ID: "acc_shared", ProviderCode: "openai"}
	g1Only := gatewayruntimecache.OpenAIAccountSecret{ID: "acc_a1", ProviderCode: "openai"}
	g2Only := gatewayruntimecache.OpenAIAccountSecret{ID: "acc_b1", ProviderCode: "openai"}
	cache := &mergeRuntimeCache{
		fakeRuntimeCache: whRuntimeCacheFor(nil),
		cachedByGroup: map[string][]gatewayruntimecache.OpenAIAccountSecret{
			"group_1": {shared, g1Only},
			"group_2": {shared, g2Only},
		},
	}
	service, _, _ := newTestService(t, func(s *Service) { s.RuntimeCache = cache })
	loader := candidateLoader(service, &PreflightOptions{}, nil, mergeRuntimeRow(), "group_1", "sys_1")
	if loader == nil {
		t.Fatal("merge 必须构造候选加载闭包")
	}
	pool, err := loader("m1", "chat_completions")
	if err != nil {
		t.Fatal(err)
	}
	if len(pool) != 3 {
		t.Fatalf("pool = %v, want 3 deduped accounts", accountIDList(pool))
	}
	if pool[0].ID != "acc_shared" || pool[1].ID != "acc_a1" || pool[2].ID != "acc_b1" {
		t.Fatalf("pool order = %v, want binding-then-group order", accountIDList(pool))
	}
	stamps := map[string]string{"acc_shared": "group_1", "acc_a1": "group_1", "acc_b1": "group_2"}
	for _, account := range pool {
		if account.BoundGroupID == nil || *account.BoundGroupID != stamps[account.ID] {
			t.Fatalf("account %s stamp = %v, want %s", account.ID, account.BoundGroupID, stamps[account.ID])
		}
	}
	// 扇出确实逐组列出。
	if len(cache.listedCachedGroups) != 2 || cache.listedCachedGroups[0] != "group_1" || cache.listedCachedGroups[1] != "group_2" {
		t.Fatalf("listed groups = %v", cache.listedCachedGroups)
	}
	// 对照：normal 记录按窗口组单组重读、不组标（收窄即存量语义）。
	normalRecord := mergeRuntimeRow()
	normalRecord.RouteStrategyMode = gatewayruntimecache.RouteStrategyModeNormal
	normalLoader := candidateLoader(service, &PreflightOptions{}, nil, normalRecord, "group_1", "sys_1")
	normalPool, err := normalLoader("m1", "chat_completions")
	if err != nil {
		t.Fatal(err)
	}
	if len(normalPool) != 2 {
		t.Fatalf("normal pool = %v, want single group listing", accountIDList(normalPool))
	}
	for _, account := range normalPool {
		if account.BoundGroupID != nil {
			t.Fatalf("normal account %s must not be stamped, got %s", account.ID, *account.BoundGroupID)
		}
	}
}

// 3.4：recoverableLoader 按绑定扇出重建扁平池；等待 scopeKey 保持窗口组。
func TestMergeRecoverableLoaderFansOutAcrossBindings(t *testing.T) {
	g1Only := gatewayruntimecache.OpenAIAccountSecret{ID: "acc_a1", ProviderCode: "openai"}
	g2Only := gatewayruntimecache.OpenAIAccountSecret{ID: "acc_b1", ProviderCode: "openai"}
	buildInput := func() recoveryInput {
		return recoveryInput{
			req: whNewRequest("POST", "/v1/chat/completions"), auditCapture: &fakeAuditCapture{},
			systemAccountID: "sys_1", apiKeyID: "key_1", groupID: "group_1",
			serverRetryBudget: NewServerRetryBudget(5_000, newFakeClock(1_000)),
		}
	}
	// 1. 活跃账户扇出：两组账号合并 + 组标。
	cache := &mergeRuntimeCache{
		fakeRuntimeCache: whRuntimeCacheFor(nil),
		freshByGroup: map[string][]gatewayruntimecache.OpenAIAccountSecret{
			"group_1": {g1Only},
			"group_2": {g2Only},
		},
	}
	service, _, _ := newTestService(t, func(s *Service) { s.RuntimeCache = cache })
	loader := recoverableLoader(service, &PreflightOptions{}, nil, mergeRuntimeRow(), buildInput())
	if loader == nil {
		t.Fatal("merge 必须构造可恢复加载闭包")
	}
	pool, err := loader()
	if err != nil {
		t.Fatal(err)
	}
	if len(pool) != 2 || pool[0].ID != "acc_a1" || pool[1].ID != "acc_b1" {
		t.Fatalf("pool = %v, want [acc_a1 acc_b1]", accountIDList(pool))
	}
	if pool[0].BoundGroupID == nil || *pool[0].BoundGroupID != "group_1" ||
		pool[1].BoundGroupID == nil || *pool[1].BoundGroupID != "group_2" {
		t.Fatalf("stamps = %v/%v, want group_1/group_2", pool[0].BoundGroupID, pool[1].BoundGroupID)
	}
	if strings.Join(cache.listedFreshGroups, ",") != "group_1,group_2" {
		t.Fatalf("fresh reads = %v", cache.listedFreshGroups)
	}
	// 2. 全池空 + 可恢复在次组：等待 scopeKey 取窗口组（group_1）。
	emptyCache := &mergeRuntimeCache{
		fakeRuntimeCache: whRuntimeCacheFor(nil),
		recoverableByGroup: map[string][]gatewayruntimecache.OpenAIAccountSecret{
			"group_2": {g2Only},
		},
	}
	service2, _, _ := newTestService(t, func(s *Service) { s.RuntimeCache = emptyCache })
	recoverable := &fakeRecoverable{}
	service2.Recoverable = recoverable
	loader2 := recoverableLoader(service2, &PreflightOptions{}, nil, mergeRuntimeRow(), buildInput())
	accounts, err := loader2()
	if err != nil || len(accounts) != 0 {
		t.Fatalf("等待后快照 = %v err=%v", accounts, err)
	}
	if len(recoverable.waited) != 1 || !strings.Contains(recoverable.waited[0], "sys_1:key_1:group_1") {
		t.Fatalf("等待范围 = %v, want window group scope key", recoverable.waited)
	}
}

// B20：交互亲和命中跨组——亲和记录组（group_1）的列表不再含被钉账号，但该
// 账号在另一启用绑定组（group_2）中；merge 下过滤在合并池进行，命中后窗口
// 身份取账号所在片段组。
func TestMergeInteractionAffinityHitsAcrossGroups(t *testing.T) {
	accAff := gatewayruntimecache.OpenAIAccountSecret{ID: "acc_aff", ProviderCode: "gemini"}
	accOther := gatewayruntimecache.OpenAIAccountSecret{ID: "acc_other", ProviderCode: "gemini"}
	cache := &mergeRuntimeCache{
		fakeRuntimeCache: whRuntimeCacheFor(nil),
		cachedByGroup: map[string][]gatewayruntimecache.OpenAIAccountSecret{
			"group_1": {accOther},
			"group_2": {accAff},
		},
	}
	affinity := gatewaygemini.NewInteractionAffinity(nil)
	if _, err := affinity.Remember(context.Background(), "ia-merge",
		gatewaygemini.UpstreamAccount{ID: "acc_aff", ProviderCode: "gemini"},
		gatewaygemini.AffinityScope{SystemAccountID: "sys_1", APIKeyID: "key_1", GroupID: "group_1"},
	); err != nil {
		t.Fatal(err)
	}
	service, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = cache
		s.Candidates = whFullDispatchCandidates(accAff)
		s.Codex = &fakeCodex{compactResult: CodexCompactPreflightResult{Accounts: []gatewayruntimecache.OpenAIAccountSecret{accAff}}}
		s.Affinity = affinity
	})
	options := plainPreflightOptions()
	options.APIKeyRecord = mergeRuntimeRow()
	options.Identity.GroupID = "group_1"
	req, _, writer := newTestRequest("GET", "/v1beta/interactions/ia-merge")
	result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req, Res: writer, AuditCapture: &fakeAuditCapture{},
		Options:   options,
		StartedAt: 1, TraceID: "trace", Endpoint: "GET /v1beta/interactions/ia-merge",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.DispatchContext == nil {
		t.Fatalf("merge 亲和跨组命中必须继续派发: %+v", result)
	}
	if result.DispatchContext.InteractionResourceAffinity == nil {
		t.Fatal("亲和绑定必须进入派发上下文")
	}
	if len(result.DispatchContext.Accounts) != 1 || result.DispatchContext.Accounts[0].ID != "acc_aff" {
		t.Fatalf("accounts = %v, want [acc_aff]", result.DispatchContext.Accounts)
	}
	// 窗口身份取账号所在片段组（请求级 UsageContext 与账号组一致）。
	if result.DispatchContext.UsageContext.GroupID != "group_2" {
		t.Fatalf("window usage group = %s, want group_2", result.DispatchContext.UsageContext.GroupID)
	}
}

func accountIDList(accounts []gatewayruntimecache.OpenAIAccountSecret) []string {
	ids := make([]string, 0, len(accounts))
	for _, account := range accounts {
		ids = append(ids, account.ID)
	}
	return ids
}
