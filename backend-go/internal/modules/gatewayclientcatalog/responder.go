package gatewayclientcatalog

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

type ModelsResponseProtocol string

const (
	ModelsProtocolOpenAI    ModelsResponseProtocol = "openai_v1"
	ModelsProtocolCodex     ModelsResponseProtocol = "codex_models"
	ModelsProtocolAnthropic ModelsResponseProtocol = "anthropic_v1"
	ModelsProtocolGemini    ModelsResponseProtocol = "gemini_v1beta"
)

type ModelsProtocolInput struct {
	Method                string
	PathAndQuery          string
	ExplicitProfile       string
	UserAgent             string
	Originator            string
	CodexClient           string
	HasCodexClientVersion bool
	HasGeminiAPIKey       bool
	HasAnthropicVersion   bool
	HasAnthropicBeta      bool
	HasClaudeSessionID    bool
	HasClaudeAgentID      bool
}

func ResolveModelsResponseProtocol(input ModelsProtocolInput) (ModelsResponseProtocol, bool) {
	if !strings.EqualFold(strings.TrimSpace(input.Method), "GET") {
		return "", false
	}
	path, rawQuery := splitPathAndQuery(input.PathAndQuery)
	path = strings.ToLower(strings.TrimSpace(path))
	if path != "/models" && path != "/v1/models" && path != "/v1beta/models" {
		return "", false
	}
	if path == "/v1beta/models" {
		return ModelsProtocolGemini, true
	}

	profile := normalizeProfile(input.ExplicitProfile)
	if path == "/models" && (isGeminiProfile(profile) || input.HasGeminiAPIKey || hasNonEmptyQueryValue(rawQuery, "key") || isGeminiUserAgent(input.UserAgent)) {
		return ModelsProtocolGemini, true
	}
	if isAnthropicProfile(profile) || input.HasAnthropicVersion || input.HasAnthropicBeta ||
		input.HasClaudeSessionID || input.HasClaudeAgentID || isClaudeUserAgent(input.UserAgent) {
		return ModelsProtocolAnthropic, true
	}
	if input.HasCodexClientVersion || hasNonEmptyQueryValue(rawQuery, "client_version") ||
		containsCodex(input.Originator) || containsCodex(input.UserAgent) || containsCodex(input.CodexClient) {
		return ModelsProtocolCodex, true
	}
	return ModelsProtocolOpenAI, true
}

type ModelsPayload interface {
	modelsPayload()
}

type OpenAIModelsResponse struct {
	Object string            `json:"object"`
	Data   []OpenAIModelItem `json:"data"`
}

func (*OpenAIModelsResponse) modelsPayload() {}

type OpenAIModelItem struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type CodexModelsResponse struct {
	Models []CodexModelItem `json:"models"`
}

func (*CodexModelsResponse) modelsPayload() {}

type CodexReasoningLevel struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}

type CodexServiceTier struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type CodexTruncationPolicy struct {
	Mode  string `json:"mode"`
	Limit int    `json:"limit"`
}

type CodexModelItem struct {
	Slug                          string                `json:"slug"`
	DisplayName                   string                `json:"display_name"`
	Description                   *string               `json:"description"`
	DefaultReasoningLevel         *string               `json:"default_reasoning_level,omitempty"`
	SupportedReasoningLevels      []CodexReasoningLevel `json:"supported_reasoning_levels,omitempty"`
	ShellType                     string                `json:"shell_type"`
	Visibility                    string                `json:"visibility"`
	SupportedInAPI                bool                  `json:"supported_in_api"`
	Priority                      int                   `json:"priority"`
	AdditionalSpeedTiers          []string              `json:"additional_speed_tiers"`
	ServiceTiers                  []CodexServiceTier    `json:"service_tiers"`
	DefaultServiceTier            any                   `json:"default_service_tier"`
	AvailabilityNUX               any                   `json:"availability_nux"`
	Upgrade                       any                   `json:"upgrade"`
	BaseInstructions              string                `json:"base_instructions"`
	ModelMessages                 any                   `json:"model_messages"`
	SupportsReasoningSummaries    bool                  `json:"supports_reasoning_summaries"`
	DefaultReasoningSummary       string                `json:"default_reasoning_summary"`
	SupportVerbosity              bool                  `json:"support_verbosity"`
	DefaultVerbosity              any                   `json:"default_verbosity"`
	ApplyPatchToolType            any                   `json:"apply_patch_tool_type"`
	WebSearchToolType             string                `json:"web_search_tool_type"`
	TruncationPolicy              CodexTruncationPolicy `json:"truncation_policy"`
	SupportsParallelToolCalls     bool                  `json:"supports_parallel_tool_calls"`
	SupportsImageDetailOriginal   bool                  `json:"supports_image_detail_original"`
	ContextWindow                 int                   `json:"context_window"`
	MaxContextWindow              int                   `json:"max_context_window"`
	AutoCompactTokenLimit         any                   `json:"auto_compact_token_limit"`
	EffectiveContextWindowPercent int                   `json:"effective_context_window_percent"`
	ExperimentalSupportedTools    []string              `json:"experimental_supported_tools"`
	InputModalities               []string              `json:"input_modalities"`
	SupportsSearchTool            bool                  `json:"supports_search_tool"`
	UseResponsesLite              bool                  `json:"use_responses_lite"`
	AutoReviewModelOverride       any                   `json:"auto_review_model_override"`
	ToolMode                      any                   `json:"tool_mode"`
	MultiAgentVersion             *string               `json:"multi_agent_version"`
}

type AnthropicModelsResponse struct {
	Data    []AnthropicModelItem `json:"data"`
	HasMore bool                 `json:"has_more"`
	FirstID *string              `json:"first_id"`
	LastID  *string              `json:"last_id"`
}

func (*AnthropicModelsResponse) modelsPayload() {}

type AnthropicModelItem struct {
	Type        string `json:"type"`
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at,omitempty"`
}

type GeminiModelsResponse struct {
	Models []GeminiModelItem `json:"models"`
}

func (*GeminiModelsResponse) modelsPayload() {}

type GeminiModelItem struct {
	Name                       string   `json:"name"`
	Version                    string   `json:"version"`
	DisplayName                string   `json:"displayName"`
	Description                string   `json:"description,omitempty"`
	InputTokenLimit            int      `json:"inputTokenLimit,omitempty"`
	OutputTokenLimit           int      `json:"outputTokenLimit,omitempty"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
}

func BuildModelsResponse(protocol ModelsResponseProtocol, items []port.GatewayClientCatalogModel) (ModelsPayload, error) {
	switch protocol {
	case ModelsProtocolOpenAI:
		data := make([]OpenAIModelItem, 0, len(items))
		for _, item := range items {
			ownedBy := "juhe-ai"
			if strings.EqualFold(strings.TrimSpace(item.Scope), "built_in") {
				ownedBy = "openai"
			}
			data = append(data, OpenAIModelItem{ID: item.Model, Object: "model", Created: modelCreatedUnix(item), OwnedBy: ownedBy})
		}
		return &OpenAIModelsResponse{Object: "list", Data: data}, nil
	case ModelsProtocolCodex:
		models := make([]CodexModelItem, 0, len(items))
		for index, item := range items {
			models = append(models, codexModelItem(item, index))
		}
		return &CodexModelsResponse{Models: models}, nil
	case ModelsProtocolAnthropic:
		data := make([]AnthropicModelItem, 0, len(items))
		for _, item := range items {
			data = append(data, AnthropicModelItem{Type: "model", ID: item.Model, DisplayName: item.Model, CreatedAt: modelCreatedRFC3339(item)})
		}
		response := &AnthropicModelsResponse{Data: data}
		if len(data) > 0 {
			response.FirstID = stringPointer(data[0].ID)
			response.LastID = stringPointer(data[len(data)-1].ID)
		}
		return response, nil
	case ModelsProtocolGemini:
		models := make([]GeminiModelItem, 0, len(items))
		for _, item := range items {
			name := item.Model
			if !strings.HasPrefix(name, "models/") {
				name = "models/" + name
			}
			description := strings.TrimSpace(item.CapabilityNotes)
			if description == "" {
				description = strings.TrimSpace(item.Notes)
			}
			models = append(models, GeminiModelItem{
				Name: name, Version: item.Model, DisplayName: item.Model, Description: description,
				InputTokenLimit:            positiveInt(firstInt(item.MaxInputTokens, item.ContextWindowTokens)),
				OutputTokenLimit:           positiveInt(item.MaxOutputTokens),
				SupportedGenerationMethods: geminiGenerationMethods(item.SupportedAPIProtocols),
			})
		}
		return &GeminiModelsResponse{Models: models}, nil
	default:
		return nil, fmt.Errorf("unsupported models response protocol %q", protocol)
	}
}

func codexModelItem(item port.GatewayClientCatalogModel, index int) CodexModelItem {
	reasoningLevels := make([]CodexReasoningLevel, 0, len(item.CodexSupportedReasoningLevels))
	seenReasoningLevels := map[string]struct{}{}
	for _, value := range item.CodexSupportedReasoningLevels {
		level := strings.TrimSpace(value)
		if level == "" {
			continue
		}
		if _, exists := seenReasoningLevels[level]; exists {
			continue
		}
		seenReasoningLevels[level] = struct{}{}
		reasoningLevels = append(reasoningLevels, CodexReasoningLevel{Effort: level, Description: codexReasoningLevelDescription(level)})
	}
	var defaultReasoningLevel *string
	configuredDefault := strings.TrimSpace(item.CodexDefaultReasoningLevel)
	if _, supported := seenReasoningLevels[configuredDefault]; configuredDefault != "" && supported {
		defaultReasoningLevel = stringPointer(configuredDefault)
	}

	serviceTiers := make([]CodexServiceTier, 0, len(item.SupportedServiceTiers))
	additionalSpeedTiers := []string{}
	seenServiceTiers := map[string]struct{}{}
	for _, value := range item.SupportedServiceTiers {
		tier := strings.TrimSpace(value)
		if tier == "" {
			continue
		}
		if _, exists := seenServiceTiers[tier]; exists {
			continue
		}
		seenServiceTiers[tier] = struct{}{}
		name, description := "Flex", "Flex processing"
		if tier == "priority" {
			name, description = "Fast", "Priority processing"
			additionalSpeedTiers = append(additionalSpeedTiers, "fast")
		}
		serviceTiers = append(serviceTiers, CodexServiceTier{ID: tier, Name: name, Description: description})
	}

	description := firstNonEmpty(item.CapabilityNotes, item.PricingNotes, item.Notes)
	contextWindow := positiveInt(item.ContextWindowTokens)
	if contextWindow == 0 {
		contextWindow = 272_000
	}
	return CodexModelItem{
		Slug: item.Model, DisplayName: item.Model, Description: optionalString(description),
		DefaultReasoningLevel: defaultReasoningLevel, SupportedReasoningLevels: reasoningLevels,
		ShellType: "shell_command", Visibility: "list", SupportedInAPI: true, Priority: index,
		AdditionalSpeedTiers: additionalSpeedTiers, ServiceTiers: serviceTiers,
		BaseInstructions: "You are Codex, a coding agent.", SupportsReasoningSummaries: false,
		DefaultReasoningSummary: "auto", SupportVerbosity: false, WebSearchToolType: "text",
		TruncationPolicy:          CodexTruncationPolicy{Mode: "bytes", Limit: 10_000},
		SupportsParallelToolCalls: false, SupportsImageDetailOriginal: false,
		ContextWindow: contextWindow, MaxContextWindow: contextWindow, EffectiveContextWindowPercent: 95,
		ExperimentalSupportedTools: []string{}, InputModalities: []string{"text", "image"},
		SupportsSearchTool: false, UseResponsesLite: usesCodexResponsesLite(item.Model),
		MultiAgentVersion: optionalString(strings.TrimSpace(item.CodexMultiAgentVersion)),
	}
}

func splitPathAndQuery(value string) (string, string) {
	path, query, found := strings.Cut(value, "?")
	if !found {
		return path, ""
	}
	return path, query
}

func normalizeProfile(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer("-", "_", " ", "_").Replace(value)
	return value
}

func isGeminiProfile(value string) bool {
	return value == "gemini" || value == "generic_gemini" || value == "gemini_cli"
}

func isAnthropicProfile(value string) bool {
	return value == "anthropic" || value == "generic_anthropic" || value == "claude_code"
}

func containsCodex(value string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(value)), "codex")
}

func hasNonEmptyQueryValue(rawQuery, key string) bool {
	values, err := url.ParseQuery(rawQuery)
	return err == nil && strings.TrimSpace(values.Get(key)) != ""
}

func isGeminiUserAgent(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return geminiCLIUserAgentPattern.MatchString(value)
}

var geminiCLIUserAgentPattern = regexp.MustCompile(`(?:\bgeminicli(?:[-/]|$)|proxy_client=geminicli\b)`)

func isClaudeUserAgent(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(value, "claude-cli/") || strings.Contains(value, " claude-cli/")
}

func modelCreatedUnix(item port.GatewayClientCatalogModel) int64 {
	if parsed, ok := modelCreatedTime(item); ok {
		return parsed.Unix()
	}
	return 0
}

func modelCreatedRFC3339(item port.GatewayClientCatalogModel) string {
	if parsed, ok := modelCreatedTime(item); ok {
		return parsed.UTC().Format(time.RFC3339)
	}
	return ""
}

func modelCreatedTime(item port.GatewayClientCatalogModel) (time.Time, bool) {
	if parsed, ok := parseCatalogTime(item.ReleaseDate); ok {
		return parsed, true
	}
	if !item.CreatedAt.IsZero() {
		return item.CreatedAt, true
	}
	return time.Time{}, false
}

func firstInt(values ...*int) *int {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func positiveInt(value *int) int {
	if value == nil || *value <= 0 {
		return 0
	}
	return *value
}

func stringPointer(value string) *string { return &value }

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return stringPointer(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func codexReasoningLevelDescription(level string) string {
	switch level {
	case "none":
		return "None"
	case "minimal":
		return "Minimal"
	case "low":
		return "Low"
	case "medium":
		return "Medium"
	case "high":
		return "High"
	case "xhigh":
		return "Extra High"
	case "max":
		return "Max"
	case "ultra":
		return "Ultra"
	default:
		return level
	}
}

func usesCodexResponsesLite(model string) bool {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna":
		return true
	default:
		return false
	}
}

func geminiGenerationMethods(protocols []string) []string {
	methods := make([]string, 0, 3)
	seen := map[string]struct{}{}
	add := func(method string) {
		if _, exists := seen[method]; !exists {
			seen[method] = struct{}{}
			methods = append(methods, method)
		}
	}
	for _, protocol := range protocols {
		switch strings.ToLower(strings.TrimSpace(protocol)) {
		case "generate_content", "stream_generate_content":
			add("generateContent")
		case "count_tokens":
			add("countTokens")
		case "embed_content":
			add("embedContent")
		}
	}
	if len(methods) == 0 {
		return []string{"generateContent"}
	}
	return methods
}
