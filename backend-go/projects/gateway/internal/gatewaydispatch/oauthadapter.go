package gatewaydispatch

import (
	"encoding/json"
	"net/http"
	"regexp"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// Codex request adapter, migrated from adapters/gpt-codex/oauth-adapter.ts.
// The Node worker_threads large-body path becomes plain in-process parsing
// (Go has no worker pool dependency); the normalization cache mirrors the
// Node per-request cache key.

// OpenAIOAuthCodexRequestParts mirrors OpenAIOAuthCodexRequestParts.
type OpenAIOAuthCodexRequestParts struct {
	Headers http.Header
	Body    []byte
}

// OpenAIOAuthCodexRequestOptions mirrors the options bag.
type OpenAIOAuthCodexRequestOptions struct {
	ModelOverride                    string
	RequestOverrideModelCapabilities *GptRequestOverrideModelCapabilities
	SanitizeCodexHistory             bool
}

// var openAIResponsesCompactPattern mirrors the /\/responses\/compact$/ path check.
var openAIResponsesCompactPattern = regexp.MustCompile(`(?:^|/)responses/compact$`)

// IsOpenAIOAuthCodexCompactRequest mirrors isOpenAIOAuthCodexCompactRequest:
// the /responses/compact endpoint (with the /v1 prefix stripped).
func IsOpenAIOAuthCodexCompactRequest(req *gatewaypreauth.GatewayRequest) bool {
	path := req.Path()
	if path == "" {
		path = "/"
	}
	return stripV1Prefix(path) == "/responses/compact"
}

// BuildOpenAIOAuthCodexRequestParts mirrors buildOpenAIOAuthCodexRequestParts.
func BuildOpenAIOAuthCodexRequestParts(
	req *gatewaypreauth.GatewayRequest,
	inputHeaders http.Header,
	account OpenAIOAuthCodexAccount,
	identity OpenAIOAuthCodexIdentity,
	options OpenAIOAuthCodexRequestOptions,
) (OpenAIOAuthCodexRequestParts, error) {
	compact := IsOpenAIOAuthCodexCompactRequest(req)
	normalizedBody, err := normalizeOpenAIOAuthCodexBody(req, inputHeaders, account, identity, compact, options)
	if err != nil {
		return OpenAIOAuthCodexRequestParts{}, err
	}
	headers, err := buildOpenAIOAuthCodexHeaders(inputHeaders, account, codexHeaderInput{
		compact: compact,
		stream:  normalizedBody.Stream,
		session: normalizedBody.Session,
		model:   normalizedBody.Model,
	})
	if err != nil {
		return OpenAIOAuthCodexRequestParts{}, err
	}
	var body []byte
	if normalizedBody.CodexHistorySanitized && normalizedBody.BodyBytes != nil {
		body = MarkGatewayCodexHistorySanitized(normalizedBody.BodyBytes)
	} else if normalizedBody.Body != "" {
		body = []byte(normalizedBody.Body)
	}
	return OpenAIOAuthCodexRequestParts{Headers: headers, Body: body}, nil
}
func normalizeOpenAIOAuthCodexBody(
	req *gatewaypreauth.GatewayRequest,
	inputHeaders http.Header,
	account OpenAIOAuthCodexAccount,
	identity OpenAIOAuthCodexIdentity,
	compact bool,
	options OpenAIOAuthCodexRequestOptions,
) (NormalizedCodexBody, error) {
	method := req.MethodUpper()
	if method == "GET" || method == "HEAD" {
		return NormalizedCodexBody{Stream: false}, nil
	}

	body, err := parseOpenAIOAuthCodexJsonObjectBody(req)
	if err != nil {
		return NormalizedCodexBody{}, err
	}
	return NormalizeOpenAIOAuthCodexParsedBody(body, OpenAIOAuthCodexNormalizeInput{
		InputHeaders:                     inputHeaders,
		Account:                          account,
		Identity:                         identity,
		Compact:                          compact,
		SanitizeCodexHistory:             options.SanitizeCodexHistory,
		ModelOverride:                    options.ModelOverride,
		RequestOverrideModelCapabilities: options.RequestOverrideModelCapabilities,
	})
}

func parseOpenAIOAuthCodexJsonObjectBody(req *gatewaypreauth.GatewayRequest) (any, error) {
	if req.Body != nil {
		if parsed, ok := req.Body.Body.(map[string]any); ok {
			return parsed, nil
		}
		if req.Body.Body != nil {
			return req.Body.Body, nil
		}
	}
	if state := req.BodyState(); state != nil && state.JSONParseStatus == gatewaybody.JSONParseStatusInvalidJSON {
		return nil, NewOpenAIOAuthCodexAdapterError("请求体必须是有效的 JSON 对象")
	}
	rawBody := rawBodyOf(req)
	if len(rawBody) > 0 {
		var parsed any
		if err := json.Unmarshal(rawBody, &parsed); err != nil {
			return nil, NewOpenAIOAuthCodexAdapterError("请求体必须是有效的 JSON 对象")
		}
		if object, ok := parsed.(map[string]any); ok {
			return object, nil
		}
		return parsed, nil
	}
	return map[string]any{}, nil
}

func rawBodyOf(req *gatewaypreauth.GatewayRequest) []byte {
	if req == nil || req.Body == nil {
		return nil
	}
	return req.Body.RawBody
}
func buildOpenAIOAuthCodexHeaders(
	inputHeaders http.Header,
	account OpenAIOAuthCodexAccount,
	input codexHeaderInput,
) (http.Header, error) {
	validatedAttestation := http.Header{}
	if err := copyOpenAIOAuthCodexAttestationHeader(validatedAttestation, inputHeaders); err != nil {
		return nil, err
	}
	headers := CopyOfficialOAuthClientRequestHeaders(inputHeaders, OAuthHeaderProfileOpenAICodex)
	if attestation := validatedAttestation.Get("X-Oai-Attestation"); attestation != "" {
		headers.Set("X-Oai-Attestation", attestation)
	}
	nativeCodexClient := IsOpenAICodexClientHeaders(headers)
	NormalizeOpenAICodexClientHeaders(headers, input.model)
	headers.Set("Authorization", "Bearer "+account.APIKey)
	headers.Set("Content-Type", "application/json")
	if headers.Get("Openai-Beta") == "" {
		headers.Set("Openai-Beta", "responses=experimental")
	}
	if !nativeCodexClient {
		accept := "text/event-stream"
		if input.compact || !input.stream {
			accept = "application/json"
		}
		headers.Set("Accept", accept)
	}

	accountID := stringCredential(account.Credentials, "account_id")
	if accountID == "" {
		accountID = stringCredential(account.Credentials, "chatgpt_account_id")
	}
	if accountID != "" {
		headers.Set("Chatgpt-Account-Id", accountID)
	}
	if input.session.SessionID != "" {
		headers.Set("Session-Id", input.session.SessionID)
	}
	if input.session.ConversationID != "" {
		headers.Set("Thread-Id", input.session.ConversationID)
		if headers.Get("X-Client-Request-Id") == "" {
			headers.Set("X-Client-Request-Id", input.session.ConversationID)
		}
	}

	return headers, nil
}

type codexHeaderInput struct {
	compact bool
	stream  bool
	session OpenAIOAuthCodexSessionResolution
	model   string
}

var attestationForbiddenPattern = regexp.MustCompile(`[\r\n\0]`)

func copyOpenAIOAuthCodexAttestationHeader(output http.Header, inputHeaders http.Header) error {
	value := headerValueOf(inputHeaders, "x-oai-attestation")
	if value == "" {
		return nil
	}
	if len(value) > 32*1024 || attestationForbiddenPattern.MatchString(value) {
		return NewOpenAIOAuthCodexAdapterError(
			"Codex 设备证明 header 无效",
			WithCodexAdapterCode("invalid_openai_oauth_codex_attestation"),
		)
	}
	output.Set("X-Oai-Attestation", value)
	return nil
}

// gatewaypreauthJSONParseStatusInvalid keeps the body-state status comparison
// local (the concrete status enum lives in gatewaybody).
func gatewaypreauthJSONParseStatusInvalid() string {
	return "invalid_json"
}
