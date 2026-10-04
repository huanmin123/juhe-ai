package main

// M3 gemini（Veo）视频任务链 chain 级集成测试（契约 §5.2；媒体设计 §4.2/
// §7/§8）：进程内 fixture（newChainFixture 先例）+ gemini 分组/账户 +
// mockupstream media_gemini_video_* 场景。覆盖三条链路：
//  1. 创建（predictLongRunning 改写 + X-Goog-Api-Key 认证 + params 回显）→
//     轮询 queued → completed（uri 冻结进 artifact）→ content 经绝对签名
//     URL 无凭据直连下载 mp4 + 终态 usage spool（usage_missing 口径，不虚计）；
//  2. done:true + error → failed 终态（error 摘要 + cost 0）；
//  3. DELETE 取消（:cancel 转发 → 上游 2xx → 本地 cancelled）。
//
// 上游拓扑：账户 base_url 指向包一层的手工记录服务器（断言 gemini 认证头），
// 内层为 mockupstream 引擎；completed 的产物 uri 由引擎按自身 URL 渲染，
// 网关直连该 URL（不经过记录层）——认证面断言据此二分：任务面请求带
// X-Goog-Api-Key，产物下载无任何凭据。
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

// geminiRecordedRequest 是记录层捕获的一条上游请求（方法/路径/认证面）。
type geminiRecordedRequest struct {
	Method        string
	Path          string
	XGoogAPIKey   string
	Authorization string
}

// geminiUpstreamRecorder 是账户 base_url 指向的记录层：逐请求记录 gemini
// 认证头并转发给 mockupstream 引擎（场景头由客户端显式携带或引擎默认）。
type geminiUpstreamRecorder struct {
	mu       sync.Mutex
	requests []geminiRecordedRequest
}

func (r *geminiUpstreamRecorder) record(method, path, xGoogAPIKey, authorization string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, geminiRecordedRequest{
		Method: method, Path: path, XGoogAPIKey: xGoogAPIKey, Authorization: authorization,
	})
}

func (r *geminiUpstreamRecorder) snapshot() []geminiRecordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]geminiRecordedRequest(nil), r.requests...)
}

// newGeminiRecordedUpstream 组装「记录层 + mockupstream 引擎」双层上游，
// 返回记录层、引擎与记录层地址（种账户 base_url 用）。
func newGeminiRecordedUpstream(t *testing.T) (*geminiUpstreamRecorder, *platformmock.Server, string) {
	t.Helper()
	mock := platformmock.New()
	recorder := &geminiUpstreamRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadGateway)
			return
		}
		_ = r.Body.Close()
		recorder.record(r.Method, r.URL.Path, r.Header.Get("X-Goog-Api-Key"), r.Header.Get("Authorization"))
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

// seedMediaGeminiVideoAccount 种 gemini 分组 + veo 测试账户 + 独立路由策略与
// API Key（不复用 fixture 的 openai 组，避免跨 provider 候选串扰），返回
// 网关 API Key 明文。凭据带 base_url 与 supported_endpoint_modes（gemini
// 词表：generate_content_json + video_*，opt-in 语义）。模型约束种
// veo-3.0-generate-preview。
func seedMediaGeminiVideoAccount(t *testing.T, fixture *chainFixture, id, baseURL, apiKey string) string {
	t.Helper()
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(query, args...); err != nil {
			t.Fatalf("seed gemini video row: %v: %v", query, err)
		}
	}
	keySecret := "sk-chain-gemini-video"
	credentials := map[string]any{
		"api_key":  apiKey,
		"base_url": baseURL,
		"supported_endpoint_modes": []string{
			"generate_content_json", "video_create", "video_get", "video_content", "video_cancel",
		},
	}
	seed(`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type)
		VALUES ('group_gem_video', ?, 'gemini', 1, 'personal')`, fixture.systemAccount)
	seed(`INSERT INTO accounts (
			id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
			name, type, status, schedulable, concurrency_limit, priority, credentials_encrypted, deleted_at
		) VALUES (?, ?, 'gemini', 'profile_gemini_native_v1beta', 'gemini', 'v1beta', ?, 'api_key', 'active', 1, 0, 0, ?, NULL)`,
		id, fixture.systemAccount, id, mustEncryptCredentials(t, credentials))
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
		VALUES ('group_gem_video', ?, ?, 1, '2026-10-04T00:00:00.000Z')`, fixture.systemAccount, id)
	seed(`INSERT INTO account_supported_models (account_id, provider_code, model, created_at)
		VALUES (?, 'gemini', 'veo-3.0-generate-preview', '2026-10-04T00:00:00.000Z')`, id)
	seed(`INSERT INTO route_strategies (id, system_account_id, name, mode, config_json, status)
		VALUES ('rs_gem_video', ?, 'gemini视频', 'normal', NULL, 'active')`, fixture.systemAccount)
	seed(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at)
		VALUES ('rsg_gem_video', 'rs_gem_video', ?, 'group_gem_video', 0, 1, 'active', '2026-10-04T00:00:00.000Z')`, fixture.systemAccount)
	seed(`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, status, created_at)
		VALUES ('key_gem_video', ?, 'rs_gem_video', 'gemini视频Key', ?, 'active', '2026-10-04T00:00:00.000Z')`,
		fixture.systemAccount, gatewayruntimecache.HashSecret(keySecret))
	return keySecret
}

// TestChainMediaGeminiVideoFullFlowLifecycle 覆盖链路 1：创建（predictLongRunning
// 改写 + gemini 认证头 + ignored 回显）→ 轮询 queued → completed（uri 进
// artifact）→ content 绝对 URL 无凭据直连 mp4 → 终态 usage（usage_missing，
// 契约 §2.8 不虚计）。
func TestChainMediaGeminiVideoFullFlowLifecycle(t *testing.T) {
	fixture := newChainFixture(t)
	recorder, mock, upstreamURL := newGeminiRecordedUpstream(t)
	apiKey := seedMediaGeminiVideoAccount(t, fixture, "acc_gem_video", upstreamURL, "gk-video-a")

	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain := composeMediaVideoChain(t, fixture, spoolDir)
	server := httptest.NewServer(chain)
	defer server.Close()

	job := mediaVideoCreateJob(t, server.URL, apiKey,
		`{"model":"veo-3.0-generate-preview","prompt":"一只猫在弹钢琴","size":"1280x720","negative_prompt":"模糊","seconds":"8"}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaGeminiVideoCreateOK)})
	jobID, _ := job["id"].(string)
	if jobID == "" || !strings.HasPrefix(jobID, "video_") {
		t.Fatalf("对外 job id 缺失或非 video_ 前缀: %#v", job)
	}
	if job["status"] != "queued" {
		t.Fatalf("创建响应 status = %v, want queued（operation 未 done 归一 queued）", job["status"])
	}
	if job["provider"] != "gemini" {
		t.Fatalf("provider = %v, want gemini", job["provider"])
	}
	providerJobID, _ := job["provider_job_id"].(string)
	if !strings.HasPrefix(providerJobID, "models/veo-3.0-generate-preview/operations/") {
		t.Fatalf("provider_job_id = %q, want operation name 形态", providerJobID)
	}
	applied := mediaVideoJobKeys(t, job, "params_applied")
	for _, want := range []string{"model", "prompt", "size", "negative_prompt"} {
		if !applied[want] {
			t.Fatalf("params_applied 缺少 %s: %v", want, job["params_applied"])
		}
	}
	if ignored := mediaVideoJobKeys(t, job, "params_ignored"); !ignored["seconds"] {
		t.Fatalf("params_ignored 缺少 seconds（veo 请求面无时长参数）: %v", job["params_ignored"])
	}

	// 创建出站断言：POST /v1beta/models/{model}:predictLongRunning + gemini
	// 认证头 + 转换报文（instances/parameters，ignored 键不进报文）。
	records := recorder.snapshot()
	if len(records) != 1 || records[0].Method != http.MethodPost ||
		records[0].Path != "/v1beta/models/veo-3.0-generate-preview:predictLongRunning" {
		t.Fatalf("创建出站请求形态错误: %+v", records)
	}
	if records[0].XGoogAPIKey != "gk-video-a" {
		t.Fatalf("创建请求 X-Goog-Api-Key = %q, want gk-video-a", records[0].XGoogAPIKey)
	}
	createBody := ""
	for _, request := range mock.Requests() {
		if request.Method == http.MethodPost && strings.HasSuffix(request.Path, ":predictLongRunning") {
			createBody = request.Body
		}
	}
	if createBody == "" {
		t.Fatal("引擎侧未录制创建请求")
	}
	if !strings.Contains(createBody, `"instances":[{"prompt":"一只猫在弹钢琴"}]`) ||
		!strings.Contains(createBody, `"aspectRatio":"16:9"`) || !strings.Contains(createBody, `"resolution":"720p"`) ||
		!strings.Contains(createBody, `"negativePrompt":"模糊"`) {
		t.Fatalf("创建报文转换错误: %s", createBody)
	}
	if strings.Contains(createBody, "seconds") {
		t.Fatalf("创建报文不得含 ignored 键 seconds: %s", createBody)
	}

	// 轮询两次：queued（done:false）→ completed（done:true + uri）。
	if first := mediaVideoPollOnce(t, server.URL, apiKey, jobID); first["status"] != "queued" {
		t.Fatalf("第一轮轮询 status = %v, want queued", first["status"])
	}
	if second := mediaVideoPollOnce(t, server.URL, apiKey, jobID); second["status"] != "completed" {
		t.Fatalf("第二轮轮询 status = %v, want completed: %v", second["status"], second)
	}
	// 任务面轮询经记录层：GET /v1beta/{name} + gemini 认证头。
	records = recorder.snapshot()
	if len(records) != 3 || records[1].Method != http.MethodGet || records[1].Path != "/v1beta/"+providerJobID ||
		records[2].Method != http.MethodGet || records[2].Path != "/v1beta/"+providerJobID {
		t.Fatalf("轮询出站请求形态错误: %+v", records)
	}
	for _, record := range records[1:] {
		if record.XGoogAPIKey != "gk-video-a" {
			t.Fatalf("轮询请求 X-Goog-Api-Key = %q", record.XGoogAPIKey)
		}
	}

	// media_jobs 行终态 completed + operation 名回填 + 无秒计量不虚计（0）。
	var status, upstreamJobID string
	var costUsd float64
	if err := fixture.db.QueryRow(`SELECT status, upstream_job_id, cost_usd FROM media_jobs WHERE id = ?`, jobID).Scan(&status, &upstreamJobID, &costUsd); err != nil {
		t.Fatalf("查询 media_jobs 行: %v", err)
	}
	if status != "completed" || upstreamJobID != providerJobID {
		t.Fatalf("media_jobs = %s/%s, want completed/%s", status, upstreamJobID, providerJobID)
	}
	if costUsd != 0 {
		t.Fatalf("veo 无输出秒回报（seconds 属 ignored），cost_usd = %v, want 0（usage_missing 不虚计）", costUsd)
	}

	// content 下载：completed 的 uri 是引擎渲染的绝对 URL（不经记录层），
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
		t.Fatalf("产物下载不得经账户 base_url（ContentFromArtifact 直连 uri）: %+v", recorder.snapshot())
	}
	for _, request := range mock.Requests() {
		if request.Path == "/v1beta/"+providerJobID+"/content" && request.AuthHeader != "" {
			t.Fatalf("产物直连不得携带 Authorization: %q", request.AuthHeader)
		}
	}

	// 终态 usage：spool 链异步落盘，completed 无秒计量 → usageMissing 标记
	//（契约 §2.8：上游不回报且请求参数非计量基源 → 0 计费 + usage_missing，
	// 不猜测）。
	waitGeminiSpoolRecord(t, spoolDir, func(text string) bool {
		return strings.Contains(text, `"usageMissing":true`) && strings.Contains(text, `"endpoint":"/v1/videos"`)
	}, "gemini completed usage_missing 终态记录")
}

// TestChainMediaGeminiVideoFailedTerminal 覆盖链路 2：done:true + error →
// failed 终态（error 摘要透传、cost 0、终态 usage 行 success=false）。
func TestChainMediaGeminiVideoFailedTerminal(t *testing.T) {
	fixture := newChainFixture(t)
	_, _, upstreamURL := newGeminiRecordedUpstream(t)
	apiKey := seedMediaGeminiVideoAccount(t, fixture, "acc_gem_video_fail", upstreamURL, "gk-video-fail")

	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain := composeMediaVideoChain(t, fixture, spoolDir)
	server := httptest.NewServer(chain)
	defer server.Close()

	job := mediaVideoCreateJob(t, server.URL, apiKey,
		`{"model":"veo-3.0-generate-preview","prompt":"失败终态","size":"720x1280"}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaGeminiVideoPollError)})
	jobID, _ := job["id"].(string)

	terminal := mediaVideoPollOnce(t, server.URL, apiKey, jobID)
	if terminal["status"] != "failed" {
		t.Fatalf("轮询 status = %v, want failed: %v", terminal["status"], terminal)
	}
	jobError, _ := terminal["error"].(map[string]any)
	if jobError == nil || jobError["code"] != "INTERNAL" {
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
	waitGeminiSpoolRecord(t, spoolDir, func(text string) bool {
		return strings.Contains(text, `"success":false`) && strings.Contains(text, `"endpoint":"/v1/videos"`)
	}, "gemini failed 终态记录")
}

// TestChainMediaGeminiVideoCancel 覆盖链路 3：DELETE → POST /v1beta/{name}:cancel
// 转发 → 上游 200 → 本地 cancelled + 204；终态行轮询回本地对象不再打上游。
func TestChainMediaGeminiVideoCancel(t *testing.T) {
	fixture := newChainFixture(t)
	recorder, _, upstreamURL := newGeminiRecordedUpstream(t)
	apiKey := seedMediaGeminiVideoAccount(t, fixture, "acc_gem_video_cancel", upstreamURL, "gk-video-cancel")

	chain := composeMediaVideoChain(t, fixture, filepath.Join(t.TempDir(), "spool"))
	server := httptest.NewServer(chain)
	defer server.Close()

	job := mediaVideoCreateJob(t, server.URL, apiKey,
		`{"model":"veo-3.0-generate-preview","prompt":"取消语义"}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaGeminiVideoPollRunning)})
	jobID, _ := job["id"].(string)
	providerJobID, _ := job["provider_job_id"].(string)

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
	records := recorder.snapshot()
	if len(records) != 2 || records[1].Method != http.MethodPost || records[1].Path != "/v1beta/"+providerJobID+":cancel" {
		t.Fatalf("取消出站请求形态错误: %+v", records)
	}

	// 终态行轮询回本地对象，不再打上游（记录数不变）。
	pollResponse, pollPayload := mediaVideoClientDo(t, server.URL, http.MethodGet, "/v1/videos/"+jobID, apiKey, "", nil)
	if pollResponse.StatusCode != http.StatusOK || mediaVideoDecodeJob(t, pollPayload)["status"] != "cancelled" {
		t.Fatalf("终态轮询应回本地 cancelled: status=%d body=%s", pollResponse.StatusCode, pollPayload)
	}
	if len(recorder.snapshot()) != 2 {
		t.Fatalf("终态轮询不得再打上游: %+v", recorder.snapshot())
	}
}

// mediaVideoPollOnce 轮询一次并解码 job 对象。
func mediaVideoPollOnce(t *testing.T, serverURL, apiKey, jobID string) map[string]any {
	t.Helper()
	response, payload := mediaVideoClientDo(t, serverURL, http.MethodGet, "/v1/videos/"+jobID, apiKey, "", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("poll %s status=%d body=%s", jobID, response.StatusCode, payload)
	}
	return mediaVideoDecodeJob(t, payload)
}

// mediaVideoJobKeys 把 job 对象的字符串数组字段投影为集合。
func mediaVideoJobKeys(t *testing.T, job map[string]any, field string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	values, _ := job[field].([]any)
	for _, value := range values {
		if text, ok := value.(string); ok {
			out[text] = true
		}
	}
	return out
}

// waitGeminiSpoolRecord 轮询等待 spool 落盘出现满足谓词的记录（smoke 测试
// 同款轮询等待）。
func waitGeminiSpoolRecord(t *testing.T, spoolDir string, predicate func(string) bool, what string) {
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
