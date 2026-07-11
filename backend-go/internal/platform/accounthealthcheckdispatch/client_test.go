package accounthealthcheckdispatch

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	goldenSecret    = "account-health-check-dispatch-http-secret"
	goldenBody      = `{"accountId":"account-signature","reason":"activation"}`
	goldenSignature = "v1=53edb5f836f86d8a5d1ec0055bb58625d41dbc26faef38ef2e08aa8b3def6e2a"
)

func TestDispatchSendsNodeCompatibleRequest(t *testing.T) {
	type capturedRequest struct {
		method          string
		path            string
		contentType     string
		contentEncoding string
		signature       string
		body            string
	}
	captured := make(chan capturedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		captured <- capturedRequest{
			method:          r.Method,
			path:            r.URL.RequestURI(),
			contentType:     r.Header.Get("Content-Type"),
			contentEncoding: r.Header.Get("Content-Encoding"),
			signature:       r.Header.Get("X-Juhe-AI-Signature"),
			body:            string(body),
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, goldenSecret)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if err := client.Dispatch(t.Context(), "account-signature", "activation"); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}

	request := <-captured
	if request.method != http.MethodPost {
		t.Errorf("method = %q, want %q", request.method, http.MethodPost)
	}
	if request.path != dispatchPath {
		t.Errorf("path = %q, want %q", request.path, dispatchPath)
	}
	if request.contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", request.contentType)
	}
	if request.contentEncoding != "identity" {
		t.Errorf("Content-Encoding = %q, want identity", request.contentEncoding)
	}
	if request.body != goldenBody {
		t.Errorf("body = %q, want %q", request.body, goldenBody)
	}
	if request.signature != goldenSignature {
		t.Errorf("signature = %q, want %q", request.signature, goldenSignature)
	}
}

func TestDispatchAcceptsConfigurationReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if got, want := string(body), `{"accountId":"account-configuration","reason":"configuration"}`; got != want {
			t.Errorf("body = %q, want %q", got, want)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, goldenSecret)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if err := client.Dispatch(t.Context(), "account-configuration", "configuration"); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
}

func TestNewClientRejectsUnsafeBaseURLs(t *testing.T) {
	tests := []string{
		"",
		"http://127.0.0.1",
		"https://127.0.0.1:443",
		"http://localhost:3000",
		"http://example.com:3000",
		"http://10.0.0.1:3000",
		"http://192.168.1.1:3000",
		"http://[::2]:3000",
		"http://user:password@127.0.0.1:3000",
		"http://127.0.0.1:3000/",
		"http://127.0.0.1:3000/dispatch",
		"http://127.0.0.1:3000?query=value",
		"http://127.0.0.1:3000?",
		"http://127.0.0.1:3000#fragment",
		"http://127.0.0.1:3000#",
		"http://127.0.0.1:0",
		"http://127.0.0.1:65536",
		"http://127.0.0.1:http",
		"http://0177.0.0.1:3000",
	}
	for _, rawURL := range tests {
		t.Run(rawURL, func(t *testing.T) {
			if _, err := NewClient(rawURL, goldenSecret); err == nil {
				t.Fatal("NewClient() error = nil, want unsafe base URL error")
			}
		})
	}
}

func TestNewClientAcceptsLoopbackIPLiteralWithExplicitPort(t *testing.T) {
	for _, rawURL := range []string{
		"http://127.0.0.1:1",
		"http://127.255.255.254:65535",
		"http://[::1]:8080",
		"http://[0:0:0:0:0:0:0:1]:8080",
	} {
		t.Run(rawURL, func(t *testing.T) {
			if _, err := NewClient(rawURL, goldenSecret); err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
		})
	}
}

func TestNewClientRequiresSecret(t *testing.T) {
	if _, err := NewClient("http://127.0.0.1:3000", ""); err == nil {
		t.Fatal("NewClient() error = nil, want secret error")
	}
}

func TestNewClientBuildsDedicatedSafeHTTPClient(t *testing.T) {
	client, err := NewClient("http://127.0.0.1:3000", goldenSecret)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if client.httpClient.Timeout != requestTimeout {
		t.Errorf("http client timeout = %s, want %s", client.httpClient.Timeout, requestTimeout)
	}
	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.httpClient.Transport)
	}
	if transport == http.DefaultTransport {
		t.Fatal("transport reuses http.DefaultTransport, want a clone")
	}
	if transport.Proxy != nil {
		t.Fatal("transport Proxy is non-nil, want proxy disabled")
	}
	if !transport.DisableCompression {
		t.Fatal("transport DisableCompression = false, want compression disabled")
	}
	if transport.ForceAttemptHTTP2 {
		t.Fatal("transport ForceAttemptHTTP2 = true, want controlled HTTP/1 transport")
	}
	if transport.DialContext == nil {
		t.Fatal("transport DialContext = nil, want dedicated dialer")
	}
	if transport.DialTLS != nil || transport.DialTLSContext != nil {
		t.Fatal("transport has custom TLS dial hook, want none")
	}
	if transport.MaxIdleConns <= 0 || transport.MaxIdleConnsPerHost <= 0 || transport.MaxConnsPerHost <= 0 {
		t.Fatalf(
			"transport connection limits = max idle %d, per host %d, max per host %d; want positive values",
			transport.MaxIdleConns,
			transport.MaxIdleConnsPerHost,
			transport.MaxConnsPerHost,
		)
	}
	if transport.IdleConnTimeout <= 0 ||
		transport.ResponseHeaderTimeout <= 0 ||
		transport.TLSHandshakeTimeout <= 0 ||
		transport.ExpectContinueTimeout <= 0 {
		t.Fatalf(
			"transport timeouts = idle %s, response header %s, TLS handshake %s, expect continue %s; want positive values",
			transport.IdleConnTimeout,
			transport.ResponseHeaderTimeout,
			transport.TLSHandshakeTimeout,
			transport.ExpectContinueTimeout,
		)
	}
	if transport.MaxResponseHeaderBytes <= 0 {
		t.Fatalf("transport MaxResponseHeaderBytes = %d, want positive bound", transport.MaxResponseHeaderBytes)
	}
	if client.httpClient.CheckRedirect == nil {
		t.Fatal("CheckRedirect = nil, want redirects disabled")
	}
	if err := client.httpClient.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("CheckRedirect() error = %v, want http.ErrUseLastResponse", err)
	}
}

func TestNewClientIgnoresReplacedDefaultTransport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	originalDefaultTransport := http.DefaultTransport
	http.DefaultTransport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
		panic("global default transport must not be used")
	})
	defer func() {
		http.DefaultTransport = originalDefaultTransport
	}()

	client, err := NewClient(server.URL, goldenSecret)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if err := client.Dispatch(t.Context(), "account-isolated-transport", "activation"); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
}

func TestNewClientIgnoresProxyEnvironment(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("ALL_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, goldenSecret)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.httpClient.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("transport Proxy is non-nil under proxy environment")
	}
	if err := client.Dispatch(t.Context(), "account-no-proxy", "activation"); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
}

func TestDispatchRejectsInvalidInputWithoutRequest(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, goldenSecret)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	tests := []struct {
		name      string
		accountID string
		reason    string
	}{
		{name: "empty account ID", accountID: "", reason: "activation"},
		{name: "blank account ID", accountID: " \t\r\n ", reason: "configuration"},
		{name: "empty reason", accountID: "account-1", reason: ""},
		{name: "scheduled reason", accountID: "account-1", reason: "scheduled"},
		{name: "case changed reason", accountID: "account-1", reason: "Activation"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := client.Dispatch(t.Context(), test.accountID, test.reason); err == nil {
				t.Fatal("Dispatch() error = nil, want validation error")
			}
		})
	}
	if got := requestCount.Load(); got != 0 {
		t.Fatalf("request count = %d, want 0", got)
	}
}

func TestDispatchHonorsCallerCancellation(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-releaseHandler
	}))
	defer server.Close()
	defer close(releaseHandler)

	client, err := NewClient(server.URL, goldenSecret)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		result <- client.Dispatch(ctx, "account-cancel", "activation")
	}()

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("dispatch request did not reach loopback server")
	}
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Dispatch() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Dispatch() did not stop after caller cancellation")
	}
}

func TestDispatchHonorsCallerDeadline(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-releaseHandler
	}))
	defer server.Close()
	defer close(releaseHandler)

	client, err := NewClient(server.URL, goldenSecret)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()

	result := make(chan error, 1)
	go func() {
		result <- client.Dispatch(ctx, "account-deadline", "configuration")
	}()
	select {
	case <-requestStarted:
	case err := <-result:
		t.Fatalf("Dispatch() ended before reaching loopback server: %v", err)
	case <-time.After(time.Second):
		t.Fatal("dispatch request did not reach loopback server")
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Dispatch() error = %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Dispatch() did not stop at caller deadline")
	}
}

func TestDispatchOnlyAcceptsExact202AndDoesNotLeakBodyOrRetry(t *testing.T) {
	for _, statusCode := range []int{
		http.StatusOK,
		http.StatusNoContent,
		http.StatusBadRequest,
		http.StatusServiceUnavailable,
	} {
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			const responseMarker = "private-node-response-body-must-not-leak"
			var requestCount atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestCount.Add(1)
				w.WriteHeader(statusCode)
				_, _ = io.WriteString(w, responseMarker)
			}))
			defer server.Close()

			client, err := NewClient(server.URL, goldenSecret)
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			err = client.Dispatch(t.Context(), "account-status", "activation")
			if err == nil {
				t.Fatal("Dispatch() error = nil, want status error")
			}
			if strings.Contains(err.Error(), responseMarker) {
				t.Fatalf("Dispatch() error leaks response body: %v", err)
			}
			if !strings.Contains(err.Error(), strconv.Itoa(statusCode)) ||
				!strings.Contains(err.Error(), http.StatusText(statusCode)) {
				t.Fatalf("Dispatch() error = %q, want bounded status context", err)
			}
			if len(err.Error()) > 256 {
				t.Fatalf("Dispatch() error length = %d, want <= 256", len(err.Error()))
			}
			if got := requestCount.Load(); got != 1 {
				t.Fatalf("request count = %d, want 1", got)
			}
		})
	}
}

func TestDispatchDoesNotFollowRedirects(t *testing.T) {
	var redirectedRequestCount atomic.Int32
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedRequestCount.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer redirectTarget.Close()

	var sourceRequestCount atomic.Int32
	redirectSource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceRequestCount.Add(1)
		http.Redirect(w, r, redirectTarget.URL, http.StatusTemporaryRedirect)
	}))
	defer redirectSource.Close()

	client, err := NewClient(redirectSource.URL, goldenSecret)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	err = client.Dispatch(t.Context(), "account-redirect", "activation")
	if err == nil {
		t.Fatal("Dispatch() error = nil, want redirect status error")
	}
	if got := sourceRequestCount.Load(); got != 1 {
		t.Fatalf("source request count = %d, want 1", got)
	}
	if got := redirectedRequestCount.Load(); got != 0 {
		t.Fatalf("redirect target request count = %d, want 0", got)
	}
}

func TestDispatchRequestCannotBeAutomaticallyReplayed(t *testing.T) {
	client, err := NewClient("http://127.0.0.1:3000", goldenSecret)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	var requestGetBody func() (io.ReadCloser, error)
	client.httpClient.Transport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		requestGetBody = request.GetBody
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Status:     "202 Accepted",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	})

	if err := client.Dispatch(t.Context(), "account-no-retry", "activation"); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if requestGetBody != nil {
		t.Fatal("request GetBody is non-nil, allowing the transport to replay the POST")
	}
}

func TestDispatchBoundsAndClosesResponseBody(t *testing.T) {
	client, err := NewClient("http://127.0.0.1:3000", goldenSecret)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	body := &unboundedResponseBody{}
	client.httpClient.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Status:     "503 Service Unavailable",
			Header:     make(http.Header),
			Body:       body,
		}, nil
	})

	err = client.Dispatch(t.Context(), "account-bounded", "activation")
	if err == nil {
		t.Fatal("Dispatch() error = nil, want status error")
	}
	if got := body.bytesRead.Load(); got != maxResponseDrainBytes {
		t.Fatalf("response bytes read = %d, want %d", got, maxResponseDrainBytes)
	}
	if !body.closed.Load() {
		t.Fatal("response body was not closed")
	}
}

func TestDispatchDrainsSmallResponseForConnectionReuse(t *testing.T) {
	var newConnections atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "small response")
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			newConnections.Add(1)
		}
	}
	server.Start()
	defer server.Close()

	client, err := NewClient(server.URL, goldenSecret)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	for range 2 {
		if err := client.Dispatch(t.Context(), "account-reuse", "activation"); err == nil {
			t.Fatal("Dispatch() error = nil, want status error")
		}
	}
	if got := newConnections.Load(); got != 1 {
		t.Fatalf("new connection count = %d, want 1", got)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type unboundedResponseBody struct {
	bytesRead atomic.Int64
	closed    atomic.Bool
}

func (body *unboundedResponseBody) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = 'x'
	}
	body.bytesRead.Add(int64(len(buffer)))
	return len(buffer), nil
}

func (body *unboundedResponseBody) Close() error {
	body.closed.Store(true)
	return nil
}
