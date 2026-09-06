package main

// codexUsageHeadersChannelDispatcher（compose_codex_usage_headers.go）的装配
// 回归：job 形状逐字段对照 Node buildOpenAICodexUsageRecordMaintenanceJob
// （usage.service.ts:74-87），fire-and-forget 语义与无头静默契约。

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/tablemonitor"
)

func newCodexUsageHeadersTestChannel(t *testing.T) (*sql.DB, *tablemonitor.DurableDispatch) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "codex-usage.sqlite3"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	dispatch, err := tablemonitor.NewDurableRecordMaintenanceDispatch(db, false, time.Now)
	if err != nil {
		t.Fatalf("create durable dispatch: %v", err)
	}
	return db, dispatch
}

// TestCodexUsageHeadersChannelDispatcherJobShape：合格 OAuth codex 头 →
// account_usage_snapshot_upsert 行（kind/source/snapshot payload 键与 Node
// 投影一致，5h 窗口归一 + reset_at）；无 codex 头不落行；nil 通道保持静默。
func TestCodexUsageHeadersChannelDispatcherJobShape(t *testing.T) {
	db, dispatch := newCodexUsageHeadersTestChannel(t)
	adapter := newCodexUsageHeadersChannelDispatcher(dispatch)
	if adapter == nil {
		t.Fatal("non-nil channel must build the dispatcher")
	}
	if newCodexUsageHeadersChannelDispatcher(nil) != nil {
		t.Fatal("nil channel must keep the nil dispatcher silent contract")
	}

	headers := http.Header{
		"X-Codex-Primary-Used-Percent":        []string{"12.5"},
		"X-Codex-Primary-Reset-After-Seconds": []string{"3600"},
		"X-Codex-Primary-Window-Minutes":      []string{"300"},
	}
	adapter.PersistOpenAICodexUsageHeaders(context.Background(), "acc_codex", headers, "gateway_error")

	const (
		jobType    = "type"
		accountCol = "account_id"
		kindCol    = "kind"
		sourceCol  = "source"
		snapshot   = "snapshot_json"
		updatedAt  = "updated_at"
	)
	var (
		rowType, accountID, kind, source, snapshotJSON, rowUpdatedAt string
		readErr                                                      error
	)
	// -race 全包运行下调度显著慢化：轮询窗放宽到 15s（命中时首轮即返回）。
	deadline := time.Now().Add(15 * time.Second)
	for {
		readErr = db.QueryRow(`SELECT type, account_id, kind, source, snapshot_json, updated_at
			FROM record_maintenance_jobs LIMIT 1`).
			Scan(&rowType, &accountID, &kind, &source, &snapshotJSON, &rowUpdatedAt)
		if readErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("snapshot job row never landed (fire-and-forget dispatch): %v", readErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if rowType != tablemonitor.RecordMaintenanceJobTypeAccountUsageSnapshotUpsert {
		t.Fatalf("job type = %q", rowType)
	}
	if accountID != "acc_codex" || kind != "openai_codex" || source != "gateway_error" {
		t.Fatalf("job envelope = %s/%s/%s", accountID, kind, source)
	}
	if rowUpdatedAt == "" {
		t.Fatal("updated_at must be persisted (executor validates it)")
	}
	snapshotPayload := map[string]any{}
	if err := json.Unmarshal([]byte(snapshotJSON), &snapshotPayload); err != nil {
		t.Fatalf("snapshot_json invalid: %v", err)
	}
	if snapshotPayload["codex_usage_updated_at"] == nil {
		t.Fatalf("payload must carry codex_usage_updated_at: %v", snapshotPayload)
	}
	if snapshotPayload["source"] != "gateway_error" {
		t.Fatalf("payload source = %v", snapshotPayload["source"])
	}
	if snapshotPayload["codex_primary_used_percent"] != float64(12.5) {
		t.Fatalf("codex_primary_used_percent = %v", snapshotPayload["codex_primary_used_percent"])
	}
	if snapshotPayload["codex_primary_reset_after_seconds"] == nil {
		t.Fatalf("codex_primary_reset_after_seconds missing: %v", snapshotPayload)
	}
	// 300 分钟 ≤ 360 → 归一为 5h 窗口并派生 reset_at（usage.service.ts:111-124）。
	if snapshotPayload["codex_5h_used_percent"] != float64(12.5) {
		t.Fatalf("codex_5h_used_percent = %v", snapshotPayload["codex_5h_used_percent"])
	}
	if snapshotPayload["codex_5h_reset_at"] == nil {
		t.Fatalf("codex_5h_reset_at missing: %v", snapshotPayload)
	}

	// 无 codex 头：派发口静默返回，不追加行。
	adapter.PersistOpenAICodexUsageHeaders(context.Background(), "acc_codex",
		http.Header{"X-Request-Id": []string{"plain"}}, "gateway_error")
	time.Sleep(50 * time.Millisecond)
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM record_maintenance_jobs`).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("rows = %d want 1 (headers without codex data must not enqueue)", count)
	}
}

// 编译期锚：gatewaydispatch 的 job 载荷投影函数保持可导出（组合根唯一
// 消费点在本适配器）。
var _ = gatewaydispatch.BuildOpenAICodexUsageRecordMaintenanceJob
