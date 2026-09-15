package tablemonitor

// w7d（tablemonitor 错误臂批次三）：SQLite store 错误臂（续租/写入/校验/
// bootstrap 重检）、retention 多批循环与 batch 参数守卫、schemaReady 缓存。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestW7DSQLiteRenewAndVerifyLeaseArms(t *testing.T) {
	cfg, store := w7dSQLiteFixture(t)
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), cfg.InstanceID, cfg.OwnerLease)
	if err != nil || !acquired {
		t.Fatalf("获取租约: acquired=%t err=%v", acquired, err)
	}
	_ = err
	_ = acquired

	// 错误 fence token → 续租未命中（false），校验失败（ErrOwnerLeaseLost）。
	if ok, err := store.RenewOwnerLease(context.Background(), OwnerLease{OwnerID: "ghost", FenceToken: 1}, time.Minute); err != nil || ok {
		t.Fatalf("错误 token 续租必须未命中: ok=%t err=%v", ok, err)
	}
	if err := store.verifyOwnerLease(context.Background(), nil, OwnerLease{}); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("空租约校验必须拒绝: %v", err)
	}

	// 底层库关闭 → 续租/写入/清理错误臂（先释放 fixture 租约再让 closed store 获取）。
	if err := store.ReleaseOwnerLease(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	closed, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	closedLease, acquired, err := closed.AcquireOwnerLease(context.Background(), cfg.InstanceID, cfg.OwnerLease)
	if err != nil || !acquired {
		t.Fatalf("closed fixture 获取租约: acquired=%t err=%v", acquired, err)
	}
	if err := closed.db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := closed.RenewOwnerLease(context.Background(), closedLease, time.Minute); err == nil {
		t.Fatal("关闭库续租必须报错")
	}
	if err := closed.WriteSample(context.Background(), closedLease, collectedSample{}); err == nil {
		t.Fatal("关闭库写入必须报错")
	}
	if _, err := closed.Cleanup(context.Background(), closedLease, time.Now(), 10); err == nil {
		t.Fatal("关闭库清理必须报错")
	}
}

func TestW7DCleanupUntilCompleteGuardsAndPendingCheck(t *testing.T) {
	cfg, store := w7dSQLiteFixture(t)
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), cfg.InstanceID, cfg.OwnerLease)
	if err != nil || !acquired {
		t.Fatalf("获取租约: %v", err)
	}

	// batch 参数守卫。
	if _, err := store.CleanupUntilComplete(context.Background(), lease, time.Now(), 0, 1); err == nil {
		t.Fatal("batchSize=0 必须拒绝")
	}
	if _, err := store.CleanupUntilComplete(context.Background(), lease, time.Now(), 10, 0); err == nil {
		t.Fatal("maxBatches=0 必须拒绝")
	}

	// 写入一批过期快照后：单批清完 → 正常返回。
	sample := collectedSample{databases: []DatabaseSnapshot{{
		Role: "business", Path: "fixture", SampledAt: time.Now().UTC().Add(-48 * time.Hour), PageSize: intPtr64(4096),
	}}}
	if err := store.WriteSample(context.Background(), lease, sample); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.CleanupUntilComplete(context.Background(), lease, time.Now().UTC(), 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("过期快照必须删除 1 行: %d", deleted)
	}
}

func TestW7DSchemaReadyCachedAfterFirstEnsure(t *testing.T) {
	cfg, store := w7dSQLiteFixture(t)
	// 首次 EnsureSchema 执行 bootstrap；随后 schemaReady 缓存直接返回。
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("缓存后的 schema 校验必须直接返回: %v", err)
	}
	if !store.schemaReady {
		t.Fatal("schemaReady 必须置位")
	}

	// 重建一个全新 store：schema 存在性检查 missing → bootstrap 建表。
	fresh, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if _, _, err := fresh.AcquireOwnerLease(context.Background(), cfg.InstanceID, cfg.OwnerLease); err != nil {
		t.Fatalf("fresh store 租约引导必须自建 schema: %v", err)
	}
}

func TestW7DRetentionLoopReportsRemainingAfterMaxBatches(t *testing.T) {
	cfg, store := w7dSQLiteFixture(t)
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), cfg.InstanceID, cfg.OwnerLease)
	if err != nil || !acquired {
		t.Fatalf("获取租约: %v", err)
	}
	// 写入 3 行过期快照，batchSize=1 且 maxBatches=2 → 触发“未清空”守卫。
	base := time.Now().UTC().Add(-48 * time.Hour)
	for index := 0; index < 3; index++ {
		sample := collectedSample{databases: []DatabaseSnapshot{{
			Role: "business", Path: "fixture", SampledAt: base.Add(time.Duration(index) * time.Hour), PageSize: intPtr64(4096),
		}}}
		if err := store.WriteSample(context.Background(), lease, sample); err != nil {
			t.Fatal(err)
		}
	}
	_, err = store.CleanupUntilComplete(context.Background(), lease, time.Now().UTC(), 1, 2)
	if err == nil || !strings.Contains(err.Error(), "拒绝静默遗漏") {
		t.Fatalf("多批未清空必须显式报错: %v", err)
	}
}
