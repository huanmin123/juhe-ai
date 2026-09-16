package taskruns

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

// w12b_failinject_test.go 通过可回放的失败注入驱动（包装 modernc.org/sqlite）
// 覆盖 taskruns 存储层错误路径与零行 CAS 未命中路径；另以直接写坏时间文本
// 的行覆盖 scanTaskRun 的解析错误分支。
//
// 不可达清单（覆盖率登记）：
//   - ids.go newID 的 rand.Read 失败 panic：crypto/rand 在测试环境无注入点，
//     失败即进程级故障，无可控路径。
//   - store.go OpenStore SQLite 分支的 sql.Open 错误返回：驱动已注册时
//     sql.Open 不触发真实打开（懒连接），仅可能因驱动名未知失败，无可达路径。
//   - store.go TryAcquireScheduledLease PG advisory 查询执行中错误：需要
//     PG 会话级故障注入（事务内连接中断），SQLite 注入驱动不可达；PG 门控
//     测试覆盖 advisory 成功与 busy 两个快路径分支。
//
// 注入失败错误消息固定为 "w12b 注入失败"，不携带连接串或敏感数据。

const w12bFailDriverName = "w12b-taskruns-fail-sqlite"

var w12bFailDriverRegister sync.Once

// w12bFailSpec 描述一次注入：match 命中查询子串；once 只注入一次；
// zero=true 时静默返回 0 行影响（模拟 CAS 未命中），否则返回错误。
type w12bFailSpec struct {
	mu    sync.Mutex
	match string
	once  bool
	fired bool
	zero  bool
}

func (spec *w12bFailSpec) arm(match string) {
	spec.mu.Lock()
	defer spec.mu.Unlock()
	spec.match, spec.once, spec.fired, spec.zero = match, false, false, false
}

func (spec *w12bFailSpec) armOnce(match string) {
	spec.mu.Lock()
	defer spec.mu.Unlock()
	spec.match, spec.once, spec.fired, spec.zero = match, true, false, false
}

func (spec *w12bFailSpec) armZero(match string) {
	spec.mu.Lock()
	defer spec.mu.Unlock()
	spec.match, spec.once, spec.fired, spec.zero = match, false, false, true
}

func (spec *w12bFailSpec) disarm() {
	spec.mu.Lock()
	defer spec.mu.Unlock()
	spec.match = ""
}

func (spec *w12bFailSpec) hit(query string) (fail, zero bool) {
	spec.mu.Lock()
	defer spec.mu.Unlock()
	if spec.match == "" || !strings.Contains(query, spec.match) {
		return false, false
	}
	if spec.once && spec.fired {
		return false, false
	}
	spec.fired = true
	return !spec.zero, spec.zero
}

func w12bInjectedErr() error { return errors.New("w12b 注入失败") }

type w12bFailDriver struct {
	inner driver.Driver
	spec  *w12bFailSpec
}

func (d *w12bFailDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	w12bFailDriversMu.Lock()
	spec := w12bFailDrivers[w12bFailDriverName]
	w12bFailDriversMu.Unlock()
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
	return &w12bFailStmt{inner: stmt, spec: c.spec}, nil
}

func (c *w12bFailConn) Close() error { return c.inner.Close() }

func (c *w12bFailConn) Begin() (driver.Tx, error) {
	if fail, _ := c.spec.hit("BEGIN"); fail {
		return nil, w12bInjectedErr()
	}
	return c.inner.Begin()
}

func (c *w12bFailConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if fail, zero := c.spec.hit(query); fail {
		return nil, w12bInjectedErr()
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
	return stmt.Exec(w12bDriverValues(args))
}

func (c *w12bFailConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if fail, _ := c.spec.hit(query); fail {
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
	return stmt.Query(w12bDriverValues(args))
}

type w12bFailStmt struct {
	inner driver.Stmt
	spec  *w12bFailSpec
}

func (s *w12bFailStmt) Close() error { return s.inner.Close() }

func (s *w12bFailStmt) NumInput() int { return -1 }

func (s *w12bFailStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.inner.Exec(args)
}

func (s *w12bFailStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.inner.Query(args)
}

func w12bDriverValues(args []driver.NamedValue) []driver.Value {
	values := make([]driver.Value, len(args))
	for i, arg := range args {
		values[i] = arg.Value
	}
	return values
}

// w12bOpenFailStore 打开一个共享注入 spec 的 SQLite 测试存储。
func w12bOpenFailStore(t *testing.T) (*Store, *w12bFailSpec) {
	t.Helper()
	w12bFailDriverRegister.Do(func() {
		sql.Register(w12bFailDriverName, &w12bFailDriver{inner: &sqlite.Driver{}, spec: nil})
	})
	spec := &w12bFailSpec{}
	w12bFailDriversMu.Lock()
	w12bFailDrivers[w12bFailDriverName] = spec
	w12bFailDriversMu.Unlock()
	db, err := sql.Open(w12bFailDriverName, "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "w12b.db"))+"?_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		t.Fatalf("打开注入存储失败: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })
	store := NewStore(db, ModeSQLite)
	clock := NewFakeClock(time.Date(2026, 9, 17, 9, 0, 0, 0, time.UTC))
	store.SetClock(clock)
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("注入存储建表失败: %v", err)
	}
	return store, spec
}

var (
	w12bFailDriversMu sync.Mutex
	w12bFailDrivers   = map[string]*w12bFailSpec{}
)

// ---- OpenStore 配置错误路径 ----

func TestW12bOpenStoreSqlitePragmaFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "w12b-missing-dir", "taskruns.db")
	if _, err := OpenStore(StoreConfig{Mode: ModeSQLite, DatabasePath: missing}); err == nil {
		t.Fatal("不存在的目录应导致 PRAGMA 失败")
	}
}

func TestW12bOpenStorePgInvalidLimits(t *testing.T) {
	_, err := OpenStore(StoreConfig{
		Mode:                 ModePostgres,
		PostgresURL:          "postgres://w12b-invalid/w12b",
		PostgresMaxOpenConns: 4,
		PostgresMaxIdleConns: 5,
	})
	if err == nil {
		t.Fatal("maxIdle > maxOpen 应被 sql pool 校验拒绝")
	}
}

// ---- EnsureSchema 错误路径 ----

func TestW12bEnsureSchemaSqliteFailure(t *testing.T) {
	store, spec := w12bOpenFailStore(t)
	spec.arm("CREATE TABLE")
	if err := store.EnsureSchema(context.Background()); err == nil {
		t.Fatal("注入 CREATE TABLE 失败后 EnsureSchema 应报错")
	}
	spec.disarm()
}

func TestW12bEnsureSchemaPostgresArmOnSqlite(t *testing.T) {
	// PG 分支在 SQLite 句柄上执行 PG DDL 必然失败：覆盖 EnsureSchema PG 臂错误路径。
	db, err := sql.Open("sqlite", filepath.ToSlash(filepath.Join(t.TempDir(), "w12b-pg-arm.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewStore(db, ModePostgres)
	if err := store.EnsureSchema(context.Background()); err == nil {
		t.Fatal("PG DDL 在 SQLite 上应失败")
	}
}

// ---- CreateTaskRun 错误与降级路径 ----

func TestW12bCreateTaskRunPaths(t *testing.T) {
	ctx := context.Background()
	store, spec := w12bOpenFailStore(t)

	// params_json 序列化失败回退 "{}"。
	run, err := store.CreateTaskRun(ctx, TaskRunCreateInput{
		JobName: "w12b-j", JobType: "probe", WorkerRole: TemporaryMaintenanceWorkerRole,
		LeaseKey: "w12b-k", Params: map[string]any{"bad": func() {}},
	})
	if err != nil {
		t.Fatalf("Marshal 失败应回退空对象而不是报错: %v", err)
	}
	if len(run.Params) != 0 {
		t.Fatalf("不可序列化 params 应落为空对象: %#v", run.Params)
	}

	// INSERT 失败。
	spec.arm("INSERT INTO background_task_runs")
	if _, err := store.CreateTaskRun(ctx, TaskRunCreateInput{JobName: "w12b-j", WorkerRole: TemporaryMaintenanceWorkerRole}); err == nil {
		t.Fatal("INSERT 注入失败后 CreateTaskRun 应报错")
	}

	// 写入后读回失败。
	spec.armOnce("FROM background_task_runs")
	if _, err := store.CreateTaskRun(ctx, TaskRunCreateInput{JobName: "w12b-j", WorkerRole: TemporaryMaintenanceWorkerRole}); err == nil {
		t.Fatal("读回注入失败后 CreateTaskRun 应报错")
	}
	spec.disarm()
}

// ---- GetTaskRun：不存在 / 注入错误 / 坏时间文本 ----

func TestW12bGetTaskRunNoRowsAndScanError(t *testing.T) {
	ctx := context.Background()
	store, spec := w12bOpenFailStore(t)

	run, err := store.GetTaskRun(ctx, "w12b-missing")
	if err != nil || run != nil {
		t.Fatalf("不存在应返回 nil,nil: %v %v", run, err)
	}
	spec.arm("FROM background_task_runs")
	if _, err := store.GetTaskRun(ctx, "w12b-x"); err == nil {
		t.Fatal("SELECT 注入失败应报错")
	}
	spec.disarm()
}

func TestW12bGetTaskRunInvalidInstants(t *testing.T) {
	ctx := context.Background()
	store, _ := w12bOpenFailStore(t)

	cases := []struct {
		field string
	}{
		{"submitted_at"}, {"started_at"}, {"heartbeat_at"}, {"finished_at"}, {"created_at"}, {"updated_at"},
	}
	for i, tc := range cases {
		runID := "w12b-bad-" + tc.field
		columns := map[string]string{
			"submitted_at": "2026-09-17T09:00:00.000Z", "created_at": "2026-09-17T09:00:00.000Z",
			"updated_at": "2026-09-17T09:00:00.000Z", "started_at": "", "heartbeat_at": "", "finished_at": "",
		}
		columns[tc.field] = "w12b-bad-time"
		_, err := store.db.ExecContext(ctx, `
		INSERT INTO background_task_runs (
		  run_id, job_name, job_type, worker_role, status, lease_key, params_json, result_json,
		  submitted_at, created_at, updated_at, started_at, heartbeat_at, finished_at
		) VALUES (?, 'w12b-j', 'probe', 'w', 'queued', 'w12b-k', '{}', '{}', ?, ?, ?, ?, ?, ?)`,
			runID, columns["submitted_at"], columns["created_at"], columns["updated_at"],
			columns["started_at"], columns["heartbeat_at"], columns["finished_at"])
		if err != nil {
			t.Fatalf("插入坏时间行失败: %v", err)
		}
		if _, err := store.GetTaskRun(ctx, runID); err == nil {
			t.Fatalf("%s 坏时间文本应报错（case %d）", tc.field, i)
		}
	}
}

// ---- TryStart / Heartbeat / Finish 错误路径 ----

func TestW12bStartHeartbeatFinishErrorPaths(t *testing.T) {
	ctx := context.Background()
	store, spec := w12bOpenFailStore(t)
	clock := store.clock.(*FakeClock)

	setup := func(t *testing.T, owner string) TaskRun {
		t.Helper()
		run, err := store.CreateTaskRun(ctx, TaskRunCreateInput{JobName: "w12b-j", JobType: "probe", WorkerRole: TemporaryMaintenanceWorkerRole, LeaseKey: "w12b-k"})
		if err != nil {
			t.Fatal(err)
		}
		started, err := store.TryStartTaskRun(ctx, TaskRunStartInput{RunID: run.RunID, OwnerID: owner, LeaseUntil: clock.Now().Add(time.Minute)})
		if err != nil || !started {
			t.Fatalf("setup start: %v %v", started, err)
		}
		return run
	}

	// TryStartTaskRun UPDATE 失败。
	spec.arm("SET status = 'running'")
	if _, err := store.TryStartTaskRun(ctx, TaskRunStartInput{RunID: "w12b-any", OwnerID: "o", LeaseUntil: clock.Now().Add(time.Minute)}); err == nil {
		t.Fatal("start UPDATE 注入失败应报错")
	}

	// HeartbeatTaskRun UPDATE 失败。
	spec.disarm()
	run := setup(t, "w12b-worker")
	// 显式 now 参数分支。
	nowRef := clock.Now().Add(time.Second)
	if ok, err := store.HeartbeatTaskRun(ctx, run.RunID, "w12b-worker", clock.Now().Add(time.Minute), &nowRef); err != nil || !ok {
		t.Fatalf("显式 now 心跳应成功: %v %v", ok, err)
	}
	spec.arm("SET heartbeat_at")
	if _, err := store.HeartbeatTaskRun(ctx, run.RunID, "w12b-worker", clock.Now().Add(time.Minute), nil); err == nil {
		t.Fatal("heartbeat UPDATE 注入失败应报错")
	}

	// 心跳行更新成功但续租失败（租约表 UPDATE 注入）。
	spec.arm("UPDATE background_job_leases")
	if _, err := store.HeartbeatTaskRun(ctx, run.RunID, "w12b-worker", clock.Now().Add(time.Minute), nil); err == nil {
		t.Fatal("续租注入失败应使 HeartbeatTaskRun 报错")
	}

	// FinishTaskRun：不存在的 run 返回 (false,nil)；result 序列化失败回退；
	// UPDATE 注入失败报错。
	spec.disarm()
	changed, err := store.FinishTaskRun(ctx, TaskRunFinishInput{RunID: "w12b-missing", Status: StatusCompleted})
	if err != nil || changed {
		t.Fatalf("不存在 run 应 (false,nil): %v %v", changed, err)
	}
	if _, err := store.FinishTaskRun(ctx, TaskRunFinishInput{RunID: run.RunID, Status: StatusCompleted, Result: map[string]any{"bad": func() {}}}); err != nil {
		t.Fatalf("Marshal 失败应回退空结果: %v", err)
	}
	spec.arm("UPDATE background_task_runs")
	if _, err := store.FinishTaskRun(ctx, TaskRunFinishInput{RunID: run.RunID, Status: StatusCompleted}); err == nil {
		t.Fatal("finish UPDATE 注入失败应报错")
	}
	spec.disarm()
}

// ---- 租约错误路径与校验 ----

func TestW12bLeaseErrorPaths(t *testing.T) {
	ctx := context.Background()
	store, spec := w12bOpenFailStore(t)
	clock := store.clock.(*FakeClock)

	// AcquireLease INSERT 注入失败。
	spec.arm("INSERT INTO background_job_leases")
	if _, err := store.AcquireLease(ctx, LeaseAcquireInput{LeaseKey: "w12b-l1", JobName: "j", OwnerID: "o", LeaseUntil: clock.Now().Add(time.Minute)}); err == nil {
		t.Fatal("AcquireLease 注入失败应报错")
	}

	// ReleaseLease：空 owner no-op；DELETE 注入失败。
	spec.disarm()
	if err := store.ReleaseLease(ctx, "w12b-l1", ""); err != nil {
		t.Fatalf("空 owner 释放应为 no-op: %v", err)
	}
	if err := store.ReleaseLease(ctx, "w12b-l1", "o"); err != nil {
		t.Fatalf("正常删除不应报错: %v", err)
	}
	spec.arm("DELETE FROM background_job_leases")
	if err := store.ReleaseLease(ctx, "w12b-l1", "o"); err == nil {
		t.Fatal("DELETE 注入失败应报错")
	}

	// RenewScheduledLease 校验错误。
	spec.disarm()
	if _, err := store.RenewScheduledLease(ctx, LeaseIdentity{LeaseKey: "k", OwnerID: "o", FencingToken: 1}, time.Millisecond); err == nil {
		t.Fatal("TTL 过小应报错")
	}
	if _, err := store.RenewScheduledLease(ctx, LeaseIdentity{OwnerID: "o", FencingToken: 1}, time.Minute); err == nil {
		t.Fatal("空 leaseKey 应报错")
	}
	if _, err := store.RenewScheduledLease(ctx, LeaseIdentity{LeaseKey: "k", FencingToken: 1}, time.Minute); err == nil {
		t.Fatal("空 owner 应报错")
	}

	// TryAcquireScheduledLease 参数校验错误。
	bad := []ScheduledLeaseAcquireInput{
		{OwnerID: "o", TTL: time.Minute},                                // jobName 空
		{JobName: "j", ShardKey: "   ", OwnerID: "o", TTL: time.Minute}, // shardKey 空白
		{JobName: "j", LeaseKey: "  ", OwnerID: "o", TTL: time.Minute},  // leaseKey 空白
		{JobName: "j", OwnerID: "", TTL: time.Minute},                   // owner 空
		{JobName: "j", OwnerID: "o", TTL: time.Millisecond},             // TTL 过小
		{JobName: "j", OwnerID: "o", RunID: "  ", TTL: time.Minute},     // runId 空白
	}
	for i, input := range bad {
		if _, err := store.TryAcquireScheduledLease(ctx, input); err == nil {
			t.Fatalf("非法入参 case %d 应报错", i)
		}
	}

	// ScheduledLeaseAdvisoryKey 空 key。
	if _, err := ScheduledLeaseAdvisoryKey("  "); err == nil {
		t.Fatal("空 leaseKey 应报错")
	}

	// RETURNING 查询注入：acquire 行级失败与 renew 查询失败。
	spec.arm("RETURNING")
	if _, err := store.TryAcquireScheduledLease(ctx, ScheduledLeaseAcquireInput{JobName: "w12b-j", OwnerID: "o", TTL: time.Minute}); err == nil {
		t.Fatal("acquire RETURNING 注入失败应报错")
	}
	if _, err := store.RenewScheduledLease(ctx, LeaseIdentity{LeaseKey: "k", OwnerID: "o", FencingToken: 1}, time.Minute); err == nil {
		t.Fatal("renew RETURNING 注入失败应报错")
	}

	// AssertScheduledLease：SELECT 注入失败与非 NoRows 错误路径。
	spec.arm("SELECT lease_key")
	if err := store.AssertScheduledLease(ctx, LeaseFence{LeaseKey: "k", OwnerID: "o", FencingToken: 1}); err == nil {
		t.Fatal("assert SELECT 注入失败应报错")
	}

	// ReleaseScheduledLease UPDATE 注入失败。
	spec.arm("UPDATE background_job_leases")
	if _, err := store.ReleaseScheduledLease(ctx, LeaseIdentity{LeaseKey: "k", OwnerID: "o", FencingToken: 1}); err == nil {
		t.Fatal("release UPDATE 注入失败应报错")
	}
	spec.disarm()
}

func TestW12bScanLeaseIdentityBadUntil(t *testing.T) {
	// Store 各写路径 RETURNING 均返回数据库生成的合法时间文本，坏文本只能
	// 通过直接构造行覆盖 scanLeaseIdentity 的解析错误分支。
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	row := db.QueryRowContext(context.Background(), `SELECT 'w12b-k', 'o', 1, 'w12b-bad-time'`)
	identity, err := scanLeaseIdentity(row)
	if err == nil || identity != nil {
		t.Fatalf("坏 lease_until 应在解析时报错: %+v %v", identity, err)
	}
}

// ---- ReconcileStale 事务错误路径 ----

func TestW12bReconcileStaleTxErrors(t *testing.T) {
	ctx := context.Background()

	// BEGIN 注入失败。
	store, spec := w12bOpenFailStore(t)
	spec.arm("BEGIN")
	if _, err := store.ReconcileStale(ctx, TaskRunReconcileInput{}); err == nil {
		t.Fatal("BEGIN 注入失败应报错")
	}

	// 对账 UPDATE 注入失败：事务回滚。
	store2, spec2 := w12bOpenFailStore(t)
	spec2.arm("SET status = 'failed'")
	if _, err := store2.ReconcileStale(ctx, TaskRunReconcileInput{}); err == nil {
		t.Fatal("对账 UPDATE 注入失败应报错")
	}

	// 过期租约 DELETE 注入失败：前两步成功后第三步报错。
	store3, spec3 := w12bOpenFailStore(t)
	spec3.arm("DELETE FROM background_job_leases")
	if _, err := store3.ReconcileStale(ctx, TaskRunReconcileInput{}); err == nil {
		t.Fatal("DELETE 注入失败应报错")
	}

	// running 步 UPDATE 注入失败：COALESCE 仅出现在 running 对账 SQL，
	// queued 步先成功后第二步报错。
	store4, spec4 := w12bOpenFailStore(t)
	spec4.arm("COALESCE")
	if _, err := store4.ReconcileStale(ctx, TaskRunReconcileInput{}); err == nil {
		t.Fatal("running 步注入失败应报错")
	}
}

// ---- postgresBool []byte 分支 ----

func TestW12bPostgresBoolBytes(t *testing.T) {
	if !postgresBool([]byte("t")) || !postgresBool([]byte("true")) || postgresBool([]byte("f")) {
		t.Fatal("postgresBool []byte 分支不符")
	}
}
