package accounthealth

import (
	"context"
	"database/sql"
	"testing"
)

// TestSeedDirectInputBaseline 锚定 BUG-0194 修复语义：白名单过滤、幂等
// （重复执行零写入）、既有版本行不被基线覆盖。
func TestSeedDirectInputBaseline(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE accounts (
		id text PRIMARY KEY, deleted_at text, provider_code text, type text)`); err != nil {
		t.Fatalf("create accounts: %v", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE account_health_jobs_input_versions (
		account_id text PRIMARY KEY, current_version integer NOT NULL, reserved_at text NOT NULL)`); err != nil {
		t.Fatalf("create versions: %v", err)
	}
	seedAccounts := []struct{ id, deletedAt, provider, accountType string }{
		{"acc-whitelist-1", "", "openai", "oauth"},
		{"acc-whitelist-2", "", "glm", "api_key"},
		{"acc-deleted", "2026-01-01T00:00:00.000Z", "openai", "oauth"},
		{"acc-provider-excluded", "", "newapi", "api_key"},
		{"acc-type-excluded", "", "openai", "web"},
		{"acc-preexisting", "", "openai", "oauth"},
	}
	for _, account := range seedAccounts {
		deletedAt := "NULL"
		if account.deletedAt != "" {
			deletedAt = "'" + account.deletedAt + "'"
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO accounts (id, deleted_at, provider_code, type)
			VALUES ('`+account.id+`', `+deletedAt+`, '`+account.provider+`', '`+account.accountType+`')`); err != nil {
			t.Fatalf("seed account %s: %v", account.id, err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO account_health_jobs_input_versions
		VALUES ('acc-preexisting', 3, '2026-01-01T00:00:00.000Z')`); err != nil {
		t.Fatalf("seed preexisting version: %v", err)
	}

	seeded, err := seedDirectInputBaseline(ctx, db, "", "?")
	if err != nil {
		t.Fatalf("first seed: %v", err)
	}
	if seeded != 2 {
		t.Fatalf("first seed inserted %d rows, want 2", seeded)
	}
	var version int64
	if err := db.QueryRowContext(ctx, `SELECT current_version FROM account_health_jobs_input_versions
		WHERE account_id = 'acc-preexisting'`).Scan(&version); err != nil {
		t.Fatalf("read preexisting version: %v", err)
	}
	if version != 3 {
		t.Fatalf("preexisting version = %d, want 3 (基线不得覆盖既有行)", version)
	}
	var baselineVersion int64
	if err := db.QueryRowContext(ctx, `SELECT current_version FROM account_health_jobs_input_versions
		WHERE account_id = 'acc-whitelist-1'`).Scan(&baselineVersion); err != nil {
		t.Fatalf("read baseline version: %v", err)
	}
	if baselineVersion != 1 {
		t.Fatalf("baseline version = %d, want 1", baselineVersion)
	}

	reseeded, err := seedDirectInputBaseline(ctx, db, "", "?")
	if err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if reseeded != 0 {
		t.Fatalf("second seed inserted %d rows, want 0 (幂等)", reseeded)
	}
}
