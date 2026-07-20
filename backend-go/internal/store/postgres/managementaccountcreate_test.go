package postgres

import (
	"strings"
	"testing"
)

func TestManagementAccountCreateSQLChecksProviderProfileAndGroup(t *testing.T) {
	for _, required := range []string{"providers.enabled = true", "profiles.enabled = true", "account_types_json", "groups.provider_code = $2", "INSERT INTO juhe_business.accounts"} {
		if !strings.Contains(managementAccountCreateSQL, required) {
			t.Fatalf("SQL missing %q", required)
		}
	}
	if !strings.Contains(managementAccountCreateGroupBindingSQL, "INSERT INTO juhe_business.group_accounts") {
		t.Fatal("group binding SQL missing group_accounts insert")
	}
}
