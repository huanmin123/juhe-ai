package managementapikeys

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	apiKeyUpdatedReason             = "api_key_updated"
	apiKeyUpdateInvalidationTimeout = 5 * time.Second
)

var (
	ErrAPIKeyUpdateInvalid      = errors.New("API Key 更新参数无效")
	ErrAPIKeyDefaultRouteChange = errors.New("默认 API Key 不允许更换策略路由")
)

type apiKeyUpdateValidationError struct {
	cause error
}

func (e apiKeyUpdateValidationError) Error() string {
	return e.cause.Error()
}

func (e apiKeyUpdateValidationError) Unwrap() error {
	return e.cause
}

func IsAPIKeyUpdateValidationError(err error) bool {
	if errors.Is(err, ErrAPIKeyUpdateInvalid) {
		return true
	}
	var target apiKeyUpdateValidationError
	return errors.As(err, &target)
}

func newAPIKeyUpdateValidationError(err error) error {
	if err == nil || IsAPIKeyUpdateValidationError(err) {
		return err
	}
	return apiKeyUpdateValidationError{cause: err}
}

type UpdateInput struct {
	ActorSystemAccountID    string
	ActorRole               string
	SystemAccountID         string
	SelfOnly                bool
	APIKeyID                string
	HasName                 bool
	Name                    string
	HasDescription          bool
	Description             any
	HasRouteStrategyID      bool
	RouteStrategyID         string
	HasStatus               bool
	Status                  string
	HasExpiresAt            bool
	ExpiresAt               any
	HasQuotaLimits          bool
	QuotaLimits             any
	HasAvailabilitySchedule bool
	AvailabilitySchedule    any
}

type UpdateResult struct {
	Before               ListItem
	After                ListItem
	OwnerSystemAccountID string
	Committed            bool
}

type normalizedUpdateInput struct {
	ownerSystemAccountID            string
	includeOwner                    bool
	apiKeyID                        string
	hasName                         bool
	name                            string
	hasDescription                  bool
	description                     *string
	hasRouteStrategyID              bool
	routeStrategyID                 string
	hasStatus                       bool
	status                          string
	hasExpiresAt                    bool
	expiresAt                       *time.Time
	hasQuotaLimits                  bool
	quotaLimitsJSON                 *string
	hourlyQuotaHours                *int
	hasAvailabilitySchedule         bool
	availabilityScheduleJSON        *string
	availabilityScheduleNextCheckAt *time.Time
	now                             time.Time
}

func (s *Service) Update(ctx context.Context, input UpdateInput) (UpdateResult, error) {
	if s.updater == nil {
		return UpdateResult{}, fmt.Errorf("management API Key updater is required")
	}
	if s.store == nil {
		return UpdateResult{}, fmt.Errorf("management API Key usage reader is required")
	}
	if s.invalidator == nil {
		return UpdateResult{}, fmt.Errorf("management API Key cache invalidator is required")
	}

	normalized, err := s.normalizeUpdateInput(ctx, input)
	if err != nil {
		return UpdateResult{}, err
	}
	stored, err := s.updater.UpdateManagementAPIKey(ctx, port.ManagementAPIKeyUpdateInput{
		APIKeyID:                        normalized.apiKeyID,
		OwnerSystemAccountID:            normalized.ownerSystemAccountID,
		HasName:                         normalized.hasName,
		Name:                            normalized.name,
		HasDescription:                  normalized.hasDescription,
		Description:                     normalized.description,
		HasRouteStrategyID:              normalized.hasRouteStrategyID,
		RouteStrategyID:                 normalized.routeStrategyID,
		HasStatus:                       normalized.hasStatus,
		Status:                          normalized.status,
		HasExpiresAt:                    normalized.hasExpiresAt,
		ExpiresAt:                       normalized.expiresAt,
		HasQuotaLimits:                  normalized.hasQuotaLimits,
		QuotaLimitsJSON:                 normalized.quotaLimitsJSON,
		HourlyQuotaHours:                normalized.hourlyQuotaHours,
		HasAvailabilitySchedule:         normalized.hasAvailabilitySchedule,
		AvailabilityScheduleJSON:        normalized.availabilityScheduleJSON,
		AvailabilityScheduleNextCheckAt: normalized.availabilityScheduleNextCheckAt,
		UpdatedAt:                       normalized.now,
	})
	if err != nil {
		switch {
		case errors.Is(err, port.ErrManagementAPIKeyNotFound):
			return UpdateResult{}, ErrAPIKeyNotFound
		case errors.Is(err, port.ErrManagementAPIKeyRouteStrategyNotFound):
			return UpdateResult{}, ErrAPIKeyRouteStrategyMissing
		case errors.Is(err, port.ErrManagementAPIKeyRouteStrategyDisabled):
			return UpdateResult{}, ErrAPIKeyRouteStrategyOff
		case errors.Is(err, port.ErrManagementAPIKeyNameExists):
			return UpdateResult{}, NewAPIKeyNameExistsError(normalized.name)
		case errors.Is(err, port.ErrManagementAPIKeyDefaultRouteChange):
			return UpdateResult{}, ErrAPIKeyDefaultRouteChange
		default:
			return UpdateResult{}, err
		}
	}

	result, err := updateResultFromRows(stored, normalized.includeOwner)
	if err != nil {
		result.Committed = true
		result.OwnerSystemAccountID = stored.After.SystemAccountID
		return result, err
	}
	result.Committed = true

	validationCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		apiKeyUpdateInvalidationTimeout,
	)
	defer cancel()
	if err := s.invalidator.InvalidateAPIKeyValidationCache(validationCtx); err != nil {
		return result, fmt.Errorf(
			"invalidate management API Key validation cache after update: %w",
			err,
		)
	}
	_ = s.invalidator.InvalidateGatewayRuntime(ctx, apiKeyUpdatedReason)
	_ = s.invalidator.InvalidateAPIKeyQuotaChanged(
		ctx,
		stored.After.ID,
		apiKeyUpdatedReason,
	)

	usageRows, err := s.store.ListManagementAPIKeyUsageTotals(
		ctx,
		[]port.ManagementAPIKeyUsageScope{{
			SystemAccountID: stored.After.SystemAccountID,
			APIKeyID:        stored.After.ID,
		}},
	)
	if err != nil {
		return result, fmt.Errorf("load management API Key usage after update: %w", err)
	}
	for _, row := range usageRows {
		if row.SystemAccountID == stored.After.SystemAccountID &&
			row.APIKeyID == stored.After.ID {
			result.Before.Usage = row.Usage
			result.After.Usage = row.Usage
			break
		}
	}
	return result, nil
}

func (s *Service) normalizeUpdateInput(
	ctx context.Context,
	input UpdateInput,
) (normalizedUpdateInput, error) {
	ownerSystemAccountID, includeOwner, apiKeyID, err := updateScope(input)
	if err != nil {
		return normalizedUpdateInput{}, newAPIKeyUpdateValidationError(err)
	}
	if !hasUpdateContent(input) {
		return normalizedUpdateInput{}, newAPIKeyUpdateValidationError(
			errors.New("请提供要修改的 API Key 内容"),
		)
	}

	normalized := normalizedUpdateInput{
		ownerSystemAccountID:    ownerSystemAccountID,
		includeOwner:            includeOwner,
		apiKeyID:                apiKeyID,
		hasName:                 input.HasName,
		hasDescription:          input.HasDescription,
		hasRouteStrategyID:      input.HasRouteStrategyID,
		routeStrategyID:         strings.TrimSpace(input.RouteStrategyID),
		hasStatus:               input.HasStatus,
		hasExpiresAt:            input.HasExpiresAt,
		hasQuotaLimits:          input.HasQuotaLimits,
		hasAvailabilitySchedule: input.HasAvailabilitySchedule,
		now:                     s.now().UTC(),
	}
	if input.HasName {
		normalized.name = strings.TrimSpace(input.Name)
		if normalized.name == "" {
			return normalizedUpdateInput{}, newAPIKeyUpdateValidationError(
				errors.New("API Key 名称不能为空"),
			)
		}
	}
	if input.HasDescription {
		normalized.description, err = normalizeMutationDescription(input.Description)
		if err != nil {
			return normalizedUpdateInput{}, newAPIKeyUpdateValidationError(err)
		}
	}
	if input.HasStatus {
		normalized.status, err = normalizeMutationStatus(input.Status, "")
		if err != nil {
			return normalizedUpdateInput{}, newAPIKeyUpdateValidationError(err)
		}
	}
	if input.HasExpiresAt {
		normalized.expiresAt, err = normalizeMutationExpiresAt(input.ExpiresAt)
		if err != nil {
			return normalizedUpdateInput{}, newAPIKeyUpdateValidationError(err)
		}
	}
	if input.HasQuotaLimits {
		_, normalized.quotaLimitsJSON, normalized.hourlyQuotaHours, err =
			normalizeMutationQuotaLimits(input.QuotaLimits)
		if err != nil {
			return normalizedUpdateInput{}, newAPIKeyUpdateValidationError(err)
		}
	}
	if input.HasAvailabilitySchedule && input.AvailabilitySchedule != nil {
		var allowed bool
		_, normalized.availabilityScheduleJSON,
			normalized.availabilityScheduleNextCheckAt,
			allowed,
			err = normalizeMutationAvailabilitySchedule(
			ctx,
			s.usageStatsTimezoneReader,
			input.AvailabilitySchedule,
			normalized.now,
		)
		if err != nil {
			return normalizedUpdateInput{}, newAPIKeyUpdateValidationError(err)
		}
		normalized.hasStatus = true
		normalized.status = "disabled"
		if allowed {
			normalized.status = "active"
		}
	}
	return normalized, nil
}

func updateScope(input UpdateInput) (string, bool, string, error) {
	actorSystemAccountID := strings.TrimSpace(input.ActorSystemAccountID)
	apiKeyID := strings.TrimSpace(input.APIKeyID)
	if actorSystemAccountID == "" || apiKeyID == "" {
		return "", false, "", ErrAPIKeyUpdateInvalid
	}
	if input.SelfOnly || !isAdminRole(input.ActorRole) {
		return actorSystemAccountID, false, apiKeyID, nil
	}
	ownerSystemAccountID := strings.TrimSpace(input.SystemAccountID)
	if ownerSystemAccountID == "all" {
		ownerSystemAccountID = ""
	}
	return ownerSystemAccountID, true, apiKeyID, nil
}

func hasUpdateContent(input UpdateInput) bool {
	return input.HasName ||
		input.HasDescription ||
		input.HasRouteStrategyID ||
		input.HasStatus ||
		input.HasExpiresAt ||
		input.HasQuotaLimits ||
		input.HasAvailabilitySchedule
}

func updateResultFromRows(
	stored port.ManagementAPIKeyUpdateResult,
	includeOwner bool,
) (UpdateResult, error) {
	before, err := listItem(
		stored.Before,
		port.ManagementAccountUsageSummary{},
		includeOwner,
	)
	if err != nil {
		return UpdateResult{}, err
	}
	after, err := listItem(
		stored.After,
		port.ManagementAccountUsageSummary{},
		includeOwner,
	)
	if err != nil {
		return UpdateResult{Before: before}, err
	}
	return UpdateResult{
		Before:               before,
		After:                after,
		OwnerSystemAccountID: stored.After.SystemAccountID,
	}, nil
}
