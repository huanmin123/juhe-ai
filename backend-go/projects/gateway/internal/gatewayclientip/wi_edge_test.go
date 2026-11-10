package gatewayclientip

import (
	"context"
	"errors"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// erroringStateStore 是全失败的 RuntimeStateStore：驱动层错误必须透传，
// 不允许静默当作未命中处理。
type erroringStateStore struct{}

func (erroringStateStore) GetJSON(context.Context, string, any) (bool, error) {
	return false, errors.New("状态存储不可用")
}
func (erroringStateStore) SetJSON(context.Context, string, any, int64) error {
	return errors.New("状态存储不可用")
}
func (erroringStateStore) Delete(context.Context, string) error {
	return errors.New("状态存储不可用")
}

func TestWICircuitRedisErrorPropagation(t *testing.T) {
	circuit, err := NewErrorCircuit(ErrorCircuitOptions{
		Clock:              newManualClock(time.UnixMilli(1_000_000)),
		RuntimeStateDriver: RuntimeStateDriverRedis,
		StateStore:         erroringStateStore{},
	})
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	t.Cleanup(circuit.Close)
	ctx := context.Background()
	preAuthInput := gatewaypreauth.PreAuthCircuitInput{ClientIP: "10.0.0.1"}
	failureInput := gatewaypreauth.PreAuthFailureInput{ClientIP: "10.0.0.1", Reason: gatewaypreauth.PreAuthFailureMissingBearerToken}
	errorScope := gatewaypreauth.ClientIPErrorCircuitInput{SystemAccountID: "sys", ClientIP: "10.0.0.1"}
	sampleInput := gatewaypreauth.ClientIPErrorCircuitSampleInput{SystemAccountID: "sys", ClientIP: "10.0.0.1"}

	if _, err := circuit.InspectPreAuthCircuit(ctx, preAuthInput); err == nil {
		t.Fatal("inspect 状态存储故障必须透传")
	}
	if _, err := circuit.RecordPreAuthFailure(ctx, failureInput); err == nil {
		t.Fatal("记录状态存储故障必须透传")
	}
	if _, err := circuit.InspectClientIPErrorCircuit(ctx, errorScope); err == nil {
		t.Fatal("错误熔断 inspect 必须透传")
	}
	if _, err := circuit.RecordClientIPErrorCircuitSample(ctx, sampleInput); err == nil {
		t.Fatal("错误熔断采样必须透传")
	}
	if err := circuit.RecordClientIPErrorCircuitSuccess(ctx, errorScope); err == nil {
		t.Fatal("错误熔断 success 必须透传")
	}
}

func TestWIClientIPConcurrencyReleaseRetryWarns(t *testing.T) {
	server := miniredis.RunT(t)
	addr := server.Addr()
	logger := &spyingLogger{}
	concurrency, err := NewClientIPConcurrency(ClientIPConcurrencyOptions{
		Clock:              newManualClock(time.UnixMilli(3_000_000)),
		Logger:             logger,
		RuntimeStateDriver: RuntimeStateDriverRedis,
		StateRedisURL:      "redis://" + addr,
		Sleep:              func(time.Duration) {}, // 重试延迟压缩为 0，保证确定性。
	})
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	t.Cleanup(concurrency.Close)
	// redis 命令全部报错：释放走完全部重试并最终告警（SetError 保持连接可建，
	// 避免拨号重试的不确定性等待）。
	server.SetError("模拟故障")
	concurrency.releaseRedisClientIPSlotWithRetry(context.Background(), "k", "tok")
	if logger.count("redis_client_ip_concurrency_release_failed") != 1 {
		t.Fatalf("重试耗尽必须告警一次: %d", logger.count("redis_client_ip_concurrency_release_failed"))
	}
	// 无 logger 时重试耗尽静默返回。
	quiet, err := NewClientIPConcurrency(ClientIPConcurrencyOptions{
		Clock:              newManualClock(time.UnixMilli(3_000_000)),
		RuntimeStateDriver: RuntimeStateDriverRedis,
		StateRedisURL:      "redis://" + addr,
		Sleep:              func(time.Duration) {},
	})
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	t.Cleanup(quiet.Close)
	quiet.releaseRedisClientIPSlotWithRetry(context.Background(), "k", "tok")
}

func TestWIAvoidanceMemoryAsyncDelegates(t *testing.T) {
	// memory 驱动的 Async 入口必须等价委托 memory 实现（Node 单驱动语义）。
	avoidance, _ := newTestAvoidance(t, nil)
	ctx := context.Background()
	scope := AvoidanceScopeInput{SystemAccountID: "sys", APIKeyID: "key", ClientIP: "10.0.0.1"}
	tracker := avoidance.CreateAvoidanceTracker(scope)
	avoidance.RememberPendingFailure(tracker, "a1", "A1", AccountFailure{ErrorPhase: "stream"})

	confirm, err := avoidance.ConfirmAfterSuccessAsync(ctx, tracker, "a1", nil)
	// a1 是成功账户被跳过、且 entries 表尚无记录：确认列表为空、未清除。
	if err != nil || confirm.Cleared || confirm.ClearedAccountID != "a1" || len(confirm.ConfirmedAccountIDs) != 0 {
		t.Fatalf("success async=%+v err=%v", confirm, err)
	}
	tracker2 := avoidance.CreateAvoidanceTracker(scope)
	avoidance.RememberPendingFailure(tracker2, "a2", "A2", AccountFailure{ErrorPhase: "stream"})
	finalConfirm, err := avoidance.ConfirmAfterFinalFailureAsync(ctx, tracker2, nil)
	if err != nil || len(finalConfirm.ConfirmedAccountIDs) != 1 {
		t.Fatalf("final failure async=%+v err=%v", finalConfirm, err)
	}
	cleared, err := avoidance.ClearForAccountAsync(ctx, tracker2, "a2")
	if err != nil || !cleared {
		t.Fatalf("clear async=%v err=%v", cleared, err)
	}
}

func TestWIPolicyCacheRedisStatsBridge(t *testing.T) {
	t.Run("stats 缺失时共享回源报错", func(t *testing.T) {
		factory := newFakeSharedCacheFactory()
		cache, _, _, _, _ := newWITestPolicyCache(t, func(opts *PolicyCacheOptions) {
			opts.CacheDriver = CacheDriverRedis
			opts.Shared = factory
			opts.ProcessRole = ProcessRoleServer
			opts.StatsWriter = nil
		})
		if _, err := cache.InspectPolicy(context.Background(), "10.0.0.1", InspectClientIPPolicyOptions{}); err == nil {
			t.Fatal("stats 缺失的共享回源必须报错")
		}
	})
	t.Run("stats 失败时共享回源报错", func(t *testing.T) {
		factory := newFakeSharedCacheFactory()
		stats := &stubStatsWriter{err: errors.New("桥接失败")}
		cache, _, _, _, _ := newWITestPolicyCache(t, func(opts *PolicyCacheOptions) {
			opts.CacheDriver = CacheDriverRedis
			opts.Shared = factory
			opts.ProcessRole = ProcessRoleServer
			opts.StatsWriter = stats
		})
		if _, err := cache.InspectPolicy(context.Background(), "10.0.0.1", InspectClientIPPolicyOptions{}); err == nil {
			t.Fatal("stats 失败必须透传")
		}
	})
	t.Run("stats 成功时共享回源并落缓存", func(t *testing.T) {
		factory := newFakeSharedCacheFactory()
		stats := &stubStatsWriter{}
		policy := wiBlacklistPolicy("p1", NormalizeClientIPForStats("10.0.0.1").IPHash)
		stats.findResult = &policy
		cache, _, _, _, _ := newWITestPolicyCache(t, func(opts *PolicyCacheOptions) {
			opts.CacheDriver = CacheDriverRedis
			opts.Shared = factory
			opts.ProcessRole = ProcessRoleServer
			opts.StatsWriter = stats
		})
		decision, err := cache.InspectPolicy(context.Background(), "10.0.0.1", InspectClientIPPolicyOptions{})
		if err != nil || !decision.Blocked {
			t.Fatalf("stats 回源=%+v err=%v", decision, err)
		}
		if len(stats.operations) != 1 || stats.operations[0] != StatsWriterOpFindActiveClientIPPolicyByHash {
			t.Fatalf("operations=%v", stats.operations)
		}
	})
	t.Run("形状非法的共享条目按缺失处理", func(t *testing.T) {
		factory := newFakeSharedCacheFactory()
		cache, source, _, _, _ := newWITestPolicyCache(t, func(opts *PolicyCacheOptions) {
			opts.CacheDriver = CacheDriverRedis
			opts.Shared = factory
		})
		// 直接写入共享缓存的非法形状（policyType 未知）→ 视为无策略。
		if err := cache.sharedByIP.Set(context.Background(), NormalizeClientIPForStats("10.0.0.1").IPHash, sharedByIPEntry{
			LoadedAt: "2024-01-01T00:00:00.000Z",
			Policy:   &ActiveClientIPPolicy{ID: "x", PolicyType: "weird"},
		}, clientIPPolicyCacheTTL); err != nil {
			t.Fatalf("注入共享条目失败: %v", err)
		}
		decision, err := cache.InspectPolicy(context.Background(), "10.0.0.1", InspectClientIPPolicyOptions{CacheOnly: true})
		if err != nil || decision.Blocked {
			t.Fatalf("非法形状必须按缺失处理: %+v err=%v", decision, err)
		}
		if source.findCalls != 0 {
			t.Fatal("cacheOnly 不得回源")
		}
	})
}

func TestWIGatewayClientIPKeyHelpers(t *testing.T) {
	// 队列键与容量聚合 helper（apiKeyQueueKey / uniqueAccountIDs /
	// buildAccountCapacities）已随高并发队列退役删除。
	if isActiveClientIPPolicyShape(ActiveClientIPPolicy{PolicyType: PolicyTypeBlacklist}) != true {
		t.Fatal("blacklist 形状必须合法")
	}
	if isActiveClientIPPolicyShape(ActiveClientIPPolicy{PolicyType: "x"}) {
		t.Fatal("未知类型必须非法")
	}
	ranks := ModelRankByAccountID(&gatewayrouting.GatewayAccountModelPriority{RankByAccountID: map[string]int{"a": 2}})
	if ranks == nil || ranks["a"] != 2 {
		t.Fatalf("ModelRankByAccountID=%v", ranks)
	}
}

func TestWIRedisAccountConcurrencyLaneErrors(t *testing.T) {
	// ByLane 的空列表短路分支。
	server := miniredis.RunT(t)
	instance, _ := newTestRedisAccountConcurrency(t, server, "dev")
	empty, err := instance.LoadAccountCurrentConcurrencyByLane(context.Background(), nil, "text")
	if err != nil || len(empty) != 0 {
		t.Fatalf("空列表=%v err=%v", empty, err)
	}
}

var _ = gatewaypreauth.ClientIPPolicyDecision{}
var _ = gatewayruntimecache.GatewaySettings{}
