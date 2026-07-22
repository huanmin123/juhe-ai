package gatewayerrors

import (
	"errors"

	"juhe-ai/backend-go/internal/modules/gatewaycredentials"
	gatewayprotocol "juhe-ai/backend-go/internal/protocols/gateway"
)

type APIKeyState string

const (
	APIKeyStateActive   APIKeyState = "active"
	APIKeyStateInvalid  APIKeyState = "invalid"
	APIKeyStateDisabled APIKeyState = "disabled"
	APIKeyStateExpired  APIKeyState = "expired"
)

type Protocol = gatewayprotocol.ClientErrorProtocol

const (
	ProtocolOpenAI    = gatewayprotocol.ClientErrorOpenAI
	ProtocolAnthropic = gatewayprotocol.ClientErrorAnthropic
	ProtocolGemini    = gatewayprotocol.ClientErrorGemini
)

type ErrorClass string

const ErrorClassAuthentication ErrorClass = "authentication"

var (
	ErrCredentialMissing   = gatewaycredentials.ErrMissingCredential
	ErrCredentialMalformed = gatewaycredentials.ErrMalformedCredential
	ErrCredentialAmbiguous = gatewaycredentials.ErrAmbiguousCredential
	ErrAPIKeyInvalid       = errors.New("gateway API key invalid")
	ErrAPIKeyDisabled      = errors.New("gateway API key disabled")
	ErrAPIKeyExpired       = errors.New("gateway API key expired")
)

type APIKeyStateError struct {
	state APIKeyState
	cause error
}

func NewAPIKeyStateError(state APIKeyState, cause error) error {
	return &APIKeyStateError{state: state, cause: cause}
}

func (e *APIKeyStateError) Error() string {
	if e.cause != nil {
		return "gateway API key " + string(e.state) + ": " + e.cause.Error()
	}
	return "gateway API key " + string(e.state)
}

func (e *APIKeyStateError) Unwrap() error {
	return e.cause
}

func (e *APIKeyStateError) State() APIKeyState {
	return e.state
}

type PublicError struct {
	statusCode int
	message    string
	class      ErrorClass
	code       string
}

type RenderedError struct {
	StatusCode int
	Payload    any
}

type ErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

type OpenAIErrorPayload struct {
	Error ErrorDetail `json:"error"`
}

type AnthropicErrorPayload struct {
	Type  string      `json:"type"`
	Error ErrorDetail `json:"error"`
}

type GeminiErrorDetail struct {
	Message string `json:"message"`
	Status  string `json:"status"`
	Code    string `json:"code,omitempty"`
}

type GeminiErrorPayload struct {
	Error GeminiErrorDetail `json:"error"`
}

func Classify(err error) (PublicError, bool) {
	if err == nil {
		return PublicError{}, false
	}
	if errors.Is(err, ErrCredentialMissing) {
		return credentialMissingError(), true
	}
	if errors.Is(err, ErrCredentialMalformed) || errors.Is(err, ErrCredentialAmbiguous) {
		return invalidCredentialError(), true
	}

	var stateError *APIKeyStateError
	if errors.As(err, &stateError) {
		return ClassifyAPIKeyState(stateError.State())
	}
	switch {
	case errors.Is(err, ErrAPIKeyInvalid):
		return ClassifyAPIKeyState(APIKeyStateInvalid)
	case errors.Is(err, ErrAPIKeyDisabled):
		return ClassifyAPIKeyState(APIKeyStateDisabled)
	case errors.Is(err, ErrAPIKeyExpired):
		return ClassifyAPIKeyState(APIKeyStateExpired)
	}
	return PublicError{}, false
}

func ClassifyAPIKeyState(state APIKeyState) (PublicError, bool) {
	if state == APIKeyStateActive {
		return PublicError{}, false
	}
	return PublicError{
		statusCode: 401,
		message:    "API Key 无效或不可用",
		class:      ErrorClassAuthentication,
		code:       "invalid_api_key",
	}, true
}

func (e PublicError) Render(protocol Protocol) RenderedError {
	response := RenderedError{StatusCode: e.statusCode}
	switch protocol {
	case ProtocolAnthropic:
		response.Payload = AnthropicErrorPayload{Type: "error", Error: ErrorDetail{
			Message: e.message,
			Type:    anthropicType(e),
			Code:    e.code,
		}}
	case ProtocolGemini:
		response.Payload = GeminiErrorPayload{Error: GeminiErrorDetail{
			Message: e.message,
			Status:  geminiStatus(e),
			Code:    e.code,
		}}
	default:
		response.Payload = OpenAIErrorPayload{Error: ErrorDetail{
			Message: e.message,
			Type:    openAIType(e),
			Code:    e.code,
		}}
	}
	return response
}

func (e PublicError) StatusCode() int { return e.statusCode }

func (e PublicError) Message() string { return e.message }

func (e PublicError) Class() ErrorClass { return e.class }

func (e PublicError) Code() string { return e.code }

func credentialMissingError() PublicError {
	return PublicError{
		statusCode: 401,
		message:    "缺少访问令牌",
		class:      ErrorClassAuthentication,
		code:       "missing_api_key",
	}
}

func invalidCredentialError() PublicError {
	public, _ := ClassifyAPIKeyState(APIKeyStateInvalid)
	return public
}

func geminiStatus(e PublicError) string {
	if e.class == ErrorClassAuthentication {
		return "UNAUTHENTICATED"
	}
	return "INTERNAL"
}

func openAIType(e PublicError) string {
	if e.class == ErrorClassAuthentication {
		return "invalid_request_error"
	}
	return "api_error"
}

func anthropicType(e PublicError) string {
	if e.class == ErrorClassAuthentication {
		return "authentication_error"
	}
	return "api_error"
}
