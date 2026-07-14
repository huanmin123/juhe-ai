package managementclientippolicies

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	clientIPPolicyInvalidationTimeout = 5 * time.Second
	maxPolicyReasonRunes              = 500

	allowlistReplacementReason = "被新的白名单策略替换"
	defaultUnallowlistReason   = "管理员解除策略"
)

type ClientIPPolicyCacheInvalidator interface {
	InvalidateClientIPPolicyCache(ctx context.Context) error
}

type Service struct {
	transactor  port.ManagementClientIPPolicyTransactor
	invalidator ClientIPPolicyCacheInvalidator
	logger      *slog.Logger
	now         func() time.Time
	newID       func(prefix string) string
}

type ServiceOptions struct {
	Transactor  port.ManagementClientIPPolicyTransactor
	Invalidator ClientIPPolicyCacheInvalidator
	Logger      *slog.Logger
	Now         func() time.Time
	NewID       func(prefix string) string
}

type AllowlistInput struct {
	IPHash               string
	ActorSystemAccountID string
	Reason               *string
}

type UnallowlistInput struct {
	IPHash               string
	ActorSystemAccountID string
	Reason               *string
}

type PolicySummary struct {
	ID                        string  `json:"id"`
	IPHash                    string  `json:"ipHash"`
	PolicyType                string  `json:"policyType"`
	Status                    string  `json:"status"`
	Reason                    *string `json:"reason,omitempty"`
	ExpiresAt                 *string `json:"expiresAt,omitempty"`
	CreatedBySystemAccountID  string  `json:"createdBySystemAccountId"`
	CreatedAt                 string  `json:"createdAt"`
	UpdatedAt                 string  `json:"updatedAt"`
	DisabledAt                *string `json:"disabledAt,omitempty"`
	DisabledBySystemAccountID *string `json:"disabledBySystemAccountId,omitempty"`
	DisabledReason            *string `json:"disabledReason,omitempty"`
}

type UnallowlistResult struct {
	DisabledCount int64 `json:"disabledCount"`
}

type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string {
	return e.Message
}

func NewService(transactor port.ManagementClientIPPolicyTransactor) *Service {
	return NewServiceWithOptions(ServiceOptions{Transactor: transactor})
}

func NewServiceWithOptions(options ServiceOptions) *Service {
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	newID := options.NewID
	if newID == nil {
		newID = func(prefix string) string {
			return prefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		}
	}
	return &Service{
		transactor:  options.Transactor,
		invalidator: options.Invalidator,
		logger:      logger,
		now:         now,
		newID:       newID,
	}
}

func (s *Service) Allowlist(ctx context.Context, input AllowlistInput) (PolicySummary, error) {
	normalized, err := normalizeMutationInput(
		input.IPHash,
		input.ActorSystemAccountID,
		input.Reason,
	)
	if err != nil {
		return PolicySummary{}, err
	}
	if s.transactor == nil {
		return PolicySummary{}, fmt.Errorf("management client IP policy transactor is required")
	}

	now := s.now().UTC()
	id := s.newID("ip_policy")
	var created port.ManagementClientIPPolicySummary
	err = s.transactor.ManagementClientIPPolicyInTx(
		ctx,
		func(txCtx context.Context, store port.ManagementClientIPPolicyStore) error {
			_, found, err := store.LockManagementClientIPRegistry(txCtx, normalized.ipHash)
			if err != nil {
				return err
			}
			if !found {
				return &ValidationError{Message: "IP 不存在"}
			}
			if _, err := store.DisableActiveManagementClientIPPolicies(
				txCtx,
				port.ManagementClientIPPolicyDisableInput{
					IPHash:               normalized.ipHash,
					ActorSystemAccountID: normalized.actorSystemAccountID,
					Reason:               allowlistReplacementReason,
					Now:                  now,
				},
			); err != nil {
				return err
			}
			created, err = store.InsertManagementClientIPAllowlistPolicy(
				txCtx,
				port.ManagementClientIPAllowlistCreateInput{
					ID:                   id,
					IPHash:               normalized.ipHash,
					Reason:               normalized.reason,
					ActorSystemAccountID: normalized.actorSystemAccountID,
					Now:                  now,
				},
			)
			return err
		},
	)
	if err != nil {
		return PolicySummary{}, err
	}

	s.invalidateCache(ctx)
	return policySummary(created), nil
}

func (s *Service) Unallowlist(
	ctx context.Context,
	input UnallowlistInput,
) (UnallowlistResult, error) {
	normalized, err := normalizeMutationInput(
		input.IPHash,
		input.ActorSystemAccountID,
		input.Reason,
	)
	if err != nil {
		return UnallowlistResult{}, err
	}
	if s.transactor == nil {
		return UnallowlistResult{}, fmt.Errorf("management client IP policy transactor is required")
	}

	reason := defaultUnallowlistReason
	if normalized.reason != nil {
		reason = *normalized.reason
	}
	now := s.now().UTC()
	var disabledCount int64
	err = s.transactor.ManagementClientIPPolicyInTx(
		ctx,
		func(txCtx context.Context, store port.ManagementClientIPPolicyStore) error {
			if _, _, err := store.LockManagementClientIPRegistry(
				txCtx,
				normalized.ipHash,
			); err != nil {
				return err
			}
			disabledCount, err = store.DisableActiveManagementClientIPAllowlistPolicies(
				txCtx,
				port.ManagementClientIPPolicyDisableInput{
					IPHash:               normalized.ipHash,
					ActorSystemAccountID: normalized.actorSystemAccountID,
					Reason:               reason,
					Now:                  now,
				},
			)
			return err
		},
	)
	if err != nil {
		return UnallowlistResult{}, err
	}

	s.invalidateCache(ctx)
	return UnallowlistResult{DisabledCount: disabledCount}, nil
}

func (s *Service) invalidateCache(ctx context.Context) {
	if s.invalidator == nil {
		return
	}
	invalidationCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		clientIPPolicyInvalidationTimeout,
	)
	defer cancel()
	if err := s.invalidator.InvalidateClientIPPolicyCache(invalidationCtx); err != nil {
		s.logger.Warn(
			"客户端 IP 策略写入后共享缓存失效失败",
			slog.String(
				"event",
				"management_client_ip_policy_cache_invalidation_failed",
			),
			slog.Any("error", err),
		)
	}
}

type normalizedMutationInput struct {
	ipHash               string
	actorSystemAccountID string
	reason               *string
}

func normalizeMutationInput(
	ipHash string,
	actorSystemAccountID string,
	reason *string,
) (normalizedMutationInput, error) {
	normalizedIPHash := strings.ToLower(trimECMAScriptWhitespace(ipHash))
	if !validIPHash(normalizedIPHash) {
		return normalizedMutationInput{}, &ValidationError{Message: "IP 标识无效"}
	}
	normalizedActor := trimECMAScriptWhitespace(actorSystemAccountID)
	if normalizedActor == "" {
		return normalizedMutationInput{}, &ValidationError{Message: "缺少系统账户上下文"}
	}
	normalizedReason, err := normalizeReason(reason)
	if err != nil {
		return normalizedMutationInput{}, err
	}
	return normalizedMutationInput{
		ipHash:               normalizedIPHash,
		actorSystemAccountID: normalizedActor,
		reason:               normalizedReason,
	}, nil
}

func validIPHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func normalizeReason(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	normalized := trimECMAScriptWhitespace(*value)
	if utf16CodeUnitCount(normalized) > maxPolicyReasonRunes {
		return nil, &ValidationError{Message: "原因不能超过 500 个字符"}
	}
	if normalized == "" {
		return nil, nil
	}
	return &normalized, nil
}

func policySummary(value port.ManagementClientIPPolicySummary) PolicySummary {
	return PolicySummary{
		ID:                        value.ID,
		IPHash:                    value.IPHash,
		PolicyType:                string(value.PolicyType),
		Status:                    string(value.Status),
		Reason:                    copyString(value.Reason),
		ExpiresAt:                 formatOptionalTime(value.ExpiresAt),
		CreatedBySystemAccountID:  value.CreatedBySystemAccountID,
		CreatedAt:                 formatTime(value.CreatedAt),
		UpdatedAt:                 formatTime(value.UpdatedAt),
		DisabledAt:                formatOptionalTime(value.DisabledAt),
		DisabledBySystemAccountID: copyString(value.DisabledBySystemAccountID),
		DisabledReason:            copyString(value.DisabledReason),
	}
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func formatOptionalTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := formatTime(*value)
	return &formatted
}

func copyString(value *string) *string {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func utf16CodeUnitCount(value string) int {
	count := 0
	for _, character := range value {
		if character > 0xFFFF {
			count += 2
		} else {
			count++
		}
	}
	return count
}

func trimECMAScriptWhitespace(value string) string {
	return strings.TrimFunc(value, func(character rune) bool {
		switch character {
		case '\u0009', '\u000B', '\u000C', '\u0020', '\u00A0', '\u1680',
			'\u2000', '\u2001', '\u2002', '\u2003', '\u2004', '\u2005',
			'\u2006', '\u2007', '\u2008', '\u2009', '\u200A', '\u202F',
			'\u205F', '\u3000', '\uFEFF', '\u000A', '\u000D', '\u2028',
			'\u2029':
			return true
		default:
			return false
		}
	})
}
