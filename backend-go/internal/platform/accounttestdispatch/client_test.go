package accounttestdispatch

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientDispatchesSignedTaskID(t *testing.T) {
	var gotPath, gotSignature, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotSignature = r.Header.Get("X-Juhe-AI-Signature")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	client, err := NewClient(strings.Replace(server.URL, "localhost", "127.0.0.1", 1), "bridge-secret")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if err := client.Dispatch(context.Background(), " accttest_1 "); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if gotPath != "/__aiinternal__/v1/account-test/dispatch" || gotBody != `{"version":1,"taskId":"accttest_1"}` {
		t.Fatalf("path=%q body=%q", gotPath, gotBody)
	}
	if !strings.HasPrefix(gotSignature, "v1=") || len(gotSignature) != 67 {
		t.Fatalf("signature = %q", gotSignature)
	}
}

func TestClientRejectsUnsafeBaseURLAndUnexpectedStatus(t *testing.T) {
	if _, err := NewClient("http://example.com:3000", "bridge-secret"); err == nil {
		t.Fatal("NewClient() error = nil, want loopback rejection")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "bridge-secret")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if err := client.Dispatch(context.Background(), "accttest_2"); err == nil {
		t.Fatal("Dispatch() error = nil, want status rejection")
	}
}
