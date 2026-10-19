package gatewaypreauth

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// 组级会话亲和三阶段 preflight 接线（策略路由分组会话亲和设计 §4/§8 单元
// 测试矩阵）：静态身份快照在身份解析后经 SelectGatewayRuntimeForDispatchAsync
// 完成第 3 步；身份解析前零排序器/零组亲和 GET；黏住命中/未命中的状态语义；
// ≤2 次组亲和 GET 边界（同键 profile 重解析零补查、异键补查一次）；重入
// preflight（Identity 注入 / interaction 亲和）不进补全块。

// groupStickyRuntimeCache 是静态三阶段 fake：static read 返回带标记的 rr 双
// 绑定运行时，select 记录入参并按 PreferredGroupID 重写派发终态。
type groupStickyRuntimeCache struct {
	fakeRuntimeCache
	mu           sync.Mutex
	staticRow    gatewayruntimecache.GatewayRuntime
	staticCalls  int
	selectInputs []gatewayruntimecache.SelectGatewayRuntimeInput
}

func (c *groupStickyRuntimeCache) ReadCachedGatewayRuntimeStaticAsync(_ context.Context, _ string) (gatewayruntimecache.GatewayRuntime, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.staticCalls++
	row := c.staticRow
	return row, nil
}

func (c *groupStickyRuntimeCache) staticCallCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.staticCalls
}

func (c *groupStickyRuntimeCache) recordedSelects() []gatewayruntimecache.SelectGatewayRuntimeInput {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]gatewayruntimecache.SelectGatewayRuntimeInput(nil), c.selectInputs...)
}

func (c *groupStickyRuntimeCache) SelectGatewayRuntimeForDispatchAsync(_ context.Context, static gatewayruntimecache.GatewayRuntime, input gatewayruntimecache.SelectGatewayRuntimeInput) (gatewayruntimecache.GatewayRuntime, error) {
	c.mu.Lock()
	c.selectInputs = append(c.selectInputs, input)
	c.mu.Unlock()
	selected := static
	apiKey := *static.APIKey
	// walk 语义投影（镜像真实 selectGatewayRuntimeFromOrderedBindings：选中
	// 行携带派发序绑定）：无黏住/黏住组不在绑定时保持存储序首位（grp_a），
	// 命中时落黏住组（grp_b 在位）并把黏住组重排到绑定序首位。
	if input.PreferredGroupID != "" {
		apiKey.SelectedGroupID = input.PreferredGroupID
		reordered := make([]gatewayruntimecache.GatewayAPIKeyGroupBindingRow, 0, len(apiKey.GroupBindings))
		for _, binding := range apiKey.GroupBindings {
			if binding.GroupID == input.PreferredGroupID {
				reordered = append(reordered, binding)
			}
		}
		for _, binding := range apiKey.GroupBindings {
			if binding.GroupID != input.PreferredGroupID {
				reordered = append(reordered, binding)
			}
		}
		apiKey.GroupBindings = reordered
	}
	selected.APIKey = &apiKey
	selected.StaticResolutionOnly = false
	return selected, nil
}

// fakeGroupAffinityPort 记录键派生与黏住查询次数（≤2 GET 边界断言）。
type fakeGroupAffinityPort struct {
	mu          sync.Mutex
	bindings    map[string]string
	deriveConvs []string
	stickyKeys  []string
}

func (f *fakeGroupAffinityPort) DeriveGroupAffinityKeyParts(_ string, _ string, _ string, conversationKey string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deriveConvs = append(f.deriveConvs, conversationKey)
	if conversationKey == "" {
		return "", nil
	}
	return "gak:" + conversationKey, nil
}

func (f *fakeGroupAffinityPort) StickyGroupAsync(_ context.Context, groupAffinityKey string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stickyKeys = append(f.stickyKeys, groupAffinityKey)
	groupID, ok := f.bindings[groupAffinityKey]
	return groupID, ok
}

func (f *fakeGroupAffinityPort) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.deriveConvs), len(f.stickyKeys)
}

// fixedSessionIdentity 返回固定会话身份（可空会话键）。
type fixedSessionIdentity struct{ identity SessionIdentity }

func (f *fixedSessionIdentity) ResolveGatewaySessionIdentity(*GatewayRequest, SessionIdentityInput) SessionIdentity {
	return f.identity
}

// profileKeySessionIdentity 让会话键跟随最终 client profile 变化（§4 关键点 5
// 场景注入）。
type profileKeySessionIdentity struct{}

func (profileKeySessionIdentity) ResolveGatewaySessionIdentity(_ *GatewayRequest, input SessionIdentityInput) SessionIdentity {
	return SessionIdentity{SessionID: "session_" + input.ClientProfile, ConversationKey: "conv_" + input.ClientProfile}
}

// profileSwitchingStrategy 模拟最终 profile 重解析（§4 关键点 5）：供应商解
// 耦后 ClientStrategyInput 不再携带 provider 维度（设计 §8），初始/最终两次
// 解析入参同形——profile 精化改以解析次数模拟（首次 generic，二次 codex，
// 对应 body 检查完成后 clientCompatibility 到位的既有语义）。
type profileSwitchingStrategy struct {
	calls int64
}

func (s *profileSwitchingStrategy) Resolve(_ *GatewayRequest, _ ClientStrategyInput) ClientStrategyContext {
	s.calls++
	if s.calls == 1 {
		return ClientStrategyContext{ClientProfile: "generic"}
	}
	return ClientStrategyContext{ClientProfile: "codex"}
}
func (profileSwitchingStrategy) AuditMetadata(ClientStrategyContext) map[string]any { return nil }

func groupStickyStaticRow() gatewayruntimecache.GatewayRuntime {
	row := validRuntimeRow()
	row.RouteStrategyMode = gatewayruntimecache.RouteStrategyModeRoundRobin
	row.RouteStrategyID = "strategy_rr_1"
	row.SelectedGroupID = "grp_a"
	row.GroupBindings = []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
		{ID: "binding_a", APIKeyID: row.ID, GroupID: "grp_a", Status: "active", GroupEnabled: 1},
		{ID: "binding_b", APIKeyID: row.ID, GroupID: "grp_b", Status: "active", GroupEnabled: 1},
	}
	account := gatewayruntimecache.OpenAIAccountSecret{ID: "acc_sticky", ProviderCode: "openai", Status: "active"}
	return gatewayruntimecache.GatewayRuntime{
		APIKey:               row,
		Settings:             gatewayruntimecache.GatewaySettings{NoAvailableAccountWaitTimeoutSeconds: 30},
		GroupAccess:          &gatewayruntimecache.GroupUsageAccessMetadata{GroupAccessType: "owner"},
		Accounts:             []gatewayruntimecache.OpenAIAccountSecret{account},
		StaticResolutionOnly: true,
	}
}

func groupStickyAffinityTestService(t *testing.T, affinity *fakeGroupAffinityPort, identity SessionIdentityResolver) (*Service, *groupStickyRuntimeCache) {
	t.Helper()
	staticRow := groupStickyStaticRow()
	cache := &groupStickyRuntimeCache{
		fakeRuntimeCache: fakeRuntimeCache{
			runtimeByKey: map[string]gatewayruntimecache.GatewayRuntime{"sk-good": staticRow},
			groupAccess:  &gatewayruntimecache.GroupUsageAccessMetadata{},
			accounts:     staticRow.Accounts,
		},
		staticRow: staticRow,
	}
	account := staticRow.Accounts[0]
	service, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = cache
		s.GroupAffinity = affinity
		s.SessionIdentity = identity
		s.Candidates = whSuccessfulCandidates(account)
		s.Codex = &fakeCodex{compactResult: CodexCompactPreflightResult{Accounts: []gatewayruntimecache.OpenAIAccountSecret{account}}}
	})
	return service, cache
}

func groupStickyPreflight(t *testing.T, service *Service, options *PreflightOptions) (*DispatchContext, *httptest.ResponseRecorder) {
	t.Helper()
	audit := &fakeAuditCapture{}
	req, recorder, writer := whAuthorizedChatRequest()
	result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req, Res: writer, AuditCapture: audit, Options: options,
		StartedAt: 1_700_000_000_000, TraceID: "trace_1",
		Endpoint:        "POST /v1/chat/completions",
		RequestSnapshot: UsageRequestSnapshot{Method: "POST", Path: "/v1/chat/completions"},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.DispatchContext == nil {
		t.Fatalf("应返回 DispatchContext: %+v", result)
	}
	return result.DispatchContext, recorder
}

func TestGroupStickyPreflightCompletesSelectionOnStickyHit(t *testing.T) {
	affinity := &fakeGroupAffinityPort{bindings: map[string]string{"gak:conv_generic": "grp_b"}}
	service, cache := groupStickyAffinityTestService(t, affinity, &fixedSessionIdentity{identity: SessionIdentity{SessionID: "session_1", ConversationKey: "conv_generic"}})

	context, recorder := groupStickyPreflight(t, service, &PreflightOptions{DispatchStaticResolution: true})
	if recorder.Code >= 400 {
		t.Fatalf("成功路径不得写失败响应: %d", recorder.Code)
	}
	selects := cache.recordedSelects()
	if len(selects) != 1 {
		t.Fatalf("select 调用 = %d, want 1（三阶段第 3 步恰好一次）", len(selects))
	}
	if selects[0].PreferredGroupID != "grp_b" || !selects[0].AdvanceOnStickyMiss {
		t.Fatalf("select 入参 = %+v, want {grp_b, advance=true（miss 才推进；hit 不消费）}", selects[0])
	}
	if context.UsageContext.GroupID != "grp_b" {
		t.Fatalf("UsageContext.GroupID = %q, want grp_b（黏住命中覆盖静态首组）", context.UsageContext.GroupID)
	}
	if context.GroupAffinityKey != "gak:conv_generic" {
		t.Fatalf("GroupAffinityKey = %q, want gak:conv_generic", context.GroupAffinityKey)
	}
	if context.RouteStrategyID != "strategy_rr_1" {
		t.Fatalf("RouteStrategyID = %q, want strategy_rr_1", context.RouteStrategyID)
	}
	if context.UsageContext.GroupID != "grp_b" {
		t.Fatalf("UsageContext.GroupID = %q, want grp_b（身份以最终落组为准）", context.UsageContext.GroupID)
	}
	// P1-1（复审）回归防线：组回退快照必须锚定黏住重排后的派发序——选中组
	// cursor=0，同请求组回退仍可回退到其余组（旧完整读行为）。
	snapshot := context.RoutePlanSnapshot
	if len(snapshot.OrderedAllowedTargets) != 2 {
		t.Fatalf("快照目标数 = %d, want 2", len(snapshot.OrderedAllowedTargets))
	}
	if snapshot.OrderedAllowedTargets[0] != "grp_b" || snapshot.OrderedAllowedTargets[1] != "grp_a" {
		t.Fatalf("快照绑定序 = %v, want [grp_b grp_a]（黏住组置首）", snapshot.OrderedAllowedTargets)
	}
	if snapshot.Cursor != 0 {
		t.Fatalf("快照 cursor = %d, want 0（选中组可回退）", snapshot.Cursor)
	}
}

func TestGroupStickyPreflightMissDerivesKeyAndAdvances(t *testing.T) {
	affinity := &fakeGroupAffinityPort{bindings: map[string]string{}}
	service, cache := groupStickyAffinityTestService(t, affinity, &fixedSessionIdentity{identity: SessionIdentity{SessionID: "session_1", ConversationKey: "conv_generic"}})

	context, _ := groupStickyPreflight(t, service, &PreflightOptions{DispatchStaticResolution: true})
	selects := cache.recordedSelects()
	if len(selects) != 1 {
		t.Fatalf("select 调用 = %d, want 1", len(selects))
	}
	if selects[0].PreferredGroupID != "" || !selects[0].AdvanceOnStickyMiss {
		t.Fatalf("miss 入参 = %+v, want 空黏住 + 推进一次", selects[0])
	}
	// 未命中键同样带出：首个协议成功负责创建绑定（§5 写入即建即更）。
	if context.GroupAffinityKey != "gak:conv_generic" {
		t.Fatalf("miss 后 GroupAffinityKey = %q, want gak:conv_generic", context.GroupAffinityKey)
	}
	if context.UsageContext.GroupID != "grp_a" {
		t.Fatalf("miss 后落组 = %q, want grp_a（存储序首位）", context.UsageContext.GroupID)
	}
}

func TestGroupStickyPreflightNoConversationKeyIsZeroOverhead(t *testing.T) {
	affinity := &fakeGroupAffinityPort{bindings: map[string]string{"gak:conv_generic": "grp_b"}}
	service, cache := groupStickyAffinityTestService(t, affinity, &fixedSessionIdentity{identity: SessionIdentity{SessionID: "session_1"}})

	context, _ := groupStickyPreflight(t, service, &PreflightOptions{DispatchStaticResolution: true})
	selects := cache.recordedSelects()
	if len(selects) != 1 {
		t.Fatalf("补全仍须完成（无黏住推进一次），select = %d", len(selects))
	}
	if selects[0].PreferredGroupID != "" || !selects[0].AdvanceOnStickyMiss {
		t.Fatalf("无会话键入参 = %+v, want 空黏住 + 推进一次", selects[0])
	}
	derives, stickies := affinity.counts()
	if derives != 0 || stickies != 0 {
		t.Fatalf("无会话键零额外开销路径：derive=%d sticky=%d, want 0/0", derives, stickies)
	}
	if context.GroupAffinityKey != "" {
		t.Fatalf("无会话键不得携带组亲和键：%q", context.GroupAffinityKey)
	}
}

func TestGroupStickyPreflightIdentityInjectionSkipsCompletion(t *testing.T) {
	affinity := &fakeGroupAffinityPort{bindings: map[string]string{"gak:conv_generic": "grp_b"}}
	service, cache := groupStickyAffinityTestService(t, affinity, &fixedSessionIdentity{identity: SessionIdentity{SessionID: "session_1", ConversationKey: "conv_generic"}})

	// 跨组回退重入（Identity 注入）：不进补全块——回退只消费既有候选快照，
	// 不推进模式状态、不重查组亲和（§4 关键点 2）。
	options := plainPreflightOptions()
	options.DispatchStaticResolution = true
	options.GroupAffinityKey = "gak:conv_generic"
	context, _ := groupStickyPreflight(t, service, options)
	if selects := cache.recordedSelects(); len(selects) != 0 {
		t.Fatalf("Identity 注入不得触发第 3 步：select = %d", len(selects))
	}
	if context.GroupAffinityKey != "gak:conv_generic" {
		t.Fatalf("种子键必须透传：%q", context.GroupAffinityKey)
	}
	if derives, stickies := affinity.counts(); derives != 0 || stickies != 0 {
		t.Fatalf("回退重入不得重查组亲和：derive=%d sticky=%d", derives, stickies)
	}
}

func TestGroupStickyPreflightSameKeyAcrossProfileChangeIsSingleGET(t *testing.T) {
	// ≤2 GET 边界行（§4 关键点 5）：profile 重解析但会话键不变——多数请求
	// 形态，初始查询已代表最终身份，补查跳过（全请求 1 次 GET）。
	affinity := &fakeGroupAffinityPort{bindings: map[string]string{"gak:conv_generic": "grp_b"}}
	service, cache := groupStickyAffinityTestService(t, affinity, &fixedSessionIdentity{identity: SessionIdentity{SessionID: "session_1", ConversationKey: "conv_generic"}})
	service.ClientStrategy = &profileSwitchingStrategy{}

	context, _ := groupStickyPreflight(t, service, &PreflightOptions{DispatchStaticResolution: true})
	selects := cache.recordedSelects()
	if len(selects) != 1 {
		t.Fatalf("同键补查应跳过：select = %d, want 1", len(selects))
	}
	// derive 是本地 HMAC 不计 GET；组绑定读（GET）只发生 1 次——同键补查跳过。
	derives, stickies := affinity.counts()
	if derives != 2 || stickies != 1 {
		t.Fatalf("同键 profile 重解析：derive=%d sticky(GET)=%d, want 2/1（GET ≤2 且未重复）", derives, stickies)
	}
	if context.UsageContext.GroupID != "grp_b" {
		t.Fatalf("落组 = %q, want grp_b", context.UsageContext.GroupID)
	}
}

func TestGroupStickyPreflightProfileChangeWithNewKeyRequeries(t *testing.T) {
	// profile 重解析且会话键变化：初始 lookup（grp_a 命中，零推进）→ 最终
	// 身份补查一次（grp_b 命中，重选不重复推进），全请求共 2 次 GET。
	affinity := &fakeGroupAffinityPort{bindings: map[string]string{
		"gak:conv_generic": "grp_a",
		"gak:conv_codex":   "grp_b",
	}}
	service, cache := groupStickyAffinityTestService(t, affinity, profileKeySessionIdentity{})
	service.ClientStrategy = &profileSwitchingStrategy{}

	context, _ := groupStickyPreflight(t, service, &PreflightOptions{DispatchStaticResolution: true})
	selects := cache.recordedSelects()
	if len(selects) != 2 {
		t.Fatalf("select 调用 = %d, want 2（初始选择 + 最终身份重选）", len(selects))
	}
	if selects[0].PreferredGroupID != "grp_a" || !selects[0].AdvanceOnStickyMiss {
		t.Fatalf("初始入参 = %+v, want {grp_a, advance=true}", selects[0])
	}
	if selects[1].PreferredGroupID != "grp_b" {
		t.Fatalf("重选入参 = %+v, want grp_b（最终身份命中）", selects[1])
	}
	derives, stickies := affinity.counts()
	if derives != 2 || stickies != 2 {
		t.Fatalf("异键 profile 重解析 GET 次数 derive=%d sticky=%d, want 2/2（≤2 边界）", derives, stickies)
	}
	if context.UsageContext.GroupID != "grp_b" || context.GroupAffinityKey != "gak:conv_codex" {
		t.Fatalf("最终落组/键 = %q/%q, want grp_b/gak:conv_codex", context.UsageContext.GroupID, context.GroupAffinityKey)
	}
}

func TestGroupStickyPreflightMissThenHitDoesNotAdvanceTwice(t *testing.T) {
	// 初始 miss（推进一次）→ 最终身份命中：重选保持既有派发序不二次推进
	//（§4 关键点 5 推进预算由初始阶段承担）。
	affinity := &fakeGroupAffinityPort{bindings: map[string]string{"gak:conv_codex": "grp_b"}}
	service, cache := groupStickyAffinityTestService(t, affinity, profileKeySessionIdentity{})
	service.ClientStrategy = &profileSwitchingStrategy{}

	context, _ := groupStickyPreflight(t, service, &PreflightOptions{DispatchStaticResolution: true})
	selects := cache.recordedSelects()
	if len(selects) != 2 {
		t.Fatalf("select 调用 = %d, want 2", len(selects))
	}
	if selects[0].PreferredGroupID != "" || !selects[0].AdvanceOnStickyMiss {
		t.Fatalf("初始入参 = %+v, want 空黏住 + 推进一次", selects[0])
	}
	if selects[1].PreferredGroupID != "grp_b" || selects[1].AdvanceOnStickyMiss {
		t.Fatalf("重选入参 = %+v, want {grp_b, advance=false（不二次推进）}", selects[1])
	}
	if context.UsageContext.GroupID != "grp_b" {
		t.Fatalf("最终落组 = %q, want grp_b", context.UsageContext.GroupID)
	}
}
