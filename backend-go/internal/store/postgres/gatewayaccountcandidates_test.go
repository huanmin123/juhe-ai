package postgres

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestGatewayGroupAccessSQLPreservesOwnerAndAuthorizationBoundaries(t *testing.T) {
	for _, fragment := range []string{
		"groups.enabled = true",
		"groups.system_account_id = $2::text",
		"resource_authorizations.resource_type = 'group'",
		"resource_authorizations.resource_owner_system_account_id = groups.system_account_id",
		"resource_authorizations.grantee_system_account_id = $2::text",
		"resource_authorizations.status = 'active'",
		"resource_authorizations.expires_at > $3::timestamptz",
		"COALESCE(group_authorization_settings.enabled, true) = true",
		"LIMIT 1",
	} {
		if !strings.Contains(resolveGatewayGroupAccessSQL, fragment) {
			t.Fatalf("group access SQL missing %q", fragment)
		}
	}
}

func TestGatewayAccountCandidateSQLIsBoundedAndAuthorizationAware(t *testing.T) {
	for _, fragment := range []string{
		"group_accounts.group_id = $1::text",
		"group_accounts.system_account_id = $2::text",
		"group_accounts.enabled = true",
		"INNER JOIN juhe_business.groups AS groups",
		"groups.enabled = true",
		"group_authorizations.id = $8::text",
		"group_authorizations.resource_owner_system_account_id = groups.system_account_id",
		"group_authorizations.grantee_system_account_id = $3::text",
		"COALESCE(group_authorization_settings.enabled, true) = true",
		"accounts.provider_code = $4::text",
		"accounts.deleted_at IS NULL",
		"accounts.authorization_instance_authorization_id IS NULL",
		"accounts.authorization_instance_source_account_id IS NULL",
		"accounts.authorization_instance_owner_system_account_id IS NULL",
		"group_accounts.account_authorization_id IS NULL",
		"accounts.schedulable = true",
		"group_accounts.account_authorization_id = accounts.authorization_instance_authorization_id",
		"account_authorizations.grantee_system_account_id = $3::text",
		"account_authorizations.status = 'active'",
		"accounts.authorization_instance_owner_system_account_id = account_authorizations.resource_owner_system_account_id",
		"source_accounts.deleted_at IS NULL",
		"source_accounts.schedulable = true",
		"FROM juhe_business.account_supported_models AS supported_models",
		"supported_models.account_id = COALESCE(source_accounts.id, accounts.id)",
		"FROM juhe_business.account_model_mappings AS model_mappings",
		"model_mappings.source_model = $9::text",
		"model_mappings.source_endpoint_family = $10::text",
		"model_mappings.upstream_model <> model_mappings.source_model",
		"mapped_models.model = model_mappings.upstream_model",
		"group_accounts.local_fallback_enabled ASC",
		"group_accounts.local_super_priority_enabled DESC",
		"group_accounts.local_priority ASC",
		"group_accounts.created_at ASC",
		"group_accounts.account_id ASC",
		"LIMIT $11",
	} {
		if !strings.Contains(listGatewayAccountCandidatesSQL, fragment) {
			t.Fatalf("candidate SQL missing %q", fragment)
		}
	}
	if strings.Contains(listGatewayAccountCandidatesSQL, "OFFSET") {
		t.Fatal("candidate SQL must not use OFFSET")
	}
}

func TestGatewayAccountCandidateSQLDoesNotDowngradeOrphanedAuthorizationInstances(t *testing.T) {
	directCandidateClause := `accounts.authorization_instance_authorization_id IS NULL
      AND accounts.authorization_instance_source_account_id IS NULL
      AND accounts.authorization_instance_owner_system_account_id IS NULL
      AND group_accounts.account_authorization_id IS NULL
      AND accounts.type IN ('api_key', 'oauth', 'google_oauth')`
	if !strings.Contains(listGatewayAccountCandidatesSQL, directCandidateClause) {
		t.Fatal("direct candidates must exclude orphaned authorization-instance markers")
	}
}

func TestScanGatewayAccountCandidateMapsDirectAndResourceFields(t *testing.T) {
	now := time.Date(2026, 7, 22, 8, 9, 10, 0, time.UTC)
	later := now.Add(time.Hour)
	values := []any{
		"acc_instance", "sys_grantee", "group_1", pgtype.Text{String: "auth_account", Valid: true},
		3, true, false, now,
		"gpt", "profile_1", "openai", "v1", "实例", "oauth", "active", true,
		20, 4, false, true, "codex_responses", "encrypted_instance",
		pgtype.Text{String: "proxy_instance", Valid: true}, pgtype.Text{String: `{}`, Valid: true},
		pgtype.Timestamptz{Time: later, Valid: true}, pgtype.Timestamptz{}, 7,
		pgtype.Text{String: "acc_source", Valid: true}, pgtype.Text{String: "auth_account", Valid: true}, pgtype.Text{String: "sys_owner", Valid: true},
		pgtype.Timestamptz{Time: later, Valid: true}, pgtype.Text{String: `{"total":{"enabled":true}}`, Valid: true},
		pgtype.Text{String: "team", Valid: true}, pgtype.Text{String: "team_1", Valid: true},
		pgtype.Text{String: "acc_source", Valid: true}, pgtype.Text{String: "gpt", Valid: true}, pgtype.Text{String: "profile_1", Valid: true},
		pgtype.Text{String: "openai", Valid: true}, pgtype.Text{String: "v1", Valid: true}, pgtype.Text{String: "oauth", Valid: true},
		pgtype.Text{String: "active", Valid: true}, pgtype.Bool{Bool: true, Valid: true}, pgtype.Text{String: "encrypted_source", Valid: true},
		pgtype.Text{String: "proxy_source", Valid: true}, pgtype.Timestamptz{}, pgtype.Timestamptz{Time: later, Valid: true},
		pgtype.Int4{Int32: 99, Valid: true}, pgtype.Text{String: "openai_standard", Valid: true},
	}
	candidate, err := scanGatewayAccountCandidate(func(dest ...any) error {
		if len(dest) != len(values) {
			t.Fatalf("scan destinations = %d, values = %d", len(dest), len(values))
		}
		for index := range dest {
			reflect.ValueOf(dest[index]).Elem().Set(reflect.ValueOf(values[index]))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scanGatewayAccountCandidate() error = %v", err)
	}
	if candidate.AccountID != "acc_instance" || candidate.AccountAuthorizationID != "auth_account" || candidate.ConfigRevision != 7 {
		t.Fatalf("candidate direct fields = %+v", candidate)
	}
	if candidate.AuthorizationOwnerSystemAccountID != "sys_owner" || candidate.AuthorizationSourceTeamID != "team_1" {
		t.Fatalf("candidate authorization fields = %+v", candidate)
	}
	if candidate.ResourceAccountID != "acc_source" || candidate.ResourceCredentialsEncrypted != "encrypted_source" || candidate.ResourceConcurrencyLimit != 99 {
		t.Fatalf("candidate resource fields = %+v", candidate)
	}
	if candidate.CooldownUntil == nil || !candidate.CooldownUntil.Equal(later) || candidate.ResourceAccountExpiresAt == nil || !candidate.ResourceAccountExpiresAt.Equal(later) {
		t.Fatalf("candidate time fields = %+v", candidate)
	}
}
