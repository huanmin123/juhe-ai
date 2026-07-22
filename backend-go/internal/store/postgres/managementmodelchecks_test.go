package postgres

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementModelCheckActiveQueryIsActorOwnedAndStable(t *testing.T) {
	query := managementModelCheckActiveQuery()
	assertManagementModelCheckReadOnlySQL(t, query)
	for _, fragment := range []string{
		"FROM juhe_dataset.model_check_runs AS mcr",
		"mcr.actor_system_account_id = $1::text",
		"mcr.status = 'running'",
		"ORDER BY mcr.created_at DESC, mcr.id DESC",
		"LIMIT 1",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("active query missing %q:\n%s", fragment, query)
		}
	}
	if strings.Contains(query, "WHERE mcr.system_account_id = $1") || strings.Contains(query, "AND mcr.system_account_id = $1") {
		t.Fatalf("active query is resource-owned instead of actor-owned:\n%s", query)
	}
	if strings.Contains(query, "request_summary_json") || strings.Contains(query, "result_summary_json") {
		t.Fatalf("active summary query reads report summary JSON:\n%s", query)
	}
}

func TestManagementModelCheckListQueryUsesBoundedParameterizedFilters(t *testing.T) {
	query, args := managementModelCheckListQuery(port.ManagementModelCheckRunListInput{
		SystemAccountID: "sys_owner", TargetType: "account", TargetID: "acct_1", Model: "gpt-5.6-sol",
		Level: "likely", Status: "completed", StartAt: "2026-07-01", EndAt: "2026-07-22",
		Limit: 100, Offset: 900,
	})
	assertManagementModelCheckReadOnlySQL(t, query)
	for _, fragment := range []string{
		"mcr.system_account_id = $1::text",
		"mcr.target_type = $2::text",
		"mcr.target_id = $3::text",
		"mcr.model = $4::text",
		"mcr.level = $5::text",
		"mcr.status = $6::text",
		"mcr.created_at >= $7::text",
		"mcr.created_at <= $8::text",
		"ORDER BY mcr.created_at DESC, mcr.id DESC",
		"LIMIT $9 OFFSET $10",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("list query missing %q:\n%s", fragment, query)
		}
	}
	wantArgs := []any{"sys_owner", "account", "acct_1", "gpt-5.6-sol", "likely", "completed", "2026-07-01", "2026-07-22", 101, 900}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", args, wantArgs)
	}
	if strings.Contains(query, "request_summary_json") || strings.Contains(query, "result_summary_json") {
		t.Fatalf("list query reads summary JSON:\n%s", query)
	}
	if strings.Contains(strings.ToUpper(query), "COUNT(") {
		t.Fatalf("list query performs exact count:\n%s", query)
	}
}

func TestManagementModelCheckListQueryGlobalScopeAndSecondDefenseBounds(t *testing.T) {
	query, args := managementModelCheckListQuery(port.ManagementModelCheckRunListInput{Limit: 999, Offset: 9999})
	if strings.Contains(query, "mcr.system_account_id =") {
		t.Fatalf("global query unexpectedly scoped:\n%s", query)
	}
	if !reflect.DeepEqual(args, []any{101, 900}) {
		t.Fatalf("args = %#v, want bounded limit/offset", args)
	}
	_, smallestPageArgs := managementModelCheckListQuery(port.ManagementModelCheckRunListInput{Limit: 1, Offset: 999})
	if !reflect.DeepEqual(smallestPageArgs, []any{2, 999}) {
		t.Fatalf("pageSize=1 args = %#v, want full 1001-row window", smallestPageArgs)
	}
}

func TestManagementModelCheckDetailAndItemsQueriesAreScopedAndOrdered(t *testing.T) {
	narrowQuery, narrowArgs := managementModelCheckDetailQuery("mcr_1", "sys_owner")
	assertManagementModelCheckReadOnlySQL(t, narrowQuery)
	if !strings.Contains(narrowQuery, "mcr.id = $1::text") || !strings.Contains(narrowQuery, "mcr.system_account_id = $2::text") || !strings.Contains(narrowQuery, "LIMIT 1") {
		t.Fatalf("narrow detail query =\n%s", narrowQuery)
	}
	if !strings.Contains(narrowQuery, "mcr.request_summary_json") || !strings.Contains(narrowQuery, "mcr.result_summary_json") {
		t.Fatalf("detail query omits summaries:\n%s", narrowQuery)
	}
	if !reflect.DeepEqual(narrowArgs, []any{"mcr_1", "sys_owner"}) {
		t.Fatalf("narrow args = %#v", narrowArgs)
	}

	globalQuery, globalArgs := managementModelCheckDetailQuery("mcr_1", "")
	if strings.Contains(globalQuery, "mcr.system_account_id =") || !reflect.DeepEqual(globalArgs, []any{"mcr_1"}) {
		t.Fatalf("global detail query/args =\n%s\n%#v", globalQuery, globalArgs)
	}

	itemsQuery := managementModelCheckItemsQuery()
	assertManagementModelCheckReadOnlySQL(t, itemsQuery)
	if !strings.Contains(itemsQuery, "WHERE mci.run_id = $1::text") || !strings.Contains(itemsQuery, "ORDER BY mci.created_at ASC, mci.id ASC") {
		t.Fatalf("items query =\n%s", itemsQuery)
	}
}

func TestManagementModelAccountTrustResultQueryReadsOnlyPreaggregatedPrimaryKeyRow(t *testing.T) {
	query := managementModelAccountTrustResultSQL
	if regexp.MustCompile(`(?i)\b(INSERT|UPDATE|DELETE|ALTER|CREATE|DROP|TRUNCATE)\b`).MatchString(query) {
		t.Fatalf("trust result query is not read-only:\n%s", query)
	}
	for _, fragment := range []string{
		"FROM juhe_stats.model_account_trust_results",
		"system_account_id = $1::text",
		"account_id = $2::text",
		"requested_model = $3::text",
		"reason_codes_json",
		"LIMIT 1",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("trust result query missing %q:\n%s", fragment, query)
		}
	}
	for _, forbidden := range []string{"model_check_observations", "model_trust_window_sources", "GROUP BY", "COUNT("} {
		if strings.Contains(query, forbidden) {
			t.Fatalf("trust result query scans or aggregates %q:\n%s", forbidden, query)
		}
	}
}

func TestManagementModelCheckRowMappingKeepsNullableFacts(t *testing.T) {
	row := managementModelCheckRunRow{
		ID: "mcr_1", SystemAccountID: "sys_owner", ActorSystemAccountID: "sys_actor", ProviderCode: "gpt",
		TargetType: "account", TargetID: "acct_1", TargetName: pgtype.Text{String: "目标账户", Valid: true},
		TargetOwnerSystemAccountID: pgtype.Text{String: "sys_owner", Valid: true}, AccountID: pgtype.Text{String: "acct_1", Valid: true},
		Model: "gpt-5.6-sol", Profile: "full", TrustedComparison: 1, TrustedComparisonAvailable: 1,
		Level: "likely", Score: 88, MaxScore: 100, Status: "completed", Message: "完成",
		TraceID: pgtype.Text{String: "trace_1", Valid: true}, ProbeSetVersion: "v4", StartedAt: "start",
		FinishedAt: pgtype.Text{String: "finish", Valid: true}, DurationMs: pgtype.Int4{Int32: 25, Valid: true},
		RequestSummaryJSON: `{"a":1}`, ResultSummaryJSON: `{"b":2}`, ErrorCode: pgtype.Text{}, ErrorMessage: pgtype.Text{},
		CreatedAt: "created", UpdatedAt: "updated",
	}
	fact := managementModelCheckRunFact(row)
	if fact.ID != "mcr_1" || fact.TargetName == nil || *fact.TargetName != "目标账户" || fact.DurationMs == nil || *fact.DurationMs != 25 || !fact.TrustedComparison || !fact.TrustedComparisonAvailable {
		t.Fatalf("fact = %+v", fact)
	}
	if fact.ErrorCode != nil || fact.ErrorMessage != nil {
		t.Fatalf("invalid nullable mapping: %+v", fact)
	}
}

func assertManagementModelCheckReadOnlySQL(t *testing.T, query string) {
	t.Helper()
	if regexp.MustCompile(`(?i)\b(INSERT|UPDATE|DELETE|ALTER|CREATE|DROP|TRUNCATE)\b`).MatchString(query) {
		t.Fatalf("query is not read-only:\n%s", query)
	}
	if !strings.Contains(query, "juhe_dataset.") {
		t.Fatalf("query does not read the Node-owned dataset schema:\n%s", query)
	}
}
