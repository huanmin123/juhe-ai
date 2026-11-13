package main

// M2 视频任务链 chain 级集成测试（媒体设计 §4.2/§7/§8、契约 §4.3；计划
// T2 功能/边界项）：进程内 fixture（chain_test.go newChainFixture 先例）+
// mockupstream media_video_* 场景。覆盖六条链路：
//  1. 创建→轮询 3 次→completed→content 下载（mp4 magic bytes）+ 终态 usage
//     spool 回填 + 列表回显；
//  2. 创建 429 受理前换账户（两账户不同 mock key，受理边界 §7）；
//  3. 受理后轮询 500 只报错不换账户（断言请求只打原账户）；
//  4. 产物过期（上游 content 404 → media_artifact_expired，§8.1.5）；
//  5. DELETE 语义（上游 204 → 本地 cancelled；上游 404 同置 cancelled；
//     非本人任务 404）；
//  6. params_ignored 回显（negative_prompt/seed/audio 对 sora 忽略并回显，
//     契约 §2.4 规则 1；上游报文不含被忽略键）。
import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaymedia"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	platformmock "github.com/huanminabc/juhe-ai/backend-go-platform/mockupstream"
)

// composeMediaVideoChain 按 chainSmokeDeps 先例组装链，叠加媒体任务面依赖
// （业务库句柄 + 方言 + 凭据解密 secret，与 selector 同源）。
func composeMediaVideoChain(t *testing.T, fixture *chainFixture, spoolDir string) http.Handler {
	t.Helper()
	return composeMediaVideoChainWithPublicBase(t, fixture, spoolDir, "")
}

// composeMediaVideoChainWithPublicBase 是 composeMediaVideoChain 的公共地址
// 前缀变体（JUHE_AI_GATEWAY_PUBLIC_BASE_URL 链级验证：组合根 deps →
// mediaJobsRuntime.publicBaseURL → 查询响应回退 content 绝对地址）。
func composeMediaVideoChainWithPublicBase(t *testing.T, fixture *chainFixture, spoolDir, publicBaseURL string) http.Handler {
	t.Helper()
	deps := chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, spoolDir)
	deps.MediaJobsDB = fixture.db
	deps.MediaJobsSecret = "chain-test-secret"
	deps.MediaJobsPublicBaseURL = publicBaseURL
	chain, shutdown, err := composeGatewayChain(deps)
	if err != nil {
		t.Fatalf("compose gateway chain: %v", err)
	}
	t.Cleanup(shutdown)
	return chain
}

// seedMediaVideoAccount 种一个 openai 族视频测试账户（凭据带 base_url 与
// api_key；videoMode 控制显式 supported_endpoint_modes 是否含 video_create
// —— opt-in 语义与 images_json 同款）。模型约束种 sora-2。
func seedMediaVideoAccount(t *testing.T, db *sql.DB, fixture *chainFixture, id, baseURL, apiKey string, priority int, videoMode bool) {
	t.Helper()
	seedMediaVideoAccountWithVendor(t, db, fixture, id, baseURL, apiKey, priority, "gpt", videoMode)
}

// seedMediaVideoAccountWithVendor 是 seedMediaVideoAccount 的 vendor 泛化形
// （任务行 vendor 读面从 accounts.vendor_code 水合；xai 等直链厂商用例需要
// 非 gpt 账户行承接）。
func seedMediaVideoAccountWithVendor(t *testing.T, db *sql.DB, fixture *chainFixture, id, baseURL, apiKey string, priority int, vendor string, videoMode bool) {
	t.Helper()
	// v2 版本根语义：openai 族 base_url 携带 /v1，adapter opPath 为 /videos。
	baseURL = strings.TrimRight(baseURL, "/") + "/v1"
	modes := []string{"chat_json", "chat_sse", "responses_json", "responses_sse"}
	if videoMode {
		modes = append(modes, "video_create")
	}
	credentials := map[string]any{
		"api_key":                  apiKey,
		"base_url":                 baseURL,
		"supported_endpoint_modes": modes,
	}
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(fixtureBind(query), args...); err != nil {
			t.Fatalf("seed video account row: %v: %v", query, err)
		}
	}
	seed(`INSERT INTO accounts (
			id, system_account_id, vendor_code, provider_protocol_profile_id, protocol_code, protocol_version,
			name, type, status, schedulable, concurrency_limit, priority, credentials_encrypted, deleted_at
		) VALUES (?, ?, ?, 'prof_1', 'openai', 'v1', ?, 'api_key', 'active', 1, 0, ?, ?, NULL)`,
		id, fixture.systemAccount, vendor, id, priority, mustEncryptCredentials(t, credentials))
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at) VALUES (?, ?, ?, 1, ?)`,
		fixture.groupID, fixture.systemAccount, id, "2026-10-04T00:00:00.000Z")
	// B4 第一批目标形态：支持模型经 models 单表 + model_id 外键解析。
	seed(`INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, status, supported_endpoint_modes_json, created_at, updated_at)
		VALUES ('mdl_sora2', 'gpt', 'sora-2', 'sora-2', 'global', 'active', '["chat_json","chat_sse","responses_json","responses_sse","video_create"]', '2026-10-04T00:00:00.000Z', '2026-10-04T00:00:00.000Z') ON CONFLICT (id) DO NOTHING`)
	seed(`INSERT INTO account_supported_models (account_id, model, model_id, created_at) VALUES (?, 'sora-2', 'mdl_sora2', ?)`,
		id, "2026-10-04T00:00:00.000Z")
	// B4 第一批单连接事实源：BaseURL 与 mode 集改连接行（credentials.base_url 不再消费）。
	chainSeedAccountConnection(t, db, id, baseURL, modes...)
}

// mediaVideoClient 是带网关 API Key 的客户端请求构造。
func mediaVideoClientDo(t *testing.T, serverURL, method, path, apiKey string, body string, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, serverURL+path, reader)
	if err != nil {
		t.Fatalf("build request %s %s: %v", method, path, err)
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	payload, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil {
		t.Fatalf("read response %s %s: %v", method, path, readErr)
	}
	return response, payload
}

// mediaVideoCreateJob 创建一个视频任务并断言受理（2xx + 对外 job 对象）。
func mediaVideoCreateJob(t *testing.T, serverURL, apiKey, body string, headers map[string]string) map[string]any {
	t.Helper()
	response, payload := mediaVideoClientDo(t, serverURL, http.MethodPost, "/v1/video/generations", apiKey, body, headers)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("POST /v1/videos status=%d body=%s", response.StatusCode, payload)
	}
	var object map[string]any
	if err := json.Unmarshal(payload, &object); err != nil {
		t.Fatalf("decode job object: %v: %s", err, payload)
	}
	return object
}

func mediaVideoDecodeJob(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(payload, &object); err != nil {
		t.Fatalf("decode job object: %v: %s", err, payload)
	}
	return object
}

// mediaVideoMetadataOf 取 New API 创建/查询响应的 metadata 回显对象（诊断
// 字段 provider/provider_job_id/params_applied/params_ignored/
// provider_options_applied 的承载面）。
func mediaVideoMetadataOf(t *testing.T, object map[string]any) map[string]any {
	t.Helper()
	metadata, ok := object["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("响应缺少 metadata 对象: %#v", object)
	}
	return metadata
}

// mediaVideoTaskIDOf 取任务 id（创建响应 id/task_id 同值；查询响应只有
// task_id）。
func mediaVideoTaskIDOf(t *testing.T, object map[string]any) string {
	t.Helper()
	for _, key := range []string{"task_id", "id"} {
		if value, _ := object[key].(string); value != "" {
			return value
		}
	}
	t.Fatalf("响应缺少任务 id（task_id/id）: %#v", object)
	return ""
}

// TestChainMediaVideoFullFlowLifecycle 覆盖用例 1：创建→轮询 3 次→completed
// →content mp4 下载→usage spool 回填→列表回显（media_video_ok_poll3）。
func TestChainMediaVideoFullFlowLifecycle(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	seedMediaVideoAccount(t, fixture.db, fixture, "acc_video_a", mock.URL, "sk-video-a", 0, true)

	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain := composeMediaVideoChain(t, fixture, spoolDir)
	server := httptest.NewServer(chain)
	defer server.Close()

	job := mediaVideoCreateJob(t, server.URL, fixture.apiKeySecret,
		`{"model":"sora-2","prompt":"一只猫在弹钢琴","duration":"4","width":1280,"height":720}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaVideoOKPoll3)})
	jobID := mediaVideoTaskIDOf(t, job)
	if jobID == "" || !strings.HasPrefix(jobID, "video_") {
		t.Fatalf("对外 job id 缺失或非 video_ 前缀: %#v", job)
	}
	if job["status"] != "queued" || job["object"] != "video" {
		t.Fatalf("创建响应 status/object = %v/%v, want queued/video", job["status"], job["object"])
	}
	if _, ok := job["created_at"].(float64); !ok {
		t.Fatalf("创建响应缺少 created_at: %#v", job)
	}
	createMetadata := mediaVideoMetadataOf(t, job)
	if createMetadata["provider"] != "gpt" {
		t.Fatalf("metadata.provider = %v, want gpt", createMetadata["provider"])
	}
	if providerJobID, _ := createMetadata["provider_job_id"].(string); !strings.HasPrefix(providerJobID, "video_") {
		t.Fatalf("metadata.provider_job_id 缺失: %#v", createMetadata)
	} else if providerJobID == jobID {
		t.Fatalf("对外 id 不得等于上游 id: %s", jobID)
	}
	appliedText := fmt.Sprintf("%v", createMetadata["params_applied"])
	for _, want := range []string{"model", "prompt", "seconds", "size"} {
		if !strings.Contains(appliedText, want) {
			t.Fatalf("metadata.params_applied 缺少 %s: %v", want, createMetadata["params_applied"])
		}
	}

	// 轮询三次：in_progress(33) → in_progress(66) → completed(100)。
	progressByStatus := map[string]float64{}
	var payloadCompleted []byte
	for poll := 0; poll < 3; poll++ {
		response, payload := mediaVideoClientDo(t, server.URL, http.MethodGet, "/v1/video/generations/"+jobID, fixture.apiKeySecret, "", nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("poll #%d status=%d body=%s", poll+1, response.StatusCode, payload)
		}
		object := mediaVideoDecodeJob(t, payload)
		if object["task_id"] != jobID {
			t.Fatalf("poll task_id = %v, want %s", object["task_id"], jobID)
		}
		if object["code"] != "success" || object["message"] != "" {
			t.Fatalf("查询响应缺少 code/message 包裹: %#v", object)
		}
		if data, _ := object["data"].(map[string]any); data == nil || data["task_id"] != jobID {
			t.Fatalf("查询响应 data 包裹缺失或 task_id 不符: %#v", object)
		}
		status, _ := object["status"].(string)
		progress, _ := object["progress"].(float64)
		progressByStatus[status] = progress
		if status == "completed" {
			payloadCompleted = payload
		}
	}
	if progressByStatus["completed"] != 100 {
		t.Fatalf("第三次轮询未到 completed(100): %v", progressByStatus)
	}
	// completed 后查询响应的 url/format：gpt（Sora）无公开产物直链
	//（ContentFromArtifact=false），url 回退网关 content 端点相对路径（需
	// API Key）；format 按扩展名推断，content 形态无扩展名 → 缺省 mp4
	//（契约 §4.2 url/format 规则）。
	completed := mediaVideoDecodeJob(t, payloadCompleted)
	completedURL, _ := completed["url"].(string)
	if completedURL != "/v1/video/generations/"+jobID+"/content" || completed["format"] != "mp4" {
		t.Fatalf("completed url/format = %v/%v, want content 回退与 mp4", completed["url"], completed["format"])
	}

	// media_jobs 行终态 completed + 上游定位已回填。
	var status, upstreamJobID string
	var costUsd float64
	if err := fixture.db.QueryRow(`SELECT status, upstream_job_id, cost_usd FROM media_jobs WHERE id = ?`, jobID).Scan(&status, &upstreamJobID, &costUsd); err != nil {
		t.Fatalf("查询 media_jobs 行: %v", err)
	}
	if status != "completed" {
		t.Fatalf("media_jobs status = %s, want completed", status)
	}
	if providerJobID, _ := mediaVideoMetadataOf(t, job)["provider_job_id"].(string); upstreamJobID != providerJobID {
		t.Fatalf("media_jobs upstream_job_id = %s, want %s", upstreamJobID, providerJobID)
	}
	// 终态计费回填（媒体设计 §10）：sora-2 / sora-2-pro 已于 2026-09-24
	// 官方关停（目录行保留 ShutdownDate），运行时目录查找未命中 → cost_usd
	// 落 0（usage_missing 语义，不虚计）；秒计量列不受影响照抽。
	if costUsd != 0 {
		t.Fatalf("media_jobs cost_usd = %v, want 0（sora-2 已 shutdown，目录未命中不虚计）", costUsd)
	}

	// content 下载：流式转发 mp4（ftyp magic bytes + video/mp4）。
	response, payload := mediaVideoClientDo(t, server.URL, http.MethodGet, "/v1/video/generations/"+jobID+"/content", fixture.apiKeySecret, "", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("content status=%d body=%s", response.StatusCode, payload)
	}
	if got := response.Header.Get("Content-Type"); !strings.Contains(got, "video/mp4") {
		t.Fatalf("content-type = %q, want video/mp4", got)
	}
	if len(payload) < 12 || string(payload[4:8]) != "ftyp" {
		t.Fatalf("content 不是 mp4 载荷（magic bytes 缺失）: % x", payload[:min(12, len(payload))])
	}

	// New API 无列表面：GET 集合恒 404（不读本地行、不返回 list envelope）。
	listResponse, listPayload := mediaVideoClientDo(t, server.URL, http.MethodGet, "/v1/video/generations", fixture.apiKeySecret, "", nil)
	if listResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /v1/video/generations status=%d body=%s, want 404", listResponse.StatusCode, listPayload)
	}

	// 终态 usage 回填：spool 链异步落盘（smoke 测试同款轮询等待），断言
	// outputVideoSeconds=4（契约 §2.8 秒数来自轮询响应 seconds_length）。
	// 终态 usage 回填：spool 链异步落盘（smoke 测试同款轮询等待；记录在
	// spool 根的实例子目录 gateway-chain/*.json），断言 outputVideoSeconds=4
	//（契约 §2.8 秒数来自轮询响应 seconds_length）与 endpoint 归属。
	deadline := time.Now().Add(5 * time.Second)
	found := false
	for time.Now().Before(deadline) && !found {
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
				if strings.Contains(string(raw), `"outputVideoSeconds":4`) && strings.Contains(string(raw), `"endpoint":"/v1/video/generations"`) {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			time.Sleep(25 * time.Millisecond)
		}
	}
	if !found {
		entries := spoolDirectoryEntries(t, spoolDir)
		t.Fatalf("usage spool 未出现视频终态记录（entries=%d）", len(entries))
	}
}

// TestChainMediaVideoCreate429SwitchesAccount 覆盖用例 2：创建 429（受理前）
// 换账户重建（media_video_429_create → 第二账户 ok 脚本成功）。
func TestChainMediaVideoCreate429SwitchesAccount(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	// 第一个上游恒注入 429 场景（受理前失败，§7 可切换）；第二个为默认
	// ok_poll3 脚本。两账户不同 mock key。
	mock429Inner := platformmock.New()
	defer mock429Inner.Close()
	mock429 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("X-Mock-Scenario", string(platformmock.ScenarioMediaVideo429Create))
		mock429Inner.Config.Handler.ServeHTTP(w, r)
	}))
	defer mock429.Close()
	// priority 0 先派发（候选排序 ascending），429 后换第二账户。
	seedMediaVideoAccount(t, fixture.db, fixture, "acc_video_429", mock429.URL, "sk-video-429", 0, true)
	seedMediaVideoAccount(t, fixture.db, fixture, "acc_video_ok", mock.URL, "sk-video-ok", 10, true)

	chain := composeMediaVideoChain(t, fixture, filepath.Join(t.TempDir(), "spool"))
	server := httptest.NewServer(chain)
	defer server.Close()

	job := mediaVideoCreateJob(t, server.URL, fixture.apiKeySecret,
		`{"model":"sora-2","prompt":"换账户重建","seconds":"8","size":"720x1280"}`, nil)
	if job["status"] != "queued" {
		t.Fatalf("换账户后创建 status = %v, want queued", job["status"])
	}
	// 受理边界证据：第一账户恰好一次 429 创建，第二账户恰好一次成功创建。
	requests429 := mock429Inner.Requests()
	if len(requests429) != 1 || requests429[0].Method != http.MethodPost || requests429[0].Path != "/v1/videos" {
		t.Fatalf("429 上游请求数/形态错误: %#v", requests429)
	}
	if requests429[0].AuthHeader != "Bearer sk-video-429" {
		t.Fatalf("429 请求认证头 = %q", requests429[0].AuthHeader)
	}
	okCreates := 0
	for _, request := range mock.Requests() {
		if request.Method == http.MethodPost && request.Path == "/v1/videos" {
			okCreates++
			if request.AuthHeader != "Bearer sk-video-ok" {
				t.Fatalf("成功创建认证头 = %q, want sk-video-ok", request.AuthHeader)
			}
		}
	}
	if okCreates != 1 {
		t.Fatalf("第二账户创建请求数 = %d, want 1", okCreates)
	}
	// 落表亲和第二账户。
	var accountID string
	if err := fixture.db.QueryRow(`SELECT account_id FROM media_jobs WHERE id = ?`, job["id"]).Scan(&accountID); err != nil {
		t.Fatalf("查询 media_jobs: %v", err)
	}
	if accountID != "acc_video_ok" {
		t.Fatalf("media_jobs account_id = %s, want acc_video_ok", accountID)
	}
}

// TestChainMediaVideoPoll500KeepsAccount 覆盖用例 3：受理后轮询 5xx 只报错，
// 不换账户（media_video_poll_500；断言全部上游请求只打原账户）。
func TestChainMediaVideoPoll500KeepsAccount(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	// 候选一：poll_500 脚本账户（创建成功、轮询恒 500）。候选二：无
	// video_create 端点模式——创建候选过滤后只剩候选一（opt-in 语义），
	// 若任务面错误换账户则必然打到候选二，断言可证伪。
	seedMediaVideoAccount(t, fixture.db, fixture, "acc_video_poll500", mock.URL, "sk-video-poll500", 0, true)
	seedMediaVideoAccount(t, fixture.db, fixture, "acc_video_nomode", mock.URL, "sk-video-nomode", 10, false)

	chain := composeMediaVideoChain(t, fixture, filepath.Join(t.TempDir(), "spool"))
	server := httptest.NewServer(chain)
	defer server.Close()

	job := mediaVideoCreateJob(t, server.URL, fixture.apiKeySecret,
		`{"model":"sora-2","prompt":"轮询故障不换账户","seconds":"4","size":"1280x720"}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaVideoPoll500)})
	jobID, _ := job["id"].(string)

	response, payload := mediaVideoClientDo(t, server.URL, http.MethodGet, "/v1/video/generations/"+jobID, fixture.apiKeySecret, "", nil)
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("poll 500 应向客户端报错（502），实际 status=%d body=%s", response.StatusCode, payload)
	}
	if !strings.Contains(string(payload), "upstream_error") {
		t.Fatalf("poll 500 错误码缺失: %s", payload)
	}
	// 本地行保持非终态（不得伪造终态，契约 §2.6）。
	var status string
	if err := fixture.db.QueryRow(`SELECT status FROM media_jobs WHERE id = ?`, jobID).Scan(&status); err != nil {
		t.Fatalf("查询 media_jobs: %v", err)
	}
	if status != "queued" {
		t.Fatalf("poll 500 后本地 status = %s, want queued（不伪造推进）", status)
	}
	// 上游请求全部只打原账户（创建 1 + 轮询 1，均 sk-video-poll500）。
	for _, request := range mock.Requests() {
		if request.AuthHeader != "Bearer sk-video-poll500" {
			t.Fatalf("上游请求打到其他账户: path=%s auth=%q", request.Path, request.AuthHeader)
		}
	}
	creates, polls := 0, 0
	for _, request := range mock.Requests() {
		switch {
		case request.Method == http.MethodPost && request.Path == "/v1/videos":
			creates++
		case request.Method == http.MethodGet && strings.HasPrefix(request.Path, "/v1/videos/"):
			polls++
		}
	}
	if creates != 1 || polls != 1 {
		t.Fatalf("上游请求 creates=%d polls=%d, want 1/1", creates, polls)
	}
}

// TestChainMediaVideoFailedTerminalZeroCost 终态计费回填断言（媒体设计 §10
// "失败任务不虚计"）：受理后轮询推进到 failed（media_video_fail_after_accept
// 第 2 轮），cost_usd 落 0，终态 usage 行 success=false 且不携带秒计量/CostUsd。
func TestChainMediaVideoFailedTerminalZeroCost(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	seedMediaVideoAccount(t, fixture.db, fixture, "acc_video_fail", mock.URL, "sk-video-fail", 0, true)

	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain := composeMediaVideoChain(t, fixture, spoolDir)
	server := httptest.NewServer(chain)
	defer server.Close()

	job := mediaVideoCreateJob(t, server.URL, fixture.apiKeySecret,
		`{"model":"sora-2","prompt":"失败任务不计费","seconds":"4"}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaVideoFailAfterAccept)})
	jobID, _ := job["id"].(string)

	// 第 1 轮 in_progress(50)，第 2 轮 failed（终态 + error）。
	second := map[string]any{}
	for poll := 0; poll < 2; poll++ {
		response, payload := mediaVideoClientDo(t, server.URL, http.MethodGet, "/v1/video/generations/"+jobID, fixture.apiKeySecret, "", nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("poll #%d status=%d body=%s", poll+1, response.StatusCode, payload)
		}
		second = mediaVideoDecodeJob(t, payload)
	}
	if second["status"] != "failed" {
		t.Fatalf("第二轮轮询 status = %v, want failed", second["status"])
	}
	var status string
	var costUsd float64
	if err := fixture.db.QueryRow(`SELECT status, cost_usd FROM media_jobs WHERE id = ?`, jobID).Scan(&status, &costUsd); err != nil {
		t.Fatalf("查询 media_jobs 行: %v", err)
	}
	if status != "failed" {
		t.Fatalf("media_jobs status = %s, want failed", status)
	}
	if costUsd != 0 {
		t.Fatalf("失败任务 cost_usd = %v, want 0（不虚计）", costUsd)
	}
	// 终态 usage 行：success=false，不带 outputVideoSeconds，也不带 costUsd
	//（估算不出的成本保持 NULL 语义，与完成尝试记录一致）。
	deadline := time.Now().Add(5 * time.Second)
	found := false
	for time.Now().Before(deadline) && !found {
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
				text := string(raw)
				if strings.Contains(text, `"success":false`) && strings.Contains(text, `"endpoint":"/v1/video/generations"`) {
					found = true
					if strings.Contains(text, "outputVideoSeconds") {
						t.Fatalf("失败任务 usage 行不得带秒计量: %s", text)
					}
					if strings.Contains(text, `"costUsd":`) {
						t.Fatalf("失败任务 usage 行不得带 costUsd: %s", text)
					}
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			time.Sleep(25 * time.Millisecond)
		}
	}
	if !found {
		t.Fatal("usage spool 未出现失败任务终态记录")
	}
}

// TestChainMediaVideoContentExpired 覆盖用例 4：产物过期（media_video_
// content_expired：completed 但 content 404 → media_artifact_expired）。
func TestChainMediaVideoContentExpired(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	seedMediaVideoAccount(t, fixture.db, fixture, "acc_video_a", mock.URL, "sk-video-a", 0, true)

	chain := composeMediaVideoChain(t, fixture, filepath.Join(t.TempDir(), "spool"))
	server := httptest.NewServer(chain)
	defer server.Close()

	job := mediaVideoCreateJob(t, server.URL, fixture.apiKeySecret,
		`{"model":"sora-2","prompt":"产物时效","seconds":"4"}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaVideoContentExpired)})
	jobID, _ := job["id"].(string)
	// 第一次轮询即 completed（content_expired 脚本 fast path）。
	pollResponse, pollPayload := mediaVideoClientDo(t, server.URL, http.MethodGet, "/v1/video/generations/"+jobID, fixture.apiKeySecret, "", nil)
	if pollResponse.StatusCode != http.StatusOK || mediaVideoDecodeJob(t, pollPayload)["status"] != "completed" {
		t.Fatalf("poll 未到 completed: status=%d body=%s", pollResponse.StatusCode, pollPayload)
	}
	// content：上游 404 → 明确"产物已过期"错误码。
	response, payload := mediaVideoClientDo(t, server.URL, http.MethodGet, "/v1/video/generations/"+jobID+"/content", fixture.apiKeySecret, "", nil)
	if response.StatusCode != http.StatusGone {
		t.Fatalf("content 过期 status=%d, want 410", response.StatusCode)
	}
	if !strings.Contains(string(payload), "media_artifact_expired") {
		t.Fatalf("content 过期错误码缺失: %s", payload)
	}
}

// TestChainMediaVideoCancelAndList404 覆盖 New API 任务面裁剪（契约 §4.2：
// 列表 / 取消 / 删除不提供）：DELETE /v1/video/generations/{id} 与 GET 集合
// 恒 404，不读本地行、不打上游（任务行状态不变）；content 端点保留。
func TestChainMediaVideoCancelAndList404(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	seedMediaVideoAccount(t, fixture.db, fixture, "acc_video_a", mock.URL, "sk-video-a", 0, true)

	chain := composeMediaVideoChain(t, fixture, filepath.Join(t.TempDir(), "spool"))
	server := httptest.NewServer(chain)
	defer server.Close()

	job := mediaVideoCreateJob(t, server.URL, fixture.apiKeySecret,
		`{"model":"sora-2","prompt":"取消面已裁剪"}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaVideoCancelOK)})
	jobID := mediaVideoTaskIDOf(t, job)
	upstreamRequests := len(mock.Requests())

	// DELETE 恒 404：本地行保持 queued，且不发上游取消请求。
	response, payload := mediaVideoClientDo(t, server.URL, http.MethodDelete, "/v1/video/generations/"+jobID, fixture.apiKeySecret, "", nil)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("DELETE status=%d body=%s, want 404", response.StatusCode, payload)
	}
	var status string
	if err := fixture.db.QueryRow(`SELECT status FROM media_jobs WHERE id = ?`, jobID).Scan(&status); err != nil {
		t.Fatalf("查询 media_jobs: %v", err)
	}
	if status != "queued" {
		t.Fatalf("DELETE 后本地 status = %s, want queued（不提供取消面）", status)
	}
	if len(mock.Requests()) != upstreamRequests {
		t.Fatalf("DELETE 不得打上游（%d → %d）", upstreamRequests, len(mock.Requests()))
	}

	// 集合 GET 恒 404（New API 无列表面）。
	response, payload = mediaVideoClientDo(t, server.URL, http.MethodGet, "/v1/video/generations", fixture.apiKeySecret, "", nil)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("GET collection status=%d body=%s, want 404", response.StatusCode, payload)
	}
	// 轮询仍可用（任务面唯一保留的读取入口）。
	response, payload = mediaVideoClientDo(t, server.URL, http.MethodGet, "/v1/video/generations/"+jobID, fixture.apiKeySecret, "", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("poll status=%d body=%s, want 200", response.StatusCode, payload)
	}
}

// TestChainMediaVideoLegacyPaths404 覆盖旧 /v1/videos 全面硬 404（不保留别名）：
// 创建/轮询/content/集合/取消五个旧形态都不再注册路由；body 不进入派发。
func TestChainMediaVideoLegacyPaths404(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	seedMediaVideoAccount(t, fixture.db, fixture, "acc_video_a", mock.URL, "sk-video-a", 0, true)

	chain := composeMediaVideoChain(t, fixture, filepath.Join(t.TempDir(), "spool"))
	server := httptest.NewServer(chain)
	defer server.Close()

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/v1/videos"},
		{http.MethodGet, "/v1/videos"},
		{http.MethodGet, "/v1/videos/video_legacy"},
		{http.MethodGet, "/v1/videos/video_legacy/content"},
		{http.MethodDelete, "/v1/videos/video_legacy"},
	}
	for _, tc := range cases {
		body := ""
		if tc.method == http.MethodPost {
			body = `{"model":"sora-2","prompt":"旧路径"}`
		}
		response, payload := mediaVideoClientDo(t, server.URL, tc.method, tc.path, fixture.apiKeySecret, body, nil)
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("%s %s status=%d body=%s, want 404", tc.method, tc.path, response.StatusCode, payload)
		}
		// 404 形体钉扎（writeLegacyGatewayPathNotFound）：OpenAI error 信封。
		var errBody struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
				Param   string `json:"param"`
				Code    string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(payload, &errBody); err != nil {
			t.Fatalf("%s %s 404 body 非 JSON: %s", tc.method, tc.path, payload)
		}
		if errBody.Error.Code != "not_found" || errBody.Error.Type != "invalid_request_error" || errBody.Error.Message == "" || errBody.Error.Param != "" {
			t.Fatalf("%s %s 404 error 信封不符: %s", tc.method, tc.path, payload)
		}
	}
	if requests := mock.Requests(); len(requests) != 0 {
		t.Fatalf("旧路径 404 不得进入派发（上游请求 %#v）", requests)
	}
}

// TestChainMediaVideoParamsIgnoredEcho 覆盖用例 6：params_ignored 回显
// （metadata 的 negative_prompt/seed/audio 提升为公共参数后对 sora 忽略并
// 回显，契约 §2.4 规则 1；metadata 原样回显；上游报文不含被忽略键——不静默
// 透传）。
func TestChainMediaVideoParamsIgnoredEcho(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	seedMediaVideoAccount(t, fixture.db, fixture, "acc_video_a", mock.URL, "sk-video-a", 0, true)

	chain := composeMediaVideoChain(t, fixture, filepath.Join(t.TempDir(), "spool"))
	server := httptest.NewServer(chain)
	defer server.Close()

	job := mediaVideoCreateJob(t, server.URL, fixture.apiKeySecret,
		`{"model":"sora-2","prompt":"回显被忽略参数","duration":4,"metadata":{"negative_prompt":"模糊","seed":42,"audio":true}}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaVideoOKPoll1)})
	metadata := mediaVideoMetadataOf(t, job)
	ignored := fmt.Sprintf("%v", metadata["params_ignored"])
	for _, want := range []string{"negative_prompt", "seed", "audio"} {
		if !strings.Contains(ignored, want) {
			t.Fatalf("params_ignored 缺少 %s: %v", want, metadata["params_ignored"])
		}
	}
	if strings.Contains(fmt.Sprintf("%v", metadata["params_applied"]), "negative_prompt") {
		t.Fatalf("negative_prompt 不得进 params_applied: %v", metadata["params_applied"])
	}
	// metadata 原样回显（请求值 + 诊断键共存）。
	if metadata["negative_prompt"] != "模糊" || metadata["seed"] != float64(42) || metadata["audio"] != true {
		t.Fatalf("metadata 未原样回显请求值: %#v", metadata)
	}
	// 上游报文不含被忽略键（忽略即不进报文，契约 §2.4 规则 1）。
	for _, request := range mock.Requests() {
		if request.Method == http.MethodPost && request.Path == "/v1/videos" {
			for _, banned := range []string{"negative_prompt", "\"seed\"", "\"audio\""} {
				if strings.Contains(request.Body, banned) {
					t.Fatalf("上游创建报文不得含被忽略键 %s: %s", banned, request.Body)
				}
			}
			if !strings.Contains(request.Body, `"prompt":"回显被忽略参数"`) {
				t.Fatalf("上游创建报文缺少 prompt: %s", request.Body)
			}
		}
	}
}

// TestChainMediaVideoMetadataProviderOptionsEcho 覆盖 metadata 唯一扩展通道
// （契约 §2.2/§2.4 规则 4）：metadata 其余键在选择账户后 deep-merge 进该厂商
// provider_options 子对象；响应 metadata.provider_options_applied 回显命中的
// 键名摘要（不含值）；轮询回显同一摘要（request_snapshot 冻结）。公开
// provider_options 出现即 400（旧通道已退役）。
func TestChainMediaVideoMetadataProviderOptionsEcho(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	seedMediaVideoAccount(t, fixture.db, fixture, "acc_video_popts", mock.URL, "sk-video-popts", 0, true)

	chain := composeMediaVideoChain(t, fixture, filepath.Join(t.TempDir(), "spool"))
	server := httptest.NewServer(chain)
	defer server.Close()

	job := mediaVideoCreateJob(t, server.URL, fixture.apiKeySecret,
		`{"model":"sora-2","prompt":"L3 回显","duration":4,"metadata":{"vendor_flag":true,"nested":{"k":1}}}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaVideoOKPoll3)})
	metadata := mediaVideoMetadataOf(t, job)
	applied := fmt.Sprintf("%v", metadata["provider_options_applied"])
	if !strings.Contains(applied, "vendor_flag") || !strings.Contains(applied, "nested") {
		t.Fatalf("provider_options_applied 缺少命中键名: %v", applied)
	}
	if strings.Contains(applied, "true") {
		t.Fatalf("provider_options_applied 不得含值: %v", applied)
	}
	// 上游报文：metadata 其余键 deep-merge 进创建体。
	for _, request := range mock.Requests() {
		if request.Method == http.MethodPost && request.Path == "/v1/videos" {
			if !strings.Contains(request.Body, `"vendor_flag":true`) || !strings.Contains(request.Body, `"nested":{"k":1}`) {
				t.Fatalf("上游创建报文缺少 metadata 扩展键合并: %s", request.Body)
			}
		}
	}
	// 轮询回显同一摘要（request_snapshot 冻结，创建后原始请求不可得）。
	jobID := mediaVideoTaskIDOf(t, job)
	response, payload := mediaVideoClientDo(t, server.URL, http.MethodGet, "/v1/video/generations/"+jobID, fixture.apiKeySecret, "", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("poll status=%d body=%s", response.StatusCode, payload)
	}
	polled := fmt.Sprintf("%v", mediaVideoMetadataOf(t, mediaVideoDecodeJob(t, payload))["provider_options_applied"])
	if !strings.Contains(polled, "vendor_flag") {
		t.Fatalf("轮询回显 provider_options_applied 缺失: %s", payload)
	}

	// 公开 provider_options 出现即 400（唯一扩展通道是 metadata）。
	response, payload = mediaVideoClientDo(t, server.URL, http.MethodPost, "/v1/video/generations", fixture.apiKeySecret,
		`{"model":"sora-2","prompt":"旧通道","provider_options":{"openai":{"vendor_flag":true}}}`, nil)
	if response.StatusCode != http.StatusBadRequest || !strings.Contains(string(payload), "metadata") {
		t.Fatalf("provider_options 应 400 并提示 metadata，实际 status=%d body=%s", response.StatusCode, payload)
	}
}

// TestChainMediaJobRepoRoundTrip 是仓储直连单测（插入/推进/终态回填/归属查询/
// TTL 查询），钉住 JSON 列编解码与 ErrMediaJobNotFound 语义。
func TestChainMediaJobRepoRoundTrip(t *testing.T) {
	// newChainFixturePGDatabase 同款惯例（原 sqlite 文件夹具随存储形态退役重
	// 钉 PG）：sqldialect 包装连接承担生产同款 `?`→$n 改写，search_path 让
	// 夹具裸表 DDL 落在生产读取 schema。
	db := newChainFixturePGDatabase(t)
	if _, err := db.Exec(`CREATE TABLE media_jobs (
		id TEXT PRIMARY KEY, kind TEXT NOT NULL, api_key_id TEXT NOT NULL, owner_system_account_id TEXT NOT NULL DEFAULT '', account_id TEXT NOT NULL,
		upstream_job_id TEXT NOT NULL,
		status TEXT NOT NULL, request_snapshot_json TEXT NOT NULL DEFAULT '{}', artifact_json TEXT NOT NULL DEFAULT '{}',
		error_json TEXT NOT NULL DEFAULT '{}', usage_json TEXT NOT NULL DEFAULT '{}', cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
		params_applied_json TEXT NOT NULL DEFAULT '[]', params_ignored_json TEXT NOT NULL DEFAULT '[]',
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("create media_jobs: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE accounts (id TEXT PRIMARY KEY, vendor_code TEXT)`); err != nil {
		t.Fatalf("create accounts: %v", err)
	}
	repo := gatewaymedia.NewMediaJobsRepo(db, time.Now)
	ctx := context.Background()
	seconds := 4.0
	if err := repo.Insert(ctx, mediaJobRecordOf("job_1", "key_1", "acc_1", seconds)); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if affected, err := repo.UpdateStatus(ctx, "job_1", gatewaymedia.JobStatusInProgress); err != nil || affected != 1 {
		t.Fatalf("update status: affected=%d err=%v, want 1/nil", affected, err)
	}
	secondsOut := 4.0
	if affected, err := repo.UpdateTerminal(ctx, "job_1", gatewaymedia.JobStatusCompleted, nil,
		gatewaymedia.MediaJobArtifact{ContentURL: "https://up.invalid/content"},
		gatewaymedia.MediaJobUsage{OutputVideoSeconds: &secondsOut}, 0.4); err != nil || affected != 1 {
		t.Fatalf("update terminal: affected=%d err=%v, want 1/nil", affected, err)
	}
	loaded, err := repo.GetByIDAndAPIKey(ctx, "job_1", "key_1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if loaded.Status != gatewaymedia.JobStatusCompleted || loaded.UpstreamJobID != "up_1" {
		t.Fatalf("loaded = %#v", loaded)
	}
	if loaded.CostUsd != 0.4 {
		t.Fatalf("cost_usd = %v, want 0.4", loaded.CostUsd)
	}
	if loaded.Usage.OutputVideoSeconds == nil || *loaded.Usage.OutputVideoSeconds != 4 {
		t.Fatalf("usage seconds = %v, want 4", loaded.Usage.OutputVideoSeconds)
	}
	if loaded.Artifact.ContentURL != "https://up.invalid/content" {
		t.Fatalf("artifact = %#v", loaded.Artifact)
	}
	if len(loaded.ParamsIgnored) != 1 || loaded.ParamsIgnored[0] != "negative_prompt" {
		t.Fatalf("params_ignored = %v", loaded.ParamsIgnored)
	}
	// 归属校验：非本人任务未命中。
	if _, err := repo.GetByIDAndAPIKey(ctx, "job_1", "key_other"); err != gatewaymedia.ErrMediaJobNotFound {
		t.Fatalf("other api key err = %v, want ErrMediaJobNotFound", err)
	}
	// TTL 查询：created_at 早于 before 的行命中；before 早于创建时刻则不命中。
	expired, err := repo.ListExpired(ctx, time.Now().Add(gatewaymedia.MediaJobRetentionTTL), 10)
	if err != nil {
		t.Fatalf("list expired: %v", err)
	}
	if len(expired) != 1 || expired[0].ID != "job_1" {
		t.Fatalf("expired = %#v", expired)
	}
	fresh, err := repo.ListExpired(ctx, time.Now().Add(-time.Minute), 10)
	if err != nil {
		t.Fatalf("list fresh: %v", err)
	}
	if len(fresh) != 0 {
		t.Fatalf("fresh = %#v, want 空", fresh)
	}
	// 分页列表按归属 Key。
	listed, err := repo.ListByAPIKey(ctx, "key_1", 10, 0)
	if err != nil || len(listed) != 1 {
		t.Fatalf("list by key: %v %d", err, len(listed))
	}
}

// mediaJobRecordOf 构造一行测试任务。
func mediaJobRecordOf(id, apiKeyID, accountID string, seconds float64) gatewaymedia.MediaJobRecord {
	return gatewaymedia.MediaJobRecord{
		ID:            id,
		Kind:          gatewaymedia.JobKindVideo,
		APIKeyID:      apiKeyID,
		AccountID:     accountID,
		UpstreamJobID: "up_1",
		Status:        gatewaymedia.JobStatusQueued,
		RequestSnapshot: gatewaymedia.MediaJobRequestSnapshot{
			Model: "sora-2", Prompt: "repo", Seconds: &seconds, Size: "1280x720", N: 1,
		},
		ParamsIgnored: []string{"negative_prompt"},
	}
}

// TestChainMediaVideoCreate400BadSizeKeepsAccount 覆盖媒体设计 §7 创建行的
// 参数类 4xx 语义（M2 复审 MAJOR-A）：上游 400（media_video_create_400_
// bad_size，size 形态合法但值域外——本地 GatewayRequestValidationError 不
// 触发）确定性参数错误 → 短路透传上游 400（invalid_size），不换账户（B 零
// 创建请求）、不落 media_jobs 行；对照 429 用例的可切换语义。
func TestChainMediaVideoCreate400BadSizeKeepsAccount(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	// 第一上游恒注入 400 参数错误场景；第二上游默认 ok 脚本（若短路缺失，
	// A 的 400 会像 429 一样换 B 重建，断言 B 零创建可证伪）。
	mock400Inner := platformmock.New()
	defer mock400Inner.Close()
	mock400 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("X-Mock-Scenario", string(platformmock.ScenarioMediaVideoCreate400BadSize))
		mock400Inner.Config.Handler.ServeHTTP(w, r)
	}))
	defer mock400.Close()
	seedMediaVideoAccount(t, fixture.db, fixture, "acc_video_400", mock400.URL, "sk-video-400", 0, true)
	seedMediaVideoAccount(t, fixture.db, fixture, "acc_video_ok400", mock.URL, "sk-video-ok400", 10, true)

	chain := composeMediaVideoChain(t, fixture, filepath.Join(t.TempDir(), "spool"))
	server := httptest.NewServer(chain)
	defer server.Close()

	// width+height 归一为 size=9999x9999（形态合法、值域由上游裁决）→ 上游
	// 400 透传客户端。
	response, payload := mediaVideoClientDo(t, server.URL, http.MethodPost, "/v1/video/generations", fixture.apiKeySecret,
		`{"model":"sora-2","prompt":"参数错误不换账户","duration":4,"width":9999,"height":9999}`, nil)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("create status=%d body=%s, want 400（上游参数错误透传客户端）", response.StatusCode, payload)
	}
	if !strings.Contains(string(payload), "invalid_size") {
		t.Fatalf("400 响应缺少上游 invalid_size 错误码: %s", payload)
	}
	// 只打一个账户：A 恰好一次创建；B 零创建（确定性错误不换账户）。
	if requests := mock400Inner.Requests(); len(requests) != 1 || requests[0].Method != http.MethodPost || requests[0].Path != "/v1/videos" {
		t.Fatalf("A 上游请求数/形态错误: %#v", requests)
	}
	for _, request := range mock.Requests() {
		if request.Method == http.MethodPost && request.Path == "/v1/videos" {
			t.Fatalf("不得换账户重建：B 收到创建请求 %#v", request)
		}
	}
	// 受理凭据未确立，不得落任务行。
	var count int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM media_jobs`).Scan(&count); err != nil {
		t.Fatalf("count media_jobs: %v", err)
	}
	if count != 0 {
		t.Fatalf("参数错误 400 不得落 media_jobs 行: %d", count)
	}
}

// TestChainMediaJobTerminalUpdateRaceOnce 是 MAJOR-B 仓储级并发回归：同 job
// 两 goroutine 并发终态回填，条件 UPDATE（非终态守卫）保证恰好一个写入
// 生效（affected==1）；胜出后的取消写入被终态守卫拒绝（0 行，不回退）。
// TestChainMediaJobTerminalUpdateRaceOnce 是 MAJOR-B 仓储级并发回归：同 job
// 两 goroutine 并发终态回填，条件 UPDATE（非终态守卫）保证恰好一个写入
// 生效（affected==1）；胜出后的取消写入被终态守卫拒绝（0 行，不回退）。
// PG 连接池真实并发，胜负裁决完全由 WHERE 守卫承担。
func TestChainMediaJobTerminalUpdateRaceOnce(t *testing.T) {
	db := newChainFixturePGDatabase(t)
	if _, err := db.Exec(`CREATE TABLE media_jobs (
		id TEXT PRIMARY KEY, kind TEXT NOT NULL, api_key_id TEXT NOT NULL, owner_system_account_id TEXT NOT NULL DEFAULT '', account_id TEXT NOT NULL,
		upstream_job_id TEXT NOT NULL,
		status TEXT NOT NULL, request_snapshot_json TEXT NOT NULL DEFAULT '{}', artifact_json TEXT NOT NULL DEFAULT '{}',
		error_json TEXT NOT NULL DEFAULT '{}', usage_json TEXT NOT NULL DEFAULT '{}', cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
		params_applied_json TEXT NOT NULL DEFAULT '[]', params_ignored_json TEXT NOT NULL DEFAULT '[]',
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("create media_jobs: %v", err)
	}
	repo := gatewaymedia.NewMediaJobsRepo(db, time.Now)
	ctx := context.Background()
	seconds := 4.0
	if err := repo.Insert(ctx, mediaJobRecordOf("job_race", "key_1", "acc_1", seconds)); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var wins atomic.Int64
	errc := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			affected, err := repo.UpdateTerminal(ctx, "job_race", gatewaymedia.JobStatusCompleted, nil,
				gatewaymedia.MediaJobArtifact{ContentURL: "https://up.invalid/content"},
				gatewaymedia.MediaJobUsage{OutputVideoSeconds: &seconds}, 0.4)
			if err != nil {
				errc <- err
				return
			}
			if affected == 1 {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Fatalf("并发终态回填: %v", err)
	}
	if got := wins.Load(); got != 1 {
		t.Fatalf("终态生效次数 = %d, want 1（条件 UPDATE 保证恰好一个写入方生效）", got)
	}
	// 终态守卫：终态行不再被非终态写入（取消）回退。
	if affected, err := repo.UpdateStatus(ctx, "job_race", gatewaymedia.JobStatusCancelled); err != nil || affected != 0 {
		t.Fatalf("终态行取消写入: affected=%d err=%v, want 0/nil（不回退）", affected, err)
	}
	// 重复终态：0 行（调用方据此跳过 usage 入队）。
	if affected, err := repo.UpdateTerminal(ctx, "job_race", gatewaymedia.JobStatusFailed,
		&gatewaymedia.MediaJobError{Code: "x", Message: "y"},
		gatewaymedia.MediaJobArtifact{}, gatewaymedia.MediaJobUsage{}, 0); err != nil || affected != 0 {
		t.Fatalf("重复终态回填: affected=%d err=%v, want 0/nil", affected, err)
	}
}

// mediaVideoPollBarrier 把两个并发轮询请求在上游响应前对齐（都到达才放
// 行）：保证两个轮询 goroutine 都读到非终态行、都拿到 completed 上游响应，
// 随后在 UpdateTerminal 上真正竞争——对照无守卫实现（第二条 UPDATE 覆盖
// 第一条且双份入队 usage）。
type mediaVideoPollBarrier struct {
	mu      sync.Mutex
	arrived int
	release chan struct{}
}

func (b *mediaVideoPollBarrier) arrive() {
	b.mu.Lock()
	b.arrived++
	if b.arrived == 2 {
		close(b.release)
	}
	b.mu.Unlock()
}

// TestChainMediaVideoConcurrentTerminalSingleUsage 是 MAJOR-B 链级并发回归：
// 同 job 两轮询并发到达终态 → 条件 UPDATE 恰好一个生效 → usage spool 只
// 入队一条终态记录（终态只入队一次，否则同 job 双份 usage/计费）。
func TestChainMediaVideoConcurrentTerminalSingleUsage(t *testing.T) {
	fixture := newChainFixture(t)
	// PG 连接池真实并发执行两条轮询 UPDATE，胜负由 WHERE 非终态守卫裁决。
	mock := platformmock.New()
	defer mock.Close()

	barrier := &mediaVideoPollBarrier{release: make(chan struct{})}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/videos/") {
			barrier.arrive()
			select {
			case <-barrier.release:
			case <-time.After(5 * time.Second):
				// 超时兜底放行（另一轮询缺席时测试仍可收敛，竞争覆盖弱化）。
			}
		}
		mock.Config.Handler.ServeHTTP(w, r)
	}))
	defer upstream.Close()
	seedMediaVideoAccount(t, fixture.db, fixture, "acc_video_race", upstream.URL, "sk-video-race", 0, true)

	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain := composeMediaVideoChain(t, fixture, spoolDir)
	server := httptest.NewServer(chain)
	defer server.Close()

	job := mediaVideoCreateJob(t, server.URL, fixture.apiKeySecret,
		`{"model":"sora-2","prompt":"并发终态只入队一次","seconds":"4","size":"1280x720"}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaVideoOKPoll1)})
	jobID, _ := job["id"].(string)

	type pollResult struct {
		status int
		body   string
		err    error
	}
	results := make(chan pollResult, 2)
	for i := 0; i < 2; i++ {
		go func() {
			request, err := http.NewRequest(http.MethodGet, server.URL+"/v1/video/generations/"+jobID, nil)
			if err != nil {
				results <- pollResult{err: err}
				return
			}
			request.Header.Set("Authorization", "Bearer "+fixture.apiKeySecret)
			client := &http.Client{Timeout: 15 * time.Second}
			response, err := client.Do(request)
			if err != nil {
				results <- pollResult{err: err}
				return
			}
			payload, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			results <- pollResult{status: response.StatusCode, body: string(payload), err: readErr}
		}()
	}
	for i := 0; i < 2; i++ {
		result := <-results
		if result.err != nil {
			t.Fatalf("并发轮询 #%d: %v", i+1, result.err)
		}
		if result.status != http.StatusOK {
			t.Fatalf("并发轮询 #%d status=%d body=%s, want 200", i+1, result.status, result.body)
		}
		if !strings.Contains(result.body, `"completed"`) {
			t.Fatalf("并发轮询 #%d 未回显终态: %s", i+1, result.body)
		}
	}

	// 行终态 completed（两写入方竞争后恰好一个终态确立）。
	var status string
	if err := fixture.db.QueryRow(`SELECT status FROM media_jobs WHERE id = ?`, jobID).Scan(&status); err != nil {
		t.Fatalf("查询 media_jobs: %v", err)
	}
	if status != "completed" {
		t.Fatalf("media_jobs status = %s, want completed", status)
	}

	// usage spool 恰好一条本 job 的终态记录（等待异步落盘）。
	deadline := time.Now().Add(5 * time.Second)
	usageCount := 0
	for time.Now().Before(deadline) {
		usageCount = 0
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
				if strings.Contains(string(raw), `"jobId":"`+jobID+`"`) {
					usageCount++
				}
			}
		}
		if usageCount > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if usageCount != 1 {
		t.Fatalf("终态 usage 记录 = %d 条, want 1（并发终态只入队一次）", usageCount)
	}
}

// TestChainMediaVideoNewAPIBodyMapping 覆盖 New API 创建字段 → 内部统一参数
// 的映射（契约 §2.2）：duration→seconds、width+height→size、image→
// input_reference、seed/n 直传、metadata 已知 L2 键提升、fps/response_format/
// user 进 metadata 回显并计入 params_ignored、单边 width/height 忽略回显。
func TestChainMediaVideoNewAPIBodyMapping(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	seedMediaVideoAccount(t, fixture.db, fixture, "acc_video_map", mock.URL, "sk-video-map", 0, true)

	chain := composeMediaVideoChain(t, fixture, filepath.Join(t.TempDir(), "spool"))
	server := httptest.NewServer(chain)
	defer server.Close()

	job := mediaVideoCreateJob(t, server.URL, fixture.apiKeySecret,
		`{"model":"sora-2","prompt":"映射覆盖","duration":5,"width":1280,"height":720,"image":"https://img.invalid/first.png","seed":7,"n":1,"fps":24,"response_format":"mp4","user":"u1","metadata":{"negative_prompt":"低清","audio":false}}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaVideoOKPoll1)})
	metadata := mediaVideoMetadataOf(t, job)
	applied := fmt.Sprintf("%v", metadata["params_applied"])
	for _, want := range []string{"model", "prompt", "seconds", "size", "n", "input_reference"} {
		if !strings.Contains(applied, want) {
			t.Fatalf("params_applied 缺少 %s: %v", want, metadata["params_applied"])
		}
	}
	ignored := fmt.Sprintf("%v", metadata["params_ignored"])
	for _, want := range []string{"negative_prompt", "audio", "seed", "fps", "response_format", "user"} {
		if !strings.Contains(ignored, want) {
			t.Fatalf("params_ignored 缺少 %s: %v", want, metadata["params_ignored"])
		}
	}
	for _, want := range []string{"fps", "response_format", "user", "negative_prompt", "audio"} {
		if _, ok := metadata[want]; !ok {
			t.Fatalf("metadata 回显缺少请求键 %s: %#v", want, metadata)
		}
	}
	// 上游报文：New API 字段已归一为 openai videos 请求面字段，未知键不进报文。
	for _, request := range mock.Requests() {
		if request.Method != http.MethodPost || request.Path != "/v1/videos" {
			continue
		}
		for _, want := range []string{`"seconds":"5"`, `"size":"1280x720"`, `"input_reference":"https://img.invalid/first.png"`, `"n":1`} {
			if !strings.Contains(request.Body, want) {
				t.Fatalf("上游创建报文缺少 %s: %s", want, request.Body)
			}
		}
		for _, banned := range []string{"duration", "width", "height", `"image"`, "fps", "response_format", "metadata", "user"} {
			if strings.Contains(request.Body, banned) {
				t.Fatalf("上游创建报文不得含 New API 字段 %s: %s", banned, request.Body)
			}
		}
	}

	// 单边 width 缺失：忽略并回显，不猜测 height/size。
	single := mediaVideoCreateJob(t, server.URL, fixture.apiKeySecret,
		`{"model":"sora-2","prompt":"单边维度","width":1280}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaVideoOKPoll1)})
	singleIgnored := fmt.Sprintf("%v", mediaVideoMetadataOf(t, single)["params_ignored"])
	if !strings.Contains(singleIgnored, "width") {
		t.Fatalf("单边 width 应进 params_ignored: %v", singleIgnored)
	}
}

// TestChainMediaVideoTerminalStatusNormalization 覆盖 New API 查询状态词表与
// url/format 规则（契约 §4.2）：本地 cancelled/expired 归一 failed 并带
// error；completed 优先上游直链并按扩展名推断 format，缺失回退 content 相对
// 路径且缺省 mp4、metadata 含请求原值与诊断键。
func TestChainMediaVideoTerminalStatusNormalization(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	seedMediaVideoAccount(t, fixture.db, fixture, "acc_video_term", mock.URL, "sk-video-term", 0, true)

	chain := composeMediaVideoChain(t, fixture, filepath.Join(t.TempDir(), "spool"))
	server := httptest.NewServer(chain)
	defer server.Close()

	repo := gatewaymedia.NewMediaJobsRepo(fixture.db, time.Now)
	insert := func(id string, status gatewaymedia.MediaJobStatus, artifact gatewaymedia.MediaJobArtifact, accountID string) {
		t.Helper()
		record := gatewaymedia.MediaJobRecord{
			ID: id, Kind: gatewaymedia.JobKindVideo, APIKeyID: "key_1",
			OwnerSystemAccountID: fixture.systemAccount, AccountID: accountID,
			VendorCode: "gpt", UpstreamJobID: "up_term", Status: status,
			RequestSnapshot: gatewaymedia.MediaJobRequestSnapshot{Model: "sora-2", Metadata: map[string]any{"trace": "t1"}},
			Artifact:        artifact,
			ParamsApplied:   []string{"model", "prompt", "seconds"},
			ParamsIgnored:   []string{"seed"},
		}
		if err := repo.Insert(context.Background(), record); err != nil {
			t.Fatalf("insert terminal row %s: %v", id, err)
		}
	}
	insert("video_cancelled", gatewaymedia.JobStatusCancelled, gatewaymedia.MediaJobArtifact{}, "acc_video_term")
	insert("video_expired", gatewaymedia.JobStatusExpired, gatewaymedia.MediaJobArtifact{}, "acc_video_term")
	// 直链用例：任务行 vendor 读面从 accounts.vendor_code 水合（B4① 后行内
	// 不落 vendor），必须落到 xai 账户上才能覆盖 ContentFromArtifact=true 的
	// 直链分支；gpt/Sora（video_fallback）即便行内有上游定位也强制回退网关
	// content 端点（契约 §4.2）。
	seedMediaVideoAccountWithVendor(t, fixture.db, fixture, "acc_video_term_xai", mock.URL, "sk-video-term-xai", 0, "xai", true)
	insert("video_direct", gatewaymedia.JobStatusCompleted, gatewaymedia.MediaJobArtifact{ContentURL: "https://cdn.invalid/out/clip.webm"}, "acc_video_term_xai")
	insert("video_fallback", gatewaymedia.JobStatusCompleted, gatewaymedia.MediaJobArtifact{}, "acc_video_term")

	type termCase struct {
		id         string
		wantStatus string
		wantURL    string
		wantFormat string
		wantErr    string
		wantVendor string
	}
	for _, tc := range []termCase{
		{"video_cancelled", "failed", "", "mp4", "cancelled", "gpt"},
		{"video_expired", "failed", "", "mp4", "expired", "gpt"},
		{"video_direct", "completed", "https://cdn.invalid/out/clip.webm", "webm", "", "xai"},
		{"video_fallback", "completed", "/v1/video/generations/video_fallback/content", "mp4", "", "gpt"},
	} {
		response, payload := mediaVideoClientDo(t, server.URL, http.MethodGet, "/v1/video/generations/"+tc.id, fixture.apiKeySecret, "", nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", tc.id, response.StatusCode, payload)
		}
		object := mediaVideoDecodeJob(t, payload)
		if object["task_id"] != tc.id || object["status"] != tc.wantStatus || object["url"] != tc.wantURL || object["format"] != tc.wantFormat {
			t.Fatalf("%s 查询响应不符: %s", tc.id, payload)
		}
		if object["code"] != "success" || object["message"] != "" {
			t.Fatalf("%s 缺少 code/message 包裹: %s", tc.id, payload)
		}
		data, _ := object["data"].(map[string]any)
		if data == nil || data["task_id"] != tc.id || data["status"] != tc.wantStatus || data["url"] != tc.wantURL || data["format"] != tc.wantFormat {
			t.Fatalf("%s data 包裹不符: %s", tc.id, payload)
		}
		flatError, _ := object["error"].(map[string]any)
		if tc.wantErr == "" {
			if flatError != nil {
				t.Fatalf("%s 不应携带 error: %s", tc.id, payload)
			}
		} else if flatError == nil || flatError["code"] != tc.wantErr {
			t.Fatalf("%s error 应携带 code=%s: %s", tc.id, tc.wantErr, payload)
		}
		metadata, _ := object["metadata"].(map[string]any)
		if metadata == nil || metadata["trace"] != "t1" || metadata["provider"] != tc.wantVendor || metadata["provider_job_id"] != "up_term" {
			t.Fatalf("%s metadata 回显不符: %s", tc.id, payload)
		}
	}
}

// TestChainMediaVideoPublicBaseURLAbsoluteFallback 是
// JUHE_AI_GATEWAY_PUBLIC_BASE_URL 的链级验证（组合根 deps → mediaJobsRuntime
// → 查询响应）：配置前缀后 gpt 回退 content URL 为绝对地址，未配置保持相对
// 路径（相对分支由 TestChainMediaVideoTerminalStatusNormalization 钉扎）。
func TestChainMediaVideoPublicBaseURLAbsoluteFallback(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	seedMediaVideoAccount(t, fixture.db, fixture, "acc_video_pub", mock.URL, "sk-video-pub", 0, true)

	chain := composeMediaVideoChainWithPublicBase(t, fixture, filepath.Join(t.TempDir(), "spool"), "https://api.example.com")
	server := httptest.NewServer(chain)
	defer server.Close()

	repo := gatewaymedia.NewMediaJobsRepo(fixture.db, time.Now)
	record := gatewaymedia.MediaJobRecord{
		ID: "video_pub_base", Kind: gatewaymedia.JobKindVideo, APIKeyID: "key_1",
		OwnerSystemAccountID: fixture.systemAccount, AccountID: "acc_video_pub",
		VendorCode: "gpt", UpstreamJobID: "up_pub", Status: gatewaymedia.JobStatusCompleted,
		RequestSnapshot: gatewaymedia.MediaJobRequestSnapshot{Model: "sora-2"},
	}
	if err := repo.Insert(context.Background(), record); err != nil {
		t.Fatalf("insert row: %v", err)
	}
	response, payload := mediaVideoClientDo(t, server.URL, http.MethodGet, "/v1/video/generations/video_pub_base", fixture.apiKeySecret, "", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, payload)
	}
	object := mediaVideoDecodeJob(t, payload)
	if object["url"] != "https://api.example.com/v1/video/generations/video_pub_base/content" {
		t.Fatalf("url = %v, want absolute content fallback", object["url"])
	}
}
