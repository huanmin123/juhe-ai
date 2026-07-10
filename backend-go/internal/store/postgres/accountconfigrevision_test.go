package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestAccountConfigRevisionMigrationAddsSharedVersionColumn(t *testing.T) {
	source, err := os.ReadFile("../../../db/migrations/000030_w1b_account_config_revision.sql")
	if err != nil {
		t.Fatalf("read account config revision migration: %v", err)
	}
	sql := string(source)
	for _, want := range []string{
		"ALTER TABLE juhe_business.accounts",
		"ADD COLUMN IF NOT EXISTS config_revision integer NOT NULL DEFAULT 1",
		"DROP COLUMN IF EXISTS config_revision",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("account config revision migration missing %q", want)
		}
	}
}
