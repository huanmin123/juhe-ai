package gatewaymedia

// veo 视频 adapter 测试（契约 §5.2 报文，fixture 形态与 mockupstream
// veoOperationBody 同源派生——Mock 上游按同一契约实现）：创建报文构造
//（prompt→instances、size→aspectRatio/resolution 换算、input_reference
// base64/data URL 直传与 url 形态 400、negative_prompt→parameters、
// provider_options 合并）、轮询/取消请求构造与产物定位声明、轮询响应
// 归一（done 归一、error 终态、done 缺 uri 的协议违约、非 2xx）。
import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// veoCreateAcceptedFixture 对齐 mockupstream 创建响应形态
//（media_gemini_video_create_ok，受理凭据 = name 字段）。
const veoCreateAcceptedFixture = `{"name":"models/veo-3.0-generate-preview/operations/0123456789ab"}`

const veoPollRunningFixture = `{"done":false}`

// veoPollDoneURIFixture 对齐 mockupstream done:true 成功形态
//（media_gemini_video_poll_done_uri，uri 为绝对签名 URL）。
const veoPollDoneURIFixture = `{"done":true,"response":{"generateVideoResponse":{"generatedSamples":[{"video":{"uri":"https://storage.googleapis.com/mock-bucket/veo%2Fsample.mp4?X-Goog-Signature=abc"}}]}}}`

// veoPollErrorFixture 对齐 mockupstream done:true 失败形态
//（media_gemini_video_poll_error，gemini 错误三元组）。
const veoPollErrorFixture = `{"done":true,"error":{"code":500,"message":"Video generation failed after the request was accepted","status":"INTERNAL"}}`

func TestVeoVideoAdapterRegistry(t *testing.T) {
	adapter := VideoAdapterForProvider("gemini")
	if adapter == nil {
		t.Fatal("gemini（veo）视频 adapter 应已注册")
	}
	if adapter.Provider() != "gemini" {
		t.Fatalf("Provider = %q", adapter.Provider())
	}
	if VideoAdapterForProvider(" Gemini ") == nil {
		t.Fatal("provider_code 归一匹配失败")
	}
}

func TestVeoVideoAdapterCapabilities(t *testing.T) {
	caps := VideoAdapterForProvider("gemini").Capabilities()
	// Veo 请求面（契约 §5.2）：negative_prompt 与 input_reference（仅 base64
	// 直传）原生支持；seconds/n/seed/audio 请求面无对应字段 → 忽略 + 回显。
	if !caps.SupportsNegativePrompt || !caps.SupportsInputReference {
		t.Fatalf("veo 应支持 negative_prompt/input_reference: %+v", caps)
	}
	if caps.SupportsSeconds || caps.SupportsN || caps.SupportsSeed || caps.SupportsAudio {
		t.Fatalf("veo 请求面无 seconds/n/seed/audio: %+v", caps)
	}
}

func TestVeoVideoAdapterContentFromArtifact(t *testing.T) {
	veo := VideoAdapterForProvider("gemini")
	if !veo.ContentFromArtifact() {
		t.Fatal("veo 产物定位应为绝对 Artifact.ContentURL（GCS 签名 URL）")
	}
	if VideoAdapterForProvider("openai").ContentFromArtifact() {
		t.Fatal("openai 产物定位应由 job id 构造（对比项）")
	}
}

func TestVeoVideoAdapterCreateRequest(t *testing.T) {
	adapter := VideoAdapterForProvider("gemini")
	params, err := ParseVideoParams(map[string]any{
		"model":           "veo-3.0-generate-preview",
		"prompt":          "a cat surfing",
		"size":            "1280x720",
		"negative_prompt": "blurry",
		// seconds/seed/audio 属 ignored（Capabilities 裁决），进 params_ignored。
		"seconds": "8",
		"seed":    float64(42),
		"audio":   true,
	}, adapter.Capabilities())
	if err != nil {
		t.Fatalf("ParseVideoParams err = %v", err)
	}
	out, err := adapter.Create(context.Background(), VideoCreateInput{Params: params})
	if err != nil {
		t.Fatalf("Create err = %v", err)
	}
	if out.Method != http.MethodPost || out.Path != "/v1beta/models/veo-3.0-generate-preview:predictLongRunning" {
		t.Fatalf("method/path = %s %s", out.Method, out.Path)
	}
	var body struct {
		Instances []struct {
			Prompt string `json:"prompt"`
			Image  *struct {
				BytesBase64Encoded string `json:"bytesBase64Encoded"`
			} `json:"image"`
		} `json:"instances"`
		Parameters *struct {
			AspectRatio    string `json:"aspectRatio"`
			Resolution     string `json:"resolution"`
			NegativePrompt string `json:"negativePrompt"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(out.Body, &body); err != nil {
		t.Fatalf("decode body: %v: %s", err, out.Body)
	}
	if len(body.Instances) != 1 || body.Instances[0].Prompt != "a cat surfing" {
		t.Fatalf("instances = %+v", body.Instances)
	}
	if body.Instances[0].Image != nil {
		t.Fatalf("未传 input_reference 时不得携带 image: %s", out.Body)
	}
	if body.Parameters == nil || body.Parameters.AspectRatio != "16:9" || body.Parameters.Resolution != "720p" {
		t.Fatalf("parameters 换算错误（1280x720 → 16:9/720p）: %s", out.Body)
	}
	if body.Parameters.NegativePrompt != "blurry" {
		t.Fatalf("negativePrompt 缺失: %s", out.Body)
	}
	for _, banned := range []string{"seconds", "\"seed\"", "\"audio\""} {
		if strings.Contains(string(out.Body), banned) {
			t.Fatalf("创建报文不得含 ignored 键 %s: %s", banned, out.Body)
		}
	}
}

func TestVeoVideoAdapterCreateSizeConversion(t *testing.T) {
	adapter := VideoAdapterForProvider("gemini")
	cases := []struct {
		size             string
		aspectRatio      string
		resolution       string
		inputReference   string
		wantBase64Img    string
		wantInputRefFail bool
	}{
		// 换算规则（契约 §2.2/§2.7）：宽≥高 → 16:9，反之 9:16；短边 ≥1080 →
		// 1080p，否则 720p（两档取最近档）。
		{"1280x720", "16:9", "720p", "", "", false},
		{"1920x1080", "16:9", "1080p", "", "", false},
		{"720x1280", "9:16", "720p", "", "", false},
		{"1080x1920", "9:16", "1080p", "", "", false},
		{"960x960", "16:9", "720p", "", "", false},
		{"640x360", "16:9", "720p", "", "", false},
		// input_reference：data URL 剥前缀直传 bytesBase64Encoded；裸 base64
		// 直传；http(s) url 形态 400（零存储不下载，契约 §5.2/M3 裁决）。
		{"", "", "", "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("png-bytes")), base64.StdEncoding.EncodeToString([]byte("png-bytes")), false},
		{"", "", "", base64.StdEncoding.EncodeToString([]byte("raw-b64")), base64.StdEncoding.EncodeToString([]byte("raw-b64")), false},
		{"", "", "", "https://example.com/first-frame.png", "", true},
	}
	for _, tc := range cases {
		request := map[string]any{"model": "veo-3.0-generate-preview", "prompt": "p"}
		if tc.size != "" {
			request["size"] = tc.size
		}
		if tc.inputReference != "" {
			request["input_reference"] = tc.inputReference
		}
		params, err := ParseVideoParams(request, adapter.Capabilities())
		if err != nil {
			t.Fatalf("ParseVideoParams(%s): %v", tc.size, err)
		}
		out, err := adapter.Create(context.Background(), VideoCreateInput{Params: params})
		if tc.wantInputRefFail {
			if err == nil {
				t.Fatalf("input_reference=%q 应拒绝（url 形态不支持）: %s", tc.inputReference, out.Body)
			}
			if !errors.Is(err, ErrParamUnsupported) {
				t.Fatalf("url input_reference 错误应为 ErrParamUnsupported（400 语义）: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("Create(%s): %v", tc.size, err)
		}
		var body struct {
			Instances []struct {
				Image *struct {
					BytesBase64Encoded string `json:"bytesBase64Encoded"`
				} `json:"image"`
			} `json:"instances"`
			Parameters *struct {
				AspectRatio string `json:"aspectRatio"`
				Resolution  string `json:"resolution"`
			} `json:"parameters"`
		}
		if err := json.Unmarshal(out.Body, &body); err != nil {
			t.Fatalf("decode body: %v: %s", err, out.Body)
		}
		if tc.size == "" {
			if body.Parameters != nil {
				t.Fatalf("未传 size 不得携带 parameters: %s", out.Body)
			}
		} else {
			if body.Parameters == nil || body.Parameters.AspectRatio != tc.aspectRatio || body.Parameters.Resolution != tc.resolution {
				t.Fatalf("size=%s 换算错误，want %s/%s: %s", tc.size, tc.aspectRatio, tc.resolution, out.Body)
			}
		}
		if tc.inputReference == "" {
			if body.Instances[0].Image != nil {
				t.Fatalf("未传 input_reference 不得携带 image: %s", out.Body)
			}
		} else if body.Instances[0].Image == nil || body.Instances[0].Image.BytesBase64Encoded != tc.wantBase64Img {
			t.Fatalf("input_reference 直传载荷错误: %s", out.Body)
		}
	}
}

func TestVeoVideoAdapterProviderOptionsMerge(t *testing.T) {
	adapter := VideoAdapterForProvider("gemini")
	params, err := ParseVideoParams(map[string]any{
		"model":  "veo-3.0-generate-preview",
		"prompt": "p",
		"size":   "1280x720",
	}, adapter.Capabilities())
	if err != nil {
		t.Fatalf("ParseVideoParams err = %v", err)
	}
	out, err := adapter.Create(context.Background(), VideoCreateInput{
		Params: params,
		// 命中 gemini 子对象：覆盖换算结果 + 厂商个例键原样透传（L3）；
		// 未命中的 openai 子对象不进报文。
		ProviderOptions: map[string]any{
			"gemini": map[string]any{
				"parameters": map[string]any{"aspectRatio": "9:16", "personGeneration": "allow_all"},
			},
			"openai": map[string]any{"vendor_flag": true},
		},
	})
	if err != nil {
		t.Fatalf("Create err = %v", err)
	}
	if !strings.Contains(string(out.Body), `"aspectRatio":"9:16"`) || !strings.Contains(string(out.Body), `"personGeneration":"allow_all"`) {
		t.Fatalf("provider_options 命中子对象未 deep-merge: %s", out.Body)
	}
	if strings.Contains(string(out.Body), "vendor_flag") {
		t.Fatalf("未命中 provider 的键不得进报文: %s", out.Body)
	}
}

func TestVeoVideoAdapterRequestBuilders(t *testing.T) {
	adapter := VideoAdapterForProvider("gemini")
	name := "models/veo-3.0-generate-preview/operations/0123456789ab"
	method, path, body := adapter.BuildPollRequest(name)
	if method != http.MethodGet || path != "/v1beta/"+name || body != nil {
		t.Fatalf("poll request = %s %s %v", method, path, body)
	}
	cancelMethod, cancelPath := adapter.BuildCancelRequest(name)
	if cancelMethod != http.MethodPost || cancelPath != "/v1beta/"+name+":cancel" {
		t.Fatalf("cancel request = %s %s", cancelMethod, cancelPath)
	}
}

func TestVeoVideoAdapterParsePollResponse(t *testing.T) {
	adapter := VideoAdapterForProvider("gemini")
	// 创建响应（name 即受理凭据，done 缺席 → queued，契约 §2.6 gemini 列）。
	created, err := adapter.ParsePollResponse(http.StatusOK, []byte(veoCreateAcceptedFixture))
	if err != nil {
		t.Fatalf("create parse err = %v", err)
	}
	if created.UpstreamJobID != "models/veo-3.0-generate-preview/operations/0123456789ab" {
		t.Fatalf("UpstreamJobID = %q", created.UpstreamJobID)
	}
	if created.Status != JobStatusQueued {
		t.Fatalf("create status = %q, want queued", created.Status)
	}
	// 轮询未 done → queued（Veo 无进展字段，in_progress 不可达）。
	running, err := adapter.ParsePollResponse(http.StatusOK, []byte(veoPollRunningFixture))
	if err != nil {
		t.Fatalf("running parse err = %v", err)
	}
	if running.Status != JobStatusQueued {
		t.Fatalf("running status = %q, want queued", running.Status)
	}
	// done + uri → completed，uri 进 Artifact.ContentURL。
	completed, err := adapter.ParsePollResponse(http.StatusOK, []byte(veoPollDoneURIFixture))
	if err != nil {
		t.Fatalf("done parse err = %v", err)
	}
	if completed.Status != JobStatusCompleted {
		t.Fatalf("done status = %q, want completed", completed.Status)
	}
	if completed.Artifact.ContentURL != "https://storage.googleapis.com/mock-bucket/veo%2Fsample.mp4?X-Goog-Signature=abc" {
		t.Fatalf("artifact uri = %q", completed.Artifact.ContentURL)
	}
	if completed.Usage.OutputVideoSeconds != nil {
		t.Fatalf("veo 不回报输出秒数（usage_missing 口径），不得伪造: %+v", completed.Usage)
	}
	// done + error → failed（code 取 status）。
	failed, err := adapter.ParsePollResponse(http.StatusOK, []byte(veoPollErrorFixture))
	if err != nil {
		t.Fatalf("error parse err = %v", err)
	}
	if failed.Status != JobStatusFailed || failed.Error == nil {
		t.Fatalf("failed = %+v", failed)
	}
	if failed.Error.Code != "INTERNAL" || failed.Error.Message == "" {
		t.Fatalf("error 摘要 = %+v", failed.Error)
	}
	// 非 2xx → UpstreamStatusError（证据原样上抛）。
	_, err = adapter.ParsePollResponse(http.StatusInternalServerError, []byte(`{"error":{"code":500,"message":"boom","status":"INTERNAL"}}`))
	var statusErr *UpstreamStatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("非 2xx 应为 UpstreamStatusError: %v", err)
	}
	// done 但缺 uri 与 error → 协议违约，不猜测产物可达性。
	_, err = adapter.ParsePollResponse(http.StatusOK, []byte(`{"done":true,"response":{}}`))
	if err == nil || !strings.Contains(err.Error(), "generatedSamples") {
		t.Fatalf("done 缺产物定位应报协议违约: %v", err)
	}
	// 非 JSON → 报错。
	if _, err := adapter.ParsePollResponse(http.StatusOK, []byte(`not-json`)); err == nil {
		t.Fatal("非 JSON 应报错")
	}
}
