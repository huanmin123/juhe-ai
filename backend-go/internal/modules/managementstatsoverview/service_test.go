package managementstatsoverview

import (
	"context"
	"errors"
	"testing"

	"juhe-ai/backend-go/internal/modules/managementstats"
	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceOverviewMapsPreaggregatedWindows(t *testing.T) {
	lastUsedAt := "2026-07-22T08:30:00.000Z"
	errorMessage := "upstream unavailable"
	reader := &overviewReaderStub{
		window: port.ManagementStatsOverviewWindow{
			Summary: &port.ManagementStatsOverviewSummaryRow{
				RequestCount: 4, SuccessCount: 3, ErrorCount: 1,
				InputTokens: 10, OutputTokens: 20, CacheReadTokens: 5,
				CacheReadCost: 0.1, CacheWriteTokens: 6, CacheWrite1hTokens: 2,
				CacheWriteCost: 0.2, ThinkingTokens: 7, InputImageTokens: 8,
				OutputImageTokens: 9, TotalCost: 0.3, DurationMsSum: 300,
				DurationMsCount: 2, FirstTokenMsSum: 40, FirstTokenMsCount: 2,
				LastUsedAt: &lastUsedAt,
			},
			HourlyTrend: []port.ManagementStatsOverviewTrendRow{{
				StatHour: "2026-07-22T08", RequestCount: 4, ErrorCount: 1,
				InputTokens: 10, OutputTokens: 20, CacheReadTokens: 5,
				CacheWriteTokens: 6, CacheWrite1hTokens: 2, CacheWriteCost: 0.2,
				ThinkingTokens: 7, InputImageTokens: 8, OutputImageTokens: 9,
				TotalCost: 0.3, DurationMsSum: 300, DurationMsCount: 2,
			}},
			ModelDistribution: []port.ManagementStatsOverviewModelRow{{
				ProviderCode: "openai", Model: "gpt-5", RequestCount: 4,
				InputTokens: 10, OutputTokens: 20, CacheReadTokens: 5,
				CacheWriteTokens: 6, CacheWrite1hTokens: 2, CacheWriteCost: 0.2,
				ThinkingTokens: 7, InputImageTokens: 8, OutputImageTokens: 9,
				TotalCost: 0.3,
			}},
			Errors: []port.ManagementStatsOverviewErrorRow{{
				ProviderCode: "openai", ErrorCode: "upstream_error", StatusCode: 503,
				ErrorMessage: &errorMessage, ErrorCount: 1,
			}},
		},
	}
	service := NewService(ServiceOptions{
		Reader:       reader,
		WindowReader: usageWindowReaderStub{window: managementstats.UsageWindow{Timezone: "Asia/Shanghai", StartDate: "2026-06-22", EndDate: "2026-07-22", Days: 31, MaxDays: 31}},
	})

	got, err := service.Overview(context.Background(), "global", Input{})

	if err != nil {
		t.Fatalf("Overview() error = %v", err)
	}
	if reader.input != (port.ManagementStatsOverviewReadInput{
		SystemAccountID: "global",
		WindowKey:       "2026-07-22:2026-07-22",
		StartDate:       "2026-07-22",
		EndDate:         "2026-07-22",
	}) {
		t.Fatalf("reader input = %+v", reader.input)
	}
	if got.Range.StartDate != "2026-07-22" || got.Range.EndDate != "2026-07-22" || got.Range.Days != 1 || got.Range.MaxDays != 31 {
		t.Fatalf("range = %+v", got.Range)
	}
	if got.Summary.RequestCount != 4 || got.Summary.TotalTokens != 30 || got.Summary.ErrorRate != 0.25 ||
		got.Summary.AverageDurationMs == nil || *got.Summary.AverageDurationMs != 150 ||
		got.Summary.AverageFirstTokenMs == nil || *got.Summary.AverageFirstTokenMs != 20 ||
		got.Summary.LastUsedAt == nil || *got.Summary.LastUsedAt != lastUsedAt {
		t.Fatalf("summary = %+v", got.Summary)
	}
	if len(got.HourlyTrend) != 1 || got.HourlyTrend[0].TotalTokens != 30 ||
		got.HourlyTrend[0].AverageDurationMs == nil || *got.HourlyTrend[0].AverageDurationMs != 150 {
		t.Fatalf("hourly trend = %+v", got.HourlyTrend)
	}
	if len(got.ModelDistribution) != 1 || got.ModelDistribution[0].TotalTokens != 30 ||
		len(got.Errors) != 1 || got.Errors[0].StatusCode == nil || *got.Errors[0].StatusCode != 503 {
		t.Fatalf("models/errors = %+v / %+v", got.ModelDistribution, got.Errors)
	}
}

func TestServiceOverviewNormalizesNodeCompatibleDateWindows(t *testing.T) {
	tests := []struct {
		name      string
		input     Input
		wantStart string
		wantEnd   string
	}{
		{name: "one boundary becomes one day", input: Input{StartDate: "2026-07-10"}, wantStart: "2026-07-10", wantEnd: "2026-07-10"},
		{name: "old dates clamp to supported floor", input: Input{StartDate: "2020-01-01", EndDate: "2020-02-01"}, wantStart: "2026-06-22", wantEnd: "2026-06-22"},
		{name: "future dates clamp to today", input: Input{StartDate: "2099-01-01", EndDate: "2099-02-01"}, wantStart: "2026-07-22", wantEnd: "2026-07-22"},
		{name: "start after end collapses to end", input: Input{StartDate: "2026-07-20", EndDate: "2026-07-10"}, wantStart: "2026-07-10", wantEnd: "2026-07-10"},
		{name: "calendar-invalid formatted values fall back to today", input: Input{StartDate: "2026-99-99", EndDate: "2026-02-30"}, wantStart: "2026-07-22", wantEnd: "2026-07-22"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := &overviewReaderStub{}
			service := NewService(ServiceOptions{
				Reader:       reader,
				WindowReader: usageWindowReaderStub{window: managementstats.UsageWindow{Timezone: "Asia/Shanghai", StartDate: "2026-06-22", EndDate: "2026-07-22", Days: 31, MaxDays: 31}},
			})

			got, err := service.Overview(context.Background(), "sys_1", test.input)

			if err != nil {
				t.Fatalf("Overview() error = %v", err)
			}
			if got.Range.StartDate != test.wantStart || got.Range.EndDate != test.wantEnd ||
				reader.input.StartDate != test.wantStart || reader.input.EndDate != test.wantEnd {
				t.Fatalf("range = %+v, reader input = %+v", got.Range, reader.input)
			}
			if got.HourlyTrend == nil || got.ModelDistribution == nil || got.Errors == nil {
				t.Fatalf("empty collections must encode as arrays: %+v", got)
			}
		})
	}
}

func TestServiceOverviewRejectsMissingDependenciesAndPropagatesReads(t *testing.T) {
	readErr := errors.New("postgres down")
	tests := []struct {
		name    string
		service *Service
		wantErr error
	}{
		{name: "missing reader", service: NewService(ServiceOptions{WindowReader: usageWindowReaderStub{}})},
		{name: "missing window reader", service: NewService(ServiceOptions{Reader: &overviewReaderStub{}})},
		{name: "window error", service: NewService(ServiceOptions{Reader: &overviewReaderStub{}, WindowReader: usageWindowReaderStub{err: readErr}}), wantErr: readErr},
		{name: "overview error", service: NewService(ServiceOptions{Reader: &overviewReaderStub{err: readErr}, WindowReader: usageWindowReaderStub{window: managementstats.UsageWindow{EndDate: "2026-07-22", MaxDays: 31}}}), wantErr: readErr},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.service.Overview(context.Background(), "sys_1", Input{})
			if err == nil {
				t.Fatal("Overview() error = nil")
			}
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("Overview() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

type usageWindowReaderStub struct {
	window managementstats.UsageWindow
	err    error
}

func (s usageWindowReaderStub) UsageWindow(context.Context) (managementstats.UsageWindow, error) {
	return s.window, s.err
}

type overviewReaderStub struct {
	input  port.ManagementStatsOverviewReadInput
	window port.ManagementStatsOverviewWindow
	err    error
}

func (s *overviewReaderStub) ReadManagementStatsOverview(_ context.Context, input port.ManagementStatsOverviewReadInput) (port.ManagementStatsOverviewWindow, error) {
	s.input = input
	return s.window, s.err
}

var _ port.ManagementStatsOverviewReader = (*overviewReaderStub)(nil)
