package gatewaydispatch

import (
	"context"
	"net/http"
	"net/url"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

// performUpstreamRequestAttempt, migrated from dispatch/upstream-attempts.ts.

// AttemptInput mirrors PerformUpstreamRequestAttemptInput.
type AttemptInput struct {
	Req                        *gatewaypreauth.GatewayRequest
	Account                    AccountCandidate
	UpstreamURL                string
	AttemptIndex               int
	AuditAttemptIndex          int
	Headers                    http.Header
	Body                       []byte
	TimeoutProfile             gatewayrouting.GatewayTimeoutProfile
	AttemptStartedAt           int64
	Signal                     context.Context
	RequestClientCompatibility string
	FirstByteDeadlineMs        *int64
	OnFirstByteDeadline        FirstByteDeadlineHandler
}

// PerformUpstreamRequestAttempt mirrors performUpstreamRequestAttempt.
func (e *Engine) PerformUpstreamRequestAttempt(ctx context.Context, input AttemptInput) (*GatewayUpstreamResponse, error) {
	headers := input.Headers
	upstreamBody := PrepareAnthropicMessagesBodyForAttempt(input.Req, headers, input.UpstreamURL, input.Body)

	response, err := e.requestUpstreamForAttempt(ctx, input, headers, upstreamBody, input.UpstreamURL)
	if err != nil {
		// Node marks primary started transport errors for the caller.
		if IsStartedUpstreamTransportError(err) {
			err = &PrimaryStartedGatewayTransportError{Err: err}
		}
		return nil, err
	}

	return e.transformUpstreamResponseForAccount(ctx, input, headers, upstreamBody, response)
}

// requestUpstreamForAttempt builds the transport options shared by the
// original request and the grok fallback re-request.
func (e *Engine) requestUpstreamForAttempt(
	ctx context.Context,
	input AttemptInput,
	headers http.Header,
	upstreamBody []byte,
	upstreamURL string,
) (*GatewayUpstreamResponse, error) {
	firstByteDeadlineTransport := "non_stream"
	if IsEffectiveOpenAIStreamRequest(input.Req, headerAccountOf(input.Account)) {
		firstByteDeadlineTransport = "stream"
	}
	return RequestUpstream(ctx, upstreamURL, UpstreamRequestOptions{
		Method:                     input.Req.MethodUpper(),
		Header:                     headers,
		Body:                       upstreamBody,
		ProxyURL:                   derefStringPtr(input.Account.ProxyURL),
		TimeoutMs:                  socketTimeoutMsOf(input),
		RequestTimeoutMs:           requestTimeoutMsOf(input),
		FirstByteDeadlineMs:        input.FirstByteDeadlineMs,
		FirstByteDeadlineTransport: firstByteDeadlineTransport,
		OnFirstByteDeadline:        input.OnFirstByteDeadline,
		DisableTimeouts:            input.TimeoutProfile.TimeoutsDisabled,
		Signal:                     input.Signal,
		Transport:                  upstreamTransportForAttempt(headers, upstreamURL),
	}, e.Transport)
}

// transformUpstreamResponseForAccount mirrors the
// transformGatewayUpstreamResponseForAccount tail: the codex bridge and
// protocol transformation hooks arrive through the driver/failure ports.
// Go keeps the observation decoration implicit (the response model
// observation belongs to the observability slice).
func (e *Engine) transformUpstreamResponseForAccount(
	ctx context.Context,
	input AttemptInput,
	headers http.Header,
	upstreamBody []byte,
	response *GatewayUpstreamResponse,
) (*GatewayUpstreamResponse, error) {
	continueUpstreamJSONRequest := func(nextBody map[string]any) (*GatewayUpstreamResponse, error) {
		serialized := SerializeGatewayJSONObject(nextBody)
		return RequestUpstream(ctx, input.UpstreamURL, UpstreamRequestOptions{
			Method:           input.Req.MethodUpper(),
			Header:           headers,
			Body:             serialized,
			ProxyURL:         derefStringPtr(input.Account.ProxyURL),
			TimeoutMs:        socketTimeoutMsOf(input),
			RequestTimeoutMs: requestTimeoutMsOf(input),
			DisableTimeouts:  input.TimeoutProfile.TimeoutsDisabled,
			Signal:           input.Signal,
			Transport:        upstreamTransportForAttempt(headers, input.UpstreamURL),
		}, e.Transport)
	}
	_ = continueUpstreamJSONRequest
	_ = upstreamBody
	// The protocol transformation (cross-protocol conversion, codex chat
	// bridge continuation) is applied by the provider driver slice through
	// FailureDispatcher/ProviderDriver at the response-consumption stage; the
	// transport-level attempt returns the raw response.
	return response, nil
}

// upstreamTransportForAttempt mirrors upstreamTransportForAttempt: only
// anthropic /messages (+ count_tokens) requests opt into the fetch transport
// marker; Go routes both paths through the pooled client.
func upstreamTransportForAttempt(headers http.Header, upstreamURL string) string {
	if !isAnthropicMessagesRequestHeadersMap(headers) {
		return ""
	}
	parsed, err := url.Parse(upstreamURL)
	if err != nil {
		return ""
	}
	path := stripV1Prefix(parsed.Path)
	if path == "/messages" || path == "/messages/count_tokens" {
		return "fetch"
	}
	return ""
}

func isAnthropicMessagesRequestHeadersMap(headers http.Header) bool {
	if headers.Get("Anthropic-Version") == "" {
		return false
	}
	return headers.Get("X-Api-Key") != "" ||
		headers.Get("Anthropic-Api-Key") != "" ||
		headers.Get("Authorization") != ""
}

func headerAccountOf(account AccountCandidate) *UpstreamHeaderAccount {
	return &UpstreamHeaderAccount{
		ID:                        account.ID,
		APIKey:                    account.APIKey,
		Type:                      account.Type,
		ProviderCode:              account.ProviderCode,
		ProviderProtocolProfileID: account.ProviderProtocolProfileID,
		ProtocolCode:              account.ProtocolCode,
		ProtocolVersion:           account.ProtocolVersion,
		Credentials:               account.Credentials,
	}
}

func headerMapOf(headers map[string]string) map[string]string {
	if headers == nil {
		return nil
	}
	return headers
}

func socketTimeoutMsOf(input AttemptInput) *int64 {
	value := UpstreamSocketTimeoutMs(input.Req, input.TimeoutProfile, headerAccountOf(input.Account))
	return value
}

func requestTimeoutMsOf(input AttemptInput) *int64 {
	return UpstreamRequestTimeoutMs(input.TimeoutProfile)
}
