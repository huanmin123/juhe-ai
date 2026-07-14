package managementapikeys

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/apikeysecret"
	"juhe-ai/backend-go/internal/store/port"
)

const (
	apiKeyCreatedReason             = "api_key_created"
	apiKeyCreateInvalidationTimeout = 5 * time.Second
)

var (
	ErrAPIKeyCreateInvalid        = errors.New("API Key 创建参数无效")
	ErrAPIKeyRouteStrategyMissing = errors.New("API Key 绑定的策略路由不存在或不属于当前用户")
	ErrAPIKeyRouteStrategyOff     = errors.New("API Key 只能绑定启用状态的策略路由")
)

type apiKeyNameExistsError struct {
	name string
}

type apiKeyCreateValidationError struct {
	cause error
}

func (e apiKeyCreateValidationError) Error() string {
	return e.cause.Error()
}

func (e apiKeyCreateValidationError) Unwrap() error {
	return e.cause
}

func (e apiKeyNameExistsError) Error() string {
	return "API Key 名称已存在：" + e.name
}

func NewAPIKeyNameExistsError(name string) error {
	return apiKeyNameExistsError{name: strings.TrimSpace(name)}
}

func APIKeyNameExistsMessage(err error) (string, bool) {
	var target apiKeyNameExistsError
	if !errors.As(err, &target) {
		return "", false
	}
	return target.Error(), true
}

func IsAPIKeyCreateValidationError(err error) bool {
	if errors.Is(err, ErrAPIKeyCreateInvalid) {
		return true
	}
	var target apiKeyCreateValidationError
	return errors.As(err, &target)
}

func newAPIKeyCreateValidationError(err error) error {
	if err == nil || IsAPIKeyCreateValidationError(err) {
		return err
	}
	return apiKeyCreateValidationError{cause: err}
}

type CreateInput struct {
	ActorSystemAccountID string
	ActorRole            string
	SystemAccountID      string
	SelfOnly             bool
	Name                 string
	Description          any
	RouteStrategyID      string
	Status               string
	ExpiresAt            any
	QuotaLimits          any
	AvailabilitySchedule any
}

type CreateResult struct {
	ListItem
	Key                  string `json:"key"`
	OwnerSystemAccountID string `json:"-"`
}

type normalizedCreateInput struct {
	ownerSystemAccountID            string
	includeOwner                    bool
	name                            string
	description                     *string
	routeStrategyID                 string
	status                          string
	expiresAt                       *time.Time
	quotaLimitsJSON                 *string
	hourlyQuotaHours                *int
	availabilityScheduleJSON        *string
	availabilityScheduleNextCheckAt *time.Time
	now                             time.Time
}

func (s *Service) Create(ctx context.Context, input CreateInput) (CreateResult, error) {
	if s.creator == nil {
		return CreateResult{}, fmt.Errorf("management API Key creator is required")
	}
	normalized, err := s.normalizeCreateInput(ctx, input)
	if err != nil {
		return CreateResult{}, err
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		result, err := s.createOnce(ctx, normalized)
		if errors.Is(err, port.ErrManagementAPIKeyHashExists) {
			lastErr = err
			continue
		}
		return result, err
	}
	return CreateResult{}, lastErr
}

func (s *Service) createOnce(
	ctx context.Context,
	input normalizedCreateInput,
) (CreateResult, error) {
	key, err := s.newSecret()
	if err != nil {
		return CreateResult{}, fmt.Errorf("generate management API Key secret: %w", err)
	}
	if key == "" {
		return CreateResult{}, fmt.Errorf("generate management API Key secret: empty secret")
	}
	encrypted, err := s.codec.EncryptJSON(map[string]any{"key": key})
	if err != nil {
		return CreateResult{}, fmt.Errorf("encrypt management API Key secret: %w", err)
	}

	row, err := s.creator.CreateManagementAPIKey(ctx, port.ManagementAPIKeyCreateInput{
		ID:                              s.newID("key"),
		SystemAccountID:                 input.ownerSystemAccountID,
		RouteStrategyID:                 input.routeStrategyID,
		Name:                            input.name,
		Description:                     input.description,
		KeyHash:                         apikeysecret.Hash(key),
		KeyPrefix:                       apikeysecret.Prefix(key),
		KeySuffix:                       apikeysecret.Suffix(key),
		KeySecretEncrypted:              encrypted,
		Status:                          input.status,
		IsDefault:                       false,
		ExpiresAt:                       input.expiresAt,
		QuotaLimitsJSON:                 input.quotaLimitsJSON,
		HourlyQuotaHours:                input.hourlyQuotaHours,
		AvailabilityScheduleJSON:        input.availabilityScheduleJSON,
		AvailabilityScheduleNextCheckAt: input.availabilityScheduleNextCheckAt,
		CreatedAt:                       input.now,
		UpdatedAt:                       input.now,
	})
	if err != nil {
		switch {
		case errors.Is(err, port.ErrManagementAPIKeyRouteStrategyNotFound):
			return CreateResult{}, ErrAPIKeyRouteStrategyMissing
		case errors.Is(err, port.ErrManagementAPIKeyRouteStrategyDisabled):
			return CreateResult{}, ErrAPIKeyRouteStrategyOff
		case errors.Is(err, port.ErrManagementAPIKeyNameExists):
			return CreateResult{}, NewAPIKeyNameExistsError(input.name)
		default:
			return CreateResult{}, err
		}
	}

	item, err := listItem(row, port.ManagementAccountUsageSummary{}, input.includeOwner)
	if err != nil {
		return CreateResult{}, err
	}
	if s.invalidator != nil {
		invalidationCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			apiKeyCreateInvalidationTimeout,
		)
		defer cancel()
		_ = s.invalidator.InvalidateAPIKeyLookupCache(
			invalidationCtx,
			row.ID,
			apiKeyCreatedReason,
		)
		_ = s.invalidator.InvalidateGatewayRuntime(invalidationCtx, apiKeyCreatedReason)
		_ = s.invalidator.InvalidateAPIKeyQuotaChanged(
			invalidationCtx,
			row.ID,
			apiKeyCreatedReason,
		)
	}
	return CreateResult{
		ListItem:             item,
		Key:                  key,
		OwnerSystemAccountID: row.SystemAccountID,
	}, nil
}

func (s *Service) normalizeCreateInput(
	ctx context.Context,
	input CreateInput,
) (normalizedCreateInput, error) {
	ownerSystemAccountID, includeOwner, err := createScope(input)
	if err != nil {
		return normalizedCreateInput{}, newAPIKeyCreateValidationError(err)
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return normalizedCreateInput{}, newAPIKeyCreateValidationError(
			errors.New("API Key 名称不能为空"),
		)
	}
	description, err := normalizeMutationDescription(input.Description)
	if err != nil {
		return normalizedCreateInput{}, newAPIKeyCreateValidationError(err)
	}
	routeStrategyID := strings.TrimSpace(input.RouteStrategyID)
	status, err := normalizeMutationStatus(input.Status, "active")
	if err != nil {
		return normalizedCreateInput{}, newAPIKeyCreateValidationError(err)
	}
	expiresAt, err := normalizeMutationExpiresAt(input.ExpiresAt)
	if err != nil {
		return normalizedCreateInput{}, newAPIKeyCreateValidationError(err)
	}
	_, quotaLimitsJSON, hourlyQuotaHours, err := normalizeMutationQuotaLimits(input.QuotaLimits)
	if err != nil {
		return normalizedCreateInput{}, newAPIKeyCreateValidationError(err)
	}
	now := s.now().UTC()
	var scheduleJSON *string
	var nextCheckAt *time.Time
	if input.AvailabilitySchedule != nil {
		var allowed bool
		_, scheduleJSON, nextCheckAt, allowed, err = normalizeMutationAvailabilitySchedule(
			ctx,
			s.usageStatsTimezoneReader,
			input.AvailabilitySchedule,
			now,
		)
		if err != nil {
			return normalizedCreateInput{}, newAPIKeyCreateValidationError(err)
		}
		status = "disabled"
		if allowed {
			status = "active"
		}
	}
	return normalizedCreateInput{
		ownerSystemAccountID:            ownerSystemAccountID,
		includeOwner:                    includeOwner,
		name:                            name,
		description:                     description,
		routeStrategyID:                 routeStrategyID,
		status:                          status,
		expiresAt:                       expiresAt,
		quotaLimitsJSON:                 quotaLimitsJSON,
		hourlyQuotaHours:                hourlyQuotaHours,
		availabilityScheduleJSON:        scheduleJSON,
		availabilityScheduleNextCheckAt: nextCheckAt,
		now:                             now,
	}, nil
}

func createScope(input CreateInput) (string, bool, error) {
	actorSystemAccountID := strings.TrimSpace(input.ActorSystemAccountID)
	if actorSystemAccountID == "" {
		return "", false, ErrAPIKeyCreateInvalid
	}
	if input.SelfOnly || !isAdminRole(input.ActorRole) {
		return actorSystemAccountID, false, nil
	}
	targetSystemAccountID := strings.TrimSpace(input.SystemAccountID)
	if targetSystemAccountID == "" || targetSystemAccountID == "all" {
		targetSystemAccountID = actorSystemAccountID
	}
	return targetSystemAccountID, true, nil
}
