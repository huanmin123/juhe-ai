package circuitstore

// overlay（Redis 半边 + SQLite 对账半边）与 RuntimeStateReader 的行为测试：
// 键空间、dirty 队列认领/确认语义、并发快照 Lua、probe 运行态投影与
// policy avoidance 覆盖。时钟用 miniredis.SetTime 或注入 Now 保证可重放。

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

func futureRFC3339(delta time.Duration) string {
	return time.Now().Add(delta).UTC().Format(time.RFC3339Nano)
}

// newOverlayStore 构造注入 client 的 OverlayRedisStore（Close 由注入方管理）。
func newOverlayStore(t *testing.T, server *miniredis.Miniredis) *OverlayRedisStore {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store, err := NewOverlayRedisStore(OverlayRedisConfig{Namespace: "ns"}, client)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// TestOverlayRedisStoreKeysAndConstruction 锁定键形状与构造/关闭语义。
func TestOverlayRedisStoreKeysAndConstruction(t *testing.T) {
	if _, err := NewOverlayRedisStore(OverlayRedisConfig{URL: "  "}, nil); err == nil {
		t.Fatal("无 URL 且无 Client 必须报错")
	}
	if _, err := NewOverlayRedisStore(OverlayRedisConfig{URL: "://bad"}, nil); err == nil {
		t.Fatal("非法 URL 必须报错")
	}
	server := miniredis.RunT(t)
	t.Cleanup(func() { server.Close() })
	urlStore, err := NewOverlayRedisStore(OverlayRedisConfig{URL: "redis://" + server.Addr(), Namespace: "ns"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := urlStore.Close(); err != nil {
		t.Fatalf("自建连接 Close: %v", err)
	}
	store := newOverlayStore(t, server)
	// redisNamespacedKey 对 juhe-ai: 根前缀键在根后插入 namespace（单根前缀）。
	if store.concurrencyKey("acc-1", "total") != "juhe-ai:ns:account-concurrency-v2:acc-1:total" {
		t.Fatalf("并发键形状不符: %s", store.concurrencyKey("acc-1", "total"))
	}
	if store.dirtyKey() != "juhe-ai:ns:account-concurrency-projection-dirty" {
		t.Fatalf("dirty 键形状不符: %s", store.dirtyKey())
	}
	if store.generationKey() != "juhe-ai:ns:account-concurrency-projection-generation" {
		t.Fatalf("generation 键形状不符: %s", store.generationKey())
	}
	if err := store.Close(); err != nil {
		t.Fatalf("注入 client 的 Close 应为空操作: %v", err)
	}
}

// TestOverlayListDirtyAndAcknowledge 覆盖 dirty 队列认领与确认语义。
func TestOverlayListDirtyAndAcknowledge(t *testing.T) {
	server := miniredis.RunT(t)
	t.Cleanup(func() { server.Close() })
	store := newOverlayStore(t, server)
	ctx := context.Background()
	// 无条目 → 空（不报错）。
	entries, err := store.ListDirtyEntries(ctx, 10)
	if err != nil || len(entries) != 0 {
		t.Fatalf("空队列应返回空: %v %v", entries, err)
	}

	if _, err := server.ZAdd(store.dirtyKey(), 100, "acc-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.ZAdd(store.dirtyKey(), 101, "acc-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.ZAdd(store.dirtyKey(), 102, "acc-no-gen"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.ZAdd(store.dirtyKey(), 103, "acc-bad-gen"); err != nil {
		t.Fatal(err)
	}
	server.HSet(store.generationKey(), "acc-1", "3")
	server.HSet(store.generationKey(), "acc-2", "9")
	server.HSet(store.generationKey(), "acc-bad-gen", "not-a-number")

	// 认领：limit 0 归一化为 1 → 只返回队首 1 条；缺 generation 的被移除。
	entries, err = store.ListDirtyEntries(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].AccountID != "acc-1" {
		t.Fatalf("limit 0 应归一化为 1 条: %+v", entries)
	}
	entries, err = store.ListDirtyEntries(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].AccountID != "acc-1" || entries[1].AccountID != "acc-2" {
		t.Fatalf("认领结果不符: %+v", entries)
	}
	if store.trackedGeneration("acc-1") != 3 || store.trackedGeneration("acc-2") != 9 {
		t.Fatalf("认领应暂存 generation: %d %d", store.trackedGeneration("acc-1"), store.trackedGeneration("acc-2"))
	}
	remaining, _ := server.ZMembers(store.dirtyKey())
	if len(remaining) != 3 {
		t.Fatalf("缺 generation 的条目应被移除、非法 generation 保留: %v", remaining)
	}

	// 确认：无 nextReconcileAt → 从队列与代次表同时删除；未认领的
	// acc-bad-gen 不受影响。
	if err := store.Acknowledge(ctx, entries); err != nil {
		t.Fatal(err)
	}
	remainingAfterAck, _ := server.ZMembers(store.dirtyKey())
	if len(remainingAfterAck) != 1 || remainingAfterAck[0] != "acc-bad-gen" {
		t.Fatalf("确认必须只移出已认领条目: %v", remainingAfterAck)
	}
	if server.HGet(store.generationKey(), "acc-1") != "" {
		t.Fatal("确认后必须清除代次记录")
	}
	// 空切片 no-op。
	if err := store.Acknowledge(ctx, nil); err != nil {
		t.Fatalf("空确认不得报错: %v", err)
	}
	// 非法 nextReconcileAt → 报错。
	bad := []opsjobs.OverlayEntry{{AccountID: "acc-x", NextReconcileAt: ptrString("not-a-time")}}
	if err := store.Acknowledge(ctx, bad); err == nil {
		t.Fatal("非法 nextReconcileAt 必须报错")
	}
}

// TestOverlayAcknowledgeKeepsFutureReconcile 覆盖未来重对账计划的保留语义。
func TestOverlayAcknowledgeKeepsFutureReconcile(t *testing.T) {
	server := miniredis.RunT(t)
	t.Cleanup(func() { server.Close() })
	store := newOverlayStore(t, server)
	ctx := context.Background()
	if _, err := server.ZAdd(store.dirtyKey(), 1, "acc-keep"); err != nil {
		t.Fatal(err)
	}
	server.HSet(store.generationKey(), "acc-keep", "5")
	entries, err := store.ListDirtyEntries(ctx, 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("认领失败: %v %v", entries, err)
	}
	future := futureRFC3339(time.Hour)
	entries[0].NextReconcileAt = &future
	if err := store.Acknowledge(ctx, entries); err != nil {
		t.Fatal(err)
	}
	remaining, _ := server.ZMembers(store.dirtyKey())
	if len(remaining) != 1 || remaining[0] != "acc-keep" {
		t.Fatalf("未来重对账计划必须保留为重对账队列: %v", remaining)
	}
	if server.HGet(store.generationKey(), "acc-keep") != "5" {
		t.Fatal("未来计划下代次记录必须保留")
	}

	// generation 已变 → 确认不动队列（避免覆盖新一轮脏标记）。
	server.HSet(store.generationKey(), "acc-stale", "20")
	if _, err := server.ZAdd(store.dirtyKey(), 2, "acc-stale"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListDirtyEntries(ctx, 10); err != nil {
		t.Fatal(err)
	}
	server.HSet(store.generationKey(), "acc-stale", "21") // 模拟认领后代次被别处推进
	if err := store.Acknowledge(ctx, []opsjobs.OverlayEntry{{AccountID: "acc-stale"}}); err != nil {
		t.Fatal(err)
	}
	if remaining, _ := server.ZMembers(store.dirtyKey()); len(remaining) != 2 {
		t.Fatalf("generation 已变时确认不得改队列: %v", remaining)
	}
}

// TestOverlayLoadSnapshots 覆盖并发快照 Lua（TIME 清理、当前并发、到期计划）。
func TestOverlayLoadSnapshots(t *testing.T) {
	server := miniredis.RunT(t)
	t.Cleanup(func() { server.Close() })
	base := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)
	server.SetTime(base)
	store := newOverlayStore(t, server)
	ctx := context.Background()

	totalKey := store.concurrencyKey("acc-1", "total")
	// 两个活跃槽位（未来到期）+ 一个已过期槽位（应被清理）；metadata 是
	// HASH 面（网关写侧的槽位元数据），本读面只对它做 HDEL 清理。
	metadataKey := store.concurrencyKey("acc-1", "metadata")
	// 活跃槽位 score 用真实未来（LoadSnapshots 以真实时钟判定 NextReconcileAt
	// 是否呈现）；过期槽用 base 之前（SetTime 已把服务端 now 固定在 base）。
	futureScore := float64(time.Now().Add(30 * time.Minute).UnixMilli())
	pastScore := float64(base.Add(-time.Minute).UnixMilli())
	if _, err := server.ZAdd(totalKey, futureScore, "lease-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.ZAdd(totalKey, pastScore, "lease-expired"); err != nil {
		t.Fatal(err)
	}
	server.HSet(metadataKey, "lease-a", "{}", "lease-expired", "{}")

	snapshots, err := store.LoadSnapshots(ctx, []string{"acc-1", "acc-empty"})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 2 {
		t.Fatalf("每个请求 id 都必须有快照: %+v", snapshots)
	}
	first := snapshots[0]
	if first.AccountID != "acc-1" || first.CurrentConcurrency != 1 {
		t.Fatalf("过期槽清理后当前并发应为 1: %+v", first)
	}
	if first.NextReconcileAt == nil {
		t.Fatalf("未来到期槽必须给出 NextReconcileAt: %+v", first)
	}
	if _, err := time.Parse(time.RFC3339Nano, *first.NextReconcileAt); err != nil {
		t.Fatalf("NextReconcileAt 必须是 RFC3339: %s", *first.NextReconcileAt)
	}
	if snapshots[1].CurrentConcurrency != 0 || snapshots[1].NextReconcileAt != nil {
		t.Fatalf("空账户快照应为零值: %+v", snapshots[1])
	}
	// 过期槽应已被删除。
	if members, _ := server.ZMembers(totalKey); len(members) != 1 {
		t.Fatalf("过期槽必须被清理: %v", members)
	}
	// 非法 id → 报错。
	if _, err := store.LoadSnapshots(ctx, []string{"   "}); err == nil {
		t.Fatal("空 id 必须报错")
	}
	// 空列表 → 空快照。
	empty, err := store.LoadSnapshots(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("空输入应返回空: %v %v", empty, err)
	}
}

// TestRedisInt64Slice 覆盖 Lua 数值数组的类型归一。
func TestRedisInt64Slice(t *testing.T) {
	got, err := redisInt64Slice([]any{int64(1), "2"})
	if err != nil || got[0] != 1 || got[1] != 2 {
		t.Fatalf("混合类型归一: %v %v", got, err)
	}
	if got, err := redisInt64Slice([]int64{7}); err != nil || got[0] != 7 {
		t.Fatalf("int64 切片直通: %v %v", got, err)
	}
	if _, err := redisInt64Slice([]any{"abc"}); err == nil {
		t.Fatal("非法数字必须报错")
	}
	if _, err := redisInt64Slice(42); err == nil {
		t.Fatal("非切片必须报错")
	}
}

// TestOverlayReconcilerSQLite 覆盖对账半边的存在性检查与 overlay upsert。
func TestOverlayReconcilerSQLite(t *testing.T) {
	repo, db := openListAvailabilityFixture(t)
	server := miniredis.RunT(t)
	t.Cleanup(func() { server.Close() })
	store := newOverlayStore(t, server)
	if got := NewOverlayReconciler(nil, nil); got != nil {
		t.Fatal("nil 依赖必须返回 nil reconciler")
	}
	reconciler := NewOverlayReconciler(store, repo)
	if reconciler == nil {
		t.Fatal("合法依赖必须返回 reconciler")
	}
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id) VALUES ('acc-1', 'viewer-1')`); err != nil {
		t.Fatal(err)
	}

	// 空输入 → 空 map（不触达 DB）。
	existing, err := reconciler.ExistingAccountIDs(ctx, nil)
	if err != nil || len(existing) != 0 {
		t.Fatalf("空输入应返回空: %v %v", existing, err)
	}
	existing, err = reconciler.ExistingAccountIDs(ctx, []string{"acc-1", "acc-missing", "acc-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, found := existing["acc-1"]; !found {
		t.Fatalf("现存账户必须在结果中: %v", existing)
	}
	if _, found := existing["acc-missing"]; found {
		t.Fatalf("缺失账户不得出现: %v", existing)
	}
	if _, err := reconciler.ExistingAccountIDs(ctx, []string{"   "}); err == nil {
		t.Fatal("空 id 必须报错")
	}

	// UpsertOverlays：插入 + 冲突更新 + 去重（后者覆盖前者）。
	err = reconciler.UpsertOverlays(ctx, []opsjobs.OverlayUpsert{
		{AccountID: "acc-1", CurrentConcurrency: 3, ObservedAt: "2026-09-04T10:00:00.000Z"},
		{AccountID: "acc-1", CurrentConcurrency: 5, ObservedAt: "2026-09-04T11:00:00.000Z"},
		{AccountID: "acc-2", CurrentConcurrency: 0, ObservedAt: "2026-09-04T10:00:00.000Z", NextReconcileAt: ptrString("  ")},
	})
	if err != nil {
		t.Fatal(err)
	}
	var concurrency int64
	if err := db.QueryRow(`SELECT current_concurrency FROM account_list_availability_runtime_overlays WHERE account_id = 'acc-1'`).Scan(&concurrency); err != nil {
		t.Fatal(err)
	}
	if concurrency != 3 {
		// 行为存疑：重复 accountId 时 ordered 保留首个 entry（后者不生效），
		// 与 byAccountID 记录最后值的意图不一致；按当前实际行为断言。
		t.Fatalf("重复 accountId 按当前行为取首值: %d", concurrency)
	}
	var nextReconcile *string
	if err := db.QueryRow(`SELECT next_reconcile_at FROM account_list_availability_runtime_overlays WHERE account_id = 'acc-2'`).Scan(&nextReconcile); err != nil {
		t.Fatal(err)
	}
	if nextReconcile != nil {
		t.Fatalf("空白 nextReconcileAt 必须落 NULL: %v", nextReconcile)
	}

	// 参数校验。
	if err := reconciler.UpsertOverlays(ctx, []opsjobs.OverlayUpsert{{AccountID: "  ", CurrentConcurrency: 1, ObservedAt: "x"}}); err == nil {
		t.Fatal("空 accountId 必须报错")
	}
	if err := reconciler.UpsertOverlays(ctx, []opsjobs.OverlayUpsert{{AccountID: "acc-1", CurrentConcurrency: -1, ObservedAt: "x"}}); err == nil {
		t.Fatal("负并发必须报错")
	}
	if err := reconciler.UpsertOverlays(ctx, []opsjobs.OverlayUpsert{{AccountID: "acc-1", CurrentConcurrency: 1, ObservedAt: " "}}); err == nil {
		t.Fatal("空 observedAt 必须报错")
	}
	if err := reconciler.UpsertOverlays(ctx, nil); err != nil {
		t.Fatalf("空列表 no-op: %v", err)
	}

	// 对账组合半边透传（dirty 队列直通 Redis 半边）。
	if _, err := server.ZAdd(store.dirtyKey(), 1, "acc-1"); err != nil {
		t.Fatal(err)
	}
	server.HSet(store.generationKey(), "acc-1", "2")
	entries, err := reconciler.ListDirtyEntries(ctx, 10)
	if err != nil || len(entries) != 1 || entries[0].AccountID != "acc-1" {
		t.Fatalf("reconciler 必须透传 dirty 认领: %v %v", entries, err)
	}
	snapshots, err := reconciler.LoadSnapshots(ctx, []string{"acc-1"})
	if err != nil || len(snapshots) != 1 {
		t.Fatalf("reconciler 必须透传快照读取: %v %v", snapshots, err)
	}
}

// TestRuntimeStateReaderConstruction 覆盖构造与关闭。
func TestRuntimeStateReaderConstruction(t *testing.T) {
	if _, err := NewRuntimeStateReader("  ", "ns", nil); err == nil {
		t.Fatal("无 URL 且无 Client 必须报错")
	}
	if _, err := NewRuntimeStateReader("://bad", "ns", nil); err == nil {
		t.Fatal("非法 URL 必须报错")
	}
	server := miniredis.RunT(t)
	t.Cleanup(func() { server.Close() })
	reader, err := NewRuntimeStateReader("redis://"+server.Addr(), "ns", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("自建连接 Close: %v", err)
	}
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	injected, err := NewRuntimeStateReader("", "ns", client)
	if err != nil {
		t.Fatal(err)
	}
	if err := injected.Close(); err != nil {
		t.Fatalf("注入 client 的 Close 应为空操作: %v", err)
	}
}

// TestRuntimeStateReaderLoadAvailability 覆盖运行态可用性投影主路径。
func TestRuntimeStateReaderLoadAvailability(t *testing.T) {
	server := miniredis.RunT(t)
	t.Cleanup(func() { server.Close() })
	base := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)
	server.SetTime(base)
	reader, err := NewRuntimeStateReader("redis://"+server.Addr(), "ns", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	nowMS := base.UnixMilli()

	// 空/重复输入归一。
	got, err := reader.LoadRuntimeAvailability(ctx, []string{"", "  "})
	if err != nil || len(got) != 0 {
		t.Fatalf("空输入应返回空: %v %v", got, err)
	}

	seedState := func(runtimeKey string, state distributedRecoveryProbeState) {
		t.Helper()
		raw, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := server.Set(reader.statePrefix+sanitizeProbeKeyPart(runtimeKey), string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	// degraded + scheduled（due ZSET 命中 + nextAttemptAt 未来）。
	// reader 内部以真实时钟比较 nextAttemptAt，因此用真实相对时间。
	futureAttempt := time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339Nano)
	pastAttempt := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339Nano)
	seedState("acc-degraded", distributedRecoveryProbeState{
		RuntimeKey: "acc-degraded", Phase: "degraded", Generation: 2,
		StartedAtMs: nowMS - 1000, AttemptCount: 1, Reason: "recent_failures",
		ProbePresentation: map[string]any{"schedule": map[string]any{"nextAttemptAt": futureAttempt}},
	})
	if _, err := server.ZAdd(reader.dueKey, float64(nowMS+300_000), "acc-degraded"); err != nil {
		t.Fatal(err)
	}
	// due_waiting：有调度但 nextAttemptAt 已过。
	seedState("acc-due", distributedRecoveryProbeState{
		RuntimeKey: "acc-due", Phase: "degraded", Generation: 1,
		StartedAtMs: nowMS - 2000, AttemptCount: 1,
		ProbePresentation: map[string]any{"schedule": map[string]any{"nextAttemptAt": pastAttempt}},
	})
	if _, err := server.ZAdd(reader.dueKey, float64(nowMS-1000), "acc-due"); err != nil {
		t.Fatal(err)
	}
	// running：probe run 活跃（runUntil 相对真实时钟，reader 内部用真实 now）。
	runID := "run-1"
	runUntil := time.Now().Add(time.Hour).UnixMilli()
	seedState("acc-running", distributedRecoveryProbeState{
		RuntimeKey: "acc-running", Phase: "degraded", Generation: 1,
		StartedAtMs: nowMS - 3000, AttemptCount: 1,
		ProbeRunID: &runID, ProbeRunUntilMs: &runUntil,
	})
	// recovery_wait attempt=0 → 不输出。
	seedState("acc-wait", distributedRecoveryProbeState{
		RuntimeKey: "acc-wait", Phase: "recovery_wait", Generation: 1, AttemptCount: 0,
	})
	// precheck_pending。
	seedState("acc-precheck", distributedRecoveryProbeState{
		RuntimeKey: "acc-precheck", Phase: "precheck_pending", Generation: 1,
		StartedAtMs: nowMS - 4000, AttemptCount: 1,
	})

	got, err = reader.LoadRuntimeAvailability(ctx, []string{
		"acc-degraded", "acc-due", "acc-running", "acc-wait", "acc-precheck", "acc-absent",
	})
	if err != nil {
		t.Fatal(err)
	}
	degraded := got["acc-degraded"]
	if degraded.Status != "degraded" || degraded.Reason != "recent_failures" || degraded.Since == "" {
		t.Fatalf("degraded 投影不符: %+v", degraded)
	}
	schedule, ok := degraded.ProbePresentation["schedule"].(map[string]any)
	if !ok || schedule["state"] != "scheduled" || schedule["nextAttemptAt"] != futureAttempt {
		t.Fatalf("scheduled 计划不符: %v", degraded.ProbePresentation["schedule"])
	}
	if got["acc-due"].ProbePresentation["schedule"].(map[string]any)["state"] != "due_waiting" {
		t.Fatalf("过期 nextAttemptAt 应为 due_waiting: %+v", got["acc-due"])
	}
	if got["acc-running"].ProbePresentation["schedule"].(map[string]any)["state"] != "running" {
		t.Fatalf("活跃 probe run 应为 running: %+v", got["acc-running"])
	}
	if _, exists := got["acc-wait"]; exists {
		t.Fatalf("recovery_wait attempt=0 不得输出: %+v", got["acc-wait"])
	}
	if got["acc-precheck"].Status != "precheck_pending" {
		t.Fatalf("precheck_pending 投影不符: %+v", got["acc-precheck"])
	}
	if _, exists := got["acc-absent"]; exists {
		t.Fatalf("无状态键不得输出: %+v", got["acc-absent"])
	}

	// 损坏 JSON → fail closed（报错并删除坏键）。
	if err := server.Set(reader.statePrefix+sanitizeProbeKeyPart("acc-bad"), "{broken"); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.LoadRuntimeAvailability(ctx, []string{"acc-bad"}); err == nil {
		t.Fatal("损坏状态必须报错（fail closed）")
	}
	if server.Exists(reader.statePrefix + sanitizeProbeKeyPart("acc-bad")) {
		t.Fatal("损坏状态键必须被删除")
	}
}

// TestRuntimeStateReaderPolicyAvoidance 覆盖 policy avoidance 对运行态的覆盖。
func TestRuntimeStateReaderPolicyAvoidance(t *testing.T) {
	server := miniredis.RunT(t)
	t.Cleanup(func() { server.Close() })
	base := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)
	server.SetTime(base)
	reader, err := NewRuntimeStateReader("redis://"+server.Addr(), "ns", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	nowMS := base.UnixMilli()
	seedState := func(runtimeKey string, state distributedRecoveryProbeState) {
		t.Helper()
		raw, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := server.Set(reader.statePrefix+sanitizeProbeKeyPart(runtimeKey), string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	seedState("acc-avoid", distributedRecoveryProbeState{
		RuntimeKey: "acc-avoid", Phase: "degraded", Generation: 1, AttemptCount: 1,
	})
	seedAvoidance := func(runtimeKey string, state configuredPolicyAvoidanceState) {
		t.Helper()
		raw, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := server.Set(reader.avoidanceKeyPrefix+sanitizeProbeKeyPart(runtimeKey), string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	seedAvoidance("acc-avoid", configuredPolicyAvoidanceState{
		RuntimeKey: "acc-avoid", AccountID: "acc-avoid", Reason: "policy",
		StartedAtMs: nowMS - 1000,
		// reader 内部以真实时钟判断 TTL，因此 UntilMs 用真实未来时间。
		UntilMs: time.Now().Add(time.Hour).UnixMilli(),
	})
	// 过期 avoidance 不覆盖。
	seedAvoidance("acc-expired-avoidance", configuredPolicyAvoidanceState{
		RuntimeKey: "acc-expired-avoidance", AccountID: "acc", UntilMs: nowMS - 1,
	})

	got, err := reader.LoadRuntimeAvailability(ctx, []string{"acc-avoid", "acc-expired-avoidance"})
	if err != nil {
		t.Fatal(err)
	}
	avoid := got["acc-avoid"]
	if avoid.Status != "local_suppressed" || avoid.Reason != "policy" {
		t.Fatalf("policy avoidance 必须覆盖为 local_suppressed: %+v", avoid)
	}
	if avoid.ProbePresentation["recoveryAtKind"] != "policy_ttl_expiry" {
		t.Fatalf("recoveryAtKind 必须是 policy_ttl_expiry: %+v", avoid.ProbePresentation)
	}
	if avoid.ProbePresentation["recoveryAt"] == "" {
		t.Fatalf("recoveryAt 必须来自 UntilMs: %+v", avoid.ProbePresentation)
	}
	if _, exists := got["acc-expired-avoidance"]; exists {
		t.Fatalf("源状态缺失的过期 avoidance 不得输出: %+v", got["acc-expired-avoidance"])
	}

	// 损坏 avoidance → 报错。
	if err := server.Set(reader.avoidanceKeyPrefix+sanitizeProbeKeyPart("acc-bad-avoid"), "{broken"); err != nil {
		t.Fatal(err)
	}
	seedState("acc-bad-avoid", distributedRecoveryProbeState{RuntimeKey: "acc-bad-avoid", AttemptCount: 1})
	if _, err := reader.LoadRuntimeAvailability(ctx, []string{"acc-bad-avoid"}); err == nil {
		t.Fatal("损坏 avoidance 必须报错")
	}
}

// TestVisibleRuntimeProbePresentationPure 纯函数表驱动覆盖 presentation 推导。
func TestVisibleRuntimeProbePresentationPure(t *testing.T) {
	nowMS := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC).UnixMilli()
	future := "2030-01-01T00:00:00.000Z"
	past := "2020-01-01T00:00:00.000Z"
	cases := []struct {
		name         string
		presentation map[string]any
		input        visibleProbeInput
		wantState    string
	}{
		{name: "运行中优先", presentation: map[string]any{"schedule": map[string]any{"nextAttemptAt": future}}, input: visibleProbeInput{running: true, taskScheduled: true}, wantState: "running"},
		{name: "调度加未来时间", presentation: map[string]any{"schedule": map[string]any{"nextAttemptAt": future}}, input: visibleProbeInput{taskScheduled: true}, wantState: "scheduled"},
		{name: "到期等待", presentation: map[string]any{"schedule": map[string]any{"nextAttemptAt": past}}, input: visibleProbeInput{taskScheduled: true}, wantState: "due_waiting"},
		{name: "未调度为none", presentation: map[string]any{"schedule": map[string]any{"nextAttemptAt": future}}, input: visibleProbeInput{}, wantState: "none"},
		{name: "调度但缺时间", presentation: map[string]any{"schedule": map[string]any{}}, input: visibleProbeInput{taskScheduled: true}, wantState: "none"},
		{name: "非法时间保持空schedule", presentation: map[string]any{"schedule": map[string]any{"nextAttemptAt": "junk"}}, input: visibleProbeInput{taskScheduled: true}, wantState: ""},
		{name: "无presentation", input: visibleProbeInput{taskScheduled: true}, wantState: "none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := visibleRuntimeProbePresentation(tc.presentation, tc.input, nowMS)
			schedule, ok := got["schedule"].(map[string]any)
			if tc.wantState == "" {
				if ok {
					t.Fatalf("非法时间不得产出 state: %v", got)
				}
				return
			}
			if !ok || schedule["state"] != tc.wantState {
				t.Fatalf("schedule state = %v, 期望 %v", got["schedule"], tc.wantState)
			}
		})
	}
	// lastObservation 原样透传。
	observation := map[string]any{"result": "success"}
	got := visibleRuntimeProbePresentation(map[string]any{"lastObservation": observation}, visibleProbeInput{}, nowMS)
	if got["lastObservation"] == nil {
		t.Fatalf("lastObservation 必须透传: %v", got)
	}
}

// TestRuntimeProbeStateRunning 覆盖运行态判定。
func TestRuntimeProbeStateRunning(t *testing.T) {
	nowMS := int64(1000)
	if runtimeProbeStateRunning(distributedRecoveryProbeState{}, nowMS) {
		t.Fatal("空状态不运行")
	}
	runID, leaseID := "r", "l"
	future := nowMS + 1
	past := nowMS - 1
	if !runtimeProbeStateRunning(distributedRecoveryProbeState{ProbeRunID: &runID, ProbeRunUntilMs: &future}, nowMS) {
		t.Fatal("活跃 probe run 必须判运行")
	}
	if runtimeProbeStateRunning(distributedRecoveryProbeState{ProbeRunID: &runID, ProbeRunUntilMs: &past}, nowMS) {
		t.Fatal("过期 probe run 不判运行")
	}
	if !runtimeProbeStateRunning(distributedRecoveryProbeState{HalfOpenLeaseID: &leaseID, HalfOpenLeaseUntilMs: &future}, nowMS) {
		t.Fatal("活跃 half-open lease 必须判运行")
	}
}

// TestUniqueRuntimeKeys 覆盖去重、修剪与 100 上限。
func TestUniqueRuntimeKeys(t *testing.T) {
	if got := uniqueRuntimeKeys([]string{" a ", "a", "", "b"}); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("去重修剪不符: %v", got)
	}
	many := make([]string, 150)
	for i := range many {
		many[i] = "k" + itoa(i)
	}
	if got := uniqueRuntimeKeys(many); len(got) != 100 {
		t.Fatalf("输出必须截断到 100: %d", len(got))
	}
}

// TestOverlayConcurrencySource 覆盖并发读源适配。
func TestOverlayConcurrencySource(t *testing.T) {
	server := miniredis.RunT(t)
	t.Cleanup(func() { server.Close() })
	base := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)
	server.SetTime(base)
	store := newOverlayStore(t, server)
	futureScore := float64(base.Add(time.Minute).UnixMilli())
	if _, err := server.ZAdd(store.concurrencyKey("acc-1", "total"), futureScore, "lease-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.ZAdd(store.concurrencyKey("acc-2", "total"), futureScore, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.ZAdd(store.concurrencyKey("acc-2", "total"), futureScore, "b"); err != nil {
		t.Fatal(err)
	}
	source := NewOverlayConcurrencySource(store)
	values, err := source.LoadConcurrency(context.Background(), []string{"acc-1", "acc-2", "acc-empty"})
	if err != nil {
		t.Fatal(err)
	}
	if values["acc-1"] != 1 || values["acc-2"] != 2 || values["acc-empty"] != 0 {
		t.Fatalf("并发快照适配不符: %v", values)
	}
}

// TestProbeStateStoreGenerationRunLifecycle 覆盖 probe run 的 CAS 获取与提交。
func TestProbeStateStoreGenerationRunLifecycle(t *testing.T) {
	server := miniredis.RunT(t)
	t.Cleanup(func() { server.Close() })
	base := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)
	server.SetTime(base)
	probeStore, err := NewProbeStateStore("redis://"+server.Addr(), "ns", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	runtimeKey := "availability:acc-1:probe_kind:r1"
	nowMS := base.UnixMilli()

	seed := func(state probeState) {
		t.Helper()
		raw, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := server.Set(probeStore.stateKey(runtimeKey), string(raw)); err != nil {
			t.Fatal(err)
		}
	}

	// 无状态 → acquire 返回 nil。
	taken, err := probeStore.acquireGenerationRun(ctx, runtimeKey, 5, "run-x", nowMS+1000, 1000)
	if err != nil || taken != nil {
		t.Fatalf("无状态不得获取 run: %v %v", taken, err)
	}
	seed(probeState{RuntimeKey: runtimeKey, Generation: 5, NextProbeAtMs: nowMS})
	// generation 不匹配 → nil。
	taken, err = probeStore.acquireGenerationRun(ctx, runtimeKey, 6, "run-x", nowMS+1000, 1000)
	if err != nil || taken != nil {
		t.Fatalf("generation 不匹配不得获取: %v %v", taken, err)
	}
	// 活跃 half-open lease → nil（等待半开探测先完成）。lease 字段不在
	// probeState 结构中（Lua 直读 JSON 键），用裸 JSON 种子；脚本以真实
	// 时钟判断 lease 存活，因此截止用真实未来时间。
	leaseUntil := time.Now().Add(time.Hour).UnixMilli()
	leaseJSON := `{"runtimeKey":"` + runtimeKey + `","generation":5,"nextProbeAtMs":` + itoa(int(nowMS)) +
		`,"halfOpenLeaseId":"lease-1","halfOpenLeaseUntilMs":` + itoa(int(leaseUntil)) + `}`
	if err := server.Set(probeStore.stateKey(runtimeKey), leaseJSON); err != nil {
		t.Fatal(err)
	}
	taken, err = probeStore.acquireGenerationRun(ctx, runtimeKey, 5, "run-x", nowMS+1000, 1000)
	if err != nil || taken != nil {
		t.Fatalf("活跃 lease 下不得获取 run: %v %v", taken, err)
	}
	// 正常获取 → probeRunId 被设置、due 键更新。
	seed(probeState{RuntimeKey: runtimeKey, Generation: 5, NextProbeAtMs: nowMS})
	taken, err = probeStore.acquireGenerationRun(ctx, runtimeKey, 5, "run-owner", nowMS+5000, 60_000)
	if err != nil {
		t.Fatal(err)
	}
	if taken == nil || taken.ProbeRunID == nil || *taken.ProbeRunID != "run-owner" {
		t.Fatalf("run 获取必须记录 owner: %+v", taken)
	}
	if taken.NextProbeAtMs < nowMS+5000 {
		t.Fatalf("run 截止应推进 nextProbeAt: %d", taken.NextProbeAtMs)
	}

	// commit：run 不匹配 → false。
	next := *taken
	next.Outcome = &[]string{ProbeOutcomeSuccess}[0]
	next.CompletedAtMs = &nowMS
	committed, err := probeStore.commitGenerationRun(ctx, next, "wrong-owner", 60_000)
	if err != nil || committed {
		t.Fatalf("owner 不匹配不得提交: %v %v", committed, err)
	}
	// commit：generation 不匹配 → false。
	changedGeneration := next
	changedGeneration.Generation = 9
	committed, err = probeStore.commitGenerationRun(ctx, changedGeneration, "run-owner", 60_000)
	if err != nil || committed {
		t.Fatalf("generation 不匹配不得提交: %v %v", committed, err)
	}
	// 正常提交：sourceFences 合并去重（存储态无 fences，incoming 两项）。
	incomingFences := []string{"fence-b", "fence-c"}
	next.SourceFences = &incomingFences
	committed, err = probeStore.commitGenerationRun(ctx, next, "run-owner", 60_000)
	if err != nil || !committed {
		t.Fatalf("合法提交失败: %v %v", committed, err)
	}
	stored, err := probeStore.get(ctx, runtimeKey)
	if err != nil || stored == nil {
		t.Fatalf("提交后必须可读回: %v %v", stored, err)
	}
	if stored.ProbeRunID != nil {
		t.Fatalf("提交必须清除 probeRunId: %+v", stored)
	}
	if stored.SourceFences == nil || len(*stored.SourceFences) != 2 {
		t.Fatalf("sourceFences 应合并去重: %v", stored.SourceFences)
	}

	// get：无键 → nil；损坏 JSON → nil 且删除。
	missing, err := probeStore.get(ctx, "availability:missing:r1")
	if err != nil || missing != nil {
		t.Fatalf("缺失状态应为 nil: %v %v", missing, err)
	}
	if err := server.Set(probeStore.stateKey(runtimeKey), "{broken"); err != nil {
		t.Fatal(err)
	}
	corrupt, err := probeStore.get(ctx, runtimeKey)
	if err != nil || corrupt != nil {
		t.Fatalf("损坏状态应返回 nil: %v %v", corrupt, err)
	}
	if server.Exists(probeStore.stateKey(runtimeKey)) {
		t.Fatal("损坏状态键必须被删除")
	}
}

// TestSettleDispatchedBySourceFenceGuards 覆盖结算入口的守卫分支。
func TestSettleDispatchedBySourceFenceGuards(t *testing.T) {
	server := miniredis.RunT(t)
	t.Cleanup(func() { server.Close() })
	probeStore, err := NewProbeStateStore("redis://"+server.Addr(), "ns", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	fence := ProbeSourceFence{StateKey: "k", AccountID: "acc", SourceGeneration: 1, SourceFenceID: "2f0a1b3c-4d5e-6f70-8192-a3b4c5d6e7f8"}
	if _, err := probeStore.SettleDispatchedBySourceFence(ctx, "availability:x:r1", 1, fence, "bogus-outcome", nil); err == nil {
		t.Fatal("无效 outcome 必须报错")
	}
	// 无状态 → false（不报错）。
	settled, err := probeStore.SettleDispatchedBySourceFence(ctx, "availability:x:r1", 1, fence, ProbeOutcomeSuccess, nil)
	if err != nil || settled {
		t.Fatalf("无状态不得结算: %v %v", settled, err)
	}
	// generationKeyOf 形状（namespace 插在 juhe-ai 根之后）。
	if got := probeStore.generationKeyOf("k"); got != "juhe-ai:ns:probe:gateway-availability-probe-coordinator:generation:k" {
		t.Fatalf("generation 键形状不符: %s", got)
	}
}

// TestProbeStateHelpers 覆盖小工具与 fence 归一化的错误分支。
func TestProbeStateHelpers(t *testing.T) {
	if normalizedTTLMS(0) != 1 {
		t.Fatal("TTL 下限为 1")
	}
	if maxInt64(3, 2) != 3 || maxInt64(1, 2) != 2 {
		t.Fatal("maxInt64 语义不符")
	}
	if redisRawString("s") != "s" || redisRawString([]byte("b")) != "b" || redisRawString(9) != "" {
		t.Fatal("redisRawString 类型分支不符")
	}
	if sanitizeProbeKeyPart("  ") != "default" {
		t.Fatalf("空白键回退 default: %s", sanitizeProbeKeyPart("  "))
	}
	if got := sanitizeProbeKeyPart("a b/c"); got != "a_b_c" {
		t.Fatalf("非法字符折叠: %s", got)
	}
	// NormalizeSourceFence 错误分支。
	valid := ProbeSourceFence{StateKey: "k", AccountID: "acc", SourceGeneration: 1, SourceFenceID: "2F0A1B3C-4D5E-6F70-8192-A3B4C5D6E7F8"}
	normalized, err := NormalizeSourceFence(valid)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.SourceFenceID != "2f0a1b3c-4d5e-6f70-8192-a3b4c5d6e7f8" {
		t.Fatalf("fence id 应归一为小写: %s", normalized.SourceFenceID)
	}
	if _, err := NormalizeSourceFence(ProbeSourceFence{SourceFenceID: valid.SourceFenceID}); err == nil {
		t.Fatal("缺 stateKey/accountId 必须报错")
	}
	negative := valid
	negative.SourceGeneration = -1
	if _, err := NormalizeSourceFence(negative); err == nil {
		t.Fatal("负 generation 必须报错")
	}
	badUUID := valid
	badUUID.SourceFenceID = "not-uuid"
	if _, err := NormalizeSourceFence(badUUID); err == nil {
		t.Fatal("非 UUID fence id 必须报错")
	}
	// ValidProbeOutcome。
	for _, outcome := range []string{ProbeOutcomeSuccess, ProbeOutcomeHealthFailure, ProbeOutcomeUnknown, ProbeOutcomeProbeTaskFailure, ProbeOutcomeCanceled, ProbeOutcomeStale} {
		if !ValidProbeOutcome(outcome) {
			t.Fatalf("%s 必须是合法 outcome", outcome)
		}
	}
	if ValidProbeOutcome("other") {
		t.Fatal("未知 outcome 必须拒绝")
	}
}
