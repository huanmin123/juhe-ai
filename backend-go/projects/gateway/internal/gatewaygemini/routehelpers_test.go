package gatewaygemini

import (
	"net/http/httptest"
	"testing"
)

// BUG-0174 B-5：interactions 族匹配对齐 gemini-endpoint-modes.ts:120，
// 覆盖裸 /interactions（create）、/interactions/{id}、/interactions/{id}/cancel
// 三形态；affinity resource 匹配仍要求 id 非空。
func TestEndpointFamilyFromPathInteractionsForms(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		family EndpointFamily
	}{
		{"bare create", "/interactions", EndpointFamilyInteractions},
		{"bare create with v1beta", "/v1beta/interactions", EndpointFamilyInteractions},
		{"with id", "/interactions/abc_123", EndpointFamilyInteractions},
		{"with id and v1beta", "/v1beta/interactions/abc_123", EndpointFamilyInteractions},
		{"cancel", "/interactions/abc_123/cancel", EndpointFamilyInteractions},
		{"cancel with v1beta", "/v1beta/interactions/abc_123/cancel", EndpointFamilyInteractions},
		{"case insensitive", "/Interactions/abc", EndpointFamilyInteractions},
		{"prefix must not match", "/interactionsX", ""},
		{"trailing slash must not match", "/interactions/", ""},
		{"empty id must not match", "/interactions//cancel", ""},
		{"models unaffected", "/models", EndpointFamilyModels},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if family := EndpointFamilyFromPath(tc.path); family != tc.family {
				t.Fatalf("EndpointFamilyFromPath(%q) = %q, want %q", tc.path, family, tc.family)
			}
		})
	}
}

func TestIsNativeRequestInteractionsForms(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		native bool
	}{
		{"POST bare create", "POST", "/v1beta/interactions", true},
		{"GET bare interactions family is native", "GET", "/v1beta/interactions", true},
		{"GET with id", "GET", "/v1beta/interactions/abc", true},
		{"DELETE with id", "DELETE", "/v1beta/interactions/abc", true},
		{"POST cancel", "POST", "/v1beta/interactions/abc/cancel", true},
		{"PATCH cancel must not be native", "PATCH", "/v1beta/interactions/abc/cancel", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.path, nil)
			if got := IsNativeRequest(request); got != tc.native {
				t.Fatalf("IsNativeRequest(%s %s) = %v, want %v", tc.method, tc.path, got, tc.native)
			}
		})
	}
}

func TestEndpointModeForRequestShapeInteractions(t *testing.T) {
	if mode := EndpointModeForRequestShape("/v1beta/interactions", false); mode != EndpointModeInteractionsJSON {
		t.Fatalf("bare create JSON mode = %q, want %q", mode, EndpointModeInteractionsJSON)
	}
	if mode := EndpointModeForRequestShape("/v1beta/interactions", true); mode != EndpointModeInteractionsSSE {
		t.Fatalf("bare create SSE mode = %q, want %q", mode, EndpointModeInteractionsSSE)
	}
	if mode := EndpointModeForRequestShape("/v1beta/interactions/abc/cancel", false); mode != EndpointModeInteractionsJSON {
		t.Fatalf("cancel mode = %q, want %q", mode, EndpointModeInteractionsJSON)
	}
}

func TestBuildUpstreamURLInteractionsForms(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{"bare create", "/interactions", "https://generativelanguage.googleapis.com/v1beta/interactions"},
		{"with id", "/interactions/abc", "https://generativelanguage.googleapis.com/v1beta/interactions/abc"},
		{"cancel", "/interactions/abc/cancel", "https://generativelanguage.googleapis.com/v1beta/interactions/abc/cancel"},
		{"alt dropped", "/interactions?alt=sse&key=secret", "https://generativelanguage.googleapis.com/v1beta/interactions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			built, err := BuildUpstreamURL("", tc.path, false)
			if err != nil {
				t.Fatalf("BuildUpstreamURL(%q) error: %v", tc.path, err)
			}
			if built != tc.want {
				t.Fatalf("BuildUpstreamURL(%q) = %q, want %q", tc.path, built, tc.want)
			}
		})
	}
	built, err := BuildUpstreamURL("", "/interactions", true)
	if err != nil {
		t.Fatalf("BuildUpstreamURL stream error: %v", err)
	}
	if built != "https://generativelanguage.googleapis.com/v1beta/interactions?alt=sse" {
		t.Fatalf("stream create url = %q", built)
	}
}

// 裸 create 路径不得被 affinity 资源匹配误收（捕获组必须非空）。
func TestAffinityResourceMatchIgnoresBareCreate(t *testing.T) {
	create := httptest.NewRequest("POST", "/v1beta/interactions", nil)
	if id := ResourceIDFromRequest(create); id != "" {
		t.Fatalf("ResourceIDFromRequest(create) = %q, want empty", id)
	}
	if IsInteractionResourceRequest(create) {
		t.Fatal("IsInteractionResourceRequest(create) = true, want false")
	}
	resource := httptest.NewRequest("GET", "/v1beta/interactions/abc", nil)
	if id := ResourceIDFromRequest(resource); id != "abc" {
		t.Fatalf("ResourceIDFromRequest(resource) = %q, want abc", id)
	}
}
