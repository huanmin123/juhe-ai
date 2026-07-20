package postgres

import (
	"strings"
	"testing"
)

func TestAccountHealthCheckCandidatesSQLIsBoundedKeysetQuery(t *testing.T) {
	for _, fragment := range []string{"a.id > $1", "ORDER BY a.id", "LIMIT $2", "a.next_health_check_at <= $3", "a.config_revision"} {
		if !strings.Contains(accountHealthCheckCandidatesSQL, fragment) {
			t.Fatalf("candidate SQL missing %q", fragment)
		}
	}
}
