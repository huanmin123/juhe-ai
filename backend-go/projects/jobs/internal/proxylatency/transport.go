package proxylatency

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
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
	client := upstreamhttp.NewClientWithTransport(transport)
	defer client.CloseIdleConnections()
	started := time.Now()
	response, err := client.Do(httpRequest)
	if err != nil {
		if errors.Is(requestCtx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return taskFailureResult("probe_cancelled")
		}
		return transportFailureResult(err)
	}
	defer response.Body.Close()
	if _, err := upstreamhttp.Drain(response.Body); err != nil {
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
	if raw == "" || strings.Contains(raw, "\\") || hasTargetControlCharacter(raw) {
		return nil, errors.New("target URL invalid")
	}
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil, errors.New("target URL invalid")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed == nil {
		return nil, errors.New("target URL invalid")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, errors.New("target URL scheme invalid")
	}
	colon := strings.IndexByte(value, ':')
	if colon <= 0 || colon+2 >= len(value) || !strings.EqualFold(value[:colon], scheme) || value[colon+1:colon+3] != "//" {
		return nil, errors.New("target URL authority invalid")
	}
	if parsed.Opaque != "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || strings.Contains(value, "#") {
		return nil, errors.New("target URL invalid")
	}
	if strings.Contains(parsed.Host, "%") {
		return nil, errors.New("target URL host invalid")
	}
	hostname := parsed.Hostname()
	if !validTargetHostname(hostname) {
		return nil, errors.New("target URL host invalid")
	}
	port, err := canonicalTargetPort(parsed.Host)
	if err != nil {
		return nil, err
	}
	path := parsed.Path
	if path == "" {
		path = "/"
	}
	if hasTargetControlCharacter(path) || strings.Contains(path, "//") || hasEncodedTargetByte(parsed.EscapedPath(), "2f") || hasEncodedTargetByte(parsed.EscapedPath(), "5c") {
		return nil, errors.New("target URL path invalid")
	}
	for _, segment := range strings.Split(path, "/") {
		decoded, err := url.PathUnescape(segment)
		if err != nil || decoded == "." || decoded == ".." || strings.ContainsAny(decoded, "/\\") || hasTargetControlCharacter(decoded) {
			return nil, errors.New("target URL path dot segment invalid")
		}
	}
	parsed.Scheme = scheme
	canonicalHost := strings.ToLower(hostname)
	if strings.Contains(hostname, ":") {
		canonicalHost = "[" + canonicalHost + "]"
	}
	if port != "" && !isDefaultTargetPort(scheme, port) {
		canonicalHost += ":" + port
	}
	parsed.Host = canonicalHost
	parsed.Path = path
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed, nil
}

func hasTargetControlCharacter(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func hasEncodedTargetByte(path, encoded string) bool {
	path = strings.ToLower(path)
	return strings.Contains(path, "%"+encoded)
}

func validTargetHostname(hostname string) bool {
	if hostname == "" || strings.HasPrefix(hostname, ".") || strings.Contains(hostname, "..") {
		return false
	}
	if net.ParseIP(hostname) != nil {
		return true
	}
	if strings.Contains(hostname, ":") || len(hostname) > 253 {
		return false
	}
	if strings.HasSuffix(hostname, ".") {
		hostname = strings.TrimSuffix(hostname, ".")
	}
	if hostname == "" {
		return false
	}
	for _, label := range strings.Split(hostname, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

func canonicalTargetPort(host string) (string, error) {
	port := ""
	if strings.HasPrefix(host, "[") {
		closing := strings.IndexByte(host, ']')
		if closing < 0 {
			return "", errors.New("target URL host invalid")
		}
		rest := host[closing+1:]
		if rest == "" {
			return "", nil
		}
		if !strings.HasPrefix(rest, ":") || len(rest) == 1 {
			return "", errors.New("target URL port invalid")
		}
		port = rest[1:]
	} else {
		colonCount := strings.Count(host, ":")
		if colonCount > 1 {
			return "", errors.New("target URL host invalid")
		}
		if colonCount == 1 {
			separator := strings.LastIndexByte(host, ':')
			if separator == 0 || separator == len(host)-1 {
				return "", errors.New("target URL port invalid")
			}
			port = host[separator+1:]
		}
	}
	if port == "" {
		return "", nil
	}
	parsed, err := strconv.ParseUint(port, 10, 16)
	if err != nil || parsed == 0 {
		return "", errors.New("target URL port invalid")
	}
	return strconv.FormatUint(parsed, 10), nil
}

func isDefaultTargetPort(scheme, port string) bool {
	return (scheme == "http" && port == "80") || (scheme == "https" && port == "443")
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
	if _, err := upstreamhttp.ParseProxyURL(rawProxyURL); err != nil {
		return nil, errors.New("proxy URL invalid")
	}
	// Node's http.request does not synthesize Accept-Encoding. Disable Go's
	// transparent gzip negotiation so the probe wire shape remains identical.
	// The shared transport also enables HTTP/2 for the SOCKS custom dialer and
	// keeps direct access independent from HTTP(S)_PROXY environment state.
	return upstreamhttp.NewTransport(rawProxyURL, upstreamhttp.TransportOptions{
		ResponseHeaderTimeout:  timeout,
		DisableCompression:     true,
		ForceRemoteSOCKS5:      true,
		MaxResponseHeaderBytes: 64 * 1024,
		ProxyConnectHeader:     http.Header{"Proxy-Connection": []string{"close"}},
	})
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
