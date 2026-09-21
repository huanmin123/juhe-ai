package statsagg

// w13g5_statsagg_kit_test.go 提供可回放的失败注入 SQLite 驱动（包装
// modernc.org/sqlite），覆盖聚合域深层 err 传播臂；注入策略对齐
// taskruns/w12b_failinject_test.go 的既有模式。
//
// 不可达清单（覆盖率登记，证据见各条目）：
//   - aggregate.go aggregateRows 内 MergeAccumulator 错误臂（totalEntries /
//     timeEntries / modelEntries 合并处）：进入合并前 UsageStatsTimeKeysFor
//     已校验 created_at，累加器的 LastUsedAt/LastErrorAt 均来自同一已规范化
//     或必然报错的 created_at，MaxOptionalISO 不可能失败，数据不可构造。
//   - record.go scanUsageStatsRecordRows 的 rows.Err() 迭代中途错误臂：
//     modernc.org/sqlite 驱动在单连接串行测试里无法在迭代中段注入连接故障。
//
// 注入失败错误消息固定为 "w13g5 注入失败"，不携带连接串或敏感数据。

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

const w13g5StatsFailDriverName = "w13g5-statsagg-fail-sqlite"

var (
	w13g5StatsFailDriverRegister sync.Once
	w13g5StatsFailDriversMu      sync.Mutex
	w13g5StatsFailDrivers        = map[string]*w13g5StatsSpec{}
)

// w13g5StatsArmRecord 记一次装载的注入臂（match + 是否命中），供收尾断言。
type w13g5StatsArmRecord struct {
	match string
	fired bool
}

// w13g5StatsSpec 描述一次注入：match 命中查询子串；once 只注入一次；
// afterN 从第 N+1 次命中起持续注入；zero=true 时静默返回 0 行影响，
// 否则返回错误。
type w13g5StatsSpec struct {
	mu     sync.Mutex
	match  string
	once   bool
	afterN bool
	fired  bool
	skip   int
	zero   bool
	// records 由 enableAssert 开启记账后生效：每次装载记录一条，注入真实
	// 消费时回填；测试收尾断言所有记录均已命中。
	records []*w13g5StatsArmRecord
	current *w13g5StatsArmRecord
}

func (spec *w13g5StatsSpec) arm(match string) { spec.armImpl(match, false, false, 0, false) }

func (spec *w13g5StatsSpec) armOnce(match string) { spec.armImpl(match, true, false, 0, false) }

// armAfter 从第 skip+1 次命中开始持续注入（同一语句前 N 次放行）。
func (spec *w13g5StatsSpec) armAfter(match string, skip int) {
	spec.armImpl(match, true, true, skip, false)
}

func (spec *w13g5StatsSpec) armZero(match string) { spec.armImpl(match, false, false, 0, true) }

func (spec *w13g5StatsSpec) armImpl(match string, once, afterN bool, skip int, zero bool) {
	spec.mu.Lock()
	defer spec.mu.Unlock()
	spec.match, spec.once, spec.afterN, spec.fired, spec.skip, spec.zero = match, once, afterN, false, skip, zero
	if spec.records == nil {
		return
	}
	entry := &w13g5StatsArmRecord{match: match}
	spec.records = append(spec.records, entry)
	spec.current = entry
}

// enableAssert 开启装载记账并在测试收尾断言所有装载臂都真实命中（注入被
// 消费）：match 与生产 SQL 漂移或语句从未执行导致的伪覆盖臂在此变红。
func (spec *w13g5StatsSpec) enableAssert(t *testing.T) {
	t.Helper()
	spec.mu.Lock()
	spec.records = []*w13g5StatsArmRecord{}
	spec.mu.Unlock()
	t.Cleanup(func() {
		spec.mu.Lock()
		defer spec.mu.Unlock()
		for _, entry := range spec.records {
			if !entry.fired {
				t.Errorf("w13g5 注入规则未命中（伪覆盖）：match=%q", entry.match)
			}
		}
	})
}

func (spec *w13g5StatsSpec) disarm() {
	spec.mu.Lock()
	defer spec.mu.Unlock()
	spec.match = ""
}

func (spec *w13g5StatsSpec) hit(key string) (fail, zero bool) {
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
	if spec.current != nil {
		spec.current.fired = true
	}
	return !spec.zero, spec.zero
}

func w13g5StatsInjectedErr() error { return errors.New("w13g5 注入失败") }

type w13g5StatsFailDriver struct{ inner driver.Driver }

func (d *w13g5StatsFailDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	w13g5StatsFailDriversMu.Lock()
	spec := w13g5StatsFailDrivers[w13g5StatsFailDriverName]
	w13g5StatsFailDriversMu.Unlock()
	return &w13g5StatsFailConn{inner: conn, spec: spec}, nil
}

type w13g5StatsFailConn struct {
	inner driver.Conn
	spec  *w13g5StatsSpec
}

func (c *w13g5StatsFailConn) Prepare(query string) (driver.Stmt, error) {
	if fail, _ := c.spec.hit(query); fail {
		return nil, w13g5StatsInjectedErr()
	}
	stmt, err := c.inner.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &w13g5StatsFailStmt{inner: stmt, spec: c.spec}, nil
}

func (c *w13g5StatsFailConn) Close() error { return c.inner.Close() }

func (c *w13g5StatsFailConn) Begin() (driver.Tx, error) {
	if fail, _ := c.spec.hit("w13g5-BEGIN"); fail {
		return nil, w13g5StatsInjectedErr()
	}
	tx, err := c.inner.Begin()
	if err != nil {
		return nil, err
	}
	return &w13g5StatsFailTx{inner: tx, spec: c.spec}, nil
}

func (c *w13g5StatsFailConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if fail, zero := c.spec.hit(query); fail {
		return nil, w13g5StatsInjectedErr()
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
	return stmt.Exec(w13g5StatsDriverValues(args))
}

func (c *w13g5StatsFailConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if fail, _ := c.spec.hit(query); fail {
		return nil, w13g5StatsInjectedErr()
	}
	if queryer, ok := c.inner.(driver.QueryerContext); ok {
		return queryer.QueryContext(ctx, query, args)
	}
	stmt, err := c.inner.Prepare(query)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	return stmt.Query(w13g5StatsDriverValues(args))
}

type w13g5StatsFailTx struct {
	inner driver.Tx
	spec  *w13g5StatsSpec
}

func (tx *w13g5StatsFailTx) Commit() error {
	if fail, _ := tx.spec.hit("w13g5-COMMIT"); fail {
		return w13g5StatsInjectedErr()
	}
	return tx.inner.Commit()
}

func (tx *w13g5StatsFailTx) Rollback() error {
	if fail, _ := tx.spec.hit("w13g5-ROLLBACK"); fail {
		return w13g5StatsInjectedErr()
	}
	return tx.inner.Rollback()
}

type w13g5StatsFailStmt struct {
	inner driver.Stmt
	spec  *w13g5StatsSpec
}

func (s *w13g5StatsFailStmt) Close() error { return s.inner.Close() }

func (s *w13g5StatsFailStmt) NumInput() int { return -1 }

func (s *w13g5StatsFailStmt) Exec(args []driver.Value) (driver.Result, error) {
	if fail, zero := s.spec.hit("w13g5-STMT-EXEC"); fail {
		return nil, w13g5StatsInjectedErr()
	} else if zero {
		return driver.RowsAffected(0), nil
	}
	return s.inner.Exec(args)
}

func (s *w13g5StatsFailStmt) Query(args []driver.Value) (driver.Rows, error) {
	if fail, _ := s.spec.hit("w13g5-STMT-QUERY"); fail {
		return nil, w13g5StatsInjectedErr()
	}
	return s.inner.Query(args)
}

func w13g5StatsDriverValues(args []driver.NamedValue) []driver.Value {
	values := make([]driver.Value, len(args))
	for i, arg := range args {
		values[i] = arg.Value
	}
	return values
}

// w13g5StatsOpenFailDB 用注入驱动打开一个绑定全局 spec 的裸 *sql.DB
// （不建 schema），供业务库镜像等辅助句柄复用。
func w13g5StatsOpenFailDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	w13g5StatsFailDriverRegister.Do(func() {
		sql.Register(w13g5StatsFailDriverName, &w13g5StatsFailDriver{inner: &sqlite.Driver{}})
	})
	w13g5StatsFailDriversMu.Lock()
	_ = w13g5StatsFailDrivers[w13g5StatsFailDriverName]
	w13g5StatsFailDriversMu.Unlock()
	db, err := sql.Open(w13g5StatsFailDriverName, "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatalf("打开注入 DB 失败: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// w13g5StatsOpenFailEnv 打开一个共享注入 spec 的 SQLite 测试环境。
func w13g5StatsOpenFailEnv(t *testing.T) (*testEnv, *w13g5StatsSpec) {
	t.Helper()
	w13g5StatsFailDriverRegister.Do(func() {
		sql.Register(w13g5StatsFailDriverName, &w13g5StatsFailDriver{inner: &sqlite.Driver{}})
	})
	spec := &w13g5StatsSpec{}
	spec.enableAssert(t)
	w13g5StatsFailDriversMu.Lock()
	w13g5StatsFailDrivers[w13g5StatsFailDriverName] = spec
	w13g5StatsFailDriversMu.Unlock()
	db := w13g5StatsOpenFailDB(t, filepath.Join(t.TempDir(), "w13g5-stats.sqlite3"))
	for _, statement := range SQLiteTestSchema {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("注入环境建表失败: %v", err)
		}
	}
	zone, err := LoadStatsTimezone("UTC")
	if err != nil {
		t.Fatal(err)
	}
	env := &testEnv{
		t:       t,
		db:      db,
		dialect: Dialect{Postgres: false},
		now:     time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC),
		zone:    zone,
	}
	return env, spec
}

// w13g5StatsErrClock 让 StatsTimezone 直接失败，覆盖时钟错误臂。
type w13g5StatsErrClock struct{}

func (w13g5StatsErrClock) StatsTimezone(context.Context) (*time.Location, error) {
	return nil, w13g5StatsInjectedErr()
}
