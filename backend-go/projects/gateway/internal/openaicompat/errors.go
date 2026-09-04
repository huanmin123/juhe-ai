package openaicompat

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

// newRequestError mirrors the Node constructor defaults
// (400 / invalid_request_error / no code).
func newRequestError(message string, statusCode int, errType, code string) *RequestError {
	return &RequestError{Message: message, StatusCode: statusCode, Type: errType, Code: code}
}

// badRequest mirrors new OpenAICompatible*RequestError(msg) (400 defaults).
func badRequest(message, code string) *RequestError {
	return newRequestError(message, http.StatusBadRequest, "invalid_request_error", code)
}

// notFound mirrors the explicit 404 constructions in both route modules.
func notFound(message, code string) *RequestError {
	return newRequestError(message, http.StatusNotFound, "invalid_request_error", code)
}

// writeGatewayErrorPayload mirrors gatewayErrorPayload +
// res.status(status).json(...) in the route catch handler: byte order is
// message, type, code and code is omitted when empty (Node spread).
func writeGatewayErrorPayload(w http.ResponseWriter, status int, message, errType, code string) {
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

func (e *RequestError) write(w http.ResponseWriter) {
	writeGatewayErrorPayload(w, e.StatusCode, e.Message, e.Type, e.Code)
}

// errUnhandled mirrors the Node fall-through `next(error)` path rendered by
// the process-level express error handler: 500 {"message":"服务器内部错误"}.
var errUnhandled = &unhandledError{}

type unhandledError struct{}

func (e *unhandledError) Error() string { return "服务器内部错误" }

// writeUnhandledError mirrors server.ts app.use(error) 500 contract.
func writeUnhandledError(w http.ResponseWriter) {
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

func bridgeError(message, code string, statusCode int, errType string) *BridgeRequestError {
	return &BridgeRequestError{Message: message, Code: code, StatusCode: statusCode, Type: errType}
}
