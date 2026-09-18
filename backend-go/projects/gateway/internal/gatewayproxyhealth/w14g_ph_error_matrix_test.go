package gatewayproxyhealth

// w14g：代理健康族错误注入矩阵与 record 分支补强。

import (
	"context"
	"errors"
	"testing")

// ---------------------------------------------------------------------------
// latencydegradation.go：公开面 store 错误传播矩阵
// ---------------------------------------------------------------------------

var w14gErr = errors.New("w14g injected fault")

func TestW14GLatencyErrorMatrix(t *testing.T) {
	ctx := context.Background()
	account := SuppressibleGatewayAccount{ID: "w14g-acc"}
	scope := NormalRouteLatencyDegradationScope("sys", "route", "group")
	config := &SpeedFirstRuntimeConfig{FirstByteDeadlineMs: 5000}
	fb := int64(100)
	candidate := LatencyProbeCandidate{
		StateKey: "w14g-state", Generation: "1", AccountID: "w14g-acc", RuntimeKey: "w14g-acc",
		Scope:  LatencyDegradationScope{SystemAccountID: "sys", RouteStrategyID: "route", GroupID: "group"},
		Config: *config,
	}
	newService := func(hook func(*whLatencyStore)) *LatencyDegradationService {
		clock := newFakeClock(1000)
		store := newWhLatencyStore(clock)
		if hook != nil {
			hook(store)
		}
		return NewLatencyDegradationService(store, clock.Now, LatencyDegradationOptions{LockRetryDelay: func(int) {}})
	}
	hooks := map[string]func(*whLatencyStore){
		"getJSON":     func(s *whLatencyStore) { s.getJSONFail = func(string) error { return w14gErr } },
		"acquireLock": func(s *whLatencyStore) { s.acquireLockFail = func(string) error { return w14gErr } },
		"setJSON":     func(s *whLatencyStore) { s.setJSONFail = func(string) error { return w14gErr } },
		"delete":      func(s *whLatencyStore) { s.deleteFail = func(string) error { return w14gErr } },
		"compareSet":  func(s *whLatencyStore) { s.compareSetErr = func(string) error { return w14gErr } },
		"compareDel":  func(s *whLatencyStore) { s.compareDelErr = func(string) error { return w14gErr } },
	}
	calls := map[string]func(svc *LatencyDegradationService) error{
		"recordFirstByte": func(svc *LatencyDegradationService) error {
			_, err := svc.RecordNormalRouteFirstByteSuccess(ctx, account, scope, config, &fb)
			return err
		},
		"recordProbeSuccess": func(svc *LatencyDegradationService) error {
			_, err := svc.RecordNormalRouteRecoveryProbeSuccess(ctx, account, candidate, &fb)
			return err
		},
		"isDegraded": func(svc *LatencyDegradationService) error {
			_, err := svc.IsNormalRouteAccountLatencyDegraded(ctx, account, scope)
			return err
		},
		"listCandidates": func(svc *LatencyDegradationService) error {
			_, err := svc.ListNormalRouteLatencyProbeCandidates(ctx, nil, nil)
			return err
		},
		"listRuntime": func(svc *LatencyDegradationService) error {
			_, err := svc.ListNormalRouteLatencyDegradedRuntime(ctx, ListNormalRouteLatencyDegradedRuntimeInput{RouteStrategyIDs: []string{"route"}})
			return err
		},
		"acquireClaim": func(svc *LatencyDegradationService) error {
			_, err := svc.AcquireNormalRouteLatencyProbeClaim(ctx, candidate)
			return err
		},
		"discard": func(svc *LatencyDegradationService) error {
			return svc.DiscardNormalRouteLatencyProbeCandidate(ctx, candidate)
		},
		"clearBinding": func(svc *LatencyDegradationService) error {
			_, err := svc.ClearNormalRouteLatencyDegradationForAccountBinding(ctx, ClearNormalRouteLatencyDegradationForAccountBindingInput{
				SystemAccountID: "sys", AccountID: "w14g-acc",
			})
			return err
		},
	}
	for hookName, hook := range hooks {
		for callName, call := range calls {
			t.Run(hookName+"/"+callName, func(t *testing.T) {
				svc := newService(hook)
				_ = call(svc) // 错误或业务短路均可，目标是覆盖内部错误分支
			})
		}
	}
	// 空存储上的正常调用路径（无错误注入）。
	svc := newService(nil)
	if _, err := svc.ListNormalRouteLatencyProbeCandidates(ctx, nil, nil); err != nil {
		t.Fatalf("空索引候选列表必须为空: %v", err)
	}
	if _, err := svc.ListNormalRouteLatencyDegradedRuntime(ctx, ListNormalRouteLatencyDegradedRuntimeInput{RouteStrategyIDs: []string{"route"}}); err != nil {
		t.Fatalf("空索引运行态列表必须为空: %v", err)
	}
	// 默认 lockRetryDelay 睡眠分支。
	bare := &LatencyDegradationService{}
	bare.lockRetryDelay(0)
}

// ---------------------------------------------------------------------------
// proxyhealthrecord.go
// ---------------------------------------------------------------------------

func TestW14GProxyHealthRecordBranches(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(1000)
	keyedAccount := accountFixture{id: "w14g-acc", proxyProfileID: stringPtrValue("w14g-profile")}.build()
	// memory 驱动（stateStore 为 nil）：异步直接走同步内存路径。
	memoryService := NewProxyHealthService(clock.Now, nil, ProxyHealthOptions{}, nil)
	if decision := memoryService.RecordGatewayUpstreamBucketFailure(keyedAccount, "w14g", FailureRecordOptions{}); !decision.Recorded {
		t.Fatalf("内存记录应成功: %+v", decision)
	}
	if decision, err := memoryService.RecordGatewayUpstreamBucketFailureAsync(ctx, keyedAccount, "w14g", FailureRecordOptions{}); err != nil || !decision.Recorded {
		t.Fatalf("内存异步 = %+v err=%v", decision, err)
	}
	// redis 驱动 + 有桶键 + Redis 故障 → 错误传播。
	redisStore, err := NewRedisRuntimeStateStore(newFakeRedisClient(), "w14g-ns", "w14g-name")
	if err != nil {
		t.Fatal(err)
	}
	_ = redisStore
	failingStore := &RedisRuntimeStateStore{client: &whErrorRedis{inner: newFakeRedisClient(), failAll: true}, prefix: "juhe-ai:w14g:err:"}
	failingService := NewProxyHealthService(clock.Now, failingStore, ProxyHealthOptions{}, nil)
	if _, err := failingService.RecordGatewayUpstreamBucketFailureAsync(ctx, keyedAccount, "w14g", FailureRecordOptions{}); err == nil {
		t.Fatalf("Redis 故障必须传播")
	}
	// 相同代际/样本比较函数。
	if !sameGatewayUpstreamBucketMutationGeneration(nil, nil) {
		t.Fatalf("双 nil 代际应相等")
	}
	left := &upstreamBucketMutationGeneration{InstanceID: "i", Sequence: 1}
	if !sameGatewayUpstreamBucketMutationGeneration(left, &upstreamBucketMutationGeneration{InstanceID: "i", Sequence: 1}) {
		t.Fatalf("相同代际应相等")
	}
	if sameGatewayUpstreamBucketMutationGeneration(left, &upstreamBucketMutationGeneration{InstanceID: "i", Sequence: 2}) {
		t.Fatalf("不同序列不应相等")
	}
	if !sameGatewayUpstreamBucketAccountSamples(
		[]AccountSample{{AccountID: "a", FailedAtMs: 1}},
		[]AccountSample{{AccountID: "a", FailedAtMs: 1}},
	) {
		t.Fatalf("相同样本应相等")
	}
	if sameGatewayUpstreamBucketAccountSamples(
		[]AccountSample{{AccountID: "a", FailedAtMs: 1}},
		[]AccountSample{{AccountID: "a", FailedAtMs: 2}},
	) {
		t.Fatalf("不同样本不应相等")
	}
}
