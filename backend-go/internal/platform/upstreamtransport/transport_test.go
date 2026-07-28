package upstreamtransport

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/platform/upstreamurlpolicy"
)

func TestExecuteReadsCompleteResponseWithBoundedCapture(t *testing.T) {
	t.Parallel()
	const payload = "0123456789"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("X-Upstream", "observed")
		response.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(response, payload)
	}))
	defer server.Close()

	client, err := NewClient(Options{MaxResponseBodyBytes: 4, URLPolicy: privateURLPolicy(server.URL)})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	request, _ := http.NewRequest(http.MethodGet, server.URL+"/probe", nil)
	result, err := client.Execute(t.Context(), request)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Attempted || !result.ResponseObserved || !result.FramingComplete || result.StatusCode != http.StatusCreated {
		t.Fatalf("result evidence = %+v", result)
	}
	if string(result.Body) != payload[:4] || result.BodyBytesRead != int64(len(payload)) || !result.BodyTruncated {
		t.Fatalf("body=%q bytes=%d truncated=%t", result.Body, result.BodyBytesRead, result.BodyTruncated)
	}
	if result.Header.Get("X-Upstream") != "observed" || result.AttemptURL != server.URL+"/probe" {
		t.Fatalf("result metadata = %+v", result)
	}
}

func TestExecuteDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetCalls.Add(1)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, target.URL, http.StatusFound)
	}))
	defer redirect.Close()

	client, _ := NewClient(Options{URLPolicy: privateURLPolicy(redirect.URL)})
	defer client.CloseIdleConnections()
	request, _ := http.NewRequest(http.MethodGet, redirect.URL, nil)
	result, err := client.Execute(t.Context(), request)
	if err != nil || result.StatusCode != http.StatusFound || !result.FramingComplete {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if targetCalls.Load() != 0 {
		t.Fatal("redirect target was called")
	}
}

func TestExecuteSupportsHTTPSWithCallerTrustRoots(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, "secure")
	}))
	defer server.Close()
	serverTransport := server.Client().Transport.(*http.Transport)
	client, err := NewClient(Options{TLSConfig: serverTransport.TLSClientConfig, URLPolicy: privateURLPolicy(server.URL)})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	request, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	result, err := client.Execute(t.Context(), request)
	if err != nil || string(result.Body) != "secure" || !result.FramingComplete {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestExecuteRetainsAttemptOnConnectionFailure(t *testing.T) {
	t.Parallel()
	transportErr := errors.New("dial failed with secret=do-not-log")
	client, _ := NewClient(Options{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, transportErr
	})})
	request, _ := http.NewRequest(http.MethodGet, "https://upstream.example.test/v1/models", nil)
	result, err := client.Execute(t.Context(), request)
	if kind, ok := FailureKindOf(err); !ok || kind != FailureConnection {
		t.Fatalf("error=%v kind=%q ok=%t", err, kind, ok)
	}
	if !result.Attempted || result.ResponseObserved || result.AttemptURL != request.URL.String() {
		t.Fatalf("result=%+v", result)
	}
	if !strings.Contains(err.Error(), "do-not-log") || !errors.Is(err, transportErr) {
		t.Fatalf("error detail/wrapping = %q", err)
	}
}

func TestExecuteClassifiesTimeoutAndCancellation(t *testing.T) {
	t.Parallel()
	t.Run("timeout", func(t *testing.T) {
		client, _ := NewClient(Options{Timeout: 10 * time.Millisecond, Transport: blockingRoundTripper{}})
		request, _ := http.NewRequest(http.MethodGet, "https://upstream.example.test", nil)
		result, err := client.Execute(t.Context(), request)
		if kind, _ := FailureKindOf(err); kind != FailureTimeout || !result.Attempted {
			t.Fatalf("result=%+v err=%v kind=%q", result, err, kind)
		}
	})
	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		client, _ := NewClient(Options{Transport: blockingRoundTripper{}})
		request, _ := http.NewRequest(http.MethodGet, "https://upstream.example.test", nil)
		result, err := client.Execute(ctx, request)
		if kind, _ := FailureKindOf(err); kind != FailureCanceled || result.Attempted {
			t.Fatalf("result=%+v err=%v kind=%q", result, err, kind)
		}
	})
	t.Run("caller deadline is cancellation", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
		defer cancel()
		client, _ := NewClient(Options{Timeout: time.Second, Transport: blockingRoundTripper{}})
		request, _ := http.NewRequest(http.MethodGet, "https://upstream.example.test", nil)
		result, err := client.Execute(ctx, request)
		if kind, _ := FailureKindOf(err); kind != FailureCanceled || !result.Attempted {
			t.Fatalf("result=%+v err=%v kind=%q", result, err, kind)
		}
	})
}

func TestExecuteReturnsPartialBodyOnReadFailure(t *testing.T) {
	t.Parallel()
	readErr := errors.New("incomplete response")
	client, _ := NewClient(Options{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"X-Observed": []string{"yes"}},
			Body:       &errorReadCloser{reader: io.MultiReader(strings.NewReader("partial"), errorReader{err: readErr})},
		}, nil
	})})
	request, _ := http.NewRequest(http.MethodGet, "https://upstream.example.test", nil)
	result, err := client.Execute(t.Context(), request)
	if kind, _ := FailureKindOf(err); kind != FailureRead || !errors.Is(err, readErr) {
		t.Fatalf("err=%v kind=%q", err, kind)
	}
	if !result.Attempted || !result.ResponseObserved || result.FramingComplete || string(result.Body) != "partial" || result.StatusCode != http.StatusOK {
		t.Fatalf("result=%+v", result)
	}
}

func TestExecuteUsesFreshReplayableRequestBody(t *testing.T) {
	t.Parallel()
	var received string
	client, _ := NewClient(Options{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		received = string(body)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(nil))}, nil
	})})
	request, _ := http.NewRequest(http.MethodPost, "https://upstream.example.test", strings.NewReader("owned"))
	result, err := client.Execute(t.Context(), request)
	if err != nil || !result.FramingComplete || received != "owned" {
		t.Fatalf("result=%+v received=%q err=%v", result, received, err)
	}
	original, err := io.ReadAll(request.Body)
	if err != nil || string(original) != "owned" {
		t.Fatalf("original body=%q err=%v", original, err)
	}
}

func TestExecuteRejectsInvalidRequestWithoutAttempt(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client, _ := NewClient(Options{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, nil
	})})
	request, _ := http.NewRequest(http.MethodGet, "file:///tmp/secret", nil)
	result, err := client.Execute(t.Context(), request)
	if kind, _ := FailureKindOf(err); kind != FailureInvalidRequest || result.Attempted || calls.Load() != 0 || !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("result=%+v err=%v kind=%q calls=%d", result, err, kind, calls.Load())
	}
}

func TestExecuteRejectsEmptyHostnameAndInvalidPortWithoutAttempt(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client, _ := NewClient(Options{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, nil
	})})
	for _, rawURL := range []string{"http://:8080/probe", "https://upstream.example.test:0/probe", "https://upstream.example.test:65536/probe"} {
		request := &http.Request{Method: http.MethodGet, URL: mustParseTestURL(t, rawURL), Header: make(http.Header)}
		result, err := client.Execute(t.Context(), request)
		if kind, _ := FailureKindOf(err); kind != FailureInvalidRequest || result.Attempted || calls.Load() != 0 {
			t.Fatalf("url=%q result=%+v err=%v kind=%q calls=%d", rawURL, result, err, kind, calls.Load())
		}
	}
}

func TestExecuteDoesNotCountHTTPClientRejectionAsAttempt(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client, _ := NewClient(Options{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, nil
	})})
	requests := []*http.Request{}
	requestURI, _ := http.NewRequest(http.MethodGet, "https://upstream.example.test", nil)
	requestURI.RequestURI = "/server-only"
	requests = append(requests, requestURI)
	invalidMethod, _ := http.NewRequest(http.MethodGet, "https://upstream.example.test", nil)
	invalidMethod.Method = "GE T"
	requests = append(requests, invalidMethod)
	invalidHeader, _ := http.NewRequest(http.MethodGet, "https://upstream.example.test", nil)
	invalidHeader.Header["Bad Header"] = []string{"value"}
	requests = append(requests, invalidHeader)

	for _, request := range requests {
		result, err := client.Execute(t.Context(), request)
		if kind, _ := FailureKindOf(err); kind != FailureInvalidRequest || result.Attempted || calls.Load() != 0 || !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("request=%+v result=%+v err=%v kind=%q calls=%d", request, result, err, kind, calls.Load())
		}
	}
}

func TestExecuteClassifiesInvalidTransportResponse(t *testing.T) {
	t.Parallel()
	client, _ := NewClient(Options{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        http.Header{"X-Observed": []string{"yes"}},
			ContentLength: 10,
		}, nil
	})})
	request, _ := http.NewRequest(http.MethodGet, "https://upstream.example.test", nil)
	result, err := client.Execute(t.Context(), request)
	if kind, _ := FailureKindOf(err); kind != FailureInvalidResponse || !result.Attempted || !result.ResponseObserved ||
		result.StatusCode != http.StatusOK || result.Header.Get("X-Observed") != "yes" || !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("result=%+v err=%v kind=%q", result, err, kind)
	}
}

func TestExecuteClosesAndRecordsResponseReturnedWithError(t *testing.T) {
	t.Parallel()
	closed := atomic.Bool{}
	transportErr := errors.New("malformed transport response")
	client, _ := NewClient(Options{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Header:     http.Header{"X-Observed": []string{"yes"}},
			Body:       closeTrackingBody{closed: &closed},
		}, transportErr
	})})
	request, _ := http.NewRequest(http.MethodGet, "https://upstream.example.test", nil)
	result, err := client.Execute(t.Context(), request)
	if kind, _ := FailureKindOf(err); kind != FailureInvalidResponse || !result.Attempted || !result.ResponseObserved ||
		result.StatusCode != http.StatusBadGateway || result.Header.Get("X-Observed") != "yes" || !closed.Load() ||
		!errors.Is(err, ErrInvalidResponse) || !errors.Is(err, transportErr) {
		t.Fatalf("result=%+v err=%v kind=%q closed=%t", result, err, kind, closed.Load())
	}
}

func TestExecuteNormalizesHTTPSchemeBeforeAttempt(t *testing.T) {
	t.Parallel()
	var observedScheme string
	client, _ := NewClient(Options{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		observedScheme = request.URL.Scheme
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})})
	request, _ := http.NewRequest(http.MethodGet, "https://upstream.example.test", nil)
	request.URL.Scheme = "HTTPS"
	result, err := client.Execute(t.Context(), request)
	if err != nil || !result.Attempted || observedScheme != "https" || request.URL.Scheme != "HTTPS" {
		t.Fatalf("result=%+v err=%v observed=%q original=%q", result, err, observedScheme, request.URL.Scheme)
	}
}

func TestExecutePreservesFramingOnCloseFailure(t *testing.T) {
	t.Parallel()
	closeErr := errors.New("close failed")
	client, _ := NewClient(Options{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       &closeErrorReadCloser{Reader: strings.NewReader("complete"), err: closeErr},
		}, nil
	})})
	request, _ := http.NewRequest(http.MethodGet, "https://upstream.example.test", nil)
	result, err := client.Execute(t.Context(), request)
	if kind, _ := FailureKindOf(err); kind != FailureClose || !errors.Is(err, closeErr) || !result.FramingComplete || string(result.Body) != "complete" {
		t.Fatalf("result=%+v err=%v kind=%q", result, err, kind)
	}
}

func TestNewClientValidatesOptionsAndProxyURL(t *testing.T) {
	t.Parallel()
	tests := []Options{
		{Timeout: -time.Second},
		{Timeout: MaxTimeout + time.Second},
		{MaxResponseBodyBytes: -1},
		{MaxResponseBodyBytes: MaxResponseBodyBytes + 1},
		{ProxyURL: "ftp://127.0.0.1:21"},
		{ProxyURL: "http://proxy.example.test"},
		{ProxyURL: "http://127.0.0.1:8080/path"},
		{ProxyURL: "http://127.0.0.1:8080", Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, nil })},
		{TLSConfig: &tls.Config{}, Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, nil })},
	}
	for _, options := range tests {
		if _, err := NewClient(options); !errors.Is(err, ErrInvalidOptions) {
			t.Fatalf("NewClient(%+v) error=%v", options, err)
		}
	}
}

func TestNewClientConfiguresHTTPAndSOCKSProxyTransports(t *testing.T) {
	t.Parallel()
	httpClient, err := NewClient(Options{ProxyURL: "http://user:password@127.0.0.1:8080"})
	if err != nil || httpClient.ownedTransport.Proxy == nil {
		t.Fatalf("HTTP proxy client=%+v err=%v", httpClient, err)
	}
	httpClient.CloseIdleConnections()
	socksClient, err := NewClient(Options{ProxyURL: "socks5h://user:password@127.0.0.1:1080"})
	if err != nil || socksClient.ownedTransport.Proxy == nil {
		t.Fatalf("SOCKS proxy client=%+v err=%v", socksClient, err)
	}
	socksClient.CloseIdleConnections()
}

func TestExecuteRejectsPlainHTTPForwardProxyBeforeAttempt(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	proxyServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
	}))
	defer proxyServer.Close()

	client, err := NewClient(Options{ProxyURL: proxyServer.URL, URLPolicy: publicURLPolicy("8.8.8.8")})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	request, _ := http.NewRequest(http.MethodGet, "http://upstream.example.test/probe?mode=proxy", nil)
	result, err := client.Execute(t.Context(), request)
	if kind, ok := FailureKindOf(err); !ok || kind != FailureInvalidRequest || result.Attempted {
		t.Fatalf("result=%+v error=%v kind=%q", result, err, kind)
	}
	if calls.Load() != 0 {
		t.Fatalf("forward proxy calls = %d", calls.Load())
	}
}

func TestExecutePinsHTTPSConnectTargetThroughForwardProxy(t *testing.T) {
	t.Parallel()
	var method, target string
	proxyServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		method, target = request.Method, request.Host
		response.WriteHeader(http.StatusBadGateway)
	}))
	defer proxyServer.Close()

	client, err := NewClient(Options{ProxyURL: proxyServer.URL, URLPolicy: publicURLPolicy("8.8.8.8")})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	request, _ := http.NewRequest(http.MethodGet, "https://upstream.example.test/probe", nil)
	result, executeErr := client.Execute(t.Context(), request)
	if executeErr == nil || !result.Attempted {
		t.Fatalf("result=%+v error=%v", result, executeErr)
	}
	if method != http.MethodConnect || target != "8.8.8.8:443" {
		t.Fatalf("proxy request = %s %s", method, target)
	}
}

func TestTLSConfigIsCloned(t *testing.T) {
	t.Parallel()
	config := &tls.Config{ServerName: "before.example.test", MinVersion: tls.VersionTLS12}
	client, err := NewClient(Options{TLSConfig: config})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	config.ServerName = "after.example.test"
	if client.ownedTransport.TLSClientConfig.ServerName != "before.example.test" {
		t.Fatal("TLS config was not cloned")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type blockingRoundTripper struct{}

func (blockingRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	<-request.Context().Done()
	return nil, context.Cause(request.Context())
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

type errorReadCloser struct{ reader io.Reader }

func (r *errorReadCloser) Read(value []byte) (int, error) { return r.reader.Read(value) }
func (*errorReadCloser) Close() error                     { return nil }

type closeErrorReadCloser struct {
	io.Reader
	err error
}

func (r *closeErrorReadCloser) Close() error { return r.err }

type closeTrackingBody struct{ closed *atomic.Bool }

func (closeTrackingBody) Read([]byte) (int, error) { return 0, io.EOF }
func (b closeTrackingBody) Close() error {
	b.closed.Store(true)
	return nil
}

func mustParseTestURL(t *testing.T, rawURL string) *url.URL {
	t.Helper()
	value, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

type transportResolverFunc func(context.Context, string, string) ([]netip.Addr, error)

func (f transportResolverFunc) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return f(ctx, network, host)
}

func privateURLPolicy(origin string) upstreamurlpolicy.Config {
	return upstreamurlpolicy.Config{PrivateBaseURLAllowlist: []string{origin}}
}

func publicURLPolicy(addresses ...string) upstreamurlpolicy.Config {
	return upstreamurlpolicy.Config{Resolver: transportResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		result := make([]netip.Addr, 0, len(addresses))
		for _, address := range addresses {
			result = append(result, netip.MustParseAddr(address))
		}
		return result, nil
	})}
}
