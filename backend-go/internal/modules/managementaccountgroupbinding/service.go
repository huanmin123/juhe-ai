package managementaccountgroupbinding

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/modules/accountpagedata"
	"juhe-ai/backend-go/internal/store/port"
)

const (
	GatewayRuntimeReason = "group_account_binding"
	invalidationTimeout  = 5 * time.Second
)

var ErrBindingRejected = errors.New("账户不存在、授权已失效或分组不可用")

type Store interface {
	port.ManagementAccountGroupBinder
}

type RuntimeInvalidator interface {
	InvalidateGatewayRuntime(ctx context.Context, reason string) error
}

type GroupAccountIDsInvalidator interface {
	InvalidateGroupAccountIDsCache(ctx context.Context) error
}

type Options struct {
	Store                      Store
	GranteeReader              accountpagedata.GranteeReader
	PageDataPublisher          accountpagedata.Publisher
	RuntimeInvalidator         RuntimeInvalidator
	GroupAccountIDsInvalidator GroupAccountIDsInvalidator
	Logger                     *slog.Logger
	Now                        func() time.Time
}

type Service struct {
	store                      Store
	granteeReader              accountpagedata.GranteeReader
	pageDataPublisher          accountpagedata.Publisher
	runtimeInvalidator         RuntimeInvalidator
	groupAccountIDsInvalidator GroupAccountIDsInvalidator
	logger                     *slog.Logger
	now                        func() time.Time
}

type BindInput struct {
	ActorSystemAccountID string
	ActorRole            string
	SystemAccountID      string
	SelfOnly             bool
	AccountID            string
	GroupID              string
}

type Account struct {
	ID                        string `json:"id"`
	SystemAccountID           string `json:"systemAccountId,omitempty"`
	OwnerSystemAccountID      string `json:"ownerSystemAccountId,omitempty"`
	Name                      string `json:"name"`
	ProviderCode              string `json:"providerCode"`
	ProviderProtocolProfileID string `json:"providerProtocolProfileId,omitempty"`
	ProtocolCode              string `json:"protocolCode,omitempty"`
	ProtocolVersion           string `json:"protocolVersion,omitempty"`
	Type                      string `json:"type"`
	Status                    string `json:"status"`
	ClientCompatibility       string `json:"clientCompatibility,omitempty"`
	BoundGroupID              string `json:"boundGroupId"`
	BoundGroupName            string `json:"boundGroupName"`
	Schedulable               bool   `json:"schedulable"`
	ConcurrencyLimit          int    `json:"concurrencyLimit"`
	Priority                  int    `json:"priority"`
	SuperPriorityEnabled      bool   `json:"superPriorityEnabled"`
	FallbackEnabled           bool   `json:"fallbackEnabled"`
	HealthCheckModel          string `json:"healthCheckModel"`
	AccessType                string `json:"accessType"`
}

type BindResult struct {
	Account         Account
	PreviousGroupID string
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
		granteeReader:              opts.GranteeReader,
		pageDataPublisher:          opts.PageDataPublisher,
		runtimeInvalidator:         opts.RuntimeInvalidator,
		groupAccountIDsInvalidator: opts.GroupAccountIDsInvalidator,
		logger:                     logger,
		now:                        now,
	}
}

func (s *Service) Bind(ctx context.Context, input BindInput) (BindResult, error) {
	if s.store == nil {
		return BindResult{}, fmt.Errorf("management account group binding store is required")
	}
	accountID := strings.TrimSpace(input.AccountID)
	groupID := strings.TrimSpace(input.GroupID)
	if accountID == "" || groupID == "" {
		return BindResult{}, ErrBindingRejected
	}
	effectiveSystemAccountID, canAccessAll := bindingScope(input)
	if strings.TrimSpace(input.ActorSystemAccountID) == "" {
		return BindResult{}, ErrBindingRejected
	}

	saved, ok, err := s.store.BindManagementAccountGroup(ctx, port.ManagementAccountGroupBindingInput{
		AccountID:                accountID,
		GroupID:                  groupID,
		EffectiveSystemAccountID: effectiveSystemAccountID,
		CanAccessAll:             canAccessAll,
		UpdatedAt:                s.now().UTC(),
	})
	if err != nil {
		return BindResult{}, err
	}
	if !ok {
		return BindResult{}, ErrBindingRejected
	}

	result := BindResult{
		Account: Account{
			ID:                        saved.Account.ID,
			SystemAccountID:           saved.Account.SystemAccountID,
			OwnerSystemAccountID:      saved.Account.SystemAccountID,
			Name:                      saved.Account.Name,
			ProviderCode:              saved.Account.ProviderCode,
			ProviderProtocolProfileID: saved.Account.ProviderProtocolProfileID,
			ProtocolCode:              saved.Account.ProtocolCode,
			ProtocolVersion:           saved.Account.ProtocolVersion,
			Type:                      saved.Account.Type,
			Status:                    saved.Account.Status,
			ClientCompatibility:       saved.Account.ClientCompatibility,
			BoundGroupID:              saved.Account.BoundGroupID,
			BoundGroupName:            saved.Account.BoundGroupName,
			Schedulable:               saved.Account.Schedulable,
			ConcurrencyLimit:          saved.Account.ConcurrencyLimit,
			Priority:                  saved.Account.Priority,
			SuperPriorityEnabled:      saved.Account.SuperPriorityEnabled,
			FallbackEnabled:           saved.Account.FallbackEnabled,
			HealthCheckModel:          saved.Account.HealthCheckModel,
			AccessType:                "owner",
		},
		PreviousGroupID: saved.PreviousGroupID,
	}
	s.publishPageData(ctx, result.Account)
	s.invalidateGroupAccountIDs(ctx)
	s.invalidateRuntime(ctx)
	return result, nil
}

func bindingScope(input BindInput) (string, bool) {
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

func (s *Service) publishPageData(ctx context.Context, account Account) {
	if s.pageDataPublisher == nil {
		return
	}
	lookupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), invalidationTimeout)
	owners, allScopes, err := accountpagedata.ResolveOwners(lookupCtx, s.granteeReader, account.ID, []string{account.SystemAccountID})
	cancel()
	if err != nil {
		s.logger.WarnContext(context.WithoutCancel(ctx), "账户绑定分组后页面数据 owner 查询失败", "accountId", account.ID, "error", err)
	}
	publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), invalidationTimeout)
	defer cancel()
	if err := s.pageDataPublisher.PublishAccountStaticChange(publishCtx, accountpagedata.ChangeInput{
		AccountID:             account.ID,
		Operation:             accountpagedata.OperationUpsert,
		OwnerSystemAccountIDs: owners,
		FieldMask:             []string{"boundGroupId"},
		MembershipChanged:     true,
		FilterChanged:         true,
		PageChanged:           true,
		AllScopes:             allScopes,
	}); err != nil {
		s.logger.WarnContext(context.WithoutCancel(ctx), "账户绑定分组后页面数据失效失败", "accountId", account.ID, "error", err)
	}
}

func (s *Service) invalidateGroupAccountIDs(ctx context.Context) {
	if s.groupAccountIDsInvalidator == nil {
		return
	}
	invalidationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), invalidationTimeout)
	defer cancel()
	if err := s.groupAccountIDsInvalidator.InvalidateGroupAccountIDsCache(invalidationCtx); err != nil {
		s.logger.WarnContext(context.WithoutCancel(ctx), "账户绑定分组后账号 ID 缓存失效失败", "error", err)
	}
}

func (s *Service) invalidateRuntime(ctx context.Context) {
	if s.runtimeInvalidator == nil {
		return
	}
	invalidationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), invalidationTimeout)
	defer cancel()
	if err := s.runtimeInvalidator.InvalidateGatewayRuntime(invalidationCtx, GatewayRuntimeReason); err != nil {
		s.logger.WarnContext(context.WithoutCancel(ctx), "账户绑定分组后网关运行态失效失败", "reason", GatewayRuntimeReason, "error", err)
	}
}
