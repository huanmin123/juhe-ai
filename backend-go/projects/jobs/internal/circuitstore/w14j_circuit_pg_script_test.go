// 波次 w14j：控制面 PG 方言分支的脚本化覆盖。以 modernc SQLite 为内层
// 驱动，ATTACH 内存 schema 模拟 juhe_business 前缀，并在驱动层清洗
// PostgreSQL 专有的 FOR UPDATE [SKIP LOCKED] 行锁语法，使 PG 分支的 Go
// 代码路径（候选扫描、两段更新、扫描映射、错误传播）在进程内可回放。
// 含 clock_timestamp 等 PG 时钟函数的语句超出该替换能力，登记为脚本化
// 不可达（w14j_circuit_pg_script_test.go 头部清单）。
package circuitstore

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

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	"modernc.org/sqlite"
)

const w14jPGDriverName = "w14j-circuit-pg-script-sqlite"

var (
	w14jPGDriverRegister sync.Once
	w14jPGDriversMu      sync.Mutex
	w14jPGDrivers        = map[string]*w14jPGSpec{}
)

// w14jPGSpec 与前波注入语义一致：match 命中查询子串后注入错误。
type w14jPGSpec struct {
	mu    sync.Mutex
	match string
}

func (spec *w14jPGSpec) arm(match string) {
	spec.mu.Lock()
	defer spec.mu.Unlock()
	spec.match = match
}

func (spec *w14jPGSpec) disarm() {
	spec.mu.Lock()
	defer spec.mu.Unlock()
	spec.match = ""
}

func (spec *w14jPGSpec) hit(key string) bool {
	spec.mu.Lock()
	defer spec.mu.Unlock()
	return spec.match != "" && strings.Contains(key, spec.match)
}

func w14jPGInjectedErr() error { return errors.New("w14j 注入失败") }

// w14jRewritePGSQL 清洗 SQLite 无法执行的 PG 专有行锁语法。
func w14jRewritePGSQL(query string) string {
	query = strings.ReplaceAll(query, " FOR UPDATE SKIP LOCKED", "")
	query = strings.ReplaceAll(query, " FOR UPDATE", "")
	return query
}

type w14jPGDriver struct{ inner driver.Driver }

func (d *w14jPGDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	w14jPGDriversMu.Lock()
	spec := w14jPGDrivers[w14jPGDriverName]
	w14jPGDriversMu.Unlock()
	adapted := &w14jPGConn{inner: conn, spec: spec}
	// 每个新连接挂载 juhe_business 内存 schema 并重建最小表集（幂等）。
	if _, err := adapted.ExecContext(context.Background(), "ATTACH ':memory:' AS juhe_business", nil); err != nil {
		return nil, err
	}
	for _, statement := range w13g5CircuitSchema {
		prefixed := strings.Replace(statement, "CREATE TABLE ", "CREATE TABLE IF NOT EXISTS juhe_business.", 1)
		if _, err := adapted.ExecContext(context.Background(), prefixed, nil); err != nil {
			return nil, err
		}
	}
	return adapted, nil
}

type w14jPGConn struct {
	inner driver.Conn
	spec  *w14jPGSpec
}

func (c *w14jPGConn) Prepare(query string) (driver.Stmt, error) {
	query = w14jRewritePGSQL(query)
	if c.spec.hit(query) {
		return nil, w14jPGInjectedErr()
	}
	return c.inner.Prepare(query)
}

func (c *w14jPGConn) Close() error { return c.inner.Close() }

func (c *w14jPGConn) Begin() (driver.Tx, error) { return c.inner.Begin() }

func (c *w14jPGConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	query = w14jRewritePGSQL(query)
	if c.spec.hit(query) {
		return nil, w14jPGInjectedErr()
	}
	if execer, ok := c.inner.(driver.ExecerContext); ok {
		return execer.ExecContext(ctx, query, args)
	}
	stmt, err := c.inner.Prepare(query)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	return stmt.Exec(w14jDriverValues(args))
}

func (c *w14jPGConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	query = w14jRewritePGSQL(query)
	if c.spec.hit(query) {
		return nil, w14jPGInjectedErr()
	}
	if queryer, ok := c.inner.(driver.QueryerContext); ok {
		return queryer.QueryContext(ctx, query, args)
	}
	stmt, err := c.inner.Prepare(query)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	return stmt.Query(w14jDriverValues(args))
}

func w14jDriverValues(args []driver.NamedValue) []driver.Value {
	values := make([]driver.Value, len(args))
	for i, arg := range args {
		values[i] = arg.Value
	}
	return values
}

// w14jPGFixture 打开 PG 方言脚本化 fixture：两个仓储均以 Postgres: true
// 构造，表位于 juhe_business 内存 schema。
func w14jPGFixture(t *testing.T) (*ListAvailabilityRepo, *ControlPlaneRepo, *sql.DB, *w14jPGSpec) {
	t.Helper()
	w14jPGDriverRegister.Do(func() {
		sql.Register(w14jPGDriverName, &w14jPGDriver{inner: &sqlite.Driver{}})
	})
	spec := &w14jPGSpec{}
	w14jPGDriversMu.Lock()
	w14jPGDrivers[w14jPGDriverName] = spec
	w14jPGDriversMu.Unlock()
	db, err := sql.Open(w14jPGDriverName, "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "w14j-pg-script.sqlite3"))+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("打开 PG 脚本 fixture 失败: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })
	fixed := func() time.Time { return time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC) }
	listRepo, err := NewListAvailabilityRepo(ListAvailabilityConfig{DB: db, Postgres: true, Now: fixed})
	if err != nil {
		t.Fatal(err)
	}
	controlRepo, err := NewControlPlaneRepo(ControlPlaneConfig{DB: db, Postgres: true, Now: fixed})
	if err != nil {
		t.Fatal(err)
	}
	if err := controlRepo.EnsureCursorSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return listRepo, controlRepo, db, spec
}

func w14jSeedPGOutbox(t *testing.T, db *sql.DB, eventID, status string, claimUntilMS int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO juhe_business.account_circuit_outbox (
		event_id, projection_key, dedupe_key, event_type, account_id, account_runtime_key,
		transition_id, dispatch_revision, status, claim_token, available_at_ms, claim_until_ms,
		attempt_count, created_at_ms, updated_at_ms
	) VALUES (?, 'w14j-pk', ?, 'dispatch_revision_changed', 'w14j-acc', 'w14j-rk', 'tr', 3, ?, ?, 0, ?, 0, 1, 1)`,
		eventID, "dedupe:"+eventID, status, "token-"+eventID, claimUntilMS); err != nil {
		t.Fatal(err)
	}
}

// TestW14JClaimPGDialectPath 覆盖 Claim 的 PG 分支主体：候选扫描、processing
// 过期重认领、两段更新与事件映射。
func TestW14JClaimPGDialectPath(t *testing.T) {
	_, control, db, spec := w14jPGFixture(t)
	ctx := context.Background()
	w14jSeedPGOutbox(t, db, "w14j-pg-evt-1", "pending", 0)
	events, err := control.Claim(ctx, "w14j-owner", 1000, 30_000, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("PG 分支必须认领 1 条: %d", len(events))
	}
	if events[0].ClaimToken == "" || events[0].CircuitScopeKey != "" {
		t.Fatalf("认领事件形状错误: %+v", events[0])
	}
	var status string
	var attemptCount int64
	if err := db.QueryRow(`SELECT status, attempt_count FROM juhe_business.account_circuit_outbox WHERE event_id='w14j-pg-evt-1'`).Scan(&status, &attemptCount); err != nil {
		t.Fatal(err)
	}
	if status != "processing" || attemptCount != 1 {
		t.Fatalf("认领后必须 processing/attempt=1: %s %d", status, attemptCount)
	}
	// lease 未过期 → 无候选。
	events, err = control.Claim(ctx, "w14j-owner", 2000, 30_000, 10)
	if err != nil || len(events) != 0 {
		t.Fatalf("lease 内不得重复认领: %v %v", events, err)
	}
	// processing 且 claim_until 过期 → 再次认领（OR 第二分支）。
	if _, err := db.Exec(`UPDATE juhe_business.account_circuit_outbox SET claim_until_ms=1 WHERE event_id='w14j-pg-evt-1'`); err != nil {
		t.Fatal(err)
	}
	events, err = control.Claim(ctx, "w14j-owner", 5000, 30_000, 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("过期 lease 必须可重认领: %v %v", events, err)
	}
	// 候选查询注入。
	spec.arm("ORDER BY available_at_ms ASC, created_at_ms ASC, event_id ASC")
	if _, err := control.Claim(ctx, "w14j-owner", 9000, 30_000, 10); err == nil {
		t.Fatal("候选查询注入必须传播")
	}
	spec.disarm()
}

// TestW14JAckReleasePGDialectPath 覆盖 Ack 的 FOR UPDATE 读取分支与
// ReleaseForReplay 的 PG 前缀路径。
func TestW14JAckReleasePGDialectPath(t *testing.T) {
	_, control, db, spec := w14jPGFixture(t)
	ctx := context.Background()
	w14jSeedPGOutbox(t, db, "w14j-pg-evt-2", "processing", 9_000_000)
	if _, err := db.Exec(`INSERT INTO juhe_business.accounts (id, system_account_id, dispatch_revision) VALUES ('w14j-acc', 'w14j-viewer', 5)`); err != nil {
		t.Fatal(err)
	}
	event := opsjobs.OutboxEvent{
		EventID:           "w14j-pg-evt-2",
		EventType:         "dispatch_revision_changed",
		AccountRuntimeKey: "w14j-rk",
		TransitionID:      "tr",
		DispatchRevision:  3,
		ProjectionKey:     "w14j-pk",
		ClaimToken:        "token-w14j-pg-evt-2",
	}
	committed, err := control.Ack(ctx, event, 2000)
	if err != nil || !committed {
		t.Fatalf("PG Ack 必须成功: %v %v", committed, err)
	}
	var projected int64
	if err := db.QueryRow(`SELECT circuit_projection_revision FROM juhe_business.accounts WHERE id='w14j-acc'`).Scan(&projected); err != nil {
		t.Fatal(err)
	}
	if projected != 3 {
		t.Fatalf("PG 回写期待 3: %d", projected)
	}
	// Ack 候选读注入（rewrite 后按剩余文本匹配）。
	w14jSeedPGOutbox(t, db, "w14j-pg-evt-3", "processing", 9_000_000)
	spec.arm("WHERE event_id = ?")
	releaseEvent := event
	releaseEvent.EventID = "w14j-pg-evt-3"
	releaseEvent.ClaimToken = "token-w14j-pg-evt-3"
	if _, err := control.Ack(ctx, releaseEvent, 3000); err == nil {
		t.Fatal("Ack FOR UPDATE 读注入必须传播")
	}
	spec.disarm()
	// ReleaseForReplay PG 前缀路径。
	if err := control.ReleaseForReplay(ctx, releaseEvent, "w14j-boom", 4000, 1000); err != nil {
		t.Fatalf("PG Release 必须成功: %v", err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM juhe_business.account_circuit_outbox WHERE event_id='w14j-pg-evt-3'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("释放后必须回 pending: %s", status)
	}
}

// TestW14JIncidentReadsPGDialectPath 覆盖 incident 只读面的 PG 前缀路径
//（ListForRebuild / ListByRuntimeKeys / GetByScopeKey / cursor 往返）。
func TestW14JIncidentReadsPGDialectPath(t *testing.T) {
	list, control, db, spec := w14jPGFixture(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO juhe_business.accounts (id, system_account_id, dispatch_revision) VALUES ('w14j-acc', 'w14j-viewer', 5)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.account_circuit_incidents (
		circuit_scope_key, account_id, account_runtime_key, scope_kind,
		incident_id, child_incident_ids_json, state, generation, dispatch_revision,
		ledger_revision, transition_id, created_at_ms, updated_at_ms
	) VALUES ('w14j-pg-scope', 'w14j-acc', 'w14j-rk', 'account', 'w14j-pg-inc', '[]', 'OPEN', 1, 5, 2, 'tr', 10, 20)`); err != nil {
		t.Fatal(err)
	}
	page, err := control.ListForRebuild(ctx, opsjobs.RebuildPageQuery{NowMS: 1000, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].IncidentID != "w14j-pg-inc" {
		t.Fatalf("PG rebuild 必须返回 1 条: %+v", page)
	}
	records, err := control.ListByRuntimeKeys(ctx, []string{"w14j-rk"}, false, 1000)
	if err != nil || len(records) != 1 {
		t.Fatalf("PG runtime 键查询必须返回 1 条: %v %v", records, err)
	}
	record, err := control.GetByScopeKey(ctx, "w14j-pg-scope")
	if err != nil || record == nil {
		t.Fatalf("PG scope 查询必须命中: %v %v", record, err)
	}
	// cursor PG 前缀往返。
	cursorStore, err := NewReconcileCursorStore(ControlPlaneConfig{DB: control.db, Postgres: true, Now: func() time.Time { return time.UnixMilli(7000) }})
	if err != nil {
		t.Fatal(err)
	}
	if err := cursorStore.Save(ctx, opsjobs.IncidentCursor{UpdatedAtMS: 20, CircuitScopeKey: "w14j-pg-scope"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := cursorStore.Load(ctx)
	if err != nil || loaded == nil || loaded.UpdatedAtMS != 20 {
		t.Fatalf("PG cursor 往返失败: %+v %v", loaded, err)
	}
	// 列表投影 PG 前缀读取（空结果路径）。
	if _, err := list.ListScopes(ctx, []string{"w14j-acc"}); err != nil {
		t.Fatalf("PG ListScopes 失败: %v", err)
	}
	// 读取注入传播。
	spec.arm("FROM juhe_business.account_circuit_incidents")
	if _, err := control.ListForRebuild(ctx, opsjobs.RebuildPageQuery{NowMS: 1000, Limit: 10}); err == nil {
		t.Fatal("PG rebuild 注入必须传播")
	}
	spec.disarm()
}
