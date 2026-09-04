package statsverify

import (
	"testing"
	"time"
)

func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	location, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load location %q: %v", name, err)
	}
	return location
}

// TestDateKeyIn checks the Intl.DateTimeFormat('en-CA') equivalent date-key
// rendering across UTC, a fixed-offset style zone and a DST zone.
func TestDateKeyIn(t *testing.T) {
	cases := []struct {
		name     string
		instant  string
		timezone string
		want     string
	}{
		{name: "utc instant utc zone", instant: "2026-03-01T00:30:00Z", timezone: "UTC", want: "2026-03-01"},
		{name: "utc instant shanghai", instant: "2026-03-01T16:30:00Z", timezone: "Asia/Shanghai", want: "2026-03-02"},
		{name: "utc instant los angeles", instant: "2026-03-01T02:30:00Z", timezone: "America/Los_Angeles", want: "2026-02-28"},
		{name: "dst spring forward day", instant: "2026-03-08T10:30:00Z", timezone: "America/Los_Angeles", want: "2026-03-08"},
		{name: "leap day", instant: "2028-02-29T12:00:00Z", timezone: "UTC", want: "2028-02-29"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			instant, err := time.Parse(time.RFC3339, tc.instant)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			got := DateKeyIn(instant, mustLocation(t, tc.timezone))
			if got != tc.want {
				t.Fatalf("dateKey=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestHourKeyIn(t *testing.T) {
	instant, _ := time.Parse(time.RFC3339, "2026-03-01T16:30:00Z")
	if got := HourKeyIn(instant, mustLocation(t, "Asia/Shanghai")); got != "2026-03-02T00" {
		t.Fatalf("hourKey=%q", got)
	}
	if got := HourKeyIn(instant, time.UTC); got != "2026-03-01T16" {
		t.Fatalf("hourKey utc=%q", got)
	}
}

// TestNextDateKeyIn covers calendar rollover (day, month, year, leap) plus
// the invalid-input passthrough of nextDateKey.
func TestNextDateKeyIn(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "same month", in: "2026-03-01", want: "2026-03-02"},
		{name: "month rollover", in: "2026-02-28", want: "2026-03-01"},
		{name: "year rollover", in: "2026-12-31", want: "2027-01-01"},
		{name: "leap february", in: "2028-02-28", want: "2028-02-29"},
		{name: "non-leap february", in: "2026-02-28", want: "2026-03-01"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NextDateKeyIn(tc.in, time.UTC); got != tc.want {
				t.Fatalf("nextDateKey(%q)=%q, want %q", tc.in, got, tc.want)
			}
		})
	}
	if got := NextDateKey("not-a-date"); got != "not-a-date" {
		t.Fatalf("invalid input must pass through, got %q", got)
	}
	// Node parses in the host-local timezone; the UTC variant must agree
	// under time.UTC for pure calendar keys.
	if got := NextDateKey("2026-03-01"); got != "2026-03-02" {
		t.Fatalf("host-local nextDateKey=%q", got)
	}
}

func TestFixedUsageStatsDateKeys(t *testing.T) {
	keys := FixedUsageStatsDateKeys(time.UTC, "2026-03-15")
	if len(keys) != FixedRangeWindowDays {
		t.Fatalf("len=%d, want %d", len(keys), FixedRangeWindowDays)
	}
	if keys[len(keys)-1] != "2026-03-15" {
		t.Fatalf("last=%q", keys[len(keys)-1])
	}
	if keys[0] != "2026-02-13" {
		t.Fatalf("first=%q (31-day window ends 2026-03-15)", keys[0])
	}
	if got := FixedUsageStatsDateKeys(time.UTC, "bad"); got != nil {
		t.Fatalf("invalid todayKey must return nil, got %v", got)
	}
}

// TestParseRFC3339 rejects offset-less instants, mirroring
// requiredRfc3339Instant's "Z or numeric offset" contract.
func TestParseRFC3339(t *testing.T) {
	if _, err := ParseRFC3339("2026-03-01T00:00:00Z", "x"); err != nil {
		t.Fatalf("valid Z rejected: %v", err)
	}
	if _, err := ParseRFC3339("2026-03-01T08:00:00+08:00", "x"); err != nil {
		t.Fatalf("valid offset rejected: %v", err)
	}
	if _, err := ParseRFC3339("2026-03-01 00:00:00", "x"); err == nil {
		t.Fatal("space-separated instant must be rejected")
	}
	if _, err := ParseRFC3339("2026-03-01T00:00:00", "x"); err == nil {
		t.Fatal("offset-less instant must be rejected")
	}
}
