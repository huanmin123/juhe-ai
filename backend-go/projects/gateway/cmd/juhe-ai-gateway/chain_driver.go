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
	if isCodexOAuthAccount(account) {
		parts, err := gatewaydispatch.BuildOpenAIOAuthCodexRequestParts(req, req.HTTP.Header, codexAccountOf(account), codexIdentityOf(account), gatewaydispatch.OpenAIOAuthCodexRequestOptions{
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
				UpstreamModel:          mapping.UpstreamModel,
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
	}
	return gatewaydispatch.PreparedRequestParts{Headers: headers, Body: body}, nil
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
		if req != nil && !gatewaygemini.IsNativeRequest(req.HTTP) {
			return "gemini_native_unsupported"
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
	if containsTrimmed(account.SupportedModels, requestedModel) {
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
// identity headers are dropped, the protocol credential header injected.
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
		if credential != "" {
			headers.Set("X-Api-Key", credential)
		}
		headers.Set("Anthropic-Version", gatewayanthropic.ProtocolVersion)
	case driverProtocolGemini:
		if credential != "" {
			headers.Set("X-Goog-Api-Key", credential)
		}
	default:
		if credential != "" {
			headers.Set("Authorization", "Bearer "+credential)
		}
	}
	return headers
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
		if strings.TrimSpace(value) == target {
			return true
		}
	}
	return false
}
