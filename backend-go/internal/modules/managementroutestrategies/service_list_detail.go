package managementroutestrategies

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	defaultListPageSize = 50
	maxListPageSize     = 200

	defaultSchedulingPreference          = "cost_first"
	schedulingPreferenceSpeedFirst       = "speed_first"
	defaultFirstByteThresholdMs          = 30000
	defaultSlowTriggerCount              = 3
	defaultSlowWindowSeconds             = 120
	defaultRecoverySuccessCount          = 3
	defaultProbeIntervalSeconds          = 30
	defaultDegradedTTLSeconds            = 300
	defaultMaxFirstByteRetriesPerRequest = 2

	defaultHybridScoringContextMode             = "full_request"
	defaultHybridQualityPreference              = "balanced"
	defaultHybridScoringTimeoutMs               = 15000
	defaultHybridScoringFallbackMaxLevel        = 5
	defaultHybridScoringCacheTTLSeconds         = 300
	defaultHybridAffinityTTLSeconds             = 900
	defaultHybridSwitchMinLevelDelta            = 2
	defaultHybridDowngradeConsecutiveLowCount   = 2
	defaultHybridQualityInspectionTriggerMode   = "risk_based"
	defaultHybridQualityInspectionMaxTrigger    = 6
	defaultHybridQualityInspectionMaxRetries    = 2
	defaultHybridQualityInspectionFailureAction = "repair_then_upgrade"
	defaultHybridQualityInspectionUnavailable   = "pass_through"
	maxHybridLevelRouteCount                    = 5
)

var (
	ErrRouteStrategyListInvalid = errors.New("management route strategy list invalid")
	ErrRouteStrategyNotFound    = errors.New("策略路由不存在")

	routeStrategyDecimalNumberPattern = regexp.MustCompile(
		`^[+-]?(?:(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?)$`,
	)
)

type ListInput struct {
	ActorSystemAccountID string
	ActorRole            string
	SystemAccountID      string
	SelfOnly             bool
	Page                 int
	PageSize             int
	PageSizeProvided     bool
	Keyword              string
	Mode                 string
	Status               string
}

type SpeedFirstConfig struct {
	FirstByteThresholdMs          int `json:"firstByteThresholdMs"`
	SlowTriggerCount              int `json:"slowTriggerCount"`
	SlowWindowSeconds             int `json:"slowWindowSeconds"`
	RecoverySuccessCount          int `json:"recoverySuccessCount"`
	ProbeIntervalSeconds          int `json:"probeIntervalSeconds"`
	DegradedTTLSeconds            int `json:"degradedTtlSeconds"`
	MaxFirstByteRetriesPerRequest int `json:"maxFirstByteRetriesPerRequest"`
}

type NormalRoutingConfig struct {
	SchedulingPreference string            `json:"schedulingPreference"`
	SpeedFirstConfig     *SpeedFirstConfig `json:"speedFirstConfig,omitempty"`
}

type GroupBindingSummary struct {
	ID           string `json:"id"`
	GroupID      string `json:"groupId"`
	GroupName    string `json:"groupName,omitempty"`
	ProviderCode string `json:"providerCode,omitempty"`
	Priority     int    `json:"priority"`
	Weight       int    `json:"weight"`
	Status       string `json:"status"`
	GroupEnabled bool   `json:"groupEnabled"`
}

type GroupBindingPreview struct {
	ID           string `json:"id"`
	GroupID      string `json:"groupId"`
	GroupName    string `json:"groupName,omitempty"`
	ProviderCode string `json:"providerCode,omitempty"`
	Status       string `json:"status"`
	GroupEnabled bool   `json:"groupEnabled"`
}

type ListItem struct {
	ID                  string                `json:"id"`
	SystemAccountID     string                `json:"systemAccountId,omitempty"`
	SystemAccountName   string                `json:"systemAccountName,omitempty"`
	Name                string                `json:"name"`
	Description         *string               `json:"description,omitempty"`
	Mode                string                `json:"mode"`
	Status              string                `json:"status"`
	IsDefault           bool                  `json:"isDefault"`
	NormalRoutingConfig *NormalRoutingConfig  `json:"normalRoutingConfig,omitempty"`
	GroupBindingPreview []GroupBindingPreview `json:"groupBindingPreview"`
	BindingCount        int64                 `json:"bindingCount"`
	APIKeyCount         int64                 `json:"apiKeyCount"`
	CreatedAt           string                `json:"createdAt"`
	UpdatedAt           string                `json:"updatedAt"`
}

type ListResult struct {
	Items    []ListItem `json:"items"`
	Total    int        `json:"total"`
	HasMore  bool       `json:"hasMore"`
	Page     int        `json:"page"`
	PageSize int        `json:"pageSize"`
}

type DetailInput struct {
	ActorSystemAccountID string
	ActorRole            string
	SystemAccountID      string
	SelfOnly             bool
	RouteStrategyID      string
}

type DetailResult struct {
	ID                  string                `json:"id"`
	SystemAccountID     string                `json:"systemAccountId,omitempty"`
	SystemAccountName   string                `json:"systemAccountName,omitempty"`
	Name                string                `json:"name"`
	Description         *string               `json:"description,omitempty"`
	Mode                string                `json:"mode"`
	Status              string                `json:"status"`
	IsDefault           bool                  `json:"isDefault"`
	NormalRoutingConfig *NormalRoutingConfig  `json:"normalRoutingConfig,omitempty"`
	HybridRoutingConfig map[string]any        `json:"hybridRoutingConfig,omitempty"`
	GroupBindings       []GroupBindingSummary `json:"groupBindings"`
	APIKeyCount         int64                 `json:"apiKeyCount"`
	CreatedAt           string                `json:"createdAt"`
	UpdatedAt           string                `json:"updatedAt"`
}

type routeStrategyRuntimeConfig struct {
	NormalRoutingConfig *NormalRoutingConfig
	HybridRoutingConfig map[string]any
}

func (s *Service) List(ctx context.Context, input ListInput) (ListResult, error) {
	if s.listReader == nil {
		return ListResult{}, fmt.Errorf("management route strategy list reader is required")
	}
	systemAccountID, includeOwner, err := routeStrategyReadScope(
		input.ActorSystemAccountID,
		input.ActorRole,
		input.SystemAccountID,
		input.SelfOnly,
	)
	if err != nil {
		return ListResult{}, err
	}
	page := max(input.Page, 1)
	pageSize := routeStrategyListPageSize(input.PageSize, input.PageSizeProvided)
	offset := routeStrategyListOffset(page, pageSize)
	storedPage, err := s.listReader.ListManagementRouteStrategies(ctx, port.ManagementRouteStrategyListInput{
		SystemAccountID: systemAccountID,
		Keyword:         strings.TrimSpace(input.Keyword),
		Mode:            routeStrategyListMode(input.Mode),
		Status:          routeStrategyListStatus(input.Status),
		Limit:           pageSize + 1,
		Offset:          offset,
	})
	if err != nil {
		return ListResult{}, err
	}
	rows := storedPage.Rows
	hasMore := storedPage.HasMore || len(rows) > pageSize
	if len(rows) > pageSize {
		rows = rows[:pageSize]
	}
	if len(rows) == 0 {
		return routeStrategyListResult(nil, page, pageSize, hasMore), nil
	}

	scopes := make([]port.ManagementRouteStrategyScope, 0, len(rows))
	for _, row := range rows {
		scopes = append(scopes, port.ManagementRouteStrategyScope{
			ID:              strings.TrimSpace(row.ID),
			SystemAccountID: strings.TrimSpace(row.SystemAccountID),
		})
	}
	enrichmentRows, err := s.listReader.ListManagementRouteStrategyListEnrichment(ctx, scopes)
	if err != nil {
		return ListResult{}, err
	}
	enrichmentByScope := make(map[port.ManagementRouteStrategyScope]port.ManagementRouteStrategyListEnrichment, len(enrichmentRows))
	for _, row := range enrichmentRows {
		enrichmentByScope[port.ManagementRouteStrategyScope{
			ID:              strings.TrimSpace(row.ID),
			SystemAccountID: strings.TrimSpace(row.SystemAccountID),
		}] = row
	}

	items := make([]ListItem, 0, len(rows))
	for _, row := range rows {
		config, err := parseRouteStrategyRuntimeConfig(row.ConfigJSON)
		if err != nil {
			return ListResult{}, fmt.Errorf("parse management route strategy %q config: %w", row.ID, err)
		}
		enrichment := enrichmentByScope[port.ManagementRouteStrategyScope{
			ID:              strings.TrimSpace(row.ID),
			SystemAccountID: strings.TrimSpace(row.SystemAccountID),
		}]
		items = append(items, routeStrategyListItem(row, enrichment, config, includeOwner))
	}
	return routeStrategyListResult(items, page, pageSize, hasMore), nil
}

func (s *Service) Detail(ctx context.Context, input DetailInput) (DetailResult, error) {
	if s.detailReader == nil {
		return DetailResult{}, fmt.Errorf("management route strategy detail reader is required")
	}
	systemAccountID, includeOwner, err := routeStrategyReadScope(
		input.ActorSystemAccountID,
		input.ActorRole,
		input.SystemAccountID,
		input.SelfOnly,
	)
	if err != nil {
		return DetailResult{}, err
	}
	row, found, err := s.detailReader.FindManagementRouteStrategyDetail(ctx, port.ManagementRouteStrategyDetailInput{
		RouteStrategyID: strings.TrimSpace(input.RouteStrategyID),
		SystemAccountID: systemAccountID,
	})
	if err != nil {
		return DetailResult{}, err
	}
	if !found {
		return DetailResult{}, ErrRouteStrategyNotFound
	}
	config, err := parseRouteStrategyRuntimeConfig(row.ConfigJSON)
	if err != nil {
		return DetailResult{}, fmt.Errorf("parse management route strategy %q config: %w", row.ID, err)
	}
	return routeStrategyDetailResult(row, config, includeOwner), nil
}

func routeStrategyReadScope(
	actorSystemAccountID string,
	actorRole string,
	systemAccountID string,
	selfOnly bool,
) (string, bool, error) {
	actorSystemAccountID = strings.TrimSpace(actorSystemAccountID)
	if actorSystemAccountID == "" {
		return "", false, ErrRouteStrategyListInvalid
	}
	if selfOnly || !routeStrategyAdminRole(actorRole) {
		return actorSystemAccountID, false, nil
	}
	if systemAccountID == "all" {
		systemAccountID = ""
	}
	return systemAccountID, true, nil
}

func routeStrategyAdminRole(role string) bool {
	switch strings.TrimSpace(role) {
	case "admin", "super_admin":
		return true
	default:
		return false
	}
}

func routeStrategyListPageSize(value int, provided bool) int {
	if !provided {
		return defaultListPageSize
	}
	return min(max(value, 1), maxListPageSize)
}

func routeStrategyListOffset(page int, pageSize int) int {
	maxInt := int(^uint(0) >> 1)
	pageIndex := page - 1
	if pageIndex > maxInt/max(1, pageSize) {
		return maxInt - pageSize
	}
	return pageIndex * pageSize
}

func routeStrategyListMode(value string) string {
	switch strings.TrimSpace(value) {
	case "normal", "hybrid_smart", "weighted", "failover", "round_robin":
		return strings.TrimSpace(value)
	default:
		return ""
	}
}

func routeStrategyListStatus(value string) string {
	switch strings.TrimSpace(value) {
	case "active", "disabled":
		return strings.TrimSpace(value)
	default:
		return ""
	}
}

func routeStrategyListResult(items []ListItem, page int, pageSize int, hasMore bool) ListResult {
	if items == nil {
		items = []ListItem{}
	}
	total := routeStrategyListOffset(page, pageSize) + len(items)
	if hasMore && total < int(^uint(0)>>1) {
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

func routeStrategyListItem(
	row port.ManagementRouteStrategyListRow,
	enrichment port.ManagementRouteStrategyListEnrichment,
	config routeStrategyRuntimeConfig,
	includeOwner bool,
) ListItem {
	previewLimit := min(len(enrichment.GroupBindingPreview), 3)
	preview := make([]GroupBindingPreview, 0, previewLimit)
	for _, binding := range enrichment.GroupBindingPreview[:previewLimit] {
		preview = append(preview, GroupBindingPreview{
			ID:           binding.ID,
			GroupID:      binding.GroupID,
			GroupName:    binding.GroupName,
			ProviderCode: binding.ProviderCode,
			Status:       binding.Status,
			GroupEnabled: binding.GroupEnabled,
		})
	}
	item := ListItem{
		ID:                  row.ID,
		Name:                row.Name,
		Description:         row.Description,
		Mode:                row.Mode,
		Status:              row.Status,
		IsDefault:           row.IsDefault,
		GroupBindingPreview: preview,
		BindingCount:        enrichment.BindingCount,
		APIKeyCount:         enrichment.APIKeyCount,
		CreatedAt:           row.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:           row.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if row.Mode == "normal" {
		item.NormalRoutingConfig = config.NormalRoutingConfig
		if item.NormalRoutingConfig == nil {
			item.NormalRoutingConfig = &NormalRoutingConfig{SchedulingPreference: defaultSchedulingPreference}
		}
	}
	if includeOwner {
		item.SystemAccountID = row.SystemAccountID
		item.SystemAccountName = row.SystemAccountName
	}
	return item
}

func routeStrategyDetailResult(
	row port.ManagementRouteStrategyDetailRow,
	config routeStrategyRuntimeConfig,
	includeOwner bool,
) DetailResult {
	bindings := make([]GroupBindingSummary, 0, len(row.GroupBindings))
	for _, binding := range row.GroupBindings {
		bindings = append(bindings, GroupBindingSummary{
			ID:           binding.ID,
			GroupID:      binding.GroupID,
			GroupName:    binding.GroupName,
			ProviderCode: binding.ProviderCode,
			Priority:     binding.Priority,
			Weight:       binding.Weight,
			Status:       binding.Status,
			GroupEnabled: binding.GroupEnabled,
		})
	}
	result := DetailResult{
		ID:            row.ID,
		Name:          row.Name,
		Description:   row.Description,
		Mode:          row.Mode,
		Status:        row.Status,
		IsDefault:     row.IsDefault,
		GroupBindings: bindings,
		APIKeyCount:   row.APIKeyCount,
		CreatedAt:     row.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:     row.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	switch row.Mode {
	case "normal":
		result.NormalRoutingConfig = config.NormalRoutingConfig
		if result.NormalRoutingConfig == nil {
			result.NormalRoutingConfig = &NormalRoutingConfig{SchedulingPreference: defaultSchedulingPreference}
		}
	case "hybrid_smart":
		result.HybridRoutingConfig = config.HybridRoutingConfig
	}
	if includeOwner {
		result.SystemAccountID = row.SystemAccountID
		result.SystemAccountName = row.SystemAccountName
	}
	return result
}

func parseRouteStrategyRuntimeConfig(raw *string) (routeStrategyRuntimeConfig, error) {
	if raw == nil || *raw == "" {
		return routeStrategyRuntimeConfig{}, nil
	}
	decoder := json.NewDecoder(bytes.NewBufferString(*raw))
	decoder.UseNumber()
	var parsed any
	if err := decoder.Decode(&parsed); err != nil {
		return routeStrategyRuntimeConfig{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return routeStrategyRuntimeConfig{}, fmt.Errorf("策略路由配置包含多余 JSON 值")
		}
		return routeStrategyRuntimeConfig{}, err
	}
	record, ok := parsed.(map[string]any)
	if !ok || record == nil {
		return routeStrategyRuntimeConfig{}, nil
	}
	var config routeStrategyRuntimeConfig
	if value, exists := record["normalRoutingConfig"]; exists && routeStrategyConfigValuePresent(value) {
		normal, err := normalizeManagementNormalRoutingConfig(value)
		if err != nil {
			return routeStrategyRuntimeConfig{}, err
		}
		config.NormalRoutingConfig = normal
	}
	if value, exists := record["hybridRoutingConfig"]; exists && routeStrategyConfigValuePresent(value) {
		hybrid, err := normalizeManagementHybridRoutingConfig(value)
		if err != nil {
			return routeStrategyRuntimeConfig{}, err
		}
		config.HybridRoutingConfig = hybrid
	}
	return config, nil
}

func routeStrategyConfigValuePresent(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return typed != ""
	case bool:
		return typed
	case json.Number:
		number, _ := typed.Float64()
		return number != 0 && !math.IsNaN(number)
	case float64:
		return typed != 0 && !math.IsNaN(typed)
	case int:
		return typed != 0
	default:
		return true
	}
}

func routeStrategyConfigValueMissing(value any) bool {
	if value == nil {
		return true
	}
	text, ok := value.(string)
	return ok && text == ""
}

func normalizeManagementNormalRoutingConfig(value any) (*NormalRoutingConfig, error) {
	if routeStrategyConfigValueMissing(value) {
		return &NormalRoutingConfig{SchedulingPreference: defaultSchedulingPreference}, nil
	}
	record, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("普通路由调度配置无效")
	}
	preference := defaultSchedulingPreference
	if raw, exists := record["schedulingPreference"]; exists && !routeStrategyConfigValueMissing(raw) {
		text, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("普通路由调度偏好无效")
		}
		preference = text
	}
	var speed *SpeedFirstConfig
	if raw, exists := record["speedFirstConfig"]; exists {
		var err error
		speed, err = normalizeManagementSpeedFirstConfig(raw)
		if err != nil {
			return nil, err
		}
	}
	switch preference {
	case defaultSchedulingPreference:
		return &NormalRoutingConfig{SchedulingPreference: preference}, nil
	case schedulingPreferenceSpeedFirst:
		if speed == nil {
			var err error
			speed, err = normalizeManagementSpeedFirstConfig(nil)
			if err != nil {
				return nil, err
			}
		}
		return &NormalRoutingConfig{
			SchedulingPreference: preference,
			SpeedFirstConfig:     speed,
		}, nil
	default:
		return nil, fmt.Errorf("普通路由调度偏好无效")
	}
}

func normalizeManagementSpeedFirstConfig(value any) (*SpeedFirstConfig, error) {
	config := &SpeedFirstConfig{
		FirstByteThresholdMs:          defaultFirstByteThresholdMs,
		SlowTriggerCount:              defaultSlowTriggerCount,
		SlowWindowSeconds:             defaultSlowWindowSeconds,
		RecoverySuccessCount:          defaultRecoverySuccessCount,
		ProbeIntervalSeconds:          defaultProbeIntervalSeconds,
		DegradedTTLSeconds:            defaultDegradedTTLSeconds,
		MaxFirstByteRetriesPerRequest: defaultMaxFirstByteRetriesPerRequest,
	}
	if routeStrategyConfigValueMissing(value) {
		return config, nil
	}
	record, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("速度优先配置无效")
	}
	var err error
	if config.FirstByteThresholdMs, err = routeStrategyConfigInteger(record["firstByteThresholdMs"], config.FirstByteThresholdMs, 10000, 60000, "首字观察阈值必须是 10000-60000 毫秒"); err != nil {
		return nil, err
	}
	if config.SlowTriggerCount, err = routeStrategyConfigInteger(record["slowTriggerCount"], config.SlowTriggerCount, 2, 10, "速度优先触发次数必须是 2-10"); err != nil {
		return nil, err
	}
	if config.SlowWindowSeconds, err = routeStrategyConfigInteger(record["slowWindowSeconds"], config.SlowWindowSeconds, 60, 600, "速度优先窗口期必须是 60-600 秒"); err != nil {
		return nil, err
	}
	if config.RecoverySuccessCount, err = routeStrategyConfigInteger(record["recoverySuccessCount"], config.RecoverySuccessCount, 3, 10, "速度优先恢复次数必须是 3-10"); err != nil {
		return nil, err
	}
	if config.ProbeIntervalSeconds, err = routeStrategyConfigInteger(record["probeIntervalSeconds"], config.ProbeIntervalSeconds, 10, 300, "速度优先探针间隔必须是 10-300 秒"); err != nil {
		return nil, err
	}
	if config.DegradedTTLSeconds, err = routeStrategyConfigInteger(record["degradedTtlSeconds"], config.DegradedTTLSeconds, 60, 3600, "速度优先降级保留时间必须是 60-3600 秒"); err != nil {
		return nil, err
	}
	if config.MaxFirstByteRetriesPerRequest, err = routeStrategyConfigInteger(record["maxFirstByteRetriesPerRequest"], config.MaxFirstByteRetriesPerRequest, 1, 3, "速度优先单请求切号次数必须是 1-3"); err != nil {
		return nil, err
	}
	return config, nil
}

func routeStrategyConfigInteger(
	value any,
	fallback int,
	minValue int,
	maxValue int,
	message string,
) (int, error) {
	if routeStrategyConfigValueMissing(value) {
		return fallback, nil
	}
	numeric, ok := routeStrategyConfigNumber(value)
	if !ok {
		return 0, fmt.Errorf("%s", message)
	}
	if math.IsNaN(numeric) || math.IsInf(numeric, 0) || numeric != math.Trunc(numeric) ||
		numeric < float64(minValue) || numeric > float64(maxValue) {
		return 0, fmt.Errorf("%s", message)
	}
	return int(numeric), nil
}

func normalizeManagementHybridRoutingConfig(value any) (map[string]any, error) {
	record, ok := value.(map[string]any)
	if !ok || record == nil {
		return nil, fmt.Errorf("混合路由配置不能为空")
	}
	scoringModel, err := routeStrategyConfigNonEmptyString(
		record["scoringModel"],
		"混合路由评分模型不能为空",
	)
	if err != nil {
		return nil, err
	}
	scoringContextMode, err := routeStrategyHybridScoringContextMode(record["scoringContextMode"])
	if err != nil {
		return nil, err
	}
	qualityPreference, err := routeStrategyHybridQualityPreference(record["qualityPreference"])
	if err != nil {
		return nil, err
	}
	scoringTimeoutMs, err := routeStrategyConfigInteger(
		record["scoringTimeoutMs"],
		defaultHybridScoringTimeoutMs,
		1000,
		60000,
		"混合路由评分超时时间必须是 1000-60000 毫秒",
	)
	if err != nil {
		return nil, err
	}
	scoringFallbackMaxLevel, err := routeStrategyConfigInteger(
		record["scoringFallbackMaxLevel"],
		defaultHybridScoringFallbackMaxLevel,
		2,
		5,
		"混合路由评分不可用兜底上限必须是 2-5",
	)
	if err != nil {
		return nil, err
	}
	scoringCacheTTLSeconds, err := routeStrategyConfigInteger(
		record["scoringCacheTtlSeconds"],
		defaultHybridScoringCacheTTLSeconds,
		1,
		3600,
		"混合路由评分缓存 TTL 必须是 1-3600 秒",
	)
	if err != nil {
		return nil, err
	}
	affinityTTLSeconds, err := routeStrategyConfigInteger(
		record["affinityTtlSeconds"],
		defaultHybridAffinityTTLSeconds,
		1,
		86400,
		"混合路由缓存亲和 TTL 必须是 1-86400 秒",
	)
	if err != nil {
		return nil, err
	}
	switchMinLevelDelta, err := routeStrategyConfigInteger(
		record["switchMinLevelDelta"],
		defaultHybridSwitchMinLevelDelta,
		0,
		9,
		"混合路由切换等级差必须是 0-9",
	)
	if err != nil {
		return nil, err
	}
	downgradeConsecutiveLowCount, err := routeStrategyConfigInteger(
		record["downgradeConsecutiveLowCount"],
		defaultHybridDowngradeConsecutiveLowCount,
		1,
		20,
		"混合路由降级确认次数必须是 1-20",
	)
	if err != nil {
		return nil, err
	}
	levelRoutes, err := normalizeManagementHybridLevelRoutes(record["levelRoutes"])
	if err != nil {
		return nil, err
	}
	qualityInspection, err := normalizeManagementHybridQualityInspection(
		record["qualityInspection"],
		scoringModel,
	)
	if err != nil {
		return nil, err
	}
	output := map[string]any{
		"scoringModel":                 scoringModel,
		"scoringContextMode":           scoringContextMode,
		"qualityPreference":            qualityPreference,
		"scoringTimeoutMs":             scoringTimeoutMs,
		"scoringFallbackMaxLevel":      scoringFallbackMaxLevel,
		"scoringCacheEnabled":          true,
		"scoringCacheTtlSeconds":       scoringCacheTTLSeconds,
		"cacheAffinityEnabled":         true,
		"affinityTtlSeconds":           affinityTTLSeconds,
		"switchMinLevelDelta":          switchMinLevelDelta,
		"downgradeConsecutiveLowCount": downgradeConsecutiveLowCount,
		"levelRoutes":                  levelRoutes,
		"qualityInspection":            qualityInspection,
	}
	if scoringGroupID := routeStrategyConfigOptionalString(record["scoringGroupId"]); scoringGroupID != "" {
		output["scoringGroupId"] = scoringGroupID
	}
	return output, nil
}

func routeStrategyHybridScoringContextMode(value any) (string, error) {
	if routeStrategyConfigValueMissing(value) {
		return defaultHybridScoringContextMode, nil
	}
	if value == defaultHybridScoringContextMode {
		return defaultHybridScoringContextMode, nil
	}
	return "", fmt.Errorf("混合路由评分上下文模式无效")
}

func routeStrategyHybridQualityPreference(value any) (string, error) {
	if routeStrategyConfigValueMissing(value) {
		return defaultHybridQualityPreference, nil
	}
	switch value {
	case "cost_first", defaultHybridQualityPreference, "quality_first":
		return value.(string), nil
	default:
		return "", fmt.Errorf("混合路由质量偏好无效")
	}
}

type managementHybridLevelRoute struct {
	MinLevel    int
	MaxLevel    int
	TargetModel string
	Enabled     bool
}

func normalizeManagementHybridLevelRoutes(value any) ([]map[string]any, error) {
	rawRoutes, ok := value.([]any)
	if !ok || len(rawRoutes) == 0 {
		return nil, fmt.Errorf("混合路由等级范围不能为空")
	}
	routes := make([]managementHybridLevelRoute, 0, len(rawRoutes))
	for _, rawRoute := range rawRoutes {
		route, err := normalizeManagementHybridLevelRoute(rawRoute)
		if err != nil {
			return nil, err
		}
		if route.Enabled {
			routes = append(routes, route)
		}
	}
	if len(routes) == 0 {
		return nil, fmt.Errorf("混合路由至少需要一个启用的等级范围")
	}
	if len(routes) > maxHybridLevelRouteCount {
		return nil, fmt.Errorf("混合路由最多只能配置 %d 个等级范围", maxHybridLevelRouteCount)
	}
	targetModels := make(map[string]struct{}, len(routes))
	for _, route := range routes {
		targetModels[strings.ToLower(route.TargetModel)] = struct{}{}
	}
	if len(targetModels) < 2 {
		return nil, fmt.Errorf("混合路由至少需要配置 2 个不同的目标模型")
	}
	firstRoute := routes[0]
	if firstRoute.MinLevel != 1 || firstRoute.MaxLevel < 2 || firstRoute.MaxLevel > 5 {
		return nil, fmt.Errorf("混合路由最低档必须从等级 1 开始，并覆盖 1-2 到 1-5 之间的范围")
	}
	expectedMinLevel := 1
	for index, route := range routes {
		if route.MinLevel != expectedMinLevel {
			return nil, fmt.Errorf(
				"混合路由第 %d 个等级范围必须从等级 %d 开始",
				index+1,
				expectedMinLevel,
			)
		}
		expectedMinLevel = route.MaxLevel + 1
	}
	if expectedMinLevel != 11 {
		return nil, fmt.Errorf("混合路由等级范围必须按从小到大连续覆盖 1-10")
	}
	output := make([]map[string]any, 0, len(routes))
	for _, route := range routes {
		output = append(output, map[string]any{
			"minLevel":    route.MinLevel,
			"maxLevel":    route.MaxLevel,
			"targetModel": route.TargetModel,
			"enabled":     route.Enabled,
		})
	}
	return output, nil
}

func normalizeManagementHybridLevelRoute(value any) (managementHybridLevelRoute, error) {
	record, ok := value.(map[string]any)
	if !ok || record == nil {
		return managementHybridLevelRoute{}, fmt.Errorf("混合路由等级范围无效")
	}
	minLevel, err := routeStrategyConfigRequiredInteger(
		record["minLevel"],
		1,
		10,
		"混合路由最小等级必须是 1-10",
	)
	if err != nil {
		return managementHybridLevelRoute{}, err
	}
	maxLevel, err := routeStrategyConfigRequiredInteger(
		record["maxLevel"],
		1,
		10,
		"混合路由最大等级必须是 1-10",
	)
	if err != nil {
		return managementHybridLevelRoute{}, err
	}
	if minLevel > maxLevel {
		return managementHybridLevelRoute{}, fmt.Errorf("混合路由等级范围最小值不能大于最大值")
	}
	targetModel, err := routeStrategyConfigNonEmptyString(
		record["targetModel"],
		"混合路由目标模型不能为空",
	)
	if err != nil {
		return managementHybridLevelRoute{}, err
	}
	enabled := true
	if rawEnabled, exists := record["enabled"]; exists {
		enabled, err = routeStrategyConfigBoolean(
			rawEnabled,
			"混合路由等级范围启用状态必须是布尔值",
		)
		if err != nil {
			return managementHybridLevelRoute{}, err
		}
	}
	return managementHybridLevelRoute{
		MinLevel:    minLevel,
		MaxLevel:    maxLevel,
		TargetModel: targetModel,
		Enabled:     enabled,
	}, nil
}

func normalizeManagementHybridQualityInspection(
	value any,
	defaultScoringModel string,
) (map[string]any, error) {
	if routeStrategyConfigValueMissing(value) {
		return map[string]any{
			"enabled":           true,
			"scoringModel":      defaultScoringModel,
			"triggerMode":       defaultHybridQualityInspectionTriggerMode,
			"maxTriggerLevel":   defaultHybridQualityInspectionMaxTrigger,
			"maxRetries":        defaultHybridQualityInspectionMaxRetries,
			"failureAction":     defaultHybridQualityInspectionFailureAction,
			"unavailableAction": defaultHybridQualityInspectionUnavailable,
		}, nil
	}
	record, ok := value.(map[string]any)
	if !ok || record == nil {
		return nil, fmt.Errorf("混合路由质量评分配置无效")
	}
	enabled := true
	var err error
	if rawEnabled, exists := record["enabled"]; exists {
		enabled, err = routeStrategyConfigBoolean(
			rawEnabled,
			"混合路由质量评分开关必须是布尔值",
		)
		if err != nil {
			return nil, err
		}
	}
	scoringModel := routeStrategyConfigOptionalString(record["scoringModel"])
	if scoringModel == "" {
		scoringModel = defaultScoringModel
	}
	if enabled && scoringModel == "" {
		return nil, fmt.Errorf("混合路由质量评分模型不能为空")
	}
	triggerMode, err := routeStrategyHybridQualityInspectionTriggerMode(record["triggerMode"])
	if err != nil {
		return nil, err
	}
	maxTriggerLevel, err := routeStrategyConfigInteger(
		record["maxTriggerLevel"],
		defaultHybridQualityInspectionMaxTrigger,
		1,
		10,
		"混合路由质量评分最高触发等级必须是 1-10",
	)
	if err != nil {
		return nil, err
	}
	maxRetries, err := routeStrategyConfigInteger(
		record["maxRetries"],
		defaultHybridQualityInspectionMaxRetries,
		0,
		2,
		"混合路由质量评分重试次数必须是 0-2",
	)
	if err != nil {
		return nil, err
	}
	failureAction, err := routeStrategyHybridQualityInspectionFailureAction(record["failureAction"])
	if err != nil {
		return nil, err
	}
	unavailableAction, err := routeStrategyHybridQualityInspectionUnavailableAction(record["unavailableAction"])
	if err != nil {
		return nil, err
	}
	output := map[string]any{
		"enabled":           enabled,
		"scoringModel":      scoringModel,
		"triggerMode":       triggerMode,
		"maxTriggerLevel":   maxTriggerLevel,
		"maxRetries":        maxRetries,
		"failureAction":     failureAction,
		"unavailableAction": unavailableAction,
	}
	if scoringGroupID := routeStrategyConfigOptionalString(record["scoringGroupId"]); scoringGroupID != "" {
		output["scoringGroupId"] = scoringGroupID
	}
	return output, nil
}

func routeStrategyHybridQualityInspectionTriggerMode(value any) (string, error) {
	if routeStrategyConfigValueMissing(value) {
		return defaultHybridQualityInspectionTriggerMode, nil
	}
	switch value {
	case "quality_first_only", defaultHybridQualityInspectionTriggerMode, "always_for_hybrid":
		return value.(string), nil
	default:
		return "", fmt.Errorf("混合路由质量评分触发模式无效")
	}
}

func routeStrategyHybridQualityInspectionFailureAction(value any) (string, error) {
	if routeStrategyConfigValueMissing(value) {
		return defaultHybridQualityInspectionFailureAction, nil
	}
	switch value {
	case defaultHybridQualityInspectionFailureAction,
		"upgrade_next_level",
		"retry_same_model",
		"return_error":
		return value.(string), nil
	default:
		return "", fmt.Errorf("混合路由质量评分失败动作无效")
	}
}

func routeStrategyHybridQualityInspectionUnavailableAction(value any) (string, error) {
	if routeStrategyConfigValueMissing(value) {
		return defaultHybridQualityInspectionUnavailable, nil
	}
	switch value {
	case defaultHybridQualityInspectionUnavailable, "return_error":
		return value.(string), nil
	default:
		return "", fmt.Errorf("混合路由质量评分不可用处理方式无效")
	}
}

func routeStrategyConfigRequiredInteger(
	value any,
	minValue int,
	maxValue int,
	message string,
) (int, error) {
	if routeStrategyConfigValueMissing(value) {
		return 0, fmt.Errorf("%s", message)
	}
	return routeStrategyConfigInteger(value, 0, minValue, maxValue, message)
}

func routeStrategyConfigNonEmptyString(value any, message string) (string, error) {
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s", message)
	}
	text = routeStrategyTrimECMAScriptWhitespace(text)
	if text == "" {
		return "", fmt.Errorf("%s", message)
	}
	return text, nil
}

func routeStrategyConfigOptionalString(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return routeStrategyTrimECMAScriptWhitespace(text)
}

func routeStrategyConfigBoolean(value any, message string) (bool, error) {
	enabled, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%s", message)
	}
	return enabled, nil
}

func routeStrategyConfigNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case json.Number:
		number, err := typed.Float64()
		return number, err == nil || math.IsInf(number, 0)
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	case bool:
		if typed {
			return 1, true
		}
		return 0, true
	case string:
		return routeStrategyConfigStringNumber(typed)
	case []any:
		return routeStrategyConfigStringNumber(routeStrategyConfigArrayString(typed))
	default:
		return 0, false
	}
}

func routeStrategyConfigStringNumber(value string) (float64, bool) {
	value = routeStrategyTrimECMAScriptWhitespace(value)
	if value == "" {
		return 0, true
	}
	switch value {
	case "Infinity", "+Infinity":
		return math.Inf(1), true
	case "-Infinity":
		return math.Inf(-1), true
	}
	if len(value) > 2 && value[0] == '0' {
		base := 0
		switch value[1] {
		case 'x', 'X':
			base = 16
		case 'b', 'B':
			base = 2
		case 'o', 'O':
			base = 8
		}
		if base != 0 {
			number, err := strconv.ParseUint(value[2:], base, 64)
			if err != nil {
				return 0, false
			}
			return float64(number), true
		}
	}
	if !routeStrategyDecimalNumberPattern.MatchString(value) {
		return 0, false
	}
	number, err := strconv.ParseFloat(value, 64)
	return number, err == nil || math.IsInf(number, 0)
}

func routeStrategyConfigArrayString(values []any) string {
	parts := make([]string, len(values))
	for index, value := range values {
		switch typed := value.(type) {
		case nil:
			parts[index] = ""
		case string:
			parts[index] = typed
		case bool:
			parts[index] = strconv.FormatBool(typed)
		case json.Number:
			parts[index] = typed.String()
		case float64:
			parts[index] = strconv.FormatFloat(typed, 'g', -1, 64)
		case int:
			parts[index] = strconv.Itoa(typed)
		case []any:
			parts[index] = routeStrategyConfigArrayString(typed)
		default:
			parts[index] = "[object Object]"
		}
	}
	return strings.Join(parts, ",")
}

func routeStrategyTrimECMAScriptWhitespace(value string) string {
	return strings.TrimFunc(value, func(character rune) bool {
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
	})
}
