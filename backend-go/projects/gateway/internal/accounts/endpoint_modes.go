package accounts

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
)

// Endpoint-mode domain (第 1 段凭据规范化域的支撑面): the port of
// backend/src/domain/provider-protocol.ts, openai-endpoint-modes.ts,
// anthropic-endpoint-modes.ts, gemini-endpoint-modes.ts,
// account-health-check-endpoint-mode.ts and
// modules/providers/drivers/ (the credential-driver registry subset the
// credentials write path consumes). Only the write-side normalization plus the
// batch compatibility asserts are ported; the request-shape mappers
// (endpointModeForRequestShape family) belong to the gateway proto slices.
//
// REFACTOR-0005 阶段 0：谓词族、模式值表与运行时默认解析已下沉
// accountscore（中立包，供 accountstest 等子域 import）；根包保留私有结构
// protocolPredicateInput / endpointModeDefaultContext 与同名包装，包内既有
// 调用点零改动。写入侧归一化、凭据 driver 注册表、兼容断言与健康检查解析
// 仍留本包。

const (
	gptOpenAIV1ProfileIDConstant         = "profile_gpt_openai_v1"
	xaiOpenAIV1ProfileID                 = "profile_xai_openai_v1"
	deepSeekOpenAIV1ProfileID            = "profile_deepseek_openai_v1"
	deepSeekAnthropicV1ProfileID         = accountscore.DeepSeekAnthropicV1ProfileID
	glmGeneralOpenAIV1ProfileID          = "profile_glm_general_openai_v1"
	glmCodingOpenAIV1ProfileID           = "profile_glm_coding_openai_v1"
	glmCodingAnthropicV1ProfileID        = accountscore.GlmCodingAnthropicV1ProfileID
	geminiOpenAIChatV1BetaProfileID      = "profile_gemini_openai_chat_v1beta"
	anthropicProviderCode                = accountscore.AnthropicProviderCode
	geminiProviderCode                   = accountscore.GeminiProviderCode
	anthropicProtocolCodeConstant        = accountscore.AnthropicProtocolCode
	anthropicProtocolVersionConstant     = accountscore.AnthropicProtocolVersion
	geminiProtocolCodeConstant           = accountscore.GeminiProtocolCode
	geminiProtocolVersionConstant        = accountscore.GeminiProtocolVersion
	deepSeekProviderCode                 = accountscore.DeepSeekProviderCode
	glmProviderCode                      = accountscore.GlmProviderCode
	xaiProviderCode                      = accountscore.XaiProviderCode
	hybridProviderCode                   = accountscore.HybridProviderCode
	openAICompatibleProviderCodeConstant = accountscore.OpenAICompatibleProviderCode
)

var openAIEndpointModeValues = accountscore.OpenAIEndpointModeValues
var openAIChatEndpointModes = accountscore.OpenAIChatEndpointModes
var openAIResponsesEndpointModes = accountscore.OpenAIResponsesEndpointModes
var anthropicEndpointModeValues = accountscore.AnthropicEndpointModeValues
var geminiEndpointModeValues = accountscore.GeminiEndpointModeValues
var hybridEndpointModeValues = accountscore.HybridEndpointModeValues
var openAIChatEndpointModeSet = accountscore.StringSet(openAIChatEndpointModes)

func stringSet(values []string) map[string]bool {
	return accountscore.StringSet(values)
}

func isOpenAIEndpointMode(value string) bool    { return accountscore.IsOpenAIEndpointMode(value) }
func isAnthropicEndpointMode(value string) bool { return accountscore.IsAnthropicEndpointMode(value) }
func isGeminiEndpointMode(value string) bool    { return accountscore.IsGeminiEndpointMode(value) }
func isHybridEndpointMode(value string) bool    { return accountscore.IsHybridEndpointMode(value) }

// ---- provider-protocol.ts predicates ----

func isGptVendorCodeToken(value string) bool {
	return accountscore.IsGptVendorCodeToken(value)
}

func isXaiProviderCodeToken(value string) bool {
	return accountscore.IsXaiProviderCodeToken(value)
}

func isDeepSeekProviderCodeToken(value string) bool {
	return accountscore.IsDeepSeekProviderCodeToken(value)
}

func isGlmProviderCodeToken(value string) bool {
	return accountscore.IsGlmProviderCodeToken(value)
}

func isGeminiProviderCodeToken(value string) bool {
	return accountscore.IsGeminiProviderCodeToken(value)
}

func isHybridProviderCodeToken(value string) bool {
	return accountscore.IsHybridProviderCodeToken(value)
}

// protocolPredicateInput mirrors the ProviderProtocolDefinition/
// ProviderProtocolProfileDefinition shapes the predicates consume.
type protocolPredicateInput struct {
	providerCode              string
	protocolCode              string
	protocolVersion           string
	providerProtocolProfileID string
}

// toProtocolPredicate converts the facade-private predicate input into the
// neutral accountscore shape (包装转换，行为不变).
func toProtocolPredicate(input protocolPredicateInput) accountscore.ProtocolPredicate {
	return accountscore.ProtocolPredicate{
		ProviderCode:              input.providerCode,
		ProtocolCode:              input.protocolCode,
		ProtocolVersion:           input.protocolVersion,
		ProviderProtocolProfileID: input.providerProtocolProfileID,
	}
}

func isOpenAIProtocolProfileOf(input protocolPredicateInput) bool {
	return accountscore.IsOpenAIProtocolProfileOf(toProtocolPredicate(input))
}

func isAnthropicProtocolProfileOf(input protocolPredicateInput) bool {
	return accountscore.IsAnthropicProtocolProfileOf(toProtocolPredicate(input))
}

func isGeminiProtocolProfileOf(input protocolPredicateInput) bool {
	return accountscore.IsGeminiProtocolProfileOf(toProtocolPredicate(input))
}

// ---- endpoint-mode defaults + write normalization ----

// endpointModeDefaultContext mirrors OpenAIEndpointModeDefaultContext /
// AnthropicEndpointModeDefaultContext / GeminiEndpointModeDefaultContext (the
// shared field set; unused fields stay empty per family).
type endpointModeDefaultContext struct {
	providerCode              string
	accountType               string
	protocolCode              string
	protocolVersion           string
	providerProtocolProfileID string
	clientCompatibility       string
}

// toModeDefaultContext converts the facade-private default context into the
// neutral accountscore shape (包装转换，行为不变).
func toModeDefaultContext(input endpointModeDefaultContext) accountscore.ModeDefaultContext {
	return accountscore.ModeDefaultContext{
		ProviderCode:              input.providerCode,
		AccountType:               input.accountType,
		ProtocolCode:              input.protocolCode,
		ProtocolVersion:           input.protocolVersion,
		ProviderProtocolProfileID: input.providerProtocolProfileID,
		ClientCompatibility:       input.clientCompatibility,
	}
}

func (c endpointModeDefaultContext) predicate() protocolPredicateInput {
	return protocolPredicateInput{
		providerCode:              c.providerCode,
		protocolCode:              c.protocolCode,
		protocolVersion:           c.protocolVersion,
		providerProtocolProfileID: c.providerProtocolProfileID,
	}
}

func defaultOpenAIEndpointModes(input endpointModeDefaultContext) []string {
	return accountscore.DefaultOpenAIEndpointModes(toModeDefaultContext(input))
}

// normalizeEndpointModeList carries the shared array validation body of
// normalizeXEndpointModesForWrite: absent keys fall back to the defaults, an
// explicit null or a non-array value throws, values must belong to the family
// set, dedupe in order, non-empty.
// optionalValue carries the JS undefined-vs-null distinction for credential
// record fields: absent keys stay !present (defaults apply), explicit null
// arrives as present=true with a nil value (the array checks reject it).
type optionalValue struct {
	value   any
	present bool
}

func opt(value any) optionalValue { return optionalValue{value: value, present: true} }

// normalizeEndpointModeList carries the shared array validation body of
// normalizeXEndpointModesForWrite: absent keys fall back to the defaults, an
// explicit null or a non-array value throws, values must belong to the family
// set, dedupe in order, non-empty.
func normalizeEndpointModeList(value optionalValue, known func(string) bool, defaults []string, label string) ([]string, error) {
	if !value.present {
		return defaults, nil
	}
	list, ok := value.value.([]any)
	if !ok {
		return nil, errors.New(label + "必须是数组")
	}
	output := []string{}
	seen := map[string]bool{}
	for _, item := range list {
		text, ok := item.(string)
		if !ok || !known(text) {
			return nil, fmt.Errorf("%s包含不支持的能力：%s", label, renderUnsupportedValue(item))
		}
		if seen[text] {
			continue
		}
		seen[text] = true
		output = append(output, text)
	}
	if len(output) == 0 {
		return nil, errors.New(label + "至少选择一项")
	}
	return output, nil
}

// renderUnsupportedValue mirrors String(item) for the rejected entry.
func renderUnsupportedValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case string:
		return typed
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	default:
		return fmt.Sprintf("%v", value)
	}
}

func defaultAnthropicEndpointModes(input endpointModeDefaultContext) []string {
	return accountscore.DefaultAnthropicEndpointModes(toModeDefaultContext(input))
}

func defaultGeminiEndpointModes(endpointModeDefaultContext) []string {
	return accountscore.DefaultGeminiEndpointModes(accountscore.ModeDefaultContext{})
}

func normalizeHybridEndpointModesForWrite(value optionalValue) ([]string, error) {
	return normalizeEndpointModeList(value, isHybridEndpointMode, append([]string{}, hybridEndpointModeValues...), "上游接口能力")
}

// ---- credential driver registry (account-credentials.registry.ts) ----

// credentialDriver mirrors ProviderAccountCredentialDriver for the write path.
type credentialDriver struct {
	id                string
	supportsContext   func(endpointModeDefaultContext) bool
	normalizeForWrite func(optionalValue, endpointModeDefaultContext) ([]string, error)
}

// providerAccountCredentialDrivers mirrors the registry order; lookup scans in
// order exactly like Node's find().
var providerAccountCredentialDrivers = []credentialDriver{
	{
		id: "openai-compatible",
		supportsContext: func(c endpointModeDefaultContext) bool {
			openAICompatible := normalizeProviderToken(c.providerCode) == openAICompatibleProviderCodeConstant ||
				(normalizeProviderToken(c.providerCode) == geminiProviderCode &&
					c.providerProtocolProfileID == geminiOpenAIChatV1BetaProfileID)
			return openAICompatible && isOpenAIProtocolProfileOf(c.predicate())
		},
		normalizeForWrite: func(value optionalValue, c endpointModeDefaultContext) ([]string, error) {
			return normalizeOpenAIEndpointModesForWrite(value, c)
		},
	},
	{
		id: "gpt",
		supportsContext: func(c endpointModeDefaultContext) bool {
			return isGptVendorCodeToken(c.providerCode) && isOpenAIProtocolProfileOf(c.predicate())
		},
		normalizeForWrite: func(value optionalValue, c endpointModeDefaultContext) ([]string, error) {
			return normalizeOpenAIEndpointModesForWrite(value, c)
		},
	},
	{
		id: "xai",
		supportsContext: func(c endpointModeDefaultContext) bool {
			return (c.accountType == "api_key" || c.accountType == "oauth") &&
				isXaiProviderCodeToken(c.providerCode) &&
				isOpenAIProtocolProfileOf(c.predicate()) &&
				c.providerProtocolProfileID == xaiOpenAIV1ProfileID
		},
		normalizeForWrite: func(value optionalValue, c endpointModeDefaultContext) ([]string, error) {
			pinned := c
			pinned.providerCode = xaiProviderCode
			pinned.providerProtocolProfileID = xaiOpenAIV1ProfileID
			return normalizeOpenAIEndpointModesForWrite(value, pinned)
		},
	},
	{
		id: "deepseek",
		supportsContext: func(c endpointModeDefaultContext) bool {
			if !isDeepSeekProviderCodeToken(c.providerCode) {
				return false
			}
			if isOpenAIProtocolProfileOf(c.predicate()) && c.providerProtocolProfileID == deepSeekOpenAIV1ProfileID {
				return true
			}
			return isAnthropicProtocolProfileOf(c.predicate()) && c.providerProtocolProfileID == deepSeekAnthropicV1ProfileID
		},
		normalizeForWrite: normalizeDeepSeekEndpointModesForWrite,
	},
	{
		id: "anthropic",
		supportsContext: func(c endpointModeDefaultContext) bool {
			return normalizeProviderToken(c.providerCode) == anthropicProviderCode &&
				isAnthropicProtocolProfileOf(c.predicate())
		},
		normalizeForWrite: func(value optionalValue, c endpointModeDefaultContext) ([]string, error) {
			return normalizeAnthropicEndpointModesForWrite(value, c)
		},
	},
	{
		id: "gemini",
		supportsContext: func(c endpointModeDefaultContext) bool {
			return normalizeProviderToken(c.providerCode) == geminiProviderCode &&
				isGeminiProtocolProfileOf(c.predicate())
		},
		normalizeForWrite: func(value optionalValue, c endpointModeDefaultContext) ([]string, error) {
			return normalizeGeminiEndpointModesForWrite(value, c)
		},
	},
	{
		id: "glm",
		supportsContext: func(c endpointModeDefaultContext) bool {
			if !isGlmProviderCodeToken(c.providerCode) {
				return false
			}
			if isOpenAIProtocolProfileOf(c.predicate()) {
				return c.providerProtocolProfileID == glmGeneralOpenAIV1ProfileID ||
					c.providerProtocolProfileID == glmCodingOpenAIV1ProfileID
			}
			return isAnthropicProtocolProfileOf(c.predicate()) && c.providerProtocolProfileID == glmCodingAnthropicV1ProfileID
		},
		normalizeForWrite: normalizeGLMEndpointModesForWrite,
	},
	{
		id: "hybrid",
		supportsContext: func(c endpointModeDefaultContext) bool {
			return isHybridProviderCodeToken(c.providerCode)
		},
		normalizeForWrite: func(value optionalValue, _ endpointModeDefaultContext) ([]string, error) {
			return normalizeHybridEndpointModesForWrite(value)
		},
	},
}

func normalizeOpenAIEndpointModesForWrite(value optionalValue, defaults endpointModeDefaultContext) ([]string, error) {
	return normalizeEndpointModeList(value, isOpenAIEndpointMode, defaultOpenAIEndpointModes(defaults), "上游接口能力")
}

func normalizeAnthropicEndpointModesForWrite(value optionalValue, defaults endpointModeDefaultContext) ([]string, error) {
	return normalizeEndpointModeList(value, isAnthropicEndpointMode, defaultAnthropicEndpointModes(defaults), "上游接口能力")
}

func normalizeGeminiEndpointModesForWrite(value optionalValue, defaults endpointModeDefaultContext) ([]string, error) {
	return normalizeEndpointModeList(value, isGeminiEndpointMode, defaultGeminiEndpointModes(defaults), "上游接口能力")
}

// normalizeDeepSeekEndpointModesForWrite mirrors the deepSeek driver body:
// anthropic profiles pin the messages pair, openai profiles normalize plainly.
func normalizeDeepSeekEndpointModesForWrite(value optionalValue, context endpointModeDefaultContext) ([]string, error) {
	anthropicMessagesModes := map[string]bool{"messages_json": true, "messages_sse": true}
	if isAnthropicProtocolProfileOf(context.predicate()) || context.providerProtocolProfileID == deepSeekAnthropicV1ProfileID {
		pinned := context
		pinned.providerCode = deepSeekProviderCode
		pinned.providerProtocolProfileID = deepSeekAnthropicV1ProfileID
		modes, err := normalizeAnthropicEndpointModesForWrite(value, pinned)
		if err != nil {
			return nil, err
		}
		unsupported := []string{}
		for _, mode := range modes {
			if !anthropicMessagesModes[mode] {
				unsupported = append(unsupported, mode)
			}
		}
		if len(unsupported) > 0 {
			return nil, fmt.Errorf("DeepSeek Anthropic 账户上游接口能力只支持 Messages API (JSON) 或 Messages API (Streaming)：%s",
				strings.Join(unsupported, ", "))
		}
		return modes, nil
	}
	pinned := context
	pinned.providerCode = deepSeekProviderCode
	return normalizeOpenAIEndpointModesForWrite(value, pinned)
}

// normalizeGLMEndpointModesForWrite mirrors the glm driver body: anthropic
// profiles pin the messages pair, openai profiles pin the chat pair.
func normalizeGLMEndpointModesForWrite(value optionalValue, context endpointModeDefaultContext) ([]string, error) {
	anthropicMessagesModes := map[string]bool{"messages_json": true, "messages_sse": true}
	if isAnthropicProtocolProfileOf(context.predicate()) || context.providerProtocolProfileID == glmCodingAnthropicV1ProfileID {
		pinned := context
		pinned.providerCode = glmProviderCode
		pinned.providerProtocolProfileID = glmCodingAnthropicV1ProfileID
		modes, err := normalizeAnthropicEndpointModesForWrite(value, pinned)
		if err != nil {
			return nil, err
		}
		unsupported := []string{}
		for _, mode := range modes {
			if !anthropicMessagesModes[mode] {
				unsupported = append(unsupported, mode)
			}
		}
		if len(unsupported) > 0 {
			return nil, fmt.Errorf("智谱 GLM Coding Anthropic 账户上游接口能力只支持 Messages API (JSON) 或 Messages API (Streaming)：%s",
				strings.Join(unsupported, ", "))
		}
		return modes, nil
	}
	pinned := context
	pinned.providerCode = glmProviderCode
	modes, err := normalizeOpenAIEndpointModesForWrite(value, pinned)
	if err != nil {
		return nil, err
	}
	unsupported := []string{}
	for _, mode := range modes {
		if !openAIChatEndpointModeSet[mode] {
			unsupported = append(unsupported, mode)
		}
	}
	if len(unsupported) > 0 {
		chatCapabilityName := "对话补全"
		if context.providerProtocolProfileID == glmCodingOpenAIV1ProfileID {
			chatCapabilityName = "OpenAI Chat Completions"
		}
		return nil, fmt.Errorf("智谱 GLM 账户上游接口能力只支持 %s (JSON) 或 %s (Streaming)：%s",
			chatCapabilityName, chatCapabilityName, strings.Join(unsupported, ", "))
	}
	return modes, nil
}

// providerAccountCredentialDriverForContext mirrors
// providerAccountCredentialDriverForContext: normalize the provider token and
// fall back to the openai-compatible driver when both tokens are blank.
func providerAccountCredentialDriverForContext(context endpointModeDefaultContext) (*credentialDriver, error) {
	normalized := context
	normalized.providerCode = normalizeProviderToken(context.providerCode)
	if normalized.providerCode == "" && normalizeProviderToken(context.protocolCode) == "" {
		return &providerAccountCredentialDrivers[0], nil
	}
	for index := range providerAccountCredentialDrivers {
		if providerAccountCredentialDrivers[index].supportsContext(normalized) {
			return &providerAccountCredentialDrivers[index], nil
		}
	}
	return nil, nil
}

// normalizeEndpointModesForWrite mirrors the storage helper of the same name:
// resolve the driver for the context and delegate; an unregistered context is
// a hard error.
func normalizeEndpointModesForWrite(value optionalValue, context endpointModeDefaultContext) ([]string, error) {
	driver, err := providerAccountCredentialDriverForContext(context)
	if err != nil {
		return nil, err
	}
	if driver == nil {
		providerCode := context.providerCode
		if providerCode == "" {
			providerCode = "unknown"
		}
		return nil, errors.New("供应商协议档案未注册接口能力归一化：" + providerCode)
	}
	return driver.normalizeForWrite(value, context)
}

// ---- compatibility asserts (batch + write paths) ----

// assertOpenAIEndpointModesCompatible mirrors assertOpenAIEndpointModesCompatible.
func assertOpenAIEndpointModesCompatible(modes []string, accountType, clientCompatibility string) error {
	if accountType == "oauth" {
		responsesModes := stringSet(openAIResponsesEndpointModes)
		unsupported := []string{}
		for _, mode := range modes {
			if !responsesModes[mode] {
				unsupported = append(unsupported, mode)
			}
		}
		if len(unsupported) > 0 {
			return fmt.Errorf("OAuth 账户上游接口能力只能选择 Responses API (JSON) 或 Responses API (Streaming)")
		}
		if !containsString(modes, "responses_sse") {
			return errors.New("OAuth 账户上游接口能力必须启用 Responses API (Streaming)")
		}
	}
	if clientCompatibility == "codex_responses" && !containsString(modes, "responses_sse") {
		return errors.New("Codex Responses 账户上游接口能力必须启用 Responses API (Streaming)")
	}
	return nil
}

// assertAnthropicEndpointModesCompatible mirrors assertAnthropicEndpointModesCompatible.
func assertAnthropicEndpointModesCompatible(modes []string, accountType string) error {
	if accountType != "api_key" && accountType != "oauth" {
		return errors.New("Anthropic 当前仅支持 API Key 或 OAuth Access Token 账户")
	}
	known := stringSet(anthropicEndpointModeValues)
	unsupported := []string{}
	for _, mode := range modes {
		if !known[mode] {
			unsupported = append(unsupported, mode)
		}
	}
	if len(unsupported) > 0 {
		return fmt.Errorf("Anthropic 账户上游接口能力不支持：%s", strings.Join(unsupported, ", "))
	}
	if !containsString(modes, "messages_json") && !containsString(modes, "messages_sse") {
		return errors.New("Anthropic 账户上游接口能力必须至少启用 Messages API (JSON) 或 Messages API (Streaming)")
	}
	return nil
}

// assertGeminiEndpointModesCompatible mirrors assertGeminiEndpointModesCompatible.
func assertGeminiEndpointModesCompatible(modes []string, accountType string) error {
	if accountType != "api_key" && accountType != "google_oauth" {
		return errors.New("Gemini 原生协议当前仅支持 API Key 或 Google OAuth 账户")
	}
	known := stringSet(geminiEndpointModeValues)
	unsupported := []string{}
	for _, mode := range modes {
		if !known[mode] {
			unsupported = append(unsupported, mode)
		}
	}
	if len(unsupported) > 0 {
		return fmt.Errorf("Gemini 账户上游接口能力不支持：%s", strings.Join(unsupported, ", "))
	}
	for _, mode := range []string{"generate_content_json", "generate_content_sse", "interactions_json", "interactions_sse"} {
		if containsString(modes, mode) {
			return nil
		}
	}
	return errors.New("Gemini 账户上游接口能力必须至少启用 generateContent、streamGenerateContent 或 Interactions")
}

// assertEndpointModesCompatible mirrors account-batch-edit.service.ts
// assertEndpointModesCompatible: hybrid accounts skip the asserts entirely.
func assertEndpointModesCompatible(providerCode, accountType, clientCompatibility string, profile protocolPredicateInput, modes []string) error {
	if isHybridProviderCodeToken(providerCode) {
		return nil
	}
	if isAnthropicProtocolProfileOf(profile) {
		return assertAnthropicEndpointModesCompatible(modes, accountType)
	}
	if isOpenAIProtocolProfileOf(profile) {
		return assertOpenAIEndpointModesCompatible(modes, accountType, clientCompatibility)
	}
	if isGeminiProtocolProfileOf(profile) {
		return assertGeminiEndpointModesCompatible(modes, accountType)
	}
	return nil
}

// ---- health check endpoint mode (account-health-check-endpoint-mode.ts) ----

var accountHealthCheckEndpointModeOrder = []string{
	"images_json", "chat_json", "chat_sse", "responses_json", "responses_sse",
	"messages_json", "messages_sse", "generate_content_json", "generate_content_sse",
	"interactions_json", "interactions_sse",
}

func isAccountHealthCheckEndpointMode(value string) bool {
	return accountHealthCheckEndpointModes[value]
}

func preferredHealthCheckEndpointMode(providerCode, providerProtocolProfileID string) string {
	if providerProtocolProfileID == "profile_gemini_native_v1beta" {
		return "generate_content_json"
	}
	if strings.Contains(providerProtocolProfileID, "anthropic") {
		return "messages_json"
	}
	if providerCode == anthropicProviderCode {
		return "messages_json"
	}
	if providerCode == gptVendorCode {
		return "responses_sse"
	}
	return "chat_json"
}

// resolveDefaultHealthCheckEndpointMode mirrors resolveDefaultHealthCheckEndpointMode.
func resolveDefaultHealthCheckEndpointMode(providerCode, providerProtocolProfileID string, enabledModes []string) (string, error) {
	known := accountHealthCheckEndpointModes
	filtered := []string{}
	for _, mode := range enabledModes {
		if known[mode] {
			filtered = append(filtered, mode)
		}
	}
	preferred := preferredHealthCheckEndpointMode(providerCode, providerProtocolProfileID)
	if containsString(filtered, preferred) {
		return preferred, nil
	}
	for _, mode := range filtered {
		if strings.HasSuffix(mode, "_json") {
			return mode, nil
		}
	}
	if len(filtered) > 0 {
		return filtered[0], nil
	}
	return "", errors.New("账户至少需要启用一个可用于健康检查的请求形态")
}

// resolveHealthCheckEndpointMode mirrors resolveHealthCheckEndpointMode.
// modelSupportsImages: nil means the caller had no catalog evidence (Node
// passes account.healthCheckEndpointMode === 'images_json' in that slot).
func resolveHealthCheckEndpointMode(value *string, providerCode, providerProtocolProfileID string, enabledModes []string, modelSupportsImages *bool) (string, error) {
	if value == nil {
		return resolveDefaultHealthCheckEndpointMode(providerCode, providerProtocolProfileID, enabledModes)
	}
	if !isAccountHealthCheckEndpointMode(*value) {
		return "", errors.New("账户健康检查请求形态无效")
	}
	mode := *value
	if mode == "images_json" {
		if modelSupportsImages == nil || !*modelSupportsImages {
			return "", errors.New("检查模型未被模型目录证实支持 Images API")
		}
		return mode, nil
	}
	if !containsString(enabledModes, mode) {
		return "", fmt.Errorf("账户健康检查请求形态 %s 未启用", mode)
	}
	return mode, nil
}
