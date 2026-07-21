package migrationtests

import (
	"os"
	"strings"
	"testing"
)

func TestW6AuditLogsSchemaPreservesSharedNodeWriterTables(t *testing.T) {
	const migrationName = "000069_w6_audit_logs_read_schema.sql"
	source, err := os.ReadFile(migrationPath(migrationName))
	if err != nil {
		t.Fatalf("read %s: %v", migrationName, err)
	}
	sql := strings.ReplaceAll(string(source), "\r\n", "\n")
	for _, want := range []string{
		"CREATE SCHEMA IF NOT EXISTS juhe_dataset", "CREATE TABLE IF NOT EXISTS juhe_dataset.audit_logs",
		"CREATE TABLE IF NOT EXISTS juhe_dataset.audit_log_attempts", "CREATE TABLE IF NOT EXISTS juhe_dataset.audit_payload_blobs",
		"CREATE TABLE IF NOT EXISTS juhe_dataset.audit_payload_refs", "CREATE TABLE IF NOT EXISTS juhe_dataset.audit_error_groups",
		"FOREIGN KEY (audit_log_id) REFERENCES juhe_dataset.audit_logs(id) ON DELETE CASCADE",
		"UNIQUE (fingerprint, window_started_at)", "idx_audit_logs_system_trace_c_created_sort",
		"idx_audit_logs_system_client_ip_c_created_sort", "idx_audit_payload_blobs_unique",
		"-- no-op: audit tables are retained for the shared Node writer schema.",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("%s missing %q", migrationName, want)
		}
	}
}
