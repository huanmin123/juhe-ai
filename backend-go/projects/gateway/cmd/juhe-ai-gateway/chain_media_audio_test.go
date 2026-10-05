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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	platformmock "github.com/huanminabc/juhe-ai/backend-go-platform/mockupstream"
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
		if _, err := fixture.db.Exec(query, args...); err != nil {
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
	seed(`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type)
		VALUES ('group_qwen_asr', ?, 'qwen', 1, 'personal')`, fixture.systemAccount)
	seed(`INSERT INTO accounts (
			id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
			name, type, status, schedulable, concurrency_limit, priority, credentials_encrypted, deleted_at
		) VALUES (?, ?, 'qwen', 'profile_qwen_openai_v1', 'openai', 'v1', ?, 'api_key', 'active', 1, 0, 0, ?, NULL)`,
		id, fixture.systemAccount, id, mustEncryptCredentials(t, credentials))
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
		VALUES ('group_qwen_asr', ?, ?, 1, '2026-10-04T00:00:00.000Z')`, fixture.systemAccount, id)
	seed(`INSERT INTO account_supported_models (account_id, provider_code, model, created_at)
		VALUES (?, 'qwen', 'paraformer-v2', '2026-10-04T00:00:00.000Z')`, id)
	seed(`INSERT INTO route_strategies (id, system_account_id, name, mode, config_json, status)
		VALUES ('rs_qwen_asr', ?, 'qwen长转写', 'normal', NULL, 'active')`, fixture.systemAccount)
	seed(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at)
		VALUES ('rsg_qwen_asr', 'rs_qwen_asr', ?, 'group_qwen_asr', 0, 1, 'active', '2026-10-04T00:00:00.000Z')`, fixture.systemAccount)
	seed(`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, status, created_at)
		VALUES ('key_qwen_asr', ?, 'rs_qwen_asr', 'qwen长转写Key', ?, 'active', '2026-10-04T00:00:00.000Z')`,
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
	//（usage JSON 字符串 duration=62.5）按目录 USD 秒价计费（2026-10-05 用户
	// 裁决补价：paraformer-v2 0.00008 元/秒 ÷ 7.0 = $0.0000114/秒；本断言随
	// 该补价回正——补价落库后本用例断言未同步，属定价事实回正非语义变更）。
	var kind, status, upstreamJobID string
	var costUsd float64
	if err := fixture.db.QueryRow(`SELECT kind, status, upstream_job_id, cost_usd FROM media_jobs WHERE id = ?`, jobID).Scan(&kind, &status, &upstreamJobID, &costUsd); err != nil {
		t.Fatalf("查询 media_jobs 行: %v", err)
	}
	if kind != "audio_transcription" || status != "completed" || upstreamJobID != providerJobID {
		t.Fatalf("media_jobs = %s/%s/%s, want audio_transcription/completed/%s", kind, status, upstreamJobID, providerJobID)
	}
	if costUsd != 0.0007125 {
		t.Fatalf("paraformer 按目录秒价计费 cost_usd = %v, want 0.0007125（62.5s × $0.0000114/s）", costUsd)
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
		if _, err := fixture.db.Exec(query, args...); err != nil {
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
	seed(`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type)
		VALUES ('group_glm_speech', ?, 'glm', 1, 'personal')`, fixture.systemAccount)
	seed(`INSERT INTO accounts (
			id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
			name, type, status, schedulable, concurrency_limit, priority, credentials_encrypted, deleted_at
		) VALUES (?, ?, 'glm', 'profile_glm_general_openai_v1', 'openai', 'v1', ?, 'api_key', 'active', 1, 0, 0, ?, NULL)`,
		id, fixture.systemAccount, id, mustEncryptCredentials(t, credentials))
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
		VALUES ('group_glm_speech', ?, ?, 1, '2026-10-04T00:00:00.000Z')`, fixture.systemAccount, id)
	seed(`INSERT INTO account_supported_models (account_id, provider_code, model, created_at)
		VALUES (?, 'glm', 'cogtts', '2026-10-04T00:00:00.000Z')`, id)
	seed(`INSERT INTO route_strategies (id, system_account_id, name, mode, config_json, status)
		VALUES ('rs_glm_speech', ?, 'glm语音', 'normal', NULL, 'active')`, fixture.systemAccount)
	seed(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at)
		VALUES ('rsg_glm_speech', 'rs_glm_speech', ?, 'group_glm_speech', 0, 1, 'active', '2026-10-04T00:00:00.000Z')`, fixture.systemAccount)
	seed(`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, status, created_at)
		VALUES ('key_glm_speech', ?, 'rs_glm_speech', 'glm语音Key', ?, 'active', '2026-10-04T00:00:00.000Z')`,
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

// TestChainMediaGlmSpeechUpstreamURLDedup 钉住 glm 语音 URL 归一的服务根去重
//（契约 §7.2/CogVideo §7.1 同一先例）：base 已含 /api/paas/v4 时不重复拼接，
// 不含时直拼官方根——两种形态都不得出现 openai /v1 强制补缀。
func TestChainMediaGlmSpeechUpstreamURLDedup(t *testing.T) {
	dedup, err := chainGlmVideoUpstreamURL("https://open.bigmodel.cn/api/paas/v4", "/api/paas/v4/audio/speech")
	if err != nil {
		t.Fatalf("dedup url: %v", err)
	}
	if dedup != "https://open.bigmodel.cn/api/paas/v4/audio/speech" {
		t.Fatalf("base 含服务根时未去重: %q", dedup)
	}
	plain, err := chainGlmVideoUpstreamURL("https://open.bigmodel.cn", "/api/paas/v4/audio/speech")
	if err != nil {
		t.Fatalf("plain url: %v", err)
	}
	if plain != "https://open.bigmodel.cn/api/paas/v4/audio/speech" {
		t.Fatalf("base 不含服务根时未直拼官方根: %q", plain)
	}
}

// seedMediaVolcengineSpeechAccount 种 volcengine 分组 + TTS 测试账户 + 独立
// 路由策略与 API Key（M6，契约 §9.2）。凭据带 ark API Key、base_url 与
// **语音应用双值 speech_appid/speech_token**（凭据归一化 M6 放行键）与
// supported_endpoint_modes（openai 族词表 audio_speech，opt-in 语义）。模型
// 约束种统一面占位名 doubao-tts（网关模型门要求账户显式声明支持模型——
// V3 TTS 无官方模型面、官方目录不落行，占位名经运营自定义目录行承载，
// 值不透传上游，见《火山方舟账号接入.md》TTS 节取舍说明）。
func seedMediaVolcengineSpeechAccount(t *testing.T, fixture *chainFixture, id, baseURL, arkKey string, withSpeechCredentials bool) string {
	t.Helper()
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(query, args...); err != nil {
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
		credentials["speech_appid"] = "app-voice-123"
		credentials["speech_token"] = "volc-tts-token-a"
	}
	seed(`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type)
		VALUES ('group_volcengine_speech', ?, 'volcengine', 1, 'personal')`, fixture.systemAccount)
	seed(`INSERT INTO accounts (
			id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
			name, type, status, schedulable, concurrency_limit, priority, credentials_encrypted, deleted_at
		) VALUES (?, ?, 'volcengine', 'profile_volcengine_openai_v1', 'openai', 'v1', ?, 'api_key', 'active', 1, 0, 0, ?, NULL)`,
		id, fixture.systemAccount, id, mustEncryptCredentials(t, credentials))
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
		VALUES ('group_volcengine_speech', ?, ?, 1, '2026-10-04T00:00:00.000Z')`, fixture.systemAccount, id)
	seed(`INSERT INTO account_supported_models (account_id, provider_code, model, created_at)
		VALUES (?, 'volcengine', 'doubao-tts', '2026-10-04T00:00:00.000Z')`, id)
	seed(`INSERT INTO route_strategies (id, system_account_id, name, mode, config_json, status)
		VALUES ('rs_volcengine_speech', ?, 'volcengine语音', 'normal', NULL, 'active')`, fixture.systemAccount)
	seed(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at)
		VALUES ('rsg_volcengine_speech', 'rs_volcengine_speech', ?, 'group_volcengine_speech', 0, 1, 'active', '2026-10-04T00:00:00.000Z')`, fixture.systemAccount)
	seed(`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, status, created_at)
		VALUES ('key_volcengine_speech', ?, 'rs_volcengine_speech', 'volcengine语音Key', ?, 'active', '2026-10-04T00:00:00.000Z')`,
		fixture.systemAccount, gatewayruntimecache.HashSecret(keySecret))
	return keySecret
}

// TestChainMediaVolcengineSpeechFullChain 覆盖 M6 链路 6（契约 §9.2）：出站
// POST /api/v3/tts（固定语音服务域——测试 seam env 指回 mock，未设置时恒
// openspeech.bytedance.com）+ `Authorization: Bearer;<token>` 分号鉴权特例 +
// req_params 嵌套报文（input→text、voice→speaker、response_format→format、
// user.uid=speech_appid、reqid uuid、operation=query、model 不透传）；响应
// code 3000 + data base64 解码后以二进制 mp3 透传（frame sync magic bytes）；
// usage 按请求字符自算（V3 响应无字符回报，§2.8）。
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

	// 出站断言：POST /api/v3/tts + Bearer; 分号鉴权特例 + req_params 嵌套报文。
	requests := mock.Requests()
	if len(requests) != 1 || requests[0].Method != http.MethodPost || requests[0].Path != "/api/v3/tts" {
		t.Fatalf("volcengine speech 出站请求形态错误: %+v", requests)
	}
	if requests[0].AuthHeader != "Bearer;volc-tts-token-a" {
		t.Fatalf("volcengine speech Authorization = %q, want Bearer;volc-tts-token-a（分号特例）", requests[0].AuthHeader)
	}
	body := requests[0].Body
	for _, want := range []string{
		`"user":{"uid":"app-voice-123"}`,
		`"text":"你好火山"`,
		`"speaker":"BV700_streaming"`,
		`"format":"mp3"`,
		`"speed_ratio":1.5`,
		`"operation":"query"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("出站报文缺少 %s: %s", want, body)
		}
	}
	if strings.Contains(body, `"model"`) {
		t.Fatalf("V3 请求面无 model 字段，统一面 model 占位不得透传: %s", body)
	}
	// reqid 网关生成（uuid v4 形状）。
	var decoded struct {
		Reqid string `json:"reqid"`
	}
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("decode outbound body: %v", err)
	}
	if len(decoded.Reqid) != 36 || decoded.Reqid[14] != '4' {
		t.Fatalf("reqid = %q, want uuid v4 shape（网关生成）", decoded.Reqid)
	}

	// usage：V3 响应无字符回报 → 网关按请求 input 自算（4 runes）；目录无
	// 字符价 → 成本不虚计。
	waitGlmSpoolRecord(t, spoolDir, func(text string) bool {
		return strings.Contains(text, `"ttsInputChars":4`) && strings.Contains(text, `"endpoint":"POST /v1/audio/speech"`)
	}, "volcengine tts 请求字符自算终态记录")
}

// TestChainMediaVolcengineSpeechMissingCredentials 钉住能力语义（契约 §9.2）：
// volcengine 账户勾选 audio_speech 但缺语音双值凭据 → 显式失败（账户级能力
// 缺失，不静默回退、不打上游），不是客户端 400（沿 videoCreateRequest 能力
// 缺失先例）。
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
	if response.StatusCode < 500 {
		t.Fatalf("缺凭据 speech status=%d, want 5xx（账户级能力缺失，非客户端 400）body=%s", response.StatusCode, payload)
	}
	if len(mock.Requests()) != 0 {
		t.Fatalf("缺凭据不得打上游: %+v", mock.Requests())
	}
}
