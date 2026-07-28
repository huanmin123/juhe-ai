package accountprobe

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/platform/upstreamtransport"
	"juhe-ai/backend-go/internal/platform/upstreamurlpolicy"
)

type TransportFactory struct {
	Timeout              time.Duration
	MaxResponseBodyBytes int64
	TLSConfig            *tls.Config
	URLPolicy            upstreamurlpolicy.Config
}

func (f TransportFactory) New(candidate gatewaycandidatewindow.Candidate) (AttemptTransport, error) {
	proxyURL, err := candidateProxyURL(candidate.Proxy)
	if err != nil {
		return nil, err
	}
	client, err := upstreamtransport.NewClient(upstreamtransport.Options{
		Timeout: f.Timeout, MaxResponseBodyBytes: f.MaxResponseBodyBytes,
		ProxyURL: proxyURL, TLSConfig: f.TLSConfig, URLPolicy: f.URLPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("create account probe upstream transport: %w", err)
	}
	return client, nil
}

func candidateProxyURL(proxy *gatewaycandidatewindow.ProxyRuntime) (string, error) {
	if proxy == nil {
		return "", nil
	}
	if !proxy.Enabled || !proxy.Available {
		return "", fmt.Errorf("account probe proxy %q is unavailable: %s", strings.TrimSpace(proxy.ID), strings.TrimSpace(proxy.UnavailableReason))
	}
	scheme := strings.ToLower(strings.TrimSpace(proxy.Type))
	if scheme == "socks5" {
		scheme = "socks5h"
	}
	if scheme != "http" && scheme != "https" && scheme != "socks5h" {
		return "", fmt.Errorf("account probe proxy type %q is unsupported", proxy.Type)
	}
	host := strings.Trim(strings.TrimSpace(proxy.Host), "[]")
	if host == "" || proxy.Port < 1 || proxy.Port > 65535 || strings.ContainsAny(host, "\r\n\x00") {
		return "", fmt.Errorf("account probe proxy address is invalid")
	}
	proxyURL := &url.URL{Scheme: scheme, Host: net.JoinHostPort(host, strconv.Itoa(proxy.Port))}
	username := strings.TrimSpace(proxy.Username)
	if username != "" {
		if password, ok := proxy.Credentials.StringValue("password"); ok {
			proxyURL.User = url.UserPassword(username, password)
		} else {
			proxyURL.User = url.User(username)
		}
	}
	return proxyURL.String(), nil
}

var _ interface {
	New(gatewaycandidatewindow.Candidate) (AttemptTransport, error)
} = TransportFactory{}
