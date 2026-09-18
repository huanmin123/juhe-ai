package accountkeystates

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
)

// w14f_aks_arms_test.go 用脚本化 database/sql 驱动对 accountkeystates 做
// 行级故障注入（Scan / rows.Err / Exec / RowsAffected / 标脏链路），并补齐
// PostgreSQL 分支（revalidateUpdateSQL、genericGuard、RecordFailure /
// RecordSuccess 的 PG upsert）与 changed==0 重试路径。
//
// 不可达语句登记（语句覆盖率口径下确实无法触达的防御守卫，保持与 Node
// 原文逐段对照，不做删除）：
//   - probe.go passiveJitterWindowMS：`case intervalMS < 60_000` 的 else 臂
//     （intervalMS < 60_000 ⇒ half < 30_000，else 永不成立）；
//   - probe.go passiveJitterWindowMS：`windowMS > half` 收敛臂（各档窗口值
//     恒 ≤ half，详见函数内算术）；
//   - probe.go passiveJitterWindowMS：`windowMS < 0` 收敛臂（windowMS 恒 ≥ 0）；
//   - probe.go passiveProbeRetryAt：`delay < 1` 收敛臂（delay ≥ intervalMS −
//     half ≥ 1）；
//   - summary.go LoadSummariesByAccountIds：`len(entries) < 2 → continue` 臂
//     （能走到该行必先通过 IsAccountAPIKeyPoolIsolationEnabled，其要求
//     len(entries) > 1）；
//   - details.go LoadAPIKeyRuntimeDetails：`row.viewAccountID != ids[0] →
//     continue` 臂与函数尾 `return []map[string]any{}, nil`（查询 WHERE
//     accounts.id IN (ids[0]) 保证 view_account_id == ids[0]，且循环体每条
//     分支都直接 return）；
//   - details.go LoadAPIKeyRuntimeDetails：`len(entries) < 2` 臂（同上，
//     isolation 门已保证 ≥ 2）。

// ---- 脚本化驱动 ----

type w14fFakeState struct {
	mu      sync.Mutex
	queryFn func(query string, args []driver.NamedValue) (driver.Rows, error)
	execFn  func(query string, args []driver.NamedValue) (driver.Result, error)
}

type w14fFakeConnector struct{ state *w14fFakeState }

func (c *w14fFakeConnector) Connect(context.Context) (driver.Conn, error) {
	return &w14fFakeConn{state: c.state}, nil
}
func (c *w14fFakeConnector) Driver() driver.Driver { return w14fFakeDriver{} }

type w14fFakeDriver struct{}

func (w14fFakeDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("w14f fake driver requires a connector")
}

type w14fFakeConn struct{ state *w14fFakeState }

func (c *w14fFakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("w14f fake driver handles queries directly")
}
func (c *w14fFakeConn) Close() error { return nil }
func (c *w14fFakeConn) Begin() (driver.Tx, error) {
	return nil, errors.New("w14f fake driver has no transactions")
}

func (c *w14fFakeConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.state.mu.Lock()
	execFn := c.state.execFn
	c.state.mu.Unlock()
	if execFn != nil {
		return execFn(query, args)
	}
	return w14fFakeResult{rowsAffected: 1}, nil
}

func (c *w14fFakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.state.mu.Lock()
	queryFn := c.state.queryFn
	c.state.mu.Unlock()
	if queryFn != nil {
		return queryFn(query, args)
	}
	return &w14fFakeRows{}, nil
}

type w14fFakeResult struct {
	rowsAffected int64
	raErr        error
}

func (r w14fFakeResult) LastInsertId() (int64, error) { return 0, errors.New("w14f: last_insert_id unsupported") }
func (r w14fFakeResult) RowsAffected() (int64, error) { return r.rowsAffected, r.raErr }

type w14fFakeRows struct {
	columns []string
	values  [][]driver.Value
	nextErr error
	i       int
}

func (r *w14fFakeRows) Columns() []string { return r.columns }
func (r *w14fFakeRows) Close() error      { return nil }
func (r *w14fFakeRows) Next(dest []driver.Value) error {
	if r.i < len(r.values) {
		copy(dest, r.values[r.i])
		r.i++
		return nil
	}
	if r.nextErr != nil {
		return r.nextErr
	}
	return io.EOF
}

func w14fOpenFakeDB(t *testing.T, state *w14fFakeState) *sql.DB {
	t.Helper()
	db := sql.OpenDB(&w14fFakeConnector{state: state})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// w14fFakeStore 直接以脚本化驱动组装 Store（postgres 切换方言分支）。
func w14fFakeStore(t *testing.T, postgres bool, state *w14fFakeState) *Store {
	t.Helper()
	return &Store{
		db:       w14fOpenFakeDB(t, state),
		postgres: postgres,
		secret:   testSecret,
		now:      func() time.Time { return testNow },
	}
}

func w14fErrRows(err error) driver.Rows { return &w14fFakeRows{nextErr: err} }

// w14fPoolEnvelope 返回带两把 Key 的合法凭据信封。
func w14fPoolEnvelope(t *testing.T) string {
	t.Helper()
	sealed, err := accounts.EncryptJSON(testSecret, map[string]any{"api_keys": []any{"w14f-key-a", "w14f-key-b"}})
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

// w14fRevalidateAccountRow 是 loadRevalidateAccountRow 的 8 列成功行。
func w14fRevalidateAccountRow(t *testing.T) []driver.Value {
	return []driver.Value{"openai", "openai", "v1", "api_key", "active", int64(1), int64(1), w14fPoolEnvelope(t)}
}

// w14fFingerprintValues 返回池内两把 Key 的指纹（按 entries 顺序）。
func w14fFingerprintValues(t *testing.T) []string {
	t.Helper()
	store := &Store{secret: testSecret}
	return []string{store.FingerprintAPIKey("w14f-key-a"), store.FingerprintAPIKey("w14f-key-b")}
}

// w14fRevalidateDispatch 按 SQL 片段分发 RevalidatePool 链路。
type w14fRevalidateScript struct {
	accountRows  [][]driver.Value
	gateRows     [][]driver.Value
	gateErr      error
	candidateRow bool
	candidateErr error
	affectedRows [][]driver.Value
	affectedErr  error
	affectedScan []driver.Value // 非 nil 时作为行内容返回（可注入坏值）
	groupRows    [][]driver.Value
	groupErr     error
	updateResult driver.Result
	updateErr    error
	dirtyExecErr error
	// retrySecondUpdate 时第二次 UPDATE 返回 1 行（changed==0 → 重试成功）。
	retrySecondUpdate bool
	updateCalls       int
}

func (s *w14fRevalidateScript) queryFn(query string, _ []driver.NamedValue) (driver.Rows, error) {
	switch {
	case strings.Contains(query, "SELECT provider_code"):
		return &w14fFakeRows{columns: make([]string, 8), values: s.accountRows}, nil
	case strings.Contains(query, "SELECT status, schedulable, config_revision FROM"):
		if s.gateErr != nil {
			return nil, s.gateErr
		}
		return &w14fFakeRows{columns: make([]string, 3), values: s.gateRows}, nil
	case strings.Contains(query, "SELECT 1 FROM"):
		if s.candidateErr != nil {
			return nil, s.candidateErr
		}
		if !s.candidateRow {
			return &w14fFakeRows{columns: []string{"one"}}, nil
		}
		return &w14fFakeRows{columns: []string{"one"}, values: [][]driver.Value{{int64(1)}}}, nil
	case strings.Contains(query, "SELECT id FROM"):
		if s.affectedErr != nil {
			return nil, s.affectedErr
		}
		if s.affectedScan != nil {
			return &w14fFakeRows{columns: []string{"id"}, values: [][]driver.Value{s.affectedScan}}, nil
		}
		return &w14fFakeRows{columns: []string{"id"}, values: s.affectedRows}, nil
	case strings.Contains(query, "SELECT DISTINCT group_id"):
		if s.groupErr != nil {
			return nil, s.groupErr
		}
		return &w14fFakeRows{columns: []string{"group_id"}, values: s.groupRows}, nil
	}
	return &w14fFakeRows{}, nil
}

func (s *w14fRevalidateScript) execFn(query string, _ []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(query, "INSERT INTO") {
		return w14fFakeResult{rowsAffected: 1}, s.dirtyExecErr
	}
	s.updateCalls++
	if s.retrySecondUpdate && s.updateCalls >= 2 {
		return w14fFakeResult{rowsAffected: 1}, nil
	}
	return s.updateResult, s.updateErr
}

// ---- Revalidate 错误臂与 changed==0 重试路径 ----

func TestW14fRevalidatePoolRetrySuccessPath(t *testing.T) {
	script := &w14fRevalidateScript{
		accountRows:       [][]driver.Value{w14fRevalidateAccountRow(t)},
		gateRows:          [][]driver.Value{{"active", int64(1), int64(1)}},
		candidateRow:      true,
		// 第一次 UPDATE 命中 0 行（changed==0 分支），重试命中 1 行。
		retrySecondUpdate: true,
		updateResult:      w14fFakeResult{rowsAffected: 0},
	}
	state := &w14fFakeState{queryFn: script.queryFn, execFn: script.execFn}
	store := w14fFakeStore(t, true, state)
	invalCalls := 0
	store.inval = func(reason string) { invalCalls++ }

	result, err := store.RevalidatePool(context.Background(), "w14f-acc", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Eligible || result.Changed != 1 || result.Reason != "" {
		t.Fatalf("retry result: %+v", result)
	}
	if invalCalls != 1 {
		t.Fatalf("inval calls: %d", invalCalls)
	}
}

func TestW14fRevalidatePoolGateRereadArms(t *testing.T) {
	build := func(t *testing.T, mutate func(*w14fRevalidateScript)) *Store {
		t.Helper()
		script := &w14fRevalidateScript{
			accountRows: [][]driver.Value{w14fRevalidateAccountRow(t)},
			gateRows:    [][]driver.Value{{"active", int64(1), int64(1)}},
			// 候选默认存在、重试 UPDATE 仍 0 行；各用例按需覆盖。
			candidateRow: true,
			updateResult: w14fFakeResult{rowsAffected: 0},
		}
		mutate(script)
		return w14fFakeStore(t, true, &w14fFakeState{queryFn: script.queryFn, execFn: script.execFn})
	}

	// 重读账户行不存在 → not_found。
	store := build(t, func(s *w14fRevalidateScript) { s.gateRows = nil })
	if result, err := store.RevalidatePool(context.Background(), "w14f-acc", 1); err != nil ||
		result.Reason != ReasonAccountNotFound {
		t.Fatalf("gate not found: %+v %v", result, err)
	}
	// 重读 revision 冲突。
	store = build(t, func(s *w14fRevalidateScript) {
		s.gateRows = [][]driver.Value{{"active", int64(1), int64(9)}}
	})
	if result, err := store.RevalidatePool(context.Background(), "w14f-acc", 1); err != nil ||
		result.Reason != ReasonConfigRevisionConflict {
		t.Fatalf("gate conflict: %+v %v", result, err)
	}
	// 重读 status 非 active。
	store = build(t, func(s *w14fRevalidateScript) {
		s.gateRows = [][]driver.Value{{"rate_limited", int64(1), int64(1)}}
	})
	if result, err := store.RevalidatePool(context.Background(), "w14f-acc", 1); err != nil ||
		result.Reason != ReasonAccountNotActive {
		t.Fatalf("gate not active: %+v %v", result, err)
	}
	// 重读 schedulable != 1。
	store = build(t, func(s *w14fRevalidateScript) {
		s.gateRows = [][]driver.Value{{"active", int64(0), int64(1)}}
	})
	if result, err := store.RevalidatePool(context.Background(), "w14f-acc", 1); err != nil ||
		result.Reason != ReasonAccountUnschedulable {
		t.Fatalf("gate unschedulable: %+v %v", result, err)
	}
	// 重读查询失败。
	store = build(t, func(s *w14fRevalidateScript) { s.gateErr = errors.New("w14f gate read failure") })
	if _, err := store.RevalidatePool(context.Background(), "w14f-acc", 1); err == nil {
		t.Fatalf("gate read failure must error")
	}
	// 候选探测失败。
	store = build(t, func(s *w14fRevalidateScript) { s.candidateErr = errors.New("w14f candidate failure") })
	if _, err := store.RevalidatePool(context.Background(), "w14f-acc", 1); err == nil {
		t.Fatalf("candidate failure must error")
	}
	// 候选存在但重试 UPDATE 仍 0 行 → no_revalidatable_key。
	store = build(t, func(s *w14fRevalidateScript) { s.candidateRow = true })
	if result, err := store.RevalidatePool(context.Background(), "w14f-acc", 1); err != nil ||
		result.Reason != ReasonNoRevalidatableKey {
		t.Fatalf("retry zero rows: %+v %v", result, err)
	}
}

func TestW14fRevalidatePoolErrorArms(t *testing.T) {
	build := func(t *testing.T, mutate func(*w14fRevalidateScript)) *Store {
		t.Helper()
		script := &w14fRevalidateScript{
			accountRows: [][]driver.Value{w14fRevalidateAccountRow(t)},
		}
		mutate(script)
		return w14fFakeStore(t, true, &w14fFakeState{queryFn: script.queryFn, execFn: script.execFn})
	}

	// 首次 UPDATE 失败。
	store := build(t, func(s *w14fRevalidateScript) { s.updateErr = errors.New("w14f update failure") })
	if _, err := store.RevalidatePool(context.Background(), "w14f-acc", 1); err == nil {
		t.Fatalf("update failure must error")
	}
	// RowsAffected 失败。
	store = build(t, func(s *w14fRevalidateScript) {
		s.updateResult = w14fFakeResult{rowsAffected: 1, raErr: errors.New("w14f rows affected failure")}
	})
	if _, err := store.RevalidatePool(context.Background(), "w14f-acc", 1); err == nil {
		t.Fatalf("rows affected failure must error")
	}
	// changed>0 后标脏链路失败（affected 查询失败）。
	store = build(t, func(s *w14fRevalidateScript) {
		s.updateResult = w14fFakeResult{rowsAffected: 1}
		s.affectedErr = errors.New("w14f affected query failure")
	})
	if _, err := store.RevalidatePool(context.Background(), "w14f-acc", 1); err == nil {
		t.Fatalf("affected query failure must error")
	}
	// affected 行 Scan 失败（非法 driver 值）。
	store = build(t, func(s *w14fRevalidateScript) {
		s.updateResult = w14fFakeResult{rowsAffected: 1}
		s.affectedScan = []driver.Value{struct{}{}}
	})
	if _, err := store.RevalidatePool(context.Background(), "w14f-acc", 1); err == nil {
		t.Fatalf("affected scan failure must error")
	}
	// affected 为空 → 回落来源账户；group 查询失败。
	store = build(t, func(s *w14fRevalidateScript) {
		s.updateResult = w14fFakeResult{rowsAffected: 1}
		s.groupErr = errors.New("w14f group query failure")
	})
	if _, err := store.RevalidatePool(context.Background(), "w14f-acc", 1); err == nil {
		t.Fatalf("group query failure must error")
	}
	// group 行 Scan 失败。
	store = build(t, func(s *w14fRevalidateScript) {
		s.updateResult = w14fFakeResult{rowsAffected: 1}
		s.groupRows = [][]driver.Value{{struct{}{}}}
	})
	if _, err := store.RevalidatePool(context.Background(), "w14f-acc", 1); err == nil {
		t.Fatalf("group scan failure must error")
	}
	// group 行迭代失败（rows.Err）。
	script := &w14fRevalidateScript{
		accountRows:  [][]driver.Value{w14fRevalidateAccountRow(t)},
		updateResult: w14fFakeResult{rowsAffected: 1},
	}
	state := &w14fFakeState{execFn: script.execFn, queryFn: func(query string, args []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(query, "SELECT DISTINCT group_id") {
			return w14fErrRows(errors.New("w14f group iterate failure")), nil
		}
		return script.queryFn(query, args)
	}}
	store = w14fFakeStore(t, true, state)
	if _, err := store.RevalidatePool(context.Background(), "w14f-acc", 1); err == nil {
		t.Fatalf("group iterate failure must error")
	}
	// 脏标记 upsert 失败。
	script2 := &w14fRevalidateScript{
		accountRows:  [][]driver.Value{w14fRevalidateAccountRow(t)},
		updateResult: w14fFakeResult{rowsAffected: 1},
		groupRows:    [][]driver.Value{{"w14f-group"}},
		dirtyExecErr: errors.New("w14f dirty upsert failure"),
	}
	store = w14fFakeStore(t, true, &w14fFakeState{queryFn: script2.queryFn, execFn: script2.execFn})
	if _, err := store.RevalidatePool(context.Background(), "w14f-acc", 1); err == nil {
		t.Fatalf("dirty upsert failure must error")
	}
}

func TestW14fRevalidateDecryptFailureFallsBackToNotSupported(t *testing.T) {
	script := &w14fRevalidateScript{
		accountRows: [][]driver.Value{{"openai", "openai", "v1", "api_key", "active", int64(1), int64(1), "w14f-not-an-envelope"}},
	}
	store := w14fFakeStore(t, true, &w14fFakeState{queryFn: script.queryFn, execFn: script.execFn})
	if result, err := store.RevalidatePool(context.Background(), "w14f-acc", 1); err != nil ||
		result.Reason != ReasonNotSupported {
		t.Fatalf("decrypt fallback: %+v %v", result, err)
	}
	// PG 方言的 UPDATE 渲染（AS states 别名）。
	if !strings.Contains(store.revalidateUpdateSQL(2), "AS states") {
		t.Fatalf("postgres revalidate SQL missing AS states alias")
	}
	if strings.Contains((&Store{postgres: false}).revalidateUpdateSQL(2), "AS states") {
		t.Fatalf("sqlite revalidate SQL must not alias")
	}
}

// ---- ClaimDueForProbe 错误臂 ----

func w14fClaimDispatch(t *testing.T, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "states.next_probe_at IS NOT NULL") {
		return &w14fFakeRows{columns: make([]string, 17), values: [][]driver.Value{
			{"w14f-acc", w14fFingerprintValues(t)[0], int64(0), "rate_limited", "2026-09-04T09:00:00.000Z", "2026-09-04T09:00:00.000Z",
				nil, nil, "w14f-account", "openai", "openai", "v1", "api_key", w14fPoolEnvelope(t), int64(1), nil, nil},
		}}, nil
	}
	_ = args
	return &w14fFakeRows{}, nil
}

func TestW14fClaimDueForProbeErrorArms(t *testing.T) {
	// 行 Scan 失败（列数不足）。
	store := w14fFakeStore(t, true, &w14fFakeState{queryFn: func(query string, args []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(query, "states.next_probe_at IS NOT NULL") {
			return &w14fFakeRows{columns: make([]string, 3), values: [][]driver.Value{{"a", "b", "c"}}}, nil
		}
		return &w14fFakeRows{}, nil
	}})
	if _, err := store.ClaimDueForProbe(context.Background(), 1); err == nil {
		t.Fatalf("claim scan failure must error")
	}
	// 行迭代失败（rows.Err）。
	store = w14fFakeStore(t, true, &w14fFakeState{queryFn: func(query string, args []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(query, "states.next_probe_at IS NOT NULL") {
			return w14fErrRows(errors.New("w14f claim iterate failure")), nil
		}
		return &w14fFakeRows{}, nil
	}})
	if _, err := store.ClaimDueForProbe(context.Background(), 1); err == nil {
		t.Fatalf("claim iterate failure must error")
	}
	// claim UPDATE 失败。
	store = w14fFakeStore(t, true, &w14fFakeState{
		queryFn: func(query string, args []driver.NamedValue) (driver.Rows, error) {
			return w14fClaimDispatch(t, query, args)
		},
		execFn: func(string, []driver.NamedValue) (driver.Result, error) {
			return nil, errors.New("w14f claim exec failure")
		},
	})
	if _, err := store.ClaimDueForProbe(context.Background(), 1); err == nil {
		t.Fatalf("claim exec failure must error")
	}
	// claim RowsAffected 失败。
	store = w14fFakeStore(t, true, &w14fFakeState{
		queryFn: func(query string, args []driver.NamedValue) (driver.Rows, error) {
			return w14fClaimDispatch(t, query, args)
		},
		execFn: func(string, []driver.NamedValue) (driver.Result, error) {
			return w14fFakeResult{rowsAffected: 1, raErr: errors.New("w14f claim rows affected failure")}, nil
		},
	})
	if _, err := store.ClaimDueForProbe(context.Background(), 1); err == nil {
		t.Fatalf("claim rows affected failure must error")
	}
	// claim 竞争失败（0 行）→ 候选被丢弃。
	store = w14fFakeStore(t, true, &w14fFakeState{
		queryFn: func(query string, args []driver.NamedValue) (driver.Rows, error) {
			return w14fClaimDispatch(t, query, args)
		},
		execFn: func(string, []driver.NamedValue) (driver.Result, error) {
			return w14fFakeResult{rowsAffected: 0}, nil
		},
	})
	claimed, err := store.ClaimDueForProbe(context.Background(), 1)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("claim race loss: %v %v", claimed, err)
	}
}

// ---- RecordFailure / RecordSuccess / DeferProbe 分支 ----

// w14fMutationStore 组装带标脏链路的 mutation 脚本：existing 行读取 + exec。
func w14fMutationStore(t *testing.T, postgres bool, existingRows [][]driver.Value, exec func(string) (driver.Result, error)) (*Store, *w14fFakeState) {
	t.Helper()
	state := &w14fFakeState{
		queryFn: func(query string, args []driver.NamedValue) (driver.Rows, error) {
			switch {
			case strings.Contains(query, "SELECT status, recovery_started_at, last_error_code, probe_backoff_seconds"):
				return &w14fFakeRows{columns: make([]string, 4), values: existingRows}, nil
			case strings.Contains(query, "SELECT id FROM"):
				return &w14fFakeRows{columns: []string{"id"}, values: [][]driver.Value{{"w14f-acc"}}}, nil
			case strings.Contains(query, "SELECT DISTINCT group_id"):
				return &w14fFakeRows{columns: []string{"group_id"}}, nil
			}
			_ = args
			return &w14fFakeRows{}, nil
		},
		execFn: func(query string, args []driver.NamedValue) (driver.Result, error) {
			return exec(query)
		},
	}
	return w14fFakeStore(t, postgres, state), state
}

func w14fMutationTarget(t *testing.T) TargetInput {
	t.Helper()
	return TargetInput{
		AccountID:                 "w14f-acc",
		SystemAccountID:           "w14f-sys",
		SelectedAPIKeyFingerprint: w14fFingerprintValues(t)[1],
		HasSelectedAPIKeyIndex:    true,
		SelectedAPIKeyIndex:       2,
		ProviderCode:              "openai",
		ProtocolCode:              "openai",
		ProtocolVersion:           "v1",
		AccountType:               "api_key",
		APIKey:                    "w14f-key-b",
		APIKeys:                   []string{"w14f-key-a", "w14f-key-b"},
	}
}

func TestW14fRecordFailurePGUpsertArms(t *testing.T) {
	// PG 无 fence upsert 成功（覆盖 PG INSERT + finishMutation 链路）。
	store, _ := w14fMutationStore(t, true, nil, func(string) (driver.Result, error) {
		return w14fFakeResult{rowsAffected: 1}, nil
	})
	result, err := store.RecordFailure(context.Background(), FailureInput{Account: w14fMutationTarget(t), Status: "rate_limited"})
	if err != nil || !result.Changed {
		t.Fatalf("pg upsert failure: %+v %v", result, err)
	}
	// PG 无 fence upsert 失败。
	store, _ = w14fMutationStore(t, true, nil, func(string) (driver.Result, error) {
		return nil, errors.New("w14f pg upsert failure")
	})
	if _, err := store.RecordFailure(context.Background(), FailureInput{Account: w14fMutationTarget(t)}); err == nil {
		t.Fatalf("pg upsert failure must error")
	}
	// PG 带 fence：genericGuard PG 方言 + fence UPDATE exec 失败（fence 需要已
	// 存在的运行态行，否则在 exec 前按 stale_probe_state 跳过）。
	existing := [][]driver.Value{{"rate_limited", "", "", int64(3)}}
	store, _ = w14fMutationStore(t, true, existing, func(string) (driver.Result, error) {
		return nil, errors.New("w14f fenced update failure")
	})
	if _, err := store.RecordFailure(context.Background(), FailureInput{
		Account:  w14fMutationTarget(t),
		Expected: ExpectedProbeState{Status: "rate_limited"},
	}); err == nil {
		t.Fatalf("fenced update failure must error")
	}
	// genericGuard 两种方言渲染。
	if !strings.Contains(store.genericGuard(), "current_state.") {
		t.Fatalf("postgres generic guard missing alias prefix")
	}
	if strings.Contains((&Store{postgres: false}).genericGuard(), "current_state.") {
		t.Fatalf("sqlite generic guard must not alias")
	}
}

func TestW14fRecordFailureSQLiteArms(t *testing.T) {
	// SQLite 已有行：UPDATE 失败。
	store, _ := w14fMutationStore(t, false, [][]driver.Value{{"rate_limited", "", "", int64(3)}},
		func(string) (driver.Result, error) { return nil, errors.New("w14f sqlite update failure") })
	if _, err := store.RecordFailure(context.Background(), FailureInput{Account: w14fMutationTarget(t)}); err == nil {
		t.Fatalf("sqlite update failure must error")
	}
	// SQLite 无行：INSERT 失败。
	store, _ = w14fMutationStore(t, false, nil,
		func(string) (driver.Result, error) { return nil, errors.New("w14f sqlite insert failure") })
	if _, err := store.RecordFailure(context.Background(), FailureInput{Account: w14fMutationTarget(t)}); err == nil {
		t.Fatalf("sqlite insert failure must error")
	}
}

func TestW14fRecordSuccessArms(t *testing.T) {
	target := w14fMutationTarget(t)
	// 非法 fence → invalid_expected_probe_at（覆盖 skip 臂）。
	store, _ := w14fMutationStore(t, true, nil, func(string) (driver.Result, error) {
		t.Fatalf("must not execute statements")
		return nil, nil
	})
	if result, err := store.RecordSuccess(context.Background(), target, SuccessInput{
		Expected: ExpectedProbeState{NextProbeAt: "not-a-time"},
	}); err != nil || result.SkippedReason != "invalid_expected_probe_at" {
		t.Fatalf("invalid fence: %+v %v", result, err)
	}
	// PG 无 fence upsert 成功。
	store, _ = w14fMutationStore(t, true, nil, func(string) (driver.Result, error) {
		return w14fFakeResult{rowsAffected: 1}, nil
	})
	if result, err := store.RecordSuccess(context.Background(), target, SuccessInput{}); err != nil || !result.Changed {
		t.Fatalf("pg success: %+v %v", result, err)
	}
	// PG 无 fence upsert 失败。
	store, _ = w14fMutationStore(t, true, nil, func(string) (driver.Result, error) {
		return nil, errors.New("w14f pg success failure")
	})
	if _, err := store.RecordSuccess(context.Background(), target, SuccessInput{}); err == nil {
		t.Fatalf("pg success failure must error")
	}
	// SQLite 无 fence upsert 失败。
	store, _ = w14fMutationStore(t, false, nil, func(string) (driver.Result, error) {
		return nil, errors.New("w14f sqlite success failure")
	})
	if _, err := store.RecordSuccess(context.Background(), target, SuccessInput{}); err == nil {
		t.Fatalf("sqlite success failure must error")
	}
}

func TestW14fDeferAndFinishMutationArms(t *testing.T) {
	target := w14fMutationTarget(t)
	// DeferProbe：RowsAffected 失败。
	store, _ := w14fMutationStore(t, false, nil, func(string) (driver.Result, error) {
		return w14fFakeResult{rowsAffected: 1, raErr: errors.New("w14f defer rows affected failure")}, nil
	})
	if _, err := store.DeferProbe(context.Background(), target, DeferInput{
		ExpectedNextProbeAt: "2026-09-04T10:00:00.000Z",
		DelaySeconds:        30,
	}); err == nil {
		t.Fatalf("defer rows affected failure must error")
	}
	// finishMutation：RowsAffected 失败（SQLite success 链路）。
	store, _ = w14fMutationStore(t, false, nil, func(string) (driver.Result, error) {
		return w14fFakeResult{rowsAffected: 0, raErr: errors.New("w14f finish rows affected failure")}, nil
	})
	if _, err := store.RecordSuccess(context.Background(), target, SuccessInput{}); err == nil {
		t.Fatalf("finish rows affected failure must error")
	}
	// finishMutation：标脏失败（affected 查询失败）。
	store, _ = w14fMutationStore(t, false, nil, func(string) (driver.Result, error) {
		return w14fFakeResult{rowsAffected: 1}, nil
	})
	state := &w14fFakeState{
		queryFn: func(query string, args []driver.NamedValue) (driver.Rows, error) {
			if strings.Contains(query, "SELECT id FROM") {
				return nil, errors.New("w14f mark failure")
			}
			_ = args
			return &w14fFakeRows{}, nil
		},
		execFn: func(string, []driver.NamedValue) (driver.Result, error) {
			return w14fFakeResult{rowsAffected: 1}, nil
		},
	}
	store = w14fFakeStore(t, false, state)
	if _, err := store.RecordSuccess(context.Background(), target, SuccessInput{}); err == nil {
		t.Fatalf("mark failure must error")
	}
}

// ---- jitter offset 零偏移臂 ----

func TestW14fPassiveJitterOffsetZeroArm(t *testing.T) {
	// windowMS=1 时 floor(u*3)-1 可取 0，命中 `offset == 0 → offset = 1` 臂后
	// 返回值与自然 1 不可区分；这里以大样本保证随机采样覆盖该内部臂，并约束
	// 输出值域（采样后 offset ∈ {-1, 1}）。
	for i := 0; i < 10000; i++ {
		offset := passiveJitterOffsetMS(1)
		if offset != -1 && offset != 1 {
			t.Fatalf("unexpected offset sample: %d", offset)
		}
	}
}

// ---- summaries / details 错误臂 ----

func TestW14fSummaryErrorArms(t *testing.T) {
	// 详情行查询失败。
	store, _ := func() (*Store, *w14fFakeState) {
		st := &w14fFakeState{queryFn: func(query string, args []driver.NamedValue) (driver.Rows, error) {
			switch {
			case strings.Contains(query, "AS view_account_id"):
				return &w14fFakeRows{columns: make([]string, 7), values: [][]driver.Value{{
					"w14f-acc", "w14f-acc", "openai", "openai", "v1", "api_key", w14fPoolEnvelope(t),
				}}}, nil
			case strings.Contains(query, "SELECT account_id, key_fingerprint"):
				return nil, errors.New("w14f detail query failure")
			}
			_ = args
			return &w14fFakeRows{}, nil
		}}
		return w14fFakeStore(t, true, st), st
	}()
	if _, err := store.LoadSummariesByAccountIds(context.Background(), []string{"w14f-acc"}); err == nil {
		t.Fatalf("detail query failure must error")
	}

	build := func(t *testing.T, sourceRows func() (driver.Rows, error), detailRows func() (driver.Rows, error)) *Store {
		t.Helper()
		return w14fFakeStore(t, true, &w14fFakeState{queryFn: func(query string, args []driver.NamedValue) (driver.Rows, error) {
			if strings.Contains(query, "AS view_account_id") {
				return sourceRows()
			}
			if strings.Contains(query, "SELECT account_id, key_fingerprint") {
				return detailRows()
			}
			_ = args
			return &w14fFakeRows{}, nil
		}})
	}
	goodSource := func() (driver.Rows, error) {
		return &w14fFakeRows{columns: make([]string, 7), values: [][]driver.Value{{
			"w14f-acc", "w14f-acc", "openai", "openai", "v1", "api_key", w14fPoolEnvelope(t),
		}}}, nil
	}
	emptyDetail := func() (driver.Rows, error) { return &w14fFakeRows{columns: make([]string, 15)}, nil }

	// 源行 Scan 失败。
	store = build(t, func() (driver.Rows, error) {
		return &w14fFakeRows{columns: make([]string, 7), values: [][]driver.Value{{struct{}{}, nil, nil, nil, nil, nil, nil}}}, nil
	}, emptyDetail)
	if _, err := store.LoadSummariesByAccountIds(context.Background(), []string{"w14f-acc"}); err == nil {
		t.Fatalf("source scan failure must error")
	}
	// 源行过滤（view/source/credentials 空 → continue）。
	store = build(t, func() (driver.Rows, error) {
		return &w14fFakeRows{columns: make([]string, 7), values: [][]driver.Value{{"", "", "", "", "", "", ""}}}, nil
	}, emptyDetail)
	if summaries, err := store.LoadSummariesByAccountIds(context.Background(), []string{"w14f-acc"}); err != nil || len(summaries) != 0 {
		t.Fatalf("empty source row: %v %v", summaries, err)
	}
	// 源行迭代失败。
	store = build(t, func() (driver.Rows, error) {
		return w14fErrRows(errors.New("w14f source iterate failure")), nil
	}, emptyDetail)
	if _, err := store.LoadSummariesByAccountIds(context.Background(), []string{"w14f-acc"}); err == nil {
		t.Fatalf("source iterate failure must error")
	}
	// 详情行 Scan 失败。
	store = build(t, goodSource, func() (driver.Rows, error) {
		return &w14fFakeRows{columns: make([]string, 15), values: [][]driver.Value{{struct{}{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil}}}, nil
	})
	if _, err := store.LoadSummariesByAccountIds(context.Background(), []string{"w14f-acc"}); err == nil {
		t.Fatalf("detail scan failure must error")
	}
	// 详情行迭代失败。
	store = build(t, goodSource, func() (driver.Rows, error) {
		return w14fErrRows(errors.New("w14f detail iterate failure")), nil
	})
	if _, err := store.LoadSummariesByAccountIds(context.Background(), []string{"w14f-acc"}); err == nil {
		t.Fatalf("detail iterate failure must error")
	}
	// AllUnavailable：源查询失败。
	store = build(t, func() (driver.Rows, error) { return nil, errors.New("w14f all unavailable failure") }, emptyDetail)
	if _, err := store.AllUnavailable(context.Background(), "w14f-acc"); err == nil {
		t.Fatalf("all unavailable failure must error")
	}
}

func TestW14fSummaryDecryptFailureSkipsRow(t *testing.T) {
	h := openTestDB(t)
	h.seedSchema(t)
	// 池账户但凭据信封损坏 → 解密失败 continue（渲染空摘要）。
	h.exec(t, `INSERT INTO accounts (id, system_account_id, name, type, status, schedulable,
      provider_code, protocol_code, protocol_version, config_revision, credentials_encrypted)
    VALUES ('w14f-bad', 'sys-owner', 'w14f-bad', 'api_key', 'active', 1, 'openai', 'openai', 'v1', 1, 'w14f-broken-envelope')`)
	h.exec(t, `INSERT INTO group_accounts (group_id, account_id) VALUES ('w14f-group', 'w14f-bad')`)
	summaries, err := h.store.LoadSummariesByAccountIds(context.Background(), []string{"w14f-bad"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := summaries["w14f-bad"]; ok {
		t.Fatalf("decrypt failure row must be skipped: %#v", summaries)
	}
}

func TestW14fSelectionStatesRowsErrArm(t *testing.T) {
	store := w14fFakeStore(t, true, &w14fFakeState{queryFn: func(query string, args []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(query, "key_fingerprint") {
			return w14fErrRows(errors.New("w14f selection iterate failure")), nil
		}
		_ = args
		return &w14fFakeRows{}, nil
	}})
	if _, err := store.LoadSelectionStatesByAccountIds(context.Background(), []string{"w14f-acc"}); err == nil {
		t.Fatalf("selection rows.Err failure must error")
	}
}

func TestW14fNewStoreDefaultsClock(t *testing.T) {
	db := w14fOpenFakeDB(t, &w14fFakeState{})
	store, err := NewStore(Config{DB: db, Secret: "w14f-secret"})
	if err != nil {
		t.Fatal(err)
	}
	// Now 未注入 → 默认 UTC 时钟（覆盖 NewStore 的默认臂）。
	if _, err := time.Parse(time.RFC3339Nano, store.nowISO()); err != nil {
		t.Fatalf("default clock output must be RFC3339: %s", store.nowISO())
	}
}

// ---- ResolveTarget 合并凭据臂 ----

func TestW14fResolveTargetMergesCredentials(t *testing.T) {
	store := &Store{secret: testSecret}
	credentials := map[string]any{"base_url": "https://w14f.example"}
	target := store.ResolveTarget(TargetInput{
		AccountID:                 "w14f-acc",
		SystemAccountID:           "w14f-sys",
		OwnerSystemAccountID:      "w14f-owner",
		CredentialSourceAccountID: "w14f-source",
		SelectedAPIKeyFingerprint: "",
		ProviderCode:              "openai",
		ProtocolCode:              "openai",
		ProtocolVersion:           "v1",
		AccountType:               "api_key",
		APIKey:                    "w14f-key-b",
		APIKeys:                   []string{"w14f-key-a", "w14f-key-b"},
		Credentials:               credentials,
	})
	if target != nil {
		t.Fatalf("empty fingerprint must skip: %+v", target)
	}
	fingerprint := (&Store{secret: testSecret}).FingerprintAPIKey("w14f-key-b")
	target = store.ResolveTarget(TargetInput{
		AccountID:                 "w14f-acc",
		SystemAccountID:           "w14f-sys",
		OwnerSystemAccountID:      "w14f-owner",
		CredentialSourceAccountID: "w14f-source",
		SelectedAPIKeyFingerprint: fingerprint,
		ProviderCode:              "openai",
		ProtocolCode:              "openai",
		ProtocolVersion:           "v1",
		AccountType:               "api_key",
		APIKey:                    "w14f-key-b",
		APIKeys:                   []string{"w14f-key-a", "w14f-key-b"},
		Credentials:               credentials,
	})
	if target == nil {
		t.Fatalf("merged credentials must resolve")
	}
	if target.AccountID != "w14f-source" || target.SystemAccountID != "w14f-owner" || target.KeyIndex != 0 {
		t.Fatalf("target projection: %+v", target)
	}
}
