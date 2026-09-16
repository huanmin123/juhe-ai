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
	if _, ok := names["system-metrics-sample"]; !ok {
		t.Fatalf("system-metrics-sample missing: %v", names)
	}
	if names["system-metrics-sample"] != "systemMetricsSampleIntervalSeconds" {
		t.Fatalf("settings key=%q", names["system-metrics-sample"])
	}
	if names["usage-stats-aggregation"] != names["client-ip-stats-aggregation"] {
		t.Fatal("both stats jobs share the aggregation interval key")
	}

	if constraint, ok := ModeConstraintFor("system-metrics-sample"); ok {
		t.Fatalf("system-metrics-sample must not carry a constraint: %+v", constraint)
	}
	found := false
	for name := range modeConstraints() {
		constraint, ok := ModeConstraintFor(name)
		if !ok {
			t.Fatalf("%s listed in modeConstraints but lookup missed", name)
		}
		found = true
		_ = constraint
	}
	if !found {
		t.Fatal("modeConstraints must not be empty")
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
	schedule, ok := ResolveSchedule("system-metrics-sample", w9hSettingsInterval(123*time.Second))
	if !ok || schedule.Interval != 123*time.Second {
		t.Fatalf("override schedule=%+v ok=%v", schedule, ok)
	}
	schedule, ok = ResolveSchedule("system-metrics-sample", w9hNeverMatches())
	if !ok || schedule.Interval <= 0 {
		t.Fatalf("default schedule=%+v ok=%v", schedule, ok)
	}
	schedule, ok = ResolveSchedule("system-metrics-sample", w9hSettingsInterval(-5*time.Second))
	if !ok || schedule.Interval <= 0 {
		t.Fatalf("ignored override schedule=%+v ok=%v", schedule, ok)
	}
	if _, ok := ResolveSchedule("system-metrics-sample", nil); !ok {
		t.Fatal("nil settings must still resolve")
	}
}

func TestW9HResolveScheduleForDriverArms(t *testing.T) {
	constrained := ""
	for name := range modeConstraints() {
		constrained = name
		break
	}
	if constrained == "" {
		t.Fatal("modeConstraints must not be empty")
	}
	constraint, _ := ModeConstraintFor(constrained)

	if constraint.PostgresOnly {
		if _, ok := ResolveScheduleForDriver(constrained, nil, "sqlite"); ok {
			t.Fatalf("%s postgres-only 必须在 sqlite 下不注册", constrained)
		}
		if _, ok := ResolveScheduleForDriver(constrained, nil, "postgres"); !ok {
			t.Fatalf("%s postgres-only 必须在 postgres 下注册", constrained)
		}
	}
	if constraint.SQLiteOnly {
		if _, ok := ResolveScheduleForDriver(constrained, nil, "postgres"); ok {
			t.Fatalf("%s sqlite-only 必须在 postgres 下不注册", constrained)
		}
		if _, ok := ResolveScheduleForDriver(constrained, nil, "sqlite"); !ok {
			t.Fatalf("%s sqlite-only 必须在 sqlite 下注册", constrained)
		}
	}
	if !constraint.PostgresOnly && !constraint.SQLiteOnly && constraint.SQLiteInterval > 0 {
		schedule, ok := ResolveScheduleForDriver(constrained, nil, "sqlite")
		if !ok || schedule.Interval != constraint.SQLiteInterval {
			t.Fatalf("sqlite interval schedule=%+v want %d", schedule, constraint.SQLiteInterval)
		}
		schedule, ok = ResolveScheduleForDriver(constrained, w9hSettingsInterval(777*time.Second), "sqlite")
		if !ok || schedule.Interval != 777*time.Second {
			t.Fatalf("settings override schedule=%+v", schedule)
		}
		pgSchedule, ok := ResolveScheduleForDriver(constrained, nil, "postgres")
		if !ok || pgSchedule.Interval == constraint.SQLiteInterval {
			t.Fatalf("postgres branch keeps base interval: %+v", pgSchedule)
		}
	}
	if _, ok := ResolveScheduleForDriver("w9h-unknown", nil, "sqlite"); ok {
		t.Fatal("unknown job must not resolve")
	}
}
