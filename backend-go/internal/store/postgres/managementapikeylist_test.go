package postgres

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func TestW5ManagementAPIKeyListSQLIsBoundedStableAndSecretFree(t *testing.T) {
	source, err := os.ReadFile("queries/w5_management_api_key_list.sql")
	if err != nil {
		t.Fatalf("read W5 management API Key list query: %v", err)
	}
	sql := string(source)
	for _, required := range []string{
		"ListManagementAPIKeys",
		"ListManagementAPIKeysByKeyword",
		"matched_api_key_ids AS MATERIALIZED",
		"ListManagementAPIKeyUsageTotals",
		`starts_with(keyword_api_keys.name, sqlc.arg(keyword)::text)`,
		"api_keys.is_default DESC",
		"api_keys.updated_at DESC",
		"api_keys.created_at DESC",
		"api_keys.id DESC",
		"requested_scopes AS MATERIALIZED",
		"scope_type = 'api_key'",
		"usage_stats.system_account_id = requested_scopes.system_account_id",
		"usage_stats.scope_id = requested_scopes.api_key_id",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("W5 management API Key SQL missing %q", required)
		}
	}
	keywordMarker := "-- name: ListManagementAPIKeysByKeyword :many"
	usageMarker := "-- name: ListManagementAPIKeyUsageTotals :many"
	keywordIndex := strings.Index(sql, keywordMarker)
	usageIndex := strings.Index(sql, usageMarker)
	if keywordIndex < 0 || usageIndex <= keywordIndex {
		t.Fatalf("W5 management API Key SQL query order is invalid")
	}
	ordinarySQL := sql[:keywordIndex]
	keywordSQL := sql[keywordIndex:usageIndex]
	if strings.Contains(ordinarySQL, "matched_api_key_ids") ||
		strings.Contains(ordinarySQL, "starts_with") {
		t.Fatalf("ordinary API Key list path must not include keyword candidate work: %s", ordinarySQL)
	}
	if !strings.Contains(keywordSQL, "matched_api_key_ids AS MATERIALIZED") ||
		!strings.Contains(keywordSQL, "api_keys.id IN (SELECT id FROM matched_api_key_ids)") {
		t.Fatalf("keyword API Key list path must use materialized candidate IDs: %s", keywordSQL)
	}
	for _, forbidden := range []string{
		"key_hash",
		"key_secret_encrypted",
		"usage_records",
		"COUNT(",
		"GROUP BY",
	} {
		if strings.Contains(strings.ToLower(sql), strings.ToLower(forbidden)) {
			t.Fatalf("W5 management API Key SQL must not contain %q", forbidden)
		}
	}
}

func TestW5ManagementAPIKeyListMigrationAddsOnlyFreshListIndexes(t *testing.T) {
	source, err := os.ReadFile("../../../db/migrations/000033_w5_management_api_key_list.sql")
	if err != nil {
		t.Fatalf("read W5 management API Key list migration: %v", err)
	}
	sql := string(source)
	for _, required := range []string{
		"idx_api_keys_default_updated",
		"idx_api_keys_status_default_updated",
		"idx_api_keys_route_default_updated",
		"idx_api_keys_name_c_lookup",
		"is_default DESC, updated_at DESC, created_at DESC, id DESC",
		"route_strategy_id, is_default DESC, updated_at DESC, created_at DESC, id DESC",
		"name COLLATE \"C\", id",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("W5 management API Key migration missing %q", required)
		}
	}
	if strings.Contains(sql, "DROP INDEX") || strings.Contains(sql, "ALTER TABLE") {
		t.Fatalf("W5 management API Key migration should only add fresh indexes: %s", sql)
	}
	if strings.Contains(sql, "idx_usage_stats_totals_scope_lookup") {
		t.Fatalf("W5 management API Key migration must not add scope-only usage index: %s", sql)
	}
}

func TestPublicAPIKeyCleanupTargetUpsertPreservesRetryState(t *testing.T) {
	source, err := os.ReadFile("queries/w1b_public_api_keys.sql")
	if err != nil {
		t.Fatalf("read public API Key SQL: %v", err)
	}
	sql := string(source)
	for _, required := range []string{
		"-- name: UpsertPublicAPIKeyRecordCleanupTarget :exec",
		"INSERT INTO juhe_dataset.api_key_record_cleanup_targets",
		"api_key_id",
		"system_account_id",
		"created_at",
		"updated_at",
		"ON CONFLICT (api_key_id) DO UPDATE SET",
		"system_account_id = EXCLUDED.system_account_id",
		"updated_at = EXCLUDED.updated_at",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("public API Key cleanup upsert missing %q", required)
		}
	}
	normalized := strings.Join(strings.Fields(sql), " ")
	if !strings.Contains(
		normalized,
		"api_key_id, system_account_id, created_at, updated_at",
	) {
		t.Fatal("public API Key cleanup upsert must write the Node worker columns")
	}
	upsert := sql[strings.Index(sql, "-- name: UpsertPublicAPIKeyRecordCleanupTarget"):]
	for _, forbidden := range []string{
		"created_at = EXCLUDED.created_at",
		"attempt_count =",
		"last_attempt_at =",
		"last_blocked_reason =",
		"last_error_message =",
	} {
		if strings.Contains(upsert, forbidden) {
			t.Fatalf("public API Key cleanup upsert must preserve retry state, found %q", forbidden)
		}
	}
}

func TestListManagementAPIKeysMapsRowsAndUsesPageSizePlusOne(t *testing.T) {
	expiresAt := time.Date(2026, 7, 10, 3, 2, 3, 0, time.UTC)
	q := &managementAPIKeyListQueriesStub{
		keywordRows: []postgresqueries.ListManagementAPIKeysByKeywordRow{
			{
				ID:                  "key_1",
				SystemAccountID:     "sys_owner",
				SystemAccountName:   "所有者",
				Name:                "Key",
				Description:         pgtype.Text{String: "desc", Valid: true},
				KeyPrefix:           "sk-prefix",
				KeySuffix:           "suffix",
				Status:              "active",
				IsDefault:           true,
				RouteStrategyID:     "route_1",
				RouteStrategyName:   "策略",
				RouteStrategyMode:   "normal",
				RouteStrategyStatus: "active",
				ExpiresAt:           pgtype.Timestamptz{Time: expiresAt, Valid: true},
				QuotaLimitsJson:     pgtype.Text{String: `{"daily":{"enabled":true,"limit":1}}`, Valid: true},
			},
			{
				ID: "key_extra",
			},
		},
	}

	page, err := listManagementAPIKeys(context.Background(), q, port.ManagementAPIKeyListInput{
		SystemAccountID: " sys_owner ",
		Keyword:         " Key% ",
		Status:          "active",
		RouteStrategyID: " route_1 ",
		Limit:           2,
		Offset:          -5,
	})
	if err != nil {
		t.Fatalf("listManagementAPIKeys() error = %v", err)
	}
	if len(q.listCalls) != 0 || len(q.keywordCalls) != 1 {
		t.Fatalf("list calls = %d keyword calls = %d", len(q.listCalls), len(q.keywordCalls))
	}
	call := q.keywordCalls[0]
	if call.SystemAccountID != "sys_owner" ||
		call.Keyword != "Key%" ||
		call.KeywordUpper != "Key&" ||
		call.Status != "active" ||
		call.RouteStrategyID != "route_1" ||
		call.RowLimit != 2 ||
		call.RowOffset != 0 {
		t.Fatalf("list params = %+v", call)
	}
	if !page.HasMore || len(page.Rows) != 1 {
		t.Fatalf("page = %+v", page)
	}
	row := page.Rows[0]
	if row.ID != "key_1" ||
		row.SystemAccountID != "sys_owner" ||
		row.Description == nil ||
		*row.Description != "desc" ||
		row.ExpiresAt == nil ||
		!row.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("row = %+v", row)
	}
}

func TestListManagementAPIKeysUsesOrdinaryQueryWithoutKeyword(t *testing.T) {
	q := &managementAPIKeyListQueriesStub{}
	_, err := listManagementAPIKeys(context.Background(), q, port.ManagementAPIKeyListInput{
		SystemAccountID: "sys_owner",
		Status:          "disabled",
		RouteStrategyID: "route_1",
		Limit:           51,
	})
	if err != nil {
		t.Fatalf("listManagementAPIKeys() error = %v", err)
	}
	if len(q.listCalls) != 1 || len(q.keywordCalls) != 0 {
		t.Fatalf("list calls = %d keyword calls = %d", len(q.listCalls), len(q.keywordCalls))
	}
}

func TestListManagementAPIKeyUsageTotalsBatchesCurrentPageIDs(t *testing.T) {
	lastUsedAt := "2026-07-11T01:02:03.456Z"
	q := &managementAPIKeyListQueriesStub{
		usageRows: []postgresqueries.ListManagementAPIKeyUsageTotalsRow{{
			SystemAccountID:    "sys_1",
			ScopeID:            "key_1",
			RequestCount:       3,
			InputTokens:        4,
			OutputTokens:       5,
			CacheReadTokens:    6,
			CacheReadCostUsd:   0.1,
			CacheWriteTokens:   7,
			CacheWrite1hTokens: 8,
			CacheWriteCostUsd:  0.2,
			ThinkingTokens:     9,
			InputImageTokens:   10,
			OutputImageTokens:  11,
			TotalCostUsd:       0.3,
			LastUsedAt:         pgtype.Text{String: lastUsedAt, Valid: true},
		}},
	}

	rows, err := listManagementAPIKeyUsageTotals(
		context.Background(),
		q,
		[]port.ManagementAPIKeyUsageScope{
			{SystemAccountID: " sys_1 ", APIKeyID: " key_1 "},
			{},
			{SystemAccountID: "sys_1", APIKeyID: "key_1"},
			{SystemAccountID: "sys_2", APIKeyID: "key_2"},
		},
	)
	if err != nil {
		t.Fatalf("listManagementAPIKeyUsageTotals() error = %v", err)
	}
	wantCalls := []postgresqueries.ListManagementAPIKeyUsageTotalsParams{{
		SystemAccountIds: []string{"sys_1", "sys_2"},
		ApiKeyIds:        []string{"key_1", "key_2"},
	}}
	if !reflect.DeepEqual(q.usageCalls, wantCalls) {
		t.Fatalf("usage calls = %#v", q.usageCalls)
	}
	if len(rows) != 1 ||
		rows[0].SystemAccountID != "sys_1" ||
		rows[0].APIKeyID != "key_1" ||
		rows[0].Usage.TotalTokens != 9 ||
		rows[0].Usage.LastUsedAt == nil ||
		rows[0].Usage.LastUsedAt.Format(time.RFC3339Nano) != lastUsedAt {
		t.Fatalf("usage rows = %+v", rows)
	}
}

type managementAPIKeyListQueriesStub struct {
	rows         []postgresqueries.ListManagementAPIKeysRow
	keywordRows  []postgresqueries.ListManagementAPIKeysByKeywordRow
	usageRows    []postgresqueries.ListManagementAPIKeyUsageTotalsRow
	listCalls    []postgresqueries.ListManagementAPIKeysParams
	keywordCalls []postgresqueries.ListManagementAPIKeysByKeywordParams
	usageCalls   []postgresqueries.ListManagementAPIKeyUsageTotalsParams
}

func (s *managementAPIKeyListQueriesStub) ListManagementAPIKeys(
	_ context.Context,
	arg postgresqueries.ListManagementAPIKeysParams,
) ([]postgresqueries.ListManagementAPIKeysRow, error) {
	s.listCalls = append(s.listCalls, arg)
	return s.rows, nil
}

func (s *managementAPIKeyListQueriesStub) ListManagementAPIKeysByKeyword(
	_ context.Context,
	arg postgresqueries.ListManagementAPIKeysByKeywordParams,
) ([]postgresqueries.ListManagementAPIKeysByKeywordRow, error) {
	s.keywordCalls = append(s.keywordCalls, arg)
	return s.keywordRows, nil
}

func (s *managementAPIKeyListQueriesStub) ListManagementAPIKeyUsageTotals(
	_ context.Context,
	arg postgresqueries.ListManagementAPIKeyUsageTotalsParams,
) ([]postgresqueries.ListManagementAPIKeyUsageTotalsRow, error) {
	s.usageCalls = append(s.usageCalls, arg)
	return s.usageRows, nil
}

var _ managementAPIKeyListQueries = (*managementAPIKeyListQueriesStub)(nil)
