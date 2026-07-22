package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestParsePublicAPILogRetentionDays(t *testing.T) {
	for _, raw := range []string{"1", "30", "365"} {
		value, err := parsePublicAPILogRetentionDays(raw)
		if err != nil {
			t.Fatalf("parsePublicAPILogRetentionDays(%q) error = %v", raw, err)
		}
		if value < 1 || value > 365 {
			t.Fatalf("parsePublicAPILogRetentionDays(%q) = %d", raw, value)
		}
	}
	for _, raw := range []string{"0", "366", "1.5", `"30"`, "invalid"} {
		if _, err := parsePublicAPILogRetentionDays(raw); err == nil {
			t.Fatalf("parsePublicAPILogRetentionDays(%q) error = nil", raw)
		}
	}
}

func TestPublicAPILogRetentionCleanupSQLIsBoundedStableAndCutoffSafe(t *testing.T) {
	data, err := os.ReadFile("queries/w1b_public_api.sql")
	if err != nil {
		t.Fatalf("read public API SQL: %v", err)
	}
	sql := string(data)
	for _, want := range []string{
		"GetPublicAPILogRetentionDays",
		"publicApiLogRetentionDays",
		"CleanupPublicAPILogsBefore",
		"created_at < sqlc.arg(cutoff_created_at)::timestamptz",
		"ORDER BY created_at ASC, id ASC",
		"LIMIT sqlc.arg(row_limit)::int",
		"WHERE id IN (SELECT id FROM stale_public_api_logs)",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("public API SQL missing %q", want)
		}
	}
}
