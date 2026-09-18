package gatewaydispatch

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestNormalizeOpenAIReasoningFieldsForUpstream(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		body  string
		check func(t *testing.T, got []byte)
	}{
		{
			name: "chat drops nested field when both wire shapes are present",
			path: "/v1/chat/completions",
			body: `{"model":"glm-5.3-flash","reasoning_effort":"high","reasoning":{"effort":"high","summary":"auto"}}`,
			check: func(t *testing.T, got []byte) {
				var body map[string]any
				if err := json.Unmarshal(got, &body); err != nil {
					t.Fatal(err)
				}
				if body["reasoning_effort"] != "high" {
					t.Fatalf("reasoning_effort = %#v", body["reasoning_effort"])
				}
				if _, ok := body["reasoning"]; ok {
					t.Fatalf("Chat body still contains reasoning: %s", got)
				}
			},
		},
		{
			name: "responses drops flat field when both wire shapes are present",
			path: "/v1/responses?stream=true",
			body: `{"model":"gpt-5.6","reasoning_effort":"low","reasoning":{"effort":"high","summary":"auto"}}`,
			check: func(t *testing.T, got []byte) {
				var body map[string]any
				if err := json.Unmarshal(got, &body); err != nil {
					t.Fatal(err)
				}
				if _, ok := body["reasoning_effort"]; ok {
					t.Fatalf("Responses body still contains reasoning_effort: %s", got)
				}
				reasoning, ok := body["reasoning"].(map[string]any)
				if !ok || reasoning["effort"] != "high" || reasoning["summary"] != "auto" {
					t.Fatalf("reasoning = %#v", body["reasoning"])
				}
			},
		},
		{
			name: "chat maps nested-only effort to flat field",
			path: "/chat/completions",
			body: `{"model":"glm-5.3-flash","reasoning":{"effort":"medium"}}`,
			check: func(t *testing.T, got []byte) {
				var body map[string]any
				if err := json.Unmarshal(got, &body); err != nil {
					t.Fatal(err)
				}
				if body["reasoning_effort"] != "medium" {
					t.Fatalf("reasoning_effort = %#v", body["reasoning_effort"])
				}
				if _, ok := body["reasoning"]; ok {
					t.Fatalf("Chat body still contains reasoning: %s", got)
				}
			},
		},
		{
			name: "responses keeps nested-only body byte-for-byte",
			path: "/v1/responses",
			body: `{"model":"gpt-5.6", "reasoning":{"effort":"high"}}`,
			check: func(t *testing.T, got []byte) {
				want := []byte(`{"model":"gpt-5.6", "reasoning":{"effort":"high"}}`)
				if !bytes.Equal(got, want) {
					t.Fatalf("body changed: got %s want %s", got, want)
				}
			},
		},
		{
			name: "non-openai path is untouched",
			path: "/v1/messages",
			body: `{"reasoning_effort":"high","reasoning":{"effort":"high"}}`,
			check: func(t *testing.T, got []byte) {
				want := []byte(`{"reasoning_effort":"high","reasoning":{"effort":"high"}}`)
				if !bytes.Equal(got, want) {
					t.Fatalf("body changed: got %s want %s", got, want)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeOpenAIReasoningFieldsForUpstream(tc.path, []byte(tc.body))
			tc.check(t, got)
		})
	}
}
