package migrationtests

import (
	"os"
	"strings"
	"testing"
)

func TestAccountHealthCheckHourlyDefaultsMigration(t *testing.T) {
	source, err := os.ReadFile(migrationPath("000080_w7_account_health_check_hourly_defaults.sql"))
	if err != nil {
		t.Fatalf("read account health-check hourly defaults migration: %v", err)
	}
	up, down, found := strings.Cut(string(source), "-- +goose Down")
	if !found {
		t.Fatal("migration is missing goose Down marker")
	}
	for _, required := range []string{
		"legacy_defaults",
		"key = 'accountHealthCheckIntervalHours' AND value_json = '12'",
		"key = 'accountHealthCheckJitterMinutes' AND value_json = '120'",
		"WHEN 'accountHealthCheckIntervalHours' THEN '1'",
		"WHEN 'accountHealthCheckJitterMinutes' THEN '10'",
		"UPDATE juhe_business.accounts AS accounts",
		"accounts.next_health_check_at <= now()",
		"hashtextextended(accounts.id, 0)",
		"600)::integer",
		"accounts.status = 'active'",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("migration Up section missing %q", required)
		}
	}
	for _, required := range []string{
		"hourly_defaults",
		"WHEN 'accountHealthCheckIntervalHours' THEN '12'",
		"WHEN 'accountHealthCheckJitterMinutes' THEN '120'",
	} {
		if !strings.Contains(down, required) {
			t.Fatalf("migration Down section missing %q", required)
		}
	}
}
