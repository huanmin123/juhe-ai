package gatewayopenai

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// B-4 audit B-10: openAIToAnthropicBridgeUpstreamPath — the openai chat /
// responses source mapped onto anthropic messages rewrites the upstream path
// to /messages + the client query.

func resolvedMapping(source, upstream string) *gatewayproto.ResolvedModelMapping {
	return &gatewayproto.ResolvedModelMapping{
		SourceModel:            "m",
		SourceEndpointFamily:   source,
		UpstreamModel:          "m-up",
		UpstreamEndpointFamily: upstream,
	}
}

func TestModelMappedUpstreamPathAnthropicMessagesTarget(t *testing.T) {
	cases := []struct {
		name    string
		source  string
		original string
		want    string
	}{
		{"chat source keeps query", FamilyChatCompletions, "/v1/chat/completions?beta=1", "/messages?beta=1"},
		{"responses source", FamilyResponses, "/v1/responses", "/messages"},
		{"no query", FamilyChatCompletions, "/chat/completions", "/messages"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := modelMappedUpstreamPathAndQuery(tc.original, resolvedMapping(tc.source, FamilyAnthropicMessages))
			if !ok || got != tc.want {
				t.Fatalf("got %q ok=%v want %q", got, ok, tc.want)
			}
		})
	}
	// stored row vocabulary ("messages") resolves the same rewrite.
	got, ok := modelMappedUpstreamPathAndQuery("/chat/completions?a=b", resolvedMapping(FamilyChatCompletions, "messages"))
	if !ok || got != "/messages?a=b" {
		t.Fatalf("stored vocabulary got %q ok=%v", got, ok)
	}
}

func TestModelMappedUpstreamPathNonBridgeUnchanged(t *testing.T) {
	// 同协议映射不重写。
	got, ok := modelMappedUpstreamPathAndQuery("/chat/completions", resolvedMapping(FamilyChatCompletions, FamilyChatCompletions))
	if ok || got != "/chat/completions" {
		t.Fatalf("same-family mapping rewrote: %q ok=%v", got, ok)
	}
	// chat -> gemini 原生不在该 helper 重写（bridge errors earlier）。
	got, ok = modelMappedUpstreamPathAndQuery("/v1/chat/completions?alt=sse&key=k", resolvedMapping(FamilyChatCompletions, FamilyGeminiGenerateContent))
	if ok || got != "/v1/chat/completions?alt=sse&key=k" {
		t.Fatalf("gemini native path should not rewrite here: %q ok=%v", got, ok)
	}
}

func TestModelMappedUpstreamPathChatTargetStillRewrites(t *testing.T) {
	got, ok := modelMappedUpstreamPathAndQuery("/v1/responses?x=1", resolvedMapping(FamilyResponses, FamilyChatCompletions))
	if !ok || got != "/chat/completions?x=1" {
		t.Fatalf("responses->chat got %q ok=%v", got, ok)
	}
}
