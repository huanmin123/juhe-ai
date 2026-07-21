package postgres

import (
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementAccountTestDispatchSQLKeepsScopeAndStateBoundaries(t *testing.T) {
	checks := map[string][]string{
		managementAccountTestDispatchResolveSQL: {"juhe_business.accounts", "deleted_at IS NULL", "resource_authorizations", "authorization_instance_authorization_id"},
		managementAccountTestDispatchCreateSQL:  {"account_test_tasks", "'queued'", "request_system_account_filter_id", "diagnostics", "draft_account_encrypted"},
		managementAccountTestDispatchSessionSQL: {"account_test_sessions", "status = 'running'", "account_test_session_tasks"},
		managementAccountTestDispatchFailSQL:    {"status = 'failed'", "status = 'queued'", "finished_at"},
	}
	for query, fragments := range checks {
		for _, fragment := range fragments {
			if !strings.Contains(query, fragment) {
				t.Fatalf("query missing %q: %s", fragment, query)
			}
		}
	}
	if strings.Contains(managementAccountTestDispatchResolveSQL, "resource_authorizations.status IN ('active', 'paused', 'expired')") {
		t.Fatal("account test dispatch still accepts paused or expired authorizations")
	}
	if !strings.Contains(managementAccountTestDispatchResolveSQL, "resource_authorizations.status = 'active'") {
		t.Fatal("account test dispatch does not require an active authorization")
	}
	if !strings.Contains(managementAccountTestDispatchResolveSQL, "resource_authorizations.expires_at > now()") {
		t.Fatal("account test dispatch does not reject elapsed authorization expiry")
	}
	if strings.Count(managementAccountTestDispatchResolveSQL, "accounts.deleted_at IS NULL") < 2 {
		t.Fatal("account test dispatch does not exclude deleted owner and authorized account rows")
	}
	if !strings.Contains(managementAccountTestDispatchResolveSQL, "accounts.authorization_instance_owner_system_account_id = resource_authorizations.resource_owner_system_account_id") {
		t.Fatal("account test dispatch does not validate authorized instance owner consistency")
	}
}

func TestStoreImplementsManagementAccountTestDispatchStore(t *testing.T) {
	var _ port.ManagementAccountTestDispatchStore = (*Store)(nil)
}
