package operationlog

// T203（F4 写租约复活链）回归覆盖：writer keeper（键
// f4-operation-log-persistence:<instanceID>）失租进入 terminal 后，producer
// 门禁每条记录丢弃直到进程重启——supervisor 的 RunOwner 重启路径只覆盖
// retention owner，写 keeper 没有复活路径。本文件验证导出 Revive 的三面契约
// （fake 单测）与真实 PG 缺陷场景闭环（真实失租 → 门禁丢弃 → Revive →
// fence 递增 → 落库恢复 → 默认键行不被 writer 侧触碰）。
//
// fake 单测复用 lease_keeper_renew_grace_test.go 的 renewGraceFakeStore，不触
// 碰真实数据库；真 PG 测试走 pgkit_test.go 的 pgtestkit 门禁
// （JUHE_AI_TEST_EXTERNAL=1）。

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// TestLeaseKeeperReviveIsNoOpOnHealthyKeeper 覆盖 T203 契约 a) 的健康臂：
// healthy keeper 上 Revive 必须直接返回 nil（reacquire 对 LostError()==nil
// 的 keeper 是 no-op），不产生新的 AcquireOwnerLease，也不改变存活状态。
func TestLeaseKeeperReviveIsNoOpOnHealthyKeeper(t *testing.T) {
	store := &renewGraceFakeStore{}
	keeper := startRenewGraceKeeper(t, store)
	defer keeper.Close()

	if err := keeper.Revive(context.Background()); err != nil {
		t.Fatalf("healthy keeper 上 Revive 必须 no-op 成功: %v", err)
	}
	store.mu.Lock()
	acquires := store.acquireCalls
	store.mu.Unlock()
	if acquires != 1 {
		t.Fatalf("healthy keeper 上 Revive 不得重新获取租约: acquires=%d", acquires)
	}
	if err := keeper.LostError(); err != nil {
		t.Fatalf("healthy keeper 不得报告丢失: %v", err)
	}
	select {
	case <-keeper.Lost():
		t.Fatal("healthy keeper 的 Lost 通道不得关闭")
	default:
	}
}

// TestLeaseKeeperReviveRebuildsLeaseAfterTerminalLoss 覆盖 T203 契约 a) 的
// terminal 臂：0 行更新进入终态后，Revive 必须重新 AcquireOwnerLease（新
// fence token）、清空 LostError 并换新 Lost() 通道（旧通道保持已关闭）。
func TestLeaseKeeperReviveRebuildsLeaseAfterTerminalLoss(t *testing.T) {
	store := &renewGraceFakeStore{renewFunc: func(int) (bool, error) {
		return false, nil // 0 行更新 → 立即终态
	}}
	keeper := startRenewGraceKeeper(t, store)
	defer keeper.Close()
	select {
	case <-keeper.Lost():
	case <-time.After(3 * time.Second):
		t.Fatal("0 行更新后未放弃所有权")
	}
	lostBefore := keeper.Lost()
	select {
	case <-lostBefore:
	default:
		t.Fatal("终态后旧 Lost 通道应已关闭")
	}
	if got := keeper.Lease(); got.OwnerID != "" || got.FenceToken != 0 {
		t.Fatalf("终态后 Lease() 必须为零租约: %+v", got)
	}

	// Revive 前放行续租成功，避免新续租循环首拍再次终态干扰断言。
	store.mu.Lock()
	store.renewFunc = nil
	store.mu.Unlock()

	if err := keeper.Revive(context.Background()); err != nil {
		t.Fatalf("terminal keeper 上 Revive 必须成功: %v", err)
	}
	revived := keeper.Lease()
	if revived.OwnerID == "" || revived.FenceToken == 0 {
		t.Fatalf("复活后必须持有真实租约: %+v", revived)
	}
	store.mu.Lock()
	acquires := store.acquireCalls
	tokens := append([]int64(nil), store.fenceTokens...)
	store.mu.Unlock()
	if acquires != 2 || revived.FenceToken != 2 {
		t.Fatalf("复活必须恰好重新获取一次租约且 fence 递增: acquires=%d tokens=%v lease=%+v", acquires, tokens, revived)
	}
	if err := keeper.LostError(); err != nil {
		t.Fatalf("复活后终态必须清空: %v", err)
	}
	lostAfter := keeper.Lost()
	if lostAfter == lostBefore {
		t.Fatal("复活必须更换 Lost() 通道，观察者才能看到新代际")
	}
	select {
	case <-lostAfter:
		t.Fatal("复活后新 Lost() 通道不得关闭")
	default:
	}
}

// TestLeaseKeeperReviveRefusedAfterClose 覆盖 T203 契约 a) 的 closed 臂：
// Close 之后 Revive 必须拒绝——Close 已释放或放弃租约行，重建出的租约无法
// 再被释放；且拒绝路径不得触碰 store（不产生新 acquire）。
func TestLeaseKeeperReviveRefusedAfterClose(t *testing.T) {
	store := &renewGraceFakeStore{}
	keeper := startRenewGraceKeeper(t, store)
	keeper.Close()

	err := keeper.Revive(context.Background())
	if err == nil {
		t.Fatal("Close 后 Revive 必须返回错误")
	}
	if !errors.Is(err, ErrKeeperClosed) {
		t.Fatalf("拒绝错误必须是 ErrKeeperClosed 哨兵（BUG-0360）: %v", err)
	}
	store.mu.Lock()
	acquires := store.acquireCalls
	store.mu.Unlock()
	if acquires != 1 {
		t.Fatalf("Close 后 Revive 不得触碰 store: acquires=%d", acquires)
	}
}

// TestWriterLeaseRevivalRestoresProducerGateAfterRealLoss 是 T203 生产缺陷
// 的真实 PG 回归：双 store 装配复刻生产形态（writer 键
// LeaseKeyForInstance("t203-writer")，retention 用默认键），走完整缺陷链——
// 真实失租（行过期 + rival 接管）→ producer 门禁丢弃 → rival 释放 →
// Revive（fence 递增）→ producer 落库恢复且内容一致 → 全程默认键行的
// owner/fence 不被 writer 侧任何操作改动（反分裂）。
func TestWriterLeaseRevivalRestoresProducerGateAfterRealLoss(t *testing.T) {
	testDB := pgtestkit.NewTestDatabase(t, prepareBusinessFixture("365"))
	// retention 侧不设 LeaseKey → 默认键；writer 侧显式按实例派生键。
	retentionStore, err := OpenStore(Config{Enabled: true, InstanceID: "t203-retention", PostgresURL: testDB.DSN})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = retentionStore.Close() })
	writerKey := LeaseKeyForInstance("t203-writer")
	writerStore, err := OpenStore(Config{Enabled: true, InstanceID: "t203-writer", PostgresURL: testDB.DSN, LeaseKey: writerKey})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writerStore.Close() })
	writerImplementation := writerStore.(*sqlStore)

	ctx := context.Background()
	retentionKeeper, ok, err := StartLeaseKeeper(ctx, retentionStore, "t203-retention", 2*time.Second, nil)
	if err != nil || !ok {
		t.Fatalf("start retention keeper: ok=%v err=%v", ok, err)
	}
	defer retentionKeeper.Close()
	writerKeeper, ok, err := StartLeaseKeeper(ctx, writerStore, "t203-writer", 2*time.Second, nil)
	if err != nil || !ok {
		t.Fatalf("start writer keeper: ok=%v err=%v", ok, err)
	}
	defer writerKeeper.Close()

	// 反分裂基线：留存默认键行 owner/fence 快照（此刻只被 retention keeper
	// 获取过；writer 侧后续任何操作都不得改动该行）。
	var snapOwner string
	var snapFence int64
	if err := writerImplementation.db.QueryRow(
		`SELECT owner_id, fence_token FROM juhe_dataset.operation_log_owner_leases WHERE lease_key=$1`,
		DefaultLeaseKey).Scan(&snapOwner, &snapFence); err != nil {
		t.Fatal(err)
	}
	if held := retentionKeeper.Lease(); snapOwner != held.OwnerID || snapFence != held.FenceToken {
		t.Fatalf("默认键行必须与 retention keeper 一致: row=(%s,%d) keeper=%+v", snapOwner, snapFence, held)
	}

	producer := NewProducer(writerStore, writerKeeper, Config{OwnerLease: writerKeeper.TTL()}, nil)

	// 1) 健康期：producer 投一条，落库成功。
	first := Input{ID: "t203-revival-1", ActorSystemAccountID: "actor-1", ActorRole: "admin", Module: "accounts", Action: "update", OperationKey: "accounts.update", ResourceType: "account", Summary: "before loss", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	producer.Record(first)
	waitKeeperPersisted(t, writerStore, first.ID)

	// 2) 真实失租：writer 键行置过期 + rival 接管（fence 递增）；keeper 下一次
	//    续租 0 行 → 按既有语义终态 ErrOwnerLeaseLost，Lease() 归零。
	expired := storageTime(time.Now().UTC().Add(-time.Minute))
	if _, err := writerImplementation.db.Exec(
		`UPDATE juhe_dataset.operation_log_owner_leases SET lease_until=$1 WHERE lease_key=$2 AND owner_id=$3`,
		expired, writerKey, "t203-writer"); err != nil {
		t.Fatal(err)
	}
	rival, ok, err := writerStore.AcquireOwnerLease(ctx, "t203-rival", time.Minute)
	if err != nil || !ok {
		t.Fatalf("rival takeover: ok=%v err=%v", ok, err)
	}
	select {
	case <-writerKeeper.Lost():
		if !errors.Is(writerKeeper.LostError(), ErrOwnerLeaseLost) {
			t.Fatalf("真实失租必须以 ErrOwnerLeaseLost 终态: %v", writerKeeper.LostError())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("租约被接管后 Lost 未关闭")
	}
	if got := writerKeeper.Lease(); got.OwnerID != "" || got.FenceToken != 0 {
		t.Fatalf("终态后 Lease() 必须为零租约: %+v", got)
	}

	// 3) 门禁丢弃：终态期间投入的记录不得落库（零租约续租被守卫拒绝）。
	dropped := Input{ID: "t203-revival-2-dropped", ActorSystemAccountID: "actor-1", ActorRole: "admin", Module: "accounts", Action: "delete", OperationKey: "accounts.delete", ResourceType: "account", Summary: "dropped while lost", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	producer.Record(dropped)
	waitOperationAbsent(t, writerStore, dropped.ID)

	// 4) rival 释放行（lease_until=0 保留行，BUG-0317 契约）后 Revive：新
	//    fence 必须大于 rival 的 token，终态清空，Lost() 换新通道。
	if err := writerStore.ReleaseOwnerLease(ctx, rival); err != nil {
		t.Fatalf("rival release: %v", err)
	}
	if err := writerKeeper.Revive(ctx); err != nil {
		t.Fatalf("Revive: %v", err)
	}
	revived := writerKeeper.Lease()
	if revived.OwnerID != "t203-writer" || revived.FenceToken <= rival.FenceToken {
		t.Fatalf("复活必须持递增 fence: revived=%+v rivalFence=%d", revived, rival.FenceToken)
	}
	if err := writerKeeper.LostError(); err != nil {
		t.Fatalf("复活后终态必须清空: %v", err)
	}
	select {
	case <-writerKeeper.Lost():
		t.Fatal("复活后新 Lost() 通道不得关闭")
	default:
	}

	// 5) 落库恢复：producer 再投一条，必须以新 fence 落库且内容与投入一致。
	third := Input{ID: "t203-revival-3-restored", ActorSystemAccountID: "actor-1", ActorRole: "admin", Module: "groups", Action: "update", OperationKey: "groups.update", ResourceType: "group", Summary: "restored after revival", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	producer.Record(third)
	result := waitKeeperPersisted(t, writerStore, third.ID)
	for _, item := range result.Items {
		if item.ID == third.ID && item.Summary != third.Summary {
			t.Fatalf("复活后落库内容必须与投入一致: summary=%q want=%q", item.Summary, third.Summary)
		}
	}

	// 6) 反分裂断言：默认键行 owner/fence 与快照一致（retention keeper 一直
	//    持有续租，writer 侧的失租/接管/复活全程未触碰该行）。
	var endOwner string
	var endFence int64
	if err := writerImplementation.db.QueryRow(
		`SELECT owner_id, fence_token FROM juhe_dataset.operation_log_owner_leases WHERE lease_key=$1`,
		DefaultLeaseKey).Scan(&endOwner, &endFence); err != nil {
		t.Fatal(err)
	}
	if endOwner != snapOwner || endFence != snapFence {
		t.Fatalf("默认键行不得被 writer 侧改动: snapshot=(%s,%d) end=(%s,%d)", snapOwner, snapFence, endOwner, endFence)
	}
	if held := retentionKeeper.Lease(); endOwner != held.OwnerID || endFence != held.FenceToken {
		t.Fatalf("默认键行必须仍与 retention keeper 一致: row=(%s,%d) keeper=%+v", endOwner, endFence, held)
	}
}

// waitOperationAbsent 轮询确认该 id 的操作日志在窗口内始终未落库（门禁丢弃
// 生效）；窗口结束仍不存在即通过，读到即失败。
func waitOperationAbsent(t *testing.T, store Store, id string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		result, err := store.List(context.Background(), ListOptions{})
		if err != nil {
			t.Fatalf("list persisted logs: %v", err)
		}
		for _, item := range result.Items {
			if item.ID == id {
				t.Fatalf("终态期间记录 %s 必须被门禁丢弃，不得落库", id)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
}
