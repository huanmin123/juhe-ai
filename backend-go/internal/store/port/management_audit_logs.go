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

type ManagementAuditLogAttempt struct {
	ID, UpstreamMethod, UpstreamURL, StartedAt                       string
	AccountID, AccountName, AccountOwnerSystemAccountID              *string
	GroupID, GroupName, ProxyURL, ProviderCode                       *string
	Model, UpstreamModel, PricingModel                               *string
	ModelMappingSource, SourceEndpointFamily, UpstreamEndpointFamily *string
	ErrorPhase, ErrorCode, ErrorMessage, EndedAt                     *string
	AttemptIndex                                                     int
	ModelMappingApplied, Success                                     bool
	UpstreamStatusCode                                               *int
	DurationMs                                                       *int64
}

type ManagementAuditLogPayloadSummary struct {
	ID, PartType, CaptureStatus, CreatedAt  string
	AttemptID, ContentType, ContentEncoding *string
	HeadersSHA256, BodySHA256               *string
	SequenceIndex                           int
	SizeBytes, CompressedSizeBytes          int64
	HasHeaders, HasBody                     bool
}

type ManagementAuditErrorGroup struct {
	ID, Fingerprint, WindowStartedAt, WindowEndedAt, CreatedAt, UpdatedAt string
	SystemAccountID, SystemAccountName, APIKeyID, APIKeyName              *string
	GroupID, GroupName, AccountID, AccountName, ProviderCode              *string
	Path, Model, ErrorPhase, ErrorCode, ErrorType                         *string
	RequestFingerprint, ErrorFingerprint                                  *string
	FirstEventID, LastEventID, SampleEventID, LastMessage                 *string
	StatusCode                                                            *int
	Count                                                                 int
}

type ManagementAuditLogDetail struct {
	ManagementAuditLogSummary
	Attempts   []ManagementAuditLogAttempt
	ErrorGroup *ManagementAuditErrorGroup
	Payloads   []ManagementAuditLogPayloadSummary
}

type ManagementAuditLogReader interface {
	ListManagementAuditLogs(context.Context, ManagementAuditLogListInput) (ManagementAuditLogListResult, error)
	GetManagementAuditLog(context.Context, string) (ManagementAuditLogDetail, bool, error)
	ListManagementAuditLogsByIDs(context.Context, []string) ([]ManagementAuditLogSummary, error)
}
