package statsverify

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

	"modernc.org/sqlite"
)

// w12b_failinject_test.go 以可回放的失败注入驱动（stats 与 business 两个
// 句柄各自独立 spec）覆盖 statsverify 各写路径的错误臂：groupstats 脏协议
// 的 PG/SQLite 双模、client-ip 聚合/窗口刷新、一致性检查、job 入口、
// OpenStore/EnsureSchema/LoadUsageStats* 配置臂，以及事务 BEGIN/COMMIT 注入。
//
// 不可达清单（覆盖率登记）：
//   - store.go checkPostgresSchema 的 missing-tables 臂：w1cover 已补齐冻结
//     表后恒通过，无法在不删除共享表的情况下注入（query 错误臂经
//     PG-mode-over-sqlite 句柄覆盖）。
//   - groupstats.go 各 load* 的行 Scan 错误与 rows.Err() 非零：列类型恒
//     兼容，modernc sqlite 迭代期错误先经 Scan/Close 返回。
//   - ipnorm.go normalizeIpv4 空串臂、NormalizeClientIPForStats 二次空串臂、
//     clientIPIdentity ParseInt 错误臂：入参已前置校验或 sha256 十六进制恒
//     可解析，属防御守卫。
//
//   - store.go OpenStore 的 EnsureSchema 失败臂与 sql.Open 错误臂：OpenStore
//     固定使用真实 "sqlite" 驱动（无法注入），懒连接下 sql.Open 恒成功；
//     等价的 EnsureSchema 失败语义由直接构造 Store 的注入用例覆盖。
//   - store.go checkPostgresSchema 的 rows.Scan/rows.Err 与 missing-tables
//     臂：SQLite 句柄在查询处先行失败，真实 PG 已补齐冻结表后恒通过。
//   - clientipwindows.go take 的 PG FOR UPDATE SKIP LOCKED lockClause 臂与
//     clearClientIPRangeWindowDirtyRows 的 PG DELETE..USING VALUES 臂：
//     仅 PG 模式分支，PG 会话级故障无注入点。
//   - clientipwindows.go readDirtyRows 的行 Scan/rows.Err 臂：列类型恒兼容。
//   - clientipwindows.go RefreshClientIPUsageRangeWindows 的空窗口臂：
//     FixedUsageStatsDateKeys 恒返回固定天数。
//   - clientipwindows.go refreshRangeWindow per-hash 的 len(ipHashes)==0 臂：
//     调用点以非空 claim 调用，空集在 hasStale 分支已返回。
//   - consistency.go sumUsageStatsHourly 的 NoRows 臂：无 GROUP BY 聚合
//     恒返回一行；hourly SUM 的 sqlFloat 错误臂：SUM 聚合恒返回数值。
// 注入失败错误消息固定为 "w12b 注入失败"，不携带连接串或敏感数据。

const (
	w12bSvStatsDriverName    = "w12b-sv-fail-stats"
	w12bSvBusinessDriverName = "w12b-sv-fail-business"
)

var (
	w12bSvDriversMu sync.Mutex
	w12bSvDrivers   = map[string]*w12bFailSpec{}
	w12bSvRegister  sync.Once
)

// w12bFailArmRecord 记一次装载的注入臂（match + 是否命中），供收尾断言。
type w12bFailArmRecord struct {
	match  string
	fired  bool
	exempt bool
}

type w12bFailSpec struct {
	mu    sync.Mutex
	match string
	once  bool
	fired bool
	// records 由 enableAssert 开启记账后生效：每次装载记录一条，命中时回填；
	// 测试收尾断言所有非豁免记录均已命中。
	records []*w12bFailArmRecord
	current *w12bFailArmRecord
}

func (spec *w12bFailSpec) arm(match string) { spec.armImpl(match, false, false) }

func (spec *w12bFailSpec) armOnce(match string) { spec.armImpl(match, true, false) }

// armGuard 装载一条负对照守卫臂（故意永不命中，用于让另一句柄保持放行），
// 豁免收尾断言。
func (spec *w12bFailSpec) armGuard(match string) { spec.armImpl(match, false, true) }

func (spec *w12bFailSpec) armImpl(match string, once, exempt bool) {
	spec.mu.Lock()
	defer spec.mu.Unlock()
	spec.match, spec.once, spec.fired = match, once, false
	if spec.records == nil {
		return
	}
	entry := &w12bFailArmRecord{match: match, exempt: exempt}
	spec.records = append(spec.records, entry)
	spec.current = entry
}

// enableAssert 开启装载记账并在测试收尾断言所有非豁免装载臂都真实命中：
// match 与生产 SQL 漂移或语句从未执行导致的伪覆盖臂在此变红。
func (spec *w12bFailSpec) enableAssert(t *testing.T) {
	t.Helper()
	spec.mu.Lock()
	spec.records = []*w12bFailArmRecord{}
	spec.mu.Unlock()
	t.Cleanup(func() {
		spec.mu.Lock()
		defer spec.mu.Unlock()
		for _, entry := range spec.records {
			if !entry.fired && !entry.exempt {
				t.Errorf("w12b 注入规则未命中（伪覆盖）：match=%q", entry.match)
			}
		}
	})
}

func (spec *w12bFailSpec) disarm() {
	spec.mu.Lock()
	defer spec.mu.Unlock()
	spec.match = ""
}

// hit 消费式命中（once 只注入一次）。
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
	if spec.current != nil {
		spec.current.fired = true
	}
	return true
}

// peek 非消费命中：BEGIN 阶段预判 COMMIT 注入，同时带回当前记账记录，
// 供真实提交注入时回填 fired。
func (spec *w12bFailSpec) peek(match string) (*w12bFailArmRecord, bool) {
	spec.mu.Lock()
	defer spec.mu.Unlock()
	if spec.match != match {
		return nil, false
	}
	return spec.current, true
}

func w12bInjectedErr() error { return errors.New("w12b 注入失败") }

func w12bSvDriverFor(name string, inner driver.Driver) driver.Driver {
	return &w12bFailDriver{name: name, inner: inner}
}

type w12bFailDriver struct {
	name  string
	inner driver.Driver
}

func (d *w12bFailDriver) Open(dsn string) (driver.Conn, error) {
	conn, err := d.inner.Open(dsn)
	if err != nil {
		return nil, err
	}
	w12bSvDriversMu.Lock()
	spec := w12bSvDrivers[d.name]
	w12bSvDriversMu.Unlock()
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

func (c *w12bFailConn) Begin() (driver.Tx, error) {
	if c.spec.hit("BEGIN") {
		return nil, w12bInjectedErr()
	}
	tx, err := c.inner.Begin()
	if err != nil {
		return nil, err
	}
	commitRecord, failCommit := c.spec.peek("COMMIT")
	return &w12bFailTx{inner: tx, spec: c.spec, commitRecord: commitRecord, failCommit: failCommit}, nil
}

type w12bFailTx struct {
	inner        driver.Tx
	spec         *w12bFailSpec
	commitRecord *w12bFailArmRecord
	failCommit   bool
	rolledBack   bool
}

func (t *w12bFailTx) Commit() error {
	if t.failCommit {
		// 注入提交失败时先回滚底层事务，避免把仍处于事务态的连接归还连接池。
		_ = t.inner.Rollback()
		t.rolledBack = true
		if t.commitRecord != nil {
			t.commitRecord.fired = true
		}
		return w12bInjectedErr()
	}
	return t.inner.Commit()
}

func (t *w12bFailTx) Rollback() error {
	if t.rolledBack || t.inner == nil {
		return nil
	}
	t.rolledBack = true
	return t.inner.Rollback()
}

func (c *w12bFailConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.spec.hit(query) {
		return nil, w12bInjectedErr()
	}
	if execer, ok := c.inner.(driver.ExecerContext); ok {
		return execer.ExecContext(ctx, query, args)
	}
	stmt, err := c.inner.Prepare(query)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	return stmt.Exec(w12bSvValues(args))
}

func (c *w12bFailConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.spec.hit(query) {
		return nil, w12bInjectedErr()
	}
	if queryer, ok := c.inner.(driver.QueryerContext); ok {
		return queryer.QueryContext(ctx, query, args)
	}
	stmt, err := c.inner.Prepare(query)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	return stmt.Query(w12bSvValues(args))
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

func w12bSvValues(args []driver.NamedValue) []driver.Value {
	values := make([]driver.Value, len(args))
	for i, arg := range args {
		values[i] = arg.Value
	}
	return values
}

func w12bSvRegisterDrivers() {
	w12bSvRegister.Do(func() {
		sql.Register(w12bSvStatsDriverName, w12bSvDriverFor(w12bSvStatsDriverName, &sqlite.Driver{}))
		sql.Register(w12bSvBusinessDriverName, w12bSvDriverFor(w12bSvBusinessDriverName, &sqlite.Driver{}))
	})
}

// w12bSvOpenStore 打开 stats/business 双句柄分别走独立注入 spec 的测试存储。
func w12bSvOpenStore(t *testing.T) (*Store, *w12bFailSpec, *w12bFailSpec) {
	t.Helper()
	w12bSvRegisterDrivers()
	statsSpec, businessSpec := &w12bFailSpec{}, &w12bFailSpec{}
	statsSpec.enableAssert(t)
	businessSpec.enableAssert(t)
	w12bSvDriversMu.Lock()
	w12bSvDrivers[w12bSvStatsDriverName] = statsSpec
	w12bSvDrivers[w12bSvBusinessDriverName] = businessSpec
	w12bSvDriversMu.Unlock()
	dir := t.TempDir()
	statsDB, err := sql.Open(w12bSvStatsDriverName, "file:"+filepath.ToSlash(filepath.Join(dir, "stats.sqlite3"))+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	statsDB.SetMaxOpenConns(1)
	businessDB, err := sql.Open(w12bSvBusinessDriverName, "file:"+filepath.ToSlash(filepath.Join(dir, "business.sqlite3"))+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	businessDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = statsDB.Close(); _ = businessDB.Close() })
	store := &Store{mode: StoreSQLite, db: statsDB, business: businessDB}
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("注入存储建表失败: %v", err)
	}
	return store, statsSpec, businessSpec
}

func w12bSvDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestW12bSvStoreConfigArms(t *testing.T) {
	// SQLite PRAGMA 失败臂。
	missing := filepath.Join(t.TempDir(), "missing", "x.sqlite3")
	if _, err := OpenStore(StoreConfig{Mode: StoreSQLite, SQLiteStatsPath: missing, SQLiteBusinessPath: filepath.Join(t.TempDir(), "b.sqlite3")}); err == nil {
		t.Fatal("stats 库 PRAGMA 失败应报错")
	}
	if _, err := OpenStore(StoreConfig{Mode: StoreSQLite, SQLiteStatsPath: filepath.Join(t.TempDir(), "s.sqlite3"), SQLiteBusinessPath: missing}); err == nil {
		t.Fatal("business 库 PRAGMA 失败应报错")
	}
	// PG 极限校验臂。
	if _, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: "postgres://w12b/x", PostgresMaxOpenConns: 4, PostgresMaxIdleConns: 5}); err == nil {
		t.Fatal("maxIdle > maxOpen 应报错")
	}
	// PG-mode 句柄挂在 SQLite 驱动上：checkPostgresSchema 查询/EnsureSchema 臂。
	w12bSvRegisterDrivers()
	plainDB, err := sql.Open("sqlite", filepath.ToSlash(filepath.Join(t.TempDir(), "pgmode.sqlite3")))
	if err != nil {
		t.Fatal(err)
	}
	defer plainDB.Close()
	pgMode := &Store{mode: StorePostgres, db: plainDB}
	if err := pgMode.checkPostgresSchema(context.Background()); err == nil {
		t.Fatal("PG schema 检查在 SQLite 句柄上应失败")
	}
	if err := pgMode.EnsureSchema(context.Background()); err == nil {
		t.Fatal("PG EnsureSchema 在 SQLite 句柄上应失败")
	}
	// LoadUsageStatsTimezone 错误臂：缺设置 / 坏 JSON / 坏时区 / 读取注入。
	store, _, businessSpec := w12bSvOpenStore(t)
	ctx := context.Background()
	if _, err := store.LoadUsageStatsTimezone(ctx, time.Now()); err == nil {
		t.Fatal("缺 usageStatsTimezone 应报错")
	}
	mustExec(t, ctx, store.business, `INSERT INTO system_settings (system_account_id, key, value_json) VALUES ('sys_admin', 'usageStatsTimezone', '{bad')`)
	if _, err := store.LoadUsageStatsTimezone(ctx, time.Now()); err == nil {
		t.Fatal("坏 JSON 应报错")
	}
	mustExec(t, ctx, store.business, `UPDATE system_settings SET value_json = '"Mars/Phobos"' WHERE key = 'usageStatsTimezone'`)
	if _, err := store.LoadUsageStatsTimezone(ctx, time.Now()); err == nil {
		t.Fatal("未知时区应报错")
	}
	mustExec(t, ctx, store.business, `UPDATE system_settings SET value_json = '"UTC"' WHERE key = 'usageStatsTimezone'`)
	if _, err := store.LoadUsageStatsTimezone(ctx, time.Now()); err != nil {
		t.Fatalf("UTC 应通过: %v", err)
	}
	businessSpec.arm("system_settings")
	if _, err := store.LoadUsageStatsTimezone(ctx, time.Now().Add(2*UsageStatsTimezoneCacheTTL)); err == nil {
		t.Fatal("读取注入失败应报错")
	}
	businessSpec.disarm()
	// queryRowContext business 分支。
	if err := store.queryRowContext(ctx, `SELECT value_json FROM system_settings WHERE key = 'usageStatsTimezone' LIMIT 1`).Scan(new(sql.NullString)); err != nil {
		t.Fatalf("business 分支查询应可用: %v", err)
	}
	// PG-mode 无 pool 的 Close 臂。
	if err := pgMode.Close(); err != nil {
		t.Fatalf("PG-mode Close 应走 db.Close: %v", err)
	}
}

func TestW12bSvOpenStoreEnsureSchemaFailure(t *testing.T) {
	w12bSvRegisterDrivers()
	spec := &w12bFailSpec{match: "CREATE TABLE"}
	w12bSvDriversMu.Lock()
	w12bSvDrivers[w12bSvStatsDriverName] = spec
	w12bSvDrivers[w12bSvBusinessDriverName] = &w12bFailSpec{}
	w12bSvDriversMu.Unlock()
	dir := t.TempDir()
	statsDB, err := sql.Open(w12bSvStatsDriverName, "file:"+filepath.ToSlash(filepath.Join(dir, "s.sqlite3")))
	if err != nil {
		t.Fatal(err)
	}
	defer statsDB.Close()
	businessDB, err := sql.Open(w12bSvBusinessDriverName, "file:"+filepath.ToSlash(filepath.Join(dir, "b.sqlite3")))
	if err != nil {
		t.Fatal(err)
	}
	defer businessDB.Close()
	store := &Store{mode: StoreSQLite, db: statsDB, business: businessDB}
	if err := store.EnsureSchema(context.Background()); err == nil {
		t.Fatal("CREATE TABLE 注入失败应报错")
	}
}

func TestW12bSvGroupStatsErrorArms(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	refresh := func(s *Store) (int, error) {
		return s.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Now: now})
	}

	store, statsSpec, businessSpec := w12bSvOpenStore(t)
	seedGroupFixture(t, ctx, store)

	// SQLite 事务开启/提交注入。
	businessSpec.arm("BEGIN")
	if _, err := refresh(store); err == nil {
		t.Fatal("business BEGIN 注入应报错")
	}
	// 先解除 business BEGIN，stats BEGIN 臂才会被真实消费。
	businessSpec.disarm()
	statsSpec.arm("BEGIN")
	if _, err := refresh(store); err == nil {
		t.Fatal("stats BEGIN 注入应报错")
	}
	statsSpec.disarm()
	businessSpec.arm("group_account_stats_dirty")
	if _, err := refresh(store); err == nil {
		t.Fatal("脏表注入应报错")
	}
	businessSpec.disarm()
	// business COMMIT 注入。
	businessSpec.arm("COMMIT")
	statsSpec.armGuard("group_account_stats_dirty") // stats 侧不命中，保证流程到达提交
	if _, err := refresh(store); err == nil {
		t.Fatal("business COMMIT 注入应报错")
	}
	statsSpec.disarm()
	businessSpec.disarm()
	// stats COMMIT 注入（business 侧不命中）。
	statsSpec.arm("COMMIT")
	businessSpec.armGuard("usage_stats_never_matches")
	if _, err := refresh(store); err == nil {
		t.Fatal("stats COMMIT 注入应报错")
	}
	businessSpec.disarm()
	statsSpec.disarm()
	// hasStats 探针注入。
	statsSpec.arm("SELECT 1 FROM")
	if _, err := refresh(store); err == nil {
		t.Fatal("hasStats 注入应报错")
	}
	// 初始标记写入注入。
	statsSpec.disarm()
	businessSpec.arm("INSERT INTO group_account_stats_dirty")
	if _, err := refresh(store); err == nil {
		t.Fatal("initial_cache_build 写入注入应报错")
	}
	businessSpec.disarm()
	// PG-mode 双臂：beginWriteTx 错误与 $n 占位符在 SQLite 上必然失败。
	pgMode, pgStatsSpec, _ := w12bSvOpenStore(t)
	pgMode.mode = StorePostgres
	pgStatsSpec.arm("BEGIN")
	if _, err := refresh(pgMode); err == nil {
		t.Fatal("PG-mode BEGIN 注入应报错")
	}
	pgStatsSpec.disarm()
	if _, err := refresh(pgMode); err == nil {
		t.Fatal("PG-mode $n SQL 在 SQLite 上应失败")
	}
	// MarkAllGroupAccountStatsDirty 臂。
	store2, statsSpec2, businessSpec2 := w12bSvOpenStore(t)
	businessSpec2.arm("BEGIN")
	if err := store2.MarkAllGroupAccountStatsDirty(ctx, "w12b", now); err == nil {
		t.Fatal("SQLite business BEGIN 注入应报错")
	}
	businessSpec2.arm("group_account_stats_dirty")
	if err := store2.MarkAllGroupAccountStatsDirty(ctx, "w12b", now); err == nil {
		t.Fatal("脏行写入注入应报错")
	}
	businessSpec2.disarm()
	businessSpec2.arm("COMMIT")
	if err := store2.MarkAllGroupAccountStatsDirty(ctx, "w12b", now); err == nil {
		t.Fatal("business COMMIT 注入应报错")
	}
	businessSpec2.disarm()
	pgStore2 := &Store{mode: StorePostgres, db: store2.db, business: store2.business}
	pgStatsSpec2 := statsSpec2
	pgStatsSpec2.arm("BEGIN")
	if err := pgStore2.MarkAllGroupAccountStatsDirty(ctx, "w12b", now); err == nil {
		t.Fatal("PG-mode BEGIN 注入应报错")
	}
	pgStatsSpec2.disarm()
	if err := pgStore2.MarkAllGroupAccountStatsDirty(ctx, "w12b", now); err == nil {
		t.Fatal("PG-mode $n SQL 在 SQLite 上应失败")
	}
}

func TestW12bSvGroupStatsRebuildErrorArms(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	refresh := func(s *Store, limit int) (int, error) {
		return s.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Limit: limit, Now: now})
	}
	store, statsSpec, businessSpec := w12bSvOpenStore(t)
	seedGroupFixture(t, ctx, store)

	arms := []struct {
		match    string
		spec     *w12bFailSpec
		note     string
		wantArms func(*w12bFailSpec)
	}{
		{"FROM groups", businessSpec, "groups 分页注入", nil},
		{"DELETE FROM group_account_stats WHERE", statsSpec, "缓存清理注入", nil},
		{"INSERT INTO group_account_stats ", statsSpec, "缓存写入注入", nil},
		{"INNER JOIN", businessSpec, "join 注入", nil},
		{"WHERE id IN", businessSpec, "groups 全量解析注入", nil},
	}
	for _, tc := range arms {
		if err := store.MarkAllGroupAccountStatsDirty(ctx, "w12b-rebuild", now); err != nil {
			t.Fatal(err)
		}
		tc.spec.arm(tc.match)
		if _, err := refresh(store, 0); err == nil {
			t.Fatalf("%s 应报错", tc.note)
		}
		tc.spec.disarm()
		// 排干脏表，避免影响后续用例。
		if _, err := refresh(store, 0); err != nil {
			t.Fatalf("%s 解除后应完成: %v", tc.note, err)
		}
	}
	// 游标耗尽后删除 __all__ 注入。
	if err := store.MarkAllGroupAccountStatsDirty(ctx, "w12b-rebuild-2", now); err != nil {
		t.Fatal(err)
	}
	if _, err := refresh(store, 0); err != nil {
		t.Fatalf("第二次全量应完成: %v", err)
	}
	businessSpec.arm("DELETE FROM group_account_stats_dirty")
	mustExec(t, ctx, store.business, `INSERT INTO group_account_stats_dirty (group_id, reason, updated_at) VALUES ('__all__', 'all_cursor:zzz', '2026-09-17T11:00:00.000Z')`)
	if _, err := refresh(store, 0); err == nil {
		t.Fatal("脏行删除注入应报错")
	}
	businessSpec.disarm()
	// 游标更新注入（limit=1 且组数>1）。
	mustExec(t, ctx, store.business, `DELETE FROM group_account_stats_dirty`)
	mustExec(t, ctx, store.business, `INSERT INTO groups (id, system_account_id) VALUES ('w12b-ga', 'sys'), ('w12b-gb', 'sys')`)
	mustExec(t, ctx, store.business, `INSERT INTO group_account_stats_dirty (group_id, reason, updated_at) VALUES ('__all__', 'w12b-cursor', '2026-09-17T12:00:00.000Z')`)
	businessSpec.arm("UPDATE group_account_stats_dirty")
	if _, err := refresh(store, 1); err == nil {
		t.Fatal("游标更新注入应报错")
	}
	businessSpec.disarm()
	// 按组路径的删除注入。
	mustExec(t, ctx, store.business, `INSERT INTO group_account_stats_dirty (group_id, reason, updated_at) VALUES ('g1', 'w12b-write', '2026-09-17T12:05:00.000Z')`)
	businessSpec.arm("DELETE FROM group_account_stats_dirty")
	if _, err := refresh(store, 0); err == nil {
		t.Fatal("按组删除注入应报错")
	}
	businessSpec.disarm()
}

func TestW12bSvClientIPArms(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	store, statsSpec, businessSpec := w12bSvOpenStore(t)
	mustExec(t, ctx, store.business, `INSERT INTO system_settings (system_account_id, key, value_json) VALUES ('sys_admin', 'usageStatsTimezone', '"UTC"')`)
	insertUsageRecord(t, ctx, store, UsageStatsRecordRow{
		ID: "w12b-r1", SystemAccountID: "sys", ClientIP: strPtr("1.2.3.4"), Success: 1,
		CreatedAt: "2026-09-17T10:00:00.000Z",
	})

	aggregate := func() (int, error) { return store.AggregateClientIPStatsBatch(ctx, 10, now) }
	// 时区读取注入（business 句柄）。
	businessSpec.arm("system_settings")
	if _, err := aggregate(); err == nil {
		t.Fatal("时区注入应报错")
	}
	// BEGIN/COMMIT 注入（stats 句柄事务）。先解除 business 时区臂，
	// stats BEGIN 臂才会被真实消费。
	businessSpec.disarm()
	statsSpec.arm("BEGIN")
	if _, err := aggregate(); err == nil {
		t.Fatal("BEGIN 注入应报错")
	}
	statsSpec.arm("COMMIT")
	businessSpec.armGuard("system_settings_never")
	if _, err := aggregate(); err == nil {
		t.Fatal("COMMIT 注入应报错")
	}
	businessSpec.disarm()
	statsSpec.disarm()
	// job state 读取注入。
	statsSpec.arm("stats_job_state")
	if _, err := aggregate(); err == nil {
		t.Fatal("job state 注入应报错")
	}
	// usage_records 读取注入。
	statsSpec.arm("FROM usage_records")
	if _, err := aggregate(); err == nil {
		t.Fatal("usage_records 注入应报错")
	}
	// 正常聚合一次后排空队列。
	statsSpec.disarm()
	processed, err := aggregate()
	if err != nil || processed != 1 {
		t.Fatalf("正常聚合应成功: %d %v", processed, err)
	}
	// 空批次下的 ignored cursor 与 lag 查询注入。
	statsSpec.arm("traffic_source IN ")
	if _, err := aggregate(); err == nil {
		t.Fatal("ignored cursor 注入应报错")
	}
	statsSpec.arm("traffic_source NOT IN ")
	if _, err := aggregate(); err == nil {
		t.Fatal("lag 查询注入应报错")
	}
	// job state 更新注入（UPSERT 形态）。
	statsSpec.arm("INSERT INTO stats_job_state")
	if _, err := aggregate(); err == nil {
		t.Fatal("job state 更新注入应报错")
	}
	statsSpec.disarm()

	// 窗口刷新：脏表读取注入。
	if err := store.RefreshClientIPUsageRangeWindows(ctx, ClientIPRangeWindowRefreshOptions{Now: now}); err != nil {
		t.Fatalf("空脏表刷新应成功: %v", err)
	}
	statsSpec.arm("client_ip_range_window_dirty_ips")
	if err := store.RefreshClientIPUsageRangeWindows(ctx, ClientIPRangeWindowRefreshOptions{Now: now}); err == nil {
		t.Fatal("脏表注入应报错")
	}
	statsSpec.disarm()
	// 标记脏后按哈希刷新成功路径。
	mustExec(t, ctx, store.db, `INSERT INTO client_ip_registry (ip_hash, bucket_no, aggregate_ip_key, client_ip, ip_version, first_seen_at, last_seen_at, created_at, updated_at) VALUES ('w12b-hash', 1, '1.2.3.4', '1.2.3.4', 4, '2026-09-17T00:00:00.000Z', '2026-09-17T00:00:00.000Z', '2026-09-17T00:00:00.000Z', '2026-09-17T00:00:00.000Z')`)
	mustExec(t, ctx, store.db, `INSERT INTO client_ip_range_window_dirty_ips (ip_hash, first_dirty_at, generation, updated_at) VALUES ('w12b-hash', '2026-09-17T00:00:00.000Z', 1, '2026-09-17T00:00:00.000Z')`)
	mustExec(t, ctx, store.db, `INSERT INTO client_ip_account_range_window_dirty_ips (ip_hash, first_dirty_at, generation, updated_at) VALUES ('w12b-hash', '2026-09-17T00:00:00.000Z', 1, '2026-09-17T00:00:00.000Z')`)
	if err := store.RefreshClientIPUsageRangeWindows(ctx, ClientIPRangeWindowRefreshOptions{Now: now}); err != nil {
		t.Fatalf("按哈希刷新应成功: %v", err)
	}
	// 再标记一次并注入窗口 UPSERT。
	mustExec(t, ctx, store.db, `INSERT INTO client_ip_range_window_dirty_ips (ip_hash, first_dirty_at, generation, updated_at) VALUES ('w12b-hash', '2026-09-17T01:00:00.000Z', 2, '2026-09-17T01:00:00.000Z')`)
	mustExec(t, ctx, store.db, `INSERT INTO client_ip_account_range_window_dirty_ips (ip_hash, first_dirty_at, generation, updated_at) VALUES ('w12b-hash', '2026-09-17T01:00:00.000Z', 2, '2026-09-17T01:00:00.000Z')`)
	statsSpec.arm("ON CONFLICT")
	if err := store.RefreshClientIPUsageRangeWindows(ctx, ClientIPRangeWindowRefreshOptions{Now: now}); err == nil {
		t.Fatal("窗口 UPSERT 注入应报错")
	}
	statsSpec.disarm()

	// 一致性检查：样本查询与 hourly 聚合注入。
	seedConsistencyDaily(t, ctx, store, "sys", "system", "", "2026-09-16", 1, 0)
	statsSpec.arm("FROM usage_stats_daily")
	if _, err := store.CheckUsageStatsConsistency(ctx, UsageStatsConsistencyOptions{Now: now}); err == nil {
		t.Fatal("daily 样本注入应报错")
	}
	statsSpec.arm("usage_stats_hourly")
	if _, err := store.CheckUsageStatsConsistency(ctx, UsageStatsConsistencyOptions{Now: now}); err == nil {
		t.Fatal("hourly 聚合注入应报错")
	}
	statsSpec.disarm()
	// 无 hourly 行：daily 与空 hourly 有差异属预期；补齐一致 hourly 后再断言。
	seedConsistencyHourly(t, ctx, store, "sys", "system", "", "2026-09-16T00", 1, 0)
	issues, err := store.CheckUsageStatsConsistency(ctx, UsageStatsConsistencyOptions{Now: now})
	if err != nil {
		t.Fatalf("一致性检查应成功: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("一致数据不应有差异: %#v", issues)
	}
	mustExec(t, ctx, store.db, `UPDATE usage_stats_hourly SET request_count = 99 WHERE stat_hour = '2026-09-16T00'`)
	issues, err = store.CheckUsageStatsConsistency(ctx, UsageStatsConsistencyOptions{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) == 0 || issues[0].Metric != "request_count" {
		t.Fatalf("漂移应产出 request_count issue: %#v", issues)
	}

	// job 入口：nil IngestGate 必败 + 注入错误臂。
	if _, err := store.RunClientIPStatsAggregation(ctx, RunClientIPStatsAggregationOptions{IngestGate: nil}); err == nil {
		t.Fatal("缺 IngestGate 应报错")
	}
	statsSpec.arm("FROM usage_records")
	if _, err := store.RunClientIPStatsAggregation(ctx, RunClientIPStatsAggregationOptions{
		IngestGate: w12bStubGate{}, Clock: NewFixedClock(now),
	}); err == nil {
		t.Fatal("job 聚合注入应报错")
	}
	statsSpec.arm("FROM usage_stats_daily")
	if _, err := store.RunUsageStatsConsistencyCheck(ctx, now, w12bSvDiscardLogger()); err == nil {
		t.Fatal("job 一致性注入应报错")
	}
	statsSpec.disarm()
	// 一致性 job 正常臂（带告警日志）。
	mustExec(t, ctx, store.db, `DELETE FROM usage_stats_hourly`)
	if _, err := store.RunUsageStatsConsistencyCheck(ctx, now, w12bSvDiscardLogger()); err != nil {
		t.Fatalf("一致性 job 应成功: %v", err)
	}
	_ = businessSpec
}

type w12bStubGate struct{}

func (w12bStubGate) EnsureUsageRecordsIngested(context.Context) error { return nil }
