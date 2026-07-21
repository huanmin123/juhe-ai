package managementaccountauthorizeddispatch

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

const GatewayRuntimeReason = "authorized_binding_dispatch"

var (
	ErrInvalid     = errors.New("授权账户调度参数无效")
	ErrNotFound    = errors.New("授权账户不存在或尚未绑定分组")
	ErrPendingTest = errors.New("待检查账户需等待后台健康检查通过后才能参与调度")
	ErrExclusive   = errors.New("超级优先和降级备用不能同时开启")
	ErrUnavailable = errors.New("授权账户当前不可用，不能启用调度标记")
)

type RuntimeTarget struct {
	AccountID              string
	SystemAccountID        string
	GroupID                string
	AccountAuthorizationID string
}

type RuntimeAvailabilityClearer interface {
	ClearAuthorizedAccountRuntimeAvailability(context.Context, RuntimeTarget) error
}

type AccountLookupInvalidator interface {
	InvalidateAccountLookupCache(context.Context, string) error
}

type GatewayRuntimeInvalidator interface {
	InvalidateGatewayRuntime(context.Context, string) error
}

type Options struct {
	Store                    port.ManagementAccountAuthorizedDispatcher
	RuntimeClearer           RuntimeAvailabilityClearer
	AccountLookupInvalidator AccountLookupInvalidator
	GatewayInvalidator       GatewayRuntimeInvalidator
	PageDataPublisher        accountpagedata.Publisher
	Logger                   *slog.Logger
	Now                      func() time.Time
}

type Service struct {
	opts Options
}

type Input struct {
	ActorSystemAccountID string
	ActorRole            string
	SystemAccountID      string
	SelfOnly             bool
	AccountID            string
	Status               *string
	Priority             *int
	SuperPriorityEnabled *bool
	FallbackEnabled      *bool
	ClearFailureState    bool
}

type Account struct {
	ID                     string `json:"id"`
	SystemAccountID        string `json:"systemAccountId"`
	OwnerSystemAccountID   string `json:"ownerSystemAccountId"`
	Name                   string `json:"name"`
	ProviderCode           string `json:"providerCode"`
	Type                   string `json:"type"`
	Status                 string `json:"status"`
	Schedulable            bool   `json:"schedulable"`
	ConcurrencyLimit       int    `json:"concurrencyLimit"`
	Priority               int    `json:"priority"`
	SuperPriorityEnabled   bool   `json:"superPriorityEnabled"`
	FallbackEnabled        bool   `json:"fallbackEnabled"`
	BoundGroupID           string `json:"boundGroupId"`
	BoundGroupName         string `json:"boundGroupName"`
	AccountAuthorizationID string `json:"accountAuthorizationId"`
	AccessType             string `json:"accessType"`
}

type Result struct {
	Account       Account
	ChangedFields []string
}

func NewService(opts Options) *Service {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Service{opts: opts}
}

func (s *Service) Update(ctx context.Context, input Input) (Result, error) {
	if s.opts.Store == nil {
		return Result{}, fmt.Errorf("management account authorized dispatch store is required")
	}
	actorID, accountID := strings.TrimSpace(input.ActorSystemAccountID), strings.TrimSpace(input.AccountID)
	if actorID == "" || accountID == "" || !validInput(input) {
		return Result{}, ErrInvalid
	}
	effectiveSystemAccountID, canAccessAll := dispatchScope(input)
	stored, found, err := s.opts.Store.UpdateManagementAccountAuthorizedDispatch(ctx, port.ManagementAccountAuthorizedDispatchInput{
		AccountID: accountID, EffectiveSystemAccountID: effectiveSystemAccountID, CanAccessAll: canAccessAll,
		Status: input.Status, Priority: input.Priority, SuperPriorityEnabled: input.SuperPriorityEnabled,
		FallbackEnabled: input.FallbackEnabled, ClearFailureState: input.ClearFailureState, UpdatedAt: s.opts.Now().UTC(),
	})
	if err != nil {
		switch {
		case errors.Is(err, port.ErrManagementAccountAuthorizedDispatchPendingTest):
			return Result{}, ErrPendingTest
		case errors.Is(err, port.ErrManagementAccountAuthorizedDispatchExclusive):
			return Result{}, ErrExclusive
		case errors.Is(err, port.ErrManagementAccountAuthorizedDispatchUnavailable):
			return Result{}, ErrUnavailable
		default:
			return Result{}, err
		}
	}
	if !found {
		return Result{}, ErrNotFound
	}
	account := mapAccount(stored.Account)
	s.afterCommit(ctx, account, stored.ChangedFields, input.ClearFailureState || (input.Status != nil && *input.Status == "active"))
	return Result{Account: account, ChangedFields: stored.ChangedFields}, nil
}

func validInput(input Input) bool {
	if input.Status != nil && *input.Status != "active" && *input.Status != "disabled" {
		return false
	}
	if input.Priority != nil && *input.Priority < 0 {
		return false
	}
	return true
}

func dispatchScope(input Input) (string, bool) {
	if input.SelfOnly || (strings.TrimSpace(input.ActorRole) != "admin" && strings.TrimSpace(input.ActorRole) != "super_admin") {
		return strings.TrimSpace(input.ActorSystemAccountID), false
	}
	systemID := strings.TrimSpace(input.SystemAccountID)
	if systemID == "all" {
		systemID = ""
	}
	return systemID, systemID == ""
}

func mapAccount(value port.ManagementAccountAuthorizedDispatchAccount) Account {
	return Account{ID: value.ID, SystemAccountID: value.SystemAccountID, OwnerSystemAccountID: value.SystemAccountID,
		Name: value.Name, ProviderCode: value.ProviderCode, Type: value.Type, Status: value.Status,
		Schedulable: value.Schedulable, ConcurrencyLimit: value.ConcurrencyLimit, Priority: value.Priority,
		SuperPriorityEnabled: value.SuperPriorityEnabled, FallbackEnabled: value.FallbackEnabled,
		BoundGroupID: value.BoundGroupID, BoundGroupName: value.BoundGroupName,
		AccountAuthorizationID: value.AccountAuthorizationID, AccessType: "authorized"}
}

func (s *Service) afterCommit(ctx context.Context, account Account, fields []string, clearRuntime bool) {
	postCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if clearRuntime && s.opts.RuntimeClearer != nil {
		target := RuntimeTarget{AccountID: account.ID, SystemAccountID: account.SystemAccountID, GroupID: account.BoundGroupID, AccountAuthorizationID: account.AccountAuthorizationID}
		if err := s.opts.RuntimeClearer.ClearAuthorizedAccountRuntimeAvailability(postCtx, target); err != nil {
			s.opts.Logger.WarnContext(postCtx, "授权账户调度更新后运行态清理失败", "accountId", account.ID, "error", err)
		}
	}
	if s.opts.AccountLookupInvalidator != nil {
		if err := s.opts.AccountLookupInvalidator.InvalidateAccountLookupCache(postCtx, account.ID); err != nil {
			s.opts.Logger.WarnContext(postCtx, "授权账户调度更新后账户缓存失效失败", "accountId", account.ID, "error", err)
		}
	}
	if s.opts.GatewayInvalidator != nil {
		if err := s.opts.GatewayInvalidator.InvalidateGatewayRuntime(postCtx, GatewayRuntimeReason); err != nil {
			s.opts.Logger.WarnContext(postCtx, "授权账户调度更新后网关运行态失效失败", "accountId", account.ID, "error", err)
		}
	}
	if s.opts.PageDataPublisher == nil {
		return
	}
	change := accountpagedata.ChangeInput{AccountID: account.ID, Operation: accountpagedata.OperationUpsert,
		OwnerSystemAccountIDs: []string{account.SystemAccountID}, FieldMask: fields,
		OrderChanged: contains(fields, "priority")}
	if err := s.opts.PageDataPublisher.PublishAccountStaticChange(postCtx, change); err != nil {
		s.opts.Logger.WarnContext(postCtx, "授权账户调度更新后静态页面数据失效失败", "accountId", account.ID, "error", err)
	}
	if contains(fields, "status") || contains(fields, "clearFailureState") {
		change.FieldMask = []string{"status", "schedulable"}
		if err := s.opts.PageDataPublisher.PublishAccountRuntimeChange(postCtx, change); err != nil {
			s.opts.Logger.WarnContext(postCtx, "授权账户调度更新后运行态页面数据失效失败", "accountId", account.ID, "error", err)
		}
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
