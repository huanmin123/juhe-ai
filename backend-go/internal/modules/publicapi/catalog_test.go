package publicapi

import (
	"net/http"
	"testing"
)

func TestEndpointsFreezeW1bCatalog(t *testing.T) {
	if Prefix != "/__aipublic__" {
		t.Fatalf("Prefix = %q", Prefix)
	}
	if AuthTypeBearer != "Bearer" {
		t.Fatalf("AuthTypeBearer = %q", AuthTypeBearer)
	}
	if JSONBodyLimitBytes != 256*1024 {
		t.Fatalf("JSONBodyLimitBytes = %d", JSONBodyLimitBytes)
	}
	if TokenValuePrefix != "juis_" {
		t.Fatalf("TokenValuePrefix = %q", TokenValuePrefix)
	}
	if BuiltInTestRateLimitWindowSeconds != 60 || BuiltInTestRateLimitMaxRequests != 10 {
		t.Fatalf("built-in test rate limit = %ds/%d", BuiltInTestRateLimitWindowSeconds, BuiltInTestRateLimitMaxRequests)
	}

	endpoints := Endpoints()
	if got, want := len(endpoints), 16; got != want {
		t.Fatalf("len(Endpoints()) = %d, want %d", got, want)
	}

	want := []Endpoint{
		{ID: "api-key-list", Method: http.MethodGet, Path: "/__aipublic__/api-key/list", Scope: ScopeAPIKeyListRead},
		{ID: "api-key-add", Method: http.MethodPost, Path: "/__aipublic__/api-key/add", Scope: ScopeAPIKeyAddWrite},
		{ID: "api-key-update", Method: http.MethodPost, Path: "/__aipublic__/api-key/update", Scope: ScopeAPIKeyUpdateWrite},
		{ID: "api-key-delete", Method: http.MethodPost, Path: "/__aipublic__/api-key/del", Scope: ScopeAPIKeyDeleteWrite},
		{ID: "route-strategy-list", Method: http.MethodGet, Path: "/__aipublic__/route-strategy/list", Scope: ScopeRouteStrategyListRead},
		{ID: "route-strategy-add", Method: http.MethodPost, Path: "/__aipublic__/route-strategy/add", Scope: ScopeRouteStrategyAddWrite},
		{ID: "route-strategy-update", Method: http.MethodPost, Path: "/__aipublic__/route-strategy/update", Scope: ScopeRouteStrategyUpdateWrite},
		{ID: "route-strategy-delete", Method: http.MethodPost, Path: "/__aipublic__/route-strategy/del", Scope: ScopeRouteStrategyDeleteWrite},
		{ID: "group-list", Method: http.MethodGet, Path: "/__aipublic__/group/list", Scope: ScopeGroupListRead},
		{ID: "group-add", Method: http.MethodPost, Path: "/__aipublic__/group/add", Scope: ScopeGroupAddWrite},
		{ID: "group-update", Method: http.MethodPost, Path: "/__aipublic__/group/update", Scope: ScopeGroupUpdateWrite},
		{ID: "group-delete", Method: http.MethodPost, Path: "/__aipublic__/group/del", Scope: ScopeGroupDeleteWrite},
		{ID: "account-list", Method: http.MethodGet, Path: "/__aipublic__/account/list", Scope: ScopeAccountListRead},
		{ID: "account-add", Method: http.MethodPost, Path: "/__aipublic__/account/add", Scope: ScopeAccountAddWrite},
		{ID: "account-update", Method: http.MethodPost, Path: "/__aipublic__/account/update", Scope: ScopeAccountUpdateWrite},
		{ID: "account-delete", Method: http.MethodPost, Path: "/__aipublic__/account/del", Scope: ScopeAccountDeleteWrite},
	}

	seenPaths := map[string]struct{}{}
	seenScopes := map[string]struct{}{}
	for i, endpoint := range endpoints {
		if endpoint != want[i] {
			t.Fatalf("endpoint[%d] = %+v, want %+v", i, endpoint, want[i])
		}
		key := endpoint.Method + " " + endpoint.Path
		if _, ok := seenPaths[key]; ok {
			t.Fatalf("duplicate endpoint path: %s", key)
		}
		seenPaths[key] = struct{}{}
		if _, ok := seenScopes[endpoint.Scope]; ok {
			t.Fatalf("duplicate endpoint scope: %s", endpoint.Scope)
		}
		seenScopes[endpoint.Scope] = struct{}{}
	}
}

func TestFindEndpointRejectsRemovedPublicPaths(t *testing.T) {
	removed := []string{
		"/__aipublic__/demo/source-auth",
		"/__aipublic__/ip/usage",
		"/__aipublic__/account/usage",
		"/__aipublic__/consumption/ranking",
		"/__aipublic__/access/info",
	}
	for _, path := range removed {
		if endpoint, ok := FindEndpoint(http.MethodGet, path); ok {
			t.Fatalf("FindEndpoint(%q) = %+v, want not found", path, endpoint)
		}
	}
}

func TestEndpointsReturnsCopy(t *testing.T) {
	endpoints := Endpoints()
	endpoints[0].Path = "/mutated"

	fresh := Endpoints()
	if fresh[0].Path == "/mutated" {
		t.Fatal("Endpoints() returned mutable package backing slice")
	}
}
