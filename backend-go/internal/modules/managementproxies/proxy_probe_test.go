package managementproxies

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDefaultProxyProbeUsesHTTPProxyAndBoundsResponseBody(t *testing.T) {
	const oversizedBody = 512*1024 + 128
	targetURL := "http://upstream.example.com/probe"
	proxySeen := make(chan *http.Request, 1)
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxySeen <- r.Clone(r.Context())
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		if r.RequestURI != targetURL {
			t.Fatalf("request URI = %q, want absolute target %q", r.RequestURI, targetURL)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(strings.Repeat("x", oversizedBody)))
	}))
	defer proxyServer.Close()

	result, err := newDefaultProxyProbe().Probe(context.Background(), ProxyProbeInput{
		TargetURL: targetURL,
		ProxyURL:  proxyServer.URL,
		Timeout:   2 * time.Second,
	})
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	if result.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", result.StatusCode)
	}
	if len(result.Body) != maxProxyProbeResponseBytes {
		t.Fatalf("body length = %d, want %d", len(result.Body), maxProxyProbeResponseBytes)
	}
	select {
	case req := <-proxySeen:
		if req.Header.Get("User-Agent") != "juhe-ai-proxy-test/0.1" ||
			req.Header.Get("Accept") != "application/json,text/plain,*/*" {
			t.Fatalf("headers = %+v", req.Header)
		}
	case <-time.After(time.Second):
		t.Fatal("proxy server did not receive request")
	}
}

func TestDefaultProxyProbeRejectsInvalidProxyScheme(t *testing.T) {
	_, err := newDefaultProxyProbe().Probe(context.Background(), ProxyProbeInput{
		TargetURL: "https://api.example.com",
		ProxyURL:  "ftp://proxy.example.com:21",
		Timeout:   time.Second,
	})
	if err == nil || !strings.Contains(fmt.Sprint(err), "代理协议无效") {
		t.Fatalf("Probe() error = %v, want invalid proxy scheme", err)
	}
	if isTransportProxyProbeFailure(err) {
		t.Fatalf("invalid proxy configuration classified as transport failure: %v", err)
	}
}

func TestDefaultProxyProbeReturnsCompleteServerErrorResponse(t *testing.T) {
	targetURL := "http://upstream.example.com/probe"
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.RequestURI != targetURL {
			t.Fatalf("request URI = %q, want %q", r.RequestURI, targetURL)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("temporary upstream response"))
	}))
	defer proxyServer.Close()

	result, err := newDefaultProxyProbe().Probe(context.Background(), ProxyProbeInput{
		TargetURL: targetURL,
		ProxyURL:  proxyServer.URL,
		Timeout:   2 * time.Second,
	})
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	if result.StatusCode != http.StatusServiceUnavailable || result.Body != "temporary upstream response" {
		t.Fatalf("result = %+v", result)
	}
}
