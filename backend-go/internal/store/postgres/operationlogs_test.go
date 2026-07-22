package postgres

import (
	"os"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestOperationLogSummarySearchTermsCoverChineseSummary(t *testing.T) {
	terms := operationLogSummarySearchTerms(" 更新账户标签：主账号 ")
	if len(terms) == 0 {
		t.Fatal("operationLogSummarySearchTerms() returned no terms")
	}
	for _, want := range []string{"更新账户标签:主账号", "更新账户标签主账号", "更", "账", "更新", "账户", "标签", "主账"} {
		if !containsString(terms, want) {
			t.Fatalf("terms missing %q: %#v", want, terms[:min(len(terms), 20)])
		}
	}
	if len(terms) > maxOperationLogSearchTerms {
		t.Fatalf("terms = %d, want <= %d", len(terms), maxOperationLogSearchTerms)
	}
}

func TestOperationLogSummarySearchTermsCoverSingleEnglishAndNumberTerms(t *testing.T) {
	terms := operationLogSummarySearchTerms("API Key 7")
	for _, want := range []string{"a", "p", "i", "k", "e", "y", "7"} {
		if !containsString(terms, want) {
			t.Fatalf("terms missing %q: %#v", want, terms)
		}
	}
	if len(terms) > maxOperationLogSearchTerms {
		t.Fatalf("terms = %d, want <= %d", len(terms), maxOperationLogSearchTerms)
	}
	term, hasSearch, invalidSearch := operationLogSearchTermFromKeyword("７")
	if term != "7" || !hasSearch || invalidSearch {
		t.Fatalf("operationLogSearchTermFromKeyword() = (%q, %v, %v), want (7, true, false)", term, hasSearch, invalidSearch)
	}
}

func TestOperationLogViewersAddActorAndOwnerDefaults(t *testing.T) {
	input := operationLogStoreFixture()
	input.Viewers = nil
	input.ActorSystemAccountID = "sys_admin"
	input.OperationScopeSystemAccountID = "sys_user"
	input.ActorRole = "admin"

	viewers := operationLogViewers(input)
	if !hasOperationLogViewer(viewers, "sys_admin", operationLogActorSelfViewerReason) {
		t.Fatalf("viewers missing actor_self: %#v", viewers)
	}
	if !hasOperationLogViewer(viewers, "sys_user", operationLogAdminManagedReason) {
		t.Fatalf("viewers missing admin managed owner: %#v", viewers)
	}
}

func TestOperationLogSQLGuards(t *testing.T) {
	data, err := os.ReadFile("queries/w2_operation_logs.sql")
	if err != nil {
		t.Fatalf("read operation log sql: %v", err)
	}
	sql := string(data)
	for _, want := range []string{
		"ON CONFLICT (id) DO NOTHING",
		"RETURNING id",
		"operation_log_viewers",
		"operation_log_summary_search_terms",
		"unnest(sqlc.arg(terms)::text[])",
		"operation_log_summary_search_terms AS search",
		`ol.trace_id COLLATE "C"`,
		"operation_log_viewers AS visible",
		"GetVisibleOperationLogDetail",
		"CleanupOperationLogsBefore",
		"GetOperationLogMaxChangesPerRecord",
		"operationLogMaxChangesPerRecord",
		"created_at < sqlc.arg(cutoff_created_at)::timestamptz",
		"ORDER BY created_at ASC, id ASC",
		"LIMIT sqlc.arg(row_limit)::int",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("operation log sql missing %q", want)
		}
	}
	for _, forbidden := range []string{
		" ILIKE ",
		" LIKE ",
		" MATCH ",
		"UNION ALL",
	} {
		if strings.Contains(strings.ToUpper(sql), forbidden) {
			t.Fatalf("operation log sql contains forbidden scan/sort pattern %q", forbidden)
		}
	}
}

func TestW5OperationLogSettingsMigrationSeedsMaxChanges(t *testing.T) {
	data, err := os.ReadFile("../../../db/migrations/000023_w5_operation_log_settings.sql")
	if err != nil {
		t.Fatalf("read W5 operation log settings migration: %v", err)
	}
	sql := string(data)
	for _, want := range []string{
		"INSERT INTO juhe_business.system_settings",
		"('sys_admin', 'operationLogMaxChangesPerRecord', '100', now())",
		"ON CONFLICT (system_account_id, key) DO NOTHING",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("W5 operation log settings migration missing %q", want)
		}
	}
}

func TestParseOperationLogMaxChangesPerRecord(t *testing.T) {
	for _, raw := range []string{"1", "100", "500"} {
		value, err := parseOperationLogMaxChangesPerRecord(raw)
		if err != nil {
			t.Fatalf("parseOperationLogMaxChangesPerRecord(%q) error = %v", raw, err)
		}
		if value < 1 || value > 500 {
			t.Fatalf("parseOperationLogMaxChangesPerRecord(%q) = %d", raw, value)
		}
	}
	for _, raw := range []string{"0", "501", "1.5", `"100"`, "invalid"} {
		if _, err := parseOperationLogMaxChangesPerRecord(raw); err == nil {
			t.Fatalf("parseOperationLogMaxChangesPerRecord(%q) error = nil", raw)
		}
	}
}

func operationLogStoreFixture() port.OperationLogInput {
	statusCode := 200
	return port.OperationLogInput{
		ID:                            "oplog_1",
		ActorSystemAccountID:          "sys_user",
		ActorRole:                     "user",
		OperationScopeSystemAccountID: "sys_user",
		Module:                        "accounts",
		Action:                        "update_tags",
		OperationKey:                  "accounts.update_tags",
		ResourceType:                  "account",
		ResourceID:                    "acct_main",
		ResourceName:                  "主账号",
		Summary:                       "更新账户标签：主账号",
		Method:                        "PATCH",
		Path:                          "/__aisys__/api/my-accounts/acct_main/tags",
		StatusCode:                    &statusCode,
		CreatedAt:                     time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func hasOperationLogViewer(viewers []port.OperationLogViewerInput, systemAccountID string, reason string) bool {
	for _, viewer := range viewers {
		if viewer.SystemAccountID == systemAccountID && viewer.VisibilityReason == reason {
			return true
		}
	}
	return false
}
