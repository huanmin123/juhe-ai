package main

// M3 glm（CogVideoX）视频任务链 chain 级集成测试（契约 §7.1；媒体设计 §4.2/
// §7/§8）：进程内 fixture（newChainFixture 先例）+ glm 分组/账户 + mockupstream
// media_glm_video_* 场景。覆盖三条链路：
//  1. 创建（videos/generations 报文改写 + Bearer 认证 + /api/paas/v4 服务根
//     URL 归一 + params 回显）→ 轮询 in_progress → completed（url 冻结进
//     artifact）→ content 经绝对 URL 无凭据直连下载 mp4 + 终态 usage spool
//    （usage_missing 口径，不虚计）；
//  2. task_status=FAIL → failed 终态（task_status 摘要 code + cost 0）；
//  3. DELETE 取消（glm 无上游取消 API → 不发上游请求，本地收敛 cancelled）。
//
// 上游拓扑：账户 base_url 指向包一层的手工记录服务器（断言 Bearer 认证头
// 与出站路径），内层为 mockupstream 引擎；completed 的产物 url 由引擎按自身
// URL 渲染，网关直连该 URL（不经过记录层）。
import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	platformmock "github.com/huanminabc/juhe-ai/backend-go-platform/mockupstream"
)

// glmRecordedRequest 是记录层捕获的一条上游请求（方法/路径/认证面）。
type glmRecordedRequest struct {
	Method        string
	Path          string
	Authorization string
}

// glmUpstreamRecorder 是账户 base_url 指向的记录层：逐请求记录认证头并转发
// 给 mockupstream 引擎（场景头由客户端显式携带或引擎默认）。
type glmUpstreamRecorder struct {
	mu       sync.Mutex
	requests []glmRecordedRequest
}

func (r *glmUpstreamRecorder) record(method, path, authorization string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, glmRecordedRequest{Method: method, Path: path, Authorization: authorization})
}

func (r *glmUpstreamRecorder) snapshot() []glmRecordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]glmRecordedRequest(nil), r.requests...)
}

// newGlmRecordedUpstream 组装「记录层 + mockupstream 引擎」双层上游，返回
// 记录层、引擎与记录层地址（种账户 base_url 用）。
func newGlmRecordedUpstream(t *testing.T) (*glmUpstreamRecorder, *platformmock.Server, string) {
	t.Helper()
	mock := platformmock.New()
	recorder := &glmUpstreamRecorder{}
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

// seedMediaGlmVideoAccount 种 glm 分组 + cogvideo 测试账户 + 独立路由策略与
// API Key（不复用 fixture 的 openai 组，避免跨 provider 候选串扰），返回
// 网关 API Key 明文。凭据带 base_url 与 supported_endpoint_modes（openai 族
// 词表：chat_json + video_*，opt-in 语义；glm 写侧词表已放宽 video_*）。
// 模型约束种 cogvideox-3。
func seedMediaGlmVideoAccount(t *testing.T, fixture *chainFixture, id, baseURL, apiKey string) string {
	t.Helper()
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(query, args...); err != nil {
			t.Fatalf("seed glm video row: %v: %v", query, err)
		}
	}
	keySecret := "sk-chain-glm-video"
	credentials := map[string]any{
		"api_key":  apiKey,
		"base_url": baseURL,
		"supported_endpoint_modes": []string{
			"chat_json", "video_create", "video_get", "video_content", "video_cancel",
		},
	}
	seed(`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type)
		VALUES ('group_glm_video', ?, 'glm', 1, 'personal')`, fixture.systemAccount)
	seed(`INSERT INTO accounts (
			id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
			name, type, status, schedulable, concurrency_limit, priority, credentials_encrypted, deleted_at
		) VALUES (?, ?, 'glm', 'profile_glm_general_openai_v1', 'openai', 'v1', ?, 'api_key', 'active', 1, 0, 0, ?, NULL)`,
		id, fixture.systemAccount, id, mustEncryptCredentials(t, credentials))
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
		VALUES ('group_glm_video', ?, ?, 1, '2026-10-04T00:00:00.000Z')`, fixture.systemAccount, id)
	seed(`INSERT INTO account_supported_models (account_id, provider_code, model, created_at)
		VALUES (?, 'glm', 'cogvideox-3', '2026-10-04T00:00:00.000Z')`, id)
	seed(`INSERT INTO route_strategies (id, system_account_id, name, mode, config_json, status)
		VALUES ('rs_glm_video', ?, 'glm视频', 'normal', NULL, 'active')`, fixture.systemAccount)
	seed(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at)
		VALUES ('rsg_glm_video', 'rs_glm_video', ?, 'group_glm_video', 0, 1, 'active', '2026-10-04T00:00:00.000Z')`, fixture.systemAccount)
	seed(`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, status, created_at)
		VALUES ('key_glm_video', ?, 'rs_glm_video', 'glm视频Key', ?, 'active', '2026-10-04T00:00:00.000Z')`,
		fixture.systemAccount, gatewayruntimecache.HashSecret(keySecret))
	return keySecret
}

// TestChainMediaGlmVideoFullFlowLifecycle 覆盖链路 1：创建（videos/generations
// 报文改写 + Bearer 认证头 + 服务根 URL 归一 + ignored 回显）→ 轮询
// in_progress → completed（url 进 artifact）→ content 绝对 URL 无凭据直连
// mp4 → 终态 usage（usage_missing，契约 §2.8 不虚计）。
func TestChainMediaGlmVideoFullFlowLifecycle(t *testing.T) {
	fixture := newChainFixture(t)
	recorder, mock, upstreamURL := newGlmRecordedUpstream(t)
	apiKey := seedMediaGlmVideoAccount(t, fixture, "acc_glm_video", upstreamURL, "glm-key-a")

	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain := composeMediaVideoChain(t, fixture, spoolDir)
	server := httptest.NewServer(chain)
	defer server.Close()

	job := mediaVideoCreateJob(t, server.URL, apiKey,
		`{"model":"cogvideox-3","prompt":"一只猫在弹钢琴","size":"1280x720","negative_prompt":"低清画质","seconds":"6","audio":true,"seed":42}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaGlmVideoCreateOK)})
	jobID, _ := job["id"].(string)
	if jobID == "" || !strings.HasPrefix(jobID, "video_") {
		t.Fatalf("对外 job id 缺失或非 video_ 前缀: %#v", job)
	}
	if job["status"] != "in_progress" {
		t.Fatalf("创建响应 status = %v, want in_progress（task_status=PROCESSING 归一）", job["status"])
	}
	if job["provider"] != "glm" {
		t.Fatalf("provider = %v, want glm", job["provider"])
	}
	providerJobID, _ := job["provider_job_id"].(string)
	if len(providerJobID) != 12 || strings.ContainsAny(providerJobID, "/-") {
		t.Fatalf("provider_job_id = %q, want 12 hex 任务 id 形态", providerJobID)
	}
	applied := mediaVideoJobKeys(t, job, "params_applied")
	for _, want := range []string{"model", "prompt", "size", "negative_prompt", "seconds", "audio"} {
		if !applied[want] {
			t.Fatalf("params_applied 缺少 %s: %v", want, job["params_applied"])
		}
	}
	if ignored := mediaVideoJobKeys(t, job, "params_ignored"); !ignored["seed"] {
		t.Fatalf("params_ignored 缺少 seed（glm 请求面无 seed）: %v", job["params_ignored"])
	}

	// 创建出站断言：POST /api/paas/v4/videos/generations + Bearer 认证头 +
	// 转换报文（duration 5s 最近档、with_audio、image_url/size 语义），ignored
	// 键不进报文。
	records := recorder.snapshot()
	if len(records) != 1 || records[0].Method != http.MethodPost || records[0].Path != "/api/paas/v4/videos/generations" {
		t.Fatalf("创建出站请求形态错误: %+v", records)
	}
	if records[0].Authorization != "Bearer glm-key-a" {
		t.Fatalf("创建请求 Authorization = %q, want Bearer glm-key-a", records[0].Authorization)
	}
	createBody := ""
	for _, request := range mock.Requests() {
		if request.Method == http.MethodPost && request.Path == "/api/paas/v4/videos/generations" {
			createBody = request.Body
		}
	}
	if createBody == "" {
		t.Fatal("引擎侧未录制创建请求")
	}
	for _, want := range []string{
		`"model":"cogvideox-3"`, `"prompt":"一只猫在弹钢琴"`, `"negative_prompt":"低清画质"`,
		`"size":"1280x720"`, `"duration":"5s"`, `"with_audio":true`,
	} {
		if !strings.Contains(createBody, want) {
			t.Fatalf("创建报文缺少 %s: %s", want, createBody)
		}
	}
	if strings.Contains(createBody, `"seed"`) {
		t.Fatalf("创建报文不得含 ignored 键 seed: %s", createBody)
	}

	// 轮询两次：in_progress（PROCESSING）→ completed（SUCCESS + url）。
	if first := mediaVideoPollOnce(t, server.URL, apiKey, jobID); first["status"] != "in_progress" {
		t.Fatalf("第一轮轮询 status = %v, want in_progress", first["status"])
	}
	if second := mediaVideoPollOnce(t, server.URL, apiKey, jobID); second["status"] != "completed" {
		t.Fatalf("第二轮轮询 status = %v, want completed: %v", second["status"], second)
	}
	// 任务面轮询经记录层：GET /api/paas/v4/async-result/{id} + Bearer 认证头。
	records = recorder.snapshot()
	if len(records) != 3 ||
		records[1].Method != http.MethodGet || records[1].Path != "/api/paas/v4/async-result/"+providerJobID ||
		records[2].Method != http.MethodGet || records[2].Path != "/api/paas/v4/async-result/"+providerJobID {
		t.Fatalf("轮询出站请求形态错误: %+v", records)
	}
	for _, record := range records[1:] {
		if record.Authorization != "Bearer glm-key-a" {
			t.Fatalf("轮询请求 Authorization = %q, want Bearer glm-key-a", record.Authorization)
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
		t.Fatalf("glm 无输出秒回报且目录未落秒价，cost_usd = %v, want 0（usage_missing 不虚计）", costUsd)
	}

	// content 下载：completed 的 url 是引擎渲染的绝对 URL（不经记录层），
	// 网关无凭据直连——引擎侧 Authorization 记录为空可证。响应为 mp4 流。
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
		if request.Path == "/api/paas/v4/async-result/"+providerJobID+"/content" && request.AuthHeader != "" {
			t.Fatalf("产物直连不得携带 Authorization: %q", request.AuthHeader)
		}
	}

	// 终态 usage：spool 链异步落盘，completed 无秒计量 → usageMissing 标记
	//（契约 §2.8：上游不回报且无可查证秒价 → 0 计费 + usage_missing，不猜测）。
	waitGlmSpoolRecord(t, spoolDir, func(text string) bool {
		return strings.Contains(text, `"usageMissing":true`) && strings.Contains(text, `"endpoint":"/v1/videos"`)
	}, "glm completed usage_missing 终态记录")
}

// TestChainMediaGlmVideoFailedTerminal 覆盖链路 2：task_status=FAIL →
// failed 终态（task_status 摘要 code 透传、cost 0、终态 usage 行
// success=false）。
func TestChainMediaGlmVideoFailedTerminal(t *testing.T) {
	fixture := newChainFixture(t)
	_, _, upstreamURL := newGlmRecordedUpstream(t)
	apiKey := seedMediaGlmVideoAccount(t, fixture, "acc_glm_video_fail", upstreamURL, "glm-key-fail")

	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain := composeMediaVideoChain(t, fixture, spoolDir)
	server := httptest.NewServer(chain)
	defer server.Close()

	job := mediaVideoCreateJob(t, server.URL, apiKey,
		`{"model":"cogvideox-3","prompt":"失败终态","size":"720x1440"}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaGlmVideoPollFail)})
	jobID, _ := job["id"].(string)

	terminal := mediaVideoPollOnce(t, server.URL, apiKey, jobID)
	if terminal["status"] != "failed" {
		t.Fatalf("轮询 status = %v, want failed: %v", terminal["status"], terminal)
	}
	jobError, _ := terminal["error"].(map[string]any)
	if jobError == nil || jobError["code"] != "FAIL" {
		t.Fatalf("failed error 摘要错误: %v", terminal["error"])
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
	}, "glm failed 终态记录")
}

// TestChainMediaGlmVideoCancel 覆盖链路 3：DELETE → glm 无上游取消 API
//（契约 §7.1）→ 不发上游取消请求、直接本地收敛 cancelled + 204；终态行
// 轮询回本地对象不再打上游。
func TestChainMediaGlmVideoCancel(t *testing.T) {
	fixture := newChainFixture(t)
	recorder, _, upstreamURL := newGlmRecordedUpstream(t)
	apiKey := seedMediaGlmVideoAccount(t, fixture, "acc_glm_video_cancel", upstreamURL, "glm-key-cancel")

	chain := composeMediaVideoChain(t, fixture, filepath.Join(t.TempDir(), "spool"))
	server := httptest.NewServer(chain)
	defer server.Close()

	job := mediaVideoCreateJob(t, server.URL, apiKey,
		`{"model":"cogvideox-3","prompt":"取消语义"}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaGlmVideoPollProcessing)})
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
	// 账户亲和面：取消未发任何上游请求（glm 无取消端点，本地收敛），记录层
	// 仅有创建一条。
	records := recorder.snapshot()
	if len(records) != 1 {
		t.Fatalf("glm 取消不得打上游（SupportsCancel=false 本地收敛）: %+v", records)
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

// waitGlmSpoolRecord 轮询等待 spool 落盘出现满足谓词的记录（smoke 测试
// 同款轮询等待）。
func waitGlmSpoolRecord(t *testing.T, spoolDir string, predicate func(string) bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, entry := range spoolDirectoryEntries(t, spoolDir) {
			if !entry.IsDir() {
				continue
			}
			for _, record := range spoolDirectoryEntries(t, filepath.Join(spoolDir, entry.Name())) {
				if record.IsDir() || !strings.HasSuffix(record.Name(), ".json") {
					continue
				}
				raw, err := os.ReadFile(filepath.Join(spoolDir, entry.Name(), record.Name()))
				if err != nil {
					t.Fatalf("读 spool 文件: %v", err)
				}
				if predicate(string(raw)) {
					return
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("usage spool 未出现 %s（entries=%d）", what, len(spoolDirectoryEntries(t, spoolDir)))
}
