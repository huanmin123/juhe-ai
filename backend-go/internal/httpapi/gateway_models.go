package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/apikeysecret"
	"juhe-ai/backend-go/internal/modules/gatewayclientcatalog"
	"juhe-ai/backend-go/internal/modules/gatewaycredentials"
	"juhe-ai/backend-go/internal/modules/gatewayerrors"
	"juhe-ai/backend-go/internal/store/port"
)

// GatewayModelsCredentialAuthorizer is the model-discovery-specific API key boundary.
// Implementations authenticate the canonical credential and return only the
// owner and active route bindings needed to scope the client catalog.
// It deliberately does not use the full dispatch preflight: a valid key with
// zero active bindings must still receive a successful empty model list.
type GatewayModelsCredentialAuthorizer interface {
	AuthorizeGatewayModels(context.Context, string) (GatewayModelsAPIKeyScope, error)
}

// GatewayModelsCatalog is the read-only client catalog boundary used by the
// HTTP adapter. The API key path consumes the already-authorized provider
// scope rather than a dispatch-preflight DTO.
type GatewayModelsCatalog interface {
	APIKey(context.Context, GatewayModelsAPIKeyScope) ([]port.GatewayClientCatalogModel, error)
}

// GatewayModelsCredentialExtractor keeps HTTP field collection separate from
// the canonical credential parser. Custom transports can inject an equivalent
// adapter without teaching the handler credential precedence rules.
type GatewayModelsCredentialExtractor interface {
	ExtractGatewayModelsCredential(*http.Request, gatewayclientcatalog.ModelsResponseProtocol) (gatewaycredentials.Credential, error)
}

type GatewayModelsAPIKeyScope struct {
	SystemAccountID string
	ProviderCodes   []string
}

type GatewayModelsHandlerOptions struct {
	Credentials GatewayModelsCredentialExtractor
	Authorizer  GatewayModelsCredentialAuthorizer
	Catalog     GatewayModelsCatalog
}

type gatewayModelsCredentialAuthorizer struct {
	reader port.GatewayPreflightReader
	now    func() time.Time
}

func NewGatewayModelsCredentialAuthorizer(reader port.GatewayPreflightReader) GatewayModelsCredentialAuthorizer {
	return gatewayModelsCredentialAuthorizer{reader: reader, now: time.Now}
}

func (a gatewayModelsCredentialAuthorizer) AuthorizeGatewayModels(ctx context.Context, rawAPIKey string) (GatewayModelsAPIKeyScope, error) {
	if !strings.HasPrefix(rawAPIKey, "sk-") {
		return GatewayModelsAPIKeyScope{}, gatewayerrors.ErrAPIKeyInvalid
	}
	if a.reader == nil {
		return GatewayModelsAPIKeyScope{}, errors.New("gateway models credential reader is required")
	}
	now := a.now
	if now == nil {
		now = time.Now
	}
	current := now().UTC()
	record, found, err := a.reader.LoadGatewayPreflightAPIKey(ctx, apikeysecret.Hash(rawAPIKey))
	if err != nil {
		return GatewayModelsAPIKeyScope{}, err
	}
	if !found {
		return GatewayModelsAPIKeyScope{}, gatewayerrors.ErrAPIKeyInvalid
	}
	if record.APIKeyStatus != "active" || record.SystemAccountStatus != "active" {
		return GatewayModelsAPIKeyScope{}, gatewayerrors.ErrAPIKeyDisabled
	}
	if record.ExpiresAt != nil && !current.Before(*record.ExpiresAt) {
		return GatewayModelsAPIKeyScope{}, gatewayerrors.ErrAPIKeyExpired
	}
	scope := GatewayModelsAPIKeyScope{SystemAccountID: strings.TrimSpace(record.SystemAccountID)}
	if record.RouteStrategyStatus != "active" {
		return scope, nil
	}
	bindings, err := a.reader.ListGatewayPreflightBindings(ctx, record.ID, record.RouteStrategyID, record.SystemAccountID, current, 20)
	if err != nil {
		return GatewayModelsAPIKeyScope{}, err
	}
	seen := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		if binding.Status != "active" || !binding.GroupEnabled || (binding.AccessExpiresAt != nil && !current.Before(*binding.AccessExpiresAt)) {
			continue
		}
		providerCode := strings.ToLower(strings.TrimSpace(binding.ProviderCode))
		if providerCode != "" {
			seen[providerCode] = struct{}{}
		}
	}
	for providerCode := range seen {
		scope.ProviderCodes = append(scope.ProviderCodes, providerCode)
	}
	sort.Strings(scope.ProviderCodes)
	return scope, nil
}

func NewGatewayModelsHandler(opts GatewayModelsHandlerOptions) http.Handler {
	return newGatewayModelsHandler(opts)
}

// NewGatewayModelsCatalog adapts the gatewayclientcatalog service to the
// authorizer's compact provider-code scope.
func NewGatewayModelsCatalog(service *gatewayclientcatalog.Service) GatewayModelsCatalog {
	return gatewayModelsCatalogServiceAdapter{service: service}
}

type gatewayModelsCatalogServiceAdapter struct {
	service *gatewayclientcatalog.Service
}

func (a gatewayModelsCatalogServiceAdapter) APIKey(ctx context.Context, scope GatewayModelsAPIKeyScope) ([]port.GatewayClientCatalogModel, error) {
	if a.service == nil {
		return nil, errors.New("gateway client catalog service is required")
	}
	bindings := make([]gatewayclientcatalog.Binding, 0, len(scope.ProviderCodes))
	for _, providerCode := range scope.ProviderCodes {
		bindings = append(bindings, gatewayclientcatalog.Binding{ProviderCode: providerCode, Status: "active"})
	}
	return a.service.APIKey(ctx, gatewayclientcatalog.APIKeyInput{
		SystemAccountID: scope.SystemAccountID,
		Bindings:        bindings,
	})
}

func newGatewayModelsHandler(opts GatewayModelsHandlerOptions) http.Handler {
	credentials := opts.Credentials
	if credentials == nil {
		credentials = gatewayModelsHTTPCredentialExtractor{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocol, ok := gatewayModelsProtocol(r)
		if !ok {
			writeGatewayModelsFailure(w, gatewayclientcatalog.ModelsProtocolOpenAI, http.StatusNotFound, "not_found", "请求的资源不存在")
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeGatewayModelsFailure(w, protocol, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET 请求")
			return
		}

		credential, credentialErr := credentials.ExtractGatewayModelsCredential(r, protocol)
		if credentialErr != nil {
			setGatewayModelsPrivateCacheHeaders(w.Header())
			if errors.Is(credentialErr, gatewaycredentials.ErrMissingCredential) {
				writeGatewayModelsAuthenticationError(w, protocol, gatewayerrors.ErrCredentialMissing)
				return
			}
			writeGatewayModelsAuthenticationError(w, protocol, credentialErr)
			return
		}

		var items []port.GatewayClientCatalogModel
		setGatewayModelsPrivateCacheHeaders(w.Header())
		if opts.Authorizer == nil {
			writeGatewayModelsFailure(w, protocol, http.StatusServiceUnavailable, "service_unavailable", "模型目录服务暂时不可用")
			return
		}
		keyScope, authorizationErr := opts.Authorizer.AuthorizeGatewayModels(r.Context(), credential.Secret())
		if authorizationErr != nil {
			if _, classified := gatewayerrors.Classify(authorizationErr); classified {
				writeGatewayModelsAuthenticationError(w, protocol, authorizationErr)
			} else {
				writeGatewayModelsFailure(w, protocol, http.StatusServiceUnavailable, "service_unavailable", "模型目录服务暂时不可用")
			}
			return
		}
		if opts.Catalog == nil {
			writeGatewayModelsFailure(w, protocol, http.StatusServiceUnavailable, "service_unavailable", "模型目录服务暂时不可用")
			return
		}
		items, err := opts.Catalog.APIKey(r.Context(), GatewayModelsAPIKeyScope{
			SystemAccountID: keyScope.SystemAccountID,
			ProviderCodes:   append([]string(nil), keyScope.ProviderCodes...),
		})
		if err != nil {
			writeGatewayModelsFailure(w, protocol, http.StatusServiceUnavailable, "service_unavailable", "模型目录服务暂时不可用")
			return
		}

		payload, err := gatewayclientcatalog.BuildModelsResponse(protocol, items)
		if err != nil {
			writeGatewayModelsFailure(w, protocol, http.StatusServiceUnavailable, "service_unavailable", "模型目录服务暂时不可用")
			return
		}
		writeGatewayModelsJSON(w, http.StatusOK, payload)
	})
}

type gatewayModelsHTTPCredentialExtractor struct{}

func (gatewayModelsHTTPCredentialExtractor) ExtractGatewayModelsCredential(
	r *http.Request,
	protocol gatewayclientcatalog.ModelsResponseProtocol,
) (gatewaycredentials.Credential, error) {
	queryKeys := []string(nil)
	if values, exists := r.URL.Query()["key"]; exists {
		queryKeys = append(queryKeys, values...)
	}
	return gatewaycredentials.Extract(gatewaycredentials.Input{
		Authorization:   append([]string(nil), r.Header.Values("Authorization")...),
		XAPIKey:         append([]string(nil), r.Header.Values("X-API-Key")...),
		GeminiHeaderKey: append([]string(nil), r.Header.Values("X-Goog-API-Key")...),
		GeminiQueryKey:  queryKeys,
		GeminiNative:    protocol == gatewayclientcatalog.ModelsProtocolGemini,
	})
}

func gatewayModelsProtocol(r *http.Request) (gatewayclientcatalog.ModelsResponseProtocol, bool) {
	return gatewayclientcatalog.ResolveModelsResponseProtocol(gatewayclientcatalog.ModelsProtocolInput{
		// Resolve the response protocol independently of method so this
		// GET-only handler can still render a protocol-correct 405 response.
		Method:                http.MethodGet,
		PathAndQuery:          r.URL.RequestURI(),
		ExplicitProfile:       r.Header.Get("X-Juhe-Client-Profile"),
		UserAgent:             r.Header.Get("User-Agent"),
		Originator:            r.Header.Get("Originator"),
		CodexClient:           r.Header.Get("X-Codex-Client"),
		HasCodexClientVersion: headerHasNonEmptyValue(r.Header, "X-Codex-Client-Version"),
		HasGeminiAPIKey:       headerHasNonEmptyValue(r.Header, "X-Goog-API-Key"),
		HasAnthropicVersion:   headerHasNonEmptyValue(r.Header, "Anthropic-Version"),
		HasAnthropicBeta:      headerHasNonEmptyValue(r.Header, "Anthropic-Beta"),
		HasClaudeSessionID:    headerHasNonEmptyValue(r.Header, "X-Claude-Code-Session-Id"),
		HasClaudeAgentID:      headerHasNonEmptyValue(r.Header, "X-Claude-Code-Agent-Id"),
	})
}

func headerHasNonEmptyValue(header http.Header, name string) bool {
	for _, value := range header.Values(name) {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func setGatewayModelsPrivateCacheHeaders(header http.Header) {
	header.Set("Cache-Control", "private, no-cache")
	mergeGatewayModelsVary(header, []string{
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
		// Node omitted this discriminator even though it changes the Codex
		// response shape. Go includes it so shared caches cannot mix variants.
		"X-Codex-Client-Version",
	})
}

func mergeGatewayModelsVary(header http.Header, additions []string) {
	values := make([]string, 0, len(additions)+1)
	seen := make(map[string]struct{}, len(additions)+1)
	for _, item := range strings.Split(header.Get("Vary"), ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		key := strings.ToLower(item)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		values = append(values, item)
	}
	for _, item := range additions {
		key := strings.ToLower(item)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		values = append(values, item)
	}
	header.Set("Vary", strings.Join(values, ", "))
}

func writeGatewayModelsAuthenticationError(w http.ResponseWriter, protocol gatewayclientcatalog.ModelsResponseProtocol, err error) {
	publicError, ok := gatewayerrors.Classify(err)
	if !ok {
		writeGatewayModelsFailure(w, protocol, http.StatusServiceUnavailable, "service_unavailable", "模型目录服务暂时不可用")
		return
	}
	rendered := publicError.Render(gatewayModelsErrorProtocol(protocol))
	writeGatewayModelsJSON(w, rendered.StatusCode, rendered.Payload)
}

func gatewayModelsErrorProtocol(protocol gatewayclientcatalog.ModelsResponseProtocol) gatewayerrors.Protocol {
	switch protocol {
	case gatewayclientcatalog.ModelsProtocolAnthropic:
		return gatewayerrors.ProtocolAnthropic
	case gatewayclientcatalog.ModelsProtocolGemini:
		return gatewayerrors.ProtocolGemini
	default:
		return gatewayerrors.ProtocolOpenAI
	}
}

func writeGatewayModelsFailure(
	w http.ResponseWriter,
	protocol gatewayclientcatalog.ModelsResponseProtocol,
	status int,
	code string,
	message string,
) {
	var payload any
	switch protocol {
	case gatewayclientcatalog.ModelsProtocolAnthropic:
		payload = gatewayerrors.AnthropicErrorPayload{Type: "error", Error: gatewayerrors.ErrorDetail{Message: message, Type: "api_error", Code: code}}
	case gatewayclientcatalog.ModelsProtocolGemini:
		geminiStatus := "INTERNAL"
		if status == http.StatusServiceUnavailable {
			geminiStatus = "UNAVAILABLE"
		}
		payload = gatewayerrors.GeminiErrorPayload{Error: gatewayerrors.GeminiErrorDetail{Message: message, Status: geminiStatus, Code: code}}
	default:
		payload = gatewayerrors.OpenAIErrorPayload{Error: gatewayerrors.ErrorDetail{Message: message, Type: "api_error", Code: code}}
	}
	writeGatewayModelsJSON(w, status, payload)
}

func writeGatewayModelsJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
