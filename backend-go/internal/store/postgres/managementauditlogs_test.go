package postgres

import (
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementAuditErrorGroupListQueryUsesSummaryTableFilters(t *testing.T) {
	status := 503
	query, args := managementAuditErrorGroupListQuery(port.ManagementAuditErrorGroupListInput{
		Path: "/v1/responses", Model: "gpt-5", StatusCode: &status,
		SystemAccountID: "sys_1", APIKeyID: "key_1", GroupID: "group_1", AccountID: "account_1",
	}, 101, 200)
	wantArgs := []any{"/v1/responses", "gpt-5", int32(503), "sys_1", "key_1", "group_1", "account_1", int32(101), int32(200)}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", args, wantArgs)
	}
	for _, want := range []string{
		`aeg.path = $1::text`, `aeg.model = $2::text`, `aeg.status_code = $3::integer`,
		`aeg.system_account_id = $4::text`, `aeg.api_key_id = $5::text`,
		`aeg.group_id = $6::text`, `aeg.account_id = $7::text`,
		`ORDER BY aeg.updated_at DESC, aeg.id DESC`, `LIMIT $8::integer`, `OFFSET $9::integer`,
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("query missing %q:\n%s", want, query)
		}
	}
	for _, forbidden := range []string{"COUNT(", "audit_logs", "audit_payload", "blob", "body", "header", "SELECT aeg.*"} {
		if strings.Contains(query, forbidden) {
			t.Fatalf("query contains %q:\n%s", forbidden, query)
		}
	}
}

func TestManagementAuditErrorGroupListQueryUsesECMAScriptTrim(t *testing.T) {
	model := "\u0085gpt-5\u0085"
	_, args := managementAuditErrorGroupListQuery(port.ManagementAuditErrorGroupListInput{
		Path: "\uFEFF/v1/responses\uFEFF", Model: model, SystemAccountID: "\uFEFFsys_1\uFEFF",
	}, 101, 0)
	wantArgs := []any{"/v1/responses", model, "sys_1", int32(101), int32(0)}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args = %#v, want FEFF trimmed and U+0085 preserved as %#v", args, wantArgs)
	}
}

func TestManagementAuditErrorGroupSelectProjectsSummaryWithNames(t *testing.T) {
	query := managementAuditErrorGroupDetailQuery()
	for _, want := range []string{
		"aeg.id, aeg.fingerprint, aeg.window_started_at, aeg.window_ended_at",
		"aeg.system_account_id, sa.display_name, aeg.api_key_id, ak.name",
		"aeg.group_id, grp.name, aeg.account_id, acc.name, aeg.provider_code",
		"aeg.path, aeg.model, aeg.status_code, aeg.error_phase, aeg.error_code, aeg.error_type",
		"aeg.request_fingerprint, aeg.error_fingerprint, aeg.count",
		"aeg.first_event_id, aeg.last_event_id, aeg.sample_event_id, aeg.last_message",
		"aeg.created_at, aeg.updated_at",
		"FROM juhe_dataset.audit_error_groups AS aeg",
		"LEFT JOIN juhe_business.system_accounts AS sa ON sa.id = aeg.system_account_id",
		"LEFT JOIN juhe_business.api_keys AS ak ON ak.id = aeg.api_key_id",
		"LEFT JOIN juhe_business.groups AS grp ON grp.id = aeg.group_id",
		"LEFT JOIN juhe_business.accounts AS acc ON acc.id = aeg.account_id",
		"WHERE aeg.id = $1::text",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("query missing %q:\n%s", want, query)
		}
	}
	if !strings.HasPrefix(query, managementAuditErrorGroupSelect) {
		t.Fatalf("detail query does not reuse managementAuditErrorGroupSelect:\n%s", query)
	}
}

func TestManagementAuditErrorGroupMapperCoversOptionalFields(t *testing.T) {
	text := func(value string) pgtype.Text { return pgtype.Text{String: value, Valid: true} }
	row := managementAuditErrorGroupRow{
		ID: "err_group_1", Fingerprint: "fingerprint_1",
		WindowStartedAt: "2026-07-21T00:00:00.000Z", WindowEndedAt: "2026-07-21T01:00:00.000Z",
		CreatedAt: "2026-07-21T00:00:01.000Z", UpdatedAt: "2026-07-21T01:00:01.000Z",
		SystemAccountID: text("sys_1"), SystemAccountName: text("System One"),
		APIKeyID: text("key_1"), APIKeyName: text("Key One"),
		GroupID: text("group_1"), GroupName: text("Group One"),
		AccountID: text("account_1"), AccountName: text("Account One"), ProviderCode: text("openai"),
		Path: text("/v1/responses"), Model: text("gpt-5"), ErrorPhase: text("upstream"),
		ErrorCode: text("service_unavailable"), ErrorType: text("upstream_error"),
		RequestFingerprint: text("request_fp"), ErrorFingerprint: text("error_fp"),
		FirstEventID: text("event_1"), LastEventID: text("event_3"), SampleEventID: text("event_2"),
		LastMessage: text("upstream unavailable"), StatusCode: pgtype.Int4{Int32: 503, Valid: true}, Count: 3,
	}

	got := managementAuditErrorGroup(row)
	if got.ID != row.ID || got.Fingerprint != row.Fingerprint || got.Count != 3 {
		t.Fatalf("identity/count = %#v/%#v/%d", got.ID, got.Fingerprint, got.Count)
	}
	if got.WindowStartedAt != row.WindowStartedAt || got.WindowEndedAt != row.WindowEndedAt || got.CreatedAt != row.CreatedAt || got.UpdatedAt != row.UpdatedAt {
		t.Fatalf("times = %#v, want row times", got)
	}
	assertManagementAuditErrorGroupStringPointer(t, "system account id", got.SystemAccountID, "sys_1")
	assertManagementAuditErrorGroupStringPointer(t, "system account name", got.SystemAccountName, "System One")
	assertManagementAuditErrorGroupStringPointer(t, "api key id", got.APIKeyID, "key_1")
	assertManagementAuditErrorGroupStringPointer(t, "api key name", got.APIKeyName, "Key One")
	assertManagementAuditErrorGroupStringPointer(t, "group id", got.GroupID, "group_1")
	assertManagementAuditErrorGroupStringPointer(t, "group name", got.GroupName, "Group One")
	assertManagementAuditErrorGroupStringPointer(t, "account id", got.AccountID, "account_1")
	assertManagementAuditErrorGroupStringPointer(t, "account name", got.AccountName, "Account One")
	assertManagementAuditErrorGroupStringPointer(t, "provider code", got.ProviderCode, "openai")
	assertManagementAuditErrorGroupStringPointer(t, "path", got.Path, "/v1/responses")
	assertManagementAuditErrorGroupStringPointer(t, "model", got.Model, "gpt-5")
	assertManagementAuditErrorGroupStringPointer(t, "error phase", got.ErrorPhase, "upstream")
	assertManagementAuditErrorGroupStringPointer(t, "error code", got.ErrorCode, "service_unavailable")
	assertManagementAuditErrorGroupStringPointer(t, "error type", got.ErrorType, "upstream_error")
	assertManagementAuditErrorGroupStringPointer(t, "request fingerprint", got.RequestFingerprint, "request_fp")
	assertManagementAuditErrorGroupStringPointer(t, "error fingerprint", got.ErrorFingerprint, "error_fp")
	assertManagementAuditErrorGroupStringPointer(t, "first event id", got.FirstEventID, "event_1")
	assertManagementAuditErrorGroupStringPointer(t, "last event id", got.LastEventID, "event_3")
	assertManagementAuditErrorGroupStringPointer(t, "sample event id", got.SampleEventID, "event_2")
	assertManagementAuditErrorGroupStringPointer(t, "last message", got.LastMessage, "upstream unavailable")
	if got.StatusCode == nil || *got.StatusCode != 503 {
		t.Fatalf("status code = %#v, want 503", got.StatusCode)
	}

	empty := managementAuditErrorGroup(managementAuditErrorGroupRow{})
	for name, value := range map[string]*string{
		"system account id": empty.SystemAccountID, "system account name": empty.SystemAccountName,
		"api key id": empty.APIKeyID, "api key name": empty.APIKeyName,
		"group id": empty.GroupID, "group name": empty.GroupName,
		"account id": empty.AccountID, "account name": empty.AccountName, "provider code": empty.ProviderCode,
		"path": empty.Path, "model": empty.Model, "error phase": empty.ErrorPhase, "error code": empty.ErrorCode,
		"error type": empty.ErrorType, "request fingerprint": empty.RequestFingerprint,
		"error fingerprint": empty.ErrorFingerprint, "first event id": empty.FirstEventID,
		"last event id": empty.LastEventID, "sample event id": empty.SampleEventID, "last message": empty.LastMessage,
	} {
		if value != nil {
			t.Fatalf("empty %s = %#v, want nil", name, *value)
		}
	}
	if empty.StatusCode != nil {
		t.Fatalf("empty status code = %d, want nil", *empty.StatusCode)
	}
}

func assertManagementAuditErrorGroupStringPointer(t *testing.T, name string, got *string, want string) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("%s = %#v, want %q", name, got, want)
	}
}

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
		"WHERE aeg.id = $1::text",
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
