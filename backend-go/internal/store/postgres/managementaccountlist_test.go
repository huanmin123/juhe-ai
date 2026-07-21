package postgres

import (
	"strings"
	"testing"
)

func TestManagementAccountListSQLIncludesHealthCheckFields(t *testing.T) {
	sql := listManagementAccountsSQL
	for _, fragment := range []string{
		"a.health_check_model",
		"a.health_check_endpoint_mode",
		"v.health_check_model",
		"v.health_check_endpoint_mode",
	} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("list management accounts SQL missing %s", fragment)
		}
	}
	if strings.Count(sql, "health_check_model") < 3 {
		t.Fatalf("expected owner/authorized/outer health_check_model projections, got %d", strings.Count(sql, "health_check_model"))
	}
}
