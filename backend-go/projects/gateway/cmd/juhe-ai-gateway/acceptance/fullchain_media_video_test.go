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
