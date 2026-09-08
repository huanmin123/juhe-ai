package main

// G20 phase-2 composition-root adapter: the gatewaydispatch.ProviderDriver
// port (providers/drivers/registry.ts consumed surface) over the protocol
// packages gatewayproto/gatewayopenai/gatewayanthropic/gatewaygemini.
//
// Node authority: providers/drivers/registry.ts prepareGatewayUpstreamAccount
// / buildGatewayUpstreamUrlsForAccount / buildGatewayUpstreamRequestParts /
// accountSupportsGatewayRequest / gatewayRequestCapabilityMismatchReason.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayanthropic"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaygemini"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/openaicompat"
)

// Protocol codes mirrored from the protocol packages (the preauth re-exports
// the openai/gemini constants; anthropic carries its own).
const (
	driverProtocolOpenAI    = gatewayopenai.ProtocolCode
	driverProtocolAnthropic = gatewayanthropic.ProtocolCode
	driverProtocolGemini    = gatewaygemini.ProtocolCode
)

// chainProviderDriver implements gatewaydispatch.ProviderDriver.
type chainProviderDriver struct {
	// protocol registry for path/profile-based driver selection (openai is
	// registered; anthropic/gemini expose route helpers rather than the full
	// G01 driver surface and are handled through their URL builders below).
	openai *gatewayopenai.Driver
	// gptOverrideCatalog ports the provider model catalog for the D-151 gpt
	// request-override capability resolution (nil keeps capabilities
	// unresolved and the overrides inert).
	gptOverrideCatalog gatewaydispatch.GptRequestOverrideModelCatalog
}

func newChainProviderDriver() *chainProviderDriver {
	// D-151（BUG-0175）：gpt 账户请求覆盖 hook 原先零赋值，配置可保存但运行时
	// 静默无效。此处把归档 providers/drivers/gpt/request-overrides.ts 的
	// applyGptAccountRequestOverrides 挂到 normalize 链；能力解析走
	// gatewaydispatch.ResolveGptRequestOverrideModelCapabilities。
	gatewaydispatch.SetGptAccountRequestOverridesHook(gatewaydispatch.ApplyGptAccountRequestOverridesBody)
	return &chainProviderDriver{openai: gatewayopenai.NewDriver()}
}

// newChainProviderDriverWithCache wires the runtime-cache-backed provider model
// catalog into the D-151 capability resolution.
func newChainProviderDriverWithCache(cache *gatewayruntimecache.Service) *chainProviderDriver {
	driver := newChainProviderDriver()
	if cache != nil {
		driver.gptOverrideCatalog = chainGptRequestOverrideModelCatalog{cache: cache}
	}
	return driver
}

// chainGptRequestOverrideModelCatalog adapts *gatewayruntimecache.Service to
// gatewaydispatch.GptRequestOverrideModelCatalog (Node
// listCachedProviderModelCatalogAsync includeUnpriced: true).
type chainGptRequestOverrideModelCatalog struct {
	cache *gatewayruntimecache.Service
}

func (a chainGptRequestOverrideModelCatalog) ListGptRequestOverrideModelCatalog(ctx context.Context, providerCode, systemAccountID string, includeUnpriced bool) ([]gatewaydispatch.GptRequestOverrideModelCatalogItem, error) {
	items, err := a.cache.ListCachedProviderModelCatalogAsync(ctx, gatewayruntimecache.ModelCatalogListOptions{
		ProviderCode:    providerCode,
		SystemAccountID: systemAccountID,
		IncludeUnpriced: includeUnpriced,
	})
	if err != nil {
		return nil, err
	}
	out := make([]gatewaydispatch.GptRequestOverrideModelCatalogItem, 0, len(items))
	for _, item := range items {
		if item.Status != "active" {
			continue
		}
		out = append(out, gatewaydispatch.GptRequestOverrideModelCatalogItem{
			Model:                     item.Model,
			SupportedServiceTiers:     item.SupportedServiceTiers,
			SupportedReasoningEfforts: item.SupportedReasoningEfforts,
		})
	}
	return out, nil
}

// PrepareGatewayUpstreamAccount mirrors prepareGatewayUpstreamAccount. The
// api-key rotation halves of the Node prepare live on the engine
// (SelectAccountApiKeyForDispatch) and the codex OAuth request-parts halves
// run in BuildGatewayUpstreamRequestParts, so the registry-level prepare is
// the identity pass-through over the already-hydrated secret.
func (d *chainProviderDriver) PrepareGatewayUpstreamAccount(_ context.Context, account gatewaydispatch.AccountCandidate) (gatewaydispatch.AccountCandidate, error) {
	return account, nil
}

// BuildGatewayUpstreamURLsForAccount mirrors buildGatewayUpstreamUrlsForAccount:
// per-protocol URL construction with the account base URL.
func (d *chainProviderDriver) BuildGatewayUpstreamURLsForAccount(_ context.Context, account gatewaydispatch.AccountCandidate, req *gatewaypreauth.GatewayRequest) ([]string, error) {
	if req == nil || req.HTTP == nil || account.BaseURL == "" {
		return nil, fmt.Errorf("构建上游地址缺少请求或账户 baseUrl：%s", account.ID)
	}
	switch normalizeProtocol(account.ProtocolCode) {
	case driverProtocolAnthropic:
		urls := gatewayanthropic.BuildUpstreamURLsForAccount(gatewayanthropic.UpstreamAccount{
			Type:    account.Type,
			BaseURL: account.BaseURL,
		}, req.HTTP)
		if len(urls) == 0 {
			return nil, fmt.Errorf("账户 %s 不支持当前 Anthropic 请求路径", account.ID)
		}
		return urls, nil
	case driverProtocolGemini:
		// Code Assist / Google One OAuth 运行时恒定走 /v1internal 流式包装端点
		//（对齐 Node gemini/driver.ts:123-125 buildUpstreamUrls）；其余原生
		// 请求维持 gemini 路由 helper 的 URL 构建。
		if geminiAccountUsesCodeAssistRuntime(account) {
			if !isGeminiCodeAssistGenerationRequest(req) {
				return nil, fmt.Errorf("账户 %s 不支持当前 Gemini 请求路径", account.ID)
			}
			return []string{geminiCodeAssistUpstreamURL(account.BaseURL)}, nil
		}
		urls := gatewaygemini.BuildUpstreamURLsForAccount(gatewaygemini.UpstreamAccount{
			ID:      account.ID,
			Type:    account.Type,
			BaseURL: account.BaseURL,
		}, req.HTTP, gatewaypreauth.IsGatewayModelsRequest(req))
		if len(urls) == 0 {
			return nil, fmt.Errorf("账户 %s 不支持当前 Gemini 请求路径", account.ID)
		}
		return urls, nil
	default:
		// openai-compatible (openai / hybrid and any other OpenAI-style
		// upstream): always /v1-suffixed base + version-stripped path.
		return []string{gatewayopenai.BuildUpstreamURL(account.BaseURL, req.PathAndQuery())}, nil
	}
}

// BuildGatewayUpstreamRequestParts mirrors buildGatewayUpstreamRequestParts:
// upstream headers + body for one account. Codex OAuth accounts run through
// the dedicated request-parts builder; standard accounts forward the client
// body (model mapping applied through the openai driver transform when the
// account resolves a mapping) with the protocol auth header injected.
func (d *chainProviderDriver) BuildGatewayUpstreamRequestParts(
	ctx context.Context,
	req *gatewaypreauth.GatewayRequest,
	account gatewaydispatch.AccountCandidate,
	identity gatewaydispatch.UsageIdentity,
	requestClientCompatibility string,
) (gatewaydispatch.PreparedRequestParts, error) {
	_ = ctx
	_ = identity
	if req == nil {
		return gatewaydispatch.PreparedRequestParts{}, fmt.Errorf("构建上游请求缺少请求上下文")
	}
	if normalizeProtocol(account.ProtocolCode) == driverProtocolGemini && geminiAccountUsesCodeAssistRuntime(account) {
		return d.buildGeminiCodeAssistRequestParts(req, account)
	}
	if isCodexOAuthAccount(account) {
		modelOverride := canonicalAccountModel(req, account)
		// D-151（BUG-0175）：请求期能力解析 + body 应用。Node
		// gpt/driver.ts buildUpstreamRequestParts 的 oauth 分支在构造请求前
		// 先 normalizeGptRequestOverrideCapabilitiesForGateway。
		requestOverrideCapabilities, err := gatewaydispatch.ResolveGptRequestOverrideModelCapabilities(ctx, account, modelOverride)
		if err != nil {
			return gatewaydispatch.PreparedRequestParts{}, err
		}
		parts, err := gatewaydispatch.BuildOpenAIOAuthCodexRequestParts(req, req.HTTP.Header, codexAccountOf(account), codexIdentityOf(account), gatewaydispatch.OpenAIOAuthCodexRequestOptions{
			ModelOverride:                    modelOverride,
			SanitizeCodexHistory:             true,
			RequestOverrideModelCapabilities: requestOverrideCapabilities,
		})
		if err != nil {
			return gatewaydispatch.PreparedRequestParts{}, err
		}
		return gatewaydispatch.PreparedRequestParts{Headers: parts.Headers, Body: parts.Body}, nil
	}
	// D-99（BUG-0175）：openai-v1 api-key-client-compatibility 组合入口。Node
	// gpt / openai-compatible driver 的 api_key 分支在映射 body 之前先构造
	// compatibilityBody（codex_responses 客户端 + POST /responses 时把上游
	// body 规范化为 Codex Responses 形态并强制 SSE）。
	compatibilityBody, compatErr := d.buildOpenAIClientCompatibilityBody(req, requestClientCompatibility)
	if compatErr != nil {
		return gatewaydispatch.PreparedRequestParts{}, compatErr
	}
	body := clientUpstreamBody(req)
	headers := upstreamHeadersOf(req, account)
	if compatibilityBody != nil {
		body = compatibilityBody
		modelOverride := ""
		if mapping := d.resolveAccountModelMapping(account, req, requestClientCompatibility); mapping != nil {
			modelOverride = strings.TrimSpace(mapping.UpstreamModel)
		} else if canonical := canonicalAccountModel(req, account); canonical != "" {
			modelOverride = canonical
		} else if requested, ok := gatewaypreauth.RequestModel(req); ok {
			modelOverride = requested
		}
		applyOpenAIClientCompatibilityHeaders(headers, req, modelOverride, true)
	} else if mapping := d.resolveAccountModelMapping(account, req, requestClientCompatibility); mapping != nil {
		transformed, err := d.openai.BuildUpstreamRequest(gatewayproto.BuildUpstreamRequestInput{
			Method:              req.MethodUpper(),
			ClientPathAndQuery:  req.PathAndQuery(),
			Body:                body,
			Header:              req.HTTP.Header,
			ParsedBody:          req.ParsedJSONObjectBody(),
			ParsedBodyAvailable: req.ParsedJSONObjectBody() != nil,
			ModelMapping: &gatewayproto.ResolvedModelMapping{
				SourceModel:            mapping.SourceModel,
				SourceEndpointFamily:   mapping.SourceEndpointFamily,
				UpstreamModel:          strings.TrimSpace(mapping.UpstreamModel),
				UpstreamEndpointFamily: mapping.UpstreamEndpointFamily,
				RuntimeSource:          mapping.RuntimeSource,
				RuntimeRouteRuleID:     mapping.RuntimeRouteRuleID,
			},
		})
		if err != nil {
			return gatewaydispatch.PreparedRequestParts{}, err
		}
		body = transformed.Body
		if transformed.Stream {
			headers = headers.Clone()
			headers.Set("Accept", "text/event-stream")
		}
	} else if canonical := canonicalAccountModel(req, account); canonical != "" {
		requestedModel, _ := gatewaypreauth.RequestModel(req)
		if requestedModel != canonical {
			body = canonicalizeModelBody(body, req.ParsedJSONObjectBody(), canonical)
		}
	}
	// D-151：api_key 账户的运行时 body 应用（Node applyGptAccountRequestOverrides
	// 的非 oauth 分支）。端点族取映射上游族，缺省回落请求族；compact 仅出现在
	// /responses/compact 端点。
	if endpointFamily := gptRequestOverrideEndpointFamily(req, account, d.resolveAccountModelMapping(account, req, requestClientCompatibility)); endpointFamily != "" {
		upstreamModel := ""
		if requested, ok := gatewaypreauth.RequestModel(req); ok {
			upstreamModel = requested
		}
		if canonical := canonicalAccountModel(req, account); canonical != "" {
			upstreamModel = canonical
		}
		overridesBody, err := gatewaydispatch.ApplyGptAccountRequestOverridesToUpstreamBody(
			ctx, body, account, endpointFamily,
			gatewaydispatch.IsOpenAIOAuthCodexCompactRequest(req), upstreamModel)
		if err != nil {
			return gatewaydispatch.PreparedRequestParts{}, err
		}
		body = overridesBody
	}
	return gatewaydispatch.PreparedRequestParts{Headers: headers, Body: body}, nil
}

// buildGeminiCodeAssistRequestParts 对齐 Node buildGeminiCodeAssistRequestParts
// （code-assist-runtime.ts:51-70；探针 probe.go:1005-1020 为流式参照）：上游
// 恒定 /v1internal:streamGenerateContent?alt=sse 流式端点，请求体包装为
// {model, project, request}，头部为全新集合（authorization + content-type +
// GeminiCLI user-agent），不透传客户端其他头。model 优先取模型映射的上游
// 拼写，否则用请求模型。
func (d *chainProviderDriver) buildGeminiCodeAssistRequestParts(req *gatewaypreauth.GatewayRequest, account gatewaydispatch.AccountCandidate) (gatewaydispatch.PreparedRequestParts, error) {
	if !isGeminiCodeAssistGenerationRequest(req) {
		return gatewaydispatch.PreparedRequestParts{}, fmt.Errorf("Gemini Code Assist / Google One OAuth 仅支持 generateContent 与 streamGenerateContent")
	}
	model := ""
	if mapping := d.resolveAccountModelMapping(account, req, ""); mapping != nil {
		model = strings.TrimSpace(mapping.UpstreamModel)
	}
	if model == "" {
		if requested, ok := gatewaypreauth.RequestModel(req); ok {
			model = strings.TrimSpace(requested)
		}
	}
	if model == "" {
		return gatewaydispatch.PreparedRequestParts{}, fmt.Errorf("Gemini Code Assist 请求缺少 model")
	}
	project := accountCredentialText(account, "project_id")
	if project == "" {
		return gatewaydispatch.PreparedRequestParts{}, fmt.Errorf("Gemini Code Assist / Google One OAuth 缺少 project_id")
	}
	raw := clientUpstreamBody(req)
	if len(raw) == 0 {
		return gatewaydispatch.PreparedRequestParts{}, fmt.Errorf("Gemini Code Assist 请求体不能为空")
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return gatewaydispatch.PreparedRequestParts{}, fmt.Errorf("Gemini Code Assist 请求体必须是有效的 JSON 对象")
	}
	requestObject, ok := decoded.(map[string]any)
	if !ok {
		return gatewaydispatch.PreparedRequestParts{}, fmt.Errorf("Gemini Code Assist 请求体必须是 JSON 对象")
	}
	body, err := json.Marshal(map[string]any{"model": model, "project": project, "request": requestObject})
	if err != nil {
		return gatewaydispatch.PreparedRequestParts{}, fmt.Errorf("编码 Gemini Code Assist 请求体失败: %w", err)
	}
	credential := account.APIKey
	if credential == "" && len(account.APIKeys) > 0 {
		credential = account.APIKeys[0]
	}
	headers := http.Header{}
	if credential != "" {
		headers.Set("Authorization", "Bearer "+credential)
	}
	headers.Set("Content-Type", "application/json")
	headers.Set("User-Agent", geminiCLIUserAgent)
	return gatewaydispatch.PreparedRequestParts{Headers: headers, Body: body}, nil
}

// canonicalAccountModel returns the configured account spelling for a
// case-insensitive direct model match. An empty result means the account has
// no explicit supported-model constraint or the request model was unavailable.
func canonicalAccountModel(req *gatewaypreauth.GatewayRequest, account gatewaydispatch.AccountCandidate) string {
	if req == nil {
		return ""
	}
	requestedModel, ok := gatewaypreauth.RequestModel(req)
	if !ok {
		return ""
	}
	canonical := gatewayopenai.CanonicalModel(requestedModel, account.SupportedModels)
	if canonical == "" {
		return ""
	}
	return canonical
}

func canonicalizeModelBody(raw []byte, parsed any, canonical string) []byte {
	root, ok := parsed.(map[string]any)
	if !ok && len(raw) > 0 {
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err == nil {
			root, ok = decoded.(map[string]any)
		}
	}
	if !ok || canonical == "" {
		return raw
	}
	next := make(map[string]any, len(root)+1)
	for key, value := range root {
		next[key] = value
	}
	next["model"] = canonical
	encoded, err := json.Marshal(next)
	if err != nil {
		return raw
	}
	return encoded
}

// AccountSupportsGatewayRequest mirrors accountSupportsGatewayRequest: the
// protocol surface matches the account and the requested model is reachable
// through supported models or a resolved mapping.
func (d *chainProviderDriver) AccountSupportsGatewayRequest(req *gatewaypreauth.GatewayRequest, account gatewaydispatch.AccountCandidate, requestClientCompatibility string) bool {
	return d.gatewayRequestCapabilityMismatchReasonFor(req, account, requestClientCompatibility) == ""
}

// GatewayRequestCapabilityMismatchReason mirrors
// gatewayRequestCapabilityMismatchReason: the shared mismatch reason for the
// candidate set, empty when every account is compatible.
func (d *chainProviderDriver) GatewayRequestCapabilityMismatchReason(req *gatewaypreauth.GatewayRequest, accounts []gatewaydispatch.AccountCandidate) string {
	for _, account := range accounts {
		if reason := d.gatewayRequestCapabilityMismatchReasonFor(req, account, ""); reason != "" {
			return reason
		}
	}
	return ""
}

func (d *chainProviderDriver) gatewayRequestCapabilityMismatchReasonFor(req *gatewaypreauth.GatewayRequest, account gatewaydispatch.AccountCandidate, requestClientCompatibility string) string {
	protocol := normalizeProtocol(account.ProtocolCode)
	switch protocol {
	case driverProtocolAnthropic:
		if req != nil && !gatewayanthropic.IsNativeRequest(req.HTTP) {
			return "anthropic_native_unsupported"
		}
	case driverProtocolGemini:
		if req != nil {
			if !gatewaygemini.IsNativeRequest(req.HTTP) {
				return "gemini_native_unsupported"
			}
			// Code Assist / Google One OAuth 仅支持 generateContent 与
			// streamGenerateContent（Node accountSupportsRequest 同门）。
			if geminiAccountUsesCodeAssistRuntime(account) && !isGeminiCodeAssistGenerationRequest(req) {
				return "gemini_code_assist_unsupported_endpoint"
			}
		}
	}
	// Client compatibility: a pinned account only serves its own
	// compatibility class (Node clientCompatibility check).
	if account.ClientCompatibility != "" && requestClientCompatibility != "" &&
		!strings.EqualFold(account.ClientCompatibility, requestClientCompatibility) {
		return "client_compatibility_mismatch"
	}
	// D-99 eliminated 裁决（Node gpt/driver.ts accountSupportsRequest）：
	// OAuth 账户只服务 codex_responses 客户端形态，其他客户端兼容类直接淘汰。
	if account.Type == "oauth" && requestClientCompatibility != "" &&
		!strings.EqualFold(requestClientCompatibility, "codex_responses") {
		return "oauth_account_client_compatibility_unsupported"
	}
	// D-155（BUG-0175）：SupportedEndpointModes 消费。此前派发链对账户端点
	// 模式零消费，映射许可表成为唯一放行闸门。
	if reason := d.endpointModeMismatchReason(req, account, requestClientCompatibility); reason != "" {
		return reason
	}
	requestedModel := ""
	if req != nil {
		if model, ok := gatewaypreauth.RequestModel(req); ok {
			requestedModel = model
		}
	}
	if requestedModel == "" {
		return ""
	}
	if len(account.SupportedModels) == 0 {
		// No constraint configured: the account accepts every model (Node
		// supportedModels null semantics).
		return ""
	}
	if gatewayopenai.CanonicalModel(requestedModel, account.SupportedModels) != "" {
		return ""
	}
	if d.resolveAccountModelMapping(account, req, requestClientCompatibility) != nil {
		return ""
	}
	return "model_unsupported"
}

// requestMappingSourceFamilyOf returns the request-side source endpoint family
// in the stored model-mapping vocabulary (chat_completions / responses /
// messages / generate_content / stream_generate_content). The gateway dispatch
// filter resolves mappings with the same vocabulary (dispatch/candfilters.go
// gatewayRequestEndpointFamily); the previous chat/responses-only view made
// messages-source bridge mappings unresolvable in the driver chain (D-149).
func requestMappingSourceFamilyOf(req *gatewaypreauth.GatewayRequest) string {
	if req == nil {
		return gatewayopenai.FamilyChatCompletions
	}
	path := gatewaypreauth.RequestPathWithoutQuery(req)
	switch {
	case strings.Contains(path, "/chat/completions"):
		return gatewayopenai.FamilyChatCompletions
	case chainStripGatewayVersionPrefix(path) == "/responses":
		return gatewayopenai.FamilyResponses
	case strings.Contains(path, ":generateContent"):
		return "generate_content"
	case strings.Contains(path, ":streamGenerateContent"):
		return "stream_generate_content"
	case strings.Contains(path, ":countTokens"):
		return "messages"
	default:
		if chainStripGatewayVersionPrefix(path) == "/messages" && req.MethodUpper() == "POST" {
			return "messages"
		}
		return gatewayopenai.FamilyChatCompletions
	}
}

// resolveAccountModelMapping resolves the account mapping for the request
// model through the shared openai resolver (Node resolveOpenAICaccountModelMapping
// source of truth shared with the routing layer).
func (d *chainProviderDriver) resolveAccountModelMapping(account gatewaydispatch.AccountCandidate, req *gatewaypreauth.GatewayRequest, _ string) *gatewayproto.ResolvedModelMapping {
	requestedModel := ""
	if req != nil {
		if model, ok := gatewaypreauth.RequestModel(req); ok {
			requestedModel = model
		}
	}
	if requestedModel == "" {
		return nil
	}
	runtime := &gatewayopenai.RuntimeAccount{
		ModelMappings:             openAIModelMappingsOf(account.ModelMappings),
		ProviderCode:              account.ProviderCode,
		ProviderProtocolProfileID: account.ProviderProtocolProfileID,
		ProtocolCode:              account.ProtocolCode,
		ProtocolVersion:           account.ProtocolVersion,
	}
	return gatewayopenai.ResolveAccountModelMapping(runtime, requestedModel, requestMappingSourceFamilyOf(req))
}

// upstreamHeadersOf builds the upstream headers: hop-by-hop and gateway
// identity headers are dropped, the protocol credential header injected
// through the per-protocol auth form branches (BUG-0174 M-4/M-5).
func upstreamHeadersOf(req *gatewaypreauth.GatewayRequest, account gatewaydispatch.AccountCandidate) http.Header {
	headers := req.HTTP.Header.Clone()
	for _, name := range []string{
		"Authorization", "X-Api-Key", "X-Goog-Api-Key", "Cookie",
		"Host", "Content-Length", "Connection", "Keep-Alive",
		"Transfer-Encoding", "Upgrade", "Proxy-Connection", "Expect",
	} {
		headers.Del(name)
	}
	credential := account.APIKey
	if credential == "" && len(account.APIKeys) > 0 {
		credential = account.APIKeys[0]
	}
	switch normalizeProtocol(account.ProtocolCode) {
	case driverProtocolAnthropic:
		applyAnthropicUpstreamAuthHeaders(headers, account, credential)
	case driverProtocolGemini:
		applyGeminiUpstreamAuthHeaders(headers, account, credential)
	default:
		if credential != "" {
			headers.Set("Authorization", "Bearer "+credential)
		}
	}
	return headers
}

// anthropicOAuthBetaMergeValues 对齐 anthropicOAuthBetaHeaders
// （anthropic/driver.ts:68-73；探针 probe.go:422 逐值一致）：oauth 账户在
// 客户端 anthropic-beta 基础上合并的四个官方 CLI 标记。
var anthropicOAuthBetaMergeValues = []string{
	"claude-code-20250219",
	"oauth-2025-04-20",
	"interleaved-thinking-2025-05-14",
	"fine-grained-tool-streaming-2025-05-14",
}

// anthropicVersionHeaderValue 对齐 defaultAnthropicVersion（driver.ts:67）。
// gatewayanthropic.ProtocolVersion（"v1"）是 URL 协议段常量而非头值；
// 主链头值此前误用该常量（BUG-0174 M-4）。
const anthropicVersionHeaderValue = "2023-06-01"

// geminiCLIUserAgent 对齐 GEMINI_CLI_USER_AGENT
// （code-assist-runtime.ts:8；探针 probe.go:1019 逐值一致）。
const geminiCLIUserAgent = "GeminiCLI/0.1.5 (Windows; AMD64)"

// glmCodingAnthropicProfileID 是 GLM Coding Anthropic 档案标识（Node
// GLM_CODING_ANTHROPIC_V1_PROFILE_ID；探针 probe.go:416 同值判定）。
const glmCodingAnthropicProfileID = "profile_glm_coding_anthropic_v1"

// applyAnthropicUpstreamAuthHeaders 对齐 Node applyAnthropicUpstreamAuthHeaders
// + anthropicVersionHeader + anthropicBetaHeader + applyAnthropicOAuthCliIdentityHeaders
// （anthropic/driver.ts:146-157/329-352/355-358/278-292；探针 probe.go:415-434
// 为已对齐参照）：oauth 账户走 Bearer access_token + beta 四标记合并 +
// CLI 身份头；GLM Coding 档案走 Bearer API Key；其余走 x-api-key；
// anthropic-version 客户端显式携带时透传，否则默认 2023-06-01。
func applyAnthropicUpstreamAuthHeaders(headers http.Header, account gatewaydispatch.AccountCandidate, credential string) {
	isOAuth := account.Type == "oauth"
	switch {
	case isOAuth:
		if token := accountCredentialText(account, "access_token"); token != "" {
			headers.Set("Authorization", "Bearer "+token)
		}
	case account.ProviderProtocolProfileID == glmCodingAnthropicProfileID:
		if credential != "" {
			headers.Set("Authorization", "Bearer "+credential)
		}
	default:
		if credential != "" {
			headers.Set("X-Api-Key", credential)
		}
	}
	version := strings.TrimSpace(headers.Get("Anthropic-Version"))
	if version == "" {
		version = anthropicVersionHeaderValue
	}
	headers.Set("Anthropic-Version", version)
	if beta := mergeAnthropicBetaHeader(headers.Get("Anthropic-Beta"), isOAuth); beta != "" {
		headers.Set("Anthropic-Beta", beta)
	}
	if isOAuth {
		applyAnthropicOAuthCliIdentityHeaders(headers)
	}
}

// mergeAnthropicBetaHeader 对齐 anthropicBetaHeader（driver.ts:361-380）：
// 客户端值在前、oauth 四标记在后，逗号切分、大小写不敏感去重、保留首次出现。
func mergeAnthropicBetaHeader(clientBeta string, isOAuth bool) string {
	values := make([]string, 0, len(anthropicOAuthBetaMergeValues)+1)
	if strings.TrimSpace(clientBeta) != "" {
		values = append(values, clientBeta)
	}
	if isOAuth {
		values = append(values, anthropicOAuthBetaMergeValues...)
	}
	seen := make(map[string]struct{}, len(values))
	merged := make([]string, 0, len(values))
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			key := strings.ToLower(item)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			merged = append(merged, item)
		}
	}
	return strings.Join(merged, ",")
}

// applyAnthropicOAuthCliIdentityHeaders 对齐 applyAnthropicOAuthCliIdentityHeaders
// （anthropic/driver.ts:278-292；探针 probe.go:423-433 逐值一致）。
func applyAnthropicOAuthCliIdentityHeaders(headers http.Header) {
	identityHeaders := []struct{ Name, Value string }{
		{"User-Agent", "claude-cli/2.1.161 (external, cli)"},
		{"X-Stainless-Lang", "js"},
		{"X-Stainless-Package-Version", "0.94.0"},
		{"X-Stainless-Os", "Linux"},
		{"X-Stainless-Arch", "arm64"},
		{"X-Stainless-Runtime", "node"},
		{"X-Stainless-Runtime-Version", "v24.3.0"},
		{"X-Stainless-Retry-Count", "0"},
		{"X-Stainless-Timeout", "600"},
		{"X-App", "cli"},
		{"Anthropic-Dangerous-Direct-Browser-Access", "true"},
	}
	for _, header := range identityHeaders {
		headers.Set(header.Name, header.Value)
	}
}

// geminiOAuthClientHeaderAllowlist 对齐 commonOfficialOAuthHeaderNames +
// geminiOAuthCliHeaderNames（header-policy.ts:80-108）：google_oauth 账户按
// 'gemini_cli' 档案正向白名单复制客户端头（copyOfficialOAuthClientRequestHeaders）。
var geminiOAuthClientHeaderAllowlist = []string{
	"Accept", "Accept-Language", "Content-Type", "Idempotency-Key", "User-Agent",
	"Api-Revision", "X-Gemini-Api-Privileged-User-Id", "X-Goog-Api-Client",
	"X-Vertex-Ai-Llm-Request-Type", "X-Vertex-Ai-Llm-Shared-Request-Type",
}

// applyGeminiUpstreamAuthHeaders 对齐 Node gemini/driver.ts:164-174 +
// 探针 probe.go:436-461：google_oauth 账户删 x-goog-api-key、改 Bearer 授权、
// 可选 x-goog-user-project 并应用 gemini_cli 正向白名单；其余（api_key 等）
// 维持 x-goog-api-key。
func applyGeminiUpstreamAuthHeaders(headers http.Header, account gatewaydispatch.AccountCandidate, credential string) {
	if account.Type != "google_oauth" {
		if credential != "" {
			headers.Set("X-Goog-Api-Key", credential)
		}
		return
	}
	filterGeminiOAuthClientHeaders(headers)
	if credential != "" {
		headers.Set("Authorization", "Bearer "+credential)
	}
	if project := accountCredentialText(account, "quota_project_id"); project != "" {
		headers.Set("X-Goog-User-Project", project)
	}
}

// filterGeminiOAuthClientHeaders 对齐 copyOfficialOAuthClientRequestHeaders
// （header-policy.ts:119-149）：仅保留白名单头。
func filterGeminiOAuthClientHeaders(headers http.Header) {
	allowed := make(map[string]struct{}, len(geminiOAuthClientHeaderAllowlist))
	for _, name := range geminiOAuthClientHeaderAllowlist {
		allowed[http.CanonicalHeaderKey(name)] = struct{}{}
	}
	for name := range headers {
		if _, ok := allowed[name]; !ok {
			headers.Del(name)
		}
	}
}

// accountCredentialText 读取账户凭据中的文本字段（等价探针 credentialText）。
func accountCredentialText(account gatewaydispatch.AccountCandidate, key string) string {
	if account.Credentials == nil {
		return ""
	}
	value, ok := account.Credentials[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

// geminiAccountUsesCodeAssistRuntime 对齐 usesGeminiCodeAssistRuntime
// （code-assist-runtime.ts:40-48）：google_oauth 账户 oauth_type 为
// code_assist / google_one，或 oauth_type 缺省但配置了 project_id（legacy
// Code Assist 凭据）时走 Code Assist 包装运行时。
func geminiAccountUsesCodeAssistRuntime(account gatewaydispatch.AccountCandidate) bool {
	if account.Type != "google_oauth" {
		return false
	}
	oauthType := strings.ToLower(accountCredentialText(account, "oauth_type"))
	switch oauthType {
	case "code_assist", "google_one":
		return true
	case "":
		return accountCredentialText(account, "project_id") != ""
	default:
		return false
	}
}

// isGeminiCodeAssistGenerationRequest 对齐 isGeminiCodeAssistGenerationRequest
// （gemini/driver.ts，剔除跨协议 mapping 桥分支）：仅原生 generateContent 与
// streamGenerateContent 请求可走 Code Assist 流式端点。
func isGeminiCodeAssistGenerationRequest(req *gatewaypreauth.GatewayRequest) bool {
	if req == nil || !gatewaygemini.IsNativeRequest(req.HTTP) {
		return false
	}
	family := gatewaygemini.EndpointFamilyFromPath(gatewaygemini.RequestPathAndQuery(req.HTTP))
	return family == gatewaygemini.EndpointFamilyGenerateContent ||
		family == gatewaygemini.EndpointFamilyStreamGenerateContent
}

// geminiCodeAssistUpstreamURL 对齐 geminiCodeAssistStreamUrl
// （code-assist-runtime.ts:16-18）：恒定 /v1internal:streamGenerateContent?alt=sse
// 流式端点；base 取账户 base_url（探针 buildUpstreamURL 同一取值来源）。
func geminiCodeAssistUpstreamURL(baseURL string) string {
	return strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/v1internal:streamGenerateContent?alt=sse"
}

// clientUpstreamBody returns the serialized upstream body (the gateway body
// pipeline cache when present, otherwise the raw body).
func clientUpstreamBody(req *gatewaypreauth.GatewayRequest) []byte {
	if req.Body != nil && req.Body.UpstreamBodyCache != nil && len(req.Body.UpstreamBodyCache.PassthroughBody) > 0 {
		return req.Body.UpstreamBodyCache.PassthroughBody
	}
	if req.Body != nil && len(req.Body.RawBody) > 0 {
		return req.Body.RawBody
	}
	return nil
}

func isCodexOAuthAccount(account gatewaydispatch.AccountCandidate) bool {
	return account.Type == "oauth" && normalizeProtocol(account.ProtocolCode) == driverProtocolOpenAI
}

func codexAccountOf(account gatewaydispatch.AccountCandidate) gatewaydispatch.OpenAIOAuthCodexAccount {
	return gatewaydispatch.OpenAIOAuthCodexAccount{
		ID:          account.ID,
		APIKey:      account.APIKey,
		Credentials: account.Credentials,
	}
}

func codexIdentityOf(account gatewaydispatch.AccountCandidate) gatewaydispatch.OpenAIOAuthCodexIdentity {
	return gatewaydispatch.OpenAIOAuthCodexIdentity{
		SystemAccountID: account.SystemAccountID,
		APIKeyID:        account.ID,
		GroupID:         deref(account.BoundGroupID),
	}
}

// requestEndpointFamilyOf mirrors openAIEndpointFamilyFromPath for the shared
// model-mapping resolver (unexported in gatewayopenai, re-derived here).
func requestEndpointFamilyOf(pathAndQuery string) string {
	path := strings.ToLower(strings.TrimSpace(pathAndQuery))
	if index := strings.Index(path, "?"); index >= 0 {
		path = path[:index]
	}
	switch {
	case strings.Contains(path, "/chat/completions"):
		return gatewayopenai.FamilyChatCompletions
	case strings.Contains(path, "/responses"):
		return gatewayopenai.FamilyResponses
	default:
		return gatewayopenai.FamilyChatCompletions
	}
}

func normalizeProtocol(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// openAIModelMappingsOf converts the runtime-cache mapping rows onto the
// openai mapping rows (identical field vocabulary, pointer Enabled).
func openAIModelMappingsOf(mappings []gatewayruntimecache.AccountModelMapping) []gatewayopenai.AccountModelMapping {
	out := make([]gatewayopenai.AccountModelMapping, 0, len(mappings))
	for _, mapping := range mappings {
		enabled := mapping.Enabled
		out = append(out, gatewayopenai.AccountModelMapping{
			SourceModel:            mapping.SourceModel,
			SourceEndpointFamily:   mapping.SourceEndpointFamily,
			UpstreamModel:          mapping.UpstreamModel,
			UpstreamEndpointFamily: mapping.UpstreamEndpointFamily,
			Enabled:                &enabled,
			RuntimeSource:          deref(mapping.RuntimeSource),
			RuntimeRouteRuleID:     deref(mapping.RuntimeRouteRuleID),
		})
	}
	return out
}

func containsTrimmed(values []string, target string) bool {
	target = strings.TrimSpace(target)
	for _, value := range values {
		if gatewayopenai.ModelsEqual(value, target) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// D-99（BUG-0175）：openai-v1 api-key-client-compatibility
// （gateway/protocols/openai-v1/api-key-client-compatibility.ts 全量移植）。
// codex_responses 客户端 + POST /responses 的 api_key 账户：上游 body 强制
// 规范化为 Codex Responses 形态，SSE 强制开启。
// ---------------------------------------------------------------------------

// shouldForceOpenAICodexResponsesSse mirrors shouldForceOpenAICodexResponsesSse.
func shouldForceOpenAICodexResponsesSse(req *gatewaypreauth.GatewayRequest, requestClientCompatibility string) bool {
	return strings.EqualFold(requestClientCompatibility, "codex_responses") &&
		isOpenAIResponsesPostRequestForCompatibility(req)
}

// isOpenAIResponsesPostRequest mirrors isOpenAIResponsesPostRequest.
func isOpenAIResponsesPostRequestForCompatibility(req *gatewaypreauth.GatewayRequest) bool {
	if req == nil || req.MethodUpper() != "POST" {
		return false
	}
	path := gatewaypreauth.RequestPathWithoutQuery(req)
	return chainStripGatewayVersionPrefix(path) == "/responses"
}

// chainStripGatewayVersionPrefix mirrors the Node ^\/v1(?=\/|$) path
// normalization used by the openai-v1 route helpers.
func chainStripGatewayVersionPrefix(path string) string {
	for _, prefix := range []string{"/v1/", "/v1beta/"} {
		if strings.HasPrefix(path, prefix) {
			return path[len(prefix)-1:]
		}
	}
	if path == "/v1" || path == "/v1beta" {
		return "/"
	}
	return path
}

// buildOpenAIClientCompatibilityBody mirrors buildOpenAIClientCompatibilityBody:
// nil when the request is not a codex_responses /responses POST.
func (d *chainProviderDriver) buildOpenAIClientCompatibilityBody(req *gatewaypreauth.GatewayRequest, requestClientCompatibility string) ([]byte, error) {
	if !shouldForceOpenAICodexResponsesSse(req, requestClientCompatibility) {
		return nil, nil
	}
	body, err := parseOpenAIClientCompatibilityJSONObject(req)
	if err != nil {
		return nil, err
	}
	modelOverride := d.compatibilityModelOverride(req)
	if modelOverride != "" {
		body["model"] = modelOverride
	}
	applyCodexResponsesCompatibility(body)
	gatewaydispatch.NormalizeOpenAICodexResponsesLiteBody(body, stringValueOrEmpty(body["model"]), nil)
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("编码 Codex Responses 兼容请求体失败: %w", err)
	}
	return encoded, nil
}

// compatibilityModelOverride mirrors options.modelOverride at the caller: the
// resolved upstream model wins, otherwise the request model.
func (d *chainProviderDriver) compatibilityModelOverride(req *gatewaypreauth.GatewayRequest) string {
	if req == nil {
		return ""
	}
	if requested, ok := gatewaypreauth.RequestModel(req); ok {
		return strings.TrimSpace(requested)
	}
	return ""
}

func stringValueOrEmpty(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

// parseOpenAIClientCompatibilityJSONObject mirrors
// parseOpenAIClientCompatibilityJsonBody.
func parseOpenAIClientCompatibilityJSONObject(req *gatewaypreauth.GatewayRequest) (map[string]any, error) {
	if req == nil {
		return map[string]any{}, nil
	}
	if parsed := req.ParsedJSONObjectBody(); parsed != nil {
		object := make(map[string]any, len(parsed))
		for key, value := range parsed {
			object[key] = value
		}
		return object, nil
	}
	if state := req.BodyState(); state != nil && state.JSONParseStatus == gatewaybody.JSONParseStatusInvalidJSON {
		return nil, fmt.Errorf("Codex Responses 请求形态要求请求体是有效的 JSON 对象")
	}
	rawBody := clientUpstreamBody(req)
	if len(rawBody) == 0 {
		return map[string]any{}, nil
	}
	var parsed any
	if err := json.Unmarshal(rawBody, &parsed); err != nil {
		return nil, fmt.Errorf("Codex Responses 请求形态要求请求体是有效的 JSON 对象")
	}
	object, ok := parsed.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Codex Responses 请求形态要求请求体是 JSON 对象")
	}
	cloned := make(map[string]any, len(object))
	for key, value := range object {
		cloned[key] = value
	}
	return cloned, nil
}

// applyCodexResponsesCompatibility mirrors applyCodexResponsesCompatibility
// (non-strict account test requests: the gateway path never preserves the
// output budget).
func applyCodexResponsesCompatibility(body map[string]any) {
	if text, ok := body["input"].(string); ok {
		body["input"] = []any{
			map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": text},
				},
			},
		}
	} else if items, ok := body["input"].([]any); ok {
		body["input"] = normalizeCodexResponsesInputItems(items)
	}
	if _, has := body["instructions"]; !has {
		body["instructions"] = ""
	}
	_, toolsIsArray := body["tools"].([]any)
	if !toolsIsArray {
		if _, has := body["tools"]; has {
			body["tools"] = []any{}
		} else if !codexResponsesInputHasAdditionalTools(body["input"]) {
			body["tools"] = []any{}
		}
	}
	if _, ok := body["tool_choice"].(string); !ok {
		if _, ok := body["tool_choice"].(map[string]any); !ok {
			body["tool_choice"] = "auto"
		}
	}
	gatewaydispatch.NormalizeOpenAICodexBuiltinTools(body)
	if _, ok := body["parallel_tool_calls"].(bool); !ok {
		body["parallel_tool_calls"] = true
	}
	body["stream"] = true
	body["store"] = false
	body["include"] = ensureCodexResponsesReasoningEncryptedContent(body["include"])
	delete(body, "max_output_tokens")
	delete(body, "max_completion_tokens")
	delete(body, "temperature")
	delete(body, "top_p")
	delete(body, "context_management")
	delete(body, "truncation")
	delete(body, "user")
}

func codexResponsesInputHasAdditionalTools(value any) bool {
	items, ok := value.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		if record, ok := item.(map[string]any); ok && record["type"] == "additional_tools" {
			return true
		}
	}
	return false
}

func ensureCodexResponsesReasoningEncryptedContent(value any) []any {
	include := []any{}
	if items, ok := value.([]any); ok {
		for _, item := range items {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				include = append(include, text)
			}
		}
	}
	for _, item := range include {
		if text, ok := item.(string); ok && text == "reasoning.encrypted_content" {
			return include
		}
	}
	return append(include, "reasoning.encrypted_content")
}

func normalizeCodexResponsesInputItems(items []any) []any {
	output := make([]any, 0, len(items))
	for _, item := range items {
		record, ok := item.(map[string]any)
		if !ok || record["role"] != "system" {
			output = append(output, item)
			continue
		}
		converted := make(map[string]any, len(record)+1)
		for key, value := range record {
			converted[key] = value
		}
		converted["role"] = "developer"
		output = append(output, converted)
	}
	return output
}

// applyOpenAIClientCompatibilityHeaders mirrors applyOpenAIClientCompatibilityHeaders.
// forced carries the caller's shouldForceOpenAICodexResponsesSse verdict (the
// compatibility body path only runs when forced).
func applyOpenAIClientCompatibilityHeaders(headers http.Header, req *gatewaypreauth.GatewayRequest, modelOverride string, forced bool) {
	if !forced {
		return
	}
	if headers == nil {
		return
	}
	if gatewaydispatch.IsOpenAICodexClientHeaders(headers) {
		return
	}
	headers.Set("Accept", "text/event-stream")
	headers.Set("Content-Type", "application/json")
	if modelOverride == "" {
		if requested, ok := gatewaypreauth.RequestModel(req); ok {
			modelOverride = requested
		}
	}
	gatewaydispatch.NormalizeOpenAICodexClientHeaders(headers, modelOverride)
}

// gptRequestOverrideEndpointFamily mirrors gptRequestOverrideEndpointFamily:
// the override wire family is the mapping upstream family, falling back to the
// request family, and limited to chat_completions / responses.
func gptRequestOverrideEndpointFamily(req *gatewaypreauth.GatewayRequest, account gatewaydispatch.AccountCandidate, mapping *gatewayproto.ResolvedModelMapping) string {
	family := ""
	if mapping != nil && mapping.UpstreamEndpointFamily != "" {
		family = mapping.UpstreamEndpointFamily
	} else {
		family = requestEndpointFamilyOf(req.PathAndQuery())
	}
	switch family {
	case "chat_completions", "responses":
		return family
	default:
		return ""
	}
}

// ---------------------------------------------------------------------------
// D-155（BUG-0175）：SupportedEndpointModes 派发消费。
// ---------------------------------------------------------------------------

// endpointModeMismatchReason reports the unsupported-endpoint-mode reason for
// one account, or "" when the account serves the request shape. Accounts with
// no explicit mode constraint (nil / empty after runtime normalization) keep
// the unconstrained default semantics.
func (d *chainProviderDriver) endpointModeMismatchReason(req *gatewaypreauth.GatewayRequest, account gatewaydispatch.AccountCandidate, requestClientCompatibility string) string {
	if len(account.SupportedEndpointModes) == 0 {
		return ""
	}
	mode, required := d.requiredSupportedEndpointMode(req, account, requestClientCompatibility)
	if !required {
		return ""
	}
	if mode == "" {
		// The request shape carries no gated mode: do not filter.
		return ""
	}
	for _, supported := range account.SupportedEndpointModes {
		if supported == mode {
			return ""
		}
	}
	return "endpoint_mode_unsupported"
}

// requiredSupportedEndpointMode computes the endpoint-mode token this request
// would exercise on the account (bridge mappings remap the vocabulary onto the
// upstream family, mirroring the Node bridge required-mode helpers). The
// second result reports whether a mode verdict exists at all; a false value
// means the account is not filtered by endpoint modes for this request, a
// true value with an empty mode means the request is outside the protocol
// surface (e.g. OAuth gpt accounts only carry Responses) and must be
// eliminated.
func (d *chainProviderDriver) requiredSupportedEndpointMode(req *gatewaypreauth.GatewayRequest, account gatewaydispatch.AccountCandidate, requestClientCompatibility string) (string, bool) {
	if req == nil {
		return "", false
	}
	stream := gatewaypreauth.RequestStream(req)
	// codex_responses 客户端的 /responses POST 强制 SSE（D-99），账户必须持有
	// responses_sse。
	if strings.EqualFold(requestClientCompatibility, "codex_responses") &&
		isOpenAIResponsesPostRequestForCompatibility(req) {
		return gatewaypreauth.EndpointModeResponsesSSE, true
	}
	// 跨协议映射把许可词汇表切到上游族（Node
	// anthropicMessagesChatBridgeRequiredEndpointMode /
	// geminiGenerateContentChatBridgeRequiredEndpointMode /
	// codexResponsesChatBridgeRequiredEndpointMode /
	// openAIToAnthropicBridgeRequiredEndpointMode）。
	if mapping := d.resolveAccountModelMapping(account, req, requestClientCompatibility); mapping != nil &&
		mapping.UpstreamEndpointFamily != "" && mapping.UpstreamEndpointFamily != mapping.SourceEndpointFamily {
		switch openaicompat.NormalizeEndpointFamily(mapping.UpstreamEndpointFamily) {
		case "chat_completions":
			if stream || openaicompat.NormalizeEndpointFamily(mapping.SourceEndpointFamily) == "responses" {
				return gatewaypreauth.EndpointModeChatSSE, true
			}
			return gatewaypreauth.EndpointModeChatJSON, true
		case "anthropic_messages":
			if stream {
				return gatewaypreauth.EndpointModeMessagesSSE, true
			}
			return gatewaypreauth.EndpointModeMessagesJSON, true
		case "gemini_generate_content", "gemini_stream_generate":
			if stream {
				return gatewaypreauth.EndpointModeGenerateContentSSE, true
			}
			return gatewaypreauth.EndpointModeGenerateContentJSON, true
		default:
			return "", false
		}
	}
	switch normalizeProtocol(account.ProtocolCode) {
	case driverProtocolAnthropic:
		mode := gatewaypreauth.RequestSupportedEndpointMode(req)
		switch mode {
		case gatewaypreauth.EndpointModeMessagesJSON, gatewaypreauth.EndpointModeMessagesSSE, gatewaypreauth.EndpointModeMessageTokenCounting:
			return mode, true
		default:
			// Non-anthropic shapes on an anthropic account are rejected by the
			// protocol surface check above; no additional mode gate.
			return "", false
		}
	case driverProtocolGemini:
		if geminiAccountUsesCodeAssistRuntime(account) {
			return gatewaypreauth.EndpointModeGenerateContentSSE, true
		}
		return gatewaypreauth.RequestSupportedEndpointMode(req), true
	default:
		// openai 协议面：mode 为空（/models 等未闸门形态）不过滤；oauth 账户
		// 的 chat/images 形态经由账户 responses-only 模式集产生淘汰（Node
		// accountSupportsOpenAIEndpointMode 语义）。
		return gatewaypreauth.RequestSupportedEndpointMode(req), true
	}
}
