package postgres

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestRequireGooseSchemaVersion(t *testing.T) {
	tests := []struct {
		name      string
		rows      []fakeSchemaVersionRow
		wantError string
		wantCalls int
	}{
		{
			name: "version 57 applied",
			rows: []fakeSchemaVersionRow{
				{version: "57", applied: true},
				{err: pgx.ErrNoRows},
			},
			wantCalls: 2,
		},
		{
			name: "version 58 rollback history resolves to version 57",
			rows: []fakeSchemaVersionRow{
				{version: "57", applied: true},
				{err: pgx.ErrNoRows},
			},
			wantCalls: 2,
		},
		{
			name:      "version 56",
			rows:      []fakeSchemaVersionRow{{version: "56", applied: true}},
			wantError: "expected 57",
			wantCalls: 1,
		},
		{
			name:      "version 58",
			rows:      []fakeSchemaVersionRow{{version: "58", applied: true}},
			wantError: "expected 57",
			wantCalls: 1,
		},
		{
			name:      "version 57 unapplied",
			rows:      []fakeSchemaVersionRow{{version: "57", applied: false}},
			wantError: "not applied",
			wantCalls: 1,
		},
		{
			name:      "empty table",
			rows:      []fakeSchemaVersionRow{{err: pgx.ErrNoRows}},
			wantError: "no version record",
			wantCalls: 1,
		},
		{
			name: "newer applied version",
			rows: []fakeSchemaVersionRow{
				{version: "57", applied: true},
				{version: "58", applied: true},
			},
			wantError: "newer applied version 58",
			wantCalls: 2,
		},
		{
			name:      "missing table",
			rows:      []fakeSchemaVersionRow{{err: errors.New(`relation "goose_db_version" does not exist`)}},
			wantError: "query current goose schema version",
			wantCalls: 1,
		},
		{
			name: "newer version query error",
			rows: []fakeSchemaVersionRow{
				{version: "57", applied: true},
				{err: errors.New("synthetic query failure")},
			},
			wantError: "query newer applied goose schema version",
			wantCalls: 2,
		},
		{
			name:      "context timeout",
			rows:      []fakeSchemaVersionRow{{err: context.DeadlineExceeded}},
			wantError: "context deadline exceeded",
			wantCalls: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			querier := &fakeSchemaVersionQuerier{rows: append([]fakeSchemaVersionRow(nil), test.rows...)}
			err := requireGooseSchemaVersion(t.Context(), querier, 57)
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("requireGooseSchemaVersion() error = %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("requireGooseSchemaVersion() error = %v, want contains %q", err, test.wantError)
			}
			if got := len(querier.calls); got != test.wantCalls {
				t.Fatalf("QueryRow() calls = %d, want %d", got, test.wantCalls)
			}
			assertSchemaVersionQueries(t, querier.calls)
		})
	}
}

func TestRequireGooseSchemaVersionPreservesVersionParseError(t *testing.T) {
	querier := &fakeSchemaVersionQuerier{rows: []fakeSchemaVersionRow{{
		version: "not-a-version",
		applied: true,
	}}}

	err := requireGooseSchemaVersion(t.Context(), querier, 57)
	if err == nil {
		t.Fatal("requireGooseSchemaVersion() error = nil, want parse error")
	}
	var numberError *strconv.NumError
	if !errors.As(err, &numberError) {
		t.Fatalf("error = %v, want wrapped *strconv.NumError", err)
	}
	if !errors.Is(err, strconv.ErrSyntax) {
		t.Fatalf("error = %v, want wrapped strconv.ErrSyntax", err)
	}
}

func assertSchemaVersionQueries(t *testing.T, calls []fakeSchemaVersionQueryCall) {
	t.Helper()
	if len(calls) == 0 {
		return
	}
	const wantCurrentQuery = `WITH latest_versions AS (
	SELECT DISTINCT ON (version_id)
		id,
		version_id,
		is_applied
	FROM goose_db_version
	ORDER BY version_id, id DESC
)
SELECT version_id::text, is_applied
FROM latest_versions
WHERE is_applied = TRUE
ORDER BY id DESC
LIMIT 1`
	if calls[0].query != wantCurrentQuery {
		t.Fatalf("current query = %q, want %q", calls[0].query, wantCurrentQuery)
	}
	if len(calls[0].args) != 0 {
		t.Fatalf("current query args = %v, want none", calls[0].args)
	}
	if len(calls) < 2 {
		return
	}
	const wantNewerQuery = `WITH latest_versions AS (
	SELECT DISTINCT ON (version_id)
		id,
		version_id,
		is_applied
	FROM goose_db_version
	ORDER BY version_id, id DESC
)
SELECT version_id::text, is_applied
FROM latest_versions
WHERE version_id > $1 AND is_applied = TRUE
ORDER BY id DESC
LIMIT 1`
	if calls[1].query != wantNewerQuery {
		t.Fatalf("newer query = %q, want %q", calls[1].query, wantNewerQuery)
	}
	if want := []any{int64(57)}; !reflect.DeepEqual(calls[1].args, want) {
		t.Fatalf("newer query args = %#v, want %#v", calls[1].args, want)
	}
}

type fakeSchemaVersionQuerier struct {
	rows  []fakeSchemaVersionRow
	calls []fakeSchemaVersionQueryCall
}

type fakeSchemaVersionQueryCall struct {
	query string
	args  []any
}

func (q *fakeSchemaVersionQuerier) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	q.calls = append(q.calls, fakeSchemaVersionQueryCall{query: query, args: append([]any(nil), args...)})
	if len(q.rows) == 0 {
		return fakeSchemaVersionRow{err: errors.New("unexpected query")}
	}
	row := q.rows[0]
	q.rows = q.rows[1:]
	return row
}

type fakeSchemaVersionRow struct {
	version string
	applied bool
	err     error
}

func (r fakeSchemaVersionRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != 2 {
		return fmt.Errorf("scan destinations = %d, want 2", len(dest))
	}
	version, ok := dest[0].(*string)
	if !ok {
		return fmt.Errorf("version destination = %T, want *string", dest[0])
	}
	applied, ok := dest[1].(*bool)
	if !ok {
		return fmt.Errorf("applied destination = %T, want *bool", dest[1])
	}
	*version = r.version
	*applied = r.applied
	return nil
}
