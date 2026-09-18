// w13g2 modelcheckowner 失败注入驱动与覆盖补充（仅测试编译）：与 authz 同
// 构的 QueryContext/ExecContext 层 SQL 片段注入，覆盖各 store 函数事务中途
// 的错误返回臂；badscan 单列假行触发 rows.Scan 列数不匹配错误分支。
package modelcheckowner

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	sqlite "modernc.org/sqlite"
)

var errW13g2McFailpoint = errors.New("w13g2 mc failpoint: injected failure")

type w13g2McFailpoint struct {
	mu      sync.Mutex
	pattern string
	scan    bool
}

func (fp *w13g2McFailpoint) arm(pattern string) {
	fp.mu.Lock()
	fp.pattern = pattern
	fp.mu.Unlock()
}

func (fp *w13g2McFailpoint) armScan(pattern string) {
	fp.mu.Lock()
	fp.pattern = pattern
	fp.scan = true
	fp.mu.Unlock()
}

func (fp *w13g2McFailpoint) disarm() {
	fp.mu.Lock()
	fp.pattern = ""
	fp.scan = false
	fp.mu.Unlock()
}

func (fp *w13g2McFailpoint) armed(query string) bool {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	return fp.pattern != "" && !fp.scan && strings.Contains(query, fp.pattern)
}

func (fp *w13g2McFailpoint) scanArmed(query string) bool {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	return fp.pattern != "" && fp.scan && strings.Contains(query, fp.pattern)
}

type w13g2McFakeRows struct{ sent bool }

func (r *w13g2McFakeRows) Columns() []string { return []string{"c0"} }

func (r *w13g2McFakeRows) Close() error { return nil }

func (r *w13g2McFakeRows) Next(dest []driver.Value) error {
	if r.sent {
		return context.Canceled
	}
	r.sent = true
	if len(dest) > 0 {
		dest[0] = int64(7)
	}
	return nil
}

type w13g2McFailDriver struct {
	inner driver.Driver
	fp    *w13g2McFailpoint
}

func (d *w13g2McFailDriver) Open(name string) (driver.Conn, error) {
	raw, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	conn := &w13g2McFailConn{Conn: raw, fp: d.fp}
	if beginTx, ok := raw.(driver.ConnBeginTx); ok {
		conn.beginTx = beginTx
	}
	if queryer, ok := raw.(driver.QueryerContext); ok {
		conn.queryer = queryer
	}
	if execer, ok := raw.(driver.ExecerContext); ok {
		conn.execer = execer
	}
	return conn, nil
}

type w13g2McFailConn struct {
	driver.Conn
	beginTx driver.ConnBeginTx
	queryer driver.QueryerContext
	execer  driver.ExecerContext
	fp      *w13g2McFailpoint
}

func (c *w13g2McFailConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if c.beginTx == nil {
		return nil, driver.ErrSkip
	}
	return c.beginTx.BeginTx(ctx, opts)
}

func (c *w13g2McFailConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.fp.scanArmed(query) {
		return &w13g2McFakeRows{}, nil
	}
	if c.fp.armed(query) {
		return nil, errW13g2McFailpoint
	}
	if c.queryer == nil {
		return nil, driver.ErrSkip
	}
	return c.queryer.QueryContext(ctx, query, args)
}

func (c *w13g2McFailConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.fp.armed(query) {
		return nil, errW13g2McFailpoint
	}
	if c.execer == nil {
		return nil, driver.ErrSkip
	}
	return c.execer.ExecContext(ctx, query, args)
}

var w13g2McFailDrivers sync.Map

func w13g2McRegisterFailDriver(key string) *w13g2McFailpoint {
	if loaded, ok := w13g2McFailDrivers.Load(key); ok {
		return loaded.(*w13g2McFailpoint)
	}
	d := &w13g2McFailDriver{inner: &sqlite.Driver{}, fp: &w13g2McFailpoint{}}
	if actual, loaded := w13g2McFailDrivers.LoadOrStore(key, d.fp); loaded {
		return actual.(*w13g2McFailpoint)
	}
	sql.Register("w13g2mcfail-"+key, d)
	return d.fp
}

// w13g2McFailStore 打开 runtimeTestDDL 结构的失败注入库。
func w13g2McFailStore(t *testing.T) (*Store, *w13g2McFailpoint) {
	t.Helper()
	key := strings.ReplaceAll(t.Name(), "/", "-")
	fp := w13g2McRegisterFailDriver(key)
	db, err := sql.Open("w13g2mcfail-"+key, "file:w13g2mcfail-"+key+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range runtimeTestDDL() {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return &Store{db: db, mode: "sqlite"}, fp
}

func w13g2McTx(t *testing.T, s *Store) *sql.Tx {
	t.Helper()
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

// TestW13g2McFailpointCoreArms 覆盖 runtime/input/run/durable/query/outcome
// 与健康同步的深层错误臂。
func TestW13g2McFailpointCoreArms(t *testing.T) {
	s, fp := w13g2McFailStore(t)
	ctx := context.Background()
	input := wbInputFixture()

	t.Run("issueInputExistingRead", func(t *testing.T) {
		fp.arm("FROM model_check_inputs WHERE input_id=?")
		defer fp.disarm()
		if _, err := s.IssueInput(ctx, input); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("issueInputVersionInsert", func(t *testing.T) {
		fp.arm("INSERT INTO model_check_input_versions")
		defer fp.disarm()
		if _, err := s.IssueInput(ctx, input); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("issueInputPayloadInsert", func(t *testing.T) {
		fp.arm("INSERT INTO model_check_inputs (input_id")
		defer fp.disarm()
		if _, err := s.IssueInput(ctx, input); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("loadInputSelect", func(t *testing.T) {
		if _, err := s.IssueInput(ctx, input); err != nil {
			t.Fatal(err)
		}
		fp.arm("FROM model_check_inputs WHERE input_id=?")
		defer fp.disarm()
		if _, err := s.LoadInput(ctx, input.InputID, time.Now()); err == nil {
			t.Fatalf("应失败")
		}
	})

	now := input.IssuedAt.Add(time.Second)
	t.Run("claimInputSelect", func(t *testing.T) {
		fp.arm("FROM model_check_execution_claims WHERE input_id=?")
		defer fp.disarm()
		if _, err := s.ClaimInput(ctx, input.InputID, "token", "outcome-1", "owner", time.Minute, now); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("renewClaim", func(t *testing.T) {
		claim, err := s.ClaimInput(ctx, input.InputID, "token", "outcome-1", "owner", time.Minute, now)
		if err != nil {
			t.Fatal(err)
		}
		fp.arm("SET claim_until=?,updated_at=? WHERE input_id=? AND claim_token=?")
		defer fp.disarm()
		if err := s.RenewClaim(ctx, claim, time.Minute, now); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("releaseClaim", func(t *testing.T) {
		fp.arm("DELETE FROM model_check_execution_claims WHERE input_id=?")
		defer fp.disarm()
		if err := s.ReleaseClaim(ctx, Claim{InputID: input.InputID}, now); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("commitOutcome", func(t *testing.T) {
		fp.arm("INSERT INTO model_check_outcomes")
		defer fp.disarm()
		if err := s.CommitOutcome(ctx, Outcome{
			OutcomeID: "o1", InputID: input.InputID, InputDigest: "d", FenceToken: 1,
			ObservedAt: now, StoredAt: now, Payload: []byte(`{}`), PayloadDigest: "p",
		}, Claim{InputID: input.InputID, FenceToken: 1}, now); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("listCommittedOutcomes", func(t *testing.T) {
		fp.arm("FROM model_check_outcomes o JOIN")
		defer fp.disarm()
		if _, err := s.ListCommittedOutcomes(ctx, OutcomeCursor{}, 10); err == nil {
			t.Fatalf("应失败")
		}
		fp.disarm()
		fp.armScan("FROM model_check_outcomes o JOIN")
		defer fp.disarm()
		if _, err := s.ListCommittedOutcomes(ctx, OutcomeCursor{}, 10); err == nil {
			t.Fatalf("badscan 应失败")
		}
	})

	t.Run("createRun", func(t *testing.T) {
		fp.arm("INSERT INTO model_check_runs")
		defer fp.disarm()
		if err := s.CreateRun(ctx, RunRecord{ID: "run-fp", StartedAt: time.Now()}); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("appendItem", func(t *testing.T) {
		fp.arm("INSERT INTO model_check_items")
		defer fp.disarm()
		if err := s.AppendItem(ctx, ItemRecord{ID: "item-fp", RunID: "run-1"}); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("appendObservation", func(t *testing.T) {
		fp.arm("INSERT INTO model_check_observations")
		defer fp.disarm()
		if err := s.AppendObservation(ctx, ObservationRecord{ID: "obs-fp", RunID: "run-1", CreatedAt: time.Now()}); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("projectOutcome", func(t *testing.T) {
		fp.arm("UPDATE model_check_runs SET")
		defer fp.disarm()
		if err := s.ProjectOutcome(ctx, OutcomeProjection{RunID: "run-1", FinishedAt: time.Now()}); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("beginRunning", func(t *testing.T) {
		fp.arm("FROM model_check_runs WHERE id=?")
		defer fp.disarm()
		if _, err := s.beginRunning(ctx, "run-missing"); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("applyHealthFact", func(t *testing.T) {
		fp.arm("INSERT INTO account_quality_health_hourly")
		defer fp.disarm()
		if _, err := s.ApplyHealthFact(ctx, HealthFact{AccountID: "a", StatHour: "h", RunID: "r", ObservedAt: time.Now()}); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("readHealthFact", func(t *testing.T) {
		fp.arm("FROM account_quality_health_hourly WHERE")
		defer fp.disarm()
		if _, _, err := s.ReadHealthFact(ctx, "a", "h"); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("markHealthSync", func(t *testing.T) {
		fp.arm("UPDATE model_check_runs SET quality_health_sync_status=?")
		defer fp.disarm()
		if err := s.MarkHealthSync(ctx, "run-1", "ok"); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("listHealthSyncRetries", func(t *testing.T) {
		fp.arm("FROM model_check_runs WHERE")
		defer fp.disarm()
		if _, err := s.ListHealthSyncRetries(ctx, 10); err == nil {
			t.Fatalf("应失败")
		}
		fp.disarm()
		fp.armScan("FROM model_check_runs WHERE")
		defer fp.disarm()
		if _, err := s.ListHealthSyncRetries(ctx, 10); err == nil {
			t.Fatalf("badscan 应失败")
		}
	})

	t.Run("activateTokenInterceptBaseline", func(t *testing.T) {
		fp.arm("model_token_intercept_baselines")
		defer fp.disarm()
		if err := s.ActivateTokenInterceptBaseline(ctx, TokenInterceptBaselineActivation{
			CohortKeyHMAC: "hmac", RequestedModel: "m", TokenizerVersion: "t",
			ProbeSetVersion: "p", BaselineVersion: 1, StrongThresholdIntercept: 0.5,
		}); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("ensureHealthRetryTasks", func(t *testing.T) {
		fp.arm("EnsureHealthRetryTasks placeholder")
		defer fp.disarm()
	})

	t.Run("projectTrust", func(t *testing.T) {
		fp.arm("INSERT INTO model_trust_observation_receipts")
		defer fp.disarm()
		if err := s.ProjectTrust(ctx, TrustProjection{RunID: "r", SystemAccountID: "s", AccountID: "a", RequestedModel: "m"}); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("checkSchemaColumns", func(t *testing.T) {
		fp.arm("PRAGMA table_info")
		defer fp.disarm()
		if err := s.checkColumns(ctx, "model_check_runs", []string{"id"}); err == nil {
			t.Fatalf("应失败")
		}
	})
}
