package migrationtests

import (
	"os"
	"strings"
	"testing"
)

func TestRuntimeLogsMigrationMatchesNodeReadContract(t *testing.T) {
	source, err := os.ReadFile(migrationPath("000045_w6_runtime_logs.sql"))
	if err != nil {
		t.Fatalf("read runtime logs migration: %v", err)
	}
	up, down, found := strings.Cut(string(source), "-- +goose Down")
	if !found {
		t.Fatal("migration is missing goose Down marker")
	}
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS juhe_dataset.runtime_logs",
		"id text PRIMARY KEY",
		"log_file text",
		"log_offset bigint",
		"line_number integer",
		"time text NOT NULL",
		"level text NOT NULL",
		"trace_id text",
		"event text",
		"message text",
		"error_message text",
		"raw_json text NOT NULL",
		"created_at text NOT NULL",
		"CREATE TABLE IF NOT EXISTS juhe_dataset.runtime_log_file_cursors",
		"cursor_offset bigint NOT NULL DEFAULT 0",
		"file_size bigint NOT NULL DEFAULT 0",
		"CREATE TABLE IF NOT EXISTS juhe_dataset.runtime_log_facet_summary",
		"total_count bigint NOT NULL DEFAULT 0",
		"CREATE TABLE IF NOT EXISTS juhe_dataset.runtime_log_level_facets",
		"CREATE TABLE IF NOT EXISTS juhe_dataset.runtime_log_event_facets",
		"CREATE INDEX IF NOT EXISTS idx_runtime_logs_time",
		"(time DESC, id DESC)",
		"CREATE INDEX IF NOT EXISTS idx_runtime_logs_trace_c_time",
		"((trace_id COLLATE \"C\"), time DESC, id DESC)",
		"CREATE INDEX IF NOT EXISTS idx_runtime_logs_level_time",
		"(level, time DESC, id DESC)",
		"CREATE INDEX IF NOT EXISTS idx_runtime_logs_event_time",
		"(event, time DESC, id DESC)",
		"CREATE INDEX IF NOT EXISTS idx_runtime_log_file_cursors_updated",
		"CREATE INDEX IF NOT EXISTS idx_runtime_log_facet_summary_latest",
		"CREATE INDEX IF NOT EXISTS idx_runtime_log_event_facets_latest",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("migration Up section missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"CREATE EXTENSION",
		"FOREIGN KEY",
		"REFERENCES ",
	} {
		if strings.Contains(up, forbidden) {
			t.Fatalf("runtime log read migration must not contain %q", forbidden)
		}
	}
	if !strings.Contains(down, "-- no-op:") || strings.Contains(down, "DROP TABLE") {
		t.Fatal("migration Down section must remain a non-destructive no-op")
	}
}
