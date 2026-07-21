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
