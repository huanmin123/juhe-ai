package gateway

import "testing"

func TestOpenAIOperationFromPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path string
		want OpenAIOperation
	}{
		{path: "/v1/chat/completions?trace=1", want: OpenAIOperationChatCompletionsCreate},
		{path: "/CHAT/COMPLETIONS", want: OpenAIOperationChatCompletionsCreate},
		{path: "/v1/chat/completions/", want: OpenAIOperationChatCompletionsCreate},
		{path: "/v1/responses", want: OpenAIOperationResponsesCreate},
		{path: "/RESPONSES", want: OpenAIOperationResponsesCreate},
		{path: "/responses/", want: OpenAIOperationResponsesCreate},
		{path: "/v1/responses/compact", want: OpenAIOperationResponsesCompact},
		{path: "/responses/replay", want: OpenAIOperationUnknown},
		{path: "/internal/responses", want: OpenAIOperationUnknown},
		{path: "/v1/%72esponses", want: OpenAIOperationUnknown},
		{path: "/v1/other/../responses", want: OpenAIOperationUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			if got := OpenAIOperationFromPath(tt.path); got != tt.want {
				t.Fatalf("OpenAIOperationFromPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}
