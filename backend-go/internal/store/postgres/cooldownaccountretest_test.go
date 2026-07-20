package postgres

import (
	"strings"
	"testing"
)

func TestCooldownAccountRetestQueriesAreBoundedAndKeysetOrdered(t *testing.T) {
	for _, want := range []string{
		"LIMIT $6", "(a.cooldown_until, a.priority, a.created_at, a.id) > ($2, $3, $4, $5)",
		"ORDER BY a.cooldown_until ASC, a.priority ASC, a.created_at ASC, a.id ASC",
		"a.status IN ('temporary_unavailable', 'rate_limited')", "a.schedulable = true",
		"JOIN LATERAL", "resource_authorizations", "account_authorization_id = a.authorization_instance_authorization_id",
	} {
		if !strings.Contains(listCooldownAccountRetestCandidatesSQL, want) {
			t.Fatalf("candidate query missing %q", want)
		}
	}
	if strings.Contains(listCooldownAccountRetestCandidatesSQL, "OFFSET") {
		t.Fatal("candidate query must not use OFFSET")
	}
}
