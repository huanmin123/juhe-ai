package recordmaintenance

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
	"modernc.org/sqlite"
)

// w12b_failinject_test.go 通过可回放的失败注入驱动（包装 modernc.org/sqlite）
// 覆盖 recordmaintenance 存储与 drain 的错误臂：EnsureSchema/列探测/补列失败、
// Dequeue 错误臂、Delete 失败保留、DrainShutdown 读取失败与空表返回、Run 的
// 失败退避计时分支。
//
// 不可达清单（覆盖率登记）：
//   - queue.go existingColumns 行 Scan 错误（SQLite PRAGMA 与 PG
//     information_schema 两臂）：行形状固定且列类型与 Scan 目标恒兼容。
//   - queue.go existingColumns PG information_schema 查询错误：连接级故障
//     在建表语句处已先行失败，无独立注入点。
//   - queue.go Dequeue rows.Scan / rows.Err() 非零：modernc sqlite 与 pgx
//     的迭代期错误均先经 Scan/Close 以错误返回，无法单独注入。
//   - queue.go PG ADD COLUMN IF NOT EXISTS 臂：仅当共享表缺 v2 列时执行，
//     w1cover 表为全列幂等建表（见 w12b_pg_gate_test.go 头注释）。
//
// 注入失败错误消息固定为 "w12b 注入失败"，不携带连接串或敏感数据。

const w12bRMDriverName = "w12b-rm-fail-sqlite"

var (
	w12bRMDriversMu sync.Mutex
	w12bRMDrivers   = map[string]*w12bFailSpec{}
)

var w12bRMRegister sync.Once

// w12bFailSpec 描述一次注入：match 命中查询子串；once 只注入一次。
type w12bFailSpec struct {
	mu    sync.Mutex
	match string
	once  bool
	fired bool
}

func (spec *w12bFailSpec) arm(match string) {
	spec.mu.Lock()
	defer spec.mu.Unlock()
	spec.match, spec.once, spec.fired = match, false, false
}

func (spec *w12bFailSpec) armOnce(match string) {
	spec.mu.Lock()
	defer spec.mu.Unlock()
	spec.match, spec.once, spec.fired = match, true, false
}

func (spec *w12bFailSpec) disarm() {
	spec.mu.Lock()
	defer spec.mu.Unlock()
	spec.match = ""
}

func (spec *w12bFailSpec) hit(query string) bool {
	spec.mu.Lock()
	defer spec.mu.Unlock()
	if spec.match == "" || !strings.Contains(query, spec.match) {
		return false
	}
	if spec.once && spec.fired {
		return false
	}
	spec.fired = true
	return true
}

func w12bRMInjectedErr() error { return errors.New("w12b 注入失败") }

type w12bFailDriver struct{ inner driver.Driver }

func (d *w12bFailDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	w12bRMDriversMu.Lock()
	spec := w12bRMDrivers[w12bRMDriverName]
	w12bRMDriversMu.Unlock()
	return &w12bFailConn{inner: conn, spec: spec}, nil
}

type w12bFailConn struct {
	inner driver.Conn
	spec  *w12bFailSpec
}

func (c *w12bFailConn) Prepare(query string) (driver.Stmt, error) {
	stmt, err := c.inner.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &w12bFailStmt{inner: stmt}, nil
}

func (c *w12bFailConn) Close() error { return c.inner.Close() }

func (c *w12bFailConn) Begin() (driver.Tx, error) { return c.inner.Begin() }

func (c *w12bFailConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.spec.hit(query) {
		return nil, w12bRMInjectedErr()
	}
	if execer, ok := c.inner.(driver.ExecerContext); ok {
		return execer.ExecContext(ctx, query, args)
	}
	stmt, err := c.inner.Prepare(query)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	return stmt.Exec(w12bRMValues(args))
}

func (c *w12bFailConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.spec.hit(query) {
		return nil, w12bRMInjectedErr()
	}
	if queryer, ok := c.inner.(driver.QueryerContext); ok {
		return queryer.QueryContext(ctx, query, args)
	}
	stmt, err := c.inner.Prepare(query)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	return stmt.Query(w12bRMValues(args))
}

type w12bFailStmt struct{ inner driver.Stmt }

func (s *w12bFailStmt) Close() error { return s.inner.Close() }

func (s *w12bFailStmt) NumInput() int { return -1 }

func (s *w12bFailStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.inner.Exec(args)
}

func (s *w12bFailStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.inner.Query(args)
}

func w12bRMValues(args []driver.NamedValue) []driver.Value {
	values := make([]driver.Value, len(args))
	for i, arg := range args {
		values[i] = arg.Value
	}
	return values
}

// w12bOpenFailStore 打开一个共享注入 spec 的 SQLite 队列存储（不建表）。
func w12bOpenFailStore(t *testing.T) (*Store, *sql.DB, *w12bFailSpec) {
	t.Helper()
	w12bRMRegister.Do(func() {
		sql.Register(w12bRMDriverName, &w12bFailDriver{inner: &sqlite.Driver{}})
	})
	spec := &w12bFailSpec{}
	w12bRMDriversMu.Lock()
	w12bRMDrivers[w12bRMDriverName] = spec
	w12bRMDriversMu.Unlock()
	db, err := sql.Open(w12bRMDriverName, "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "w12b-rm.sqlite3"))+"?_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		t.Fatalf("打开注入存储失败: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	store, err := OpenStore(db, false)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	return store, db, spec
}

func w12bDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestW12bEnsureSchemaFailures(t *testing.T) {
	ctx := context.Background()

	// CREATE TABLE 注入失败。
	store, _, spec := w12bOpenFailStore(t)
	spec.arm("CREATE TABLE")
	if err := store.EnsureSchema(ctx); err == nil {
		t.Fatal("CREATE TABLE 注入失败应报错")
	}

	// 首次建表成功后列探测（PRAGMA）失败 → ensureSnapshotColumns 失败。
	store3, _, spec3 := w12bOpenFailStore(t)
	db3 := store3.db
	// 独立临时库：直接构造 6 列旧表（无 v2 扩展列）。
	if _, err := db3.ExecContext(ctx, `CREATE TABLE record_maintenance_jobs (
	  id TEXT PRIMARY KEY, type TEXT NOT NULL, cutoff_at TEXT NOT NULL,
	  batch_size INTEGER NOT NULL, max_batches INTEGER NOT NULL, created_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	spec3.arm("PRAGMA table_info")
	if err := store3.EnsureSchema(ctx); err == nil {
		t.Fatal("PRAGMA 注入失败应使升级报错")
	}

	// 补列 ALTER 注入失败（非 duplicate column）→ ensureSnapshotColumns 失败。
	spec3.arm("ALTER TABLE")
	if err := store3.EnsureSchema(ctx); err == nil {
		t.Fatal("ALTER TABLE 注入失败应报错")
	}
	spec3.disarm()
	if err := store3.EnsureSchema(ctx); err != nil {
		t.Fatalf("解除注入后应幂等通过: %v", err)
	}
}

func TestW12bDequeueArms(t *testing.T) {
	ctx := context.Background()

	// limit<=0 no-op。
	store, _, _ := w12bOpenFailStore(t)
	jobs, err := store.Dequeue(ctx, 0)
	if err != nil || jobs != nil {
		t.Fatalf("limit<=0 应返回 nil,nil: %v %v", jobs, err)
	}

	// SELECT 注入失败。
	store2, db2, spec2 := w12bOpenFailStore(t)
	if err := store2.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db2.Exec(`INSERT INTO record_maintenance_jobs (id, type, cutoff_at, batch_size, max_batches, created_at)
		VALUES ('w12b-rm-1', 'x', 'c', 1, 1, 'a')`); err != nil {
		t.Fatal(err)
	}
	spec2.arm("FROM record_maintenance_jobs")
	if _, err := store2.Dequeue(ctx, 10); err == nil {
		t.Fatal("SELECT 注入失败应报错")
	}
	spec2.disarm()
	if got, err := store2.Dequeue(ctx, 10); err != nil || len(got) != 1 {
		t.Fatalf("解除注入后应可读: %v %v", got, err)
	}
}

// TestW12bDrainErrorArms drain 层错误臂：Dequeue 失败中止本轮；
// 快照段执行成功但删行失败；单任务执行成功但删行失败。
func TestW12bDrainErrorArms(t *testing.T) {
	ctx := context.Background()
	runner := &mockRunner{}

	// Dequeue 失败 → DrainOnce 返回错误。
	store, _, spec := w12bOpenFailStore(t)
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	drainer := &Drainer{Store: store, Runner: runner, Logger: w12bDiscardLogger(), BatchSize: 5}
	spec.arm("FROM record_maintenance_jobs")
	if _, err := drainer.DrainOnce(ctx); err == nil {
		t.Fatal("Dequeue 注入失败应报错")
	}
	spec.disarm()

	// 快照任务执行成功但 Delete 失败：段保留并报错。
	store2, _, spec2 := w12bOpenFailStore(t)
	if err := store2.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	seedRow(t, store2.db, "w12b-snap-del", retention.JobTypeAccountUsageSnapshotUpsert, "c", 1, 1, "a")
	drainer2 := &Drainer{Store: store2, Runner: runner, Logger: w12bDiscardLogger()}
	spec2.armOnce("DELETE FROM record_maintenance_jobs")
	if _, err := drainer2.DrainOnce(ctx); err == nil {
		t.Fatal("快照段删行失败应报错")
	}
	if got := pendingCount(t, store2); got != 1 {
		t.Fatalf("删行失败应保留任务: %d", got)
	}

	// 单任务执行成功但 Delete 失败（快照段先行成功排空，避免队头阻塞）。
	spec2.disarm()
	if _, err := drainer2.DrainOnce(ctx); err != nil {
		t.Fatalf("解除注入后应排空快照行: %v", err)
	}
	spec2.armOnce("DELETE FROM record_maintenance_jobs")
	seedRow(t, store2.db, "w12b-clean-del", retention.JobTypeNonBusinessDataCleanup, "c", 1, 1, "b")
	if _, err := drainer2.DrainOnce(ctx); err == nil {
		t.Fatal("清理行删行失败应报错")
	}
	if got := pendingCount(t, store2); got != 1 {
		t.Fatalf("删行失败应保留任务: %d", got)
	}
}

func TestW12bDrainShutdownErrorAndEmptyArms(t *testing.T) {
	ctx := context.Background()
	runner := &mockRunner{}

	// 空表：Dequeue 成功但无行 → 返回 0。
	store, _, _ := w12bOpenFailStore(t)
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	drainer := &Drainer{Store: store, Runner: runner, Logger: w12bDiscardLogger()}
	if got := drainer.DrainShutdown(3); got != 0 {
		t.Fatalf("空表停机排空应为 0: %d", got)
	}
	// maxBatches < 1 归一。
	if got := drainer.DrainShutdown(0); got != 0 {
		t.Fatalf("空表 + 缺省批次应为 0: %d", got)
	}

	// Dequeue 失败：警告并返回已处理数。
	store2, _, spec2 := w12bOpenFailStore(t)
	if err := store2.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	spec2.arm("FROM record_maintenance_jobs")
	drainer2 := &Drainer{Store: store2, Runner: runner, Logger: w12bDiscardLogger()}
	if got := drainer2.DrainShutdown(2); got != 0 {
		t.Fatalf("读取失败应返回 0: %d", got)
	}
}

// TestW12bRunRetryBackoffTimerBranch Run 循环失败退避：短间隔下命中
// timer.C 分支并再次进入 drain，随后 stop 退出。
func TestW12bRunRetryBackoffTimerBranch(t *testing.T) {
	store, _, spec := w12bOpenFailStore(t)
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	spec.arm("FROM record_maintenance_jobs")
	drainer := &Drainer{
		Store: store, Runner: &mockRunner{}, Logger: w12bDiscardLogger(),
		FlushInterval: time.Millisecond, RetryDelay: 5 * time.Millisecond,
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		drainer.Run(stop)
	}()
	time.Sleep(80 * time.Millisecond)
	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run 未在 stop 后退出")
	}
}
