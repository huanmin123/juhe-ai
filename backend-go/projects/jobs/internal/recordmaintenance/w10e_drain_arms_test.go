package recordmaintenance

import (
	"context"
	"errors"
	"testing"
)

// drain 循环的可达错误臂：上下文取消、普通任务 RunOnce 失败、快照任务段
// RunOnce 失败。均复用 mockRunner 注入失败 + SQLite 种子行。

func TestW10EDrainErrorArms(t *testing.T) {
	// 1) 上下文取消 → DrainOnce 返回 ctx.Err()（104-106）。
	{
		runner := &mockRunner{}
		drainer, store := newTestDrainer(t, runner)
		seedRow(t, store.db, "w10e-ctx", "non_business_data_cleanup", "2026-09-01T00:00:00.000Z", 10, 1, "2026-09-04T01:00:00.000Z")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := drainer.DrainOnce(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled ctx err=%v", err)
		}
	}

	// 2) 普通任务 RunOnce 失败 → runBatch 中止（139-144），行保留。
	{
		runner := &mockRunner{}
		drainer, store := newTestDrainer(t, runner)
		seedRow(t, store.db, "w10e-fail", "non_business_data_cleanup", "2026-09-01T00:00:00.000Z", 10, 1, "2026-09-04T01:00:00.000Z")
		runner.setFailure("w10e-fail", errors.New("w10e runner boom"))
		if _, err := drainer.DrainOnce(context.Background()); err == nil || err.Error() != "w10e runner boom" {
			t.Fatalf("runonce failure err=%v", err)
		}
		if pending := pendingCount(t, store); pending != 1 {
			t.Fatalf("失败行应保留，pending=%d", pending)
		}
	}

	// 3) 快照任务段 RunOnce 失败 → runSnapshotUpsertRun 中止（203-208）。
	{
		runner := &mockRunner{}
		drainer, store := newTestDrainer(t, runner)
		seedGatewaySnapshotRow(t, store.db, "w10e-snap", "acc-1", `{"accounts":[]}`, "2026-09-04T01:00:00.000Z")
		runner.setFailure("w10e-snap", errors.New("w10e snapshot boom"))
		if _, err := drainer.DrainOnce(context.Background()); err == nil || err.Error() != "w10e snapshot boom" {
			t.Fatalf("snapshot failure err=%v", err)
		}
		if pending := pendingCount(t, store); pending != 1 {
			t.Fatalf("快照失败行应保留，pending=%d", pending)
		}
	}
}
