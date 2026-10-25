package jobregistry

import (
	"testing"
	"time"
)

func TestW9HWiredJobNamesAndModeConstraints(t *testing.T) {
	wired := WiredJobNames()
	if len(wired) == 0 {
		t.Fatal("wired jobs must not be empty")
	}
	for _, entry := range ScheduledEntries() {
		if wired[entry.JobName] && entry.GoStatus != GoWired {
			t.Fatalf("%s listed wired but status=%q", entry.JobName, entry.GoStatus)
		}
	}

	names := SettingsIntervalJobNames()
	if names["usage-stats-aggregation"] != "statsAggregationIntervalSeconds" {
		t.Fatalf("settings key=%q", names["usage-stats-aggregation"])
	}
	if names["usage-stats-aggregation"] != names["client-ip-stats-aggregation"] {
		t.Fatal("both stats jobs share the aggregation interval key")
	}

}

func w9hSettingsInterval(duration time.Duration) SettingsInterval {
	return func(string) (time.Duration, bool) { return duration, true }
}

func w9hNeverMatches() SettingsInterval {
	return func(string) (time.Duration, bool) { return 0, false }
}

func TestW9HResolveScheduleArms(t *testing.T) {
	if _, ok := ResolveSchedule("w9h-unknown", nil); ok {
		t.Fatal("unknown job must not resolve")
	}
	schedule, ok := ResolveSchedule("usage-stats-aggregation", w9hSettingsInterval(123*time.Second))
	if !ok || schedule.Interval != 123*time.Second {
		t.Fatalf("override schedule=%+v ok=%v", schedule, ok)
	}
	schedule, ok = ResolveSchedule("usage-stats-aggregation", w9hNeverMatches())
	if !ok || schedule.Interval <= 0 {
		t.Fatalf("default schedule=%+v ok=%v", schedule, ok)
	}
	schedule, ok = ResolveSchedule("usage-stats-aggregation", w9hSettingsInterval(-5*time.Second))
	if !ok || schedule.Interval <= 0 {
		t.Fatalf("ignored override schedule=%+v ok=%v", schedule, ok)
	}
	if _, ok := ResolveSchedule("usage-stats-aggregation", nil); !ok {
		t.Fatal("nil settings must still resolve")
	}
}
