package gatewaycodex

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// Port of client-profiles/strategy.ts: the gateway client profile (codex /
// generic_openai / claude_code / gemini_cli families), the downstream
// protocol resolution and the retry coordination contract.

// GatewayClientProfileHeader mirrors gatewayClientProfileHeader.
const GatewayClientProfileHeader = "x-juhe-client-profile"

// Client profiles.
const (
	ClientProfileCodex            = "codex"
	ClientProfileGenericOpenAI    = "generic_openai"
	ClientProfileClaudeCode       = "claude_code"
	ClientProfileGenericAnthropic = "generic_anthropic"
	ClientProfileGeminiCLI        = "gemini_cli"
	ClientProfileGenericGemini    = "generic_gemini"
)

// Downstream protocols.
const (
	DownstreamResponsesSSE            = "responses_sse"
	DownstreamChatCompletionsSSE      = "chat_completions_sse"
	DownstreamMessagesSSE             = "messages_sse"
	DownstreamGeminiStreamGenerateSSE = "gemini_stream_generate_content_sse"
	DownstreamGeminiInteractionsSSE   = "gemini_interactions_sse"
	DownstreamJSON                    = "json"
	DownstreamUnknownStream           = "unknown_stream"
)

// Upstream adapters.
const (
	UpstreamAdapterOpenAIAPIKey     = "openai_api_key"
	UpstreamAdapterOpenAIOAuthCodex = "openai_oauth_codex"
	UpstreamAdapterOpenAIMixed      = "openai_mixed"
	UpstreamAdapterAnthropicAPIKey  = "anthropic_api_key"
	UpstreamAdapterGeminiAPIKey     = "gemini_api_key"
)

// Request client compatibility values.
const (
	CompatibilityCodexResponses  = "codex_responses"
	CompatibilityOpenAIStandard  = "openai_standard"
	CompatibilityClaudeCode      = "claude_code"
	CompatibilityAnthropicNative = "anthropic_native"
)

// Client profile sources.
const (
	ProfileSourceDefault                    = "default"
	ProfileSourceExplicitHeader             = "explicit_header"
	ProfileSourceCodexTurnMetadata          = "codex_turn_metadata"
	ProfileSourceClaudeCodeRequestSignature = "claude_code_request_signature"
	ProfileSourceGeminiCLIRequestSignature  = "gemini_cli_request_signature"
)

// Retry coordination signals.
const (
	FailureSignalProtocolErrorEvent = "protocol_error_event"
	FailureSignalHTTPError          = "http_error"
	FailureSignalDisconnect         = "disconnect"
)

// GatewayClientRetryCoordination mirrors GatewayClientRetryCoordination.
type GatewayClientRetryCoordination struct {
	PreCommitFailureSignal string
	CommittedFailureSignal string
}

// OpenAIGatewayCodexTurnContext mirrors OpenAIGatewayCodexTurnContext.
type OpenAIGatewayCodexTurnContext struct {
	TurnID    string
	SessionID string
	ThreadID  string
	// StateKey is always an HMAC of the complete source scope. It is absent
	// when no safe source identity exists, which disables persistent
	// avoidance.
	StateKey   string
	SourceKind GatewayClientSourceKind
}

// OpenAIGatewayClientStrategyContext mirrors
// OpenAIGatewayClientStrategyContext.
type OpenAIGatewayClientStrategyContext struct {
	ClientProfile              string
	RequestClientCompatibility string
	DownstreamProtocol         string
	UpstreamAdapter            string
	CodexCompactionExpected    bool
	CodexTurn                  *OpenAIGatewayCodexTurnContext
	ClientSource               *GatewayClientSourceIdentity
	// A source key is intentionally narrower than account health. Codex keeps
	// its turn as a child scope; every other supported client uses the common
	// source scope directly.
	ClientSourceAvoidanceStateKey     string
	ClientProfileSource               string
	RetryCoordination                 GatewayClientRetryCoordination
	AllowClientSourceAccountAvoidance bool
	AllowCodexTurnAccountAvoidance    bool
}

// ClientStrategyIdentity mirrors OpenAIGatewayClientStrategyIdentity.
type ClientStrategyIdentity struct {
	SystemAccountID           string
	APIKeyID                  string
	GroupID                   string
	Endpoint                  string
	ProviderCode              string
	ProviderProtocolProfileID string
	ProtocolCode              string
	ProtocolVersion           string
	ClientIP                  string
}

// ResponseProtocolForRequest mirrors
// gatewayProtocolResponseProtocolForRequest(req, identity): the request's
// response protocol family. The gatewaypreauth protocol view owns the
// driver match.
func ResponseProtocolForRequest(req *gatewaypreauth.GatewayRequest) string {
	protocol, ok := gatewayProtocolResponseProtocol(req)
	if !ok {
		// Node falls back to the openai driver's response protocol through
		// requireGatewayProtocolDriverForProfile; the openai path space is
		// the default owner.
		return "openai_v1"
	}
	return protocol
}

// gatewayProtocolResponseProtocol maps the gatewaypreauth driver match onto
// the Node response protocol codes.
func gatewayProtocolResponseProtocol(req *gatewaypreauth.GatewayRequest) (string, bool) {
	if req == nil {
		return "", false
	}
	if gatewaypreauth.IsOpenAIProtocolRequestPath(req.PathAndQuery()) {
		return "openai_v1", true
	}
	if req.HTTP != nil && gatewaypreauth.IsGatewayProtocolNativeRequest(req, "anthropic") {
		return "anthropic_v1", true
	}
	if req.HTTP != nil && gatewaypreauth.IsGatewayProtocolNativeRequest(req, "gemini") {
		return "gemini_v1beta", true
	}
	return "", false
}

// GatewayClientAllowsUpstreamSemanticInterpretation mirrors
// gatewayClientAllowsUpstreamSemanticInterpretation.
func GatewayClientAllowsUpstreamSemanticInterpretation(strategy OpenAIGatewayClientStrategyContext) bool {
	return strategy.ClientProfile == ClientProfileCodex ||
		strategy.ClientProfile == ClientProfileClaudeCode ||
		strategy.ClientProfile == ClientProfileGeminiCLI
}

// ClientStrategyDeps carries the strategy collaborators.
type ClientStrategyDeps struct {
	CompactionExpected func(req *gatewaypreauth.GatewayRequest) bool
	Source             *SourceIdentityResolver
}

// ResolveOpenAIGatewayClientStrategy mirrors
// resolveOpenAIGatewayClientStrategy.
func (d *ClientStrategyDeps) ResolveOpenAIGatewayClientStrategy(req *gatewaypreauth.GatewayRequest, identity ClientStrategyIdentity) OpenAIGatewayClientStrategyContext {
	responseProtocol := ResponseProtocolForRequest(req)
	if responseProtocol == "anthropic_v1" {
		return d.ResolveAnthropicGatewayClientStrategy(req, &identity)
	}
	if responseProtocol == "gemini_v1beta" {
		return d.ResolveGeminiGatewayClientStrategy(req, &identity)
	}
	downstreamProtocol := ResolveOpenAIGatewayDownstreamProtocol(req)
	codexCompactionExpected := false
	if d.CompactionExpected != nil {
		codexCompactionExpected = d.CompactionExpected(req)
	}
	codexMetadata := parseCodexTurnMetadata(req.Header("x-codex-turn-metadata"))
	canUseCodexProfile := codexMetadata != nil &&
		(downstreamProtocol == DownstreamResponsesSSE || isOpenAICodexCompactPostRequest(req))
	explicitProfile := parseGatewayClientProfileHeader(req.Header(GatewayClientProfileHeader))
	clientProfile := ClientProfileGenericOpenAI
	if canUseCodexProfile || explicitProfile == ClientProfileCodex {
		clientProfile = ClientProfileCodex
	}
	clientProfileSource := ProfileSourceDefault
	if canUseCodexProfile {
		clientProfileSource = ProfileSourceCodexTurnMetadata
	} else if explicitProfile == ClientProfileCodex {
		clientProfileSource = ProfileSourceExplicitHeader
	}
	clientSource := d.resolveClientSource(req, identity, clientProfile, clientProfileSource, downstreamProtocol)
	var codexTurn *OpenAIGatewayCodexTurnContext
	if canUseCodexProfile && codexMetadata != nil {
		codexTurn = buildCodexTurnContext(d.Source, identity, *codexMetadata, downstreamProtocol, *clientSource)
	}
	clientSourceStateKey := d.deriveClientSourceStateKey(clientSource, clientProfile, identity.Endpoint, downstreamProtocol)
	clientSourceAvoidanceStateKey := clientSourceStateKey
	if codexTurn != nil && codexTurn.StateKey != "" {
		clientSourceAvoidanceStateKey = codexTurn.StateKey
	}
	strategy := OpenAIGatewayClientStrategyContext{
		ClientProfile:                     clientProfile,
		RequestClientCompatibility:        CompatibilityOpenAIStandard,
		DownstreamProtocol:                downstreamProtocol,
		UpstreamAdapter:                   UpstreamAdapterOpenAIMixed,
		CodexCompactionExpected:           codexTurn != nil && codexCompactionExpected,
		CodexTurn:                         codexTurn,
		ClientSource:                      clientSource,
		ClientProfileSource:               clientProfileSource,
		RetryCoordination:                 ResolveGatewayClientRetryCoordination(clientProfile, downstreamProtocol),
		AllowClientSourceAccountAvoidance: clientSourceAvoidanceStateKey != "",
		AllowCodexTurnAccountAvoidance:    codexTurn != nil && codexTurn.StateKey != "",
	}
	if codexTurn != nil {
		strategy.RequestClientCompatibility = CompatibilityCodexResponses
	}
	if clientSourceAvoidanceStateKey != "" {
		strategy.ClientSourceAvoidanceStateKey = clientSourceAvoidanceStateKey
	}
	return strategy
}

// ResolveAnthropicGatewayClientStrategy mirrors
// resolveAnthropicGatewayClientStrategy.
func (d *ClientStrategyDeps) ResolveAnthropicGatewayClientStrategy(req *gatewaypreauth.GatewayRequest, identity *ClientStrategyIdentity) OpenAIGatewayClientStrategyContext {
	downstreamProtocol := ResolveAnthropicGatewayDownstreamProtocol(req)
	explicitProfile := parseGatewayClientProfileHeader(req.Header(GatewayClientProfileHeader))
	supportedAnthropicShape := downstreamProtocol != DownstreamUnknownStream
	explicitClaudeCode := explicitProfile == ClientProfileClaudeCode && supportedAnthropicShape
	signatureClaudeCode := !explicitClaudeCode && supportedAnthropicShape && isClaudeCodeAnthropicRequestSignature(req)
	claudeCode := explicitClaudeCode || signatureClaudeCode
	clientProfile := ClientProfileGenericAnthropic
	if claudeCode {
		clientProfile = ClientProfileClaudeCode
	}
	clientProfileSource := ProfileSourceDefault
	if explicitClaudeCode {
		clientProfileSource = ProfileSourceExplicitHeader
	} else if signatureClaudeCode {
		clientProfileSource = ProfileSourceClaudeCodeRequestSignature
	}
	var clientSource *GatewayClientSourceIdentity
	clientSourceAvoidanceStateKey := ""
	if identity != nil {
		resolved := d.resolveClientSource(req, *identity, clientProfile, clientProfileSource, downstreamProtocol)
		clientSource = resolved
		clientSourceAvoidanceStateKey = d.deriveClientSourceStateKey(resolved, clientProfile, identity.Endpoint, downstreamProtocol)
	}
	strategy := OpenAIGatewayClientStrategyContext{
		ClientProfile:                     clientProfile,
		DownstreamProtocol:                downstreamProtocol,
		UpstreamAdapter:                   UpstreamAdapterAnthropicAPIKey,
		CodexCompactionExpected:           false,
		ClientSource:                      clientSource,
		ClientProfileSource:               clientProfileSource,
		RetryCoordination:                 ResolveGatewayClientRetryCoordination(clientProfile, downstreamProtocol),
		AllowClientSourceAccountAvoidance: clientSourceAvoidanceStateKey != "",
		AllowCodexTurnAccountAvoidance:    false,
	}
	if claudeCode {
		strategy.RequestClientCompatibility = CompatibilityClaudeCode
	} else {
		strategy.RequestClientCompatibility = CompatibilityAnthropicNative
	}
	if clientSourceAvoidanceStateKey != "" {
		strategy.ClientSourceAvoidanceStateKey = clientSourceAvoidanceStateKey
	}
	return strategy
}

// ResolveGeminiGatewayClientStrategy mirrors resolveGeminiGatewayClientStrategy.
func (d *ClientStrategyDeps) ResolveGeminiGatewayClientStrategy(req *gatewaypreauth.GatewayRequest, identity *ClientStrategyIdentity) OpenAIGatewayClientStrategyContext {
	downstreamProtocol := ResolveGeminiGatewayDownstreamProtocol(req)
	explicitProfile := parseGatewayClientProfileHeader(req.Header(GatewayClientProfileHeader))
	supportedGeminiShape := downstreamProtocol != DownstreamUnknownStream
	explicitGeminiCli := explicitProfile == ClientProfileGeminiCLI && supportedGeminiShape
	signatureGeminiCli := !explicitGeminiCli && supportedGeminiShape && isGeminiCliRequestSignature(req)
	geminiCli := explicitGeminiCli || signatureGeminiCli
	clientProfile := ClientProfileGenericGemini
	if geminiCli {
		clientProfile = ClientProfileGeminiCLI
	}
	clientProfileSource := ProfileSourceDefault
	if explicitGeminiCli {
		clientProfileSource = ProfileSourceExplicitHeader
	} else if signatureGeminiCli {
		clientProfileSource = ProfileSourceGeminiCLIRequestSignature
	}
	var clientSource *GatewayClientSourceIdentity
	clientSourceAvoidanceStateKey := ""
	if identity != nil {
		resolved := d.resolveClientSource(req, *identity, clientProfile, clientProfileSource, downstreamProtocol)
		clientSource = resolved
		clientSourceAvoidanceStateKey = d.deriveClientSourceStateKey(resolved, clientProfile, identity.Endpoint, downstreamProtocol)
	}
	return OpenAIGatewayClientStrategyContext{
		ClientProfile:                     clientProfile,
		RequestClientCompatibility:        CompatibilityOpenAIStandard,
		DownstreamProtocol:                downstreamProtocol,
		UpstreamAdapter:                   UpstreamAdapterGeminiAPIKey,
		CodexCompactionExpected:           false,
		ClientSource:                      clientSource,
		ClientSourceAvoidanceStateKey:     clientSourceAvoidanceStateKey,
		ClientProfileSource:               clientProfileSource,
		RetryCoordination:                 ResolveGatewayClientRetryCoordination(clientProfile, downstreamProtocol),
		AllowClientSourceAccountAvoidance: clientSourceAvoidanceStateKey != "",
		AllowCodexTurnAccountAvoidance:    false,
	}
}

func (d *ClientStrategyDeps) resolveClientSource(req *gatewaypreauth.GatewayRequest, identity ClientStrategyIdentity, clientProfile, clientProfileSource, downstreamProtocol string) *GatewayClientSourceIdentity {
	if d.Source == nil {
		return &GatewayClientSourceIdentity{Status: SourceStatusMissing}
	}
	source := d.Source.ResolveGatewayClientSourceIdentity(req, GatewayClientSourceIdentityInput{
		ClientProfile:       clientProfile,
		ClientProfileSource: clientProfileSource,
		DownstreamProtocol:  downstreamProtocol,
		SystemAccountID:     identity.SystemAccountID,
		APIKeyID:            identity.APIKeyID,
		ClientIP:            identity.ClientIP,
	})
	return &source
}

func (d *ClientStrategyDeps) deriveClientSourceStateKey(source *GatewayClientSourceIdentity, clientProfile, endpoint, downstreamProtocol string) string {
	if d.Source == nil || source == nil {
		return ""
	}
	return d.Source.DeriveGatewayClientSourceStateKey(*source, struct {
		ClientProfile      string
		Endpoint           string
		DownstreamProtocol string
	}{
		ClientProfile:      clientProfile,
		Endpoint:           endpoint,
		DownstreamProtocol: downstreamProtocol,
	})
}

// ResolveGatewayClientRetryCoordination mirrors
// resolveGatewayClientRetryCoordination.
func ResolveGatewayClientRetryCoordination(clientProfile, downstreamProtocol string) GatewayClientRetryCoordination {
	protocolEventSupported :=
		(clientProfile == ClientProfileCodex && downstreamProtocol == DownstreamResponsesSSE) ||
			(clientProfile == ClientProfileClaudeCode && downstreamProtocol == DownstreamMessagesSSE) ||
			(clientProfile == ClientProfileGeminiCLI && (downstreamProtocol == DownstreamGeminiStreamGenerateSSE || downstreamProtocol == DownstreamGeminiInteractionsSSE))
	if protocolEventSupported {
		return GatewayClientRetryCoordination{
			PreCommitFailureSignal: FailureSignalProtocolErrorEvent,
			CommittedFailureSignal: FailureSignalProtocolErrorEvent,
		}
	}
	return GatewayClientRetryCoordination{
		PreCommitFailureSignal: FailureSignalHTTPError,
		CommittedFailureSignal: FailureSignalDisconnect,
	}
}

// ResolveOpenAIGatewayDownstreamProtocol mirrors
// resolveOpenAIGatewayDownstreamProtocol.
func ResolveOpenAIGatewayDownstreamProtocol(req *gatewaypreauth.GatewayRequest) string {
	normalizedPath := normalizeOpenAIRequestPath(req.PathAndQuery())
	acceptsEventStream := requestAcceptsEventStream(req)
	streamRequested := gatewaypreauth.RequestStream(req) || acceptsEventStream
	if req.MethodUpper() == "POST" && normalizedPath == "/responses" && streamRequested {
		return DownstreamResponsesSSE
	}
	if req.MethodUpper() == "POST" && normalizedPath == "/chat/completions" && streamRequested {
		return DownstreamChatCompletionsSSE
	}
	if streamRequested || acceptsEventStream {
		return DownstreamUnknownStream
	}
	return DownstreamJSON
}

// ResolveAnthropicGatewayDownstreamProtocol mirrors
// resolveAnthropicGatewayDownstreamProtocol.
func ResolveAnthropicGatewayDownstreamProtocol(req *gatewaypreauth.GatewayRequest) string {
	normalizedPath := normalizeOpenAIRequestPath(req.PathAndQuery())
	acceptsEventStream := requestAcceptsEventStream(req)
	streamRequested := gatewaypreauth.RequestStream(req) || acceptsEventStream
	if req.MethodUpper() == "POST" && normalizedPath == "/messages" && streamRequested {
		return DownstreamMessagesSSE
	}
	if streamRequested || acceptsEventStream {
		return DownstreamUnknownStream
	}
	return DownstreamJSON
}

// ResolveGeminiGatewayDownstreamProtocol mirrors
// resolveGeminiGatewayDownstreamProtocol.
func ResolveGeminiGatewayDownstreamProtocol(req *gatewaypreauth.GatewayRequest) string {
	normalizedPath := normalizedGeminiRequestPath(req)
	acceptsEventStream := requestAcceptsEventStream(req)
	interactionStreamRequested := gatewaypreauth.RequestStream(req) || acceptsEventStream
	streamRequested := interactionStreamRequested || geminiAltSSEQuery(req)
	if req.MethodUpper() == "POST" && geminiModelsActionPattern.MatchString(normalizedPath) && strings.HasSuffix(normalizedPath, ":streamgeneratecontent") {
		return DownstreamGeminiStreamGenerateSSE
	}
	if req.MethodUpper() == "POST" && geminiModelsActionPattern.MatchString(normalizedPath) && strings.HasSuffix(normalizedPath, ":generatecontent") && streamRequested {
		return DownstreamGeminiStreamGenerateSSE
	}
	if req.MethodUpper() == "POST" && normalizedPath == "/interactions" {
		if interactionStreamRequested {
			return DownstreamGeminiInteractionsSSE
		}
		return DownstreamJSON
	}
	if req.MethodUpper() == "GET" && geminiInteractionGetPattern.MatchString(normalizedPath) {
		if interactionStreamRequested {
			return DownstreamGeminiInteractionsSSE
		}
		return DownstreamJSON
	}
	if streamRequested || acceptsEventStream {
		return DownstreamUnknownStream
	}
	return DownstreamJSON
}

var (
	geminiModelsActionPattern   = regexp.MustCompile(`^/models/[^/]+:(generatecontent|streamgeneratecontent)$`)
	geminiStreamGeneratePattern = regexp.MustCompile(`^/models/[^/]+:streamgeneratecontent$`)
	geminiGeneratePattern       = regexp.MustCompile(`^/models/[^/]+:generatecontent$`)
	geminiInteractionGetPattern = regexp.MustCompile(`^/interactions/[^/]+$`)
)

// OpenAIGatewayClientStrategyAuditMetadata mirrors
// openAIGatewayClientStrategyAuditMetadata.
func OpenAIGatewayClientStrategyAuditMetadata(strategy OpenAIGatewayClientStrategyContext) map[string]any {
	return map[string]any{
		"clientProfile":                        strategy.ClientProfile,
		"requestClientCompatibility":           strategy.RequestClientCompatibility,
		"clientProfileSource":                  strategy.ClientProfileSource,
		"downstreamProtocol":                   strategy.DownstreamProtocol,
		"upstreamAdapter":                      strategy.UpstreamAdapter,
		"codexCompactionExpected":              strategy.CodexCompactionExpected,
		"codexTurnIdPresent":                   strategy.CodexTurn != nil && strategy.CodexTurn.TurnID != "",
		"codexSessionIdPresent":                strategy.CodexTurn != nil && strategy.CodexTurn.SessionID != "",
		"codexThreadIdPresent":                 strategy.CodexTurn != nil && strategy.CodexTurn.ThreadID != "",
		"codexTurnStateKey":                    codexTurnStateKeyOrNil(strategy.CodexTurn),
		"clientSourceStatus":                   clientSourceFieldOrNil(strategy.ClientSource, sourceFieldValueStatus),
		"clientSourceKind":                     clientSourceFieldOrNil(strategy.ClientSource, sourceFieldValueKind),
		"clientSourceNamespace":                clientSourceFieldOrNil(strategy.ClientSource, sourceFieldValueNamespace),
		"clientSourceKeyPresent":               strategy.ClientSource != nil && strategy.ClientSource.SourceKey != "",
		"clientSourceAvoidanceStateKeyPresent": strategy.ClientSourceAvoidanceStateKey != "",
		"preCommitFailureSignal":               strategy.RetryCoordination.PreCommitFailureSignal,
		"committedFailureSignal":               strategy.RetryCoordination.CommittedFailureSignal,
		"allowClientSourceAccountAvoidance":    strategy.AllowClientSourceAccountAvoidance,
		"allowCodexTurnAccountAvoidance":       strategy.AllowCodexTurnAccountAvoidance,
	}
}

func codexTurnStateKeyOrNil(turn *OpenAIGatewayCodexTurnContext) any {
	if turn == nil || turn.StateKey == "" {
		return nil
	}
	return turn.StateKey
}

const (
	sourceFieldValueStatus    = 0
	sourceFieldValueKind      = 1
	sourceFieldValueNamespace = 2
)

func clientSourceFieldOrNil(source *GatewayClientSourceIdentity, field int) any {
	if source == nil {
		return nil
	}
	switch field {
	case sourceFieldValueStatus:
		return source.Status
	case sourceFieldValueKind:
		return source.Kind
	case sourceFieldValueNamespace:
		return source.SemanticNamespace
	}
	return nil
}

func parseGatewayClientProfileHeader(value string) string {
	normalized := jsStringValue(value)
	if normalized == "" {
		return ""
	}
	normalized = strings.ToLower(normalized)
	normalized = replaceRuns(normalized, func(r rune) bool { return r == '-' || unicode.IsSpace(r) }, '_')
	if normalized == ClientProfileCodex {
		return ClientProfileCodex
	}
	if normalized == ClientProfileClaudeCode {
		return ClientProfileClaudeCode
	}
	if normalized == ClientProfileGeminiCLI {
		return ClientProfileGeminiCLI
	}
	return ""
}

// replaceRuns mirrors replace(/[-\s]+/g, '_'): runs collapse to one underscore.
func replaceRuns(value string, match func(rune) bool, replacement rune) string {
	var builder strings.Builder
	prevReplaced := false
	for _, r := range value {
		if match(r) {
			if !prevReplaced {
				builder.WriteRune(replacement)
				prevReplaced = true
			}
			continue
		}
		builder.WriteRune(r)
		prevReplaced = false
	}
	return builder.String()
}

type codexTurnMetadata struct {
	turnID    string
	sessionID string
	threadID  string
}

func parseCodexTurnMetadata(value string) *codexTurnMetadata {
	rawValue := jsStringValue(value)
	if rawValue == "" {
		return nil
	}
	parsed := parseJSONObjectValue(rawValue)
	if parsed == nil {
		return nil
	}
	turnID := jsStringValue(parsed["turn_id"])
	if turnID == "" {
		return nil
	}
	return &codexTurnMetadata{
		turnID:    turnID,
		sessionID: jsStringValue(parsed["session_id"]),
		threadID:  jsStringValue(parsed["thread_id"]),
	}
}

func parseJSONObjectValue(value string) map[string]any {
	if value == "" {
		return nil
	}
	var parsed any
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		return nil
	}
	record, isObject := parsed.(map[string]any)
	if !isObject {
		return nil
	}
	return record
}

func isOpenAICodexCompactPostRequest(req *gatewaypreauth.GatewayRequest) bool {
	return req.MethodUpper() == "POST" && normalizeOpenAIRequestPath(req.PathAndQuery()) == "/responses/compact"
}

func normalizedGeminiRequestPath(req *gatewaypreauth.GatewayRequest) string {
	rawPath := req.PathAndQuery()
	if index := strings.IndexByte(rawPath, '?'); index >= 0 {
		rawPath = rawPath[:index]
	}
	if rawPath == "" {
		rawPath = "/"
	}
	if !strings.HasPrefix(rawPath, "/") {
		rawPath = "/" + rawPath
	}
	// Mirror path.replace(/^\/v1beta(?=\/|$)/i, '').
	lower := strings.ToLower(rawPath)
	if strings.HasPrefix(lower, "/v1beta") && (len(rawPath) == len("/v1beta") || lower[len("/v1beta")] == '/') {
		rawPath = rawPath[len("/v1beta"):]
	}
	path := strings.ToLower(rawPath)
	if path == "" {
		return "/"
	}
	return path
}

func requestAcceptsEventStream(req *gatewaypreauth.GatewayRequest) bool {
	accept := req.Header("Accept")
	return strings.Contains(strings.ToLower(accept), "text/event-stream")
}

// jsStringValue mirrors the strategy stringValue helper: trimmed non-empty
// strings only.
func jsStringValue(value any) string {
	text, isString := value.(string)
	if !isString {
		return ""
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	return trimmed
}

func isClaudeCodeAnthropicRequestSignature(req *gatewaypreauth.GatewayRequest) bool {
	if req.MethodUpper() != "POST" || normalizeOpenAIRequestPath(req.PathAndQuery()) != "/messages" {
		return false
	}
	signals := 0
	if hasClaudeCodeUserAgent(req) {
		signals++
	}
	if hasClaudeCodeBetaHeader(req) {
		signals++
	}
	if hasClaudeCodeSessionHeader(req) {
		signals++
	}
	if hasAnthropicBetaQuery(req) {
		signals++
	}
	return signals >= 2
}

func hasClaudeCodeUserAgent(req *gatewaypreauth.GatewayRequest) bool {
	userAgent := strings.ToLower(jsStringValue(req.Header("User-Agent")))
	if userAgent == "" {
		return false
	}
	return strings.HasPrefix(userAgent, "claude-cli/") || strings.Contains(userAgent, " claude-cli/")
}

func hasClaudeCodeBetaHeader(req *gatewaypreauth.GatewayRequest) bool {
	betaHeader := strings.ToLower(jsStringValue(req.Header("Anthropic-Beta")))
	if betaHeader == "" {
		return false
	}
	for _, item := range strings.Split(betaHeader, ",") {
		if strings.HasPrefix(strings.TrimSpace(item), "claude-code-") {
			return true
		}
	}
	return false
}

func hasClaudeCodeSessionHeader(req *gatewaypreauth.GatewayRequest) bool {
	return jsStringValue(req.Header("X-Claude-Code-Session-Id")) != "" ||
		jsStringValue(req.Header("X-Claude-Code-Agent-Id")) != ""
}

func hasAnthropicBetaQuery(req *gatewaypreauth.GatewayRequest) bool {
	originalURL := req.PathAndQuery()
	queryIndex := strings.IndexByte(originalURL, '?')
	if queryIndex < 0 {
		return false
	}
	values := parseURLSearchParams(originalURL[queryIndex+1:])
	value, ok := values["beta"]
	return ok && value == "true"
}

func isGeminiCliRequestSignature(req *gatewaypreauth.GatewayRequest) bool {
	if req.MethodUpper() != "POST" {
		return false
	}
	if !geminiModelsActionPattern.MatchString(normalizedGeminiRequestPath(req)) {
		return false
	}
	return hasGeminiCliUserAgent(req) && hasGeminiAuthSignal(req)
}

func hasGeminiCliUserAgent(req *gatewaypreauth.GatewayRequest) bool {
	userAgent := jsStringValue(req.Header("User-Agent"))
	if userAgent == "" {
		return false
	}
	return geminiCLIUserAgentPattern.MatchString(userAgent)
}

var (
	geminiCLIUserAgentPattern = regexp.MustCompile(`(?i)\bGeminiCLI(?:[-/]|$)`)
	geminiProxyClientPattern  = regexp.MustCompile(`(?i)proxy_client=geminicli\b`)
)

func hasGeminiAuthSignal(req *gatewaypreauth.GatewayRequest) bool {
	return jsStringValue(req.Header("X-Goog-Api-Key")) != "" ||
		jsStringValue(req.Header("X-Api-Key")) != "" ||
		jsStringValue(req.Header("Authorization")) != "" ||
		func() bool {
			_, ok := geminiQueryParam(req, "key")
			return ok
		}()
}

func geminiAltSSEQuery(req *gatewaypreauth.GatewayRequest) bool {
	value, ok := geminiQueryParam(req, "alt")
	return ok && strings.ToLower(value) == "sse"
}

func geminiQueryParam(req *gatewaypreauth.GatewayRequest, name string) (string, bool) {
	originalURL := req.PathAndQuery()
	queryIndex := strings.IndexByte(originalURL, '?')
	if queryIndex < 0 {
		return "", false
	}
	values := parseURLSearchParams(originalURL[queryIndex+1:])
	value, ok := values[name]
	if !ok {
		return "", false
	}
	normalized := jsStringValue(value)
	if normalized == "" {
		return "", false
	}
	return normalized, true
}

// parseURLSearchParams mirrors new URLSearchParams(text) single-value get
// semantics for the flags the strategy reads.
func parseURLSearchParams(query string) map[string]string {
	values := map[string]string{}
	for _, pair := range strings.Split(query, "&") {
		if pair == "" {
			continue
		}
		key, value := pair, ""
		if index := strings.IndexByte(pair, '='); index >= 0 {
			key, value = pair[:index], pair[index+1:]
		}
		key = urlDecodeComponent(key)
		value = urlDecodeComponent(value)
		if _, exists := values[key]; !exists {
			values[key] = value
		}
	}
	return values
}

// urlDecodeComponent mirrors decodeURIComponent's '+' handling difference:
// URLSearchParams treats '+' as space.
func urlDecodeComponent(value string) string {
	replaced := strings.ReplaceAll(value, "+", " ")
	if !strings.Contains(replaced, "%") {
		return replaced
	}
	var builder strings.Builder
	for i := 0; i < len(replaced); i++ {
		c := replaced[i]
		if c == '%' && i+2 < len(replaced) {
			high, ok1 := hexDigit(replaced[i+1])
			low, ok2 := hexDigit(replaced[i+2])
			if ok1 && ok2 {
				builder.WriteByte(high<<4 | low)
				i += 2
				continue
			}
		}
		builder.WriteByte(c)
	}
	return builder.String()
}

func hexDigit(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

func buildCodexTurnContext(source *SourceIdentityResolver, identity ClientStrategyIdentity, metadata codexTurnMetadata, downstreamProtocol string, clientSource GatewayClientSourceIdentity) *OpenAIGatewayCodexTurnContext {
	sourceStateKey := ""
	if source != nil {
		sourceStateKey = source.DeriveGatewayClientSourceStateKey(clientSource, struct {
			ClientProfile      string
			Endpoint           string
			DownstreamProtocol string
		}{
			ClientProfile:      ClientProfileCodex,
			Endpoint:           identity.Endpoint,
			DownstreamProtocol: downstreamProtocol,
		})
	}
	turn := &OpenAIGatewayCodexTurnContext{
		TurnID:    metadata.turnID,
		SessionID: metadata.sessionID,
		ThreadID:  metadata.threadID,
	}
	if sourceStateKey != "" && source != nil {
		turn.StateKey = source.DeriveGatewayClientSourceChildStateKey(sourceStateKey, "codex_turn", metadata.turnID)
		turn.SourceKind = clientSource.Kind
	}
	return turn
}
