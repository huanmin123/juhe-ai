package managementaccountforceactivate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/modules/managementaccountdetails"
	"juhe-ai/backend-go/internal/store/port"
)

var (
	ErrNotFound      = errors.New("management account not found")
	ErrAuthorized    = errors.New("authorized account cannot be force activated")
	ErrInvalidStatus = errors.New("management account is not pending_test")
	ErrStateChanged  = errors.New("account state changed")
	ErrConfirmation  = errors.New("account availability confirmation is required")
)

type DetailReader interface {
	Get(context.Context, managementaccountdetails.Input, managementaccountdetails.Level) (map[string]any, bool, error)
}

type RuntimeAvailabilityClearer interface {
	ClearAccountRuntimeAvailability(context.Context, string) error
}

type GatewayRuntimeInvalidator interface {
	InvalidateGatewayRuntime(context.Context, string) error
}

type ServiceOptions struct {
	Store              port.ManagementAccountForceActivator
	Details            DetailReader
	RuntimeClearer     RuntimeAvailabilityClearer
	GatewayInvalidator GatewayRuntimeInvalidator
	Logger             *slog.Logger
	Now                func() time.Time
}

type Service struct {
	store              port.ManagementAccountForceActivator
	details            DetailReader
	runtimeClearer     RuntimeAvailabilityClearer
	gatewayInvalidator GatewayRuntimeInvalidator
	logger             *slog.Logger
	now                func() time.Time
}

type Input struct {
	AccountID       string
	SystemAccountID string
	Acknowledged    bool
}

type Result struct {
	Before        map[string]any
	After         map[string]any
	OwnerSystemID string
	BeforeStatus  string
	AfterStatus   string
}

func NewService(opts ServiceOptions) *Service {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{store: opts.Store, details: opts.Details, runtimeClearer: opts.RuntimeClearer,
		gatewayInvalidator: opts.GatewayInvalidator, logger: logger, now: now}
}

func (s *Service) ForceActivate(ctx context.Context, input Input) (Result, error) {
	if !input.Acknowledged {
		return Result{}, ErrConfirmation
	}
	if s.store == nil || s.details == nil {
		return Result{}, fmt.Errorf("management account force activate dependencies are required")
	}
	accountID := strings.TrimSpace(input.AccountID)
	if accountID == "" {
		return Result{}, ErrNotFound
	}
	systemID := strings.TrimSpace(input.SystemAccountID)
	before, found, err := s.details.Get(ctx, managementaccountdetails.Input{AccountID: accountID, SystemAccountID: systemID}, managementaccountdetails.LevelAdvanced)
	if err != nil {
		return Result{}, err
	}
	if !found {
		return Result{}, ErrNotFound
	}
	if strings.TrimSpace(stringValue(before, "accessType")) == "authorized" {
		return Result{}, ErrAuthorized
	}
	if stringValue(before, "status") != "pending_test" {
		return Result{}, ErrInvalidStatus
	}
	ownerID := firstText(stringValue(before, "ownerSystemAccountId"), stringValue(before, "systemAccountId"), systemID)
	if ownerID == "" {
		return Result{}, fmt.Errorf("account owner is missing")
	}
	now := s.now().UTC()
	mutation, changed, err := s.store.ForceActivatePendingAccount(ctx, port.ManagementAccountForceActivateInput{
		AccountID: accountID, OwnerSystemID: ownerID, ConfigRevision: intValue(before["configRevision"]),
		Now: now, Schedule: mapValue(before["availabilitySchedule"]),
	})
	if err != nil {
		return Result{}, err
	}
	if !changed {
		return Result{}, ErrStateChanged
	}
	after, found, err := s.details.Get(ctx, managementaccountdetails.Input{AccountID: accountID, SystemAccountID: systemID}, managementaccountdetails.LevelAdvanced)
	if err != nil {
		return Result{}, err
	}
	if !found {
		return Result{}, fmt.Errorf("account disappeared after force activation")
	}
	s.afterCommit(ctx, accountID, ownerID, after)
	return Result{Before: before, After: after, OwnerSystemID: ownerID,
		BeforeStatus: mutation.BeforeStatus, AfterStatus: mutation.AfterStatus}, nil
}

func (s *Service) afterCommit(ctx context.Context, accountID, ownerID string, account map[string]any) {
	postCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if s.runtimeClearer != nil {
		if err := s.runtimeClearer.ClearAccountRuntimeAvailability(postCtx, accountID); err != nil {
			s.logger.WarnContext(postCtx, "account runtime availability clear failed", "accountId", accountID, "error", err)
		}
	}
	if s.gatewayInvalidator != nil {
		if err := s.gatewayInvalidator.InvalidateGatewayRuntime(postCtx, "account_pending_force_activated"); err != nil {
			s.logger.WarnContext(postCtx, "gateway runtime invalidation failed", "accountId", accountID, "error", err)
		}
	}
}

func stringValue(value map[string]any, key string) string {
	text, _ := value[key].(string)
	return strings.TrimSpace(text)
}
func mapValue(value any) map[string]any { result, _ := value.(map[string]any); return result }
func intValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(typed))
		return n
	}
	return 0
}
func firstText(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
