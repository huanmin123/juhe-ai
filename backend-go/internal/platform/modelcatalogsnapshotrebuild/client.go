package modelcatalogsnapshotrebuild

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
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	rebuildPath              = "/__aiinternal__/v1/model-catalog-snapshots/rebuild"
	readinessPath            = "/__aiinternal__/v1/model-catalog-snapshots/readyz"
	rebuildSignatureDomain   = "juhe-ai:model-catalog-snapshot-rebuild:v1\n"
	readinessSignatureDomain = "juhe-ai:model-catalog-snapshot-readiness:v1\n"
	maxProbeResponseBytes    = 4 * 1024
)

type Client struct {
	rebuildEndpoint string
	probeEndpoint   string
	secret          []byte
	rebuildHTTP     *http.Client
	probeHTTP       *http.Client
}

type rebuildPayload struct {
	Scope           string `json:"scope"`
	SystemAccountID string `json:"systemAccountId,omitempty"`
}

func NewClient(rawBaseURL, secret string) (*Client, error) {
	return NewClientWithTimeouts(rawBaseURL, secret, time.Minute, 2*time.Second)
}

func NewClientWithTimeouts(rawBaseURL, secret string, rebuildTimeout, probeTimeout time.Duration) (*Client, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return nil, errors.New("模型目录快照重建 secret 不能为空")
	}
	baseURL, err := parseBaseURL(rawBaseURL)
	if err != nil {
		return nil, err
	}
	if rebuildTimeout < time.Second || rebuildTimeout > 5*time.Minute {
		return nil, errors.New("模型目录快照重建 timeout 必须在 1s 到 5m 之间")
	}
	if probeTimeout < 100*time.Millisecond || probeTimeout > 10*time.Second {
		return nil, errors.New("模型目录快照 readiness timeout 必须在 100ms 到 10s 之间")
	}
	rebuildURL := *baseURL
	rebuildURL.Path = rebuildPath
	probeURL := *baseURL
	probeURL.Path = readinessPath
	return &Client{
		rebuildEndpoint: rebuildURL.String(),
		probeEndpoint:   probeURL.String(),
		secret:          []byte(secret),
		rebuildHTTP:     newHTTPClient(rebuildTimeout),
		probeHTTP:       newHTTPClient(probeTimeout),
	}, nil
}

func newHTTPClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            dialer.DialContext,
		ForceAttemptHTTP2:      false,
		MaxIdleConns:           16,
		MaxIdleConnsPerHost:    8,
		MaxConnsPerHost:        16,
		IdleConnTimeout:        30 * time.Second,
		TLSHandshakeTimeout:    timeout,
		ResponseHeaderTimeout:  timeout,
		ExpectContinueTimeout:  timeout,
		MaxResponseHeaderBytes: 16 * 1024,
		DisableCompression:     true,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (c *Client) Rebuild(ctx context.Context, scope, systemAccountID string) error {
	if ctx == nil {
		return errors.New("模型目录快照重建 context 不能为空")
	}
	scope = strings.TrimSpace(scope)
	systemAccountID = strings.TrimSpace(systemAccountID)
	if scope != "all" && scope != "personal" {
		return errors.New("模型目录快照重建 scope 必须是 all 或 personal")
	}
	if scope == "personal" && systemAccountID == "" {
		return errors.New("个人模型目录快照重建必须提供系统账户 ID")
	}
	if scope == "all" && systemAccountID != "" {
		return errors.New("全量模型目录快照重建不能提供系统账户 ID")
	}
	raw, err := json.Marshal(rebuildPayload{Scope: scope, SystemAccountID: systemAccountID})
	if err != nil {
		return fmt.Errorf("编码模型目录快照重建请求失败: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.rebuildEndpoint, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("创建模型目录快照重建请求失败: %w", err)
	}
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "identity")
	req.Header.Set("X-Juhe-AI-Signature", signature(c.secret, rebuildSignatureDomain, raw))
	resp, err := c.rebuildHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("发送模型目录快照重建请求失败: %w", err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8*1024))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("模型目录快照重建返回非预期 HTTP 状态: %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	return nil
}

type ProbeFailureKind string

const (
	ProbeFailureUnreachable           ProbeFailureKind = "unreachable"
	ProbeFailureUnauthorized          ProbeFailureKind = "unauthorized"
	ProbeFailureNotFound              ProbeFailureKind = "not_found"
	ProbeFailureDependencyUnavailable ProbeFailureKind = "dependency_unavailable"
	ProbeFailureInvalidResponse       ProbeFailureKind = "invalid_response"
	ProbeFailureHTTPStatus            ProbeFailureKind = "http_status"
)

type ProbeError struct {
	Kind ProbeFailureKind
}

func (e *ProbeError) Error() string {
	return "Node 模型目录快照 bridge readiness 检查失败: " + string(e.Kind)
}

type probeResponse struct {
	Ready           bool   `json:"ready"`
	Component       string `json:"component"`
	ContractVersion int    `json:"contractVersion"`
	DatabaseDriver  string `json:"databaseDriver"`
	SchemaVersion   int64  `json:"schemaVersion"`
}

func (c *Client) Probe(ctx context.Context) error {
	if ctx == nil {
		return errors.New("模型目录快照 readiness context 不能为空")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.probeEndpoint, nil)
	if err != nil {
		return &ProbeError{Kind: ProbeFailureInvalidResponse}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Juhe-AI-Signature", signature(c.secret, readinessSignatureDomain, nil))
	resp, err := c.probeHTTP.Do(req)
	if err != nil {
		return &ProbeError{Kind: ProbeFailureUnreachable}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxProbeResponseBytes))
		return &ProbeError{Kind: probeHTTPFailure(resp.StatusCode)}
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return &ProbeError{Kind: ProbeFailureInvalidResponse}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxProbeResponseBytes+1))
	if err != nil || len(raw) > maxProbeResponseBytes {
		return &ProbeError{Kind: ProbeFailureInvalidResponse}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var result probeResponse
	if err := decoder.Decode(&result); err != nil {
		return &ProbeError{Kind: ProbeFailureInvalidResponse}
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return &ProbeError{Kind: ProbeFailureInvalidResponse}
	}
	if !result.Ready || result.Component != "model-catalog-snapshot-rebuild" || result.ContractVersion != 1 || result.DatabaseDriver != "postgres" || result.SchemaVersion != 63 {
		return &ProbeError{Kind: ProbeFailureInvalidResponse}
	}
	return nil
}

func probeHTTPFailure(statusCode int) ProbeFailureKind {
	switch statusCode {
	case http.StatusUnauthorized:
		return ProbeFailureUnauthorized
	case http.StatusNotFound:
		return ProbeFailureNotFound
	case http.StatusServiceUnavailable:
		return ProbeFailureDependencyUnavailable
	default:
		return ProbeFailureHTTPStatus
	}
}

func parseBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Opaque != "" || u.Host == "" || u.User != nil || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, errors.New("模型目录快照重建 base URL 必须是无 path/query 的 loopback HTTP 地址")
	}
	host, port := u.Hostname(), u.Port()
	if host == "" || port == "" {
		return nil, errors.New("模型目录快照重建 base URL 必须包含 loopback IP 和显式端口")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return nil, errors.New("模型目录快照重建 base URL 端口无效")
	}
	ip := net.ParseIP(host)
	if ip == nil || !(ip.To4() != nil && ip.To4()[0] == 127) && !ip.Equal(net.IPv6loopback) {
		return nil, errors.New("模型目录快照重建 base URL 仅允许 loopback IP literal")
	}
	return u, nil
}

func signature(secret []byte, domain string, raw []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(domain))
	_, _ = mac.Write(raw)
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}
