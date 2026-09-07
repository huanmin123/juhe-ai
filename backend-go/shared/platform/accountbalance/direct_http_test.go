package accountbalance

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"
)

type balanceJSONResponseClient struct {
	body []byte
}

func (c balanceJSONResponseClient) Do(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(c.body)),
		Header:     make(http.Header),
	}, nil
}

func TestBalanceHTTPClientUsesSharedDirectTransport(t *testing.T) {
	doer, err := balanceHTTPClient(QueryOptions{}, nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client, ok := doer.(*http.Client)
	if !ok {
		t.Fatalf("client type=%T", doer)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type=%T", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("balance direct transport must not consult environment proxy settings")
	}
	if !transport.ForceAttemptHTTP2 {
		t.Fatal("balance direct transport must enable HTTP/2")
	}
	if transport.ResponseHeaderTimeout != time.Second {
		t.Fatalf("response header timeout=%s", transport.ResponseHeaderTimeout)
	}
	second, err := balanceHTTPClient(QueryOptions{}, nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if second != doer {
		t.Fatal("balance direct queries must reuse the shared HTTP client for the same transport policy")
	}
}

func TestBalanceJSONRejectsTrailingValue(t *testing.T) {
	requester := &balanceRequester{
		ctx:      context.Background(),
		input:    Input{BaseURL: "https://example.test"},
		key:      "test-key",
		doer:     balanceJSONResponseClient{body: []byte(`{"remaining":12} {"unexpected":true}`)},
		maxBytes: 1024,
	}
	value, diagnostic := requester.getJSON("/v1/usage")
	if value != nil {
		t.Fatalf("trailing JSON must not return a decoded value: %#v", value)
	}
	if diagnostic == nil || diagnostic.code != "invalid_json" {
		t.Fatalf("trailing JSON diagnostic = %#v, want invalid_json", diagnostic)
	}
}
