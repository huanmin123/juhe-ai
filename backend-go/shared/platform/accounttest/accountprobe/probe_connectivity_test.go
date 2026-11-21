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

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
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
	view.ConnectionPresetCode = contracts.PresetGPTOpenAIV1
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
		if _, err := newTestService(t, &fakeSource{view: view}).Probe(ctx, accountquality.ProbeRequest{AccountID: "acc-1", Full: true}); err != nil {
			t.Fatal(err)
		}
		if capture.method != http.MethodGet {
			t.Fatalf("method = %s, want GET", capture.method)
		}
		if capture.path != "/api/v3/videos/"+ConnectivityProbeTaskID {
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
		if capture.body != `{"model":"tts-model"}` {
			t.Fatalf("audio 连通性探测应携带 model：%q", capture.body)
		}
	})
}

// TestConnectivityProbeVolcengineSpeech：火山语音域是固定上游（openspeech +
// /api/v3/tts/create + X-Api-Key speech_token），账户 base_url（ark /api/v3）
// 不参与拼接；body 必须带 model（缺 model 会被 45000030 挡在 resource_id
// 解析层，无法证明密钥有效）。固定域无法用 httptest 重定向，这里直接断言
// 构造出的出站请求；分类表由通用 OpenAI 规格覆盖。
func TestConnectivityProbeVolcengineSpeech(t *testing.T) {
	view := connectivityView("https://ark.cn-beijing.volces.com/api/v3", ModeAudioSpeech, "seed-audio-1.0")
	view.ProviderCode = "volcengine"
	view.ConnectionPresetCode = contracts.PresetVolcengineOpenSpeech
	view.Credentials = map[string]any{"speech_token": "speech-token-1"}

	spec, ok := contracts.ConnectivityProbeForPreset(view.ConnectionPresetCode, string(ModeAudioSpeech))
	if !ok {
		t.Fatal("火山语音规格未登记")
	}
	request, err := buildConnectivityRequest(context.Background(), view, spec, view.HealthCheckModel, "api-key-not-used")
	if err != nil {
		t.Fatal(err)
	}
	if request.Method != http.MethodPost || request.URL.String() != "https://openspeech.bytedance.com/api/v3/tts/create" {
		t.Fatalf("request = %s %s", request.Method, request.URL.String())
	}
	if request.Header.Get("X-Api-Key") != "speech-token-1" {
		t.Fatalf("X-Api-Key = %q", request.Header.Get("X-Api-Key"))
	}
	if request.Header.Get("Authorization") != "" {
		t.Fatalf("语音域不得走 Authorization: %q", request.Header.Get("Authorization"))
	}
	raw, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"model":"seed-audio-1.0"}` {
		t.Fatalf("body = %q", raw)
	}
}

// TestConnectivityProbeVolcengineSpeechMissingToken：规格显式要求的
// speech_token 缺失时必须构造失败（由 executeConnectivityAttempt 归类为失败
// 观测），不得回落到 Bearer 认证把 api_key 发到语音域。
func TestConnectivityProbeVolcengineSpeechMissingToken(t *testing.T) {
	view := connectivityView("https://ark.cn-beijing.volces.com/api/v3", ModeAudioSpeech, "seed-audio-1.0")
	view.ProviderCode = "volcengine"
	view.ConnectionPresetCode = contracts.PresetVolcengineOpenSpeech
	view.Credentials = map[string]any{}

	spec, ok := contracts.ConnectivityProbeForPreset(view.ConnectionPresetCode, string(ModeAudioSpeech))
	if !ok {
		t.Fatal("火山语音规格未登记")
	}
	if _, err := buildConnectivityRequest(context.Background(), view, spec, view.HealthCheckModel, "api-key"); err == nil {
		t.Fatal("缺少 speech_token 时应构造失败")
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

// TestConnectivityProbeVolcengineAUCHeaders 锁定 AUC 查询请求的资源头三件套：
// 缺 X-Api-Resource-Id 时上游可能 200 + 业务错误，连接测试不能只看状态码。
func TestConnectivityProbeVolcengineAUCHeaders(t *testing.T) {
	view := connectivityView("https://ark.cn-beijing.volces.com/api/v3", ModeAudioTranscriptionJSON, "volc.seedasr.auc")
	view.ProviderCode = "volcengine"
	view.ConnectionPresetCode = contracts.PresetVolcengineOpenSpeech
	view.Credentials = map[string]any{"speech_token": "speech-token-1"}

	spec, ok := contracts.ConnectivityProbeForPreset(view.ConnectionPresetCode, string(ModeAudioTranscriptionJSON))
	if !ok {
		t.Fatal("火山 AUC 规格未登记")
	}
	request, err := buildConnectivityRequest(context.Background(), view, spec, view.HealthCheckModel, "api-key-not-used")
	if err != nil {
		t.Fatal(err)
	}
	if request.Method != http.MethodPost || request.URL.String() != "https://openspeech.bytedance.com/api/v3/auc/bigmodel/query" {
		t.Fatalf("request = %s %s", request.Method, request.URL.String())
	}
	if request.Header.Get("X-Api-Key") != "speech-token-1" {
		t.Fatalf("X-Api-Key = %q", request.Header.Get("X-Api-Key"))
	}
	if request.Header.Get("X-Api-Resource-Id") != "volc.seedasr.auc" {
		t.Fatalf("X-Api-Resource-Id = %q", request.Header.Get("X-Api-Resource-Id"))
	}
	if id := request.Header.Get("X-Api-Request-Id"); len(id) != 36 || strings.Count(id, "-") != 4 || id == ConnectivityProbeTaskID {
		t.Fatalf("X-Api-Request-Id 必须是独立 UUID：%q", id)
	}
	if request.Header.Get("X-Api-Sequence") != "-1" {
		t.Fatalf("X-Api-Sequence = %q", request.Header.Get("X-Api-Sequence"))
	}
}

// TestConnectivityProbeOnlyContentMode 确保只声明 content/cancel 的账户也能
// 选择到规格，不会因 mode 不在旧规格表而报“没有可用请求形态”。
func TestConnectivityProbeOnlyContentMode(t *testing.T) {
	cases := []struct {
		preset string
		mode   EndpointMode
		method string
		path   string
	}{
		{contracts.PresetQwenDashScope, ModeVideoContent, http.MethodGet, "/api/v1/tasks/" + ConnectivityProbeTaskID},
		{contracts.PresetQwenDashScope, ModeVideoCancel, http.MethodGet, "/api/v1/tasks/" + ConnectivityProbeTaskID},
		{contracts.PresetQwenDashScope, ModeAudioJobContent, http.MethodGet, "/api/v1/tasks/" + ConnectivityProbeTaskID},
		{contracts.PresetQwenDashScope, ModeAudioJobCancel, http.MethodGet, "/api/v1/tasks/" + ConnectivityProbeTaskID},
		{contracts.PresetMiniMaxMedia, ModeVideoCancel, http.MethodGet, "/query/video_generation"},
		{contracts.PresetVolcengineArk, ModeVideoContent, http.MethodGet, "/contents/generations/tasks/" + ConnectivityProbeTaskID},
		{contracts.PresetGeminiNativeV1Beta, ModeVideoContent, http.MethodGet, "/interactions/" + ConnectivityProbeTaskID},
	}
	for _, tc := range cases {
		view := connectivityView("https://upstream.example.com", tc.mode, "media-model")
		view.ConnectionPresetCode = tc.preset
		mode, spec, ok := ConnectivityProbeModeForView(view)
		if !ok || mode != tc.mode {
			t.Fatalf("%s/%s 未命中探测规格：mode=%s ok=%v", tc.preset, tc.mode, mode, ok)
		}
		request, err := buildConnectivityRequest(context.Background(), view, spec, view.HealthCheckModel, "api-key")
		if err != nil {
			t.Fatal(err)
		}
		if request.Method != tc.method || request.URL.Path != tc.path {
			t.Fatalf("%s/%s request = %s %s, want %s %s", tc.preset, tc.mode, request.Method, request.URL.Path, tc.method, tc.path)
		}
	}
}

// TestConnectivityProbeModeForViewUsesDomainLaneModes 钉值五域退役读侧口径
// （AI 账户上游接口能力设计 §5）：五域账户连接测试按域默认形态集
// （capabilitydomain.LaneModesForDomain）经注册表规格优先级命中——audio 域
// qwen→audio_job_create、volcengine openspeech / minimax / gemini native→
// audio_speech；image/video/embedding/rerank 各落单值 lane；五域即使残留旧
// 声明词面也按域判定；chat / 空域不受 lane 影响，仍走既有声明集 → health 列
// 回落链（空声明 fail closed）。
func TestConnectivityProbeModeForViewUsesDomainLaneModes(t *testing.T) {
	cases := []struct {
		name      string
		domain    string
		preset    string
		modes     []EndpointMode // 既有声明集（模拟残留词面；nil = 声明恒空）
		healthCol string
		want      EndpointMode
		wantOK    bool
	}{
		{"qwen audio 走长音频任务提交", "audio", contracts.PresetQwenDashScope, nil, "", ModeAudioJobCreate, true},
		{"volcengine openspeech audio 走语音合成", "audio", contracts.PresetVolcengineOpenSpeech, nil, "", ModeAudioSpeech, true},
		{"minimax audio 走语音合成", "audio", contracts.PresetMiniMaxMedia, nil, "", ModeAudioSpeech, true},
		{"gemini native audio 走语音合成", "audio", contracts.PresetGeminiNativeV1Beta, nil, "", ModeAudioSpeech, true},
		{"image 走生图形态", "image", contracts.PresetGPTOpenAIV1, nil, "", ModeImagesJSON, true},
		{"video 走任务查询", "video", contracts.PresetVolcengineArk, nil, "", ModeVideoCreate, true},
		{"embedding 走向量形态", "embedding", contracts.PresetQwenDashScope, nil, "", ModeEmbeddingsJSON, true},
		{"rerank 走重排形态", "rerank", contracts.PresetQwenDashScope, nil, "", ModeRerankJSON, true},
		// 五域残留旧声明词面仍按域判定（不猜 modes）。
		{"video 残留 chat 声明仍按域", "video", contracts.PresetVolcengineArk, []EndpointMode{ModeCountTokens}, "chat_sse", ModeVideoCreate, true},
		{"audio 残留生图声明仍按域", "audio", contracts.PresetVolcengineOpenSpeech, []EndpointMode{ModeImagesJSON}, "images_json", ModeAudioSpeech, true},
		// chat / 空域不受 lane 影响：既有声明集命中 count_tokens（gemini native
		// 注册该形态）；全空 fail closed；空域回落 health 列。
		{"chat 声明集命中不落 lane", "chat", contracts.PresetGeminiNativeV1Beta, []EndpointMode{ModeCountTokens}, "", ModeCountTokens, true},
		{"chat 空声明 fail closed", "chat", contracts.PresetGPTOpenAIV1, nil, "", "", false},
		{"空域走既有回落链", "", contracts.PresetGeminiNativeV1Beta, nil, string(ModeCountTokens), ModeCountTokens, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			view := connectivityView("https://upstream.example.com", ModeImagesJSON, "probe-model")
			view.CapabilityDomain = tc.domain
			view.ConnectionPresetCode = tc.preset
			view.HealthCheckEndpointMode = tc.healthCol
			view.NormalizeEndpointModes = map[EndpointMode]bool{}
			for _, mode := range tc.modes {
				view.NormalizeEndpointModes[mode] = true
			}
			mode, _, ok := ConnectivityProbeModeForView(view)
			if ok != tc.wantOK || mode != tc.want {
				t.Fatalf("domain=%q preset=%s modes=%v → mode=%q ok=%v, want %q %v",
					tc.domain, tc.preset, tc.modes, mode, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestConnectivityProbeQwenMediaStripsBaseServiceRoot（T221）：qwen 媒体探测
// opPath 是 DashScope 全路径（/api/v1/tasks/{probe_id}），base 尾部服务根
// （存量行/凭据域形态）必须先剥一次再直拼，与 gateway 媒体链同构——双根
// 出站会把有效密钥误判为不可达；非 qwen 供应商不受影响。
func TestConnectivityProbeQwenMediaStripsBaseServiceRoot(t *testing.T) {
	spec, ok := contracts.ConnectivityProbeForPreset(contracts.PresetQwenDashScope, string(ModeVideoGet))
	if !ok {
		t.Fatal("qwen 视频查询规格未登记")
	}
	cases := []struct {
		name, base, want string
	}{
		{
			name: "official origin stays untouched",
			base: "https://dashscope.aliyuncs.com",
			want: "https://dashscope.aliyuncs.com/api/v1/tasks/" + ConnectivityProbeTaskID,
		},
		{
			name: "compatible-mode suffix stripped once",
			base: "https://dashscope.aliyuncs.com/compatible-mode/v1",
			want: "https://dashscope.aliyuncs.com/api/v1/tasks/" + ConnectivityProbeTaskID,
		},
		{
			name: "api-v1 suffix stripped once",
			base: "https://dashscope.aliyuncs.com/api/v1/",
			want: "https://dashscope.aliyuncs.com/api/v1/tasks/" + ConnectivityProbeTaskID,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			view := connectivityView(tc.base, ModeVideoGet, "wan2.2-t2v-plus")
			view.ProviderCode = "qwen"
			view.ConnectionPresetCode = contracts.PresetQwenDashScope
			request, err := buildConnectivityRequest(context.Background(), view, spec, view.HealthCheckModel, "api-key-not-used")
			if err != nil {
				t.Fatal(err)
			}
			if request.URL.String() != tc.want {
				t.Fatalf("qwen 媒体探测 URL = %q, want %q", request.URL.String(), tc.want)
			}
		})
	}

	// 非 qwen：base 直通（带 /v1 尾根不被剥），保证剥根只作用于 qwen。
	view := connectivityView("https://api.openai.com/v1", ModeVideoCreate, "sora-2")
	view.ProviderCode = "gpt"
	view.ConnectionPresetCode = contracts.PresetGPTOpenAIV1
	spec, ok = contracts.ConnectivityProbeForPreset(contracts.PresetGPTOpenAIV1, string(ModeVideoCreate))
	if !ok {
		t.Fatal("openai 视频创建规格未登记")
	}
	request, err := buildConnectivityRequest(context.Background(), view, spec, view.HealthCheckModel, "api-key-not-used")
	if err != nil {
		t.Fatal(err)
	}
	if request.URL.String() != "https://api.openai.com/v1/videos/"+ConnectivityProbeTaskID {
		t.Fatalf("非 qwen 探测不得剥根: %q", request.URL.String())
	}
}
