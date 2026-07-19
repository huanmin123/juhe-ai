package migrationtests

import (
	"os"
	"strings"
	"testing"
)

func TestAnnouncementSchemaMatchesCurrentContract(t *testing.T) {
	source, err := os.ReadFile(migrationPath("000058_w8_announcements.sql"))
	if err != nil {
		t.Fatalf("read announcement migration: %v", err)
	}
	up, down, found := strings.Cut(string(source), "-- +goose Down")
	if !found {
		t.Fatal("announcement migration is missing goose Down marker")
	}

	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS juhe_business.announcements",
		"CREATE TABLE IF NOT EXISTS juhe_business.announcement_reads",
		"DROP CONSTRAINT IF EXISTS announcements_title_length_check",
		"ADD CONSTRAINT announcements_title_length_check",
		"ALTER COLUMN published_at TYPE timestamptz",
		"ALTER COLUMN read_at TYPE timestamptz",
		"CHECK (level IN ('critical', 'warning', 'info', 'normal'))",
		"CHECK (status IN ('draft', 'published', 'archived'))",
		"char_length(btrim(title)) BETWEEN 1 AND 120",
		"char_length(btrim(content)) BETWEEN 1 AND 5000",
		"REFERENCES juhe_business.system_accounts(id)",
		"REFERENCES juhe_business.announcements(id) ON DELETE CASCADE",
		"PRIMARY KEY (announcement_id, system_account_id)",
		"DROP INDEX IF EXISTS juhe_business.idx_announcement_reads_account",
		"CREATE INDEX idx_announcements_public_order",
		"WHERE status = 'published' AND published_at IS NOT NULL",
		"published_at DESC, created_at DESC, id DESC",
		"updated_at DESC, created_at DESC, id DESC",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("announcement migration Up section missing %q", required)
		}
	}
	if !strings.Contains(down, "-- no-op:") {
		t.Fatal("announcement migration Down section must remain a no-op")
	}
}
