package proxylatency

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxResponseBodyBytes = 512 * 1024

// ProbeItem validates the frozen request shape before any network activity.
// A complete HTTP response is a transport pass regardless of status code.
func ProbeItem(ctx context.Context, request ProbeRequest) ItemResult {
	if err := ctx.Err(); err != nil {
		return taskFailureResult("probe_cancelled")
	}
	target, err := parseTargetURL(request.TargetURL)
	if err != nil {
		return taskFailureResult("target_url_invalid")
	}
	if request.Timeout <= 0 {
		return taskFailureResult("timeout_invalid")
	}
	transport, err := newProxyTransport(request.ProxyURL, request.Timeout)
	if err != nil {
		return taskFailureResult(proxyConfigurationCode(err))
	}
	proxyURL, err := url.Parse(strings.TrimSpace(request.ProxyURL))
	if err != nil || proxyURL == nil {
		return taskFailureResult("proxy_invalid")
	}

	requestCtx, cancel := context.WithTimeout(ctx, request.Timeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(requestCtx, http.MethodGet, target.String(), nil)
	if err != nil {
		return taskFailureResult("request_build_failed")
	}
	applyNodeProbeHeaders(httpRequest, target, proxyURL)
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	started := time.Now()
	response, err := client.Do(httpRequest)
	if err != nil {
		if errors.Is(requestCtx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return taskFailureResult("probe_cancelled")
		}
		return transportFailureResult(err)
	}
	defer response.Body.Close()
	if err := discardBounded(response.Body, maxResponseBodyBytes); err != nil {
		return responseReadFailureResult(err)
	}
	result := ItemResult{
		Status:     ItemPassed,
		HTTPStatus: response.StatusCode,
		LatencyMS:  time.Since(started).Milliseconds(),
		Outcome:    OutcomeSuccess,
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		result.Outcome = OutcomeNeutral
		result.ErrorCode = "upstream_http_status"
	}
	return result
}

func parseTargetURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return nil, errors.New("target URL invalid")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("target URL scheme invalid")
	}
	return parsed, nil
}

// applyNodeProbeHeaders mirrors the two request shapes in Node's
// proxy-test.service. Forward HTTP proxy requests carry the target Host and
// close headers on the request sent to the proxy. CONNECT/SOCKS requests carry
// only the probe identity headers; their proxy-specific headers belong to the
// CONNECT handshake (configured below), never to the tunneled target request.
func applyNodeProbeHeaders(request *http.Request, target, proxyURL *url.URL) {
	request.Header.Set("Accept", "application/json,text/plain,*/*")
	request.Header.Set("User-Agent", "juhe-ai-proxy-test/0.1")
	if target == nil || proxyURL == nil || target.Scheme != "http" || !isForwardProxyScheme(proxyURL.Scheme) {
		return
	}
	request.Header.Set("Connection", "close")
	request.Header.Set("Proxy-Connection", "close")
	request.Host = target.Host
	request.Close = true
}

func isForwardProxyScheme(scheme string) bool {
	return scheme == "http" || scheme == "https"
}

func newProxyTransport(rawProxyURL string, timeout time.Duration) (*http.Transport, error) {
	if strings.TrimSpace(rawProxyURL) == "" {
		return nil, errors.New("proxy required")
	}
	proxyURL, err := url.Parse(strings.TrimSpace(rawProxyURL))
	if err != nil || proxyURL == nil || proxyURL.Host == "" {
		return nil, errors.New("proxy URL invalid")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ForceAttemptHTTP2 = false
	// Node's http.request does not synthesize Accept-Encoding. Disable Go's
	// transparent gzip negotiation so the probe wire shape remains identical.
	transport.DisableCompression = true
	transport.MaxIdleConns = 4
	transport.MaxIdleConnsPerHost = 1
	transport.MaxConnsPerHost = 1
	transport.ResponseHeaderTimeout = timeout
	// The proxy CONNECT response is parsed by net/http using this limit. Keep
	// the limit explicit instead of inheriting Go's much larger default.
	transport.MaxResponseHeaderBytes = 64 * 1024
	switch proxyURL.Scheme {
	case "http", "https":
		transport.Proxy = http.ProxyURL(proxyURL)
		// Node's custom CONNECT agent closes the proxy handshake. This header
		// is intentionally scoped to CONNECT by net/http's ProxyConnectHeader;
		// it must not leak into a tunneled HTTPS/SOCKS target request.
		transport.ProxyConnectHeader = http.Header{"Proxy-Connection": []string{"close"}}
		if proxyURL.User != nil {
			username := proxyURL.User.Username()
			password, _ := proxyURL.User.Password()
			credentials := username + ":" + password
			transport.ProxyConnectHeader.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(credentials)))
		}
	case "socks5", "socks5h":
		transport.Proxy = nil
		// Node's repository maps both stored SOCKS variants to socks5h. The
		// effective contract therefore delegates hostname resolution remotely
		// for both `socks5` and `socks5h` inputs.
		transport.DialContext = newSOCKS5DialContext(proxyURL, true)
	default:
		return nil, errors.New("proxy scheme invalid")
	}
	return transport, nil
}

func discardBounded(body io.Reader, maxBytes int64) error {
	if maxBytes <= 0 {
		return errors.New("response body limit invalid")
	}
	// maxBytes limits retained/diagnostic data, not framing. Continue reading
	// until EOF so a complete oversized response remains a passed/neutral item
	// exactly like Node's bounded collector.
	var retained int64
	buffer := make([]byte, 32*1024)
	for {
		read, err := body.Read(buffer)
		if read > 0 && retained < maxBytes {
			retained += int64(read)
			if retained > maxBytes {
				retained = maxBytes
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func taskFailureResult(code string) ItemResult {
	return ItemResult{Status: ItemUnknown, Outcome: OutcomeProbeTaskFailure, ErrorCode: code}
}

func transportFailureResult(err error) ItemResult {
	if errors.Is(err, context.DeadlineExceeded) {
		return ItemResult{Status: ItemFailed, Outcome: OutcomeUpstreamFailure, ErrorCode: "timeout"}
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) && networkErr.Timeout() {
		return ItemResult{Status: ItemFailed, Outcome: OutcomeUpstreamFailure, ErrorCode: "timeout"}
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return ItemResult{Status: ItemFailed, Outcome: OutcomeUpstreamFailure, ErrorCode: "dns"}
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return ItemResult{Status: ItemFailed, Outcome: OutcomeUpstreamFailure, ErrorCode: "early_eof"}
	}
	return ItemResult{Status: ItemFailed, Outcome: OutcomeUpstreamFailure, ErrorCode: "transport"}
}

func responseReadFailureResult(err error) ItemResult {
	if errors.Is(err, context.DeadlineExceeded) {
		return ItemResult{Status: ItemFailed, Outcome: OutcomeUpstreamFailure, ErrorCode: "timeout"}
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) && networkErr.Timeout() {
		return ItemResult{Status: ItemFailed, Outcome: OutcomeUpstreamFailure, ErrorCode: "timeout"}
	}
	return ItemResult{Status: ItemFailed, Outcome: OutcomeUpstreamFailure, ErrorCode: "early_eof"}
}

func proxyConfigurationCode(err error) string {
	if strings.Contains(err.Error(), "required") {
		return "proxy_required"
	}
	return "proxy_invalid"
}
