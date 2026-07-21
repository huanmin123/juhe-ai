package managementusagerecords

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	defaultPageSize   = 50
	maxPageSize       = 200
	maxListWindowRows = 1001
	jsTimeLayout      = "2006-01-02T15:04:05.000Z"
)

type Store interface {
	port.ManagementUsageRecordReader
	port.ManagementUsageStatsTimezoneReader
}

type Service struct {
	store Store
	now   func() time.Time
}

type ServiceOptions struct {
	Store Store
	Now   func() time.Time
}

type ListInput struct {
	ScopeSystemAccountID string
	IncludeSystemAccount bool
	TraceID              string
	AccountKeyword       string
	ClientIP             string
	Result               string
	StatusCode           int
	GroupID              string
	Model                string
	TrafficSource        string
	StartDate            string
	EndDate              string
	SortOrder            string
	Page                 int
	PageSize             int
	PageSizeProvided     bool
}

type DetailInput struct {
	ID                   string
	ScopeSystemAccountID string
	IncludeSystemAccount bool
}

type ListResult struct {
	Items    []Summary `json:"items"`
	Total    int       `json:"total"`
	HasMore  bool      `json:"hasMore"`
	Page     int       `json:"page"`
	PageSize int       `json:"pageSize"`
}

type CostBreakdown struct {
	InputCostUSD             *float64 `json:"inputCostUsd,omitempty"`
	OutputCostUSD            *float64 `json:"outputCostUsd,omitempty"`
	InputUSDPer1M            *float64 `json:"inputUsdPer1M,omitempty"`
	OutputUSDPer1M           *float64 `json:"outputUsdPer1M,omitempty"`
	CacheReadCostUSD         *float64 `json:"cacheReadCostUsd,omitempty"`
	CacheReadUSDPer1M        *float64 `json:"cacheReadUsdPer1M,omitempty"`
	CacheWriteCostUSD        *float64 `json:"cacheWriteCostUsd,omitempty"`
	CacheWriteUSDPer1M       *float64 `json:"cacheWriteUsdPer1M,omitempty"`
	CacheWrite1hCostUSD      *float64 `json:"cacheWrite1hCostUsd,omitempty"`
	CacheWrite1hUSDPer1M     *float64 `json:"cacheWrite1hUsdPer1M,omitempty"`
	ThinkingTokens           *int64   `json:"thinkingTokens,omitempty"`
	InputImageCostUSD        *float64 `json:"inputImageCostUsd,omitempty"`
	OutputImageCostUSD       *float64 `json:"outputImageCostUsd,omitempty"`
	InputImageUSDPer1M       *float64 `json:"inputImageUsdPer1M,omitempty"`
	OutputImageUSDPer1M      *float64 `json:"outputImageUsdPer1M,omitempty"`
	InputAudioCostUSD        *float64 `json:"inputAudioCostUsd,omitempty"`
	OutputAudioCostUSD       *float64 `json:"outputAudioCostUsd,omitempty"`
	InputAudioUSDPer1M       *float64 `json:"inputAudioUsdPer1M,omitempty"`
	OutputAudioUSDPer1M      *float64 `json:"outputAudioUsdPer1M,omitempty"`
	OutputImageUnitCostUSD   *float64 `json:"outputImageUnitCostUsd,omitempty"`
	OutputUSDPerImage        *float64 `json:"outputUsdPerImage,omitempty"`
	AccountChargeUSD         *float64 `json:"accountChargeUsd,omitempty"`
	Multiplier               float64  `json:"multiplier"`
	ServiceTierPricingSource string   `json:"serviceTierPricingSource"`
	ServiceTierMultiplier    *float64 `json:"serviceTierMultiplier,omitempty"`
}

type Summary struct {
	ID                        string          `json:"id"`
	SystemAccountID           string          `json:"systemAccountId,omitempty"`
	SystemAccountName         string          `json:"systemAccountName,omitempty"`
	TraceID                   string          `json:"traceId"`
	TrafficSource             string          `json:"trafficSource"`
	ClientIP                  string          `json:"clientIp,omitempty"`
	APIKeyID                  string          `json:"apiKeyId,omitempty"`
	APIKeyName                string          `json:"apiKeyName,omitempty"`
	GroupID                   string          `json:"groupId,omitempty"`
	GroupName                 string          `json:"groupName,omitempty"`
	AccountID                 string          `json:"accountId,omitempty"`
	AccountName               string          `json:"accountName,omitempty"`
	Endpoint                  string          `json:"endpoint,omitempty"`
	ProviderCode              string          `json:"providerCode,omitempty"`
	ProviderProtocolProfileID string          `json:"providerProtocolProfileId,omitempty"`
	UsageSemantic             string          `json:"usageSemantic,omitempty"`
	Model                     string          `json:"model,omitempty"`
	UpstreamModel             string          `json:"upstreamModel,omitempty"`
	PricingModel              string          `json:"pricingModel,omitempty"`
	RequestedServiceTier      string          `json:"requestedServiceTier,omitempty"`
	EffectiveServiceTier      string          `json:"effectiveServiceTier,omitempty"`
	ReportedServiceTier       string          `json:"reportedServiceTier,omitempty"`
	BilledServiceTier         string          `json:"billedServiceTier,omitempty"`
	RequestedReasoningEffort  string          `json:"requestedReasoningEffort,omitempty"`
	EffectiveReasoningEffort  string          `json:"effectiveReasoningEffort,omitempty"`
	ModelMappingApplied       bool            `json:"modelMappingApplied"`
	ModelMappingSource        string          `json:"modelMappingSource,omitempty"`
	SourceEndpointFamily      string          `json:"sourceEndpointFamily,omitempty"`
	UpstreamEndpointFamily    string          `json:"upstreamEndpointFamily,omitempty"`
	Stream                    bool            `json:"stream"`
	StatusCode                *int            `json:"statusCode,omitempty"`
	Success                   bool            `json:"success"`
	FailureAttribution        string          `json:"failureAttribution,omitempty"`
	FirstTokenMs              *int64          `json:"firstTokenMs,omitempty"`
	DurationMs                *int64          `json:"durationMs,omitempty"`
	InputTokens               *int64          `json:"inputTokens,omitempty"`
	OutputTokens              *int64          `json:"outputTokens,omitempty"`
	CacheReadTokens           *int64          `json:"cacheReadTokens,omitempty"`
	CacheReadCostUSD          *float64        `json:"cacheReadCostUsd,omitempty"`
	CacheWriteTokens          *int64          `json:"cacheWriteTokens,omitempty"`
	CacheWrite1hTokens        *int64          `json:"cacheWrite1hTokens,omitempty"`
	CacheWriteCostUSD         *float64        `json:"cacheWriteCostUsd,omitempty"`
	ThinkingTokens            *int64          `json:"thinkingTokens,omitempty"`
	InputImageTokens          *int64          `json:"inputImageTokens,omitempty"`
	OutputImageTokens         *int64          `json:"outputImageTokens,omitempty"`
	InputAudioTokens          *int64          `json:"inputAudioTokens,omitempty"`
	OutputAudioTokens         *int64          `json:"outputAudioTokens,omitempty"`
	OutputImageCount          *int64          `json:"outputImageCount,omitempty"`
	CostUSD                   *float64        `json:"costUsd,omitempty"`
	CostBreakdown             *CostBreakdown  `json:"costBreakdown,omitempty"`
	ErrorCode                 string          `json:"errorCode,omitempty"`
	ErrorMessage              string          `json:"errorMessage,omitempty"`
	RequestSnapshot           *map[string]any `json:"requestSnapshot,omitempty"`
	ResponseSnapshot          *map[string]any `json:"responseSnapshot,omitempty"`
	CreatedAt                 string          `json:"createdAt"`
}

func NewService(store Store) *Service {
	return NewServiceWithOptions(ServiceOptions{Store: store})
}

func NewServiceWithOptions(opts ServiceOptions) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{store: opts.Store, now: now}
}

func (s *Service) List(ctx context.Context, input ListInput) (ListResult, error) {
	if s.store == nil {
		return ListResult{}, fmt.Errorf("management usage record store is required")
	}
	pageSize := normalizedPageSize(input.PageSize, input.PageSizeProvided)
	page := normalizedPage(input.Page, pageSize)
	startAt, endAt, err := s.dateRange(ctx, input.StartDate, input.EndDate)
	if err != nil {
		return ListResult{}, err
	}
	result, err := s.store.ListManagementUsageRecords(ctx, port.ManagementUsageRecordListInput{
		SystemAccountID: strings.TrimSpace(input.ScopeSystemAccountID),
		TraceID:         strings.TrimSpace(input.TraceID),
		AccountKeyword:  strings.TrimSpace(input.AccountKeyword),
		ClientIP:        strings.TrimSpace(input.ClientIP),
		Result:          normalizedResult(input.Result),
		StatusCode:      normalizedStatusCode(input.StatusCode),
		GroupID:         strings.TrimSpace(input.GroupID),
		Model:           strings.TrimSpace(input.Model),
		TrafficSource:   normalizedTrafficSource(input.TrafficSource),
		StartAt:         startAt,
		EndAt:           endAt,
		SortAscending:   input.SortOrder == "asc",
		Limit:           pageSize,
		Offset:          (page - 1) * pageSize,
	})
	if err != nil {
		return ListResult{}, err
	}
	items := make([]Summary, 0, len(result.Items))
	for _, row := range result.Items {
		items = append(items, summaryFromStore(row, input.IncludeSystemAccount))
	}
	return ListResult{
		Items:    items,
		Total:    (page-1)*pageSize + len(items) + boolInt(result.HasMore),
		HasMore:  result.HasMore,
		Page:     page,
		PageSize: pageSize,
	}, nil
}

func (s *Service) Detail(ctx context.Context, input DetailInput) (Summary, bool, error) {
	if s.store == nil {
		return Summary{}, false, fmt.Errorf("management usage record store is required")
	}
	detail, found, err := s.store.GetManagementUsageRecord(ctx, port.ManagementUsageRecordDetailInput{
		ID: strings.TrimSpace(input.ID), SystemAccountID: strings.TrimSpace(input.ScopeSystemAccountID),
	})
	if err != nil || !found {
		return Summary{}, found, err
	}
	result := summaryFromStore(detail.ManagementUsageRecordSummary, input.IncludeSystemAccount)
	requestSnapshot, err := parseOptionalObject(detail.RequestSnapshotJSON)
	if err != nil {
		return Summary{}, false, fmt.Errorf("parse usage record request snapshot: %w", err)
	}
	responseSnapshot, err := parseOptionalObject(detail.ResponseSnapshotJSON)
	if err != nil {
		return Summary{}, false, fmt.Errorf("parse usage record response snapshot: %w", err)
	}
	result.RequestSnapshot = requestSnapshot
	result.ResponseSnapshot = responseSnapshot
	if result.Endpoint == "" && requestSnapshot != nil {
		result.Endpoint = endpointFromSnapshot(*requestSnapshot)
	}
	return result, true, nil
}

func (s *Service) dateRange(ctx context.Context, startText, endText string) (time.Time, time.Time, error) {
	timezone, found, err := s.store.GetManagementUsageStatsTimezone(ctx)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if !found || strings.TrimSpace(timezone) == "" {
		return time.Time{}, time.Time{}, fmt.Errorf("系统设置缺少 usageStatsTimezone")
	}
	location, err := time.LoadLocation(strings.TrimSpace(timezone))
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("系统设置 usageStatsTimezone 无效: %w", err)
	}
	startDate, startOK := parseDate(startText)
	endDate, endOK := parseDate(endText)
	if !startOK && !endOK {
		if strings.TrimSpace(startText) != "" || strings.TrimSpace(endText) != "" {
			return time.Time{}, time.Time{}, nil
		}
		now := s.now().In(location)
		startDate = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
		return startDate.UTC(), startDate.AddDate(0, 0, 1).UTC(), nil
	}
	if !startOK {
		startDate = endDate
	}
	if !endOK {
		endDate = startDate
	}
	if startDate.After(endDate) {
		startDate, endDate = endDate, startDate
	}
	start := time.Date(startDate.Year(), startDate.Month(), startDate.Day(), 0, 0, 0, 0, location)
	end := time.Date(endDate.Year(), endDate.Month(), endDate.Day(), 0, 0, 0, 0, location).AddDate(0, 0, 1)
	return start.UTC(), end.UTC(), nil
}

func parseDate(value string) (time.Time, bool) {
	parsed, err := time.Parse(time.DateOnly, strings.TrimSpace(value))
	return parsed, err == nil
}

func summaryFromStore(row port.ManagementUsageRecordSummary, includeSystemAccount bool) Summary {
	result := Summary{
		ID: row.ID, TraceID: row.TraceID, TrafficSource: row.TrafficSource,
		ClientIP: text(row.ClientIP), APIKeyID: text(row.APIKeyID), APIKeyName: text(row.APIKeyName),
		GroupID: text(row.GroupID), GroupName: text(row.GroupName), AccountID: text(row.AccountID), AccountName: text(row.AccountName),
		Endpoint: text(row.Endpoint), ProviderCode: text(row.ProviderCode), ProviderProtocolProfileID: text(row.ProviderProtocolProfileID), UsageSemantic: text(row.UsageSemantic),
		Model: text(row.Model), UpstreamModel: text(row.UpstreamModel), PricingModel: text(row.PricingModel),
		RequestedServiceTier: text(row.RequestedServiceTier), EffectiveServiceTier: text(row.EffectiveServiceTier), ReportedServiceTier: text(row.ReportedServiceTier), BilledServiceTier: text(row.BilledServiceTier),
		RequestedReasoningEffort: text(row.RequestedReasoningEffort), EffectiveReasoningEffort: text(row.EffectiveReasoningEffort),
		ModelMappingApplied: row.ModelMappingApplied, ModelMappingSource: text(row.ModelMappingSource), SourceEndpointFamily: text(row.SourceEndpointFamily), UpstreamEndpointFamily: text(row.UpstreamEndpointFamily),
		Stream: row.Stream, StatusCode: row.StatusCode, Success: row.Success, FailureAttribution: text(row.FailureAttribution), FirstTokenMs: row.FirstTokenMs, DurationMs: row.DurationMs,
		InputTokens: row.InputTokens, OutputTokens: row.OutputTokens, CacheReadTokens: row.CacheReadTokens, CacheReadCostUSD: row.CacheReadCostUSD,
		CacheWriteTokens: row.CacheWriteTokens, CacheWrite1hTokens: row.CacheWrite1hTokens, CacheWriteCostUSD: row.CacheWriteCostUSD, ThinkingTokens: row.ThinkingTokens,
		InputImageTokens: row.InputImageTokens, OutputImageTokens: row.OutputImageTokens, InputAudioTokens: row.InputAudioTokens, OutputAudioTokens: row.OutputAudioTokens, OutputImageCount: row.OutputImageCount,
		CostUSD: row.CostUSD, ErrorCode: text(row.ErrorCode), ErrorMessage: text(row.ErrorMessage), CreatedAt: formatTime(row.CreatedAt),
	}
	if includeSystemAccount {
		result.SystemAccountID = text(row.SystemAccountID)
		result.SystemAccountName = text(row.SystemAccountName)
	}
	if row.Success {
		result.CostBreakdown = costBreakdown(row)
	}
	return result
}

func costBreakdown(row port.ManagementUsageRecordSummary) *CostBreakdown {
	if row.CostBreakdownSnapshotJSON != nil {
		var result CostBreakdown
		if json.Unmarshal([]byte(*row.CostBreakdownSnapshotJSON), &result) == nil {
			return &result
		}
	}
	return &CostBreakdown{
		CacheReadCostUSD: row.CacheReadCostUSD, CacheWriteCostUSD: row.CacheWriteCostUSD,
		ThinkingTokens: row.ThinkingTokens, AccountChargeUSD: row.CostUSD,
		Multiplier: 1, ServiceTierPricingSource: "unknown",
	}
}

func parseOptionalObject(raw string) (*map[string]any, error) {
	if strings.TrimFunc(raw, usageRecordECMAScriptWhitespace) == "" {
		return nil, nil
	}
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("JSON 对象字段必须是对象")
	}
	return &object, nil
}

func usageRecordECMAScriptWhitespace(character rune) bool {
	switch character {
	case '\u0009', '\u000B', '\u000C', '\u0020', '\u00A0', '\u1680',
		'\u2000', '\u2001', '\u2002', '\u2003', '\u2004', '\u2005',
		'\u2006', '\u2007', '\u2008', '\u2009', '\u200A', '\u202F',
		'\u205F', '\u3000', '\uFEFF', '\u000A', '\u000D', '\u2028',
		'\u2029':
		return true
	default:
		return false
	}
}

func endpointFromSnapshot(snapshot map[string]any) string {
	if snapshot == nil {
		return ""
	}
	method, hasMethod := snapshot["method"].(string)
	if hasMethod {
		method = strings.ToUpper(method)
	} else {
		method = "GET"
	}
	originalURL, hasOriginalURL := snapshot["originalUrl"].(string)
	endpoint := ""
	if hasOriginalURL {
		endpoint = strings.SplitN(originalURL, "?", 2)[0]
	} else if path, ok := snapshot["path"].(string); ok {
		endpoint = path
	}
	if endpoint == "" {
		return ""
	}
	return method + " " + endpoint
}

func normalizedPageSize(value int, provided bool) int {
	if !provided {
		return defaultPageSize
	}
	return min(max(value, 1), maxPageSize)
}

func normalizedPage(value, pageSize int) int {
	return min(max(value, 1), max(1, (maxListWindowRows-1)/pageSize))
}

func normalizedResult(value string) string {
	switch strings.TrimSpace(value) {
	case "success", "failed":
		return strings.TrimSpace(value)
	default:
		return "all"
	}
}

func normalizedStatusCode(value int) *int {
	if value < 100 || value > 599 {
		return nil
	}
	return &value
}

func normalizedTrafficSource(value string) string {
	value = strings.TrimSpace(value)
	switch value {
	case "gateway", "manual_account_test", "account_health_check", "runtime_recovery_probe", "cooldown_retest", "hybrid_scoring", "hybrid_quality_scoring":
		return value
	default:
		return ""
	}
}

func text(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Truncate(time.Millisecond).Format(jsTimeLayout)
}
