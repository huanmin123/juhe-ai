package migrations

import (
	"os"
	"strings"
	"testing"
)

func TestRemoveCooldownLongTermIntervalSettingMigration(t *testing.T) {
	source, err := os.ReadFile("000044_w5_remove_cooldown_long_term_interval_setting.sql")
	if err != nil {
		t.Fatalf("read cooldown long-term interval setting migration: %v", err)
	}
	up, down, found := strings.Cut(string(source), "-- +goose Down")
	if !found {
		t.Fatal("migration is missing goose Down marker")
	}
	if !strings.Contains(up, "DELETE FROM juhe_business.system_settings") ||
		!strings.Contains(up, "WHERE key = 'cooldownAccountRetestLongTermIntervalHours'") {
		t.Fatal("migration Up must remove the obsolete configurable interval")
	}
	for _, required := range []string{
		"INSERT INTO juhe_business.system_settings",
		"'sys_admin', 'cooldownAccountRetestLongTermIntervalHours', '1'",
		"ON CONFLICT (system_account_id, key) DO NOTHING",
	} {
		if !strings.Contains(down, required) {
			t.Fatalf("migration Down section missing %q", required)
		}
	}
}
