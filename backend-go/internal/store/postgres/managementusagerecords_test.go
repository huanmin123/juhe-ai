package postgres

import (
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementUsageRecordListQueryIsBoundedScopedAndStable(t *testing.T) {
	statusCode := 429
	query, args := managementUsageRecordListQuery(port.ManagementUsageRecordListInput{
		SystemAccountID: "sys_user",
		TraceID:         "trace_",
		AccountKeyword:  "账户",
		ClientIP:        "203.0.113.",
		Result:          "failed",
		StatusCode:      &statusCode,
		GroupID:         "group_1",
		Model:           "gpt-5.5",
		TrafficSource:   "gateway",
		StartAt:         time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC),
		EndAt:           time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC),
	}, 51, 50)

	for _, required := range []string{
		"FROM juhe_usage.usage_records AS ur",
		"LEFT JOIN juhe_business.system_accounts",
		"LEFT JOIN juhe_business.api_keys",
		"LEFT JOIN juhe_business.groups",
		"LEFT JOIN juhe_business.accounts",
		"ur.system_account_id =",
		"ur.trace_id COLLATE \"C\" >=",
		"ur.client_ip COLLATE \"C\" >=",
		"ur.account_id IN (",
		"ur.success =",
		"ur.status_code =",
		"ur.group_id =",
		"ur.model =",
		"ur.traffic_source =",
		"ur.created_at >=",
		"ur.created_at <",
		"ORDER BY ur.created_at DESC, ur.id DESC",
		"LIMIT",
		"OFFSET",
	} {
		if !strings.Contains(query, required) {
			t.Fatalf("query missing %q:\n%s", required, query)
		}
	}
	for _, forbidden := range []string{"request_snapshot_json", "response_snapshot_json", "COUNT("} {
		if strings.Contains(query, forbidden) {
			t.Fatalf("list query contains %q:\n%s", forbidden, query)
		}
	}
	if len(args) < 15 {
		t.Fatalf("args = %#v", args)
	}
}

func TestManagementUsageRecordListQueryMatchesDirectAndGroupAuthorizedAccounts(t *testing.T) {
	query, args := managementUsageRecordListQuery(port.ManagementUsageRecordListInput{
		SystemAccountID: "sys_grantee",
		AccountKeyword:  "shared",
	}, 51, 0)

	for _, required := range []string{
		"accounts.system_account_id = $1::text",
		"instances.system_account_id = $1::text",
		"INNER JOIN juhe_business.resource_authorizations AS direct_authorization",
		"direct_authorization.resource_type = 'account'",
		"direct_authorization.resource_id = accounts.id",
		"direct_authorization.grantee_system_account_id =",
		"INNER JOIN juhe_business.group_accounts AS ga",
		"ga.enabled = true",
		"INNER JOIN juhe_business.resource_authorizations AS group_authorization",
		"group_authorization.resource_type = 'group'",
		"group_authorization.resource_id = ga.group_id",
		"group_authorization.grantee_system_account_id =",
		"SELECT DISTINCT ON (matched.id)",
		"ORDER BY matched.id, matched.source_rank",
		"ORDER BY ordered.source_rank, ordered.match_name COLLATE \"C\", ordered.id",
	} {
		if !strings.Contains(query, required) {
			t.Fatalf("query missing %q:\n%s", required, query)
		}
	}
	if len(args) != 5 || args[0] != "sys_grantee" || args[1] != "shared" || args[2] != "sharee" {
		t.Fatalf("args = %#v", args)
	}
	if count := strings.Count(query, "LIMIT 200"); count != 5 {
		t.Fatalf("LIMIT 200 count = %d, want four bounded sources plus final cap:\n%s", count, query)
	}
}

func TestManagementUsageRecordDetailQueryIncludesSnapshotsAndScope(t *testing.T) {
	query, args := managementUsageRecordDetailQuery(port.ManagementUsageRecordDetailInput{ID: "usage_1", SystemAccountID: "sys_user"})
	for _, required := range []string{
		"ur.request_snapshot_json",
		"ur.response_snapshot_json",
		"WHERE ur.id = $1::text",
		"ur.system_account_id = $2::text",
		"LIMIT 1",
	} {
		if !strings.Contains(query, required) {
			t.Fatalf("query missing %q:\n%s", required, query)
		}
	}
	if len(args) != 2 || args[0] != "usage_1" || args[1] != "sys_user" {
		t.Fatalf("args = %#v", args)
	}
}

func TestManagementUsageRecordDetailQueryPrunesPartitionFromNewFormatID(t *testing.T) {
	query, args := managementUsageRecordDetailQuery(port.ManagementUsageRecordDetailInput{
		ID:              "usage_20260722_s3_trace",
		SystemAccountID: "sys_user",
	})

	for _, required := range []string{
		"ur.created_at >= $2::timestamptz",
		"ur.created_at < $3::timestamptz",
		"ur.system_account_id = $4::text",
	} {
		if !strings.Contains(query, required) {
			t.Fatalf("query missing %q:\n%s", required, query)
		}
	}
	if len(args) != 4 {
		t.Fatalf("args = %#v", args)
	}
	start, ok := args[1].(time.Time)
	if !ok || !start.Equal(time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("partition start = %#v", args[1])
	}
	end, ok := args[2].(time.Time)
	if !ok || !end.Equal(time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("partition end = %#v", args[2])
	}
}

func TestManagementUsageRecordDetailQueryFallsBackForInvalidNewFormatDate(t *testing.T) {
	query, args := managementUsageRecordDetailQuery(port.ManagementUsageRecordDetailInput{
		ID:              "usage_20260231_s3_trace",
		SystemAccountID: "sys_user",
	})
	if strings.Contains(query, "ur.created_at >=") || strings.Contains(query, "ur.created_at <") {
		t.Fatalf("invalid date must not add partition bounds:\n%s", query)
	}
	if !strings.Contains(query, "ur.system_account_id = $2::text") || len(args) != 2 {
		t.Fatalf("query = %s args = %#v", query, args)
	}
}
