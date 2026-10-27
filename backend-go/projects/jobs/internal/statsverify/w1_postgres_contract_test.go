package statsverify

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type w1PGContractStep struct {
	kind             string
	queryContains    string
	queryContainsAll []string
	args             []any
	columns          []string
	rows             [][]driver.Value
	rowsErr          error
	err              error
}

type w1PGContractScript struct {
	mu        sync.Mutex
	t         *testing.T
	steps     []w1PGContractStep
	nextStep  int
	commits   int
	rollbacks int
	commitErr error
}

func w1PGContractDB(t *testing.T, steps ...w1PGContractStep) (*sql.DB, *w1PGContractScript) {
	t.Helper()
	script := &w1PGContractScript{t: t, steps: steps}
	db := sql.OpenDB(w1PGContractConnector{script: script})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		_ = db.Close()
		script.assertConsumed()
	})
	return db, script
}

func (s *w1PGContractScript) take(kind, query string, args []driver.NamedValue) (w1PGContractStep, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nextStep >= len(s.steps) {
		s.t.Errorf("unexpected PG %s: %s", kind, query)
		return w1PGContractStep{}, errors.New("unexpected PG contract call")
	}
	step := s.steps[s.nextStep]
	s.nextStep++
	if step.kind != kind {
		s.t.Errorf("PG call kind = %q, want %q", kind, step.kind)
	}
	if !strings.Contains(query, step.queryContains) {
		s.t.Errorf("PG %s query %q does not contain %q", kind, query, step.queryContains)
	}
	for _, fragment := range step.queryContainsAll {
		if !strings.Contains(query, fragment) {
			s.t.Errorf("PG %s query %q does not contain %q", kind, query, fragment)
		}
	}
	if len(args) != len(step.args) {
		s.t.Errorf("PG %s args = %#v, want %#v", kind, args, step.args)
	} else {
		for i := range args {
			if !reflect.DeepEqual(args[i].Value, step.args[i]) {
				s.t.Errorf("PG %s arg[%d] = %#v, want %#v", kind, i, args[i].Value, step.args[i])
			}
		}
	}
	return step, step.err
}

func (s *w1PGContractScript) assertConsumed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nextStep != len(s.steps) {
		s.t.Errorf("PG contract consumed %d of %d scripted calls", s.nextStep, len(s.steps))
	}
}

type w1PGContractConnector struct{ script *w1PGContractScript }

func (c w1PGContractConnector) Connect(context.Context) (driver.Conn, error) {
	return &w1PGContractConn{script: c.script}, nil
}

func (w1PGContractConnector) Driver() driver.Driver { return w1PGContractDriver{} }

type w1PGContractDriver struct{}

func (w1PGContractDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("w1 PG contract driver requires its scripted connector")
}

type w1PGContractConn struct{ script *w1PGContractScript }

func (*w1PGContractConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepared statement in PG contract test")
}

func (*w1PGContractConn) Close() error { return nil }

func (c *w1PGContractConn) Begin() (driver.Tx, error) {
	return &w1PGContractTx{script: c.script}, nil
}

func (*w1PGContractConn) CheckNamedValue(*driver.NamedValue) error { return nil }

func (c *w1PGContractConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	step, err := c.script.take("query", query, args)
	if err != nil {
		return nil, err
	}
	return &w1PGContractRows{columns: step.columns, rows: step.rows, rowsErr: step.rowsErr}, nil
}

func (c *w1PGContractConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	_, err := c.script.take("exec", query, args)
	if err != nil {
		return nil, err
	}
	return driver.RowsAffected(1), nil
}

type w1PGContractTx struct{ script *w1PGContractScript }

func (tx *w1PGContractTx) Commit() error {
	tx.script.mu.Lock()
	tx.script.commits++
	err := tx.script.commitErr
	tx.script.mu.Unlock()
	return err
}

func (tx *w1PGContractTx) Rollback() error {
	tx.script.mu.Lock()
	tx.script.rollbacks++
	tx.script.mu.Unlock()
	return nil
}

type w1PGContractRows struct {
	columns       []string
	rows          [][]driver.Value
	rowsErr       error
	nextRow       int
	returnedError bool
}

func (r *w1PGContractRows) Columns() []string { return r.columns }

func (*w1PGContractRows) Close() error { return nil }

func (r *w1PGContractRows) Next(dest []driver.Value) error {
	if r.nextRow < len(r.rows) {
		row := r.rows[r.nextRow]
		r.nextRow++
		if len(row) != len(dest) {
			return fmt.Errorf("scripted row has %d values for %d columns", len(row), len(dest))
		}
		copy(dest, row)
		return nil
	}
	if r.rowsErr != nil && !r.returnedError {
		r.returnedError = true
		return r.rowsErr
	}
	return io.EOF
}

func TestW1PostgresSchemaContract(t *testing.T) {
	query := "table_schema || '.' || table_name"
	args := []any{
		[]string{"juhe_stats"},
		[]string{"juhe_business", "juhe_usage"},
		append([]string(nil), postgresRequiredTables...),
	}
	allRows := make([][]driver.Value, 0, len(postgresRequiredTables))
	for _, table := range postgresRequiredTables {
		allRows = append(allRows, []driver.Value{table})
	}

	t.Run("all required tables and qualified-name filters", func(t *testing.T) {
		db, _ := w1PGContractDB(t, w1PGContractStep{
			kind: "query", queryContains: query, args: args,
			queryContainsAll: []string{"table_schema = ANY($1)", "table_schema = ANY($2)", "(table_schema || '.' || table_name) = ANY($3)"},
			columns:          []string{"qualified"}, rows: allRows,
		})
		store := &Store{db: db}
		if err := store.checkPostgresSchema(context.Background()); err != nil {
			t.Fatalf("complete external schema should pass: %v", err)
		}
	})

	t.Run("missing external table is reported", func(t *testing.T) {
		missing := postgresRequiredTables[0]
		rows := make([][]driver.Value, 0, len(allRows)-1)
		for _, row := range allRows {
			if row[0] != missing {
				rows = append(rows, row)
			}
		}
		db, _ := w1PGContractDB(t, w1PGContractStep{
			kind: "query", queryContains: query, args: args,
			queryContainsAll: []string{"table_schema = ANY($1)", "table_schema = ANY($2)", "(table_schema || '.' || table_name) = ANY($3)"},
			columns:          []string{"qualified"}, rows: rows,
		})
		store := &Store{db: db}
		err := store.checkPostgresSchema(context.Background())
		if err == nil || err.Error() != "statsverify PostgreSQL 缺少外部预置表: "+missing {
			t.Fatalf("missing schema error = %v", err)
		}
	})

	t.Run("query error retains its cause", func(t *testing.T) {
		cause := errors.New("catalog query unavailable")
		db, _ := w1PGContractDB(t, w1PGContractStep{
			kind: "query", queryContains: query, args: args, err: cause,
			queryContainsAll: []string{"table_schema = ANY($1)", "table_schema = ANY($2)", "(table_schema || '.' || table_name) = ANY($3)"},
		})
		store := &Store{db: db}
		err := store.checkPostgresSchema(context.Background())
		if !errors.Is(err, cause) || err.Error() != "读取 statsverify postgres tables 失败: "+cause.Error() {
			t.Fatalf("schema query error = %v", err)
		}
	})

	t.Run("row scan error is contextualized", func(t *testing.T) {
		db, _ := w1PGContractDB(t, w1PGContractStep{
			kind: "query", queryContains: query, args: args,
			queryContainsAll: []string{"table_schema = ANY($1)", "table_schema = ANY($2)", "(table_schema || '.' || table_name) = ANY($3)"},
			columns:          []string{"qualified"}, rows: [][]driver.Value{{nil}},
		})
		store := &Store{db: db}
		err := store.checkPostgresSchema(context.Background())
		if err == nil || !strings.Contains(err.Error(), "读取 statsverify postgres table 名称失败:") || !strings.Contains(err.Error(), "converting NULL to string") {
			t.Fatalf("schema row scan error = %v", err)
		}
	})

	t.Run("row iteration error retains its cause", func(t *testing.T) {
		cause := errors.New("catalog stream interrupted")
		db, _ := w1PGContractDB(t, w1PGContractStep{
			kind: "query", queryContains: query, args: args,
			queryContainsAll: []string{"table_schema = ANY($1)", "table_schema = ANY($2)", "(table_schema || '.' || table_name) = ANY($3)"},
			columns:          []string{"qualified"}, rows: allRows, rowsErr: cause,
		})
		store := &Store{db: db}
		err := store.checkPostgresSchema(context.Background())
		if !errors.Is(err, cause) || err.Error() != "遍历 statsverify postgres tables 失败: "+cause.Error() {
			t.Fatalf("schema row iteration error = %v", err)
		}
	})
}

func TestW1PostgresGroupAccountStatsDirtyContract(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 12, 3, 4, 5, 0, time.UTC)
	updatedAt := NowIso(now)

	t.Run("mark all uses postgres placeholders and commits", func(t *testing.T) {
		db, script := w1PGContractDB(t, w1PGContractStep{
			kind: "exec", queryContains: "INSERT INTO juhe_business.group_account_stats_dirty",
			queryContainsAll: []string{
				"VALUES ($1, $2, $3)", "ON CONFLICT(group_id)",
				"reason = EXCLUDED.reason", "updated_at = EXCLUDED.updated_at",
			},
			args: []any{groupAccountStatsDirtyAll, "w1-contract", updatedAt},
		})
		store := &Store{db: db}
		if err := store.MarkAllGroupAccountStatsDirty(ctx, "w1-contract", now); err != nil {
			t.Fatalf("MarkAllGroupAccountStatsDirty: %v", err)
		}
		script.mu.Lock()
		defer script.mu.Unlock()
		if script.commits != 1 || script.rollbacks != 0 {
			t.Fatalf("transaction counts: commits=%d rollbacks=%d", script.commits, script.rollbacks)
		}
	})

	t.Run("mark all preserves a missing-table error", func(t *testing.T) {
		cause := errors.New(`ERROR: relation "juhe_business.group_account_stats_dirty" does not exist (SQLSTATE 42P01)`)
		db, script := w1PGContractDB(t, w1PGContractStep{
			kind: "exec", queryContains: "INSERT INTO juhe_business.group_account_stats_dirty",
			queryContainsAll: []string{
				"VALUES ($1, $2, $3)", "ON CONFLICT(group_id)",
				"reason = EXCLUDED.reason", "updated_at = EXCLUDED.updated_at",
			},
			args: []any{groupAccountStatsDirtyAll, "w1-contract", updatedAt}, err: cause,
		})
		store := &Store{db: db}
		err := store.MarkAllGroupAccountStatsDirty(ctx, "w1-contract", now)
		want := "标记 group_account_stats_dirty 失败: " + cause.Error()
		if !errors.Is(err, cause) || err.Error() != want {
			t.Fatalf("mark error = %v, want %q", err, want)
		}
		script.mu.Lock()
		defer script.mu.Unlock()
		if script.commits != 0 || script.rollbacks != 1 {
			t.Fatalf("transaction counts: commits=%d rollbacks=%d", script.commits, script.rollbacks)
		}
	})

	t.Run("refresh all consumes an empty page and commits", func(t *testing.T) {
		dirtyAt := "2026-10-11T01:02:03.000Z"
		db, script := w1PGContractDB(t,
			w1PGContractStep{
				kind: "query", queryContains: "FROM juhe_business.group_account_stats_dirty",
				queryContainsAll: []string{"WHERE group_id = $1", "LIMIT 1"},
				args:             []any{groupAccountStatsDirtyAll},
				columns:          []string{"group_id", "reason", "updated_at"},
				rows:             [][]driver.Value{{groupAccountStatsDirtyAll, "w1-contract", dirtyAt}},
			},
			w1PGContractStep{
				kind: "query", queryContains: "FROM juhe_business.groups",
				queryContainsAll: []string{"ORDER BY id ASC", "LIMIT $1"},
				args:             []any{groupAccountStatsBatchLimit},
				columns:          []string{"id", "system_account_id"},
			},
			w1PGContractStep{
				kind: "exec", queryContains: "DELETE FROM juhe_business.group_account_stats_dirty",
				queryContainsAll: []string{"group_id = $1 AND updated_at = $2"},
				args:             []any{groupAccountStatsDirtyAll, dirtyAt},
			},
		)
		store := &Store{db: db}
		refreshed, err := store.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Now: now})
		if err != nil || refreshed != 1 {
			t.Fatalf("RefreshDirtyGroupAccountStats = %d, %v; want 1, nil", refreshed, err)
		}
		script.mu.Lock()
		defer script.mu.Unlock()
		if script.commits != 1 || script.rollbacks != 0 {
			t.Fatalf("transaction counts: commits=%d rollbacks=%d", script.commits, script.rollbacks)
		}
	})

	t.Run("refresh preserves a missing-table error", func(t *testing.T) {
		cause := errors.New(`ERROR: relation "juhe_business.group_account_stats_dirty" does not exist (SQLSTATE 42P01)`)
		db, script := w1PGContractDB(t, w1PGContractStep{
			kind: "query", queryContains: "FROM juhe_business.group_account_stats_dirty",
			queryContainsAll: []string{"WHERE group_id = $1", "LIMIT 1"},
			args:             []any{groupAccountStatsDirtyAll}, err: cause,
		})
		store := &Store{db: db}
		_, err := store.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Now: now})
		want := "读取 group_account_stats_dirty __all__ 失败: " + cause.Error()
		if !errors.Is(err, cause) || err.Error() != want {
			t.Fatalf("refresh error = %v, want %q", err, want)
		}
		script.mu.Lock()
		defer script.mu.Unlock()
		if script.commits != 0 || script.rollbacks != 1 {
			t.Fatalf("transaction counts: commits=%d rollbacks=%d", script.commits, script.rollbacks)
		}
	})
}

func w1PGNoAllDirtyRowsStep() w1PGContractStep {
	return w1PGContractStep{
		kind: "query", queryContains: "FROM juhe_business.group_account_stats_dirty",
		queryContainsAll: []string{"WHERE group_id = $1", "LIMIT 1"},
		args:             []any{groupAccountStatsDirtyAll},
		columns:          []string{"group_id", "reason", "updated_at"},
	}
}

func w1PGNoPerGroupDirtyRowsStep() w1PGContractStep {
	return w1PGContractStep{
		kind: "query", queryContains: "FROM juhe_business.group_account_stats_dirty",
		queryContainsAll: []string{"WHERE group_id <> $1", "LIMIT $2"},
		args:             []any{groupAccountStatsDirtyAll, groupAccountStatsBatchLimit},
		columns:          []string{"group_id", "reason", "updated_at"},
	}
}

func w1PGOnePerGroupDirtyRowsStep(groupID, updatedAt string) w1PGContractStep {
	step := w1PGNoPerGroupDirtyRowsStep()
	step.rows = [][]driver.Value{{groupID, "w1-contract", updatedAt}}
	return step
}

func TestW1PostgresGroupAccountStatsFailureBoundaries(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 12, 3, 4, 5, 0, time.UTC)
	dirtyAt := "2026-10-11T01:02:03.000Z"

	t.Run("per-group dirty query error retains its cause", func(t *testing.T) {
		cause := errors.New("dirty query interrupted")
		db, script := w1PGContractDB(t,
			w1PGNoAllDirtyRowsStep(),
			w1PGContractStep{
				kind: "query", queryContains: "FROM juhe_business.group_account_stats_dirty",
				queryContainsAll: []string{"WHERE group_id <> $1", "LIMIT $2"},
				args:             []any{groupAccountStatsDirtyAll, groupAccountStatsBatchLimit},
				err:              cause,
			},
		)
		store := &Store{db: db}
		_, err := store.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Now: now})
		want := "读取 group_account_stats_dirty 失败: " + cause.Error()
		if !errors.Is(err, cause) || err.Error() != want {
			t.Fatalf("per-group dirty query error = %v, want %q", err, want)
		}
		script.mu.Lock()
		defer script.mu.Unlock()
		if script.commits != 0 || script.rollbacks != 1 {
			t.Fatalf("transaction counts: commits=%d rollbacks=%d", script.commits, script.rollbacks)
		}
	})

	t.Run("stats probe error retains its cause", func(t *testing.T) {
		cause := errors.New("stats catalog unavailable")
		db, script := w1PGContractDB(t,
			w1PGNoAllDirtyRowsStep(),
			w1PGNoPerGroupDirtyRowsStep(),
			w1PGContractStep{
				kind: "query", queryContains: "SELECT 1 FROM juhe_stats.group_account_stats LIMIT 1",
				err: cause,
			},
		)
		store := &Store{db: db}
		_, err := store.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Now: now})
		want := "检查 group_account_stats 失败: " + cause.Error()
		if !errors.Is(err, cause) || err.Error() != want {
			t.Fatalf("stats probe error = %v, want %q", err, want)
		}
		script.mu.Lock()
		defer script.mu.Unlock()
		if script.commits != 0 || script.rollbacks != 1 {
			t.Fatalf("transaction counts: commits=%d rollbacks=%d", script.commits, script.rollbacks)
		}
	})

	t.Run("group page query error retains its cause", func(t *testing.T) {
		cause := errors.New("groups query interrupted")
		db, script := w1PGContractDB(t,
			w1PGNoAllDirtyRowsStep(),
			w1PGOnePerGroupDirtyRowsStep("group-1", dirtyAt),
			w1PGContractStep{
				kind: "query", queryContains: "FROM juhe_business.groups",
				queryContainsAll: []string{"WHERE id IN ($1)"},
				args:             []any{"group-1"},
				err:              cause,
			},
		)
		store := &Store{db: db}
		_, err := store.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Now: now})
		want := "读取 groups 失败: " + cause.Error()
		if !errors.Is(err, cause) || err.Error() != want {
			t.Fatalf("group page query error = %v, want %q", err, want)
		}
		script.mu.Lock()
		defer script.mu.Unlock()
		if script.commits != 0 || script.rollbacks != 1 {
			t.Fatalf("transaction counts: commits=%d rollbacks=%d", script.commits, script.rollbacks)
		}
	})

	t.Run("group page scan error is returned", func(t *testing.T) {
		db, script := w1PGContractDB(t,
			w1PGNoAllDirtyRowsStep(),
			w1PGOnePerGroupDirtyRowsStep("group-1", dirtyAt),
			w1PGContractStep{
				kind: "query", queryContains: "FROM juhe_business.groups",
				queryContainsAll: []string{"WHERE id IN ($1)"},
				args:             []any{"group-1"},
				columns:          []string{"id", "system_account_id"},
				rows:             [][]driver.Value{{nil, "sys-1"}},
			},
		)
		store := &Store{db: db}
		_, err := store.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Now: now})
		if err == nil || !strings.Contains(err.Error(), "converting NULL to string") {
			t.Fatalf("group page scan error = %v", err)
		}
		script.mu.Lock()
		defer script.mu.Unlock()
		if script.commits != 0 || script.rollbacks != 1 {
			t.Fatalf("transaction counts: commits=%d rollbacks=%d", script.commits, script.rollbacks)
		}
	})

	t.Run("group page iteration error retains its cause", func(t *testing.T) {
		cause := errors.New("groups stream interrupted")
		db, script := w1PGContractDB(t,
			w1PGNoAllDirtyRowsStep(),
			w1PGOnePerGroupDirtyRowsStep("group-1", dirtyAt),
			w1PGContractStep{
				kind: "query", queryContains: "FROM juhe_business.groups",
				queryContainsAll: []string{"WHERE id IN ($1)"},
				args:             []any{"group-1"},
				columns:          []string{"id", "system_account_id"},
				rows:             [][]driver.Value{{"group-1", "sys-1"}},
				rowsErr:          cause,
			},
		)
		store := &Store{db: db}
		_, err := store.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Now: now})
		if !errors.Is(err, cause) || err.Error() != cause.Error() {
			t.Fatalf("group page iteration error = %v", err)
		}
		script.mu.Lock()
		defer script.mu.Unlock()
		if script.commits != 0 || script.rollbacks != 1 {
			t.Fatalf("transaction counts: commits=%d rollbacks=%d", script.commits, script.rollbacks)
		}
	})

	t.Run("commit error is returned", func(t *testing.T) {
		cause := errors.New("commit interrupted")
		db, script := w1PGContractDB(t,
			w1PGContractStep{
				kind: "query", queryContains: "FROM juhe_business.group_account_stats_dirty",
				queryContainsAll: []string{"WHERE group_id = $1", "LIMIT 1"},
				args:             []any{groupAccountStatsDirtyAll},
				columns:          []string{"group_id", "reason", "updated_at"},
				rows:             [][]driver.Value{{groupAccountStatsDirtyAll, "w1-contract", dirtyAt}},
			},
			w1PGContractStep{
				kind: "query", queryContains: "FROM juhe_business.groups",
				queryContainsAll: []string{"ORDER BY id ASC", "LIMIT $1"},
				args:             []any{groupAccountStatsBatchLimit},
				columns:          []string{"id", "system_account_id"},
			},
			w1PGContractStep{
				kind: "exec", queryContains: "DELETE FROM juhe_business.group_account_stats_dirty",
				queryContainsAll: []string{"group_id = $1 AND updated_at = $2"},
				args:             []any{groupAccountStatsDirtyAll, dirtyAt},
			},
		)
		script.commitErr = cause
		store := &Store{db: db}
		refreshed, err := store.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Now: now})
		if refreshed != 0 || !errors.Is(err, cause) || err.Error() != cause.Error() {
			t.Fatalf("commit result = %d, %v; want 0, %v", refreshed, err, cause)
		}
		script.mu.Lock()
		defer script.mu.Unlock()
		if script.commits != 1 || script.rollbacks != 0 {
			t.Fatalf("transaction counts: commits=%d rollbacks=%d", script.commits, script.rollbacks)
		}
	})
}

func TestW1DateKeyRejectsNonNumericComponent(t *testing.T) {
	if _, ok := parseDateKeyParts("202x-02-28"); ok {
		t.Fatal("date key with a non-numeric year component must be rejected")
	}
}

