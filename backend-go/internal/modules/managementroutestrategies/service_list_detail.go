package managementroutestrategies

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
)

var (
	ErrRouteStrategyListInvalid = errors.New("management route strategy list invalid")
	ErrRouteStrategyNotFound    = errors.New("策略路由不存在")
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
	systemAccountID = strings.TrimSpace(systemAccountID)
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
	if page > maxInt/max(1, pageSize) {
		return maxInt - pageSize
	}
	return (page - 1) * pageSize
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
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return routeStrategyRuntimeConfig{}, nil
	}
	decoder := json.NewDecoder(bytes.NewBufferString(*raw))
	decoder.UseNumber()
	var parsed any
	if err := decoder.Decode(&parsed); err != nil {
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
		hybrid, ok := value.(map[string]any)
		if !ok || len(hybrid) == 0 {
			return routeStrategyRuntimeConfig{}, fmt.Errorf("混合路由配置无效")
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
	default:
		return true
	}
}

func normalizeManagementNormalRoutingConfig(value any) (*NormalRoutingConfig, error) {
	record, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("普通路由调度配置无效")
	}
	preference := defaultSchedulingPreference
	if raw, exists := record["schedulingPreference"]; exists && routeStrategyConfigValuePresent(raw) {
		text, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("普通路由调度偏好无效")
		}
		preference = text
	}
	switch preference {
	case defaultSchedulingPreference:
		return &NormalRoutingConfig{SchedulingPreference: preference}, nil
	case schedulingPreferenceSpeedFirst:
		speed, err := normalizeManagementSpeedFirstConfig(record["speedFirstConfig"])
		if err != nil {
			return nil, err
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
	if !routeStrategyConfigValuePresent(value) {
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
	if !routeStrategyConfigValuePresent(value) {
		return fallback, nil
	}
	var numeric float64
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, fmt.Errorf("%s", message)
		}
		numeric = parsed
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return 0, fmt.Errorf("%s", message)
		}
		numeric = parsed
	case float64:
		numeric = typed
	case int:
		numeric = float64(typed)
	default:
		return 0, fmt.Errorf("%s", message)
	}
	if math.IsNaN(numeric) || math.IsInf(numeric, 0) || numeric != math.Trunc(numeric) ||
		numeric < float64(minValue) || numeric > float64(maxValue) {
		return 0, fmt.Errorf("%s", message)
	}
	return int(numeric), nil
}
