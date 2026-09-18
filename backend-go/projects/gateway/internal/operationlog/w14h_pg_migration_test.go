package operationlog

// w14h 覆盖波次：MigrateLegacyPostgres 全路径（通过 pgpool 注入脚本化连接池）
// 与 legacy 校验辅助的剩余错误臂。所有 PG 交互均为脚本驱动，不访问真实网络。
//
// 已知不可达语句（经证据复核，登记备查）：
//   - legacy_migration.go verifyLegacySQLiteExistingRecord 中 actual 侧
//     created_at 的 parseStorageTime 错误臂：actual.CreatedAt 必经
//     storageTimestamp.Scan（内含同一 parseStorageTime）归一化，再次解析不会失败。
//   - legacy_migration.go verifyLegacySQLiteSamples 的 sample.Input.ID==""
//     continue 臂与 config.go validateUsageShardIsolation 的 SameFile 复核臂：
//     前者空 ID 在 scanLegacyOperationLog 已被拒绝，后者被更早的 within 检查
//     遮蔽（本次一并按授权删除）。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
	sqlite "modernc.org/sqlite"
)

var w14hBoom = errors.New("w14h boom")

var w14hLegacyCols = []string{
	"id", "trace_id", "actor_system_account_id", "actor_username", "actor_display_name",
	"actor_role", "operation_scope_system_account_id", "mode", "module", "action",
	"operation_key", "resource_type", "resource_id", "resource_name", "summary",
	"detail_level", "visibility_scope", "changes_json", "metadata_json", "method",
	"path", "status_code", "client_ip", "user_agent", "created_at",
}

const w14hLegacyViewerPK = "operation_log_id,system_account_id,visibility_reason"

// w14hSnapshotRow 构造一条可通过 scanLegacyOperationLog 的旧 Node 投影行。
func w14hSnapshotRow(id string) []driver.Value {
	return []driver.Value{id, "", "w14h-actor", "actor", "Actor", "admin", "", "admin",
		"w14h", "migrate", "w14h.migrate", "operation_log", id, "W14H", "w14h summary hello",
		"full", "admin_only", "[]", "{}", "POST", "/v1/x", int64(200), "127.0.0.1", "w14h-agent",
		"2026-08-13T08:00:00.100000000+08:00"}
}

// w14hCatalog 构造带指定 viewer 主键的伪 PG catalog fallback。
func w14hCatalog(viewerPK string, mutate func(*w9bPGCatalog)) func(string, string, []driver.NamedValue) ([]string, [][]driver.Value, error) {
	catalog := newW9BPGCatalog()
	catalog.viewerPK = viewerPK
	if mutate != nil {
		mutate(catalog)
	}
	return catalog.respond
}

// w14hMigrationPool 通过 pgpool 注入脚本化 db，返回可直接调用 MigrateLegacyPostgres 的配置。
func w14hMigrationPool(t *testing.T, script *w9bScript) Config {
	t.Helper()
	handle, err := pgpool.NewRegistry().AcquireWith(func() (*sql.DB, error) {
		return w9bOpen(t, script), nil
	}, "w14h-pool", "operation-log", 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	return Config{Mode: ModePostgres, PostgresPool: handle}
}

// w14hUpgradeBaseSteps 返回 in-place 升级成功路径所需的全部查询脚本：
// 两个抽样快照空结果、主键约束名、search term 重建的一批一行 + 空 批次。
// 故障步骤放在切片前端可遮蔽同匹配的基础步骤。
func w14hUpgradeBaseSteps() []w9bStep {
	return []w9bStep{
		{matcher: []string{"ORDER BY created_at,id LIMIT 1"}, noRows: true, cols: w14hLegacyCols},
		{matcher: []string{"ORDER BY created_at DESC,id DESC LIMIT 1"}, noRows: true, cols: w14hLegacyCols},
		{matcher: []string{"SELECT conname FROM pg_constraint"}, rows: [][]driver.Value{{"operation_log_viewers_pkey"}}},
		{matcher: []string{"LIMIT 200"}, cols: []string{"id", "summary", "created_at"}, rows: [][]driver.Value{{"w14h-op-1", "w14h summary hello", time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)}}},
		{matcher: []string{"LIMIT 200"}, noRows: true, cols: []string{"id", "summary", "created_at"}},
	}
}

func TestW14HMigrateLegacyPostgresUpgradeInPlace(t *testing.T) {
	ctx := context.Background()
	// 迁移前主键读取（首个 string_agg 查询）必须返回旧主键；迁移后的
	// validatePostgresSchema 走 fallback，返回升级后的新主键。
	steps := append([]w9bStep{
		{matcher: []string{"string_agg"}, repeat: 1, cols: []string{"pk"}, rows: [][]driver.Value{{w14hLegacyViewerPK}}},
	}, w14hUpgradeBaseSteps()...)
	script := &w9bScript{steps: steps, fallback: w14hCatalog(postgresPrimaryKeys["operation_log_viewers"], nil)}
	result, err := MigrateLegacyPostgres(ctx, w14hMigrationPool(t, script), w11fGates())
	if err != nil {
		t.Fatalf("upgrade in place = %v", err)
	}
	if result.Mode != "postgres-in-place" || result.NoOp || !result.SearchTermsRebuilt {
		t.Fatalf("result = %+v", result)
	}
}

func TestW14HMigrateLegacyPostgresNoOp(t *testing.T) {
	ctx := context.Background()
	currentPK := postgresPrimaryKeys["operation_log_viewers"]
	noOpSteps := []w9bStep{
		{matcher: []string{"ORDER BY created_at,id LIMIT 1"}, noRows: true, cols: w14hLegacyCols},
		{matcher: []string{"ORDER BY created_at DESC,id DESC LIMIT 1"}, noRows: true, cols: w14hLegacyCols},
	}
	// NoOp 成功路径。
	script := &w9bScript{steps: noOpSteps, fallback: w14hCatalog(currentPK, nil)}
	result, err := MigrateLegacyPostgres(ctx, w14hMigrationPool(t, script), w11fGates())
	if err != nil || !result.NoOp {
		t.Fatalf("no-op = %+v/%v", result, err)
	}
	// NoOp 路径 validate 失败。
	script2 := &w9bScript{steps: noOpSteps, fallback: w14hCatalog(currentPK, func(c *w9bPGCatalog) { c.columnErr = w14hBoom })}
	if _, err := MigrateLegacyPostgres(ctx, w14hMigrationPool(t, script2), w11fGates()); err == nil {
		t.Fatal("no-op validate fault must fail")
	}
	// NoOp 路径 commit 失败。
	script3 := &w9bScript{steps: append(noOpSteps, w9bStep{matcher: []string{"COMMIT"}, commitErr: w14hBoom}), fallback: w14hCatalog(currentPK, nil)}
	if _, err := MigrateLegacyPostgres(ctx, w14hMigrationPool(t, script3), w11fGates()); err == nil {
		t.Fatal("no-op commit fault must fail")
	}
}

func TestW14HMigrateLegacyPostgresErrorArms(t *testing.T) {
	ctx := context.Background()
	currentPK := postgresPrimaryKeys["operation_log_viewers"]
	// 升级路径公共脚本：首个 string_agg（迁移前主键读取）返回旧主键；迁移后
	// 的 validatePostgresSchema 走 fallback 返回升级后的新主键。故障步骤置于
	// 切片前端以遮蔽同匹配的后续步骤。
	build := func(extra []w9bStep, mutate func(*w9bPGCatalog)) Config {
		pkStep := w9bStep{matcher: []string{"string_agg"}, repeat: 1, cols: []string{"pk"}, rows: [][]driver.Value{{w14hLegacyViewerPK}}}
		steps := append(append([]w9bStep{}, extra...), pkStep)
		steps = append(steps, w14hUpgradeBaseSteps()...)
		script := &w9bScript{steps: steps, fallback: w14hCatalog(currentPK, mutate)}
		return w14hMigrationPool(t, script)
	}
	cases := []struct {
		name    string
		steps   []w9bStep
		mutate  func(*w9bPGCatalog)
		want    string
		wantErr error
	}{
		{name: "begin tx", steps: []w9bStep{{matcher: []string{"BEGIN"}, beginErr: w14hBoom}}, wantErr: w14hBoom},
		{name: "advisory lock", steps: []w9bStep{{matcher: []string{"pg_advisory_xact_lock"}, execErr: w14hBoom}}, want: "迁移锁失败"},
		{name: "source counts", steps: []w9bStep{{matcher: []string{"FROM juhe_dataset.operation_logs"}, rowsErr: w14hBoom}}, want: "读取操作日志表"},
		{name: "snapshot", steps: []w9bStep{{matcher: []string{"ORDER BY created_at,id LIMIT 1"}, rowsErr: w14hBoom}}, want: "读取旧 Node 操作日志行失败"},
		{name: "pk read", steps: []w9bStep{{matcher: []string{"string_agg"}, rowsErr: w14hBoom}}, want: "主键失败"},
		{name: "unknown pk", steps: []w9bStep{{matcher: []string{"string_agg"}, repeat: 1, cols: []string{"pk"}, rows: [][]driver.Value{{"w14h,bogus,pk"}}}}, want: "拒绝未知"},
		{name: "constraint read", steps: []w9bStep{{matcher: []string{"SELECT conname FROM pg_constraint"}, rowsErr: w14hBoom}}, want: "主键约束失败"},
		{name: "drop constraint", steps: []w9bStep{{matcher: []string{"DROP CONSTRAINT"}, execErr: w14hBoom}}, want: "升级旧 Node"},
		{name: "add constraint", steps: []w9bStep{{matcher: []string{"ADD CONSTRAINT"}, execErr: w14hBoom}}, want: "创建 F4 operation_log_viewers 主键失败"},
		{name: "rebuild indexes", steps: []w9bStep{{matcher: []string{"DROP INDEX IF EXISTS juhe_dataset."}, execErr: w14hBoom}}, want: "删除旧 Node F4 索引"},
		{name: "rebuild terms delete", steps: []w9bStep{{matcher: []string{"DELETE FROM juhe_dataset.operation_log_summary_search_terms"}, execErr: w14hBoom}}, want: "清理旧 Node F4 search terms"},
		{name: "rebuild terms batch", steps: []w9bStep{{matcher: []string{"LIMIT 200"}, rowsErr: w14hBoom}}, want: "读取 F4 PostgreSQL search term 重建批次失败"},
		{name: "rebuild terms scan", steps: []w9bStep{{matcher: []string{"LIMIT 200"}, cols: []string{"id", "summary", "created_at"}, rows: [][]driver.Value{{nil, "s", time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)}}}}, want: "读取 F4 PostgreSQL search term 重建行失败"},
		{name: "rebuild terms rows err", steps: []w9bStep{{matcher: []string{"LIMIT 200"}, cols: []string{"id", "summary", "created_at"}, rows: [][]driver.Value{{"w14h-op-1", "s", time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)}}, eofErr: w14hBoom}}, want: "遍历 F4 PostgreSQL search term 重建批次失败"},
		{name: "rebuild terms insert", steps: []w9bStep{{matcher: []string{"INSERT INTO juhe_dataset.operation_log_summary_search_terms"}, execErr: w14hBoom}}, want: "写入 F4 PostgreSQL 重建 search terms 失败"},
		{name: "apply schema", steps: []w9bStep{{matcher: []string{"CREATE SCHEMA"}, execErr: w14hBoom}}, want: "initialize F4 postgres schema"},
		{name: "validate schema", mutate: func(c *w9bPGCatalog) { c.columnErr = w14hBoom }, want: "schema incompatible"},
		{name: "target counts", steps: []w9bStep{
			{matcher: []string{"SELECT COUNT(*) FROM juhe_dataset."}, repeat: 4, rows: [][]driver.Value{{int64(0)}}},
			{matcher: []string{"SELECT COUNT(*) FROM juhe_dataset."}, rowsErr: w14hBoom},
		}, want: "读取操作日志表"},
		{name: "counts mismatch", steps: []w9bStep{
			{matcher: []string{"SELECT COUNT(*) FROM juhe_dataset."}, repeat: 4, rows: [][]driver.Value{{int64(0)}}},
			{matcher: []string{"SELECT COUNT(*) FROM juhe_dataset."}, repeat: 1, rows: [][]driver.Value{{int64(7)}}},
		}, want: "行数变化"},
		{name: "commit", steps: []w9bStep{{matcher: []string{"COMMIT"}, commitErr: w14hBoom}}, want: "提交 F4 PostgreSQL 历史迁移失败"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := build(tc.steps, tc.mutate)
			_, err := MigrateLegacyPostgres(ctx, cfg, w11fGates())
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("%s = %v（期望 %v）", tc.name, err, tc.wantErr)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s = %v（期望包含 %q）", tc.name, err, tc.want)
			}
		})
	}
}

// TestW14HMigrateLegacyPostgresSampleVerify 覆盖带非空样本时的抽样校验错误臂。
func TestW14HMigrateLegacyPostgresSampleVerify(t *testing.T) {
	ctx := context.Background()
	script := &w9bScript{
		steps: []w9bStep{
			{matcher: []string{"ORDER BY created_at,id LIMIT 1"}, cols: w14hLegacyCols, rows: [][]driver.Value{w14hSnapshotRow("w14h-sample-1")}},
			{matcher: []string{"ORDER BY created_at DESC,id DESC LIMIT 1"}, noRows: true, cols: w14hLegacyCols},
			{matcher: []string{"FROM juhe_dataset.operation_log_targets WHERE operation_log_id"}, noRows: true, cols: []string{"id"}},
			{matcher: []string{"FROM juhe_dataset.operation_log_viewers WHERE operation_log_id"}, noRows: true, cols: []string{"system_account_id"}},
			{matcher: []string{"SELECT conname FROM pg_constraint"}, rows: [][]driver.Value{{"operation_log_viewers_pkey"}}},
			{matcher: []string{"LIMIT 200"}, cols: []string{"id", "summary", "created_at"}},
			{matcher: []string{"WHERE id=$1"}, rowsErr: w14hBoom},
		},
		fallback: w14hCatalog(w14hLegacyViewerPK, nil),
	}
	_, err := MigrateLegacyPostgres(ctx, w14hMigrationPool(t, script), w11fGates())
	if err == nil {
		t.Fatal("sample verify fault must fail")
	}
}

// TestW14HPostgresRecordReaderArms 直驱 readPostgresLegacyRecord 与 PG 读取辅助的错误臂。
func TestW14HPostgresRecordReaderArms(t *testing.T) {
	ctx := context.Background()
	projection := postgresLegacyOperationLogProjection
	// 主行命中后 targets 查询失败。
	script := &w9bScript{steps: []w9bStep{
		{matcher: []string{"FROM juhe_dataset.operation_logs"}, cols: w14hLegacyCols, rows: [][]driver.Value{w14hSnapshotRow("w14h-rec-1")}},
		{matcher: []string{"FROM juhe_dataset.operation_log_targets WHERE operation_log_id"}, rowsErr: w14hBoom},
	}}
	tx, _ := w9bOpen(t, script).BeginTx(ctx, nil)
	defer tx.Rollback()
	if _, _, err := readPostgresLegacyRecord(ctx, tx, projection+" LIMIT 1", nil); err == nil {
		t.Fatal("record targets fault must fail")
	}
	// targets 为空 + viewers 查询失败。
	script2 := &w9bScript{steps: []w9bStep{
		{matcher: []string{"FROM juhe_dataset.operation_logs"}, cols: w14hLegacyCols, rows: [][]driver.Value{w14hSnapshotRow("w14h-rec-2")}},
		{matcher: []string{"FROM juhe_dataset.operation_log_targets WHERE operation_log_id"}, noRows: true, cols: []string{"id"}},
		{matcher: []string{"FROM juhe_dataset.operation_log_viewers WHERE operation_log_id"}, rowsErr: w14hBoom},
	}}
	tx2, _ := w9bOpen(t, script2).BeginTx(ctx, nil)
	defer tx2.Rollback()
	if _, _, err := readPostgresLegacyRecord(ctx, tx2, projection+" LIMIT 1", nil); err == nil {
		t.Fatal("record viewers fault must fail")
	}
	// targets 行扫描失败 / viewers 行扫描失败。
	script3 := &w9bScript{steps: []w9bStep{
		{matcher: []string{"FROM juhe_dataset.operation_log_targets"}, cols: []string{"id", "target_type", "target_id", "target_name", "target_owner_system_account_id", "relation", "created_at"}, rows: [][]driver.Value{{nil, "group", "g", "n", "o", "child", "2026-01-01T00:00:00Z"}}},
	}}
	tx3, _ := w9bOpen(t, script3).BeginTx(ctx, nil)
	defer tx3.Rollback()
	if _, err := readPostgresLegacyTargets(ctx, tx3, "w14h-rec-3"); err == nil {
		t.Fatal("pg targets scan fault must fail")
	}
	script4 := &w9bScript{steps: []w9bStep{
		{matcher: []string{"FROM juhe_dataset.operation_log_viewers"}, cols: []string{"system_account_id", "visibility_reason", "detail_level", "created_at"}, rows: [][]driver.Value{{nil, "owner", "full", "2026-01-01T00:00:00Z"}}},
	}}
	tx4, _ := w9bOpen(t, script4).BeginTx(ctx, nil)
	defer tx4.Rollback()
	if _, err := readPostgresLegacyViewers(ctx, tx4, "w14h-rec-4"); err == nil {
		t.Fatal("pg viewers scan fault must fail")
	}
	// verifyPostgresLegacySamples 查询错误（w11f 只覆盖了 not-found 臂）。
	script5 := &w9bScript{steps: []w9bStep{{matcher: []string{"WHERE id=$1"}, rowsErr: w14hBoom}}}
	tx5, _ := w9bOpen(t, script5).BeginTx(ctx, nil)
	defer tx5.Rollback()
	sample := legacyOperationLog{Input: w11fInput("w14h-sample")}
	if err := verifyPostgresLegacySamples(ctx, tx5, []legacyOperationLog{sample}); err == nil {
		t.Fatal("verify samples fault must fail")
	}
}

// TestW14HLegacySQLiteReferenceAndTimeArms 覆盖引用校验查询错误与 target/viewer 时间解析错误臂。
func TestW14HLegacySQLiteReferenceAndTimeArms(t *testing.T) {
	ctx := context.Background()
	// verifySQLiteOperationLogReferences 查询错误。
	script := &w9bScript{steps: []w9bStep{{matcher: []string{"LEFT JOIN operation_logs"}, rowsErr: w14hBoom}}}
	if err := verifySQLiteOperationLogReferences(ctx, w9bOpen(t, script), "w14h"); err == nil {
		t.Fatal("reference query fault must fail")
	}
	// readLegacyTargets：created_at 非法。
	script2 := &w9bScript{steps: []w9bStep{
		{matcher: []string{"FROM operation_log_targets"}, cols: []string{"id", "target_type", "target_id", "target_name", "target_owner_system_account_id", "relation", "created_at"}, rows: [][]driver.Value{{"w14h-t", "group", "g", "n", "o", "primary", "w14h-bogus-time"}}},
	}}
	if _, err := readLegacyTargets(ctx, w9bOpen(t, script2), "w14h-op"); err == nil || !strings.Contains(err.Error(), "target created_at 无效") {
		t.Fatalf("target time fault = %v", err)
	}
	// readLegacyViewers：created_at 非法。
	script3 := &w9bScript{steps: []w9bStep{
		{matcher: []string{"FROM operation_log_viewers"}, cols: []string{"system_account_id", "visibility_reason", "detail_level", "created_at"}, rows: [][]driver.Value{{"w14h-viewer", "resource_owner", "full", "w14h-bogus-time"}}},
	}}
	if _, err := readLegacyViewers(ctx, w9bOpen(t, script3), "w14h-op"); err == nil || !strings.Contains(err.Error(), "viewer created_at 无效") {
		t.Fatalf("viewer time fault = %v", err)
	}
	// legacySearchTerms：扫描失败与行迭代失败。
	script4 := &w9bScript{steps: []w9bStep{
		{matcher: []string{"operation_log_summary_search_terms"}, cols: []string{"term"}, rows: [][]driver.Value{{nil}}},
	}}
	if _, err := legacySearchTerms(ctx, w11fQueryerDB{w9bOpen(t, script4)}, "w14h-op"); err == nil {
		t.Fatal("terms scan fault must fail")
	}
	script5 := &w9bScript{steps: []w9bStep{
		{matcher: []string{"operation_log_summary_search_terms"}, cols: []string{"term"}, rows: [][]driver.Value{{"a"}}, eofErr: w14hBoom},
	}}
	if _, err := legacySearchTerms(ctx, w11fQueryerDB{w9bOpen(t, script5)}, "w14h-op"); err == nil {
		t.Fatal("terms rows fault must fail")
	}
}

// TestW14HVerifyExistingRecordFaultArms 覆盖 verifyLegacySQLiteExistingRecord 剩余分支。
func TestW14HVerifyExistingRecordFaultArms(t *testing.T) {
	ctx := context.Background()
	store, script, lease := w11fFaultStore(t)
	w14hVerifyBase := func() legacyOperationLog {
		input := w11fInput("w14h-verify-1")
		input.Changes = []Change{}
		input.Metadata = json.RawMessage("{}")
		return legacyOperationLog{Input: input, RawChangesJSON: "[]", RawMetadataJSON: "{}",
			Targets: []legacyTarget{{ID: "w14h-vt", Target: Target{TargetType: "group", TargetID: "g", Relation: "child"}, CreatedAt: "2026-01-01T00:00:00.000Z"}},
			Viewers: []legacyViewer{{Viewer: Viewer{SystemAccountID: "w14h-vv", VisibilityReason: "resource_owner", DetailLevel: "full"}, CreatedAt: "2026-01-01T00:00:00.000Z"}},
		}
	}
	record := w14hVerifyBase()
	if _, err := persistLegacySQLiteOperationLog(ctx, store, lease, record); err != nil {
		t.Fatal(err)
	}
	// 主行查询非 ErrNoRows 错误。
	script.failQuery("FROM operation_logs WHERE id=?")
	if err := verifyLegacySQLiteExistingRecord(ctx, store.db, record); err == nil || !strings.Contains(err.Error(), "校验已迁移操作日志") {
		t.Fatalf("row query fault = %v", err)
	}
	// 实际行 changes_json 非法。
	if _, err := store.db.Exec(`UPDATE operation_logs SET changes_json='w14h-not-json' WHERE id=?`, record.Input.ID); err != nil {
		t.Fatal(err)
	}
	if err := verifyLegacySQLiteExistingRecord(ctx, store.db, w14hVerifyBase()); err == nil || !strings.Contains(err.Error(), "changes_json 失败") {
		t.Fatalf("changes_json fault = %v", err)
	}
	if _, err := store.db.Exec(`UPDATE operation_logs SET changes_json='[]' WHERE id=?`, record.Input.ID); err != nil {
		t.Fatal(err)
	}
	// targets 回读查询失败。
	script.failQuery("FROM operation_log_targets")
	if err := verifyLegacySQLiteExistingRecord(ctx, store.db, w14hVerifyBase()); err == nil {
		t.Fatal("targets read fault must fail")
	}
	// viewers 回读查询失败。
	script.failQuery("FROM operation_log_viewers")
	if err := verifyLegacySQLiteExistingRecord(ctx, store.db, w14hVerifyBase()); err == nil {
		t.Fatal("viewers read fault must fail")
	}
	// 记录侧 target created_at 非法。
	badTarget := w14hVerifyBase()
	badTarget.Targets = []legacyTarget{{ID: "w14h-vt", Target: Target{TargetType: "group", TargetID: "g", Relation: "child"}, CreatedAt: "w14h-bogus"}}
	if err := verifyLegacySQLiteExistingRecord(ctx, store.db, badTarget); err == nil || !strings.Contains(err.Error(), "created_at 失败") {
		t.Fatalf("record target time fault = %v", err)
	}
	// 记录侧 viewer created_at 非法。
	badViewer := w14hVerifyBase()
	badViewer.Viewers = []legacyViewer{{Viewer: Viewer{SystemAccountID: "w14h-vv", VisibilityReason: "resource_owner", DetailLevel: "full"}, CreatedAt: "w14h-bogus"}}
	if err := verifyLegacySQLiteExistingRecord(ctx, store.db, badViewer); err == nil || !strings.Contains(err.Error(), "created_at 失败") {
		t.Fatalf("record viewer time fault = %v", err)
	}
	// terms 回读查询失败。
	script.failQuery("operation_log_summary_search_terms")
	if err := verifyLegacySQLiteExistingRecord(ctx, store.db, w14hVerifyBase()); err == nil {
		t.Fatal("terms read fault must fail")
	}
	// 记录侧 created_at 非法。
	badRecordTime := w14hVerifyBase()
	badRecordTime.Input.CreatedAt = "w14h-bogus"
	if err := verifyLegacySQLiteExistingRecord(ctx, store.db, badRecordTime); err == nil || !strings.Contains(err.Error(), "created_at 失败") {
		t.Fatalf("record created_at fault = %v", err)
	}
}

// TestW14HPersistLegacyLeaseVerifyArms 覆盖 persistLegacySQLiteOperationLog 的两次租约校验失败臂。
// 两个子测试各自独立运行：w11fFaultStore 以 t.Name() 命名共享内存库，
// 同一测试内重复调用会命中同一内存库导致租约冲突。
func TestW14HPersistLegacyLeaseVerifyArms(t *testing.T) {
	record := legacyOperationLog{Input: w11fInput("w14h-persist-1"), RawChangesJSON: "[]", RawMetadataJSON: "{}",
		Targets: []legacyTarget{{ID: "w14h-pt", Target: Target{TargetType: "group", Relation: "child"}, CreatedAt: "2026-01-01T00:00:00.000Z"}}}
	t.Run("first lease verify", func(t *testing.T) {
		ctx := context.Background()
		store, script, lease := w11fFaultStore(t)
		script.failQuery("operation_log_owner_leases")
		if _, err := persistLegacySQLiteOperationLog(ctx, store, lease, record); err == nil {
			t.Fatal("first lease verify fault must fail")
		}
	})
	// 首条规则放过首个租约校验，第二条规则让写后校验失败。
	t.Run("second lease verify", func(t *testing.T) {
		ctx := context.Background()
		store, script, lease := w11fFaultStore(t)
		script.rules = append(script.rules,
			&w11fStoreRule{substr: "operation_log_owner_leases", limit: 1},
			&w11fStoreRule{substr: "operation_log_owner_leases", queryErr: w14hBoom},
		)
		if _, err := persistLegacySQLiteOperationLog(ctx, store, lease, record); err == nil || !errors.Is(err, w14hBoom) {
			t.Fatalf("second lease verify = %v", err)
		}
	})
}

// w14hSamplesFaultStore 与 w11fFaultStore 同型，但包装连接池允许 2 连接，
// 供抽样校验（外层 rows 打开期间继续读 source）使用。
func w14hSamplesFaultStore(t *testing.T) (*sqlStore, *w11fStoreScript, OwnerLease) {
	t.Helper()
	base, err := sqlite.NewConnector("file:w14h-oplog-" + strings.ReplaceAll(t.Name(), "/", "-") + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	script := &w11fStoreScript{}
	db := sql.OpenDB(w11fStoreConnector{base: base, script: script})
	db.SetMaxOpenConns(2)
	t.Cleanup(func() { _ = db.Close() })
	root := t.TempDir()
	business := filepath.Join(root, "business.sqlite3")
	createBusinessSettings(t, business, "365")
	opened, err := OpenStore(Config{Mode: ModeSQLite, InstanceID: "w14h-owner", DatabasePath: filepath.Join(root, "operation.sqlite3"), BusinessSettingsPath: business, OwnerLease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	store := opened.(*sqlStore)
	origDB := store.db
	t.Cleanup(func() { _ = origDB.Close() })
	t.Cleanup(func() { _ = store.Close() })
	store.db = db
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	lease, ok, err := store.AcquireOwnerLease(ctx, "w14h-owner", time.Minute)
	if err != nil || !ok {
		t.Fatalf("lease = %v/%v", ok, err)
	}
	return store, script, lease
}

// TestW14HVerifyLegacySQLiteSamplesFaults 覆盖抽样校验内的查询/子读取错误臂。
// 抽样主查询 rows 保持打开期间会继续在同一 source 上读取 targets/viewers，
// 因此包装连接池必须允许 ≥2 连接，否则内层查询会因连接信号量永久阻塞。
func TestW14HVerifyLegacySQLiteSamplesFaults(t *testing.T) {
	ctx := context.Background()
	store, script, lease := w14hSamplesFaultStore(t)
	if _, err := store.Persist(ctx, lease, w11fInput("w14h-sample-1")); err != nil {
		t.Fatal(err)
	}
	// 主查询失败。
	script.failQuery("ORDER BY created_at,id")
	if err := verifyLegacySQLiteSamples(ctx, store.db, store.db); err == nil || !strings.Contains(err.Error(), "读取 F4 SQLite 迁移抽样失败") {
		t.Fatalf("samples main fault = %v", err)
	}
	// targets 子查询失败。
	script.failQuery("FROM operation_log_targets")
	if err := verifyLegacySQLiteSamples(ctx, store.db, store.db); err == nil {
		t.Fatal("samples targets fault must fail")
	}
	// viewers 子查询失败。
	script.failQuery("FROM operation_log_viewers")
	if err := verifyLegacySQLiteSamples(ctx, store.db, store.db); err == nil {
		t.Fatal("samples viewers fault must fail")
	}
}

// TestW14HNormalizedTargetRelationInvalid 覆盖 normalizedTargets 的非法 relation 臂。
func TestW14HNormalizedTargetRelationInvalid(t *testing.T) {
	_, err := normalizedTargets([]Target{{TargetType: "group", Relation: "w14h-bogus"}})
	if err == nil || !strings.Contains(err.Error(), "relation is invalid") {
		t.Fatalf("invalid relation = %v", err)
	}
}

// TestW14HValidateUsageShardInsideRoot 确认操作库位于分片根内仍被拒绝（同文件臂由该检查遮蔽）。
func TestW14HValidateUsageShardInsideRoot(t *testing.T) {
	root := t.TempDir()
	shardDir := filepath.Join(root, "2026", "09", "01")
	if err := os.MkdirAll(shardDir, 0o750); err != nil {
		t.Fatal(err)
	}
	shard := filepath.Join(shardDir, "usage-20260901-s0.sqlite3")
	file, err := os.Create(shard)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	err = validateUsageShardIsolation(shard, root)
	if err == nil || !strings.Contains(err.Error(), "must not be inside") {
		t.Fatalf("inside shard root = %v", err)
	}
}
