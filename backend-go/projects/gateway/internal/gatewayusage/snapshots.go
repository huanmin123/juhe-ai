package gatewayusage

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// Usage request/response snapshots mirroring
// backend/src/modules/gateway/usage/snapshots.ts. Both structs serialize
// with the Node key order and skip undefined (nil) fields so the persisted
// JSON documents stay field-compatible.

// DownstreamConnectionClosedMessage mirrors downstreamConnectionClosedMessage
// (response/client-abort.ts).
const DownstreamConnectionClosedMessage = "下游连接关闭"

// GeneratedByGateway mirrors the generatedBy: 'gateway' marker.
const GeneratedByGateway = "gateway"// UsageRequestSnapshot mirrors UsageRequestSnapshot.
type UsageRequestSnapshot struct {
	Method                   string         `json:"method"`
	Path                     string         `json:"path"`
	OriginalURL              string         `json:"originalUrl"`
	ClientIP                 string         `json:"clientIp,omitempty"`
	TraceID                  string         `json:"traceId"`
	RequestedServiceTier     string         `json:"requestedServiceTier,omitempty"`
	RequestedReasoningEffort string         `json:"requestedReasoningEffort,omitempty"`
	Headers                  map[string]any `json:"headers"`
	Body                     any            `json:"body,omitempty"`
	// BodyOmission carries omission metadata (Node bodyOmission: unknown).
	BodyOmission any `json:"bodyOmission,omitempty"`
}

// UsageResponseSnapshot mirrors UsageResponseSnapshot.
type UsageResponseSnapshot struct {
	UpstreamURL  string           `json:"upstreamUrl,omitempty"`
	StatusCode   *int             `json:"statusCode,omitempty"`
	Headers      map[string]any   `json:"headers,omitempty"`
	BodyText     string           `json:"bodyText,omitempty"`
	BodyOmission any              `json:"bodyOmission,omitempty"`
	ErrorMessage string           `json:"errorMessage,omitempty"`
	GeneratedBy  string           `json:"generatedBy,omitempty"`
	LastUpstream *LastUpstreamAttemptSnapshot `json:"lastUpstreamAttempt,omitempty"`
}

// LastUpstreamAttemptSnapshot mirrors the lastUpstreamAttempt shape.
type LastUpstreamAttemptSnapshot struct {
	AccountID    string           `json:"accountId"`
	AccountName  string           `json:"accountName"`
	UpstreamURL  string           `json:"upstreamUrl"`
	StatusCode   *int             `json:"statusCode,omitempty"`
	Headers      map[string]any   `json:"headers,omitempty"`
	BodyText     string           `json:"bodyText,omitempty"`
	ErrorMessage string           `json:"errorMessage,omitempty"`
}

// UpstreamAttempt mirrors the consumed UpstreamAttempt
// (upstream/attempt.ts) fields the error snapshot builder reads.
type UpstreamAttempt struct {
	AccountID             string
	AccountName           string
	UpstreamURL           string
	Status                *int
	ResponseHeaders       map[string]any
	ResponseBodyText      string
	Message               string
}

// RequestSnapshotBodyState mirrors the consumed GatewayRequestBodyState
// fields (request/body.ts) for snapshot tier/effort extraction.
type RequestSnapshotBodyState struct {
	ServiceTier    any
	ReasoningEffort any
	Model          any
	Stream         any
}

// BuildUsageRequestSnapshotInput mirrors the inputs buildUsageRequestSnapshot
// derives from the Express request.
type BuildUsageRequestSnapshotInput struct {
	Method      string
	Path        string
	OriginalURL string
	ClientIP    string
	TraceID     string
	// BodyState mirrors getGatewayRequestBodyState(req); nil = unset.
	BodyState *RequestSnapshotBodyState
	// RawBody mirrors req.body (a JSON-like value; typically *OrderedObject).
	RawBody any
	// BodySummary mirrors buildGatewayRequestBodySummary(req); non-nil wins
	// over RawBody.
	BodySummary any
	// Headers mirrors requestHeadersToObject(req.headers).
	Headers map[string]any
}

// BuildUsageRequestSnapshot mirrors buildUsageRequestSnapshot.
func BuildUsageRequestSnapshot(input BuildUsageRequestSnapshotInput) UsageRequestSnapshot {
	bodyStateTier := any(nil)
	bodyStateEffort := any(nil)
	if input.BodyState != nil {
		bodyStateTier = input.BodyState.ServiceTier
		bodyStateEffort = input.BodyState.ReasoningEffort
	}
	if bodyStateTier == nil {
		bodyStateTier = jsonRecordField(input.RawBody, "service_tier")
	}
	if bodyStateEffort == nil {
		bodyStateEffort = requestedReasoningEffortFromBody(input.RawBody)
	}
	snapshot := UsageRequestSnapshot{
		Method:                   input.Method,
		Path:                     input.Path,
		OriginalURL:              input.OriginalURL,
		ClientIP:                 input.ClientIP,
		TraceID:                  input.TraceID,
		RequestedServiceTier:     NormalizeUsageServiceTier(bodyStateTier),
		RequestedReasoningEffort: NormalizeUsageReasoningEffort(bodyStateEffort),
		Headers:                  input.Headers,
	}
	if input.BodySummary != nil {
		snapshot.Body = input.BodySummary
	} else if input.RawBody != nil {
		snapshot.Body = input.RawBody
	}
	return snapshot
}

// requestedReasoningEffortFromBody mirrors requestedReasoningEffortFromBody:
// nested reasoning.effort wins, then the flat reasoning_effort field.
func requestedReasoningEffortFromBody(body any) any {
	record, ok := body.(*OrderedObject)
	if !ok {
		if asMap, isMap := body.(map[string]any); isMap {
			ordered := NewOrderedObject()
			for _, key := range sortedMapKeys(asMap) {
				ordered.Set(key, asMap[key])
			}
			record = ordered
		} else {
			return nil
		}
	}
	if nested, isRecord := record.Get("reasoning").(*OrderedObject); isRecord {
		if normalized := NormalizeUsageReasoningEffort(nested.Get("effort")); normalized != "" {
			return normalized
		}
	}
	return record.Get("reasoning_effort")
}

// jsonRecordField extracts one string-typed field from a JSON-like body.
func jsonRecordField(body any, field string) any {
	if record, ok := body.(*OrderedObject); ok {
		return record.Get(field)
	}
	if asMap, isMap := body.(map[string]any); isMap {
		return asMap[field]
	}
	return nil
}

// HeadersToObject mirrors requestHeadersToObject: single values stay
// strings, repeated values become arrays, undefined values are skipped.
func HeadersToObject(headers http.Header) map[string]any {
	output := map[string]any{}
	for name, values := range headers {
		if len(values) == 0 {
			continue
		}
		if len(values) == 1 {
			output[name] = values[0]
			continue
		}
		converted := make([]any, len(values))
		for index, value := range values {
			converted[index] = value
		}
		output[name] = converted
	}
	return output
}

// BuildUsageResponseSnapshotInput mirrors the buildUsageResponseSnapshot
// input object.
type BuildUsageResponseSnapshotInput struct {
	UpstreamURL  string
	StatusCode   *int
	Headers      map[string]any
	BodyText     string
	BodyOmission any
	ErrorMessage string
	GeneratedBy  string
}

// BuildUsageResponseSnapshot mirrors buildUsageResponseSnapshot (the headers
// map is copied).
func BuildUsageResponseSnapshot(input BuildUsageResponseSnapshotInput) UsageResponseSnapshot {
	var headers map[string]any
	if input.Headers != nil {
		headers = make(map[string]any, len(input.Headers))
		for name, value := range input.Headers {
			headers[name] = value
		}
	}
	return UsageResponseSnapshot{
		UpstreamURL:  input.UpstreamURL,
		StatusCode:   input.StatusCode,
		Headers:      headers,
		BodyText:     input.BodyText,
		BodyOmission: input.BodyOmission,
		ErrorMessage: input.ErrorMessage,
		GeneratedBy:  input.GeneratedBy,
	}
}

// GatewayErrorPayload mirrors the consumed GatewayErrorPayload
// (response/responses.ts) shape.
type GatewayErrorPayload struct {
	Error GatewayErrorPayloadError
	Extra *OrderedObject
}

// GatewayErrorPayloadError mirrors the nested error object.
type GatewayErrorPayloadError struct {
	Message string
	Type    string
	Code    string
}

// BuildGatewayErrorResponseSnapshot mirrors buildGatewayErrorResponseSnapshot.
// payload is the JSON-like GatewayErrorPayload (error.message / error.code /
// error.type plus any extra keys); it is serialized verbatim as bodyText.
func BuildGatewayErrorResponseSnapshot(statusCode int, payload any, lastAttempt *UpstreamAttempt) UsageResponseSnapshot {
	errorMessage := ""
	if message, ok := nestedErrorField(payload, "message").(string); ok {
		errorMessage = message
	}
	status := statusCode
	snapshot := BuildUsageResponseSnapshot(BuildUsageResponseSnapshotInput{
		StatusCode:   &status,
		Headers:      map[string]any{"content-type": "application/json; charset=utf-8"},
		BodyText:     string(mustMarshalJSON(payload)),
		ErrorMessage: errorMessage,
		GeneratedBy:  GeneratedByGateway,
	})
	if lastAttempt != nil {
		snapshot.LastUpstream = &LastUpstreamAttemptSnapshot{
			AccountID:    lastAttempt.AccountID,
			AccountName:  lastAttempt.AccountName,
			UpstreamURL:  lastAttempt.UpstreamURL,
			StatusCode:   lastAttempt.Status,
			Headers:      lastAttempt.ResponseHeaders,
			BodyText:     lastAttempt.ResponseBodyText,
			ErrorMessage: lastAttempt.Message,
		}
	}
	return snapshot
}

// GatewayErrorField reads one field of the GatewayErrorPayload error object
// (payload.error.<field>) from a JSON-like payload.
func GatewayErrorField(payload any, field string) any {
	return nestedErrorField(payload, field)
}

func nestedErrorField(payload any, field string) any {
	errorObject := jsonRecordField(payload, "error")
	return jsonRecordField(errorObject, field)
}

func mustMarshalJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		return []byte("{}")
	}
	return encoded
}

// SanitizeURLCredentialsForLog mirrors sanitizeUrlCredentialsForLog: trim
// and pass through; empty means undefined.
func SanitizeURLCredentialsForLog(value string) string {
	return strings.TrimSpace(value)
}

// sanitizeURLForLogSensitiveNames mirrors the oauth sensitive query names.
var sanitizeURLForLogSensitiveNames = map[string]bool{
	"state":           true,
	"nonce":           true,
	"code_challenge":  true,
	"transaction_id":  true,
	"user_code":       true,
}

// SanitizeURLForLog mirrors sanitizeUrlForLog: only /oauth/authorize and
// /oauth/device paths are rewritten; sensitive query names are redacted and
// only path+query survive.
func SanitizeURLForLog(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return value
	}
	if parsed.Path != "/oauth/authorize" && parsed.Path != "/oauth/device" {
		return value
	}
	query := parsed.Query()
	for name := range query {
		if sanitizeURLForLogSensitiveNames[name] {
			query.Set(name, "[redacted]")
		}
	}
	return parsed.Path + "?" + query.Encode()
}

// gatewayLogErrorMessageMaxBytes mirrors gatewayLogErrorMessageMaxBytes.
const gatewayLogErrorMessageMaxBytes = 4 * 1024

// GatewayLogErrorMessage mirrors GatewayLogErrorMessage.
type GatewayLogErrorMessage struct {
	ErrorMessage         string
	ErrorMessageBytes    int
	ErrorMessageTruncated bool
}

// BuildGatewayLogErrorMessage mirrors buildGatewayLogErrorMessage: the
// persisted error message budget with the exact truncation suffix contract.
func BuildGatewayLogErrorMessage(value string) GatewayLogErrorMessage {
	if len(value) == 0 {
		return GatewayLogErrorMessage{ErrorMessageBytes: 0, ErrorMessageTruncated: false}
	}
	errorMessageBytes := len(value)
	if errorMessageBytes <= gatewayLogErrorMessageMaxBytes {
		return GatewayLogErrorMessage{
			ErrorMessage:      value,
			ErrorMessageBytes: errorMessageBytes,
			ErrorMessageTruncated: false,
		}
	}
	suffix := "...[truncated " + itoa(errorMessageBytes) + " bytes]"
	suffixBytes := len(suffix)
	prefixBudget := gatewayLogErrorMessageMaxBytes - suffixBytes - 32
	if prefixBudget < 0 {
		prefixBudget = 0
	}
	prefix := sliceStringByUTF8Bytes(value, prefixBudget)
	prefixBytes := len(prefix)
	truncatedSuffix := "...[truncated " + itoa(errorMessageBytes-prefixBytes) + " bytes]"
	return GatewayLogErrorMessage{
		ErrorMessage:      prefix + truncatedSuffix,
		ErrorMessageBytes: errorMessageBytes,
		ErrorMessageTruncated: true,
	}
}
