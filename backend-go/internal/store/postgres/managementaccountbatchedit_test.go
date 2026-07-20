package postgres

import (
	"strings"
	"testing"
)

func TestBatchEditContextQueryKeepsScopeAndDeletedGuards(t *testing.T) {
	for _, fragment := range []string{"deleted_at IS NULL", "system_account_id = $2", "ORDER BY id"} {
		if !strings.Contains(loadManagementAccountBatchEditContextSQL, fragment) {
			t.Fatalf("missing %q in query", fragment)
		}
	}
}
