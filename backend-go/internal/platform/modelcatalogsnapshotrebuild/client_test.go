package modelcatalogsnapshotrebuild

import (
	"io"
	"net/http"
	"net/http/httptest"
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

func TestNewClientWithTimeoutUsesSnapshotSpecificBounds(t *testing.T) {
	if _, err := NewClientWithTimeout("http://127.0.0.1:3001", "snapshot-secret", 60*time.Second); err != nil {
		t.Fatalf("NewClientWithTimeout(60s) error = %v", err)
	}
	for _, timeout := range []time.Duration{time.Second - time.Nanosecond, 5*time.Minute + time.Nanosecond} {
		if _, err := NewClientWithTimeout("http://127.0.0.1:3001", "snapshot-secret", timeout); err == nil {
			t.Fatalf("NewClientWithTimeout(%s) error = nil", timeout)
		}
	}
}
