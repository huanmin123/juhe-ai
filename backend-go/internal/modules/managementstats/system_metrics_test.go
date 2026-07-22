package managementstats

import (
	"context"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceSystemMetricsNormalizesWindowGranularityAndMapsNodeDTO(t *testing.T) {
	float := func(value float64) *float64 { return &value }
	integer := func(value int64) *int64 { return &value }
	store := &systemMetricsStoreStub{
		timezone: "Asia/Shanghai",
		found:    true,
		snapshot: port.ManagementSystemMetricsSnapshot{
			HourlyTrend: []port.ManagementSystemMetricsHourlyAggregate{{
				StatHour:                     "2026-07-01",
				SampleCount:                  2,
				CPUPercentSum:                21,
				CPUPercentMax:                float(15.5),
				MemoryUsedPercentSum:         101,
				MemoryUsedPercentMax:         float(55.5),
				EventLoopLagMSSum:            3,
				EventLoopLagMSSampleCount:    2,
				EventLoopLagMSMax:            float(2.5),
				NetworkRXBytesPerSecondSum:   5,
				NetworkRXBytesPerSecondCount: 2,
				NetworkRXBytesPerSecondMax:   float(4),
				NetworkTXBytesPerSecondSum:   9,
				NetworkTXBytesPerSecondCount: 2,
				NetworkTXBytesPerSecondMax:   float(6),
				NetworkRXTotalBytesMax:       integer(100),
				NetworkTXTotalBytesMax:       integer(200),
				ProcessRSSBytesMax:           integer(300),
				ProcessHeapUsedBytesMax:      integer(400),
				DBFileBytesMax:               integer(500),
				StatsLagSecondsMax:           integer(6),
			}},
			ProcessLatest: []port.ManagementProcessMetricSample{{
				ProcessRole:     "server",
				ProcessPID:      integer(101),
				SampledAt:       "2026-07-22T04:20:00.000Z",
				EventLoopLagMS:  float(7.5),
				ProcessRSSBytes: integer(900),
			}},
			ProcessPeak: []port.ManagementProcessMetricSample{{
				ProcessRole:    "stats-worker",
				ProcessPID:     integer(202),
				SampledAt:      "2026-07-21T18:00:00.000Z",
				EventLoopLagMS: float(25),
			}},
			ProcessTrend: []port.ManagementProcessMetricTrendAggregate{{
				StatHour:                  "2026-07-01",
				ProcessRole:               "server",
				SampleCount:               2,
				EventLoopLagMSSum:         3,
				EventLoopLagMSSampleCount: 2,
				EventLoopLagMSMax:         float(2.5),
				ProcessRSSBytesSum:        5,
				ProcessRSSBytesMax:        integer(4),
				ProcessHeapUsedBytesSum:   9,
				ProcessHeapUsedBytesMax:   integer(6),
				ProcessHeapTotalBytesSum:  13,
				ProcessHeapTotalBytesMax:  integer(8),
			}},
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store:              store,
		SystemMetricsStore: store,
		Now: func() time.Time {
			return time.Date(2026, 7, 22, 4, 30, 0, 0, time.UTC)
		},
	})

	got, err := service.SystemMetrics(context.Background(), SystemMetricsQuery{
		StartDate: "2026-07-01",
		EndDate:   "2026-07-22",
	})

	if err != nil {
		t.Fatalf("SystemMetrics() error = %v", err)
	}
	if store.input.StartDate != "2026-07-01" || store.input.EndDate != "2026-07-22" ||
		store.input.WindowKey != "2026-07-01:2026-07-22" || store.input.Days != 22 ||
		store.input.BucketHours != 24 || store.input.PeakStartedAt != "2026-07-21T04:30:00.000Z" {
		t.Fatalf("store input = %+v", store.input)
	}
	if len(store.input.ProcessRoles) != 5 || store.input.ProcessRoles[4] != "db-service" {
		t.Fatalf("process roles = %#v", store.input.ProcessRoles)
	}
	if len(got.HourlyTrend) != 1 {
		t.Fatalf("hourly trend = %+v", got.HourlyTrend)
	}
	hourly := got.HourlyTrend[0]
	if hourly.CPUPercentAvg == nil || *hourly.CPUPercentAvg != 11 ||
		hourly.MemoryUsedPercentAvg == nil || *hourly.MemoryUsedPercentAvg != 51 ||
		hourly.EventLoopLagMSAvg == nil || *hourly.EventLoopLagMSAvg != 2 ||
		hourly.NetworkRXBytesPerSecondAvg == nil || *hourly.NetworkRXBytesPerSecondAvg != 3 ||
		hourly.NetworkTXBytesPerSecondAvg == nil || *hourly.NetworkTXBytesPerSecondAvg != 5 {
		t.Fatalf("hourly mapped averages = %+v", hourly)
	}
	if len(got.ProcessEventLoopLatestStatus) != 5 || len(got.ProcessEventLoopPeakStatus) != 5 {
		t.Fatalf("status lengths latest=%d peak=%d", len(got.ProcessEventLoopLatestStatus), len(got.ProcessEventLoopPeakStatus))
	}
	if latest := got.ProcessEventLoopLatestStatus[0]; latest.ProcessRole != "server" || !latest.SampleAvailable || latest.ProcessPID == nil || *latest.ProcessPID != 101 {
		t.Fatalf("server latest = %+v", latest)
	}
	if missing := got.ProcessEventLoopLatestStatus[4]; missing.ProcessRole != "db-service" || missing.SampleAvailable || missing.SampledAt != nil {
		t.Fatalf("db-service missing latest = %+v", missing)
	}
	if peak := got.ProcessEventLoopPeakStatus[2]; peak.ProcessRole != "stats-worker" || !peak.SampleAvailable || peak.EventLoopLagMS == nil || *peak.EventLoopLagMS != 25 {
		t.Fatalf("stats-worker peak = %+v", peak)
	}
	if len(got.ProcessEventLoopTrend) != 1 || got.ProcessEventLoopTrend[0].StatMinute != "2026-07-01" ||
		got.ProcessEventLoopTrend[0].ProcessRSSBytesAvg == nil || *got.ProcessEventLoopTrend[0].ProcessRSSBytesAvg != 3 {
		t.Fatalf("process trend = %+v", got.ProcessEventLoopTrend)
	}
}

func TestServiceSystemMetricsMirrorsSingleBoundaryAndSelectsGranularity(t *testing.T) {
	tests := []struct {
		name        string
		query       SystemMetricsQuery
		startDate   string
		endDate     string
		days        int
		bucketHours int
	}{
		{name: "default today", startDate: "2026-07-22", endDate: "2026-07-22", days: 1, bucketHours: 1},
		{name: "start only", query: SystemMetricsQuery{StartDate: "2026-07-20"}, startDate: "2026-07-20", endDate: "2026-07-20", days: 1, bucketHours: 1},
		{name: "end only", query: SystemMetricsQuery{EndDate: "2026-07-20"}, startDate: "2026-07-20", endDate: "2026-07-20", days: 1, bucketHours: 1},
		{name: "three days", query: SystemMetricsQuery{StartDate: "2026-07-20", EndDate: "2026-07-22"}, startDate: "2026-07-20", endDate: "2026-07-22", days: 3, bucketHours: 6},
		{name: "clamp to 31 days", query: SystemMetricsQuery{StartDate: "2026-01-01", EndDate: "2026-07-22"}, startDate: "2026-06-22", endDate: "2026-07-22", days: 31, bucketHours: 24},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &systemMetricsStoreStub{timezone: "UTC", found: true}
			service := NewServiceWithOptions(ServiceOptions{
				Store:              store,
				SystemMetricsStore: store,
				Now:                func() time.Time { return time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC) },
			})
			if _, err := service.SystemMetrics(context.Background(), test.query); err != nil {
				t.Fatalf("SystemMetrics() error = %v", err)
			}
			if store.input.StartDate != test.startDate || store.input.EndDate != test.endDate ||
				store.input.Days != test.days || store.input.BucketHours != test.bucketHours {
				t.Fatalf("input = %+v", store.input)
			}
		})
	}
}

type systemMetricsStoreStub struct {
	timezone string
	found    bool
	input    port.ManagementSystemMetricsReadInput
	snapshot port.ManagementSystemMetricsSnapshot
	err      error
}

func (s *systemMetricsStoreStub) GetManagementUsageStatsTimezone(context.Context) (string, bool, error) {
	return s.timezone, s.found, s.err
}

func (s *systemMetricsStoreStub) ReadManagementSystemMetrics(_ context.Context, input port.ManagementSystemMetricsReadInput) (port.ManagementSystemMetricsSnapshot, error) {
	s.input = input
	return s.snapshot, s.err
}

var _ port.ManagementUsageStatsTimezoneReader = (*systemMetricsStoreStub)(nil)
var _ port.ManagementSystemMetricsReader = (*systemMetricsStoreStub)(nil)
