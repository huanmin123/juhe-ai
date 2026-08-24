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

// ProbeOpenAI executes one frozen direct probe. The historical exported name is
// retained for callers, but dispatches by the signed protocol profile rather
// than assuming every account speaks OpenAI v1.
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
	if err := verifyResponse(input, body); err != nil {
		return neutral(response.StatusCode, "upstream_protocol_invalid", "上游响应未满足探活语义")
	}
	return ProbeResult{Outcome: OutcomeSuccess, StatusCode: response.StatusCode}
}

func validateInput(input Input, options ProbeOptions) error {
	if strings.TrimSpace(input.AccountID) == "" || input.InputVersion <= 0 || input.ConfigRevision <= 0 || input.DispatchRevision <= 0 {
		return errors.New("input fence 无效")
	}
	profileProvider := probeProfileProvider(input.ProtocolProfileID, input.Provider)
	if !isSupportedDirectProfile(input.ProtocolProfileID, profileProvider, input.Type, input.EndpointMode) {
		return errors.New("未冻结的 provider 或账户类型")
	}
	if err := validateDirectProtocolMetadata(input.ProtocolProfileID, input.ProtocolCode, input.ProtocolVersion); err != nil {
		return err
	}
	protocol := directProbeProtocolForMode(input.ProtocolProfileID, profileProvider, input.EndpointMode)
	if protocol == "" || strings.TrimSpace(input.Provider) != protocol {
		return errors.New("provider 与协议 profile 不一致")
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
	if (input.Type == "oauth" || input.Type == "google_oauth") && (input.OAuthExpiresAt == nil || !input.OAuthExpiresAt.After(now.Add(time.Minute))) {
		return errors.New("OAuth access token 已到期或接近到期")
	}
	if input.Type == "google_oauth" && (input.OAuthType == "code_assist" || input.OAuthType == "google_one") {
		if protocol != "gemini" || strings.TrimSpace(input.OAuthProjectID) == "" {
			return errors.New("Gemini Code Assist / Google One 探活缺少协议或 project_id")
		}
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
	protocol := directProbeProtocolForMode(input.ProtocolProfileID, input.Provider, input.EndpointMode)
	path := ""
	method := http.MethodPost
	var body any
	switch input.EndpointMode {
	case "chat_json", "chat_sse":
		path = "/v1/chat/completions"
		body = map[string]any{
			"model":      input.HealthModel,
			"messages":   []map[string]any{{"role": "user", "content": "只能回复：juhe"}},
			"max_tokens": 256,
			"stream":     input.EndpointMode == "chat_sse",
		}
	case "responses_json":
		path = "/v1/responses"
		if input.Type == "oauth" && (input.ProtocolProfileID == "" || input.ProtocolProfileID == "profile_gpt_openai_v1") {
			path = "/responses"
		}
		body = map[string]any{
			"model":             input.HealthModel,
			"input":             []map[string]any{{"role": "user", "content": []map[string]any{{"type": "input_text", "text": "只能回复：juhe"}}}},
			"instructions":      probeInstructions,
			"max_output_tokens": 256,
			"stream":            false,
		}
		if input.Type == "oauth" && (input.ProtocolProfileID == "" || input.ProtocolProfileID == "profile_gpt_openai_v1") {
			body.(map[string]any)["store"] = false
		}
	case "responses_sse":
		path = "/v1/responses"
		if input.Type == "oauth" && (input.ProtocolProfileID == "" || input.ProtocolProfileID == "profile_gpt_openai_v1") {
			path = "/responses"
		}
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
	case "messages_json", "messages_sse":
		path = "/v1/messages"
		body = map[string]any{
			"model":      input.HealthModel,
			"max_tokens": 256,
			"messages":   []map[string]any{{"role": "user", "content": "只能回复：juhe"}},
			"stream":     input.EndpointMode == "messages_sse",
		}
	case "generate_content_json", "generate_content_sse":
		model := strings.TrimPrefix(input.HealthModel, "models/")
		path = "/v1beta/models/" + url.PathEscape(model) + ":generateContent"
		if input.EndpointMode == "generate_content_sse" {
			path = "/v1beta/models/" + url.PathEscape(model) + ":streamGenerateContent?alt=sse"
		}
		body = map[string]any{
			"contents":         []map[string]any{{"role": "user", "parts": []map[string]any{{"text": "只能回复：juhe"}}}},
			"generationConfig": map[string]any{"maxOutputTokens": 256},
		}
	case "interactions_json", "interactions_sse":
		path = "/v1beta/interactions"
		body = map[string]any{
			"model":  input.HealthModel,
			"input":  "只能回复：juhe",
			"stream": input.EndpointMode == "interactions_sse",
		}
	default:
		return nil, errors.New("未冻结的 endpoint mode")
	}
	codeAssist := protocol == "gemini" && input.Type == "google_oauth" && (input.OAuthType == "code_assist" || input.OAuthType == "google_one")
	if codeAssist {
		path = "/v1internal:streamGenerateContent?alt=sse"
		body = map[string]any{"model": input.HealthModel, "project": input.OAuthProjectID, "request": body}
	}
	if input.ProtocolProfileID == "profile_gemini_openai_chat_v1beta" && protocol == "openai" {
		path = strings.TrimPrefix(path, "/v1")
	}
	if (input.ProtocolProfileID == "profile_glm_general_openai_v1" || input.ProtocolProfileID == "profile_glm_coding_openai_v1") && protocol == "openai" {
		path = strings.TrimPrefix(path, "/v1")
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
	request.Header.Set("User-Agent", defaultProbeUserAgent)
	switch protocol {
	case "anthropic":
		request.Header.Set("anthropic-version", "2023-06-01")
		if input.Type == "oauth" || input.ProtocolProfileID == "profile_glm_coding_anthropic_v1" {
			request.Header.Set("Authorization", "Bearer "+token)
		} else {
			request.Header.Set("x-api-key", token)
		}
		if input.Type == "oauth" {
			request.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,fine-grained-tool-streaming-2025-05-14")
			request.Header.Set("User-Agent", "claude-cli/2.1.161 (external, cli)")
			request.Header.Set("x-stainless-lang", "js")
			request.Header.Set("x-stainless-package-version", "0.94.0")
			request.Header.Set("x-stainless-os", "Linux")
			request.Header.Set("x-stainless-arch", "arm64")
			request.Header.Set("x-stainless-runtime", "node")
			request.Header.Set("x-stainless-runtime-version", "v24.3.0")
			request.Header.Set("x-stainless-retry-count", "0")
			request.Header.Set("x-stainless-timeout", "600")
			request.Header.Set("x-app", "cli")
			request.Header.Set("anthropic-dangerous-direct-browser-access", "true")
		}
	case "gemini":
		if input.Type == "google_oauth" {
			request.Header.Set("Authorization", "Bearer "+token)
			if value := strings.TrimSpace(input.OAuthQuotaProjectID); value != "" {
				request.Header.Set("x-goog-user-project", value)
			}
		} else {
			request.Header.Set("x-goog-api-key", token)
		}
		if input.EndpointMode == "interactions_json" || input.EndpointMode == "interactions_sse" {
			request.Header.Set("api-revision", "2026-05-20")
		}
		if codeAssist {
			request.Header.Set("User-Agent", "GeminiCLI/0.1.5 (Windows; AMD64)")
		}
	default:
		request.Header.Set("Authorization", "Bearer "+token)
		if input.ProtocolProfileID == "profile_xai_openai_v1" && input.Type == "oauth" && strings.EqualFold(request.URL.Hostname(), "cli-chat-proxy.grok.com") {
			request.Header.Set("User-Agent", "xai-grok-workspace/0.2.93")
			request.Header.Set("x-xai-token-auth", "xai-grok-cli")
			request.Header.Set("x-grok-client-version", "0.2.93")
		}
	}
	if input.EndpointMode == "responses_sse" || input.EndpointMode == "chat_sse" || input.EndpointMode == "messages_sse" || input.EndpointMode == "generate_content_sse" || input.EndpointMode == "interactions_sse" || codeAssist {
		request.Header.Set("Accept", "text/event-stream")
	} else {
		request.Header.Set("Accept", "application/json")
	}
	if protocol == "openai" && input.Type == "oauth" && (input.ProtocolProfileID == "" || input.ProtocolProfileID == "profile_gpt_openai_v1") {
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
	pathOnly, rawQuery, hasQuery := strings.Cut(path, "?")
	basePath := strings.TrimRight(copy.Path, "/")
	if strings.HasSuffix(basePath, "/v1") {
		basePath = strings.TrimSuffix(basePath, "/v1")
	}
	if strings.HasSuffix(basePath, "/v1beta") && strings.HasPrefix(pathOnly, "/v1beta/") {
		pathOnly = strings.TrimPrefix(pathOnly, "/v1beta")
	}
	copy.Path = basePath + pathOnly
	if hasQuery {
		copy.RawQuery = rawQuery
	}
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

func verifyResponse(input Input, body []byte) error {
	codeAssist := input.Provider == "gemini" && input.Type == "google_oauth" && (input.OAuthType == "code_assist" || input.OAuthType == "google_one")
	if codeAssist {
		return verifyGeminiSSE(body)
	}
	switch input.EndpointMode {
	case "responses_sse":
		return verifyResponsesSSE(body)
	case "chat_sse":
		return verifyOpenAIChatSSE(body)
	case "messages_sse":
		return verifyAnthropicSSE(body)
	case "generate_content_sse":
		return verifyGeminiSSE(body)
	case "interactions_sse":
		return verifyInteractionsSSE(body)
	}
	var response map[string]any
	if err := json.Unmarshal(body, &response); err != nil {
		return errors.New("响应不是 JSON")
	}
	if input.ProtocolProfileID == "" {
		if input.EndpointMode == "images_json" {
			return verifyImagesJSON(response)
		}
		if containsChallenge(response) {
			return nil
		}
		return errors.New("响应未包含挑战值")
	}
	switch input.EndpointMode {
	case "images_json":
		return verifyImagesJSON(response)
	case "chat_json":
		if !chatCompleted(response) || !containsChallenge(response) {
			return errors.New("Chat Completions 响应未完成挑战")
		}
		return nil
	case "responses_json":
		if textField(response, "status") != "completed" || !(response["object"] == "response" || response["output"] != nil) || !containsChallenge(response) {
			return errors.New("Responses 响应未完成挑战")
		}
		return nil
	case "messages_json":
		if response["type"] != "message" || textField(response, "stop_reason") == "" || !containsChallenge(response) {
			return errors.New("Anthropic Messages 响应未完成挑战")
		}
		return nil
	case "generate_content_json":
		if !geminiCandidateComplete(response) || !containsChallenge(response) {
			return errors.New("Gemini GenerateContent 响应未完成挑战")
		}
		return nil
	case "interactions_json":
		if textField(response, "status") != "completed" || !containsChallenge(response) {
			return errors.New("Gemini Interactions 响应未完成挑战")
		}
		return nil
	default:
		if containsChallenge(response) {
			return nil
		}
		return errors.New("响应未包含挑战值")
	}
}

func chatCompleted(response map[string]any) bool {
	choices, ok := response["choices"].([]any)
	if !ok {
		return false
	}
	for _, item := range choices {
		choice, _ := item.(map[string]any)
		if textField(choice, "finish_reason") != "" {
			return true
		}
	}
	return false
}

func verifyImagesJSON(response map[string]any) error {
	data, ok := response["data"].([]any)
	if !ok {
		return errors.New("Images API 响应缺少 data")
	}
	for _, item := range data {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if value := textField(record, "b64_json"); value != "" {
			return nil
		}
		if value := textField(record, "url"); value != "" {
			return nil
		}
	}
	return errors.New("Images API 响应缺少有效图片结果")
}

func textField(value map[string]any, name string) string {
	text, _ := value[name].(string)
	return strings.TrimSpace(text)
}

func geminiCandidateComplete(response map[string]any) bool {
	candidates, ok := response["candidates"].([]any)
	if !ok {
		return false
	}
	for _, item := range candidates {
		candidate, ok := item.(map[string]any)
		if ok && textField(candidate, "finishReason") != "" {
			return true
		}
	}
	return false
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

func verifyOpenAIChatSSE(body []byte) error {
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	var output strings.Builder
	done := false
	for _, block := range strings.Split(text, "\n\n") {
		for _, line := range strings.Split(block, "\n") {
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				done = true
				continue
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(data), &payload); err != nil {
				return errors.New("Chat SSE data 不是 JSON")
			}
			choices, _ := payload["choices"].([]any)
			for _, item := range choices {
				choice, _ := item.(map[string]any)
				if textField(choice, "finish_reason") != "" {
					done = true
				}
				if delta, _ := choice["delta"].(map[string]any); delta != nil {
					output.WriteString(textField(delta, "content"))
				}
				if message, _ := choice["message"].(map[string]any); message != nil {
					output.WriteString(textField(message, "content"))
				}
			}
		}
	}
	if !done || !strings.Contains(strings.ToLower(output.String()), probeChallenge) {
		return errors.New("Chat SSE 未完成挑战")
	}
	return nil
}

func verifyAnthropicSSE(body []byte) error {
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	var output strings.Builder
	stopped := false
	for _, block := range strings.Split(text, "\n\n") {
		var eventName string
		var data string
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event:"):
				eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			}
		}
		if data == "" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			return errors.New("Messages SSE data 不是 JSON")
		}
		if kind := textField(payload, "type"); kind != "" {
			eventName = kind
		}
		if eventName == "content_block_delta" {
			if delta, _ := payload["delta"].(map[string]any); delta != nil {
				output.WriteString(textField(delta, "text"))
			}
		}
		if eventName == "message_stop" {
			stopped = true
		}
	}
	if !stopped || !strings.Contains(strings.ToLower(output.String()), probeChallenge) {
		return errors.New("Messages SSE 未完成挑战")
	}
	return nil
}

func verifyGeminiSSE(body []byte) error {
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	var output strings.Builder
	finished := false
	for _, block := range strings.Split(text, "\n\n") {
		for _, line := range strings.Split(block, "\n") {
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				finished = true
				continue
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(data), &payload); err != nil {
				return errors.New("Gemini SSE data 不是 JSON")
			}
			candidatePayload := payload
			if nested, ok := payload["response"].(map[string]any); ok {
				candidatePayload = nested
			}
			if geminiCandidateComplete(candidatePayload) {
				finished = true
			}
			if candidates, ok := candidatePayload["candidates"].([]any); ok {
				for _, item := range candidates {
					candidate, _ := item.(map[string]any)
					content, _ := candidate["content"].(map[string]any)
					parts, _ := content["parts"].([]any)
					for _, part := range parts {
						if record, _ := part.(map[string]any); record != nil {
							output.WriteString(textField(record, "text"))
						}
					}
				}
			}
			if containsChallenge(payload) {
				output.WriteString(probeChallenge)
			}
		}
	}
	if !finished || !strings.Contains(strings.ToLower(output.String()), probeChallenge) {
		return errors.New("Gemini SSE 未完成挑战")
	}
	return nil
}

func verifyInteractionsSSE(body []byte) error {
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	var output strings.Builder
	completed := false
	for _, block := range strings.Split(text, "\n\n") {
		for _, line := range strings.Split(block, "\n") {
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				completed = true
				continue
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(data), &payload); err != nil {
				return errors.New("Interactions SSE data 不是 JSON")
			}
			if textField(payload, "status") == "completed" || textField(payload, "type") == "interaction.completed" {
				completed = true
			}
			if interaction, _ := payload["interaction"].(map[string]any); interaction != nil && textField(interaction, "status") == "completed" {
				completed = true
			}
			if containsChallenge(payload) {
				output.WriteString(probeChallenge)
			}
		}
	}
	if !completed || !strings.Contains(strings.ToLower(output.String()), probeChallenge) {
		return errors.New("Interactions SSE 未完成挑战")
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
