package postgres

import (
	"context"
	"errors"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func TestListManagementRuntimeLogsBuildsFilteredQueryAndMapsLookahead(t *testing.T) {
	executor := &runtimeLogListExecutorStub{
		rows: []runtimeLogListRow{
			{
				ID:           "runtime_1",
				Time:         "2026-07-14T08:02:03.004Z",
				Level:        "warn",
				TraceID:      pgtype.Text{String: "trace-1", Valid: true},
				Event:        pgtype.Text{String: "worker.retry", Valid: true},
				Message:      pgtype.Text{String: "needle%_\\", Valid: true},
				ErrorMessage: pgtype.Text{String: "retry failed", Valid: true},
				CreatedAt:    "2026-07-14T08:02:03.005Z",
			},
			{ID: "runtime_2", Time: "2026-07-14T08:02:02.004Z", Level: "info", CreatedAt: "2026-07-14T08:02:02.005Z"},
			{ID: "runtime_probe"},
		},
	}

	result, err := listManagementRuntimeLogs(
		context.Background(),
		executor,
		port.ManagementRuntimeLogListInput{
			TraceID: "\uFEFFtrace% ",
			Level:   " WARN ",
			Event:   " worker.retry ",
			Keyword: " needle%_\\ ",
			StartAt: " 2026-07-14T09:00:00.000Z ",
			EndAt:   " 2026-07-14T08:00:00.000Z ",
			Limit:   2,
			Offset:  5000,
		},
	)
	if err != nil {
		t.Fatalf("list management runtime logs: %v", err)
	}
	if len(executor.calls) != 1 {
		t.Fatalf("list calls = %d", len(executor.calls))
	}
	call := executor.calls[0]
	wantArgs := []any{
		"trace%",
		"trace&",
		"warn",
		"worker.retry",
		"2026-07-14T08:00:00.000Z",
		"2026-07-14T09:00:00.000Z",
		`%needle\%\_\\%`,
		int32(3),
		int32(998),
	}
	if !reflect.DeepEqual(call.args, wantArgs) {
		t.Fatalf("list args = %#v, want %#v", call.args, wantArgs)
	}
	for _, required := range []string{
		`rl.trace_id COLLATE "C" >= $1::text`,
		`rl.trace_id COLLATE "C" < $2::text`,
		`rl.level = $3::text`,
		`rl.event = $4::text`,
		`rl.time >= $5::text`,
		`rl.time <= $6::text`,
		`rl.message LIKE $7::text ESCAPE '\'`,
		`ORDER BY rl.time DESC, rl.id DESC`,
		`LIMIT $8::int`,
		`OFFSET $9::int`,
	} {
		if !strings.Contains(call.query, required) {
			t.Fatalf("runtime log list SQL missing %q:\n%s", required, call.query)
		}
	}
	assertRuntimeLogListSQLIsLightweight(t, call.query)

	if !result.HasMore || len(result.Items) != 2 {
		t.Fatalf("list result = %+v", result)
	}
	item := result.Items[0]
	if item.ID != "runtime_1" ||
		item.Time != "2026-07-14T08:02:03.004Z" ||
		item.TraceID == nil || *item.TraceID != "trace-1" ||
		item.Event == nil || *item.Event != "worker.retry" ||
		item.Message == nil || *item.Message != "needle%_\\" ||
		item.ErrorMessage == nil || *item.ErrorMessage != "retry failed" ||
		item.CreatedAt != "2026-07-14T08:02:03.005Z" {
		t.Fatalf("mapped item = %+v", item)
	}
	if result.Items[1].TraceID != nil || result.Items[1].Event != nil ||
		result.Items[1].Message != nil || result.Items[1].ErrorMessage != nil {
		t.Fatalf("nullable list fields must remain nil: %+v", result.Items[1])
	}
}

func TestRuntimeLogListQueryOnlyAddsPresentFilters(t *testing.T) {
	tests := []struct {
		name           string
		input          port.ManagementRuntimeLogListInput
		wantWhere      []string
		forbiddenWhere []string
		wantArgs       []any
	}{
		{
			name:  "no filters",
			input: port.ManagementRuntimeLogListInput{Level: "unknown"},
			forbiddenWhere: []string{
				"WHERE ", "rl.trace_id", "rl.level =", "rl.event =", "rl.time >=", "rl.time <=", "rl.message LIKE",
			},
			wantArgs: []any{int32(11), int32(4)},
		},
		{
			name:      "trace only",
			input:     port.ManagementRuntimeLogListInput{TraceID: " trace-9 "},
			wantWhere: []string{`rl.trace_id COLLATE "C" >= $1::text`, `rl.trace_id COLLATE "C" < $2::text`},
			forbiddenWhere: []string{
				"rl.level =", "rl.event =", "rl.time >=", "rl.time <=", "rl.message LIKE",
			},
			wantArgs: []any{"trace-9", "trace-:", int32(11), int32(4)},
		},
		{
			name:      "level and event",
			input:     port.ManagementRuntimeLogListInput{Level: " ERROR ", Event: " gateway.failed "},
			wantWhere: []string{`rl.level = $1::text`, `rl.event = $2::text`},
			forbiddenWhere: []string{
				"rl.trace_id", "rl.time >=", "rl.time <=", "rl.message LIKE",
			},
			wantArgs: []any{"error", "gateway.failed", int32(11), int32(4)},
		},
		{
			name:      "start only",
			input:     port.ManagementRuntimeLogListInput{StartAt: " 2026-07-14T08:00:00.000Z "},
			wantWhere: []string{`rl.time >= $1::text`},
			forbiddenWhere: []string{
				"rl.trace_id", "rl.level =", "rl.event =", "rl.time <=", "rl.message LIKE",
			},
			wantArgs: []any{"2026-07-14T08:00:00.000Z", int32(11), int32(4)},
		},
		{
			name:      "end and keyword",
			input:     port.ManagementRuntimeLogListInput{EndAt: " 2026-07-14T09:00:00.000Z ", Keyword: ` 50%_\ `},
			wantWhere: []string{`rl.time <= $1::text`, `rl.message LIKE $2::text ESCAPE '\'`},
			forbiddenWhere: []string{
				"rl.trace_id", "rl.level =", "rl.event =", "rl.time >=",
			},
			wantArgs: []any{"2026-07-14T09:00:00.000Z", `%50\%\_\\%`, int32(11), int32(4)},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			query, args := runtimeLogListQuery(test.input, 11, 4)
			whereSQL := runtimeLogListWhereClause(query)
			if !reflect.DeepEqual(args, test.wantArgs) {
				t.Fatalf("args = %#v, want %#v", args, test.wantArgs)
			}
			for _, required := range test.wantWhere {
				if !strings.Contains(query, required) {
					t.Fatalf("query missing %q:\n%s", required, query)
				}
			}
			for _, forbidden := range test.forbiddenWhere {
				if strings.Contains(whereSQL, forbidden) {
					t.Fatalf("query unexpectedly contains %q:\n%s", forbidden, query)
				}
			}
			if !strings.Contains(query, "ORDER BY rl.time DESC, rl.id DESC\nLIMIT $") ||
				!strings.Contains(query, "::int\nOFFSET $") {
				t.Fatalf("query ordering/window is unstable:\n%s", query)
			}
			assertRuntimeLogListSQLIsLightweight(t, query)
		})
	}
}

func runtimeLogListWhereClause(query string) string {
	start := strings.Index(query, "\nWHERE ")
	if start < 0 {
		return ""
	}
	end := strings.Index(query[start:], "\nORDER BY ")
	if end < 0 {
		return query[start:]
	}
	return query[start : start+end]
}

func TestListManagementRuntimeLogsClampsWindowAndReturnsNonNilEmptyItems(t *testing.T) {
	executor := &runtimeLogListExecutorStub{}

	result, err := listManagementRuntimeLogs(
		context.Background(),
		executor,
		port.ManagementRuntimeLogListInput{Limit: 1000, Offset: -1},
	)
	if err != nil {
		t.Fatalf("list management runtime logs: %v", err)
	}
	if result.Items == nil || result.HasMore {
		t.Fatalf("empty list result = %+v", result)
	}
	if want := []any{int32(101), int32(0)}; !reflect.DeepEqual(executor.calls[0].args, want) {
		t.Fatalf("bounded args = %#v, want %#v", executor.calls[0].args, want)
	}
}

func TestListManagementRuntimeLogsWrapsExecutorError(t *testing.T) {
	want := errors.New("query failed")
	executor := &runtimeLogListExecutorStub{err: want}

	_, err := listManagementRuntimeLogs(context.Background(), executor, port.ManagementRuntimeLogListInput{})
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "list runtime logs") {
		t.Fatalf("list error = %v", err)
	}
}

func TestGetManagementRuntimeLogReadsRawJSONOnlyOnDetail(t *testing.T) {
	q := &runtimeLogQueriesStub{
		detail: postgresqueries.GetRuntimeLogDetailRow{
			ID:        "runtime_1",
			Time:      "2026-07-14T08:02:03.004Z",
			Level:     "error",
			Message:   pgtype.Text{String: "failed", Valid: true},
			CreatedAt: "2026-07-14T08:02:03.005Z",
			RawJson:   `{"level":50,"msg":"failed"}`,
		},
	}

	detail, found, err := getManagementRuntimeLog(context.Background(), q, "\uFEFFruntime_1 ")
	if err != nil {
		t.Fatalf("get management runtime log: %v", err)
	}
	if !found || len(q.detailCalls) != 1 || q.detailCalls[0] != "runtime_1" {
		t.Fatalf("detail found/calls = %v / %+v", found, q.detailCalls)
	}
	if detail.ID != "runtime_1" || detail.Message == nil || *detail.Message != "failed" ||
		detail.RawJSON != `{"level":50,"msg":"failed"}` {
		t.Fatalf("detail = %+v", detail)
	}
}

func TestGetManagementRuntimeLogReturnsNotFound(t *testing.T) {
	q := &runtimeLogQueriesStub{detailErr: pgx.ErrNoRows}

	detail, found, err := getManagementRuntimeLog(context.Background(), q, "missing")
	if err != nil {
		t.Fatalf("get missing management runtime log: %v", err)
	}
	if found || detail != (port.ManagementRuntimeLog{}) {
		t.Fatalf("missing detail = %+v, found = %v", detail, found)
	}
}

func TestGetManagementRuntimeLogWrapsQueryError(t *testing.T) {
	want := errors.New("detail failed")
	q := &runtimeLogQueriesStub{detailErr: want}

	_, _, err := getManagementRuntimeLog(context.Background(), q, "runtime_1")
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "get runtime log detail") {
		t.Fatalf("detail error = %v", err)
	}
}

func TestRuntimeLogSQLSourceKeepsListOutOfSQLCAndDetailByID(t *testing.T) {
	source, err := os.ReadFile("queries/w6_runtime_logs.sql")
	if err != nil {
		t.Fatalf("read runtime log query: %v", err)
	}
	sql := string(source)
	if strings.Contains(sql, "ListRuntimeLogs") {
		t.Fatalf("runtime log list must use dynamic repository SQL, not sqlc:\n%s", sql)
	}
	if !strings.Contains(sql, "-- name: GetRuntimeLogDetail :one") ||
		!strings.Contains(sql, "rl.raw_json") ||
		!strings.Contains(sql, "WHERE rl.id = sqlc.arg(id)::text") {
		t.Fatalf("runtime log detail SQL must read raw_json by id:\n%s", sql)
	}
}

func assertRuntimeLogListSQLIsLightweight(t *testing.T, query string) {
	t.Helper()
	forbidden := []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bOR\b`),
		regexp.MustCompile(`(?i)raw_json`),
		regexp.MustCompile(`(?i)\bCOUNT\s*\(`),
	}
	for _, pattern := range forbidden {
		if pattern.MatchString(query) {
			t.Fatalf("runtime log list SQL matches forbidden %q:\n%s", pattern, query)
		}
	}
}

type runtimeLogListCall struct {
	query string
	args  []any
}

type runtimeLogListExecutorStub struct {
	rows  []runtimeLogListRow
	err   error
	calls []runtimeLogListCall
}

func (s *runtimeLogListExecutorStub) QueryRuntimeLogs(
	_ context.Context,
	query string,
	args ...any,
) ([]runtimeLogListRow, error) {
	s.calls = append(s.calls, runtimeLogListCall{
		query: query,
		args:  append([]any(nil), args...),
	})
	return s.rows, s.err
}

type runtimeLogQueriesStub struct {
	detail      postgresqueries.GetRuntimeLogDetailRow
	detailErr   error
	detailCalls []string
}

func (s *runtimeLogQueriesStub) GetRuntimeLogDetail(
	_ context.Context,
	id string,
) (postgresqueries.GetRuntimeLogDetailRow, error) {
	s.detailCalls = append(s.detailCalls, id)
	return s.detail, s.detailErr
}

var _ runtimeLogListExecutor = (*runtimeLogListExecutorStub)(nil)
var _ runtimeLogQueries = (*runtimeLogQueriesStub)(nil)
