package postgres

import (
	"strings"
	"testing"
)

func TestManagementAccountImportSQLIsTransactionalAndScoped(t *testing.T) {
	for _, required := range []string{"BEGIN", "INSERT INTO juhe_business.accounts", "system_account_id", "group_accounts", "proxy_profiles"} {
		if !strings.Contains(managementAccountImportSQL, required) {
			t.Fatalf("SQL missing %q", required)
		}
	}
}
