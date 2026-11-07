package poolhealth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accountcrypto"
	"github.com/huanminabc/juhe-ai/backend-go-platform/proxyresolve"
)

// ErrProbeConfiguration means the probe could not be attempted because the
// stored proxy row is invalid (bad scheme/host/port, undecryptable password).
// These are not proxy-availability failures: streak/status stay unchanged.
var ErrProbeConfiguration = errors.New("proxy pool probe configuration invalid")

type passwordDecrypter struct {
	secret string
}

func (d passwordDecrypter) DecryptProxyPassword(_ context.Context, encrypted string) (string, error) {
	var envelope struct {
		Password *string `json:"password"`
	}
	if err := accountcrypto.DecryptJSON(d.secret, encrypted, &envelope); err != nil {
		return "", err
	}
	if envelope.Password == nil {
		return "", errors.New("代理密码缺失")
	}
	return *envelope.Password, nil
}

// HTTPProber performs the fixed 204 probe through the stored proxy.
type HTTPProber struct {
	secret   string
	endpoint string
	timeout  time.Duration
}

func NewHTTPProber(secret, endpoint string, timeout time.Duration) *HTTPProber {
	if strings.TrimSpace(endpoint) == "" {
		endpoint = DefaultEndpoint
	}
	if timeout <= 0 {
		timeout = DefaultProbeTimeout
	}
	return &HTTPProber{secret: secret, endpoint: endpoint, timeout: timeout}
}

func (p *HTTPProber) Probe(ctx context.Context, target Target) error {
	profile := proxyresolve.Profile{
		ID:                target.ProfileID,
		Name:              target.Name,
		Type:              target.Type,
		Host:              target.Host,
		Port:              target.Port,
		Username:          target.Username,
		PasswordEncrypted: target.PasswordEncrypted,
		Enabled:           true,
	}
	proxyURL, err := proxyresolve.BuildProfileURL(ctx, profile, passwordDecrypter{secret: p.secret})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrProbeConfiguration, err)
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrProbeConfiguration, err)
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyURL(parsed),
		TLSHandshakeTimeout:   p.timeout,
		ResponseHeaderTimeout: p.timeout,
		DisableKeepAlives:     true,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   p.timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	defer transport.CloseIdleConnections()

	probeCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, p.endpoint, nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrProbeConfiguration, err)
	}
	response, err := client.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("代理可用性探测失败: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("代理可用性探测返回 %d，要求 204", response.StatusCode)
	}
	return nil
}
