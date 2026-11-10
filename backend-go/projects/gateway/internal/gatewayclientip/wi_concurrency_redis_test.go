package gatewayclientip

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayscheduling"
)

// wiIPGatePolicy 构造两键普通分组策略（clientIpConcurrencyLimit 生效）。
func wiIPGatePolicy(limit int) *gatewayscheduling.Policy {
	return &gatewayscheduling.Policy{DefaultSoftConcurrency: -1, ClientIPConcurrencyLimit: limit}
}

// 排队溢出模式的内存/Redis 测试已随队列退役删除；IP 闸口只保留 reject 即时
// 拒绝语义（获取/满额拒绝/释放恢复/取消/scope 隔离见 concurrency_test.go）。

func TestWIClientIPConcurrencyRedisSlotContract(t *testing.T) {
	server := miniredis.RunT(t)
	clock := newManualClock(time.UnixMilli(3_000_000))
	concurrency, err := NewClientIPConcurrency(ClientIPConcurrencyOptions{
		Clock: clock, RuntimeStateDriver: RuntimeStateDriverRedis,
		StateRedisURL: "redis://" + server.Addr(),
	})
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	t.Cleanup(concurrency.Close)
	ctx := context.Background()
	input := ClientIPConcurrencyAcquireInput{
		SystemAccountID: "sys", GroupID: "grp", APIKeyID: "key",
		ClientIP: "10.0.0.1", Policy: wiIPGatePolicy(1),
	}
	key := clientIPConcurrencyKey("sys", "grp", "key", "10.0.0.1")

	// 首次获取成功并写入 redis 槽。
	first, err := concurrency.AcquireClientIPSlot(ctx, input)
	if err != nil || !first.Acquired || first.Current != 1 {
		t.Fatalf("redis 首次获取=%+v err=%v", first, err)
	}
	slotKey := redisClientIPConcurrencyKeyNS("", key)
	if wiZCard(t, server, slotKey) != 1 {
		t.Fatalf("槽 ZSET=%d want 1", wiZCard(t, server, slotKey))
	}
	// 契约：续租只对持有同 token 的槽生效；陌生 token 返回 false。
	members, err := server.ZMembers(slotKey)
	if err != nil || len(members) != 1 {
		t.Fatalf("槽成员=%v err=%v", members, err)
	}
	token := members[0]
	renewed, err := concurrency.renewRedisClientIPSlot(ctx, key, token)
	if err != nil || !renewed {
		t.Fatalf("同 token 续租=%v err=%v", renewed, err)
	}
	if renewed, err := concurrency.renewRedisClientIPSlot(ctx, key, "bogus-token"); err != nil || renewed {
		t.Fatalf("陌生 token 续租必须 false: %v err=%v", renewed, err)
	}
	// 释放：槽被删除且幂等。
	first.Release()
	if wiZCard(t, server, slotKey) != 0 {
		t.Fatalf("释放后槽 ZSET=%d want 0", wiZCard(t, server, slotKey))
	}
	first.Release()

	// 拒绝路径：打满 → limit_reached 即时拒绝（溢出队列模式已退役）。
	rejectInput := ClientIPConcurrencyAcquireInput{
		SystemAccountID: "sys", GroupID: "grp", APIKeyID: "key",
		ClientIP: "10.0.0.2",
		Policy:   wiIPGatePolicy(1),
	}
	held, err := concurrency.AcquireClientIPSlot(ctx, rejectInput)
	if err != nil || !held.Acquired {
		t.Fatalf("拒绝测试持有失败: %+v err=%v", held, err)
	}
	rejected, err := concurrency.AcquireClientIPSlot(ctx, rejectInput)
	if err != nil || rejected.Acquired || rejected.Reason != RejectLimitReached {
		t.Fatalf("reject 决策=%+v err=%v", rejected, err)
	}
	held.Release()

	// redis 不可用：错误透传，不静默降级。
	server.Close()
	if _, err := concurrency.AcquireClientIPSlot(ctx, input); err == nil {
		t.Fatal("redis 不可用时必须报错")
	}
}

func TestWIClientIPConcurrencyRedisConstructionError(t *testing.T) {
	if _, err := NewClientIPConcurrency(ClientIPConcurrencyOptions{RuntimeStateDriver: RuntimeStateDriverRedis}); err == nil {
		t.Fatal("redis driver 缺 URL 必须报错")
	}
}

func TestWIAvoidanceNotAppliedOrder(t *testing.T) {
	avoidance, _ := newTestAvoidance(t, nil)
	accounts := []gatewayruntimecache.OpenAIAccountSecret{account("a1", 1, false, false)}
	// 空 scope → 原样返回（notAppliedAvoidanceOrder）。
	result := avoidance.OrderAccountsByClientIPAccountAvoidance(accounts, AvoidanceScopeInput{ClientIP: " "}, nil)
	if result.Applied || len(result.Accounts) != 1 || result.Accounts[0].ID != "a1" {
		t.Fatalf("nil scope result=%+v", result)
	}
	// 空账户列表 → 原样返回。
	empty := avoidance.OrderAccountsByClientIPAccountAvoidance(nil, AvoidanceScopeInput{ClientIP: "10.0.0.1"}, nil)
	if empty.Applied || len(empty.Accounts) != 0 {
		t.Fatalf("空列表 result=%+v", empty)
	}
}

func TestWIGatewayDispatchModelRank(t *testing.T) {
	// nil options → rank 0；缺失账号 → unknown rank 3；负 rank → 0。
	if got := gatewayAccountDispatchModelRank("a", DispatchPriorityTierInput{}); got != 0 {
		t.Fatalf("nil options rank=%d", got)
	}
	opts := DispatchPriorityTierInput{ModelRankByAccountID: map[string]int{"known": 2, "negative": -5}}
	if got := gatewayAccountDispatchModelRank("known", opts); got != 2 {
		t.Fatalf("known rank=%d", got)
	}
	if got := gatewayAccountDispatchModelRank("missing", opts); got != unknownGatewayDispatchModelRank {
		t.Fatalf("missing rank=%d", got)
	}
	if got := gatewayAccountDispatchModelRank("negative", opts); got != 0 {
		t.Fatalf("negative rank=%d", got)
	}
	if ModelRankByAccountID(nil) != nil {
		t.Fatal("nil priority 必须返回 nil map")
	}
	// tier 字符串组合：model:fallback:super:priority。
	tier := GatewayAccountDispatchPriorityTier(gatewayruntimecache.OpenAIAccountSecret{
		ID: "a", Priority: 7, SuperPriorityEnabled: true,
	}, DispatchPriorityTierInput{ModelRankByAccountID: map[string]int{"a": 1}})
	if want := "1:0:0:7"; tier != want {
		t.Fatalf("tier=%q want %q", tier, want)
	}
	// 单账户时 tier 保持直接返回副本（len<2 快路径）。
	single := []gatewayruntimecache.OpenAIAccountSecret{account("a", 1, false, false)}
	out := PreserveGatewayAccountDispatchPriorityTiers(single, single, DispatchPriorityTierInput{})
	if len(out) != 1 || out[0].ID != "a" {
		t.Fatalf("单账户 tier=%+v", out)
	}
}

func TestWIPreAuthSprayThresholdBothDrivers(t *testing.T) {
	t.Run("memory 驱动", func(t *testing.T) {
		circuit, err := NewErrorCircuit(ErrorCircuitOptions{Clock: newManualClock(time.UnixMilli(1_000_000))})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(circuit.Close)
		// 同一 IP 用 120 个不同 token 打 invalid_api_key → spray 阈值熔断。
		var decision gatewaypreauth.CircuitDecision
		for i := 0; i < preAuthInvalidTokenSprayThreshold; i++ {
			decision = circuit.RecordGatewayPreAuthFailure(gatewaypreauth.PreAuthFailureInput{
				ClientIP:      "10.0.0.1",
				Authorization: "Bearer token-" + strconv.Itoa(i),
				Reason:        gatewaypreauth.PreAuthFailureInvalidAPIKey,
			})
		}
		if !decision.Blocked || decision.Reason != preAuthReasonInvalidTokenSpray {
			t.Fatalf("spray 阈值必须熔断: %+v", decision)
		}
		// spray 熔断后同 IP 任何 invalid_api_key 直接返回 spray 决策。
		if decision := circuit.RecordGatewayPreAuthFailure(gatewaypreauth.PreAuthFailureInput{
			ClientIP: "10.0.0.1", Authorization: "Bearer another", Reason: gatewaypreauth.PreAuthFailureInvalidAPIKey,
		}); !decision.Blocked || decision.Reason != preAuthReasonInvalidTokenSpray {
			t.Fatalf("spray 熔断后必须短路: %+v", decision)
		}
		// 快照包含 preAuth 行。
		preAuthRows, _ := circuit.SecuritySnapshotForTest()
		if len(preAuthRows) == 0 {
			t.Fatal("preAuth 快照不得为空")
		}
	})

	t.Run("redis 驱动", func(t *testing.T) {
		server := miniredis.RunT(t)
		circuit, err := NewErrorCircuit(ErrorCircuitOptions{
			Clock:              newManualClock(time.UnixMilli(1_000_000)),
			RuntimeStateDriver: RuntimeStateDriverRedis,
			StateRedisURL:      "redis://" + server.Addr(),
			RedisNamespace:     "dev",
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(circuit.Close)
		ctx := context.Background()
		var decision gatewaypreauth.CircuitDecision
		for i := 0; i < preAuthInvalidTokenSprayThreshold; i++ {
			decision, err = circuit.RecordPreAuthFailure(ctx, gatewaypreauth.PreAuthFailureInput{
				ClientIP:      "10.0.0.1",
				Authorization: "Bearer token-" + strconv.Itoa(i),
				Reason:        gatewaypreauth.PreAuthFailureInvalidAPIKey,
			})
			if err != nil {
				t.Fatalf("第 %d 次记录失败: %v", i, err)
			}
		}
		if !decision.Blocked {
			t.Fatalf("redis spray 阈值必须熔断: %+v", decision)
		}
		// 空作用域（空 IP）的异步入口全部返回不阻塞。
		if inspect, err := circuit.InspectPreAuthCircuit(ctx, gatewaypreauth.PreAuthCircuitInput{ClientIP: " "}); err != nil || inspect.Blocked {
			t.Fatalf("空 IP=%+v err=%v", inspect, err)
		}
		if inspect, err := circuit.InspectClientIPErrorCircuit(ctx, gatewaypreauth.ClientIPErrorCircuitInput{ClientIP: " "}); err != nil || inspect.Blocked {
			t.Fatalf("空 IP 错误熔断=%+v err=%v", inspect, err)
		}
		if err := circuit.RecordClientIPErrorCircuitSuccess(ctx, gatewaypreauth.ClientIPErrorCircuitInput{ClientIP: " "}); err != nil {
			t.Fatalf("空 IP success=%v", err)
		}
		// 未熔断的作用域 inspect/success 正常返回。
		if inspect, err := circuit.InspectClientIPErrorCircuit(ctx, gatewaypreauth.ClientIPErrorCircuitInput{
			SystemAccountID: "sys", ClientIP: "10.1.1.1",
		}); err != nil || inspect.Blocked {
			t.Fatalf("未知作用域=%+v err=%v", inspect, err)
		}
		if err := circuit.RecordClientIPErrorCircuitSuccess(ctx, gatewaypreauth.ClientIPErrorCircuitInput{
			SystemAccountID: "sys", ClientIP: "10.1.1.1",
		}); err != nil {
			t.Fatalf("未知作用域 success=%v", err)
		}
	})
}

func TestWIPolicyHitsPostgresChunksAndDefaultHitAt(t *testing.T) {
	// PG 真库（policysql_test.go 夹具）：超过 policyHitsChunkSize 的批次走
	// recordHitsPostgres 的 chunk 循环；hitAt 缺省回落批量时间戳（entries
	// 构建分支），hit_count 下限钳到 1。
	source, db := newPolicyPGSource(t)
	ctx := context.Background()
	hits := make([]PolicyHitInput, 0, policyHitsChunkSize+1)
	for i := 0; i < policyHitsChunkSize+1; i++ {
		hits = append(hits, PolicyHitInput{
			IPHash:   padLeft(i%10000) + strings.Repeat("a", 60),
			PolicyID: "p1",
			HitCount: 2,
		})
	}
	if err := source.RecordClientIPPolicyHits(ctx, hits); err != nil {
		t.Fatalf("chunk 写入失败: %v", err)
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM juhe_stats.client_ip_policy_hits`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != policyHitsChunkSize+1 {
		t.Fatalf("rows=%d want %d", rows, policyHitsChunkSize+1)
	}
	// hitAt 为空时回落批量时间戳（entries 构建分支）。
	if err := source.RecordClientIPPolicyHits(ctx, []PolicyHitInput{{
		IPHash: strings.Repeat("b", 64), PolicyID: "p2", HitCount: 0,
	}}); err != nil {
		t.Fatalf("hitAt 缺省分支失败: %v", err)
	}
	var hitCount int64
	var statDate, lastHitAt string
	if err := db.QueryRow(`SELECT hit_count, stat_date, last_hit_at FROM juhe_stats.client_ip_policy_hits WHERE policy_id = 'p2'`).Scan(&hitCount, &statDate, &lastHitAt); err != nil {
		t.Fatal(err)
	}
	if hitCount != 1 {
		t.Fatalf("hitCount=%d want 1", hitCount)
	}
	fixed := time.UnixMilli(1_000_000).UTC()
	if statDate != fixed.Format("2006-01-02") || lastHitAt != fixed.Format("2006-01-02T15:04:05.000Z") {
		t.Fatalf("缺省 hitAt 未回落批量时间戳: statDate=%s lastHitAt=%s", statDate, lastHitAt)
	}
}

func TestWIRecordHitsUpsertContract(t *testing.T) {
	// PG 方言 upsert：命中按 (ip_hash, stat_date, policy_id) 累加，last_hit_at
	// 取更晚者（生产同款 juhe_stats 夹具，见 newPolicyPGSource）。
	source, db := newPolicyPGSource(t)
	ctx := context.Background()
	hitAt := "2030-01-02T03:04:05Z"
	if err := source.RecordClientIPPolicyHits(ctx, []PolicyHitInput{
		{IPHash: strings.Repeat("a", 64), PolicyID: "p1", HitCount: 2, HitAt: hitAt},
		// 空字段的 hit 被跳过（normalize 守卫）。
		{IPHash: "", PolicyID: "p1", HitCount: 1},
	}); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if err := source.RecordClientIPPolicyHits(ctx, []PolicyHitInput{
		{IPHash: strings.Repeat("a", 64), PolicyID: "p1", HitCount: 3, HitAt: hitAt},
	}); err != nil {
		t.Fatalf("第二次写入失败: %v", err)
	}
	var hitCount int64
	if err := db.QueryRow(`SELECT hit_count FROM juhe_stats.client_ip_policy_hits WHERE policy_id = 'p1'`).Scan(&hitCount); err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if hitCount != 5 {
		t.Fatalf("hitCount=%d want 5", hitCount)
	}
	// 全部 hit 被跳过时无错返回。
	if err := source.RecordClientIPPolicyHits(ctx, []PolicyHitInput{{IPHash: " ", PolicyID: " "}}); err != nil {
		t.Fatalf("全跳过必须无错: %v", err)
	}
}

var _ = gatewayrouting.GatewayAccountModelPriority{}

// wiZCard 读取 miniredis ZSET 的成员数。
func wiZCard(t *testing.T, server *miniredis.Miniredis, key string) int {
	t.Helper()
	members, err := server.ZMembers(key)
	if err != nil {
		return 0
	}
	return len(members)
}
