package auditlog

// w16a 覆盖收尾第三批：基于 SQL 子串脚本的故障注入大扫除（w14h 模式扩展版）。
// 在 w14h 能力之上新增：Rows.Scan/Err/Close 注入、Result.RowsAffected 注入、
// 读回值替换（PRAGMA 校验臂）。全部使用独立命名的共享内存 SQLite 库，
// 每个子测试互不干扰；不触碰真实业务库，也不起任何网络服务。

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sqlite "modernc.org/sqlite"
)

var w16aBoom = errors.New("w16a injected boom")

type w16aRule struct {
	substr          string
	queryErr        error
	execErr         error
	scanNil         bool
	rowsErr         error
	closeErr        error
	rowsAffectedErr error
	override        driver.Value
	limit           int
	hits            int
	// exempt 标记负对照臂：注册后故意不命中，豁免 assertRulesFired。
	exempt bool
}

type w16aScript struct {
	mu        sync.Mutex
	t         *testing.T
	rules     []*w16aRule
	beginErr  error
	commitErr error
}

// allowUnfired 把规则标记为负对照（故意不命中），Cleanup 断言跳过。
func (r *w16aRule) allowUnfired() *w16aRule {
	r.exempt = true
	return r
}

// assertRulesFired 在测试收尾断言所有注册规则都被真实消费过（hits>0）：
// 子串与生产 SQL 漂移导致规则永不命中的伪覆盖臂在此变红。
func (s *w16aScript) assertRulesFired() {
	if s.t == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rules {
		if r.hits == 0 && !r.exempt {
			s.t.Errorf("w16a 注入规则未命中（伪覆盖）：substr=%q", r.substr)
		}
	}
}

func (s *w16aScript) add(rule *w16aRule) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = append(s.rules, rule)
}

func (s *w16aScript) failQuery(substr string) *w16aRule {
	r := &w16aRule{substr: substr, queryErr: w16aBoom}
	s.add(r)
	return r
}

func (s *w16aScript) failExec(substr string) *w16aRule {
	r := &w16aRule{substr: substr, execErr: w16aBoom}
	s.add(r)
	return r
}

func (s *w16aScript) failScan(substr string) *w16aRule {
	r := &w16aRule{substr: substr, scanNil: true}
	s.add(r)
	return r
}

func (s *w16aScript) failRowsErr(substr string) *w16aRule {
	r := &w16aRule{substr: substr, rowsErr: w16aBoom}
	s.add(r)
	return r
}

func (s *w16aScript) failClose(substr string) *w16aRule {
	r := &w16aRule{substr: substr, closeErr: w16aBoom}
	s.add(r)
	return r
}

func (s *w16aScript) failRowsAffected(substr string) *w16aRule {
	r := &w16aRule{substr: substr, rowsAffectedErr: w16aBoom}
	s.add(r)
	return r
}

func (s *w16aScript) overrideResult(substr string, value driver.Value) *w16aRule {
	r := &w16aRule{substr: substr, override: value}
	s.add(r)
	return r
}

func (s *w16aScript) skipExec(substr string, n int) *w16aRule {
	r := &w16aRule{substr: substr, limit: n}
	s.add(r)
	return r
}

func (s *w16aScript) take(query string, applies func(*w16aRule) bool) *w16aRule {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rule := range s.rules {
		if !applies(rule) || !strings.Contains(query, rule.substr) {
			continue
		}
		if rule.hits >= max(rule.limit, 1) {
			continue
		}
		rule.hits++
		return rule
	}
	return nil
}

func w16aRuleIsPass(r *w16aRule) bool {
	return r.queryErr == nil && r.execErr == nil && !r.scanNil && r.rowsErr == nil && r.closeErr == nil && r.rowsAffectedErr == nil && r.override == nil
}

func (s *w16aScript) takeExec(query string) *w16aRule {
	return s.take(query, func(r *w16aRule) bool {
		return r.execErr != nil || r.rowsAffectedErr != nil || w16aRuleIsPass(r)
	})
}

func (s *w16aScript) takeQuery(query string) *w16aRule {
	return s.take(query, func(r *w16aRule) bool {
		return r.queryErr != nil || r.scanNil || r.rowsErr != nil || r.closeErr != nil || r.override != nil || w16aRuleIsPass(r)
	})
}

type w16aConnector struct {
	base   driver.Connector
	script *w16aScript
}

func (c w16aConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &w16aConn{base: conn, script: c.script}, nil
}

func (c w16aConnector) Driver() driver.Driver { return c.base.Driver() }

type w16aConn struct {
	base   driver.Conn
	script *w16aScript
}

func (c *w16aConn) Prepare(query string) (driver.Stmt, error) { return c.base.Prepare(query) }
func (c *w16aConn) Close() error                              { return c.base.Close() }

func (c *w16aConn) Begin() (driver.Tx, error) {
	c.script.mu.Lock()
	beginErr := c.script.beginErr
	c.script.mu.Unlock()
	if beginErr != nil {
		return nil, beginErr
	}
	tx, err := c.base.Begin()
	if err != nil {
		return nil, err
	}
	return &w16aTx{base: tx, script: c.script}, nil
}

func (c *w16aConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Begin()
}

func (c *w16aConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	rule := c.script.takeExec(query)
	if rule != nil && rule.execErr != nil {
		return nil, rule.execErr
	}
	result, err := c.base.(driver.ExecerContext).ExecContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	if rule != nil && rule.rowsAffectedErr != nil {
		return w16aResult{base: result, rowsAffectedErr: rule.rowsAffectedErr}, nil
	}
	return result, nil
}

func (c *w16aConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	rule := c.script.takeQuery(query)
	if rule != nil && rule.queryErr != nil {
		return nil, rule.queryErr
	}
	rows, err := c.base.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	if rule == nil {
		return rows, nil
	}
	return &w16aRows{base: rows, rule: rule}, nil
}

type w16aTx struct {
	base   driver.Tx
	script *w16aScript
}

func (t *w16aTx) Commit() error {
	t.script.mu.Lock()
	commitErr := t.script.commitErr
	t.script.mu.Unlock()
	if commitErr != nil {
		_ = t.base.Rollback()
		return commitErr
	}
	return t.base.Commit()
}

func (t *w16aTx) Rollback() error { return t.base.Rollback() }

type w16aResult struct {
	base            driver.Result
	rowsAffectedErr error
}

func (r w16aResult) LastInsertId() (int64, error) { return r.base.LastInsertId() }

func (r w16aResult) RowsAffected() (int64, error) {
	if r.rowsAffectedErr != nil {
		return 0, r.rowsAffectedErr
	}
	return r.base.RowsAffected()
}

type w16aRows struct {
	base driver.Rows
	rule *w16aRule
	done bool
}

func (r *w16aRows) Columns() []string { return r.base.Columns() }

func (r *w16aRows) Close() error {
	closeErr := r.base.Close()
	if r.rule.closeErr != nil {
		return r.rule.closeErr
	}
	return closeErr
}

func (r *w16aRows) Next(dest []driver.Value) error {
	if r.rule.override != nil && !r.done {
		r.done = true
		dest[0] = r.rule.override
		return nil
	}
	err := r.base.Next(dest)
	if err == nil && r.rule.scanNil && !r.done {
		r.done = true
		for i := range dest {
			dest[i] = nil
		}
		return nil
	}
	if err == io.EOF && r.rule.rowsErr != nil {
		return r.rule.rowsErr
	}
	return err
}

// w16aFaultStore 构造带故障脚本的 SQLite store（共享内存库按子测试命名）。
func w16aFaultStore(t *testing.T) (*sqlStore, *w16aScript, OwnerLease) {
	t.Helper()
	base, err := sqlite.NewConnector("file:w16a-audit-" + strings.ReplaceAll(t.Name(), "/", "-") + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	script := &w16aScript{t: t}
	t.Cleanup(script.assertRulesFired)
	db := sql.OpenDB(w16aConnector{base: base, script: script})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	cfg := sqliteConfig(t, t.TempDir())
	store := &sqlStore{db: db, mode: ModeSQLite, blobDir: cfg.PayloadBlobDirectory, hotDir: filepath.Join(filepath.Dir(cfg.AuditDatabasePath), "w16a-hot")}
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	// sqliteSchema 自带 PRAGMA foreign_keys = ON；以下用例需要悬空引用行，
	// 在本连接上显式关闭外键（仅影响本测试库连接）。
	if _, err := db.Exec("PRAGMA foreign_keys = OFF"); err != nil {
		t.Fatal(err)
	}
	lease, ok, err := store.AcquireOwnerLease(ctx, "w16a-owner", time.Minute)
	if err != nil || !ok {
		t.Fatalf("lease=%v/%v", ok, err)
	}
	return store, script, lease
}

// w16aSuccessInput 构造落在 success hot trim 窗口内的成功日志。
func w16aSuccessInput(id string) AuditLogInput {
	input := w14hPayloadInput(id, `{"ok":"w16a"}`)
	input.AuditOutcome = AuditOutcomeSuccess
	input.Success = true
	input.SampleReason = "success_hot_full_retention"
	return input
}

func w16aTrimConfig() RetentionConfig {
	hot := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	return RetentionConfig{
		SuccessHotCutoff: hot,
		SuccessCutoff:    hot.Add(-24 * time.Hour),
		FailureCutoff:    hot,
		ErrorGroupCutoff: hot,
		BatchSize:        50,
	}
}

func w16aSeedPendingGC(t *testing.T, store *sqlStore, blobID, storageKey string) {
	t.Helper()
	if _, err := store.db.Exec(`INSERT INTO audit_payload_blob_gc (blob_id, storage_key, scheduled_at) VALUES (?,?,?)`, blobID, storageKey, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
}

func TestW16aPersistFaultSweep(t *testing.T) {
	body := `{"body":"w16a persist"}`
	t.Run("verify lease rows affected", func(t *testing.T) {
		store, script, lease := w16aFaultStore(t)
		script.failRowsAffected("UPDATE audit_log_owner_leases")
		if _, err := store.Persist(context.Background(), lease, w14hPayloadInput("w16a-p-vlra", body)); err == nil {
			t.Fatal("租约校验 RowsAffected 失败必须透传")
		}
	})
	t.Run("reactivate gc mismatch delete fail", func(t *testing.T) {
		store, script, lease := w16aFaultStore(t)
		record, _, err := newBlobRecord([]byte(body), "application/json", "")
		if err != nil {
			t.Fatal(err)
		}
		w16aSeedPendingGC(t, store, record.id, record.storageKey)
		script.failExec("DELETE FROM audit_payload_blob_gc")
		if _, err := store.Persist(context.Background(), lease, w14hPayloadInput("w16a-p-gc1", body)); err == nil || !strings.Contains(err.Error(), "清理失配的 F3 pending blob GC 失败") {
			t.Fatalf("失配 GC 清理失败必须透传: %v", err)
		}
	})
	t.Run("reactivate gc size read fail", func(t *testing.T) {
		store, script, lease := w16aFaultStore(t)
		record, _, err := newBlobRecord([]byte(body), "application/json", "")
		if err != nil {
			t.Fatal(err)
		}
		w16aSeedPendingGC(t, store, record.id, record.storageKey)
		script.failQuery("SELECT compressed_size_bytes FROM")
		if _, err := store.Persist(context.Background(), lease, w14hPayloadInput("w16a-p-gc2", body)); err == nil || !strings.Contains(err.Error(), "读取 F3 pending blob 元数据失败") {
			t.Fatalf("pending blob 元数据读取失败必须透传: %v", err)
		}
	})
	t.Run("reactivate gc storage key empty", func(t *testing.T) {
		store, _, lease := w16aFaultStore(t)
		input := w14hPayloadInput("w16a-p-gc3-progress", body)
		input.LifecycleStatus = LifecycleInProgress
		if _, err := store.Persist(context.Background(), lease, input); err != nil {
			t.Fatal(err)
		}
		record, _, err := newBlobRecord([]byte(body), "application/json", "")
		if err != nil {
			t.Fatal(err)
		}
		w16aSeedPendingGC(t, store, record.id, "   ")
		if _, err := store.Persist(context.Background(), lease, w14hPayloadInput("w16a-p-gc3", body)); err == nil {
			t.Fatal("空 storage_key 的 pending GC 必须失败")
		}
	})
	t.Run("reactivate gc metadata delete fail", func(t *testing.T) {
		store, script, lease := w16aFaultStore(t)
		input := w14hPayloadInput("w16a-p-gc4-progress", body)
		input.LifecycleStatus = LifecycleInProgress
		if _, err := store.Persist(context.Background(), lease, input); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(store.blobDir, filepath.FromSlash(recordStorageKey(t, store, body)))); err != nil {
			t.Fatal(err)
		}
		record, _, err := newBlobRecord([]byte(body), "application/json", "")
		if err != nil {
			t.Fatal(err)
		}
		w16aSeedPendingGC(t, store, record.id, record.storageKey)
		script.failExec("DELETE FROM audit_payload_blobs WHERE id=? AND NOT EXISTS")
		if _, err := store.Persist(context.Background(), lease, w14hPayloadInput("w16a-p-gc4", body)); err == nil || !strings.Contains(err.Error(), "删除缺失物理文件的 F3 pending blob 元数据失败") {
			t.Fatalf("缺失文件元数据删除失败必须透传: %v", err)
		}
	})
	t.Run("reactivate gc rows affected fail", func(t *testing.T) {
		store, script, lease := w16aFaultStore(t)
		input := w14hPayloadInput("w16a-p-gc5-progress", body)
		input.LifecycleStatus = LifecycleInProgress
		if _, err := store.Persist(context.Background(), lease, input); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(store.blobDir, filepath.FromSlash(recordStorageKey(t, store, body)))); err != nil {
			t.Fatal(err)
		}
		record, _, err := newBlobRecord([]byte(body), "application/json", "")
		if err != nil {
			t.Fatal(err)
		}
		w16aSeedPendingGC(t, store, record.id, record.storageKey)
		script.failRowsAffected("DELETE FROM audit_payload_blobs WHERE id=? AND NOT EXISTS")
		if _, err := store.Persist(context.Background(), lease, w14hPayloadInput("w16a-p-gc5", body)); err == nil || !strings.Contains(err.Error(), "读取缺失物理文件的 F3 pending blob 删除结果失败") {
			t.Fatalf("删除结果读取失败必须透传: %v", err)
		}
	})
	t.Run("reactivate gc refs regained", func(t *testing.T) {
		store, _, lease := w16aFaultStore(t)
		input := w14hPayloadInput("w16a-p-gc6-progress", body)
		input.LifecycleStatus = LifecycleInProgress
		if _, err := store.Persist(context.Background(), lease, input); err != nil {
			t.Fatal(err)
		}
		storageKey := recordStorageKey(t, store, body)
		if err := os.Remove(filepath.Join(store.blobDir, filepath.FromSlash(storageKey))); err != nil {
			t.Fatal(err)
		}
		record, _, err := newBlobRecord([]byte(body), "application/json", "")
		if err != nil {
			t.Fatal(err)
		}
		w16aSeedPendingGC(t, store, record.id, record.storageKey)
		if _, err := store.db.Exec(`INSERT INTO audit_payload_refs (id, audit_log_id, part_type, sequence_index, capture_status, created_at, headers_blob_id) VALUES ('w16a-ref-gc6','w16a-missing-log','client_request',0,'complete','2026-08-09T12:00:00Z',?)`, record.id); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Persist(context.Background(), lease, w14hPayloadInput("w16a-p-gc6", body)); err == nil || !strings.Contains(err.Error(), "重新获得引用") {
			t.Fatalf("引用仍在时必须拒绝: %v", err)
		}
	})
	t.Run("reactivate gc final delete fail", func(t *testing.T) {
		store, script, lease := w16aFaultStore(t)
		input := w14hPayloadInput("w16a-p-gc7-progress", body)
		input.LifecycleStatus = LifecycleInProgress
		if _, err := store.Persist(context.Background(), lease, input); err != nil {
			t.Fatal(err)
		}
		record, _, err := newBlobRecord([]byte(body), "application/json", "")
		if err != nil {
			t.Fatal(err)
		}
		w16aSeedPendingGC(t, store, record.id, record.storageKey)
		script.failExec("DELETE FROM audit_payload_blob_gc")
		if _, err := store.Persist(context.Background(), lease, w14hPayloadInput("w16a-p-gc7", body)); err == nil || !strings.Contains(err.Error(), "取消 F3 pending blob GC 失败") {
			t.Fatalf("pending GC 取消失败必须透传: %v", err)
		}
	})
	t.Run("upsert log rows affected", func(t *testing.T) {
		store, script, lease := w16aFaultStore(t)
		script.failRowsAffected("INSERT INTO audit_logs")
		if _, err := store.Persist(context.Background(), lease, w14hPayloadInput("w16a-p-ul", body)); err == nil {
			t.Fatal("audit_logs 写入结果读取失败必须透传")
		}
	})
	t.Run("header blob touch fail", func(t *testing.T) {
		store, script, lease := w16aFaultStore(t)
		input := w14hPayloadInput("w16a-p-hdr", body)
		input.Payloads[0].Headers = map[string]HeaderValues{"x-w16a": {Values: []string{"one"}}}
		script.failExec("SET last_seen_at")
		if _, err := store.Persist(context.Background(), lease, input); err == nil {
			t.Fatal("headers blob 时间更新失败必须透传")
		}
	})
	t.Run("payload refs rows affected", func(t *testing.T) {
		store, script, lease := w16aFaultStore(t)
		script.failRowsAffected("INSERT INTO audit_payload_refs")
		if _, err := store.Persist(context.Background(), lease, w14hPayloadInput("w16a-p-refs", body)); err == nil {
			t.Fatal("refs 写入结果读取失败必须透传")
		}
	})
	t.Run("existing canonical content encoding retained", func(t *testing.T) {
		store, _, lease := w16aFaultStore(t)
		ctx := context.Background()
		first := w14hPayloadInput("w16a-p-enc-1", body)
		first.Payloads[0].ContentType = "text/w16a-enc"
		first.Payloads[0].ContentEncoding = "gzip"
		first.Payloads[0].Headers = map[string]HeaderValues{"x-w16a": {Values: []string{"keep"}}}
		if _, err := store.Persist(ctx, lease, first); err != nil {
			t.Fatal(err)
		}
		second := w14hPayloadInput("w16a-p-enc-2", body)
		second.Payloads[0].ContentType = "text/w16a-enc"
		second.Payloads[0].Headers = map[string]HeaderValues{"x-w16a": {Values: []string{"keep"}}}
		if _, err := store.Persist(ctx, lease, second); err != nil {
			t.Fatal(err)
		}
		var contentEncoding sql.NullString
		if err := store.db.QueryRow(`SELECT content_encoding FROM audit_payload_blobs WHERE content_type='text/w16a-enc'`).Scan(&contentEncoding); err != nil {
			t.Fatal(err)
		}
		if !contentEncoding.Valid || contentEncoding.String != "gzip" {
			t.Fatalf("既有 canonical content_encoding 必须保留: %+v", contentEncoding)
		}
	})
}

func recordStorageKey(t *testing.T, store *sqlStore, body string) string {
	t.Helper()
	record, _, err := newBlobRecord([]byte(body), "application/json", "")
	if err != nil {
		t.Fatal(err)
	}
	var storageKey string
	if err := store.db.QueryRow(`SELECT storage_key FROM audit_payload_blobs WHERE sha256=?`, record.sha256).Scan(&storageKey); err != nil {
		t.Fatal(err)
	}
	return storageKey
}

func TestW16aRetentionFaultSweep(t *testing.T) {
	ctx := context.Background()
	nonPersistedRow := func(t *testing.T, store *sqlStore) {
		t.Helper()
		if _, err := store.db.Exec(`INSERT INTO audit_logs (id,trace_id,traffic_source,method,path,audit_outcome,success,sample_bucket,sample_reason,started_at,ended_at,created_at) VALUES ('w16a-np','w16a-trace','account_health_check','POST','/v1/x','gateway_failed',0,0,'test','2026-08-09T12:00:00Z','2026-08-09T12:00:00Z','2026-08-09T12:00:00Z')`); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("begin fail", func(t *testing.T) {
		store, script, lease := w16aFaultStore(t)
		script.beginErr = w16aBoom
		if _, err := store.CleanupRetention(ctx, lease, w16aTrimConfig()); err == nil {
			t.Fatal("retention begin 失败必须透传")
		}
	})
	t.Run("trim refs delete fail", func(t *testing.T) {
		store, script, lease := w16aFaultStore(t)
		if _, err := store.Persist(ctx, lease, w16aSuccessInput("w16a-rt-trim")); err != nil {
			t.Fatal(err)
		}
		script.failExec("DELETE FROM audit_payload_refs")
		if _, err := store.CleanupRetention(ctx, lease, w16aTrimConfig()); err == nil {
			t.Fatal("trim 阶段 refs 删除失败必须透传")
		}
	})
	t.Run("trim metadata-only update fail", func(t *testing.T) {
		store, script, lease := w16aFaultStore(t)
		if _, err := store.Persist(ctx, lease, w16aSuccessInput("w16a-rt-mark")); err != nil {
			t.Fatal(err)
		}
		script.failExec("SET attempt_count=0")
		if _, err := store.CleanupRetention(ctx, lease, w16aTrimConfig()); err == nil || !strings.Contains(err.Error(), "标记 F3 success audit metadata-only 失败") {
			t.Fatalf("metadata-only 标记失败必须透传: %v", err)
		}
	})
	t.Run("trim revalidate scan fail", func(t *testing.T) {
		store, script, lease := w16aFaultStore(t)
		if _, err := store.Persist(ctx, lease, w16aSuccessInput("w16a-rt-reval")); err != nil {
			t.Fatal(err)
		}
		script.failScan("ORDER BY id ASC")
		if _, err := store.CleanupRetention(ctx, lease, w16aTrimConfig()); err == nil {
			t.Fatal("revalidate 扫描失败必须透传")
		}
	})
	t.Run("trim revalidate rows err", func(t *testing.T) {
		store, script, lease := w16aFaultStore(t)
		if _, err := store.Persist(ctx, lease, w16aSuccessInput("w16a-rt-reval2")); err != nil {
			t.Fatal(err)
		}
		script.failRowsErr("ORDER BY id ASC")
		if _, err := store.CleanupRetention(ctx, lease, w16aTrimConfig()); err == nil {
			t.Fatal("revalidate rows 错误必须透传")
		}
	})
	t.Run("non-persisted select fail", func(t *testing.T) {
		store, script, lease := w16aFaultStore(t)
		nonPersistedRow(t, store)
		script.failQuery("traffic_source IN (")
		if _, err := store.CleanupRetention(ctx, lease, w16aTrimConfig()); err == nil {
			t.Fatal("非持久化来源读取失败必须透传")
		}
	})
	t.Run("non-persisted refs fail", func(t *testing.T) {
		store, script, lease := w16aFaultStore(t)
		nonPersistedRow(t, store)
		script.failExec("DELETE FROM audit_payload_refs")
		if _, err := store.CleanupRetention(ctx, lease, w16aTrimConfig()); err == nil {
			t.Fatal("非持久化来源 refs 删除失败必须透传")
		}
	})
	t.Run("non-persisted logs fail", func(t *testing.T) {
		store, script, lease := w16aFaultStore(t)
		nonPersistedRow(t, store)
		script.failExec("DELETE FROM audit_logs WHERE id IN")
		if _, err := store.CleanupRetention(ctx, lease, w16aTrimConfig()); err == nil {
			t.Fatal("非持久化来源日志删除失败必须透传")
		}
	})
	t.Run("delete select fail", func(t *testing.T) {
		store, script, lease := w16aFaultStore(t)
		if _, err := store.Persist(ctx, lease, w14hPayloadInput("w16a-rt-del", `{"dead":"w16a"}`)); err != nil {
			t.Fatal(err)
		}
		script.failQuery("OR (audit_outcome <> 'success'")
		if _, err := store.CleanupRetention(ctx, lease, w16aTrimConfig()); err == nil {
			t.Fatal("删除阶段读取失败必须透传")
		}
	})
	t.Run("delete refs fail", func(t *testing.T) {
		store, script, lease := w16aFaultStore(t)
		if _, err := store.Persist(ctx, lease, w14hPayloadInput("w16a-rt-del2", `{"dead":"w16a"}`)); err != nil {
			t.Fatal(err)
		}
		script.failExec("DELETE FROM audit_payload_refs")
		if _, err := store.CleanupRetention(ctx, lease, w16aTrimConfig()); err == nil {
			t.Fatal("删除阶段 refs 删除失败必须透传")
		}
	})
	t.Run("error group delete fail", func(t *testing.T) {
		store, script, lease := w16aFaultStore(t)
		if _, err := store.db.Exec(`INSERT INTO audit_error_groups (id,fingerprint,window_started_at,window_ended_at,count,created_at,updated_at) VALUES ('w16a-eg','w16a-fp','2026-08-01T00:00:00Z','2026-08-01T00:05:00Z',1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		script.failExec("DELETE FROM audit_error_groups WHERE id IN")
		if _, err := store.CleanupRetention(ctx, lease, w16aTrimConfig()); err == nil || !strings.Contains(err.Error(), "删除 F3 error groups 失败") {
			t.Fatalf("error group 删除失败必须透传: %v", err)
		}
	})
	t.Run("unreferenced blobs query fail", func(t *testing.T) {
		store, script, lease := w16aFaultStore(t)
		if _, err := store.Persist(ctx, lease, w14hPayloadInput("w16a-rt-orphan", `{"orphan":"w16a"}`)); err != nil {
			t.Fatal(err)
		}
		script.failQuery("WHERE NOT EXISTS (SELECT 1 FROM audit_payload_refs")
		if _, err := store.CleanupRetention(ctx, lease, w16aTrimConfig()); err == nil {
			t.Fatal("无引用 blob 查询失败必须透传")
		}
	})
	t.Run("unreferenced blobs scan fail", func(t *testing.T) {
		store, script, lease := w16aFaultStore(t)
		if _, err := store.Persist(ctx, lease, w14hPayloadInput("w16a-rt-orphan2", `{"orphan":"w16a"}`)); err != nil {
			t.Fatal(err)
		}
		script.failScan("WHERE NOT EXISTS (SELECT 1 FROM audit_payload_refs")
		if _, err := store.CleanupRetention(ctx, lease, w16aTrimConfig()); err == nil {
			t.Fatal("无引用 blob 扫描失败必须透传")
		}
	})
	t.Run("schedule gc select fail", func(t *testing.T) {
		store, script, lease := w16aFaultStore(t)
		if _, err := store.Persist(ctx, lease, w14hPayloadInput("w16a-rt-sched", `{"sched":"w16a"}`)); err != nil {
			t.Fatal(err)
		}
		script.failQuery("SELECT storage_key FROM audit_payload_blobs WHERE id=? AND NOT EXISTS")
		if _, err := store.CleanupRetention(ctx, lease, w16aTrimConfig()); err == nil || !strings.Contains(err.Error(), "复核 F3 unreferenced blob 失败") {
			t.Fatalf("GC 复核失败必须透传: %v", err)
		}
	})
	t.Run("hot dir read fail", func(t *testing.T) {
		store, _, lease := w16aFaultStore(t)
		store.hotDir = filepath.Join(t.TempDir(), "w16a-hot|illegal")
		if _, err := store.CleanupRetention(ctx, lease, w16aTrimConfig()); err == nil || !strings.Contains(err.Error(), "读取 F3 hot-search 清理目录失败") {
			t.Fatalf("hot 目录读取失败必须透传: %v", err)
		}
	})
	t.Run("lease before commit fail", func(t *testing.T) {
		store, script, lease := w16aFaultStore(t)
		script.skipExec("UPDATE audit_log_owner_leases", 1)
		script.failExec("UPDATE audit_log_owner_leases")
		if _, err := store.CleanupRetention(ctx, lease, w16aTrimConfig()); err == nil {
			t.Fatal("retention 提交前租约校验失败必须透传")
		}
	})
	t.Run("phase2 revalidate scan fail", func(t *testing.T) {
		store, script, lease := w16aFaultStore(t)
		if _, err := store.Persist(ctx, lease, w16aSuccessInput("w16a-rt-reval3")); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`INSERT INTO audit_logs (id,trace_id,traffic_source,method,path,audit_outcome,success,sample_bucket,sample_reason,started_at,ended_at,created_at) VALUES ('w16a-np2','w16a-trace','account_health_check','POST','/v1/x','gateway_failed',0,0,'test','2026-08-09T12:00:00Z','2026-08-09T12:00:00Z','2026-08-09T12:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		script.skipExec("ORDER BY id ASC", 1)
		script.failScan("ORDER BY id ASC")
		if _, err := store.CleanupRetention(ctx, lease, w16aTrimConfig()); err == nil {
			t.Fatal("phase2 revalidate 扫描失败必须透传")
		}
	})
	t.Run("phase3 revalidate scan fail", func(t *testing.T) {
		store, script, lease := w16aFaultStore(t)
		if _, err := store.Persist(ctx, lease, w14hPayloadInput("w16a-rt-reval4", `{"dead":"w16a"}`)); err != nil {
			t.Fatal(err)
		}
		script.failScan("ORDER BY id ASC")
		if _, err := store.CleanupRetention(ctx, lease, w16aTrimConfig()); err == nil {
			t.Fatal("phase3 revalidate 扫描失败必须透传")
		}
	})
	t.Run("error group select fail", func(t *testing.T) {
		store, script, lease := w16aFaultStore(t)
		if _, err := store.db.Exec(`INSERT INTO audit_error_groups (id,fingerprint,window_started_at,window_ended_at,count,created_at,updated_at) VALUES ('w16a-eg2','w16a-fp2','2026-08-01T00:00:00Z','2026-08-01T00:05:00Z',1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		script.failQuery("FROM audit_error_groups WHERE updated_at")
		if _, err := store.CleanupRetention(ctx, lease, w16aTrimConfig()); err == nil {
			t.Fatal("error group 读取失败必须透传")
		}
	})
	t.Run("retention ids scan fail", func(t *testing.T) {
		store, script, lease := w16aFaultStore(t)
		if _, err := store.Persist(ctx, lease, w16aSuccessInput("w16a-rt-scan")); err != nil {
			t.Fatal(err)
		}
		script.failScan("ORDER BY created_at ASC,id ASC LIMIT")
		if _, err := store.CleanupRetention(ctx, lease, w16aTrimConfig()); err == nil {
			t.Fatal("retention IDs 扫描失败必须透传")
		}
	})
	t.Run("retention ids rows err", func(t *testing.T) {
		store, script, lease := w16aFaultStore(t)
		if _, err := store.Persist(ctx, lease, w16aSuccessInput("w16a-rt-rowserr")); err != nil {
			t.Fatal(err)
		}
		script.failRowsErr("ORDER BY created_at ASC,id ASC LIMIT")
		if _, err := store.CleanupRetention(ctx, lease, w16aTrimConfig()); err == nil {
			t.Fatal("retention IDs rows 错误必须透传")
		}
	})
	t.Run("children refs select fail", func(t *testing.T) {
		store, script, lease := w16aFaultStore(t)
		if _, err := store.Persist(ctx, lease, w14hPayloadInput("w16a-rt-child", `{"child":"w16a"}`)); err != nil {
			t.Fatal(err)
		}
		script.failQuery("SELECT headers_blob_id,body_blob_id FROM")
		if _, err := store.CleanupRetention(ctx, lease, w16aTrimConfig()); err == nil {
			t.Fatal("子行引用读取失败必须透传")
		}
	})
	t.Run("children refs rows err", func(t *testing.T) {
		store, script, lease := w16aFaultStore(t)
		if _, err := store.Persist(ctx, lease, w14hPayloadInput("w16a-rt-child3", `{"child":"w16a"}`)); err != nil {
			t.Fatal(err)
		}
		script.failRowsErr("SELECT headers_blob_id,body_blob_id FROM")
		if _, err := store.CleanupRetention(ctx, lease, w16aTrimConfig()); err == nil {
			t.Fatal("子行引用 rows 错误必须透传")
		}
	})
	t.Run("revalidate ids lock error is pg only", func(t *testing.T) {
		// SQLite 上 lockAuditLogLifecycleIDs 恒为 no-op：这里仅验证空入参直通。
		store, _, _ := w16aFaultStore(t)
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		ids, err := store.lockAndRevalidateRetentionAuditIDs(ctx, tx, nil, "id='x'", nil)
		if err != nil || ids != nil {
			t.Fatalf("空入参必须直通: %v %v", ids, err)
		}
		_ = tx.Rollback()
	})
}

func TestW16aScheduledBlobGCFaultSweep(t *testing.T) {
	ctx := context.Background()
	body := `{"gc":"w16a"}`
	// 预置：一条已删除日志的 pending GC（真实 retention 流程产出）。retention
	// 提交后的物理清理阶段用一次性规则拦下，使 pending GC 行留在表里。
	pending := func(t *testing.T) (*sqlStore, *w16aScript, OwnerLease, pendingBlobGCRow) {
		t.Helper()
		store, script, lease := w16aFaultStore(t)
		if _, err := store.Persist(ctx, lease, w14hPayloadInput("w16a-gc-src", body)); err != nil {
			t.Fatal(err)
		}
		script.failQuery("ORDER BY scheduled_at")
		if _, err := store.CleanupRetention(ctx, lease, w16aTrimConfig()); err == nil {
			t.Fatal("预置阶段应因注入失败而保留 pending GC")
		}
		var row pendingBlobGCRow
		if err := store.db.QueryRow(`SELECT blob_id,storage_key FROM audit_payload_blob_gc LIMIT 1`).Scan(&row.blobID, &row.storageKey); err != nil {
			t.Fatal(err)
		}
		return store, script, lease, row
	}
	t.Run("list query fail", func(t *testing.T) {
		store, script, lease, _ := pending(t)
		script.failQuery("ORDER BY scheduled_at")
		if _, err := store.cleanupScheduledBlobFiles(ctx, lease, 10); err == nil {
			t.Fatal("pending GC 列表读取失败必须透传")
		}
	})
	t.Run("list scan fail", func(t *testing.T) {
		store, script, lease, _ := pending(t)
		script.failScan("ORDER BY scheduled_at")
		if _, err := store.cleanupScheduledBlobFiles(ctx, lease, 10); err == nil || !strings.Contains(err.Error(), "读取 F3 pending blob GC 行失败") {
			t.Fatalf("pending GC 行扫描失败必须透传: %v", err)
		}
	})
	t.Run("list rows err", func(t *testing.T) {
		store, script, lease, _ := pending(t)
		script.failRowsErr("ORDER BY scheduled_at")
		if _, err := store.cleanupScheduledBlobFiles(ctx, lease, 10); err == nil {
			t.Fatal("pending GC 遍历失败必须透传")
		}
	})
	t.Run("list close fail", func(t *testing.T) {
		store, script, lease, _ := pending(t)
		script.failClose("ORDER BY scheduled_at")
		if _, err := store.cleanupScheduledBlobFiles(ctx, lease, 10); err == nil {
			t.Fatal("pending GC 查询关闭失败必须透传")
		}
	})
	t.Run("begin fail", func(t *testing.T) {
		store, script, lease, row := pending(t)
		script.beginErr = w16aBoom
		if _, err := store.cleanupScheduledBlobFile(ctx, lease, row); err == nil {
			t.Fatal("pending GC 事务开始失败必须透传")
		}
	})
	t.Run("gc state select fail", func(t *testing.T) {
		store, script, lease, row := pending(t)
		script.failQuery("SELECT storage_key FROM audit_payload_blob_gc WHERE blob_id")
		if _, err := store.cleanupScheduledBlobFile(ctx, lease, row); err == nil {
			t.Fatal("pending GC 状态读取失败必须透传")
		}
	})
	t.Run("refs exists select fail", func(t *testing.T) {
		store, script, lease, row := pending(t)
		script.failQuery("SELECT EXISTS (SELECT 1 FROM audit_payload_refs")
		if _, err := store.cleanupScheduledBlobFile(ctx, lease, row); err == nil {
			t.Fatal("引用检查失败必须透传")
		}
	})
	t.Run("re-referenced cancel success", func(t *testing.T) {
		store, _, lease, row := pending(t)
		if _, err := store.db.Exec(`INSERT INTO audit_payload_refs (id, audit_log_id, part_type, sequence_index, capture_status, created_at, headers_blob_id) VALUES ('w16a-ref-cancel','w16a-missing-log','client_request',0,'complete','2026-08-09T12:00:00Z',?)`, row.blobID); err != nil {
			t.Fatal(err)
		}
		removed, err := store.cleanupScheduledBlobFile(ctx, lease, row)
		if err != nil || removed {
			t.Fatalf("重引用必须取消 GC 且不删除: removed=%t err=%v", removed, err)
		}
		var remaining int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM audit_payload_blob_gc WHERE blob_id=?`, row.blobID).Scan(&remaining); err != nil {
			t.Fatal(err)
		}
		if remaining != 0 {
			t.Fatal("重引用后 pending GC 必须被取消")
		}
	})
	t.Run("re-referenced gc delete fail", func(t *testing.T) {
		store, script, lease, row := pending(t)
		if _, err := store.db.Exec(`INSERT INTO audit_payload_refs (id, audit_log_id, part_type, sequence_index, capture_status, created_at, headers_blob_id) VALUES ('w16a-ref-cancel2','w16a-missing-log','client_request',0,'complete','2026-08-09T12:00:00Z',?)`, row.blobID); err != nil {
			t.Fatal(err)
		}
		script.failExec("DELETE FROM audit_payload_blob_gc WHERE blob_id=?")
		if _, err := store.cleanupScheduledBlobFile(ctx, lease, row); err == nil || !strings.Contains(err.Error(), "取消已重新引用的 F3 pending blob GC 失败") {
			t.Fatalf("重引用取消失败必须透传: %v", err)
		}
	})
	t.Run("re-referenced lease before commit fail", func(t *testing.T) {
		store, script, lease, row := pending(t)
		if _, err := store.db.Exec(`INSERT INTO audit_payload_refs (id, audit_log_id, part_type, sequence_index, capture_status, created_at, headers_blob_id) VALUES ('w16a-ref-cancel3','w16a-missing-log','client_request',0,'complete','2026-08-09T12:00:00Z',?)`, row.blobID); err != nil {
			t.Fatal(err)
		}
		script.skipExec("UPDATE audit_log_owner_leases", 1)
		script.failExec("UPDATE audit_log_owner_leases")
		if _, err := store.cleanupScheduledBlobFile(ctx, lease, row); err == nil {
			t.Fatal("取消路径提交前租约失败必须透传")
		}
	})
	t.Run("blobs exists select fail", func(t *testing.T) {
		store, script, lease, row := pending(t)
		script.failQuery("SELECT EXISTS (SELECT 1 FROM audit_payload_blobs")
		if _, err := store.cleanupScheduledBlobFile(ctx, lease, row); err == nil {
			t.Fatal("元数据存在性检查失败必须透传")
		}
	})
	t.Run("metadata already gone path", func(t *testing.T) {
		store, _, lease, row := pending(t)
		if _, err := store.db.Exec(`DELETE FROM audit_payload_blobs WHERE id=?`, row.blobID); err != nil {
			t.Fatal(err)
		}
		removed, err := store.cleanupScheduledBlobFile(ctx, lease, row)
		if err != nil || removed {
			t.Fatalf("元数据缺失路径必须成功且不计删除: removed=%t err=%v", removed, err)
		}
	})
	t.Run("metadata delete fail", func(t *testing.T) {
		store, script, lease, row := pending(t)
		script.failExec("DELETE FROM audit_payload_blobs WHERE id=? AND NOT EXISTS")
		if _, err := store.cleanupScheduledBlobFile(ctx, lease, row); err == nil || !strings.Contains(err.Error(), "删除 F3 pending blob 元数据失败") {
			t.Fatalf("元数据删除失败必须透传: %v", err)
		}
	})
	t.Run("metadata rows affected fail", func(t *testing.T) {
		store, script, lease, row := pending(t)
		script.failRowsAffected("DELETE FROM audit_payload_blobs WHERE id=? AND NOT EXISTS")
		if _, err := store.cleanupScheduledBlobFile(ctx, lease, row); err == nil {
			t.Fatal("元数据删除结果读取失败必须透传")
		}
	})
	t.Run("final gc delete fail", func(t *testing.T) {
		store, script, lease, row := pending(t)
		script.failExec("DELETE FROM audit_payload_blob_gc WHERE blob_id=?")
		if _, err := store.cleanupScheduledBlobFile(ctx, lease, row); err == nil || !strings.Contains(err.Error(), "清理 F3 pending blob GC 状态失败") {
			t.Fatalf("GC 状态清理失败必须透传: %v", err)
		}
	})
	t.Run("lease before delete commit fail", func(t *testing.T) {
		store, script, lease, row := pending(t)
		script.skipExec("UPDATE audit_log_owner_leases", 1)
		script.failExec("UPDATE audit_log_owner_leases")
		if _, err := store.cleanupScheduledBlobFile(ctx, lease, row); err == nil {
			t.Fatal("删除路径提交前租约失败必须透传")
		}
	})
	t.Run("commit fail", func(t *testing.T) {
		store, script, lease, row := pending(t)
		script.commitErr = w16aBoom
		if _, err := store.cleanupScheduledBlobFile(ctx, lease, row); err == nil || !strings.Contains(err.Error(), "提交 F3 pending blob GC 事务失败") {
			t.Fatalf("GC 提交失败必须透传: %v", err)
		}
	})
}

func TestW16aStoreHelperGaps(t *testing.T) {
	ctx := context.Background()
	t.Run("configure sqlite pragma arms", func(t *testing.T) {
		base, err := sqlite.NewConnector("file:w16a-audit-pragma?mode=memory&cache=shared")
		if err != nil {
			t.Fatal(err)
		}
		script := &w16aScript{}
		db := sql.OpenDB(w16aConnector{base: base, script: script})
		defer db.Close()
		script.failExec("PRAGMA busy_timeout")
		if err := configureSQLite(db); err == nil {
			t.Fatal("busy_timeout 设置失败必须透传")
		}
		script.failQuery("PRAGMA busy_timeout")
		if err := configureSQLite(db); err == nil {
			t.Fatal("busy_timeout 回读失败必须透传")
		}
		script.overrideResult("PRAGMA busy_timeout", int64(1234))
		if err := configureSQLite(db); err == nil || !strings.Contains(err.Error(), "busy_timeout 未生效") {
			t.Fatalf("busy_timeout 不一致必须失败: %v", err)
		}
		script.overrideResult("PRAGMA journal_mode", "delete")
		if err := configureSQLite(db); err == nil || !strings.Contains(err.Error(), "WAL 未生效") {
			t.Fatalf("WAL 未生效必须失败: %v", err)
		}
	})
	t.Run("write blob temps mkdir fail", func(t *testing.T) {
		store, _, lease := w16aFaultStore(t)
		blobDir := t.TempDir()
		store.blobDir = blobDir
		if err := os.WriteFile(filepath.Join(blobDir, "sha256"), []byte("occupied"), 0o600); err != nil {
			t.Fatal(err)
		}
		plans := []blobPlan{{record: blobRecord{storageKey: "sha256/x.blob", compressedSize: 4}, bytes: []byte("w16a")}}
		plans[0].root = blobDir
		if err := store.writeBlobTemps(lease, plans); err == nil {
			t.Fatal("blob 目录被文件占用时必须失败")
		}
	})
	t.Run("cleanup temps with lease cleanup error", func(t *testing.T) {
		store, _, lease := w16aFaultStore(t)
		if err := store.cleanupBlobTempsWithLease(ctx, lease, func() error { return w16aBoom }); err == nil || !strings.Contains(err.Error(), "清理 F3 过期 blob 临时文件失败") {
			t.Fatalf("清理函数失败必须透传: %v", err)
		}
	})
	t.Run("lock and reactivation plan dedup", func(t *testing.T) {
		store, _, _ := w16aFaultStore(t)
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		if err := store.lockBlobLifecyclePlans(ctx, tx, []blobPlan{{}, {record: blobRecord{id: "blob:w16a-dup"}}, {record: blobRecord{id: "blob:w16a-dup"}}}); err != nil {
			t.Fatal(err)
		}
		if err := store.reactivateScheduledBlobGC(ctx, tx, []blobPlan{{}, {record: blobRecord{id: "blob:w16a-dup"}}, {record: blobRecord{id: "blob:w16a-dup"}}}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("resolve blob ids keeps existing encoding", func(t *testing.T) {
		store, _, _ := w16aFaultStore(t)
		digestBytes := sha256.Sum256([]byte("w16a-enc-body"))
		digest := hex.EncodeToString(digestBytes[:])
		if _, err := store.db.Exec(`INSERT INTO audit_payload_blobs (id,sha256,raw_size_bytes,compressed_size_bytes,content_type,content_encoding,compression,storage_key,first_seen_at,last_seen_at,created_at) VALUES ('blob:w16a-enc',?,13,13,'text/w16a-enc','gzip','none','sha256/enc.blob','2026-08-09T12:00:00Z','2026-08-09T12:00:00Z','2026-08-09T12:00:00Z')`, digest); err != nil {
			t.Fatal(err)
		}
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		plans := []blobPlan{{record: blobRecord{sha256: digest, rawSize: 13, contentType: "text/w16a-enc"}}}
		if err := store.resolveBlobIDs(ctx, tx, plans); err != nil {
			t.Fatal(err)
		}
		if plans[0].record.contentEncoding != "gzip" || plans[0].record.id != "blob:w16a-enc" {
			t.Fatalf("既有 blob 元数据必须回填: %+v", plans[0].record)
		}
	})
	t.Run("verify existing blob files dedup", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "sha256"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "sha256", "dedup.blob"), []byte("w16a"), 0o600); err != nil {
			t.Fatal(err)
		}
		record := blobRecord{id: "blob:w16a-dedup", storageKey: "sha256/dedup.blob", compressedSize: 4}
		plans := []blobPlan{{record: record, existing: true, root: root}, {record: record, existing: true, root: root}}
		if err := verifyExistingBlobFiles(plans); err != nil {
			t.Fatal(err)
		}
		// 既有物理文件与元数据不一致 → 拒绝。
		mismatch := []blobPlan{{record: blobRecord{id: "blob:w16a-dedup", storageKey: "sha256/dedup.blob", compressedSize: 5}, existing: true, root: root}}
		if err := verifyExistingBlobFiles(mismatch); err == nil {
			t.Fatal("既有 blob 与元数据不一致必须拒绝")
		}
	})
	t.Run("upsert error group rejects bad created at", func(t *testing.T) {
		store, _, _ := w16aFaultStore(t)
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		input := w14hPayloadInput("w16a-eg-bad", `{"bad":"w16a"}`)
		input.AuditOutcome = AuditOutcomeUpstreamFailed
		input.Success = false
		input.CreatedAt = "not-a-time"
		if err := store.upsertErrorGroup(ctx, tx, input, nil); err == nil || !strings.Contains(err.Error(), "error group createdAt 非法") {
			t.Fatalf("非法 createdAt 必须拒绝: %v", err)
		}
	})
	t.Run("append and cleanup hot verify lease arms", func(t *testing.T) {
		store, script, lease := w16aFaultStore(t)
		script.skipExec("UPDATE audit_log_owner_leases", 1)
		script.failExec("UPDATE audit_log_owner_leases")
		if _, err := store.AppendHotSearch(ctx, lease, []AuditLogInput{w14hPayloadInput("w16a-hot-lease", `{"hot":"w16a"}`)}); err == nil {
			t.Fatal("hot 追加提交前租约校验失败必须透传")
		}
		script.skipExec("UPDATE audit_log_owner_leases", 1)
		script.failExec("UPDATE audit_log_owner_leases")
		if _, err := store.CleanupHotSearch(ctx, lease, time.Now().Add(time.Hour), 4); err == nil {
			t.Fatal("hot 清理提交前租约校验失败必须透传")
		}
	})
}
