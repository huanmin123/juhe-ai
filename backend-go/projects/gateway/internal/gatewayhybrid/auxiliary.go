package gatewayhybrid

import (
	"context"
	"strconv"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// Pure helpers split out of
// backend/src/modules/gateway/hybrid/auxiliary-dispatch.service.ts. The
// upstream dispatch itself is the AuxiliaryDispatcher port; the response /
// usage parsing and the upstream failure normalization stay in this package
// so the port adapters reuse the exact Node semantics.

// AuxiliaryEndpointSuffix mirrors the endpoint suffix selection in
// dispatchHybridAuxiliaryChatCompletion.
func AuxiliaryEndpointSuffix(trafficSource string) string {
	if trafficSource == AuxiliaryTrafficSourceHybridQualityScoring {
		return "#hybrid-quality-scoring"
	}
	return "#hybrid-scoring"
}

// ComposeAuxiliaryEndpoint mirrors `${input.endpoint}#${suffix}`.
func ComposeAuxiliaryEndpoint(endpoint, trafficSource string) string {
	return endpoint + AuxiliaryEndpointSuffix(trafficSource)
}

// ParseHybridAuxiliaryResponse mirrors parseHybridAuxiliaryResponse: parse
// the non-stream JSON body and extract OpenAI usage (value first, text
// fragment fallback for invalid bodies).
func ParseHybridAuxiliaryResponse(bodyText string, contentType string) (NonStreamJSONBody, gatewayproto.ParsedUsage) {
	parsedResponseBody := ParseNonStreamJSONBody(bodyText, contentType)
	if parsedResponseBody.Status == "valid" {
		return parsedResponseBody, gatewayopenai.ParseUsageFromJSONValue(ToNativeValue(parsedResponseBody.Value))
	}
	return parsedResponseBody, gatewayopenai.ParseUsageFromJSONTextFragment(bodyText)
}

// EmptyHybridAuxiliaryUsage mirrors emptyHybridAuxiliaryUsage.
func EmptyHybridAuxiliaryUsage() gatewayproto.ParsedUsage {
	return gatewayproto.EmptyUsage()
}

// AuxiliaryUpstreamFailure mirrors hybridAuxiliaryUpstreamFailure: a
// non-empty error payload message wins, then the trimmed body text, then the
// HTTP status line.
func AuxiliaryUpstreamFailure(input AuxiliaryUpstreamFailureInput) (string, string) {
	parsedBody := ParseNonStreamJSONBody(input.BodyText, input.ContentType)
	errorPayload := gatewayproto.ErrorPayload{}
	if parsedBody.Status == "valid" {
		// gatewayopenai expects plain map payloads; OrderedJSON values are
		// converted without changing the JSON semantics.
		errorPayload = gatewayopenai.ParseErrorPayloadFromJSONValue(ToNativeValue(parsedBody.Value))
	}
	errorCode := input.FallbackErrorCode
	if strings.TrimSpace(errorPayload.Code) != "" {
		errorCode = errorPayload.Code
	}
	errorMessage := ""
	if strings.TrimSpace(errorPayload.Message) != "" {
		errorMessage = errorPayload.Message
	} else if trimmed := strings.TrimSpace(input.BodyText); trimmed != "" {
		errorMessage = trimmed
	} else {
		errorMessage = "上游返回 HTTP " + strconv.Itoa(input.StatusCode)
	}
	return errorCode, errorMessage
}

// ToNativeValue converts OrderedJSON trees into plain map[string]any/[]any
// values for protocol helpers that expect encoding/json shapes.
func ToNativeValue(value any) any {
	switch typed := value.(type) {
	case *OrderedJSON:
		converted := make(map[string]any, typed.Len())
		for _, key := range typed.Keys() {
			item, _ := typed.Get(key)
			converted[key] = ToNativeValue(item)
		}
		return converted
	case []any:
		converted := make([]any, len(typed))
		for index, item := range typed {
			converted[index] = ToNativeValue(item)
		}
		return converted
	default:
		return value
	}
}

// AuxiliaryUpstreamFailureInput mirrors the hybridAuxiliaryUpstreamFailure
// input.
type AuxiliaryUpstreamFailureInput struct {
	Account           OpenAIAccountSecret
	BodyText          string
	ContentType       string
	StatusCode        int
	FallbackErrorCode string
}

// AuxiliaryFinishOnce wraps a dispatcher Finish callback with the
// createFinish idempotence guard (the port adapter owns the audit/lease
// side effects; this package only enforces call-once).
func AuxiliaryFinishOnce(finish func(ctx context.Context, finish AuxiliaryDispatchFinishInput) error, ctx context.Context) func(AuxiliaryDispatchFinishInput) {
	return onceFinish(finish, ctx)
}
