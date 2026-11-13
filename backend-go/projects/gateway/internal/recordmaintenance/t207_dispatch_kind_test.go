package recordmaintenance

// T207 C1 投递端毒丸防线回归（pgtestkit 隔离 PG 库）：快照行 kind 在入队前
// 经共享白名单校验，非法 kind 直接拒绝不落行（ERROR 日志），合法 kind 正常
// 入库——与 jobs 消费端 retention 执行器同一集合。

import (
	"context"
	"testing"
)

// TestDispatchRejectsUnknownSnapshotKind：非法 kind 拒绝投递且不落行。
func TestDispatchRejectsUnknownSnapshotKind(t *testing.T) {
	dispatch, db := newDispatchPG(t)
	result := dispatch.EnqueueAccountUsageSnapshotUpsert(context.Background(), RecordMaintenanceSnapshotJob{
		AccountID: "acc-t207",
		Kind:      "totally_unknown_kind",
		Source:    "gateway_error",
		Snapshot:  map[string]any{"k": "v"},
		UpdatedAt: "2026-11-13T00:00:00.000Z",
	})
	if result.Queued {
		t.Fatalf("非法 kind 不得入队: %+v", result)
	}
	if result.DroppedReason != "worker_dispatch_failed" {
		t.Fatalf("droppedReason = %q want worker_dispatch_failed（Node 词汇保持）", result.DroppedReason)
	}
	// 非法 kind 在 ensureSchema 之前拒绝：表可能尚未创建（fresh 库），存在性
	// 归零即视为未落行。
	var tableExists bool
	if err := db.QueryRow(`SELECT to_regclass('juhe_dataset.record_maintenance_jobs') IS NOT NULL`).Scan(&tableExists); err != nil {
		t.Fatalf("probe table: %v", err)
	}
	if !tableExists {
		return
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM juhe_dataset.record_maintenance_jobs`).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("非法 kind 不得落行, count = %d", count)
	}
}

// TestDispatchAcceptsWhitelistedSnapshotKinds：白名单内两个 kind 均正常入队
// （与 jobs retention 执行器校验同一集合）。
func TestDispatchAcceptsWhitelistedSnapshotKinds(t *testing.T) {
	dispatch, db := newDispatchPG(t)
	for index, kind := range []string{"openai_codex", "anthropic_claude"} {
		result := dispatch.EnqueueAccountUsageSnapshotUpsert(context.Background(), RecordMaintenanceSnapshotJob{
			AccountID: "acc-t207-ok",
			Kind:      kind,
			Source:    "gateway_error",
			Snapshot:  map[string]any{"k": "v"},
			UpdatedAt: "2026-11-13T00:00:00.000Z",
		})
		if !result.Queued {
			t.Fatalf("kind %s 应入队: %+v", kind, result)
		}
		var stored string
		if err := db.QueryRow(`SELECT kind FROM juhe_dataset.record_maintenance_jobs WHERE account_id = $1 ORDER BY created_at LIMIT $2 OFFSET $3`,
			"acc-t207-ok", 1, index).Scan(&stored); err != nil {
			t.Fatalf("read kind row #%d: %v", index, err)
		}
		if stored != kind {
			t.Fatalf("落行 kind = %q want %q", stored, kind)
		}
	}
}
