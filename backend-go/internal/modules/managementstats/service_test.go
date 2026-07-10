package managementstats

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceUsageWindowUsesConfiguredTimezoneAcrossUTCDateBoundary(t *testing.T) {
	store := &usageStatsTimezoneStoreStub{
		timezone: "Asia/Shanghai",
		found:    true,
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store: store,
		Now: func() time.Time {
			return time.Date(2026, 7, 8, 16, 30, 0, 0, time.UTC)
		},
	})

	got, err := service.UsageWindow(context.Background())

	if err != nil {
		t.Fatalf("UsageWindow() error = %v", err)
	}
	if !store.called {
		t.Fatal("usageStatsTimezone store was not called")
	}
	if got.Timezone != "Asia/Shanghai" ||
		got.StartDate != "2026-06-09" ||
		got.EndDate != "2026-07-09" ||
		got.Days != 31 ||
		got.MaxDays != 31 {
		t.Fatalf("UsageWindow() = %+v", got)
	}
}

func TestServiceUsageWindowUsesCurrentTimeAfterTimezoneRead(t *testing.T) {
	now := time.Date(2026, 7, 8, 15, 59, 59, 0, time.UTC)
	store := &usageStatsTimezoneStoreStub{
		timezone: "Asia/Shanghai",
		found:    true,
		onRead: func() {
			now = time.Date(2026, 7, 8, 16, 0, 1, 0, time.UTC)
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})

	got, err := service.UsageWindow(context.Background())

	if err != nil {
		t.Fatalf("UsageWindow() error = %v", err)
	}
	if got.EndDate != "2026-07-09" {
		t.Fatalf("UsageWindow() = %+v, want date after timezone read", got)
	}
}

func TestServiceUsageWindowAcceptsCaseInsensitiveIANATimezone(t *testing.T) {
	tests := []struct {
		timezone  string
		startDate string
		endDate   string
	}{
		{timezone: "asia/shanghai", startDate: "2026-06-09", endDate: "2026-07-09"},
		{timezone: "us/pacific", startDate: "2026-06-08", endDate: "2026-07-08"},
		{timezone: "antarctica/mcmurdo", startDate: "2026-06-09", endDate: "2026-07-09"},
	}

	for _, test := range tests {
		t.Run(test.timezone, func(t *testing.T) {
			service := NewServiceWithOptions(ServiceOptions{
				Store: &usageStatsTimezoneStoreStub{
					timezone: test.timezone,
					found:    true,
				},
				Now: func() time.Time {
					return time.Date(2026, 7, 8, 16, 30, 0, 0, time.UTC)
				},
			})

			got, err := service.UsageWindow(context.Background())

			if err != nil {
				t.Fatalf("UsageWindow() error = %v", err)
			}
			if got.Timezone != test.timezone ||
				got.StartDate != test.startDate ||
				got.EndDate != test.endDate {
				t.Fatalf("UsageWindow() = %+v", got)
			}
		})
	}
}

func TestServiceUsageWindowUsesCalendarDaysAcrossDSTBoundary(t *testing.T) {
	service := NewServiceWithOptions(ServiceOptions{
		Store: &usageStatsTimezoneStoreStub{
			timezone: "America/New_York",
			found:    true,
		},
		Now: func() time.Time {
			return time.Date(2026, 3, 8, 7, 30, 0, 0, time.UTC)
		},
	})

	got, err := service.UsageWindow(context.Background())

	if err != nil {
		t.Fatalf("UsageWindow() error = %v", err)
	}
	if got.StartDate != "2026-02-06" ||
		got.EndDate != "2026-03-08" ||
		got.Days != 31 {
		t.Fatalf("UsageWindow() = %+v", got)
	}
}

func TestServiceUsageWindowCachesValidatedTimezoneForSixtySeconds(t *testing.T) {
	now := time.Date(2026, 7, 8, 16, 30, 0, 0, time.UTC)
	store := &usageStatsTimezoneStoreStub{
		timezone: "UTC",
		found:    true,
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})

	first, err := service.UsageWindow(context.Background())
	if err != nil {
		t.Fatalf("first UsageWindow() error = %v", err)
	}
	store.timezone = "Asia/Shanghai"
	now = now.Add(59 * time.Second)
	second, err := service.UsageWindow(context.Background())
	if err != nil {
		t.Fatalf("second UsageWindow() error = %v", err)
	}
	if store.calls != 1 || second.Timezone != first.Timezone {
		t.Fatalf("cached window = %+v, store calls = %d", second, store.calls)
	}

	now = now.Add(2 * time.Second)
	third, err := service.UsageWindow(context.Background())
	if err != nil {
		t.Fatalf("third UsageWindow() error = %v", err)
	}
	if store.calls != 2 || third.Timezone != "Asia/Shanghai" {
		t.Fatalf("refreshed window = %+v, store calls = %d", third, store.calls)
	}
}

func TestServiceUsageWindowCoalescesRefreshAndCanceledWaiterDoesNotBlock(t *testing.T) {
	store := &blockingUsageStatsTimezoneStore{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(store.release) })
	service := NewServiceWithOptions(ServiceOptions{
		Store: store,
		Now: func() time.Time {
			return time.Date(2026, 7, 8, 16, 30, 0, 0, time.UTC)
		},
	})

	firstDone := make(chan error, 1)
	go func() {
		_, err := service.UsageWindow(context.Background())
		firstDone <- err
	}()
	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("timezone refresh did not start")
	}

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() {
		_, err := service.UsageWindow(waiterCtx)
		secondDone <- err
	}()
	cancelWaiter()
	select {
	case err := <-secondDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting UsageWindow() error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiting UsageWindow() did not honor context cancellation")
	}
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("timezone store calls = %d, want one coalesced refresh", got)
	}

	releaseOnce.Do(func() { close(store.release) })
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("refreshing UsageWindow() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("refreshing UsageWindow() did not finish")
	}
}

func TestServiceUsageWindowRejectsMissingOrEmptyTimezoneSetting(t *testing.T) {
	tests := []struct {
		name     string
		timezone string
		found    bool
	}{
		{name: "missing", found: false},
		{name: "empty", timezone: "", found: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := NewServiceWithOptions(ServiceOptions{
				Store: &usageStatsTimezoneStoreStub{
					timezone: test.timezone,
					found:    test.found,
				},
				Now: func() time.Time {
					return time.Date(2026, 7, 8, 16, 30, 0, 0, time.UTC)
				},
			})

			got, err := service.UsageWindow(context.Background())

			if err == nil || !strings.Contains(err.Error(), "usageStatsTimezone") {
				t.Fatalf("UsageWindow() error = %v, want missing usageStatsTimezone error", err)
			}
			if got.Timezone == "UTC" {
				t.Fatalf("UsageWindow() = %+v, missing setting must not fall back to UTC", got)
			}
		})
	}
}

func TestServiceUsageWindowRejectsInvalidTimezone(t *testing.T) {
	service := NewServiceWithOptions(ServiceOptions{
		Store: &usageStatsTimezoneStoreStub{
			timezone: "Invalid/Timezone",
			found:    true,
		},
		Now: func() time.Time {
			return time.Date(2026, 7, 8, 16, 30, 0, 0, time.UTC)
		},
	})

	_, err := service.UsageWindow(context.Background())

	if err == nil || !strings.Contains(err.Error(), "usageStatsTimezone") {
		t.Fatalf("UsageWindow() error = %v, want invalid usageStatsTimezone error", err)
	}
}

func TestServiceUsageWindowReturnsStoreError(t *testing.T) {
	want := errors.New("postgres down")
	service := NewServiceWithOptions(ServiceOptions{
		Store: &usageStatsTimezoneStoreStub{err: want},
	})

	_, err := service.UsageWindow(context.Background())

	if !errors.Is(err, want) {
		t.Fatalf("UsageWindow() error = %v, want %v", err, want)
	}
}

type usageStatsTimezoneStoreStub struct {
	called   bool
	calls    int
	timezone string
	found    bool
	err      error
	onRead   func()
}

func (s *usageStatsTimezoneStoreStub) GetManagementUsageStatsTimezone(context.Context) (string, bool, error) {
	s.called = true
	s.calls++
	if s.onRead != nil {
		s.onRead()
	}
	if s.err != nil {
		return "", false, s.err
	}
	return s.timezone, s.found, nil
}

var _ port.ManagementUsageStatsTimezoneReader = (*usageStatsTimezoneStoreStub)(nil)

type blockingUsageStatsTimezoneStore struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	calls   atomic.Int32
}

func (s *blockingUsageStatsTimezoneStore) GetManagementUsageStatsTimezone(ctx context.Context) (string, bool, error) {
	s.calls.Add(1)
	s.once.Do(func() { close(s.entered) })
	select {
	case <-ctx.Done():
		return "", false, ctx.Err()
	case <-s.release:
		return "UTC", true, nil
	}
}

var _ port.ManagementUsageStatsTimezoneReader = (*blockingUsageStatsTimezoneStore)(nil)
