package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestAccountRuntimeDiagnosticsMigrationContainsRequiredColumns(t *testing.T) {
	source, err := os.ReadFile("../../../db/migrations/000064_w1_account_runtime_diagnostics.sql")
	if err != nil {
		t.Fatalf("read account runtime diagnostics migration: %v", err)
	}

	sql := string(source)
	for _, want := range []string{
		"ADD COLUMN IF NOT EXISTS cooldown_retest_failure_count integer NOT NULL DEFAULT 0",
		"ADD COLUMN IF NOT EXISTS cooldown_retest_observation_started_at timestamptz",
		"ADD COLUMN IF NOT EXISTS cooldown_retest_last_at timestamptz",
		"ADD COLUMN IF NOT EXISTS cooldown_retest_last_status_code integer",
		"ADD COLUMN IF NOT EXISTS last_health_check_at timestamptz",
		"ADD COLUMN IF NOT EXISTS last_health_success_at timestamptz",
		"ADD COLUMN IF NOT EXISTS stream_failure_count integer NOT NULL DEFAULT 0",
		"ADD COLUMN IF NOT EXISTS stream_failure_window_started_at timestamptz",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("account runtime diagnostics migration missing %q", want)
		}
	}
}
