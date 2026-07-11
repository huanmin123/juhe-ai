package accounthealthcheckdispatch

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	dispatchPath                          = "/__aiinternal__/v1/account-health-check/dispatch"
	signatureDomain                       = "juhe-ai:account-health-check-dispatch:v1\n"
	requestTimeout                        = 2 * time.Second
	transportKeepAlive                    = 30 * time.Second
	transportMaxIdleConns                 = 16
	transportMaxIdleConnsPerHost          = 8
	transportMaxConnsPerHost              = 16
	transportMaxResponseHeaderBytes       = 16 * 1024
	maxResponseDrainBytes           int64 = 8 * 1024
)

type Client struct {
	endpoint   string
	secret     []byte
	httpClient *http.Client
}

type dispatchPayload struct {
	AccountID string `json:"accountId"`
	Reason    string `json:"reason"`
}

func NewClient(rawBaseURL string, secret string) (*Client, error) {
	return NewClientWithTimeout(rawBaseURL, secret, requestTimeout)
}

func NewClientWithTimeout(rawBaseURL string, secret string, timeout time.Duration) (*Client, error) {
	if timeout <= 0 {
		return nil, errors.New("账户健康检查 dispatch timeout 必须大于 0")
	}
	baseURL, err := parseBaseURL(rawBaseURL)
	if err != nil {
		return nil, err
	}
	if secret == "" {
		return nil, errors.New("账户健康检查 dispatch secret 不能为空")
	}

	dialer := &net.Dialer{
		Timeout:   timeout,
		KeepAlive: transportKeepAlive,
	}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            dialer.DialContext,
		ForceAttemptHTTP2:      false,
		MaxIdleConns:           transportMaxIdleConns,
		MaxIdleConnsPerHost:    transportMaxIdleConnsPerHost,
		MaxConnsPerHost:        transportMaxConnsPerHost,
		IdleConnTimeout:        timeout,
		TLSHandshakeTimeout:    timeout,
		ResponseHeaderTimeout:  timeout,
		ExpectContinueTimeout:  timeout,
		MaxResponseHeaderBytes: transportMaxResponseHeaderBytes,
		DisableCompression:     true,
	}

	baseURL.Path = dispatchPath
	return &Client{
		endpoint: baseURL.String(),
		secret:   []byte(secret),
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   timeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (client *Client) Dispatch(ctx context.Context, accountID string, reason string) error {
	if ctx == nil {
		return errors.New("账户健康检查 dispatch context 不能为空")
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return errors.New("账户健康检查 dispatch account ID 不能为空")
	}
	if reason != "activation" && reason != "configuration" {
		return errors.New("账户健康检查 dispatch reason 必须是 activation 或 configuration")
	}

	rawBody, err := json.Marshal(dispatchPayload{
		AccountID: accountID,
		Reason:    reason,
	})
	if err != nil {
		return fmt.Errorf("编码账户健康检查 dispatch 请求失败: %w", err)
	}

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		client.endpoint,
		bytes.NewReader(rawBody),
	)
	if err != nil {
		return fmt.Errorf("创建账户健康检查 dispatch 请求失败: %w", err)
	}
	request.GetBody = nil
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Encoding", "identity")
	request.Header.Set("X-Juhe-AI-Signature", createSignature(client.secret, rawBody))

	response, err := client.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("发送账户健康检查 dispatch 请求失败: %w", err)
	}
	drainAndClose(response.Body)

	if response.StatusCode != http.StatusAccepted {
		statusText := http.StatusText(response.StatusCode)
		if statusText == "" {
			return fmt.Errorf("账户健康检查 dispatch 返回非预期 HTTP 状态: %d", response.StatusCode)
		}
		return fmt.Errorf(
			"账户健康检查 dispatch 返回非预期 HTTP 状态: %d %s",
			response.StatusCode,
			statusText,
		)
	}
	return nil
}

func parseBaseURL(rawBaseURL string) (*url.URL, error) {
	if strings.Contains(rawBaseURL, "#") {
		return nil, errors.New("账户健康检查 dispatch base URL 不允许 fragment")
	}
	baseURL, err := url.Parse(rawBaseURL)
	if err != nil {
		return nil, fmt.Errorf("解析账户健康检查 dispatch base URL 失败: %w", err)
	}
	if baseURL.Scheme != "http" {
		return nil, errors.New("账户健康检查 dispatch base URL 必须使用 http")
	}
	if baseURL.Opaque != "" || baseURL.Host == "" {
		return nil, errors.New("账户健康检查 dispatch base URL 格式无效")
	}
	if baseURL.User != nil {
		return nil, errors.New("账户健康检查 dispatch base URL 不允许 userinfo")
	}
	if baseURL.Path != "" || baseURL.RawPath != "" {
		return nil, errors.New("账户健康检查 dispatch base URL 不允许 path")
	}
	if baseURL.RawQuery != "" || baseURL.ForceQuery {
		return nil, errors.New("账户健康检查 dispatch base URL 不允许 query")
	}
	if baseURL.Fragment != "" {
		return nil, errors.New("账户健康检查 dispatch base URL 不允许 fragment")
	}

	host := baseURL.Hostname()
	port := baseURL.Port()
	if host == "" || port == "" {
		return nil, errors.New("账户健康检查 dispatch base URL 必须包含 IP 和显式端口")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return nil, errors.New("账户健康检查 dispatch base URL 端口无效")
	}

	ip := net.ParseIP(host)
	if ip == nil || !isAllowedLoopbackLiteral(host, ip) {
		return nil, errors.New("账户健康检查 dispatch base URL 仅允许 loopback IP literal")
	}
	return baseURL, nil
}

func isAllowedLoopbackLiteral(host string, ip net.IP) bool {
	if strings.Contains(host, ":") {
		return ip.Equal(net.IPv6loopback)
	}
	ipv4 := ip.To4()
	return ipv4 != nil && ipv4[0] == 127
}

func createSignature(secret []byte, rawBody []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(signatureDomain))
	_, _ = mac.Write(rawBody)
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}

func drainAndClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(body, maxResponseDrainBytes))
	_ = body.Close()
}
