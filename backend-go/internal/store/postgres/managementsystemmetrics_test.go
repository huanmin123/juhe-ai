package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

func TestReadManagementSystemMetricsUsesBoundedWindowAndRoleQueries(t *testing.T) {
	source := &managementSystemMetricsSourceStub{
		hourly: []port.ManagementSystemMetricsHourlyAggregate{{StatHour: "2026-07-01", SampleCount: 1}},
		latest: []port.ManagementProcessMetricSample{{ProcessRole: "server"}},
		peak:   []port.ManagementProcessMetricSample{{ProcessRole: "stats-worker"}},
		trend:  []port.ManagementProcessMetricTrendAggregate{{StatHour: "2026-07-01", ProcessRole: "server"}},
	}
	input := port.ManagementSystemMetricsReadInput{
		WindowKey:     "2026-07-01:2026-07-22",
		StartDate:     "2026-07-01",
		EndDate:       "2026-07-22",
		Days:          22,
		BucketHours:   24,
		PeakStartedAt: "2026-07-21T04:30:00.000Z",
		ProcessRoles:  []string{"server", "ingest-worker", "stats-worker", "ops-worker", "db-service"},
	}

	got, err := readManagementSystemMetrics(context.Background(), source, input)

	if err != nil {
		t.Fatalf("readManagementSystemMetrics() error = %v", err)
	}
	if len(got.HourlyTrend) != 1 || len(got.ProcessLatest) != 1 || len(got.ProcessPeak) != 1 || len(got.ProcessTrend) != 1 {
		t.Fatalf("snapshot = %+v", got)
	}
	if source.hourlyLimit != 22 || source.trendLimit != 110 || source.latestLimit != 5 || source.peakLimit != 5 {
		t.Fatalf("limits hourly=%d trend=%d latest=%d peak=%d", source.hourlyLimit, source.trendLimit, source.latestLimit, source.peakLimit)
	}
	if source.hourlyInput.WindowKey != input.WindowKey || source.trendInput.StartDate != input.StartDate ||
		source.peakStartedAt != input.PeakStartedAt || len(source.latestRoles) != 5 {
		t.Fatalf("captured source inputs = %+v %+v %q %#v", source.hourlyInput, source.trendInput, source.peakStartedAt, source.latestRoles)
	}
}

func TestReadManagementSystemMetricsReturnsAnySourceError(t *testing.T) {
	want := errors.New("postgres unavailable")
	source := &managementSystemMetricsSourceStub{trendErr: want}
	_, err := readManagementSystemMetrics(context.Background(), source, port.ManagementSystemMetricsReadInput{
		WindowKey:    "2026-07-22:2026-07-22",
		StartDate:    "2026-07-22",
		EndDate:      "2026-07-22",
		Days:         1,
		BucketHours:  1,
		ProcessRoles: []string{"server"},
	})
	if !errors.Is(err, want) {
		t.Fatalf("readManagementSystemMetrics() error = %v, want %v", err, want)
	}
}

func TestManagementSystemMetricsSQLIsReadOnlyParameterizedAndBounded(t *testing.T) {
	queries := map[string]string{
		"system trend":  managementSystemMetricsHourlySQL,
		"process trend": managementSystemMetricsProcessTrendSQL,
		"latest":        managementSystemMetricsLatestSQL,
		"peak":          managementSystemMetricsPeakSQL,
	}
	for name, query := range queries {
		t.Run(name, func(t *testing.T) {
			upper := strings.ToUpper(query)
			for _, forbidden := range []string{"INSERT ", "UPDATE ", "DELETE ", " SUM(", "GROUP BY", "DB-SERVICE IPC"} {
				if strings.Contains(upper, forbidden) {
					t.Fatalf("query contains forbidden %q:\n%s", forbidden, query)
				}
			}
			if !strings.Contains(upper, "LIMIT $") {
				t.Fatalf("query is not parameter bounded:\n%s", query)
			}
		})
	}
	for _, required := range []string{
		"FROM juhe_stats.system_metrics_trend_windows",
		"window_key = $1",
		"start_date = $2",
		"end_date = $3",
		"ORDER BY bucket_key ASC",
		"LIMIT $4",
	} {
		if !strings.Contains(managementSystemMetricsHourlySQL, required) {
			t.Fatalf("system trend query missing %q:\n%s", required, managementSystemMetricsHourlySQL)
		}
	}
	for _, required := range []string{
		"FROM juhe_stats.process_event_loop_trend_windows",
		"process_role = ANY($4::text[])",
		"ORDER BY bucket_key ASC, process_role ASC",
		"LIMIT $5",
	} {
		if !strings.Contains(managementSystemMetricsProcessTrendSQL, required) {
			t.Fatalf("process trend query missing %q:\n%s", required, managementSystemMetricsProcessTrendSQL)
		}
	}
	for _, required := range []string{
		"DISTINCT ON (process_role)",
		"FROM juhe_stats.process_event_loop_samples",
		"process_role = ANY($1::text[])",
		"ORDER BY process_role, sampled_at DESC, id DESC",
		"LIMIT $2",
	} {
		if !strings.Contains(managementSystemMetricsLatestSQL, required) {
			t.Fatalf("latest query missing %q:\n%s", required, managementSystemMetricsLatestSQL)
		}
	}
	for _, required := range []string{
		"sampled_at >= $2",
		"event_loop_lag_ms IS NOT NULL",
		"ORDER BY process_role, event_loop_lag_ms DESC, sampled_at DESC, id DESC",
		"LIMIT $3",
	} {
		if !strings.Contains(managementSystemMetricsPeakSQL, required) {
			t.Fatalf("peak query missing %q:\n%s", required, managementSystemMetricsPeakSQL)
		}
	}
	for _, forbidden := range []string{"system_metrics_hourly", "process_event_loop_hourly"} {
		for name, query := range queries {
			if strings.Contains(query, forbidden) {
				t.Fatalf("%s must follow latest Node read path and not scan %s", name, forbidden)
			}
		}
	}
}

type managementSystemMetricsSourceStub struct {
	hourly []port.ManagementSystemMetricsHourlyAggregate
	latest []port.ManagementProcessMetricSample
	peak   []port.ManagementProcessMetricSample
	trend  []port.ManagementProcessMetricTrendAggregate

	hourlyErr error
	latestErr error
	peakErr   error
	trendErr  error

	hourlyInput   port.ManagementSystemMetricsReadInput
	trendInput    port.ManagementSystemMetricsReadInput
	hourlyLimit   int
	trendLimit    int
	latestRoles   []string
	latestLimit   int
	peakRoles     []string
	peakStartedAt string
	peakLimit     int
}

func (s *managementSystemMetricsSourceStub) listHourly(_ context.Context, input port.ManagementSystemMetricsReadInput, limit int) ([]port.ManagementSystemMetricsHourlyAggregate, error) {
	s.hourlyInput, s.hourlyLimit = input, limit
	return s.hourly, s.hourlyErr
}

func (s *managementSystemMetricsSourceStub) listProcessTrend(_ context.Context, input port.ManagementSystemMetricsReadInput, limit int) ([]port.ManagementProcessMetricTrendAggregate, error) {
	s.trendInput, s.trendLimit = input, limit
	return s.trend, s.trendErr
}

func (s *managementSystemMetricsSourceStub) listLatest(_ context.Context, roles []string, limit int) ([]port.ManagementProcessMetricSample, error) {
	s.latestRoles, s.latestLimit = append([]string(nil), roles...), limit
	return s.latest, s.latestErr
}

func (s *managementSystemMetricsSourceStub) listPeak(_ context.Context, roles []string, startedAt string, limit int) ([]port.ManagementProcessMetricSample, error) {
	s.peakRoles, s.peakStartedAt, s.peakLimit = append([]string(nil), roles...), startedAt, limit
	return s.peak, s.peakErr
}
