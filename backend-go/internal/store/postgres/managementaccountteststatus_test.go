package postgres

import (
	"strings"
	"testing"
)

func TestManagementAccountTestQueriesAreScopedAndBounded(t *testing.T) {
	for name, query := range map[string]string{"session": accountTestSessionProjectionSQL, "task": accountTestTaskProjectionSQL, "session tasks": accountTestSessionTasksSQL} {
		if !strings.Contains(query, "request_system_account_id") || !strings.Contains(query, "request_system_account_filter_id") {
			t.Fatalf("%s query is not scoped", name)
		}
	}
	if !strings.Contains(accountTestSessionTasksSQL, "LIMIT $4") || !strings.Contains(accountTestSessionTasksSQL, "ORDER BY t.queued_at ASC, t.id ASC") {
		t.Fatal("session tasks query must be bounded and stable")
	}
}
