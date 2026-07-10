package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestW1PublicSettingsMigrationSeedsAllSystemAPIRateLimits(t *testing.T) {
	source, err := os.ReadFile("../../../db/migrations/000002_w1_public_settings.sql")
	if err != nil {
		t.Fatalf("read W1 public settings migration: %v", err)
	}
	sql := string(source)

	for _, expected := range []string{
		"('sys_admin', 'systemApiRateLimitIpReadPerMinute', '600', now())",
		"('sys_admin', 'systemApiRateLimitIpReadBurstPer10Seconds', '120', now())",
		"('sys_admin', 'systemApiRateLimitIpWritePerMinute', '180', now())",
		"('sys_admin', 'systemApiRateLimitIpWriteBurstPer10Seconds', '40', now())",
		"('sys_admin', 'systemApiRateLimitUserReadPerMinute', '300', now())",
		"('sys_admin', 'systemApiRateLimitUserWritePerMinute', '120', now())",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("W1 public settings migration missing %q", expected)
		}
	}
}
