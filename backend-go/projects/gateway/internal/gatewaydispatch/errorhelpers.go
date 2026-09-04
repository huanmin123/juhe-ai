package gatewaydispatch

import (
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// buildDiagnosticUpstreamError, migrated from upstream/error-helpers.ts: it
// turns the last attempt into the diagnostic error envelope with upstream
// message preservation semantics.

// DiagnosticUpstreamError mirrors the returned union.
type DiagnosticUpstreamError struct {
	StatusCode             int
	Payload                gatewaypreauth.GatewayErrorPayload
	ErrorMessage           string
	PreserveUpstreamMessage bool
}

// BuildDiagnosticUpstreamError mirrors buildDiagnosticUpstreamError. The
// protocol error payload parser (Node
// parseGatewayProtocolErrorPayloadFromJsonValue) is injected because the
// protocol registry belongs to another slice; nil keeps the generic {error}
// object branch.
func BuildDiagnosticUpstreamError(
	lastAttempt *UpstreamAttempt,
	fallbackMessage string,
	parseProtocolError func(attempt UpstreamAttempt, payload map[string]any) ProtocolErrorPayload,
) *DiagnosticUpstreamError {
	if lastAttempt == nil {
		return nil
	}

	transportFailure := lastAttempt.TransportFailureKind
	statusCode := 503
	switch {
	case isHTTPStatusCode(lastAttempt.Status):
		statusCode = lastAttempt.Status
	case transportFailure == TransportFailureKindTimeout:
		statusCode = 504
	case transportFailure != "":
		statusCode = 502
	}
	bodyText := strings.TrimSpace(lastAttempt.ResponseBodyText)
	responseHeaders := headersFromObject(lastAttempt.ResponseHeaders)
	parsedPayload := lastAttempt.ParsedResponseBody
	if parsedPayload == nil && bodyText != "" {
		parsedPayload = parseGatewayNonStreamJSONBody(bodyText, responseHeaders)
	}
	var parsedError ProtocolErrorPayload
	if parsedPayload != nil {
		if parseProtocolError != nil {
			parsedError = parseProtocolError(*lastAttempt, parsedPayload)
		}
	}
	upstreamErrorMessage := strings.TrimSpace(parsedError.Message)
	errorMessage := firstNonEmpty(upstreamErrorMessage, lastAttempt.Message, fallbackMessage)
	errorType := firstNonEmpty(
		strings.TrimSpace(parsedError.Type),
		errorTypeForTransport(transportFailure),
		strings.TrimSpace(parsedError.Code),
		"upstream_error",
	)
	errorCode := firstNonEmpty(
		strings.TrimSpace(parsedError.Code),
		lastAttempt.ErrorCode,
		errorCodeForTransport(transportFailure),
	)
	payload := gatewaypreauth.GatewayErrorPayloadOf(errorMessage, errorType, errorCode)
	if hasErrorObject(parsedPayload) {
		payload = gatewayErrorPayloadFromObject(parsedPayload)
	}

	return &DiagnosticUpstreamError{
		StatusCode:              statusCode,
		Payload:                 payload,
		ErrorMessage:            errorMessage,
		PreserveUpstreamMessage: hasErrorObject(parsedPayload) || upstreamErrorMessage != "",
	}
}

// ProtocolErrorPayload mirrors the parsedError pick.
type ProtocolErrorPayload struct {
	Message string
	Type    string
	Code    string
}

func errorTypeForTransport(transportFailure string) string {
	if transportFailure == TransportFailureKindTimeout {
		return "upstream_timeout_error"
	}
	if transportFailure != "" {
		return "upstream_transport_error"
	}
	return ""
}

func errorCodeForTransport(transportFailure string) string {
	if transportFailure == TransportFailureKindTimeout {
		return "upstream_timeout"
	}
	if transportFailure != "" {
		return "upstream_" + transportFailure
	}
	return ""
}

func headersFromObject(headers map[string]string) http.Header {
	output := http.Header{}
	if headers == nil {
		return output
	}
	for name, value := range headers {
		output.Set(name, value)
	}
	return output
}

// parseGatewayNonStreamJSONBody mirrors parseGatewayNonStreamJsonBody for the
// diagnostic path: valid top-level JSON objects only.
func parseGatewayNonStreamJSONBody(bodyText string, headers http.Header) map[string]any {
	trimmed := strings.TrimSpace(bodyText)
	if trimmed == "" || trimmed[0] != '{' {
		return nil
	}
	return parseJSONObject(trimmed)
}

func hasErrorObject(payload map[string]any) bool {
	if payload == nil {
		return false
	}
	errorObject, ok := payload["error"].(map[string]any)
	return ok && errorObject != nil
}

func gatewayErrorPayloadFromObject(payload map[string]any) gatewaypreauth.GatewayErrorPayload {
	errorObject, _ := payload["error"].(map[string]any)
	result := gatewaypreauth.GatewayErrorPayload{
		Error: gatewaypreauth.GatewayErrorBody{
			Message: stringValue(errorObject["message"]),
			Type:    stringValue(errorObject["type"]),
			Code:    stringValue(errorObject["code"]),
		},
	}
	for key, value := range errorObject {
		switch key {
		case "message", "type", "code":
			continue
		}
		if result.Error.Extra == nil {
			result.Error.Extra = map[string]any{}
		}
		result.Error.Extra[key] = value
	}
	return result
}

func isHTTPStatusCode(status int) bool {
	return status >= 400 && status <= 599
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func stringValue(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

// parseJSONObject is a minimal JSON object parser used by the diagnostic
// body branch; full parsing belongs to the body pipeline.
func parseJSONObject(text string) map[string]any {
	parsed, ok := decodeJSONObject([]byte(text))
	if !ok {
		return nil
	}
	return parsed
}
