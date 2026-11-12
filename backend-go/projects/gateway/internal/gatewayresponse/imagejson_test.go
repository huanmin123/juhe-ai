package gatewayresponse

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
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

// newImageEditInputFixture 走编辑面（multipart 或 JSON），供编辑响应转换与
// 回显门测试使用（2026-11-12 T216）。
func newImageEditInputFixture(t *testing.T, requestBody []byte, contentType string, upstreamBody []byte, vendor string) (HandleUpstreamResponseInput, *httptest.ResponseRecorder) {
	t.Helper()
	input, recorder := newInputFixture(NewSliceUpstreamBody(upstreamBody), 200,
		map[string]string{"Content-Type": "application/json"})
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(requestBody))
	if contentType != "" {
		httpReq.Header.Set("Content-Type", contentType)
	}
	input.Req = gatewaypreauth.NewGatewayRequest(httpReq)
	input.Req.Body = &gatewaybody.Request{RawBody: requestBody, ContentTypeHeader: contentType}
	secret := gatewayAccountFixture
	secret.VendorCode = vendor
	secret.ProtocolCode = "openai"
	input.Account = OpenAIAccountView{Account: secret}
	input.Deps = &FinalizationDeps{NowMs: func() int64 { return 1200 }}
	return input, recorder
}

// imageEditMultipartFixture 构造承载 PNG 文件的 multipart 编辑请求。
func imageEditMultipartFixture(t *testing.T, fields map[string]string, withMask bool) ([]byte, string) {
	t.Helper()
	return imageEditMultipartFixtureWithImages(t, fields, 1, withMask)
}

// imageEditMultipartFixtureWithImages 构造承载多张 PNG 的 multipart 编辑
// 请求（输入图计数用；OpenAI legacy 多图形态为重复 image / image[] part）。
func imageEditMultipartFixtureWithImages(t *testing.T, fields map[string]string, imageParts int, withMask bool) ([]byte, string) {
	t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatalf("write field %s: %v", name, err)
		}
	}
	png := append([]byte("\x89PNG\r\n\x1a\n"), []byte("mock-png-body")...)
	writeFile := func(field, filename string) {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", `form-data; name="`+field+`"; filename="`+filename+`"`)
		header.Set("Content-Type", "image/png")
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatalf("create part %s: %v", field, err)
		}
		if _, err := part.Write(png); err != nil {
			t.Fatalf("write part %s: %v", field, err)
		}
	}
	for index := 0; index < imageParts; index++ {
		field := "image"
		if imageParts > 1 && index > 0 {
			field = "image[]"
		}
		writeFile(field, fmt.Sprintf("image-%d.png", index))
	}
	if withMask {
		writeFile("mask", "mask.png")
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return buffer.Bytes(), writer.FormDataContentType()
}

func TestNonStreamQwenImageTransformToOpenAIImages(t *testing.T) {
	requestBody := []byte(`{"model":"qwen-image-3.0","prompt":"a cat","size":"1024x1024","metadata":{"negative_prompt":"blur","seed":42}}`)
	upstreamBody := []byte(`{"output":{"choices":[{"message":{"content":[{"type":"image","image":"https://example.com/a.png"}]}}]},"usage":{"output_image_count":1,"output_width":1024,"output_height":1024}}`)
	input, recorder := newImageInputFixture(t, requestBody, upstreamBody, "qwen")
	var requestObject map[string]any
	if err := json.Unmarshal(requestBody, &requestObject); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	input.Req.Body.Body = requestObject
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
	metadata, _ := out["metadata"].(map[string]any)
	if metadata["negative_prompt"] != "blur" || metadata["seed"] != float64(42) {
		t.Fatalf("metadata = %#v, want request metadata", out["metadata"])
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
	if got := recorder.Header().Get("X-Juhe-Image-Provider-Options-Applied"); !strings.Contains(got, "negative_prompt") {
		t.Fatalf("provider options applied echo = %q, want negative_prompt", got)
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

func TestNonStreamGPTImageStreamIgnoredEcho(t *testing.T) {
	// 图像统一契约不提供 stream：gpt 请求带 stream/partial_images 也只返回 JSON，
	// 响应头回显被忽略的参数。
	requestBody := []byte(`{"model":"gpt-image-2","prompt":"a cat","stream":true,"partial_images":1}`)
	upstreamBody := []byte(`{"data":[{"b64_json":"aGVsbG8="}]}`)
	input, recorder := newImageInputFixture(t, requestBody, upstreamBody, "gpt")
	var requestObject map[string]any
	if err := json.Unmarshal(requestBody, &requestObject); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	input.Req.Body.Body = requestObject
	if _, err := HandleNonStreamUpstreamResponse(input); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.String() != string(upstreamBody) {
		t.Fatalf("gpt image response must stay JSON passthrough: %s", recorder.Body.String())
	}
	if got := recorder.Header().Get("X-Juhe-Image-Params-Ignored"); got != "partial_images,stream" {
		t.Fatalf("params ignored echo = %q, want partial_images,stream", got)
	}
	if got := recorder.Header().Get("X-Juhe-Image-Provider"); got != "gpt" {
		t.Fatalf("provider echo = %q", got)
	}
}

func TestNonStreamQwenImageEditTransformToOpenAIImages(t *testing.T) {
	// 2026-11-12 T216：qwen 编辑响应走与生成面同一转换（multipart 请求），
	// output.choices[].message.content[].image → data[].url，
	// output_image_count 取实际图片数。
	requestBody, contentType := imageEditMultipartFixture(t, map[string]string{
		"model":           "qwen-image-3.0",
		"prompt":          "把背景换成海边",
		"response_format": "url",
		"stream":          "true",
		"metadata":        `{"negative_prompt":"blur"}`,
	}, true)
	upstreamBody := []byte(`{"output":{"choices":[{"message":{"content":[{"type":"image","image":"https://example.com/edit.png"}]}}]},"usage":{"output_image_count":1}}`)
	input, recorder := newImageEditInputFixture(t, requestBody, contentType, upstreamBody, "qwen")
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
	if len(data) != 1 || data[0].(map[string]any)["url"] != "https://example.com/edit.png" {
		t.Fatalf("data = %#v", out["data"])
	}
	if _, ok := out["metadata"]; ok {
		t.Fatalf("multipart edit must not echo metadata: %s", recorder.Body.String())
	}
	if result.Usage.OutputImageCount == nil || *result.Usage.OutputImageCount != 1 {
		t.Fatalf("usage output_image_count = %v", result.Usage.OutputImageCount)
	}
	if got := recorder.Header().Get("X-Juhe-Image-Provider"); got != "qwen" {
		t.Fatalf("provider echo = %q", got)
	}
	ignored := recorder.Header().Get("X-Juhe-Image-Params-Ignored")
	for _, key := range []string{"mask", "response_format", "stream"} {
		if !strings.Contains(ignored, key) {
			t.Fatalf("params ignored echo = %q, want %s", ignored, key)
		}
	}
}

func TestNonStreamQwenImageEditJSONTransformEchoesMetadata(t *testing.T) {
	// JSON 编辑与生成面同口径：请求 metadata 原值随转换响应写回 body。
	requestBody := []byte(`{"model":"qwen-image-3.0","prompt":"p","images":[{"image_url":"https://example.com/a.png"}],"metadata":{"negative_prompt":"blur"}}`)
	upstreamBody := []byte(`{"output":{"choices":[{"message":{"content":[{"type":"image","image":"https://example.com/edit.png"}]}}]},"usage":{"output_image_count":1}}`)
	input, recorder := newImageEditInputFixture(t, requestBody, "application/json", upstreamBody, "qwen")
	var requestObject map[string]any
	if err := json.Unmarshal(requestBody, &requestObject); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	input.Req.Body.Body = requestObject
	if _, err := HandleNonStreamUpstreamResponse(input); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode downstream: %v; body=%s", err, recorder.Body.String())
	}
	metadata, _ := out["metadata"].(map[string]any)
	if metadata["negative_prompt"] != "blur" {
		t.Fatalf("metadata = %#v, want request metadata", out["metadata"])
	}
	if got := recorder.Header().Get("X-Juhe-Image-Provider-Options-Applied"); !strings.Contains(got, "negative_prompt") {
		t.Fatalf("provider options applied echo = %q, want negative_prompt", got)
	}
}

func TestNonStreamGPTImageEditMultipartEchoAndPassthrough(t *testing.T) {
	// gpt 编辑 multipart：响应字节透传，回显头由重新解析原始 body 计算
	//（stream 删除并回显；mask 原生支持不回显 ignored）。
	requestBody, contentType := imageEditMultipartFixture(t, map[string]string{
		"model":  "gpt-image-2",
		"prompt": "把猫换成狗",
		"stream": "true",
	}, true)
	upstreamBody := []byte(`{"data":[{"b64_json":"aGVsbG8="}]}`)
	input, recorder := newImageEditInputFixture(t, requestBody, contentType, upstreamBody, "gpt")
	if _, err := HandleNonStreamUpstreamResponse(input); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.String() != string(upstreamBody) {
		t.Fatalf("gpt edit response must be byte passthrough: %s", recorder.Body.String())
	}
	if got := recorder.Header().Get("X-Juhe-Image-Provider"); got != "gpt" {
		t.Fatalf("provider echo = %q", got)
	}
	ignored := recorder.Header().Get("X-Juhe-Image-Params-Ignored")
	if !strings.Contains(ignored, "stream") {
		t.Fatalf("params ignored echo = %q, want stream", ignored)
	}
	if strings.Contains(ignored, "mask") {
		t.Fatalf("gpt mask is native, must not be ignored: %q", ignored)
	}
}

func TestNonStreamImageEditEchoWithoutRequestBody(t *testing.T) {
	// 编辑面 Body 缺失时回显只落 provider，不 panic（防守 nil Body）。
	upstreamBody := []byte(`{"data":[{"url":"https://example.com/edit.png"}]}`)
	input, recorder := newImageEditInputFixture(t, nil, "", upstreamBody, "xai")
	input.Req.Body = nil
	if _, err := HandleNonStreamUpstreamResponse(input); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("X-Juhe-Image-Provider"); got != "xai" {
		t.Fatalf("provider echo = %q", got)
	}
}

func TestNonStreamImageOutputPixels(t *testing.T) {
	// 2026-11-12 档位批：图像请求 size W×H 解析为 OutputImagePixels，供
	// 按像素档选按张价（Seedream 5.0 Pro ≤2.61M/>2.61M）。
	requestBody := []byte(`{"model":"doubao-seedream-5-0-pro-260628","prompt":"a cat","size":"2048x2048"}`)
	upstreamBody := []byte(`{"data":[{"url":"https://example.com/large.png"}],"usage":{"generated_images":1}}`)
	input, _ := newImageInputFixture(t, requestBody, upstreamBody, "volcengine")
	result, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if result.Usage.OutputImagePixels == nil || *result.Usage.OutputImagePixels != 2048*2048 {
		t.Fatalf("output image pixels = %v want %d", result.Usage.OutputImagePixels, 2048*2048)
	}
}

func TestNonStreamImageInputImageCount(t *testing.T) {
	// 2026-11-12 输入素材批：请求 image 数组 → InputImageCount 张数。
	requestBody := []byte(`{"model":"doubao-seedream-5-0-pro-260628","prompt":"a cat","size":"1024x1024","image":["https://example.com/a.png","https://example.com/b.png"]}`)
	upstreamBody := []byte(`{"data":[{"url":"https://example.com/out.png"}],"usage":{"generated_images":1}}`)
	input, _ := newImageInputFixture(t, requestBody, upstreamBody, "volcengine")
	result, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if result.Usage.InputImageCount == nil || *result.Usage.InputImageCount != 2 {
		t.Fatalf("input image count = %v want 2", result.Usage.InputImageCount)
	}

	// 生成面单图字符串同样计 1。
	single := []byte(`{"model":"doubao-seedream-5-0-pro-260628","prompt":"a cat","image":"https://example.com/a.png"}`)
	singleInput, _ := newImageInputFixture(t, single, upstreamBody, "volcengine")
	singleResult, err := HandleNonStreamUpstreamResponse(singleInput)
	if err != nil {
		t.Fatalf("handle single: %v", err)
	}
	if singleResult.Usage.InputImageCount == nil || *singleResult.Usage.InputImageCount != 1 {
		t.Fatalf("single input image count = %v want 1", singleResult.Usage.InputImageCount)
	}
}

func TestNonStreamImageEditJSONInputImageCount(t *testing.T) {
	// 2026-11-13 编辑面补齐：JSON images[] 按元素数计输入图。
	requestBody := []byte(`{"model":"qwen-image-3.0","prompt":"p","images":[{"image_url":"https://example.com/a.png"},{"image_url":"https://example.com/b.png"}]}`)
	upstreamBody := []byte(`{"output":{"choices":[{"message":{"content":[{"type":"image","image":"https://example.com/edit.png"}]}}]},"usage":{"output_image_count":1}}`)
	input, _ := newImageEditInputFixture(t, requestBody, "application/json", upstreamBody, "qwen")
	result, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if result.Usage.InputImageCount == nil || *result.Usage.InputImageCount != 2 {
		t.Fatalf("json edit input image count = %v want 2", result.Usage.InputImageCount)
	}
}

func TestNonStreamImageEditMultipartInputImageCount(t *testing.T) {
	// 2026-11-13 编辑面补齐：multipart image / image[] part 计数，mask 不计。
	requestBody, contentType := imageEditMultipartFixtureWithImages(t, map[string]string{
		"model":  "doubao-seedream-5-0-pro-260628",
		"prompt": "把背景换成海边",
	}, 2, true)
	upstreamBody := []byte(`{"data":[{"url":"https://example.com/edit.png"}],"usage":{"generated_images":1}}`)
	input, recorder := newImageEditInputFixture(t, requestBody, contentType, upstreamBody, "volcengine")
	result, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if result.Usage.InputImageCount == nil || *result.Usage.InputImageCount != 2 {
		t.Fatalf("multipart edit input image count = %v want 2", result.Usage.InputImageCount)
	}
}

func TestNonStreamImageEditMultipartInputImageCountFieldVariants(t *testing.T) {
	// 2026-11-13 独立复审 B1：输入图计数的字段归类与编辑解析器共用同一判定
	//（image[N] / 大小写 / 空白变体均计入；mask 与未知字段不计）。修复前
	// image[0] 形态参与生成与计费但漏记输入图。
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	_ = writer.WriteField("model", "doubao-seedream-5-0-pro-260628")
	_ = writer.WriteField("prompt", "把背景换成海边")
	png := append([]byte("\x89PNG\r\n\x1a\n"), []byte("mock-png-body")...)
	for _, field := range []string{"IMAGE[0]", "Image[1]", " image ", "image[]", "mask", "attachment"} {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", `form-data; name="`+field+`"; filename="f.png"`)
		header.Set("Content-Type", "image/png")
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatalf("create part %s: %v", field, err)
		}
		if _, err := part.Write(png); err != nil {
			t.Fatalf("write part %s: %v", field, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	upstreamBody := []byte(`{"data":[{"url":"https://example.com/edit.png"}],"usage":{"generated_images":1}}`)
	input, recorder := newImageEditInputFixture(t, buffer.Bytes(), writer.FormDataContentType(), upstreamBody, "volcengine")
	result, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	// 4 张参考图（IMAGE[0]/Image[1]/image/image[]）；mask 与 attachment 不计。
	if result.Usage.InputImageCount == nil || *result.Usage.InputImageCount != 4 {
		t.Fatalf("multipart edit input image count = %v want 4", result.Usage.InputImageCount)
	}
}

func TestNonStreamImageInputImageCountFromMetadata(t *testing.T) {
	// 2026-11-13：metadata 的其余键会投影为该厂商 provider_options 并
	// deep-merge 进上游报文；metadata.image/images 必须计入输入图费用，
	// 否则 Seedream i2i 的 L3 参考图漏计。
	requestBody := []byte(`{"model":"doubao-seedream-5-0-pro-260628","prompt":"a cat","size":"1024x1024","metadata":{"image":["https://example.com/a.png","https://example.com/b.png"]}}`)
	upstreamBody := []byte(`{"data":[{"url":"https://example.com/out.png"}],"usage":{"generated_images":1}}`)
	input, _ := newImageInputFixture(t, requestBody, upstreamBody, "volcengine")
	result, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if result.Usage.InputImageCount == nil || *result.Usage.InputImageCount != 2 {
		t.Fatalf("metadata input image count = %v want 2", result.Usage.InputImageCount)
	}
}

func TestNonStreamImageEditJSONMetadataImageNotCounted(t *testing.T) {
	// 编辑面 metadata.image 会被统一契约剔除，不得覆盖/替代 images[] 计数。
	requestBody := []byte(`{"model":"qwen-image-3.0","prompt":"p","images":[{"image_url":"https://example.com/a.png"},{"image_url":"https://example.com/b.png"}],"metadata":{"image":"https://example.com/ignored.png"}}`)
	upstreamBody := []byte(`{"output":{"choices":[{"message":{"content":[{"type":"image","image":"https://example.com/edit.png"}]}}]},"usage":{"output_image_count":1}}`)
	input, _ := newImageEditInputFixture(t, requestBody, "application/json", upstreamBody, "qwen")
	result, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if result.Usage.InputImageCount == nil || *result.Usage.InputImageCount != 2 {
		t.Fatalf("edit input image count = %v want 2（metadata.image 不计）", result.Usage.InputImageCount)
	}
}
