package apikeyschedule

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNormalizeStrictlyValidatesAndCanonicalizesAvailabilitySchedule(t *testing.T) {
	now := time.Date(2026, 7, 13, 10, 30, 0, 0, time.UTC)
	got, allowed, err := Normalize(map[string]any{
		"enabled":  true,
		"timezone": " Asia/Shanghai ",
		"mode":     "allow_windows",
		"windows": []any{map[string]any{
			"daysOfWeek": []any{float64(7), float64(1), float64(7)},
			"start":      " 09:00 ",
			"end":        "18:00",
		}},
		"dateRange": map[string]any{
			"startDate": "2026-07-01",
			"endDate":   "2026-07-31",
		},
		"exceptions": []any{map[string]any{
			"date":   "2026-07-14",
			"action": "allow",
			"windows": []any{map[string]any{
				"start": "10:00",
				"end":   "12:00",
			}},
		}},
	}, now, "UTC")
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if allowed {
		t.Fatal("Normalize() allowed = true, want false outside normalized Monday window in Asia/Shanghai")
	}
	if got["timezone"] != "Asia/Shanghai" {
		t.Fatalf("timezone = %#v", got["timezone"])
	}
	windows, ok := got["windows"].([]map[string]any)
	if !ok || len(windows) != 1 {
		t.Fatalf("windows = %#v", got["windows"])
	}
	if !reflect.DeepEqual(windows[0]["daysOfWeek"], []int{1, 7}) ||
		windows[0]["start"] != "09:00" ||
		windows[0]["end"] != "18:00" {
		t.Fatalf("normalized window = %#v", windows[0])
	}
}

func TestAllowedAtReusesPersistedScheduleDecision(t *testing.T) {
	t.Parallel()
	raw := `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[7],"start":"08:00","end":"09:00"}]}`
	allowed, err := AllowedAt(&raw, time.Date(2026, 7, 26, 8, 30, 0, 0, time.UTC))
	if err != nil || !allowed {
		t.Fatalf("AllowedAt() = %v, %v", allowed, err)
	}
	allowed, err = AllowedAt(&raw, time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC))
	if err != nil || allowed {
		t.Fatalf("AllowedAt() boundary = %v, %v", allowed, err)
	}
	allowed, err = AllowedAt(nil, time.Now())
	if err != nil || !allowed {
		t.Fatalf("AllowedAt(nil) = %v, %v", allowed, err)
	}
}

func TestAllowedAtRejectsOversizedPersistedJSONBeforeDecode(t *testing.T) {
	t.Parallel()
	raw := strings.Repeat(" ", maxScheduleJSONBytes+1)
	if _, err := AllowedAt(&raw, time.Now()); err == nil || !strings.Contains(err.Error(), "过大") {
		t.Fatalf("AllowedAt() error = %v", err)
	}
}

func TestNormalizeRejectsStructurallyInvalidAvailabilitySchedules(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{
			"enabled":  true,
			"timezone": "UTC",
			"mode":     "allow_windows",
			"windows": []any{map[string]any{
				"daysOfWeek": []any{float64(1)},
				"start":      "09:00",
				"end":        "18:00",
			}},
		}
	}
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "unknown root field", mutate: func(value map[string]any) { value["unknown"] = true }},
		{name: "disabled", mutate: func(value map[string]any) { value["enabled"] = false }},
		{name: "wrong mode", mutate: func(value map[string]any) { value["mode"] = "deny_windows" }},
		{name: "null timezone", mutate: func(value map[string]any) { value["timezone"] = nil }},
		{name: "invalid timezone", mutate: func(value map[string]any) { value["timezone"] = "Invalid/Timezone" }},
		{name: "empty windows", mutate: func(value map[string]any) { value["windows"] = []any{} }},
		{name: "null date range", mutate: func(value map[string]any) { value["dateRange"] = nil }},
		{name: "null date range field", mutate: func(value map[string]any) {
			value["dateRange"] = map[string]any{"startDate": nil}
		}},
		{name: "null exceptions", mutate: func(value map[string]any) { value["exceptions"] = nil }},
		{name: "invalid day", mutate: func(value map[string]any) {
			value["windows"].([]any)[0].(map[string]any)["daysOfWeek"] = []any{float64(8)}
		}},
		{name: "equal endpoints", mutate: func(value map[string]any) {
			value["windows"].([]any)[0].(map[string]any)["end"] = "09:00"
		}},
		{name: "reverse date range", mutate: func(value map[string]any) {
			value["dateRange"] = map[string]any{"startDate": "2026-08-01", "endDate": "2026-07-01"}
		}},
		{name: "deny exception windows", mutate: func(value map[string]any) {
			value["exceptions"] = []any{map[string]any{
				"date":    "2026-07-13",
				"action":  "deny",
				"windows": []any{},
			}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := base()
			test.mutate(value)
			_, _, err := Normalize(value, time.Now(), "UTC")
			if err == nil {
				t.Fatal("Normalize() error = nil")
			}
		})
	}
}

func TestNormalizeOmitsEmptyOptionalScheduleSections(t *testing.T) {
	got, _, err := Normalize(map[string]any{
		"enabled":    true,
		"timezone":   "UTC",
		"mode":       "allow_windows",
		"windows":    []any{map[string]any{"daysOfWeek": []any{float64(1)}, "start": "09:00", "end": "18:00"}},
		"dateRange":  map[string]any{},
		"exceptions": []any{},
	}, time.Now(), "UTC")
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if _, exists := got["dateRange"]; exists {
		t.Fatalf("dateRange should be omitted: %#v", got)
	}
	if _, exists := got["exceptions"]; exists {
		t.Fatalf("exceptions should be omitted: %#v", got)
	}
}

func TestNormalizeAcceptsNodeCompatibleTimezoneCaseAndPreservesText(t *testing.T) {
	for _, timezone := range []string{"america/new_york", "ASIA/SHANGHAI"} {
		t.Run(timezone, func(t *testing.T) {
			got, _, err := Normalize(map[string]any{
				"enabled":  true,
				"timezone": timezone,
				"mode":     "allow_windows",
				"windows": []any{map[string]any{
					"daysOfWeek": []any{float64(1)},
					"start":      "09:00",
					"end":        "18:00",
				}},
			}, time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC), "UTC")
			if err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}
			if got["timezone"] != timezone {
				t.Fatalf("timezone = %#v, want %q", got["timezone"], timezone)
			}
		})
	}
}

func TestNextCheckAtFindsScheduleBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		schedule map[string]any
		now      time.Time
		want     time.Time
	}{
		{
			name:     "UTC window start",
			schedule: normalizedTestSchedule("UTC", []int{1}, "09:00", "18:00"),
			now:      time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC),
			want:     time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC),
		},
		{
			name:     "UTC window end and strict future boundary",
			schedule: normalizedTestSchedule("UTC", []int{1}, "09:00", "18:00"),
			now:      time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC),
			want:     time.Date(2026, 7, 13, 18, 0, 0, 0, time.UTC),
		},
		{
			name:     "cross midnight window end",
			schedule: normalizedTestSchedule("UTC", []int{1}, "22:00", "02:00"),
			now:      time.Date(2026, 7, 14, 0, 30, 0, 0, time.UTC),
			want:     time.Date(2026, 7, 14, 2, 0, 0, 0, time.UTC),
		},
		{
			name: "deny exception suppresses regular boundaries",
			schedule: withTestScheduleExceptions(
				normalizedTestSchedule("UTC", []int{1, 2, 3, 4, 5, 6, 7}, "09:00", "18:00"),
				[]map[string]any{{
					"date":   "2026-07-13",
					"action": "deny",
				}},
			),
			now:  time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC),
			want: time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC),
		},
		{
			name: "allow exception replaces regular boundaries",
			schedule: withTestScheduleExceptions(
				normalizedTestSchedule("UTC", []int{1}, "09:00", "18:00"),
				[]map[string]any{{
					"date":   "2026-07-13",
					"action": "allow",
					"windows": []map[string]any{{
						"start": "10:00",
						"end":   "12:00",
					}},
				}},
			),
			now:  time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC),
			want: time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC),
		},
		{
			name: "first duplicate exception wins",
			schedule: withTestScheduleExceptions(
				normalizedTestSchedule("UTC", []int{1, 2, 3, 4, 5, 6, 7}, "09:00", "18:00"),
				[]map[string]any{
					{
						"date":   "2026-07-13",
						"action": "deny",
					},
					{
						"date":   "2026-07-13",
						"action": "allow",
						"windows": []map[string]any{{
							"start": "10:00",
							"end":   "12:00",
						}},
					},
				},
			),
			now:  time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC),
			want: time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC),
		},
		{
			name: "date range filters earlier regular boundaries",
			schedule: withTestScheduleDateRange(
				normalizedTestSchedule("UTC", []int{1, 2, 3, 4, 5, 6, 7}, "09:00", "18:00"),
				"2026-07-14",
				"2026-07-20",
			),
			now:  time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC),
			want: time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC),
		},
		{
			name:     "Asia Shanghai converts local boundary to UTC",
			schedule: normalizedTestSchedule("Asia/Shanghai", []int{1}, "09:00", "18:00"),
			now:      time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC),
			want:     time.Date(2026, 7, 13, 1, 0, 0, 0, time.UTC),
		},
		{
			name:     "DST nonexistent local start is skipped",
			schedule: normalizedTestSchedule("America/New_York", []int{7}, "02:30", "03:30"),
			now:      time.Date(2026, 3, 8, 6, 0, 0, 0, time.UTC),
			want:     time.Date(2026, 3, 8, 7, 30, 0, 0, time.UTC),
		},
		{
			name: "falls back seven days when horizon has no boundary",
			schedule: withTestScheduleDateRange(
				normalizedTestSchedule("UTC", []int{1}, "09:00", "18:00"),
				"2027-01-01",
				"2027-01-31",
			),
			now:  time.Date(2026, 7, 13, 8, 15, 30, 0, time.UTC),
			want: time.Date(2026, 7, 20, 8, 15, 30, 0, time.UTC),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := NextCheckAt(test.schedule, test.now)
			if got == nil {
				t.Fatal("NextCheckAt() = nil")
			}
			if !got.Equal(test.want) {
				t.Fatalf("NextCheckAt() = %s, want %s", got.Format(time.RFC3339), test.want.Format(time.RFC3339))
			}
			if got.Location() != time.UTC {
				t.Fatalf("NextCheckAt() location = %s, want UTC", got.Location())
			}
		})
	}
}

func TestNextCheckAtAcceptsNodeCompatibleTimezoneCase(t *testing.T) {
	tests := []struct {
		name     string
		timezone string
		now      time.Time
		want     time.Time
	}{
		{
			name:     "lowercase America New York",
			timezone: "america/new_york",
			now:      time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC),
			want:     time.Date(2026, 7, 13, 13, 0, 0, 0, time.UTC),
		},
		{
			name:     "uppercase Asia Shanghai",
			timezone: "ASIA/SHANGHAI",
			now:      time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC),
			want:     time.Date(2026, 7, 13, 1, 0, 0, 0, time.UTC),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := NextCheckAt(normalizedTestSchedule(test.timezone, []int{1}, "09:00", "18:00"), test.now)
			if got == nil {
				t.Fatal("NextCheckAt() = nil")
			}
			if !got.Equal(test.want) {
				t.Fatalf("NextCheckAt() = %s, want %s", got.Format(time.RFC3339), test.want.Format(time.RFC3339))
			}
		})
	}
}

func TestNextCheckAtReturnsNilWithoutSchedule(t *testing.T) {
	if got := NextCheckAt(nil, time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC)); got != nil {
		t.Fatalf("NextCheckAt(nil) = %s, want nil", got.Format(time.RFC3339))
	}
}

func normalizedTestSchedule(timezone string, days []int, start string, end string) map[string]any {
	return map[string]any{
		"enabled":  true,
		"timezone": timezone,
		"mode":     "allow_windows",
		"windows": []map[string]any{{
			"daysOfWeek": days,
			"start":      start,
			"end":        end,
		}},
	}
}

func withTestScheduleDateRange(schedule map[string]any, startDate string, endDate string) map[string]any {
	schedule["dateRange"] = map[string]any{
		"startDate": startDate,
		"endDate":   endDate,
	}
	return schedule
}

func withTestScheduleExceptions(schedule map[string]any, exceptions []map[string]any) map[string]any {
	schedule["exceptions"] = exceptions
	return schedule
}
