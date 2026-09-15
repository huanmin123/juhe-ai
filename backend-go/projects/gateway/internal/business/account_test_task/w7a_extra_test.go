package accounttesttask

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
)

var w7aErrBoom = errors.New("w7a boom")

// ---------------------------------------------------------------------------
// 脚本化 database/sql driver（w7a 前缀）：注入 begin/commit 失败、
// RowsAffected!=1、行迭代错误等单靠 SQLite 无法触达的防御臂。
// ---------------------------------------------------------------------------

type w7aStep struct {
	contains  string
	cols      []string
	row       []driver.Value
	noRows    bool
	rowsErr   error
	eofErr    error
	execErr   error
	affected  int64
	beginErr  error
	commitErr error
}

var w7aDriverSeq int64

type w7aScript struct{ steps []w7aStep }

func (s *w7aScript) next(kind, query string) (*w7aStep, error) {
	if len(s.steps) == 0 {
		return nil, fmt.Errorf("w7a script exhausted at %s: %s", kind, query)
	}
	step := &s.steps[0]
	s.steps = s.steps[1:]
	if step.contains != "" && !strings.Contains(query, step.contains) {
		return nil, fmt.Errorf("w7a step mismatch: want %q got %s: %s", step.contains, kind, query)
	}
	return step, nil
}

type w7aConn struct{ script *w7aScript }

func (c *w7aConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("w7a: prepare unsupported")
}
func (c *w7aConn) Close() error { return nil }

// BeginTx 满足 driver.ConnBeginTx，使 CheckContract 的 ReadOnly 事务可用。
func (c *w7aConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) { return c.Begin() }
func (c *w7aConn) Begin() (driver.Tx, error) {
	step, err := c.script.next("Begin", "BEGIN")
	if err != nil {
		return nil, err
	}
	if step.beginErr != nil {
		return nil, step.beginErr
	}
	return &w7aTx{script: c.script}, nil
}
func (c *w7aConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	step, err := c.script.next("Query", query)
	if err != nil {
		return nil, err
	}
	if step.rowsErr != nil {
		return nil, step.rowsErr
	}
	if step.noRows || (step.row == nil && step.cols == nil) {
		return &w7aRows{cols: []string{"x"}}, nil
	}
	cols := step.cols
	if cols == nil {
		cols = []string{"id"}
	}
	return &w7aRows{cols: cols, eofErr: step.eofErr, values: [][]driver.Value{step.row}}, nil
}
func (c *w7aConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	step, err := c.script.next("Exec", query)
	if err != nil {
		return nil, err
	}
	if step.execErr != nil {
		return nil, step.execErr
	}
	return w7aResult{affected: step.affected}, nil
}

type w7aTx struct{ script *w7aScript }

func (t *w7aTx) Commit() error {
	step, err := t.script.next("Commit", "COMMIT")
	if err != nil {
		return err
	}
	return step.commitErr
}
func (t *w7aTx) Rollback() error { return nil }

type w7aResult struct{ affected int64 }

func (r w7aResult) LastInsertId() (int64, error) { return 0, nil }
func (r w7aResult) RowsAffected() (int64, error) { return r.affected, nil }

type w7aRows struct {
	cols   []string
	values [][]driver.Value
	eofErr error
	index  int
}

func (r *w7aRows) Columns() []string { return r.cols }
func (r *w7aRows) Close() error      { return nil }
func (r *w7aRows) Next(dest []driver.Value) error {
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
func (r *w7aRows) Err() error { return nil }

type w7aDriver struct{ script *w7aScript }

func (d w7aDriver) Open(string) (driver.Conn, error) { return &w7aConn{script: d.script}, nil }

func w7aOpen(t *testing.T, steps []w7aStep) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("w7a-att-%d", atomic.AddInt64(&w7aDriverSeq, 1))
	sql.Register(name, w7aDriver{script: &w7aScript{steps: steps}})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func w7aScriptedStore(t *testing.T, steps []w7aStep) *Store {
	t.Helper()
	s, err := New(w7aOpen(t, steps), false, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, "gateway-test")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// ---------------------------------------------------------------------------
// 构造函数与方言
// ---------------------------------------------------------------------------

func TestW7AConstructorAndDialect(t *testing.T) {
	if _, err := New(nil, false, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, "gateway-test"); err == nil {
		t.Fatal("nil db must be rejected")
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := New(db, false, OwnerGate{}, " "); err == nil {
		t.Fatal("blank owner must be rejected")
	}
	s := &Store{postgres: true}
	if s.table("account_test_tasks") != "juhe_business.account_test_tasks" {
		t.Fatal(s.table("account_test_tasks"))
	}
	if got := s.bind("WHERE id=? AND status=?"); got != "WHERE id=$1 AND status=$2" {
		t.Fatal(got)
	}
	var nilStore *Store
	if err := nilStore.CheckContract(context.Background()); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("nil store contract=%v", err)
	}
}

func TestW7AGetReadsSeededTaskAndErrors(t *testing.T) {
	s, db := sqliteStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	defer db.Close()
	seedTask(t, db, "task-g", "queued")
	if _, err := db.Exec(`UPDATE account_test_tasks SET status_message='msg',result_json='{"ok":1}',cancel_requested=1,started_at='s',finished_at='f' WHERE id='task-g'`); err != nil {
		t.Fatal(err)
	}
	task, err := s.Get(context.Background(), "task-g")
	if err != nil {
		t.Fatal(err)
	}
	if task.Message != "msg" || task.ResultJSON != `{"ok":1}` || !task.CancelRequested || task.StartedAt != "s" || task.FinishedAt != "f" {
		t.Fatalf("task=%+v", task)
	}
	if _, err := s.Get(context.Background(), "missing"); err == nil {
		t.Fatal("missing task must error")
	}
}

func TestW7ADigestIsStable(t *testing.T) {
	first := digest(map[string]int{"a": 1})
	second := digest(map[string]int{"a": 1})
	if first == "" || first != second {
		t.Fatalf("digest=%s/%s", first, second)
	}
}

// ---------------------------------------------------------------------------
// Acquire 校验臂
// ---------------------------------------------------------------------------

func TestW7AAcquireValidationArms(t *testing.T) {
	s, db := sqliteStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	defer db.Close()
	ctx := context.Background()
	seedTask(t, db, "t-run", "running")
	if _, err := s.Acquire(ctx, "t-run", time.Minute); !errors.Is(err, ErrStaleCAS) {
		t.Fatalf("running=%v", err)
	}
	seedTask(t, db, "t-cancel", "queued")
	if _, err := db.Exec(`UPDATE account_test_tasks SET cancel_requested=1 WHERE id='t-cancel'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Acquire(ctx, "t-cancel", time.Minute); !errors.Is(err, ErrStaleCAS) {
		t.Fatalf("canceled=%v", err)
	}
	seedTask(t, db, "t-lease", "queued")
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour).Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO account_test_task_leases(task_id,owner_id,fence_token,lease_until,updated_at) VALUES('t-lease','other',7,?,'u')`, future); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Acquire(ctx, "t-lease", time.Minute); !errors.Is(err, ErrStaleCAS) {
		t.Fatalf("live lease=%v", err)
	}
	// 过期租约允许重取且 fence 递增。
	past := now.Add(-time.Hour).Format(time.RFC3339Nano)
	if _, err := db.Exec(`UPDATE account_test_task_leases SET lease_until=? WHERE task_id='t-lease'`, past); err != nil {
		t.Fatal(err)
	}
	lease, err := s.Acquire(ctx, "t-lease", 0)
	if err != nil {
		t.Fatal(err)
	}
	if lease.Fence != 8 || lease.OwnerID != "gateway-test" {
		t.Fatalf("lease=%+v", lease)
	}
	if !lease.Until.After(now.Add(4 * time.Minute)) {
		t.Fatalf("default lease until=%v", lease.Until)
	}
}

// ---------------------------------------------------------------------------
// finish 的成功/失败与租约校验臂
// ---------------------------------------------------------------------------

func TestW7AFailAndLeaseLoss(t *testing.T) {
	s, db := sqliteStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	defer db.Close()
	seedTask(t, db, "t-f", "queued")
	lease, err := s.Acquire(context.Background(), "t-f", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Fail(context.Background(), lease, Result{Success: false, Message: "bad", Data: map[string]int{"n": 1}}); err != nil {
		t.Fatal(err)
	}
	var status, errMsg, resultJSON string
	if err := db.QueryRow(`SELECT status,error_message,result_json FROM account_test_tasks WHERE id='t-f'`).Scan(&status, &errMsg, &resultJSON); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || errMsg != "bad" || resultJSON == "" {
		t.Fatalf("status=%s err=%s result=%s", status, errMsg, resultJSON)
	}
	// 完成后租约必须失效。
	if err := s.Fail(context.Background(), lease, Result{Success: false, Message: "bad"}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("lost lease=%v", err)
	}
	if err := s.Fail(context.Background(), Lease{}, Result{}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("missing lease=%v", err)
	}
	// 租约过期同样拒绝 finish。
	seedTask(t, db, "t-exp", "queued")
	expired, err := s.Acquire(context.Background(), "t-exp", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC) }
	if err := s.Complete(context.Background(), expired, Result{Success: true}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired lease=%v", err)
	}
}

// ---------------------------------------------------------------------------
// Cancel / UpdateMessage / Maintenance 剩余臂
// ---------------------------------------------------------------------------

func TestW7ACancelAndMessageArms(t *testing.T) {
	s, db := sqliteStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	defer db.Close()
	ctx := context.Background()
	seedTask(t, db, "t-m", "queued")
	// 空消息是 no-op。
	if err := s.UpdateMessage(ctx, "t-m", "   "); err != nil {
		t.Fatal(err)
	}
	lease, err := s.Acquire(ctx, "t-m", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateMessage(ctx, "t-m", "progress 50%"); err != nil {
		t.Fatal(err)
	}
	msg, err := s.CancelMessage(ctx, "t-m")
	if err != nil || msg != "progress 50%" {
		t.Fatalf("msg=%q err=%v", msg, err)
	}
	// 非运行态更新必须拒绝。
	if err := s.Cancel(ctx, "t-m", "用户取消"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateMessage(ctx, "t-m", "late"); !errors.Is(err, ErrStaleCAS) {
		t.Fatalf("late update=%v", err)
	}
	if err := s.Cancel(ctx, "t-m", "再次取消"); !errors.Is(err, ErrStaleCAS) {
		t.Fatalf("double cancel=%v", err)
	}
	_ = lease
	// 已完成任务取消必须拒绝。
	seedTask(t, db, "t-done", "success")
	if err := s.Cancel(ctx, "t-done", "x"); !errors.Is(err, ErrStaleCAS) {
		t.Fatalf("done cancel=%v", err)
	}
	if _, err := s.CancelMessage(ctx, "missing"); err == nil {
		t.Fatal("missing cancel message must error")
	}
	if _, err := s.IsCancelRequested(ctx, "missing"); err == nil {
		t.Fatal("missing cancel flag must error")
	}
}

func TestW7AMaintenanceArms(t *testing.T) {
	s, db := sqliteStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	defer db.Close()
	ctx := context.Background()
	if err := s.Maintenance(ctx, "rebuild", time.Minute); err == nil {
		t.Fatal("unknown action must fail")
	}
	seedTask(t, db, "t-old", "queued")
	old := time.Date(2029, 6, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	if _, err := db.Exec(`UPDATE account_test_tasks SET queued_at=? WHERE id='t-old'`, old); err != nil {
		t.Fatal(err)
	}
	seedTask(t, db, "t-new", "queued")
	if err := s.Maintenance(ctx, "sweep", 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	var oldStatus, newStatus string
	if err := db.QueryRow(`SELECT status FROM account_test_tasks WHERE id='t-old'`).Scan(&oldStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM account_test_tasks WHERE id='t-new'`).Scan(&newStatus); err != nil {
		t.Fatal(err)
	}
	if oldStatus != "failed" || newStatus != "queued" {
		t.Fatalf("sweep old=%s new=%s", oldStatus, newStatus)
	}
	// maxQueued=0 的 sweep 不做任何事。
	seedTask(t, db, "t-old2", "queued")
	if _, err := db.Exec(`UPDATE account_test_tasks SET queued_at=? WHERE id='t-old2'`, old); err != nil {
		t.Fatal(err)
	}
	if err := s.Maintenance(ctx, "sweep", 0); err != nil {
		t.Fatal(err)
	}
	var still string
	if err := db.QueryRow(`SELECT status FROM account_test_tasks WHERE id='t-old2'`).Scan(&still); err != nil || still != "queued" {
		t.Fatalf("no-op sweep status=%s err=%v", still, err)
	}
	// gate 关闭。
	s.gate = OwnerGate{Confirmed: true, SchemaReady: true}
	if err := s.Maintenance(ctx, "start", 0); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("gate=%v", err)
	}
}

// ---------------------------------------------------------------------------
// 脚本化失败臂
// ---------------------------------------------------------------------------

func w7aAcquireSteps(fenceRow []driver.Value) []w7aStep {
	return []w7aStep{
		{},
		{contains: "SELECT status,cancel_requested FROM account_test_tasks", cols: []string{"status", "cancel_requested"}, row: []driver.Value{"queued", false}},
		{contains: "SELECT fence_token,lease_until FROM account_test_task_leases", cols: []string{"fence_token", "lease_until"}, noRows: fenceRow == nil, row: fenceRow},
		{contains: "INSERT INTO account_test_task_leases", affected: 1},
		{contains: "UPDATE account_test_tasks SET status='running'", affected: 1},
	}
}

func w7aWithExecErr(steps []w7aStep, contains string) []w7aStep {
	for i := range steps {
		if steps[i].contains == contains {
			steps[i].execErr = w7aErrBoom
			return steps
		}
	}
	return steps
}

func TestW7AScriptedAcquireFailureArms(t *testing.T) {
	ctx := context.Background()
	casSteps := w7aAcquireSteps(nil)
	casSteps[len(casSteps)-1].affected = 0
	cases := []struct {
		name  string
		steps []w7aStep
	}{
		{"begin", []w7aStep{{beginErr: w7aErrBoom}}},
		{"task read", []w7aStep{{}, {contains: "SELECT status,cancel_requested", rowsErr: w7aErrBoom}}},
		{"lease read", []w7aStep{
			{},
			{contains: "SELECT status,cancel_requested", cols: []string{"status", "cancel_requested"}, row: []driver.Value{"queued", false}},
			{contains: "SELECT fence_token,lease_until", rowsErr: w7aErrBoom},
		}},
		{"lease insert", w7aWithExecErr(w7aAcquireSteps(nil), "INSERT INTO account_test_task_leases")},
		{"task update", w7aWithExecErr(w7aAcquireSteps(nil), "UPDATE account_test_tasks SET status='running'")},
		{"task update cas", casSteps},
		{"commit", append(w7aAcquireSteps(nil), w7aStep{commitErr: w7aErrBoom})},
	}
	for _, tc := range cases {
		s := w7aScriptedStore(t, tc.steps)
		if _, err := s.Acquire(ctx, "t", time.Minute); err == nil {
			t.Fatalf("acquire %s must fail", tc.name)
		}
	}
	// 已有租约行但读取正常（覆盖 errLease==nil && until.Valid 的过期分支在脚本下继续放行）。
	live := w7aScriptedStore(t, append(w7aAcquireSteps([]driver.Value{int64(3), "2020-01-01T00:00:00Z"}), w7aStep{commitErr: w7aErrBoom}))
	if _, err := live.Acquire(ctx, "t", time.Minute); err == nil {
		t.Fatal("expired scripted lease acquire+commit failure must surface")
	}
}

func TestW7AGateBlocksWriteOperations(t *testing.T) {
	s, db := sqliteStore(t, OwnerGate{Confirmed: true, SchemaReady: true})
	defer db.Close()
	ctx := context.Background()
	if err := s.Complete(ctx, Lease{}, Result{Success: true}); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("complete=%v", err)
	}
	if err := s.Fail(ctx, Lease{}, Result{}); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("fail=%v", err)
	}
	if err := s.Cancel(ctx, "t", "x"); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("cancel=%v", err)
	}
	if err := s.UpdateMessage(ctx, "t", "x"); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("update message=%v", err)
	}
	if err := s.Maintenance(ctx, "sweep", time.Minute); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("maintenance=%v", err)
	}
}

func TestW7AScriptedFinishFailureArms(t *testing.T) {
	ctx := context.Background()
	future := "2999-01-01T00:00:00Z"
	base := []w7aStep{
		{},
		{contains: "SELECT lease_until FROM account_test_task_leases", cols: []string{"lease_until"}, row: []driver.Value{future}},
	}
	cases := []struct {
		name  string
		steps []w7aStep
	}{
		{"begin", []w7aStep{{beginErr: w7aErrBoom}}},
		{"task update", append(append([]w7aStep{}, base...), w7aStep{contains: "UPDATE account_test_tasks SET status=?", execErr: w7aErrBoom})},
		{"cas canceled", append(append([]w7aStep{}, base...),
			w7aStep{contains: "UPDATE account_test_tasks SET status=?", affected: 0},
			w7aStep{contains: "SELECT cancel_requested FROM account_test_tasks", cols: []string{"cancel_requested"}, row: []driver.Value{true}})},
		{"cas raced", append(append([]w7aStep{}, base...),
			w7aStep{contains: "UPDATE account_test_tasks SET status=?", affected: 0},
			w7aStep{contains: "SELECT cancel_requested FROM account_test_tasks", cols: []string{"cancel_requested"}, row: []driver.Value{false}})},
		{"lease update", append(append([]w7aStep{}, base...),
			w7aStep{contains: "UPDATE account_test_tasks SET status=?", affected: 1},
			w7aStep{contains: "UPDATE account_test_task_leases SET lease_until=?", execErr: w7aErrBoom})},
		{"commit", append(append([]w7aStep{}, base...),
			w7aStep{contains: "UPDATE account_test_tasks SET status=?", affected: 1},
			w7aStep{contains: "UPDATE account_test_task_leases SET lease_until=?", affected: 1},
			w7aStep{commitErr: w7aErrBoom})},
	}
	for _, tc := range cases {
		s := w7aScriptedStore(t, tc.steps)
		if err := s.Complete(ctx, Lease{TaskID: "t", OwnerID: "gateway-test", Fence: 1}, Result{Success: true}); err == nil {
			t.Fatalf("finish %s must fail", tc.name)
		}
	}
}

func TestW7AScriptedSimpleStatementFailureArms(t *testing.T) {
	ctx := context.Background()
	// Cancel / UpdateMessage 直接 Exec 失败。
	for _, tc := range []struct {
		name string
		call func(s *Store) error
	}{
		{"cancel", func(s *Store) error { return s.Cancel(ctx, "t", "x") }},
		{"update message", func(s *Store) error { return s.UpdateMessage(ctx, "t", "x") }},
	} {
		s := w7aScriptedStore(t, []w7aStep{{contains: "UPDATE account_test_tasks", execErr: w7aErrBoom}})
		if err := tc.call(s); err == nil {
			t.Fatalf("%s exec failure must surface", tc.name)
		}
	}
	// Maintenance start 全链路失败臂。
	maintenanceBase := []w7aStep{
		{contains: "UPDATE account_test_task_leases SET lease_until=?", affected: 1},
		{contains: "SET status='canceled',status_message=CASE", affected: 1},
		{contains: "SET status='queued',status_message='后台 worker 重启后重新排队'", affected: 1},
	}
	for _, tc := range []struct {
		name   string
		action string
		steps  []w7aStep
	}{
		{"begin", "start", []w7aStep{{beginErr: w7aErrBoom}}},
		{"lease refresh", "start", append([]w7aStep{{}}, w7aStep{contains: "UPDATE account_test_task_leases", execErr: w7aErrBoom})},
		{"cancel running", "start", append(append([]w7aStep{{}}, maintenanceBase[0]), w7aStep{contains: "SET status='canceled',status_message=CASE", execErr: w7aErrBoom})},
		{"requeue", "start", append(append([]w7aStep{{}}, maintenanceBase[:2]...), w7aStep{contains: "SET status='queued'", execErr: w7aErrBoom})},
		{"commit", "start", append(append([]w7aStep{{}}, maintenanceBase...), w7aStep{commitErr: w7aErrBoom})},
		{"sweep", "sweep", []w7aStep{{contains: "SET status='failed',status_message='排队超时'", execErr: w7aErrBoom}}},
	} {
		s := w7aScriptedStore(t, tc.steps)
		if err := s.Maintenance(ctx, tc.action, time.Hour); err == nil {
			t.Fatalf("maintenance %s must fail", tc.name)
		}
	}
}

func TestW7AScriptedCheckContractFailureArms(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		steps []w7aStep
	}{
		{"begin", []w7aStep{{beginErr: w7aErrBoom}}},
		{"tasks table", []w7aStep{{}, {contains: "SELECT id,account_id,status", execErr: w7aErrBoom}}},
		{"lease table", []w7aStep{{}, {contains: "SELECT id,account_id,status", affected: 0}, {contains: "SELECT task_id,owner_id,fence_token", execErr: w7aErrBoom}}},
		{"commit", []w7aStep{{}, {contains: "SELECT id,account_id,status", affected: 0}, {contains: "SELECT task_id,owner_id,fence_token", affected: 0}, {commitErr: w7aErrBoom}}},
	}
	for _, tc := range cases {
		s := w7aScriptedStore(t, tc.steps)
		if err := s.CheckContract(ctx); err == nil {
			t.Fatalf("contract %s must fail", tc.name)
		}
	}
}

func TestW7AScriptedGetFailure(t *testing.T) {
	s := w7aScriptedStore(t, []w7aStep{{contains: "SELECT id,account_id,status", rowsErr: w7aErrBoom}})
	if _, err := s.Get(context.Background(), "t"); err == nil {
		t.Fatal("get failure must surface")
	}
}
