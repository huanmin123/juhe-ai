package gatewaydispatch

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
)

// GPT account request overrides (BUG-0175 D-151), ported from
// providers/drivers/gpt/request-overrides.ts + request-override-capabilities.ts
// + request-override-body.ts:
//
//	credentials.service_tier_override / reasoning_effort_override are applied to
//	the upstream body at dispatch time, gated by the provider model catalog
//	capabilities (supportedServiceTiers / supportedReasoningEfforts). Without
//	capability evidence the overrides stay inert exactly like the Node
//	effectiveGptAccountRequestOverrides gate.
//
// The catalog read goes through the narrow GptRequestOverrideModelCatalog
// port (composition-root handover mirrors Node
// listCachedProviderModelCatalogAsync); a nil port keeps capability resolution
// returning undefined (overrides inert) instead of guessing.

// GptServiceTierOverride mirrors GptServiceTierOverride.
type GptServiceTierOverride = string

// GptReasoningEffortOverride mirrors GptReasoningEffortOverride.
type GptReasoningEffortOverride = string

// GptAccountRequestOverrides mirrors GptAccountRequestOverrides.
type GptAccountRequestOverrides struct {
	ServiceTier     GptServiceTierOverride
	ReasoningEffort GptReasoningEffortOverride
}

// gptAccountRequestOverrideCredentialPattern mirrors the optionalCredentialToken
// token shape /^[a-z0-9][a-z0-9._-]{0,63}$/i.
var gptAccountRequestOverrideCredentialPattern = regexp.MustCompile(`(?i)^[a-z0-9][a-z0-9._-]{0,63}$`)

// ReadGptAccountRequestOverrides mirrors readGptAccountRequestOverrides. Like
// the Node source it throws (returns an error) on malformed credential tokens.
func ReadGptAccountRequestOverrides(credentials map[string]any) (GptAccountRequestOverrides, error) {
	serviceTier, err := gptCredentialOverrideToken(credentials, "service_tier_override")
	if err != nil {
		return GptAccountRequestOverrides{}, err
	}
	reasoningEffort, err := gptCredentialOverrideToken(credentials, "reasoning_effort_override")
	if err != nil {
		return GptAccountRequestOverrides{}, err
	}
	return GptAccountRequestOverrides{
		ServiceTier:     serviceTier,
		ReasoningEffort: reasoningEffort,
	}, nil
}

// gptCredentialOverrideToken mirrors optionalCredentialToken.
func gptCredentialOverrideToken(credentials map[string]any, key string) (string, error) {
	if credentials == nil {
		return "", nil
	}
	raw, ok := credentials[key]
	if !ok || raw == nil {
		return "", nil
	}
	text, ok := raw.(string)
	if !ok {
		return "", &GptAccountRequestOverrideError{Message: "账户请求覆盖字段 " + key + " 无效"}
	}
	if text == "" {
		return "", nil
	}
	if text == strings.TrimSpace(text) && gptAccountRequestOverrideCredentialPattern.MatchString(text) {
		return text, nil
	}
	return "", &GptAccountRequestOverrideError{Message: "账户请求覆盖字段 " + key + " 无效"}
}

// AssertGptAccountRequestOverrideValues mirrors assertGptAccountRequestOverrideValues.
func AssertGptAccountRequestOverrideValues(overrides GptAccountRequestOverrides) error {
	if overrides.ServiceTier != "" && overrides.ServiceTier != "default" && overrides.ServiceTier != "priority" && overrides.ServiceTier != "flex" {
		return &GptAccountRequestOverrideError{Message: "GPT 账户请求覆盖字段 service_tier_override 无效"}
	}
	switch overrides.ReasoningEffort {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max":
	default:
		return &GptAccountRequestOverrideError{Message: "GPT 账户请求覆盖字段 reasoning_effort_override 无效"}
	}
	return nil
}

// HasApplicableGptAccountRequestOverrides mirrors hasApplicableGptAccountRequestOverrides.
func HasApplicableGptAccountRequestOverrides(overrides GptAccountRequestOverrides, endpointFamily string, compact bool) bool {
	if endpointFamily == "" {
		return false
	}
	return overrides.ServiceTier != "" || (!compact && overrides.ReasoningEffort != "")
}

// EffectiveGptAccountRequestOverrides mirrors effectiveGptAccountRequestOverrides.
func EffectiveGptAccountRequestOverrides(overrides GptAccountRequestOverrides, capabilities *GptRequestOverrideModelCapabilities) GptAccountRequestOverrides {
	if overrides.ServiceTier == "" && overrides.ReasoningEffort == "" {
		return GptAccountRequestOverrides{}
	}
	if capabilities == nil {
		return GptAccountRequestOverrides{}
	}
	effective := GptAccountRequestOverrides{}
	if overrides.ServiceTier != "" {
		supported := false
		if overrides.ServiceTier == "default" {
			supported = len(capabilities.SupportedServiceTiers) > 0
		} else {
			supported = gptOverrideListContains(capabilities.SupportedServiceTiers, overrides.ServiceTier)
		}
		if supported {
			effective.ServiceTier = overrides.ServiceTier
		}
	}
	if overrides.ReasoningEffort != "" && gptOverrideListContains(capabilities.SupportedReasoningEfforts, overrides.ReasoningEffort) {
		effective.ReasoningEffort = overrides.ReasoningEffort
	}
	return effective
}

// gptAccountRequestOverridesHook is the composition-root-injected override
// hook consumed by applyOpenAIOAuthCodexAccountRequestOverrides when the
// per-call input leaves the port unset. Set it through
// SetGptAccountRequestOverridesHook (newChainProviderDriver).
var gptAccountRequestOverridesHook func(body map[string]any, input GptAccountOverrideInput) (map[string]any, error)

// SetGptAccountRequestOverridesHooks wires the override hook (composition
// root). The expected value is ApplyGptAccountRequestOverridesBody.
func SetGptAccountRequestOverridesHook(hook func(body map[string]any, input GptAccountOverrideInput) (map[string]any, error)) {
	gptAccountRequestOverridesHook = hook
}

// ApplyGptAccountRequestOverridesBody mirrors applyGptAccountRequestOverrides.
// It is the hook value assigned to
// OpenAIOAuthCodexNormalizeInput.ApplyGptAccountRequestOverrides (the port the
// composition root previously left unwired).
func ApplyGptAccountRequestOverridesBody(inputBody map[string]any, input GptAccountOverrideInput) (map[string]any, error) {
	overrides, err := ReadGptAccountRequestOverrides(input.Credentials)
	if err != nil {
		return nil, err
	}
	if err := AssertGptAccountRequestOverrideValues(overrides); err != nil {
		return nil, err
	}
	effective := EffectiveGptAccountRequestOverrides(overrides, input.ModelCapabilities)
	if !HasApplicableGptAccountRequestOverrides(effective, input.EndpointFamily, input.Compact) {
		return inputBody, nil
	}
	body := cloneJSONObject(inputBody)
	if effective.ServiceTier == "default" {
		delete(body, "service_tier")
	} else if effective.ServiceTier != "" {
		body["service_tier"] = effective.ServiceTier
	}
	if input.Compact || effective.ReasoningEffort == "" {
		return body, nil
	}
	switch input.EndpointFamily {
	case "responses":
		reasoning := map[string]any{}
		if existing := bridgeObjectValueOf(body["reasoning"]); existing != nil {
			reasoning = existing
		}
		reasoning["effort"] = effective.ReasoningEffort
		body["reasoning"] = reasoning
		delete(body, "reasoning_effort")
	case "chat_completions":
		body["reasoning_effort"] = effective.ReasoningEffort
		delete(body, "reasoning")
	}
	return body, nil
}

// gptOverrideListContains reports whether values contains target verbatim.
func gptOverrideListContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func bridgeObjectValueOf(value any) map[string]any {
	if object, ok := value.(map[string]any); ok {
		return object
	}
	return nil
}

// ---------------------------------------------------------------------------
// Model capability resolution (request-override-capabilities.ts)
// ---------------------------------------------------------------------------

// GptRequestOverrideModelCatalogItem mirrors the catalog row fields the
// capability resolver consumes.
type GptRequestOverrideModelCatalogItem struct {
	Model                     string
	SupportedServiceTiers     []string
	SupportedReasoningEfforts []string
}

// GptRequestOverrideModelCatalog ports listCachedProviderModelCatalogAsync
// for the override capability resolver (composition-root handover).
type GptRequestOverrideModelCatalog interface {
	ListGptRequestOverrideModelCatalog(ctx context.Context, providerCode, systemAccountID string, includeUnpriced bool) ([]GptRequestOverrideModelCatalogItem, error)
}

// gptRequestOverrideModelCatalog is the injectable catalog port; nil keeps
// ResolveGptRequestOverrideModelCapabilities returning nil (overrides inert).
var gptRequestOverrideModelCatalog GptRequestOverrideModelCatalog

// gptRequestOverrideModelCandidates mirrors the
// modelPricingProviderDriverForProvider(providerCode).buildModelCandidates(model)
// fallback chain: the requested model plus vendor-equivalent spellings. The
// gateway side only needs the plain identity candidate unless a provider
// driver registers extra spellings.
var gptRequestOverrideModelCandidates = func(providerCode, model string) []string {
	candidates := []string{model}
	if normalized := strings.TrimSpace(model); normalized != model && normalized != "" {
		candidates = append(candidates, normalized)
	}
	return candidates
}

// SetGptRequestOverrideModelCatalog wires the catalog port (composition root).
func SetGptRequestOverrideModelCatalog(catalog GptRequestOverrideModelCatalog) {
	gptRequestOverrideModelCatalog = catalog
}

// SetGptRequestOverrideModelCandidates overrides the model-candidate expander
// (test / provider-driver handover).
func SetGptRequestOverrideModelCandidates(expander func(providerCode, model string) []string) {
	if expander != nil {
		gptRequestOverrideModelCandidates = expander
	}
}

// ResolveGptRequestOverrideModelCapabilities mirrors
// resolveGptRequestOverrideModelCapabilities.
func ResolveGptRequestOverrideModelCapabilities(ctx context.Context, account AccountCandidate, upstreamModel string) (*GptRequestOverrideModelCapabilities, error) {
	model := strings.TrimSpace(upstreamModel)
	if model == "" {
		return nil, nil
	}
	overrides, err := ReadGptAccountRequestOverrides(account.Credentials)
	if err != nil {
		return nil, err
	}
	if err := AssertGptAccountRequestOverrideValues(overrides); err != nil {
		return nil, err
	}
	if overrides.ServiceTier == "" && overrides.ReasoningEffort == "" {
		return nil, nil
	}
	providerCode := strings.TrimSpace(account.ProviderCode)
	if providerCode == "" {
		return nil, nil
	}
	if gptRequestOverrideModelCatalog == nil {
		return nil, nil
	}
	catalog, err := gptRequestOverrideModelCatalog.ListGptRequestOverrideModelCatalog(ctx, providerCode, account.AccountOwnerSystemAccountID, true)
	if err != nil {
		return nil, err
	}
	for _, candidate := range gptRequestOverrideModelCandidates(providerCode, model) {
		for _, item := range catalog {
			if strings.TrimSpace(item.Model) != candidate {
				continue
			}
			return &GptRequestOverrideModelCapabilities{
				SupportedServiceTiers:     item.SupportedServiceTiers,
				SupportedReasoningEfforts: item.SupportedReasoningEfforts,
			}, nil
		}
	}
	return nil, nil
}

// ApplyGptAccountRequestOverridesToUpstreamBody mirrors
// applyGptAccountRequestOverridesToBody for the gateway non-OAuth request
// path: body stays untouched unless applicable overrides survive the
// capability gate; otherwise the JSON object body is rewritten in place.
func ApplyGptAccountRequestOverridesToUpstreamBody(
	ctx context.Context,
	body []byte,
	account AccountCandidate,
	endpointFamily string,
	compact bool,
	upstreamModel string,
) ([]byte, error) {
	overrides, err := ReadGptAccountRequestOverrides(account.Credentials)
	if err != nil {
		return nil, err
	}
	if err := AssertGptAccountRequestOverrideValues(overrides); err != nil {
		return nil, err
	}
	if !HasApplicableGptAccountRequestOverrides(overrides, endpointFamily, compact) {
		return body, nil
	}
	model := strings.TrimSpace(upstreamModel)
	if model == "" {
		parsed := map[string]any{}
		if err := json.Unmarshal(body, &parsed); err != nil || parsed == nil {
			return nil, &GptAccountRequestOverrideError{Message: "账户请求覆盖要求请求体是有效的 JSON 对象"}
		}
		model, _ = parsed["model"].(string)
	}
	capabilities, err := ResolveGptRequestOverrideModelCapabilities(ctx, account, model)
	if err != nil {
		return nil, err
	}
	effective := EffectiveGptAccountRequestOverrides(overrides, capabilities)
	if !HasApplicableGptAccountRequestOverrides(effective, endpointFamily, compact) {
		return body, nil
	}
	parsed := map[string]any{}
	if err := json.Unmarshal(body, &parsed); err != nil || parsed == nil {
		return nil, &GptAccountRequestOverrideError{Message: "账户请求覆盖要求请求体是有效的 JSON 对象"}
	}
	overridden, err := ApplyGptAccountRequestOverridesBody(parsed, GptAccountOverrideInput{
		Credentials:       account.Credentials,
		EndpointFamily:    endpointFamily,
		Compact:           compact,
		ModelCapabilities: capabilities,
	})
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(overridden)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}
