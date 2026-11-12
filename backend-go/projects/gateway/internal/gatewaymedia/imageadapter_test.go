package gatewaymedia

import (
	"encoding/json"
	"strings"
	"testing"
)

func decodeImageTestBody(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode image body: %v; raw=%s", err, raw)
	}
	return body
}

func TestIsImageGenerationPath(t *testing.T) {
	cases := map[string]bool{
		"/v1/images/generations":     true,
		"/images/generations":        true,
		"/v1/images/generations?x=1": true,
		"/v1/images/edits":           false,
		"/v1/images/generations/x":   false,
		"/v1/chat/completions":       false,
	}
	for path, want := range cases {
		if got := IsImageGenerationPath(path); got != want {
			t.Errorf("IsImageGenerationPath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestOpenAIImageAdapterPassthroughWithoutProviderOptions(t *testing.T) {
	raw := []byte(`{"model":"gpt-image-2","prompt":"a cat"}`)
	got, err := BuildImageUpstreamRequest("gpt", raw)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got != nil {
		t.Fatalf("gpt without provider_options should preserve bytes, got %s", got)
	}
}

func TestOpenAIImageAdapterIgnoresStreamParams(t *testing.T) {
	raw := []byte(`{"model":"gpt-image-2","prompt":"a cat","stream":true,"partial_images":1}`)
	got, err := BuildImageUpstreamRequest("gpt", raw)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	body := decodeImageTestBody(t, got)
	for _, key := range []string{"stream", "partial_images"} {
		if _, ok := body[key]; ok {
			t.Fatalf("%s must be removed: %s", key, got)
		}
	}
	if body["model"] != "gpt-image-2" || body["prompt"] != "a cat" {
		t.Fatalf("base params changed: %s", got)
	}
	echo := ImageEchoForRequest("gpt", decodeImageTestBody(t, raw))
	for _, key := range []string{"stream", "partial_images"} {
		if !imageTestContainsString(echo.ParamsIgnored, key) {
			t.Fatalf("params ignored = %v, want %s", echo.ParamsIgnored, key)
		}
	}
}

func TestOpenAIImageAdapterMergesProviderOptions(t *testing.T) {
	raw := []byte(`{"model":"gpt-image-2","prompt":"a cat","provider_options":{"openai":{"quality":"high"},"xai":{"n":2}}}`)
	got, err := BuildImageUpstreamRequest("gpt", raw)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	body := decodeImageTestBody(t, got)
	if body["quality"] != "high" {
		t.Fatalf("quality = %v, want high", body["quality"])
	}
	if _, ok := body["provider_options"]; ok {
		t.Fatalf("provider_options must not be forwarded: %s", got)
	}
	if _, ok := body["n"]; ok {
		t.Fatalf("other provider subobject must be ignored: %s", got)
	}
}

func TestXaiImageAdapterIgnoresOpenAIOnlyParams(t *testing.T) {
	raw := []byte(`{"model":"grok-imagine-image-2.0","prompt":"a cat","stream":true,"partial_images":2,"quality":"high","n":1,"size":"1024x1024","provider_options":{"xai":{"response_format":"url"}}}`)
	got, err := BuildImageUpstreamRequest("xai", raw)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	body := decodeImageTestBody(t, got)
	for _, key := range []string{"stream", "partial_images", "quality", "provider_options"} {
		if _, ok := body[key]; ok {
			t.Fatalf("%s must be removed: %s", key, got)
		}
	}
	if body["response_format"] != "url" || body["size"] != "1024x1024" {
		t.Fatalf("documented params should pass through: %s", got)
	}
	echo := ImageEchoForRequest("xai", decodeImageTestBody(t, raw))
	if !imageTestContainsString(echo.ParamsIgnored, "stream") || !imageTestContainsString(echo.ParamsIgnored, "quality") {
		t.Fatalf("params ignored = %v, want stream/quality", echo.ParamsIgnored)
	}
}

func TestVolcengineImageAdapterDefaultsWatermarkFalse(t *testing.T) {
	raw := []byte(`{"model":"doubao-seedream-5-0-flash-260915","prompt":"a cat","quality":"high","stream":true,"provider_options":{"volcengine":{"sequential_image_generation":"disabled"}}}`)
	got, err := BuildImageUpstreamRequest("volcengine", raw)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	body := decodeImageTestBody(t, got)
	if body["watermark"] != false {
		t.Fatalf("watermark = %v, want false", body["watermark"])
	}
	if body["sequential_image_generation"] != "disabled" {
		t.Fatalf("provider option not merged: %s", got)
	}
	for _, key := range []string{"quality", "stream", "provider_options"} {
		if _, ok := body[key]; ok {
			t.Fatalf("%s must be removed: %s", key, got)
		}
	}
}

func TestVolcengineImageAdapterProviderOptionsCanEnableWatermark(t *testing.T) {
	raw := []byte(`{"model":"doubao-seedream-5-0-flash-260915","prompt":"a cat","provider_options":{"volcengine":{"watermark":true}}}`)
	got, err := BuildImageUpstreamRequest("volcengine", raw)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	body := decodeImageTestBody(t, got)
	if body["watermark"] != true {
		t.Fatalf("watermark = %v, want true", body["watermark"])
	}
}

func TestQwenImageAdapterBuildsNativeRequest(t *testing.T) {
	raw := []byte(`{"model":"qwen-image-3.0","prompt":"a cat","n":2,"size":"1024x1024","response_format":"b64_json","stream":true,"provider_options":{"qwen":{"negative_prompt":"blur","prompt_extend":false,"seed":42,"stream":true,"quality":"high"}}}`)
	got, err := BuildImageUpstreamRequest("qwen", raw)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	body := decodeImageTestBody(t, got)
	if body["model"] != "qwen-image-3.0" {
		t.Fatalf("model = %v", body["model"])
	}
	input, _ := body["input"].(map[string]any)
	messages, _ := input["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages = %#v", input["messages"])
	}
	message, _ := messages[0].(map[string]any)
	content, _ := message["content"].([]any)
	if len(content) != 1 || content[0].(map[string]any)["text"] != "a cat" {
		t.Fatalf("content = %#v", content)
	}
	parameters, _ := body["parameters"].(map[string]any)
	if parameters["size"] != "1024*1024" || parameters["n"] != float64(2) {
		t.Fatalf("parameters = %#v", parameters)
	}
	if parameters["negative_prompt"] != "blur" || parameters["prompt_extend"] != false || parameters["seed"] != float64(42) {
		t.Fatalf("provider options not merged into parameters: %#v", parameters)
	}
	if parameters["watermark"] != false {
		t.Fatalf("watermark = %v, want false", parameters["watermark"])
	}
	for _, key := range []string{"stream", "quality"} {
		if _, ok := parameters[key]; ok {
			t.Fatalf("provider_options 内的 %s 也必须忽略: %#v", key, parameters)
		}
	}
	echo := ImageEchoForRequest("qwen", decodeImageTestBody(t, raw))
	if !imageTestContainsString(echo.ParamsIgnored, "response_format") || !imageTestContainsString(echo.ParamsIgnored, "stream") {
		t.Fatalf("params ignored = %v, want response_format/stream", echo.ParamsIgnored)
	}
}

func TestQwenImageAdapterRequiresPrompt(t *testing.T) {
	if _, err := BuildImageUpstreamRequest("qwen", []byte(`{"model":"qwen-image-3.0"}`)); err == nil {
		t.Fatal("qwen image without prompt should fail")
	}
}

func TestQwenImageTransformResponse20And30Usage(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "2.0 image_count",
			body: `{"output":{"choices":[{"message":{"content":[{"image":"https://example.com/a.png"}]}}]},"usage":{"image_count":1,"width":1024,"height":1024},"request_id":"r1"}`,
		},
		{
			name: "3.0 output_image_count",
			body: `{"output":{"choices":[{"message":{"content":[{"type":"image","image":"https://example.com/a.png"}]}}]},"usage":{"output_image_count":1,"output_width":1024,"output_height":1024},"request_id":"r2"}`,
		},
	}
	adapter := ImageAdapterForProvider("qwen")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := adapter.TransformResponse(ImageRequest{}, []byte(tc.body))
			if err != nil {
				t.Fatalf("transform: %v", err)
			}
			var out map[string]any
			if err := json.Unmarshal(got, &out); err != nil {
				t.Fatalf("decode transformed: %v", err)
			}
			data, _ := out["data"].([]any)
			if len(data) != 1 || data[0].(map[string]any)["url"] != "https://example.com/a.png" {
				t.Fatalf("data = %#v", out["data"])
			}
			usage, _ := out["usage"].(map[string]any)
			if usage["output_image_count"] != float64(1) {
				t.Fatalf("usage = %#v", usage)
			}
			if _, ok := out["model"]; ok {
				t.Fatalf("qwen transform must not synthesize model: %s", got)
			}
		})
	}
}

func TestQwenImageTransformResponseWithoutImageFails(t *testing.T) {
	adapter := ImageAdapterForProvider("qwen")
	if _, err := adapter.TransformResponse(ImageRequest{}, []byte(`{"output":{"choices":[]},"usage":{"image_count":0}}`)); err == nil {
		t.Fatal("no image should fail")
	}
}

func TestQwenImageAdapterSizeAutoIgnored(t *testing.T) {
	raw := []byte(`{"model":"qwen-image-3.0","prompt":"a cat","size":"auto"}`)
	got, err := BuildImageUpstreamRequest("qwen", raw)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	body := decodeImageTestBody(t, got)
	parameters, _ := body["parameters"].(map[string]any)
	if _, ok := parameters["size"]; ok {
		t.Fatalf("qwen size=auto must be omitted: %#v", parameters)
	}
	echo := ImageEchoForRequest("qwen", decodeImageTestBody(t, raw))
	if !imageTestContainsString(echo.ParamsIgnored, "size") {
		t.Fatalf("size=auto should be params_ignored: %v", echo.ParamsIgnored)
	}
	if imageTestContainsString(echo.ParamsApplied, "size") {
		t.Fatalf("size=auto must not be params_applied: %v", echo.ParamsApplied)
	}
}

func TestBuildImageUpstreamRequestRejectsNonObjectProviderOptions(t *testing.T) {
	if _, err := BuildImageUpstreamRequest("gpt", []byte(`{"model":"gpt-image-2","prompt":"a cat","provider_options":{"gpt":"bad"}}`)); err == nil {
		t.Fatal("non-object provider_options should fail")
	}
	if _, err := BuildImageUpstreamRequest("qwen", []byte(`{"model":"qwen-image-3.0","prompt":"a cat","provider_options":[]}`)); err == nil {
		t.Fatal("array provider_options should fail")
	}
}

func TestQwenImageBaseURLNormalization(t *testing.T) {
	cases := map[string]string{
		"https://dashscope.aliyuncs.com":                      "https://dashscope.aliyuncs.com",
		"https://dashscope.aliyuncs.com/":                     "https://dashscope.aliyuncs.com",
		"https://dashscope.aliyuncs.com/compatible-mode/v1":   "https://dashscope.aliyuncs.com",
		"https://dashscope.aliyuncs.com/api/v1":               "https://dashscope.aliyuncs.com",
		"https://dashscope.aliyuncs.com/v1":                   "https://dashscope.aliyuncs.com",
		"https://custom.example/dashscope/compatible-mode/v1": "https://custom.example/dashscope",
		"https://ark.cn-beijing.volces.com/api/v3":            "https://ark.cn-beijing.volces.com/api/v3",
	}
	for input, want := range cases {
		if got := ImageBaseURLForProvider("qwen", input); got != want {
			t.Errorf("qwen base %q = %q, want %q", input, got, want)
		}
	}
	if got := ImageBaseURLForProvider("volcengine", "https://ark.cn-beijing.volces.com/api/v3"); got != "https://ark.cn-beijing.volces.com/api/v3" {
		t.Fatalf("non-qwen base must be unchanged, got %q", got)
	}
}

func TestImageEchoHeaders(t *testing.T) {
	headers := ImageEchoHeaders(ImageEcho{
		Provider:               "qwen",
		ParamsApplied:          []string{"n", "size", "watermark"},
		ParamsIgnored:          []string{"response_format", "stream"},
		ProviderOptionsApplied: []string{"negative_prompt", "seed"},
	})
	if headers[ImageProviderHeader] != "qwen" {
		t.Fatalf("provider header = %q", headers[ImageProviderHeader])
	}
	if !strings.Contains(headers[ImageParamsAppliedHeader], "watermark") {
		t.Fatalf("params applied = %q", headers[ImageParamsAppliedHeader])
	}
	if !strings.Contains(headers[ImageParamsIgnoredHeader], "stream") {
		t.Fatalf("params ignored = %q", headers[ImageParamsIgnoredHeader])
	}
	if !strings.Contains(headers[ImageProviderOptionsAppliedHeader], "seed") {
		t.Fatalf("provider options = %q", headers[ImageProviderOptionsAppliedHeader])
	}
}

func imageTestContainsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
