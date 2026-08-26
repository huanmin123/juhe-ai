package modelcheckprobe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

const (
	DefaultProbeTimeout     = 30 * time.Second
	DefaultMaxResponseBytes = 2 << 20
)

type TransportOptions struct {
	Endpoint         string
	Headers          http.Header
	Client           *http.Client
	Timeout          time.Duration
	MaxResponseBytes int64
}

// ExecuteRequest exposes one raw jobs-owned request attempt to the executor.
// It is intentionally narrow: construction, retry and evaluation remain
// separate so callers cannot accidentally retry a semantic score failure.
func ExecuteRequest(ctx context.Context, request Request, options TransportOptions) (ProbeResult, error) {
	return Execute(ctx, request, options)
}

// Execute sends one already-built probe directly to the upstream endpoint.
// Credentials exist only in the in-memory Headers supplied by the runtime;
// the returned ProbeResult contains no headers or raw response body.
func Execute(ctx context.Context, request Request, options TransportOptions) (ProbeResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint, err := buildProbeURL(options.Endpoint, request.Protocol, request.Path)
	if err != nil {
		return ProbeResult{ExpectedModel: request.ExpectedModel, Response: ParsedResponse{ErrorMessage: err.Error()}}, err
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = DefaultProbeTimeout
	}
	requestContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(requestContext, http.MethodPost, endpoint, strings.NewReader(string(request.Body)))
	if err != nil {
		return ProbeResult{ExpectedModel: request.ExpectedModel, Response: ParsedResponse{ErrorMessage: "模型检测请求构造失败"}}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", acceptForRequest(request))
	for key, values := range options.Headers {
		for _, value := range values {
			httpRequest.Header.Add(key, value)
		}
	}
	client := http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if options.Client != nil {
		client = *options.Client
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		if client.Timeout <= 0 {
			client.Timeout = timeout
		}
	}
	started := time.Now()
	response, err := client.Do(httpRequest)
	if err != nil {
		message := "模型检测上游请求失败"
		if errors.Is(requestContext.Err(), context.DeadlineExceeded) {
			message = "模型检测上游请求超时"
		} else if errors.Is(requestContext.Err(), context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			message = "模型检测上游请求已取消"
		}
		return ProbeResult{HTTPStatusCode: 0, DurationMS: time.Since(started).Milliseconds(), ExpectedModel: request.ExpectedModel, Response: ParsedResponse{ErrorMessage: message}}, nil
	}
	defer response.Body.Close()
	maxBytes := options.MaxResponseBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxResponseBytes
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	truncated := int64(len(body)) > maxBytes
	if truncated {
		body = body[:maxBytes]
	}
	parsed := ParseResponse(request.Protocol, body)
	if readErr != nil && parsed.ErrorMessage == "" {
		parsed.ErrorMessage = "模型检测上游响应读取失败"
	}
	if response.StatusCode != http.StatusOK && parsed.ErrorMessage == "" {
		parsed.ErrorMessage = fmt.Sprintf("模型检测上游返回 HTTP %d", response.StatusCode)
	}
	result := ProbeResult{
		HTTPStatusCode:     response.StatusCode,
		Success:            response.StatusCode == http.StatusOK && readErr == nil && !truncated && parsed.Successful(response.StatusCode),
		DurationMS:         time.Since(started).Milliseconds(),
		TraceID:            newTraceID(),
		ExpectedModel:      request.ExpectedModel,
		UpstreamModel:      parsed.Model,
		UpstreamStatusCode: intPtr(response.StatusCode),
		ResponseTruncated:  truncated,
		Response:           parsed,
	}
	return result, nil
}

func buildProbeURL(endpoint string, protocol modelcheckprofile.Protocol, path string) (string, error) {
	base, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || base.Scheme != "http" && base.Scheme != "https" || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return "", errors.New("模型检测 endpoint URL 无效")
	}
	if strings.ContainsAny(endpoint, "\\\r\n\t") {
		return "", errors.New("模型检测 endpoint URL 无效")
	}
	requestPath, requestQuery := splitProbePath(path)
	requestPath = normalizeRequestPath(requestPath)
	basePath := strings.TrimRight(base.Path, "/")
	switch protocol {
	case modelcheckprofile.ProtocolOpenAIResponses, modelcheckprofile.ProtocolOpenAIChat, modelcheckprofile.ProtocolAnthropic:
		if !strings.HasSuffix(basePath, "/v1") {
			basePath += "/v1"
		}
		requestPath = strings.TrimPrefix(requestPath, "/v1")
	case modelcheckprofile.ProtocolGeminiNative:
		if !strings.HasSuffix(basePath, "/v1beta") {
			basePath += "/v1beta"
		}
		requestPath = strings.TrimPrefix(requestPath, "/v1beta")
	default:
		return "", errors.New("模型检测协议不支持")
	}
	base.Path = strings.TrimRight(basePath, "/") + "/" + strings.TrimLeft(requestPath, "/")
	base.RawPath = ""
	base.RawQuery = requestQuery
	return base.String(), nil
}

func splitProbePath(value string) (string, string) {
	parts := strings.SplitN(value, "?", 2)
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[0], parts[1]
}

func normalizeRequestPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "/"
	}
	if !strings.HasPrefix(value, "/") {
		return "/" + value
	}
	return value
}

func acceptForRequest(request Request) string {
	if strings.Contains(request.Path, "alt=sse") || request.Protocol != modelcheckprofile.ProtocolGeminiNative && request.Body != nil && strings.Contains(string(request.Body), `"stream":true`) {
		return "text/event-stream"
	}
	return "application/json"
}

func newTraceID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return hex.EncodeToString(bytes[:])
	}
	return "model-check"
}

func intPtr(value int) *int { return &value }
