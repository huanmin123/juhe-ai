package postgres

import (
	"context"
	"testing"
)

func TestOpenRequiresURL(t *testing.T) {
	if _, err := Open(context.Background(), ""); err == nil {
		t.Fatal("Open() error = nil, want error")
	}
}

func TestParsePublicSettingValue(t *testing.T) {
	got, err := parsePublicSettingValue(`"聚合 AI"`, "appName")
	if err != nil {
		t.Fatalf("parsePublicSettingValue() error = %v", err)
	}
	if got != "聚合 AI" {
		t.Fatalf("parsePublicSettingValue() = %q", got)
	}
}

func TestParsePublicSettingValueRejectsNonString(t *testing.T) {
	if _, err := parsePublicSettingValue(`123`, "appName"); err == nil {
		t.Fatal("parsePublicSettingValue() error = nil, want non-string error")
	}
}

func TestParsePublicSettingValueRejectsBlankString(t *testing.T) {
	if _, err := parsePublicSettingValue(`" "`, "appName"); err == nil {
		t.Fatal("parsePublicSettingValue() error = nil, want blank string error")
	}
}

func TestParseIntegerSettingValue(t *testing.T) {
	got, err := parseIntegerSettingValue(`600`, "systemApiRateLimitIpReadPerMinute", 0, 1_000_000)
	if err != nil {
		t.Fatalf("parseIntegerSettingValue() error = %v", err)
	}
	if got != 600 {
		t.Fatalf("parseIntegerSettingValue() = %d, want 600", got)
	}
}

func TestParseIntegerSettingValueRejectsString(t *testing.T) {
	if _, err := parseIntegerSettingValue(`"600"`, "systemApiRateLimitIpReadPerMinute", 0, 1_000_000); err == nil {
		t.Fatal("parseIntegerSettingValue() error = nil, want string error")
	}
}

func TestParseIntegerSettingValueRejectsOutOfRange(t *testing.T) {
	if _, err := parseIntegerSettingValue(`1000001`, "systemApiRateLimitIpReadPerMinute", 0, 1_000_000); err == nil {
		t.Fatal("parseIntegerSettingValue() error = nil, want range error")
	}
}
