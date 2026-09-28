package gatewaypreauth

// 可靠性批次2测试：
//  3. chat 覆盖候选（provided 且无 Identity）跳过 Key 策略面的 normal 路由
//     判定，不再按 Key 绑定面 400；外部 /v1（无注入候选）语义不变。
//  5. provided 候选接入可恢复等待：按注入的绑定作用域组重读发现恢复，
//     不按 Key 策略窗口组重读（不逃逸绑定作用域）。

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// batch2CountingRouteResolver 统计调用的 normal 路由 fake。
type batch2CountingRouteResolver struct {
	normal NormalRouteResult
	calls  int32
}

func (f *batch2CountingRouteResolver) ResolveNormalGatewayModelRoute(context.Context, NormalRouteInput) (NormalRouteResult, error) {
	atomic.AddInt32(&f.calls, 1)
	return f.normal, nil
}

// TestPreflightSkipsNormalRouteForChatProvidedCandidatesBatch2 覆盖缺陷3：
// chat 调度覆盖注入候选（CandidateAccounts 非 nil、无 Identity）时跳过 normal
// 路由判定；同一配置去掉注入候选则恢复既有 404 失败路径（对照组）。
func TestPreflightSkipsNormalRouteForChatProvidedCandidatesBatch2(t *testing.T) {
	run := func(withProvided bool) (*batch2CountingRouteResolver, *fakeResponseSink) {
		resolver := &batch2CountingRouteResolver{normal: NormalRouteResult{
			Outcome: NormalRouteOutcomeFailed, StatusCode: 404, Type: "invalid_request_error",
			Code: "model_not_routable_for_api_key", Message: "当前 API Key 无法路由该模型",
			RequestedModel: "gpt-x", MatchedProviderCodes: []string{"openai"},
		}}
		service, _, sink := newTestService(t, func(s *Service) {
			s.RouteResolver = resolver
			s.RuntimeCache = &fakeRuntimeCache{
				runtimeByKey: map[string]gatewayruntimecache.GatewayRuntime{"sk-good": validRuntime()},
				groupAccess:  &gatewayruntimecache.GroupUsageAccessMetadata{ProviderCode: "openai"},
			}
			s.Candidates = &fakeCandidates{filterResult: CandidateFilterResult{Outcome: CandidateOutcomeCompleted}}
		})
		audit := &fakeAuditCapture{}
		req, _, writer := newTestRequest("POST", "/v1/chat/completions")
		req.HTTP.Header.Set("Authorization", "Bearer sk-good")
		bodyReq := bodyRequestForBody(map[string]any{"model": "gpt-x"})
		req.Body = &bodyReq
		options := &PreflightOptions{}
		if withProvided {
			// chat 覆盖注入形态：不设 Identity，仅注入绑定作用域收敛的候选。
			options.CandidateAccounts = []gatewayruntimecache.OpenAIAccountSecret{{ID: "acc-bound", ProviderCode: "openai"}}
			options.RecoverableCandidateScope = &CandidateRecoverableScope{GroupIDs: []string{"grp-bound"}}
		}
		result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
			Req: req, Res: writer, AuditCapture: audit, Options: options,
			StartedAt: 1, TraceID: "trace", ClientIP: "203.0.113.9",
			Endpoint: "POST /v1/chat/completions", RequestSnapshot: UsageRequestSnapshot{},
		})
		if err != nil {
			t.Fatalf("PrepareOpenAIGatewayDispatchContext err = %v", err)
		}
		if result.DispatchContext != nil {
			t.Fatalf("fakeCandidates Completed 语义下不应产出 DispatchContext")
		}
		return resolver, sink
	}

	t.Run("注入候选跳过 normal 路由判定", func(t *testing.T) {
		resolver, sink := run(true)
		if atomic.LoadInt32(&resolver.calls) != 0 {
			t.Fatalf("chat 覆盖候选注入时不应调用 normal 路由判定: %d", resolver.calls)
		}
		if failure, ok := sink.lastFailure(); ok {
			t.Fatalf("不应发送失败响应: %+v", failure)
		}
	})
	t.Run("无注入候选保持既有判定", func(t *testing.T) {
		resolver, sink := run(false)
		if atomic.LoadInt32(&resolver.calls) != 1 {
			t.Fatalf("外部请求应调用 normal 路由判定: %d", resolver.calls)
		}
		failure, ok := sink.lastFailure()
		if !ok || failure.StatusCode != 404 {
			t.Fatalf("应保留 404 normal 路由失败: %+v ok=%v", failure, ok)
		}
	})
}

// batch2ScopedRuntimeCache 按 groupID 配置 active/recoverable 读取并记录读取
// 组集合的 fake（其余读取沿用 fakeRuntimeCache 零值默认）。
type batch2ScopedRuntimeCache struct {
	fakeRuntimeCache
	mu               sync.Mutex
	activeByGroup    map[string][]gatewayruntimecache.OpenAIAccountSecret
	recoverableByGrp map[string][]gatewayruntimecache.OpenAIAccountSecret
	freshGroups      []string
	recoverableGrps  []string
}

func (c *batch2ScopedRuntimeCache) ListFreshOpenAIAccountsForGroupAsync(_ context.Context, groupID, _ string, _ gatewayruntimecache.CachedOpenAIAccountsForGroupOptions) ([]gatewayruntimecache.OpenAIAccountSecret, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.freshGroups = append(c.freshGroups, groupID)
	return c.activeByGroup[groupID], nil
}

func (c *batch2ScopedRuntimeCache) ListRecoverableUnavailableOpenAIAccountsForGroupAsync(_ context.Context, groupID, _ string, _ gatewayruntimecache.CachedOpenAIAccountsForGroupOptions, _ *int64) ([]gatewayruntimecache.OpenAIAccountSecret, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recoverableGrps = append(c.recoverableGrps, groupID)
	return c.recoverableByGrp[groupID], nil
}

func (c *batch2ScopedRuntimeCache) setActive(groupID string, accounts []gatewayruntimecache.OpenAIAccountSecret) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.activeByGroup[groupID] = accounts
}

func (c *batch2ScopedRuntimeCache) snapshotGroups() (fresh []string, recoverable []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string{}, c.freshGroups...), append([]string{}, c.recoverableGrps...)
}

// batch2RecoveringWait 记录 scopeKey 并在等待点执行恢复回调的 fake。
type batch2RecoveringWait struct {
	mu        sync.Mutex
	scopeKeys []string
	onWait    func()
}

func (w *batch2RecoveringWait) WaitForRecoverableUnavailableState(_ context.Context, input RecoverableWaitInput) error {
	w.mu.Lock()
	w.scopeKeys = append(w.scopeKeys, input.ScopeKey)
	onWait := w.onWait
	w.mu.Unlock()
	if onWait != nil {
		onWait()
	}
	return nil
}

// TestRecoverableLoaderProvidedScopeBatch2 覆盖缺陷5的 loader 分支：provided +
// 作用域注入 → 接入等待；provided 无作用域（API Key 分组回退）→ 保持短路；
// 非 provided → 保持按窗口组等待。
func TestRecoverableLoaderProvidedScopeBatch2(t *testing.T) {
	service := &Service{}
	options := &PreflightOptions{CandidateAccounts: []gatewayruntimecache.OpenAIAccountSecret{{ID: "acc-1"}}}
	provided := recoverableLoader(service, options, nil, validRuntimeRow(), recoveryInput{groupID: "group_1"})
	if provided != nil {
		t.Fatalf("provided 无作用域应保持短路: %v", provided != nil)
	}
	scoped := &PreflightOptions{
		CandidateAccounts:         options.CandidateAccounts,
		RecoverableCandidateScope: &CandidateRecoverableScope{GroupIDs: []string{"grp-bound"}},
	}
	if loader := recoverableLoader(service, scoped, nil, validRuntimeRow(), recoveryInput{groupID: "group_1"}); loader == nil {
		t.Fatalf("provided + 作用域应接入可恢复等待")
	}
	external := recoverableLoader(service, &PreflightOptions{}, nil, validRuntimeRow(), recoveryInput{groupID: "group_1"})
	if external == nil {
		t.Fatalf("外部请求应保持按窗口组等待")
	}
}

// TestWaitForRecoverableProvidedCandidateAccountsBatch2 覆盖缺陷5的等待实现：
// 绑定作用域候选全部冷却时按注入作用域组等待，恢复后返回候选；读取组集合
// 限定为注入作用域（不读 Key 策略窗口组），account 模式按目标账户收敛。
func TestWaitForRecoverableProvidedCandidateAccountsBatch2(t *testing.T) {
	clock := newFakeClock(1_700_000_000_000)
	buildReq := func() *GatewayRequest {
		req, _, _ := newTestRequest("POST", "/v1/chat/completions")
		bodyReq := bodyRequestForBody(map[string]any{"model": "gpt-5"})
		req.Body = &bodyReq
		return req
	}
	t.Run("group 模式按绑定组等待恢复", func(t *testing.T) {
		cache := &batch2ScopedRuntimeCache{
			activeByGroup:    map[string][]gatewayruntimecache.OpenAIAccountSecret{"grp-bound": {}},
			recoverableByGrp: map[string][]gatewayruntimecache.OpenAIAccountSecret{"grp-bound": {{ID: "acc-cool", ProviderCode: "openai"}}},
		}
		waiter := &batch2RecoveringWait{onWait: func() {
			cache.setActive("grp-bound", []gatewayruntimecache.OpenAIAccountSecret{{ID: "acc-cool", ProviderCode: "openai"}})
		}}
		service := &Service{RuntimeCache: cache, Recoverable: waiter, Clock: clock}
		input := recoveryInput{
			req: buildReq(), systemAccountID: "sys_1", apiKeyID: "key_1",
			groupID: "group_1", serverRetryBudget: NewServerRetryBudget(60000, clock),
			recoverableScope: &CandidateRecoverableScope{GroupIDs: []string{"grp-bound"}},
		}
		accounts, err := service.waitForRecoverableProvidedCandidateAccounts(input)
		if err != nil {
			t.Fatalf("等待恢复 err = %v", err)
		}
		if len(accounts) != 1 || accounts[0].ID != "acc-cool" {
			t.Fatalf("恢复候选不符: %+v", accounts)
		}
		fresh, recoverable := cache.snapshotGroups()
		for _, group := range append(fresh, recoverable...) {
			if group != "grp-bound" {
				t.Fatalf("读取逃逸绑定作用域: fresh=%v recoverable=%v", fresh, recoverable)
			}
		}
		waiter.mu.Lock()
		keys := append([]string{}, waiter.scopeKeys...)
		waiter.mu.Unlock()
		if len(keys) != 1 || keys[0] == "" {
			t.Fatalf("等待 scopeKey 不符: %v", keys)
		}
	})
	t.Run("account 模式按目标账户收敛", func(t *testing.T) {
		cache := &batch2ScopedRuntimeCache{
			activeByGroup: map[string][]gatewayruntimecache.OpenAIAccountSecret{
				"grp-a": {{ID: "acc-other", ProviderCode: "openai"}},
				"grp-b": {{ID: "acc-target", ProviderCode: "openai"}},
			},
		}
		service := &Service{RuntimeCache: cache, Recoverable: &batch2RecoveringWait{}, Clock: clock}
		input := recoveryInput{
			req: buildReq(), systemAccountID: "sys_1", apiKeyID: "key_1",
			groupID: "group_1", serverRetryBudget: NewServerRetryBudget(60000, clock),
			recoverableScope: &CandidateRecoverableScope{GroupIDs: []string{"grp-a", "grp-b"}, AccountID: "acc-target"},
		}
		accounts, err := service.waitForRecoverableProvidedCandidateAccounts(input)
		if err != nil {
			t.Fatalf("收敛 err = %v", err)
		}
		if len(accounts) != 1 || accounts[0].ID != "acc-target" {
			t.Fatalf("account 模式应收敛为目标账户单元素: %+v", accounts)
		}
	})
	t.Run("作用域无可恢复账户直接空返回", func(t *testing.T) {
		cache := &batch2ScopedRuntimeCache{activeByGroup: map[string][]gatewayruntimecache.OpenAIAccountSecret{"grp-bound": {}}}
		waiter := &batch2RecoveringWait{}
		service := &Service{RuntimeCache: cache, Recoverable: waiter, Clock: clock}
		input := recoveryInput{
			req: buildReq(), systemAccountID: "sys_1", apiKeyID: "key_1",
			groupID: "group_1", serverRetryBudget: NewServerRetryBudget(60000, clock),
			recoverableScope: &CandidateRecoverableScope{GroupIDs: []string{"grp-bound"}},
		}
		accounts, err := service.waitForRecoverableProvidedCandidateAccounts(input)
		if err != nil {
			t.Fatalf("无可恢复候选 err = %v", err)
		}
		if len(accounts) != 0 {
			t.Fatalf("无可恢复候选应返回空: %+v", accounts)
		}
		waiter.mu.Lock()
		waited := len(waiter.scopeKeys)
		waiter.mu.Unlock()
		if waited != 0 {
			t.Fatalf("无可恢复候选不应进入等待: %d", waited)
		}
	})
}
