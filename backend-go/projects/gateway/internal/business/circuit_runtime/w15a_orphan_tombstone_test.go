package circuitruntime

import (
	"context"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// ---------------------------------------------------------------------------
// w15a：无索引映射的过期 closed 墓碑必须可被 GC（问题-0306 生产事故复现）。
//
// 生产形态（DB2 juhe-ai:prod:account-circuit:gateway-account-circuit）：
// closed zset 残留 5 条 Node→Go 切换前（2026-10-08）的过期墓碑，其索引映射
// 在运行时索引重建（begin 只 DEL 四个索引 hash）后消失；cleanup_closed 对
// "有 closed 成员但 scope-runtime 无映射"返回 false，mutation 入口无条件
// 先跑 cleanup，导致所有 mutation（含 get）永久秒拒
// 'invalid account circuit runtime index'——模型检测全部失败，重建索引无法
// 治愈（重建不碰 closed）。
// ---------------------------------------------------------------------------

// w15aSeedOrphanTombstone 按 2026-10-11 生产事故形态播种：closed zset 过期
// 成员、无 states 字段、无任何索引映射。
func w15aSeedOrphanTombstone(t *testing.T, backfiller *AccountCircuitRuntimeIndexBackfiller, clock *w7bClock, member string) {
	t.Helper()
	expired := float64(clock.Now().Add(-time.Hour).UnixMilli())
	if err := backfiller.client.client.ZAdd(context.Background(), backfiller.keys.closed, goredis.Z{Score: expired, Member: member}).Err(); err != nil {
		t.Fatal(err)
	}
}

// w15aOrphanCount 返回 closed zset 剩余成员数。
func w15aOrphanCount(t *testing.T, backfiller *AccountCircuitRuntimeIndexBackfiller) int64 {
	t.Helper()
	count, err := backfiller.client.client.ZCard(context.Background(), backfiller.keys.closed).Result()
	if err != nil {
		t.Fatal(err)
	}
	return count
}

// 修复目标：过期孤儿墓碑被 cleanup 回收，mutation 正常执行，而不是永久拒绝。
func TestW15aExpiredOrphanTombstoneSelfHealsOnMutation(t *testing.T) {
	clock := newW7BClock()
	store, runtime := w7bReadyStore(t, clock, 8, time.Minute)
	backfiller, err := NewAccountCircuitRuntimeIndexBackfiller(store.client)
	if err != nil {
		t.Fatal(err)
	}
	w15aSeedOrphanTombstone(t, backfiller, clock, "w15a-orphan-tombstone")

	scope := w7bProtocolScope("acc_w15a_fresh", "bucket")
	state, err := runtime.GetGatewayAccountCircuit(context.Background(), GatewayAccountCircuitGetInput{AccountID: "acc_w15a_fresh", Scope: scope, Now: clock.Now()})
	if err != nil {
		t.Fatalf("孤儿过期墓碑必须被 cleanup 回收而不是拒绝所有 mutation: %v", err)
	}
	if state.Phase != GatewayAccountCircuitPhaseClosed {
		t.Fatalf("新鲜 scope 应返回 CLOSED 缺省态, got %s", state.Phase)
	}
	if remaining := w15aOrphanCount(t, backfiller); remaining != 0 {
		t.Fatalf("closed 残留 %d 条孤儿墓碑，未被回收", remaining)
	}

	// 回收后同一命名空间的后续 mutation 持续可用（含真实 suspect 写路径）。
	result, err := runtime.SuspectGatewayAccountCircuit(context.Background(), GatewayAccountCircuitSuspectInput{
		AccountID: "acc_w15a_fresh", Scope: scope, DispatchRevision: 1, TransitionID: "w15a-t1", Reason: "w15a", Now: clock.Now(),
	})
	w7bExpectPhase(t, result, err, GatewayAccountCircuitMutationApplied, GatewayAccountCircuitPhaseSuspect)
}

// 真实不一致（有索引映射但指向不完整）必须保持响亮失败，不得被静默回收。
func TestW15aInconsistentTombstoneStillFailsLoudly(t *testing.T) {
	clock := newW7BClock()
	store, runtime := w7bReadyStore(t, clock, 8, time.Minute)
	backfiller, err := NewAccountCircuitRuntimeIndexBackfiller(store.client)
	if err != nil {
		t.Fatal(err)
	}
	member := "w15a-inconsistent-scope"
	if err := store.client.client.HSet(context.Background(), backfiller.keys.scopeRuntime, member, "w15a-runtime").Err(); err != nil {
		t.Fatal(err)
	}
	w15aSeedOrphanTombstone(t, backfiller, clock, member)

	scope := w7bProtocolScope("acc_w15a_strict", "bucket")
	if _, err := runtime.GetGatewayAccountCircuit(context.Background(), GatewayAccountCircuitGetInput{AccountID: "acc_w15a_strict", Scope: scope, Now: clock.Now()}); err == nil {
		t.Fatal("索引映射存在但不完整的墓碑必须保持响亮失败")
	}
}

// 容量压力下最旧 closed 条目为孤儿墓碑时同样必须可回收，不得卡死新写入。
func TestW15aCapacityEvictReclaimsOrphanTombstone(t *testing.T) {
	clock := newW7BClock()
	store, runtime := w7bReadyStore(t, clock, 1, time.Hour)
	backfiller, err := NewAccountCircuitRuntimeIndexBackfiller(store.client)
	if err != nil {
		t.Fatal(err)
	}
	// 未过期孤儿墓碑占住 evict 位；capacity=1 的新 suspect 必须驱逐它成功。
	w15aSeedOrphanTombstone(t, backfiller, clock, "w15a-evict-orphan")
	if err := store.client.client.ZAdd(context.Background(), backfiller.keys.closed, goredis.Z{
		Score: float64(clock.Now().Add(time.Hour).UnixMilli()), Member: "w15a-evict-orphan",
	}).Err(); err != nil {
		t.Fatal(err)
	}
	// 先占满 states（capacity=1），让下一个新 scope 的 suspect 走 evict 路径。
	first := w7bProtocolScope("acc_w15a_cap0", "bucket")
	filled, err := runtime.SuspectGatewayAccountCircuit(context.Background(), GatewayAccountCircuitSuspectInput{
		AccountID: "acc_w15a_cap0", Scope: first, DispatchRevision: 1, TransitionID: "w15a-cap-t0", Reason: "w15a", Now: clock.Now(),
	})
	w7bExpectPhase(t, filled, err, GatewayAccountCircuitMutationApplied, GatewayAccountCircuitPhaseSuspect)

	scope := w7bProtocolScope("acc_w15a_cap", "bucket")
	result, err := runtime.SuspectGatewayAccountCircuit(context.Background(), GatewayAccountCircuitSuspectInput{
		AccountID: "acc_w15a_cap", Scope: scope, DispatchRevision: 1, TransitionID: "w15a-cap-t1", Reason: "w15a", Now: clock.Now(),
	})
	if err != nil {
		t.Fatalf("容量驱逐孤儿墓碑失败会永久卡死新写入: %v", err)
	}
	if result.Status != GatewayAccountCircuitMutationApplied {
		t.Fatalf("status = %s, 期望 applied", result.Status)
	}
}

// 索引重建必须回收无 states 来源的 closed/due 成员（形成路径根治）：
// 重建以 states/escalation 为唯一投影来源，无来源墓碑不可投影，保留会让
// 重建后的运行时再次中毒。
func TestW15aBackfillSweepsUnprojectableTombstones(t *testing.T) {
	clock := newW7BClock()
	store, _ := w7bReadyStore(t, clock, 8, time.Minute)
	backfiller, err := NewAccountCircuitRuntimeIndexBackfiller(store.client)
	if err != nil {
		t.Fatal(err)
	}
	w15aSeedOrphanTombstone(t, backfiller, clock, "w15a-rebuild-orphan")
	if err := store.client.client.ZAdd(context.Background(), backfiller.keys.due, goredis.Z{
		Score: float64(clock.Now().Add(-time.Minute).UnixMilli()), Member: "w15a-rebuild-orphan-due",
	}).Err(); err != nil {
		t.Fatal(err)
	}

	reader := DispatchRevisionReaderFunc(func(_ context.Context, _ GatewayAccountCircuitDispatchRevisionPageInput) (GatewayAccountCircuitDispatchRevisionPage, error) {
		return GatewayAccountCircuitDispatchRevisionPage{}, nil
	})
	if _, err := store.BackfillRuntimeIndex(context.Background(), GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "w15a-owner"}, reader); err != nil {
		t.Fatalf("重建失败: %v", err)
	}
	if remaining := w15aOrphanCount(t, backfiller); remaining != 0 {
		t.Fatalf("重建后 closed 残留 %d 条无来源墓碑", remaining)
	}
	dueCount, err := backfiller.client.client.ZCard(context.Background(), backfiller.keys.due).Result()
	if err != nil {
		t.Fatal(err)
	}
	if dueCount != 0 {
		t.Fatalf("重建后 due 残留 %d 条无来源成员", dueCount)
	}
}

// miniredis 直驱守卫：孤儿回收不改变正常 CLOSED 生命周期（未过期墓碑保留）。
func TestW15aUnexpiredTombstoneStaysUntilExpiry(t *testing.T) {
	clock := newW7BClock()
	store, runtime := w7bReadyStore(t, clock, 8, time.Minute)
	backfiller, err := NewAccountCircuitRuntimeIndexBackfiller(store.client)
	if err != nil {
		t.Fatal(err)
	}
	w15aSeedOrphanTombstone(t, backfiller, clock, "w15a-unexpired-orphan")
	if err := store.client.client.ZAdd(context.Background(), backfiller.keys.closed, goredis.Z{
		Score: float64(clock.Now().Add(time.Hour).UnixMilli()), Member: "w15a-unexpired-orphan",
	}).Err(); err != nil {
		t.Fatal(err)
	}

	scope := w7bProtocolScope("acc_w15a_live", "bucket")
	if _, err := runtime.GetGatewayAccountCircuit(context.Background(), GatewayAccountCircuitGetInput{AccountID: "acc_w15a_live", Scope: scope, Now: clock.Now()}); err != nil {
		t.Fatalf("未过期墓碑不应影响正常 mutation: %v", err)
	}
	if remaining := w15aOrphanCount(t, backfiller); remaining != 1 {
		t.Fatalf("未过期墓碑应保留至过期, closed 成员数 = %d", remaining)
	}
}
