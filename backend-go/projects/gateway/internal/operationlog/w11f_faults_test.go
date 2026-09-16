package operationlog

// w11f 覆盖波次：离线迁移错误闸门与辅助函数分支、store 错误臂。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sqlite "modernc.org/sqlite"
)

var w11fBoom = errors.New("w11f boom")

func w11fGates() LegacyMigrationOptions {
	return LegacyMigrationOptions{NodeStopped: true, GoStopped: true, BackupConfirmed: true}
}

func TestW11FLegacySQLiteGates(t *testing.T) {
	ctx := context.Background()
	gates := w11fGates()
	root := t.TempDir()
	sourcePath := filepath.Join(root, "w11f-source.sqlite3")
	targetPath := filepath.Join(root, "w11f-target.sqlite3")
	businessPath := filepath.Join(root, "w11f-business.sqlite3")
	createLegacyNodeOperationLogSQLite(t, sourcePath)

	// 模式不符。
	_, err := MigrateLegacySQLite(ctx, Config{Mode: ModePostgres}, gates)
	if err == nil || !strings.Contains(err.Error(), "sqlite") {
		t.Fatalf("mode gate = %v", err)
	}
	// 缺源路径。
	_, err = MigrateLegacySQLite(ctx, Config{Mode: ModeSQLite, DatabasePath: targetPath}, gates)
	if err == nil || !strings.Contains(err.Error(), "--operation-log-source-db") {
		t.Fatalf("source gate = %v", err)
	}
	// 源即目标。
	_, err = MigrateLegacySQLite(ctx, Config{Mode: ModeSQLite, DatabasePath: sourcePath}, LegacyMigrationOptions{
		SourceDatabasePath: sourcePath, NodeStopped: true, GoStopped: true, BackupConfirmed: true,
	})
	if err == nil || !strings.Contains(err.Error(), "同一物理文件") {
		t.Fatalf("same file gate = %v", err)
	}
	// 目标为空 → SameFile 校验失败。
	_, err = MigrateLegacySQLite(ctx, Config{Mode: ModeSQLite}, LegacyMigrationOptions{
		SourceDatabasePath: sourcePath, NodeStopped: true, GoStopped: true, BackupConfirmed: true,
	})
	if err == nil {
		t.Fatalf("empty target = %v", err)
	}
	// 源是目录 → 连接失败。
	dirSource := filepath.Join(root, "dir-source")
	if err := mkdirW11F(dirSource); err != nil {
		t.Fatal(err)
	}
	_, err = MigrateLegacySQLite(ctx, Config{Mode: ModeSQLite, DatabasePath: targetPath}, LegacyMigrationOptions{
		SourceDatabasePath: dirSource, NodeStopped: true, GoStopped: true, BackupConfirmed: true,
	})
	if err == nil {
		t.Fatalf("dir source = %v", err)
	}
	// 空 SQLite（缺表）→ schema 校验失败。
	emptySource := filepath.Join(root, "empty-source.sqlite3")
	emptyDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(emptySource)+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := emptyDB.Exec(`CREATE TABLE unrelated (id TEXT)`); err != nil {
		t.Fatal(err)
	}
	emptyDB.Close()
	_, err = MigrateLegacySQLite(ctx, Config{Mode: ModeSQLite, DatabasePath: targetPath}, LegacyMigrationOptions{
		SourceDatabasePath: emptySource, NodeStopped: true, GoStopped: true, BackupConfirmed: true,
	})
	if err == nil || !strings.Contains(err.Error(), "缺少必需表") {
		t.Fatalf("schema gate = %v", err)
	}
	// 悬空引用 → 引用完整性失败。
	danglingSource := filepath.Join(root, "dangling-source.sqlite3")
	createLegacyNodeOperationLogSQLite(t, danglingSource)
	danglingDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(danglingSource)+"?mode=rw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := danglingDB.Exec(`INSERT INTO operation_log_targets (id, operation_log_id, target_type, target_id, relation, created_at) VALUES ('w11f-t', 'w11f-missing', 'group', 'g', 'child', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := danglingDB.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = MigrateLegacySQLite(ctx, Config{Mode: ModeSQLite, DatabasePath: targetPath}, LegacyMigrationOptions{
		SourceDatabasePath: danglingSource, NodeStopped: true, GoStopped: true, BackupConfirmed: true,
	})
	if err == nil || !strings.Contains(err.Error(), "悬空引用") {
		t.Fatalf("dangling gate = %v", err)
	}
	// 业务库缺配置 → 迁移失败（打开目标后 settings 读取失败）。
	_, err = MigrateLegacySQLite(ctx, Config{Mode: ModeSQLite, InstanceID: "w11f-i", DatabasePath: targetPath, BusinessSettingsPath: filepath.Join(root, "missing-business.sqlite3"), OwnerLease: time.Minute}, LegacyMigrationOptions{
		SourceDatabasePath: sourcePath, NodeStopped: true, GoStopped: true, BackupConfirmed: true,
	})
	if err == nil {
		t.Fatalf("missing business = %v", err)
	}
	// 正常迁移后再次迁移 → NoOp（0 行新迁移）。
	createBusinessSettings(t, businessPath, "365")
	cfg := Config{Mode: ModeSQLite, InstanceID: "w11f-i", DatabasePath: targetPath, BusinessSettingsPath: businessPath, OwnerLease: time.Minute}
	result, err := MigrateLegacySQLite(ctx, cfg, LegacyMigrationOptions{
		SourceDatabasePath: sourcePath, NodeStopped: true, GoStopped: true, BackupConfirmed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err = MigrateLegacySQLite(ctx, cfg, LegacyMigrationOptions{
		SourceDatabasePath: sourcePath, NodeStopped: true, GoStopped: true, BackupConfirmed: true,
	})
	if err != nil || !result.NoOp {
		t.Fatalf("second migration = %+v/%v", result, err)
	}
	// 路径分隔不同写法的同文件 → SameFile 命中。
	_, err = MigrateLegacySQLite(ctx, Config{Mode: ModeSQLite, DatabasePath: filepath.Join(root, ".", "w11f-source.sqlite3")}, LegacyMigrationOptions{
		SourceDatabasePath: sourcePath, NodeStopped: true, GoStopped: true, BackupConfirmed: true,
	})
	if err == nil {
		t.Fatalf("dotted path = %v", err)
	}
}

func mkdirW11F(path string) error {
	return os.MkdirAll(path, 0o750)
}

// w11fDirectHelperBranches 覆盖纯辅助函数。
func TestW11FDirectHelperBranches(t *testing.T) {
	// verifyDistinctSQLiteMigrationPaths: 相同路径 / 不同路径。
	if err := verifyDistinctSQLiteMigrationPaths("a/b", "a/b"); err == nil {
		t.Fatal("same path must fail")
	}
	if err := verifyDistinctSQLiteMigrationPaths("a/b", "a/c"); err != nil {
		t.Fatal(err)
	}
	// legacySQLiteReadOnlyDSN 两分支（非法路径走回退分支难触发；正常分支覆盖）。
	if dsn := legacySQLiteReadOnlyDSN(filepath.Join(t.TempDir(), "x.sqlite3")); !strings.Contains(dsn, "mode=ro") {
		t.Fatalf("ro dsn = %q", dsn)
	}
	// config: validateUsageShardIsolation 分支（操作库位于分片根内 → 拒绝）。
	shardRoot := t.TempDir()
	inner := filepath.Join(shardRoot, "sub", "op.sqlite3")
	if err := validateUsageShardIsolation(inner, shardRoot); err == nil {
		t.Fatalf("inside shard root = %v", err)
	}
	outside := filepath.Join(t.TempDir(), "op.sqlite3")
	if err := validateUsageShardIsolation(outside, shardRoot); err != nil {
		t.Fatalf("outside shard root = %v", err)
	}
}

// TestW11FLegacyQueryHelpers 用脚本驱动覆盖 FromQuery 辅助的错误臂。
func TestW11FLegacyQueryHelpers(t *testing.T) {
	ctx := context.Background()
	script := &w9bScript{}
	db := w9bOpen(t, script)

	// readLegacyTargets: 查询错误 / Scan 错误 / 命中。
	script.steps = append(script.steps, w9bStep{matcher: []string{"FROM operation_log_targets"}, rowsErr: w11fBoom})
	if _, err := readLegacyTargets(ctx, db, "w11f-op"); err == nil {
		t.Fatal("targets query fault must fail")
	}
	script2 := &w9bScript{}
	db2 := w9bOpen(t, script2)
	script2.steps = append(script2.steps, w9bStep{matcher: []string{"FROM operation_log_targets"}, cols: []string{"id", "operation_log_id", "target_type", "target_id", "target_name", "target_owner_system_account_id"}, rows: [][]driver.Value{{nil, "w11f-op", "group", true, nil, nil}}})
	if _, err := readLegacyTargets(ctx, db2, "w11f-op"); err == nil {
		t.Fatal("targets scan fault must fail")
	}
	// readLegacyViewers 同型。
	script3 := &w9bScript{}
	db3 := w9bOpen(t, script3)
	script3.steps = append(script3.steps, w9bStep{matcher: []string{"FROM operation_log_viewers"}, rowsErr: w11fBoom})
	if _, err := readLegacyViewers(ctx, db3, "w11f-op"); err == nil {
		t.Fatal("viewers query fault must fail")
	}
	script4 := &w9bScript{}
	db4 := w9bOpen(t, script4)
	script4.steps = append(script4.steps, w9bStep{matcher: []string{"FROM operation_log_viewers"}, cols: []string{"id", "operation_log_id", "system_account_id", "visibility_reason"}, rows: [][]driver.Value{{nil, "w11f-op", true, "owner"}}})
	if _, err := readLegacyViewers(ctx, db4, "w11f-op"); err == nil {
		t.Fatal("viewers scan fault must fail")
	}
}

// w11fRowsScanner 直接构造 scanLegacyOperationLog 的坏行。
type w11fBadScanner struct{ values []driver.Value }

func (s w11fBadScanner) Scan(targets ...any) error {
	if len(s.values) != len(targets) {
		return w11fBoom
	}
	for index := range targets {
		if s.values[index] == nil {
			return fmt.Errorf("w11f null at %d", index)
		}
	}
	return w11fBoom
}

func TestW11FLegacySearchTermsBranches(t *testing.T) {
	ctx := context.Background()
	script := &w9bScript{}
	db := w9bOpen(t, script)
	// legacySearchTerms: 查询错误。
	script.steps = append(script.steps, w9bStep{matcher: []string{"operation_log_summary_search_terms"}, rowsErr: w11fBoom})
	if _, err := legacySearchTerms(ctx, w11fQueryerDB{db}, "w11f-op"); err == nil {
		t.Fatal("search terms fault must fail")
	}
}

// w11fQueryerDB 适配 legacyQueryer。
type w11fQueryerDB struct{ db *sql.DB }

func (q w11fQueryerDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return q.db.QueryContext(ctx, query, args...)
}

var _ = io.EOF

// ---------------------------------------------------------------------------
// 故障连接（store 错误臂）
// ---------------------------------------------------------------------------

type w11fStoreRule struct {
	substr   string
	queryErr error
	execErr  error
	limit    int
	hits     int
}

type w11fStoreScript struct {
	mu        sync.Mutex
	rules     []*w11fStoreRule
	beginErr  error
	commitErr error
}

func (s *w11fStoreScript) failQuery(substr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = append(s.rules, &w11fStoreRule{substr: substr, queryErr: w11fBoom})
}

func (s *w11fStoreScript) failExec(substr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = append(s.rules, &w11fStoreRule{substr: substr, execErr: w11fBoom})
}

func (s *w11fStoreScript) failCommitOnce() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commitErr = w11fBoom
}

func (s *w11fStoreScript) failBeginOnce() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.beginErr = w11fBoom
}

func (s *w11fStoreScript) take(query string) *w11fStoreRule {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rules {
		if !strings.Contains(query, r.substr) {
			continue
		}
		if r.limit >= 0 && r.hits >= max(r.limit, 1) {
			continue
		}
		r.hits++
		return r
	}
	return nil
}

type w11fStoreConnector struct {
	base   driver.Connector
	script *w11fStoreScript
}

func (c w11fStoreConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &w11fStoreConn{base: conn, script: c.script}, nil
}

func (c w11fStoreConnector) Driver() driver.Driver { return c.base.Driver() }

type w11fStoreConn struct {
	base   driver.Conn
	script *w11fStoreScript
}

func (c *w11fStoreConn) Prepare(query string) (driver.Stmt, error) { return c.base.Prepare(query) }
func (c *w11fStoreConn) Close() error                              { return c.base.Close() }

func (c *w11fStoreConn) Begin() (driver.Tx, error) {
	if err := c.script.beginErrOnce(); err != nil {
		return nil, err
	}
	tx, err := c.base.Begin()
	if err != nil {
		return nil, err
	}
	return &w11fStoreTx{base: tx, script: c.script}, nil
}

func (c *w11fStoreConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if err := c.script.beginErrOnce(); err != nil {
		return nil, err
	}
	if bt, ok := c.base.(driver.ConnBeginTx); ok {
		tx, err := bt.BeginTx(ctx, opts)
		if err != nil {
			return nil, err
		}
		return &w11fStoreTx{base: tx, script: c.script}, nil
	}
	return c.Begin()
}

func (c *w11fStoreConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if rule := c.script.take(query); rule != nil && rule.execErr != nil {
		return nil, rule.execErr
	}
	return c.base.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c *w11fStoreConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if rule := c.script.take(query); rule != nil && rule.queryErr != nil {
		return nil, rule.queryErr
	}
	return c.base.(driver.QueryerContext).QueryContext(ctx, query, args)
}

type w11fStoreTx struct {
	base   driver.Tx
	script *w11fStoreScript
}

func (t *w11fStoreTx) Commit() error {
	if err := t.script.commitErrOnce(); err != nil {
		_ = t.base.Rollback()
		return err
	}
	return t.base.Commit()
}

func (t *w11fStoreTx) Rollback() error { return t.base.Rollback() }

func (s *w11fStoreScript) beginErrOnce() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.beginErr
	s.beginErr = nil
	return err
}

func (s *w11fStoreScript) commitErrOnce() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.commitErr
	s.commitErr = nil
	return err
}

// w11fFaultStore 构造带故障脚本的 sqlite store。
func w11fFaultStore(t *testing.T) (*sqlStore, *w11fStoreScript, OwnerLease) {
	t.Helper()
	base, err := sqlite.NewConnector("file:w11f-oplog-" + strings.ReplaceAll(t.Name(), "/", "-") + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	script := &w11fStoreScript{}
	db := sql.OpenDB(w11fStoreConnector{base: base, script: script})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	root := t.TempDir()
	business := filepath.Join(root, "business.sqlite3")
	createBusinessSettings(t, business, "365")
	opened, err := OpenStore(Config{Mode: ModeSQLite, InstanceID: "w11f-owner", DatabasePath: filepath.Join(root, "operation.sqlite3"), BusinessSettingsPath: business, OwnerLease: time.Minute})
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
	lease, ok, err := store.AcquireOwnerLease(ctx, "w11f-owner", time.Minute)
	if err != nil || !ok {
		t.Fatalf("lease = %v/%v", ok, err)
	}
	return store, script, lease
}

func w11fInput(id string) Input {
	return Input{
		ID: id, ActorSystemAccountID: "w11f-actor", ActorRole: "admin", Mode: "admin",
		Module: "w11f", Action: "test", OperationKey: "w11f.test", ResourceType: "test",
		ResourceID: id, Summary: "w11f 摘要 " + id, DetailLevel: "full", VisibilityScope: "admin_only",
		CreatedAt: "2026-09-01T00:00:00.000Z",
	}
}

func TestW11FStoreFaultArms(t *testing.T) {
	ctx := context.Background()
	store, script, lease := w11fFaultStore(t)

	// Persist 正常 + begin/commit/INSERT 故障。
	if _, err := store.Persist(ctx, lease, w11fInput("w11f-op-1")); err != nil {
		t.Fatal(err)
	}
	script.failBeginOnce()
	if _, err := store.Persist(ctx, lease, w11fInput("w11f-op-2")); err == nil {
		t.Fatal("persist begin fault must fail")
	}
	script.failCommitOnce()
	if _, err := store.Persist(ctx, lease, w11fInput("w11f-op-3")); err == nil {
		t.Fatal("persist commit fault must fail")
	}
	script.failExec("INSERT INTO operation_logs")
	if _, err := store.Persist(ctx, lease, w11fInput("w11f-op-4")); err == nil {
		t.Fatal("persist insert fault must fail")
	}
	// 重复 ID → 幂等忽略。
	ignored, err := store.Persist(ctx, lease, w11fInput("w11f-op-1"))
	if err != nil || !ignored {
		t.Fatalf("idempotent persist = %v/%v", ignored, err)
	}

	// List: 行查询错误 / 名字解析错误。
	script.failQuery("FROM operation_logs ol")
	if _, err := store.List(ctx, ListOptions{}); err == nil {
		t.Fatal("list rows fault must fail")
	}

	// Detail: 缺失 / 变更查询错误 / targets 错误 / viewers 错误。
	if _, found, err := store.Detail(ctx, "w11f-missing", ""); err != nil || found {
		t.Fatalf("detail missing = %v/%v", found, err)
	}
	script.failQuery("SELECT changes_json")
	if _, _, err := store.Detail(ctx, "w11f-op-1", ""); err == nil {
		t.Fatal("detail changes fault must fail")
	}
	script.failQuery("FROM operation_log_targets")
	if _, _, err := store.Detail(ctx, "w11f-op-1", ""); err == nil {
		t.Fatal("detail targets fault must fail")
	}
	script.failQuery("FROM operation_log_viewers")
	if _, _, err := store.Detail(ctx, "w11f-op-1", ""); err == nil {
		t.Fatal("detail viewers fault must fail")
	}

	// CleanupRetention: begin / 行查询 / 删除 / commit 故障。
	script.failBeginOnce()
	if _, err := store.CleanupRetention(ctx, lease, time.Now().Add(time.Hour), 5); err == nil {
		t.Fatal("cleanup begin fault must fail")
	}
	script.failQuery("WHERE created_at<? ORDER BY created_at,id LIMIT ?")
	if _, err := store.CleanupRetention(ctx, lease, time.Now().Add(time.Hour), 5); err == nil {
		t.Fatal("cleanup select fault must fail")
	}
	script.failExec("DELETE FROM operation_logs")
	if _, err := store.CleanupRetention(ctx, lease, time.Now().Add(time.Hour), 5); err == nil {
		t.Fatal("cleanup delete fault must fail")
	}
	script.failCommitOnce()
	if _, err := store.CleanupRetention(ctx, lease, time.Now().Add(time.Hour), 5); err == nil {
		t.Fatal("cleanup commit fault must fail")
	}
	// 正常删除。
	deleted, err := store.CleanupRetention(ctx, lease, time.Now().Add(time.Hour), 5)
	if err != nil || deleted != 1 {
		t.Fatalf("cleanup hit = %d/%v", deleted, err)
	}
}
