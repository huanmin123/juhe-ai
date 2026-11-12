package accountprobe

// 媒体域密钥连通性探测测试（2026-11-01 用户裁决）：分类表按 Mock 上游回放
// 实测（video 404/400=通过、401/403/5xx=失败；image/audio 400/422/2xx=通过、
// 401/5xx=失败），请求形状断言三模式绝不触碰真实生成/创建端点。
import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountquality"
)

type connectivityCapture struct {
	method    string
	path      string
	body      string
	auth      string
	apiKey    string
	timedOut  bool
	observed  bool
	observedN int
}

func newConnectivityServer(t *testing.T, status int, body string, capture *connectivityCapture) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if capture != nil {
			capture.method = r.Method
			capture.path = r.URL.Path
			capture.body = string(raw)
			capture.auth = r.Header.Get("Authorization")
			capture.apiKey = r.Header.Get("X-Api-Key")
			capture.observed = true
			capture.observedN++
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func connectivityView(baseURL string, mode EndpointMode, model string) *View {
	view := probeView(baseURL)
	view.HealthCheckModel = model
	view.SupportedModels = []string{model}
	view.HealthCheckEndpointMode = string(mode)
	view.NormalizeEndpointModes = map[EndpointMode]bool{mode: true}
	return view
}

// TestConnectivityProbeClassificationTable 按裁决分类表逐格回放：三种媒体
// 模式的通过/失败状态码与文案，evidence 均带 connectivity 标记。
func TestConnectivityProbeClassificationTable(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name        string
		mode        EndpointMode
		model       string
		status      int
		body        string
		wantSuccess bool
		wantMessage string // 子串断言
	}{
		// video_create：查询固定占位任务，404/400 = 任务不存在属预期（通过）。
		{"video 404 通过", ModeVideoCreate, "video-model", http.StatusNotFound, `{"code":"ResourceNotFound"}`, true, "密钥连通性验证通过（上游 HTTP 404）"},
		{"video 400 通过", ModeVideoCreate, "video-model", http.StatusBadRequest, `{"error":{"message":"bad task id"}}`, true, "密钥连通性验证通过（上游 HTTP 400）"},
		{"video 401 密钥无效", ModeVideoCreate, "video-model", http.StatusUnauthorized, `{"error":{"message":"invalid api key"}}`, false, "上游拒绝访问（HTTP 401）"},
		{"video 403 密钥无效", ModeVideoCreate, "video-model", http.StatusForbidden, `denied`, false, "上游拒绝访问（HTTP 403）"},
		{"video 500 上游不可用", ModeVideoCreate, "video-model", http.StatusInternalServerError, `boom`, false, "上游服务不可用（HTTP 500）"},
		{"video 429 其余状态失败", ModeVideoCreate, "video-model", http.StatusTooManyRequests, `rate limited`, false, "密钥连通性验证失败（HTTP 429）"},
		// images_json：空 prompt 体被参数校验拒绝（400/422）= 密钥有效。
		{"image 400 通过", ModeImagesJSON, "img-model", http.StatusBadRequest, `{"error":{"code":"content_policy_violation","message":"prompt required"}}`, true, "密钥连通性验证通过（上游 HTTP 400）"},
		{"image 422 通过", ModeImagesJSON, "img-model", http.StatusUnprocessableEntity, `unprocessable`, true, "密钥连通性验证通过（上游 HTTP 422）"},
		{"image 200 通过", ModeImagesJSON, "img-model", http.StatusOK, `{"created":1,"data":[]}`, true, "密钥连通性验证通过（上游 HTTP 200）"},
		{"image 401 失败", ModeImagesJSON, "img-model", http.StatusUnauthorized, `{"error":{"message":"invalid api key"}}`, false, "上游拒绝访问（HTTP 401）"},
		{"image 500 失败", ModeImagesJSON, "img-model", http.StatusInternalServerError, `{"error":{"message":"upstream down"}}`, false, "上游服务不可用（HTTP 500）"},
		// audio_speech：带 model 的 POST /tts/create 同款分类。volcengine 缺 model
		// 会被 45000030 挡在 resource_id 解析层，携带 model 才能进入参数校验。
		{"audio 400 通过", ModeAudioSpeech, "tts-model", http.StatusBadRequest, `{"error":{"message":"empty body"}}`, true, "密钥连通性验证通过（上游 HTTP 400）"},
		{"audio 422 通过", ModeAudioSpeech, "tts-model", http.StatusUnprocessableEntity, `unprocessable`, true, "密钥连通性验证通过（上游 HTTP 422）"},
		{"audio 200 通过", ModeAudioSpeech, "tts-model", http.StatusOK, `ok`, true, "密钥连通性验证通过（上游 HTTP 200）"},
		{"audio 401 失败", ModeAudioSpeech, "tts-model", http.StatusUnauthorized, `unauthorized`, false, "上游拒绝访问（HTTP 401）"},
		{"audio 502 失败", ModeAudioSpeech, "tts-model", http.StatusBadGateway, `bad gateway`, false, "上游服务不可用（HTTP 502）"},
		// audio_job_get：查询固定占位任务，400/404 属预期（通过）。
		{"audio job 404 通过", ModeAudioJobGet, "asr-model", http.StatusNotFound, `{"code":"NotFound"}`, true, "密钥连通性验证通过（上游 HTTP 404）"},
		{"audio job 400 通过", ModeAudioJobGet, "asr-model", http.StatusBadRequest, `{"message":"bad task id"}`, true, "密钥连通性验证通过（上游 HTTP 400）"},
		{"audio job 401 失败", ModeAudioJobGet, "asr-model", http.StatusUnauthorized, `unauthorized`, false, "上游拒绝访问（HTTP 401）"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := newConnectivityServer(t, tc.status, tc.body, nil)
			defer server.Close()
			view := connectivityView(server.URL, tc.mode, tc.model)
			observation, err := newTestService(t, &fakeSource{view: view}).Probe(ctx, accountquality.ProbeRequest{AccountID: "acc-1", Full: true})
			if err != nil || observation == nil {
				t.Fatalf("probe: %+v %v", observation, err)
			}
			if observation.Result.Success != tc.wantSuccess {
				t.Fatalf("success = %v, want %v（message=%q）", observation.Result.Success, tc.wantSuccess, observation.Result.Message)
			}
			if !strings.Contains(observation.Result.Message, tc.wantMessage) {
				t.Fatalf("message = %q, want contains %q", observation.Result.Message, tc.wantMessage)
			}
			if observation.Result.StatusCode == nil || *observation.Result.StatusCode != tc.status {
				t.Fatalf("statusCode = %+v, want %d", observation.Result.StatusCode, tc.status)
			}
			if !observation.Evidence.Connectivity {
				t.Fatal("evidence 必须带 connectivity 标记")
			}
			if !observation.Evidence.HasRealUpstreamAttempt || observation.Evidence.UpstreamStatus != tc.status {
				t.Fatalf("evidence = %+v", observation.Evidence)
			}
		})
	}
}

// TestConnectivityProbeRequestShape 断言三模式出站请求形状：video 只 GET 任务
// 查询形态（含 base_url 版本根直拼），images 只发 {"model"} 空 prompt 体，
// audio POST /tts/create 空体；认证走既有 Bearer 通道。
func TestConnectivityProbeRequestShape(t *testing.T) {
	ctx := context.Background()

	t.Run("video_create 只查询不创建", func(t *testing.T) {
		capture := &connectivityCapture{}
		server := newConnectivityServer(t, http.StatusNotFound, `{}`, capture)
		defer server.Close()
		view := connectivityView(server.URL+"/api/v3", ModeVideoCreate, "video-model")
		view.ProviderCode = "volcengine"
		if _, err := newTestService(t, &fakeSource{view: view}).Probe(ctx, accountquality.ProbeRequest{AccountID: "acc-1", Full: true}); err != nil {
			t.Fatal(err)
		}
		if capture.method != http.MethodGet {
			t.Fatalf("method = %s, want GET", capture.method)
		}
		if capture.path != "/api/v3/contents/generations/tasks/"+ConnectivityProbeTaskID {
			t.Fatalf("path = %s", capture.path)
		}
		if capture.body != "" {
			t.Fatalf("video 连通性探测不得携带请求体：%q", capture.body)
		}
		if capture.auth != "Bearer sk-test" {
			t.Fatalf("auth = %q", capture.auth)
		}
	})

	t.Run("images_json 空 prompt 体", func(t *testing.T) {
		capture := &connectivityCapture{}
		server := newConnectivityServer(t, http.StatusBadRequest, `{}`, capture)
		defer server.Close()
		view := connectivityView(server.URL+"/v1", ModeImagesJSON, "img-model")
		if _, err := newTestService(t, &fakeSource{view: view}).Probe(ctx, accountquality.ProbeRequest{AccountID: "acc-1", Full: true}); err != nil {
			t.Fatal(err)
		}
		if capture.method != http.MethodPost || capture.path != "/v1/images/generations" {
			t.Fatalf("request = %s %s", capture.method, capture.path)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(capture.body), &payload); err != nil {
			t.Fatalf("body not json: %q", capture.body)
		}
		if len(payload) != 1 || payload["model"] != "img-model" {
			t.Fatalf("images 连通性体应只含 model：%v", payload)
		}
		if _, hasPrompt := payload["prompt"]; hasPrompt {
			t.Fatal("images 连通性体不得携带 prompt（避免真实生成）")
		}
	})

	t.Run("audio_speech 带 model 的 POST audio/speech", func(t *testing.T) {
		capture := &connectivityCapture{}
		server := newConnectivityServer(t, http.StatusBadRequest, `{}`, capture)
		defer server.Close()
		view := connectivityView(server.URL, ModeAudioSpeech, "tts-model")
		if _, err := newTestService(t, &fakeSource{view: view}).Probe(ctx, accountquality.ProbeRequest{AccountID: "acc-1", Full: true}); err != nil {
			t.Fatal(err)
		}
		if capture.method != http.MethodPost || capture.path != "/audio/speech" {
			t.Fatalf("request = %s %s", capture.method, capture.path)
		}
		// OpenAI/GLM/xAI 同步 TTS 缺 input 返回 400；必须携带 model 才能进入
		// 参数校验层，证明已过认证。
		if capture.body != `{"model":"tts-model"}` {
			t.Fatalf("audio 连通性探测应携带 model：%q", capture.body)
		}
	})

	t.Run("qwen 图像走 DashScope 原生 multimodal-generation", func(t *testing.T) {
		capture := &connectivityCapture{}
		server := newConnectivityServer(t, http.StatusBadRequest, `{}`, capture)
		defer server.Close()
		view := connectivityView(server.URL, ModeImagesJSON, "qwen-image")
		view.ProviderCode = "qwen"
		if _, err := newTestService(t, &fakeSource{view: view}).Probe(ctx, accountquality.ProbeRequest{AccountID: "acc-1", Full: true}); err != nil {
			t.Fatal(err)
		}
		if capture.method != http.MethodPost || capture.path != "/api/v1/services/aigc/multimodal-generation/generation" {
			t.Fatalf("request = %s %s", capture.method, capture.path)
		}
		if !strings.Contains(capture.body, `"messages"`) || !strings.Contains(capture.body, `"model":"qwen-image"`) {
			t.Fatalf("qwen image body = %q", capture.body)
		}
	})

	t.Run("qwen 视频查 DashScope 任务路径", func(t *testing.T) {
		capture := &connectivityCapture{}
		server := newConnectivityServer(t, http.StatusNotFound, `{}`, capture)
		defer server.Close()
		view := connectivityView(server.URL, ModeVideoCreate, "qwen-video")
		view.ProviderCode = "qwen"
		if _, err := newTestService(t, &fakeSource{view: view}).Probe(ctx, accountquality.ProbeRequest{AccountID: "acc-1", Full: true}); err != nil {
			t.Fatal(err)
		}
		if capture.method != http.MethodGet || capture.path != "/api/v1/tasks/"+ConnectivityProbeTaskID {
			t.Fatalf("request = %s %s", capture.method, capture.path)
		}
	})

	t.Run("qwen 长音频查 DashScope 任务路径且不创建", func(t *testing.T) {
		capture := &connectivityCapture{}
		server := newConnectivityServer(t, http.StatusNotFound, `{}`, capture)
		defer server.Close()
		view := connectivityView(server.URL, ModeAudioJobGet, "qwen-asr")
		view.ProviderCode = "qwen"
		if _, err := newTestService(t, &fakeSource{view: view}).Probe(ctx, accountquality.ProbeRequest{AccountID: "acc-1", Full: true}); err != nil {
			t.Fatal(err)
		}
		if capture.method != http.MethodGet || capture.path != "/api/v1/tasks/"+ConnectivityProbeTaskID {
			t.Fatalf("request = %s %s", capture.method, capture.path)
		}
		if capture.body != "" {
			t.Fatalf("audio_job_get 不得携带请求体：%q", capture.body)
		}
	})
}

// TestConnectivityProbeVolcengineSpeech：火山语音域是固定上游（openspeech +
// /api/v3/tts/create + X-Api-Key speech_token），账户 base_url（ark /api/v3）
// 不参与拼接；body 必须带 model（缺 model 会被 45000030 挡在 resource_id
// 解析层，无法证明密钥有效）。
func TestConnectivityProbeVolcengineSpeech(t *testing.T) {
	capture := &connectivityCapture{}
	server := newConnectivityServer(t, http.StatusBadRequest, `{"code":45001116,"message":"text_prompt is required"}`, capture)
	defer server.Close()
	original := volcengineSpeechBaseURL
	volcengineSpeechBaseURL = server.URL
	t.Cleanup(func() { volcengineSpeechBaseURL = original })

	view := connectivityView("https://ark.cn-beijing.volces.com/api/v3", ModeAudioSpeech, "seed-audio-1.0")
	view.ProviderCode = "volcengine"
	view.Credentials = map[string]any{"speech_token": "speech-token-1"}
	observation, err := newTestService(t, &fakeSource{view: view}).Probe(context.Background(), accountquality.ProbeRequest{AccountID: "acc-1", Full: true})
	if err != nil {
		t.Fatal(err)
	}
	if !observation.Result.Success {
		t.Fatalf("火山语音连通性应通过：%+v", observation.Result)
	}
	if capture.method != http.MethodPost || capture.path != "/api/v3/tts/create" {
		t.Fatalf("request = %s %s", capture.method, capture.path)
	}
	if capture.apiKey != "speech-token-1" {
		t.Fatalf("X-Api-Key = %q", capture.apiKey)
	}
	if capture.auth != "" {
		t.Fatalf("语音域不得走 Authorization: %q", capture.auth)
	}
	if capture.body != `{"model":"seed-audio-1.0"}` {
		t.Fatalf("body = %q", capture.body)
	}
}

// TestConnectivityProbeLimitedMaskMessage：limited 诊断下的失败文案脱敏沿用
// 既有规则（上游请求失败）。
func TestConnectivityProbeLimitedMaskMessage(t *testing.T) {
	ctx := context.Background()
	server := newConnectivityServer(t, http.StatusInternalServerError, `{"error":{"message":"internal"}}`, nil)
	defer server.Close()
	view := connectivityView(server.URL, ModeVideoCreate, "video-model")
	service := newTestService(t, &fakeSource{view: view})
	observation, err := service.Probe(ctx, accountquality.ProbeRequest{AccountID: "acc-1", Full: false})
	if err != nil || observation == nil {
		t.Fatalf("probe: %+v %v", observation, err)
	}
	if observation.Result.Success {
		t.Fatalf("limited 500 应失败：%+v", observation.Result)
	}
	if observation.Result.Message != "上游请求失败" {
		t.Fatalf("limited message = %q", observation.Result.Message)
	}
	if !observation.Evidence.Connectivity {
		t.Fatal("evidence 必须带 connectivity 标记")
	}
}

// TestUtilityConnectivityRequestShapes 锁定 B7 工具账户人工连通性测试：
// 空输入 POST 到同族端点，qwen 走 compatible-mode / DashScope 原生路径，
// 历史版本根先剥离，不产生真实向量/重排/审核结果。
func TestUtilityConnectivityRequestShapes(t *testing.T) {
	cases := []struct {
		name       string
		provider   string
		baseSuffix string
		mode       EndpointMode
		model      string
		wantPath   string
		wantBody   []string
	}{
		{
			name: "gpt embeddings", provider: "gpt", mode: ModeEmbedContent, model: "text-embedding-3-small",
			wantPath: "/embeddings", wantBody: []string{`"model":"text-embedding-3-small"`, `"input":[]`},
		},
		{
			name: "qwen embeddings strips compatible-mode root", provider: "qwen", baseSuffix: "/compatible-mode/v1",
			mode: ModeEmbedContent, model: "text-embedding-v4",
			wantPath: "/compatible-mode/v1/embeddings", wantBody: []string{`"model":"text-embedding-v4"`, `"input":[]`},
		},
		{
			name: "qwen rerank", provider: "qwen", mode: ModeRerankJSON, model: "qwen3.7-text-rerank",
			wantPath: "/api/v1/services/rerank/text-rerank/text-rerank",
			wantBody: []string{`"model":"qwen3.7-text-rerank"`, `"input":{"documents":[],"query":""}`},
		},
		{
			name: "gpt moderation", provider: "gpt", mode: ModeModerationJSON, model: "omni-moderation-latest",
			wantPath: "/moderations", wantBody: []string{`"model":"omni-moderation-latest"`, `"input":[]`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			view := connectivityView("https://upstream.example"+tc.baseSuffix, tc.mode, tc.model)
			view.ProviderCode = tc.provider
			req, err := buildConnectivityRequest(context.Background(), view, tc.mode, tc.model, "sk-test")
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if req.Method != http.MethodPost || req.URL.Path != tc.wantPath {
				t.Fatalf("request = %s %s, want POST %s", req.Method, req.URL.Path, tc.wantPath)
			}
			raw, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			for _, fragment := range tc.wantBody {
				if !strings.Contains(string(raw), fragment) {
					t.Fatalf("body = %s, want %s", raw, fragment)
				}
			}
		})
	}
}

func TestUtilityConnectivityModeRecognized(t *testing.T) {
	for _, mode := range []EndpointMode{ModeEmbedContent, ModeRerankJSON, ModeModerationJSON} {
		if !isConnectivityProbeMode(mode) {
			t.Fatalf("%s 必须走连通性探测", mode)
		}
		if !connectivityExpectedRejectionCodes(mode)[http.StatusBadRequest] {
			t.Fatalf("%s 必须把 400 视为可达", mode)
		}
		if !connectivityExpectedRejectionCodes(mode)[http.StatusUnprocessableEntity] {
			t.Fatalf("%s 必须把 422 视为可达", mode)
		}
	}
}

func TestResolveUtilityEndpointMode(t *testing.T) {
	view := &View{
		Type:                    "api_key",
		ProtocolCode:            "openai",
		HealthCheckEndpointMode: string(ModeEmbedContent),
		NormalizeEndpointModes:  map[EndpointMode]bool{ModeEmbedContent: true},
	}
	got, err := resolveEndpointMode(view, ModeEmbedContent)
	if err != nil || got != ModeEmbedContent {
		t.Fatalf("resolveEndpointMode = %q, %v, want embed_content", got, err)
	}
}
