package managementaccountdelete

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	AccountDeletedReason = "account_deleted"
	invalidationTimeout  = 5 * time.Second
)

var (
	ErrAccountNotFound       = errors.New("账户不存在")
	ErrAuthorizationInstance = errors.New("授权账户请使用归还操作")
)

type AccountLookupInvalidator interface {
	InvalidateAccountLookupCache(ctx context.Context, accountID string) error
}

type GroupAccountIDsInvalidator interface {
	InvalidateGroupAccountIDsCache(ctx context.Context) error
}

type AuthorizationInvalidator interface {
	InvalidateAuthorizationChanged(ctx context.Context, reason string) error
}

type GatewayRuntimeInvalidator interface {
	InvalidateGatewayRuntime(ctx context.Context, reason string) error
}

type Options struct {
	Store                      port.ManagementAccountDeleter
	AccountLookupInvalidator   AccountLookupInvalidator
	GroupAccountIDsInvalidator GroupAccountIDsInvalidator
	AuthorizationInvalidator   AuthorizationInvalidator
	GatewayRuntimeInvalidator  GatewayRuntimeInvalidator
	Logger                     *slog.Logger
	Now                        func() time.Time
}

type Service struct {
	store                      port.ManagementAccountDeleter
	accountLookupInvalidator   AccountLookupInvalidator
	groupAccountIDsInvalidator GroupAccountIDsInvalidator
	authorizationInvalidator   AuthorizationInvalidator
	gatewayRuntimeInvalidator  GatewayRuntimeInvalidator
	logger                     *slog.Logger
	now                        func() time.Time
}

type DeleteInput struct {
	ActorSystemAccountID string
	ActorRole            string
	SystemAccountID      string
	SelfOnly             bool
	AccountID            string
}

type DeleteResult struct {
	Before            port.ManagementAccountDeleteSummary
	DeletedAccountIDs []string
}

func NewService(opts Options) *Service {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		store:                      opts.Store,
		accountLookupInvalidator:   opts.AccountLookupInvalidator,
		groupAccountIDsInvalidator: opts.GroupAccountIDsInvalidator,
		authorizationInvalidator:   opts.AuthorizationInvalidator,
		gatewayRuntimeInvalidator:  opts.GatewayRuntimeInvalidator,
		logger:                     logger,
		now:                        now,
	}
}

func (s *Service) Delete(ctx context.Context, input DeleteInput) (DeleteResult, error) {
	if s.store == nil {
		return DeleteResult{}, fmt.Errorf("management account delete store is required")
	}
	actorSystemAccountID := strings.TrimSpace(input.ActorSystemAccountID)
	accountID := strings.TrimSpace(input.AccountID)
	if actorSystemAccountID == "" || accountID == "" {
		return DeleteResult{}, ErrAccountNotFound
	}
	effectiveSystemAccountID, canAccessAll := deleteScope(input)
	deleted, err := s.store.DeleteManagementAccount(ctx, port.ManagementAccountDeleteInput{
		AccountID:                accountID,
		EffectiveSystemAccountID: effectiveSystemAccountID,
		CanAccessAll:             canAccessAll,
		DeletedBy:                actorSystemAccountID,
		DeletedAt:                s.now().UTC(),
	})
	if err != nil {
		switch {
		case errors.Is(err, port.ErrManagementAccountDeleteNotFound):
			return DeleteResult{}, ErrAccountNotFound
		case errors.Is(err, port.ErrManagementAccountDeleteAuthorizationInstance):
			return DeleteResult{}, ErrAuthorizationInstance
		default:
			return DeleteResult{}, err
		}
	}

	s.invalidateCaches(ctx, deleted.DeletedAccountIDs)
	return DeleteResult{Before: deleted.Before, DeletedAccountIDs: deleted.DeletedAccountIDs}, nil
}

func deleteScope(input DeleteInput) (string, bool) {
	actorSystemAccountID := strings.TrimSpace(input.ActorSystemAccountID)
	if input.SelfOnly || !isAdminRole(input.ActorRole) {
		return actorSystemAccountID, false
	}
	systemAccountID := strings.TrimSpace(input.SystemAccountID)
	if systemAccountID == "all" {
		systemAccountID = ""
	}
	return systemAccountID, systemAccountID == ""
}

func isAdminRole(role string) bool {
	switch strings.TrimSpace(role) {
	case "admin", "super_admin":
		return true
	default:
		return false
	}
}

func (s *Service) invalidateCaches(ctx context.Context, accountIDs []string) {
	for _, accountID := range accountIDs {
		if s.accountLookupInvalidator == nil {
			break
		}
		s.runInvalidation(ctx, "账户删除后账户查找缓存失效失败", func(invalidationCtx context.Context) error {
			return s.accountLookupInvalidator.InvalidateAccountLookupCache(invalidationCtx, accountID)
		}, "accountId", accountID)
	}
	if s.groupAccountIDsInvalidator != nil {
		s.runInvalidation(ctx, "账户删除后分组账户 ID 缓存失效失败", s.groupAccountIDsInvalidator.InvalidateGroupAccountIDsCache)
	}
	if s.authorizationInvalidator != nil {
		s.runInvalidation(ctx, "账户删除后授权缓存失效失败", func(invalidationCtx context.Context) error {
			return s.authorizationInvalidator.InvalidateAuthorizationChanged(invalidationCtx, AccountDeletedReason)
		})
	}
	if s.gatewayRuntimeInvalidator != nil {
		s.runInvalidation(ctx, "账户删除后网关运行态失效失败", func(invalidationCtx context.Context) error {
			return s.gatewayRuntimeInvalidator.InvalidateGatewayRuntime(invalidationCtx, AccountDeletedReason)
		})
	}
}

func (s *Service) runInvalidation(ctx context.Context, message string, invalidate func(context.Context) error, attrs ...any) {
	invalidationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), invalidationTimeout)
	defer cancel()
	if err := invalidate(invalidationCtx); err != nil {
		attrs = append(attrs, "error", err)
		s.logger.WarnContext(context.WithoutCancel(ctx), message, attrs...)
	}
}
