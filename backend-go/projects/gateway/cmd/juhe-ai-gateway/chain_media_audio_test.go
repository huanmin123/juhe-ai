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
	//（usage JSON 字符串 duration=62.5）但目录无 USD 秒价 → cost 0（不虚计）。
	var kind, status, upstreamJobID string
	var costUsd float64
	if err := fixture.db.QueryRow(`SELECT kind, status, upstream_job_id, cost_usd FROM media_jobs WHERE id = ?`, jobID).Scan(&kind, &status, &upstreamJobID, &costUsd); err != nil {
		t.Fatalf("查询 media_jobs 行: %v", err)
	}
	if kind != "audio_transcription" || status != "completed" || upstreamJobID != providerJobID {
		t.Fatalf("media_jobs = %s/%s/%s, want audio_transcription/completed/%s", kind, status, upstreamJobID, providerJobID)
	}
	if costUsd != 0 {
		t.Fatalf("paraformer 目录未落 USD 秒价，cost_usd = %v, want 0（计量照落成本不虚计）", costUsd)
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
