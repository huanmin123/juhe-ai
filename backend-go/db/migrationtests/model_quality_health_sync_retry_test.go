package migrationtests

import (
	"os"
	"strings"
	"testing"
)

func TestModelQualityHealthSyncRetryMigrationKeepsNodeFactCompatibilityAndAddsGoFencing(t *testing.T) {
	const migrationName = "000085_w7_model_quality_health_sync_retry.sql"
	source, err := os.ReadFile(migrationPath(migrationName))
	if err != nil {
		t.Fatalf("read %s: %v", migrationName, err)
	}
	up, down, found := strings.Cut(string(source), "-- +goose Down")
	if !found {
		t.Fatal("migration is missing goose Down marker")
	}

	// A fresh Goose database must expose the current Node-owned run fact with
	// the same TEXT/INTEGER physical representation used during coexistence.
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS juhe_dataset.model_check_runs",
		"id text PRIMARY KEY",
		"system_account_id text NOT NULL",
		"actor_system_account_id text NOT NULL",
		"provider_code text NOT NULL",
		"target_type text NOT NULL",
		"target_id text NOT NULL",
		"target_name text",
		"target_owner_system_account_id text",
		"account_id text",
		"group_id text",
		"api_key_id text",
		"model text NOT NULL",
		"profile text NOT NULL DEFAULT 'quick'",
		"trigger_kind text NOT NULL DEFAULT 'manual'",
		"schedule_id text",
		"trusted_comparison_enabled integer NOT NULL DEFAULT 0",
		"trusted_comparison_available integer NOT NULL DEFAULT 0",
		"level text NOT NULL DEFAULT 'unavailable'",
		"score integer NOT NULL DEFAULT 0",
		"max_score integer NOT NULL DEFAULT 100",
		"status text NOT NULL DEFAULT 'running'",
		"message text NOT NULL DEFAULT ''",
		"trace_id text",
		"probe_set_version text NOT NULL DEFAULT 'openai-model-check-v1'",
		"started_at text NOT NULL",
		"finished_at text",
		"duration_ms integer",
		"request_summary_json text NOT NULL DEFAULT '{}'",
		"result_summary_json text NOT NULL DEFAULT '{}'",
		"policy_snapshot_json text NOT NULL DEFAULT '{}'",
		"quality_decision_json text NOT NULL DEFAULT '{}'",
		"quality_health_sync_status text",
		"quality_health_sync_status IN ('applied', 'pending_retry', 'failed')",
		"error_code text",
		"error_message text",
		"created_at text NOT NULL",
		"updated_at text NOT NULL",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("migration Up section missing Node-compatible run fact %q", required)
		}
	}

	// Retry fencing is additive so an existing Node-created fact table can be
	// upgraded without replacing or rewriting its rows.
	for _, required := range []string{
		"ADD COLUMN IF NOT EXISTS quality_health_sync_claim_owner text",
		"ADD COLUMN IF NOT EXISTS quality_health_sync_claim_token text",
		"ADD COLUMN IF NOT EXISTS quality_health_sync_claim_epoch bigint NOT NULL DEFAULT 0",
		"CHECK (quality_health_sync_claim_epoch >= 0)",
		"ADD COLUMN IF NOT EXISTS quality_health_sync_claim_until text",
		"ADD COLUMN IF NOT EXISTS quality_health_sync_next_attempt_at text",
		"ADD COLUMN IF NOT EXISTS quality_health_sync_attempt_count bigint NOT NULL DEFAULT 0",
		"CHECK (quality_health_sync_attempt_count >= 0)",
		"ADD COLUMN IF NOT EXISTS quality_health_sync_last_error_class text",
		"ADD COLUMN IF NOT EXISTS quality_health_sync_last_error_message text",
		"ADD COLUMN IF NOT EXISTS quality_health_sync_updated_at text",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("migration Up section missing additive retry fencing %q", required)
		}
	}

	compactUpSQL := strings.Join(strings.Fields(stripMigrationSQLLineComments(up)), " ")
	const dueIndex = "CREATE INDEX IF NOT EXISTS idx_model_check_runs_quality_health_sync_due ON juhe_dataset.model_check_runs ( COALESCE(quality_health_sync_next_attempt_at, updated_at), updated_at, id ) WHERE account_id IS NOT NULL AND status = 'completed' AND quality_health_sync_status = 'failed';"
	if !strings.Contains(compactUpSQL, dueIndex) {
		t.Fatal("migration Up section must add the failed-run due-work partial index in retry ordering")
	}

	upSQL := strings.ToLower(stripMigrationSQLLineComments(up))
	for _, checkClause := range migrationSQLCheckClauses(upSQL) {
		if strings.Contains(checkClause, "quality_health_sync_claim_owner") &&
			strings.Contains(checkClause, "quality_health_sync_claim_token") &&
			strings.Contains(checkClause, "quality_health_sync_claim_until") {
			t.Fatalf("migration must not add an owner/token/until tuple CHECK while Node can write unfenced rows: %q", checkClause)
		}
	}
	for _, forbidden := range []string{
		"timestamptz",
		"jsonb",
		"boolean not null default",
		"default now()",
	} {
		if strings.Contains(upSQL, forbidden) {
			t.Fatalf("migration must preserve the Node TEXT/INTEGER physical contract, found %q", forbidden)
		}
	}

	downSQL := strings.ToLower(stripMigrationSQLLineComments(down))
	if !strings.Contains(strings.ToLower(down), "forward-only shared-schema safety fence") ||
		!strings.Contains(downSQL, "select 1;") {
		t.Fatal("migration Down section must be an executable forward-only shared-schema safety fence")
	}
	for _, destructive := range []string{"drop table", "drop column", "delete from", "truncate"} {
		if strings.Contains(downSQL, destructive) {
			t.Fatalf("migration Down section must retain shared facts and retry fencing, found %q", destructive)
		}
	}
}

func stripMigrationSQLLineComments(source string) string {
	lines := strings.Split(strings.ReplaceAll(source, "\r\n", "\n"), "\n")
	for index, line := range lines {
		if comment := strings.Index(line, "--"); comment >= 0 {
			lines[index] = line[:comment]
		}
	}
	return strings.Join(lines, "\n")
}

func migrationSQLCheckClauses(source string) []string {
	var clauses []string
	for offset := 0; offset < len(source); {
		relative := strings.Index(source[offset:], "check")
		if relative < 0 {
			break
		}
		keyword := offset + relative
		offset = keyword + len("check")
		if (keyword > 0 && isMigrationSQLIdentifierByte(source[keyword-1])) ||
			(offset < len(source) && isMigrationSQLIdentifierByte(source[offset])) {
			continue
		}
		for offset < len(source) && (source[offset] == ' ' || source[offset] == '\t' || source[offset] == '\r' || source[offset] == '\n') {
			offset++
		}
		if offset >= len(source) || source[offset] != '(' {
			continue
		}

		start := offset
		depth := 0
		for ; offset < len(source); offset++ {
			switch source[offset] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					offset++
					clauses = append(clauses, source[start:offset])
					break
				}
			}
			if depth == 0 {
				break
			}
		}
	}
	return clauses
}

func isMigrationSQLIdentifierByte(value byte) bool {
	return value == '_' || value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}
