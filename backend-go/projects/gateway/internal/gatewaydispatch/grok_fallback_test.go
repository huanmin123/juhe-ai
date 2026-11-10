package gatewaydispatch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type grokTestBody struct {
	io.Reader
	closed bool
}

func (b *grokTestBody) Close() error {
	b.closed = true
	return nil
}

type grokBlockingBody struct {
	closed chan struct{}
	once   sync.Once
}

func (b *grokBlockingBody) Read([]byte) (int, error) {
	<-b.closed
	return 0, io.EOF
}

func (b *grokBlockingBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

func newGrokFallbackResponse(status int, body string) (*GatewayUpstreamResponse, *grokTestBody) {
	reader := &grokTestBody{Reader: strings.NewReader(body)}
	return NewGatewayUpstreamResponseForTransform(status, http.Header{}, reader), reader
}

func grokFallbackHeaders() http.Header {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer token-1")
	headers.Set("x-xai-token-auth", "xai-grok-cli")
	headers.Set("x-grok-client-version", "1.0.45")
	headers.Set("x-grok-client-surface", "cli")
	headers.Set("x-userid", "u-1")
	headers.Set("x-email", "u@example.com")
	headers.Set("User-Agent", "grok-cli/1.0.45")
	return headers
}

func grokFallbackInputFor(response *GatewayUpstreamResponse) grokAccessDeniedFallbackInput {
	return grokAccessDeniedFallbackInput{
		UpstreamURL: "https://cli-chat-proxy.grok.com/v1/responses?trace=1",
		Headers:     grokFallbackHeaders(),
		Body:        []byte(`{"model":"grok-4.7"}`),
		Response:    response,
		Signal:      context.Background(),
	}
}

func TestGrokFallbackRejectsNonCandidate(t *testing.T) {
	response, _ := newGrokFallbackResponse(http.StatusForbidden, "access denied")
	input := grokFallbackInputFor(response)
	input.UpstreamURL = "https://api.x.ai/v1/responses"
	called := false
	input.RequestFallback = func(string, http.Header) (*GatewayUpstreamResponse, error) {
		called = true
		return nil, nil
	}
	result, err := applyGrokAccessDeniedFallback(input)
	if err != nil {
		t.Fatalf("fallback: %v", err)
	}
	if result.UsedFallback || called || result.Response != response {
		t.Fatalf("non-candidate must pass through: %#v called=%v", result, called)
	}
}

func TestGrokFallbackRetriesOfficialAPIOnAccessDenied(t *testing.T) {
	response, originalBody := newGrokFallbackResponse(http.StatusForbidden, "Access Denied")
	input := grokFallbackInputFor(response)
	var (
		gotURL     string
		gotHeaders http.Header
	)
	input.RequestFallback = func(url string, headers http.Header) (*GatewayUpstreamResponse, error) {
		gotURL = url
		gotHeaders = headers
		fallbackResponse, _ := newGrokFallbackResponse(http.StatusOK, `{"ok":true}`)
		return fallbackResponse, nil
	}
	result, err := applyGrokAccessDeniedFallback(input)
	if err != nil {
		t.Fatalf("fallback: %v", err)
	}
	if !result.UsedFallback || result.Response.Status() != http.StatusOK {
		t.Fatalf("fallback result = %#v", result)
	}
	if gotURL != "https://api.x.ai/v1/responses?trace=1" {
		t.Fatalf("fallback url = %s", gotURL)
	}
	for _, header := range grokCLIOnlyHeaders {
		if gotHeaders.Get(header) != "" {
			t.Fatalf("CLI header %s must be stripped: %q", header, gotHeaders.Get(header))
		}
	}
	if gotHeaders.Get("Authorization") != "Bearer token-1" {
		t.Fatalf("authorization must be preserved: %q", gotHeaders.Get("Authorization"))
	}
	if !originalBody.closed {
		t.Fatal("original 403 body must be closed after fallback success")
	}
}

func TestGrokFallbackKeepsOriginalWhenBodyIsNotAccessDenied(t *testing.T) {
	response, _ := newGrokFallbackResponse(http.StatusForbidden, "forbidden by policy")
	input := grokFallbackInputFor(response)
	called := false
	input.RequestFallback = func(string, http.Header) (*GatewayUpstreamResponse, error) {
		called = true
		return nil, nil
	}
	result, err := applyGrokAccessDeniedFallback(input)
	if err != nil {
		t.Fatalf("fallback: %v", err)
	}
	if result.UsedFallback || called {
		t.Fatalf("non access-denied body must not retry: %#v", result)
	}
	body, err := io.ReadAll(result.Response.Body)
	if err != nil {
		t.Fatalf("replay body: %v", err)
	}
	if string(body) != "forbidden by policy" {
		t.Fatalf("replay body = %q", body)
	}
}

func TestGrokFallbackKeepsOriginalWhenRetryFails(t *testing.T) {
	response, _ := newGrokFallbackResponse(http.StatusForbidden, "access denied")
	input := grokFallbackInputFor(response)
	input.RequestFallback = func(string, http.Header) (*GatewayUpstreamResponse, error) {
		fallbackResponse, _ := newGrokFallbackResponse(http.StatusInternalServerError, "boom")
		return fallbackResponse, nil
	}
	result, err := applyGrokAccessDeniedFallback(input)
	if err != nil {
		t.Fatalf("fallback: %v", err)
	}
	if result.UsedFallback || result.Response.Status() != http.StatusForbidden {
		t.Fatalf("failed retry must keep original 403: %#v", result)
	}
	body, err := io.ReadAll(result.Response.Body)
	if err != nil {
		t.Fatalf("replay body: %v", err)
	}
	if string(body) != "access denied" {
		t.Fatalf("replay body = %q", body)
	}
}

func TestGrokFallbackKeepsOriginalWhenRetryErrors(t *testing.T) {
	response, _ := newGrokFallbackResponse(http.StatusForbidden, "access denied")
	input := grokFallbackInputFor(response)
	input.RequestFallback = func(string, http.Header) (*GatewayUpstreamResponse, error) {
		return nil, errors.New("dial failed")
	}
	result, err := applyGrokAccessDeniedFallback(input)
	if err != nil {
		t.Fatalf("fallback: %v", err)
	}
	if result.UsedFallback || result.Response.Status() != http.StatusForbidden {
		t.Fatalf("retry error must keep original 403: %#v", result)
	}
}

func TestGrokFallbackBodyInspectionTimeout(t *testing.T) {
	body := &grokBlockingBody{closed: make(chan struct{})}
	response := NewGatewayUpstreamResponseForTransform(http.StatusForbidden, http.Header{}, body)
	input := grokFallbackInputFor(response)
	input.BodyInspectionTimeout = 10 * time.Millisecond
	_, err := applyGrokAccessDeniedFallback(input)
	var timeoutErr *UpstreamRequestTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("expected inspection timeout, got %v", err)
	}
	select {
	case <-body.closed:
	default:
		t.Fatal("timed-out inspection must close the body")
	}
}

func TestGrokFallbackIncompleteBodyDoesNotRetry(t *testing.T) {
	payload := strings.Repeat("a", grokFallbackBodyLimit) + "access denied"
	response, _ := newGrokFallbackResponse(http.StatusForbidden, payload)
	input := grokFallbackInputFor(response)
	called := false
	input.RequestFallback = func(string, http.Header) (*GatewayUpstreamResponse, error) {
		called = true
		return nil, nil
	}
	result, err := applyGrokAccessDeniedFallback(input)
	if err != nil {
		t.Fatalf("fallback: %v", err)
	}
	if result.UsedFallback || called {
		t.Fatalf("oversized body must not retry: %#v", result)
	}
	body, err := io.ReadAll(result.Response.Body)
	if err != nil {
		t.Fatalf("replay body: %v", err)
	}
	if string(body) != payload {
		t.Fatalf("replay body length = %d, want %d", len(body), len(payload))
	}
}
