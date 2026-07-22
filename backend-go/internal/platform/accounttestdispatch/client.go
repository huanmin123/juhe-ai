package accounttestdispatch

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
	dispatchPath                = "/__aiinternal__/v1/account-test/dispatch"
	cancelPath                  = "/__aiinternal__/v1/account-test/cancel"
	signatureDomain             = "juhe-ai:account-test-dispatch:v1\n"
	cancelSignatureDomain       = "juhe-ai:account-test-cancel:v1\n"
	requestTimeout              = 2 * time.Second
	maxResponseDrainBytes int64 = 8 * 1024
)

type Client struct {
	dispatchEndpoint string
	cancelEndpoint   string
	secret           []byte
	httpClient       *http.Client
}

type dispatchPayload struct {
	Version int    `json:"version"`
	TaskID  string `json:"taskId"`
}

func NewClient(rawBaseURL, secret string) (*Client, error) {
	return NewClientWithTimeout(rawBaseURL, secret, requestTimeout)
}

func NewClientWithTimeout(rawBaseURL, secret string, timeout time.Duration) (*Client, error) {
	if timeout <= 0 {
		return nil, errors.New("账户测试 dispatch timeout 必须大于 0")
	}
	baseURL, err := parseBaseURL(rawBaseURL)
	if err != nil {
		return nil, err
	}
	if secret == "" {
		return nil, errors.New("账户测试 dispatch secret 不能为空")
	}
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy: nil, DialContext: dialer.DialContext, ForceAttemptHTTP2: false,
		MaxIdleConns: 64, MaxIdleConnsPerHost: 64, MaxConnsPerHost: 0,
		IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: timeout,
		ResponseHeaderTimeout: timeout, ExpectContinueTimeout: timeout,
		MaxResponseHeaderBytes: 16 * 1024, DisableCompression: true,
	}
	dispatchURL := *baseURL
	dispatchURL.Path = dispatchPath
	cancelURL := *baseURL
	cancelURL.Path = cancelPath
	return &Client{
		dispatchEndpoint: dispatchURL.String(),
		cancelEndpoint:   cancelURL.String(),
		secret:           []byte(secret),
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   timeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (c *Client) Dispatch(ctx context.Context, taskID string) error {
	return c.send(ctx, taskID, c.dispatchEndpoint, []byte(signatureDomain), "dispatch")
}

func (c *Client) Cancel(ctx context.Context, taskID string) error {
	return c.send(ctx, taskID, c.cancelEndpoint, []byte(cancelSignatureDomain), "cancel")
}

func (c *Client) send(ctx context.Context, taskID, endpoint string, domain []byte, action string) error {
	if ctx == nil {
		return fmt.Errorf("账户测试 %s context 不能为空", action)
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return fmt.Errorf("账户测试 %s task ID 不能为空", action)
	}
	body, err := json.Marshal(dispatchPayload{Version: 1, TaskID: taskID})
	if err != nil {
		return fmt.Errorf("编码账户测试 %s 请求失败: %w", action, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("创建账户测试 %s 请求失败: %w", action, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "identity")
	req.Header.Set("X-Juhe-AI-Signature", createSignature(c.secret, body, domain))
	response, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("发送账户测试 %s 请求失败: %w", action, err)
	}
	drainAndClose(response.Body)
	if response.StatusCode != http.StatusAccepted {
		return fmt.Errorf("账户测试 %s 返回非预期 HTTP 状态: %d %s", action, response.StatusCode, http.StatusText(response.StatusCode))
	}
	return nil
}

func parseBaseURL(raw string) (*url.URL, error) {
	baseURL, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("解析账户测试 dispatch base URL 失败: %w", err)
	}
	if baseURL.Scheme != "http" || baseURL.Opaque != "" || baseURL.Host == "" || baseURL.User != nil ||
		baseURL.Path != "" || baseURL.RawPath != "" || baseURL.RawQuery != "" || baseURL.ForceQuery || baseURL.Fragment != "" {
		return nil, errors.New("账户测试 dispatch base URL 格式无效")
	}
	port, err := strconv.Atoi(baseURL.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("账户测试 dispatch base URL 必须包含有效显式端口")
	}
	host := baseURL.Hostname()
	ip := net.ParseIP(host)
	if ip == nil || !(ip.IsLoopback() && (strings.Contains(host, ":") && ip.Equal(net.IPv6loopback) || !strings.Contains(host, ":") && ip.To4() != nil)) {
		return nil, errors.New("账户测试 dispatch base URL 仅允许 loopback IP literal")
	}
	return baseURL, nil
}

func createSignature(secret, body, domain []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(domain)
	_, _ = mac.Write(body)
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}

func drainAndClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(body, maxResponseDrainBytes))
	_ = body.Close()
}
