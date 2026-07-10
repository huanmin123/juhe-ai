package managementgroups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	defaultOptionLimit = 50
	maxOptionLimit     = 50

	GroupCreatedReason              = "group_created"
	groupRuntimeInvalidationTimeout = 5 * time.Second
)

type Service struct {
	store                   port.ManagementGroupOptionReader
	listStore               port.ManagementGroupListReader
	detailStore             port.ManagementGroupDetailReader
	usageStatsTimezoneStore port.ManagementUsageStatsTimezoneReader
	accountConcurrency      AccountConcurrencyReader
	invalidator             RuntimeInvalidator
	logger                  *slog.Logger
	now                     func() time.Time
	newID                   func(prefix string) string
}

type ServiceOptions struct {
	Store                   port.ManagementGroupOptionReader
	ListStore               port.ManagementGroupListReader
	DetailStore             port.ManagementGroupDetailReader
	UsageStatsTimezoneStore port.ManagementUsageStatsTimezoneReader
	AccountConcurrency      AccountConcurrencyReader
	Invalidator             RuntimeInvalidator
	Logger                  *slog.Logger
	Now                     func() time.Time
	NewID                   func(prefix string) string
}

type RuntimeInvalidator interface {
	InvalidateGatewayRuntime(ctx context.Context, reason string) error
}

type OptionListInput struct {
	SystemAccountID            string
	IncludeSystemAccountFields bool
	IDs                        []string
	Keyword                    string
	ProviderCode               string
	Limit                      int
	ManageableOnly             bool
	PreferDefault              bool
}

type ResourcePermissions struct {
	CanUse                 bool `json:"canUse"`
	CanEdit                bool `json:"canEdit"`
	CanDelete              bool `json:"canDelete"`
	CanReturnAuthorization bool `json:"canReturnAuthorization"`
	CanAuthorize           bool `json:"canAuthorize"`
	CanViewCredentials     bool `json:"canViewCredentials"`
	CanManageAccounts      bool `json:"canManageAccounts"`
	CanBindToAPIKey        bool `json:"canBindToApiKey"`
}

type Option struct {
	ID                     string              `json:"id"`
	SystemAccountID        string              `json:"systemAccountId,omitempty"`
	SystemAccountName      string              `json:"systemAccountName,omitempty"`
	OwnerSystemAccountID   string              `json:"ownerSystemAccountId"`
	OwnerSystemAccountName string              `json:"ownerSystemAccountName,omitempty"`
	Name                   string              `json:"name"`
	ProviderCode           string              `json:"providerCode"`
	Enabled                bool                `json:"enabled"`
	IsDefault              bool                `json:"isDefault"`
	GroupType              string              `json:"groupType"`
	SchedulingPolicy       map[string]any      `json:"schedulingPolicy,omitempty"`
	AccessType             string              `json:"accessType"`
	GroupAuthorizationID   string              `json:"groupAuthorizationId,omitempty"`
	AuthorizationStatus    string              `json:"authorizationStatus,omitempty"`
	AuthorizationExpiresAt *time.Time          `json:"authorizationExpiresAt,omitempty"`
	AuthorizationLimits    map[string]any      `json:"authorizationLimits,omitempty"`
	Permissions            ResourcePermissions `json:"permissions"`
}

type AccountOption struct {
	Option
	AccountIDs []string `json:"accountIds"`
}

type SchedulingPolicyInput struct {
	DefaultSoftConcurrency          *int    `json:"defaultSoftConcurrency,omitempty"`
	MaxQueueWaitMs                  *int    `json:"maxQueueWaitMs,omitempty"`
	ClientIPConcurrencyLimit        *int    `json:"clientIpConcurrencyLimit,omitempty"`
	ClientIPConcurrencyOverflowMode *string `json:"clientIpConcurrencyOverflowMode,omitempty"`
	ImageLaneMaxConcurrency         *int    `json:"imageLaneMaxConcurrency,omitempty"`
}

type SchedulingPolicy struct {
	Mode                            string `json:"mode"`
	DefaultSoftConcurrency          int    `json:"defaultSoftConcurrency"`
	FastFirstEnabled                bool   `json:"fastFirstEnabled"`
	FallbackOnQueueEnabled          bool   `json:"fallbackOnQueueEnabled"`
	BreakAffinityOnSoftLimit        bool   `json:"breakAffinityOnSoftLimit"`
	BreakAffinityOnQueueWaitMs      int    `json:"breakAffinityOnQueueWaitMs"`
	SlowRequestThresholdMs          int    `json:"slowRequestThresholdMs"`
	FirstOutputSlowThresholdMs      int    `json:"firstOutputSlowThresholdMs"`
	RecentTimeoutWindowSeconds      int    `json:"recentTimeoutWindowSeconds"`
	RecentTimeoutPenaltyThreshold   int    `json:"recentTimeoutPenaltyThreshold"`
	MaxQueueWaitMs                  int    `json:"maxQueueWaitMs"`
	MaxQueueSize                    int    `json:"maxQueueSize"`
	PerAPIKeyQueueLimit             int    `json:"perApiKeyQueueLimit"`
	ClientIPConcurrencyLimit        int    `json:"clientIpConcurrencyLimit"`
	ClientIPConcurrencyOverflowMode string `json:"clientIpConcurrencyOverflowMode"`
	ImageLaneMaxConcurrency         int    `json:"imageLaneMaxConcurrency"`
}

type UsageSummary struct {
	RequestCount       int64      `json:"requestCount"`
	InputTokens        int64      `json:"inputTokens"`
	OutputTokens       int64      `json:"outputTokens"`
	CacheReadTokens    int64      `json:"cacheReadTokens"`
	CacheReadCost      float64    `json:"cacheReadCost"`
	CacheWriteTokens   int64      `json:"cacheWriteTokens"`
	CacheWrite1hTokens int64      `json:"cacheWrite1hTokens"`
	CacheWriteCost     float64    `json:"cacheWriteCost"`
	ThinkingTokens     int64      `json:"thinkingTokens"`
	InputImageTokens   int64      `json:"inputImageTokens"`
	OutputImageTokens  int64      `json:"outputImageTokens"`
	TotalTokens        int64      `json:"totalTokens"`
	TotalCost          float64    `json:"totalCost"`
	LastUsedAt         *time.Time `json:"lastUsedAt,omitempty"`
}

type GroupAccountStats struct {
	Total              int          `json:"total"`
	Available          int          `json:"available"`
	Active             int          `json:"active"`
	Disabled           int          `json:"disabled"`
	Error              int          `json:"error"`
	RateLimited        int          `json:"rateLimited"`
	CurrentConcurrency int          `json:"currentConcurrency"`
	ConcurrencyLimit   int          `json:"concurrencyLimit"`
	TodayUsage         UsageSummary `json:"todayUsage"`
	Usage              UsageSummary `json:"usage"`
}

type Summary struct {
	ID               string            `json:"id"`
	SystemAccountID  string            `json:"systemAccountId,omitempty"`
	Name             string            `json:"name"`
	ProviderCode     string            `json:"providerCode"`
	Description      *string           `json:"description,omitempty"`
	Enabled          bool              `json:"enabled"`
	IsDefault        bool              `json:"isDefault"`
	GroupType        string            `json:"groupType"`
	SchedulingPolicy *SchedulingPolicy `json:"schedulingPolicy,omitempty"`
	AccountIDs       []string          `json:"accountIds"`
	AccountStats     GroupAccountStats `json:"accountStats"`
}

type CreateInput struct {
	SystemAccountID            string
	IncludeSystemAccountFields bool
	Name                       string
	ProviderCode               string
	Description                *string
	Enabled                    *bool
	GroupType                  string
	SchedulingPolicy           *SchedulingPolicyInput
}

type CreateResult = Summary

var ErrSystemAccountNotFound = errors.New("目标系统账户不存在")

type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string {
	return e.Message
}

type ProviderNotFoundError struct {
	Code string
}

func (e *ProviderNotFoundError) Error() string {
	return "不支持的供应商：" + strings.TrimSpace(e.Code)
}

type ProviderDisabledError struct {
	Code string
}

func (e *ProviderDisabledError) Error() string {
	return "供应商已停用：" + strings.TrimSpace(e.Code)
}

type NameExistsError struct {
	Name string
}

func (e *NameExistsError) Error() string {
	name := strings.TrimSpace(e.Name)
	if name == "" {
		return "同一供应商下分组名称已存在"
	}
	return "同一供应商下分组名称已存在：" + name
}

func ValidationMessage(err error) (string, bool) {
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		return "", false
	}
	if strings.TrimSpace(validationErr.Message) == "" {
		return "分组参数无效", true
	}
	return validationErr.Message, true
}

func ProviderNotFoundMessage(err error) (string, bool) {
	var providerErr *ProviderNotFoundError
	if !errors.As(err, &providerErr) {
		return "", false
	}
	return providerErr.Error(), true
}

func ProviderDisabledMessage(err error) (string, bool) {
	var providerErr *ProviderDisabledError
	if !errors.As(err, &providerErr) {
		return "", false
	}
	return providerErr.Error(), true
}

func NameExistsMessage(err error) (string, bool) {
	var existsErr *NameExistsError
	if !errors.As(err, &existsErr) {
		return "", false
	}
	return existsErr.Error(), true
}

func NewService(store port.ManagementGroupOptionReader) *Service {
	return NewServiceWithOptions(ServiceOptions{Store: store})
}

func NewServiceWithOptions(opts ServiceOptions) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	newID := opts.NewID
	if newID == nil {
		newID = func(prefix string) string {
			return prefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		}
	}
	listStore := opts.ListStore
	if listStore == nil {
		if candidate, ok := opts.Store.(port.ManagementGroupListReader); ok {
			listStore = candidate
		}
	}
	detailStore := opts.DetailStore
	if detailStore == nil {
		if candidate, ok := opts.Store.(port.ManagementGroupDetailReader); ok {
			detailStore = candidate
		}
	}
	usageStatsTimezoneStore := opts.UsageStatsTimezoneStore
	if usageStatsTimezoneStore == nil {
		if candidate, ok := opts.Store.(port.ManagementUsageStatsTimezoneReader); ok {
			usageStatsTimezoneStore = candidate
		} else if candidate, ok := opts.ListStore.(port.ManagementUsageStatsTimezoneReader); ok {
			usageStatsTimezoneStore = candidate
		}
	}
	return &Service{
		store:                   opts.Store,
		listStore:               listStore,
		detailStore:             detailStore,
		usageStatsTimezoneStore: usageStatsTimezoneStore,
		accountConcurrency:      opts.AccountConcurrency,
		invalidator:             opts.Invalidator,
		logger:                  opts.Logger,
		now:                     now,
		newID:                   newID,
	}
}

func (s *Service) Options(ctx context.Context, input OptionListInput) ([]Option, error) {
	if s.store == nil {
		return nil, fmt.Errorf("management group option store is required")
	}
	rows, err := s.store.ListManagementGroupOptions(ctx, port.ManagementGroupOptionListInput{
		SystemAccountID:            strings.TrimSpace(input.SystemAccountID),
		IncludeSystemAccountFields: input.IncludeSystemAccountFields,
		IDs:                        uniqueStrings(input.IDs, 50),
		Keyword:                    strings.TrimSpace(input.Keyword),
		ProviderCode:               strings.TrimSpace(input.ProviderCode),
		Limit:                      optionLimit(input.Limit),
		ManageableOnly:             input.ManageableOnly,
		PreferDefault:              input.PreferDefault,
	})
	if err != nil {
		return nil, err
	}
	items := make([]Option, 0, len(rows))
	for _, row := range rows {
		items = append(items, Option{
			ID:                     row.ID,
			SystemAccountID:        row.SystemAccountID,
			SystemAccountName:      row.SystemAccountName,
			OwnerSystemAccountID:   row.OwnerSystemAccountID,
			OwnerSystemAccountName: row.OwnerSystemAccountName,
			Name:                   row.Name,
			ProviderCode:           row.ProviderCode,
			Enabled:                row.Enabled,
			IsDefault:              row.IsDefault,
			GroupType:              row.GroupType,
			SchedulingPolicy:       row.SchedulingPolicy,
			AccessType:             groupAccessType(row.AccessType),
			GroupAuthorizationID:   row.GroupAuthorizationID,
			AuthorizationStatus:    row.AuthorizationStatus,
			AuthorizationExpiresAt: row.AuthorizationExpiresAt,
			AuthorizationLimits:    row.AuthorizationLimits,
			Permissions:            groupPermissions(row),
		})
	}
	return items, nil
}

func (s *Service) AccountOptions(ctx context.Context, input OptionListInput) ([]AccountOption, error) {
	if s.store == nil {
		return nil, fmt.Errorf("management account group option store is required")
	}
	rows, err := s.store.ListManagementGroupAccountOptions(ctx, port.ManagementGroupOptionListInput{
		SystemAccountID:            strings.TrimSpace(input.SystemAccountID),
		IncludeSystemAccountFields: input.IncludeSystemAccountFields,
		IDs:                        uniqueStrings(input.IDs, 50),
		Keyword:                    strings.TrimSpace(input.Keyword),
		ProviderCode:               strings.TrimSpace(input.ProviderCode),
		Limit:                      optionLimit(input.Limit),
		ManageableOnly:             input.ManageableOnly,
		PreferDefault:              input.PreferDefault,
	})
	if err != nil {
		return nil, err
	}
	items := make([]AccountOption, 0, len(rows))
	for _, row := range rows {
		items = append(items, AccountOption{
			Option: Option{
				ID:                     row.ID,
				SystemAccountID:        row.SystemAccountID,
				SystemAccountName:      row.SystemAccountName,
				OwnerSystemAccountID:   row.OwnerSystemAccountID,
				OwnerSystemAccountName: row.OwnerSystemAccountName,
				Name:                   row.Name,
				ProviderCode:           row.ProviderCode,
				Enabled:                row.Enabled,
				IsDefault:              row.IsDefault,
				GroupType:              row.GroupType,
				SchedulingPolicy:       row.SchedulingPolicy,
				AccessType:             groupAccessType(row.AccessType),
				GroupAuthorizationID:   row.GroupAuthorizationID,
				AuthorizationStatus:    row.AuthorizationStatus,
				AuthorizationExpiresAt: row.AuthorizationExpiresAt,
				AuthorizationLimits:    row.AuthorizationLimits,
				Permissions:            groupAccountPermissions(row),
			},
			AccountIDs: append([]string(nil), row.AccountIDs...),
		})
	}
	return items, nil
}

func (s *Service) Create(ctx context.Context, input CreateInput) (CreateResult, error) {
	writer, err := s.groupCreator()
	if err != nil {
		return CreateResult{}, err
	}
	systemAccountID := strings.TrimSpace(input.SystemAccountID)
	if systemAccountID == "" {
		return CreateResult{}, &ValidationError{Message: "缺少系统账户上下文"}
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return CreateResult{}, &ValidationError{Message: "分组名称不能为空"}
	}
	providerCode := strings.TrimSpace(input.ProviderCode)
	if providerCode == "" {
		return CreateResult{}, &ValidationError{Message: "供应商不能为空"}
	}
	description := normalizeDescription(input.Description)
	groupType, err := normalizeGroupType(input.GroupType)
	if err != nil {
		return CreateResult{}, err
	}
	policy, err := normalizeSchedulingPolicy(input.SchedulingPolicy)
	if err != nil {
		return CreateResult{}, err
	}
	var schedulingPolicy *SchedulingPolicy
	var schedulingPolicyJSON *string
	if groupType == "high_concurrency" {
		schedulingPolicy = &policy
		encoded, err := json.Marshal(policy)
		if err != nil {
			return CreateResult{}, fmt.Errorf("encode management group scheduling policy: %w", err)
		}
		value := string(encoded)
		schedulingPolicyJSON = &value
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	now := s.now().UTC()
	created, err := writer.CreateManagementGroup(ctx, port.ManagementGroupCreateInput{
		ID:                   s.newID("grp"),
		SystemAccountID:      systemAccountID,
		Name:                 name,
		ProviderCode:         providerCode,
		Description:          description,
		Enabled:              enabled,
		GroupType:            groupType,
		SchedulingPolicyJSON: schedulingPolicyJSON,
		CreatedAt:            now,
		UpdatedAt:            now,
	})
	switch {
	case errors.Is(err, port.ErrManagementGroupSystemAccountNotFound):
		return CreateResult{}, ErrSystemAccountNotFound
	case errors.Is(err, port.ErrManagementGroupProviderNotFound):
		return CreateResult{}, &ProviderNotFoundError{Code: providerCode}
	case errors.Is(err, port.ErrManagementGroupProviderDisabled):
		return CreateResult{}, &ProviderDisabledError{Code: providerCode}
	case errors.Is(err, port.ErrManagementGroupNameExists):
		return CreateResult{}, &NameExistsError{Name: name}
	case err != nil:
		return CreateResult{}, err
	}
	s.invalidateRuntime(ctx)
	result := CreateResult{
		ID:               created.ID,
		Name:             created.Name,
		ProviderCode:     created.ProviderCode,
		Description:      created.Description,
		Enabled:          created.Enabled,
		IsDefault:        created.IsDefault,
		GroupType:        created.GroupType,
		SchedulingPolicy: schedulingPolicy,
		AccountIDs:       []string{},
		AccountStats:     GroupAccountStats{},
	}
	if input.IncludeSystemAccountFields {
		result.SystemAccountID = created.SystemAccountID
	}
	return result, nil
}

func (s *Service) groupCreator() (port.ManagementGroupCreator, error) {
	if s.store == nil {
		return nil, fmt.Errorf("management group store is required")
	}
	writer, ok := s.store.(port.ManagementGroupCreator)
	if !ok {
		return nil, fmt.Errorf("management group creator store is required")
	}
	return writer, nil
}

func (s *Service) invalidateRuntime(ctx context.Context) {
	if s.invalidator == nil {
		return
	}
	invalidationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), groupRuntimeInvalidationTimeout)
	defer cancel()
	if err := s.invalidator.InvalidateGatewayRuntime(invalidationCtx, GroupCreatedReason); err != nil && s.logger != nil {
		s.logger.Warn(
			"分组创建后网关运行态失效失败",
			slog.String("event", "management_group_gateway_runtime_invalidation_failed"),
			slog.String("reason", GroupCreatedReason),
			slog.Any("error", err),
		)
	}
}

func normalizeDescription(value *string) *string {
	if value == nil {
		return nil
	}
	text := strings.TrimSpace(*value)
	if text == "" {
		return nil
	}
	return &text
}

func normalizeGroupType(value string) (string, error) {
	switch value {
	case "", "personal":
		return "personal", nil
	case "high_concurrency":
		return "high_concurrency", nil
	default:
		return "", &ValidationError{Message: "分组类型无效"}
	}
}

func normalizeSchedulingPolicy(input *SchedulingPolicyInput) (SchedulingPolicy, error) {
	policy := SchedulingPolicy{
		Mode:                            "balanced_fast",
		DefaultSoftConcurrency:          5,
		FastFirstEnabled:                true,
		FallbackOnQueueEnabled:          true,
		BreakAffinityOnSoftLimit:        true,
		BreakAffinityOnQueueWaitMs:      0,
		SlowRequestThresholdMs:          30000,
		FirstOutputSlowThresholdMs:      15000,
		RecentTimeoutWindowSeconds:      120,
		RecentTimeoutPenaltyThreshold:   2,
		MaxQueueWaitMs:                  60000,
		MaxQueueSize:                    1000,
		PerAPIKeyQueueLimit:             1000,
		ClientIPConcurrencyLimit:        0,
		ClientIPConcurrencyOverflowMode: "reject",
		ImageLaneMaxConcurrency:         0,
	}
	if input == nil {
		return policy, nil
	}
	if input.DefaultSoftConcurrency != nil {
		if err := validatePolicyInteger("defaultSoftConcurrency", *input.DefaultSoftConcurrency, 1, 1000000); err != nil {
			return SchedulingPolicy{}, err
		}
		policy.DefaultSoftConcurrency = *input.DefaultSoftConcurrency
	}
	if input.MaxQueueWaitMs != nil {
		if err := validatePolicyInteger("maxQueueWaitMs", *input.MaxQueueWaitMs, 1, 3600000); err != nil {
			return SchedulingPolicy{}, err
		}
		policy.MaxQueueWaitMs = *input.MaxQueueWaitMs
	}
	if input.ClientIPConcurrencyLimit != nil {
		if err := validatePolicyInteger("clientIpConcurrencyLimit", *input.ClientIPConcurrencyLimit, 0, 1000000); err != nil {
			return SchedulingPolicy{}, err
		}
		policy.ClientIPConcurrencyLimit = *input.ClientIPConcurrencyLimit
	}
	if input.ClientIPConcurrencyOverflowMode != nil {
		mode := *input.ClientIPConcurrencyOverflowMode
		if mode != "reject" && mode != "queue" {
			return SchedulingPolicy{}, &ValidationError{Message: "分组调度策略 clientIpConcurrencyOverflowMode 无效"}
		}
		policy.ClientIPConcurrencyOverflowMode = mode
	}
	if input.ImageLaneMaxConcurrency != nil {
		if err := validatePolicyInteger("imageLaneMaxConcurrency", *input.ImageLaneMaxConcurrency, 0, 1000000); err != nil {
			return SchedulingPolicy{}, err
		}
		policy.ImageLaneMaxConcurrency = *input.ImageLaneMaxConcurrency
	}
	return policy, nil
}

func validatePolicyInteger(key string, value int, minimum int, maximum int) error {
	if value < minimum || value > maximum {
		return &ValidationError{
			Message: fmt.Sprintf("分组调度策略 %s 必须在 %d-%d 之间", key, minimum, maximum),
		}
	}
	return nil
}

func optionLimit(limit int) int {
	if limit <= 0 {
		return defaultOptionLimit
	}
	return min(limit, maxOptionLimit)
}

func uniqueStrings(values []string, maxItems int) []string {
	seen := make(map[string]struct{}, len(values))
	output := make([]string, 0, len(values))
	for _, value := range values {
		text := strings.TrimSpace(value)
		if text == "" {
			continue
		}
		if _, exists := seen[text]; exists {
			continue
		}
		seen[text] = struct{}{}
		output = append(output, text)
		if len(output) >= maxItems {
			break
		}
	}
	return output
}

func ownerPermissions() ResourcePermissions {
	return ResourcePermissions{
		CanUse:                 true,
		CanEdit:                true,
		CanDelete:              true,
		CanReturnAuthorization: false,
		CanAuthorize:           true,
		CanViewCredentials:     true,
		CanManageAccounts:      true,
		CanBindToAPIKey:        true,
	}
}

func authorizedGroupPermissions(canBindToAPIKey bool, canReturnAuthorization bool) ResourcePermissions {
	return ResourcePermissions{
		CanUse:                 true,
		CanEdit:                true,
		CanDelete:              false,
		CanReturnAuthorization: canReturnAuthorization,
		CanAuthorize:           false,
		CanViewCredentials:     false,
		CanManageAccounts:      false,
		CanBindToAPIKey:        canBindToAPIKey,
	}
}

func groupPermissions(row port.ManagementGroupOption) ResourcePermissions {
	if groupAccessType(row.AccessType) != "authorized" {
		return ownerPermissions()
	}
	return authorizedGroupPermissions(
		canBindAuthorizedGroup(row.Enabled, row.AuthorizationStatus, row.AuthorizationExpiresAt),
		row.HasActiveManualAuthorizationSource,
	)
}

func groupAccountPermissions(row port.ManagementGroupAccountOption) ResourcePermissions {
	if groupAccessType(row.AccessType) != "authorized" {
		return ownerPermissions()
	}
	return authorizedGroupPermissions(
		canBindAuthorizedGroup(row.Enabled, row.AuthorizationStatus, row.AuthorizationExpiresAt),
		row.HasActiveManualAuthorizationSource,
	)
}

func canBindAuthorizedGroup(enabled bool, status string, expiresAt *time.Time) bool {
	if !enabled || status != "active" {
		return false
	}
	return expiresAt == nil || expiresAt.After(time.Now())
}

func groupAccessType(value string) string {
	if strings.TrimSpace(value) == "authorized" {
		return "authorized"
	}
	return "owner"
}
