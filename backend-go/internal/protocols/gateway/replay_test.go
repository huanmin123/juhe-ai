package gateway

import "testing"

func TestClassifyReplayUsesExactMethodAndOperation(t *testing.T) {
	storeFalse := false
	storeTrue := true
	tests := []struct {
		name    string
		request RequestShape
		profile *Profile
		allowed bool
		class   ReplayClass
	}{
		{name: "safe get", request: RequestShape{Method: " get ", Path: "/v1/models"}, allowed: true, class: ReplaySafeRead},
		{name: "openai inference", request: RequestShape{Method: "POST", Path: "/v1/responses?stream=true", StoreRequested: &storeFalse}, allowed: true, class: ReplayInference},
		{name: "chat inference", request: RequestShape{Method: "POST", Path: "/v1/chat/completions", StoreRequested: &storeFalse}, allowed: true, class: ReplayInference},
		{name: "unparsed chat store", request: RequestShape{Method: "POST", Path: "/v1/chat/completions"}, class: ReplayResourceMutation},
		{name: "stored chat", request: RequestShape{Method: "POST", Path: "/v1/chat/completions", StoreRequested: &storeTrue}, class: ReplayResourceMutation},
		{name: "unparsed responses store", request: RequestShape{Method: "POST", Path: "/v1/responses"}, class: ReplayResourceMutation},
		{name: "stored response", request: RequestShape{Method: "POST", Path: "/v1/responses", StoreRequested: &storeTrue}, class: ReplayResourceMutation},
		{name: "anthropic inference", request: RequestShape{Method: "POST", Path: "/v1/messages"}, allowed: true, class: ReplayInference},
		{name: "gemini inference", request: RequestShape{Method: "POST", Path: "/v1beta/models/gemini-2.5-pro:streamGenerateContent?alt=sse"}, allowed: true, class: ReplayInference},
		{name: "image generation", request: RequestShape{Method: "POST", Path: "/v1/images/generations"}, allowed: true, class: ReplayInference},
		{name: "interaction create", request: RequestShape{Method: "POST", Path: "/v1beta/interactions"}, class: ReplayResourceMutation},
		{name: "interaction resource read", request: RequestShape{Method: "GET", Path: "/v1beta/interactions/id"}, class: ReplayUnknown},
		{name: "interaction cancel", request: RequestShape{Method: "POST", Path: "/v1beta/interactions/id/cancel"}, class: ReplayResourceMutation},
		{name: "response resource", request: RequestShape{Method: "POST", Path: "/v1/responses/id/cancel"}, class: ReplayResourceMutation},
		{name: "response resource read", request: RequestShape{Method: "GET", Path: "/v1/responses/id"}, class: ReplayUnknown},
		{name: "compact local owner", request: RequestShape{Method: "POST", Path: "/v1/responses/compact"}, class: ReplayResourceMutation},
		{name: "similar path", request: RequestShape{Method: "POST", Path: "/v1/responses-evil"}, class: ReplayUnknown},
		{name: "unknown get", request: RequestShape{Method: "GET", Path: "/custom"}, class: ReplayUnknown},
		{name: "profile fallback custom", request: RequestShape{Method: "POST", Path: "/custom"}, profile: &Profile{Code: "openai", Version: "v1"}, class: ReplayUnknown},
		{name: "delete", request: RequestShape{Method: "DELETE", Path: "/v1/responses/id"}, class: ReplayUnknown},
		{name: "missing method", request: RequestShape{Path: "/v1/responses"}, class: ReplayUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyReplay(tt.request, tt.profile)
			if got.Allowed != tt.allowed || got.Class != tt.class {
				t.Fatalf("ClassifyReplay() = %+v", got)
			}
		})
	}
}
