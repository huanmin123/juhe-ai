package auditlog

// w13g 波次测试：W13 生产复核（2026-10-24）确认的第二条空壳路径——
// persistOne 在「续租失败」与「store.Persist 失败」两个 early-return 分支
// 只告警不撤回，已写入的 in_progress 信封行永久残留。生产证据：13:16-13:17
// 五条 `F3 审计采集持久化失败`（context deadline exceeded）+ 一条
// `F3 owner lease renewal failed; dropping audit capture`，与空壳
// auditLogID 一一对应，同窗口 retract 日志命中 0。本波测试锁定修复契约：
// 两条失败分支必须按 Cancel() 同口径派发信封撤回；Ignored（重复/迟到）
// 不得触发撤回。

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// TestW13GPersistFailureRetractsEnvelopeRow 用真实 PG 存储（pgtestkit 独立
// 库，与生产同 PG 臂）复现生产序列：信封写入成功 → 终态 Persist 失败（模拟
// blob 锁/元数据超时）→ 信封必须被撤回删除，不得残留 in_progress 空壳。
func TestW13GPersistFailureRetractsEnvelopeRow(t *testing.T) {
	store, lease := w13fStore(t)
	flaky := &w13gFailFinalizeStore{Store: store}
	producer := NewProducer(flaky, StaticLease(lease), Config{OwnerLease: 30 * time.Second}, nil)

	const id = "w13g-e2e-envelope"
	producer.Capture(fixture(id, LifecycleInProgress))
	w13gWaitForRowStatus(t, store, id, "in_progress")

	flaky.failFinalize.Store(true)
	producer.Capture(fixture(id, LifecycleFinalized))

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM juhe_dataset.audit_logs WHERE id = $1`, id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			return // 信封已撤回，空壳未残留
		}
		time.Sleep(10 * time.Millisecond)
	}
	var status string
	if err := store.db.QueryRow(`SELECT lifecycle_status FROM juhe_dataset.audit_logs WHERE id = $1`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	t.Fatalf("终态持久化失败后必须撤回 in_progress 信封，实际残留 lifecycle_status=%s", status)
}

// TestW13GPersistErrorDispatchesRetract 锁定 Persist 失败分支的撤回派发。
func TestW13GPersistErrorDispatchesRetract(t *testing.T) {
	fake := &fakeStore{renewOK: true, persistErr: errors.New("解析已存在 audit blob 元数据失败: context deadline exceeded")}
	producer := NewProducer(fake, StaticLease(OwnerLease{OwnerID: "owner-1", FenceToken: 7}), Config{OwnerLease: 30 * time.Second}, nil)
	producer.Capture(producerTestInput("w13g-persist-err"))
	// persist 路径 renew+persist 两次；修复后追加撤回路径 renew+retract 两次。
	fake.waitForCalls(t, 4)
	got := fake.retractions()
	if len(got) != 1 || got[0] != "w13g-persist-err" {
		t.Fatalf("持久化失败必须按 Cancel 同口径派发信封撤回: %v", got)
	}
}

// TestW13GRenewFailureDispatchesRetract 锁定续租失败分支的撤回派发：首次
// 续约超时（生产 13:16 形态），撤回 worker 的续约随后恢复，删除必须执行。
func TestW13GRenewFailureDispatchesRetract(t *testing.T) {
	fake := &fakeStore{renewOK: true, renewFailFirst: 1}
	producer := NewProducer(fake, StaticLease(OwnerLease{OwnerID: "owner-1", FenceToken: 7}), Config{OwnerLease: 30 * time.Second}, nil)
	producer.Capture(producerTestInput("w13g-renew-err"))
	// persist 路径 renew 失败一次即止；修复后追加撤回路径 renew+retract 两次。
	fake.waitForCalls(t, 3)
	got := fake.retractions()
	if len(got) != 1 || got[0] != "w13g-renew-err" {
		t.Fatalf("续租失败丢弃采集时必须派发信封撤回: %v", got)
	}
	if renews, persists, _ := fake.counts(); persists != 0 {
		t.Fatalf("续租失败不得触达 Persist: renews=%d persists=%d", renews, persists)
	}
}

// TestW13GIgnoredPersistDoesNotRetract 钉死边界：重复/迟到输入（Ignored）
// 表示行已是终态或更新版本已写入，撤回既无必要也有误删风险面，不得派发。
func TestW13GIgnoredPersistDoesNotRetract(t *testing.T) {
	fake := &fakeStore{renewOK: true, persistIgn: true}
	producer := NewProducer(fake, StaticLease(OwnerLease{OwnerID: "owner-1", FenceToken: 7}), Config{OwnerLease: 30 * time.Second}, nil)
	producer.Capture(producerTestInput("w13g-ignored"))
	fake.waitForCalls(t, 2) // renew+persist；Ignored 分支在 hot-search 前返回
	// 撤回若被错误派发会经其他 worker 异步落账，短稳定窗消除竞态窗口。
	time.Sleep(50 * time.Millisecond)
	if got := fake.retractions(); len(got) != 0 {
		t.Fatalf("迟到/重复输入不得触发撤回: %v", got)
	}
}

// w13gFailFinalizeStore 只在开关打开后对 finalized 输入注入 Persist 失败，
// 复现生产 13:16 的终态写入超时（信封已落库、终态事务回滚）。
type w13gFailFinalizeStore struct {
	Store
	failFinalize atomic.Bool
}

func (s *w13gFailFinalizeStore) Persist(ctx context.Context, lease OwnerLease, input AuditLogInput) (PersistResult, error) {
	if input.LifecycleStatus == LifecycleFinalized && s.failFinalize.Load() {
		return PersistResult{}, errors.New("解析已存在 audit blob 元数据失败: context deadline exceeded")
	}
	return s.Store.Persist(ctx, lease, input)
}

func w13gWaitForRowStatus(t *testing.T, store *sqlStore, id string, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		err := store.db.QueryRow(`SELECT lifecycle_status FROM juhe_dataset.audit_logs WHERE id = $1`, id).Scan(&status)
		if err == sql.ErrNoRows {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if status == want {
			return
		}
	}
	t.Fatalf("等待 audit_logs 行 %s 进入 %s 超时", id, want)
}
