package postgres

import (
	"strings"
	"testing"
)

func TestAccountHealthCheckCandidatesSQLIsBoundedKeysetQuery(t *testing.T) {
	for _, fragment := range []string{
		"(CASE WHEN a.status = 'pending_test' THEN 0 ELSE 1 END, a.id) > ($1, $2)",
		"ORDER BY CASE WHEN a.status = 'pending_test' THEN 0 ELSE 1 END, a.id",
		"LIMIT $3",
		"a.next_health_check_at <= $4",
		"a.config_revision",
	} {
		if !strings.Contains(accountHealthCheckCandidatesSQL, fragment) {
			t.Fatalf("candidate SQL missing %q", fragment)
		}
	}
}

func TestAccountHealthCheckSQLAllowsValidAuthorizationInstances(t *testing.T) {
	for name, query := range map[string]string{
		"candidate scan": accountHealthCheckCandidatesSQL,
		"current check":  accountHealthCheckCurrentSQL,
	} {
		for _, fragment := range []string{
			"LEFT JOIN juhe_business.resource_authorizations AS ra",
			"LEFT JOIN juhe_business.accounts AS source",
			"ga.account_authorization_id = a.authorization_instance_authorization_id",
			"ra.status = 'active'",
			"ra.expires_at IS NULL OR ra.expires_at >",
			"source.status = 'active'",
			"source.schedulable = true",
		} {
			if !strings.Contains(query, fragment) {
				t.Fatalf("%s SQL missing %q", name, fragment)
			}
		}
		if strings.Contains(query, "a.authorization_instance_authorization_id IS NULL\n  AND") {
			t.Fatalf("%s SQL still excludes every authorization instance", name)
		}
	}
}
