package main

// M3 第五批 qwen（通义百炼万相视频）chain 级集成测试（契约 §10.1；媒体设计
// §4.2/§7/§8）：进程内 fixture（newChainFixture 先例）+ qwen 分组/账户 +
// mockupstream media_qwen_* 场景。覆盖四条链路：
//  1. 创建（video-synthesis 报文改写：prompt→input.prompt、input_reference
//     双形态直传 input.img_url、size WxH→W*H、seconds→parameters.duration、
//     seed 直传 + Bearer 认证 + **X-DashScope-Async: enable 异步头** +
//     /api/v1 服务根 URL 归一 + params 回显）→ 轮询 PENDING→RUNNING→
//     SUCCEEDED（output.video_url 冻结进 artifact + usage JSON 字符串解析出
//     video_duration=5）→ content 经绝对 URL 无凭据直连下载 mp4 + 终态 usage
//     spool（秒计量照落、目录无 USD 秒价成本 0，不虚计）；
//  2. task_status=FAILED + 顶层 code/message → failed 终态（code 透传 +
//     cost 0）；
//  3. DELETE 取消（qwen §10.1 面无取消 API → 不发上游请求，本地收敛
//     cancelled）；
//  4. size 非 WxH 形态（16:9）→ 本地 400 参数边界（契约 §2.4 规则 2），
//     不打上游。
//
// 上游拓扑：账户 base_url 指向包一层的手工记录服务器（断言 Bearer 认证头、
// X-DashScope-Async 异步头与出站路径），内层为 mockupstream 引擎；completed
// 的产物 url 由引擎按自身 URL 渲染，网关直连该 URL（不经过记录层）。
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

// qwenRecordedRequest 是记录层捕获的一条上游请求（方法/路径/认证面/异步头）。
type qwenRecordedRequest struct {
	Method         string
	Path           string
	Authorization  string
	DashScopeAsync string
}

// qwenUpstreamRecorder 是账户 base_url 指向的记录层：逐请求记录认证头与
// X-DashScope-Async 异步头并转发给 mockupstream 引擎（场景头由客户端显式
// 携带或引擎默认）。
type qwenUpstreamRecorder struct {
	mu       sync.Mutex
	requests []qwenRecordedRequest
}

func (r *qwenUpstreamRecorder) record(method, path, authorization, dashScopeAsync string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, qwenRecordedRequest{
		Method: method, Path: path, Authorization: authorization, DashScopeAsync: dashScopeAsync,
	})
}

func (r *qwenUpstreamRecorder) snapshot() []qwenRecordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]qwenRecordedRequest(nil), r.requests...)
}

// newQwenRecordedUpstream 组装「记录层 + mockupstream 引擎」双层上游，返回
// 记录层、引擎与记录层地址（种账户 base_url 用）。
func newQwenRecordedUpstream(t *testing.T) (*qwenUpstreamRecorder, *platformmock.Server, string) {
	t.Helper()
	mock := platformmock.New()
	recorder := &qwenUpstreamRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadGateway)
			return
		}
		_ = r.Body.Close()
		recorder.record(r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("X-DashScope-Async"))
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

// seedMediaQwenAccount 种 qwen 分组 + 万相测试账户 + 独立路由策略与 API Key
// （不复用 fixture 的 openai 组，避免跨 provider 候选串扰），返回网关 API Key
// 明文。凭据带 base_url 与 supported_endpoint_modes（openai 族词表：video_*
// 四值，opt-in 语义；qwen 档案 Capabilities 只声明视频，无 chat/audio 模式）。
// 模型约束种 wan2.2-t2v-plus。
func seedMediaQwenAccount(t *testing.T, fixture *chainFixture, id, baseURL, apiKey string) string {
	t.Helper()
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(query, args...); err != nil {
			t.Fatalf("seed qwen media row: %v: %v", query, err)
		}
	}
	keySecret := "sk-chain-qwen-media"
	credentials := map[string]any{
		"api_key":  apiKey,
		"base_url": baseURL,
		"supported_endpoint_modes": []string{
			"video_create", "video_get", "video_content", "video_cancel",
		},
	}
	seed(`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type)
		VALUES ('group_qwen_media', ?, 'qwen', 1, 'personal')`, fixture.systemAccount)
	seed(`INSERT INTO accounts (
			id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
			name, type, status, schedulable, concurrency_limit, priority, credentials_encrypted, deleted_at
		) VALUES (?, ?, 'qwen', 'profile_qwen_openai_v1', 'openai', 'v1', ?, 'api_key', 'active', 1, 0, 0, ?, NULL)`,
		id, fixture.systemAccount, id, mustEncryptCredentials(t, credentials))
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
		VALUES ('group_qwen_media', ?, ?, 1, '2026-10-04T00:00:00.000Z')`, fixture.systemAccount, id)
	seed(`INSERT INTO account_supported_models (account_id, provider_code, model, created_at)
		VALUES (?, 'qwen', 'wan2.2-t2v-plus', '2026-10-04T00:00:00.000Z')`, id)
	seed(`INSERT INTO route_strategies (id, system_account_id, name, mode, config_json, status)
		VALUES ('rs_qwen_media', ?, 'qwen媒体', 'normal', NULL, 'active')`, fixture.systemAccount)
	seed(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at)
		VALUES ('rsg_qwen_media', 'rs_qwen_media', ?, 'group_qwen_media', 0, 1, 'active', '2026-10-04T00:00:00.000Z')`, fixture.systemAccount)
	seed(`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, status, created_at)
		VALUES ('key_qwen_media', ?, 'rs_qwen_media', 'qwen媒体Key', ?, 'active', '2026-10-04T00:00:00.000Z')`,
		fixture.systemAccount, gatewayruntimecache.HashSecret(keySecret))
	return keySecret
}

// TestChainMediaQwenVideoFullFlowLifecycle 覆盖链路 1：创建（video-synthesis
// 报文改写 + Bearer 认证头 + X-DashScope-Async 异步头 + /api/v1 服务根 URL +
// applied/ignored 回显）→ 轮询 PENDING→queued、RUNNING→in_progress、
// SUCCEEDED→completed（output.video_url 进 artifact）→ content 绝对 URL 无凭据
// 直连 mp4 → 终态 usage（usage JSON 字符串解析出 5 秒计量照落；目录无 USD
// 秒价成本 0，契约 §2.8 不虚计）。
func TestChainMediaQwenVideoFullFlowLifecycle(t *testing.T) {
	fixture := newChainFixture(t)
	recorder, mock, upstreamURL := newQwenRecordedUpstream(t)
	apiKey := seedMediaQwenAccount(t, fixture, "acc_qwen_media", upstreamURL, "sk-dashscope-key-a")

	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain := composeMediaVideoChain(t, fixture, spoolDir)
	server := httptest.NewServer(chain)
	defer server.Close()

	job := mediaVideoCreateJob(t, server.URL, apiKey,
		`{"model":"wan2.2-t2v-plus","prompt":"一只猫在弹钢琴","seconds":5,"size":"1920x1080","seed":42,"input_reference":"data:image/png;base64,aGVsbG8=","negative_prompt":"模糊","audio":false,"provider_options":{"qwen":{"parameters":{"prompt_extend":true}}}}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaQwenCreatePending)})
	jobID, _ := job["id"].(string)
	if jobID == "" || !strings.HasPrefix(jobID, "video_") {
		t.Fatalf("对外 job id 缺失或非 video_ 前缀: %#v", job)
	}
	if job["status"] != "queued" {
		t.Fatalf("创建响应 status = %v, want queued（创建响应 task_status=PENDING 归一）", job["status"])
	}
	if job["provider"] != "qwen" {
		t.Fatalf("provider = %v, want qwen", job["provider"])
	}
	providerJobID, _ := job["provider_job_id"].(string)
	if !strings.Contains(providerJobID, "-") || len(providerJobID) != 36 {
		t.Fatalf("provider_job_id = %q, want uuid 任务 id 形态", providerJobID)
	}
	applied := mediaVideoJobKeys(t, job, "params_applied")
	for _, want := range []string{"model", "prompt", "seconds", "size", "seed", "input_reference", "negative_prompt"} {
		if !applied[want] {
			t.Fatalf("params_applied 缺少 %s: %v", want, job["params_applied"])
		}
	}
	ignored := mediaVideoJobKeys(t, job, "params_ignored")
	if !ignored["audio"] {
		t.Fatalf("params_ignored 缺少 audio（qwen 请求面无生成音频开关）: %v", job["params_ignored"])
	}

	// 创建出站断言：POST /api/v1/services/aigc/video-generation/video-synthesis
	// + Bearer 认证头 + X-DashScope-Async: enable 异步头 + 转换报文
	//（input.prompt/input.img_url/input.negative_prompt、parameters.size 星号
	// 形态/duration/seed/prompt_extend 嵌套合并），ignored 键不进报文。
	records := recorder.snapshot()
	if len(records) != 1 || records[0].Method != http.MethodPost || records[0].Path != "/api/v1/services/aigc/video-generation/video-synthesis" {
		t.Fatalf("创建出站请求形态错误: %+v", records)
	}
	if records[0].Authorization != "Bearer sk-dashscope-key-a" {
		t.Fatalf("创建请求 Authorization = %q, want Bearer sk-dashscope-key-a", records[0].Authorization)
	}
	if records[0].DashScopeAsync != "enable" {
		t.Fatalf("创建请求 X-DashScope-Async = %q, want enable（DashScope 异步任务约定，契约 §10.1）", records[0].DashScopeAsync)
	}
	createBody := ""
	for _, request := range mock.Requests() {
		if request.Method == http.MethodPost && request.Path == "/api/v1/services/aigc/video-generation/video-synthesis" {
			createBody = request.Body
		}
	}
	if createBody == "" {
		t.Fatal("引擎侧未录制创建请求")
	}
	for _, want := range []string{
		`"model":"wan2.2-t2v-plus"`,
		`"input":{"img_url":"data:image/png;base64,aGVsbG8=","negative_prompt":"模糊","prompt":"一只猫在弹钢琴"}`,
		`"parameters":{"duration":5,"prompt_extend":true,"seed":42,"size":"1920*1080"}`,
	} {
		if !strings.Contains(createBody, want) {
			t.Fatalf("创建报文缺少 %s: %s", want, createBody)
		}
	}
	for _, banned := range []string{`"audio"`, `"n":`} {
		if strings.Contains(createBody, banned) {
			t.Fatalf("创建报文不得含 %s: %s", banned, createBody)
		}
	}

	// 轮询三次：queued（#1 PENDING 归一）→ in_progress（#2 RUNNING 归一）→
	// completed（#3 SUCCEEDED + output.video_url 冻结）。
	if first := mediaVideoPollOnce(t, server.URL, apiKey, jobID); first["status"] != "queued" {
		t.Fatalf("第一轮轮询 status = %v, want queued", first["status"])
	}
	if second := mediaVideoPollOnce(t, server.URL, apiKey, jobID); second["status"] != "in_progress" {
		t.Fatalf("第二轮轮询 status = %v, want in_progress（RUNNING 归一）", second["status"])
	}
	if third := mediaVideoPollOnce(t, server.URL, apiKey, jobID); third["status"] != "completed" {
		t.Fatalf("第三轮轮询 status = %v, want completed: %v", third["status"], third)
	}
	// 任务面轮询经记录层：GET /api/v1/tasks/{task_id} + Bearer；异步头只在
	// 创建面携带（DashScope 约定针对创建请求，任务面查询无需）。
	records = recorder.snapshot()
	if len(records) != 4 {
		t.Fatalf("轮询出站请求计数错误: %+v", records)
	}
	for _, record := range records[1:] {
		if record.Method != http.MethodGet || record.Path != "/api/v1/tasks/"+providerJobID {
			t.Fatalf("轮询出站请求形态错误: %+v", record)
		}
		if record.Authorization != "Bearer sk-dashscope-key-a" {
			t.Fatalf("轮询请求 Authorization = %q, want Bearer sk-dashscope-key-a", record.Authorization)
		}
	}

	// media_jobs 行终态 completed + 任务 id 回填 + 秒计量照抽（usage JSON
	// 字符串 video_duration=5）按目录 USD 秒价计费（2026-10-05 用户裁决补价：
	// wan2.2-t2v-plus 1080P 档 0.70 元/秒 ÷ 7.0 = $0.1/秒；本断言随该补价
	// 回正——补价落库后本用例断言未同步，属定价事实回正非语义变更）。
	var status, upstreamJobID string
	var costUsd float64
	if err := fixture.db.QueryRow(`SELECT status, upstream_job_id, cost_usd FROM media_jobs WHERE id = ?`, jobID).Scan(&status, &upstreamJobID, &costUsd); err != nil {
		t.Fatalf("查询 media_jobs 行: %v", err)
	}
	if status != "completed" || upstreamJobID != providerJobID {
		t.Fatalf("media_jobs = %s/%s, want completed/%s", status, upstreamJobID, providerJobID)
	}
	if costUsd != 0.5 {
		t.Fatalf("qwen 按目录秒价计费 cost_usd = %v, want 0.5（5s × $0.1/s）", costUsd)
	}

	// content 下载：completed 的 output.video_url 是引擎渲染的绝对 URL（不经
	// 记录层），网关无凭据直连——引擎侧 Authorization 记录为空可证。响应为
	// mp4 流。
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
		if strings.HasPrefix(request.Path, "/api/v1/tasks/") &&
			strings.HasSuffix(request.Path, "/content") && request.AuthHeader != "" {
			t.Fatalf("产物直连不得携带 Authorization: %q", request.AuthHeader)
		}
	}

	// 终态 usage：spool 链异步落盘，completed 携带 usage JSON 字符串解析出的
	// 5 秒输出计量（outputVideoSeconds），目录无价 → 无 costUsd 行项、无
	// usageMissing 标记（契约 §2.8：计量照落成本不虚计）。
	waitGlmSpoolRecord(t, spoolDir, func(text string) bool {
		return strings.Contains(text, `"outputVideoSeconds":5`) && strings.Contains(text, `"endpoint":"/v1/videos"`)
	}, "qwen completed 秒计量终态记录")
}

// TestChainMediaQwenVideoFailedTerminal 覆盖链路 2：task_status=FAILED + 顶层
// code/message → failed 终态（错误摘要 code/message 透传、cost 0、终态 usage
// 行 success=false）。
func TestChainMediaQwenVideoFailedTerminal(t *testing.T) {
	fixture := newChainFixture(t)
	_, _, upstreamURL := newQwenRecordedUpstream(t)
	apiKey := seedMediaQwenAccount(t, fixture, "acc_qwen_media_fail", upstreamURL, "sk-dashscope-key-fail")

	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain := composeMediaVideoChain(t, fixture, spoolDir)
	server := httptest.NewServer(chain)
	defer server.Close()

	job := mediaVideoCreateJob(t, server.URL, apiKey,
		`{"model":"wan2.2-t2v-plus","prompt":"失败终态"}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaQwenPollFailedError)})
	jobID, _ := job["id"].(string)

	terminal := mediaVideoPollOnce(t, server.URL, apiKey, jobID)
	if terminal["status"] != "failed" {
		t.Fatalf("轮询 status = %v, want failed: %v", terminal["status"], terminal)
	}
	jobError, _ := terminal["error"].(map[string]any)
	if jobError == nil || jobError["code"] != "InternalError" {
		t.Fatalf("failed error 摘要错误（code 应取顶层 code 透传）: %v", terminal["error"])
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
	}, "qwen failed 终态记录")
}

// TestChainMediaQwenVideoCancel 覆盖链路 3：DELETE → qwen §10.1 面无上游取消
// API → 不发上游取消请求、直接本地收敛 cancelled + 204；终态行轮询回本地
// 对象不再打上游。
func TestChainMediaQwenVideoCancel(t *testing.T) {
	fixture := newChainFixture(t)
	recorder, _, upstreamURL := newQwenRecordedUpstream(t)
	apiKey := seedMediaQwenAccount(t, fixture, "acc_qwen_media_cancel", upstreamURL, "sk-dashscope-key-cancel")

	chain := composeMediaVideoChain(t, fixture, filepath.Join(t.TempDir(), "spool"))
	server := httptest.NewServer(chain)
	defer server.Close()

	job := mediaVideoCreateJob(t, server.URL, apiKey,
		`{"model":"wan2.2-t2v-plus","prompt":"取消语义"}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaQwenPollRunning)})
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
	// 账户亲和面：取消未发任何上游请求（§10.1 面无取消端点，本地收敛），
	// 记录层仅有创建一条。
	records := recorder.snapshot()
	if len(records) != 1 {
		t.Fatalf("qwen 取消不得打上游（SupportsCancel=false 本地收敛）: %+v", records)
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

// TestChainMediaQwenVideoCreateSizeFormLocal400 覆盖链路 4：size 非 WxH 像素
// 串（16:9 比例串）→ 网关本地 400（invalid_request_error，契约 §2.4 规则 2
// ：万相 size 是 W*H 像素形态，公共层 WxH 校验失败不猜测换算），不打上游。
func TestChainMediaQwenVideoCreateSizeFormLocal400(t *testing.T) {
	fixture := newChainFixture(t)
	recorder, _, upstreamURL := newQwenRecordedUpstream(t)
	apiKey := seedMediaQwenAccount(t, fixture, "acc_qwen_media_400", upstreamURL, "sk-dashscope-key-400")

	chain := composeMediaVideoChain(t, fixture, filepath.Join(t.TempDir(), "spool"))
	server := httptest.NewServer(chain)
	defer server.Close()

	response, payload := mediaVideoClientDo(t, server.URL, http.MethodPost, "/v1/videos", apiKey,
		`{"model":"wan2.2-t2v-plus","prompt":"比例串形态","size":"16:9"}`,
		nil)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("size=16:9 status=%d, want 400（本地形态校验）body=%s", response.StatusCode, payload)
	}
	if !strings.Contains(string(payload), "invalid_request_error") {
		t.Fatalf("400 响应缺少 invalid_request_error: %s", payload)
	}
	if len(recorder.snapshot()) != 0 {
		t.Fatalf("本地 400 不得打上游: %+v", recorder.snapshot())
	}
}
