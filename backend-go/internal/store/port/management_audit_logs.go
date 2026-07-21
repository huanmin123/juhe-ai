package port

import "context"

type ManagementAuditLogListInput struct {
	TraceID, ErrorGroupID, Outcome, Path, Model, SystemAccountID string
	APIKeyID, GroupID, AccountID, ClientIP, StartAt, EndAt       string
	TrafficSource                                                string
	StatusCode                                                   *int
	Limit, Offset                                                int
}

type ManagementAuditLogSummary struct {
	ID, TraceID, TrafficSource, Method, Path, AuditOutcome           string
	SystemAccountID, SystemAccountName, APIKeyID, APIKeyName         *string
	GroupID, GroupName, AccountID, AccountName, ProviderCode         *string
	QueryString, Model, UpstreamModel, PricingModel                  *string
	ModelMappingApplied                                              bool
	ModelMappingSource, SourceEndpointFamily, UpstreamEndpointFamily *string
	Stream, Success                                                  bool
	ClientIP, UserAgent                                              *string
	FinalStatusCode                                                  *int
	ErrorPhase, ErrorCode, ErrorMessage                              *string
	SampleBucket                                                     int
	SampleReason                                                     string
	AttemptCount, PayloadCount                                       int
	RawPayloadBytes, CompressedPayloadBytes, CompressionSavedBytes   int64
	ErrorGroupID                                                     *string
	CaptureStatus, StartedAt, EndedAt, CreatedAt                     string
	DurationMs, HTTPDurationMs, FirstTokenMs                         *int64
	HTTPCompletedAt                                                  *string
}

type ManagementAuditLogListResult struct {
	Items   []ManagementAuditLogSummary
	HasMore bool
}

type ManagementAuditLogReader interface {
	ListManagementAuditLogs(context.Context, ManagementAuditLogListInput) (ManagementAuditLogListResult, error)
}
