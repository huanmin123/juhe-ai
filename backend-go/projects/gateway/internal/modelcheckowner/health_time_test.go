package modelcheckowner

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestLoadBusinessHealthStatHourUsesNodeSystemSetting(t *testing.T) {
	db := newJ3BPGDatabase(t,
		`CREATE SCHEMA IF NOT EXISTS juhe_business`,
		`CREATE TABLE juhe_business.system_settings (system_account_id TEXT NOT NULL,key TEXT NOT NULL,value_json TEXT NOT NULL,PRIMARY KEY(system_account_id,key))`,
		`INSERT INTO juhe_business.system_settings VALUES ('sys_admin','usageStatsTimezone','"Asia/Shanghai"')`)
	format, err := LoadBusinessHealthStatHour(context.Background(), db, true)
	if err != nil {
		t.Fatal(err)
	}
	got, err := format(time.Date(2026, 8, 27, 16, 30, 0, 0, time.UTC))
	if err != nil || got != "2026-08-28T00" {
		t.Fatalf("stat hour=%q err=%v", got, err)
	}
}

func TestLoadBusinessHealthStatHourFailsClosedWithoutValidSetting(t *testing.T) {
	db := newJ3BPGDatabase(t,
		`CREATE SCHEMA IF NOT EXISTS juhe_business`,
		`CREATE TABLE juhe_business.system_settings (system_account_id TEXT NOT NULL,key TEXT NOT NULL,value_json TEXT NOT NULL,PRIMARY KEY(system_account_id,key))`)
	if _, err := LoadBusinessHealthStatHour(context.Background(), db, true); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing setting err=%v", err)
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.system_settings VALUES ('sys_admin','usageStatsTimezone','123')`); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBusinessHealthStatHour(context.Background(), db, true); err == nil {
		t.Fatal("non-string setting must fail closed")
	}
}

func TestNewHealthStatHourFuncMatchesNodeUsageStatsTimezoneKey(t *testing.T) {
	format, err := NewHealthStatHourFunc("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	got, err := format(time.Date(2026, 8, 27, 18, 1, 2, 0, time.UTC))
	if err != nil || got != "2026-08-28T02" {
		t.Fatalf("stat hour=%q err=%v", got, err)
	}
}

func TestNewHealthStatHourFuncRejectsMissingAndInvalidTimezone(t *testing.T) {
	for _, timezone := range []string{"", "  ", "Mars/Olympus"} {
		if _, err := NewHealthStatHourFunc(timezone); err == nil {
			t.Fatalf("timezone %q must fail closed", timezone)
		}
	}
}

func TestStoreFormatHealthStatHourRequiresExplicitFormatter(t *testing.T) {
	store := &Store{}
	observedAt := time.Date(2026, 8, 27, 18, 1, 2, 0, time.UTC)
	if _, err := store.formatHealthStatHour(observedAt); err == nil || !strings.Contains(err.Error(), "timezone") {
		t.Fatalf("missing formatter err=%v", err)
	}
	store.HealthStatHour = func(time.Time) (string, error) { return "2026-08-28T02:00:00Z", nil }
	if _, err := store.formatHealthStatHour(observedAt); err == nil || !strings.Contains(err.Error(), "invalid key") {
		t.Fatalf("non-Node key err=%v", err)
	}
}

func mustHealthStatHourFunc(t *testing.T, timezone string) HealthStatHourFunc {
	t.Helper()
	format, err := NewHealthStatHourFunc(timezone)
	if err != nil {
		t.Fatal(err)
	}
	return format
}
