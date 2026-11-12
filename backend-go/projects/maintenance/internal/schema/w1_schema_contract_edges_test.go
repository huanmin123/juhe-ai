package schema

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
)

const w1PGCapabilityDomainOldCheck = "CHECK ((capability_domain = ANY (ARRAY['chat','video','audio','image'])))"

func w1PostgresShapeResult(t *testing.T, columnRows, checkRows [][]driver.Value) error {
	t.Helper()
	rec := &wmSchemaRecorder{}
	rec.script("FROM information_schema.columns", nil, columnRows)
	rec.script("FROM pg_catalog.pg_constraint", nil, checkRows)
	db := openWMSchemaFakeDB(rec)
	t.Cleanup(func() { _ = db.Close() })
	return VerifyPostgresBusinessCapabilityDomainShape(context.Background(), db)
}

func TestW1PostgresCapabilityDomainShapeEdges(t *testing.T) {
	validColumn := [][]driver.Value{{"text", "NO", "'chat'::text"}}
	cases := []struct {
		name       string
		columnRows [][]driver.Value
		checkRows  [][]driver.Value
		wantError  string
	}{
		{name: "missing column is accepted", columnRows: [][]driver.Value{}, checkRows: [][]driver.Value{}},
		{name: "no check is valid", columnRows: validColumn, checkRows: [][]driver.Value{}},
		{name: "unrelated check is ignored", columnRows: validColumn, checkRows: [][]driver.Value{{"CHECK ((code = 'x'::text))"}}},
		{name: "wrong type", columnRows: [][]driver.Value{{"character varying", "NO", "'chat'::text"}}, wantError: "列类型"},
		{name: "nullable", columnRows: [][]driver.Value{{"text", "YES", "'chat'::text"}}, wantError: "列可空"},
		{name: "missing default", columnRows: [][]driver.Value{{"text", "NO", nil}}, wantError: "默认值为 NULL（无默认值）"},
		{name: "wrong default", columnRows: [][]driver.Value{{"text", "NO", "'video'::text"}}, wantError: "默认值为 'video'::text"},
		{name: "old check must be dropped", columnRows: validColumn, checkRows: [][]driver.Value{{w1PGCapabilityDomainOldCheck}}, wantError: "仍存在 CHECK"},
		{name: "column scan error", columnRows: [][]driver.Value{{"text", nil, "'chat'::text"}}, wantError: "converting NULL to string is unsupported"},
		{name: "check scan error", columnRows: validColumn, checkRows: [][]driver.Value{{nil}}, wantError: "converting NULL to string is unsupported"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := w1PostgresShapeResult(t, tc.columnRows, tc.checkRows)
			if tc.wantError == "" {
				if err != nil {
					t.Fatalf("VerifyPostgresBusinessCapabilityDomainShape: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error = %v, want substring %q", err, tc.wantError)
			}
		})
	}

	rec := &wmSchemaRecorder{}
	rec.script("FROM information_schema.columns", nil, validColumn)
	rec.scriptNextError("FROM pg_catalog.pg_constraint", [][]driver.Value{{w1PGCapabilityDomainOldCheck}})
	db := openWMSchemaFakeDB(rec)
	t.Cleanup(func() { _ = db.Close() })
	if err := VerifyPostgresBusinessCapabilityDomainShape(context.Background(), db); err == nil || !strings.Contains(err.Error(), "wm fake rows: 注入迭代错误") {
		t.Fatalf("rows.Err must be preserved: %v", err)
	}
	closed := openWMSchemaFakeDB(&wmSchemaRecorder{})
	if err := closed.Close(); err != nil {
		t.Fatalf("close fake DB: %v", err)
	}
	if err := VerifyPostgresBusinessCapabilityDomainShape(context.Background(), closed); err == nil || !strings.Contains(err.Error(), "database is closed") {
		t.Fatalf("query error must be preserved, got %v", err)
	}
}

func TestW1EnsurePostgresCapabilityDomainColumnExecutesAndPreservesErrors(t *testing.T) {
	const wantAlter = `ALTER TABLE "juhe_business"."accounts" ADD COLUMN IF NOT EXISTS capability_domain text NOT NULL DEFAULT 'chat'`
	t.Run("add and verify", func(t *testing.T) {
		rec := &wmSchemaRecorder{}
		rec.script("FROM information_schema.columns", nil, [][]driver.Value{{"text", "NO", "'chat'::text"}})
		rec.script("FROM pg_catalog.pg_constraint", nil, [][]driver.Value{})
		db := openWMSchemaFakeDB(rec)
		defer db.Close()
		if err := EnsurePostgresBusinessCapabilityDomainColumn(context.Background(), db); err != nil {
			t.Fatalf("EnsurePostgresBusinessCapabilityDomainColumn: %v", err)
		}
		queries := rec.execQueries()
		if len(queries) != 2 || queries[0] != wantAlter {
			t.Fatalf("executed DDL = %#v, want ALTER + drop-check", queries)
		}
		if !strings.Contains(queries[1], "DROP CONSTRAINT") || !strings.Contains(queries[1], "capability_domain") {
			t.Fatalf("second DDL must drop the old capability_domain CHECK: %q", queries[1])
		}
	})

	t.Run("add failure keeps original error", func(t *testing.T) {
		original := errors.New("injected PostgreSQL DDL error")
		rec := &wmSchemaRecorder{failNextExecsWith: []error{original}}
		db := openWMSchemaFakeDB(rec)
		defer db.Close()
		err := EnsurePostgresBusinessCapabilityDomainColumn(context.Background(), db)
		if !errors.Is(err, original) || !strings.Contains(err.Error(), "add accounts.capability_domain") {
			t.Fatalf("wrapped DDL error = %v, want original error and operation context", err)
		}
		if rec.queryCount() != 0 {
			t.Fatalf("shape verification must not run after ALTER failure, queries=%d", rec.queryCount())
		}
	})
}

func TestW1EnsurePostgresReturnsFinalCapabilityShapeError(t *testing.T) {
	rec := &wmSchemaRecorder{}
	rec.script("FROM information_schema.columns", nil, [][]driver.Value{{"character varying", "NO", "'chat'::text"}})
	db := openWMSchemaFakeDB(rec)
	defer db.Close()
	result, err := EnsurePostgres(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "verify accounts.capability_domain shape") || !strings.Contains(err.Error(), "列类型") {
		t.Fatalf("EnsurePostgres shape error = %v, want wrapped final verifier error", err)
	}
	if result != (PGResult{}) {
		t.Fatalf("failed final shape verification must return zero result, got %+v", result)
	}
}
