package openai

import (
	"strings"

	gatewayprotocol "juhe-ai/backend-go/internal/protocols/gateway"
)

// HybridEligibilityReason is stable diagnostic output for preflight and
// tests. Ineligible requests should continue through their normal route.
type HybridEligibilityReason string

const (
	HybridEligible               HybridEligibilityReason = "eligible"
	HybridMethodNotAllowed       HybridEligibilityReason = "method_not_allowed"
	HybridUnsupportedOperation   HybridEligibilityReason = "unsupported_operation"
	HybridNonJSONBody            HybridEligibilityReason = "non_json_body"
	HybridEmptyBody              HybridEligibilityReason = "empty_body"
	HybridImageGenerationRequest HybridEligibilityReason = "image_generation_request"
)

// HybridRequestInput consumes classifications produced by the shared gateway
// request pipeline. JSONBody records the body parser's decision so this layer
// does not create a second Content-Type policy.
type HybridRequestInput struct {
	Request     gatewayprotocol.RequestShape
	JSONBody    bool
	BodyPresent bool
}

// HybridRequestClassification determines whether a request may enter smart
// hybrid scoring without performing I/O or mutating the request.
type HybridRequestClassification struct {
	Eligible       bool
	Reason         HybridEligibilityReason
	EndpointFamily gatewayprotocol.EndpointFamily
	Operation      gatewayprotocol.OpenAIOperation
	Lane           gatewayprotocol.RequestLane
}

// ClassifyHybridRequest limits smart hybrid routing to JSON text-generation
// calls. Generic JSON POST endpoints, response compaction, and image requests
// must not be scored or have their model rewritten by the text hybrid router.
func ClassifyHybridRequest(input HybridRequestInput) HybridRequestClassification {
	result := HybridRequestClassification{
		EndpointFamily: gatewayprotocol.EndpointFamilyFromPath(gatewayprotocol.ProtocolOpenAI, input.Request.Path),
		Operation:      gatewayprotocol.OpenAIOperationFromPath(input.Request.Path),
		Lane:           gatewayprotocol.ResolveRequestLane(input.Request),
	}

	switch {
	case !strings.EqualFold(strings.TrimSpace(input.Request.Method), "POST"):
		result.Reason = HybridMethodNotAllowed
	case result.Operation != gatewayprotocol.OpenAIOperationChatCompletionsCreate &&
		result.Operation != gatewayprotocol.OpenAIOperationResponsesCreate:
		result.Reason = HybridUnsupportedOperation
	case !input.JSONBody:
		result.Reason = HybridNonJSONBody
	case !input.BodyPresent:
		result.Reason = HybridEmptyBody
	case result.Lane == gatewayprotocol.RequestLaneImage:
		result.Reason = HybridImageGenerationRequest
	default:
		result.Eligible = true
		result.Reason = HybridEligible
	}

	return result
}
