package circuitcontrolplane

// w14g：控制面 SQL 错误分支覆盖率补强。
//
// 通过代理 modernc.org/sqlite 驱动注入 query/exec/rows/commit/affected 故障
// （Mock 优先），覆盖 rows.Err、rows.Close、tx.Commit 与中途 SQL 错误分支。
//
// 不可达语句登记（分析依据见各条；均不需要在本轮构造可达路径）：
//   - store.go checkKeyModelCapabilityIndex 的共享契约缺陷分支（259-261 契约
//     缺失、265-266 多余定义 continue、268-270 重复定义、274-276 未命中）：
//     sharedcontracts.BusinessSQLiteSchema 编译期固定且恰好包含该索引一次。
//   - store.go predicatesEquivalent 的成对去括号循环（原 402-404）已随本波次
//     删除：norm 移除全部括号后条件恒为假。
//   - store.go randomToken 的 rand.Read 错误分支（1562-1564）及其调用方的
//     token 错误包装（466-468/655-657/725-727）：crypto/rand 在受支持平台
//     不会失败。
//   - json.Marshal([]string) 分支（1073-1075/1077-1079）：字符串切片序列化
//     不会失败。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	sharedcontracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
	"modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// SQL 故障注入代理驱动
// ---------------------------------------------------------------------------

var w14gErrInjected = errors.New("w14g injected sql fault")

type w14gFaults struct {
	mu           sync.Mutex
	onQuery      func(query string) error
	onExec       func(query string) error
	onRowsNext   func(query string, dest []driver.Value)
	onRowsFail   func(query string) error
	onRowsErr    func(query string) error
	onRowsClose  func(query string) error
	onCommit     func() error
	onAffected   func(query string) (int64, bool)
}

func (f *w14gFaults) set(mod func(*w14gFaults)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	mod(f)
}

func (f *w14gFaults) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onQuery, f.onExec, f.onRowsNext, f.onRowsFail, f.onRowsErr, f.onRowsClose, f.onCommit, f.onAffected = nil, nil, nil, nil, nil, nil, nil, nil
}

func (f *w14gFaults) queryErr(query string) error {
	f.mu.Lock()
	fn := f.onQuery
	f.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(query)
}

func (f *w14gFaults) execErr(query string) error {
	f.mu.Lock()
	fn := f.onExec
	f.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(query)
}

func (f *w14gFaults) rowsNext(query string, dest []driver.Value) {
	f.mu.Lock()
	fn := f.onRowsNext
	f.mu.Unlock()
	if fn != nil {
		fn(query, dest)
	}
}

func (f *w14gFaults) rowsFail(query string) error {
	f.mu.Lock()
	fn := f.onRowsFail
	f.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(query)
}

func (f *w14gFaults) rowsErr(query string) error {
	f.mu.Lock()
	fn := f.onRowsErr
	f.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(query)
}

func (f *w14gFaults) rowsClose(query string) error {
	f.mu.Lock()
	fn := f.onRowsClose
	f.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(query)
}

func (f *w14gFaults) commitErr() error {
	f.mu.Lock()
	fn := f.onCommit
	f.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn()
}

type w14gConnector struct {
	inner  driver.Connector
	faults *w14gFaults
}

func (c w14gConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &w14gConn{inner: conn, faults: c.faults}, nil
}

func (c w14gConnector) Driver() driver.Driver { return c.inner.Driver() }

type w14gConn struct {
	inner  driver.Conn
	faults *w14gFaults
}

func (c *w14gConn) Prepare(query string) (driver.Stmt, error) {
	stmt, err := c.inner.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &w14gStmt{inner: stmt, faults: c.faults, query: query}, nil
}

func (c *w14gConn) Close() error { return c.inner.Close() }

func (c *w14gConn) Begin() (driver.Tx, error) {
	tx, err := c.inner.Begin()
	if err != nil {
		return nil, err
	}
	return &w14gTx{inner: tx, faults: c.faults}, nil
}

func (c *w14gConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := c.faults.queryErr(query); err != nil {
		return nil, err
	}
	q, ok := c.inner.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	rows, err := q.QueryContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	return &w14gRows{inner: rows, faults: c.faults, query: query}, nil
}

func (c *w14gConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := c.faults.execErr(query); err != nil {
		return nil, err
	}
	e, ok := c.inner.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	res, err := e.ExecContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	return w14gResult{inner: res, faults: c.faults, query: query}, nil
}

type w14gStmt struct {
	inner  driver.Stmt
	faults *w14gFaults
	query  string
}

func (s *w14gStmt) Close() error  { return s.inner.Close() }
func (s *w14gStmt) NumInput() int { return s.inner.NumInput() }

func (s *w14gStmt) Exec(args []driver.Value) (driver.Result, error) {
	if err := s.faults.execErr(s.query); err != nil {
		return nil, err
	}
	res, err := s.inner.Exec(args)
	if err != nil {
		return nil, err
	}
	return w14gResult{inner: res, faults: s.faults, query: s.query}, nil
}

func (s *w14gStmt) Query(args []driver.Value) (driver.Rows, error) {
	if err := s.faults.queryErr(s.query); err != nil {
		return nil, err
	}
	rows, err := s.inner.Query(args)
	if err != nil {
		return nil, err
	}
	return &w14gRows{inner: rows, faults: s.faults, query: s.query}, nil
}

type w14gRows struct {
	inner  driver.Rows
	faults *w14gFaults
	query  string
}

func (r *w14gRows) Columns() []string { return r.inner.Columns() }

func (r *w14gRows) Close() error {
	if err := r.faults.rowsClose(r.query); err != nil {
		_ = r.inner.Close()
		return err
	}
	return r.inner.Close()
}

func (r *w14gRows) Next(dest []driver.Value) error {
	if err := r.faults.rowsFail(r.query); err != nil {
		return err
	}
	err := r.inner.Next(dest)
	if err != nil {
		if errors.Is(err, io.EOF) {
			if faulted := r.faults.rowsErr(r.query); faulted != nil {
				return faulted
			}
		}
		return err
	}
	r.faults.rowsNext(r.query, dest)
	return nil
}

type w14gTx struct {
	inner  driver.Tx
	faults *w14gFaults
}

func (t *w14gTx) Commit() error {
	if err := t.faults.commitErr(); err != nil {
		_ = t.inner.Rollback()
		return err
	}
	return t.inner.Commit()
}

func (t *w14gTx) Rollback() error { return t.inner.Rollback() }

type w14gResult struct {
	inner  driver.Result
	faults *w14gFaults
	query  string
}

func (r w14gResult) LastInsertId() (int64, error) { return r.inner.LastInsertId() }

func (r w14gResult) RowsAffected() (int64, error) {
	r.faults.mu.Lock()
	fn := r.faults.onAffected
	r.faults.mu.Unlock()
	if fn != nil {
		if n, forceErr := fn(r.query); n >= 0 || forceErr {
			if forceErr {
				return 0, w14gErrInjected
			}
			return n, nil
		}
	}
	return r.inner.RowsAffected()
}

// w14gFaultStore 构造带故障注入代理的 SQLite 存储与 wkStoreLabeled 同 schema。
func w14gFaultStore(t *testing.T, label string) (*Store, *w14gFaults) {
	t.Helper()
	faults := &w14gFaults{}
	inner, err := sqlite.NewConnector("file:" + strings.ReplaceAll(t.Name(), "/", "-") + label + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(w14gConnector{inner: inner, faults: faults})
	t.Cleanup(func() {
		faults.reset()
		_ = db.Close()
	})
	for _, ddl := range wkDDLs {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX idx_account_circuit_incidents_key_model_capability ON account_circuit_incidents(scope_kind, capability_hash) WHERE scope_kind = 'key_model' AND capability_hash IS NOT NULL`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts(id,dispatch_revision,circuit_projection_revision) VALUES ('a1',1,0)`); err != nil {
		t.Fatal(err)
	}
	s, err := New(db, SQLite, "", OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	return s, faults
}

func w14gMatchQuery(matchers map[string]error) func(string) error {
	return func(query string) error {
		for needle, err := range matchers {
			if strings.Contains(query, needle) {
				return err
			}
		}
		return nil
	}
}

// ---------------------------------------------------------------------------
// 索引校验错误分支
// ---------------------------------------------------------------------------

func TestW14GCPSQLiteIndexDefinitionFaults(t *testing.T) {
	s, faults := w14gFaultStore(t, "-idx")
	ctx := context.Background()
	spec := sharedcontracts.BusinessSQLiteSchema["account_circuit_incidents"]
	var required sharedcontracts.SQLiteIndexDefinition
	for _, definition := range spec.IndexDefinitions {
		if definition.Name == "idx_account_circuit_incidents_key_model_capability" {
			required = definition
		}
	}
	// index_list 查询失败。
	faults.set(func(f *w14gFaults) {
		f.onQuery = w14gMatchQuery(map[string]error{"PRAGMA index_list": w14gErrInjected})
	})
	if _, _, err := checkSQLiteIndexDefinition(ctx, s.db, "account_circuit_incidents", required); !errors.Is(err, w14gErrInjected) {
		t.Fatalf("index_list 故障必须透传: %v", err)
	}
	// index_list 行扫描失败。
	faults.reset()
	faults.set(func(f *w14gFaults) {
		f.onRowsNext = func(query string, dest []driver.Value) {
			if strings.Contains(query, "PRAGMA index_list") {
				dest[0] = nil
			}
		}
	})
	if _, _, err := checkSQLiteIndexDefinition(ctx, s.db, "account_circuit_incidents", required); err == nil {
		t.Fatalf("index_list 扫描失败必须透传")
	}
	// index_list rows.Err 失败（index_list 命中目标索引后提前 break，无法
	// 走到 EOF，因此用首行即失败钩子注入 rows.Err）。
	faults.reset()
	faults.set(func(f *w14gFaults) {
		f.onRowsFail = func(query string) error {
			if strings.Contains(query, "PRAGMA index_list") {
				return w14gErrInjected
			}
			return nil
		}
	})
	if _, _, err := checkSQLiteIndexDefinition(ctx, s.db, "account_circuit_incidents", required); !errors.Is(err, w14gErrInjected) {
		t.Fatalf("index_list rows.Err 故障必须透传: %v", err)
	}
	// index_list rows.Close 失败。
	faults.reset()
	faults.set(func(f *w14gFaults) {
		f.onRowsClose = func(query string) error {
			if strings.Contains(query, "PRAGMA index_list") {
				return w14gErrInjected
			}
			return nil
		}
	})
	if _, _, err := checkSQLiteIndexDefinition(ctx, s.db, "account_circuit_incidents", required); !errors.Is(err, w14gErrInjected) {
		t.Fatalf("index_list Close 故障必须透传: %v", err)
	}
	// index_info 查询失败。
	faults.reset()
	faults.set(func(f *w14gFaults) {
		f.onQuery = w14gMatchQuery(map[string]error{"PRAGMA index_info": w14gErrInjected})
	})
	if _, _, err := checkSQLiteIndexDefinition(ctx, s.db, "account_circuit_incidents", required); !errors.Is(err, w14gErrInjected) {
		t.Fatalf("index_info 故障必须透传: %v", err)
	}
	// index_info 行扫描失败。
	faults.reset()
	faults.set(func(f *w14gFaults) {
		f.onRowsNext = func(query string, dest []driver.Value) {
			if strings.Contains(query, "PRAGMA index_info") {
				dest[0] = nil
			}
		}
	})
	if _, _, err := checkSQLiteIndexDefinition(ctx, s.db, "account_circuit_incidents", required); err == nil {
		t.Fatalf("index_info 扫描失败必须透传")
	}
	// index_info rows.Err 失败。
	faults.reset()
	faults.set(func(f *w14gFaults) {
		f.onRowsErr = func(query string) error {
			if strings.Contains(query, "PRAGMA index_info") {
				return w14gErrInjected
			}
			return nil
		}
	})
	if _, _, err := checkSQLiteIndexDefinition(ctx, s.db, "account_circuit_incidents", required); !errors.Is(err, w14gErrInjected) {
		t.Fatalf("index_info rows.Err 故障必须透传: %v", err)
	}
	// index_info rows.Close 失败。
	faults.reset()
	faults.set(func(f *w14gFaults) {
		f.onRowsClose = func(query string) error {
			if strings.Contains(query, "PRAGMA index_info") {
				return w14gErrInjected
			}
			return nil
		}
	})
	if _, _, err := checkSQLiteIndexDefinition(ctx, s.db, "account_circuit_incidents", required); !errors.Is(err, w14gErrInjected) {
		t.Fatalf("index_info Close 故障必须透传: %v", err)
	}
	// sqlite_master 查询失败。
	faults.reset()
	faults.set(func(f *w14gFaults) {
		f.onQuery = w14gMatchQuery(map[string]error{"sqlite_master": w14gErrInjected})
	})
	if _, _, err := checkSQLiteIndexDefinition(ctx, s.db, "account_circuit_incidents", required); !errors.Is(err, w14gErrInjected) {
		t.Fatalf("sqlite_master 故障必须透传: %v", err)
	}
	// 空谓词契约遇上 partial 索引 → unexpected partial predicate。
	faults.reset()
	if _, err := s.db.Exec(`CREATE INDEX w14g_partial_idx ON account_circuit_incidents(account_id) WHERE account_id = 'w14g-x'`); err != nil {
		t.Fatal(err)
	}
	custom := sharedcontracts.SQLiteIndexDefinition{Name: "w14g_partial_idx", Columns: []string{"account_id"}, Unique: false}
	ok, detail, err := checkSQLiteIndexDefinition(ctx, s.db, "account_circuit_incidents", custom)
	if err != nil || ok || !strings.Contains(detail, "unexpected partial predicate") {
		t.Fatalf("partial 索引必须拒绝: ok=%v detail=%q err=%v", ok, detail, err)
	}
	// 关闭数据库后 checkKeyModelCapabilityIndex 失败包装。
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.checkKeyModelCapabilityIndex(ctx); err == nil {
		t.Fatalf("关闭数据库后索引校验必须失败")
	}
}

// ---------------------------------------------------------------------------
// AdvanceDispatchRevision
// ---------------------------------------------------------------------------

func TestW14GCPAdvanceDispatchFaults(t *testing.T) {
	s, faults := w14gFaultStore(t, "-adv")
	ctx := context.Background()
	in := DispatchRevision{AccountID: "a1", AccountRuntimeKey: "a1", TransitionID: "w14g-t1", NowMS: 100}
	if res, err := s.AdvanceDispatchRevision(ctx, in); err != nil || res.Status != "applied" || res.DispatchRevision != 2 {
		t.Fatalf("基线 advance = %+v err=%v", res, err)
	}
	// dedupe 查询失败。
	faults.set(func(f *w14gFaults) {
		f.onQuery = w14gMatchQuery(map[string]error{"dedupe_key": w14gErrInjected})
	})
	if _, err := s.AdvanceDispatchRevision(ctx, in); !errors.Is(err, w14gErrInjected) {
		t.Fatalf("dedupe 故障必须透传: %v", err)
	}
	// 幂等重放 commit 失败。
	faults.reset()
	faults.set(func(f *w14gFaults) { f.onCommit = func() error { return w14gErrInjected } })
	if _, err := s.AdvanceDispatchRevision(ctx, in); !errors.Is(err, w14gErrInjected) {
		t.Fatalf("重放 commit 故障必须透传: %v", err)
	}
	faults.reset()
	// 账户行 UPDATE 失败。
	faults.set(func(f *w14gFaults) {
		f.onExec = w14gMatchQuery(map[string]error{"SET dispatch_revision=dispatch_revision+1": w14gErrInjected})
	})
	in2 := in
	in2.TransitionID = "w14g-t2"
	if _, err := s.AdvanceDispatchRevision(ctx, in2); !errors.Is(err, w14gErrInjected) {
		t.Fatalf("UPDATE 故障必须透传: %v", err)
	}
	// 账户行 UPDATE 受影响 0 行 → CAS 冲突。
	faults.reset()
	faults.set(func(f *w14gFaults) {
		f.onAffected = func(query string) (int64, bool) {
			if strings.Contains(query, "SET dispatch_revision=dispatch_revision+1") {
				return 0, false
			}
			return -1, false
		}
	})
	if _, err := s.AdvanceDispatchRevision(ctx, in2); !errors.Is(err, ErrCAS) {
		t.Fatalf("0 行受影响必须 CAS 冲突: %v", err)
	}
	// outbox 插入失败。
	faults.reset()
	faults.set(func(f *w14gFaults) {
		f.onExec = w14gMatchQuery(map[string]error{"INSERT INTO account_circuit_outbox": w14gErrInjected})
	})
	in3 := in
	in3.TransitionID = "w14g-t3"
	if _, err := s.AdvanceDispatchRevision(ctx, in3); err == nil {
		t.Fatalf("outbox 插入故障必须透传")
	}
	// 首次 commit 失败。
	faults.reset()
	faults.set(func(f *w14gFaults) { f.onCommit = func() error { return w14gErrInjected } })
	in4 := in
	in4.TransitionID = "w14g-t4"
	if _, err := s.AdvanceDispatchRevision(ctx, in4); !errors.Is(err, w14gErrInjected) {
		t.Fatalf("commit 故障必须透传: %v", err)
	}
}

// ---------------------------------------------------------------------------
// LoadIncidentForProjection / CompareAndSetIncident
// ---------------------------------------------------------------------------

func TestW14GCPLoadIncidentFaults(t *testing.T) {
	s, faults := w14gFaultStore(t, "-load")
	ctx := context.Background()
	if _, err := s.CompareAndSetIncident(ctx, wkBaseIncident("w14g-scope", "a1", 1)); err != nil {
		t.Fatal(err)
	}
	event := Outbox{AccountID: "a1", CircuitScopeKey: strPtr2("w14g-scope"), IncidentID: strPtr2("inc-w14g-scope"), DispatchRevision: 1}
	// 账户行查询失败。
	faults.set(func(f *w14gFaults) {
		f.onQuery = w14gMatchQuery(map[string]error{"SELECT dispatch_revision FROM": w14gErrInjected})
	})
	if _, err := s.LoadIncidentForProjection(ctx, event); !errors.Is(err, w14gErrInjected) {
		t.Fatalf("账户查询故障必须透传: %v", err)
	}
	// incident 查询失败。
	faults.reset()
	faults.set(func(f *w14gFaults) {
		f.onQuery = w14gMatchQuery(map[string]error{"FROM account_circuit_incidents WHERE circuit_scope_key=?": w14gErrInjected})
	})
	if _, err := s.LoadIncidentForProjection(ctx, event); !errors.Is(err, w14gErrInjected) {
		t.Fatalf("incident 查询故障必须透传: %v", err)
	}
}

func TestW14GCPCASFaults(t *testing.T) {
	s, faults := w14gFaultStore(t, "-cas")
	ctx := context.Background()
	in := wkBaseIncident("w14g-cas", "a1", 1)
	if res, err := s.CompareAndSetIncident(ctx, in); err != nil || res.Status != "applied" {
		t.Fatalf("基线 CAS = %+v err=%v", res, err)
	}
	// 账户行 SELECT 失败。
	faults.set(func(f *w14gFaults) {
		f.onQuery = w14gMatchQuery(map[string]error{"SELECT dispatch_revision, deleted_at FROM": w14gErrInjected})
	})
	in2 := wkBaseIncident("w14g-cas-b", "a1", 1)
	if _, err := s.CompareAndSetIncident(ctx, in2); !errors.Is(err, w14gErrInjected) {
		t.Fatalf("账户 SELECT 故障必须透传: %v", err)
	}
	// 重放路径 dedupe 查询失败。
	faults.reset()
	faults.set(func(f *w14gFaults) {
		f.onQuery = w14gMatchQuery(map[string]error{"dedupe_key": w14gErrInjected})
	})
	if _, err := s.CompareAndSetIncident(ctx, in); !errors.Is(err, w14gErrInjected) {
		t.Fatalf("dedupe 故障必须透传: %v", err)
	}
	// 重放路径 incident 查询失败。
	faults.reset()
	faults.set(func(f *w14gFaults) {
		f.onQuery = w14gMatchQuery(map[string]error{"FROM account_circuit_incidents WHERE circuit_scope_key=?": w14gErrInjected})
	})
	if _, err := s.CompareAndSetIncident(ctx, in); !errors.Is(err, w14gErrInjected) {
		t.Fatalf("重放 incident 查询故障必须透传: %v", err)
	}
	// 重放路径 commit 失败。
	faults.reset()
	faults.set(func(f *w14gFaults) { f.onCommit = func() error { return w14gErrInjected } })
	if _, err := s.CompareAndSetIncident(ctx, in); !errors.Is(err, w14gErrInjected) {
		t.Fatalf("重放 commit 故障必须透传: %v", err)
	}
	// CAS 行锁 SELECT 失败（SQLite 模式无 FOR UPDATE 后缀，查询与普通读取一致）。
	faults.reset()
	faults.set(func(f *w14gFaults) {
		f.onQuery = w14gMatchQuery(map[string]error{"FROM account_circuit_incidents WHERE circuit_scope_key=?": w14gErrInjected})
	})
	if _, err := s.CompareAndSetIncident(ctx, in2); !errors.Is(err, w14gErrInjected) {
		t.Fatalf("行锁 SELECT 故障必须透传: %v", err)
	}
	// outbox 插入失败。
	faults.reset()
	faults.set(func(f *w14gFaults) {
		f.onExec = w14gMatchQuery(map[string]error{"INSERT INTO account_circuit_outbox": w14gErrInjected})
	})
	in3 := wkBaseIncident("w14g-cas-c", "a1", 1)
	if _, err := s.CompareAndSetIncident(ctx, in3); err == nil {
		t.Fatalf("outbox 插入故障必须透传")
	}
	// incident upsert 受影响行数读取失败。
	faults.reset()
	faults.set(func(f *w14gFaults) {
		f.onAffected = func(query string) (int64, bool) {
			if strings.Contains(query, "INSERT INTO account_circuit_incidents") {
				return 0, true
			}
			return -1, false
		}
	})
	if _, err := s.CompareAndSetIncident(ctx, in3); !errors.Is(err, w14gErrInjected) {
		t.Fatalf("upsert 受影响行数故障必须透传: %v", err)
	}
	// 首次 commit 失败。
	faults.reset()
	faults.set(func(f *w14gFaults) { f.onCommit = func() error { return w14gErrInjected } })
	if _, err := s.CompareAndSetIncident(ctx, in3); !errors.Is(err, w14gErrInjected) {
		t.Fatalf("commit 故障必须透传: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Claim / Acknowledge Outbox
// ---------------------------------------------------------------------------

func w14gSeedClaimedEvent(t *testing.T, s *Store) (string, string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.CompareAndSetIncident(ctx, wkBaseIncident("w14g-claim", "a1", 1)); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimOutbox(ctx, "w14g-owner", 200, 1000, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %d err=%v", len(claimed), err)
	}
	return claimed[0].EventID, value(claimed[0].ClaimToken)
}

func TestW14GPClaimOutboxFaults(t *testing.T) {
	s, faults := w14gFaultStore(t, "-claim")
	if _, err := s.CompareAndSetIncident(context.Background(), wkBaseIncident("w14g-claim", "a1", 1)); err != nil {
		t.Fatal(err)
	}
	// claim 查询失败。
	faults.set(func(f *w14gFaults) {
		f.onQuery = w14gMatchQuery(map[string]error{"status='pending' AND available_at_ms<=?": w14gErrInjected})
	})
	if _, err := s.ClaimOutbox(context.Background(), "w14g-owner", 200, 1000, 10); !errors.Is(err, w14gErrInjected) {
		t.Fatalf("claim 查询故障必须透传: %v", err)
	}
	// claim 行迭代失败。
	faults.reset()
	faults.set(func(f *w14gFaults) {
		f.onRowsErr = w14gMatchQuery(map[string]error{"status='pending' AND available_at_ms<=?": w14gErrInjected})
	})
	if _, err := s.ClaimOutbox(context.Background(), "w14g-owner", 200, 1000, 10); !errors.Is(err, w14gErrInjected) {
		t.Fatalf("claim 行迭代故障必须透传: %v", err)
	}
	// claim 行关闭失败。
	faults.reset()
	faults.set(func(f *w14gFaults) {
		f.onRowsClose = w14gMatchQuery(map[string]error{"status='pending' AND available_at_ms<=?": w14gErrInjected})
	})
	if _, err := s.ClaimOutbox(context.Background(), "w14g-owner", 200, 1000, 10); !errors.Is(err, w14gErrInjected) {
		t.Fatalf("claim 行关闭故障必须透传: %v", err)
	}
	// claim UPDATE 失败。
	faults.reset()
	faults.set(func(f *w14gFaults) {
		f.onExec = w14gMatchQuery(map[string]error{"SET status='processing'": w14gErrInjected})
	})
	if _, err := s.ClaimOutbox(context.Background(), "w14g-owner", 200, 1000, 10); !errors.Is(err, w14gErrInjected) {
		t.Fatalf("claim UPDATE 故障必须透传: %v", err)
	}
	// claim UPDATE 受影响 0 行 → 跳过候选。
	faults.reset()
	faults.set(func(f *w14gFaults) {
		f.onAffected = func(query string) (int64, bool) {
			if strings.Contains(query, "SET status='processing'") {
				return 0, false
			}
			return -1, false
		}
	})
	claimed, err := s.ClaimOutbox(context.Background(), "w14g-owner", 200, 1000, 10)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("0 行受影响必须返回空: %d err=%v", len(claimed), err)
	}
	// commit 失败。
	faults.reset()
	faults.set(func(f *w14gFaults) { f.onCommit = func() error { return w14gErrInjected } })
	if _, err := s.ClaimOutbox(context.Background(), "w14g-owner", 200, 1000, 10); !errors.Is(err, w14gErrInjected) {
		t.Fatalf("claim commit 故障必须透传: %v", err)
	}
}

func TestW14GCPAcknowledgeFaults(t *testing.T) {
	s, faults := w14gFaultStore(t, "-ack")
	eventID, token := w14gSeedClaimedEvent(t, s)
	ctx := context.Background()
	// outbox 读取失败。
	faults.set(func(f *w14gFaults) {
		f.onQuery = w14gMatchQuery(map[string]error{"WHERE event_id=?": w14gErrInjected})
	})
	if _, err := s.AcknowledgeOutbox(ctx, eventID, ProjectionKey, token, 300); !errors.Is(err, w14gErrInjected) {
		t.Fatalf("outbox 读取故障必须透传: %v", err)
	}
	// ack UPDATE 失败。
	faults.reset()
	faults.set(func(f *w14gFaults) {
		f.onExec = w14gMatchQuery(map[string]error{"SET status='dispatched'": w14gErrInjected})
	})
	if _, err := s.AcknowledgeOutbox(ctx, eventID, ProjectionKey, token, 300); !errors.Is(err, w14gErrInjected) {
		t.Fatalf("ack UPDATE 故障必须透传: %v", err)
	}
	// ack UPDATE 受影响 0 行。
	faults.reset()
	faults.set(func(f *w14gFaults) {
		f.onAffected = func(query string) (int64, bool) {
			if strings.Contains(query, "SET status='dispatched'") {
				return 0, false
			}
			return -1, false
		}
	})
	if ok, err := s.AcknowledgeOutbox(ctx, eventID, ProjectionKey, token, 300); ok || err != nil {
		t.Fatalf("0 行受影响必须返回 false: %v err=%v", ok, err)
	}
}

// ---------------------------------------------------------------------------
// ListProjectionGaps / ListDispatchRevisions / Cleanup
// ---------------------------------------------------------------------------

func TestW14GCPListProjectionGapsFaults(t *testing.T) {
	s, faults := w14gFaultStore(t, "-gaps")
	ctx := context.Background()
	// dispatch 缺口行迭代失败。
	faults.set(func(f *w14gFaults) {
		f.onRowsErr = w14gMatchQuery(map[string]error{"circuit_projection_revision<dispatch_revision": w14gErrInjected})
	})
	if _, err := s.ListProjectionGaps(ctx, "", 0, "", 10); !errors.Is(err, w14gErrInjected) {
		t.Fatalf("gap 行迭代故障必须透传: %v", err)
	}
	// dispatch 缺口行关闭失败。
	faults.reset()
	faults.set(func(f *w14gFaults) {
		f.onRowsClose = w14gMatchQuery(map[string]error{"circuit_projection_revision<dispatch_revision": w14gErrInjected})
	})
	if _, err := s.ListProjectionGaps(ctx, "", 0, "", 10); !errors.Is(err, w14gErrInjected) {
		t.Fatalf("gap 行关闭故障必须透传: %v", err)
	}
}

func TestW14GCPListDispatchRevisionsFaults(t *testing.T) {
	s, faults := w14gFaultStore(t, "-list")
	ctx := context.Background()
	// 行扫描失败。
	faults.set(func(f *w14gFaults) {
		f.onRowsNext = func(query string, dest []driver.Value) {
			if strings.Contains(query, "SELECT id,dispatch_revision FROM") {
				dest[1] = nil
			}
		}
	})
	if _, err := s.ListDispatchRevisions(ctx, "", 10); err == nil {
		t.Fatalf("行扫描失败必须透传")
	}
	// 行迭代失败。
	faults.reset()
	faults.set(func(f *w14gFaults) {
		f.onRowsErr = w14gMatchQuery(map[string]error{"SELECT id,dispatch_revision FROM": w14gErrInjected})
	})
	if _, err := s.ListDispatchRevisions(ctx, "", 10); !errors.Is(err, w14gErrInjected) {
		t.Fatalf("行迭代故障必须透传: %v", err)
	}
}

func TestW14GCPCleanupFaults(t *testing.T) {
	s, faults := w14gFaultStore(t, "-cleanup")
	ctx := context.Background()
	if _, err := s.CompareAndSetIncident(ctx, wkBaseIncident("w14g-cleanup", "a1", 1)); err != nil {
		t.Fatal(err)
	}
	// 直接 upsert 一条已过期 CLOSED incident，保证清理选择能返回行。
	closed := wkBaseIncident("w14g-cleanup-closed", "a1", 1).Incident
	closed.State = "CLOSED"
	closed.RetainedUntilMS = int64Ptr2(100)
	closed.ProjectedLedgerRevision = 1
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.upsertIncident(ctx, tx, closed); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// 清理 outbox 选择查询失败。
	faults.set(func(f *w14gFaults) {
		f.onQuery = w14gMatchQuery(map[string]error{"SELECT event_id FROM": w14gErrInjected})
	})
	if _, err := s.Cleanup(ctx, 1000, 500, 10); !errors.Is(err, w14gErrInjected) {
		t.Fatalf("清理查询故障必须透传: %v", err)
	}
	// 清理行扫描失败。
	faults.reset()
	faults.set(func(f *w14gFaults) {
		f.onRowsNext = func(query string, dest []driver.Value) {
			if strings.Contains(query, "SELECT circuit_scope_key FROM") {
				dest[0] = nil
			}
		}
	})
	if _, err := s.Cleanup(ctx, 1000, 500, 10); err == nil {
		t.Fatalf("清理行扫描失败必须透传")
	}
	// 清理行迭代失败。
	faults.reset()
	faults.set(func(f *w14gFaults) {
		f.onRowsErr = w14gMatchQuery(map[string]error{"SELECT circuit_scope_key FROM": w14gErrInjected})
	})
	if _, err := s.Cleanup(ctx, 1000, 500, 10); !errors.Is(err, w14gErrInjected) {
		t.Fatalf("清理行迭代故障必须透传: %v", err)
	}
	// 清理 DELETE 失败。
	faults.reset()
	faults.set(func(f *w14gFaults) {
		f.onExec = w14gMatchQuery(map[string]error{"DELETE FROM": w14gErrInjected})
	})
	if _, err := s.Cleanup(ctx, 1000, 500, 10); !errors.Is(err, w14gErrInjected) {
		t.Fatalf("清理 DELETE 故障必须透传: %v", err)
	}
}

// ---------------------------------------------------------------------------
// upsert 默认值与 validateIncident 文本分支
// ---------------------------------------------------------------------------

func TestW14GCPUpsertNilSliceDefaults(t *testing.T) {
	s, _ := w14gFaultStore(t, "-upsert")
	ctx := context.Background()
	incident := wkBaseIncident("w14g-upsert", "a1", 1).Incident
	incident.ChildIncidentIDs = nil
	incident.ConfirmationFailureEvidenceKeys = nil
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.upsertIncident(ctx, tx, incident); err != nil {
		t.Fatalf("nil 切片默认值 upsert 失败: %v", err)
	}
}

func TestW14GCPValidateIncidentTextBranches(t *testing.T) {
	base := wkBaseIncident("w14g-validate", "a1", 1).Incident
	expectFailure := func(name string, mutate func(*Incident)) {
		t.Helper()
		v := base
		mutate(&v)
		if err := validateIncident(&v); err == nil {
			t.Fatalf("%s 必须校验失败", name)
		}
	}
	expectFailure("空 transition id", func(v *Incident) { v.TransitionID = "" })
	expectFailure("超长 runtime key", func(v *Incident) { v.AccountRuntimeKey = strings.Repeat("k", 2000) })
	expectFailure("超长 key fingerprint", func(v *Incident) {
		kind := "key"
		v.ScopeKind = kind
		v.KeyFingerprint = strPtr2(strings.Repeat("f", 300))
	})
	expectFailure("超长 protocol code", func(v *Incident) {
		v.ScopeKind = "protocol_model"
		v.ProtocolCode = strPtr2(strings.Repeat("p", 100))
		v.RequestLane = strPtr2("text")
		v.ModelFamily = strPtr2("bucket")
	})
	expectFailure("超长 request lane", func(v *Incident) {
		v.ScopeKind = "protocol_model"
		v.ProtocolCode = strPtr2("openai")
		v.RequestLane = strPtr2(strings.Repeat("l", 100))
		v.ModelFamily = strPtr2("bucket")
	})
	expectFailure("超长 model family", func(v *Incident) {
		v.ScopeKind = "protocol_model"
		v.ProtocolCode = strPtr2("openai")
		v.RequestLane = strPtr2("text")
		v.ModelFamily = strPtr2(strings.Repeat("m", 300))
	})
	expectFailure("超长 client model", func(v *Incident) {
		v.ScopeKind = "key_model"
		v.KeyFingerprint = strPtr2("fp")
		v.ClientModel = strPtr2(strings.Repeat("c", 300))
		v.CapabilityHash = strPtr2("cap")
		v.CredentialSourceAccountID = strPtr2("src")
		v.ClientEndpointFamily = strPtr2("chat")
		v.FinalUpstreamModel = strPtr2("final")
		v.UpstreamEndpointMode = strPtr2("mode")
	})
	expectFailure("超长 lease id", func(v *Incident) {
		v.LeaseID = strPtr2(strings.Repeat("l", 300))
		v.LeasePurpose = strPtr2("confirmation")
		v.LeaseOwnerRunID = strPtr2("run")
		v.LeaseUntilMS = int64Ptr2(1000)
		v.AttemptStartedAtMS = int64Ptr2(10)
		v.AttemptHardDeadlineMS = int64Ptr2(100)
	})
	expectFailure("超长 lease owner run id", func(v *Incident) {
		v.LeaseID = strPtr2("lease")
		v.LeasePurpose = strPtr2("confirmation")
		v.LeaseOwnerRunID = strPtr2(strings.Repeat("r", 300))
		v.LeaseUntilMS = int64Ptr2(1000)
		v.AttemptStartedAtMS = int64Ptr2(10)
		v.AttemptHardDeadlineMS = int64Ptr2(100)
	})
	expectFailure("超长 parent incident id", func(v *Incident) {
		v.ParentIncidentID = strPtr2(strings.Repeat("p", 300))
	})
	expectFailure("超长 caused by terminal outcome id", func(v *Incident) {
		v.CausedByTerminalOutcomeID = strPtr2(strings.Repeat("t", 300))
	})
}
