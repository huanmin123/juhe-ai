package postgres

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestCooldownAccountRetestQueriesAreBoundedAndKeysetOrdered(t *testing.T) {
	for _, want := range []string{
		"LIMIT $6", "(a.cooldown_until, a.priority, a.created_at, a.id) > ($2, $3, $4, $5)",
		"ORDER BY a.cooldown_until ASC, a.priority ASC, a.created_at ASC, a.id ASC",
		"a.status IN ('temporary_unavailable', 'rate_limited')", "a.schedulable = true",
		"JOIN LATERAL", "resource_authorizations", "account_authorization_id = a.authorization_instance_authorization_id",
		"a.dispatch_revision", "a.cooldown_retest_observation_started_at IS NOT NULL",
		"a.cooldown_retest_generation IS NOT NULL", "CHR(160)", "CHR(65279)",
		"a.cooldown_retest_generation = btrim(a.cooldown_retest_generation",
		"a.authorization_instance_source_account_id IS NULL", "a.authorization_instance_owner_system_account_id IS NULL",
		"source_accounts.config_revision", "ra.resource_type = 'account'",
		"ra.resource_id = a.authorization_instance_source_account_id",
		"ra.resource_owner_system_account_id = source_accounts.system_account_id",
		"ra.grantee_system_account_id = a.system_account_id",
		"source_accounts.status = 'active'", "source_accounts.schedulable = true",
	} {
		if !strings.Contains(listCooldownAccountRetestCandidatesSQL, want) {
			t.Fatalf("candidate query missing %q", want)
		}
	}
	if strings.Contains(listCooldownAccountRetestCandidatesSQL, "OFFSET") {
		t.Fatal("candidate query must not use OFFSET")
	}
	for _, want := range []string{
		"a.dispatch_revision", "a.cooldown_retest_observation_started_at IS NOT NULL",
		"a.cooldown_retest_generation IS NOT NULL", "CHR(160)", "CHR(65279)",
		"a.cooldown_retest_generation = btrim(a.cooldown_retest_generation", "source_accounts.config_revision",
		"a.authorization_instance_source_account_id IS NULL", "ra.resource_type = 'account'",
	} {
		if !strings.Contains(findCooldownAccountRetestCandidateSQL, want) {
			t.Fatalf("find query missing %q", want)
		}
	}
	for _, query := range []string{listCooldownAccountRetestCandidatesSQL, findCooldownAccountRetestCandidateSQL} {
		for _, codePoint := range []int{
			9, 10, 11, 12, 13, 32, 160, 5760,
			8192, 8193, 8194, 8195, 8196, 8197, 8198, 8199, 8200, 8201, 8202,
			8232, 8233, 8239, 8287, 12288, 65279,
		} {
			if want := fmt.Sprintf("CHR(%d)", codePoint); !strings.Contains(query, want) {
				t.Fatalf("candidate query missing ECMAScript whitespace %q", want)
			}
		}
		if strings.Contains(query, "CHR(133)") {
			t.Fatal("candidate query must preserve non-ECMAScript U+0085 whitespace")
		}
	}
}

func TestScanCooldownAccountRetestCandidateCarriesFivePartFence(t *testing.T) {
	cooldownUntil := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	createdAt := cooldownUntil.Add(-time.Hour)
	observation := cooldownUntil.Add(-time.Minute)
	sourceRevision := 12
	candidate, err := scanCooldownAccountRetestCandidate(func(dest ...any) error {
		if len(dest) != 14 {
			t.Fatalf("scan destination count = %d, want 14", len(dest))
		}
		*dest[0].(*string) = "acct-1"
		*dest[1].(*string) = "Account 1"
		*dest[2].(*int) = 7
		*dest[3].(*int) = 8
		*dest[4].(*time.Time) = cooldownUntil
		*dest[5].(*int) = 9
		*dest[6].(*time.Time) = createdAt
		*dest[7].(**time.Time) = &observation
		*dest[8].(*string) = "generation-1"
		*dest[9].(**int) = &sourceRevision
		*dest[10].(*string) = "system-1"
		*dest[11].(*string) = "group-1"
		*dest[12].(*string) = "gpt-5"
		*dest[13].(*string) = "responses_json"
		return nil
	})
	if err != nil {
		t.Fatalf("scanCooldownAccountRetestCandidate() error = %v", err)
	}
	if candidate.ConfigRevision != 7 || candidate.DispatchRevision != 8 ||
		candidate.ObservationStartedAt == nil || !candidate.ObservationStartedAt.Equal(observation) ||
		candidate.Generation != "generation-1" || candidate.SourceConfigRevision == nil || *candidate.SourceConfigRevision != 12 {
		t.Fatalf("candidate fence = %+v", candidate)
	}
}
