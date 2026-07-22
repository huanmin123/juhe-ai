package gateway

import "testing"

func TestResolveRequestLane(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		request RequestShape
		want    RequestLane
	}{
		{name: "image endpoint", request: RequestShape{Path: "/v1/images/generations"}, want: RequestLaneImage},
		{name: "gpt image model", request: RequestShape{Path: "/v1/responses", Model: " GPT-IMAGE-1 "}, want: RequestLaneImage},
		{name: "dall e model", request: RequestShape{Path: "/chat/completions", Model: "dall-e-3"}, want: RequestLaneImage},
		{name: "body hint", request: RequestShape{Path: "/responses", ImageGenerationHint: true}, want: RequestLaneImage},
		{name: "text", request: RequestShape{Path: "/responses", Model: "gpt-5"}, want: RequestLaneText},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ResolveRequestLane(tt.request); got != tt.want {
				t.Fatalf("ResolveRequestLane() = %q, want %q", got, tt.want)
			}
		})
	}
}
