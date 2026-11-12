package gatewayresponse

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

func newImageInputFixture(t *testing.T, requestBody, upstreamBody []byte, vendor string) (HandleUpstreamResponseInput, *httptest.ResponseRecorder) {
	t.Helper()
	input, recorder := newInputFixture(NewSliceUpstreamBody(upstreamBody), 200,
		map[string]string{"Content-Type": "application/json"})
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(requestBody))
	input.Req = gatewaypreauth.NewGatewayRequest(httpReq)
	input.Req.Body = &gatewaybody.Request{RawBody: requestBody}
	secret := gatewayAccountFixture
	secret.VendorCode = vendor
	secret.ProtocolCode = "openai"
	input.Account = OpenAIAccountView{Account: secret}
	input.Deps = &FinalizationDeps{NowMs: func() int64 { return 1200 }}
	return input, recorder
}

func TestNonStreamQwenImageTransformToOpenAIImages(t *testing.T) {
	requestBody := []byte(`{"model":"qwen-image-3.0","prompt":"a cat","size":"1024x1024"}`)
	upstreamBody := []byte(`{"output":{"choices":[{"message":{"content":[{"type":"image","image":"https://example.com/a.png"}]}}]},"usage":{"output_image_count":1,"output_width":1024,"output_height":1024}}`)
	input, recorder := newImageInputFixture(t, requestBody, upstreamBody, "qwen")
	result, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode downstream: %v; body=%s", err, recorder.Body.String())
	}
	data, _ := out["data"].([]any)
	if len(data) != 1 || data[0].(map[string]any)["url"] != "https://example.com/a.png" {
		t.Fatalf("data = %#v", out["data"])
	}
	if _, ok := out["model"]; ok {
		t.Fatalf("qwen transform must not synthesize model: %s", recorder.Body.String())
	}
	if result.Usage.OutputImageCount == nil || *result.Usage.OutputImageCount != 1 {
		t.Fatalf("usage output_image_count = %v", result.Usage.OutputImageCount)
	}
	if got := recorder.Header().Get("X-Juhe-Image-Provider"); got != "qwen" {
		t.Fatalf("provider echo = %q", got)
	}
}

func TestNonStreamQwenImageNoImageClearsEchoHeaders(t *testing.T) {
	requestBody := []byte(`{"model":"qwen-image-3.0","prompt":"a cat"}`)
	upstreamBody := []byte(`{"output":{"choices":[]},"usage":{"image_count":0}}`)
	input, recorder := newImageInputFixture(t, requestBody, upstreamBody, "qwen")
	if _, err := HandleNonStreamUpstreamResponse(input); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	for _, name := range []string{
		"X-Juhe-Image-Provider",
		"X-Juhe-Image-Params-Applied",
		"X-Juhe-Image-Params-Ignored",
		"X-Juhe-Image-Provider-Options-Applied",
	} {
		if got := recorder.Header().Get(name); got != "" {
			t.Fatalf("failed qwen image must clear %s, got %q", name, got)
		}
	}
}

func TestNonStreamGPTImageResponseTolerance(t *testing.T) {
	// 中转返回缺 created/model/usage 的 b64 响应：纯透传成功，不按字段缺失拦截。
	requestBody := []byte(`{"model":"gpt-image-2","prompt":"a cat"}`)
	upstreamBody := []byte(`{"data":[{"b64_json":"aGVsbG8="}]}`)
	input, recorder := newImageInputFixture(t, requestBody, upstreamBody, "gpt")
	result, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.String() != string(upstreamBody) {
		t.Fatalf("gpt image response must be byte passthrough: %s", recorder.Body.String())
	}
	if result.Usage.OutputImageCount == nil || *result.Usage.OutputImageCount != 1 {
		t.Fatalf("data[] count fallback = %v", result.Usage.OutputImageCount)
	}
	if got := recorder.Header().Get("X-Juhe-Image-Provider"); got != "gpt" {
		t.Fatalf("provider echo = %q", got)
	}
}
