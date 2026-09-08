package chat

import (
	"regexp"
	"strings"
)

// Chat generation-parameter capability plumbing, the port of
// backend/src/modules/chat/chat-generation-parameters.ts +
// chat-model-options.ts flattenGenerationParameters + the
// chat.routes.ts constrainChatModelOptionForAccounts parameter intersection
// (BUG-0175 D-185). GET models fills ChatModelOption.GenerationParameters and
// stream requests validate against the (route-constrained) capability list
// instead of rejecting every parameter.

const (
	generationProtocolChatCompletions = "chat_completions"
	generationProtocolResponses       = "responses"
)

// chatGenerationRouteAccount mirrors ChatGenerationRouteAccount: the account
// subset routeCapabilityForAccount reads. ChatTransportAccount carries the
// same fields, so the transport snapshot plugs in directly.
type chatGenerationRouteAccount = ChatTransportAccount

// generationParameterDefinitions mirrors the definitions table. The step
// column never leaves this package (the option payload exposes
// parameter/min/max/defaultValue), so it is omitted from the capability
// struct.
var generationParameterDefinitions = map[string]ChatGenerationParameterCapability{
	"temperature":      {Parameter: "temperature", Min: 0, Max: 2, DefaultValue: 1},
	"topP":             {Parameter: "topP", Min: 0, Max: 1, DefaultValue: 1},
	"frequencyPenalty": {Parameter: "frequencyPenalty", Min: -2, Max: 2, DefaultValue: 0},
	"presencePenalty":  {Parameter: "presencePenalty", Min: -2, Max: 2, DefaultValue: 0},
	"maxOutputTokens":  {Parameter: "maxOutputTokens", Min: 1, Max: 128_000, DefaultValue: 4_096},
	"seed":             {Parameter: "seed", Min: 0, Max: 2_147_483_647, DefaultValue: 0},
}

var generationParameterNames = []string{"temperature", "topP", "frequencyPenalty", "presencePenalty", "maxOutputTokens", "seed"}

// generationParameterCapabilitiesForModel mirrors
// generationParameterCapabilitiesForModel: the per-provider protocol →
// capability table (normalized provider token, lowercase model).
func generationParameterCapabilitiesForModel(providerCode, model string, maxOutputTokens *int64) map[string][]ChatGenerationParameterCapability {
	normalized := normalizeProviderToken(providerCode)
	modelName := strings.ToLower(strings.TrimSpace(model))
	capability := func(parameter string) ChatGenerationParameterCapability {
		definition, ok := generationParameterDefinitions[parameter]
		if !ok {
			return ChatGenerationParameterCapability{Parameter: parameter}
		}
		if parameter == "maxOutputTokens" && maxOutputTokens != nil && *maxOutputTokens > 0 {
			if float64(*maxOutputTokens) < definition.Max {
				definition.Max = float64(*maxOutputTokens)
			}
			if definition.DefaultValue > definition.Max {
				definition.DefaultValue = definition.Max
			}
		}
		return definition
	}
	selectParameters := func(parameters ...string) []ChatGenerationParameterCapability {
		output := make([]ChatGenerationParameterCapability, 0, len(parameters))
		for _, parameter := range parameters {
			output = append(output, capability(parameter))
		}
		return output
	}

	switch normalized {
	case "gpt":
		// Responses has no documented penalty or seed fields. Keep GPT-5
		// conservative because several reasoning variants reject non-default
		// sampling values.
		if strings.HasPrefix(modelName, "gpt-5") {
			return map[string][]ChatGenerationParameterCapability{
				generationProtocolChatCompletions: selectParameters("frequencyPenalty", "presencePenalty", "maxOutputTokens", "seed"),
				generationProtocolResponses:       selectParameters("maxOutputTokens"),
			}
		}
		return map[string][]ChatGenerationParameterCapability{
			generationProtocolChatCompletions: selectParameters(generationParameterNames...),
			generationProtocolResponses:       selectParameters("temperature", "topP", "maxOutputTokens"),
		}
	case "xai":
		reasoning := regexpReasoning.MatchString(modelName)
		chat := selectParameters(generationParameterNames...)
		if reasoning {
			chat = selectParameters("temperature", "topP", "maxOutputTokens", "seed")
		}
		return map[string][]ChatGenerationParameterCapability{
			generationProtocolChatCompletions: chat,
			generationProtocolResponses:       selectParameters("temperature", "topP", "maxOutputTokens"),
		}
	case "deepseek":
		// V4 defaults to thinking; its sampling fields are accepted but ignored
		// in that mode.
		if modelName == "deepseek-chat" {
			return map[string][]ChatGenerationParameterCapability{
				generationProtocolChatCompletions: selectParameters("temperature", "topP", "maxOutputTokens"),
			}
		}
		return map[string][]ChatGenerationParameterCapability{
			generationProtocolChatCompletions: selectParameters("maxOutputTokens"),
		}
	case "anthropic":
		// Recent Claude models reject non-default sampling values; only output
		// length is safe to expose without the account's exact
		// thinking/model-version policy.
		if regexpAnthropicSamplingRejected.MatchString(modelName) {
			return map[string][]ChatGenerationParameterCapability{
				generationProtocolChatCompletions: selectParameters("maxOutputTokens"),
			}
		}
		return map[string][]ChatGenerationParameterCapability{
			generationProtocolChatCompletions: selectParameters("temperature", "topP", "maxOutputTokens"),
		}
	case "gemini":
		// The current OpenAI-Gemini bridge only preserves these three fields.
		if regexpGemini3SamplingDeprecated.MatchString(modelName) {
			return map[string][]ChatGenerationParameterCapability{
				generationProtocolChatCompletions: selectParameters("maxOutputTokens"),
				generationProtocolResponses:       selectParameters("maxOutputTokens"),
			}
		}
		sampling := selectParameters("temperature", "topP", "maxOutputTokens")
		return map[string][]ChatGenerationParameterCapability{
			generationProtocolChatCompletions: append([]ChatGenerationParameterCapability{}, sampling...),
			generationProtocolResponses:       sampling,
		}
	case "glm":
		output := make([]ChatGenerationParameterCapability, 0, 3)
		for _, parameter := range []string{"temperature", "topP", "maxOutputTokens"} {
			entry := capability(parameter)
			if parameter == "temperature" {
				entry.Max = 1
			}
			if parameter == "topP" {
				entry.Min = 0.01
			}
			output = append(output, entry)
		}
		return map[string][]ChatGenerationParameterCapability{
			generationProtocolChatCompletions: output,
		}
	}
	return map[string][]ChatGenerationParameterCapability{}
}

var (
	// /(?:reasoning|think)/
	regexpReasoning = regexp.MustCompile(`(?:reasoning|think)`)
	// /(?:claude-(?:opus|sonnet)-4\.(?:7|8)|(?:fable|mythos|opus|sonnet)-5)/
	regexpAnthropicSamplingRejected = regexp.MustCompile(`(?:claude-(?:opus|sonnet)-4\.(?:7|8)|(?:fable|mythos|opus|sonnet)-5)`)
	// /(?:^|[-_.])gemini-3(?:[-_.]|$)/
	regexpGemini3SamplingDeprecated = regexp.MustCompile(`(?:^|[-_.])gemini-3(?:[-_.]|$)`)
)

// limitGenerationParameterMaxOutputTokens mirrors the same-named helper: the
// maxOutputTokens entry is clamped (or dropped when the clamp violates min).
func limitGenerationParameterMaxOutputTokens(capabilities map[string][]ChatGenerationParameterCapability, maxOutputTokens *int64) map[string][]ChatGenerationParameterCapability {
	if maxOutputTokens == nil || *maxOutputTokens <= 0 {
		return capabilities
	}
	limit := float64(*maxOutputTokens)
	output := make(map[string][]ChatGenerationParameterCapability, len(capabilities))
	for protocol, items := range capabilities {
		limited := make([]ChatGenerationParameterCapability, 0, len(items))
		for _, item := range items {
			if item.Parameter != "maxOutputTokens" {
				limited = append(limited, item)
				continue
			}
			max := item.Max
			if limit < max {
				max = limit
			}
			if max < item.Min {
				continue
			}
			item.Max = max
			if item.DefaultValue > max {
				item.DefaultValue = max
			}
			limited = append(limited, item)
		}
		output[protocol] = limited
	}
	return output
}

// catalogItemGenerationParameterCapabilities resolves a catalog row's
// per-protocol capability table. The archive derives it when the catalog
// snapshot is assembled (model-catalog.service.ts toBuiltInCatalogItem /
// toCustomCatalogItem: limitGenerationParameterMaxOutputTokens(
// generationParameterCapabilitiesForModel(...), maxOutputTokens)); the Go
// catalog port renders the same derivation here, with an explicit row field
// taking precedence once the composition root populates it.
func catalogItemGenerationParameterCapabilities(item ProviderModelCatalogItem) map[string][]ChatGenerationParameterCapability {
	if item.GenerationParameterCapabilities != nil {
		return item.GenerationParameterCapabilities
	}
	return limitGenerationParameterMaxOutputTokens(
		generationParameterCapabilitiesForModel(item.ProviderCode, item.Model, item.MaxOutputTokens),
		item.MaxOutputTokens)
}

// intersectGenerationParameterCapabilities mirrors the same-named helper:
// per-protocol parameter intersection with the widest min / narrowest max.
func intersectGenerationParameterCapabilities(items []map[string][]ChatGenerationParameterCapability) map[string][]ChatGenerationParameterCapability {
	if len(items) == 0 {
		return map[string][]ChatGenerationParameterCapability{}
	}
	output := map[string][]ChatGenerationParameterCapability{}
	for _, protocol := range []string{generationProtocolChatCompletions, generationProtocolResponses} {
		lists := make([][]ChatGenerationParameterCapability, 0, len(items))
		empty := false
		for _, item := range items {
			list := item[protocol]
			if len(list) == 0 {
				empty = true
				break
			}
			lists = append(lists, list)
		}
		if empty {
			continue
		}
		capabilities := []ChatGenerationParameterCapability{}
		for _, candidate := range lists[0] {
			min := candidate.Min
			max := candidate.Max
			all := true
			for _, list := range lists {
				matching, found := findCapability(list, candidate.Parameter)
				if !found {
					all = false
					break
				}
				if matching.Min > min {
					min = matching.Min
				}
				if matching.Max < max {
					max = matching.Max
				}
			}
			if !all || min > max {
				continue
			}
			merged := candidate
			merged.Min = min
			merged.Max = max
			capabilities = append(capabilities, merged)
		}
		if len(capabilities) > 0 {
			output[protocol] = capabilities
		}
	}
	return output
}

// flattenGenerationParameters mirrors chat-model-options.ts
// flattenGenerationParameters: the protocol intersection of the catalog rows
// narrowed to the protocols every row supports.
func flattenGenerationParameters(items []ProviderModelCatalogItem) []ChatGenerationParameterCapability {
	mergedByProtocol := intersectGenerationParameterCapabilities(mapItemsCapabilities(items, catalogItemGenerationParameterCapabilities))
	activeProtocols := intersectStringCapabilityLists(mapItems(items, func(item ProviderModelCatalogItem) []string {
		protocols := []string{}
		for _, protocol := range nilToEmpty(item.SupportedAPIProtocols) {
			if protocol == generationProtocolChatCompletions || protocol == generationProtocolResponses {
				protocols = append(protocols, protocol)
			}
		}
		return protocols
	}))
	capabilityLists := make([][]ChatGenerationParameterCapability, 0, len(activeProtocols))
	for _, protocol := range activeProtocols {
		capabilityLists = append(capabilityLists, mergedByProtocol[protocol])
	}
	if len(capabilityLists) == 0 {
		return []ChatGenerationParameterCapability{}
	}
	for _, list := range capabilityLists {
		if len(list) == 0 {
			return []ChatGenerationParameterCapability{}
		}
	}
	capabilities := []ChatGenerationParameterCapability{}
	for _, candidate := range capabilityLists[0] {
		min := candidate.Min
		max := candidate.Max
		all := true
		for _, list := range capabilityLists[1:] {
			matching, found := findCapability(list, candidate.Parameter)
			if !found {
				all = false
				break
			}
			if matching.Min > min {
				min = matching.Min
			}
			if matching.Max < max {
				max = matching.Max
			}
		}
		if !all || min > max {
			continue
		}
		merged := candidate
		merged.Min = min
		merged.Max = max
		merged.DefaultValue = clampCapabilityDefault(candidate.DefaultValue, min, max)
		if merged.Min <= merged.Max {
			capabilities = append(capabilities, merged)
		}
	}
	return capabilities
}

func mapItemsCapabilities(items []ProviderModelCatalogItem, project func(ProviderModelCatalogItem) map[string][]ChatGenerationParameterCapability) []map[string][]ChatGenerationParameterCapability {
	out := make([]map[string][]ChatGenerationParameterCapability, 0, len(items))
	for _, item := range items {
		out = append(out, project(item))
	}
	return out
}

func findCapability(list []ChatGenerationParameterCapability, parameter string) (ChatGenerationParameterCapability, bool) {
	for _, item := range list {
		if item.Parameter == parameter {
			return item, true
		}
	}
	return ChatGenerationParameterCapability{}, false
}

func clampCapabilityDefault(value, min, max float64) float64 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

// constrainChatGenerationParametersForRoute mirrors the same-named helper:
// each capability narrows across every routed account, dropping entries any
// account cannot carry.
func constrainChatGenerationParametersForRoute(capabilities []ChatGenerationParameterCapability, model string, protocol ChatTransportProtocol, accounts []chatGenerationRouteAccount) []ChatGenerationParameterCapability {
	if len(accounts) == 0 {
		return []ChatGenerationParameterCapability{}
	}
	output := []ChatGenerationParameterCapability{}
	for _, capability := range capabilities {
		resolved := make([]ChatGenerationParameterCapability, 0, len(accounts))
		valid := true
		for _, account := range accounts {
			routeCapability, ok := routeCapabilityForAccount(account, capability, model, protocol)
			if !ok {
				valid = false
				break
			}
			resolved = append(resolved, routeCapability)
		}
		if !valid {
			continue
		}
		min := resolved[0].Min
		max := resolved[0].Max
		for _, item := range resolved[1:] {
			if item.Min > min {
				min = item.Min
			}
			if item.Max < max {
				max = item.Max
			}
		}
		if min > max {
			continue
		}
		merged := capability
		merged.Min = min
		merged.Max = max
		merged.DefaultValue = clampCapabilityDefault(capability.DefaultValue, min, max)
		output = append(output, merged)
	}
	return output
}

// intersectGenerationParameterCapabilityLists mirrors the same-named helper.
func intersectGenerationParameterCapabilityLists(lists [][]ChatGenerationParameterCapability) []ChatGenerationParameterCapability {
	if len(lists) == 0 {
		return []ChatGenerationParameterCapability{}
	}
	for _, list := range lists {
		if len(list) == 0 {
			return []ChatGenerationParameterCapability{}
		}
	}
	output := []ChatGenerationParameterCapability{}
	for _, candidate := range lists[0] {
		min := candidate.Min
		max := candidate.Max
		all := true
		for _, list := range lists {
			matching, found := findCapability(list, candidate.Parameter)
			if !found {
				all = false
				break
			}
			if matching.Min > min {
				min = matching.Min
			}
			if matching.Max < max {
				max = matching.Max
			}
		}
		if !all || min > max {
			continue
		}
		merged := candidate
		merged.Min = min
		merged.Max = max
		merged.DefaultValue = clampCapabilityDefault(candidate.DefaultValue, min, max)
		output = append(output, merged)
	}
	return output
}

// bridgePreservedParameters mirrors bridgePreservedParameters: protocol
// bridges keep only the universally preserved sampling fields.
var bridgePreservedParameters = map[string]bool{
	"temperature":     true,
	"topP":            true,
	"maxOutputTokens": true,
}

// routeCapabilityForAccount mirrors routeCapabilityForAccount: the effective
// capability for one routed account given its (enabled, protocol-matching)
// model mapping. ok=false drops the parameter for the account.
func routeCapabilityForAccount(account chatGenerationRouteAccount, capability ChatGenerationParameterCapability, model string, protocol ChatTransportProtocol) (ChatGenerationParameterCapability, bool) {
	if normalizeProviderToken(account.ProviderCode) == "gpt" && account.Type == "oauth" {
		return ChatGenerationParameterCapability{}, false
	}
	var mapping *ChatTransportModelMapping
	for index := range account.ModelMappings {
		item := &account.ModelMappings[index]
		if item.Enabled != nil && !*item.Enabled {
			continue
		}
		if item.SourceModel == model && item.SourceEndpointFamily == string(protocol) {
			mapping = item
			break
		}
	}
	if mapping == nil {
		return capability, true
	}
	if mapping.UpstreamEndpointFamily != "" && mapping.UpstreamEndpointFamily != string(protocol) {
		return capability, bridgePreservedParameters[capability.Parameter]
	}
	if mapping.UpstreamModel == "" || mapping.UpstreamModel == model {
		return capability, true
	}
	protocolCapabilities := generationParameterCapabilitiesForModel(account.ProviderCode, mapping.UpstreamModel, nil)[string(protocol)]
	item, found := findCapability(protocolCapabilities, capability.Parameter)
	return item, found
}
