package openaicompat

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Mock transport for the image generation provider.
type mockTransport struct {
	handler func(request *http.Request) (*http.Response, error)
}

func (m *mockTransport) Do(request *http.Request) (*http.Response, error) {
	return m.handler(request)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestImageGenerationExecutorGating(t *testing.T) {
	if NewImageGenerationExecutor(Config{}, "", nil, nil) != nil {
		t.Fatal("no authorization -> nil executor")
	}
	executor := NewImageGenerationExecutor(Config{Port: 3000}, "Bearer x", nil, nil)
	if executor == nil {
		t.Fatal("authorization present -> executor")
	}
	if executor.Provider.Endpoint != "http://127.0.0.1:3000/v1/images/generations" {
		t.Fatalf("endpoint = %s", executor.Provider.Endpoint)
	}
	if executor.Provider.Model != ImageGenerationProviderModel || executor.Provider.TimeoutMs != ImageGenerationProviderTimeoutMs || executor.Provider.MaxBodyBytes != ImageGenerationProviderMaxBodyBytes {
		t.Fatalf("provider defaults = %+v", executor.Provider)
	}
}

func TestImageGenerationProviderRequestBody(t *testing.T) {
	compression := 80.0
	partials := 7.0
	body := ImageGenerationProviderRequestBody(
		ImageGenerationProviderRuntime{Model: "gpt-image-2"},
		"a cat",
		ImageGenerationToolConfig{
			Size: "1024x1024", Quality: "high", OutputFormat: "png",
			OutputCompression: &compression, PartialImages: &partials,
			Moderation: "low", Background: "transparent",
		},
		true,
	)
	checks := map[string]any{
		"model": "gpt-image-2", "prompt": "a cat", "n": 1,
		"size": "1024x1024", "quality": "high", "output_format": "png",
		"output_compression": 80.0, "moderation": "low", "background": "transparent",
		"stream": true, "partial_images": 3, // clamped from 7
	}
	for key, want := range checks {
		if body[key] != want {
			t.Fatalf("body[%s] = %v, want %v", key, body[key], want)
		}
	}
	if len(body) != len(checks) {
		t.Fatalf("body keys = %v", body)
	}
	// Stream false omits stream/partial fields.
	plain := ImageGenerationProviderRequestBody(ImageGenerationProviderRuntime{Model: "m"}, "p", ImageGenerationToolConfig{PartialImages: &partials}, false)
	if _, exists := plain["stream"]; exists {
		t.Fatalf("non-stream body has stream: %v", plain)
	}
}

func TestImageGenerationGenerateSuccessChains(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		outputFormat string
		wantImage    string
		wantPrompt   string
	}{
		{
			name:      "images API data[0].b64_json",
			body:      `{"data":[{"b64_json":"QUJD","revised_prompt":"a cat on a mat"}]}`,
			wantImage: "QUJD", wantPrompt: "a cat on a mat",
		},
		{
			name:      "responses API output item",
			body:      `{"output":[{"type":"image_generation_call","result":"RES=","prompt":"resp prompt"}],"revised_prompt":"top prompt"}`,
			wantImage: "RES=", wantPrompt: "top prompt",
		},
		{
			name:      "record result field fallback",
			body:      `{"result":"Tk8="}`,
			wantImage: "Tk8=", wantPrompt: "",
		},
		{
			name:      "item.result fallback",
			body:      `{"item":{"type":"image_generation_call","result":"SXRFTQ=="}}`,
			wantImage: "SXRFTQ==", wantPrompt: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			executor := NewImageGenerationExecutor(Config{}, "Bearer x", &mockTransport{handler: func(request *http.Request) (*http.Response, error) {
				return jsonResponse(200, tc.body), nil
			}}, &ImageGenerationProviderRuntime{Endpoint: "http://mock"})
			result, err := executor.Generate(context.Background(), ImageGenerationInput{Prompt: "p", Tool: ImageGenerationToolConfig{OutputFormat: tc.outputFormat}})
			if err != nil {
				t.Fatal(err)
			}
			if result.ImageBase64 != tc.wantImage || result.RevisedPrompt != tc.wantPrompt {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestImageGenerationGenerateErrors(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		body         string
		wantErrCode  string
		wantErrType  string
		wantErrStat  int
		wantProvider bool
	}{
		{
			name:   "4xx maps to 400 provider error",
			status: 422, body: `{"error":{"message":"bad prompt","type":"invalid_request_error","code":"bad_prompt","moderation_details":{"reason":"x"}}}`,
			wantErrCode: "bad_prompt", wantErrType: "invalid_request_error", wantErrStat: 400, wantProvider: true,
		},
		{
			name:   "5xx maps to 502 provider error",
			status: 500, body: `{"error":{"message":"boom"}}`,
			wantErrCode: "openai_anthropic_bridge_image_generation_provider_error", wantErrType: "upstream_error", wantErrStat: 502, wantProvider: true,
		},
		{
			name:   "missing b64 -> invalid response",
			status: 200, body: `{"data":[]}`,
			wantErrCode: "openai_anthropic_bridge_image_generation_provider_invalid_response", wantErrType: "upstream_error", wantErrStat: 502,
		},
		{
			name:   "invalid b64 shape -> invalid response",
			status: 200, body: `{"data":[{"b64_json":"not valid!"}]}`,
			wantErrCode: "openai_anthropic_bridge_image_generation_provider_invalid_response", wantErrType: "upstream_error", wantErrStat: 502,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			executor := NewImageGenerationExecutor(Config{}, "Bearer x", &mockTransport{handler: func(request *http.Request) (*http.Response, error) {
				return jsonResponse(tc.status, tc.body), nil
			}}, &ImageGenerationProviderRuntime{Endpoint: "http://mock"})
			_, err := executor.Generate(context.Background(), ImageGenerationInput{Prompt: "p"})
			if err == nil {
				t.Fatal("expected error")
			}
			if tc.wantProvider {
				providerErr, ok := err.(*ImageGenerationProviderError)
				if !ok || providerErr.Code != tc.wantErrCode || providerErr.Type != tc.wantErrType || providerErr.StatusCode != tc.wantErrStat {
					t.Fatalf("error = %v", err)
				}
				if tc.status == 422 && providerErr.ModerationDetails == nil {
					t.Fatalf("moderation details = %+v", providerErr)
				}
				return
			}
			bridgeErr, ok := err.(*BridgeRequestError)
			if !ok || bridgeErr.Code != tc.wantErrCode || bridgeErr.StatusCode != tc.wantErrStat || bridgeErr.Type != tc.wantErrType {
				t.Fatalf("error = %v", err)
			}
			if bridgeErr.Message != "图像生成 provider 响应缺少 data[0].b64_json" {
				t.Fatalf("message = %s", bridgeErr.Message)
			}
		})
	}
}

func TestImageGenerationTransportErrors(t *testing.T) {
	executor := NewImageGenerationExecutor(Config{}, "Bearer x", &mockTransport{handler: func(request *http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	}}, &ImageGenerationProviderRuntime{Endpoint: "http://mock"})
	_, err := executor.Generate(context.Background(), ImageGenerationInput{Prompt: "p"})
	bridgeErr, ok := err.(*BridgeRequestError)
	if !ok || bridgeErr.StatusCode != 502 || bridgeErr.Code != "openai_anthropic_bridge_image_generation_provider_request_failed" {
		t.Fatalf("error = %v", err)
	}
	if !strings.HasPrefix(bridgeErr.Message, "图像生成 provider 请求失败：") {
		t.Fatalf("message = %s", bridgeErr.Message)
	}
}

func TestImageGenerationProviderTimeout(t *testing.T) {
	executor := NewImageGenerationExecutor(Config{}, "Bearer x", &mockTransport{handler: func(request *http.Request) (*http.Response, error) {
		// A real transport honors the request context; emulate it.
		select {
		case <-request.Context().Done():
			return nil, request.Context().Err()
		case <-time.After(200 * time.Millisecond):
			return jsonResponse(200, `{"data":[{"b64_json":"QQ=="}]}`), nil
		}
	}}, &ImageGenerationProviderRuntime{Endpoint: "http://mock", TimeoutMs: 20})
	_, err := executor.Generate(context.Background(), ImageGenerationInput{Prompt: "p"})
	bridgeErr, ok := err.(*BridgeRequestError)
	if !ok || bridgeErr.Code != "openai_anthropic_bridge_image_generation_provider_timeout" || bridgeErr.StatusCode != 504 {
		t.Fatalf("error = %v", err)
	}
}

func TestImageGenerationResponseTooLarge(t *testing.T) {
	executor := NewImageGenerationExecutor(Config{}, "Bearer x", &mockTransport{handler: func(request *http.Request) (*http.Response, error) {
		return jsonResponse(200, strings.Repeat("a", 4096)), nil
	}}, &ImageGenerationProviderRuntime{Endpoint: "http://mock", MaxBodyBytes: 1024})
	_, err := executor.Generate(context.Background(), ImageGenerationInput{Prompt: "p"})
	bridgeErr, ok := err.(*BridgeRequestError)
	if !ok || bridgeErr.Code != "openai_anthropic_bridge_image_generation_provider_response_too_large" {
		t.Fatalf("error = %v", err)
	}
}

func TestImageGenerationStreamSSE(t *testing.T) {
	sse := "" +
		"event: image_generation.partial_image\n" +
		`data: {"b64_json":"UDE=","partial_image_index":0}` + "\n\n" +
		"event: image_generation.completed\n" +
		`data: {"b64_json":"RklO","revised_prompt":"done"}` + "\n\n"
	executor := NewImageGenerationExecutor(Config{}, "Bearer x", &mockTransport{handler: func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(sse)),
		}, nil
	}}, &ImageGenerationProviderRuntime{Endpoint: "http://mock"})
	next, err := executor.GenerateStream(context.Background(), ImageGenerationInput{Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := next()
	if err != nil || first.Type != "partial_image" || first.ImageBase64 != "UDE=" || first.PartialImageIndex == nil || *first.PartialImageIndex != 0 {
		t.Fatalf("first = %+v err %v", first, err)
	}
	second, err := next()
	if err != nil || second.Type != "completed" || second.Result == nil || second.Result.ImageBase64 != "RklO" || second.Result.RevisedPrompt != "done" {
		t.Fatalf("second = %+v err %v", second, err)
	}
	if _, err := next(); err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestImageGenerationStreamMissingCompleted(t *testing.T) {
	sse := "event: image_generation.partial_image\ndata: {\"b64_json\":\"UDE=\"}\n\n"
	executor := NewImageGenerationExecutor(Config{}, "Bearer x", &mockTransport{handler: func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(sse)),
		}, nil
	}}, &ImageGenerationProviderRuntime{Endpoint: "http://mock"})
	next, err := executor.GenerateStream(context.Background(), ImageGenerationInput{Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := next(); err != nil {
		t.Fatalf("first event err %v", err)
	}
	_, err = next()
	bridgeErr, ok := err.(*BridgeRequestError)
	if !ok || bridgeErr.Message != "图像生成 provider streaming 响应缺少最终图片结果" {
		t.Fatalf("error = %v", err)
	}
}

func TestImageGenerationStreamNonSSEAndErrorEvents(t *testing.T) {
	// Non-SSE content type renders a single completed event.
	executor := NewImageGenerationExecutor(Config{}, "Bearer x", &mockTransport{handler: func(request *http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"data":[{"b64_json":"REE="}]}`), nil
	}}, &ImageGenerationProviderRuntime{Endpoint: "http://mock"})
	next, err := executor.GenerateStream(context.Background(), ImageGenerationInput{Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	event, err := next()
	if err != nil || event.Type != "completed" || event.Result.ImageBase64 != "REE=" {
		t.Fatalf("event = %+v err %v", event, err)
	}

	// Error event payload renders the provider error (502 base).
	errorSSE := "event: error\n" + `data: {"error":{"message":"moderation blocked","code":"blocked"}}` + "\n\n"
	executor = NewImageGenerationExecutor(Config{}, "Bearer x", &mockTransport{handler: func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(errorSSE)),
		}, nil
	}}, &ImageGenerationProviderRuntime{Endpoint: "http://mock"})
	next, err = executor.GenerateStream(context.Background(), ImageGenerationInput{Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = next()
	providerErr, ok := err.(*ImageGenerationProviderError)
	if !ok || providerErr.StatusCode != 502 || providerErr.Code != "blocked" || providerErr.Message != "moderation blocked" {
		t.Fatalf("error = %v", err)
	}

	// partial_image without b64 -> invalid response error.
	badPartial := "event: image_generation.partial_image\ndata: {}\n\n"
	executor = NewImageGenerationExecutor(Config{}, "Bearer x", &mockTransport{handler: func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(badPartial)),
		}, nil
	}}, &ImageGenerationProviderRuntime{Endpoint: "http://mock"})
	next, err = executor.GenerateStream(context.Background(), ImageGenerationInput{Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = next()
	if bridgeErr, ok := err.(*BridgeRequestError); !ok || bridgeErr.Message != "图像生成 provider partial image 响应缺少 b64_json" {
		t.Fatalf("error = %v", err)
	}
}

func TestImageGenerationStreamProviderErrorStatus(t *testing.T) {
	executor := NewImageGenerationExecutor(Config{}, "Bearer x", &mockTransport{handler: func(request *http.Request) (*http.Response, error) {
		return jsonResponse(503, `{"error":{"message":"upstream down"}}`), nil
	}}, &ImageGenerationProviderRuntime{Endpoint: "http://mock"})
	_, err := executor.GenerateStream(context.Background(), ImageGenerationInput{Prompt: "p"})
	providerErr, ok := err.(*ImageGenerationProviderError)
	if !ok || providerErr.StatusCode != 502 || providerErr.Message != "upstream down" {
		t.Fatalf("error = %v", err)
	}
}

var _ = bytes.MinRead
