// Package gatewayrequestprep converts bounded, already-parsed request facts
// into an immutable gateway request plan. It has no HTTP, storage, upstream,
// or response-writer ownership.
package gatewayrequestprep

import (
	"net/url"
	"strings"

	"juhe-ai/backend-go/internal/modules/gatewaystreamrelay"
	protocolgateway "juhe-ai/backend-go/internal/protocols/gateway"
)

type Protocol = protocolgateway.ProtocolCode

const (
	ProtocolUnknown   Protocol = ""
	ProtocolOpenAI    Protocol = protocolgateway.ProtocolOpenAI
	ProtocolAnthropic Protocol = protocolgateway.ProtocolAnthropic
	ProtocolGemini    Protocol = protocolgateway.ProtocolGemini
)

type DownstreamProtocol = protocolgateway.DownstreamProtocol

const (
	DownstreamJSON                           = protocolgateway.DownstreamJSON
	DownstreamUnknownStream                  = protocolgateway.DownstreamUnknownStream
	DownstreamResponsesSSE                   = protocolgateway.DownstreamResponsesSSE
	DownstreamChatCompletionsSSE             = protocolgateway.DownstreamChatCompletionsSSE
	DownstreamMessagesSSE                    = protocolgateway.DownstreamMessagesSSE
	DownstreamGeminiStreamGenerateContentSSE = protocolgateway.DownstreamGeminiGenerateContentSSE
	DownstreamGeminiInteractionsSSE          = protocolgateway.DownstreamGeminiInteractionsSSE
)

type ClientProfile = protocolgateway.ClientProfile

const (
	ClientProfileUnknown          ClientProfile = ""
	ClientProfileCodex                          = protocolgateway.ClientProfileCodex
	ClientProfileGenericOpenAI                  = protocolgateway.ClientProfileGenericOpenAI
	ClientProfileClaudeCode                     = protocolgateway.ClientProfileClaudeCode
	ClientProfileGenericAnthropic               = protocolgateway.ClientProfileGenericAnthropic
	ClientProfileGeminiCLI                      = protocolgateway.ClientProfileGeminiCLI
	ClientProfileGenericGemini                  = protocolgateway.ClientProfileGenericGemini
)

type ClientProfileSource = protocolgateway.ClientProfileSource

const (
	ClientProfileSourceDefault         = protocolgateway.ClientProfileSourceDefault
	ClientProfileSourceExplicitHeader  = protocolgateway.ClientProfileSourceExplicitHeader
	ClientProfileSourceCodexTurn       = protocolgateway.ClientProfileSourceCodexTurnMetadata
	ClientProfileSourceClaudeSignature = protocolgateway.ClientProfileSourceClaudeSignature
	ClientProfileSourceGeminiSignature = protocolgateway.ClientProfileSourceGeminiSignature
)

type RequestClientCompatibility = protocolgateway.ClientCompatibility

const (
	RequestClientCompatibilityOpenAI    = protocolgateway.CompatibilityOpenAIStandard
	RequestClientCompatibilityCodex     = protocolgateway.CompatibilityCodexResponses
	RequestClientCompatibilityClaude    = protocolgateway.CompatibilityClaudeCode
	RequestClientCompatibilityAnthropic = protocolgateway.CompatibilityAnthropicNative
)

type UpstreamAdapter string

const (
	UpstreamAdapterUnknown   UpstreamAdapter = "unknown"
	UpstreamAdapterOpenAI    UpstreamAdapter = "openai_mixed"
	UpstreamAdapterAnthropic UpstreamAdapter = "anthropic_api_key"
	UpstreamAdapterGemini    UpstreamAdapter = "gemini_api_key"
)

type PreCommitFailureSignal string

const (
	PreCommitFailureSignalHTTPError     PreCommitFailureSignal = "http_error"
	PreCommitFailureSignalProtocolEvent PreCommitFailureSignal = "protocol_error_event"
)

// Input contains only request-shape facts plus a caller-owned protocol
// fallback. Body parsing, credential extraction, authentication, quota checks,
// and account routing remain outside this package. Credential presence is a
// boolean, never a secret value.
type Input struct {
	Method           string
	Path             string
	FallbackProtocol Protocol

	StreamRequested       bool
	AcceptsEventStream    bool
	GeminiAltSSE          bool
	ExplicitClientProfile string
	UserAgent             string

	HasAnthropicBeta      bool
	HasAnthropicBetaQuery bool
	HasClaudeSessionID    bool
	HasClaudeAgentID      bool
	HasGeminiCredential   bool
	// CodexTurnMetadataValid means a prior bounded parser confirmed a non-empty
	// snake_case turn_id. The raw metadata and its state/hash stay out of this
	// planning seam.
	CodexTurnMetadataValid bool
}

// Result holds a plan produced by Prepare. Its fields are private so callers
// cannot synthesize protocol-event permission without the canonical resolver.
type Result struct {
	protocol              Protocol
	downstream            DownstreamProtocol
	clientProfile         ClientProfile
	clientProfileSource   ClientProfileSource
	compatibility         RequestClientCompatibility
	upstreamAdapter       UpstreamAdapter
	preCommitSignal       PreCommitFailureSignal
	committedSignal       gatewaystreamrelay.CommittedFailureSignal
	controlledFailureType gatewaystreamrelay.ControlledFailureProtocol
}

func (r Result) Protocol() Protocol                                     { return r.protocol }
func (r Result) DownstreamProtocol() DownstreamProtocol                 { return r.downstream }
func (r Result) ClientProfile() ClientProfile                           { return r.clientProfile }
func (r Result) ClientProfileSource() ClientProfileSource               { return r.clientProfileSource }
func (r Result) RequestClientCompatibility() RequestClientCompatibility { return r.compatibility }
func (r Result) UpstreamAdapter() UpstreamAdapter                       { return r.upstreamAdapter }
func (r Result) PreCommitFailureSignal() PreCommitFailureSignal         { return r.preCommitSignal }
func (r Result) CommittedFailureSignal() gatewaystreamrelay.CommittedFailureSignal {
	return r.committedSignal
}

// ControlledFailureProtocol is present only for the exact profile/protocol
// pairs which can safely receive a post-commit terminal event.
func (r Result) ControlledFailureProtocol() (gatewaystreamrelay.ControlledFailureProtocol, bool) {
	return r.controlledFailureType, r.controlledFailureType != ""
}

// Prepare delegates protocol/path/SSE/client signature parsing to the shared
// protocolgateway registry. OpenAI-shaped native paths remain higher priority
// than a fallback profile, matching the coexistence contract in one place.
func Prepare(input Input) Result {
	shape := requestShape(input)
	definition, found := protocolgateway.ResolveDefinition(shape, fallbackProfile(input.FallbackProtocol))
	if !found {
		return Result{
			protocol: ProtocolUnknown, downstream: protocolgateway.ResolveDownstreamProtocol(ProtocolUnknown, shape),
			clientProfile: ClientProfileUnknown, clientProfileSource: ClientProfileSourceDefault,
			compatibility: RequestClientCompatibilityOpenAI, upstreamAdapter: UpstreamAdapterUnknown,
			preCommitSignal: PreCommitFailureSignalHTTPError, committedSignal: gatewaystreamrelay.CommittedFailureSignalDisconnect,
		}
	}

	result := Result{protocol: definition.Code, downstream: protocolgateway.ResolveDownstreamProtocol(definition.Code, shape), upstreamAdapter: upstreamAdapter(definition.Code)}
	profile, _ := protocolgateway.ResolveClientProfile(definition.Code, shape)
	if definition.Code == ProtocolOpenAI && profile.Profile != ClientProfileCodex && explicitCodex(input.ExplicitClientProfile) {
		profile = protocolgateway.ClientProfileResolution{
			Profile: ClientProfileCodex, Source: ClientProfileSourceExplicitHeader, Compatibility: RequestClientCompatibilityOpenAI,
		}
	}
	if definition.Code == ProtocolGemini && profile.Profile != ClientProfileGeminiCLI && explicitGeminiCLI(input.ExplicitClientProfile) && result.downstream != DownstreamUnknownStream {
		profile = protocolgateway.ClientProfileResolution{
			Profile: ClientProfileGeminiCLI, Source: ClientProfileSourceExplicitHeader, Compatibility: RequestClientCompatibilityOpenAI,
		}
	}
	result.clientProfile = profile.Profile
	result.clientProfileSource = profile.Source
	result.compatibility = profile.Compatibility
	result.resolveRetryCoordination()
	return result
}

func requestShape(input Input) protocolgateway.RequestShape {
	headers := map[string]string{
		"X-Juhe-Client-Profile": input.ExplicitClientProfile,
		"User-Agent":            input.UserAgent,
	}
	if input.AcceptsEventStream {
		headers["Accept"] = "text/event-stream"
	}
	if input.HasAnthropicBeta {
		headers["Anthropic-Beta"] = "claude-code-present"
	}
	if input.HasClaudeSessionID {
		headers["X-Claude-Code-Session-Id"] = "present"
	}
	if input.HasClaudeAgentID {
		headers["X-Claude-Code-Agent-Id"] = "present"
	}
	if input.HasGeminiCredential {
		headers["X-Goog-API-Key"] = "present"
	}
	if input.CodexTurnMetadataValid {
		headers["X-Codex-Turn-Metadata"] = `{"turn_id":"validated"}`
	}
	return protocolgateway.RequestShape{Method: input.Method, Path: sanitizedPath(input), Headers: headers, Stream: input.StreamRequested}
}

func sanitizedPath(input Input) string {
	path, _, _ := strings.Cut(strings.TrimSpace(input.Path), "?")
	if path == "" {
		path = "/"
	}
	query := url.Values{}
	if input.GeminiAltSSE {
		query.Set("alt", "sse")
	}
	if input.HasAnthropicBetaQuery {
		query.Set("beta", "true")
	}
	if encoded := query.Encode(); encoded != "" {
		return path + "?" + encoded
	}
	return path
}

func fallbackProfile(protocol Protocol) *protocolgateway.Profile {
	switch protocol {
	case ProtocolOpenAI, ProtocolAnthropic, ProtocolGemini:
		return &protocolgateway.Profile{Code: string(protocol), Version: map[Protocol]string{ProtocolOpenAI: "v1", ProtocolAnthropic: "v1", ProtocolGemini: "v1beta"}[protocol]}
	default:
		return nil
	}
}

func upstreamAdapter(protocol Protocol) UpstreamAdapter {
	switch protocol {
	case ProtocolOpenAI:
		return UpstreamAdapterOpenAI
	case ProtocolAnthropic:
		return UpstreamAdapterAnthropic
	case ProtocolGemini:
		return UpstreamAdapterGemini
	default:
		return UpstreamAdapterUnknown
	}
}

func (r *Result) resolveRetryCoordination() {
	r.preCommitSignal = PreCommitFailureSignalHTTPError
	r.committedSignal = gatewaystreamrelay.CommittedFailureSignalDisconnect
	switch {
	case r.clientProfile == ClientProfileCodex && r.downstream == DownstreamResponsesSSE:
		r.preCommitSignal = PreCommitFailureSignalProtocolEvent
		r.committedSignal = gatewaystreamrelay.CommittedFailureSignalProtocolEvent
		r.controlledFailureType = gatewaystreamrelay.ControlledFailureProtocolResponses
	case r.clientProfile == ClientProfileClaudeCode && r.downstream == DownstreamMessagesSSE:
		r.preCommitSignal = PreCommitFailureSignalProtocolEvent
		r.committedSignal = gatewaystreamrelay.CommittedFailureSignalProtocolEvent
		r.controlledFailureType = gatewaystreamrelay.ControlledFailureProtocolAnthropic
	case r.clientProfile == ClientProfileGeminiCLI && (r.downstream == DownstreamGeminiStreamGenerateContentSSE || r.downstream == DownstreamGeminiInteractionsSSE):
		r.preCommitSignal = PreCommitFailureSignalProtocolEvent
		r.committedSignal = gatewaystreamrelay.CommittedFailureSignalProtocolEvent
		r.controlledFailureType = gatewaystreamrelay.ControlledFailureProtocolGemini
	}
}

func explicitCodex(value string) bool { return normalizedExplicitProfile(value) == ClientProfileCodex }
func explicitGeminiCLI(value string) bool {
	return normalizedExplicitProfile(value) == ClientProfileGeminiCLI
}

func normalizedExplicitProfile(value string) ClientProfile {
	value = strings.NewReplacer("-", "_", " ", "_").Replace(strings.ToLower(strings.TrimSpace(value)))
	switch value {
	case string(ClientProfileCodex):
		return ClientProfileCodex
	case string(ClientProfileGeminiCLI):
		return ClientProfileGeminiCLI
	default:
		return ClientProfileUnknown
	}
}
