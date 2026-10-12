package studio

// generations/tasks 第二批单测（TestStudio* 入口，匹配 -run 'Studio|ChainStudio'）：
// operation_id 幂等（重复提交同 ID 返回原任务且上游只调一次）、图像受理即结算
// 路径（产物落库 + 配额 + 结算幂等 DB 兜底）、视频受理边界（确立前 4xx 短路 /
// 确立后 in_progress 三元组落定）、TTS/STT 收据幂等、取消四态、域未授权 403、
// 账户越范围 403、模型域不匹配 400、归属 404。Mock 内部 /v1 = httptest 上游
// + 进程内 executor 适配（记录调用与审计头）。

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordedCall 记录一次进程内 /v1 派发（路径/方法/审计头/Bearer/账户收敛目标/体）。
type recordedCall struct {
	Method          string
	Path            string
	Authorization   string
	Purpose         string
	DispatchAccount string
	Body            []byte
	ContentType     string
}

// fakeGateway 是 Mock 内部 /v1：httptest 上游 + executor 适配，场景按字段注
// 入（默认全 2xx）。图像面支持门控（取消四态 a 态的在途中止验证）。
type fakeGateway struct {
	t      *testing.T
	server *httptest.Server

	mu               sync.Mutex
	calls            []recordedCall
	dispatchAccounts []string

	imagesStatus int
	imagesBody   string
	videoStatus  int
	videoBody    string
	speechBody   []byte
	sttBody      string

	// imagesHold 为真时图像处理器在 entered 门后等待 gate（取消四态 a 态的
	// 在途中止验证）；默认立即响应。
	imagesHold bool

	// 图像门控：entered 在处理器进入时关闭；gate 关闭后放行响应。
	imagesEntered chan struct{}
	imagesGate    chan struct{}
	imagesDone    chan struct{}
}

func newFakeGateway(t *testing.T) *fakeGateway {
	gateway := &fakeGateway{
		t:             t,
		imagesEntered: make(chan struct{}),
		imagesGate:    make(chan struct{}),
		imagesDone:    make(chan struct{}),
	}
	gateway.server = httptest.NewServer(http.HandlerFunc(gateway.serve))
	t.Cleanup(gateway.server.Close)
	return gateway
}

// executor 适配 studio.GatewayExecutor（组合根 studioGatewayExecutor 的测试
// 同构：缓冲读 + 透传头；DispatchAccount 在生产实现注入进程内 context，测试
// 记录供断言）。
func (g *fakeGateway) executor() *fakeGatewayExecutor {
	return &fakeGatewayExecutor{gateway: g}
}

type fakeGatewayExecutor struct {
	gateway *fakeGateway
}

func (e *fakeGatewayExecutor) Dispatch(ctx context.Context, req GatewayDispatchRequest) (*GatewayDispatchResponse, error) {
	e.gateway.mu.Lock()
	e.gateway.dispatchAccounts = append(e.gateway.dispatchAccounts, req.DispatchAccount)
	e.gateway.mu.Unlock()
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, e.gateway.server.URL+req.Path, bytes.NewReader(req.Body))
	if err != nil {
		return nil, err
	}
	for key, value := range req.Headers {
		httpReq.Header.Set(key, value)
	}
	response, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	return &GatewayDispatchResponse{Status: response.StatusCode, Header: response.Header, Body: body}, nil
}

func (g *fakeGateway) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	g.mu.Lock()
	g.calls = append(g.calls, recordedCall{
		Method:        r.Method,
		Path:          r.URL.Path,
		Authorization: r.Header.Get("Authorization"),
		Purpose:       r.Header.Get(StudioPurposeHeader),
		Body:          body,
		ContentType:   r.Header.Get("Content-Type"),
	})
	g.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/images/generations":
		close(g.imagesEntered)
		defer close(g.imagesDone)
		if g.imagesHold {
			select {
			case <-g.imagesGate:
			case <-r.Context().Done():
				return
			}
		}
		g.mu.Lock()
		status, payload := g.imagesStatus, g.imagesBody
		g.mu.Unlock()
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(payload))
	case r.Method == http.MethodPost && r.URL.Path == "/v1/videos":
		g.mu.Lock()
		status, payload := g.videoStatus, g.videoBody
		g.mu.Unlock()
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(payload))
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/videos/"):
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/audio/speech":
		g.mu.Lock()
		payload := g.speechBody
		g.mu.Unlock()
		if payload == nil {
			payload = studioTestWAV()
		}
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(payload)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/audio/transcriptions":
		g.mu.Lock()
		payload := g.sttBody
		g.mu.Unlock()
		if payload == "" {
			payload = `{"text":"你好世界"}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"资源不存在"}`))
	}
}

func (g *fakeGateway) recorded() []recordedCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]recordedCall(nil), g.calls...)
}

func (g *fakeGateway) accounts() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.dispatchAccounts...)
}

func (g *fakeGateway) setImages(status int, body string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.imagesStatus, g.imagesBody = status, body
}

func (g *fakeGateway) setVideo(status int, body string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.videoStatus, g.videoBody = status, body
}

// waitForTaskStatus 轮询任务直到目标状态（后台执行体异步收口）。
func waitForTaskStatus(t *testing.T, env *studioTestEnv, taskID, ownerID, status string) *Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		task, err := env.store.GetTask(taskID, ownerID)
		if err != nil {
			t.Fatalf("get task: %v", err)
		}
		if task != nil && task.Status == status {
			return task
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("task %s 未在时限内进入 %s", taskID, status)
	return nil
}

// studioTestWAV 构造可被嗅探的最小 WAV 字节（RIFF....WAVE）。
func studioTestWAV() []byte {
	payload := []byte("RIFF\x24\x00\x00\x00WAVEfmt \x10\x00\x00\x00\x01\x00\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00data\x00\x00\x00\x00")
	return append(payload, bytes.Repeat([]byte{0x01}, 64)...)
}

// newGenerationTestEnv 在资产测试环境上装配生成端点依赖：Mock /v1、账户候选、
// 域模型目录、studio Key stub。
func newGenerationTestEnv(t *testing.T) (*studioTestEnv, *fakeGateway) {
	t.Helper()
	env := newStudioTestEnv(t)
	gateway := newFakeGateway(t)
	env.deps.Executor = gateway.executor()
	env.deps.AccountOptions = &stubAccounts{rows: []StudioAccountOption{
		{AccountID: "acc_img", Name: "图像号", ProviderCode: "openai", Domain: "image", Status: "active"},
		{AccountID: "acc_vid", Name: "视频号", ProviderCode: "volcengine", Domain: "video", Status: "active"},
		{AccountID: "acc_audio", Name: "音频号", ProviderCode: "volcengine", Domain: "audio", Status: "active"},
	}}
	env.deps.Models = &stubModels{rows: []StudioModel{
		{Model: "gpt-image-1", ProviderCode: "openai", Domain: "image", EndpointModes: []string{"images_json"}, Capabilities: map[string]any{}},
		{Model: "seedance-pro", ProviderCode: "volcengine", Domain: "video", EndpointModes: []string{"video_create"}, Capabilities: map[string]any{}},
		{Model: "cosyvoice-v2", ProviderCode: "volcengine", Domain: "audio", EndpointModes: []string{"audio_speech"}, Capabilities: map[string]any{}},
		{Model: "whisper-large", ProviderCode: "volcengine", Domain: "audio", EndpointModes: []string{"audio_transcription"}, Capabilities: map[string]any{}},
	}}
	return env, gateway
}

func studioJSONRequest(t *testing.T, path string, payload map[string]any) *http.Request {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func studioInvokeJSON(rt *studioRoutes, r *http.Request, owner string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	rt.wrap(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/generations/images":
			rt.generationImages(w, req)
		case "/generations/videos":
			rt.generationVideos(w, req)
		case "/generations/speech":
			rt.generationSpeech(w, req)
		default:
			rt.generationTranscriptions(w, req)
		}
	})).ServeHTTP(recorder, studioAuthed(r, owner))
	return recorder
}

func studioDecodeData(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode data: %v body=%s", err, recorder.Body.String())
	}
	return payload.Data
}

// --- 图像：受理即结算路径 + 幂等 ---

func TestStudioGenerationImagesAcceptsAndSettles(t *testing.T) {
	env, gateway := newGenerationTestEnv(t)
	pngB64 := base64.StdEncoding.EncodeToString(studioPNG(t, 3, 2))
	gateway.setImages(http.StatusOK, fmt.Sprintf(`{"created":1,"data":[{"b64_json":%q}]}`, pngB64))

	recorder := studioInvokeJSON(env.routes, studioJSONRequest(t, "/generations/images", map[string]any{
		"accountId":         "acc_img",
		"model":             "gpt-image-1",
		"prompt":            "一只在月球上的猫",
		"clientOperationId": "op-img-1",
	}), studioTestOwner)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("images accept status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	data := studioDecodeData(t, recorder)
	taskID, _ := data["id"].(string)
	if !strings.HasPrefix(taskID, "stask_") {
		t.Fatalf("task id = %v, want stask_ prefix", data["id"])
	}
	if data["status"] != "queued" || data["settlement"] != "pending_settled" {
		t.Fatalf("accepted task = %+v", data)
	}
	if data["model"] != "gpt-image-1" || data["clientOperationId"] != "op-img-1" {
		t.Fatalf("task dto = %+v", data)
	}
	task := waitForTaskStatus(t, env, taskID, studioTestOwner, "completed")
	if task.Settlement != "settled" || task.AssetID == nil {
		t.Fatalf("settled task = %+v", task)
	}
	asset, err := env.store.GetAsset(*task.AssetID, studioTestOwner)
	if err != nil || asset == nil {
		t.Fatalf("settle asset = %v, %v", asset, err)
	}
	if asset.Kind != "image" || asset.SourceTaskID == nil || *asset.SourceTaskID != taskID {
		t.Fatalf("asset = %+v", asset)
	}
	if asset.Width == nil || *asset.Width != 3 {
		t.Fatalf("asset width = %v", asset.Width)
	}
	// 配额入账 + 上游恰好一次 + 审计头/Bearer/账户收敛。
	if bytes_, count := studioUsageOf(t, env.db, studioTestOwner); count != 1 || bytes_ != asset.Bytes {
		t.Fatalf("usage = %d/%d, want %d/1", bytes_, count, asset.Bytes)
	}
	calls := gateway.recorded()
	if len(calls) != 1 {
		t.Fatalf("upstream calls = %d, want 1", len(calls))
	}
	call := calls[0]
	if call.Path != "/v1/images/generations" || call.Purpose != PurposeStudioImage {
		t.Fatalf("call = %+v", call)
	}
	if call.Authorization != "Bearer sk-studio-key_stub" {
		t.Fatalf("authorization = %q", call.Authorization)
	}
	// 账户收敛目标经执行器注入（§4.5.3）。
	if accounts := gateway.accounts(); len(accounts) != 1 || accounts[0] != "acc_img" {
		t.Fatalf("dispatch accounts = %v, want [acc_img]", accounts)
	}
	// GET /tasks/{id}：settled 且资产存在 → assetId 返回。
	request := httptest.NewRequest(http.MethodGet, "/tasks/"+taskID, nil)
	request.SetPathValue("taskId", taskID)
	recorder = httptest.NewRecorder()
	env.routes.getTask(recorder, studioAuthed(request, studioTestOwner))
	if recorder.Code != http.StatusOK {
		t.Fatalf("get task status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	dto := studioDecodeData(t, recorder)
	if dto["assetId"] != *task.AssetID {
		t.Fatalf("task dto assetId = %v, want %v", dto["assetId"], *task.AssetID)
	}
}

func TestStudioGenerationImagesIdempotentReplay(t *testing.T) {
	env, gateway := newGenerationTestEnv(t)
	pngB64 := base64.StdEncoding.EncodeToString(studioPNG(t, 2, 2))
	gateway.setImages(http.StatusOK, fmt.Sprintf(`{"data":[{"b64_json":%q}]}`, pngB64))
	payload := map[string]any{
		"accountId":         "acc_img",
		"model":             "gpt-image-1",
		"prompt":            "重复提交",
		"clientOperationId": "op-img-replay",
	}
	first := studioInvokeJSON(env.routes, studioJSONRequest(t, "/generations/images", payload), studioTestOwner)
	if first.Code != http.StatusCreated {
		t.Fatalf("first status = %d body=%s", first.Code, first.Body.String())
	}
	firstTask := studioDecodeData(t, first)
	waitForTaskStatus(t, env, firstTask["id"].(string), studioTestOwner, "completed")

	second := studioInvokeJSON(env.routes, studioJSONRequest(t, "/generations/images", payload), studioTestOwner)
	if second.Code != http.StatusOK {
		t.Fatalf("replay status = %d body=%s, want 200", second.Code, second.Body.String())
	}
	if !strings.Contains(second.Body.String(), "已返回既有任务") {
		t.Fatalf("replay body missing message: %s", second.Body.String())
	}
	replayTask := studioDecodeData(t, second)
	if replayTask["id"] != firstTask["id"] {
		t.Fatalf("replay id = %v, want %v", replayTask["id"], firstTask["id"])
	}
	if calls := gateway.recorded(); len(calls) != 1 {
		t.Fatalf("upstream calls = %d, want 1（同 ID 重放不再调用上游）", len(calls))
	}
}

func TestStudioGenerationImagesUpstreamFailure(t *testing.T) {
	env, gateway := newGenerationTestEnv(t)
	gateway.setImages(http.StatusBadRequest, `{"error":{"message":"尺寸参数不合法"}}`)
	recorder := studioInvokeJSON(env.routes, studioJSONRequest(t, "/generations/images", map[string]any{
		"accountId":         "acc_img",
		"model":             "gpt-image-1",
		"prompt":            "失败路径",
		"clientOperationId": "op-img-fail",
	}), studioTestOwner)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("accept status = %d", recorder.Code)
	}
	taskID := studioDecodeData(t, recorder)["id"].(string)
	task := waitForTaskStatus(t, env, taskID, studioTestOwner, "failed")
	if task.Settlement != "settled" {
		t.Fatalf("failed task settlement = %q, want settled（无产物可结算）", task.Settlement)
	}
	taskError := taskErrorOf(task.ErrorJSON)
	if taskError == nil || taskError.Code != "studio_upstream_failed" || !strings.Contains(taskError.Message, "尺寸参数不合法") {
		t.Fatalf("task error = %+v", taskError)
	}
	if _, count := studioUsageOf(t, env.db, studioTestOwner); count != 0 {
		t.Fatalf("usage count = %d, want 0（失败不落资产）", count)
	}
}

// --- 视频：受理边界 ---

func TestStudioGenerationVideosAcceptBoundary(t *testing.T) {
	t.Run("确立后 in_progress 三元组落定", func(t *testing.T) {
		env, gateway := newGenerationTestEnv(t)
		gateway.setVideo(http.StatusOK, `{"id":"mj_video_1","status":"queued","provider":"volcengine"}`)
		recorder := studioInvokeJSON(env.routes, studioJSONRequest(t, "/generations/videos", map[string]any{
			"accountId":         "acc_vid",
			"model":             "seedance-pro",
			"prompt":            "城市延时摄影",
			"clientOperationId": "op-vid-1",
		}), studioTestOwner)
		if recorder.Code != http.StatusCreated {
			t.Fatalf("video accept status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		dto := studioDecodeData(t, recorder)
		if dto["status"] != "in_progress" {
			t.Fatalf("accepted video status = %v", dto["status"])
		}
		if dto["accountId"] != "acc_vid" || dto["providerCode"] != "volcengine" {
			t.Fatalf("affinity triple = %+v", dto)
		}
		task, err := env.store.GetTask(dto["id"].(string), studioTestOwner)
		if err != nil || task == nil {
			t.Fatalf("get task: %v, %v", task, err)
		}
		if task.UpstreamJobID == nil || *task.UpstreamJobID != "mj_video_1" {
			t.Fatalf("upstream job id = %v", task.UpstreamJobID)
		}
		calls := gateway.recorded()
		if len(calls) != 1 || calls[0].Path != "/v1/videos" || calls[0].Purpose != PurposeStudioVideo {
			t.Fatalf("calls = %+v", calls)
		}
	})

	t.Run("确定性 4xx 短路 + 任务 failed", func(t *testing.T) {
		env, gateway := newGenerationTestEnv(t)
		gateway.setVideo(http.StatusBadRequest, `{"error":{"message":"seconds 不支持"}}`)
		recorder := studioInvokeJSON(env.routes, studioJSONRequest(t, "/generations/videos", map[string]any{
			"accountId":         "acc_vid",
			"model":             "seedance-pro",
			"prompt":            "参数错误",
			"clientOperationId": "op-vid-4xx",
		}), studioTestOwner)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("4xx short-circuit status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		if _, code := studioErrorPayload(t, recorder); code != CodeInvalidRequest {
			t.Fatalf("4xx code = %q", code)
		}
		task, err := env.store.GetTaskByOperation(studioTestOwner, "op-vid-4xx")
		if err != nil || task == nil {
			t.Fatalf("task row = %v, %v", task, err)
		}
		if task.Status != "failed" || task.Settlement != "settled" {
			t.Fatalf("task = %s/%s, want failed/settled", task.Status, task.Settlement)
		}
		_ = gateway
	})

	t.Run("2xx 缺 job id 协议违约", func(t *testing.T) {
		env, gateway := newGenerationTestEnv(t)
		gateway.setVideo(http.StatusOK, `{"status":"queued"}`)
		recorder := studioInvokeJSON(env.routes, studioJSONRequest(t, "/generations/videos", map[string]any{
			"accountId":         "acc_vid",
			"model":             "seedance-pro",
			"prompt":            "无凭据",
			"clientOperationId": "op-vid-noid",
		}), studioTestOwner)
		if recorder.Code != http.StatusBadGateway {
			t.Fatalf("missing job id status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		if _, code := studioErrorPayload(t, recorder); code != CodeGenerationFailed {
			t.Fatalf("code = %q", code)
		}
		task, err := env.store.GetTaskByOperation(studioTestOwner, "op-vid-noid")
		if err != nil || task == nil || task.Status != "failed" {
			t.Fatalf("task = %v, %v", task, err)
		}
	})
}

// --- TTS：同步资产 + 收据幂等 ---

func TestStudioGenerationSpeechReceiptIdempotent(t *testing.T) {
	env, gateway := newGenerationTestEnv(t)
	payload := map[string]any{
		"accountId":         "acc_audio",
		"model":             "cosyvoice-v2",
		"input":             "欢迎使用媒体工作室",
		"clientOperationId": "op-tts-1",
	}
	first := studioInvokeJSON(env.routes, studioJSONRequest(t, "/generations/speech", payload), studioTestOwner)
	if first.Code != http.StatusOK {
		t.Fatalf("speech status = %d body=%s", first.Code, first.Body.String())
	}
	asset := studioDecodeData(t, first)
	if asset["kind"] != "audio" || !strings.HasPrefix(asset["id"].(string), "stasset_") {
		t.Fatalf("speech asset = %+v", asset)
	}
	params := asset["params"].(map[string]any)
	if params["model"] != "cosyvoice-v2" || params["input"] != "欢迎使用媒体工作室" {
		t.Fatalf("asset params = %+v", params)
	}

	second := studioInvokeJSON(env.routes, studioJSONRequest(t, "/generations/speech", payload), studioTestOwner)
	if second.Code != http.StatusOK {
		t.Fatalf("replay status = %d body=%s", second.Code, second.Body.String())
	}
	if !strings.Contains(second.Body.String(), "已返回既有结果") {
		t.Fatalf("replay body missing message: %s", second.Body.String())
	}
	replay := studioDecodeData(t, second)
	if replay["id"] != asset["id"] {
		t.Fatalf("replay asset id = %v, want %v", replay["id"], asset["id"])
	}
	if calls := gateway.recorded(); len(calls) != 1 {
		t.Fatalf("upstream calls = %d, want 1（收据命中不再调用上游）", len(calls))
	}
	if calls := gateway.recorded(); calls[0].Purpose != PurposeStudioAudio {
		t.Fatalf("purpose = %q", calls[0].Purpose)
	}
	// 收据保留而资产被删：同 ID 重放返回 404 studio_asset_not_found，不重新生成。
	op, err := env.store.GetOperation(studioTestOwner, "op-tts-1", OperationKindAudioSpeech)
	if err != nil || op == nil {
		t.Fatalf("receipt = %v, %v", op, err)
	}
	if _, err := env.store.DeleteAsset(*op.AssetID, studioTestOwner, studioTestNow); err != nil {
		t.Fatalf("delete asset: %v", err)
	}
	third := studioInvokeJSON(env.routes, studioJSONRequest(t, "/generations/speech", payload), studioTestOwner)
	if third.Code != http.StatusNotFound {
		t.Fatalf("deleted-asset replay status = %d body=%s", third.Code, third.Body.String())
	}
	if _, code := studioErrorPayload(t, third); code != CodeAssetNotFound {
		t.Fatalf("code = %q", code)
	}
	if calls := gateway.recorded(); len(calls) != 1 {
		t.Fatalf("upstream calls after deleted replay = %d, want 1", len(calls))
	}
}

// --- STT：multipart 透传 + 收据幂等 ---

func TestStudioGenerationTranscriptionsFlowAndReceipt(t *testing.T) {
	env, gateway := newGenerationTestEnv(t)
	buildRequest := func(opID string) *http.Request {
		var buffer bytes.Buffer
		writer := multipart.NewWriter(&buffer)
		_ = writer.WriteField("accountId", "acc_audio")
		_ = writer.WriteField("model", "whisper-large")
		_ = writer.WriteField("clientOperationId", opID)
		_ = writer.WriteField("language", "zh")
		part, _ := writer.CreateFormFile("file", "a.wav")
		_, _ = part.Write(studioTestWAV())
		_ = writer.Close()
		request := httptest.NewRequest(http.MethodPost, "/generations/transcriptions", &buffer)
		request.Header.Set("Content-Type", writer.FormDataContentType())
		return request
	}
	first := studioInvokeJSON(env.routes, buildRequest("op-stt-1"), studioTestOwner)
	if first.Code != http.StatusOK {
		t.Fatalf("stt status = %d body=%s", first.Code, first.Body.String())
	}
	data := studioDecodeData(t, first)
	if data["text"] != "你好世界" {
		t.Fatalf("stt result = %+v", data)
	}
	second := studioInvokeJSON(env.routes, buildRequest("op-stt-1"), studioTestOwner)
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), "已返回既有结果") {
		t.Fatalf("replay = %d %s", second.Code, second.Body.String())
	}
	calls := gateway.recorded()
	if len(calls) != 1 {
		t.Fatalf("upstream calls = %d, want 1", len(calls))
	}
	if calls[0].Purpose != PurposeStudioText || !strings.HasPrefix(calls[0].Path, "/v1/audio/transcriptions") {
		t.Fatalf("call = %+v", calls[0])
	}
	if !strings.Contains(calls[0].ContentType, "multipart/form-data") {
		t.Fatalf("forward content type = %q", calls[0].ContentType)
	}
	// 收据结果重放形状一致。
	replay := studioDecodeData(t, second)
	if replay["text"] != "你好世界" {
		t.Fatalf("replay result = %+v", replay)
	}
}

// --- 取消四态 ---

func TestStudioTaskCancelStates(t *testing.T) {
	t.Run("a 态：queued 未受理本地取消，不产生第二份产物", func(t *testing.T) {
		env, gateway := newGenerationTestEnv(t)
		gateway.mu.Lock()
		gateway.imagesHold = true
		gateway.mu.Unlock()
		pngB64 := base64.StdEncoding.EncodeToString(studioPNG(t, 4, 4))
		gateway.setImages(http.StatusOK, fmt.Sprintf(`{"data":[{"b64_json":%q}]}`, pngB64))
		recorder := studioInvokeJSON(env.routes, studioJSONRequest(t, "/generations/images", map[string]any{
			"accountId":         "acc_img",
			"model":             "gpt-image-1",
			"prompt":            "将被取消",
			"clientOperationId": "op-cancel-a",
		}), studioTestOwner)
		taskID := studioDecodeData(t, recorder)["id"].(string)
		// 等待后台执行体进入 /v1 调用（在途），再取消——尽力中止在途同步调用。
		select {
		case <-gateway.imagesEntered:
		case <-time.After(2 * time.Second):
			t.Fatalf("images dispatch 未进入")
		}
		request := httptest.NewRequest(http.MethodDelete, "/tasks/"+taskID, nil)
		request.SetPathValue("taskId", taskID)
		recorder = httptest.NewRecorder()
		env.routes.cancelTask(recorder, studioAuthed(request, studioTestOwner))
		if recorder.Code != http.StatusOK {
			t.Fatalf("cancel status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		dto := studioDecodeData(t, recorder)
		if dto["status"] != "cancelled" {
			t.Fatalf("cancelled dto = %+v", dto)
		}
		// 释放门控并等执行体退出：任务保持 cancelled，产物不落库、不复活。
		close(gateway.imagesGate)
		select {
		case <-gateway.imagesDone:
		case <-time.After(5 * time.Second):
			t.Fatalf("images dispatch 未退出")
		}
		task, err := env.store.GetTask(taskID, studioTestOwner)
		if err != nil || task == nil {
			t.Fatalf("get task: %v, %v", task, err)
		}
		if task.Status != "cancelled" || task.Settlement != "settled" {
			t.Fatalf("task after late success = %s/%s, want cancelled/settled", task.Status, task.Settlement)
		}
		var assetCount int
		if err := env.db.QueryRow(`SELECT COUNT(*) FROM studio_assets WHERE system_account_id = ?`, studioTestOwner).Scan(&assetCount); err != nil {
			t.Fatal(err)
		}
		if assetCount != 0 {
			t.Fatalf("asset rows = %d, want 0（取消后产物不落库）", assetCount)
		}
		if _, count := studioUsageOf(t, env.db, studioTestOwner); count != 0 {
			t.Fatalf("usage count = %d, want 0（账本一致）", count)
		}
	})

	t.Run("b 态：已受理尽力上游取消，不阻塞本地收口", func(t *testing.T) {
		env, gateway := newGenerationTestEnv(t)
		gateway.setVideo(http.StatusOK, `{"id":"mj_cancel_1","provider":"volcengine"}`)
		recorder := studioInvokeJSON(env.routes, studioJSONRequest(t, "/generations/videos", map[string]any{
			"accountId":         "acc_vid",
			"model":             "seedance-pro",
			"prompt":            "将被取消的视频",
			"clientOperationId": "op-cancel-b",
		}), studioTestOwner)
		taskID := studioDecodeData(t, recorder)["id"].(string)
		request := httptest.NewRequest(http.MethodDelete, "/tasks/"+taskID, nil)
		request.SetPathValue("taskId", taskID)
		recorder = httptest.NewRecorder()
		env.routes.cancelTask(recorder, studioAuthed(request, studioTestOwner))
		if recorder.Code != http.StatusOK {
			t.Fatalf("cancel status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		dto := studioDecodeData(t, recorder)
		if dto["status"] != "cancelled" || dto["settlement"] != "settled" {
			t.Fatalf("cancelled dto = %+v", dto)
		}
		calls := gateway.recorded()
		var cancelCall *recordedCall
		for index := range calls {
			if calls[index].Method == http.MethodDelete {
				cancelCall = &calls[index]
			}
		}
		if cancelCall == nil {
			t.Fatalf("upstream cancel 未发出: %+v", calls)
		}
		if cancelCall.Path != "/v1/videos/mj_cancel_1" || cancelCall.Purpose != PurposeStudioVideo {
			t.Fatalf("cancel call = %+v", cancelCall)
		}
		// 重复取消：幂等返回当前态。
		request = httptest.NewRequest(http.MethodDelete, "/tasks/"+taskID, nil)
		request.SetPathValue("taskId", taskID)
		recorder = httptest.NewRecorder()
		env.routes.cancelTask(recorder, studioAuthed(request, studioTestOwner))
		if recorder.Code != http.StatusOK {
			t.Fatalf("repeat cancel status = %d", recorder.Code)
		}
	})

	t.Run("c 态：completed 未结算 409，本地不删", func(t *testing.T) {
		env, _ := newGenerationTestEnv(t)
		if _, err := env.db.Exec(`INSERT INTO studio_tasks (id, system_account_id, client_operation_id, kind, status, settlement, created_at, updated_at)
			VALUES ('stask_c_state', ?, 'op-cancel-c', 'image', 'completed', 'pending_settled', ?, ?)`,
			studioTestOwner, studioTestNow, studioTestNow); err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodDelete, "/tasks/stask_c_state", nil)
		request.SetPathValue("taskId", "stask_c_state")
		recorder := httptest.NewRecorder()
		env.routes.cancelTask(recorder, studioAuthed(request, studioTestOwner))
		if recorder.Code != http.StatusConflict {
			t.Fatalf("c-state cancel status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		if _, code := studioErrorPayload(t, recorder); code != CodeTaskAlreadyCompleted {
			t.Fatalf("code = %q", code)
		}
		task, err := env.store.GetTask("stask_c_state", studioTestOwner)
		if err != nil || task == nil || task.Status != "completed" {
			t.Fatalf("task unchanged check = %v, %v", task, err)
		}
	})

	t.Run("终态幂等返回当前态", func(t *testing.T) {
		env, _ := newGenerationTestEnv(t)
		if _, err := env.db.Exec(`INSERT INTO studio_tasks (id, system_account_id, client_operation_id, kind, status, settlement, error_json, created_at, updated_at)
			VALUES ('stask_failed', ?, 'op-cancel-f', 'video', 'failed', 'settled', '{"code":"studio_upstream_failed","message":"失败"}', ?, ?)`,
			studioTestOwner, studioTestNow, studioTestNow); err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodDelete, "/tasks/stask_failed", nil)
		request.SetPathValue("taskId", "stask_failed")
		recorder := httptest.NewRecorder()
		env.routes.cancelTask(recorder, studioAuthed(request, studioTestOwner))
		if recorder.Code != http.StatusOK {
			t.Fatalf("terminal cancel status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		dto := studioDecodeData(t, recorder)
		if dto["status"] != "failed" {
			t.Fatalf("terminal dto = %+v", dto)
		}
		errorField, _ := dto["error"].(map[string]any)
		if errorField["code"] != "studio_upstream_failed" {
			t.Fatalf("dto error = %+v", errorField)
		}
	})
}

// --- 任务列表与归属 ---

func TestStudioTaskListAndOwnership(t *testing.T) {
	env, gateway := newGenerationTestEnv(t)
	gateway.setVideo(http.StatusOK, `{"id":"mj_list_1","provider":"volcengine"}`)
	recorder := studioInvokeJSON(env.routes, studioJSONRequest(t, "/generations/videos", map[string]any{
		"accountId": "acc_vid", "model": "seedance-pro", "prompt": "列表任务一", "clientOperationId": "op-list-1",
	}), studioTestOwner)
	taskOne := studioDecodeData(t, recorder)["id"].(string)
	recorder = studioInvokeJSON(env.routes, studioJSONRequest(t, "/generations/videos", map[string]any{
		"accountId": "acc_vid", "model": "seedance-pro", "prompt": "列表任务二", "clientOperationId": "op-list-2",
	}), studioTestOther)
	taskOther := studioDecodeData(t, recorder)["id"].(string)

	request := httptest.NewRequest(http.MethodGet, "/tasks?limit=1", nil)
	listRecorder := httptest.NewRecorder()
	env.routes.listTasks(listRecorder, studioAuthed(request, studioTestOwner))
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", listRecorder.Code, listRecorder.Body.String())
	}
	var payload struct {
		Data struct {
			Rows  []map[string]any `json:"rows"`
			Total int              `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(listRecorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Total != 1 || len(payload.Data.Rows) != 1 || payload.Data.Rows[0]["id"] != taskOne {
		t.Fatalf("owner-scoped list = %+v", payload.Data)
	}
	// operation_id 过滤（响应丢失确认入口）。
	request = httptest.NewRequest(http.MethodGet, "/tasks?operation_id=op-list-1", nil)
	listRecorder = httptest.NewRecorder()
	env.routes.listTasks(listRecorder, studioAuthed(request, studioTestOwner))
	_ = json.Unmarshal(listRecorder.Body.Bytes(), &payload)
	if payload.Data.Total != 1 || payload.Data.Rows[0]["clientOperationId"] != "op-list-1" {
		t.Fatalf("operation_id filter = %+v", payload.Data)
	}
	// status 非法 → 400；未知键 → 400。
	request = httptest.NewRequest(http.MethodGet, "/tasks?status=running", nil)
	listRecorder = httptest.NewRecorder()
	env.routes.listTasks(listRecorder, studioAuthed(request, studioTestOwner))
	if listRecorder.Code != http.StatusBadRequest {
		t.Fatalf("bad status = %d", listRecorder.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/tasks?cursor=1", nil)
	listRecorder = httptest.NewRecorder()
	env.routes.listTasks(listRecorder, studioAuthed(request, studioTestOwner))
	if listRecorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown key = %d", listRecorder.Code)
	}
	// 归属 404 同型：他人任务详情/取消不可见。
	request = httptest.NewRequest(http.MethodGet, "/tasks/"+taskOther, nil)
	request.SetPathValue("taskId", taskOther)
	detail := httptest.NewRecorder()
	env.routes.getTask(detail, studioAuthed(request, studioTestOwner))
	if detail.Code != http.StatusNotFound {
		t.Fatalf("cross-user get = %d body=%s", detail.Code, detail.Body.String())
	}
	if _, code := studioErrorPayload(t, detail); code != CodeTaskNotFound {
		t.Fatalf("code = %q", code)
	}
	request = httptest.NewRequest(http.MethodDelete, "/tasks/"+taskOther, nil)
	request.SetPathValue("taskId", taskOther)
	cross := httptest.NewRecorder()
	env.routes.cancelTask(cross, studioAuthed(request, studioTestOwner))
	if cross.Code != http.StatusNotFound {
		t.Fatalf("cross-user cancel = %d", cross.Code)
	}
}

// --- 授权与校验 ---

func TestStudioGenerationAuthorizationAndValidation(t *testing.T) {
	newEnv := func(t *testing.T, domains []string) (*studioTestEnv, *fakeGateway) {
		env, gateway := newGenerationTestEnv(t)
		env.keys.domains = domains
		return env, gateway
	}

	t.Run("域未授权 403 studio_domain_not_authorized", func(t *testing.T) {
		env, gateway := newEnv(t, []string{"text"})
		recorder := studioInvokeJSON(env.routes, studioJSONRequest(t, "/generations/images", map[string]any{
			"accountId": "acc_img", "model": "gpt-image-1", "prompt": "x", "clientOperationId": "op-dom-1",
		}), studioTestOwner)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("domain denied status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		if _, code := studioErrorPayload(t, recorder); code != CodeDomainNotAuthorized {
			t.Fatalf("code = %q", code)
		}
		if calls := gateway.recorded(); len(calls) != 0 {
			t.Fatalf("upstream calls = %d, want 0", len(calls))
		}
	})

	t.Run("账户越范围 403", func(t *testing.T) {
		env, _ := newEnv(t, nil)
		recorder := studioInvokeJSON(env.routes, studioJSONRequest(t, "/generations/images", map[string]any{
			"accountId": "acc_other", "model": "gpt-image-1", "prompt": "x", "clientOperationId": "op-acc-1",
		}), studioTestOwner)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("account denied status = %d", recorder.Code)
		}
		if _, code := studioErrorPayload(t, recorder); code != CodeAccountNotAuthorized {
			t.Fatalf("code = %q", code)
		}
	})

	t.Run("模型域不匹配 400", func(t *testing.T) {
		env, _ := newEnv(t, nil)
		recorder := studioInvokeJSON(env.routes, studioJSONRequest(t, "/generations/images", map[string]any{
			"accountId": "acc_img", "model": "seedance-pro", "prompt": "x", "clientOperationId": "op-model-1",
		}), studioTestOwner)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("model mismatch status = %d", recorder.Code)
		}
		if _, code := studioErrorPayload(t, recorder); code != CodeModelDomainMismatch {
			t.Fatalf("code = %q", code)
		}
	})

	t.Run("operationId 与未知字段校验", func(t *testing.T) {
		env, _ := newEnv(t, nil)
		cases := []struct {
			name    string
			payload map[string]any
			code    string
		}{
			{"缺 operationId", map[string]any{"accountId": "acc_img", "model": "gpt-image-1", "prompt": "x"}, CodeOperationIDInvalid},
			{"超长 operationId", map[string]any{"accountId": "acc_img", "model": "gpt-image-1", "prompt": "x", "clientOperationId": strings.Repeat("a", 129)}, CodeOperationIDInvalid},
			{"未知字段", map[string]any{"accountId": "acc_img", "model": "gpt-image-1", "prompt": "x", "clientOperationId": "op-ok", "n": 2}, CodeInvalidRequest},
			{"能力未声明 size", map[string]any{"accountId": "acc_img", "model": "gpt-image-1", "prompt": "x", "clientOperationId": "op-ok", "size": "1024x1024"}, CodeInvalidRequest},
			{"缺 prompt", map[string]any{"accountId": "acc_img", "model": "gpt-image-1", "clientOperationId": "op-ok"}, CodeInvalidRequest},
		}
		for _, testCase := range cases {
			recorder := studioInvokeJSON(env.routes, studioJSONRequest(t, "/generations/images", testCase.payload), studioTestOwner)
			if recorder.Code != http.StatusBadRequest {
				t.Errorf("%s: status = %d body=%s", testCase.name, recorder.Code, recorder.Body.String())
				continue
			}
			if _, code := studioErrorPayload(t, recorder); code != testCase.code {
				t.Errorf("%s: code = %q, want %q", testCase.name, code, testCase.code)
			}
		}
	})
}

// --- 结算幂等 DB 兜底（idx_studio_assets_owner_source_task）与状态机门控 ---

func TestStudioTaskStoreTransitions(t *testing.T) {
	env := newStudioTestEnv(t)
	task, created, err := env.store.CreateTask(CreateTaskInput{
		SystemAccountID: studioTestOwner, ClientOperationID: "op-store-1", Kind: "video",
		RequestSnapshotJSON: `{"model":"seedance-pro","prompt":"仓库测试"}`, Now: studioTestNow,
	})
	if err != nil || !created {
		t.Fatalf("create = %v, %v", created, err)
	}
	// 幂等重放：同 ID 返回原任务。
	replay, created, err := env.store.CreateTask(CreateTaskInput{
		SystemAccountID: studioTestOwner, ClientOperationID: "op-store-1", Kind: "video", Now: studioTestNow,
	})
	if err != nil || created || replay.ID != task.ID {
		t.Fatalf("replay = %v, %v, %v", replay.ID, created, err)
	}
	// 受理确立门控 + 取消竞态。
	ok, err := env.store.MarkTaskDispatched(task.ID, studioTestOwner, "acc_vid", "volcengine", "mj_store_1", studioTestNow)
	if err != nil || !ok {
		t.Fatalf("dispatch = %v, %v", ok, err)
	}
	if ok, err := env.store.MarkTaskDispatched(task.ID, studioTestOwner, "acc_vid", "volcengine", "mj_store_1", studioTestNow); err != nil || ok {
		t.Fatalf("double dispatch should lose: %v, %v", ok, err)
	}
	// 结算幂等：单事务收口一次成功，重放返回冲突且不产生第二份资产。
	asset, err := env.store.SettleTaskWithAsset(SettleTaskInput{
		TaskID: task.ID, OwnerID: studioTestOwner,
		Asset: CreateAssetInput{
			SystemAccountID: studioTestOwner, Kind: "video", Mime: "video/mp4",
			StorageKey: "ab/cd/store-asset", Bytes: 128, Sha256: strings.Repeat("b", 64),
			SourceTaskID: &task.ID, ParamsJSON: "{}", Now: studioTestNow,
		},
		Now: studioTestNow,
	})
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	_ = asset
	if _, err := env.store.SettleTaskWithAsset(SettleTaskInput{
		TaskID: task.ID, OwnerID: studioTestOwner,
		Asset: CreateAssetInput{
			SystemAccountID: studioTestOwner, Kind: "video", Mime: "video/mp4",
			StorageKey: "ab/cd/store-asset-2", Bytes: 128, Sha256: strings.Repeat("c", 64),
			SourceTaskID: &task.ID, ParamsJSON: "{}", Now: studioTestNow,
		},
		Now: studioTestNow,
	}); !errors.Is(err, ErrTaskSettleConflict) {
		t.Fatalf("second settle error = %v, want ErrTaskSettleConflict（终态唯一性闸）", err)
	}
	var assetCount int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM studio_assets WHERE source_task_id = ?`, task.ID).Scan(&assetCount); err != nil {
		t.Fatal(err)
	}
	if assetCount != 1 {
		t.Fatalf("settle asset rows = %d, want 1（partial 唯一索引兜底）", assetCount)
	}
	if bytes_, count := studioUsageOf(t, env.db, studioTestOwner); count != 1 || bytes_ != int64(128) {
		t.Fatalf("usage = %d/%d, want 128/1", bytes_, count)
	}
	_ = asset
	// 租约列更新。
	if err := env.store.UpdateLease(task.ID, "jobs-owner", "2026-10-12T08:01:00.000Z", studioTestNow); err != nil {
		t.Fatalf("lease: %v", err)
	}
	leased, _ := env.store.GetTask(task.ID, studioTestOwner)
	if leased.LeaseOwner == nil || *leased.LeaseOwner != "jobs-owner" {
		t.Fatalf("lease owner = %v", leased.LeaseOwner)
	}
	// 结算失败迁移：新任务 pending_settled → failed。
	other, _, err := env.store.CreateTask(CreateTaskInput{
		SystemAccountID: studioTestOwner, ClientOperationID: "op-store-2", Kind: "image", Now: studioTestNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	ok, err = env.store.MarkSettlementFailed(other.ID, studioTestOwner, `{"code":"studio_settle_failed"}`, studioTestNow)
	if err != nil || !ok {
		t.Fatalf("mark settlement failed = %v, %v", ok, err)
	}
	again, _ := env.store.GetTask(other.ID, studioTestOwner)
	if again.Settlement != "failed" {
		t.Fatalf("settlement = %q", again.Settlement)
	}
	// MarkSettled：failed → settled 是 §6 重领取重试收口臂；settled 后重复落
	// 定竞争失败。
	ok, err = env.store.MarkSettled(other.ID, studioTestOwner, nil, studioTestNow)
	if err != nil || !ok {
		t.Fatalf("mark settled from failed = %v, %v", ok, err)
	}
	if ok, err := env.store.MarkSettled(other.ID, studioTestOwner, nil, studioTestNow); err != nil || ok {
		t.Fatalf("double mark settled should lose: %v, %v", ok, err)
	}
}
