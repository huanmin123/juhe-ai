// Package gatewayfallbackcapability supplies the concrete, side-effect-free
// provider request-capability gate used by W10 cross-group fallback policy.
package gatewayfallbackcapability

import (
	"context"
	"fmt"
	"strings"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/modules/gatewayfallbackpolicy"
	"juhe-ai/backend-go/internal/modules/gatewayupstream"
	protocolgateway "juhe-ai/backend-go/internal/protocols/gateway"
	"juhe-ai/backend-go/internal/store/port"
)

var knownEndpointModes = map[string]struct{}{
	"chat_json": {}, "chat_sse": {}, "responses_json": {}, "responses_sse": {},
	"messages_json": {}, "messages_sse": {}, "message_token_counting": {},
	"generate_content_json": {}, "generate_content_sse": {}, "interactions_json": {},
	"interactions_sse": {}, "count_tokens": {}, "embed_content": {},
}

var supportedProviderCodes = map[string]struct{}{
	"openai": {}, "gpt": {}, "xai": {}, "deepseek": {}, "glm": {},
	"anthropic": {}, "gemini": {}, "hybrid": {},
}

type providerProfileIdentity struct {
	provider string
	protocol protocolgateway.ProtocolCode
	version  string
}

// These are the provider protocol profile identities actually seeded by the
// Go catalog. The generic protocol registry proves only a wire protocol; it
// cannot grant a provider/profile pair permission to execute a request.
var supportedProviderProfiles = map[string]providerProfileIdentity{
	"profile_openai_openai_v1":             {provider: "openai", protocol: protocolgateway.ProtocolOpenAI, version: "v1"},
	"profile_gpt_openai_v1":                {provider: "gpt", protocol: protocolgateway.ProtocolOpenAI, version: "v1"},
	"profile_xai_openai_v1":                {provider: "xai", protocol: protocolgateway.ProtocolOpenAI, version: "v1"},
	"profile_deepseek_openai_v1":           {provider: "deepseek", protocol: protocolgateway.ProtocolOpenAI, version: "v1"},
	"profile_deepseek_anthropic_v1":        {provider: "deepseek", protocol: protocolgateway.ProtocolAnthropic, version: "v1"},
	"profile_glm_general_openai_v1":        {provider: "glm", protocol: protocolgateway.ProtocolOpenAI, version: "v1"},
	"profile_glm_coding_openai_v1":         {provider: "glm", protocol: protocolgateway.ProtocolOpenAI, version: "v1"},
	"profile_glm_coding_anthropic_v1":      {provider: "glm", protocol: protocolgateway.ProtocolAnthropic, version: "v1"},
	"profile_anthropic_anthropic_v1":       {provider: "anthropic", protocol: protocolgateway.ProtocolAnthropic, version: "v1"},
	"profile_gemini_native_v1beta":         {provider: "gemini", protocol: protocolgateway.ProtocolGemini, version: "v1beta"},
	"profile_gemini_openai_chat_v1beta":    {provider: "gemini", protocol: protocolgateway.ProtocolOpenAI, version: "v1"},
	"profile_hybrid_openai_chat_v1":        {provider: "hybrid", protocol: protocolgateway.ProtocolOpenAI, version: "v1"},
	"profile_hybrid_anthropic_messages_v1": {provider: "hybrid", protocol: protocolgateway.ProtocolAnthropic, version: "v1"},
}

// Service is deliberately not a dispatcher: Build only proves that a target
// request is constructible with the hydrated public facts. It uses a synthetic
// credential and makes no network request or lease/claim mutation.
type Service struct {
	builder    gatewayupstream.Builder
	credential gatewayupstream.Credential
}

func NewService() (*Service, error) {
	credential, err := gatewayupstream.NewCredential("fallback-capability-probe", gatewayupstream.CredentialOptions{AuthMode: gatewayupstream.AuthAuto})
	if err != nil {
		return nil, fmt.Errorf("create fallback capability probe credential: %w", err)
	}
	return &Service{credential: credential}, nil
}

// FilterFallbackCapability implements Node's accountSupportsGatewayRequest
// gate with the Go runtime's actual three-protocol registry and request
// builder. Unsupported Node bridge paths are excluded until their Go request
// transformation and response bridge exist; they cannot become implicit
// OpenAI-compatible fallbacks.
func (s *Service) FilterFallbackCapability(ctx context.Context, input gatewayfallbackpolicy.CapabilityInput) (gatewayfallbackpolicy.AccountSelection, error) {
	if s == nil {
		return gatewayfallbackpolicy.AccountSelection{}, fmt.Errorf("gateway fallback capability service is not configured")
	}
	if ctx == nil {
		return gatewayfallbackpolicy.AccountSelection{}, fmt.Errorf("gateway fallback capability context is required")
	}
	if strings.TrimSpace(input.RequestClientCompatibility) == "" {
		return gatewayfallbackpolicy.AccountSelection{}, fmt.Errorf("gateway fallback request client compatibility is required")
	}
	selected := make([]string, 0, len(input.Candidates))
	for _, candidate := range input.Candidates {
		supported, err := s.candidateSupports(ctx, input, candidate)
		if err != nil {
			return gatewayfallbackpolicy.AccountSelection{}, err
		}
		if supported {
			selected = append(selected, candidate.Projection.AccountID)
		}
	}
	return gatewayfallbackpolicy.AccountSelection{CandidateAccountIDs: selected}, nil
}

func (s *Service) candidateSupports(ctx context.Context, input gatewayfallbackpolicy.CapabilityInput, candidate gatewaycandidatewindow.Candidate) (bool, error) {
	projection := candidate.Projection
	accountID := strings.TrimSpace(projection.AccountID)
	if accountID == "" {
		return false, fmt.Errorf("fallback capability candidate has no account id")
	}
	// Node validates request-local Codex bridge state before it finds a driver.
	// Go has not migrated that state, chain restore, or completion bridge, so a
	// Codex request cannot be treated as an ordinary OpenAI-compatible request.
	if strings.EqualFold(strings.TrimSpace(input.RequestClientCompatibility), string(protocolgateway.CompatibilityCodexResponses)) {
		return false, nil
	}
	providerCode, profileID, profile := effectiveProfile(projection)
	if _, known := supportedProviderCodes[providerCode]; !known {
		return false, nil // Node's providerDriverForAccount returns undefined.
	}
	definition, registered := protocolgateway.DefinitionForProfile(profile)
	if !registered || definition.Code != input.Protocol {
		return false, nil
	}
	profileIdentity, knownProfile := supportedProviderProfiles[profileID]
	if !knownProfile || profileIdentity.provider != providerCode || profileIdentity.protocol != definition.Code || profileIdentity.version != definition.Version {
		return false, nil
	}
	if !candidate.EndpointModesComplete {
		return false, nil
	}
	if err := validateEndpointModes(candidate.SupportedEndpointModes); err != nil {
		return false, fmt.Errorf("fallback capability candidate %q endpoint modes: %w", accountID, err)
	}
	if mappingRequiresUnavailableBridge(candidate, input.RequestedModel, input.EndpointFamily) {
		return false, nil
	}
	mode, modeKnown := endpointMode(input.RequestShape, definition.Code)
	if !modeKnown {
		return false, nil
	}
	if !contains(candidate.SupportedEndpointModes, mode) {
		return false, nil
	}
	if !providerAllows(providerCode, projection, input.RequestShape, input.RequestClientCompatibility) {
		return false, nil
	}
	if _, _, err := s.builder.Build(gatewayupstream.Input{
		Context: ctx, Request: input.RequestShape, Candidate: projection, BaseURL: candidate.DefaultBaseURL, Credential: s.credential,
	}); err != nil {
		return false, nil
	}
	return true, nil
}

func effectiveProfile(candidate port.GatewayAccountCandidate) (string, string, protocolgateway.Profile) {
	if strings.TrimSpace(candidate.ResourceAccountID) != "" {
		return strings.ToLower(strings.TrimSpace(candidate.ResourceProviderCode)), strings.TrimSpace(candidate.ResourceProviderProtocolProfileID), protocolgateway.Profile{
			Code: candidate.ResourceProtocolCode, Version: candidate.ResourceProtocolVersion,
		}
	}
	return strings.ToLower(strings.TrimSpace(candidate.ProviderCode)), strings.TrimSpace(candidate.ProviderProtocolProfileID), protocolgateway.Profile{
		Code: candidate.ProtocolCode, Version: candidate.ProtocolVersion,
	}
}

func endpointMode(request protocolgateway.RequestShape, protocol protocolgateway.ProtocolCode) (string, bool) {
	method := strings.ToUpper(strings.TrimSpace(request.Method))
	family := protocolgateway.EndpointFamilyFromPath(protocol, request.Path)
	downstream := protocolgateway.ResolveDownstreamProtocol(protocol, request)
	switch protocol {
	case protocolgateway.ProtocolOpenAI:
		if method != "POST" {
			return "", false
		}
		switch family {
		case protocolgateway.EndpointChatCompletions:
			if downstream == protocolgateway.DownstreamChatCompletionsSSE {
				return "chat_sse", true
			}
			if downstream == protocolgateway.DownstreamJSON {
				return "chat_json", true
			}
		case protocolgateway.EndpointResponses:
			if downstream == protocolgateway.DownstreamResponsesSSE {
				return "responses_sse", true
			}
			if downstream == protocolgateway.DownstreamJSON {
				return "responses_json", true
			}
		}
	case protocolgateway.ProtocolAnthropic:
		if method != "POST" {
			return "", false
		}
		switch family {
		case protocolgateway.EndpointMessages:
			if downstream == protocolgateway.DownstreamMessagesSSE {
				return "messages_sse", true
			}
			if downstream == protocolgateway.DownstreamJSON {
				return "messages_json", true
			}
		case protocolgateway.EndpointMessageTokenCounting:
			if !request.Stream {
				return "message_token_counting", true
			}
		}
	case protocolgateway.ProtocolGemini:
		switch family {
		case protocolgateway.EndpointGenerateContent:
			if method == "POST" {
				if downstream == protocolgateway.DownstreamGeminiGenerateContentSSE {
					return "generate_content_sse", true
				}
				if downstream == protocolgateway.DownstreamJSON {
					return "generate_content_json", true
				}
			}
		case protocolgateway.EndpointStreamGenerateContent:
			if method == "POST" && downstream == protocolgateway.DownstreamGeminiGenerateContentSSE {
				return "generate_content_sse", true
			}
		case protocolgateway.EndpointInteractions:
			if protocolgateway.GeminiInteractionActionForRequest(method, request.Path) != protocolgateway.GeminiInteractionNone {
				if downstream == protocolgateway.DownstreamGeminiInteractionsSSE {
					return "interactions_sse", true
				}
				if downstream == protocolgateway.DownstreamJSON {
					return "interactions_json", true
				}
			}
		case protocolgateway.EndpointCountTokens:
			if method == "POST" && !request.Stream {
				return "count_tokens", true
			}
		case protocolgateway.EndpointEmbedContent:
			if method == "POST" && !request.Stream {
				return "embed_content", true
			}
		}
	}
	return "", false
}

func mappingRequiresUnavailableBridge(candidate gatewaycandidatewindow.Candidate, model, endpointFamily string) bool {
	_, configured := gatewaycandidatewindow.ResolveConfiguredModelMapping(candidate, model, endpointFamily)
	return configured
}

func providerAllows(providerCode string, candidate port.GatewayAccountCandidate, request protocolgateway.RequestShape, requestCompatibility string) bool {
	accountType := strings.ToLower(strings.TrimSpace(candidate.Type))
	if strings.TrimSpace(candidate.ResourceAccountID) != "" {
		accountType = strings.ToLower(strings.TrimSpace(candidate.ResourceType))
	}
	family := protocolgateway.EndpointFamilyFromPath(protocolgateway.ProtocolOpenAI, request.Path)
	switch providerCode {
	case "gpt":
		return accountType != "oauth" || (strings.EqualFold(strings.TrimSpace(requestCompatibility), string(protocolgateway.CompatibilityCodexResponses)) && family == protocolgateway.EndpointResponses)
	case "xai":
		if accountType != "api_key" && accountType != "oauth" {
			return false
		}
		return accountType != "oauth" || family == protocolgateway.EndpointResponses
	default:
		return true
	}
}

func validateEndpointModes(modes []string) error {
	if len(modes) == 0 {
		return fmt.Errorf("supported endpoint modes are missing")
	}
	seen := make(map[string]struct{}, len(modes))
	for _, mode := range modes {
		if strings.TrimSpace(mode) != mode {
			return fmt.Errorf("supported endpoint mode has surrounding whitespace: %q", mode)
		}
		if _, known := knownEndpointModes[mode]; !known {
			return fmt.Errorf("supported endpoint mode is invalid: %q", mode)
		}
		if _, duplicate := seen[mode]; duplicate {
			return fmt.Errorf("supported endpoint mode is duplicated: %q", mode)
		}
		seen[mode] = struct{}{}
	}
	return nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

var _ gatewayfallbackpolicy.CapabilityFilter = (*Service)(nil)
