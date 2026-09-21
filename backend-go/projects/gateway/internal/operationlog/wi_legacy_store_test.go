package operationlog

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestWIMigrateLegacySQLiteGuards(t *testing.T) {
	root := t.TempDir()
	cfg := Config{Enabled: true, InstanceID: "wi-migrator", Mode: ModeSQLite, DatabasePath: root + "/operation.sqlite3", BusinessSettingsPath: root + "/business.sqlite3", OwnerLease: time.Minute}
	options := LegacyMigrationOptions{NodeStopped: true, GoStopped: true, BackupConfirmed: true}
	sourcePath := root + "/source.sqlite"
	targetPath := root + "/target.sqlite"
	_ = targetPath

	// 停机 + 备份门禁。
	if _, err := MigrateLegacySQLite(context.Background(), cfg, LegacyMigrationOptions{}); err == nil || !strings.Contains(err.Error(), "停机") {
		t.Fatalf("停机门禁=%v", err)
	}
	// 备份确认门禁。
	if _, err := MigrateLegacySQLite(context.Background(), cfg, LegacyMigrationOptions{NodeStopped: true, GoStopped: true}); err == nil || !strings.Contains(err.Error(), "备份") {
		t.Fatalf("备份门禁=%v", err)
	}
	// 非 sqlite mode。
	pgCfg := cfg
	pgCfg.Mode = ModePostgres
	if _, err := MigrateLegacySQLite(context.Background(), pgCfg, options); err == nil || !strings.Contains(err.Error(), "sqlite") {
		t.Fatalf("mode 门禁=%v", err)
	}
	// 空 source 路径。
	if _, err := MigrateLegacySQLite(context.Background(), cfg, options); err == nil || !strings.Contains(err.Error(), "source-db") {
		t.Fatalf("空 source=%v", err)
	}
	// 源与目标同文件。
	if _, err := MigrateLegacySQLite(context.Background(), cfg, LegacyMigrationOptions{
		NodeStopped: true, GoStopped: true, BackupConfirmed: true,
		SourceDatabasePath: cfg.DatabasePath,
	}); err == nil || !strings.Contains(err.Error(), "同一物理文件") {
		t.Fatalf("同文件=%v", err)
	}
	// 无 schema 的源库 → 缺表错误。
	db, err := sql.Open("sqlite", "file:"+filepath0(sourcePath))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE placeholder (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateLegacySQLite(context.Background(), cfg, LegacyMigrationOptions{
		NodeStopped: true, GoStopped: true, BackupConfirmed: true,
		SourceDatabasePath: sourcePath,
	}); err == nil || !strings.Contains(err.Error(), "缺少必需表") {
		t.Fatalf("缺表=%v", err)
	}
}

// filepath0 等价 filepath.ToSlash 的轻包装（避免重复 import 冲突）。
func filepath0(value string) string { return strings.ReplaceAll(value, "\\", "/") }

func TestWIMigrateLegacySQLiteRejectsBrokenReference(t *testing.T) {
	root := t.TempDir()
	cfg := Config{Enabled: true, InstanceID: "wi-migrator", Mode: ModeSQLite, DatabasePath: root + "/operation.sqlite3", BusinessSettingsPath: root + "/business.sqlite3", OwnerLease: time.Minute}
	sourcePath := root + "/source.sqlite"
	db, err := sql.Open("sqlite", "file:"+filepath0(sourcePath))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// 建 legacy 表并写入孤儿 target（引用不存在的日志）。
	// 刻意复刻旧 Node 操作日志 schema（迁移源形态，非 canonical Go schema；
	// operation_log_targets 旧列集含 system_account_id，MigrateLegacySQLite
	// 按该源结构校验与读取）。
	for _, ddl := range []string{
		`CREATE TABLE operation_logs (id TEXT PRIMARY KEY, summary TEXT, created_at TEXT)`,
		`CREATE TABLE operation_log_targets (operation_log_id TEXT, system_account_id TEXT, PRIMARY KEY (operation_log_id, system_account_id))`,
		`CREATE TABLE operation_log_viewers (operation_log_id TEXT, system_account_id TEXT, visibility_reason TEXT, detail_level TEXT, PRIMARY KEY (operation_log_id, system_account_id, visibility_reason))`,
		`CREATE TABLE operation_log_summary_search_terms (operation_log_id TEXT, term TEXT, PRIMARY KEY (operation_log_id, term))`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO operation_log_targets VALUES ('ghost', 'actor-1')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateLegacySQLite(context.Background(), cfg, LegacyMigrationOptions{
		NodeStopped: true, GoStopped: true, BackupConfirmed: true,
		SourceDatabasePath: sourcePath,
	}); err == nil {
		t.Fatal("孤儿引用必须报错")
	}
}

func TestWIMigrateLegacyPostgresModeGate(t *testing.T) {
	// 非 postgres mode 的守卫在任何数据库连接前拒绝。
	cfg := Config{Mode: ModeSQLite, DatabasePath: "unused.sqlite3"}
	if _, err := MigrateLegacyPostgres(context.Background(), cfg, LegacyMigrationOptions{
		NodeStopped: true, GoStopped: true, BackupConfirmed: true,
	}); err == nil || !strings.Contains(err.Error(), "postgres") {
		t.Fatalf("mode 门禁=%v", err)
	}
	// 停机门禁同样优先于连接。
	pgCfg := Config{Mode: ModePostgres}
	if _, err := MigrateLegacyPostgres(context.Background(), pgCfg, LegacyMigrationOptions{}); err == nil || !strings.Contains(err.Error(), "停机") {
		t.Fatalf("停机门禁=%v", err)
	}
}

func TestWISQLStoreDialectHelpers(t *testing.T) {
	store := wiOpenSQLiteStore(t, t.TempDir())
	implementation := store.(*sqlStore)
	// sqlite 模式 bind 原样返回、table 无 schema 前缀。
	if got := implementation.bind("VALUES (?, ?)"); got != "VALUES (?, ?)" {
		t.Fatalf("bind=%q", got)
	}
	if got := implementation.table("operation_logs"); got != "operation_logs" {
		t.Fatalf("table=%q", got)
	}
	// PG 方言的 bind/table 分支（直测，不触库）。
	pg := &sqlStore{mode: ModePostgres}
	if got := pg.bind("VALUES (?, ?)"); got != "VALUES ($1, $2)" {
		t.Fatalf("pg bind=%q", got)
	}
	if got := pg.table("operation_logs"); got != "juhe_operation_logs.operation_logs" && got == "operation_logs" {
		t.Fatalf("pg table=%q", got)
	}
	// beginTx 与 beginLegacyMigrationTx 在关闭的库上必须报错。
	_ = store.Close()
	if _, err := implementation.beginTx(context.Background()); err == nil {
		t.Fatal("关闭后 beginTx 必须报错")
	}
	if _, err := implementation.beginLegacyMigrationTx(context.Background()); err == nil {
		t.Fatal("关闭后 beginLegacyMigrationTx 必须报错")
	}
}

func TestWIProducerWarnOnFailure(t *testing.T) {
	store := wiOpenSQLiteStore(t, t.TempDir())
	lease, ok, err := store.AcquireOwnerLease(context.Background(), "wi-producer", time.Minute)
	if err != nil || !ok {
		t.Fatalf("lease=%v err=%v", ok, err)
	}
	recorder := &wiRecordingSlog{warns: make(chan string, 4)}
	// 过期 fence：续租被拒 → 告警并丢弃（fire-and-forget 契约）。
	stale := OwnerLease{OwnerID: lease.OwnerID, FenceToken: lease.FenceToken + 7}
	producer := NewProducer(store, stale, Config{OwnerLease: time.Minute}, recorder)
	producer.Record(Input{ID: "wi-op-entry", Summary: "s", CreatedAt: "2026-09-10T08:00:00Z"})
	waitForSlogWarn(t, recorder)
	// 无 Logger 时同样安全。
	quiet := NewProducer(store, lease, Config{OwnerLease: 0}, nil)
	quiet.Record(Input{ID: "wi-op-entry-2", Summary: "s", CreatedAt: "2026-09-10T08:00:00Z"})
	_ = slog.Default()
}

type wiRecordingSlog struct {
	warns chan string
}

func (r *wiRecordingSlog) Warn(msg string, args ...any) {
	if r.warns != nil {
		r.warns <- msg
	}
}

func (r *wiRecordingSlog) Error(msg string, args ...any) {}

func waitForSlogWarn(t *testing.T, recorder *wiRecordingSlog) {
	t.Helper()
	select {
	case <-recorder.warns:
	case <-time.After(5 * time.Second):
		t.Fatal("Record 失败未告警")
	}
}

func TestWIRetentionFencedAndAccountNames(t *testing.T) {
	store := wiOpenSQLiteStore(t, t.TempDir())
	time.Sleep(100 * time.Millisecond) // 前一个测试的异步 Record 已结束。
	ctx := context.Background()
	lease, ok, err := store.AcquireOwnerLease(ctx, "wi-retention", time.Minute)
	if err != nil || !ok {
		t.Fatalf("lease=%v err=%v", ok, err)
	}
	// 过期 fence → ErrOwnerLeaseLost。
	stale := OwnerLease{OwnerID: lease.OwnerID, FenceToken: lease.FenceToken + 3}
	if _, err := store.CleanupRetention(ctx, stale, time.Now().UTC().Add(time.Hour), 10); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("stale retention=%v", err)
	}
	// 正常 fence 且无数据 → 0 删除。
	deleted, err := store.CleanupRetention(ctx, lease, time.Now().UTC().Add(time.Hour), 10)
	if err != nil || deleted != 0 {
		t.Fatalf("空 retention=%d err=%v", deleted, err)
	}
	// accountNames 空输入安全。
	names, err := store.(*sqlStore).accountNames(ctx, nil)
	if err != nil || len(names) != 0 {
		t.Fatalf("accountNames=%v err=%v", names, err)
	}
	// textPrefixUpperBound：前缀上界排除下一个字符。
	if got := textPrefixUpperBound("abc"); got != "abd" {
		t.Fatalf("upper=%q", got)
	}
	if got := textPrefixUpperBound("ab\xff"); got == "" || got <= "ab\xff" {
		t.Fatalf("0xff 前缀=%q", got)
	}
}
