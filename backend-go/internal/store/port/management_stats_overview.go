package port

import "context"

type ManagementStatsOverviewReadInput struct {
	SystemAccountID string
	WindowKey       string
	StartDate       string
	EndDate         string
}

type ManagementStatsOverviewSummaryRow struct {
	RequestCount       int64
	SuccessCount       int64
	ErrorCount         int64
	InputTokens        int64
	OutputTokens       int64
	CacheReadTokens    int64
	CacheReadCost      float64
	CacheWriteTokens   int64
	CacheWrite1hTokens int64
	CacheWriteCost     float64
	ThinkingTokens     int64
	InputImageTokens   int64
	OutputImageTokens  int64
	TotalCost          float64
	DurationMsSum      int64
	DurationMsCount    int64
	FirstTokenMsSum    int64
	FirstTokenMsCount  int64
	LastUsedAt         *string
}

type ManagementStatsOverviewTrendRow struct {
	StatHour           string
	RequestCount       int64
	ErrorCount         int64
	InputTokens        int64
	OutputTokens       int64
	CacheReadTokens    int64
	CacheWriteTokens   int64
	CacheWrite1hTokens int64
	CacheWriteCost     float64
	ThinkingTokens     int64
	InputImageTokens   int64
	OutputImageTokens  int64
	TotalCost          float64
	DurationMsSum      int64
	DurationMsCount    int64
}

type ManagementStatsOverviewDailyRow struct {
	StatDate     string
	InputTokens  int64
	OutputTokens int64
	TotalCost    float64
}

type ManagementStatsOverviewModelRow struct {
	ProviderCode       string
	Model              string
	RequestCount       int64
	InputTokens        int64
	OutputTokens       int64
	CacheReadTokens    int64
	CacheWriteTokens   int64
	CacheWrite1hTokens int64
	CacheWriteCost     float64
	ThinkingTokens     int64
	InputImageTokens   int64
	OutputImageTokens  int64
	TotalCost          float64
}

type ManagementStatsOverviewErrorRow struct {
	ProviderCode string
	ErrorCode    string
	StatusCode   int32
	ErrorMessage *string
	ErrorCount   int64
}

type ManagementStatsOverviewWindow struct {
	Summary           *ManagementStatsOverviewSummaryRow
	HourlyTrend       []ManagementStatsOverviewTrendRow
	ModelDistribution []ManagementStatsOverviewModelRow
	Errors            []ManagementStatsOverviewErrorRow
}

type ManagementStatsOverviewReader interface {
	ReadManagementStatsOverviewSummary(ctx context.Context, input ManagementStatsOverviewReadInput) (ManagementStatsOverviewSummaryRow, bool, error)
	ReadManagementStatsOverviewDailyTrend(ctx context.Context, input ManagementStatsOverviewReadInput) ([]ManagementStatsOverviewDailyRow, error)
	ReadManagementStatsOverviewHourlyTrend(ctx context.Context, input ManagementStatsOverviewReadInput) ([]ManagementStatsOverviewTrendRow, error)
	ReadManagementStatsOverviewModelDistribution(ctx context.Context, input ManagementStatsOverviewReadInput) ([]ManagementStatsOverviewModelRow, error)
	ReadManagementStatsOverviewErrors(ctx context.Context, input ManagementStatsOverviewReadInput) ([]ManagementStatsOverviewErrorRow, error)
}
