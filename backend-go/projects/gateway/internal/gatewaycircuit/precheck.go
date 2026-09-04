package gatewaycircuit

import (
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Precheck probe policy mirrors account-probe-confirmation-policy.ts.
const (
	AccountPrecheckProbeIntervalMs      = int64(2 * 60_000)
	AccountPrecheckMinimumObservationMs = int64(5 * 60_000)
)

// PrecheckProbeInput mirrors the nextAccountPrecheckProbeAtMs input.
type PrecheckProbeInput struct {
	AttemptCount int64
	MaxAttempts  int64
	StartedAtMs  int64
	NowMs        int64
}

// NextAccountPrecheckProbeAtMs mirrors nextAccountPrecheckProbeAtMs. The
// second return mirrors the undefined case (no further probe scheduled).
func NextAccountPrecheckProbeAtMs(input PrecheckProbeInput, random func() float64) (int64, bool) {
	if input.AttemptCount < input.MaxAttempts {
		return input.NowMs + passiveScheduleDelayMs(AccountPrecheckProbeIntervalMs, random), true
	}
	confirmationAtMs := input.StartedAtMs + AccountPrecheckMinimumObservationMs
	if input.NowMs < confirmationAtMs {
		return input.NowMs + passiveScheduleNotBeforeDelayMs(confirmationAtMs-input.NowMs, random), true
	}
	return 0, false
}

// Protocol code / version constants mirror domain/provider-protocol.ts.
const (
	OpenAIProtocolCode     = "openai"
	OpenAIProtocolVersion  = "v1"
	AnthropicProtocolCode  = "anthropic"
	AnthropicProtocolVersion = "v1"
	GeminiProtocolCode     = "gemini"
	GeminiProtocolVersion  = "v1beta"
)

// PrecheckUsageSummary mirrors the empty usage block the mapper fills.
type PrecheckUsageSummary struct {
	RequestCount      int64   `json:"requestCount"`
	InputTokens       int64   `json:"inputTokens"`
	OutputTokens      int64   `json:"outputTokens"`
	CacheReadTokens   int64   `json:"cacheReadTokens"`
	CacheReadCost     float64 `json:"cacheReadCost"`
	CacheWriteTokens  int64   `json:"cacheWriteTokens"`
	CacheWrite1hTokens int64  `json:"cacheWrite1hTokens"`
	CacheWriteCost    float64 `json:"cacheWriteCost"`
	ThinkingTokens    int64   `json:"thinkingTokens"`
	InputImageTokens  int64   `json:"inputImageTokens"`
	OutputImageTokens int64   `json:"outputImageTokens"`
	TotalTokens       int64   `json:"totalTokens"`
	TotalCost         float64 `json:"totalCost"`
}

// PrecheckSummaryPermissions mirrors the permission block.
type PrecheckSummaryPermissions struct {
	CanUse             bool `json:"canUse"`
	CanEdit            bool `json:"canEdit"`
	CanDelete          bool `json:"canDelete"`
	CanAuthorize       bool `json:"canAuthorize"`
	CanViewCredentials bool `json:"canViewCredentials"`
}

// PrecheckSummaryContext mirrors AccountPrecheckSummaryContext.
type PrecheckSummaryContext struct {
	GroupID         string
	SystemAccountID string
}

// PrecheckSummaryMapper mirrors the pure mapping logic of
// account-precheck-summary.mapper.ts: system account resolution, protocol
// fallbacks and the summary skeleton. The domain
// accountSummaryWithEffectiveAvailability decoration stays with the domain
// owner through the WithEffectiveAvailability hook (nil keeps the raw
// summary).
type PrecheckSummaryMapper struct {
	// WithEffectiveAvailability mirrors accountSummaryWithEffectiveAvailability.
	WithEffectiveAvailability func(summary PrecheckAccountSummary, nowMs int64) (PrecheckAccountSummary, error)
	// Now mirrors Date.now() in the mapper call path.
	Now func() int64
}

// PrecheckAccountSummary mirrors the AccountSummary fields the Node mapper
// assembles (the domain-owned effective availability/presentation is applied
// by the hook above).
type PrecheckAccountSummary struct {
	ID                          string
	SystemAccountID             string
	OwnerSystemAccountID        string
	ProviderCode                string
	ProviderProtocolProfileID   string
	ProtocolCode                string
	ProtocolVersion             string
	Name                        string
	Type                        string
	Credentials                 map[string]any
	Status                      string
	ConcurrencyLimit            int
	CurrentConcurrency          int
	Priority                    int
	SuperPriorityEnabled        bool
	FallbackEnabled             bool
	ClientCompatibility         string
	SupportedModels             []string
	ModelMappings               []gatewayruntimecache.AccountModelMapping
	HealthCheckModel            string
	HealthCheckEndpointMode     string
	ProxyProfileID              string
	Schedulable                 bool
	CooldownUntil               string
	LastErrorMessage            string
	StreamFailureCount          int
	StreamFailureWindowStartedAt string
	TodayUsage                  PrecheckUsageSummary
	Usage                       PrecheckUsageSummary
	AccessType                  string
	AccountAuthorizationID      string
	BoundGroupID                string
	BindingSystemAccountID      string
	Permissions                 PrecheckSummaryPermissions
}

// MapFromGatewayPrecheckAccount mirrors accountSummaryFromGatewayPrecheckAccount.
func (m *PrecheckSummaryMapper) MapFromGatewayPrecheckAccount(
	account gatewayruntimecache.OpenAIAccountSecret,
	context PrecheckSummaryContext,
) (PrecheckAccountSummary, error) {
	systemAccountID, err := gatewayAccountSummarySystemAccountID(account, context)
	if err != nil {
		return PrecheckAccountSummary{}, err
	}
	boundGroupID := context.GroupID
	bindingSystemAccountID := ""
	if account.AccountAccessType == "account_authorized" {
		boundGroupID, err = gatewayAccountSummaryBoundGroupID(account)
		if err != nil {
			return PrecheckAccountSummary{}, err
		}
		bindingSystemAccountID = systemAccountID
	}
	accessType := "owner"
	if account.AccountAccessType == "account_authorized" {
		accessType = "authorized"
	}
	emptyUsage := PrecheckUsageSummary{}
	summary := PrecheckAccountSummary{
		ID:                        account.ID,
		SystemAccountID:           systemAccountID,
		OwnerSystemAccountID:      account.AccountOwnerSystemAccountID,
		ProviderCode:              account.ProviderCode,
		ProviderProtocolProfileID: account.ProviderProtocolProfileID,
		ProtocolCode:              gatewayAccountSummaryProtocolCode(account),
		ProtocolVersion:           gatewayAccountSummaryProtocolVersion(account),
		Name:                      account.Name,
		Type:                      account.Type,
		Credentials:               account.Credentials,
		Status:                    account.Status,
		ConcurrencyLimit:          account.ConcurrencyLimit,
		CurrentConcurrency:        derefInt(account.CurrentConcurrency),
		Priority:                  account.Priority,
		SuperPriorityEnabled:      account.SuperPriorityEnabled,
		FallbackEnabled:           account.FallbackEnabled,
		ClientCompatibility:       account.ClientCompatibility,
		SupportedModels:           append([]string{}, account.SupportedModels...),
		ModelMappings:             append([]gatewayruntimecache.AccountModelMapping{}, account.ModelMappings...),
		HealthCheckModel:          strings.TrimSpace(account.HealthCheckModel),
		HealthCheckEndpointMode:   account.HealthCheckEndpointMode,
		ProxyProfileID:            derefString(account.ProxyProfileID),
		Schedulable:               true,
		CooldownUntil:             derefString(account.CooldownUntil),
		LastErrorMessage:          derefString(account.LastErrorMessage),
		StreamFailureCount:        account.StreamFailureCount,
		StreamFailureWindowStartedAt: derefString(account.StreamFailureWindowStartedAt),
		TodayUsage:                emptyUsage,
		Usage:                     emptyUsage,
		AccessType:                accessType,
		AccountAuthorizationID:    derefString(account.AccountAuthorizationID),
		BoundGroupID:              boundGroupID,
		BindingSystemAccountID:    bindingSystemAccountID,
		Permissions: PrecheckSummaryPermissions{
			CanUse:             true,
			CanEdit:            false,
			CanDelete:          false,
			CanAuthorize:       false,
			CanViewCredentials: false,
		},
	}
	if m.WithEffectiveAvailability != nil {
		now := int64(0)
		if m.Now != nil {
			now = m.Now()
		}
		return m.WithEffectiveAvailability(summary, now)
	}
	return summary, nil
}

func gatewayAccountSummarySystemAccountID(
	account gatewayruntimecache.OpenAIAccountSecret,
	context PrecheckSummaryContext,
) (string, error) {
	if account.AccountAccessType == "account_authorized" {
		if binding := strings.TrimSpace(derefString(account.BindingSystemAccountID)); binding != "" {
			return binding, nil
		}
		if contextSystem := strings.TrimSpace(context.SystemAccountID); contextSystem != "" {
			return contextSystem, nil
		}
		return "", errAccountSummary("授权账户缺少绑定系统账户，无法构造测试摘要")
	}
	if systemAccountID := strings.TrimSpace(account.SystemAccountID); systemAccountID != "" {
		return systemAccountID, nil
	}
	if contextSystem := strings.TrimSpace(context.SystemAccountID); contextSystem != "" {
		return contextSystem, nil
	}
	return "", errAccountSummary("账户缺少系统账户，无法构造测试摘要")
}

func gatewayAccountSummaryBoundGroupID(account gatewayruntimecache.OpenAIAccountSecret) (string, error) {
	if boundGroupID := strings.TrimSpace(derefString(account.BoundGroupID)); boundGroupID != "" {
		return boundGroupID, nil
	}
	return "", errAccountSummary("授权账户缺少绑定分组，无法构造测试摘要")
}

type summaryError struct {
	message string
}

func (e *summaryError) Error() string { return e.message }

func errAccountSummary(message string) error { return &summaryError{message: message} }

func gatewayAccountSummaryProtocolCode(account gatewayruntimecache.OpenAIAccountSecret) string {
	if protocolCode := strings.TrimSpace(account.ProtocolCode); protocolCode != "" {
		return protocolCode
	}
	profileID := strings.ToLower(account.ProviderProtocolProfileID)
	switch {
	case strings.Contains(profileID, "_openai_"):
		return OpenAIProtocolCode
	case strings.Contains(profileID, "_anthropic_"):
		return AnthropicProtocolCode
	case strings.Contains(profileID, "_gemini_") || strings.Contains(profileID, "_native_"):
		return GeminiProtocolCode
	default:
		return ""
	}
}

func gatewayAccountSummaryProtocolVersion(account gatewayruntimecache.OpenAIAccountSecret) string {
	if protocolVersion := strings.TrimSpace(account.ProtocolVersion); protocolVersion != "" {
		return protocolVersion
	}
	profileID := strings.ToLower(account.ProviderProtocolProfileID)
	switch {
	case strings.Contains(profileID, "_openai_"):
		return OpenAIProtocolVersion
	case strings.Contains(profileID, "_anthropic_"):
		return AnthropicProtocolVersion
	case strings.Contains(profileID, "_gemini_") || strings.Contains(profileID, "_native_"):
		return GeminiProtocolVersion
	default:
		return ""
	}
}

func derefInt(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}
