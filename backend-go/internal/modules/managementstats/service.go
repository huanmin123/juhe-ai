package managementstats

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	usageWindowDays       = 31
	usageStatsTimezoneTTL = time.Minute
)

type Service struct {
	store           port.ManagementUsageStatsTimezoneReader
	now             func() time.Time
	timezoneMu      sync.Mutex
	timezoneCache   cachedUsageStatsTimezone
	timezoneRefresh *usageStatsTimezoneRefresh
}

type ServiceOptions struct {
	Store port.ManagementUsageStatsTimezoneReader
	Now   func() time.Time
}

type UsageWindow struct {
	Timezone  string `json:"timezone"`
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
	Days      int    `json:"days"`
	MaxDays   int    `json:"maxDays"`
}

type cachedUsageStatsTimezone struct {
	name      string
	location  *time.Location
	expiresAt time.Time
}

type usageStatsTimezoneRefresh struct {
	done chan struct{}
}

func NewService(store port.ManagementUsageStatsTimezoneReader) *Service {
	return NewServiceWithOptions(ServiceOptions{Store: store})
}

func NewServiceWithOptions(opts ServiceOptions) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		store: opts.Store,
		now:   now,
	}
}

func (s *Service) UsageWindow(ctx context.Context) (UsageWindow, error) {
	if s.store == nil {
		return UsageWindow{}, fmt.Errorf("management usage stats timezone store is required")
	}
	cacheNow := s.now()
	timezone, location, err := s.usageStatsTimezone(ctx, cacheNow)
	if err != nil {
		return UsageWindow{}, err
	}
	now := s.now()
	year, month, day := now.In(location).Date()
	endDate := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	startDate := endDate.AddDate(0, 0, -(usageWindowDays - 1))
	return UsageWindow{
		Timezone:  timezone,
		StartDate: startDate.Format(time.DateOnly),
		EndDate:   endDate.Format(time.DateOnly),
		Days:      usageWindowDays,
		MaxDays:   usageWindowDays,
	}, nil
}

func (s *Service) usageStatsTimezone(ctx context.Context, now time.Time) (string, *time.Location, error) {
	for {
		s.timezoneMu.Lock()
		if s.timezoneCache.location != nil && now.Before(s.timezoneCache.expiresAt) {
			timezone := s.timezoneCache.name
			location := s.timezoneCache.location
			s.timezoneMu.Unlock()
			return timezone, location, nil
		}
		if refresh := s.timezoneRefresh; refresh != nil {
			done := refresh.done
			s.timezoneMu.Unlock()
			select {
			case <-ctx.Done():
				return "", nil, ctx.Err()
			case <-done:
				continue
			}
		}
		refresh := &usageStatsTimezoneRefresh{done: make(chan struct{})}
		s.timezoneRefresh = refresh
		s.timezoneMu.Unlock()

		timezone, location, err := s.readUsageStatsTimezone(ctx, now)

		s.timezoneMu.Lock()
		if s.timezoneRefresh == refresh {
			s.timezoneRefresh = nil
		}
		close(refresh.done)
		s.timezoneMu.Unlock()
		return timezone, location, err
	}
}

func (s *Service) readUsageStatsTimezone(ctx context.Context, now time.Time) (string, *time.Location, error) {
	timezone, found, err := s.store.GetManagementUsageStatsTimezone(ctx)
	if err != nil {
		return "", nil, err
	}
	timezone = strings.TrimSpace(timezone)
	if !found || timezone == "" {
		return "", nil, fmt.Errorf("系统设置缺少 usageStatsTimezone")
	}
	location, err := loadUsageStatsLocation(timezone)
	if err != nil {
		return "", nil, fmt.Errorf("系统设置 usageStatsTimezone 无效: %w", err)
	}
	s.timezoneMu.Lock()
	s.timezoneCache = cachedUsageStatsTimezone{
		name:      timezone,
		location:  location,
		expiresAt: now.Add(usageStatsTimezoneTTL),
	}
	s.timezoneMu.Unlock()
	return timezone, location, nil
}
