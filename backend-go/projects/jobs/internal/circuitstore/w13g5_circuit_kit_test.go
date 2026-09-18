package circuitstore

// w13g5_circuit_kit_test.go 提供可回放的失败注入 SQLite 驱动（包装
// modernc.org/sqlite）与控制面/列表投影合并 fixture，覆盖深层 err 传播臂。
// 注入策略对齐 statsagg/w13g5_statsagg_kit_test.go 与 taskruns/w12b 的既有
// 模式；错误消息固定为 "w13g5 注入失败"，不携带连接串或敏感数据。
//
// 不可达清单（覆盖率登记）：
//   - id.go newRandomUUID 的 rand.Read 失败 panic：crypto/rand 无注入点，
//     失败即进程级故障，无可控路径。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"modernc.org/sqlite"
)

const w13g5CircuitFailDriverName = "w13g5-circuit-fail-sqlite"

var (
	w13g5CircuitFailDriverRegister sync.Once
	w13g5CircuitFailDriversMu      sync.Mutex
	w13g5CircuitFailDrivers        = map[string]*w13g5CircuitSpec{}
)

// w13g5CircuitSpec 描述一次注入：match 命中查询子串；once 只注入一次；
// afterN 从第 N+1 次命中起持续注入；zero=true 静默返回 0 行影响。
type w13g5CircuitSpec struct {
	mu     sync.Mutex
	match  string
	once   bool
	afterN bool
	fired  bool
	skip   int
	zero   bool
}

func (spec *w13g5CircuitSpec) arm(match string) {
	spec.mu.Lock()
	defer spec.mu.Unlock()
	spec.match, spec.once, spec.afterN, spec.fired, spec.skip, spec.zero = match, false, false, false, 0, false
}

func (spec *w13g5CircuitSpec) armOnce(match string) {
	spec.mu.Lock()
	defer spec.mu.Unlock()
	spec.match, spec.once, spec.afterN, spec.fired, spec.skip, spec.zero = match, true, false, false, 0, false
}

func (spec *w13g5CircuitSpec) armAfter(match string, skip int) {
	spec.mu.Lock()
	defer spec.mu.Unlock()
	spec.match, spec.once, spec.afterN, spec.fired, spec.skip, spec.zero = match, true, true, false, skip, false
}

func (spec *w13g5CircuitSpec) armZero(match string) {
	spec.mu.Lock()
	defer spec.mu.Unlock()
	spec.match, spec.once, spec.afterN, spec.fired, spec.skip, spec.zero = match, false, false, false, 0, true
}

func (spec *w13g5CircuitSpec) disarm() {
	spec.mu.Lock()
	defer spec.mu.Unlock()
	spec.match = ""
}

func (spec *w13g5CircuitSpec) hit(key string) (fail, zero bool) {
	spec.mu.Lock()
	defer spec.mu.Unlock()
	if spec.match == "" || !strings.Contains(key, spec.match) {
		return false, false
	}
	if spec.once {
		if spec.skip > 0 {
			spec.skip--
			return false, false
		}
		if !spec.afterN && spec.fired {
			return false, false
		}
		spec.fired = true
	}
	return !spec.zero, spec.zero
}

func w13g5CircuitInjectedErr() error { return errors.New("w13g5 注入失败") }

type w13g5CircuitFailDriver struct{ inner driver.Driver }

func (d *w13g5CircuitFailDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	w13g5CircuitFailDriversMu.Lock()
	spec := w13g5CircuitFailDrivers[w13g5CircuitFailDriverName]
	w13g5CircuitFailDriversMu.Unlock()
	return &w13g5CircuitFailConn{inner: conn, spec: spec}, nil
}

type w13g5CircuitFailConn struct {
	inner driver.Conn
	spec  *w13g5CircuitSpec
}

func (c *w13g5CircuitFailConn) Prepare(query string) (driver.Stmt, error) {
	if fail, _ := c.spec.hit(query); fail {
		return nil, w13g5CircuitInjectedErr()
	}
	stmt, err := c.inner.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &w13g5CircuitFailStmt{inner: stmt, spec: c.spec}, nil
}

func (c *w13g5CircuitFailConn) Close() error { return c.inner.Close() }

func (c *w13g5CircuitFailConn) Begin() (driver.Tx, error) {
	if fail, _ := c.spec.hit("w13g5-BEGIN"); fail {
		return nil, w13g5CircuitInjectedErr()
	}
	tx, err := c.inner.Begin()
	if err != nil {
		return nil, err
	}
	return &w13g5CircuitFailTx{inner: tx, spec: c.spec}, nil
}

func (c *w13g5CircuitFailConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if fail, zero := c.spec.hit(query); fail {
		return nil, w13g5CircuitInjectedErr()
	} else if zero {
		return driver.RowsAffected(0), nil
	}
	if execer, ok := c.inner.(driver.ExecerContext); ok {
		return execer.ExecContext(ctx, query, args)
	}
	stmt, err := c.inner.Prepare(query)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	return stmt.Exec(w13g5CircuitDriverValues(args))
}

func (c *w13g5CircuitFailConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if fail, _ := c.spec.hit(query); fail {
		return nil, w13g5CircuitInjectedErr()
	}
	if queryer, ok := c.inner.(driver.QueryerContext); ok {
		return queryer.QueryContext(ctx, query, args)
	}
	stmt, err := c.inner.Prepare(query)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	return stmt.Query(w13g5CircuitDriverValues(args))
}

type w13g5CircuitFailTx struct {
	inner driver.Tx
	spec  *w13g5CircuitSpec
}

func (tx *w13g5CircuitFailTx) Commit() error {
	if fail, _ := tx.spec.hit("w13g5-COMMIT"); fail {
		return w13g5CircuitInjectedErr()
	}
	return tx.inner.Commit()
}

func (tx *w13g5CircuitFailTx) Rollback() error {
	if fail, _ := tx.spec.hit("w13g5-ROLLBACK"); fail {
		return w13g5CircuitInjectedErr()
	}
	return tx.inner.Rollback()
}

type w13g5CircuitFailStmt struct {
	inner driver.Stmt
	spec  *w13g5CircuitSpec
}

func (s *w13g5CircuitFailStmt) Close() error { return s.inner.Close() }

func (s *w13g5CircuitFailStmt) NumInput() int { return -1 }

func (s *w13g5CircuitFailStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.inner.Exec(args)
}

func (s *w13g5CircuitFailStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.inner.Query(args)
}

func w13g5CircuitDriverValues(args []driver.NamedValue) []driver.Value {
	values := make([]driver.Value, len(args))
	for i, arg := range args {
		values[i] = arg.Value
	}
	return values
}

// w13g5CircuitFixture 打开注入驱动的合并 SQLite 业务库（控制面 + 列表投影
// 最小 schema），返回两个仓储与共享 spec。
func w13g5CircuitFixture(t *testing.T) (*ListAvailabilityRepo, *ControlPlaneRepo, *sql.DB, *w13g5CircuitSpec) {
	t.Helper()
	w13g5CircuitFailDriverRegister.Do(func() {
		sql.Register(w13g5CircuitFailDriverName, &w13g5CircuitFailDriver{inner: &sqlite.Driver{}})
	})
	spec := &w13g5CircuitSpec{}
	w13g5CircuitFailDriversMu.Lock()
	w13g5CircuitFailDrivers[w13g5CircuitFailDriverName] = spec
	w13g5CircuitFailDriversMu.Unlock()
	db, err := sql.Open(w13g5CircuitFailDriverName, "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "w13g5-circuit.sqlite3"))+"?_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		t.Fatalf("打开注入 fixture 失败: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range w13g5CircuitSchema {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("注入 fixture 建表失败: %v", err)
		}
	}
	fixed := func() time.Time { return time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC) }
	listRepo, err := NewListAvailabilityRepo(ListAvailabilityConfig{DB: db, Postgres: false, Now: fixed})
	if err != nil {
		t.Fatal(err)
	}
	controlRepo, err := NewControlPlaneRepo(ControlPlaneConfig{DB: db, Postgres: false, Now: fixed})
	if err != nil {
		t.Fatal(err)
	}
	return listRepo, controlRepo, db, spec
}

// w13g5CircuitSchema 控制面与列表投影测试所需的最小同形表集（对齐
// controlplane_test.go 与 listavailability_test.go 的 fixture）。
var w13g5CircuitSchema = []string{
	`CREATE TABLE accounts (
		id TEXT PRIMARY KEY,
		system_account_id TEXT NOT NULL,
		created_at TEXT NOT NULL DEFAULT '2026-01-01T00:00:00.000Z',
		deleted_at TEXT,
		authorization_instance_authorization_id TEXT,
		dispatch_revision INTEGER NOT NULL DEFAULT 1,
		circuit_projection_revision INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE resource_authorizations (id TEXT PRIMARY KEY, status TEXT NOT NULL)`,
	`CREATE TABLE system_accounts (id TEXT PRIMARY KEY)`,
	`CREATE TABLE account_name_search_terms (account_id TEXT NOT NULL, term TEXT NOT NULL)`,
	`CREATE TABLE account_name_search_documents (account_id TEXT PRIMARY KEY)`,
	`CREATE TABLE account_list_availability_projection_dependency_health (
		dependency_name TEXT PRIMARY KEY,
		state TEXT NOT NULL,
		generation INTEGER NOT NULL,
		reason TEXT,
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE account_list_availability_dirty (
		account_id TEXT PRIMARY KEY,
		viewer_system_account_id TEXT NOT NULL,
		generation INTEGER NOT NULL,
		applied_generation INTEGER NOT NULL,
		reason TEXT NOT NULL,
		available_at_ms INTEGER NOT NULL,
		claim_token TEXT,
		claimed_by TEXT,
		claim_until_ms INTEGER,
		attempt_count INTEGER NOT NULL DEFAULT 0,
		created_at_ms INTEGER NOT NULL,
		updated_at_ms INTEGER NOT NULL
	)`,
	`CREATE TABLE account_list_availability_projections (
		viewer_system_account_id TEXT NOT NULL,
		account_id TEXT NOT NULL,
		source_account_id TEXT,
		authorization_id TEXT,
		effective_status TEXT NOT NULL,
		schedulable_bucket TEXT NOT NULL,
		provider_code TEXT NOT NULL,
		provider_protocol_profile_id TEXT NOT NULL,
		account_type TEXT NOT NULL,
		bound_group_id TEXT,
		name_sort_key TEXT NOT NULL,
		priority_sort_key INTEGER NOT NULL,
		super_priority_sort_key INTEGER NOT NULL,
		fallback_sort_key INTEGER NOT NULL,
		concurrency_sort_key INTEGER NOT NULL,
		account_expires_at_sort_key TEXT,
		last_used_at_sort_key TEXT,
		created_at_sort_key TEXT NOT NULL,
		payload_json TEXT NOT NULL,
		source_generation INTEGER NOT NULL,
		next_transition_at TEXT,
		projected_at TEXT NOT NULL,
		PRIMARY KEY (viewer_system_account_id, account_id)
	)`,
	`CREATE TABLE account_list_availability_projection_index (
		viewer_system_account_id TEXT NOT NULL,
		account_id TEXT NOT NULL,
		effective_status TEXT NOT NULL,
		schedulable_bucket TEXT NOT NULL,
		provider_code TEXT NOT NULL,
		provider_protocol_profile_id TEXT NOT NULL,
		account_type TEXT NOT NULL,
		bound_group_id TEXT,
		name_sort_key TEXT NOT NULL,
		priority_sort_key INTEGER NOT NULL,
		super_priority_sort_key INTEGER NOT NULL,
		fallback_sort_key INTEGER NOT NULL,
		concurrency_sort_key INTEGER NOT NULL,
		account_expires_at_sort_key TEXT,
		last_used_at_sort_key TEXT,
		created_at_sort_key TEXT NOT NULL,
		access_type_sort_key TEXT NOT NULL DEFAULT 'owner',
		search_index_complete INTEGER NOT NULL DEFAULT 0,
		authorization_quota_exceeded INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (viewer_system_account_id, account_id)
	)`,
	`CREATE TABLE account_list_availability_projection_tags (
		viewer_system_account_id TEXT NOT NULL,
		account_id TEXT NOT NULL,
		tag_id TEXT NOT NULL,
		PRIMARY KEY (viewer_system_account_id, account_id, tag_id)
	)`,
	`CREATE TABLE account_list_availability_projection_search_terms (
		viewer_system_account_id TEXT NOT NULL,
		account_id TEXT NOT NULL,
		term TEXT NOT NULL,
		name_sort_key TEXT NOT NULL,
		created_at_sort_key TEXT NOT NULL,
		PRIMARY KEY (viewer_system_account_id, account_id, term)
	)`,
	`CREATE TABLE account_list_availability_runtime_overlays (
		account_id TEXT PRIMARY KEY,
		current_concurrency INTEGER NOT NULL,
		observed_at TEXT NOT NULL,
		next_reconcile_at TEXT
	)`,
	`CREATE TABLE account_list_availability_projection_viewer_health (
		viewer_system_account_id TEXT PRIMARY KEY,
		projection_count INTEGER NOT NULL,
		oldest_projected_at TEXT,
		next_transition_at TEXT,
		is_current INTEGER NOT NULL DEFAULT 0,
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE account_circuit_incidents (
		circuit_scope_key TEXT PRIMARY KEY,
		account_id TEXT NOT NULL,
		account_runtime_key TEXT NOT NULL,
		scope_kind TEXT NOT NULL,
		key_fingerprint TEXT,
		protocol_code TEXT,
		request_lane TEXT,
		model_family TEXT,
		incident_id TEXT NOT NULL,
		parent_incident_id TEXT,
		child_incident_ids_json TEXT NOT NULL DEFAULT '[]',
		state TEXT NOT NULL,
		generation INTEGER NOT NULL,
		dispatch_revision INTEGER NOT NULL,
		ledger_revision INTEGER NOT NULL,
		projected_ledger_revision INTEGER NOT NULL DEFAULT 0,
		transition_id TEXT NOT NULL,
		lease_id TEXT,
		lease_purpose TEXT,
		lease_until_ms INTEGER,
		backoff_level INTEGER NOT NULL DEFAULT 0,
		consecutive_failures INTEGER NOT NULL DEFAULT 0,
		confirmation_failures_required INTEGER NOT NULL DEFAULT 1,
		confirmation_failure_evidence_keys_json TEXT NOT NULL DEFAULT '[]',
		recovering_successes INTEGER NOT NULL DEFAULT 0,
		next_transition_at_ms INTEGER,
		open_until_ms INTEGER,
		last_failure_class TEXT,
		retained_until_ms INTEGER,
		created_at_ms INTEGER NOT NULL,
		updated_at_ms INTEGER NOT NULL
	)`,
	`CREATE TABLE account_circuit_outbox (
		event_id TEXT PRIMARY KEY,
		projection_key TEXT NOT NULL,
		dedupe_key TEXT NOT NULL,
		event_type TEXT NOT NULL,
		account_id TEXT NOT NULL,
		account_runtime_key TEXT NOT NULL,
		circuit_scope_key TEXT,
		incident_id TEXT,
		transition_id TEXT NOT NULL,
		dispatch_revision INTEGER NOT NULL,
		generation INTEGER,
		ledger_revision INTEGER,
		status TEXT NOT NULL DEFAULT 'pending',
		available_at_ms INTEGER NOT NULL,
		claim_token TEXT,
		claimed_by TEXT,
		claim_until_ms INTEGER,
		attempt_count INTEGER NOT NULL DEFAULT 0,
		last_error_class TEXT,
		acknowledged_at_ms INTEGER,
		created_at_ms INTEGER NOT NULL,
		updated_at_ms INTEGER NOT NULL
	)`,
}

// w13g5SeedAccount 写入一条可用账户（供 dirty/投影流程引用）。
func w13g5SeedAccount(t *testing.T, db *sql.DB, accountID, viewer string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id) VALUES (?, ?)`, accountID, viewer); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO system_accounts (id) VALUES (?)`, viewer); err != nil {
		t.Fatal(err)
	}
}

// w13g5SeedDirty 写入一条 pending dirty 行。
func w13g5SeedDirty(t *testing.T, db *sql.DB, accountID, viewer string, generation int64, claimToken string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO account_list_availability_dirty (
		account_id, viewer_system_account_id, generation, applied_generation, reason,
		available_at_ms, claim_token, attempt_count, created_at_ms, updated_at_ms
	) VALUES (?, ?, ?, 0, 'w13g5-test', 0, ?, 0, 1, 1)`, accountID, viewer, generation, claimToken); err != nil {
		t.Fatal(err)
	}
}

// w13g5SeedOutbox 写入一条 outbox 事件。
func w13g5SeedOutbox(t *testing.T, db *sql.DB, eventID, eventType, status, claimToken string, dispatchRevision, ledgerRevision int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO account_circuit_outbox (
		event_id, projection_key, dedupe_key, event_type, account_id, account_runtime_key,
		circuit_scope_key, incident_id, transition_id, dispatch_revision, ledger_revision,
		status, claim_token, available_at_ms, attempt_count, created_at_ms, updated_at_ms
	) VALUES (?, 'account_circuit_runtime_v1', 'w13g5-dedupe', ?, 'w13g5-acc', 'w13g5-acc',
		'w13g5-scope', 'w13g5-incident', 'w13g5-tr', ?, ?, ?, ?, 0, 0, 1, 1)`,
		eventID, eventType, dispatchRevision, ledgerRevision, status, claimToken); err != nil {
		t.Fatal(err)
	}
}
