package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/modules/gatewayclientcatalog"
	"juhe-ai/backend-go/internal/modules/gatewayerrors"
	"juhe-ai/backend-go/internal/store/port"
)

func TestGatewayModelsHandlerServesPublicOpenAIModelsWithoutPreflight(t *testing.T) {
	catalog := &gatewayModelsCatalogStub{publicItems: []port.GatewayClientCatalogModel{{Model: "gpt-5.6", Scope: "built_in"}}}
	authorizer := &gatewayModelsAuthorizerStub{}
	handler := newGatewayModelsHandler(GatewayModelsHandlerOptions{Authorizer: authorizer, Catalog: catalog})

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/models", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	assertGatewayModelsJSON(t, recorder.Body.Bytes(), `{"object":"list","data":[{"id":"gpt-5.6","object":"model","created":0,"owned_by":"openai"}]}`)
	if authorizer.calls != nil {
		t.Fatalf("authorizer calls = %#v, want none", authorizer.calls)
	}
	if catalog.publicCalls != 1 || len(catalog.apiKeyCalls) != 0 {
		t.Fatalf("catalog calls = public:%d apiKey:%#v", catalog.publicCalls, catalog.apiKeyCalls)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "" {
		t.Fatalf("public Cache-Control = %q, want empty", got)
	}
}

func TestGatewayModelsHandlerUsesCanonicalCredentialAndAPIKeyScope(t *testing.T) {
	catalog := &gatewayModelsCatalogStub{apiKeyItems: []port.GatewayClientCatalogModel{{Model: "gpt-5.6-sol", Scope: "built_in"}}}
	authorizer := &gatewayModelsAuthorizerStub{result: GatewayModelsAPIKeyScope{
		SystemAccountID: "sys-1",
		ProviderCodes:   []string{"gpt", "anthropic"},
	}}
	handler := newGatewayModelsHandler(GatewayModelsHandlerOptions{Authorizer: authorizer, Catalog: catalog})
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer sk-secret")

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	if !reflect.DeepEqual(authorizer.calls, []string{"sk-secret"}) {
		t.Fatalf("authorizer calls = %#v", authorizer.calls)
	}
	wantInput := GatewayModelsAPIKeyScope{SystemAccountID: "sys-1", ProviderCodes: []string{"gpt", "anthropic"}}
	if !reflect.DeepEqual(catalog.apiKeyCalls, []GatewayModelsAPIKeyScope{wantInput}) {
		t.Fatalf("catalog api key calls = %#v, want %#v", catalog.apiKeyCalls, []GatewayModelsAPIKeyScope{wantInput})
	}
	if got := recorder.Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Fatalf("Cache-Control = %q", got)
	}
	for _, name := range []string{"Authorization", "X-API-Key", "X-Goog-API-Key", "X-Juhe-Client-Profile", "Anthropic-Version", "Anthropic-Beta", "X-Claude-Code-Session-Id", "X-Claude-Code-Agent-Id", "Originator", "User-Agent", "X-Codex-Client", "X-Codex-Client-Version"} {
		if !varyContains(recorder.Header().Get("Vary"), name) {
			t.Fatalf("Vary = %q, missing %q", recorder.Header().Get("Vary"), name)
		}
	}
	if strings.Contains(recorder.Body.String(), "sk-secret") {
		t.Fatal("response leaked API key")
	}
}

func TestGatewayModelsHandlerAllowsValidAPIKeyWithNoActiveBindings(t *testing.T) {
	catalog := &gatewayModelsCatalogStub{apiKeyItems: []port.GatewayClientCatalogModel{}}
	authorizer := &gatewayModelsAuthorizerStub{result: GatewayModelsAPIKeyScope{SystemAccountID: "sys-empty"}}
	handler := newGatewayModelsHandler(GatewayModelsHandlerOptions{Authorizer: authorizer, Catalog: catalog})
	request := httptest.NewRequest(http.MethodGet, "/models", nil)
	request.Header.Set("X-API-Key", "sk-valid-without-bindings")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	assertGatewayModelsJSON(t, recorder.Body.Bytes(), `{"object":"list","data":[]}`)
	if catalog.publicCalls != 0 || len(catalog.apiKeyCalls) != 1 || len(catalog.apiKeyCalls[0].ProviderCodes) != 0 {
		t.Fatalf("catalog calls = public:%d apiKey:%#v", catalog.publicCalls, catalog.apiKeyCalls)
	}
}

func TestGatewayModelsCatalogAdapterConvertsAuthorizedProviderCodes(t *testing.T) {
	reader := &gatewayModelsReaderStub{}
	catalog := NewGatewayModelsCatalog(gatewayclientcatalog.NewService(reader))

	_, err := catalog.APIKey(context.Background(), GatewayModelsAPIKeyScope{
		SystemAccountID: "sys-1",
		ProviderCodes:   []string{" anthropic ", "gpt", "gpt"},
	})

	if err != nil {
		t.Fatalf("APIKey() error = %v", err)
	}
	want := []port.GatewayClientCatalogModelListInput{{
		LogicalProviderCodes: []string{"anthropic", "gpt"},
		SystemAccountID:      "sys-1",
	}}
	if !reflect.DeepEqual(reader.modelInputs, want) {
		t.Fatalf("model inputs = %#v, want %#v", reader.modelInputs, want)
	}
}

func TestGatewayModelsHandlerRendersNativeProtocolShapes(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		headers    map[string]string
		wantTopKey string
		wantNested string
	}{
		{name: "codex", path: "/models?client_version=1", wantTopKey: "models", wantNested: "slug"},
		{name: "anthropic", path: "/v1/models", headers: map[string]string{"X-Juhe-Client-Profile": "claude-code"}, wantTopKey: "data", wantNested: "type"},
		{name: "gemini", path: "/v1beta/models", wantTopKey: "models", wantNested: "name"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			catalog := &gatewayModelsCatalogStub{publicItems: []port.GatewayClientCatalogModel{{Model: "model-1", Scope: "built_in"}}}
			handler := newGatewayModelsHandler(GatewayModelsHandlerOptions{Catalog: catalog})
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			for name, value := range test.headers {
				request.Header.Set(name, value)
			}
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			items, ok := body[test.wantTopKey].([]any)
			if !ok || len(items) != 1 {
				t.Fatalf("response = %#v, want one %s item", body, test.wantTopKey)
			}
			item, ok := items[0].(map[string]any)
			if !ok || item[test.wantNested] == nil {
				t.Fatalf("response item = %#v, missing %s", items[0], test.wantNested)
			}
		})
	}
}

func TestGatewayModelsHandlerNeverDowngradesPresentedInvalidCredentialToPublic(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*http.Request)
	}{
		{
			name: "malformed higher priority authorization",
			prepare: func(request *http.Request) {
				request.Header.Set("Authorization", "Basic sk-wrong-scheme")
				request.Header.Set("X-API-Key", "sk-valid-lower-priority")
			},
		},
		{
			name: "ambiguous repeated API key",
			prepare: func(request *http.Request) {
				request.Header.Add("X-API-Key", "sk-one")
				request.Header.Add("X-API-Key", "sk-two")
			},
		},
		{
			name: "ineligible Gemini header is still presented",
			prepare: func(request *http.Request) {
				request.Header.Set("X-Goog-API-Key", "sk-gemini-on-openai-path")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			catalog := &gatewayModelsCatalogStub{}
			authorizer := &gatewayModelsAuthorizerStub{}
			handler := newGatewayModelsHandler(GatewayModelsHandlerOptions{Authorizer: authorizer, Catalog: catalog})
			request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			test.prepare(request)
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body=%s", recorder.Code, recorder.Body.String())
			}
			if catalog.publicCalls != 0 || catalog.apiKeyCalls != nil || authorizer.calls != nil {
				t.Fatalf("invalid credential reached services: public=%d api=%#v authorizer=%#v", catalog.publicCalls, catalog.apiKeyCalls, authorizer.calls)
			}
			if !strings.Contains(recorder.Body.String(), `"code":"invalid_api_key"`) {
				t.Fatalf("body = %s, want unified invalid_api_key", recorder.Body.String())
			}
		})
	}
}

func TestGatewayModelsHandlerUsesUnifiedProtocolAuthenticationErrors(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		headers    map[string]string
		wantMarker string
	}{
		{name: "openai", path: "/v1/models", wantMarker: `"type":"invalid_request_error"`},
		{name: "anthropic", path: "/v1/models", headers: map[string]string{"Anthropic-Version": "2023-06-01"}, wantMarker: `"type":"authentication_error"`},
		{name: "gemini", path: "/v1beta/models", wantMarker: `"status":"UNAUTHENTICATED"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authorizer := &gatewayModelsAuthorizerStub{err: gatewayerrors.ErrAPIKeyDisabled}
			handler := newGatewayModelsHandler(GatewayModelsHandlerOptions{Authorizer: authorizer, Catalog: &gatewayModelsCatalogStub{}})
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			request.Header.Set("X-API-Key", "sk-disabled")
			for name, value := range test.headers {
				request.Header.Set(name, value)
			}
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), test.wantMarker) {
				t.Fatalf("status/body = %d %s, want marker %s", recorder.Code, recorder.Body.String(), test.wantMarker)
			}
		})
	}
}

func TestGatewayModelsHandlerHidesDependencyErrors(t *testing.T) {
	secretFailure := errors.New("postgres password=do-not-leak")
	tests := []struct {
		name       string
		authorizer *gatewayModelsAuthorizerStub
		catalog    *gatewayModelsCatalogStub
		withAPIKey bool
	}{
		{name: "authorizer", authorizer: &gatewayModelsAuthorizerStub{err: secretFailure}, catalog: &gatewayModelsCatalogStub{}, withAPIKey: true},
		{name: "catalog", authorizer: &gatewayModelsAuthorizerStub{}, catalog: &gatewayModelsCatalogStub{publicErr: secretFailure}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := newGatewayModelsHandler(GatewayModelsHandlerOptions{Authorizer: test.authorizer, Catalog: test.catalog})
			request := httptest.NewRequest(http.MethodGet, "/models", nil)
			if test.withAPIKey {
				request.Header.Set("X-API-Key", "sk-key")
			}
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503; body=%s", recorder.Code, recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), "password") || !strings.Contains(recorder.Body.String(), `"code":"service_unavailable"`) {
				t.Fatalf("unsafe body = %s", recorder.Body.String())
			}
		})
	}
}

func TestGatewayModelsHandlerIsGETOnly(t *testing.T) {
	handler := newGatewayModelsHandler(GatewayModelsHandlerOptions{Catalog: &gatewayModelsCatalogStub{}})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(`{}`)))

	if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("status/Allow = %d %q, want 405 GET", recorder.Code, recorder.Header().Get("Allow"))
	}
}

type gatewayModelsAuthorizerStub struct {
	result GatewayModelsAPIKeyScope
	err    error
	calls  []string
}

func (s *gatewayModelsAuthorizerStub) AuthorizeGatewayModels(_ context.Context, rawAPIKey string) (GatewayModelsAPIKeyScope, error) {
	s.calls = append(s.calls, rawAPIKey)
	return s.result, s.err
}

type gatewayModelsCatalogStub struct {
	publicItems []port.GatewayClientCatalogModel
	apiKeyItems []port.GatewayClientCatalogModel
	publicErr   error
	apiKeyErr   error
	publicCalls int
	apiKeyCalls []GatewayModelsAPIKeyScope
}

type gatewayModelsReaderStub struct {
	modelInputs []port.GatewayClientCatalogModelListInput
}

func (*gatewayModelsReaderStub) ListGatewayClientCatalogProviders(context.Context) ([]port.GatewayClientCatalogProvider, error) {
	return nil, nil
}

func (s *gatewayModelsReaderStub) ListGatewayClientCatalogModels(_ context.Context, input port.GatewayClientCatalogModelListInput) ([]port.GatewayClientCatalogModel, error) {
	s.modelInputs = append(s.modelInputs, input)
	return nil, nil
}

func (s *gatewayModelsCatalogStub) Public(context.Context) ([]port.GatewayClientCatalogModel, error) {
	s.publicCalls++
	return append([]port.GatewayClientCatalogModel(nil), s.publicItems...), s.publicErr
}

func (s *gatewayModelsCatalogStub) APIKey(_ context.Context, input GatewayModelsAPIKeyScope) ([]port.GatewayClientCatalogModel, error) {
	s.apiKeyCalls = append(s.apiKeyCalls, input)
	return append([]port.GatewayClientCatalogModel(nil), s.apiKeyItems...), s.apiKeyErr
}

func assertGatewayModelsJSON(t *testing.T, got []byte, want string) {
	t.Helper()
	var gotValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode got JSON: %v", err)
	}
	var wantValue any
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("decode want JSON: %v", err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("JSON = %#v, want %#v", gotValue, wantValue)
	}
}

func varyContains(value, want string) bool {
	for _, item := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(item), want) {
			return true
		}
	}
	return false
}
