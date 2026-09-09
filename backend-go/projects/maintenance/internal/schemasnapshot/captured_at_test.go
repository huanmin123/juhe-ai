package schemasnapshot

import (
	"testing"
	"time"
)

func TestFormatCapturedAtUsesDayOfMonth(t *testing.T) {
	now := time.Date(2026, time.September, 10, 11, 12, 13, 456000000, time.FixedZone("CST", 8*60*60))
	got := formatCapturedAt(now)
	want := "2026-09-10T03:12:13.456Z"
	if got != want {
		t.Fatalf("formatCapturedAt() = %q, want %q", got, want)
	}
}
