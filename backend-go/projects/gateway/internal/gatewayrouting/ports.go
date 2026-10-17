package gatewayrouting

import "context"

// ClientCompatibilityCapability mirrors the Node
// CLIENT_COMPATIBILITY_CAPABILITIES union
// ('openai_standard' | 'codex_responses' | 'anthropic_native' | 'claude_code').
const (
	ClientCompatibilityOpenAIStandard  = "openai_standard"
	ClientCompatibilityCodexResponses  = "codex_responses"
	ClientCompatibilityAnthropicNative = "anthropic_native"
	ClientCompatibilityClaudeCode      = "claude_code"
)

// CapabilityFilterInput carries what the capability probe needs from the
// request. Node rebuilds the express req with a model override before
// probing (gatewayRequestWithModelOverride); the Go gateway passes the
// effective model explicitly.
type CapabilityFilterInput struct {
	// RequestModel is the model override probe target
	// (options.requestModelOverride); empty means no override.
	RequestModel string
	// RequestClientCompatibility mirrors
	// options.requestClientCompatibility; empty means unspecified.
	RequestClientCompatibility string
}

// CapabilityFilterResult mirrors GatewayAccountCapabilityFilterResult.
type CapabilityFilterResult struct {
	Accounts     []UpstreamAccount
	SkippedCount int
	// Reason mirrors GatewayAccountCapabilityFilterReason (the provider
	// driver mismatch reason); empty when absent.
	Reason string
}

// AccountCapabilityFilter is the port toward the provider-driver capability
// probe (Node dispatch/account-capability-filter.ts
// filterGatewayAccountsByRequestCapability, backed by the providers drivers
// registry). The concrete probe belongs to the dispatch layer; the routing
// core consumes it through this port so tests can mock it.
type AccountCapabilityFilter interface {
	FilterAccountsByRequestCapability(ctx context.Context, accounts []UpstreamAccount, input CapabilityFilterInput) CapabilityFilterResult
}

// PassthroughCapabilityFilter reproduces the degenerate case where every
// account passes the capability probe. The Node routing path always runs the
// real probe, so production wires a concrete implementation; the
// pass-through exists only for contexts without a driver registry.
type PassthroughCapabilityFilter struct{}

// FilterAccountsByRequestCapability returns the input unchanged.
func (PassthroughCapabilityFilter) FilterAccountsByRequestCapability(_ context.Context, accounts []UpstreamAccount, _ CapabilityFilterInput) CapabilityFilterResult {
	return CapabilityFilterResult{Accounts: accounts}
}

// RuntimeCacheReader is the read port toward the gateway runtime cache (Node
// runtime-cache.service.ts functions the routing layer calls). The concrete
// implementation is owned by the gatewayruntimecache work package.
type RuntimeCacheReader interface {
	// ResolveCachedGroupUsageAccessMetadataAsync mirrors
	// resolveCachedGroupUsageAccessMetadataAsync; found=false mirrors the
	// Node undefined result.
	ResolveCachedGroupUsageAccessMetadataAsync(ctx context.Context, groupID, systemAccountID string) (GroupUsageAccessMetadata, bool, error)

	// ListCachedOpenAIAccountsForGroupAsync mirrors
	// listCachedOpenAIAccountsForGroupAsync.
	ListCachedOpenAIAccountsForGroupAsync(ctx context.Context, groupID, systemAccountID string, options CachedAccountsForGroupOptions) ([]UpstreamAccount, error)

	// ResolveCachedProviderModelRouteAsync mirrors
	// resolveCachedProviderModelRouteAsync. 供应商解耦后内部为 models 单表目
	// 录语义（设计 §3/§6.1）：matched = 请求模型存在 builtin/active 目录行，
	// missing = 无可见 active 行；ProviderCodes 是供应商收窄时代的过渡入参，
	// 不再参与判定。
	ResolveCachedProviderModelRouteAsync(ctx context.Context, input ProviderModelRouteInput) (ProviderModelRouteResolution, error)
}

// CachedAccountsForGroupOptions mirrors CachedOpenAIAccountsForGroupOptions.
type CachedAccountsForGroupOptions struct {
	RequestedModel          string
	RequestedEndpointFamily string
}

// ProviderModelRouteInput mirrors resolveCachedProviderModelRouteAsync's
// input shape. ProviderCodes 过渡保留（供应商解耦后分组不再携带 provider 身
// 份，路由侧恒传空集合）。
type ProviderModelRouteInput struct {
	Model           string
	ProviderCodes   []string
	SystemAccountID string
	IncludeUnpriced bool
}

// Provider model route outcomes (runtime-cache.service.ts
// ProviderModelRouteResolution). ambiguous 在 models 单表语义下不再产生，
// 常量过渡保留。
const (
	ProviderModelRouteMatched   = "matched"
	ProviderModelRouteMissing   = "missing"
	ProviderModelRouteAmbiguous = "ambiguous"
)

// ProviderModelRouteResolution mirrors ProviderModelRouteResolution. 供应商解
// 耦后承载 models 单表目录语义：matched/missing 二值，ProviderCode 与
// MatchedProviderCodes 恒空。
type ProviderModelRouteResolution struct {
	Outcome              string
	ModelKey             string
	ProviderCode         string // 供应商目录时代载体，models 语义下恒空
	MatchedProviderCodes []string
}
