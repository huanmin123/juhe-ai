package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestManagementAccountDetailsSQLMatchesCurrentScopeAndRawFieldContract(t *testing.T) {
	source, err := os.ReadFile("queries/w2_management_account_details.sql")
	if err != nil {
		t.Fatalf("read management account detail query: %v", err)
	}
	sql := string(source)
	for _, want := range []string{
		"-- name: GetManagementAccountDetailSource :one",
		"accounts.authorization_instance_source_account_id IS NULL",
		"resource_authorizations.status IN ('active', 'paused', 'expired')",
		"resource_authorizations.grantee_system_account_id = accounts.system_account_id",
		"sqlc.arg(system_account_id)::text = ''",
		"'owner'::text AS access_type",
		"'authorized'::text AS access_type",
		"accounts.credentials_encrypted",
		"''::text AS credentials_encrypted",
		"juhe_business.account_supported_models",
		"juhe_business.account_model_mappings",
		"juhe_business.account_tag_bindings",
		"juhe_business.account_api_key_runtime_states",
		"last_error_message",
		"last_trace_id",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("management account detail query missing %q", want)
		}
	}
	for _, forbidden := range []string{"[redacted]", "sanit", "regexp_replace", "credentials_mask"} {
		if strings.Contains(strings.ToLower(sql), strings.ToLower(forbidden)) {
			t.Fatalf("management account detail query contains forbidden transformation %q", forbidden)
		}
	}
}
