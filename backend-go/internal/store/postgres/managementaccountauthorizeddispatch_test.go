package postgres

import (
	"strings"
	"testing"
)

func TestManagementAccountAuthorizedDispatchSQLKeepsAuthorizedBindingScopeAndTransactionFields(t *testing.T) {
	for _, fragment := range []string{"authorization_instance_authorization_id IS NOT NULL", "account_authorization_id = accounts.authorization_instance_authorization_id", "FOR UPDATE OF accounts, group_accounts"} {
		if !strings.Contains(lockManagementAccountAuthorizedDispatchTargetSQL, fragment) {
			t.Fatalf("lock SQL missing %q", fragment)
		}
	}
	for _, fragment := range []string{"cooldown_until = NULL", "last_error_trace_id = NULL", "stream_failure_count = 0"} {
		if !strings.Contains(updateManagementAccountAuthorizedDispatchStateSQL, fragment) {
			t.Fatalf("state SQL missing %q", fragment)
		}
	}
	for _, fragment := range []string{"local_priority = $1", "local_super_priority_enabled = $2", "local_fallback_enabled = $3", "account_authorization_id = $8"} {
		if !strings.Contains(updateManagementAccountAuthorizedDispatchBindingSQL, fragment) {
			t.Fatalf("binding SQL missing %q", fragment)
		}
	}
}
