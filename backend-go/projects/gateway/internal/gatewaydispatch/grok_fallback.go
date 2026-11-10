package gatewaydispatch

// Grok CLI 403 access-denied 官方 API 回退（Node
// providers/drivers/xai/grok-access-denied-fallback.ts，D-163 残留）。
//
// xAI OAuth 账户默认走 cli-chat-proxy.grok.com。该代理在拒绝访问时返回
// 403 + "access denied" 正文；Node 会在响应体确认后把请求改到官方
// api.x.ai 重试一次，并剥掉 CLI 专用头。Go 此前直接透传 403。

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	grokCLIProxyHost            = "cli-chat-proxy.grok.com"
	grokOfficialAPIHost         = "api.x.ai"
	grokFallbackBodyLimit       = 64 << 10
	grokFallbackInspectionLimit = 5 * time.Second
)

var grokCLIOnlyHeaders = []string{
	"x-xai-token-auth",
	"x-grok-client-version",
	"x-grok-client-surface",
	"x-userid",
	"x-email",
	"user-agent",
}

type grokAccessDeniedFallbackInput struct {
	UpstreamURL           string
	Headers               http.Header
	Body                  []byte
	Response              *GatewayUpstreamResponse
	Signal                context.Context
	BodyInspectionTimeout time.Duration
	RequestFallback       func(url string, headers http.Header) (*GatewayUpstreamResponse, error)
}

type grokAccessDeniedFallbackResult struct {
	Response     *GatewayUpstreamResponse
	UsedFallback bool
}

// applyGrokAccessDeniedFallback mirrors applyGrokAccessDeniedFallback: only
// inspect the response body when the request is a Grok CLI OAuth call, replay
// the body when no fallback is used, and keep the original 403 when the
// official API retry fails or is not 2xx.
func applyGrokAccessDeniedFallback(input grokAccessDeniedFallbackInput) (grokAccessDeniedFallbackResult, error) {
	if !isGrokAccessDeniedFallbackCandidate(input) {
		return grokAccessDeniedFallbackResult{Response: input.Response}, nil
	}
	prefix, complete, replay, err := inspectGrokFallbackBody(
		input.Response.Body,
		grokFallbackBodyLimit,
		input.Signal,
		positiveGrokFallbackTimeout(input.BodyInspectionTimeout),
	)
	if err != nil {
		return grokAccessDeniedFallbackResult{}, err
	}
	original := input.Response
	original.Body = replay
	if !complete || !strings.Contains(strings.ToLower(string(prefix)), "access denied") {
		return grokAccessDeniedFallbackResult{Response: original}, nil
	}
	fallbackURL, err := buildGrokFallbackURL(input.UpstreamURL)
	if err != nil {
		return grokAccessDeniedFallbackResult{Response: original}, nil
	}
	fallbackHeaders := input.Headers.Clone()
	for _, header := range grokCLIOnlyHeaders {
		fallbackHeaders.Del(header)
	}
	fallbackResponse, err := input.RequestFallback(fallbackURL, fallbackHeaders)
	if err != nil {
		return grokAccessDeniedFallbackResult{Response: original}, nil
	}
	if fallbackResponse == nil || !fallbackResponse.OK() {
		if fallbackResponse != nil && fallbackResponse.Body != nil {
			_ = fallbackResponse.Body.Close()
		}
		return grokAccessDeniedFallbackResult{Response: original}, nil
	}
	if original.Body != nil {
		_ = original.Body.Close()
	}
	return grokAccessDeniedFallbackResult{Response: fallbackResponse, UsedFallback: true}, nil
}

func isGrokAccessDeniedFallbackCandidate(input grokAccessDeniedFallbackInput) bool {
	if input.Response == nil || input.Response.Status() != http.StatusForbidden {
		return false
	}
	if input.Body == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(input.Headers.Get("x-xai-token-auth")), "xai-grok-cli") {
		return false
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(input.Headers.Get("Authorization"))), "bearer ") {
		return false
	}
	parsed, err := url.Parse(input.UpstreamURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Hostname(), grokCLIProxyHost)
}

func buildGrokFallbackURL(upstreamURL string) (string, error) {
	parsed, err := url.Parse(upstreamURL)
	if err != nil {
		return "", err
	}
	parsed.Scheme = "https"
	parsed.Host = grokOfficialAPIHost
	return parsed.String(), nil
}

func positiveGrokFallbackTimeout(value time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return grokFallbackInspectionLimit
}

// inspectGrokFallbackBody reads up to limit+1 bytes (EOF marks the body as
// complete) under the caller signal and the inspection timeout, then returns a
// replay body containing every consumed byte. A timeout/abort mirrors Node:
// close the body and return the corresponding attempt error.
func inspectGrokFallbackBody(
	body io.ReadCloser,
	limit int,
	signal context.Context,
	timeout time.Duration,
) (prefix []byte, complete bool, replay io.ReadCloser, err error) {
	if body == nil {
		return nil, true, nil, nil
	}
	type readResult struct {
		data []byte
		err  error
	}
	done := make(chan readResult, 1)
	go func() {
		data, readErr := io.ReadAll(io.LimitReader(body, int64(limit)+1))
		done <- readResult{data: data, err: readErr}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var signalDone <-chan struct{}
	if signal != nil {
		signalDone = signal.Done()
	}
	select {
	case <-signalDone:
		_ = body.Close()
		<-done
		return nil, false, nil, &UpstreamRequestAbortedError{Message: "请求已取消", UpstreamRequestStarted: true}
	case <-timer.C:
		_ = body.Close()
		<-done
		return nil, false, nil, &UpstreamRequestTimeoutError{Message: "Grok CLI 403 响应体检查超时"}
	case result := <-done:
		if result.err != nil {
			_ = body.Close()
			return nil, false, nil, result.err
		}
		complete = len(result.data) <= limit
		if complete {
			_ = body.Close()
			return result.data, true, io.NopCloser(bytes.NewReader(result.data)), nil
		}
		return result.data, false, &grokReplayBody{
			reader: io.MultiReader(bytes.NewReader(result.data), body),
			closer: body,
		}, nil
	}
}

type grokReplayBody struct {
	reader io.Reader
	closer io.Closer
}

func (b *grokReplayBody) Read(buffer []byte) (int, error) { return b.reader.Read(buffer) }

func (b *grokReplayBody) Close() error {
	if b.closer == nil {
		return nil
	}
	return b.closer.Close()
}

// logGrokFallbackSuccess mirrors the Node warn event.
func logGrokFallbackSuccess(accountID, method, path string) {
	slog.Warn("Grok CLI 代理拒绝访问，已改用官方 API",
		"event", "grok_cli_access_denied_api_fallback_succeeded",
		"accountId", accountID,
		"method", method,
		"path", path,
	)
}
