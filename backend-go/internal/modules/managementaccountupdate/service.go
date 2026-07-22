package managementaccountupdate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	GatewayRuntimeReason = "account_updated"
	invalidationTimeout  = 5 * time.Second
)

var (
	ErrInvalid         = errors.New("账户更新参数无效")
	ErrNotFound        = errors.New("账户不存在")
	ErrAuthorized      = errors.New("授权账户不能修改来源账户配置")
	ErrVersionConflict = errors.New("账户配置已发生变化，请刷新后重试")
	ErrProviderInvalid = errors.New("账户供应商或协议档案不可用")
	ErrGroupInvalid    = errors.New("账户分组无效")
	ErrNameExists      = errors.New("同一用户下账户名称已存在")
)

type CredentialCodec interface {
	DecryptJSON(string) (map[string]any, error)
	EncryptJSON(map[string]any) (string, error)
}

type AccountLookupInvalidator interface {
	InvalidateAccountLookupCache(context.Context, string) error
}

type GroupAccountIDsInvalidator interface {
	InvalidateGroupAccountIDsCache(context.Context) error
}

type GatewayRuntimeInvalidator interface {
	InvalidateGatewayRuntime(context.Context, string) error
}

type Options struct {
	Store                      port.ManagementAccountUpdater
	CredentialCodec            CredentialCodec
	AccountLookupInvalidator   AccountLookupInvalidator
	GroupAccountIDsInvalidator GroupAccountIDsInvalidator
	GatewayRuntimeInvalidator  GatewayRuntimeInvalidator
	Logger                     *slog.Logger
	Now                        func() time.Time
}

type Service struct {
	store                      port.ManagementAccountUpdater
	credentialCodec            CredentialCodec
	accountLookupInvalidator   AccountLookupInvalidator
	groupAccountIDsInvalidator GroupAccountIDsInvalidator
	gatewayRuntimeInvalidator  GatewayRuntimeInvalidator
	logger                     *slog.Logger
	now                        func() time.Time
}

type UpdateInput struct {
	ActorSystemAccountID   string
	ActorRole              string
	SystemAccountID        string
	SelfOnly               bool
	AccountID              string
	ExpectedConfigRevision int
	Fields                 map[string]any
}

type Result struct {
	Before               map[string]any
	After                map[string]any
	OwnerSystemAccountID string
	ChangedFields        []string
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
		store: opts.Store, credentialCodec: opts.CredentialCodec,
		accountLookupInvalidator:   opts.AccountLookupInvalidator,
		groupAccountIDsInvalidator: opts.GroupAccountIDsInvalidator,
		gatewayRuntimeInvalidator:  opts.GatewayRuntimeInvalidator, logger: logger, now: now,
	}
}

func (s *Service) Update(ctx context.Context, input UpdateInput) (Result, error) {
	if s.store == nil || s.credentialCodec == nil {
		return Result{}, fmt.Errorf("management account update dependencies are required")
	}
	actorID := strings.TrimSpace(input.ActorSystemAccountID)
	accountID := strings.TrimSpace(input.AccountID)
	if actorID == "" || accountID == "" || input.ExpectedConfigRevision < 1 {
		return Result{}, ErrInvalid
	}
	fields, err := normalizeFields(input.Fields)
	if err != nil {
		return Result{}, err
	}
	effectiveSystemAccountID, canAccessAll := updateScope(input)
	target, found, err := s.store.LoadManagementAccountUpdateTarget(ctx, port.ManagementAccountUpdateTargetInput{
		AccountID: accountID, EffectiveSystemAccountID: effectiveSystemAccountID, CanAccessAll: canAccessAll,
	})
	if err != nil {
		return Result{}, err
	}
	if !found {
		return Result{}, ErrNotFound
	}
	if target.AccessType == "authorized" {
		return Result{}, ErrAuthorized
	}
	if target.ConfigRevision != input.ExpectedConfigRevision {
		return Result{}, ErrVersionConflict
	}

	storeInput := port.ManagementAccountUpdateInput{
		AccountID: accountID, EffectiveSystemAccountID: effectiveSystemAccountID, CanAccessAll: canAccessAll,
		ExpectedConfigRevision: input.ExpectedConfigRevision, Updates: fields, UpdatedAt: s.now().UTC(),
	}
	if requested, ok := fields["credentials"].(map[string]any); ok {
		current, decryptErr := s.credentialCodec.DecryptJSON(target.CredentialsEncrypted)
		if decryptErr != nil {
			return Result{}, fmt.Errorf("decrypt management account credentials: %w", decryptErr)
		}
		merged := mergeCredentials(target.Type, current, requested)
		storeInput.CredentialsEncrypted, err = s.credentialCodec.EncryptJSON(merged)
		if err != nil {
			return Result{}, fmt.Errorf("encrypt management account credentials: %w", err)
		}
		storeInput.HasCredentials = true
		delete(storeInput.Updates, "credentials")
	}

	stored, updated, err := s.store.UpdateManagementAccount(ctx, storeInput)
	if err != nil {
		switch {
		case errors.Is(err, port.ErrManagementAccountUpdateProviderInvalid):
			return Result{}, ErrProviderInvalid
		case errors.Is(err, port.ErrManagementAccountUpdateGroupInvalid):
			return Result{}, ErrGroupInvalid
		case errors.Is(err, port.ErrManagementAccountUpdateNameExists):
			return Result{}, ErrNameExists
		default:
			return Result{}, err
		}
	}
	if !updated {
		return Result{}, ErrVersionConflict
	}
	s.afterCommit(ctx, stored)
	return Result{Before: stored.Before, After: stored.After, OwnerSystemAccountID: stored.OwnerSystemAccountID, ChangedFields: stored.ChangedFields}, nil
}

func updateScope(input UpdateInput) (string, bool) {
	actorID := strings.TrimSpace(input.ActorSystemAccountID)
	if input.SelfOnly || !isAdminRole(input.ActorRole) {
		return actorID, false
	}
	systemID := strings.TrimSpace(input.SystemAccountID)
	if systemID == "all" {
		systemID = ""
	}
	return systemID, systemID == ""
}

func isAdminRole(role string) bool {
	role = strings.TrimSpace(role)
	return role == "admin" || role == "super_admin"
}

var allowedFields = map[string]struct{}{
	"name": {}, "credentials": {}, "healthCheckModel": {},
	"healthCheckEndpointMode": {}, "status": {},
	"concurrencyLimit": {}, "priority": {}, "superPriorityEnabled": {}, "fallbackEnabled": {},
	"proxyProfileId": {}, "schedulable": {}, "temporaryUnavailableContinuousProbeEnabled": {}, "notes": {},
	"clearFailureState": {},
}

func normalizeFields(input map[string]any) (map[string]any, error) {
	if len(input) == 0 {
		return nil, ErrInvalid
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		if _, ok := allowedFields[key]; !ok {
			return nil, ErrInvalid
		}
		out[key] = value
	}
	if name, ok := out["name"]; ok {
		text, valid := name.(string)
		if !valid || strings.TrimSpace(text) == "" {
			return nil, ErrInvalid
		}
		out["name"] = strings.TrimSpace(text)
	}
	if credentials, ok := out["credentials"]; ok {
		if _, valid := credentials.(map[string]any); !valid {
			return nil, ErrInvalid
		}
	}
	if revisionSensitiveStatus, ok := out["status"]; ok {
		status, valid := revisionSensitiveStatus.(string)
		if !valid || !validStatus(status) {
			return nil, ErrInvalid
		}
	}
	if group, ok := out["groupId"]; ok {
		text, valid := group.(string)
		if !valid || strings.TrimSpace(text) == "" {
			return nil, ErrGroupInvalid
		}
		out["groupId"] = strings.TrimSpace(text)
	}
	if super, _ := out["superPriorityEnabled"].(bool); super {
		if fallback, _ := out["fallbackEnabled"].(bool); fallback {
			return nil, errors.New("超级优先和降级备用不能同时开启")
		}
	}
	return out, nil
}

func validStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "active", "pending_test", "disabled", "error", "rate_limited", "temporary_unavailable":
		return true
	default:
		return false
	}
}

func mergeCredentials(accountType string, current, requested map[string]any) map[string]any {
	merged := make(map[string]any, len(requested)+len(current))
	for key, value := range requested {
		merged[key] = value
	}
	preserveCredentialText(merged, current, "base_url")
	preserveCredentialArray(merged, current, "supported_endpoint_modes")
	if accountType == "api_key" {
		replacing := hasCredentialText(requested["api_key"]) || hasCredentialStringArray(requested["api_keys"])
		preserveCredentialText(merged, current, "api_key")
		if !replacing {
			preserveCredentialArray(merged, current, "api_keys")
			preserveCredentialText(merged, current, "api_key_strategy")
			preserveCredentialArray(merged, current, "api_key_weights")
		} else if hasCredentialStringArray(merged["api_keys"]) {
			preserveCredentialText(merged, current, "api_key_strategy")
			preserveCredentialArray(merged, current, "api_key_weights")
		}
	} else {
		for _, key := range []string{"access_token", "refresh_token", "expires_at", "client_id", "client_secret", "id_token", "email", "account_id", "chatgpt_user_id", "plan_type", "quota_project_id"} {
			preserveCredentialText(merged, current, key)
		}
	}
	return merged
}

func preserveCredentialText(output, source map[string]any, key string) {
	if hasCredentialText(output[key]) {
		return
	}
	if hasCredentialText(source[key]) {
		output[key] = source[key]
	}
}

func preserveCredentialArray(output, source map[string]any, key string) {
	if values, ok := output[key].([]any); ok && len(values) > 0 {
		return
	}
	if values, ok := source[key].([]any); ok && len(values) > 0 {
		output[key] = values
	}
}

func hasCredentialText(value any) bool {
	text, ok := value.(string)
	return ok && strings.TrimSpace(text) != ""
}

func hasCredentialStringArray(value any) bool {
	values, ok := value.([]any)
	if !ok {
		return false
	}
	for _, value := range values {
		if hasCredentialText(value) {
			return true
		}
	}
	return false
}

func (s *Service) afterCommit(ctx context.Context, result port.ManagementAccountUpdateResult) {
	fields := append([]string(nil), result.ChangedFields...)
	sort.Strings(fields)
	postCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), invalidationTimeout)
	defer cancel()
	if s.accountLookupInvalidator != nil {
		if err := s.accountLookupInvalidator.InvalidateAccountLookupCache(postCtx, result.AccountID); err != nil {
			s.logger.WarnContext(postCtx, "账户更新后查找缓存失效失败", "accountId", result.AccountID, "error", err)
		}
	}
	if contains(fields, "groupId") && s.groupAccountIDsInvalidator != nil {
		if err := s.groupAccountIDsInvalidator.InvalidateGroupAccountIDsCache(postCtx); err != nil {
			s.logger.WarnContext(postCtx, "账户更新后分组账户 ID 缓存失效失败", "accountId", result.AccountID, "error", err)
		}
	}
	if s.gatewayRuntimeInvalidator != nil {
		if err := s.gatewayRuntimeInvalidator.InvalidateGatewayRuntime(postCtx, GatewayRuntimeReason); err != nil {
			s.logger.WarnContext(postCtx, "账户更新后网关运行态失效失败", "accountId", result.AccountID, "error", err)
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

func containsAny(values []string, targets ...string) bool {
	for _, target := range targets {
		if contains(values, target) {
			return true
		}
	}
	return false
}
