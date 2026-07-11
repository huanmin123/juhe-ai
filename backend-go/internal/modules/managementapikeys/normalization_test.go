package managementapikeys

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNormalizeMutationDescriptionUsesUTF16AndClearsBlankValues(t *testing.T) {
	for _, value := range []any{nil, "", "   "} {
		got, err := normalizeMutationDescription(value)
		if err != nil {
			t.Fatalf("normalizeMutationDescription(%#v) error = %v", value, err)
		}
		if got != nil {
			t.Fatalf("normalizeMutationDescription(%#v) = %#v, want nil", value, got)
		}
	}

	got, err := normalizeMutationDescription("  生产说明  ")
	if err != nil {
		t.Fatalf("normalizeMutationDescription() error = %v", err)
	}
	if got == nil || *got != "生产说明" {
		t.Fatalf("normalizeMutationDescription() = %#v", got)
	}

	if _, err := normalizeMutationDescription(strings.Repeat("😀", 101)); err == nil ||
		err.Error() != "API Key 说明不能超过 200 个字符" {
		t.Fatalf("UTF-16 length error = %v", err)
	}
}

func TestNormalizeMutationExpiresAtAcceptsOnlyUTCSecondsOrThreeMilliseconds(t *testing.T) {
	for _, value := range []any{nil, "", "   "} {
		got, err := normalizeMutationExpiresAt(value)
		if err != nil {
			t.Fatalf("normalizeMutationExpiresAt(%#v) error = %v", value, err)
		}
		if got != nil {
			t.Fatalf("normalizeMutationExpiresAt(%#v) = %#v, want nil", value, got)
		}
	}

	for _, value := range []string{
		"2026-07-31T23:59:58Z",
		"2026-07-31T23:59:58.123Z",
		"2020-01-01T00:00:00Z",
	} {
		got, err := normalizeMutationExpiresAt(value)
		if err != nil || got == nil {
			t.Fatalf("normalizeMutationExpiresAt(%q) = %v, %v", value, got, err)
		}
	}

	for _, value := range []any{
		1,
		"2026-07-31T23:59:58+08:00",
		"2026-07-31T23:59:58.1Z",
		"2026-07-31T23:59:58.1234Z",
	} {
		if _, err := normalizeMutationExpiresAt(value); err == nil {
			t.Fatalf("normalizeMutationExpiresAt(%#v) error = nil", value)
		}
	}
}

func TestNormalizeMutationQuotaLimitsPreservesExactDecimalRules(t *testing.T) {
	_, raw, hours, err := normalizeMutationQuotaLimits(map[string]any{
		"hourly": map[string]any{
			"enabled": true,
			"hours":   json.Number("6"),
			"limit":   json.Number("1.000001"),
		},
		"daily": map[string]any{
			"enabled": true,
			"limit":   json.Number("9.007199254740991e15"),
		},
	})
	if err != nil {
		t.Fatalf("normalizeMutationQuotaLimits() error = %v", err)
	}
	if raw == nil || !strings.Contains(*raw, "9.007199254740991e15") ||
		hours == nil || *hours != 6 {
		t.Fatalf("normalized quota raw=%v hours=%v", raw, hours)
	}

	for _, value := range []any{
		map[string]any{"daily": map[string]any{
			"enabled": true,
			"limit":   json.Number("9007199254740992"),
		}},
		map[string]any{"daily": map[string]any{
			"enabled": true,
			"limit":   json.Number("1.0000001"),
		}},
	} {
		if _, _, _, err := normalizeMutationQuotaLimits(value); err == nil {
			t.Fatalf("normalizeMutationQuotaLimits(%#v) error = nil", value)
		}
	}
}

func TestNormalizeMutationAvailabilityScheduleFallsBackToUTC(t *testing.T) {
	now := time.Date(2026, 7, 13, 0, 30, 0, 0, time.UTC)
	schedule := map[string]any{
		"enabled": true,
		"mode":    "allow_windows",
		"windows": []any{map[string]any{
			"daysOfWeek": []any{json.Number("1")},
			"start":      "08:00",
			"end":        "10:00",
		}},
	}
	reader := &managementAPIKeyCreateStoreStub{
		timezoneErr: errors.New("settings unavailable"),
	}

	_, raw, next, allowed, err := normalizeMutationAvailabilitySchedule(
		context.Background(),
		reader,
		schedule,
		now,
	)
	if err != nil {
		t.Fatalf("normalizeMutationAvailabilitySchedule() error = %v", err)
	}
	if raw == nil || next == nil || allowed {
		t.Fatalf("schedule raw=%v next=%v allowed=%t", raw, next, allowed)
	}
	var stored map[string]any
	if err := json.Unmarshal([]byte(*raw), &stored); err != nil {
		t.Fatalf("decode schedule: %v", err)
	}
	if stored["timezone"] != "UTC" {
		t.Fatalf("timezone = %#v, want UTC", stored["timezone"])
	}
}
