package gatewaypreauth

// Port of request/validation-error.ts: the three gateway-local error types
// the known-error handler discriminates on.

// GatewayRequestValidationError mirrors GatewayRequestValidationError.
type GatewayRequestValidationError struct {
	Message       string
	Code          string
	StatusCode    int
	Type          string
	AccountScoped bool
}

// NewGatewayRequestValidationError mirrors the constructor defaults:
// code 'invalid_gateway_request', status 400, type 'invalid_request_error'.
func NewGatewayRequestValidationError(message string, options ...GatewayRequestValidationErrorOption) *GatewayRequestValidationError {
	err := &GatewayRequestValidationError{
		Message:    message,
		Code:       "invalid_gateway_request",
		StatusCode: 400,
		Type:       "invalid_request_error",
	}
	for _, option := range options {
		option(err)
	}
	return err
}

// GatewayRequestValidationErrorOption mirrors the constructor options bag.
type GatewayRequestValidationErrorOption func(*GatewayRequestValidationError)

// WithValidationErrorCode mirrors options.code.
func WithValidationErrorCode(code string) GatewayRequestValidationErrorOption {
	return func(err *GatewayRequestValidationError) { err.Code = code }
}

// WithValidationErrorStatusCode mirrors options.statusCode.
func WithValidationErrorStatusCode(statusCode int) GatewayRequestValidationErrorOption {
	return func(err *GatewayRequestValidationError) { err.StatusCode = statusCode }
}

// WithValidationErrorType mirrors options.type.
func WithValidationErrorType(errorType string) GatewayRequestValidationErrorOption {
	return func(err *GatewayRequestValidationError) { err.Type = errorType }
}

// WithValidationErrorAccountScoped mirrors options.accountScoped === true.
func WithValidationErrorAccountScoped() GatewayRequestValidationErrorOption {
	return func(err *GatewayRequestValidationError) { err.AccountScoped = true }
}

// Error implements error with the verbatim message.
func (e *GatewayRequestValidationError) Error() string { return e.Message }

// GatewayAgentGuidanceProtocol mirrors the protocol union.
type GatewayAgentGuidanceProtocol string

const (
	AgentGuidanceProtocolChatCompletions GatewayAgentGuidanceProtocol = "chat_completions"
	AgentGuidanceProtocolResponses       GatewayAgentGuidanceProtocol = "responses"
	AgentGuidanceProtocolMessages        GatewayAgentGuidanceProtocol = "messages"
	AgentGuidanceProtocolGemini          GatewayAgentGuidanceProtocol = "gemini"
)

// GatewayAgentGuidanceResponse mirrors GatewayAgentGuidanceResponse: a 200
// agent-guidance body delivered as a successful response. AccountScoped
// mirrors `accountScoped !== false`: nil keeps the Node default true.
type GatewayAgentGuidanceResponse struct {
	Message       string
	Code          string
	AccountScoped *bool
	Protocol      GatewayAgentGuidanceProtocol
	Stream        bool
	Model         string
}

// NewGatewayAgentGuidanceResponse mirrors the constructor.
func NewGatewayAgentGuidanceResponse(input GatewayAgentGuidanceResponse) *GatewayAgentGuidanceResponse {
	return &input
}

// IsAccountScoped mirrors the readonly accountScoped resolution.
func (e *GatewayAgentGuidanceResponse) IsAccountScoped() bool {
	return e.AccountScoped == nil || *e.AccountScoped
}

// StatusCode mirrors the readonly statusCode = 200.
func (e *GatewayAgentGuidanceResponse) StatusCode() int { return 200 }

// ErrorType mirrors the readonly type = 'agent_guidance'.
func (e *GatewayAgentGuidanceResponse) ErrorType() string { return "agent_guidance" }

// Error implements error with the verbatim message.
func (e *GatewayAgentGuidanceResponse) Error() string { return e.Message }

// GatewayLocalProtocolResponse mirrors GatewayLocalProtocolResponse: a raw
// local protocol body delivered with status + content type.
type GatewayLocalProtocolResponse struct {
	Message     string
	Code        string
	Body        string
	ContentType string
	// StatusCode mirrors `statusCode ?? 200`: zero keeps the default.
	StatusCode int
}

// NewGatewayLocalProtocolResponse mirrors the constructor: statusCode
// defaults to 200.
func NewGatewayLocalProtocolResponse(input GatewayLocalProtocolResponse) *GatewayLocalProtocolResponse {
	if input.StatusCode == 0 {
		input.StatusCode = 200
	}
	return &input
}

// ErrorType mirrors the readonly type = 'local_protocol_response'.
func (e *GatewayLocalProtocolResponse) ErrorType() string { return "local_protocol_response" }

// Error implements error with the verbatim message.
func (e *GatewayLocalProtocolResponse) Error() string { return e.Message }
