package gatewaymodelcapability

import (
	"strings"

	gatewayprotocol "juhe-ai/backend-go/internal/protocols/gateway"
)

type EndpointFamily = gatewayprotocol.EndpointFamily

const (
	EndpointFamilyChatCompletions       = gatewayprotocol.EndpointChatCompletions
	EndpointFamilyResponses             = gatewayprotocol.EndpointResponses
	EndpointFamilyMessages              = gatewayprotocol.EndpointMessages
	EndpointFamilyGenerateContent       = gatewayprotocol.EndpointGenerateContent
	EndpointFamilyStreamGenerateContent = gatewayprotocol.EndpointStreamGenerateContent
)

type CapabilityMismatchReason string

const (
	CapabilityMismatchRequest                         CapabilityMismatchReason = "request_capability_mismatch"
	CapabilityMismatchNativeAnthropicOpenAICompatible CapabilityMismatchReason = "anthropic_native_group_openai_compatible_request"
)

type ModelMismatchReason string

const (
	ModelMismatchMissing     ModelMismatchReason = "missing_model"
	ModelMismatchUnsupported ModelMismatchReason = "unsupported_model"
)

type ModelPriorityRank int

const (
	ModelPriorityDirect ModelPriorityRank = iota
	ModelPriorityMapping
	ModelPriorityUnsupported
)

// Capability contains already-resolved account capabilities. Resolution belongs
// to the provider driver; filtering stays independent from HTTP and persistence.
type Capability struct {
	Registered             bool     `json:"registered"`
	ContextAllowed         bool     `json:"contextAllowed"`
	UpstreamRouteAvailable bool     `json:"upstreamRouteAvailable"`
	SupportedEndpointModes []string `json:"supportedEndpointModes"`
	ClientCompatibilities  []string `json:"clientCompatibilities"`
}

type ModelMapping struct {
	SourceModel            string         `json:"sourceModel"`
	SourceEndpointFamily   EndpointFamily `json:"sourceEndpointFamily"`
	UpstreamModel          string         `json:"upstreamModel"`
	UpstreamEndpointFamily EndpointFamily `json:"upstreamEndpointFamily"`
	Enabled                bool           `json:"enabled"`
	RuntimeSource          string         `json:"runtimeSource,omitempty"`
}

// ModelResolution is the exact runtime model/family decision for one
// candidate. A changed endpoint family still requires the caller to run the
// matching protocol bridge; this value never authorizes a model-only rewrite.
type ModelResolution struct {
	RequestedModel         string         `json:"requestedModel"`
	UpstreamModel          string         `json:"upstreamModel"`
	SourceEndpointFamily   EndpointFamily `json:"sourceEndpointFamily"`
	UpstreamEndpointFamily EndpointFamily `json:"upstreamEndpointFamily"`
	MappingApplied         bool           `json:"mappingApplied"`
	MappingSource          string         `json:"mappingSource,omitempty"`
}

type Candidate struct {
	ID                        string         `json:"id"`
	ProviderCode              string         `json:"providerCode"`
	ProviderProtocolProfileID string         `json:"providerProtocolProfileId"`
	ProtocolCode              string         `json:"protocolCode"`
	ProtocolVersion           string         `json:"protocolVersion"`
	Capability                Capability     `json:"capability"`
	SupportedModels           []string       `json:"supportedModels"`
	ModelMappings             []ModelMapping `json:"modelMappings"`
}

type CapabilityRequest struct {
	EndpointMode        string                   `json:"endpointMode"`
	ClientCompatibility string                   `json:"clientCompatibility"`
	EmptyReason         CapabilityMismatchReason `json:"emptyReason,omitempty"`
}

type CapabilityFilterResult struct {
	Candidates   []Candidate              `json:"candidates"`
	SkippedCount int                      `json:"skippedCount"`
	Reason       CapabilityMismatchReason `json:"reason,omitempty"`
}

type ModelPriority struct {
	RequestedModel       string                       `json:"requestedModel,omitempty"`
	SourceEndpointFamily EndpointFamily               `json:"sourceEndpointFamily,omitempty"`
	RankByCandidateID    map[string]ModelPriorityRank `json:"rankByCandidateId"`
}

type ModelFilterResult struct {
	Candidates                  []Candidate         `json:"candidates"`
	SkippedCount                int                 `json:"skippedCount"`
	LimitedAccountCount         int                 `json:"limitedAccountCount"`
	InvalidModelConstraintCount int                 `json:"invalidModelConstraintCount"`
	DirectMatchedCount          int                 `json:"directMatchedCount"`
	MappingMatchedCount         int                 `json:"mappingMatchedCount"`
	RequestedModel              string              `json:"requestedModel,omitempty"`
	SourceEndpointFamily        EndpointFamily      `json:"sourceEndpointFamily,omitempty"`
	Priority                    ModelPriority       `json:"priority"`
	Reason                      ModelMismatchReason `json:"reason,omitempty"`
}

type FilterInput struct {
	Candidates           []Candidate       `json:"candidates"`
	Capability           CapabilityRequest `json:"capability"`
	RequestedModel       string            `json:"requestedModel"`
	SourceEndpointFamily EndpointFamily    `json:"sourceEndpointFamily"`
}

type FilterResult struct {
	Candidates []Candidate            `json:"candidates"`
	Capability CapabilityFilterResult `json:"capability"`
	Model      ModelFilterResult      `json:"model"`
}

func FilterCandidates(input FilterInput) FilterResult {
	capability := FilterCapabilityCandidates(input.Candidates, input.Capability)
	if len(capability.Candidates) == 0 {
		return FilterResult{Candidates: []Candidate{}, Capability: capability}
	}
	model := FilterModelCandidates(capability.Candidates, input.RequestedModel, input.SourceEndpointFamily)
	return FilterResult{Candidates: model.Candidates, Capability: capability, Model: model}
}

func FilterCapabilityCandidates(candidates []Candidate, request CapabilityRequest) CapabilityFilterResult {
	filtered := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if capabilitySupportsRequest(candidate.Capability, request) {
			filtered = append(filtered, candidate)
		}
	}
	result := CapabilityFilterResult{
		Candidates:   filtered,
		SkippedCount: len(candidates) - len(filtered),
	}
	if len(candidates) > 0 && len(filtered) == 0 {
		result.Reason = request.EmptyReason
		if result.Reason == "" {
			result.Reason = CapabilityMismatchRequest
		}
	}
	return result
}

func FilterModelCandidates(candidates []Candidate, requestedModel string, sourceEndpointFamily EndpointFamily) ModelFilterResult {
	model := strings.TrimSpace(requestedModel)
	direct := make([]Candidate, 0, len(candidates))
	mapped := make([]Candidate, 0, len(candidates))
	ranks := make(map[string]ModelPriorityRank, len(candidates))
	result := ModelFilterResult{
		RequestedModel:       model,
		SourceEndpointFamily: sourceEndpointFamily,
		Priority: ModelPriority{
			RequestedModel:       model,
			SourceEndpointFamily: sourceEndpointFamily,
			RankByCandidateID:    ranks,
		},
	}

	for _, candidate := range candidates {
		if len(candidate.SupportedModels) == 0 {
			// An empty account model list is an invalid constraint, not an
			// unrestricted fallback. Routing it would silently send a request to
			// an account whose declared model capability is unknown.
			result.InvalidModelConstraintCount++
			result.SkippedCount++
			ranks[candidate.ID] = ModelPriorityUnsupported
			continue
		}
		result.LimitedAccountCount++
		if mapping, ok := ResolveModelMapping(candidate, model, sourceEndpointFamily); ok {
			if containsExact(candidate.SupportedModels, mapping.UpstreamModel) {
				result.MappingMatchedCount++
				ranks[candidate.ID] = ModelPriorityMapping
				mapped = append(mapped, candidate)
				continue
			}
			// Node never falls back to the source model after an applicable
			// mapping has been selected. Doing so would execute a request the
			// account's mapping explicitly redirected to an unsupported target.
			result.SkippedCount++
			ranks[candidate.ID] = ModelPriorityUnsupported
			continue
		}
		if model != "" && containsExact(candidate.SupportedModels, model) {
			result.DirectMatchedCount++
			ranks[candidate.ID] = ModelPriorityDirect
			direct = append(direct, candidate)
			continue
		}
		result.SkippedCount++
		ranks[candidate.ID] = ModelPriorityUnsupported
	}

	result.Candidates = make([]Candidate, 0, len(direct)+len(mapped))
	result.Candidates = append(result.Candidates, direct...)
	result.Candidates = append(result.Candidates, mapped...)
	if result.SkippedCount > 0 && len(result.Candidates) == 0 {
		if model == "" {
			result.Reason = ModelMismatchMissing
		} else {
			result.Reason = ModelMismatchUnsupported
		}
	}
	return result
}

func capabilitySupportsRequest(capability Capability, request CapabilityRequest) bool {
	if !capability.Registered || !capability.ContextAllowed || !capability.UpstreamRouteAvailable {
		return false
	}
	endpointMode := strings.TrimSpace(request.EndpointMode)
	if endpointMode != "" && !containsExact(capability.SupportedEndpointModes, endpointMode) {
		return false
	}
	compatibility := strings.TrimSpace(request.ClientCompatibility)
	return compatibility == "" || containsExact(capability.ClientCompatibilities, compatibility)
}

// ResolveModelMapping applies the same exact-match and conversion rules used
// by candidate filtering. Model names are case-sensitive by contract.
func ResolveModelMapping(candidate Candidate, requestedModel string, source EndpointFamily) (ModelMapping, bool) {
	model := strings.TrimSpace(requestedModel)
	if model == "" || !isMappingSourceFamily(source) {
		return ModelMapping{}, false
	}
	if candidate.ProviderProtocolProfileID == "profile_gemini_openai_chat_v1beta" && source == EndpointFamilyMessages {
		return ModelMapping{}, false
	}
	for _, mapping := range candidate.ModelMappings {
		if !mapping.Enabled || mapping.SourceModel != model || mapping.SourceEndpointFamily != source {
			continue
		}
		if mapping.RuntimeSource == "explicit_hybrid_route" ||
			mapping.UpstreamModel == mapping.SourceModel && mapping.UpstreamEndpointFamily == mapping.SourceEndpointFamily ||
			!mappingRuntimeConversionSupported(mapping, candidate) {
			return ModelMapping{}, false
		}
		return mapping, true
	}
	return ModelMapping{}, false
}

// ResolveEffectiveModel returns a direct or mapped runtime target only when
// the candidate's declared supported-model constraint proves it is usable.
func ResolveEffectiveModel(candidate Candidate, requestedModel string, source EndpointFamily) (ModelResolution, bool) {
	model := strings.TrimSpace(requestedModel)
	if model == "" || len(candidate.SupportedModels) == 0 {
		return ModelResolution{}, false
	}
	if isMappingSourceFamily(source) {
		mapping, ok := ResolveModelMapping(candidate, model, source)
		if ok {
			if containsExact(candidate.SupportedModels, mapping.UpstreamModel) {
				sourceName := strings.TrimSpace(mapping.RuntimeSource)
				if sourceName == "" {
					sourceName = "account"
				}
				return ModelResolution{
					RequestedModel: model, UpstreamModel: mapping.UpstreamModel,
					SourceEndpointFamily: source, UpstreamEndpointFamily: mapping.UpstreamEndpointFamily,
					MappingApplied: true, MappingSource: sourceName,
				}, true
			}
			return ModelResolution{}, false
		}
	}
	if !containsExact(candidate.SupportedModels, model) {
		return ModelResolution{}, false
	}
	return ModelResolution{
		RequestedModel: model, UpstreamModel: model,
		SourceEndpointFamily: source, UpstreamEndpointFamily: source,
	}, true
}

func mappingRuntimeConversionSupported(mapping ModelMapping, candidate Candidate) bool {
	source := mapping.SourceEndpointFamily
	upstream := mapping.UpstreamEndpointFamily
	if source == upstream || source == EndpointFamilyStreamGenerateContent && upstream == EndpointFamilyGenerateContent {
		return true
	}
	if source == EndpointFamilyResponses && upstream == EndpointFamilyChatCompletions &&
		normalizeToken(candidate.ProtocolCode) == "openai" && normalizeToken(candidate.ProtocolVersion) == "v1" {
		return true
	}
	if normalizeToken(candidate.ProviderCode) != "hybrid" {
		return false
	}
	return source == EndpointFamilyResponses && upstream == EndpointFamilyChatCompletions ||
		source == EndpointFamilyMessages && upstream == EndpointFamilyChatCompletions ||
		(source == EndpointFamilyGenerateContent || source == EndpointFamilyStreamGenerateContent) && upstream == EndpointFamilyChatCompletions ||
		source == EndpointFamilyChatCompletions && upstream == EndpointFamilyMessages ||
		source == EndpointFamilyResponses && upstream == EndpointFamilyMessages ||
		(source == EndpointFamilyGenerateContent || source == EndpointFamilyStreamGenerateContent) && upstream == EndpointFamilyMessages ||
		source == EndpointFamilyChatCompletions && upstream == EndpointFamilyGenerateContent ||
		source == EndpointFamilyResponses && upstream == EndpointFamilyGenerateContent ||
		source == EndpointFamilyMessages && upstream == EndpointFamilyGenerateContent
}

func isMappingSourceFamily(value EndpointFamily) bool {
	switch value {
	case EndpointFamilyChatCompletions, EndpointFamilyResponses, EndpointFamilyMessages, EndpointFamilyGenerateContent, EndpointFamilyStreamGenerateContent:
		return true
	default:
		return false
	}
}

func containsExact(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func normalizeToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
