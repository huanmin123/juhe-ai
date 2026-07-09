package postgres

import (
	"os"
	"strings"
	"testing"
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
