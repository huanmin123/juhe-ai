package postgres

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
)

func TestListManagementPublicAPILogsBuildsAllFiltersAndMapsLookahead(t *testing.T) {
	startedAt := time.Date(2026, 7, 14, 8, 1, 2, 3, time.FixedZone("UTC+8", 8*60*60))
	endedAt := startedAt.Add(350 * time.Millisecond)
	createdAt := endedAt.Add(time.Millisecond)
	executor := &managementPublicAPILogListExecutorStub{
               rows: []managementPublicAPILogListRow{
                       {
                               ID:         "publog_1",
                               CreatedAt:  createdAt,
                               SourceName: pgtype.Text{String: "source one", Valid: true},
                               Method:     "POST",
                               Path:       "/__aipublic__/account/add",
                               Success:    false,
                               StatusCode: pgtype.Int4{Int32: 503, Valid: true},
                               DurationMs: pgtype.Int8{Int64: 350, Valid: true},
                               ClientIP:   pgtype.Text{String: "203.0.113.8", Valid: true},
                               TraceID:    pgtype.Text{String: "trace-public-1", Valid: true},
                       },
                       {
                               ID:        "publog_2",
                               Method:    "GET",
                               Path:      "/__aipublic__/group/list",
                               CreatedAt: createdAt,
                       },
                       {ID: "publog_lookahead"},
               },
	}
	statusCode := 503
	startFilter := time.Date(2026, 7, 14, 8, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	endFilter := startFilter.Add(time.Hour)

	result, err := listManagementPublicAPILogs(
		context.Background(),
		executor,
		port.ManagementPublicAPILogListInput{
			TraceID:     " trace-public- ",
			SourceRefID: " source_1 ",
			Path:        " /__aipublic__/account/add ",
			Result:      port.ManagementPublicAPILogResultFailed,
			StatusCode:  &statusCode,
			ClientIP:    " 203.0.113. ",
			StartAt:     startFilter,
			EndAt:       endFilter,
			Limit:       2,
			Offset:      5000,
		},
	)
	if err != nil {
		t.Fatalf("list management public API logs: %v", err)
	}
	if len(executor.calls) != 1 {
		t.Fatalf("list calls = %d, want 1", len(executor.calls))
	}
	call := executor.calls[0]
	wantArgs := []any{
		"trace-public-",
		"trace-public.",
		"source_1",
		"/__aipublic__/account/add",
		false,
		int32(503),
		"203.0.113.",
		"203.0.113/",
		startFilter.UTC(),
		endFilter.UTC(),
		int32(3),
		int32(998),
	}
	if !reflect.DeepEqual(call.args, wantArgs) {
		t.Fatalf("list args = %#v, want %#v", call.args, wantArgs)
	}
	for _, required := range []string{
		`pal.trace_id COLLATE "C" >= $1::text`,
		`pal.trace_id COLLATE "C" < $2::text`,
		`pal.source_ref_id = $3::text`,
		`pal.path = $4::text`,
		`pal.success = $5::boolean`,
		`pal.status_code = $6::integer`,
		`pal.client_ip COLLATE "C" >= $7::text`,
		`pal.client_ip COLLATE "C" < $8::text`,
		`pal.created_at >= $9::timestamptz`,
		`pal.created_at <= $10::timestamptz`,
		`ORDER BY pal.created_at DESC, pal.id DESC`,
		`LIMIT $11::integer`,
		`OFFSET $12::integer`,
	} {
		if !strings.Contains(call.query, required) {
			t.Fatalf("list SQL missing %q:\n%s", required, call.query)
		}
	}
	assertManagementPublicAPILogListSQLIsLightweight(t, call.query)

	if !result.HasMore || len(result.Items) != 2 {
		t.Fatalf("list result = %+v", result)
	}
	item := result.Items[0]
       if item.ID != "publog_1" ||
               item.TraceID == nil || *item.TraceID != "trace-public-1" ||
               item.SourceName == nil || *item.SourceName != "source one" ||
               item.Method != "POST" || item.Path != "/__aipublic__/account/add" ||
               item.ClientIP == nil || *item.ClientIP != "203.0.113.8" ||
               item.StatusCode == nil || *item.StatusCode != 503 || item.Success ||
               item.DurationMs == nil || *item.DurationMs != 350 ||
               !item.CreatedAt.Equal(createdAt.UTC()) {
               t.Fatalf("mapped item = %+v", item)
       }
       nullableItem := result.Items[1]
       if nullableItem.TraceID != nil || nullableItem.SourceName != nil || nullableItem.ClientIP != nil ||
               nullableItem.StatusCode != nil || nullableItem.DurationMs != nil {
               t.Fatalf("nullable fields must remain nil: %+v", nullableItem)
       }
}

func TestManagementPublicAPILogListQueryOnlyAddsWhitelistedFilters(t *testing.T) {
	tests := []struct {
		name      string
		input     port.ManagementPublicAPILogListInput
		wantSQL   []string
		wantArgs  []any
		forbidden []string
	}{
		{
			name: "no filters",
			input: port.ManagementPublicAPILogListInput{
				Result: port.ManagementPublicAPILogResultAll,
			},
			wantArgs: []any{int32(11), int32(4)},
			forbidden: []string{
				"WHERE ", "pal.trace_id", "pal.source_ref_id =", "pal.path =", "pal.success =",
				"pal.status_code =", "pal.client_ip", "pal.created_at >=", "pal.created_at <=",
			},
		},
		{
			name:     "success maps to bool",
			input:    port.ManagementPublicAPILogListInput{Result: port.ManagementPublicAPILogResultSuccess},
			wantSQL:  []string{"WHERE pal.success = $1::boolean"},
			wantArgs: []any{true, int32(11), int32(4)},
		},
		{
			name: "unknown result is ignored and values remain parameters",
			input: port.ManagementPublicAPILogListInput{
				Path:   `/safe' OR TRUE --`,
				Result: port.ManagementPublicAPILogResultFilter("unexpected"),
			},
			wantSQL:  []string{"WHERE pal.path = $1::text"},
			wantArgs: []any{`/safe' OR TRUE --`, int32(11), int32(4)},
		},
		{
			name: "non ECMAScript whitespace is a literal prefix and invalid status is ignored",
			input: port.ManagementPublicAPILogListInput{
				TraceID:    "\u0085",
				ClientIP:   "\u0085",
				StatusCode: intPointer(600),
			},
			wantSQL: []string{
				`pal.trace_id COLLATE "C" >= $1::text`,
				`pal.client_ip COLLATE "C" >= $3::text`,
			},
			wantArgs:  []any{"\u0085", "\u0086", "\u0085", "\u0086", int32(11), int32(4)},
			forbidden: []string{"pal.status_code ="},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			query, args := managementPublicAPILogListQuery(test.input, 11, 4)
			if !reflect.DeepEqual(args, test.wantArgs) {
				t.Fatalf("args = %#v, want %#v", args, test.wantArgs)
			}
			for _, required := range test.wantSQL {
				if !strings.Contains(query, required) {
					t.Fatalf("query missing %q:\n%s", required, query)
				}
			}
			whereSQL := managementPublicAPILogListWhereClause(query)
			for _, value := range test.forbidden {
				if strings.Contains(whereSQL, value) {
					t.Fatalf("query unexpectedly contains %q:\n%s", value, query)
				}
			}
			if strings.Contains(query, `/safe' OR TRUE --`) {
				t.Fatalf("filter value was interpolated into SQL:\n%s", query)
			}
			assertManagementPublicAPILogListSQLIsLightweight(t, query)
		})
	}
}

func intPointer(value int) *int {
	return &value
}

func TestListManagementPublicAPILogsClampsWindowAndReturnsNonNilEmptyItems(t *testing.T) {
	tests := []struct {
		name       string
		input      port.ManagementPublicAPILogListInput
		wantWindow []any
	}{
		{
			name:       "default limit and negative offset",
			input:      port.ManagementPublicAPILogListInput{Offset: -1},
			wantWindow: []any{int32(51), int32(0)},
		},
		{
			name:       "maximum limit and upper window",
			input:      port.ManagementPublicAPILogListInput{Limit: 1000, Offset: 5000},
			wantWindow: []any{int32(101), int32(900)},
		},
		{
			name:       "small page leaves lookahead inside window",
			input:      port.ManagementPublicAPILogListInput{Limit: 1, Offset: 1000},
			wantWindow: []any{int32(2), int32(999)},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := &managementPublicAPILogListExecutorStub{}
			result, err := listManagementPublicAPILogs(context.Background(), executor, test.input)
			if err != nil {
				t.Fatalf("list management public API logs: %v", err)
			}
			if result.Items == nil || result.HasMore {
				t.Fatalf("empty result = %+v", result)
			}
			if got := executor.calls[0].args; !reflect.DeepEqual(got, test.wantWindow) {
				t.Fatalf("window args = %#v, want %#v", got, test.wantWindow)
			}
		})
	}
}

func TestListManagementPublicAPILogsWrapsExecutorError(t *testing.T) {
	want := errors.New("query failed")
	executor := &managementPublicAPILogListExecutorStub{err: want}

	_, err := listManagementPublicAPILogs(
		context.Background(),
		executor,
		port.ManagementPublicAPILogListInput{},
	)
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "list management public API logs") {
		t.Fatalf("list error = %v", err)
	}
}

func TestGetManagementPublicAPILogUsesExactIDAndReadsPayload(t *testing.T) {
	createdAt := time.Date(2026, 7, 14, 8, 1, 2, 3, time.FixedZone("UTC+8", 8*60*60))
	executor := &managementPublicAPILogDetailExecutorStub{
		row: managementPublicAPILogDetailRow{
			managementPublicAPILogRow: managementPublicAPILogRow{
				ID:                    " publog_exact ",
				TraceID:               pgtype.Text{String: "trace-1", Valid: true},
				Method:                "POST",
				Path:                  "/__aipublic__/group/add",
				StatusCode:            pgtype.Int4{Int32: 201, Valid: true},
				Success:               true,
				DurationMs:            pgtype.Int8{Int64: 12, Valid: true},
				RequestCaptureStatus:  string(port.PublicAPILogCaptureComplete),
				ResponseCaptureStatus: string(port.PublicAPILogCaptureComplete),
				StartedAt:             createdAt,
				EndedAt:               createdAt.Add(12 * time.Millisecond),
				CreatedAt:             createdAt.Add(13 * time.Millisecond),
			},
			RequestDataJSON:  `{"body":{"name":"group"}}`,
			ResponseDataJSON: `{"statusCode":201}`,
		},
	}

	detail, found, err := getManagementPublicAPILog(context.Background(), executor, " publog_exact ")
	if err != nil {
		t.Fatalf("get management public API log: %v", err)
	}
	if !found || len(executor.calls) != 1 || executor.calls[0].id != " publog_exact " {
		t.Fatalf("detail found/calls = %v / %+v", found, executor.calls)
	}
	query := executor.calls[0].query
	for _, required := range []string{
		"pal.request_data_json",
		"pal.response_data_json",
		"WHERE pal.id = $1::text",
		"LIMIT 1",
	} {
		if !strings.Contains(query, required) {
			t.Fatalf("detail SQL missing %q:\n%s", required, query)
		}
	}
	if strings.Contains(strings.ToUpper(query), "SELECT *") {
		t.Fatalf("detail SQL must explicitly select columns:\n%s", query)
	}
	if detail.ID != " publog_exact " || detail.TraceID == nil || *detail.TraceID != "trace-1" ||
		detail.StatusCode == nil || *detail.StatusCode != 201 || !detail.Success ||
		detail.RequestDataJSON != `{"body":{"name":"group"}}` ||
		detail.ResponseDataJSON != `{"statusCode":201}` ||
		!detail.CreatedAt.Equal(createdAt.Add(13*time.Millisecond).UTC()) {
		t.Fatalf("detail = %+v", detail)
	}
}

func TestGetManagementPublicAPILogReturnsNotFound(t *testing.T) {
	executor := &managementPublicAPILogDetailExecutorStub{err: pgx.ErrNoRows}

	detail, found, err := getManagementPublicAPILog(context.Background(), executor, "missing")
	if err != nil {
		t.Fatalf("get missing management public API log: %v", err)
	}
	if found || detail != (port.ManagementPublicAPILogDetail{}) {
		t.Fatalf("missing detail = %+v, found = %v", detail, found)
	}
}

func TestGetManagementPublicAPILogWrapsExecutorError(t *testing.T) {
	want := errors.New("detail query failed")
	executor := &managementPublicAPILogDetailExecutorStub{err: want}

	_, _, err := getManagementPublicAPILog(context.Background(), executor, "publog_1")
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "get management public API log") {
		t.Fatalf("detail error = %v", err)
	}
}

func assertManagementPublicAPILogListSQLIsLightweight(t *testing.T, query string) {
	t.Helper()
	for _, forbidden := range []*regexp.Regexp{
		regexp.MustCompile(`(?i)request_data_json`),
		regexp.MustCompile(`(?i)response_data_json`),
		regexp.MustCompile(`(?i)token_id`),
		regexp.MustCompile(`(?i)token_name`),
		regexp.MustCompile(`(?i)user_agent`),
		regexp.MustCompile(`(?i)request_size_bytes`),
		regexp.MustCompile(`(?i)error_message`),
		regexp.MustCompile(`(?i)token_id`),
		regexp.MustCompile(`(?i)token_name`),
		regexp.MustCompile(`(?i)user_agent`),
		regexp.MustCompile(`(?i)request_size_bytes`),
		regexp.MustCompile(`(?i)error_message`),
		regexp.MustCompile(`(?i)SELECT\s+\*`),
		regexp.MustCompile(`(?i)\bOR\b`),
		regexp.MustCompile(`(?i)\bCOUNT\s*\(`),
	} {
		if forbidden.MatchString(query) {
			t.Fatalf("list SQL matches forbidden %q:\n%s", forbidden, query)
		}
	}
	if !strings.Contains(query, "ORDER BY pal.created_at DESC, pal.id DESC") {
		t.Fatalf("list SQL has unstable ordering:\n%s", query)
	}
}

func managementPublicAPILogListWhereClause(query string) string {
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

type managementPublicAPILogListCall struct {
	query string
	args  []any
}

type managementPublicAPILogListExecutorStub struct {
	rows  []managementPublicAPILogListRow
	err   error
	calls []managementPublicAPILogListCall
}

func (s *managementPublicAPILogListExecutorStub) QueryManagementPublicAPILogs(
	_ context.Context,
	query string,
	args ...any,
) ([]managementPublicAPILogListRow, error) {
	s.calls = append(s.calls, managementPublicAPILogListCall{
		query: query,
		args:  append([]any(nil), args...),
	})
	return s.rows, s.err
}

type managementPublicAPILogDetailCall struct {
	query string
	id    string
}

type managementPublicAPILogDetailExecutorStub struct {
	row   managementPublicAPILogDetailRow
	err   error
	calls []managementPublicAPILogDetailCall
}

func (s *managementPublicAPILogDetailExecutorStub) QueryManagementPublicAPILog(
	_ context.Context,
	query string,
	id string,
) (managementPublicAPILogDetailRow, error) {
	s.calls = append(s.calls, managementPublicAPILogDetailCall{query: query, id: id})
	return s.row, s.err
}

var _ managementPublicAPILogListExecutor = (*managementPublicAPILogListExecutorStub)(nil)
var _ managementPublicAPILogDetailExecutor = (*managementPublicAPILogDetailExecutorStub)(nil)
