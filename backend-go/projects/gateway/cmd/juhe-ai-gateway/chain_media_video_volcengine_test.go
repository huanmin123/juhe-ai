package main

// M3 volcengine（火山方舟 Seedance 视频）chain 级集成测试（契约 §9.1；媒体
// 设计 §4.2/§7/§8）：进程内 fixture（newChainFixture 先例）+ volcengine 分组/
// 账户 + mockupstream media_volcengine_* 场景。覆盖四条链路：
//  1. 创建（contents/generations/tasks 报文改写：prompt→content[].text、
//     input_reference 双形态直传、size→resolution+ratio 换算、seconds→
//     duration、seed 直传 + Bearer 认证 + /api/v3 服务根 URL 归一 + params
//     回显）→ 轮询 queued → in_progress → completed（content.video_url 冻结
//     进 artifact）→ content 经绝对 URL 无凭据直连下载 mp4 + 终态 usage
//     spool（usage_missing 口径，不虚计）；
//  2. status=failed + error{code,message} → failed 终态（code 透传 + cost 0）；
//  3. DELETE 取消（volcengine §9.1 面无取消 API → 不发上游请求，本地收敛
//     cancelled）；
//  4. size 宽高比词表外（1279x720）→ 本地 400 参数边界（契约 §2.4 规则 2），
//     不打上游。
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

// volcengineRecordedRequest 是记录层捕获的一条上游请求（方法/路径/认证面）。
type volcengineRecordedRequest struct {
	Method        string
	Path          string
	Authorization string
}

// volcengineUpstreamRecorder 是账户 base_url 指向的记录层：逐请求记录认证头并
// 转发给 mockupstream 引擎（场景头由客户端显式携带或引擎默认）。
type volcengineUpstreamRecorder struct {
	mu       sync.Mutex
	requests []volcengineRecordedRequest
}

func (r *volcengineUpstreamRecorder) record(method, path, authorization string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, volcengineRecordedRequest{Method: method, Path: path, Authorization: authorization})
}

func (r *volcengineUpstreamRecorder) snapshot() []volcengineRecordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]volcengineRecordedRequest(nil), r.requests...)
}

// newVolcengineRecordedUpstream 组装「记录层 + mockupstream 引擎」双层上游，
// 返回记录层、引擎与记录层地址（种账户 base_url 用）。
func newVolcengineRecordedUpstream(t *testing.T) (*volcengineUpstreamRecorder, *platformmock.Server, string) {
	t.Helper()
	mock := platformmock.New()
	recorder := &volcengineUpstreamRecorder{}
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

// seedMediaVolcengineAccount 种 volcengine 分组 + Seedance 测试账户 + 独立路由
// 策略与 API Key（不复用 fixture 的 openai 组，避免跨 provider 候选串扰），
// 返回网关 API Key 明文。凭据带 base_url 与 supported_endpoint_modes
//（openai 族词表：video_* 四值，opt-in 语义；volcengine 档案 Capabilities
// 只声明视频，无 chat/audio 模式）。模型约束种 doubao-seedance-1-0-pro-250528。
func seedMediaVolcengineAccount(t *testing.T, fixture *chainFixture, id, baseURL, apiKey string) string {
	t.Helper()
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(query, args...); err != nil {
			t.Fatalf("seed volcengine media row: %v: %v", query, err)
		}
	}
	keySecret := "sk-chain-volcengine-media"
	credentials := map[string]any{
		"api_key":  apiKey,
		"base_url": baseURL,
		"supported_endpoint_modes": []string{
			"video_create", "video_get", "video_content", "video_cancel",
		},
	}
	seed(`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type)
		VALUES ('group_volcengine_media', ?, 'volcengine', 1, 'personal')`, fixture.systemAccount)
	seed(`INSERT INTO accounts (
			id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
			name, type, status, schedulable, concurrency_limit, priority, credentials_encrypted, deleted_at
		) VALUES (?, ?, 'volcengine', 'profile_volcengine_openai_v1', 'openai', 'v1', ?, 'api_key', 'active', 1, 0, 0, ?, NULL)`,
		id, fixture.systemAccount, id, mustEncryptCredentials(t, credentials))
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
		VALUES ('group_volcengine_media', ?, ?, 1, '2026-10-04T00:00:00.000Z')`, fixture.systemAccount, id)
	seed(`INSERT INTO account_supported_models (account_id, provider_code, model, created_at)
		VALUES (?, 'volcengine', 'doubao-seedance-1-0-pro-250528', '2026-10-04T00:00:00.000Z')`, id)
	seed(`INSERT INTO route_strategies (id, system_account_id, name, mode, config_json, status)
		VALUES ('rs_volcengine_media', ?, 'volcengine媒体', 'normal', NULL, 'active')`, fixture.systemAccount)
	seed(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at)
		VALUES ('rsg_volcengine_media', 'rs_volcengine_media', ?, 'group_volcengine_media', 0, 1, 'active', '2026-10-04T00:00:00.000Z')`, fixture.systemAccount)
	seed(`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, status, created_at)
		VALUES ('key_volcengine_media', ?, 'rs_volcengine_media', 'volcengine媒体Key', ?, 'active', '2026-10-04T00:00:00.000Z')`,
		fixture.systemAccount, gatewayruntimecache.HashSecret(keySecret))
	return keySecret
}

// TestChainMediaVolcengineVideoFullFlowLifecycle 覆盖链路 1：创建
//（contents/generations/tasks 报文改写 + Bearer 认证头 + /api/v3 服务根 URL +
// applied/ignored 回显）→ 轮询 queued → in_progress（running 归一）→
// completed（content.video_url 进 artifact）→ content 绝对 URL 无凭据直连
// mp4 → 终态 usage（usage_missing，契约 §2.8 不虚计）。
func TestChainMediaVolcengineVideoFullFlowLifecycle(t *testing.T) {
	fixture := newChainFixture(t)
	recorder, mock, upstreamURL := newVolcengineRecordedUpstream(t)
	apiKey := seedMediaVolcengineAccount(t, fixture, "acc_volcengine_media", upstreamURL, "ark-key-a")

	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain := composeMediaVideoChain(t, fixture, spoolDir)
	server := httptest.NewServer(chain)
	defer server.Close()

	job := mediaVideoCreateJob(t, server.URL, apiKey,
		`{"model":"doubao-seedance-1-0-pro-250528","prompt":"一只猫在弹钢琴","seconds":5,"size":"1280x720","seed":42,"input_reference":"data:image/png;base64,aGVsbG8=","negative_prompt":"模糊","audio":false,"provider_options":{"volcengine":{"camera_fixed":true}}}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaVolcengineCreateOK)})
	jobID, _ := job["id"].(string)
	if jobID == "" || !strings.HasPrefix(jobID, "video_") {
		t.Fatalf("对外 job id 缺失或非 video_ 前缀: %#v", job)
	}
	if job["status"] != "queued" {
		t.Fatalf("创建响应 status = %v, want queued（创建响应 status=queued 归一）", job["status"])
	}
	if job["provider"] != "volcengine" {
		t.Fatalf("provider = %v, want volcengine", job["provider"])
	}
	providerJobID, _ := job["provider_job_id"].(string)
	if !strings.HasPrefix(providerJobID, "cgt-") || len(providerJobID) != len("cgt-")+16 {
		t.Fatalf("provider_job_id = %q, want cgt-<16hex> 任务 id 形态", providerJobID)
	}
	applied := mediaVideoJobKeys(t, job, "params_applied")
	for _, want := range []string{"model", "prompt", "seconds", "size", "seed", "input_reference"} {
		if !applied[want] {
			t.Fatalf("params_applied 缺少 %s: %v", want, job["params_applied"])
		}
	}
	ignored := mediaVideoJobKeys(t, job, "params_ignored")
	for _, want := range []string{"negative_prompt", "audio"} {
		if !ignored[want] {
			t.Fatalf("params_ignored 缺少 %s（volcengine 请求面无对应字段）: %v", want, job["params_ignored"])
		}
	}

	// 创建出站断言：POST /api/v3/contents/generations/tasks + Bearer 认证头 +
	// 转换报文（content 数组 text/image_url 段、resolution/ratio 换算、
	// duration 数值、seed、provider_options 合并），ignored 键不进报文。
	records := recorder.snapshot()
	if len(records) != 1 || records[0].Method != http.MethodPost || records[0].Path != "/api/v3/contents/generations/tasks" {
		t.Fatalf("创建出站请求形态错误: %+v", records)
	}
	if records[0].Authorization != "Bearer ark-key-a" {
		t.Fatalf("创建请求 Authorization = %q, want Bearer ark-key-a", records[0].Authorization)
	}
	createBody := ""
	for _, request := range mock.Requests() {
		if request.Method == http.MethodPost && request.Path == "/api/v3/contents/generations/tasks" {
			createBody = request.Body
		}
	}
	if createBody == "" {
		t.Fatal("引擎侧未录制创建请求")
	}
	for _, want := range []string{
		`"model":"doubao-seedance-1-0-pro-250528"`,
		`"text":"一只猫在弹钢琴"`, `"type":"text"`,
		`"image_url":{"url":"data:image/png;base64,aGVsbG8="}`, `"type":"image_url"`,
		`"resolution":"720p"`, `"ratio":"16:9"`, `"duration":5`, `"seed":42`,
		`"camera_fixed":true`,
	} {
		if !strings.Contains(createBody, want) {
			t.Fatalf("创建报文缺少 %s: %s", want, createBody)
		}
	}
	for _, banned := range []string{`"negative_prompt"`, `"audio"`, `"size"`} {
		if strings.Contains(createBody, banned) {
			t.Fatalf("创建报文不得含 %s: %s", banned, createBody)
		}
	}

	// 轮询三次：queued（#1）→ in_progress（#2 running）→ completed（#3
	// succeeded + content.video_url 冻结）。
	if first := mediaVideoPollOnce(t, server.URL, apiKey, jobID); first["status"] != "queued" {
		t.Fatalf("第一轮轮询 status = %v, want queued", first["status"])
	}
	if second := mediaVideoPollOnce(t, server.URL, apiKey, jobID); second["status"] != "in_progress" {
		t.Fatalf("第二轮轮询 status = %v, want in_progress（running 归一）", second["status"])
	}
	if third := mediaVideoPollOnce(t, server.URL, apiKey, jobID); third["status"] != "completed" {
		t.Fatalf("第三轮轮询 status = %v, want completed: %v", third["status"], third)
	}
	// 任务面轮询经记录层：GET /api/v3/contents/generations/tasks/{id} + Bearer。
	records = recorder.snapshot()
	if len(records) != 4 {
		t.Fatalf("轮询出站请求计数错误: %+v", records)
	}
	for _, record := range records[1:] {
		if record.Method != http.MethodGet || record.Path != "/api/v3/contents/generations/tasks/"+providerJobID {
			t.Fatalf("轮询出站请求形态错误: %+v", record)
		}
		if record.Authorization != "Bearer ark-key-a" {
			t.Fatalf("轮询请求 Authorization = %q, want Bearer ark-key-a", record.Authorization)
		}
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
		t.Fatalf("volcengine 无输出秒回报且目录未落秒价，cost_usd = %v, want 0（usage_missing 不虚计）", costUsd)
	}

	// content 下载：completed 的 content.video_url 是引擎渲染的绝对 URL
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
		if strings.HasPrefix(request.Path, "/api/v3/contents/generations/tasks/") &&
			strings.HasSuffix(request.Path, "/content") && request.AuthHeader != "" {
			t.Fatalf("产物直连不得携带 Authorization: %q", request.AuthHeader)
		}
	}

	// 终态 usage：spool 链异步落盘，completed 无秒计量 → usageMissing 标记
	//（契约 §2.8：上游 usage/duration 字段未回填 → 0 计费 + usage_missing，
	// 不猜测，同 glm/minimax 先例）。
	waitGlmSpoolRecord(t, spoolDir, func(text string) bool {
		return strings.Contains(text, `"usageMissing":true`) && strings.Contains(text, `"endpoint":"/v1/videos"`)
	}, "volcengine completed usage_missing 终态记录")
}

// TestChainMediaVolcengineVideoFailedTerminal 覆盖链路 2：status=failed +
// error{code,message} → failed 终态（错误摘要 code/message 透传、cost 0、终态
// usage 行 success=false）。
func TestChainMediaVolcengineVideoFailedTerminal(t *testing.T) {
	fixture := newChainFixture(t)
	_, _, upstreamURL := newVolcengineRecordedUpstream(t)
	apiKey := seedMediaVolcengineAccount(t, fixture, "acc_volcengine_media_fail", upstreamURL, "ark-key-fail")

	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain := composeMediaVideoChain(t, fixture, spoolDir)
	server := httptest.NewServer(chain)
	defer server.Close()

	job := mediaVideoCreateJob(t, server.URL, apiKey,
		`{"model":"doubao-seedance-1-0-pro-250528","prompt":"失败终态"}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaVolcenginePollFailedError)})
	jobID, _ := job["id"].(string)

	terminal := mediaVideoPollOnce(t, server.URL, apiKey, jobID)
	if terminal["status"] != "failed" {
		t.Fatalf("轮询 status = %v, want failed: %v", terminal["status"], terminal)
	}
	jobError, _ := terminal["error"].(map[string]any)
	if jobError == nil || jobError["code"] != "InternalServiceError" {
		t.Fatalf("failed error 摘要错误（code 应取 error.code 透传）: %v", terminal["error"])
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
	}, "volcengine failed 终态记录")
}

// TestChainMediaVolcengineVideoCancel 覆盖链路 3：DELETE → volcengine §9.1 面无
// 上游取消 API → 不发上游取消请求、直接本地收敛 cancelled + 204；终态行轮询
// 回本地对象不再打上游。
func TestChainMediaVolcengineVideoCancel(t *testing.T) {
	fixture := newChainFixture(t)
	recorder, _, upstreamURL := newVolcengineRecordedUpstream(t)
	apiKey := seedMediaVolcengineAccount(t, fixture, "acc_volcengine_media_cancel", upstreamURL, "ark-key-cancel")

	chain := composeMediaVideoChain(t, fixture, filepath.Join(t.TempDir(), "spool"))
	server := httptest.NewServer(chain)
	defer server.Close()

	job := mediaVideoCreateJob(t, server.URL, apiKey,
		`{"model":"doubao-seedance-1-0-pro-250528","prompt":"取消语义"}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaVolcenginePollRunning)})
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
	// 账户亲和面：取消未发任何上游请求（§9.1 面无取消端点，本地收敛），
	// 记录层仅有创建一条。
	records := recorder.snapshot()
	if len(records) != 1 {
		t.Fatalf("volcengine 取消不得打上游（SupportsCancel=false 本地收敛）: %+v", records)
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

// TestChainMediaVolcengineVideoCreateSizeRatioLocal400 覆盖链路 4：size 宽高比
// 约分后不在火山 ratio 词表（16:9/9:16/1:1/4:3/3:4/21:9）→ 网关本地 400
//（invalid_request_error，契约 §2.4 规则 2：不近似贴合不猜测），不打上游。
func TestChainMediaVolcengineVideoCreateSizeRatioLocal400(t *testing.T) {
	fixture := newChainFixture(t)
	recorder, _, upstreamURL := newVolcengineRecordedUpstream(t)
	apiKey := seedMediaVolcengineAccount(t, fixture, "acc_volcengine_media_400", upstreamURL, "ark-key-400")

	chain := composeMediaVideoChain(t, fixture, filepath.Join(t.TempDir(), "spool"))
	server := httptest.NewServer(chain)
	defer server.Close()

	response, payload := mediaVideoClientDo(t, server.URL, http.MethodPost, "/v1/videos", apiKey,
		`{"model":"doubao-seedance-1-0-pro-250528","prompt":"词表外宽高比","size":"1279x720"}`,
		nil)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("size=1279x720 status=%d, want 400（本地能力边界裁决）body=%s", response.StatusCode, payload)
	}
	if !strings.Contains(string(payload), "invalid_request_error") {
		t.Fatalf("400 响应缺少 invalid_request_error: %s", payload)
	}
	if len(recorder.snapshot()) != 0 {
		t.Fatalf("本地 400 不得打上游: %+v", recorder.snapshot())
	}
}
