package openaicompat

// imagegeneration.go 剩余分支的补充覆盖：partial_images 夹取、流式字节上限、
// 非 SSE 的 JSON 回退迭代器、流尾帧处理与 provider 错误形态。

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func covImageExecutor(handler func(request *http.Request) (*http.Response, error)) *ImageGenerationExecutor {
	return NewImageGenerationExecutor(Config{Port: 3000}, "Bearer x", &mockTransport{handler: handler}, nil)
}

func TestCovImageGenerationRequestBodyClamp(t *testing.T) {
	negative := -2.0
	body := ImageGenerationProviderRequestBody(
		ImageGenerationProviderRuntime{Model: "m"}, "p",
		ImageGenerationToolConfig{PartialImages: &negative}, true,
	)
	if got := body["partial_images"]; got != 0 {
		t.Errorf("负 partial_images 应夹到 0：%v", got)
	}
	zero := 0.0
	body = ImageGenerationProviderRequestBody(
		ImageGenerationProviderRuntime{Model: "m"}, "p",
		ImageGenerationToolConfig{PartialImages: &zero}, false,
	)
	if _, exists := body["partial_images"]; exists {
		t.Errorf("非 stream 不应产出 partial_images：%v", body)
	}
	if _, exists := body["size"]; exists {
		t.Errorf("空可选字段不应输出：%v", body)
	}
}

func TestCovImageGenerationGenerateStreamBranches(t *testing.T) {
	t.Run("非 2xx 错误响应", func(t *testing.T) {
		executor := covImageExecutor(func(request *http.Request) (*http.Response, error) {
			return jsonResponse(500, `{"error":{"message":"上游挂了","code":"boom"}}`), nil
		})
		iterator, err := executor.GenerateStream(context.Background(), ImageGenerationInput{
			Prompt: "p", Tool: ImageGenerationToolConfig{},
		})
		if err == nil {
			t.Fatal("非 2xx 应报错")
		}
		// 5xx 统一折算 502 上游错误（4xx 折算 400，Node 同语义）。
		if providerErr, ok := err.(*ImageGenerationProviderError); !ok || providerErr.StatusCode != 502 || providerErr.Code != "boom" {
			t.Fatalf("err = %+v", err)
		}
		if iterator != nil {
			t.Error("失败时不应返回迭代器")
		}
	})
	t.Run("JSON 响应的伪流迭代", func(t *testing.T) {
		executor := covImageExecutor(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(
					`{"data":[{"b64_json":"QUJD","url":"https://x/a.png"}],"usage":{"total_tokens":9,"input_tokens":4,"output_tokens":5}}`)),
			}, nil
		})
		iterator, err := executor.GenerateStream(context.Background(), ImageGenerationInput{
			Prompt: "p", Tool: ImageGenerationToolConfig{OutputFormat: "png"},
		})
		if err != nil {
			t.Fatalf("流构造失败：%v", err)
		}
		event, err := iterator()
		if err != nil || event.Type != "completed" {
			t.Fatalf("首事件应 completed：%+v（%v）", event, err)
		}
		if event.Result == nil || event.Result.ImageBase64 != "QUJD" {
			t.Fatalf("result = %+v", event.Result)
		}
		if _, err := iterator(); err != io.EOF {
			t.Fatalf("第二次应 EOF：%v", err)
		}
	})
	t.Run("JSON 响应解析失败包装", func(t *testing.T) {
		executor := covImageExecutor(func(request *http.Request) (*http.Response, error) {
			return jsonResponse(200, `{"unexpected":true}`), nil
		})
		_, err := executor.GenerateStream(context.Background(), ImageGenerationInput{
			Prompt: "p", Tool: ImageGenerationToolConfig{},
		})
		if err == nil || !strings.Contains(err.Error(), "data[0].b64_json") {
			t.Fatalf("缺 data 图片应报 provider 错误：%v", err)
		}
	})
	t.Run("流式字节上限", func(t *testing.T) {
		executor := covImageExecutor(func(request *http.Request) (*http.Response, error) {
			big := strings.Repeat("x", 64)
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader("data: " + big + "\n\ndata: " + big + "\n\n")),
			}, nil
		})
		executor.Provider.MaxBodyBytes = 64
		iterator, err := executor.GenerateStream(context.Background(), ImageGenerationInput{
			Prompt: "p", Tool: ImageGenerationToolConfig{},
		})
		if err != nil {
			t.Fatalf("流构造失败：%v", err)
		}
		_, err = iterator()
		if err == nil || !strings.Contains(err.Error(), "读取上限") {
			t.Fatalf("应报响应体过大：%v", err)
		}
	})
	t.Run("流尾帧补完成校验", func(t *testing.T) {
		// 尾帧是 partial：先产出 partial，随后因缺少 completed 报错。
		executor := covImageExecutor(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body: io.NopCloser(strings.NewReader(
					"event: image_generation.partial_image\ndata: " + `{"b64_json":"QUJD"}` + "\n")),
			}, nil
		})
		iterator, err := executor.GenerateStream(context.Background(), ImageGenerationInput{
			Prompt: "p", Tool: ImageGenerationToolConfig{},
		})
		if err != nil {
			t.Fatalf("流构造失败：%v", err)
		}
		event, err := iterator()
		if err != nil || event.Type != "partial_image" {
			t.Fatalf("尾帧 partial 应产出：%+v（%v）", event, err)
		}
		_, err = iterator()
		if err == nil || !strings.Contains(err.Error(), "最终图片结果") {
			t.Fatalf("缺 completed 应报错：%v", err)
		}
	})
	t.Run("流尾 completed 帧", func(t *testing.T) {
		executor := covImageExecutor(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body: io.NopCloser(strings.NewReader(
					"data: " + `{"type":"image_generation.completed","data":[{"b64_json":"QUJD"}]}` + "\n")),
			}, nil
		})
		iterator, err := executor.GenerateStream(context.Background(), ImageGenerationInput{
			Prompt: "p", Tool: ImageGenerationToolConfig{},
		})
		if err != nil {
			t.Fatalf("流构造失败：%v", err)
		}
		event, err := iterator()
		if err != nil || event.Type != "completed" {
			t.Fatalf("尾帧 completed 应产出：%+v（%v）", event, err)
		}
		if _, err := iterator(); err != io.EOF {
			t.Fatalf("completed 后应 EOF：%v", err)
		}
	})
	t.Run("上下文取消", func(t *testing.T) {
		executor := covImageExecutor(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		iterator, err := executor.GenerateStream(ctx, ImageGenerationInput{
			Prompt: "p", Tool: ImageGenerationToolConfig{},
		})
		if err != nil {
			t.Fatalf("流构造失败：%v", err)
		}
		_, err = iterator()
		if err == nil {
			t.Fatal("取消后应报错")
		}
	})
}

func TestCovImageGenerationStreamEventFrames(t *testing.T) {
	t.Run("partial 缺 b64 报错", func(t *testing.T) {
		_, err := imageGenerationProviderStreamEventFromSSEFrame(
			"data: "+`{"type":"image_generation.partial_image"}`, "png")
		if err == nil || !strings.Contains(err.Error(), "b64_json") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("error 事件带 response 包裹", func(t *testing.T) {
		_, err := imageGenerationProviderStreamEventFromSSEFrame(
			"data: "+`{"type":"response.failed","response":{"error":{"message":"失败","code":"bad"}}}`, "png")
		providerErr, ok := err.(*ImageGenerationProviderError)
		if !ok || providerErr.Code != "bad" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("error 对象不带 type", func(t *testing.T) {
		_, err := imageGenerationProviderStreamEventFromSSEFrame(
			"data: "+`{"error":{"message":"直接错误"}}`, "png")
		if err == nil {
			t.Fatal("裸 error 对象应报错")
		}
	})
	t.Run("空帧与 DONE 忽略", func(t *testing.T) {
		event, err := imageGenerationProviderStreamEventFromSSEFrame("", "png")
		if err != nil || event != nil {
			t.Fatalf("空帧 = %+v（%v）", event, err)
		}
		event, err = imageGenerationProviderStreamEventFromSSEFrame("data: [DONE]", "png")
		if err != nil || event != nil {
			t.Fatalf("DONE = %+v（%v）", event, err)
		}
	})
	t.Run("takeNextSSEFrame 分隔符选择", func(t *testing.T) {
		frame, rest, found := takeNextSSEFrame("a\r\n\r\nb\n\nc")
		if !found || frame != "a" || rest != "b\n\nc" {
			t.Errorf("CRLF 优先 = %q/%q/%v", frame, rest, found)
		}
		if _, _, found := takeNextSSEFrame("无边界"); found {
			t.Error("无边界应 not found")
		}
	})
}
