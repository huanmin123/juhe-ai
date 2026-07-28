// Package upstreamtransport owns one bounded HTTP(S) exchange for an already
// prepared upstream request. It deliberately has no account, credential,
// protocol, retry, persistence, or worker ownership.
package upstreamtransport

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/http/httpguts"
	xproxy "golang.org/x/net/proxy"
	"juhe-ai/backend-go/internal/platform/upstreamurlpolicy"
)

const (
	DefaultTimeout              = 60 * time.Second
	MaxTimeout                  = 10 * time.Minute
	DefaultMaxResponseBodyBytes = int64(1 << 20)
	MaxResponseBodyBytes        = int64(16 << 20)
	defaultMaxResponseHeaders   = int64(64 << 10)
	defaultKeepAlive            = 30 * time.Second
	defaultIdleConnTimeout      = 30 * time.Second
)

var (
	ErrInvalidOptions  = errors.New("upstream transport options are invalid")
	ErrInvalidRequest  = errors.New("upstream transport request is invalid")
	ErrInvalidResponse = errors.New("upstream transport response is invalid")
)

// FailureKind distinguishes local input failures from failures after a real
// HTTP(S) attempt began. Callers still decide whether a transport failure is
// attributable to an account.
type FailureKind string

const (
	FailureInvalidRequest  FailureKind = "invalid_request"
	FailureCanceled        FailureKind = "canceled"
	FailureTimeout         FailureKind = "timeout"
	FailureConnection      FailureKind = "connection"
	FailureRead            FailureKind = "read"
	FailureClose           FailureKind = "close"
	FailureInvalidResponse FailureKind = "invalid_response"
)

// Failure keeps both the typed classification and the original transport error.
type Failure struct {
	Kind FailureKind
	err  error
}

func (e *Failure) Error() string {
	if e == nil {
		return "upstream transport failure"
	}
	if e.err != nil {
		return fmt.Sprintf("upstream transport %s: %v", e.Kind, e.err)
	}
	return "upstream transport " + string(e.Kind)
}

func (e *Failure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

// FailureKindOf returns the typed kind without parsing the original error text.
func FailureKindOf(err error) (FailureKind, bool) {
	var failure *Failure
	if !errors.As(err, &failure) || failure == nil {
		return "", false
	}
	return failure.Kind, true
}

// Options configures one reusable bounded client. Transport is primarily a
// test/custom-network seam; it cannot be combined with transport-owned proxy
// or TLS settings because those options cannot be applied to an opaque value.
type Options struct {
	Timeout              time.Duration
	MaxResponseBodyBytes int64
	ProxyURL             string
	TLSConfig            *tls.Config
	Transport            http.RoundTripper
	URLPolicy            upstreamurlpolicy.Config
}

// Client owns its HTTP client and, when created without a custom transport,
// the underlying connection pool.
type Client struct {
	httpClient           *http.Client
	ownedTransport       *http.Transport
	timeout              time.Duration
	maxResponseBodyBytes int64
	urlPolicy            upstreamurlpolicy.Config
	customTransport      bool
	proxyURL             *url.URL
}

func (*Client) String() string   { return "[upstream transport client]" }
func (*Client) GoString() string { return "[upstream transport client]" }

// Result contains bounded diagnostic bytes and the transport evidence needed
// by a later account-probe classifier. Body is a private copy owned by Result.
type Result struct {
	AttemptURL       string
	Attempted        bool
	ResponseObserved bool
	FramingComplete  bool
	StatusCode       int
	Header           http.Header
	Body             []byte
	BodyBytesRead    int64
	BodyTruncated    bool
}

func NewClient(options Options) (*Client, error) {
	timeout, maxBody, err := normalizeOptions(options)
	if err != nil {
		return nil, err
	}
	if options.Transport != nil && (strings.TrimSpace(options.ProxyURL) != "" || options.TLSConfig != nil) {
		return nil, fmt.Errorf("%w: custom transport cannot be combined with proxy or TLS options", ErrInvalidOptions)
	}

	roundTripper := options.Transport
	var owned *http.Transport
	if roundTripper == nil {
		owned, err = newHTTPTransport(timeout, strings.TrimSpace(options.ProxyURL), options.TLSConfig)
		if err != nil {
			return nil, err
		}
		roundTripper = owned
	}
	var proxyURL *url.URL
	if rawProxyURL := strings.TrimSpace(options.ProxyURL); rawProxyURL != "" {
		proxyURL, err = parseProxyURL(rawProxyURL)
		if err != nil {
			return nil, err
		}
	}
	return &Client{
		httpClient: &http.Client{
			Transport: recordingRoundTripper{next: roundTripper},
			Timeout:   timeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		ownedTransport:       owned,
		timeout:              timeout,
		maxResponseBodyBytes: maxBody,
		urlPolicy:            options.URLPolicy,
		customTransport:      options.Transport != nil,
		proxyURL:             proxyURL,
	}, nil
}

type attemptContextKey struct{}

type attemptRecorder struct {
	attempted        bool
	responseObserved bool
	statusCode       int
	header           http.Header
}

type recordingRoundTripper struct {
	next http.RoundTripper
}

func (r recordingRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	recorder, _ := request.Context().Value(attemptContextKey{}).(*attemptRecorder)
	if recorder != nil {
		recorder.attempted = true
	}
	response, err := r.next.RoundTrip(request)
	if response != nil && recorder != nil {
		recorder.responseObserved = true
		recorder.statusCode = response.StatusCode
		recorder.header = response.Header.Clone()
	}
	if response != nil && err != nil {
		if response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, errors.Join(ErrInvalidResponse, err)
	}
	if err == nil && response == nil {
		return nil, ErrInvalidResponse
	}
	if err == nil && response.Body == nil && response.ContentLength > 0 && request.Method != http.MethodHead {
		return nil, ErrInvalidResponse
	}
	return response, err
}

func normalizeOptions(options Options) (time.Duration, int64, error) {
	timeout := options.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	if timeout < 0 || timeout > MaxTimeout {
		return 0, 0, fmt.Errorf("%w: timeout must be in (0,%s]", ErrInvalidOptions, MaxTimeout)
	}
	maxBody := options.MaxResponseBodyBytes
	if maxBody == 0 {
		maxBody = DefaultMaxResponseBodyBytes
	}
	if maxBody < 0 || maxBody > MaxResponseBodyBytes {
		return 0, 0, fmt.Errorf("%w: response body limit must be in (0,%d]", ErrInvalidOptions, MaxResponseBodyBytes)
	}
	return timeout, maxBody, nil
}

func newHTTPTransport(timeout time.Duration, rawProxyURL string, tlsConfig *tls.Config) (*http.Transport, error) {
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: defaultKeepAlive}
	transport := &http.Transport{
		DialContext:            dialer.DialContext,
		ForceAttemptHTTP2:      true,
		MaxIdleConns:           64,
		MaxIdleConnsPerHost:    8,
		MaxConnsPerHost:        16,
		IdleConnTimeout:        defaultIdleConnTimeout,
		TLSHandshakeTimeout:    timeout,
		ResponseHeaderTimeout:  timeout,
		ExpectContinueTimeout:  timeout,
		MaxResponseHeaderBytes: defaultMaxResponseHeaders,
		DisableCompression:     true,
		TLSClientConfig:        cloneTLSConfig(tlsConfig),
	}
	if rawProxyURL == "" {
		return transport, nil
	}
	proxyURL, err := parseProxyURL(rawProxyURL)
	if err != nil {
		return nil, err
	}
	switch proxyURL.Scheme {
	case "http", "https", "socks5", "socks5h":
		transport.Proxy = http.ProxyURL(proxyURL)
	default:
		return nil, fmt.Errorf("%w: unsupported proxy scheme", ErrInvalidOptions)
	}
	return transport, nil
}

func cloneTLSConfig(config *tls.Config) *tls.Config {
	if config == nil {
		return nil
	}
	return config.Clone()
}

func parseProxyURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" || parsed.Host == "" || parsed.Fragment != "" || parsed.RawQuery != "" {
		return nil, fmt.Errorf("%w: proxy URL is invalid", ErrInvalidOptions)
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return nil, fmt.Errorf("%w: proxy URL path is invalid", ErrInvalidOptions)
	}
	if _, _, err := net.SplitHostPort(parsed.Host); err != nil {
		return nil, fmt.Errorf("%w: proxy URL requires an explicit valid port", ErrInvalidOptions)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	return parsed, nil
}

// Execute sends exactly one request and owns the response body. The supplied
// context is authoritative task cancellation and is never classified as an
// upstream timeout; Options.Timeout owns the attributable hard timeout.
// When request.GetBody is available, Execute uses a fresh body so the caller's
// request remains reusable. Otherwise the request body ownership is transferred.
func (c *Client) Execute(ctx context.Context, request *http.Request) (Result, error) {
	return c.ExecuteWithFence(ctx, request, nil)
}

// ExecuteWithFence runs fence after URL/DNS policy preparation and immediately
// before handing the request to the real RoundTripper. A rejected fence is a
// local, non-attempted failure. This closes the load-to-send revocation window
// without making the transport aware of account or credential semantics.
func (c *Client) ExecuteWithFence(ctx context.Context, request *http.Request, fence func(context.Context) error) (Result, error) {
	if c == nil || c.httpClient == nil || ctx == nil || request == nil || request.URL == nil {
		return Result{}, failure(FailureInvalidRequest, ErrInvalidRequest)
	}
	if err := validateRequest(request); err != nil {
		return Result{}, failure(FailureInvalidRequest, err)
	}
	if err := context.Cause(ctx); err != nil {
		return Result{}, failure(FailureCanceled, err)
	}

	runCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	recorder := &attemptRecorder{}
	runCtx = context.WithValue(runCtx, attemptContextKey{}, recorder)
	prepared, err := cloneRequest(runCtx, request)
	if err != nil {
		return Result{}, failure(FailureInvalidRequest, err)
	}
	result := Result{AttemptURL: prepared.URL.String()}
	if prepared.Header.Get("Accept-Encoding") == "" {
		prepared.Header.Set("Accept-Encoding", "identity")
	}
	httpClient := c.httpClient
	var executionTransport *http.Transport
	if !c.customTransport {
		plan, policyErr := upstreamurlpolicy.PrepareRequestURL(runCtx, result.AttemptURL, c.urlPolicy)
		if policyErr != nil {
			return result, failure(FailureInvalidRequest, errors.Join(ErrInvalidRequest, policyErr))
		}
		prepared.URL = plan.URL()
		executionTransport, err = c.executionTransport(prepared, plan)
		if err != nil {
			return result, failure(FailureInvalidRequest, errors.Join(ErrInvalidRequest, err))
		}
		defer executionTransport.CloseIdleConnections()
		copyClient := *c.httpClient
		copyClient.Transport = recordingRoundTripper{next: executionTransport}
		httpClient = &copyClient
	}
	if fence != nil {
		copyClient := *httpClient
		copyClient.Transport = fenceRoundTripper{next: httpClient.Transport, fence: fence}
		httpClient = &copyClient
	}
	response, doErr := httpClient.Do(prepared)
	result.Attempted = recorder.attempted
	if doErr != nil {
		if recorder.responseObserved {
			result.ResponseObserved = true
			result.StatusCode = recorder.statusCode
			result.Header = recorder.header.Clone()
		}
		if response != nil {
			result.ResponseObserved = true
			result.StatusCode = response.StatusCode
			result.Header = response.Header.Clone()
			if response.Body != nil {
				_ = response.Body.Close()
			}
		}
		if errors.Is(doErr, ErrInvalidResponse) {
			return result, failure(FailureInvalidResponse, doErr)
		}
		if !result.Attempted {
			return result, failure(FailureInvalidRequest, errors.Join(ErrInvalidRequest, doErr))
		}
		return result, failure(classifyNetworkFailure(ctx, runCtx, doErr, FailureConnection), doErr)
	}
	if response == nil {
		return result, failure(FailureInvalidResponse, ErrInvalidResponse)
	}
	result.ResponseObserved = true
	result.StatusCode = response.StatusCode
	result.Header = response.Header.Clone()
	if response.StatusCode < 100 || response.StatusCode > 999 || response.Body == nil {
		if response.Body != nil {
			_ = response.Body.Close()
		}
		return result, failure(FailureInvalidResponse, ErrInvalidResponse)
	}

	body, total, truncated, readErr := readBounded(response.Body, c.maxResponseBodyBytes)
	closeErr := response.Body.Close()
	result.Body = body
	result.BodyBytesRead = total
	result.BodyTruncated = truncated
	if readErr != nil {
		combined := errors.Join(readErr, closeErr)
		return result, failure(classifyNetworkFailure(ctx, runCtx, combined, FailureRead), combined)
	}
	result.FramingComplete = true
	if closeErr != nil {
		return result, failure(FailureClose, closeErr)
	}
	return result, nil
}

type fenceRoundTripper struct {
	next  http.RoundTripper
	fence func(context.Context) error
}

func (r fenceRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if r.next == nil || r.fence == nil {
		return nil, ErrInvalidRequest
	}
	if err := r.fence(request.Context()); err != nil {
		return nil, fmt.Errorf("upstream execution fence rejected request: %w", err)
	}
	return r.next.RoundTrip(request)
}

func (c *Client) executionTransport(request *http.Request, plan *upstreamurlpolicy.DialPlan) (*http.Transport, error) {
	if c == nil || c.ownedTransport == nil || request == nil || request.URL == nil || plan == nil {
		return nil, ErrInvalidRequest
	}
	transport := c.ownedTransport.Clone()
	if c.proxyURL == nil {
		transport.DialContext = plan.DialContext
		return transport, nil
	}
	addresses := plan.Addresses()
	if len(addresses) == 0 {
		return nil, fmt.Errorf("validated upstream URL has no pinned address")
	}
	originalHostname := request.URL.Hostname()
	port := request.URL.Port()
	if port == "" {
		if request.URL.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	transport.Proxy = nil
	transport.DialContext = c.proxyTunnelDialContext(addresses, port)
	if request.URL.Scheme == "https" {
		transport.TLSClientConfig = cloneTLSConfig(transport.TLSClientConfig)
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{}
		}
		if strings.TrimSpace(transport.TLSClientConfig.ServerName) == "" {
			transport.TLSClientConfig.ServerName = originalHostname
		}
	}
	return transport, nil
}

func (c *Client) proxyTunnelDialContext(addresses []netip.Addr, port string) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		if network != "tcp" && network != "tcp4" && network != "tcp6" {
			return nil, fmt.Errorf("unsupported proxy tunnel network %q", network)
		}
		var failures []error
		for _, address := range addresses {
			target := net.JoinHostPort(address.String(), port)
			connection, err := c.dialProxyTunnel(ctx, network, target)
			if err == nil {
				return connection, nil
			}
			failures = append(failures, err)
		}
		return nil, fmt.Errorf("dial pinned upstream through proxy: %w", errors.Join(failures...))
	}
}

func (c *Client) dialProxyTunnel(ctx context.Context, network, target string) (net.Conn, error) {
	switch c.proxyURL.Scheme {
	case "http", "https":
		return c.dialHTTPConnectTunnel(ctx, network, target)
	case "socks5", "socks5h":
		return c.dialSOCKSTunnel(ctx, network, target)
	default:
		return nil, fmt.Errorf("unsupported proxy scheme")
	}
}

func (c *Client) dialHTTPConnectTunnel(ctx context.Context, network, target string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: c.timeout, KeepAlive: defaultKeepAlive}
	connection, err := dialer.DialContext(ctx, network, c.proxyURL.Host)
	if err != nil {
		return nil, err
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = connection.Close()
		}
	}()
	if c.proxyURL.Scheme == "https" {
		proxyTLS := cloneTLSConfig(c.ownedTransport.TLSClientConfig)
		if proxyTLS == nil {
			proxyTLS = &tls.Config{}
		}
		proxyTLS.ServerName = c.proxyURL.Hostname()
		tlsConnection := tls.Client(connection, proxyTLS)
		if err := tlsConnection.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		connection = tlsConnection
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}
	connectRequest := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: target},
		Host:   target,
		Header: make(http.Header),
	}
	if c.proxyURL.User != nil {
		password, _ := c.proxyURL.User.Password()
		credential := c.proxyURL.User.Username() + ":" + password
		connectRequest.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(credential)))
	}
	if err := connectRequest.Write(connection); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(connection)
	response, err := http.ReadResponse(reader, connectRequest)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return nil, fmt.Errorf("proxy CONNECT returned HTTP %d", response.StatusCode)
	}
	if err := connection.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	closeOnError = false
	if reader.Buffered() > 0 {
		return &bufferedConn{Conn: connection, reader: reader}, nil
	}
	return connection, nil
}

func (c *Client) dialSOCKSTunnel(ctx context.Context, network, target string) (net.Conn, error) {
	var auth *xproxy.Auth
	if c.proxyURL.User != nil {
		password, _ := c.proxyURL.User.Password()
		auth = &xproxy.Auth{User: c.proxyURL.User.Username(), Password: password}
	}
	dialer, err := xproxy.SOCKS5(network, c.proxyURL.Host, auth, &net.Dialer{Timeout: c.timeout, KeepAlive: defaultKeepAlive})
	if err != nil {
		return nil, err
	}
	if contextDialer, ok := dialer.(xproxy.ContextDialer); ok {
		return contextDialer.DialContext(ctx, network, target)
	}
	type dialResult struct {
		connection net.Conn
		err        error
	}
	result := make(chan dialResult, 1)
	go func() {
		connection, dialErr := dialer.Dial(network, target)
		result <- dialResult{connection: connection, err: dialErr}
	}()
	select {
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	case value := <-result:
		return value.connection, value.err
	}
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(value []byte) (int, error) {
	return c.reader.Read(value)
}

func validateRequest(request *http.Request) error {
	if request.Method == "" || !httpguts.ValidHeaderFieldName(request.Method) || request.RequestURI != "" ||
		!validHTTPURL(request.URL) ||
		(request.Host != "" && !httpguts.ValidHostHeader(request.Host)) ||
		(request.ContentLength > 0 && (request.Body == nil || request.Body == http.NoBody)) ||
		!validHeader(request.Header) || !validHeader(request.Trailer) {
		return ErrInvalidRequest
	}
	return nil
}

func validHTTPURL(value *url.URL) bool {
	if value == nil || value.Host == "" || value.Hostname() == "" || value.Opaque != "" || value.User != nil || value.Fragment != "" {
		return false
	}
	scheme := strings.ToLower(value.Scheme)
	if scheme != "http" && scheme != "https" {
		return false
	}
	if port := value.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return false
		}
	}
	return true
}

func validHeader(header http.Header) bool {
	for name, values := range header {
		if !httpguts.ValidHeaderFieldName(name) {
			return false
		}
		for _, value := range values {
			if !httpguts.ValidHeaderFieldValue(value) {
				return false
			}
		}
	}
	return true
}

func cloneRequest(ctx context.Context, request *http.Request) (*http.Request, error) {
	prepared := request.Clone(ctx)
	prepared.URL.Scheme = strings.ToLower(prepared.URL.Scheme)
	if request.Body == nil || request.Body == http.NoBody || request.GetBody == nil {
		return prepared, nil
	}
	body, err := request.GetBody()
	if err != nil {
		return nil, fmt.Errorf("%w: request body cannot be replayed", ErrInvalidRequest)
	}
	prepared.Body = body
	return prepared, nil
}

// readBounded retains only a bounded prefix while consuming through EOF so
// truncation remains distinct from incomplete response framing.
func readBounded(reader io.Reader, limit int64) ([]byte, int64, bool, error) {
	capture := &boundedCapture{limit: limit, body: make([]byte, 0, minInt64(limit, 32<<10))}
	buffer := make([]byte, 32<<10)
	_, err := io.CopyBuffer(capture, reader, buffer)
	return capture.body, capture.total, capture.truncated, err
}

type boundedCapture struct {
	limit     int64
	total     int64
	body      []byte
	truncated bool
}

func (w *boundedCapture) Write(value []byte) (int, error) {
	length := len(value)
	if length == 0 {
		return 0, nil
	}
	if w.total > math.MaxInt64-int64(length) {
		w.total = math.MaxInt64
	} else {
		w.total += int64(length)
	}
	remaining := w.limit - int64(len(w.body))
	if remaining > 0 {
		keep := int64(length)
		if keep > remaining {
			keep = remaining
		}
		w.body = append(w.body, value[:int(keep)]...)
	}
	if w.total > w.limit {
		w.truncated = true
	}
	return length, nil
}

func classifyNetworkFailure(parentCtx, runCtx context.Context, err error, fallback FailureKind) FailureKind {
	if context.Cause(parentCtx) != nil {
		return FailureCanceled
	}
	if cause := context.Cause(runCtx); cause != nil {
		return contextFailureKind(cause)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return FailureTimeout
	}
	if errors.Is(err, context.Canceled) {
		return FailureCanceled
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return FailureTimeout
	}
	return fallback
}

func contextFailureKind(err error) FailureKind {
	if errors.Is(err, context.DeadlineExceeded) {
		return FailureTimeout
	}
	return FailureCanceled
}

func failure(kind FailureKind, err error) error {
	return &Failure{Kind: kind, err: err}
}

func minInt64(left, right int64) int {
	if left < right {
		return int(left)
	}
	return int(right)
}

// CloseIdleConnections releases pooled connections owned by this client.
func (c *Client) CloseIdleConnections() {
	if c != nil && c.ownedTransport != nil {
		c.ownedTransport.CloseIdleConnections()
	}
}
