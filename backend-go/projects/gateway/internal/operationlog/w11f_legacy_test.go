package operationlog

// w11f 覆盖波次（legacy 部分）：persist/verify 迁移臂、PG 辅助函数的脚本
// 驱动错误臂、真实 PG 门禁化的 MigrateLegacyPostgres 打开错误。

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestW11FLegacyPostgresHelpers 用 w9b 脚本驱动覆盖 PG 迁移辅助的错误臂。
func TestW11FLegacyPostgresHelpers(t *testing.T) {
	ctx := context.Background()

	legacyCols := []string{"id", "trace_id", "actor_system_account_id", "actor_username", "actor_display_name",
		"actor_role", "operation_scope_system_account_id", "mode", "module", "action", "operation_key",
		"resource_type", "resource_id", "resource_name", "summary", "detail_level", "visibility_scope",
		"changes_json", "metadata_json", "method", "path", "status_code", "client_ip", "user_agent", "created_at"}

	// snapshot: 首查询错误。
	script := &w9bScript{}
	script.steps = append(script.steps, w9bStep{matcher: []string{"ORDER BY created_at,id LIMIT 1"}, rowsErr: w11fBoom})
	db := w9bOpen(t, script)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := snapshotPostgresLegacySamples(ctx, tx); err == nil {
		t.Fatal("snapshot fault must fail")
	}

	// snapshot: 空表 → 0 样本（两次查询均无行）。
	script2 := &w9bScript{}
	script2.steps = append(script2.steps,
		w9bStep{matcher: []string{"ORDER BY created_at,id LIMIT 1"}, noRows: true, cols: legacyCols},
		w9bStep{matcher: []string{"ORDER BY created_at DESC,id DESC LIMIT 1"}, noRows: true, cols: legacyCols})
	db2 := w9bOpen(t, script2)
	tx2, _ := db2.BeginTx(ctx, nil)
	defer tx2.Rollback()
	samples, err := snapshotPostgresLegacySamples(ctx, tx2)
	if err != nil || len(samples) != 0 {
		t.Fatalf("empty snapshot = %v/%v", samples, err)
	}

	// verify: 空样本 → 通过；样本缺失 → 不一致错误。
	if err := verifyPostgresLegacySamples(ctx, tx2, nil); err != nil {
		t.Fatal(err)
	}
	script3 := &w9bScript{}
	script3.steps = append(script3.steps, w9bStep{matcher: []string{"WHERE id=$1"}, noRows: true, cols: legacyCols})
	db3 := w9bOpen(t, script3)
	tx3, _ := db3.BeginTx(ctx, nil)
	defer tx3.Rollback()
	if err := verifyPostgresLegacySamples(ctx, tx3, []legacyOperationLog{{Input: w11fInput("w11f-pg")}}); err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("verify mismatch = %v", err)
	}

	// readPostgresLegacyTargets / Viewers 查询错误。
	script4 := &w9bScript{}
	script4.steps = append(script4.steps, w9bStep{matcher: []string{"FROM juhe_dataset.operation_log_targets"}, rowsErr: w11fBoom})
	db4 := w9bOpen(t, script4)
	tx4, _ := db4.BeginTx(ctx, nil)
	defer tx4.Rollback()
	if _, err := readPostgresLegacyTargets(ctx, tx4, "w11f-pg"); err == nil {
		t.Fatal("pg targets fault must fail")
	}
	script5 := &w9bScript{}
	script5.steps = append(script5.steps, w9bStep{matcher: []string{"FROM juhe_dataset.operation_log_viewers"}, rowsErr: w11fBoom})
	db5 := w9bOpen(t, script5)
	tx5, _ := db5.BeginTx(ctx, nil)
	defer tx5.Rollback()
	if _, err := readPostgresLegacyViewers(ctx, tx5, "w11f-pg"); err == nil {
		t.Fatal("pg viewers fault must fail")
	}

	// rebuildPostgresOperationLogIndexes / SearchTerms 错误臂。
	script6 := &w9bScript{}
	script6.steps = append(script6.steps, w9bStep{matcher: []string{"DROP INDEX IF EXISTS juhe_dataset."}, execErr: w11fBoom})
	db6 := w9bOpen(t, script6)
	tx6, _ := db6.BeginTx(ctx, nil)
	defer tx6.Rollback()
	if err := rebuildPostgresOperationLogIndexes(ctx, tx6); err == nil {
		t.Fatal("rebuild indexes fault must fail")
	}
	script7 := &w9bScript{}
	script7.steps = append(script7.steps, w9bStep{matcher: []string{"DELETE FROM juhe_dataset.operation_log_summary_search_terms"}, execErr: w11fBoom})
	db7 := w9bOpen(t, script7)
	tx7, _ := db7.BeginTx(ctx, nil)
	defer tx7.Rollback()
	if err := rebuildPostgresOperationLogSearchTerms(ctx, tx7); err == nil {
		t.Fatal("rebuild terms delete fault must fail")
	}
	script8 := &w9bScript{}
	script8.steps = append(script8.steps, w9bStep{matcher: []string{"ORDER BY created_at,id LIMIT 200"}, rowsErr: w11fBoom})
	db8 := w9bOpen(t, script8)
	tx8, _ := db8.BeginTx(ctx, nil)
	defer tx8.Rollback()
	if err := rebuildPostgresOperationLogSearchTerms(ctx, tx8); err == nil {
		t.Fatal("rebuild terms batch fault must fail")
	}
	// 重建正常完成（一批 0 行）。
	script9 := &w9bScript{}
	script9.steps = append(script9.steps, w9bStep{matcher: []string{"ORDER BY created_at,id LIMIT 200"}, noRows: true, cols: []string{"id", "summary", "created_at"}})
	db9 := w9bOpen(t, script9)
	tx9, _ := db9.BeginTx(ctx, nil)
	defer tx9.Rollback()
	if err := rebuildPostgresOperationLogSearchTerms(ctx, tx9); err != nil {
		t.Fatal(err)
	}
}

// TestW11FMigrateLegacyPostgresOpenFaults 覆盖 PG 迁移的模式与打开错误臂。
func TestW11FMigrateLegacyPostgresOpenFaults(t *testing.T) {
	ctx := context.Background()
	options := w11fGates()
	// 打开目标失败（非法 URL）。
	if _, err := MigrateLegacyPostgres(ctx, Config{PostgresURL: "not a url"}, options); err == nil {
		t.Fatal("bad URL must fail")
	}
}

// TestW11FListViewerPathFaults 覆盖 List 的 viewer 两臂查询与错误。

var _ = sql.ErrNoRows
var _ = filepath.Join
var _ = time.Now
