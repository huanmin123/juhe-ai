package managementaccounttrafficmigration

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

const GatewayRuntimeReason = "traffic_migration"

var (
	ErrInvalid           = errors.New("迁移流量参数无效")
	ErrNotFound          = errors.New("账户不存在或无权迁移")
	ErrSameAccount       = errors.New("目标账户不能和当前账户相同")
	ErrDifferentOwner    = errors.New("目标账户必须和当前账户归属同一个系统账户")
	ErrDifferentProvider = errors.New("目标账户必须和当前账户属于同一个供应商")
	ErrDifferentGroup    = errors.New("目标账户必须和当前账户在同一个分组内")
	ErrTargetUnavailable = errors.New("目标账户当前不可调度，请选择正常可用的账户")
)

type SourceStatus = port.ManagementAccountTrafficMigrationSourceStatus

const (
	SourceStatusTemporaryUnavailable = port.ManagementAccountTrafficMigrationTemporaryUnavailable
	SourceStatusDisabled             = port.ManagementAccountTrafficMigrationDisabled
	SourceStatusUnchanged            = port.ManagementAccountTrafficMigrationUnchanged
)

type RuntimeScope struct {
	SystemAccountID string
	GroupID         string
}

type RuntimeMigrationInput struct {
	SourceAccountID        string
	TargetAccountID        string
	AffinityScope          *RuntimeScope
	PreferenceScope        *RuntimeScope
	PreferMigratedSessions bool
}

type RuntimeMigrator interface {
	MigrateAccountTrafficRuntime(context.Context, RuntimeMigrationInput) (int, error)
}

type AccountLookupInvalidator interface {
	InvalidateAccountLookupCache(context.Context, string) error
}

type GatewayRuntimeInvalidator interface {
	InvalidateGatewayRuntime(context.Context, string) error
}

type Options struct {
	Store                    port.ManagementAccountTrafficMigrator
	RuntimeMigrator          RuntimeMigrator
	AccountLookupInvalidator AccountLookupInvalidator
	GatewayInvalidator       GatewayRuntimeInvalidator
	GranteeReader            accountpagedata.GranteeReader
	PageDataPublisher        accountpagedata.Publisher
	Logger                   *slog.Logger
	Now                      func() time.Time
}

type Service struct{ opts Options }

type Input struct {
	ActorSystemAccountID string
	ActorRole            string
	SystemAccountID      string
	SelfOnly             bool
	SourceAccountID      string
	TargetAccountID      string
	SourceStatus         SourceStatus
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
	CooldownUntil          string `json:"cooldownUntil,omitempty"`
	BoundGroupID           string `json:"boundGroupId"`
	AccountAuthorizationID string `json:"accountAuthorizationId,omitempty"`
	AccessType             string `json:"accessType"`
}

type Result struct {
	SourceAccount        Account      `json:"sourceAccount"`
	TargetAccount        Account      `json:"targetAccount"`
	SourceCooldownUntil  string       `json:"sourceCooldownUntil,omitempty"`
	MigratedSessionCount int          `json:"migratedSessionCount"`
	SourceStatus         SourceStatus `json:"sourceStatus"`
	GroupID              string       `json:"-"`
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

func (s *Service) Migrate(ctx context.Context, input Input) (Result, error) {
	if s.opts.Store == nil {
		return Result{}, fmt.Errorf("management account traffic migration store is required")
	}
	input.ActorSystemAccountID = strings.TrimSpace(input.ActorSystemAccountID)
	input.SourceAccountID = strings.TrimSpace(input.SourceAccountID)
	input.TargetAccountID = strings.TrimSpace(input.TargetAccountID)
	if input.ActorSystemAccountID == "" || input.SourceAccountID == "" || input.TargetAccountID == "" || !validSourceStatus(input.SourceStatus) {
		return Result{}, ErrInvalid
	}
	effectiveSystemAccountID, canAccessAll := migrationScope(input)
	stored, found, err := s.opts.Store.MigrateManagementAccountTraffic(ctx, port.ManagementAccountTrafficMigrationInput{
		SourceAccountID: input.SourceAccountID, TargetAccountID: input.TargetAccountID,
		EffectiveSystemAccountID: effectiveSystemAccountID, CanAccessAll: canAccessAll,
		SourceStatus: input.SourceStatus, UpdatedAt: s.opts.Now().UTC(),
	})
	if err != nil {
		return Result{}, mapStoreError(err)
	}
	if !found {
		return Result{}, ErrNotFound
	}

	s.afterCommit(ctx, stored)
	migratedSessionCount := 0
	if s.opts.RuntimeMigrator != nil {
		migratedSessionCount, err = s.opts.RuntimeMigrator.MigrateAccountTrafficRuntime(ctx, runtimeInput(stored, input.SourceStatus))
		if err != nil {
			return Result{}, fmt.Errorf("migrate account traffic runtime: %w", err)
		}
	}
	return Result{
		SourceAccount: mapAccount(stored.SourceAccount), TargetAccount: mapAccount(stored.TargetAccount),
		SourceCooldownUntil: formatTime(stored.SourceCooldownUntil), MigratedSessionCount: migratedSessionCount,
		SourceStatus: input.SourceStatus, GroupID: stored.GroupID,
	}, nil
}

func validSourceStatus(status SourceStatus) bool {
	return status == SourceStatusTemporaryUnavailable || status == SourceStatusDisabled || status == SourceStatusUnchanged
}

func migrationScope(input Input) (string, bool) {
	role := strings.TrimSpace(input.ActorRole)
	if input.SelfOnly || (role != "admin" && role != "super_admin") {
		return input.ActorSystemAccountID, false
	}
	systemID := strings.TrimSpace(input.SystemAccountID)
	if systemID == "all" {
		systemID = ""
	}
	return systemID, systemID == ""
}

func mapStoreError(err error) error {
	switch {
	case errors.Is(err, port.ErrManagementAccountTrafficMigrationNotFound), errors.Is(err, port.ErrManagementAccountTrafficMigrationStateChanged):
		return ErrNotFound
	case errors.Is(err, port.ErrManagementAccountTrafficMigrationSameAccount):
		return ErrSameAccount
	case errors.Is(err, port.ErrManagementAccountTrafficMigrationDifferentOwner):
		return ErrDifferentOwner
	case errors.Is(err, port.ErrManagementAccountTrafficMigrationDifferentProvider):
		return ErrDifferentProvider
	case errors.Is(err, port.ErrManagementAccountTrafficMigrationDifferentGroup):
		return ErrDifferentGroup
	case errors.Is(err, port.ErrManagementAccountTrafficMigrationTargetUnavailable):
		return ErrTargetUnavailable
	default:
		return err
	}
}

func runtimeInput(result port.ManagementAccountTrafficMigrationResult, status SourceStatus) RuntimeMigrationInput {
	input := RuntimeMigrationInput{SourceAccountID: result.SourceAccount.ID, TargetAccountID: result.TargetAccount.ID, PreferMigratedSessions: status == SourceStatusUnchanged}
	scope := &RuntimeScope{SystemAccountID: result.SourceAccount.SystemAccountID, GroupID: result.GroupID}
	if result.SourceAccount.AccessType == "authorized" {
		input.AffinityScope = scope
	}
	if status != SourceStatusUnchanged {
		input.PreferenceScope = scope
	}
	return input
}

func (s *Service) afterCommit(ctx context.Context, result port.ManagementAccountTrafficMigrationResult) {
	postCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if result.SourceChanged && s.opts.AccountLookupInvalidator != nil {
		if err := s.opts.AccountLookupInvalidator.InvalidateAccountLookupCache(postCtx, result.SourceAccount.ID); err != nil {
			s.opts.Logger.WarnContext(postCtx, "账户流量迁移后账户缓存失效失败", "accountId", result.SourceAccount.ID, "error", err)
		}
	}
	if result.SourceChanged && s.opts.GatewayInvalidator != nil {
		if err := s.opts.GatewayInvalidator.InvalidateGatewayRuntime(postCtx, GatewayRuntimeReason); err != nil {
			s.opts.Logger.WarnContext(postCtx, "账户流量迁移后网关运行态失效失败", "accountId", result.SourceAccount.ID, "error", err)
		}
	}
	if s.opts.PageDataPublisher == nil {
		return
	}
	owners := []string{result.SourceAccount.SystemAccountID}
	allScopes := false
	if result.SourceAccount.AccessType != "authorized" {
		var err error
		owners, allScopes, err = accountpagedata.ResolveOwners(postCtx, s.opts.GranteeReader, result.SourceAccount.ID, []string{result.SourceAccount.OwnerSystemAccountID})
		if err != nil {
			s.opts.Logger.WarnContext(postCtx, "账户流量迁移后页面数据 owner 查询失败", "accountId", result.SourceAccount.ID, "error", err)
		}
	}
	if err := s.opts.PageDataPublisher.PublishAccountRuntimeChange(postCtx, accountpagedata.ChangeInput{
		AccountID: result.SourceAccount.ID, Operation: accountpagedata.OperationUpsert,
		OwnerSystemAccountIDs: accountpagedata.NormalizeOwnerIDs(owners), AllScopes: allScopes,
		FieldMask: []string{"status", "schedulable", "cooldownUntil"},
	}); err != nil {
		s.opts.Logger.WarnContext(postCtx, "账户流量迁移后页面数据失效失败", "accountId", result.SourceAccount.ID, "error", err)
	}
}

func mapAccount(value port.ManagementAccountTrafficMigrationAccount) Account {
	return Account{ID: value.ID, SystemAccountID: value.SystemAccountID, OwnerSystemAccountID: value.OwnerSystemAccountID,
		Name: value.Name, ProviderCode: value.ProviderCode, Type: value.Type, Status: value.Status,
		Schedulable: value.Schedulable, CooldownUntil: formatTime(value.CooldownUntil), BoundGroupID: value.BoundGroupID,
		AccountAuthorizationID: value.AccountAuthorizationID, AccessType: value.AccessType}
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
