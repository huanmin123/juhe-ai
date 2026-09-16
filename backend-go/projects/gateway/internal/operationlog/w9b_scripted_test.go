package operationlog

// w9b 脚本化 database/sql driver：按查询内容匹配注入失败/行数据，覆盖单靠
// SQLite 或真实 PG 难以触达的 PG 方言防御臂（SET LOCAL 失败、commit 失败、
// 行迭代错误等）。匹配策略为"未消耗步骤中第一个子串全命中的步骤"，避免
// validatePostgresSchema 的 map 随机迭代顺序破坏脚本。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
)

var w9bErrBoom = errors.New("w9b boom")

// ---------------------------------------------------------------------------
// 脚本化 driver
// ---------------------------------------------------------------------------

type w9bStep struct {
	matcher   []string
	cols      []string
	rows      [][]driver.Value
	noRows    bool
	rowsErr   error
	eofErr    error
	execErr   error
	affected  int64
	beginErr  error
	commitErr error
	repeat    int
	consumed  int
}

type w9bScript struct {
	steps    []w9bStep
	fallback func(kind, query string, args []driver.NamedValue) (cols []string, rows [][]driver.Value, err error)
	// execAffected 是未匹配步骤的 Exec 默认 RowsAffected（0=幂等语义）。
	execAffected int64
}

func (s *w9bScript) take(kind, query string) *w9bStep {
	for index := range s.steps {
		step := &s.steps[index]
		limit := step.repeat
		if limit < 1 {
			limit = 1
		}
		if step.consumed >= limit {
			continue
		}
		matched := true
		for _, fragment := range step.matcher {
			if !strings.Contains(query, fragment) {
				matched = false
				break
			}
		}
		if matched {
			step.consumed++
			return step
		}
	}
	return nil
}

func (s *w9bScript) takeForQuery(query string, args []driver.NamedValue) (cols []string, rows [][]driver.Value, eofErr error, err error) {
	if step := s.take("Query", query); step != nil {
		if step.rowsErr != nil {
			return nil, nil, nil, step.rowsErr
		}
		if step.noRows {
			if step.cols != nil {
				return step.cols, nil, nil, nil
			}
			return []string{"x"}, nil, nil, nil
		}
		cols := step.cols
		if cols == nil {
			cols = []string{"v"}
		}
		return cols, step.rows, step.eofErr, nil
	}
	if s.fallback != nil {
		cols, rows, err = s.fallback("Query", query, args)
		return cols, rows, nil, err
	}
	return nil, nil, nil, fmt.Errorf("w9b script exhausted at Query: %.120s", query)
}

type w9bConn struct{ script *w9bScript }

func (c *w9bConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("w9b: prepare unsupported")
}
func (c *w9bConn) Close() error { return nil }

// CheckNamedValue 接受任意参数类型（PG unnest 的 []string 数组参数）。
func (c *w9bConn) CheckNamedValue(*driver.NamedValue) error { return nil }

func (c *w9bConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) { return c.Begin() }
func (c *w9bConn) Begin() (driver.Tx, error) {
	if step := c.script.take("Begin", "BEGIN"); step != nil && step.beginErr != nil {
		return nil, step.beginErr
	}
	return &w9bTx{script: c.script}, nil
}

func (c *w9bConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	cols, values, eofErr, err := c.script.takeForQuery(query, args)
	if err != nil {
		return nil, err
	}
	return &w9bRows{cols: cols, values: values, eofErr: eofErr}, nil
}

func (c *w9bConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if step := c.script.take("Exec", query); step != nil {
		if step.execErr != nil {
			return nil, step.execErr
		}
		return w9bResult{affected: step.affected}, nil
	}
	return w9bResult{affected: c.script.execAffected}, nil
}

type w9bTx struct{ script *w9bScript }

func (t *w9bTx) Commit() error {
	if step := t.script.take("Commit", "COMMIT"); step != nil {
		return step.commitErr
	}
	return nil
}
func (t *w9bTx) Rollback() error { return nil }

type w9bResult struct{ affected int64 }

func (r w9bResult) LastInsertId() (int64, error) { return 0, nil }
func (r w9bResult) RowsAffected() (int64, error) { return r.affected, nil }

type w9bRows struct {
	cols   []string
	values [][]driver.Value
	eofErr error
	index  int
}

func (r *w9bRows) Columns() []string { return r.cols }
func (r *w9bRows) Close() error      { return nil }
func (r *w9bRows) Next(dest []driver.Value) error {
	if r.index < len(r.values) {
		copy(dest, r.values[r.index])
		r.index++
		return nil
	}
	if r.eofErr != nil {
		return r.eofErr
	}
	return io.EOF
}
func (r *w9bRows) Err() error { return nil }

var w9bDriverSeq int64

type w9bDriver struct{ script *w9bScript }

func (d w9bDriver) Open(string) (driver.Conn, error) { return &w9bConn{script: d.script}, nil }

func w9bOpen(t *testing.T, script *w9bScript) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("w9b-oplog-%d", atomic.AddInt64(&w9bDriverSeq, 1))
	sql.Register(name, w9bDriver{script: script})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func w9bPGStore(t *testing.T, script *w9bScript) *sqlStore {
	t.Helper()
	// 未显式提供 fallback 时默认挂伪 catalog：让 EnsureSchema 的 validate 查询可答。
	if script.fallback == nil {
		script.fallback = newW9BPGCatalog().respond
	}
	return &sqlStore{db: w9bOpen(t, script), mode: ModePostgres}
}

// ---------------------------------------------------------------------------
// 伪 PG catalog：动态应答 validatePostgresSchema / schema / lease 查询
// ---------------------------------------------------------------------------

type w9bPGCatalog struct {
	viewerPK    string
	fkPresent   bool
	counts      map[string]int64
	indexErr    error
	fkErr       error
	notNullErr  error
	columnErr   error
	pkReadErr   error
	columnTypes map[string]string
}

func newW9BPGCatalog() *w9bPGCatalog {
	return &w9bPGCatalog{viewerPK: postgresPrimaryKeys["operation_log_viewers"], fkPresent: true}
}

func (c *w9bPGCatalog) respond(_ string, query string, args []driver.NamedValue) (cols []string, rows [][]driver.Value, err error) {
	switch {
	case strings.Contains(query, "format_type"):
		if c.columnErr != nil {
			return nil, nil, c.columnErr
		}
		table, column := asString(args[0].Value), asString(args[1].Value)
		definition := c.columnTypes[table+"."+column]
		if definition == "" {
			for table2, columns := range postgresSchemaColumns {
				if table2 == strings.TrimPrefix(table, "juhe_dataset.") {
					for _, column2 := range columns {
						if column2.name == column {
							definition = column2.typeName
						}
					}
				}
			}
		}
		return []string{"format_type"}, [][]driver.Value{{definition}}, nil
	case strings.Contains(query, "string_agg"):
		if c.pkReadErr != nil {
			return nil, nil, c.pkReadErr
		}
		table := strings.TrimPrefix(asString(args[0].Value), "juhe_dataset.")
		pk := postgresPrimaryKeys[table]
		if table == "operation_log_viewers" {
			pk = c.viewerPK
		}
		return []string{"pk"}, [][]driver.Value{{pk}}, nil
	case strings.Contains(query, "attnotnull"):
		if c.notNullErr != nil {
			return nil, nil, c.notNullErr
		}
		return []string{"notnull"}, [][]driver.Value{{true}}, nil
	case strings.Contains(query, "pg_get_indexdef"):
		if c.indexErr != nil {
			return nil, nil, c.indexErr
		}
		return []string{"indexdef"}, [][]driver.Value{{postgresRequiredIndexDefinitions[asString(args[0].Value)]}}, nil
	case strings.Contains(query, "EXISTS"):
		if c.fkErr != nil {
			return nil, nil, c.fkErr
		}
		return []string{"exists"}, [][]driver.Value{{c.fkPresent}}, nil
	case strings.Contains(query, "COUNT(*)"):
		table := "operation_logs"
		for candidate := range c.counts {
			if strings.Contains(query, candidate) {
				table = candidate
			}
		}
		count := c.counts[table]
		return []string{"count"}, [][]driver.Value{{count}}, nil
	case strings.Contains(query, "lease_until>clock_timestamp()"):
		return []string{"one"}, [][]driver.Value{{int64(1)}}, nil
	case strings.Contains(query, "id=ANY($1::text[])"):
		return []string{"id", "name"}, [][]driver.Value{{"actor", "Actor"}}, nil
	}
	return nil, nil, fmt.Errorf("w9b fake catalog: unexpected query %.120s", query)
}

func asString(value driver.Value) string {
	if typed, ok := value.(string); ok {
		return typed
	}
	return fmt.Sprintf("%v", value)
}

// ---------------------------------------------------------------------------
// PG 方言错误臂
// ---------------------------------------------------------------------------

func TestW9BBeginTxPostgresTimeoutArms(t *testing.T) {
	ctx := context.Background()
	// SET LOCAL 失败必须回滚并包装错误（beginTx 与 beginLegacyMigrationTx 各自独立脚本）。
	beginFail := func(matcher, want string, migration bool) func(t *testing.T) {
		return func(t *testing.T) {
			script := &w9bScript{steps: []w9bStep{{matcher: []string{matcher}, execErr: w9bErrBoom}}}
			store := &sqlStore{db: w9bOpen(t, script), mode: ModePostgres}
			var err error
			if migration {
				_, err = store.beginLegacyMigrationTx(ctx)
			} else {
				_, err = store.beginTx(ctx)
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("%s=%v", want, err)
			}
		}
	}
	t.Run("beginTx statement timeout", beginFail("SET LOCAL statement_timeout", "configure F4 PostgreSQL transaction", false))
	t.Run("beginTx lock timeout", beginFail("SET LOCAL lock_timeout", "configure F4 PostgreSQL transaction", false))
	t.Run("beginLegacyMigrationTx statement timeout", beginFail("SET LOCAL statement_timeout", "legacy migration transaction", true))
	t.Run("beginLegacyMigrationTx idle timeout", beginFail("SET LOCAL idle_in_transaction_session_timeout", "legacy migration transaction", true))

	// 全部 SET LOCAL 成功路径。
	okScript := &w9bScript{}
	okStore := &sqlStore{db: w9bOpen(t, okScript), mode: ModePostgres}
	tx, err := okStore.beginTx(ctx)
	if err != nil {
		t.Fatalf("beginTx=%v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit=%v", err)
	}
	migTx, err := (&sqlStore{db: w9bOpen(t, &w9bScript{}), mode: ModePostgres}).beginLegacyMigrationTx(ctx)
	if err != nil {
		t.Fatalf("beginLegacyMigrationTx=%v", err)
	}
	if err := migTx.Rollback(); err != nil {
		t.Fatalf("rollback=%v", err)
	}
	// 非 PG 模式直通（不执行 SET LOCAL）。
	sqliteMode := &sqlStore{db: w9bOpen(t, &w9bScript{})}
	direct, err := sqliteMode.beginTx(ctx)
	if err != nil {
		t.Fatalf("sqlite beginTx=%v", err)
	}
	_ = direct.Rollback()
}

func TestW9BEnsureSchemaPostgresArms(t *testing.T) {
	ctx := context.Background()
	// advisory lock 失败。
	lockScript := &w9bScript{steps: []w9bStep{{matcher: []string{"pg_advisory_xact_lock"}, execErr: w9bErrBoom}}}
	if err := w9bPGStore(t, lockScript).EnsureSchema(ctx); err == nil {
		t.Fatal("advisory lock 失败必须透传")
	}
	// applyPostgresSchema 失败。
	schemaScript := &w9bScript{steps: []w9bStep{{matcher: []string{"CREATE SCHEMA"}, execErr: w9bErrBoom}}}
	if err := w9bPGStore(t, schemaScript).EnsureSchema(ctx); err == nil || !strings.Contains(err.Error(), "initialize F4 postgres schema") {
		t.Fatalf("applyPostgresSchema=%v", err)
	}
	// validatePostgresSchema 失败（列读取错误）。
	validateScript := &w9bScript{fallback: func(kind, query string, args []driver.NamedValue) ([]string, [][]driver.Value, error) {
		if strings.Contains(query, "format_type") {
			return nil, nil, w9bErrBoom
		}
		return nil, nil, fmt.Errorf("unexpected %.80s", query)
	}}
	if err := w9bPGStore(t, validateScript).EnsureSchema(ctx); err == nil || !strings.Contains(err.Error(), "schema incompatible") {
		t.Fatalf("validate=%v", err)
	}
	// commit 失败。
	commitScript := &w9bScript{steps: []w9bStep{{matcher: []string{"COMMIT"}, commitErr: w9bErrBoom}}, fallback: newW9BPGCatalog().respond}
	if err := w9bPGStore(t, commitScript).EnsureSchema(ctx); err == nil {
		t.Fatal("commit 失败必须透传")
	}
	// 成功路径（伪 catalog 全部应答）。
	okScript := &w9bScript{fallback: newW9BPGCatalog().respond}
	store := w9bPGStore(t, okScript)
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema=%v", err)
	}
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("schemaReady 短路=%v", err)
	}
}

func TestW9BOwnerLeasePostgresArms(t *testing.T) {
	ctx := context.Background()
	leaseQuery := "INSERT INTO juhe_dataset.operation_log_owner_leases"
	// ErrNoRows → ok=false 且提交事务。
	noRows := &w9bScript{steps: []w9bStep{{matcher: []string{leaseQuery}, noRows: true}}}
	lease, ok, err := w9bPGStore(t, noRows).AcquireOwnerLease(ctx, "w9b", time.Minute)
	if err != nil || ok || lease != (OwnerLease{}) {
		t.Fatalf("no rows：lease=%+v ok=%v err=%v", lease, ok, err)
	}
	// 查询错误透传。
	queryErr := &w9bScript{steps: []w9bStep{{matcher: []string{leaseQuery}, rowsErr: w9bErrBoom}}}
	if _, _, err := w9bPGStore(t, queryErr).AcquireOwnerLease(ctx, "w9b", time.Minute); err == nil {
		t.Fatal("查询错误必须透传")
	}
	// commit 失败。
	commitErr := &w9bScript{steps: []w9bStep{
		{matcher: []string{leaseQuery}, rows: [][]driver.Value{{int64(7)}}},
		{matcher: []string{"COMMIT"}, commitErr: w9bErrBoom},
	}}
	if _, _, err := w9bPGStore(t, commitErr).AcquireOwnerLease(ctx, "w9b", time.Minute); err == nil {
		t.Fatal("commit 失败必须透传")
	}
	// 成功。
	okScript := &w9bScript{steps: []w9bStep{{matcher: []string{leaseQuery}, rows: [][]driver.Value{{int64(3)}}}}}
	if lease, ok, err := w9bPGStore(t, okScript).AcquireOwnerLease(ctx, "w9b", time.Minute); err != nil || !ok || lease.FenceToken != 3 {
		t.Fatalf("成功获取：lease=%+v ok=%v err=%v", lease, ok, err)
	}

	// RenewOwnerLease PG：begin 失败 / exec 失败 / n!=1 / 成功。
	renewBegin := &w9bScript{steps: []w9bStep{{matcher: []string{"BEGIN"}, beginErr: w9bErrBoom}}}
	if _, err := w9bPGStore(t, renewBegin).RenewOwnerLease(ctx, OwnerLease{OwnerID: "o", FenceToken: 1}, time.Minute); err == nil {
		t.Fatal("renew begin 失败必须透传")
	}
	renewExec := &w9bScript{steps: []w9bStep{{matcher: []string{"UPDATE juhe_dataset.operation_log_owner_leases SET lease_until=clock_timestamp()"}, execErr: w9bErrBoom}}}
	if _, err := w9bPGStore(t, renewExec).RenewOwnerLease(ctx, OwnerLease{OwnerID: "o", FenceToken: 1}, time.Minute); err == nil {
		t.Fatal("renew exec 失败必须透传")
	}
	renewMiss := &w9bScript{steps: []w9bStep{{matcher: []string{"UPDATE juhe_dataset.operation_log_owner_leases SET lease_until=clock_timestamp()"}, affected: 0}}}
	if renewed, err := w9bPGStore(t, renewMiss).RenewOwnerLease(ctx, OwnerLease{OwnerID: "o", FenceToken: 1}, time.Minute); err != nil || renewed {
		t.Fatalf("renew miss：renewed=%v err=%v", renewed, err)
	}
	renewHit := &w9bScript{steps: []w9bStep{{matcher: []string{"UPDATE juhe_dataset.operation_log_owner_leases SET lease_until=clock_timestamp()"}, affected: 1}}}
	if renewed, err := w9bPGStore(t, renewHit).RenewOwnerLease(ctx, OwnerLease{OwnerID: "o", FenceToken: 1}, time.Minute); err != nil || !renewed {
		t.Fatalf("renew hit：renewed=%v err=%v", renewed, err)
	}
	renewCommit := &w9bScript{steps: []w9bStep{
		{matcher: []string{"UPDATE juhe_dataset.operation_log_owner_leases SET lease_until=clock_timestamp()"}, affected: 1},
		{matcher: []string{"COMMIT"}, commitErr: w9bErrBoom},
	}}
	if _, err := w9bPGStore(t, renewCommit).RenewOwnerLease(ctx, OwnerLease{OwnerID: "o", FenceToken: 1}, time.Minute); err == nil {
		t.Fatal("renew commit 失败必须透传")
	}

	// ReleaseOwnerLease PG：exec 失败 / n!=1 → ErrOwnerLeaseLost / 成功。
	releaseExec := &w9bScript{steps: []w9bStep{{matcher: []string{"UPDATE juhe_dataset.operation_log_owner_leases SET lease_until=to_timestamp(0)"}, execErr: w9bErrBoom}}}
	if err := w9bPGStore(t, releaseExec).ReleaseOwnerLease(ctx, OwnerLease{OwnerID: "o", FenceToken: 1}); err == nil {
		t.Fatal("release exec 失败必须透传")
	}
	releaseMiss := &w9bScript{steps: []w9bStep{{matcher: []string{"UPDATE juhe_dataset.operation_log_owner_leases SET lease_until=to_timestamp(0)"}, affected: 0}}}
	if err := w9bPGStore(t, releaseMiss).ReleaseOwnerLease(ctx, OwnerLease{OwnerID: "o", FenceToken: 1}); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("release miss=%v", err)
	}
	releaseHit := &w9bScript{steps: []w9bStep{{matcher: []string{"UPDATE juhe_dataset.operation_log_owner_leases SET lease_until=to_timestamp(0)"}, affected: 1}}}
	if err := w9bPGStore(t, releaseHit).ReleaseOwnerLease(ctx, OwnerLease{OwnerID: "o", FenceToken: 1}); err != nil {
		t.Fatalf("release hit=%v", err)
	}
}

func TestW9BPersistPostgresArms(t *testing.T) {
	ctx := context.Background()
	base := Input{ID: "w9b-pg", ActorSystemAccountID: "actor", ActorRole: "admin", Mode: "admin", Module: "m", Action: "a", OperationKey: "m.a", ResourceType: "r", Summary: "hello w9b", DetailLevel: "full", VisibilityScope: "targeted", CreatedAt: "2026-09-01T00:00:00.000000000Z", Targets: []Target{{TargetType: "account", Relation: "primary"}}, Viewers: []Viewer{{SystemAccountID: "v", VisibilityReason: "resource_owner", DetailLevel: "full"}}}
	lease := OwnerLease{OwnerID: "w9b", FenceToken: 1}
	// PG 写场景公共脚本：租约校验 ×2、主行 INSERT 命中 1 行，可追加 children 步骤。
	writableScript := func(extra ...w9bStep) *w9bScript {
		steps := []w9bStep{
			{matcher: []string{"SELECT 1 FROM juhe_dataset.operation_log_owner_leases"}, rows: [][]driver.Value{{int64(1)}}, repeat: 2},
			{matcher: []string{"INSERT INTO juhe_dataset.operation_logs ("}, affected: 1},
		}
		return &w9bScript{steps: append(steps, extra...)}
	}
	// persistPostgresChildren targets 失败。
	targetsFail := writableScript(w9bStep{matcher: []string{"INSERT INTO juhe_dataset.operation_log_targets"}, execErr: w9bErrBoom})
	if _, err := w9bPGStore(t, targetsFail).Persist(ctx, lease, base); err == nil {
		t.Fatal("targets 失败必须透传")
	}
	// viewers 失败。
	viewersFail := writableScript(w9bStep{matcher: []string{"INSERT INTO juhe_dataset.operation_log_viewers"}, execErr: w9bErrBoom})
	if _, err := w9bPGStore(t, viewersFail).Persist(ctx, lease, base); err == nil {
		t.Fatal("viewers 失败必须透传")
	}
	// search terms 失败。
	termsFail := writableScript(w9bStep{matcher: []string{"INSERT INTO juhe_dataset.operation_log_summary_search_terms"}, execErr: w9bErrBoom})
	if _, err := w9bPGStore(t, termsFail).Persist(ctx, lease, base); err == nil {
		t.Fatal("terms 失败必须透传")
	}
	// 幂等命中（RowsAffected=0）→ 不写 children 直接提交。
	idempotent := &w9bScript{steps: []w9bStep{
		{matcher: []string{"SELECT 1 FROM juhe_dataset.operation_log_owner_leases"}, rows: [][]driver.Value{{int64(1)}}},
		{matcher: []string{"INSERT INTO juhe_dataset.operation_logs ("}, affected: 0},
	}}
	ignored, err := w9bPGStore(t, idempotent).Persist(ctx, lease, base)
	if err != nil || !ignored {
		t.Fatalf("幂等：ignored=%v err=%v", ignored, err)
	}
	// 租约在写入后失效（第二个 verifyLease ErrNoRows）：两次租约校验分别命中
	// 行步骤与 noRows 步骤（顺序扫描按消耗次序推进）。
	lostAfterWrite := &w9bScript{steps: []w9bStep{
		{matcher: []string{"SELECT 1 FROM juhe_dataset.operation_log_owner_leases"}, rows: [][]driver.Value{{int64(1)}}},
		{matcher: []string{"INSERT INTO juhe_dataset.operation_logs ("}, affected: 1},
		{matcher: []string{"INSERT INTO juhe_dataset.operation_log_targets ("}, affected: 1},
		{matcher: []string{"INSERT INTO juhe_dataset.operation_log_viewers ("}, affected: 1},
		{matcher: []string{"INSERT INTO juhe_dataset.operation_log_summary_search_terms ("}, affected: 1},
		{matcher: []string{"SELECT 1 FROM juhe_dataset.operation_log_owner_leases"}, noRows: true},
	}}
	if _, err := w9bPGStore(t, lostAfterWrite).Persist(ctx, lease, base); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("写后丢租约=%v", err)
	}
	// 成功路径。
	success := writableScript(
		w9bStep{matcher: []string{"INSERT INTO juhe_dataset.operation_log_targets ("}, affected: 1},
		w9bStep{matcher: []string{"INSERT INTO juhe_dataset.operation_log_viewers ("}, affected: 1},
		w9bStep{matcher: []string{"INSERT INTO juhe_dataset.operation_log_summary_search_terms ("}, affected: 1},
	)
	if ignored, err := w9bPGStore(t, success).Persist(ctx, lease, base); err != nil || ignored {
		t.Fatalf("成功路径：ignored=%v err=%v", ignored, err)
	}
	// verifyLease 非缺失错误包装。
	verifyErr := &w9bScript{steps: []w9bStep{{matcher: []string{"SELECT 1 FROM juhe_dataset.operation_log_owner_leases"}, rowsErr: w9bErrBoom}}}
	if _, err := w9bPGStore(t, verifyErr).Persist(ctx, lease, base); err == nil || !strings.Contains(err.Error(), "verify F4 operation-log owner lease") {
		t.Fatalf("verify 错误=%v", err)
	}
}

func TestW9BCleanupRetentionPostgresArms(t *testing.T) {
	ctx := context.Background()
	lease := OwnerLease{OwnerID: "w9b", FenceToken: 1}
	// 空结果 → 0 删除。
	empty := &w9bScript{steps: []w9bStep{
		{matcher: []string{"SELECT 1 FROM juhe_dataset.operation_log_owner_leases"}, rows: [][]driver.Value{{int64(1)}}, repeat: 2},
		{matcher: []string{"SELECT id FROM juhe_dataset.operation_logs"}, noRows: true},
	}}
	deleted, err := w9bPGStore(t, empty).CleanupRetention(ctx, lease, time.Now(), 10)
	if err != nil || deleted != 0 {
		t.Fatalf("空清理：deleted=%d err=%v", deleted, err)
	}
	// 删除失败。
	deleteFail := &w9bScript{steps: []w9bStep{
		{matcher: []string{"SELECT 1 FROM juhe_dataset.operation_log_owner_leases"}, rows: [][]driver.Value{{int64(1)}}},
		{matcher: []string{"SELECT id FROM juhe_dataset.operation_logs"}, rows: [][]driver.Value{{"id-1"}}},
		{matcher: []string{"DELETE FROM juhe_dataset.operation_logs WHERE id=ANY"}, execErr: w9bErrBoom},
	}}
	if _, err := w9bPGStore(t, deleteFail).CleanupRetention(ctx, lease, time.Now(), 10); err == nil {
		t.Fatal("删除失败必须透传")
	}
	// id 扫描失败。
	scanFail := &w9bScript{steps: []w9bStep{
		{matcher: []string{"SELECT 1 FROM juhe_dataset.operation_log_owner_leases"}, rows: [][]driver.Value{{int64(1)}}},
		{matcher: []string{"SELECT id FROM juhe_dataset.operation_logs"}, cols: []string{"id"}, rows: [][]driver.Value{{nil}}},
	}}
	if _, err := w9bPGStore(t, scanFail).CleanupRetention(ctx, lease, time.Now(), 10); err == nil {
		t.Fatal("扫描失败必须透传")
	}
	// 成功删除。
	success := &w9bScript{steps: []w9bStep{
		{matcher: []string{"SELECT 1 FROM juhe_dataset.operation_log_owner_leases"}, rows: [][]driver.Value{{int64(1)}}, repeat: 2},
		{matcher: []string{"SELECT id FROM juhe_dataset.operation_logs"}, rows: [][]driver.Value{{"id-1"}, {"id-2"}}},
		{matcher: []string{"DELETE FROM juhe_dataset.operation_logs WHERE id=ANY"}, affected: 2},
	}}
	deleted, err = w9bPGStore(t, success).CleanupRetention(ctx, lease, time.Now(), 10)
	if err != nil || deleted != 2 {
		t.Fatalf("成功清理：deleted=%d err=%v", deleted, err)
	}
}

func TestW9BRetentionDaysPostgresArms(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		value   any
		noRows  bool
		rowsErr bool
		want    int
		wantErr bool
	}{
		{name: "valid", value: "30", want: 30},
		{name: "fallback", noRows: true, want: 365},
		{name: "query error", rowsErr: true, wantErr: true},
		{name: "bad json", value: "{invalid", wantErr: true},
		{name: "not number", value: `"12"`, wantErr: true},
		{name: "fraction", value: "12.5", wantErr: true},
		{name: "below range", value: "0", wantErr: true},
		{name: "above range", value: "3651", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			script := &w9bScript{}
			if tc.noRows {
				script.steps = append(script.steps, w9bStep{matcher: []string{"juhe_business.system_settings"}, noRows: true})
			} else if tc.rowsErr {
				script.steps = append(script.steps, w9bStep{matcher: []string{"juhe_business.system_settings"}, rowsErr: w9bErrBoom})
			} else {
				script.steps = append(script.steps, w9bStep{matcher: []string{"juhe_business.system_settings"}, rows: [][]driver.Value{{tc.value}}})
			}
			days, err := w9bPGStore(t, script).RetentionDays(ctx, 365)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("%s 必须报错，days=%d", tc.name, days)
				}
				return
			}
			if err != nil || days != tc.want {
				t.Fatalf("%s：days=%d err=%v", tc.name, days, err)
			}
		})
	}
}

func TestW9BOpenStorePostgresPoolInjection(t *testing.T) {
	// pool 注入路径：不真实连接，OpenStore 直接返回。
	handle, err := pgpool.NewRegistry().AcquireWith(func() (*sql.DB, error) {
		db := w9bOpen(t, &w9bScript{})
		return db, nil
	}, "w9b-pool", "operation-log", 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(Config{Mode: ModePostgres, PostgresPool: handle})
	if err != nil {
		t.Fatalf("pool 注入=%v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close=%v", err)
	}
	// 坏 URL：pool 打开失败。
	if _, err := OpenStore(Config{Mode: ModePostgres, PostgresURL: "postgres://bad url with spaces"}); err == nil || !strings.Contains(err.Error(), "open F4 PostgreSQL pool") {
		t.Fatalf("坏 URL=%v", err)
	}
}

func TestW9BTextPrefixUpperBoundAllBytes(t *testing.T) {
	if got := textPrefixUpperBound("\xff\xff"); got != "\xff\xff\x00" {
		t.Fatalf("全 0xff 边界=%q", got)
	}
	if got := textPrefixUpperBound("a\xffb"); got == "" || got[len(got)-1] != 'c' {
		t.Fatalf("中段 0xff=%q", got)
	}
}
