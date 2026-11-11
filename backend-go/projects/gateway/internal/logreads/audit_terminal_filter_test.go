package logreads

// audit_terminal_filter_test.go 是 F3 终态唯一写入的读面回归（2026-11-11）：
// 列表/详情/按 ID 三族审计读面恒过滤 lifecycle_status='finalized'，历史残留
// 的 in_progress 行不再对管理面可见。真库经 pgtestkit 范式
// （newLogreadsPGDatabase，JUHE_AI_TEST_EXTERNAL=1 门禁统一执行）。

import (
	"context"
	"testing"
)

// TestAuditReadsFilterToFinalizedLifecycle 固定读面过滤：同一批 seed 中
// finalized 行可见、手工残留的 in_progress 行在 List / Detail / ListByID
// 三族读面上均不可见。
func TestAuditReadsFilterToFinalizedLifecycle(t *testing.T) {
	db := newLogreadsPGDatabase(t)
	reader, err := NewAuditLogQueryReader(db, AuditQueryDirectories{
		HotSearchDirectory:   t.TempDir(),
		PayloadBlobDirectory: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// finalized 行：正常终态，读面必须返回。
	db.ExecContext(ctx, fixtureDatasetSQL(`INSERT INTO audit_logs (id, trace_id, traffic_source, method, path, audit_outcome, success,
		sample_bucket, sample_reason, capture_status, lifecycle_status, started_at, ended_at, created_at)
		VALUES ('terminal-finalized', 'trace-terminal-ok', 'gateway', 'POST', '/v1/chat/completions', 'success', true,
		1, 'sampled', 'complete', 'finalized', '2026-11-11T10:00:00.000Z', '2026-11-11T10:00:00.100Z', '2026-11-11T10:00:01.000Z')`))
	// in_progress 行：模拟历史残留占位（绕过写入面手工落库），读面必须过滤。
	db.ExecContext(ctx, fixtureDatasetSQL(`INSERT INTO audit_logs (id, trace_id, traffic_source, method, path, audit_outcome, success,
		sample_bucket, sample_reason, capture_status, lifecycle_status, started_at, ended_at, created_at)
		VALUES ('terminal-in-progress', 'trace-terminal-shell', 'gateway', 'POST', '/v1/chat/completions', 'gateway_succeeded', true,
		1, 'in_progress', 'metadata_only', 'in_progress', '2026-11-11T10:00:00.000Z', '2026-11-11T10:00:00.100Z', '2026-11-11T10:00:01.000Z')`))

	pageSize := 10
	page, err := reader.ListAuditLogs(ctx, AuditLogListOptions{PageSize: &pageSize})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "terminal-finalized" {
		t.Fatalf("列表读面必须只返回 finalized 行: %+v", page.Items)
	}
	for _, item := range page.Items {
		if item.LifecycleStatus != "finalized" {
			t.Fatalf("列表行 lifecycle 必须 finalized: %q", item.LifecycleStatus)
		}
	}

	detail, err := reader.GetAuditLogDetail(ctx, "terminal-in-progress")
	if err != nil {
		t.Fatal(err)
	}
	if detail != nil {
		t.Fatalf("in_progress 残留行不得出现在详情读面: %+v", detail)
	}
	finalDetail, err := reader.GetAuditLogDetail(ctx, "terminal-finalized")
	if err != nil {
		t.Fatal(err)
	}
	if finalDetail == nil || finalDetail.ID != "terminal-finalized" {
		t.Fatalf("finalized 详情读面必须可达: %+v", finalDetail)
	}

	byID, err := reader.ListAuditLogsByID(ctx, []string{"terminal-in-progress", "terminal-finalized"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byID) != 1 || byID[0].ID != "terminal-finalized" {
		t.Fatalf("按 ID 读面必须过滤 in_progress 行: %+v", byID)
	}
}
