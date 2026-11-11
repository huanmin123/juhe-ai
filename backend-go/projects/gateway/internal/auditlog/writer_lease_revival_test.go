package auditlog

// T203（F3 写租约复活链）回归覆盖：writer keeper（键
// f3-audit-log-persistence:<instanceID>）失租进入 terminal 后，producer 门禁
// 每条记录丢弃直到进程重启——supervisor 的 RunOwner 重启路径只覆盖 retention
// owner，写 keeper 没有复活路径。本文件验证导出 Revive 的三面契约（fake 单
// 测）与真实 PG 缺陷场景闭环（真实失租 → 门禁丢弃 → Revive → fence 递增 →
// 落库恢复 → 默认键行不被 writer 侧触碰）。
//
// fake 单测复用 owner_renew_grace_test.go 的 renewGraceFakeStore，不触碰真实
// 数据库；真 PG 测试走 pgkit_test.go 的 pgtestkit 门禁（JUHE_AI_TEST_EXTERNAL=1）。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
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
	if !strings.Contains(err.Error(), "已关闭") {
		t.Fatalf("拒绝错误必须说明 keeper 已关闭: %v", err)
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
	// openPostgresStoreConfig 产物不设 LeaseKey → 默认键，即 retention 侧。
	retentionStore, implementation, cfg := openPostgresStoreConfig(t)
	writerCfg := cfg
	writerCfg.InstanceID = "t203-writer"
	writerCfg.LeaseKey = LeaseKeyForInstance("t203-writer")
	writerStore, err := OpenStore(writerCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writerStore.Close() })
	writerImplementation := writerStore.(*sqlStore)

	ctx := context.Background()
	retentionKeeper, ok, err := StartLeaseKeeper(ctx, retentionStore, cfg.InstanceID, 2*time.Second, nil)
	if err != nil || !ok {
		t.Fatalf("start retention keeper: ok=%v err=%v", ok, err)
	}
	defer retentionKeeper.Close()
	writerKeeper, ok, err := StartLeaseKeeper(ctx, writerStore, writerCfg.InstanceID, 2*time.Second, nil)
	if err != nil || !ok {
		t.Fatalf("start writer keeper: ok=%v err=%v", ok, err)
	}
	defer writerKeeper.Close()

	// 反分裂基线：留存默认键行 owner/fence 快照（此刻只被 retention keeper
	// 获取过；writer 侧后续任何操作都不得改动该行）。
	var snapOwner string
	var snapFence int64
	if err := implementation.db.QueryRow(
		implementation.bind(`SELECT owner_id, fence_token FROM `+implementation.table("audit_log_owner_leases")+` WHERE lease_key=?`),
		DefaultLeaseKey).Scan(&snapOwner, &snapFence); err != nil {
		t.Fatal(err)
	}
	if held := retentionKeeper.Lease(); snapOwner != held.OwnerID || snapFence != held.FenceToken {
		t.Fatalf("默认键行必须与 retention keeper 一致: row=(%s,%d) keeper=%+v", snapOwner, snapFence, held)
	}

	producer := NewProducer(writerStore, writerKeeper, Config{OwnerLease: writerKeeper.TTL()}, nil)

	// 1) 健康期：producer 投一条，落库成功。
	first := producerTestInput("t203-revival-1")
	producer.Capture(first)
	waitAuditPersisted(t, writerImplementation, first.ID)

	// 2) 真实失租：writer 键行置过期 + rival 接管（fence 递增）；keeper 下一次
	//    续租 0 行 → 按既有语义终态 ErrOwnerLeaseLost，Lease() 归零。
	expired := time.Now().UTC().Add(-time.Minute)
	if _, err := writerImplementation.db.Exec(
		writerImplementation.bind(`UPDATE `+writerImplementation.table("audit_log_owner_leases")+` SET lease_until=? WHERE lease_key=? AND owner_id=?`),
		expired, writerCfg.LeaseKey, writerCfg.InstanceID); err != nil {
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

	// 3) 门禁丢弃：终态期间投入的记录不得落库（零租约续租/持久化守卫拒绝）。
	dropped := producerTestInput("t203-revival-2-dropped")
	producer.Capture(dropped)
	waitAuditAbsent(t, writerImplementation, dropped.ID)

	// 4) rival 释放行（lease_until=0 保留行，BUG-0317 契约）后 Revive：新
	//    fence 必须大于 rival 的 token，终态清空，Lost() 换新通道。
	if err := writerStore.ReleaseOwnerLease(ctx, rival); err != nil {
		t.Fatalf("rival release: %v", err)
	}
	if err := writerKeeper.Revive(ctx); err != nil {
		t.Fatalf("Revive: %v", err)
	}
	revived := writerKeeper.Lease()
	if revived.OwnerID != writerCfg.InstanceID || revived.FenceToken <= rival.FenceToken {
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
	third := producerTestInput("t203-revival-3-restored")
	producer.Capture(third)
	waitAuditPersisted(t, writerImplementation, third.ID)
	var gotTrace, gotPath string
	if err := writerImplementation.db.QueryRow(
		writerImplementation.bind(`SELECT trace_id, path FROM `+writerImplementation.table("audit_logs")+` WHERE id=?`),
		third.ID).Scan(&gotTrace, &gotPath); err != nil {
		t.Fatal(err)
	}
	if gotTrace != third.TraceID || gotPath != third.Path {
		t.Fatalf("复活后落库内容必须与投入一致: trace=%q path=%q want=(%q,%q)", gotTrace, gotPath, third.TraceID, third.Path)
	}

	// 6) 反分裂断言：默认键行 owner/fence 与快照一致（retention keeper 一直
	//    持有续租，writer 侧的失租/接管/复活全程未触碰该行）。
	var endOwner string
	var endFence int64
	if err := implementation.db.QueryRow(
		implementation.bind(`SELECT owner_id, fence_token FROM `+implementation.table("audit_log_owner_leases")+` WHERE lease_key=?`),
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

// waitAuditPersisted 轮询直到该 id 的审计行落库或超时（producer 异步持久化）。
func waitAuditPersisted(t *testing.T, implementation *sqlStore, id string) {
	t.Helper()
	if waitAuditCount(t, implementation, id, 10*time.Second) < 1 {
		t.Fatalf("producer record %s never persisted", id)
	}
}

// waitAuditAbsent 轮询确认该 id 的审计行在窗口内始终未落库（门禁丢弃生效）。
func waitAuditAbsent(t *testing.T, implementation *sqlStore, id string) {
	t.Helper()
	if waitAuditCount(t, implementation, id, 3*time.Second) != 0 {
		t.Fatalf("终态期间记录 %s 必须被门禁丢弃，不得落库", id)
	}
}

// waitAuditCount 在 window 窗口内轮询该 id 的行数：窗口结束时返回行数（期间
// 每次读到 >0 立即返回，供 persisted 断言提前结束）。
func waitAuditCount(t *testing.T, implementation *sqlStore, id string, window time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(window)
	for {
		var count int
		if err := implementation.db.QueryRow(
			`SELECT COUNT(*) FROM juhe_dataset.audit_logs WHERE id=$1`, id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			return count
		}
		if !time.Now().Before(deadline) {
			return count
		}
		time.Sleep(10 * time.Millisecond)
	}
}
