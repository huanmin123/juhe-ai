package postgres

import (
	"strings"
	"testing"
)

func TestManagementAccountExportSQLUsesBoundedCursorAndOwnerScope(t *testing.T) {
	required := []string{
		"accounts.system_account_id = $1",
		"accounts.authorization_instance_source_account_id IS NULL",
		"accounts.id > $10",
		"ORDER BY accounts.id",
		"LIMIT $11",
		"COUNT(*) OVER() AS matched_count",
		"account_supported_models",
		"account_model_mappings",
		"account_tag_bindings",
	}
	for _, fragment := range required {
		if !strings.Contains(managementAccountExportSQL, fragment) {
			t.Fatalf("account export SQL missing %q", fragment)
		}
	}
}

func TestNullableTextArrayTrimsAndDeduplicates(t *testing.T) {
	got := nullableTextArray([]string{" account-1 ", "", "account-1", "account-2"})
	if len(got) != 2 || got[0] != "account-1" || got[1] != "account-2" {
		t.Fatalf("nullableTextArray() = %#v", got)
	}
	if nullableTextArray(nil) != nil {
		t.Fatal("nullableTextArray(nil) should stay nil")
	}
}
