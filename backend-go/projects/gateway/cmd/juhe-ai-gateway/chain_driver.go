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
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaygemini"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
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
}

func newChainProviderDriver() *chainProviderDriver {
	return &chainProviderDriver{openai: gatewayopenai.NewDriver()}
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
		parts, err := gatewaydispatch.BuildOpenAIOAuthCodexRequestParts(req, req.HTTP.Header, codexAccountOf(account), codexIdentityOf(account), gatewaydispatch.OpenAIOAuthCodexRequestOptions{
			ModelOverride:        canonicalAccountModel(req, account),
			SanitizeCodexHistory: true,
		})
		if err != nil {
			return gatewaydispatch.PreparedRequestParts{}, err
		}
		return gatewaydispatch.PreparedRequestParts{Headers: parts.Headers, Body: parts.Body}, nil
	}
	body := clientUpstreamBody(req)
	headers := upstreamHeadersOf(req, account)
	if mapping := d.resolveAccountModelMapping(account, req, requestClientCompatibility); mapping != nil {
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
	family := gatewayopenai.FamilyChatCompletions
	if req != nil {
		family = requestEndpointFamilyOf(req.PathAndQuery())
	}
	return gatewayopenai.ResolveAccountModelMapping(runtime, requestedModel, family)
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
