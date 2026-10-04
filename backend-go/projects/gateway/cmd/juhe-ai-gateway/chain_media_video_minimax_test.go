package main

// M3 minimax（Hailuo 视频 + t2a_v2 TTS）chain 级集成测试（契约 §8；媒体设计
// §4.2/§7/§8）：进程内 fixture（newChainFixture 先例）+ minimax 分组/账户 +
// mockupstream media_minimax_* 场景。覆盖四条链路：
//  1. 创建（video_generation 报文改写 + Bearer 认证 + /v1 openai 归一 URL +
//     params 回显）→ 轮询 queued → queued → completed（file_download_url 冻结
//     进 artifact）→ content 经绝对 URL 无凭据直连下载 mp4 + 终态 usage
//     spool（usage_missing 口径，不虚计）；
//  2. status=Fail + base_resp → failed 终态（code=1004 摘要 + cost 0）；
//  3. DELETE 取消（minimax 无上游取消 API → 不发上游请求，本地收敛
//     cancelled）；
//  4. TTS 全链（/v1/audio/speech → POST /v1/t2a_v2 改写 + Bearer；响应
//     data.audio hex 解码后透传二进制 mp3 + content-type audio/mpeg；usage
//     按 extra_info.usage_characters 回报计量）+ speed 超区间本地 400。
//
// 上游拓扑：账户 base_url 指向包一层的手工记录服务器（断言 Bearer 认证头
// 与出站路径），内层为 mockupstream 引擎；completed 的产物 url 由引擎按自身
// URL 渲染，网关直连该 URL（不经过记录层）。
import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	platformmock "github.com/huanminabc/juhe-ai/backend-go-platform/mockupstream"
)

// minimaxRecordedRequest 是记录层捕获的一条上游请求（方法/路径/认证面）。
type minimaxRecordedRequest struct {
	Method        string
	Path          string
	Authorization string
}

// minimaxUpstreamRecorder 是账户 base_url 指向的记录层：逐请求记录认证头并
// 转发给 mockupstream 引擎（场景头由客户端显式携带或引擎默认）。
type minimaxUpstreamRecorder struct {
	mu       sync.Mutex
	requests []minimaxRecordedRequest
}

func (r *minimaxUpstreamRecorder) record(method, path, authorization string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, minimaxRecordedRequest{Method: method, Path: path, Authorization: authorization})
}

func (r *minimaxUpstreamRecorder) snapshot() []minimaxRecordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]minimaxRecordedRequest(nil), r.requests...)
}

// newMinimaxRecordedUpstream 组装「记录层 + mockupstream 引擎」双层上游，返回
// 记录层、引擎与记录层地址（种账户 base_url 用）。
func newMinimaxRecordedUpstream(t *testing.T) (*minimaxUpstreamRecorder, *platformmock.Server, string) {
	t.Helper()
	mock := platformmock.New()
	recorder := &minimaxUpstreamRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadGateway)
			return
		}
		_ = r.Body.Close()
		recorder.record(r.Method, r.URL.Path, r.Header.Get("Authorization"))
		forwarded := httptest.NewRequest(r.Method, mock.URL+r.URL.RequestURI(), strings.NewReader(string(body)))
		forwarded.Header = r.Header.Clone()
		mock.Config.Handler.ServeHTTP(w, forwarded)
	}))
	t.Cleanup(func() {
		server.Close()
		mock.Close()
	})
	return recorder, mock, server.URL
}

// seedMediaMinimaxAccount 种 minimax 分组 + Hailuo/speech 测试账户 + 独立路由
// 策略与 API Key（不复用 fixture 的 openai 组，避免跨 provider 候选串扰），
// 返回网关 API Key 明文。凭据带 base_url 与 supported_endpoint_modes
//（openai 族词表：video_* 四值 + audio_speech，opt-in 语义；minimax 档案
// Capabilities 只声明媒体，无 chat 模式）。模型约束种 MiniMax-Hailuo-2.3 与
// speech-02-turbo。
func seedMediaMinimaxAccount(t *testing.T, fixture *chainFixture, id, baseURL, apiKey string) string {
	t.Helper()
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(query, args...); err != nil {
			t.Fatalf("seed minimax media row: %v: %v", query, err)
		}
	}
	keySecret := "sk-chain-minimax-media"
	credentials := map[string]any{
		"api_key":  apiKey,
		"base_url": baseURL,
		"supported_endpoint_modes": []string{
			"audio_speech", "video_create", "video_get", "video_content", "video_cancel",
		},
	}
	seed(`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type)
		VALUES ('group_minimax_media', ?, 'minimax', 1, 'personal')`, fixture.systemAccount)
	seed(`INSERT INTO accounts (
			id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
			name, type, status, schedulable, concurrency_limit, priority, credentials_encrypted, deleted_at
		) VALUES (?, ?, 'minimax', 'profile_minimax_openai_v1', 'openai', 'v1', ?, 'api_key', 'active', 1, 0, 0, ?, NULL)`,
		id, fixture.systemAccount, id, mustEncryptCredentials(t, credentials))
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
		VALUES ('group_minimax_media', ?, ?, 1, '2026-10-04T00:00:00.000Z')`, fixture.systemAccount, id)
	seed(`INSERT INTO account_supported_models (account_id, provider_code, model, created_at)
		VALUES (?, 'minimax', 'MiniMax-Hailuo-2.3', '2026-10-04T00:00:00.000Z')`, id)
	seed(`INSERT INTO account_supported_models (account_id, provider_code, model, created_at)
		VALUES (?, 'minimax', 'speech-02-turbo', '2026-10-04T00:00:00.000Z')`, id)
	seed(`INSERT INTO route_strategies (id, system_account_id, name, mode, config_json, status)
		VALUES ('rs_minimax_media', ?, 'minimax媒体', 'normal', NULL, 'active')`, fixture.systemAccount)
	seed(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at)
		VALUES ('rsg_minimax_media', 'rs_minimax_media', ?, 'group_minimax_media', 0, 1, 'active', '2026-10-04T00:00:00.000Z')`, fixture.systemAccount)
	seed(`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, status, created_at)
		VALUES ('key_minimax_media', ?, 'rs_minimax_media', 'minimax媒体Key', ?, 'active', '2026-10-04T00:00:00.000Z')`,
		fixture.systemAccount, gatewayruntimecache.HashSecret(keySecret))
	return keySecret
}

// TestChainMediaMinimaxVideoFullFlowLifecycle 覆盖链路 1：创建
//（video_generation 报文改写 + Bearer 认证头 + /v1 openai 归一 URL + ignored
// 回显）→ 轮询 queued → queued → completed（file_download_url 进 artifact）
// → content 绝对 URL 无凭据直连 mp4 → 终态 usage（usage_missing，契约 §2.8
// 不虚计）。
func TestChainMediaMinimaxVideoFullFlowLifecycle(t *testing.T) {
	fixture := newChainFixture(t)
	recorder, mock, upstreamURL := newMinimaxRecordedUpstream(t)
	apiKey := seedMediaMinimaxAccount(t, fixture, "acc_minimax_media", upstreamURL, "minimax-key-a")

	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain := composeMediaVideoChain(t, fixture, spoolDir)
	server := httptest.NewServer(chain)
	defer server.Close()

	job := mediaVideoCreateJob(t, server.URL, apiKey,
		`{"model":"MiniMax-Hailuo-2.3","prompt":"一只猫在弹钢琴","seconds":"6","input_reference":"data:image/png;base64,aGVsbG8=","seed":42,"provider_options":{"minimax":{"prompt_optimizer":true}}}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaMinimaxCreateOK)})
	jobID, _ := job["id"].(string)
	if jobID == "" || !strings.HasPrefix(jobID, "video_") {
		t.Fatalf("对外 job id 缺失或非 video_ 前缀: %#v", job)
	}
	if job["status"] != "queued" {
		t.Fatalf("创建响应 status = %v, want queued（创建响应无 status 字段，已受理未开始）", job["status"])
	}
	if job["provider"] != "minimax" {
		t.Fatalf("provider = %v, want minimax", job["provider"])
	}
	providerJobID, _ := job["provider_job_id"].(string)
	if len(providerJobID) != 16 || strings.ContainsAny(providerJobID, "/-") {
		t.Fatalf("provider_job_id = %q, want 16 hex 任务 id 形态", providerJobID)
	}
	applied := mediaVideoJobKeys(t, job, "params_applied")
	for _, want := range []string{"model", "prompt", "seconds", "input_reference"} {
		if !applied[want] {
			t.Fatalf("params_applied 缺少 %s: %v", want, job["params_applied"])
		}
	}
	if ignored := mediaVideoJobKeys(t, job, "params_ignored"); !ignored["seed"] {
		t.Fatalf("params_ignored 缺少 seed（minimax 请求面无 seed）: %v", job["params_ignored"])
	}

	// 创建出站断言：POST /v1/video_generation + Bearer 认证头 + 转换报文
	//（duration 数值、first_frame_image 裸 base64、provider_options 合并），
	// ignored 键不进报文。
	records := recorder.snapshot()
	if len(records) != 1 || records[0].Method != http.MethodPost || records[0].Path != "/v1/video_generation" {
		t.Fatalf("创建出站请求形态错误: %+v", records)
	}
	if records[0].Authorization != "Bearer minimax-key-a" {
		t.Fatalf("创建请求 Authorization = %q, want Bearer minimax-key-a", records[0].Authorization)
	}
	createBody := ""
	for _, request := range mock.Requests() {
		if request.Method == http.MethodPost && request.Path == "/v1/video_generation" {
			createBody = request.Body
		}
	}
	if createBody == "" {
		t.Fatal("引擎侧未录制创建请求")
	}
	for _, want := range []string{
		`"model":"MiniMax-Hailuo-2.3"`, `"prompt":"一只猫在弹钢琴"`,
		`"duration":6`, `"first_frame_image":"aGVsbG8="`, `"prompt_optimizer":true`,
	} {
		if !strings.Contains(createBody, want) {
			t.Fatalf("创建报文缺少 %s: %s", want, createBody)
		}
	}
	if strings.Contains(createBody, `"seed"`) {
		t.Fatalf("创建报文不得含 ignored 键 seed: %s", createBody)
	}

	// 轮询三次：queued（Preparing）→ queued（Queueing）→ completed（Success +
	// file_download_url 冻结）。
	if first := mediaVideoPollOnce(t, server.URL, apiKey, jobID); first["status"] != "queued" {
		t.Fatalf("第一轮轮询 status = %v, want queued（Preparing 归一）", first["status"])
	}
	if second := mediaVideoPollOnce(t, server.URL, apiKey, jobID); second["status"] != "queued" {
		t.Fatalf("第二轮轮询 status = %v, want queued（Queueing 归一）", second["status"])
	}
	if third := mediaVideoPollOnce(t, server.URL, apiKey, jobID); third["status"] != "completed" {
		t.Fatalf("第三轮轮询 status = %v, want completed: %v", third["status"], third)
	}
	// 任务面轮询经记录层：GET /v1/query/video_generation（task_id 在 query
	// 串，记录层只记 path；query 形态由引擎侧 RawQuery 佐证）+ Bearer。
	records = recorder.snapshot()
	if len(records) != 4 {
		t.Fatalf("轮询出站请求计数错误: %+v", records)
	}
	for _, record := range records[1:] {
		if record.Method != http.MethodGet || record.Path != "/v1/query/video_generation" {
			t.Fatalf("轮询出站请求形态错误: %+v", record)
		}
		if record.Authorization != "Bearer minimax-key-a" {
			t.Fatalf("轮询请求 Authorization = %q, want Bearer minimax-key-a", record.Authorization)
		}
	}
	queryHits := 0
	for _, request := range mock.Requests() {
		if request.Method == http.MethodGet && request.Path == "/v1/query/video_generation" &&
			strings.Contains(request.RawQuery, "task_id="+providerJobID) {
			queryHits++
		}
	}
	if queryHits != 3 {
		t.Fatalf("引擎侧 query 形态命中 %d 次, want 3（task_id=%s）: %+v", queryHits, providerJobID, mock.Requests())
	}

	// media_jobs 行终态 completed + 任务 id 回填 + 无秒计量不虚计（0）。
	var status, upstreamJobID string
	var costUsd float64
	if err := fixture.db.QueryRow(`SELECT status, upstream_job_id, cost_usd FROM media_jobs WHERE id = ?`, jobID).Scan(&status, &upstreamJobID, &costUsd); err != nil {
		t.Fatalf("查询 media_jobs 行: %v", err)
	}
	if status != "completed" || upstreamJobID != providerJobID {
		t.Fatalf("media_jobs = %s/%s, want completed/%s", status, upstreamJobID, providerJobID)
	}
	if costUsd != 0 {
		t.Fatalf("minimax 无输出秒回报且目录未落秒价，cost_usd = %v, want 0（usage_missing 不虚计）", costUsd)
	}

	// content 下载：completed 的 file_download_url 是引擎渲染的绝对 URL
	//（不经记录层），网关无凭据直连——引擎侧 Authorization 记录为空可证。
	// 响应为 mp4 流。
	response, payload := mediaVideoClientDo(t, server.URL, http.MethodGet, "/v1/videos/"+jobID+"/content", apiKey, "", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("content status=%d body=%s", response.StatusCode, payload)
	}
	if got := response.Header.Get("Content-Type"); !strings.Contains(got, "video/mp4") {
		t.Fatalf("content-type = %q, want video/mp4", got)
	}
	if len(payload) < 12 || string(payload[4:8]) != "ftyp" {
		t.Fatalf("content 不是 mp4 载荷（ftyp magic bytes 缺失）: % x", payload[:min(12, len(payload))])
	}
	if len(recorder.snapshot()) != 4 {
		t.Fatalf("产物下载不得经账户 base_url（ContentFromArtifact 直连 url）: %+v", recorder.snapshot())
	}
	for _, request := range mock.Requests() {
		if strings.HasPrefix(request.Path, "/v1/files/retrieve") && request.AuthHeader != "" {
			t.Fatalf("产物直连不得携带 Authorization: %q", request.AuthHeader)
		}
	}

	// 终态 usage：spool 链异步落盘，completed 无秒计量 → usageMissing 标记
	//（契约 §2.8：上游不回报且无可查证秒价 → 0 计费 + usage_missing，不猜测）。
	waitGlmSpoolRecord(t, spoolDir, func(text string) bool {
		return strings.Contains(text, `"usageMissing":true`) && strings.Contains(text, `"endpoint":"/v1/videos"`)
	}, "minimax completed usage_missing 终态记录")
}

// TestChainMediaMinimaxVideoFailedTerminal 覆盖链路 2：status=Fail +
// base_resp → failed 终态（错误摘要 code=base_resp.status_code 数字串透传、
// cost 0、终态 usage 行 success=false）。
func TestChainMediaMinimaxVideoFailedTerminal(t *testing.T) {
	fixture := newChainFixture(t)
	_, _, upstreamURL := newMinimaxRecordedUpstream(t)
	apiKey := seedMediaMinimaxAccount(t, fixture, "acc_minimax_media_fail", upstreamURL, "minimax-key-fail")

	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain := composeMediaVideoChain(t, fixture, spoolDir)
	server := httptest.NewServer(chain)
	defer server.Close()

	job := mediaVideoCreateJob(t, server.URL, apiKey,
		`{"model":"MiniMax-Hailuo-2.3","prompt":"失败终态"}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaMinimaxPollFail)})
	jobID, _ := job["id"].(string)

	terminal := mediaVideoPollOnce(t, server.URL, apiKey, jobID)
	if terminal["status"] != "failed" {
		t.Fatalf("轮询 status = %v, want failed: %v", terminal["status"], terminal)
	}
	jobError, _ := terminal["error"].(map[string]any)
	if jobError == nil || jobError["code"] != "1004" {
		t.Fatalf("failed error 摘要错误（code 应取 base_resp.status_code）: %v", terminal["error"])
	}
	var status string
	var costUsd float64
	if err := fixture.db.QueryRow(`SELECT status, cost_usd FROM media_jobs WHERE id = ?`, jobID).Scan(&status, &costUsd); err != nil {
		t.Fatalf("查询 media_jobs 行: %v", err)
	}
	if status != "failed" || costUsd != 0 {
		t.Fatalf("media_jobs = %s/%v, want failed/0（失败不虚计）", status, costUsd)
	}
	waitGlmSpoolRecord(t, spoolDir, func(text string) bool {
		return strings.Contains(text, `"success":false`) && strings.Contains(text, `"endpoint":"/v1/videos"`)
	}, "minimax failed 终态记录")
}

// TestChainMediaMinimaxVideoCancel 覆盖链路 3：DELETE → minimax 无上游取消
// API（契约 §8.1）→ 不发上游取消请求、直接本地收敛 cancelled + 204；终态行
// 轮询回本地对象不再打上游。
func TestChainMediaMinimaxVideoCancel(t *testing.T) {
	fixture := newChainFixture(t)
	recorder, _, upstreamURL := newMinimaxRecordedUpstream(t)
	apiKey := seedMediaMinimaxAccount(t, fixture, "acc_minimax_media_cancel", upstreamURL, "minimax-key-cancel")

	chain := composeMediaVideoChain(t, fixture, filepath.Join(t.TempDir(), "spool"))
	server := httptest.NewServer(chain)
	defer server.Close()

	job := mediaVideoCreateJob(t, server.URL, apiKey,
		`{"model":"MiniMax-Hailuo-2.3","prompt":"取消语义"}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaMinimaxPollRunning)})
	jobID, _ := job["id"].(string)

	response, payload := mediaVideoClientDo(t, server.URL, http.MethodDelete, "/v1/videos/"+jobID, apiKey, "", nil)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status=%d body=%s, want 204", response.StatusCode, payload)
	}
	var status string
	if err := fixture.db.QueryRow(`SELECT status FROM media_jobs WHERE id = ?`, jobID).Scan(&status); err != nil {
		t.Fatalf("查询 media_jobs: %v", err)
	}
	if status != "cancelled" {
		t.Fatalf("DELETE 后本地 status = %s, want cancelled", status)
	}
	// 账户亲和面：取消未发任何上游请求（minimax 无取消端点，本地收敛），
	// 记录层仅有创建一条。
	records := recorder.snapshot()
	if len(records) != 1 {
		t.Fatalf("minimax 取消不得打上游（SupportsCancel=false 本地收敛）: %+v", records)
	}

	// 终态行轮询回本地对象，不再打上游（记录数不变）。
	pollResponse, pollPayload := mediaVideoClientDo(t, server.URL, http.MethodGet, "/v1/videos/"+jobID, apiKey, "", nil)
	if pollResponse.StatusCode != http.StatusOK || mediaVideoDecodeJob(t, pollPayload)["status"] != "cancelled" {
		t.Fatalf("终态轮询应回本地 cancelled: status=%d body=%s", pollResponse.StatusCode, pollPayload)
	}
	if len(recorder.snapshot()) != 1 {
		t.Fatalf("终态轮询不得再打上游: %+v", recorder.snapshot())
	}
}

// TestChainMediaMinimaxSpeechFullChain 覆盖链路 4：/v1/audio/speech →
// POST /v1/t2a_v2 报文改写（input→text、voice→voice_setting.voice_id、
// response_format→audio_setting.format）+ Bearer 认证；响应 data.audio hex
// 解码后以二进制 mp3 透传（content-type audio/mpeg，magic bytes 断言——
// 未解码的 hex 字符串不满足 frame sync）；usage 按 extra_info.usage_characters
// 回报计量（契约 §8.2/§2.8，ttsInputChars 落 spool）。
func TestChainMediaMinimaxSpeechFullChain(t *testing.T) {
	fixture := newChainFixture(t)
	recorder, _, upstreamURL := newMinimaxRecordedUpstream(t)
	apiKey := seedMediaMinimaxAccount(t, fixture, "acc_minimax_tts", upstreamURL, "minimax-key-tts")

	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain := composeMediaVideoChain(t, fixture, spoolDir)
	server := httptest.NewServer(chain)
	defer server.Close()

	response, payload := mediaVideoClientDo(t, server.URL, http.MethodPost, "/v1/audio/speech", apiKey,
		`{"model":"speech-02-turbo","input":"你好 minimax","voice":"male-qn-qingse","response_format":"mp3"}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaMinimaxTTSOK)})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("speech status=%d body=%s", response.StatusCode, payload)
	}
	if got := response.Header.Get("Content-Type"); !strings.Contains(got, "audio/mpeg") {
		t.Fatalf("content-type = %q, want audio/mpeg（data.audio hex 解码后的二进制通道）", got)
	}
	// hex 解码断言：mp3 frame sync（0xFF 0xFB）。上游 data.audio 是 hex
	// 字符串（'f''f'…），未解码的透传不满足二进制 magic bytes。
	if len(payload) < 4 || payload[0] != 0xFF || payload[1] != 0xFB {
		t.Fatalf("响应不是解码后的 mp3 载荷（frame sync 缺失）: % x", payload[:min(4, len(payload))])
	}

	// 出站断言：POST /v1/t2a_v2 + Bearer + 转换报文。
	records := recorder.snapshot()
	if len(records) != 1 || records[0].Method != http.MethodPost || records[0].Path != "/v1/t2a_v2" {
		t.Fatalf("speech 出站请求形态错误: %+v", records)
	}
	if records[0].Authorization != "Bearer minimax-key-tts" {
		t.Fatalf("speech 请求 Authorization = %q, want Bearer minimax-key-tts", records[0].Authorization)
	}

	// usage：extra_info.usage_characters 回报优先（mock 刻意 +1 偏离请求
	// 字符数 10 → 回报 11，见 serveMinimaxTTS；若网关按请求自算会记 10）；
	// 目录未落字符价 → 成本不虚计。
	waitGlmSpoolRecord(t, spoolDir, func(text string) bool {
		return strings.Contains(text, `"ttsInputChars":11`) && strings.Contains(text, `"endpoint":"POST /v1/audio/speech"`)
	}, "minimax tts extra_info 字符计量终态记录")
}

// TestChainMediaMinimaxSpeechSpeedOutOfRangeLocal400 验证契约 §2.4 规则 2：
// speed 超出 minimax 厂商区间 [0.5, 2.0] → 网关本地 400（invalid_request_
// error），不打上游。
func TestChainMediaMinimaxSpeechSpeedOutOfRangeLocal400(t *testing.T) {
	fixture := newChainFixture(t)
	recorder, _, upstreamURL := newMinimaxRecordedUpstream(t)
	apiKey := seedMediaMinimaxAccount(t, fixture, "acc_minimax_tts_400", upstreamURL, "minimax-key-400")

	chain := composeMediaVideoChain(t, fixture, filepath.Join(t.TempDir(), "spool"))
	server := httptest.NewServer(chain)
	defer server.Close()

	response, payload := mediaVideoClientDo(t, server.URL, http.MethodPost, "/v1/audio/speech", apiKey,
		`{"model":"speech-02-turbo","input":"hi","voice":"male-qn-qingse","speed":3.0}`,
		nil)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("speech speed=3.0 status=%d, want 400（本地能力边界裁决）body=%s", response.StatusCode, payload)
	}
	if !strings.Contains(string(payload), "invalid_request_error") {
		t.Fatalf("400 响应缺少 invalid_request_error: %s", payload)
	}
	if len(recorder.snapshot()) != 0 {
		t.Fatalf("本地 400 不得打上游: %+v", recorder.snapshot())
	}
}
