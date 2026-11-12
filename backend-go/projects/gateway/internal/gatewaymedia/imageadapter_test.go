package gatewaymedia

import (
	"bytes"
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

func TestOpenAIImageAdapterPassthroughWithoutMetadata(t *testing.T) {
	raw := []byte(`{"model":"gpt-image-2","prompt":"a cat"}`)
	result, err := BuildImageUpstreamRequestForPath("gpt", "/v1/images/generations", "application/json", raw)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if result.Body != nil {
		t.Fatalf("gpt without metadata should preserve bytes, got %s", result.Body)
	}
}

func TestImageMetadataNullTreatedAsAbsent(t *testing.T) {
	raw := []byte(`{"model":"gpt-image-2","prompt":"a cat","metadata":null}`)
	result, err := BuildImageUpstreamRequestForPath("gpt", "/v1/images/generations", "application/json", raw)
	if err != nil {
		t.Fatalf("metadata null should be tolerated: %v", err)
	}
	if result.Body == nil {
		t.Fatal("metadata null should be removed from wire body")
	}
	body := decodeImageTestBody(t, result.Body)
	for _, key := range []string{"metadata", "provider_options"} {
		if _, ok := body[key]; ok {
			t.Fatalf("%s must not be forwarded: %s", key, result.Body)
		}
	}
}

func TestOpenAIImageAdapterIgnoresStreamParams(t *testing.T) {
	raw := []byte(`{"model":"gpt-image-2","prompt":"a cat","stream":true,"partial_images":1}`)
	result, err := BuildImageUpstreamRequestForPath("gpt", "/v1/images/generations", "application/json", raw)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	got := result.Body
	body := decodeImageTestBody(t, got)
	for _, key := range []string{"stream", "partial_images"} {
		if _, ok := body[key]; ok {
			t.Fatalf("%s must be removed: %s", key, got)
		}
	}
	if body["model"] != "gpt-image-2" || body["prompt"] != "a cat" {
		t.Fatalf("base params changed: %s", got)
	}
	for _, key := range []string{"stream", "partial_images"} {
		if !imageTestContainsString(result.ParamsIgnored, key) {
			t.Fatalf("params ignored = %v, want %s", result.ParamsIgnored, key)
		}
	}
}

func TestOpenAIImageAdapterMergesMetadata(t *testing.T) {
	raw := []byte(`{"model":"gpt-image-2","prompt":"a cat","metadata":{"quality":"high"}}`)
	result := mustBuildImageForPath(t, "gpt", "/v1/images/generations", "application/json", raw)
	body := decodeImageTestBody(t, result.Body)
	if body["quality"] != "high" {
		t.Fatalf("quality = %v, want high", body["quality"])
	}
	for _, key := range []string{"metadata", "provider_options"} {
		if _, ok := body[key]; ok {
			t.Fatalf("%s must not be forwarded: %s", key, result.Body)
		}
	}
	if !imageTestContainsString(result.ProviderOptionsApplied, "quality") {
		t.Fatalf("provider options applied = %v, want quality", result.ProviderOptionsApplied)
	}
}

// 公开 provider_options 已退役，但 adapter 仍消费内部投影：直接调用 adapter
// 验证 provider_options 子对象 deep-merge 语义未被移除。
func TestOpenAIImageAdapterBuildMergesInternalProviderOptions(t *testing.T) {
	adapter := openAIImageAdapter{}
	result, err := adapter.Build(ParseImageRequest(map[string]any{
		"model":            "gpt-image-2",
		"prompt":           "a cat",
		"provider_options": map[string]any{"openai": map[string]any{"quality": "high"}},
	}, "gpt"))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	body := decodeImageTestBody(t, result.Body)
	if body["quality"] != "high" {
		t.Fatalf("quality = %v, want high", body["quality"])
	}
	if _, ok := body["provider_options"]; ok {
		t.Fatalf("provider_options must not be forwarded: %s", result.Body)
	}
	if !imageTestContainsString(result.ProviderOptionsApplied, "quality") {
		t.Fatalf("provider options applied = %v, want quality", result.ProviderOptionsApplied)
	}
}

func TestXaiImageAdapterIgnoresOpenAIOnlyParams(t *testing.T) {
	raw := []byte(`{"model":"grok-imagine-image-2.0","prompt":"a cat","stream":true,"partial_images":2,"quality":"high","n":1,"size":"1024x1024","metadata":{"response_format":"url"}}`)
	result := mustBuildImageForPath(t, "xai", "/v1/images/generations", "application/json", raw)
	body := decodeImageTestBody(t, result.Body)
	for _, key := range []string{"stream", "partial_images", "quality", "metadata", "provider_options"} {
		if _, ok := body[key]; ok {
			t.Fatalf("%s must be removed: %s", key, result.Body)
		}
	}
	if body["response_format"] != "url" || body["size"] != "1024x1024" {
		t.Fatalf("documented params should pass through: %s", result.Body)
	}
	echo := ImageEchoForPath("xai", "/v1/images/generations", "application/json", raw)
	if !imageTestContainsString(echo.ParamsIgnored, "stream") || !imageTestContainsString(echo.ParamsIgnored, "quality") {
		t.Fatalf("params ignored = %v, want stream/quality", echo.ParamsIgnored)
	}
	if echo.Metadata["response_format"] != "url" {
		t.Fatalf("echo metadata = %#v, want response_format", echo.Metadata)
	}
}

func TestVolcengineImageAdapterDefaultsWatermarkFalse(t *testing.T) {
	raw := []byte(`{"model":"doubao-seedream-5-0-flash-260915","prompt":"a cat","quality":"high","stream":true,"metadata":{"sequential_image_generation":"disabled"}}`)
	result := mustBuildImageForPath(t, "volcengine", "/v1/images/generations", "application/json", raw)
	body := decodeImageTestBody(t, result.Body)
	if body["watermark"] != false {
		t.Fatalf("watermark = %v, want false", body["watermark"])
	}
	if body["sequential_image_generation"] != "disabled" {
		t.Fatalf("metadata not merged: %s", result.Body)
	}
	for _, key := range []string{"quality", "stream", "metadata", "provider_options"} {
		if _, ok := body[key]; ok {
			t.Fatalf("%s must be removed: %s", key, result.Body)
		}
	}
}

func TestVolcengineImageAdapterMetadataCanEnableWatermark(t *testing.T) {
	raw := []byte(`{"model":"doubao-seedream-5-0-flash-260915","prompt":"a cat","metadata":{"watermark":true}}`)
	result := mustBuildImageForPath(t, "volcengine", "/v1/images/generations", "application/json", raw)
	body := decodeImageTestBody(t, result.Body)
	if body["watermark"] != true {
		t.Fatalf("watermark = %v, want true", body["watermark"])
	}
}

func TestQwenImageAdapterBuildsNativeRequest(t *testing.T) {
	raw := []byte(`{"model":"qwen-image-3.0","prompt":"a cat","n":2,"size":"1024x1024","response_format":"b64_json","stream":true,"metadata":{"negative_prompt":"blur","prompt_extend":false,"seed":42,"stream":true,"quality":"high"}}`)
	result := mustBuildImageForPath(t, "qwen", "/v1/images/generations", "application/json", raw)
	body := decodeImageTestBody(t, result.Body)
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
			t.Fatalf("metadata 内的 %s 也必须忽略: %#v", key, parameters)
		}
	}
	echo := ImageEchoForPath("qwen", "/v1/images/generations", "application/json", raw)
	if !imageTestContainsString(echo.ParamsIgnored, "response_format") || !imageTestContainsString(echo.ParamsIgnored, "stream") {
		t.Fatalf("params ignored = %v, want response_format/stream", echo.ParamsIgnored)
	}
	if echo.Metadata["negative_prompt"] != "blur" {
		t.Fatalf("echo metadata = %#v, want negative_prompt", echo.Metadata)
	}
}

func TestQwenImageAdapterRequiresPrompt(t *testing.T) {
	if _, err := BuildImageUpstreamRequestForPath("qwen", "/v1/images/generations", "application/json", []byte(`{"model":"qwen-image-3.0"}`)); err == nil {
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

// qwen 转换响应写请求 metadata 原值（仅非空时）；无 metadata 不产生该键。
func TestQwenImageTransformResponseEchoesMetadata(t *testing.T) {
	adapter := ImageAdapterForProvider("qwen")
	upstream := []byte(`{"output":{"choices":[{"message":{"content":[{"image":"https://example.com/a.png"}]}}]},"usage":{"image_count":1}}`)
	metadata := map[string]any{"negative_prompt": "blur", "seed": float64(42)}
	transformed, err := adapter.TransformResponse(ImageRequest{Metadata: metadata}, upstream)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(transformed, &out); err != nil {
		t.Fatalf("decode transformed: %v", err)
	}
	echoed, _ := out["metadata"].(map[string]any)
	if echoed["negative_prompt"] != "blur" || echoed["seed"] != float64(42) {
		t.Fatalf("metadata = %#v, want request metadata", out["metadata"])
	}
	withoutMetadata, err := adapter.TransformResponse(ImageRequest{}, upstream)
	if err != nil {
		t.Fatalf("transform without metadata: %v", err)
	}
	var emptyOut map[string]any
	if err := json.Unmarshal(withoutMetadata, &emptyOut); err != nil {
		t.Fatalf("decode transformed: %v", err)
	}
	if _, ok := emptyOut["metadata"]; ok {
		t.Fatalf("empty request metadata must not add key: %s", withoutMetadata)
	}
}

func TestQwenImageAdapterSizeAutoIgnored(t *testing.T) {
	raw := []byte(`{"model":"qwen-image-3.0","prompt":"a cat","size":"auto"}`)
	result, err := BuildImageUpstreamRequestForPath("qwen", "/v1/images/generations", "application/json", raw)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	body := decodeImageTestBody(t, result.Body)
	parameters, _ := body["parameters"].(map[string]any)
	if _, ok := parameters["size"]; ok {
		t.Fatalf("qwen size=auto must be omitted: %#v", parameters)
	}
	if !imageTestContainsString(result.ParamsIgnored, "size") {
		t.Fatalf("size=auto should be params_ignored: %v", result.ParamsIgnored)
	}
	if imageTestContainsString(result.ParamsApplied, "size") {
		t.Fatalf("size=auto must not be params_applied: %v", result.ParamsApplied)
	}
}

func TestBuildImageRequestForPathRejectsRetiredProviderOptions(t *testing.T) {
	generation := []byte(`{"model":"gpt-image-2","prompt":"a cat","provider_options":{"gpt":{"quality":"high"}}}`)
	for _, provider := range []string{"gpt", "xai", "volcengine", "qwen"} {
		_, err := BuildImageUpstreamRequestForPath(provider, "/v1/images/generations", "application/json", generation)
		if err == nil || !strings.Contains(err.Error(), "provider_options 已退役") {
			t.Fatalf("%s generation provider_options error = %v, want retired hint", provider, err)
		}
	}
	edit := []byte(`{"model":"gpt-image-2","prompt":"p","images":[{"image_url":"https://example.com/a.png"}],"provider_options":{"openai":{"quality":"high"}}}`)
	for _, provider := range []string{"gpt", "xai", "volcengine", "qwen"} {
		_, err := BuildImageUpstreamRequestForPath(provider, "/v1/images/edits", "application/json", edit)
		if err == nil || !strings.Contains(err.Error(), "provider_options 已退役") {
			t.Fatalf("%s edit provider_options error = %v, want retired hint", provider, err)
		}
	}
}

func TestBuildImageRequestForPathRejectsNonObjectMetadata(t *testing.T) {
	cases := []string{
		`{"model":"gpt-image-2","prompt":"a cat","metadata":"x"}`,
		`{"model":"gpt-image-2","prompt":"a cat","metadata":[]}`,
	}
	for _, raw := range cases {
		_, err := BuildImageUpstreamRequestForPath("gpt", "/v1/images/generations", "application/json", []byte(raw))
		if err == nil || !strings.Contains(err.Error(), "metadata 必须是 JSON 对象") {
			t.Fatalf("metadata case %s error = %v, want object hint", raw, err)
		}
	}
	edit := []byte(`{"model":"gpt-image-2","prompt":"p","images":[{"image_url":"https://example.com/a.png"}],"metadata":"x"}`)
	if _, err := BuildImageUpstreamRequestForPath("gpt", "/v1/images/edits", "application/json", edit); err == nil ||
		!strings.Contains(err.Error(), "metadata 必须是 JSON 对象") {
		t.Fatalf("edit metadata error = %v, want object hint", err)
	}
}

// metadata 的投影键是 adapter 注册键（gpt → openai），且 metadata 不留在
// 归一后的 body 中。
func TestNormalizeImageMetadataBodyProjectsToAdapterProviderKey(t *testing.T) {
	normalized, metadata, err := normalizeImageMetadataBody("gpt",
		[]byte(`{"model":"x","prompt":"p","metadata":{"quality":"high"}}`))
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if metadata["quality"] != "high" {
		t.Fatalf("metadata = %#v", metadata)
	}
	body := decodeImageTestBody(t, normalized)
	if _, ok := body["metadata"]; ok {
		t.Fatalf("metadata must be removed: %s", normalized)
	}
	options, _ := body["provider_options"].(map[string]any)
	if _, ok := options["openai"]; !ok {
		t.Fatalf("provider_options = %#v, want openai key", body["provider_options"])
	}
	// 无 metadata 时保持原始字节（零改写）。
	original := []byte(`{"model":"x","prompt":"p"}`)
	unchanged, absent, err := normalizeImageMetadataBody("gpt", original)
	if err != nil || absent != nil || !bytes.Equal(unchanged, original) {
		t.Fatalf("zero rewrite failed: body=%s metadata=%#v err=%v", unchanged, absent, err)
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
