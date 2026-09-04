package gatewayhybrid

import (
	"strings"
)

// GatewayRequestView mirrors the Express request surface the hybrid modules
// read (routing.service.ts, scoring.service.ts, quality-inspection.service.ts).
// The gateway request plumbing (request/body.ts ownership) materializes this
// view; the hybrid package only consumes it.
type GatewayRequestView struct {
	// Method is the raw method; the routability check compares upper-cased.
	Method string
	// Path mirrors `req.originalUrl.split('?')[0] || req.path`.
	Path string
	// ContentType mirrors `String(req.headers['content-type'] ?? '')`.
	ContentType string
	// RawBody mirrors `request.rawBody` (nil when absent).
	RawBody []byte
	// BodyAvailable mirrors `Boolean(req.body)`.
	BodyAvailable bool
	// ParsedBody mirrors `request.body` when it is a JSON object
	// (OrderedJSON preserves Node key order); nil otherwise.
	ParsedBody any
	// OriginalModel mirrors requestModel(req): gemini path → bodyState.model →
	// body.model string.
	OriginalModel string
	// OriginalModelPresent reports OriginalModel was defined (undefined keys
	// are dropped from JSON payloads in Node).
	OriginalModelPresent bool
	// ConversationKey mirrors getGatewaySessionIdentity(req)?.conversationKey.
	ConversationKey string
	// BodyState mirrors getGatewayRequestBodyState(req); nil when absent.
	BodyState *RequestBodyState
}

// RequestBodyState mirrors GatewayRequestBodyState (request/body.ts) for the
// fields hybrid routing reads. Optional fields stay nil exactly like the
// Node `?: undefined` contract (JSON.stringify drops those keys).
type RequestBodyState struct {
	RawBodyBytes            int64
	ContentType             string
	JSONParseStatus         string // pending|parsing|parsed|scanned_json|deferred_large_json|invalid_json
	Model                   string
	Stream                  *bool
	ImageGeneration         *bool
	ImageGenerationForced   *bool
	StrictOutputRequirement bool
}

// NonStreamJSONBody mirrors GatewayNonStreamJsonBody
// (response/non-stream-json-body.ts).
type NonStreamJSONBody struct {
	Status string // valid | empty | not_json | invalid
	Value  any
}

// ParseNonStreamJSONBody mirrors parseGatewayNonStreamJsonBody.
func ParseNonStreamJSONBody(bodyText string, contentType string) NonStreamJSONBody {
	trimmed := strings.TrimSpace(bodyText)
	if trimmed == "" {
		return NonStreamJSONBody{Status: "empty"}
	}
	lowerType := strings.ToLower(contentType)
	if !strings.Contains(lowerType, "json") && !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return NonStreamJSONBody{Status: "not_json"}
	}
	value, err := ParseJSONOrdered([]byte(trimmed))
	if err != nil {
		return NonStreamJSONBody{Status: "invalid"}
	}
	return NonStreamJSONBody{Status: "valid", Value: value}
}

// NonStreamJSONBodyFromValue mirrors gatewayNonStreamJsonBodyFromValue.
func NonStreamJSONBodyFromValue(value any) NonStreamJSONBody {
	return NonStreamJSONBody{Status: "valid", Value: value}
}

// hasRawBody mirrors `rawBody?.length` truthiness.
func (view *GatewayRequestView) hasRawBody() bool {
	return len(view.RawBody) > 0
}

// rawBodyBytes returns Buffer.byteLength(rawBody).
func (view *GatewayRequestView) rawBodyBytes() int {
	return len(view.RawBody)
}

// bodyObject returns request.body when it is a JSON object.
func (view *GatewayRequestView) bodyObject() *OrderedJSON {
	if object, ok := view.ParsedBody.(*OrderedJSON); ok {
		return object
	}
	return nil
}
