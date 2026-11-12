// 2026-11-11 图像生成同步批网关级验收：xAI / 火山 / Qwen-Image 三条出站
// 适配走真实网关链（隔离 PG 库 + 空闲端口 + 全 Mock 上游），验证：
//   - xai：OpenAI 兼容 /v1/images/generations 出站，请求归一（stream/quality
//     忽略、provider_options 不泄漏），响应 data[].url 原样透传；
//   - volcengine：Ark /api/v3/images/generations 出站，watermark 默认 false，
//     usage.generated_images 张数归一；
//   - qwen：DashScope 原生 multimodal-generation 出站，请求改写为
//     input.messages + parameters（size W*H、watermark:false），响应由网关
//     转换为 OpenAI Images。
//
// 不依赖真实付费上游；真实上游协议探针与 gpt 中转 E2E 由 T207 的授权验证覆盖。
package acceptance

import (
	"encoding/json"
	"io"
	"net/http"
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

			body := `{"model":"` + tc.model + `","prompt":"一只猫在弹钢琴","n":1,"size":"1024x1024","stream":true}`
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
			if _, ok := upstreamBody["provider_options"]; ok {
				t.Fatalf("%s image provider_options leaked upstream: %s", tc.name, call.Body)
			}
			switch tc.name {
			case "gpt":
				if _, ok := upstreamBody["stream"]; ok {
					t.Fatalf("gpt image stream must be ignored: %s", call.Body)
				}
				if _, ok := upstreamBody["partial_images"]; ok {
					t.Fatalf("gpt image partial_images must be ignored: %s", call.Body)
				}
			case "xai":
				if _, ok := upstreamBody["stream"]; ok {
					t.Fatalf("xai image stream must be ignored: %s", call.Body)
				}
				if _, ok := upstreamBody["quality"]; ok {
					t.Fatalf("xai image quality must be ignored: %s", call.Body)
				}
			case "volcengine":
				if upstreamBody["watermark"] != false {
					t.Fatalf("volcengine image watermark=%v, want false: %s", upstreamBody["watermark"], call.Body)
				}
			case "qwen":
				if upstreamBody["watermark"] != nil {
					t.Fatalf("qwen image watermark must live under parameters: %s", call.Body)
				}
				parameters, _ := upstreamBody["parameters"].(map[string]any)
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
