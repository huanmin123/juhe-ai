package acceptance

// M2 视频链路全链路验收（媒体矩阵 Stage 3）——在 fullchain 夹具（真实
// gateway 二进制 + 场景控制器 mock 上游 + 管理面全量配置）之上覆盖四条
// /v1/videos 业务链，场景用 mockupstream media_video_*（契约 §4.3）：
//
//  1. TestFullchainMediaVideoLifecycle：完整生命周期——创建（统一 job 对象、
//     params_ignored 回显 negative_prompt）→ 轮询 33→66→completed → content
//     下载（video/mp4 + ftyp magic bytes）→ 管理面 media-jobs 行终态
//     cost_usd>0；
//  2. TestFullchainMediaVideoAcceptanceBoundary：受理边界（媒体设计 §7）——
//     创建 429（受理前）换账户重建；受理后轮询 500 只报错不换账户（任务面
//     账户亲和，断言另一账户无任何任务面请求）；
//  3. TestFullchainMediaVideoArtifactExpired：产物过期（§8.1.5）——completed
//     后上游 content 404 → 网关 410 media_artifact_expired；
//  4. TestFullchainMediaVideoCreate400BadSizeNoSwitch：创建参数类 400
//     （§7 创建行）——上游 400 invalid_size 透传客户端，不换账户、不落
//     media_jobs 行（M2 复审 MAJOR-A 修复回归）。
//
// 视频账户与 chat 矩阵账户的差异只有两点：supportedModels 声明 sora-2（gpt
// 供应商目录 M2 预置行）、credentials.supported_endpoint_modes 显式含
// video_create（创建链候选过滤的 opt-in 模式，媒体设计 §11.6），其余完全
// 复用 fullchain 夹具。场景队列时序注意（fixture 头注 §3.3.2）：任务面 GET
// 也是 scriptable，会消耗队列剩余条目，但 mockupstream 引擎的 poll/content
// handler 不读场景值（按创建时冻结的任务脚本推进），因此每个创建场景在发起
// 创建 POST 前排到队首、耗尽后回落 globalDefault 即可，不影响任务面行为。
import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	platformmock "github.com/huanminabc/juhe-ai/backend-go-platform/mockupstream"
)

// fullchainVideoCreateBody 是视频创建请求体（negative_prompt 对 sora 请求面
// 不支持 → 进 params_ignored 回显，契约 §2.4 规则 1；seconds 用 openai 原生
// 字符串形态，契约 §4.3）。
const fullchainVideoCreateBody = `{"model":"sora-2","prompt":"一只猫在弹钢琴","seconds":"4","size":"1280x720","negative_prompt":"低清画质"}`

// fullchainVideoAccountExtra 是视频验收账户相对 createAccount 默认值的覆盖项：
// 支持模型声明 sora-2 + 探针模型，凭据显式声明视频端点模式（创建链候选过滤
// 消费 video_create；其余 chat/responses 模式保留健康检查语义）。
func fullchainVideoAccountExtra() map[string]any {
	return map[string]any{
		"supportedModels": []string{"sora-2", fullchainProbeModel},
		"credentials": map[string]any{
			"supported_endpoint_modes": []string{
				"chat_json", "chat_sse", "responses_json", "responses_sse",
				"video_create", "video_get", "video_content", "video_cancel",
			},
		},
	}
}

// videoT 调用 /v1/videos 族端点（POST 创建 / GET 任务面），body 为完整 JSON
// 字符串（空串表示无请求体）。
func (f *fullchainFixture) videoT(t *testing.T, apiKey, method, path, body string) fullchainChatResponse {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, f.gw.baseURL+path, reader)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	return f.doRawT(t, request)
}

// decodeVideoJob 解析统一 job 对象响应体（创建/轮询共用形态）。
func decodeVideoJob(t *testing.T, response fullchainChatResponse) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal([]byte(response.Body), &object); err != nil {
		t.Fatalf("decode video job object: %v: %s", err, response.Body)
	}
	return object
}

// ---------------------------------------------------------------------------
// 管理面 media-jobs 断言面
// ---------------------------------------------------------------------------

// fullchainMediaJobRow 是 GET /__aisys__/api/media-jobs 列表行的断言投影。
type fullchainMediaJobRow struct {
	ID            string
	Kind          string
	Status        string
	AccountID     string
	ProviderJobID string
	CostUsd       float64
}

// listMediaJobRows 按 apiKeyId 过滤拉取管理面媒体任务列表（requireAdmin 由
// admin 登录态满足；行数据直读 media_jobs 业务库，不经 usage 交接链）。
func (f *fullchainFixture) listMediaJobRows(t *testing.T, apiKeyID string) []fullchainMediaJobRow {
	t.Helper()
	_, payload := f.admin.do(http.MethodGet,
		"/__aisys__/api/media-jobs?api_key_id="+url.QueryEscape(apiKeyID)+"&limit=100", nil, wantStatus(http.StatusOK))
	rows, _ := data(payload)["rows"].([]any)
	out := []fullchainMediaJobRow{}
	for _, raw := range rows {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		cost, _ := item["costUsd"].(float64)
		out = append(out, fullchainMediaJobRow{
			ID:            str(item["id"]),
			Kind:          str(item["kind"]),
			Status:        str(item["status"]),
			AccountID:     str(item["accountId"]),
			ProviderJobID: str(item["providerJobId"]),
			CostUsd:       cost,
		})
	}
	return out
}

// waitMediaJobRow 轮询直到指定 job 行满足谓词（行创建与终态回填都发生在
// 对应请求内、业务库同步可见；轮询仅兜底读模型与请求时序窗口）。
func (f *fullchainFixture) waitMediaJobRow(t *testing.T, apiKeyID, jobID string, predicate func(fullchainMediaJobRow) bool, what string) fullchainMediaJobRow {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, row := range f.listMediaJobRows(t, apiKeyID) {
			if row.ID == jobID && predicate(row) {
				return row
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("media job row %s not %s in time; rows=%#v", jobID, what, f.listMediaJobRows(t, apiKeyID))
	return fullchainMediaJobRow{}
}

// ---------------------------------------------------------------------------
// 用例 1：完整生命周期（media_video_ok_poll3）
// ---------------------------------------------------------------------------

// TestFullchainMediaVideoLifecycle 验证 M2 视频链主流程：管理面配置 → 创建
// （params_ignored 回显）→ 轮询 33→66→completed → content mp4 下载 → 管理面
// media-jobs 行终态 cost_usd>0（媒体设计 §10 终态计费回填）。
func TestFullchainMediaVideoLifecycle(t *testing.T) {
	requireFullchainGate(t)
	f := startFullchainFixture(t)

	route := f.newRoute("MV生命", "normal", []fullchainGroupSpec{
		{accounts: []fullchainAccountSpec{
			{script: []platformmock.Scenario{platformmock.ScenarioMediaVideoOKPoll3}, extra: fullchainVideoAccountExtra()},
		}},
	}, nil, nil)

	// 创建：200 + 统一 job 对象（对外 id video_ 前缀、status queued、
	// params_ignored 含 negative_prompt、params_applied 覆盖核心参数）。
	created := f.videoT(t, route.apiKey, http.MethodPost, "/v1/videos", fullchainVideoCreateBody)
	if created.Status != http.StatusOK {
		t.Fatalf("MV lifecycle create status=%d body=%s", created.Status, created.Body)
	}
	job := decodeVideoJob(t, created)
	jobID := str(job["id"])
	if !strings.HasPrefix(jobID, "video_") {
		t.Fatalf("MV lifecycle job id 缺少 video_ 前缀: %#v", job)
	}
	if job["status"] != "queued" {
		t.Fatalf("MV lifecycle create status 字段 = %v, want queued", job["status"])
	}
	if !strings.Contains(fmt.Sprintf("%v", job["params_ignored"]), "negative_prompt") {
		t.Fatalf("MV lifecycle params_ignored 缺少 negative_prompt: %v", job["params_ignored"])
	}
	applied := fmt.Sprintf("%v", job["params_applied"])
	for _, want := range []string{"model", "prompt", "seconds", "size"} {
		if !strings.Contains(applied, want) {
			t.Fatalf("MV lifecycle params_applied 缺少 %s: %v", want, job["params_applied"])
		}
	}

	// 轮询三次：in_progress(33) → in_progress(66) → completed(100)。
	wantProgress := []struct {
		progress float64
		status   string
	}{{33, "in_progress"}, {66, "in_progress"}, {100, "completed"}}
	for index, want := range wantProgress {
		polled := f.videoT(t, route.apiKey, http.MethodGet, "/v1/videos/"+jobID, "")
		if polled.Status != http.StatusOK {
			t.Fatalf("MV lifecycle poll #%d status=%d body=%s", index+1, polled.Status, polled.Body)
		}
		object := decodeVideoJob(t, polled)
		progress, _ := object["progress"].(float64)
		if progress != want.progress || object["status"] != want.status {
			t.Fatalf("MV lifecycle poll #%d status/progress = %v/%v, want %s/%v（body=%s）",
				index+1, object["status"], progress, want.status, want.progress, polled.Body)
		}
	}

	// content 下载：video/mp4 + ftyp magic bytes（ISO-BMFF 首盒，mock 契约
	// §3.2.3 的确定性 72 字节载荷）。
	content := f.videoT(t, route.apiKey, http.MethodGet, "/v1/videos/"+jobID+"/content", "")
	if content.Status != http.StatusOK {
		t.Fatalf("MV lifecycle content status=%d body=%s", content.Status, content.Body)
	}
	if !strings.Contains(content.ContentType, "video/mp4") {
		t.Fatalf("MV lifecycle content-type=%q, want video/mp4", content.ContentType)
	}
	payload := []byte(content.Body)
	if len(payload) < 12 || string(payload[4:8]) != "ftyp" {
		t.Fatalf("MV lifecycle content 非 mp4 载荷（ftyp magic bytes 缺失）: % x", payload[:min(12, len(payload))])
	}

	// 管理面 media-jobs：行终态 completed + cost_usd>0（sora-2 静态目录秒价
	// × seconds_length=4，随终态一次落行），且归因到受理账户。
	row := f.waitMediaJobRow(t, route.apiKeyID, jobID, func(row fullchainMediaJobRow) bool {
		return row.Status == "completed" && row.CostUsd > 0
	}, "completed with cost_usd>0")
	if row.AccountID != route.accountIDs[0] {
		t.Fatalf("MV lifecycle row accountId=%s, want %s", row.AccountID, route.accountIDs[0])
	}
}

// ---------------------------------------------------------------------------
// 用例 2：受理边界（media_video_429_create / media_video_ok_poll1 /
// media_video_poll_500）
// ---------------------------------------------------------------------------

// TestFullchainMediaVideoAcceptanceBoundary 验证媒体设计 §7 受理边界：
//   - 受理前（创建请求）429 属可切换故障——A 账户 429 后换 B 账户成功受理
//     （两账户各收到创建请求，任务归因 B）；
//   - 受理后（任务面轮询）500 属账户亲和故障——网关对原账户透出错误，不得
//     对另一账户发轮询（A 无任何任务面 GET 请求）。
func TestFullchainMediaVideoAcceptanceBoundary(t *testing.T) {
	requireFullchainGate(t)
	f := startFullchainFixture(t)

	keyA := fullchainUpstreamKey(t, "mv-429")
	route := f.newRoute("MV边界", "normal", []fullchainGroupSpec{
		{accounts: []fullchainAccountSpec{
			// A 持续 429（default 兜底）：即使派发引擎对 429 做同账户重试，
			// 也不会「意外成功」污染换账户语义。
			{key: keyA, defaultScenario: platformmock.ScenarioMediaVideo429Create, extra: fullchainVideoAccountExtra()},
			{extra: fullchainVideoAccountExtra()},
		}},
	}, nil, nil)
	keyB := route.upstreamKeys[1]

	// 任务 1：创建经 A 429 → 换 B（ok_poll1）受理成功。
	f.mock.script(keyB, platformmock.ScenarioMediaVideoOKPoll1)
	created := f.videoT(t, route.apiKey, http.MethodPost, "/v1/videos", fullchainVideoCreateBody)
	if created.Status != http.StatusOK {
		t.Fatalf("MV boundary create#1 status=%d body=%s", created.Status, created.Body)
	}
	job1ID := str(decodeVideoJob(t, created)["id"])

	// 换账户证据：A、B 各收到创建请求（A 的 429 尝试 + B 的成功受理）。
	countCreateCalls := func(key string) int {
		hits := 0
		for _, call := range f.mock.callsByKey(key) {
			if call.Method == http.MethodPost && call.Path == "/v1/videos" {
				hits++
			}
		}
		return hits
	}
	if hitsA, hitsB := countCreateCalls(keyA), countCreateCalls(keyB); hitsA < 1 || hitsB < 1 {
		t.Fatalf("MV boundary 换账户证据不足：A 创建请求 %d 次、B 创建请求 %d 次（want 各 >=1）", hitsA, hitsB)
	}
	// 任务归因 B（media_jobs 行 account_id）。
	row1 := f.waitMediaJobRow(t, route.apiKeyID, job1ID, func(row fullchainMediaJobRow) bool {
		return row.Status != ""
	}, "present")
	if row1.AccountID != route.accountIDs[1] {
		t.Fatalf("MV boundary job#1 accountId=%s, want B(%s)", row1.AccountID, route.accountIDs[1])
	}
	// 推进任务 1 到终态（ok_poll1 快路径），与后续新任务互不干扰。
	if polled := f.videoT(t, route.apiKey, http.MethodGet, "/v1/videos/"+job1ID, ""); polled.Status != http.StatusOK {
		t.Fatalf("MV boundary poll#1 status=%d body=%s", polled.Status, polled.Body)
	}

	// 任务 2（同一账户 B 的新任务）：受理后轮询 500。
	f.mock.script(keyB, platformmock.ScenarioMediaVideoPoll500)
	created2 := f.videoT(t, route.apiKey, http.MethodPost, "/v1/videos", fullchainVideoCreateBody)
	if created2.Status != http.StatusOK {
		t.Fatalf("MV boundary create#2 status=%d body=%s", created2.Status, created2.Body)
	}
	job2 := decodeVideoJob(t, created2)
	job2ID, providerJob2ID := str(job2["id"]), str(job2["provider_job_id"])
	if job2["status"] != "queued" {
		t.Fatalf("MV boundary create#2 status 字段 = %v, want queued", job2["status"])
	}

	// 轮询 500：网关透出错误（502 upstream_error，受理后不换账户不伪造终态）。
	pollFailed := f.videoT(t, route.apiKey, http.MethodGet, "/v1/videos/"+job2ID, "")
	if pollFailed.Status != http.StatusBadGateway {
		t.Fatalf("MV boundary poll#2 status=%d, want 502（上游轮询 500 透出为网关错误）body=%s", pollFailed.Status, pollFailed.Body)
	}
	if !strings.Contains(pollFailed.Body, "upstream_error") {
		t.Fatalf("MV boundary poll#2 错误码缺少 upstream_error: %s", pollFailed.Body)
	}

	// 账户亲和证据：B 收到任务 2 的轮询；A 从未收到任何任务面请求。
	pollHitsB := 0
	for _, call := range f.mock.callsByKey(keyB) {
		if call.Method == http.MethodGet && call.Path == "/v1/videos/"+providerJob2ID {
			pollHitsB++
		}
	}
	if pollHitsB < 1 {
		t.Fatalf("MV boundary B 未收到 job#2 轮询（providerJobId=%s），B calls=%#v", providerJob2ID, f.mock.callsByKey(keyB))
	}
	for _, call := range f.mock.callsByKey(keyA) {
		if call.Method == http.MethodGet && strings.HasPrefix(call.Path, "/v1/videos") {
			t.Fatalf("MV boundary 受理后故障不得换账户轮询：A 收到任务面请求 %#v", call)
		}
	}
}

// ---------------------------------------------------------------------------
// 用例 4：创建参数错误不换账户（media_video_create_400_bad_size）
// ---------------------------------------------------------------------------

// TestFullchainMediaVideoCreate400BadSizeNoSwitch 验证媒体设计 §7 创建行
// 参数类 4xx 语义（M2 复审 MAJOR-A）：上游 400（size 值域外，本地形态校验
// 不触发）→ 网关把上游 400 + invalid_size 错误体透传客户端，不换账户（B
// 零创建请求，对照 429 的可切换语义）、不落 media_jobs 行。
func TestFullchainMediaVideoCreate400BadSizeNoSwitch(t *testing.T) {
	requireFullchainGate(t)
	f := startFullchainFixture(t)

	keyA := fullchainUpstreamKey(t, "mv-400")
	route := f.newRoute("MV参数错误", "normal", []fullchainGroupSpec{
		{accounts: []fullchainAccountSpec{
			// A 持续 400 参数错误（default 兜底）；B 默认 ok 脚本——若短路
			// 缺失，A 的 400 会换 B 重建（429 同款），B 零创建可证伪。
			{key: keyA, defaultScenario: platformmock.ScenarioMediaVideoCreate400BadSize, extra: fullchainVideoAccountExtra()},
			{extra: fullchainVideoAccountExtra()},
		}},
	}, nil, nil)
	keyB := route.upstreamKeys[1]

	// size=9999x9999：本地 WxH 形态校验通过，值域由上游裁决 → 上游 400。
	failed := f.videoT(t, route.apiKey, http.MethodPost, "/v1/videos",
		`{"model":"sora-2","prompt":"参数错误不换账户","seconds":"4","size":"9999x9999"}`)
	if failed.Status != http.StatusBadRequest {
		t.Fatalf("MV 400 create status=%d body=%s, want 400（上游参数错误透传）", failed.Status, failed.Body)
	}
	if !strings.Contains(failed.Body, "invalid_size") {
		t.Fatalf("MV 400 响应缺少上游 invalid_size 错误码: %s", failed.Body)
	}

	// 不换账户证据：A 恰好一次创建（mock 请求记录单账户），B 零创建请求。
	countCreateCalls := func(key string) int {
		hits := 0
		for _, call := range f.mock.callsByKey(key) {
			if call.Method == http.MethodPost && call.Path == "/v1/videos" {
				hits++
			}
		}
		return hits
	}
	if hitsA := countCreateCalls(keyA); hitsA != 1 {
		t.Fatalf("MV 400 A 创建请求 %d 次, want 1（恰好一次，失败后不重放同账户）", hitsA)
	}
	if hitsB := countCreateCalls(keyB); hitsB != 0 {
		t.Fatalf("MV 400 B 创建请求 %d 次, want 0（确定性参数错误不得换账户）", hitsB)
	}
	// 受理凭据未确立，不落任务行。
	if rows := f.listMediaJobRows(t, route.apiKeyID); len(rows) != 0 {
		t.Fatalf("MV 400 不得落 media_jobs 行: %#v", rows)
	}
}

// ---------------------------------------------------------------------------
// 用例 3：产物过期（media_video_content_expired）
// ---------------------------------------------------------------------------

// TestFullchainMediaVideoArtifactExpired 验证 §8.1.5：任务 completed 但上游
// 产物已失效（mock 引擎 content 404）→ 网关 410 media_artifact_expired，
// 不补救不降级。
func TestFullchainMediaVideoArtifactExpired(t *testing.T) {
	requireFullchainGate(t)
	f := startFullchainFixture(t)

	route := f.newRoute("MV过期", "normal", []fullchainGroupSpec{
		{accounts: []fullchainAccountSpec{
			{script: []platformmock.Scenario{platformmock.ScenarioMediaVideoContentExpired}, extra: fullchainVideoAccountExtra()},
		}},
	}, nil, nil)

	created := f.videoT(t, route.apiKey, http.MethodPost, "/v1/videos", fullchainVideoCreateBody)
	if created.Status != http.StatusOK {
		t.Fatalf("MV expired create status=%d body=%s", created.Status, created.Body)
	}
	jobID := str(decodeVideoJob(t, created)["id"])

	polled := f.videoT(t, route.apiKey, http.MethodGet, "/v1/videos/"+jobID, "")
	if polled.Status != http.StatusOK {
		t.Fatalf("MV expired poll status=%d body=%s", polled.Status, polled.Body)
	}
	if object := decodeVideoJob(t, polled); object["status"] != "completed" {
		t.Fatalf("MV expired poll status 字段 = %v, want completed（content_expired 脚本轮询即完成）", object["status"])
	}

	content := f.videoT(t, route.apiKey, http.MethodGet, "/v1/videos/"+jobID+"/content", "")
	if content.Status != http.StatusGone {
		t.Fatalf("MV expired content status=%d, want 410 body=%s", content.Status, content.Body)
	}
	if !strings.Contains(content.Body, "media_artifact_expired") {
		t.Fatalf("MV expired content 错误码缺少 media_artifact_expired: %s", content.Body)
	}
}

// ---------------------------------------------------------------------------
// 用例 5：Gemini Veo 全生命周期（media_gemini_video_*，M3/契约 §5.2）
// ---------------------------------------------------------------------------

// fullchainGeminiVideoCreateBody 是 gemini 视频创建请求体：size 换算
// aspectRatio/resolution（16:9/720p）、negative_prompt 进 parameters（veo
// 原生支持）；seconds 对 veo 请求面不存在 → params_ignored 回显。
const fullchainGeminiVideoCreateBody = `{"model":"veo-3.0-generate-preview","prompt":"一只猫在弹钢琴","size":"1280x720","negative_prompt":"低清画质","seconds":"8"}`

// fullchainCreateGeminiVideoAccount 经管理面创建指向 mock 上游的 gemini
// api_key 账户并绑定分组（gemini 供应商 + 原生 v1beta 档案；目录 seed 预置
// veo-3.0-generate-preview 行）；凭据显式声明 gemini 词表的 video_* 端点
// 模式（opt-in，与 openai 族同语义）。
func (f *fullchainFixture) fullchainCreateGeminiVideoAccount(name, upstreamKey, groupID string) string {
	f.t.Helper()
	_, created := f.admin.do(http.MethodPost, "/__aisys__/api/accounts", map[string]any{
		"providerCode":              "gemini",
		"providerProtocolProfileId": "profile_gemini_native_v1beta",
		"name":                      name,
		"type":                      "api_key",
		"credentials": map[string]any{
			"api_key":  upstreamKey,
			"base_url": f.mock.server.URL,
			"supported_endpoint_modes": []string{
				"generate_content_json", "video_create", "video_get", "video_content", "video_cancel",
			},
		},
		"supportedModels": []string{"veo-3.0-generate-preview"},
		"status":          "active",
		"groupId":         groupID,
	}, wantStatus(http.StatusCreated))
	accountID := str(data(created)["id"])
	if accountID == "" {
		f.t.Fatalf("gemini video account create payload wrong: %#v", created)
	}
	return accountID
}

// TestFullchainMediaVideoGeminiVeo 验证 M3 gemini（Veo）视频链主流程（契约
// §5.2）：管理面配置（gemini 分组 + veo 模型账户）→ 创建（predictLongRunning
// 改写、统一 job 对象、provider=gemini、params 回显 seconds 进 ignored）→
// 轮询 queued → completed（operation done 归一 + uri 冻结）→ content 经绝对
// 签名 URL 无凭据直连下载 mp4 → 管理面 media-jobs 行终态（usage_missing 口
// 径 cost=0，不虚计）→ 账户亲和（上游命中只打 gemini 账户 key 的 veo 端点）。
func TestFullchainMediaVideoGeminiVeo(t *testing.T) {
	requireFullchainGate(t)
	f := startFullchainFixture(t)

	key := fullchainUpstreamKey(t, "mvg")
	groupID := f.createGroupWithProvider("MVgemini组", "gemini")
	accountID := f.fullchainCreateGeminiVideoAccount("全链路-MVgemini账户", key, groupID)
	strategyID := f.createStrategy("全链路-MVgemini策略", "normal", []map[string]any{
		{"groupId": groupID, "priority": 1, "weight": 100},
	}, nil)
	apiKey := f.createAPIKey("全链路-MVgemini-Key", strategyID)
	apiKeyID := f.apiKeyIDByName("全链路-MVgemini-Key")

	// 创建：200 + 统一 job 对象（对外 id video_ 前缀、status queued、
	// provider_job_id 为 operation 名、params_applied 含 size/negative_prompt、
	// params_ignored 含 seconds）。
	f.mock.script(key, platformmock.ScenarioMediaGeminiVideoCreateOK)
	created := f.videoT(t, apiKey, http.MethodPost, "/v1/videos", fullchainGeminiVideoCreateBody)
	if created.Status != http.StatusOK {
		t.Fatalf("MV gemini create status=%d body=%s", created.Status, created.Body)
	}
	job := decodeVideoJob(t, created)
	jobID := str(job["id"])
	if !strings.HasPrefix(jobID, "video_") {
		t.Fatalf("MV gemini job id 缺少 video_ 前缀: %#v", job)
	}
	if job["status"] != "queued" {
		t.Fatalf("MV gemini create status 字段 = %v, want queued（operation 未 done 归一 queued）", job["status"])
	}
	if job["provider"] != "gemini" {
		t.Fatalf("MV gemini provider = %v, want gemini", job["provider"])
	}
	providerJobID := str(job["provider_job_id"])
	if !strings.HasPrefix(providerJobID, "models/veo-3.0-generate-preview/operations/") {
		t.Fatalf("MV gemini provider_job_id = %q, want operation name 形态", providerJobID)
	}
	applied := fmt.Sprintf("%v", job["params_applied"])
	for _, want := range []string{"model", "prompt", "size", "negative_prompt"} {
		if !strings.Contains(applied, want) {
			t.Fatalf("MV gemini params_applied 缺少 %s: %v", want, job["params_applied"])
		}
	}
	if !strings.Contains(fmt.Sprintf("%v", job["params_ignored"]), "seconds") {
		t.Fatalf("MV gemini params_ignored 缺少 seconds（veo 请求面无时长参数）: %v", job["params_ignored"])
	}

	// 轮询两次：queued（done:false）→ completed（done:true + uri 冻结）。
	first := f.videoT(t, apiKey, http.MethodGet, "/v1/videos/"+jobID, "")
	if first.Status != http.StatusOK {
		t.Fatalf("MV gemini poll#1 status=%d body=%s", first.Status, first.Body)
	}
	if object := decodeVideoJob(t, first); object["status"] != "queued" {
		t.Fatalf("MV gemini poll#1 status 字段 = %v, want queued", object["status"])
	}
	second := f.videoT(t, apiKey, http.MethodGet, "/v1/videos/"+jobID, "")
	if second.Status != http.StatusOK {
		t.Fatalf("MV gemini poll#2 status=%d body=%s", second.Status, second.Body)
	}
	if object := decodeVideoJob(t, second); object["status"] != "completed" {
		t.Fatalf("MV gemini poll#2 status 字段 = %v, want completed: %v", object["status"], object)
	}

	// content 下载：completed 的产物 uri 是引擎渲染的绝对签名 URL，网关
	// 无凭据直连（video/mp4 + ftyp magic bytes）。
	content := f.videoT(t, apiKey, http.MethodGet, "/v1/videos/"+jobID+"/content", "")
	if content.Status != http.StatusOK {
		t.Fatalf("MV gemini content status=%d body=%s", content.Status, content.Body)
	}
	if !strings.Contains(content.ContentType, "video/mp4") {
		t.Fatalf("MV gemini content-type=%q, want video/mp4", content.ContentType)
	}
	payload := []byte(content.Body)
	if len(payload) < 12 || string(payload[4:8]) != "ftyp" {
		t.Fatalf("MV gemini content 非 mp4 载荷（ftyp magic bytes 缺失）: % x", payload[:min(12, len(payload))])
	}

	// 管理面 media-jobs：行终态 completed 归因 gemini 账户；veo 无输出秒
	// 回报且 seconds 属 ignored → cost_usd=0（契约 §2.8 usage_missing 不虚计）。
	row := f.waitMediaJobRow(t, apiKeyID, jobID, func(row fullchainMediaJobRow) bool {
		return row.Status == "completed"
	}, "completed")
	if row.AccountID != accountID {
		t.Fatalf("MV gemini row accountId=%s, want %s", row.AccountID, accountID)
	}
	if row.CostUsd != 0 {
		t.Fatalf("MV gemini row costUsd=%v, want 0（usage_missing 不虚计）", row.CostUsd)
	}

	// 账户亲和 + 出站形态：gemini key 命中创建（predictLongRunning）与两轮
	// 轮询（GET /v1beta/{name}），无其它上游流量打到该 key。
	creates, polls := 0, 0
	for _, call := range f.mock.callsByKey(key) {
		switch {
		case call.Method == http.MethodPost && strings.HasSuffix(call.Path, ":predictLongRunning"):
			creates++
		case call.Method == http.MethodGet && strings.Contains(call.Path, "/operations/"):
			polls++
		default:
			t.Fatalf("MV gemini 未预期的上游请求: %#v", call)
		}
	}
	if creates != 1 || polls != 2 {
		t.Fatalf("MV gemini 上游命中 creates=%d polls=%d, want 1/2", creates, polls)
	}
}

// ---------------------------------------------------------------------------
// 用例 6：GLM CogVideoX 全生命周期（media_glm_video_*，M3/契约 §7.1）
// ---------------------------------------------------------------------------

// fullchainGlmVideoCreateBody 是 glm 视频创建请求体：negative_prompt/audio/
// seconds 直达 glm 请求面（原生字段），size 原样透传；seed 对 glm 请求面
// 不存在 → params_ignored 回显。
const fullchainGlmVideoCreateBody = `{"model":"cogvideox-3","prompt":"一只猫在弹钢琴","size":"1280x720","negative_prompt":"低清画质","seconds":"6","audio":true,"seed":42}`
// fullchainCreateGlmVideoAccount 经管理面创建指向 mock 上游的 glm api_key
// 账户并绑定分组（glm 供应商 + 通用 openai v1 档案；目录 seed 预置
// cogvideox-3 行）；凭据显式声明 openai 族词表的 video_* 端点模式（opt-in，
// glm 写侧词表已放宽 video_*，本用例同时覆盖该写侧门禁）。
func (f *fullchainFixture) fullchainCreateGlmVideoAccount(name, upstreamKey, groupID string) string {
	f.t.Helper()
	_, created := f.admin.do(http.MethodPost, "/__aisys__/api/accounts", map[string]any{
		"providerCode":              "glm",
		"providerProtocolProfileId": "profile_glm_general_openai_v1",
		"name":                      name,
		"type":                      "api_key",
		"credentials": map[string]any{
			"api_key":  upstreamKey,
			"base_url": f.mock.server.URL,
			"supported_endpoint_modes": []string{
				"chat_json", "chat_sse", "video_create", "video_get", "video_content", "video_cancel",
			},
		},
		"supportedModels": []string{"cogvideox-3"},
		"status":          "active",
		"groupId":         groupID,
	}, wantStatus(http.StatusCreated))
	accountID := str(data(created)["id"])
	if accountID == "" {
		f.t.Fatalf("glm video account create payload wrong: %#v", created)
	}
	return accountID
}

// TestFullchainMediaVideoGlmCogVideo 验证 M3 glm（CogVideoX）视频链主流程
// （契约 §7.1）：管理面配置（glm 分组 + cogvideox 模型账户，含写侧 video_*
// 端点模式受理）→ 创建（videos/generations 报文改写、统一 job 对象、
// provider=glm、params 回显 seed 进 ignored）→ 轮询 in_progress → completed
// （task_status 归一 + video_result.url 冻结）→ content 经绝对 URL 无凭据
// 直连下载 mp4 → 管理面 media-jobs 行终态（按次计费口径 cost=$0.2，
// 2026-10-05 按次计费批）→ 账户亲和（上游命中只打 glm 账户 key 的 cogvideo
// 端点）。
func TestFullchainMediaVideoGlmCogVideo(t *testing.T) {
	requireFullchainGate(t)
	f := startFullchainFixture(t)

	key := fullchainUpstreamKey(t, "mvglm")
	groupID := f.createGroupWithProvider("MVglm组", "glm")
	accountID := f.fullchainCreateGlmVideoAccount("全链路-MVglm账户", key, groupID)
	strategyID := f.createStrategy("全链路-MVglm策略", "normal", []map[string]any{
		{"groupId": groupID, "priority": 1, "weight": 100},
	}, nil)
	apiKey := f.createAPIKey("全链路-MVglm-Key", strategyID)
	apiKeyID := f.apiKeyIDByName("全链路-MVglm-Key")

	// 创建：200 + 统一 job 对象（对外 id video_ 前缀、status in_progress、
	// provider_job_id 为 12 hex 任务 id、params_applied 含 size/
	// negative_prompt/seconds/audio、params_ignored 含 seed）。
	f.mock.script(key, platformmock.ScenarioMediaGlmVideoCreateOK)
	created := f.videoT(t, apiKey, http.MethodPost, "/v1/videos", fullchainGlmVideoCreateBody)
	if created.Status != http.StatusOK {
		t.Fatalf("MV glm create status=%d body=%s", created.Status, created.Body)
	}
	job := decodeVideoJob(t, created)
	jobID := str(job["id"])
	if !strings.HasPrefix(jobID, "video_") {
		t.Fatalf("MV glm job id 缺少 video_ 前缀: %#v", job)
	}
	if job["status"] != "in_progress" {
		t.Fatalf("MV glm create status 字段 = %v, want in_progress（task_status=PROCESSING 归一）", job["status"])
	}
	if job["provider"] != "glm" {
		t.Fatalf("MV glm provider = %v, want glm", job["provider"])
	}
	providerJobID := str(job["provider_job_id"])
	if len(providerJobID) != 12 || strings.ContainsAny(providerJobID, "/-") {
		t.Fatalf("MV glm provider_job_id = %q, want 12 hex 任务 id 形态", providerJobID)
	}
	applied := fmt.Sprintf("%v", job["params_applied"])
	for _, want := range []string{"model", "prompt", "size", "negative_prompt", "seconds", "audio"} {
		if !strings.Contains(applied, want) {
			t.Fatalf("MV glm params_applied 缺少 %s: %v", want, job["params_applied"])
		}
	}
	if !strings.Contains(fmt.Sprintf("%v", job["params_ignored"]), "seed") {
		t.Fatalf("MV glm params_ignored 缺少 seed（glm 请求面无 seed）: %v", job["params_ignored"])
	}

	// 轮询两次：in_progress（PROCESSING）→ completed（SUCCESS + url 冻结）。
	first := f.videoT(t, apiKey, http.MethodGet, "/v1/videos/"+jobID, "")
	if first.Status != http.StatusOK {
		t.Fatalf("MV glm poll#1 status=%d body=%s", first.Status, first.Body)
	}
	if object := decodeVideoJob(t, first); object["status"] != "in_progress" {
		t.Fatalf("MV glm poll#1 status 字段 = %v, want in_progress", object["status"])
	}
	second := f.videoT(t, apiKey, http.MethodGet, "/v1/videos/"+jobID, "")
	if second.Status != http.StatusOK {
		t.Fatalf("MV glm poll#2 status=%d body=%s", second.Status, second.Body)
	}
	if object := decodeVideoJob(t, second); object["status"] != "completed" {
		t.Fatalf("MV glm poll#2 status 字段 = %v, want completed: %v", object["status"], object)
	}

	// content 下载：completed 的产物 url 是引擎渲染的绝对 URL，网关无凭据
	// 直连（video/mp4 + ftyp magic bytes）。
	content := f.videoT(t, apiKey, http.MethodGet, "/v1/videos/"+jobID+"/content", "")
	if content.Status != http.StatusOK {
		t.Fatalf("MV glm content status=%d body=%s", content.Status, content.Body)
	}
	if !strings.Contains(content.ContentType, "video/mp4") {
		t.Fatalf("MV glm content-type=%q, want video/mp4", content.ContentType)
	}
	payload := []byte(content.Body)
	if len(payload) < 12 || string(payload[4:8]) != "ftyp" {
		t.Fatalf("MV glm content 非 mp4 载荷（ftyp magic bytes 缺失）: % x", payload[:min(12, len(payload))])
	}

	// 管理面 media-jobs：行终态 completed 归因 glm 账户；按次计费（2026-10-05
	// 按次计费批）：cogvideox-3 目录行 $0.2/次，一次任务 = 一次调用，无秒
	// 计量也按次计（契约 §2.8 按次计费）。
	row := f.waitMediaJobRow(t, apiKeyID, jobID, func(row fullchainMediaJobRow) bool {
		return row.Status == "completed"
	}, "completed")
	if row.AccountID != accountID {
		t.Fatalf("MV glm row accountId=%s, want %s", row.AccountID, accountID)
	}
	if row.CostUsd != 0.2 {
		t.Fatalf("MV glm row costUsd=%v, want 0.2（按次计费 VideoOutputCostPerCall × 1）", row.CostUsd)
	}

	// 账户亲和 + 出站形态：glm key 命中创建（videos/generations）与两轮轮询
	//（GET async-result/{id}），无其它带凭据流量打到该 key（content 直连无
	// Authorization，不落入该 key 的命中记录）。
	creates, polls := 0, 0
	for _, call := range f.mock.callsByKey(key) {
		switch {
		case call.Method == http.MethodPost && call.Path == "/api/paas/v4/videos/generations":
			creates++
		case call.Method == http.MethodGet && strings.HasPrefix(call.Path, "/api/paas/v4/async-result/"):
			polls++
		default:
			t.Fatalf("MV glm 未预期的上游请求: %#v", call)
		}
	}
	if creates != 1 || polls != 2 {
		t.Fatalf("MV glm 上游命中 creates=%d polls=%d, want 1/2", creates, polls)
	}
}

// ---------------------------------------------------------------------------
// 用例 7：MiniMax Hailuo 全生命周期（media_minimax_*，M3/契约 §8.1）
// ---------------------------------------------------------------------------

// fullchainMinimaxVideoCreateBody 是 minimax 视频创建请求体：seconds 数值
// 直传 duration、input_reference（base64 data URL）进 first_frame_image；
// seed 对 minimax 请求面不存在 → params_ignored 回显；prompt_optimizer 经
// provider_options.minimax 命中 → provider_options_applied 回显。
const fullchainMinimaxVideoCreateBody = `{"model":"MiniMax-Hailuo-2.3","prompt":"一只猫在弹钢琴","seconds":"6","input_reference":"data:image/png;base64,aGVsbG8=","seed":42,"provider_options":{"minimax":{"prompt_optimizer":true}}}`

// fullchainCreateMinimaxVideoAccount 经管理面创建指向 mock 上游的 minimax
// api_key 账户并绑定分组（minimax 供应商 + 媒体档案 profile_minimax_openai_
// v1；目录 seed 预置 MiniMax-Hailuo-2.3 行）；凭据显式声明 openai 族词表的
// video_* 端点模式（opt-in，minimax 档案 Capabilities 只声明媒体——无 chat
// 模式，本用例同时覆盖该写侧门禁）。
func (f *fullchainFixture) fullchainCreateMinimaxVideoAccount(name, upstreamKey, groupID string) string {
	f.t.Helper()
	_, created := f.admin.do(http.MethodPost, "/__aisys__/api/accounts", map[string]any{
		"providerCode":              "minimax",
		"providerProtocolProfileId": "profile_minimax_openai_v1",
		"name":                      name,
		"type":                      "api_key",
		"credentials": map[string]any{
			"api_key":  upstreamKey,
			"base_url": f.mock.server.URL,
			"supported_endpoint_modes": []string{
				"video_create", "video_get", "video_content", "video_cancel",
			},
		},
		"supportedModels": []string{"MiniMax-Hailuo-2.3"},
		"status":          "active",
		"groupId":         groupID,
	}, wantStatus(http.StatusCreated))
	accountID := str(data(created)["id"])
	if accountID == "" {
		f.t.Fatalf("minimax video account create payload wrong: %#v", created)
	}
	return accountID
}

// TestFullchainMediaVideoMinimaxHailuo 验证 M3 minimax（Hailuo）视频链主流程
// （契约 §8.1）：管理面配置（minimax 分组 + Hailuo 模型账户，媒体档案无
// chat 模式）→ 创建（video_generation 报文改写、统一 job 对象、provider=
// minimax、params 回显 seed 进 ignored / provider_options_applied 命中）→
// 轮询 queued → queued → completed（status 归一 + file_download_url 冻结）→
// content 经绝对 URL 无凭据直连下载 mp4 → 管理面 media-jobs 行终态
//（按次计费口径 cost=$0.28，2026-10-05 按次计费批）→ 账户亲和（上游命中
// 只打 minimax 账户 key 的 video_generation/query/files 端点）。
func TestFullchainMediaVideoMinimaxHailuo(t *testing.T) {
	requireFullchainGate(t)
	f := startFullchainFixture(t)

	key := fullchainUpstreamKey(t, "mvmx")
	groupID := f.createGroupWithProvider("MVminimax组", "minimax")
	accountID := f.fullchainCreateMinimaxVideoAccount("全链路-MVminimax账户", key, groupID)
	strategyID := f.createStrategy("全链路-MVminimax策略", "normal", []map[string]any{
		{"groupId": groupID, "priority": 1, "weight": 100},
	}, nil)
	apiKey := f.createAPIKey("全链路-MVminimax-Key", strategyID)
	apiKeyID := f.apiKeyIDByName("全链路-MVminimax-Key")

	// 创建：200 + 统一 job 对象（对外 id video_ 前缀、status queued、
	// provider_job_id 为 16 hex 任务 id、params_applied 含 seconds/
	// input_reference、params_ignored 含 seed、provider_options_applied 含
	// minimax）。
	f.mock.script(key, platformmock.ScenarioMediaMinimaxCreateOK)
	created := f.videoT(t, apiKey, http.MethodPost, "/v1/videos", fullchainMinimaxVideoCreateBody)
	if created.Status != http.StatusOK {
		t.Fatalf("MV minimax create status=%d body=%s", created.Status, created.Body)
	}
	job := decodeVideoJob(t, created)
	jobID := str(job["id"])
	if !strings.HasPrefix(jobID, "video_") {
		t.Fatalf("MV minimax job id 缺少 video_ 前缀: %#v", job)
	}
	if job["status"] != "queued" {
		t.Fatalf("MV minimax create status 字段 = %v, want queued（创建响应无 status 字段，已受理未开始）", job["status"])
	}
	if job["provider"] != "minimax" {
		t.Fatalf("MV minimax provider = %v, want minimax", job["provider"])
	}
	providerJobID := str(job["provider_job_id"])
	if len(providerJobID) != 16 || strings.ContainsAny(providerJobID, "/-") {
		t.Fatalf("MV minimax provider_job_id = %q, want 16 hex 任务 id 形态", providerJobID)
	}
	applied := fmt.Sprintf("%v", job["params_applied"])
	for _, want := range []string{"model", "prompt", "seconds", "input_reference"} {
		if !strings.Contains(applied, want) {
			t.Fatalf("MV minimax params_applied 缺少 %s: %v", want, job["params_applied"])
		}
	}
	if !strings.Contains(fmt.Sprintf("%v", job["params_ignored"]), "seed") {
		t.Fatalf("MV minimax params_ignored 缺少 seed（minimax 请求面无 seed）: %v", job["params_ignored"])
	}
	if !strings.Contains(fmt.Sprintf("%v", job["provider_options_applied"]), "prompt_optimizer") {
		t.Fatalf("MV minimax provider_options_applied 缺少 prompt_optimizer（minimax 子对象命中键名回显）: %v", job["provider_options_applied"])
	}

	// 轮询三次：queued（Preparing）→ queued（Queueing）→ completed
	//（Success + file_download_url 冻结）。
	for index, want := range []string{"queued", "queued", "completed"} {
		polled := f.videoT(t, apiKey, http.MethodGet, "/v1/videos/"+jobID, "")
		if polled.Status != http.StatusOK {
			t.Fatalf("MV minimax poll #%d status=%d body=%s", index+1, polled.Status, polled.Body)
		}
		if object := decodeVideoJob(t, polled); object["status"] != want {
			t.Fatalf("MV minimax poll #%d status 字段 = %v, want %s", index+1, object["status"], want)
		}
	}

	// content 下载：completed 的产物 url 是引擎渲染的绝对 URL，网关无凭据
	// 直连（video/mp4 + ftyp magic bytes）。
	content := f.videoT(t, apiKey, http.MethodGet, "/v1/videos/"+jobID+"/content", "")
	if content.Status != http.StatusOK {
		t.Fatalf("MV minimax content status=%d body=%s", content.Status, content.Body)
	}
	if !strings.Contains(content.ContentType, "video/mp4") {
		t.Fatalf("MV minimax content-type=%q, want video/mp4", content.ContentType)
	}
	payload := []byte(content.Body)
	if len(payload) < 12 || string(payload[4:8]) != "ftyp" {
		t.Fatalf("MV minimax content 非 mp4 载荷（ftyp magic bytes 缺失）: % x", payload[:min(12, len(payload))])
	}

	// 管理面 media-jobs：行终态 completed 归因 minimax 账户；按次计费
	//（2026-10-05 按次计费批）：Hailuo 目录行按条明文主档 $0.28/次（768P-6s），
	// 一次任务 = 一次调用，无秒计量也按次计（契约 §2.8 按次计费）。
	row := f.waitMediaJobRow(t, apiKeyID, jobID, func(row fullchainMediaJobRow) bool {
		return row.Status == "completed"
	}, "completed")
	if row.AccountID != accountID {
		t.Fatalf("MV minimax row accountId=%s, want %s", row.AccountID, accountID)
	}
	if row.CostUsd != 0.28 {
		t.Fatalf("MV minimax row costUsd=%v, want 0.28（按次计费 VideoOutputCostPerCall × 1）", row.CostUsd)
	}

	// 账户亲和 + 出站形态：minimax key 命中创建（POST /v1/video_generation）
	// 与三轮轮询（GET /v1/query/video_generation），无其它带凭据流量打到该
	// key（content 直连无 Authorization，不落入该 key 的命中记录）。
	creates, polls := 0, 0
	for _, call := range f.mock.callsByKey(key) {
		switch {
		case call.Method == http.MethodPost && call.Path == "/v1/video_generation":
			creates++
		case call.Method == http.MethodGet && call.Path == "/v1/query/video_generation":
			polls++
		default:
			t.Fatalf("MV minimax 未预期的上游请求: %#v", call)
		}
	}
	if creates != 1 || polls != 3 {
		t.Fatalf("MV minimax 上游命中 creates=%d polls=%d, want 1/3", creates, polls)
	}
}

// ---------------------------------------------------------------------------
// 用例 8：火山方舟 Seedance 全生命周期（media_volcengine_*，M3/契约 §9.1）
// ---------------------------------------------------------------------------

// fullchainVolcengineVideoCreateBody 是 volcengine 视频创建请求体：size
// 1280x720 换算 resolution 720p + ratio 16:9、seconds 5 直传 duration、seed
// 直传、input_reference（base64 data URL）进 content[].image_url 直传；
// negative_prompt/audio 对 volcengine 请求面不存在 → params_ignored 回显；
// camera_fixed 经 provider_options.volcengine 命中 → provider_options_applied
// 回显。
const fullchainVolcengineVideoCreateBody = `{"model":"doubao-seedance-1-0-pro-250528","prompt":"一只猫在弹钢琴","seconds":5,"size":"1280x720","seed":42,"input_reference":"data:image/png;base64,aGVsbG8=","negative_prompt":"模糊","audio":false,"provider_options":{"volcengine":{"camera_fixed":true}}}`

// fullchainCreateVolcengineVideoAccount 经管理面创建指向 mock 上游的
// volcengine api_key 账户并绑定分组（volcengine 供应商 + 媒体档案
// profile_volcengine_openai_v1；目录 seed 预置 doubao-seedance-1-0-pro-250528
// 行）；凭据显式声明 openai 族词表的 video_* 端点模式（opt-in，volcengine
// 档案 Capabilities 只声明视频——无 chat/audio 模式，本用例同时覆盖该写侧
// 门禁）。
func (f *fullchainFixture) fullchainCreateVolcengineVideoAccount(name, upstreamKey, groupID string) string {
	f.t.Helper()
	_, created := f.admin.do(http.MethodPost, "/__aisys__/api/accounts", map[string]any{
		"providerCode":              "volcengine",
		"providerProtocolProfileId": "profile_volcengine_openai_v1",
		"name":                      name,
		"type":                      "api_key",
		"credentials": map[string]any{
			"api_key":  upstreamKey,
			"base_url": f.mock.server.URL,
			"supported_endpoint_modes": []string{
				"video_create", "video_get", "video_content", "video_cancel",
			},
		},
		"supportedModels": []string{"doubao-seedance-1-0-pro-250528"},
		"status":          "active",
		"groupId":         groupID,
	}, wantStatus(http.StatusCreated))
	accountID := str(data(created)["id"])
	if accountID == "" {
		f.t.Fatalf("volcengine video account create payload wrong: %#v", created)
	}
	return accountID
}

// TestFullchainMediaVideoVolcengineSeedance 验证 M3 volcengine（Seedance）视频
// 链主流程（契约 §9.1）：管理面配置（volcengine 分组 + Seedance 模型账户，
// 媒体档案无 chat 模式）→ 创建（contents/generations/tasks 报文改写、统一
// job 对象、provider=volcengine、params 回显 negative_prompt/audio 进 ignored /
// provider_options_applied 命中）→ 轮询 queued → in_progress → completed
//（status 归一 + content.video_url 冻结）→ content 经绝对 URL 无凭据直连下载
// mp4 → 管理面 media-jobs 行终态（usage_missing 口径 cost=0，不虚计）→ 账户
// 亲和（上游命中只打 volcengine 账户 key 的 tasks 创建/轮询端点）。
func TestFullchainMediaVideoVolcengineSeedance(t *testing.T) {
	requireFullchainGate(t)
	f := startFullchainFixture(t)

	key := fullchainUpstreamKey(t, "mvve")
	groupID := f.createGroupWithProvider("MVvolcengine组", "volcengine")
	accountID := f.fullchainCreateVolcengineVideoAccount("全链路-MVvolcengine账户", key, groupID)
	strategyID := f.createStrategy("全链路-MVvolcengine策略", "normal", []map[string]any{
		{"groupId": groupID, "priority": 1, "weight": 100},
	}, nil)
	apiKey := f.createAPIKey("全链路-MVvolcengine-Key", strategyID)
	apiKeyID := f.apiKeyIDByName("全链路-MVvolcengine-Key")

	// 创建：200 + 统一 job 对象（对外 id video_ 前缀、status queued、
	// provider_job_id 为 cgt-<16hex> 任务 id、params_applied 含 seconds/size/
	// seed/input_reference、params_ignored 含 negative_prompt/audio、
	// provider_options_applied 含 volcengine）。
	f.mock.script(key, platformmock.ScenarioMediaVolcengineCreateOK)
	created := f.videoT(t, apiKey, http.MethodPost, "/v1/videos", fullchainVolcengineVideoCreateBody)
	if created.Status != http.StatusOK {
		t.Fatalf("MV volcengine create status=%d body=%s", created.Status, created.Body)
	}
	job := decodeVideoJob(t, created)
	jobID := str(job["id"])
	if !strings.HasPrefix(jobID, "video_") {
		t.Fatalf("MV volcengine job id 缺少 video_ 前缀: %#v", job)
	}
	if job["status"] != "queued" {
		t.Fatalf("MV volcengine create status 字段 = %v, want queued", job["status"])
	}
	if job["provider"] != "volcengine" {
		t.Fatalf("MV volcengine provider = %v, want volcengine", job["provider"])
	}
	providerJobID := str(job["provider_job_id"])
	if !strings.HasPrefix(providerJobID, "cgt-") || len(providerJobID) != len("cgt-")+16 {
		t.Fatalf("MV volcengine provider_job_id = %q, want cgt-<16hex> 任务 id 形态", providerJobID)
	}
	applied := fmt.Sprintf("%v", job["params_applied"])
	for _, want := range []string{"model", "prompt", "seconds", "size", "seed", "input_reference"} {
		if !strings.Contains(applied, want) {
			t.Fatalf("MV volcengine params_applied 缺少 %s: %v", want, job["params_applied"])
		}
	}
	ignoredList := fmt.Sprintf("%v", job["params_ignored"])
	for _, want := range []string{"negative_prompt", "audio"} {
		if !strings.Contains(ignoredList, want) {
			t.Fatalf("MV volcengine params_ignored 缺少 %s（volcengine 请求面无对应字段）: %v", want, job["params_ignored"])
		}
	}
	if !strings.Contains(fmt.Sprintf("%v", job["provider_options_applied"]), "camera_fixed") {
		t.Fatalf("MV volcengine provider_options_applied 缺少 camera_fixed（volcengine 子对象命中键名回显）: %v", job["provider_options_applied"])
	}

	// 轮询三次：queued → in_progress（running 归一）→ completed（succeeded +
	// content.video_url 冻结）。
	for index, want := range []string{"queued", "in_progress", "completed"} {
		polled := f.videoT(t, apiKey, http.MethodGet, "/v1/videos/"+jobID, "")
		if polled.Status != http.StatusOK {
			t.Fatalf("MV volcengine poll #%d status=%d body=%s", index+1, polled.Status, polled.Body)
		}
		if object := decodeVideoJob(t, polled); object["status"] != want {
			t.Fatalf("MV volcengine poll #%d status 字段 = %v, want %s", index+1, object["status"], want)
		}
	}

	// content 下载：completed 的产物 url 是引擎渲染的绝对 URL，网关无凭据
	// 直连（video/mp4 + ftyp magic bytes）。
	content := f.videoT(t, apiKey, http.MethodGet, "/v1/videos/"+jobID+"/content", "")
	if content.Status != http.StatusOK {
		t.Fatalf("MV volcengine content status=%d body=%s", content.Status, content.Body)
	}
	if !strings.Contains(content.ContentType, "video/mp4") {
		t.Fatalf("MV volcengine content-type=%q, want video/mp4", content.ContentType)
	}
	payload := []byte(content.Body)
	if len(payload) < 12 || string(payload[4:8]) != "ftyp" {
		t.Fatalf("MV volcengine content 非 mp4 载荷（ftyp magic bytes 缺失）: % x", payload[:min(12, len(payload))])
	}

	// 管理面 media-jobs：行终态 completed 归因 volcengine 账户；请求 seconds=5
	// 生效、上游无回报 → usage 行按请求参数自算计量（usage_source=request，
	// 2026-10-05 批）；seedance 目录未落秒价 → cost_usd=0（计量照落成本不虚计）。
	row := f.waitMediaJobRow(t, apiKeyID, jobID, func(row fullchainMediaJobRow) bool {
		return row.Status == "completed"
	}, "completed")
	if row.AccountID != accountID {
		t.Fatalf("MV volcengine row accountId=%s, want %s", row.AccountID, accountID)
	}
	if row.CostUsd != 0 {
		t.Fatalf("MV volcengine row costUsd=%v, want 0（usage_missing 不虚计）", row.CostUsd)
	}

	// 账户亲和 + 出站形态：volcengine key 命中创建（POST /api/v3/contents/
	// generations/tasks）与三轮轮询（GET 同路径 /{id}），无其它带凭据流量打到
	// 该 key（content 直连无 Authorization，不落入该 key 的命中记录）。
	creates, polls := 0, 0
	for _, call := range f.mock.callsByKey(key) {
		switch {
		case call.Method == http.MethodPost && call.Path == "/api/v3/contents/generations/tasks":
			creates++
		case call.Method == http.MethodGet && strings.HasPrefix(call.Path, "/api/v3/contents/generations/tasks/"):
			polls++
		default:
			t.Fatalf("MV volcengine 未预期的上游请求: %#v", call)
		}
	}
	if creates != 1 || polls != 3 {
		t.Fatalf("MV volcengine 上游命中 creates=%d polls=%d, want 1/3", creates, polls)
	}
}

// ---------------------------------------------------------------------------
// 用例 9：通义百炼万相全生命周期（media_qwen_*，M3 第五批/契约 §10.1）
// ---------------------------------------------------------------------------

// fullchainQwenVideoCreateBody 是 qwen 视频创建请求体：size 1920x1080 换算
// parameters.size "1920*1080"（x→* 星号格式转换）、seconds 5 直传
// parameters.duration、seed 直传、input_reference（base64 data URL）进
// input.img_url 直传、negative_prompt 进 input.negative_prompt（官方原生
// 字段，M3 首个 negative_prompt 生效的视频供应商）；audio 对 qwen 请求面
// 无生成开关 → params_ignored 回显；prompt_extend 经 provider_options.qwen
// 命中 → provider_options_applied 回显。
const fullchainQwenVideoCreateBody = `{"model":"wan2.2-t2v-plus","prompt":"一只猫在弹钢琴","seconds":5,"size":"1920x1080","seed":42,"input_reference":"data:image/png;base64,aGVsbG8=","negative_prompt":"模糊","audio":false,"provider_options":{"qwen":{"parameters":{"prompt_extend":true}}}}`

// fullchainCreateQwenVideoAccount 经管理面创建指向 mock 上游的 qwen api_key
// 账户并绑定分组（qwen 供应商 + 媒体档案 profile_qwen_openai_v1；目录 seed
// 预置 wan2.2-t2v-plus 行）；凭据显式声明 openai 族词表的 video_* 端点模式
//（opt-in，qwen 档案 Capabilities 只声明视频——无 chat/audio 模式，本用例
// 同时覆盖该写侧门禁）。
func (f *fullchainFixture) fullchainCreateQwenVideoAccount(name, upstreamKey, groupID string) string {
	f.t.Helper()
	_, created := f.admin.do(http.MethodPost, "/__aisys__/api/accounts", map[string]any{
		"providerCode":              "qwen",
		"providerProtocolProfileId": "profile_qwen_openai_v1",
		"name":                      name,
		"type":                      "api_key",
		"credentials": map[string]any{
			"api_key":  upstreamKey,
			"base_url": f.mock.server.URL,
			"supported_endpoint_modes": []string{
				"video_create", "video_get", "video_content", "video_cancel",
			},
		},
		"supportedModels": []string{"wan2.2-t2v-plus"},
		"status":          "active",
		"groupId":         groupID,
	}, wantStatus(http.StatusCreated))
	accountID := str(data(created)["id"])
	if accountID == "" {
		f.t.Fatalf("qwen video account create payload wrong: %#v", created)
	}
	return accountID
}

// TestFullchainMediaVideoQwenWanx 验证 M3 第五批 qwen（万相）视频链主流程
//（契约 §10.1）：管理面配置（qwen 分组 + 万相模型账户，媒体档案无 chat
// 模式）→ 创建（video-synthesis 报文改写 + X-DashScope-Async 异步头——mock
// 引擎对无头创建按上游语义拒绝 400，创建成功本身即异步头到达的行为断言、
// 统一 job 对象、provider=qwen、params 回显 negative_prompt 生效 / audio 进
// ignored / provider_options_applied 命中）→ 轮询 queued → in_progress →
// completed（task_status 归一 + output.video_url 冻结）→ content 经绝对 URL
// 无凭据直连下载 mp4 → 管理面 media-jobs 行终态（usage JSON 字符串解析出
// 5 秒计量照落、目录无 USD 秒价 cost=0 不虚计）→ 账户亲和（上游命中只打
// qwen 账户 key 的 video-synthesis 创建与 /api/v1/tasks 轮询端点）。
func TestFullchainMediaVideoQwenWanx(t *testing.T) {
	requireFullchainGate(t)
	f := startFullchainFixture(t)

	key := fullchainUpstreamKey(t, "mvqw")
	groupID := f.createGroupWithProvider("MVqwen组", "qwen")
	accountID := f.fullchainCreateQwenVideoAccount("全链路-MVqwen账户", key, groupID)
	strategyID := f.createStrategy("全链路-MVqwen策略", "normal", []map[string]any{
		{"groupId": groupID, "priority": 1, "weight": 100},
	}, nil)
	apiKey := f.createAPIKey("全链路-MVqwen-Key", strategyID)
	apiKeyID := f.apiKeyIDByName("全链路-MVqwen-Key")

	// 创建：200 + 统一 job 对象（对外 id video_ 前缀、status queued、
	// provider_job_id 为 uuid 任务 id、params_applied 含 seconds/size/seed/
	// input_reference/negative_prompt、params_ignored 含 audio、
	// provider_options_applied 含 qwen 子对象键名）。X-DashScope-Async 头
	// 由 mock 引擎的创建门禁校验（缺失即 400，契约 §10.1）——创建成功即
	// 证明链上已注入异步头（链级测试另直接断言头值）。
	f.mock.script(key, platformmock.ScenarioMediaQwenCreatePending)
	created := f.videoT(t, apiKey, http.MethodPost, "/v1/videos", fullchainQwenVideoCreateBody)
	if created.Status != http.StatusOK {
		t.Fatalf("MV qwen create status=%d body=%s", created.Status, created.Body)
	}
	job := decodeVideoJob(t, created)
	jobID := str(job["id"])
	if !strings.HasPrefix(jobID, "video_") {
		t.Fatalf("MV qwen job id 缺少 video_ 前缀: %#v", job)
	}
	if job["status"] != "queued" {
		t.Fatalf("MV qwen create status 字段 = %v, want queued", job["status"])
	}
	if job["provider"] != "qwen" {
		t.Fatalf("MV qwen provider = %v, want qwen", job["provider"])
	}
	providerJobID := str(job["provider_job_id"])
	if len(providerJobID) != 36 || !strings.Contains(providerJobID, "-") {
		t.Fatalf("MV qwen provider_job_id = %q, want uuid 任务 id 形态", providerJobID)
	}
	applied := fmt.Sprintf("%v", job["params_applied"])
	for _, want := range []string{"model", "prompt", "seconds", "size", "seed", "input_reference", "negative_prompt"} {
		if !strings.Contains(applied, want) {
			t.Fatalf("MV qwen params_applied 缺少 %s: %v", want, job["params_applied"])
		}
	}
	ignoredList := fmt.Sprintf("%v", job["params_ignored"])
	if !strings.Contains(ignoredList, "audio") {
		t.Fatalf("MV qwen params_ignored 缺少 audio（qwen 请求面无生成音频开关）: %v", job["params_ignored"])
	}
	if !strings.Contains(fmt.Sprintf("%v", job["provider_options_applied"]), "parameters") {
		t.Fatalf("MV qwen provider_options_applied 缺少 parameters（qwen 子对象命中键名回显）: %v", job["provider_options_applied"])
	}

	// 轮询三次：queued → in_progress（RUNNING 归一）→ completed（SUCCEEDED +
	// output.video_url 冻结 + usage JSON 字符串解析出 5 秒计量）。
	for index, want := range []string{"queued", "in_progress", "completed"} {
		polled := f.videoT(t, apiKey, http.MethodGet, "/v1/videos/"+jobID, "")
		if polled.Status != http.StatusOK {
			t.Fatalf("MV qwen poll #%d status=%d body=%s", index+1, polled.Status, polled.Body)
		}
		if object := decodeVideoJob(t, polled); object["status"] != want {
			t.Fatalf("MV qwen poll #%d status 字段 = %v, want %s", index+1, object["status"], want)
		}
	}

	// content 下载：completed 的产物 url 是引擎渲染的绝对 URL，网关无凭据
	// 直连（video/mp4 + ftyp magic bytes）。
	content := f.videoT(t, apiKey, http.MethodGet, "/v1/videos/"+jobID+"/content", "")
	if content.Status != http.StatusOK {
		t.Fatalf("MV qwen content status=%d body=%s", content.Status, content.Body)
	}
	if !strings.Contains(content.ContentType, "video/mp4") {
		t.Fatalf("MV qwen content-type=%q, want video/mp4", content.ContentType)
	}
	payload := []byte(content.Body)
	if len(payload) < 12 || string(payload[4:8]) != "ftyp" {
		t.Fatalf("MV qwen content 非 mp4 载荷（ftyp magic bytes 缺失）: % x", payload[:min(12, len(payload))])
	}

	// 管理面 media-jobs：行终态 completed 归因 qwen 账户；usage JSON 字符串
	// 解析出 5 秒计量（链级测试断言 outputVideoSeconds=5 的 spool 记录），按
	// 目录 USD 秒价计费（2026-10-05 补价：wan2.2-t2v-plus $0.1/s）→
	// cost_usd=0.5（5s × $0.1，与链级测试同口径；本断言随定价落库回正）。
	row := f.waitMediaJobRow(t, apiKeyID, jobID, func(row fullchainMediaJobRow) bool {
		return row.Status == "completed"
	}, "completed")
	if row.AccountID != accountID {
		t.Fatalf("MV qwen row accountId=%s, want %s", row.AccountID, accountID)
	}
	if row.CostUsd != 0.5 {
		t.Fatalf("MV qwen row costUsd=%v, want 0.5（$0.1/s × 5s）", row.CostUsd)
	}

	// 账户亲和 + 出站形态：qwen key 命中创建（POST /api/v1/services/aigc/
	// video-generation/video-synthesis）与三轮轮询（GET /api/v1/tasks/{id}），
	// 无其它带凭据流量打到该 key（content 直连无 Authorization，不落入该 key
	// 的命中记录）。
	creates, polls := 0, 0
	for _, call := range f.mock.callsByKey(key) {
		switch {
		case call.Method == http.MethodPost && call.Path == "/api/v1/services/aigc/video-generation/video-synthesis":
			creates++
		case call.Method == http.MethodGet && strings.HasPrefix(call.Path, "/api/v1/tasks/"):
			polls++
		default:
			t.Fatalf("MV qwen 未预期的上游请求: %#v", call)
		}
	}
	if creates != 1 || polls != 3 {
		t.Fatalf("MV qwen 上游命中 creates=%d polls=%d, want 1/3", creates, polls)
	}
}

// ---------------------------------------------------------------------------
// 用例 10：hybrid 供应商媒体映射全链路（M4b，媒体设计 §9 hybrid 行）
// ---------------------------------------------------------------------------

// fullchainCreateHybridVideoAccount 经管理面创建指向 mock 上游的 hybrid
// api_key 账户并绑定分组（hybrid 供应商 + 通用 openai v1 档案 profile_
// hybrid_openai_chat_v1）。账号媒体映射 source sora-2-pro → upstream sora-2
//（video_generation 族，写侧矩阵/协议模型池放行的端到端覆盖）；supportedModels
// 只声明 upstream 名（候选过滤必须经映射命中，不得直连 source 名）；凭据声明
// openai 族词表的 video_* 端点模式（opt-in，hybrid 模式词表并集）。
func (f *fullchainFixture) fullchainCreateHybridVideoAccount(name, upstreamKey, groupID string) string {
	f.t.Helper()
	_, created := f.admin.do(http.MethodPost, "/__aisys__/api/accounts", map[string]any{
		"providerCode":              "hybrid",
		"providerProtocolProfileId": "profile_hybrid_openai_chat_v1",
		"name":                      name,
		"type":                      "api_key",
		"credentials": map[string]any{
			"api_key":  upstreamKey,
			"base_url": f.mock.server.URL,
			"supported_endpoint_modes": []string{
				"chat_json", "chat_sse", "video_create", "video_get", "video_content", "video_cancel",
			},
		},
		"supportedModels": []string{"sora-2"},
		"modelMappings": []map[string]any{{
			"sourceModel": "sora-2-pro", "sourceEndpointFamily": "video_generation",
			"upstreamModel": "sora-2", "upstreamEndpointFamily": "video_generation",
		}},
		"status":  "active",
		"groupId": groupID,
	}, wantStatus(http.StatusCreated))
	accountID := str(data(created)["id"])
	if accountID == "" {
		f.t.Fatalf("hybrid video account create payload wrong: %#v", created)
	}
	return accountID
}

// TestFullchainMediaVideoHybridMapped 验证 M4b（媒体设计 §9 hybrid 行）：
// 管理面配置（hybrid 分组 + 媒体映射账户，写侧矩阵与协议模型池放行）→ 客户端
// 请求 source 模型名 sora-2-pro → 候选过滤经映射命中 → 创建链把出站 model
// 改写为 upstream 名 sora-2 打到中转 base_url 的 /v1/videos（mock 命中记录
// 断言）→ 统一 job 对象 provider=openai（hybrid 媒体执行面 = 中转的 OpenAI
// 形态媒体端点）→ 轮询 completed → 管理面 media-jobs 行归因 hybrid 账户。
func TestFullchainMediaVideoHybridMapped(t *testing.T) {
	requireFullchainGate(t)
	f := startFullchainFixture(t)

	key := fullchainUpstreamKey(t, "mvhb")
	groupID := f.createGroupWithProvider("MVhybrid组", "hybrid")
	accountID := f.fullchainCreateHybridVideoAccount("全链路-MVhybrid账户", key, groupID)
	strategyID := f.createStrategy("全链路-MVhybrid策略", "normal", []map[string]any{
		{"groupId": groupID, "priority": 1, "weight": 100},
	}, nil)
	apiKey := f.createAPIKey("全链路-MVhybrid-Key", strategyID)
	apiKeyID := f.apiKeyIDByName("全链路-MVhybrid-Key")

	// 创建：请求模型是 source 名 sora-2-pro；200 + 统一 job 对象（对外 id
	// video_ 前缀、status queued、provider=openai）。
	f.mock.script(key, platformmock.ScenarioMediaVideoOKPoll1)
	created := f.videoT(t, apiKey, http.MethodPost, "/v1/videos",
		`{"model":"sora-2-pro","prompt":"一只猫在弹钢琴","seconds":"4","size":"1280x720"}`)
	if created.Status != http.StatusOK {
		t.Fatalf("MV hybrid create status=%d body=%s", created.Status, created.Body)
	}
	job := decodeVideoJob(t, created)
	jobID := str(job["id"])
	if !strings.HasPrefix(jobID, "video_") {
		t.Fatalf("MV hybrid job id 缺少 video_ 前缀: %#v", job)
	}
	if job["status"] != "queued" {
		t.Fatalf("MV hybrid create status 字段 = %v, want queued", job["status"])
	}
	if job["provider"] != "openai" {
		t.Fatalf("MV hybrid provider = %v, want openai（hybrid 媒体执行面为中转 OpenAI 形态媒体端点）", job["provider"])
	}
	// params 回显的 model 是改写后的 upstream 名（不得回显 source 名）。
	if strings.Contains(fmt.Sprintf("%v", job["params_applied"]), "sora-2-pro") {
		t.Fatalf("MV hybrid params_applied 不得回显 source 模型名: %v", job["params_applied"])
	}

	// 出站模型改写证据：mock 命中记录的 POST /v1/videos 恰好一次且 Model 为
	// upstream 名 sora-2（source 名不出站）。
	creates := 0
	for _, call := range f.mock.callsByKey(key) {
		if call.Method == http.MethodPost && call.Path == "/v1/videos" {
			creates++
			if call.Model != "sora-2" {
				t.Fatalf("MV hybrid 出站创建 model = %q, want upstream 名 sora-2", call.Model)
			}
		}
	}
	if creates != 1 {
		t.Fatalf("MV hybrid 创建出站 %d 次, want 1", creates)
	}

	// 轮询一次到 completed（ok_poll1 快路径），行归因 hybrid 账户。
	polled := f.videoT(t, apiKey, http.MethodGet, "/v1/videos/"+jobID, "")
	if polled.Status != http.StatusOK {
		t.Fatalf("MV hybrid poll status=%d body=%s", polled.Status, polled.Body)
	}
	if object := decodeVideoJob(t, polled); object["status"] != "completed" {
		t.Fatalf("MV hybrid poll status 字段 = %v, want completed", object["status"])
	}
	row := f.waitMediaJobRow(t, apiKeyID, jobID, func(row fullchainMediaJobRow) bool {
		return row.Status == "completed"
	}, "completed")
	if row.AccountID != accountID {
		t.Fatalf("MV hybrid row accountId=%s, want %s", row.AccountID, accountID)
	}
	// 终态计费沿用静态目录秒价（provider=openai + upstream 模型 sora-2 ×
	// seconds_length=4 > 0）。
	if row.CostUsd <= 0 {
		t.Fatalf("MV hybrid row costUsd=%v, want >0（sora-2 秒价计费）", row.CostUsd)
	}
}

// ---------------------------------------------------------------------------
// 用例 11：xAI Grok Imagine Video 全生命周期（media_xai_*，M3 回填池/契约 §6.1）
// ---------------------------------------------------------------------------

// fullchainXaiVideoCreateBody 是 xai 视频创建请求体：size 1280x720 换算
// aspect_ratio 16:9 + resolution 720p、seconds 6 整数化直传 duration、
// input_reference（base64 data URL）进 image 直传、audio=false 直传
// generate_audio；negative_prompt/seed 对 xai 请求面不存在 → params_ignored
// 回显；reference_audios 经 provider_options.xai 命中 → provider_options_
// applied 回显（契约 §6.1 范围外能力经 L3 通道可达）。
const fullchainXaiVideoCreateBody = `{"model":"grok-imagine-video-1.5","prompt":"一只猫在弹钢琴","seconds":6,"size":"1280x720","input_reference":"data:image/png;base64,aGVsbG8=","negative_prompt":"模糊","seed":42,"audio":false,"provider_options":{"xai":{"reference_audios":["voice-1"]}}}`

// fullchainCreateXaiVideoAccount 经管理面创建指向 mock 上游的 xai api_key
// 账户并绑定分组（xai 供应商 + 既有文本档案 profile_xai_openai_v1——与
// volcengine/qwen 专属媒体档案不同，xai 是既有综合供应商，视频能力沿
// openai 族写侧归一 opt-in，无需新档案；目录 seed 预置 grok-imagine-video-1.5
// 行）；凭据显式声明 openai 族词表的 video_* 端点模式（opt-in，写侧门禁
// 沿 xai 的 openai 族归一分支）。
func (f *fullchainFixture) fullchainCreateXaiVideoAccount(name, upstreamKey, groupID string) string {
	f.t.Helper()
	_, created := f.admin.do(http.MethodPost, "/__aisys__/api/accounts", map[string]any{
		"providerCode":              "xai",
		"providerProtocolProfileId": "profile_xai_openai_v1",
		"name":                      name,
		"type":                      "api_key",
		"credentials": map[string]any{
			"api_key":  upstreamKey,
			"base_url": f.mock.server.URL,
			"supported_endpoint_modes": []string{
				"video_create", "video_get", "video_content", "video_cancel",
			},
		},
		"supportedModels": []string{"grok-imagine-video-1.5"},
		"status":          "active",
		"groupId":         groupID,
	}, wantStatus(http.StatusCreated))
	accountID := str(data(created)["id"])
	if accountID == "" {
		f.t.Fatalf("xai video account create payload wrong: %#v", created)
	}
	return accountID
}

// TestFullchainMediaVideoXaiGrokImagine 验证 M3 回填池 xai（Grok Imagine
// Video）视频链主流程（契约 §6.1）：管理面配置（xai 分组 + Grok Imagine 模型
// 账户，既有文本档案 opt-in 视频）→ 创建（报文改写 + 统一 job 对象、
// provider=xai、创建响应无 status 字段空归一 in_progress、params 回显
// negative_prompt/seed 进 ignored / audio 生效进 applied /
// provider_options_applied 命中）→ 轮询 in_progress（pending 归一）→
// completed（done 归一 + video.url 冻结 + duration 秒计量）→ content 经绝对
// URL 无凭据直连下载 mp4 → 终态 usage 行落账（video.duration=6 秒计量照抽，
// 秒数值断言在链级 TestChainMediaXaiVideoFullFlowLifecycle 的 spool 记录）+
// 管理面 media-jobs 行终态（目录未落 USD 秒价 cost=0，不虚计）→ 账户亲和
//（上游命中只打 xai 账户 key 的 /v1/videos/generations 创建与 /v1/videos/
// {request_id} 轮询端点）。
func TestFullchainMediaVideoXaiGrokImagine(t *testing.T) {
	requireFullchainGate(t)
	f := startFullchainFixture(t)

	key := fullchainUpstreamKey(t, "mvxa")
	groupID := f.createGroupWithProvider("MVxai组", "xai")
	accountID := f.fullchainCreateXaiVideoAccount("全链路-MVxai账户", key, groupID)
	strategyID := f.createStrategy("全链路-MVxai策略", "normal", []map[string]any{
		{"groupId": groupID, "priority": 1, "weight": 100},
	}, nil)
	apiKey := f.createAPIKey("全链路-MVxai-Key", strategyID)
	apiKeyID := f.apiKeyIDByName("全链路-MVxai-Key")

	// 创建：200 + 统一 job 对象（对外 id video_ 前缀、status in_progress——
	// 创建响应只有 request_id 无 status 字段，空归一 pending 同义、
	// provider_job_id 为 uuid request_id、params_applied 含 seconds/size/
	// input_reference/audio、params_ignored 含 negative_prompt/seed、
	// provider_options_applied 含 xai 子对象键名）。
	f.mock.script(key, platformmock.ScenarioMediaXaiVideoCreateRequestID)
	created := f.videoT(t, apiKey, http.MethodPost, "/v1/videos", fullchainXaiVideoCreateBody)
	if created.Status != http.StatusOK {
		t.Fatalf("MV xai create status=%d body=%s", created.Status, created.Body)
	}
	job := decodeVideoJob(t, created)
	jobID := str(job["id"])
	if !strings.HasPrefix(jobID, "video_") {
		t.Fatalf("MV xai job id 缺少 video_ 前缀: %#v", job)
	}
	if job["status"] != "in_progress" {
		t.Fatalf("MV xai create status 字段 = %v, want in_progress（创建响应无 status，空归一）", job["status"])
	}
	if job["provider"] != "xai" {
		t.Fatalf("MV xai provider = %v, want xai", job["provider"])
	}
	providerJobID := str(job["provider_job_id"])
	if len(providerJobID) != 36 || !strings.Contains(providerJobID, "-") {
		t.Fatalf("MV xai provider_job_id = %q, want uuid request_id 形态", providerJobID)
	}
	applied := fmt.Sprintf("%v", job["params_applied"])
	for _, want := range []string{"model", "prompt", "seconds", "size", "input_reference", "audio"} {
		if !strings.Contains(applied, want) {
			t.Fatalf("MV xai params_applied 缺少 %s: %v", want, job["params_applied"])
		}
	}
	ignoredList := fmt.Sprintf("%v", job["params_ignored"])
	for _, want := range []string{"negative_prompt", "seed"} {
		if !strings.Contains(ignoredList, want) {
			t.Fatalf("MV xai params_ignored 缺少 %s（xai 请求面无对应字段）: %v", want, job["params_ignored"])
		}
	}
	if !strings.Contains(fmt.Sprintf("%v", job["provider_options_applied"]), "reference_audios") {
		t.Fatalf("MV xai provider_options_applied 缺少 reference_audios（xai 子对象命中键名回显）: %v", job["provider_options_applied"])
	}

	// 轮询两次：in_progress（pending 归一）→ completed（done + video.url
	// 冻结 + duration 秒计量抽取）。
	for index, want := range []string{"in_progress", "completed"} {
		polled := f.videoT(t, apiKey, http.MethodGet, "/v1/videos/"+jobID, "")
		if polled.Status != http.StatusOK {
			t.Fatalf("MV xai poll #%d status=%d body=%s", index+1, polled.Status, polled.Body)
		}
		if object := decodeVideoJob(t, polled); object["status"] != want {
			t.Fatalf("MV xai poll #%d status 字段 = %v, want %s", index+1, object["status"], want)
		}
	}

	// content 下载：completed 的产物 url 是引擎渲染的绝对 URL，网关无凭据
	// 直连（video/mp4 + ftyp magic bytes）。
	content := f.videoT(t, apiKey, http.MethodGet, "/v1/videos/"+jobID+"/content", "")
	if content.Status != http.StatusOK {
		t.Fatalf("MV xai content status=%d body=%s", content.Status, content.Body)
	}
	if !strings.Contains(content.ContentType, "video/mp4") {
		t.Fatalf("MV xai content-type=%q, want video/mp4", content.ContentType)
	}
	payload := []byte(content.Body)
	if len(payload) < 12 || string(payload[4:8]) != "ftyp" {
		t.Fatalf("MV xai content 非 mp4 载荷（ftyp magic bytes 缺失）: % x", payload[:min(12, len(payload))])
	}

	// 管理面 media-jobs：行终态 completed 归因 xai 账户；done 的
	// video.duration=6 秒计量照抽（OutputVideoSeconds>0），目录 $0.08/s
	//（grok-imagine-video-1.5，复核批回正）→ cost_usd=0.48（6s × $0.08，
	// 与链级测试同口径；本断言随定价落库回正）。
	row := f.waitMediaJobRow(t, apiKeyID, jobID, func(row fullchainMediaJobRow) bool {
		return row.Status == "completed"
	}, "completed")
	if row.AccountID != accountID {
		t.Fatalf("MV xai row accountId=%s, want %s", row.AccountID, accountID)
	}
	if row.CostUsd != 0.48 {
		t.Fatalf("MV xai row costUsd=%v, want 0.48（$0.08/s × 6s）", row.CostUsd)
	}
	// 终态 usage 行落账（completed 成功行，endpoint=/v1/videos，模型直达）：
	// usage_missing 语义在链级测试钉住（计量在场时无该标记）。
	f.waitUsageRecords(t, apiKeyID, func(rows []fullchainUsageRecord) bool {
		for _, record := range rows {
			if record.Endpoint == "/v1/videos" && record.Success && record.Model == "grok-imagine-video-1.5" {
				return true
			}
		}
		return false
	}, "xai completed 秒计量终态 usage 行")

	// 账户亲和 + 出站形态：xai key 命中创建（POST /v1/videos/generations）
	// 与两轮轮询（GET /v1/videos/{request_id}），无其它带凭据流量打到该
	// key（content 直连无 Authorization，不落入该 key 的命中记录）。
	creates, polls := 0, 0
	for _, call := range f.mock.callsByKey(key) {
		switch {
		case call.Method == http.MethodPost && call.Path == "/v1/videos/generations":
			creates++
		case call.Method == http.MethodGet && strings.HasPrefix(call.Path, "/v1/videos/") && call.Path != "/v1/videos/generations":
			polls++
		default:
			t.Fatalf("MV xai 未预期的上游请求: %#v", call)
		}
	}
	if creates != 1 || polls != 2 {
		t.Fatalf("MV xai 上游命中 creates=%d polls=%d, want 1/2", creates, polls)
	}
}
