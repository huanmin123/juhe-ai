package port

import "context"

type ManagementStatsScope struct {
	SystemAccountID            string
	ScopeType                  string
	ViewerSystemAccountID      string
	IncludeSystemAccountFields bool
}

type ManagementStatsRange struct {
	StartDate string
	EndDate   string
}

type ManagementUsageAggregate struct {
	RequestCount       int64
	InputTokens        int64
	OutputTokens       int64
	CacheReadTokens    int64
	CacheReadCostUSD   float64
	CacheWriteTokens   int64
	CacheWrite1hTokens int64
	CacheWriteCostUSD  float64
	ThinkingTokens     int64
	InputImageTokens   int64
	OutputImageTokens  int64
	TotalCostUSD       float64
	LastUsedAt         *string
}

type ManagementStatsAccount struct {
	ID                     string
	Name                   string
	Type                   string
	Status                 string
	ProviderCode           string
	SystemAccountID        string
	SystemAccountName      string
	OwnerSystemAccountID   string
	OwnerSystemAccountName string
	AccessType             string
	RequestCountLast7d     int64
}

type ManagementAccountUsageRow struct {
	Account ManagementStatsAccount
	Usage   ManagementUsageAggregate
}

type ManagementAccountUsageReadInput struct {
	Scope      ManagementStatsScope
	Range      ManagementStatsRange
	Page       int
	PageSize   int
	Keyword    string
	AccountIDs []string
}

type ManagementAccountUsageReadResult struct {
	Rows                   []ManagementAccountUsageRow
	Summary                ManagementUsageAggregate
	DefaultTrendAccountIDs []string
	PageRowCount           int
	HasMore                bool
}

type ManagementAccountUsageDailyRow struct {
	AccountID string
	StatDate  string
	Usage     ManagementUsageAggregate
}

type ManagementAccountUsageTrendReadInput struct {
	Scope      ManagementStatsScope
	Range      ManagementStatsRange
	AccountIDs []string
}

type ManagementAccountUsageTrendReadResult struct {
	Accounts  []ManagementStatsAccount
	DailyRows []ManagementAccountUsageDailyRow
}

type ManagementAIPerformanceAggregate struct {
	RequestCount      int64
	FirstTokenMSSum   int64
	FirstTokenMSCount int64
	FirstTokenMSMax   int64
	DurationMSSum     int64
	DurationMSCount   int64
	DurationMSMax     int64
}

type ManagementAIPerformanceHourlyRow struct {
	AccountID         string
	StatHour          string
	RequestCount      int64
	FirstTokenMSSum   int64
	FirstTokenMSCount int64
	FirstTokenMSMax   int64
	DurationMSSum     int64
	DurationMSCount   int64
	DurationMSMax     int64
}

type ManagementAIPerformanceReadInput struct {
	Scope      ManagementStatsScope
	Range      ManagementStatsRange
	AccountIDs []string
}

type ManagementAIPerformanceReadResult struct {
	DefaultAccounts  []ManagementStatsAccount
	SelectedAccounts []ManagementStatsAccount
	HourlyRows       []ManagementAIPerformanceHourlyRow
	Summary          ManagementAIPerformanceAggregate
}

type ManagementAIPerformanceAccountsReadInput struct {
	Scope      ManagementStatsScope
	Keyword    string
	AccountIDs []string
	Limit      int
}

type ManagementStatsReader interface {
	ReadManagementAccountUsage(context.Context, ManagementAccountUsageReadInput) (ManagementAccountUsageReadResult, error)
	ReadManagementAccountUsageTrend(context.Context, ManagementAccountUsageTrendReadInput) (ManagementAccountUsageTrendReadResult, error)
	ReadManagementAIPerformance(context.Context, ManagementAIPerformanceReadInput) (ManagementAIPerformanceReadResult, error)
	ReadManagementAIPerformanceAccounts(context.Context, ManagementAIPerformanceAccountsReadInput) ([]ManagementStatsAccount, error)
}
