// UpsertOverlays 行序确定性回归（2026-11-08 巡检实录根治）：调用方顺序来自
// Redis dirty 列表（非确定），两个并发批次以不同行序对同一账户集做多行
// upsert 会交叉加行锁死锁（生产 pg_stat_database.deadlocks 每 15-20 分钟
// 增长一次）。修复 = 批次去重后按 account_id 升序排序；本测试钉住「输入
// 顺序不影响落库语义」的行为等价性。
package circuitstore

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

func TestUpsertOverlaysInputOrderIndependent(t *testing.T) {
	repo, db := openListAvailabilityFixture(t)
	server := miniredis.RunT(t)
	t.Cleanup(func() { server.Close() })
	store := newOverlayStore(t, server)
	reconciler := NewOverlayReconciler(store, repo)
	ctx := context.Background()

	accounts := []string{"acc-order-b", "acc-order-a", "acc-order-c", "acc-order-d"}
	for _, id := range accounts {
		if _, err := db.Exec(`INSERT INTO juhe_business.accounts (id, system_account_id) VALUES ($1, 'viewer-order') ON CONFLICT (id) DO NOTHING`, id); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	concurrency := map[string]int{"acc-order-a": 1, "acc-order-b": 2, "acc-order-c": 3, "acc-order-d": 4}

	build := func(order []string) []opsjobs.OverlayUpsert {
		out := make([]opsjobs.OverlayUpsert, 0, len(order))
		for _, id := range order {
			out = append(out, opsjobs.OverlayUpsert{
				AccountID:          id,
				CurrentConcurrency: concurrency[id],
				ObservedAt:         "2026-11-08T12:00:00.000Z",
			})
		}
		return out
	}

	// 同一账户集两种相反顺序各写一次（写同值，终态必须一致；行序不同不得
	// 影响语义）。
	forward := build(accounts)
	reversed := build([]string{"acc-order-d", "acc-order-c", "acc-order-b", "acc-order-a"})
	if err := reconciler.UpsertOverlays(ctx, forward); err != nil {
		t.Fatalf("正序写入: %v", err)
	}
	if err := reconciler.UpsertOverlays(ctx, reversed); err != nil {
		t.Fatalf("反序写入: %v", err)
	}
	for id, wantI := range concurrency {
		want := int64(wantI)
		var got int64
		if err := db.QueryRow(`SELECT current_concurrency FROM juhe_business.account_list_availability_runtime_overlays WHERE account_id = $1`, id).Scan(&got); err != nil {
			t.Fatalf("读 %s: %v", id, err)
		}
		if got != want {
			t.Fatalf("%s concurrency = %d, want %d（输入顺序不得影响落库）", id, got, want)
		}
	}
}
