package playground

// 媒体三执行器测试（设计 §3.4/§3.5/§3.6）：Mock 派发执行器回放固定上游响应，
// 断言请求构造（generations/edits 分路、multipart 形状、videos 创建体、speech
// 体）、响应归一（暂存产物条目形状、jobId、状态归一）与候选校验错误映射。
// URL 回退下载经自定义 RoundTripper 回放，不触网。

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestImagesGenerationsRequestConstruction 文生图：断言 POST
// /v1/images/generations 请求体（model/prompt/n/size/response_format）与暂存
// 产物条目形状（id/url，不再内联 base64 dataUrl）。
func TestImagesGenerationsRequestConstruction(t *testing.T) {
	deps := playgroundFixtureDeps(t, false)
	// 补一个 image 候选（gpt 家族 × gpt-image-2，api_key 账户可路由）。
	catalog := deps.ProviderCatalog.(*staticCatalog)
	catalog.byProvider["gpt"] = append(catalog.byProvider["gpt"], CatalogItem{Model: "gpt-image-2", SupportedAPIProtocols: []string{"image_generation"}})
	transportView := deps.TransportAccounts.(*staticTransport)
	transportView.views["acc_1"][0].SupportedEndpointModes = append(transportView.views["acc_1"][0].SupportedEndpointModes, "images_json")
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{
		Body: []byte(`{"data":[{"b64_json":"` + base64Of(pngOnePixel) + `"}]}`),
	}}
	mux, _ := newTestRoutes(deps)
	payload, _ := json.Marshal(map[string]any{
		"accountId": "acc_1", "modelId": "gpt-image-2", "prompt": "一只赛博猫", "size": "1024x1024",
	})
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/images", payload)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}
	if len(dispatch.calls) != 1 {
		t.Fatalf("派发次数 = %d", len(dispatch.calls))
	}
	call := dispatch.calls[0]
	if call.Request.Path != "/v1/images/generations" || call.Request.Method != "POST" {
		t.Fatalf("派发目标 = %s %s", call.Request.Method, call.Request.Path)
	}
	if call.Request.Headers["x-juhe-ai-purpose"] != "playground_images" {
		t.Fatalf("purpose 头 = %q", call.Request.Headers["x-juhe-ai-purpose"])
	}
	var outbound struct {
		Model          string `json:"model"`
		Prompt         string `json:"prompt"`
		Size           string `json:"size"`
		ResponseFormat string `json:"response_format"`
	}
	if err := json.Unmarshal(call.Request.Body, &outbound); err != nil {
		t.Fatalf("请求体解析失败: %v", err)
	}
	if outbound.Model != "gpt-image-2" || outbound.Prompt != "一只赛博猫" || outbound.Size != "1024x1024" {
		t.Fatalf("出站体不符: %+v", outbound)
	}
	if outbound.ResponseFormat != "b64_json" {
		t.Fatalf("response_format = %q，期望 b64_json", outbound.ResponseFormat)
	}
	var response struct {
		Data struct {
			Images []struct {
				ID  string `json:"id"`
				URL string `json:"url"`
			} `json:"images"`
			Model string `json:"model"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	if len(response.Data.Images) != 1 || response.Data.Model != "gpt-image-2" {
		t.Fatalf("响应形状不符: %+v", response.Data)
	}
	for _, image := range response.Data.Images {
		if image.ID == "" || image.URL != "/playground/artifacts/"+image.ID+"/content" {
			t.Fatalf("产物条目形状不符: %+v", image)
		}
	}
	if strings.Contains(recorder.Body.String(), "dataUrl") || strings.Contains(recorder.Body.String(), base64Of(pngOnePixel)) {
		t.Fatalf("响应不得再内联 base64 dataUrl: %s", recorder.Body.String())
	}
}

// TestImagesEditsRequestConstruction 图生图：multipart POST /v1/images/edits，
// 断言表单字段、参考图 part 的真实 MIME 与文件名。
func TestImagesEditsRequestConstruction(t *testing.T) {
	deps := playgroundFixtureDeps(t, false)
	catalog := deps.ProviderCatalog.(*staticCatalog)
	catalog.byProvider["gpt"] = append(catalog.byProvider["gpt"], CatalogItem{Model: "gpt-image-2", SupportedAPIProtocols: []string{"image_generation"}})
	transportView := deps.TransportAccounts.(*staticTransport)
	transportView.views["acc_1"][0].SupportedEndpointModes = append(transportView.views["acc_1"][0].SupportedEndpointModes, "images_json")
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{
		Body: []byte(`{"data":[{"b64_json":"` + base64Of(pngOnePixel) + `"}]}`),
	}}
	mux, _ := newTestRoutes(deps)
	payload, _ := json.Marshal(map[string]any{
		"accountId": "acc_1", "modelId": "gpt-image-2", "prompt": "改成水彩风",
		"inputImageBase64": base64Of(pngOnePixel),
	})
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/images", payload)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}
	call := dispatch.calls[0]
	if call.Request.Path != "/v1/images/edits" {
		t.Fatalf("派发目标 = %s", call.Request.Path)
	}
	mediaType, params, err := mime.ParseMediaType(call.Request.Headers["content-type"])
	if err != nil || mediaType != "multipart/form-data" {
		t.Fatalf("content-type = %q（err=%v）", call.Request.Headers["content-type"], err)
	}
	reader := multipart.NewReader(bytes.NewReader(call.Request.Body), params["boundary"])
	var sawModel, sawPrompt, sawImage, sawB64Format bool
	for {
		part, partErr := reader.NextPart()
		if partErr != nil {
			break
		}
		data, _ := io.ReadAll(part)
		switch part.FormName() {
		case "model":
			sawModel = string(data) == "gpt-image-2"
		case "prompt":
			sawPrompt = string(data) == "改成水彩风"
		case "response_format":
			sawB64Format = string(data) == "b64_json"
		case "image":
			sawImage = string(data) == string(pngOnePixel) &&
				part.Header.Get("Content-Type") == "image/png" &&
				strings.Contains(part.Header.Get("Content-Disposition"), "reference.png")
		}
	}
	if !sawModel || !sawPrompt || !sawImage || !sawB64Format {
		t.Fatalf("multipart 形状不符: model=%v prompt=%v image=%v b64=%v", sawModel, sawPrompt, sawImage, sawB64Format)
	}
}

// TestImagesURLFallbackDownloads grok 只回临时 URL：经守卫客户端下载字节并转
// dataUrl（本测试换装 RoundTripper 回放字节，不触网）。
func TestImagesURLFallbackDownloads(t *testing.T) {
	deps := playgroundFixtureDeps(t, false)
	catalog := deps.ProviderCatalog.(*staticCatalog)
	catalog.byProvider["gpt"] = append(catalog.byProvider["gpt"], CatalogItem{Model: "gpt-image-2", SupportedAPIProtocols: []string{"image_generation"}})
	transportView := deps.TransportAccounts.(*staticTransport)
	transportView.views["acc_1"][0].SupportedEndpointModes = append(transportView.views["acc_1"][0].SupportedEndpointModes, "images_json")
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{
		Body: []byte(`{"data":[{"url":"https://93.184.216.34/img.png"}]}`),
	}}
	previousClient := imageUrlDownloadClient
	t.Cleanup(func() { imageUrlDownloadClient = previousClient })
	imageUrlDownloadClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "93.184.216.34" {
			return nil, errors.New("测试仅回放 93.184.216.34")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"image/png"}},
			Body:       io.NopCloser(bytes.NewReader(pngOnePixel)),
		}, nil
	})}
	mux, _ := newTestRoutes(deps)
	payload, _ := json.Marshal(map[string]any{
		"accountId": "acc_1", "modelId": "gpt-image-2", "prompt": "一只赛博猫",
	})
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/images", payload)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data struct {
			Images []struct {
				ID  string `json:"id"`
				URL string `json:"url"`
			} `json:"images"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	if len(response.Data.Images) != 1 || response.Data.Images[0].URL != "/playground/artifacts/"+response.Data.Images[0].ID+"/content" {
		t.Fatalf("URL 回退产物形状不符: %s", recorder.Body.String())
	}
}

// TestImagesSSRFRejectsPrivateURL URL 回退指向内网地址：守卫拒绝（响应 502
// 且 message 携带内网拒绝语义），不发真实请求。
func TestImagesSSRFRejectsPrivateURL(t *testing.T) {
	deps := playgroundFixtureDeps(t, false)
	catalog := deps.ProviderCatalog.(*staticCatalog)
	catalog.byProvider["gpt"] = append(catalog.byProvider["gpt"], CatalogItem{Model: "gpt-image-2", SupportedAPIProtocols: []string{"image_generation"}})
	transportView := deps.TransportAccounts.(*staticTransport)
	transportView.views["acc_1"][0].SupportedEndpointModes = append(transportView.views["acc_1"][0].SupportedEndpointModes, "images_json")
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{
		Body: []byte(`{"data":[{"url":"http://127.0.0.1:8080/secret.png"}]}`),
	}}
	mux, _ := newTestRoutes(deps)
	payload, _ := json.Marshal(map[string]any{
		"accountId": "acc_1", "modelId": "gpt-image-2", "prompt": "内网图",
	})
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/images", payload)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("状态码 = %d，期望 502", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "内网") {
		t.Fatalf("错误面未透出 SSRF 拒绝语义: %s", recorder.Body.String())
	}
}

// TestImagesValidation 生图校验面：n 越界 400、模型不在候选 400。
func TestImagesValidation(t *testing.T) {
	deps := playgroundFixtureDeps(t, false)
	catalog := deps.ProviderCatalog.(*staticCatalog)
	catalog.byProvider["gpt"] = append(catalog.byProvider["gpt"], CatalogItem{Model: "gpt-image-2", SupportedAPIProtocols: []string{"image_generation"}})
	transportView := deps.TransportAccounts.(*staticTransport)
	transportView.views["acc_1"][0].SupportedEndpointModes = append(transportView.views["acc_1"][0].SupportedEndpointModes, "images_json")
	mux, _ := newTestRoutes(deps)
	noModel, _ := json.Marshal(map[string]any{"accountId": "acc_1", "modelId": "nope-image", "prompt": "x"})
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/images", noModel)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "playground_model_not_available") {
		t.Fatalf("模型不在候选应 400 playground_model_not_available: %d %s", recorder.Code, recorder.Body.String())
	}
}

// TestVideosCreateAndPoll 视频创建与轮询：创建体（model/prompt/size/seconds/
// input_reference data URL）、响应 {jobId}；轮询 in_progress→running 归一、
// error 透出、purpose 头与 GET 方法。
func TestVideosCreateAndPoll(t *testing.T) {
	deps := playgroundFixtureDeps(t, false)
	catalog := deps.ProviderCatalog.(*staticCatalog)
	catalog.byProvider["gpt"] = append(catalog.byProvider["gpt"], CatalogItem{Model: "grok-imagine-video-1.5", SupportedAPIProtocols: []string{"video"}})
	transportView := deps.TransportAccounts.(*staticTransport)
	transportView.views["acc_1"][0].SupportedEndpointModes = []string{"chat_sse", "video_create", "audio_speech"}
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{
		{Body: []byte(`{"id":"job_v1","status":"queued","progress":0}`)},
		{Body: []byte(`{"id":"job_v1","status":"in_progress","progress":40}`)},
	}
	mux, _ := newTestRoutes(deps)
	payload, _ := json.Marshal(map[string]any{
		"accountId": "acc_1", "modelId": "grok-imagine-video-1.5", "prompt": "一只猫在跳舞",
		"size": "1280x720", "seconds": 5.0, "inputImageBase64": base64Of(pngOnePixel),
	})
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/videos", payload)
	if recorder.Code != http.StatusOK {
		t.Fatalf("创建状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}
	createCall := dispatch.calls[0]
	if createCall.Request.Path != "/v1/videos" || createCall.Request.Method != "POST" {
		t.Fatalf("创建派发目标 = %s %s", createCall.Request.Method, createCall.Request.Path)
	}
	if createCall.Request.Headers["x-juhe-ai-purpose"] != "playground_videos" {
		t.Fatalf("purpose 头 = %q", createCall.Request.Headers["x-juhe-ai-purpose"])
	}
	var createOutbound struct {
		Model          string   `json:"model"`
		Prompt         string   `json:"prompt"`
		Size           string   `json:"size"`
		Seconds        *float64 `json:"seconds"`
		InputReference string   `json:"input_reference"`
	}
	if err := json.Unmarshal(createCall.Request.Body, &createOutbound); err != nil {
		t.Fatalf("创建请求体解析失败: %v", err)
	}
	if createOutbound.Model != "grok-imagine-video-1.5" || createOutbound.Prompt != "一只猫在跳舞" || createOutbound.Size != "1280x720" {
		t.Fatalf("创建体不符: %+v", createOutbound)
	}
	if createOutbound.Seconds == nil || *createOutbound.Seconds != 5 {
		t.Fatalf("seconds 透传不符: %v", createOutbound.Seconds)
	}
	if !strings.HasPrefix(createOutbound.InputReference, "data:image/png;base64,") {
		t.Fatalf("首帧 input_reference 应为 data URL: %q", createOutbound.InputReference)
	}
	var created struct {
		Data struct {
			JobID string `json:"jobId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil || created.Data.JobID != "job_v1" {
		t.Fatalf("创建响应 = %s（err %v）", recorder.Body.String(), err)
	}
	// 轮询。
	recorder = doJSON(mux, "GET", "/__aisys__/api/playground/videos/job_v1?accountId=acc_1", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("轮询状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}
	pollCall := dispatch.calls[1]
	if pollCall.Request.Method != "GET" || pollCall.Request.Path != "/v1/videos/job_v1" {
		t.Fatalf("轮询派发 = %s %s", pollCall.Request.Method, pollCall.Request.Path)
	}
	var pollPayload struct {
		Data struct {
			Status   string `json:"status"`
			Progress *int64 `json:"progress"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &pollPayload); err != nil {
		t.Fatalf("轮询响应解析失败: %v", err)
	}
	if pollPayload.Data.Status != "running" || pollPayload.Data.Progress == nil || *pollPayload.Data.Progress != 40 {
		t.Fatalf("轮询归一不符: %+v", pollPayload.Data)
	}
}

// TestVideosPollErrorPropagation 轮询上游失败：502 + 上游 message 摘要。
func TestVideosPollErrorPropagation(t *testing.T) {
	deps := playgroundFixtureDeps(t, false)
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{
		Status: http.StatusNotFound,
		Body:   []byte(`{"error":{"message":"任务不存在"}}`),
	}}
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "GET", "/__aisys__/api/playground/videos/job_x?accountId=acc_1", nil)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("状态码 = %d，期望 502", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "playground_dispatch_failed") || !strings.Contains(recorder.Body.String(), "任务不存在") {
		t.Fatalf("错误面缺少归一语义: %s", recorder.Body.String())
	}
}

// TestVideosContentStreamsPassthrough 产物下载：Content-Type 透传 + inline +
// 字节回放。
func TestVideosContentStreamsPassthrough(t *testing.T) {
	deps := playgroundFixtureDeps(t, false)
	dispatch := deps.Dispatch.(*mockDispatch)
	videoBytes := append([]byte{0x1A, 0x45, 0xDF, 0xA3}, bytes.Repeat([]byte{0x42}, 64)...)
	dispatch.script = []scriptedResponse{{
		Header: http.Header{"Content-Type": []string{"video/mp4"}},
		Body:   videoBytes,
	}}
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "GET", "/__aisys__/api/playground/videos/job_c/content?accountId=acc_1", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != "video/mp4" {
		t.Fatalf("Content-Type = %q，期望透传 video/mp4", got)
	}
	if got := recorder.Header().Get("Content-Disposition"); got != "inline" {
		t.Fatalf("Content-Disposition = %q", got)
	}
	if !bytes.Equal(recorder.Body.Bytes(), videoBytes) {
		t.Fatalf("产物字节不符（len %d）", recorder.Body.Len())
	}
	if dispatch.calls[0].Request.Path != "/v1/videos/job_c/content" {
		t.Fatalf("派发路径 = %q", dispatch.calls[0].Request.Path)
	}
}

// TestSpeechSynthesis 语音合成：出站体（model/input/voice/response_format/
// speed）、音频落暂存后的 JSON 响应形状（{id,url,mimeType}，不再直接回字节
// 流）、暂存文件字节一致、purpose 头。
func TestSpeechSynthesis(t *testing.T) {
	deps := playgroundFixtureDeps(t, false)
	catalog := deps.ProviderCatalog.(*staticCatalog)
	catalog.byProvider["gpt"] = append(catalog.byProvider["gpt"], CatalogItem{Model: "speech-2.8-hd", SupportedAPIProtocols: []string{"audio_speech"}})
	transportView := deps.TransportAccounts.(*staticTransport)
	transportView.views["acc_1"][0].SupportedEndpointModes = []string{"chat_sse", "video_create", "audio_speech"}
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{
		Header: http.Header{"Content-Type": []string{"audio/wav"}},
		Body:   wavBytes,
	}}
	mux, _ := newTestRoutes(deps)
	payload, _ := json.Marshal(map[string]any{
		"accountId": "acc_1", "modelId": "speech-2.8-hd", "input": "演练场语音合成", "voice": "tongtong", "speed": 1.1, "format": "wav",
	})
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/speech", payload)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data struct {
			ID       string `json:"id"`
			URL      string `json:"url"`
			MimeType string `json:"mimeType"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	if response.Data.ID == "" || response.Data.URL != "/playground/artifacts/"+response.Data.ID+"/content" || response.Data.MimeType != "audio/wav" {
		t.Fatalf("响应形状不符: %+v", response.Data)
	}
	if bytes.Contains(recorder.Body.Bytes(), wavBytes) {
		t.Fatalf("响应不得再直接回音频字节流: %s", recorder.Body.String())
	}
	// 暂存文件字节与上游音频一致（真实落盘验证）。
	data, err := os.ReadFile(filepath.Join(deps.ArtifactsRoot, "sys_user_1", response.Data.ID+".wav"))
	if err != nil || !bytes.Equal(data, wavBytes) {
		t.Fatalf("暂存文件读取失败或字节不符（err %v）", err)
	}
	call := dispatch.calls[0]
	if call.Request.Path != "/v1/audio/speech" || call.Request.Headers["x-juhe-ai-purpose"] != "playground_speech" {
		t.Fatalf("派发形状不符: %s %v", call.Request.Path, call.Request.Headers)
	}
	var outbound struct {
		Model          string  `json:"model"`
		Input          string  `json:"input"`
		Voice          string  `json:"voice"`
		ResponseFormat string  `json:"response_format"`
		Speed          float64 `json:"speed"`
	}
	if err := json.Unmarshal(call.Request.Body, &outbound); err != nil {
		t.Fatalf("请求体解析失败: %v", err)
	}
	if outbound.Model != "speech-2.8-hd" || outbound.Input != "演练场语音合成" || outbound.Voice != "tongtong" || outbound.ResponseFormat != "wav" || outbound.Speed != 1.1 {
		t.Fatalf("出站体不符: %+v", outbound)
	}
}

// TestSpeechValidation 语音校验面：input 超 2000 字 400；模型不在 audio 候选
// 400（账户无 audio 模型/端点模式时 400，契约 §3.6）。
func TestSpeechValidation(t *testing.T) {
	deps := playgroundFixtureDeps(t, false)
	catalog := deps.ProviderCatalog.(*staticCatalog)
	catalog.byProvider["gpt"] = append(catalog.byProvider["gpt"], CatalogItem{Model: "speech-2.8-hd", SupportedAPIProtocols: []string{"audio_speech"}})
	transportView := deps.TransportAccounts.(*staticTransport)
	transportView.views["acc_1"][0].SupportedEndpointModes = []string{"chat_sse", "audio_speech"}
	transportView.views["acc_1"][0].SupportedModels = []string{"speech-2.8-hd"}
	mux, _ := newTestRoutes(deps)
	longInput := strings.Repeat("字", 2001)
	payload, _ := json.Marshal(map[string]any{"accountId": "acc_1", "modelId": "speech-2.8-hd", "input": longInput})
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/speech", payload)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "2000") {
		t.Fatalf("超长 input 应 400: %d %s", recorder.Code, recorder.Body.String())
	}
	payload, _ = json.Marshal(map[string]any{"accountId": "acc_1", "modelId": "nope-speech", "input": "正常文本"})
	recorder = doJSON(mux, "POST", "/__aisys__/api/playground/speech", payload)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "playground_model_not_available") {
		t.Fatalf("无 audio 候选应 400 playground_model_not_available: %d %s", recorder.Code, recorder.Body.String())
	}
}

// roundTripFunc 是 RoundTripper 的函数适配（URL 回退下载回放用）。
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

// mustJSON 是测试载荷编码辅助（表驱动用例构造请求体）。
func mustJSON(value any) []byte {
	payload, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return payload
}

// TestVideosPollUnknownStatusKeepsRawText 未知任务状态：按 failed 收敛且原文
// 透出到 error.message（「未知状态 <原文>」，不静默丢弃）。
func TestVideosPollUnknownStatusKeepsRawText(t *testing.T) {
	deps := playgroundFixtureDeps(t, false)
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{Body: []byte(`{"id":"job_u","status":"weird_state","progress":10}`)}}
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "GET", "/__aisys__/api/playground/videos/job_u?accountId=acc_1", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Data struct {
			Status   string `json:"status"`
			Progress *int64 `json:"progress"`
			Error    *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	if payload.Data.Status != "failed" {
		t.Fatalf("未知状态应收敛为 failed: %+v", payload.Data)
	}
	if payload.Data.Error == nil || !strings.Contains(payload.Data.Error.Message, "未知状态 weird_state") {
		t.Fatalf("未知状态原文必须透出到 error.message: %+v", payload.Data.Error)
	}
}

// TestVideosPollKnownStatusNoSyntheticError 词表内状态不得合成 error 载荷
// （未知状态分支仅对词表外生效）。
func TestVideosPollKnownStatusNoSyntheticError(t *testing.T) {
	deps := playgroundFixtureDeps(t, false)
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{Body: []byte(`{"id":"job_k","status":"expired","progress":100}`)}}
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "GET", "/__aisys__/api/playground/videos/job_k?accountId=acc_1", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Data struct {
			Status string `json:"status"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	if payload.Data.Status != "failed" || payload.Data.Error != nil {
		t.Fatalf("expired 归一 failed 且不应合成 error: %+v", payload.Data)
	}
}

// TestMediaEndpointsAccountNotFoundMaps404 媒体三端点账户不存在/范围外：与
// chatstream 对齐 404 playground_account_not_found（原 400 model_not_available
// 误标语义），且不进入派发路径。
func TestMediaEndpointsAccountNotFoundMaps404(t *testing.T) {
	deps := playgroundFixtureDeps(t, false)
	catalog := deps.ProviderCatalog.(*staticCatalog)
	catalog.byProvider["gpt"] = append(catalog.byProvider["gpt"],
		CatalogItem{Model: "gpt-image-2", SupportedAPIProtocols: []string{"image_generation"}},
		CatalogItem{Model: "grok-imagine-video-1.5", SupportedAPIProtocols: []string{"video"}},
		CatalogItem{Model: "speech-2.8-hd", SupportedAPIProtocols: []string{"audio_speech"}},
	)
	transportView := deps.TransportAccounts.(*staticTransport)
	transportView.views["acc_1"][0].SupportedEndpointModes = []string{"chat_sse", "video_create", "audio_speech"}
	transportView.views["acc_1"][0].SupportedModels = []string{"gpt-image-2", "grok-imagine-video-1.5", "speech-2.8-hd"}
	mux, depsOut := newTestRoutes(deps)

	cases := []struct {
		name   string
		target string
		body   []byte
	}{
		{"images", "/__aisys__/api/playground/images", mustJSON(map[string]any{"accountId": "acc_missing", "modelId": "gpt-image-2", "prompt": "一只猫"})},
		{"videos", "/__aisys__/api/playground/videos", mustJSON(map[string]any{"accountId": "acc_missing", "modelId": "grok-imagine-video-1.5", "prompt": "一只猫"})},
		{"speech", "/__aisys__/api/playground/speech", mustJSON(map[string]any{"accountId": "acc_missing", "modelId": "speech-2.8-hd", "input": "文本"})},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := doJSON(mux, "POST", testCase.target, testCase.body)
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("%s 状态码 = %d，期望 404（body %s）", testCase.name, recorder.Code, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), "playground_account_not_found") {
				t.Fatalf("%s 错误码不符: %s", testCase.name, recorder.Body.String())
			}
		})
	}
	dispatch := depsOut.Dispatch.(*mockDispatch)
	if len(dispatch.calls) != 0 {
		t.Fatalf("账户不存在时不应派发（%d 次）", len(dispatch.calls))
	}
}

// TestVideosJobIDWhitelist jobId 白名单 ^[A-Za-z0-9._-]{1,512}$：非法字符在
// 轮询与内容两端点都以 400 invalid_request 收敛，不透传派发。
func TestVideosJobIDWhitelist(t *testing.T) {
	deps := playgroundFixtureDeps(t, false)
	mux, depsOut := newTestRoutes(deps)
	recorder := doJSON(mux, "GET", "/__aisys__/api/playground/videos/job!x?accountId=acc_1", nil)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "playground_invalid_request") {
		t.Fatalf("轮询非法 jobId 应 400 invalid_request: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = doJSON(mux, "GET", "/__aisys__/api/playground/videos/job!x/content?accountId=acc_1", nil)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "playground_invalid_request") {
		t.Fatalf("内容端点非法 jobId 应 400 invalid_request: %d %s", recorder.Code, recorder.Body.String())
	}
	if len(depsOut.Dispatch.(*mockDispatch).calls) != 0 {
		t.Fatalf("非法 jobId 不应进入派发路径（%d 次）", len(depsOut.Dispatch.(*mockDispatch).calls))
	}
}
