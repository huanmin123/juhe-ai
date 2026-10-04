package main

// M3 回填池 xai（Grok Imagine Video）chain 级集成测试（契约 §6.1；媒体设计
// §4.2/§7/§8）：进程内 fixture（newChainFixture 先例）+ xai 分组/账户 +
// mockupstream media_xai_* 场景。覆盖链路：
//  1. 创建（报文改写：duration 整数化、size→aspect_ratio+resolution、image
//     首帧直传、generate_audio 布尔直传 + Bearer 认证 + openai 族 /v1 URL
//     归一 + params 回显）→ 轮询 pending→done（video.url 冻结进 artifact +
//     video.duration=6 秒计量）→ content 经绝对 URL 无凭据直连下载 mp4 +
//     终态 usage spool（秒计量照落、目录无 USD 秒价成本 0，不虚计）；
//  2. DELETE 取消（xai §6.1 面无取消 API → 不发上游请求，本地收敛
//     cancelled）；
//  3. 词表外宽高比 → 本地 400 参数边界（契约 §2.4 规则 2），不打上游。
//
// 上游拓扑：账户 base_url 指向包一层的手工记录服务器（断言 Bearer 认证头与
// 出站路径；沿 qwen 链级测试的记录层先例），内层为 mockupstream 引擎；
// completed 的产物 url 由引擎按自身 URL 渲染，网关直连该 URL（不经过记录层）。
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

// xaiRecordedRequest 是记录层捕获的一条上游请求（方法/路径/认证面）。
type xaiRecordedRequest struct {
	Method        string
	Path          string
	Authorization string
}

// xaiUpstreamRecorder 是账户 base_url 指向的记录层：逐请求记录认证头并
// 转发给 mockupstream 引擎（沿 qwenUpstreamRecorder 先例；xai 无厂商私有头，
// 记录面只留 Authorization）。
type xaiUpstreamRecorder struct {
	mu       sync.Mutex
	requests []xaiRecordedRequest
}

func (r *xaiUpstreamRecorder) record(method, path, authorization string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, xaiRecordedRequest{
		Method: method, Path: path, Authorization: authorization,
	})
}

func (r *xaiUpstreamRecorder) snapshot() []xaiRecordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]xaiRecordedRequest(nil), r.requests...)
}

// newXaiRecordedUpstream 组装「记录层 + mockupstream 引擎」双层上游，返回
// 记录层、引擎与记录层地址（种账户 base_url 用）。
func newXaiRecordedUpstream(t *testing.T) (*xaiUpstreamRecorder, *platformmock.Server, string) {
	t.Helper()
	mock := platformmock.New()
	recorder := &xaiUpstreamRecorder{}
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

// seedMediaXaiAccount 种 xai 分组 + Grok Imagine 测试账户 + 独立路由策略与
// API Key（不复用 fixture 的 openai 组，避免跨 provider 候选串扰），返回网关
// API Key 明文。凭据带 base_url 与 supported_endpoint_modes（openai 族词表：
// video_* 四值，opt-in 语义——xai 账户写侧走 openai 族归一，无媒体收敛）。
// 模型约束种 grok-imagine-video-1.5（目录 seed 预置行）。
func seedMediaXaiAccount(t *testing.T, fixture *chainFixture, id, baseURL, apiKey string) string {
	t.Helper()
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(query, args...); err != nil {
			t.Fatalf("seed xai media row: %v: %v", query, err)
		}
	}
	keySecret := "sk-chain-xai-media"
	credentials := map[string]any{
		"api_key":  apiKey,
		"base_url": baseURL,
		"supported_endpoint_modes": []string{
			"video_create", "video_get", "video_content", "video_cancel",
		},
	}
	seed(`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type)
		VALUES ('group_xai_media', ?, 'xai', 1, 'personal')`, fixture.systemAccount)
	seed(`INSERT INTO accounts (
			id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
			name, type, status, schedulable, concurrency_limit, priority, credentials_encrypted, deleted_at
		) VALUES (?, ?, 'xai', 'profile_xai_openai_v1', 'openai', 'v1', ?, 'api_key', 'active', 1, 0, 0, ?, NULL)`,
		id, fixture.systemAccount, id, mustEncryptCredentials(t, credentials))
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
		VALUES ('group_xai_media', ?, ?, 1, '2026-10-04T00:00:00.000Z')`, fixture.systemAccount, id)
	seed(`INSERT INTO account_supported_models (account_id, provider_code, model, created_at)
		VALUES (?, 'xai', 'grok-imagine-video-1.5', '2026-10-04T00:00:00.000Z')`, id)
	seed(`INSERT INTO route_strategies (id, system_account_id, name, mode, config_json, status)
		VALUES ('rs_xai_media', ?, 'xai媒体', 'normal', NULL, 'active')`, fixture.systemAccount)
	seed(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at)
		VALUES ('rsg_xai_media', 'rs_xai_media', ?, 'group_xai_media', 0, 1, 'active', '2026-10-04T00:00:00.000Z')`, fixture.systemAccount)
	seed(`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, status, created_at)
		VALUES ('key_xai_media', ?, 'rs_xai_media', 'xai媒体Key', ?, 'active', '2026-10-04T00:00:00.000Z')`,
		fixture.systemAccount, gatewayruntimecache.HashSecret(keySecret))
	return keySecret
}

// TestChainMediaXaiVideoFullFlowLifecycle 覆盖链路 1：创建（报文改写 + Bearer
// 认证头 + openai 族 /v1 URL 归一 + applied/ignored 回显）→ 轮询 pending→
// in_progress、done→completed（video.url 进 artifact）→ content 绝对 URL
// 无凭据直连 mp4 → 终态 usage（video.duration=6 秒计量照落；目录无 USD 秒价
// 成本 0，契约 §2.8 不虚计）。
func TestChainMediaXaiVideoFullFlowLifecycle(t *testing.T) {
	fixture := newChainFixture(t)
	recorder, mock, upstreamURL := newXaiRecordedUpstream(t)
	apiKey := seedMediaXaiAccount(t, fixture, "acc_xai_media", upstreamURL, "sk-xai-key-a")

	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain := composeMediaVideoChain(t, fixture, spoolDir)
	server := httptest.NewServer(chain)
	defer server.Close()

	job := mediaVideoCreateJob(t, server.URL, apiKey,
		`{"model":"grok-imagine-video-1.5","prompt":"一只猫在弹钢琴","seconds":6,"size":"1280x720","input_reference":"data:image/png;base64,aGVsbG8=","negative_prompt":"模糊","seed":42,"audio":false,"provider_options":{"xai":{"reference_audios":["voice-1"]}}}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaXaiVideoCreateRequestID)})
	jobID, _ := job["id"].(string)
	if jobID == "" || !strings.HasPrefix(jobID, "video_") {
		t.Fatalf("对外 job id 缺失或非 video_ 前缀: %#v", job)
	}
	// 创建响应 {"request_id"} 无 status 字段 → 空归一 in_progress（pending
	// 同义，受理凭据确立即已受理）。
	if job["status"] != "in_progress" {
		t.Fatalf("创建响应 status = %v, want in_progress（创建响应无 status，空归一）", job["status"])
	}
	if job["provider"] != "xai" {
		t.Fatalf("provider = %v, want xai", job["provider"])
	}
	providerJobID, _ := job["provider_job_id"].(string)
	if len(providerJobID) != 36 || !strings.Contains(providerJobID, "-") {
		t.Fatalf("provider_job_id = %q, want uuid request_id 形态", providerJobID)
	}
	applied := mediaVideoJobKeys(t, job, "params_applied")
	for _, want := range []string{"model", "prompt", "seconds", "size", "input_reference", "audio"} {
		if !applied[want] {
			t.Fatalf("params_applied 缺少 %s: %v", want, job["params_applied"])
		}
	}
	ignored := mediaVideoJobKeys(t, job, "params_ignored")
	for _, want := range []string{"negative_prompt", "seed"} {
		if !ignored[want] {
			t.Fatalf("params_ignored 缺少 %s（xai 请求面无对应字段）: %v", want, job["params_ignored"])
		}
	}

	// 创建出站断言：POST /v1/videos/generations + Bearer 认证头（openai 族
	// 认权分支）+ 转换报文（duration 整数、aspect_ratio/resolution 档位、
	// image 直传、generate_audio 布尔、provider_options deep-merge），
	// ignored 键不进报文。
	records := recorder.snapshot()
	if len(records) != 1 || records[0].Method != http.MethodPost || records[0].Path != "/v1/videos/generations" {
		t.Fatalf("创建出站请求形态错误: %+v", records)
	}
	if records[0].Authorization != "Bearer sk-xai-key-a" {
		t.Fatalf("创建请求 Authorization = %q, want Bearer sk-xai-key-a", records[0].Authorization)
	}
	createBody := ""
	for _, request := range mock.Requests() {
		if request.Method == http.MethodPost && request.Path == "/v1/videos/generations" {
			createBody = request.Body
		}
	}
	if createBody == "" {
		t.Fatal("引擎侧未录制创建请求")
	}
	for _, want := range []string{
		`"model":"grok-imagine-video-1.5"`,
		`"prompt":"一只猫在弹钢琴"`,
		`"duration":6`,
		`"aspect_ratio":"16:9"`,
		`"resolution":"720p"`,
		`"image":"data:image/png;base64,aGVsbG8="`,
		`"generate_audio":false`,
		`"reference_audios":["voice-1"]`,
	} {
		if !strings.Contains(createBody, want) {
			t.Fatalf("创建报文缺少 %s: %s", want, createBody)
		}
	}
	for _, banned := range []string{`"negative_prompt"`, `"seed"`, `"n":`} {
		if strings.Contains(createBody, banned) {
			t.Fatalf("创建报文不得含 %s: %s", banned, createBody)
		}
	}

	// 轮询两次：in_progress（#1 pending 归一）→ completed（#2 done +
	// video.url 冻结 + duration 秒计量）。
	if first := mediaVideoPollOnce(t, server.URL, apiKey, jobID); first["status"] != "in_progress" {
		t.Fatalf("第一轮轮询 status = %v, want in_progress（pending 归一）", first["status"])
	}
	if second := mediaVideoPollOnce(t, server.URL, apiKey, jobID); second["status"] != "completed" {
		t.Fatalf("第二轮轮询 status = %v, want completed: %v", second["status"], second)
	}
	// 任务面轮询经记录层：GET /v1/videos/{request_id} + Bearer（openai 族
	// /v1 URL 归一——base 无 /v1 由 BuildUpstreamURL 强制补缀）。
	records = recorder.snapshot()
	if len(records) != 3 {
		t.Fatalf("轮询出站请求计数错误: %+v", records)
	}
	for _, record := range records[1:] {
		if record.Method != http.MethodGet || record.Path != "/v1/videos/"+providerJobID {
			t.Fatalf("轮询出站请求形态错误: %+v", record)
		}
		if record.Authorization != "Bearer sk-xai-key-a" {
			t.Fatalf("轮询请求 Authorization = %q, want Bearer sk-xai-key-a", record.Authorization)
		}
	}

	// media_jobs 行终态 completed + 任务 id 回填 + 秒计量照抽（done 的
	// video.duration=6）但目录无 USD 秒价 → cost 0（不虚计）。
	var status, upstreamJobID string
	var costUsd float64
	if err := fixture.db.QueryRow(`SELECT status, upstream_job_id, cost_usd FROM media_jobs WHERE id = ?`, jobID).Scan(&status, &upstreamJobID, &costUsd); err != nil {
		t.Fatalf("查询 media_jobs 行: %v", err)
	}
	if status != "completed" || upstreamJobID != providerJobID {
		t.Fatalf("media_jobs = %s/%s, want completed/%s", status, upstreamJobID, providerJobID)
	}
	if costUsd != 0 {
		t.Fatalf("xai 目录未落 USD 秒价，cost_usd = %v, want 0（计量照落成本不虚计）", costUsd)
	}

	// content 下载：completed 的 video.url 是引擎渲染的绝对 URL（不经记录层），
	// 网关无凭据直连——记录层无第四次命中可证。响应为 mp4 流。
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
	if len(recorder.snapshot()) != 3 {
		t.Fatalf("产物下载不得经账户 base_url（ContentFromArtifact 直连 url）: %+v", recorder.snapshot())
	}
	for _, request := range mock.Requests() {
		if strings.HasPrefix(request.Path, "/v1/videos/") &&
			strings.HasSuffix(request.Path, "/content") && request.AuthHeader != "" {
			t.Fatalf("产物直连不得携带 Authorization: %q", request.AuthHeader)
		}
	}

	// 终态 usage：spool 链异步落盘，completed 携带 video.duration=6 的输出
	// 秒计量（outputVideoSeconds），目录无价 → 无 costUsd 行项、无
	// usageMissing 标记（契约 §2.8：计量照落成本不虚计）。
	waitGlmSpoolRecord(t, spoolDir, func(text string) bool {
		return strings.Contains(text, `"outputVideoSeconds":6`) && strings.Contains(text, `"endpoint":"/v1/videos"`)
	}, "xai completed 秒计量终态记录")
}

// TestChainMediaXaiVideoCancelLocalConverge 覆盖链路 2：DELETE 取消——xai
// §6.1 面无取消端点（SupportsCancel=false）→ 不发上游请求，直接本地收敛
// cancelled（§2.6 本地终态语义，沿 glm/minimax/volcengine/qwen 裁决）。
func TestChainMediaXaiVideoCancelLocalConverge(t *testing.T) {
	fixture := newChainFixture(t)
	recorder, _, upstreamURL := newXaiRecordedUpstream(t)
	apiKey := seedMediaXaiAccount(t, fixture, "acc_xai_media_cancel", upstreamURL, "sk-xai-key-cancel")

	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain := composeMediaVideoChain(t, fixture, spoolDir)
	server := httptest.NewServer(chain)
	defer server.Close()

	job := mediaVideoCreateJob(t, server.URL, apiKey,
		`{"model":"grok-imagine-video-1.5","prompt":"取消路径"}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaXaiVideoPollPending)})
	jobID, _ := job["id"].(string)

	// DELETE：204，本地收敛 cancelled，不发上游请求（记录层仍只有创建一条）。
	response, payload := mediaVideoClientDo(t, server.URL, http.MethodDelete, "/v1/videos/"+jobID, apiKey, "", nil)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("cancel status=%d body=%s", response.StatusCode, payload)
	}
	var status string
	if err := fixture.db.QueryRow(`SELECT status FROM media_jobs WHERE id = ?`, jobID).Scan(&status); err != nil {
		t.Fatalf("查询 media_jobs 行: %v", err)
	}
	if status != "cancelled" {
		t.Fatalf("media_jobs status = %s, want cancelled（本地收敛）", status)
	}
	if calls := recorder.snapshot(); len(calls) != 1 {
		t.Fatalf("取消不得发上游请求（SupportsCancel=false）: %+v", calls)
	}
}

// TestChainMediaXaiVideoAspectRatioOutOfVocabulary 覆盖链路 3：size 宽高比
// 不在 xai 七值词表（854x480 约分 427:240）→ 本地 400 参数边界（契约 §2.4
// 规则 2），不打上游。
func TestChainMediaXaiVideoAspectRatioOutOfVocabulary(t *testing.T) {
	fixture := newChainFixture(t)
	recorder, _, upstreamURL := newXaiRecordedUpstream(t)
	apiKey := seedMediaXaiAccount(t, fixture, "acc_xai_media_400", upstreamURL, "sk-xai-key-400")

	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain := composeMediaVideoChain(t, fixture, spoolDir)
	server := httptest.NewServer(chain)
	defer server.Close()

	response, payload := mediaVideoClientDo(t, server.URL, http.MethodPost, "/v1/videos", apiKey,
		`{"model":"grok-imagine-video-1.5","prompt":"参数边界","size":"854x480"}`, nil)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("词表外宽高比 status=%d body=%s, want 400", response.StatusCode, payload)
	}
	if !strings.Contains(string(payload), "aspect_ratio") {
		t.Fatalf("400 错误应指向 aspect_ratio 词表: %s", payload)
	}
	if calls := recorder.snapshot(); len(calls) != 0 {
		t.Fatalf("参数边界不得打上游: %+v", calls)
	}
}
