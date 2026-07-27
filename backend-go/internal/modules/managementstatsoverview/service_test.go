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
		WindowKey:       "2026-06-22:2026-07-22",
		StartDate:       "2026-06-22",
		EndDate:         "2026-07-22",
	}) {
		t.Fatalf("reader input = %+v", reader.input)
	}
	if got.Range.StartDate != "2026-06-22" || got.Range.EndDate != "2026-07-22" || got.Range.Days != 31 || got.Range.MaxDays != 31 {
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
		{name: "empty input uses the fixed window", input: Input{}, wantStart: "2026-06-22", wantEnd: "2026-07-22"},
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

func TestServiceProgressiveSectionsReadOnlyTheirOwnSource(t *testing.T) {
	reader := &overviewReaderStub{
		daily: []port.ManagementStatsOverviewDailyRow{{StatDate: "2026-07-01", InputTokens: 2, OutputTokens: 3, TotalCost: 0.5}},
		window: port.ManagementStatsOverviewWindow{
			Summary:           &port.ManagementStatsOverviewSummaryRow{RequestCount: 1},
			HourlyTrend:       []port.ManagementStatsOverviewTrendRow{{StatHour: "2026-07-01T00"}},
			ModelDistribution: []port.ManagementStatsOverviewModelRow{{Model: "gpt-5"}},
			Errors:            []port.ManagementStatsOverviewErrorRow{{ErrorCode: "timeout"}},
		},
	}
	service := NewService(ServiceOptions{
		Reader:       reader,
		WindowReader: usageWindowReaderStub{window: managementstats.UsageWindow{StartDate: "2026-06-22", EndDate: "2026-07-22", MaxDays: 31}},
	})

	daily, err := service.DailyTrend(context.Background(), "sys_1", Input{StartDate: "2026-07-01", EndDate: "2026-07-03"})
	if err != nil {
		t.Fatalf("DailyTrend() error = %v", err)
	}
	if reader.dailyCalls != 1 || reader.summaryCalls != 0 || reader.hourlyCalls != 0 || reader.modelCalls != 0 || reader.errorCalls != 0 {
		t.Fatalf("calls summary/daily/hourly/models/errors = %d/%d/%d/%d/%d", reader.summaryCalls, reader.dailyCalls, reader.hourlyCalls, reader.modelCalls, reader.errorCalls)
	}
	if len(daily.DailyTrend) != 3 || daily.DailyTrend[0].TotalTokens != 5 || daily.DailyTrend[1].StatDate != "2026-07-02" || daily.DailyTrend[1].TotalTokens != 0 || daily.DailyTrend[2].StatDate != "2026-07-03" {
		t.Fatalf("daily trend = %+v", daily.DailyTrend)
	}

	reader.resetCalls()
	if _, err := service.Summary(context.Background(), "sys_1", Input{}); err != nil {
		t.Fatalf("Summary() error = %v", err)
	}
	if reader.summaryCalls != 1 || reader.dailyCalls+reader.hourlyCalls+reader.modelCalls+reader.errorCalls != 0 {
		t.Fatalf("summary calls = %+v", reader)
	}

	reader.resetCalls()
	if _, err := service.HourlyTrend(context.Background(), "sys_1", Input{}); err != nil {
		t.Fatalf("HourlyTrend() error = %v", err)
	}
	if reader.hourlyCalls != 1 || reader.summaryCalls+reader.dailyCalls+reader.modelCalls+reader.errorCalls != 0 {
		t.Fatalf("hourly calls = %+v", reader)
	}

	reader.resetCalls()
	if _, err := service.ModelDistribution(context.Background(), "sys_1", Input{}); err != nil {
		t.Fatalf("ModelDistribution() error = %v", err)
	}
	if reader.modelCalls != 1 || reader.summaryCalls+reader.dailyCalls+reader.hourlyCalls+reader.errorCalls != 0 {
		t.Fatalf("model calls = %+v", reader)
	}

	reader.resetCalls()
	if _, err := service.Errors(context.Background(), "sys_1", Input{}); err != nil {
		t.Fatalf("Errors() error = %v", err)
	}
	if reader.errorCalls != 1 || reader.summaryCalls+reader.dailyCalls+reader.hourlyCalls+reader.modelCalls != 0 {
		t.Fatalf("error calls = %+v", reader)
	}
}

func TestServiceOverviewRoundsAveragesLikeNode(t *testing.T) {
	reader := &overviewReaderStub{window: port.ManagementStatsOverviewWindow{
		Summary: &port.ManagementStatsOverviewSummaryRow{
			DurationMsSum: 5, DurationMsCount: 2,
			FirstTokenMsSum: 7, FirstTokenMsCount: 2,
		},
		HourlyTrend: []port.ManagementStatsOverviewTrendRow{{
			StatHour: "2026-07-22T08", DurationMsSum: 5, DurationMsCount: 2,
		}},
	}}
	service := NewService(ServiceOptions{
		Reader:       reader,
		WindowReader: usageWindowReaderStub{window: managementstats.UsageWindow{StartDate: "2026-06-22", EndDate: "2026-07-22", MaxDays: 31}},
	})

	got, err := service.Overview(context.Background(), "sys_1", Input{})

	if err != nil {
		t.Fatalf("Overview() error = %v", err)
	}
	if got.Summary.AverageDurationMs == nil || *got.Summary.AverageDurationMs != 3 ||
		got.Summary.AverageFirstTokenMs == nil || *got.Summary.AverageFirstTokenMs != 4 ||
		len(got.HourlyTrend) != 1 || got.HourlyTrend[0].AverageDurationMs == nil ||
		*got.HourlyTrend[0].AverageDurationMs != 3 {
		t.Fatalf("rounded averages = summary %+v, trend %+v", got.Summary, got.HourlyTrend)
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
	input        port.ManagementStatsOverviewReadInput
	window       port.ManagementStatsOverviewWindow
	daily        []port.ManagementStatsOverviewDailyRow
	err          error
	summaryCalls int
	dailyCalls   int
	hourlyCalls  int
	modelCalls   int
	errorCalls   int
}

func (s *overviewReaderStub) ReadManagementStatsOverviewSummary(_ context.Context, input port.ManagementStatsOverviewReadInput) (port.ManagementStatsOverviewSummaryRow, bool, error) {
	s.input = input
	s.summaryCalls++
	if s.err != nil || s.window.Summary == nil {
		return port.ManagementStatsOverviewSummaryRow{}, false, s.err
	}
	return *s.window.Summary, true, nil
}

func (s *overviewReaderStub) ReadManagementStatsOverviewDailyTrend(_ context.Context, input port.ManagementStatsOverviewReadInput) ([]port.ManagementStatsOverviewDailyRow, error) {
	s.input = input
	s.dailyCalls++
	return s.daily, s.err
}

func (s *overviewReaderStub) ReadManagementStatsOverviewHourlyTrend(_ context.Context, input port.ManagementStatsOverviewReadInput) ([]port.ManagementStatsOverviewTrendRow, error) {
	s.input = input
	s.hourlyCalls++
	return s.window.HourlyTrend, s.err
}

func (s *overviewReaderStub) ReadManagementStatsOverviewModelDistribution(_ context.Context, input port.ManagementStatsOverviewReadInput) ([]port.ManagementStatsOverviewModelRow, error) {
	s.input = input
	s.modelCalls++
	return s.window.ModelDistribution, s.err
}

func (s *overviewReaderStub) ReadManagementStatsOverviewErrors(_ context.Context, input port.ManagementStatsOverviewReadInput) ([]port.ManagementStatsOverviewErrorRow, error) {
	s.input = input
	s.errorCalls++
	return s.window.Errors, s.err
}

func (s *overviewReaderStub) resetCalls() {
	s.summaryCalls = 0
	s.dailyCalls = 0
	s.hourlyCalls = 0
	s.modelCalls = 0
	s.errorCalls = 0
}

var _ port.ManagementStatsOverviewReader = (*overviewReaderStub)(nil)
