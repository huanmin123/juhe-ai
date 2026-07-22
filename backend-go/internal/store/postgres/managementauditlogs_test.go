package postgres

import (
	"reflect"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementAuditLogListQueryMatchesNodeFiltersAndStaysLightweight(t *testing.T) {
	status := 503
	start := "2026-07-21T00:00:00.000Z"
	end := "2026-07-21T23:59:59.999Z"
	query, args := managementAuditLogListQuery(port.ManagementAuditLogListInput{
		TraceID: "trace_", ErrorGroupID: "err_1", Outcome: "upstream_failed", StatusCode: &status,
		Path: "/v1/responses", Model: "gpt-5", SystemAccountID: "sys_1", APIKeyID: "key_1",
		GroupID: "group_1", AccountID: "account_1", ClientIP: "203.0.113.", StartAt: start, EndAt: end,
		TrafficSource: "gateway",
	}, 101, 200)
	wantArgs := []any{"trace_", "trace`", "/v1/responses", "gpt-5", "203.0.113.", "203.0.113/", "upstream_failed", int32(503), "gateway", start, end, "sys_1", "key_1", "group_1", "account_1", "err_1", int32(101), int32(200)}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", args, wantArgs)
	}
	for _, want := range []string{
		`al.trace_id COLLATE "C" >= $1::text`, `al.client_ip COLLATE "C" < $6::text`,
		`al.path = $3::text`, `al.model = $4::text`, `al.audit_outcome = $7::text`,
		`al.final_status_code = $8::integer`, `al.created_at <= $11::text`,
		`ORDER BY al.created_at DESC, al.id DESC`, `LIMIT $17::integer`, `OFFSET $18::integer`,
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("query missing %q:\n%s", want, query)
		}
	}
	for _, forbidden := range []string{"COUNT(", "audit_payload", "body_blob", "upstream_model =", "pricing_model =", "SELECT al.*"} {
		if strings.Contains(query, forbidden) {
			t.Fatalf("query contains %q:\n%s", forbidden, query)
		}
	}
}

func TestManagementAuditLogListQueryPreservesNonECMAScriptWhitespace(t *testing.T) {
	model := "\u0085gpt-5\u0085"
	_, args := managementAuditLogListQuery(port.ManagementAuditLogListInput{Model: model}, 101, 0)
	if len(args) < 1 || args[0] != model {
		t.Fatalf("model arg = %#v, want non-ECMAScript whitespace preserved", args)
	}
}

func TestManagementAuditLogDetailQueriesReadMetadataOnly(t *testing.T) {
	queries := []string{
		managementAuditLogDetailQuery(),
		managementAuditLogAttemptsQuery(),
		managementAuditLogPayloadSummariesQuery(),
		managementAuditErrorGroupDetailQuery(),
	}
	for _, required := range []string{
		"WHERE al.id = $1::text",
		"ORDER BY attempts.attempt_index ASC, attempts.id ASC",
		"ORDER BY refs.sequence_index ASC, refs.id ASC",
		"WHERE groups.id = $1::text",
	} {
		found := false
		for _, query := range queries {
			found = found || strings.Contains(query, required)
		}
		if !found {
			t.Fatalf("queries missing %q: %#v", required, queries)
		}
	}
	for _, forbidden := range []string{"body_bytes", "headers_bytes", "pg_read_binary_file", "audit_log_hot"} {
		for _, query := range queries {
			if strings.Contains(query, forbidden) {
				t.Fatalf("query contains %q:\n%s", forbidden, query)
			}
		}
	}
}

func TestManagementAuditLogsByIDsQueryIsBoundedAndParameterized(t *testing.T) {
	query := managementAuditLogsByIDsQuery()
	for _, want := range []string{"WHERE al.id = ANY($1::text[])", "ORDER BY al.created_at DESC, al.id DESC", "LIMIT 100"} {
		if !strings.Contains(query, want) {
			t.Fatalf("query missing %q:\n%s", want, query)
		}
	}
	for _, forbidden := range []string{"audit_payload_refs", "body_bytes", "COUNT("} {
		if strings.Contains(query, forbidden) {
			t.Fatalf("query contains %q:\n%s", forbidden, query)
		}
	}
}
