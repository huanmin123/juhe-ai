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

func TestW4AuthorizationUsageWindowMigrationMatchesCurrentContract(t *testing.T) {
	source, err := os.ReadFile("../../../db/migrations/000020_w4_authorization_usage_windows.sql")
	if err != nil {
		t.Fatalf("read W4 authorization usage migration: %v", err)
	}
	sql := string(source)
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS juhe_stats.authorization_team_usage_summary_daily",
		"CREATE TABLE IF NOT EXISTS juhe_stats.authorization_team_usage_range_windows",
		"CREATE TABLE IF NOT EXISTS juhe_stats.authorization_user_usage_summary_daily",
		"CREATE TABLE IF NOT EXISTS juhe_stats.authorization_user_usage_range_windows",
		"PRIMARY KEY (system_account_id, start_date, end_date, team_filter_id, resource_filter_type, resource_filter_id)",
		"PRIMARY KEY (system_account_id, start_date, end_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id)",
		"idx_authorization_team_usage_range_sort",
		"idx_authorization_user_usage_range_sort",
		"idx_authorization_team_usage_summary_daily_date",
		"idx_authorization_user_usage_summary_daily_date",
		"last_used_at timestamptz",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("W4 authorization usage migration missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"DROP TABLE",
		"juhe_usage.usage_records",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("W4 authorization usage migration should not contain %q", forbidden)
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

func TestManagementAuthorizationUsageOverviewQueriesReadRangeWindowsOnly(t *testing.T) {
	input := port.ManagementAuthorizationUsageOverviewInput{
		ActorSystemAccountID:   "sys_admin",
		CanAccessAll:           true,
		ScopedSystemAccountID:  "sys_owner",
		ResourceType:           "account",
		ResourceID:             "acct_main",
		TeamID:                 "team_ops",
		GranteeSystemAccountID: "sys_grantee",
		StartDate:              "2026-07-01",
		EndDate:                "2026-07-09",
		Limit:                  21,
		Offset:                 40,
	}
	teamQuery, teamArgs := managementAuthorizationTeamUsageOverviewQuery(input)
	userQuery, userArgs := managementAuthorizationUserUsageOverviewQuery(input)
	for label, query := range map[string]string{"team": teamQuery, "user": userQuery} {
		for _, want := range []string{
			"WITH page_rows AS",
			"juhe_stats.authorization_" + label + "_usage_range_windows",
			"report.system_account_id",
			"report.start_date",
			"report.end_date",
			"ORDER BY report.total_cost_usd DESC",
			"LIMIT",
			"OFFSET",
			"juhe_business.accounts",
			"juhe_business.groups",
		} {
			if !strings.Contains(query, want) {
				t.Fatalf("%s usage query missing %q:\n%s", label, want, query)
			}
		}
		for _, forbidden := range []string{
			"juhe_usage.usage_records",
			"usage_records",
			"GROUP BY",
			"SUM(",
		} {
			if strings.Contains(query, forbidden) {
				t.Fatalf("%s usage query should not contain %q:\n%s", label, forbidden, query)
			}
		}
	}
	for _, want := range []any{"sys_owner", "2026-07-01", "2026-07-09", "team_ops", "account", "acct_main", 21, 40} {
		if !containsQueryArg(teamArgs, want) {
			t.Fatalf("team usage args missing %v: %v", want, teamArgs)
		}
	}
	for _, want := range []any{"sys_owner", "2026-07-01", "2026-07-09", "team_ops", "sys_grantee", "account", "acct_main", 21, 40} {
		if !containsQueryArg(userArgs, want) {
			t.Fatalf("user usage args missing %v: %v", want, userArgs)
		}
	}
}

func TestManagementAuthorizationUsageRangeRefreshQueriesReadDailySummariesOnly(t *testing.T) {
	teamDelete := managementAuthorizationTeamUsageRangeRefreshDeleteQuery()
	userDelete := managementAuthorizationUserUsageRangeRefreshDeleteQuery()
	teamInsert := managementAuthorizationTeamUsageRangeRefreshInsertQuery()
	userInsert := managementAuthorizationUserUsageRangeRefreshInsertQuery()

	for label, query := range map[string]string{
		"team delete": teamDelete,
		"user delete": userDelete,
	} {
		for _, want := range []string{
			"DELETE FROM juhe_stats.authorization_",
			"WHERE end_date = $1",
			"AND start_date = $2",
		} {
			if !strings.Contains(query, want) {
				t.Fatalf("%s query missing %q:\n%s", label, want, query)
			}
		}
	}
	for label, query := range map[string]string{
		"team insert": teamInsert,
		"user insert": userInsert,
	} {
		for _, want := range []string{
			"INSERT INTO juhe_stats.authorization_",
			"usage_range_windows",
			"FROM juhe_stats.authorization_",
			"usage_summary_daily",
			"WHERE stat_date >= $4",
			"AND stat_date <= $5",
			"GROUP BY",
			"HAVING COALESCE(SUM(request_count), 0) > 0",
			"MAX(last_used_at)",
		} {
			if !strings.Contains(query, want) {
				t.Fatalf("%s query missing %q:\n%s", label, want, query)
			}
		}
		for _, forbidden := range []string{
			"juhe_usage.usage_records",
			"FROM usage_records",
			"JOIN usage_records",
		} {
			if strings.Contains(query, forbidden) {
				t.Fatalf("%s query should not contain %q:\n%s", label, forbidden, query)
			}
		}
	}
}

func TestManagementResourceAuthorizationUsageDetailQueryReadsRangeWindowsOnly(t *testing.T) {
	summary := port.ManagementResourceAuthorizationSummary{
		ID:                           "rauthgrant_team",
		ResourceType:                 "account",
		ResourceID:                   "acct_main",
		ResourceOwnerSystemAccountID: "sys_owner",
		GranteeType:                  "team",
		GranteeTeamID:                "team_ops",
	}
	input := port.ManagementResourceAuthorizationUsageInput{
		AuthorizationID:      "rauthgrant_team",
		ActorSystemAccountID: "sys_admin",
		CanAccessAll:         true,
		StartDate:            "2026-07-01",
		EndDate:              "2026-07-09",
		Limit:                201,
		Offset:               400,
	}
	query, args := managementResourceAuthorizationTeamUsageDetailQuery(summary, input)
	for _, want := range []string{
		"WITH page_rows AS",
		"juhe_business.resource_authorizations AS ra",
		"juhe_business.resource_authorization_sources AS ras",
		"juhe_stats.authorization_user_usage_range_windows",
		"usage_windows.system_account_id = CASE",
		"usage_windows.team_filter_id",
		"usage_windows.grantee_filter_system_account_id",
		"ORDER BY usage_windows.last_used_at DESC NULLS LAST",
		"LIMIT",
		"OFFSET",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("usage detail query missing %q:\n%s", want, query)
		}
	}
	for _, forbidden := range []string{
		"juhe_usage.usage_records",
		"usage_records",
		"GROUP BY",
		"SUM(",
	} {
		if strings.Contains(query, forbidden) {
			t.Fatalf("usage detail query should not contain %q:\n%s", forbidden, query)
		}
	}
	for _, want := range []any{"team_ops", "account", "acct_main", "sys_owner", "2026-07-01", "2026-07-09", 201, 400} {
		if !containsQueryArg(args, want) {
			t.Fatalf("usage detail args missing %v: %v", want, args)
		}
	}
	if got := managementResourceAuthorizationUsageStatsSystemAccountID("account", "sys_owner", "sys_grantee"); got != "sys_grantee" {
		t.Fatalf("account usage stats system account id = %q", got)
	}
	if got := managementResourceAuthorizationUsageStatsSystemAccountID("group", "sys_owner", "sys_grantee"); got != "sys_owner" {
		t.Fatalf("group usage stats system account id = %q", got)
	}
}

func TestManagementResourceAuthorizationRevokeQueryKeepsTeamGrantScope(t *testing.T) {
	source, err := os.ReadFile("managementauthorizations.go")
	if err != nil {
		t.Fatalf("read management authorizations store: %v", err)
	}
	code := strings.ReplaceAll(string(source), "\r\n", "\n")
	for _, want := range []string{
		"func (s *Store) RevokeManagementResourceAuthorization",
		"UPDATE juhe_business.resource_authorization_grants",
		"SET status = 'revoked'",
		"markAllGroupAccountStatsDirtyIfPresentTx(ctx, tx, \"resource_authorization_revoked\", now)",
		"func revokeManagementTeamGrantSourcesForGrantTx",
		"AND ra.resource_type = $2",
		"AND ra.resource_id = $3",
		"ended_reason = COALESCE(ended_reason, 'team_revoked')",
		"noActiveSourceReason:              \"authorization_revoked\"",
		"preserveExpiredWhenNoActiveSource: false",
	} {
		if !strings.Contains(code, want) {
			t.Fatalf("revoke implementation missing %q", want)
		}
	}
	if strings.Contains(code, "RevokeManagementResourceAuthorization(ctx context.Context") &&
		strings.Contains(code, "revokeAllManagementTeamSourcesTx(ctx, tx") {
		t.Fatal("authorization revoke must not call whole-team source revoke helper")
	}
}

func TestManagementResourceAuthorizationRevokeQueryExcludesTerminalGrants(t *testing.T) {
	source, err := os.ReadFile("managementauthorizations.go")
	if err != nil {
		t.Fatalf("read management authorizations store: %v", err)
	}
	code := strings.ReplaceAll(string(source), "\r\n", "\n")
	start := strings.Index(code, "func findRevocableManagementGrantTx(")
	end := strings.Index(code, "\nfunc revokeManagementManualGrantSourcesTx(")
	if start < 0 || end < 0 || end <= start {
		t.Fatal("find revocable management grant function block not found")
	}
	query := code[start:end]
	if !strings.Contains(query, "AND status IN ('active', 'paused', 'expired')") {
		t.Fatalf("find revocable management grant query must exclude terminal statuses:\n%s", query)
	}
}

func TestManagementResourceAuthorizationAccountInstancePreservesHealthCheckModel(t *testing.T) {
	source, err := os.ReadFile("managementauthorizations.go")
	if err != nil {
		t.Fatalf("read management authorizations store: %v", err)
	}
	code := strings.ReplaceAll(string(source), "\r\n", "\n")
	for _, want := range []string{
		"protocol_version, name, type, concurrency_limit, health_check_model, health_check_endpoint_mode",
		"health_check_model = $7",
		"health_check_endpoint_mode = $8",
		"temporary_unavailable_continuous_probe_enabled = $9",
		"concurrency_limit, priority, super_priority_enabled, fallback_enabled, schedulable,\n  health_check_model, health_check_endpoint_mode,",
		"source.ConcurrencyLimit, source.HealthCheckModel, source.HealthCheckEndpointMode, source.TemporaryUnavailableContinuousProbeEnabled, authorization.ResourceID",
	} {
		if !strings.Contains(code, want) {
			t.Fatalf("authorization account instance implementation missing %q", want)
		}
	}
}

func TestManagementResourceAuthorizationReturnByResourceKeepsDirectUserGrantScope(t *testing.T) {
	source, err := os.ReadFile("managementauthorizations.go")
	if err != nil {
		t.Fatalf("read management authorizations store: %v", err)
	}
	code := string(source)
	for _, want := range []string{
		"func (s *Store) ReturnManagementResourceAuthorizationForGranteeByResource",
		"func findManagementRuntimeAuthorizationForResourceReturnTx",
		"FROM juhe_business.accounts",
		"AND system_account_id = $2",
		"AND deleted_at IS NULL",
		"AND authorization_instance_authorization_id IS NOT NULL",
		"WHERE resource_type = 'group'",
		"AND grantee_system_account_id = $2",
		"func findReturnableManagementDirectGrantForRuntimeAuthorizationTx",
		"grantee_type = 'system_account'",
		"status NOT IN ('revoked', 'returned')",
		"hasActiveManagementManualAuthorizationSourceTx(ctx, tx, runtimeAuthorization.ID)",
		"returnManagementResourceAuthorizationGrantTx(ctx, tx, grant, runtimeAuthorization",
		"markAllGroupAccountStatsDirtyIfPresentTx(ctx, tx, \"resource_authorization_returned\", now)",
	} {
		if !strings.Contains(code, want) {
			t.Fatalf("resource return implementation missing %q", want)
		}
	}
	if strings.Contains(code, "ReturnManagementResourceAuthorizationForGranteeByResource(ctx context.Context") &&
		strings.Contains(code, "revokeAllManagementTeamSourcesTx(ctx, tx") {
		t.Fatal("resource return must not call whole-team source revoke helper")
	}
}

func TestManagementResourceAuthorizationExpirySweepQueryUsesGrantExpiryIndex(t *testing.T) {
	query := managementResourceAuthorizationExpirySweepQuery()
	for _, want := range []string{
		"FROM juhe_business.resource_authorization_grants",
		"WHERE status IN ('active', 'paused')",
		"expires_at IS NOT NULL",
		"expires_at <= $1",
		"ORDER BY expires_at ASC, updated_at ASC, id ASC",
		"LIMIT $2",
		"FOR UPDATE SKIP LOCKED",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("expiry sweep query missing %q:\n%s", want, query)
		}
	}
	for _, forbidden := range []string{
		"juhe_usage.usage_records",
		"usage_records",
		"SUM(",
		"GROUP BY",
		"resource_authorizations AS",
	} {
		if strings.Contains(query, forbidden) {
			t.Fatalf("expiry sweep query should not contain %q:\n%s", forbidden, query)
		}
	}
}

func TestManagementResourceAuthorizationUpdateQueryKeepsRuntimeScope(t *testing.T) {
	source, err := os.ReadFile("managementauthorizations.go")
	if err != nil {
		t.Fatalf("read management authorizations store: %v", err)
	}
	code := string(source)
	for _, want := range []string{
		"func (s *Store) UpdateManagementResourceAuthorization",
		"func findUpdatableManagementGrantTx",
		"FOR UPDATE",
		"nextManagementAuthorizationGrantStatus",
		"到期授权恢复时请同时调整过期时间",
		"markAllGroupAccountStatsDirtyIfPresentTx(ctx, tx, \"resource_authorization_updated\", now)",
		"func syncManagementUserGrantRuntimeAfterUpdateTx",
		"func syncManagementTeamGrantRuntimeAfterUpdateTx",
		"func updateManagementTeamGrantSourceRuntimesTx",
		"if grant.LimitsJson.Valid {",
		"LimitsJSON:                   limitsJSON",
		"AND ras.source_team_id = $3",
		"AND ras.status = 'active'",
		"WHEN $2 = 'paused' THEN 'authorization_paused'",
		"refreshManagementResourceAuthorizationEffectiveSourceTx(ctx, tx, authorizationID, actor, now)",
		"revokeManagementTeamGrantSourcesForGrantTx(ctx, tx, grant.ResourceType, grant.ResourceID, teamID, actor, now)",
	} {
		if !strings.Contains(code, want) {
			t.Fatalf("update implementation missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"expireDueResourceAuthorizations",
		"revokeAllManagementTeamSourcesTx(ctx, tx",
	} {
		if strings.Contains(code, forbidden) {
			t.Fatalf("update implementation should not contain %q", forbidden)
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
