package managementauditlogs

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	defaultPageSize   = 100
	maxPageSize       = 100
	maxListWindowRows = 1001
)

type Service struct{ store port.ManagementAuditLogReader }

type ListInput struct {
	TraceID, ErrorGroupID, Outcome, Path, Model, SystemAccountID string
	APIKeyID, GroupID, AccountID, ClientIP, StartAt, EndAt       string
	TrafficSource                                                string
	StatusCode, Page, PageSize                                   int
	PageSizeProvided                                             bool
}

type ListResult struct {
	Items    []Summary `json:"items"`
	Total    int       `json:"total"`
	HasMore  bool      `json:"hasMore"`
	Page     int       `json:"page"`
	PageSize int       `json:"pageSize"`
}

type Summary struct {
	ID                     string `json:"id"`
	TraceID                string `json:"traceId"`
	TrafficSource          string `json:"trafficSource"`
	SystemAccountID        string `json:"systemAccountId,omitempty"`
	SystemAccountName      string `json:"systemAccountName,omitempty"`
	APIKeyID               string `json:"apiKeyId,omitempty"`
	APIKeyName             string `json:"apiKeyName,omitempty"`
	GroupID                string `json:"groupId,omitempty"`
	GroupName              string `json:"groupName,omitempty"`
	AccountID              string `json:"accountId,omitempty"`
	AccountName            string `json:"accountName,omitempty"`
	ProviderCode           string `json:"providerCode,omitempty"`
	Method                 string `json:"method"`
	Path                   string `json:"path"`
	QueryString            string `json:"queryString,omitempty"`
	Model                  string `json:"model,omitempty"`
	UpstreamModel          string `json:"upstreamModel,omitempty"`
	PricingModel           string `json:"pricingModel,omitempty"`
	ModelMappingApplied    bool   `json:"modelMappingApplied"`
	ModelMappingSource     string `json:"modelMappingSource,omitempty"`
	SourceEndpointFamily   string `json:"sourceEndpointFamily,omitempty"`
	UpstreamEndpointFamily string `json:"upstreamEndpointFamily,omitempty"`
	Stream                 bool   `json:"stream"`
	ClientIP               string `json:"clientIp,omitempty"`
	UserAgent              string `json:"userAgent,omitempty"`
	AuditOutcome           string `json:"auditOutcome"`
	Success                bool   `json:"success"`
	FinalStatusCode        *int   `json:"finalStatusCode,omitempty"`
	ErrorPhase             string `json:"errorPhase,omitempty"`
	ErrorCode              string `json:"errorCode,omitempty"`
	ErrorMessage           string `json:"errorMessage,omitempty"`
	SampleBucket           int    `json:"sampleBucket"`
	SampleReason           string `json:"sampleReason"`
	AttemptCount           int    `json:"attemptCount"`
	PayloadCount           int    `json:"payloadCount"`
	RawPayloadBytes        int64  `json:"rawPayloadBytes"`
	CompressedPayloadBytes int64  `json:"compressedPayloadBytes"`
	CompressionSavedBytes  int64  `json:"compressionSavedBytes"`
	ErrorGroupID           string `json:"errorGroupId,omitempty"`
	CaptureStatus          string `json:"captureStatus"`
	StartedAt              string `json:"startedAt"`
	EndedAt                string `json:"endedAt"`
	DurationMs             *int64 `json:"durationMs,omitempty"`
	HTTPCompletedAt        string `json:"httpCompletedAt,omitempty"`
	HTTPDurationMs         *int64 `json:"httpDurationMs,omitempty"`
	FirstTokenMs           *int64 `json:"firstTokenMs,omitempty"`
	CreatedAt              string `json:"createdAt"`
}

func NewService(store port.ManagementAuditLogReader) *Service { return &Service{store: store} }

func (s *Service) List(ctx context.Context, input ListInput) (ListResult, error) {
	if s.store == nil {
		return ListResult{}, fmt.Errorf("management audit log reader is required")
	}
	pageSize := defaultPageSize
	if input.PageSizeProvided {
		pageSize = min(max(input.PageSize, 1), maxPageSize)
	}
	page := min(max(input.Page, 1), max(1, (maxListWindowRows-1)/pageSize))
	result, err := s.store.ListManagementAuditLogs(ctx, port.ManagementAuditLogListInput{
		TraceID: trim(input.TraceID), ErrorGroupID: trim(input.ErrorGroupID), Outcome: normalizeOutcome(input.Outcome),
		StatusCode: normalizeStatusCode(input.StatusCode), Path: normalizePath(input.Path), Model: trim(input.Model),
		SystemAccountID: trim(input.SystemAccountID), APIKeyID: trim(input.APIKeyID), GroupID: trim(input.GroupID),
		AccountID: trim(input.AccountID), ClientIP: trim(input.ClientIP), StartAt: trim(input.StartAt), EndAt: trim(input.EndAt),
		TrafficSource: normalizeTrafficSource(input.TrafficSource), Limit: pageSize, Offset: (page - 1) * pageSize,
	})
	if err != nil {
		return ListResult{}, err
	}
	items := make([]Summary, 0, len(result.Items))
	for _, row := range result.Items {
		items = append(items, summary(row))
	}
	total := (page-1)*pageSize + len(items)
	if result.HasMore {
		total++
	}
	return ListResult{Items: items, Total: total, HasMore: result.HasMore, Page: page, PageSize: pageSize}, nil
}

func summary(row port.ManagementAuditLogSummary) Summary {
	return Summary{
		ID: row.ID, TraceID: row.TraceID, TrafficSource: row.TrafficSource,
		SystemAccountID: text(row.SystemAccountID), SystemAccountName: text(row.SystemAccountName), APIKeyID: text(row.APIKeyID), APIKeyName: text(row.APIKeyName),
		GroupID: text(row.GroupID), GroupName: text(row.GroupName), AccountID: text(row.AccountID), AccountName: text(row.AccountName), ProviderCode: text(row.ProviderCode),
		Method: row.Method, Path: row.Path, QueryString: text(row.QueryString), Model: text(row.Model), UpstreamModel: text(row.UpstreamModel), PricingModel: text(row.PricingModel),
		ModelMappingApplied: row.ModelMappingApplied, ModelMappingSource: text(row.ModelMappingSource), SourceEndpointFamily: text(row.SourceEndpointFamily), UpstreamEndpointFamily: text(row.UpstreamEndpointFamily),
		Stream: row.Stream, ClientIP: text(row.ClientIP), UserAgent: text(row.UserAgent), AuditOutcome: row.AuditOutcome, Success: row.Success, FinalStatusCode: row.FinalStatusCode,
		ErrorPhase: text(row.ErrorPhase), ErrorCode: text(row.ErrorCode), ErrorMessage: text(row.ErrorMessage), SampleBucket: row.SampleBucket, SampleReason: row.SampleReason,
		AttemptCount: row.AttemptCount, PayloadCount: row.PayloadCount, RawPayloadBytes: row.RawPayloadBytes, CompressedPayloadBytes: row.CompressedPayloadBytes,
		CompressionSavedBytes: row.CompressionSavedBytes, ErrorGroupID: text(row.ErrorGroupID), CaptureStatus: row.CaptureStatus, StartedAt: row.StartedAt, EndedAt: row.EndedAt,
		DurationMs: row.DurationMs, HTTPCompletedAt: text(row.HTTPCompletedAt), HTTPDurationMs: row.HTTPDurationMs, FirstTokenMs: row.FirstTokenMs, CreatedAt: row.CreatedAt,
	}
}

func normalizeOutcome(value string) string {
	switch trim(value) {
	case "success", "success_after_retry", "gateway_succeeded", "gateway_failed", "upstream_failed", "stream_failed", "client_aborted":
		return trim(value)
	default:
		return ""
	}
}
func normalizeTrafficSource(value string) string {
	switch trim(value) {
	case "gateway", "manual_account_test", "account_health_check", "runtime_recovery_probe", "cooldown_retest", "hybrid_scoring", "hybrid_quality_scoring":
		return trim(value)
	default:
		return ""
	}
}
func normalizeStatusCode(value int) *int {
	if value < 100 || value > 599 {
		return nil
	}
	return &value
}
func normalizePath(value string) string {
	value = trim(value)
	for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"} {
		if len(value) > len(method) && strings.EqualFold(value[:len(method)], method) {
			r, _ := utf8.DecodeRuneInString(value[len(method):])
			if isWhitespace(r) {
				value = strings.TrimLeftFunc(value[len(method):], isWhitespace)
				break
			}
		}
	}
	if i := strings.IndexByte(value, '?'); i >= 0 {
		value = value[:i]
	}
	return trim(value)
}
func text(value *string) string {
	if value == nil || *value == "" {
		return ""
	}
	return *value
}
func trim(value string) string { return strings.TrimFunc(value, isWhitespace) }
func isWhitespace(r rune) bool {
	switch r {
	case '\u0009', '\u000B', '\u000C', '\u0020', '\u00A0', '\u1680', '\u2000', '\u2001', '\u2002', '\u2003', '\u2004', '\u2005', '\u2006', '\u2007', '\u2008', '\u2009', '\u200A', '\u202F', '\u205F', '\u3000', '\uFEFF', '\u000A', '\u000D', '\u2028', '\u2029':
		return true
	default:
		return false
	}
}
