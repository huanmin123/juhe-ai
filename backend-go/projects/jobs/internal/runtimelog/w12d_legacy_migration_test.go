package runtimelog

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// w12d_legacy_migration_test.go 覆盖 w12d 波次 legacy SQLite 迁移的参数校验、
// 完整迁移路径与数据校验拒绝分支（全部本地临时文件）。

// w12dCreateLegacyDataset 建一个含 canonical 数据的旧 dataset SQLite 文件。
func w12dCreateLegacyDataset(t *testing.T, path, timeValue string) {
	t.Helper()
	handle, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if _, err := handle.Exec(sqliteSchema); err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`INSERT INTO runtime_logs (id, log_file, log_offset, line_number, time, level, trace_id, event, message, error_message, raw_json, created_at)
			VALUES ('w12d-legacy-1', 'juhe-ai.log', 10, 1, '` + timeValue + `', 'info', '', 'w12d-legacy', 'msg', '', '{}', '` + timeValue + `')`,
		`INSERT INTO runtime_log_file_cursors (log_file, file_identity, cursor_offset, line_number, file_size, truncation_generation, file_mtime_ms, last_read_at, last_error_message, created_at, updated_at)
			VALUES ('juhe-ai.log', 'identity-w12d', 10, 1, 100, 0, 0, '` + timeValue + `', NULL, '` + timeValue + `', '` + timeValue + `')`,
		`INSERT INTO runtime_log_facet_summary (bucket_key, total_count, earliest_time, latest_time, updated_at)
			VALUES ('current', 1, '` + timeValue + `', '` + timeValue + `', '` + timeValue + `')`,
		`INSERT INTO runtime_log_level_facets (bucket_key, level, count, updated_at)
			VALUES ('current', 'info', 1, '` + timeValue + `')`,
		`INSERT INTO runtime_log_event_facets (bucket_key, event, count, latest_time, updated_at)
			VALUES ('current', 'w12d-legacy', 1, '` + timeValue + `', '` + timeValue + `')`,
	}
	for _, statement := range statements {
		if _, err := handle.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

// w12dAcquiredContext 以独立 owner 获取真实 lease；SQLite owner lease 是
// 单行全局锁，调用方必须在使用后立即释放，因此不用 t.Cleanup。
func w12dAcquiredContext(t *testing.T, store Store, ownerID string) context.Context {
	t.Helper()
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), ownerID, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire lease: lease=%#v acquired=%t err=%v", lease, acquired, err)
	}
	ctx := withOwnerLease(context.Background(), lease)
	t.Cleanup(func() { _ = store.ReleaseOwnerLease(context.Background(), lease) })
	return ctx
}

func w12dReleaseLease(t *testing.T, store Store, ownerID string) {
	t.Helper()
	if err := store.ReleaseOwnerLease(context.Background(), OwnerLease{OwnerID: ownerID, FenceToken: 1}); err != nil {
		t.Logf("release: %v", err)
	}
}

func w12dMigrationConfig(t *testing.T, datasetPath string) Config {
	config := w12dFakeConfig(t)
	config.DatasetPath = datasetPath
	return config
}

// TestW12dMigrateLegacySQLiteArms 覆盖迁移入口的参数校验分支。
func TestW12dMigrateLegacySQLiteArms(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestSQLiteStore(t)
	if err := EnsureSchema(ctx, store); err != nil {
		t.Fatal(err)
	}
	var storeIface Store = store
	// mode 不是 sqlite。
	badMode := w12dFakeConfig(t)
	badMode.Mode = ModePostgres
	badMode.DatasetPath = filepath.Join(t.TempDir(), "legacy.sqlite3")
	if err := MigrateLegacySQLite(ctx, badMode, storeIface); err == nil || !strings.Contains(err.Error(), "仅能迁移到 sqlite Store") {
		t.Fatalf("bad mode: %v", err)
	}
	// DatasetPath 缺失。
	noPath := w12dFakeConfig(t)
	noPath.DatasetPath = " "
	if err := MigrateLegacySQLite(ctx, noPath, storeIface); err == nil || !strings.Contains(err.Error(), "缺少 JUHE_AI_DATASET_DATABASE_PATH") {
		t.Fatalf("missing dataset: %v", err)
	}
	// 与专用库同文件。
	samePath := w12dFakeConfig(t)
	samePath.RuntimeLogDatabasePath = "w12d-rl.sqlite3"
	samePath.DatasetPath = samePath.RuntimeLogDatabasePath
	if err := MigrateLegacySQLite(ctx, samePath, storeIface); err == nil || !strings.Contains(err.Error(), "不得与") {
		t.Fatalf("same file: %v", err)
	}
	// 数据源不存在。
	absent := w12dFakeConfig(t)
	absent.DatasetPath = filepath.Join(t.TempDir(), "absent.sqlite3")
	if err := MigrateLegacySQLite(ctx, absent, storeIface); err == nil || !strings.Contains(err.Error(), "无法访问") {
		t.Fatalf("absent dataset: %v", err)
	}
	// 非 sqlite store。
	wrongStore := &w12dFakeStore{}
	dataset := filepath.Join(t.TempDir(), "legacy.sqlite3")
	w12dCreateLegacyDataset(t, dataset, "2026-08-08T00:00:00.000Z")
	if err := MigrateLegacySQLite(ctx, w12dMigrationConfig(t, dataset), wrongStore); err == nil || !strings.Contains(err.Error(), "需要 sqlite Store") {
		t.Fatalf("wrong store: %v", err)
	}
}

// TestW12dMigrateLegacySQLiteBadData 覆盖数据源缺表/坏时间的拒绝分支。
func TestW12dMigrateLegacySQLiteBadData(t *testing.T) {
	store, _ := openTestSQLiteStore(t)
	if err := EnsureSchema(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	var storeIface Store = store
	// 空库（没有任何表）。
	empty := filepath.Join(t.TempDir(), "empty.sqlite3")
	handle, err := sql.Open("sqlite", empty)
	if err != nil {
		t.Fatal(err)
	}
	// 触发连接建立以生成空文件。
	if _, err := handle.Exec("PRAGMA user_version = 0"); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	err = MigrateLegacySQLite(w12dAcquiredContext(t, storeIface, "w12d-baddata-1"), w12dMigrationConfig(t, empty), storeIface)
	w12dReleaseLease(t, storeIface, "w12d-baddata-1")
	if err == nil || !strings.Contains(err.Error(), "缺少表") {
		t.Fatalf("missing tables: %v", err)
	}
	// 含表但时间非 canonical。
	badTime := filepath.Join(t.TempDir(), "badtime.sqlite3")
	w12dCreateLegacyDataset(t, badTime, "2026-08-08T08:00:00+08:00")
	err = MigrateLegacySQLite(w12dAcquiredContext(t, storeIface, "w12d-baddata-2"), w12dMigrationConfig(t, badTime), storeIface)
	w12dReleaseLease(t, storeIface, "w12d-baddata-2")
	if err == nil || !strings.Contains(err.Error(), "canonical UTC") {
		t.Fatalf("non canonical time: %v", err)
	}
}

// TestW12dMigrateLegacySQLiteHappyPath 覆盖完整迁移与幂等重放。
func TestW12dMigrateLegacySQLiteHappyPath(t *testing.T) {
	ctx := context.Background()
	store, config := openTestSQLiteStore(t)
	if err := EnsureSchema(ctx, store); err != nil {
		t.Fatal(err)
	}
	var storeIface Store = store
	dataset := filepath.Join(t.TempDir(), "legacy.sqlite3")
	w12dCreateLegacyDataset(t, dataset, "2026-08-08T00:00:00.000Z")
	runCtx := testOwnerContext(t, storeIface)
	if err := MigrateLegacySQLite(runCtx, w12dMigrationConfig(t, dataset), storeIface); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// 幂等重放（INSERT OR IGNORE）。
	if err := MigrateLegacySQLite(runCtx, w12dMigrationConfig(t, dataset), storeIface); err != nil {
		t.Fatalf("replay: %v", err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM runtime_logs WHERE id = 'w12d-legacy-1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("migrated rows=%d", count)
	}
	// legacy 文件保持可回读。
	opened, err := sql.Open("sqlite", dataset)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	var sourceCount int
	if err := opened.QueryRow(`SELECT COUNT(*) FROM runtime_logs`).Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 1 {
		t.Fatalf("source rows=%d", sourceCount)
	}
	_ = config
}
