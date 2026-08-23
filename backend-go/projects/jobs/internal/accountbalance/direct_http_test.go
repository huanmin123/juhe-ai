package accountbalance

import (
	"net/http"
	"testing"
	"time"
)

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
