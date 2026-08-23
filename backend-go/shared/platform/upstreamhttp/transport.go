// Package upstreamhttp contains the transport-only boundary used by Go
// projects that call an external upstream directly.  It deliberately does
// not know account, provider, retry, SSE, or response-interpretation rules.
package upstreamhttp

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const DefaultMaxResponseHeaderBytes int64 = 64 * 1024

var (
	ErrProxyURLInvalid        = errors.New("upstream proxy URL is invalid")
	ErrProxySchemeUnsupported = errors.New("upstream proxy scheme is unsupported")
)

// TransportOptions controls only low-level connection behavior.  A zero
// ResponseHeaderTimeout lets the caller's request context remain the total
// deadline; callers that have a per-upstream budget should set it explicitly.
type TransportOptions struct {
	ResponseHeaderTimeout  time.Duration
	MaxResponseHeaderBytes int64
	DisableCompression     bool
	ForceRemoteSOCKS5      bool
	ProxyConnectHeader     http.Header
}

// ParseProxyURL validates a stored proxy URL without contacting it.  Empty
// input is rejected so a caller cannot accidentally turn a malformed proxy
// credential into a direct request.
func ParseProxyURL(raw string) (*url.URL, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil, ErrProxyURLInvalid
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed == nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Opaque != "" || parsed.Fragment != "" {
		return nil, ErrProxyURLInvalid
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5", "socks5h":
		return parsed, nil
	default:
		return nil, ErrProxySchemeUnsupported
	}
}

// NewTransport creates an explicitly configured transport.  Empty proxyURL
// means direct upstream access and intentionally disables HTTP(S)_PROXY
// environment lookup.  HTTP/2 is always enabled because a custom SOCKS dialer
// otherwise makes net/http parse an upstream HTTP/2 SETTINGS frame as HTTP/1.
func NewTransport(rawProxyURL string, options TransportOptions) (*http.Transport, error) {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok || base == nil {
		return nil, errors.New("http.DefaultTransport is not *http.Transport")
	}
	transport := base.Clone()
	transport.Proxy = nil
	transport.ForceAttemptHTTP2 = true
	transport.MaxIdleConns = 0
	transport.MaxIdleConnsPerHost = 0
	transport.MaxConnsPerHost = 0
	transport.ResponseHeaderTimeout = options.ResponseHeaderTimeout
	if options.MaxResponseHeaderBytes > 0 {
		transport.MaxResponseHeaderBytes = options.MaxResponseHeaderBytes
	} else {
		transport.MaxResponseHeaderBytes = DefaultMaxResponseHeaderBytes
	}
	transport.DisableCompression = options.DisableCompression
	if options.ProxyConnectHeader != nil {
		transport.ProxyConnectHeader = cloneHeader(options.ProxyConnectHeader)
	}

	if strings.TrimSpace(rawProxyURL) == "" {
		return transport, nil
	}
	proxyURL, err := ParseProxyURL(rawProxyURL)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(proxyURL.Scheme) {
	case "http", "https":
		transport.Proxy = http.ProxyURL(proxyURL)
	case "socks5", "socks5h":
		transport.DialContext = NewSOCKS5DialContext(proxyURL, strings.EqualFold(proxyURL.Scheme, "socks5h") || options.ForceRemoteSOCKS5)
	default:
		// ParseProxyURL currently makes this unreachable. Keep the branch so a
		// future scheme cannot silently fall back to direct connectivity.
		return nil, ErrProxySchemeUnsupported
	}
	return transport, nil
}

// NewClient creates the standard no-redirect client used by direct upstream
// checks.  Request-specific deadlines remain on the request context so this
// helper is also safe for streaming callers.
func NewClient(rawProxyURL string, options TransportOptions) (*http.Client, error) {
	transport, err := NewTransport(rawProxyURL, options)
	if err != nil {
		return nil, err
	}
	return NewClientWithTransport(transport), nil
}

// NewClientWithTransport applies the shared no-redirect policy to a transport
// that a caller needs to inspect or close itself (for example a bounded proxy
// probe). The transport remains owned by the caller.
func NewClientWithTransport(transport http.RoundTripper) *http.Client {
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func cloneHeader(source http.Header) http.Header {
	return source.Clone()
}
