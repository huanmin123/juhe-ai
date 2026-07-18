package port

import (
	"context"
	"time"
)

type ManagementClientIPStatsStatus string

const (
	ManagementClientIPStatsStatusAll         ManagementClientIPStatsStatus = "all"
	ManagementClientIPStatsStatusNormal      ManagementClientIPStatsStatus = "normal"
	ManagementClientIPStatsStatusBlacklisted ManagementClientIPStatsStatus = "blacklisted"
	ManagementClientIPStatsStatusAllowlisted ManagementClientIPStatsStatus = "allowlisted"
)

type ManagementClientIPStatsSortField string

const (
	ManagementClientIPStatsSortRequestCount ManagementClientIPStatsSortField = "requestCount"
	ManagementClientIPStatsSortSuccessCount ManagementClientIPStatsSortField = "successCount"
	ManagementClientIPStatsSortErrorCount   ManagementClientIPStatsSortField = "errorCount"
	ManagementClientIPStatsSortErrorRate    ManagementClientIPStatsSortField = "errorRate"
	ManagementClientIPStatsSortTotalTokens  ManagementClientIPStatsSortField = "totalTokens"
	ManagementClientIPStatsSortTotalCost    ManagementClientIPStatsSortField = "totalCost"
	ManagementClientIPStatsSortActiveDays   ManagementClientIPStatsSortField = "activeDays"
	ManagementClientIPStatsSortLastUsedAt   ManagementClientIPStatsSortField = "lastUsedAt"
)

type ManagementClientIPStatsSortOrder string

const (
	ManagementClientIPStatsSortAscending  ManagementClientIPStatsSortOrder = "asc"
	ManagementClientIPStatsSortDescending ManagementClientIPStatsSortOrder = "desc"
)

type ManagementClientIPStatsListInput struct {
	StartDate              string
	EndDate                string
	Keyword                string
	Status                 ManagementClientIPStatsStatus
	LastUsedStartAt        *time.Time
	LastUsedEndExclusiveAt *time.Time
	SortField              ManagementClientIPStatsSortField
	SortOrder              ManagementClientIPStatsSortOrder
	Now                    time.Time
	Limit                  int
	Offset                 int
}

type ManagementClientIPUsageSummary struct {
	RequestCount        int64
	SuccessCount        int64
	ErrorCount          int64
	ErrorRate           float64
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheReadCost       float64
	CacheWriteTokens    int64
	CacheWrite1hTokens  int64
	CacheWriteCost      float64
	ThinkingTokens      int64
	InputImageTokens    int64
	OutputImageTokens   int64
	TotalTokens         int64
	TotalCost           float64
	ActiveDays          int32
	AverageDurationMs   *float64
	AverageFirstTokenMs *float64
	MaxDurationMs       *int64
	LastUsedAt          *string
	LastErrorAt         *string
}

type ManagementClientIPStatsListRow struct {
	IPHash         string
	AggregateIPKey string
	LastSeenAt     string
	Status         ManagementClientIPStatsStatus
	RangeUsage     ManagementClientIPUsageSummary
}

type ManagementClientIPStatsListPage struct {
	Rows       []ManagementClientIPStatsListRow
	HasMore    bool
	RangeReady bool
}

type ManagementClientIPStatsListReader interface {
	ListManagementClientIPStats(
		ctx context.Context,
		input ManagementClientIPStatsListInput,
	) (ManagementClientIPStatsListPage, error)
}

type ManagementClientIPStatsDetailInput struct {
	IPHash    string
	StartDate string
	EndDate   string
	SortField ManagementClientIPStatsSortField
	SortOrder ManagementClientIPStatsSortOrder
	Limit     int
	Offset    int
}

type ManagementClientIPStatsRegistry struct {
	IPHash         string
	AggregateIPKey string
	LastSeenAt     string
}

type ManagementClientIPStatsRegistryReader interface {
	FindManagementClientIPStatsRegistry(
		ctx context.Context,
		ipHash string,
	) (ManagementClientIPStatsRegistry, bool, error)
}

type ManagementClientIPAccountUsageRow struct {
	AccountID                     string
	AccountName                   *string
	AccountOwnerSystemAccountID   *string
	AccountOwnerSystemAccountName *string
	RangeUsage                    ManagementClientIPUsageSummary
}

type ManagementClientIPStatsDetailPage struct {
	Rows       []ManagementClientIPAccountUsageRow
	HasMore    bool
	RangeReady bool
}

type ManagementClientIPStatsDetailReader interface {
	ListManagementClientIPStatsDetail(
		ctx context.Context,
		input ManagementClientIPStatsDetailInput,
	) (ManagementClientIPStatsDetailPage, error)
}
