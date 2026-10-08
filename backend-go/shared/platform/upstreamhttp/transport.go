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

// DefaultMaxIdleConnsPerHost is the idle-connection keep-alive pool per
// upstream host applied when TransportOptions leaves it unset. Without it the
// cloned http.DefaultTransport keeps its DefaultMaxIdleConnsPerHost of 2,
// which forces constant re-dial + TLS handshakes against HTTP/1.1 upstreams
// under gateway fan-out.
const DefaultMaxIdleConnsPerHost = 64

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
	// MaxIdleConnsPerHost overrides the per-host idle pool; zero applies
	// DefaultMaxIdleConnsPerHost instead of the stdlib fallback of 2.
	MaxIdleConnsPerHost int
	DisableCompression  bool
	ForceRemoteSOCKS5   bool
	ProxyConnectHeader  http.Header
	// DialGuard installs the SSRF validated-dial hook (D-192/D-146): direct
	// and HTTP(S)-proxy transports resolve and validate the connect target
	// through the guard before any socket is established. SOCKS dialers keep
	// their own resolution and ignore the guard.
	DialGuard *DialGuard
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
	if options.MaxIdleConnsPerHost > 0 {
		transport.MaxIdleConnsPerHost = options.MaxIdleConnsPerHost
	} else {
		transport.MaxIdleConnsPerHost = DefaultMaxIdleConnsPerHost
	}
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

	// DialGuard 装配（BUG-0175 既有缺陷的修复）：此前安装条件
	// `transport.DialContext == nil` 恒为假（Clone 自 http.DefaultTransport 的
	// DialContext 非 nil），且直连路径在本段之前提前返回，导致
	// TransportOptions.DialGuard 从未生效。按 TransportOptions 契约在直连与
	// HTTP(S) 代理路径安装 guard（校验实际 socket 对端：直连为上游主机，代理
	// 为代理主机）；SOCKS 拨号器保持自身解析（socks5h 为远端解析），不受影响。
	installDialGuard := func() {
		if options.DialGuard != nil {
			transport.DialContext = options.DialGuard.DialContext
		}
	}
	if strings.TrimSpace(rawProxyURL) == "" {
		installDialGuard()
		return transport, nil
	}
	proxyURL, err := ParseProxyURL(rawProxyURL)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(proxyURL.Scheme) {
	case "http", "https":
		transport.Proxy = http.ProxyURL(proxyURL)
		installDialGuard()
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
