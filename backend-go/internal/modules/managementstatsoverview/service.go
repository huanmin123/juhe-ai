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

type DailyPoint struct {
	StatDate    string  `json:"statDate"`
	TotalTokens int64   `json:"totalTokens"`
	TotalCost   float64 `json:"totalCost"`
}

type SummaryResult struct {
	Range   Range   `json:"range"`
	Summary Summary `json:"summary"`
}

type DailyTrendResult struct {
	Range      Range        `json:"range"`
	DailyTrend []DailyPoint `json:"dailyTrend"`
}

type HourlyTrendResult struct {
	Range       Range        `json:"range"`
	HourlyTrend []TrendPoint `json:"hourlyTrend"`
}

type ModelDistributionResult struct {
	Range             Range        `json:"range"`
	ModelDistribution []ModelPoint `json:"modelDistribution"`
}

type ErrorsResult struct {
	Range  Range        `json:"range"`
	Errors []ErrorPoint `json:"errors"`
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
	usageRange, readInput, err := s.normalizeReadInput(ctx, systemAccountID, input)
	if err != nil {
		return Overview{}, err
	}
	summary, found, err := s.reader.ReadManagementStatsOverviewSummary(ctx, readInput)
	if err != nil {
		return Overview{}, fmt.Errorf("read management stats overview summary: %w", err)
	}
	hourly, err := s.reader.ReadManagementStatsOverviewHourlyTrend(ctx, readInput)
	if err != nil {
		return Overview{}, fmt.Errorf("read management stats overview hourly trend: %w", err)
	}
	models, err := s.reader.ReadManagementStatsOverviewModelDistribution(ctx, readInput)
	if err != nil {
		return Overview{}, fmt.Errorf("read management stats overview model distribution: %w", err)
	}
	errors, err := s.reader.ReadManagementStatsOverviewErrors(ctx, readInput)
	if err != nil {
		return Overview{}, fmt.Errorf("read management stats overview errors: %w", err)
	}
	stored := port.ManagementStatsOverviewWindow{HourlyTrend: hourly, ModelDistribution: models, Errors: errors}
	if found {
		stored.Summary = &summary
	}
	return mapOverview(usageRange, stored), nil
}

func (s *Service) Summary(ctx context.Context, systemAccountID string, input Input) (SummaryResult, error) {
	usageRange, readInput, err := s.normalizeReadInput(ctx, systemAccountID, input)
	if err != nil {
		return SummaryResult{}, err
	}
	row, found, err := s.reader.ReadManagementStatsOverviewSummary(ctx, readInput)
	if err != nil {
		return SummaryResult{}, fmt.Errorf("read management stats overview summary: %w", err)
	}
	result := SummaryResult{Range: usageRange}
	if found {
		result.Summary = mapSummary(row)
	}
	return result, nil
}

func (s *Service) DailyTrend(ctx context.Context, systemAccountID string, input Input) (DailyTrendResult, error) {
	usageRange, readInput, err := s.normalizeReadInput(ctx, systemAccountID, input)
	if err != nil {
		return DailyTrendResult{}, err
	}
	rows, err := s.reader.ReadManagementStatsOverviewDailyTrend(ctx, readInput)
	if err != nil {
		return DailyTrendResult{}, fmt.Errorf("read management stats overview daily trend: %w", err)
	}
	return DailyTrendResult{Range: usageRange, DailyTrend: mapDailyTrend(usageRange, rows)}, nil
}

func (s *Service) HourlyTrend(ctx context.Context, systemAccountID string, input Input) (HourlyTrendResult, error) {
	usageRange, readInput, err := s.normalizeReadInput(ctx, systemAccountID, input)
	if err != nil {
		return HourlyTrendResult{}, err
	}
	rows, err := s.reader.ReadManagementStatsOverviewHourlyTrend(ctx, readInput)
	if err != nil {
		return HourlyTrendResult{}, fmt.Errorf("read management stats overview hourly trend: %w", err)
	}
	return HourlyTrendResult{Range: usageRange, HourlyTrend: mapHourlyTrend(rows)}, nil
}

func (s *Service) ModelDistribution(ctx context.Context, systemAccountID string, input Input) (ModelDistributionResult, error) {
	usageRange, readInput, err := s.normalizeReadInput(ctx, systemAccountID, input)
	if err != nil {
		return ModelDistributionResult{}, err
	}
	rows, err := s.reader.ReadManagementStatsOverviewModelDistribution(ctx, readInput)
	if err != nil {
		return ModelDistributionResult{}, fmt.Errorf("read management stats overview model distribution: %w", err)
	}
	return ModelDistributionResult{Range: usageRange, ModelDistribution: mapModelDistribution(rows)}, nil
}

func (s *Service) Errors(ctx context.Context, systemAccountID string, input Input) (ErrorsResult, error) {
	usageRange, readInput, err := s.normalizeReadInput(ctx, systemAccountID, input)
	if err != nil {
		return ErrorsResult{}, err
	}
	rows, err := s.reader.ReadManagementStatsOverviewErrors(ctx, readInput)
	if err != nil {
		return ErrorsResult{}, fmt.Errorf("read management stats overview errors: %w", err)
	}
	return ErrorsResult{Range: usageRange, Errors: mapErrors(rows)}, nil
}

func (s *Service) normalizeReadInput(ctx context.Context, systemAccountID string, input Input) (Range, port.ManagementStatsOverviewReadInput, error) {
	if s == nil || s.reader == nil {
		return Range{}, port.ManagementStatsOverviewReadInput{}, fmt.Errorf("management stats overview reader is required")
	}
	if s.windowReader == nil {
		return Range{}, port.ManagementStatsOverviewReadInput{}, fmt.Errorf("management stats usage window reader is required")
	}
	systemAccountID = strings.TrimSpace(systemAccountID)
	if systemAccountID == "" {
		return Range{}, port.ManagementStatsOverviewReadInput{}, fmt.Errorf("management stats overview system account id is required")
	}
	window, err := s.windowReader.UsageWindow(ctx)
	if err != nil {
		return Range{}, port.ManagementStatsOverviewReadInput{}, fmt.Errorf("read management stats usage window: %w", err)
	}
	usageRange, err := normalizeRange(window, input)
	if err != nil {
		return Range{}, port.ManagementStatsOverviewReadInput{}, err
	}
	return usageRange, port.ManagementStatsOverviewReadInput{
		SystemAccountID: systemAccountID,
		WindowKey:       usageRange.StartDate + ":" + usageRange.EndDate,
		StartDate:       usageRange.StartDate,
		EndDate:         usageRange.EndDate,
	}, nil
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
	startText := strings.TrimSpace(input.StartDate)
	endText := strings.TrimSpace(input.EndDate)
	if startText == "" && endText == "" {
		startText = strings.TrimSpace(window.StartDate)
		endText = strings.TrimSpace(window.EndDate)
	}
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
		overview.Summary = mapSummary(*stored.Summary)
	}
	overview.HourlyTrend = mapHourlyTrend(stored.HourlyTrend)
	overview.ModelDistribution = mapModelDistribution(stored.ModelDistribution)
	overview.Errors = mapErrors(stored.Errors)
	return overview
}

func mapSummary(row port.ManagementStatsOverviewSummaryRow) Summary {
	return Summary{
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

func mapDailyTrend(usageRange Range, rows []port.ManagementStatsOverviewDailyRow) []DailyPoint {
	byDate := make(map[string]port.ManagementStatsOverviewDailyRow, len(rows))
	for _, row := range rows {
		byDate[row.StatDate] = row
	}
	start, _ := time.Parse(time.DateOnly, usageRange.StartDate)
	result := make([]DailyPoint, 0, usageRange.Days)
	for offset := 0; offset < usageRange.Days; offset++ {
		statDate := start.AddDate(0, 0, offset).Format(time.DateOnly)
		row := byDate[statDate]
		result = append(result, DailyPoint{StatDate: statDate, TotalTokens: row.InputTokens + row.OutputTokens, TotalCost: row.TotalCost})
	}
	return result
}

func mapHourlyTrend(rows []port.ManagementStatsOverviewTrendRow) []TrendPoint {
	result := make([]TrendPoint, 0, len(rows))
	for _, row := range rows {
		result = append(result, TrendPoint{
			StatHour: row.StatHour, RequestCount: row.RequestCount, CacheReadTokens: row.CacheReadTokens,
			CacheWriteTokens: row.CacheWriteTokens, CacheWrite1hTokens: row.CacheWrite1hTokens,
			CacheWriteCost: row.CacheWriteCost, ThinkingTokens: row.ThinkingTokens,
			InputImageTokens: row.InputImageTokens, OutputImageTokens: row.OutputImageTokens,
			TotalTokens: row.InputTokens + row.OutputTokens, TotalCost: row.TotalCost,
			AverageDurationMs: average(row.DurationMsSum, row.DurationMsCount), ErrorCount: row.ErrorCount,
		})
	}
	return result
}

func mapModelDistribution(rows []port.ManagementStatsOverviewModelRow) []ModelPoint {
	result := make([]ModelPoint, 0, len(rows))
	for _, row := range rows {
		result = append(result, ModelPoint{
			Model: row.Model, ProviderCode: row.ProviderCode, RequestCount: row.RequestCount,
			TotalTokens: row.InputTokens + row.OutputTokens, CacheReadTokens: row.CacheReadTokens,
			CacheWriteTokens: row.CacheWriteTokens, CacheWrite1hTokens: row.CacheWrite1hTokens,
			CacheWriteCost: row.CacheWriteCost, ThinkingTokens: row.ThinkingTokens,
			InputImageTokens: row.InputImageTokens, OutputImageTokens: row.OutputImageTokens, TotalCost: row.TotalCost,
		})
	}
	return result
}

func mapErrors(rows []port.ManagementStatsOverviewErrorRow) []ErrorPoint {
	result := make([]ErrorPoint, 0, len(rows))
	for _, row := range rows {
		var statusCode *int32
		if row.StatusCode != 0 {
			value := row.StatusCode
			statusCode = &value
		}
		result = append(result, ErrorPoint{
			ErrorCode: row.ErrorCode, ProviderCode: row.ProviderCode, StatusCode: statusCode,
			ErrorMessage: row.ErrorMessage, ErrorCount: row.ErrorCount,
		})
	}
	return result
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
