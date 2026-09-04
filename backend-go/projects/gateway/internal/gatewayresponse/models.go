package gatewayresponse

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayanthropic"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaygemini"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// 模型目录响应构建，对齐 fixed-responses.ts + model-catalog.service.ts 的
// models 载荷面。

// ModelCatalogEntry 是 ProviderModelCatalogItem 中 models 响应实际消费的投影。
type ModelCatalogEntry struct {
	Model                        string
	Scope                        string // 'built_in' | 'global' | 'personal' | ...
	ReleaseDate                  string // YYYY-MM-DD
	CreatedAt                    string // RFC3339
	CapabilityNotes              string
	PricingNotes                 string
	Notes                        string
	ContextWindowTokens          int
	SupportedServiceTiers        []string
	CodexSupportedReasoningLevels []string
	CodexDefaultReasoningLevel   string
	CodexMultiAgentVersion       string
}

// openAIModelListItem 对齐 OpenAIModelListItem。
type openAIModelListItem struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type openAIModelsListResponse struct {
	Object string                `json:"object"`
	Data   []openAIModelListItem `json:"data"`
}

// codexReasoningEffortPreset 对齐 CodexReasoningEffortPreset。
type codexReasoningEffortPreset struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}

// codexModelListItem 对齐 CodexModelListItem（保持 Node 字段序）。
type codexModelListItem struct {
	Slug                        string                       `json:"slug"`
	DisplayName                 string                       `json:"display_name"`
	Description                 *string                      `json:"description"`
	DefaultReasoningLevel       *string                      `json:"default_reasoning_level,omitempty"`
	SupportedReasoningLevels    []codexReasoningEffortPreset `json:"supported_reasoning_levels,omitempty"`
	ShellType                   string                       `json:"shell_type"`
	Visibility                  string                       `json:"visibility"`
	SupportedInAPI              bool                         `json:"supported_in_api"`
	Priority                    int                          `json:"priority"`
	AdditionalSpeedTiers        []string                     `json:"additional_speed_tiers"`
	ServiceTiers                []codexServiceTier           `json:"service_tiers"`
	DefaultServiceTier          *string                      `json:"default_service_tier"`
	AvailabilityNux             *string                      `json:"availability_nux"`
	Upgrade                     *string                      `json:"upgrade"`
	BaseInstructions            string                       `json:"base_instructions"`
	ModelMessages               *string                      `json:"model_messages"`
	SupportsReasoningSummaries  bool                         `json:"supports_reasoning_summaries"`
	DefaultReasoningSummary     string                       `json:"default_reasoning_summary"`
	SupportVerbosity            bool                         `json:"support_verbosity"`
	DefaultVerbosity            *string                      `json:"default_verbosity"`
	ApplyPatchToolType          *string                      `json:"apply_patch_tool_type"`
	WebSearchToolType           string                       `json:"web_search_tool_type"`
	TruncationPolicy            codexTruncationPolicy        `json:"truncation_policy"`
	SupportsParallelToolCalls   bool                         `json:"supports_parallel_tool_calls"`
	SupportsImageDetailOriginal bool                         `json:"supports_image_detail_original"`
	ContextWindow               int                          `json:"context_window"`
	MaxContextWindow            int                          `json:"max_context_window"`
	AutoCompactTokenLimit       *int                         `json:"auto_compact_token_limit"`
	EffectiveContextWindowPercent int                        `json:"effective_context_window_percent"`
	ExperimentalSupportedTools  []string                     `json:"experimental_supported_tools"`
	InputModalities             []string                     `json:"input_modalities"`
	SupportsSearchTool          bool                         `json:"supports_search_tool"`
	UseResponsesLite            bool                         `json:"use_responses_lite"`
	AutoReviewModelOverride     *string                      `json:"auto_review_model_override"`
	ToolMode                    *string                      `json:"tool_mode"`
	MultiAgentVersion           *string                      `json:"multi_agent_version"`
}

type codexServiceTier struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type codexTruncationPolicy struct {
	Mode  string `json:"mode"`
	Limit int    `json:"limit"`
}

type codexModelsListResponse struct {
	Models []codexModelListItem `json:"models"`
}

// buildOpenAIModelsPayload 对齐 buildOpenAIModelsResponse（Codex 请求返回
// Codex 形状）。
func buildOpenAIModelsPayload(catalog []ModelCatalogEntry, req *gatewaypreauth.GatewayRequest) any {
	if req != nil && isCodexModelsRequest(req) {
		return buildCodexModelsPayload(catalog)
	}
	items := make([]openAIModelListItem, 0, len(catalog))
	for _, item := range catalog {
		items = append(items, openAIModelListItem{
			ID:      item.Model,
			Object:  "model",
			Created: modelCreatedUnixSeconds(item),
			OwnedBy: openAIOwnedBy(item.Scope),
		})
	}
	return openAIModelsListResponse{Object: "list", Data: items}
}

func openAIOwnedBy(scope string) string {
	if scope == "built_in" {
		return "openai"
	}
	return "juhe-ai"
}

// buildCodexModelsPayload 对齐 buildCodexModelsResponseFromCatalog。
func buildCodexModelsPayload(catalog []ModelCatalogEntry) codexModelsListResponse {
	models := make([]codexModelListItem, 0, len(catalog))
	for index, item := range catalog {
		models = append(models, buildCodexModelInfo(item, index))
	}
	return codexModelsListResponse{Models: models}
}

func buildCodexModelInfo(item ModelCatalogEntry, index int) codexModelListItem {
	contextWindow := codexContextWindow(item)
	supportedReasoningLevels := make([]codexReasoningEffortPreset, 0, len(item.CodexSupportedReasoningLevels))
	for _, effort := range item.CodexSupportedReasoningLevels {
		supportedReasoningLevels = append(supportedReasoningLevels, codexReasoningEffortPreset{
			Effort:      effort,
			Description: codexReasoningLevelDescription(effort),
		})
	}
	serviceTiers := make([]codexServiceTier, 0, len(item.SupportedServiceTiers))
	for _, tier := range item.SupportedServiceTiers {
		name := "Flex"
		description := "Flex processing"
		if tier == "priority" {
			name = "Fast"
			description = "Priority processing"
		}
		serviceTiers = append(serviceTiers, codexServiceTier{ID: tier, Name: name, Description: description})
	}
	description := firstNonEmpty(item.CapabilityNotes, item.PricingNotes, item.Notes)
	var descriptionPtr *string
	if description != "" {
		descriptionPtr = &description
	}
	response := codexModelListItem{
		Slug:                        item.Model,
		DisplayName:                 item.Model,
		Description:                 descriptionPtr,
		ShellType:                   "shell_command",
		Visibility:                  "list",
		SupportedInAPI:              true,
		Priority:                    index,
		AdditionalSpeedTiers:        []string{},
		ServiceTiers:                serviceTiers,
		BaseInstructions:            "You are Codex, a coding agent.",
		DefaultReasoningSummary:     "auto",
		WebSearchToolType:           "text",
		TruncationPolicy:            codexTruncationPolicy{Mode: "bytes", Limit: 10_000},
		ContextWindow:               contextWindow,
		MaxContextWindow:            contextWindow,
		EffectiveContextWindowPercent: 95,
		ExperimentalSupportedTools:  []string{},
		InputModalities:             []string{"text", "image"},
		UseResponsesLite:            usesOpenAICodexResponsesLite(item.Model),
	}
	if len(supportedReasoningLevels) > 0 {
		response.SupportedReasoningLevels = supportedReasoningLevels
		if item.CodexDefaultReasoningLevel != "" {
			for _, level := range supportedReasoningLevels {
				if level.Effort == item.CodexDefaultReasoningLevel {
					value := item.CodexDefaultReasoningLevel
					response.DefaultReasoningLevel = &value
					break
				}
			}
		}
	}
	if stringSliceContains(item.SupportedServiceTiers, "priority") {
		response.AdditionalSpeedTiers = []string{"fast"}
	}
	if item.CodexMultiAgentVersion != "" {
		value := item.CodexMultiAgentVersion
		response.MultiAgentVersion = &value
	}
	return response
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
		return "XHigh"
	default:
		return strings.ToUpper(level[:1]) + level[1:]
	}
}

func codexContextWindow(item ModelCatalogEntry) int {
	if item.ContextWindowTokens > 0 {
		return item.ContextWindowTokens
	}
	return 272_000
}

// usesOpenAICodexResponsesLite 对齐 usesOpenAICodexResponsesLite（按模型名
// 判定轻量 Responses 通道；Node 规则归属 model-pricing，这里保守 false）。
func usesOpenAICodexResponsesLite(model string) bool {
	return false
}

// modelCreatedUnixSeconds 对齐 modelCreatedUnixSeconds。
func modelCreatedUnixSeconds(item ModelCatalogEntry) int64 {
	if item.ReleaseDate != "" {
		if parsed, err := time.Parse("2006-01-02", item.ReleaseDate); err == nil {
			return parsed.Unix()
		}
		return 0
	}
	if item.CreatedAt == "" {
		return 0
	}
	parsed, err := time.Parse(time.RFC3339, item.CreatedAt)
	if err != nil {
		return 0
	}
	return parsed.Unix()
}

// isCodexModelsRequest 对齐 isCodexModelsRequest。
func isCodexModelsRequest(req *gatewaypreauth.GatewayRequest) bool {
	if !isOpenAIModelsRequest(req) {
		return false
	}
	if headerContainsCodex(req.Header("originator")) ||
		headerContainsCodex(req.Header("user-agent")) ||
		headerContainsCodex(req.Header("x-codex-client")) {
		return true
	}
	return hasNonEmptyQueryParam(req, "client_version")
}

func isOpenAIModelsRequest(req *gatewaypreauth.GatewayRequest) bool {
	if req == nil || req.MethodUpper() != "GET" {
		return false
	}
	path := LowercasedRequestPath(req.PathAndQuery())
	return normalizeV1PrefixPath(path) == "/models"
}

func hasNonEmptyQueryParam(req *gatewaypreauth.GatewayRequest, name string) bool {
	if req.HTTP == nil || req.HTTP.URL == nil {
		return false
	}
	return strings.TrimSpace(req.HTTP.URL.Query().Get(name)) != ""
}

func headerContainsCodex(value string) bool {
	return strings.Contains(strings.ToLower(value), "codex")
}

// buildAnthropicModelsPayload 复用 G03 的 buildAnthropicModelsResponse。
func buildAnthropicModelsPayload(catalog []ModelCatalogEntry) anthropicModelsPayload {
	items := make([]gatewayanthropic.ModelCatalogItem, 0, len(catalog))
	for _, item := range catalog {
		items = append(items, gatewayanthropic.ModelCatalogItem{
			Model:       item.Model,
			ReleaseDate: item.ReleaseDate,
			CreatedAt:   item.CreatedAt,
		})
	}
	response, err := gatewayanthropic.BuildModelsResponse(items)
	if err != nil {
		return anthropicModelsPayload{}
	}
	encoded, _ := json.Marshal(response)
	var payload anthropicModelsPayload
	_ = json.Unmarshal(encoded, &payload)
	return payload
}

type anthropicModelsPayload struct {
	Object  string                   `json:"object"`
	Data    []map[string]any         `json:"data"`
	HasMore bool                     `json:"has_more"`
	FirstID *string                  `json:"first_id"`
	LastID  *string                  `json:"last_id"`
}

// buildGeminiModelsPayload 复用 G04 的 buildGeminiModelsResponse。
func buildGeminiModelsPayload(catalog []ModelCatalogEntry) gatewaygemini.ModelsListResponse {
	items := make([]gatewaygemini.ModelCatalogItem, 0, len(catalog))
	for _, item := range catalog {
		items = append(items, gatewaygemini.ModelCatalogItem{
			Model:                 item.Model,
			CapabilityNotes:       item.CapabilityNotes,
			Notes:                 item.Notes,
			MaxInputTokens:        item.ContextWindowTokens,
			ContextWindowTokens:   item.ContextWindowTokens,
			MaxOutputTokens:       0,
			SupportedAPIProtocols: item.SupportedServiceTiers,
		})
	}
	return gatewaygemini.BuildModelsResponse(items)
}

// setAuthenticatedModelsClientCacheHeaders 对齐
// setAuthenticatedModelsClientCacheHeaders。
func setAuthenticatedModelsClientCacheHeaders(res gatewaypreauth.GatewayResponseWriter) {
	header := res.Header()
	header.Set("Cache-Control", "private, no-cache")
	varyHeaders := []string{
		"Authorization",
		"X-API-Key",
		"X-Goog-API-Key",
		"X-Juhe-Client-Profile",
		"Anthropic-Version",
		"Anthropic-Beta",
		"X-Claude-Code-Session-Id",
		"X-Claude-Code-Agent-Id",
		"Originator",
		"User-Agent",
		"X-Codex-Client",
	}
	merged := map[string]string{}
	order := make([]string, 0, len(varyHeaders))
	existing := strings.Split(header.Get("Vary"), ",")
	for _, item := range existing {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, exists := merged[key]; !exists {
			order = append(order, trimmed)
		}
		merged[key] = trimmed
	}
	for _, varyHeader := range varyHeaders {
		key := strings.ToLower(varyHeader)
		if _, exists := merged[key]; !exists {
			order = append(order, varyHeader)
		}
		merged[key] = varyHeader
	}
	header.Set("Vary", strings.Join(order, ", "))
}
