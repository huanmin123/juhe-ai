package gometrics

import (
	"testing"
	"time"
)

func TestWindowAggregatorGroupsAndRetainsHourlySamples(t *testing.T) {
	now := time.Date(2026, 9, 2, 10, 30, 0, 0, time.UTC)
	a := NewWindowAggregator(2 * time.Hour)
	a.Add(RuntimeSnapshot{SampledAt: now, Service: "juhe-ai", Role: "jobs", Goroutines: 4, HeapAllocBytes: 100, Threads: 2})
	a.Add(RuntimeSnapshot{SampledAt: now.Add(-10 * time.Minute), Service: "juhe-ai", Role: "jobs", Goroutines: 8, HeapAllocBytes: 300, Threads: 4})
	a.Add(RuntimeSnapshot{SampledAt: now.Add(-3 * time.Hour), Service: "juhe-ai", Role: "jobs", Goroutines: 99})
	windows := a.Windows()
	if len(windows) != 1 {
		t.Fatalf("expected one retained window, got %d: %#v", len(windows), windows)
	}
	if windows[0].Service != "juhe-ai" || windows[0].Role != "jobs" || windows[0].RuntimeKind != "go" || windows[0].SampleCount != 2 || windows[0].GoroutinesAvg != 6 || windows[0].GoroutinesMax != 8 || windows[0].HeapAllocBytesAvg != 200 {
		t.Fatalf("unexpected aggregate: %#v", windows[0])
	}
}

func TestWindowAggregatorIgnoresMissingTimestamp(t *testing.T) {
	a := NewWindowAggregator(time.Hour)
	a.Add(RuntimeSnapshot{Goroutines: 10})
	if got := len(a.Windows()); got != 0 {
		t.Fatalf("expected missing timestamp to be unavailable, got %d windows", got)
	}
}

func TestWindowAggregatorSeparatesRoles(t *testing.T) {
	now := time.Date(2026, 9, 2, 10, 30, 0, 0, time.UTC)
	a := NewWindowAggregator(time.Hour)
	a.Add(RuntimeSnapshot{SampledAt: now, Service: "juhe-ai", Role: "jobs", Goroutines: 2})
	a.Add(RuntimeSnapshot{SampledAt: now, Service: "juhe-ai", Role: "gateway", Goroutines: 7})
	if got := len(a.Windows()); got != 2 {
		t.Fatalf("expected separate role windows, got %d", got)
	}
}
