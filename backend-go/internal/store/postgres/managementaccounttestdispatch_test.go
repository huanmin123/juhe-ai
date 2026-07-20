package postgres

import (
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementAccountTestDispatchSQLKeepsScopeAndStateBoundaries(t *testing.T) {
	checks := map[string][]string{
		managementAccountTestDispatchResolveSQL: {"juhe_business.accounts", "deleted_at IS NULL", "authorization_instance_authorization_id"},
		managementAccountTestDispatchCreateSQL:  {"account_test_tasks", "'queued'", "request_system_account_filter_id"},
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
}

func TestStoreImplementsManagementAccountTestDispatchStore(t *testing.T) {
	var _ port.ManagementAccountTestDispatchStore = (*Store)(nil)
}
