// 2026-11-11 图像生成同步批网关级验收：xAI / 火山 / Qwen-Image 三条出站
// 适配走真实网关链（隔离 PG 库 + 空闲端口 + 全 Mock 上游），验证：
//   - xai：OpenAI 兼容 /v1/images/generations 出站，请求归一（stream/quality
//     忽略、metadata 投影为 provider_options 后不泄漏），响应 data[].url 原样
//     透传；
//   - volcengine：Ark /api/v3/images/generations 出站，watermark 默认 false，
//     usage.generated_images 张数归一；
//   - qwen：DashScope 原生 multimodal-generation 出站，请求改写为
//     input.messages + parameters（size W*H、watermark:false），响应由网关
//     转换为 OpenAI Images。
//
// 不依赖真实付费上游；真实上游协议探针与 gpt 中转 E2E 由 T207 的授权验证覆盖。
package acceptance

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"testing"
	"time"

	platformmock "github.com/huanminabc/juhe-ai/backend-go-platform/mockupstream"
)

func imagePost(t *testing.T, baseURL, apiKey, body string) (int, string, http.Header) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, baseURL+"/v1/images/generations", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build image request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 120 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("image request: %v", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read image response: %v", err)
	}
	return response.StatusCode, string(raw), response.Header
}

func (f *fullchainFixture) createImageAccount(name, providerCode, profileID, preset, model, baseURL, upstreamKey, groupID string) string {
	f.t.Helper()
	_, created := f.admin.do(http.MethodPost, "/__aisys__/api/accounts", map[string]any{
		"providerCode":              providerCode,
		"providerProtocolProfileId": profileID,
		"name":                      name,
		"type":                      "api_key",
		"capabilityDomain":          "image",
		"credentials": map[string]any{
			"api_key":                  upstreamKey,
			"base_url":                 baseURL,
			"supported_endpoint_modes": []string{"images_json"},
		},
		"supportedModels": []string{model},
		"connections": []map[string]any{{
			"connectionPresetCode": preset,
			"baseUrl":              baseURL,
			"endpointModes":        []string{"images_json"},
			"enabled":              true,
		}},
		"healthCheckConnectionPresetCode": preset,
		"healthCheckEndpointMode":         "images_json",
		"healthCheckModel":                model,
		"status":                          "active",
		"groupId":                         groupID,
	}, wantStatus(http.StatusCreated))
	accountID := str(data(created)["id"])
	if accountID == "" {
		f.t.Fatalf("image account create payload wrong: %#v", created)
	}
	return accountID
}

func TestFullchainImageProvidersSync(t *testing.T) {
	requireFullchainGate(t)
	f := startFullchainFixture(t)
	f.fullchainT5EnableOwnerImageGeneration()

	cases := []struct {
		name       string
		provider   string
		profileID  string
		preset     string
		model      string
		baseSuffix string
		scenario   platformmock.Scenario
		wantURL    string
		wantPath   string
	}{
		{
			name: "gpt", provider: "gpt", profileID: "profile_gpt_openai_v1",
			preset: "gpt_openai_v1", model: "gpt-image-2",
			baseSuffix: "/v1", scenario: platformmock.ScenarioMediaImagesOK,
			wantURL: "mock-0001.png", wantPath: "/v1/images/generations",
		},
		{
			name: "xai", provider: "xai", profileID: "profile_xai_openai_v1",
			preset: "xai_openai_v1", model: "grok-imagine-image-2.0",
			baseSuffix: "/v1", scenario: platformmock.ScenarioMediaXaiImageOK,
			wantURL: "xai-0001.png", wantPath: "/v1/images/generations",
		},
		{
			name: "volcengine", provider: "volcengine", profileID: "profile_volcengine_openai_v1",
			preset: "volcengine_ark", model: "doubao-seedream-5-0-flash-260915",
			baseSuffix: "/api/v3", scenario: platformmock.ScenarioMediaVolcengineImageOK,
			wantURL: "seedream-0001.png", wantPath: "/api/v3/images/generations",
		},
		{
			name: "qwen", provider: "qwen", profileID: "profile_qwen_openai_v1",
			preset: "qwen_dashscope", model: "qwen-image-3.0",
			baseSuffix: "", scenario: platformmock.ScenarioMediaQwenImageOK,
			wantURL: "qwen-0001.png", wantPath: "/api/v1/services/aigc/multimodal-generation/generation",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := fullchainUpstreamKey(t, "img-sync-"+tc.name)
			groupID := f.createGroupWithProvider("图像同步-"+tc.name, tc.provider)
			f.createImageAccount("全链路-图像同步-"+tc.name, tc.provider, tc.profileID,
				tc.preset, tc.model, f.mock.server.URL+tc.baseSuffix, key, groupID)
			f.mock.setDefault(key, tc.scenario)
			strategyID := f.createStrategy("图像同步-"+tc.name+"-策略", "normal", []map[string]any{
				{"groupId": groupID, "priority": 1, "weight": 100},
			}, nil)
			apiKey := f.createAPIKey("图像同步-"+tc.name+"-Key", strategyID)

			body := `{"model":"` + tc.model + `","prompt":"一只猫在弹钢琴","n":1,"size":"1024x1024","stream":true,"metadata":{"seed":42}}`
			status, responseBody, responseHeader := imagePost(t, f.gw.baseURL, apiKey, body)
			if status != http.StatusOK {
				t.Fatalf("%s image status=%d body=%s, want 200", tc.name, status, responseBody)
			}
			if !strings.Contains(responseBody, tc.wantURL) {
				t.Fatalf("%s image response missing %s: %s", tc.name, tc.wantURL, responseBody)
			}
			if !strings.Contains(responseBody, `"data"`) {
				t.Fatalf("%s image response not OpenAI Images shape: %s", tc.name, responseBody)
			}
			if contentType := responseHeader.Get("Content-Type"); !strings.Contains(contentType, "application/json") {
				t.Fatalf("%s image response content-type=%q, want JSON", tc.name, contentType)
			}
			if got := responseHeader.Get("X-Juhe-Image-Provider"); got != tc.provider {
				t.Fatalf("%s image provider echo=%q, want %q", tc.name, got, tc.provider)
			}
			if tc.name == "qwen" {
				if !strings.Contains(responseBody, `"metadata"`) || !strings.Contains(responseBody, `"seed":42`) {
					t.Fatalf("%s image conversion must echo request metadata: %s", tc.name, responseBody)
				}
			} else if strings.Contains(responseBody, `"metadata"`) {
				t.Fatalf("%s passthrough response must not synthesize metadata: %s", tc.name, responseBody)
			}
			if got := responseHeader.Get("X-Juhe-Image-Params-Ignored"); !strings.Contains(got, "stream") {
				t.Fatalf("%s image params ignored echo=%q, want stream", tc.name, got)
			}
			calls := f.mock.callsByKey(key)
			if len(calls) == 0 {
				t.Fatalf("%s image no upstream call", tc.name)
			}
			call := calls[len(calls)-1]
			if call.Method != http.MethodPost || call.Path != tc.wantPath {
				t.Fatalf("%s image upstream call=%s %s, want POST %s", tc.name, call.Method, call.Path, tc.wantPath)
			}
			if call.Model != tc.model {
				t.Fatalf("%s image upstream model=%q, want %q", tc.name, call.Model, tc.model)
			}
			if call.AuthHeader != "Bearer "+key {
				t.Fatalf("%s image upstream auth=%q, want Bearer <key>", tc.name, call.AuthHeader)
			}
			var upstreamBody map[string]any
			if err := json.Unmarshal([]byte(call.Body), &upstreamBody); err != nil {
				t.Fatalf("%s image upstream body not JSON: %v; body=%s", tc.name, err, call.Body)
			}
			if _, ok := upstreamBody["metadata"]; ok {
				t.Fatalf("%s image metadata leaked upstream: %s", tc.name, call.Body)
			}
			if _, ok := upstreamBody["provider_options"]; ok {
				t.Fatalf("%s image provider_options leaked upstream: %s", tc.name, call.Body)
			}
			if got := responseHeader.Get("X-Juhe-Image-Provider-Options-Applied"); !strings.Contains(got, "seed") {
				t.Fatalf("%s image provider options applied echo=%q, want seed", tc.name, got)
			}
			switch tc.name {
			case "gpt":
				if _, ok := upstreamBody["stream"]; ok {
					t.Fatalf("gpt image stream must be ignored: %s", call.Body)
				}
				if _, ok := upstreamBody["partial_images"]; ok {
					t.Fatalf("gpt image partial_images must be ignored: %s", call.Body)
				}
				if upstreamBody["seed"] != float64(42) {
					t.Fatalf("gpt image seed=%v, want metadata projection: %s", upstreamBody["seed"], call.Body)
				}
			case "xai":
				if _, ok := upstreamBody["stream"]; ok {
					t.Fatalf("xai image stream must be ignored: %s", call.Body)
				}
				if _, ok := upstreamBody["quality"]; ok {
					t.Fatalf("xai image quality must be ignored: %s", call.Body)
				}
				if upstreamBody["seed"] != float64(42) {
					t.Fatalf("xai image seed=%v, want metadata projection: %s", upstreamBody["seed"], call.Body)
				}
			case "volcengine":
				if upstreamBody["watermark"] != false {
					t.Fatalf("volcengine image watermark=%v, want false: %s", upstreamBody["watermark"], call.Body)
				}
				if upstreamBody["seed"] != float64(42) {
					t.Fatalf("volcengine image seed=%v, want metadata projection: %s", upstreamBody["seed"], call.Body)
				}
			case "qwen":
				if upstreamBody["watermark"] != nil {
					t.Fatalf("qwen image watermark must live under parameters: %s", call.Body)
				}
				parameters, _ := upstreamBody["parameters"].(map[string]any)
				if parameters["seed"] != float64(42) {
					t.Fatalf("qwen image parameters.seed=%v, want metadata projection: %s", parameters["seed"], call.Body)
				}
				if parameters["size"] != "1024*1024" {
					t.Fatalf("qwen image size=%v, want 1024*1024: %s", parameters["size"], call.Body)
				}
				if parameters["watermark"] != false {
					t.Fatalf("qwen image parameters.watermark=%v, want false: %s", parameters["watermark"], call.Body)
				}
			}
		})
	}
}

// imageEditPostMultipart 走真实网关链的 multipart 编辑请求（2026-11-12
// T216）：单张 PNG 参考图 + mask + 文本字段（含 stream/partial_images，
// 验证跨厂商忽略；response_format 用于验证其余文本字段仍保留）。
func imageEditPostMultipart(t *testing.T, baseURL, apiKey, model string) (int, string, http.Header) {
	t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	for name, value := range map[string]string{
		"model":           model,
		"prompt":          "把背景换成海边",
		"stream":          "true",
		"partial_images":  "2",
		"response_format": "b64_json",
	} {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatalf("write field %s: %v", name, err)
		}
	}
	png := append([]byte("\x89PNG\r\n\x1a\n"), []byte("acceptance-png-body")...)
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
	writeFile("image", "cat.png")
	writeFile("mask", "mask.png")
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	request, err := http.NewRequest(http.MethodPost, baseURL+"/v1/images/edits", bytes.NewReader(buffer.Bytes()))
	if err != nil {
		t.Fatalf("build image edit request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := (&http.Client{Timeout: 120 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("image edit request: %v", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read image edit response: %v", err)
	}
	return response.StatusCode, string(raw), response.Header
}

// TestFullchainImageEditProvidersSync 验证编辑面四家共用统一图像链（T216）：
// gpt multipart 字节级透传（仅删 stream 文本字段）、xai 原生 edits、火山改写
// generations、qwen 原生 multimodal-generation + 响应转换；响应统一 OpenAI
// Images JSON，回显头 provider 与 ignored 一致。
func TestFullchainImageEditProvidersSync(t *testing.T) {
	requireFullchainGate(t)
	f := startFullchainFixture(t)
	f.fullchainT5EnableOwnerImageGeneration()

	cases := []struct {
		name       string
		provider   string
		profileID  string
		preset     string
		model      string
		baseSuffix string
		wantPath   string
		// scenario/wantURL：gpt/xai/volcengine 出站落在 OpenAI 兼容 Images
		// 面（edit-ok 载荷）；qwen 原生端点必须返回 DashScope 形态，响应由
		// 网关转换为 OpenAI Images（复用生成面转换）。
		scenario platformmock.Scenario
		wantURL  string
	}{
		{
			name: "gpt", provider: "gpt", profileID: "profile_gpt_openai_v1",
			preset: "gpt_openai_v1", model: "gpt-image-2",
			baseSuffix: "/v1", wantPath: "/v1/images/edits",
			scenario: platformmock.ScenarioMediaImagesEditOK, wantURL: "edit-0001.png",
		},
		{
			name: "xai", provider: "xai", profileID: "profile_xai_openai_v1",
			preset: "xai_openai_v1", model: "grok-imagine-image-2.0",
			baseSuffix: "/v1", wantPath: "/v1/images/edits",
			scenario: platformmock.ScenarioMediaImagesEditOK, wantURL: "edit-0001.png",
		},
		{
			name: "volcengine", provider: "volcengine", profileID: "profile_volcengine_openai_v1",
			preset: "volcengine_ark", model: "doubao-seedream-5-0-flash-260915",
			baseSuffix: "/api/v3", wantPath: "/api/v3/images/generations",
			scenario: platformmock.ScenarioMediaImagesEditOK, wantURL: "edit-0001.png",
		},
		{
			name: "qwen", provider: "qwen", profileID: "profile_qwen_openai_v1",
			preset: "qwen_dashscope", model: "qwen-image-3.0",
			baseSuffix: "", wantPath: "/api/v1/services/aigc/multimodal-generation/generation",
			scenario: platformmock.ScenarioMediaQwenImageOK, wantURL: "qwen-0001.png",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := fullchainUpstreamKey(t, "img-edit-"+tc.name)
			groupID := f.createGroupWithProvider("图像编辑-"+tc.name, tc.provider)
			f.createImageAccount("全链路-图像编辑-"+tc.name, tc.provider, tc.profileID,
				tc.preset, tc.model, f.mock.server.URL+tc.baseSuffix, key, groupID)
			f.mock.setDefault(key, tc.scenario)
			strategyID := f.createStrategy("图像编辑-"+tc.name+"-策略", "normal", []map[string]any{
				{"groupId": groupID, "priority": 1, "weight": 100},
			}, nil)
			apiKey := f.createAPIKey("图像编辑-"+tc.name+"-Key", strategyID)

			status, responseBody, responseHeader := imageEditPostMultipart(t, f.gw.baseURL, apiKey, tc.model)
			if status != http.StatusOK {
				t.Fatalf("%s image edit status=%d body=%s, want 200", tc.name, status, responseBody)
			}
			if !strings.Contains(responseBody, tc.wantURL) {
				t.Fatalf("%s image edit response missing %s: %s", tc.name, tc.wantURL, responseBody)
			}
			if got := responseHeader.Get("X-Juhe-Image-Provider"); got != tc.provider {
				t.Fatalf("%s image edit provider echo=%q, want %q", tc.name, got, tc.provider)
			}
			ignored := responseHeader.Get("X-Juhe-Image-Params-Ignored")
			if !strings.Contains(ignored, "stream") {
				t.Fatalf("%s image edit params ignored echo=%q, want stream", tc.name, ignored)
			}
			if tc.name != "gpt" && !strings.Contains(ignored, "mask") {
				t.Fatalf("%s image edit params ignored echo=%q, want mask", tc.name, ignored)
			}
			calls := f.mock.callsByKey(key)
			if len(calls) == 0 {
				t.Fatalf("%s image edit no upstream call", tc.name)
			}
			call := calls[len(calls)-1]
			if call.Method != http.MethodPost || call.Path != tc.wantPath {
				t.Fatalf("%s image edit upstream call=%s %s, want POST %s", tc.name, call.Method, call.Path, tc.wantPath)
			}
			switch tc.name {
			case "gpt":
				for _, dropped := range []string{"stream", "partial_images"} {
					if strings.Contains(call.Body, `name="`+dropped+`"`) {
						t.Fatalf("gpt edit multipart %s must be removed: %s", dropped, call.Body)
					}
				}
				for _, kept := range []string{"image", "prompt", "model", "response_format"} {
					if !strings.Contains(call.Body, `name="`+kept+`"`) {
						t.Fatalf("gpt edit multipart part %s missing: %s", kept, call.Body)
					}
				}
			case "xai":
				var upstreamBody map[string]any
				if err := json.Unmarshal([]byte(call.Body), &upstreamBody); err != nil {
					t.Fatalf("xai edit upstream body not JSON: %v; body=%s", err, call.Body)
				}
				image, _ := upstreamBody["image"].(map[string]any)
				if image == nil || !strings.HasPrefix(image["url"].(string), "data:image/png;base64,") {
					t.Fatalf("xai edit image = %#v", upstreamBody["image"])
				}
				if _, ok := upstreamBody["mask"]; ok {
					t.Fatalf("xai edit mask must not be forwarded: %s", call.Body)
				}
			case "volcengine":
				var upstreamBody map[string]any
				if err := json.Unmarshal([]byte(call.Body), &upstreamBody); err != nil {
					t.Fatalf("volcengine edit upstream body not JSON: %v; body=%s", err, call.Body)
				}
				if !strings.HasPrefix(upstreamBody["image"].(string), "data:image/png;base64,") {
					t.Fatalf("volcengine edit image = %#v", upstreamBody["image"])
				}
				if upstreamBody["watermark"] != false {
					t.Fatalf("volcengine edit watermark=%v, want false", upstreamBody["watermark"])
				}
			case "qwen":
				var upstreamBody map[string]any
				if err := json.Unmarshal([]byte(call.Body), &upstreamBody); err != nil {
					t.Fatalf("qwen edit upstream body not JSON: %v; body=%s", err, call.Body)
				}
				input, _ := upstreamBody["input"].(map[string]any)
				messages, _ := input["messages"].([]any)
				content, _ := messages[0].(map[string]any)["content"].([]any)
				if len(content) != 2 || !strings.HasPrefix(content[0].(map[string]any)["image"].(string), "data:image/png;base64,") {
					t.Fatalf("qwen edit content = %#v", content)
				}
				if content[1].(map[string]any)["text"] != "把背景换成海边" {
					t.Fatalf("qwen edit text = %#v", content[1])
				}
			}
		})
	}
}
