package modelcatalogsnapshotrebuild

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRebuildSendsNodeCompatiblePersonalRequest(t *testing.T) {
	var body, signature, path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		body = string(raw)
		signature = r.Header.Get("X-Juhe-AI-Signature")
		path = r.URL.Path
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Content-Encoding") != "identity" {
			t.Fatalf("request = %s %s headers=%v", r.Method, r.URL.Path, r.Header)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, " snapshot-secret ")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if err := client.Rebuild(t.Context(), "personal", " sys_user "); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}
	if path != rebuildPath || body != `{"scope":"personal","systemAccountId":"sys_user"}` {
		t.Fatalf("path/body = %q/%q", path, body)
	}
	if signature != "v1=5f389398abaca0a70210f56ca86f74fff94636712fa3939a4975d1d1bf678911" {
		t.Fatalf("signature = %q", signature)
	}
}

func TestRebuildSendsAllScopeWithoutOwner(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if string(raw) != `{"scope":"all"}` {
			t.Fatalf("body = %s", raw)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "snapshot-secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Rebuild(t.Context(), "all", ""); err != nil {
		t.Fatal(err)
	}
}

func TestRebuildRejectsInvalidScopeAndOwner(t *testing.T) {
	client, err := NewClient("http://127.0.0.1:3001", "snapshot-secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ scope, owner string }{{"personal", ""}, {"all", "sys_user"}, {"global", ""}} {
		if err := client.Rebuild(t.Context(), test.scope, test.owner); err == nil {
			t.Fatalf("Rebuild(%q, %q) error = nil", test.scope, test.owner)
		}
	}
}

func TestNewClientRejectsUnsafeBaseURLAndUnexpectedStatus(t *testing.T) {
	for _, raw := range []string{"", "https://127.0.0.1:3001", "http://localhost:3001", "http://10.0.0.1:3001", "http://127.0.0.1", "http://127.0.0.1:3001/path"} {
		if _, err := NewClient(raw, "snapshot-secret"); err == nil {
			t.Fatalf("NewClient(%q) error = nil", raw)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("secret response"))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "snapshot-secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Rebuild(t.Context(), "all", ""); err == nil {
		t.Fatal("unexpected status error = nil")
	}
}

func TestNewClientWithTimeoutsUseIndependentBounds(t *testing.T) {
	if _, err := NewClientWithTimeouts("http://127.0.0.1:3001", "snapshot-secret", 60*time.Second, 2*time.Second); err != nil {
		t.Fatalf("NewClientWithTimeouts(60s, 2s) error = %v", err)
	}
	for _, test := range []struct {
		rebuild time.Duration
		probe   time.Duration
	}{
		{time.Second - time.Nanosecond, 2 * time.Second},
		{5*time.Minute + time.Nanosecond, 2 * time.Second},
		{time.Minute, 100*time.Millisecond - time.Nanosecond},
		{time.Minute, 10*time.Second + time.Nanosecond},
	} {
		if _, err := NewClientWithTimeouts("http://127.0.0.1:3001", "snapshot-secret", test.rebuild, test.probe); err == nil {
			t.Fatalf("NewClientWithTimeouts(%s, %s) error = nil", test.rebuild, test.probe)
		}
	}
}

func TestProbeSendsAuthenticatedReadOnlyRequestAndValidatesContract(t *testing.T) {
	var method, path, requestSignature string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		requestSignature = r.Header.Get("X-Juhe-AI-Signature")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"ready":true,"component":"model-catalog-snapshot-rebuild","contractVersion":1,"databaseDriver":"postgres","schemaVersion":67}`))
	}))
	defer server.Close()

	client, err := NewClientWithTimeouts(server.URL, "snapshot-secret", time.Minute, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Probe(t.Context()); err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	if method != http.MethodGet || path != readinessPath {
		t.Fatalf("request = %s %s", method, path)
	}
	if requestSignature != "v1=29ff4948f16ba2cdbfede5ae852914cda14a4ce7aef2f9e179ea45c6d412c7c4" {
		t.Fatalf("signature = %q", requestSignature)
	}
}

func TestProbeClassifiesFailuresWithoutLeakingResponseOrEndpoint(t *testing.T) {
	tests := []struct {
		name        string
		statusCode  int
		body        string
		contentType string
		want        ProbeFailureKind
	}{
		{name: "unauthorized", statusCode: http.StatusUnauthorized, body: `{"message":"secret marker"}`, want: ProbeFailureUnauthorized},
		{name: "route missing", statusCode: http.StatusNotFound, want: ProbeFailureNotFound},
		{name: "node dependency", statusCode: http.StatusServiceUnavailable, body: `{"code":"private marker"}`, want: ProbeFailureDependencyUnavailable},
		{name: "other status", statusCode: http.StatusBadGateway, want: ProbeFailureHTTPStatus},
		{name: "wrong content type", statusCode: http.StatusOK, body: `{}`, contentType: "text/plain", want: ProbeFailureInvalidResponse},
		{name: "invalid json", statusCode: http.StatusOK, body: `{`, want: ProbeFailureInvalidResponse},
		{name: "wrong owner contract", statusCode: http.StatusOK, body: `{"ready":true,"component":"other","contractVersion":1,"databaseDriver":"postgres","schemaVersion":67}`, want: ProbeFailureInvalidResponse},
		{name: "wrong schema", statusCode: http.StatusOK, body: `{"ready":true,"component":"model-catalog-snapshot-rebuild","contractVersion":1,"databaseDriver":"postgres","schemaVersion":62}`, want: ProbeFailureInvalidResponse},
		{name: "unknown field", statusCode: http.StatusOK, body: `{"ready":true,"component":"model-catalog-snapshot-rebuild","contractVersion":1,"databaseDriver":"postgres","schemaVersion":67,"extra":true}`, want: ProbeFailureInvalidResponse},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if test.contentType != "" {
					w.Header().Set("Content-Type", test.contentType)
				} else {
					w.Header().Set("Content-Type", "application/json")
				}
				w.WriteHeader(test.statusCode)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			client, err := NewClientWithTimeouts(server.URL, "snapshot-secret", time.Minute, 2*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			err = client.Probe(t.Context())
			var probeError *ProbeError
			if !errors.As(err, &probeError) || probeError.Kind != test.want {
				t.Fatalf("Probe() error = %#v, want kind %q", err, test.want)
			}
			if strings.Contains(err.Error(), "marker") || strings.Contains(err.Error(), server.URL) {
				t.Fatalf("Probe() leaked internal detail: %q", err)
			}
		})
	}
}

func TestProbeRejectsRedirectOversizedResponseAndTimeout(t *testing.T) {
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ready":true,"component":"model-catalog-snapshot-rebuild","contractVersion":1,"databaseDriver":"postgres","schemaVersion":67}`))
	}))
	defer redirectTarget.Close()

	for _, test := range []struct {
		name    string
		handler http.HandlerFunc
		kind    ProbeFailureKind
		timeout time.Duration
	}{
		{name: "redirect", kind: ProbeFailureHTTPStatus, timeout: time.Second, handler: func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, redirectTarget.URL, http.StatusFound)
		}},
		{name: "oversized", kind: ProbeFailureInvalidResponse, timeout: time.Second, handler: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(strings.Repeat("x", 4097)))
		}},
		{name: "timeout", kind: ProbeFailureUnreachable, timeout: 100 * time.Millisecond, handler: func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(200 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(test.handler)
			defer server.Close()
			client, err := NewClientWithTimeouts(server.URL, "snapshot-secret", time.Minute, test.timeout)
			if err != nil {
				t.Fatal(err)
			}
			err = client.Probe(t.Context())
			var probeError *ProbeError
			if !errors.As(err, &probeError) || probeError.Kind != test.kind {
				t.Fatalf("Probe() error = %#v, want kind %q", err, test.kind)
			}
		})
	}
}

func TestProbeUsesCurrentGooseSchemaVersion(t *testing.T) {
	source, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if !strings.Contains(text, "result.SchemaVersion != version.SchemaVersion") {
		t.Fatal("Probe must validate schema with version.SchemaVersion")
	}
	if strings.Contains(text, "result.SchemaVersion != 63") {
		t.Fatal("Probe must not hard-code Goose schema version")
	}
}
