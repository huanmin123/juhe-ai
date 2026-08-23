package accounthealth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

const (
	probeChallenge        = "juhe"
	probeInstructions     = "You are ChatGPT, a helpful assistant."
	defaultMaxBodyBytes   = int64(256 * 1024)
	defaultProbeUserAgent = "juhe-ai-jobs-account-health/1"
)

type ProbeOptions struct {
	Secret           string
	Timeout          time.Duration
	MaxResponseBytes int64
	Now              func() time.Time
}

// ProbeOpenAI executes one direct probe. It intentionally has no dependency
// on Node, Gateway, IPC, Redis, routing, model mapping, or usage writers.
func ProbeOpenAI(ctx context.Context, input Input, credential CredentialEnvelope, options ProbeOptions) ProbeResult {
	if err := validateInput(input, options); err != nil {
		return taskFailure("invalid_input", err.Error())
	}
	token, err := decryptToken(options.Secret, credential)
	if err != nil {
		return taskFailure("credential_unavailable", "凭据不可用")
	}
	baseURL, err := parseBaseURL(input.BaseURL, input.AllowInsecureBaseURL)
	if err != nil {
		return taskFailure("base_url_invalid", err.Error())
	}
	client, err := probeHTTPClient(input, options)
	if err != nil {
		return taskFailure("proxy_unavailable", err.Error())
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := buildProbeRequest(requestCtx, baseURL, input, token)
	if err != nil {
		return taskFailure("request_build_failed", err.Error())
	}
	response, err := client.Do(request)
	if err != nil {
		return transportFailure(err)
	}
	defer response.Body.Close()
	maxBytes := options.MaxResponseBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxBodyBytes
	}
	body, err := upstreamhttp.ReadBounded(response.Body, maxBytes)
	if err != nil {
		return responseReadFailure(err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return neutral(response.StatusCode, "upstream_http_status", "上游返回非成功状态")
	}
	if err := verifyResponse(input.EndpointMode, input.HealthModel, body); err != nil {
		return neutral(response.StatusCode, "upstream_protocol_invalid", "上游响应未满足探活语义")
	}
	return ProbeResult{Outcome: OutcomeSuccess, StatusCode: response.StatusCode}
}

func validateInput(input Input, options ProbeOptions) error {
	if strings.TrimSpace(input.AccountID) == "" || input.InputVersion <= 0 || input.ConfigRevision <= 0 || input.DispatchRevision <= 0 {
		return errors.New("input fence 无效")
	}
	if strings.TrimSpace(input.Provider) != "openai" || (input.Type != "api_key" && input.Type != "oauth") {
		return errors.New("未冻结的 provider 或账户类型")
	}
	switch input.EndpointMode {
	case "chat_json", "responses_json", "responses_sse", "images_json":
	default:
		return errors.New("未冻结的探活 endpoint mode")
	}
	if strings.TrimSpace(input.HealthModel) == "" {
		return errors.New("health model 缺失")
	}
	now := time.Now().UTC()
	if options.Now != nil {
		now = options.Now().UTC()
	}
	if input.ExpiresAt.IsZero() || !input.ExpiresAt.After(now) {
		return errors.New("input 已过期")
	}
	if input.Type == "oauth" && (input.OAuthExpiresAt == nil || !input.OAuthExpiresAt.After(now.Add(time.Minute))) {
		return errors.New("OAuth access token 已到期或接近到期")
	}
	if input.Type == "oauth" && input.EndpointMode != "responses_json" {
		return errors.New("OAuth 仅允许独立冻结的 responses_json 探活")
	}
	return nil
}

func probeTransport(input Input, options ProbeOptions) (*http.Transport, error) {
	proxyURL, timeout, err := probeTransportConfig(input, options)
	if err != nil {
		return nil, err
	}
	return upstreamhttp.NewTransport(proxyURL, upstreamhttp.TransportOptions{ResponseHeaderTimeout: timeout})
}

func probeHTTPClient(input Input, options ProbeOptions) (*http.Client, error) {
	proxyURL, timeout, err := probeTransportConfig(input, options)
	if err != nil {
		return nil, err
	}
	return upstreamhttp.SharedClient(proxyURL, upstreamhttp.TransportOptions{ResponseHeaderTimeout: timeout})
}

func probeTransportConfig(input Input, options ProbeOptions) (string, time.Duration, error) {
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	if input.Proxy == nil {
		return "", timeout, nil
	}
	proxyText, err := decryptToken(options.Secret, *input.Proxy)
	if err != nil {
		return "", 0, errors.New("代理凭据不可用")
	}
	if _, err := upstreamhttp.ParseProxyURL(proxyText); err != nil {
		if errors.Is(err, upstreamhttp.ErrProxySchemeUnsupported) {
			return "", 0, errors.New("未支持的代理协议")
		}
		return "", 0, errors.New("代理 URL 无效")
	}
	return proxyText, timeout, nil
}

func buildProbeRequest(ctx context.Context, base *url.URL, input Input, token string) (*http.Request, error) {
	path := ""
	method := http.MethodPost
	var body any
	switch input.EndpointMode {
	case "chat_json":
		path = "/v1/chat/completions"
		body = map[string]any{
			"model":      input.HealthModel,
			"messages":   []map[string]any{{"role": "user", "content": "只能回复：juhe"}},
			"max_tokens": 256,
			"stream":     false,
		}
	case "responses_json":
		path = "/v1/responses"
		if input.Type == "oauth" {
			path = "/responses"
		}
		body = map[string]any{
			"model":             input.HealthModel,
			"input":             []map[string]any{{"role": "user", "content": []map[string]any{{"type": "input_text", "text": "只能回复：juhe"}}}},
			"instructions":      probeInstructions,
			"max_output_tokens": 256,
			"stream":            false,
		}
		if input.Type == "oauth" {
			body.(map[string]any)["store"] = false
		}
	case "responses_sse":
		path = "/v1/responses"
		body = map[string]any{
			"model":             input.HealthModel,
			"input":             []map[string]any{{"role": "user", "content": []map[string]any{{"type": "input_text", "text": "只能回复：juhe"}}}},
			"instructions":      probeInstructions,
			"max_output_tokens": 256,
			"stream":            true,
		}
	case "images_json":
		path = "/v1/images/generations"
		body = map[string]any{
			"model":              input.HealthModel,
			"prompt":             "Solid black.",
			"n":                  1,
			"size":               "1024x1024",
			"quality":            "low",
			"output_format":      "webp",
			"output_compression": 100,
		}
	default:
		return nil, errors.New("未冻结的 endpoint mode")
	}
	target := joinBaseURL(base, path)
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("编码探活请求失败: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if input.EndpointMode == "responses_sse" {
		request.Header.Set("Accept", "text/event-stream")
	} else {
		request.Header.Set("Accept", "application/json")
	}
	request.Header.Set("User-Agent", defaultProbeUserAgent)
	if input.Type == "oauth" {
		request.Header.Set("OpenAI-Beta", "responses=experimental")
		if accountID := strings.TrimSpace(input.OAuthAccountID); accountID != "" {
			request.Header.Set("ChatGPT-Account-Id", accountID)
		}
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return request, nil
}

func parseBaseURL(raw string, allowInsecure bool) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("base URL 无效")
	}
	if parsed.Scheme != "https" && !(allowInsecure && parsed.Scheme == "http") {
		return nil, errors.New("base URL 必须使用 HTTPS")
	}
	return parsed, nil
}

func joinBaseURL(base *url.URL, path string) *url.URL {
	copy := *base
	basePath := strings.TrimRight(copy.Path, "/")
	if strings.HasSuffix(basePath, "/v1") {
		basePath = strings.TrimSuffix(basePath, "/v1")
	}
	copy.Path = basePath + path
	return &copy
}

func decryptToken(secret string, envelope CredentialEnvelope) (string, error) {
	if strings.TrimSpace(secret) == "" || strings.TrimSpace(envelope.Ciphertext) == "" {
		return "", errors.New("凭据 envelope 缺失")
	}
	plaintext, err := DecryptV1Envelope(secret, envelope.Ciphertext)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(plaintext))
	if value == "" {
		return "", errors.New("凭据为空")
	}
	var fields map[string]any
	if json.Unmarshal(plaintext, &fields) == nil {
		for _, key := range []string{"api_key", "access_token", "token", "url"} {
			if text, ok := fields[key].(string); ok && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text), nil
			}
		}
	}
	return value, nil
}

func verifyResponse(mode, model string, body []byte) error {
	if mode == "responses_sse" {
		return verifyResponsesSSE(body)
	}
	var response map[string]any
	if err := json.Unmarshal(body, &response); err != nil {
		return errors.New("响应不是 JSON")
	}
	if mode == "images_json" {
		data, ok := response["data"].([]any)
		if !ok {
			return errors.New("Images API 响应缺少 data")
		}
		for _, item := range data {
			record, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if value, ok := record["b64_json"].(string); ok && strings.TrimSpace(value) != "" {
				return nil
			}
			if value, ok := record["url"].(string); ok && strings.TrimSpace(value) != "" {
				return nil
			}
		}
		return errors.New("Images API 响应缺少有效图片结果")
	}
	if containsChallenge(response) {
		return nil
	}
	return errors.New("响应未包含挑战值")
}

func verifyResponsesSSE(body []byte) error {
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	var output strings.Builder
	completed := false
	for _, block := range strings.Split(text, "\n\n") {
		if err := consumeResponsesSSEEvent(block, &output, &completed); err != nil {
			return err
		}
	}
	if !completed {
		return errors.New("SSE 缺少 response.completed")
	}
	if !strings.Contains(strings.ToLower(output.String()), probeChallenge) {
		return errors.New("SSE 输出未包含挑战值")
	}
	return nil
}

func consumeResponsesSSEEvent(block string, output *strings.Builder, completed *bool) error {
	var eventName string
	var dataLines []string
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(line, ":") || line == "" {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if len(dataLines) == 0 {
		return nil
	}
	data := strings.Join(dataLines, "\n")
	if data == "[DONE]" {
		return nil
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return errors.New("SSE data 不是 JSON")
	}
	if kind, ok := payload["type"].(string); ok && strings.TrimSpace(kind) != "" {
		eventName = kind
	}
	switch eventName {
	case "response.output_text.delta":
		if delta, ok := payload["delta"].(string); ok {
			output.WriteString(delta)
		}
	case "response.output_text.done":
		if finalText, ok := payload["text"].(string); ok {
			output.WriteString(finalText)
		}
	case "response.completed":
		*completed = true
	case "response.failed", "response.incomplete":
		return errors.New("SSE 上游未完成")
	}
	return nil
}

func containsChallenge(value any) bool {
	switch item := value.(type) {
	case string:
		return strings.Contains(strings.ToLower(item), probeChallenge)
	case []any:
		for _, child := range item {
			if containsChallenge(child) {
				return true
			}
		}
	case map[string]any:
		for _, child := range item {
			if containsChallenge(child) {
				return true
			}
		}
	}
	return false
}

func transportFailure(err error) ProbeResult {
	if errors.Is(err, context.Canceled) {
		return taskFailure("probe_cancelled", "探活已取消")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ProbeResult{Outcome: OutcomeUpstreamFailed, ErrorCode: "upstream_timeout", ErrorMessage: "上游请求超时"}
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) && networkErr.Timeout() {
		return ProbeResult{Outcome: OutcomeUpstreamFailed, ErrorCode: "upstream_timeout", ErrorMessage: "上游请求超时"}
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		switch {
		case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
			return ProbeResult{Outcome: OutcomeUpstreamFailed, ErrorCode: "upstream_connection_closed", ErrorMessage: "上游提前关闭连接"}
		case errors.Is(err, syscall.ECONNREFUSED):
			return ProbeResult{Outcome: OutcomeUpstreamFailed, ErrorCode: "upstream_connection_refused", ErrorMessage: "上游拒绝连接"}
		case errors.Is(err, syscall.ECONNRESET):
			return ProbeResult{Outcome: OutcomeUpstreamFailed, ErrorCode: "upstream_connection_reset", ErrorMessage: "上游重置连接"}
		}
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) {
			return ProbeResult{Outcome: OutcomeUpstreamFailed, ErrorCode: "upstream_dns", ErrorMessage: "上游域名解析失败"}
		}
		return ProbeResult{Outcome: OutcomeUpstreamFailed, ErrorCode: "upstream_connection", ErrorMessage: "上游连接失败"}
	}
	return taskFailure("probe_local_failure", "探活本地执行失败")
}

func responseReadFailure(err error) ProbeResult {
	if errors.Is(err, context.Canceled) {
		return taskFailure("probe_cancelled", "探活已取消")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ProbeResult{Outcome: OutcomeUpstreamFailed, ErrorCode: "upstream_timeout", ErrorMessage: "上游响应读取超时"}
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) && networkErr.Timeout() {
		return ProbeResult{Outcome: OutcomeUpstreamFailed, ErrorCode: "upstream_timeout", ErrorMessage: "上游响应读取超时"}
	}
	return ProbeResult{Outcome: OutcomeUpstreamFailed, ErrorCode: "upstream_read_incomplete", ErrorMessage: "上游响应读取未完成"}
}

func neutral(status int, code, message string) ProbeResult {
	return ProbeResult{Outcome: OutcomeNeutral, StatusCode: status, ErrorCode: code, ErrorMessage: message}
}

func taskFailure(code, message string) ProbeResult {
	return ProbeResult{Outcome: OutcomeTaskFailed, ErrorCode: code, ErrorMessage: message}
}
