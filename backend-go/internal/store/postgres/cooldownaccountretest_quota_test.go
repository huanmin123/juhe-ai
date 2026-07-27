package postgres

import (
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestCooldownAccountRetestQuotaSubjectQueryIsBatchAndFailClosed(t *testing.T) {
	for _, want := range []string{
		"a.id = ANY($1::text[])",
		"ra.resource_type = 'account'",
		"ra.resource_id = a.authorization_instance_source_account_id",
		"ra.grantee_system_account_id = a.system_account_id",
		"ra.status = 'active'",
		"ra.expires_at > $2",
		"grant_rows.grantee_team_id = ra.effective_source_team_id",
		"grant_rows.status = 'active'",
		"grant_rows.expires_at > $2",
		"source_accounts.deleted_at IS NULL",
		"authorization_valid",
	} {
		if !strings.Contains(loadCooldownAccountRetestQuotaSubjectsSQL, want) {
			t.Fatalf("quota subject query missing %q", want)
		}
	}
	for _, forbidden := range []string{"OFFSET", "usage_records", "usage_stats_"} {
		if strings.Contains(loadCooldownAccountRetestQuotaSubjectsSQL, forbidden) {
			t.Fatalf("quota subject query must not contain %q", forbidden)
		}
	}
}

func TestUniqueCooldownAccountRetestQuotaAccountIDs(t *testing.T) {
	got := uniqueCooldownAccountRetestQuotaAccountIDs([]string{" a ", "", "b", "a", " b ", "c"})
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("uniqueCooldownAccountRetestQuotaAccountIDs() = %#v, want %#v", got, want)
	}
}

func TestParseCooldownAccountRetestQuotaLimitsMatchesStrictNodeShape(t *testing.T) {
	limits, err := parseCooldownAccountRetestQuotaLimits(pgtype.Text{String: `{"hourly":{"enabled":true,"hours":6,"limit":1.25},"daily":{"enabled":true,"limit":2}}`, Valid: true})
	if err != nil {
		t.Fatalf("parseCooldownAccountRetestQuotaLimits() error = %v", err)
	}
	if limits.Hourly == nil || limits.Hourly.Hours != 6 || limits.Hourly.Limit != 1.25 || limits.Daily == nil || limits.Daily.Limit != 2 {
		t.Fatalf("limits = %#v", limits)
	}

	for _, raw := range []string{
		`{"hourly":null}`,
		`{"daily":null}`,
		`{"weekly":null}`,
		`{"monthly":null}`,
		`{"total":null}`,
		`{"daily":{"enabled":false,"limit":1}}`,
		`{"hourly":{"enabled":true,"hours":0,"limit":1}}`,
		`{"total":{"enabled":true,"limit":0}}`,
		`{"daily":{"enabled":true,"limit":1,"unexpected":true}}`,
		`{"unexpected":{}}`,
		`{} {}`,
	} {
		if _, err := parseCooldownAccountRetestQuotaLimits(pgtype.Text{String: raw, Valid: true}); err == nil {
			t.Fatalf("parseCooldownAccountRetestQuotaLimits(%s) error = nil, want strict rejection", raw)
		}
	}
}
