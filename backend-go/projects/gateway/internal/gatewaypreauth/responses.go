package gatewaypreauth

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// Port of the response/responses.ts subset the pre-auth and preflight chain
// consumes: the gateway error payload builders, protocol-specific client
// payloads, the JSON error senders and the stream failure events. Chinese
// messages pass through verbatim; system-style messages localize exactly like
// localizeSystemErrorMessage via the kernel.

// GatewayErrorPayload mirrors GatewayErrorPayload: {error: {...}} plus
// optional extra top-level keys.
type GatewayErrorPayload struct {
	// Extra carries additional top-level payload keys (Node spreads them
	// verbatim); nil is the common shape.
	Extra map[string]any `json:"-"`
	Error GatewayErrorBody
}

// GatewayErrorBody mirrors payload.error: message/type plus optional code and
// extra keys (client_ip / aggregate_ip_key on blacklist responses).
type GatewayErrorBody struct {
	Message string
	Type    string
	// Code mirrors the optional code field; empty omits it.
	Code string
	// Extra carries additional error keys, merged verbatim.
	Extra map[string]any `json:"-"`
}

// MarshalJSON encodes {error: {...}} with insertion-order-compatible field
// placement: message, type, code when set, then extras.
func (b GatewayErrorBody) marshal() (map[string]any, error) {
	object := map[string]any{
		"message": b.Message,
		"type":    b.Type,
	}
	if b.Code != "" {
		object["code"] = b.Code
	}
	for key, value := range b.Extra {
		object[key] = value
	}
	return object, nil
}

// MarshalJSON mirrors JSON.stringify of the payload.
func (p GatewayErrorPayload) MarshalJSON() ([]byte, error) {
	errorObject, err := p.Error.marshal()
	if err != nil {
		return nil, err
	}
	object := map[string]any{"error": errorObject}
	for key, value := range p.Extra {
		object[key] = value
	}
	return json.Marshal(object)
}

// GatewayErrorProtocol mirrors the client error protocol union.
type GatewayErrorProtocol string

const (
	GatewayErrorProtocolOpenAI    GatewayErrorProtocol = "openai"
	GatewayErrorProtocolAnthropic GatewayErrorProtocol = "anthropic"
	GatewayErrorProtocolGemini    GatewayErrorProtocol = "gemini"
)

// OpenAIGatewayDownstreamProtocol mirrors the consumed downstream protocol
// union member used by the stream failure event selection.
type OpenAIGatewayDownstreamProtocol string

// DownstreamProtocolResponsesSSE mirrors 'responses_sse'.
const DownstreamProtocolResponsesSSE OpenAIGatewayDownstreamProtocol = "responses_sse"

// GatewayErrorPayload mirrors gatewayErrorPayload(message, type, code?).
func GatewayErrorPayloadOf(message, errorType string, code ...string) GatewayErrorPayload {
	payload := GatewayErrorPayload{Error: GatewayErrorBody{Message: message, Type: errorType}}
	if len(code) > 0 && code[0] != "" {
		payload.Error.Code = code[0]
	}
	return payload
}

// LocalizedGatewayErrorPayload mirrors localizedGatewayErrorPayload: the
// message is either preserved (CJK already) or replaced by the status
// default; an unchanged message returns the payload untouched.
func LocalizedGatewayErrorPayload(payload GatewayErrorPayload, statusCode int) GatewayErrorPayload {
	message := kernel.LocalizeSystemErrorMessage(payload.Error.Message, statusCode)
	if message == payload.Error.Message {
		return payload
	}
	payload.Error.Message = message
	return payload
}

// GatewayErrorPayloadForProtocol mirrors gatewayErrorPayloadForProtocol.
func GatewayErrorPayloadForProtocol(payload GatewayErrorPayload, protocol GatewayErrorProtocol) any {
	if protocol == GatewayErrorProtocolAnthropic {
		errorObject := map[string]any{
			"type":    anthropicGatewayErrorType(payload),
			"message": payload.Error.Message,
		}
		if payload.Error.Code != "" {
			errorObject["code"] = payload.Error.Code
		}
		return map[string]any{
			"type":  "error",
			"error": errorObject,
		}
	}
	if protocol == GatewayErrorProtocolGemini {
		errorObject := map[string]any{
			"message": payload.Error.Message,
			"status":  geminiGatewayErrorStatus(payload),
		}
		if payload.Error.Code != "" {
			errorObject["code"] = payload.Error.Code
		}
		return map[string]any{"error": errorObject}
	}
	return payload
}

// anthropicGatewayErrorType mirrors the Node mapping.
func anthropicGatewayErrorType(payload GatewayErrorPayload) string {
	errorType := payload.Error.Type
	code := payload.Error.Code
	switch {
	case errorType == "rate_limit_exceeded":
		return "rate_limit_error"
	case errorType == "invalid_request_error":
		return "invalid_request_error"
	case errorType == "server_overloaded" || errorType == "service_unavailable" || code == "server_overloaded":
		return "overloaded_error"
	case errorType == "authentication_error":
		return "authentication_error"
	case errorType == "permission_error":
		return "permission_error"
	case errorType == "not_found_error":
		return "not_found_error"
	case errorType == "billing_error":
		return "billing_error"
	default:
		return "api_error"
	}
}

// geminiGatewayErrorStatus mirrors the Node mapping.
func geminiGatewayErrorStatus(payload GatewayErrorPayload) string {
	errorType := payload.Error.Type
	code := payload.Error.Code
	message := payload.Error.Message
	switch {
	case errorType == "rate_limit_exceeded":
		return "RESOURCE_EXHAUSTED"
	case (errorType == "invalid_request_error" || code == "invalid_request_error") &&
		containsAuthSignal(message):
		return "UNAUTHENTICATED"
	case errorType == "invalid_request_error":
		return "INVALID_ARGUMENT"
	case errorType == "authentication_error":
		return "UNAUTHENTICATED"
	case errorType == "permission_error" || errorType == "forbidden":
		return "PERMISSION_DENIED"
	case errorType == "not_found_error":
		return "NOT_FOUND"
	case errorType == "billing_error":
		return "RESOURCE_EXHAUSTED"
	case errorType == "server_overloaded" || errorType == "service_unavailable" || code == "server_overloaded":
		return "UNAVAILABLE"
	case strings.Contains(code, "timeout") || strings.Contains(code, "deadline"):
		return "DEADLINE_EXCEEDED"
	default:
		return "INTERNAL"
	}
}

// containsAuthSignal mirrors /令牌|api key|authentication|auth/i.
func containsAuthSignal(message string) bool {
	for _, needle := range []string{"令牌", "api key", "authentication", "auth"} {
		if containsFoldASCII(message, needle) {
			return true
		}
	}
	return false
}

// containsFoldASCII mirrors JS RegExp case-insensitive contains for ASCII
// needles plus verbatim CJK needles.
func containsFoldASCII(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	lowerHaystack := strings.ToLower(haystack)
	return strings.Contains(lowerHaystack, strings.ToLower(needle))
}

// GatewayResponseWriter is the gateway-facing response writer contract: the
// net/http writer plus the express flags the Node pipeline reads
// (res.headersSent / res.statusCode).
type GatewayResponseWriter interface {
	http.ResponseWriter
	// HeadersSent mirrors res.headersSent.
	HeadersSent() bool
	// StatusCode mirrors res.statusCode (200 before any explicit status).
	StatusCode() int
}

// TrackingWriter is the default GatewayResponseWriter: it records the status
// and header state so the pre-auth pipeline mirrors the Node res flags. It
// forwards kernel.UpstreamError marking and http.Flusher to the wrapped
// writer.
type TrackingWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	ended       bool
	destroyed   bool
}

// NewTrackingWriter wraps a response writer.
func NewTrackingWriter(w http.ResponseWriter) *TrackingWriter {
	return &TrackingWriter{ResponseWriter: w, status: http.StatusOK}
}

// WriteHeader records the status exactly once, like res.status(...).json(...).
func (w *TrackingWriter) WriteHeader(status int) {
	if !w.wroteHeader {
		w.status = status
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(status)
}

// Write marks headers sent (status 200 when unset) and delegates.
func (w *TrackingWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.status = http.StatusOK
		w.wroteHeader = true
	}
	return w.ResponseWriter.Write(body)
}

// End mirrors res.end(): later body writes are dropped.
func (w *TrackingWriter) End() { w.ended = true }

// SetDestroyed mirrors res.destroyed for the early-return check.
func (w *TrackingWriter) SetDestroyed() { w.destroyed = true }

// WritableEnded mirrors res.writableEnded.
func (w *TrackingWriter) WritableEnded() bool { return w.ended }

// HeadersSent mirrors res.headersSent.
func (w *TrackingWriter) HeadersSent() bool { return w.wroteHeader }

// StatusCode mirrors res.statusCode.
func (w *TrackingWriter) StatusCode() int { return w.status }

// MarkUpstreamError forwards the kernel upstream marker so error
// localization keeps verbatim messages across the tracking wrapper.
func (w *TrackingWriter) MarkUpstreamError() {
	if marker, ok := w.ResponseWriter.(kernel.UpstreamMarker); ok {
		marker.MarkUpstream()
	}
}

// Flush forwards to the wrapped flusher for SSE paths.
func (w *TrackingWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// SendGatewayJSONError mirrors sendGatewayJsonError: localize unless the
// upstream flag is preserved (then mark the kernel writer), convert to the
// protocol payload and write it as JSON.
func SendGatewayJSONError(w GatewayResponseWriter, statusCode int, payload GatewayErrorPayload, options SendGatewayErrorOptions) {
	var responsePayload GatewayErrorPayload
	if options.PreserveUpstreamErrorMessage {
		responsePayload = payload
		kernel.MarkUpstreamError(w)
	} else {
		responsePayload = LocalizedGatewayErrorPayload(payload, statusCode)
	}
	clientPayload := GatewayErrorPayloadForProtocol(responsePayload, options.Protocol)
	kernel.WriteJSON(w, statusCode, clientPayload)
}

// SendGatewayErrorOptions mirrors the sendGatewayJsonError options bag.
type SendGatewayErrorOptions struct {
	// Protocol defaults to 'openai' when empty.
	Protocol GatewayErrorProtocol
	// PreserveUpstreamErrorMessage mirrors the option of the same name.
	PreserveUpstreamErrorMessage bool
}

// SendGatewayErrorResponse mirrors sendGatewayErrorResponse: skip finished or
// destroyed responses, send JSON when headers are not sent, and otherwise
// emit the stream failure event for SSE content types before ending.
func SendGatewayErrorResponse(w GatewayResponseWriter, statusCode int, payload GatewayErrorPayload, options SendGatewayErrorResponseOptions) {
	if tracking, ok := w.(*TrackingWriter); ok && (tracking.WritableEnded() || tracking.destroyed) {
		return
	}
	var responsePayload GatewayErrorPayload
	if options.PreserveUpstreamErrorMessage {
		responsePayload = payload
	} else {
		responsePayload = LocalizedGatewayErrorPayload(payload, statusCode)
	}
	if !w.HeadersSent() {
		SendGatewayJSONError(w, statusCode, responsePayload, SendGatewayErrorOptions{
			Protocol:                     options.Protocol,
			PreserveUpstreamErrorMessage: options.PreserveUpstreamErrorMessage,
		})
		return
	}
	contentType := w.Header().Get("Content-Type")
	if IsOpenAIStreamContentType(contentType) {
		failureEvent := WriteGatewayStreamFailureEvent(responsePayload.Error.Message, responsePayload.Error.Code, options.Protocol, options.DownstreamProtocol)
		if failureEvent != nil {
			_, _ = w.Write(failureEvent)
		}
	}
	if tracking, ok := w.(*TrackingWriter); ok {
		tracking.End()
	}
}

// SendGatewayErrorResponseOptions mirrors the sendGatewayErrorResponse
// options bag.
type SendGatewayErrorResponseOptions struct {
	Protocol                     GatewayErrorProtocol
	DownstreamProtocol           OpenAIGatewayDownstreamProtocol
	PreserveUpstreamErrorMessage bool
}

// IsOpenAIStreamContentType mirrors isOpenAIStreamContentType.
func IsOpenAIStreamContentType(contentType string) bool {
	return responseMimeType(contentType) == "text/event-stream"
}

// responseMimeType mirrors responseMimeType: the lowercased part before ';'.
func responseMimeType(contentType string) string {
	return strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
}

// Gateway stream failure event constants mirror responses.ts.
const (
	GatewayStreamClientRetryErrorCode = "upstream_retryable_error"
	GatewayStreamClientRetryMessage   = "上游流式响应在输出前失败，请重试"
)

// WriteGatewayStreamFailureEvent mirrors writeGatewayStreamFailureEvent.
func WriteGatewayStreamFailureEvent(message string, code string, protocol GatewayErrorProtocol, downstreamProtocol OpenAIGatewayDownstreamProtocol) []byte {
	return BuildGatewayStreamFailureEventForProtocol(message, code, protocol, downstreamProtocol)
}

// BuildGatewayStreamFailureEventForProtocol mirrors the protocol dispatch.
func BuildGatewayStreamFailureEventForProtocol(message string, code string, protocol GatewayErrorProtocol, downstreamProtocol OpenAIGatewayDownstreamProtocol) []byte {
	if protocol == GatewayErrorProtocolAnthropic {
		return BuildAnthropicGatewayStreamFailureEvent(GatewayErrorPayloadOf(message, "service_unavailable", code))
	}
	if protocol == GatewayErrorProtocolGemini {
		return BuildGeminiGatewayStreamFailureEvent(GatewayErrorPayloadOf(message, "service_unavailable", code))
	}
	if downstreamProtocol == DownstreamProtocolResponsesSSE {
		return BuildGatewayStreamFailureEvent(message, code)
	}
	return nil
}

// GatewayStreamFailureCode mirrors gatewayStreamFailureCode: a constant
// independent of the message.
func GatewayStreamFailureCode(_ string) string { return "upstream_stream_interrupted" }

// BuildGatewayStreamFailureEvent mirrors buildGatewayStreamFailureEvent.
func BuildGatewayStreamFailureEvent(message string, code ...string) []byte {
	failureCode := ""
	if len(code) > 0 && code[0] != "" {
		failureCode = code[0]
	} else {
		failureCode = GatewayStreamFailureCode(message)
	}
	payloadBody := map[string]any{
		"type": "response.failed",
		"response": map[string]any{
			"status": "failed",
			"error": map[string]any{
				"code":    failureCode,
				"message": message,
			},
		},
	}
	encoded, err := json.Marshal(payloadBody)
	if err != nil {
		return nil
	}
	return []byte("event: response.failed\ndata: " + string(encoded) + "\n\n")
}

// BuildAnthropicGatewayStreamFailureEvent mirrors the anthropic event body.
func BuildAnthropicGatewayStreamFailureEvent(payload GatewayErrorPayload) []byte {
	errorPayload := GatewayErrorPayloadForProtocol(payload, GatewayErrorProtocolAnthropic)
	encoded, err := json.Marshal(errorPayload)
	if err != nil {
		return nil
	}
	return []byte("event: error\ndata: " + string(encoded) + "\n\n")
}

// BuildGeminiGatewayStreamFailureEvent mirrors the gemini event body.
func BuildGeminiGatewayStreamFailureEvent(payload GatewayErrorPayload) []byte {
	errorPayload := GatewayErrorPayloadForProtocol(payload, GatewayErrorProtocolGemini)
	encoded, err := json.Marshal(errorPayload)
	if err != nil {
		return nil
	}
	return []byte("event: error\ndata: " + string(encoded) + "\n\n")
}
