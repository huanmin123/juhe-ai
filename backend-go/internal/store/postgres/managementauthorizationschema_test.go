package postgres

import (
	"os"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

func TestW4ResourceAuthorizationGrantSourceMigrationMatchesCurrentContract(t *testing.T) {
	source, err := os.ReadFile("../../../db/migrations/000018_w4_resource_authorization_sources_and_grants.sql")
	if err != nil {
		t.Fatalf("read W4 authorization migration: %v", err)
	}
	sql := string(source)
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS juhe_business.resource_authorization_sources",
		"authorization_id text NOT NULL REFERENCES juhe_business.resource_authorizations(id) ON DELETE CASCADE",
		"source_type text NOT NULL CHECK (source_type IN ('manual', 'team'))",
		"source_team_id text REFERENCES juhe_business.system_teams(id) ON DELETE CASCADE",
		"status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'superseded', 'revoked'))",
		"CREATE TABLE IF NOT EXISTS juhe_business.resource_authorization_grants",
		"resource_type text NOT NULL CHECK (resource_type IN ('group', 'account'))",
		"resource_owner_system_account_id text NOT NULL REFERENCES juhe_business.system_accounts(id) ON DELETE CASCADE",
		"grantee_type text NOT NULL CHECK (grantee_type IN ('system_account', 'team'))",
		"status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused', 'expired', 'revoked', 'returned'))",
		"limits_json text CHECK (limits_json IS NULL OR jsonb_typeof(limits_json::jsonb) = 'object')",
		"idx_resource_authorization_sources_active_manual_unique",
		"idx_resource_authorization_sources_active_team_unique",
		"idx_resource_authorization_grants_active_user_unique",
		"idx_resource_authorization_grants_active_team_unique",
		"idx_resource_authorization_grants_expiry_sweep",
		"idx_resource_authorization_grants_team_quota_snapshot",
		"idx_resource_authorizations_quota_snapshot",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("W4 authorization migration missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"DROP TABLE",
		"DELETE FROM juhe_business.resource_authorizations",
		"DELETE FROM juhe_business.resource_authorization_sources",
		"DELETE FROM juhe_business.resource_authorization_grants",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("W4 authorization migration should not contain destructive statement %q", forbidden)
		}
	}
}

func TestW4AuthorizationQuotaAndStatsStateMigrationMatchesCurrentContract(t *testing.T) {
	source, err := os.ReadFile("../../../db/migrations/000019_w4_authorization_quota_and_stats_state.sql")
	if err != nil {
		t.Fatalf("read W4 authorization quota migration: %v", err)
	}
	sql := string(source)
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS juhe_business.request_quota_hourly_window_configs",
		"window_hours integer PRIMARY KEY CHECK (window_hours BETWEEN 1 AND 720)",
		"CREATE TABLE IF NOT EXISTS juhe_business.group_account_stats_dirty",
		"group_id text PRIMARY KEY",
		"idx_group_account_stats_dirty_updated",
		"(1, NOW(), NOW())",
		"(720, NOW(), NOW())",
		"ON CONFLICT DO NOTHING",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("W4 authorization quota migration missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"DROP TABLE",
		"DELETE FROM juhe_business.request_quota_hourly_window_configs",
		"DELETE FROM juhe_business.group_account_stats_dirty",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("W4 authorization quota migration should not contain destructive statement %q", forbidden)
		}
	}
}

func TestManagementResourceAuthorizationListQueryScopesAndFilters(t *testing.T) {
	query, args := managementResourceAuthorizationListQuery(port.ManagementResourceAuthorizationListInput{
		AuthorizationID:              "rauthgrant_main",
		ActorSystemAccountID:         "sys_actor",
		CanAccessAll:                 false,
		ResourceType:                 "account",
		ResourceID:                   "acct_main",
		ResourceOwnerSystemAccountID: "sys_owner",
		GranteeSystemAccountID:       "sys_grantee",
		TeamID:                       "team_ops",
		Status:                       "active",
		Direction:                    "inbound",
		SourceType:                   "team",
		Keyword:                      "授权",
		Limit:                        6,
		Offset:                       12,
	})

	for _, want := range []string{
		"FROM juhe_business.resource_authorization_grants AS rag",
		"LEFT JOIN LATERAL",
		"rag.id =",
		"rag.resource_type =",
		"rag.resource_owner_system_account_id =",
		"rag.grantee_system_account_id =",
		"rag.grantee_team_id =",
		`COLLATE "C"`,
		"starts_with(",
		"juhe_business.system_team_members AS stm_scope",
		"juhe_business.system_team_members AS stm_direction",
		"ORDER BY rag.created_at DESC, rag.id DESC",
		"LIMIT",
		"OFFSET",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("list query missing %q:\n%s", want, query)
		}
	}
	if len(args) < 2 || args[len(args)-2] != 6 || args[len(args)-1] != 12 {
		t.Fatalf("pagination args = %v, want last args 6, 12", args)
	}
	for _, want := range []any{"rauthgrant_main", "sys_actor", "acct_main", "sys_owner", "sys_grantee", "team_ops", "授权"} {
		if !containsQueryArg(args, want) {
			t.Fatalf("query args missing %v: %v", want, args)
		}
	}
}

func containsQueryArg(args []any, want any) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
