package statreads

import (
	"testing"
	"time"
)

func TestRowCalendarTextNormalizesDriverTime(t *testing.T) {
	hour := time.Date(2026, 9, 6, 11, 0, 0, 0, time.UTC)
	day := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	row := Row{"stat_hour": hour, "stat_date": day, "stat_month": day}
	if got := row.hour("stat_hour"); got != "2026-09-06T11" {
		t.Fatalf("hour = %q", got)
	}
	if got := row.day("stat_date"); got != "2026-09-06" {
		t.Fatalf("day = %q", got)
	}
	if got := row.month("stat_month"); got != "2026-09" {
		t.Fatalf("month = %q", got)
	}
	if got := row.nullDay("stat_date"); got == nil || *got != "2026-09-06" {
		t.Fatalf("nullDay = %v", got)
	}
}
