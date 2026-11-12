package statsagg

import "testing"

func TestCalendarDBValue(t *testing.T) {
	cases := []struct{ column, value, want string }{
		{"stat_month", "2026-04", "2026-04-01"},
		{"stat_hour", "2026-04-18T10", "2026-04-18 10:00:00"},
		{"stat_minute", "2026-04-18T10:15", "2026-04-18 10:15:00"},
		{"stat_date", "2026-04-18", "2026-04-18"},
	}
	for _, tc := range cases {
		if got := CalendarDBValue(tc.column, tc.value); got != tc.want {
			t.Fatalf("CalendarDBValue(%s, %s) = %s want %s", tc.column, tc.value, got, tc.want)
		}
	}
}
