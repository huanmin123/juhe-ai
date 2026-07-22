package openai

import "testing"

func TestClassifyHybridRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      HybridRequestInput
		eligible   bool
		reason     HybridEligibilityReason
		family     HybridEndpointFamily
		modelKind  ModelKind
		normalized string
	}{
		{
			name: "chat completions",
			input: HybridRequestInput{
				Method:       "POST",
				PathAndQuery: "/v1/chat/completions?trace=1",
				ContentType:  "application/json; charset=utf-8",
				BodyPresent:  true,
				Model:        " GPT-5.4 ",
			},
			eligible:   true,
			reason:     HybridEligible,
			family:     HybridEndpointChatCompletions,
			modelKind:  ModelKindOther,
			normalized: "gpt-5.4",
		},
		{
			name: "responses vendor json",
			input: HybridRequestInput{
				Method:       "post",
				PathAndQuery: "/responses",
				ContentType:  "application/vnd.openai.request+json",
				BodyPresent:  true,
			},
			eligible:  true,
			reason:    HybridEligible,
			family:    HybridEndpointResponses,
			modelKind: ModelKindMissing,
		},
		{
			name: "image model on responses",
			input: HybridRequestInput{
				Method:       "POST",
				PathAndQuery: "/v1/responses",
				ContentType:  "application/json",
				BodyPresent:  true,
				Model:        "gpt-image-1.5",
			},
			eligible:   false,
			reason:     HybridImageGenerationRequest,
			family:     HybridEndpointResponses,
			modelKind:  ModelKindImageGeneration,
			normalized: "gpt-image-1.5",
		},
		{
			name: "image tool hint on responses",
			input: HybridRequestInput{
				Method:              "POST",
				PathAndQuery:        "/v1/responses",
				ContentType:         "application/json",
				BodyPresent:         true,
				Model:               "gpt-5.4",
				ImageGenerationHint: true,
			},
			eligible:   false,
			reason:     HybridImageGenerationRequest,
			family:     HybridEndpointResponses,
			modelKind:  ModelKindOther,
			normalized: "gpt-5.4",
		},
		{
			name: "embeddings excluded",
			input: HybridRequestInput{
				Method:       "POST",
				PathAndQuery: "/v1/embeddings",
				ContentType:  "application/json",
				BodyPresent:  true,
				Model:        "text-embedding-3-small",
			},
			eligible:   false,
			reason:     HybridUnsupportedEndpoint,
			modelKind:  ModelKindOther,
			normalized: "text-embedding-3-small",
		},
		{
			name: "responses compact excluded",
			input: HybridRequestInput{
				Method:       "POST",
				PathAndQuery: "/v1/responses/compact",
				ContentType:  "application/json",
				BodyPresent:  true,
			},
			eligible:  false,
			reason:    HybridUnsupportedEndpoint,
			modelKind: ModelKindMissing,
		},
		{
			name: "path substring excluded",
			input: HybridRequestInput{
				Method:       "POST",
				PathAndQuery: "/internal/responses/replay",
				ContentType:  "application/json",
				BodyPresent:  true,
			},
			eligible:  false,
			reason:    HybridUnsupportedEndpoint,
			modelKind: ModelKindMissing,
		},
		{
			name: "get excluded",
			input: HybridRequestInput{
				Method:       "GET",
				PathAndQuery: "/v1/responses",
				ContentType:  "application/json",
				BodyPresent:  true,
			},
			eligible:  false,
			reason:    HybridMethodNotAllowed,
			family:    HybridEndpointResponses,
			modelKind: ModelKindMissing,
		},
		{
			name: "misleading json substring excluded",
			input: HybridRequestInput{
				Method:       "POST",
				PathAndQuery: "/v1/responses",
				ContentType:  "text/not-json",
				BodyPresent:  true,
			},
			eligible:  false,
			reason:    HybridUnsupportedMediaType,
			family:    HybridEndpointResponses,
			modelKind: ModelKindMissing,
		},
		{
			name: "malformed media type excluded",
			input: HybridRequestInput{
				Method:       "POST",
				PathAndQuery: "/v1/responses",
				ContentType:  "application/json; charset",
				BodyPresent:  true,
			},
			eligible:  false,
			reason:    HybridUnsupportedMediaType,
			family:    HybridEndpointResponses,
			modelKind: ModelKindMissing,
		},
		{
			name: "empty body excluded",
			input: HybridRequestInput{
				Method:       "POST",
				PathAndQuery: "/v1/chat/completions",
				ContentType:  "application/json",
			},
			eligible:  false,
			reason:    HybridEmptyBody,
			family:    HybridEndpointChatCompletions,
			modelKind: ModelKindMissing,
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
			if got.Model.Kind != tt.modelKind {
				t.Errorf("Model.Kind = %q, want %q", got.Model.Kind, tt.modelKind)
			}
			if got.Model.Normalized != tt.normalized {
				t.Errorf("Model.Normalized = %q, want %q", got.Model.Normalized, tt.normalized)
			}
		})
	}
}

func TestClassifyModelHintUsesFamilyBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		model string
		kind  ModelKind
	}{
		{model: "gpt-image", kind: ModelKindImageGeneration},
		{model: "gpt-image-1", kind: ModelKindImageGeneration},
		{model: "dall-e", kind: ModelKindImageGeneration},
		{model: "DALL-E-3", kind: ModelKindImageGeneration},
		{model: "gpt-imageology", kind: ModelKindOther},
		{model: "dall-east", kind: ModelKindOther},
		{model: "", kind: ModelKindMissing},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			t.Parallel()
			if got := ClassifyModelHint(tt.model); got.Kind != tt.kind {
				t.Fatalf("ClassifyModelHint(%q).Kind = %q, want %q", tt.model, got.Kind, tt.kind)
			}
		})
	}
}

func TestHybridEndpointFamilyFromPathIsExact(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path   string
		family HybridEndpointFamily
	}{
		{path: "/v1/chat/completions", family: HybridEndpointChatCompletions},
		{path: "/chat/completions///?request_id=1", family: HybridEndpointChatCompletions},
		{path: "/v1/responses", family: HybridEndpointResponses},
		{path: "/responses/?request_id=1", family: HybridEndpointResponses},
		{path: "/V1/responses", family: HybridEndpointUnknown},
		{path: "/v1/%72esponses", family: HybridEndpointUnknown},
		{path: "/v1/other/../responses", family: HybridEndpointUnknown},
		{path: "https://api.openai.com/v1/responses", family: HybridEndpointUnknown},
		{path: "/v1/responses/compact", family: HybridEndpointUnknown},
		{path: "/v1/responses/replay", family: HybridEndpointUnknown},
		{path: "/v1/chat/completions/batches", family: HybridEndpointUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			if got := HybridEndpointFamilyFromPath(tt.path); got != tt.family {
				t.Fatalf("HybridEndpointFamilyFromPath(%q) = %q, want %q", tt.path, got, tt.family)
			}
		})
	}
}
