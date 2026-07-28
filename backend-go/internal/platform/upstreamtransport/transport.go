// Package upstreamtransport owns one bounded HTTP(S) exchange for an already
// prepared upstream request. It deliberately has no account, credential,
// protocol, retry, persistence, or worker ownership.
package upstreamtransport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/http/httpguts"
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
}

// Client owns its HTTP client and, when created without a custom transport,
// the underlying connection pool.
type Client struct {
	httpClient           *http.Client
	ownedTransport       *http.Transport
	timeout              time.Duration
	maxResponseBodyBytes int64
}

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
	response, doErr := c.httpClient.Do(prepared)
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
