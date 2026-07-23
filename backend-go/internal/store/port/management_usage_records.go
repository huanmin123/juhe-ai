package port

import (
	"context"
	"time"
)

type ManagementUsageRecordListInput struct {
	SystemAccountID string
	TraceID         string
	AccountKeyword  string
	ClientIP        string
	Result          string
	StatusCode      *int
	GroupID         string
	Model           string
	TrafficSource   string
	StartAt         time.Time
	EndAt           time.Time
	SortAscending   bool
	Limit           int
	Offset          int
}

type ManagementUsageRecordSummary struct {
	ID                        string
	SystemAccountID           *string
	SystemAccountName         *string
	TraceID                   string
	TrafficSource             string
	ClientIP                  *string
	APIKeyID                  *string
	APIKeyName                *string
	GroupID                   *string
	GroupName                 *string
	AccountID                 *string
	AccountName               *string
	Endpoint                  *string
	ProviderCode              *string
	ProviderProtocolProfileID *string
	UsageSemantic             *string
	Model                     *string
	UpstreamModel             *string
	PricingModel              *string
	RequestedServiceTier      *string
	EffectiveServiceTier      *string
	ReportedServiceTier       *string
	BilledServiceTier         *string
	RequestedReasoningEffort  *string
	EffectiveReasoningEffort  *string
	CostBreakdownSnapshotJSON *string
	ModelMappingApplied       bool
	ModelMappingSource        *string
	SourceEndpointFamily      *string
	UpstreamEndpointFamily    *string
	Stream                    bool
	StatusCode                *int
	Success                   bool
	FailureAttribution        *string
	FirstTokenMs              *int64
	DurationMs                *int64
	InputTokens               *int64
	OutputTokens              *int64
	CacheReadTokens           *int64
	CacheReadCostUSD          *float64
	CacheWriteTokens          *int64
	CacheWrite1hTokens        *int64
	CacheWriteCostUSD         *float64
	ThinkingTokens            *int64
	InputImageTokens          *int64
	OutputImageTokens         *int64
	InputAudioTokens          *int64
	OutputAudioTokens         *int64
	OutputImageCount          *int64
	CostUSD                   *float64
	ErrorCode                 *string
	ErrorMessage              *string
	CreatedAt                 time.Time
}

type ManagementUsageRecordListResult struct {
	Items   []ManagementUsageRecordSummary
	HasMore bool
}

type ManagementUsageRecordReader interface {
	ListManagementUsageRecords(context.Context, ManagementUsageRecordListInput) (ManagementUsageRecordListResult, error)
}
