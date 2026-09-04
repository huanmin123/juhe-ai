package oauthmgmt

import (
	"context"
	"net/url"
	"strings"
)

// providerPlan is the per-provider route/service adapter: identity constants,
// allowed body keys, the credential-patch parser and the token exchange
// closures over the provider services.
type providerPlan struct {
	slug               string // route segment, e.g. "openai"
	module             string // operation-log module, e.g. "openai_oauth"
	providerCode       string // gpt / anthropic / gemini / xai
	accountType        string // oauth / google_oauth
	label              string // route copy label
	defaultAccountName string
	emailNameFallback  bool
	requiredProfileID  string // grok pins profile_xai_openai_v1
	preserveBaseURL    bool   // anthropic/gemini/grok keep the stored base_url
	capabilities       bool   // gemini GET /capabilities
	sso                bool   // grok POST /sso-to-oauth
	// revisionConflictMessage overrides the 409 copy when a provider renders a
	// specific message (openai/gemini); empty falls back to the route message.
	revisionConflictMessage string

	authURLKeys       []string
	createCodeKeys    []string
	createRefreshKeys []string
	reauthCodeKeys    []string
	reauthRefreshKeys []string

	parseCredentialsPatch func(body map[string]any) (map[string]any, bool)

	authURL         func(ctx context.Context, s *Store, body map[string]any, ownerID string) (map[string]any, error)
	exchangeCode    func(ctx context.Context, s *Store, body map[string]any, ownerID string) (*tokenOutcome, error)
	exchangeRefresh func(ctx context.Context, s *Store, body map[string]any) (*tokenOutcome, error)
	refreshStored   func(ctx context.Context, s *Store, current *rotationAccount) (map[string]any, error)
	refreshInput    func(ctx context.Context, s *Store, body map[string]any, current *rotationAccount) (map[string]any, error)
}

// rotatable mirrors findRotatableXxxOAuthAccount: provider code, protocol
// profile family, account type (and the grok exact profile pin).
func (p providerPlan) rotatable(account *rotationAccount) bool {
	if account.ProviderCode != p.providerCode || account.Type != p.accountType {
		return false
	}
	if p.requiredProfileID != "" {
		return account.ProviderProtocolProfileID == p.requiredProfileID
	}
	return account.ProtocolCode == protocolCodeForProvider(p.providerCode) &&
		account.ProtocolVersion == protocolVersionForProvider(p.providerCode)
}

// revisionMessage renders the 409 copy: openai/gemini pin the dedicated
// message, anthropic/grok render the route fallback (Node handleOAuthAccountUpdateError).
func (p providerPlan) revisionMessage(fallback string) string {
	if p.revisionConflictMessage != "" {
		return p.revisionConflictMessage
	}
	return fallback
}

// safePatch extracts credentialsPatch via the plan parser; absent/nil passes.
func safePatch(p providerPlan, body map[string]any) (map[string]any, bool) {
	value, present := body["credentialsPatch"]
	if !present || value == nil {
		return map[string]any{}, true
	}
	patch, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	return p.parseCredentialsPatch(patch)
}

// mergePatchOpenAI mirrors buildSafeOpenAIOAuthCredentials: the token-built
// credentials win over the patch ({...patch, ...credentials}).
func mergePatchOpenAI(patch, credentials map[string]any) map[string]any {
	merged := map[string]any{}
	for key, value := range patch {
		merged[key] = value
	}
	for key, value := range credentials {
		merged[key] = value
	}
	return merged
}

// mergePatchLast mirrors the anthropic/gemini/grok buildSafe helpers: the patch
// wins over the token-built credentials.
func mergePatchLast(credentials, patch map[string]any) map[string]any {
	merged := map[string]any{}
	for key, value := range credentials {
		merged[key] = value
	}
	for key, value := range patch {
		merged[key] = value
	}
	return merged
}

// mergeRotationCredentials mirrors buildReauthorizedOpenAIOAuthCredentials and
// its per-provider siblings: {...current, ...token} plus the stored base_url
// preservation for anthropic/gemini/grok.
func mergeRotationCredentials(current, token map[string]any, preserveBaseURL bool) map[string]any {
	merged := map[string]any{}
	for key, value := range current {
		merged[key] = value
	}
	for key, value := range token {
		merged[key] = value
	}
	if preserveBaseURL {
		if base := stringCredential(current, "base_url"); base != "" {
			merged["base_url"] = base
		}
	}
	return merged
}

// isOpenAIBlockedErrorAccount mirrors isBlockedOpenAIOAuthErrorAccount with the
// managed refresh error codes (oauth_token_refresh_failed,
// oauth_token_refresh_local_configuration_invalid).
func isOpenAIBlockedErrorAccount(account *rotationAccount) bool {
	if account.Status != "error" {
		return false
	}
	switch account.LastErrorCode {
	case "oauth_token_refresh_failed", "oauth_token_refresh_local_configuration_invalid":
		return false
	}
	return true
}

// --- credential patch parsers ----------------------------------------------

// policyPatchKeys are the free-form policy fields accepted but not validated by
// this slice (the policy validators belong to the accounts companion slice).
var policyPatchKeys = []string{"error_handling_rules", "response_inspection_rules", "quota_recovery_policy"}

func endpointModes(value any) ([]string, bool) {
	if value == nil {
		return nil, true
	}
	return parseStringSlice(value, 20)
}

// parseOpenAICredentialsPatch mirrors the openai oauthCredentialsPatchSchema.
func parseOpenAICredentialsPatch(patch map[string]any) (map[string]any, bool) {
	output := map[string]any{}
	for key, value := range patch {
		switch key {
		case "supported_endpoint_modes":
			modes, ok := endpointModes(value)
			if !ok || len(modes) == 0 {
				return nil, false
			}
			output[key] = modes
		case "service_tier_override":
			if text, ok := value.(string); !ok {
				return nil, false
			} else if text = strings.TrimSpace(text); text != "" {
				switch text {
				case "default", "priority", "flex":
					output[key] = text
				default:
					return nil, false
				}
			} else {
				return nil, false
			}
		case "reasoning_effort_override":
			if text, ok := value.(string); !ok {
				return nil, false
			} else if text = strings.TrimSpace(text); text != "" {
				switch text {
				case "none", "minimal", "low", "medium", "high", "xhigh", "max":
					output[key] = text
				default:
					return nil, false
				}
			} else {
				return nil, false
			}
		case "error_handling_rules", "response_inspection_rules", "quota_recovery_policy":
			output[key] = value
		default:
			return nil, false
		}
	}
	return output, true
}

// parseAnthropicCredentialsPatch mirrors the anthropic patch schema.
func parseAnthropicCredentialsPatch(patch map[string]any) (map[string]any, bool) {
	output := map[string]any{}
	for key, value := range patch {
		switch key {
		case "base_url", "service_tier_override", "reasoning_effort_override":
			text, ok := value.(string)
			if !ok || strings.TrimSpace(text) == "" {
				return nil, false
			}
			output[key] = strings.TrimSpace(text)
		case "supported_endpoint_modes":
			modes, ok := endpointModes(value)
			if !ok || len(modes) == 0 {
				return nil, false
			}
			output[key] = modes
		case policyPatchKeys[0], policyPatchKeys[1], policyPatchKeys[2]:
			output[key] = value
		default:
			return nil, false
		}
	}
	return output, true
}

// parseGeminiCredentialsPatch mirrors the gemini patch schema (base_url must be
// a URL).
func parseGeminiCredentialsPatch(patch map[string]any) (map[string]any, bool) {
	output := map[string]any{}
	for key, value := range patch {
		switch key {
		case "quota_project_id", "service_tier_override", "reasoning_effort_override":
			text, ok := value.(string)
			if !ok || strings.TrimSpace(text) == "" {
				return nil, false
			}
			output[key] = strings.TrimSpace(text)
		case "base_url":
			text, ok := value.(string)
			if !ok || !isHTTPURL(strings.TrimSpace(text)) {
				return nil, false
			}
			output[key] = strings.TrimSpace(text)
		case "supported_endpoint_modes":
			modes, ok := endpointModes(value)
			if !ok || len(modes) == 0 {
				return nil, false
			}
			output[key] = modes
		case policyPatchKeys[0], policyPatchKeys[1], policyPatchKeys[2]:
			output[key] = value
		default:
			return nil, false
		}
	}
	return output, true
}

// parseGrokCredentialsPatch mirrors the grok patch schema.
func parseGrokCredentialsPatch(patch map[string]any) (map[string]any, bool) {
	output := map[string]any{}
	for key, value := range patch {
		switch key {
		case "base_url":
			text, ok := value.(string)
			if !ok || strings.TrimSpace(text) == "" {
				return nil, false
			}
			output[key] = strings.TrimSpace(text)
		case "supported_endpoint_modes":
			modes, ok := endpointModes(value)
			if !ok || len(modes) == 0 {
				return nil, false
			}
			output[key] = modes
		case policyPatchKeys[0], policyPatchKeys[1], policyPatchKeys[2]:
			output[key] = value
		default:
			return nil, false
		}
	}
	return output, true
}

func trim(value string) string { return strings.TrimSpace(value) }

func isHTTPURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}
