package main

// M3f qwen（通义百炼 paraformer 长转写）chain 级集成测试（契约 §10.2；媒体
// 设计 §4.2/§7/§8）：进程内 fixture（newChainFixture 先例）+ qwen 分组/
// audio_job 账户 + mockupstream media_qwen_asr_* 场景。覆盖四条链路：
//  1. 创建（transcription 报文改写：input_url → input.file_urls 单元素数组、
//     language → parameters.language_hints 数组、provider_options deep-merge、
//     Bearer 认证、**无 X-DashScope-Async 头**（天然异步服务）、/api/v1 服务
//     根 URL 归一 + params 回显）→ 轮询 PENDING→RUNNING→SUCCEEDED
//    （output.results[].transcription_url 冻结 + usage JSON 字符串解析出
//     duration=62.5 时长计量）→ content 经绝对 URL 无凭据直连下载转写结果
//     JSON + 终态 usage spool（audioInputSeconds 计量照落、目录无 USD 秒价
//     成本 0，不虚计）+ /v1/audio/jobs 列表 kind 过滤（不与视频行混行）；
//  2. multipart 文件输入 → 本地 400（唯一输入形态 input_url，零存储不暂存，
//     不打上游）；
//  3. DELETE 取消（qwen §10.2 面无取消 API → 不发上游请求，本地收敛
//     cancelled）；
//  4. language 参数映射在出站报文断言（链路 1 内联覆盖）。
//
// M6 追加（契约 §7.2/§9.2）：
//  5. glm TTS 透传链路：/v1/audio/speech → 出站 POST /api/paas/v4/audio/speech
//     （glm 通用根归一，非 openai /v1 补缀）+ Bearer + provider_options.glm
//     的 ref_audio/ref_text 深合并进顶层 body（通道键不透传），mock
//     media_glm_tts_ok 回 wav 载荷（RIFF magic bytes）；
//  6. volcengine TTS adapter 链路：/v1/audio/speech → 出站 POST /api/v3/tts
//     （固定语音服务域，经测试 seam env 指回 mock）+ `Authorization:
//     Bearer;<token>` 分号鉴权特例 + req_params 嵌套报文（user.uid=
//     speech_appid、reqid uuid、operation=query），响应 code 3000 + data
//     base64 解码后透传 mp3（frame sync magic bytes）。
//
// 上游拓扑沿 chain_media_video_qwen_test.go：账户 base_url 指向记录层
//（断言认证头与异步头缺席），内层为 mockupstream 引擎；completed 的产物
// url 由引擎按自身 URL 渲染，网关直连该 URL（不经过记录层）。
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	platformmock "github.com/huanminabc/juhe-ai/backend-go-platform/mockupstream"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamenvelope"
)

// mediaAudioDecodeList 解码任务面列表响应的 data 数组。
func mediaAudioDecodeList(t *testing.T, payload []byte) []map[string]any {
	t.Helper()
	var object struct {
		Object string           `json:"object"`
		Data   []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(payload, &object); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if object.Object != "list" {
		t.Fatalf("list 响应 object = %q, want list: %s", object.Object, payload)
	}
	return object.Data
}

// seedMediaQwenASRAccount 种 qwen 分组 + paraformer 长转写测试账户 + 独立
// 路由策略与 API Key（不复用 fixture 的 openai 组与视频 qwen 组，避免跨
// provider/kind 候选串扰），返回网关 API Key 明文。凭据带 base_url 与
// supported_endpoint_modes（openai 族词表：audio_job_* 四值，opt-in 语义；
// 候选过滤按 audio_job_create 消费）。模型约束种 paraformer-v2。
func seedMediaQwenASRAccount(t *testing.T, fixture *chainFixture, id, baseURL, apiKey string) string {
	t.Helper()
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(fixtureBind(query), args...); err != nil {
			t.Fatalf("seed qwen asr row: %v: %v", query, err)
		}
	}
	keySecret := "sk-chain-qwen-asr"
	credentials := map[string]any{
		"api_key":  apiKey,
		"base_url": baseURL,
		"supported_endpoint_modes": []string{
			"audio_job_create", "audio_job_get", "audio_job_content", "audio_job_cancel",
		},
	}
	seed(`INSERT INTO groups (id, system_account_id, enabled, group_type) VALUES ('group_qwen_asr', ?, 1, 'personal')`, fixture.systemAccount)
	seed(`INSERT INTO accounts (id, system_account_id, vendor_code, provider_protocol_profile_id, protocol_code, protocol_version, name, type, status, schedulable, concurrency_limit, priority, credentials_encrypted, deleted_at) VALUES (?, ?, 'qwen', 'profile_qwen_openai_v1', 'openai', 'v1', ?, 'api_key', 'active', 1, 0, 0, ?, NULL)`,
		id, fixture.systemAccount, id, mustEncryptCredentials(t, credentials))
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
		VALUES ('group_qwen_asr', ?, ?, 1, '2026-10-04T00:00:00.000Z')`, fixture.systemAccount, id)
	seed(`INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, status, supported_endpoint_modes_json, created_at, updated_at)
		VALUES ('mdl_qwen_paraformer', 'qwen', 'paraformer-v2', 'paraformer-v2', 'global', 'active', '["audio_job_create","audio_job_get","audio_job_content","audio_job_cancel"]', '2026-10-04T00:00:00.000Z', '2026-10-04T00:00:00.000Z') ON CONFLICT (id) DO NOTHING`)
	seed(`INSERT INTO account_supported_models (account_id, model, model_id, created_at)
		VALUES (?, 'paraformer-v2', 'mdl_qwen_paraformer', '2026-10-04T00:00:00.000Z')`, id)
	// B4 第一批单连接事实源：BaseURL 与 mode 集改连接行。
	chainSeedAccountConnection(t, fixture.db, id, baseURL, "audio_job_create", "audio_job_get", "audio_job_content", "audio_job_cancel")
	seed(`INSERT INTO route_strategies (id, system_account_id, name, mode, config_json, status)
		VALUES ('rs_qwen_asr', ?, 'qwen长转写', 'normal', NULL, 'active')`, fixture.systemAccount)
	seed(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at)
		VALUES ('rsg_qwen_asr', 'rs_qwen_asr', ?, 'group_qwen_asr', 0, 1, 'active', '2026-10-04T00:00:00.000Z')`, fixture.systemAccount)
	seed(`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, status, scheduling_preference_id, created_at)
		VALUES ('key_qwen_asr', ?, 'rs_qwen_asr', 'qwen长转写Key', ?, 'active', 'schpref-builtin-chat-cost-first', '2026-10-04T00:00:00.000Z')`,
		fixture.systemAccount, gatewayruntimecache.HashSecret(keySecret))
	return keySecret
}

// mediaAudioJobCreate 创建一个长音频任务并断言受理（2xx + 对外 job 对象）。
func mediaAudioJobCreate(t *testing.T, serverURL, apiKey, body string, headers map[string]string) map[string]any {
	t.Helper()
	response, payload := mediaVideoClientDo(t, serverURL, http.MethodPost, "/v1/audio/jobs", apiKey, body, headers)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("audio job create status=%d body=%s", response.StatusCode, payload)
	}
	return mediaVideoDecodeJob(t, payload)
}

// TestChainMediaAudioJobQwenParaformerLifecycle 覆盖链路 1：创建（报文改写 +
// 无异步头 + /api/v1 服务根 URL + applied 回显）→ 轮询 PENDING→RUNNING→
// SUCCEEDED（transcription_url 冻结 + usage 时长计量照抽）→ content 绝对
// URL 无凭据直连转写结果 JSON → 终态 usage（audioInputSeconds=62.5 照落、
// 目录无 USD 秒价成本 0，契约 §10.2/§2.8 不虚计）→ /v1/audio/jobs 列表
// kind 过滤。
func TestChainMediaAudioJobQwenParaformerLifecycle(t *testing.T) {
	fixture := newChainFixture(t)
	recorder, mock, upstreamURL := newQwenRecordedUpstream(t)
	apiKey := seedMediaQwenASRAccount(t, fixture, "acc_qwen_asr", upstreamURL, "sk-dashscope-asr-a")

	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain := composeMediaVideoChain(t, fixture, spoolDir)
	server := httptest.NewServer(chain)
	defer server.Close()

	job := mediaAudioJobCreate(t, server.URL, apiKey,
		`{"model":"paraformer-v2","input_url":"https://oss.example.com/audio/meeting.mp3","language":"zh","provider_options":{"qwen":{"parameters":{"diarization_enabled":true}}}}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaQwenASRCreatePending)})
	jobID, _ := job["id"].(string)
	if jobID == "" || !strings.HasPrefix(jobID, "audiojob_") {
		t.Fatalf("对外 job id 缺失或非 audiojob_ 前缀: %#v", job)
	}
	if job["object"] != "audio_job" {
		t.Fatalf("object = %v, want audio_job", job["object"])
	}
	if job["status"] != "queued" {
		t.Fatalf("创建响应 status = %v, want queued（创建响应 task_status=PENDING 归一）", job["status"])
	}
	if job["provider"] != "qwen" {
		t.Fatalf("provider = %v, want qwen", job["provider"])
	}
	if job["language"] != "zh" {
		t.Fatalf("language 回显 = %v, want zh", job["language"])
	}
	providerJobID, _ := job["provider_job_id"].(string)
	if !strings.Contains(providerJobID, "-") || len(providerJobID) != 36 {
		t.Fatalf("provider_job_id = %q, want uuid 任务 id 形态（与万相同一任务接口）", providerJobID)
	}
	applied := mediaVideoJobKeys(t, job, "params_applied")
	for _, want := range []string{"model", "input_url", "language"} {
		if !applied[want] {
			t.Fatalf("params_applied 缺少 %s: %v", want, job["params_applied"])
		}
	}
	if !strings.Contains(fmt.Sprintf("%v", job["provider_options_applied"]), "parameters") {
		t.Fatalf("provider_options_applied 缺少 parameters: %v", job["provider_options_applied"])
	}

	// 创建出站断言：POST /api/v1/services/audio/asr/transcription + Bearer 认证
	// 头 + **无 X-DashScope-Async 头**（天然异步服务，契约 §10.2 与万相 §10.1
	// 的关键差异）+ 转换报文（input.file_urls 单元素数组、parameters.
	// language_hints 数组、diarization_enabled 嵌套合并）。
	records := recorder.snapshot()
	if len(records) != 1 || records[0].Method != http.MethodPost || records[0].Path != "/api/v1/services/audio/asr/transcription" {
		t.Fatalf("创建出站请求形态错误: %+v", records)
	}
	if records[0].Authorization != "Bearer sk-dashscope-asr-a" {
		t.Fatalf("创建请求 Authorization = %q, want Bearer sk-dashscope-asr-a", records[0].Authorization)
	}
	if records[0].DashScopeAsync != "" {
		t.Fatalf("长转写创建不得携带 X-DashScope-Async 头（天然异步服务）: %q", records[0].DashScopeAsync)
	}
	createBody := ""
	for _, request := range mock.Requests() {
		if request.Method == http.MethodPost && request.Path == "/api/v1/services/audio/asr/transcription" {
			createBody = request.Body
		}
	}
	if createBody == "" {
		t.Fatal("引擎侧未录制创建请求")
	}
	for _, want := range []string{
		`"model":"paraformer-v2"`,
		`"input":{"file_urls":["https://oss.example.com/audio/meeting.mp3"]}`,
		`"language_hints":["zh"]`,
		`"diarization_enabled":true`,
	} {
		if !strings.Contains(createBody, want) {
			t.Fatalf("创建报文缺少 %s: %s", want, createBody)
		}
	}

	// 轮询三次：queued（#1 PENDING 归一）→ in_progress（#2 RUNNING 归一）→
	// completed（#3 SUCCEEDED + transcription_url 冻结 + usage duration=62.5）。
	for index, want := range []string{"queued", "in_progress", "completed"} {
		response, payload := mediaVideoClientDo(t, server.URL, http.MethodGet, "/v1/audio/jobs/"+jobID, apiKey, "", nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("poll #%d status=%d body=%s", index+1, response.StatusCode, payload)
		}
		if object := mediaVideoDecodeJob(t, payload); object["status"] != want {
			t.Fatalf("poll #%d status = %v, want %s", index+1, object["status"], want)
		}
	}
	// 任务面轮询经记录层：GET /api/v1/tasks/{task_id} + Bearer（与万相视频
	// 同一 DashScope 任务接口）。
	records = recorder.snapshot()
	if len(records) != 4 {
		t.Fatalf("轮询出站请求计数错误: %+v", records)
	}
	for _, record := range records[1:] {
		if record.Method != http.MethodGet || record.Path != "/api/v1/tasks/"+providerJobID {
			t.Fatalf("轮询出站请求形态错误: %+v", record)
		}
	}

	// media_jobs 行终态：kind=audio_transcription + completed + 时长计量照抽
	//（usage JSON 字符串 duration=62.5）按目录 USD 秒价计费（2026-10-05
	// 取证批：国际站 Model Studio USD 明文 $0.000012/秒覆盖换算值——核对批
	// 换算值 $0.00001143/秒作废；本断言随定价事实回正非语义变更）。
	var kind, status, upstreamJobID string
	var costUsd float64
	if err := fixture.db.QueryRow(`SELECT kind, status, upstream_job_id, cost_usd FROM media_jobs WHERE id = ?`, jobID).Scan(&kind, &status, &upstreamJobID, &costUsd); err != nil {
		t.Fatalf("查询 media_jobs 行: %v", err)
	}
	if kind != "audio_transcription" || status != "completed" || upstreamJobID != providerJobID {
		t.Fatalf("media_jobs = %s/%s/%s, want audio_transcription/completed/%s", kind, status, upstreamJobID, providerJobID)
	}
	// paraformer-v2 已随 2025 前模型清理批移除目录行：无秒价 → cost 0，
	// usage 行计量照落（audioInputSeconds 62.5）不打 usage_missing（契约
	// §2.8/§10.2：计量照落成本不虚计）。
	if costUsd != 0 {
		t.Fatalf("paraformer 无目录价 cost_usd = %v, want 0（2025 前模型清理批后兜底）", costUsd)
	}

	// content 下载：completed 的 transcription_url 是引擎渲染的绝对 URL（不经
	// 记录层），网关无凭据直连。产物是转写结果 JSON（契约 §10.2），非音频。
	response, payload := mediaVideoClientDo(t, server.URL, http.MethodGet, "/v1/audio/jobs/"+jobID+"/content", apiKey, "", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("content status=%d body=%s", response.StatusCode, payload)
	}
	if got := response.Header.Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("content-type = %q, want application/json（转写结果 JSON 文件）", got)
	}
	if !strings.Contains(string(payload), `"transcripts"`) || !strings.Contains(string(payload), "meeting.mp3") {
		t.Fatalf("content 不是转写结果 JSON（transcripts/file_url 缺失）: %s", payload)
	}
	if len(recorder.snapshot()) != 4 {
		t.Fatalf("产物下载不得经账户 base_url（ContentFromArtifact 直连 url）: %+v", recorder.snapshot())
	}
	for _, request := range mock.Requests() {
		if strings.HasPrefix(request.Path, "/api/v1/tasks/") &&
			strings.HasSuffix(request.Path, "/content") && request.AuthHeader != "" {
			t.Fatalf("产物直连不得携带 Authorization: %q", request.AuthHeader)
		}
	}

	// 列表 kind 过滤：/v1/audio/jobs 含本任务（object=audio_job），/v1/videos
	// 不混入长音频行（两族互不混行，M3f 泛化）。
	listResponse, listPayload := mediaVideoClientDo(t, server.URL, http.MethodGet, "/v1/audio/jobs", apiKey, "", nil)
	if listResponse.StatusCode != http.StatusOK {
		t.Fatalf("audio list status=%d body=%s", listResponse.StatusCode, listPayload)
	}
	listObject := mediaAudioDecodeList(t, listPayload)
	if len(listObject) != 1 || listObject[0]["id"] != jobID || listObject[0]["object"] != "audio_job" {
		t.Fatalf("/v1/audio/jobs 列表回显错误: %s", listPayload)
	}
	videoListResponse, videoListPayload := mediaVideoClientDo(t, server.URL, http.MethodGet, "/v1/videos", apiKey, "", nil)
	if videoListResponse.StatusCode != http.StatusOK {
		t.Fatalf("video list status=%d body=%s", videoListResponse.StatusCode, videoListPayload)
	}
	if objects := mediaAudioDecodeList(t, videoListPayload); len(objects) != 0 {
		t.Fatalf("/v1/videos 列表不得混入长音频行: %s", videoListPayload)
	}

	// 终态 usage：spool 链异步落盘，completed 携带 usage JSON 字符串解析出的
	// 62.5 秒输入时长计量（audioInputSeconds）+ endpoint=/v1/audio/jobs；目录
	// 无价 → 无 costUsd 行项、无 usageMissing 标记（契约 §2.8/§10.2：计量
	// 照落成本不虚计）。
	waitGlmSpoolRecord(t, spoolDir, func(text string) bool {
		return strings.Contains(text, `"audioInputSeconds":62.5`) && strings.Contains(text, `"endpoint":"/v1/audio/jobs"`)
	}, "qwen asr completed 时长计量终态记录")
}

// TestChainMediaAudioJobMultipartRejected 覆盖链路 2：multipart 文件输入 →
// 网关本地 400（长音频任务唯一输入形态是 input_url 公网 URL——契约 §10.2
// 上游只收 file_urls、零存储不暂存；确定性参数错误不换账户不打上游）。
func TestChainMediaAudioJobMultipartRejected(t *testing.T) {
	fixture := newChainFixture(t)
	recorder, _, upstreamURL := newQwenRecordedUpstream(t)
	apiKey := seedMediaQwenASRAccount(t, fixture, "acc_qwen_asr_multipart", upstreamURL, "sk-dashscope-asr-mp")

	chain := composeMediaVideoChain(t, fixture, filepath.Join(t.TempDir(), "spool"))
	server := httptest.NewServer(chain)
	defer server.Close()

	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/audio/jobs",
		strings.NewReader("--boundary\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.mp3\"\r\n\r\nx\r\n--boundary--"))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", "multipart/form-data; boundary=boundary")
	client := &http.Client{Timeout: 15e9}
	response, readErr := client.Do(request)
	if readErr != nil {
		t.Fatalf("do request: %v", readErr)
	}
	payload, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("multipart status=%d, want 400（唯一输入形态 input_url）body=%s", response.StatusCode, payload)
	}
	if !strings.Contains(string(payload), "invalid_request_error") {
		t.Fatalf("400 响应缺少 invalid_request_error: %s", payload)
	}
	if !strings.Contains(string(payload), "input_url") {
		t.Fatalf("400 响应应指明唯一输入形态 input_url: %s", payload)
	}
	if len(recorder.snapshot()) != 0 {
		t.Fatalf("本地 400 不得打上游: %+v", recorder.snapshot())
	}
}

// TestChainMediaAudioJobCancel 覆盖链路 3：DELETE → qwen §10.2 面无上游取消
// API → 不发上游取消请求、直接本地收敛 cancelled + 204；终态行轮询回本地
// 对象不再打上游。
func TestChainMediaAudioJobCancel(t *testing.T) {
	fixture := newChainFixture(t)
	recorder, _, upstreamURL := newQwenRecordedUpstream(t)
	apiKey := seedMediaQwenASRAccount(t, fixture, "acc_qwen_asr_cancel", upstreamURL, "sk-dashscope-asr-cancel")

	chain := composeMediaVideoChain(t, fixture, filepath.Join(t.TempDir(), "spool"))
	server := httptest.NewServer(chain)
	defer server.Close()

	job := mediaAudioJobCreate(t, server.URL, apiKey,
		`{"model":"paraformer-v2","input_url":"https://oss.example.com/audio/cancel.wav"}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaQwenASRPollRunning)})
	jobID, _ := job["id"].(string)

	response, payload := mediaVideoClientDo(t, server.URL, http.MethodDelete, "/v1/audio/jobs/"+jobID, apiKey, "", nil)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status=%d body=%s, want 204", response.StatusCode, payload)
	}
	var kind, status string
	if err := fixture.db.QueryRow(`SELECT kind, status FROM media_jobs WHERE id = ?`, jobID).Scan(&kind, &status); err != nil {
		t.Fatalf("查询 media_jobs: %v", err)
	}
	if kind != "audio_transcription" || status != "cancelled" {
		t.Fatalf("DELETE 后本地行 = %s/%s, want audio_transcription/cancelled", kind, status)
	}
	// 账户亲和面：取消未发任何上游请求（§10.2 面无取消端点，本地收敛），
	// 记录层仅有创建一条。
	if records := recorder.snapshot(); len(records) != 1 {
		t.Fatalf("qwen asr 取消不得打上游（SupportsCancel=false 本地收敛）: %+v", records)
	}
	// 终态行轮询回本地对象，不再打上游。
	pollResponse, pollPayload := mediaVideoClientDo(t, server.URL, http.MethodGet, "/v1/audio/jobs/"+jobID, apiKey, "", nil)
	if pollResponse.StatusCode != http.StatusOK || mediaVideoDecodeJob(t, pollPayload)["status"] != "cancelled" {
		t.Fatalf("终态轮询应回本地 cancelled: status=%d body=%s", pollResponse.StatusCode, pollPayload)
	}
	if len(recorder.snapshot()) != 1 {
		t.Fatalf("终态轮询不得再打上游: %+v", recorder.snapshot())
	}
}

// seedMediaGlmSpeechAccount 种 glm 分组 + TTS 测试账户 + 独立路由策略与
// API Key（M6，契约 §7.2 透传分支；profile_glm_general_openai_v1）。凭据带
// base_url 与 supported_endpoint_modes（openai 族词表 audio_speech，opt-in
// 语义）；模型约束种 cogtts（目录 seed M6 增补行）。
func seedMediaGlmSpeechAccount(t *testing.T, fixture *chainFixture, id, baseURL, apiKey string) string {
	t.Helper()
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(fixtureBind(query), args...); err != nil {
			t.Fatalf("seed glm speech row: %v: %v", query, err)
		}
	}
	keySecret := "sk-chain-glm-speech"
	credentials := map[string]any{
		"api_key":  apiKey,
		"base_url": baseURL,
		"supported_endpoint_modes": []string{
			"audio_speech",
		},
	}
	seed(`INSERT INTO groups (id, system_account_id, enabled, group_type) VALUES ('group_glm_speech', ?, 1, 'personal')`, fixture.systemAccount)
	seed(`INSERT INTO accounts (id, system_account_id, vendor_code, provider_protocol_profile_id, protocol_code, protocol_version, name, type, status, schedulable, concurrency_limit, priority, credentials_encrypted, deleted_at) VALUES (?, ?, 'glm', 'profile_glm_general_openai_v1', 'openai', 'v1', ?, 'api_key', 'active', 1, 0, 0, ?, NULL)`,
		id, fixture.systemAccount, id, mustEncryptCredentials(t, credentials))
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
		VALUES ('group_glm_speech', ?, ?, 1, '2026-10-04T00:00:00.000Z')`, fixture.systemAccount, id)
	seed(`INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, status, supported_endpoint_modes_json, created_at, updated_at)
		VALUES ('mdl_glm_cogtts', 'glm', 'cogtts', 'cogtts', 'global', 'active', '["audio_speech"]', '2026-10-04T00:00:00.000Z', '2026-10-04T00:00:00.000Z') ON CONFLICT (id) DO NOTHING`)
	seed(`INSERT INTO account_supported_models (account_id, model, model_id, created_at)
		VALUES (?, 'cogtts', 'mdl_glm_cogtts', '2026-10-04T00:00:00.000Z')`, id)
	// B4 第一批单连接事实源：BaseURL 与 mode 集改连接行。
	chainSeedAccountConnection(t, fixture.db, id, baseURL, "audio_speech")
	seed(`INSERT INTO route_strategies (id, system_account_id, name, mode, config_json, status)
		VALUES ('rs_glm_speech', ?, 'glm语音', 'normal', NULL, 'active')`, fixture.systemAccount)
	seed(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at)
		VALUES ('rsg_glm_speech', 'rs_glm_speech', ?, 'group_glm_speech', 0, 1, 'active', '2026-10-04T00:00:00.000Z')`, fixture.systemAccount)
	seed(`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, status, scheduling_preference_id, created_at)
		VALUES ('key_glm_speech', ?, 'rs_glm_speech', 'glm语音Key', ?, 'active', 'schpref-builtin-chat-cost-first', '2026-10-04T00:00:00.000Z')`,
		fixture.systemAccount, gatewayruntimecache.HashSecret(keySecret))
	return keySecret
}

// TestChainMediaGlmSpeechUrlPassthroughRefAudio 覆盖 M6 链路 5（契约 §7.2）：
// glm 账户 /v1/audio/speech 走 openai 透传——出站 URL 归一到官方通用根
// /api/paas/v4/audio/speech（CogVideo 同根去重先例，非 openai /v1 补缀）、
// provider_options.glm 的 ref_audio/ref_text 深合并进顶层（通道键不透传）、
// Bearer 认证；mock media_glm_tts_ok 回 wav 载荷（RIFF magic bytes——未转换
// 的报文通道即音频二进制）；usage 按请求字符自算（透传面无上游回报）。
func TestChainMediaGlmSpeechUrlPassthroughRefAudio(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	apiKey := seedMediaGlmSpeechAccount(t, fixture, "acc_glm_speech", mock.URL, "glm-key-tts")

	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain := composeMediaVideoChain(t, fixture, spoolDir)
	server := httptest.NewServer(chain)
	defer server.Close()

	response, payload := mediaVideoClientDo(t, server.URL, http.MethodPost, "/v1/audio/speech", apiKey,
		`{"model":"cogtts","input":"你好智谱","voice":"tongtong","response_format":"wav","provider_options":{"glm":{"ref_audio":"data:audio/wav;base64,aGVsbG8=","ref_text":"样本文本"}}}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaGlmTTSOK)})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("glm speech status=%d body=%s", response.StatusCode, payload)
	}
	if got := response.Header.Get("Content-Type"); !strings.Contains(got, "audio/wav") {
		t.Fatalf("content-type = %q, want audio/wav", got)
	}
	// wav magic bytes：RIFF 头（透传面直接是音频二进制，mock 按请求 wav 协商）。
	if len(payload) < 12 || string(payload[0:4]) != "RIFF" || string(payload[8:12]) != "WAVE" {
		t.Fatalf("响应不是 wav 载荷（RIFF/WAVE magic bytes 缺失）: % x", payload[:min(12, len(payload))])
	}

	// 出站断言：POST /api/paas/v4/audio/speech（官方通用根归一）+ Bearer +
	// ref_audio/ref_text 深合并进顶层 + provider_options 通道键不透传。
	requests := mock.Requests()
	if len(requests) != 1 || requests[0].Method != http.MethodPost || requests[0].Path != "/api/paas/v4/audio/speech" {
		t.Fatalf("glm speech 出站请求形态错误: %+v", requests)
	}
	if requests[0].AuthHeader != "Bearer glm-key-tts" {
		t.Fatalf("glm speech Authorization = %q, want Bearer glm-key-tts", requests[0].AuthHeader)
	}
	body := requests[0].Body
	for _, want := range []string{
		`"model":"cogtts"`, `"input":"你好智谱"`, `"voice":"tongtong"`,
		`"ref_audio":"data:audio/wav;base64,aGVsbG8="`, `"ref_text":"样本文本"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("出站报文缺少 %s: %s", want, body)
		}
	}
	if strings.Contains(body, `"provider_options"`) {
		t.Fatalf("网关私有 provider_options 通道键不得透传上游: %s", body)
	}

	// usage：透传面无上游字符回报 → 网关按请求 input 自算（4 runes）。
	waitGlmSpoolRecord(t, spoolDir, func(text string) bool {
		return strings.Contains(text, `"ttsInputChars":4`) && strings.Contains(text, `"endpoint":"POST /v1/audio/speech"`)
	}, "glm tts 请求字符自算终态记录")
}

// TestChainMediaGlmSpeechUpstreamURLDedup 重写为 v2 直拼钉值（动机：原
// chainGlmVideoUpstreamURL 的服务根去重归一随 v2 删除）：glm 语音 opPath
// 不含版本段（/audio/speech），/api/paas/v4 由 base_url 版本根承载，经共享
// upstreamenvelope.JoinUpstreamURL 直拼——不再有 base 侧服务根判定。
func TestChainMediaGlmSpeechUpstreamURLDedup(t *testing.T) {
	versionRoot, err := upstreamenvelope.JoinUpstreamURL("https://open.bigmodel.cn/api/paas/v4", "/audio/speech")
	if err != nil {
		t.Fatalf("version root url: %v", err)
	}
	if versionRoot != "https://open.bigmodel.cn/api/paas/v4/audio/speech" {
		t.Fatalf("版本根 base 直拼结果不符: %q", versionRoot)
	}
}

// seedMediaVolcengineSpeechAccount 种 volcengine 分组 + TTS 测试账户 + 独立
// 路由策略与 API Key（契约 §9.2，2026-10-17 迁移批形态）。凭据带 ark API
// Key、base_url 与**单值 speech_token**（新版语音控制台 API Key，凭据归一
// 化放行键）与 supported_endpoint_modes（openai 族词表 audio_speech，
// opt-in 语义）。模型约束种统一面占位名 doubao-tts（历史占位目录行——
// 迁移批起 adapter 出站前归一映射为 seed-audio-1.0，见《火山方舟账号接
// 入.md》模型目录节）。
func seedMediaVolcengineSpeechAccount(t *testing.T, fixture *chainFixture, id, baseURL, arkKey string, withSpeechCredentials bool) string {
	t.Helper()
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(fixtureBind(query), args...); err != nil {
			t.Fatalf("seed volcengine speech row: %v: %v", query, err)
		}
	}
	keySecret := "sk-chain-volcengine-speech"
	credentials := map[string]any{
		"api_key":  arkKey,
		"base_url": baseURL,
		"supported_endpoint_modes": []string{
			"audio_speech",
		},
	}
	if withSpeechCredentials {
		credentials["speech_token"] = "volc-tts-token-a"
	}
	seed(`INSERT INTO groups (id, system_account_id, enabled, group_type) VALUES ('group_volcengine_speech', ?, 1, 'personal')`, fixture.systemAccount)
	seed(`INSERT INTO accounts (id, system_account_id, vendor_code, provider_protocol_profile_id, protocol_code, protocol_version, name, type, status, schedulable, concurrency_limit, priority, credentials_encrypted, deleted_at) VALUES (?, ?, 'volcengine', 'profile_volcengine_openai_v1', 'openai', 'v1', ?, 'api_key', 'active', 1, 0, 0, ?, NULL)`,
		id, fixture.systemAccount, id, mustEncryptCredentials(t, credentials))
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
		VALUES ('group_volcengine_speech', ?, ?, 1, '2026-10-04T00:00:00.000Z')`, fixture.systemAccount, id)
	seed(`INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, status, supported_endpoint_modes_json, created_at, updated_at)
		VALUES ('mdl_volc_doubao_tts', 'volcengine', 'doubao-tts', 'doubao-tts', 'global', 'active', '["audio_speech"]', '2026-10-04T00:00:00.000Z', '2026-10-04T00:00:00.000Z') ON CONFLICT (id) DO NOTHING`)
	seed(`INSERT INTO account_supported_models (account_id, model, model_id, created_at)
		VALUES (?, 'doubao-tts', 'mdl_volc_doubao_tts', '2026-10-04T00:00:00.000Z')`, id)
	// B4 第一批单连接事实源：BaseURL 与 mode 集改连接行。
	chainSeedAccountConnection(t, fixture.db, id, baseURL, "audio_speech")
	seed(`INSERT INTO route_strategies (id, system_account_id, name, mode, config_json, status)
		VALUES ('rs_volcengine_speech', ?, 'volcengine语音', 'normal', NULL, 'active')`, fixture.systemAccount)
	seed(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at)
		VALUES ('rsg_volcengine_speech', 'rs_volcengine_speech', ?, 'group_volcengine_speech', 0, 1, 'active', '2026-10-04T00:00:00.000Z')`, fixture.systemAccount)
	seed(`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, status, scheduling_preference_id, created_at)
		VALUES ('key_volcengine_speech', ?, 'rs_volcengine_speech', 'volcengine语音Key', ?, 'active', 'schpref-builtin-chat-cost-first', '2026-10-04T00:00:00.000Z')`,
		fixture.systemAccount, gatewayruntimecache.HashSecret(keySecret))
	return keySecret
}

// TestChainMediaVolcengineSpeechFullChain 覆盖链路 6（契约 §9.2，2026-10-17
// 迁移批形态）：出站 POST /api/v3/tts/create（固定语音服务域——测试 seam
// env 指回 mock，未设置时恒 openspeech.bytedance.com）+ `X-Api-Key` 单头
// 单值鉴权（Authorization 不出站）+ 新版报文（model=占位名 doubao-tts 归一
// 映射为 seed-audio-1.0、input→text_prompt、voice→speaker、
// response_format→audio_config.format、speed→audio_config.speech_rate）；
// 响应 audio base64 解码后以二进制 mp3 透传（frame sync magic bytes）；
// usage 按请求字符自算（上游无字符回报，§2.8）。
func TestChainMediaVolcengineSpeechFullChain(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	// 测试 seam：把火山语音固定 host 指回本进程 mock（生产缺省恒官方根，
	// 见 chainVolcengineSpeechUpstreamURL 注释）。
	t.Setenv(chainVolcengineSpeechUpstreamEnv, mock.URL)
	apiKey := seedMediaVolcengineSpeechAccount(t, fixture, "acc_volcengine_speech", mock.URL, "ark-key-a", true)

	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain := composeMediaVideoChain(t, fixture, spoolDir)
	server := httptest.NewServer(chain)
	defer server.Close()

	response, payload := mediaVideoClientDo(t, server.URL, http.MethodPost, "/v1/audio/speech", apiKey,
		`{"model":"doubao-tts","input":"你好火山","voice":"BV700_streaming","response_format":"mp3","speed":1.5}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaVolcengineTTSOK)})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("volcengine speech status=%d body=%s", response.StatusCode, payload)
	}
	if got := response.Header.Get("Content-Type"); !strings.Contains(got, "audio/mpeg") {
		t.Fatalf("content-type = %q, want audio/mpeg（data base64 解码后的二进制通道）", got)
	}
	// base64 解码断言：mp3 frame sync（0xFF 0xFB）。上游 data 是 base64 字符串，
	// 未解码的透传不满足二进制 magic bytes。
	if len(payload) < 4 || payload[0] != 0xFF || payload[1] != 0xFB {
		t.Fatalf("响应不是解码后的 mp3 载荷（frame sync 缺失）: % x", payload[:min(4, len(payload))])
	}

	// 出站断言：POST /api/v3/tts/create + X-Api-Key 单头鉴权（Authorization
	// 已被网关删除）+ 新版报文（占位名归一映射为 seed-audio-1.0）。
	requests := mock.Requests()
	if len(requests) != 1 || requests[0].Method != http.MethodPost || requests[0].Path != "/api/v3/tts/create" {
		t.Fatalf("volcengine speech 出站请求形态错误: %+v", requests)
	}
	if requests[0].APIKeyHeader != "volc-tts-token-a" {
		t.Fatalf("volcengine speech X-Api-Key = %q, want speech_token 单值（新版语音控制台 API Key）", requests[0].APIKeyHeader)
	}
	if requests[0].AuthHeader != "" {
		t.Fatalf("volcengine speech Authorization = %q, want 空（语音面不得携带 ark Bearer 出站）", requests[0].AuthHeader)
	}
	body := requests[0].Body
	for _, want := range []string{
		`"model":"seed-audio-1.0"`,
		`"text_prompt":"你好火山"`,
		`"speaker":"BV700_streaming"`,
		`"format":"mp3"`,
		`"speech_rate":1.5`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("出站报文缺少 %s: %s", want, body)
		}
	}
	// 旧版报文骨架（user/req_params/reqid/operation）随旧端点废弃，不得残留；
	// 占位名 doubao-tts 已归一映射，不得原样出站。
	for _, legacy := range []string{`"user"`, `"req_params"`, `"reqid"`, `"operation"`, `"doubao-tts"`} {
		if strings.Contains(body, legacy) {
			t.Fatalf("出站报文残留旧版骨架 %s（迁移批废弃）: %s", legacy, body)
		}
	}

	// usage：维度修正批（契约 §9.2/§2.8）——官方计费维度为生成原始时长，
	// mock 响应 duration=1.23 秒落 audioInputSeconds，不再落字符口径
	// ttsInputChars（目录秒价已落值 $0.00238095/秒，成本照算）。
	record := waitGlmSpoolRecord(t, spoolDir, func(text string) bool {
		return strings.Contains(text, `"audioInputSeconds":1.23`) && strings.Contains(text, `"endpoint":"POST /v1/audio/speech"`)
	}, "volcengine tts 生成时长计量终态记录")
	if strings.Contains(record, `"ttsInputChars"`) {
		t.Fatalf("volcengine tts 维度修正后不得再落字符口径: %s", record)
	}
}

// TestChainMediaVolcengineSpeechMissingCredentials 钉住能力语义（契约 §9.2 +
// 流式零计费/TTS 凭据修复）：volcengine 账户勾选 audio_speech 但缺语音双值
// 凭据 → 确定性本地错误（400 + speech_credentials_missing，message 保留
// "缺少火山语音凭据"原文语义）——凭据缺失是账户配置缺位，不是可重试的上游
// 临时故障，不得落入 503 upstream_retryable_error / 未预期异常渲染；零上游
// 出站、不静默回退。
func TestChainMediaVolcengineSpeechMissingCredentials(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	t.Setenv(chainVolcengineSpeechUpstreamEnv, mock.URL)
	apiKey := seedMediaVolcengineSpeechAccount(t, fixture, "acc_volcengine_speech_nocred", mock.URL, "ark-key-nocred", false)

	chain := composeMediaVideoChain(t, fixture, filepath.Join(t.TempDir(), "spool"))
	server := httptest.NewServer(chain)
	defer server.Close()

	response, payload := mediaVideoClientDo(t, server.URL, http.MethodPost, "/v1/audio/speech", apiKey,
		`{"model":"doubao-tts","input":"hi","voice":"BV700_streaming"}`, nil)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("缺凭据 speech status=%d, want 400（确定性配置错误，非 503 retryable/未预期异常）body=%s", response.StatusCode, payload)
	}
	if !strings.Contains(string(payload), "speech_credentials_missing") || !strings.Contains(string(payload), "缺少火山语音凭据") {
		t.Fatalf("缺凭据 speech 错误载荷缺少确定性 code/message: %s", payload)
	}
	if len(mock.Requests()) != 0 {
		t.Fatalf("缺凭据不得打上游: %+v", mock.Requests())
	}
}

// seedMediaOpenAISpeechAccount 种 openai 分组 + TTS 直连透传对照账户（契约
// §9.2 分派语义回归面：openai 族 speech 维持直连透传，不做本地
// ParseSpeechRequest L1 校验）。凭据带 api_key/base_url 与
// supported_endpoint_modes（audio_speech opt-in），模型约束种 tts-1。
func seedMediaOpenAISpeechAccount(t *testing.T, fixture *chainFixture, id, baseURL, apiKey string) string {
	t.Helper()
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(fixtureBind(query), args...); err != nil {
			t.Fatalf("seed openai speech row: %v: %v", query, err)
		}
	}
	keySecret := "sk-chain-openai-speech"
	credentials := map[string]any{
		"api_key":  apiKey,
		"base_url": baseURL,
		"supported_endpoint_modes": []string{
			"audio_speech",
		},
	}
	seed(`INSERT INTO groups (id, system_account_id, enabled, group_type) VALUES ('group_openai_speech', ?, 1, 'personal')`, fixture.systemAccount)
	seed(`INSERT INTO accounts (id, system_account_id, provider_protocol_profile_id, protocol_code, protocol_version, name, type, status, schedulable, concurrency_limit, priority, credentials_encrypted, deleted_at) VALUES (?, ?, 'prof_1', 'openai', 'v1', ?, 'api_key', 'active', 1, 0, 0, ?, NULL)`,
		id, fixture.systemAccount, id, mustEncryptCredentials(t, credentials))
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
		VALUES ('group_openai_speech', ?, ?, 1, '2026-10-04T00:00:00.000Z')`, fixture.systemAccount, id)
	seed(`INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, status, supported_endpoint_modes_json, created_at, updated_at)
		VALUES ('mdl_openai_tts1', 'openai', 'tts-1', 'tts-1', 'global', 'active', '["audio_speech"]', '2026-10-04T00:00:00.000Z', '2026-10-04T00:00:00.000Z') ON CONFLICT (id) DO NOTHING`)
	seed(`INSERT INTO account_supported_models (account_id, model, model_id, created_at)
		VALUES (?, 'tts-1', 'mdl_openai_tts1', '2026-10-04T00:00:00.000Z')`, id)
	// B4 第一批单连接事实源：BaseURL 与 mode 集改连接行。
	chainSeedAccountConnection(t, fixture.db, id, baseURL, "audio_speech")
	seed(`INSERT INTO route_strategies (id, system_account_id, name, mode, config_json, status)
		VALUES ('rs_openai_speech', ?, 'openai语音', 'normal', NULL, 'active')`, fixture.systemAccount)
	seed(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at)
		VALUES ('rsg_openai_speech', 'rs_openai_speech', ?, 'group_openai_speech', 0, 1, 'active', '2026-10-04T00:00:00.000Z')`, fixture.systemAccount)
	seed(`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, status, scheduling_preference_id, created_at)
		VALUES ('key_openai_speech', ?, 'rs_openai_speech', 'openai语音Key', ?, 'active', 'schpref-builtin-chat-cost-first', '2026-10-04T00:00:00.000Z')`,
		fixture.systemAccount, gatewayruntimecache.HashSecret(keySecret))
	return keySecret
}

// TestChainMediaVolcengineSpeechMissingVoiceLocal400 钉住分派语义（契约 §9.2
// 「分派语义（2026-10-17 实测回填批）」）：volcengine 账户 + speech 路径 +
// 合法 JSON body 但缺 voice（OpenAI 语义必填）→ 网关本地 400
// invalid_gateway_request、消息透传 ParseSpeechRequest 原文（"语音合成请求
// 缺少 voice"）、零上游出站——不得静默落 openai 通用透传链把参数错误伪装成
// 503 upstream_retryable_error（真实复现：出站 URL 曾错拼
// ark/api/v3/v1/audio/speech 打上游 404）。
func TestChainMediaVolcengineSpeechMissingVoiceLocal400(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	t.Setenv(chainVolcengineSpeechUpstreamEnv, mock.URL)
	apiKey := seedMediaVolcengineSpeechAccount(t, fixture, "acc_volcengine_speech_novoice", mock.URL, "ark-key-novoice", true)

	chain := composeMediaVideoChain(t, fixture, filepath.Join(t.TempDir(), "spool"))
	server := httptest.NewServer(chain)
	defer server.Close()

	response, payload := mediaVideoClientDo(t, server.URL, http.MethodPost, "/v1/audio/speech", apiKey,
		`{"model":"doubao-tts","input":"你好火山"}`, nil)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("缺 voice speech status=%d, want 400（本地校验错误，非 503 upstream_retryable）body=%s", response.StatusCode, payload)
	}
	if !strings.Contains(string(payload), "invalid_gateway_request") {
		t.Fatalf("缺 voice speech 错误载荷缺少 code invalid_gateway_request: %s", payload)
	}
	if !strings.Contains(string(payload), "voice") {
		t.Fatalf("缺 voice speech 错误消息须透传 ParseSpeechRequest 原文（语音合成请求缺少 voice）: %s", payload)
	}
	if len(mock.Requests()) != 0 {
		t.Fatalf("参数校验失败不得打上游（旧行为会以 openai 透传 URL 出站 404）: %+v", mock.Requests())
	}
}

// TestChainMediaOpenAISpeechMissingVoicePassthrough 钉住通用面回归边界（契约
// §9.2 分派语义的对照面）：openai 账户 + 同样缺 voice 的 speech body → 维持
// 既有直连透传行为（本地不做 ParseSpeechRequest L1 校验，请求原样出站
// /v1/audio/speech），不得被 volcengine 修复连带 400。
func TestChainMediaOpenAISpeechMissingVoicePassthrough(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	apiKey := seedMediaOpenAISpeechAccount(t, fixture, "acc_openai_speech_novoice", mock.URL, "sk-openai-tts-a")

	chain := composeMediaVideoChain(t, fixture, filepath.Join(t.TempDir(), "spool"))
	server := httptest.NewServer(chain)
	defer server.Close()

	response, payload := mediaVideoClientDo(t, server.URL, http.MethodPost, "/v1/audio/speech", apiKey,
		`{"model":"tts-1","input":"hi"}`, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("openai 缺 voice speech status=%d, want 200（透传语义维持，voice 校验交上游）body=%s", response.StatusCode, payload)
	}
	requests := mock.Requests()
	if len(requests) != 1 || requests[0].Path != "/v1/audio/speech" {
		t.Fatalf("openai speech 出站请求形态错误: %+v", requests)
	}
	if strings.Contains(requests[0].Body, `"voice"`) {
		t.Fatalf("openai 透传不得私造 voice 字段: %s", requests[0].Body)
	}
}

// TestChainDriverMinimaxSpeechMissingVoiceValidationError 在 driver 分派层钉
// 住 minimax 同构修复（与 volcengineSpeechRequest 同根因：ParseSpeechRequest
// 失败曾静默 ok=false 落通用链，参数错误会被送上游 /v1/audio/speech 404 后
// 伪装成 503）：缺 voice → GatewayRequestValidationError（400
// invalid_gateway_request，消息含 voice）；非 minimax 账户保持"非本
// provider"既有语义（ok=false 且无错误）。
func TestChainDriverMinimaxSpeechMissingVoiceValidationError(t *testing.T) {
	driver := newChainProviderDriver()
	account := gatewaydispatch.AccountCandidate{
		ID:           "acc_mnx_tts",
		VendorCode:   "minimax",
		ProtocolCode: "openai",
		Type:         "api_key",
		BaseURL:      "https://mnx.example",
		APIKey:       "mk-1",
	}
	req := mappedURLTestRequest(t, http.MethodPost, "/v1/audio/speech",
		`{"model":"speech-01","input":"hi"}`)

	if _, _, ok, err := driver.minimaxSpeechRequest(req, account); err == nil || ok {
		t.Fatalf("minimax speech 缺 voice 必须显式报错（不得静默 ok=false）: ok=%v err=%v", ok, err)
	} else {
		var validation *gatewaypreauth.GatewayRequestValidationError
		if !errors.As(err, &validation) {
			t.Fatalf("错误必须是 GatewayRequestValidationError: %v", err)
		}
		if validation.Code != "invalid_gateway_request" || validation.StatusCode != http.StatusBadRequest {
			t.Fatalf("code/status = %q/%d, want invalid_gateway_request/400", validation.Code, validation.StatusCode)
		}
		if !strings.Contains(err.Error(), "voice") {
			t.Fatalf("消息须透传 ParseSpeechRequest 原文（语音合成请求缺少 voice）: %v", err)
		}
	}

	openaiAccount := gatewaydispatch.AccountCandidate{
		ID:           "acc_oai_tts",
		VendorCode:   "gpt",
		ProtocolCode: "openai",
		Type:         "api_key",
		BaseURL:      "https://api.openai.example",
		APIKey:       "sk-1",
	}
	if _, _, ok, err := driver.minimaxSpeechRequest(req, openaiAccount); ok || err != nil {
		t.Fatalf("非 minimax 账户必须保持非本 provider 语义（ok=false 无错误）: ok=%v err=%v", ok, err)
	}
}

// TestChainDriverVolcengineSpeechMissingVoiceValidationError 在 URL 构造消费点
// 钉住 volcengine 缺 voice 的本地校验失败（真实复现路径：URL 构造层曾静默落
// openai 通用归一 BuildUpstreamURL(base, /v1/audio/speech)，把参数错误送上游
// 404 后以 503 upstream_retryable_error 伪装）。
func TestChainDriverVolcengineSpeechMissingVoiceValidationError(t *testing.T) {
	driver := newChainProviderDriver()
	account := gatewaydispatch.AccountCandidate{
		ID:           "acc_volc_tts",
		VendorCode:   "volcengine",
		ProtocolCode: "openai",
		Type:         "api_key",
		BaseURL:      "https://ark.cn-beijing.volces.com",
		APIKey:       "ark-1",
	}
	req := mappedURLTestRequest(t, http.MethodPost, "/v1/audio/speech",
		`{"model":"doubao-tts","input":"hi"}`)
	urls, err := driver.BuildGatewayUpstreamURLsForAccount(context.Background(), account, req)
	if err == nil {
		t.Fatalf("volcengine speech 缺 voice 必须本地校验失败，不得构造 openai 透传出站 URL: %#v", urls)
	}
	var validation *gatewaypreauth.GatewayRequestValidationError
	if !errors.As(err, &validation) || validation.Code != "invalid_gateway_request" {
		t.Fatalf("错误必须是 invalid_gateway_request validation: %v", err)
	}
	if !strings.Contains(err.Error(), "voice") {
		t.Fatalf("消息须透传 ParseSpeechRequest 原文（语音合成请求缺少 voice）: %v", err)
	}
}

// TestChainMediaVolcengineSpeechAliasModelCrossName 是「派发匹配等价」候选
// 窗口层的跨名回归钉（契约 §9.2，2026-10-17 实测回填批评审应修-3）：账户
// supportedModels 只声明占位名 doubao-tts（seedMediaVolcengineSpeechAccount
// 的播种形态），客户端用官方现役名 seed-audio-1.0 请求——候选窗口 SQL 的
// direct 谓词按等价组展开 IN 后账户必须命中并完整走通出站（mock 收到
// /api/v3/tts/create + 报文 model 归一为现役名）。若候选窗口 SQL 的 IN 展开
// 退化为精确匹配，本用例 503 endpoint_mode_unsupported。
func TestChainMediaVolcengineSpeechAliasModelCrossName(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	t.Setenv(chainVolcengineSpeechUpstreamEnv, mock.URL)
	apiKey := seedMediaVolcengineSpeechAccount(t, fixture, "acc_volcengine_speech_alias", mock.URL, "ark-key-a", true)

	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain := composeMediaVideoChain(t, fixture, spoolDir)
	server := httptest.NewServer(chain)
	defer server.Close()

	response, payload := mediaVideoClientDo(t, server.URL, http.MethodPost, "/v1/audio/speech", apiKey,
		`{"model":"seed-audio-1.0","input":"你好火山","voice":"BV700_streaming","response_format":"mp3"}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaVolcengineTTSOK)})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("跨名请求 status=%d body=%s（候选窗口等价展开失效）", response.StatusCode, payload)
	}
	requests := mock.Requests()
	if len(requests) == 0 {
		t.Fatal("mock 未收到出站请求（跨名候选未命中账户）")
	}
	found := false
	for _, request := range requests {
		if strings.Contains(request.Path, "/api/v3/tts/create") &&
			strings.Contains(request.Body, `"model":"seed-audio-1.0"`) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("出站未命中 /api/v3/tts/create + 现役名透传: %+v", requests)
	}
	_ = apiKey
}

// seedMediaVolcengineASRAccount 种 volcengine 分组 + 录音文件识别 2.0 测试
// 账户 + 独立路由策略与 API Key（独立 group/rs/key 三件，避免跨 provider
// 候选串扰），返回网关 API Key 明文。凭据带 speech_token（X-Api-Key 出站
// 凭据，长转写与 TTS 同字段）与 supported_endpoint_modes（audio_job_* 四值，
// opt-in）。模型约束种 volc.seedasr.auc。base_url 种 ark 官方根占位——长
// 转写出站不经 base_url（openspeech 语音服务域 URL 构造特例），链级经
// chainVolcengineASRUpstreamEnv seam 指向 mock。
func seedMediaVolcengineASRAccount(t *testing.T, fixture *chainFixture, id, apiKey, speechToken string) string {
	t.Helper()
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(fixtureBind(query), args...); err != nil {
			t.Fatalf("seed volcengine asr row: %v: %v", query, err)
		}
	}
	keySecret := "sk-chain-volc-asr"
	credentials := map[string]any{
		"api_key":      apiKey,
		"base_url":     "https://ark.cn-beijing.volces.com",
		"speech_token": speechToken,
		"supported_endpoint_modes": []string{
			"audio_job_create", "audio_job_get", "audio_job_content", "audio_job_cancel",
		},
	}
	seed(`INSERT INTO groups (id, system_account_id, enabled, group_type) VALUES ('group_volc_asr', ?, 1, 'personal')`, fixture.systemAccount)
	seed(`INSERT INTO accounts (id, system_account_id, vendor_code, provider_protocol_profile_id, protocol_code, protocol_version, name, type, status, schedulable, concurrency_limit, priority, credentials_encrypted, deleted_at) VALUES (?, ?, 'volcengine', 'profile_volcengine_openai_v1', 'openai', 'v1', ?, 'api_key', 'active', 1, 0, 0, ?, NULL)`,
		id, fixture.systemAccount, id, mustEncryptCredentials(t, credentials))
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
		VALUES ('group_volc_asr', ?, ?, 1, '2026-10-17T00:00:00.000Z')`, fixture.systemAccount, id)
	seed(`INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, status, supported_endpoint_modes_json, created_at, updated_at)
		VALUES ('mdl_volc_seedasr', 'volcengine', 'volc.seedasr.auc', 'volc.seedasr.auc', 'global', 'active', '["audio_job_create","audio_job_get","audio_job_content","audio_job_cancel"]', '2026-10-17T00:00:00.000Z', '2026-10-17T00:00:00.000Z') ON CONFLICT (id) DO NOTHING`)
	seed(`INSERT INTO account_supported_models (account_id, model, model_id, created_at)
		VALUES (?, 'volc.seedasr.auc', 'mdl_volc_seedasr', '2026-10-17T00:00:00.000Z')`, id)
	// B4 第一批单连接事实源：BaseURL 与 mode 集改连接行（volc ASR base_url
	// 在凭据中固定为 ark 域名）。
	chainSeedAccountConnection(t, fixture.db, id, "https://ark.cn-beijing.volces.com", "audio_job_create", "audio_job_get", "audio_job_content", "audio_job_cancel")
	seed(`INSERT INTO route_strategies (id, system_account_id, name, mode, config_json, status)
		VALUES ('rs_volc_asr', ?, 'volc长转写', 'normal', NULL, 'active')`, fixture.systemAccount)
	seed(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at)
		VALUES ('rsg_volc_asr', 'rs_volc_asr', ?, 'group_volc_asr', 0, 1, 'active', '2026-10-17T00:00:00.000Z')`, fixture.systemAccount)
	seed(`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, status, scheduling_preference_id, created_at)
		VALUES ('key_volc_asr', ?, 'rs_volc_asr', 'volc长转写Key', ?, 'active', 'schpref-builtin-chat-cost-first', '2026-10-17T00:00:00.000Z')`,
		fixture.systemAccount, gatewayruntimecache.HashSecret(keySecret))
	return keySecret
}

// TestChainMediaVolcengineASRLifecycle 覆盖火山长转写臂全生命周期（契约
// §9.2）：创建（头驱动 submit——X-Api-Key 凭据 + 资源 ID/请求 ID/序列三头
// + 报文映射与 URL 特例）→ 轮询 20000001 in_progress → 20000000 completed
// （毫秒计量 ÷1000 落 audio_input_seconds + 目录秒价计费）→ content 同构
// query 代理转写 JSON → 终态 usage 行。
func TestChainMediaVolcengineASRLifecycle(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	t.Setenv(chainVolcengineSpeechUpstreamEnv, mock.URL)
	apiKey := seedMediaVolcengineASRAccount(t, fixture, "acc_volc_asr", "sk-volc-asr-a", "ark-test-speech-token")

	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain := composeMediaVideoChain(t, fixture, spoolDir)
	server := httptest.NewServer(chain)
	defer server.Close()
	defer mock.Close()

	job := mediaAudioJobCreate(t, server.URL, apiKey,
		`{"model":"volc.seedasr.auc","input_url":"https://oss.example.com/audio/meeting.wav"}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaVolcengineASRCreateOK)})
	jobID, _ := job["id"].(string)
	if jobID == "" || !strings.HasPrefix(jobID, "audiojob_") {
		t.Fatalf("对外 job id 缺失或非 audiojob_ 前缀: %#v", job)
	}
	if job["object"] != "audio_job" || job["provider"] != "volcengine" {
		t.Fatalf("object/provider = %v/%v, want audio_job/volcengine", job["object"], job["provider"])
	}
	if job["status"] != "queued" {
		t.Fatalf("创建响应 status = %v, want queued（submit 受理归一）", job["status"])
	}
	providerJobID, _ := job["provider_job_id"].(string)
	if len(providerJobID) != 32 {
		t.Fatalf("provider_job_id = %q, want 16 字节 hex 任务 ID（网关生成请求 ID）", providerJobID)
	}

	// 创建出站断言：POST /api/v3/auc/bigmodel/submit + 头驱动三件套 +
	// X-Api-Key 凭据（speech_token）+ 无 Authorization 头 + 报文映射。
	requests := mock.Requests()
	if len(requests) != 1 || requests[0].Method != http.MethodPost || requests[0].Path != "/api/v3/auc/bigmodel/submit" {
		t.Fatalf("创建出站形态错误: %+v", requests)
	}
	create := requests[0]
	if create.APIKeyHeader != "ark-test-speech-token" {
		t.Fatalf("创建 X-Api-Key = %q, want speech_token 凭据", create.APIKeyHeader)
	}
	if create.AuthHeader != "" {
		t.Fatalf("长转写出站不得携带 Authorization: %q", create.AuthHeader)
	}
	if create.ResourceIDHeader != "volc.seedasr.auc" {
		t.Fatalf("创建 X-Api-Resource-Id = %q, want volc.seedasr.auc", create.ResourceIDHeader)
	}
	if create.RequestIDHeader != providerJobID {
		t.Fatalf("创建 X-Api-Request-Id = %q, want 网关生成任务 ID %q", create.RequestIDHeader, providerJobID)
	}
	for _, want := range []string{
		`"url":"https://oss.example.com/audio/meeting.wav"`,
		`"format":"wav"`,
		`"model_name":"bigmodel"`,
	} {
		if !strings.Contains(create.Body, want) {
			t.Fatalf("submit 报文缺少 %s: %s", want, create.Body)
		}
	}

	// 轮询两次：#1 20000001 → in_progress；#2 20000000 + duration 1118ms →
	// completed（计量 1.118 秒）。query 面与 submit 同构头。
	for index, want := range []string{"in_progress", "completed"} {
		response, payload := mediaVideoClientDo(t, server.URL, http.MethodGet, "/v1/audio/jobs/"+jobID, apiKey, "", nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("poll #%d status=%d body=%s", index+1, response.StatusCode, payload)
		}
		if object := mediaVideoDecodeJob(t, payload); object["status"] != want {
			t.Fatalf("poll #%d status = %v, want %s", index+1, object["status"], want)
		}
	}
	requests = mock.Requests()
	if len(requests) != 3 {
		t.Fatalf("出站请求计数 = %d, want 3（submit + query×2）", len(requests))
	}
	for _, request := range requests[1:] {
		if request.Method != http.MethodPost || request.Path != "/api/v3/auc/bigmodel/query" {
			t.Fatalf("轮询出站形态错误: %+v", request)
		}
		if request.RequestIDHeader != providerJobID || request.ResourceIDHeader != "volc.seedasr.auc" {
			t.Fatalf("轮询头错误: requestID=%q resourceID=%q", request.RequestIDHeader, request.ResourceIDHeader)
		}
		if request.Body != "{}" {
			t.Fatalf("query body = %q, want {}", request.Body)
		}
	}

	// media_jobs 行终态：completed + 任务 ID + 按目录秒价计费（1.118s ×
	// $3.1746031746031745e-05，volc.seedasr.auc 现役行 0.8 元/小时 ÷3600
	// ÷7.0；落库值经 fixedNumber 10 位小数舍入 3.5492063492...→3.54921e-05）。
	var kind, status, upstreamJobID string
	var costUsd float64
	if err := fixture.db.QueryRow(`SELECT kind, status, upstream_job_id, cost_usd FROM media_jobs WHERE id = ?`, jobID).Scan(&kind, &status, &upstreamJobID, &costUsd); err != nil {
		t.Fatalf("查询 media_jobs 行: %v", err)
	}
	if kind != "audio_transcription" || status != "completed" || upstreamJobID != providerJobID {
		t.Fatalf("media_jobs = %s/%s/%s, want audio_transcription/completed/%s", kind, status, upstreamJobID, providerJobID)
	}
	if costUsd != 3.54921e-05 {
		t.Fatalf("cost_usd = %v, want 3.54921e-05（1.118s × 目录秒价，fixedNumber 10 位小数舍入）", costUsd)
	}

	// content 下载：同构 query 代理——终态转写 JSON 即产物（无产物 URL），
	// 携带任务头与凭据直发上游。
	response, payload := mediaVideoClientDo(t, server.URL, http.MethodGet, "/v1/audio/jobs/"+jobID+"/content", apiKey, "", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("content status=%d body=%s", response.StatusCode, payload)
	}
	if got := response.Header.Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("content-type = %q, want application/json（转写 JSON 即产物）", got)
	}
	if !strings.Contains(string(payload), `"text":"Carry an oily rag."`) || !strings.Contains(string(payload), `"duration":1118`) {
		t.Fatalf("content 不是终态转写 payload: %s", payload)
	}
	if len(mock.Requests()) != 4 {
		t.Fatalf("content 应再发一次同构 query（submit+query×2+content query=4）: %d", len(mock.Requests()))
	}

	// 终态 usage：spool 落盘 audioInputSeconds=1.118（毫秒 ÷1000）+
	// endpoint=/v1/audio/jobs + 目录秒价成本行。
	waitGlmSpoolRecord(t, spoolDir, func(text string) bool {
		return strings.Contains(text, `"audioInputSeconds":1.118`) && strings.Contains(text, `"endpoint":"/v1/audio/jobs"`)
	}, "volcengine asr completed 时长计量终态记录")
}

// TestChainMediaVolcengineASRPollFailedTerminal 覆盖轮询失败终态（契约 §9.2
// 状态归一 450000xx→failed）：mock 场景 _poll_failed（#1 即 45000006 音频
// 拉取失败）——轮询归一 failed 落库（error 随行回显）+ 无计量不虚计（cost 0
// + usage_missing），failure 任务不产生 audioInputSeconds 行。
func TestChainMediaVolcengineASRPollFailedTerminal(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	t.Setenv(chainVolcengineSpeechUpstreamEnv, mock.URL)
	apiKey := seedMediaVolcengineASRAccount(t, fixture, "acc_volc_asr_fail", "sk-volc-asr-f", "ark-test-speech-token")

	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain := composeMediaVideoChain(t, fixture, spoolDir)
	server := httptest.NewServer(chain)
	defer server.Close()
	defer mock.Close()

	job := mediaAudioJobCreate(t, server.URL, apiKey,
		`{"model":"volc.seedasr.auc","input_url":"https://oss.example.com/audio/offshore.wav"}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaVolcengineASRPollFailed)})
	jobID, _ := job["id"].(string)
	if jobID == "" {
		t.Fatalf("对外 job id 缺失: %#v", job)
	}
	// 首次轮询即终态失败（脚本 #1 = 45000006）。
	response, payload := mediaVideoClientDo(t, server.URL, http.MethodGet, "/v1/audio/jobs/"+jobID, apiKey, "", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("poll status=%d body=%s", response.StatusCode, payload)
	}
	object := mediaVideoDecodeJob(t, payload)
	if object["status"] != "failed" {
		t.Fatalf("poll status = %v, want failed（45000006 归一）", object["status"])
	}
	var kind, status string
	var costUsd float64
	if err := fixture.db.QueryRow(`SELECT kind, status, cost_usd FROM media_jobs WHERE id = ?`, jobID).Scan(&kind, &status, &costUsd); err != nil {
		t.Fatalf("查询 media_jobs 行: %v", err)
	}
	if kind != "audio_transcription" || status != "failed" {
		t.Fatalf("media_jobs = %s/%s, want audio_transcription/failed", kind, status)
	}
	if costUsd != 0 {
		t.Fatalf("failed 任务 cost_usd = %v, want 0（失败不虚计）", costUsd)
	}
	// 终态 usage 行：failed 任务的 usage 行以 success=false 表达（usageMissing
	// 标记只用于 completed 无计量形态），无 audioInputSeconds 行项、无成本。
	waitGlmSpoolRecord(t, spoolDir, func(text string) bool {
		return strings.Contains(text, `"endpoint":"/v1/audio/jobs"`) && strings.Contains(text, `"success":false`) && !strings.Contains(text, `"audioInputSeconds"`)
	}, "volcengine asr failed 终态 usage 行")
}

// TestChainMediaVolcengineASRMissingCredentials 覆盖缺凭据显式失败（契约
// §9.2：speech_token 缺失即该账户无长转写能力，派发出站时显式 400 不静默
// 回退，稳定错误码 speech_credentials_missing——同 TTS lane 语义）。
func TestChainMediaVolcengineASRMissingCredentials(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	t.Setenv(chainVolcengineSpeechUpstreamEnv, mock.URL)
	keySecret := "sk-chain-volc-asr-nocred"
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(fixtureBind(query), args...); err != nil {
			t.Fatalf("seed volcengine asr nocred row: %v: %v", query, err)
		}
	}
	credentials := map[string]any{
		"api_key":  "sk-volc-asr-nocred",
		"base_url": "https://ark.cn-beijing.volces.com",
		"supported_endpoint_modes": []string{
			"audio_job_create", "audio_job_get", "audio_job_content", "audio_job_cancel",
		},
	}
	seed(`INSERT INTO groups (id, system_account_id, enabled, group_type) VALUES ('group_volc_asr_nc', ?, 1, 'personal')`, fixture.systemAccount)
	seed(`INSERT INTO accounts (id, system_account_id, vendor_code, provider_protocol_profile_id, protocol_code, protocol_version, name, type, status, schedulable, concurrency_limit, priority, credentials_encrypted, deleted_at) VALUES ('acc_volc_asr_nc', ?, 'volcengine', 'profile_volcengine_openai_v1', 'openai', 'v1', 'acc_volc_asr_nc', 'api_key', 'active', 1, 0, 0, ?, NULL)`,
		fixture.systemAccount, mustEncryptCredentials(t, credentials))
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
		VALUES ('group_volc_asr_nc', ?, 'acc_volc_asr_nc', 1, '2026-10-17T00:00:00.000Z')`, fixture.systemAccount)
	seed(`INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, status, supported_endpoint_modes_json, created_at, updated_at)
		VALUES ('mdl_volc_seedasr', 'volcengine', 'volc.seedasr.auc', 'volc.seedasr.auc', 'global', 'active', '["audio_job_create","audio_job_get","audio_job_content","audio_job_cancel"]', '2026-10-17T00:00:00.000Z', '2026-10-17T00:00:00.000Z') ON CONFLICT (id) DO NOTHING`)
	seed(`INSERT INTO account_supported_models (account_id, model, model_id, created_at)
		VALUES ('acc_volc_asr_nc', 'volc.seedasr.auc', 'mdl_volc_seedasr', '2026-10-17T00:00:00.000Z')`)
	// B4 第一批单连接事实源：nc 账户也需要 enabled 连接行才能水合（缺的是
	// speech_token 凭据，不是连接面）。
	chainSeedAccountConnection(t, fixture.db, "acc_volc_asr_nc", "https://ark.cn-beijing.volces.com", "audio_job_create", "audio_job_get", "audio_job_content", "audio_job_cancel")
	seed(`INSERT INTO route_strategies (id, system_account_id, name, mode, config_json, status)
		VALUES ('rs_volc_asr_nc', ?, 'volc长转写无凭据', 'normal', NULL, 'active')`, fixture.systemAccount)
	seed(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at)
		VALUES ('rsg_volc_asr_nc', 'rs_volc_asr_nc', ?, 'group_volc_asr_nc', 0, 1, 'active', '2026-10-17T00:00:00.000Z')`, fixture.systemAccount)
	seed(`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, status, scheduling_preference_id, created_at)
		VALUES ('key_volc_asr_nc', ?, 'rs_volc_asr_nc', 'volc长转写无凭据Key', ?, 'active', 'schpref-builtin-chat-cost-first', '2026-10-17T00:00:00.000Z')`,
		fixture.systemAccount, gatewayruntimecache.HashSecret(keySecret))

	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain := composeMediaVideoChain(t, fixture, spoolDir)
	server := httptest.NewServer(chain)
	defer server.Close()
	defer mock.Close()

	response, payload := mediaVideoClientDo(t, server.URL, http.MethodPost, "/v1/audio/jobs", keySecret,
		`{"model":"volc.seedasr.auc","input_url":"https://oss.example.com/audio/meeting.wav"}`, nil)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("缺凭据创建 status=%d body=%s, want 400（本地确定性配置错误）", response.StatusCode, payload)
	}
	if !strings.Contains(string(payload), "speech_credentials_missing") {
		t.Fatalf("缺凭据错误码缺失: %s", payload)
	}
	if requests := mock.Requests(); len(requests) != 0 {
		t.Fatalf("缺凭据不得出站: %+v", requests)
	}
}

// TestChainMediaVolcengineSpeechStreamFullChain 覆盖火山流式语音桥接 V1a
// 全链（seed-tts-2.0 → POST /api/v3/tts/unidirectional NDJSON 分块）：出站
// 路径与 X-Api-Key 单头（speech_token 沿用，Authorization 不出站）、req_params
// 报文（text/speaker/audio_params.format）、NDJSON 多分块 base64 拼接后二
// 进制 mp3 透传（frame sync）、usage 字符口径（ttsInputChars，官方计费维度
// 为字符——与 1.0 同步面的时长口径按 model 分流）+ 目录字符价成本落库。
func TestChainMediaVolcengineSpeechStreamFullChain(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	t.Setenv(chainVolcengineSpeechUpstreamEnv, mock.URL)
	apiKey := seedMediaVolcengineSpeechAccount(t, fixture, "acc_volcengine_speech_stream", mock.URL, "ark-key-stream", true)
	if _, err := fixture.db.Exec(`INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, status, supported_endpoint_modes_json, created_at, updated_at)
		VALUES ('mdl_volc_seedtts2', 'volcengine', 'seed-tts-2.0', 'seed-tts-2.0', 'global', 'active', '["audio_speech"]', '2026-10-18T00:00:00.000Z', '2026-10-18T00:00:00.000Z') ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("seed seed-tts-2.0 models row: %v", err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO account_supported_models (account_id, model, model_id, created_at)
		VALUES ('acc_volcengine_speech_stream', 'seed-tts-2.0', 'mdl_volc_seedtts2', '2026-10-18T00:00:00.000Z')`); err != nil {
		t.Fatalf("seed stream model: %v", err)
	}
	// 目录行（fixture DB 列集无媒体四维——字符价由 go:embed 静态层补齐，
	// chainCatalogPricing 媒体单价权威源，见 chain_pricing.go）。
	if _, err := fixture.db.Exec(`INSERT INTO models (
			id, vendor_code, public_model, upstream_model, scope, status, supported_endpoint_modes_json, source, created_at, updated_at)
		VALUES ('cat_volc_seed_tts_2_0', 'volcengine', 'seed-tts-2.0', 'seed-tts-2.0', 'builtin', 'active', '["audio_speech"]', 'builtin',
			'2026-10-18T00:00:00.000Z', '2026-10-18T00:00:00.000Z')`); err != nil {
		t.Fatalf("seed stream catalog row: %v", err)
	}

	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain := composeMediaVideoChain(t, fixture, spoolDir)
	server := httptest.NewServer(chain)
	defer server.Close()

	response, payload := mediaVideoClientDo(t, server.URL, http.MethodPost, "/v1/audio/speech", apiKey,
		`{"model":"seed-tts-2.0","input":"你好火山流式","voice":"zh_female_demo_v2","response_format":"mp3"}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaVolcengineTTSStreamOK)})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("stream speech status=%d body=%s", response.StatusCode, payload)
	}
	if got := response.Header.Get("Content-Type"); !strings.Contains(got, "audio/mpeg") {
		t.Fatalf("content-type = %q, want audio/mpeg", got)
	}
	if len(payload) < 4 || payload[0] != 0xFF || payload[1] != 0xFB {
		t.Fatalf("响应不是拼接解码后的 mp3 载荷: % x", payload[:min(4, len(payload))])
	}

	// 出站断言：POST /api/v3/tts/unidirectional + X-Api-Key 单头 + req_params 报文。
	requests := mock.Requests()
	if len(requests) != 1 || requests[0].Method != http.MethodPost || requests[0].Path != "/api/v3/tts/unidirectional" {
		t.Fatalf("stream speech 出站请求形态错误: %+v", requests)
	}
	if requests[0].APIKeyHeader != "volc-tts-token-a" {
		t.Fatalf("stream speech X-Api-Key = %q", requests[0].APIKeyHeader)
	}
	if requests[0].AuthHeader != "" {
		t.Fatalf("stream speech Authorization = %q, want 空", requests[0].AuthHeader)
	}
	body := requests[0].Body
	for _, want := range []string{
		`"req_params"`,
		`"text":"你好火山流式"`,
		`"speaker":"zh_female_demo_v2"`,
		`"audio_params"`,
		`"format":"mp3"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("出站报文缺少 %s: %s", want, body)
		}
	}
	for _, forbidden := range []string{`"text_prompt"`, `"audio_config"`, `"model"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("流式面出站报文不得残留 1.0 同步面/模型键 %s: %s", forbidden, body)
		}
	}

	// usage：字符口径（官方计费维度为字符）——6 rune 输入 → ttsInputChars=6；
	// 成本按目录字符价（3 元/万字符 ÷7.0）落库，不落时长口径。
	record := waitGlmSpoolRecord(t, spoolDir, func(text string) bool {
		return strings.Contains(text, `"ttsInputChars":6`) && strings.Contains(text, `"endpoint":"POST /v1/audio/speech"`)
	}, "volcengine 流式 tts 字符计量终态记录")
	if strings.Contains(record, `"audioInputSeconds"`) {
		t.Fatalf("流式面不得落时长口径: %s", record)
	}
	// 成本位说明：链内计价的媒体四维单价权威源是 go:embed 静态层
	//（chainCatalogPricing），字符价的引擎级断言由 pricing 包
	// TestVolcengineCatalogPriceAnchors 钉住（fixture 运行时目录列集不含
	// 媒体四维，与既有语音链测试同口径只断言计量）。

	// 失败臂：上游错误行（55000000 音色未授权实测形态）→ 502 透传 message。
	errorResponse, errorPayload := mediaVideoClientDo(t, server.URL, http.MethodPost, "/v1/audio/speech", apiKey,
		`{"model":"seed-tts-2.0","input":"x","voice":"unauthorized"}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaVolcengineTTSStreamFailed)})
	if errorResponse.StatusCode != http.StatusBadGateway {
		t.Fatalf("失败臂 status=%d, want 502 body=%s", errorResponse.StatusCode, errorPayload)
	}
	if !strings.Contains(string(errorPayload), "speaker related resource") {
		t.Fatalf("失败臂应透传上游 message: %s", errorPayload)
	}
}
