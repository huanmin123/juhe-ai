package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestAccountTestAndUsageSnapshotMigrationDefinesCurrentRuntimeTables(t *testing.T) {
	source, err := os.ReadFile("../../../db/migrations/000066_w2_account_test_and_usage_snapshot.sql")
	if err != nil {
		t.Fatalf("read account test and usage snapshot migration: %v", err)
	}

	sql := string(source)
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS juhe_stats.account_usage_snapshots",
		"PRIMARY KEY (system_account_id, account_id, kind)",
		"CREATE TABLE IF NOT EXISTS juhe_business.account_test_tasks",
		"request_role text NOT NULL",
		"CREATE TABLE IF NOT EXISTS juhe_business.account_test_sessions",
		"CREATE TABLE IF NOT EXISTS juhe_business.account_test_session_tasks",
		"FOREIGN KEY (session_id) REFERENCES juhe_business.account_test_sessions(id) ON DELETE CASCADE",
		"FOREIGN KEY (task_id) REFERENCES juhe_business.account_test_tasks(id) ON DELETE CASCADE",
		"idx_account_test_tasks_status_queued",
		"idx_account_test_sessions_status_heartbeat",
		"idx_account_usage_snapshots_kind_account",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("account test/runtime migration missing %q", want)
		}
	}
}

func TestManagementAccountTestDispatchPersistsRequestRole(t *testing.T) {
	if !strings.Contains(managementAccountTestDispatchCreateSQL, "request_role") {
		t.Fatal("account test dispatch does not persist request_role")
	}
}

func TestAccountTestPostgresTypeUpgradeMigrationUsesGoRuntimeTypes(t *testing.T) {
	source, err := os.ReadFile("../../../db/migrations/000067_w2_account_test_postgres_types.sql")
	if err != nil {
		t.Fatalf("read account test postgres type upgrade migration: %v", err)
	}

	sql := string(source)
	for _, want := range []string{
		"ALTER COLUMN cancel_requested TYPE boolean",
		"ELSE NULL",
		"ALTER COLUMN queued_at TYPE timestamptz",
		"ALTER COLUMN last_heartbeat_at TYPE timestamptz",
		"ALTER COLUMN updated_at TYPE timestamptz",
		"idx_account_test_session_tasks_task",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("account test postgres type migration missing %q", want)
		}
	}
}
