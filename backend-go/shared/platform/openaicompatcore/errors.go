package openaicompatcore

import (
	"encoding/json"
	"net/http"
)

// RequestError mirrors OpenAICompatibleFilesRequestError and
// OpenAICompatibleVectorStoresRequestError: a route-level error rendered as
// the OpenAI gateway error payload {error:{message,type,code?}}.
type RequestError struct {
	Message    string
	StatusCode int
	Type       string
	Code       string
}

func (e *RequestError) Error() string { return e.Message }

// NewRequestError mirrors the Node constructor defaults
// (400 / invalid_request_error / no code).
func NewRequestError(message string, statusCode int, errType, code string) *RequestError {
	return &RequestError{Message: message, StatusCode: statusCode, Type: errType, Code: code}
}

// BadRequest mirrors new OpenAICompatible*RequestError(msg) (400 defaults).
func BadRequest(message, code string) *RequestError {
	return NewRequestError(message, http.StatusBadRequest, "invalid_request_error", code)
}

// NotFound mirrors the explicit 404 constructions in both route modules.
func NotFound(message, code string) *RequestError {
	return NewRequestError(message, http.StatusNotFound, "invalid_request_error", code)
}

// WriteGatewayErrorPayload mirrors gatewayErrorPayload +
// res.status(status).json(...) in the route catch handler: byte order is
// message, type, code and code is omitted when empty (Node spread).
func WriteGatewayErrorPayload(w http.ResponseWriter, status int, message, errType, code string) {
	body := struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code,omitempty"`
		} `json:"error"`
	}{}
	body.Error.Message = message
	body.Error.Type = errType
	body.Error.Code = code
	encoded, err := json.Marshal(body)
	if err != nil {
		encoded = []byte(`{"error":{"message":"服务器内部错误","type":"api_error"}}`)
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
}

// Write renders the request error through the gateway error payload
// contract (the former in-package write helper).
func (e *RequestError) Write(w http.ResponseWriter) {
	WriteGatewayErrorPayload(w, e.StatusCode, e.Message, e.Type, e.Code)
}

// ErrUnhandled mirrors the Node fall-through `next(error)` path rendered by
// the process-level express error handler: 500 {"message":"服务器内部错误"}.
var ErrUnhandled error = &unhandledError{}

type unhandledError struct{}

func (e *unhandledError) Error() string { return "服务器内部错误" }

// WriteUnhandledError mirrors server.ts app.use(error) 500 contract.
func WriteUnhandledError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write([]byte(`{"message":"服务器内部错误"}`))
}

// BridgeRequestError mirrors GatewayRequestValidationError from the
// openai-anthropic bridge: message + code with explicit status/type options.
// Executors surface it so the bridge layer can render the same payloads.
type BridgeRequestError struct {
	Message    string
	Code       string
	StatusCode int
	Type       string
}

func (e *BridgeRequestError) Error() string { return e.Message }

// BridgeError mirrors the former in-package bridgeError constructor.
func BridgeError(message, code string, statusCode int, errType string) *BridgeRequestError {
	return &BridgeRequestError{Message: message, Code: code, StatusCode: statusCode, Type: errType}
}
