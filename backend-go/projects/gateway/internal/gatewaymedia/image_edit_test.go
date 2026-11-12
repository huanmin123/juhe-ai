package gatewaymedia

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"strings"
	"testing"
)

// imageTestPNG 是带 PNG magic 的最小载荷（http.DetectContentType 识别为
// image/png）。
var imageTestPNG = append([]byte("\x89PNG\r\n\x1a\n"), []byte("mock-png-body")...)

type imageEditTestField struct {
	name  string
	value string
}

type imageEditTestFile struct {
	field       string
	filename    string
	contentType string
	data        []byte
}

// buildImageEditMultipart 构造 multipart/form-data 编辑请求体（返回原始
// body 与 Content-Type）。
func buildImageEditMultipart(t *testing.T, fields []imageEditTestField, files []imageEditTestFile) ([]byte, string) {
	t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	for _, field := range fields {
		if err := writer.WriteField(field.name, field.value); err != nil {
			t.Fatalf("write field %s: %v", field.name, err)
		}
	}
	for _, file := range files {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", `form-data; name="`+file.field+`"; filename="`+file.filename+`"`)
		if file.contentType != "" {
			header.Set("Content-Type", file.contentType)
		}
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatalf("create file part %s: %v", file.field, err)
		}
		if _, err := part.Write(file.data); err != nil {
			t.Fatalf("write file part %s: %v", file.field, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return buffer.Bytes(), writer.FormDataContentType()
}

func imageTestDataURL(data []byte) string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
}

func mustBuildImageForPath(t *testing.T, provider, path, contentType string, rawBody []byte) ImageBuildResult {
	t.Helper()
	result, err := BuildImageUpstreamRequestForPath(provider, path, contentType, rawBody)
	if err != nil {
		t.Fatalf("build image request for %s: %v", provider, err)
	}
	return result
}

func TestImagePathPredicates(t *testing.T) {
	cases := []struct {
		path         string
		wantImage    bool
		wantGenerate bool
		wantEdit     bool
	}{
		{path: "/v1/images/generations", wantImage: true, wantGenerate: true},
		{path: "/images/generations", wantImage: true, wantGenerate: true},
		{path: "/v1/images/generations?x=1", wantImage: true, wantGenerate: true},
		{path: "/v1/images/edits", wantImage: true, wantEdit: true},
		{path: "/images/edits?x=1", wantImage: true, wantEdit: true},
		{path: "/v1/images/edits/", wantImage: true, wantEdit: true},
		{path: "/v1/chat/completions"},
		{path: "/v1/images/generations/x"},
		{path: "/v1/images/edits-x"},
		{path: "/"},
	}
	for _, tc := range cases {
		if got := IsImagePath(tc.path); got != tc.wantImage {
			t.Errorf("IsImagePath(%q) = %v, want %v", tc.path, got, tc.wantImage)
		}
		if got := IsImageGenerationPath(tc.path); got != tc.wantGenerate {
			t.Errorf("IsImageGenerationPath(%q) = %v, want %v", tc.path, got, tc.wantGenerate)
		}
		if got := IsImageEditPath(tc.path); got != tc.wantEdit {
			t.Errorf("IsImageEditPath(%q) = %v, want %v", tc.path, got, tc.wantEdit)
		}
	}
	// 生成判定语义不变：编辑路径不属于生成面。
	if IsImageGenerationPath("/v1/images/edits") {
		t.Fatal("IsImageGenerationPath(/v1/images/edits) must stay false")
	}
}

func TestParseImageEditRequestMultipart(t *testing.T) {
	contentType := ""
	rawBody := []byte(nil)
	t.Run("单图 + mask + 文本字段", func(t *testing.T) {
		rawBody, contentType = buildImageEditMultipart(t,
			[]imageEditTestField{{name: "model", value: "gpt-image-2"}, {name: "prompt", value: "把猫换成狗"}, {name: "n", value: "2"}},
			[]imageEditTestFile{
				{field: "image", filename: "cat.png", contentType: "image/png", data: imageTestPNG},
				{field: "mask", filename: "mask.png", contentType: "image/png", data: imageTestPNG},
			})
		request, err := ParseImageEditRequest("/v1/images/edits", contentType, rawBody, "gpt")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if request.Operation != ImageOperationEdit {
			t.Fatalf("operation = %q", request.Operation)
		}
		if len(request.Files) != 1 || request.Files[0].Field != "image" || request.Files[0].MIMEType != "image/png" {
			t.Fatalf("files = %#v", request.Files)
		}
		if request.Mask == nil || request.Mask.MIMEType != "image/png" {
			t.Fatalf("mask = %#v", request.Mask)
		}
		if request.Body["model"] != "gpt-image-2" || request.Body["prompt"] != "把猫换成狗" {
			t.Fatalf("body = %#v", request.Body)
		}
		if request.Body["n"] != float64(2) {
			t.Fatalf("n = %#v, want numeric 2", request.Body["n"])
		}
		if request.Multipart == nil || len(request.Multipart.Parts) != 5 {
			t.Fatalf("multipart parts = %#v", request.Multipart)
		}
	})

	t.Run("image[] 多图", func(t *testing.T) {
		rawBody, contentType = buildImageEditMultipart(t,
			[]imageEditTestField{{name: "model", value: "xai-image"}, {name: "prompt", value: "p"}},
			[]imageEditTestFile{
				{field: "image[]", filename: "a.png", contentType: "image/png", data: imageTestPNG},
				{field: "image[0]", filename: "b.png", contentType: "image/png", data: imageTestPNG},
				{field: "image[1]", filename: "c.png", contentType: "image/png", data: imageTestPNG},
			})
		request, err := ParseImageEditRequest("/v1/images/edits", contentType, rawBody, "xai")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if len(request.Files) != 3 {
			t.Fatalf("files = %d, want 3", len(request.Files))
		}
	})

	t.Run("超过 16 张图", func(t *testing.T) {
		files := make([]imageEditTestFile, 0, imageEditMaxImages+1)
		for i := 0; i < imageEditMaxImages+1; i++ {
			files = append(files, imageEditTestFile{field: "image[]", filename: "a.png", contentType: "image/png", data: imageTestPNG})
		}
		rawBody, contentType = buildImageEditMultipart(t,
			[]imageEditTestField{{name: "model", value: "m"}, {name: "prompt", value: "p"}}, files)
		if _, err := ParseImageEditRequest("/v1/images/edits", contentType, rawBody, "xai"); err == nil {
			t.Fatal("17 images should fail")
		}
	})

	t.Run("单图超过 10MB", func(t *testing.T) {
		oversize := bytes.Repeat([]byte{0x89}, imageEditMaxImageBytes+1)
		rawBody, contentType = buildImageEditMultipart(t,
			[]imageEditTestField{{name: "model", value: "m"}, {name: "prompt", value: "p"}},
			[]imageEditTestFile{{field: "image", filename: "big.png", contentType: "image/png", data: oversize}})
		if _, err := ParseImageEditRequest("/v1/images/edits", contentType, rawBody, "gpt"); err == nil {
			t.Fatal("oversize image should fail")
		}
	})

	t.Run("未知文件字段", func(t *testing.T) {
		rawBody, contentType = buildImageEditMultipart(t,
			[]imageEditTestField{{name: "model", value: "m"}, {name: "prompt", value: "p"}},
			[]imageEditTestFile{{field: "upload", filename: "a.png", contentType: "image/png", data: imageTestPNG}})
		if _, err := ParseImageEditRequest("/v1/images/edits", contentType, rawBody, "gpt"); err == nil {
			t.Fatal("unknown file field should fail")
		}
	})

	t.Run("非图片 MIME", func(t *testing.T) {
		rawBody, contentType = buildImageEditMultipart(t,
			[]imageEditTestField{{name: "model", value: "m"}, {name: "prompt", value: "p"}},
			[]imageEditTestFile{{field: "image", filename: "a.txt", contentType: "text/plain", data: []byte("hello")}})
		if _, err := ParseImageEditRequest("/v1/images/edits", contentType, rawBody, "gpt"); err == nil {
			t.Fatal("non-image MIME should fail")
		}
	})

	t.Run("缺 prompt", func(t *testing.T) {
		rawBody, contentType = buildImageEditMultipart(t,
			[]imageEditTestField{{name: "model", value: "m"}},
			[]imageEditTestFile{{field: "image", filename: "a.png", contentType: "image/png", data: imageTestPNG}})
		if _, err := ParseImageEditRequest("/v1/images/edits", contentType, rawBody, "gpt"); err == nil {
			t.Fatal("missing prompt should fail")
		}
	})

	t.Run("缺 model", func(t *testing.T) {
		rawBody, contentType = buildImageEditMultipart(t,
			[]imageEditTestField{{name: "prompt", value: "p"}},
			[]imageEditTestFile{{field: "image", filename: "a.png", contentType: "image/png", data: imageTestPNG}})
		if _, err := ParseImageEditRequest("/v1/images/edits", contentType, rawBody, "gpt"); err == nil {
			t.Fatal("missing model should fail")
		}
	})

	t.Run("文本字段超过 64KB", func(t *testing.T) {
		rawBody, contentType = buildImageEditMultipart(t,
			[]imageEditTestField{{name: "model", value: "m"}, {name: "prompt", value: strings.Repeat("p", imageEditMaxTextBytes+1)}},
			[]imageEditTestFile{{field: "image", filename: "a.png", contentType: "image/png", data: imageTestPNG}})
		if _, err := ParseImageEditRequest("/v1/images/edits", contentType, rawBody, "gpt"); err == nil {
			t.Fatal("oversize text field should fail")
		}
	})

	t.Run("part 超过 64 个", func(t *testing.T) {
		fields := make([]imageEditTestField, 0, imageEditMaxParts+1)
		fields = append(fields, imageEditTestField{name: "model", value: "m"}, imageEditTestField{name: "prompt", value: "p"})
		for i := 0; i < imageEditMaxParts; i++ {
			fields = append(fields, imageEditTestField{name: "user", value: "u"})
		}
		rawBody, contentType = buildImageEditMultipart(t, fields, nil)
		if _, err := ParseImageEditRequest("/v1/images/edits", contentType, rawBody, "gpt"); err == nil {
			t.Fatal("too many parts should fail")
		}
	})
}

func TestParseImageEditRequestJSON(t *testing.T) {
	t.Run("images 数组 + mask", func(t *testing.T) {
		raw := []byte(`{"model":"gpt-image-2","prompt":"p","images":[{"image_url":"` + imageTestDataURL(imageTestPNG) + `"}],"mask":{"image_url":"` + imageTestDataURL(imageTestPNG) + `"}}`)
		request, err := ParseImageEditRequest("/v1/images/edits", "application/json", raw, "gpt")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if request.Multipart != nil || len(request.Files) != 0 {
			t.Fatalf("json request must not carry multipart files: %#v", request)
		}
		if request.Operation != ImageOperationEdit {
			t.Fatalf("operation = %q", request.Operation)
		}
	})

	t.Run("非 gpt 的 file_id 拒绝", func(t *testing.T) {
		raw := []byte(`{"model":"m","prompt":"p","images":[{"file_id":"file-123"}]}`)
		if _, err := ParseImageEditRequest("/v1/images/edits", "application/json", raw, "xai"); err == nil {
			t.Fatal("non-gpt file_id should fail")
		}
		if _, err := ParseImageEditRequest("/v1/images/edits", "application/json", raw, "gpt"); err != nil {
			t.Fatalf("gpt file_id must pass through: %v", err)
		}
	})

	t.Run("超过 16 张图", func(t *testing.T) {
		items := make([]string, 0, imageEditMaxImages+1)
		for i := 0; i < imageEditMaxImages+1; i++ {
			items = append(items, `{"image_url":"https://example.com/a.png"}`)
		}
		raw := []byte(`{"model":"m","prompt":"p","images":[` + strings.Join(items, ",") + `]}`)
		if _, err := ParseImageEditRequest("/v1/images/edits", "application/json", raw, "xai"); err == nil {
			t.Fatal("17 json images should fail")
		}
	})

	t.Run("非图片 data URL", func(t *testing.T) {
		raw := []byte(`{"model":"m","prompt":"p","images":[{"image_url":"data:text/plain;base64,aGVsbG8="}]}`)
		if _, err := ParseImageEditRequest("/v1/images/edits", "application/json", raw, "xai"); err == nil {
			t.Fatal("non-image data URL should fail")
		}
	})

	t.Run("非法 JSON", func(t *testing.T) {
		if _, err := ParseImageEditRequest("/v1/images/edits", "application/json", []byte(`{`), "gpt"); err == nil {
			t.Fatal("invalid JSON should fail")
		}
	})

	t.Run("非编辑路径", func(t *testing.T) {
		raw := []byte(`{"model":"m","prompt":"p"}`)
		if _, err := ParseImageEditRequest("/v1/images/generations", "application/json", raw, "gpt"); err == nil {
			t.Fatal("generation path must not parse as edit")
		}
	})
}

func TestGPTImageEditMultipartPassthrough(t *testing.T) {
	rawBody, contentType := buildImageEditMultipart(t,
		[]imageEditTestField{
			{name: "model", value: "gpt-image-2"},
			{name: "prompt", value: "把猫换成狗"},
			{name: "response_format", value: "b64_json"},
		},
		[]imageEditTestFile{{field: "image", filename: "cat.png", contentType: "image/png", data: imageTestPNG}})

	result := mustBuildImageForPath(t, "gpt", "/v1/images/edits", contentType, rawBody)
	if result.Body != nil {
		t.Fatalf("gpt multipart without stream must stay byte passthrough, got %s", result.Body)
	}
	if result.ContentType != "" {
		t.Fatalf("passthrough ContentType = %q, want empty", result.ContentType)
	}
	if len(result.ParamsIgnored) != 0 {
		t.Fatalf("params ignored = %v", result.ParamsIgnored)
	}

	// 带 stream/partial_images：重建 multipart，删除两个文本字段并回显。
	streamBody, streamContentType := buildImageEditMultipart(t,
		[]imageEditTestField{
			{name: "model", value: "gpt-image-2"},
			{name: "prompt", value: "把猫换成狗"},
			{name: "stream", value: "true"},
			{name: "partial_images", value: "2"},
			{name: "response_format", value: "b64_json"},
		},
		[]imageEditTestFile{{field: "image", filename: "cat.png", contentType: "image/png", data: imageTestPNG}})
	rebuilt := mustBuildImageForPath(t, "gpt", "/v1/images/edits", streamContentType, streamBody)
	if rebuilt.Body == nil {
		t.Fatal("gpt multipart with stream must be rebuilt")
	}
	if !strings.HasPrefix(rebuilt.ContentType, "multipart/form-data; boundary=") {
		t.Fatalf("rebuilt ContentType = %q", rebuilt.ContentType)
	}
	if strings.Join(rebuilt.ParamsIgnored, ",") != "partial_images,stream" {
		t.Fatalf("params ignored = %v", rebuilt.ParamsIgnored)
	}
	parts := readImageMultipartParts(t, rebuilt.Body, rebuilt.ContentType)
	if _, ok := parts["stream"]; ok {
		t.Fatal("stream part must be removed")
	}
	if _, ok := parts["partial_images"]; ok {
		t.Fatal("partial_images part must be removed")
	}
	if parts["model"] != "gpt-image-2" || parts["prompt"] != "把猫换成狗" || parts["response_format"] != "b64_json" {
		t.Fatalf("text parts changed: %#v", parts)
	}
	if !bytes.Equal(parts["image"].([]byte), imageTestPNG) {
		t.Fatalf("image bytes changed: %#v", parts["image"])
	}
}

func readImageMultipartParts(t *testing.T, rawBody []byte, contentType string) map[string]any {
	t.Helper()
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatalf("parse rebuilt content type %q: %v", contentType, err)
	}
	reader := multipart.NewReader(bytes.NewReader(rawBody), params["boundary"])
	parts := map[string]any{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read rebuilt part: %v", err)
		}
		var buffer bytes.Buffer
		if _, err := buffer.ReadFrom(part); err != nil {
			t.Fatalf("read part: %v", err)
		}
		_ = part.Close()
		if part.FileName() != "" {
			parts[part.FormName()] = buffer.Bytes()
			continue
		}
		parts[part.FormName()] = buffer.String()
	}
	return parts
}

func TestGPTImageEditJSONUsesBuild(t *testing.T) {
	raw := []byte(`{"model":"gpt-image-2","prompt":"p","images":[{"image_url":"https://example.com/a.png"}],"stream":true,"partial_images":1,"metadata":{"quality":"high"}}`)
	result := mustBuildImageForPath(t, "gpt", "/v1/images/edits", "application/json", raw)
	if result.Body == nil {
		t.Fatalf("result = %#v", result)
	}
	// gpt JSON 编辑复用生成面 Build：ContentType 留空由调用方回落 JSON。
	if result.ContentType != "" && result.ContentType != "application/json" {
		t.Fatalf("ContentType = %q", result.ContentType)
	}
	body := decodeImageTestBody(t, result.Body)
	if _, ok := body["stream"]; ok {
		t.Fatalf("stream must be dropped: %s", result.Body)
	}
	if _, ok := body["partial_images"]; ok {
		t.Fatalf("partial_images must be dropped: %s", result.Body)
	}
	if _, ok := body["metadata"]; ok {
		t.Fatalf("metadata must not be forwarded: %s", result.Body)
	}
	if _, ok := body["provider_options"]; ok {
		t.Fatalf("provider_options must not be forwarded: %s", result.Body)
	}
	if body["quality"] != "high" {
		t.Fatalf("metadata not merged: %s", result.Body)
	}
}

func TestXAIImageEditBuild(t *testing.T) {
	t.Run("multipart 单图写 image:{url}", func(t *testing.T) {
		rawBody, contentType := buildImageEditMultipart(t,
			[]imageEditTestField{
				{name: "model", value: "grok-imagine-image-2.0"},
				{name: "prompt", value: "p"},
				{name: "quality", value: "high"},
				{name: "size", value: "1024x1024"},
				{name: "response_format", value: "url"},
			},
			[]imageEditTestFile{
				{field: "image", filename: "a.png", contentType: "image/png", data: imageTestPNG},
				{field: "mask", filename: "m.png", contentType: "image/png", data: imageTestPNG},
			})
		result := mustBuildImageForPath(t, "xai", "/v1/images/edits", contentType, rawBody)
		body := decodeImageTestBody(t, result.Body)
		image, _ := body["image"].(map[string]any)
		if image == nil || image["url"] != imageTestDataURL(imageTestPNG) {
			t.Fatalf("image = %#v", body["image"])
		}
		if _, ok := body["images"]; ok {
			t.Fatalf("single image must not emit images[]: %s", result.Body)
		}
		if _, ok := body["mask"]; ok {
			t.Fatalf("mask must not be forwarded: %s", result.Body)
		}
		if _, ok := body["quality"]; ok {
			t.Fatalf("quality must not be forwarded: %s", result.Body)
		}
		if body["size"] != "1024x1024" || body["response_format"] != "url" {
			t.Fatalf("documented params must pass through: %s", result.Body)
		}
		for _, key := range []string{"mask", "quality"} {
			if !imageTestContainsString(result.ParamsIgnored, key) {
				t.Fatalf("params ignored = %v, want %s", result.ParamsIgnored, key)
			}
		}
	})

	t.Run("JSON 多图写 images:[{url}]", func(t *testing.T) {
		raw := []byte(`{"model":"grok-imagine-image-2.0","prompt":"p","images":[{"image_url":"https://example.com/a.png"},{"image_url":"` + imageTestDataURL(imageTestPNG) + `"}],"metadata":{"aspect_ratio":"1:1"}}`)
		result := mustBuildImageForPath(t, "xai", "/v1/images/edits", "application/json", raw)
		body := decodeImageTestBody(t, result.Body)
		images, _ := body["images"].([]any)
		if len(images) != 2 {
			t.Fatalf("images = %#v", body["images"])
		}
		if images[0].(map[string]any)["url"] != "https://example.com/a.png" {
			t.Fatalf("images[0] = %#v", images[0])
		}
		if _, ok := body["image"]; ok {
			t.Fatalf("multi image must use images[] only: %s", result.Body)
		}
		if body["aspect_ratio"] != "1:1" {
			t.Fatalf("metadata not merged: %s", result.Body)
		}
		if result.ProviderOptionsApplied == nil || result.ProviderOptionsApplied[0] != "aspect_ratio" {
			t.Fatalf("provider options applied = %v", result.ProviderOptionsApplied)
		}
	})

	t.Run("JSON file_id 拒绝", func(t *testing.T) {
		raw := []byte(`{"model":"m","prompt":"p","images":[{"file_id":"file-1"}]}`)
		if _, err := BuildImageUpstreamRequestForPath("xai", "/v1/images/edits", "application/json", raw); err == nil {
			t.Fatal("xai file_id should fail")
		}
	})
}

func TestVolcengineImageEditBuild(t *testing.T) {
	if got := (volcengineImageAdapter{}).UpstreamPath("/v1/images/edits"); got != "/images/generations" {
		t.Fatalf("edits upstream path = %q", got)
	}
	if got := (volcengineImageAdapter{}).UpstreamPath("/v1/images/edits?x=1"); got != "/images/generations?x=1" {
		t.Fatalf("edits upstream path with query = %q", got)
	}
	if got := (volcengineImageAdapter{}).UpstreamPath("/v1/images/generations"); got != "/v1/images/generations" {
		t.Fatalf("generation upstream path must stay unchanged, got %q", got)
	}

	rawBody, contentType := buildImageEditMultipart(t,
		[]imageEditTestField{
			{name: "model", value: "doubao-seedream-5-0-flash-260915"},
			{name: "prompt", value: "p"},
			{name: "response_format", value: "url"},
			{name: "output_format", value: "png"},
			{name: "background", value: "opaque"},
			{name: "quality", value: "high"},
		},
		[]imageEditTestFile{
			{field: "image", filename: "a.png", contentType: "image/png", data: imageTestPNG},
			{field: "mask", filename: "m.png", contentType: "image/png", data: imageTestPNG},
		})
	result := mustBuildImageForPath(t, "volcengine", "/v1/images/edits", contentType, rawBody)
	body := decodeImageTestBody(t, result.Body)
	if body["image"] != imageTestDataURL(imageTestPNG) {
		t.Fatalf("image = %#v", body["image"])
	}
	if body["watermark"] != false {
		t.Fatalf("watermark = %v, want false", body["watermark"])
	}
	for _, key := range []string{"mask", "quality"} {
		if _, ok := body[key]; ok {
			t.Fatalf("%s must not be forwarded: %s", key, result.Body)
		}
		if !imageTestContainsString(result.ParamsIgnored, key) {
			t.Fatalf("params ignored = %v, want %s", result.ParamsIgnored, key)
		}
	}
	if body["response_format"] != "url" || body["output_format"] != "png" || body["background"] != "opaque" {
		t.Fatalf("documented params must pass through: %s", result.Body)
	}

	jsonRaw := []byte(`{"model":"m","prompt":"p","images":[{"image_url":"https://example.com/a.png"},{"image_url":"https://example.com/b.png"}],"metadata":{"watermark":true}}`)
	jsonResult := mustBuildImageForPath(t, "volcengine", "/v1/images/edits", "application/json", jsonRaw)
	jsonBody := decodeImageTestBody(t, jsonResult.Body)
	images, _ := jsonBody["image"].([]any)
	if len(images) != 2 || images[0] != "https://example.com/a.png" {
		t.Fatalf("image = %#v", jsonBody["image"])
	}
	if jsonBody["watermark"] != true {
		t.Fatalf("metadata.watermark must override: %s", jsonResult.Body)
	}
}

func TestQwenImageEditBuild(t *testing.T) {
	if got := (qwenImageAdapter{}).UpstreamPath("/v1/images/edits"); got != qwenImageUpstreamPath {
		t.Fatalf("qwen edit upstream path = %q", got)
	}

	rawBody, contentType := buildImageEditMultipart(t,
		[]imageEditTestField{
			{name: "model", value: "qwen-image-3.0"},
			{name: "prompt", value: "把背景换成海边"},
			{name: "n", value: "2"},
			{name: "size", value: "1024x1024"},
			{name: "response_format", value: "url"},
			{name: "quality", value: "high"},
		},
		[]imageEditTestFile{
			{field: "image[]", filename: "a.png", contentType: "image/png", data: imageTestPNG},
			{field: "image[]", filename: "b.png", contentType: "image/png", data: imageTestPNG},
			{field: "mask", filename: "m.png", contentType: "image/png", data: imageTestPNG},
		})
	result := mustBuildImageForPath(t, "qwen", "/v1/images/edits", contentType, rawBody)
	body := decodeImageTestBody(t, result.Body)
	if body["model"] != "qwen-image-3.0" {
		t.Fatalf("model = %v", body["model"])
	}
	input, _ := body["input"].(map[string]any)
	messages, _ := input["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages = %#v", input["messages"])
	}
	content, _ := messages[0].(map[string]any)["content"].([]any)
	if len(content) != 3 {
		t.Fatalf("content = %#v", content)
	}
	for index := 0; index < 2; index++ {
		if content[index].(map[string]any)["image"] != imageTestDataURL(imageTestPNG) {
			t.Fatalf("content[%d] = %#v", index, content[index])
		}
	}
	if content[2].(map[string]any)["text"] != "把背景换成海边" {
		t.Fatalf("text content = %#v", content[2])
	}
	parameters, _ := body["parameters"].(map[string]any)
	if parameters["size"] != "1024*1024" || parameters["n"] != float64(2) || parameters["watermark"] != false {
		t.Fatalf("parameters = %#v", parameters)
	}
	for _, key := range []string{"mask", "quality", "response_format"} {
		if !imageTestContainsString(result.ParamsIgnored, key) {
			t.Fatalf("params ignored = %v, want %s", result.ParamsIgnored, key)
		}
	}

	jsonRaw := []byte(`{"model":"qwen-image-3.0","prompt":"p","images":[{"image_url":"https://example.com/a.png"}],"metadata":{"negative_prompt":"blur","seed":42}}`)
	jsonResult := mustBuildImageForPath(t, "qwen", "/v1/images/edits", "application/json", jsonRaw)
	jsonBody := decodeImageTestBody(t, jsonResult.Body)
	jsonInput, _ := jsonBody["input"].(map[string]any)
	jsonMessages, _ := jsonInput["messages"].([]any)
	jsonContent, _ := jsonMessages[0].(map[string]any)["content"].([]any)
	if len(jsonContent) != 2 || jsonContent[0].(map[string]any)["image"] != "https://example.com/a.png" {
		t.Fatalf("content = %#v", jsonContent)
	}
	jsonParameters, _ := jsonBody["parameters"].(map[string]any)
	if jsonParameters["negative_prompt"] != "blur" || jsonParameters["seed"] != float64(42) {
		t.Fatalf("metadata not merged: %#v", jsonParameters)
	}
	if jsonResult.ProviderOptionsApplied == nil || !imageTestContainsString(jsonResult.ProviderOptionsApplied, "negative_prompt") {
		t.Fatalf("provider options applied = %v", jsonResult.ProviderOptionsApplied)
	}
}

func TestImageEchoForPathMultipartEdit(t *testing.T) {
	rawBody, contentType := buildImageEditMultipart(t,
		[]imageEditTestField{
			{name: "model", value: "grok-imagine-image-2.0"},
			{name: "prompt", value: "p"},
			{name: "quality", value: "high"},
			{name: "stream", value: "true"},
		},
		[]imageEditTestFile{
			{field: "image", filename: "a.png", contentType: "image/png", data: imageTestPNG},
			{field: "mask", filename: "m.png", contentType: "image/png", data: imageTestPNG},
		})
	echo := ImageEchoForPath("xai", "/v1/images/edits", contentType, rawBody)
	if echo.Provider != "xai" {
		t.Fatalf("provider = %q", echo.Provider)
	}
	for _, key := range []string{"mask", "quality", "stream"} {
		if !imageTestContainsString(echo.ParamsIgnored, key) {
			t.Fatalf("params ignored = %v, want %s", echo.ParamsIgnored, key)
		}
	}

	gptBody, gptContentType := buildImageEditMultipart(t,
		[]imageEditTestField{
			{name: "model", value: "gpt-image-2"},
			{name: "prompt", value: "p"},
			{name: "stream", value: "true"},
		},
		[]imageEditTestFile{{field: "image", filename: "a.png", contentType: "image/png", data: imageTestPNG}})
	gptEcho := ImageEchoForPath("gpt", "/v1/images/edits", gptContentType, gptBody)
	if gptEcho.Provider != "gpt" || !imageTestContainsString(gptEcho.ParamsIgnored, "stream") {
		t.Fatalf("gpt echo = %#v", gptEcho)
	}
	if imageTestContainsString(gptEcho.ParamsIgnored, "mask") {
		t.Fatalf("gpt mask is natively supported, must not be ignored: %v", gptEcho.ParamsIgnored)
	}

	// 生成面回显语义不变：仍从 JSON body 计算。
	generationEcho := ImageEchoForPath("gpt", "/v1/images/generations", "application/json",
		[]byte(`{"model":"gpt-image-2","prompt":"p","stream":true}`))
	if !imageTestContainsString(generationEcho.ParamsIgnored, "stream") {
		t.Fatalf("generation echo = %#v", generationEcho)
	}

	// Body 缺失 / 空 rawBody 时只回显 provider，不 panic。
	if echo := ImageEchoForPath("qwen", "/v1/images/edits", "", nil); echo.Provider != "qwen" {
		t.Fatalf("empty body echo = %#v", echo)
	}
	if echo := ImageEchoForPath("unknown", "/v1/images/edits", "", nil); echo.Provider != "unknown" {
		t.Fatalf("unknown provider echo = %#v", echo)
	}
}

func TestImageEditBuildRequiresModelAndPrompt(t *testing.T) {
	for _, provider := range []string{"xai", "volcengine", "qwen"} {
		raw := []byte(`{"model":"m","images":[{"image_url":"https://example.com/a.png"}]}`)
		if _, err := BuildImageUpstreamRequestForPath(provider, "/v1/images/edits", "application/json", raw); err == nil {
			t.Fatalf("%s edit without prompt should fail", provider)
		}
	}
}

func TestImageEditRequiresAtLeastOneImage(t *testing.T) {
	for _, provider := range []string{"gpt", "xai", "volcengine", "qwen"} {
		raw := []byte(`{"model":"m","prompt":"edit it"}`)
		if _, err := BuildImageUpstreamRequestForPath(provider, "/v1/images/edits", "application/json", raw); err == nil {
			t.Fatalf("%s edit without image should fail", provider)
		}
	}
	// gpt 的 JSON file_id 是合法图片来源，不被“至少一张图”拦截。
	gptFileID := []byte(`{"model":"gpt-image-2","prompt":"edit","images":[{"file_id":"file_123"}]}`)
	if _, err := BuildImageUpstreamRequestForPath("gpt", "/v1/images/edits", "application/json", gptFileID); err != nil {
		t.Fatalf("gpt file_id edit should pass presence check: %v", err)
	}
	// mask 不计入参考图。
	rawMaskOnly := []byte(`{"model":"m","prompt":"edit","mask":{"image_url":"https://example.com/mask.png"}}`)
	if _, err := BuildImageUpstreamRequestForPath("xai", "/v1/images/edits", "application/json", rawMaskOnly); err == nil {
		t.Fatalf("mask-only edit should fail image presence check")
	}
}

// 2026-11-12 复审批 Blockers：JSON 顶层 image 只接受字符串；数组形态必须
// 400（否则参考图被静默丢弃后按文生图计费，且绕过 16 张/10MB/MIME 校验）。
func TestImageEditRejectsTopLevelImageArray(t *testing.T) {
	raw := []byte(`{"model":"m","prompt":"p","image":["https://example.com/a.png"]}`)
	for _, provider := range []string{"gpt", "xai", "volcengine", "qwen"} {
		_, err := BuildImageUpstreamRequestForPath(provider, "/v1/images/edits", "application/json", raw)
		if err == nil {
			t.Fatalf("%s top-level image array must be rejected", provider)
		}
		if !strings.Contains(err.Error(), "images 数组") {
			t.Fatalf("%s error = %v, want images 数组 hint", provider, err)
		}
	}
	// 顶层 image 字符串仍是合法来源（volcengine 单图字符串 / qwen content）。
	stringRaw := []byte(`{"model":"m","prompt":"p","image":"https://example.com/a.png"}`)
	volcBody := decodeImageTestBody(t, mustBuildImageForPath(t, "volcengine", "/v1/images/edits", "application/json", stringRaw).Body)
	if volcBody["image"] != "https://example.com/a.png" {
		t.Fatalf("volcengine top-level image string = %#v", volcBody["image"])
	}
	qwenBody := decodeImageTestBody(t, mustBuildImageForPath(t, "qwen", "/v1/images/edits", "application/json", stringRaw).Body)
	qwenInput, _ := qwenBody["input"].(map[string]any)
	qwenMessages, _ := qwenInput["messages"].([]any)
	qwenContent, _ := qwenMessages[0].(map[string]any)["content"].([]any)
	if len(qwenContent) != 2 || qwenContent[0].(map[string]any)["image"] != "https://example.com/a.png" {
		t.Fatalf("qwen top-level image string content = %#v", qwenContent)
	}
}

// metadata 不得绕过 fixed forbidden 列表（stream 可能让上游返回 SSE）。
func TestXAIImageEditMetadataCannotBypassIgnored(t *testing.T) {
	raw := []byte(`{"model":"grok-imagine-image-2.0","prompt":"p","images":[{"image_url":"https://example.com/a.png"}],"metadata":{"stream":true,"quality":"high"}}`)
	result := mustBuildImageForPath(t, "xai", "/v1/images/edits", "application/json", raw)
	body := decodeImageTestBody(t, result.Body)
	for _, key := range []string{"stream", "quality"} {
		if _, ok := body[key]; ok {
			t.Fatalf("xai wire body must not carry metadata.%s: %s", key, result.Body)
		}
		if !imageTestContainsString(result.ParamsIgnored, key) {
			t.Fatalf("params ignored = %v, want %s", result.ParamsIgnored, key)
		}
		if imageTestContainsString(result.ProviderOptionsApplied, key) {
			t.Fatalf("provider options applied = %v, must not contain %s", result.ProviderOptionsApplied, key)
		}
	}
}

func TestVolcengineImageEditMetadataCannotBypassIgnored(t *testing.T) {
	raw := []byte(`{"model":"doubao-seedream-5-0-flash-260915","prompt":"p","images":[{"image_url":"https://example.com/a.png"}],"metadata":{"stream":true,"partial_images":2,"mask":"x"}}`)
	result := mustBuildImageForPath(t, "volcengine", "/v1/images/edits", "application/json", raw)
	body := decodeImageTestBody(t, result.Body)
	for _, key := range []string{"stream", "partial_images", "mask"} {
		if _, ok := body[key]; ok {
			t.Fatalf("volcengine wire body must not carry metadata.%s: %s", key, result.Body)
		}
		if !imageTestContainsString(result.ParamsIgnored, key) {
			t.Fatalf("params ignored = %v, want %s", result.ParamsIgnored, key)
		}
		if imageTestContainsString(result.ProviderOptionsApplied, key) {
			t.Fatalf("provider options applied = %v, must not contain %s", result.ProviderOptionsApplied, key)
		}
	}
}

// L3（metadata 投影）不得注入图片或打破 xAI 的 image/images 互斥。
func TestXAIImageEditMetadataCannotInjectImages(t *testing.T) {
	raw := []byte(`{"model":"grok-imagine-image-2.0","prompt":"p","images":[{"image_url":"https://example.com/a.png"},{"image_url":"https://example.com/b.png"}],"metadata":{"image":{"url":"https://example.com/c.png"}}}`)
	result := mustBuildImageForPath(t, "xai", "/v1/images/edits", "application/json", raw)
	body := decodeImageTestBody(t, result.Body)
	if _, ok := body["image"]; ok {
		t.Fatalf("metadata.image must not break image/images exclusivity: %s", result.Body)
	}
	images, _ := body["images"].([]any)
	if len(images) != 2 || images[0].(map[string]any)["url"] != "https://example.com/a.png" {
		t.Fatalf("images = %#v", body["images"])
	}
	if imageTestContainsString(result.ProviderOptionsApplied, "image") {
		t.Fatalf("provider options applied = %v, must not contain image", result.ProviderOptionsApplied)
	}
}

// 未处理顶层参数必须回显 ignored，而不是静默丢弃。
func TestImageEditIgnoredTopLevelUnknownParams(t *testing.T) {
	t.Run("xai aspect_ratio", func(t *testing.T) {
		raw := []byte(`{"model":"grok-imagine-image-2.0","prompt":"p","images":[{"image_url":"https://example.com/a.png"}],"aspect_ratio":"1:1"}`)
		result := mustBuildImageForPath(t, "xai", "/v1/images/edits", "application/json", raw)
		body := decodeImageTestBody(t, result.Body)
		if _, ok := body["aspect_ratio"]; ok {
			t.Fatalf("xai wire body must not carry unhandled aspect_ratio: %s", result.Body)
		}
		if !imageTestContainsString(result.ParamsIgnored, "aspect_ratio") {
			t.Fatalf("params ignored = %v, want aspect_ratio", result.ParamsIgnored)
		}
	})

	t.Run("volcengine sequential_image_generation/tools", func(t *testing.T) {
		raw := []byte(`{"model":"doubao-seedream-5-0-flash-260915","prompt":"p","images":[{"image_url":"https://example.com/a.png"}],"sequential_image_generation":"auto","tools":[{"type":"web_search"}]}`)
		result := mustBuildImageForPath(t, "volcengine", "/v1/images/edits", "application/json", raw)
		body := decodeImageTestBody(t, result.Body)
		for _, key := range []string{"sequential_image_generation", "tools"} {
			if _, ok := body[key]; ok {
				t.Fatalf("volcengine wire body must not carry %s: %s", key, result.Body)
			}
			if !imageTestContainsString(result.ParamsIgnored, key) {
				t.Fatalf("params ignored = %v, want %s", result.ParamsIgnored, key)
			}
		}
	})

	t.Run("qwen seed", func(t *testing.T) {
		raw := []byte(`{"model":"qwen-image-3.0","prompt":"p","images":[{"image_url":"https://example.com/a.png"}],"seed":42}`)
		result := mustBuildImageForPath(t, "qwen", "/v1/images/edits", "application/json", raw)
		body := decodeImageTestBody(t, result.Body)
		parameters, _ := body["parameters"].(map[string]any)
		if _, ok := parameters["seed"]; ok {
			t.Fatalf("qwen parameters must not carry top-level seed: %s", result.Body)
		}
		if !imageTestContainsString(result.ParamsIgnored, "seed") {
			t.Fatalf("params ignored = %v, want seed", result.ParamsIgnored)
		}
	})
}

// multipart 不承载 provider_options：按未知字段忽略并回显，不报 400；
// gpt multipart 维持字节级透传不变。
func TestImageEditMultipartProviderOptionsIgnored(t *testing.T) {
	for _, provider := range []string{"xai", "volcengine", "qwen"} {
		rawBody, contentType := buildImageEditMultipart(t,
			[]imageEditTestField{
				{name: "model", value: "m"},
				{name: "prompt", value: "p"},
				{name: "provider_options", value: "not-json"},
			},
			[]imageEditTestFile{{field: "image", filename: "a.png", contentType: "image/png", data: imageTestPNG}})
		result := mustBuildImageForPath(t, provider, "/v1/images/edits", contentType, rawBody)
		if result.Body == nil {
			t.Fatalf("%s multipart edit must produce an upstream body", provider)
		}
		body := decodeImageTestBody(t, result.Body)
		if _, ok := body["provider_options"]; ok {
			t.Fatalf("%s provider_options must not be forwarded: %s", provider, result.Body)
		}
		if !imageTestContainsString(result.ParamsIgnored, "provider_options") {
			t.Fatalf("%s params ignored = %v, want provider_options", provider, result.ParamsIgnored)
		}
	}

	// gpt 不看 provider_options 文本字段：无 stream 时保持字节透传。
	gptBody, gptContentType := buildImageEditMultipart(t,
		[]imageEditTestField{
			{name: "model", value: "gpt-image-2"},
			{name: "prompt", value: "p"},
			{name: "provider_options", value: "not-json"},
		},
		[]imageEditTestFile{{field: "image", filename: "a.png", contentType: "image/png", data: imageTestPNG}})
	gptResult := mustBuildImageForPath(t, "gpt", "/v1/images/edits", gptContentType, gptBody)
	if gptResult.Body != nil {
		t.Fatalf("gpt multipart provider_options must stay byte passthrough: %s", gptResult.Body)
	}
}

// multipart 编辑不承载 metadata：非 gpt 厂商按未知文本字段忽略并回显
// params_ignored，不报 400；gpt 保持字节透传（未知字段由上游裁决）。
func TestImageEditMultipartMetadataIgnored(t *testing.T) {
	for _, provider := range []string{"xai", "volcengine", "qwen"} {
		rawBody, contentType := buildImageEditMultipart(t,
			[]imageEditTestField{
				{name: "model", value: "m"},
				{name: "prompt", value: "p"},
				{name: "metadata", value: `{"seed":42}`},
			},
			[]imageEditTestFile{{field: "image", filename: "a.png", contentType: "image/png", data: imageTestPNG}})
		result := mustBuildImageForPath(t, provider, "/v1/images/edits", contentType, rawBody)
		if result.Body == nil {
			t.Fatalf("%s multipart edit must produce an upstream body", provider)
		}
		body := decodeImageTestBody(t, result.Body)
		if _, ok := body["metadata"]; ok {
			t.Fatalf("%s metadata must be ignored: %s", provider, result.Body)
		}
		if !imageTestContainsString(result.ParamsIgnored, "metadata") {
			t.Fatalf("%s params ignored = %v, want metadata", provider, result.ParamsIgnored)
		}
		echo := ImageEchoForPath(provider, "/v1/images/edits", contentType, rawBody)
		if !imageTestContainsString(echo.ParamsIgnored, "metadata") {
			t.Fatalf("%s echo params ignored = %v, want metadata", provider, echo.ParamsIgnored)
		}
		if echo.Metadata != nil {
			t.Fatalf("%s multipart echo metadata = %#v, want empty", provider, echo.Metadata)
		}
	}

	// gpt 不看 metadata 文本字段：无 stream 时保持字节透传。
	gptBody, gptContentType := buildImageEditMultipart(t,
		[]imageEditTestField{
			{name: "model", value: "gpt-image-2"},
			{name: "prompt", value: "p"},
			{name: "metadata", value: `{"seed":42}`},
		},
		[]imageEditTestFile{{field: "image", filename: "a.png", contentType: "image/png", data: imageTestPNG}})
	gptResult := mustBuildImageForPath(t, "gpt", "/v1/images/edits", gptContentType, gptBody)
	if gptResult.Body != nil {
		t.Fatalf("gpt multipart metadata must stay byte passthrough: %s", gptResult.Body)
	}
}

// JSON 编辑与生成面一致：metadata 原值随 ImageEchoForPath 回显。
func TestImageEchoForPathJSONMetadataEcho(t *testing.T) {
	raw := []byte(`{"model":"qwen-image-3.0","prompt":"p","images":[{"image_url":"https://example.com/a.png"}],"metadata":{"negative_prompt":"blur"}}`)
	echo := ImageEchoForPath("qwen", "/v1/images/edits", "application/json", raw)
	if echo.Metadata["negative_prompt"] != "blur" {
		t.Fatalf("echo metadata = %#v, want negative_prompt", echo.Metadata)
	}
	if !imageTestContainsString(echo.ProviderOptionsApplied, "negative_prompt") {
		t.Fatalf("provider options applied = %v, want negative_prompt", echo.ProviderOptionsApplied)
	}
	generationRaw := []byte(`{"model":"gpt-image-2","prompt":"p","metadata":{"quality":"high"}}`)
	generationEcho := ImageEchoForPath("gpt", "/v1/images/generations", "application/json", generationRaw)
	if generationEcho.Metadata["quality"] != "high" {
		t.Fatalf("generation echo metadata = %#v, want quality", generationEcho.Metadata)
	}
	if !imageTestContainsString(generationEcho.ProviderOptionsApplied, "quality") {
		t.Fatalf("generation provider options applied = %v, want quality", generationEcho.ProviderOptionsApplied)
	}
}

// qwen 编辑不再从 metadata.image 兜底装配图片（至少一张客户端参考图是前
// 提），该键必须被剔除而不是漏进 parameters。
func TestQwenImageEditMetadataCannotInjectImage(t *testing.T) {
	raw := []byte(`{"model":"qwen-image-3.0","prompt":"p","images":[{"image_url":"https://example.com/a.png"}],"metadata":{"image":"https://example.com/b.png"}}`)
	result := mustBuildImageForPath(t, "qwen", "/v1/images/edits", "application/json", raw)
	body := decodeImageTestBody(t, result.Body)
	parameters, _ := body["parameters"].(map[string]any)
	for _, key := range []string{"image", "images"} {
		if _, ok := parameters[key]; ok {
			t.Fatalf("qwen parameters must not carry metadata.%s: %s", key, result.Body)
		}
	}
	input, _ := body["input"].(map[string]any)
	messages, _ := input["messages"].([]any)
	content, _ := messages[0].(map[string]any)["content"].([]any)
	if len(content) != 2 || content[0].(map[string]any)["image"] != "https://example.com/a.png" {
		t.Fatalf("qwen content = %#v", content)
	}
	if imageTestContainsString(result.ProviderOptionsApplied, "image") {
		t.Fatalf("provider options applied = %v, must not contain image", result.ProviderOptionsApplied)
	}
}

// 2026-11-12 复核 3：metadata.parameters 的嵌套图片键与 stream
// 同口径——不得进入 wire parameters，且必须回显 ignored。
func TestQwenImageEditMetadataNestedImageKeys(t *testing.T) {
	raw := []byte(`{"model":"qwen-image-3.0","prompt":"p","images":[{"image_url":"https://example.com/a.png"}],"metadata":{"parameters":{"image":"x","images":["y"],"stream":true}}}`)
	result := mustBuildImageForPath(t, "qwen", "/v1/images/edits", "application/json", raw)
	body := decodeImageTestBody(t, result.Body)
	parameters, _ := body["parameters"].(map[string]any)
	for _, key := range []string{"image", "images", "stream"} {
		if _, ok := parameters[key]; ok {
			t.Fatalf("qwen parameters must not carry nested %s: %s", key, result.Body)
		}
		if !imageTestContainsString(result.ParamsIgnored, key) {
			t.Fatalf("params ignored = %v, want %s", result.ParamsIgnored, key)
		}
		if imageTestContainsString(result.ProviderOptionsApplied, key) {
			t.Fatalf("provider options applied = %v, must not contain %s", result.ProviderOptionsApplied, key)
		}
	}
}

// 2026-11-12 复核 3：顶层 watermark 是 volcengine 编辑面保留参数——客户端
// true 原样透传，回显 params_applied 且不进 params_ignored；缺省仍默认 false。
func TestVolcengineImageEditTopLevelWatermarkPreserved(t *testing.T) {
	raw := []byte(`{"model":"doubao-seedream-5-0-flash-260915","prompt":"p","images":[{"image_url":"https://example.com/a.png"}],"watermark":true}`)
	result := mustBuildImageForPath(t, "volcengine", "/v1/images/edits", "application/json", raw)
	body := decodeImageTestBody(t, result.Body)
	if body["watermark"] != true {
		t.Fatalf("volcengine watermark = %v, want true: %s", body["watermark"], result.Body)
	}
	if !imageTestContainsString(result.ParamsApplied, "watermark") {
		t.Fatalf("params applied = %v, want watermark", result.ParamsApplied)
	}
	if imageTestContainsString(result.ParamsIgnored, "watermark") {
		t.Fatalf("params ignored = %v, must not contain watermark", result.ParamsIgnored)
	}
	// 缺省仍默认 false。
	defaultRaw := []byte(`{"model":"doubao-seedream-5-0-flash-260915","prompt":"p","images":[{"image_url":"https://example.com/a.png"}]}`)
	defaultBody := decodeImageTestBody(t, mustBuildImageForPath(t, "volcengine", "/v1/images/edits", "application/json", defaultRaw).Body)
	if defaultBody["watermark"] != false {
		t.Fatalf("volcengine default watermark = %v, want false", defaultBody["watermark"])
	}
}

// 2026-11-12 复核 3：metadata.<vendor>.image 被 L3 图片装配键剔除后
// 必须回显 ignored（不再静默），且不进入 provider_options_applied。
func TestImageEditMetadataImageKeysEchoIgnored(t *testing.T) {
	cases := map[string]string{
		"xai":        `{"model":"grok-imagine-image-2.0","prompt":"p","images":[{"image_url":"https://example.com/a.png"},{"image_url":"https://example.com/b.png"}],"metadata":{"image":{"url":"https://example.com/c.png"}}}`,
		"volcengine": `{"model":"doubao-seedream-5-0-flash-260915","prompt":"p","images":[{"image_url":"https://example.com/a.png"}],"metadata":{"image":"https://example.com/c.png"}}`,
		"qwen":       `{"model":"qwen-image-3.0","prompt":"p","images":[{"image_url":"https://example.com/a.png"}],"metadata":{"image":"https://example.com/c.png"}}`,
	}
	for provider, raw := range cases {
		result := mustBuildImageForPath(t, provider, "/v1/images/edits", "application/json", []byte(raw))
		if !imageTestContainsString(result.ParamsIgnored, "image") {
			t.Fatalf("%s params ignored = %v, want image", provider, result.ParamsIgnored)
		}
		if imageTestContainsString(result.ProviderOptionsApplied, "image") {
			t.Fatalf("%s provider options applied = %v, must not contain image", provider, result.ProviderOptionsApplied)
		}
	}
}

// 2026-11-12 复核 3：空键/纯空白键不参与 ignored 回显（避免逗号拼接前导
// 分隔符），正常未处理键仍回显。
func TestImageEditIgnoredTopLevelSkipsEmptyKeys(t *testing.T) {
	request := ImageRequest{
		Body: map[string]any{
			"model":   "m",
			"prompt":  "p",
			"":        "empty",
			"   ":     "blank",
			"quality": "high",
		},
		ProviderCode: "xai",
	}
	ignored := imageEditIgnoredTopLevel(request, xaiImageEditPreservedParams, xaiImageEditIgnoredParams, map[string]any{})
	for _, key := range ignored {
		if strings.TrimSpace(key) == "" {
			t.Fatalf("ignored = %v, must not carry empty key", ignored)
		}
	}
	if !imageTestContainsString(ignored, "quality") {
		t.Fatalf("ignored = %v, want quality", ignored)
	}
}
