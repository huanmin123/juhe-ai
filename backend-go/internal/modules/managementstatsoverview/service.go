package managementstatsoverview

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/modules/managementstats"
	"juhe-ai/backend-go/internal/store/port"
)

const globalStatsSystemAccountID = "global"

type UsageWindowReader interface {
	UsageWindow(ctx context.Context) (managementstats.UsageWindow, error)
}

type Service struct {
	reader       port.ManagementStatsOverviewReader
	windowReader UsageWindowReader
}

type ServiceOptions struct {
	Reader       port.ManagementStatsOverviewReader
	WindowReader UsageWindowReader
}

type Input struct {
	StartDate string
	EndDate   string
}

type Range struct {
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
	Days      int    `json:"days"`
	MaxDays   int    `json:"maxDays"`
}

type Summary struct {
	RequestCount        int64    `json:"requestCount"`
	InputTokens         int64    `json:"inputTokens"`
	OutputTokens        int64    `json:"outputTokens"`
	CacheReadTokens     int64    `json:"cacheReadTokens"`
	CacheReadCost       float64  `json:"cacheReadCost"`
	CacheWriteTokens    int64    `json:"cacheWriteTokens"`
	CacheWrite1hTokens  int64    `json:"cacheWrite1hTokens"`
	CacheWriteCost      float64  `json:"cacheWriteCost"`
	ThinkingTokens      int64    `json:"thinkingTokens"`
	InputImageTokens    int64    `json:"inputImageTokens"`
	OutputImageTokens   int64    `json:"outputImageTokens"`
	TotalTokens         int64    `json:"totalTokens"`
	TotalCost           float64  `json:"totalCost"`
	LastUsedAt          *string  `json:"lastUsedAt,omitempty"`
	SuccessCount        int64    `json:"successCount"`
	ErrorCount          int64    `json:"errorCount"`
	ErrorRate           float64  `json:"errorRate"`
	AverageDurationMs   *float64 `json:"averageDurationMs,omitempty"`
	AverageFirstTokenMs *float64 `json:"averageFirstTokenMs,omitempty"`
}

type TrendPoint struct {
	StatHour           string   `json:"statHour"`
	RequestCount       int64    `json:"requestCount"`
	CacheReadTokens    int64    `json:"cacheReadTokens"`
	CacheWriteTokens   int64    `json:"cacheWriteTokens"`
	CacheWrite1hTokens int64    `json:"cacheWrite1hTokens"`
	CacheWriteCost     float64  `json:"cacheWriteCost"`
	ThinkingTokens     int64    `json:"thinkingTokens"`
	InputImageTokens   int64    `json:"inputImageTokens"`
	OutputImageTokens  int64    `json:"outputImageTokens"`
	TotalTokens        int64    `json:"totalTokens"`
	TotalCost          float64  `json:"totalCost"`
	AverageDurationMs  *float64 `json:"averageDurationMs,omitempty"`
	ErrorCount         int64    `json:"errorCount"`
}

type ModelPoint struct {
	Model              string  `json:"model"`
	ProviderCode       string  `json:"providerCode"`
	RequestCount       int64   `json:"requestCount"`
	TotalTokens        int64   `json:"totalTokens"`
	CacheReadTokens    int64   `json:"cacheReadTokens"`
	CacheWriteTokens   int64   `json:"cacheWriteTokens"`
	CacheWrite1hTokens int64   `json:"cacheWrite1hTokens"`
	CacheWriteCost     float64 `json:"cacheWriteCost"`
	ThinkingTokens     int64   `json:"thinkingTokens"`
	InputImageTokens   int64   `json:"inputImageTokens"`
	OutputImageTokens  int64   `json:"outputImageTokens"`
	TotalCost          float64 `json:"totalCost"`
}

type ErrorPoint struct {
	ErrorCode    string  `json:"errorCode"`
	ProviderCode string  `json:"providerCode"`
	StatusCode   *int32  `json:"statusCode,omitempty"`
	ErrorMessage *string `json:"errorMessage,omitempty"`
	ErrorCount   int64   `json:"errorCount"`
}

type Overview struct {
	Range             Range        `json:"range"`
	Summary           Summary      `json:"summary"`
	HourlyTrend       []TrendPoint `json:"hourlyTrend"`
	ModelDistribution []ModelPoint `json:"modelDistribution"`
	Errors            []ErrorPoint `json:"errors"`
}

func NewService(opts ServiceOptions) *Service {
	return &Service{reader: opts.Reader, windowReader: opts.WindowReader}
}

func (s *Service) Overview(ctx context.Context, systemAccountID string, input Input) (Overview, error) {
	if s == nil || s.reader == nil {
		return Overview{}, fmt.Errorf("management stats overview reader is required")
	}
	if s.windowReader == nil {
		return Overview{}, fmt.Errorf("management stats usage window reader is required")
	}
	systemAccountID = strings.TrimSpace(systemAccountID)
	if systemAccountID == "" {
		return Overview{}, fmt.Errorf("management stats overview system account id is required")
	}
	window, err := s.windowReader.UsageWindow(ctx)
	if err != nil {
		return Overview{}, fmt.Errorf("read management stats usage window: %w", err)
	}
	usageRange, err := normalizeRange(window, input)
	if err != nil {
		return Overview{}, err
	}
	stored, err := s.reader.ReadManagementStatsOverview(ctx, port.ManagementStatsOverviewReadInput{
		SystemAccountID: systemAccountID,
		WindowKey:       usageRange.StartDate + ":" + usageRange.EndDate,
		StartDate:       usageRange.StartDate,
		EndDate:         usageRange.EndDate,
	})
	if err != nil {
		return Overview{}, fmt.Errorf("read management stats overview: %w", err)
	}
	return mapOverview(usageRange, stored), nil
}

func normalizeRange(window managementstats.UsageWindow, input Input) (Range, error) {
	today, err := time.Parse(time.DateOnly, window.EndDate)
	if err != nil {
		return Range{}, fmt.Errorf("management stats usage window end date is invalid: %w", err)
	}
	maxDays := window.MaxDays
	if maxDays <= 0 {
		return Range{}, fmt.Errorf("management stats usage window max days is invalid")
	}
	floor := today.AddDate(0, 0, -(maxDays - 1))
	if parsed, parseErr := time.Parse(time.DateOnly, window.StartDate); parseErr == nil && parsed.After(floor) {
		floor = parsed
	}
	startText := strings.TrimSpace(input.StartDate)
	endText := strings.TrimSpace(input.EndDate)
	if startText == "" {
		startText = endText
	}
	if endText == "" {
		endText = startText
	}
	start := parseDateOr(startText, today)
	end := parseDateOr(endText, today)
	end = clampDate(end, floor, today)
	start = clampDate(start, floor, today)
	if start.After(end) {
		start = end
	}
	earliestStart := end.AddDate(0, 0, -(maxDays - 1))
	if start.Before(earliestStart) {
		start = earliestStart
	}
	days := int(end.Sub(start).Hours()/24) + 1
	return Range{StartDate: start.Format(time.DateOnly), EndDate: end.Format(time.DateOnly), Days: days, MaxDays: maxDays}, nil
}

func parseDateOr(value string, fallback time.Time) time.Time {
	parsed, err := time.Parse(time.DateOnly, value)
	if err != nil {
		return fallback
	}
	return parsed
}

func clampDate(value time.Time, minimum time.Time, maximum time.Time) time.Time {
	if value.Before(minimum) {
		return minimum
	}
	if value.After(maximum) {
		return maximum
	}
	return value
}

func mapOverview(usageRange Range, stored port.ManagementStatsOverviewWindow) Overview {
	overview := Overview{
		Range:             usageRange,
		HourlyTrend:       make([]TrendPoint, 0, len(stored.HourlyTrend)),
		ModelDistribution: make([]ModelPoint, 0, len(stored.ModelDistribution)),
		Errors:            make([]ErrorPoint, 0, len(stored.Errors)),
	}
	if stored.Summary != nil {
		row := stored.Summary
		overview.Summary = Summary{
			RequestCount: row.RequestCount, InputTokens: row.InputTokens, OutputTokens: row.OutputTokens,
			CacheReadTokens: row.CacheReadTokens, CacheReadCost: row.CacheReadCost,
			CacheWriteTokens: row.CacheWriteTokens, CacheWrite1hTokens: row.CacheWrite1hTokens,
			CacheWriteCost: row.CacheWriteCost, ThinkingTokens: row.ThinkingTokens,
			InputImageTokens: row.InputImageTokens, OutputImageTokens: row.OutputImageTokens,
			TotalTokens: row.InputTokens + row.OutputTokens, TotalCost: row.TotalCost,
			LastUsedAt: row.LastUsedAt, SuccessCount: row.SuccessCount, ErrorCount: row.ErrorCount,
			ErrorRate: ratio(row.ErrorCount, row.RequestCount), AverageDurationMs: average(row.DurationMsSum, row.DurationMsCount),
			AverageFirstTokenMs: average(row.FirstTokenMsSum, row.FirstTokenMsCount),
		}
	}
	for _, row := range stored.HourlyTrend {
		overview.HourlyTrend = append(overview.HourlyTrend, TrendPoint{
			StatHour: row.StatHour, RequestCount: row.RequestCount, CacheReadTokens: row.CacheReadTokens,
			CacheWriteTokens: row.CacheWriteTokens, CacheWrite1hTokens: row.CacheWrite1hTokens,
			CacheWriteCost: row.CacheWriteCost, ThinkingTokens: row.ThinkingTokens,
			InputImageTokens: row.InputImageTokens, OutputImageTokens: row.OutputImageTokens,
			TotalTokens: row.InputTokens + row.OutputTokens, TotalCost: row.TotalCost,
			AverageDurationMs: average(row.DurationMsSum, row.DurationMsCount), ErrorCount: row.ErrorCount,
		})
	}
	for _, row := range stored.ModelDistribution {
		overview.ModelDistribution = append(overview.ModelDistribution, ModelPoint{
			Model: row.Model, ProviderCode: row.ProviderCode, RequestCount: row.RequestCount,
			TotalTokens: row.InputTokens + row.OutputTokens, CacheReadTokens: row.CacheReadTokens,
			CacheWriteTokens: row.CacheWriteTokens, CacheWrite1hTokens: row.CacheWrite1hTokens,
			CacheWriteCost: row.CacheWriteCost, ThinkingTokens: row.ThinkingTokens,
			InputImageTokens: row.InputImageTokens, OutputImageTokens: row.OutputImageTokens, TotalCost: row.TotalCost,
		})
	}
	for _, row := range stored.Errors {
		var statusCode *int32
		if row.StatusCode != 0 {
			value := row.StatusCode
			statusCode = &value
		}
		overview.Errors = append(overview.Errors, ErrorPoint{
			ErrorCode: row.ErrorCode, ProviderCode: row.ProviderCode, StatusCode: statusCode,
			ErrorMessage: row.ErrorMessage, ErrorCount: row.ErrorCount,
		})
	}
	return overview
}

func average(sum int64, count int64) *float64 {
	if count <= 0 {
		return nil
	}
	value := math.Round(float64(sum) / float64(count))
	return &value
}

func ratio(numerator int64, denominator int64) float64 {
	if denominator <= 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func GlobalSystemAccountID() string {
	return globalStatsSystemAccountID
}
