package openai

import (
	"testing"

	gatewayprotocol "juhe-ai/backend-go/internal/protocols/gateway"
)

func TestClassifyHybridRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     HybridRequestInput
		eligible  bool
		reason    HybridEligibilityReason
		family    gatewayprotocol.EndpointFamily
		operation gatewayprotocol.OpenAIOperation
		lane      gatewayprotocol.RequestLane
	}{
		{
			name:      "chat completions",
			input:     requestInput("POST", "/v1/chat/completions?trace=1", "gpt-5.4"),
			eligible:  true,
			reason:    HybridEligible,
			family:    gatewayprotocol.EndpointChatCompletions,
			operation: gatewayprotocol.OpenAIOperationChatCompletionsCreate,
			lane:      gatewayprotocol.RequestLaneText,
		},
		{
			name:      "chat completions trailing slash",
			input:     requestInput("POST", "/v1/chat/completions/", "gpt-5.4"),
			eligible:  true,
			reason:    HybridEligible,
			family:    gatewayprotocol.EndpointChatCompletions,
			operation: gatewayprotocol.OpenAIOperationChatCompletionsCreate,
			lane:      gatewayprotocol.RequestLaneText,
		},
		{
			name:      "responses",
			input:     requestInput("post", "/responses", ""),
			eligible:  true,
			reason:    HybridEligible,
			family:    gatewayprotocol.EndpointResponses,
			operation: gatewayprotocol.OpenAIOperationResponsesCreate,
			lane:      gatewayprotocol.RequestLaneText,
		},
		{
			name: "body pipeline may accept legacy text json",
			input: func() HybridRequestInput {
				input := requestInput("POST", "/v1/responses", "gpt-5.4")
				input.Request.Headers = map[string]string{"content-type": "text/json"}
				return input
			}(),
			eligible:  true,
			reason:    HybridEligible,
			family:    gatewayprotocol.EndpointResponses,
			operation: gatewayprotocol.OpenAIOperationResponsesCreate,
			lane:      gatewayprotocol.RequestLaneText,
		},
		{
			name:      "image model on responses",
			input:     requestInput("POST", "/v1/responses", "gpt-image-1.5"),
			eligible:  false,
			reason:    HybridImageGenerationRequest,
			family:    gatewayprotocol.EndpointResponses,
			operation: gatewayprotocol.OpenAIOperationResponsesCreate,
			lane:      gatewayprotocol.RequestLaneImage,
		},
		{
			name:      "image family prefix uses the shared lane",
			input:     requestInput("POST", "/v1/responses", "gpt-imageology"),
			eligible:  false,
			reason:    HybridImageGenerationRequest,
			family:    gatewayprotocol.EndpointResponses,
			operation: gatewayprotocol.OpenAIOperationResponsesCreate,
			lane:      gatewayprotocol.RequestLaneImage,
		},
		{
			name: "image tool hint on responses",
			input: func() HybridRequestInput {
				input := requestInput("POST", "/v1/responses", "gpt-5.4")
				input.Request.ImageGenerationHint = true
				return input
			}(),
			eligible:  false,
			reason:    HybridImageGenerationRequest,
			family:    gatewayprotocol.EndpointResponses,
			operation: gatewayprotocol.OpenAIOperationResponsesCreate,
			lane:      gatewayprotocol.RequestLaneImage,
		},
		{
			name:     "embeddings excluded",
			input:    requestInput("POST", "/v1/embeddings", "text-embedding-3-small"),
			eligible: false,
			reason:   HybridUnsupportedOperation,
			family:   gatewayprotocol.EndpointEmbeddings,
			lane:     gatewayprotocol.RequestLaneText,
		},
		{
			name:      "responses compact excluded",
			input:     requestInput("POST", "/v1/responses/compact", "gpt-5.4"),
			eligible:  false,
			reason:    HybridUnsupportedOperation,
			family:    gatewayprotocol.EndpointResponses,
			operation: gatewayprotocol.OpenAIOperationResponsesCompact,
			lane:      gatewayprotocol.RequestLaneText,
		},
		{
			name:     "path substring excluded",
			input:    requestInput("POST", "/internal/responses/replay", "gpt-5.4"),
			eligible: false,
			reason:   HybridUnsupportedOperation,
			family:   gatewayprotocol.EndpointUnknown,
			lane:     gatewayprotocol.RequestLaneText,
		},
		{
			name:      "shared path normalization accepts uppercase",
			input:     requestInput("POST", "/V1/RESPONSES", "gpt-5.4"),
			eligible:  true,
			reason:    HybridEligible,
			family:    gatewayprotocol.EndpointResponses,
			operation: gatewayprotocol.OpenAIOperationResponsesCreate,
			lane:      gatewayprotocol.RequestLaneText,
		},
		{
			name:      "get excluded",
			input:     requestInput("GET", "/v1/responses", "gpt-5.4"),
			eligible:  false,
			reason:    HybridMethodNotAllowed,
			family:    gatewayprotocol.EndpointResponses,
			operation: gatewayprotocol.OpenAIOperationResponsesCreate,
			lane:      gatewayprotocol.RequestLaneText,
		},
		{
			name: "non json body excluded",
			input: func() HybridRequestInput {
				input := requestInput("POST", "/v1/responses", "gpt-5.4")
				input.JSONBody = false
				return input
			}(),
			eligible:  false,
			reason:    HybridNonJSONBody,
			family:    gatewayprotocol.EndpointResponses,
			operation: gatewayprotocol.OpenAIOperationResponsesCreate,
			lane:      gatewayprotocol.RequestLaneText,
		},
		{
			name: "empty body excluded",
			input: func() HybridRequestInput {
				input := requestInput("POST", "/v1/chat/completions", "gpt-5.4")
				input.BodyPresent = false
				return input
			}(),
			eligible:  false,
			reason:    HybridEmptyBody,
			family:    gatewayprotocol.EndpointChatCompletions,
			operation: gatewayprotocol.OpenAIOperationChatCompletionsCreate,
			lane:      gatewayprotocol.RequestLaneText,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ClassifyHybridRequest(tt.input)
			if got.Eligible != tt.eligible {
				t.Fatalf("Eligible = %v, want %v", got.Eligible, tt.eligible)
			}
			if got.Reason != tt.reason {
				t.Errorf("Reason = %q, want %q", got.Reason, tt.reason)
			}
			if got.EndpointFamily != tt.family {
				t.Errorf("EndpointFamily = %q, want %q", got.EndpointFamily, tt.family)
			}
			if got.Operation != tt.operation {
				t.Errorf("Operation = %q, want %q", got.Operation, tt.operation)
			}
			if got.Lane != tt.lane {
				t.Errorf("Lane = %q, want %q", got.Lane, tt.lane)
			}
			if got.Eligible {
				assertEligibleFamilyMatchesOperation(t, got)
			}
		})
	}
}

func assertEligibleFamilyMatchesOperation(t *testing.T, got HybridRequestClassification) {
	t.Helper()
	switch got.Operation {
	case gatewayprotocol.OpenAIOperationChatCompletionsCreate:
		if got.EndpointFamily != gatewayprotocol.EndpointChatCompletions {
			t.Fatalf("eligible chat operation has endpoint family %q", got.EndpointFamily)
		}
	case gatewayprotocol.OpenAIOperationResponsesCreate:
		if got.EndpointFamily != gatewayprotocol.EndpointResponses {
			t.Fatalf("eligible responses operation has endpoint family %q", got.EndpointFamily)
		}
	default:
		t.Fatalf("eligible request has unsupported operation %q", got.Operation)
	}
}

func requestInput(method, path, model string) HybridRequestInput {
	return HybridRequestInput{
		Request: gatewayprotocol.RequestShape{
			Method: method,
			Path:   path,
			Model:  model,
		},
		JSONBody:    true,
		BodyPresent: true,
	}
}
