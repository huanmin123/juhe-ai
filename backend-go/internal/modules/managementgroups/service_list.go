package managementgroups

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/timezonecompat"
)

const (
	defaultListPageSize   = 50
	maxListPageSize       = 500
	maxListWindowRowCount = 1000

	maxRequestQuotaHourlyWindowHours = 24 * 30
	maxRequestQuotaAmountUSD         = 9007199254740991
	requestQuotaAmountPrecision      = 1000000
)

var ErrGroupListInvalid = errors.New("management group list invalid")

type ListInput struct {
	ActorSystemAccountID string
	ActorRole            string
	SystemAccountID      string
	SelfOnly             bool
	Page                 int
	PageSize             int
	PageSizeProvided     bool
}

type AuthorizationSourceSummary struct {
	ActiveSourceCount int      `json:"activeSourceCount"`
	HasManual         bool     `json:"hasManual"`
	HasTeam           bool     `json:"hasTeam"`
	TeamNames         []string `json:"teamNames"`
}

type ListItem struct {
	ID                         string                      `json:"id"`
	SystemAccountID            string                      `json:"systemAccountId,omitempty"`
	SystemAccountName          string                      `json:"systemAccountName,omitempty"`
	OwnerSystemAccountID       string                      `json:"ownerSystemAccountId"`
	OwnerSystemAccountName     string                      `json:"ownerSystemAccountName,omitempty"`
	Name                       string                      `json:"name"`
	ProviderCode               string                      `json:"providerCode"`
	Description                *string                     `json:"description,omitempty"`
	Enabled                    bool                        `json:"enabled"`
	IsDefault                  bool                        `json:"isDefault"`
	GroupType                  string                      `json:"groupType"`
	AccountStats               ListAccountStats            `json:"accountStats"`
	AccessType                 string                      `json:"accessType"`
	GroupAuthorizationID       string                      `json:"groupAuthorizationId,omitempty"`
	AuthorizationStatus        string                      `json:"authorizationStatus,omitempty"`
	AuthorizationExpiresAt     *time.Time                  `json:"authorizationExpiresAt,omitempty"`
	CanEdit                    bool                        `json:"canEdit"`
	CanDelete                  bool                        `json:"canDelete"`
	CanReturn                  bool                        `json:"canReturn"`
	AuthorizationSourceSummary *AuthorizationSourceSummary `json:"authorizationSourceSummary,omitempty"`
}

type ListAccountStats struct {
	Total            int `json:"total"`
	Available        int `json:"available"`
	Active           int `json:"active"`
	Disabled         int `json:"disabled"`
	Error            int `json:"error"`
	RateLimited      int `json:"rateLimited"`
	ConcurrencyLimit int `json:"concurrencyLimit"`
}

type ListResult struct {
	Items    []ListItem `json:"items"`
	Total    int        `json:"total"`
	HasMore  bool       `json:"hasMore"`
	Page     int        `json:"page"`
	PageSize int        `json:"pageSize"`
}

func (s *Service) List(ctx context.Context, input ListInput) (ListResult, error) {
	if s.listStore == nil {
		return ListResult{}, fmt.Errorf("management group list reader is required")
	}
	systemAccountID, includeSystemAccountFields, err := managementGroupListScope(input)
	if err != nil {
		return ListResult{}, err
	}
	pageSize := managementGroupListPageSize(input.PageSize, input.PageSizeProvided || input.PageSize != 0)
	page := managementGroupListPage(input.Page, pageSize)
	result, err := s.listStore.ListManagementGroups(ctx, port.ManagementGroupListInput{
		SystemAccountID: systemAccountID,
		Limit:           pageSize + 1,
		Offset:          (page - 1) * pageSize,
	})
	if err != nil {
		return ListResult{}, err
	}
	rows := result.Rows
	hasMore := result.HasMore || len(rows) > pageSize
	if len(rows) > pageSize {
		rows = rows[:pageSize]
	}
	if len(rows) == 0 {
		return managementGroupListResult(nil, page, pageSize, hasMore), nil
	}

	enrichment, err := s.loadManagementGroupListEnrichment(ctx, rows)
	if err != nil {
		return ListResult{}, err
	}
	items := make([]ListItem, 0, len(rows))
	for _, row := range rows {
		item, err := managementGroupListItem(
			row,
			includeSystemAccountFields,
			enrichment,
		)
		if err != nil {
			return ListResult{}, err
		}
		items = append(items, item)
	}
	return managementGroupListResult(items, page, pageSize, hasMore), nil
}

type managementGroupListEnrichment struct {
	accountStatsByGroup map[string]port.ManagementGroupAccountStatsRow
	sourceSummaryByAuth map[string]AuthorizationSourceSummary
}

func (s *Service) loadManagementGroupListEnrichment(
	ctx context.Context,
	rows []port.ManagementGroupListRow,
) (managementGroupListEnrichment, error) {
	groupIDs := make([]string, 0, len(rows))
	authorizationIDs := make([]string, 0, len(rows))
	enrichment := managementGroupListEnrichment{
		accountStatsByGroup: make(map[string]port.ManagementGroupAccountStatsRow, len(rows)),
		sourceSummaryByAuth: make(map[string]AuthorizationSourceSummary),
	}
	for _, row := range rows {
		groupIDs = append(groupIDs, row.ID)
		accessType := groupAccessType(row.AccessType)
		if accessType == "authorized" {
			authorizationID := strings.TrimSpace(row.GroupAuthorizationID)
			if authorizationID != "" {
				authorizationIDs = append(authorizationIDs, authorizationID)
			}
			continue
		}
	}

	statsRows, err := s.listStore.ListManagementGroupAccountStats(ctx, groupIDs)
	if err != nil {
		return managementGroupListEnrichment{}, err
	}
	for _, row := range statsRows {
		enrichment.accountStatsByGroup[managementGroupStatsKey(row.SystemAccountID, row.GroupID)] = row
	}

	if len(authorizationIDs) == 0 {
		return enrichment, nil
	}
	sourceRows, err := s.listStore.ListManagementGroupAuthorizationSources(ctx, authorizationIDs)
	if err != nil {
		return managementGroupListEnrichment{}, err
	}
	enrichment.sourceSummaryByAuth = summarizeManagementGroupAuthorizationSources(sourceRows)
	return enrichment, nil
}

func (s *Service) managementGroupListStatDate(ctx context.Context, now time.Time) (string, error) {
	if s.usageStatsTimezoneStore == nil {
		return "", fmt.Errorf("management usage stats timezone reader is required")
	}
	timezone, found, err := s.usageStatsTimezoneStore.GetManagementUsageStatsTimezone(ctx)
	if err != nil {
		return "", err
	}
	timezone = strings.TrimSpace(timezone)
	if !found || timezone == "" {
		return "", fmt.Errorf("系统设置缺少 usageStatsTimezone")
	}
	location, err := timezonecompat.LoadNodeLocation(timezone)
	if err != nil {
		return "", fmt.Errorf("系统设置 usageStatsTimezone 无效: %w", err)
	}
	return now.In(location).Format("2006-01-02"), nil
}

func managementGroupListScope(input ListInput) (string, bool, error) {
	actorSystemAccountID := strings.TrimSpace(input.ActorSystemAccountID)
	if actorSystemAccountID == "" {
		return "", false, ErrGroupListInvalid
	}
	if input.SelfOnly || !managementGroupListAdminRole(input.ActorRole) {
		return actorSystemAccountID, false, nil
	}
	systemAccountID := strings.TrimSpace(input.SystemAccountID)
	if systemAccountID == "all" {
		systemAccountID = ""
	}
	return systemAccountID, true, nil
}

func managementGroupListAdminRole(role string) bool {
	role = strings.TrimSpace(role)
	return role == "admin" || role == "super_admin"
}

func managementGroupListPageSize(value int, provided bool) int {
	if !provided {
		return defaultListPageSize
	}
	return min(max(value, 1), maxListPageSize)
}

func managementGroupListPage(value int, pageSize int) int {
	if value <= 0 {
		return 1
	}
	maxPage := max(1, maxListWindowRowCount/max(1, pageSize))
	return min(value, maxPage)
}

func managementGroupListResult(items []ListItem, page int, pageSize int, hasMore bool) ListResult {
	if items == nil {
		items = []ListItem{}
	}
	total := (page-1)*pageSize + len(items)
	if hasMore {
		total++
	}
	return ListResult{
		Items:    items,
		Total:    total,
		HasMore:  hasMore,
		Page:     page,
		PageSize: pageSize,
	}
}

func managementGroupListItem(
	row port.ManagementGroupListRow,
	includeSystemAccountFields bool,
	enrichment managementGroupListEnrichment,
) (ListItem, error) {
	accessType := groupAccessType(row.AccessType)
	groupType, err := normalizeGroupType(row.GroupType)
	if err != nil {
		return ListItem{}, fmt.Errorf("normalize management group %q type: %w", row.ID, err)
	}
	statsRow := enrichment.accountStatsByGroup[managementGroupStatsKey(row.SystemAccountID, row.ID)]
	permissions := ownerPermissions()
	isDefault := row.IsDefault
	var sourceSummary *AuthorizationSourceSummary
	if accessType == "authorized" {
		summary := enrichment.sourceSummaryByAuth[row.GroupAuthorizationID]
		if summary.TeamNames == nil {
			summary.TeamNames = []string{}
		}
		sourceSummary = &summary
		permissions = authorizedGroupPermissions(
			false,
			false,
		)
		isDefault = false
	}
	item := ListItem{
		ID:                     row.ID,
		OwnerSystemAccountID:   row.SystemAccountID,
		OwnerSystemAccountName: row.SystemAccountName,
		Name:                   row.Name,
		ProviderCode:           row.ProviderCode,
		Description:            row.Description,
		Enabled:                row.Enabled,
		IsDefault:              isDefault,
		GroupType:              groupType,
		AccountStats: ListAccountStats{
			Total:            statsRow.Total,
			Available:        statsRow.Available,
			Active:           statsRow.Active,
			Disabled:         statsRow.Disabled,
			Error:            statsRow.Error,
			RateLimited:      statsRow.RateLimited,
			ConcurrencyLimit: statsRow.ConcurrencyLimit,
		},
		AccessType:                 accessType,
		GroupAuthorizationID:       row.GroupAuthorizationID,
		AuthorizationStatus:        row.AuthorizationStatus,
		AuthorizationExpiresAt:     row.AuthorizationExpiresAt,
		CanEdit:                    !isDefault && permissions.CanEdit,
		CanDelete:                  !isDefault && permissions.CanDelete,
		CanReturn:                  accessType == "authorized" && permissions.CanReturnAuthorization,
		AuthorizationSourceSummary: sourceSummary,
	}
	if includeSystemAccountFields {
		item.SystemAccountID = row.SystemAccountID
		item.SystemAccountName = row.SystemAccountName
	}
	if accessType != "authorized" {
		item.GroupAuthorizationID = ""
		item.AuthorizationStatus = ""
		item.AuthorizationExpiresAt = nil
	}
	return item, nil
}

func managementGroupAccountStats(
	row port.ManagementGroupAccountStatsRow,
	todayUsage port.ManagementAccountUsageSummary,
	totalUsage port.ManagementAccountUsageSummary,
) GroupAccountStats {
	return GroupAccountStats{
		Total:              row.Total,
		Available:          row.Available,
		Active:             row.Active,
		Disabled:           row.Disabled,
		Error:              row.Error,
		RateLimited:        row.RateLimited,
		CurrentConcurrency: row.CurrentConcurrency,
		ConcurrencyLimit:   row.ConcurrencyLimit,
		TodayUsage:         managementGroupUsageSummary(todayUsage),
		Usage:              managementGroupUsageSummary(totalUsage),
	}
}

func managementGroupUsageSummary(value port.ManagementAccountUsageSummary) UsageSummary {
	return UsageSummary{
		RequestCount:       value.RequestCount,
		InputTokens:        value.InputTokens,
		OutputTokens:       value.OutputTokens,
		CacheReadTokens:    value.CacheReadTokens,
		CacheReadCost:      value.CacheReadCost,
		CacheWriteTokens:   value.CacheWriteTokens,
		CacheWrite1hTokens: value.CacheWrite1hTokens,
		CacheWriteCost:     value.CacheWriteCost,
		ThinkingTokens:     value.ThinkingTokens,
		InputImageTokens:   value.InputImageTokens,
		OutputImageTokens:  value.OutputImageTokens,
		TotalTokens:        value.InputTokens + value.OutputTokens,
		TotalCost:          value.TotalCost,
		LastUsedAt:         value.LastUsedAt,
	}
}

func managementGroupStatsKey(systemAccountID string, groupID string) string {
	return strings.TrimSpace(systemAccountID) + "\x00" + strings.TrimSpace(groupID)
}

func summarizeManagementGroupAuthorizationSources(
	rows []port.ManagementGroupAuthorizationSourceRow,
) map[string]AuthorizationSourceSummary {
	result := make(map[string]AuthorizationSourceSummary)
	teamNames := make(map[string]map[string]struct{})
	for _, row := range rows {
		authorizationID := strings.TrimSpace(row.AuthorizationID)
		if authorizationID == "" {
			continue
		}
		summary := result[authorizationID]
		if summary.TeamNames == nil {
			summary.TeamNames = []string{}
		}
		sourceType := strings.TrimSpace(row.SourceType)
		status := strings.TrimSpace(row.Status)
		if sourceType == "team" {
			summary.HasTeam = true
		}
		if status == "active" {
			summary.ActiveSourceCount++
			if sourceType == "manual" {
				summary.HasManual = true
			}
			if sourceType == "team" {
				teamName := strings.TrimSpace(row.SourceTeamName)
				if teamName != "" {
					if teamNames[authorizationID] == nil {
						teamNames[authorizationID] = make(map[string]struct{})
					}
					if _, exists := teamNames[authorizationID][teamName]; !exists {
						teamNames[authorizationID][teamName] = struct{}{}
						summary.TeamNames = append(summary.TeamNames, teamName)
					}
				}
			}
		}
		result[authorizationID] = summary
	}
	return result
}

func parseManagementGroupAuthorizationLimits(value *string) (port.ManagementRequestQuotaLimits, error) {
	if value == nil || strings.TrimSpace(*value) == "" {
		return port.ManagementRequestQuotaLimits{}, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(*value), &raw); err != nil {
		return port.ManagementRequestQuotaLimits{}, err
	}
	if raw == nil {
		return port.ManagementRequestQuotaLimits{}, nil
	}
	limits := port.ManagementRequestQuotaLimits{}
	for key, encoded := range raw {
		switch key {
		case "hourly":
			limit, err := parseManagementGroupHourlyQuotaLimit(encoded)
			if err != nil {
				return port.ManagementRequestQuotaLimits{}, err
			}
			limits.Hourly = limit
		case "daily":
			limit, err := parseManagementGroupQuotaLimit(encoded, "日额度")
			if err != nil {
				return port.ManagementRequestQuotaLimits{}, err
			}
			limits.Daily = limit
		case "weekly":
			limit, err := parseManagementGroupQuotaLimit(encoded, "周额度")
			if err != nil {
				return port.ManagementRequestQuotaLimits{}, err
			}
			limits.Weekly = limit
		case "monthly":
			limit, err := parseManagementGroupQuotaLimit(encoded, "月额度")
			if err != nil {
				return port.ManagementRequestQuotaLimits{}, err
			}
			limits.Monthly = limit
		case "total":
			limit, err := parseManagementGroupQuotaLimit(encoded, "总额度")
			if err != nil {
				return port.ManagementRequestQuotaLimits{}, err
			}
			limits.Total = limit
		default:
			return port.ManagementRequestQuotaLimits{}, fmt.Errorf("请求额度限制包含不支持字段: %s", key)
		}
	}
	return limits, nil
}

func parseManagementGroupQuotaLimit(
	value json.RawMessage,
	label string,
) (*port.ManagementRequestQuotaLimit, error) {
	if isManagementGroupJSONNull(value) {
		return nil, fmt.Errorf("%s参数无效", label)
	}
	var limit port.ManagementRequestQuotaLimit
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&limit); err != nil {
		return nil, fmt.Errorf("%s参数无效: %w", label, err)
	}
	if !limit.Enabled {
		return nil, fmt.Errorf("%s启用状态必须为 true", label)
	}
	if err := validateManagementGroupQuotaAmount(limit.Limit, label); err != nil {
		return nil, err
	}
	return &limit, nil
}

func parseManagementGroupHourlyQuotaLimit(
	value json.RawMessage,
) (*port.ManagementRequestHourlyQuotaLimit, error) {
	if isManagementGroupJSONNull(value) {
		return nil, fmt.Errorf("小时额度参数无效")
	}
	var limit port.ManagementRequestHourlyQuotaLimit
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&limit); err != nil {
		return nil, fmt.Errorf("小时额度参数无效: %w", err)
	}
	if !limit.Enabled {
		return nil, fmt.Errorf("小时额度启用状态必须为 true")
	}
	if limit.Hours < 1 || limit.Hours > maxRequestQuotaHourlyWindowHours {
		return nil, fmt.Errorf(
			"小时额度窗口必须在 1-%d 之间",
			maxRequestQuotaHourlyWindowHours,
		)
	}
	if err := validateManagementGroupQuotaAmount(limit.Limit, "小时额度"); err != nil {
		return nil, err
	}
	return &limit, nil
}

func validateManagementGroupQuotaAmount(value float64, label string) error {
	if math.IsNaN(value) ||
		math.IsInf(value, 0) ||
		value <= 0 ||
		value > maxRequestQuotaAmountUSD {
		return fmt.Errorf("%s金额必须是大于 0 的数字", label)
	}
	scaled := value * requestQuotaAmountPrecision
	if math.Round(scaled) != scaled {
		return fmt.Errorf("%s金额最多支持 6 位小数", label)
	}
	return nil
}

func parseManagementGroupListSchedulingPolicy(value *string, groupType string) (*SchedulingPolicy, error) {
	if groupType != "high_concurrency" {
		return nil, nil
	}
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil, fmt.Errorf("高并发分组调度策略缺失")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(*value), &raw); err != nil || raw == nil {
		return nil, fmt.Errorf("分组调度策略无效")
	}
	requiredKeys := []string{
		"mode",
		"defaultSoftConcurrency",
		"fastFirstEnabled",
		"fallbackOnQueueEnabled",
		"breakAffinityOnSoftLimit",
		"breakAffinityOnQueueWaitMs",
		"slowRequestThresholdMs",
		"firstOutputSlowThresholdMs",
		"recentTimeoutWindowSeconds",
		"recentTimeoutPenaltyThreshold",
		"maxQueueWaitMs",
		"maxQueueSize",
		"perApiKeyQueueLimit",
		"clientIpConcurrencyLimit",
		"clientIpConcurrencyOverflowMode",
		"imageLaneMaxConcurrency",
	}
	if len(raw) != len(requiredKeys) {
		return nil, fmt.Errorf("分组调度策略字段无效")
	}
	for _, key := range requiredKeys {
		value, exists := raw[key]
		if !exists {
			return nil, fmt.Errorf("分组调度策略缺少字段 %s", key)
		}
		if isManagementGroupJSONNull(value) {
			return nil, fmt.Errorf("分组调度策略字段 %s 不能为空", key)
		}
	}
	var policy SchedulingPolicy
	if err := json.Unmarshal([]byte(*value), &policy); err != nil {
		return nil, err
	}
	if policy.Mode != "balanced_fast" ||
		validatePolicyInteger("defaultSoftConcurrency", policy.DefaultSoftConcurrency, 1, 1000000) != nil ||
		validatePolicyInteger("breakAffinityOnQueueWaitMs", policy.BreakAffinityOnQueueWaitMs, 0, 1000000) != nil ||
		validatePolicyInteger("slowRequestThresholdMs", policy.SlowRequestThresholdMs, 1, 1000000) != nil ||
		validatePolicyInteger("firstOutputSlowThresholdMs", policy.FirstOutputSlowThresholdMs, 1, 1000000) != nil ||
		validatePolicyInteger("recentTimeoutWindowSeconds", policy.RecentTimeoutWindowSeconds, 1, 1000000) != nil ||
		validatePolicyInteger("recentTimeoutPenaltyThreshold", policy.RecentTimeoutPenaltyThreshold, 1, 1000000) != nil ||
		validatePolicyInteger("maxQueueWaitMs", policy.MaxQueueWaitMs, 1, 3600000) != nil ||
		validatePolicyInteger("maxQueueSize", policy.MaxQueueSize, 1, 1000000) != nil ||
		validatePolicyInteger("perApiKeyQueueLimit", policy.PerAPIKeyQueueLimit, 1, policy.MaxQueueSize) != nil ||
		validatePolicyInteger("clientIpConcurrencyLimit", policy.ClientIPConcurrencyLimit, 0, 1000000) != nil ||
		validatePolicyInteger("imageLaneMaxConcurrency", policy.ImageLaneMaxConcurrency, 0, 1000000) != nil ||
		(policy.ClientIPConcurrencyOverflowMode != "reject" && policy.ClientIPConcurrencyOverflowMode != "queue") {
		return nil, fmt.Errorf("分组调度策略无效")
	}
	return &policy, nil
}

func isManagementGroupJSONNull(value json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(value), []byte("null"))
}

func canBindAuthorizedGroupAt(enabled bool, status string, expiresAt *time.Time, now time.Time) bool {
	if !enabled || status != "active" {
		return false
	}
	return expiresAt == nil || expiresAt.After(now)
}
