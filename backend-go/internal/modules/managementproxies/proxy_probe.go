package managementproxies

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	xproxy "golang.org/x/net/proxy"
)

const maxProxyProbeResponseBytes = 512 * 1024

type defaultProxyProbe struct{}

type proxyProbeFailureKind string

const (
	proxyProbeFailureUnknown   proxyProbeFailureKind = "unknown"
	proxyProbeFailureTransport proxyProbeFailureKind = "transport"
)

// proxyProbeFailure retains whether a probe reached the transport layer. A
// malformed local configuration must not be reported as a broken proxy, while
// a request or a complete-response read failure is actionable transport
// evidence.
type proxyProbeFailure struct {
	kind proxyProbeFailureKind
	err  error
}

func (e *proxyProbeFailure) Error() string { return e.err.Error() }

func (e *proxyProbeFailure) Unwrap() error { return e.err }

func unknownProxyProbeFailure(err error) error {
	return &proxyProbeFailure{kind: proxyProbeFailureUnknown, err: err}
}

func transportProxyProbeFailure(err error) error {
	return &proxyProbeFailure{kind: proxyProbeFailureTransport, err: err}
}

func isTransportProxyProbeFailure(err error) bool {
	var failure *proxyProbeFailure
	return errors.As(err, &failure) && failure.kind == proxyProbeFailureTransport
}

func newDefaultProxyProbe() ProxyProbe {
	return defaultProxyProbe{}
}

func (defaultProxyProbe) Probe(ctx context.Context, input ProxyProbeInput) (ProxyProbeResult, error) {
	target, err := url.Parse(input.TargetURL)
	if err != nil {
		return ProxyProbeResult{}, unknownProxyProbeFailure(fmt.Errorf("解析检测地址失败: %w", err))
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return ProxyProbeResult{}, unknownProxyProbeFailure(fmt.Errorf("检测地址协议无效"))
	}
	proxyAddress, err := url.Parse(input.ProxyURL)
	if err != nil {
		return ProxyProbeResult{}, unknownProxyProbeFailure(fmt.Errorf("解析代理地址失败: %w", err))
	}
	timeout := input.Timeout
	if timeout <= 0 || timeout > proxyProbeTimeout {
		timeout = proxyProbeTimeout
	}
	transport := &http.Transport{
		DisableKeepAlives:     true,
		ForceAttemptHTTP2:     false,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
	}
	switch normalizedProxyScheme(proxyAddress.Scheme) {
	case "http", "https":
		transport.Proxy = http.ProxyURL(proxyAddress)
	case "socks5", "socks5h":
		dialContext, err := socksProxyDialContext(proxyAddress, timeout)
		if err != nil {
			return ProxyProbeResult{}, unknownProxyProbeFailure(err)
		}
		transport.DialContext = dialContext
	default:
		return ProxyProbeResult{}, unknownProxyProbeFailure(fmt.Errorf("代理协议无效"))
	}
	defer transport.CloseIdleConnections()

	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, target.String(), nil)
	if err != nil {
		return ProxyProbeResult{}, unknownProxyProbeFailure(err)
	}
	request.Header.Set("Accept", "application/json,text/plain,*/*")
	request.Header.Set("User-Agent", "juhe-ai-proxy-test/0.1")

	startedAt := time.Now()
	response, err := (&http.Client{Transport: transport}).Do(request)
	if err != nil {
		if probeCtx.Err() != nil {
			return ProxyProbeResult{}, transportProxyProbeFailure(fmt.Errorf("代理检测请求超时: %w", probeCtx.Err()))
		}
		return ProxyProbeResult{}, transportProxyProbeFailure(err)
	}
	defer response.Body.Close()
	body, err := readBoundedProxyProbeBody(response.Body)
	if err != nil {
		return ProxyProbeResult{}, transportProxyProbeFailure(err)
	}
	return ProxyProbeResult{
		StatusCode: response.StatusCode,
		Body:       string(body),
		LatencyMs:  int(time.Since(startedAt).Round(time.Millisecond) / time.Millisecond),
	}, nil
}

// readBoundedProxyProbeBody keeps at most the diagnostic payload limit but
// always drains the response to EOF. That makes a returned HTTP status proof
// of complete response framing rather than merely received response headers.
func readBoundedProxyProbeBody(body io.Reader) ([]byte, error) {
	limited := &io.LimitedReader{R: body, N: maxProxyProbeResponseBytes + 1}
	value, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(io.Discard, body); err != nil {
		return nil, err
	}
	if len(value) > maxProxyProbeResponseBytes {
		value = value[:maxProxyProbeResponseBytes]
	}
	return value, nil
}

func socksProxyDialContext(proxyAddress *url.URL, timeout time.Duration) (func(context.Context, string, string) (net.Conn, error), error) {
	var auth *xproxy.Auth
	if proxyAddress.User != nil {
		password, _ := proxyAddress.User.Password()
		auth = &xproxy.Auth{
			User:     proxyAddress.User.Username(),
			Password: password,
		}
	}
	baseDialer := &net.Dialer{
		Timeout:   timeout,
		KeepAlive: 30 * time.Second,
	}
	dialer, err := xproxy.SOCKS5("tcp", proxyAddress.Host, auth, baseDialer)
	if err != nil {
		return nil, fmt.Errorf("创建 SOCKS 代理连接失败: %w", err)
	}
	if contextDialer, ok := dialer.(xproxy.ContextDialer); ok {
		return contextDialer.DialContext, nil
	}
	return func(ctx context.Context, network string, address string) (net.Conn, error) {
		type dialResult struct {
			conn net.Conn
			err  error
		}
		result := make(chan dialResult, 1)
		go func() {
			conn, dialErr := dialer.Dial(network, address)
			result <- dialResult{conn: conn, err: dialErr}
		}()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case value := <-result:
			return value.conn, value.err
		}
	}, nil
}

func normalizedProxyScheme(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
