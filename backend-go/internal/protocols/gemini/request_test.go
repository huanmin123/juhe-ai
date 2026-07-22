package gemini

import (
	"errors"
	"testing"

	protocolgateway "juhe-ai/backend-go/internal/protocols/gateway"
)

func TestClassifyRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       RequestInput
		family      protocolgateway.EndpointFamily
		mode        EndpointMode
		action      protocolgateway.GeminiInteractionAction
		model       string
		interaction string
		stream      bool
		native      bool
	}{
		{
			name:   "model list",
			input:  RequestInput{Method: "GET", PathAndQuery: "/v1beta/models?pageSize=20"},
			family: protocolgateway.EndpointModels,
			mode:   ModeModels,
			native: true,
		},
		{
			name:   "shared root model list follows openai priority",
			input:  RequestInput{Method: "GET", PathAndQuery: "/models"},
			family: protocolgateway.EndpointModels,
		},
		{
			name:   "generate content keeps decoded model",
			input:  RequestInput{Method: "post", PathAndQuery: "/v1beta/models/gemini-3.5-flash:generateContent?key=secret"},
			family: protocolgateway.EndpointGenerateContent,
			mode:   ModeGenerateContentJSON,
			model:  "gemini-3.5-flash",
			native: true,
		},
		{
			name:   "generate content alt sse follows gateway registry",
			input:  RequestInput{Method: "POST", PathAndQuery: "/v1beta/models/gemini-3.5-flash:generateContent?alt=sse"},
			family: protocolgateway.EndpointGenerateContent,
			mode:   ModeGenerateContentSSE,
			model:  "gemini-3.5-flash",
			stream: true,
			native: true,
		},
		{
			name:   "stream generate is always sse",
			input:  RequestInput{Method: "POST", PathAndQuery: "/models/gemini-3.5-flash:streamGenerateContent"},
			family: protocolgateway.EndpointStreamGenerateContent,
			mode:   ModeGenerateContentSSE,
			model:  "gemini-3.5-flash",
			stream: true,
			native: true,
		},
		{
			name:   "count tokens",
			input:  RequestInput{Method: "POST", PathAndQuery: "/v1beta/models/gemini-3.5-flash:countTokens"},
			family: protocolgateway.EndpointCountTokens,
			mode:   ModeCountTokens,
			model:  "gemini-3.5-flash",
			native: true,
		},
		{
			name:   "embed content",
			input:  RequestInput{Method: "POST", PathAndQuery: "/v1beta/models/gemini-embedding-2:embedContent"},
			family: protocolgateway.EndpointEmbedContent,
			mode:   ModeEmbedContent,
			model:  "gemini-embedding-2",
			native: true,
		},
		{
			name:   "interaction create json",
			input:  RequestInput{Method: "POST", PathAndQuery: "/v1beta/interactions"},
			family: protocolgateway.EndpointInteractions,
			mode:   ModeInteractionsJSON,
			action: protocolgateway.GeminiInteractionCreate,
			native: true,
		},
		{
			name:   "interaction create body stream",
			input:  RequestInput{Method: "POST", PathAndQuery: "/v1beta/interactions", BodyStream: true},
			family: protocolgateway.EndpointInteractions,
			mode:   ModeInteractionsSSE,
			action: protocolgateway.GeminiInteractionCreate,
			stream: true,
			native: true,
		},
		{
			name:   "interaction create accept stream",
			input:  RequestInput{Method: "POST", PathAndQuery: "/v1beta/interactions", Accept: "application/json, text/event-stream"},
			family: protocolgateway.EndpointInteractions,
			mode:   ModeInteractionsSSE,
			action: protocolgateway.GeminiInteractionCreate,
			stream: true,
			native: true,
		},
		{
			name:   "interaction create query stream is ignored",
			input:  RequestInput{Method: "POST", PathAndQuery: "/v1beta/interactions?stream=true"},
			family: protocolgateway.EndpointInteractions,
			mode:   ModeInteractionsJSON,
			action: protocolgateway.GeminiInteractionCreate,
			native: true,
		},
		{
			name:        "interaction get query stream",
			input:       RequestInput{Method: "GET", PathAndQuery: "/v1beta/interactions/abc%2D123?stream=TRUE"},
			family:      protocolgateway.EndpointInteractions,
			mode:        ModeInteractionsSSE,
			action:      protocolgateway.GeminiInteractionGet,
			interaction: "abc-123",
			stream:      true,
			native:      true,
		},
		{
			name:        "interaction delete cannot become stream",
			input:       RequestInput{Method: "DELETE", PathAndQuery: "/v1beta/interactions/abc?stream=true", Accept: "text/event-stream", BodyStream: true},
			family:      protocolgateway.EndpointInteractions,
			mode:        ModeInteractionsJSON,
			action:      protocolgateway.GeminiInteractionDelete,
			interaction: "abc",
			native:      true,
		},
		{
			name:        "interaction cancel",
			input:       RequestInput{Method: "POST", PathAndQuery: "/v1beta/interactions/abc/cancel"},
			family:      protocolgateway.EndpointInteractions,
			mode:        ModeInteractionsJSON,
			action:      protocolgateway.GeminiInteractionCancel,
			interaction: "abc",
			native:      true,
		},
		{
			name:   "legacy interaction alt sse is not stream",
			input:  RequestInput{Method: "POST", PathAndQuery: "/v1beta/interactions?alt=sse"},
			family: protocolgateway.EndpointInteractions,
			mode:   ModeInteractionsJSON,
			action: protocolgateway.GeminiInteractionCreate,
			native: true,
		},
		{
			name:   "wrong method is not native",
			input:  RequestInput{Method: "GET", PathAndQuery: "/v1beta/models/gemini:generateContent"},
			family: protocolgateway.EndpointGenerateContent,
			model:  "gemini",
		},
		{
			name:   "interaction root get is not a resource lookup",
			input:  RequestInput{Method: "GET", PathAndQuery: "/v1beta/interactions"},
			family: protocolgateway.EndpointInteractions,
		},
		{
			name:        "interaction resource post is not a supported action",
			input:       RequestInput{Method: "POST", PathAndQuery: "/v1beta/interactions/abc"},
			family:      protocolgateway.EndpointInteractions,
			interaction: "abc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ClassifyRequest(tt.input)
			if err != nil {
				t.Fatalf("ClassifyRequest() error = %v", err)
			}
			if got.Family != tt.family || got.Mode != tt.mode || got.InteractionAction != tt.action {
				t.Fatalf("classification = family %q mode %q action %q, want %q %q %q", got.Family, got.Mode, got.InteractionAction, tt.family, tt.mode, tt.action)
			}
			if got.Model != tt.model || got.InteractionID != tt.interaction {
				t.Errorf("identifiers = model %q interaction %q, want %q %q", got.Model, got.InteractionID, tt.model, tt.interaction)
			}
			if got.Stream != tt.stream || got.Native != tt.native {
				t.Errorf("flags = stream %v native %v, want %v %v", got.Stream, got.Native, tt.stream, tt.native)
			}
		})
	}
}

func TestClassifyRequestQueryFactsDoNotExposeCredential(t *testing.T) {
	t.Parallel()

	got, err := ClassifyRequest(RequestInput{
		Method:       "POST",
		PathAndQuery: "/v1beta/models/gemini:streamGenerateContent?key=top-secret&alt=sse&trace=1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Query.HasAPIKey || !got.Query.AltSSE {
		t.Fatalf("query facts = %#v", got.Query)
	}
	if got.Query.StreamRequested {
		t.Fatal("alt=sse must not be reported as the Interactions stream query")
	}
}

func TestClassifyRequestRejectsAmbiguousOrUnsafeTargets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		err  error
	}{
		{name: "conflicting stream query", path: "/v1beta/interactions/abc?stream=true&stream=false", err: ErrAmbiguousQuery},
		{name: "encoded model slash", path: "/v1beta/models/gemini%2Fflash:generateContent", err: ErrInvalidIdentifier},
		{name: "encoded interaction slash", path: "/v1beta/interactions/a%2Fb", err: ErrInvalidIdentifier},
		{name: "interaction parent segment", path: "/v1beta/interactions/%2E%2E", err: ErrInvalidIdentifier},
		{name: "interaction encoded query delimiter", path: "/v1beta/interactions/a%3Fb", err: ErrInvalidIdentifier},
		{name: "malformed escape", path: "/v1beta/interactions/%zz", err: ErrInvalidIdentifier},
		{name: "absolute URL", path: "https://example.test/v1beta/interactions/abc", err: ErrInvalidRequestTarget},
		{name: "fragment", path: "/v1beta/interactions/abc#fragment", err: ErrInvalidRequestTarget},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := ClassifyRequest(RequestInput{Method: "GET", PathAndQuery: tt.path})
			if !errors.Is(err, tt.err) {
				t.Fatalf("error = %v, want %v", err, tt.err)
			}
		})
	}
}
