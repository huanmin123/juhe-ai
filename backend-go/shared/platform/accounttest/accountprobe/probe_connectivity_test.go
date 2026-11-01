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
		// audio_speech：空体 POST /tts/create 同款分类。
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

	t.Run("audio_speech 空体 POST tts/create", func(t *testing.T) {
		capture := &connectivityCapture{}
		server := newConnectivityServer(t, http.StatusBadRequest, `{}`, capture)
		defer server.Close()
		view := connectivityView(server.URL, ModeAudioSpeech, "tts-model")
		if _, err := newTestService(t, &fakeSource{view: view}).Probe(ctx, accountquality.ProbeRequest{AccountID: "acc-1", Full: true}); err != nil {
			t.Fatal(err)
		}
		if capture.method != http.MethodPost || capture.path != "/tts/create" {
			t.Fatalf("request = %s %s", capture.method, capture.path)
		}
		if capture.body != "" {
			t.Fatalf("audio 连通性探测应为空体：%q", capture.body)
		}
	})
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
